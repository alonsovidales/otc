// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI
import SwiftProtobuf
import Photos
import MapKit
import CryptoKit
import AVKit

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

    // Issue #77: the date scrubber. Only meaningful against date order - a
    // tag search sorts by relevance (dao.SearchMedia switches to "order by
    // score desc" whenever tags are given) - so showScrubber below hides
    // it outright rather than showing ticks against an order it doesn't
    // reflect. A person filter alone is fine, that keeps created-desc.
    struct DateBucket: Identifiable { let month: String; let count: Int; let start: Int; let end: Int; var id: String { month } }
    @Published var dateBuckets: [DateBucket] = []
    // scrubFrac is the live drag position (0=newest/top, 1=oldest/bottom),
    // nil whenever the user isn't actively dragging. placeholderCount
    // outlives the drag itself: it stays set (showing black squares in
    // place of the real grid) from the moment a drag starts until the
    // jump-to-date fetch it triggers actually resolves, so releasing the
    // thumb doesn't flash an empty grid while the real thumbnails are
    // still in flight.
    @Published var scrubFrac: Double? = nil
    @Published var placeholderCount: Int? = nil

    var totalPhotos: Int { dateBuckets.last?.end ?? 0 }
    var showScrubber: Bool { chips.isEmpty && !dateBuckets.isEmpty }

    // Ticks: one per year, positioned by cumulative photo count rather
    // than calendar-uniform spacing, so a drag fraction actually
    // corresponds to "how far into the library" that year sits (matches
    // Google Photos' own timeline, where a sparse year takes less track
    // space than a busy one). Mirrors web's PhotoGallery.tsx.
    var yearTicks: [(year: String, pct: Double)] {
        guard totalPhotos > 0 else { return [] }
        var ticks: [(String, Double)] = []
        var lastYear = ""
        for b in dateBuckets {
            let year = String(b.month.prefix(4))
            if year != lastYear {
                ticks.append((year, Double(b.start) / Double(totalPhotos)))
                lastYear = year
            }
        }
        return ticks
    }

    var scrubTarget: DateBucket? {
        guard let frac = scrubFrac, totalPhotos > 0 else { return nil }
        let idx = Int(frac * Double(totalPhotos))
        return dateBuckets.first(where: { idx >= $0.start && idx < $0.end }) ?? dateBuckets.last
    }

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
    // Issue #106: non-nil while a video is open in the viewer. Held rather
    // than rebuilt in the view body - a player constructed inline is
    // recreated on every SwiftUI update, which tears playback down and
    // restarts it (see the same fix in the social feed, issue #107).
    @Published var videoPlayer: AVPlayer? = nil
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

    // MARK: Date scrubber (issue #77)
    private func loadDateBuckets() async {
        guard chips.isEmpty else { dateBuckets = []; return }
        let people = selectedPeople // snapshot - see fetchPage's own doc comment on why
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            var b = Msg_ReqPhotoDateBuckets()
            b.personIds = people
            b.includeVideos = true // issue #106: same set the grid shows
            req.payload = .reqPhotoDateBuckets(b)
            e = req
        }) else { return }
        if case .respPhotoDateBuckets(let r) = resp.payload {
            var cum = 0
            dateBuckets = r.buckets.map { pb in
                let start = cum
                cum += Int(pb.count)
                return DateBucket(month: pb.month, count: Int(pb.count), start: start, end: cum)
            }
        }
    }

    // Mirrors web's jumpToDate in PhotoGallery.tsx: a reset exactly like
    // resetAndLoadFirstPage does for a fresh filter, plus a `before`
    // cutoff anchoring the fresh search to the target month's own last
    // instant (so it starts at that month's newest photo and reads
    // backward, same as scrolling there normally would).
    func jumpToDate(_ month: String) {
        searchTask?.cancel()
        searchTask = Task { await performJump(month) }
    }

    private func performJump(_ month: String) async {
        let parts = month.split(separator: "-").compactMap { Int($0) }
        guard parts.count == 2 else { return }
        var comps = DateComponents()
        comps.year = parts[0]
        comps.month = parts[1]
        comps.day = 1
        let cal = Calendar.current
        guard let firstOfMonth = cal.date(from: comps),
              let firstOfNextMonth = cal.date(byAdding: .month, value: 1, to: firstOfMonth) else { return }
        let before = firstOfNextMonth.addingTimeInterval(-1) // last instant of `month`

        searchGeneration += 1
        let myGeneration = searchGeneration
        loading = false
        endReached = false
        token = ""
        items = []
        selected.removeAll()
        await fetchPage(overrideToken: "", before: before)
        // placeholderCount is left showing until this resolves (or is
        // superseded) - cleared here rather than by the caller so a jump
        // that gets superseded by a *newer* jump/filter change doesn't
        // clear placeholders that newer request is still relying on.
        if myGeneration == searchGeneration { placeholderCount = nil }
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
        // A filter change makes any scrub in progress meaningless (its
        // target bucket was computed against the *previous* filter's
        // counts) - drop it rather than leave stale placeholders or a
        // thumb positioned against numbers that no longer apply.
        scrubFrac = nil
        placeholderCount = nil
        async let buckets: Void = loadDateBuckets()
        await fetchPage(overrideToken: "")
        await buckets
        mergeLocalIfAny()
    }

    func loadMoreIfNeeded(current item: Item?) async {
        guard let item else { return }
        guard !endReached else { return }
        // Near the end is the only thing worth acting on - checked before
        // the in-flight case below so a tile appearing at the top of the
        // grid can't queue up a page nobody needs yet.
        guard let idx = items.firstIndex(of: item), idx >= items.count - 12 else { return }
        // A tile that appears while a fetch is already running used to
        // just return. Nothing then asked again: the only thing that can
        // trigger the next page is a tile appearing for the first time
        // (.task runs once per tile), so a page that produced no new
        // tiles left the grid permanently stuck. Remember the ask instead
        // and let the in-flight fetch pick it up when it lands.
        guard !loading else {
            morePending = true
            return
        }

        await fetchUntilProgress()
    }

    /// Fetches pages until one actually adds something, the end is
    /// reached, or we give up.
    ///
    /// A page can legitimately add nothing and still not be the end: when
    /// the device no longer recognises the search token (it expires after
    /// a few minutes of not scrolling, and a device restart drops all of
    /// them), it starts the search again from the beginning and hands
    /// back photos this grid already has. Stopping there is what made the
    /// gallery look like it had run out of photos partway down.
    private func fetchUntilProgress() async {
        var failures = 0
        for _ in 0..<cMaxPagesWithoutProgress {
            let before = items.count
            if await fetchPage() {
                failures = 0
                if endReached || items.count > before { return }
                continue
            }

            // The request itself failed - a dropped connection, a device
            // that went away mid-scroll. endReached is deliberately left
            // alone (this is not the end of the library), but something
            // has to try again: no new tiles appeared, so nothing else
            // will ask. Back off a little between attempts, since the
            // usual cause is a connection that needs a moment.
            failures += 1
            if failures > cMaxFetchRetries { return }
            try? await Task.sleep(nanoseconds: UInt64(failures) * 1_500_000_000)
            if Task.isCancelled { return }
        }
    }

    /// Set when a tile asked for more while a fetch was already running,
    /// so the fetch that lands can honour it (see loadMoreIfNeeded).
    private var morePending = false

    /// How many consecutive pages that add nothing to tolerate before
    /// giving up, so a device that keeps restarting the same search can
    /// never spin here forever.
    private let cMaxPagesWithoutProgress = 12

    /// How many times to retry a page whose request failed outright
    /// before leaving it to the next tile that scrolls into view.
    private let cMaxFetchRetries = 2

    /// Returns whether the request completed (not whether it added
    /// anything) - a caller needs to tell "no more photos" apart from
    /// "that didn't work", because only one of those means stop asking.
    @discardableResult
    private func fetchPage(overrideToken: String? = nil, before: Date? = nil) async -> Bool {
        guard !loading, !endReached else { return false }
        let myGeneration = searchGeneration
        loading = true
        defer {
            // Only this request's own generation may clear loading - a
            // stale one finishing after a newer search started must not
            // report "done" for a fetch that isn't actually the current
            // one.
            if myGeneration == searchGeneration {
                loading = false
                if morePending, !endReached {
                    morePending = false
                    // Detached from this call so the defer isn't waiting
                    // on another round trip.
                    Task { await self.fetchUntilProgress() }
                }
            }
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
                // Issue #106: videos belong in the Images section. They
                // were excluded when this flag arrived (issue #60, where
                // only the composer opted in), which left a device's
                // videos unbrowsable from the app entirely.
                sp.includeVideos = true
                sp.token = requestToken
                // Lets the device resume where this grid actually is if
                // it no longer holds the token (see SearchPhotos.have).
                sp.have = Int32(self.items.count)
                // Issue #77: the date scrubber's "jump to date" - set only
                // by performJump above, which also resets loading/
                // endReached/token so this always starts a fresh,
                // cutoff-filtered search.
                if let before {
                    sp.before = SwiftProtobuf.Google_Protobuf_Timestamp(date: before)
                }
                req.payload = .reqSearchPhotos(sp)
                e = req
            }
            // A newer search superseded this one while it was in flight -
            // discard rather than let a stale reply clobber current
            // results. Task.isCancelled backs up the generation check
            // (restartSearch cancels this call's Task the moment a newer
            // one starts) - belt and suspenders, since either alone
            // catches the same case here.
            guard myGeneration == searchGeneration, !Task.isCancelled else { return false }
            // An error reply (the device answering "internal error", say)
            // lands here too - it's not a page, and it must not be read
            // as the end of the library.
            guard case .respListOfFiles(let lof) = resp.payload else { return false }

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

            return true
        } catch {
            // Swallowed silently before, which meant one failed page
            // stopped the grid loading anything ever again - nothing
            // retries on its own here (see loadMoreIfNeeded).
            print("[PhotoGallery] page fetch failed: \(error)")
            return false
        }
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
        videoPlayer?.pause()
        videoPlayer = nil
        infoOpen = false
        infoData = nil
        Task { await fetchHiRes(index: index) }
    }
    func closeModal() {
        openIndex = nil
        hiResImage = nil
        videoPlayer?.pause()
        videoPlayer = nil
    }

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

        // Issue #106: a video can't be decoded into a UIImage - it gets
        // written out and played instead. Its own thumbnail already stands
        // in on screen while this runs, so there is no blank frame.
        if it.mime.hasPrefix("video/") {
            await fetchVideo(it)
            return
        }

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

    /// Issue #106/#107: fetches a video and hands back a player.
    ///
    /// The extension is load-bearing: AVFoundation works out how to demux a
    /// file:// URL from its path extension, so writing everything as .mp4
    /// (as the social feed used to) leaves a QuickTime recording - what an
    /// iPhone actually produces - mislabelled and silently unplayable.
    private func fetchVideo(_ it: Item) async {
        if let u = it.localURL {
            self.videoPlayer = AVPlayer(url: u)
            return
        }

        // Issue #110: stream it if the device offers a URL, so playback
        // starts on the first chunk instead of after the whole file has
        // come down the socket and been written to a temp file. A clip
        // small enough that streaming wouldn't pay for itself is declined
        // by the device, and falls through to the download below.
        if let streamURL = await MediaStream.url(forPath: it.path) {
            self.videoPlayer = AVPlayer(url: streamURL)
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
            guard case .respFile(let f) = resp.payload, f.hasContent else { return }
            let ext: String
            switch f.mime.lowercased() {
            case "video/quicktime": ext = "mov"
            case "video/mp4", "video/x-m4v": ext = "mp4"
            case "video/x-matroska": ext = "mkv"
            case "video/3gpp": ext = "3gp"
            default:
                let own = (it.path as NSString).pathExtension
                ext = own.isEmpty ? "mp4" : own.lowercased()
            }
            let tmp = FileManager.default.temporaryDirectory
                .appendingPathComponent(UUID().uuidString)
                .appendingPathExtension(ext)
            try f.content.write(to: tmp)
            self.videoPlayer = AVPlayer(url: tmp)
        } catch { /* leave the poster in place */ }
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
    // Back to a fixed 3 columns (issue #77 briefly tried .adaptive here to
    // fix an overflow caused by reserving dedicated layout space for the
    // scrubber - reverted along with that reservation itself, which
    // turned out to be the actual thing worth removing; the scrubber now
    // overlays the grid's own edge instead of pushing it inward).
    private let cols = Array(repeating: GridItem(.flexible(minimum: 120, maximum: 160), spacing: 1), count: 3)

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
                        // Precomputed rather than inlined into the Text -
                        // an unrelated type-check timeout elsewhere in
                        // this same body started tripping once enough
                        // other view code was added nearby, and pulling
                        // this particular double-lookup-plus-ternary out
                        // of the ViewBuilder expression is what resolved it.
                        let targetName = vm.allPeople.first(where: { $0.id == targetID })?.name
                        let displayName = (targetName?.isEmpty == false) ? targetName! : "Unnamed"
                        HStack(spacing: 4) {
                            Text("Merging into \(displayName) — tap another person to merge them in.")
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

            // Grid - while the date scrubber has a target bucket (dragging,
            // or the jump it triggered still in flight), placeholder
            // squares stand in for the real grid rather than showing
            // whatever was scrolled to before the jump started. Capped at
            // 300 - a month with thousands of photos doesn't need that
            // many real views just to convey "this is a lot of squares".
            // Wrapped in ScrollViewReader (issue #77) purely to reset
            // scroll position to the top once a jump starts - the page
            // doesn't otherwise know to, since `items` being reset
            // doesn't itself move an already-scrolled ScrollView.
            ScrollViewReader { proxy in
                // The scroll-to-top anchor used to be the LazyVGrid's own
                // first child - which made it a real grid cell (row 1,
                // column 1), not just an invisible marker, pushing every
                // photo over by one slot and rendering as a black square
                // where the first real thumbnail should be. It's now a
                // plain (zero-height) sibling of the grid inside the same
                // ScrollView instead - a VStack wrapping the *ScrollView*
                // itself (tried briefly) made the grid's very first load
                // render blank until a scroll gesture forced SwiftUI to
                // lay it out, so the ScrollView itself stays exactly as
                // it was, with the anchor moved one level in instead.
                ScrollView {
                    VStack(spacing: 0) {
                        Color.clear.frame(height: 0).id("photoGridTop")
                        LazyVGrid(columns: cols, spacing: 1) {
                            if let placeholderCount = vm.placeholderCount {
                                ForEach(0..<min(placeholderCount, 300), id: \.self) { _ in
                                    RoundedRectangle(cornerRadius: 8)
                                        .fill(Color.black)
                                        .aspectRatio(1, contentMode: .fit)
                                }
                            } else {
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
                        }
                    }
                    .padding(10)
                }
                .overlay(alignment: .trailing) {
                    // Issue #77: Google-Photos-style date scrubber -
                    // overlaid directly on the grid's own right edge
                    // (like Google Photos' own does) rather than
                    // reserving dedicated layout space for it, so it
                    // can't push a fixed 3-column grid past the screen's
                    // actual width. Ticks/tooltip only show while
                    // actively dragging - see PhotoDateScrubber.
                    if vm.showScrubber {
                        PhotoDateScrubber(vm: vm, scrollProxy: proxy)
                    }
                }
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
                videoPlayer: vm.videoPlayer,
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
        // Issue #106: a video's thumbData is a JPEG poster exactly like a
        // photo's, so without this there is nothing to tell them apart.
        .overlay(alignment: .bottomLeading) {
            if item.mime.hasPrefix("video/") {
                Image(systemName: "play.circle.fill")
                    .font(.system(size: 18))
                    .foregroundStyle(.white)
                    .shadow(radius: 2)
                    .padding(4)
            }
        }
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
    /// Issue #106: non-nil when the open item is a video, in which case it
    /// plays here instead of `image` being shown.
    let videoPlayer: AVPlayer?
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

                if let player = videoPlayer {
                    // Issue #106: plays in the viewer, filling it the same
                    // way a photo does. Tapping the cell is the play
                    // gesture, so it starts on its own rather than landing
                    // on a paused player needing a second tap.
                    VideoPlayer(player: player)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .onAppear { player.play() }
                        .onDisappear { player.pause() }
                } else if let image {
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

// Issue #77: Google-Photos-style date scrubber. A DragGesture over a thin
// trailing-edge track, matching the style already used for ImageModal's
// swipe-to-page gesture elsewhere in this file - year ticks positioned by
// cumulative photo count (see PhotoGalleryVM.yearTicks), a floating
// month/year tooltip only while the drag is active. Mirrors web's
// PhotoScrubber bit of PhotoGallery.tsx.
/// Issue #113: how wide the scrubber's grab area is. Deliberately under
/// the 44pt Apple suggests for a touch target - the gesture it competes
/// with, scrolling the grid, is the one performed constantly, and a
/// mis-grab there is far more annoying than having to aim a little
/// closer to the edge to scrub.
private let cScrubberTouchWidth: CGFloat = 16

/// Issue #113: how far a finger must travel inside the strip before it
/// counts as scrubbing at all. minimumDistance has to stay 0 (the gesture
/// must claim the touch before the ScrollView does, or a scrub - which is
/// also a vertical drag - gets eaten as a scroll), so this is what tells a
/// deliberate scrub from a touch that was only ever meant to be a scroll.
private let cScrubEngageDistance: CGFloat = 8

private struct PhotoDateScrubber: View {
    @ObservedObject var vm: PhotoGalleryVM
    let scrollProxy: ScrollViewProxy

    private static let monthNames = ["Jan","Feb","Mar","Apr","May","Jun","Jul","Aug","Sep","Oct","Nov","Dec"]
    private static func label(for month: String) -> String {
        let parts = month.split(separator: "-")
        guard parts.count == 2, let m = Int(parts[1]), (1...12).contains(m) else { return month }
        return "\(monthNames[m - 1]) \(parts[0])"
    }

    // A run of sparse years can land closer together than a label is
    // tall - reproduced live (both here and on web) as several years'
    // labels rendering stacked on top of each other, unreadable - so this
    // drops any tick that would land within minGap of the last one
    // actually kept, once the track's real height is known. Mirrors
    // web's identical thinning in PhotoGallery.tsx's yearTicks.
    private static func thinnedTicks(_ ticks: [(year: String, pct: Double)], height: CGFloat, minGap: CGFloat = 14) -> [(year: String, pct: Double)] {
        guard height > 0 else { return [] }
        var kept: [(year: String, pct: Double)] = []
        var lastPx: CGFloat = -.infinity
        for t in ticks {
            let px = height * t.pct
            if px - lastPx < minGap { continue }
            kept.append(t)
            lastPx = px
        }
        return kept
    }

    var body: some View {
        // The 64pt width has to constrain the GeometryReader itself, not
        // a view inside it - GeometryReader always expands to fill
        // whatever space its parent (here, the .overlay) offers it, which
        // is the *whole* grid's width, not a trailing sliver. A
        // .frame(width:) applied to a child further down only shrinks
        // that child's own reported size; it doesn't reposition the
        // child within its parent, so the child (and this scrubber along
        // with it) ended up pinned to the *leading* edge of that full-
        // width GeometryReader instead of the trailing one - reproduced
        // live as the whole timeline rendering down the left edge of the
        // screen, half off-screen, instead of the right. Constraining the
        // GeometryReader from the outside makes geo.size.width correctly
        // report 64 on the inside, and lets .overlay(alignment: .trailing)
        // in PhotoGalleryView do the actual right-edge placement.
        GeometryReader { geo in
            ZStack(alignment: .topTrailing) {
                // A persistent thin rail - the only thing visible at
                // rest, so there's still some indication a draggable
                // timeline exists there even though the year labels
                // themselves only appear once you actually touch it.
                Capsule()
                    .fill(Color.white.opacity(0.15))
                    .frame(width: 3)
                    .padding(.trailing, 6)
                // Year labels only show up while actively dragging, same
                // as the month/year tooltip below - a permanently-visible
                // column of labels was cluttering the grid at rest;
                // Google Photos' own only appears once you touch the bar.
                if vm.scrubFrac != nil {
                    ForEach(Self.thinnedTicks(vm.yearTicks, height: geo.size.height), id: \.year) { tick in
                        Text(tick.year)
                            .font(.system(size: 10))
                            .foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity, alignment: .trailing)
                            .padding(.trailing, 16)
                            .offset(y: geo.size.height * tick.pct - 6)
                    }
                }
                if let target = vm.scrubTarget, let frac = vm.scrubFrac {
                    Text(Self.label(for: target.month))
                        .font(.caption.bold())
                        .padding(.horizontal, 8).padding(.vertical, 4)
                        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 6))
                        .frame(maxWidth: .infinity, alignment: .trailing)
                        .padding(.trailing, 16)
                        .offset(y: geo.size.height * frac - 12)
                    Circle()
                        .fill(Color.yellow)
                        .frame(width: 10, height: 10)
                        .offset(y: geo.size.height * frac - 5)
                }
            }
            .frame(width: geo.size.width, height: geo.size.height, alignment: .topTrailing)
            // Issue #113: the drag lives on a narrow strip at the very
            // edge rather than the whole 64pt column. The column is that
            // wide so the year labels have room, but making all of it
            // grabbable meant a finger starting a scroll anywhere near
            // the right edge was taken as a scrub - and with
            // minimumDistance 0 (a tap has to jump) it was claimed the
            // instant you touched down, before the gesture's direction
            // was knowable.
            //
            // This does not undo the widening the .frame(width: 64)
            // comment below describes: the year labels only render while
            // a drag is already in progress, so at rest there is nothing
            // out there to tap, and a drag once started keeps tracking
            // outside the strip anyway.
            .overlay(alignment: .trailing) {
                Color.clear
                    .frame(width: cScrubberTouchWidth)
                    .contentShape(Rectangle())
                    .gesture(
                DragGesture(minimumDistance: 0)
                    .onChanged { value in
                        // Issue #113: nothing happens until the finger has
                        // actually travelled. onChanged fires once on
                        // touch-down with no translation at all, and that
                        // first call used to scroll the grid to the top -
                        // so brushing this strip on the way into a scroll
                        // threw the whole library back to the newest
                        // photo, which is what "it does a massive
                        // scrolling" was. A scrub moves; a stray touch
                        // doesn't.
                        guard vm.scrubFrac != nil || abs(value.translation.height) >= cScrubEngageDistance else { return }
                        if vm.scrubFrac == nil {
                            // Now it's genuinely a drag: scroll away
                            // immediately rather than waiting for the jump
                            // to resolve, so the cards visibly start
                            // moving out of the way.
                            scrollProxy.scrollTo("photoGridTop", anchor: .top)
                        }
                        let frac = min(1, max(0, value.location.y / geo.size.height))
                        vm.scrubFrac = frac
                        // No network call here at all - the placeholder
                        // count comes straight out of the already-fetched
                        // bucket counts, which is the whole point:
                        // dragging fast across years costs nothing but
                        // re-renders.
                        if vm.totalPhotos > 0 {
                            let idx = Int(frac * Double(vm.totalPhotos))
                            let bucket = vm.dateBuckets.first(where: { idx >= $0.start && idx < $0.end }) ?? vm.dateBuckets.last
                            if let bucket { vm.placeholderCount = bucket.count }
                        }
                    }
                    .onEnded { _ in
                        let target = vm.scrubTarget // capture before clearing scrubFrac below
                        vm.scrubFrac = nil
                        if let target {
                            vm.jumpToDate(target.month)
                        } else {
                            vm.placeholderCount = nil
                        }
                    }
                    )
            }
        }
        // Wide enough to hold the year labels *inside* the interactive
        // strip, not off to its side - reproduced live as tapping
        // directly on a visible year label doing nothing, because the
        // actual hit area used to be a narrow edge-only sliver the labels
        // floated outside of. Now the whole box (labels included) is one
        // tap/drag target. See the GeometryReader comment above for why
        // this has to sit out here rather than on a view inside it.
        .frame(width: 64)
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
