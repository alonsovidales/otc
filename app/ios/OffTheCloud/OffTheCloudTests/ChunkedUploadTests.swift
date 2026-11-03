// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import CryptoKit
import Testing
@testable import OffTheCloud

/// Issue #205 against a stand-in device: BeginUpload names the content, an
/// upload starts at what the device already holds, one cut off mid-file is
/// begun again with the same key and goes on from there - never reading the
/// bytes before it out of the file - and a device before release 166, which
/// answers 0 and forgot the upload with its connection, gets it whole
/// again. Android's ChunkedUploadTest has the same cases.
struct ChunkedUploadTests {
    private let chunk = OTCConnection.uploadChunkSize

    /// `n` bytes of content that differ from chunk to chunk.
    private func content(_ n: Int) -> Data {
        Data((0..<n).map { UInt8(truncatingIfNeeded: $0 &* 31 &+ $0 / 7) })
    }

    private func sha(_ d: Data) -> String {
        SHA256.hash(data: d).map { String(format: "%02x", $0) }.joined()
    }

    private struct Dropped: Error {}
    private struct Offline: Error {}

    /// The device's side of BeginUpload/UploadChunk/FinishUpload.
    /// `resumes`: release 166, which keeps an unfinished upload across
    /// connections and answers BeginUpload of the same path, size and sha256
    /// with what it holds; otherwise an older device, which drops it with
    /// its connection and always answers 0. `dropAfter`: the connection
    /// goes once the device has taken that chunk (its answer lost), each
    /// count once.
    private final class Device: @unchecked Sendable {
        final class Upload {
            let id: String
            let key: String
            let size: Int64
            var data = Data()
            init(id: String, key: String, size: Int64) {
                self.id = id
                self.key = key
                self.size = size
            }
        }

        let resumes: Bool
        private let lock = NSLock()
        private var dropAfter: Set<Int>
        private var uploads: [String: Upload] = [:]
        private var nextID = 0
        private(set) var begins: [Msg_BeginUpload] = []
        private(set) var offsets: [Int64] = []
        private(set) var stored: [String: Data] = [:]
        private var chunksTaken = 0
        /// What the next UploadChunk is answered with instead, once.
        var chunkAnswer: Msg_RespEnvelope?
        /// BeginUploads that fail on the way, from the second on.
        var beginDrops = 0

        init(resumes: Bool = true, dropAfter: Set<Int> = []) {
            self.resumes = resumes
            self.dropAfter = dropAfter
        }

        /// An unfinished upload this device already holds, as another
        /// connection left it.
        func holds(path: String, size: Int64, sha256: String, bytes: Data) {
            lock.withLock {
                let key = "\(path)|\(size)|\(sha256)"
                let up = Upload(id: "u\(nextID)", key: key, size: size)
                nextID += 1
                up.data = bytes
                uploads[key] = up
            }
        }

        var request: OTCConnection.UploadRequest {
            { [self] payload in try lock.withLock { try answer(payload) } }
        }

        private func error(_ message: String, code: String = "") -> Msg_RespEnvelope {
            var r = Msg_RespEnvelope()
            r.error = true
            r.errorMessage = message
            r.errorCode = code
            return r
        }

        private func answer(_ payload: Msg_ReqEnvelope.OneOf_Payload) throws -> Msg_RespEnvelope {
            var out = Msg_RespEnvelope()
            switch payload {
            case .reqBeginUpload(let b):
                begins.append(b)
                if begins.count > 1 && beginDrops > 0 {
                    beginDrops -= 1
                    throw Offline()
                }
                let key = "\(b.path)|\(b.size)|\(b.sha256)"
                let up: Upload
                if resumes, !b.sha256.isEmpty, let held = uploads[key] {
                    up = held
                } else {
                    up = Upload(id: "u\(nextID)", key: key, size: b.size)
                    nextID += 1
                    uploads[key] = up
                }
                var st = Msg_UploadStarted()
                st.uploadID = up.id
                st.received = Int64(up.data.count)
                out.payload = .respUploadStarted(st)
            case .reqUploadChunk(let c):
                offsets.append(c.offset)
                if let a = chunkAnswer {
                    chunkAnswer = nil
                    return a
                }
                guard let up = uploads.values.first(where: { $0.id == c.uploadID }) else { return error("unknown upload") }
                guard c.offset == Int64(up.data.count) else { return error("out of order") }
                up.data.append(c.data)
                chunksTaken += 1
                if dropAfter.remove(chunksTaken) != nil {
                    // A device before 166 aborts what the connection had going.
                    if !resumes { uploads = [:] }
                    throw Dropped()
                }
                var p = Msg_UploadProgress()
                p.received = Int64(up.data.count)
                out.payload = .respUploadProgress(p)
            case .reqFinishUpload(let f):
                guard let up = uploads.values.first(where: { $0.id == f.uploadID }) else { return error("unknown upload") }
                let hash = SHA256.hash(data: up.data).map { String(format: "%02x", $0) }.joined()
                guard hash == f.sha256, Int64(up.data.count) == up.size else { return error("content doesn't match its SHA-256") }
                uploads[up.key] = nil
                let path = String(up.key.split(separator: "|")[0])
                stored[path] = up.data
                var file = Msg_File()
                file.path = path
                file.hash = hash
                out.payload = .respFile(file)
            default:
                return error("unknown payload")
            }
            return out
        }
    }

    /// What one upload saw: the pauses before its resumes and the bytes it
    /// reported.
    private final class Run: @unchecked Sendable {
        private let lock = NSLock()
        private var _pauses: [Int] = []
        private var _sent: [Int64] = []
        /// What the caller answers when asked whether a cut goes on.
        let mayResume: Bool
        init(mayResume: Bool = true) { self.mayResume = mayResume }
        var pauses: [Int] { lock.withLock { _pauses } }
        var sent: [Int64] { lock.withLock { _sent } }
        func paused(_ n: Int) { lock.withLock { _pauses.append(n) } }
        func reported(_ n: Int64) { lock.withLock { _sent.append(n) } }
    }

    private let path = "/ios/x/v.mov"

    @discardableResult
    private func upload(_ device: Device, _ source: OTCConnection.UploadSource, sha256: String, run: Run = Run()) async throws -> Msg_RespEnvelope {
        try await OTCConnection.sendChunked(
            path: path, source: source, forceOverride: false, created: nil, modified: nil, cloudID: "",
            sha256: sha256, progress: { run.reported($0) }, request: device.request,
            pause: { run.paused($0) }, mayResume: { run.mayResume }, log: { _ in })
    }

    /// The bytes as a file on disk, read through FileHandle as the sync's
    /// bigger sources are.
    private func file(_ bytes: Data) throws -> URL {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("chunked-\(UUID().uuidString).bin")
        try bytes.write(to: url)
        return url
    }

    @Test func beginUploadNamesTheContent() async throws {
        let bytes = content(chunk + 10)
        let device = Device()
        let resp = try await upload(device, .data(bytes), sha256: sha(bytes))
        guard case .respFile = resp.payload else { Issue.record("expected a File"); return }
        #expect(device.begins.count == 1)
        #expect(device.begins.first?.sha256 == sha(bytes))
        #expect(device.begins.first?.size == Int64(bytes.count))
        #expect(device.begins.first?.path == path)
        #expect(device.stored[path] == bytes)
        #expect(device.offsets == [0, Int64(chunk)])
    }

    /// The device already holds part of it (a run cut short before): the
    /// chunks start there, from a file whose start is never read.
    @Test func receivedAboveZeroGoesOnFromThere() async throws {
        let bytes = content(3 * chunk + 3)
        let held = chunk + 5
        let device = Device()
        device.holds(path: path, size: Int64(bytes.count), sha256: sha(bytes), bytes: bytes.prefix(held))
        // The file's start is zeros here: a read of it would send them, and
        // the device would refuse the whole at FinishUpload.
        var onDisk = bytes
        onDisk.replaceSubrange(0..<held, with: Data(count: held))
        let url = try file(onDisk)
        defer { try? FileManager.default.removeItem(at: url) }
        let run = Run()
        try await upload(device, .file(url), sha256: sha(bytes), run: run)
        #expect(device.stored[path] == bytes)
        #expect(device.offsets == [Int64(held), Int64(held + chunk)])
        #expect(run.sent.first == Int64(held))
        #expect(run.sent.last == Int64(bytes.count))
        #expect(run.pauses.isEmpty)
    }

    /// All of it there already (FinishUpload's answer lost last time):
    /// straight to FinishUpload.
    @Test func everythingReceivedGoesStraightToFinish() async throws {
        let bytes = content(chunk + 1)
        let device = Device()
        device.holds(path: path, size: Int64(bytes.count), sha256: sha(bytes), bytes: bytes)
        try await upload(device, .data(bytes), sha256: sha(bytes))
        #expect(device.offsets.isEmpty)
        #expect(device.stored[path] == bytes)
    }

    /// The connection goes after the second chunk reached the device: begun
    /// again with the same key, it goes on from the third.
    @Test func aDropMidFileResumesWhereTheDeviceIs() async throws {
        let bytes = content(3 * chunk + 100)
        let device = Device(dropAfter: [2])
        let url = try file(bytes)
        defer { try? FileManager.default.removeItem(at: url) }
        let run = Run()
        try await upload(device, .file(url), sha256: sha(bytes), run: run)
        #expect(device.stored[path] == bytes)
        #expect(device.begins.count == 2)
        #expect(device.begins.first == device.begins.last)
        // No chunk sent twice.
        #expect(device.offsets == [0, Int64(chunk), Int64(2 * chunk), Int64(3 * chunk)])
        #expect(run.pauses == [1])
    }

    /// An older device answers 0 and forgot the upload with its connection:
    /// the file goes again from the start.
    @Test func anOldDeviceAnsweringZeroStartsOver() async throws {
        let bytes = content(3 * chunk)
        let device = Device(resumes: false, dropAfter: [2])
        let url = try file(bytes)
        defer { try? FileManager.default.removeItem(at: url) }
        let run = Run()
        try await upload(device, .file(url), sha256: sha(bytes), run: run)
        #expect(device.stored[path] == bytes)
        #expect(device.offsets == [0, Int64(chunk), 0, Int64(chunk), Int64(2 * chunk)])
        #expect(run.pauses == [1])
    }

    /// Cuts that keep coming with nothing gained give up after maxResumes,
    /// with the last error, for the sync's retry list.
    @Test func cutsWithNothingGainedGiveUp() async throws {
        let bytes = content(2 * chunk)
        let device = Device(resumes: false, dropAfter: Set(1...8))
        let run = Run()
        await #expect(throws: Dropped.self) {
            try await upload(device, .data(bytes), sha256: sha(bytes), run: run)
        }
        #expect(device.begins.count == OTCConnection.maxResumes + 1)
        #expect(run.pauses == Array(1...OTCConnection.maxResumes))
        #expect(device.stored.isEmpty)
    }

    /// Each cut that came after the device got further is a new count: a
    /// long file over a flaky link gets there.
    @Test func cutsAfterProgressKeepGoing() async throws {
        let bytes = content(8 * chunk)
        let device = Device(dropAfter: Set(1...7))
        let run = Run()
        try await upload(device, .data(bytes), sha256: sha(bytes), run: run)
        #expect(device.stored[path] == bytes)
        #expect(device.begins.count == 8)
        #expect(run.pauses == Array(repeating: 1, count: 7))
        #expect(device.offsets == (0..<8).map { Int64($0 * chunk) })
    }

    /// No network yet when it is begun again: another try after a longer
    /// pause, the same key.
    @Test func aBeginThatFailsWhileResumingIsAnotherTry() async throws {
        let bytes = content(2 * chunk)
        let device = Device(dropAfter: [1])
        device.beginDrops = 2
        let run = Run()
        try await upload(device, .data(bytes), sha256: sha(bytes), run: run)
        #expect(device.stored[path] == bytes)
        #expect(device.begins.count == 4)
        #expect(run.pauses == [1, 2, 3])
    }

    /// The caller says not now after the pause (the sync's Wi-Fi only, the
    /// phone on mobile data): the cut is the caller's, not resumed - and
    /// the next run goes on from what the device kept.
    @Test func aCutTheCallerHoldsIsNotResumed() async throws {
        let bytes = content(2 * chunk)
        let device = Device(dropAfter: [1])
        let run = Run(mayResume: false)
        await #expect(throws: Dropped.self) {
            try await upload(device, .data(bytes), sha256: sha(bytes), run: run)
        }
        #expect(device.begins.count == 1)
        #expect(run.pauses == [1])
        let again = Run()
        try await upload(device, .data(bytes), sha256: sha(bytes), run: again)
        #expect(device.stored[path] == bytes)
        #expect(device.offsets == [0, Int64(chunk)])
        #expect(again.sent.first == Int64(chunk))
    }

    /// The device losing its place answers an error rather than a drop:
    /// resumed from what it says it holds.
    @Test func aRefusedChunkResumesFromTheDevice() async throws {
        let bytes = content(2 * chunk)
        let device = Device()
        var refused = Msg_RespEnvelope()
        refused.error = true
        refused.errorMessage = "unknown upload"
        device.chunkAnswer = refused
        let run = Run()
        try await upload(device, .data(bytes), sha256: sha(bytes), run: run)
        #expect(device.stored[path] == bytes)
        #expect(device.begins.count == 2)
        #expect(run.pauses == [1])
    }

    /// storage_full is the device's answer, not a cut: thrown at once with
    /// its code (the sync stops on it).
    @Test func storageFullIsNotResumed() async throws {
        let bytes = content(2 * chunk)
        let device = Device()
        var full = Msg_RespEnvelope()
        full.error = true
        full.errorCode = ErrorCodes.storageFull
        full.errorMessage = "full"
        device.chunkAnswer = full
        let run = Run()
        do {
            try await upload(device, .data(bytes), sha256: sha(bytes), run: run)
            Issue.record("expected storage_full")
        } catch {
            #expect(ErrorCodes.isStorageFull(code: ErrorCodes.code(of: error)))
        }
        #expect(device.begins.count == 1)
        #expect(run.pauses.isEmpty)
    }

    /// The first BeginUpload failing is the caller's, as before: no pause,
    /// no second try.
    @Test func theFirstBeginFailsAsBefore() async throws {
        let bytes = content(10)
        let run = Run()
        await #expect(throws: Offline.self) {
            try await OTCConnection.sendChunked(
                path: "/a", source: .data(bytes), forceOverride: false, created: nil, modified: nil, cloudID: "",
                sha256: sha(bytes), progress: nil, request: { _ in throw Offline() },
                pause: { run.paused($0) }, log: { _ in })
        }
        #expect(run.pauses.isEmpty)
    }

    /// FinishUpload still checks the whole content: one that doesn't match
    /// is refused, not resumed.
    @Test func finishStillChecksTheWholeContent() async throws {
        let bytes = content(chunk + 1)
        let device = Device()
        // What the device holds of it isn't this file's start.
        device.holds(path: path, size: Int64(bytes.count), sha256: sha(bytes), bytes: Data(count: chunk))
        do {
            try await upload(device, .data(bytes), sha256: sha(bytes))
            Issue.record("expected the hash check to refuse it")
        } catch {
            #expect((error as NSError).localizedDescription.contains("SHA-256"))
        }
        #expect(device.stored[path] == nil)
    }

    @Test func resumePausesDouble() {
        #expect((1...OTCConnection.maxResumes).map { OTCConnection.resumePause($0) } == [2, 4, 8, 16])
    }
}
