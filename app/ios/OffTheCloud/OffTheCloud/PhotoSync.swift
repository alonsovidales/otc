// SPDX-License-Identifier: AGPL-3.0-or-later

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

final class PhotoSync: NSObject {
    static let shared = PhotoSync()
    private override init() {
        super.init()
        // Issue #70: react the moment the Photos library actually changes
        // (a new photo taken, an iCloud download finishing, ...) instead
        // of only ever checking on a cold launch or whenever iOS happens
        // to grant a background task - see photoLibraryDidChange below.
        // PHPhotoLibrary holds observers weakly, so this doesn't need an
        // unregister anywhere: `shared` living for the app's whole process
        // lifetime is what actually keeps it alive. NSObject (rather than
        // a plain class) is required here: PHPhotoLibraryChangeObserver is
        // an @objc protocol.
        PHPhotoLibrary.shared().register(self)
    }

    // Issue #70: guards against two runForeground() calls actually
    // uploading concurrently - now that a sync can be triggered from
    // several independent places (cold launch, returning to foreground,
    // a Photos library change, a BGProcessingTask), more than one of
    // those can legitimately land close together. Uploads are already
    // idempotent (the knownPaths/HasFile dedup checks), so an overlap
    // wouldn't corrupt anything, but it would double up network/disk work
    // and stomp on UploadModel's shared progress state from two directions
    // at once. NSLock rather than an actor: PhotoSync's other methods
    // (resourceFilename, readData, exportAssetToTempFile) are called
    // synchronously from NewPostPicker and have no need to become
    // actor-isolated just for this one flag. The lock/unlock calls
    // themselves stay inside the two plain (non-async) helpers below,
    // never directly in runForeground's own async body - calling
    // NSLock.lock()/.unlock() straight from an async function is flagged
    // even today, and becomes an outright error under Swift 6.
    private let syncLock = NSLock()
    private var isSyncing = false
    /// Bumped by cancel() (Log Out). A run only writes state - the
    /// watermark, the asset cache, the upload bar - and only sends
    /// requests while the generation it started with is current: Log Out
    /// doesn't wait for the run to stop, and a run finishing its chunk
    /// after the wipe used to write lastSyncDate=now into the fresh
    /// defaults, so the next device never got the existing library.
    private var generation = 0
    /// Bumped by syncFromNow(). A run's watermark and retry-list writes
    /// land only while the value it started with is current: after the
    /// press, its per-chunk watermark is older than the press and its
    /// failures are what Sync From Now skips. Its uploads carry on.
    private var fromNowEpoch = 0
    /// The sync in progress, so Log Out can stop it (`cancel()`). Under
    /// syncLock: written from the cooperative pool, read on main.
    private var currentSync: Task<Void, Error>?

    /// The generation this run belongs to, or nil if one is running.
    private func beginSyncIfNotAlreadyRunning() -> Int? {
        syncLock.lock()
        defer { syncLock.unlock() }
        if isSyncing { return nil }
        isSyncing = true
        return generation
    }

    private func endSync() {
        syncLock.lock()
        defer { syncLock.unlock() }
        isSyncing = false
    }

    private func setCurrentSync(_ task: Task<Void, Error>?, gen: Int) {
        syncLock.lock()
        defer { syncLock.unlock() }
        currentSync = task
        // Log Out landed between begin and here.
        if let task, generation != gen { task.cancel() }
    }

    /// Runs `body` only if the run's generation is still current, under
    /// the same lock cancel() takes: a write lands before Log Out (whose
    /// wipe then erases it) or not at all. With `epoch`, likewise only
    /// before a Sync From Now press (see `fromNowEpoch`).
    @discardableResult
    private func ifLive(_ gen: Int, epoch: Int? = nil, _ body: () -> Void = {}) -> Bool {
        syncLock.lock()
        defer { syncLock.unlock() }
        guard generation == gen, epoch == nil || epoch == fromNowEpoch else { return false }
        body()
        return true
    }

    /// Where a run starts: the watermark, and the Sync From Now epoch its
    /// writes belong to, read together.
    private func startingPoint() -> (last: Date?, epoch: Int) {
        syncLock.lock()
        defer { syncLock.unlock() }
        return (UserDefaults.standard.object(forKey: "lastSyncDate") as? Date, fromNowEpoch)
    }

    /// Sync From Now: skip everything already in the library, what earlier
    /// runs couldn't finish included. A run in progress still uploads
    /// what it fetched, as it always did, but no longer moves the
    /// watermark back before now or puts its failures back on the list.
    func syncFromNow() {
        syncLock.lock()
        defer { syncLock.unlock() }
        fromNowEpoch &+= 1
        UserDefaults.standard.set(Date(), forKey: "lastSyncDate")
        AssetSyncCache.shared.clearPending()
    }

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

    /// Each asset's PHCloudIdentifier as a string, by local identifier
    /// (release 7). The cloud identifier is what stays the same for an
    /// asset across the owner's devices and reinstalls, where the local
    /// identifier is this library's alone. Assets PhotoKit can't map are
    /// simply absent, and go through the hash path as before.
    private func cloudIdentifiers(for assets: [PHAsset]) -> [String: String] {
        let ids = assets.map(\.localIdentifier)
        guard !ids.isEmpty else { return [:] }
        var out: [String: String] = [:]
        for (local, result) in PHPhotoLibrary.shared().cloudIdentifierMappings(forLocalIdentifiers: ids) {
            if case .success(let cloud) = result {
                out[local] = cloud.stringValue
            }
        }

        return out
    }

    /// Just the filename PHAssetResource already has in its local metadata
    /// - no network access, no download, unlike exportAssetToTempFile
    /// below (which picks the same resource for the same reason, but
    /// actually fetches its bytes). Used to compute an asset's remote path
    /// cheaply, e.g. to check AssetSyncCache/knownPaths before deciding
    /// whether the expensive iCloud fetch is even needed.
    func resourceFilename(for asset: PHAsset) -> String? {
        let resources = PHAssetResource.assetResources(for: asset)
        let res = resources.first(where: { $0.type == .photo || $0.type == .fullSizePhoto || $0.type == .video }) ?? resources.first
        return res?.originalFilename
    }

    /// The name an asset is stored under on the device. Camera names
    /// repeat - IMG_0001..IMG_9999 wraps, and an iPhone and an iPad share
    /// one iCloud library - so a different asset whose name is already
    /// taken goes to `alt`: the name plus 8 hex of its localIdentifier's
    /// SHA-256, before the extension. Deterministic, so later runs (and
    /// the post composer) find it there again.
    static func remoteName(_ cleanName: String, localIdentifier: String, alt: Bool) -> String {
        guard alt else { return cleanName }
        let suffix = SHA256.hash(data: Data(localIdentifier.utf8)).prefix(4).map { String(format: "%02x", $0) }.joined()
        let ext = (cleanName as NSString).pathExtension
        guard !ext.isEmpty else { return "\(cleanName)_\(suffix)" }
        return "\((cleanName as NSString).deletingPathExtension)_\(suffix).\(ext)"
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

    /// The retry list's assets that still exist, whatever their type: a
    /// video kept for later while "Include videos" is off stays on the list.
    private func fetchAssets(localIdentifiers ids: [String]) -> [PHAsset] {
        guard !ids.isEmpty else { return [] }
        var out: [PHAsset] = []
        PHAsset.fetchAssets(withLocalIdentifiers: ids, options: nil).enumerateObjects { a, _, _ in out.append(a) }
        return out
    }

    /// What became of one asset in a run.
    private enum Outcome {
        /// On the device (or found there).
        case done(String)
        /// Failed for what may be a passing reason (the connection, the
        /// device, an iCloud download): tried again next run.
        case retry(String)
        /// Couldn't be read with "Sync from iCloud" off: tried again once
        /// it is on.
        case retryWhenICloud(String)
        /// Can never work (no resource, over the 1 GB ceiling): given up
        /// on, as before. Retrying would export the whole file each run.
        case skip(String)
    }

    /// Reading the asset from Photos failed, as opposed to talking to the
    /// device.
    private struct ReadFailure: Error {
        let underlying: Error
    }

    /// Remote paths taken during one run, with the content each was taken
    /// for, so a second asset with the same name and other content goes
    /// straight to its alternate name instead of a wasted transfer (or, in
    /// an upload-only folder, replacing the first). The same content keeps
    /// the name - a photo duplicated in Photos keeps its filename - and the
    /// device answers it with the existing row: one file, as always.
    final class PathClaims: @unchecked Sendable {
        private let lock = NSLock()
        private var owners: [String: (id: String, hash: String)] = [:]

        /// True when `path` is free, already `id`'s, or taken for the same
        /// content.
        func claim(_ path: String, by id: String, hash: String) -> Bool {
            lock.lock()
            defer { lock.unlock() }
            if let owner = owners[path] { return owner.id == id || owner.hash == hash }
            owners[path] = (id, hash)
            return true
        }
    }

    /// Whether a listed file's creation time is the asset's: it then is
    /// that asset, synced before, as the bare path check always assumed.
    /// Stored as the client sent it, in whole seconds (MySQL DATETIME).
    private static func sameCreation(_ listed: Date?, _ asset: Date?) -> Bool {
        guard let listed, let asset else { return true }
        return abs(listed.timeIntervalSince(asset)) <= 1
    }

    // 25MB was never revisited after this only had to handle photos - any
    // video (this app syncs videos too, per the "Include videos" setting)
    // over that size always threw AssetTooLargeForMemory, including from
    // plain background sync, not just issue #49's new picker where this
    // got noticed. Data(contentsOf:options:.mappedIfSafe) below is a
    // memory-mapped read, not a full eager load, so 1GB here is a safety
    // ceiling against a truly pathological file, not a real memory budget.
    func readData(for asset: PHAsset,
                  allowNetwork: Bool = true,
                  maxBytes: Int64 = 1024 * 1024 * 1024) throws -> (data: Data, filename: String, mime: String) {

        let (url, filename, mime) = try exportAssetToTempFile(asset, allowNetwork: allowNetwork)
        // On every exit, the too-large throw included: a rejected 1 GB+
        // video used to leave its whole copy behind in tmp. The mapping
        // below stays valid after the unlink.
        defer { try? FileManager.default.removeItem(at: url) }

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

        return (data, filename, mime)
    }
    
    /// Log Out: stop the sync in progress, and make sure nothing it still
    /// does afterwards writes anything (see `generation`).
    func cancel() {
        syncLock.lock()
        defer { syncLock.unlock() }
        generation &+= 1
        currentSync?.cancel()
    }

    /// The sync runs in its own Task so Log Out can cancel it. Awaiting a
    /// Task's value doesn't pass the caller's cancellation on, so the
    /// handler below does: that is how a BGProcessingTask's expiration
    /// (SyncScheduler cancels the Task calling this) reaches the loop.
    func runForeground() async throws {
        guard let gen = beginSyncIfNotAlreadyRunning() else {
            print("Sync already running, skipping overlapping request")
            return
        }
        defer { endSync() }
        // Issue #190: no route switch while a sync runs.
        TransferActivity.shared.begin()
        defer { TransferActivity.shared.end() }
        let sync = Task { try await self.syncOnce(gen: gen) }
        setCurrentSync(sync, gen: gen)
        defer { setCurrentSync(nil, gen: gen) }
        try await withTaskCancellationHandler {
            try await sync.value
        } onCancel: {
            sync.cancel()
        }
    }

    private func syncOnce(gen: Int) async throws {
        try await ensureAuth()
        let secrets = SecretsStore.loadOrCreate()
        let ws = OTCConnection.shared
        try await ws.ensureConnected()

        // This was never actually wired up before — the "Sync from iCloud"
        // toggle changed a setting nothing read, so turning it off had no
        // effect on the running sync at all. Captured once, as a plain
        // Bool, rather than passing `secrets` itself into the concurrent
        // tasks below.
        let allowICloudDownloads = secrets.downloadFromiCloud

        // The watermark never goes past this: a photo with a future date
        // (a wrong camera clock) can't push it ahead of what was fetched.
        let runStart = Date()
        let (stored, epoch) = startingPoint()
        var last = stored
        // A watermark ahead of the clock (Sync From Now, or photos synced
        // while the clock was ahead, then corrected) would match no new
        // photo until real time caught up: the end-of-run "now" write that
        // used to correct it is gone. Pulled back, as on Android.
        if let s = stored, s > runStart {
            let pulled = runStart.addingTimeInterval(-1)
            ifLive(gen, epoch: epoch) { UserDefaults.standard.set(pulled, forKey: "lastSyncDate") }
            last = pulled
        }
        print("Sync photos from: \(String(describing: last))")
        var assets = fetchNewAssets(includeVideos: secrets.includeVideos, since: last)

        // Assets earlier runs couldn't finish, which the watermark has
        // already moved past: first, unless the new fetch has them anyway.
        let pendingIDs = AssetSyncCache.shared.pending(includeICloud: allowICloudDownloads)
        var retriedIDs = Set<String>()
        if !pendingIDs.isEmpty {
            let found = fetchAssets(localIdentifiers: pendingIDs)
            let foundIDs = Set(found.map(\.localIdentifier))
            let gone = pendingIDs.filter { !foundIDs.contains($0) }
            ifLive(gen) { AssetSyncCache.shared.resolve(gone) }
            let fresh = Set(assets.map(\.localIdentifier))
            let retried = found.filter {
                !fresh.contains($0.localIdentifier) && (secrets.includeVideos || $0.mediaType == .image)
            }
            retriedIDs = Set(retried.map(\.localIdentifier))
            assets = retried + assets
            print("Retrying \(retried.count) assets from earlier runs")
        }
        ifLive(gen) { UploadModel.shared.begin(total: assets.count) }

        let targetPath = "/ios/\(secrets.deviceId)/"
        // Get a list of all the files for the target path - only when
        // there is something to check against it. Every library change and
        // every return to the foreground starts a run, almost always with
        // nothing new, and the listing holds every photo this phone ever
        // synced (tens of MB through the bridge for a big library).
        // With each file's hash and creation time: a path alone can't tell
        // this asset from another one that had the same name.
        var knownFiles: [String: (hash: String, created: Date?)] = [:]
        if !assets.isEmpty {
            let resp = try await ws.request { env in
                var list = Msg_ListFiles()
                list.path = targetPath
                env.payload = .reqListFiles(list)
            }
            if case .respListOfFiles = resp.payload {
                resp.respListOfFiles.files.forEach {
                    knownFiles[$0.path] = ($0.hash, $0.hasCreated ? $0.created.date : nil)
                }
            } else if resp.error {
                print("Upload listing the files:", resp.errorMessage)
            }
        }

        // Release 7: ask the device which of these assets it already holds
        // by their PHCloudIdentifier - the same id on every device signed
        // into the owner's iCloud account - and get each one's content
        // hash back. A match is linked straight to its path below without
        // the iCloud download and the hashing that used to be the only
        // way to find out. One PhotoKit lookup and a few batched round
        // trips up front, instead of one download per asset.
        let cloudIDs = cloudIdentifiers(for: assets)
        var knownCloudHashes: [String: String] = [:]
        for batch in Array(cloudIDs.values).chunked(into: 500) {
            let found = try? await ws.request { env in
                var q = Msg_HasCloudIds()
                q.cloudIds = batch
                env.payload = .reqHasCloudIds(q)
            }
            if let found, case .respCloudIdsFound(let f) = found.payload {
                for entry in f.files {
                    knownCloudHashes[entry.cloudID] = entry.hash
                }
            }
        }
        let knownCloud = knownCloudHashes
        print("[dedup] \(knownCloud.count) of \(cloudIDs.count) assets already on the device by cloud id")

        let claims = PathClaims()
        var idx = 0
        // Cancelled: the run throws, so a BG task reports it unfinished.
        var stopped = false
        let chunks = assets.chunked(into: Self.cMaxConcurrentUploads)
        for (ci, chunk) in chunks.enumerated() {
            // Issue #70: a BGProcessingTask's expiration and Log Out cancel
            // this run. Checked between chunks; uploads already in flight
            // stop at their next 4 MiB chunk (uploadChunked's
            // checkCancellation).
            if Task.isCancelled { stopped = true; break }

            // Issue #30: pause/resume from the upload bar. Checked between
            // chunks rather than cancelling in-flight requests — whatever's
            // already uploading finishes, nothing new starts until resumed.
            // Not while cancelled: the sleep then throws at once and this
            // spun at full CPU.
            while UploadModel.shared.isPaused && !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 500_000_000)
            }
            if Task.isCancelled { stopped = true; break }

            let outcomes = await withTaskGroup(of: Outcome.self, returning: [Outcome].self) { group in
                for asset in chunk {
                    idx += 1
                    let position = idx
                    group.addTask {
                        let id = asset.localIdentifier
                        // Reads the asset's bytes, failures marked as such.
                        func read() throws -> Data {
                            do { return try self.readData(for: asset, allowNetwork: allowICloudDownloads).data }
                            catch { throw ReadFailure(underlying: error) }
                        }
                        do {
                            // Cheap: local Photos metadata only, no
                            // download - lets path (and therefore
                            // knownFiles/the asset cache below) be checked
                            // before ever touching the expensive part.
                            guard let rawName = self.resourceFilename(for: asset) else {
                                throw NSError(domain: "PhotoExport", code: -10, userInfo: [NSLocalizedDescriptionKey: "No asset resource"])
                            }
                            let cleanName = rawName.replacingOccurrences(of: "/", with: "_")
                            let basePath = "\(targetPath)\(cleanName)"
                            let altPath = "\(targetPath)\(Self.remoteName(cleanName, localIdentifier: id, alt: true))"
                            var path = basePath

                            if let listed = knownFiles[basePath], Self.sameCreation(listed.created, asset.creationDate) {
                                print("File already in server: \(basePath)")
                                return .done(id)
                            }
                            if knownFiles[altPath] != nil {
                                print("File already in server: \(altPath)")
                                return .done(id)
                            }

                            self.ifLive(gen) { UploadModel.shared.step(file: cleanName, index: position - 1, total: assets.count) }
                            let created = Google_Protobuf_Timestamp(date: asset.creationDate ?? Date())

                            // Issue #58 follow-up: a previous successful
                            // sync of this exact PHAsset already told us
                            // its content hash - reuse it instead of
                            // paying for another iCloud download + hash
                            // just to (most likely) rediscover the same
                            // thing. Most valuable after a reinstall: the
                            // device ID (and therefore every remote path)
                            // is fresh then, so the knownFiles check above
                            // can never match even though the content is
                            // identical to what synced before.
                            var data: Data? = nil
                            var hash: String
                            let cloudID = cloudIDs[asset.localIdentifier] ?? ""
                            let cloudHash = cloudID.isEmpty ? nil : knownCloud[cloudID]
                            if let cloudHash {
                                // Release 7: the device already has this
                                // asset (uploaded from another phone, or
                                // before a reinstall) - no download, no
                                // hashing, no HasFile round trip.
                                print("[dedup] \(cleanName): cloud id already on device -> \(cloudHash)")
                                hash = cloudHash
                            } else if let cachedHash = AssetSyncCache.shared.hash(for: asset.localIdentifier) {
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
                                let readBytes = try read()
                                print("[dedup] \(cleanName): read \(readBytes.count) bytes from Photos in \(String(format: "%.3f", Date().timeIntervalSince(readStart)))s")
                                data = readBytes

                                let hashStart = Date()
                                hash = SHA256.hash(data: readBytes).map { String(format: "%02x", $0) }.joined()
                                print("[dedup] \(cleanName): hashed \(readBytes.count) bytes in \(String(format: "%.3f", Date().timeIntervalSince(hashStart)))s -> \(hash)")
                            }

                            // The name is taken by a file created at another
                            // time: the same photo only if the content is.
                            if let listed = knownFiles[basePath] {
                                if listed.hash == hash {
                                    print("File already in server: \(basePath) (same content)")
                                    self.ifLive(gen) { AssetSyncCache.shared.record(localIdentifier: id, hash: hash) }
                                    return .done(id)
                                }
                                print("[dedup] \(cleanName): \(basePath) holds another file, using \(altPath)")
                                path = altPath
                            } else if !claims.claim(basePath, by: id, hash: hash) {
                                print("[dedup] \(cleanName): name taken in this run by other content, using \(altPath)")
                                path = altPath
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
                                    // Names the asset for the device, so
                                    // content it already has (from any
                                    // platform, or older builds) gets its
                                    // cloud id attached by hash.
                                    hf.cloudID = cloudID
                                    env.payload = .reqHasFile(hf)
                                }
                                print("[dedup] \(cleanName): HasFile round trip in \(String(format: "%.3f", Date().timeIntervalSince(hasFileStart)))s")
                                if case .respFileExists(let fe) = hasResp.payload {
                                    return fe.exists
                                }
                                print("[dedup] \(cleanName): HasFile check failed/unexpected response (error=\(hasResp.error) \(hasResp.errorMessage)) - falling back to full upload")
                                return false
                            }

                            // Log Out since this asset started (an iCloud
                            // download can take long): nothing more goes to
                            // the device under the old session's path.
                            guard self.ifLive(gen) else { return .retry(id) }
                            var alreadyOnDevice = cloudHash != nil
                            if !alreadyOnDevice {
                                alreadyOnDevice = try await checkHasFile()
                            }

                            // The cached hash turned out not to be on the
                            // device after all (e.g. storage was reset) -
                            // fall back to actually downloading and
                            // hashing it fresh rather than failing outright.
                            if !alreadyOnDevice && data == nil {
                                print("[dedup] \(cleanName): cached hash not confirmed on device, downloading now")
                                let readStart = Date()
                                let readBytes = try read()
                                print("[dedup] \(cleanName): read \(readBytes.count) bytes from Photos in \(String(format: "%.3f", Date().timeIntervalSince(readStart)))s")
                                data = readBytes

                                let hashStart = Date()
                                hash = SHA256.hash(data: readBytes).map { String(format: "%02x", $0) }.joined()
                                print("[dedup] \(cleanName): hashed \(readBytes.count) bytes in \(String(format: "%.3f", Date().timeIntervalSince(hashStart)))s -> \(hash)")
                                guard self.ifLive(gen) else { return .retry(id) }
                                alreadyOnDevice = try await checkHasFile()
                            }
                            print("[dedup] \(cleanName): already on device = \(alreadyOnDevice)")

                            func send(to target: String) async throws -> Msg_RespEnvelope {
                                if alreadyOnDevice {
                                    let h = hash
                                    return try await ws.request { env in
                                        var lf = Msg_LinkFile()
                                        lf.hash = h
                                        lf.path = target
                                        lf.forceOverride = false
                                        lf.created = created
                                        lf.cloudID = cloudID
                                        env.payload = .reqLinkFile(lf)
                                    }
                                }
                                guard let data else {
                                    throw NSError(domain: "PhotoExport", code: -13, userInfo: [NSLocalizedDescriptionKey: "No data to upload"])
                                }
                                // Issue #165: chunked, never the whole
                                // file in one message; reuses the hash
                                // already computed for HasFile.
                                return try await ws.uploadChunked(path: target, source: .data(data),
                                                                  forceOverride: false, created: created,
                                                                  cloudID: cloudID, sha256: hash)
                            }

                            let sendStart = Date()
                            var resp: Msg_RespEnvelope
                            do {
                                resp = try await send(to: path)
                            } catch let error where path != altPath && ErrorCodes.isDuplicatedFile(error) {
                                resp = Msg_RespEnvelope.with {
                                    $0.error = true
                                    $0.errorCode = ErrorCodes.code(of: error)
                                    $0.errorMessage = error.localizedDescription
                                }
                            }
                            print("[dedup] \(cleanName): \(alreadyOnDevice ? "LinkFile" : "chunked upload") round trip in \(String(format: "%.3f", Date().timeIntervalSince(sendStart)))s")
                            // Another file got the name first (in this run,
                            // or from the post composer): once more under
                            // the asset's own alternate name. The device
                            // says this only for different content, so it
                            // can never store the same photo twice.
                            // ErrorCodes: error_code "duplicated_file", or
                            // an older device's message.
                            if path != altPath && ErrorCodes.isDuplicatedFile(resp) {
                                print("[dedup] \(cleanName): \(path) holds another file, using \(altPath)")
                                path = altPath
                                guard self.ifLive(gen) else { return .retry(id) }
                                // A refused upload's content is dropped
                                // with it; it may be there from elsewhere.
                                if !alreadyOnDevice { alreadyOnDevice = try await checkHasFile() }
                                resp = try await send(to: path)
                            }

                            // A successful UploadFile/LinkFile answers with
                            // RespFile (the stored file's metadata), not
                            // RespAck — checking for the wrong case here
                            // used to mean a real success matched neither
                            // branch and was silently unobserved.
                            if case .respFile = resp.payload {
                                self.ifLive(gen) { AssetSyncCache.shared.record(localIdentifier: asset.localIdentifier, hash: hash) }
                                return .done(id)
                            }
                            print("Upload failed:", resp.errorMessage)
                            return .retry(id)
                        } catch {
                            let cause = (error as? ReadFailure)?.underlying ?? error
                            let ns = cause as NSError
                            if ns.domain == "PhotoExport" && (ns.code == -10 || ns.code == -20) {
                                print("Skipping:", cause)
                                return .skip(id)
                            }
                            if error is ReadFailure && !allowICloudDownloads {
                                // The likely cause when "Sync from iCloud"
                                // is off and this asset isn't cached
                                // locally: PHAssetResourceManager can't
                                // fetch it without network access, so it
                                // throws instead of silently skipping.
                                // That's the correct behavior for the
                                // toggle (only sync what's already on the
                                // device); it is picked up once the toggle
                                // is on.
                                print("Skipping (iCloud sync disabled, not cached locally):", cause)
                                return .retryWhenICloud(id)
                            }
                            print("Upload error:", cause)
                            return .retry(id)
                        }
                    }
                }
                var out: [Outcome] = []
                for await outcome in group { out.append(outcome) }
                return out
            }

            var finished: [String] = [], failed: [String] = [], failedICloud: [String] = []
            for outcome in outcomes {
                switch outcome {
                case .done(let id), .skip(let id): finished.append(id)
                case .retry(let id): failed.append(id)
                case .retryWhenICloud(let id): failedICloud.append(id)
                }
            }
            // The retry list before the watermark: no asset is ever behind
            // the watermark without being on the device or on the list.
            // Failures used to be logged and then skipped for good.
            ifLive(gen, epoch: epoch) {
                AssetSyncCache.shared.resolve(finished)
                AssetSyncCache.shared.markPending(failed, iCloud: false)
                AssetSyncCache.shared.markPending(failedICloud, iCloud: true)
            }

            // Cancelled while the chunk ran: its uploads were cut short,
            // so the watermark stays before it.
            if Task.isCancelled { stopped = true; break }

            // The whole chunk has been attempted by the time the group
            // above returns, so the watermark can move past the newly
            // fetched dates in it (retried assets are older).
            if var latest = chunk.filter({ !retriedIDs.contains($0.localIdentifier) }).compactMap(\.creationDate).max() {
                // A date the next chunk's first fresh asset shares isn't
                // done yet (as on Android): a run stopped before it would
                // skip it for good under the fetch's strict "after". This
                // chunk's assets at that date are fetched again next run
                // and skipped as already synced.
                if ci + 1 < chunks.count,
                   let next = chunks[ci + 1].first(where: { !retriedIDs.contains($0.localIdentifier) })?.creationDate,
                   next <= latest {
                    latest = latest.addingTimeInterval(-0.001)
                }
                let mark = min(latest, runStart)
                ifLive(gen, epoch: epoch) { UserDefaults.standard.set(mark, forKey: "lastSyncDate") }
                print("Latest date:", mark)
            }

            // A failure may mean the device is gone (restarting, the
            // bridge answering device_unreachable). Then every later asset
            // fails too, most after a full iCloud download; the next
            // trigger carries on from the watermark instead.
            if !failed.isEmpty {
                do {
                    try await ws.ensureConnected()
                } catch {
                    print("Device not reachable, stopping this run:", error)
                    break
                }
            }
        }

        // Fold the records journal into its snapshot when it got long, and
        // take the upload bar down - also when the run stopped early.
        ifLive(gen) {
            AssetSyncCache.shared.flush()
            UploadModel.shared.complete()
        }
        if stopped {
            // SyncScheduler reports the BG task as not completed.
            throw CancellationError()
        }
        // No final "lastSyncDate = now" any more: the per-chunk watermark
        // already stands at the newest asset handled, and now would skip
        // whatever arrived during the run with an older date.
    }
}

// Issue #70: "the photo sync only runs when the app opens and ... doesn't
// continue checking for new photos - it should check all the time." A
// timer/poll would work but wastes battery re-scanning the library on a
// schedule that's either too slow (miss a photo taken between polls, the
// exact bug reported) or too fast (pointless churn when nothing changed).
// PHPhotoLibraryChangeObserver is the event-driven alternative Photos
// itself offers: this fires the moment the library actually changes, so a
// freshly-taken photo (or one that finishes downloading from iCloud)
// triggers a sync right away, no matter how long the app has been sitting
// idle in the foreground.
extension PhotoSync: PHPhotoLibraryChangeObserver {
    func photoLibraryDidChange(_ changeInstance: PHChange) {
        // Runs on an arbitrary PhotoKit-owned queue - hop into a Task
        // rather than calling the async runForeground() directly here.
        // isSyncing (checked inside runForeground) coalesces a burst of
        // several rapid changes (e.g. importing multiple Live Photos at
        // once fires this more than once) into whichever pass is already
        // in flight picking all of them up, rather than one overlapping
        // run per notification.
        Task {
            try? await runForeground()
        }
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
