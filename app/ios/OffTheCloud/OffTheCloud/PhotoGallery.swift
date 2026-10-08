// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI
import SwiftProtobuf
import Photos
import MapKit
import CryptoKit
import AVKit
import ImageIO
import Combine

// MARK: - Proto typealiases (rename if your generated names differ)
typealias ReqEnvelope       = Msg_ReqEnvelope
typealias RespEnvelope      = Msg_RespEnvelope
typealias FileMsg           = Msg_File
typealias TagsListMsg       = Msg_TagsList
typealias SearchPhotosMsg   = Msg_SearchPhotos
typealias GetFileMsg        = Msg_GetFile
typealias UploadFileMsg     = Msg_UploadFile
typealias ShareFilesLinkMsg = Msg_ShareFilesLink
typealias DownloadSharedMsg = Msg_DownloadSharedLink
typealias AckMsg            = Msg_Ack

/// SearchPhotos.limit for the request that starts a photo search (no
/// token): a small first page paints quickly over a slow upload. Pages
/// that continue the token send none and get the device's own size; a
/// device before release 97 ignores it and answers its default, 30.
let cFirstPhotoPageLimit: Int32 = 12

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
        // The Files grid's cached thumbnail, when the viewer is opened from
        // there (it holds decoded images, not the bytes thumbData carries).
        var thumbImage: UIImage? = nil
        // The month the photo was taken in ("2024-03", the viewer's time
        // zone), worked out once when it arrives: the grid's month titles
        // (PhotoMonths.swift). nil without a date.
        var month: String? = nil
        // When it was taken: what VoiceOver reads on its tile ("Photo,
        // 5 October 2026", as the web's tiles). nil without a date.
        var created: Date? = nil
    }

    // Shared, already-authenticated connection (see OTCConnection.swift)
    private let ws = OTCConnection.shared
    private let deviceID: String
    private let localFolder: URL?

    // UI state
    @Published var tags: [String] = []
    @Published var chips: [String] = []

    // Issue #52 follow-up: the person filter. People are picked in the
    // search (TopSearch.swift) and show as chips over it. Selecting more
    // than one person means AND, not OR - see dao.SearchMedia on the
    // backend: a photo must contain a face matched to *every* person
    // selected, not just one. Loaded, and offered, only while face
    // recognition is on (FaceRecognition.swift).
    @Published var allPeople: [Msg_Person] = [] {
        didSet { faceCache.removeAll() }
    }
    @Published var selectedPeople: [String] = []
    // People's faces, decoded from their cover thumbnails as the search
    // and the chips first show them.
    private var faceCache: [String: UIImage] = [:]
    private var facesWatch: AnyCancellable?
    // Issue #192: Files keeping a folder out of Images, or showing it again.
    private var imagesWatch: AnyCancellable?
    // When the tags and people were last fetched: a search that ends a
    // while later fetches them again (new photos bring new ones), so the
    // next search has them and nothing moves while picking.
    private var libraryFetchedAt = Date()
    /// A listing of the people has come back since face recognition was
    /// last turned on: the People page shows them rather than its
    /// placeholder faces.
    @Published private(set) var peopleLoaded = false

    // Issue #115: image groups, which the app calls collections. A group is
    // one more filter on the same search (SearchPhotos.group_id), which is
    // what keeps tags, people, the date scrubber and paging all working
    // unchanged inside one - activeGroup just rides along in every request.
    @Published var groups: [Msg_ImageGroup] = []
    /// A listing of the collections has come back: the Collections page
    /// shows them rather than its placeholder cards.
    @Published private(set) var groupsLoaded = false
    @Published var activeGroup: Msg_ImageGroup? = nil
    @Published var showGroups = false
    /// The People page as a sheet over Images (a narrow window; a wide
    /// one's menu shows it as a page of its own). Here rather than in the
    /// view so that turning the phone can move it between the two.
    @Published var showPeople = false
    /// What the People page is in the middle of (picked faces, a merge or
    /// a delete being asked about), kept here for the same reason.
    let peoplePage = PeoplePageState()
    // The selection bar's "Add to collection" flow: pick an existing group,
    // or name a new one.
    @Published var showGroupPicker = false
    @Published var showNewGroupName = false
    @Published var newGroupName = ""
    @Published var showRenameGroup = false
    @Published var renameGroupName = ""
    @Published var confirmDeleteGroup = false

    // Issue #77: the date scrubber. Only meaningful against date order - a
    // tag search sorts by relevance (dao.SearchMedia switches to "order by
    // score desc" whenever tags are given) - so showScrubber below hides
    // it outright rather than showing ticks against an order it doesn't
    // reflect. A person filter alone is fine, that keeps created-desc.
    struct DateBucket: Identifiable { let month: String; let count: Int; let start: Int; let end: Int; var id: String { month } }
    // The month counts also size the grid's last month (its room for the
    // photos still on their way) and, in an open collection, give the
    // months it spans. They are kept with the filter they count (people
    // and the open collection - tags never change them), and shown only
    // for that: after a filter change the previous list is still here
    // until the new one comes, and it isn't this filter's (the web's
    // bucketsFresh).
    @Published private(set) var dateBuckets: [DateBucket] = []
    private var bucketsKey: String?
    private var bucketGeneration = 0
    /// What the date buckets count for the current filter; nil out of date
    /// order (a tag search), which has none.
    private var bucketKeyNow: String? {
        chips.isEmpty ? "\(selectedPeople.joined(separator: ","))|\(activeGroup?.id ?? "")" : nil
    }
    /// The date buckets are the current filter's (the web's bucketsFresh).
    var bucketsFresh: Bool { bucketsKey != nil && bucketsKey == bucketKeyNow }
    /// The current filter's buckets, newest first; empty until they come.
    var freshBuckets: [DateBucket] { bucketsFresh ? dateBuckets : [] }

    /// What a person filter shows over its photos (the web's PersonHeader,
    /// shown for people alone - no tags, no open collection): the people
    /// picked, in order, as far as the list of people has them, and how
    /// many photos they are in once the date buckets are theirs. nil when
    /// nobody is picked; `loaded` false while the list of people is on
    /// its way.
    struct PersonFilter: Equatable {
        var loaded: Bool
        var people: [Msg_Person]
        var total: Int?

        /// The header for `selected` people with `tags` and the open
        /// collection `group`, from the list of people (`loaded` once it
        /// came) and the photos' count (nil until it is this filter's).
        static func of(selected: [String], tags: [String], group: String?, loaded: Bool,
                       people: [Msg_Person], total: Int?) -> PersonFilter? {
            guard !selected.isEmpty, tags.isEmpty, group == nil else { return nil }
            guard loaded else { return PersonFilter(loaded: false, people: [], total: nil) }
            let picked = selected.compactMap { id in people.first { $0.id == id } }
            guard !picked.isEmpty else { return nil }
            return PersonFilter(loaded: true, people: picked, total: total)
        }
    }
    var personFilter: PersonFilter? {
        .of(selected: selectedPeople, tags: chips, group: activeGroup?.id, loaded: peopleLoaded,
            people: allPeople, total: bucketsFresh ? totalPhotos : nil)
    }
    // scrubFrac is the live drag position (0=newest/top, 1=oldest/bottom),
    // nil whenever the user isn't actively dragging. placeholderCount
    // outlives the drag itself: it stays set (grey tiles under the month's
    // title in place of the real grid) from the moment a drag starts until
    // the jump-to-date fetch it triggers actually resolves, so releasing
    // the thumb doesn't flash an empty grid while the real thumbnails are
    // still in flight - and the photos land where the grey tiles were.
    @Published var scrubFrac: Double? = nil
    @Published var placeholderCount: Int? = nil
    @Published var placeholderMonth: String? = nil

    /// A month that hasn't loaded shows this many grey tiles at most while
    /// the scrubber is on it: enough to read as "a lot", not thousands of
    /// views (the web's cMaxPlaceholders).
    static let maxPlaceholders = 300

    var totalPhotos: Int { freshBuckets.last?.end ?? 0 }
    var showScrubber: Bool { !freshBuckets.isEmpty }

    // Ticks: one per year, positioned by cumulative photo count rather
    // than calendar-uniform spacing, so a drag fraction actually
    // corresponds to "how far into the library" that year sits (matches
    // Google Photos' own timeline, where a sparse year takes less track
    // space than a busy one). Mirrors web's PhotoGallery.tsx.
    var yearTicks: [(year: String, pct: Double)] {
        guard totalPhotos > 0 else { return [] }
        var ticks: [(String, Double)] = []
        var lastYear = ""
        for b in freshBuckets {
            let year = String(b.month.prefix(4))
            if year != lastYear {
                ticks.append((year, Double(b.start) / Double(totalPhotos)))
                lastYear = year
            }
        }
        return ticks
    }

    var scrubTarget: DateBucket? {
        guard let frac = scrubFrac else { return nil }
        let buckets = freshBuckets
        return PhotoMonths.bucketIndex(atFraction: frac, counts: buckets.map(\.count)).map { buckets[$0] }
    }

    /// The scrubber is dragged to `fraction` of its track: grey tiles under
    /// that month's title stand in for the grid. No request here - the
    /// count comes from the buckets already loaded, so dragging across
    /// years costs nothing but renders.
    func previewScrub(_ fraction: Double) {
        scrubFrac = min(1, max(0, fraction))
        guard let target = scrubTarget else { return }
        if placeholderMonth != target.month { placeholderMonth = target.month }
        let count = min(target.count, Self.maxPlaceholders)
        if placeholderCount != count { placeholderCount = count }
    }

    /// The scrubber is let go: the photos jump to its month, landing under
    /// that month's title where the grey tiles were.
    func endScrub() {
        // A touch that never scrubbed: a jump on its way keeps its tiles.
        guard scrubFrac != nil else { return }
        let target = scrubTarget // before scrubFrac goes
        scrubFrac = nil
        guard let target else {
            placeholderCount = nil
            placeholderMonth = nil
            return
        }
        placeholderMonth = target.month
        placeholderCount = min(target.count, Self.maxPlaceholders)
        jumpToDate(target.month)
    }

    @Published var items: [Item] = [] {
        didSet { itemsVersion &+= 1 }
    }
    /// Bumped by every change to `items`: the grid's layout is worked out
    /// again only then (gridLayout).
    private var itemsVersion = 0
    private struct LayoutKey: Equatable {
        var items: Int
        var metrics: PhotoGridMetrics
        var dated: Bool
        var lastRoom: Int?
        var placeholder: PhotoGridLayout.Placeholder
        var placeholderCount: Int
    }
    private var layoutCache: (key: LayoutKey, layout: PhotoGridLayout)?

    /// The grid at this width, as rows (PhotoMonths.swift): the scrubber's
    /// grey tiles under their month's title, the first page's skeleton, or
    /// the photos under their months - none in a tag search, which is
    /// ordered by how well photos match. Kept until the photos, the width
    /// or what decides the layout changes, so drawing a frame never groups
    /// anything.
    func gridLayout(_ metrics: PhotoGridMetrics) -> PhotoGridLayout {
        let dated = chips.isEmpty
        var placeholder = PhotoGridLayout.Placeholder.none
        var count = 0
        if let n = placeholderCount, let month = placeholderMonth {
            placeholder = .month(month)
            count = n
        } else if items.isEmpty && (loading || firstPagePending) && !fixedList {
            placeholder = .skeleton(titled: dated)
            count = PhotoGridLayout.skeletonTiles
        }
        // The last month's room: what its bucket says it will hold, while
        // more pages are on their way.
        var lastRoom: Int?
        // (The last month's: a photo without a date stays with the one
        // before it.)
        if dated, placeholder == .none, !endReached, let month = items.last(where: { $0.month != nil })?.month {
            lastRoom = freshBuckets.first { $0.month == month }?.count
        }
        let key = LayoutKey(items: itemsVersion, metrics: metrics, dated: dated, lastRoom: lastRoom,
                            placeholder: placeholder, placeholderCount: count)
        if let c = layoutCache, c.key == key { return c.layout }
        let layout = placeholder == .none
            ? PhotoGridLayout.build(months: items.map(\.month), ids: items.map(\.id), dated: dated,
                                    lastRoom: lastRoom, metrics: metrics)
            : PhotoGridLayout.placeholder(placeholder, count: count, metrics: metrics)
        layoutCache = (key, layout)
        return layout
    }

    @Published var loading = false
    /// A search has started and its first page isn't here yet (nor did it
    /// fail): grey tiles rather than an empty page, from the very first
    /// paint (the web's firstLoad).
    @Published private(set) var firstPagePending = false
    /// Bumped by every search that replaces the grid (a filter change):
    /// the grid goes back to its top, as the web scrolls the window up -
    /// its grey tiles would otherwise show wherever the last search was
    /// scrolled to, under no title.
    @Published private(set) var searchStarts = 0
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
    // Full-size images by path, kept for the last few opened so the pager
    // can draw the neighbour it is sliding towards and swiping back to a
    // photo doesn't fetch it again. hiResImage is the open one's.
    //
    // Display bitmaps, capped at 4096 px and decoded off the main thread
    // (decodeForDisplay); hiResSource keeps the bytes each came from, for
    // Save and Share at full resolution. Bounded by count and by bytes:
    // eight 24 MP photos decoded at full size were ~800 MB.
    @Published var hiResImages: [String: UIImage] = [:]
    private var hiResSource: [String: HiResSource] = [:]
    private var hiResOrder: [String] = []
    private var inFlightHiRes = Set<String>()
    private let cHiResBudget = 200 << 20

    enum HiResSource {
        case data(Data)
        case file(URL)
    }
    // Photos whose full-size fetch came back empty or errored - the viewer
    // keeps showing the thumbnail, and its "Low res" badge drops the
    // spinner since nothing is on its way any more.
    @Published private(set) var hiResFailed = Set<String>()
    private let cHiResCacheSize = 8
    var hiResImage: UIImage? {
        guard let i = openIndex, items.indices.contains(i) else { return nil }
        return hiResImages[items[i].path]
    }
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
    /// Drives the selection bar's share sheet - see shareSelected().
    @Published var selectionShareURL: URL? = nil
    /// Which selection action is waiting on the device, so the bar can
    /// show a spinner rather than looking like the tap did nothing.
    @Published var preparing: SelectionActionTask?

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
        // Face recognition turned on: its people, for the search. Turned
        // off: nobody to search for any more.
        facesWatch = FaceRecognition.shared.$enabled
            .removeDuplicates()
            .dropFirst()
            .sink { [weak self] on in
                Task { @MainActor in self?.faceRecognitionChanged(on == true) }
            }
        imagesWatch = NotificationCenter.default.publisher(for: .otcImagesChanged)
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                Task { @MainActor in self?.imagesChanged() }
            }
    }

    // The Files section opens its photos and videos in this same viewer:
    // an instance made with init() only ever shows the list handed to
    // showFiles - no search, no paging, no local merge.
    private var fixedList = false
    /// Called with the path after the viewer deleted a file, so the screen
    /// that opened it can re-read its listing.
    var onDeleted: ((String) -> Void)?

    init() {
        self.deviceID = ""
        self.localFolder = nil
        self.fixedList = true
    }

    /// Fills the viewer with these items (a folder's photos and videos, in
    /// the order the Files screen shows them) and opens the one at startAt.
    func showFiles(_ files: [Item], startAt: Int) {
        items = files
        selected.removeAll()
        open(index: startAt)
    }

    func onAppearInitial() {
        guard !fixedList else { return }
        libraryAsked = true
        photosAsked = true
        firstPagePending = true
        Task {
            await loadTags()
            if FaceRecognition.shared.isOn { await loadPeople() }
            await resetAndLoadFirstPage()
        }
    }

    private func faceRecognitionChanged(_ on: Bool) {
        guard !fixedList else { return }
        if on {
            Task { await loadPeople() }
            return
        }
        allPeople = []
        peopleLoaded = false
        if !selectedPeople.isEmpty {
            selectedPeople = []
            restartSearch()
        }
    }

    private var libraryAsked = false
    /// Images has shown its photos: a change to what it shows asks again.
    private var photosAsked = false

    /// Issue #192: Files kept a folder out of Images, or showed it there
    /// again. Its photos leave the grid or come back, and the tags and
    /// faces found only in them are gone (or come back as they are found),
    /// so everything Images has asked for so far is asked for again: the
    /// photos, the tags and people the search offers (People shows the
    /// same list), and the collections, whose counts and covers leave out
    /// what is kept out.
    func imagesChanged() {
        guard !fixedList else { return }
        if libraryAsked {
            libraryFetchedAt = Date()
            Task {
                await loadTags()
                if FaceRecognition.shared.isOn { await loadPeople() }
            }
        }
        if groupsLoaded { Task { await loadGroups() } }
        if photosAsked { restartSearch() }
    }

    /// Sends the library's own requests (the tags, the collections). Tests
    /// answer them in the device's place.
    var libraryRequest: @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope = { payload in
        try await OTCConnection.shared.request { $0.payload = payload }
    }

    /// The tags and people for the search, once, without the photos: the
    /// wide layout's top bar searches from any section, before Images
    /// has ever been shown.
    func loadLibraryOnce() {
        guard !fixedList, !libraryAsked else { return }
        libraryAsked = true
        libraryFetchedAt = Date()
        Task {
            await loadTags()
            if FaceRecognition.shared.isOn { await loadPeople() }
        }
    }

    /// The search ended: the tags and people are fetched again if the last
    /// time was a while ago (the web's refreshIfStale).
    func refreshLibraryIfStale() {
        guard !fixedList, Date().timeIntervalSince(libraryFetchedAt) > 5 * 60 else { return }
        libraryFetchedAt = Date()
        Task {
            await loadTags()
            if FaceRecognition.shared.isOn { await loadPeople() }
        }
    }

    /// The search's clear button: no tags, no people (an open collection
    /// stays open, as on the web).
    func clearSearch() {
        guard !chips.isEmpty || !selectedPeople.isEmpty else { return }
        chips = []
        selectedPeople = []
        restartSearch()
    }

    /// A person's face, as the search and its chips show it.
    func face(for p: Msg_Person) -> UIImage? {
        if let img = faceCache[p.id] { return img }
        guard !p.coverThumbnail.isEmpty, let img = UIImage(data: p.coverThumbnail) else { return nil }
        faceCache[p.id] = img
        return img
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
            let resp = try await libraryRequest(.reqGetTags(.init()))
            if case .respTagsList(let tl) = resp.payload {
                self.tags = tl.tags
            }
        } catch { /* ignore */ }
    }

    // MARK: Date scrubber (issue #77)
    private func loadDateBuckets() async {
        // A newer load (another filter, or the same after a delete) wins,
        // whichever answer lands last.
        bucketGeneration += 1
        let gen = bucketGeneration
        guard let key = bucketKeyNow else { return }
        let people = selectedPeople // snapshot - see fetchPage's own doc comment on why
        let group = activeGroup?.id ?? ""
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            var b = Msg_ReqPhotoDateBuckets()
            b.personIds = people
            b.groupID = group // issue #115
            b.includeVideos = true // issue #106: same set the grid shows
            req.payload = .reqPhotoDateBuckets(b)
            e = req
        }) else { return }
        guard gen == bucketGeneration, case .respPhotoDateBuckets(let r) = resp.payload else { return }
        setDateBuckets(r.buckets.map { ($0.month, Int($0.count)) }, key: key)
    }

    /// The month counts (newest first) for the filter `key` names.
    func setDateBuckets(_ list: [(month: String, count: Int)], key: String?) {
        var cum = 0
        bucketsKey = key
        dateBuckets = list.map { b in
            let start = cum
            cum += b.count
            return DateBucket(month: b.month, count: b.count, start: start, end: cum)
        }
    }

    /// The bucket key of the filter as it is now, for setDateBuckets.
    var currentBucketKey: String? { bucketKeyNow }

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
        // The bucket's month is Gregorian, whatever calendar the phone is
        // set to: so is its last instant (PhotoMonths.calendar).
        guard let before = PhotoMonths.lastInstant(of: month) else { return }

        searchGeneration += 1
        let myGeneration = searchGeneration
        loading = false
        morePendingAt = nil // an ask from the old grid's tiles, not this one's
        endReached = false
        token = ""
        items = []
        selected.removeAll()
        await fetchPage(overrideToken: "", before: before)
        // placeholderCount is left showing until this resolves (or is
        // superseded) - cleared here rather than by the caller so a jump
        // that gets superseded by a *newer* jump/filter change doesn't
        // clear placeholders that newer request is still relying on. A
        // scrub begun meanwhile keeps its own grey tiles: its jump clears
        // them.
        if myGeneration == searchGeneration && scrubFrac == nil {
            placeholderCount = nil
            placeholderMonth = nil
        }
    }

    // MARK: People (issue #52 follow-up)

    /// Asks the device for its people: for the search, and every time the
    /// People page opens (new faces are found as photos arrive). True once
    /// they are here.
    @discardableResult
    func loadPeople() async -> Bool {
        guard let resp = try? await peopleRequest(.reqListPeople(.init())) else { return false }
        // Turned off while the list was on its way: nobody to offer.
        guard FaceRecognition.shared.isOn, case .respPeople(let p) = resp.payload else { return false }
        allPeople = p.people
        peopleLoaded = true
        return true
    }

    /// Images picked in the wide layout's menu: the whole library, as the
    /// web's showAll - whatever was searched for, and an open collection,
    /// are left.
    func showAll() {
        guard !chips.isEmpty || !selectedPeople.isEmpty || activeGroup != nil else { return }
        chips = []
        selectedPeople = []
        activeGroup = nil
        restartSearch()
    }

    /// One person's photos, from the People page: a new search, as the
    /// web's showPerson - no tags, no other people, no open collection.
    func showPerson(_ id: String) {
        chips = []
        selectedPeople = [id]
        activeGroup = nil
        restartSearch()
    }

    /// Sends one of People's requests (the list, a name, a merge, a
    /// deletion). Tests answer them in the device's place.
    var peopleRequest: @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope = { payload in
        try await OTCConnection.shared.request { $0.payload = payload }
    }

    /// How a request the device answers with an Ack went.
    enum AckResult { case ok, refused, unanswered }

    private func ack(_ payload: Msg_ReqEnvelope.OneOf_Payload) async -> AckResult {
        guard let resp = try? await peopleRequest(payload) else { return .unanswered }
        if case .respAck(let a) = resp.payload, a.ok { return .ok }
        return .refused
    }

    /// Names a person, or takes the name off ("" - RenamePerson also gives
    /// an unnamed person their first name).
    func renamePerson(_ id: String, to name: String) async -> AckResult {
        var r = Msg_RenamePerson()
        r.id = id
        r.name = name
        let res = await ack(.reqRenamePerson(r))
        if res == .ok, let i = allPeople.firstIndex(where: { $0.id == id }) { allPeople[i].name = name }
        return res
    }

    enum MergeResult { case merged, nameNotSaved, notMerged, unanswered }

    /// Issue #74: folds the others into the face kept - one person the
    /// face matching split in several. A name the kept face lacks is given
    /// to it first: merged first and named after, a failed rename would
    /// lose a name whose only owner was just merged away. This way round a
    /// failure loses nothing (the web's PeopleView does the same).
    func mergePeople(keep: Msg_Person, sources: [String], name: String) async -> MergeResult {
        guard !sources.isEmpty else { return .notMerged }
        if !name.isEmpty && name != PeopleOrder.tidy(keep.name) {
            switch await renamePerson(keep.id, to: name) {
            case .ok: break
            case .refused: return .nameNotSaved
            case .unanswered: return .unanswered
            }
        }
        var m = Msg_MergePeople()
        m.targetID = keep.id
        m.sourceIds = sources
        switch await ack(.reqMergePeople(m)) {
        case .ok:
            forgetPeople(Set(sources))
            // The kept face's count has grown, and the order with it
            // (issue #75): asked again rather than added up here.
            Task { await loadPeople() }
            return .merged
        case .refused: return .notMerged
        case .unanswered: return .unanswered
        }
    }

    /// One DeletePerson per person, a few at a time. Their photos stay;
    /// the faces matched to them go. `progress` hears of each answer.
    func deletePeople(_ ids: [String], progress: @escaping () -> Void) async -> (deleted: Set<String>, unanswered: Int) {
        var deleted = Set<String>()
        var unanswered = 0
        await withTaskGroup(of: (String, AckResult).self) { group in
            var next = 0
            func add() {
                guard next < ids.count else { return }
                let id = ids[next]
                next += 1
                group.addTask { @MainActor in
                    var d = Msg_DeletePerson()
                    d.id = id
                    return (id, await self.ack(.reqDeletePerson(d)))
                }
            }
            for _ in 0..<min(3, ids.count) { add() }
            while let (id, res) = await group.next() {
                if res == .ok { deleted.insert(id) } else if res == .unanswered { unanswered += 1 }
                progress()
                add()
            }
        }
        if !deleted.isEmpty {
            forgetPeople(deleted)
            Task { await loadPeople() }
        }
        return (deleted, unanswered)
    }

    /// Merged away or deleted: off the list at once, before it comes
    /// again, and no longer narrowing Images.
    private func forgetPeople(_ ids: Set<String>) {
        allPeople.removeAll { ids.contains($0.id) }
        let filtered = selectedPeople.filter { !ids.contains($0) }
        if filtered.count != selectedPeople.count {
            selectedPeople = filtered
            restartSearch()
        }
    }

    // MARK: Image groups (issue #115)
    /// false: the device didn't answer with the list.
    @discardableResult
    func loadGroups() async -> Bool {
        guard let resp = try? await libraryRequest(.reqListImageGroups(.init())) else { return false }
        guard case .respImageGroups(let g) = resp.payload else { return false }
        groups = g.groups
        groupsLoaded = true
        // Keep the chip's name/count fresh if the open group changed.
        if let open = activeGroup, let fresh = g.groups.first(where: { $0.id == open.id }) {
            activeGroup = fresh
        }
        return true
    }

    /// A collection's photos, from Collections (the wide layout's page or
    /// Images' sheet): a new search, as the web's openGroup - the tags and
    /// people searched for before go, and can be added again inside it.
    func openGroup(_ g: Msg_ImageGroup) {
        showGroups = false
        chips = []
        selectedPeople = []
        activeGroup = g
        restartSearch()
    }

    func leaveGroup() {
        activeGroup = nil
        restartSearch()
    }

    func renameActiveGroup() async {
        guard let g = activeGroup else { return }
        let name = renameGroupName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty, name != g.name else { return }
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            var r = Msg_RenameImageGroup()
            r.id = g.id
            r.name = name
            req.payload = .reqRenameImageGroup(r)
            e = req
        }), case .respAck(let ack) = resp.payload, ack.ok else {
            alertMessage = "Could not rename the collection."; showAlert = true
            return
        }
        activeGroup?.name = name
        if let idx = groups.firstIndex(where: { $0.id == g.id }) { groups[idx].name = name }
    }

    /// Removes the group only - the pictures are kept.
    func deleteActiveGroup() async {
        guard let g = activeGroup else { return }
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            var d = Msg_DeleteImageGroup()
            d.id = g.id
            req.payload = .reqDeleteImageGroup(d)
            e = req
        }), case .respAck(let ack) = resp.payload, ack.ok else {
            alertMessage = "Could not delete the collection."; showAlert = true
            return
        }
        groups.removeAll { $0.id == g.id }
        leaveGroup()
    }

    // Both send the selection's paths; the device resolves them to hashes.
    func createGroupFromSelection() async {
        let name = newGroupName.trimmingCharacters(in: .whitespacesAndNewlines)
        newGroupName = ""
        guard !name.isEmpty else { return }
        let paths = Array(selected)
        guard await ensureUploadedIfLocal(paths) else { return }
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            var c = Msg_CreateImageGroup()
            c.name = name
            c.paths = paths
            req.payload = .reqCreateImageGroup(c)
            e = req
        }), case .respImageGroup(let r) = resp.payload else {
            alertMessage = "Could not create the collection."; showAlert = true
            return
        }
        groups.insert(r.group, at: 0)
        selected.removeAll()
        alertMessage = "Collection \"\(name)\" created."; showAlert = true
    }

    func addSelectionToGroup(_ g: Msg_ImageGroup) async {
        let paths = Array(selected)
        guard await ensureUploadedIfLocal(paths) else { return }
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            var a = Msg_AddToImageGroup()
            a.groupID = g.id
            a.paths = paths
            req.payload = .reqAddToImageGroup(a)
            e = req
        }), case .respAck(let ack) = resp.payload, ack.ok else {
            alertMessage = "Could not add to the collection."; showAlert = true
            return
        }
        selected.removeAll()
        await loadGroups()
        // If that was the open group, its grid has new members to show.
        if activeGroup?.id == g.id { restartSearch() }
        alertMessage = "Added to \"\(g.name)\"."; showAlert = true
    }

    func togglePerson(_ id: String) {
        if let idx = selectedPeople.firstIndex(of: id) {
            selectedPeople.remove(at: idx)
        } else {
            selectedPeople.append(id)
        }
        restartSearch()
    }

    // MARK: Paging
    func resetAndLoadFirstPage() async {
        guard !fixedList else { return }
        // Invalidates any still-in-flight fetchPage from the *previous*
        // selection before this one's own request even goes out - see
        // searchGeneration's doc comment.
        searchGeneration += 1
        searchStarts += 1
        loading = false
        // An ask from the previous selection's tiles: its index means
        // nothing against the new items, and would pull a page nobody
        // scrolled to.
        morePendingAt = nil
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
        placeholderMonth = nil
        firstPagePending = true
        let myGeneration = searchGeneration
        async let buckets: Void = loadDateBuckets()
        await fetchPage(overrideToken: "")
        if myGeneration == searchGeneration { firstPagePending = false }
        await buckets
        mergeLocalIfAny()
    }

    /// The tile of the photo at `idx` (whose id is `id`) came on screen.
    func loadMoreIfNeeded(index idx: Int, id: String) async {
        guard !fixedList else { return }
        guard !endReached else { return }
        // Near the end is the only thing worth acting on - checked before
        // the in-flight case below so a tile appearing at the top of the
        // grid can't queue up a page nobody needs yet. A tile of a grid
        // that has been replaced since (a new search) asks nothing.
        guard idx >= items.count - 12, items.indices.contains(idx), items[idx].id == id else { return }
        // A tile that appears while a fetch is already running used to
        // just return. Nothing then asked again: the only thing that can
        // trigger the next page is a tile appearing for the first time
        // (.task runs once per tile), so a page that produced no new
        // tiles left the grid permanently stuck. Remember the ask instead
        // and let the in-flight fetch pick it up when it lands.
        guard !loading else {
            morePendingAt = max(morePendingAt ?? idx, idx)
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

    /// The furthest tile (its index) that asked for more while a fetch was
    /// already running, so the fetch that lands can honour it (see
    /// loadMoreIfNeeded).
    private var morePendingAt: Int?

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
                if let at = morePendingAt, !endReached {
                    morePendingAt = nil
                    // Only if that tile is still near the end. Every tile
                    // of a small first page asks at once, and the page
                    // that just landed moved the end well past them;
                    // honouring them anyway pulled a third page nobody
                    // had scrolled to.
                    if at >= items.count - 12 {
                        // Detached from this call so the defer isn't
                        // waiting on another round trip.
                        Task { await self.fetchUntilProgress() }
                    }
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
        let group = activeGroup?.id ?? ""
        let requestToken = overrideToken ?? token ?? ""

        do {
            let resp = try await ws.request { e in
                var req = ReqEnvelope()
                var sp  = SearchPhotosMsg()
                sp.tags  = tags
                sp.personIds = people
                sp.groupID = group // issue #115: inside a group, only its members
                // Issue #106: videos belong in the Images section. They
                // were excluded when this flag arrived (issue #60, where
                // only the composer opted in), which left a device's
                // videos unbrowsable from the app entirely.
                sp.includeVideos = true
                sp.token = requestToken
                // A search starting here (a filter, the scrubber's jump)
                // gets a small first page; scrolling on, the full size.
                if requestToken.isEmpty { sp.limit = cFirstPhotoPageLimit }
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
            // Gregorian, in the viewer's time zone: the device's buckets'.
            let calendar = PhotoMonths.calendar()
            for f in lof.files {
                let id = "\(f.path)#\(f.hash)#\(f.fileSize)"
                newItems.append(Item(
                    id: id,
                    path: f.path,
                    mime: f.mime,
                    size: Int(f.fileSize),
                    thumbData: f.hasContent ? f.content : nil,
                    localURL: nil,
                    isLocalOnly: false,
                    month: f.hasCreated ? PhotoMonths.key(for: f.created.date, calendar: calendar) : nil,
                    created: f.hasCreated ? f.created.date : nil
                ))
            }
            // Decoded off the main thread, at tile size, before the tiles
            // first draw (see GridThumbCache).
            await GridThumbCache.prewarm(newItems.map { ($0.id, $0.thumbData) }, maxPt: PhotoTile.decodeSide)
            guard myGeneration == searchGeneration, !Task.isCancelled else { return false }
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
        videoPlayer?.pause()
        videoPlayer = nil
        infoOpen = false
        infoData = nil
        Task { await fetchHiRes(index: index) }
        // The viewer slides towards the neighbours, so have them ready.
        for n in [index - 1, index + 1] where items.indices.contains(n) {
            Task { await fetchHiRes(index: n, prefetch: true) }
        }
    }
    func closeModal() {
        openIndex = nil
        hiResImages.removeAll()
        hiResSource.removeAll()
        hiResOrder.removeAll()
        hiResFailed.removeAll()
        videoPlayer?.pause()
        videoPlayer = nil
    }

    private func cacheHiRes(_ img: UIImage, source: HiResSource, for path: String) {
        var images = hiResImages
        if images[path] == nil { hiResOrder.append(path) }
        images[path] = img
        hiResSource[path] = source
        // The pager draws the open photo and both neighbours: never those.
        let drawn = Set((openIndex.map { [$0 - 1, $0, $0 + 1] } ?? [])
            .filter { items.indices.contains($0) }
            .map { items[$0].path })
        var bytes = hiResOrder.reduce(0) { $0 + (images[$1].map(Self.cost(of:)) ?? 0) }
        var i = 0
        while i < hiResOrder.count && (hiResOrder.count > cHiResCacheSize || bytes > cHiResBudget) {
            let p = hiResOrder[i]
            if drawn.contains(p) { i += 1; continue }
            hiResOrder.remove(at: i)
            bytes -= images.removeValue(forKey: p).map(Self.cost(of:)) ?? 0
            hiResSource.removeValue(forKey: p)
        }
        hiResImages = images
    }

    private static func cost(of image: UIImage) -> Int {
        if let cg = image.cgImage { return cg.bytesPerRow * cg.height }
        return Int(image.size.width * image.scale * image.size.height * image.scale) * 4
    }

    /// Decodes now, at most `maxSide` px (Android's cap, ~3x the widest
    /// iPhone screen; a 12 MP photo is not downscaled at all), honouring
    /// the EXIF orientation as UIImage(data:) does. Off the main thread:
    /// UIImage(data:) decoded lazily, during the slide animation.
    nonisolated static func decodeForDisplay(_ src: CGImageSource, maxSide: Int = 4096) -> UIImage? {
        let opts: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: maxSide,
        ]
        guard let cg = CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary) else { return nil }
        return UIImage(cgImage: cg, scale: 1, orientation: .up)
    }

    /// The open photo at full resolution, for Save and Share: decoded from
    /// the same bytes, the same way, as before the display cap. nil while
    /// it hasn't arrived.
    func fullImageForOpen() async -> UIImage? {
        guard let i = openIndex, items.indices.contains(i), let src = hiResSource[items[i].path] else { return nil }
        return await Task.detached(priority: .userInitiated) { () -> UIImage? in
            switch src {
            case .data(let d): return UIImage(data: d)
            case .file(let u): return UIImage(contentsOfFile: u.path)
            }
        }.value
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
            onDeleted?(item.path)
            if !fixedList { Task { await loadDateBuckets() } }
            if items.isEmpty {
                closeModal()
            } else {
                let nextIdx = min(idx, items.count - 1)
                open(index: nextIdx)
            }
        }
    }

    /// prefetch: a neighbour being readied for the slide - its image is
    /// fetched, but a video is not started until it is actually opened.
    private func fetchHiRes(index: Int, prefetch: Bool = false) async {
        guard items.indices.contains(index) else { return }
        let it = items[index]

        // Issue #106: a video can't be decoded into a UIImage - it gets
        // written out and played instead. Its own thumbnail already stands
        // in on screen while this runs, so there is no blank frame.
        if it.mime.hasPrefix("video/") {
            if !prefetch { await fetchVideo(it) }
            return
        }

        if hiResImages[it.path] != nil || inFlightHiRes.contains(it.path) { return }
        inFlightHiRes.insert(it.path)
        defer { inFlightHiRes.remove(it.path) }
        if let u = it.localURL {
            let img = await Task.detached(priority: .userInitiated) {
                CGImageSourceCreateWithURL(u as CFURL, nil).flatMap { Self.decodeForDisplay($0) }
            }.value
            if let img {
                // Closed meanwhile: don't fill the cache again.
                if openIndex != nil { cacheHiRes(img, source: .file(u), for: it.path) }
                return
            }
        }
        hiResFailed.remove(it.path)
        do {
            let resp = try await ws.request { e in
                var req = ReqEnvelope()
                var gf  = GetFileMsg()
                gf.path = it.path
                req.payload = .reqGetFile(gf)
                e = req
            }
            if case .respFile(let f) = resp.payload {
                let data = f.content
                let img = await Task.detached(priority: .userInitiated) {
                    CGImageSourceCreateWithData(data as CFData, nil).flatMap { Self.decodeForDisplay($0) }
                }.value
                if let img {
                    if openIndex != nil { cacheHiRes(img, source: .data(data), for: it.path) }
                } else {
                    hiResFailed.insert(it.path)
                }
            } else {
                hiResFailed.insert(it.path)
            }
        } catch { hiResFailed.insert(it.path) }
    }

    /// Issue #106/#107: fetches a video and hands back a player.
    ///
    /// The extension is load-bearing: AVFoundation works out how to demux a
    /// file:// URL from its path extension, so writing everything as .mp4
    /// (as the social feed used to) leaves a QuickTime recording - what an
    /// iPhone actually produces - mislabelled and silently unplayable.
    private func fetchVideo(_ it: Item) async {
        // Still on this video? Stepping on before a slow stream URL or
        // download came back used to start the previous video over the
        // next one.
        func stillOpen() -> Bool { openIndex.flatMap { items.indices.contains($0) ? items[$0].path : nil } == it.path }
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
            guard stillOpen() else { return }
            print("[video] streaming \(it.path) from \(streamURL.absoluteString)")
            let player = MediaStream.player(for: streamURL)
            Self.logFailure(of: player, what: "stream")
            self.videoPlayer = player
            return
        }
        print("[video] no stream URL for \(it.path) - downloading the whole file")

        do {
            // In 4 MiB pieces straight to disk (FileDownload), not as one
            // message held whole in memory; named once the mime is known.
            let raw = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
            let (mime, size) = try await FileDownload.download(path: it.path, mime: it.mime, to: raw)
            guard stillOpen() else {
                try? FileManager.default.removeItem(at: raw)
                return
            }
            let ext: String
            switch mime.lowercased() {
            case "video/quicktime": ext = "mov"
            case "video/mp4", "video/x-m4v": ext = "mp4"
            case "video/x-matroska": ext = "mkv"
            case "video/3gpp": ext = "3gp"
            default:
                let own = (it.path as NSString).pathExtension
                ext = own.isEmpty ? "mp4" : own.lowercased()
            }
            let tmp = raw.appendingPathExtension(ext)
            try FileManager.default.moveItem(at: raw, to: tmp)
            print("[video] downloaded \(it.path): \(size) bytes, \(mime), .\(ext)")
            guard stillOpen() else { return }
            let player = AVPlayer(url: tmp)
            Self.logFailure(of: player, what: "download")
            self.videoPlayer = player
        } catch {
            print("[video] download of \(it.path) failed: \(error)")
        }
    }

    /// Prints why a video didn't play - AVPlayer only shows a crossed-out
    /// play icon otherwise.
    private static var failureObservers: [NSKeyValueObservation] = []
    private static func logFailure(of player: AVPlayer, what: String) {
        guard let item = player.currentItem else { return }
        failureObservers.append(item.observe(\.status, options: [.new]) { item, _ in
            if item.status == .failed {
                print("[video] \(what) failed: \(item.error.map { String(describing: $0) } ?? "no error")")
            } else if item.status == .readyToPlay {
                print("[video] \(what) ready to play")
            }
        })
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
                    let created = SwiftProtobuf.Google_Protobuf_Timestamp(date: Date())

                    // Issue #58: skip re-sending content the device
                    // already has under some other path — see
                    // PhotoSync.swift's identical check for the full
                    // reasoning.
                    // Issue #165: hashed and uploaded straight from the
                    // file in chunks, never loaded whole - and hashed off
                    // the main actor, which this view model is on.
                    let hash = try await Task.detached(priority: .userInitiated) {
                        try OTCConnection.sha256Hex(of: url)
                    }.value
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
                        _ = try await ws.uploadChunked(path: p, source: .file(url),
                                                       forceOverride: true, created: created,
                                                       sha256: hash)
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
                // The months' counts: the scrubber and the last month's room.
                await loadDateBuckets()
            }
        }
    }

    /// Share, as the Files tab means it: a link to the selection, handed
    /// to the system share sheet.
    ///
    /// This replaced two buttons. "Share in social" is gone because
    /// publishing belongs on the Social tab, which is where you write a
    /// caption anyway, and "Share link" is gone because copying a link to
    /// the clipboard is one of the things the share sheet already offers.
    func shareSelected() {
        Task {
            preparing = .share
            defer { preparing = nil }
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
                    selectionShareURL = url
                } else {
                    alertMessage = "Could not create share link."
                    showAlert = true
                }
            } catch {
                alertMessage = "Share failed: \(error.localizedDescription)"
                showAlert = true
            }
        }
    }

    func downloadZip() {
        Task {
            preparing = .download
            defer { preparing = nil }
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

}

// MARK: - SwiftUI View (iOS)

struct PhotoGalleryView: View {
    // MainView's, as is the search: the wide layout's top bar searches,
    // and its menu's People and Collections pages show, the same library
    // (AppMenu.swift), and turning the phone keeps all of it.
    @ObservedObject var vm: PhotoGalleryVM
    @ObservedObject var search: TopSearchModel
    @ObservedObject private var faces = FaceRecognition.shared
    @ObservedObject private var filesNav = FilesNav.shared
    // A wide window: the search is in the top bar and People and
    // Collections are in the menu, so this view shows neither - only an
    // open collection's chip, and the photos.
    @Environment(\.wideLayout) private var wide
    @FocusState private var searchFocused: Bool
    /// The search's header height, where its panel starts, and whether the
    /// keyboard shows: the panel may rise over the field (SearchPanelPlace).
    @State private var headerHeight: CGFloat = 0
    @State private var keyboardUp = false
    // Issue #180: set to start the "Share as Gallery" flow - one for the
    // view itself (the open group's chip), one for the groups sheet, which
    // has to present the flow from inside itself.
    @State private var gallerySource: Msg_SharedGallerySource?
    @State private var groupsSheetGallerySource: Msg_SharedGallerySource?
    // Issue #123: as many columns as fit, so the grid uses the whole width
    // on an iPad instead of the fixed three that only ever made sense on a
    // phone - and still exactly three on a phone. The sizes are the web's
    // (PhotoGridMetrics), read from the window's width as its CSS reads
    // the viewport's, and the grid's own.
    @Environment(\.windowWidth) private var windowWidth
    @State private var gridWidth: CGFloat = 0
    /// The first photo of the row at the top of the grid (see widthChanged).
    @State private var scrollMemo = GridScrollMemo()

    init(vm: PhotoGalleryVM, search: TopSearchModel) {
        self.vm = vm
        self.search = search
    }

    var body: some View {
        VStack(spacing: 0) {
            // The search (TopSearch.swift), its chips, and the open
            // collection - only the last on a wide window.
            if !wide || vm.activeGroup != nil {
                header
                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { headerHeight = $0 }
            }

            // Grid - in date order the photos stand under month titles, and
            // months short of a row sit side by side (PhotoMonths.swift, the
            // web's PhotoGallery.tsx). While the date scrubber has a target
            // bucket (dragging, or the jump it triggered still in flight),
            // grey tiles under that month's title stand in for the real
            // grid rather than showing whatever was scrolled to before the
            // jump started, and the photos land where they were. Capped at
            // 300 - a month with thousands of photos doesn't need that many
            // real views just to convey "this is a lot of squares".
            // Wrapped in ScrollViewReader (issue #77) to reset the scroll
            // position to the top once a jump starts - the page doesn't
            // otherwise know to, since `items` being reset doesn't itself
            // move an already-scrolled ScrollView - and to keep the photo
            // at the top there when the width changes.
            ScrollViewReader { proxy in
                let layout = vm.gridLayout(metrics)
                // The scroll-to-top anchor is a plain (zero-height) sibling
                // of the grid inside the ScrollView: as the grid's own first
                // child it was a cell, pushing every photo over by one. A
                // VStack wrapping the *ScrollView* itself (tried briefly)
                // made the grid's very first load render blank until a
                // scroll gesture forced SwiftUI to lay it out.
                ScrollView {
                    VStack(spacing: 0) {
                        // A new search comes back to the page's top (the
                        // web's scrollTo(0, 0)); the scrubber to the
                        // grid's, past the person's header (its
                        // gridRef.scrollIntoView).
                        Color.clear.frame(height: 0).id("photoPageTop")
                        if let person = vm.personFilter {
                            PersonHeader(filter: person, phone: layout.metrics.phone, face: vm.face(for:))
                        }
                        Color.clear.frame(height: 0).id("photoGridTop")
                        // One row per line of the grid, lazily: a big
                        // month's title spans the row over its own rows of
                        // tiles; months sharing a line are one row.
                        LazyVStack(alignment: .leading, spacing: 0) {
                            ForEach(layout.rows) { row in
                                gridRow(row, layout: layout)
                                    .padding(.top, row.top)
                            }
                        }
                        .scrollTargetLayout()
                        if layout.placeholder == .none && vm.loading && !vm.items.isEmpty {
                            ProgressView().frame(height: 60)
                        }
                    }
                    .padding(.horizontal, layout.metrics.pad)
                    .padding(.bottom, 8)
                }
                .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { w in
                    widthChanged(to: w, proxy: proxy)
                }
                .onChange(of: vm.searchStarts) { _, _ in
                    proxy.scrollTo("photoPageTop", anchor: .top)
                }
                // The row at the top, as the grid scrolls - kept aside, not
                // in state: nothing needs drawing again for it.
                .onScrollTargetVisibilityChange(idType: String.self, threshold: 0.5) { ids in
                    scrollMemo.visible(ids)
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
                    SelectionActionBar(
                        count: vm.selected.count,
                        busy: vm.preparing,
                        onShare: vm.shareSelected,
                        onDownload: vm.downloadZip,
                        onDelete: { vm.confirmDeleteSelected = true },
                        onGroup: {
                            Task { await vm.loadGroups() }
                            vm.showGroupPicker = true
                        },
                        // Issue #180: the selected photos, as a gallery -
                        // no group needed.
                        onGallery: { gallerySource = .paths(Array(vm.selected).sorted()) }
                    )
                    .transition(.move(edge: .bottom))
                    .sheet(isPresented: Binding(
                        get: { vm.selectionShareURL != nil },
                        set: { if !$0 { vm.selectionShareURL = nil } }
                    )) {
                        if let url = vm.selectionShareURL { ActivityView(items: [url]) }
                    }
                }
            }
        }
        // The search's suggestions, over the photos while something is
        // typed - and over the search too on a window too short for them
        // under it while the keyboard shows.
        .overlay {
            if panelShown && !wide {
                GeometryReader { proxy in
                    narrowPanel(height: proxy.size.height)
                }
            }
        }
        .keyboardShown($keyboardUp)
        .onChange(of: searchFocused) { _, focused in
            if focused { search.open = true }
        }
        // Sheets on a narrow window only: a wide one's menu has these as
        // pages (MainView moves one over when the window changes).
        .sheet(isPresented: Binding(get: { vm.showPeople && !wide }, set: { vm.showPeople = $0 })) {
            NavigationStack {
                PeopleView(vm: vm, onOpenPhotos: { vm.showPeople = false }, onClose: { vm.showPeople = false })
            }
        }
        // Face recognition turned off (here in Settings, or on the web):
        // no People to show.
        .onChange(of: faces.isOn) { _, on in
            if !on { vm.showPeople = false }
        }
        .onChange(of: search.query) { _, _ in
            if searchFocused && !search.typed.isEmpty { search.open = true }
        }
        // Issue #115: the groups list - a picture on the left, like the
        // notifications rows.
        .sheet(isPresented: Binding(get: { vm.showGroups && !wide }, set: { vm.showGroups = $0 })) {
            NavigationStack {
                List {
                    if vm.groups.isEmpty {
                        Text("No collections yet — select some pictures and choose Add to collection.")
                            .foregroundStyle(.secondary)
                    }
                    ForEach(vm.groups, id: \.id) { g in
                        Button { vm.openGroup(g) } label: {
                            HStack(spacing: 12) {
                                if let ui = UIImage(data: g.coverThumbnail) {
                                    Image(uiImage: ui)
                                        .resizable().scaledToFill()
                                        .frame(width: 44, height: 44)
                                        .clipShape(RoundedRectangle(cornerRadius: 6))
                                } else {
                                    RoundedRectangle(cornerRadius: 6)
                                        .fill(Color.secondary.opacity(0.15))
                                        .frame(width: 44, height: 44)
                                        .overlay(NavIconView(.collections, size: 24).foregroundStyle(.secondary))
                                }
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(g.name).foregroundStyle(.primary)
                                    Text("\(g.fileCount) \(g.fileCount == 1 ? "picture" : "pictures")")
                                        .font(.caption).foregroundStyle(.secondary)
                                }
                            }
                        }
                        // Issue #180: long-press or swipe a group to share it.
                        .contextMenu {
                            Button {
                                groupsSheetGallerySource = .group(g.id)
                            } label: {
                                Label("Share as Gallery", systemImage: "photo.on.rectangle.angled")
                            }
                        }
                        .swipeActions(edge: .trailing) {
                            Button {
                                groupsSheetGallerySource = .group(g.id)
                            } label: {
                                Label("Share", systemImage: "square.and.arrow.up")
                            }
                            .tint(.accentColor)
                        }
                    }
                }
                .navigationTitle("Collections")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .navigationBarTrailing) {
                        Button("Done") { vm.showGroups = false }
                    }
                }
            }
            .sharedGalleryShareFlow(source: $groupsSheetGallerySource)
        }
        // The selection bar's "Add to collection": pick an existing group or start a new one.
        .confirmationDialog("Add \(vm.selected.count) to a collection", isPresented: $vm.showGroupPicker, titleVisibility: .visible) {
            ForEach(vm.groups, id: \.id) { g in
                Button(g.name) { Task { await vm.addSelectionToGroup(g) } }
            }
            Button("New collection…") { vm.showNewGroupName = true }
            Button("Cancel", role: .cancel) {}
        }
        .alert("New collection", isPresented: $vm.showNewGroupName) {
            TextField("Collection name", text: $vm.newGroupName)
            Button("Create") { Task { await vm.createGroupFromSelection() } }
            Button("Cancel", role: .cancel) { vm.newGroupName = "" }
        } message: {
            Text("\(vm.selected.count) \(vm.selected.count == 1 ? "picture" : "pictures") will be added to it.")
        }
        .alert("Rename collection", isPresented: $vm.showRenameGroup) {
            TextField("Collection name", text: $vm.renameGroupName)
            Button("Save") { Task { await vm.renameActiveGroup() } }
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog(
            "Delete the collection \"\(vm.activeGroup?.name ?? "")\"?",
            isPresented: $vm.confirmDeleteGroup, titleVisibility: .visible
        ) {
            Button("Delete collection", role: .destructive) { Task { await vm.deleteActiveGroup() } }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The pictures themselves are kept.")
        }
        .sharedGalleryShareFlow(source: $gallerySource)
        .onAppear { vm.onAppearInitial() }
        .confirmationDialog(
            "Delete \(vm.selected.count) item\(vm.selected.count == 1 ? "" : "s")?",
            isPresented: $vm.confirmDeleteSelected,
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive, action: vm.deleteSelected)
            Button("Cancel", role: .cancel) {}
        }
        .alert(vm.alertMessage, isPresented: $vm.showAlert) { Button("OK", role: .cancel) {} }
        // Presented once and left up while paging: the previous sheet was
        // keyed by the index, so every swipe dismissed it and presented a
        // new one - the "closes and reopens" the viewer used to do. Full
        // screen, like the Photos app and the Android viewer.
        .fullScreenCover(isPresented: Binding(
            get: { vm.openIndex != nil },
            set: { if !$0 { vm.closeModal() } }
        )) {
            ImageModal(
                vm: vm,
                save: {
                    let fallback = vm.openIndex.flatMap { idxFromThumb($0) }
                    Task { vm.saveToPhotos(await vm.fullImageForOpen() ?? fallback) }
                },
                share: {
                    let fallback = vm.openIndex.flatMap { idxFromThumb($0) }
                    Task { vm.shareCurrentPhoto(await vm.fullImageForOpen() ?? fallback) }
                },
                delete: { vm.deleteCurrentPhoto() }
            )
        }
    }

    // MARK: The grid

    /// The grid's sizes at its width (the window's until it is measured).
    private var metrics: PhotoGridMetrics {
        let window = windowWidth > 0 ? windowWidth : (wide ? WideLayout.minWidth : 390)
        return PhotoGridMetrics(width: gridWidth > 0 ? gridWidth : window, windowWidth: window)
    }

    /// The grid's width changed (the phone turned, the menu opened or
    /// closed, a window resized): every row moves when the tiles change
    /// size, so the photo that was at the top is brought back there - its
    /// row, or its month's title when it starts the month.
    private func widthChanged(to width: CGFloat, proxy: ScrollViewProxy) {
        let before = metrics
        gridWidth = width
        let after = metrics
        guard before.cols != after.cols || abs(before.tile - after.tile) > 0.5,
              let item = scrollMemo.topItem, item > 0 else { return }
        scrollMemo.restoring = true
        // Once the rows at the new width are there.
        DispatchQueue.main.async {
            let layout = vm.gridLayout(metrics)
            if layout.placeholder == .none, let r = layout.rowIndex(forItem: item) {
                proxy.scrollTo(layout.rows[r].id, anchor: .top)
            }
            DispatchQueue.main.async { scrollMemo.restoring = false }
        }
    }

    @ViewBuilder
    private func gridRow(_ row: PhotoGridLayout.Row, layout: PhotoGridLayout) -> some View {
        let m = layout.metrics
        switch row.kind {
        case .title(let month):
            MonthTitle(month: month, skeleton: layout.placeholder != .none && month == nil, metrics: m)
        case .tiles(let range):
            HStack(spacing: m.gap) {
                ForEach(range, id: \.self) { i in tile(i, layout: layout) }
            }
        case .line(let pieces):
            // Months short of a row, side by side: titles on one line,
            // their tiles level under them.
            HStack(alignment: .top, spacing: m.secGap) {
                ForEach(pieces, id: \.tiles.lowerBound) { p in
                    VStack(alignment: .leading, spacing: m.gap) {
                        MonthTitle(month: p.month, skeleton: layout.placeholder != .none && p.month == nil, metrics: m)
                        HStack(spacing: m.gap) {
                            ForEach(p.tiles, id: \.self) { i in tile(i, layout: layout) }
                        }
                    }
                    .frame(width: m.sectionWidth(p.room), alignment: .leading)
                }
            }
        }
    }

    @ViewBuilder
    private func tile(_ i: Int, layout: PhotoGridLayout) -> some View {
        let side = layout.metrics.tile
        if layout.placeholder == .none, vm.items.indices.contains(i) {
            let it = vm.items[i]
            PhotoTile(
                item: it,
                side: side,
                isSelected: vm.selected.contains(it.path),
                hasSelection: !vm.selected.isEmpty,
                onTap: { openPath(it.path) },
                onLongPress: { vm.toggleSelect(it.path) }
            )
            .task { await vm.loadMoreIfNeeded(index: i, id: it.id) }
        } else {
            // The first page on its way, or the scrubber's month.
            RoundedRectangle(cornerRadius: PhotoTile.corner)
                .fill(Color(.secondarySystemFill))
                .frame(width: side, height: side)
                .accessibilityHidden(true)
        }
    }

    // MARK: The header

    private var header: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !wide {
                if !vm.chips.isEmpty || !vm.selectedPeople.isEmpty {
                    chipsRow
                }
                searchRow
            }
            // Issue #115: the open group, as a chip - tap the name to
            // rename it, × to leave it. Everything else in this bar
            // keeps working inside it.
            if let g = vm.activeGroup {
                collectionChip(g)
            }
        }
        .padding(.vertical, 8)
        // The whole width also when only the collection's chip shows.
        .frame(maxWidth: .infinity, alignment: .leading)
        // An iPad window showing its controls has them over the search
        // field's start (AppMenu.swift).
        .modifier(AvoidsWindowControls())
        .background(.ultraThinMaterial)
    }

    private var searchRow: some View {
        HStack(spacing: 8) {
            searchField
            if searchActive {
                Button("Cancel") { actions.cancel() }
            } else {
                // People, as the web's menu has it before
                // Collections - only while face recognition is on.
                if faces.isOn {
                    Button {
                        vm.showPeople = true
                    } label: {
                        NavIconView(.people, size: 24)
                            .frame(width: 34, height: 34)
                            .contentShape(Rectangle())
                    }
                    .accessibilityLabel("People")
                }
                // Issue #115: the collections list.
                Button {
                    Task { await vm.loadGroups() }
                    vm.showGroups = true
                } label: {
                    NavIconView(.collections, size: 24)
                        .frame(width: 34, height: 34)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel("Collections")
            }
        }
        .padding(.horizontal, 8)
    }

    /// "12 items · Mar – Oct 2024", as the web's collection header: the
    /// months from the date buckets once they are this collection's, and
    /// only the years below 900 wide, as there.
    private func collectionLine(_ g: Msg_ImageGroup) -> String {
        let items = PhotoMonths.count(Int(g.fileCount), "item")
        let span = PhotoMonths.span(vm.freshBuckets.map(\.month), years: (windowWidth > 0 ? windowWidth : 390) < 900)
        return span.isEmpty ? items : "\(items) · \(span)"
    }

    private func collectionChip(_ g: Msg_ImageGroup) -> some View {
        HStack(spacing: 6) {
            NavIconView(.collections, size: 16)
            Button(g.name) {
                vm.renameGroupName = g.name
                vm.showRenameGroup = true
            }
            .buttonStyle(.plain)
            .lineLimit(1)
            // The name keeps the room; its line gives way first.
            .layoutPriority(1)
            Text("· \(collectionLine(g))").foregroundStyle(.secondary).font(.caption).lineLimit(1)
            // Issue #180: the group as a gallery behind a link.
            Button {
                gallerySource = .group(g.id)
            } label: { Image(systemName: "square.and.arrow.up").font(.caption) }
            .accessibilityLabel("Share as Gallery")
            Button {
                vm.confirmDeleteGroup = true
            } label: { Image(systemName: "trash").font(.caption) }
            .accessibilityLabel("Delete collection")
            .foregroundStyle(.red)
            Button("×") { vm.leaveGroup() }
                .accessibilityLabel("Close the collection \(g.name)")
        }
        .padding(.horizontal, 10).padding(.vertical, 5)
        .background(Color.orange.opacity(0.15))
        .clipShape(Capsule())
        .padding(.horizontal, 8)
    }

    // MARK: The search (TopSearch.swift)

    /// Something is typed and the panel is up.
    private var panelShown: Bool { search.open && !search.typed.isEmpty }

    /// The panel under the search, the whole space down to the keyboard
    /// (`height` down). Risen over the search when that space is too short
    /// (SearchPanelPlace): then a card only as tall as its rows.
    private func narrowPanel(height: CGFloat) -> some View {
        let place = SearchPanelPlace(under: headerHeight, bottom: height, ceiling: 4, keyboardUp: keyboardUp, cap: nil)
        return SearchPanelLayout(place: place) {
            TopSearchPanel(
                suggestions: actions.suggestions(),
                typed: search.typed,
                alreadyIn: vm.chips.contains { FoldedText($0).text == FoldedText(search.typed).text },
                selectedPeople: vm.selectedPeople,
                face: vm.face(for:),
                onPick: actions.pick,
                moved: search.moved,
                fitsRows: place.rises
            )
            .clipShape(UnevenRoundedRectangle(bottomLeadingRadius: place.rises ? 16 : 0, bottomTrailingRadius: place.rises ? 16 : 0, style: .continuous))
            .shadow(color: .black.opacity(place.rises ? 0.22 : 0), radius: 24, y: 10)
        }
    }
    /// The field has the keyboard, or its panel shows: Cancel ends it.
    private var searchActive: Bool { searchFocused || panelShown }

    private var actions: TopSearchActions {
        TopSearchActions(vm: vm, search: search, faces: faces.isOn, filesNav: filesNav, focus: { searchFocused = $0 })
    }

    private var searchField: some View {
        HStack(spacing: 6) {
            NavIconView(.search, size: 18)
                .foregroundStyle(.secondary)
            TextField(
                vm.chips.isEmpty && vm.selectedPeople.isEmpty ? "Search photos and files" : "Search",
                text: $search.query
            )
            .focused($searchFocused)
            .submitLabel(.search)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            .onSubmit { actions.submit() }
            // A hardware keyboard: the arrows go through the suggestions and
            // Escape closes them, as on the web.
            .onKeyPress(.upArrow) { actions.arrow(-1) }
            .onKeyPress(.downArrow) { actions.arrow(1) }
            .onKeyPress(.escape) { actions.escape() }
            .accessibilityLabel("Search photos and files")
            if !search.query.isEmpty || !vm.chips.isEmpty || !vm.selectedPeople.isEmpty {
                // Everything typed and picked goes - not an open collection.
                Button {
                    actions.clear()
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.secondary)
                        .frame(width: 28, height: 28)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear search")
            }
        }
        .padding(.leading, 12)
        .padding(.trailing, 4)
        .frame(height: 38)
        .background(Color(.tertiarySystemFill), in: Capsule())
        .contentShape(Capsule())
        .onTapGesture { searchFocused = true }
    }

    /// What the search narrows Images to: the people, then the tags.
    private var chipsRow: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(vm.selectedPeople, id: \.self) { pid in
                    let person = vm.allPeople.first { $0.id == pid }
                    let name = person.map { $0.name.trimmingCharacters(in: .whitespacesAndNewlines) } ?? ""
                    HStack(spacing: 6) {
                        Group {
                            if let person, let img = vm.face(for: person) {
                                Image(uiImage: img).resizable().scaledToFill()
                            } else {
                                Color(.tertiarySystemFill).overlay(NavIconView(.face, size: 16).foregroundStyle(.secondary))
                            }
                        }
                        .frame(width: 24, height: 24)
                        .clipShape(Circle())
                        Text(person == nil ? "Person" : (name.isEmpty ? "Unnamed" : name))
                            .font(.subheadline)
                            .lineLimit(1)
                        chipRemove(name.isEmpty ? "Remove this person" : "Remove \(name)") { vm.togglePerson(pid) }
                    }
                    .padding(.leading, 3)
                    .padding(.trailing, 4)
                    .padding(.vertical, 3)
                    .background(Color.accentColor.opacity(0.15), in: Capsule())
                }
                ForEach(vm.chips, id: \.self) { chip in
                    HStack(spacing: 4) {
                        Text(chip).font(.subheadline).lineLimit(1)
                        chipRemove("Remove \(chip)") { vm.removeChip(chip) }
                    }
                    .padding(.leading, 12)
                    .padding(.trailing, 4)
                    .padding(.vertical, 3)
                    .background(Color.accentColor.opacity(0.15), in: Capsule())
                }
            }
            .padding(.horizontal, 8)
        }
    }

    private func chipRemove(_ label: String, _ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: "xmark")
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
                .frame(width: 24, height: 24)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
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

/// A month's title over its photos: one line, not sticky, as the web's
/// .pg-month. A month one tile wide shows "Sep 2026" where "September
/// 2026" doesn't fit. Photos without a date at the top of the list get an
/// empty line, so the tiles of months beside them stay level; the first
/// page's skeleton a grey bar.
private struct MonthTitle: View {
    let month: String?
    let skeleton: Bool
    let metrics: PhotoGridMetrics

    var body: some View {
        Group {
            if skeleton {
                Capsule()
                    .fill(Color(.secondarySystemFill))
                    .frame(width: 140, height: 14)
                    .padding(.vertical, 3)
                    .accessibilityHidden(true)
            } else if let month {
                ViewThatFits(in: .horizontal) {
                    Text(PhotoMonths.title(month))
                    Text(PhotoMonths.short(month))
                }
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(PhotoMonths.title(month))
                .accessibilityAddTraits(.isHeader)
            } else {
                Text(" ").accessibilityHidden(true)
            }
        }
        .font(.subheadline.weight(.semibold))
        .foregroundStyle(.primary)
        .lineLimit(1)
        .padding(.top, PhotoGridMetrics.titleTop)
        .padding(.bottom, metrics.titleBottom)
        .padding(.horizontal, metrics.titleInset)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// One person's photos (or several people's together), over the grid: up
/// to three faces, the names and how many photos they are in - the web's
/// PersonHeader. It scrolls with the photos. Until the list of people is
/// here, blanks of the same height; until the count is, an empty line, so
/// nothing moves when they come.
private struct PersonHeader: View {
    let filter: PhotoGalleryVM.PersonFilter
    /// A phone's sizes (a window under 600 wide), as the web's.
    let phone: Bool
    let face: (Msg_Person) -> UIImage?

    private var faceSide: CGFloat { phone ? 48 : 56 }

    var body: some View {
        HStack(spacing: phone ? 12 : 16) {
            if filter.loaded {
                HStack(spacing: -16) {
                    ForEach(filter.people.prefix(3), id: \.id) { p in
                        faceView(face(p))
                    }
                }
                .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 2) {
                    Text(PhotoMonths.peopleTitle(filter.people.map(Self.name)))
                        .font(phone ? .title2.weight(.semibold) : .title.weight(.semibold))
                        .lineLimit(2)
                        .accessibilityAddTraits(.isHeader)
                    Text(filter.total.map { PhotoMonths.count($0, "item") } ?? " ")
                        .font(.subheadline)
                        .monospacedDigit()
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .accessibilityHidden(filter.total == nil)
                }
            } else {
                faceView(nil)
                VStack(alignment: .leading, spacing: 0) {
                    Capsule().fill(Color(.secondarySystemFill))
                        .frame(width: 160, height: 22)
                        .padding(.vertical, 7)
                    Capsule().fill(Color(.secondarySystemFill))
                        .frame(width: 72, height: 12)
                        .padding(.top, 6).padding(.bottom, 4)
                }
                .accessibilityHidden(true)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.top, phone ? 8 : 16)
        .padding(.bottom, 4)
    }

    /// A person as the web's personLabel names them.
    private static func name(_ p: Msg_Person) -> String {
        let n = p.name.trimmingCharacters(in: .whitespacesAndNewlines)
        return n.isEmpty ? "Unnamed" : n
    }

    private func faceView(_ img: UIImage?) -> some View {
        Group {
            if let img {
                Image(uiImage: img).resizable().scaledToFill()
            } else {
                Color(.tertiarySystemFill)
                    .overlay(NavIconView(.face, size: 28).foregroundStyle(.secondary))
            }
        }
        .frame(width: faceSide, height: faceSide)
        .clipShape(Circle())
        // Ringed in the page's colour, so faces over each other stay apart.
        .overlay(Circle().strokeBorder(Color(.systemBackground), lineWidth: 2))
    }
}

/// The first photo of the row at the top of the grid, as it scrolls: where
/// the grid goes back to when its width changes. A plain object, so that
/// scrolling changes no state and draws nothing again.
final class GridScrollMemo {
    private(set) var topItem: Int?
    /// While the grid is being brought back: the rows seen meanwhile are
    /// the new width's at the old offset, not where the user was.
    var restoring = false

    func visible(_ ids: [String]) {
        guard !restoring else { return }
        topItem = ids.compactMap(PhotoGridLayout.firstItem(ofRowID:)).min()
    }
}

private struct PhotoTile: View {
    let item: PhotoGalleryVM.Item
    /// The tile's width and height (PhotoGridMetrics.tile).
    let side: CGFloat
    let isSelected: Bool
    // Whether *any* tile in the grid is currently selected — while true,
    // tapping a tile toggles its selection instead of opening the preview,
    // matching the Photos app's selection-mode behavior.
    let hasSelection: Bool
    let onTap: () -> Void
    let onLongPress: () -> Void

    /// The size thumbnails are decoded for (PhotoGridMetrics.decodeSide).
    static let decodeSide = PhotoGridMetrics.decodeSide
    /// Nearly square corners, as the web's tiles (2px): with 2 pt between
    /// tiles, rounder ones read as separate cards rather than one wall.
    static let corner: CGFloat = 2

    var body: some View {
        ZStack(alignment: .bottomTrailing) {
            thumb
                .background(Color.secondary.opacity(0.1))
                .clipShape(RoundedRectangle(cornerRadius: Self.corner))
                .overlay(selectionOverlay)
            if item.isLocalOnly {
                Label("", systemImage: "iphone")
                    .padding(4)
                    .background(.ultraThinMaterial)
                    .clipShape(Circle())
                    .padding(6)
            }
        }
        .frame(width: side, height: side)
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
        // VoiceOver: one element per tile, its own square - not the
        // photo's uncropped frame reaching over the month's title - read
        // as the web's tiles are ("Photo, 5 October 2026").
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(PhotoMonths.tileLabel(video: item.mime.hasPrefix("video/"), date: item.created))
        .accessibilityAddTraits(isSelected ? [.isButton, .isSelected] : .isButton)
        .accessibilityAction {
            if hasSelection { onLongPress() } else { onTap() }
        }
        .accessibilityAction(named: isSelected ? "Deselect" : "Select") { onLongPress() }
    }

    private var thumb: some View {
        Group {
            // Decoded once at tile size and cached, not on every pass of
            // this body. A local file is read from disk, as before.
            if let img = GridThumbCache.image(id: item.id, data: item.thumbData,
                                              localURL: item.localURL, maxPt: Self.decodeSide) {
                Image(uiImage: img).resizable().scaledToFill()
            } else {
                Color.gray.opacity(0.2)
            }
        }
        // Cropped to the tile itself: scaledToFill's own frame is the
        // whole photo (127 x 283 pt for a portrait one in a 127 pt tile),
        // and the video mark below would sit at its corner, out of sight.
        .frame(width: side, height: side)
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
                RoundedRectangle(cornerRadius: Self.corner)
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

/// The Photos app's own single-image viewer: full screen, paging between
/// photos by sliding the strip of previous/current/next (three views, so
/// a library of thousands costs nothing), share/save-to-Photos/delete
/// along the bottom (issue #9), info top-left (issue #41), pinch to zoom
/// (issue #36). Mirrors the Android viewer's pager. The Files section
/// opens its photos and videos in it too (PhotoGalleryVM.showFiles).
struct ImageModal: View {
    @ObservedObject var vm: PhotoGalleryVM
    let save: () -> Void
    let share: () -> Void
    let delete: () -> Void

    @State private var confirmDelete = false
    // The strip's horizontal offset while a finger drags it or it animates
    // to the neighbour; zero when at rest on the open photo.
    @State private var dragOffset: CGFloat = 0
    @State private var sliding = false

    // Issue #36: zooms around wherever the fingers are (MagnifyGesture's
    // startAnchor) and snaps back on release - @GestureState resets itself.
    @GestureState private var pinchScale: CGFloat = 1.0
    @GestureState private var pinchAnchor: UnitPoint = .center

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            VStack(spacing: 0) {
                HStack {
                    Button { vm.openInfo() } label: {
                        Image(systemName: "info.circle").font(.title2).foregroundStyle(.white)
                    }
                    Spacer()
                    Button { vm.closeModal() } label: {
                        Image(systemName: "xmark.circle.fill").font(.title).foregroundStyle(.white)
                    }
                }
                .padding()

                GeometryReader { geo in
                    let w = geo.size.width
                    let idx = vm.openIndex ?? 0
                    HStack(spacing: 0) {
                        page(idx - 1, size: geo.size)
                        page(idx, size: geo.size)
                        page(idx + 1, size: geo.size)
                    }
                    .frame(width: w * 3, height: geo.size.height)
                    .offset(x: -w + dragOffset)
                    .gesture(
                        DragGesture(minimumDistance: 20)
                            .onChanged { v in
                                guard !sliding else { return }
                                var dx = v.translation.width
                                // Rubber-band at either end rather than
                                // sliding into nothing.
                                if (dx > 0 && idx == 0) || (dx < 0 && idx >= vm.items.count - 1) { dx /= 3 }
                                dragOffset = dx
                            }
                            .onEnded { v in
                                guard !sliding else { return }
                                let dx = v.translation.width
                                let flick = v.predictedEndTranslation.width
                                if (dx < -w / 4 || flick < -w / 2), idx < vm.items.count - 1 {
                                    slide(to: -w) { vm.next() }
                                } else if (dx > w / 4 || flick > w / 2), idx > 0 {
                                    slide(to: w) { vm.prev() }
                                } else {
                                    withAnimation(.easeOut(duration: 0.2)) { dragOffset = 0 }
                                }
                            }
                    )
                }
                .clipped()

                HStack(spacing: 48) {
                    Button { share() } label: { Image(systemName: "square.and.arrow.up") }
                    Button { save() } label: { Image(systemName: "arrow.down.circle") }
                    Button(role: .destructive) { confirmDelete = true } label: { Image(systemName: "trash") }
                }
                .font(.title2)
                .foregroundStyle(.white)
                .padding(.vertical, 12)
            }
        }
        .confirmationDialog("Delete this photo?", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("Delete Photo", role: .destructive, action: delete)
            Button("Cancel", role: .cancel) {}
        }
        // Nested here rather than on PhotoGalleryView: a second sheet on the
        // presenting view can't show while this cover is up.
        .sheet(isPresented: Binding(
            get: { vm.shareURL != nil },
            set: { if !$0 { vm.shareURL = nil } }
        )) {
            if let url = vm.shareURL { ActivityView(items: [url]) }
        }
        .sheet(isPresented: $vm.infoOpen) {
            FileInfoView(loading: vm.infoLoading, info: vm.infoData)
        }
    }

    /// Animates the strip one page over, then commits the new index and
    /// re-centres the strip without animating, so the photo that just slid
    /// in stays exactly where it landed.
    private func slide(to x: CGFloat, then commit: @escaping () -> Void) {
        sliding = true
        withAnimation(.easeOut(duration: 0.25)) { dragOffset = x }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.26) {
            var t = Transaction()
            t.disablesAnimations = true
            withTransaction(t) {
                commit()
                dragOffset = 0
            }
            sliding = false
        }
    }

    @ViewBuilder
    private func page(_ i: Int, size: CGSize) -> some View {
        ZStack {
            if vm.items.indices.contains(i) {
                let item = vm.items[i]
                if i == vm.openIndex, let player = vm.videoPlayer {
                    // Issue #106: plays in the viewer, filling it the same
                    // way a photo does, starting on its own.
                    VideoPlayer(player: player)
                        .onAppear {
                            // The app starts in .ambient (for the muted
                            // feed), which the ringer switch silences - a
                            // video opened here is meant to be heard.
                            Task.detached(priority: .userInitiated) {
                                let session = AVAudioSession.sharedInstance()
                                try? session.setCategory(.playback)
                                try? session.setActive(true)
                            }
                            player.play()
                        }
                        .onDisappear { player.pause() }
                } else if let img = vm.hiResImages[item.path] ?? thumb(item) {
                    Image(uiImage: img)
                        .resizable()
                        .scaledToFit()
                        // On the photo's own frame, so it sits in the
                        // picture's corner rather than the black margin,
                        // and before the pinch so zooming doesn't grow it.
                        .overlay(alignment: .bottomTrailing) {
                            if showsLowRes(item) {
                                LowResBadge(loading: !vm.hiResFailed.contains(item.path))
                                    .padding(12)
                            }
                        }
                        .scaleEffect(i == vm.openIndex ? pinchScale : 1, anchor: pinchAnchor)
                        .animation(.spring(response: 0.3, dampingFraction: 0.7), value: pinchScale)
                        // Simultaneous so pinching isn't swallowed by the
                        // strip's drag.
                        .simultaneousGesture(
                            MagnifyGesture()
                                .updating($pinchScale) { value, state, _ in state = value.magnification }
                                .updating($pinchAnchor) { value, state, _ in state = value.startAnchor }
                        )
                } else {
                    ProgressView().tint(.white)
                }
            }
        }
        .frame(width: size.width, height: size.height)
    }

    /// Still drawn from its thumbnail: the full-size image hasn't arrived
    /// (or failed to). A local file's "thumbnail" is the file itself, and
    /// a video is never decoded into a full-size image at all.
    private func showsLowRes(_ item: PhotoGalleryVM.Item) -> Bool {
        !item.mime.hasPrefix("video/") && item.localURL == nil && vm.hiResImages[item.path] == nil
    }

    /// The grid's own thumbnail, shown until the full-size image arrives.
    private func thumb(_ item: PhotoGalleryVM.Item) -> UIImage? {
        if let u = item.localURL, let img = UIImage(contentsOfFile: u.path) { return img }
        if let d = item.thumbData { return UIImage(data: d) }
        return item.thumbImage
    }
}

/// Marks a photo the viewer is still showing from its thumbnail. The
/// spinner means the full-size image is on its way; without it, it isn't
/// coming.
private struct LowResBadge: View {
    let loading: Bool

    var body: some View {
        HStack(spacing: 4) {
            if loading {
                ProgressView().tint(.white).controlSize(.mini)
            }
            Text("Low res").font(.system(size: 11, weight: .semibold))
        }
        .foregroundStyle(.white)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(.black.opacity(0.55), in: Capsule())
    }
}

/// Issue #41: camera/EXIF metadata panel, with a native map for GPS.
private struct FileInfoView: View {
    let loading: Bool
    let info: Msg_FileExifInfo?
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
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
                        // One line ("Jan 2024"), reaching out of the
                        // column to the left rather than wrapping in it.
                        .lineLimit(1)
                        .fixedSize()
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
                        // No network call here at all - the placeholder
                        // count comes straight out of the already-fetched
                        // bucket counts, which is the whole point:
                        // dragging fast across years costs nothing but
                        // re-renders.
                        vm.previewScrub(Double(value.location.y / geo.size.height))
                    }
                    .onEnded { _ in
                        // Jumps to the month, which lands under its title
                        // where the grey tiles were.
                        vm.endScrub()
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
