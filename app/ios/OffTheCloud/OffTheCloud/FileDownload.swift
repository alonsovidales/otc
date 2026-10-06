// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FileDownload.swift
//  OffTheCloud
//
//  The download side of ChunkedUpload.swift. Opening a file or an older
//  version in Files, and fetching a synced video to trim or play, used
//  GetFile: the device read the whole blob and sent it as one WebSocket
//  message, which the app then held twice (the frame and the parsed
//  content) before writing it out on the main actor. A 700 MB video was
//  1.4 GB of RAM and a frozen UI; anything over the 1000 MiB message cap
//  failed and took every other request on the socket down with it.
//  ReadFile moves the original in 4 MiB pieces, each written to disk as it
//  arrives (the macOS app's SyncModel.download does the same).

import Foundation

enum FileDownload {
    /// The device answered with an error, or with something else than the
    /// file. Callers show their own "could not fetch" text for it, as they
    /// did for a GetFile that didn't answer with the file.
    struct Refused: LocalizedError {
        let message: String
        var errorDescription: String? { message }
    }

    static let chunkSize = 4 << 20

    /// GetFile converts these to JPEG (files_manager isHeicFile); ReadFile
    /// sends the original. Kept on GetFile, so what is shown and shared
    /// stays exactly the same. They are small.
    static func isConvertedByGetFile(path: String, mime: String) -> Bool {
        path.uppercased().hasSuffix(".HEIC") || mime.lowercased() == "image/heic"
    }

    /// Writes `path` (or its older version `hash`; "" is the current one)
    /// to `dest`, replacing it. Returns the file's mime and size.
    @discardableResult
    static func download(path: String, hash: String = "", mime: String = "", to dest: URL) async throws -> (mime: String, size: Int64) {
        // Issue #190: no route switch while this runs.
        TransferActivity.shared.begin()
        defer { TransferActivity.shared.end() }
        if isConvertedByGetFile(path: path, mime: mime) {
            return try await getFile(path: path, hash: hash, to: dest)
        }

        let part = dest.appendingPathExtension("otc-part")
        let sink = try await Task.detached(priority: .utility) { try ChunkSink(url: part) }.value
        var done = false
        defer {
            sink.close()
            if !done { try? FileManager.default.removeItem(at: part) }
        }

        var offset: Int64 = 0
        var first: Msg_FileChunk?
        repeat {
            let at = offset
            let resp = try await OTCConnection.shared.request { e in
                var rf = Msg_ReadFile()
                rf.path = path
                rf.hash = hash
                rf.offset = at
                rf.length = Int32(chunkSize)
                e.payload = .reqReadFile(rf)
            }
            if resp.error && resp.errorCode == "unknown_payload" {
                // A device from before ReadFile: the old single message.
                return try await getFile(path: path, hash: hash, to: dest)
            }
            guard !resp.error, case .respFileChunk(let chunk) = resp.payload else {
                throw Refused(message: resp.errorMessage.isEmpty ? "Unexpected response" : resp.errorMessage)
            }
            if let first, chunk.hash != first.hash {
                throw NSError(domain: "FileDownload", code: 1, userInfo: [NSLocalizedDescriptionKey: "The file changed on the device during the download"])
            }
            if chunk.offset != offset || (chunk.data.isEmpty && offset < chunk.size) || offset + Int64(chunk.data.count) > chunk.size {
                throw NSError(domain: "FileDownload", code: 2, userInfo: [NSLocalizedDescriptionKey: "The device sent a piece that doesn't fit the file"])
            }
            if first == nil { first = chunk }
            let data = chunk.data
            try await Task.detached(priority: .utility) { try sink.write(data) }.value
            offset += Int64(data.count)
        } while offset < (first?.size ?? 0)

        sink.close()
        try await Task.detached(priority: .utility) {
            try? FileManager.default.removeItem(at: dest)
            try FileManager.default.moveItem(at: part, to: dest)
        }.value
        done = true
        return (first?.mime ?? "", offset)
    }

    private static func getFile(path: String, hash: String, to dest: URL) async throws -> (mime: String, size: Int64) {
        let resp = try await OTCConnection.shared.request { e in
            var gf = Msg_GetFile()
            gf.path = path
            gf.hash = hash
            e.payload = .reqGetFile(gf)
        }
        guard case .respFile(let f) = resp.payload else {
            throw Refused(message: resp.errorMessage.isEmpty ? "Unexpected response" : resp.errorMessage)
        }
        let content = f.content
        try await Task.detached(priority: .utility) { try content.write(to: dest) }.value
        return (f.mime, Int64(content.count))
    }
}

/// Appends to a file from a detached task, never on the main actor.
private final class ChunkSink: @unchecked Sendable {
    private let handle: FileHandle

    init(url: URL) throws {
        FileManager.default.createFile(atPath: url.path, contents: nil)
        handle = try FileHandle(forWritingTo: url)
        try handle.truncate(atOffset: 0)
    }

    func write(_ data: Data) throws {
        try handle.write(contentsOf: data)
    }

    func close() {
        try? handle.close()
    }
}
