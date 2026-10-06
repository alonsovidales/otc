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

extension FileMsg {
    /// A device file's size in bytes (issue #187), as the device's
    /// dao.FileSize and otc-sync's fileSize: size64, or the int32 size from
    /// a device before release 93, which leaves size64 at 0 (and wraps a
    /// size of 2 GiB or more, as it always has).
    var fileSize: Int64 { size64 != 0 ? size64 : Int64(size) }
}

final class WSClient {

    // MARK: Public callbacks
    /// Fires once the socket is up AND the device has accepted the
    /// password - not before. It used to fire on the raw socket opening,
    /// while Auth was still in flight, so the first ListFiles went out
    /// unauthenticated and every folder showed "not authenticated" under a
    /// green "Connected" until the next retry. Says which way the app got
    /// there (issue #190).
    var onConnect: ((ConnectionRoute) -> Void)?
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

    // Issue #190: the device on the home network (see LocalRoute.swift).
    /// The configured address; nil until the first configure().
    private var domain: String?
    /// Where the device said it is at home, for `domain`. Also in the
    /// Keychain; this client is the only one that writes it there.
    private var local: LocalEndpoint?
    /// Bumped whenever `local` is forgotten: an answer to a
    /// GetLocalEndpoint sent before that isn't stored.
    private var localGen: UInt64 = 0
    private var localRace: LocalRace?
    /// The way the current socket went.
    private var route: ConnectionRoute = .bridge
    /// A home connection that got through the handshakes but never signed
    /// in: the next attempt goes straight to the bridge, so a device whose
    /// home listener misbehaves can't keep the app from connecting at all.
    private var skipHomeOnce = false
    /// A home endpoint learnt on this connect was already tried: see
    /// authenticateThenAnnounce.
    private var triedNewHome = false
    /// A sign-in at home is a few small messages on the LAN: a device that
    /// doesn't answer in this long is better reached through the bridge.
    private static let homeSignInTimeout: TimeInterval = 30
    /// GetLocalEndpoint holds up the "Connected" of a bridge sign-in, so a
    /// lost answer must not hold it for long. As otc-sync's localAskTO.
    private static let localAskTimeout: TimeInterval = 5

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
            if self.domain != domain {
                // Another device or address: what was learnt about the old
                // one is forgotten. The first configure just reads it.
                if self.domain != nil { self.dropLocal() }
                self.domain = domain
                self.local = LocalEndpointStore.load(domain: domain)
            }
            self.url = newURL
            self.key = key
        }
    }

    /// Disconnect and a change of device: the home endpoint is forgotten
    /// with the address it came from.
    func forgetLocalEndpoint() {
        queue.async { [weak self] in self?.dropLocal() }
    }

    private func dropLocal() {
        localGen &+= 1
        local = nil
        skipHomeOnce = false
        LocalEndpointStore.clear()
    }

    /// A network change or a wake (issue #190): connect again, home network
    /// first, when there is a home endpoint to try and the client is meant
    /// to be connected (not after a rejected password). The caller checks
    /// that nothing is being transferred. True when it reconnects.
    func reconnectForRoute() -> Bool {
        queue.sync {
            guard autoReconnect, url != nil, local != nil else { return false }
            // A new network is worth another try at home.
            skipHomeOnce = false
            startConnecting()
            return true
        }
    }

    func enableAutoReconnect(_ enabled: Bool = true) {
        queue.async { [weak self] in self?.autoReconnect = enabled }

    }

    // MARK: Connect / Disconnect
    func connect() {
        queue.async { [weak self] in
            guard let self = self, self.url != nil else { return }
            self.startConnecting()
        }
    }

    /// connect() on the queue.
    private func startConnecting() {
        autoReconnect = true
        // Also what a manual connect (Retry, Connect) does to a
        // reconnect still waiting: it is dropped.
        connGen &+= 1
        reconnectPending = false

        // One connection at a time: the previous one, if any, is
        // silenced and closed, or its late .cancelled/.failed would
        // schedule reconnects of its own next to this one's.
        localRace?.cancel()
        localRace = nil
        if let old = conn {
            old.stateUpdateHandler = nil
            old.cancel()
            conn = nil
            // Its receive loop ignores it from now on, so nothing it
            // was asked would ever be answered: fail that at once
            // rather than at the 30-minute timeout (the RAID poll and
            // a folder's listing used to sit there), and forget its
            // sign-in. No onDisconnect: "Connecting…" stays on screen.
            isOpen = false; signedIn = false
            partial = Data()
            flushAndFail(NSError(domain: "ws", code: -999,
                                 userInfo: [NSLocalizedDescriptionKey: "Cancelled"]))
        }

        // Issue #190: the home network first, when the device told us
        // where it is there; the configured address when no address
        // answers in time, as before.
        guard let local, !local.urls.isEmpty, !skipHomeOnce else {
            skipHomeOnce = false
            dialConfigured()
            return
        }
        let gen = connGen
        let race = LocalRace(queue: queue) { [weak self] winner in
            guard let self else { winner?.cancel(); return }
            // connect() or disconnect() since: theirs is the connection.
            guard gen == self.connGen, self.autoReconnect else { winner?.cancel(); return }
            self.localRace = nil
            if let winner {
                self.adopt(winner, route: .home)
            } else {
                self.dialConfigured()
            }
        }
        localRace = race
        race.start(urls: local.urls, pin: local.pin)
    }

    /// The WebSocket stack, over `tls` when given.
    static func parameters(tls: NWProtocolTLS.Options?) -> NWParameters {
        // WebSocket options — set LARGE max message size (your choice)
        let wsOpts = NWProtocolWebSocket.Options()
        wsOpts.autoReplyPing = true
        wsOpts.maximumMessageSize = 1000 * 1024 * 1024 // 1000 MB

        let params = NWParameters(tls: tls, tcp: .init())
        params.defaultProtocolStack.applicationProtocols.insert(wsOpts, at: 0)
        return params
    }

    /// The configured address, exactly as before issue #190.
    private func dialConfigured() {
        guard let url else { return }
        let isSecure = (url.scheme?.lowercased() == "wss")
        let params = Self.parameters(tls: isSecure ? NWProtocolTLS.Options() : nil)

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
        route = .bridge
        watch(conn)
        conn.start(queue: queue)
    }

    /// The home connection that won the race, already open.
    private func adopt(_ conn: NWConnection, route: ConnectionRoute) {
        self.conn = conn
        self.route = route
        watch(conn)
        opened(conn)
    }

    private func watch(_ conn: NWConnection) {
        conn.stateUpdateHandler = { [weak self] state in
            guard let self, self.conn === conn else { return }
            switch state {
            case .ready:
                self.opened(conn)

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
    }

    private func opened(_ conn: NWConnection) {
        // backoffSeconds is reset once signed in, not here: the
        // bridge answers even when the device is offline, and
        // resetting on every socket would retry each second.
        isOpen = true
        signedIn = false
        receiveLoop()
        authenticateThenAnnounce()
    }

    /// Manual stop. Also disables auto-reconnect (call `enableAutoReconnect()` to re-enable).
    func disconnect() {
        queue.async { [weak self] in
            guard let self else { return }
            self.autoReconnect = false
            self.isOpen = false; self.signedIn = false
            self.localRace?.cancel()
            self.localRace = nil
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
    /// `timeout`, when given, replaces requestTimeout.
    private func request(on expected: NWConnection?, timeout: TimeInterval? = nil, _ build: @escaping (inout Req) -> Void) async throws -> Resp {
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
                    self.queue.asyncAfter(deadline: .now() + (timeout ?? self.requestTimeout)) { [weak self] in
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
    private func auth(key: String, on conn: NWConnection?, timeout: TimeInterval? = nil) async throws -> Bool {
        // Fetch this connection's ephemeral public key and encrypt the
        // password with it before it ever leaves the app (see issue #2:
        // the bridge only relays already-encrypted payloads).
        let pubKeyResp = try await request(on: conn, timeout: timeout) { req in
            req.payload = .reqGetPubKey(Msg_GetPubKey())
        }
        guard case .respPubKey(let pubKey) = pubKeyResp.payload else {
            if case .respAck(let ack) = pubKeyResp.payload, ack.code == "device_unreachable" || ack.code == "device_disabled" {
                throw UnreachableError(message: ack.errorMsg)
            }
            throw NSError(domain: "auth", code: 1, userInfo: [NSLocalizedDescriptionKey: "Unable to fetch the connection's public key"])
        }
        let encryptedKey = try PwCrypto.encryptPassword(key, pubKeyDER: pubKey.publicKey)

        let resp = try await request(on: conn, timeout: timeout) { req in
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
        let route = self.route
        guard let key = self.key else { self.signedIn = true; self.onConnect?(route); return }
        Task { [weak self] in
            guard let self else { return }
            do {
                if try await self.auth(key: key, on: conn, timeout: route == .home ? Self.homeSignInTimeout : nil) {
                    // Issue #190: through the bridge, ask where the device
                    // is at home before announcing. A new answer is tried
                    // at once, while nothing has started on this socket:
                    // otherwise a Mac that stays put would only move home
                    // at its next reconnect, maybe days and a big upload
                    // later. Once per connect, so an answer that keeps
                    // changing can't bounce it back and forth.
                    if route == .bridge, await self.learnLocalEndpoint(on: conn) {
                        let switched = self.queue.sync { () -> Bool in
                            guard self.conn === conn, self.autoReconnect, !self.triedNewHome else { return false }
                            self.triedNewHome = true
                            self.backoffSeconds = 1
                            self.startConnecting()
                            return true
                        }
                        if switched { return }
                    }
                    let current = self.queue.sync { () -> Bool in
                        // Open still: a socket that dropped while the
                        // device was asked is already reconnecting.
                        guard self.conn === conn, self.isOpen else { return false }
                        self.backoffSeconds = 1; self.signedIn = true
                        self.triedNewHome = false
                        return true
                    }
                    if current { self.onConnect?(route) }
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
                    if route == .home { self.skipHomeOnce = true }
                    self.isOpen = false; self.signedIn = false
                    conn.cancel()
                }
            }
        }
    }

    /// What to do with the answer to GetLocalEndpoint.
    enum LocalAnswer: Equatable {
        case store(LocalEndpoint)
        /// A device that doesn't know the request (older than issue #190)
        /// or can't be reached at home: stay on the bridge.
        case clear
        /// No answer to go by: what is stored stays.
        case keep
    }

    static func localAnswer(_ resp: Resp, domain: String) -> LocalAnswer {
        if !resp.error, case .respLocalEndpoint(let ep) = resp.payload {
            // Answered, but with nothing usable: as good as unavailable.
            return LocalEndpoint(domain: domain, addresses: ep.addresses, port: Int(ep.port), pin: ep.certSha256)
                .map { .store($0) } ?? .clear
        }
        if resp.error, resp.errorCode == "unknown_payload" || resp.errorCode == "local_unavailable" {
            return .clear
        }
        return .keep
    }

    /// Asks the device, signed in through the bridge on `conn`, where it is
    /// on the home network, and stores or forgets that. True when it gave
    /// an endpoint other than the one held: worth trying now.
    private func learnLocalEndpoint(on conn: NWConnection) async -> Bool {
        let (gen, domain) = queue.sync { (localGen, self.domain) }
        guard let domain else { return false }
        let resp: Resp
        do {
            resp = try await request(on: conn, timeout: Self.localAskTimeout) { req in
                req.payload = .reqGetLocalEndpoint(Msg_GetLocalEndpoint())
            }
        } catch {
            // A network error: what is stored stays.
            return false
        }
        let answer = Self.localAnswer(resp, domain: domain)
        return queue.sync { () -> Bool in
            // Forgotten meanwhile (Disconnect, another device): not stored.
            guard gen == localGen, self.domain == domain else { return false }
            switch answer {
            case .keep:
                return false
            case .clear:
                if local != nil {
                    local = nil
                    LocalEndpointStore.clear()
                }
                return false
            case .store(let ep):
                if let local, local.sameRoute(as: ep) { return false }
                local = ep
                skipHomeOnce = false
                LocalEndpointStore.save(ep)
                return true
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
