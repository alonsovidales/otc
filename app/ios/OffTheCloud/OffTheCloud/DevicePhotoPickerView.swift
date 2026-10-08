// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  DevicePhotoPickerView.swift
//  OffTheCloud
//
//  Issue #178: "Change photo" -> "From your device" in Settings > Profile.
//  Browses the photos already stored on the OTC device (optionally inside
//  one of its image groups) and hands the chosen one, fetched full size,
//  back as a ProfilePhotoCropItem so it goes through the same crop step
//  as a photo picked from the phone.
//

import SwiftUI
import UIKit

@MainActor
final class DevicePhotoPickerVM: ObservableObject {
    struct Item: Identifiable, Hashable {
        let id: String
        let path: String
        let thumbData: Data?
    }

    private let ws = OTCConnection.shared
    /// Three tiles across the screen.
    static var tileSide: CGFloat { UIScreen.main.bounds.width / 3 }

    @Published var groups: [Msg_ImageGroup] = []
    /// "" means "All photos" - same meaning SearchPhotos gives group_id.
    @Published var groupID = ""
    @Published var items: [Item] = []
    @Published var loading = false
    @Published var endReached = false
    /// Path of the photo whose full-size fetch is in flight (spinner).
    @Published var fetchingPath: String?
    @Published var errorText: String?

    private var token = ""
    /// Bumped on every reset so a page answered for the previous chip
    /// can't land in the new grid.
    private var generation = 0

    func loadGroups() async {
        guard let resp = try? await ws.request({ e in
            var req = Msg_ReqEnvelope()
            // Covers as the grids' tiles: small thumbnails (release 111).
            var lg = Msg_ListImageGroups()
            lg.smallThumbnails = true
            req.payload = .reqListImageGroups(lg)
            e = req
        }) else { return }
        if case .respImageGroups(let g) = resp.payload { groups = g.groups }
    }

    func select(group id: String) async {
        guard id != groupID else { return }
        groupID = id
        await reset()
    }

    func reset() async {
        generation += 1
        items = []
        token = ""
        endReached = false
        loading = false
        await fetchPage()
    }

    func loadMoreIfNeeded(current item: Item) async {
        guard !loading, !endReached,
              let idx = items.firstIndex(of: item), idx >= items.count - 12 else { return }
        await fetchPage()
    }

    private func fetchPage() async {
        guard !loading, !endReached else { return }
        loading = true
        let myGeneration = generation
        defer { if myGeneration == generation { loading = false } }
        let group = groupID
        let pageToken = token
        // Snapshot, with the token it goes with (see the request below).
        let have = Int32(items.count)
        do {
            let resp = try await ws.request { e in
                var req = Msg_ReqEnvelope()
                var sp = Msg_SearchPhotos()
                sp.token = pageToken
                // A new search (opening, or another chip) gets a small
                // first page so the grid paints quickly; scrolling on, the
                // full size (see cFirstPhotoPageLimit).
                if pageToken.isEmpty { sp.limit = cFirstPhotoPageLimit }
                sp.groupID = group
                // A profile photo can't be a video.
                sp.includeVideos = false
                // Where this grid is, should the device no longer hold the
                // token (SearchPhotos.have) - as Images and Android's
                // picker.
                sp.have = have
                // A grid: its tiles' small thumbnails (release 111; an
                // older device sends big ones).
                sp.smallThumbnails = true
                req.payload = .reqSearchPhotos(sp)
                e = req
            }
            guard myGeneration == generation,
                  case .respListOfFiles(let lof) = resp.payload else { return }
            let page = lof.files
                .filter { !$0.mime.hasPrefix("video/") }
                .map { Item(id: "\($0.path)#\($0.hash)#\($0.fileSize)", path: $0.path,
                            thumbData: $0.hasContent ? $0.content : nil) }
            // Decoded off the main thread, at tile size, before the tiles
            // first draw (see GridThumbCache).
            await GridThumbCache.prewarm(page.map { ($0.id, $0.thumbData) }, maxPt: Self.tileSide)
            guard myGeneration == generation else { return }
            let existing = Set(items.map(\.id))
            let fresh = page.filter { !existing.contains($0.id) }
            items.append(contentsOf: fresh)
            token = lof.token
            endReached = lof.token.isEmpty
        } catch {
            print("[DevicePhotoPicker] page fetch failed: \(error)")
        }
    }

    /// Fetches the full image (the device already converts HEIC to JPEG
    /// for GetFile) and turns it into a crop item; nil on failure, with
    /// errorText set.
    func fetchFull(_ item: Item) async -> ProfilePhotoCropItem? {
        guard fetchingPath == nil else { return nil }
        fetchingPath = item.path
        errorText = nil
        defer { fetchingPath = nil }
        do {
            let resp = try await ws.request { e in
                var req = Msg_ReqEnvelope()
                var gf = Msg_GetFile()
                gf.path = item.path
                req.payload = .reqGetFile(gf)
                e = req
            }
            guard case .respFile(let f) = resp.payload, !f.content.isEmpty else {
                errorText = resp.error && !resp.errorMessage.isEmpty
                    ? resp.errorMessage : "Couldn't load that photo from your device."
                return nil
            }
            guard let crop = ProfilePhotoCropItem(data: f.content) else {
                errorText = "That photo couldn't be opened."
                return nil
            }
            return crop
        } catch {
            errorText = "Couldn't load that photo: \(error.localizedDescription)"
            return nil
        }
    }
}

struct DevicePhotoPickerView: View {
    /// Called with the fetched photo; the caller dismisses this sheet and
    /// presents the crop.
    let onPick: (ProfilePhotoCropItem) -> Void
    @Environment(\.dismiss) private var dismiss
    @StateObject private var vm = DevicePhotoPickerVM()

    private let cols = Array(repeating: GridItem(.flexible(), spacing: 2), count: 3)

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                chips
                if let err = vm.errorText {
                    Text(err)
                        .font(.footnote)
                        .foregroundColor(.red)
                        .padding(.horizontal)
                        .padding(.bottom, 6)
                }
                ScrollView {
                    LazyVGrid(columns: cols, spacing: 2) {
                        ForEach(vm.items) { item in
                            tile(item)
                                .task { await vm.loadMoreIfNeeded(current: item) }
                        }
                        if vm.loading {
                            ProgressView().frame(height: 60).gridCellColumns(cols.count)
                        }
                    }
                    if !vm.loading && vm.items.isEmpty {
                        Text("No photos here.")
                            .font(.footnote)
                            .foregroundColor(.secondary)
                            .padding(.top, 40)
                    }
                }
            }
            .navigationTitle("Choose a photo")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
        .task {
            async let g: Void = vm.loadGroups()
            async let p: Void = vm.reset()
            _ = await (g, p)
        }
    }

    private var chips: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                chip("All photos", id: "")
                ForEach(vm.groups, id: \.id) { g in
                    chip(g.name, id: g.id)
                }
            }
            .padding(.horizontal)
            .padding(.vertical, 8)
        }
    }

    private func chip(_ title: String, id: String) -> some View {
        let on = vm.groupID == id
        return Button {
            Task { await vm.select(group: id) }
        } label: {
            Text(title)
                .font(.subheadline)
                .lineLimit(1)
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
                .background(on ? Color.accentColor : Color.secondary.opacity(0.15), in: Capsule())
                .foregroundColor(on ? .white : .primary)
        }
        .buttonStyle(.plain)
    }

    private func tile(_ item: DevicePhotoPickerVM.Item) -> some View {
        // Color.clear sized square, image overlaid and clipped, so an
        // aspect-fill thumbnail never widens its grid cell.
        Color.clear
            .aspectRatio(1, contentMode: .fit)
            .overlay {
                if let img = GridThumbCache.image(id: item.id, data: item.thumbData, maxPt: DevicePhotoPickerVM.tileSide) {
                    Image(uiImage: img).resizable().scaledToFill()
                } else {
                    Color.gray.opacity(0.2)
                }
            }
            .clipped()
            .overlay {
                if vm.fetchingPath == item.path {
                    ZStack {
                        Color.black.opacity(0.35)
                        ProgressView().tint(.white)
                    }
                }
            }
            .contentShape(Rectangle())
            .onTapGesture {
                guard vm.fetchingPath == nil else { return }
                Task {
                    if let crop = await vm.fetchFull(item) {
                        onPick(crop)
                        dismiss()
                    }
                }
            }
    }
}
