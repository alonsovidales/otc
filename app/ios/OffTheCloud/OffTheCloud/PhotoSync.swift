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

    private func readData(for asset: PHAsset) throws -> (data: Data, filename: String, mime: String) {
        let res = PHAssetResource.assetResources(for: asset)
        let preferred = res.first(where: { $0.type == .photo || $0.type == .fullSizePhoto || $0.type == .video }) ?? res.first!

        var data = Data()
        var loadError: Error?
        let sem = DispatchSemaphore(value: 0)   // start at 0
        
        let opts = PHAssetResourceRequestOptions()
        let secrets = SecretsStore.loadOrCreate()
        opts.isNetworkAccessAllowed = secrets.downloadFromiCloud
        
        PHAssetResourceManager.default().requestData(for: preferred, options: opts,
            dataReceivedHandler: { data.append($0) },
            completionHandler: { error in
                loadError = error
                sem.signal()                    // exactly one signal
            }
        )

        sem.wait()                              // exactly one wait

        if let err = loadError { throw err }

        let name = preferred.originalFilename
        let mime: String = {
            let lower = name.lowercased()
            if lower.hasSuffix(".png")  { return "image/png" }
            if lower.hasSuffix(".jpg") || lower.hasSuffix(".jpeg") { return "image/jpeg" }
            if lower.hasSuffix(".heic") { return "image/heic" }
            if lower.hasSuffix(".mov")  { return "video/quicktime" }
            return "application/octet-stream"
        }()

        return (data, name, mime)
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
