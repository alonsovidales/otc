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
    /// The folder's rows as shown: `listing` in the order chosen (issue #196).
    @Published private(set) var rows: [FileRow] = []
    @Published var loading = false
    /// Issue #196: the order of the list, the grid and the search's results,
    /// one choice for the app (FilesSort.swift), kept as files.viewMode is.
    @Published private(set) var sort: FilesSortOrder
    /// A big folder's rows are being put in order (FilesSort.offMainAbove).
    @Published private(set) var sorting = false
    /// The last listing as the device sent it.
    private var listing: [FileRow] = []
    /// The folder `rows` are of: a new folder's listing being put in order
    /// doesn't keep the last folder's rows on screen meanwhile.
    private var rowsFolder: String?
    /// Bumped by every arrangement: only the last one asked for lands.
    private var sortSeq = 0
    private let defaults: UserDefaults
    /// The language names are compared in. Tests decide in the app's place.
    var collationLocale: @MainActor () -> Locale = { L10n.shared.locale }
    /// The line under the path field, kept as what to say and worded as
    /// it is shown, so it follows a change of language (docs/i18n.md).
    @Published private var errorText: (() -> String)?
    var error: String? { errorText?() }
    @Published var selected: Set<String> = []
    /// The toast at the top, worded as it is shown (errorText).
    @Published private var toastText: (() -> String)?
    var toast: String? { toastText?() }
    /// Bumped by every toast: only the last one's timer takes it away.
    private var toasts = 0
    @Published var previewURL: URL?
    @Published var shareURL: URL?
    /// The files picked to upload, while they go: "Uploading N of M" over
    /// the bar, with Cancel (UploadQueue); nil: none.
    @Published private(set) var uploadProgress: UploadProgress?
    /// Picked files upload two at a time (Android's uploadSlots): each one
    /// is hashed and read chunk by chunk as it goes.
    private lazy var uploads = UploadQueue<UploadPick>(
        slots: 2,
        send: { [weak self] pick in await self?.upload(pick) },
        onChange: { [weak self] progress in self?.uploadProgress = progress }
    )
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
    /// The thumbnails kept on this phone between launches, by content hash
    /// (the listing gives each file's): a small one is used as it is, a big
    /// or unknown one shown and asked for again once a launch, and every
    /// answer kept there. Tests use their own.
    var thumbDisk: ThumbDiskCache = .shared
    // Paths the grid wants (cells that appeared), drained 24 at a time by
    // one task at a time; inFlight keeps a cell scrolling back into view
    // from queuing its path twice.
    private var thumbQueue: [(key: String, path: String, folder: String)] = []
    private var thumbInFlight: Set<String> = []
    private var thumbPumping = false
    /// Cells looked up in the thumbnail cache on this phone first, in the
    /// order they appeared (readDiskQueue), by one task at a time.
    private var diskQueue: [(want: (key: String, path: String, folder: String), hash: String)] = []
    private var diskReading = false

    /// A listing that failed on the way (no answer in time, a dropped
    /// connection, the bridge's "device unreachable") is asked again, 1 s
    /// doubling to 10 s and at once on a wake, while its folder is still
    /// the one shown and nothing newer was asked for (listingAsks). The
    /// device's own refusal (a folder that isn't there) is not.
    let listRetry = PageRetry()
    private var listingAsks = 0
    private var wakeWatch: AnyCancellable?
    private var clearWatch: AnyCancellable?
    /// Why the listing of failedPath failed: said while it is asked again
    /// (not cleared at each try, which flickered and was announced anew).
    private var listingProblem: LoadProblem?
    private var failedPath: String?
    /// Whether the phone has a network now. Tests decide in the phone's place.
    var online: @MainActor () -> Bool = { !NetworkWatch.shared.offline }
    /// Why a listing failed. Tests decide in the phone's place.
    var problemOf: @MainActor (Msg_RespEnvelope?, Error?) -> LoadProblem = { LoadProblem.now(resp: $0, error: $1) }

    init(initialPath: String, defaults: UserDefaults = .standard) {
        self.path = initialPath
        self.defaults = defaults
        self.sort = FilesSortOrder(stored: defaults.string(forKey: FilesSortOrder.storageKey))
        wakeWatch = OTCConnection.shared.$wakes
            .dropFirst()
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.listRetry.wake() }
        clearWatch = NotificationCenter.default.publisher(for: .otcThumbnailCacheCleared)
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.thumbnailCacheCleared() }
    }

    /// Settings cleared the thumbnail cache: the grid's and the results'
    /// thumbnails in memory go too, and are asked for again as cells show.
    func thumbnailCacheCleared() {
        objectWillChange.send()
        thumbs.removeAll()
        thumbCache.removeAllObjects()
        thumbBytes.removeAllObjects()
        noThumb.removeAll()
        resultThumbs = [:]
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
        problemWhileAsking(problem, failedPath: failedPath, p, online: online)?.text(.files)
    }

    /// The problem errorWhileAsking words, kept until it is shown.
    nonisolated static func problemWhileAsking(_ problem: LoadProblem?, failedPath: String?, _ p: String, online: Bool) -> LoadProblem? {
        guard let problem, failedPath == p else { return nil }
        return problem.backOnline(online)
    }

    /// The device's own words when it gave some (it wrote them in the
    /// app's language), else ours.
    static func say(_ words: String, or ours: @escaping () -> String) -> () -> String {
        words.isEmpty ? ours : { words }
    }

    /// The listing failed on the way (`problem`: why): said in plain words,
    /// and asked again after the wait.
    private func listingFailed(_ asked: String, _ problem: LoadProblem) {
        listingProblem = problem
        failedPath = asked
        errorText = { problem.text(.files) }
        let ask = listingAsks
        listRetry.failed { [weak self] in
            guard let self, self.listingAsks == ask, self.path == asked else { return }
            Task { await self.load() }
        }
    }

    /// Lists the folder - and, while the Storage view shows (issue #195),
    /// asks what takes space in it alongside.
    func load() async {
        if storageShown {
            async let usage: Void = loadUsage()
            await loadListing()
            await usage
        } else {
            await loadListing()
        }
    }

    // MARK: The Storage view (issue #195, FolderUsage.swift)

    /// The Storage view shows: GetFolderUsage is asked for the open folder
    /// (when it opens, as the folder changes, on a refresh or after a
    /// delete there) - only then: List and Grid never size folders.
    private(set) var storageShown = false
    /// What it shows for the open folder; nil while it doesn't show.
    @Published private(set) var usage: FolderUsageState?
    /// The folder `usage` is for: another folder's rows don't stay up while
    /// the next one's are asked for.
    private var usageFolder: String?
    /// Bumped by every ask: an error for an older one is dropped.
    private var usageAsks = 0
    /// What the signed-in device is known not to do (tests use their own).
    var support = DeviceSupport.shared

    /// The Storage view shows (`on`) or no longer does. Shown, the
    /// selection goes (it has none: List has), and true: its sizes are to
    /// be asked for.
    @discardableResult
    func showStorage(_ on: Bool) -> Bool {
        guard on != storageShown else { return false }
        storageShown = on
        if on {
            selected.removeAll()
            dragSelection = nil
        } else {
            usageAsks += 1
            usage = nil
            usageFolder = nil
        }
        return on
    }

    /// Asks the device what takes space in the open folder. An answer for
    /// another folder is dropped (FolderUsageModel.answers); a device too
    /// old to say isn't asked again until the next sign-in.
    func loadUsage() async {
        guard storageShown else { return }
        let asked = path
        usageAsks += 1
        let mine = usageAsks
        if usageFolder != asked {
            usageFolder = asked
            usage = .loading
        }
        guard support.folderUsage else {
            usage = .needsUpdate
            return
        }
        var req = Msg_GetFolderUsage()
        req.path = asked
        do {
            let resp = try await request(.reqGetFolderUsage(req))
            guard storageShown else { return }
            if case .respFolderUsage(let u) = resp.payload {
                // The latest ask's, for the folder shown: an older answer
                // for it (from before a delete or a refresh) that lands
                // last doesn't put back what it said.
                guard FolderUsageModel.takes(u.path, folder: path, ask: mine, latest: usageAsks) else { return }
                usage = .loaded(u)
                return
            }
            guard mine == usageAsks, asked == path else { return }
            if ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) {
                support.folderUsage = false
                usage = .needsUpdate
            } else if case .respAck(let ack) = resp.payload, ack.code == LoadProblem.deviceUnreachable {
                usage = .failed(nil)
            } else {
                usage = .failed(resp.errorMessage.isEmpty ? nil : resp.errorMessage)
            }
        } catch {
            guard storageShown, mine == usageAsks, asked == path else { return }
            usage = .failed(nil)
        }
    }

    private func loadListing() async {
        // The folder this listing is for: one that comes back after the
        // screen moved on (the search sending it to another folder while
        // the first listing was on its way) is dropped.
        let asked = path
        listingAsks += 1
        loading = true
        defer { if asked == path { loading = false } }
        let still = Self.problemWhileAsking(listingProblem, failedPath: failedPath, asked, online: online())
        errorText = still.map { problem in { problem.text(.files) } }
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
                errorText = nil
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
                listing = files.map(FileRow.init(file:))
                selected.removeAll()
                // Issue #200: a new listing mid-drag (a refresh, an upload
                // done, another folder) ends the drag with the selection
                // as just set; later moves do nothing, and the lift is
                // still no tap (the gesture's).
                dragSelection = nil
                await arrange(folder: asked)
            } else if resp.error {
                folderOutOfImages = false
                listingProblem = nil
                errorText = Self.say(resp.errorMessage) { S.appFilesListFailed }
            } else {
                folderOutOfImages = false
                listingProblem = nil
                errorText = { S.appFilesUnexpectedResponse }
            }
        } catch {
            guard asked == path else { return }
            folderOutOfImages = false
            listingFailed(asked, problemOf(nil, error))
        }
    }

    /// Issue #196: puts `listing` (of `folder`) in the order chosen and
    /// shows it - at once for up to FilesSort.offMainAbove rows, off the main
    /// thread above that. Only the last arrangement asked for lands: one for
    /// a listing or an order replaced meanwhile is dropped. A new folder's
    /// rows replace the last folder's at once (none while being put in
    /// order); the same folder listed again keeps its rows until then.
    private func arrange(folder: String) async {
        sortSeq += 1
        let mine = sortSeq
        let source = listing
        let order = sort
        let locale = collationLocale()
        if source.count <= FilesSort.offMainAbove {
            rows = FilesSort.sorted(source, by: order, locale: locale)
            rowsFolder = folder
            sorting = false
            return
        }
        if rowsFolder != folder {
            rows = []
            rowsFolder = folder
        }
        sorting = true
        let out = await Task.detached(priority: .userInitiated) {
            FilesSort.sorted(source, by: order, locale: locale)
        }.value
        guard mine == sortSeq else { return }
        rows = out
        sorting = false
    }

    /// Issue #196: picks the order by `key` (the same key again: the other
    /// way), keeps it, and puts the folder and the search's results in it.
    func pickSort(_ key: FilesSortKey) async {
        sort = sort.picking(key)
        defaults.set(sort.stored, forKey: FilesSortOrder.storageKey)
        Announce.polite(FilesSortText.sorted(sort))
        if var r = results {
            r.files = FilesSort.sorted(r.files, by: sort, locale: collationLocale())
            results = r
        }
        await arrange(folder: rowsFolder ?? path)
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
            case failed(Failure, retry: Bool)
        }
        /// Why a search failed, worded when it is shown: the device's own
        /// words, or ours.
        enum Failure: Equatable {
            case unreached
            case tooOld
            case noAnswer
            case device(String)

            var text: String {
                switch self {
                case .unreached: return S.appFilesSearchUnreached
                case .tooOld: return S.appFilesSearchTooOld
                case .noAnswer: return S.appFilesSearchNoAnswer
                case .device(let words): return words
                }
            }
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
            results?.state = .failed(.unreached, retry: true)
            return
        }
        let tooOld = resp.error && resp.errorCode == "unknown_payload"
        // The search stops offering it on such a device.
        if tooOld { FilesNav.shared.deviceCantSearchFiles() }
        guard mine == searchSeq else { return }
        if case .respListOfFiles(let lof) = resp.payload {
            // In the folder's order (issue #196): at most searchLimit of them.
            let found = FilesSort.sorted(lof.files, by: sort, locale: collationLocale())
            results = SearchResults(text: text, state: .done, files: found)
            await loadResultThumbs(found, seq: mine)
        } else if tooOld {
            results?.state = .failed(.tooOld, retry: false)
        } else {
            results?.state = .failed(resp.errorMessage.isEmpty ? .noAnswer : .device(resp.errorMessage), retry: true)
        }
    }

    /// Back to the folder.
    func closeResults() {
        guard results != nil else { return }
        searchSeq += 1
        results = nil
        resultThumbs = [:]
    }

    /// The results' photos and videos: from the thumbnail cache on this
    /// phone (by the hashes the results carry), the rest asked for 24 to a
    /// request as the grid asks, and kept there.
    private func loadResultThumbs(_ files: [Msg_File], seq: Int) async {
        let media = files.filter { !isDirFile($0) && isMediaFile($0) }
        let disk = thumbDisk
        let cached: [(path: String, hash: String, kind: ThumbDiskCache.Kind, thumb: FileThumb)] =
            await Task.detached(priority: .userInitiated) {
                media.compactMap { f in
                    guard let hit = disk.lookup(f.hash) else { return nil }
                    guard let img = Self.decodeTile(hit.data) else {
                        disk.discard(f.hash)
                        return nil
                    }
                    return (f.path, f.hash, hit.kind, FileThumb(image: img, data: hit.data))
                }
            }.value
        guard seq == searchSeq else { return }
        for c in cached { resultThumbs[c.path] = c.thumb }
        // A big or unknown one is asked for again, once a launch.
        let done = Set(cached.filter { $0.kind == .small || !disk.firstFilesReask($0.hash) }.map(\.path))
        var paths = media.map(\.path).filter { !done.contains($0) }
        while !paths.isEmpty, seq == searchSeq {
            let batch = Array(paths.prefix(24))
            paths.removeFirst(batch.count)
            var req = Msg_GetThumbnails()
            req.paths = batch
            // Tiles: small thumbnails (release 111; an older device sends
            // big ones).
            req.smallThumbnails = true
            guard let resp = try? await request(.reqGetThumbnails(req)),
                  case .respListOfFiles(let lof) = resp.payload else { return }
            let asked = Set(batch)
            let got = lof.files.filter { asked.contains($0.path) && !$0.content.isEmpty }
            let decoded = await Task.detached(priority: .userInitiated) {
                got.map { f -> (String, Data, UIImage?) in
                    let img = Self.decodeTile(f.content)
                    if img != nil { disk.store(f.hash, kind: ThumbDiskCache.Kind(f), data: f.content) }
                    return (f.path, f.content, img)
                }
            }.value
            guard seq == searchSeq else { return }
            for (path, data, img) in decoded {
                if let img { resultThumbs[path] = FileThumb(image: img, data: data) }
            }
            // Not looked at (the answer's size): asked for again.
            paths.insert(contentsOf: ThumbAnswer.of(asked: batch, answer: lof, deviceIsNew: disk.deviceOmits == true).askAgain, at: 0)
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
                thumbImage: thumb?.image,
                hash: f.hash
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
        diskQueue.append(((key, fullPath(for: row), path), row.raw.hash))
        guard !diskReading else { return }
        diskReading = true
        Task { await readDiskQueue() }
    }

    /// The thumbnail cache on this phone first, for the cells waiting, in
    /// their order: a small one is all a cell needs; a big or unknown one
    /// is shown and the small one asked for, once a launch; none is asked
    /// for.
    private func readDiskQueue() async {
        defer { diskReading = false }
        while !diskQueue.isEmpty {
            let batch = diskQueue
            diskQueue = []
            let disk = thumbDisk
            let found: [(kind: ThumbDiskCache.Kind, thumb: FileThumb)?] = await Task.detached(priority: .userInitiated) {
                batch.map { item in
                    guard let hit = disk.lookup(item.hash) else { return nil }
                    guard let img = Self.decodeTile(hit.data) else {
                        disk.discard(item.hash)
                        return nil
                    }
                    return (hit.kind, FileThumb(image: img, data: hit.data))
                }
            }.value
            var shown: [String: FileThumb] = [:]
            var asks: [(key: String, path: String, folder: String)] = []
            for (item, f) in zip(batch, found) {
                let want = item.want
                if let f {
                    thumbBytes.setObject(f.thumb.data as NSData, forKey: want.key as NSString, cost: f.thumb.data.count)
                    if visibleThumbs.contains(want.key) {
                        shown[want.key] = f.thumb
                    } else {
                        thumbCache.setObject(f.thumb, forKey: want.key as NSString, cost: f.thumb.cost)
                    }
                }
                if want.folder == path, f.map({ $0.kind != .small && disk.firstFilesReask(item.hash) }) ?? true {
                    asks.append(want)
                } else {
                    thumbInFlight.remove(want.key)
                }
            }
            // One change for the lot, not one redraw per tile.
            if !shown.isEmpty {
                objectWillChange.send()
                thumbs.merge(shown) { $1 }
            }
            guard !asks.isEmpty else { continue }
            thumbQueue.append(contentsOf: asks)
            if !thumbPumping {
                thumbPumping = true
                Task { await pumpThumbnails() }
            }
        }
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
    /// and may stop early around 8 MB, so 24 stays clear of both). What it
    /// didn't look at (ask_again_from, release 113) is asked for again;
    /// what it looked at and didn't answer has no thumbnail and keeps the
    /// type icon - from an older device, which doesn't say where it
    /// stopped, until the cell shows again (ThumbAnswer). Each thumbnail is
    /// kept in the cache on this phone under the answer's hash. Results for
    /// a folder the user already left are dropped.
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
            guard folder == path else {
                for item in batch { thumbInFlight.remove(item.key) }
                continue
            }
            // A failed request (no connection) remembers nothing, so the
            // cells ask again the next time they appear.
            guard let resp, case .respListOfFiles(let lof) = resp.payload else {
                for item in batch { thumbInFlight.remove(item.key) }
                continue
            }
            let outcome = ThumbAnswer.of(asked: batch.map(\.path), answer: lof,
                                         deviceIsNew: thumbDisk.deviceOmits == true)
            let again = Set(outcome.askAgain)
            let none = Set(outcome.none)
            // Not looked at: first in line again, still on their way.
            thumbQueue.insert(contentsOf: batch.filter { again.contains($0.path) }, at: 0)
            for item in batch where !again.contains(item.path) { thumbInFlight.remove(item.key) }
            var got: [String: Msg_File] = [:]
            for f in lof.files where !f.content.isEmpty { got[f.path] = f }
            let wanted = batch.filter { !again.contains($0.path) }.map { (key: $0.key, path: $0.path, file: got[$0.path]) }
            let disk = thumbDisk
            let decoded = await Task.detached(priority: .userInitiated) {
                wanted.map { w -> (String, String, Data?, UIImage?) in
                    guard let f = w.file else { return (w.key, w.path, nil, nil) }
                    let img = Self.decodeTile(f.content)
                    if img != nil { disk.store(f.hash, kind: ThumbDiskCache.Kind(f), data: f.content) }
                    return (w.key, w.path, f.content, img)
                }
            }.value
            var fresh: [String: FileThumb] = [:]
            for (key, filePath, data, img) in decoded {
                guard let data, let img else {
                    // None now (or one that doesn't decode); an older
                    // device's silence is no answer: asked again when the
                    // cell shows again.
                    if data != nil || none.contains(filePath) { noThumb.insert(key) }
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
                thumbImage: thumb?.image,
                hash: row.raw.hash
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
            showToast { S.appFilesOpenFailed }
        } catch {
            showToast { S.appFilesDownloadFailed(detail: error.localizedDescription) }
        }
    }

    /// Issue #132: true when the selection touches an upload-only folder -
    /// the device refuses those deletes, so the button greys out first.
    var selectionUploadOnly: Bool {
        rows.contains { selected.contains($0.path) && $0.uploadOnly }
    }

    /// The selection when it is one folder: what the bar's "Share as
    /// gallery" (issue #180) and keep out of Images act on (issue #200,
    /// once the long-press menu's; Android's State.selectedFolder).
    var selectedFolder: FileRow? {
        guard selected.count == 1, let p = selected.first else { return nil }
        return rows.first { $0.path == p && $0.isDir && $0.path != ".." }
    }

    /// Issue #192: the folder the bar's switch for Images acts on - one
    /// folder picked, on a device that can. Not inside a folder kept out:
    /// that one (or one above it) keeps it out, and the device refuses its
    /// Show; the banner shows the folder to show instead.
    var outOfImagesSwitch: FileRow? {
        guard outOfImagesSupported, !folderOutOfImages else { return nil }
        return selectedFolder
    }

    // MARK: Drag selection (issue #200, DragSelect.swift)

    private var dragSelection: DragSelection<String>?

    /// The rows a drag selects across, as shown: all but "..".
    private var selectableOrder: [String] { rows.map(\.path).filter { $0 != ".." } }

    /// A long press on the row (or tile) `path`: it is selected, and a drag
    /// on from it selects the range.
    func beginDragSelect(_ path: String) {
        guard path != ".." else { return }
        dragSelection = DragSelection(base: selected, start: path)
        selected.insert(path)
    }

    /// The finger is over `path` now (nil: over no row): every row from the
    /// one pressed to it is selected, in the order shown, on top of what
    /// was before. False once the drag is over: the pressed row has gone
    /// from the rows shown, and the selection stays as it stands
    /// (DragSelection's rule), or a new listing came (load(), which sets
    /// the selection).
    @discardableResult
    func dragSelect(to path: String?) -> Bool {
        guard var drag = dragSelection else { return false }
        let order = selectableOrder
        guard drag.goesOn(in: order) else {
            dragSelection = nil
            return false
        }
        guard let path, path != "..", drag.move(to: path) else { return true }
        dragSelection = drag
        if let s = drag.selection(in: order) { selected = s }
        return true
    }

    func endDragSelect() { dragSelection = nil }

    /// The selection bar's x (or Escape on an iPad's keyboard): nothing is
    /// picked any more and a drag under way is over (the finger going on
    /// picks nothing); the bar stays for Upload, and a tap opens again.
    func clearSelection() {
        selected.removeAll()
        dragSelection = nil
    }

    /// A tap on a row or tile while something is selected: it is picked
    /// or left out, as in Images.
    func toggleSelected(_ path: String) {
        if selected.contains(path) { selected.remove(path) } else { selected.insert(path) }
    }

    func deleteSelected() async {
        for p in selected {
            // Issue #201: with the content the row was listed with, so the
            // device deletes nothing that changed since ("changed").
            let row = rows.first { $0.path == p }
            let req = DeleteCopies.request(path: p.contains("/") ? p : joinPath(path, p),
                                           hash: row.map { $0.isDir ? "" : $0.raw.hash } ?? "",
                                           copies: nil, everyCopy: false)
            guard let resp = try? await request(.reqDelFile(req)) else { continue }
            if resp.error && resp.errorCode == "upload_only" {
                let name = leafName(req.path)
                showToast { S.appFilesDeleteRefused(name: name) }
                break
            }
            // It changed on the device since it was listed: the device's
            // words; the listing is read again below.
            if resp.error && resp.errorCode == "changed" {
                let words = resp.errorMessage
                showToast { words }
                continue
            }
            // A deleted photo's thumbnail doesn't stay on the phone either.
            if !resp.error, let row = rows.first(where: { $0.path == p }), !row.isDir {
                thumbDisk.discard(row.raw.hash)
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
            showToast(Self.say(resp.errorMessage) { S.appFilesFolderUpdateFailed })
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
                showToast(Self.say(resp.errorMessage) { S.appFilesFolderUpdateFailed })
            }
        } catch {
            showToast { S.appFilesFolderUpdateFailed }
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
            showToast { S.appFilesVersionOpenFailed }
        } catch {
            showToast { S.appFilesDownloadFailed(detail: error.localizedDescription) }
        }
    }

    /// Downloads the selection, the same way the Images tab does it: the
    /// share link the device hands back is itself the download.
    func downloadSelected() async {
        preparing = .download
        defer { preparing = nil }
        guard let link = await shareLink(), let url = URL(string: link) else {
            showToast { S.appFilesDownloadLinkFailed }
            return
        }
        await UIApplication.shared.open(url)
    }

    func shareSelected() async {
        preparing = .share
        defer { preparing = nil }
        guard let link = await shareLink(), let url = URL(string: link) else {
            showToast { S.appFilesShareLinkFailed }
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

    /// One file picked to upload, and the path it goes to: the folder it
    /// was picked into, read before it waits its turn (the owner may open
    /// another folder meanwhile).
    struct UploadPick {
        let url: URL
        let target: String
    }

    /// Files picked in the document picker, into the folder shown now: two
    /// at a time, "Uploading N of M" meanwhile (UploadQueue).
    func upload(picked urls: [URL]) {
        uploads.add(urls.map { UploadPick(url: $0, target: joinPath(path, $0.lastPathComponent)) })
    }

    /// The progress line's Cancel: what waits never goes, what goes stops at
    /// its next chunk; none of it is reported as failed. The folder is
    /// listed again for the ones that made it.
    func cancelUploads() {
        uploads.cancel()
        Task { await load() }
    }

    /// Streams the file: hashed off the main actor in 4 MiB pieces, then
    /// sent chunk by chunk. Reading it whole (and hashing it here) put
    /// every picked file in memory at once and froze the UI - a few large
    /// videos and iOS killed the app. Read through NSFileCoordinator, so a
    /// file in iCloud Drive that isn't on the phone yet is downloaded
    /// first; one that can't be read says so (it was skipped without a
    /// word). A cancel stops it without a word.
    private func upload(_ pick: UploadPick) async {
        let url = pick.url
        let path = pick.target
        let name = url.lastPathComponent
        // The picker's access to the file, for as long as it is read.
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        let hash: String
        do {
            hash = try await Self.coordinatedHash(of: url)
        } catch {
            if Task.isCancelled { return }
            showToast { S.appFilesUploadFailed(detail: S.appFilesCannotRead(name: name)) }
            return
        }
        if Task.isCancelled { return }
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
            if resp.error {
                // The device's words, in the app's language, as the detail.
                let words = resp.errorMessage
                showToast { S.appFilesUploadFailed(detail: words) }
            }
        } catch {
            // Cancelled from the progress line: no failure to report.
            if Task.isCancelled || error is CancellationError { return }
            showToast { S.appFilesUploadFailed(detail: error.localizedDescription) }
        }
        await load()
    }

    /// The file's SHA-256, read through NSFileCoordinator (an iCloud Drive
    /// file not on the phone is downloaded for it), off the main actor.
    nonisolated static func coordinatedHash(of url: URL) async throws -> String {
        try await Task.detached(priority: .userInitiated) { () throws -> String in
            var coordinatorError: NSError?
            var result: Result<String, Error> = .failure(CocoaError(.fileReadUnknown))
            NSFileCoordinator(filePresenter: nil).coordinate(readingItemAt: url, options: [.withoutChanges], error: &coordinatorError) { readURL in
                result = Result { try OTCConnection.sha256Hex(of: readURL) }
            }
            if let coordinatorError { throw coordinatorError }
            return try result.get()
        }.value
    }

    /// Says a toast to VoiceOver, which never moves to it on its own - a
    /// refused switch (out_of_images_by_parent, locked_by_parent) would
    /// look as if it did nothing. Tests listen in its place.
    var announce: @MainActor (String) -> Void = { AccessibilityNotification.Announcement($0).post() }

    /// Shows `say` as the toast: worded as it is drawn, and in the language
    /// shown now for VoiceOver and for how long it stays.
    func showToast(_ say: @escaping () -> String) {
        toasts += 1
        let mine = toasts
        toastText = say
        let m = say()
        announce(m)
        // Long enough to read: 2.5 s, longer for a sentence (issue #192's
        // refusal names two folders).
        let seconds = max(2.5, Double(m.count) / 18)
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
            if self?.toasts == mine { self?.toastText = nil }
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
    @State private var lockInfo: UploadOnlyText.Info?
    // Issue #192: keeping a folder out of Images asks first too (it deletes
    // the tags and faces found in it), and the mark on a folder kept out
    // says what that means, with the way back.
    @State private var outOfImagesPrompt: OutOfImagesAsk?
    @State private var outOfImagesInfo: OutOfImagesAsk?
    // List, grid or (issue #195) Storage ("list"/"grid"/"storage"),
    // remembered across launches - the web and Android explorers keep
    // theirs under the same key.
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
    /// Issue #200: where each row or tile on screen is, for a drag
    /// selection's finger (DragSelect.swift): the list's on the screen (a
    /// List's rows don't see a name given to it), the grid's in its own
    /// space. Apart: switching between them, one's rows go as the other's
    /// come.
    @State private var listFrames = DragSelectFrames<String>()
    @State private var gridFrames = DragSelectFrames<String>()
    /// The list's columns on a wide window (FilesColumns.swift): the widths
    /// given on this phone and a drag under way; the list's width, which
    /// they are fitted to; the text size, which their defaults follow.
    @StateObject private var columns = FilesColumnsModel()
    @State private var listWidth: CGFloat = 0
    @Environment(\.dynamicTypeSize) private var typeSize
    private static let listSpace = DragSelectSpace.global
    private static let gridSpaceName = "filesGrid"
    private static let gridSpace = DragSelectSpace.named(gridSpaceName)
    // Selection is managed here rather than with List(selection:) +
    // EditButton(): that pairing needs the row's tap to be the List's own
    // selection-toggle handling, and this view also needs a tap to open
    // the file, which intercepts the touch first and left "Edit" mode
    // silently doing nothing. Every row carries its own checkbox all the
    // time (the web's table has had one per row from the start), so
    // nothing has to be switched on before something can be picked:
    // tapping the checkbox selects, a long press selects the row and a
    // drag on from it the rows up to the finger (issue #200, as in
    // Images), and tapping the rest of the row opens it - or, while
    // something is selected, picks it or leaves it out.

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
                            if vm.loading || vm.sorting { ProgressView() }
                            viewOptions
                                .tourTarget(.viewOptions)
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
                        } else if viewMode == "storage" {
                            // Issue #195: what takes space in the folder.
                            FolderUsageList(vm: vm, open: tap)
                        } else {
                            // On a wide window, with the columns' headings over it
                            // (folderList).
                            folderList
                        }

                        // The files picked, while they go: how far along, and
                        // Cancel.
                        if let progress = vm.uploadProgress {
                            HStack(spacing: 10) {
                                ProgressView()
                                Text(S.appFilesUploading(done: progress.done, total: progress.total))
                                    .font(.footnote)
                                    .foregroundStyle(.secondary)
                                Spacer(minLength: 0)
                                Button(S.commonCancel) { vm.cancelUploads() }
                                    .font(.footnote.weight(.semibold))
                            }
                            .padding(.horizontal)
                            .padding(.top, 6)
                        }
                        // Always shown here, unlike Images: Upload acts on the
                        // folder being browsed, not on a selection, so it needs
                        // somewhere to live when nothing is selected - and this is
                        // where the rest of the actions are. Share/Download/Delete
                        // grey out until something is ticked.
                        SelectionActionBar(
                            count: vm.selected.count,
                            busy: vm.preparing ?? (vm.outOfImagesIdle ? nil : .outOfImages),
                            onShare: { Task { await vm.shareSelected() } },
                            onDownload: { Task { await vm.downloadSelected() } },
                            onDelete: {
                                if vm.selectionUploadOnly {
                                    vm.showToast { S.appFilesSelectionUploadOnly }
                                } else {
                                    vm.confirmDeleteSelected = true
                                }
                            },
                            onClear: vm.clearSelection,
                            // Issue #200: one folder picked - its photos and
                            // videos as a gallery (issue #180), and keeping it
                            // out of Images or showing it there again (issue
                            // #192), once the long-press menu's; as Android's bar.
                            onGallery: vm.selectedFolder.map { row in { gallerySource = .directory(vm.fullPath(for: row)) } },
                            onUpload: { showImporter = true },
                            onOutOfImages: vm.outOfImagesSwitch.map { row in { outOfImagesPrompt = vm.outOfImagesAsk(row) } },
                            keptOutOfImages: vm.outOfImagesSwitch?.outOfImages == true
                        )
                        .padding(.vertical, 8)
                    }
                }
                .accessibilityHidden(searchPanelShown)
            }
            .coordinateSpace(.named(Self.bodySpace))
            // What the viewer says (saved to Photos, a delete refused - issue
            // #201's "changed" among them), as Images says it.
            .modifier(ViewerNotice(viewer: viewer))
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
            // A wide window: the top bar and the menu are above, and this
            // empty bar would only push the path down, as on Social.
            .toolbar(wide ? .hidden : .automatic, for: .navigationBar)
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
        .task {
            vm.showStorage(viewMode == "storage")
            await vm.load()
        }
        // Issue #195: the Storage view chosen asks for the folder's sizes;
        // List and Grid never do.
        .onChange(of: viewMode) { _, mode in
            if vm.showStorage(mode == "storage") { Task { await vm.loadUsage() } }
        }
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
            // Two at a time, each file's access opened while it is read.
            vm.upload(picked: urls)
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
                        Section(footer: Text(S.appFilesVersionsExplain)) {
                            Button {
                                Task { await vm.openVersion(row, hash: "") }
                            } label: {
                                HStack {
                                    Text(S.appFilesVersionCurrent).fontWeight(.semibold)
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
                                        // The app's formatting locale: the system's own
                                        // while the app speaks its language.
                                        Text(S.appFilesVersionReplaced(date: v.hasModified
                                            ? v.modified.date.formatted(Date.FormatStyle(date: .abbreviated, time: .shortened, locale: L10n.shared.locale))
                                            : "—"))
                                        Spacer()
                                        Text(formatByteCount(v.fileSize)).foregroundColor(.secondary)
                                    }
                                }
                            }
                        }
                    }
                    .navigationTitle(S.appFilesVersionsTitle(name: row.name))
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar { ToolbarItem(placement: .cancellationAction) { Button(S.commonDone) { vm.versionsOf = nil } } }
                }
            }
        }
        .alert(
            lockPrompt.map { UploadOnlyText.promptTitle(name: $0.name, uploadOnly: $0.uploadOnly) } ?? "",
            isPresented: Binding(get: { lockPrompt != nil }, set: { if !$0 { lockPrompt = nil } }),
            presenting: lockPrompt
        ) { row in
            Button(row.uploadOnly ? S.iosFilesAllowDeletions : S.iosFilesMakeUploadOnly) { Task { await vm.toggleUploadOnly(row) } }
            Button(S.commonCancel, role: .cancel) {}
        } message: { row in
            Text(row.uploadOnly ? UploadOnlyText.clearMessage : UploadOnlyText.makeMessage)
        }
        .alert(S.appFilesUploadOnly, isPresented: Binding(get: { lockInfo != nil }, set: { if !$0 { lockInfo = nil } })) {
            Button(S.commonOk, role: .cancel) {}
        } message: {
            Text(lockInfo?.text ?? "")
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
            Button(S.commonCancel, role: .cancel) {}
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
            Button(S.commonOk, role: .cancel) {}
        } message: { _ in
            Text(OutOfImagesText.explain)
        }
        .confirmationDialog(
            S.appFilesDeleteConfirm(count: vm.selected.count),
            isPresented: $vm.confirmDeleteSelected,
            titleVisibility: .visible
        ) {
            Button(S.commonDelete, role: .destructive) { Task { await vm.deleteSelected() } }
            Button(S.commonCancel, role: .cancel) {}
        }
        // Presented exactly as PhotoGalleryView presents it.
        .fullScreenCover(isPresented: Binding(
            get: { viewer.openIndex != nil },
            set: { if !$0 { viewer.closeModal() } }
        )) {
            ImageModal(
                vm: viewer,
                save: {
                    // Issue #201: the original, as Images saves it.
                    let path = viewer.openPath
                    let fallback = viewerThumb()
                    viewer.saveOriginalToPhotos {
                        guard let path else { return fallback }
                        return await viewer.fullImage(of: path) ?? fallback
                    }
                },
                share: {
                    let fallback = viewerThumb()
                    Task { viewer.shareCurrentPhoto(await viewer.fullImageForOpen() ?? fallback) }
                },
                // Files' items have no copies: always "only this one".
                delete: { viewer.deleteCurrentPhoto(everyCopy: $0) }
            )
        }
    }

    // MARK: The folder's list

    /// The list's columns on a wide window (FilesColumns.swift): the wide
    /// layout (WideLayout.minWidth, 600 pt and up - the menu beside the
    /// page), once the list's width is known and has room for Name and
    /// Size at their minimums. nil on a narrow window, whose rows stay as
    /// they were: the name with its size under it.
    private var columnLayout: (layout: FilesColumnLayout, metrics: FilesColumnMetrics)? {
        guard wide, listWidth > 0 else { return nil }
        let m = columns.metrics(typeSize)
        let room = listWidth - 2 * FilesColumnsLook.inset
        guard FilesColumns.fits(room: room, m) else { return nil }
        return (columns.layout(room: room, m), m)
    }

    /// The folder's rows. Each carries its own checkbox all the time - see
    /// the note on selection at the top of this view. On a wide window the
    /// web's columns: Name, Size, Created and Modified under headings that
    /// sort and resize them (FilesColumnsView.swift).
    private var folderList: some View {
        let cols = columnLayout
        return VStack(spacing: 0) {
            if let cols {
                FilesColumnsHeading(model: columns, layout: cols.layout, metrics: cols.metrics, sort: vm.sort) { key in
                    Task { await vm.pickSort(key) }
                }
            }
            List {
                ForEach(vm.rows) { row in
                    Group {
                        if let cols {
                            wideRow(row, cols.layout)
                        } else {
                            narrowRow(row)
                        }
                    }
                    .contentShape(Rectangle())
                    .onTapGesture { tap(row) }
                    // Issue #200: a long press selects the row,
                    // and a drag on from it the rows up to the
                    // finger (the list's dragSelect).
                    .modifier(DragSelectRow(path: row.path, frames: listFrames, space: Self.listSpace))
                    // The headings' insets, so the columns line up.
                    .listRowInsets(cols == nil ? nil : FilesColumnsLook.rowInsets)
                }
            }
            .listStyle(.plain)
            .gesture(dragSelect(listFrames, space: Self.listSpace))
            // Issue #51: pull down to re-list this directory - files
            // arrive from other clients (the Mac app, another phone)
            // while this screen sits open, and nothing else re-reads it.
            .refreshable { await vm.load() }
            .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { listWidth = $0 }
        }
    }

    /// A narrow window's row: the name with its size under it.
    private func narrowRow(_ row: FileRow) -> some View {
        HStack {
            selectButton(row)
            rowIcon(row)
            VStack(alignment: .leading) {
                Text(row.name).lineLimit(1)
                if !row.isDir {
                    Text(formatByteCount(row.size)).font(.caption2).foregroundColor(.secondary)
                }
            }
            Spacer()
            rowMarks(row)
        }
    }

    /// A wide window's row: the circle and the icon, then Name - the name,
    /// cut short before its marks are - then the columns shown.
    private func wideRow(_ row: FileRow, _ layout: FilesColumnLayout) -> some View {
        HStack(spacing: 0) {
            HStack(spacing: FilesColumnsLook.gap) {
                selectButton(row)
                rowIcon(row)
            }
            .frame(width: FilesColumnsLook.lead, alignment: .leading)
            HStack(spacing: FilesColumnsLook.gap) {
                Text(row.name).lineLimit(1)
                Spacer(minLength: 0)
                // Their own size first: the name gives way, down to the 10
                // characters Name's minimum keeps for it.
                HStack(spacing: FilesColumnsLook.gap) { rowMarks(row) }
                    .layoutPriority(1)
            }
            .padding(.trailing, FilesColumnsLook.gap)
            .frame(maxWidth: .infinity, alignment: .leading)
            FilesColumnCells(row: row, layout: layout)
        }
    }

    /// The row's own checkbox, always there.
    /// Issue #116: directories are selectable too - the device expands one
    /// to every file under it for share/download/delete (see
    /// files_manager.resolvePaths). Only ".." has none, being navigation
    /// rather than a thing; it keeps the width so names stay aligned.
    @ViewBuilder
    private func selectButton(_ row: FileRow) -> some View {
        if row.path != ".." {
            Button {
                vm.toggleSelected(row.path)
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
            .accessibilityLabel(vm.selected.contains(row.path) ? S.commonDeselect : S.commonSelect)
        } else {
            Color.clear.frame(width: 28, height: 28)
        }
    }

    /// Issue #71: a spinner in place of the row's own icon while its
    /// GetFile round trip is in flight - the only feedback a tap used to
    /// get was however long that took, which just looked stuck.
    @ViewBuilder
    private func rowIcon(_ row: FileRow) -> some View {
        if vm.openingPath == row.path || (row.isDir && vm.isOutOfImagesBusy(vm.fullPath(for: row))) {
            ProgressView().frame(width: 20)
        } else {
            Image(systemName: row.isDir ? "folder.fill" : (isImgFile(row.raw) ? "photo" : "doc"))
                .foregroundColor(row.isDir ? .accentColor : .secondary)
                .frame(width: 20)
        }
    }

    /// After the name: a file's versions, a folder's mark for "kept out of
    /// Images" and its lock, or the lock of a file inside an upload-only
    /// folder.
    @ViewBuilder
    private func rowMarks(_ row: FileRow) -> some View {
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
            .accessibilityLabel(S.iosFilesOlderVersions(count: Int(row.versions)))
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
            .accessibilityLabel(row.uploadOnly ? S.appFilesClearUploadOnly : S.appFilesMakeUploadOnly)
            // The tour's step about uploading: the first folder's lock.
            .tourTarget(row.id == vm.rows.first(where: { $0.path != ".." && $0.isDir })?.id ? .folderLock : nil)
        } else if row.uploadOnly {
            Button {
                lockInfo = .file
            } label: {
                Image(systemName: "lock.fill").foregroundColor(.secondary).frame(width: 28, height: 28)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(S.appFilesInUploadOnly)
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
                    // Its content goes with its Delete (issue #201).
                    viewer.showFiles([PhotoGalleryVM.Item(
                        id: "\(file.path)#\(file.hash)", path: file.path, mime: file.mime,
                        size: Int(file.fileSize), thumbData: nil, localURL: nil, isLocalOnly: false,
                        hash: file.hash
                    )], startAt: 0)
                }
            }
        }
    }

    /// Back to where it goes: the folder under the results.
    private var backLabel: String {
        vm.path == "/" ? S.appFilesBackToFiles : S.appFilesBackToFolder(folder: leafName(vm.path))
    }

    /// A result's name for VoiceOver: what it is, where, and whether it is
    /// kept out of Images - each a sentence of its own.
    private static func resultLabel(_ kind: FoundKind, name: String, folder: String, keptOut: Bool) -> String {
        switch (kind, keptOut) {
        case (.folder, false): return S.iosFilesResultFolder(name: name, folder: folder)
        case (.photo, false): return S.iosFilesResultPhoto(name: name, folder: folder)
        case (.video, false): return S.iosFilesResultVideo(name: name, folder: folder)
        case (.doc, false): return S.iosFilesResultFile(name: name, folder: folder)
        case (.folder, true): return S.iosFilesResultFolderKeptOut(name: name, folder: folder)
        case (.photo, true): return S.iosFilesResultPhotoKeptOut(name: name, folder: folder)
        case (.video, true): return S.iosFilesResultVideoKeptOut(name: name, folder: folder)
        case (.doc, true): return S.iosFilesResultFileKeptOut(name: name, folder: folder)
        }
    }

    private func resultsView(_ r: FilesExplorerViewModel.SearchResults) -> some View {
        let q = FoldedText(r.text)
        let count = r.files.count
        return VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 2) {
                Button {
                    vm.closeResults()
                } label: {
                    Label(backLabel, systemImage: "arrow.left")
                        .font(.subheadline.weight(.medium))
                        .lineLimit(1)
                        // A finger's worth of height, not just the text's.
                        .frame(minHeight: 44)
                        .contentShape(Rectangle())
                }
                Text(S.appFilesResultsTitle(query: r.text))
                    .font(.title3.weight(.semibold))
                    .accessibilityAddTraits(.isHeader)
                // Its line kept while searching, so the list doesn't move.
                Text(r.state == .done && count > 0
                     ? (count == FilesExplorerViewModel.searchLimit
                        ? S.appFilesResultsLimited(limit: count)
                        : S.appFilesResultsCount(count: count))
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
                    Text(S.appFilesSearching).foregroundStyle(.secondary)
                }
                .padding()
                Spacer()
            case .failed(let failure, let retry):
                VStack(alignment: .leading, spacing: 10) {
                    Text(failure.text).foregroundStyle(.secondary)
                    if retry {
                        Button(S.iosFilesTryAgain) { Task { await vm.startSearch(r.text) } }
                            .buttonStyle(.bordered)
                    }
                }
                .padding()
                Spacer()
            case .done where r.files.isEmpty:
                Text(S.appFilesNoResults(query: r.text))
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
        .accessibilityLabel(Self.resultLabel(FoundKind(f), name: name, folder: dir, keptOut: row.outOfImages))
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
        // The finger lifting at the end of a drag selection is no tap.
        guard DragSelect.tapAllowed else { return }
        // Issue #200: while something is selected a tap picks or leaves out,
        // as in Images (".." still goes up).
        if !vm.selected.isEmpty && row.path != ".." {
            vm.toggleSelected(row.path)
            return
        }
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

    /// Issue #200: the long press and drag over the list's rows or the
    /// grid's tiles ("..", never selected, has no frame there). A press
    /// starts only on a row or tile itself - on "..", a gap or the padding
    /// it starts nothing, and the view keeps the touch; the finger beside
    /// tiles in a row is then over the nearest.
    private func dragSelect(_ frames: DragSelectFrames<String>, space: DragSelectSpace) -> DragSelectGesture {
        let vm = vm
        return DragSelectGesture(
            space: space,
            enabled: vm.results == nil,
            canStart: { frames.item(containing: $0) != nil },
            began: { if let p = frames.item(containing: $0) { vm.beginDragSelect(p) } },
            moved: { vm.dragSelect(to: frames.item(at: $0)) },
            ended: { vm.endDragSelect() }
        )
    }

    /// List or grid, and the order (issue #196), in one menu as the Files
    /// app has it: the button shows the view in use. Under Sort by, the
    /// order in use has the checkmark, an arrow and its direction in words;
    /// picking it again reverses it. Toggles, not a Picker, for the sort
    /// keys: a Picker doesn't say when the item already picked is picked
    /// again.
    private var viewOptions: some View {
        Menu {
            Picker(selection: $viewMode) {
                Label(S.appFilesViewList, systemImage: "list.bullet").tag("list")
                Label(S.appFilesViewGrid, systemImage: "square.grid.2x2").tag("grid")
                Label(S.appFilesViewStorage, systemImage: Self.storageIcon).tag("storage")
            } label: {
                EmptyView()
            }
            .pickerStyle(.inline)
            // Storage keeps the device's order, largest first: no sort.
            if viewMode != "storage" {
                Section(S.appFilesSortBy) {
                    ForEach(FilesSortKey.allCases) { key in
                        // The menu shows the first Text as the item's title, the
                        // second under it, and the Image as its icon.
                        Toggle(isOn: Binding(get: { vm.sort.key == key }, set: { _ in Task { await vm.pickSort(key) } })) {
                            Text(FilesSortText.name(key))
                            if vm.sort.key == key {
                                Text(FilesSortText.direction(vm.sort))
                                Image(systemName: vm.sort.ascending ? "chevron.up" : "chevron.down")
                            }
                        }
                    }
                }
            }
        } label: {
            Image(systemName: viewMode == "grid" ? "square.grid.2x2" : viewMode == "storage" ? Self.storageIcon : "list.bullet")
                .frame(width: 28, height: 28)
                .contentShape(Rectangle())
        }
        .accessibilityLabel(S.appFilesViewOptions)
        .accessibilityValue(viewMode == "storage" ? S.appFilesViewStorage : FilesSortText.sorted(vm.sort))
    }

    /// Issue #195: the Storage view's mark, in the menu and on its button.
    private static let storageIcon = "chart.bar.xaxis"

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
                    // Issue #200: a long press selects the tile, and a drag
                    // on from it the tiles up to the finger.
                    .modifier(DragSelectRow(path: row.path, frames: gridFrames, space: Self.gridSpace))
                    // Lazily: only tiles that scroll into view ask the
                    // device for their thumbnail.
                    .onAppear { vm.wantThumbnail(for: row) }
                    .onDisappear { vm.thumbGone(for: row) }
                }
            }
            .padding()
            .coordinateSpace(.named(Self.gridSpaceName))
            .gesture(dragSelect(gridFrames, space: Self.gridSpace))
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
                        vm.toggleSelected(row.path)
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
                    .accessibilityLabel(vm.selected.contains(row.path) ? S.commonDeselect : S.commonSelect)
                }
            }
            .overlay(alignment: .topTrailing) {
                // A tap on the lock offers to allow deletions again, as the
                // list's does (and Android's tile); one on the out-of-Images
                // mark (issue #192) says what it means, with the way back.
                // Making a folder upload only is the list's open lock.
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
                                lockPrompt = row
                            } label: {
                                Image(systemName: "lock.fill")
                                    .font(.caption)
                                    .foregroundColor(.accentColor)
                                    .padding(5)
                                    .background(.ultraThinMaterial, in: Circle())
                                    .padding(5)
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel(S.appFilesUploadOnly)
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
                        .accessibilityLabel(S.appFilesVideo)
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
                    .accessibilityLabel(S.iosFilesOlderVersions(count: Int(row.versions)))
                }
            }
    }
}

/// The viewer's alert (PhotoGalleryVM.notice) over Files, as Images shows
/// its own.
private struct ViewerNotice: ViewModifier {
    @ObservedObject var viewer: PhotoGalleryVM

    func body(content: Content) -> some View {
        content.alert(viewer.notice?.text ?? "", isPresented: $viewer.showAlert) {
            Button(S.commonOk, role: .cancel) {}
        }
    }
}

/// What the upload-only lock says (issue #132). The same words as
/// Android's UploadOnlyText and the web lock's tooltip, from the catalog
/// (app.files.*) in the language shown when read.
enum UploadOnlyText {
    static func promptTitle(name: String, uploadOnly: Bool) -> String {
        uploadOnly ? S.appFilesAllowDeletionsTitle(name: name) : S.appFilesMakeUploadOnlyTitle(name: name)
    }
    static var makeMessage: String { S.appFilesMakeUploadOnlyMessage }
    static var clearMessage: String { S.appFilesAllowDeletionsMessage }
    static var fileInfo: String { S.appFilesUploadOnlyFileInfo }

    /// Which lock was tapped, for its explanation: a file inside an
    /// upload-only folder (a folder's lock asks to change it instead).
    enum Info {
        case file

        var text: String { UploadOnlyText.fileInfo }
    }
}

/// Issue #200: a row or tile of Files for the drag selection - where it is
/// drawn, in the list's or the grid's space. ".." is never selected, so it
/// has none.
private struct DragSelectRow: ViewModifier {
    let path: String
    let frames: DragSelectFrames<String>
    let space: DragSelectSpace

    func body(content: Content) -> some View {
        if path == ".." {
            content
        } else {
            content.dragSelectItem(path, in: frames, space: space)
        }
    }
}

/// What keeping a folder out of Images says (issue #192). The same words
/// as Android's OutOfImagesText, the web's and the computer apps', with
/// iOS's Title Case on its menu item and buttons; from the catalog in the
/// language shown when read.
enum OutOfImagesText {
    /// The menu item and the prompt's button.
    static var keep: String { S.iosFilesKeepOutOfImages }
    static var show: String { S.appShellShowInImages }
    static var state: String { S.appFilesKeptOut }
    static var explain: String { S.appFilesKeptOutExplain }
    static func promptTitle(name: String, outOfImages: Bool) -> String {
        outOfImages ? S.appFilesShowTitle(name: name) : S.appFilesKeepOutTitle(name: name)
    }
    static var keepMessage: String { S.appFilesKeepOutMessage }
    static var showMessage: String { S.appFilesShowMessage }
    static var banner: String { S.appFilesKeptOutBanner }
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
