
// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  ChunkedUpload.swift
//  OffTheCloud
//
//  Issue #165: a whole-file UploadFile made the device hold the entire file
//  - and a phone video can be several GB - in one websocket message. Every
//  upload now goes through the device's chunked upload instead:
//  BeginUpload (-> UploadStarted), then UploadChunk messages of
//  `chunkSize` bytes in order (each -> UploadProgress), then FinishUpload
//  with the content's SHA-256, which the device checks before making it the
//  file (-> File, the same answer UploadFile gave). This file is the one
//  place that speaks that protocol; every call site that used to send
//  .reqUploadFile calls uploadChunked(...) here.
//
//  Issue #205: BeginUpload names the content (sha256: with the path and
//  size, the resume key), and the device answers with what it already
//  holds of an unfinished upload of that key (UploadStarted.received, 0 for
//  a new one and from every device before release 166). The chunks start
//  there: the bytes before it are skipped (a seek, a slice), never read out
//  to be sent again. A connection that drops mid-file (the phone moving
//  between Wi-Fi and mobile data, a bridge node restarting) no longer
//  starts the file over: the upload is begun again with the same key and
//  goes on from what the device has. As Android's ChunkedUpload.kt.

import Foundation
import CryptoKit
import SwiftProtobuf

extension OTCConnection {
    /// 4 MiB: small enough that neither side ever holds more than a few MB
    /// of one upload in a message, big enough that the per-chunk round trip
    /// doesn't dominate.
    nonisolated static let uploadChunkSize = 4 << 20

    /// Issue #205: how many times in a row an upload cut off mid-file is
    /// begun again without the device getting further than it ever had - an
    /// older device starting it over from 0 has to pass that point again
    /// before a cut counts as new - and the pause before each (2, 4, 8,
    /// 16 s: a phone that changed networks, or a bridge node restarting, is
    /// back within that). After that the error is the caller's, as before:
    /// the sync's retry list, where the next run resumes it.
    nonisolated static let maxResumes = 4

    nonisolated static func resumePause(_ attempt: Int) -> TimeInterval {
        TimeInterval(1 << min(max(attempt, 1), maxResumes))
    }

    /// Where the bytes come from: already in memory (sliced per chunk), or
    /// a file on disk (read per chunk with FileHandle, never loaded whole).
    enum UploadSource {
        case data(Data)
        case file(URL)
    }

    /// What an upload needs from the connection: OTCConnection's request in
    /// the app, a stand-in device in tests.
    typealias UploadRequest = @Sendable (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope

    /// Uploads `source` to `path` in chunks and returns FinishUpload's
    /// response (a .respFile on success), so callers handle it exactly as
    /// they handled UploadFile's. Any error answer along the way throws,
    /// with the device's message and error code (ErrorCodes.code(of:)) -
    /// the callers' own retry/skip handling covers it. `sha256` is the
    /// content's hex SHA-256 - every caller hashes first, for HasFile - and
    /// the resume key. `progress` gets the bytes of it the device holds as
    /// they grow (from where a resumed upload starts). `mayResume` is asked
    /// after a cut, once its pause is over: false leaves the cut to the
    /// caller (the photo sync's "Wi-Fi only" on mobile data - a phone that
    /// left its Wi-Fi mid-file doesn't finish it over mobile data).
    /// Nonisolated so the reading stays off the main actor; only the
    /// requests themselves hop onto it.
    nonisolated func uploadChunked(path: String,
                                   source: UploadSource,
                                   forceOverride: Bool,
                                   created: Google_Protobuf_Timestamp? = nil,
                                   modified: Google_Protobuf_Timestamp? = nil,
                                   cloudID: String = "",
                                   sha256: String,
                                   progress: (@Sendable (Int64) -> Void)? = nil,
                                   mayResume: (@Sendable () -> Bool)? = nil) async throws -> Msg_RespEnvelope {
        // Issue #190: no route switch while this runs.
        TransferActivity.shared.begin()
        defer { TransferActivity.shared.end() }
        return try await Self.sendChunked(
            path: path, source: source, forceOverride: forceOverride, created: created, modified: modified,
            cloudID: cloudID, sha256: sha256, progress: progress,
            request: { payload in try await self.request { $0.payload = payload } },
            pause: { attempt in try await Task.sleep(for: .seconds(Self.resumePause(attempt))) },
            mayResume: mayResume,
            log: { print("ChunkedUpload:", $0) })
    }

    /// uploadChunked over `request`: the protocol, resuming included.
    /// `pause` waits before resume `attempt`; `log` gets lines without paths.
    nonisolated static func sendChunked(path: String,
                                        source: UploadSource,
                                        forceOverride: Bool,
                                        created: Google_Protobuf_Timestamp?,
                                        modified: Google_Protobuf_Timestamp?,
                                        cloudID: String,
                                        sha256: String,
                                        progress: (@Sendable (Int64) -> Void)?,
                                        request: UploadRequest,
                                        pause: @Sendable (Int) async throws -> Void,
                                        mayResume: (@Sendable () -> Bool)? = nil,
                                        log: @Sendable (String) -> Void) async throws -> Msg_RespEnvelope {
        func fail(_ message: String, code: String = "") -> NSError {
            var info: [String: Any] = [NSLocalizedDescriptionKey: message]
            if !code.isEmpty { info[ErrorCodes.userInfoKey] = code }
            return NSError(domain: "ChunkedUpload", code: -1, userInfo: info)
        }
        func refusal(_ resp: Msg_RespEnvelope, _ what: String) -> NSError {
            fail(resp.errorMessage.isEmpty ? "\(what) failed" : resp.errorMessage, code: resp.errorCode) // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"
        }
        /// A failure as the log has it: the device's code, or the error's
        /// kind - never its words, which may name a host or a path.
        func what(_ error: Error) -> String {
            let code = ErrorCodes.code(of: error)
            return code.isEmpty ? String(describing: type(of: error)) : code
        }

        let size: Int64
        var fileHandle: FileHandle?
        switch source {
        case .data(let d):
            size = Int64(d.count)
        case .file(let url):
            let attrs = try FileManager.default.attributesOfItem(atPath: url.path)
            size = (attrs[.size] as? NSNumber)?.int64Value ?? 0
            fileHandle = try FileHandle(forReadingFrom: url)
        }
        defer { try? fileHandle?.close() }

        var begin = Msg_BeginUpload()
        begin.path = path
        begin.size = size
        if let created { begin.created = created }
        if let modified { begin.modified = modified }
        begin.forceOverride = forceOverride
        begin.cloudID = cloudID
        begin.sha256 = sha256

        // The most the device ever said it held, and the resumes since it
        // last grew.
        var furthest: Int64 = -1
        var resumes = 0
        var first = true

        // After a cut: the pause, then BeginUpload again - unless that was
        // the last try, or the caller says not now; then `cause` is its.
        func again(_ cause: Error, _ why: String) async throws {
            resumes += 1
            if resumes > maxResumes { throw cause }
            log("\(why) (\(what(cause))): resuming, try \(resumes)") // i18n-ignore: a log line
            try await pause(resumes)
            if let mayResume, !mayResume() { throw cause }
        }

        while true {
            // The first BeginUpload's failures are the caller's, as always.
            // After a cut, one that doesn't get through is another try - not
            // the device saying it is full.
            let started: Msg_RespEnvelope
            do {
                started = try await request(.reqBeginUpload(begin))
            } catch {
                if first || Task.isCancelled || error is CancellationError { throw error }
                try await again(error, "BeginUpload failed") // i18n-ignore: a log line
                continue
            }
            guard !started.error, case .respUploadStarted(let st) = started.payload else {
                let error = refusal(started, "BeginUpload")
                if first || ErrorCodes.isStorageFull(code: started.errorCode) { throw error }
                try await again(error, "BeginUpload refused") // i18n-ignore: a log line
                continue
            }
            first = false
            let uploadID = st.uploadID
            var offset = st.received
            guard offset >= 0, offset <= size else {
                throw fail("Device has \(offset) bytes of \(size)") // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"
            }
            if offset > furthest {
                furthest = offset
                resumes = 0
            }
            if offset > 0 { log("resuming at \(offset) of \(size) bytes") } // i18n-ignore: a log line
            progress?(offset)
            // The bytes before it are skipped, never read.
            try fileHandle?.seek(toOffset: UInt64(offset))

            // Why the chunks stopped short: the connection, or the device
            // losing its place - both resumed from what it holds.
            var cut: Error?
            while offset < size {
                try Task.checkCancellation()
                let want = Int(min(Int64(uploadChunkSize), size - offset))
                let chunk: Data
                switch source {
                case .data(let d):
                    let start = d.startIndex + Int(offset)
                    chunk = Data(d[start ..< start + want])
                case .file:
                    chunk = try fileHandle?.read(upToCount: want) ?? Data()
                }
                if chunk.count < want { throw fail("File ended early at \(offset + Int64(chunk.count)) of \(size) bytes") } // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"

                var up = Msg_UploadChunk()
                up.uploadID = uploadID
                up.offset = offset
                up.data = chunk
                let resp: Msg_RespEnvelope
                do {
                    resp = try await request(.reqUploadChunk(up))
                } catch {
                    if Task.isCancelled || error is CancellationError { throw error }
                    cut = error
                    break
                }
                guard !resp.error, case .respUploadProgress(let p) = resp.payload else {
                    if ErrorCodes.isStorageFull(code: resp.errorCode) { throw refusal(resp, "UploadChunk") }
                    cut = refusal(resp, "UploadChunk")
                    break
                }
                let expected = offset + Int64(chunk.count)
                guard p.received == expected else {
                    cut = fail("Device received \(p.received) bytes, expected \(expected)") // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"
                    break
                }
                offset = expected
                if offset > furthest {
                    furthest = offset
                    resumes = 0
                }
                progress?(offset)
            }
            if let cut {
                try await again(cut, "upload cut off at \(offset) of \(size) bytes") // i18n-ignore: a log line
                continue
            }

            // Not resumed when it fails: the device may have made it the
            // file already. The caller's next try finds it by its hash, or
            // resumes.
            var finish = Msg_FinishUpload()
            finish.uploadID = uploadID
            finish.sha256 = sha256
            let done = try await request(.reqFinishUpload(finish))
            if done.error {
                throw refusal(done, "FinishUpload")
            }
            return done
        }
    }

    /// Hex SHA-256 of a file, read in uploadChunkSize pieces rather than
    /// loaded whole (issue #165).
    nonisolated static func sha256Hex(of url: URL) throws -> String {
        let fh = try FileHandle(forReadingFrom: url)
        defer { try? fh.close() }
        var hasher = SHA256()
        while let chunk = try fh.read(upToCount: uploadChunkSize), !chunk.isEmpty {
            hasher.update(data: chunk)
        }
        return hasher.finalize().map { String(format: "%02x", $0) }.joined()
    }
}
