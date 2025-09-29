import Foundation
import SwiftUI
import Combine
import CryptoKit
import SwiftProtobuf
import AppKit   // <- for NSOpenPanel

@MainActor
final class SyncModel: ObservableObject {

    struct TrackedFolder: Identifiable, Hashable, Codable {
        let id: UUID
        var url: URL
        var progress: Double

        init(id: UUID = UUID(), url: URL, progress: Double = 0) {
            self.id = id
            self.url = url
            self.progress = progress
        }
    }

    // ---- PERSISTENCE TYPES/KEYS (NEW) ----
    private struct StoredFolder: Codable {
        let id: UUID
        let bookmark: Data
    }
    private let bookmarksKey = "sync.folders.bookmarks"

    @Published var folders: [TrackedFolder] = []
    @Published var overallStatus: String = "Not connected"

    private let ws = WSClient()
    private var settings: SettingsStore?
    private var cancellables: Set<AnyCancellable> = []

    init() {
        // restore persisted folders on launch (NEW)
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
                        self.startSyncingLoop()
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
                // create a security-scoped bookmark (NEW)
                let bookmark = try url.bookmarkData(
                    options: [.withSecurityScope],
                    includingResourceValuesForKeys: nil,
                    relativeTo: nil
                )
                _ = url.startAccessingSecurityScopedResource() // keep access for this session

                // add to memory
                let tf = TrackedFolder(url: url, progress: 0)
                folders.append(tf)

                // persist (NEW)
                var stored = existingStored()
                stored.append(StoredFolder(id: tf.id, bookmark: bookmark))
                persistFolders(bookmarks: stored)

            } catch {
                print("Bookmark creation failed:", error)
            }
        }
    }

    func removeFolder(_ f: TrackedFolder) {
        // stop access (NEW)
        f.url.stopAccessingSecurityScopedResource()

        // remove from memory
        folders.removeAll { $0.id == f.id }

        // remove from storage (NEW)
        var stored = existingStored()
        stored.removeAll { $0.id == f.id }
        persistFolders(bookmarks: stored)
    }

    // MARK: - Sync loop

    @MainActor
    private func startSyncingLoop() {
        guard let settings, settings.ready else { return }

        Task.detached { [weak self] in
            while let self = self {
                let shouldRun = await MainActor.run { self.settings?.ready ?? false }
                if !shouldRun { break }

                do { try await self.syncAll() }
                try? await Task.sleep(for: .seconds(60))
            }
        }
    }

    private func updateProgress(for url: URL, value: Double) {
        if let idx = folders.firstIndex(where: { $0.url == url }) {
            folders[idx].progress = value
        }
    }

    private func syncAll() async {
        for folder in folders {
            await syncFolder(folder.url)
        }
    }

    private func syncFolder(_ root: URL) async {
        print("Sync folder: \(root.path)")
        guard ws.isConnected() else { return }
        let allLocal = enumerateFilesRecursively(at: root)

        var completed = 0
        let total = max(allLocal.count, 1)

        do {
            // list remote for this base path
            let resp = try await ws.request { req in
                var lf = ListFiles(); lf.path = remotePathFor(root.path) + "/"
                req.payload = .reqListFiles(lf)
            }
            let remoteMap: [String: String]
            if case .respListOfFiles(let lof) = resp.payload {
                remoteMap = Dictionary(uniqueKeysWithValues: lof.files.map { ($0.path, $0.hash) })
            } else {
                remoteMap = [:]
            }

            for fileURL in allLocal {
                do {
                    let remotePath = remotePathFor(fileURL.path)
                    let remoteHash = remoteMap[remotePath]
                    let localHash = try sha256Hex(of: fileURL)

                    if remoteHash != localHash {
                        try await upload(fileURL, to: remotePath)
                    }
                } catch {
                    print("Error syncing \(fileURL.lastPathComponent): \(error)")
                }
                completed += 1
                await MainActor.run {
                    self.updateProgress(for: root, value: Double(completed) / Double(total))
                }
            }
        } catch {
            print("Error listing:", error)
            return
        }
    }

    private func upload(_ url: URL, to remotePath: String) async throws {
        let data = try Data(contentsOf: url)
        let created = SwiftProtobuf.Google_Protobuf_Timestamp(
            date: (try? url.resourceValues(forKeys: [.creationDateKey]).creationDate) ?? Date()
        )
        _ = try await ws.request { req in
            var up = UploadFile()
            up.path = remotePath
            up.content = data
            up.forceOverride = true
            up.created = created
            req.payload = .reqUploadFile(up)
        }
    }

    private func enumerateFilesRecursively(at root: URL) -> [URL] {
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

    private func sha256Hex(of url: URL) throws -> String {
        let data = try Data(contentsOf: url, options: .mappedIfSafe)
        let digest = SHA256.hash(data: data)
        return digest.map { String(format: "%02x", $0) }.joined()
    }

    // MARK: - Persistence helpers (NEW)

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

                restored.append(TrackedFolder(id: item.id, url: url, progress: 0))
            } catch {
                print("Failed to resolve bookmark:", error)
            }
        }

        self.folders = restored
    }
}
