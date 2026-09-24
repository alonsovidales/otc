// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FilesExplorerView.swift
//  OffTheCloud
//
//  Native port of the web app's Files tab (web/src/components/FilesExplorer.tsx):
//  path navigation, upload (via the document picker — the native analogue of
//  the web's drag-and-drop), multi-select delete/share/download-zip, and an
//  image viewer for image files.

import SwiftUI
import CryptoKit
import UniformTypeIdentifiers
import QuickLook

private func isDirFile(_ f: Msg_File) -> Bool { f.mime == "inode/directory" }
private func isImgFile(_ f: Msg_File) -> Bool { f.mime.hasPrefix("image/") }

private func joinPath(_ base: String, _ leaf: String) -> String {
    let b = base.hasSuffix("/") ? String(base.dropLast()) : base
    let l = leaf.hasPrefix("/") ? String(leaf.dropFirst()) : leaf
    return "\(b)/\(l)"
}
private func dirnamePath(_ p: String) -> String {
    let clean = (p.hasSuffix("/") && p != "/") ? String(p.dropLast()) : p
    guard let idx = clean.lastIndex(of: "/"), clean.distance(from: clean.startIndex, to: idx) > 0 else { return "/" }
    return String(clean[..<idx])
}
private func normPath(_ p: String) -> String {
    var s = p.trimmingCharacters(in: .whitespacesAndNewlines)
    if !s.hasPrefix("/") { s = "/" + s }
    if !s.hasSuffix("/") { s += "/" }
    return s
}
private func leafName(_ full: String) -> String {
    full.split(separator: "/").last.map(String.init) ?? full
}

struct FileRow: Identifiable {
    var id: String { path }
    let path: String
    let name: String
    let isDir: Bool
    let size: Int32
    let created: Date?
    let modified: Date?
    // Issue #132: inside (or itself) an upload-only folder, and how many
    // older versions the device keeps for the file.
    let uploadOnly: Bool
    let versions: Int32
    let raw: Msg_File
}

@MainActor
final class FilesExplorerViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    @Published var path: String
    @Published var rows: [FileRow] = []
    @Published var loading = false
    @Published var error: String?
    @Published var selected: Set<String> = []
    @Published var toast: String?
    @Published var previewURL: URL?
    @Published var shareURL: URL?
    // Issue #71: the only feedback a tap on a file used to get was however
    // long GetFile's round trip actually took - nothing changed on screen
    // in the meantime, so it looked stuck, and a user tapping again (or on
    // other rows, thinking the first tap missed) queued up that many
    // concurrent opens, each popping its own viewer open once its own
    // fetch happened to finish. Tracking which single path is in flight
    // both drives a spinner on that row and - via the guard in open()
    // below - makes every other tap a no-op until it's done.
    @Published var openingPath: String?
    // Issue #54: every delete action should confirm first - this one
    // didn't.
    @Published var confirmDeleteSelected = false
    /// Which selection action is waiting on the device - the bar shows a
    /// spinner for it, since building a share link is a round trip.
    @Published var preparing: SelectionActionTask?
    // Issue #132: the versions sheet - the file it is for and the older
    // versions the device listed (newest first).
    @Published var versionsOf: (row: FileRow, versions: [Msg_File])?
    @Published var versionsLoading = false

    init(initialPath: String) { self.path = initialPath }

    func load() async {
        loading = true
        defer { loading = false }
        error = nil
        var req = Msg_ListFiles()
        req.path = path
        do {
            let resp = try await ws.request { $0.payload = .reqListFiles(req) }
            if case .respListOfFiles(let lof) = resp.payload {
                var files = lof.files
                if path != "/" {
                    var up = Msg_File()
                    up.mime = "inode/directory"
                    up.path = ".."
                    files.insert(up, at: 0)
                }
                rows = files.map { f in
                    FileRow(
                        path: f.path,
                        name: f.path == ".." ? ".." : leafName(f.path),
                        isDir: isDirFile(f),
                        size: f.size,
                        created: f.hasCreated ? f.created.date : nil,
                        modified: f.hasModified ? f.modified.date : nil,
                        uploadOnly: f.uploadOnly,
                        versions: f.versions,
                        raw: f
                    )
                }
                selected.removeAll()
            } else if resp.error {
                error = resp.errorMessage.isEmpty ? "Failed to list path" : resp.errorMessage
            } else {
                error = "Unexpected response"
            }
        } catch {
            self.error = error.localizedDescription
        }
    }

    func navigate(to newPath: String) {
        path = normPath(newPath)
        Task { await load() }
    }

    func fullPath(for row: FileRow) -> String {
        row.path.contains("/") ? row.path : joinPath(path, row.path)
    }

    func open(_ row: FileRow) async {
        if row.isDir {
            // issue #21: dirnamePath() already returns "/" once we're back
            // at the root — blindly appending another "/" after it (as
            // this used to) turned ".." from the top directory into "//".
            // navigate() below re-normalizes anyway, so just hand it the
            // bare dirname instead of guessing at a trailing slash here.
            let newPath = row.path == ".." ? dirnamePath(path) : normPath(joinPath(path, row.name))
            navigate(to: newPath)
            return
        }

        // Issue #71: single-flight - a tap while one open is already in
        // progress (this row or another) is ignored rather than queued, so
        // it can't pile up into several viewers popping open back to back
        // once each fetch happens to land.
        guard openingPath == nil else { return }
        openingPath = row.path
        defer { openingPath = nil }

        let full = fullPath(for: row)
        var req = Msg_GetFile()
        req.path = full
        do {
            let resp = try await ws.request { $0.payload = .reqGetFile(req) }
            guard case .respFile(let f) = resp.payload else {
                showToast("Could not fetch file")
                return
            }
            // Issue #72: QuickLook (the same previewer Mail/Files use for
            // attachments) natively renders PDFs, Office docs, text, audio
            // and video, not just images - writing to a temp file first
            // (rather than the old image-only UIImage(data:) path) is what
            // lets it identify the format at all, same as it would from a
            // Files app download. Its own toolbar already has a share
            // button, so this replaces the separate share-sheet fallback
            // for non-images too, not just adds preview alongside it.
            let tmp = FileManager.default.temporaryDirectory.appendingPathComponent(leafName(row.path))
            try f.content.write(to: tmp)
            previewURL = tmp
        } catch {
            showToast("Download failed: \(error.localizedDescription)")
        }
    }

    /// Issue #132: true when the selection touches an upload-only folder -
    /// the device refuses those deletes, so the button greys out first.
    var selectionUploadOnly: Bool {
        rows.contains { selected.contains($0.path) && $0.uploadOnly }
    }

    func deleteSelected() async {
        for p in selected {
            var req = Msg_DelFile()
            req.path = p.contains("/") ? p : joinPath(path, p)
            guard let resp = try? await ws.request({ $0.payload = .reqDelFile(req) }) else { continue }
            if resp.error && resp.errorCode == "upload_only" {
                showToast("\(leafName(req.path)) is in an upload-only folder and cannot be deleted")
                break
            }
        }
        await load()
    }

    /// Issue #132: the lock on a folder - flag it upload only, or clear it.
    func toggleUploadOnly(_ row: FileRow) async {
        var req = Msg_SetUploadOnly()
        req.path = fullPath(for: row)
        req.uploadOnly = !row.uploadOnly
        if let resp = try? await ws.request({ $0.payload = .reqSetUploadOnly(req) }), resp.error {
            showToast(resp.errorMessage.isEmpty ? "Could not update the folder" : resp.errorMessage)
        }
        await load()
    }

    /// Issue #132: the versions badge - list the file's older versions.
    func openVersions(_ row: FileRow) async {
        versionsLoading = true
        versionsOf = (row, [])
        defer { versionsLoading = false }
        var req = Msg_ListFileVersions()
        req.path = fullPath(for: row)
        guard let resp = try? await ws.request({ $0.payload = .reqListFileVersions(req) }),
              case .respFileVersions(let v) = resp.payload else { return }
        versionsOf = (row, v.versions)
    }

    /// A version opens in the same Quick Look preview a file does; an empty
    /// hash is the current one.
    func openVersion(_ row: FileRow, hash: String) async {
        var req = Msg_GetFile()
        req.path = fullPath(for: row)
        req.hash = hash
        do {
            let resp = try await ws.request { $0.payload = .reqGetFile(req) }
            guard case .respFile(let f) = resp.payload else {
                showToast("Could not fetch that version")
                return
            }
            let tmp = FileManager.default.temporaryDirectory.appendingPathComponent(leafName(row.path))
            try f.content.write(to: tmp)
            versionsOf = nil
            previewURL = tmp
        } catch {
            showToast("Download failed: \(error.localizedDescription)")
        }
    }

    /// Downloads the selection, the same way the Images tab does it: the
    /// share link the device hands back is itself the download.
    func downloadSelected() async {
        preparing = .download
        defer { preparing = nil }
        guard let link = await shareLink(), let url = URL(string: link) else {
            showToast("Could not create download link")
            return
        }
        await UIApplication.shared.open(url)
    }

    func shareSelected() async {
        preparing = .share
        defer { preparing = nil }
        guard let link = await shareLink(), let url = URL(string: link) else {
            showToast("Could not create share link")
            return
        }
        shareURL = url
    }

    func shareLink() async -> String? {
        guard !selected.isEmpty else { return nil }
        var req = Msg_ShareFilesLink()
        req.paths = selected.map { $0.contains("/") ? $0 : joinPath(path, $0) }
        guard let resp = try? await ws.request({ $0.payload = .reqShareFilesLink(req) }),
              case .respShareLink(let link) = resp.payload else { return nil }
        return link.link
    }

    func upload(data: Data, filename: String) async {
        let path = joinPath(path, filename)
        do {
            // Issue #58: skip re-sending content the device already has
            // under some other path — see PhotoSync.swift's identical
            // check for the full reasoning.
            let hash = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
            let hasResp = try await ws.request { e in
                var hf = Msg_HasFile()
                hf.hash = hash
                e.payload = .reqHasFile(hf)
            }

            let resp: Msg_RespEnvelope
            if case .respFileExists(let fe) = hasResp.payload, fe.exists {
                resp = try await ws.request { e in
                    var lf = Msg_LinkFile()
                    lf.hash = hash
                    lf.path = path
                    lf.forceOverride = false
                    e.payload = .reqLinkFile(lf)
                }
            } else {
                var req = Msg_UploadFile()
                req.path = path
                req.content = data
                req.forceOverride = false
                resp = try await ws.request { $0.payload = .reqUploadFile(req) }
            }
            if resp.error { showToast("Upload failed: \(resp.errorMessage)") }
        } catch {
            showToast("Upload failed: \(error.localizedDescription)")
        }
        await load()
    }

    func showToast(_ m: String) {
        toast = m
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_500_000_000)
            if self?.toast == m { self?.toast = nil }
        }
    }
}

struct FilesExplorerView: View {
    @StateObject private var vm: FilesExplorerViewModel
    @State private var pathField: String
    @State private var showImporter = false
    // Selection is managed here rather than with List(selection:) +
    // EditButton(): that pairing needs the row's tap to be the List's own
    // selection-toggle handling, and this view also needs a tap to open
    // the file, which intercepts the touch first and left "Edit" mode
    // silently doing nothing. There is no select mode any more either -
    // every row carries its own checkbox all the time (the web's table
    // has had one per row from the start), tapping the checkbox selects
    // and tapping the rest of the row opens, so nothing has to be
    // switched on before something can be picked.

    init(initialPath: String) {
        _vm = StateObject(wrappedValue: FilesExplorerViewModel(initialPath: initialPath))
        _pathField = State(initialValue: initialPath)
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                HStack {
                    TextField("/path/", text: $pathField, onCommit: { vm.navigate(to: pathField) })
                        .textFieldStyle(.roundedBorder)
                        .autocapitalization(.none)
                    if vm.loading { ProgressView() }
                }
                .padding([.horizontal, .top])

                if let error = vm.error {
                    Text(error).font(.caption).foregroundColor(.red).padding(.horizontal)
                }

                List {
                    ForEach(vm.rows) { row in
                        HStack {
                            // The row's own checkbox, always there - see the
                            // note on selection at the top of this view.
                            // Issue #116: directories are selectable too -
                            // the device expands one to every file under it
                            // for share/download/delete (see
                            // files_manager.resolvePaths). Only ".." has
                            // none, being navigation rather than a thing;
                            // it keeps the width so names stay aligned.
                            if row.path != ".." {
                                Button {
                                    if vm.selected.contains(row.path) { vm.selected.remove(row.path) }
                                    else { vm.selected.insert(row.path) }
                                } label: {
                                    Image(systemName: vm.selected.contains(row.path) ? "checkmark.circle.fill" : "circle")
                                        .foregroundColor(vm.selected.contains(row.path) ? .accentColor : .secondary)
                                        .frame(width: 28, height: 28)
                                        .contentShape(Rectangle())
                                }
                                // .plain: inside a List a default Button
                                // claims the whole row's tap, and the row
                                // tap below has to stay "open".
                                .buttonStyle(.plain)
                                .accessibilityLabel(vm.selected.contains(row.path) ? "Deselect" : "Select")
                            } else {
                                Color.clear.frame(width: 28, height: 28)
                            }
                            // Issue #71: a spinner in place of the row's own
                            // icon while its GetFile round trip is in
                            // flight - the only feedback a tap used to get
                            // was however long that took, which just
                            // looked stuck.
                            if vm.openingPath == row.path {
                                ProgressView().frame(width: 20)
                            } else {
                                Image(systemName: row.isDir ? "folder.fill" : (isImgFile(row.raw) ? "photo" : "doc"))
                                    .foregroundColor(row.isDir ? .accentColor : .secondary)
                                    .frame(width: 20)
                            }
                            VStack(alignment: .leading) {
                                Text(row.name).lineLimit(1)
                                if !row.isDir {
                                    Text(formatBytes(row.size)).font(.caption2).foregroundColor(.secondary)
                                }
                            }
                            Spacer()
                            // Issue #132: the versions badge opens the
                            // sheet; the lock on a folder toggles upload
                            // only, on a file it just says it is inside one.
                            if !row.isDir && row.versions > 0 {
                                Button {
                                    Task { await vm.openVersions(row) }
                                } label: {
                                    Label("\(row.versions)", systemImage: "clock.arrow.circlepath")
                                        .font(.caption)
                                        .padding(.horizontal, 8).padding(.vertical, 3)
                                        .background(Color.secondary.opacity(0.15), in: Capsule())
                                }
                                .buttonStyle(.plain)
                                .accessibilityLabel("\(row.versions) older version\(row.versions == 1 ? "" : "s")")
                            }
                            if row.path != ".." && row.isDir {
                                Button {
                                    Task { await vm.toggleUploadOnly(row) }
                                } label: {
                                    Image(systemName: row.uploadOnly ? "lock.fill" : "lock.open")
                                        .foregroundColor(row.uploadOnly ? .accentColor : .secondary)
                                        .frame(width: 28, height: 28)
                                        .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                                .accessibilityLabel(row.uploadOnly ? "Clear upload only" : "Make upload only")
                            } else if row.uploadOnly {
                                Image(systemName: "lock.fill").foregroundColor(.secondary).frame(width: 28, height: 28)
                                    .accessibilityLabel("In an upload-only folder")
                            }
                        }
                        .contentShape(Rectangle())
                        .onTapGesture {
                            // Issue #71: ignore taps while any row's open is
                            // already in flight - see openingPath's own doc
                            // comment for why that's the fix, not just the
                            // spinner above.
                            if vm.openingPath == nil {
                                Task { await vm.open(row) }
                            }
                        }
                        // Long-press for quick Share/Delete on a single
                        // file, independent of (and without needing) the
                        // Select mode above - the standard iOS pattern for
                        // one-off actions on a single item. Directories
                        // get it too since issue #116 (the device expands
                        // one to its files); only ".." is left out.
                        .contextMenu {
                            if row.path != ".." {
                                Button {
                                    Task {
                                        vm.selected = [row.path]
                                        if let link = await vm.shareLink(), let url = URL(string: link) {
                                            vm.shareURL = url
                                        }
                                    }
                                } label: {
                                    Label("Share", systemImage: "square.and.arrow.up")
                                }
                                if row.isDir {
                                    Button {
                                        Task { await vm.toggleUploadOnly(row) }
                                    } label: {
                                        Label(row.uploadOnly ? "Clear upload only" : "Make upload only", systemImage: row.uploadOnly ? "lock.open" : "lock")
                                    }
                                }
                                if !row.uploadOnly {
                                    Button(role: .destructive) {
                                        vm.selected = [row.path]
                                        vm.confirmDeleteSelected = true
                                    } label: {
                                        Label("Delete", systemImage: "trash")
                                    }
                                }
                            }
                        }
                    }
                }
                .listStyle(.plain)
                // Issue #51: pull down to re-list this directory - files
                // arrive from other clients (the Mac app, another phone)
                // while this screen sits open, and nothing else re-reads it.
                .refreshable { await vm.load() }

                // Always shown here, unlike Images: Upload acts on the
                // folder being browsed, not on a selection, so it needs
                // somewhere to live when nothing is selected - and this is
                // where the rest of the actions are. Share/Download/Delete
                // grey out until something is ticked.
                SelectionActionBar(
                    count: vm.selected.count,
                    busy: vm.preparing,
                    onShare: { Task { await vm.shareSelected() } },
                    onDownload: { Task { await vm.downloadSelected() } },
                    onDelete: {
                        if vm.selectionUploadOnly {
                            vm.showToast("The selection is in an upload-only folder and cannot be deleted")
                        } else {
                            vm.confirmDeleteSelected = true
                        }
                    },
                    onUpload: { showImporter = true }
                )
                .padding(.vertical, 8)
            }
            // No nav title (issue #19): the tab bar already labels this
            // screen "Files", and the path field above already shows where
            // you are. Still .inline so there's no big empty title bar.
            .navigationBarTitleDisplayMode(.inline)
            .overlay(alignment: .top) {
                if let toast = vm.toast {
                    Text(toast)
                        .padding(.horizontal, 12).padding(.vertical, 8)
                        .background(.ultraThinMaterial, in: Capsule())
                        .padding(.top, 8)
                }
            }
        }
        .task { await vm.load() }
        .onChange(of: vm.path) { _, newValue in pathField = newValue }
        .fileImporter(isPresented: $showImporter, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            guard case .success(let urls) = result else { return }
            for url in urls {
                guard url.startAccessingSecurityScopedResource() else { continue }
                defer { url.stopAccessingSecurityScopedResource() }
                if let data = try? Data(contentsOf: url) {
                    Task { await vm.upload(data: data, filename: url.lastPathComponent) }
                }
            }
        }
        .sheet(isPresented: Binding(get: { vm.previewURL != nil }, set: { if !$0 { vm.previewURL = nil } })) {
            if let url = vm.previewURL {
                QuickLookView(url: url, onDismiss: { vm.previewURL = nil })
                    // Draws edge-to-edge with its own navigation bar
                    // (title, Done button, share button - see
                    // QuickLookView's own doc comment) - ignoresSafeArea
                    // keeps that bar from getting a second inset on top of
                    // the one it already draws.
                    .ignoresSafeArea()
            }
        }
        .sheet(isPresented: Binding(get: { vm.shareURL != nil }, set: { if !$0 { vm.shareURL = nil } })) {
            if let url = vm.shareURL {
                ActivityView(items: [url])
            }
        }
        // Issue #132: the versions pop-up - the current file and every
        // older version with when it was replaced and its size; a tap
        // opens that version in the same preview a file gets.
        .sheet(isPresented: Binding(get: { vm.versionsOf != nil }, set: { if !$0 { vm.versionsOf = nil } })) {
            if let (row, versions) = vm.versionsOf {
                NavigationStack {
                    List {
                        Section(footer: Text("The file is in an upload-only folder, so each upload to this path kept the one before it.")) {
                            Button {
                                Task { await vm.openVersion(row, hash: "") }
                            } label: {
                                HStack {
                                    Text("Current").fontWeight(.semibold)
                                    Spacer()
                                    Text(formatBytes(row.size)).foregroundColor(.secondary)
                                }
                            }
                            if vm.versionsLoading {
                                ProgressView()
                            }
                            ForEach(versions, id: \.hash) { v in
                                Button {
                                    Task { await vm.openVersion(row, hash: v.hash) }
                                } label: {
                                    HStack {
                                        Text("Replaced \(v.hasModified ? v.modified.date.formatted(date: .abbreviated, time: .shortened) : "—")")
                                        Spacer()
                                        Text(formatBytes(v.size)).foregroundColor(.secondary)
                                    }
                                }
                            }
                        }
                    }
                    .navigationTitle("Versions of \(row.name)")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Done") { vm.versionsOf = nil } } }
                }
            }
        }
        .confirmationDialog(
            "Delete \(vm.selected.count) item\(vm.selected.count == 1 ? "" : "s")?",
            isPresented: $vm.confirmDeleteSelected,
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) { Task { await vm.deleteSelected() } }
            Button("Cancel", role: .cancel) {}
        }
    }

    private func formatBytes(_ n: Int32) -> String {
        let bytes = Double(n)
        if bytes >= Double(1 << 30) { return String(format: "%.1f GB", bytes / Double(1 << 30)) }
        if bytes >= Double(1 << 20) { return String(format: "%.1f MB", bytes / Double(1 << 20)) }
        if bytes >= Double(1 << 10) { return String(format: "%.1f KB", bytes / Double(1 << 10)) }
        return "\(n) B"
    }
}

/// Issue #72: the system Quick Look previewer - the same one Mail/Files use
/// for attachments/downloads - natively renders images, PDFs, Office docs,
/// plain text, audio and video, not just images.
///
/// A bare QLPreviewController (what this used to hand straight to .sheet)
/// has no Done button of its own and, for a PDF especially, its own
/// pan/scroll gesture wins over the sheet's swipe-to-dismiss often enough
/// that there was no way to close it at all (issue found right after #72
/// shipped) — it only gets a Done button automatically when it's the root
/// of a UINavigationController *and* that stack is what's presented
/// modally, which a bare .sheet { QLPreviewController() } doesn't set up.
/// Wrapping it here, plus an explicit Done item wired to onDismiss rather
/// than relying on that auto-detection alone, makes sure the button is
/// always there regardless.
private struct QuickLookView: UIViewControllerRepresentable {
    let url: URL
    let onDismiss: () -> Void

    func makeCoordinator() -> Coordinator { Coordinator(url: url, onDismiss: onDismiss) }

    func makeUIViewController(context: Context) -> UINavigationController {
        let preview = QLPreviewController()
        preview.dataSource = context.coordinator
        preview.navigationItem.leftBarButtonItem = UIBarButtonItem(
            barButtonSystemItem: .done,
            target: context.coordinator,
            action: #selector(Coordinator.dismissTapped)
        )
        return UINavigationController(rootViewController: preview)
    }

    func updateUIViewController(_ uiViewController: UINavigationController, context: Context) {}

    final class Coordinator: NSObject, QLPreviewControllerDataSource {
        let url: URL
        let onDismiss: () -> Void
        init(url: URL, onDismiss: @escaping () -> Void) {
            self.url = url
            self.onDismiss = onDismiss
        }
        func numberOfPreviewItems(in controller: QLPreviewController) -> Int { 1 }
        func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> QLPreviewItem {
            url as NSURL
        }
        @objc func dismissTapped() { onDismiss() }
    }
}

/// Thin wrapper around UIActivityViewController (system share sheet) — used
/// for both "download" (save to Files/Photos) and "share" of a fetched file.
struct ActivityView: UIViewControllerRepresentable {
    let items: [Any]
    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: items, applicationActivities: nil)
    }
    func updateUIViewController(_ uiViewController: UIActivityViewController, context: Context) {}
}
