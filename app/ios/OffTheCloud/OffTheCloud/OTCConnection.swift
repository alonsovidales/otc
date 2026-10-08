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
import UIKit

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
    /// Bumped at the moments a load that failed is worth asking for again
    /// at once, not at the end of its wait (Android's Wake; the web's
    /// usePageRetry wakes on the device's first good answer and on the
    /// browser's "online"): signed in again, a network came up, the app
    /// back in front.
    @Published private(set) var wakes = 0

    /// Issue #190: how the signed-in connection reaches the device, for
    /// Settings. nil while there is none.
    enum Route: Equatable {
        case home
        /// Through the saved endpoint: the bridge, or an address of the
        /// owner's own.
        case remote(host: String)

        /// Settings' one line. Worded as on Android and macOS.
        var description: String {
            switch self {
            case .home:
                return "Connected over your home network"
            case .remote(let host):
                // An endpoint whose URL has no host name.
                guard !host.isEmpty else { return "Connected through the configured address" }
                // Host names aren't case-sensitive: "Cala.Off-The.Cloud"
                // is the bridge too.
                let bridge = SecretsStore.bridgeDomain
                let h = host.lowercased()
                return "Connected through \(h == bridge || h.hasSuffix("." + bridge) ? bridge : host)"
            }
        }
    }
    @Published private(set) var route: Route?
    /// The home-network address this connection went to (issue #190), nil
    /// through the endpoint. Media URLs go the same way (MediaStream).
    private(set) var homeLink: HomeLink?
    /// The network the home connection was made over: while the phone is
    /// still on it, a live home socket stays (see checkRoute).
    private var homeNetwork: NetworkWatch.LocalNetwork?

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
    /// Issue #190: home attempts after a sign-in through the endpoint, and
    /// how many have been made (see scheduleHomeRetry).
    private var homeRetry: Task<Void, Never>?
    private var homeRetries = 0
    private static let homeRetryMax = 5
    /// A route check that a connect, a transfer or requests in flight put
    /// off: made once the connect or the last transfer is over.
    /// NWPathMonitor reports a change only once, so a dropped one left the
    /// app on the bridge at home - over mobile data, which iOS doesn't move
    /// to Wi-Fi - until the next foreground.
    private var recheckPending = false
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
        TransferActivity.shared.setOnIdle { [weak self] in
            Task { @MainActor [weak self] in self?.recheckIfPending() }
        }
        // Back in front: what failed while away is asked for again now,
        // and a socket left in the background - often dead without a word -
        // is checked rather than found out by the next request waiting on it.
        NotificationCenter.default.addObserver(forName: UIApplication.willEnterForegroundNotification, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor [weak self] in self?.cameToForeground() }
        }
    }

    /// How long what a screen shows waits for its answer before the request
    /// fails (WSClient.TimedOut) and the screen asks again (its own retry,
    /// 1 s doubling to 10 s), instead of hanging until the socket drops -
    /// through ask(), which gives a kind that timed out more time
    /// (Patience). As Android's PAGE_TIMEOUT_MS and LIST_TIMEOUT_MS.
    /// pageTimeout: a page of photos, posts or covers (people,
    /// collections), a folder's listing - a first page of Images from Pit
    /// measured 6-70 s over the bridge, so 2 minutes, above the bridge's
    /// own 90 s forward timeout (cForwardTimeout): through the bridge its
    /// "device unreachable" always comes first, and this only ends a
    /// request nothing will answer (at home, straight to the device,
    /// nothing else would). listTimeout: the small lists (tags, month
    /// counts), which still queue behind a page on the device's upload.
    /// The clock starts once the request has left the phone
    /// (WSClient.exchange), and connecting gets the same time on its own.
    /// Everything else keeps the socket's 30 minutes
    /// (WSClient.defaultTimeout: uploads, downloads in pieces, ApplyUpdate).
    nonisolated static let pageTimeout: TimeInterval = 120
    nonisolated static let listTimeout: TimeInterval = 60

    /// GetPubKey and Auth while signing in (Android's SIGN_IN_TIMEOUT_MS):
    /// above the bridge's 90 s, so its own "device unreachable" comes
    /// first, and far above an Argon2id derivation on a busy device. They
    /// used to wait for good, and every request waits on a sign-in in
    /// progress: a device that took the socket and never answered left
    /// Images on grey tiles through every wake and Try again. Tests
    /// shorten it.
    var signInTimeout: TimeInterval = 120

    /// How long each kind of what a screen shows waits now, by route.
    let patience = Patience()

    /// Why connecting failed when the phone has no network at all (the
    /// connection card's line), as on Android.
    nonisolated static let offlineMessage = "This phone is offline. Connect to Wi-Fi or mobile data."

    /// Sends a request, connecting/authenticating first if needed, and
    /// waits `timeout` seconds at most for its answer (nil: the socket's
    /// WSClient.defaultTimeout) - connecting included, which gets the same
    /// time on its own. Retries once end-to-end (a fresh connect+auth, then
    /// the request again) if anything in that path fails — including the
    /// connect/auth handshake itself, not just the request after it
    /// succeeded. Without covering the handshake too, a transient failure
    /// right as the device restarts (the common case: a deploy) surfaced as
    /// a hard error instead of quietly reconnecting, since
    /// ensureConnected() throwing used to bypass the retry entirely.
    func request(timeout: TimeInterval? = nil, _ build: @escaping (inout Msg_ReqEnvelope) -> Void) async throws -> Msg_RespEnvelope {
        try await exchange(timeout: timeout ?? WSClient.defaultTimeout, build).resp
    }

    /// What a screen shows, of `kind` ("page", "feed", "listing", "tags"...):
    /// `base` for its answer (pageTimeout, listTimeout), twice as long
    /// after each timeout in a row up to 5 minutes, and back down once
    /// answers come in time again - per kind and route (Patience, as
    /// Android's ask). Throws WSClient.TimedOut when the time ran out,
    /// connecting included.
    func ask(_ kind: String, base: TimeInterval, _ build: @escaping (inout Msg_ReqEnvelope) -> Void) async throws -> Msg_RespEnvelope {
        let key = "\(kind)/\(route == .home ? "home" : "bridge")"
        do {
            let a = try await exchange(timeout: patience.timeout(for: key, base: base), build)
            patience.answered(key, base: base, took: a.waited)
            return a.resp
        } catch let e as WSClient.TimedOut {
            // One that ran out while still connecting says nothing about
            // how fast the device answers.
            if e.answerTimedOut { patience.timedOut(key) }
            throw e
        }
    }

    private func exchange(timeout: TimeInterval, _ build: @escaping (inout Msg_ReqEnvelope) -> Void) async throws -> WSClient.Answer {
        var epoch: Int?
        do {
            try await connectWithin(timeout)
            epoch = connectionEpoch
            return try await ws.exchange(timeout: timeout, build: build)
        } catch let timedOut as WSClient.TimedOut {
            // No answer in its time on a socket that is still up (WSClient
            // checks it then, and one that is gone fails what it has in
            // flight as Unresponsive, below), or still connecting when the
            // time ran out: the session is fine, the device is slow or lost
            // this one. The caller asks again; signing in again first would
            // only add an Argon2id derivation to a device that is already
            // slow, and asking again here would double the wait before the
            // screen can say so.
            throw timedOut
        } catch {
            // Issue #190: a request cut off by a route switch failed on a
            // socket already replaced. Its successor is up, or on its way
            // (connectTask), and marking it down would connect yet again.
            if epoch == nil || epoch == connectionEpoch {
                authenticated = false
            }
            try await connectWithin(timeout)
            return try await ws.exchange(timeout: timeout, build: build)
        }
    }

    /// ensureConnected() within a request's own time (Android's
    /// connectWithin): a connect stuck somewhere (each attempt is bounded -
    /// WSClient.connectTimeout, signInTimeout - but a request may come in
    /// behind one) no longer leaves a screen waiting with nothing failed;
    /// the attempt itself goes on for whoever asks next. A sign-in's own
    /// timeout is the connection failing, not this request's answer.
    private func connectWithin(_ timeout: TimeInterval) async throws {
        if authenticated { return }
        do {
            if timeout >= WSClient.defaultTimeout {
                try await ensureConnected()
                return
            }
            let outcome = ConnectRace()
            let won: Bool = try await withCheckedThrowingContinuation { cont in
                outcome.start(cont)
                let timer = Task {
                    try? await Task.sleep(for: .seconds(timeout))
                    outcome.finish(.success(false))
                }
                Task { @MainActor in
                    do {
                        try await self.ensureConnected()
                        outcome.finish(.success(true))
                    } catch {
                        outcome.finish(.failure(error))
                    }
                    timer.cancel()
                }
            }
            if !won { throw WSClient.TimedOut(answerTimedOut: false) }
        } catch let e as WSClient.TimedOut where e.answerTimedOut {
            // GetPubKey or Auth unanswered: the connection failed.
            throw NSError(domain: "OTCConnection", code: 5, userInfo: [NSLocalizedDescriptionKey: e.localizedDescription])
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
        let gen = generation
        let task = Task { try await self.connectAndAuth(gen: gen) }
        connectTask = task
        defer {
            if connectTask == task { connectTask = nil }
            recheckIfPending()
        }
        try await task.value
    }

    /// Call after the user changes endpoint/password in Settings, so the
    /// next request re-authenticates against the new credentials instead of
    /// assuming the old session is still good.
    func invalidate() {
        Task { await ws.close() }
        homeRetry?.cancel()
        homeRetry = nil
        authenticated = false
        generation &+= 1
        recheckPending = false
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

    /// Thrown by a connect that Save Connection or Log Out overtook
    /// (invalidate): whatever it was doing belongs to settings that are
    /// gone.
    private static let superseded = NSError(domain: "OTCConnection", code: 4, userInfo: [
        NSLocalizedDescriptionKey: "The connection settings changed; try again.",
    ])

    /// `gen`: `generation` when this connect was decided on. Once
    /// invalidate() bumps it, the connect stops at its next step: it must
    /// not sign in to the old device, nor store what it says about the
    /// home network, after the user changed device or logged out.
    /// `raced`: a home-network socket a route check already opened (see
    /// checkRoute), signed in over instead of racing again.
    private func connectAndAuth(gen: Int, raced: (link: HomeLink, task: URLSessionWebSocketTask)? = nil) async throws {
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
        guard gen == generation else {
            raced?.task.cancel()
            throw Self.superseded
        }
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
        // No network at all: nothing to dial, and URLSession would wait for
        // one (waitsForConnectivity) with every request queued behind the
        // sign-in, so a screen could never say why it is empty. Failed at
        // once, as Android's connect does: the screens say the phone is
        // offline (and the connection card does, after a few seconds), and
        // ask again as soon as a network is back (wakes).
        if NetworkWatch.shared.offline {
            raced?.task.cancel()
            lastError = Self.offlineMessage
            connectionFailed = true
            throw URLError(.notConnectedToInternet)
        }

        // Issue #190: the home network first, on every connect, when the
        // device has told us where it is there. The socket only opens once
        // the device's certificate matched the pin; then the sign-in below
        // runs over it exactly as through the bridge.
        var home = raced
        // A raced socket belongs to the connection the route check looked
        // at. Another saved endpoint is another device: its password must
        // not go there.
        if let raced, raced.link.endpoint.endpoint != secrets.endpointURLString {
            raced.task.cancel()
            home = nil
        }
        if home == nil, let stored, !NetworkWatch.shared.onlyCellular {
            home = await PinnedSession.forPin(stored.pin).race(stored)
            guard gen == generation else {
                home?.task.cancel()
                throw Self.superseded
            }
        }
        if let home {
            // A route switch replaces a live socket: what is still in
            // flight on it gets a moment to finish rather than being sent
            // again on this one (see checkRoute).
            await ws.adopt(home.task, drain: 3)
            guard gen == generation else {
                await ws.close(ifCurrent: home.task)
                throw Self.superseded
            }
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
                // The catch below closes it.
                guard gen == generation else { throw Self.superseded }
                print("[home] signed in over the home network at \(home.link.address)")
                connected(home: home.link, endpoint: secrets.endpointURLString)
                return
            } catch {
                await ws.close()
                guard gen == generation else { throw Self.superseded }
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
            guard gen == generation else { throw Self.superseded }
            lastError = Self.describe(error)
            connectionFailed = true
            throw error
        }

        do {
            try await signIn(secrets, credKey: credKey)
            // The catch below closes it.
            guard gen == generation else { throw Self.superseded }
        } catch {
            await ws.close()
            guard gen == generation else { throw Self.superseded }
            // Any failure on the way in, not just the two the bridge names:
            // a refused handshake or an unreachable host arrives here as a
            // URLSession error with nothing set above, and the owner
            // should see it rather than empty tabs.
            if lastError == nil { lastError = Self.describe(error) }
            connectionFailed = true
            throw error
        }

        connected(home: nil, endpoint: secrets.endpointURLString)
        refreshLocalEndpoint(for: secrets.endpointURLString, stored: stored, gen: gen)
        scheduleHomeRetry(first: true)
    }

    /// Issue #190: on the bridge with a home endpoint stored and Wi-Fi or a
    /// wired network up, the home network is tried again: 30 s after a
    /// sign-in through the endpoint, then every 2 minutes, at most 5
    /// times. A home attempt that found nothing at a cold start (the Wi-Fi
    /// waking, the device busy) otherwise kept the phone on the bridge at
    /// home until the next foreground or network change; the Mac retries
    /// the same way.
    private func scheduleHomeRetry(first: Bool) {
        homeRetry?.cancel()
        if first { homeRetries = 0 }
        guard homeRetries < Self.homeRetryMax else { return }
        let gen = generation
        homeRetry = Task {
            try? await Task.sleep(for: .seconds(first ? 30 : 120))
            guard !Task.isCancelled, gen == self.generation else { return }
            self.homeRetries += 1
            guard self.authenticated, self.homeLink == nil, !NetworkWatch.shared.onlyCellular else { return }
            await self.reconsiderRoute()
            if self.homeLink == nil, gen == self.generation { self.scheduleHomeRetry(first: false) }
        }
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
        // Each step within signInTimeout: unanswered, the sign-in fails
        // and its caller closes the socket (a bridge pool slot held for
        // nothing otherwise), as a refused one.
        let pubKeyResp = try await ws.request(timeout: signInTimeout) { req in
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

        let resp = try await ws.request(timeout: signInTimeout) { req in
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
        // The grids' thumbnails kept on the phone are this device's (and
        // account's): another one's go (ThumbDiskCache).
        ThumbDiskCache.shared.use(endpoint: endpoint)
        authenticated = true
        homeLink = home
        homeNetwork = home != nil ? NetworkWatch.shared.localNetwork : nil
        route = home != nil ? .home : .remote(host: URL(string: endpoint)?.host ?? "")
        registerPushToken()
        // Back after a drop: what failed meanwhile is asked for again now.
        wake()
    }

    /// Issue #190: after every sign-in through the endpoint, asks the
    /// device where it is on the home network, for the next connect. Over
    /// this socket only, never a reconnect of its own. `gen`: the
    /// connect's own, so an answer for settings changed since it began is
    /// dropped.
    private func refreshLocalEndpoint(for endpoint: String, stored: LocalEndpoint?, gen: Int) {
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
                // Tried now, as on the Mac and in otc-sync: otherwise a
                // phone that stays on the home Wi-Fi with the app open
                // moved home only at its next foreground or network
                // change. reconsiderRoute waits for a connect or a
                // transfer still running.
                Task { await self.reconsiderRoute() }
            case .clear where stored != nil:
                print("[home] the device can't be reached on the home network any more: \(resp.errorCode)")
                LocalEndpoint.clear()
            default:
                break
            }
        }
    }

    // MARK: Issue #190: switching route

    /// A network change: once it has settled, what failed meanwhile is
    /// asked for again (a network came up) and the route reconsidered. A
    /// socket made over the network that went may be dead without a word
    /// (a phone moving between Wi-Fi and mobile data): it is checked now.
    private func networkChanged() {
        if !NetworkWatch.shared.offline { Task { await ws.verify() } }
        networkSettle?.cancel()
        networkSettle = Task {
            try? await Task.sleep(for: .seconds(1))
            guard !Task.isCancelled else { return }
            if !NetworkWatch.shared.offline { wake() }
            await reconsiderRoute()
        }
    }

    /// A network change, or the app back in the foreground. A connection
    /// through the endpoint moves to the home network when the device
    /// answers there now; one on the home network whose network is gone
    /// and that stopped answering (the phone left home) reconnects - home
    /// first, then the endpoint. Not while connecting, nor while anything
    /// is uploading, downloading or syncing: switching would cut that off.
    /// The check is then made once the connect or the last transfer is
    /// over.
    func reconsiderRoute() async {
        if let routeCheck {
            await routeCheck.value
            return
        }
        if connectTask != nil || TransferActivity.shared.busy {
            recheckPending = true
            return
        }
        recheckPending = false
        // Not signed in and not connecting: the next connect tries the
        // home network first anyway.
        guard authenticated else { return }
        let check = Task { await self.checkRoute() }
        routeCheck = check
        await check.value
        routeCheck = nil
    }

    /// The route check put off for a connect or a transfer, now that it
    /// is over.
    private func recheckIfPending() {
        guard recheckPending else { return }
        recheckPending = false
        Task { await reconsiderRoute() }
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
            // Home on a live socket over a network the phone is still on:
            // stay, as Android and macOS do. A ping is answered only when
            // the device reads its next frame, which a busy one (a large
            // upload, a burst of thumbnails) puts off for seconds; a missed
            // budget used to move the phone onto the bridge at home.
            // Leaving that network breaks the socket, and its drop
            // reconnects (handleDisconnect).
            if let net = homeNetwork, let now = NetworkWatch.shared.localNetwork, net.continues(in: now),
               await ws.connected { return }
            if await ws.ping(within: 5) { return }
            guard stillIdle() else { return }
            print("[home] \(homeLink?.address ?? "the home network") stopped answering; reconnecting")
            await reconnect(over: nil)
        case .tryHome:
            guard let stored, let won = await PinnedSession.forPin(stored.pin).race(stored) else { return }
            guard stillIdle() else {
                won.task.cancel()
                return
            }
            // Switching closes the socket in use, and a request it cuts
            // off is sent again on the new one - after the device may have
            // acted on it already (a like toggled back, a comment posted
            // twice). Only with nothing in flight; otherwise after the
            // next connect or transfer, or on the next change.
            guard await ws.waitIdle(within: 2), stillIdle() else {
                won.task.cancel()
                recheckPending = true
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
        let gen = generation
        let task = Task {
            if raced == nil { await self.ws.close() }
            try await self.connectAndAuth(gen: gen, raced: raced)
        }
        connectTask = task
        defer {
            if connectTask == task { connectTask = nil }
        }
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

    /// What failed to load is asked for again now (wakes).
    func wake() {
        wakes &+= 1
    }

    /// Waits `seconds`, cut short by a wake (Android's sleepOrWake) or the
    /// task's cancellation.
    func sleepOrWake(_ seconds: TimeInterval) async {
        let seen = wakes
        let end = Date() + seconds
        while wakes == seen, Date() < end, !Task.isCancelled {
            try? await Task.sleep(for: .milliseconds(min(250, max(1, end.timeIntervalSinceNow * 1000))))
        }
    }

    /// The app came back to the front: what failed while away is asked for
    /// again, and the socket is checked (WSClient.verify).
    private func cameToForeground() {
        Task { await ws.verify() }
        wake()
    }

    /// Plain words for the errors URLSession hands back, which are not
    /// written for people ("There was a bad response from the server").
    private static func describe(_ error: Error) -> String {
        // No network at all: not the address's fault, whatever failed.
        if NetworkWatch.shared.offline { return offlineMessage }
        if error is WSClient.Unresponsive {
            return "The device isn't answering. It may be off, or the connection may be too slow."
        }
        // The upgrade unanswered (Android: a SocketTimeoutException).
        if error is WSClient.ConnectTimeout {
            return "The device isn't answering. It may be off, or the address may be wrong."
        }
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
                return offlineMessage
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

/// connectWithin's two runners - the connect, the request's time - and the
/// one outcome that counts: whichever finishes first.
private final class ConnectRace: @unchecked Sendable {
    private let lock = NSLock()
    private var cont: CheckedContinuation<Bool, Error>?

    func start(_ c: CheckedContinuation<Bool, Error>) { lock.withLock { cont = c } }

    func finish(_ result: Result<Bool, Error>) {
        let c: CheckedContinuation<Bool, Error>? = lock.withLock {
            defer { cont = nil }
            return cont
        }
        c?.resume(with: result)
    }
}
