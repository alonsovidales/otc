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
    /// The message is the device's reason, nil when it gave none.
    var onAuthFailed: ((String?, _ retryAfter: Int?) -> Void)?
    var onPush: ((Resp) -> Void)? // unsolicited server messages
    /// The bridge could not reach the device (switched off, offline) - not
    /// a wrong password: the client keeps retrying with backoff and signs
    /// in once the device is back. Same as otc-sync's OnUnreachable.
    var onUnreachable: ((String) -> Void)?
    /// Issue #207: the device answered that it can't reach its storage
    /// disks, with its words ("" when it gave none) - not a wrong password
    /// and not unreachable: the credentials and the reconnecting stay, it
    /// is asked again every storageRetry (connect() asks at once), and the
    /// app signs in as soon as it answers otherwise. Same as otc-sync's
    /// OnStorageUnavailable.
    var onStorageUnavailable: ((String) -> Void)?
    /// A home endpoint just learnt didn't answer, and this connection went
    /// through the bridge instead (issue #190). Once per endpoint learnt:
    /// macOS's Local Network prompt, still unanswered, refuses that first
    /// try, and nothing else would try again until a network change.
    var onHomeMissed: (() -> Void)?

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

    // The client's own keep-alive, as otc-sync's wsclient: a
    // ping every pingEvery, and a connection that has received nothing - no
    // answer, no pong - for deadAfter while nothing is being sent is taken
    // for dead and dialled again. One that died without a word (the Mac
    // asleep, the Wi-Fi changed, a router that forgot it) used to look
    // alive until TCP gave up, or a request's own timeout ran out - half an
    // hour. While a message is being sent its pong can't come back before
    // it is out, so a send in progress holds the watchdog off.
    private static let pingEvery: TimeInterval = 20
    private static let deadAfter: TimeInterval = 45
    /// When something last came in on the current socket, or a send ended.
    private var lastSeen = Date()
    /// Sends under way on the current socket: when each started and by when
    /// it must be out (sendTimeout) - a send that isn't is a dead link, as
    /// otc-sync's write deadline.
    private var sending: [Int32: Date] = [:]

    /// How long sending `bytes` may take: half a minute, and as long again
    /// as the bytes take at 32 KiB/s. As otc-sync's writeTimeout.
    private static func sendTimeout(_ bytes: Int) -> TimeInterval { 30 + Double(bytes) / Double(32 << 10) }
    private var keepAliveTimer: DispatchSourceTimer?
    private var lastPing = Date()
    /// The device accepted the password on this socket. isConnected() is
    /// both: a socket that's open but still signing in used to count as
    /// connected, so a folder's first ListFiles raced the Auth, got "not
    /// authenticated" - which the receive loop takes for a lost session
    /// and drops the socket, cutting its own sign-in short - and the
    /// folder sat in that error, doing nothing.
    private var signedIn = false
    /// How many times this client has signed in (signInCount).
    private var signIns: UInt64 = 0

    // Reconnect control
    private var autoReconnect = true
    private var backoffSeconds: TimeInterval = 1
    private let maxBackoff: TimeInterval = 30
    /// Issue #207: how long a device that can't reach its disks waits for
    /// the next ask - not the growing delay of the other failures. As
    /// otc-sync's storageRetry.
    static let storageRetry: TimeInterval = 30
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
    /// The pin the current socket's home connection was dialled with (nil
    /// for the configured address): its TLS handshake checked it.
    private var connPin: Data?
    /// The device's certificate pin as this sign-in confirmed it
    /// (sessionPin): the home route's handshake checked it, or the device
    /// answered it over this sign-in (GetLocalEndpoint). pinKnown says
    /// whether there is one; pinAsked is when confirmPin last asked.
    private var signedInPin: Data?
    private var pinKnown = false
    private var pinAsked: Date?
    /// confirmPin asks once a minute at most per sign-in. As otc-sync's
    /// pinAskEvery.
    private static let pinAskEvery: TimeInterval = 60

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

    /// A home endpoint is stored for the configured address.
    var hasLocalEndpoint: Bool { queue.sync { local != nil } }

    /// The device's certificate pin as the signed-in connection confirmed
    /// it, nil when it didn't: the home route's TLS handshake checked it, or
    /// the device answered it over this sign-in (GetLocalEndpoint). That is
    /// the device's own identity (it keeps the certificate on its storage),
    /// which sync records are bound to (SyncPaths.RecordDevice). The pin
    /// stored from an earlier sign-in is not: a device set up again from
    /// scratch under the same name has another, and one whose home listener
    /// isn't up yet (local_unavailable) used to leave the record to the
    /// address alone. As otc-sync's SessionPin.
    func sessionPin() -> Data? { queue.sync { isOpen && signedIn && pinKnown ? signedInPin : nil } }

    /// Asks the device signed in to where it is on the home network when
    /// this sign-in hasn't confirmed its pin (sessionPin), at most once a
    /// minute, and says whether the pin is known now. An answer is kept as a
    /// sign-in's is; the connection doesn't move. As otc-sync's ConfirmPin.
    func confirmPin() async -> Bool {
        enum Start { case known, no, ask(NWConnection, String, UInt64) }
        let start: Start = queue.sync {
            guard isOpen, signedIn, let conn, let domain else { return .no }
            if pinKnown { return .known }
            if let asked = pinAsked, Date().timeIntervalSince(asked) < Self.pinAskEvery { return .no }
            pinAsked = Date()
            return .ask(conn, domain, localGen)
        }
        guard case let .ask(conn, domain, gen) = start else {
            if case .known = start { return true }
            return false
        }
        guard let resp = try? await request(on: conn, timeout: Self.localAskTimeout, { req in
            req.payload = .reqGetLocalEndpoint(Msg_GetLocalEndpoint())
        }) else { return false }
        let answer = Self.localAnswer(resp, domain: domain)
        return queue.sync { () -> Bool in
            guard self.conn === conn, isOpen, signedIn, gen == localGen, self.domain == domain else { return false }
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
                if !(local?.sameRoute(as: ep) ?? false) {
                    local = ep
                    skipHomeOnce = false
                    LocalEndpointStore.save(ep)
                }
                signedInPin = ep.pin
                pinKnown = true
                return true
            }
        }
    }

    /// Whether the device answers on the home network right now, pinned as
    /// always, without touching the current connection: a WebSocket that
    /// opens is closed again at once. Away from home nothing shows.
    func probeHome() async -> Bool {
        await withCheckedContinuation { (cont: CheckedContinuation<Bool, Never>) in
            queue.async { [weak self] in
                guard let self, let local = self.local, !local.urls.isEmpty else {
                    cont.resume(returning: false)
                    return
                }
                // The race keeps itself alive until it is decided, and
                // calls this once.
                LocalRace(queue: self.queue) { winner in
                    winner?.stateUpdateHandler = nil
                    winner?.cancel()
                    cont.resume(returning: winner != nil)
                }.start(urls: local.urls, pin: local.pin)
            }
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
            flushAndFail(RequestFailure.cancelled)
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
        let pin = local.pin
        let race = LocalRace(queue: queue) { [weak self] winner in
            guard let self else { winner?.cancel(); return }
            // connect() or disconnect() since: theirs is the connection.
            guard gen == self.connGen, self.autoReconnect else { winner?.cancel(); return }
            self.localRace = nil
            if let winner {
                self.adopt(winner, route: .home, pin: pin)
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
        connPin = nil
        watch(conn)
        conn.start(queue: queue)
    }

    /// The home connection that won the race, already open, pinned to `pin`.
    private func adopt(_ conn: NWConnection, route: ConnectionRoute, pin: Data) {
        self.conn = conn
        self.route = route
        connPin = pin
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
                self.flushAndFail(RequestFailure.cancelled)
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
        // This socket's sign-in confirms its own pin, or none.
        signedInPin = nil
        pinKnown = false
        pinAsked = nil
        lastSeen = Date()
        sending = [:]
        startKeepAlive(conn)
        receiveLoop()
        authenticateThenAnnounce()
    }

    /// The keep-alive of `conn` (see pingEvery): on the queue, once a
    /// second; it ends when `conn` is no longer the current socket.
    private func startKeepAlive(_ conn: NWConnection) {
        keepAliveTimer?.cancel()
        lastPing = Date()
        let timer = DispatchSource.makeTimerSource(queue: queue)
        timer.schedule(deadline: .now() + 1, repeating: 1)
        timer.setEventHandler { [weak self] in
            guard let self else { return }
            guard self.conn === conn, self.isOpen else {
                timer.cancel()
                return
            }
            if Date().timeIntervalSince(self.lastPing) >= Self.pingEvery {
                self.lastPing = Date()
                let meta = NWProtocolWebSocket.Metadata(opcode: .ping)
                meta.setPongHandler(self.queue) { [weak self] error in
                    guard let self, error == nil, self.conn === conn else { return }
                    self.lastSeen = Date()
                }
                let ctx = NWConnection.ContentContext(identifier: "ping", metadata: [meta])
                conn.send(content: nil, contentContext: ctx, isComplete: true, completion: .idempotent)
            }
            let quiet = Date().timeIntervalSince(self.lastSeen)
            let stuck = self.sending.values.contains { $0 < Date() }
            if (self.sending.isEmpty && quiet > Self.deadAfter) || stuck {
                print("WSClient: no answer from the device for \(Int(quiet))s - connecting again")
                timer.cancel()
                // Its .cancelled reconnects, as for any drop.
                self.isOpen = false; self.signedIn = false
                conn.cancel()
            }
        }
        keepAliveTimer = timer
        timer.resume()
    }

    /// Manual stop. Also disables auto-reconnect (call `enableAutoReconnect()` to re-enable).
    func disconnect() {
        queue.async { [weak self] in
            guard let self else { return }
            self.autoReconnect = false
            self.isOpen = false; self.signedIn = false
            self.localRace?.cancel()
            self.localRace = nil
            self.flushAndFail(RequestFailure.closed)
            self.conn?.cancel()
            self.conn = nil
        }
        print("Disconnected")
    }

    func isConnected() -> Bool { queue.sync { isOpen && signedIn } }

    /// How many times this client has signed in. Read before a request, a
    /// different count afterwards means the connection it went out on has
    /// been replaced by a new one (issue #205: an upload cut by a drop goes
    /// on once there is one). As otc-sync's SignIns.
    func signInCount() -> UInt64 { queue.sync { signIns } }

    /// Issue #205: `error` is a request's connection going away under it,
    /// or not there to begin with - neither the device's answer, nor one
    /// that took too long (timedOut), nor the app closing it (closed). As
    /// otc-sync's wsclient.Dropped.
    static func isDropped(_ error: Error) -> Bool {
        if let failure = error as? RequestFailure {
            return failure == .notConnected || failure == .cancelled
        }
        return error is NWError
    }

    // MARK: Request/response
    /// Send a request built by `build` and await response (matched by `id`).
    func request(_ build: @escaping (inout Req) -> Void) async throws -> Resp {
        try await request(on: nil, build)
    }

    /// request, waiting at most `timeout` for the answer: a device that took
    /// a small request and never answered it held a folder's pass for the
    /// 30-minute bound (the keep-alive finds a dead connection sooner).
    func request(timeout: TimeInterval, _ build: @escaping (inout Req) -> Void) async throws -> Resp {
        try await request(on: nil, timeout: timeout, build)
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
                    cont.resume(throwing: RequestFailure.notConnected)
                    return
                }

                let req = Self.envelope(id: self.nextId, build)
                self.nextId &+= 1

                do {
                    let bytes = try req.serializedData()

                    // Store the waiter before sending
                    self.waiters[req.id] = cont
                    let id = req.id
                    self.queue.asyncAfter(deadline: .now() + (timeout ?? self.requestTimeout)) { [weak self] in
                        guard let self, let c = self.waiters.removeValue(forKey: id) else { return }
                        c.resume(throwing: RequestFailure.timedOut)
                    }

                    // Binary WS frame
                    let meta = NWProtocolWebSocket.Metadata(opcode: .binary)
                    let ctx = NWConnection.ContentContext(identifier: "req\(req.id)", metadata: [meta])

                    self.sending[req.id] = Date().addingTimeInterval(Self.sendTimeout(bytes.count))
                    conn.send(content: bytes, contentContext: ctx, isComplete: true, completion: .contentProcessed { sendErr in
                        if self.conn === conn {
                            self.sending.removeValue(forKey: req.id)
                            self.lastSeen = Date()
                        }
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

    /// The envelope a request sends: what `build` makes of it, then its id
    /// and the app's language as ReqEnvelope.lang - set after the build,
    /// which may replace the whole envelope. The one place lang is set
    /// (docs/i18n.md): every request carries it, the sign-in's included,
    /// so the device answers in the language the app shows.
    static func envelope(id: Int32, lang: String = L10n.shared.code, _ build: (inout Req) -> Void) -> Req {
        var req = Req()
        req.id = id
        build(&req)
        req.id = id
        req.lang = lang
        return req
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
        // Issue #207: the device's own answer that it can't reach its
        // disks, before any sign-in - not a refusal of the password.
        if let message = Self.storageAnswer(pubKeyResp) { throw StorageError(message: message) }
        guard case .respPubKey(let pubKey) = pubKeyResp.payload else {
            if case .respAck(let ack) = pubKeyResp.payload, ack.code == "device_unreachable" || ack.code == "device_disabled" {
                throw UnreachableError(message: ack.errorMsg)
            }
            throw RequestFailure.noPublicKey
        }
        let encryptedKey = try PwCrypto.encryptPassword(key, pubKeyDER: pubKey.publicKey)

        let resp = try await request(on: conn, timeout: timeout) { req in
            var a = Auth()
            a.key = encryptedKey
            a.create = false
            req.payload = .reqAuth(a)
        }
        // The disks went between the key and the password: the password
        // was never looked at.
        if let message = Self.storageAnswer(resp) { throw StorageError(message: message) }
        if case .respAck(let ack) = resp.payload {
            if ack.ok { return true }
            throw AuthError(message: ack.errorMsg.isEmpty ? nil : ack.errorMsg,
                            retryAfter: ack.code == "too_many_attempts" ? Int(ack.retryAfterSeconds) : nil)
        }
        return false
    }

    struct UnreachableError: Error {
        let message: String
    }

    /// The device answered that it can't reach its storage disks (issue
    /// #207): its words, "" when it gave none.
    struct StorageError: Error {
        let message: String
    }

    /// The device's words when `resp` says it can't reach its storage
    /// (the envelope's error_code, or an Ack's code), nil otherwise. As
    /// otc-sync's storageAnswer.
    static func storageAnswer(_ resp: Resp) -> String? {
        let code = "storage_unavailable"
        if resp.errorCode == code {
            if !resp.errorMessage.isEmpty { return resp.errorMessage }
            if case .respAck(let ack) = resp.payload { return ack.errorMsg }
            return ""
        }
        if case .respAck(let ack) = resp.payload, ack.code == code {
            return ack.errorMsg.isEmpty ? resp.errorMessage : ack.errorMsg
        }
        return nil
    }

    struct AuthError: Error {
        /// The device's reason, nil when it gave none.
        let message: String?
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
                self.lastSeen = Date()
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
                    // Issue #207: the device lost its disks under this
                    // session: say so and sign in again, which lands in
                    // the storageRetry cycle until they are back.
                    if let message = Self.storageAnswer(resp), self.isOpen, self.signedIn {
                        print("WSClient: the device can't reach its storage - signing in again")
                        self.onStorageUnavailable?(message)
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
        // The home route's handshake checked it.
        let dialledPin = route == .home ? connPin : nil
        guard let key = self.key else { self.signedIn = true; self.signIns &+= 1; self.onConnect?(route); return }
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
                    var confirmed = dialledPin
                    if route == .bridge {
                        let (worthTrying, answered) = await self.learnLocalEndpoint(on: conn)
                        if worthTrying {
                            let switched = self.queue.sync { () -> Bool in
                                guard self.conn === conn, self.autoReconnect, !self.triedNewHome else { return false }
                                self.triedNewHome = true
                                self.backoffSeconds = 1
                                self.startConnecting()
                                return true
                            }
                            if switched { return }
                        }
                        // The device's own answer, on this sign-in.
                        if let answered { confirmed = answered }
                    }
                    let (current, missedHome) = self.queue.sync { () -> (Bool, Bool) in
                        // Open still: a socket that dropped while the
                        // device was asked is already reconnecting.
                        guard self.conn === conn, self.isOpen else { return (false, false) }
                        self.backoffSeconds = 1; self.signedIn = true; self.signIns &+= 1
                        self.signedInPin = confirmed
                        self.pinKnown = confirmed != nil
                        self.pinAsked = nil
                        let missed = route == .bridge && self.triedNewHome
                        self.triedNewHome = false
                        return (true, missed)
                    }
                    if current {
                        if missedHome { self.onHomeMissed?() }
                        self.onConnect?(route)
                    }
                } else {
                    self.failAuth(nil, retryAfter: nil, on: conn)
                }
            } catch let err as StorageError {
                // The device is there but can't reach its disks: say so,
                // close, and let the .cancelled handler ask again in
                // storageRetry (a sign-in puts the delay back to 1 s).
                self.queue.async {
                    guard self.conn === conn else { return }
                    self.onStorageUnavailable?(err.message)
                    self.backoffSeconds = Self.storageRetry
                    self.isOpen = false; self.signedIn = false
                    conn.cancel()
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
                print("WSClient: sign-in did not complete (\(RequestFailure.logText(error))) - reconnecting")
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
    /// on the home network, and stores or forgets that. `worthTrying` when
    /// it gave an endpoint other than the one held; `pin`, the pin the
    /// device gave (nil when it gave none, or didn't answer).
    private func learnLocalEndpoint(on conn: NWConnection) async -> (worthTrying: Bool, pin: Data?) {
        let (gen, domain) = queue.sync { (localGen, self.domain) }
        guard let domain else { return (false, nil) }
        let resp: Resp
        do {
            resp = try await request(on: conn, timeout: Self.localAskTimeout) { req in
                req.payload = .reqGetLocalEndpoint(Msg_GetLocalEndpoint())
            }
        } catch {
            // A network error: what is stored stays.
            return (false, nil)
        }
        let answer = Self.localAnswer(resp, domain: domain)
        return queue.sync { () -> (Bool, Data?) in
            // Forgotten meanwhile (Disconnect, another device): not stored.
            guard gen == localGen, self.domain == domain else { return (false, nil) }
            switch answer {
            case .keep:
                return (false, nil)
            case .clear:
                if local != nil {
                    local = nil
                    LocalEndpointStore.clear()
                }
                return (false, nil)
            case .store(let ep):
                if let local, local.sameRoute(as: ep) { return (false, ep.pin) }
                local = ep
                skipHomeOnce = false
                LocalEndpointStore.save(ep)
                return (true, ep.pin)
            }
        }
    }

    private func failAuth(_ message: String?, retryAfter: Int?, on conn: NWConnection) {
        queue.async { [weak self] in
            guard let self, self.conn === conn else { return }
            self.autoReconnect = false
            self.isOpen = false; self.signedIn = false
            self.flushAndFail(RequestFailure.passwordRejected(message))
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

/// Why a request got no answer from the device. A folder's row and the
/// device folder picker show it, in the language in effect when they read
/// it (errorDescription); a log has the case (logText).
enum RequestFailure: LocalizedError, Equatable {
    case notConnected
    case timedOut
    /// The connection was replaced (a route switch, a reconnect).
    case cancelled
    /// The app closed the connection.
    case closed
    case noPublicKey
    /// The device refused the password: its reason, nil when it gave none.
    case passwordRejected(String?)

    var errorDescription: String? {
        switch self {
        case .notConnected: return S.macosConnectionNotConnected
        case .timedOut: return S.macosConnectionTimedOut
        case .cancelled: return S.macosConnectionCancelled
        case .closed: return S.macosConnectionClosed
        case .noPublicKey: return S.macosConnectionNoPublicKey
        case .passwordRejected(let message): return message ?? S.macosConnectionPasswordRejected
        }
    }

    /// A thrown error as a log has it: a RequestFailure by its case (logs
    /// stay English, docs/i18n.md), anything else in its own words.
    static func logText(_ error: Error) -> String {
        error is RequestFailure ? String(describing: error) : error.localizedDescription
    }
}
