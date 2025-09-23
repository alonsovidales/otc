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
    private var waiters = [Int32: (Msg_RespEnvelope) -> Void]()

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
                self.connected = false
                print("WS receive error:", err)
                sleep(1000)
            case .success(let message):
                switch message {
                case .data(let data):
                    do {
                        let env = try Msg_RespEnvelope(serializedData: data)
                        if let cb = self.waiters[env.id] {
                            self.waiters.removeValue(forKey: env.id)
                            cb(env)
                        }
                        // If you expect server push, also post a Notification here.
                    } catch {
                        print("Decode error:", error)
                    }
                default: break
                }
            }
            // keep listening
            self.listen()
        }
    }

    func request(build: (inout Msg_ReqEnvelope) -> Void) async throws -> Msg_RespEnvelope {
        var env = Msg_ReqEnvelope()
        env.id = nextId; nextId += 1
        build(&env)

        //print("Sending request: \(env)")
        
        let data = try env.serializedData()
        return try await withCheckedThrowingContinuation { cont in
            self.waiters[env.id] = { resp in cont.resume(returning: resp) }
            self.task?.send(.data(data)) { error in
                if let error { cont.resume(throwing: error) }
            }
        }
    }
}
