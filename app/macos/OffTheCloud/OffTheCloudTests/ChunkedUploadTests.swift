// SPDX-License-Identifier: AGPL-3.0-or-later

import CryptoKit
import Foundation
import Network
import Security
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Issue #205: resumable uploads. Every BeginUpload names its content
/// (path, size and SHA-256), the upload goes on from what the device says
/// it holds, and one cut by a dropped connection goes on once the client
/// has signed in again - never sending the part the device holds; a file
/// changed meanwhile is a busy file. As otc-sync's engine/resume_test.go.
/// The app hosts the target's tests, so like WSClientStorageTests this runs
/// in a scratch SwiftPM package holding WSClient.swift, ChunkedUpload.swift
/// and what they need, with an in-memory Keychain stand-in.
@Suite(.serialized)
struct ChunkedUploadTests {
    static let chunk = 1024

    // MARK: The account of resumes (as TestUploadResumeAccount)

    @Test func resumesAreBoundedByDropsThatBringNothing() throws {
        var r = UploadResume()
        let early = r.dropped()
        #expect(!early, "resumed an upload the device never began")
        var refused = 0
        for received: Int64 in [-1, 11] {
            do { _ = try r.started(received: received, size: 10) } catch { refused += 1 }
        }
        #expect(refused == 2, "took a count that isn't of the file")
        let first = try r.started(received: 0, size: 10)
        #expect(first == 0)
        for _ in 0..<UploadResume.maxStalled {
            let goesOn = r.dropped()
            #expect(goesOn)
            let at = try r.started(received: 0, size: 10)
            #expect(at == 0)
        }
        let after = r.dropped()
        #expect(!after, "went on after drops that brought nothing")

        // Progress starts the count again: a link that keeps dropping goes
        // on while each connection gets something through.
        var p = UploadResume()
        _ = try p.started(received: 0, size: 100)
        for held in stride(from: Int64(10), through: 100, by: 10) {
            let goesOn = p.dropped()
            #expect(goesOn)
            let at = try p.started(received: held, size: 100)
            #expect(at == held)
        }
    }

    @Test func aDropIsTheConnectionNotAnAnswer() {
        #expect(WSClient.isDropped(RequestFailure.notConnected))
        #expect(WSClient.isDropped(RequestFailure.cancelled))
        #expect(WSClient.isDropped(NWError.posix(.ECONNRESET)))
        #expect(!WSClient.isDropped(RequestFailure.timedOut))
        #expect(!WSClient.isDropped(RequestFailure.closed))
        #expect(!WSClient.isDropped(NSError(domain: "sync.upload", code: 1)))
    }

    // MARK: Against a device

    @Test func everyBeginUploadNamesItsContentAndGoesOnFromWhatTheDeviceHolds() async throws {
        let (url, content, hash) = try Self.file(size: 2 * Self.chunk + 77)
        defer { try? FileManager.default.removeItem(at: url) }
        let device = try UploadStandIn()
        let held = Self.chunk + 10
        device.seed(path: "/mac/big.mov", size: Int64(content.count), sha: hash, bytes: content.prefix(held))
        let ws = try await device.connected()
        defer { ws.disconnect(); device.stop() }

        try await Self.send(ws, url, content, hash)

        #expect(device.begins.map(\.sha256) == [hash])
        #expect(device.begins.first?.size == Int64(content.count))
        #expect(device.answers == [Int64(held)])
        #expect(device.offsets == [Int64(held), Int64(held + Self.chunk)])
        #expect(device.stored["/mac/big.mov"] == content)
    }

    @Test func aDropMidFileResumesWithTheSameKeyAndSendsNothingTwice() async throws {
        let (url, content, hash) = try Self.file(size: 4 * Self.chunk + 5)
        defer { try? FileManager.default.removeItem(at: url) }
        let device = try UploadStandIn()
        device.dropAfterChunk = 2 // stored, its answer lost
        let ws = try await device.connected()
        defer { ws.disconnect(); device.stop() }
        let signIn = ws.signInCount()

        try await Self.send(ws, url, content, hash)

        #expect(ws.signInCount() != signIn, "the connection never dropped")
        let begins = device.begins
        #expect(begins.count == 2)
        #expect(begins.map(\.sha256) == [hash, hash])
        #expect(begins.map(\.path) == ["/mac/big.mov", "/mac/big.mov"])
        #expect(device.answers == [0, Int64(2 * Self.chunk)])
        #expect(device.offsets == (0...4).map { Int64($0 * Self.chunk) })
        #expect(device.stored["/mac/big.mov"] == content)
    }

    @Test func aDeviceThatHoldsNothingAfterTheDropStartsAgainFromZero() async throws {
        let (url, content, hash) = try Self.file(size: 3 * Self.chunk + 1)
        defer { try? FileManager.default.removeItem(at: url) }
        let device = try UploadStandIn()
        device.forgetful = true // a device before release 166
        device.dropAfterChunk = 2
        let ws = try await device.connected()
        defer { ws.disconnect(); device.stop() }

        try await Self.send(ws, url, content, hash)

        #expect(device.answers == [0, 0])
        #expect(device.offsets == [0, 1, 0, 1, 2, 3].map { Int64($0 * Self.chunk) })
        #expect(device.stored["/mac/big.mov"] == content)
    }

    @Test func aFileChangedWhileTheLinkWasDownIsBusyAndItsNextUploadStartsFresh() async throws {
        let (url, content, hash) = try Self.file(size: 3 * Self.chunk)
        defer { try? FileManager.default.removeItem(at: url) }
        var changed = Data(count: 3 * Self.chunk + 10)
        changed.withUnsafeMutableBytes { _ = SecRandomCopyBytes(kSecRandomDefault, $0.count, $0.baseAddress!) }
        let device = try UploadStandIn()
        device.dropAfterChunk = 1
        device.onDrop = {
            try? changed.write(to: url)
            try? FileManager.default.setAttributes([.modificationDate: Date().addingTimeInterval(60)], ofItemAtPath: url.path)
        }
        let ws = try await device.connected()
        defer { ws.disconnect(); device.stop() }

        await #expect(throws: ChunkedUpload.FileChanged.self) {
            try await Self.send(ws, url, content, hash)
        }
        #expect(device.begins.count == 1, "the old content was resumed")

        // The next pass hashes it again: another key, a fresh upload.
        let newHash = SHA256.hash(data: changed).map { String(format: "%02x", $0) }.joined()
        try await Self.send(ws, url, changed, newHash)
        #expect(device.begins.map(\.sha256) == [hash, newHash])
        #expect(device.answers == [0, 0])
        #expect(device.offsets.dropFirst().first == 0)
        #expect(device.stored["/mac/big.mov"] == changed)
    }

    @Test func dropsThatBringNothingGiveUp() async throws {
        let (url, content, hash) = try Self.file(size: 2 * Self.chunk)
        defer { try? FileManager.default.removeItem(at: url) }
        let device = try UploadStandIn()
        device.dropEveryChunk = true
        let ws = try await device.connected()
        defer { ws.disconnect(); device.stop() }

        do {
            try await Self.send(ws, url, content, hash)
            Issue.record("finished over a link that drops every piece")
        } catch {
            #expect(WSClient.isDropped(error))
        }
        #expect(device.begins.count == 1 + UploadResume.maxStalled)
    }

    @Test func aRequestThatFoundNoConnectionBeforeTheUploadBeganIsNotResumed() async throws {
        let (url, content, hash) = try Self.file(size: Self.chunk)
        defer { try? FileManager.default.removeItem(at: url) }
        let device = try UploadStandIn()
        device.dropBegin = true
        let ws = try await device.connected()
        defer { ws.disconnect(); device.stop() }

        let start = Date()
        do {
            try await Self.send(ws, url, content, hash)
            Issue.record("finished without a BeginUpload")
        } catch {
            #expect(WSClient.isDropped(error))
        }
        #expect(Date().timeIntervalSince(start) < 5, "it waited for the connection")
        #expect(device.begins.count == 1)
    }

    // MARK: Helpers

    static func file(size: Int) throws -> (URL, Data, String) {
        var content = Data(count: size)
        content.withUnsafeMutableBytes { _ = SecRandomCopyBytes(kSecRandomDefault, $0.count, $0.baseAddress!) }
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("otc-upload-\(UUID().uuidString).mov")
        try content.write(to: url)
        let hash = SHA256.hash(data: content).map { String(format: "%02x", $0) }.joined()
        return (url, content, hash)
    }

    static func send(_ ws: WSClient, _ url: URL, _ content: Data, _ hash: String) async throws {
        let attrs = try FileManager.default.attributesOfItem(atPath: url.path)
        try await ChunkedUpload.send(ws: ws, file: url, to: "/mac/big.mov", hash: hash,
                                     size: Int64(content.count), modified: attrs[.modificationDate] as? Date,
                                     createdStamp: Google_Protobuf_Timestamp(date: Date()),
                                     modifiedStamp: Google_Protobuf_Timestamp(date: Date()),
                                     chunkSize: chunk, smallRequest: 10, transferRequest: 10, resumeWait: 15,
                                     stillWanted: { true }, beforeFinish: {})
    }
}

private final class Once: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func first() -> Bool { lock.withLock { defer { done = true }; return !done } }
}

/// A WebSocket server on the loopback answering as a device does to
/// chunked uploads: in order, checked against the hash at the end, and
/// (unless `forgetful`) kept across connections by their resume key.
private final class UploadStandIn: @unchecked Sendable {
    private struct Key: Hashable { let path: String; let size: Int64; let sha: String }
    private let listener: NWListener
    private let queue = DispatchQueue(label: "upload-stand-in")
    private let lock = NSLock()
    private let publicKey: Data
    private var resumable: [Key: String] = [:]
    private var pending: [String: Data] = [:]
    private var paths: [String: String] = [:]
    private var _begins: [Msg_BeginUpload] = []
    private var _answers: [Int64] = []
    private var _offsets: [Int64] = []
    private var _stored: [String: Data] = [:]
    private var chunksHandled = 0
    private var dropped = false

    var forgetful = false
    var dropAfterChunk: Int?
    var dropEveryChunk = false
    var dropBegin = false
    var onDrop: (() -> Void)?

    var begins: [Msg_BeginUpload] { lock.withLock { _begins } }
    var answers: [Int64] { lock.withLock { _answers } }
    var offsets: [Int64] { lock.withLock { _offsets } }
    var stored: [String: Data] { lock.withLock { _stored } }

    init() throws {
        let ws = NWProtocolWebSocket.Options()
        ws.autoReplyPing = true
        ws.maximumMessageSize = 64 << 20
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

    func seed(path: String, size: Int64, sha: String, bytes: Data) {
        lock.withLock {
            pending["u0"] = Data(bytes)
            paths["u0"] = path
            resumable[Key(path: path, size: size, sha: sha)] = "u0"
        }
    }

    /// A client signed in to it.
    func connected() async throws -> WSClient {
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
        let ws = WSClient()
        ws.configure(domain: "ws://127.0.0.1:\(port)/ws", key: "secret")
        ws.connect()
        let end = Date() + 10
        while !ws.isConnected() {
            if Date() > end { throw URLError(.timedOut) }
            try await Task.sleep(for: .milliseconds(20))
        }
        return ws
    }

    func stop() { listener.cancel() }

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

    private func receive(on conn: NWConnection) {
        conn.receiveMessage { [weak self] data, context, _, error in
            guard let self else { return }
            let meta = context?.protocolMetadata(definition: NWProtocolWebSocket.definition) as? NWProtocolWebSocket.Metadata
            if let data, meta?.opcode == .binary, let req = try? Req(serializedBytes: data) {
                guard let resp = self.answer(req) else {
                    conn.cancel() // dropped, unanswered
                    return
                }
                guard let bytes = try? resp.serializedData() else { return }
                let ctx = NWConnection.ContentContext(identifier: "answer", metadata: [NWProtocolWebSocket.Metadata(opcode: .binary)])
                conn.send(content: bytes, contentContext: ctx, isComplete: true, completion: .contentProcessed { _ in })
            }
            if error == nil { self.receive(on: conn) }
        }
    }

    /// The answer to req, nil to drop the connection instead.
    private func answer(_ req: Req) -> Resp? {
        var resp = Resp()
        resp.id = req.id
        var drop = false
        lock.withLock {
            switch req.payload {
            case .reqGetPubKey:
                var pk = Msg_PubKey()
                pk.publicKey = publicKey
                resp.payload = .respPubKey(pk)
            case .reqAuth:
                var ack = Ack()
                ack.ok = true
                resp.payload = .respAck(ack)
            case .reqBeginUpload(let bu):
                _begins.append(bu)
                if dropBegin { drop = true; return }
                let key = Key(path: bu.path, size: bu.size, sha: bu.sha256)
                var started = Msg_UploadStarted()
                if !forgetful, !bu.sha256.isEmpty, let id = resumable[key] {
                    started.uploadID = id
                    started.received = Int64(pending[id]?.count ?? 0)
                } else {
                    let id = "u\(pending.count + 1)"
                    pending[id] = Data()
                    paths[id] = bu.path
                    resumable[key] = id
                    started.uploadID = id
                }
                _answers.append(started.received)
                resp.payload = .respUploadStarted(started)
            case .reqUploadChunk(let uc):
                if dropEveryChunk { drop = true; return }
                _offsets.append(uc.offset)
                guard let held = pending[uc.uploadID], Int64(held.count) == uc.offset else {
                    resp.error = true
                    resp.errorMessage = "out of order"
                    return
                }
                pending[uc.uploadID] = held + uc.data
                chunksHandled += 1
                var p = Msg_UploadProgress()
                p.received = Int64(held.count + uc.data.count)
                resp.payload = .respUploadProgress(p)
                if chunksHandled == dropAfterChunk, !dropped {
                    dropped = true
                    drop = true
                }
            case .reqFinishUpload(let fu):
                let data = pending[fu.uploadID] ?? Data()
                let sum = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
                guard sum == fu.sha256 else {
                    resp.error = true
                    resp.errorMessage = "hash mismatch"
                    return
                }
                resumable = resumable.filter { $0.value != fu.uploadID }
                _stored[paths[fu.uploadID] ?? ""] = data
                var f = Msg_File()
                f.path = paths[fu.uploadID] ?? ""
                resp.payload = .respFile(f)
            default:
                // GetLocalEndpoint: a device before #190.
                resp.error = true
                resp.errorCode = "unknown_payload"
            }
        }
        if drop {
            onDrop?()
            return nil
        }
        return resp
    }
}
