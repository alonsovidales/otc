// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  WSClient.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//

import Foundation

/// An `actor` (not a plain class) because URLSession delivers `receive`/`send`
/// completions on its own background queue, not on whatever thread called
/// into this client. With a plain class those callbacks mutated `waiters`/
/// `nextId` concurrently with calls made from OTCConnection, a real data
/// race that showed up in practice as a heap-corruption crash on a real
/// device once traffic got busy (large file listings). The actor serializes
/// every access to this state, callbacks included.
actor WSClient {
    private var task: URLSessionWebSocketTask?
    private let session: URLSession
    private(set) var connected = false
    private var nextId: Int32 = 1
    private var waiters = [Int32: (Result<Msg_RespEnvelope, Error>) -> Void]()

    /// Fired once, from the receive loop, when the socket breaks (read error
    /// or clean close). The owner (OTCConnection) uses this to mark itself
    /// unauthenticated and schedule a reconnect; WSClient itself does not
    /// retry on its own.
    var onDisconnect: (() -> Void)?

    init() {
        let cfg = URLSessionConfiguration.default
        cfg.waitsForConnectivity = true
        session = URLSession(configuration: cfg)
    }

    func setOnDisconnect(_ cb: @escaping () -> Void) {
        onDisconnect = cb
    }

    func connect(url: URL) async throws {
        print("Trynig to connect to: \(url)")
        if let task, task.state == .running { return }
        let t = session.webSocketTask(with: url)
        // The default is 1 MiB, which a file listing for a few thousand
        // photos blows straight past — every receive then fails with
        // "Message too long" and the connection never gets anywhere. Match
        // the macOS client's generous cap.
        t.maximumMessageSize = 1000 * 1024 * 1024
        task = t
        t.resume()
        connected = true
        print("Connected!!!")
        listen()
    }

    func close() {
        task?.cancel()
        connected = false
    }

    private func listen() {
        task?.receive { [weak self] result in
            guard let self else { return }
            Task { await self.handleReceive(result) }
        }
    }

    private func handleReceive(_ result: Result<URLSessionWebSocketTask.Message, Error>) {
        switch result {
        case .failure(let err):
            print("WS receive error:", err)
            failAndClose(err)
        case .success(let message):
            switch message {
            case .data(let data):
                do {
                    let env = try Msg_RespEnvelope(serializedData: data)
                    if let cb = waiters[env.id] {
                        waiters.removeValue(forKey: env.id)
                        cb(.success(env))
                    }
                    // If you expect server push, also post a Notification here.
                } catch {
                    print("Decode error:", error)
                }
            default: break
            }
            // keep listening only while the socket is still healthy
            listen()
        }
    }

    /// Marks the connection dead, fails every outstanding request, and
    /// notifies the owner so it can reconnect. Does NOT call listen() again
    /// — a fresh connect() starts a new receive loop.
    private func failAndClose(_ error: Error) {
        connected = false
        let pending = waiters
        waiters.removeAll()
        for (_, cb) in pending { cb(.failure(error)) }
        task?.cancel(with: .abnormalClosure, reason: nil)
        onDisconnect?()
    }

    func request(build: (inout Msg_ReqEnvelope) -> Void) async throws -> Msg_RespEnvelope {
        guard connected else {
            throw NSError(domain: "ws", code: -1, userInfo: [NSLocalizedDescriptionKey: "Not connected"])
        }
        var env = Msg_ReqEnvelope()
        let id = nextId; nextId += 1
        env.id = id
        build(&env)
        // Re-assert the id after build() rather than trusting it survived:
        // this is issue #77's actual "stuck loading" bug. Most call sites'
        // closures do `var req = ReqEnvelope(); ...; e = req`, replacing
        // the whole envelope - id included - with a fresh one whose id
        // defaults to 0, instead of mutating the passed-in `e` in place.
        // Every request built that way was silently going out on the wire
        // as id=0, so any two of them in flight at once collided on the
        // same waiters[0] slot: the second registration overwrote the
        // first's, and when the first request's actual response
        // eventually arrived, waiters[0] pointed at someone else's
        // callback, leaving the first one's continuation waiting forever.
        // Confirmed live via the Swift runtime's own "leaked its
        // continuation" diagnostic and by literally logging "request
        // id=0" for nearly every request. Restoring the real id here
        // fixes every call site at once, rather than relying on dozens of
        // them to each preserve it correctly on their own.
        env.id = id

        //print("Sending request: \(env)")

        let data = try env.serializedData()
        return try await withCheckedThrowingContinuation { cont in
            self.waiters[id] = { result in cont.resume(with: result) }
            self.task?.send(.data(data)) { error in
                guard let error else { return }
                Task { await self.failWaiter(id: id, error: error) }
            }
        }
    }

    private func failWaiter(id: Int32, error: Error) {
        if let cb = waiters.removeValue(forKey: id) {
            cb(.failure(error))
        }
    }
}
