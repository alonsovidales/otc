// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Network
import Security
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Issue #207: a device that can't reach its storage disks answers every
/// request, GetPubKey first, with error_code "storage_unavailable" and its
/// words. Not a wrong password, not unreachable: onStorageUnavailable with
/// the words, the next ask only after WSClient.storageRetry (30 s - never
/// the 1 s of the other failures), at once on connect() (a network change
/// or a wake), and a sign-in as soon as the device answers otherwise. As
/// otc-sync's wsclient storage tests. The app hosts the target's tests, so
/// like WSClientLangTests this runs in a scratch SwiftPM package holding
/// WSClient.swift and what it needs, with an in-memory Keychain stand-in.
struct WSClientStorageTests {
    @Test func theDevicesWordsComeFromTheEnvelopeOrAnAck() {
        var envelope = Resp()
        envelope.error = true
        envelope.errorCode = "storage_unavailable"
        envelope.errorMessage = "words"
        #expect(WSClient.storageAnswer(envelope) == "words")

        var acked = Resp()
        var ack = Ack()
        ack.code = "storage_unavailable"
        ack.errorMsg = "ack words"
        acked.payload = .respAck(ack)
        #expect(WSClient.storageAnswer(acked) == "ack words")

        var bare = Resp()
        bare.error = true
        bare.errorCode = "storage_unavailable"
        #expect(WSClient.storageAnswer(bare) == "")

        var other = Resp()
        other.error = true
        other.errorCode = "unknown_payload"
        #expect(WSClient.storageAnswer(other) == nil)
        var unreachable = Resp()
        var u = Ack()
        u.code = "device_unreachable"
        unreachable.payload = .respAck(u)
        #expect(WSClient.storageAnswer(unreachable) == nil)
    }

    @Test func aDeviceWithoutItsDisksIsAskedAgainLaterAndSignsInOnceTheyAreBack() async throws {
        let device = try StorageStandIn()
        let url = try await device.start()
        defer { device.stop() }
        let ws = WSClient()
        let seen = Seen()
        ws.onStorageUnavailable = { seen.add("storage: \($0)") }
        ws.onAuthFailed = { _, _ in seen.add("auth") }
        ws.onUnreachable = { _ in seen.add("unreachable") }
        ws.onConnect = { _ in seen.add("connected") }
        ws.configure(domain: url.absoluteString, key: "secret")
        ws.connect()
        defer { ws.disconnect() }

        try await until { seen.has("storage: \(StorageStandIn.words)") }
        // Not on the growing delay, whose first step is 1 s.
        try await Task.sleep(for: .seconds(2.5))
        #expect(device.keyAsks == 1)
        #expect(!ws.isConnected())

        // A network change or a wake asks at once.
        ws.connect()
        try await until { device.keyAsks == 2 }

        // The disks are back: the next ask signs in.
        device.disksBack = true
        ws.connect()
        try await until { seen.has("connected") }
        #expect(!seen.has("auth"))
        #expect(!seen.has("unreachable"))
        #expect(device.auths == 1)
    }
}

private final class Seen: @unchecked Sendable {
    private let lock = NSLock()
    private var list: [String] = []
    func add(_ s: String) { lock.withLock { list.append(s) } }
    func has(_ s: String) -> Bool { lock.withLock { list.contains(s) } }
}

private final class Once: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func first() -> Bool { lock.withLock { defer { done = true }; return !done } }
}

private func until(_ seconds: TimeInterval = 10, _ done: () -> Bool) async throws {
    let end = Date() + seconds
    while !done() {
        if Date() > end {
            Issue.record("timed out")
            return
        }
        try await Task.sleep(for: .milliseconds(20))
    }
}

/// A WebSocket server on the loopback, answering as a device whose disks
/// are gone - or, once `disksBack`, as one that signs in.
private final class StorageStandIn: @unchecked Sendable {
    static let words = "Your device can't reach its storage disks (test)."
    private let listener: NWListener
    private let queue = DispatchQueue(label: "storage-stand-in")
    private let lock = NSLock()
    private var _keyAsks = 0
    private var _auths = 0
    private var _disksBack = false
    private let publicKey: Data
    var keyAsks: Int { lock.withLock { _keyAsks } }
    var auths: Int { lock.withLock { _auths } }
    var disksBack: Bool {
        get { lock.withLock { _disksBack } }
        set { lock.withLock { _disksBack = newValue } }
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
        // SEQUENCE { AlgorithmIdentifier rsaEncryption NULL, BIT STRING { PKCS#1 } }
        let header: [UInt8] = [0x30, 0x82, 0x01, 0x22, 0x30, 0x0d, 0x06, 0x09, 0x2a, 0x86, 0x48, 0x86,
                               0xf7, 0x0d, 0x01, 0x01, 0x01, 0x05, 0x00, 0x03, 0x82, 0x01, 0x0f, 0x00]
        return Data(header) + pkcs1
    }

    func start() async throws -> URL {
        let port: UInt16 = try await withCheckedThrowingContinuation { cont in
            let once = Once()
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
        let back = disksBack
        switch req.payload {
        case .reqGetPubKey:
            lock.withLock { _keyAsks += 1 }
            if back {
                var pk = Msg_PubKey()
                pk.publicKey = publicKey
                resp.payload = .respPubKey(pk)
            }
        case .reqAuth:
            if back {
                lock.withLock { _auths += 1 }
                var ack = Ack()
                ack.ok = true
                resp.payload = .respAck(ack)
            }
        default:
            if back {
                // GetLocalEndpoint: a device before #190.
                resp.error = true
                resp.errorCode = "unknown_payload"
            }
        }
        if !back {
            resp.error = true
            resp.errorCode = "storage_unavailable"
            resp.errorMessage = Self.words
        }
        guard let data = try? resp.serializedData() else { return }
        let meta = NWProtocolWebSocket.Metadata(opcode: .binary)
        let ctx = NWConnection.ContentContext(identifier: "answer", metadata: [meta])
        conn.send(content: data, contentContext: ctx, isComplete: true, completion: .contentProcessed { _ in })
    }
}
