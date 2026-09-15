// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI
import SwiftProtobuf
import Photos
import MapKit
import CryptoKit

// MARK: - Proto typealiases (rename if your generated names differ)
typealias ReqEnvelope       = Msg_ReqEnvelope
typealias RespEnvelope      = Msg_RespEnvelope
typealias FileMsg           = Msg_File
typealias TagsListMsg       = Msg_TagsList
typealias SearchPhotosMsg   = Msg_SearchPhotos
typealias GetFileMsg        = Msg_GetFile
typealias UploadFileMsg     = Msg_UploadFile
typealias NewSocialPubMsg   = Msg_NewSocialPublication
typealias ShareFilesLinkMsg = Msg_ShareFilesLink
typealias DownloadSharedMsg = Msg_DownloadSharedLink
typealias AckMsg            = Msg_Ack

// MARK: - ViewModel (iOS only)
@MainActor
final class PhotoGalleryVM: ObservableObject {

    struct Item: Identifiable, Hashable {
        let id: String
        let path: String
        let mime: String
        let size: Int
        var thumbData: Data?
        var localURL: URL?
        var isLocalOnly: Bool
    }

    // Shared, already-authenticated connection (see OTCConnection.swift)
    private let ws = OTCConnection.shared
    private let deviceID: String
    private let localFolder: URL?

    // UI state
    @Published var tags: [String] = []
    @Published var chips: [String] = []
    @Published var queryInput: String = ""

    // Issue #52 follow-up: person filter, next to the tag search bar
    // rather than a separate People screen. Selecting more than one person
    // means AND, not OR - see dao.SearchMedia on the backend: a photo must
    // contain a face matched to *every* person selected, not just one.
    @Published var allPeople: [Msg_Person] = []
    @Published var selectedPeople: [String] = []
    @Published var editingPersonID: String? = nil
    @Published var editingPersonName: String = ""

    // Issue #74: merge two people the model split into separate identities.
    // A second, narrower "pick mode" layered on the person strip, distinct
    // from selectedPeople (that's the search filter, a different thing
    // people already multi-select for AND-search - conflating the two
    // would make clicking a person while merging also change the filter).
    @Published var mergeTargetID: String? = nil
    @Published var pendingMerge: PendingMerge? = nil
    struct PendingMerge { let target: Msg_Person; let source: Msg_Person }

    @Published var items: [Item] = []
    @Published var loading = false
    @Published var endReached = false
    private var token: String? = nil
    // Bumped every time a fresh search starts (resetAndLoadFirstPage) -
    // fetchPage captures the value at call time and checks it's unchanged
    // before applying its response. Toggling a person filter (or a tag)
    // twice in quick succession spawns two overlapping Tasks; without
    // this, whichever *response* happens to land last wins even if it was
    // for the *older* selection - reproduced live on the web app as: tap
    // a person on then off quickly, the avatar shows selected/deselected
    // correctly but the grid shows the other request's (wrong) results,
    // because that one's reply simply arrived second.
    private var searchGeneration = 0
    // The in-flight Task from the *previous* restartSearch() call, if any -
    // explicitly cancelled the moment a newer one starts, on top of (not
    // instead of) searchGeneration above: cancellation alone can't stop a
    // request already in flight over the shared socket, so the generation
    // check is still what actually keeps a stale reply from being applied.
    // This just makes sure an old Task doesn't keep doing pointless work
    // (or hold onto stale local state) any longer than it has to.
    private var searchTask: Task<Void, Never>?

    // Every filter mutation (a tag or person toggled on/off) funnels
    // through here rather than each spawning its own bare `Task { }` -
    // reported live as clicking a person filter repeatedly sometimes
    // showing unrelated photos mixed into the correct ones.
    private func restartSearch() {
        searchTask?.cancel()
        searchTask = Task { await resetAndLoadFirstPage() }
    }

    // Modal
    @Published var openIndex: Int? = nil
    @Published var hiResImage: UIImage? = nil
    @Published var showAlert = false
    @Published var alertMessage = ""
    // Issue #9: the system share sheet for the currently-open photo. A file
    // URL, not the raw UIImage — handing UIActivityViewController a large
    // in-memory UIImage directly is what made the share sheet visibly slow
    // to appear (it has to synchronously render previews from the full
    // decoded bitmap); writing it to a temp file first lets it use the
    // usual, much faster file-based path instead.
    @Published var shareURL: URL? = nil

    // Selection (via long-press)
    @Published var selected: Set<String> = []
    // Issue #45: confirm before bulk-deleting the current multi-selection.
    @Published var confirmDeleteSelected = false

    // Issue #41: "More info" — camera/EXIF metadata computed live on the
    // server from the file's own bytes.
    @Published var infoOpen = false
    @Published var infoLoading = false
    @Published var infoData: Msg_FileExifInfo? = nil

    init(deviceID: String, localPhotosFolder: URL?) {
        self.deviceID = deviceID
        self.localFolder = localPhotosFolder
    }

    func onAppearInitial() {
        Task {
            await loadTags()
            await loadPeople()
            await resetAndLoadFirstPage()
        }
    }

    func addChip(_ t: String) {
        let x = t.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !x.isEmpty, !chips.contains(x) else { return }
        chips.append(x)
        restartSearch()
    }
    func removeChip(_ t: String) {
        chips.removeAll { $0 == t }
        restartSearch()
    }

    // MARK: Tags
    private func loadTags() async {
        do {
            let resp = try await ws.request { e in
                var env = e
                env.payload = .reqGetTags(.init())
                e = env
            }
            if case .respTagsList(let tl) = resp.payload {
                self.tags = tl.tags
            }
        } catch { /* ignore */ }
    }

    // MARK: People (issue #52 follow-up)
    private func loadPeople() async {
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            req.payload = .reqListPeople(.init())
            e = req
        }) else { return }
        if case .respPeople(let p) = resp.payload { allPeople = p.people }
    }

    func togglePerson(_ id: String) {
        if let idx = selectedPeople.firstIndex(of: id) {
            selectedPeople.remove(at: idx)
        } else {
            selectedPeople.append(id)
        }
        restartSearch()
    }

    func startRenamePerson(_ p: Msg_Person) {
        editingPersonID = p.id
        editingPersonName = p.name
    }

    func commitRenamePerson(_ id: String) async {
        let name = editingPersonName.trimmingCharacters(in: .whitespacesAndNewlines)
        editingPersonID = nil
        var req = Msg_RenamePerson()
        req.id = id
        req.name = name
        guard let resp = try? await ws.request({ e in
            var envelope = ReqEnvelope()
            envelope.payload = .reqRenamePerson(req)
            e = envelope
        }) else { return }
        if case .respAck(let ack) = resp.payload, ack.ok {
            if let idx = allPeople.firstIndex(where: { $0.id == id }) { allPeople[idx].name = name }
        }
    }

    func deletePerson(_ id: String) async {
        var req = Msg_DeletePerson()
        req.id = id
        guard let resp = try? await ws.request({ e in
            var envelope = ReqEnvelope()
            envelope.payload = .reqDeletePerson(req)
            e = envelope
        }) else { return }
        if case .respAck(let ack) = resp.payload, ack.ok {
            allPeople.removeAll { $0.id == id }
            selectedPeople.removeAll { $0 == id }
        }
    }

    // Tapping a person's avatar while merge-picking is active merges
    // instead of toggling the search filter - see mergeTargetID's own doc
    // comment. Mirrors web's pickMergeTarget in PhotoGallery.tsx.
    func pickMergeTarget(_ p: Msg_Person) {
        if mergeTargetID == p.id {
            mergeTargetID = nil // tapped the target again - cancel picking
            return
        }
        guard let target = allPeople.first(where: { $0.id == mergeTargetID }) else { return }
        mergeTargetID = nil
        pendingMerge = PendingMerge(target: target, source: p)
    }

    func confirmMerge() async {
        guard let merge = pendingMerge else { return }
        pendingMerge = nil
        var req = Msg_MergePeople()
        req.targetID = merge.target.id
        req.sourceIds = [merge.source.id]
        guard let resp = try? await ws.request({ e in
            var envelope = ReqEnvelope()
            envelope.payload = .reqMergePeople(req)
            e = envelope
        }) else { return }
        if case .respAck(let ack) = resp.payload, ack.ok {
            selectedPeople.removeAll { $0 == merge.source.id }
            await loadPeople() // re-sort by the merged face count (issue #75) rather than patch counts by hand
        }
    }

    // MARK: Paging
    func resetAndLoadFirstPage() async {
        // Invalidates any still-in-flight fetchPage from the *previous*
        // selection before this one's own request even goes out - see
        // searchGeneration's doc comment.
        searchGeneration += 1
        loading = false
        endReached = false
        token = ""
        items = []
        selected.removeAll()
        await fetchPage(overrideToken: "")
        mergeLocalIfAny()
    }

    func loadMoreIfNeeded(current item: Item?) async {
        guard let item else { return }
        guard !loading, !endReached else { return }
        if let idx = items.firstIndex(of: item), idx >= items.count - 12 {
            await fetchPage()
        }
    }

    private func fetchPage(overrideToken: String? = nil) async {
        guard !loading, !endReached else { return }
        let myGeneration = searchGeneration
        loading = true
        defer {
            // Only this request's own generation may clear loading - a
            // stale one finishing after a newer search started must not
            // report "done" for a fetch that isn't actually the current
            // one.
            if myGeneration == searchGeneration { loading = false }
        }

        // Snapshot the filter right now, not inside the request-building
        // closure below: OTCConnection.request can suspend for a while
        // before that closure actually runs (ensureConnected() may need to
        // reconnect/re-auth first), and the closure reads through `self`,
        // not a captured value - so without this snapshot, a filter change
        // that lands in that window would make THIS call silently send
        // whatever the *newer* filter is instead of the one it was invoked
        // for, wiring an unrelated result set to this generation's id and
        // defeating the myGeneration guard entirely (it only protects
        // against a stale *response*, not a request that mutated out from
        // under itself before it was even sent).
        let tags = chips
        let people = selectedPeople
        let requestToken = overrideToken ?? token ?? ""

        do {
            let resp = try await ws.request { e in
                var req = ReqEnvelope()
                var sp  = SearchPhotosMsg()
                sp.tags  = tags
                sp.personIds = people
                sp.token = requestToken
                req.payload = .reqSearchPhotos(sp)
                e = req
            }
            // A newer search superseded this one while it was in flight -
            // discard rather than let a stale reply clobber current
            // results. Task.isCancelled backs up the generation check
            // (restartSearch cancels this call's Task the moment a newer
            // one starts) - belt and suspenders, since either alone
            // catches the same case here.
            guard myGeneration == searchGeneration, !Task.isCancelled else { return }
            guard case .respListOfFiles(let lof) = resp.payload else { return }

            var newItems: [Item] = []
            for f in lof.files {
                let id = "\(f.path)#\(f.hash)#\(f.size)"
                newItems.append(Item(
                    id: id,
                    path: f.path,
                    mime: f.mime,
                    size: Int(f.size),
                    thumbData: f.hasContent ? f.content : nil,
                    localURL: nil,
                    isLocalOnly: false
                ))
            }
            let existing = Set(items.map(\.id))
            let filtered = newItems.filter { !existing.contains($0.id) }
            if !filtered.isEmpty { items.append(contentsOf: filtered) }

            self.token = lof.token.isEmpty ? nil : lof.token
            self.endReached = (self.token == nil)
        } catch { /* ignore for now */ }
    }

    // MARK: Local merge
    private func remotePathForLocal(url: URL) -> String {
        let root = (localFolder?.path ?? "")
        let rel = url.path.replacingOccurrences(of: root, with: "")
            .trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        return "/ios/\(deviceID)/\(rel)"
    }

    private func scanLocalFiles() -> [URL] {
        guard let folder = localFolder else { return [] }
        var list: [URL] = []
        if let en = FileManager.default.enumerator(at: folder, includingPropertiesForKeys: nil) {
            for case let u as URL in en {
                if u.hasDirectoryPath { continue }
                if ["jpg","jpeg","png","heic","gif","bmp","tiff"].contains(u.pathExtension.lowercased()) {
                    list.append(u)
                }
            }
        }
        return list
    }

    private func mergeLocalIfAny() {
        guard localFolder != nil else { return }
        // Local-only files are whatever's sitting in the sync folder that
        // hasn't made it to the server yet - by definition unfiled and
        // untagged there, so they can never legitimately match a tag/
        // person search. Without this guard every local file gets treated
        // as "missing from these (filtered) results" and dumped in
        // regardless of the active filter - reported live as a person
        // filter's results coming back mixed with a pile of unrelated
        // photos (a previous unfiltered browse's local files, not a stale
        // server response as first suspected).
        guard chips.isEmpty && selectedPeople.isEmpty else { return }
        let remotePaths = Set(items.map(\.path))
        let locals = scanLocalFiles()
        var adds: [Item] = []

        for u in locals {
            let rp = remotePathForLocal(url: u)
            if !remotePaths.contains(rp) {
                let data = try? Data(contentsOf: u)
                let id = "local#\(rp)#\(u.lastPathComponent)#\(data?.count ?? 0)"
                adds.append(Item(
                    id: id,
                    path: rp,
                    mime: "image/jpeg",
                    size: Int((try? u.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0),
                    thumbData: data,
                    localURL: u,
                    isLocalOnly: true
                ))
            }
        }
        if !adds.isEmpty { items.append(contentsOf: adds) }
    }

    // MARK: Modal hi-res
    func open(index: Int) {
        guard items.indices.contains(index) else { return }
        openIndex = index
        hiResImage = nil
        infoOpen = false
        infoData = nil
        Task { await fetchHiRes(index: index) }
    }
    func closeModal() { openIndex = nil; hiResImage = nil }

    // Issue #41: fetch and show the currently-open photo/video's
    // camera/EXIF metadata.
    func openInfo() {
        guard let idx = openIndex, items.indices.contains(idx) else { return }
        infoOpen = true
        infoLoading = true
        infoData = nil
        let path = items[idx].path
        Task {
            defer { infoLoading = false }
            do {
                let resp = try await ws.request { e in
                    var req = ReqEnvelope()
                    var gi = Msg_GetFileInfo()
                    gi.path = path
                    req.payload = .reqGetFileInfo(gi)
                    e = req
                }
                if case .respFileInfo(let info) = resp.payload {
                    infoData = info
                }
            } catch { /* leave infoData nil, shows "no metadata" */ }
        }
    }
    func closeInfo() { infoOpen = false; infoData = nil }
    func prev() { if let i = openIndex, i > 0 { open(index: i-1) } }
    func next() { if let i = openIndex, i < items.count - 1 { open(index: i+1) } }

    // Issue #9: share the already-loaded image — encoding it to a temp
    // file happens off the main actor so the button responds instantly;
    // only the (near-instant) file write's result touches published state.
    func shareCurrentPhoto(_ image: UIImage?) {
        guard let image else {
            alertMessage = "Image isn't loaded yet"
            showAlert = true
            return
        }
        Task.detached(priority: .userInitiated) {
            guard let data = image.jpegData(compressionQuality: 0.9) else { return }
            let tmp = FileManager.default.temporaryDirectory
                .appendingPathComponent(UUID().uuidString)
                .appendingPathExtension("jpg")
            do {
                try data.write(to: tmp)
                await MainActor.run { self.shareURL = tmp }
            } catch {
                await MainActor.run {
                    self.alertMessage = "Share failed: \(error.localizedDescription)"
                    self.showAlert = true
                }
            }
        }
    }

    // Issue #9: save the already-loaded (hi-res, or thumb as a fallback)
    // image straight to the Photos library — no re-download, no zip.
    func saveToPhotos(_ image: UIImage?) {
        guard let image else {
            alertMessage = "Image isn't loaded yet"
            showAlert = true
            return
        }
        Task {
            let current = PHPhotoLibrary.authorizationStatus(for: .addOnly)
            let status = current == .notDetermined
                ? await PHPhotoLibrary.requestAuthorization(for: .addOnly)
                : current
            guard status == .authorized || status == .limited else {
                alertMessage = "Photos access denied"
                showAlert = true
                return
            }
            do {
                try await PHPhotoLibrary.shared().performChanges {
                    PHAssetChangeRequest.creationRequestForAsset(from: image)
                }
                alertMessage = "Saved to Photos ✅"
            } catch {
                alertMessage = "Save failed: \(error.localizedDescription)"
            }
            showAlert = true
        }
    }

    // Issue #9: delete the currently-open photo, iOS Photos app-style —
    // removes it from the server and advances to the next one (or closes
    // the viewer if it was the last one left).
    func deleteCurrentPhoto() {
        guard let idx = openIndex, items.indices.contains(idx) else { return }
        let item = items[idx]
        Task {
            do {
                let resp = try await ws.request { e in
                    var req = ReqEnvelope()
                    var del = Msg_DelFile()
                    del.path = item.path
                    req.payload = .reqDelFile(del)
                    e = req
                }
                if resp.error {
                    alertMessage = "Delete failed: \(resp.errorMessage)"
                    showAlert = true
                    return
                }
            } catch {
                alertMessage = "Delete failed: \(error.localizedDescription)"
                showAlert = true
                return
            }

            items.remove(at: idx)
            selected.remove(item.path)
            if items.isEmpty {
                closeModal()
            } else {
                let nextIdx = min(idx, items.count - 1)
                open(index: nextIdx)
            }
        }
    }

    private func fetchHiRes(index: Int) async {
        let it = items[index]
        if let u = it.localURL, let img = UIImage(contentsOfFile: u.path) {
            self.hiResImage = img
            return
        }
        do {
            let resp = try await ws.request { e in
                var req = ReqEnvelope()
                var gf  = GetFileMsg()
                gf.path = it.path
                req.payload = .reqGetFile(gf)
                e = req
            }
            if case .respFile(let f) = resp.payload, let img = UIImage(data: f.content) {
                self.hiResImage = img
            }
        } catch { /* ignore */ }
    }

    // MARK: Selection (no checkbox; long-press toggles)
    func toggleSelect(_ path: String) {
        if selected.contains(path) { selected.remove(path) }
        else { selected.insert(path) }
    }

    // MARK: Actions
    private func ensureUploadedIfLocal(_ paths: [String]) async -> Bool {
        for p in paths {
            guard let idx = items.firstIndex(where: {$0.path == p}) else { continue }
            if items[idx].isLocalOnly, let url = items[idx].localURL {
                do {
                    let data = try Data(contentsOf: url)
                    let created = SwiftProtobuf.Google_Protobuf_Timestamp(date: Date())

                    // Issue #58: skip re-sending content the device
                    // already has under some other path — see
                    // PhotoSync.swift's identical check for the full
                    // reasoning.
                    let hash = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
                    let hasResp = try await ws.request { e in
                        var req = ReqEnvelope()
                        var hf = Msg_HasFile()
                        hf.hash = hash
                        req.payload = .reqHasFile(hf)
                        e = req
                    }

                    if case .respFileExists(let fe) = hasResp.payload, fe.exists {
                        _ = try await ws.request { e in
                            var req = ReqEnvelope()
                            var lf = Msg_LinkFile()
                            lf.hash = hash
                            lf.path = p
                            lf.forceOverride = true
                            lf.created = created
                            req.payload = .reqLinkFile(lf)
                            e = req
                        }
                    } else {
                        _ = try await ws.request { e in
                            var req = ReqEnvelope()
                            var up  = UploadFileMsg()
                            up.path = p
                            up.content = data
                            up.forceOverride = true
                            up.created = created
                            req.payload = .reqUploadFile(up)
                            e = req
                        }
                    }
                    items[idx].isLocalOnly = false
                } catch {
                    alertMessage = "Upload failed: \(error.localizedDescription)"
                    showAlert = true
                    return false
                }
            }
        }
        return true
    }

    // Issue #45: delete every currently-selected photo/video from the grid,
    // mirroring deleteCurrentPhoto()'s single-item flow above.
    func deleteSelected() {
        let paths = Array(selected)
        guard !paths.isEmpty else { return }
        Task {
            // Collect the successes and apply them as one batch at the end,
            // rather than mutating `items` (and so re-rendering the grid)
            // once per successful delete inside the loop. With several
            // selected items that are duplicates of the same underlying
            // photo — same thumbnail, adjacent cells — that one-at-a-time
            // pattern only ever visually removed the first one from the
            // LazyVGrid; the rest stayed on screen (looking like the
            // deletes silently failed) until the view reloaded from the
            // server, even though every delete had actually succeeded.
            var deletedPaths = Set<String>()
            for path in paths {
                do {
                    let resp = try await ws.request { e in
                        var req = ReqEnvelope()
                        var del = Msg_DelFile()
                        del.path = path
                        req.payload = .reqDelFile(del)
                        e = req
                    }
                    if resp.error {
                        alertMessage = "Delete failed: \(resp.errorMessage)"
                        showAlert = true
                        continue
                    }
                } catch {
                    alertMessage = "Delete failed: \(error.localizedDescription)"
                    showAlert = true
                    continue
                }
                deletedPaths.insert(path)
            }
            if !deletedPaths.isEmpty {
                items.removeAll { deletedPaths.contains($0.path) }
                selected.subtract(deletedPaths)
            }
        }
    }

    func shareInSocial() {
        Task {
            let paths = Array(selected)
            guard await ensureUploadedIfLocal(paths) else { return }
            let text = await promptSheet(title: "Caption")
            guard let text, !text.isEmpty else { return }
            do {
                let resp = try await ws.request { e in
                    var req = ReqEnvelope()
                    var pub = NewSocialPubMsg()
                    pub.text = text
                    pub.paths = paths
                    req.payload = .reqNewSocialPublication(pub)
                    e = req
                }
                // A successful ReqNewSocialPublication answers with
                // RespNewSocial (the new publication's uuid), not a plain
                // Ack — this was checking for the wrong case and reporting
                // "Share failed" on every successful share.
                if case .respNewSocial = resp.payload, !resp.error {
                    selected.removeAll()
                    alertMessage = "Shared!"
                } else {
                    alertMessage = resp.error ? "Share failed: \(resp.errorMessage)" : "Share failed"
                }
            } catch { alertMessage = "Share failed: \(error.localizedDescription)" }
            showAlert = true
        }
    }

    func shareLink() {
        Task {
            let paths = Array(selected)
            guard await ensureUploadedIfLocal(paths) else { return }
            do {
                let r = try await ws.request { e in
                    var req = ReqEnvelope()
                    var s = ShareFilesLinkMsg()
                    s.paths = paths
                    req.payload = .reqShareFilesLink(s)
                    e = req
                }
                if case .respShareLink(let link) = r.payload {
                    UIPasteboard.general.string = link.link
                    alertMessage = "Share link copied."
                } else { alertMessage = "Could not create share link." }
            } catch { alertMessage = "Share failed: \(error.localizedDescription)" }
            showAlert = true
        }
    }

    func downloadZip() {
        Task {
            let paths = Array(selected)
            guard await ensureUploadedIfLocal(paths) else { return }
            do {
                let r = try await ws.request { e in
                    var req = ReqEnvelope()
                    var s = ShareFilesLinkMsg()
                    s.paths = paths
                    req.payload = .reqShareFilesLink(s)
                    e = req
                }
                if case .respShareLink(let link) = r.payload, let url = URL(string: link.link) {
                    await UIApplication.shared.open(url)
                } else { alertMessage = "Could not create download link."; showAlert = true }
            } catch { alertMessage = "Download failed: \(error.localizedDescription)"; showAlert = true }
        }
    }

    // Simple async prompt for iOS (sheet-like)
    private func promptSheet(title: String) async -> String? {
        await withCheckedContinuation { cont in
            let alert = UIAlertController(title: title, message: nil, preferredStyle: .alert)
            alert.addTextField { $0.placeholder = "Write something…" }
            alert.addAction(UIAlertAction(title: "Cancel", style: .cancel) { _ in cont.resume(returning: nil) })
            alert.addAction(UIAlertAction(title: "OK", style: .default) { _ in
                cont.resume(returning: alert.textFields?.first?.text ?? "")
            })
            UIApplication.shared.topMost?.present(alert, animated: true)
        }
    }
}

// MARK: - SwiftUI View (iOS)

struct PhotoGalleryView: View {
    @StateObject private var vm: PhotoGalleryVM

    @State private var showSuggest = false
    @State private var personPendingDelete: String? = nil
    private let cols = Array(repeating: GridItem(.flexible(minimum: 120, maximum: 160), spacing: 10), count: 3)

    init(deviceID: String, localPhotosFolder: URL?) {
        _vm = StateObject(wrappedValue: PhotoGalleryVM(deviceID: deviceID, localPhotosFolder: localPhotosFolder))
    }

    var body: some View {
        VStack(spacing: 0) {
            // Chips + search
            VStack(alignment: .leading, spacing: 6) {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        ForEach(vm.chips, id: \.self) { chip in
                            HStack(spacing: 6) {
                                Text(chip)
                                Button("×") { vm.removeChip(chip) }
                            }
                            .padding(.horizontal, 8).padding(.vertical, 4)
                            .background(Color.blue.opacity(0.15))
                            .clipShape(Capsule())
                        }
                    }.padding(.horizontal, 8)
                }
                HStack(spacing: 8) {
                    TextField("Type a tag…", text: $vm.queryInput, onEditingChanged: { showSuggest = $0 }) {
                        acceptCurrentQuery()
                    }
                    .textFieldStyle(.roundedBorder)

                    Button("Search") { acceptCurrentQuery() }
                }
                .padding(.horizontal, 8)

                if showSuggest, !suggestions.isEmpty {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(suggestions, id: \.self) { s in
                            Button { acceptSuggestion(s) } label: {
                                HStack { Text(s); Spacer() }
                            }
                            .buttonStyle(.plain)
                            .padding(.vertical, 6).padding(.horizontal, 10)
                            .background(Color.secondary.opacity(0.08))
                        }
                    }
                    .clipShape(RoundedRectangle(cornerRadius: 8))
                    .padding(.horizontal, 8)
                }

                // Issue #52 follow-up: person filter, right next to the tag
                // search bar - combines with tags on the same search (e.g.
                // a person plus the "dogs" tag), and picking more than one
                // person means photos containing all of them, not just one.
                if !vm.allPeople.isEmpty {
                    Divider().padding(.horizontal, 8)
                    // Issue #74: while merge-picking is active, tapping a
                    // person's avatar below merges instead of toggling the
                    // search filter - see mergeTargetID's own doc comment.
                    if let targetID = vm.mergeTargetID {
                        HStack(spacing: 4) {
                            Text("Merging into \(vm.allPeople.first(where: { $0.id == targetID })?.name.isEmpty == false ? vm.allPeople.first(where: { $0.id == targetID })!.name : "Unnamed") — tap another person to merge them in.")
                            Button("Cancel") { vm.mergeTargetID = nil }
                        }
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 8)
                    }
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(alignment: .top, spacing: 12) {
                            ForEach(vm.allPeople, id: \.id) { person in
                                PersonFilterChip(
                                    person: person,
                                    isSelected: vm.selectedPeople.contains(person.id),
                                    isMergeTarget: vm.mergeTargetID == person.id,
                                    isEditing: vm.editingPersonID == person.id,
                                    editingName: $vm.editingPersonName,
                                    onTap: {
                                        if vm.mergeTargetID != nil {
                                            vm.pickMergeTarget(person)
                                        } else {
                                            vm.togglePerson(person.id)
                                        }
                                    },
                                    onStartRename: { vm.startRenamePerson(person) },
                                    onCommitRename: { Task { await vm.commitRenamePerson(person.id) } },
                                    onMerge: { vm.mergeTargetID = person.id },
                                    onDelete: { personPendingDelete = person.id }
                                )
                            }
                        }
                        .padding(.horizontal, 8)
                    }
                }
                // Nothing renders at all when there's no one to filter by
                // yet - no explanatory hint needed.
            }
            .padding(.vertical, 8)
            .background(.ultraThinMaterial)

            // Grid
            ScrollView {
                LazyVGrid(columns: cols, spacing: 10) {
                    ForEach(vm.items) { it in
                        PhotoTile(
                            item: it,
                            isSelected: vm.selected.contains(it.path),
                            hasSelection: !vm.selected.isEmpty,
                            onTap: { openPath(it.path) },
                            onLongPress: { vm.toggleSelect(it.path) }
                        )
                        .task { await vm.loadMoreIfNeeded(current: it) }
                    }
                    if vm.loading {
                        ProgressView().frame(height: 60).gridCellColumns(cols.count)
                    }
                }
                .padding(10)
            }
            .overlay(alignment: .bottom) {
                if !vm.selected.isEmpty {
                    ActionBar(
                        count: vm.selected.count,
                        share: vm.shareInSocial,
                        shareLink: vm.shareLink,
                        downloadZip: vm.downloadZip,
                        delete: { vm.confirmDeleteSelected = true }
                    )
                    .transition(.move(edge: .bottom))
                }
            }
        }
        .onAppear { vm.onAppearInitial() }
        // Hide the global upload indicator while the multi-select action
        // bar is showing — the two would otherwise stack at the bottom.
        .onChange(of: vm.selected.isEmpty) { _, isEmpty in
            UploadModel.shared.suppressed = !isEmpty
        }
        .onDisappear { UploadModel.shared.suppressed = false }
        .confirmationDialog(
            "Delete \(vm.selected.count) item\(vm.selected.count == 1 ? "" : "s")?",
            isPresented: $vm.confirmDeleteSelected,
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive, action: vm.deleteSelected)
            Button("Cancel", role: .cancel) {}
        }
        .alert(vm.alertMessage, isPresented: $vm.showAlert) { Button("OK", role: .cancel) {} }
        .confirmationDialog(
            "Delete this person? This removes every face matched to them — it can't be undone.",
            isPresented: Binding(get: { personPendingDelete != nil }, set: { if !$0 { personPendingDelete = nil } }),
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) {
                if let id = personPendingDelete { Task { await vm.deletePerson(id) } }
            }
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog(
            vm.pendingMerge.map {
                "Merge \($0.source.name.isEmpty ? "Unnamed" : $0.source.name) into \($0.target.name.isEmpty ? "Unnamed" : $0.target.name)? Every photo of \($0.source.name.isEmpty ? "Unnamed" : $0.source.name) will show up under \($0.target.name.isEmpty ? "Unnamed" : $0.target.name) instead — this can't be undone."
            } ?? "",
            isPresented: Binding(get: { vm.pendingMerge != nil }, set: { if !$0 { vm.pendingMerge = nil } }),
            titleVisibility: .visible
        ) {
            Button("Merge", role: .destructive) { Task { await vm.confirmMerge() } }
            Button("Cancel", role: .cancel) { vm.pendingMerge = nil }
        }
        .sheet(item: Binding(
            get: { vm.openIndex.map { SheetIndex(index: $0) } },
            set: { vm.openIndex = $0?.index }
        )) { _ in
            ImageModal(
                image: vm.hiResImage ?? (vm.openIndex.flatMap { idxFromThumb($0) }),
                showPrev: (vm.openIndex ?? 0) > 0,
                showNext: (vm.openIndex ?? 0) < vm.items.count - 1,
                prev: vm.prev,
                next: vm.next,
                close: vm.closeModal,
                save: { vm.saveToPhotos(vm.hiResImage ?? (vm.openIndex.flatMap { idxFromThumb($0) })) },
                share: { vm.shareCurrentPhoto(vm.hiResImage ?? (vm.openIndex.flatMap { idxFromThumb($0) })) },
                delete: { vm.deleteCurrentPhoto() },
                // A second top-level `.sheet` on this same view (which is
                // what this used to be) can't present while the ImageModal
                // sheet above is already up — SwiftUI silently drops it, no
                // error, no share sheet, ever. Nesting it inside ImageModal
                // itself presents it from *that* sheet's own view instead,
                // which works.
                shareURL: $vm.shareURL,
                // Issue #41: same nesting reasoning applies to the "More
                // info" panel.
                showInfo: { vm.openInfo() },
                infoOpen: $vm.infoOpen,
                infoLoading: vm.infoLoading,
                infoData: vm.infoData
            )
        }
    }

    // Suggestions
    private var suggestions: [String] {
        let q = vm.queryInput.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !q.isEmpty else { return [] }
        return vm.tags.filter { $0.lowercased().hasPrefix(q) }.prefix(12).map { $0 }
    }
    private func acceptCurrentQuery() {
        let q = vm.queryInput.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { return }
        if suggestions.count == 1 { vm.addChip(suggestions[0]) }
        else { vm.addChip(q) }
        vm.queryInput = ""
        showSuggest = false
    }
    private func acceptSuggestion(_ s: String) {
        vm.addChip(s)
        vm.queryInput = ""
        showSuggest = false
    }

    private func openPath(_ p: String) {
        if let idx = vm.items.firstIndex(where: { $0.path == p }) {
            vm.open(index: idx)
        }
    }
    private func idxFromThumb(_ idx: Int) -> UIImage? {
        guard vm.items.indices.contains(idx) else { return nil }
        let it = vm.items[idx]
        if let u = it.localURL, let img = UIImage(contentsOfFile: u.path) { return img }
        if let d = it.thumbData { return UIImage(data: d) }
        return nil
    }
}

// MARK: - UI pieces (iOS)

// Issue #52 follow-up: one person in the Photo Gallery's search bar - tap
// the avatar to toggle it as a filter, tap the name to rename, trash to
// delete. Replaces the standalone People tab/screen.
private struct PersonFilterChip: View {
    let person: Msg_Person
    let isSelected: Bool
    // Issue #74: distinct from isSelected (the search-filter state) - this
    // person is the currently-picked merge target, shown with its own
    // dashed highlight so the two states never look the same.
    let isMergeTarget: Bool
    let isEditing: Bool
    @Binding var editingName: String
    let onTap: () -> Void
    let onStartRename: () -> Void
    let onCommitRename: () -> Void
    let onMerge: () -> Void
    let onDelete: () -> Void

    var body: some View {
        VStack(spacing: 2) {
            Button(action: onTap) {
                Group {
                    if let img = UIImage(data: person.coverThumbnail) {
                        Image(uiImage: img).resizable().scaledToFill()
                    } else {
                        Color.gray.opacity(0.2).overlay(Image(systemName: "person.fill"))
                    }
                }
                .frame(width: 44, height: 44)
                .clipShape(Circle())
                .overlay(
                    Circle().stroke(
                        isMergeTarget ? Color.red : (isSelected ? Color.accentColor : .clear),
                        style: StrokeStyle(lineWidth: 3, dash: isMergeTarget ? [3, 2] : [])
                    )
                )
            }
            .buttonStyle(.plain)

            if isEditing {
                TextField("Name", text: $editingName, onCommit: onCommitRename)
                    .textFieldStyle(.roundedBorder)
                    .font(.caption2)
                    .frame(width: 60)
            } else {
                Button(action: onStartRename) {
                    Text(person.name.isEmpty ? "Unnamed" : person.name)
                        .font(.caption2)
                        .lineLimit(1)
                        .frame(maxWidth: 60)
                }
                .buttonStyle(.plain)
            }

            HStack(spacing: 8) {
                Button(action: onMerge) {
                    Image(systemName: "link").font(.system(size: 9)).foregroundStyle(.secondary)
                }
                Button(action: onDelete) {
                    Image(systemName: "trash").font(.system(size: 9)).foregroundStyle(.secondary)
                }
            }
        }
    }
}

private struct PhotoTile: View {
    let item: PhotoGalleryVM.Item
    let isSelected: Bool
    // Whether *any* tile in the grid is currently selected — while true,
    // tapping a tile toggles its selection instead of opening the preview,
    // matching the Photos app's selection-mode behavior.
    let hasSelection: Bool
    let onTap: () -> Void
    let onLongPress: () -> Void

    var body: some View {
        ZStack(alignment: .topLeading) {
            ZStack(alignment: .bottomTrailing) {
                thumb
                    .frame(maxWidth: 120, maxHeight: 120)
                    .background(Color.secondary.opacity(0.1))
                    .clipShape(RoundedRectangle(cornerRadius: 8))
                    .overlay(selectionOverlay)
                if item.isLocalOnly {
                    Label("", systemImage: "iphone")
                        .padding(4)
                        .background(.ultraThinMaterial)
                        .clipShape(Circle())
                        .padding(6)
                }
            }
            .contentShape(Rectangle())
            // Issue: long-pressing a tile used to ALSO open the preview —
            // it was a Button (tap) plus a *simultaneous* long-press
            // gesture, and "simultaneous" means both fire together on
            // release, by design. Plain SwiftUI tap/long-press gestures
            // (no Button) disambiguate properly instead of both firing.
            .onTapGesture {
                if hasSelection { onLongPress() } else { onTap() }
            }
            .onLongPressGesture(minimumDuration: 0.25) {
                onLongPress()
            }
        }
    }

    private var thumb: some View {
        Group {
            if let u = item.localURL, let img = UIImage(contentsOfFile: u.path) {
                Image(uiImage: img).resizable().scaledToFill()
            } else if let d = item.thumbData, let img = UIImage(data: d) {
                Image(uiImage: img).resizable().scaledToFill()
            } else {
                Color.gray.opacity(0.2)
            }
        }
        .clipped()
    }

    private var selectionOverlay: some View {
        Group {
            if isSelected {
                RoundedRectangle(cornerRadius: 8)
                    .stroke(Color.accentColor, lineWidth: 3)
                    .overlay(alignment: .topLeading) {
                        Image(systemName: "checkmark.circle.fill")
                            .foregroundColor(.accentColor)
                            .padding(6)
                    }
            }
        }
    }
}

/// The Photos app's own single-image viewer, minus the album picker: no
/// Prev/Next buttons (issue #9) — page through with a swipe, same as the
/// Social feed's viewer (issue #13/#18) — plus share/save-to-Photos/delete
/// actions along the bottom, again matching the system Photos app.
private struct ImageModal: View {
    let image: UIImage?
    let showPrev: Bool
    let showNext: Bool
    let prev: () -> Void
    let next: () -> Void
    let close: () -> Void
    let save: () -> Void
    let share: () -> Void
    let delete: () -> Void
    @Binding var shareURL: URL?
    // Issue #41: "More info" — camera/EXIF metadata computed live on the
    // server from the file's own bytes.
    let showInfo: () -> Void
    @Binding var infoOpen: Bool
    let infoLoading: Bool
    let infoData: Msg_FileExifInfo?

    @State private var confirmDelete = false

    // Issue #36: same pinch-to-zoom already in the Social feed (issue
    // #28), now in the pictures section's own full-screen viewer too —
    // zooms around wherever the fingers actually are (MagnifyGesture's
    // startAnchor) and snaps back to normal on release, no extra
    // bookkeeping since @GestureState resets itself when the gesture ends.
    @GestureState private var pinchScale: CGFloat = 1.0
    @GestureState private var pinchAnchor: UnitPoint = .center

    var body: some View {
        ZStack {
            Color.black.opacity(0.9).ignoresSafeArea()
            VStack {
                HStack {
                    Button { showInfo() } label: {
                        Image(systemName: "info.circle").font(.title2).foregroundStyle(.white)
                    }
                    Spacer()
                    Button { close() } label: {
                        Image(systemName: "xmark.circle.fill").font(.title).foregroundStyle(.white)
                    }
                }.padding()

                if let image {
                    Image(uiImage: image)
                        .resizable()
                        .scaledToFit()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .contentShape(Rectangle())
                        .scaleEffect(pinchScale, anchor: pinchAnchor)
                        .animation(.spring(response: 0.3, dampingFraction: 0.7), value: pinchScale)
                        // Issue #9: swipe left/right to page through photos —
                        // no Prev/Next buttons, matching the Social feed's
                        // swipe (issue #13/#18) and the system Photos app.
                        .gesture(
                            DragGesture(minimumDistance: 20)
                                .onEnded { value in
                                    if value.translation.width < -30, showNext { next() }
                                    else if value.translation.width > 30, showPrev { prev() }
                                }
                        )
                        // Simultaneous (not a replacement) so pinching
                        // doesn't get swallowed by the swipe gesture above.
                        .simultaneousGesture(
                            MagnifyGesture()
                                .updating($pinchScale) { value, state, _ in
                                    state = value.magnification
                                }
                                .updating($pinchAnchor) { value, state, _ in
                                    state = value.startAnchor
                                }
                        )
                } else {
                    ProgressView().tint(.white).padding()
                }

                // Issue #9: share / save-to-Photos / delete, Photos
                // app-style, instead of a single "Download" that used to
                // just re-run the multi-select zip download.
                HStack(spacing: 48) {
                    Button { share() } label: {
                        Image(systemName: "square.and.arrow.up")
                    }
                    Button { save() } label: {
                        Image(systemName: "arrow.down.circle")
                    }
                    Button(role: .destructive) {
                        confirmDelete = true
                    } label: {
                        Image(systemName: "trash")
                    }
                }
                .font(.title2)
                .foregroundStyle(.white)
                .padding(.top, 8)
            }.padding()
        }
        .confirmationDialog("Delete this photo?", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("Delete Photo", role: .destructive, action: delete)
            Button("Cancel", role: .cancel) {}
        }
        // Nested inside this already-presented sheet rather than back on
        // PhotoGalleryView — see the call site's comment for why.
        .sheet(isPresented: Binding(
            get: { shareURL != nil },
            set: { if !$0 { shareURL = nil } }
        )) {
            if let url = shareURL { ActivityView(items: [url]) }
        }
        // Issue #41: same nesting reasoning as the share sheet above.
        .sheet(isPresented: $infoOpen) {
            FileInfoView(loading: infoLoading, info: infoData)
        }
    }
}

/// Issue #41: camera/EXIF metadata panel, with a native map for GPS.
private struct FileInfoView: View {
    let loading: Bool
    let info: Msg_FileExifInfo?
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationView {
            Group {
                if loading {
                    ProgressView()
                } else if let info {
                    List {
                        if !info.cameraMake.isEmpty || !info.cameraModel.isEmpty {
                            row("Camera", [info.cameraMake, info.cameraModel].filter { !$0.isEmpty }.joined(separator: " "))
                        }
                        if info.hasTakenAt {
                            row("Taken", info.takenAt.date.formatted(date: .abbreviated, time: .shortened))
                        }
                        if info.width > 0 && info.height > 0 {
                            row("Dimensions", "\(info.width) × \(info.height)")
                        }
                        if !info.exposureTime.isEmpty { row("Exposure", info.exposureTime) }
                        if !info.fNumber.isEmpty { row("Aperture", info.fNumber) }
                        if info.iso > 0 { row("ISO", "\(info.iso)") }
                        if !info.focalLength.isEmpty { row("Focal length", info.focalLength) }
                        if !info.city.isEmpty || !info.country.isEmpty {
                            row("Location", [info.city, info.country].filter { !$0.isEmpty }.joined(separator: ", "))
                        }
                        if info.hasGps_p {
                            Map(initialPosition: .region(MKCoordinateRegion(
                                center: CLLocationCoordinate2D(latitude: info.latitude, longitude: info.longitude),
                                span: MKCoordinateSpan(latitudeDelta: 0.05, longitudeDelta: 0.05)
                            ))) {
                                Marker("", coordinate: CLLocationCoordinate2D(latitude: info.latitude, longitude: info.longitude))
                            }
                            .frame(height: 200)
                            .listRowInsets(EdgeInsets())
                        }
                        if info.cameraMake.isEmpty && info.cameraModel.isEmpty && !info.hasGps_p && info.exposureTime.isEmpty {
                            Text("No EXIF metadata in this file").foregroundColor(.secondary)
                        }
                    }
                } else {
                    Text("No metadata found").foregroundColor(.secondary)
                }
            }
            .navigationTitle("More Info")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }

    private func row(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).foregroundColor(.secondary)
            Spacer()
            Text(value)
        }
    }
}

private struct ActionBar: View {
    let count: Int
    let share: () -> Void
    let shareLink: () -> Void
    let downloadZip: () -> Void
    let delete: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Button("Share in social", action: share)
            Spacer()
            Button("Share link", action: shareLink)
            Button("Download", action: downloadZip)
            Button(role: .destructive, action: delete) {
                Image(systemName: "trash")
            }
            Text("\(count) selected").foregroundColor(.secondary)
        }
        .padding(10)
        .background(.ultraThinMaterial)
        .overlay(Divider(), alignment: .top)
    }
}

private struct SheetIndex: Identifiable { let index: Int; var id: Int { index } }

// MARK: - Small UIKit helper to present alerts on top-most controller

private extension UIApplication {
    var topMost: UIViewController? {
        guard let s = connectedScenes.first as? UIWindowScene,
              let w = s.windows.first(where: { $0.isKeyWindow }),
              var top = w.rootViewController else { return nil }
        while let p = top.presentedViewController { top = p }
        return top
    }
}
