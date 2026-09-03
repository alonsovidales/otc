//
//  NewPostPicker.swift
//  OffTheCloud
//
//  Issue #32: dedicated "compose a new post" screen opened from the Social
//  tab's "+" button. Deliberately NOT the Images tab's PhotoGalleryView
//  reused with bits hidden — no delete/share-link/download/full-screen
//  viewer here. Just: filter by tag, tap photos to pick them, write a
//  caption, hit Publish.

import SwiftUI

@MainActor
final class NewPostPickerVM: ObservableObject {
    struct Item: Identifiable, Hashable {
        let id: String
        let path: String
        var thumbData: Data?
    }

    private let ws = OTCConnection.shared

    @Published var tags: [String] = []
    @Published var chips: [String] = []
    @Published var queryInput: String = ""

    @Published var items: [Item] = []
    @Published var loading = false
    @Published var endReached = false
    private var token: String? = nil

    @Published var selected: Set<String> = []
    @Published var caption: String = ""
    @Published var publishing = false
    @Published var showAlert = false
    @Published var alertMessage = ""

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

    private func loadTags() async {
        guard let resp = try? await ws.request({ e in
            var req = Msg_ReqEnvelope()
            req.payload = .reqGetTags(.init())
            e = req
        }) else { return }
        if case .respTagsList(let tl) = resp.payload { tags = tl.tags }
    }

    func resetAndLoadFirstPage() async {
        loading = false
        endReached = false
        token = ""
        items = []
        selected.removeAll()
        await fetchPage(overrideToken: "")
    }

    func loadMoreIfNeeded(current item: Item?) async {
        guard let item, !loading, !endReached else { return }
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
                var req = Msg_ReqEnvelope()
                var sp = Msg_SearchPhotos()
                sp.tags = self.chips
                sp.token = overrideToken ?? self.token ?? ""
                req.payload = .reqSearchPhotos(sp)
                e = req
            }
            guard case .respListOfFiles(let lof) = resp.payload else { return }
            var newItems: [Item] = []
            for f in lof.files {
                newItems.append(Item(id: "\(f.path)#\(f.hash)#\(f.size)", path: f.path, thumbData: f.hasContent ? f.content : nil))
            }
            let existing = Set(items.map(\.id))
            let filtered = newItems.filter { !existing.contains($0.id) }
            if !filtered.isEmpty { items.append(contentsOf: filtered) }
            token = lof.token.isEmpty ? nil : lof.token
            endReached = (token == nil)
        } catch { /* ignore, matches the Images tab's own best-effort paging */ }
    }

    func toggleSelect(_ path: String) {
        if selected.contains(path) { selected.remove(path) }
        else { selected.insert(path) }
    }

    func publish(onPosted: @escaping () -> Void) {
        guard !selected.isEmpty, !publishing else { return }
        publishing = true
        Task {
            defer { publishing = false }
            do {
                let resp = try await ws.request { [self] e in
                    var req = Msg_ReqEnvelope()
                    var pub = Msg_NewSocialPublication()
                    pub.text = caption
                    pub.paths = Array(selected)
                    req.payload = .reqNewSocialPublication(pub)
                    e = req
                }
                if case .respNewSocial = resp.payload, !resp.error {
                    onPosted()
                } else {
                    alertMessage = resp.error ? "Could not publish: \(resp.errorMessage)" : "Could not publish"
                    showAlert = true
                }
            } catch {
                alertMessage = "Could not publish: \(error.localizedDescription)"
                showAlert = true
            }
        }
    }
}

struct NewPostPickerView: View {
    @StateObject private var vm = NewPostPickerVM()
    @Environment(\.dismiss) private var dismiss
    @State private var showSuggest = false
    let onPosted: () -> Void

    private let cols = Array(repeating: GridItem(.flexible(minimum: 100, maximum: 140), spacing: 8), count: 3)

    var body: some View {
        NavigationView {
            VStack(spacing: 0) {
                // Tag filter
                VStack(alignment: .leading, spacing: 6) {
                    if !vm.chips.isEmpty {
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
                            }.padding(.horizontal, 12)
                        }
                    }
                    HStack(spacing: 8) {
                        TextField("Filter by tag…", text: $vm.queryInput, onEditingChanged: { showSuggest = $0 }) {
                            acceptCurrentQuery()
                        }
                        .textFieldStyle(.roundedBorder)
                        Button("Search") { acceptCurrentQuery() }
                    }
                    .padding(.horizontal, 12)

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
                        .padding(.horizontal, 12)
                    }
                }
                .padding(.vertical, 8)
                .background(.ultraThinMaterial)

                // Grid — tap a tile to select/deselect. No viewer, no
                // long-press, no delete/share affordances of any kind.
                ScrollView {
                    LazyVGrid(columns: cols, spacing: 8) {
                        ForEach(vm.items) { item in
                            PickTile(item: item, isSelected: vm.selected.contains(item.path)) {
                                vm.toggleSelect(item.path)
                            }
                            .task { await vm.loadMoreIfNeeded(current: item) }
                        }
                        if vm.loading {
                            ProgressView().frame(height: 60).gridCellColumns(cols.count)
                        }
                    }
                    .padding(10)
                }

                // Single composer bar: caption + Publish. That's it.
                HStack(spacing: 8) {
                    TextField("Write a caption…", text: $vm.caption)
                        .textFieldStyle(.roundedBorder)
                    Button(vm.publishing ? "Publishing…" : "Publish\(vm.selected.isEmpty ? "" : " (\(vm.selected.count))")") {
                        vm.publish {
                            dismiss()
                            onPosted()
                        }
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(vm.selected.isEmpty || vm.publishing)
                }
                .padding(10)
                .background(.ultraThinMaterial)
                .overlay(Divider(), alignment: .top)
            }
            .navigationTitle("New Post")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
        .onAppear { vm.onAppearInitial() }
        .alert(vm.alertMessage, isPresented: $vm.showAlert) { Button("OK", role: .cancel) {} }
    }

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
}

private struct PickTile: View {
    let item: NewPostPickerVM.Item
    let isSelected: Bool
    let onTap: () -> Void

    var body: some View {
        ZStack(alignment: .topTrailing) {
            Group {
                if let d = item.thumbData, let img = UIImage(data: d) {
                    Image(uiImage: img).resizable().scaledToFill()
                } else {
                    Color.gray.opacity(0.2)
                }
            }
            .frame(maxWidth: 120, maxHeight: 120)
            .aspectRatio(1, contentMode: .fill)
            .clipShape(RoundedRectangle(cornerRadius: 8))
            .overlay(
                RoundedRectangle(cornerRadius: 8)
                    .stroke(isSelected ? Color.accentColor : .clear, lineWidth: 3)
            )
            .opacity(isSelected ? 0.75 : 1)

            if isSelected {
                Image(systemName: "checkmark.circle.fill")
                    .foregroundColor(.accentColor)
                    .background(Color.white, in: Circle())
                    .padding(6)
            }
        }
        .contentShape(Rectangle())
        .onTapGesture { onTap() }
    }
}
