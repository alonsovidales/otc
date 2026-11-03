// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import os
import SwiftProtobuf

private let uploadLog = Logger(subsystem: "cloud.off-the.OffTheCloud", category: "sync")

/// Issue #205: one upload's account of its resumes. As otc-sync's
/// uploadResume (engine/chunked.go).
struct UploadResume {
    /// Drops in a row after which the device holds no more of the file than
    /// before, and the upload gives up: a link that keeps dropping goes on
    /// for as long as each connection gets some of it through.
    static let maxStalled = 3

    /// The device has answered a BeginUpload.
    private(set) var begun = false
    /// The most of the file it has said it holds.
    private(set) var held: Int64 = 0
    /// Drops since it last held more.
    private(set) var stalled = 0

    /// Takes the device's answer to a BeginUpload of `size` bytes and says
    /// where to go on from.
    mutating func started(received: Int64, size: Int64) throws -> Int64 {
        guard received >= 0, received <= size else {
            throw ChunkedUpload.failure(5, "the device holds \(received) bytes of a \(size)-byte upload") // i18n-ignore: a file's error, only logged
        }
        begun = true
        if received > held {
            held = received
            stalled = 0
        }
        return received
    }

    /// Notes a dropped connection and says whether to resume: not before
    /// the device has begun the upload (a request that found no connection
    /// is the pass's to retry, as before), nor after `maxStalled` drops in
    /// a row that brought nothing.
    mutating func dropped() -> Bool {
        guard begun else { return false }
        stalled += 1
        return stalled <= Self.maxStalled
    }
}

/// The bytes of an upload, in chunks (issue #168): BeginUpload,
/// UploadChunk in order, FinishUpload with the hash - so neither the Mac
/// nor the device holds the whole file in memory, and no single websocket
/// message carries it. The device checks the hash before the content
/// becomes the file.
///
/// Issue #205: an upload cut by a dropped connection goes on where the
/// device's copy of it ends, instead of from byte 0 at the next pass.
/// BeginUpload names the content - path, size and SHA-256 are the resume
/// key - and the device answers how much of it it already holds
/// (`UploadStarted.received`: 0 for a new upload, and always from a device
/// before release 166, which drops an upload with its connection). Once
/// the client has signed in again (it reconnects by itself, 1 s doubling up
/// to 30 s), the same BeginUpload goes again and the file is read on from
/// there: the part the device holds is never read again to be sent.
/// FinishUpload still checks the whole content's hash. A file that changed
/// meanwhile is a busy file; with another hash it is another key, and its
/// next upload starts fresh. A drop during FinishUpload isn't resumed: the
/// device may have made the file already, and the next pass's HasFile, or
/// its BeginUpload, finds out without sending it again. As otc-sync's
/// uploadChunked (engine/chunked.go).
enum ChunkedUpload {
    /// How long an upload cut by a drop waits for the client to sign in
    /// again before it gives up (the pass's own retry, and the device
    /// keeping what it has for 30 idle minutes, take over then).
    static let resumeWait: TimeInterval = 120
    static let signInPoll: Duration = .milliseconds(250)

    /// The file is no longer the one measured: it shrank while it was
    /// read, or changed while it was sent or while the link was down - a
    /// busy file.
    struct FileChanged: Error, CustomStringConvertible {
        var description: String { "changed while it was being sent" } // i18n-ignore: a file's error, only logged
    }

    /// Sends `url`'s content, `size` bytes last modified at `modified` (as
    /// it was when hashed), to `remotePath`. `stillWanted` says whether to
    /// go on waiting for the connection after a drop (the same device, the
    /// app not leaving it); `beforeFinish` runs right before FinishUpload,
    /// and an error from it leaves the device's file as it is.
    static func send(ws: WSClient, file url: URL, to remotePath: String, hash: String,
                     size: Int64, modified: Date?,
                     createdStamp: Google_Protobuf_Timestamp, modifiedStamp: Google_Protobuf_Timestamp,
                     chunkSize: Int, smallRequest: TimeInterval, transferRequest: TimeInterval,
                     resumeWait: TimeInterval = ChunkedUpload.resumeWait,
                     stillWanted: () -> Bool,
                     beforeFinish: () async throws -> Void) async throws {
        let fh = try FileHandle(forReadingFrom: url)
        defer { try? fh.close() }
        var resume = UploadResume()
        var uploadID = ""
        while true {
            let signIn = ws.signInCount()
            do {
                uploadID = try await sendFrom(fh, ws: ws, to: remotePath, hash: hash, size: size,
                                              createdStamp: createdStamp, modifiedStamp: modifiedStamp,
                                              chunkSize: chunkSize, smallRequest: smallRequest,
                                              transferRequest: transferRequest, resume: &resume, name: url.lastPathComponent)
                break
            } catch {
                guard WSClient.isDropped(error), resume.dropped() else { throw error }
                uploadLog.notice("\(url.lastPathComponent, privacy: .public): the connection dropped during its upload - going on once it is back") // i18n-ignore: a log line
                guard await awaitSignIn(ws, after: signIn, within: resumeWait, stillWanted: stillWanted) else { throw error }
                // Changed meanwhile: not the content the key names any more.
                guard unchanged(url, size: size, modified: modified) else { throw FileChanged() }
            }
        }
        guard unchanged(url, size: size, modified: modified) else { throw FileChanged() }
        try await beforeFinish()
        let resp = try await ws.request(timeout: transferRequest) { req in
            var fu = Msg_FinishUpload()
            fu.uploadID = uploadID
            fu.sha256 = hash
            req.payload = .reqFinishUpload(fu)
        }
        try throwIfRejected(resp)
    }

    /// Begins the upload - or resumes it, when the device holds some of
    /// this content already - and sends the rest of the file from where the
    /// device's copy ends, read from the disk from there on. Returns the
    /// upload's id once the device holds all of it.
    private static func sendFrom(_ fh: FileHandle, ws: WSClient, to remotePath: String, hash: String, size: Int64,
                                 createdStamp: Google_Protobuf_Timestamp, modifiedStamp: Google_Protobuf_Timestamp,
                                 chunkSize: Int, smallRequest: TimeInterval, transferRequest: TimeInterval,
                                 resume: inout UploadResume, name: String) async throws -> String {
        let begin = try await ws.request(timeout: smallRequest) { req in
            var bu = Msg_BeginUpload()
            bu.path = remotePath
            bu.size = size
            bu.created = createdStamp
            bu.modified = modifiedStamp
            bu.forceOverride = true
            bu.cloudID = ""
            bu.sha256 = hash
            req.payload = .reqBeginUpload(bu)
        }
        // A server-side rejection (auth failure, disk full, etc.) still
        // comes back as a normal response, not a thrown error from
        // `request` — checking resp.error is the only way to actually
        // notice the file wasn't saved, instead of silently caching it as
        // synced and never trying again.
        try throwIfRejected(begin)
        guard case .respUploadStarted(let started) = begin.payload else {
            throw failure(2, "unexpected response") // i18n-ignore: a file's error, only logged
        }
        let uploadID = started.uploadID
        var offset = try resume.started(received: started.received, size: size)
        if offset > 0 {
            uploadLog.notice("\(name, privacy: .public): the device already holds \(offset) of \(size) bytes - going on from there") // i18n-ignore: a log line
        }
        // Reading happens off the main actor, one chunk at a time - the
        // same UI-freezing concern as the hashing in reconcile().
        let from = UInt64(offset)
        try await Task.detached(priority: .utility) { try fh.seek(toOffset: from) }.value
        while offset < size {
            let data = try await Task.detached(priority: .utility) {
                try fh.read(upToCount: chunkSize) ?? Data()
            }.value
            // The file shrank since it was measured: what the device
            // would get no longer matches the hash - a busy file.
            if data.isEmpty { throw FileChanged() }
            let chunkOffset = offset
            let resp = try await ws.request(timeout: transferRequest) { req in
                var uc = Msg_UploadChunk()
                uc.uploadID = uploadID
                uc.offset = chunkOffset
                uc.data = data
                req.payload = .reqUploadChunk(uc)
            }
            // The device takes chunks strictly in order; an "out_of_order"
            // answer (or any other rejection) ends this attempt and the
            // next pass resumes it from what the device holds.
            try throwIfRejected(resp)
            offset += Int64(data.count)
            if case .respUploadProgress(let p) = resp.payload, p.received != offset {
                throw failure(4, "the device received \(p.received) bytes, expected \(offset)") // i18n-ignore: a file's error, only logged
            }
        }
        return uploadID
    }

    /// Waits, `within` at most, for `ws` to sign in again after its sign-in
    /// number `after` (signInCount). False when it doesn't, or when
    /// `stillWanted` says no more of the file should go there.
    static func awaitSignIn(_ ws: WSClient, after: UInt64, within: TimeInterval, stillWanted: () -> Bool) async -> Bool {
        let end = Date().addingTimeInterval(within)
        while true {
            guard stillWanted(), !Task.isCancelled else { return false }
            if ws.isConnected(), ws.signInCount() != after { return true }
            if Date() >= end { return false }
            try? await Task.sleep(for: signInPoll)
        }
    }

    /// The file still has the size and modification date it was measured
    /// with.
    static func unchanged(_ url: URL, size: Int64, modified: Date?) -> Bool {
        guard let attrs = try? FileManager.default.attributesOfItem(atPath: url.path) else { return false }
        return (attrs[.size] as? NSNumber)?.int64Value == size && (attrs[.modificationDate] as? Date) == modified
    }

    static func throwIfRejected(_ resp: Resp) throws {
        if resp.error {
            throw failure(1, resp.errorMessage.isEmpty ? "upload rejected" : resp.errorMessage) // i18n-ignore: a file's error, only logged
        }
    }

    /// An upload's error as SyncModel has always thrown them: a folder's
    /// row shows its words.
    static func failure(_ code: Int, _ message: String) -> NSError {
        NSError(domain: "sync.upload", code: code, userInfo: [NSLocalizedDescriptionKey: message])
    }
}
