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
        // Set for Source.synced items only where nothing else keeps their
        // thumbnail (content the thumbnail cache refused): otherwise it is
        // read from the cache on this phone by `hash`, or asked for
        // (GridThumbLoader). nil for Source.phone items, whose thumbnail
        // is the UIImage PHImageManager hands back, kept in GridThumbCache
        // under the item's id (see PhoneThumb). Not held here: a 450 px
        // image per loaded phone photo added up to gigabytes over a long
        // scroll.
        var thumbData: Data?
        // Set only for Source.phone items - nil for anything already on
        // the device (Source.synced).
        var asset: PHAsset?
        // Issue #60: drives PickTile/SelectedThumb's play badge - derived
        // from the server File's mime for Source.synced, or the PHAsset's
        // own mediaType for Source.phone (both fetches already include
        // videos with no restriction, so this is purely cosmetic, not a
        // filter).
        var isVideo: Bool = false
        // A synced item's content hash: its thumbnail's key in the cache
        // on this phone (ThumbDiskCache). "" for a phone item.
        var hash: String = ""

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
    /// The Synced tiles: from the thumbnail cache on this phone, asked for
    /// (GetThumbnails) where it has none, as Images'.
    let thumbs: GridThumbLoader
    /// Bumped when tiles got their images: the grid draws them.
    @Published private(set) var thumbsVersion = 0
    private var token: String? = nil
    // Bumped when the grid starts over (source, tags), so a page still in
    // flight for the old one lands nowhere (as Android's searchGeneration).
    private var searchGeneration = 0
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

    init() {
        thumbs = GridThumbLoader(maxPt: PickTile.side, request: GridThumbLoader.deviceRequest)
        thumbs.onImages = { [weak self] in self?.thumbsVersion &+= 1 }
    }

    /// What the loader needs for a synced tile; nil for a phone item (or
    /// one with its own bytes).
    func thumbWant(_ item: Item) -> GridThumbLoader.Want? {
        guard item.asset == nil, item.thumbData == nil, !item.hash.isEmpty else { return nil }
        return GridThumbLoader.Want(id: item.id, path: item.path, hash: item.hash)
    }

    /// A synced tile came on screen (`index`): its thumbnail first, and
    /// the tiles around it read from the cache ahead of the scroll.
    func tileShown(_ item: Item) {
        guard item.asset == nil else { return }
        thumbs.appeared(item.path)
        guard let i = items.firstIndex(of: item), i % 6 == 0 else { return }
        thumbs.readAhead(items[max(0, i - 12)..<min(items.count, i + 30)].compactMap(thumbWant))
    }

    func tileHidden(_ item: Item) {
        guard item.asset == nil else { return }
        thumbs.disappeared(item.path)
    }

    /// A synced tile has no image: from the cache, else the device.
    func needThumb(_ item: Item) {
        guard let w = thumbWant(item) else { return }
        thumbs.need(w)
    }

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
        searchGeneration += 1
        thumbs.reset()
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
            // Its own task, not the asking tile's: with a 12-photo first
            // page that tile is the first one, and scrolling it away
            // cancelled the page, with no later tile left to ask again.
            let src = source
            Task {
                switch src {
                case .synced: await self.fetchPage()
                case .phone: await self.loadLocalPage()
                }
            }
        }
    }

    private func fetchPage(overrideToken: String? = nil) async {
        guard !loading, !endReached else { return }
        let mine = searchGeneration
        loading = true
        defer { if mine == searchGeneration { loading = false } }
        // The photos this grid holds, for `have` below: the device's own
        // count, not the phone's (a page asked for while on the phone's
        // source isn't this one's).
        let have = Int32(items.filter { $0.asset == nil }.count)
        let firstLimit = PhotoPageLimit.first(deviceOmits: thumbs.cache.deviceOmits)
        do {
            let resp = try await ws.request { e in
                var req = Msg_ReqEnvelope()
                var sp = Msg_SearchPhotos()
                sp.tags = self.chips
                sp.token = overrideToken ?? self.token ?? ""
                // Where this grid is, should the device no longer hold the
                // token (SearchPhotos.have), as Images does.
                sp.have = have
                // A grid: its tiles' small thumbnails (release 111; an
                // older device sends big ones) - without them (release
                // 113): the tiles come from the cache on this phone, and
                // what it lacks is asked for (GridThumbLoader). An older
                // device sends them anyway.
                sp.smallThumbnails = true
                sp.omitThumbnails = true
                // A new search (switching to Synced, a tag added or
                // removed) gets a first page that paints quickly;
                // scrolling on, a bigger one (PhotoPageLimit, as Images).
                sp.limit = sp.token.isEmpty ? firstLimit : PhotoPageLimit.next
                // Issue #60: the composer offers videos alongside photos,
                // unlike the Photo Gallery's own search.
                sp.includeVideos = true
                req.payload = .reqSearchPhotos(sp)
                e = req
            }
            guard mine == searchGeneration, case .respListOfFiles(let lof) = resp.payload else { return }
            let existing = Set(items.map(\.id))
            let fresh = lof.files.filter { !existing.contains("\($0.path)#\($0.hash)#\($0.fileSize)") }
            let ids = fresh.map { "\($0.path)#\($0.hash)#\($0.fileSize)" }
            // Content kept in the cache on this phone, the first tiles
            // decoded off the main thread before they draw (see
            // GridThumbCache), what the cache lacks asked for.
            let keep = await thumbs.takePage(fresh, ids: ids)
            guard mine == searchGeneration, source == .synced else { return }
            let filtered = fresh.enumerated().map { i, f in
                Item(id: ids[i], path: f.path, thumbData: keep.indices.contains(i) ? keep[i] : nil,
                     isVideo: f.mime.hasPrefix("video/"), hash: f.hash)
            }
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
        let mine = searchGeneration
        loading = true
        defer { if mine == searchGeneration { loading = false } }

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

        var newItems: [Item] = []
        for i in localLoadedCount..<end {
            let asset = fetchResult.object(at: i)
            let id = "local#\(asset.localIdentifier)"
            // No thumbnail fetched here: each tile loads its own when it
            // shows (loadPhoneTile). Fetching a page's 60 one after another
            // first - from iCloud for photos not kept on the phone - held
            // the whole page back for ages.
            newItems.append(Item(id: id, path: "", asset: asset, isVideo: asset.mediaType == .video))
        }
        guard mine == searchGeneration else { return }
        items.append(contentsOf: newItems)
        localLoadedCount = end
        endReached = (localLoadedCount >= total)
        print("[phonepick] appended \(newItems.count) items, loaded=\(localLoadedCount)/\(total), items.count now = \(items.count)")
    }

    /// Issue #150: a photo or video just taken with the camera. Saved to
    /// the phone's Photos first - where a shot from the Camera app would be
    /// too - so from here on it is an ordinary phone item: first in the
    /// list, already selected, uploaded by publish() like any other.
    func addCapture(image: UIImage?, videoURL: URL?) async {
        guard image != nil || videoURL != nil else { return } // cancelled
        var localId: String?
        do {
            try await PHPhotoLibrary.shared().performChanges {
                let request: PHAssetChangeRequest?
                if let image {
                    request = PHAssetChangeRequest.creationRequestForAsset(from: image)
                } else if let videoURL {
                    request = PHAssetChangeRequest.creationRequestForAssetFromVideo(atFileURL: videoURL)
                } else {
                    request = nil
                }
                localId = request?.placeholderForCreatedAsset?.localIdentifier
            }
        } catch {
            alertMessage = "Couldn't save the capture to Photos: \(error.localizedDescription)"
            showAlert = true
            return
        }
        guard let localId, let asset = PHAsset.fetchAssets(withLocalIdentifiers: [localId], options: nil).firstObject else {
            alertMessage = "The capture was saved to Photos but couldn't be read back."
            showAlert = true
            return
        }
        var thumb = image
        if thumb == nil, let videoURL {
            let gen = AVAssetImageGenerator(asset: AVURLAsset(url: videoURL))
            gen.appliesPreferredTrackTransform = true
            if let cg = try? await gen.image(at: .zero).image { thumb = UIImage(cgImage: cg) }
        }
        if source != .phone { switchSource(.phone) }
        let item = Item(id: "local#\(asset.localIdentifier)", path: "", asset: asset, isVideo: asset.mediaType == .video)
        // The capture itself is full size: kept at the tiles' 450 px.
        if let thumb {
            let short = min(thumb.size.width, thumb.size.height)
            let s = short > 450 ? 450 / short : 1
            GridThumbCache.store(thumb.preparingThumbnail(of: CGSize(width: thumb.size.width * s, height: thumb.size.height * s)) ?? thumb, id: item.id)
        }
        items.removeAll { $0.id == item.id }
        items.insert(item, at: 0)
        if !selectedOrder.contains(item.id) { selectedOrder.append(item.id) }
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
        // AVURLAsset picks its demuxer from the extension, so a temp file
        // without one plays as nothing at all.
        let ext = (path as NSString).pathExtension.isEmpty ? "mp4" : (path as NSString).pathExtension
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("otc-trim-\(UUID().uuidString)")
            .appendingPathExtension(ext)
        // In 4 MiB pieces straight to the file: a long video no longer
        // has to fit in memory (twice) to be trimmed.
        let size = try await FileDownload.download(path: path, to: url).size
        guard size > 0 else {
            try? FileManager.default.removeItem(at: url)
            throw NSError(domain: "NewPostPicker", code: -4, userInfo: [NSLocalizedDescriptionKey: "Empty response"])
        }
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
    /// ("/ios/<deviceId>/<filename>", or its alternate name when another
    /// photo already has that name) so a photo posted here and later
    /// reached by PhotoSync's own background sync are recognized as the
    /// same file rather than uploaded twice.
    private static func uploadIfNeeded(_ asset: PHAsset) async throws -> String {
        let ws = OTCConnection.shared
        // Off the main actor: synchronous Keychain reads (see
        // OTCConnection.connectAndAuth).
        let deviceId = await Task.detached(priority: .userInitiated) {
            SecretsStore.loadOrCreate().deviceId
        }.value
        guard let rawName = PhotoSync.shared.resourceFilename(for: asset) else {
            throw NSError(domain: "NewPostPicker", code: -1, userInfo: [NSLocalizedDescriptionKey: "Could not read this photo"])
        }
        let cleanName = rawName.replacingOccurrences(of: "/", with: "_")
        let path = "/ios/\(deviceId)/\(cleanName)"
        let altPath = "/ios/\(deviceId)/\(PhotoSync.remoteName(cleanName, localIdentifier: asset.localIdentifier, alt: true))"
        let created = Google_Protobuf_Timestamp(date: asset.creationDate ?? Date())

        func link(_ hash: String, at target: String) async throws -> Msg_RespEnvelope {
            try await ws.request { e in
                var lf = Msg_LinkFile()
                lf.hash = hash
                lf.path = target
                lf.forceOverride = false
                lf.created = created
                e.payload = .reqLinkFile(lf)
            }
        }
        // The name holds another photo (camera names repeat): this one
        // goes under its alternate name, as PhotoSync does. The device
        // says this only for different content.
        func isDuplicated(_ resp: Msg_RespEnvelope) -> Bool {
            resp.error && PhotoSync.isDuplicatedFile(resp.errorMessage)
        }

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
                var target = path
                var resp = try await link(cachedHash, at: target)
                if isDuplicated(resp) {
                    target = altPath
                    resp = try await link(cachedHash, at: target)
                }
                if case .respFile = resp.payload { return target }
                if resp.error { throw NSError(domain: "NewPostPicker", code: -2, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage]) }
            }
        }

        // This is the user explicitly waiting on posting *this* photo
        // right now, not a background bulk sync - always allow the iCloud
        // fetch if needed, regardless of the "Sync from iCloud" setting
        // PhotoSync itself respects. Off the main actor: readData blocks
        // its thread until the whole original is downloaded, then the
        // hash reads every byte - the UI froze for all of it, the
        // "Uploading 1 of n…" status included.
        let (data, hash) = try await Task.detached(priority: .userInitiated) { () throws -> (Data, String) in
            let (data, _, _) = try PhotoSync.shared.readData(for: asset, allowNetwork: true)
            let hash = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
            return (data, hash)
        }.value

        func hasFile() async throws -> Bool {
            let hasResp = try await ws.request { e in
                var hf = Msg_HasFile()
                hf.hash = hash
                e.payload = .reqHasFile(hf)
            }
            if case .respFileExists(let fe) = hasResp.payload { return fe.exists }
            return false
        }
        var alreadyOnDevice = try await hasFile()

        func send(to target: String) async throws -> Msg_RespEnvelope {
            if alreadyOnDevice { return try await link(hash, at: target) }
            // Issue #165: chunked, never the whole file in one message.
            return try await ws.uploadChunked(path: target, source: .data(data),
                                              forceOverride: false, created: created,
                                              sha256: hash)
        }
        var target = path
        var resp: Msg_RespEnvelope
        do {
            resp = try await send(to: target)
        } catch let error where PhotoSync.isDuplicatedFile(error.localizedDescription) {
            resp = Msg_RespEnvelope.with { $0.error = true; $0.errorMessage = error.localizedDescription }
        }
        if isDuplicated(resp) {
            target = altPath
            // A refused upload's content is dropped with it.
            if !alreadyOnDevice { alreadyOnDevice = try await hasFile() }
            resp = try await send(to: target)
        }
        if case .respFile = resp.payload {
            AssetSyncCache.shared.record(localIdentifier: asset.localIdentifier, hash: hash)
            return target
        }
        throw NSError(domain: "NewPostPicker", code: -3, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "Upload failed" : resp.errorMessage])
    }
}

struct NewPostPickerView: View {
    @StateObject private var vm = NewPostPickerVM()
    @Environment(\.dismiss) private var dismiss
    @State private var showSuggest = false
    @State private var showCamera = false
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
                                selectionNumber: vm.selectedOrder.firstIndex(of: item.id).map { $0 + 1 },
                                thumbsVersion: vm.thumbsVersion,
                                needThumb: { vm.needThumb(item) },
                                onTap: { vm.toggleSelect(item.id) }
                            )
                            .task { await vm.loadMoreIfNeeded(current: item) }
                            // Its thumbnail asked for first while it shows.
                            .onAppear { vm.tileShown(item) }
                            .onDisappear { vm.tileHidden(item) }
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

                // Composer bar: camera (issue #150), caption, Publish.
                HStack(spacing: 8) {
                    if UIImagePickerController.isSourceTypeAvailable(.camera) {
                        Button { showCamera = true } label: {
                            Image(systemName: "camera").font(.title3)
                        }
                        .accessibilityLabel("Take a photo or video")
                        .disabled(vm.publishing)
                    }
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
        .fullScreenCover(isPresented: $showCamera) {
            CameraCapture { image, videoURL in
                showCamera = false
                Task { await vm.addCapture(image: image, videoURL: videoURL) }
            }
            .ignoresSafeArea()
        }
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

/// The phone source's tile thumbnails, from PhotoKit.
enum PhoneThumb {
    // Sized for a @3x device showing this grid's ~140pt-wide tiles
    // (140 * 3 = 420) with some headroom - 240 was visibly soft.
    private static let localSize = CGSize(width: 450, height: 450)
    // From iCloud: a little smaller, which iCloud serves from a smaller
    // copy, so it arrives sooner.
    private static let cloudSize = CGSize(width: 320, height: 320)

    /// Whatever this phone has at once - often a small, soft preview. Shown
    /// while request() gets the sharp one.
    static func quick(_ asset: PHAsset) async -> UIImage? {
        await fetch(asset, size: localSize, mode: .fastFormat, resize: .fast, network: false)
    }

    /// The copy on this phone first. Only when there is none at tile size
    /// - with "Optimize iPhone Storage" an older photo keeps just a tiny
    /// preview here, and a high-quality request that may not use the
    /// network answers nothing, which left most of the phone source's
    /// tiles empty - a tile-sized copy from iCloud: a few tens of KB,
    /// never the full original issue #58 kept off the network.
    static func request(_ asset: PHAsset) async -> UIImage? {
        if let local = await fetch(asset, size: localSize, mode: .highQualityFormat, resize: .exact, network: false) {
            return local
        }
        if Task.isCancelled { return nil }
        return await fetch(asset, size: cloudSize, mode: .highQualityFormat, resize: .fast, network: true)
    }

    /// One PhotoKit request, cancelled with the task: a tile scrolled away
    /// no longer holds the network for the ones on screen. .fastFormat and
    /// .highQualityFormat answer once; a cancelled request may answer nil
    /// or not at all, so the continuation is resumed exactly once either
    /// way (PendingThumb).
    private static func fetch(_ asset: PHAsset, size: CGSize, mode: PHImageRequestOptionsDeliveryMode,
                              resize: PHImageRequestOptionsResizeMode, network: Bool) async -> UIImage? {
        let opts = PHImageRequestOptions()
        // .fastFormat alone was "the thumbnails suck": the fastest cached
        // representation, capped well below targetSize. It is only the
        // first look now; .highQualityFormat is the best quality for
        // targetSize, still in a single call (unlike .opportunistic).
        opts.deliveryMode = mode
        // A targetSize-sized image either way: from what is cached here,
        // or (network) the matching size from iCloud - never the original.
        opts.isNetworkAccessAllowed = network
        opts.isSynchronous = false
        opts.resizeMode = resize
        let pending = PendingThumb()
        return await withTaskCancellationHandler {
            await withCheckedContinuation { cont in
                pending.begin(cont)
                let id = PHImageManager.default().requestImage(for: asset, targetSize: size, contentMode: .aspectFill, options: opts) { image, _ in
                    pending.finish(image)
                }
                pending.started(id)
            }
        } onCancel: {
            pending.cancel()
        }
    }
}

/// A PhotoKit request's continuation, resumed exactly once - by the result
/// or by cancelling, whichever comes first.
private final class PendingThumb: @unchecked Sendable {
    private let lock = NSLock()
    private var cont: CheckedContinuation<UIImage?, Never>?
    private var requestID: PHImageRequestID?
    private var cancelled = false

    func begin(_ c: CheckedContinuation<UIImage?, Never>) {
        lock.lock(); cont = c; lock.unlock()
    }
    func started(_ id: PHImageRequestID) {
        lock.lock()
        requestID = id
        let cancelNow = cancelled
        lock.unlock()
        if cancelNow { PHImageManager.default().cancelImageRequest(id) }
    }
    func finish(_ image: UIImage?) {
        lock.lock(); let c = cont; cont = nil; lock.unlock()
        c?.resume(returning: image)
    }
    func cancel() {
        lock.lock()
        cancelled = true
        let id = requestID
        lock.unlock()
        if let id { PHImageManager.default().cancelImageRequest(id) }
        finish(nil)
    }
}

/// A synced item's tile, decoded once at tile size (GridThumbCache) - the
/// grid's own decode where it has no bytes of its own (its thumbnail is in
/// the cache on this phone: GridThumbLoader decodes it at the grid's
/// size); a phone item's while it is still cached.
private func tileImage(_ item: NewPostPickerVM.Item, maxPt: CGFloat) -> UIImage? {
    if item.asset != nil { return GridThumbCache.stored(item.id) }
    return GridThumbCache.image(id: item.id, data: item.thumbData, maxPt: maxPt)
        ?? GridThumbCache.cached(id: item.id, maxPt: PickTile.side)
}

/// A phone item's tile, held by the tile while it is on screen: from the
/// cache, or asked of PhotoKit again once the cache let it go - the
/// phone's quick preview at once, then the sharp copy (which may come
/// from iCloud).
@MainActor
private func loadPhoneTile(_ item: NewPostPickerVM.Item, show: (UIImage) -> Void) async {
    guard let asset = item.asset else { return }
    if let cached = GridThumbCache.stored(item.id) { show(cached); return }
    if let quick = await PhoneThumb.quick(asset), !Task.isCancelled { show(quick) }
    guard !Task.isCancelled, let img = await PhoneThumb.request(asset), !Task.isCancelled else { return }
    GridThumbCache.store(img, id: item.id)
    show(img)
}

private struct PickTile: View {
    let item: NewPostPickerVM.Item
    // Issue #48: 1-based position in the post, nil when not selected -
    // replaces a plain checkmark so the grid itself shows the current
    // order, not just membership.
    let selectionNumber: Int?
    /// The grid's thumbnails changed (NewPostPickerVM.thumbsVersion): the
    /// image is looked up again.
    let thumbsVersion: Int
    /// A synced tile has no image: from the thumbnail cache, else the device.
    let needThumb: () -> Void
    let onTap: () -> Void

    private var isSelected: Bool { selectionNumber != nil }

    static let side: CGFloat = 120
    @State private var phoneThumb: UIImage?

    var body: some View {
        let img = phoneThumb ?? tileImage(item, maxPt: Self.side)
        ZStack(alignment: .topTrailing) {
            Group {
                if let img {
                    Image(uiImage: img).resizable().scaledToFill()
                } else {
                    Color.gray.opacity(0.2)
                }
            }
            .task(id: item.id) { await loadPhoneTile(item) { phoneThumb = $0 } }
            .task(id: img == nil) { if img == nil, item.asset == nil { needThumb() } }
            .frame(maxWidth: Self.side, maxHeight: Self.side)
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
                                thumbsVersion: vm.thumbsVersion,
                                needThumb: { vm.needThumb(item) },
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
    /// The grid's thumbnails changed (NewPostPickerVM.thumbsVersion): the
    /// image is looked up again.
    let thumbsVersion: Int
    /// A synced item's image was let go (the app went to the background,
    /// memory ran short): from the thumbnail cache, else the device.
    let needThumb: () -> Void
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

    @State private var phoneThumb: UIImage?

    var body: some View {
        let img = phoneThumb ?? tileImage(item, maxPt: 60)
        VStack(spacing: 2) {
            ZStack(alignment: .topTrailing) {
                Group {
                    if let img {
                        Image(uiImage: img).resizable().scaledToFill()
                    } else {
                        Color.gray.opacity(0.2)
                    }
                }
                .task(id: item.id) { await loadPhoneTile(item) { phoneThumb = $0 } }
                .task(id: img == nil) { if img == nil, item.asset == nil { needThumb() } }
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

/// Issue #150: the system camera, for a photo or a video, as a sheet.
/// Hands back the photo, or the recorded video's file, or neither if the
/// person cancelled.
struct CameraCapture: UIViewControllerRepresentable {
    let onDone: (UIImage?, URL?) -> Void

    func makeUIViewController(context: Context) -> UIImagePickerController {
        let picker = UIImagePickerController()
        picker.sourceType = .camera
        picker.mediaTypes = ["public.image", "public.movie"]
        picker.videoQuality = .typeHigh
        picker.delegate = context.coordinator
        return picker
    }

    func updateUIViewController(_ controller: UIImagePickerController, context: Context) {}

    func makeCoordinator() -> Coordinator { Coordinator(onDone: onDone) }

    final class Coordinator: NSObject, UIImagePickerControllerDelegate, UINavigationControllerDelegate {
        let onDone: (UIImage?, URL?) -> Void
        init(onDone: @escaping (UIImage?, URL?) -> Void) { self.onDone = onDone }

        func imagePickerController(_ picker: UIImagePickerController, didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any]) {
            onDone(info[.originalImage] as? UIImage, info[.mediaURL] as? URL)
        }

        func imagePickerControllerDidCancel(_ picker: UIImagePickerController) {
            onDone(nil, nil)
        }
    }
}
