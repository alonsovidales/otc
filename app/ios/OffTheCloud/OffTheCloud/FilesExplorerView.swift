//
//  FilesExplorerView.swift
//  OffTheCloud
//
//  Native port of the web app's Files tab (web/src/components/FilesExplorer.tsx):
//  path navigation, upload (via the document picker — the native analogue of
//  the web's drag-and-drop), multi-select delete/share/download-zip, and an
//  image viewer for image files.

import SwiftUI
import UniformTypeIdentifiers

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
    @Published var viewer: (name: String, image: UIImage)?
    @Published var shareURL: URL?

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
            let newPath = row.path == ".." ? dirnamePath(path) + "/" : normPath(joinPath(path, row.name))
            navigate(to: newPath)
            return
        }

        let full = fullPath(for: row)
        var req = Msg_GetFile()
        req.path = full
        do {
            let resp = try await ws.request { $0.payload = .reqGetFile(req) }
            guard case .respFile(let f) = resp.payload else {
                showToast("Could not fetch file")
                return
            }
            if isImgFile(row.raw), let img = UIImage(data: f.content) {
                viewer = (leafName(row.path), img)
            } else {
                // Non-image: hand off to the system share sheet (save/open/etc).
                let tmp = FileManager.default.temporaryDirectory.appendingPathComponent(leafName(row.path))
                try? f.content.write(to: tmp)
                shareURL = tmp
            }
        } catch {
            showToast("Download failed: \(error.localizedDescription)")
        }
    }

    func deleteSelected() async {
        for p in selected {
            var req = Msg_DelFile()
            req.path = p.contains("/") ? p : joinPath(path, p)
            _ = try? await ws.request { $0.payload = .reqDelFile(req) }
        }
        await load()
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
        var req = Msg_UploadFile()
        req.path = joinPath(path, filename)
        req.content = data
        req.forceOverride = false
        do {
            let resp = try await ws.request { $0.payload = .reqUploadFile(req) }
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
    @State private var editMode: EditMode = .inactive

    init(initialPath: String) {
        _vm = StateObject(wrappedValue: FilesExplorerViewModel(initialPath: initialPath))
        _pathField = State(initialValue: initialPath)
    }

    var body: some View {
        NavigationView {
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

                List(selection: $vm.selected) {
                    ForEach(vm.rows) { row in
                        HStack {
                            Image(systemName: row.isDir ? "folder.fill" : (isImgFile(row.raw) ? "photo" : "doc"))
                                .foregroundColor(row.isDir ? .accentColor : .secondary)
                            VStack(alignment: .leading) {
                                Text(row.name).lineLimit(1)
                                if !row.isDir {
                                    Text(formatBytes(row.size)).font(.caption2).foregroundColor(.secondary)
                                }
                            }
                            Spacer()
                        }
                        .contentShape(Rectangle())
                        .onTapGesture {
                            if editMode == .inactive { Task { await vm.open(row) } }
                        }
                        .tag(row.path)
                    }
                }
                .listStyle(.plain)

                if !vm.selected.isEmpty {
                    HStack {
                        Text("\(vm.selected.count) selected").font(.caption)
                        Spacer()
                        Button(role: .destructive) { Task { await vm.deleteSelected() } } label: {
                            Image(systemName: "trash")
                        }
                        Button { Task { if let link = await vm.shareLink() { UIPasteboard.general.string = link; vm.showToast("Link copied") } } } label: {
                            Image(systemName: "link")
                        }
                        Button { Task { if let link = await vm.shareLink(), let url = URL(string: link) { vm.shareURL = url } } } label: {
                            Image(systemName: "square.and.arrow.up")
                        }
                    }
                    .padding()
                    .background(.ultraThinMaterial)
                }
            }
            .navigationTitle("Files")
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button { showImporter = true } label: { Image(systemName: "square.and.arrow.down.on.square") }
                }
                ToolbarItem(placement: .navigationBarLeading) {
                    EditButton()
                }
            }
            .overlay(alignment: .top) {
                if let toast = vm.toast {
                    Text(toast)
                        .padding(.horizontal, 12).padding(.vertical, 8)
                        .background(.ultraThinMaterial, in: Capsule())
                        .padding(.top, 8)
                }
            }
            .environment(\.editMode, $editMode)
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
        .sheet(isPresented: Binding(get: { vm.viewer != nil }, set: { if !$0 { vm.viewer = nil } })) {
            if let v = vm.viewer {
                ImageQuickLook(name: v.name, image: v.image)
            }
        }
        .sheet(isPresented: Binding(get: { vm.shareURL != nil }, set: { if !$0 { vm.shareURL = nil } })) {
            if let url = vm.shareURL {
                ActivityView(items: [url])
            }
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

private struct ImageQuickLook: View {
    let name: String
    let image: UIImage
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationView {
            Image(uiImage: image).resizable().scaledToFit()
                .navigationTitle(name)
                .toolbar {
                    ToolbarItem(placement: .navigationBarTrailing) {
                        Button("Done") { dismiss() }
                    }
                }
        }
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
