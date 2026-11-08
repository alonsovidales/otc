// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  LargestFiles.swift
//  OffTheCloud
//
//  Settings › Largest files (2026-11-08, the owner: "so we can identify
//  what is taking more space"): every file on the device, largest first,
//  one row per content at its first path (ListLargestFiles in
//  messages.proto: its comment is the contract) - a thumbnail for a photo
//  or video (asked for by path, as Files' grid does), else its type's
//  icon; its name, its folder and its size; "in N places" for content at
//  several paths. Pages of 50 come as the list scrolls, asked again after
//  a failure with the pages' backoff. A tap opens a photo or video in the
//  shared viewer (Edit, Revert, Info and Delete, as from Files) and
//  anything else as Files opens it; a checkbox or a long press picks, and
//  the bar's Compress makes the picked photos and videos smaller
//  (Compress.swift): each one takes its old row's place, and the list is
//  read again in place once the job is over - as after a pull, or an edit
//  or a delete in the viewer. The web and the Android app have the same
//  screen, cases L1-L10 alike (CompressTests.swift).
//

import Combine
import SwiftUI

/// The list's rules: what it asks, what an answer means, what a page adds
/// and what the screen and a row show.
enum LargestFiles {
    /// A page's files.
    static let pageSize: Int32 = 50
    /// The most a page holds (the device's limit): the list read again in
    /// place asks for its rows in pages this big.
    static let biggestPage: Int32 = 200
    /// The most rows the list reads again in place; further down it is
    /// read again as far as that.
    static let rereadMost = 1000

    /// L1: ListLargestFiles for the first page ("") or the one after a
    /// token.
    static func request(after: String, limit: Int32 = pageSize) -> Msg_ListLargestFiles {
        var l = Msg_ListLargestFiles()
        l.after = after
        l.limit = limit
        return l
    }

    /// L2: its answer.
    enum Answer: Equatable {
        /// A page, and the next one's token ("" at the end).
        case page([Msg_File], token: String)
        /// A device before the list (unknown_payload).
        case old
        /// No answer, or a refusal: the device's words when it said why
        /// ("" otherwise).
        case failed(String)
    }

    static func answer(_ resp: Msg_RespEnvelope?) -> Answer {
        guard let resp else { return .failed("") }
        if !resp.error, case .respListOfFiles(let lof) = resp.payload { return .page(lof.files, token: lof.token) }
        if ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) { return .old }
        return .failed(CompressRules.said(resp))
    }

    /// L3: a page added under the rows shown. Pages are read afresh, so a
    /// file deleted or added between them can bring one shown already - by
    /// its path, or its content at another path (or compressed since, at its
    /// new size): it isn't shown twice.
    static func addPage(_ shown: [Msg_File], _ page: [Msg_File]) -> [Msg_File] {
        var paths = Set(shown.map(\.path))
        var hashes = Set(shown.map(\.hash).filter { !$0.isEmpty })
        var out = shown
        for f in page {
            if paths.contains(f.path) || (!f.hash.isEmpty && hashes.contains(f.hash)) { continue }
            paths.insert(f.path)
            if !f.hash.isEmpty { hashes.insert(f.hash) }
            out.append(f)
        }
        return out
    }

    /// What the list is doing: a page on its way, more to ask for, at its
    /// end, an older device, or its last page failed (the device's words,
    /// "" for none).
    enum Load: Equatable {
        case loading, more, end, old
        case failed(String)
    }

    /// What its foot shows under the rows.
    enum Foot: Equatable { case loading, failed, none }

    /// L4: what the screen shows.
    enum Shown: Equatable {
        /// The first page on its way.
        case loading
        /// An older device: it needs an update.
        case old
        /// The first page failed: the device's words, else ours, with Try
        /// again.
        case failed(String)
        /// No files at all.
        case empty
        /// The rows, with a spinner, a failure or nothing at their foot.
        case rows(Foot)
    }

    static func shown(rows: Int, load: Load) -> Shown {
        if load == .old { return .old }
        if rows == 0 {
            switch load {
            case .loading, .more: return .loading
            case .failed(let said): return .failed(said)
            default: return .empty
            }
        }
        switch load {
        case .loading: return .rows(.loading)
        case .failed: return .rows(.failed)
        default: return .rows(.none)
        }
    }

    /// L5: the next page is asked for only while there is one and none is
    /// on its way.
    static func wantsMore(_ load: Load) -> Bool { load == .more }

    /// L6: "in N places" under a row, for content at several paths: N, else
    /// 0 (nothing said).
    static func places(_ f: Msg_File) -> Int {
        f.hasCopies && f.copies > 1 ? Int(f.copies) : 0
    }

    /// L7: a photo or a video - its row shows a thumbnail, it opens in the
    /// viewer, and picked it enables Compress: by its type, or a HEIC or
    /// HEIF by its name whatever type it came with.
    static func isMedia(_ f: Msg_File) -> Bool {
        let type = (f.mime.split(separator: ";", maxSplits: 1, omittingEmptySubsequences: false).first ?? "")
            .trimmingCharacters(in: .whitespaces).lowercased()
        if type.hasPrefix("image/") || type.hasPrefix("video/") { return true }
        let ext = (f.path as NSString).pathExtension.lowercased()
        return ext == "heic" || ext == "heif"
    }

    /// L8: the selection bar's Compress is on while a photo or a video is
    /// among the picks.
    static func canCompress(_ picks: [Msg_File]) -> Bool {
        picks.contains(where: isMedia)
    }

    /// L9: once a job is over, the list is read again from the top when
    /// anything was compressed (the sizes changed, and so does the order).
    static func reloadsAfter(compressed: Int) -> Bool { compressed > 0 }

    /// L10: the folder a row is in, as it is shown under its name ("/a/b",
    /// "/" at the top).
    static func folder(of path: String) -> String {
        let dir = (path as NSString).deletingLastPathComponent
        return dir.isEmpty ? "/" : dir
    }

    static func name(of path: String) -> String {
        (path as NSString).lastPathComponent
    }

    /// The picked rows, in the list's order.
    static func picked(_ files: [Msg_File], _ selected: Set<String>) -> [Msg_File] {
        files.filter { selected.contains($0.path) }
    }

    /// The picked photos and videos Compress asks about, in the list's
    /// order, with the content they are shown with.
    static func compressPicks(_ files: [Msg_File], _ selected: Set<String>) -> [(path: String, hash: String)] {
        picked(files, selected).filter { isMedia($0) && !$0.hash.isEmpty }.map { ($0.path, $0.hash) }
    }

    /// A photo or video as the viewer's item; `thumb`, its row's image.
    static func viewerItem(_ f: Msg_File, thumb: UIImage?) -> PhotoGalleryVM.Item {
        PhotoGalleryVM.Item(
            id: PhotoPaging.itemID(f), path: f.path, mime: f.mime, size: Int(f.fileSize),
            thumbData: nil, localURL: nil, isLocalOnly: false, thumbImage: thumb,
            created: f.hasCreated ? f.created.date : nil, hash: f.hash,
            copies: f.hasCopies ? Int(f.copies) : nil,
            originalHash: f.originalHash, originalRemoved: f.originalRemoved
        )
    }
}

@MainActor
final class LargestFilesModel: ObservableObject {
    typealias Request = @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope

    /// Through OTCConnection, as a page is waited for.
    static let viaDevice: Request = { payload in
        try await OTCConnection.shared.ask("largest", base: OTCConnection.pageTimeout) { $0.payload = payload }
    }

    @Published private(set) var files: [Msg_File] = []
    @Published private(set) var load: LargestFiles.Load = .loading
    @Published var selected: Set<String> = []
    /// Bumped when rows got their thumbnails: they are looked up again.
    @Published private(set) var thumbsVersion = 0
    /// A file other than a photo or video is being fetched to open it.
    @Published private(set) var openingPath: String?
    /// What Quick Look shows (as Files opens such a file).
    @Published var previewURL: URL? {
        didSet { if oldValue != previewURL { TempFiles.remove(oldValue) } }
    }
    /// A line over the list for a while: an open that failed.
    @Published private(set) var toast: String?

    /// Sends ListLargestFiles. Tests answer in the device's place.
    var request: Request = LargestFilesModel.viaDevice
    /// A page that failed is asked for again: 1 s doubling to 10 s, at once
    /// on a wake, while the list shows.
    let retry = PageRetry()
    /// The rows' thumbnails: from the cache on this phone, else the device
    /// by path (GridThumbLoader, as Images' tiles).
    let thumbs: GridThumbLoader
    /// The compress job started here, followed until it is over (every
    /// 10 s once its dialog has closed).
    private(set) var compressFollow: CompressModel?

    /// The next page's `after`.
    private var after = ""
    /// Bumped by reload() and reread(): what was on its way for the list
    /// before lands nowhere.
    private var generation = 0
    private var shown = false
    /// The viewer changed what the list holds (an edit, a copy saved, other
    /// content than was shown): read again as it closes.
    private var rereadOnClose = false
    /// The list is being read again in place (reread): Try again reads it
    /// again, not the next page.
    private var rereading = false
    private var wakes: AnyCancellable?
    private var toasts = 0

    init(thumbCache: ThumbDiskCache = .shared) {
        thumbs = GridThumbLoader(maxPt: PhotoGridMetrics.decodeSide, cache: thumbCache, request: GridThumbLoader.deviceRequest)
        thumbs.onImages = { [weak self] in self?.thumbsVersion &+= 1 }
        // Signed in again, a network came up, back in front: a page that
        // failed goes again now.
        wakes = OTCConnection.shared.$wakes
            .dropFirst()
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.retry.wake() }
    }

    /// What the screen shows (L4).
    var view: LargestFiles.Shown { LargestFiles.shown(rows: files.count, load: load) }

    /// The screen came up: the first page, unless it has one; a page that
    /// failed meanwhile, again.
    func appeared() async {
        shown = true
        compressFollow?.watched = true
        if files.isEmpty && load == .loading && generation == 0 {
            await reload()
        } else if case .failed = load {
            if rereading { await reread() } else { await page(generation) }
        }
    }

    /// The screen went: a page that failed isn't asked for meanwhile, nor is
    /// the job followed.
    func disappeared() {
        shown = false
        retry.reset()
        compressFollow?.watched = false
    }

    /// The list from its first page, as the screen opens.
    func reload() async {
        generation += 1
        rereading = false
        after = ""
        files = []
        selected = []
        load = .more
        thumbs.reset()
        retry.reset()
        await page(generation)
    }

    /// The list read again from the top as far as it was shown (at most
    /// rereadMost rows, in the device's biggest pages) and put in place at
    /// once - after a pull, an edit or a compress - so the rows are where
    /// their sizes now put them and the list keeps its place. Failing, the
    /// rows stay as they are, its foot says so, and it is read again as a
    /// page is asked again.
    func reread() async {
        generation += 1
        let gen = generation
        rereading = true
        retry.reset()
        let want = min(max(files.count, Int(LargestFiles.pageSize)), LargestFiles.rereadMost)
        load = .loading
        var next = ""
        var got: [Msg_File] = []
        while true {
            let limit = Int32(min(Int(LargestFiles.biggestPage), max(1, want - got.count)))
            let resp = try? await request(.reqListLargestFiles(LargestFiles.request(after: next, limit: limit)))
            guard gen == generation else { return }
            switch LargestFiles.answer(resp) {
            case .page(let page, let token):
                got = LargestFiles.addPage(got, page)
                next = token
                if token.isEmpty || got.count >= want || page.isEmpty { return await reread(got, next, gen) }
            case .old:
                rereading = false
                load = .old
                return
            case .failed(let said):
                load = .failed(said)
                retry.failed { [weak self] in
                    guard let self, self.shown, gen == self.generation else { return }
                    Task { await self.reread() }
                }
                return
            }
        }
    }

    /// The rows read again (`got`, the next page after `next`) in place of
    /// those shown; the picks still there stay picked.
    private func reread(_ got: [Msg_File], _ next: String, _ gen: Int) async {
        rereading = false
        files = got
        after = next
        load = next.isEmpty ? .end : .more
        let paths = Set(got.map(\.path))
        selected = selected.filter { paths.contains($0) }
        thumbs.reset()
        let media = got.filter(LargestFiles.isMedia)
        _ = await thumbs.takePage(media, ids: media.map(PhotoPaging.itemID))
        // The rows on screen look for their thumbnails again (their ids
        // change with it).
        if gen == generation { thumbsVersion &+= 1 }
    }

    /// The row at `index` came on screen: the next page once the end is
    /// near.
    func rowShown(_ index: Int) {
        guard index >= files.count - 15, LargestFiles.wantsMore(load) else { return }
        let gen = generation
        Task { await page(gen) }
    }

    /// Try again, at the foot or in place of the list: the page now, the
    /// backoff from the start.
    func tryAgain() {
        retry.reset()
        if rereading {
            Task { await reread() }
            return
        }
        let gen = generation
        Task { await page(gen) }
    }

    private func page(_ gen: Int) async {
        guard gen == generation else { return }
        switch load {
        case .more, .failed: break
        default: return
        }
        load = .loading
        let resp = try? await request(.reqListLargestFiles(LargestFiles.request(after: after)))
        guard gen == generation else { return }
        switch LargestFiles.answer(resp) {
        case .page(let page, let token):
            retry.reset()
            let before = files.count
            files = LargestFiles.addPage(files, page)
            after = token
            load = token.isEmpty ? .end : .more
            // Their thumbnails: those the cache has decoded now, the rest
            // asked for as their rows show.
            let media = files[before...].filter(LargestFiles.isMedia)
            _ = await thumbs.takePage(media, ids: media.map(PhotoPaging.itemID))
            if gen == generation { thumbsVersion &+= 1 }
        case .old:
            load = .old
        case .failed(let said):
            load = .failed(said)
            retry.failed { [weak self] in
                guard let self, self.shown, gen == self.generation else { return }
                Task { await self.page(gen) }
            }
        }
    }

    // MARK: Rows

    /// A row's thumbnail, if decoded (a photo or video's).
    func thumbnail(_ f: Msg_File) -> UIImage? {
        GridThumbCache.cached(id: PhotoPaging.itemID(f), maxPt: PhotoGridMetrics.decodeSide)
    }

    func rowAppeared(_ f: Msg_File) {
        guard LargestFiles.isMedia(f) else { return }
        thumbs.appeared(f.path)
    }

    func rowGone(_ f: Msg_File) {
        guard LargestFiles.isMedia(f) else { return }
        thumbs.disappeared(f.path)
    }

    /// A row on screen has no thumbnail: from the cache, else the device.
    func needThumb(_ f: Msg_File) {
        guard LargestFiles.isMedia(f), !f.hash.isEmpty else { return }
        thumbs.need(GridThumbLoader.Want(id: PhotoPaging.itemID(f), path: f.path, hash: f.hash))
    }

    func toggle(_ path: String) {
        if selected.contains(path) { selected.remove(path) } else { selected.insert(path) }
    }

    func clearSelection() {
        selected.removeAll()
    }

    /// L8.
    var canCompress: Bool { LargestFiles.canCompress(LargestFiles.picked(files, selected)) }

    /// The photos and videos as the viewer pages through them, in the
    /// list's order.
    func viewerItems() -> [PhotoGalleryVM.Item] {
        files.filter(LargestFiles.isMedia).map { LargestFiles.viewerItem($0, thumb: thumbnail($0)) }
    }

    /// Anything other than a photo or video, as Files opens it: fetched in
    /// pieces to a file of its own, then shown by Quick Look.
    func open(_ f: Msg_File) async {
        guard openingPath == nil else { return }
        openingPath = f.path
        defer { openingPath = nil }
        let tmp = TempFiles.named(.preview, LargestFiles.name(of: f.path))
        do {
            try await FileDownload.download(path: f.path, mime: f.mime, to: tmp)
            previewURL = tmp
        } catch is FileDownload.Refused {
            TempFiles.remove(tmp)
            say(S.appFilesOpenFailed)
        } catch {
            TempFiles.remove(tmp)
            say(S.appFilesDownloadFailed(detail: error.localizedDescription))
        }
    }

    private func say(_ text: String) {
        toasts += 1
        let mine = toasts
        toast = text
        Announce.polite(text)
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(max(2.5, Double(text.count) / 18)))
            if self?.toasts == mine { self?.toast = nil }
        }
    }

    // MARK: The viewer

    /// The viewer told of a change at `path` (PhotoGalleryVM.onDeleted),
    /// its items now `shown`: a file deleted leaves the list at once -
    /// content left at other paths comes back as the list is read again,
    /// once the viewer closes; anything else (an edit, a copy saved, other
    /// content than was shown) is read again then (reread).
    func viewerChanged(_ path: String, shown: [PhotoGalleryVM.Item]) {
        if !shown.contains(where: { $0.path == path }), let i = files.firstIndex(where: { $0.path == path }) {
            let f = files.remove(at: i)
            selected.remove(path)
            if LargestFiles.places(f) > 0 { rereadOnClose = true }
        } else {
            rereadOnClose = true
        }
    }

    func viewerClosed() {
        guard rereadOnClose else { return }
        rereadOnClose = false
        Task { await reread() }
    }

    // MARK: Compress

    /// The bar's Compress: the dialog for the picked photos and videos.
    /// The device taking them clears the selection and the list follows the
    /// job: each one compressed takes its old row's place (J6), and once it
    /// is over the list is read again in place if anything was (L9).
    func compressModel() -> CompressModel {
        let m = CompressModel(items: CompressRules.items(LargestFiles.compressPicks(files, selected)))
        m.onStarted = { [weak self, weak m] in
            self?.clearSelection()
            self?.compressFollow = m
        }
        m.onResults = { [weak self] in self?.compressed($0) }
        m.onFinished = { [weak self, weak m] compressed in
            guard let self else { return }
            if self.compressFollow === m { self.compressFollow = nil }
            if LargestFiles.reloadsAfter(compressed: compressed) { Task { await self.reread() } }
        }
        return m
    }

    /// J6: each file compressed in its row's place - the device's File over
    /// the old one (a new hash and size, a .jpg for a PNG), its copies
    /// kept. Its old thumbnail leaves the cache on this phone.
    func compressed(_ results: [Msg_CompressResult]) {
        for r in results where r.state == "done" && r.hasFile {
            guard let i = files.firstIndex(where: { $0.path == r.path }) else { continue }
            let old = files[i]
            let f = CompressRules.compressedFile(shown: old, r.file)
            if !old.hash.isEmpty && old.hash != f.hash { thumbs.cache.discard(old.hash) }
            files[i] = f
            if selected.remove(old.path) != nil { selected.insert(f.path) }
        }
    }
}

// MARK: - The screen

/// Settings › Largest files: the section's heading, the link and what the
/// list is for.
struct LargestFilesSection: View {
    var body: some View {
        Section {
            NavigationLink(S.appSettingsLargestOpen) { LargestFilesView() }
        } header: {
            Text(S.appSettingsLargestTitle)
        } footer: {
            Text(S.appSettingsLargestHint)
        }
    }
}

struct LargestFilesView: View {
    @StateObject private var model = LargestFilesModel()
    /// Photos and videos open in the Images section's viewer, over the
    /// list's photos and videos (PhotoGalleryVM.showFiles).
    @StateObject private var viewer = PhotoGalleryVM()
    /// The bar's Compress dialog while it shows.
    @State private var compressing: CompressModel?

    var body: some View {
        List {
            Text(S.appFilesLargestIntro)
                .font(.footnote)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .listRowSeparator(.hidden)
            content
        }
        .listStyle(.plain)
        .refreshable { await model.reread() }
        .navigationTitle(S.appFilesLargestTitle)
        .navigationBarTitleDisplayMode(.inline)
        .safeAreaInset(edge: .bottom) {
            if !model.selected.isEmpty {
                SelectionActionBar(
                    count: model.selected.count,
                    onClear: { model.clearSelection() },
                    onCompress: { compressing = model.compressModel() },
                    compressEnabled: model.canCompress
                )
                .padding(.vertical, 8)
                .transition(.move(edge: .bottom))
            }
        }
        .animation(.easeInOut(duration: 0.15), value: model.selected.isEmpty)
        .overlay(alignment: .top) {
            if let toast = model.toast {
                Text(toast)
                    .padding(.horizontal, 12).padding(.vertical, 8)
                    .background(.ultraThinMaterial, in: Capsule())
                    .padding(.top, 8)
            }
        }
        .task { await model.appeared() }
        .onDisappear { model.disappeared() }
        .sheet(item: $compressing) { CompressSheet(model: $0) }
        .sheet(isPresented: Binding(get: { model.previewURL != nil }, set: { if !$0 { model.previewURL = nil } })) {
            if let url = model.previewURL {
                QuickLookView(url: url, onDismiss: { model.previewURL = nil })
            }
        }
        // What the viewer says once it has closed (a delete refused, other
        // content than was shown), as Files shows it.
        .modifier(ViewerNotice(viewer: viewer))
        .onChange(of: viewer.openIndex == nil) { _, closed in
            if closed { model.viewerClosed() }
        }
        .fullScreenCover(isPresented: Binding(
            get: { viewer.openIndex != nil },
            set: { if !$0 { viewer.closeModal() } }
        )) {
            ImageModal(
                vm: viewer,
                save: {
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
                delete: { viewer.deleteCurrentPhoto(everyCopy: $0) }
            )
        }
    }

    @ViewBuilder
    private var content: some View {
        switch model.view {
        case .loading:
            HStack(spacing: 8) {
                ProgressView()
                Text(S.commonLoading).foregroundStyle(.secondary)
            }
            .listRowSeparator(.hidden)
        case .old:
            Text(S.appFilesLargestNeedsUpdate)
                .foregroundStyle(.secondary)
                .listRowSeparator(.hidden)
        case .failed(let said):
            problem(said)
        case .empty:
            Text(S.appFilesLargestEmpty)
                .foregroundStyle(.secondary)
                .listRowSeparator(.hidden)
        case .rows(let foot):
            ForEach(Array(model.files.enumerated()), id: \.element.path) { i, f in
                row(f)
                    .onAppear { model.rowShown(i) }
            }
            switch foot {
            case .loading:
                HStack {
                    Spacer()
                    ProgressView()
                    Spacer()
                }
                .listRowSeparator(.hidden)
                .accessibilityLabel(S.commonLoading)
            case .failed:
                problem("")
            case .none:
                EmptyView()
            }
        }
    }

    /// The list, or its next page, didn't come: the device's words, else
    /// ours, and Try again. It is asked for again by itself meanwhile.
    private func problem(_ said: String) -> some View {
        let text = said.isEmpty ? S.appFilesLargestFailed : said
        return VStack(alignment: .leading, spacing: 10) {
            Text(text).foregroundStyle(.secondary)
            Button(LoadProblem.tryAgain) { model.tryAgain() }
                .buttonStyle(.bordered)
        }
        .listRowSeparator(.hidden)
        .onAppear { Announce.polite(text) }
    }

    private func row(_ f: Msg_File) -> some View {
        let picked = model.selected.contains(f.path)
        let name = LargestFiles.name(of: f.path)
        let folder = LargestFiles.folder(of: f.path)
        let size = formatByteCount(f.fileSize)
        let places = LargestFiles.places(f)
        let placesText = places > 0 ? S.appFilesLargestPlaces(count: places) : nil
        return HStack(spacing: 12) {
            // The row's own checkbox, always there, as Files' rows have.
            Button {
                model.toggle(f.path)
            } label: {
                Image(systemName: picked ? "checkmark.circle.fill" : "circle")
                    .foregroundColor(picked ? .accentColor : .secondary)
                    .frame(width: 28, height: 28)
                    .contentShape(Rectangle())
            }
            // .plain: inside a List a default Button claims the whole
            // row's tap, which has to stay "open".
            .buttonStyle(.plain)
            .accessibilityLabel(picked ? S.commonDeselect : S.commonSelect)
            thumb(f)
            VStack(alignment: .leading, spacing: 2) {
                Text(name).lineLimit(1).truncationMode(.middle)
                Text(folder)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.head)
                if let placesText {
                    Text(placesText)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer(minLength: 8)
            if model.openingPath == f.path {
                ProgressView()
            } else {
                Text(size)
                    .font(.subheadline)
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
            }
        }
        .contentShape(Rectangle())
        .onTapGesture { tap(f) }
        .onLongPressGesture { model.toggle(f.path) }
        .onAppear { model.rowAppeared(f) }
        .onDisappear { model.rowGone(f) }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([name, folder, size, placesText].compactMap { $0 }.joined(separator: ", "))
        .accessibilityAddTraits(picked ? [.isButton, .isSelected] : .isButton)
        .accessibilityAction { tap(f) }
        .accessibilityAction(named: picked ? S.commonDeselect : S.commonSelect) { model.toggle(f.path) }
    }

    /// A photo's or video's thumbnail (grey until it comes), else the file
    /// type's icon.
    @ViewBuilder
    private func thumb(_ f: Msg_File) -> some View {
        if LargestFiles.isMedia(f) {
            let img = model.thumbnail(f)
            Group {
                if let img {
                    Image(uiImage: img).resizable().scaledToFill()
                } else {
                    Color.secondary.opacity(0.15)
                }
            }
            .frame(width: 44, height: 44)
            .clipShape(RoundedRectangle(cornerRadius: 4))
            .overlay(alignment: .bottomLeading) {
                if f.mime.hasPrefix("video/") {
                    Image(systemName: "play.circle.fill")
                        .font(.caption)
                        .foregroundStyle(.white)
                        .shadow(radius: 1)
                        .padding(2)
                }
            }
            .id("\(f.path)#\(f.hash)#\(model.thumbsVersion)")
            .task(id: img == nil) { if img == nil { model.needThumb(f) } }
        } else {
            FileTypeIcon(name: f.path)
                .frame(width: 44, height: 44)
        }
    }

    /// While something is picked a tap picks or leaves out, as in Images
    /// and Files; otherwise a photo or video opens in the viewer, paging
    /// through the list's, and anything else as Files opens it.
    private func tap(_ f: Msg_File) {
        if !model.selected.isEmpty {
            model.toggle(f.path)
            return
        }
        guard model.openingPath == nil else { return }
        guard LargestFiles.isMedia(f) else {
            Task { await model.open(f) }
            return
        }
        let items = model.viewerItems()
        guard let start = items.firstIndex(where: { $0.path == f.path }) else { return }
        viewer.onDeleted = { [weak model, weak viewer] path in
            model?.viewerChanged(path, shown: viewer?.items ?? [])
        }
        viewer.showFiles(items, startAt: start)
    }

    /// The open item's thumbnail, standing in for Save and Share until the
    /// full size arrives.
    private func viewerThumb() -> UIImage? {
        guard let i = viewer.openIndex, viewer.items.indices.contains(i) else { return nil }
        return viewer.items[i].thumbImage
    }
}
