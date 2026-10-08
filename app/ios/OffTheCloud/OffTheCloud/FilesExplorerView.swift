// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FilesExplorerView.swift
//  OffTheCloud
//
//  Native port of the web app's Files tab (web/src/components/FilesExplorer.tsx):
//  path navigation, upload (via the document picker — the native analogue of
//  the web's drag-and-drop), multi-select delete/share/download-zip, and the
//  Images section's own viewer for photos and videos. In the narrow layout
//  a search field of its own heads it, for files and folders only
//  (FilesSearch.swift).

import SwiftUI
import UniformTypeIdentifiers
import QuickLook
import Combine

private func isDirFile(_ f: Msg_File) -> Bool { f.mime == "inode/directory" }

/// A grid tile's image with the device's thumbnail bytes it was decoded
/// from, kept together: the viewer's placeholder and its Save/Share
/// fallback use the bytes, at the device's full 1000 px, whenever the
/// tile is there.
final class FileThumb {
    let image: UIImage
    let data: Data

    init(image: UIImage, data: Data) {
        self.image = image
        self.data = data
    }

    /// The decoded bitmap and the bytes, for the cache's budget.
    var cost: Int {
        Int(image.size.width * image.scale * image.size.height * image.scale) * 4 + data.count
    }
}
private func isImgFile(_ f: Msg_File) -> Bool { f.mime.hasPrefix("image/") }
private func isVideoFile(_ f: Msg_File) -> Bool { f.mime.hasPrefix("video/") }
/// The grid asks the device for a thumbnail only for these - photos and
/// videos (a .heic can come back with a generic mime, hence the name check).
private func isMediaFile(_ f: Msg_File) -> Bool {
    isImgFile(f) || isVideoFile(f) || f.path.lowercased().hasSuffix(".heic")
}

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

extension Msg_File {
    /// Issue #187: the size in bytes. Devices before release 93 leave
    /// size64 at 0 and send only the int32 size, which wraps from 2 GiB on.
    var fileSize: Int64 { size64 != 0 ? size64 : Int64(size) }
}

struct FileRow: Identifiable {
    var id: String { path }
    let path: String
    let name: String
    let isDir: Bool
    let size: Int64
    let created: Date?
    let modified: Date?
    // Issue #132: inside (or itself) an upload-only folder, and how many
    // older versions the device keeps for the file.
    let uploadOnly: Bool
    let versions: Int32
    // Issue #192: inside (or itself) a folder kept out of Images.
    let outOfImages: Bool
    let raw: Msg_File
}

extension FileRow {
    /// A file as the device lists it (a full path, as SearchFiles gives).
    init(file f: Msg_File) {
        self.init(
            path: f.path,
            name: f.path == ".." ? ".." : leafName(f.path),
            isDir: isDirFile(f),
            size: f.fileSize,
            created: f.hasCreated ? f.created.date : nil,
            modified: f.hasModified ? f.modified.date : nil,
            uploadOnly: f.uploadOnly,
            versions: f.versions,
            outOfImages: f.outOfImages,
            raw: f
        )
    }
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
    // Issue #192: whether the device can keep folders out of Images -
    // devices before release 108 leave it false, and then nothing about it
    // shows - and whether the folder listed (listedPath) is kept out.
    @Published var outOfImagesSupported = false
    @Published var folderOutOfImages = false
    /// The folder the rows and folderOutOfImages were listed for. Until the
    /// next folder's listing arrives `path` is already that folder's, so
    /// the banner - and its "Show in Images", which acts on `path` - waits
    /// for it (outOfImagesBanner).
    @Published private(set) var listedPath: String?
    /// The folder whose switch is on its way, without its trailing slash
    /// (a row's full path). The device answers once the tags and faces are
    /// gone, which can take a while on a busy device (most of a minute was
    /// seen on Pit), so its row - or the banner - shows a spinner until
    /// then, and every other switch (the menu item, the mark's alert, the
    /// banner) is disabled until it is answered: one at a time, as on
    /// Android and the web.
    @Published private(set) var outOfImagesBusy: String?
    // The grid's thumbnails, by full path + hash so a replaced file gets a
    // fresh one. noThumb remembers the paths the device answered without
    // one, so they aren't asked for again.
    //
    // `thumbs` holds only the tiles on screen, so none of them can be
    // evicted; a tile scrolling away moves its image to thumbCache, which
    // is bounded and gives memory back under pressure. Every tile ever
    // drawn used to stay decoded for the session - ~5 MB each at the
    // device's 1000 px, so a few hundred tiles of a big photo folder got
    // the app killed. Not @Published: a tile leaving needs no redraw, and
    // additions send the change themselves.
    private(set) var thumbs: [String: FileThumb] = [:]
    private var visibleThumbs: Set<String> = []
    private let thumbCache: NSCache<NSString, FileThumb> = {
        let cache = NSCache<NSString, FileThumb>()
        cache.totalCostLimit = 96 << 20
        return cache
    }()
    // The device's own thumbnail bytes, for the viewer: its placeholder
    // until the full size arrives, and its save/share fallback (the Images
    // section feeds it the same way), while tiles are decoded smaller.
    // They are the grid's small thumbnails (release 111): where one would
    // stay on screen the viewer asks for the big one itself
    // (PhotoGalleryVM.thumbnailStays), never through these caches. Each
    // tile keeps its own (FileThumb); this keeps them a while longer, for
    // photos whose tile was given back.
    private let thumbBytes: NSCache<NSString, NSData> = {
        let cache = NSCache<NSString, NSData>()
        cache.totalCostLimit = 32 << 20
        return cache
    }()
    private var noThumb: Set<String> = []
    // Paths the grid wants (cells that appeared), drained 24 at a time by
    // one task at a time; inFlight keeps a cell scrolling back into view
    // from queuing its path twice.
    private var thumbQueue: [(key: String, path: String, folder: String)] = []
    private var thumbInFlight: Set<String> = []
    private var thumbPumping = false

    /// A listing that failed on the way (no answer in time, a dropped
    /// connection, the bridge's "device unreachable") is asked again, 1 s
    /// doubling to 10 s and at once on a wake, while its folder is still
    /// the one shown and nothing newer was asked for (listingAsks). The
    /// device's own refusal (a folder that isn't there) is not.
    let listRetry = PageRetry()
    private var listingAsks = 0
    private var wakeWatch: AnyCancellable?
    /// Why the listing of failedPath failed: said while it is asked again
    /// (not cleared at each try, which flickered and was announced anew).
    private var listingProblem: LoadProblem?
    private var failedPath: String?
    /// Whether the phone has a network now. Tests decide in the phone's place.
    var online: @MainActor () -> Bool = { !NetworkWatch.shared.offline }
    /// Why a listing failed. Tests decide in the phone's place.
    var problemOf: @MainActor (Msg_RespEnvelope?, Error?) -> LoadProblem = { LoadProblem.now(resp: $0, error: $1) }

    init(initialPath: String) {
        self.path = initialPath
        wakeWatch = OTCConnection.shared.$wakes
            .dropFirst()
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.listRetry.wake() }
    }

    /// Sends one of the explorer's requests (the listing, the folder
    /// switches, the grid's and the search results' thumbnails). Tests
    /// answer them in the device's place.
    var request: @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope = { payload in
        // A folder of thousands of names is a page of its own, with more
        // time after a timeout (OTCConnection.ask, as Android's
        // "listing"); the rest get the socket's own time.
        if case .reqListFiles = payload {
            return try await OTCConnection.shared.ask("listing", base: OTCConnection.pageTimeout) { $0.payload = payload }
        }
        return try await OTCConnection.shared.request { $0.payload = payload }
    }

    /// What Files says while the folder `p` is listed (Android's
    /// listingErrorWhileAsking): for the folder whose listing failed
    /// (`failedPath`, `problem`), why - it stays up while it is asked
    /// again rather than flickering away at each try - and the neutral
    /// line once the phone is back `online`; nothing for another folder.
    nonisolated static func errorWhileAsking(_ problem: LoadProblem?, failedPath: String?, _ p: String, online: Bool) -> String? {
        guard let problem, failedPath == p else { return nil }
        return problem.backOnline(online).text("The files")
    }

    /// The listing failed on the way (`problem`: why): said in plain words,
    /// and asked again after the wait.
    private func listingFailed(_ asked: String, _ problem: LoadProblem) {
        listingProblem = problem
        failedPath = asked
        error = problem.text("The files")
        let ask = listingAsks
        listRetry.failed { [weak self] in
            guard let self, self.listingAsks == ask, self.path == asked else { return }
            Task { await self.load() }
        }
    }

    func load() async {
        // The folder this listing is for: one that comes back after the
        // screen moved on (the search sending it to another folder while
        // the first listing was on its way) is dropped.
        let asked = path
        listingAsks += 1
        loading = true
        defer { if asked == path { loading = false } }
        error = Self.errorWhileAsking(listingProblem, failedPath: failedPath, asked, online: online())
        var req = Msg_ListFiles()
        req.path = asked
        do {
            let resp = try await request(.reqListFiles(req))
            guard asked == path else { return }
            if case .respAck(let ack) = resp.payload, ack.code == LoadProblem.deviceUnreachable {
                listingFailed(asked, problemOf(resp, nil))
                return
            }
            if case .respListOfFiles(let lof) = resp.payload {
                listRetry.reset()
                listingProblem = nil
                error = nil
                outOfImagesSupported = lof.outOfImagesSupported
                folderOutOfImages = lof.folderOutOfImages
                listedPath = asked
                var files = lof.files
                if asked != "/" {
                    var up = Msg_File()
                    up.mime = "inode/directory"
                    up.path = ".."
                    files.insert(up, at: 0)
                }
                rows = files.map(FileRow.init(file:))
                selected.removeAll()
            } else if resp.error {
                folderOutOfImages = false
                listingProblem = nil
                error = resp.errorMessage.isEmpty ? "Failed to list path" : resp.errorMessage
            } else {
                folderOutOfImages = false
                listingProblem = nil
                error = "Unexpected response"
            }
        } catch {
            guard asked == path else { return }
            folderOutOfImages = false
            listingFailed(asked, problemOf(nil, error))
        }
    }

    /// Shows a folder and waits for its listing (the search's pick).
    func show(_ dir: String) async {
        path = normPath(dir)
        await load()
    }

    // MARK: The search's "Search documents" (TopSearch.swift)

    /// Every file and folder whose path holds a text, as SearchFiles finds
    /// them - at most searchLimit - listed over the folder, which stays as
    /// it was underneath. Only the last search asked for lands.
    struct SearchResults {
        enum State: Equatable {
            case loading
            case done
            case failed(String, retry: Bool)
        }
        let text: String
        var state: State
        var files: [Msg_File]
    }
    static let searchLimit = 50
    @Published var results: SearchResults?
    /// The results' photos and videos by their thumbnails.
    @Published private(set) var resultThumbs: [String: FileThumb] = [:]
    private var searchSeq = 0

    func startSearch(_ text: String) async {
        searchSeq += 1
        let mine = searchSeq
        results = SearchResults(text: text, state: .loading, files: [])
        resultThumbs = [:]
        var req = Msg_SearchFiles()
        req.query = text
        req.limit = Int32(Self.searchLimit)
        let resp: Msg_RespEnvelope
        do {
            resp = try await ws.request { $0.payload = .reqSearchFiles(req) }
        } catch {
            guard mine == searchSeq else { return }
            results?.state = .failed("The search didn't reach the device. Check the connection and try again.", retry: true)
            return
        }
        let tooOld = resp.error && resp.errorCode == "unknown_payload"
        // The search stops offering it on such a device.
        if tooOld { FilesNav.shared.deviceCantSearchFiles() }
        guard mine == searchSeq else { return }
        if case .respListOfFiles(let lof) = resp.payload {
            results = SearchResults(text: text, state: .done, files: lof.files)
            await loadResultThumbs(lof.files, seq: mine)
        } else if tooOld {
            results?.state = .failed("This device can't search its files yet. Update it in Settings.", retry: false)
        } else {
            results?.state = .failed(resp.errorMessage.isEmpty ? "The device didn't answer the search." : resp.errorMessage, retry: true)
        }
    }

    /// Back to the folder.
    func closeResults() {
        guard results != nil else { return }
        searchSeq += 1
        results = nil
        resultThumbs = [:]
    }

    /// The results' photos and videos, 24 to a request as the grid asks.
    private func loadResultThumbs(_ files: [Msg_File], seq: Int) async {
        var media = files.filter { !isDirFile($0) && isMediaFile($0) }.map(\.path)
        while !media.isEmpty, seq == searchSeq {
            let batch = Array(media.prefix(24))
            media.removeFirst(batch.count)
            var req = Msg_GetThumbnails()
            req.paths = batch
            // Tiles: small thumbnails (release 111; an older device sends
            // big ones).
            req.smallThumbnails = true
            guard let resp = try? await request(.reqGetThumbnails(req)),
                  case .respListOfFiles(let lof) = resp.payload else { return }
            let got = lof.files.map { (path: $0.path, data: $0.content) }
            let decoded = await Task.detached(priority: .userInitiated) {
                got.map { ($0.path, $0.data, Self.decodeTile($0.data)) }
            }.value
            guard seq == searchSeq else { return }
            for (path, data, img) in decoded {
                if let img { resultThumbs[path] = FileThumb(image: img, data: data) }
            }
        }
    }

    /// The results' photos and videos, for the viewer.
    func resultViewerItems() -> [PhotoGalleryVM.Item] {
        (results?.files ?? []).filter { !isDirFile($0) && isMediaFile($0) }.map { f in
            let thumb = resultThumbs[f.path]
            return PhotoGalleryVM.Item(
                id: "\(f.path)#\(f.hash)#\(f.fileSize)",
                path: f.path,
                mime: f.mime,
                size: Int(f.fileSize),
                thumbData: thumb?.data,
                localURL: nil,
                isLocalOnly: false,
                thumbImage: thumb?.image
            )
        }
    }

    func thumbKey(for row: FileRow) -> String { fullPath(for: row) + "\u{0}" + row.raw.hash }

    /// Whether the grid should show this row as a thumbnail at all.
    func isMedia(_ row: FileRow) -> Bool { !row.isDir && isMediaFile(row.raw) }
    func isVideo(_ row: FileRow) -> Bool { !row.isDir && isVideoFile(row.raw) }

    /// Called as a grid cell appears: queues its thumbnail unless it is
    /// cached, known to have none, or already on its way.
    func wantThumbnail(for row: FileRow) {
        guard isMedia(row) else { return }
        let key = thumbKey(for: row)
        visibleThumbs.insert(key)
        if thumbs[key] == nil, let cached = thumbCache.object(forKey: key as NSString) {
            objectWillChange.send()
            thumbs[key] = cached
            return
        }
        guard thumbs[key] == nil, !noThumb.contains(key), !thumbInFlight.contains(key) else { return }
        thumbInFlight.insert(key)
        thumbQueue.append((key, fullPath(for: row), path))
        guard !thumbPumping else { return }
        thumbPumping = true
        Task { await pumpThumbnails() }
    }

    /// Called as a grid cell disappears: its image moves to the bounded
    /// cache. Evicted there, it is asked for again when the cell returns.
    func thumbGone(for row: FileRow) {
        guard isMedia(row) else { return }
        let key = thumbKey(for: row)
        visibleThumbs.remove(key)
        if let thumb = thumbs.removeValue(forKey: key) {
            thumbCache.setObject(thumb, forKey: key as NSString, cost: thumb.cost)
        }
    }

    /// A tile is at most ~200 pt and scaledToFill only needs the short
    /// side to cover it: decoded with that side at 600 px (3x), never
    /// above the device's own size - a fraction of the 1000 px original's
    /// memory for a photo, all of it for a wide one, whose short side is
    /// already smaller. Decoded here, off the main thread, not lazily when
    /// first drawn.
    nonisolated static func decodeTile(_ data: Data) -> UIImage? {
        GridThumbCache.decode(data: data, localURL: nil, maxPt: 200)
    }

    /// Sends the queue to the device in batches of 24 (it takes at most 48
    /// and may stop early around 8 MB, so 24 stays clear of both). Anything
    /// it doesn't answer has no thumbnail and keeps the type icon. Results
    /// for a folder the user already left are dropped.
    private func pumpThumbnails() async {
        defer { thumbPumping = false }
        while true {
            // Cells of a folder the user already left don't need theirs.
            for item in thumbQueue where item.folder != path { thumbInFlight.remove(item.key) }
            thumbQueue.removeAll { $0.folder != path }
            guard !thumbQueue.isEmpty else { return }
            let folder = path
            let batch = Array(thumbQueue.prefix(24))
            thumbQueue.removeFirst(batch.count)
            var req = Msg_GetThumbnails()
            req.paths = batch.map(\.path)
            // The tiles' small thumbnails (release 111; an older device
            // sends big ones). The viewer shows them only until the full
            // size arrives, and asks for the big one itself where it would
            // stay (PhotoGalleryVM.thumbnailStays).
            req.smallThumbnails = true
            let resp = try? await request(.reqGetThumbnails(req))
            for item in batch { thumbInFlight.remove(item.key) }
            guard folder == path else { continue }
            // A failed request (no connection) remembers nothing, so the
            // cells ask again the next time they appear.
            guard let resp, case .respListOfFiles(let lof) = resp.payload else { continue }
            var got: [String: Data] = [:]
            for f in lof.files { got[f.path] = f.content }
            let wanted = batch.map { (key: $0.key, data: got[$0.path]) }
            let decoded = await Task.detached(priority: .userInitiated) {
                wanted.map { ($0.key, $0.data, $0.data.flatMap(Self.decodeTile)) }
            }.value
            var fresh: [String: FileThumb] = [:]
            for (key, data, img) in decoded {
                guard let data, let img else {
                    noThumb.insert(key)
                    continue
                }
                thumbBytes.setObject(data as NSData, forKey: key as NSString, cost: data.count)
                let thumb = FileThumb(image: img, data: data)
                // A cell that scrolled away meanwhile: straight to the cache.
                if visibleThumbs.contains(key) {
                    fresh[key] = thumb
                } else {
                    thumbCache.setObject(thumb, forKey: key as NSString, cost: thumb.cost)
                }
            }
            // One change for the batch, not one redraw per tile.
            if !fresh.isEmpty {
                objectWillChange.send()
                thumbs.merge(fresh) { $1 }
            }
        }
    }

    func navigate(to newPath: String) {
        path = normPath(newPath)
        Task { await load() }
    }

    func fullPath(for row: FileRow) -> String {
        row.path.contains("/") ? row.path : joinPath(path, row.path)
    }

    /// The folder's photos and videos as the viewer's items, in the order
    /// the list and grid show them, each with the grid's thumbnail if it
    /// has one (the viewer fetches the full-size image either way): the
    /// device's bytes, which every tile keeps with it.
    func viewerItems() -> [PhotoGalleryVM.Item] {
        rows.filter(isMedia).map { row in
            let full = fullPath(for: row)
            let key = thumbKey(for: row) as NSString
            let thumb = thumbs[key as String] ?? thumbCache.object(forKey: key)
            return PhotoGalleryVM.Item(
                id: "\(full)#\(row.raw.hash)#\(row.size)",
                path: full,
                mime: row.raw.mime,
                size: Int(row.size),
                thumbData: thumb?.data ?? thumbBytes.object(forKey: key) as Data?,
                localURL: nil,
                isLocalOnly: false,
                thumbImage: thumb?.image
            )
        }
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
        do {
            // Issue #72: QuickLook (the same previewer Mail/Files use for
            // attachments) natively renders PDFs, Office docs, text, audio
            // and video, not just images - writing to a temp file first
            // (rather than the old image-only UIImage(data:) path) is what
            // lets it identify the format at all, same as it would from a
            // Files app download. Its own toolbar already has a share
            // button, so this replaces the separate share-sheet fallback
            // for non-images too, not just adds preview alongside it.
            // In 4 MiB pieces straight to the file, never whole in memory.
            let tmp = FileManager.default.temporaryDirectory.appendingPathComponent(leafName(row.path))
            try await FileDownload.download(path: full, mime: row.raw.mime, to: tmp)
            previewURL = tmp
        } catch is FileDownload.Refused {
            showToast("Could not fetch file")
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
        if let resp = try? await request(.reqSetUploadOnly(req)), resp.error {
            showToast(resp.errorMessage.isEmpty ? "Could not update the folder" : resp.errorMessage)
        }
        await load()
    }

    /// Issue #192: keeps a folder out of Images (`keepOut`), or shows it
    /// there again - asked first (OutOfImagesText.promptTitle). The device
    /// answers once the tags and faces found only in it are gone, so Images,
    /// its tags and People are asked for again at once (otcImagesChanged).
    /// A refusal says why in the device's words: showing a folder inside
    /// another one kept out names that one (out_of_images_by_parent).
    /// One at a time: while one is under way every way to start another is
    /// disabled (outOfImagesIdle), so this only drops a tap racing that.
    func setOutOfImages(_ folder: String, keepOut: Bool) async {
        guard outOfImagesIdle else { return }
        outOfImagesBusy = Self.folderKey(folder)
        defer { outOfImagesBusy = nil }
        var req = Msg_SetOutOfImages()
        req.path = folder
        req.outOfImages = keepOut
        do {
            let resp = try await request(.reqSetOutOfImages(req))
            if case .respAck(let ack) = resp.payload, ack.ok, !resp.error {
                NotificationCenter.default.post(name: .otcImagesChanged, object: nil)
            } else {
                showToast(resp.errorMessage.isEmpty ? "Could not update the folder" : resp.errorMessage)
            }
        } catch {
            showToast("Could not update the folder")
        }
        await load()
    }

    /// A folder row, as the keep-out-of-Images prompt asks about it.
    func outOfImagesAsk(_ row: FileRow) -> OutOfImagesAsk {
        OutOfImagesAsk(path: fullPath(for: row), name: row.name, outOfImages: row.outOfImages)
    }

    /// The folder being browsed, as the banner's "Show in Images" asks
    /// about it: its full path (no trailing slash, as a row's) and name.
    var currentFolder: OutOfImagesAsk {
        OutOfImagesAsk(path: Self.folderKey(path), name: path == "/" ? "/" : leafName(path), outOfImages: folderOutOfImages)
    }

    /// The banner shows over the folder it was listed for only: right after
    /// moving to another folder `path` is already the new one while
    /// folderOutOfImages is still the old one's.
    var outOfImagesBanner: Bool {
        outOfImagesSupported && folderOutOfImages && listedPath == path
    }

    /// No switch under way: the switches are enabled.
    var outOfImagesIdle: Bool { outOfImagesBusy == nil }

    /// Whether `folder` - a row's full path, or the folder browsed with its
    /// trailing slash - is the one whose switch is under way.
    func isOutOfImagesBusy(_ folder: String) -> Bool {
        outOfImagesBusy != nil && outOfImagesBusy == Self.folderKey(folder)
    }

    /// A folder's path without its trailing slash ("/" stays "/"), so the
    /// folder browsed ("/x/sub/") and its row ("/x/sub") compare equal.
    nonisolated static func folderKey(_ folder: String) -> String {
        var s = folder
        while s.count > 1 && s.hasSuffix("/") { s.removeLast() }
        return s
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
        do {
            let tmp = FileManager.default.temporaryDirectory.appendingPathComponent(leafName(row.path))
            try await FileDownload.download(path: fullPath(for: row), hash: hash, mime: row.raw.mime, to: tmp)
            versionsOf = nil
            previewURL = tmp
        } catch is FileDownload.Refused {
            showToast("Could not fetch that version")
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

    /// Streams the file: hashed off the main actor in 4 MiB pieces, then
    /// sent chunk by chunk. Reading it whole (and hashing it here) put
    /// every picked file in memory at once and froze the UI - a few large
    /// videos and iOS killed the app.
    func upload(fileAt url: URL, filename: String) async {
        let path = joinPath(path, filename)
        // Unreadable (or a folder): skipped without a word, as before.
        let hash: String
        do {
            hash = try await Task.detached(priority: .userInitiated) { try OTCConnection.sha256Hex(of: url) }.value
        } catch {
            return
        }
        do {
            // Issue #58: skip re-sending content the device already has
            // under some other path — see PhotoSync.swift's identical
            // check for the full reasoning.
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
                // Issue #165: chunked, never the whole file in one message.
                resp = try await ws.uploadChunked(path: path, source: .file(url),
                                                  forceOverride: false, sha256: hash)
            }
            if resp.error { showToast("Upload failed: \(resp.errorMessage)") }
        } catch {
            showToast("Upload failed: \(error.localizedDescription)")
        }
        await load()
    }

    /// Says a toast to VoiceOver, which never moves to it on its own - a
    /// refused switch (out_of_images_by_parent, locked_by_parent) would
    /// look as if it did nothing. Tests listen in its place.
    var announce: @MainActor (String) -> Void = { AccessibilityNotification.Announcement($0).post() }

    func showToast(_ m: String) {
        toast = m
        announce(m)
        // Long enough to read: 2.5 s, longer for a sentence (issue #192's
        // refusal names two folders).
        let seconds = max(2.5, Double(m.count) / 18)
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
            if self?.toast == m { self?.toast = nil }
        }
    }
}

struct FilesExplorerView: View {
    @StateObject private var vm: FilesExplorerViewModel
    // Photos and videos open in the Images section's viewer, over this
    // folder's photos and videos only (see PhotoGalleryVM.showFiles).
    @StateObject private var viewer = PhotoGalleryVM()
    @State private var pathField: String
    @State private var showImporter = false
    // Issue #180: set to start the "Share as Gallery" flow.
    @State private var gallerySource: Msg_SharedGallerySource?
    // The lock (issue #132): a tap asks first, saying what upload only
    // does - there is no hover tooltip here as on the web - and a lock that
    // only marks something explains itself.
    @State private var lockPrompt: FileRow?
    @State private var lockInfo: String?
    // Issue #192: keeping a folder out of Images asks first too (it deletes
    // the tags and faces found in it), and the mark on a folder kept out
    // says what that means, with the way back.
    @State private var outOfImagesPrompt: OutOfImagesAsk?
    @State private var outOfImagesInfo: OutOfImagesAsk?
    // List or grid ("list"/"grid"), remembered across launches - the web and
    // Android explorers keep theirs under the same key.
    @AppStorage("files.viewMode") private var viewMode = "list"
    // The Images search sends Files to a folder, a file in it, or a list
    // of results (TopSearch.swift's FilesNav).
    @ObservedObject private var nav = FilesNav.shared
    @State private var handledRequest: UUID?
    // Files' own search, files and folders only (FilesSearch.swift): at
    // the top in the narrow layout; a wide window's top bar searches Files
    // already. Its words are its own, kept while the window is wide - or
    // the window's, handed in, for its words to cross to the top bar and
    // back as the window turns (FilesSearchHandOff).
    @Environment(\.wideLayout) private var wide
    @StateObject private var fileSearch: TopSearchModel
    @FocusState private var searchFocused: Bool
    /// The field's foot, where its panel starts, and whether the keyboard
    /// shows: the panel may rise over the field (SearchPanelPlace).
    @State private var searchBottom: CGFloat = 0
    @State private var keyboardUp = false
    private static let bodySpace = "filesBody"
    // Selection is managed here rather than with List(selection:) +
    // EditButton(): that pairing needs the row's tap to be the List's own
    // selection-toggle handling, and this view also needs a tap to open
    // the file, which intercepts the touch first and left "Edit" mode
    // silently doing nothing. There is no select mode any more either -
    // every row carries its own checkbox all the time (the web's table
    // has had one per row from the start), tapping the checkbox selects
    // and tapping the rest of the row opens, so nothing has to be
    // switched on before something can be picked.

    /// `search`: Files' own search, held by the window (MainView) so that
    /// its words can go to the top bar as the window turns wide and come
    /// back (FilesSearchHandOff). Without it Files keeps its own.
    init(initialPath: String, search: TopSearchModel? = nil) {
        _vm = StateObject(wrappedValue: FilesExplorerViewModel(initialPath: initialPath))
        _pathField = State(initialValue: initialPath)
        _fileSearch = StateObject(wrappedValue: search ?? TopSearchModel(scope: .files))
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                // Files' own search (FilesSearch.swift), over the folder
                // and over a search's results alike: a row of its own above
                // the path, which keeps its place and its controls.
                if searchShown {
                    FilesSearchField(search: fileSearch, focused: $searchFocused, actions: searchActions)
                        .padding(.horizontal)
                        .padding(.top, 8)
                        // Clear of an iPad window's controls (AppMenu.swift).
                        .modifier(AvoidsWindowControls())
                        .onGeometryChange(for: CGFloat.self) { proxy in
                            proxy.frame(in: .named(Self.bodySpace)).maxY
                        } action: { searchBottom = $0 }
                }
                // Under the search's suggestions, the folder or the results
                // are out of VoiceOver's reach too, as out of sight; the field
                // and its Cancel stay.
                Group {
                    // The search's "Search documents": its results over the
                    // folder, which stays as it was underneath.
                    if let results = vm.results {
                        resultsView(results)
                    } else {
                        HStack {
                            TextField("/path/", text: $pathField, onCommit: { vm.navigate(to: pathField) })
                                .textFieldStyle(.roundedBorder)
                                .autocapitalization(.none)
                            if vm.loading { ProgressView() }
                            // Shows the mode a tap switches to, like Files/Photos.
                            Button {
                                viewMode = viewMode == "grid" ? "list" : "grid"
                            } label: {
                                Image(systemName: viewMode == "grid" ? "list.bullet" : "square.grid.2x2")
                                    .frame(width: 28, height: 28)
                                    .contentShape(Rectangle())
                            }
                            .accessibilityLabel(viewMode == "grid" ? "Show as list" : "Show as grid")
                        }
                        .padding(.horizontal)
                        // Closer under the search field than under the bar.
                        .padding(.top, searchShown ? 8 : nil)
                        // Clear of an iPad window's controls (AppMenu.swift).
                        .modifier(AvoidsWindowControls())

                        if let error = vm.error {
                            Text(error).font(.caption).foregroundColor(.red).padding(.horizontal)
                                // Said to VoiceOver when it appears or
                                // changes (Android's polite live region).
                                .onAppear { Announce.polite(error) }
                                .onChange(of: error) { _, e in Announce.polite(e) }
                        }

                        if vm.outOfImagesBanner {
                            outOfImagesBanner
                        }

                        if viewMode == "grid" {
                            grid
                        } else {
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
                                        if vm.openingPath == row.path || (row.isDir && vm.isOutOfImagesBusy(vm.fullPath(for: row))) {
                                            ProgressView().frame(width: 20)
                                        } else {
                                            Image(systemName: row.isDir ? "folder.fill" : (isImgFile(row.raw) ? "photo" : "doc"))
                                                .foregroundColor(row.isDir ? .accentColor : .secondary)
                                                .frame(width: 20)
                                        }
                                        VStack(alignment: .leading) {
                                            Text(row.name).lineLimit(1)
                                            if !row.isDir {
                                                Text(formatByteCount(row.size)).font(.caption2).foregroundColor(.secondary)
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
                                        // Issue #192: a folder kept out of Images says
                                        // so; a tap says what that means. Changed from
                                        // the long-press menu. Before the lock, so
                                        // the locks stay in one column.
                                        if row.path != ".." && row.isDir && row.outOfImages && vm.outOfImagesSupported {
                                            Button {
                                                outOfImagesInfo = vm.outOfImagesAsk(row)
                                            } label: {
                                                Image(systemName: "eye.slash.fill")
                                                    .foregroundColor(.accentColor)
                                                    .frame(width: 28, height: 28)
                                                    .contentShape(Rectangle())
                                            }
                                            .buttonStyle(.plain)
                                            .accessibilityLabel(OutOfImagesText.state)
                                        }
                                        if row.path != ".." && row.isDir {
                                            Button {
                                                lockPrompt = row
                                            } label: {
                                                Image(systemName: row.uploadOnly ? "lock.fill" : "lock.open")
                                                    .foregroundColor(row.uploadOnly ? .accentColor : .secondary)
                                                    .frame(width: 28, height: 28)
                                                    .contentShape(Rectangle())
                                            }
                                            .buttonStyle(.plain)
                                            .accessibilityLabel(row.uploadOnly ? "Clear upload only" : "Make upload only")
                                        } else if row.uploadOnly {
                                            Button {
                                                lockInfo = UploadOnlyText.fileInfo
                                            } label: {
                                                Image(systemName: "lock.fill").foregroundColor(.secondary).frame(width: 28, height: 28)
                                                    .contentShape(Rectangle())
                                            }
                                            .buttonStyle(.plain)
                                            .accessibilityLabel("In an upload-only folder")
                                        }
                                    }
                                    .contentShape(Rectangle())
                                    .onTapGesture { tap(row) }
                                    // Long-press: see rowMenu.
                                    .contextMenu { rowMenu(row) }
                                }
                            }
                            .listStyle(.plain)
                            // Issue #51: pull down to re-list this directory - files
                            // arrive from other clients (the Mac app, another phone)
                            // while this screen sits open, and nothing else re-reads it.
                            .refreshable { await vm.load() }
                        }

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
                }
                .accessibilityHidden(searchPanelShown)
            }
            .coordinateSpace(.named(Self.bodySpace))
            // The search's suggestions, over the folder (or the results)
            // while something is typed, as Images' over its photos.
            .overlay {
                if searchPanelShown {
                    FilesSearchPanel(search: fileSearch, actions: searchActions, under: searchBottom + 8, keyboardUp: keyboardUp)
                }
            }
            // No nav title (issue #19): the tab bar already labels this
            // screen "Files", and the path field above already shows where
            // you are. Still .inline so there's no big empty title bar.
            .navigationBarTitleDisplayMode(.inline)
            .overlay(alignment: .top) {
                if let toast = vm.toast {
                    // A device's refusal is a whole sentence: on lines of
                    // its own, clear of the screen's edges.
                    Text(toast)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 14).padding(.vertical, 8)
                        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                        .padding(.horizontal)
                        .padding(.top, 8)
                }
            }
        }
        .task { await vm.load() }
        // A request already waiting when Files is first shown comes too:
        // the publisher starts with what it holds. Taken on the next turn,
        // not while the view is being updated.
        .onReceive(nav.$pending.compactMap { $0 }.receive(on: RunLoop.main)) { handle($0) }
        // Files picked in the tab bar or the wide layout's menu: the
        // folder, not results, nor the suggestions of a search left open.
        .onChange(of: nav.leftSearch) { _, _ in
            vm.closeResults()
            if fileSearch.open { fileSearch.open = false }
        }
        // Files' own search (FilesSearch.swift).
        .keyboardShown($keyboardUp)
        .onChange(of: searchFocused) { _, focused in
            if focused { fileSearch.open = true }
        }
        .onChange(of: fileSearch.query) { _, _ in
            if searchFocused && !fileSearch.typed.isEmpty { fileSearch.open = true }
        }
        // Turned to the wide layout the field goes, and its panel and the
        // keyboard with it; turned back it is there, its panel closed, with
        // its words (or the top bar's: FilesSearchHandOff).
        .onChange(of: wide) { _, _ in searchActions.layoutChanged() }
        .sharedGalleryShareFlow(source: $gallerySource)
        .onChange(of: vm.path) { _, newValue in pathField = newValue }
        .fileImporter(isPresented: $showImporter, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            guard case .success(let urls) = result else { return }
            for url in urls {
                guard url.startAccessingSecurityScopedResource() else { continue }
                // Kept open until this file's upload is done: it is read
                // as it is sent now.
                Task {
                    defer { url.stopAccessingSecurityScopedResource() }
                    await vm.upload(fileAt: url, filename: url.lastPathComponent)
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
                                    Text(formatByteCount(row.size)).foregroundColor(.secondary)
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
                                        Text(formatByteCount(v.fileSize)).foregroundColor(.secondary)
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
        .alert(
            lockPrompt.map { UploadOnlyText.promptTitle(name: $0.name, uploadOnly: $0.uploadOnly) } ?? "",
            isPresented: Binding(get: { lockPrompt != nil }, set: { if !$0 { lockPrompt = nil } }),
            presenting: lockPrompt
        ) { row in
            Button(row.uploadOnly ? "Allow Deletions" : "Make Upload Only") { Task { await vm.toggleUploadOnly(row) } }
            Button("Cancel", role: .cancel) {}
        } message: { row in
            Text(row.uploadOnly ? UploadOnlyText.clearMessage : UploadOnlyText.makeMessage)
        }
        .alert("Upload only", isPresented: Binding(get: { lockInfo != nil }, set: { if !$0 { lockInfo = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(lockInfo ?? "")
        }
        // Issue #192: keep a folder out of Images, or show it there again.
        .alert(
            outOfImagesPrompt.map { OutOfImagesText.promptTitle(name: $0.name, outOfImages: $0.outOfImages) } ?? "",
            isPresented: Binding(get: { outOfImagesPrompt != nil }, set: { if !$0 { outOfImagesPrompt = nil } }),
            presenting: outOfImagesPrompt
        ) { ask in
            Button(ask.outOfImages ? OutOfImagesText.show : OutOfImagesText.keep) {
                Task { await vm.setOutOfImages(ask.path, keepOut: !ask.outOfImages) }
            }
            // One switch at a time (setOutOfImages).
            .disabled(!vm.outOfImagesIdle)
            Button("Cancel", role: .cancel) {}
        } message: { ask in
            Text(ask.outOfImages ? OutOfImagesText.showMessage : OutOfImagesText.keepMessage)
        }
        // The mark on a folder kept out: what it means, and the way back
        // (asked, as from the menu).
        .alert(
            OutOfImagesText.state,
            isPresented: Binding(get: { outOfImagesInfo != nil }, set: { if !$0 { outOfImagesInfo = nil } }),
            presenting: outOfImagesInfo
        ) { ask in
            Button(OutOfImagesText.show) { outOfImagesPrompt = ask }
                .disabled(!vm.outOfImagesIdle)
            Button("OK", role: .cancel) {}
        } message: { _ in
            Text(OutOfImagesText.explain)
        }
        .confirmationDialog(
            "Delete \(vm.selected.count) item\(vm.selected.count == 1 ? "" : "s")?",
            isPresented: $vm.confirmDeleteSelected,
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) { Task { await vm.deleteSelected() } }
            Button("Cancel", role: .cancel) {}
        }
        // Presented exactly as PhotoGalleryView presents it.
        .fullScreenCover(isPresented: Binding(
            get: { viewer.openIndex != nil },
            set: { if !$0 { viewer.closeModal() } }
        )) {
            ImageModal(
                vm: viewer,
                save: {
                    let fallback = viewerThumb()
                    Task { viewer.saveToPhotos(await viewer.fullImageForOpen() ?? fallback) }
                },
                share: {
                    let fallback = viewerThumb()
                    Task { viewer.shareCurrentPhoto(await viewer.fullImageForOpen() ?? fallback) }
                },
                delete: { viewer.deleteCurrentPhoto() }
            )
        }
    }

    // MARK: The search (TopSearch.swift)

    /// What the search - Images' field or the top bar - sent Files to do:
    /// show a folder, open a file in it as a tap there would (a photo or
    /// video once the folder is listed, to page through its photos and
    /// videos; a document at once), or list every file and folder a text
    /// finds (go).
    private func handle(_ request: FilesNav.Request) {
        nav.take(request)
        // Once each, however often the publisher hands it over.
        guard handledRequest != request.id else { return }
        handledRequest = request.id
        // Sent by the other search: Files' own suggestions make way.
        if fileSearch.open { fileSearch.open = false }
        go(request.kind)
    }

    // MARK: Files' own search (FilesSearch.swift)

    /// The field shows: the narrow layout, on a device that can search
    /// its files.
    private var searchShown: Bool { FilesSearchActions.shown(wide: wide, noFileSearch: nav.noFileSearch) }

    /// Something is typed and the panel is up, over what is under the field.
    private var searchPanelShown: Bool {
        FilesSearchActions.covers(wide: wide, noFileSearch: nav.noFileSearch, open: fileSearch.open, typed: fileSearch.typed)
    }

    private var searchActions: FilesSearchActions {
        FilesSearchActions(
            search: fileSearch,
            noFileSearch: nav.noFileSearch,
            focus: { searchFocused = $0 },
            go: go
        )
    }

    /// Shows what a search picked, from either search: a folder, a file in
    /// its folder as a tap there would open it, or every file and folder
    /// the words find, listed over the folder.
    private func go(_ kind: FilesNav.Request.Kind) {
        switch kind {
        case .search(let text):
            guard !text.isEmpty else { return }
            Task { await vm.startSearch(text) }
        case .folder(let dir, let file):
            vm.closeResults()
            let row = file.map(FileRow.init(file:))
            if let row, !vm.isMedia(row), !row.isDir {
                Task { await vm.open(row) }
            }
            Task {
                await vm.show(dir)
                guard let file, let row, vm.isMedia(row) else { return }
                // Somewhere else by now: not over that.
                guard vm.path == normPath(dir), vm.results == nil else { return }
                let items = vm.viewerItems()
                viewer.onDeleted = { _ in Task { await vm.load() } }
                if let start = items.firstIndex(where: { $0.path == file.path }) {
                    viewer.showFiles(items, startAt: start)
                } else {
                    // Not in the listing (it failed, or the file went): the file alone.
                    viewer.showFiles([PhotoGalleryVM.Item(
                        id: "\(file.path)#\(file.hash)", path: file.path, mime: file.mime,
                        size: Int(file.fileSize), thumbData: nil, localURL: nil, isLocalOnly: false
                    )], startAt: 0)
                }
            }
        }
    }

    /// Where Back goes: the folder under the results.
    private var folderLabel: String {
        vm.path == "/" ? "Files" : leafName(vm.path)
    }

    private func resultsView(_ r: FilesExplorerViewModel.SearchResults) -> some View {
        let q = FoldedText(r.text)
        let count = r.files.count
        return VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 2) {
                Button {
                    vm.closeResults()
                } label: {
                    Label("Back to \(folderLabel)", systemImage: "arrow.left")
                        .font(.subheadline.weight(.medium))
                        .lineLimit(1)
                        // A finger's worth of height, not just the text's.
                        .frame(minHeight: 44)
                        .contentShape(Rectangle())
                }
                Text("Files matching \u{201C}\(r.text)\u{201D}")
                    .font(.title3.weight(.semibold))
                    .accessibilityAddTraits(.isHeader)
                // Its line kept while searching, so the list doesn't move.
                Text(r.state == .done && count > 0
                     ? (count == FilesExplorerViewModel.searchLimit
                        ? "Showing the first \(count) results"
                        : "\(count) \(count == 1 ? "result" : "results")")
                     : " ")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            .padding(.horizontal)
            .padding(.top, 4)
            .padding(.bottom, 8)
            .modifier(AvoidsWindowControls())

            switch r.state {
            case .loading:
                HStack(spacing: 8) {
                    ProgressView()
                    Text("Searching…").foregroundStyle(.secondary)
                }
                .padding()
                Spacer()
            case .failed(let message, let retry):
                VStack(alignment: .leading, spacing: 10) {
                    Text(message).foregroundStyle(.secondary)
                    if retry {
                        Button("Try Again") { Task { await vm.startSearch(r.text) } }
                            .buttonStyle(.bordered)
                    }
                }
                .padding()
                Spacer()
            case .done where r.files.isEmpty:
                Text("No files match \u{201C}\(r.text)\u{201D}.")
                    .foregroundStyle(.secondary)
                    .padding()
                Spacer()
            case .done:
                List {
                    ForEach(r.files, id: \.path) { f in
                        resultRow(f, q: q)
                    }
                }
                .listStyle(.plain)
                .refreshable { await vm.startSearch(r.text) }
            }
        }
        // The whole width, from the leading edge, while it searches too:
        // with nothing as wide as the screen yet it used to sit centred.
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func resultRow(_ f: Msg_File, q: FoldedText) -> some View {
        let row = FileRow(file: f)
        let (name, dir) = FilePaths.parts(f.path)
        let nameSpan = SearchMatch.match(FoldedText(name), q)?.span
        let dirSpan = nameSpan == nil ? SearchMatch.match(FoldedText(dir), q)?.span : nil
        let thumb = vm.isMedia(row) ? vm.resultThumbs[f.path]?.image : nil
        return HStack(spacing: 12) {
            ZStack {
                if vm.openingPath == f.path {
                    ProgressView()
                } else if row.isDir {
                    Image(systemName: "folder.fill")
                        .font(.title2)
                        .foregroundColor(.accentColor)
                } else if let thumb {
                    Color.clear
                        .overlay(Image(uiImage: thumb).resizable().scaledToFill())
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                        .overlay(alignment: .bottomLeading) {
                            if vm.isVideo(row) {
                                Image(systemName: "play.fill")
                                    .font(.system(size: 7))
                                    .foregroundColor(.white)
                                    .padding(4)
                                    .background(Color.black.opacity(0.55), in: Circle())
                                    .padding(2)
                            }
                        }
                } else {
                    FileTypeIcon(name: row.name)
                }
            }
            .frame(width: 40, height: 40)
            VStack(alignment: .leading, spacing: 2) {
                Text(SearchMatch.marked(name, nameSpan)).lineLimit(1).truncationMode(.middle)
                // Long folders lose their start, not the end nearest the file.
                Text(SearchMatch.marked(dir, dirSpan))
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .lineLimit(1)
                    .truncationMode(.head)
            }
            Spacer(minLength: 0)
            if !row.isDir {
                Text(formatByteCount(row.size)).font(.caption2).foregroundColor(.secondary)
            }
            // Issue #192: found in (or as) a folder kept out of Images.
            // Only a mark: the folder's menu changes it.
            if row.outOfImages {
                Image(systemName: "eye.slash.fill")
                    .font(.caption)
                    .foregroundColor(.accentColor)
            }
        }
        .contentShape(Rectangle())
        .onTapGesture { openFound(f) }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(name), \(FoundKind(f).word) in \(dir)\(row.outOfImages ? ", kept out of Images" : "")")
        .accessibilityAddTraits(.isButton)
    }

    /// A found folder opens in Files; a photo or video in the viewer,
    /// paging through the results' photos and videos; anything else as a
    /// tap on it in its folder would.
    private func openFound(_ f: Msg_File) {
        let row = FileRow(file: f)
        if row.isDir {
            vm.closeResults()
            vm.navigate(to: FilePaths.asFolder(f.path))
            return
        }
        guard vm.openingPath == nil else { return }
        if vm.isMedia(row) {
            let items = vm.resultViewerItems()
            guard let start = items.firstIndex(where: { $0.path == f.path }) else { return }
            let text = vm.results?.text ?? ""
            // A delete from the viewer asks the device again.
            viewer.onDeleted = { _ in Task { await vm.startSearch(text) } }
            viewer.showFiles(items, startAt: start)
            return
        }
        Task { await vm.open(row) }
    }

    /// The open item's grid thumbnail, standing in for save/share until
    /// the full-size image arrives (as the Images section does).
    private func viewerThumb() -> UIImage? {
        guard let i = viewer.openIndex, viewer.items.indices.contains(i) else { return nil }
        return viewer.items[i].thumbData.flatMap(UIImage.init(data:)) ?? viewer.items[i].thumbImage
    }

    /// What a tap on a row (or a grid tile) does: open the folder or file.
    /// A photo or video opens in the Images section's viewer, starting at
    /// it and swiping through the folder's other photos and videos.
    private func tap(_ row: FileRow) {
        // Not while another file's open is in flight (issue #71): its
        // Quick Look sheet couldn't show over the viewer.
        if vm.isMedia(row) {
            guard vm.openingPath == nil else { return }
            let items = vm.viewerItems()
            guard let start = items.firstIndex(where: { $0.path == vm.fullPath(for: row) }) else { return }
            // A delete from the viewer re-reads the folder.
            viewer.onDeleted = { _ in Task { await vm.load() } }
            viewer.showFiles(items, startAt: start)
            return
        }
        // Issue #71: ignore taps while any row's open is already in flight -
        // see openingPath's own doc comment for why that's the fix, not
        // just the spinner on the row.
        if vm.openingPath == nil {
            Task { await vm.open(row) }
        }
    }

    // Long-press for quick Share/Delete on a single file, independent of
    // (and without needing) a selection - the standard iOS pattern for
    // one-off actions on a single item. Directories get it too since issue
    // #116 (the device expands one to its files); only ".." is left out.
    // Shared by the list's rows and the grid's tiles.
    @ViewBuilder
    private func rowMenu(_ row: FileRow) -> some View {
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
                // Issue #180: the folder's photos and
                // videos as a gallery behind a link.
                Button {
                    gallerySource = .directory(vm.fullPath(for: row))
                } label: {
                    Label("Share as Gallery", systemImage: "photo.on.rectangle.angled")
                }
                Button {
                    lockPrompt = row
                } label: {
                    Label(row.uploadOnly ? "Clear upload only" : "Make upload only", systemImage: row.uploadOnly ? "lock.open" : "lock")
                }
                // Issue #192: not on a device too old to do it, and greyed
                // out while another folder's switch is under way.
                if vm.outOfImagesSupported {
                    Button {
                        outOfImagesPrompt = vm.outOfImagesAsk(row)
                    } label: {
                        Label(row.outOfImages ? OutOfImagesText.show : OutOfImagesText.keep,
                              systemImage: row.outOfImages ? "eye" : "eye.slash")
                    }
                    .disabled(!vm.outOfImagesIdle)
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

    /// Issue #192: the folder being browsed is kept out of Images (itself,
    /// or inside one that is) - the way back is here too. Showing a folder
    /// inside another one kept out is refused, and the device's reply says
    /// which one to show first.
    private var outOfImagesBanner: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "eye.slash.fill")
                .foregroundColor(.accentColor)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 4) {
                Text(OutOfImagesText.banner)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                if vm.isOutOfImagesBusy(vm.path) {
                    ProgressView().controlSize(.small)
                } else {
                    // Greyed out while another folder's switch is under way.
                    Button(OutOfImagesText.show) { outOfImagesPrompt = vm.currentFolder }
                        .font(.footnote.weight(.semibold))
                        .buttonStyle(.borderless)
                        .disabled(!vm.outOfImagesIdle)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .background(Color.accentColor.opacity(0.1), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .padding(.horizontal)
        .padding(.top, 8)
        .accessibilityElement(children: .contain)
    }

    private var grid: some View {
        ScrollView {
            LazyVGrid(columns: [GridItem(.adaptive(minimum: 104), spacing: 10)], spacing: 14) {
                ForEach(vm.rows) { row in
                    VStack(spacing: 4) {
                        tile(row)
                        Text(row.name)
                            .font(.caption)
                            .lineLimit(2)
                            .truncationMode(.middle)
                            .multilineTextAlignment(.center)
                            .frame(maxWidth: .infinity, alignment: .top)
                    }
                    .frame(maxHeight: .infinity, alignment: .top)
                    .contentShape(Rectangle())
                    .onTapGesture { tap(row) }
                    .contextMenu { rowMenu(row) }
                    // Lazily: only tiles that scroll into view ask the
                    // device for their thumbnail.
                    .onAppear { vm.wantThumbnail(for: row) }
                    .onDisappear { vm.thumbGone(for: row) }
                }
            }
            .padding()
        }
        // Issue #51, same as the list: pull down to re-list.
        .refreshable { await vm.load() }
    }

    /// A grid tile, like the Files app's: the folder glyph, the photo or
    /// video's thumbnail, or a FileTypeIcon, with the list row's selection
    /// circle, lock and versions count as small badges on its corners.
    private func tile(_ row: FileRow) -> some View {
        let thumb = vm.isMedia(row) ? vm.thumbs[vm.thumbKey(for: row)]?.image : nil
        return RoundedRectangle(cornerRadius: 10)
            .fill(Color(.secondarySystemBackground))
            .aspectRatio(1, contentMode: .fit)
            .overlay {
                if row.isDir {
                    Image(systemName: "folder.fill")
                        .resizable()
                        .scaledToFit()
                        .foregroundColor(.accentColor)
                        .padding(22)
                } else if let thumb {
                    // Color.clear sizes it to the tile, so scaledToFill
                    // crops instead of growing the tile.
                    Color.clear
                        .overlay(Image(uiImage: thumb).resizable().scaledToFill())
                        .clipped()
                } else {
                    FileTypeIcon(name: row.name).padding(16)
                }
            }
            .clipShape(RoundedRectangle(cornerRadius: 10))
            .overlay {
                // Issue #71: the same in-flight spinner the row shows.
                if vm.openingPath == row.path || (row.isDir && vm.isOutOfImagesBusy(vm.fullPath(for: row))) {
                    ProgressView()
                        .padding(8)
                        .background(.ultraThinMaterial, in: Circle())
                }
            }
            .overlay(alignment: .topLeading) {
                if row.path != ".." {
                    Button {
                        if vm.selected.contains(row.path) { vm.selected.remove(row.path) }
                        else { vm.selected.insert(row.path) }
                    } label: {
                        Image(systemName: vm.selected.contains(row.path) ? "checkmark.circle.fill" : "circle")
                            .font(.title3)
                            .foregroundColor(vm.selected.contains(row.path) ? .accentColor : .secondary)
                            // Readable over a photo too.
                            .background(Circle().fill(Color(.systemBackground).opacity(0.7)))
                            .frame(width: 32, height: 32)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(vm.selected.contains(row.path) ? "Deselect" : "Select")
                }
            }
            .overlay(alignment: .topTrailing) {
                // The lock and the out-of-Images mark (issue #192) are
                // changed from the long-press menu here; a tap on either
                // says what it means.
                if row.isDir && row.path != ".." {
                    HStack(spacing: 0) {
                        if row.outOfImages && vm.outOfImagesSupported {
                            Button {
                                outOfImagesInfo = vm.outOfImagesAsk(row)
                            } label: {
                                Image(systemName: "eye.slash.fill")
                                    .font(.caption)
                                    .foregroundColor(.accentColor)
                                    .padding(5)
                                    .background(.ultraThinMaterial, in: Circle())
                                    .padding(5)
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel(OutOfImagesText.state)
                        }
                        if row.uploadOnly {
                            Button {
                                lockInfo = UploadOnlyText.folderInfo
                            } label: {
                                Image(systemName: "lock.fill")
                                    .font(.caption)
                                    .foregroundColor(.accentColor)
                                    .padding(5)
                                    .background(.ultraThinMaterial, in: Circle())
                                    .padding(5)
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel("Upload only")
                        }
                    }
                }
            }
            .overlay(alignment: .bottomLeading) {
                if thumb != nil && vm.isVideo(row) {
                    Image(systemName: "play.fill")
                        .font(.system(size: 10))
                        .foregroundColor(.white)
                        .padding(6)
                        .background(Color.black.opacity(0.55), in: Circle())
                        .padding(5)
                        .accessibilityLabel("Video")
                }
            }
            .overlay(alignment: .bottomTrailing) {
                if !row.isDir && row.versions > 0 {
                    // Issue #132: opens the versions sheet, as in the list.
                    Button {
                        Task { await vm.openVersions(row) }
                    } label: {
                        Label("\(row.versions)", systemImage: "clock.arrow.circlepath")
                            .font(.caption2)
                            .padding(.horizontal, 6).padding(.vertical, 2)
                            .background(.ultraThinMaterial, in: Capsule())
                    }
                    .buttonStyle(.plain)
                    .padding(5)
                    .accessibilityLabel("\(row.versions) older version\(row.versions == 1 ? "" : "s")")
                }
            }
    }
}

/// What the upload-only lock says (issue #132). The same words as
/// Android's UploadOnlyText and the web lock's tooltip.
enum UploadOnlyText {
    static func promptTitle(name: String, uploadOnly: Bool) -> String {
        uploadOnly ? "Allow deletions in \u{201C}\(name)\u{201D} again?" : "Make \u{201C}\(name)\u{201D} upload only?"
    }
    static let makeMessage = "Nothing in this folder can be deleted - from this phone, a computer or the web - and uploading a file again keeps its older version. Good for photo archives and backups."
    static let clearMessage = "Files in this folder can be deleted again, and uploading a file again replaces it. The older versions kept so far stay."
    static let folderInfo = "Nothing in this folder can be deleted, and uploading a file again keeps its older version. Long-press the folder to change it."
    static let fileInfo = "This is in an upload-only folder: it can't be deleted, and uploading it again keeps its older version. The lock on the folder changes it."
}

/// What keeping a folder out of Images says (issue #192). The same words
/// as Android's OutOfImagesText, the web's and the computer apps', with
/// iOS's Title Case on its menu item and buttons.
enum OutOfImagesText {
    /// The menu item and the prompt's button.
    static let keep = "Keep Out of Images"
    static let show = "Show in Images"
    static let state = "Kept out of Images"
    static let explain = "Photos and videos here aren't tagged, searched for faces or shown in Images. Files still shows them."
    static func promptTitle(name: String, outOfImages: Bool) -> String {
        outOfImages ? "Show \u{201C}\(name)\u{201D} in Images?" : "Keep \u{201C}\(name)\u{201D} out of Images?"
    }
    static let keepMessage = "Its photos and videos won't be tagged, searched for faces or shown in Images, and the tags and faces already found in them are deleted. Files still shows them."
    static let showMessage = "Its photos and videos go back to Images, and are tagged - and searched for faces, if face recognition is on - in the background."
    static let banner = "Kept out of Images - photos and videos here aren't tagged, searched for faces or shown in Images."
}

/// A folder the out-of-Images prompt asks about (issue #192): a row, or
/// the folder being browsed (the banner's "Show in Images").
struct OutOfImagesAsk: Equatable {
    /// The folder's full path, as SetOutOfImages takes it.
    let path: String
    let name: String
    /// Whether it is kept out now: the prompt offers the other way.
    let outOfImages: Bool
}

extension Notification.Name {
    /// Issue #192: Files kept a folder out of Images, or showed it there
    /// again - Images' photos, tags, people and collections have changed.
    static let otcImagesChanged = Notification.Name("otcImagesChanged")
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
