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

import Foundation
import CryptoKit
import SwiftProtobuf

extension OTCConnection {
    /// 4 MiB: small enough that neither side ever holds more than a few MB
    /// of one upload in a message, big enough that the per-chunk round trip
    /// doesn't dominate.
    nonisolated static let uploadChunkSize = 4 << 20

    /// Where the bytes come from: already in memory (sliced per chunk), or
    /// a file on disk (read per chunk with FileHandle, never loaded whole).
    enum UploadSource {
        case data(Data)
        case file(URL)
    }

    /// Uploads `source` to `path` in chunks and returns FinishUpload's
    /// response (a .respFile on success), so callers handle it exactly as
    /// they handled UploadFile's. Any error answer along the way throws,
    /// with the device's message - the callers' own retry/skip handling
    /// covers it. `sha256` is the content's hex SHA-256 when the caller
    /// already computed it (e.g. for HasFile); otherwise it is computed
    /// while sending. Nonisolated so the reading and hashing stay off the
    /// main actor; only the requests themselves hop onto it.
    nonisolated func uploadChunked(path: String,
                                   source: UploadSource,
                                   forceOverride: Bool,
                                   created: Google_Protobuf_Timestamp? = nil,
                                   modified: Google_Protobuf_Timestamp? = nil,
                                   cloudID: String = "",
                                   sha256: String? = nil) async throws -> Msg_RespEnvelope {
        func fail(_ message: String) -> NSError {
            NSError(domain: "ChunkedUpload", code: -1,
                    userInfo: [NSLocalizedDescriptionKey: message])
        }
        // Issue #190: no route switch while this runs.
        TransferActivity.shared.begin()
        defer { TransferActivity.shared.end() }

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
        let started = try await request { $0.payload = .reqBeginUpload(begin) }
        guard !started.error, case .respUploadStarted(let st) = started.payload else {
            throw fail(started.errorMessage.isEmpty ? "BeginUpload failed" : started.errorMessage)
        }
        let uploadID = st.uploadID

        var hasher = sha256 == nil ? SHA256() : nil
        var offset: Int64 = 0
        while offset < size {
            try Task.checkCancellation()
            let want = Int(min(Int64(Self.uploadChunkSize), size - offset))
            let chunk: Data
            switch source {
            case .data(let d):
                let start = d.startIndex + Int(offset)
                chunk = Data(d[start ..< start + want])
            case .file:
                chunk = try fileHandle?.read(upToCount: want) ?? Data()
            }
            if chunk.isEmpty { throw fail("File ended early at \(offset) of \(size) bytes") }
            hasher?.update(data: chunk)

            var up = Msg_UploadChunk()
            up.uploadID = uploadID
            up.offset = offset
            up.data = chunk
            let resp = try await request { $0.payload = .reqUploadChunk(up) }
            guard !resp.error, case .respUploadProgress(let p) = resp.payload else {
                throw fail(resp.errorMessage.isEmpty ? "UploadChunk failed" : resp.errorMessage)
            }
            let expected = offset + Int64(chunk.count)
            guard p.received == expected else {
                throw fail("Device received \(p.received) bytes, expected \(expected)")
            }
            offset = expected
        }

        var finish = Msg_FinishUpload()
        finish.uploadID = uploadID
        finish.sha256 = sha256
            ?? hasher!.finalize().map { String(format: "%02x", $0) }.joined()
        let done = try await request { $0.payload = .reqFinishUpload(finish) }
        if done.error {
            throw fail(done.errorMessage.isEmpty ? "FinishUpload failed" : done.errorMessage)
        }
        return done
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
