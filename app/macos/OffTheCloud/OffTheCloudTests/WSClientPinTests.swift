// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Network
import Security
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Sync records go by the device's home-network certificate pin only as the
/// signed-in connection confirmed it (WSClient.sessionPin): the device's own
/// GetLocalEndpoint answer on this sign-in, or the home route's pinned
/// handshake - never the pin stored from an earlier sign-in, which a device
/// set up again from scratch under the same name doesn't have. A sign-in
/// with no pin asks again (confirmPin), once a minute at most. As otc-sync's
/// wsclient local tests. The app hosts the target's tests, so like
/// WSClientStorageTests this runs in a scratch SwiftPM package holding
/// WSClient.swift and what it needs, with an in-memory Keychain stand-in.
struct WSClientPinTests {
    @Test func aPinComesFromThisSignInOnly() async throws {
        let device = try PinStandIn()
        let url = try await device.start()
        defer { device.stop() }
        let ws = WSClient()
        let seen = PinSeen()
        ws.onConnect = { _ in seen.add("connected") }
        ws.configure(domain: url.absoluteString, key: "secret")
        ws.connect()
        defer { ws.disconnect() }

        // Its home listener isn't up yet (local_unavailable): no pin.
        try await pinUntil { seen.has("connected") }
        #expect(ws.isConnected())
        #expect(ws.sessionPin() == nil)
        #expect(device.endpointAsks == 1)

        // Asked again: still none, and not again within the minute.
        #expect(await ws.confirmPin() == false)
        #expect(device.endpointAsks == 2)
        device.listening = true
        #expect(await ws.confirmPin() == false)
        #expect(device.endpointAsks == 2)
        #expect(ws.sessionPin() == nil)
    }

    @Test func confirmPinTakesTheDevicesAnswer() async throws {
        let device = try PinStandIn()
        let url = try await device.start()
        defer { device.stop() }
        let ws = WSClient()
        let seen = PinSeen()
        ws.onConnect = { _ in seen.add("connected") }
        ws.configure(domain: url.absoluteString, key: "secret")
        ws.connect()
        defer { ws.disconnect() }
        try await pinUntil { seen.has("connected") }
        #expect(ws.sessionPin() == nil)

        device.listening = true
        #expect(await ws.confirmPin())
        #expect(ws.sessionPin() == PinStandIn.pin)
        // Known: nothing more to ask.
        #expect(await ws.confirmPin())
        #expect(device.endpointAsks == 2)

        // The next sign-in starts without it; the device's answer there
        // gives it again.
        device.listening = false
        ws.connect()
        try await pinUntil { device.auths == 2 && ws.isConnected() }
        #expect(ws.sessionPin() == nil)
    }

    @Test func theSignInsOwnAnswerGivesThePin() async throws {
        let device = try PinStandIn()
        device.listening = true
        let url = try await device.start()
        defer { device.stop() }
        let ws = WSClient()
        let seen = PinSeen()
        ws.onConnect = { _ in seen.add("connected") }
        ws.configure(domain: url.absoluteString, key: "secret")
        ws.connect()
        defer { ws.disconnect() }
        // A new home endpoint is tried at once (an address with no route
        // here), and the app signs in through the configured address again,
        // where the device's answer is the pin.
        try await pinUntil(20) { seen.has("connected") }
        #expect(ws.sessionPin() == PinStandIn.pin)
    }
}

private final class PinSeen: @unchecked Sendable {
    private let lock = NSLock()
    private var list: [String] = []
    func add(_ s: String) { lock.withLock { list.append(s) } }
    func has(_ s: String) -> Bool { lock.withLock { list.contains(s) } }
}

private final class PinOnce: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func first() -> Bool { lock.withLock { defer { done = true }; return !done } }
}

private func pinUntil(_ seconds: TimeInterval = 10, _ done: () -> Bool) async throws {
    let end = Date() + seconds
    while !done() {
        if Date() > end {
            Issue.record("timed out")
            return
        }
        try await Task.sleep(for: .milliseconds(20))
    }
}

/// A WebSocket server on the loopback that signs in any password and
/// answers GetLocalEndpoint with local_unavailable - or, once `listening`,
/// with a home endpoint whose address has no route here (an IPv6 unique
/// local one), so the app's try at home fails at once.
private final class PinStandIn: @unchecked Sendable {
    static let pin = Data((0..<32).map { UInt8($0) })
    private let listener: NWListener
    private let queue = DispatchQueue(label: "pin-stand-in")
    private let lock = NSLock()
    private var _endpointAsks = 0
    private var _auths = 0
    private var _listening = false
    private let publicKey: Data
    var endpointAsks: Int { lock.withLock { _endpointAsks } }
    var auths: Int { lock.withLock { _auths } }
    var listening: Bool {
        get { lock.withLock { _listening } }
        set { lock.withLock { _listening = newValue } }
    }

    init() throws {
        let ws = NWProtocolWebSocket.Options()
        ws.autoReplyPing = true
        let params = NWParameters.tcp
        params.defaultProtocolStack.applicationProtocols.insert(ws, at: 0)
        params.requiredLocalEndpoint = NWEndpoint.hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: params)
        publicKey = try Self.spkiKey()
        listener.newConnectionHandler = { [weak self] conn in
            guard let self else { return }
            conn.start(queue: self.queue)
            self.receive(on: conn)
        }
    }

    /// An RSA-2048 public key as the device sends it: SubjectPublicKeyInfo.
    private static func spkiKey() throws -> Data {
        let attrs: [CFString: Any] = [kSecAttrKeyType: kSecAttrKeyTypeRSA, kSecAttrKeySizeInBits: 2048]
        var error: Unmanaged<CFError>?
        guard let key = SecKeyCreateRandomKey(attrs as CFDictionary, &error),
              let pub = SecKeyCopyPublicKey(key),
              let pkcs1 = SecKeyCopyExternalRepresentation(pub, &error) as Data? else {
            throw URLError(.cannotCreateFile)
        }
        let header: [UInt8] = [0x30, 0x82, 0x01, 0x22, 0x30, 0x0d, 0x06, 0x09, 0x2a, 0x86, 0x48, 0x86,
                               0xf7, 0x0d, 0x01, 0x01, 0x01, 0x05, 0x00, 0x03, 0x82, 0x01, 0x0f, 0x00]
        return Data(header) + pkcs1
    }

    func start() async throws -> URL {
        let port: UInt16 = try await withCheckedThrowingContinuation { cont in
            let once = PinOnce()
            listener.stateUpdateHandler = { [listener] state in
                switch state {
                case .ready: if once.first() { cont.resume(returning: listener.port?.rawValue ?? 0) }
                case .failed(let e): if once.first() { cont.resume(throwing: e) }
                default: break
                }
            }
            listener.start(queue: queue)
        }
        return URL(string: "ws://127.0.0.1:\(port)/ws")!
    }

    func stop() { listener.cancel() }

    private func receive(on conn: NWConnection) {
        conn.receiveMessage { [weak self] data, context, _, error in
            guard let self else { return }
            let meta = context?.protocolMetadata(definition: NWProtocolWebSocket.definition) as? NWProtocolWebSocket.Metadata
            if let data, meta?.opcode == .binary, let req = try? Req(serializedBytes: data) {
                self.answer(req, on: conn)
            }
            if error == nil { self.receive(on: conn) }
        }
    }

    private func answer(_ req: Req, on conn: NWConnection) {
        var resp = Resp()
        resp.id = req.id
        switch req.payload {
        case .reqGetPubKey:
            var pk = Msg_PubKey()
            pk.publicKey = publicKey
            resp.payload = .respPubKey(pk)
        case .reqAuth:
            lock.withLock { _auths += 1 }
            var ack = Ack()
            ack.ok = true
            resp.payload = .respAck(ack)
        case .reqGetLocalEndpoint:
            lock.withLock { _endpointAsks += 1 }
            if listening {
                var ep = Msg_LocalEndpoint()
                ep.addresses = ["fd5e:0:0:7::1"]
                ep.port = 8443
                ep.certSha256 = Self.pin
                resp.payload = .respLocalEndpoint(ep)
            } else {
                resp.error = true
                resp.errorCode = "local_unavailable"
            }
        default:
            resp.error = true
            resp.errorCode = "unknown_payload"
        }
        guard let data = try? resp.serializedData() else { return }
        let meta = NWProtocolWebSocket.Metadata(opcode: .binary)
        let ctx = NWConnection.ContentContext(identifier: "answer", metadata: [meta])
        conn.send(content: data, contentContext: ctx, isComplete: true, completion: .contentProcessed { _ in })
    }
}
