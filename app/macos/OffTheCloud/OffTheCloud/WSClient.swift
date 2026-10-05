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
    /// The bridge could not reach the device (switched off, offline) - not
    /// a wrong password: the client keeps retrying with backoff and signs
    /// in once the device is back. Same as otc-sync's OnUnreachable.
    var onUnreachable: ((String) -> Void)?

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
    /// The device accepted the password on this socket. isConnected() is
    /// both: a socket that's open but still signing in used to count as
    /// connected, so a folder's first ListFiles raced the Auth, got "not
    /// authenticated" - which the receive loop takes for a lost session
    /// and drops the socket, cutting its own sign-in short - and the
    /// folder sat in that error, doing nothing.
    private var signedIn = false

    // Reconnect control
    private var autoReconnect = true
    private var backoffSeconds: TimeInterval = 1
    private let maxBackoff: TimeInterval = 30
    /// Bumped by every connect(): a reconnect scheduled for an earlier
    /// socket doesn't fire. As otc-sync's wsclient gen.
    private var connGen: UInt64 = 0
    /// One reconnect per failure: a socket's .failed and its receive
    /// error's .cancelled both ask for one, and the second used to replace
    /// the first's fresh socket.
    private var reconnectPending = false

    // MARK: Configure
    /// domain: "your.domain.tld" (no scheme, no path); if you pass a full URL, it will be used as-is.
    func configure(domain: String, key: String, secure: Bool = true) {
        let newURL: URL?
        if domain.contains("://") {
            newURL = URL(string: domain) // full URL provided
        } else {
            let scheme = secure ? "wss" : "ws"
            newURL = URL(string: "\(scheme)://\(domain)/ws")
        }
        // On the queue that reads them, both at once: a backoff reconnect
        // firing meanwhile read them mid-write, or got the new address
        // with the old password. Async, and queued ahead of the connect()
        // that callers make next.
        queue.async { [weak self] in
            guard let self else { return }
            self.url = newURL
            self.key = key
        }
    }

    func enableAutoReconnect(_ enabled: Bool = true) {
        queue.async { [weak self] in self?.autoReconnect = enabled }

    }

    // MARK: Connect / Disconnect
    func connect() {
        queue.async { [weak self] in
            guard let self = self, let url = self.url else { return }
            self.autoReconnect = true
            // Also what a manual connect (Retry, Connect) does to a
            // reconnect still waiting: it is dropped.
            self.connGen &+= 1
            self.reconnectPending = false

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

            // One connection at a time: the previous one, if any, is
            // silenced and closed, or its late .cancelled/.failed would
            // schedule reconnects of its own next to this one's.
            if let old = self.conn {
                old.stateUpdateHandler = nil
                old.cancel()
                // Its receive loop ignores it from now on, so nothing it
                // was asked would ever be answered: fail that at once
                // rather than at the 30-minute timeout (the RAID poll and
                // a folder's listing used to sit there), and forget its
                // sign-in. No onDisconnect: "Connecting…" stays on screen.
                self.isOpen = false; self.signedIn = false
                self.partial = Data()
                self.flushAndFail(NSError(domain: "ws", code: -999,
                                          userInfo: [NSLocalizedDescriptionKey: "Cancelled"]))
            }
            let conn = NWConnection(to: endpoint, using: params)
            self.conn = conn

            conn.stateUpdateHandler = { [weak self] state in
                guard let self, self.conn === conn else { return }
                switch state {
                case .ready:
                    // backoffSeconds is reset once signed in, not here: the
                    // bridge answers even when the device is offline, and
                    // resetting on every socket would retry each second.
                    self.isOpen = true
                    self.signedIn = false
                    self.receiveLoop()
                    self.authenticateThenAnnounce()

                case .waiting(let error):
                    // The connection couldn't be made (the bridge
                    // restarting, the device updating, no network) and
                    // NWConnection waits here - for good, when nothing about
                    // the network path changes: "Disconnected" that never
                    // tried again. Close it; .cancelled reconnects with the
                    // growing delay.
                    print("WSClient: waiting: \(error) - retrying")
                    self.isOpen = false; self.signedIn = false
                    self.onDisconnect?(error)
                    conn.cancel()

                case .failed(let error):
                    print("WSClient: failed: \(error)")
                    self.isOpen = false; self.signedIn = false
                    self.flushAndFail(error)
                    self.onDisconnect?(error)
                    self.scheduleReconnect()

                case .cancelled:
                    self.isOpen = false; self.signedIn = false
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
            self.isOpen = false; self.signedIn = false
            let err = NSError(domain: "ws", code: -999,
                              userInfo: [NSLocalizedDescriptionKey: "Closed"])
            self.flushAndFail(err)
            self.conn?.cancel()
            self.conn = nil
        }
        print("Disconnected")
    }

    func isConnected() -> Bool { queue.sync { isOpen && signedIn } }

    // MARK: Request/response
    /// Send a request built by `build` and await response (matched by `id`).
    func request(_ build: @escaping (inout Req) -> Void) async throws -> Resp {
        try await request(on: nil, build)
    }

    /// `expected`, when given, is the socket the request belongs to: once
    /// it has been replaced the request fails rather than go out on the
    /// new one (a stale sign-in's Auth, sealed with the old socket's key,
    /// would count as a failed attempt there - issue #117).
    private func request(on expected: NWConnection?, _ build: @escaping (inout Req) -> Void) async throws -> Resp {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Resp, Error>) in
            queue.async { [weak self] in
                guard let self = self, let conn = self.conn, self.isOpen, expected == nil || expected === conn else {
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
                            // sending failed -> trigger reconnect, unless
                            // this socket has been replaced already
                            if self.conn === conn { self.scheduleReconnect() }
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
        try await auth(key: key, on: nil)
    }

    /// Signs in on `conn` only (see request(on:)).
    private func auth(key: String, on conn: NWConnection?) async throws -> Bool {
        // Fetch this connection's ephemeral public key and encrypt the
        // password with it before it ever leaves the app (see issue #2:
        // the bridge only relays already-encrypted payloads).
        let pubKeyResp = try await request(on: conn) { req in
            req.payload = .reqGetPubKey(Msg_GetPubKey())
        }
        guard case .respPubKey(let pubKey) = pubKeyResp.payload else {
            if case .respAck(let ack) = pubKeyResp.payload, ack.code == "device_unreachable" || ack.code == "device_disabled" {
                throw UnreachableError(message: ack.errorMsg)
            }
            throw NSError(domain: "auth", code: 1, userInfo: [NSLocalizedDescriptionKey: "Unable to fetch the connection's public key"])
        }
        let encryptedKey = try PwCrypto.encryptPassword(key, pubKeyDER: pubKey.publicKey)

        let resp = try await request(on: conn) { req in
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

    struct UnreachableError: Error {
        let message: String
    }

    struct AuthError: Error {
        let message: String
        let retryAfter: Int?
    }

    // MARK: Receive loop
    private func receiveLoop() {
        guard let conn else { return }
        conn.receiveMessage { [weak self] (data, ctx, isComplete, error) in
            // A late callback from a connection since replaced must not
            // act on (close) the current one.
            guard let self, self.conn === conn else { return }

            if let error = error {
                // Closed rather than just abandoned: its .cancelled
                // schedules the one reconnect.
                self.isOpen = false; self.signedIn = false
                self.partial = Data()
                self.flushAndFail(error)
                self.onDisconnect?(error)
                self.conn?.cancel()
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
                    // The device no longer knows this session - it
                    // restarted (an update) while the bridge kept this
                    // socket, pairing it with a fresh, signed-out
                    // connection. Every request would fail with "not
                    // authenticated" under a green "Connected" until
                    // something reconnected: cancel, and the .cancelled
                    // handler reconnects and signs in again. Same as
                    // otc-sync's wsclient.
                    if case .respAck(let ack) = resp.payload, ack.code == "not_authenticated", self.isOpen, self.signedIn {
                        print("WSClient: the device no longer knows this session - reconnecting to sign in again")
                        self.isOpen = false; self.signedIn = false
                        self.conn?.cancel()
                        return
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
    /// Everything it does afterwards is for the socket it signed in on: a
    /// sign-in still running when connect() replaced that socket (Retry
    /// while "Connecting…") used to mark the new one signed in, or cancel
    /// it, or report its own failure as the new one's.
    private func authenticateThenAnnounce() {
        guard let conn = self.conn else { return }
        guard let key = self.key else { self.signedIn = true; self.onConnect?(); return }
        Task { [weak self] in
            guard let self else { return }
            do {
                if try await self.auth(key: key, on: conn) {
                    let current = self.queue.sync { () -> Bool in
                        guard self.conn === conn else { return false }
                        self.backoffSeconds = 1; self.signedIn = true
                        return true
                    }
                    if current { self.onConnect?() }
                } else {
                    self.failAuth("The device rejected the password", retryAfter: nil, on: conn)
                }
            } catch let err as UnreachableError {
                // Close and let the .cancelled handler reconnect with a
                // growing delay.
                self.queue.async {
                    guard self.conn === conn else { return }
                    self.onUnreachable?(err.message)
                    self.isOpen = false; self.signedIn = false
                    conn.cancel()
                }
            } catch let err as AuthError {
                self.failAuth(err.message, retryAfter: err.retryAfter, on: conn)
            } catch {
                // Not an answer about the password - a timeout, or the
                // socket dropping mid sign-in (a busy device, another
                // folder's upload filling the link). Treating it as a
                // wrong password stopped the app for good; close and let
                // the .cancelled handler reconnect with a growing delay.
                print("WSClient: sign-in did not complete (\(error.localizedDescription)) - reconnecting")
                self.queue.async {
                    guard self.conn === conn else { return }
                    self.isOpen = false; self.signedIn = false
                    conn.cancel()
                }
            }
        }
    }

    private func failAuth(_ message: String, retryAfter: Int?, on conn: NWConnection) {
        queue.async { [weak self] in
            guard let self, self.conn === conn else { return }
            self.autoReconnect = false
            self.isOpen = false; self.signedIn = false
            self.flushAndFail(NSError(domain: "auth", code: 2, userInfo: [NSLocalizedDescriptionKey: message]))
            self.conn?.cancel()
            self.conn = nil
            self.onAuthFailed?(message, retryAfter)
        }
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
            guard !self.reconnectPending else { return }
            self.reconnectPending = true
            let gen = self.connGen
            let delay = self.backoffSeconds
            self.backoffSeconds = min(self.backoffSeconds * 2, self.maxBackoff)
            self.queue.asyncAfter(deadline: .now() + delay) { [weak self] in
                // connect() clears reconnectPending, so a late request for
                // the same failure is still ignored until then.
                guard let self, gen == self.connGen, self.autoReconnect else { return }
                self.connect()
            }
        }
    }
}
