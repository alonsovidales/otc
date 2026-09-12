//
//  PhotoSync.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//


import Foundation
import Photos
import SwiftProtobuf
import CryptoKit

final class PhotoSync {
    static let shared = PhotoSync()
    private init() {}

    // How many assets to read from disk + upload at the same time (issue
    // #10). A fixed pool rather than a single sequential stream: each
    // upload's network round-trip is mostly dead time on the client side,
    // so overlapping several keeps both the disk reads and the socket busy
    // instead of idling between every request/ack pair.
    //
    // Capped at cores-1 on the server (Pi 5, 4 cores) rather than something
    // higher: the device also has to hash/dedup/thumbnail/tag each upload on
    // the way in, so pushing more concurrent uploads than the Pi has spare
    // cores for just makes every request (including unrelated ones sharing
    // this same connection, like the Social feed) crawl instead of actually
    // finishing the sync faster.
    private static let cMaxConcurrentUploads = 3

    private func ensureAuth() async throws {
        print("Ensure Auth photos")
        let s = PHPhotoLibrary.authorizationStatus(for: .readWrite)
        if s == .authorized || s == .limited { return }
        let r = await PHPhotoLibrary.requestAuthorization(for: .readWrite)
        guard r == .authorized || r == .limited else {
            throw NSError(domain: "photos", code: 1, userInfo: [NSLocalizedDescriptionKey: "Photos access denied"])
        }
    }

    /// Just the filename PHAssetResource already has in its local metadata
    /// - no network access, no download, unlike exportAssetToTempFile
    /// below (which picks the same resource for the same reason, but
    /// actually fetches its bytes). Used to compute an asset's remote path
    /// cheaply, e.g. to check AssetSyncCache/knownPaths before deciding
    /// whether the expensive iCloud fetch is even needed.
    private func resourceFilename(for asset: PHAsset) -> String? {
        let resources = PHAssetResource.assetResources(for: asset)
        let res = resources.first(where: { $0.type == .photo || $0.type == .fullSizePhoto || $0.type == .video }) ?? resources.first
        return res?.originalFilename
    }

    func exportAssetToTempFile(_ asset: PHAsset,
                               allowNetwork: Bool) throws -> (url: URL, filename: String, mime: String) {
        // Pick a sensible resource (photo/video full size if available)
        let resources = PHAssetResource.assetResources(for: asset)
        guard let res = resources.first(where: { $0.type == .photo || $0.type == .fullSizePhoto || $0.type == .video }) ?? resources.first
        else {
            throw NSError(domain: "PhotoExport", code: -10, userInfo: [NSLocalizedDescriptionKey: "No asset resource"])
        }

        // Temp file to stream into
        let tmpDir = FileManager.default.temporaryDirectory
        let ext = (res.originalFilename as NSString).pathExtension
        let tmpURL = tmpDir.appendingPathComponent(UUID().uuidString).appendingPathExtension(ext.isEmpty ? "bin" : ext)
        try? FileManager.default.removeItem(at: tmpURL)

        // Open an output stream (constant memory usage)
        guard let out = OutputStream(url: tmpURL, append: false) else {
            throw NSError(domain: "PhotoExport", code: -11, userInfo: [NSLocalizedDescriptionKey: "Cannot open output stream"])
        }
        out.open()
        defer { out.close() }

        var streamError: Error?
        let opts = PHAssetResourceRequestOptions()
        opts.isNetworkAccessAllowed = allowNetwork

        let sema = DispatchSemaphore(value: 0)

        // Stream chunks to file
        PHAssetResourceManager.default().requestData(
            for: res,
            options: opts,
            dataReceivedHandler: { chunk in
                // Write the chunk fully (handle partial writes)
                chunk.withUnsafeBytes { rawPtr in
                    guard let base = rawPtr.bindMemory(to: UInt8.self).baseAddress else { return }
                    var bytesLeft = chunk.count
                    var offset = 0
                    while bytesLeft > 0 {
                        let w = out.write(base.advanced(by: offset), maxLength: bytesLeft)
                        if w <= 0 {
                            streamError = out.streamError ?? NSError(
                                domain: "PhotoExport",
                                code: -12,
                                userInfo: [NSLocalizedDescriptionKey: "Failed writing to stream"]
                            )
                            return
                        }
                        bytesLeft -= w
                        offset += w
                    }
                }
            },
            completionHandler: { err in
                streamError = streamError ?? err
                sema.signal()
            }
        )

        sema.wait()

        if let err = streamError {
            try? FileManager.default.removeItem(at: tmpURL)
            throw err
        }

        // Guess MIME
        let filename = res.originalFilename
        let mime: String = {
            if #available(iOS 14.0, *) {
                if let ut = UTType(filenameExtension: ext) {
                    switch ut {
                    case .png: return "image/png"
                    case .jpeg: return "image/jpeg"
                    case .heic: return "image/heic"
                    case .quickTimeMovie: return "video/quicktime"
                    default:
                        return ut.preferredMIMEType ?? "application/octet-stream"
                    }
                }
            }
            let lower = filename.lowercased()
            if lower.hasSuffix(".png")           { return "image/png" }
            if lower.hasSuffix(".jpg") || lower.hasSuffix(".jpeg") { return "image/jpeg" }
            if lower.hasSuffix(".heic")          { return "image/heic" }
            if lower.hasSuffix(".mov")           { return "video/quicktime" }
            return "application/octet-stream"
        }()

        return (tmpURL, filename, mime)
    }
    
    private func fetchNewAssets(includeVideos: Bool, since: Date?) -> [PHAsset] {
        print("Fetch new assets")

        let opts = PHFetchOptions()
        var preds: [NSPredicate] = []
        if let since {
            preds.append(NSPredicate(format: "creationDate > %@", since as NSDate))
        }
        if !includeVideos {
            preds.append(NSPredicate(format: "mediaType == %d", PHAssetMediaType.image.rawValue))
        }
        if !preds.isEmpty {
            opts.predicate = NSCompoundPredicate(andPredicateWithSubpredicates: preds)
        }
        opts.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: true)]
        var out: [PHAsset] = []
        PHAsset.fetchAssets(with: opts).enumerateObjects { a, _, _ in out.append(a) }
        return out
    }

    func readData(for asset: PHAsset,
                  allowNetwork: Bool = true,
                  maxBytes: Int64 = 25 * 1024 * 1024) throws -> (data: Data, filename: String, mime: String) {

        let (url, filename, mime) = try exportAssetToTempFile(asset, allowNetwork: allowNetwork)

        // Check size before loading
        let attrs = try FileManager.default.attributesOfItem(atPath: url.path)
        let size = (attrs[.size] as? NSNumber)?.int64Value ?? 0

        guard size <= maxBytes else {
            // You can also return the URL instead of throwing if you refactor callers.
            throw NSError(domain: "PhotoExport",
                          code: -20,
                          userInfo: [NSLocalizedDescriptionKey: "AssetTooLargeForMemory: \(size) bytes"])
        }

        // Map into memory (still allocates a buffer ~size)
        let data = try Data(contentsOf: url, options: .mappedIfSafe)

        // Clean up temp file if you don’t need it anymore
        try? FileManager.default.removeItem(at: url)

        return (data, filename, mime)
    }
    
    func runForeground() async throws {
        try await ensureAuth()
        let secrets = SecretsStore.loadOrCreate()
        let ws = OTCConnection.shared
        try await ws.ensureConnected()

        let last = UserDefaults.standard.object(forKey: "lastSyncDate") as? Date
        print("Sync photos from: \(last)")
        let assets = fetchNewAssets(includeVideos: secrets.includeVideos, since: last)
        UploadModel.shared.begin(total: assets.count)

        // This was never actually wired up before — the "Sync from iCloud"
        // toggle changed a setting nothing read, so turning it off had no
        // effect on the running sync at all. Captured once, as a plain
        // Bool, rather than passing `secrets` itself into the concurrent
        // tasks below.
        let allowICloudDownloads = secrets.downloadFromiCloud

        let targetPath = "/ios/\(secrets.deviceId)/"
        // Get a list of all the files for the target path
        let resp = try await ws.request { env in
            var list = Msg_ListFiles()
            list.path = targetPath
            env.payload = .reqListFiles(list)
        }
        var knownPaths = Set<String>()
        if case .respListOfFiles(let files) = resp.payload {
            resp.respListOfFiles.files.forEach {
                knownPaths.insert($0.path)
            }
        } else if resp.error {
            print("Upload listing the files:", resp.errorMessage)
        }

        var idx = 0
        for chunk in assets.chunked(into: Self.cMaxConcurrentUploads) {
            // Issue #30: pause/resume from the upload bar. Checked between
            // chunks rather than cancelling in-flight requests — whatever's
            // already uploading finishes, nothing new starts until resumed.
            while UploadModel.shared.isPaused {
                try? await Task.sleep(nanoseconds: 500_000_000)
            }

            await withTaskGroup(of: Void.self) { group in
                for asset in chunk {
                    idx += 1
                    let position = idx
                    group.addTask {
                        do {
                            // Cheap: local Photos metadata only, no
                            // download - lets path (and therefore
                            // knownPaths/the asset cache below) be checked
                            // before ever touching the expensive part.
                            guard let rawName = self.resourceFilename(for: asset) else {
                                throw NSError(domain: "PhotoExport", code: -10, userInfo: [NSLocalizedDescriptionKey: "No asset resource"])
                            }
                            let cleanName = rawName.replacingOccurrences(of: "/", with: "_")
                            let path = "\(targetPath)\(cleanName)"

                            if knownPaths.contains(path) {
                                print("File already in server: \(path)")
                                return
                            }

                            UploadModel.shared.step(file: cleanName, index: position - 1, total: assets.count)
                            let created = Google_Protobuf_Timestamp(date: asset.creationDate ?? Date())

                            // Issue #58 follow-up: a previous successful
                            // sync of this exact PHAsset already told us
                            // its content hash - reuse it instead of
                            // paying for another iCloud download + hash
                            // just to (most likely) rediscover the same
                            // thing. Most valuable after a reinstall: the
                            // device ID (and therefore every remote path)
                            // is fresh then, so the knownPaths check above
                            // can never match even though the content is
                            // identical to what synced before.
                            var data: Data? = nil
                            var hash: String
                            if let cachedHash = AssetSyncCache.shared.hash(for: asset.localIdentifier) {
                                print("[dedup] \(cleanName): asset cache hit -> \(cachedHash)")
                                hash = cachedHash
                            } else {
                                // Not part of the dedup check at all - this
                                // is PHAssetResourceManager fetching the
                                // asset's bytes, which for a library using
                                // iCloud's "Optimize storage" means a real
                                // network download for any asset not
                                // already cached locally. Timed separately
                                // so it's obvious whether a slow gap
                                // between [dedup] lines is this or
                                // something else.
                                let readStart = Date()
                                let (readBytes, _, _) = try self.readData(for: asset, allowNetwork: allowICloudDownloads)
                                print("[dedup] \(cleanName): read \(readBytes.count) bytes from Photos in \(String(format: "%.3f", Date().timeIntervalSince(readStart)))s")
                                data = readBytes

                                let hashStart = Date()
                                hash = SHA256.hash(data: readBytes).map { String(format: "%02x", $0) }.joined()
                                print("[dedup] \(cleanName): hashed \(readBytes.count) bytes in \(String(format: "%.3f", Date().timeIntervalSince(hashStart)))s -> \(hash)")
                            }

                            // Issue #58: storage is deduplicated by hash on
                            // the device already (see the Go
                            // files_manager.UploadFile/LinkFile doc
                            // comments) — this is what actually skips
                            // re-sending the bytes for content the device
                            // already has under some other path (e.g. the
                            // same photo re-synced after a reinstall, or
                            // shared into the library from elsewhere),
                            // rather than relying on that dedup only
                            // kicking in *after* the transfer.
                            func checkHasFile() async throws -> Bool {
                                let hasFileStart = Date()
                                let hasResp = try await ws.request { env in
                                    var hf = Msg_HasFile()
                                    hf.hash = hash
                                    env.payload = .reqHasFile(hf)
                                }
                                print("[dedup] \(cleanName): HasFile round trip in \(String(format: "%.3f", Date().timeIntervalSince(hasFileStart)))s")
                                if case .respFileExists(let fe) = hasResp.payload {
                                    return fe.exists
                                }
                                print("[dedup] \(cleanName): HasFile check failed/unexpected response (error=\(hasResp.error) \(hasResp.errorMessage)) - falling back to full upload")
                                return false
                            }

                            var alreadyOnDevice = try await checkHasFile()

                            // The cached hash turned out not to be on the
                            // device after all (e.g. storage was reset) -
                            // fall back to actually downloading and
                            // hashing it fresh rather than failing outright.
                            if !alreadyOnDevice && data == nil {
                                print("[dedup] \(cleanName): cached hash not confirmed on device, downloading now")
                                let readStart = Date()
                                let (readBytes, _, _) = try self.readData(for: asset, allowNetwork: allowICloudDownloads)
                                print("[dedup] \(cleanName): read \(readBytes.count) bytes from Photos in \(String(format: "%.3f", Date().timeIntervalSince(readStart)))s")
                                data = readBytes

                                let hashStart = Date()
                                hash = SHA256.hash(data: readBytes).map { String(format: "%02x", $0) }.joined()
                                print("[dedup] \(cleanName): hashed \(readBytes.count) bytes in \(String(format: "%.3f", Date().timeIntervalSince(hashStart)))s -> \(hash)")
                                alreadyOnDevice = try await checkHasFile()
                            }
                            print("[dedup] \(cleanName): already on device = \(alreadyOnDevice)")

                            let sendStart = Date()
                            let resp: Msg_RespEnvelope
                            if alreadyOnDevice {
                                resp = try await ws.request { env in
                                    var lf = Msg_LinkFile()
                                    lf.hash = hash
                                    lf.path = path
                                    lf.forceOverride = false
                                    lf.created = created
                                    env.payload = .reqLinkFile(lf)
                                }
                            } else {
                                guard let data else {
                                    throw NSError(domain: "PhotoExport", code: -13, userInfo: [NSLocalizedDescriptionKey: "No data to upload"])
                                }
                                resp = try await ws.request { env in
                                    var up = Msg_UploadFile()
                                    up.path = path
                                    up.content = data
                                    up.forceOverride = false
                                    up.created = created
                                    env.payload = .reqUploadFile(up)
                                }
                            }
                            print("[dedup] \(cleanName): \(alreadyOnDevice ? "LinkFile" : "UploadFile") round trip in \(String(format: "%.3f", Date().timeIntervalSince(sendStart)))s")

                            // A successful UploadFile/LinkFile answers with
                            // RespFile (the stored file's metadata), not
                            // RespAck — checking for the wrong case here
                            // used to mean a real success matched neither
                            // branch and was silently unobserved.
                            if case .respFile = resp.payload {
                                AssetSyncCache.shared.record(localIdentifier: asset.localIdentifier, hash: hash)
                            } else if resp.error {
                                print("Upload failed:", resp.errorMessage)
                            }
                        } catch {
                            if !allowICloudDownloads {
                                // The likely cause when "Sync from iCloud"
                                // is off and this asset isn't cached
                                // locally: PHAssetResourceManager can't
                                // fetch it without network access, so it
                                // throws instead of silently skipping.
                                // That's the correct behavior for the
                                // toggle (only sync what's already on the
                                // device) - just worth a clearer log than
                                // a bare error would give.
                                print("Skipping (iCloud sync disabled, not cached locally):", error)
                            } else {
                                print("Upload error:", error)
                            }
                        }
                    }
                }
            }
            // The whole chunk has been attempted (success or failure per
            // asset) by the time the group above returns, so it's safe to
            // advance the watermark past every date in it.
            if let latest = chunk.compactMap(\.creationDate).max() {
                UserDefaults.standard.set(latest, forKey: "lastSyncDate")
                print("Latest date:", latest)
            }
        }

        // Flush any records not yet written by AssetSyncCache's own batch
        // threshold — otherwise a run that ends (or gets interrupted)
        // between batches loses the last few entries.
        AssetSyncCache.shared.flush()

        UploadModel.shared.complete()
        UserDefaults.standard.set(Date(), forKey: "lastSyncDate")
    }
}

private extension Array {
    /// Splits into consecutive slices of at most `size` elements each (the
    /// last slice may be smaller). Used to bound upload concurrency (#10)
    /// while still giving a natural point, between chunks, to checkpoint
    /// sync progress.
    func chunked(into size: Int) -> [[Element]] {
        guard size > 0 else { return [self] }
        return stride(from: 0, to: count, by: size).map {
            Array(self[$0..<Swift.min($0 + size, count)])
        }
    }
}
