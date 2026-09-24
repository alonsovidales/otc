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

@MainActor
final class OTCConnection: ObservableObject {
    static let shared = OTCConnection()

    @Published private(set) var authenticated = false
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

    private let ws = WSClient()
    private var connectTask: Task<Void, Error>?
    private var backoffSeconds: TimeInterval = 1
    private let maxBackoffSeconds: TimeInterval = 30

    private init() {
        let ws = ws
        Task {
            await ws.setOnDisconnect { [weak self] in
                Task { @MainActor [weak self] in self?.handleDisconnect() }
            }
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
        do {
            try await ensureConnected()
            return try await ws.request(build: build)
        } catch {
            authenticated = false
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
        backoffSeconds = 1
    }

    /// Log Out: drop the connection and forget what went wrong with the
    /// last one, so the next sign-in starts without a stale error over it.
    func reset() {
        invalidate()
        lastError = nil
        statusCode = nil
        connectionFailed = false
    }

    private func connectAndAuth() async throws {
        // Read off the main actor: this class is @MainActor, and
        // loadOrCreate() does three synchronous Keychain reads, which are
        // slow the first time a process wakes the keychain daemon. Doing
        // that here blocked the whole UI at exactly the moment a tab was
        // trying to load its first data - every tab, since a blocked main
        // thread blocks all of them.
        let secrets = await Task.detached(priority: .userInitiated) {
            SecretsStore.loadOrCreate()
        }.value
        // Normalized (scheme and /ws filled in) - see
        // SecretsStore.normalizedEndpoint for why the stored value can't
        // be dialled as typed.
        guard let url = URL(string: secrets.endpointURLString) else {
            lastError = "The address \"\(secrets.endpoint)\" isn't valid."
            connectionFailed = true
            throw NSError(domain: "OTCConnection", code: 1, userInfo: [NSLocalizedDescriptionKey: lastError ?? "Bad endpoint"])
        }

        do {
            try await ws.connect(url: url)
        } catch {
            lastError = Self.describe(error)
            connectionFailed = true
            throw error
        }

        // Everything past this point talks over a live socket that, once
        // connected through the bridge, is pinned to this app's session for
        // as long as it stays open (the bridge hands one of its device's
        // pool connections to whoever connects, then reuses that same
        // pairing for every later message — it doesn't release it back
        // after just one request/response). So a failure partway through
        // the handshake below must close the socket before giving up,
        // not just abandon it: leaving it open would sit there holding
        // that pool slot hostage for nothing, since a handshake that
        // already failed here (bad pubkey response, rejected auth) isn't
        // going to start working by being left alone.
        do {
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
                if case .respAck(let ack) = resp.payload {
                    msg = ack.errorMsg
                    statusCode = ack.code.isEmpty ? nil : ack.code
                } else { msg = "Authentication failed" }
                lastError = msg
                throw NSError(domain: "OTCConnection", code: 3, userInfo: [NSLocalizedDescriptionKey: msg])
            }
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

        lastError = nil
        statusCode = nil
        connectionFailed = false
        backoffSeconds = 1
        authenticated = true
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
