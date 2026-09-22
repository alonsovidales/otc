// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  NewPostPicker.swift
//  OffTheCloud
//
//  Issue #32: dedicated "compose a new post" screen opened from the Social
//  tab's "+" button. Deliberately NOT the Images tab's PhotoGalleryView
//  reused with bits hidden — no delete/share-link/download/full-screen
//  viewer here. Just: filter by tag, tap photos to pick them, write a
//  caption, hit Publish.
//
//  Issue #49: photos not yet synced by PhotoSync (a huge library's initial
//  sync can take a long time — see issue #58's investigation) used to be
//  completely unavailable here, since this only ever searched the
//  *device's* already-uploaded photos. A Phone/Synced toggle switches the
//  grid's source; anything picked from "Phone" gets uploaded (or linked,
//  per #58's dedup) at publish time, right before the post itself is
//  created — the same one-time cost PhotoSync would have paid anyway, just
//  moved to the moment the user actually wants to post it instead of
//  waiting on the background sync queue.

import SwiftUI
import AVFoundation
import Photos
import CryptoKit
import SwiftProtobuf

@MainActor
final class NewPostPickerVM: ObservableObject {
    enum Source { case phone, synced }

    struct Item: Identifiable, Hashable {
        let id: String
        // Empty for a Source.phone item until publish() uploads/links it
        // and fills in the real server path.
        var path: String
        // Set for Source.synced items, which arrive as raw bytes from the
        // server - nil for Source.phone items, which use thumbImage
        // instead (see its doc comment for why).
        var thumbData: Data?
        // Set only for Source.phone items - the thumbnail PHImageManager
        // already handed back as a UIImage, kept as one rather than
        // round-tripped through jpegData purely to fit thumbData's type:
        // it's only ever displayed locally, never transmitted (the full
        // asset is read and sent separately at publish time), so
        // re-encoding it bought nothing but a quality loss and a
        // "trying to save an opaque image with AlphaLast" warning from
        // ImageIO for no reason.
        var thumbImage: UIImage?
        // Set only for Source.phone items - nil for anything already on
        // the device (Source.synced).
        var asset: PHAsset?
        // Issue #60: drives PickTile/SelectedThumb's play badge - derived
        // from the server File's mime for Source.synced, or the PHAsset's
        // own mediaType for Source.phone (both fetches already include
        // videos with no restriction, so this is purely cosmetic, not a
        // filter).
        var isVideo: Bool = false

        // UIImage/PHAsset aren't Hashable, and don't need to be: id alone
        // already uniquely identifies an item.
        static func == (lhs: Item, rhs: Item) -> Bool { lhs.id == rhs.id }
        func hash(into hasher: inout Hasher) { hasher.combine(id) }
    }

    private let ws = OTCConnection.shared
    private static let localPageSize = 60

    @Published var source: Source = .phone

    @Published var tags: [String] = []
    @Published var chips: [String] = []
    @Published var queryInput: String = ""

    @Published var items: [Item] = []
    @Published var loading = false
    @Published var endReached = false
    private var token: String? = nil
    private var localAssets: PHFetchResult<PHAsset>?
    private var localLoadedCount = 0
    // SwiftUI can call .onAppear more than once for the same view instance
    // (e.g. across a NavigationView push/pop) - guards against a second,
    // redundant onAppearInitial() racing the first one's still-in-flight
    // resetAndLoadFirstPage() and wiping out its results.
    private var didAppear = false

    // Issue #48: an ordered list, not a Set — publish order matches
    // selection order (last tapped = last in the post), and can be
    // explicitly rearranged via moveSelected below.
    @Published var selectedOrder: [String] = []
    // Issue #108: keyed by Item.id, because a phone pick has no server
    // path until it's uploaded at publish time - both are resolved back to
    // real paths in publish() below.
    @Published var trims: [String: TrimRange] = [:]
    // The item the trimmer sheet is open on, paired with a local file URL
    // to preview.
    @Published var trimming: (id: String, url: URL)?
    @Published var trimLoadingId: String?
    /// Preview files already fetched/resolved for trimming, so reopening
    /// the trimmer on the same clip doesn't pull it down again. Temporary
    /// files, cleaned up when the composer closes.
    private var trimPreviewURLs: [String: URL] = [:]

    /// Removes the temp files downloadForTrimming wrote. A PHAsset's own
    /// URL belongs to the photo library and is deliberately left alone.
    func cleanUpTrimPreviews() {
        for url in trimPreviewURLs.values where url.path.contains("otc-trim-") {
            try? FileManager.default.removeItem(at: url)
        }
        trimPreviewURLs.removeAll()
    }
    @Published var caption: String = ""
    @Published var publishing = false
    @Published var publishStatus: String = ""
    @Published var showAlert = false
    @Published var alertMessage = ""

    func onAppearInitial() {
        guard !didAppear else { return }
        didAppear = true
        // These are independent: loadTags is only ever needed for
        // Source.synced's tag filter, so a slow (or, if the WS connection
        // hasn't finished authenticating yet, briefly failing-and-retried)
        // GetTags round trip used to delay the *Phone* page's first load
        // for no reason, despite Phone needing no network call at all.
        Task { await loadTags() }
        Task { await resetAndLoadFirstPage() }
    }

    func switchSource(_ s: Source) {
        guard source != s else { return }
        source = s
        Task { await resetAndLoadFirstPage() }
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
        items = []
        selectedOrder.removeAll()
        switch source {
        case .synced:
            token = ""
            await fetchPage(overrideToken: "")
        case .phone:
            localAssets = nil
            localLoadedCount = 0
            await loadLocalPage()
        }
    }

    func loadMoreIfNeeded(current item: Item?) async {
        guard let item, !loading, !endReached else { return }
        if let idx = items.firstIndex(of: item), idx >= items.count - 12 {
            switch source {
            case .synced: await fetchPage()
            case .phone: await loadLocalPage()
            }
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
                // Issue #60: the composer offers videos alongside photos,
                // unlike the Photo Gallery's own search.
                sp.includeVideos = true
                req.payload = .reqSearchPhotos(sp)
                e = req
            }
            guard case .respListOfFiles(let lof) = resp.payload else { return }
            var newItems: [Item] = []
            for f in lof.files {
                newItems.append(Item(id: "\(f.path)#\(f.hash)#\(f.size)", path: f.path, thumbData: f.hasContent ? f.content : nil, isVideo: f.mime.hasPrefix("video/")))
            }
            let existing = Set(items.map(\.id))
            let filtered = newItems.filter { !existing.contains($0.id) }
            if !filtered.isEmpty { items.append(contentsOf: filtered) }
            token = lof.token.isEmpty ? nil : lof.token
            endReached = (token == nil)
        } catch { /* ignore, matches the Images tab's own best-effort paging */ }
    }

    // MARK: - Phone source (issue #49)

    private func loadLocalPage() async {
        guard !loading, !endReached else {
            print("[phonepick] loadLocalPage skipped: loading=\(loading) endReached=\(endReached)")
            return
        }
        loading = true
        defer { loading = false }

        let fetchResult: PHFetchResult<PHAsset>
        if let existing = localAssets {
            fetchResult = existing
        } else {
            let status = PHPhotoLibrary.authorizationStatus(for: .readWrite)
            print("[phonepick] authorization status = \(status.rawValue)")
            if status != .authorized && status != .limited {
                let granted = await PHPhotoLibrary.requestAuthorization(for: .readWrite)
                print("[phonepick] requested authorization, got = \(granted.rawValue)")
                guard granted == .authorized || granted == .limited else {
                    alertMessage = "Photos access is needed to pick from your phone."
                    showAlert = true
                    endReached = true
                    return
                }
            }
            let opts = PHFetchOptions()
            // Newest first - the natural order for a picker, unlike
            // PhotoSync's own ascending order (which exists purely to
            // advance its own watermark correctly).
            opts.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: false)]
            let result = PHAsset.fetchAssets(with: opts)
            print("[phonepick] fetched \(result.count) local assets")
            localAssets = result
            fetchResult = result
        }

        let total = fetchResult.count
        guard localLoadedCount < total else {
            print("[phonepick] nothing left to load: loaded=\(localLoadedCount) total=\(total)")
            endReached = true
            return
        }
        let end = min(localLoadedCount + Self.localPageSize, total)

        let manager = PHImageManager.default()
        let imgOpts = PHImageRequestOptions()
        // .fastFormat was the actual cause of "the thumbnails suck" -
        // it's documented to return whatever the *fastest* available
        // cached representation is (a small system icon), capped well
        // below whatever targetSize asks for, no matter how large that is
        // - bumping targetSize alone (previous attempt) couldn't fix
        // that. .highQualityFormat asks for the best available quality
        // for targetSize instead, while still only calling the result
        // handler once (unlike .opportunistic, which calls it twice and
        // would violate withCheckedContinuation's single-resume
        // contract below).
        imgOpts.deliveryMode = .highQualityFormat
        // Thumbnails only, never triggers the iCloud full-original
        // download issue #58 dug into - isNetworkAccessAllowed=false
        // uses whatever's already cached locally (a proper preview, not
        // just the tiny fast-format icon) even for an "Optimize Storage"
        // library, regardless of deliveryMode.
        imgOpts.isNetworkAccessAllowed = false
        imgOpts.isSynchronous = false
        imgOpts.resizeMode = .exact

        // Sized for a @3x device showing this grid's ~140pt-wide tiles
        // (140 * 3 = 420) with some headroom — 240 was visibly soft.
        let targetSize = CGSize(width: 450, height: 450)

        var newItems: [Item] = []
        for i in localLoadedCount..<end {
            let asset = fetchResult.object(at: i)
            let thumb: UIImage? = await withCheckedContinuation { cont in
                manager.requestImage(
                    for: asset,
                    targetSize: targetSize,
                    contentMode: .aspectFill,
                    options: imgOpts
                ) { image, _ in
                    cont.resume(returning: image)
                }
            }
            newItems.append(Item(id: "local#\(asset.localIdentifier)", path: "", thumbImage: thumb, asset: asset, isVideo: asset.mediaType == .video))
        }
        items.append(contentsOf: newItems)
        localLoadedCount = end
        endReached = (localLoadedCount >= total)
        print("[phonepick] appended \(newItems.count) items, loaded=\(localLoadedCount)/\(total), items.count now = \(items.count)")
    }

    func toggleSelect(_ id: String) {
        if let idx = selectedOrder.firstIndex(of: id) {
            selectedOrder.remove(at: idx)
        } else {
            selectedOrder.append(id)
        }
    }

    /// Issue #48: explicit reordering, not just re-tapping to change
    /// position — used by the "Selected" strip's move buttons.
    func moveSelected(id: String, offset: Int) {
        guard let idx = selectedOrder.firstIndex(of: id) else { return }
        let newIdx = idx + offset
        guard selectedOrder.indices.contains(newIdx) else { return }
        selectedOrder.swapAt(idx, newIdx)
    }

    // Issue #108: trimming needs the real video, not the thumbnail the
    // strip shows. A phone pick is already on this device (PHImageManager
    // hands back a file URL, fetching from iCloud if that's where it
    // lives); a synced one has to come down from the device first, which
    // is why the strip shows a spinner on the button rather than looking
    // inert for a few seconds.
    func openTrimmer(for item: Item) {
        if let cached = trimPreviewURLs[item.id] {
            trimming = (item.id, cached)
            return
        }
        trimLoadingId = item.id
        Task {
            defer { trimLoadingId = nil }
            do {
                let url: URL
                if let asset = item.asset {
                    url = try await Self.localURL(for: asset)
                } else {
                    url = try await Self.downloadForTrimming(path: item.path)
                }
                trimPreviewURLs[item.id] = url
                trimming = (item.id, url)
            } catch {
                alertMessage = "Could not load that video to trim it: \(error.localizedDescription)"
                showAlert = true
            }
        }
    }

    func applyTrim(_ range: TrimRange?) {
        guard let trimming else { return }
        if let range { trims[trimming.id] = range } else { trims.removeValue(forKey: trimming.id) }
        self.trimming = nil
    }

    private static func localURL(for asset: PHAsset) async throws -> URL {
        let opts = PHVideoRequestOptions()
        // Same reasoning as uploadIfNeeded's own allowNetwork: this is
        // someone explicitly waiting on *this* video right now, not a
        // background bulk sync.
        opts.isNetworkAccessAllowed = true
        opts.deliveryMode = .highQualityFormat
        return try await withCheckedThrowingContinuation { cont in
            PHImageManager.default().requestAVAsset(forVideo: asset, options: opts) { avAsset, _, info in
                if let urlAsset = avAsset as? AVURLAsset {
                    cont.resume(returning: urlAsset.url)
                } else {
                    let err = info?[PHImageErrorKey] as? Error
                        ?? NSError(domain: "NewPostPicker", code: -3, userInfo: [NSLocalizedDescriptionKey: "This video is not available on the phone"])
                    cont.resume(throwing: err)
                }
            }
        }
    }

    private static func downloadForTrimming(path: String) async throws -> URL {
        let resp = try await OTCConnection.shared.request { e in
            var gf = Msg_GetFile()
            gf.path = path
            e.payload = .reqGetFile(gf)
        }
        guard case .respFile(let f) = resp.payload, !f.content.isEmpty else {
            throw NSError(domain: "NewPostPicker", code: -4, userInfo: [NSLocalizedDescriptionKey: resp.error ? resp.errorMessage : "Empty response"])
        }
        // AVURLAsset picks its demuxer from the extension, so a temp file
        // without one plays as nothing at all.
        let ext = (path as NSString).pathExtension.isEmpty ? "mp4" : (path as NSString).pathExtension
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("otc-trim-\(UUID().uuidString)")
            .appendingPathExtension(ext)
        try f.content.write(to: url)
        return url
    }

    func publish(onPosted: @escaping () -> Void) {
        // Issue #96: a post always needs its own caption - the Publish
        // button below already disables on this same check, this just
        // guards the actual publish call too rather than trusting the
        // button's disabled state alone (same defense-in-depth as the
        // selectedOrder check right next to it).
        guard !selectedOrder.isEmpty, !caption.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, !publishing else { return }
        publishing = true
        Task {
            defer { publishing = false; publishStatus = "" }
            do {
                // Issue #48: publish in selectedOrder's order, not
                // items' own display order.
                let byId = Dictionary(uniqueKeysWithValues: items.map { ($0.id, $0) })
                let selectedItems = selectedOrder.compactMap { byId[$0] }
                var resolvedPaths: [String] = []
                var uploadedCount = 0
                for item in selectedItems {
                    if let asset = item.asset {
                        uploadedCount += 1
                        publishStatus = "Uploading \(uploadedCount) of \(selectedItems.filter { $0.asset != nil }.count)…"
                        resolvedPaths.append(try await Self.uploadIfNeeded(asset))
                    } else {
                        resolvedPaths.append(item.path)
                    }
                }
                publishStatus = "Publishing…"

                // Issue #108: the trims are keyed by Item.id; the device
                // only knows paths. resolvedPaths is index-aligned with
                // selectedItems, which is what pairs the two back up -
                // including for a phone pick, whose path only came into
                // existence during the upload loop just above.
                var pbTrims: [Msg_VideoTrim] = []
                for (i, item) in selectedItems.enumerated() {
                    guard let range = trims[item.id], i < resolvedPaths.count else { continue }
                    var t = Msg_VideoTrim()
                    t.path = resolvedPaths[i]
                    t.startSecs = range.start
                    t.endSecs = range.end
                    pbTrims.append(t)
                }

                let resp = try await ws.request { [self] e in
                    var req = Msg_ReqEnvelope()
                    var pub = Msg_NewSocialPublication()
                    pub.text = caption
                    pub.paths = resolvedPaths
                    pub.trims = pbTrims
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

    /// Uploads (or, per issue #58, links) a Source.phone asset the user
    /// picked, returning the server path it ends up at. Uses the exact
    /// same target-path convention PhotoSync itself uses
    /// ("/ios/<deviceId>/<filename>") so a photo posted here and later
    /// reached by PhotoSync's own background sync are recognized as the
    /// same file rather than uploaded twice.
    private static func uploadIfNeeded(_ asset: PHAsset) async throws -> String {
        let ws = OTCConnection.shared
        let secrets = SecretsStore.loadOrCreate()
        guard let rawName = PhotoSync.shared.resourceFilename(for: asset) else {
            throw NSError(domain: "NewPostPicker", code: -1, userInfo: [NSLocalizedDescriptionKey: "Could not read this photo"])
        }
        let cleanName = rawName.replacingOccurrences(of: "/", with: "_")
        let path = "/ios/\(secrets.deviceId)/\(cleanName)"
        let created = Google_Protobuf_Timestamp(date: asset.creationDate ?? Date())

        // Cache hit (already synced under some other path before, e.g. a
        // prior install) - skip the download+hash entirely, same as
        // PhotoSync's own dedup fast path.
        if let cachedHash = AssetSyncCache.shared.hash(for: asset.localIdentifier) {
            let hasResp = try await ws.request { e in
                var hf = Msg_HasFile()
                hf.hash = cachedHash
                e.payload = .reqHasFile(hf)
            }
            if case .respFileExists(let fe) = hasResp.payload, fe.exists {
                let resp = try await ws.request { e in
                    var lf = Msg_LinkFile()
                    lf.hash = cachedHash
                    lf.path = path
                    lf.forceOverride = false
                    lf.created = created
                    e.payload = .reqLinkFile(lf)
                }
                if case .respFile = resp.payload { return path }
                if resp.error { throw NSError(domain: "NewPostPicker", code: -2, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage]) }
            }
        }

        // This is the user explicitly waiting on posting *this* photo
        // right now, not a background bulk sync - always allow the iCloud
        // fetch if needed, regardless of the "Sync from iCloud" setting
        // PhotoSync itself respects.
        let (data, _, _) = try PhotoSync.shared.readData(for: asset, allowNetwork: true)
        let hash = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()

        let hasResp = try await ws.request { e in
            var hf = Msg_HasFile()
            hf.hash = hash
            e.payload = .reqHasFile(hf)
        }
        let alreadyOnDevice: Bool
        if case .respFileExists(let fe) = hasResp.payload { alreadyOnDevice = fe.exists } else { alreadyOnDevice = false }

        let resp: Msg_RespEnvelope
        if alreadyOnDevice {
            resp = try await ws.request { e in
                var lf = Msg_LinkFile()
                lf.hash = hash
                lf.path = path
                lf.forceOverride = false
                lf.created = created
                e.payload = .reqLinkFile(lf)
            }
        } else {
            resp = try await ws.request { e in
                var up = Msg_UploadFile()
                up.path = path
                up.content = data
                up.forceOverride = false
                up.created = created
                e.payload = .reqUploadFile(up)
            }
        }
        if case .respFile = resp.payload {
            AssetSyncCache.shared.record(localIdentifier: asset.localIdentifier, hash: hash)
            return path
        }
        throw NSError(domain: "NewPostPicker", code: -3, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "Upload failed" : resp.errorMessage])
    }
}

struct NewPostPickerView: View {
    @StateObject private var vm = NewPostPickerVM()
    @Environment(\.dismiss) private var dismiss
    @State private var showSuggest = false
    let onPosted: () -> Void

    private let cols = Array(repeating: GridItem(.flexible(minimum: 100, maximum: 140), spacing: 8), count: 3)

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                // Issue #49: Phone shows the camera roll directly (fast
                // thumbnails, no iCloud download - see loadLocalPage's doc
                // comment), including anything PhotoSync hasn't gotten to
                // yet; Synced is the original tag-searchable behavior,
                // scoped to what's already on the device.
                Picker("Source", selection: Binding(
                    get: { vm.source },
                    set: { vm.switchSource($0) }
                )) {
                    Text("Phone").tag(NewPostPickerVM.Source.phone)
                    Text("Synced").tag(NewPostPickerVM.Source.synced)
                }
                .pickerStyle(.segmented)
                .padding(.horizontal, 12)
                .padding(.top, 8)

                // Tag filter — only meaningful once a photo has actually
                // been tagged server-side, which only happens after it's
                // synced, so this has nothing to search for Phone.
                if vm.source == .synced {
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
                }

                // Grid — tap a tile to select/deselect. No viewer, no
                // long-press, no delete/share affordances of any kind.
                // Issue #48: a numbered badge instead of a plain
                // checkmark shows the tile's position in the post.
                ScrollView {
                    LazyVGrid(columns: cols, spacing: 8) {
                        ForEach(vm.items) { item in
                            PickTile(
                                item: item,
                                selectionNumber: vm.selectedOrder.firstIndex(of: item.id).map { $0 + 1 }
                            ) {
                                vm.toggleSelect(item.id)
                            }
                            .task { await vm.loadMoreIfNeeded(current: item) }
                        }
                        if vm.loading {
                            ProgressView().frame(height: 60).gridCellColumns(cols.count)
                        }
                    }
                    .padding(10)
                }

                // Issue #48: the order photos appear in the grid isn't
                // necessarily the order they should appear in the post
                // (in Phone mode especially, that's just newest-first) -
                // this strip shows the actual post order and lets it be
                // fixed up directly, rather than requiring
                // deselect-then-reselect-everything-in-the-right-order.
                if !vm.selectedOrder.isEmpty {
                    SelectedOrderStrip(vm: vm)
                }

                // Issue #49: publishing a Phone selection uploads it
                // first (see NewPostPickerVM.uploadIfNeeded) - can take a
                // moment for a large photo, so this says so instead of
                // just sitting on a static "Publishing…" the whole time.
                if vm.publishing, !vm.publishStatus.isEmpty {
                    Text(vm.publishStatus)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .padding(.top, 4)
                }

                // Single composer bar: caption + Publish. That's it.
                HStack(spacing: 8) {
                    TextField("Write a caption…", text: $vm.caption)
                        .textFieldStyle(.roundedBorder)
                    Button(vm.publishing ? "Publishing…" : "Publish\(vm.selectedOrder.isEmpty ? "" : " (\(vm.selectedOrder.count))")") {
                        vm.publish {
                            dismiss()
                            onPosted()
                        }
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(vm.selectedOrder.isEmpty || vm.caption.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || vm.publishing)
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
        .onDisappear { vm.cleanUpTrimPreviews() }
        .alert(vm.alertMessage, isPresented: $vm.showAlert) { Button("OK", role: .cancel) {} }
        // Issue #108. Bound to an optional identified by the item's own id,
        // so reopening the trimmer on a different clip rebuilds the sheet
        // rather than reusing the previous video's player.
        .sheet(isPresented: Binding(
            get: { vm.trimming != nil },
            set: { if !$0 { vm.trimming = nil } }
        )) {
            if let trimming = vm.trimming {
                VideoTrimmerView(
                    url: trimming.url,
                    existing: vm.trims[trimming.id],
                    onApply: { vm.applyTrim($0) },
                    onCancel: { vm.trimming = nil }
                )
                .id(trimming.id)
            }
        }
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
    // Issue #48: 1-based position in the post, nil when not selected -
    // replaces a plain checkmark so the grid itself shows the current
    // order, not just membership.
    let selectionNumber: Int?
    let onTap: () -> Void

    private var isSelected: Bool { selectionNumber != nil }

    var body: some View {
        ZStack(alignment: .topTrailing) {
            Group {
                if let img = item.thumbImage {
                    Image(uiImage: img).resizable().scaledToFill()
                } else if let d = item.thumbData, let img = UIImage(data: d) {
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
            .overlay(alignment: .bottomLeading) {
                // Issue #60: marks a video tile - bottom-leading so it
                // never collides with the selection number (top-trailing).
                if item.isVideo {
                    Image(systemName: "play.fill")
                        .font(.caption2)
                        .foregroundColor(.white)
                        .frame(width: 20, height: 20)
                        .background(Color.black.opacity(0.55), in: Circle())
                        .padding(6)
                }
            }

            if let selectionNumber {
                Text("\(selectionNumber)")
                    .font(.caption.bold())
                    .foregroundColor(.white)
                    .frame(width: 22, height: 22)
                    .background(Color.accentColor, in: Circle())
                    .padding(6)
            }
        }
        .contentShape(Rectangle())
        .onTapGesture { onTap() }
    }
}

// Issue #48: the actual reordering UI — small thumbnails in post order,
// with </> to move one position and × to deselect. Kept separate from the
// main grid/PickTile since dragging in a LazyVGrid (dozens/hundreds of
// tiles, several sized differently by source) is a lot more fragile to
// get right than dragging a short, purpose-built strip of only the
// already-selected items.
private struct SelectedOrderStrip: View {
    @ObservedObject var vm: NewPostPickerVM

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Order in post").font(.caption).foregroundStyle(.secondary).padding(.horizontal, 12)
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ForEach(Array(vm.selectedOrder.enumerated()), id: \.element) { index, id in
                        if let item = vm.items.first(where: { $0.id == id }) {
                            SelectedThumb(
                                item: item,
                                position: index + 1,
                                canMoveLeft: index > 0,
                                canMoveRight: index < vm.selectedOrder.count - 1,
                                moveLeft: { vm.moveSelected(id: id, offset: -1) },
                                moveRight: { vm.moveSelected(id: id, offset: 1) },
                                remove: { vm.toggleSelect(id) },
                                trim: item.isVideo ? { vm.openTrimmer(for: item) } : nil,
                                trimRange: vm.trims[id],
                                trimLoading: vm.trimLoadingId == id
                            )
                        }
                    }
                }
                .padding(.horizontal, 12)
            }
        }
        .padding(.vertical, 6)
        .background(.ultraThinMaterial)
        .overlay(Divider(), alignment: .top)
    }
}

private struct SelectedThumb: View {
    let item: NewPostPickerVM.Item
    let position: Int
    let canMoveLeft: Bool
    let canMoveRight: Bool
    let moveLeft: () -> Void
    let moveRight: () -> Void
    let remove: () -> Void
    // Issue #108: nil for anything that isn't a video.
    let trim: (() -> Void)?
    let trimRange: TrimRange?
    let trimLoading: Bool

    var body: some View {
        VStack(spacing: 2) {
            ZStack(alignment: .topTrailing) {
                Group {
                    if let img = item.thumbImage {
                        Image(uiImage: img).resizable().scaledToFill()
                    } else if let d = item.thumbData, let img = UIImage(data: d) {
                        Image(uiImage: img).resizable().scaledToFill()
                    } else {
                        Color.gray.opacity(0.2)
                    }
                }
                .frame(width: 60, height: 60)
                .clipShape(RoundedRectangle(cornerRadius: 6))
                // Issue #111: the whole thumbnail opens the trimmer, not
                // just the little pill in its corner - that pill was a
                // ~20x14pt target on a 60pt tile, well under anything you
                // can reliably hit. There is nothing else to tap a
                // selected video for, and the trimmer is also where you
                // can watch it, so the tile itself is the obvious target.
                // The badge below stays as the affordance and as where
                // the chosen length is shown.
                .contentShape(RoundedRectangle(cornerRadius: 6))
                .onTapGesture { if !trimLoading { trim?() } }

                Button(action: remove) {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.white, .black.opacity(0.6))
                }
                .offset(x: 6, y: -6)

                // Issue #108: shows the length that survives once a trim
                // is set, so the strip carries the decision without
                // having to reopen the editor.
                // Issue #108: shows the length that survives once a trim
                // is set, so the strip carries the decision without
                // having to reopen the editor. No longer a button of its
                // own (issue #111) - the whole tile is the target now, so
                // this is purely what tells you the tile can be trimmed.
                if trim != nil {
                    VStack {
                        Spacer()
                        HStack {
                            Group {
                                if trimLoading {
                                    ProgressView().controlSize(.mini).tint(.white)
                                } else {
                                    Text(trimRange.map { "\u{2702} \(formatTimecode($0.length))" } ?? "\u{2702} Trim")
                                        .font(.system(size: 11, weight: .semibold))
                                        .foregroundStyle(trimRange == nil ? .white : Color.black)
                                }
                            }
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background(trimRange == nil ? AnyShapeStyle(.black.opacity(0.6)) : AnyShapeStyle(Color.accentColor), in: Capsule())
                            Spacer()
                        }
                    }
                    .frame(width: 60, height: 60)
                    .padding(2)
                    // The tile underneath handles the tap.
                    .allowsHitTesting(false)
                }
            }
            HStack(spacing: 2) {
                Button(action: moveLeft) {
                    Image(systemName: "chevron.left.circle.fill")
                }
                .disabled(!canMoveLeft)
                Text("\(position)").font(.caption2).monospacedDigit()
                Button(action: moveRight) {
                    Image(systemName: "chevron.right.circle.fill")
                }
                .disabled(!canMoveRight)
            }
            .font(.caption2)
            .foregroundStyle(.secondary)
        }
    }
}
