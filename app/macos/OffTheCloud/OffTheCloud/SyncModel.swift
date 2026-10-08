// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftUI
import Combine
import CryptoKit
import SwiftProtobuf
import AppKit   // <- for NSOpenPanel
import CoreServices // <- for FSEventStreamEventFlags constants
import Network
import os

/// The unified log, so what a reconcile decided can be read back with
/// `log show --predicate 'subsystem == "cloud.off-the.OffTheCloud"'`
/// rather than lost in a print() nobody sees from a menu bar app.
private let syncLog = Logger(subsystem: "cloud.off-the.OffTheCloud", category: "sync")

@MainActor
final class SyncModel: ObservableObject {
    // Issue #37: shared so AppDelegate can bind sync at launch, before
    // (and independent of) the menu bar popover ever being opened.
    static let shared = SyncModel()

    enum FolderState: Equatable {
        // currentFile is shown alongside the percentage so a folder
        // dominated by a couple of huge files (e.g. drone video) doesn't
        // look stuck at "0%" for the minutes it can genuinely take to
        // transfer just the first one — issue #37's original complaint.
        case scanning(progress: Double, currentFile: String? = nil)
        case watching
        case error(String)
    }

    struct TrackedFolder: Identifiable, Hashable {
        let id: UUID
        var url: URL
        var state: FolderState = .scanning(progress: 0)

        static func == (lhs: TrackedFolder, rhs: TrackedFolder) -> Bool { lhs.id == rhs.id }
        func hash(into hasher: inout Hasher) { hasher.combine(id) }
    }

    // Issue #47: a remote directory on the device, linked to a local one
    // and kept in **two-way** sync — a change on either side (add, edit,
    // delete) propagates to the other, using the remote copy as the hub
    // multiple devices/apps all converge through (device A uploads, device
    // B's next reconcile picks it up from the remote; same for deletes).
    // reconcileRemoteFolder does a three-way merge against
    // lastSyncedByRemoteFolder (the baseline from the last successful
    // pass) to tell "which side actually changed" apart from "these just
    // already agree" — see that method's doc comment for the full
    // reasoning, including the conflict tie-break and the guard against a
    // deleted-local-root being mistaken for "delete everything remotely".
    // Local changes get a FolderWatcher like TrackedFolder does; remote
    // changes still have no push mechanism, so periodic polling
    // (remoteReconcileInterval) remains the only way to notice those.
    struct RemoteFolder: Identifiable, Hashable {
        let id: UUID
        var remotePath: String
        var localURL: URL
        var state: FolderState = .scanning(progress: 0)

        static func == (lhs: RemoteFolder, rhs: RemoteFolder) -> Bool { lhs.id == rhs.id }
        func hash(into hasher: inout Hasher) { hasher.combine(id) }
    }

    // A remote directory entry, as shown in the remote folder picker
    // (RemoteFolderPickerView) - not a wire type itself, just what
    // listRemoteDirectory maps Msg_File into for the UI to walk.
    struct RemoteEntry: Identifiable, Hashable {
        let id: String // full remote path — unique within one listing
        let name: String
        let path: String
        let isDir: Bool
    }

    // ---- PERSISTENCE TYPES/KEYS ----
    private struct StoredFolder: Codable {
        let id: UUID
        let bookmark: Data
        /// Issue #192: a request not yet acknowledged by the device - true
        /// to keep the folder out of Images, false to show it there again;
        /// nil (and absent from older data) when there is nothing to send.
        var outOfImages: Bool? = nil
    }
    private let bookmarksKey = "sync.folders.bookmarks"

    private struct StoredRemoteFolder: Codable {
        let id: UUID
        let remotePath: String
        let bookmark: Data
        /// Issue #192: as StoredFolder's. Only a folder added from this Mac
        /// is asked for it; one synced from the device keeps what it has
        /// there, but a request an older version stored is still sent once.
        var outOfImages: Bool? = nil
        /// Issue #132: the same kind of request for the device folder to be
        /// made upload only (the device keeps every older version of a file
        /// and refuses deletes there), asked when a two-way folder is added
        /// from this Mac. nil (and absent from older data) when there is
        /// nothing to send. A backup never has it: it is made upload only
        /// at every start (markUploadOnly).
        var uploadOnly: Bool? = nil
    }
    private let remoteFoldersKey = "sync.remoteFolders.bookmarks"

    // Issue #37: FSEvents does the real-time work; this reconcile interval
    // is only a safety net for whatever it might have missed (the app
    // wasn't running, an event got dropped, etc.) — not the primary sync
    // mechanism the old 60-second full-rescan loop was.
    private static let reconcileInterval: Duration = .seconds(600)
    // Rapid-fire FSEvents for the same path (e.g. an app doing several
    // writes while saving) are coalesced by waiting this long after the
    // last event before actually reading/uploading the file.
    private static let debounceInterval: Duration = .seconds(1)
    // A synced folder only shows progress once a pass has been working
    // this long: a pass that sends one changed file (a folder whose files
    // are rewritten every few seconds) flashed "99%" and back to "Synced".
    private static let quietPassDelay: Duration = .milliseconds(1500)
    // A folder that fails to reconcile (a dropped connection mid-request,
    // a transient server error, etc.) does get retried by the periodic
    // safety-net loop above — but waiting up to 10 minutes for that, with
    // no sign a retry is even coming, is what made an error look
    // permanent/stuck. This is a much shorter, dedicated retry just for
    // folders currently in .error.
    private static let errorRetryInterval: Duration = .seconds(30)
    /// Two-way folders: more local deletions than this in one pass (and a
    /// quarter of the folder) mean the device lost the files, not that the
    /// owner deleted them - see reconcileRemoteFolder.
    private static let massDeleteMin = 20
    // Issue #47: polling is the *only* way to notice a remote-side change
    // (no watcher possible there), so this needs to be short enough to
    // feel like "kept in sync" rather than the 10-minute upload-side
    // safety-net interval, which only ever has to catch what FSEvents
    // missed. Local-side changes also go through this same reconcile, but
    // get there near-instantly via a FolderWatcher + debounce instead of
    // waiting for the next poll — see startRemoteWatcher.
    private static let remoteReconcileInterval: Duration = .seconds(60)

    @Published var folders: [TrackedFolder] = []
    @Published var remoteFolders: [RemoteFolder] = []
    @Published var overallStatus: String = "Not connected"
    /// Issue #190: how the app reached the device, while connected.
    @Published var route: ConnectionRoute?

    // Issue #69: the device's RAID, for the menu bar icon. Polled while
    // connected (see pollRaidStatus); .unknown until the first answer and
    // whenever the connection is down, so a stale "all good" is never
    // shown for a device we can't currently hear from.
    @Published var raidHealth: RaidHealth = .unknown
    /// The device's last status answer (storage, CPU, memory) - the bar
    /// and the load pop-up next to raidHealth. nil when unknown.
    @Published var deviceStatus: Msg_Status?
    /// Issue #183: a major or critical device update not installed yet
    /// (the status's update_alert), from the same poll. nil when none.
    @Published var updateAlert: Msg_UpdateAlert?
    private var raidPollTask: Task<Void, Never>?
    private var authRetryTask: Task<Void, Never>?
    // Every 10 seconds: a minute was too slow when someone is actually
    // watching the icon after pulling a drive. GetStatus is cheap on the
    // device (it reads /proc/mdstat and a few counters, no disk I/O of
    // note), so this costs nothing that shows.
    private static let raidPollInterval: Duration = .seconds(10)

    private let ws = WSClient()
    private var settings: SettingsStore?
    private var cancellables: Set<AnyCancellable> = []

    // Issue #190: a network change (another Wi-Fi, the Mac back home) or a
    // wake can make the home route available or take it away. The
    // monitor's first answer is only the starting point; after that,
    // changes are let settle for routeCheckDelay.
    private let pathMonitor = NWPathMonitor()
    private var pathKey: String?
    private var routeCheckTask: Task<Void, Never>?
    /// routeCheckTask is a network change's: a wake meanwhile mustn't turn
    /// it into a wake's, which does nothing while the route is home.
    private var pathCheckPending = false
    private var wakeObserver: NSObjectProtocol?
    private static let routeCheckDelay: Duration = .seconds(3)
    // On the bridge with a home endpoint, opening the popover (the Mac's
    // coming to the foreground) looks for the device at home, at most
    // once per homeProbeInterval. A home endpoint whose first try failed
    // is looked for again homeRetryDelay later, once nothing is in flight.
    private var lastHomeProbe: ContinuousClock.Instant?
    private var homeRetryTask: Task<Void, Never>?
    private static let homeProbeInterval: Duration = .seconds(120)
    private static let homeRetryDelay: Duration = .seconds(60)
    /// Listings the remote-folder picker is waiting for: a route switch
    /// would fail them with "Cancelled".
    private var listingsInFlight = 0

    private var folderWatchers: [UUID: FolderWatcher] = [:]
    // Last-known-synced hash per folder, keyed by the file's *remote* path
    // — the local cache of "what the device already has", refreshed by
    // reconcile() and kept current as changes are pushed incrementally.
    private var remoteHashesByFolder: [UUID: [String: String]] = [:]
    // Issue #47: three-way merge baseline for two-way RemoteFolder sync —
    // the hash each relative path had as of the *last successful*
    // reconcile, keyed by path relative to the folder's root (not a full
    // local/remote path, since both sides need to compare against the same
    // key). Comparing local's and remote's *current* hash against this
    // baseline is what tells apart "local changed", "remote changed",
    // "both changed (conflict)", and "already agree" — see
    // reconcileRemoteFolder for the full logic.
    private var lastSyncedByRemoteFolder: [UUID: [String: String]] = [:]
    // A watched backup's changed files: each waits for a quiet
    // debounceInterval, then one worker per folder sends them one at a
    // time. A task per file used to upload them all at once - an import
    // of 500 photos held 500 open files and 500 4 MiB chunks in flight.
    private final class ChangeQueue {
        /// Paths in the order they fall due. A path changed again is
        /// queued again, and only its latest entry counts.
        var order: [(path: String, due: ContinuousClock.Instant)] = []
        var head = 0
        var latest: [String: ContinuousClock.Instant] = [:]
    }
    private var changeQueues: [UUID: ChangeQueue] = [:]
    private var changeWorkers: [UUID: Task<Void, Never>] = [:]
    private var errorRetryTasks: [UUID: Task<Void, Never>] = [:]
    private var remoteErrorRetryTasks: [UUID: Task<Void, Never>] = [:]
    private var reconcileLoopStarted = false
    private var remoteReconcileLoopStarted = false

    // Issue #47: local-side changes to a RemoteFolder get picked up near-
    // instantly via FSEvents, same infra TrackedFolder already uses —
    // debounced per *folder* (not per file, unlike the upload-only side)
    // since a single reconcileRemoteFolder pass already handles a whole
    // batch of local changes correctly in one go.
    private var remoteFolderWatchers: [UUID: FolderWatcher] = [:]
    private var remoteFolderDebounce: [UUID: Task<Void, Never>] = [:]
    private var remoteFoldersBusy: Set<UUID> = []
    // The same for backups (reconcile()), and for a backup's first pass
    // (setupFolder), whose watcher only starts once that pass is over.
    private var foldersBusy: Set<UUID> = []
    private var foldersSettingUp: Set<UUID> = []
    // Backups the device linked now has confirmed as upload only. One it
    // didn't (the link dropped, a device not updated yet) is asked again
    // by its next pass, not only at the next launch. Forgotten when the
    // address or password changes, not on a reconnect. Each failure is
    // logged once, not every pass.
    private var uploadOnlyOK: Set<UUID> = []
    private var uploadOnlyErrors: [UUID: String] = [:]

    // Issue #192: folders kept out of Images (see "Folders kept out of
    // Images" below). What the device keeps out (ListOutOfImages, each path
    // with its trailing slash) and whether it can - nil until it has said,
    // forgotten when the address or password changes.
    @Published private(set) var outOfImagesFolders: [String]?
    @Published private(set) var outOfImagesSupported: Bool?
    /// Each folder's request not yet acknowledged (true keep out, false
    /// show again), persisted with the folder until the device has it -
    /// never sent again after that, not even at the next launch.
    @Published private(set) var outOfImagesPending: [UUID: Bool] = [:]
    /// The device's refusal of a folder's last request to show it (inside
    /// a folder kept out), on its row until its next request.
    @Published private(set) var outOfImagesNotes: [UUID: String] = [:]
    private var outOfImagesTasks: [UUID: Task<Void, Never>] = [:]
    private var outOfImagesErrors: [UUID: String] = [:]
    // Issue #132: a two-way folder's request to be made upload only, chosen
    // when it was added from this Mac - the same machinery (see "A folder's
    // requests" below): pending until the device has it, whether the device
    // can, its refusal, the send under way and the last error logged.
    @Published private(set) var uploadOnlyPending: [UUID: Bool] = [:]
    @Published private(set) var uploadOnlySupported: Bool?
    @Published private(set) var uploadOnlyNotes: [UUID: String] = [:]
    private var uploadOnlyTasks: [UUID: Task<Void, Never>] = [:]
    private var uploadOnlyRequestErrors: [UUID: String] = [:]
    /// remoteDeviceRoot(), as the last backup pass found it: a row asks for
    /// its device path on every redraw, and Host's name lookup can be slow.
    private var deviceRootForImages: String?

    // Local content hashes remembered per folder, keyed by full path and
    // validated by size + modification date: a two-way folder is
    // reconciled every minute, and hashing a 30 GB tree each time (as it
    // did) kept the disk and CPU busy for nothing. Only files that
    // changed since the last pass are read again.
    // The cache also lives on disk (Application Support/hashes/<folder
    // id>.json, same shape as otc-sync's), so a relaunch doesn't start
    // from nothing: without it every launch re-read the whole folder - 66
    // GB on the reference Mac, minutes of "Checking i/N" - just to confirm
    // nothing changed. Loaded the first time a folder is checked, written
    // at the end of a pass that changed it, removed with the folder.
    private struct HashEntry: Codable, Sendable { let size: Int; let modified: Date; let hash: String }
    private var localHashCache: [UUID: [String: HashEntry]] = [:]
    private var hashCacheLoaded: Set<UUID> = []
    // Reading a big cache (tens of MB of JSON) happens off the main actor,
    // once: whoever needs a folder's cache while it loads waits for that
    // same load.
    private var hashCacheLoads: [UUID: Task<[String: HashEntry], Never>] = [:]
    private var hashCacheDirty: Set<UUID> = []

    /// The content hash of `url`, from the cache when size and date still
    /// match, else freshly computed (off the main actor) and cached.
    private func cachedHash(for url: URL, folderId: UUID) async throws -> String {
        try await cachedHashEntry(for: url, folderId: folderId).hash
    }

    /// cachedHash, with the size and date the hash belongs to.
    private func cachedHashEntry(for url: URL, folderId: UUID) async throws -> HashEntry {
        let values = try url.resourceValues(forKeys: [.fileSizeKey, .contentModificationDateKey])
        let size = values.fileSize ?? -1
        let modified = values.contentModificationDate ?? .distantPast
        let key = url.standardizedFileURL.path
        await loadHashCacheIfNeeded(folderId)
        // A date survives the JSON round trip to the microsecond, not
        // the nanosecond, hence the tolerance rather than ==.
        if let hit = localHashCache[folderId]?[key], hit.size == size, abs(hit.modified.timeIntervalSince(modified)) < 0.001 {
            return hit
        }
        let hash = try await Task.detached(priority: .utility) { try Self.sha256Hex(of: url) }.value
        let entry = HashEntry(size: size, modified: modified, hash: hash)
        localHashCache[folderId, default: [:]][key] = entry
        hashCacheDirty.insert(folderId)
        return entry
    }

    /// One scanned file, looked at against the hash cache.
    private struct CacheCheck: Sendable {
        let url: URL
        /// nil when it couldn't be looked at (cachedHash then says why).
        let size: Int?
        let modified: Date?
        /// The cached hash, when size and date still match.
        let hit: String?
    }

    /// cachedHash's cache lookup for a whole scan, run off the main actor
    /// on a snapshot of the cache: a folder already in sync used to be
    /// checked a file at a time on the main actor, never suspending on a
    /// hit, which froze the popover and menu for seconds every pass. A
    /// miss goes through cachedHash afterwards, as before. `progress` is
    /// told a few times a second how far it got.
    private nonisolated static func checkAgainstCache(_ urls: [URL], cache: [String: HashEntry], progress: (Int, URL) async -> Void) async -> [CacheCheck] {
        var checks: [CacheCheck] = []
        checks.reserveCapacity(urls.count)
        var lastShown = Date.distantPast
        for (i, url) in urls.enumerated() {
            if Date().timeIntervalSince(lastShown) > 0.3 {
                lastShown = Date()
                await progress(i, url)
            }
            guard let values = try? url.resourceValues(forKeys: [.fileSizeKey, .contentModificationDateKey]) else {
                checks.append(CacheCheck(url: url, size: nil, modified: nil, hit: nil))
                continue
            }
            // As cachedHash.
            let size = values.fileSize ?? -1
            let modified = values.contentModificationDate ?? .distantPast
            var hit: String?
            if let entry = cache[url.standardizedFileURL.path], entry.size == size, abs(entry.modified.timeIntervalSince(modified)) < 0.001 {
                hit = entry.hash
            }
            checks.append(CacheCheck(url: url, size: values.fileSize, modified: values.contentModificationDate, hit: hit))
        }
        return checks
    }

    /// What a two-way pass saw at a path when it planned: nothing, or a
    /// file of this size and modification date.
    private enum LocalStamp { case absent, present(size: Int, modified: Date) }

    /// Whether the file at `url` is still what the plan saw. A pass can
    /// run for hours: an edit made here after the hashing must not be
    /// overwritten by a planned download or trashed by a planned delete.
    private nonisolated static func localMatches(_ url: URL, _ stamp: LocalStamp) -> Bool {
        switch stamp {
        case .absent:
            return !FileManager.default.fileExists(atPath: url.path)
        case let .present(size, modified):
            // A fresh URL: no resource values cached from the scan.
            guard let v = try? URL(fileURLWithPath: url.path).resourceValues(forKeys: [.fileSizeKey, .contentModificationDateKey]),
                  let s = v.fileSize, let m = v.contentModificationDate else { return false }
            // The same tolerance as cachedHash: a cached date went through JSON.
            return s == size && abs(m.timeIntervalSince(modified)) < 0.001
        }
    }

    private static func hashCacheURL(_ folderId: UUID) -> URL? {
        guard let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first else { return nil }
        let dir = base.appendingPathComponent("OffTheCloud/hashes", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        return dir.appendingPathComponent(folderId.uuidString + ".json")
    }

    private func loadHashCacheIfNeeded(_ folderId: UUID) async {
        guard !hashCacheLoaded.contains(folderId) else { return }
        let load: Task<[String: HashEntry], Never>
        if let pending = hashCacheLoads[folderId] {
            load = pending
        } else {
            let url = Self.hashCacheURL(folderId)
            load = Task.detached(priority: .utility) {
                guard let url, let data = try? Data(contentsOf: url),
                      let stored = try? JSONDecoder().decode([String: HashEntry].self, from: data) else { return [:] }
                return stored
            }
            hashCacheLoads[folderId] = load
        }
        let stored = await load.value
        // The first one back takes it in; a folder removed meanwhile stays
        // removed.
        guard hashCacheLoads[folderId] == load else { return }
        hashCacheLoads[folderId] = nil
        hashCacheLoaded.insert(folderId)
        // Anything cached here meanwhile is newer than the file.
        localHashCache[folderId] = stored.merging(localHashCache[folderId] ?? [:]) { _, fresh in fresh }
    }

    /// Writes the folder's cache if this pass changed it; the end of
    /// every reconcile pass calls it.
    private func saveHashCache(_ folderId: UUID) {
        // A pass that outlived its folder doesn't write back the file the
        // removal deleted.
        guard folders.contains(where: { $0.id == folderId }) || remoteFolders.contains(where: { $0.id == folderId }) else {
            localHashCache.removeValue(forKey: folderId)
            hashCacheDirty.remove(folderId)
            return
        }
        guard hashCacheDirty.remove(folderId) != nil, let url = Self.hashCacheURL(folderId),
              let entries = localHashCache[folderId] else { return }
        Task.detached(priority: .utility) {
            guard let data = try? JSONEncoder().encode(entries) else { return }
            try? data.write(to: url, options: [.atomic, .completeFileProtection])
        }
    }

    /// Forgets the cached hashes of files no longer in the folder (deleted,
    /// renamed, or under the folder's old path after it moved), which
    /// were kept, and rewritten with the cache, for good. `present` is
    /// the folder's files by cache key, from a scan that saw all of them:
    /// a partial one would drop hashes still needed.
    private func pruneHashCache(_ folderId: UUID, keeping present: Set<String>) async {
        // First: a load after the prune would bring the stale entries back.
        await loadHashCacheIfNeeded(folderId)
        let stale = localHashCache[folderId]?.keys.filter { !present.contains($0) } ?? []
        guard !stale.isEmpty else { return }
        for key in stale { localHashCache[folderId]?.removeValue(forKey: key) }
        hashCacheDirty.insert(folderId)
    }

    private func dropHashCache(_ folderId: UUID) {
        localHashCache.removeValue(forKey: folderId)
        hashCacheLoaded.remove(folderId)
        hashCacheLoads.removeValue(forKey: folderId)?.cancel()
        hashCacheDirty.remove(folderId)
        if let url = Self.hashCacheURL(folderId) { try? FileManager.default.removeItem(at: url) }
    }

    // Every folder is two-way: what was in sync after the last pass
    // (relative path -> hash) is what tells "deleted here" apart from
    // "new over there". It used to live in memory only, so after every
    // launch a file deleted while the app was closed came back from the
    // device. Saved per folder next to the hash cache (synced/<id>.json).
    private var syncedLoaded: Set<UUID> = []
    // Records whose last write failed: the next pass writes them again
    // even when nothing changed, or a relaunch would start from the
    // stale one on disk.
    private var syncedUnsaved: Set<UUID> = []

    private static func syncedURL(_ folderId: UUID) -> URL? {
        guard let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first else { return nil }
        let dir = base.appendingPathComponent("OffTheCloud/synced", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        return dir.appendingPathComponent(folderId.uuidString + ".json")
    }

    /// Read off the main actor. Only reconcileRemoteFolder reads or
    /// writes the record, one pass per folder at a time.
    private func loadSyncedIfNeeded(_ folderId: UUID) async {
        guard !syncedLoaded.contains(folderId) else { return }
        syncedLoaded.insert(folderId)
        let url = Self.syncedURL(folderId)
        let stored = await Task.detached(priority: .utility) { () -> [String: String]? in
            guard let url, let data = try? Data(contentsOf: url) else { return nil }
            return try? JSONDecoder().decode([String: String].self, from: data)
        }.value
        // Removed meanwhile: stays removed.
        guard let stored, syncedLoaded.contains(folderId) else { return }
        lastSyncedByRemoteFolder[folderId] = stored
    }

    private func saveSynced(_ folderId: UUID, _ synced: [String: String]) {
        guard remoteFolders.contains(where: { $0.id == folderId }), let url = Self.syncedURL(folderId) else { return }
        syncedUnsaved.remove(folderId)
        Task.detached(priority: .utility) {
            do {
                let data = try JSONEncoder().encode(synced)
                try data.write(to: url, options: [.atomic, .completeFileProtection])
            } catch {
                syncLog.error("could not save the sync record of \(folderId.uuidString, privacy: .public): \(error.localizedDescription, privacy: .public)")
                await self.noteSyncedUnsaved(folderId)
            }
        }
    }

    private func noteSyncedUnsaved(_ folderId: UUID) {
        syncedUnsaved.insert(folderId)
    }

    private func dropSynced(_ folderId: UUID) {
        lastSyncedByRemoteFolder.removeValue(forKey: folderId)
        syncedLoaded.remove(folderId)
        syncedUnsaved.remove(folderId)
        if let url = Self.syncedURL(folderId) { try? FileManager.default.removeItem(at: url) }
    }

    /// Folders added from this Mac used to be upload-only mirrors; every
    /// folder is two-way now, the only difference being how it started
    /// (its first pass uploads what is here). Moved over once, keeping
    /// the folder's id - and with it its hash cache - and the device path
    /// it has always synced to. With no sync record yet the first pass
    /// deletes nothing: what is only on the device comes down, what is
    /// only here goes up.
    private func migrateLocalFolders() {
        // Once only: one-way backups are a choice again, added on purpose,
        // and must stay what they were added as.
        let doneKey = "sync.folders.migratedToTwoWay"
        guard !UserDefaults.standard.bool(forKey: doneKey) else { return }
        UserDefaults.standard.set(true, forKey: doneKey)
        let stored = existingStored()
        guard !stored.isEmpty else { return }
        var remote = existingStoredRemote()
        for item in stored {
            guard let folder = folders.first(where: { $0.id == item.id }) else { continue }
            let remotePath = remotePathFor(folder.url.path)
            if !remote.contains(where: { $0.id == item.id }) {
                remote.append(StoredRemoteFolder(id: item.id, remotePath: remotePath, bookmark: item.bookmark, outOfImages: item.outOfImages))
                remoteFolders.append(RemoteFolder(id: item.id, remotePath: remotePath, localURL: folder.url))
            }
        }
        persistRemoteFolders(bookmarks: remote)
        persistFolders(bookmarks: [])
        folders = []
    }

    init() {
        restoreFolders()
        restoreRemoteFolders()
        migrateLocalFolders()

        ws.onConnect = { [weak self] route in
            Task { @MainActor in
                guard let self else { return }
                self.overallStatus = "Connected"
                self.route = route
                // The one-time "Start at login?" offer waits for this.
                LoginItemSettings.shared.deviceConnected()
                self.startRaidPolling()
                // Issue #192: what the device keeps out of Images, then the
                // requests still pending - asked again at every connect,
                // since updating a device that couldn't restarts it.
                if self.outOfImagesSupported == false { self.outOfImagesSupported = nil }
                await self.refreshOutOfImages()
                self.applyPendingRequests(Self.imagesRequest)
                // Issue #132: likewise for upload only.
                if self.uploadOnlySupported == false { self.uploadOnlySupported = nil }
                self.applyPendingRequests(Self.uploadOnlyRequest)
                // Folders left in an error while the link was down (their
                // retry finds no connection and gives up) go again now,
                // instead of waiting for the 10-minute pass.
                for folder in self.folders {
                    if case .error = folder.state { await self.reconcile(folder) }
                }
                for folder in self.remoteFolders {
                    if case .error = folder.state { await self.reconcileRemoteFolder(folder) }
                }
            }
        }
        ws.onDisconnect = { [weak self] _ in
            Task { @MainActor in
                // A rejected password is its own state; a plain drop
                // must not overwrite it with "Disconnected".
                if self?.overallStatus != "Wrong password", self?.overallStatus != "Device offline - retrying" {
                    self?.overallStatus = "Disconnected"
                }
                self?.route = nil
                self?.stopRaidPolling()
            }
        }
        ws.onUnreachable = { [weak self] _ in
            Task { @MainActor in
                self?.overallStatus = "Device offline - retrying"
                self?.route = nil
                self?.stopRaidPolling()
            }
        }
        ws.onHomeMissed = { [weak self] in
            Task { @MainActor [weak self] in self?.scheduleHomeRetry() }
        }
        ws.onAuthFailed = { [weak self] message, retryAfter in
            Task { @MainActor in
                guard let self else { return }
                self.route = nil
                self.stopRaidPolling()
                if let retryAfter {
                    // Locked out for guessing, not necessarily wrong: say
                    // so, and try once more when the lock lifts.
                    self.overallStatus = "Too many attempts - retrying in \(retryAfter)s"
                    self.authRetryTask?.cancel()
                    self.authRetryTask = Task { [weak self] in
                        try? await Task.sleep(for: .seconds(retryAfter + 1))
                        guard !Task.isCancelled, let self, self.settings?.ready == true else { return }
                        self.overallStatus = "Connecting…"
                        self.ws.connect()
                    }
                } else {
                    self.overallStatus = "Wrong password"
                }
            }
        }

        pathMonitor.pathUpdateHandler = { [weak self] path in
            let key = Self.pathKey(path)
            let up = path.status == .satisfied
            Task { @MainActor [weak self] in self?.pathChanged(key, up: up) }
        }
        pathMonitor.start(queue: DispatchQueue(label: "sync.path"))
        wakeObserver = NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.didWakeNotification, object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor [weak self] in self?.scheduleRouteCheck(wake: true) }
        }
    }

    // MARK: - Home network route (issue #190)

    /// What a network change is told apart by: the physical interfaces
    /// and the gateways (another Wi-Fi has another router, a VPN another
    /// gateway). Tunnels coming and going (iCloud Private Relay and the
    /// like) are left out, or each would reconnect.
    private nonisolated static func pathKey(_ path: NWPath) -> String {
        let interfaces = path.availableInterfaces
            .filter { $0.type == .wifi || $0.type == .wiredEthernet || $0.type == .cellular }
            .map { "\($0.name)/\($0.type)" }.joined(separator: ",")
        let gateways = path.gateways.map { "\($0)" }.joined(separator: ",")
        return "\(path.status)|\(interfaces)|\(gateways)"
    }

    /// With no network there is nothing to switch to; its return is
    /// another change.
    private func pathChanged(_ key: String, up: Bool) {
        defer { pathKey = key }
        guard let old = pathKey, old != key else { return }
        guard up else {
            routeCheckTask?.cancel()
            pathCheckPending = false
            return
        }
        scheduleRouteCheck(wake: false)
    }

    /// A wake only matters when not already home: a home connection that
    /// slept is either fine or reconnects by itself. A network change
    /// wins whichever comes first: Wi-Fi rejoining another network just
    /// before the wake notification left a dead home socket still "home".
    private func scheduleRouteCheck(wake: Bool) {
        let wake = wake && !pathCheckPending
        pathCheckPending = !wake
        routeCheckTask?.cancel()
        routeCheckTask = Task { [weak self] in
            try? await Task.sleep(for: Self.routeCheckDelay)
            guard !Task.isCancelled, let self else { return }
            self.pathCheckPending = false
            if wake, self.route == .home { return }
            self.switchRouteIfIdle()
        }
    }

    /// The popover opened: the Mac's coming to the foreground. The Local
    /// Network prompt answered since, or a home listener back, is found
    /// without waiting for a network change.
    func popoverOpened() {
        if let last = lastHomeProbe, ContinuousClock.now - last < Self.homeProbeInterval { return }
        probeHomeThenSwitch()
    }

    /// Waits for nothing to be in flight, then looks for the device at home
    /// once.
    private func scheduleHomeRetry() {
        homeRetryTask?.cancel()
        homeRetryTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: Self.homeRetryDelay)
                guard !Task.isCancelled, let self, self.route == .bridge else { return }
                if !self.syncInFlight {
                    self.probeHomeThenSwitch()
                    return
                }
            }
        }
    }

    /// On the bridge with a home endpoint: reconnects, home first, only
    /// once the device has answered there, so away from home the bridge
    /// connection isn't touched and "Connecting…" never shows.
    private func probeHomeThenSwitch() {
        guard settings?.ready == true, overallStatus == "Connected", route == .bridge,
              !syncInFlight, ws.hasLocalEndpoint else { return }
        lastHomeProbe = .now
        Task { [weak self] in
            guard let self, await self.ws.probeHome() else { return }
            guard self.overallStatus == "Connected", self.route == .bridge else { return }
            syncLog.info("the device answers on the home network: connecting there")
            self.switchRouteIfIdle()
        }
    }

    /// Reconnects, home network first, but only between transfers: a
    /// switch would cut an upload or a pass short. One that is running
    /// keeps its connection, and the next reconnect picks the route.
    private func switchRouteIfIdle() {
        guard settings?.ready == true else { return }
        guard !syncInFlight else {
            syncLog.info("network changed during a transfer: staying on this connection")
            return
        }
        if ws.reconnectForRoute() {
            overallStatus = "Connecting…"
        }
    }

    /// An upload, a download, a pass or a picker's listing under way.
    /// Unlike otc-sync, not every pending request: the RAID poll ignores a
    /// failure, and one sent on a dead home socket would wait there for
    /// the 30-minute timeout, holding the Mac on it.
    private var syncInFlight: Bool {
        !foldersBusy.isEmpty || !remoteFoldersBusy.isEmpty || !foldersSettingUp.isEmpty || !changeWorkers.isEmpty
            || listingsInFlight > 0
    }

    /// Settings' status line: once connected, which way (issue #190).
    var connectionStatus: String {
        guard overallStatus == "Connected", let route, let domain = settings?.domain else { return overallStatus }
        return ConnectionRoute.label(route, domain: domain)
    }

    // MARK: Bind settings / auto-sync

    func bind(settings: SettingsStore) {
        guard self.settings == nil else { return }
        self.settings = settings

        settings.$domain
            .combineLatest(settings.$password)
            // The Settings panel applies domain and password together with
            // its Connect button (they used to bind straight to the fields,
            // reconnecting on every keystroke and spending a password
            // attempt per character - issue #117's lock-out). The short
            // debounce folds that pair of assignments into one reconnect.
            .debounce(for: .seconds(0.3), scheduler: DispatchQueue.main)
            .removeDuplicates { $0.0 == $1.0 && $0.1 == $1.1 }
            .sink { [weak self] domain, key in
                guard let self else { return }
                Task { @MainActor in
                    self.authRetryTask?.cancel()
                    // Possibly another device: each backup's next pass
                    // makes it upload only there. Not on a plain
                    // reconnect - the owner may have cleared the lock
                    // from the web app, and it is set again only at the
                    // next launch, as otc-sync does.
                    self.uploadOnlyOK.removeAll()
                    self.uploadOnlyErrors.removeAll()
                    // Nor is what it keeps out of Images: asked again; nor
                    // whether it can make folders upload only.
                    self.outOfImagesFolders = nil
                    self.outOfImagesSupported = nil
                    self.outOfImagesErrors.removeAll()
                    self.outOfImagesNotes.removeAll()
                    self.uploadOnlySupported = nil
                    self.uploadOnlyRequestErrors.removeAll()
                    self.uploadOnlyNotes.removeAll()
                    if settings.ready {
                        self.overallStatus = "Connecting…"
                        self.ws.configure(domain: domain, key: key)
                        self.ws.connect()
                        self.startSync()
                    } else {
                        // Disconnect: the home endpoint goes with the
                        // address.
                        self.ws.forgetLocalEndpoint()
                        self.ws.disconnect()
                        self.overallStatus = "Missing domain/password"
                    }
                }
            }
            .store(in: &cancellables)
    }

    // MARK: - UI actions

    /// The Connect button pressed with the settings unchanged: try again
    /// now. After "Wrong password" the client stops retrying on purpose
    /// (issue #117's lock-out), and a password changed on the device - or
    /// a device that is back - needs a way to be retried without editing
    /// the fields first.
    func reconnectNow() {
        guard let settings, settings.ready else { return }
        authRetryTask?.cancel()
        overallStatus = "Connecting…"
        ws.configure(domain: settings.domain, key: settings.password)
        ws.connect()
        startSync()
    }

    /// Forgets the device: every folder is removed from the app (the files
    /// stay where they are, here and on the device) and the address and
    /// password are cleared - folders kept across a change of device would
    /// start syncing with, or deleting on, a different device.
    func disconnect() {
        folders.forEach(removeFolder)
        remoteFolders.forEach(removeRemoteFolder)
        ws.forgetLocalEndpoint()
        settings?.apply(domain: "", password: "")
    }

    // MARK: - RAID health (issue #69)

    private func startRaidPolling() {
        guard raidPollTask == nil else { return }
        raidPollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                await self.pollRaidStatus()
                try? await Task.sleep(for: Self.raidPollInterval)
            }
        }
    }

    private func stopRaidPolling() {
        raidPollTask?.cancel()
        raidPollTask = nil
        raidHealth = .unknown
        deviceStatus = nil
        updateAlert = nil
    }

    private func pollRaidStatus() async {
        guard let resp = try? await ws.request({ req in
            req.payload = .reqGetStatus(Msg_GetStatus())
        }), case .respStatus(let status) = resp.payload else { return }
        // Published only when they change: each assignment redraws what
        // watches SyncModel, every 10 seconds for nothing. deviceStatus
        // carries CPU and memory, which do change on every poll.
        let health = RaidHealth(status: status)
        if raidHealth != health { raidHealth = health }
        deviceStatus = status
        let level = status.updateAlert.level
        let alert: Msg_UpdateAlert? = status.hasUpdateAlert && (level == "major" || level == "critical")
            ? status.updateAlert : nil
        if updateAlert != alert { updateAlert = alert }
    }

    /// A one-way backup of a folder on this Mac: new and changed files go
    /// to the device, which keeps what is deleted here (upload only,
    /// markUploadOnly), and nothing on the device ever changes the folder
    /// here (reconcile()). With `outOfImages`, kept out of Images from its
    /// first pass (issue #192).
    func addBackupFolder(outOfImages: Bool = false) {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = false
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.prompt = "Back Up"
        panel.message = "Choose a folder to back up to the device. This Mac stays the original."
        if runFolderPanel(panel) == .OK, let url = panel.url {
            do {
                let bookmark = try url.bookmarkData(options: [.withSecurityScope], includingResourceValuesForKeys: nil, relativeTo: nil)
                _ = url.startAccessingSecurityScopedResource()
                let tf = TrackedFolder(id: UUID(), url: url)
                if outOfImages { outOfImagesPending[tf.id] = true }
                folders.append(tf)
                var stored = existingStored()
                stored.append(StoredFolder(id: tf.id, bookmark: bookmark, outOfImages: outOfImages ? true : nil))
                persistFolders(bookmarks: stored)
                if settings?.ready == true {
                    Task { await self.setupFolder(tf) }
                }
            } catch {
                print("Bookmark creation failed:", error)
            }
        }
    }

    /// A two-way folder from this Mac, with the options chosen for it
    /// (AddFolderChooser's step 2): upload only (issue #132) and kept out of
    /// Images (issue #192), each sent before its first pass sends anything.
    func addFolder(uploadOnly: Bool = false, outOfImages: Bool = false) {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = false
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        if runFolderPanel(panel) == .OK, let url = panel.url {
            // Two-way like every folder: it syncs to this Mac's own place
            // on the device, and with nothing there yet its first pass
            // uploads what the folder holds.
            addTwoWayFolder(remotePath: remotePathFor(url.path), localURL: url, uploadOnly: uploadOnly, outOfImages: outOfImages)
        }
    }

    func removeFolder(_ f: TrackedFolder) {
        folderWatchers[f.id]?.stop()
        folderWatchers.removeValue(forKey: f.id)
        remoteHashesByFolder.removeValue(forKey: f.id)
        errorRetryTasks[f.id]?.cancel()
        errorRetryTasks.removeValue(forKey: f.id)
        changeWorkers.removeValue(forKey: f.id)?.cancel()
        changeQueues.removeValue(forKey: f.id)
        uploadOnlyOK.remove(f.id)
        uploadOnlyErrors.removeValue(forKey: f.id)
        forgetRequests(f.id)

        dropHashCache(f.id)
        f.url.stopAccessingSecurityScopedResource()
        folders.removeAll { $0.id == f.id }

        var stored = existingStored()
        stored.removeAll { $0.id == f.id }
        persistFolders(bookmarks: stored)
    }

    // MARK: - UI actions (issue #47: remote → local)

    /// Lists one remote directory's immediate children, for
    /// RemoteFolderPickerView to walk one level at a time — a full
    /// recursive listing isn't needed (or wanted) just to browse.
    func listRemoteDirectory(_ path: String) async throws -> [RemoteEntry] {
        // dao.GetFilesByPath's non-recursive listing counts the path's own
        // slashes to know which level to list — it needs a trailing "/" to
        // count correctly (the web app's normPath() does the same before
        // every ListFiles call, see FilesExplorer.tsx). Without this, every
        // directory returned by one listing (never trailing-slashed by the
        // server) breaks the *next* listing one level down, which is
        // exactly why navigation looked stuck after one level.
        let normalized = path.hasSuffix("/") ? path : path + "/"
        listingsInFlight += 1
        defer { listingsInFlight -= 1 }
        let resp = try await ws.request { req in
            var lf = ListFiles()
            lf.path = normalized
            lf.recursive = false
            req.payload = .reqListFiles(lf)
        }
        if resp.error {
            throw NSError(domain: "sync.listRemote", code: 1, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "Could not list remote files" : resp.errorMessage])
        }
        guard case .respListOfFiles(let lof) = resp.payload else { return [] }
        return lof.files
            .map { RemoteEntry(id: $0.path, name: ($0.path as NSString).lastPathComponent, path: $0.path, isDir: $0.mime == "inode/directory") }
            .sorted { lhs, rhs in
                if lhs.isDir != rhs.isDir { return lhs.isDir }
                return lhs.name.lowercased() < rhs.name.lowercased()
            }
    }

    /// Called once the user has picked both a remote directory (via
    /// RemoteFolderPickerView) and a local destination (via NSOpenPanel,
    /// same picker addFolder() uses) for it. No options: a folder from the
    /// device keeps what it has there (Images, upload only), which its row,
    /// the web or a phone change.
    func addRemoteFolder(remotePath: String, localURL: URL) {
        addTwoWayFolder(remotePath: remotePath, localURL: localURL, uploadOnly: false, outOfImages: false)
    }

    /// Both two-way kinds: the folder, its requests (sent before its first
    /// pass), and that first pass when connected.
    private func addTwoWayFolder(remotePath: String, localURL: URL, uploadOnly: Bool, outOfImages: Bool) {
        do {
            let bookmark = try localURL.bookmarkData(
                options: [.withSecurityScope],
                includingResourceValuesForKeys: nil,
                relativeTo: nil
            )
            _ = localURL.startAccessingSecurityScopedResource()

            let rf = RemoteFolder(id: UUID(), remotePath: remotePath, localURL: localURL)
            if outOfImages { outOfImagesPending[rf.id] = true }
            if uploadOnly { uploadOnlyPending[rf.id] = true }
            remoteFolders.append(rf)

            var stored = existingStoredRemote()
            stored.append(StoredRemoteFolder(id: rf.id, remotePath: remotePath, bookmark: bookmark,
                                             outOfImages: outOfImages ? true : nil, uploadOnly: uploadOnly ? true : nil))
            persistRemoteFolders(bookmarks: stored)

            if settings?.ready == true, ws.isConnected() {
                Task {
                    await self.reconcileRemoteFolder(rf)
                    self.startRemoteWatcher(for: rf)
                }
                startRemoteReconcileLoop()
            }
        } catch {
            print("Bookmark creation failed:", error)
        }
    }

    func removeRemoteFolder(_ f: RemoteFolder) {
        remoteFolderWatchers[f.id]?.stop()
        remoteFolderWatchers.removeValue(forKey: f.id)
        remoteFolderDebounce[f.id]?.cancel()
        remoteFolderDebounce.removeValue(forKey: f.id)
        dropSynced(f.id)
        remoteErrorRetryTasks[f.id]?.cancel()
        remoteErrorRetryTasks.removeValue(forKey: f.id)
        forgetRequests(f.id)

        dropHashCache(f.id)
        f.localURL.stopAccessingSecurityScopedResource()
        remoteFolders.removeAll { $0.id == f.id }

        var stored = existingStoredRemote()
        stored.removeAll { $0.id == f.id }
        persistRemoteFolders(bookmarks: stored)
    }

    // MARK: - Sync orchestration

    private func startSync() {
        guard let settings, settings.ready else { return }
        Task {
            while !ws.isConnected() {
                guard self.settings?.ready == true else { return }
                try? await Task.sleep(for: .seconds(1))
            }
            for folder in folders where folderWatchers[folder.id] == nil {
                await setupFolder(folder)
            }
            startReconcileLoop()

            for folder in remoteFolders {
                await reconcileRemoteFolder(folder)
                // A folder whose local root was found missing during that
                // reconcile is already gone from `remoteFolders` (see the
                // guard at the top of reconcileRemoteFolder) — nothing to
                // watch in that case.
                if self.remoteFolders.contains(where: { $0.id == folder.id }) {
                    self.startRemoteWatcher(for: folder)
                }
            }
            startRemoteReconcileLoop()
        }
    }

    /// One-time (per folder, per launch) baseline: reconcile against
    /// whatever's already on the device, then start watching for changes.
    /// Everything after this is event-driven, not scan-driven.
    private func setupFolder(_ folder: TrackedFolder) async {
        // Retry, a settings change and the startSync tasks queued while
        // offline all get here: one first pass per folder, and its watcher
        // only once that pass is done, as before.
        guard folderWatchers[folder.id] == nil, !foldersSettingUp.contains(folder.id) else { return }
        foldersSettingUp.insert(folder.id)
        defer { foldersSettingUp.remove(folder.id) }
        // reconcile() makes the folder upload only first.
        await reconcile(folder)
        startWatcher(for: folder)
    }

    private func startReconcileLoop() {
        guard !reconcileLoopStarted else { return }
        reconcileLoopStarted = true
        Task.detached { [weak self] in
            while let self {
                try? await Task.sleep(for: Self.reconcileInterval)
                let (ready, currentFolders): (Bool, [TrackedFolder]) = await MainActor.run {
                    (self.settings?.ready ?? false, self.folders)
                }
                guard ready, self.ws.isConnected() else { continue }
                for folder in currentFolders {
                    await self.reconcile(folder)
                }
            }
        }
    }

    private func startWatcher(for folder: TrackedFolder) {
        // Removed while its first pass ran: no watcher that would go on
        // uploading it for good.
        guard folderWatchers[folder.id] == nil, folders.contains(where: { $0.id == folder.id }) else { return }
        let watcher = FolderWatcher { [weak self] events in
            guard let self else { return }
            Task { @MainActor in
                self.handleEvents(events, folderId: folder.id)
            }
        }
        watcher.start(paths: [folder.url.path])
        folderWatchers[folder.id] = watcher
    }

    // MARK: - Event-driven sync (issue #37)

    private func handleEvents(_ events: [FolderWatcher.Event], folderId: UUID) {
        let due = ContinuousClock.now + Self.debounceInterval
        let queue = changeQueues[folderId] ?? ChangeQueue()
        let root = folders.first(where: { $0.id == folderId })?.url.standardizedFileURL.path
        for event in events {
            // We only care about actual file content, not directories
            // being created/renamed/removed — those surface indirectly
            // through their children's own events anyway.
            let isDir = event.flags & FSEventStreamEventFlags(kFSEventStreamEventFlagItemIsDir) != 0
            if isDir { continue }
            // Nor what reconcile skips: every Finder .DS_Store rewrite
            // used to add a version of it on the device.
            if let root, SyncPaths.isSkippedBackupPath(event.path, root: root) { continue }

            queue.order.append((event.path, due))
            queue.latest[event.path] = due
        }
        guard !queue.latest.isEmpty else { return }
        changeQueues[folderId] = queue
        if changeWorkers[folderId] == nil {
            changeWorkers[folderId] = Task { [weak self] in await self?.drainChanges(folderId) }
        }
    }

    /// The folder's worker: each changed path once it has been quiet for
    /// debounceInterval, one after another, until none is left.
    private func drainChanges(_ folderId: UUID) async {
        while !Task.isCancelled, let queue = changeQueues[folderId], queue.head < queue.order.count {
            let (path, due) = queue.order[queue.head]
            // Changed again since: its later entry stands.
            guard queue.latest[path] == due else { queue.head += 1; continue }
            if due > .now {
                // Entries behind this one fall due later still.
                try? await Task.sleep(until: due, clock: .continuous)
                continue
            }
            queue.head += 1
            queue.latest.removeValue(forKey: path)
            if queue.head >= 1024, queue.head * 2 >= queue.order.count {
                queue.order.removeFirst(queue.head)
                queue.head = 0
            }
            guard folders.contains(where: { $0.id == folderId }) else { continue }
            await processChangedPath(path, folderId: folderId)
        }
        changeQueues[folderId] = nil
        changeWorkers[folderId] = nil
    }

    private func processChangedPath(_ path: String, folderId: UUID) async {
        // Not connected right now — the reconcile safety net (or the next
        // FSEvents batch, if this exact path changes again) will catch it
        // up once we're back online.
        guard ws.isConnected() else { return }
        // As reconcile(): a change of a folder since removed, or one that
        // was hashing while the app moved to another device, is not sent.
        let domainAtStart = settings?.domain
        guard folders.contains(where: { $0.id == folderId }) else { return }

        let remotePath = remotePathFor(path)
        let fileURL = URL(fileURLWithPath: path)
        var isDir: ObjCBool = false
        let exists = FileManager.default.fileExists(atPath: path, isDirectory: &isDir)

        if exists, !isDir.boolValue {
            let localHash = try? await cachedHash(for: fileURL, folderId: folderId)
            guard let localHash else { return }
            guard folders.contains(where: { $0.id == folderId }), settings?.domain == domainAtStart else { return }
            guard remoteHashesByFolder[folderId]?[remotePath] != localHash else { return }
            do {
                try await upload(fileURL, to: remotePath, knownHash: localHash)
                remoteHashesByFolder[folderId, default: [:]][remotePath] = localHash
            } catch {
                print("Error uploading \(path): \(error)")
            }
        }
        // Gone here: nothing to do. A backup is upload only - what this Mac
        // deletes stays on the device (which refuses the delete anyway, see
        // markUploadOnly). As otc-sync.
    }

    // MARK: - Reconcile (baseline + periodic safety net)

    private func reconcile(_ folder: TrackedFolder) async {
        // A loop's or a retry's copy of a folder removed since: nothing to do.
        guard ws.isConnected(), folders.contains(where: { $0.id == folder.id }) else { return }
        // The device this pass talks to: a pass still running when the
        // folder is removed or the app moves to another device (Disconnect,
        // a new device set up) must stop, not carry on there.
        let domainAtStart = settings?.domain
        // One pass per folder at a time, as reconcileRemoteFolder: the
        // 10-minute loop used to start a second pass of a folder whose
        // first one was still uploading, and both sent the same files.
        // Before the retry cancel, so a skipped call leaves the running
        // pass's retry alone.
        guard !foldersBusy.contains(folder.id) else { return }
        foldersBusy.insert(folder.id)
        defer { foldersBusy.remove(folder.id) }
        if !uploadOnlyOK.contains(folder.id) { await markUploadOnly(folder) }

        // Reconciling now anyway (whatever triggered this call), so any
        // still-pending short retry from a previous failure would just be
        // a redundant duplicate once this one lands.
        errorRetryTasks[folder.id]?.cancel()
        errorRetryTasks[folder.id] = nil

        let root = folder.url
        // The computer's name is looked up once per pass, not per file.
        let deviceRoot = remoteDeviceRoot()
        deviceRootForImages = deviceRoot
        let remotePrefix = remotePathFor(root.path, root: deviceRoot) + "/"
        // Issue #192: before anything is sent, so a new folder's photos
        // never reach Images. One request at most: an old or unreachable
        // device doesn't hold the backup up.
        await applyRequestsBeforePass(folder.id)

        do {
            let resp = try await ws.request { req in
                var lf = ListFiles()
                lf.path = remotePrefix
                lf.recursive = true
                req.payload = .reqListFiles(lf)
            }
            // Same reasoning as upload()/delete(): a rejected request
            // (e.g. bad credentials) still comes back as a normal
            // response. Treating it the same as "empty folder" here would
            // make every file look new and re-upload the whole tree.
            if resp.error {
                updateState(folder.id, .error(resp.errorMessage.isEmpty ? "Could not list remote files" : resp.errorMessage))
                // Retried on the short schedule like a thrown error, not
                // left until the 10-minute safety net - this is the path
                // a "not authenticated" reply used to sit on.
                scheduleErrorRetry(for: folder)
                return
            }
            var remoteMap: [String: String] = [:]
            if case .respListOfFiles(let lof) = resp.payload {
                remoteMap = Dictionary(uniqueKeysWithValues: lof.files.map { ($0.path, $0.hash) })
            }

            // Upload only: what couldn't be read is simply not sent.
            let (scan, present) = await Task.detached(priority: .utility) { () -> (LocalScan, Set<String>) in
                let scan = Self.enumerateFilesRecursively(at: root)
                return (scan, Set(scan.urls.map { $0.standardizedFileURL.path }))
            }.value
            let localFiles = scan.urls
            // Right after the scan, so what the watcher caches during a
            // long pass stays.
            if scan.complete { await pruneHashCache(folder.id, keeping: present) }

            // Pass 1: figure out what actually needs uploading. This is
            // pure verification — on a folder that's already in sync (the
            // common case for the periodic safety-net reconcile, or just
            // reopening the app) every file matches and nothing here is
            // visible to the user at all, which is the point: hashing to
            // *confirm* nothing changed shouldn't look like a transfer is
            // underway. The progress bar below is reserved for real
            // mismatches only.
            // hash is nil for a file the device has nothing at: it is sent
            // whatever its content, so hashing it here only delayed the
            // start (a new folder sat on "Checking" for as long as reading
            // all of it took) - upload() hashes it just before sending.
            var toUpload: [(url: URL, remotePath: String, hash: String?, size: Int64)] = []
            // Issue #138: say which file is being checked (a few times a
            // second at most - most files are answered from the hash
            // cache in no time, the new ones are what takes a while).
            var lastShown = Date.distantPast
            var folderBytes: Int64 = 0
            await loadHashCacheIfNeeded(folder.id)
            let checks = await Task.detached(priority: .utility) { [cache = localHashCache[folder.id] ?? [:]] in
                await Self.checkAgainstCache(localFiles, cache: cache) { i, url in
                    await self.updateState(folder.id, .scanning(progress: 0, currentFile: "Checking \(i + 1)/\(localFiles.count) · \(url.lastPathComponent)"))
                }
            }.value
            for (i, check) in checks.enumerated() {
                let fileURL = check.url
                let remotePath = remotePathFor(fileURL.path, root: deviceRoot)
                let size = Int64(check.size ?? 0)
                folderBytes += size
                if remoteMap[remotePath] == nil {
                    toUpload.append((fileURL, remotePath, nil, size))
                    continue
                }
                let localHash: String?
                if let hit = check.hit {
                    localHash = hit
                    // A long run of hits still lets the UI in now and then.
                    if i % 1000 == 999 { await Task.yield() }
                } else {
                    if Date().timeIntervalSince(lastShown) > 0.3 {
                        lastShown = Date()
                        updateState(folder.id, .scanning(progress: 0, currentFile: "Checking \(i + 1)/\(localFiles.count) · \(fileURL.lastPathComponent)"))
                    }
                    localHash = try? await cachedHash(for: fileURL, folderId: folder.id)
                }
                guard let localHash, remoteMap[remotePath] != localHash else { continue }
                toUpload.append((fileURL, remotePath, localHash, size))
            }

            // Pass 2: the mismatches. Counter and bar are of the whole
            // folder - what is already on the device counts as done - so
            // they say how much of the folder is safe, the same after a
            // restart as before it (they used to count only this pass's
            // uploads, starting again from 0 of whatever was left).
            if !toUpload.isEmpty {
                // Weighted by bytes, not file count: one 400MB video among
                // small photos would otherwise sit still for the video's
                // whole multi-minute transfer - issue #37's complaint.
                let totalBytes = max(folderBytes, 1)
                var bytesDone: Int64 = folderBytes - toUpload.reduce(0) { $0 + $1.size }
                let alreadyThere = localFiles.count - toUpload.count

                for (k, item) in toUpload.enumerated() {
                    guard folders.contains(where: { $0.id == folder.id }), settings?.domain == domainAtStart else {
                        syncLog.info("backup \(remotePrefix, privacy: .public): folder removed or device changed - pass stopped")
                        return
                    }
                    // The link went (device offline, restarting): stop
                    // here rather than "fail" every remaining file in a
                    // second each, which raced the bar to 100% with
                    // nothing sent. The folder resumes when the app signs
                    // in again (onConnect retries errored folders).
                    guard ws.isConnected() else {
                        updateState(folder.id, .error("Device offline - will resume"))
                        saveHashCache(folder.id)
                        return
                    }
                    // Reported *before* the upload starts, not after — a
                    // multi-gigabyte file can take minutes to send, and
                    // without this the UI just sits on the previous
                    // file's number the whole time, which is exactly what
                    // looked like "stuck" before (issue #37).
                    updateState(folder.id, .scanning(progress: Double(bytesDone) / Double(totalBytes), currentFile: "\(alreadyThere + k + 1)/\(localFiles.count) · \(item.url.lastPathComponent)"))

                    do {
                        // A new file is hashed here rather than inside
                        // upload(), so the check after it covers the time a
                        // large one takes: a device changed meanwhile must
                        // not get it.
                        let hash: String
                        if let known = item.hash { hash = known } else { hash = try await cachedHash(for: item.url, folderId: folder.id) }
                        guard folders.contains(where: { $0.id == folder.id }), settings?.domain == domainAtStart else {
                            syncLog.info("backup \(remotePrefix, privacy: .public): folder removed or device changed - pass stopped")
                            return
                        }
                        remoteMap[item.remotePath] = try await upload(item.url, to: item.remotePath, knownHash: hash, folderId: folder.id)
                    } catch {
                        // Logged and skipped, not fatal to the whole
                        // folder — the next reconcile pass (or another
                        // FSEvents change to this same path) will retry it.
                        print("Error syncing \(item.url.lastPathComponent): \(error)")
                    }
                    bytesDone += item.size
                }
            }

            // A backup is upload only: what is no longer here stays on
            // the device.
            remoteHashesByFolder[folder.id] = remoteMap
            updateState(folder.id, .watching)
        } catch {
            updateState(folder.id, .error(error.localizedDescription))
            scheduleErrorRetry(for: folder)
        }
        saveHashCache(folder.id)
    }

    // A failure here is almost always transient (a dropped connection
    // mid-request, a momentary server error) rather than something wrong
    // with the folder itself, so retrying on its own is the right default
    // — the alternative is an error that just sits there looking permanent
    // until the next 10-minute safety-net pass happens to come around.
    private func scheduleErrorRetry(for folder: TrackedFolder) {
        errorRetryTasks[folder.id]?.cancel()
        errorRetryTasks[folder.id] = Task { [weak self] in
            try? await Task.sleep(for: Self.errorRetryInterval)
            guard !Task.isCancelled, let self else { return }
            self.errorRetryTasks[folder.id] = nil
            await self.reconcile(folder)
        }
    }

    private func updateState(_ id: UUID, _ state: FolderState) {
        setState(id, state, current: folders.first(where: { $0.id == id })?.state) { [weak self] s in
            guard let self, let idx = self.folders.firstIndex(where: { $0.id == id }) else { return }
            self.folders[idx].state = s
        }
    }

    private func updateRemoteState(_ id: UUID, _ state: FolderState) {
        setState(id, state, current: remoteFolders.first(where: { $0.id == id })?.state) { [weak self] s in
            guard let self, let idx = self.remoteFolders.firstIndex(where: { $0.id == id }) else { return }
            self.remoteFolders[idx].state = s
        }
    }

    /// A folder at rest ("Synced", "Backed up") that starts working keeps
    /// showing that it is at rest for quietPassDelay: if the pass is over
    /// by then nothing changes on screen, otherwise its latest progress
    /// shows. Everything else - errors, rest again, progress once shown -
    /// applies at once. otc-sync's engine.setState does the same.
    private var heldStates: [UUID: FolderState] = [:]
    private var heldStateTasks: [UUID: Task<Void, Never>] = [:]

    private func setState(_ id: UUID, _ state: FolderState, current: FolderState?, apply: @escaping (FolderState) -> Void) {
        guard current != nil else { return }
        if case .scanning = state, current == .watching {
            heldStates[id] = state
            if heldStateTasks[id] == nil {
                heldStateTasks[id] = Task { [weak self] in
                    try? await Task.sleep(for: Self.quietPassDelay)
                    guard !Task.isCancelled, let self else { return }
                    self.heldStateTasks[id] = nil
                    if let held = self.heldStates.removeValue(forKey: id) { apply(held) }
                }
            }
            return
        }
        heldStateTasks.removeValue(forKey: id)?.cancel()
        heldStates[id] = nil
        apply(state)
    }

    private func startRemoteReconcileLoop() {
        guard !remoteReconcileLoopStarted else { return }
        remoteReconcileLoopStarted = true
        Task.detached { [weak self] in
            while let self {
                try? await Task.sleep(for: Self.remoteReconcileInterval)
                let (ready, currentFolders): (Bool, [RemoteFolder]) = await MainActor.run {
                    (self.settings?.ready ?? false, self.remoteFolders)
                }
                guard ready, self.ws.isConnected() else { continue }
                // Issue #192: a folder kept out of Images, or shown again,
                // from the web or a phone shows here within a minute.
                await self.refreshOutOfImagesPeriodically()
                for folder in currentFolders {
                    await self.reconcileRemoteFolder(folder)
                }
            }
        }
    }

    // MARK: - Reconcile, two-way (issue #47)
    //
    // Three-way merge: each relative path's *current* local hash and
    // *current* remote hash are compared against lastSyncedByRemoteFolder
    // (what that path looked like as of the last successful reconcile) to
    // tell apart four cases:
    //   - both sides agree with each other -> nothing to do
    //   - only local differs from the baseline -> local changed -> push it
    //     (upload, or delete remote if local no longer has this file)
    //   - only remote differs from the baseline -> remote changed -> pull
    //     it (download, or delete local if remote no longer has it)
    //   - both differ from the baseline (or this path has never been seen
    //     before on both sides at once) -> genuine conflict -> whichever
    //     side's modification time is newer wins, matching "sync the
    //     latest changes"
    // This makes the remote directory the hub multiple devices converge
    // through: device A's upload becomes device B's "remote changed" on
    // B's next reconcile, and the same for deletes.
    //
    // Safety guard: if the local root itself is missing (trashed, drive
    // unmounted, ...), that is NOT "every file under it was deleted" —
    // propagating that literally would wipe the whole remote directory.
    // Stop syncing this folder instead (removeRemoteFolder) and leave the
    // remote untouched.
    private func reconcileRemoteFolder(_ folder: RemoteFolder) async {
        guard ws.isConnected() else { return }
        // As reconcile(): a pass outliving its folder, or the device it
        // started on, stops. One kept running after a Disconnect and a
        // switch to a new device used to go on downloading and uploading
        // into the folders of the new one.
        let domainAtStart = settings?.domain
        // One pass per folder at a time: the 60-second poll, the watcher's
        // debounce and a fresh add can all ask while a big tree is still
        // being hashed, and two passes reading the same baseline would
        // each act on the same differences.
        guard !remoteFoldersBusy.contains(folder.id) else { return }
        remoteFoldersBusy.insert(folder.id)
        defer { remoteFoldersBusy.remove(folder.id) }

        remoteErrorRetryTasks[folder.id]?.cancel()
        remoteErrorRetryTasks[folder.id] = nil

        var isDir: ObjCBool = false
        guard FileManager.default.fileExists(atPath: folder.localURL.path, isDirectory: &isDir), isDir.boolValue else {
            print("Remote folder's local root is gone (\(folder.localURL.path)) — stopping sync, remote left untouched.")
            removeRemoteFolder(folder)
            return
        }
        // As reconcile() - first, and never holding the pass up: upload only
        // (issue #132) before anything could be deleted, and Images (issue
        // #192) before anything is sent.
        await applyRequestsBeforePass(folder.id)
        // Upload only asked for and not acknowledged yet (it failed, or the
        // device can't yet): no delete goes to the device until it is. A
        // file deleted here keeps its baseline, and its delete goes once the
        // device has the flag - which then refuses it and keeps the file
        // (SyncPaths.keptOnDevice). As otc-sync's holdDeletes.
        let holdDeletes = uploadOnlyPending[folder.id] == true

        // Guards against a real prefix-collision risk in the recursive
        // listing below: an unanchored "/subdir" would also match a
        // sibling like "/subdir2/file.txt" (it's used as a regex prefix
        // server-side — see dao.GetFilesByPath's recursive branch), so this
        // needs the trailing "/" to only ever match this folder's own
        // descendants. Same reasoning as remotePrefix in the upload-side
        // reconcile() above.
        let remotePrefix = folder.remotePath.hasSuffix("/") ? folder.remotePath : folder.remotePath + "/"

        do {
            let listingStart = Date()
            syncLog.info("two-way \(folder.remotePath, privacy: .public): listing")
            let resp = try await ws.request { req in
                var lf = ListFiles()
                lf.path = remotePrefix
                lf.recursive = true
                req.payload = .reqListFiles(lf)
            }
            if resp.error {
                syncLog.error("two-way \(folder.remotePath, privacy: .public): listing refused: \(resp.errorMessage, privacy: .public)")
                updateRemoteState(folder.id, .error(resp.errorMessage.isEmpty ? "Could not list remote files" : resp.errorMessage))
                scheduleRemoteErrorRetry(for: folder)
                return
            }
            syncLog.info("two-way \(folder.remotePath, privacy: .public): listing answered in \(Date().timeIntervalSince(listingStart), format: .fixed(precision: 1))s")
            var remoteByRelative: [String: Msg_File] = [:]
            if case .respListOfFiles(let lof) = resp.payload {
                var outside = 0
                for file in lof.files {
                    // The device keeps paths as clients sent them: one
                    // like "<prefix>../../x" would be written outside the
                    // folder here, so only paths inside it are taken.
                    guard let relative = SyncPaths.safeRelative(file.path, under: remotePrefix) else {
                        outside += 1
                        continue
                    }
                    if SyncPaths.isExcludedFromSync(relative) { continue }
                    remoteByRelative[relative] = file
                }
                if outside > 0 {
                    syncLog.error("two-way \(folder.remotePath, privacy: .public): \(outside) device entries outside the folder ignored")
                }
            }

            let localRoot = folder.localURL.standardizedFileURL.path
            // The relative paths are worked out off the main actor too.
            let (scan, localByRelative, present) = await Task.detached(priority: .utility) { () -> (LocalScan, [String: URL], Set<String>) in
                let scan = Self.enumerateFilesRecursively(at: folder.localURL)
                var byRelative: [String: URL] = [:]
                var present = Set<String>()
                for url in scan.urls {
                    let full = url.standardizedFileURL.path
                    present.insert(full)
                    guard full.hasPrefix(localRoot) else { continue }
                    var relative = String(full.dropFirst(localRoot.count))
                    if relative.hasPrefix("/") { relative.removeFirst() }
                    byRelative[relative] = url
                }
                return (scan, byRelative, present)
            }.value
            // A folder that can't be listed (permission, the volume going)
            // looks empty: every synced file would have been deleted from
            // the device.
            guard scan.rootReadable else {
                throw NSError(domain: "sync.scan", code: 1, userInfo: [NSLocalizedDescriptionKey: "Can't read the folder on this Mac"])
            }
            if scan.complete { await pruneHashCache(folder.id, keeping: present) }

            // Hashing is the slow part, so only do it for what's actually
            // on disk right now — remote's hash comes for free from the
            // listing above. A file that can't be read is left out of the
            // comparison entirely: treating it as "not here" would fetch
            // (and overwrite) something that is here, just unreadable.
            await loadSyncedIfNeeded(folder.id)
            let storedSynced = lastSyncedByRemoteFolder[folder.id] ?? [:]
            // A record written before paths were checked may hold one
            // that leads outside the folder, or a dotfile that came down
            // from the device: they leave with the next save.
            let lastSynced = storedSynced.filter { SyncPaths.isSafeRelative($0.key) && !SyncPaths.isExcludedFromSync($0.key) }
            var localHashes: [String: String] = [:]
            // What each hashed file looked like then, checked again just
            // before a download replaces it or a delete trashes it.
            var localStamps: [String: LocalStamp] = [:]
            var unreadable: Set<String> = []
            // Only here, not on the device and never synced: an upload
            // whatever its content, so it's hashed when it is sent, not
            // before anything starts (see reconcile()).
            var newLocal: Set<String> = []
            var lastShown = Date.distantPast
            // Sizes as the check found them, for the progress bar below.
            var localSizes: [String: Int64] = [:]
            // The cache is looked at off the main actor, as in reconcile().
            await loadHashCacheIfNeeded(folder.id)
            let total = localByRelative.count
            let checks = await Task.detached(priority: .utility) { [cache = localHashCache[folder.id] ?? [:]] () -> [(relative: String, check: CacheCheck)] in
                let files = Array(localByRelative)
                let checks = await Self.checkAgainstCache(files.map(\.value), cache: cache) { i, url in
                    await self.updateRemoteState(folder.id, .scanning(progress: 0, currentFile: "Checking \(i + 1)/\(total) · \(url.lastPathComponent)"))
                }
                return zip(files, checks).map { ($0.key, $1) }
            }.value
            localSizes.reserveCapacity(checks.count)
            for (i, item) in checks.enumerated() {
                let relative = item.relative, url = item.check.url
                if let size = item.check.size { localSizes[relative] = Int64(size) }
                if remoteByRelative[relative] == nil && lastSynced[relative] == nil {
                    newLocal.insert(relative)
                    continue
                }
                if let hit = item.check.hit {
                    localHashes[relative] = hit
                    localStamps[relative] = .present(size: item.check.size ?? -1, modified: item.check.modified ?? .distantPast)
                    if i % 1000 == 999 { await Task.yield() }
                    continue
                }
                // Issue #138: which file is being checked, see reconcile().
                if Date().timeIntervalSince(lastShown) > 0.3 {
                    lastShown = Date()
                    updateRemoteState(folder.id, .scanning(progress: 0, currentFile: "Checking \(i + 1)/\(total) · \(url.lastPathComponent)"))
                }
                do {
                    let entry = try await cachedHashEntry(for: url, folderId: folder.id)
                    localHashes[relative] = entry.hash
                    localStamps[relative] = .present(size: entry.size, modified: entry.modified)
                } catch {
                    unreadable.insert(relative)
                    if unreadable.count <= 5 {
                        syncLog.error("cannot hash \(relative, privacy: .public): \(error.localizedDescription, privacy: .public)")
                    }
                }
            }
            syncLog.info("two-way \(folder.remotePath, privacy: .public): remote=\(remoteByRelative.count) local=\(localByRelative.count) hashed=\(localHashes.count) new=\(newLocal.count) unreadable=\(unreadable.count) baseline=\(self.lastSyncedByRemoteFolder[folder.id]?.count ?? 0)")

            let allRelativePaths = Set(remoteByRelative.keys).union(localByRelative.keys).union(lastSynced.keys)
            // Under a directory that couldn't be read: unknown here, not
            // deleted - left alone, as an unreadable file is.
            let failedRelatives = scan.failed.compactMap { url -> String? in
                let full = url.standardizedFileURL.path
                return full.hasPrefix(localRoot + "/") ? String(full.dropFirst(localRoot.count + 1)) : nil
            }
            if !failedRelatives.isEmpty {
                for dir in failedRelatives.prefix(5) {
                    syncLog.error("two-way \(folder.remotePath, privacy: .public): cannot read \(dir, privacy: .public) - left as it is")
                }
                for relative in allRelativePaths where failedRelatives.contains(where: { relative == $0 || relative.hasPrefix($0 + "/") }) {
                    unreadable.insert(relative)
                }
            }

            // Conflicts (both sides changed the same file): the losing
            // version is kept as a "(conflict …)" copy next to it, which
            // then syncs like any new file, instead of being overwritten.
            // As otc-sync's actDownloadKeepLocal / actUploadKeepRemote.
            enum ActionKind: Equatable { case upload, download, deleteLocal, deleteRemote, downloadKeepLocal, uploadKeepRemote(remoteHash: String) }
            // hash carries the already-computed local hash through to the
            // .upload case below, purely to avoid hashing the same file
            // twice (issue #58's HasFile check needs it anyway); for
            // .deleteRemote it is the device's hash, recorded if the device
            // keeps the file (upload only).
            var actions: [(relative: String, kind: ActionKind, size: Int, hash: String?)] = []
            var newSynced = lastSynced
            // Deletes waiting for upload only (holdDeletes).
            var deletesHeld = 0
            // The folders local deletions emptied: removed after the pass
            // (SyncPaths.removeEmptiedFolders).
            var emptiedFolders = Set<String>()

            for relative in allRelativePaths {
                if unreadable.contains(relative) { continue }
                if newLocal.contains(relative) {
                    actions.append((relative, .upload, 0, nil))
                    continue
                }
                let localHash = localHashes[relative]
                let remoteFile = remoteByRelative[relative]
                let remoteHash = remoteFile?.hash
                let last = lastSynced[relative]
                // What each side had after the last pass: the same, except
                // for a file the device kept when it was deleted here
                // (SyncPaths.keptOnDevice) - and none at all for one whose
                // folder is no longer upload only.
                let (lastLocal, lastRemote) = SyncPaths.baselines(last, listedUploadOnly: remoteFile?.uploadOnly)

                // Issue #141: listed without a hash means the device has
                // lost this file's content. A copy here is sent again, which
                // restores it; with none here there is nothing to fetch -
                // never a download (it failed on every pass, forever) nor a
                // delete. As otc-sync.
                if let remoteFile, remoteFile.hash.isEmpty {
                    if let localHash {
                        actions.append((relative, .upload, 0, localHash))
                        newSynced[relative] = localHash
                    }
                    continue
                }

                if localHash == remoteHash {
                    // Already in agreement — includes both being nil,
                    // which can't really land in allRelativePaths, but
                    // harmless either way.
                    if let localHash { newSynced[relative] = localHash } else { newSynced.removeValue(forKey: relative) }
                    continue
                }

                let localChanged = localHash != lastLocal
                let remoteChanged = remoteHash != lastRemote
                if !localChanged && !remoteChanged {
                    // Deleted here and kept by the device (an upload-only
                    // folder), and neither side changed it since: not
                    // downloaded again, nor the delete sent again. The
                    // record keeps saying so. As otc-sync.
                    continue
                }
                // Genuine conflict (both sides moved since the baseline, or
                // there's no baseline at all and they already disagree) —
                // newest modification time wins.
                let conflict = localChanged && remoteChanged

                let remoteWins: Bool
                if conflict {
                    let localDate = localByRelative[relative].flatMap {
                        try? $0.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate
                    }
                    let remoteDate = remoteFile?.modified.date
                    switch (localDate, remoteDate) {
                    case (nil, _): remoteWins = true
                    case (_, nil): remoteWins = false
                    case let (l?, r?): remoteWins = r > l
                    }
                } else {
                    // Exactly one side changed — that's the one to propagate.
                    remoteWins = remoteChanged
                }

                if actions.count < 20 {
                    syncLog.info("plan \(relative, privacy: .public): local=\(localHash?.prefix(8) ?? "-", privacy: .public) remote=\(remoteHash?.prefix(8) ?? "-", privacy: .public) last=\(last?.prefix(8) ?? "-", privacy: .public) conflict=\(conflict) remoteWins=\(remoteWins)")
                }
                // Both sides have different content: whichever loses is kept.
                let bothHaveContent = localHash != nil && remoteHash != nil
                if remoteWins {
                    if conflict && bothHaveContent, let remoteHash {
                        actions.append((relative, .downloadKeepLocal, Int(remoteFile?.fileSize ?? 0), remoteHash))
                        newSynced[relative] = remoteHash
                    } else if let remoteHash {
                        actions.append((relative, .download, Int(remoteFile?.fileSize ?? 0), remoteHash))
                        newSynced[relative] = remoteHash
                    } else {
                        actions.append((relative, .deleteLocal, 0, nil))
                        newSynced.removeValue(forKey: relative)
                    }
                } else {
                    if conflict && bothHaveContent, let localHash, let remoteHash {
                        actions.append((relative, .uploadKeepRemote(remoteHash: remoteHash), 0, localHash))
                        newSynced[relative] = localHash
                    } else if let localHash {
                        actions.append((relative, .upload, 0, localHash))
                        newSynced[relative] = localHash
                    } else if holdDeletes {
                        // Not upload only on the device yet: the delete
                        // waits, and newSynced keeps its baseline.
                        deletesHeld += 1
                    } else {
                        // The device's hash goes with it: kept there (upload
                        // only), it is what the record holds.
                        actions.append((relative, .deleteRemote, 0, remoteHash))
                        newSynced.removeValue(forKey: relative)
                    }
                }
            }

            // Mass-deletion guard: a device that was wiped or set up again
            // looks exactly like one whose owner deleted everything - every
            // synced, unchanged file "gone on the device" - and this used to
            // delete the whole folder here to match. When a pass would
            // delete more than a handful of files here and a large share of
            // the folder, the device is taken to have lost them instead:
            // they are sent back to it, and nothing here is touched.
            let localDeletes = actions.filter { $0.kind == .deleteLocal }.count
            var guardNote: String?
            if localDeletes > Self.massDeleteMin && localDeletes * 4 > max(localByRelative.count, 1) {
                syncLog.error("two-way \(folder.remotePath, privacy: .public): \(localDeletes) files gone from the device at once - restoring them instead of deleting them here")
                actions = actions.map { a in
                    guard a.kind == .deleteLocal, localByRelative[a.relative] != nil else { return a }
                    newSynced.removeValue(forKey: a.relative)
                    return (a.relative, .upload, 0, nil)
                }
                guardNote = "\(localDeletes) files had disappeared from the device - restored them from this Mac instead of deleting them here"
            }
            // Local deletes first: a rename on another client that only
            // changes case ("Photo.jpg" -> "photo.jpg") then trashes the
            // old name before the new one comes down, in one pass.
            actions = actions.filter { $0.kind == .deleteLocal } + actions.filter { $0.kind != .deleteLocal }

            syncLog.info("two-way \(folder.remotePath, privacy: .public): \(actions.count) action(s) - \(actions.filter { $0.kind == .download }.count) download, \(actions.filter { $0.kind == .upload }.count) upload, \(actions.filter { $0.kind == .deleteLocal }.count) delete local, \(actions.filter { $0.kind == .deleteRemote }.count) delete remote, \(deletesHeld) delete(s) waiting for upload only")
            if !actions.isEmpty {
                // Of the whole folder, as reconcile(): every path on either
                // side counts, and what already agrees is done - "610 of
                // 6,398" and a bar by bytes, not "4 of" this pass's actions.
                // Sizes from the check above, not every file looked at again
                // on the main actor.
                func bytes(_ relative: String) -> Int64 {
                    localSizes[relative] ?? remoteByRelative[relative]?.fileSize ?? 0
                }
                let folderPaths = Set(localByRelative.keys).union(remoteByRelative.keys)
                let folderCount = max(folderPaths.count, actions.count)
                let alreadyAgree = folderCount - actions.count
                let totalBytes = max(folderPaths.reduce(Int64(0)) { $0 + bytes($1) }, 1)
                var bytesDone = totalBytes - actions.reduce(Int64(0)) { $0 + bytes($1.relative) }
                for (i, action) in actions.enumerated() {
                    guard remoteFolders.contains(where: { $0.id == folder.id }), settings?.domain == domainAtStart else {
                        syncLog.info("two-way \(folder.remotePath, privacy: .public): folder removed or device changed - pass stopped")
                        return
                    }
                    // As reconcile(): a dropped link ends the pass; what's
                    // left keeps its baseline and goes on reconnect.
                    guard ws.isConnected() else {
                        for rest in actions[i...] {
                            if let prior = lastSynced[rest.relative] { newSynced[rest.relative] = prior } else { newSynced.removeValue(forKey: rest.relative) }
                        }
                        SyncPaths.removeEmptiedFolders(emptiedFolders, root: folder.localURL.standardizedFileURL.path)
                        if newSynced != storedSynced || syncedUnsaved.contains(folder.id) { saveSynced(folder.id, newSynced) }
                        lastSyncedByRemoteFolder[folder.id] = newSynced
                        updateRemoteState(folder.id, .error("Device offline - will resume"))
                        saveHashCache(folder.id)
                        return
                    }
                    updateRemoteState(folder.id, .scanning(progress: Double(max(bytesDone, 0)) / Double(totalBytes), currentFile: "\(alreadyAgree + i + 1)/\(folderCount) · " + (action.relative as NSString).lastPathComponent))
                    defer { bytesDone += bytes(action.relative) }
                    let localURL = folder.localURL.appendingPathComponent(action.relative)
                    // Second line behind safeRelative: nothing a pass does
                    // lands outside the folder.
                    guard localURL.standardizedFileURL.path.hasPrefix(localRoot + "/") else {
                        syncLog.error("two-way \(folder.remotePath, privacy: .public): \(action.relative, privacy: .public) is outside the folder - skipped")
                        if let prior = lastSynced[action.relative] { newSynced[action.relative] = prior } else { newSynced.removeValue(forKey: action.relative) }
                        continue
                    }
                    let remotePath = remotePrefix + action.relative
                    do {
                        switch action.kind {
                        case .upload:
                            // As reconcile(): a new file is hashed before the
                            // check, not inside upload().
                            let hash: String
                            if let known = action.hash { hash = known } else { hash = try await cachedHash(for: localURL, folderId: folder.id) }
                            guard remoteFolders.contains(where: { $0.id == folder.id }), settings?.domain == domainAtStart else {
                                syncLog.info("two-way \(folder.remotePath, privacy: .public): folder removed or device changed - pass stopped")
                                return
                            }
                            newSynced[action.relative] = try await upload(localURL, to: remotePath, knownHash: hash, folderId: folder.id)
                        case .download:
                            // The plan saw nothing at this name, yet something
                            // is there: a name that differs only by case (APFS
                            // ignores case), or a file created during the pass.
                            // Overwriting it, and then deleting the other name,
                            // lost the file. Left alone; the next pass decides.
                            if localByRelative[action.relative] == nil, FileManager.default.fileExists(atPath: localURL.path) {
                                throw NSError(domain: "sync.download", code: 6, userInfo: [NSLocalizedDescriptionKey: "another file is already at this name here - left alone"])
                            }
                            try await download(remotePath, to: localURL, expectedHash: action.hash, expectLocal: localStamps[action.relative] ?? .absent, folderId: folder.id)
                        case .downloadKeepLocal:
                            // This Mac's version first, under its conflict
                            // name; only then the device's over the original.
                            let copy = Self.conflictURL(for: localURL, from: Host.current().localizedName)
                            try FileManager.default.moveItem(at: localURL, to: copy)
                            syncLog.info("conflict on \(action.relative, privacy: .public): this Mac's version kept as \(copy.lastPathComponent, privacy: .public)")
                            // Nothing may be at the name now: something that
                            // appeared during the download is not overwritten.
                            try await download(remotePath, to: localURL, expectedHash: action.hash, expectLocal: .absent, folderId: folder.id)
                        case .uploadKeepRemote(let remoteHash):
                            let copy = Self.conflictURL(for: localURL, from: nil)
                            try await download(remotePath, to: copy, expectedHash: remoteHash, folderId: folder.id)
                            syncLog.info("conflict on \(action.relative, privacy: .public): the other version kept as \(copy.lastPathComponent, privacy: .public)")
                            newSynced[action.relative] = try await upload(localURL, to: remotePath, knownHash: action.hash, folderId: folder.id)
                        case .deleteRemote:
                            if try await delete(remotePath) == .keptOnDevice, let hash = action.hash {
                                // The folder is upload only on the device:
                                // the file stays there, and stays deleted
                                // here (SyncPaths.recordBaselines).
                                newSynced[action.relative] = SyncPaths.keptOnDevice(hash)
                            }
                        // To the Trash, not gone: recoverable if a deletion on
                        // the device was a mistake.
                        case .deleteLocal:
                            guard Self.localMatches(localURL, localStamps[action.relative] ?? .absent) else {
                                throw NSError(domain: "sync.delete", code: 2, userInfo: [NSLocalizedDescriptionKey: "changed here during the pass - not deleted"])
                            }
                            try FileManager.default.trashItem(at: localURL, resultingItemURL: nil)
                            emptiedFolders.insert(localURL.deletingLastPathComponent().standardizedFileURL.path)
                        }
                    } catch {
                        // Revert this one path back to its pre-reconcile
                        // baseline so a transient failure gets retried next
                        // pass instead of being mistaken for "now agrees".
                        if let prior = lastSynced[action.relative] {
                            newSynced[action.relative] = prior
                        } else {
                            newSynced.removeValue(forKey: action.relative)
                        }
                        print("Error syncing \(action.relative): \(error)")
                    }
                }
            }

            let removedFolders = SyncPaths.removeEmptiedFolders(emptiedFolders, root: folder.localURL.standardizedFileURL.path)
            if removedFolders > 0 {
                syncLog.info("two-way \(folder.remotePath, privacy: .public): removed \(removedFolders) folder(s) left empty by deletions on the device")
            }
            if newSynced != storedSynced || syncedUnsaved.contains(folder.id) { saveSynced(folder.id, newSynced) }
            lastSyncedByRemoteFolder[folder.id] = newSynced
            // The guard's note stays on the folder until the next pass, so
            // the owner learns the device had lost those files.
            updateRemoteState(folder.id, guardNote.map { .error($0) } ?? .watching)
        } catch {
            syncLog.error("two-way \(folder.remotePath, privacy: .public): failed: \(error.localizedDescription, privacy: .public)")
            updateRemoteState(folder.id, .error(error.localizedDescription))
            scheduleRemoteErrorRetry(for: folder)
        }
        saveHashCache(folder.id)
    }

    private func startRemoteWatcher(for folder: RemoteFolder) {
        guard remoteFolderWatchers[folder.id] == nil else { return }
        let watcher = FolderWatcher { [weak self] _ in
            guard let self else { return }
            Task { @MainActor in
                // Whole-folder debounce, not per-file: a single
                // reconcileRemoteFolder pass already handles a batch of
                // local changes correctly (and cheaply) in one go, so
                // there's no need for the per-file granularity
                // TrackedFolder's upload-only side uses.
                self.remoteFolderDebounce[folder.id]?.cancel()
                self.remoteFolderDebounce[folder.id] = Task { [weak self] in
                    try? await Task.sleep(for: Self.debounceInterval)
                    guard !Task.isCancelled, let self else { return }
                    guard let current = self.remoteFolders.first(where: { $0.id == folder.id }) else { return }
                    await self.reconcileRemoteFolder(current)
                }
            }
        }
        watcher.start(paths: [folder.localURL.path])
        remoteFolderWatchers[folder.id] = watcher
    }

    private func scheduleRemoteErrorRetry(for folder: RemoteFolder) {
        remoteErrorRetryTasks[folder.id]?.cancel()
        remoteErrorRetryTasks[folder.id] = Task { [weak self] in
            try? await Task.sleep(for: Self.errorRetryInterval)
            guard !Task.isCancelled, let self else { return }
            self.remoteErrorRetryTasks[folder.id] = nil
            await self.reconcileRemoteFolder(folder)
        }
    }

    /// `expectedHash` is the hash the listing gave for this path: the
    /// content is checked against it before anything touches the disk. A
    /// device whose blob for the file had gone missing used to answer with
    /// empty content and no error; written as a 0-byte file, the next
    /// pass uploaded that emptiness back over the device's row.
    ///
    /// Issue #168: this reads the file in chunks with ReadFile instead of
    /// one whole-file GetFile. GetFile converted HEIC to JPEG on the
    /// device, so the download never matched the listed hash and was
    /// fetched again on every pass; ReadFile always sends the original
    /// bytes. Chunks also keep a multi-GB file out of memory on both
    /// ends and out of a single websocket message. They go to a
    /// ".otc-part" file next to the destination, which is only renamed
    /// into place once the whole content matches the hash.
    /// Where the losing version of a conflicting file is kept: "report
    /// (conflict from MacBook 2026-10-04 20.15).txt" next to it, numbered if
    /// taken. Same naming as otc-sync's conflictPath.
    static func conflictURL(for url: URL, from: String?, at date: Date = Date()) -> URL {
        let dir = url.deletingLastPathComponent()
        var ext = url.pathExtension
        var stem = url.deletingPathExtension().lastPathComponent
        if stem.isEmpty { stem = url.lastPathComponent; ext = "" }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyy-MM-dd HH.mm"
        let label = from.map { "conflict from \($0) \(f.string(from: date))" } ?? "conflict \(f.string(from: date))"
        let suffix = ext.isEmpty ? "" : ".\(ext)"
        var candidate = dir.appendingPathComponent("\(stem) (\(label))\(suffix)")
        var n = 2
        while FileManager.default.fileExists(atPath: candidate.path) {
            candidate = dir.appendingPathComponent("\(stem) (\(label) \(n))\(suffix)")
            n += 1
        }
        return candidate
    }

    /// `expectLocal`, when given, is what the pass saw at `dest` when it
    /// planned this: anything else there now (an edit made since) is not
    /// replaced. With `folderId` the content's hash goes into that
    /// folder's hash cache, so the next pass doesn't read it all again.
    private func download(_ remotePath: String, to dest: URL, expectedHash: String? = nil, expectLocal: LocalStamp? = nil, folderId: UUID? = nil) async throws {
        let part = dest.appendingPathExtension("otc-part")
        let sink = try await Task.detached(priority: .utility) {
            try FileManager.default.createDirectory(at: dest.deletingLastPathComponent(), withIntermediateDirectories: true)
            return try ChunkSink(url: part)
        }.value
        var done = false
        defer {
            sink.close()
            if !done { try? FileManager.default.removeItem(at: part) }
        }

        var offset: Int64 = 0
        var last: Msg_FileChunk?
        while true {
            let resp = try await ws.request { req in
                var rf = Msg_ReadFile()
                rf.path = remotePath
                rf.offset = offset
                rf.length = Self.chunkSize
                req.payload = .reqReadFile(rf)
            }
            if resp.error {
                throw NSError(domain: "sync.download", code: 1, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "download rejected" : resp.errorMessage])
            }
            guard case .respFileChunk(let chunk) = resp.payload else {
                throw NSError(domain: "sync.download", code: 2, userInfo: [NSLocalizedDescriptionKey: "unexpected response"])
            }
            // Every chunk carries the file's current hash: a different one
            // means the file changed on the device mid-transfer, so what
            // was already written belongs to another version. Stop here;
            // the next pass starts over with the new listing.
            if let expectedHash, !expectedHash.isEmpty, chunk.hash != expectedHash {
                throw NSError(domain: "sync.download", code: 4, userInfo: [NSLocalizedDescriptionKey: "the file changed on the device during the download - not written"])
            }
            if chunk.offset != offset || (chunk.data.isEmpty && offset < chunk.size) || offset + Int64(chunk.data.count) > chunk.size {
                throw NSError(domain: "sync.download", code: 5, userInfo: [NSLocalizedDescriptionKey: "the device sent a chunk that doesn't fit the file - not written"])
            }
            let data = chunk.data
            try await Task.detached(priority: .utility) { try sink.write(data) }.value
            offset += Int64(data.count)
            last = chunk
            if offset >= chunk.size { break }
        }
        guard let file = last else { return }

        let written = try await Task.detached(priority: .utility) { () -> HashEntry? in
            let got = sink.finish()
            if let expectedHash, !expectedHash.isEmpty, got != expectedHash {
                throw NSError(domain: "sync.download", code: 3, userInfo: [NSLocalizedDescriptionKey: "the device sent \(file.size) bytes that don't match the file's hash - not written"])
            }
            // Checked last, right before the rename: the pass leaves the
            // path for the next one, which sees the edit and decides again.
            if let expectLocal, !Self.localMatches(dest, expectLocal) {
                throw NSError(domain: "sync.download", code: 7, userInfo: [NSLocalizedDescriptionKey: "changed here during the pass - not overwritten"])
            }
            if FileManager.default.fileExists(atPath: dest.path) {
                _ = try FileManager.default.replaceItemAt(dest, withItemAt: part)
            } else {
                try FileManager.default.moveItem(at: part, to: dest)
            }
            // Issue #134: the file keeps the dates it has on the device
            // (which are the dates it had where it was uploaded from),
            // rather than "now". The modification date is also what the
            // conflict rule above compares, so it must be the device's.
            var attrs: [FileAttributeKey: Any] = [:]
            if file.hasCreated { attrs[.creationDate] = file.created.date }
            if file.hasModified { attrs[.modificationDate] = file.modified.date }
            if !attrs.isEmpty { try? FileManager.default.setAttributes(attrs, ofItemAtPath: dest.path) }
            // Cached only when the file reads back with the device's size
            // and date (not on a volume with coarse dates, nor when the
            // date could not be set): a wrong cached hash would hide a
            // later edit here from the next pass.
            guard file.hasModified,
                  let v = try? URL(fileURLWithPath: dest.path).resourceValues(forKeys: [.fileSizeKey, .contentModificationDateKey]),
                  let size = v.fileSize, Int64(size) == file.size,
                  let modified = v.contentModificationDate, abs(modified.timeIntervalSince(file.modified.date)) < 0.001 else { return nil }
            return HashEntry(size: size, modified: modified, hash: got)
        }.value
        if let folderId, let written {
            await loadHashCacheIfNeeded(folderId)
            localHashCache[folderId, default: [:]][dest.standardizedFileURL.path] = written
            hashCacheDirty.insert(folderId)
        }
        done = true
    }

    /// Issue #168: the size of one ReadFile / UploadChunk transfer.
    nonisolated static let chunkSize: Int32 = 4 << 20

    // MARK: - Wire helpers

    // Issue #58: storage is deduplicated by hash server-side already (see
    // the Go files_manager.UploadFile/LinkFile doc comments) — this is
    // what actually skips re-sending the bytes for a file the device
    // already has under some other path, rather than relying on that
    // dedup only kicking in *after* the transfer. `knownHash` lets a
    // caller that already hashed this file for its own diffing (every
    // call site here has) skip hashing it a second time.
    /// Returns the content's hash - computed here (and cached, given the
    /// folder) when the caller didn't need it to decide.
    @discardableResult
    private func upload(_ url: URL, to remotePath: String, knownHash: String? = nil, folderId: UUID? = nil) async throws -> String {
        let hash: String
        if let knownHash {
            hash = knownHash
        } else if let folderId {
            hash = try await cachedHash(for: url, folderId: folderId)
        } else {
            hash = try await Task.detached(priority: .utility) {
                try Self.sha256Hex(of: url)
            }.value
        }

        let hasResp = try await ws.request { req in
            var hf = Msg_HasFile()
            hf.hash = hash
            req.payload = .reqHasFile(hf)
        }
        let dates = try? url.resourceValues(forKeys: [.creationDateKey, .contentModificationDateKey])
        let created = SwiftProtobuf.Google_Protobuf_Timestamp(date: dates?.creationDate ?? Date())
        // Issue #134: the device keeps this as the file's modified time,
        // so the same file synced down elsewhere gets the same dates.
        let modified = SwiftProtobuf.Google_Protobuf_Timestamp(date: dates?.contentModificationDate ?? Date())

        if case .respFileExists(let fe) = hasResp.payload, fe.exists {
            let resp = try await ws.request { req in
                var lf = Msg_LinkFile()
                lf.hash = hash
                lf.path = remotePath
                lf.forceOverride = true
                lf.created = created
                lf.modified = modified
                req.payload = .reqLinkFile(lf)
            }
            if resp.error {
                throw NSError(domain: "sync.upload", code: 1, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "link rejected" : resp.errorMessage])
            }
            return hash
        }

        // Issue #168: the bytes go in 4 MiB chunks (BeginUpload,
        // UploadChunk..., FinishUpload) rather than one whole-file
        // UploadFile, so neither the Mac nor the device holds the whole
        // file in memory, and no single websocket message carries it.
        // Reading happens off the main actor, one chunk at a time - the
        // same UI-freezing concern as the hashing in reconcile().
        let size = try await Task.detached(priority: .utility) { () -> Int64 in
            let attrs = try FileManager.default.attributesOfItem(atPath: url.path)
            return (attrs[.size] as? NSNumber)?.int64Value ?? 0
        }.value
        let begin = try await ws.request { req in
            var bu = Msg_BeginUpload()
            bu.path = remotePath
            bu.size = size
            bu.created = created
            bu.modified = modified
            bu.forceOverride = true
            bu.cloudID = ""
            req.payload = .reqBeginUpload(bu)
        }
        // A server-side rejection (auth failure, disk full, etc.) still
        // comes back as a normal response, not a thrown error from
        // `request` — checking resp.error is the only way to actually
        // notice the file wasn't saved, instead of silently caching it as
        // synced and never trying again.
        try Self.throwIfRejected(begin)
        guard case .respUploadStarted(let started) = begin.payload else {
            throw NSError(domain: "sync.upload", code: 2, userInfo: [NSLocalizedDescriptionKey: "unexpected response"])
        }
        let uploadID = started.uploadID

        let fh = try FileHandle(forReadingFrom: url)
        defer { try? fh.close() }
        var offset: Int64 = 0
        while offset < size {
            let data = try await Task.detached(priority: .utility) {
                try fh.read(upToCount: Int(Self.chunkSize)) ?? Data()
            }.value
            // The file shrank since it was measured: what the device
            // would get no longer matches the hash, so let the next pass
            // start over with the file as it is then.
            if data.isEmpty {
                throw NSError(domain: "sync.upload", code: 3, userInfo: [NSLocalizedDescriptionKey: "the file changed while uploading"])
            }
            let chunkOffset = offset
            let resp = try await ws.request { req in
                var uc = Msg_UploadChunk()
                uc.uploadID = uploadID
                uc.offset = chunkOffset
                uc.data = data
                req.payload = .reqUploadChunk(uc)
            }
            // The device takes chunks strictly in order; an "out_of_order"
            // answer (or any other rejection) ends this attempt and the
            // next pass starts the upload again from the beginning.
            try Self.throwIfRejected(resp)
            offset += Int64(data.count)
            if case .respUploadProgress(let p) = resp.payload, p.received != offset {
                throw NSError(domain: "sync.upload", code: 4, userInfo: [NSLocalizedDescriptionKey: "the device received \(p.received) bytes, expected \(offset)"])
            }
        }

        let resp = try await ws.request { req in
            var fu = Msg_FinishUpload()
            fu.uploadID = uploadID
            fu.sha256 = hash
            req.payload = .reqFinishUpload(fu)
        }
        try Self.throwIfRejected(resp)
        return hash
    }

    private static func throwIfRejected(_ resp: Resp) throws {
        if resp.error {
            throw NSError(domain: "sync.upload", code: 1, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "upload rejected" : resp.errorMessage])
        }
    }

    /// Makes a backup's folder on the device upload only (issue #132): the
    /// device then refuses deletes there and keeps the older version of a
    /// file when it changes. Sent at every start, so backups added before
    /// this get it too; harmless when already set. As otc-sync's
    /// markUploadOnly.
    private func markUploadOnly(_ folder: TrackedFolder) async {
        let path = remotePathFor(folder.url.path) + "/"
        let problem: String
        do {
            let resp = try await ws.request { req in
                var u = Msg_SetUploadOnly()
                u.path = path
                u.uploadOnly = true
                req.payload = .reqSetUploadOnly(u)
            }
            guard resp.error else {
                uploadOnlyOK.insert(folder.id)
                uploadOnlyErrors[folder.id] = nil
                return
            }
            problem = "upload only refused: \(resp.errorMessage)"
        } catch {
            problem = "could not make it upload only: \(error.localizedDescription)"
        }
        // A device on a release before #132 refuses it on every pass.
        guard uploadOnlyErrors[folder.id] != problem else { return }
        uploadOnlyErrors[folder.id] = problem
        syncLog.error("backup \(folder.url.path, privacy: .public): \(problem, privacy: .public)")
    }

    // MARK: - A folder's requests (issues #192 and #132)
    //
    // Keep out of Images and Upload only are asked for when a folder from
    // this Mac is added (AddFolderChooser's step 2), Images also later from
    // the row's eye. Each is kept with the folder as a request (the
    // kind's pending map, persisted with the stored folder) that is sent
    // for the folder's device path - at the start of its passes, at
    // connect, and at once when made from the row - until the device
    // acknowledges it. Then never again: unlike markUploadOnly, which makes
    // a backup upload only at every start, it is not re-sent, which would
    // undo a change made since from the web or a phone. As otc-sync's
    // engine/requests.go.
    //
    // Photos and videos in a folder kept out of Images still go to the
    // device and Files shows them, but the device never tags them,
    // searches them for faces or shows them in Images. An upload-only
    // folder keeps every older version of a file and refuses deletes; what
    // a two-way pass does with a refused delete is in reconcileRemoteFolder
    // (SyncPaths.keptOnDevice).

    /// One kind of request: where its state lives, what it sends, and how
    /// the device refuses it (otc-sync's requestKind).
    private struct RequestKind {
        let pending: ReferenceWritableKeyPath<SyncModel, [UUID: Bool]>
        let notes: ReferenceWritableKeyPath<SyncModel, [UUID: String]>
        let errors: ReferenceWritableKeyPath<SyncModel, [UUID: String]>
        let supported: ReferenceWritableKeyPath<SyncModel, Bool?>
        let tasks: ReferenceWritableKeyPath<SyncModel, [UUID: Task<Void, Never>]>
        /// Where a stored folder keeps it; nil for a backup, which never
        /// has it.
        let stored: WritableKeyPath<StoredFolder, Bool?>?
        let storedRemote: WritableKeyPath<StoredRemoteFolder, Bool?>
        /// The folder above decides: the request is dropped and the
        /// device's message shown on the row.
        let byParentCode: String
        /// "/" is never sent (the device refuses it): cleared as done.
        let skipsRoot: Bool
        /// Images: the device lists the folders it keeps out - read again
        /// after an answer that changes them, forgotten when it can't.
        let images: Bool
        /// What the log says could not be done to a path.
        let failure: (String) -> String
        let unsupportedLog: String
        let build: (String, Bool) -> Req.OneOf_Payload
    }

    private static let imagesRequest = RequestKind(
        pending: \.outOfImagesPending, notes: \.outOfImagesNotes, errors: \.outOfImagesErrors,
        supported: \.outOfImagesSupported, tasks: \.outOfImagesTasks,
        stored: \.outOfImages, storedRemote: \.outOfImages,
        byParentCode: "out_of_images_by_parent", skipsRoot: true, images: true,
        failure: { "keep \($0) out of Images, or show it there" },
        unsupportedLog: "the device can't keep folders out of Images yet (it needs an update)",
        build: { path, on in
            var m = Msg_SetOutOfImages()
            m.path = path
            m.outOfImages = on
            return .reqSetOutOfImages(m)
        })

    private static let uploadOnlyRequest = RequestKind(
        pending: \.uploadOnlyPending, notes: \.uploadOnlyNotes, errors: \.uploadOnlyRequestErrors,
        supported: \.uploadOnlySupported, tasks: \.uploadOnlyTasks,
        stored: nil, storedRemote: \.uploadOnly,
        // Only lifting it is refused that way (issue #186); never asked here.
        byParentCode: "locked_by_parent", skipsRoot: false, images: false,
        failure: { "make \($0) upload only" },
        unsupportedLog: "the device can't make folders upload only yet (it needs an update)",
        build: { path, on in
            var m = Msg_SetUploadOnly()
            m.path = path
            m.uploadOnly = on
            return .reqSetUploadOnly(m)
        })

    private static let requestKinds = [imagesRequest, uploadOnlyRequest]

    /// A row's Keep Out / Show confirmed: recorded, and sent now when
    /// connected.
    func setOutOfImages(_ id: UUID, keepOut: Bool) {
        guard requestDevicePath(id) != nil else { return }
        outOfImagesPending[id] = keepOut
        outOfImagesNotes[id] = nil
        outOfImagesErrors[id] = nil
        persistRequest(Self.imagesRequest, id)
        if ws.isConnected() { applyRequest(Self.imagesRequest, id) }
    }

    /// How folder `id` stands with Images, for its row.
    func outOfImagesState(for id: UUID) -> OutOfImagesState {
        guard let path = requestDevicePath(id) else { return .unknown }
        return SyncPaths.outOfImagesState(devicePath: path, folders: outOfImagesFolders,
                                          pending: outOfImagesPending[id], supported: outOfImagesSupported)
    }

    /// Folder `id`'s upload-only request on its way, for its row.
    func uploadOnlyState(for id: UUID) -> UploadOnlyRequestState {
        SyncPaths.uploadOnlyState(pending: uploadOnlyPending[id], supported: uploadOnlySupported)
    }

    /// The folder's device path, with the trailing slash; nil once removed.
    private func requestDevicePath(_ id: UUID) -> String? {
        var path: String
        if let f = folders.first(where: { $0.id == id }) {
            let root = deviceRootForImages ?? remoteDeviceRoot()
            deviceRootForImages = root
            path = remotePathFor(f.url.path, root: root)
        } else if let r = remoteFolders.first(where: { $0.id == id }) {
            path = r.remotePath
        } else {
            return nil
        }
        // One slash: a backup of a volume's root ends in one already.
        while path.hasSuffix("/") { path.removeLast() }
        return path + "/"
    }

    /// A pass's first step: the folder's pending requests, each waiting for
    /// one already under way rather than sending it twice. Upload only
    /// first: it is what protects the folder.
    private func applyRequestsBeforePass(_ id: UUID) async {
        for kind in [Self.uploadOnlyRequest, Self.imagesRequest] {
            guard self[keyPath: kind.pending][id] != nil, self[keyPath: kind.supported] != false else { continue }
            await applyRequest(kind, id).value
        }
    }

    /// Sends folder `id`'s pending request of `kind`, or returns the send
    /// already under way.
    @discardableResult
    private func applyRequest(_ kind: RequestKind, _ id: UUID) -> Task<Void, Never> {
        if let running = self[keyPath: kind.tasks][id] { return running }
        let task = Task { [weak self] in
            await self?.sendRequest(kind, id)
            self?[keyPath: kind.tasks][id] = nil
        }
        self[keyPath: kind.tasks][id] = task
        return task
    }

    /// Every pending request of `kind`, at connect.
    private func applyPendingRequests(_ kind: RequestKind) {
        for id in self[keyPath: kind.pending].keys { applyRequest(kind, id) }
    }

    /// Until the device has acknowledged what is pending now. The answer
    /// decides what happens to the request:
    /// - ok: cleared - only while it is still the one sent, so a change
    ///   made meanwhile from the row goes next;
    /// - unknown_payload (a device before the request existed): kept, and
    ///   the device marked as unable - the row says it needs an update,
    ///   and the request goes by itself after the update (the next
    ///   connect);
    /// - the kind's byParentCode (out_of_images_by_parent, locked_by_parent):
    ///   dropped, the device's message on the row - it can't happen until
    ///   the folder above changes;
    /// - anything else, a dropped link included: kept for the next pass,
    ///   the error logged once - unless the row changed it while this one
    ///   was under way: the change goes now (the row's own call found this
    ///   send running and left it to it).
    private func sendRequest(_ kind: RequestKind, _ id: UUID) async {
        // The value this send is through with (its request failed, or was
        // refused): it stops there unless the request changes meanwhile.
        var through: Bool?
        while let want = self[keyPath: kind.pending][id], want != through, self[keyPath: kind.supported] != false,
              ws.isConnected(), let path = requestDevicePath(id) {
            guard !(kind.skipsRoot && path == "/") else {
                // The device's whole library, which it refuses to keep
                // out: nothing to send, ever.
                clearRequest(kind, id, sent: want)
                return
            }
            let domain = settings?.domain
            let answer: Resp?
            var failure: String?
            do {
                answer = try await ws.request { req in req.payload = kind.build(path, want) }
            } catch {
                answer = nil
                failure = error.localizedDescription
            }
            // Removed, or another device, meanwhile: the answer isn't
            // about it.
            guard requestDevicePath(id) != nil, settings?.domain == domain else { return }
            through = want
            guard let answer else {
                noteRequestError(kind, id, path: path, failure ?? "no answer")
                continue
            }
            if Self.isUnknownPayload(answer) {
                syncLog.notice("\(kind.unsupportedLog, privacy: .public): \(path, privacy: .public) stays pending")
                self[keyPath: kind.supported] = false
                if kind.images { outOfImagesFolders = nil }
                return
            }
            if answer.error && answer.errorCode == kind.byParentCode {
                clearRequest(kind, id, sent: want)
                self[keyPath: kind.notes][id] = answer.errorMessage
                self[keyPath: kind.errors][id] = nil
                // The folder above that keeps it out, for the row.
                if kind.images { await refreshOutOfImages() }
                continue
            }
            if answer.error {
                noteRequestError(kind, id, path: path, answer.errorMessage.isEmpty ? "refused" : answer.errorMessage)
                continue
            }
            if self[keyPath: kind.supported] != true { self[keyPath: kind.supported] = true }
            self[keyPath: kind.errors][id] = nil
            self[keyPath: kind.notes][id] = nil
            clearRequest(kind, id, sent: want)
            through = nil
            if kind.images { await refreshOutOfImages() }
            // What is pending now - a change made while this one was under
            // way - goes next.
        }
    }

    /// The device answered `sent`: the request goes, unless it was changed
    /// meanwhile.
    private func clearRequest(_ kind: RequestKind, _ id: UUID, sent: Bool) {
        guard self[keyPath: kind.pending][id] == sent else { return }
        self[keyPath: kind.pending][id] = nil
        persistRequest(kind, id)
    }

    /// Writes folder `id`'s request of `kind` into its stored entry.
    private func persistRequest(_ kind: RequestKind, _ id: UUID) {
        let want = self[keyPath: kind.pending][id]
        var stored = existingStored()
        if let i = stored.firstIndex(where: { $0.id == id }) {
            guard let field = kind.stored else { return }
            stored[i][keyPath: field] = want
            persistFolders(bookmarks: stored)
            return
        }
        var remote = existingStoredRemote()
        if let i = remote.firstIndex(where: { $0.id == id }) {
            remote[i][keyPath: kind.storedRemote] = want
            persistRemoteFolders(bookmarks: remote)
        }
    }

    /// A removed folder's requests and their state go with it.
    private func forgetRequests(_ id: UUID) {
        for kind in Self.requestKinds {
            self[keyPath: kind.tasks].removeValue(forKey: id)?.cancel()
            self[keyPath: kind.pending][id] = nil
            self[keyPath: kind.notes][id] = nil
            self[keyPath: kind.errors][id] = nil
        }
    }

    /// Logged once per distinct error, not at every pass (as markUploadOnly).
    private func noteRequestError(_ kind: RequestKind, _ id: UUID, path: String, _ problem: String) {
        guard self[keyPath: kind.errors][id] != problem else { return }
        self[keyPath: kind.errors][id] = problem
        syncLog.error("could not \(kind.failure(path), privacy: .public) (tried again later): \(problem, privacy: .public)")
    }

    /// Asks the device which folders it keeps out of Images: at connect,
    /// after each acknowledged request, and once a minute.
    private func refreshOutOfImages() async {
        guard ws.isConnected() else { return }
        let domain = settings?.domain
        guard let answer = try? await ws.request({ req in
            req.payload = .reqListOutOfImages(Msg_ListOutOfImages())
        }), settings?.domain == domain else { return }
        if Self.isUnknownPayload(answer) {
            if outOfImagesSupported != false { outOfImagesSupported = false }
            outOfImagesFolders = nil
        } else if !answer.error, case .respOutOfImagesFolders(let list) = answer.payload {
            if outOfImagesSupported != true { outOfImagesSupported = true }
            // Published only when it changes: every assignment redraws.
            if outOfImagesFolders != list.paths { outOfImagesFolders = list.paths }
            // A refusal to show a folder inside one kept out is about that
            // folder above: once it is shown (from the web or a phone), the
            // refusal no longer holds.
            let stale = outOfImagesNotes.keys.filter { id in
                if case .keptOutBy = outOfImagesState(for: id) { return false }
                return true
            }
            for id in stale { outOfImagesNotes[id] = nil }
        }
    }

    /// The one-minute poll's: not of a device that said it can't (asked
    /// again at the next connect), nor with no folders to show it on.
    private func refreshOutOfImagesPeriodically() async {
        guard outOfImagesSupported != false, !(folders.isEmpty && remoteFolders.isEmpty) else { return }
        await refreshOutOfImages()
    }

    /// The device is older than the request: error_code "unknown_payload",
    /// or before that code existed, the bare message.
    private nonisolated static func isUnknownPayload(_ resp: Resp) -> Bool {
        resp.error && (resp.errorCode == "unknown_payload" || resp.errorMessage == "unknown payload")
    }

    /// What a delete on the device came to.
    private enum DeleteOutcome { case deleted, keptOnDevice }

    @discardableResult
    private func delete(_ remotePath: String) async throws -> DeleteOutcome {
        let resp = try await ws.request { req in
            var d = Msg_DelFile()
            d.path = remotePath
            req.payload = .reqDelFile(d)
        }
        if resp.error {
            // Issue #132: the owner made that folder upload only, so the
            // device keeps the file however the local copy goes. That is
            // the folder working as intended, not a failure to retry:
            // keptOnDevice. As otc-sync's errKeptOnDevice.
            if resp.errorCode == "upload_only" {
                syncLog.notice("delete of \(remotePath, privacy: .public) skipped: the folder is upload only - kept on the device")
                return .keptOnDevice
            }
            throw NSError(domain: "sync.delete", code: 1, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "delete rejected" : resp.errorMessage])
        }
        return .deleted
    }

    /// What enumerateFilesRecursively found.
    private struct LocalScan {
        var urls: [URL] = []
        /// What could not be read (a directory without permission): what
        /// is under it is not "gone", it is unknown.
        var failed: [URL] = []
        /// false when the folder itself could not be listed - an empty
        /// list then says nothing about what it holds.
        var rootReadable = true
        /// Every file of the folder is in `urls`.
        var complete: Bool { rootReadable && failed.isEmpty }
    }

    // `static`/`nonisolated` and self-contained (no access to `self`) on
    // purpose: walking a whole folder tree can take a while for a large
    // library, and calling this straight from @MainActor `reconcile()`
    // used to do that walk (and every file's hash below) right on the main
    // thread — freezing the popover UI, which is what made the folder list
    // look "stuck"/unopenable rather than just slow. Being a plain
    // self-free static function makes it safe to hop off-actor via
    // `Task.detached` at the call site.
    private nonisolated static func enumerateFilesRecursively(at root: URL) -> LocalScan {
        var scan = LocalScan()
        // The enumerator skipped what it couldn't read without a word, and
        // a two-way pass took those files for deleted here.
        final class Failures: @unchecked Sendable { var urls: [URL] = [] }
        let failures = Failures()
        if let e = FileManager.default.enumerator(at: root,
                                                  includingPropertiesForKeys: [.isRegularFileKey],
                                                  options: [.skipsHiddenFiles],
                                                  errorHandler: { url, _ in failures.urls.append(url); return true }) {
            for case let file as URL in e {
                // A download in progress (download() writes to a
                // ".otc-part" file first) is not a file of the folder: it
                // used to be found here and uploaded, half-written. As
                // otc-sync's scan.
                if file.lastPathComponent.hasSuffix(".otc-part") { continue }
                if (try? file.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true {
                    scan.urls.append(file)
                }
            }
        } else {
            scan.rootReadable = false
        }
        let rootPath = root.standardizedFileURL.path
        if failures.urls.contains(where: { $0.standardizedFileURL.path == rootPath }) { scan.rootReadable = false }
        scan.failed = failures.urls
        return scan
    }

    // You can refine this to use relative paths per folder root.
    // `root` is a remoteDeviceRoot() the caller already has: a pass takes
    // it once (not cached for good - renaming the Mac changes it, as
    // before).
    private func remotePathFor(_ path: String, root: String? = nil) -> String {
        (root ?? remoteDeviceRoot()) + path
    }

    /// "/mac/<this Mac's name>", where this Mac's folders go on the device.
    private func remoteDeviceRoot() -> String {
        let deviceName = Host.current().localizedName?
            .replacingOccurrences(of: "/", with: "-")
            .replacingOccurrences(of: ":", with: "-")
            .replacingOccurrences(of: " ", with: "_") ?? "Mac"
        return "/mac/\(deviceName)"
    }

    // See enumerateFilesRecursively's comment — same reasoning: this used to
    // run synchronously on the main actor inside reconcile()'s per-file
    // loop, so hashing e.g. a multi-GB video blocked the whole UI for as
    // long as that took. `nonisolated static` lets it run off-actor.
    // Read a chunk at a time, as otc-sync's sha256File: a memory-mapped
    // file truncated by another app mid-hash crashed the app (SIGBUS), and
    // on a volume Foundation won't map the whole file was read into RAM.
    private nonisolated static func sha256Hex(of url: URL) throws -> String {
        let fh = try FileHandle(forReadingFrom: url)
        defer { try? fh.close() }
        var hasher = SHA256()
        while true {
            // The pool frees each chunk's buffer as it goes: a detached
            // task has no run loop to drain it across a multi-GB file.
            let more: Bool = try autoreleasepool {
                guard let chunk = try fh.read(upToCount: Int(chunkSize)), !chunk.isEmpty else { return false }
                hasher.update(data: chunk)
                return true
            }
            if !more { break }
        }
        return hasher.finalize().map { String(format: "%02x", $0) }.joined()
    }

    // MARK: - Persistence helpers

    private func existingStored() -> [StoredFolder] {
        guard let data = UserDefaults.standard.data(forKey: bookmarksKey) else { return [] }
        return (try? JSONDecoder().decode([StoredFolder].self, from: data)) ?? []
    }

    private func persistFolders(bookmarks: [StoredFolder]) {
        do {
            let data = try JSONEncoder().encode(bookmarks)
            UserDefaults.standard.set(data, forKey: bookmarksKey)
        } catch {
            print("Persist error:", error)
        }
    }

    private func restoreFolders() {
        let stored = existingStored()
        var restored: [TrackedFolder] = []

        for item in stored {
            var stale = false
            do {
                let url = try URL(
                    resolvingBookmarkData: item.bookmark,
                    options: [.withSecurityScope],
                    relativeTo: nil,
                    bookmarkDataIsStale: &stale
                )
                _ = url.startAccessingSecurityScopedResource()

                // refresh stale bookmarks
                if stale {
                    let fresh = try url.bookmarkData(options: [.withSecurityScope],
                                                     includingResourceValuesForKeys: nil,
                                                     relativeTo: nil)
                    var updated = stored
                    if let idx = updated.firstIndex(where: { $0.id == item.id }) {
                        updated[idx] = StoredFolder(id: item.id, bookmark: fresh, outOfImages: item.outOfImages)
                        persistFolders(bookmarks: updated)
                    }
                }

                restored.append(TrackedFolder(id: item.id, url: url))
                if let want = item.outOfImages { outOfImagesPending[item.id] = want }
            } catch {
                print("Failed to resolve bookmark:", error)
            }
        }

        self.folders = restored
    }

    private func existingStoredRemote() -> [StoredRemoteFolder] {
        guard let data = UserDefaults.standard.data(forKey: remoteFoldersKey) else { return [] }
        return (try? JSONDecoder().decode([StoredRemoteFolder].self, from: data)) ?? []
    }

    private func persistRemoteFolders(bookmarks: [StoredRemoteFolder]) {
        do {
            let data = try JSONEncoder().encode(bookmarks)
            UserDefaults.standard.set(data, forKey: remoteFoldersKey)
        } catch {
            print("Persist error:", error)
        }
    }

    private func restoreRemoteFolders() {
        let stored = existingStoredRemote()
        var restored: [RemoteFolder] = []

        for item in stored {
            var stale = false
            do {
                let url = try URL(
                    resolvingBookmarkData: item.bookmark,
                    options: [.withSecurityScope],
                    relativeTo: nil,
                    bookmarkDataIsStale: &stale
                )
                _ = url.startAccessingSecurityScopedResource()

                if stale {
                    let fresh = try url.bookmarkData(options: [.withSecurityScope],
                                                     includingResourceValuesForKeys: nil,
                                                     relativeTo: nil)
                    var updated = stored
                    if let idx = updated.firstIndex(where: { $0.id == item.id }) {
                        updated[idx] = StoredRemoteFolder(id: item.id, remotePath: item.remotePath, bookmark: fresh,
                                                          outOfImages: item.outOfImages, uploadOnly: item.uploadOnly)
                        persistRemoteFolders(bookmarks: updated)
                    }
                }

                restored.append(RemoteFolder(id: item.id, remotePath: item.remotePath, localURL: url))
                if let want = item.outOfImages { outOfImagesPending[item.id] = want }
                if let want = item.uploadOnly { uploadOnlyPending[item.id] = want }
            } catch {
                print("Failed to resolve remote folder bookmark:", error)
            }
        }

        self.remoteFolders = restored
    }
}


/// How the device's storage is doing, reduced to what the menu bar icon
/// can say (issue #69): two drives, both fine; one gone; or the array
/// itself gone.
enum RaidHealth: Equatable {
    /// Every device in the array is active (or it is resyncing, which is
    /// the array healing - not a fault).
    case ok
    /// The array is up but missing devices - one drive down on a RAID1.
    case degraded
    /// No usable array: it failed, or the device reports errors and no
    /// active devices at all.
    case failed
    /// Not connected, or no answer yet.
    case unknown

    init(status: Msg_Status) {
        switch status.raidState {
        case .raidInSync, .raidSyncing:
            self = .ok
        case .raidDegraded:
            // Degraded with nothing active is a dead array, not a limp one.
            self = status.raidDevicesActive > 0 ? .degraded : .failed
        case .raidNone, .raidUnknown, .UNRECOGNIZED:
            // A device with no RAID at all is "fine" as far as this icon
            // is concerned - there is no array to be broken. Only an
            // explicit error from the device turns that red.
            self = status.errors.isEmpty ? .ok : .failed
        }
    }

    /// The icon: the rack symbol, with each drive line coloured by state.
    /// Green/green, red/orange (one down, the other carrying everything),
    /// red/red.
    var driveColors: (top: Color, bottom: Color) {
        switch self {
        case .ok:       return (.green, .green)
        case .degraded: return (.red, .orange)
        case .failed:   return (.red, .red)
        case .unknown:  return (.secondary, .secondary)
        }
    }

    var summary: String {
        switch self {
        case .ok:       return "Storage healthy"
        case .degraded: return "A drive is down - the RAID is degraded"
        case .failed:   return "The RAID has failed"
        case .unknown:  return "Storage status unknown"
        }
    }
}

/// Issue #168: where a chunked download lands - the ".otc-part" file,
/// written a chunk at a time while the SHA-256 is fed along, so the
/// content is never held whole in memory. Used from one task at a time.
private final class ChunkSink: @unchecked Sendable {
    private let handle: FileHandle
    private var hasher = SHA256()

    init(url: URL) throws {
        FileManager.default.createFile(atPath: url.path, contents: nil)
        handle = try FileHandle(forWritingTo: url)
        try handle.truncate(atOffset: 0)
    }

    func write(_ data: Data) throws {
        try handle.write(contentsOf: data)
        hasher.update(data: data)
    }

    /// Flushes and closes the file, returning the content's hex SHA-256.
    func finish() -> String {
        close()
        return hasher.finalize().map { String(format: "%02x", $0) }.joined()
    }

    func close() {
        try? handle.close()
    }
}

/// Runs a folder chooser from this menu-bar app. The app isn't the active
/// one while its popover is open, so a panel run as-is came up without
/// focus: visible, but with its sidebar greyed out and not answering clicks
/// until something activated the app. Activating first gives it the focus
/// it needs.
@MainActor
func runFolderPanel(_ panel: NSOpenPanel) -> NSApplication.ModalResponse {
    NSApp.activate(ignoringOtherApps: true)
    panel.makeKeyAndOrderFront(nil)
    return panel.runModal()
}
