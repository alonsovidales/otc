import Foundation
import SwiftProtobuf

// Replace these with your generated SwiftProtobuf types if names differ:
typealias Req = Msg_ReqEnvelope
typealias Resp = Msg_RespEnvelope
typealias Ack  = Msg_Ack
typealias FileMsg = Msg_File
typealias ListFiles = Msg_ListFiles
typealias UploadFile = Msg_UploadFile
typealias Auth = Msg_Auth
typealias ListOfFiles = Msg_ListOfFiles

/// A small resilient WS client. Not main-actor isolated; it hops to main only when needed.
final class WSClient: NSObject, URLSessionWebSocketDelegate {

    private var url: URL?
    private var key: String?
    private var session: URLSession!
    private var task: URLSessionWebSocketTask?

    private var nextId: Int32 = 1
    private var waiters = [Int32: (Resp) -> Void]()
    private let queue = DispatchQueue(label: "ws.client.queue")

    // Callbacks you can observe from the model:
    var onConnect: (() -> Void)?
    var onDisconnect: ((Error?) -> Void)?

    override init() {
        super.init()
        let config = URLSessionConfiguration.default
        session = URLSession(configuration: config, delegate: self, delegateQueue: nil)
    }

    func configure(domain: String, key: String) {
        self.url = URL(string: "wss://\(domain)/ws")
        self.key = key
    }

    func connect() {
        guard let url else { return }
        if task != nil { return }
        let t = session.webSocketTask(with: url)
        task = t
        t.resume()
        receiveLoop()
        // Try auth immediately after connect
        if let key {
            Task.detached { [weak self] in _ = try? await self?.auth(key: key) }
        }
    }

    func disconnect() {
        task?.cancel()
        task = nil
    }

    func isConnected() -> Bool {
        return task?.state == .running
    }

    // MARK: - Protobuf request/response

    func request(build: (inout Req) -> Void) async throws -> Resp {
        // 1) Build synchronously so `build` doesn't escape
        var built = Req()
        build(&built)                 // ✅ non-escaping, used immediately

        // 2) Await the response via a continuation; the rest can escape
        return try await withCheckedThrowingContinuation { cont in
            queue.async { [weak self] in
                guard let self = self, let task = self.task else {
                    cont.resume(throwing: NSError(
                        domain: "ws", code: -1,
                        userInfo: [NSLocalizedDescriptionKey: "No socket"]
                    ))
                    return
                }

                // assign a fresh id on the queue to keep it serialized
                let id = self.nextId
                self.nextId += 1

                // Make a local copy we can mutate/send
                var req = built
                req.id = id

                do {
                    let data = try req.serializedData()

                    // Register waiter before sending
                    self.waiters[id] = { resp in
                        cont.resume(returning: resp)
                    }

                    task.send(.data(data)) { err in
                        if let err = err {
                            self.waiters.removeValue(forKey: id)
                            cont.resume(throwing: err)
                        }
                    }
                } catch {
                    cont.resume(throwing: error)
                }
            }
        }
    }

    func auth(key: String) async throws -> Bool {
        let resp = try await request { req in
            var a = Auth()
            a.key = key
            a.create = false
            req.payload = .reqAuth(a)
        }
        if case .respAck(let ack) = resp.payload {
            return ack.ok
        }
        return false
    }

    // MARK: - Receive loop

    private func receiveLoop() {
        task?.receive { [weak self] result in
            guard let self else { return }
            switch result {
            case .success(let msg):
                switch msg {
                case .data(let d):
                    if let resp = try? Resp(serializedData: d) {
                        if let waiter = self.waiters.removeValue(forKey: resp.id) {
                            waiter(resp)
                        } // else could be push message
                    }
                default: break
                }
                self.receiveLoop()
            case .failure(let err):
                self.onDisconnect?(err)
                self.task = nil
                // Backoff reconnect
                DispatchQueue.global().asyncAfter(deadline: .now() + 2) { [weak self] in
                    guard let self, let _ = self.url else { return }
                    self.connect()
                }
            }
        }
    }

    // MARK: - Delegate

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask,
                    didOpenWithProtocol protocol: String?) {
        onConnect?()
    }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask,
                    didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {
        onDisconnect?(nil)
        task = nil
    }
}
