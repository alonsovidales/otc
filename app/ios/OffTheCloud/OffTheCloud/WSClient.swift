//
//  WSClient.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//


import Foundation

final class WSClient {
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

    func connect(url: URL) async throws {
        print("Trynig to connect to: \(url)")
        if let task, task.state == .running { return }
        let t = session.webSocketTask(with: url)
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
            switch result {
            case .failure(let err):
                print("WS receive error:", err)
                self.failAndClose(err)
            case .success(let message):
                switch message {
                case .data(let data):
                    do {
                        let env = try Msg_RespEnvelope(serializedData: data)
                        if let cb = self.waiters[env.id] {
                            self.waiters.removeValue(forKey: env.id)
                            cb(.success(env))
                        }
                        // If you expect server push, also post a Notification here.
                    } catch {
                        print("Decode error:", error)
                    }
                default: break
                }
                // keep listening only while the socket is still healthy
                self.listen()
            }
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
        env.id = nextId; nextId += 1
        build(&env)

        //print("Sending request: \(env)")

        let data = try env.serializedData()
        return try await withCheckedThrowingContinuation { cont in
            self.waiters[env.id] = { result in cont.resume(with: result) }
            self.task?.send(.data(data)) { error in
                if let error {
                    self.waiters.removeValue(forKey: env.id)
                    cont.resume(throwing: error)
                }
            }
        }
    }
}
