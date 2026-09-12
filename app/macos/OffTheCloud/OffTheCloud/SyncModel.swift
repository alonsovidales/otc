import Foundation
import SwiftUI
import Combine
import CryptoKit
import SwiftProtobuf
import AppKit   // <- for NSOpenPanel
import CoreServices // <- for FSEventStreamEventFlags constants

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
    }
    private let bookmarksKey = "sync.folders.bookmarks"

    private struct StoredRemoteFolder: Codable {
        let id: UUID
        let remotePath: String
        let bookmark: Data
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
    // A folder that fails to reconcile (a dropped connection mid-request,
    // a transient server error, etc.) does get retried by the periodic
    // safety-net loop above — but waiting up to 10 minutes for that, with
    // no sign a retry is even coming, is what made an error look
    // permanent/stuck. This is a much shorter, dedicated retry just for
    // folders currently in .error.
    private static let errorRetryInterval: Duration = .seconds(30)
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

    private let ws = WSClient()
    private var settings: SettingsStore?
    private var cancellables: Set<AnyCancellable> = []

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
    private var debounceTasks: [String: Task<Void, Never>] = [:]
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

    init() {
        restoreFolders()
        restoreRemoteFolders()

        ws.onConnect = { [weak self] in
            Task { @MainActor in self?.overallStatus = "Connected" }
        }
        ws.onDisconnect = { [weak self] _ in
            Task { @MainActor in self?.overallStatus = "Disconnected" }
        }
    }

    // MARK: Bind settings / auto-sync

    func bind(settings: SettingsStore) {
        guard self.settings == nil else { return }
        self.settings = settings

        settings.$domain
            .combineLatest(settings.$password)
            .sink { [weak self] domain, key in
                guard let self else { return }
                Task { @MainActor in
                    if settings.ready {
                        self.ws.configure(domain: domain, key: key)
                        self.ws.connect()
                        self.startSync()
                    } else {
                        self.ws.disconnect()
                        self.overallStatus = "Missing domain/password"
                    }
                }
            }
            .store(in: &cancellables)
    }

    // MARK: - UI actions

    func addFolder() {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = false
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        if panel.runModal() == .OK, let url = panel.url {
            do {
                // create a security-scoped bookmark
                let bookmark = try url.bookmarkData(
                    options: [.withSecurityScope],
                    includingResourceValuesForKeys: nil,
                    relativeTo: nil
                )
                _ = url.startAccessingSecurityScopedResource() // keep access for this session

                let tf = TrackedFolder(id: UUID(), url: url)
                folders.append(tf)

                var stored = existingStored()
                stored.append(StoredFolder(id: tf.id, bookmark: bookmark))
                persistFolders(bookmarks: stored)

                if settings?.ready == true {
                    Task { await self.setupFolder(tf) }
                }
            } catch {
                print("Bookmark creation failed:", error)
            }
        }
    }

    func removeFolder(_ f: TrackedFolder) {
        folderWatchers[f.id]?.stop()
        folderWatchers.removeValue(forKey: f.id)
        remoteHashesByFolder.removeValue(forKey: f.id)
        errorRetryTasks[f.id]?.cancel()
        errorRetryTasks.removeValue(forKey: f.id)

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
    /// same picker addFolder() uses) for it.
    func addRemoteFolder(remotePath: String, localURL: URL) {
        do {
            let bookmark = try localURL.bookmarkData(
                options: [.withSecurityScope],
                includingResourceValuesForKeys: nil,
                relativeTo: nil
            )
            _ = localURL.startAccessingSecurityScopedResource()

            let rf = RemoteFolder(id: UUID(), remotePath: remotePath, localURL: localURL)
            remoteFolders.append(rf)

            var stored = existingStoredRemote()
            stored.append(StoredRemoteFolder(id: rf.id, remotePath: remotePath, bookmark: bookmark))
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
        lastSyncedByRemoteFolder.removeValue(forKey: f.id)
        remoteErrorRetryTasks[f.id]?.cancel()
        remoteErrorRetryTasks.removeValue(forKey: f.id)

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
        guard folderWatchers[folder.id] == nil else { return }
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
        guard folderWatchers[folder.id] == nil else { return }
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
        for event in events {
            // We only care about actual file content, not directories
            // being created/renamed/removed — those surface indirectly
            // through their children's own events anyway.
            let isDir = event.flags & FSEventStreamEventFlags(kFSEventStreamEventFlagItemIsDir) != 0
            if isDir { continue }

            let path = event.path
            debounceTasks[path]?.cancel()
            debounceTasks[path] = Task { [weak self] in
                try? await Task.sleep(for: Self.debounceInterval)
                guard !Task.isCancelled, let self else { return }
                await self.processChangedPath(path, folderId: folderId)
                self.debounceTasks[path] = nil
            }
        }
    }

    private func processChangedPath(_ path: String, folderId: UUID) async {
        // Not connected right now — the reconcile safety net (or the next
        // FSEvents batch, if this exact path changes again) will catch it
        // up once we're back online.
        guard ws.isConnected() else { return }

        let remotePath = remotePathFor(path)
        let fileURL = URL(fileURLWithPath: path)
        var isDir: ObjCBool = false
        let exists = FileManager.default.fileExists(atPath: path, isDirectory: &isDir)

        if exists, !isDir.boolValue {
            let localHash = try? await Task.detached(priority: .utility) {
                try Self.sha256Hex(of: fileURL)
            }.value
            guard let localHash else { return }
            guard remoteHashesByFolder[folderId]?[remotePath] != localHash else { return }
            do {
                try await upload(fileURL, to: remotePath, knownHash: localHash)
                remoteHashesByFolder[folderId, default: [:]][remotePath] = localHash
            } catch {
                print("Error uploading \(path): \(error)")
            }
        } else if remoteHashesByFolder[folderId]?[remotePath] != nil {
            // It was synced before and is gone now — a real deletion, not
            // just a path we never uploaded in the first place.
            do {
                try await delete(remotePath)
                remoteHashesByFolder[folderId]?.removeValue(forKey: remotePath)
            } catch {
                print("Error deleting \(path): \(error)")
            }
        }
    }

    // MARK: - Reconcile (baseline + periodic safety net)

    private func reconcile(_ folder: TrackedFolder) async {
        guard ws.isConnected() else { return }

        // Reconciling now anyway (whatever triggered this call), so any
        // still-pending short retry from a previous failure would just be
        // a redundant duplicate once this one lands.
        errorRetryTasks[folder.id]?.cancel()
        errorRetryTasks[folder.id] = nil

        let root = folder.url
        let remotePrefix = remotePathFor(root.path) + "/"

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
                return
            }
            var remoteMap: [String: String] = [:]
            if case .respListOfFiles(let lof) = resp.payload {
                remoteMap = Dictionary(uniqueKeysWithValues: lof.files.map { ($0.path, $0.hash) })
            }

            let localFiles = await Task.detached(priority: .utility) {
                Self.enumerateFilesRecursively(at: root)
            }.value
            let localRemotePaths = Set(localFiles.map { remotePathFor($0.path) })

            // Pass 1: figure out what actually needs uploading. This is
            // pure verification — on a folder that's already in sync (the
            // common case for the periodic safety-net reconcile, or just
            // reopening the app) every file matches and nothing here is
            // visible to the user at all, which is the point: hashing to
            // *confirm* nothing changed shouldn't look like a transfer is
            // underway. The progress bar below is reserved for real
            // mismatches only.
            var toUpload: [(url: URL, remotePath: String, hash: String, size: Int64)] = []
            for fileURL in localFiles {
                let remotePath = remotePathFor(fileURL.path)
                let localHash = try? await Task.detached(priority: .utility) {
                    try Self.sha256Hex(of: fileURL)
                }.value
                guard let localHash, remoteMap[remotePath] != localHash else { continue }
                let size = (try? fileURL.resourceValues(forKeys: [.fileSizeKey]).fileSize).flatMap { Int64($0) } ?? 0
                toUpload.append((fileURL, remotePath, localHash, size))
            }

            // Pass 2: only the mismatches, so the percentage reflects how
            // much of *this* work is left rather than the whole folder —
            // otherwise one changed file in a folder of a thousand would
            // sit at 99.9% the instant it starts, which is just as
            // misleading as showing no progress at all.
            if !toUpload.isEmpty {
                // Weighted by bytes, not file count: a folder with one
                // 400MB video and 30 small photos would otherwise sit at
                // "0%" for the video's entire multi-minute transfer (1/31
                // files done), which reads as stuck even though it's
                // actively working — issue #37's original complaint.
                let totalBytes = max(toUpload.reduce(0) { $0 + $1.size }, 1)
                var bytesDone: Int64 = 0

                for item in toUpload {
                    // Reported *before* the upload starts, not after — a
                    // multi-gigabyte file can take minutes to send, and
                    // without this the UI just sits on the previous
                    // file's number the whole time, which is exactly what
                    // looked like "stuck" before (issue #37).
                    updateState(folder.id, .scanning(progress: Double(bytesDone) / Double(totalBytes), currentFile: item.url.lastPathComponent))

                    do {
                        try await upload(item.url, to: item.remotePath, knownHash: item.hash)
                        remoteMap[item.remotePath] = item.hash
                    } catch {
                        // Logged and skipped, not fatal to the whole
                        // folder — the next reconcile pass (or another
                        // FSEvents change to this same path) will retry it.
                        print("Error syncing \(item.url.lastPathComponent): \(error)")
                    }
                    bytesDone += item.size
                }
            }

            // Anything the device still has under this folder's prefix
            // that no longer exists locally gets removed to match —
            // mirrors OneDrive's "delete propagates" behavior (issue #37).
            let staleRemotePaths = remoteMap.keys.filter { !localRemotePaths.contains($0) }
            for remotePath in staleRemotePaths {
                do {
                    try await delete(remotePath)
                    remoteMap.removeValue(forKey: remotePath)
                } catch {
                    print("Error deleting stale \(remotePath): \(error)")
                }
            }

            remoteHashesByFolder[folder.id] = remoteMap
            updateState(folder.id, .watching)
        } catch {
            updateState(folder.id, .error(error.localizedDescription))
            scheduleErrorRetry(for: folder)
        }
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
        if let idx = folders.firstIndex(where: { $0.id == id }) {
            folders[idx].state = state
        }
    }

    private func updateRemoteState(_ id: UUID, _ state: FolderState) {
        if let idx = remoteFolders.firstIndex(where: { $0.id == id }) {
            remoteFolders[idx].state = state
        }
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

        remoteErrorRetryTasks[folder.id]?.cancel()
        remoteErrorRetryTasks[folder.id] = nil

        var isDir: ObjCBool = false
        guard FileManager.default.fileExists(atPath: folder.localURL.path, isDirectory: &isDir), isDir.boolValue else {
            print("Remote folder's local root is gone (\(folder.localURL.path)) — stopping sync, remote left untouched.")
            removeRemoteFolder(folder)
            return
        }

        // Guards against a real prefix-collision risk in the recursive
        // listing below: an unanchored "/subdir" would also match a
        // sibling like "/subdir2/file.txt" (it's used as a regex prefix
        // server-side — see dao.GetFilesByPath's recursive branch), so this
        // needs the trailing "/" to only ever match this folder's own
        // descendants. Same reasoning as remotePrefix in the upload-side
        // reconcile() above.
        let remotePrefix = folder.remotePath.hasSuffix("/") ? folder.remotePath : folder.remotePath + "/"

        do {
            let resp = try await ws.request { req in
                var lf = ListFiles()
                lf.path = remotePrefix
                lf.recursive = true
                req.payload = .reqListFiles(lf)
            }
            if resp.error {
                updateRemoteState(folder.id, .error(resp.errorMessage.isEmpty ? "Could not list remote files" : resp.errorMessage))
                scheduleRemoteErrorRetry(for: folder)
                return
            }
            var remoteByRelative: [String: Msg_File] = [:]
            if case .respListOfFiles(let lof) = resp.payload {
                for file in lof.files {
                    let relative = file.path.hasPrefix(remotePrefix) ? String(file.path.dropFirst(remotePrefix.count)) : file.path
                    remoteByRelative[relative] = file
                }
            }

            let localFiles = await Task.detached(priority: .utility) {
                Self.enumerateFilesRecursively(at: folder.localURL)
            }.value
            let localRoot = folder.localURL.standardizedFileURL.path
            var localByRelative: [String: URL] = [:]
            for url in localFiles {
                let full = url.standardizedFileURL.path
                guard full.hasPrefix(localRoot) else { continue }
                var relative = String(full.dropFirst(localRoot.count))
                if relative.hasPrefix("/") { relative.removeFirst() }
                localByRelative[relative] = url
            }

            // Hashing is the slow part, so only do it for what's actually
            // on disk right now — remote's hash comes for free from the
            // listing above.
            var localHashes: [String: String] = [:]
            for (relative, url) in localByRelative {
                if let h = try? await Task.detached(priority: .utility) { try Self.sha256Hex(of: url) }.value {
                    localHashes[relative] = h
                }
            }

            let lastSynced = lastSyncedByRemoteFolder[folder.id] ?? [:]
            let allRelativePaths = Set(remoteByRelative.keys).union(localByRelative.keys).union(lastSynced.keys)

            enum ActionKind { case upload, download, deleteLocal, deleteRemote }
            // hash carries the already-computed local hash through to the
            // .upload case below, purely to avoid hashing the same file
            // twice (issue #58's HasFile check needs it anyway) — unused
            // for the other three kinds.
            var actions: [(relative: String, kind: ActionKind, size: Int, hash: String?)] = []
            var newSynced = lastSynced

            for relative in allRelativePaths {
                let localHash = localHashes[relative]
                let remoteFile = remoteByRelative[relative]
                let remoteHash = remoteFile?.hash
                let last = lastSynced[relative]

                if localHash == remoteHash {
                    // Already in agreement — includes both being nil,
                    // which can't really land in allRelativePaths, but
                    // harmless either way.
                    if let localHash { newSynced[relative] = localHash } else { newSynced.removeValue(forKey: relative) }
                    continue
                }

                let localChanged = localHash != last
                let remoteChanged = remoteHash != last
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

                if remoteWins {
                    if let remoteHash {
                        actions.append((relative, .download, Int(remoteFile?.size ?? 0), nil))
                        newSynced[relative] = remoteHash
                    } else {
                        actions.append((relative, .deleteLocal, 0, nil))
                        newSynced.removeValue(forKey: relative)
                    }
                } else {
                    if let localHash {
                        actions.append((relative, .upload, 0, localHash))
                        newSynced[relative] = localHash
                    } else {
                        actions.append((relative, .deleteRemote, 0, nil))
                        newSynced.removeValue(forKey: relative)
                    }
                }
            }

            if !actions.isEmpty {
                let total = actions.count
                for (i, action) in actions.enumerated() {
                    updateRemoteState(folder.id, .scanning(progress: Double(i) / Double(total), currentFile: (action.relative as NSString).lastPathComponent))
                    let localURL = folder.localURL.appendingPathComponent(action.relative)
                    let remotePath = remotePrefix + action.relative
                    do {
                        switch action.kind {
                        case .upload: try await upload(localURL, to: remotePath, knownHash: action.hash)
                        case .download: try await download(remotePath, to: localURL)
                        case .deleteRemote: try await delete(remotePath)
                        case .deleteLocal: try FileManager.default.removeItem(at: localURL)
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

            lastSyncedByRemoteFolder[folder.id] = newSynced
            updateRemoteState(folder.id, .watching)
        } catch {
            updateRemoteState(folder.id, .error(error.localizedDescription))
            scheduleRemoteErrorRetry(for: folder)
        }
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

    private func download(_ remotePath: String, to dest: URL) async throws {
        let resp = try await ws.request { req in
            var gf = Msg_GetFile()
            gf.path = remotePath
            req.payload = .reqGetFile(gf)
        }
        if resp.error {
            throw NSError(domain: "sync.download", code: 1, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "download rejected" : resp.errorMessage])
        }
        guard case .respFile(let file) = resp.payload else {
            throw NSError(domain: "sync.download", code: 2, userInfo: [NSLocalizedDescriptionKey: "unexpected response"])
        }
        try await Task.detached(priority: .utility) {
            try FileManager.default.createDirectory(at: dest.deletingLastPathComponent(), withIntermediateDirectories: true)
            try file.content.write(to: dest, options: .atomic)
        }.value
    }

    // MARK: - Wire helpers

    // Issue #58: storage is deduplicated by hash server-side already (see
    // the Go files_manager.UploadFile/LinkFile doc comments) — this is
    // what actually skips re-sending the bytes for a file the device
    // already has under some other path, rather than relying on that
    // dedup only kicking in *after* the transfer. `knownHash` lets a
    // caller that already hashed this file for its own diffing (every
    // call site here has) skip hashing it a second time.
    private func upload(_ url: URL, to remotePath: String, knownHash: String? = nil) async throws {
        let hash: String
        if let knownHash {
            hash = knownHash
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
        let created = SwiftProtobuf.Google_Protobuf_Timestamp(
            date: (try? url.resourceValues(forKeys: [.creationDateKey]).creationDate) ?? Date()
        )

        if case .respFileExists(let fe) = hasResp.payload, fe.exists {
            let resp = try await ws.request { req in
                var lf = Msg_LinkFile()
                lf.hash = hash
                lf.path = remotePath
                lf.forceOverride = true
                lf.created = created
                req.payload = .reqLinkFile(lf)
            }
            if resp.error {
                throw NSError(domain: "sync.upload", code: 1, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "link rejected" : resp.errorMessage])
            }
            return
        }

        // Reading a multi-GB file synchronously used to happen right here,
        // on the main actor — same UI-freezing problem as the hashing in
        // reconcile(), just for the read instead of the digest.
        let data = try await Task.detached(priority: .utility) {
            try Data(contentsOf: url)
        }.value
        let resp = try await ws.request { req in
            var up = UploadFile()
            up.path = remotePath
            up.content = data
            up.forceOverride = true
            up.created = created
            req.payload = .reqUploadFile(up)
        }
        // A server-side rejection (auth failure, disk full, etc.) still
        // comes back as a normal response, not a thrown error from
        // `request` — checking resp.error is the only way to actually
        // notice the file wasn't saved, instead of silently caching it as
        // synced and never trying again.
        if resp.error {
            throw NSError(domain: "sync.upload", code: 1, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "upload rejected" : resp.errorMessage])
        }
    }

    private func delete(_ remotePath: String) async throws {
        let resp = try await ws.request { req in
            var d = Msg_DelFile()
            d.path = remotePath
            req.payload = .reqDelFile(d)
        }
        if resp.error {
            throw NSError(domain: "sync.delete", code: 1, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "delete rejected" : resp.errorMessage])
        }
    }

    // `static`/`nonisolated` and self-contained (no access to `self`) on
    // purpose: walking a whole folder tree can take a while for a large
    // library, and calling this straight from @MainActor `reconcile()`
    // used to do that walk (and every file's hash below) right on the main
    // thread — freezing the popover UI, which is what made the folder list
    // look "stuck"/unopenable rather than just slow. Being a plain
    // self-free static function makes it safe to hop off-actor via
    // `Task.detached` at the call site.
    private nonisolated static func enumerateFilesRecursively(at root: URL) -> [URL] {
        var urls: [URL] = []
        if let e = FileManager.default.enumerator(at: root,
                                                  includingPropertiesForKeys: [.isRegularFileKey],
                                                  options: [.skipsHiddenFiles]) {
            for case let file as URL in e {
                if (try? file.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true {
                    urls.append(file)
                }
            }
        }
        return urls
    }

    // You can refine this to use relative paths per folder root.
    private func remotePathFor(_ path: String) -> String {
        let deviceName = Host.current().localizedName?
            .replacingOccurrences(of: "/", with: "-")
            .replacingOccurrences(of: ":", with: "-")
            .replacingOccurrences(of: " ", with: "_") ?? "Mac"
        return "/mac/\(deviceName)\(path)"
    }

    // See enumerateFilesRecursively's comment — same reasoning: this used to
    // run synchronously on the main actor inside reconcile()'s per-file
    // loop, so hashing e.g. a multi-GB video blocked the whole UI for as
    // long as that took. `nonisolated static` lets it run off-actor.
    private nonisolated static func sha256Hex(of url: URL) throws -> String {
        let data = try Data(contentsOf: url, options: .mappedIfSafe)
        let digest = SHA256.hash(data: data)
        return digest.map { String(format: "%02x", $0) }.joined()
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
                        updated[idx] = StoredFolder(id: item.id, bookmark: fresh)
                        persistFolders(bookmarks: updated)
                    }
                }

                restored.append(TrackedFolder(id: item.id, url: url))
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
                        updated[idx] = StoredRemoteFolder(id: item.id, remotePath: item.remotePath, bookmark: fresh)
                        persistRemoteFolders(bookmarks: updated)
                    }
                }

                restored.append(RemoteFolder(id: item.id, remotePath: item.remotePath, localURL: url))
            } catch {
                print("Failed to resolve remote folder bookmark:", error)
            }
        }

        self.remoteFolders = restored
    }
}
