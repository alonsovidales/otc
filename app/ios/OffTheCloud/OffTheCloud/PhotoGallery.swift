import SwiftUI
import SwiftProtobuf

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

    // WS from here (per your note)
    private let ws: WSClient
    private let deviceID: String
    private let localFolder: URL?

    // UI state
    @Published var tags: [String] = []
    @Published var chips: [String] = []
    @Published var queryInput: String = ""

    @Published var items: [Item] = []
    @Published var loading = false
    @Published var endReached = false
    private var token: String? = nil

    // Modal
    @Published var openIndex: Int? = nil
    @Published var hiResImage: UIImage? = nil
    @Published var showAlert = false
    @Published var alertMessage = ""

    // Selection (via long-press)
    @Published var selected: Set<String> = []

    init(deviceID: String, localPhotosFolder: URL?) {
        self.ws = WSClient()                // ← as requested
        self.deviceID = deviceID
        self.localFolder = localPhotosFolder
    }

    func onAppearInitial() {
        Task {
            await loadTags()
            await resetAndLoadFirstPage()
        }
    }

    func addChip(_ t: String) {
        let x = t.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !x.isEmpty, !chips.contains(x) else { return }
        chips.append(x)
        Task { await resetAndLoadFirstPage() }
    }
    func removeChip(_ t: String) {
        chips.removeAll { $0 == t }
        Task { await resetAndLoadFirstPage() }
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

    // MARK: Paging
    func resetAndLoadFirstPage() async {
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
        loading = true
        defer { loading = false }

        do {
            let resp = try await ws.request { e in
                var req = ReqEnvelope()
                var sp  = SearchPhotosMsg()
                sp.tags  = self.chips
                sp.token = overrideToken ?? self.token ?? ""
                req.payload = .reqSearchPhotos(sp)
                e = req
            }
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
        Task { await fetchHiRes(index: index) }
    }
    func closeModal() { openIndex = nil; hiResImage = nil }
    func prev() { if let i = openIndex, i > 0 { open(index: i-1) } }
    func next() { if let i = openIndex, i < items.count - 1 { open(index: i+1) } }

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
                if case .respAck(let a) = resp.payload, a.ok {
                    selected.removeAll()
                    alertMessage = "Shared!"
                } else {
                    alertMessage = "Share failed"
                }
            } catch { alertMessage = "Share failed: \(error.localizedDescription)" }
            showAlert = true
        }
    }

    func createGroup() { alertMessage = "Create group (not implemented)"; showAlert = true }
    func addToGroup() { alertMessage = "Add to existing group (not implemented)"; showAlert = true }

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
                        createGroup: vm.createGroup,
                        addGroup: vm.addToGroup,
                        shareLink: vm.shareLink,
                        downloadZip: vm.downloadZip
                    )
                    .transition(.move(edge: .bottom))
                }
            }
        }
        .onAppear { vm.onAppearInitial() }
        .alert(vm.alertMessage, isPresented: $vm.showAlert) { Button("OK", role: .cancel) {} }
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
                download: { vm.downloadZip() }
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

private struct PhotoTile: View {
    let item: PhotoGalleryVM.Item
    let isSelected: Bool
    let onTap: () -> Void
    let onLongPress: () -> Void

    var body: some View {
        ZStack(alignment: .topLeading) {
            Button(action: onTap) {
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
            }
            .buttonStyle(.plain)
            .simultaneousGesture(LongPressGesture(minimumDuration: 0.25).onEnded { _ in onLongPress() })
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

private struct ImageModal: View {
    let image: UIImage?
    let showPrev: Bool
    let showNext: Bool
    let prev: () -> Void
    let next: () -> Void
    let close: () -> Void
    let download: () -> Void

    var body: some View {
        ZStack {
            Color.black.opacity(0.9).ignoresSafeArea()
            VStack {
                HStack {
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
                } else {
                    ProgressView().tint(.white).padding()
                }

                HStack {
                    if showPrev { Button("‹ Prev", action: prev) }
                    Spacer()
                    Button("Download", action: download)
                    Spacer()
                    if showNext { Button("Next ›", action: next) }
                }.padding()
            }.padding()
        }
    }
}

private struct ActionBar: View {
    let count: Int
    let share: () -> Void
    let createGroup: () -> Void
    let addGroup: () -> Void
    let shareLink: () -> Void
    let downloadZip: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Button("Share in social", action: share)
            Divider().frame(height: 20)
            Button("Create group", action: createGroup)
            Button("Add to group", action: addGroup)
            Spacer()
            Button("Share link", action: shareLink)
            Button("Download", action: downloadZip)
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
