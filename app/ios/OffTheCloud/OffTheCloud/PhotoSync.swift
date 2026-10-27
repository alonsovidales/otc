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
import UIKit

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
        // an @objc protocol. Only once the app may read the library
        // (observeLibraryIfAllowed).
        observeLibraryIfAllowed()
        // "Wi-Fi only" waits on mobile data; back on Wi-Fi, the sync that
        // waited goes (IOS-01).
        NetworkWatch.shared.watchCost { [weak self] expensive in
            guard !expensive, let self, self.takeWaitingForWifi() else { return }
            print("On Wi-Fi again: the sync that waited for it starts")
            Task { try? await self.runForeground() }
        }
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
    /// The library's change observer is registered.
    private var observing = false

    /// Issue #70's change observer, once the app may read the library:
    /// registered before that, it made iOS ask for access then and there -
    /// over Welcome at the first launch, or as the app opened on a device
    /// just signed in to - instead of at the sync choice's Start Sync
    /// (IOS-07). Registered at most once; each pass asks again.
    private func observeLibraryIfAllowed() {
        guard Self.libraryReadable else { return }
        let first: Bool = syncLock.withLock {
            defer { observing = true }
            return !observing
        }
        if first { PHPhotoLibrary.shared().register(self) }
    }
    /// Bumped by cancel() (Log Out). A run only writes state - the
    /// watermark, the asset cache, the upload bar - and only sends
    /// requests while the generation it started with is current: Log Out
    /// doesn't wait for the run to stop, and a run finishing its chunk
    /// after the wipe used to write lastSyncDate=now into the fresh
    /// defaults, so the next device never got the existing library.
    private var generation = 0
    /// Bumped by syncFromNow() and syncAll(), as Android's watermarkGen. A
    /// pass's watermark and retry-list writes land only while the value it
    /// started with is current: after Sync From Now its per-chunk watermark
    /// is older than the press and its failures are what the press skips;
    /// after Sync All it would write a watermark back over the "from the
    /// start" just chosen. Its uploads carry on, and the run goes through
    /// once more (rerunWanted) from the new starting point.
    private var watermarkEpoch = 0
    /// The run in progress, its passes included: Log Out (`cancel()`) and
    /// the end of a BG task's or the app's background time (`stopRun()`)
    /// stop it, and a BG task waits for it (runInBackground). Under
    /// syncLock.
    private var running: Task<Void, Error>?
    /// IOS-12: a trigger that came while a run was going - a new photo, the
    /// app back in front, Sync All - used to be dropped. The run goes
    /// through once more at its end instead (as Android's runs do).
    private var rerunWanted = false
    /// The last run stopped because the phone was on mobile data with
    /// "Wi-Fi only" on: the next cheap path starts one (watchCost).
    private var waitingForWifi = false
    /// When a run last stopped on the device's storage_full: automatic runs
    /// wait `fullPause` after it rather than reading photos only to have
    /// them refused. Sync All and Sync From Now try again at once.
    private var fullSince: Date?
    /// The device's words then, for Settings' line while the runs wait.
    private var fullWords = ""
    static let fullPause: TimeInterval = 30 * 60

    /// What one pass of the sync came to, besides throwing when it was
    /// cancelled.
    enum PassEnd: Equatable {
        /// Through every chunk (failures on the retry list).
        case done
        /// Nothing to do: no device yet, the sync choice not made, no
        /// access to the library.
        case skipped
        /// Stopped: the device can't be reached.
        case cutShort
        /// Stopped, or never started: "Wi-Fi only" on mobile data.
        case waitingForWifi
        /// Stopped on the device's storage_full, with its words.
        case deviceFull(String)
    }

    /// Starts the run, as `body` given the generation it belongs to, unless
    /// one is going: that one then goes through once more, and a BG task
    /// (`join`) gets it to wait for. Created under the lock, so no caller
    /// finds a run going with no task to wait for.
    private func startRun(join: Bool, _ body: @escaping @Sendable (Int) async throws -> Void) -> (task: Task<Void, Error>, started: Bool)? {
        syncLock.lock()
        defer { syncLock.unlock() }
        if isSyncing {
            rerunWanted = true
            guard join, let running else { return nil }
            return (running, false)
        }
        isSyncing = true
        rerunWanted = false
        let gen = generation
        let task = Task { try await body(gen) }
        running = task
        return (task, true)
    }

    private func endSync() {
        syncLock.lock()
        defer { syncLock.unlock() }
        isSyncing = false
        running = nil
        rerunWanted = false
    }

    /// Whether the run goes through once more; asking takes the wish.
    private func takeRerun() -> Bool {
        syncLock.lock()
        defer { syncLock.unlock() }
        defer { rerunWanted = false }
        return rerunWanted
    }

    private func takeWaitingForWifi() -> Bool {
        syncLock.lock()
        defer { syncLock.unlock() }
        defer { waitingForWifi = false }
        return waitingForWifi
    }

    private func setWaitingForWifi(_ waiting: Bool) {
        syncLock.lock()
        defer { syncLock.unlock() }
        waitingForWifi = waiting
    }

    /// The device's words when it was full a short while ago (fullSince,
    /// fullPause), else nil.
    private func fullWordsIfStillFull(now: Date = Date()) -> String? {
        syncLock.lock()
        defer { syncLock.unlock() }
        guard let fullSince, Self.waitsAfterFull(since: fullSince, now: now) else { return nil }
        return fullWords
    }

    private func setFull(_ since: Date?, words: String) {
        syncLock.lock()
        defer { syncLock.unlock() }
        fullSince = since
        fullWords = words
    }

    /// Pure: whether an automatic run still waits after the device said it
    /// was full at `since`. A clock set back doesn't hold it for ever.
    static func waitsAfterFull(since: Date, now: Date) -> Bool {
        now >= since && now.timeIntervalSince(since) < fullPause
    }

    /// "Wi-Fi only" turned off (SecretsStore): a run that waited for Wi-Fi
    /// goes now.
    func wifiOnlyOff() {
        guard takeWaitingForWifi() else { return }
        Task { try? await runForeground() }
    }

    /// The end of a BG task's or of the app's background time: the run
    /// stops at its next step, the watermark before what it didn't finish,
    /// without the Log Out generation bump (nothing is wiped).
    func stopRun() {
        syncLock.lock()
        defer { syncLock.unlock() }
        running?.cancel()
    }

    /// Runs `body` only if the run's generation is still current, under
    /// the same lock cancel() takes: a write lands before Log Out (whose
    /// wipe then erases it) or not at all. With `epoch`, likewise only
    /// before Sync From Now or Sync All (see `watermarkEpoch`).
    @discardableResult
    private func ifLive(_ gen: Int, epoch: Int? = nil, _ body: () -> Void = {}) -> Bool {
        syncLock.lock()
        defer { syncLock.unlock() }
        guard generation == gen, epoch == nil || epoch == watermarkEpoch else { return false }
        body()
        return true
    }

    /// Where a pass starts: the watermark, and the epoch its writes belong
    /// to, read together.
    private func startingPoint() -> (last: Date?, epoch: Int) {
        syncLock.lock()
        defer { syncLock.unlock() }
        return (UserDefaults.standard.object(forKey: "lastSyncDate") as? Date, watermarkEpoch)
    }

    /// Sync From Now: skip everything already in the library, what earlier
    /// runs couldn't finish included. A run in progress still uploads
    /// what it fetched, as it always did, but no longer moves the
    /// watermark back before now or puts its failures back on the list.
    /// The one-off passes waiting go too, and the change history starts
    /// from here (without access to read the library's token, from the
    /// next run's). With limited access, the photos visible now are what
    /// the next run compares with: only those picked after this are sent.
    func syncFromNow() {
        let token = Self.libraryReadable ? PhotoKitHistory().currentToken : nil
        let visible = PHPhotoLibrary.authorizationStatus(for: .readWrite) == .limited ? visibleIDs() : nil
        syncLock.lock()
        defer { syncLock.unlock() }
        watermarkEpoch &+= 1
        // Asked for now: a run going goes through once more from here, and
        // a device that was full is tried again at once.
        if isSyncing { rerunWanted = true }
        fullSince = nil
        let now = Date()
        UserDefaults.standard.set(now, forKey: "lastSyncDate")
        state.floor = now
        state.backfill = []
        if let visible { state.setLimitedSeen(visible) } else { state.removeLimitedSeen() }
        state.changeToken = token
        AssetSyncCache.shared.clearPending()
    }

    /// Sync All: the whole library again, from the start. Its point to
    /// sync from is "everything" (State.floor). A run going keeps its
    /// uploads but writes no watermark back over this (watermarkEpoch),
    /// and goes through once more from the start. The caller starts a run
    /// (runForeground) for when none is going.
    func syncAll() {
        syncLock.lock()
        defer { syncLock.unlock() }
        watermarkEpoch &+= 1
        if isSyncing { rerunWanted = true }
        fullSince = nil
        UserDefaults.standard.removeObject(forKey: "lastSyncDate")
        state.floor = .distantPast
    }

    /// An album turned off or back on (Settings > Sync Options > Albums).
    /// Off: its photos are left out from now on; nothing is deleted, here
    /// or on the device. On: a one-off pass sends what it already holds,
    /// older than the watermark too - what the device has is found and not
    /// sent again - starting now, or with the next run if one is going.
    /// Called on the main thread (the skip list is published). Both lists
    /// change under the lock, together.
    func setAlbum(_ id: String, skipped: Bool, secrets: SecretsStore) {
        syncLock.lock()
        let next = PhotoSources.toggled(id, skipped: skipped, skips: secrets.skippedAlbums, backfill: state.backfill)
        state.backfill = next.backfill
        secrets.skippedAlbums = next.skips
        syncLock.unlock()
        if !skipped {
            Task { try? await runForeground() }
        }
    }

    /// The sync's own state besides the watermark and the retry list.
    private let state = State.standard

    /// Full or limited access: what the library's change token needs.
    private static var libraryReadable: Bool {
        let s = PHPhotoLibrary.authorizationStatus(for: .readWrite)
        return s == .authorized || s == .limited
    }

    /// IOS-09: a run with fewer new assets than this, on a phone that has
    /// synced before (AssetSyncCache), doesn't download the listing of
    /// /ios/<deviceId>/ - every photo this phone ever synced, megabytes
    /// through the bridge for a big library - to check them against: each
    /// goes by its hash (HasFile, LinkFile), and a name another file holds
    /// is found by the device's duplicated_file. As Android's.
    static let listingFrom = 100

    /// Pure: whether a run of `assets` lists the folder first.
    static func needsListing(assets: Int, cacheEmpty: Bool) -> Bool {
        assets > 0 && (cacheEmpty || assets >= listingFrom)
    }

    /// Pure, IOS-01: whether "Wi-Fi only" holds the sync on this path.
    static func waitsForWifi(wifiOnly: Bool, expensive: Bool) -> Bool {
        wifiOnly && expensive
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
            throw NSError(domain: "PhotoExport", code: -10, userInfo: [NSLocalizedDescriptionKey: "No asset resource"]) // i18n-ignore: an internal error of the photo export, logged by the sync (a detail after a translated sentence at most)
        }

        // Temp file to stream into
        let tmpDir = FileManager.default.temporaryDirectory
        let ext = (res.originalFilename as NSString).pathExtension
        let tmpURL = tmpDir.appendingPathComponent(UUID().uuidString).appendingPathExtension(ext.isEmpty ? "bin" : ext)
        try? FileManager.default.removeItem(at: tmpURL)

        // Open an output stream (constant memory usage)
        guard let out = OutputStream(url: tmpURL, append: false) else {
            throw NSError(domain: "PhotoExport", code: -11, userInfo: [NSLocalizedDescriptionKey: "Cannot open output stream"]) // i18n-ignore: an internal error of the photo export, logged by the sync (a detail after a translated sentence at most)
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
                                userInfo: [NSLocalizedDescriptionKey: "Failed writing to stream"] // i18n-ignore: an internal error of the photo export, logged by the sync (a detail after a translated sentence at most)
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

        return (tmpURL, res.originalFilename, Self.mime(of: res.originalFilename))
    }

    /// The MIME type a resource's file name says.
    private static func mime(of filename: String) -> String {
        let ext = (filename as NSString).pathExtension
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
        let lower = filename.lowercased()
        if lower.hasSuffix(".png")           { return "image/png" }
        if lower.hasSuffix(".jpg") || lower.hasSuffix(".jpeg") { return "image/jpeg" }
        if lower.hasSuffix(".heic")          { return "image/heic" }
        if lower.hasSuffix(".mov")           { return "video/quicktime" }
        return "application/octet-stream"
    }

    /// exportAssetToTempFile for the sync (IOS-04): waits without holding a
    /// thread while PhotoKit reads - an iCloud download can take minutes,
    /// and three of those blocked three of the pool's few threads - and a
    /// run that is stopped (a BG task's end, the app's background time,
    /// Log Out) cancels the request at once instead of after the download.
    func exportAssetToTempFileCancellable(_ asset: PHAsset, allowNetwork: Bool) async throws -> (url: URL, filename: String, mime: String) {
        let resources = PHAssetResource.assetResources(for: asset)
        guard let res = resources.first(where: { $0.type == .photo || $0.type == .fullSizePhoto || $0.type == .video }) ?? resources.first
        else {
            throw NSError(domain: "PhotoExport", code: -10, userInfo: [NSLocalizedDescriptionKey: "No asset resource"]) // i18n-ignore: an internal error of the photo export, logged by the sync (a detail after a translated sentence at most)
        }
        let ext = (res.originalFilename as NSString).pathExtension
        let tmpURL = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString).appendingPathExtension(ext.isEmpty ? "bin" : ext)
        guard let out = OutputStream(url: tmpURL, append: false) else {
            throw NSError(domain: "PhotoExport", code: -11, userInfo: [NSLocalizedDescriptionKey: "Cannot open output stream"]) // i18n-ignore: an internal error of the photo export, logged by the sync (a detail after a translated sentence at most)
        }
        let opts = PHAssetResourceRequestOptions()
        opts.isNetworkAccessAllowed = allowNetwork
        // The stream is the request's: PhotoKit may still hand over a piece
        // after a cancelled export returned, and it is written, or dropped
        // once closed, under the request's lock.
        let request = ExportRequest(out)
        defer { request.close() }
        do {
            try await withTaskCancellationHandler {
                try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
                    request.start(cont)
                    let id = PHAssetResourceManager.default().requestData(
                        for: res,
                        options: opts,
                        dataReceivedHandler: { chunk in request.write(chunk) },
                        completionHandler: { err in request.finish(err) }
                    )
                    request.started(id)
                }
            } onCancel: {
                request.cancel()
            }
        } catch {
            try? FileManager.default.removeItem(at: tmpURL)
            throw error
        }
        return (tmpURL, res.originalFilename, Self.mime(of: res.originalFilename))
    }

    /// readData for the sync: the export above, cancellable.
    func readDataCancellable(for asset: PHAsset, allowNetwork: Bool,
                             maxBytes: Int64 = 1024 * 1024 * 1024) async throws -> (data: Data, filename: String, mime: String) {
        let (url, filename, mime) = try await exportAssetToTempFileCancellable(asset, allowNetwork: allowNetwork)
        defer { try? FileManager.default.removeItem(at: url) }
        let attrs = try FileManager.default.attributesOfItem(atPath: url.path)
        let size = (attrs[.size] as? NSNumber)?.int64Value ?? 0
        guard size <= maxBytes else {
            throw NSError(domain: "PhotoExport", code: -20, userInfo: [NSLocalizedDescriptionKey: "AssetTooLargeForMemory: \(size) bytes"]) // i18n-ignore: an internal error of the photo export, logged by the sync (a detail after a translated sentence at most)
        }
        return (try Data(contentsOf: url, options: .mappedIfSafe), filename, mime)
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

    /// With limited access, everything the app can see - only what the
    /// owner picked - videos included, by local identifier.
    private func visibleIDs() -> Set<String> {
        Set(fetchNewAssets(includeVideos: true, since: nil).map(\.localIdentifier))
    }

    /// A one-off pass's assets, oldest first: all of an album's, whatever
    /// their date, or everything added after a date (nil: the library).
    private func fetchOneOff(_ pass: PhotoSources.OneOff, includeVideos: Bool) -> [PHAsset] {
        switch pass {
        case .since(let date):
            return fetchNewAssets(includeVideos: includeVideos, since: date)
        case .album(let id):
            // Gone since it was turned on: nothing to send.
            guard let album = PHAssetCollection.fetchAssetCollections(withLocalIdentifiers: [id], options: nil).firstObject else { return [] }
            let opts = PHFetchOptions()
            if !includeVideos {
                opts.predicate = NSPredicate(format: "mediaType == %d", PHAssetMediaType.image.rawValue)
            }
            opts.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: true)]
            var out: [PHAsset] = []
            PHAsset.fetchAssets(in: album, options: opts).enumerateObjects { a, _, _ in out.append(a) }
            return out
        }
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
        /// The device has no room (storage_full), with its words: on the
        /// retry list, and the run stops.
        case full(String, String)
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
                          userInfo: [NSLocalizedDescriptionKey: "AssetTooLargeForMemory: \(size) bytes"]) // i18n-ignore: an internal error of the photo export, logged by the sync (a detail after a translated sentence at most)
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
        running?.cancel()
        waitingForWifi = false
        fullSince = nil
    }

    /// A sync run, from in front: it starts here unless one is going - that
    /// one then goes through once more for whatever this was asked for
    /// (IOS-12). The run is a Task of its own so Log Out (cancel()) and the
    /// end of the app's background time (stopRun()) can stop it; awaiting a
    /// Task's value doesn't pass the caller's cancellation on, so the
    /// handler in syncUnlessRunning does.
    func runForeground() async throws {
        let time = await BackgroundTime.begin { [weak self] in self?.stopRun() }
        defer { time.end() }
        guard try await syncUnlessRunning(join: false) else { return }
        // Free up space, once this run has synced what it could - after the
        // sync's own flag is down, so photos taken while iOS asks about the
        // deletion still sync. Only in front, and not more than once a day.
        await FreeUpSpace.shared.afterSync()
    }

    /// IOS-02: a BGProcessingTask's run. One going already (a photo taken,
    /// the app just in front) is waited for rather than reported finished at
    /// once - iOS then suspended the app with that run barely started - and
    /// goes through once more for whatever the task came for; with none
    /// going, one starts. The task's expiration cancels this, and the run
    /// with it.
    func runInBackground() async throws {
        _ = try await syncUnlessRunning(join: true)
    }

    /// The run: true when it started here, false when one was going (which
    /// goes through once more at its end, and was waited for with `join`).
    private func syncUnlessRunning(join: Bool) async throws -> Bool {
        guard let run = startRun(join: join, { [weak self] gen in try await self?.runPasses(gen: gen) }) else {
            print("Sync already running: it goes through once more at its end")
            return false
        }
        try await withTaskCancellationHandler {
            try await run.task.value
        } onCancel: {
            run.task.cancel()
        }
        return run.started
    }

    /// A run's passes: one, then another for each trigger that came while
    /// one went (rerunWanted), as long as each went through. The flag is
    /// down before the run's Task ends, so whoever waits on it finds the
    /// sync idle.
    private func runPasses(gen: Int) async throws {
        defer { endSync() }
        while true {
            let end = try await onePass(gen: gen)
            guard end == .done, !Task.isCancelled, takeRerun() else { return }
            print("Sync once more: asked for while it ran")
        }
    }

    /// One pass: what holds it up first, then the sync itself (syncOnce).
    private func onePass(gen: Int) async throws -> PassEnd {
        let secrets = SecretsStore.loadOrCreate()
        // IOS-07: no device yet (Welcome): nothing, and no question about
        // Photos over it.
        guard secrets.isConfigured else { return .skipped }
        await accessChanged(gen: gen)
        // Signed in but not yet told what to sync (SyncChoice): nothing, the
        // background task's runs included, until the sheet is answered.
        guard !SyncChoice.pending else {
            print("Sync waits for the sync choice")
            return .skipped
        }
        // Never asked here: the sync choice's Start Sync asks for access, and
        // Settings has the button, so no run - in the background, over
        // Welcome - brings the question up.
        guard Self.libraryReadable else {
            print("Sync skipped: no access to the photo library")
            return .skipped
        }
        observeLibraryIfAllowed()
        // IOS-01: "Wi-Fi only" on mobile data: nothing until the phone is on
        // Wi-Fi again (watchCost starts a run then).
        if await Self.holdsForWifi(secrets.wifiOnly) {
            print("Sync waits for Wi-Fi")
            finish(.waitingForWifi, gen: gen)
            return .waitingForWifi
        }
        // The device said it was full a short while ago: not read and sent
        // to be refused again (Sync All or Sync From Now try at once).
        if let words = fullWordsIfStillFull() {
            print("Sync waits: the device was full a short while ago")
            return .deviceFull(words)
        }
        // Issue #190: no route switch while a sync runs - except between its
        // chunks (IOS-10, syncOnce).
        TransferActivity.shared.begin()
        defer { TransferActivity.shared.end() }
        let end = try await syncOnce(gen: gen, secrets: secrets)
        finish(end, gen: gen)
        return end
    }

    /// What a pass that ended leaves: the line in Settings' Uploads, and
    /// what starts or holds the next run.
    private func finish(_ end: PassEnd, gen: Int) {
        switch end {
        case .waitingForWifi:
            setWaitingForWifi(true)
            ifLive(gen) { UploadModel.shared.halted(.waitingForWifi) }
        case .deviceFull(let words):
            setFull(Date(), words: words)
            ifLive(gen) { UploadModel.shared.halted(.deviceFull(words)) }
        case .done:
            setFull(nil, words: "")
            ifLive(gen) { UploadModel.shared.halted(nil) }
        case .cutShort:
            ifLive(gen) { UploadModel.shared.halted(nil) }
        case .skipped:
            break
        }
    }

    /// "Wi-Fi only" with the phone on mobile data or a hotspot. The network
    /// watch starts with the connection, and its first path comes right
    /// after.
    private static func holdsForWifi(_ wifiOnly: Bool) async -> Bool {
        guard wifiOnly else { return false }
        await MainActor.run { _ = OTCConnection.shared }
        await NetworkWatch.shared.waitForPath()
        return waitsForWifi(wifiOnly: true, expensive: NetworkWatch.shared.isExpensive)
    }

    /// What turning access to the library from limited to full needs.
    enum AccessStep: Equatable {
        case none
        /// The catch-up pass, from the floor the owner chose.
        case catchUp
        /// The floor is unknown (chosen before this version): the Sync
        /// Your Photos question again, whose answer sets it.
        case askAgain
    }

    /// Pure: what a run does about access. Only full access after limited
    /// access needs anything - the photos the app couldn't see are behind
    /// the watermark. Access taken away is not full access.
    static func accessStep(limited: Bool, full: Bool, wasLimited: Bool, floorKnown: Bool) -> AccessStep {
        guard full && wasLimited else { return .none }
        return floorKnown ? .catchUp : .askAgain
    }

    /// Before each run, the choice included: records limited access, and
    /// when access is full again after it, queues the catch-up pass or asks
    /// the Sync Your Photos question again (which holds the run up, as
    /// after a sign-in).
    private func accessChanged(gen: Int) async {
        let status = PHPhotoLibrary.authorizationStatus(for: .readWrite)
        var step = AccessStep.none
        ifLive(gen) {
            if status == .limited {
                if !state.accessWasLimited { state.accessWasLimited = true }
                return
            }
            step = Self.accessStep(limited: false, full: status == .authorized,
                                   wasLimited: state.accessWasLimited, floorKnown: state.floor != nil)
            guard step != .none else { return }
            state.accessWasLimited = false
            state.removeLimitedSeen()
            if step == .catchUp { state.queueBackfill(PhotoSources.catchUp) }
        }
        if step == .askAgain {
            print("Full photo access after limited access: asking what to sync again")
            await MainActor.run { SyncChoice.ask() }
        }
    }

    private func syncOnce(gen: Int, secrets: SecretsStore) async throws -> PassEnd {
        let ws = OTCConnection.shared
        try await ws.ensureConnected()

        // This was never actually wired up before — the "Sync from iCloud"
        // toggle changed a setting nothing read, so turning it off had no
        // effect on the running sync at all. Captured once, as a plain
        // Bool, rather than passing `secrets` itself into the concurrent
        // tasks below.
        let allowICloudDownloads = secrets.downloadFromiCloud
        // Limited access: the app sees only the photos the owner picked.
        let limited = PHPhotoLibrary.authorizationStatus(for: .readWrite) == .limited
        // Where the change history goes on from next run, taken before
        // anything is fetched: what lands during this run is the next one's.
        let history = PhotoKitHistory()
        let nextToken = history.currentToken

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
        let fresh = fetchNewAssets(includeVideos: secrets.includeVideos, since: last)
        let freshIDs = Set(fresh.map(\.localIdentifier))
        // A4: what this app saved into the library (the viewer's Save):
        // copies of what the device already holds, never sent back to it as
        // this phone's photos (nor deleted by Free Up Space).
        let saved = SavedToPhone.shared.ids()

        // The photos in the albums turned off (Settings > Sync Options >
        // Albums): read once, and only when there is something to leave
        // out - with none turned off, nothing is read.
        var skippedCache: Set<String>?
        func skippedIDs() -> Set<String> {
            if let skippedCache { return skippedCache }
            let ids = PhotoSources.skippedAssetIDs(secrets.skippedAlbums)
            skippedCache = ids
            return ids
        }

        // Photos added to the library since the last run with a date the
        // watermark is already past (PhotoHistory.swift): onto the retry
        // list. When the history can't be read, the catch-up pass from the
        // floor instead - not while access is limited, where what the owner
        // picks is found below.
        let floor = state.floor
        let plan = Self.readHistory(history, token: state.changeToken, watermark: last, skipped: skippedIDs,
                                    floorKnown: floor != nil && !limited)
        ifLive(gen, epoch: epoch) {
            AssetSyncCache.shared.markPending(plan.markPending.filter { !saved.contains($0) }, iCloud: false)
            if plan.catchUp { state.queueBackfill(PhotoSources.catchUp) }
            state.changeToken = nextToken
        }
        if !plan.markPending.isEmpty { print("\(plan.markPending.count) assets added with an older date since the last run") }
        if plan.catchUp { print("Photo change history unavailable: catching up from the floor") }

        // Limited access: photos the owner picked since the last run may be
        // older than the watermark. What is visible is only what was picked,
        // so the whole set is compared with the last run's; the first time
        // it is only recorded.
        if limited {
            let visible = visibleIDs()
            let seen = state.limitedSeen()
            let picked = PhotoSources.newlyVisible(seen: seen, visible: visible).subtracting(freshIDs).subtracting(saved)
            ifLive(gen, epoch: epoch) {
                AssetSyncCache.shared.markPending(picked.sorted(), iCloud: false)
                if seen != visible { state.setLimitedSeen(visible) }
            }
            if !picked.isEmpty { print("\(picked.count) assets picked since the last run") }
        }

        // Assets earlier runs couldn't finish, which the watermark has
        // already moved past: first, unless the new fetch has them anyway.
        let pendingIDs = AssetSyncCache.shared.pending(includeICloud: allowICloudDownloads)
        var retried: [PHAsset] = []
        if !pendingIDs.isEmpty {
            let found = fetchAssets(localIdentifiers: pendingIDs)
            let foundIDs = Set(found.map(\.localIdentifier))
            // Gone from the library, or saved here from the device: off the
            // list.
            let gone = pendingIDs.filter { !foundIDs.contains($0) || saved.contains($0) }
            ifLive(gen) { AssetSyncCache.shared.resolve(gone) }
            retried = found.filter {
                !saved.contains($0.localIdentifier) && !freshIDs.contains($0.localIdentifier) && (secrets.includeVideos || $0.mediaType == .image)
            }
            print("Retrying \(retried.count) assets from earlier runs")
        }

        // One-off passes: albums turned back on, and the catch-up. No date
        // limit; what the device already has is found below and not sent
        // again.
        let taken = state.backfill
        var oneOff: [PHAsset] = []
        for pass in PhotoSources.oneOffs(taken, floor: floor) {
            oneOff += fetchOneOff(pass, includeVideos: secrets.includeVideos).filter { !saved.contains($0.localIdentifier) }
        }
        if !taken.isEmpty { print("One-off passes over \(taken.count) sources: \(oneOff.count) assets") }

        // New, retried, then one-off (PhotoSources.runList); none in an album
        // turned off, and none the app saved here (`saved`, left out as
        // skipped: a new one doesn't hold the watermark back). A retry list
        // entry left out stays on the list: it goes once its album is on
        // again or it is taken out of it. Only the new ones move the
        // watermark.
        var byID: [String: PHAsset] = [:]
        for asset in retried + oneOff + fresh { byID[asset.localIdentifier] = asset }
        let skipped = (secrets.skippedAlbums.isEmpty || byID.isEmpty ? [] : skippedIDs()).union(saved.intersection(freshIDs))
        let list = PhotoSources.runList(retried: retried.map(\.localIdentifier), oneOff: oneOff.map(\.localIdentifier),
                                        fresh: fresh.map(\.localIdentifier), skipped: skipped)
        let assets = list.ids.compactMap { byID[$0] }
        let retriedIDs = list.exempt
        if !skipped.isEmpty { print("Leaving out \(byID.count - assets.count) assets in albums turned off or saved from the device") }
        ifLive(gen) { UploadModel.shared.begin(total: assets.count) }

        let targetPath = "/ios/\(secrets.deviceId)/"
        // Get a list of all the files for the target path - only for a big
        // run, or a phone that has synced nothing yet (IOS-09,
        // needsListing). Every library change and every return to the
        // foreground starts a run, almost always with a photo or two, and
        // the listing holds every photo this phone ever synced (tens of MB
        // through the bridge for a big library); a small run goes by each
        // asset's hash and the device's duplicated_file instead.
        // With each file's hash and creation time: a path alone can't tell
        // this asset from another one that had the same name.
        var knownFiles: [String: (hash: String, created: Date?)] = [:]
        if Self.needsListing(assets: assets.count, cacheEmpty: AssetSyncCache.shared.isEmpty) {
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
        // Stopped because the device is gone.
        var cutShort = false
        // Stopped for Wi-Fi only on mobile data, or the device full.
        var halt: PassEnd?
        let chunks = assets.chunked(into: Self.cMaxConcurrentUploads)
        for (ci, chunk) in chunks.enumerated() {
            // Issue #70: a BGProcessingTask's expiration and Log Out cancel
            // this run. Checked between chunks; uploads already in flight
            // stop at their next 4 MiB chunk (uploadChunked's
            // checkCancellation).
            if Task.isCancelled { stopped = true; break }

            // IOS-10: the run lets go of its hold on the route for a moment,
            // so a route check put off while it ran (the phone came home) is
            // made now: the rest goes over the home network rather than out
            // to the bridge and back. Nothing in flight here - the last
            // chunk's uploads are all done.
            TransferActivity.shared.end()
            await OTCConnection.shared.routeBreak()
            TransferActivity.shared.begin()

            // IOS-01: Wi-Fi only, and the phone is on mobile data now:
            // stopped here, the watermark before what wasn't sent; back on
            // Wi-Fi, a run goes on from it.
            if Self.waitsForWifi(wifiOnly: SecretsStore.wifiOnlyNow, expensive: NetworkWatch.shared.isExpensive) {
                print("Off Wi-Fi with Wi-Fi only on: the sync stops here")
                halt = .waitingForWifi
                break
            }

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
                        // Reads the asset's bytes, failures marked as such;
                        // a run that is stopped stops the read too.
                        func read() async throws -> Data {
                            do { return try await self.readDataCancellable(for: asset, allowNetwork: allowICloudDownloads).data }
                            catch { throw ReadFailure(underlying: error) }
                        }
                        do {
                            // Cheap: local Photos metadata only, no
                            // download - lets path (and therefore
                            // knownFiles/the asset cache below) be checked
                            // before ever touching the expensive part.
                            guard let rawName = self.resourceFilename(for: asset) else {
                                throw NSError(domain: "PhotoExport", code: -10, userInfo: [NSLocalizedDescriptionKey: "No asset resource"]) // i18n-ignore: an internal error of the photo export, logged by the sync (a detail after a translated sentence at most)
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
                                let readBytes = try await read()
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
                                let readBytes = try await read()
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
                                    throw NSError(domain: "PhotoExport", code: -13, userInfo: [NSLocalizedDescriptionKey: "No data to upload"]) // i18n-ignore: an internal error of the photo export, logged by the sync (a detail after a translated sentence at most)
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
                            if ErrorCodes.isStorageFull(code: resp.errorCode) { return .full(id, resp.errorMessage) }
                            print("Upload failed:", resp.errorMessage)
                            return .retry(id)
                        } catch {
                            let cause = (error as? ReadFailure)?.underlying ?? error
                            let ns = cause as NSError
                            if ns.domain == "PhotoExport" && (ns.code == -10 || ns.code == -20) {
                                print("Skipping:", cause)
                                return .skip(id)
                            }
                            // The device has no room (storage_full): the run
                            // stops on it rather than reading asset after
                            // asset to have each refused.
                            if ErrorCodes.isStorageFull(code: ErrorCodes.code(of: cause)) {
                                return .full(id, cause.localizedDescription)
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
            var fullWords: String?
            for outcome in outcomes {
                switch outcome {
                case .done(let id), .skip(let id): finished.append(id)
                case .retry(let id): failed.append(id)
                case .retryWhenICloud(let id): failedICloud.append(id)
                case .full(let id, let words):
                    failed.append(id)
                    fullWords = words
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

            // The device is full: nothing more until there is room. What it
            // refused is on the retry list.
            if let fullWords {
                print("The device is full: the sync stops here")
                halt = .deviceFull(fullWords)
                break
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
                    cutShort = true
                    break
                }
            }
        }

        // Every chunk went (what failed is on the retry list): the one-off
        // passes taken are done - those queued meanwhile stay, and so does
        // one this run left out, its album turned back on after the run read
        // the skip list - and new photos left out as skipped no longer hold
        // the watermark back, or every run would fetch them again. A Sync
        // From Now pressed meanwhile wins.
        if !stopped && !cutShort && halt == nil {
            let newestFresh = list.droppedFresh ? fresh.compactMap(\.creationDate).max() : nil
            ifLive(gen, epoch: epoch) {
                if !taken.isEmpty {
                    state.backfill = PhotoSources.afterPass(stored: state.backfill, taken: taken,
                                                            skippedInRun: secrets.skippedAlbums, finished: true)
                }
                let stored = UserDefaults.standard.object(forKey: "lastSyncDate") as? Date
                if let mark = PhotoSources.watermarkPastSkipped(newest: newestFresh, runStart: runStart, stored: stored) {
                    UserDefaults.standard.set(mark, forKey: "lastSyncDate")
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
        if cutShort { return .cutShort }
        return halt ?? .done
    }
}

extension PhotoSync {
    /// What the sync keeps besides the watermark (lastSyncDate) and the
    /// retry list (AssetSyncCache): in UserDefaults and Application
    /// Support, which Log Out wipes. Not locked: PhotoSync writes it under
    /// its own lock.
    struct State {
        let defaults: UserDefaults
        /// Where limited_seen.json is kept.
        let directory: URL

        static let standard = State(
            defaults: .standard,
            directory: FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0])

        static let backfillKey = "backfillAlbums"
        static let floorKey = "syncFloorDate"
        static let changeTokenKey = "photoChangeToken"
        static let wasLimitedKey = "accessWasLimited"

        /// One-off passes still to make: the ids of albums turned back on,
        /// and PhotoSources.catchUp.
        var backfill: [String] {
            get { defaults.stringArray(forKey: Self.backfillKey) ?? [] }
            nonmutating set {
                if newValue.isEmpty { defaults.removeObject(forKey: Self.backfillKey) } else { defaults.set(newValue, forKey: Self.backfillKey) }
            }
        }

        func queueBackfill(_ entry: String) {
            let queued = backfill
            if !queued.contains(entry) { backfill = queued + [entry] }
        }

        /// The point the owner chose to sync from: the moment Sync From Now
        /// (or "Only new ones") was pressed, or Date.distantPast for
        /// everything (Sync All). nil on a phone that hasn't chosen since
        /// this was kept. Only the catch-up pass reads it.
        var floor: Date? {
            get { defaults.object(forKey: Self.floorKey) as? Date }
            nonmutating set { defaults.set(newValue, forKey: Self.floorKey) }
        }

        /// Where the change history goes on from (PhotoHistory.swift): an
        /// archived PHPersistentChangeToken.
        var changeToken: Data? {
            get { defaults.data(forKey: Self.changeTokenKey) }
            nonmutating set { defaults.set(newValue, forKey: Self.changeTokenKey) }
        }

        /// The last run saw limited access.
        var accessWasLimited: Bool {
            get { defaults.bool(forKey: Self.wasLimitedKey) }
            nonmutating set {
                if newValue { defaults.set(true, forKey: Self.wasLimitedKey) } else { defaults.removeObject(forKey: Self.wasLimitedKey) }
            }
        }

        private var limitedSeenURL: URL { directory.appendingPathComponent("limited_seen.json") }

        /// With limited access, the photos visible at the last run; nil when
        /// none was recorded.
        func limitedSeen() -> Set<String>? {
            guard let data = try? Data(contentsOf: limitedSeenURL),
                  let ids = try? JSONDecoder().decode([String].self, from: data) else { return nil }
            return Set(ids)
        }

        func setLimitedSeen(_ ids: Set<String>) {
            guard let data = try? JSONEncoder().encode(ids.sorted()) else { return }
            try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            try? data.write(to: limitedSeenURL, options: .atomic)
        }

        func removeLimitedSeen() {
            try? FileManager.default.removeItem(at: limitedSeenURL)
        }
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

/// One PhotoKit read of the sync's export (exportAssetToTempFileCancellable):
/// resumes its caller exactly once - with PhotoKit's completion, or at once
/// when cancelled, whether that comes before or after PhotoKit gave the
/// request its id.
private final class ExportRequest: @unchecked Sendable {
    private let lock = NSLock()
    private let out: OutputStream
    private var closed = false
    private var cont: CheckedContinuation<Void, Error>?
    private var id: PHAssetResourceDataRequestID?
    private var cancelled = false
    private var writeError: Error?

    init(_ out: OutputStream) {
        self.out = out
        out.open()
    }

    /// A piece PhotoKit read, written whole; after a failure, or once the
    /// export is over, dropped.
    func write(_ chunk: Data) {
        lock.withLock {
            guard !closed, writeError == nil else { return }
            chunk.withUnsafeBytes { rawPtr in
                guard let base = rawPtr.bindMemory(to: UInt8.self).baseAddress else { return }
                var left = chunk.count
                var offset = 0
                while left > 0 {
                    let w = out.write(base.advanced(by: offset), maxLength: left)
                    if w <= 0 {
                        writeError = out.streamError ?? NSError(domain: "PhotoExport", code: -12, userInfo: [NSLocalizedDescriptionKey: "Failed writing to stream"]) // i18n-ignore: an internal error of the photo export, logged by the sync (a detail after a translated sentence at most)
                        return
                    }
                    left -= w
                    offset += w
                }
            }
        }
    }

    func close() {
        lock.withLock {
            guard !closed else { return }
            closed = true
            out.close()
        }
    }

    func start(_ c: CheckedContinuation<Void, Error>) {
        lock.withLock { cont = c }
    }

    func started(_ requestID: PHAssetResourceDataRequestID) {
        let cancelNow = lock.withLock { () -> Bool in
            id = requestID
            return cancelled
        }
        if cancelNow {
            PHAssetResourceManager.default().cancelDataRequest(requestID)
            finish(CancellationError())
        }
    }

    func cancel() {
        let requestID = lock.withLock { () -> PHAssetResourceDataRequestID? in
            cancelled = true
            return id
        }
        if let requestID { PHAssetResourceManager.default().cancelDataRequest(requestID) }
        finish(CancellationError())
    }

    func finish(_ error: Error?) {
        let (c, outcome): (CheckedContinuation<Void, Error>?, Error?) = lock.withLock {
            defer { cont = nil }
            return (cont, writeError ?? error)
        }
        guard let c else { return }
        if let outcome { c.resume(throwing: outcome) } else { c.resume() }
    }
}

/// The time iOS gives an app to finish what it was doing once it leaves the
/// screen (beginBackgroundTask, about half a minute), taken by a sync run
/// started in front (IOS-04): a run in its last chunk then finishes it
/// rather than being frozen half way, and when the time runs out the run
/// stops as at a BG task's end - the watermark before what it didn't finish.
private final class BackgroundTime: @unchecked Sendable {
    private let lock = NSLock()
    private var id = UIBackgroundTaskIdentifier.invalid

    @MainActor
    static func begin(onExpiry: @escaping @Sendable () -> Void) -> BackgroundTime {
        let time = BackgroundTime()
        let id = UIApplication.shared.beginBackgroundTask(withName: "photo-sync") {
            onExpiry()
            // Within the handler: iOS ends an app that lets it run out.
            if let id = time.take() { UIApplication.shared.endBackgroundTask(id) }
        }
        time.lock.withLock { time.id = id }
        return time
    }

    /// The task's id, once: whoever ends it first.
    private func take() -> UIBackgroundTaskIdentifier? {
        lock.withLock {
            defer { id = .invalid }
            return id == .invalid ? nil : id
        }
    }

    func end() {
        guard let id = take() else { return }
        Task { @MainActor in UIApplication.shared.endBackgroundTask(id) }
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
