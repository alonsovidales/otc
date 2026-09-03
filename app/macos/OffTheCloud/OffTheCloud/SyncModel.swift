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

    // ---- PERSISTENCE TYPES/KEYS ----
    private struct StoredFolder: Codable {
        let id: UUID
        let bookmark: Data
    }
    private let bookmarksKey = "sync.folders.bookmarks"

    // Issue #37: FSEvents does the real-time work; this reconcile interval
    // is only a safety net for whatever it might have missed (the app
    // wasn't running, an event got dropped, etc.) — not the primary sync
    // mechanism the old 60-second full-rescan loop was.
    private static let reconcileInterval: Duration = .seconds(600)
    // Rapid-fire FSEvents for the same path (e.g. an app doing several
    // writes while saving) are coalesced by waiting this long after the
    // last event before actually reading/uploading the file.
    private static let debounceInterval: Duration = .seconds(1)

    @Published var folders: [TrackedFolder] = []
    @Published var overallStatus: String = "Not connected"

    private let ws = WSClient()
    private var settings: SettingsStore?
    private var cancellables: Set<AnyCancellable> = []

    private var folderWatchers: [UUID: FolderWatcher] = [:]
    // Last-known-synced hash per folder, keyed by the file's *remote* path
    // — the local cache of "what the device already has", refreshed by
    // reconcile() and kept current as changes are pushed incrementally.
    private var remoteHashesByFolder: [UUID: [String: String]] = [:]
    private var debounceTasks: [String: Task<Void, Never>] = [:]
    private var reconcileLoopStarted = false

    init() {
        restoreFolders()

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

        f.url.stopAccessingSecurityScopedResource()
        folders.removeAll { $0.id == f.id }

        var stored = existingStored()
        stored.removeAll { $0.id == f.id }
        persistFolders(bookmarks: stored)
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
                try await upload(fileURL, to: remotePath)
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
        updateState(folder.id, .scanning(progress: 0))

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

            // Weighted by bytes, not file count: a folder with one 400MB
            // video and 30 small photos would otherwise sit at "0%" for
            // the video's entire multi-minute transfer (1/31 files done),
            // which reads as stuck even though it's actively working —
            // issue #37's original complaint, and exactly what prompted
            // this fix. Bytes give a number that actually reflects how
            // much of the transfer is really left.
            let fileSizes = localFiles.map { url -> Int64 in
                (try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize).flatMap { Int64($0) } ?? 0
            }
            let totalBytes = max(fileSizes.reduce(0, +), 1)
            var bytesDone: Int64 = 0

            for (index, fileURL) in localFiles.enumerated() {
                // Reported *before* the upload starts, not after — a
                // multi-gigabyte file can take minutes to send, and
                // without this the UI just sits on the previous file's
                // number the whole time, which is exactly what looked
                // like "stuck" before (issue #37).
                updateState(folder.id, .scanning(progress: Double(bytesDone) / Double(totalBytes), currentFile: fileURL.lastPathComponent))

                let remotePath = remotePathFor(fileURL.path)
                let localHash = try? await Task.detached(priority: .utility) {
                    try Self.sha256Hex(of: fileURL)
                }.value
                if let localHash, remoteMap[remotePath] != localHash {
                    do {
                        try await upload(fileURL, to: remotePath)
                        remoteMap[remotePath] = localHash
                    } catch {
                        // Logged and skipped, not fatal to the whole
                        // folder — the next reconcile pass (or another
                        // FSEvents change to this same path) will retry it.
                        print("Error syncing \(fileURL.lastPathComponent): \(error)")
                    }
                }
                bytesDone += fileSizes[index]
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
        }
    }

    private func updateState(_ id: UUID, _ state: FolderState) {
        if let idx = folders.firstIndex(where: { $0.id == id }) {
            folders[idx].state = state
        }
    }

    // MARK: - Wire helpers

    private func upload(_ url: URL, to remotePath: String) async throws {
        // Reading a multi-GB file synchronously used to happen right here,
        // on the main actor — same UI-freezing problem as the hashing in
        // reconcile(), just for the read instead of the digest.
        let data = try await Task.detached(priority: .utility) {
            try Data(contentsOf: url)
        }.value
        let created = SwiftProtobuf.Google_Protobuf_Timestamp(
            date: (try? url.resourceValues(forKeys: [.creationDateKey]).creationDate) ?? Date()
        )
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
}
