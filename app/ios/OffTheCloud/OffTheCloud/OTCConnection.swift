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

    private let ws = WSClient()
    private var connectTask: Task<Void, Error>?
    private var backoffSeconds: TimeInterval = 1
    private let maxBackoffSeconds: TimeInterval = 30

    private init() {
        ws.onDisconnect = { [weak self] in
            Task { @MainActor [weak self] in self?.handleDisconnect() }
        }
    }

    /// Sends a request, connecting/authenticating first if needed. Retries
    /// once if the socket died mid-flight (e.g. woke from background).
    func request(_ build: @escaping (inout Msg_ReqEnvelope) -> Void) async throws -> Msg_RespEnvelope {
        try await ensureConnected()
        do {
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
        ws.close()
        authenticated = false
        backoffSeconds = 1
    }

    private func connectAndAuth() async throws {
        let secrets = SecretsStore.loadOrCreate()
        guard let url = URL(string: secrets.endpoint) else {
            throw NSError(domain: "OTCConnection", code: 1, userInfo: [NSLocalizedDescriptionKey: "Bad endpoint"])
        }

        try await ws.connect(url: url)

        // Fetch this connection's ephemeral public key and encrypt the
        // password with it before it ever leaves the device (issue #2: the
        // bridge only relays already-encrypted payloads).
        let pubKeyResp = try await ws.request { req in
            req.payload = .reqGetPubKey(Msg_GetPubKey())
        }
        guard case .respPubKey(let pubKey) = pubKeyResp.payload else {
            throw NSError(domain: "OTCConnection", code: 2, userInfo: [NSLocalizedDescriptionKey: "Unable to fetch the connection's public key"])
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
            if case .respAck(let ack) = resp.payload { msg = ack.errorMsg }
            else { msg = "Authentication failed" }
            lastError = msg
            throw NSError(domain: "OTCConnection", code: 3, userInfo: [NSLocalizedDescriptionKey: msg])
        }

        lastError = nil
        backoffSeconds = 1
        authenticated = true
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
