// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  OTCConnection.swift
//  OffTheCloud
//
//  Shared, app-wide WebSocket connection + auth state. This is the native
//  analogue of the web app's `useWS` singleton (web/src/net/useWS.ts): every
//  screen issues requests through OTCConnection.shared.request(...), which
//  transparently connects and authenticates on first use, re-authenticates
//  after a drop, and retries a request once if the socket died mid-flight.

import Foundation
import CryptoKit

@MainActor
final class OTCConnection: ObservableObject {
    static let shared = OTCConnection()

    @Published private(set) var authenticated = false {
        didSet {
            if !authenticated {
                route = nil
                homeLink = nil
            }
        }
    }
    @Published private(set) var lastError: String?
    /// Issue #56: the bridge's machine-readable verdict when it couldn't
    /// hand this request to a device at all - "device_unreachable" (the
    /// domain is registered but nothing is connected) or
    /// "account_disabled" (issue #93). Matches Ack.code; see the field's
    /// own comment in proto/messages.proto. nil whenever the device is
    /// answering normally, which is what lets the UI built on it clear
    /// itself the moment things recover.
    @Published private(set) var statusCode: String?
    /// Set whenever connecting or signing in fails for any reason - a bad
    /// address, an unreachable host, a refused handshake, a wrong password
    /// - with lastError saying which. Cleared the moment a connection
    /// succeeds. MainView shows the connection form over the tabs while
    /// this is set, because a wrong endpoint or password is something the
    /// owner has to fix, and an app that keeps retrying in silence behind
    /// empty tabs gives them nothing to fix it with.
    @Published private(set) var connectionFailed = false

    /// Issue #190: how the signed-in connection reaches the device, for
    /// Settings. nil while there is none.
    enum Route: Equatable {
        case home
        /// Through the saved endpoint: the bridge, or an address of the
        /// owner's own.
        case remote(host: String)

        /// Settings' one line. Worded as on Android.
        var description: String {
            switch self {
            case .home:
                return "Connected over your home network"
            case .remote(let host):
                let bridge = SecretsStore.bridgeDomain
                return "Connected through \(host == bridge || host.hasSuffix("." + bridge) ? bridge : host)"
            }
        }
    }
    @Published private(set) var route: Route?
    /// The home-network address this connection went to (issue #190), nil
    /// through the endpoint. Media URLs go the same way (MediaStream).
    private(set) var homeLink: HomeLink?

    private let ws = WSClient()
    private var connectTask: Task<Void, Error>?
    /// Bumped by invalidate(): an answer about the home network that
    /// arrives after the endpoint changed, or after Log Out, is dropped.
    private var generation = 0
    /// Bumped by every sign-in; see request().
    private var connectionEpoch = 0
    /// The normalized endpoint of the signed-in connection.
    private var connectedEndpoint = ""
    private var routeCheck: Task<Void, Never>?
    private var networkSettle: Task<Void, Never>?
    private var backoffSeconds: TimeInterval = 1
    private let maxBackoffSeconds: TimeInterval = 30

    /// The device turned the password down. Every poller (notifications,
    /// the feed, Settings' status) and request()'s own retry used to send
    /// the same rejected password again at once, which tripped the
    /// device's lockout (5 failures a minute per address - the
    /// household's public IP through the bridge) for every client behind
    /// it. Until `notBefore`, an attempt with the same credentials fails
    /// with the same error without dialling. Memory only; keyed on a hash
    /// of the credentials, so saving new ones anywhere lifts it at once.
    private struct AuthRejection {
        let credKey: String
        let error: Error
        let notBefore: Date
    }
    private var authRejection: AuthRejection?
    /// 5 s doubling: at most 4 failures in the first minute, under the
    /// device's 5-per-minute lockout.
    private var authBackoff: TimeInterval = 5
    private let maxAuthBackoff: TimeInterval = 300

    private init() {
        let ws = ws
        Task {
            await ws.setOnDisconnect { [weak self] in
                Task { @MainActor [weak self] in self?.handleDisconnect() }
            }
        }
        NetworkWatch.shared.start { [weak self] in
            Task { @MainActor [weak self] in self?.networkChanged() }
        }
    }

    /// Sends a request, connecting/authenticating first if needed. Retries
    /// once end-to-end (a fresh connect+auth, then the request again) if
    /// anything in that path fails — including the connect/auth handshake
    /// itself, not just the request after it succeeded. Without covering the
    /// handshake too, a transient failure right as the device restarts (the
    /// common case: a deploy) surfaced as a hard error instead of quietly
    /// reconnecting, since ensureConnected() throwing used to bypass the
    /// retry entirely.
    func request(_ build: @escaping (inout Msg_ReqEnvelope) -> Void) async throws -> Msg_RespEnvelope {
        var epoch: Int?
        do {
            try await ensureConnected()
            epoch = connectionEpoch
            return try await ws.request(build: build)
        } catch {
            // Issue #190: a request cut off by a route switch failed on a
            // socket already replaced. Its successor is up, or on its way
            // (connectTask), and marking it down would connect yet again.
            if epoch == nil || epoch == connectionEpoch {
                authenticated = false
            }
            try await ensureConnected()
            return try await ws.request(build: build)
        }
    }

    /// Connects and authenticates if not already; safe to call from many
    /// places concurrently — callers share the one in-flight attempt.
    func ensureConnected() async throws {
        if authenticated { return }
        if let existing = connectTask {
            try await existing.value
            return
        }
        let task = Task { try await self.connectAndAuth() }
        connectTask = task
        defer { connectTask = nil }
        try await task.value
    }

    /// Call after the user changes endpoint/password in Settings, so the
    /// next request re-authenticates against the new credentials instead of
    /// assuming the old session is still good.
    func invalidate() {
        Task { await ws.close() }
        authenticated = false
        generation &+= 1
        backoffSeconds = 1
        // An explicit retry or new credentials: try at once.
        authRejection = nil
        authBackoff = 5
        // Media URLs are resolved against the endpoint; it may just have
        // changed.
        MediaStream.reset()
    }

    /// Log Out: drop the connection and forget what went wrong with the
    /// last one, so the next sign-in starts without a stale error over it.
    func reset() {
        invalidate()
        lastError = nil
        statusCode = nil
        connectionFailed = false
    }

    /// `raced`: a home-network socket a route check already opened (see
    /// checkRoute), signed in over instead of racing again.
    private func connectAndAuth(raced: (link: HomeLink, task: URLSessionWebSocketTask)? = nil) async throws {
        // Read off the main actor: this class is @MainActor, and
        // loadOrCreate() does three synchronous Keychain reads, which are
        // slow the first time a process wakes the keychain daemon. Doing
        // that here blocked the whole UI at exactly the moment a tab was
        // trying to load its first data - every tab, since a blocked main
        // thread blocks all of them.
        let (secrets, stored) = await Task.detached(priority: .userInitiated) {
            let secrets = SecretsStore.loadOrCreate()
            return (secrets, LocalEndpoint.stored(for: secrets.endpointURLString))
        }.value
        // Normalized (scheme and /ws filled in) - see
        // SecretsStore.normalizedEndpoint for why the stored value can't
        // be dialled as typed.
        guard let url = URL(string: secrets.endpointURLString) else {
            raced?.task.cancel()
            lastError = "The address \"\(secrets.endpoint)\" isn't valid."
            connectionFailed = true
            throw NSError(domain: "OTCConnection", code: 1, userInfo: [NSLocalizedDescriptionKey: lastError ?? "Bad endpoint"])
        }
        let credKey = Self.credentialsKey(secrets)
        if let r = authRejection, r.credKey == credKey, Date() < r.notBefore {
            raced?.task.cancel()
            throw r.error
        }

        // Issue #190: the home network first, on every connect, when the
        // device has told us where it is there. The socket only opens once
        // the device's certificate matched the pin; then the sign-in below
        // runs over it exactly as through the bridge.
        var home = raced
        if home == nil, let stored, !NetworkWatch.shared.onlyCellular {
            home = await PinnedSession.forPin(stored.pin).race(stored)
        }
        if let home {
            await ws.adopt(home.task)
            let before = (lastError, statusCode)
            // A socket that opened and then stalls must not hold every
            // request: closing it fails the sign-in, which then goes
            // through the endpoint.
            let ws = ws
            let watchdog = Task {
                try await Task.sleep(for: .seconds(15))
                await ws.close(ifCurrent: home.task)
            }
            defer { watchdog.cancel() }
            do {
                try await signIn(secrets, credKey: credKey)
                print("[home] signed in over the home network at \(home.link.address)")
                connected(home: home.link, endpoint: secrets.endpointURLString)
                return
            } catch {
                await ws.close()
                // The device's own verdict on the password: the bridge
                // would only hear the same one and count another failure.
                if Self.isPasswordVerdict(error) {
                    connectionFailed = true
                    throw error
                }
                print("[home] the sign-in at \(home.link.address) failed, going through the endpoint: \(error)")
                (lastError, statusCode) = before
            }
        }

        do {
            try await ws.connect(url: url)
        } catch {
            lastError = Self.describe(error)
            connectionFailed = true
            throw error
        }

        do {
            try await signIn(secrets, credKey: credKey)
        } catch {
            await ws.close()
            // Any failure on the way in, not just the two the bridge names:
            // a refused handshake or an unreachable host arrives here as a
            // URLSession error with nothing set above, and the owner
            // should see it rather than empty tabs.
            if lastError == nil { lastError = Self.describe(error) }
            connectionFailed = true
            throw error
        }

        connected(home: nil, endpoint: secrets.endpointURLString)
        refreshLocalEndpoint(for: secrets.endpointURLString, stored: stored)
    }

    /// GetPubKey, then Auth with the password sealed to that key, over the
    /// socket `ws` holds. Throws with lastError (and statusCode) set to the
    /// answer when there was one.
    ///
    /// Everything here talks over a live socket that, once connected
    /// through the bridge, is pinned to this app's session for as long as
    /// it stays open (the bridge hands one of its device's pool
    /// connections to whoever connects, then reuses that same pairing for
    /// every later message — it doesn't release it back after just one
    /// request/response). So a failure partway through must close the
    /// socket before giving up, not just abandon it - the callers do:
    /// leaving it open would sit there holding that pool slot hostage for
    /// nothing, since a handshake that already failed here (bad pubkey
    /// response, rejected auth) isn't going to start working by being left
    /// alone.
    private func signIn(_ secrets: SecretsStore, credKey: String) async throws {
        // Fetch this connection's ephemeral public key and encrypt the
        // password with it before it ever leaves the device (issue #2:
        // the bridge only relays already-encrypted payloads).
        let pubKeyResp = try await ws.request { req in
            req.payload = .reqGetPubKey(Msg_GetPubKey())
        }
        guard case .respPubKey(let pubKey) = pubKeyResp.payload else {
            // Issue #56: this is where the bridge's "I can't reach that
            // device" answer actually lands - it can't return a public
            // key, because the device that would generate one isn't
            // there. The reply's own RespAck carries both the reason
            // and a stable code; throwing a blanket "Unable to fetch
            // the connection's public key" here (as this used to) threw
            // that explanation away and made an offline device look
            // identical to a genuinely broken one.
            var msg = "Unable to fetch the connection's public key"
            if case .respAck(let ack) = pubKeyResp.payload {
                if !ack.errorMsg.isEmpty { msg = ack.errorMsg }
                statusCode = ack.code.isEmpty ? nil : ack.code
            }
            lastError = msg
            throw NSError(domain: "OTCConnection", code: 2, userInfo: [NSLocalizedDescriptionKey: msg])
        }
        let encryptedKey = try PwCrypto.encryptPassword(secrets.password, pubKeyDER: pubKey.publicKey)

        var auth = Msg_Auth()
        auth.uuid = secrets.deviceId
        auth.key = encryptedKey
        auth.create = false

        let resp = try await ws.request { req in
            req.payload = .reqAuth(auth)
        }
        guard case .respAck(let ack) = resp.payload, ack.ok else {
            let msg: String
            var rejectedFor: TimeInterval?
            if case .respAck(let ack) = resp.payload {
                msg = ack.errorMsg
                statusCode = ack.code.isEmpty ? nil : ack.code
                // The device's own verdict on the password; the
                // bridge's (device_unreachable, account_disabled)
                // keeps today's retries.
                if ack.code.isEmpty {
                    rejectedFor = authBackoff
                } else if ack.code == "too_many_attempts" {
                    rejectedFor = max(TimeInterval(ack.retryAfterSeconds) + 1, authBackoff)
                }
            } else { msg = "Authentication failed" }
            lastError = msg
            let err = NSError(domain: "OTCConnection", code: 3, userInfo: [NSLocalizedDescriptionKey: msg])
            if let rejectedFor {
                authRejection = AuthRejection(credKey: credKey, error: err, notBefore: Date() + rejectedFor)
                authBackoff = min(authBackoff * 2, maxAuthBackoff)
            }
            throw err
        }
    }

    /// Auth answered, not ok: what the device said about the credentials.
    private static func isPasswordVerdict(_ error: Error) -> Bool {
        let ns = error as NSError
        return ns.domain == "OTCConnection" && ns.code == 3
    }

    private func connected(home: HomeLink?, endpoint: String) {
        lastError = nil
        statusCode = nil
        connectionFailed = false
        backoffSeconds = 1
        authRejection = nil
        authBackoff = 5
        connectionEpoch &+= 1
        connectedEndpoint = endpoint
        authenticated = true
        homeLink = home
        route = home != nil ? .home : .remote(host: URL(string: endpoint)?.host ?? endpoint)
        registerPushToken()
    }

    /// Issue #190: after every sign-in through the endpoint, asks the
    /// device where it is on the home network, for the next connect. Over
    /// this socket only, never a reconnect of its own.
    private func refreshLocalEndpoint(for endpoint: String, stored: LocalEndpoint?) {
        let gen = generation
        Task {
            let resp: Msg_RespEnvelope
            do {
                resp = try await ws.request { $0.payload = .reqGetLocalEndpoint(Msg_GetLocalEndpoint()) }
            } catch {
                return // a network error: what is stored stays
            }
            // On the main actor with no await in between, so this can't
            // land after Log Out's wipe or a new endpoint's save.
            guard gen == generation else { return }
            switch LocalEndpoint.update(for: resp, endpoint: endpoint) {
            case .store(let ep) where ep != stored:
                print("[home] the device is at \(ep.addresses) port \(ep.port) on the home network")
                ep.save()
            case .clear where stored != nil:
                print("[home] the device can't be reached on the home network any more: \(resp.errorCode)")
                LocalEndpoint.clear()
            default:
                break
            }
        }
    }

    // MARK: Issue #190: switching route

    /// A network change: once it has settled, reconsider the route.
    private func networkChanged() {
        networkSettle?.cancel()
        networkSettle = Task {
            try? await Task.sleep(for: .seconds(1))
            guard !Task.isCancelled else { return }
            await reconsiderRoute()
        }
    }

    /// A network change, or the app back in the foreground. A connection
    /// through the endpoint moves to the home network when the device
    /// answers there now; one on the home network that stopped answering
    /// (the phone left home) reconnects - home first, then the endpoint.
    /// Only while nothing is uploading, downloading or syncing: switching
    /// would cut that off, so it waits for the next natural reconnect.
    func reconsiderRoute() async {
        if let routeCheck {
            await routeCheck.value
            return
        }
        guard authenticated, connectTask == nil, !TransferActivity.shared.busy else { return }
        let check = Task { await self.checkRoute() }
        routeCheck = check
        await check.value
        routeCheck = nil
    }

    private func checkRoute() async {
        let gen = generation
        let epoch = connectionEpoch
        // Nothing happened meanwhile that this would undo.
        func stillIdle() -> Bool {
            gen == generation && epoch == connectionEpoch && authenticated && connectTask == nil && !TransferActivity.shared.busy
        }
        let endpoint = connectedEndpoint
        let stored = homeLink != nil ? nil : await Task.detached(priority: .utility) {
            LocalEndpoint.stored(for: endpoint)
        }.value
        let check = HomeNetwork.check(signedIn: authenticated, onHome: homeLink != nil, busy: TransferActivity.shared.busy,
                                      hasStored: stored != nil, onlyCellular: NetworkWatch.shared.onlyCellular)
        switch check {
        case .nothing:
            return
        case .pingHome:
            if await ws.ping(within: 2) { return }
            guard stillIdle() else { return }
            print("[home] \(homeLink?.address ?? "the home network") stopped answering; reconnecting")
            await reconnect(over: nil)
        case .tryHome:
            guard let stored, let won = await PinnedSession.forPin(stored.pin).race(stored) else { return }
            guard stillIdle() else {
                won.task.cancel()
                return
            }
            print("[home] the device answers on the home network at \(won.link.address); moving there")
            await reconnect(over: won)
        }
    }

    /// Signs in again the way ensureConnected does - concurrent requests
    /// wait for it - over `raced` when given, else from scratch.
    private func reconnect(over raced: (link: HomeLink, task: URLSessionWebSocketTask)?) async {
        authenticated = false
        let task = Task {
            if raced == nil { await self.ws.close() }
            try await self.connectAndAuth(raced: raced)
        }
        connectTask = task
        defer { connectTask = nil }
        _ = try? await task.value
    }

    /// Hands this phone's APNs token to the device on every sign-in, not
    /// only at app launch: a device set up (or reinstalled) after the app
    /// started would otherwise never learn it and send this phone no push
    /// at all. The device keeps one row per token, so repeating is free.
    private func registerPushToken() {
        guard let token = UserDefaults.standard.string(forKey: "apnsToken"), !token.isEmpty else { return }
        Task {
            _ = try? await self.request { req in
                var reg = Msg_RegisterApnsToken()
                reg.token = token
                req.payload = .reqRegisterApnsToken(reg)
            }
        }
    }

    /// What the auth gate compares: a hash, so no second plaintext copy of
    /// the password is kept around.
    private static func credentialsKey(_ s: SecretsStore) -> String {
        let material = s.endpointURLString + "\u{0}" + s.deviceId + "\u{0}" + s.password
        return SHA256.hash(data: Data(material.utf8)).map { String(format: "%02x", $0) }.joined()
    }

    /// Plain words for the errors URLSession hands back, which are not
    /// written for people ("There was a bad response from the server").
    private static func describe(_ error: Error) -> String {
        let ns = error as NSError
        if ns.domain == NSURLErrorDomain {
            switch ns.code {
            case NSURLErrorBadServerResponse:
                return "The address answered, but not as an Off The Cloud device. Check that it ends in /ws and points at your device or its bridge name."
            case NSURLErrorCannotFindHost, NSURLErrorDNSLookupFailed:
                return "That address can't be found. Check the device name or address."
            case NSURLErrorCannotConnectToHost, NSURLErrorTimedOut, NSURLErrorNetworkConnectionLost:
                return "The device isn't answering. It may be off, or the address may be wrong."
            case NSURLErrorNotConnectedToInternet:
                return "No internet connection."
            case NSURLErrorSecureConnectionFailed, NSURLErrorServerCertificateUntrusted, NSURLErrorServerCertificateHasBadDate:
                return "Couldn't make a secure connection to that address."
            default:
                break
            }
        }
        return ns.localizedDescription
    }

    private func handleDisconnect() {
        guard authenticated || connectTask != nil else { return }
        authenticated = false
        let delay = backoffSeconds
        backoffSeconds = min(backoffSeconds * 2, maxBackoffSeconds)
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            try? await self?.ensureConnected()
        }
    }
}
