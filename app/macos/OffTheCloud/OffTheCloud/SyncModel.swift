import Foundation
import SwiftUI
import CryptoKit
import Combine

/// One model for the whole app. Main-actor to make @Published safe.
@MainActor
final class SyncModel: ObservableObject {

    struct TrackedFolder: Identifiable, Hashable {
        let id = UUID()
        var url: URL
        var progress: Double = 0.0 // 0..1
    }

    @Published var folders: [TrackedFolder] = []
    @Published var overallStatus: String = "Not connected"

    private let ws = WSClient()
    private var settings: SettingsStore?

    init() {
        ws.onConnect = { [weak self] in
            Task { @MainActor in self?.overallStatus = "Connected" }
        }
        ws.onDisconnect = { [weak self] _ in
            Task { @MainActor in self?.overallStatus = "Disconnected" }
        }
    }

    /// Bind once at launch; reconfigures + starts syncing whenever settings change.
    func bind(settings: SettingsStore) {
        guard self.settings == nil else { return }
        self.settings = settings

        // React to settings changes
        Task {
            for await _ in settings.$domain.values {} // keep the Task alive
        }

        // Every time the settings mutate, (re)configure and (re)connect
        settings.$domain.combineLatest(settings.$password).sink { [weak self] domain, key in
            guard let self else { return }
            Task { @MainActor in
                if settings.ready {
                    self.ws.configure(domain: domain, key: key)
                    self.ws.connect()
                    // Start sync automatically
                    self.startSyncingLoop()
                } else {
                    self.ws.disconnect()
                    self.overallStatus = "Missing domain/password"
                }
            }
        }.store(in: &cancellables)
    }

    // MARK: - UI actions (all in popover)

    func addFolder() {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = false
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        if panel.runModal() == .OK, let url = panel.url {
            folders.append(.init(url: url, progress: 0))
        }
    }

    func removeFolder(_ f: TrackedFolder) {
        folders.removeAll { $0.id == f.id }
    }

    // MARK: - Sync logic

    private var cancellables: Set<AnyCancellable> = []

    private func startSyncingLoop() {
        guard let settings, settings.ready else { return }
        Task.detached { [weak self] in
            while let self = self, await settings.ready {
                await self.syncAll()
                try? await Task.sleep(nanoseconds: 5_000_000_000) // 5s pause between passes
            }
        }
    }

    private func updateProgress(for url: URL, value: Double) {
        if let idx = folders.firstIndex(where: { $0.url == url }) {
            folders[idx].progress = value
        }
    }

    private func syncAll() async {
        // simple sequential pass; you can parallelize by folder if needed
        for folder in folders {
            await syncFolder(folder.url)
        }
    }

    private func syncFolder(_ root: URL) async {
        guard ws.isConnected() else { return }
        let allLocal = enumerateFilesRecursively(at: root)

        var completed = 0
        let total = max(allLocal.count, 1)

        for fileURL in allLocal {
            do {
                let remotePath = remotePathFor(fileURL, under: root)
                // Ask remote listing for *just that path's dir*
                let parent = (remotePath as NSString).deletingLastPathComponent
                let resp = try await ws.request { req in
                    var lf = ListFiles(); lf.path = parent.isEmpty ? "/" : parent
                    req.payload = .reqListFiles(lf)
                }
                var remoteHash: String?
                if case .respListOfFiles(let lof) = resp.payload {
                    remoteHash = lof.files.first(where: { $0.path == remotePath })?.hash
                }

                let localHash = try sha256Hex(of: fileURL)
                if remoteHash == localHash {
                    // skip upload
                } else {
                    try await upload(fileURL, to: remotePath)
                }
                completed += 1
                await MainActor.run {
                    self.updateProgress(for: root, value: Double(completed) / Double(total))
                }
            } catch {
                // you could post per-file errors to the UI
                completed += 1
                await MainActor.run {
                    self.updateProgress(for: root, value: Double(completed) / Double(total))
                }
            }
        }
    }

    private func upload(_ url: URL, to remotePath: String) async throws {
        let data = try Data(contentsOf: url)
        // created timestamp (seconds)
        //let created = SwiftProtobuf.Google_Protobuf_Timestamp(date: try url.resourceValues(forKeys: [.creationDateKey]).creationDate ?? Date())

        _ = try await ws.request { req in
            var up = UploadFile()
            up.path = remotePath
            up.content = data
            up.forceOverride = true
            //up.created = created
            req.payload = .reqUploadFile(up)
        }
    }

    private func enumerateFilesRecursively(at root: URL) -> [URL] {
        var urls: [URL] = []
        if let e = FileManager.default.enumerator(at: root, includingPropertiesForKeys: [.isRegularFileKey], options: [.skipsHiddenFiles]) {
            for case let file as URL in e {
                if (try? file.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true {
                    urls.append(file)
                }
            }
        }
        return urls
    }

    private func remotePathFor(_ file: URL, under root: URL) -> String {
        let rel = file.path.replacingOccurrences(of: root.path, with: "")
        // e.g.: /mac/<device>/<relative path> – adjust to your desired layout
        return rel.hasPrefix("/") ? rel : "/" + rel
    }

    private func sha256Hex(of url: URL) throws -> String {
        let data = try Data(contentsOf: url, options: .mappedIfSafe)
        let digest = SHA256.hash(data: data)
        return digest.map { String(format: "%02x", $0) }.joined()
    }
}
