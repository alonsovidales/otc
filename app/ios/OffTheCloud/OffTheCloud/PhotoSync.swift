//
//  PhotoSync.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//


import Foundation
import Photos
import SwiftProtobuf

final class PhotoSync {
    static let shared = PhotoSync()
    private init() {}

    private func ensureAuth() async throws {
        print("Ensure Auth photos")
        let s = PHPhotoLibrary.authorizationStatus(for: .readWrite)
        if s == .authorized || s == .limited { return }
        let r = await PHPhotoLibrary.requestAuthorization(for: .readWrite)
        guard r == .authorized || r == .limited else {
            throw NSError(domain: "photos", code: 1, userInfo: [NSLocalizedDescriptionKey: "Photos access denied"])
        }
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
        guard let url = URL(string: secrets.endpoint) else { throw NSError(domain:"cfg", code:1, userInfo:[NSLocalizedDescriptionKey:"Bad endpoint"]) }

        let ws = WSClient()
        print("Trynig to stablish connection with \(url)")
        try await ws.connect(url: url)

        // Authenticate (if your server requires it first)
        _ = try await ws.request { env in
            var auth = Msg_Auth()
            auth.uuid = secrets.deviceId
            auth.key = secrets.password
            print("Password:", secrets.password)
            auth.create = false
            env.payload = .reqAuth(auth)
        }

        let last = UserDefaults.standard.object(forKey: "lastSyncDate") as? Date
        print("Sync photos from: \(last)")
        let assets = fetchNewAssets(includeVideos: secrets.includeVideos, since: last)
        UploadModel.shared.begin(total: assets.count)
        
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
        for asset in assets {
            idx += 1
            do {
                let (data, name, _) = try readData(for: asset)
                let cleanName = name.replacingOccurrences(of: "/", with: "_")
                let path = "\(targetPath)\(cleanName)"
                
                if knownPaths.contains(path) {
                    print("File already in server: \(path)")
                    // TODO: Check also the hash
                    continue
                }

                UploadModel.shared.step(file: cleanName, index: idx-1, total: assets.count)

                let resp = try await ws.request { env in
                    var up = Msg_UploadFile()
                    up.path = path
                    up.content = data
                    up.forceOverride = false
                    up.created = Google_Protobuf_Timestamp(date: asset.creationDate ?? Date())
                    //TODO: Set the creation date here and not in the server
                    env.payload = .reqUploadFile(up)
                }

                if case .respAck(let ack) = resp.payload, ack.ok {
                    // ok
                } else if resp.error {
                    print("Upload failed:", resp.errorMessage)
                }
                UserDefaults.standard.set(asset.creationDate, forKey: "lastSyncDate")
                print("Latest date:", asset.creationDate)
            } catch {
                print("Upload error:", error)
            }
        }

        UploadModel.shared.complete()
        UserDefaults.standard.set(Date(), forKey: "lastSyncDate")
        ws.close()
    }
}
