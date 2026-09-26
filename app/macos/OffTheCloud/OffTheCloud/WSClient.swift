// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Network
import SwiftProtobuf

// ==== Generated types aliases (rename to your actual generated names) ====
typealias Req         = Msg_ReqEnvelope
typealias Resp        = Msg_RespEnvelope
typealias Ack         = Msg_Ack
typealias FileMsg     = Msg_File
typealias ListFiles   = Msg_ListFiles
typealias UploadFile  = Msg_UploadFile
typealias Auth        = Msg_Auth
typealias ListOfFiles = Msg_ListOfFiles
// ========================================================================

final class WSClient {

    // MARK: Public callbacks
    /// Fires once the socket is up AND the device has accepted the
    /// password - not before. It used to fire on the raw socket opening,
    /// while Auth was still in flight, so the first ListFiles went out
    /// unauthenticated and every folder showed "not authenticated" under a
    /// green "Connected" until the next retry.
    var onConnect: (() -> Void)?
    var onDisconnect: ((Error?) -> Void)?
    /// The device rejected the password. Reconnecting stops until the
    /// settings change (connect() re-enables it) rather than retrying a
    /// password that won't work.
    /// retryAfter is set when the device refused to even check the
    /// password because this address made too many attempts (issue #117).
    var onAuthFailed: ((String, _ retryAfter: Int?) -> Void)?
    var onPush: ((Resp) -> Void)? // unsolicited server messages

    // MARK: Internal state
    private var conn: NWConnection?
    private var url: URL?
    private var key: String?
    private var nextId: Int32 = 1

    private let queue = DispatchQueue(label: "wsclient.serial")
    private var waiters: [Int32 : CheckedContinuation<Resp, Error>] = [:]
    // A message Network.framework hands over in more than one piece
    // (isComplete false until the last) is assembled here - a big folder
    // listing used to be parsed piece by piece, fail silently, and leave
    // its request waiting forever.
    private var partial = Data()
    // How long a request may wait for its reply - the same bound as
    // otc-sync's requestTimeout. A lost reply then fails the request (and
    // the folder pass retries) instead of hanging it, and with it every
    // later pass of that folder, until the app is relaunched.
    private let requestTimeout: TimeInterval = 30 * 60
    private var isOpen = false

    // Reconnect control
    private var autoReconnect = true
    private var backoffSeconds: TimeInterval = 1
    private let maxBackoff: TimeInterval = 30

    // MARK: Configure
    /// domain: "your.domain.tld" (no scheme, no path); if you pass a full URL, it will be used as-is.
    func configure(domain: String, key: String, secure: Bool = true) {
        if domain.contains("://") {
            self.url = URL(string: domain) // full URL provided
        } else {
            let scheme = secure ? "wss" : "ws"
            self.url = URL(string: "\(scheme)://\(domain)/ws")
        }
        self.key = key
    }

    func enableAutoReconnect(_ enabled: Bool = true) {
        queue.async { [weak self] in self?.autoReconnect = enabled }

    }

    // MARK: Connect / Disconnect
    func connect() {
        queue.async { [weak self] in
            guard let self = self, let url = self.url else { return }
            self.autoReconnect = true

            // WebSocket options — set LARGE max message size (your choice)
            let wsOpts = NWProtocolWebSocket.Options()
            wsOpts.autoReplyPing = true
            wsOpts.maximumMessageSize = 1000 * 1024 * 1024 // 1000 MB

            let isSecure = (url.scheme?.lowercased() == "wss")
            let tls = isSecure ? NWProtocolTLS.Options() : nil
            let params = NWParameters(tls: tls, tcp: .init())
            params.defaultProtocolStack.applicationProtocols.insert(wsOpts, at: 0)

            // Endpoint: prefer URL initializer on newer SDKs
            let endpoint: NWEndpoint
            if #available(macOS 13.0, iOS 16.0, *) {
                endpoint = NWEndpoint.url(url)
            } else {
                let host = NWEndpoint.Host(url.host ?? "localhost")
                let port = NWEndpoint.Port(rawValue: UInt16(url.port ?? (isSecure ? 443 : 80)))!
                endpoint = .hostPort(host: host, port: port)
            }

            let conn = NWConnection(to: endpoint, using: params)
            self.conn = conn

            conn.stateUpdateHandler = { [weak self] state in
                guard let self else { return }
                switch state {
                case .ready:
                    self.isOpen = true
                    self.backoffSeconds = 1
                    self.receiveLoop()
                    self.authenticateThenAnnounce()

                case .waiting(let error):
                    // Path not currently available — notify, then let state machine proceed.
                    print("WSClient: waiting: \(error)")
                    self.onDisconnect?(error)

                case .failed(let error):
                    print("WSClient: failed: \(error)")
                    self.isOpen = false
                    self.flushAndFail(error)
                    self.onDisconnect?(error)
                    self.scheduleReconnect()

                case .cancelled:
                    self.isOpen = false
                    self.flushAndFail(NSError(domain: "ws", code: -999,
                                              userInfo: [NSLocalizedDescriptionKey: "Cancelled"]))
                    self.onDisconnect?(nil)
                    self.scheduleReconnect()

                default:
                    break
                }
            }

            conn.start(queue: self.queue)
        }
    }

    /// Manual stop. Also disables auto-reconnect (call `enableAutoReconnect()` to re-enable).
    func disconnect() {
        queue.async { [weak self] in
            guard let self else { return }
            self.autoReconnect = false
            self.isOpen = false
            let err = NSError(domain: "ws", code: -999,
                              userInfo: [NSLocalizedDescriptionKey: "Closed"])
            self.flushAndFail(err)
            self.conn?.cancel()
            self.conn = nil
        }
        print("Disconnected")
    }

    func isConnected() -> Bool { queue.sync { isOpen } }

    // MARK: Request/response
    /// Send a request built by `build` and await response (matched by `id`).
    func request(_ build: @escaping (inout Req) -> Void) async throws -> Resp {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Resp, Error>) in
            queue.async { [weak self] in
                guard let self = self, let conn = self.conn, self.isOpen else {
                    cont.resume(throwing: NSError(domain: "ws", code: -1,
                                                  userInfo: [NSLocalizedDescriptionKey: "Not connected"]))
                    return
                }

                var req = Req()
                req.id = self.nextId
                self.nextId &+= 1
                build(&req)

                do {
                    let bytes = try req.serializedData()

                    // Store the waiter before sending
                    self.waiters[req.id] = cont
                    let id = req.id
                    self.queue.asyncAfter(deadline: .now() + self.requestTimeout) { [weak self] in
                        guard let self, let c = self.waiters.removeValue(forKey: id) else { return }
                        c.resume(throwing: NSError(domain: "ws", code: -2,
                                                   userInfo: [NSLocalizedDescriptionKey: "The device did not answer in time"]))
                    }

                    // Binary WS frame
                    let meta = NWProtocolWebSocket.Metadata(opcode: .binary)
                    let ctx = NWConnection.ContentContext(identifier: "req\(req.id)", metadata: [meta])

                    conn.send(content: bytes, contentContext: ctx, isComplete: true, completion: .contentProcessed { sendErr in
                        if let sendErr = sendErr {
                            if let c = self.waiters.removeValue(forKey: req.id) {
                                c.resume(throwing: sendErr)
                            }
                            // sending failed -> trigger reconnect
                            self.scheduleReconnect()
                        }
                    })
                } catch {
                    cont.resume(throwing: error)
                }
            }
        }
    }

    /// Convenience auth helper; returns true if resp_ack.ok
    func auth(key: String) async throws -> Bool {
        // Fetch this connection's ephemeral public key and encrypt the
        // password with it before it ever leaves the app (see issue #2:
        // the bridge only relays already-encrypted payloads).
        let pubKeyResp = try await request { req in
            req.payload = .reqGetPubKey(Msg_GetPubKey())
        }
        guard case .respPubKey(let pubKey) = pubKeyResp.payload else {
            throw NSError(domain: "auth", code: 1, userInfo: [NSLocalizedDescriptionKey: "Unable to fetch the connection's public key"])
        }
        let encryptedKey = try PwCrypto.encryptPassword(key, pubKeyDER: pubKey.publicKey)

        let resp = try await request { req in
            var a = Auth()
            a.key = encryptedKey
            a.create = false
            req.payload = .reqAuth(a)
        }
        if case .respAck(let ack) = resp.payload {
            if ack.ok { return true }
            throw AuthError(message: ack.errorMsg.isEmpty ? "The device rejected the password" : ack.errorMsg,
                            retryAfter: ack.code == "too_many_attempts" ? Int(ack.retryAfterSeconds) : nil)
        }
        return false
    }

    struct AuthError: Error {
        let message: String
        let retryAfter: Int?
    }

    // MARK: Receive loop
    private func receiveLoop() {
        conn?.receiveMessage { [weak self] (data, ctx, isComplete, error) in
            guard let self else { return }

            if let error = error {
                self.isOpen = false
                self.partial = Data()
                self.flushAndFail(error)
                self.onDisconnect?(error)
                self.scheduleReconnect()
                return
            }

            if let data = data, !data.isEmpty {
                self.partial.append(data)
            }
            if isComplete && !self.partial.isEmpty {
                let whole = self.partial
                self.partial = Data()
                if let resp = try? Resp(serializedData: whole) {
                    if let cont = self.waiters.removeValue(forKey: resp.id) {
                        cont.resume(returning: resp)
                    } else {
                        print("WSClient: message \(resp.id) (\(whole.count) bytes) has no waiter; \(self.waiters.count) pending")
                        self.onPush?(resp)
                    }
                } else {
                    print("WSClient: could not decode a \(whole.count)-byte message")
                }
            }

            // Keep listening
            self.receiveLoop()
        }
    }

    // MARK: Helpers
    /// Auth first, onConnect after - see onConnect's doc comment.
    private func authenticateThenAnnounce() {
        guard let key = self.key else { self.onConnect?(); return }
        Task { [weak self] in
            guard let self else { return }
            do {
                if try await self.auth(key: key) {
                    self.onConnect?()
                } else {
                    self.failAuth("The device rejected the password", retryAfter: nil)
                }
            } catch let err as AuthError {
                self.failAuth(err.message, retryAfter: err.retryAfter)
            } catch {
                self.failAuth(error.localizedDescription, retryAfter: nil)
            }
        }
    }

    private func failAuth(_ message: String, retryAfter: Int?) {
        queue.async { [weak self] in
            guard let self else { return }
            self.autoReconnect = false
            self.isOpen = false
            self.flushAndFail(NSError(domain: "auth", code: 2, userInfo: [NSLocalizedDescriptionKey: message]))
            self.conn?.cancel()
            self.conn = nil
        }
        onAuthFailed?(message, retryAfter)
    }

    private func flushAndFail(_ error: Error) {
        for (_, cont) in waiters {
            cont.resume(throwing: error)
        }
        waiters.removeAll()
    }

    private func scheduleReconnect() {
        queue.async { [weak self] in
            guard let self else { return }
            guard self.autoReconnect, self.url != nil, self.conn == nil || self.isOpen == false else { return }
            let delay = self.backoffSeconds
            self.backoffSeconds = min(self.backoffSeconds * 2, self.maxBackoff)
            self.queue.asyncAfter(deadline: .now() + delay) { [weak self] in
                guard let self else { return }
                if self.autoReconnect { self.connect() }
            }
        }
    }
}
