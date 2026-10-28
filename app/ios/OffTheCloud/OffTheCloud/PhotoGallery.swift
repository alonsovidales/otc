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
        // The tile's thumbnail bytes, only where nothing else keeps them:
        // a local file's, or a page's content the thumbnail cache refused
        // (GridThumbLoader.takePage). A device's tiles are otherwise read
        // from the cache by `hash`, or asked for.
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
        // The content hash its page gave it: its thumbnail's key in the
        // cache on this phone (ThumbDiskCache). "" for a local file.
        var hash: String = ""
        /// Issue #201: at how many paths the device holds it (File.copies,
        /// its kept-out folders included); nil where the device didn't say
        /// (before #201, a page whose count failed, Files' viewer).
        var copies: Int? = nil
        /// Issue #194: brought by the search upward from a jump
        /// (newer_than), not the downward one: each search's `have`
        /// counts only the photos it brought, so a delete lowers the
        /// right one.
        var up = false
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
        didSet {
            itemsVersion &+= 1
            // Issue #200: a photo picked before a drag that has gone from
            // the grid mid-drag (deleted) leaves the drag too, so the next
            // move doesn't pick it again (DragSelection.forget).
            if let drag = dragSelection, !drag.base.isEmpty {
                let here = Set(items.map(\.path))
                dragSelection?.forget { !here.contains($0) }
            }
        }
    }
    /// Bumped by every change to `items`: the grid's layout is worked out
    /// again only then (gridLayout).
    private var itemsVersion = 0
    private struct LayoutKey: Equatable {
        var items: Int
        var metrics: PhotoGridMetrics
        var dated: Bool
        var lastRoom: Int?
        var firstRoom: Int?
        var breaks: Int
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
        } else if gridBody == .skeleton {
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
        // Issue #194: the first month's, while its newer photos are on
        // their way from above a jump - grey tiles before the photos it
        // has, filled in place as they come. Not the jump's own month,
        // which starts complete (the cutoff is its end).
        var firstRoom: Int?
        if dated, placeholder == .none, !upEnd, let first = items.first, first.up, let month = first.month {
            firstRoom = freshBuckets.first { $0.month == month }?.count
        }
        let key = LayoutKey(items: itemsVersion, metrics: metrics, dated: dated, lastRoom: lastRoom,
                            firstRoom: firstRoom, breaks: upBreaks.count,
                            placeholder: placeholder, placeholderCount: count)
        if let c = layoutCache, c.key == key { return c.layout }
        let layout = placeholder == .none
            ? PhotoGridLayout.build(months: items.map(\.month), ids: items.map(\.id), dated: dated,
                                    lastRoom: lastRoom, firstRoom: firstRoom, breaks: upBreaks, metrics: metrics)
            : PhotoGridLayout.placeholder(placeholder, count: count, metrics: metrics)
        layoutCache = (key, layout)
        return layout
    }

    @Published var loading = false
    /// The last page asked for failed, and why (nil: none did). It is asked
    /// again meanwhile (pageRetry: 1 s doubling to 10 s, at once on a wake
    /// or Try again); the grid says so until a page lands - in its place
    /// for the first page, at its end for a later one (gridBody). Never a
    /// blank page.
    @Published private(set) var pageProblem: LoadProblem?
    /// The first page has been on its way a while (slowFirstPageAfter): a
    /// pill over the grey tiles says it is still coming.
    @Published private(set) var slowFirstPage = false
    private var slowTimer: Task<Void, Never>?
    /// How long the first page may take before the grey tiles say it is
    /// still coming (People's and Collections' 10 s): a first page from Pit
    /// over the bridge measured up to 70 s. Tests shorten it.
    var slowFirstPageAfter: TimeInterval = 10
    /// A failed page's wait before it is asked again (Android's retryJob),
    /// and the background retries of the lists the search and the headers
    /// use (Android's keepAsking): the month counts, the tags, the people
    /// and the collections - each asked again until it comes.
    let pageRetry = PageRetry()
    let bucketRetry = PageRetry()
    let tagsRetry = PageRetry()
    let peopleRetry = PageRetry()
    let groupsRetry = PageRetry()
    /// The pages asked for by scrolling or a retry: the model's own task,
    /// so a tile scrolled away (whose task asked) never cancels a page on
    /// its way - one dropped after the device had answered moved its token
    /// on past photos that never arrived.
    private var pageTask: Task<Void, Never>?
    /// A jump's cutoff, for the search it started: a first page asked again
    /// starts at that month again, and a page that went on by its token is
    /// checked against it (PhotoPaging.lostCutoff). nil for any other
    /// search.
    private(set) var jumpBefore: Date?
    private var wakeWatch: AnyCancellable?
    private var clearWatch: AnyCancellable?

    /// A failed page is being asked for again (Try again's "Trying…").
    var retrying: Bool { pageProblem != nil && loading }

    /// What Images shows in the grid's place (Android's galleryBody, the
    /// web's PhotoGallery body).
    enum GridBody: Equatable {
        /// The scrubber's month as grey tiles (dragged, or its jump on its way).
        case grey
        /// Grey tiles while the first page is on its way - from the moment
        /// a search starts, so never a blank page.
        case skeleton
        /// The first page failed and is being asked for again: "Couldn't
        /// load your photos", why, and Try again. It stays up through the
        /// retries rather than flipping back to grey tiles.
        case problem
        /// Nothing to show (emptyKind says which).
        case empty
        /// The grid; a later page's failure at its end.
        case photos
    }

    var gridBody: GridBody {
        if fixedList { return .photos }
        if placeholderCount != nil { return .grey }
        if !items.isEmpty { return .photos }
        if pageProblem != nil { return .problem }
        if loading || !endReached { return .skeleton }
        return .empty
    }

    /// Why there is nothing to show (the web's and Android's three empty
    /// states).
    enum EmptyKind: Equatable { case collection, noMatch, noPhotos }

    var emptyKind: EmptyKind {
        if !chips.isEmpty || !selectedPeople.isEmpty { return .noMatch }
        if activeGroup != nil { return .collection }
        return .noPhotos
    }

    /// Why a load failed, as the grid says it. Tests decide in the phone's
    /// place.
    var problemOf: @MainActor (Msg_RespEnvelope?, Error?) -> LoadProblem = { LoadProblem.now(resp: $0, error: $1) }

    /// Bumped by every search that replaces the grid (a filter change):
    /// the grid goes back to its top, as the web scrolls the window up -
    /// its grey tiles would otherwise show wherever the last search was
    /// scrolled to, under no title.
    @Published private(set) var searchStarts = 0
    @Published var endReached = false
    private var token: String? = nil

    // Issue #194: the second cursor, for the photos newer than a jump's
    // cutoff (SearchPhotos.newer_than), asked for as the grid's top comes
    // near. A search that isn't a jump has none: nothing is newer than its
    // first photo (upEnd from the start). Never mixed with the downward
    // one: upToken goes only with newer_than, the downward token never.
    private var upToken = ""
    /// Issue #194: what each search brought that the grid shows already
    /// from the other one (by content: PhotoPaging.hidden) - still part of
    /// its `have`, as the device counts them.
    private var upHidden = 0
    private var downHidden = 0
    /// Nothing more above: not a jump, the upward search's last page came,
    /// or the device can't (before #194).
    @Published private(set) var upEnd = true
    /// An upward page is on its way.
    @Published private(set) var upLoading = false
    /// The last upward page failed, and why: asked again meanwhile
    /// (upRetry), or now with Try again - shown at the grid's top as a
    /// later page's failure is at its end.
    @Published private(set) var upProblem: LoadProblem?
    let upRetry = PageRetry()
    private var upTask: Task<Void, Never>?
    /// The nearest tile to the top (its index) that asked while an upward
    /// page was on its way (morePendingAt's twin).
    private var upPendingAt: Int?
    /// The grid was scrolled up since the jump (pulled down at its top, or
    /// scrolled back up to it): only from then on are the newer photos
    /// asked for - never at the jump itself, which lands at the top (the
    /// web's first upward gesture; Android's first grey tile drawn).
    private(set) var upArmed = false
    /// The months a line of short months never continues into: the top of
    /// the grid each time a page was put above it, so the lines on the
    /// screen stay as they are (PhotoGridLayout.build).
    private var upBreaks: Set<String> = []
    /// What the signed-in device is known not to do (tests use their own).
    var support = DeviceSupport.shared
    /// Issue #194: told right before photos are put above the grid (or the
    /// top month's room changes), so the view notes which row is on the
    /// screen; aboveChanges is bumped right after, for it to keep that row
    /// where it was.
    var willChangeAbove: (() -> Void)?
    @Published private(set) var aboveChanges = 0
    /// Right after photos were put above, until the view has put the row
    /// it showed back in place, the tiles drawn for a moment at the grid's
    /// top (its very first frame) don't ask for more.
    private var upQuietUntil = Date.distantPast
    // Bumped every time a fresh search starts (resetAndLoadFirstPage) -
    // fetchPage captures the value at call time and checks it's unchanged
    // before applying its response. Toggling a person filter (or a tag)
    // twice in quick succession spawns two overlapping Tasks; without
    // this, whichever *response* happens to land last wins even if it was
    // for the *older* selection - reproduced live on the web app as: tap
    // a person on then off quickly, the avatar shows selected/deselected
    // correctly but the grid shows the other request's (wrong) results,
    // because that one's reply simply arrived second.
    private(set) var searchGeneration = 0
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
    // eight 24 MP photos decoded at full size were ~800 MB. All of them go
    // when the viewer closes; the bytes stay on the phone for the session
    // (FullPhotoCache), so a photo opened again is decoded from there
    // rather than fetched.
    @Published var hiResImages: [String: UIImage] = [:]
    private var hiResSource: [String: HiResSource] = [:]
    private var hiResOrder: [String] = []
    /// The full-size loads on their way, by path: a second ask for one
    /// waits for it.
    private var inFlightHiRes: [String: Task<Void, Never>] = [:]
    /// The bytes behind them, kept for the session (tests use their own).
    var fullPhotos: FullPhotoCache = .shared
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
    /// The open video's player failing (watchPlayback).
    private var playerWatch: [Any] = []

    // Grids show small thumbnails (release 111, small_thumbnails): the
    // viewer shows an item's small tile only until its full size arrives.
    // Where the thumbnail stays on screen - the full size failed or can't
    // be decoded, a video couldn't be fetched or played - it asks for the
    // big one (GetThumbnails without the flag), again with the same backoff
    // for as long as the viewer stays on that item (thumbnailStays), as
    // Android and the web's MediaViewer.
    /// The big thumbnails, by path, of items whose thumbnail stays on
    /// screen in the viewer: shown instead of the grid's small tile - the
    /// open one's and the last few swiped past. Never in GridThumbCache
    /// nor the thumbnail cache on the phone (ThumbDiskCache), which hold
    /// the tiles: the two never share an entry.
    @Published private(set) var bigThumbs: [String: UIImage] = [:]
    private var bigThumbOrder: [String] = []
    private static let bigThumbsKept = 4
    /// The big thumbnail asked for (bigThumbTask), and whose.
    private var bigThumbTask: Task<Void, Never>?
    private var bigThumbFor: String?
    /// The waits between asks for a big thumbnail, by failures in a row:
    /// a grid page's (tests shorten them).
    var bigThumbDelay: (Int) -> TimeInterval = { PageRetry.delay(afterFailures: $0) }
    /// Videos the player couldn't play, or that couldn't be fetched: their
    /// poster stays, saying so, with Try again.
    @Published private(set) var unplayable = Set<String>()
    @Published var showAlert = false
    /// What the alert says, kept as what happened and worded when it shows
    /// (GalleryNotice.text), so it follows a change of language.
    @Published var notice: GalleryNotice?
    /// A device too old for what was just tapped (DeviceNeedsUpdate).
    @Published var needsUpdate: DeviceNeedsUpdate?

    /// Whether the alert (`notice`, `needsUpdate`) shows `overViewer`
    /// (ImageModal) or over the screen under it (Images, Files): over the
    /// viewer while it is open. Presented from under the full-screen
    /// viewer, an alert takes the viewer away - Save's "Photos access
    /// denied" used to close it.
    func alertShown(overViewer: Bool) -> Bool {
        showAlert && (openIndex != nil) == overViewer
    }

    /// The same for `needsUpdate`.
    func needsUpdateShown(overViewer: Bool) -> DeviceNeedsUpdate? {
        (openIndex != nil) == overViewer ? needsUpdate : nil
    }

    /// The alert's binding for the view that shows it over or under the
    /// viewer.
    func alertBinding(overViewer: Bool) -> Binding<Bool> {
        Binding(get: { [weak self] in self?.alertShown(overViewer: overViewer) ?? false },
                set: { [weak self] in self?.showAlert = $0 })
    }

    func needsUpdateBinding(overViewer: Bool) -> Binding<DeviceNeedsUpdate?> {
        Binding(get: { [weak self] in self?.needsUpdateShown(overViewer: overViewer) },
                set: { [weak self] in self?.needsUpdate = $0 })
    }
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

    /// The grid's tiles: from the thumbnail cache on this phone, asked for
    /// (GetThumbnails) where it has none (GridThumbLoader).
    let thumbs: GridThumbLoader
    /// Bumped when tiles got their images: the grid draws them.
    @Published private(set) var thumbsVersion = 0

    // Selection (via long-press)
    @Published var selected: Set<String> = []
    // Issue #45: confirm before bulk-deleting the current multi-selection.
    @Published var confirmDeleteSelected = false

    // Issue #41: "More info" — camera/EXIF metadata computed live on the
    // server from the file's own bytes.
    @Published var infoOpen = false
    @Published var infoLoading = false
    @Published var infoData: Msg_FileExifInfo? = nil

    /// `thumbCache`: where the tiles' thumbnails are kept (tests use their own).
    init(deviceID: String, localPhotosFolder: URL?, thumbCache: ThumbDiskCache = .shared) {
        self.deviceID = deviceID
        self.localFolder = localPhotosFolder
        thumbs = GridThumbLoader(maxPt: PhotoGridMetrics.decodeSide, cache: thumbCache) { _ in throw CancellationError() }
        attachThumbs()
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
        // Signed in again, a network came up, back in the foreground
        // (OTCConnection.wakes): what failed is asked for again at once.
        wakeWatch = OTCConnection.shared.$wakes
            .dropFirst()
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.wake() }
        watchCacheCleared()
    }

    /// Settings cleared the thumbnail cache: the viewer's big thumbnails
    /// go too (none is kept while the viewer is closed, but swiped-past
    /// ones are while it is open).
    private func watchCacheCleared() {
        clearWatch = NotificationCenter.default.publisher(for: .otcThumbnailCacheCleared)
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                guard let self else { return }
                self.stopBigThumb()
                self.bigThumbs = [:]
                self.bigThumbOrder = []
            }
    }

    /// A wake: a page that failed is asked for again now, not at the end of
    /// its wait, and so are the lists still being asked for.
    func wake() {
        guard !fixedList else { return }
        if pageProblem != nil { retryPage() }
        if upProblem != nil { retryNewer() }
        bucketRetry.wake()
        tagsRetry.wake()
        peopleRetry.wake()
        groupsRetry.wake()
    }

    // The Files section opens its photos and videos in this same viewer:
    // an instance made with init() only ever shows the list handed to
    // showFiles - no search, no paging, no local merge.
    private var fixedList = false
    /// Called with the path after the viewer deleted a file - or found it
    /// holds other content than was shown (issue #201's "changed") - so the
    /// screen that opened it can re-read its listing.
    var onDeleted: ((String) -> Void)?

    init() {
        self.deviceID = ""
        self.localFolder = nil
        self.fixedList = true
        thumbs = GridThumbLoader(maxPt: PhotoGridMetrics.decodeSide) { _ in throw CancellationError() }
        watchCacheCleared()
    }

    /// The tiles' GetThumbnails go as Images' other requests (photoRequest,
    /// which tests replace), as a kind of their own.
    private func attachThumbs() {
        thumbs.request = { [weak self] payload in
            guard let self else { throw CancellationError() }
            return try await self.photoRequest(payload, .tiles)
        }
        thumbs.onImages = { [weak self] in self?.thumbsVersion &+= 1 }
    }

    /// What the loader needs for a tile from the device; nil for one that
    /// has its own bytes (a local file, content the cache refused).
    func thumbWant(_ it: Item) -> GridThumbLoader.Want? {
        guard it.localURL == nil, it.thumbData == nil, !it.hash.isEmpty else { return nil }
        return GridThumbLoader.Want(id: it.id, path: it.path, hash: it.hash)
    }

    /// The tile at `i` came on screen: its thumbnail is asked for first,
    /// and the tiles around it are read from the cache ahead of the scroll.
    func tileShown(_ i: Int) {
        guard !fixedList, items.indices.contains(i) else { return }
        thumbs.appeared(items[i].path)
        guard i % 6 == 0 else { return }
        thumbs.readAhead(items[max(0, i - 12)..<min(items.count, i + 30)].compactMap(thumbWant))
    }

    func tileHidden(_ it: Item) {
        thumbs.disappeared(it.path)
    }

    /// A tile on screen has no image: from the cache, else the device.
    func needThumb(_ it: Item) {
        guard !fixedList, let w = thumbWant(it) else { return }
        thumbs.need(w)
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
        // The first page goes out at once: the tags and people come
        // alongside, not ahead of it (a slow or lost answer to either used
        // to hold the photos back, on a blank page).
        restartSearch()
        libraryFetchedAt = Date()
        loadLibrary()
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
            loadLibrary()
        }
        if groupsLoaded || activeGroup != nil { Task { await loadGroups() } }
        if photosAsked { restartSearch() }
    }

    /// One request to the device. `kind`: what a screen waits on (a page,
    /// the tags...), asked through OTCConnection.ask - its usual time for
    /// the answer, more after a timeout, back down once answers come in
    /// time again, per kind and route (Patience); nil: anything else (a
    /// rename, a delete), with the socket's own time. Tests answer in the
    /// device's place. Android's GalleryRequest.
    typealias Request = @MainActor (Msg_ReqEnvelope.OneOf_Payload, AskKind?) async throws -> Msg_RespEnvelope

    /// Through OTCConnection: ask() for a kind, request() for the rest.
    static let deviceRequest: Request = { payload, kind in
        if let kind {
            return try await OTCConnection.shared.ask(kind.key, base: kind.base) { $0.payload = payload }
        }
        return try await OTCConnection.shared.request { $0.payload = payload }
    }

    /// Sends Images' own requests (its pages, the date buckets, deletes,
    /// the viewer's big thumbnails). Tests answer them in the device's
    /// place.
    var photoRequest: Request = PhotoGalleryVM.deviceRequest

    /// Sends the library's own requests (the tags, the collections). Tests
    /// answer them in the device's place.
    var libraryRequest: Request = PhotoGalleryVM.deviceRequest

    /// What Images asks for, by how long its answer may take: a page of
    /// photos and the lists that come with covers (people, collections)
    /// OTCConnection.pageTimeout, the small lists (tags, month counts) and
    /// the viewer's big thumbnail listTimeout - as Android's kinds. The
    /// tiles' thumbnails (GridThumbLoader, up to 24 a request: about a
    /// page's bytes) pageTimeout.
    enum AskKind: Hashable {
        case page, tags, buckets, people, groups, thumbnail, tiles

        var base: TimeInterval {
            switch self {
            case .page, .people, .groups, .tiles: return OTCConnection.pageTimeout
            case .tags, .buckets, .thumbnail: return OTCConnection.listTimeout
            }
        }

        /// Its name in Patience's keys (Android's).
        var key: String {
            switch self {
            case .page: return "page"
            case .tags: return "tags"
            case .buckets: return "buckets"
            case .people: return "people"
            case .groups: return "groups"
            case .thumbnail: return "thumbnail"
            case .tiles: return "tiles"
            }
        }
    }

    private func ask(_ kind: AskKind, _ payload: Msg_ReqEnvelope.OneOf_Payload, via send: Request) async throws -> Msg_RespEnvelope {
        try await send(payload, kind)
    }

    /// Whether the phone has a network now (NetworkWatch). Tests decide in
    /// the phone's place.
    var online: @MainActor () -> Bool = { !NetworkWatch.shared.offline }

    /// The phone is online again: "This phone is offline" no longer says
    /// why while the page is asked for again - the neutral line instead
    /// (it stayed up for as long as the page then took). Android's
    /// backOnline.
    private func backOnline() {
        guard pageProblem == .offline else { return }
        pageProblem = pageProblem?.backOnline(online())
    }

    /// The tags and people for the search, once, without the photos: the
    /// wide layout's top bar searches from any section, before Images
    /// has ever been shown.
    func loadLibraryOnce() {
        guard !fixedList, !libraryAsked else { return }
        libraryAsked = true
        libraryFetchedAt = Date()
        loadLibrary()
    }

    /// The search ended: the tags and people are fetched again if the last
    /// time was a while ago (the web's refreshIfStale).
    func refreshLibraryIfStale() {
        guard !fixedList, Date().timeIntervalSince(libraryFetchedAt) > 5 * 60 else { return }
        libraryFetchedAt = Date()
        loadLibrary()
    }

    /// The tags, and the people while face recognition is on, for the
    /// search: each asked again in the background until it comes.
    private func loadLibrary() {
        Task { await loadTags() }
        if FaceRecognition.shared.isOn { Task { await loadPeople() } }
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
    /// The tags, asked again: true once the device's list is in. One that
    /// failed keeps being asked in the background (tagsRetry).
    @discardableResult
    private func loadTags() async -> Bool {
        if await loadTagsOnce() {
            // In: a loop still asking for an older failure ends.
            tagsRetry.reset()
            return true
        }
        tagsRetry.keepAsking { [weak self] in await self?.loadTagsOnce() ?? true }
        return false
    }

    private func loadTagsOnce() async -> Bool {
        guard let resp = try? await ask(.tags, .reqGetTags(.init()), via: libraryRequest) else { return false }
        if case .respTagsList(let tl) = resp.payload {
            tags = tl.tags
            return true
        }
        // A device without them: nothing to ask again.
        return resp.errorCode == "unknown_payload"
    }

    // MARK: Date scrubber (issue #77)
    /// The month counts of the filter as it is now. One that fails is
    /// asked again in the background (bucketRetry: 1 s doubling to 10 s,
    /// the web's usePageRetry) for as long as it is still this filter's -
    /// the headers' count and span, the scrubber and the last month's room
    /// all wait on it. A filter changed meanwhile has asked for its own.
    private func loadDateBuckets() async {
        // A newer load (another filter, or the same after a delete) wins,
        // whichever answer lands last.
        bucketGeneration += 1
        let gen = bucketGeneration
        bucketRetry.reset()
        guard let key = bucketKeyNow else { return }
        let people = selectedPeople // snapshot - see fetchPage's own doc comment on why
        let group = activeGroup?.id ?? ""
        if await loadDateBucketsOnce(people: people, group: group, key: key, gen: gen) { return }
        bucketRetry.keepAsking(wanted: { [weak self] in
            guard let self else { return false }
            return gen == self.bucketGeneration && self.bucketKeyNow == key
        }) { [weak self] in
            await self?.loadDateBucketsOnce(people: people, group: group, key: key, gen: gen) ?? true
        }
    }

    /// One ask for the month counts: false when it failed and is worth
    /// asking again.
    private func loadDateBucketsOnce(people: [String], group: String, key: String, gen: Int) async -> Bool {
        var b = Msg_ReqPhotoDateBuckets()
        b.personIds = people
        b.groupID = group // issue #115
        b.includeVideos = true // issue #106: same set the grid shows
        guard let resp = try? await ask(.buckets, .reqPhotoDateBuckets(b), via: photoRequest) else { return false }
        // A newer filter asked for its own meanwhile: nothing left to do here.
        guard gen == bucketGeneration else { return true }
        if case .respPhotoDateBuckets(let r) = resp.payload {
            setDateBuckets(r.buckets.map { ($0.month, Int($0.count)) }, key: key)
            return true
        }
        // A device without them: no scrubber, no counts in the headers, and
        // nothing to ask again.
        if resp.errorCode == "unknown_payload" {
            setDateBuckets([], key: key)
            return true
        }
        return false
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
        // The bucket's month is Gregorian, whatever calendar the phone is
        // set to: so is its last instant (PhotoMonths.calendar). Not a
        // month at all: no jump, the photos as they were - never grey
        // tiles left standing (as Android).
        guard let before = PhotoMonths.jumpCutoff(of: month) else {
            if scrubFrac == nil {
                placeholderCount = nil
                placeholderMonth = nil
            }
            return
        }
        searchTask?.cancel()
        searchTask = Task { await performJump(before) }
    }

    /// A fresh search like a filter change, anchored at `before` (the last
    /// instant of the month, PhotoMonths.jumpCutoff). The grey tiles go
    /// once this search is done, unless a newer one took over or a new
    /// drag has its own. The selection stays, as on the web and Android:
    /// picking photos from two dates is what the scrubber is for.
    ///
    /// A first page that fails takes the grey tiles away for the grid's
    /// "Couldn't load your photos" (as the web's jump does), and is asked
    /// again from the same month (jumpBefore).
    private func performJump(_ before: Date) async {
        searchGeneration += 1
        let myGeneration = searchGeneration
        keepPicks()
        startSearchState(jump: before)
        await fetchUntilProgress()
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

    /// What every search that replaces the grid resets: a filter change
    /// (jump nil) or a jump to `jump`. The old search's retry, pages and
    /// timer go with it.
    private func startSearchState(jump: Date?) {
        stopPageRetry()
        // The old grid's thumbnails on their way land nowhere.
        thumbs.reset()
        loading = false
        // An ask from the previous grid's tiles: its index means nothing
        // against the new items, and would pull a page nobody scrolled to.
        morePendingAt = nil
        endReached = false
        token = ""
        // Issue #194: both cursors start again - the upward one only for a
        // jump, on a device that can.
        upTask?.cancel()
        upTask = nil
        upRetry.reset()
        upToken = ""
        upHidden = 0
        downHidden = 0
        upEnd = jump == nil || !support.newerThan
        upLoading = false
        upProblem = nil
        upPendingAt = nil
        upArmed = false
        upBreaks = []
        items = []
        jumpBefore = jump
        pageProblem = nil
        slowFirstPage = false
        watchSlowFirstPage(searchGeneration)
    }

    // MARK: People (issue #52 follow-up)

    /// Asks the device for its people: for the search, and every time the
    /// People page opens (new faces are found as photos arrive). True once
    /// they are here.
    /// One that failed keeps being asked in the background while face
    /// recognition is on (peopleRetry): the People page, which shows its
    /// own "Couldn't load people" until then, and the search fill in when
    /// it comes.
    @discardableResult
    func loadPeople() async -> Bool {
        if await loadPeopleOnce() {
            peopleRetry.reset()
            return true
        }
        peopleRetry.keepAsking(wanted: { FaceRecognition.shared.isOn }) { [weak self] in
            await self?.loadPeopleOnce() ?? true
        }
        return false
    }

    private func loadPeopleOnce() async -> Bool {
        // A page of faces: each person comes with their cover.
        guard let resp = try? await ask(.people, .reqListPeople(.init()), via: peopleRequest) else { return false }
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
    var peopleRequest: Request = PhotoGalleryVM.deviceRequest

    /// How a request the device answers with an Ack went.
    enum AckResult { case ok, refused, unanswered }

    private func ack(_ payload: Msg_ReqEnvelope.OneOf_Payload) async -> AckResult {
        guard let resp = try? await peopleRequest(payload, nil) else { return .unanswered }
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
    /// The collections, asked again: true once the device's list is in
    /// (groupsLoaded). One that failed keeps being asked in the background
    /// (groupsRetry): the open collection's header, the Collections page
    /// and its sheet fill in when it comes.
    @discardableResult
    func loadGroups() async -> Bool {
        if await loadGroupsOnce() {
            groupsRetry.reset()
            return true
        }
        groupsRetry.keepAsking { [weak self] in await self?.loadGroupsOnce() ?? true }
        return false
    }

    private func loadGroupsOnce() async -> Bool {
        // Each collection comes with its cover.
        // Covers as the grids' tiles: small thumbnails (release 111; an
        // older device sends big ones).
        var lg = Msg_ListImageGroups()
        lg.smallThumbnails = true
        guard let resp = try? await ask(.groups, .reqListImageGroups(lg), via: libraryRequest) else { return false }
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
            notice = .renameFailed; showAlert = true
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
            notice = .deleteCollectionFailed; showAlert = true
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
        var c = Msg_CreateImageGroup()
        c.name = name
        c.paths = paths
        // Its cover, as the list's (loadGroupsOnce).
        c.smallThumbnails = true
        guard let resp = try? await libraryRequest(.reqCreateImageGroup(c), nil),
              case .respImageGroup(let r) = resp.payload else {
            notice = .createFailed; showAlert = true
            return
        }
        groups.insert(r.group, at: 0)
        selected.removeAll()
        notice = .created(name); showAlert = true
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
            notice = .addFailed; showAlert = true
            return
        }
        selected.removeAll()
        await loadGroups()
        // If that was the open group, its grid has new members to show.
        if activeGroup?.id == g.id { restartSearch() }
        notice = .added(g.name); showAlert = true
    }

    /// Remove from collection: the picked photos leave the open collection
    /// - the membership only; they stay on the device and in other
    /// collections - and so its grid, every tile of each (a member is a
    /// photo, issue #201). No question first: nothing is lost, Add to
    /// collection puts them back. A device from before it answers
    /// unknown_payload: it needs an update (DeviceNeedsUpdate).
    func removeSelectionFromGroup() async {
        guard let g = activeGroup, preparing == nil else { return }
        let paths = Array(selected)
        guard !paths.isEmpty else { return }
        let hashes = Set(pickedItems().values.map(\.hash).filter { !$0.isEmpty })
        preparing = .removeFromGroup
        defer { preparing = nil }
        var r = Msg_RemoveFromImageGroup()
        r.groupID = g.id
        r.paths = paths
        guard let resp = try? await libraryRequest(.reqRemoveFromImageGroup(r), nil) else {
            notice = .removeFailed; showAlert = true
            return
        }
        if resp.error, ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) {
            let send = libraryRequest
            needsUpdate = await DeviceNeedsUpdate.asking(title: S.appPhotosCollectionsRemoveNeedsUpdate) { try await send($0, nil) }
            return
        }
        guard case .respAck(let ack) = resp.payload, ack.ok else {
            notice = .removeFailed; showAlert = true
            return
        }
        let picked = Set(paths)
        items.removeAll { picked.contains($0.path) || hashes.contains($0.hash) }
        clearSelection()
        // The months' and the open collection's counts, as after a delete.
        countsChanged()
        notice = .removed(g.name); showAlert = true
    }

    // MARK: Not this person / Someone else… (RemoveFromPerson)
    //
    // Face recognition sometimes files a photo under the wrong person.
    // Among one person's photos (a face opened from People, or one person
    // in the search - tags may be there too) the selection bar and the
    // viewer's menu take photos off that person: "Not this person" asks
    // first, then the device forgets the match (face recognition won't put
    // them back), and "Someone else…" files them under another person. The
    // photos themselves stay. The same on the web and Android.

    /// The one person whose photos Images shows: exactly one in the filter
    /// (tags may be combined). nil with none or several, and in Files'
    /// viewer.
    var personInFilter: String? {
        !fixedList && selectedPeople.count == 1 ? selectedPeople[0] : nil
    }

    /// That person's name as saved; "" without one (or before the list of
    /// people has come).
    var personInFilterName: String {
        guard let pid = personInFilter else { return "" }
        return allPeople.first { $0.id == pid }.map { PeopleOrder.tidy($0.name) } ?? ""
    }

    /// Photos to take off the person in the filter, as they were when the
    /// action was tapped: the picked ones (the selection bar) or the open
    /// one (the viewer).
    struct PersonPhotos: Identifiable, Equatable {
        enum From: Equatable { case selection, viewer }
        let id = UUID()
        /// Whom they are taken off.
        let person: String
        /// Their name as saved; "" for a person without one (or one the
        /// list of people doesn't have yet).
        let name: String
        let paths: [String]
        /// Their contents, each once: what RemoveFromPerson names.
        let hashes: [String]
        let from: From

        /// How many photos: the questions and the notes count them.
        var count: Int { paths.count }

        /// Not this person's question: "Remove 3 photos from Ana?".
        var removeTitle: String {
            name.isEmpty ? S.appPhotosPersonRemoveTitleUnnamed(count: count) : S.appPhotosPersonRemoveTitle(name: name, count: count)
        }
    }

    /// Not this person's question, while it is asked.
    @Published var personRemoveAsk: PersonPhotos?
    /// Someone else…'s list of people, while it is open.
    @Published var personMoveAsk: PersonPhotos?
    /// Someone else…'s list goes as the person is picked: what came of it
    /// is said once the list has had this long to leave the screen, as an
    /// alert presented while a sheet goes away can be dropped unseen
    /// (DeviceNeedsUpdate's outcome waits too). Tests set 0.
    var sheetCloses: TimeInterval = 0.7

    /// Says `n`: over the viewer while it is open, else over Images
    /// (alertShown).
    private func say(_ n: GalleryNotice) {
        notice = n
        showAlert = true
    }

    /// What the bar's or the viewer's action would take off the person in
    /// the filter; nil when there is no such person or nothing the device
    /// has (a photo only on this phone has no content hash).
    func personPhotos(from: PersonPhotos.From) -> PersonPhotos? {
        guard let pid = personInFilter else { return nil }
        var its: [Item] = []
        switch from {
        case .selection:
            let picked = pickedItems()
            its = selected.sorted().compactMap { picked[$0] }
        case .viewer:
            if let i = openIndex, items.indices.contains(i) { its = [items[i]] }
        }
        its = its.filter { !$0.hash.isEmpty }
        guard !its.isEmpty else { return nil }
        var seen = Set<String>()
        let hashes = its.map(\.hash).filter { seen.insert($0).inserted }
        return PersonPhotos(person: pid, name: personInFilterName, paths: its.map(\.path), hashes: hashes, from: from)
    }

    /// Not this person: asks first (personRemoveAsk).
    func askNotThisPerson(from: PersonPhotos.From) {
        guard preparing == nil else { return }
        personRemoveAsk = personPhotos(from: from)
    }

    /// Someone else…: the list of the other people (personMoveAsk), asked
    /// for again meanwhile so it has the faces found since.
    func askMoveToPerson(from: PersonPhotos.From) {
        guard preparing == nil, let ask = personPhotos(from: from) else { return }
        personMoveAsk = ask
        Task { await loadPeople() }
    }

    /// Takes `ask`'s photos off its person - to `target` when given (Someone
    /// else…), to no one otherwise (Not this person). Once the device says
    /// so they leave the grid at once, every tile of each; the viewer goes
    /// on to the next photo or closes, as after a delete, and the people's
    /// counts and covers are asked for again. A device from before it
    /// answers unknown_payload: it needs an update (DeviceNeedsUpdate).
    func removeFromPerson(_ ask: PersonPhotos, to target: Msg_Person? = nil) async {
        guard preparing == nil, target?.id != ask.person else { return }
        personRemoveAsk = nil
        personMoveAsk = nil
        preparing = target == nil ? .notThisPerson : .moveToPerson
        defer { preparing = nil }
        let began = Date()
        let sheetGone = { [sheetCloses] in
            let left = target == nil ? 0 : sheetCloses - Date().timeIntervalSince(began)
            if left > 0 { try? await Task.sleep(nanoseconds: UInt64(left * 1_000_000_000)) }
        }
        var r = Msg_RemoveFromPerson()
        r.personID = ask.person
        r.hashes = ask.hashes
        r.toPersonID = target?.id ?? ""
        let failed: GalleryNotice = target == nil ? .personRemoveFailed : .personMoveFailed
        guard let resp = try? await peopleRequest(.reqRemoveFromPerson(r), nil) else {
            await sheetGone()
            say(failed)
            return
        }
        if resp.error, ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) {
            let send = peopleRequest
            let update = await DeviceNeedsUpdate.asking(title: S.appPhotosPersonNeedsUpdate) { try await send($0, nil) }
            await sheetGone()
            needsUpdate = update
            return
        }
        guard case .respAck(let ack) = resp.payload, ack.ok else {
            await sheetGone()
            say(failed)
            return
        }
        let paths = Set(ask.paths)
        let hashes = Set(ask.hashes)
        let gone = { (it: Item) in paths.contains(it.path) || (!it.hash.isEmpty && hashes.contains(it.hash)) }
        // The viewer's place among the photos that stay.
        let openAt = ask.from == .viewer ? openIndex.map { at in items.prefix(at).filter { !gone($0) }.count } : nil
        items.removeAll(where: gone)
        switch ask.from {
        case .selection:
            clearSelection()
        case .viewer:
            selected.subtract(paths)
            pickedAway = pickedAway.filter { !paths.contains($0.key) }
            if items.isEmpty {
                closeModal()
            } else if openIndex != nil, let openAt {
                open(index: min(openAt, items.count - 1))
            }
        }
        // The months' counts and the person's, as after a delete; the
        // people's counts and covers (People asks again when shown, and
        // the search's suggestions have them).
        countsChanged()
        Task { await loadPeople() }
        // Over the viewer while it goes on; over Images once it closed.
        await sheetGone()
        say(target.map { .personMoved(ask.count, PeopleOrder.tidy($0.name)) } ?? .personRemoved(ask.count, ask.name))
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
    //
    // A page that fails - the first or a later one - is asked again, 1 s
    // doubling to 10 s (the web's usePageRetry, pageRetry), at once on a
    // wake (signed in again, a network came up, back in the foreground) or
    // Try again, for as long as its search is the current one
    // (searchGeneration). The grid says so meanwhile (pageProblem), never a
    // blank page - as Android's PhotoGalleryViewModel.
    func resetAndLoadFirstPage() async {
        await resetAndLoadFirstPage(jump: nil)
    }

    /// Issue #201: what the grid shows is out of date (a delete answered
    /// "changed": a path holds other content now): its search again from
    /// the start, at the top - a jump's at its month again. The selection
    /// stays (as on the web and Android): the photos refused are still
    /// picked, by their paths, to try again once seen.
    func reloadSearch() {
        guard !fixedList else { return }
        searchTask?.cancel()
        let jump = jumpBefore
        searchTask = Task { await resetAndLoadFirstPage(jump: jump, keepSelection: true) }
    }

    private func resetAndLoadFirstPage(jump: Date?, keepSelection: Bool = false) async {
        guard !fixedList else { return }
        // Invalidates any still-in-flight fetchPage from the *previous*
        // selection before this one's own request even goes out - see
        // searchGeneration's doc comment.
        searchGeneration += 1
        searchStarts += 1
        if keepSelection { keepPicks() }
        startSearchState(jump: jump)
        if !keepSelection {
            selected.removeAll()
            pickedAway = [:]
        }
        dragSelection = nil
        // A filter change makes any scrub in progress meaningless (its
        // target bucket was computed against the *previous* filter's
        // counts) - drop it rather than leave stale placeholders or a
        // thumb positioned against numbers that no longer apply.
        scrubFrac = nil
        placeholderCount = nil
        placeholderMonth = nil
        let myGeneration = searchGeneration
        async let buckets: Void = loadDateBuckets()
        await fetchUntilProgress()
        await buckets
        if myGeneration == searchGeneration { mergeLocalIfAny() }
    }

    /// The tile of the photo at `idx` (whose id is `id`) came on screen:
    /// the next page, once it is near the end.
    func loadMoreIfNeeded(index idx: Int, id: String) async {
        guard !fixedList else { return }
        // A tile of a grid that has been replaced since (a new search)
        // asks nothing.
        guard items.indices.contains(idx), items[idx].id == id else { return }
        // Issue #194: near the top after a jump, the photos newer than it.
        if idx < 12 { loadNewerIfNeeded(at: idx) }
        guard !endReached else { return }
        // Near the end is the only thing worth acting on - checked before
        // the in-flight case below so a tile appearing at the top of the
        // grid can't queue up a page nobody needs yet.
        guard idx >= items.count - 12 else { return }
        // A page that failed waits for its retry (or Try again, or a wake),
        // not for the next tile drawn: the web's retryReady().
        guard pageProblem == nil else { return }
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
        // In the model's own task (see pageTask), waited for here.
        await askForPages().value
    }

    /// Try again (and a wake): the page that failed, asked for now, its
    /// wait started over - the first page again (at a jump's month), or
    /// the next one by the token.
    func retryPage() {
        guard !fixedList, !endReached else { return }
        backOnline()
        pageRetry.reset()
        askForPages()
    }

    // MARK: Newer photos above a jump (issue #194)
    //
    // Only after a jump (a search started with `before`, in date order):
    // as the grid's top comes near (a tile in the first 12, or the grey
    // tiles kept for the top month's newer photos), the photos newer than
    // the jump's cutoff, oldest first, put above the grid newest first.
    // Never at the jump itself: the device runs a whole-library query to
    // start an upward search. A page that fails is asked again (upRetry,
    // as a later page's, at once on a wake or Try again) with the same
    // token and `have`.

    /// The grid wants the photos above it: after a jump, in date order,
    /// once its first page is in and while there are more.
    var upWanted: Bool {
        !fixedList && jumpBefore != nil && chips.isEmpty && !upEnd && placeholderCount == nil && !items.isEmpty
    }

    /// A tile near the grid's top (at `idx`; below 0 for the grey tiles
    /// over the first photo) came on screen: the next upward page, unless
    /// one is on its way (then it is remembered) or failed (it waits for
    /// its retry).
    func loadNewerIfNeeded(at idx: Int) {
        guard upWanted, upArmed, upProblem == nil, Date() >= upQuietUntil else { return }
        guard !upLoading else {
            upPendingAt = min(upPendingAt ?? idx, idx)
            return
        }
        askForNewer()
    }

    /// The grid was scrolled up (the view saw its offset go up): the newer
    /// photos may be asked for from now on, as the top comes near.
    func armNewer() {
        guard upWanted else { return }
        upArmed = true
    }

    /// Try again at the top (and a wake): the upward page that failed,
    /// asked for now with the same token and `have`.
    func retryNewer() {
        guard upWanted else { return }
        upRetry.reset()
        askForNewer()
    }

    @discardableResult
    private func askForNewer() -> Task<Void, Never> {
        let gen = searchGeneration
        let t = Task { [weak self] in
            guard let self, gen == self.searchGeneration else { return }
            await self.fetchNewerUntilProgress()
        }
        upTask = t
        return t
    }

    /// How many photos the upward search brought that the grid still has
    /// (a delete lowers it).
    private var upShown: Int { items.reduce(0) { $1.up ? $0 + 1 : $0 } }
    /// Its `have`: those, and those it brought that show from the downward
    /// search.
    private var upHave: Int { upShown + upHidden }

    /// The contents the grid shows (local files have none).
    private func shownHashes() -> Set<String> {
        Set(items.lazy.map(\.hash).filter { !$0.isEmpty })
    }

    /// Upward pages until one adds something, the top is reached, or (a
    /// device starting the search over and over) a dozen added nothing.
    private func fetchNewerUntilProgress() async {
        let gen = searchGeneration
        for _ in 0..<cMaxPagesWithoutProgress {
            let before = upShown
            guard case .loaded = await fetchNewerPage() else { return }
            if gen != searchGeneration || upEnd || upShown > before { return }
        }
        newerFailed(.failed, generation: gen, noProgress: true)
    }

    private func newerFailed(_ problem: LoadProblem, generation gen: Int, noProgress: Bool = false) {
        guard gen == searchGeneration else { return }
        upProblem = problem
        upRetry.failed(noProgress: noProgress) { [weak self] in
            guard let self, gen == self.searchGeneration, self.upWanted else { return }
            self.askForNewer()
        }
    }

    /// Asks for the next upward page: SearchPhotos with the jump's filters
    /// and newer_than its cutoff (on every page, retries too), upToken and
    /// upHave - never `before`, never the downward token.
    @discardableResult
    private func fetchNewerPage() async -> PageOutcome {
        guard upWanted, !upLoading, let cutoff = jumpBefore else { return .skipped }
        let gen = searchGeneration
        upLoading = true
        var added = 0
        defer {
            if gen == searchGeneration {
                upLoading = false
                // A tile that asked meanwhile, if it is still near the top
                // (the photos put above moved it down by `added`).
                let at = upPendingAt
                upPendingAt = nil
                if let at, upWanted, upProblem == nil, at + added < 12 { askForNewer() }
            }
        }
        var sp = SearchPhotosMsg()
        sp.personIds = selectedPeople
        sp.groupID = activeGroup?.id ?? ""
        sp.includeVideos = true
        sp.newerThan = SwiftProtobuf.Google_Protobuf_Timestamp(date: cutoff)
        let tok = upToken
        sp.token = tok
        sp.limit = tok.isEmpty ? PhotoPageLimit.first(deviceOmits: thumbs.cache.deviceOmits) : PhotoPageLimit.next
        sp.have = Int32(upHave)
        sp.smallThumbnails = true
        sp.omitThumbnails = true
        var resp: Msg_RespEnvelope?
        var failure: Error?
        do {
            resp = try await ask(.page, .reqSearchPhotos(sp), via: photoRequest)
        } catch {
            failure = error
        }
        guard gen == searchGeneration, !Task.isCancelled else { return .skipped }
        // An error answer has no page: a failed page, never an old device.
        guard let got = resp, case .respListOfFiles(let lof) = got.payload else {
            let problem = problemOf(resp, failure)
            print("[PhotoGallery] newer page failed (\(problem)): \(failure.map { "\($0)" } ?? resp?.errorMessage ?? "")")
            newerFailed(problem, generation: gen)
            return .failed
        }
        upRetry.reset()
        upProblem = nil
        let page = PhotoPaging.newer(files: lof.files, oldestFirst: lof.oldestFirst,
                                     shownIDs: Set(items.map(\.id)), shownHashes: shownHashes())
        guard case .files(let fresh) = page else {
            // A device before #194: its newest photos, dropped; not asked
            // again until the next sign-in.
            support.newerThan = false
            upEnd = true
            return .loaded
        }
        let ids = fresh.map(PhotoPaging.itemID)
        let keep = await thumbs.takePage(fresh, ids: ids)
        guard gen == searchGeneration, !Task.isCancelled else { return .skipped }
        let calendar = PhotoMonths.calendar()
        // A downward page may have landed meanwhile.
        let own = Set(items.lazy.filter(\.up).map(\.id))
        let new: [Item] = PhotoPaging.notShown(fresh, shownIDs: Set(items.map(\.id)), shownHashes: shownHashes()).map { i in
            let f = fresh[i]
            return Item(
                id: ids[i],
                path: f.path,
                mime: f.mime,
                size: Int(f.fileSize),
                thumbData: keep.indices.contains(i) ? keep[i] : nil,
                localURL: nil,
                isLocalOnly: false,
                month: f.hasCreated ? PhotoMonths.key(for: f.created.date, calendar: calendar) : nil,
                created: f.hasCreated ? f.created.date : nil,
                hash: f.hash,
                copies: f.hasCopies ? Int(f.copies) : nil,
                up: true
            )
        }
        upHidden += PhotoPaging.hidden(lof.files, own: own, added: new.count)
        let ended = PhotoPaging.newerEnded(token: lof.token, files: lof.files.count)
        // What is above the photos on the screen changes: photos put there,
        // or the top month's grey tiles going (the top reached).
        let changes = !new.isEmpty || (ended && items.first?.up == true)
        if changes { willChangeAbove?() }
        upToken = lof.token
        if ended { upEnd = true }
        if !new.isEmpty {
            putAbove(new)
            added = new.count
        }
        if changes {
            aboveChanges &+= 1
            if willChangeAbove != nil { upQuietUntil = Date() + 0.3 }
        }
        return .loaded
    }

    /// Photos put above the grid: what is kept by index moves down with
    /// the photos it was on (the viewer's open photo, a page asked for),
    /// and the lines of short months under them stay as they were.
    private func putAbove(_ new: [Item]) {
        if let top = items.first { upBreaks.insert(top.month ?? "-") }
        items.insert(contentsOf: new, at: 0)
        if let i = openIndex { openIndex = i + new.count }
        if let m = morePendingAt { morePendingAt = m + new.count }
    }

    /// Pages in a task of the model's own (pageTask): a tile's task that
    /// asked may go (scrolled away) while its page is still on its way. A
    /// newer search drops it, by its generation.
    @discardableResult
    private func askForPages() -> Task<Void, Never> {
        let gen = searchGeneration
        let t = Task { [weak self] in
            guard let self, gen == self.searchGeneration else { return }
            await self.fetchUntilProgress()
        }
        pageTask = t
        return t
    }

    /// A new search (or a jump): the old one's retry, pages and timer go
    /// with it.
    private func stopPageRetry() {
        pageRetry.reset()
        pageTask?.cancel()
        pageTask = nil
        slowTimer?.cancel()
        slowTimer = nil
    }

    /// The page asked for failed (`problem`: why) while its search is the
    /// current one: the grid says so, and it is asked again after 1 s,
    /// doubling to 10 s (the web's usePageRetry) - `noProgress`: from 10 s
    /// to a minute (PageRetry.noProgressDelay) - the wait cut short by a
    /// wake or Try again.
    private func pageFailed(_ problem: LoadProblem, generation gen: Int, noProgress: Bool = false) {
        guard gen == searchGeneration else { return }
        slowTimer?.cancel()
        slowTimer = nil
        pageProblem = problem
        slowFirstPage = false
        pageRetry.failed(noProgress: noProgress) { [weak self] in
            guard let self, gen == self.searchGeneration else { return }
            self.askForPages()
        }
    }

    /// The first page still on its way after a while: the grey tiles say so.
    private func watchSlowFirstPage(_ gen: Int) {
        slowTimer?.cancel()
        let wait = slowFirstPageAfter
        slowTimer = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .seconds(wait))
            guard !Task.isCancelled, let self, gen == self.searchGeneration,
                  self.items.isEmpty, self.pageProblem == nil else { return }
            self.slowFirstPage = true
        }
    }

    /// Photos deleted until none is left while more pages are to come:
    /// nothing on screen would ask for them.
    private func refillIfEmptied() {
        if items.isEmpty && !endReached && !loading && pageProblem == nil { askForPages() }
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
    ///
    /// A page that fails stops here: its retry is waiting already
    /// (pageFailed).
    private func fetchUntilProgress() async {
        let gen = searchGeneration
        for _ in 0..<cMaxPagesWithoutProgress {
            let before = items.count
            guard case .loaded = await fetchPage() else { return }
            if gen != searchGeneration || endReached || items.count > before { return }
        }
        // Page after page that added nothing (a device starting the search
        // over and over): said with its Try again, and asked for again in
        // a while rather than on and on - the grid promises the photos
        // (as Android: 10 s doubling to a minute, each round up to 12
        // pages).
        pageFailed(.failed, generation: gen, noProgress: true)
    }

    /// The furthest tile (its index) that asked for more while a fetch was
    /// already running, so the fetch that lands can honour it (see
    /// loadMoreIfNeeded).
    private var morePendingAt: Int?

    /// How many consecutive pages that add nothing to tolerate before
    /// giving up, so a device that keeps restarting the same search can
    /// never spin here forever.
    private let cMaxPagesWithoutProgress = 12

    /// How a page went.
    enum PageOutcome: Equatable {
        /// It came (it may have added nothing).
        case loaded
        /// It didn't: its retry is waiting (pageFailed).
        case failed
        /// Not asked (one is on its way, or the end is reached), or its
        /// answer belongs to a search since replaced: nothing failed.
        case skipped
    }

    /// Asks for the next page: the first of a search (token "", at a jump's
    /// cutoff), or the next one by the token. Records a failure itself
    /// (pageFailed): the caller only needs to know whether to go on.
    @discardableResult
    private func fetchPage() async -> PageOutcome {
        guard !loading, !endReached else { return .skipped }
        let myGeneration = searchGeneration
        loading = true
        backOnline()
        defer {
            // Only this request's own generation may clear loading - a
            // stale one finishing after a newer search started must not
            // report "done" for a fetch that isn't actually the current
            // one.
            if myGeneration == searchGeneration {
                loading = false
                // Only if that tile is still near the end. Every tile of a
                // small first page asks at once, and the page that just
                // landed moved the end well past them; honouring them
                // anyway pulled a third page nobody had scrolled to. Not
                // after a failure: that page waits for its retry.
                let at = morePendingAt
                morePendingAt = nil
                if let at, !endReached, pageProblem == nil, at >= items.count - 12 {
                    askForPages()
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
        let requestToken = token ?? ""
        // Issue #194: only what this (downward) search brought - not the
        // photos put above a jump by the upward one - those it brought
        // that show from the upward one included (downHidden).
        let have = items.reduce(0) { $1.up || $1.isLocalOnly ? $0 : $0 + 1 } + downHidden
        let shown = Set(items.map(\.path))
        let cutoff = jumpBefore
        // What came instead of a page (nil: nothing came), and what was
        // thrown instead of an answer: why the grid says it failed.
        var resp: Msg_RespEnvelope?
        var failure: Error?
        func page(_ tok: String, _ before: Date?) async {
            resp = nil
            failure = nil
            var sp = SearchPhotosMsg()
            sp.tags = tags
            sp.personIds = people
            sp.groupID = group // issue #115: inside a group, only its members
            // Issue #106: videos belong in the Images section. They
            // were excluded when this flag arrived (issue #60, where
            // only the composer opted in), which left a device's
            // videos unbrowsable from the app entirely.
            sp.includeVideos = true
            sp.token = tok
            // A search starting here (opening, a filter, the scrubber's
            // jump) gets a first page that paints quickly; scrolling on,
            // a bigger one. Pages without content are a few KB however
            // many photos: 60 then 120 (PhotoPageLimit); a device that
            // sends content anyway (before release 113) answers at most
            // its default, 30, so its first page stays small.
            sp.limit = tok.isEmpty ? PhotoPageLimit.first(deviceOmits: thumbs.cache.deviceOmits) : PhotoPageLimit.next
            // Lets the device resume where this grid actually is if
            // it no longer holds the token (see SearchPhotos.have) - and
            // serve the same page again to a retry of one whose answer
            // was lost: the same token, the same `have`.
            sp.have = Int32(have)
            // The grid's tiles: small thumbnails (release 111; an older
            // device sends big ones). The viewer shows one only until the
            // full size arrives (thumbnailStays).
            sp.smallThumbnails = true
            // Without them (release 113): the tiles come from the cache
            // on this phone, and what it lacks is asked for
            // (GridThumbLoader). An older device sends them anyway.
            sp.omitThumbnails = true
            // Issue #77: the date scrubber's "jump to date".
            if let before {
                sp.before = SwiftProtobuf.Google_Protobuf_Timestamp(date: before)
            }
            do {
                resp = try await ask(.page, .reqSearchPhotos(sp), via: photoRequest)
            } catch {
                failure = error
            }
        }

        // The cutoff goes only on the request that starts the search (a
        // first page asked again after a jump starts at its month again);
        // the token carries the place after.
        await page(requestToken, requestToken.isEmpty ? cutoff : nil)
        // A newer search superseded this one while it was in flight -
        // discard rather than let a stale reply clobber current results.
        // Task.isCancelled backs up the generation check (a new search
        // cancels this call's task the moment it starts) - belt and
        // suspenders, since either alone catches the same case here.
        guard myGeneration == searchGeneration, !Task.isCancelled else { return .skipped }
        // After a jump, a page from a search the device started again
        // (lostCutoff) is from the wrong end of the library. Asked once
        // more with the cutoff, the device runs the jump's search, skipping
        // the `have` photos the grid holds, whatever the token (as the web
        // and Android do).
        if !requestToken.isEmpty, let cutoff, let first = resp, case .respListOfFiles(let lof) = first.payload,
           PhotoPaging.lostCutoff(files: lof.files.map { ($0.hasCreated ? $0.created.date : nil, $0.path) },
                                  token: lof.token, sent: requestToken, cutoff: cutoff, shown: shown) {
            await page(requestToken, cutoff)
            guard myGeneration == searchGeneration, !Task.isCancelled else { return .skipped }
        }
        // An error answer (the bridge's "device unreachable", the device's
        // own "internal error") is no page and no end of the library either.
        guard let got = resp, case .respListOfFiles(let lof) = got.payload else {
            let problem = problemOf(resp, failure)
            print("[PhotoGallery] page failed (\(problem)): \(failure.map { "\($0)" } ?? resp?.errorMessage ?? "")")
            pageFailed(problem, generation: myGeneration)
            return .failed
        }

        // Every entry is a photo of the grid, with its content or without
        // (`have` counts them all); those it already shows are left out -
        // by content too (issue #194: one whose oldest copy changed can
        // come from the upward search as well), as the upward pages are.
        let fresh = PhotoPaging.notShown(lof.files, shownIDs: Set(items.map(\.id)), shownHashes: shownHashes()).map { lof.files[$0] }
        let ids = fresh.map(PhotoPaging.itemID)
        // Content kept in the cache on this phone, the tiles that show
        // first decoded off the main thread before they draw (see
        // GridThumbCache), what the cache lacks asked for.
        let keep = await thumbs.takePage(fresh, ids: ids)
        guard myGeneration == searchGeneration, !Task.isCancelled else { return .skipped }
        // Gregorian, in the viewer's time zone: the device's buckets'.
        let calendar = PhotoMonths.calendar()
        // An upward page may have landed meanwhile.
        let own = Set(items.lazy.filter { !$0.up && !$0.isLocalOnly }.map(\.id))
        let filtered = PhotoPaging.notShown(fresh, shownIDs: Set(items.map(\.id)), shownHashes: shownHashes()).map { i in
            let f = fresh[i]
            return Item(
                id: ids[i],
                path: f.path,
                mime: f.mime,
                size: Int(f.fileSize),
                thumbData: keep.indices.contains(i) ? keep[i] : nil,
                localURL: nil,
                isLocalOnly: false,
                month: f.hasCreated ? PhotoMonths.key(for: f.created.date, calendar: calendar) : nil,
                created: f.hasCreated ? f.created.date : nil,
                hash: f.hash,
                copies: f.hasCopies ? Int(f.copies) : nil
            )
        }
        downHidden += PhotoPaging.hidden(lof.files, own: own, added: filtered.count)
        if !filtered.isEmpty { items.append(contentsOf: filtered) }

        self.token = lof.token.isEmpty ? nil : lof.token
        self.endReached = (self.token == nil)
        pageRetry.reset()
        slowTimer?.cancel()
        slowTimer = nil
        pageProblem = nil
        slowFirstPage = false
        return .loaded
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
        // Another item: the last one's big thumbnail is no longer asked for.
        if bigThumbFor != items[index].path { stopBigThumb() }
        openIndex = index
        videoPlayer?.pause()
        videoPlayer = nil
        playerWatch = []
        infoOpen = false
        infoData = nil
        // The open photo first, alone; its neighbours once it is on screen.
        let path = items[index].path
        Task {
            await fetchHiRes(index: index)
            await readAhead(around: path)
        }
        // Until the full size comes the viewer shows the grid's tile: its
        // own and the neighbours', decoded again from the thumbnail cache
        // if they were let go.
        if !fixedList {
            thumbs.readAhead(items[max(0, index - 1)...min(items.count - 1, index + 1)].compactMap(thumbWant))
        }
    }
    func closeModal() {
        openIndex = nil
        hiResImages.removeAll()
        hiResSource.removeAll()
        hiResOrder.removeAll()
        hiResFailed.removeAll()
        stopBigThumb()
        bigThumbs.removeAll()
        bigThumbOrder.removeAll()
        unplayable.removeAll()
        videoPlayer?.pause()
        videoPlayer = nil
        playerWatch = []
    }

    /// The open item's path; nil while the viewer is closed.
    var openPath: String? {
        openIndex.flatMap { items.indices.contains($0) ? items[$0].path : nil }
    }

    private func stopBigThumb() {
        bigThumbTask?.cancel()
        bigThumbTask = nil
        bigThumbFor = nil
    }

    /// The open item's thumbnail stays on screen (`path`): the full size
    /// failed or can't be decoded, the video couldn't be fetched or played.
    /// The grid's small tile scaled up would be all there is, so the big
    /// thumbnail is asked for (GetThumbnails without small_thumbnails) -
    /// and asked again like a grid's page, 1 s doubling to 10 s and at once
    /// on a wake, for as long as the viewer stays on it (the full size
    /// usually failed because the connection did, and then so does this).
    /// Android's thumbnailStays, the web's MediaViewer.
    func thumbnailStays(_ path: String) {
        guard openPath == path, bigThumbs[path] == nil else { return }
        if bigThumbFor == path, bigThumbTask != nil { return }
        bigThumbTask?.cancel()
        bigThumbFor = path
        bigThumbTask = Task { [weak self] in
            var failures = 0
            while !Task.isCancelled {
                guard let self, self.openPath == path else { break }
                if let got = await self.askBigThumb(path) {
                    if !got.isEmpty, !Task.isCancelled, self.openPath == path {
                        let img = await Task.detached(priority: .userInitiated) {
                            CGImageSourceCreateWithData(got as CFData, nil).flatMap { Self.decodeForDisplay($0) }
                        }.value
                        if let img, self.openPath == path { self.keepBigThumb(img, for: path) }
                    }
                    break
                }
                failures += 1
                await OTCConnection.shared.sleepOrWake(self.bigThumbDelay(failures))
            }
            guard let self, !Task.isCancelled, self.bigThumbFor == path else { return }
            self.bigThumbFor = nil
            self.bigThumbTask = nil
        }
    }

    private func keepBigThumb(_ img: UIImage, for path: String) {
        bigThumbOrder.removeAll { $0 == path }
        bigThumbOrder.append(path)
        var kept = bigThumbs
        kept[path] = img
        while bigThumbOrder.count > Self.bigThumbsKept {
            kept.removeValue(forKey: bigThumbOrder.removeFirst())
        }
        bigThumbs = kept
    }

    /// One ask for `path`'s big thumbnail: its bytes; empty when there is
    /// none to have (the device left the path out, or a device before
    /// release 80 has no GetThumbnails - the grid's own stays); nil when
    /// the ask failed and is worth repeating.
    private func askBigThumb(_ path: String) async -> Data? {
        var g = Msg_GetThumbnails()
        g.paths = [path]
        g.smallThumbnails = false
        guard let resp = try? await photoRequest(.reqGetThumbnails(g), .thumbnail) else { return nil }
        if case .respListOfFiles(let lof) = resp.payload {
            return lof.files.first { $0.path == path && !$0.content.isEmpty }?.content ?? Data()
        }
        if resp.errorCode == "unknown_payload" { return Data() }
        // The bridge answering for a device it can't reach, an error.
        return nil
    }

    /// The open video (`path`) couldn't be fetched or played: its poster
    /// stays (the big one), saying so, with Try again.
    func videoFailed(_ path: String) {
        guard openPath == path else { return }
        unplayable.insert(path)
        videoPlayer?.pause()
        videoPlayer = nil
        playerWatch = []
        thumbnailStays(path)
    }

    /// The unplayable video's Try again: fetched and played again.
    func retryVideo() {
        guard let i = openIndex, let path = openPath else { return }
        unplayable.remove(path)
        open(index: i)
    }

    /// What the player can't play (a codec this phone lacks, a stream that
    /// broke) goes back to its poster, saying so, rather than a crossed-out
    /// play button.
    private func watchPlayback(_ player: AVPlayer, path: String) {
        guard let item = player.currentItem else { return }
        let status = item.observe(\.status, options: [.new]) { [weak self] item, _ in
            guard item.status == .failed else { return }
            Task { @MainActor in self?.videoFailed(path) }
        }
        let broke = NotificationCenter.default.addObserver(forName: .AVPlayerItemFailedToPlayToEndTime, object: item, queue: .main) { [weak self] _ in
            Task { @MainActor in self?.videoFailed(path) }
        }
        playerWatch = [status, ObserverToken(broke)]
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
            notice = .notLoaded
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
                    self.notice = .shareFailed(error)
                    self.showAlert = true
                }
            }
        }
    }

    /// Issue #201: the viewer's Save is saving the original at this path:
    /// its button shows a spinner ("Saving…") until it is done.
    @Published private(set) var savingPath: String?

    /// Issue #201: Save to Photos saves the original file - its own bytes,
    /// so its date, place and content stay, and the next sync of the phone
    /// finds the same content and links it, where a picture re-encoded
    /// from the screen came back as a new photo dated today. Downloaded in
    /// 4 MiB pieces (FileDownload, a HEIC's original too) to a temporary
    /// file, added to the library as a photo or a video resource, dated as
    /// Images dates it. Only if Photos refuses a photo's format is the
    /// picture on screen saved instead (`fallback`), as before. Asks for
    /// add-only access to the library, as before.
    func saveOriginalToPhotos(fallback: @escaping @MainActor () async -> UIImage?) {
        guard savingPath == nil, let i = openIndex, items.indices.contains(i) else { return }
        let it = items[i]
        savingPath = it.path
        Task {
            defer { savingPath = nil }
            let current = PHPhotoLibrary.authorizationStatus(for: .addOnly)
            let status = current == .notDetermined
                ? await PHPhotoLibrary.requestAuthorization(for: .addOnly)
                : current
            guard status == .authorized || status == .limited else {
                notice = .photosDenied
                showAlert = true
                return
            }
            let video = it.mime.hasPrefix("video/")
            let name = Self.originalName(path: it.path, mime: it.mime)
            var folder: URL?
            defer { if let folder { try? FileManager.default.removeItem(at: folder) } }
            let file: URL
            if let local = it.localURL {
                file = local
            } else {
                let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
                folder = dir
                do {
                    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
                    file = dir.appendingPathComponent(name)
                    try await FileDownload.download(path: it.path, mime: it.mime, to: file, original: true)
                } catch {
                    notice = .saveFailed(error)
                    showAlert = true
                    return
                }
            }
            do {
                try await Self.addToPhotos(file, video: video, created: it.created, name: name)
                notice = .saved
            } catch {
                // A format Photos takes no original of: the picture shown.
                if !video, let img = await fallback() {
                    do {
                        try await PHPhotoLibrary.shared().performChanges {
                            let request = PHAssetChangeRequest.creationRequestForAsset(from: img)
                            // A4: a copy of the device's photo, not one for
                            // the sync to send back (SavedToPhone).
                            SavedToPhone.shared.remember(request.placeholderForCreatedAsset?.localIdentifier ?? "")
                        }
                        notice = .saved
                    } catch {
                        notice = .saveFailed(error)
                    }
                } else {
                    notice = .saveFailed(error)
                }
            }
            showAlert = true
        }
    }

    /// The original's name in the library (and its temporary file's, whose
    /// extension tells Photos the format): the file's own, given an
    /// extension from its type when it has none.
    nonisolated static func originalName(path: String, mime: String) -> String {
        let leaf = (path as NSString).lastPathComponent
        let base = leaf.isEmpty ? "photo" : leaf
        guard (base as NSString).pathExtension.isEmpty else { return base }
        switch mime.lowercased() {
        case "image/jpeg": return base + ".jpg"
        case "image/heic", "image/heif": return base + ".heic"
        case "image/png": return base + ".png"
        case "image/gif": return base + ".gif"
        case "video/quicktime": return base + ".mov"
        case "video/mp4", "video/x-m4v": return base + ".mp4"
        default: return base
        }
    }

    /// Adds the file at `url` to the photo library as it is: a photo or a
    /// video, named `name`, taken at `created` when known.
    nonisolated private static func addToPhotos(_ url: URL, video: Bool, created: Date?, name: String) async throws {
        try await PHPhotoLibrary.shared().performChanges {
            let request = PHAssetCreationRequest.forAsset()
            let options = PHAssetResourceCreationOptions()
            options.originalFilename = name
            request.addResource(with: video ? .video : .photo, fileURL: url, options: options)
            if let created { request.creationDate = created }
            // A4: noted before the change lands, so the sync it starts
            // already leaves it out (SavedToPhone).
            SavedToPhone.shared.remember(request.placeholderForCreatedAsset?.localIdentifier ?? "")
        }
    }

    /// The photo at `path` at full resolution, if its full size is still
    /// here: Save's fallback, for the photo it was tapped on.
    func fullImage(of path: String) async -> UIImage? {
        guard let src = hiResSource[path] else { return nil }
        return await Task.detached(priority: .userInitiated) { () -> UIImage? in
            switch src {
            case .data(let d): return UIImage(data: d)
            case .file(let u): return UIImage(contentsOfFile: u.path)
            }
        }.value
    }

    /// Issue #201: the open item's copies, for the viewer's Delete: how
    /// many paths hold it when there is more than one, else nil (asked as
    /// before).
    var openCopies: Int? {
        guard let i = openIndex, items.indices.contains(i), let n = items[i].copies, n > 1 else { return nil }
        return n
    }

    // Issue #9: delete the currently-open photo, iOS Photos app-style —
    // removes it from the server and advances to the next one (or closes
    // the viewer if it was the last one left). Issue #201: `everyCopy`
    // deletes every copy of it (the question's "all"), and the content it
    // was shown with goes along (DeleteCopies.request).
    func deleteCurrentPhoto(everyCopy: Bool = false) {
        guard let idx = openIndex, items.indices.contains(idx) else { return }
        let item = items[idx]
        Task {
            let outcome: DeleteCopies.Outcome
            do {
                let del = DeleteCopies.request(path: item.path, hash: item.hash, copies: item.copies, everyCopy: everyCopy)
                outcome = DeleteCopies.outcome(try await photoRequest(.reqDelFile(del), nil))
            } catch {
                notice = .deleteFailed(error)
                showAlert = true
                return
            }
            if case .refused(let message, let reason) = outcome {
                // Nothing was deleted: the device's words, the tile stays.
                notice = .deleteRefused(message)
                showAlert = true
                if reason == .changed { contentChanged(item.path) }
                return
            }

            // Found again by its id: photos put above the grid meanwhile
            // (issue #194) moved it.
            guard let at = items.firstIndex(where: { $0.id == item.id }) else { return }
            items.remove(at: at)
            selected.remove(item.path)
            // Nothing of it stays on the phone either.
            forgetThumbnails(of: [item])
            onDeleted?(item.path)
            countsChanged()
            if items.isEmpty {
                closeModal()
            } else if openIndex != nil {
                open(index: min(at, items.count - 1))
            }
        }
    }

    /// Issue #201: a delete was refused because `path` holds other content
    /// than was shown: the viewer closes (its photos are out of date), its
    /// alert says why, and Images reads its search again (at the jump, if
    /// there was one) - Files (the viewer's own list) its listing. The
    /// same on the web and Android.
    private func contentChanged(_ path: String) {
        closeModal()
        if fixedList {
            onDeleted?(path)
        } else {
            reloadSearch()
        }
    }

    /// Steps closer together than this are a held or rapid swipe: the
    /// photos it passes read nothing ahead (the web's cRapidStepMs).
    nonisolated static let rapidStep: Duration = .milliseconds(250)

    /// Read-ahead: once the open photo (`path`) is on screen at full size,
    /// the next one and then the previous one are readied for the slide,
    /// one at a time (fetched, or read from FullPhotoCache). Only after the
    /// open photo: started with it, they took the connection from it and
    /// the photo being looked at came later. Only if the viewer is still on
    /// it rapidStep later, so a held swipe doesn't fetch around every photo
    /// it passes. Not after a failure, and not around a video (neither
    /// shows a full-size picture). As the web's MediaViewer.
    private func readAhead(around path: String) async {
        guard hiResImages[path] != nil, openPath == path else { return }
        try? await Task.sleep(for: Self.rapidStep)
        for step in [1, -1] {
            // Moved on: the item the viewer is on reads around itself.
            guard openPath == path, let i = openIndex else { return }
            await fetchHiRes(index: i + step, prefetch: true)
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
            if !prefetch && !unplayable.contains(it.path) { await fetchVideo(it) }
            return
        }

        if hiResImages[it.path] != nil { return }
        // On its way already (read ahead, and now opened): waited for, not
        // asked for twice.
        if let running = inFlightHiRes[it.path] {
            await running.value
            return
        }
        let load = Task { await loadHiRes(it) }
        inFlightHiRes[it.path] = load
        await load.value
        inFlightHiRes[it.path] = nil
    }

    /// The full size of `it` into hiResImages: its local file, the bytes
    /// kept this session, or GetFile.
    private func loadHiRes(_ it: Item) async {
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
        // Fetched earlier this session (FullPhotoCache): shown from those
        // bytes, nothing asked of the device.
        if let kept = await fullPhotos.lookup(it.hash) {
            let img = await Task.detached(priority: .userInitiated) {
                CGImageSourceCreateWithData(kept as CFData, nil).flatMap { Self.decodeForDisplay($0) }
            }.value
            if let img {
                if openIndex != nil { cacheHiRes(img, source: .data(kept), for: it.path) }
                return
            }
            // Bytes that don't decode aren't kept: fetched again below.
            fullPhotos.discard(it.hash)
        }
        do {
            var gf = GetFileMsg()
            gf.path = it.path
            let resp = try await photoRequest(.reqGetFile(gf), nil)
            if case .respFile(let f) = resp.payload {
                let data = f.content
                let img = await Task.detached(priority: .userInitiated) {
                    CGImageSourceCreateWithData(data as CFData, nil).flatMap { Self.decodeForDisplay($0) }
                }.value
                if let img {
                    // Kept for the session under the hash the answer
                    // carries (the file's row): a tile listed before the
                    // file changed holds the old one, and the new content
                    // mustn't be found under it.
                    fullPhotos.store(f.hash.isEmpty ? it.hash : f.hash, data: data)
                    if openIndex != nil { cacheHiRes(img, source: .data(data), for: it.path) }
                    return
                }
            }
        } catch {}
        // Failed, or a format the phone can't decode: the thumbnail stays,
        // the big one if it is the open photo (a neighbour's full size is
        // asked again when it is swiped to).
        hiResFailed.insert(it.path)
        thumbnailStays(it.path)
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
            let player = AVPlayer(url: u)
            watchPlayback(player, path: it.path)
            self.videoPlayer = player
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
            watchPlayback(player, path: it.path)
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
            watchPlayback(player, path: it.path)
            self.videoPlayer = player
        } catch {
            print("[video] download of \(it.path) failed: \(error)")
            // Neither streamed nor downloaded: its poster stays (the big
            // one), saying so, with Try again.
            if stillOpen() { videoFailed(it.path) }
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

    // MARK: Selection (no checkbox): a long press selects, and a drag on
    // from it selects the range (issue #200, DragSelect.swift); while
    // anything is selected a tap toggles.
    func toggleSelect(_ path: String) {
        if selected.contains(path) { selected.remove(path) }
        else { selected.insert(path) }
    }

    /// Issue #200: the drag selection under way, if any.
    private var dragSelection: DragSelection<String>?

    /// A long press on the photo at `index`: it is selected (whether or not
    /// it was), and a drag on from it selects the range.
    func beginDragSelect(at index: Int) {
        guard items.indices.contains(index) else { return }
        let drag = DragSelection(base: selected, start: items[index].path)
        dragSelection = drag
        selected.insert(drag.start)
    }

    /// The finger is over the photo at `index` now (nil: over none of
    /// them): every photo from the one pressed to it is selected, in the
    /// grid's order, on top of what was selected before (less what has
    /// gone from the grid since: items). False once the drag is over: the
    /// pressed photo has gone from the grid (a refresh took it away), and
    /// the selection stays as it stands (DragSelection's rule).
    @discardableResult
    func dragSelect(to index: Int?) -> Bool {
        guard var drag = dragSelection else { return false }
        let order = items.map(\.path)
        guard drag.goesOn(in: order) else {
            dragSelection = nil
            return false
        }
        guard let index, items.indices.contains(index), drag.move(to: items[index].path) else { return true }
        dragSelection = drag
        if let s = drag.selection(in: order) { selected = s }
        return true
    }

    func endDragSelect() { dragSelection = nil }

    /// The selection bar's x (or Escape on an iPad's keyboard): nothing is
    /// picked any more, a drag under way is over (the finger going on
    /// picks nothing), and the picks kept from an earlier grid go too - a
    /// tap opens a photo again.
    func clearSelection() {
        selected.removeAll()
        pickedAway = [:]
        dragSelection = nil
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
                    notice = .uploadFailed(error)
                    showAlert = true
                    return false
                }
            }
        }
        return true
    }

    /// Issue #201: picked photos the grid no longer holds - a jump or a
    /// reload replaced it while the selection stayed - as they were last
    /// shown: what their delete sends (the content they were shown with,
    /// their copies) until the grid shows them again. A photo that
    /// changed meanwhile is refused again ("changed") rather than deleted
    /// unseen, as on the web.
    private var pickedAway: [String: Item] = [:]

    /// The grid is about to be replaced and the selection stays: the
    /// picked photos it shows are remembered (pickedAway).
    private func keepPicks() {
        guard !selected.isEmpty else {
            pickedAway = [:]
            return
        }
        pickedAway = pickedAway.filter { selected.contains($0.key) }
        for it in items where selected.contains(it.path) && !it.isLocalOnly { pickedAway[it.path] = it }
    }

    /// The picked photos by path: as the grid shows them now (a page that
    /// brought one back after a reload: its content now), else as they
    /// were last shown.
    private func pickedItems() -> [String: Item] {
        var picked: [String: Item] = [:]
        for it in items where selected.contains(it.path) && picked[it.path] == nil { picked[it.path] = it }
        for (path, it) in pickedAway where picked[path] == nil && selected.contains(path) { picked[path] = it }
        return picked
    }

    /// Issue #201: what the selection bar's Delete asks - about the copies
    /// when any of the picked photos has some - taken when it is tapped,
    /// so the words don't change under the question.
    @Published private(set) var deleteAsk: CopiesAsk = .plain

    /// The selection bar's Delete: asks first (issue #45).
    func askDeleteSelected() {
        let picked = pickedItems()
        deleteAsk = CopiesAsk.of(selected.map { picked[$0]?.copies })
        confirmDeleteSelected = true
    }

    // Issue #45: delete every currently-selected photo/video from the grid,
    // mirroring deleteCurrentPhoto()'s single-item flow above. Issue #201:
    // `everyCopy` (the question's "every copy") deletes every copy of the
    // picked photos that have other copies; the rest as before.
    func deleteSelected(everyCopy: Bool = false) {
        let paths = Array(selected)
        guard !paths.isEmpty else { return }
        let picked = pickedItems()
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
            // Those still there, and why: said once all are done (issue
            // #201: one in the device's words, several summed up).
            var kept: [DeleteCopies.Kept] = []
            for path in paths {
                let it = picked[path]
                let del = DeleteCopies.request(path: path, hash: it?.hash ?? "", copies: it?.copies, everyCopy: everyCopy)
                do {
                    let outcome = DeleteCopies.outcome(try await photoRequest(.reqDelFile(del), nil))
                    if case .refused(let message, let reason) = outcome {
                        kept.append(.refused(message, reason))
                        continue
                    }
                } catch {
                    kept.append(.failed(error))
                    continue
                }
                deletedPaths.insert(path)
            }
            if !deletedPaths.isEmpty {
                // Nothing of them stays on the phone either.
                forgetThumbnails(of: items.filter { deletedPaths.contains($0.path) }
                                 + pickedAway.values.filter { deletedPaths.contains($0.path) })
                items.removeAll { deletedPaths.contains($0.path) }
                selected.subtract(deletedPaths)
                pickedAway = pickedAway.filter { !deletedPaths.contains($0.key) }
                // The months' and the open collection's counts, whatever
                // else happened.
                countsChanged()
            }
            if !kept.isEmpty {
                notice = .deletesKept(kept)
                showAlert = true
            }
            // Something shown is out of date: the grid is read again, at
            // the jump if there was one; what was refused stays picked.
            if kept.contains(where: { if case .refused(_, .changed) = $0 { return true } else { return false } }) {
                reloadSearch()
            }
        }
    }

    /// Deleted photos' thumbnails leave the cache on this phone (the Files
    /// viewer's items carry their hashes too).
    private func forgetThumbnails(of deleted: [Item]) {
        for it in deleted where !it.hash.isEmpty {
            thumbs.cache.discard(it.hash)
        }
    }

    /// Photos deleted from Images: the months' counts (the scrubber, the
    /// headers, the last month's room) and the open collection's count
    /// follow, as on Android and the web.
    private func countsChanged() {
        guard !fixedList else { return }
        refillIfEmptied()
        Task { await loadDateBuckets() }
        // Asked again in the background until it comes (loadGroups).
        if activeGroup != nil { Task { await loadGroups() } }
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
                    notice = .noShareLink
                    showAlert = true
                }
            } catch {
                notice = .shareFailed(error)
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
                } else { notice = .noDownloadLink; showAlert = true }
            } catch { notice = .downloadFailed(error); showAlert = true }
        }
    }

}

/// What the gallery's alert says: what happened, worded only when the alert
/// shows (docs/i18n.md, "Each platform") - in the language shown then, and
/// with an error's own description read then. The device's words are
/// shown as they come (it writes them in the request's language).
enum GalleryNotice {
    case renameFailed, deleteCollectionFailed, createFailed, addFailed, removeFailed
    /// A new collection, by its name.
    case created(String)
    /// Added to the collection of this name.
    case added(String)
    /// Taken out of the collection of this name.
    case removed(String)
    /// Not this person / Someone else… didn't go through.
    case personRemoveFailed, personMoveFailed
    /// This many photos taken off the person of this name ("": unnamed).
    case personRemoved(Int, String)
    /// This many photos moved to the person of this name ("": unnamed).
    case personMoved(Int, String)
    case notLoaded, photosDenied, saved, noShareLink, noDownloadLink
    case shareFailed(Error), saveFailed(Error), deleteFailed(Error), uploadFailed(Error), downloadFailed(Error)
    /// The device refused a delete: its own explanation.
    case deleteRefused(String)
    /// Issue #201: a delete of several left some: each one's words, or
    /// several summed up (DeleteCopies.keptText).
    case deletesKept([DeleteCopies.Kept])

    var text: String {
        switch self {
        case .renameFailed: return S.appPhotosNoticeRenameFailed
        case .deleteCollectionFailed: return S.appPhotosNoticeDeleteCollectionFailed
        case .createFailed: return S.appPhotosNoticeCreateFailed
        case .addFailed: return S.appPhotosNoticeAddFailed
        case .removeFailed: return S.appPhotosNoticeRemoveFailed
        case .created(let name): return S.appPhotosNoticeCreated(name: name)
        case .added(let name): return S.appPhotosNoticeAdded(name: name)
        case .removed(let name): return S.appPhotosNoticeRemoved(name: name)
        case .personRemoveFailed: return S.appPhotosPersonRemoveFailed
        case .personMoveFailed: return S.appPhotosPersonMoveFailed
        case .personRemoved(let n, let name):
            return name.isEmpty ? S.appPhotosPersonRemovedUnnamed(count: n) : S.appPhotosPersonRemoved(count: n, name: name)
        case .personMoved(let n, let name):
            return name.isEmpty ? S.appPhotosPersonMovedUnnamed(count: n) : S.appPhotosPersonMoved(count: n, name: name)
        case .notLoaded: return S.appPhotosNoticeNotLoaded
        case .photosDenied: return S.iosPhotosAccessDenied
        case .saved: return S.appPhotosNoticeSaved
        case .noShareLink: return S.appPhotosNoticeNoShareLink
        case .noDownloadLink: return S.appPhotosNoticeNoDownloadLink
        case .shareFailed(let e): return S.appPhotosNoticeShareFailed(detail: e.localizedDescription)
        case .saveFailed(let e): return S.appPhotosNoticeSaveFailed(detail: e.localizedDescription)
        case .deleteFailed(let e): return S.appPhotosNoticeDeleteFailed(detail: e.localizedDescription)
        case .uploadFailed(let e): return S.iosPhotosUploadFailed(detail: e.localizedDescription)
        case .downloadFailed(let e): return S.iosPhotosDownloadFailed(detail: e.localizedDescription)
        case .deleteRefused(let message): return S.appPhotosNoticeDeleteFailed(detail: message)
        case .deletesKept(let kept): return DeleteCopies.keptText(kept) ?? ""
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
    /// Issue #194: where the rows on the screen are, so that photos put
    /// above them leave them where they are (GridAnchor).
    @State private var anchor = GridAnchor()
    /// The grid's first row is on the screen: the newer photos' status
    /// shows over it.
    @State private var atGridTop = false
    /// Issue #198: the date scrubber out at the screen's side when the
    /// grid stops short of it (a phone on its side): how far short it
    /// stops, and which way round the phone is turned - so which side its
    /// camera is on (ScrubberPlacement).
    @State private var scrubberGaps = ScrubberPlacement.Gaps()
    @State private var interfaceOrientation = UIInterfaceOrientation.unknown
    /// Issue #200: where each tile on screen is, by its index, for a drag
    /// selection's finger (DragSelect.swift), in the grid's own space.
    @State private var dragFrames = DragSelectFrames<Int>()
    private static let gridSpaceName = "photoGrid"
    private static let gridSpace = DragSelectSpace.named(gridSpaceName)
    @Environment(\.layoutDirection) private var layoutDirection

    private var scrubberPlacement: ScrubberPlacement {
        ScrubberPlacement(
            scrubberGaps,
            cameraAtTrailing: ScrubberPlacement.cameraAtTrailing(interfaceOrientation, rightToLeft: layoutDirection == .rightToLeft)
        )
    }

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
                        gridMessage
                        // One row per line of the grid, lazily: a big
                        // month's title spans the row over its own rows of
                        // tiles; months sharing a line are one row.
                        LazyVStack(alignment: .leading, spacing: 0) {
                            ForEach(layout.rows) { row in
                                gridRow(row, layout: layout)
                                    .padding(.top, row.top)
                                    // Issue #194: where it is on the screen.
                                    .onGeometryChange(for: CGRect.self) { $0.frame(in: .scrollView) } action: { anchor.record(row.id, $0) }
                                    .onDisappear { anchor.gone(row.id) }
                            }
                        }
                        .scrollTargetLayout()
                        // Issue #200: a long press selects a photo, and a
                        // drag on from it every photo up to the finger.
                        .coordinateSpace(.named(Self.gridSpaceName))
                        .gesture(dragSelect(layout))
                        if layout.placeholder == .none && !vm.items.isEmpty {
                            gridFooter
                        }
                    }
                    .padding(.horizontal, layout.metrics.pad)
                    .padding(.bottom, 8)
                }
                // Issue #194: scrolled up (not at the jump, which lands at the
                // top): the newer photos above may come.
                .onScrollGeometryChange(for: CGFloat.self) { $0.contentOffset.y } action: { old, new in
                    guard PhotoPaging.scrolledUp(from: old, to: new), !vm.upEnd, !vm.upArmed else { return }
                    vm.armNewer()
                    newerIfNearTop()
                }
                // Where its rows show.
                .onScrollGeometryChange(for: GridAnchor.Viewport.self) { g in
                    GridAnchor.Viewport(height: g.containerSize.height, top: g.contentInsets.top, bottom: g.contentInsets.bottom)
                } action: { _, v in anchor.viewport = v }
                // Issue #194: photos put above the grid - the row that was
                // at the top of the screen goes back where it was.
                .onChange(of: vm.aboveChanges) { _, _ in
                    if let kept = anchor.taken() {
                        proxy.scrollTo(kept.id, anchor: kept.unitPoint)
                    }
                    // Still near the top once that is done (the top month's
                    // grey tiles on the screen): the next page.
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.35) { newerIfNearTop() }
                }
                .onAppear {
                    vm.willChangeAbove = { [anchor, vm] in
                        let now = vm.gridLayout(metrics)
                        anchor.take(first: now.rows.first?.id, grows: now.metrics.lineGap - now.metrics.top)
                    }
                }
                // The newer photos on their way, or that they failed (with
                // Try again), over the grid's first row - laid over it, so
                // nothing under it moves.
                .overlay(alignment: .top) {
                    if atGridTop && layout.placeholder == .none {
                        newerStatus.padding(.top, 8)
                    }
                }
                .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { w in
                    widthChanged(to: w, proxy: proxy)
                }
                // Issue #198: how far the grid stops short of the screen's
                // trailing side (the safe area's band down a phone on its
                // side), and how far its ends are from the screen's top
                // and bottom - where the scrubber goes (ScrubberPlacement).
                .onGeometryChange(for: ScrubberPlacement.Gaps.self) { g in
                    ScrubberPlacement.Gaps(
                        trailing: g.safeAreaInsets.trailing,
                        top: g.frame(in: .global).minY,
                        bottom: g.safeAreaInsets.bottom
                    )
                } action: { scrubberGaps = $0 }
                // And which way round the phone is: the band beside a
                // notch is not crossed.
                .background(InterfaceOrientationReader { interfaceOrientation = $0 })
                .onChange(of: vm.searchStarts) { _, _ in
                    proxy.scrollTo("photoPageTop", anchor: .top)
                }
                // The row at the top, as the grid scrolls - kept aside, not
                // in state: nothing needs drawing again for it.
                .onScrollTargetVisibilityChange(idType: String.self, threshold: 0.5) { ids in
                    scrollMemo.visible(ids)
                    let top = layout.rows.first.map { ids.contains($0.id) } ?? false
                    if top != atGridTop { atGridTop = top }
                    newerIfNearTop()
                }
                // The first page is slow to come (a slow link, a busy
                // device): over the grey tiles, that it is still on its way.
                .overlay(alignment: .bottom) {
                    if showsStillWaiting {
                        StillWaitingPill()
                            .padding(.bottom, vm.selected.isEmpty ? 24 : 104)
                            .transition(.opacity)
                    }
                }
                .animation(.default, value: showsStillWaiting)
                .overlay(alignment: .trailing) {
                    // Issue #77: Google-Photos-style date scrubber -
                    // overlaid directly on the grid's own right edge
                    // (like Google Photos' own does) rather than
                    // reserving dedicated layout space for it, so it
                    // can't push a fixed 3-column grid past the screen's
                    // actual width. Ticks/tooltip only show while
                    // actively dragging - see PhotoDateScrubber. On a
                    // phone on its side it reaches on past that edge to
                    // the screen's - beside a notch only to the notch
                    // (issue #198, ScrubberPlacement).
                    if vm.showScrubber {
                        PhotoDateScrubber(vm: vm, scrollProxy: proxy, placement: scrubberPlacement)
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
                        onDelete: { vm.askDeleteSelected() },
                        onClear: vm.clearSelection,
                        onGroup: {
                            Task { await vm.loadGroups() }
                            vm.showGroupPicker = true
                        },
                        onRemoveFromGroup: vm.activeGroup == nil ? nil : {
                            Task { await vm.removeSelectionFromGroup() }
                        },
                        // Among one person's photos: face recognition got
                        // them wrong - off that person, or to someone else.
                        onNotThisPerson: vm.personInFilter == nil ? nil : { vm.askNotThisPerson(from: .selection) },
                        onMoveToPerson: vm.personInFilter == nil ? nil : { vm.askMoveToPerson(from: .selection) },
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
                        Text(S.appPhotosCollectionsSheetEmpty)
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
                                    Text(S.appPhotosCollectionsPictures(count: Int(g.fileCount)))
                                        .font(.caption).foregroundStyle(.secondary)
                                }
                            }
                        }
                        // Issue #180: long-press or swipe a group to share it.
                        .contextMenu {
                            Button {
                                groupsSheetGallerySource = .group(g.id)
                            } label: {
                                Label(S.appShellShareAsGalleryMenu, systemImage: "photo.on.rectangle.angled")
                            }
                        }
                        .swipeActions(edge: .trailing) {
                            Button {
                                groupsSheetGallerySource = .group(g.id)
                            } label: {
                                Label(S.commonShare, systemImage: "square.and.arrow.up")
                            }
                            .tint(.accentColor)
                        }
                    }
                }
                .navigationTitle(S.appShellSectionCollections)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .navigationBarTrailing) {
                        Button(S.commonDone) { vm.showGroups = false }
                    }
                }
            }
            .sharedGalleryShareFlow(source: $groupsSheetGallerySource)
        }
        // The selection bar's "Add to collection": pick an existing group or start a new one.
        .confirmationDialog(S.appPhotosCollectionsAddTitle(count: vm.selected.count), isPresented: $vm.showGroupPicker, titleVisibility: .visible) {
            ForEach(vm.groups, id: \.id) { g in
                Button(g.name) { Task { await vm.addSelectionToGroup(g) } }
            }
            Button(S.appPhotosCollectionsNewItem) { vm.showNewGroupName = true }
            Button(S.commonCancel, role: .cancel) {}
        }
        .alert(S.appPhotosCollectionsNewTitle, isPresented: $vm.showNewGroupName) {
            TextField(S.appPhotosCollectionsNamePlaceholder, text: $vm.newGroupName)
            Button(S.commonCreate) { Task { await vm.createGroupFromSelection() } }
            Button(S.commonCancel, role: .cancel) { vm.newGroupName = "" }
        } message: {
            Text(S.appPhotosCollectionsNewMessage(count: vm.selected.count))
        }
        .alert(S.appPhotosCollectionsRename, isPresented: $vm.showRenameGroup) {
            TextField(S.appPhotosCollectionsNamePlaceholder, text: $vm.renameGroupName)
            Button(S.commonSave) { Task { await vm.renameActiveGroup() } }
            Button(S.commonCancel, role: .cancel) {}
        }
        .confirmationDialog(
            S.appPhotosCollectionsDeleteTitle(name: vm.activeGroup?.name ?? ""),
            isPresented: $vm.confirmDeleteGroup, titleVisibility: .visible
        ) {
            Button(S.appPhotosCollectionsDelete, role: .destructive) { Task { await vm.deleteActiveGroup() } }
            Button(S.commonCancel, role: .cancel) {}
        } message: {
            Text(S.appPhotosCollectionsDeleteMessage)
        }
        .sharedGalleryShareFlow(source: $gallerySource)
        .onAppear { vm.onAppearInitial() }
        .confirmationDialog(
            S.appPhotosSelectionDeleteTitle(count: vm.selected.count),
            isPresented: $vm.confirmDeleteSelected,
            titleVisibility: .visible
        ) {
            // Issue #201: picked photos held at several paths - only the
            // copies shown, or every copy.
            if let only = vm.deleteAsk.onlyShown, let every = vm.deleteAsk.everyCopy {
                Button(only, role: .destructive) { vm.deleteSelected() }
                Button(every, role: .destructive) { vm.deleteSelected(everyCopy: true) }
            } else {
                Button(S.commonDelete, role: .destructive) { vm.deleteSelected() }
            }
            Button(S.commonCancel, role: .cancel) {}
        } message: {
            if let text = vm.deleteAsk.text { Text(text) }
        }
        // Not while the viewer is open: it shows them itself (alertShown).
        .alert(vm.notice?.text ?? "", isPresented: vm.alertBinding(overViewer: false)) { Button(S.commonOk, role: .cancel) {} }
        .deviceNeedsUpdate(vm.needsUpdateBinding(overViewer: false))
        // The selection bar's Not this person and Someone else… (the
        // viewer's are its own: PersonMove.swift).
        .modifier(PersonActionsPresenter(vm: vm, from: .selection))
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
                    // Issue #201: the original; the picture shown only if
                    // Photos refuses its format.
                    let path = vm.openPath
                    let fallback = vm.openIndex.flatMap { idxFromThumb($0) }
                    vm.saveOriginalToPhotos {
                        guard let path else { return fallback }
                        return await vm.fullImage(of: path) ?? fallback
                    }
                },
                share: {
                    let fallback = vm.openIndex.flatMap { idxFromThumb($0) }
                    Task { vm.shareCurrentPhoto(await vm.fullImageForOpen() ?? fallback) }
                },
                delete: { vm.deleteCurrentPhoto(everyCopy: $0) }
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
              let item = vm.gridLayout(before).topItem(ofRows: scrollMemo.rows), item > 0 else { return }
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

    /// Issue #200: the long press and drag over the grid's photos - not
    /// over grey tiles, and the selection bar laid over the grid's bottom
    /// is where it starts to scroll down. A press starts only on a photo
    /// itself (not a title, a gap or the empty slots after a short month);
    /// the finger beside photos in a row is then over the nearest.
    private func dragSelect(_ layout: PhotoGridLayout) -> DragSelectGesture {
        let frames = dragFrames, vm = vm
        let pressed = { (p: CGPoint) -> Int? in frames.item(containing: p).flatMap { vm.items.indices.contains($0) ? $0 : nil } }
        return DragSelectGesture(
            space: Self.gridSpace,
            enabled: layout.placeholder == .none,
            bottomBand: Self.selectionBarBand,
            canStart: { pressed($0) != nil },
            began: { if let i = pressed($0) { vm.beginDragSelect(at: i) } },
            moved: { vm.dragSelect(to: frames.item(at: $0)) },
            ended: { vm.endDragSelect() }
        )
    }

    /// Issue #194: the rows on the screen are near the grid's top (their
    /// first photo among the first 12, or the top month's grey tiles):
    /// the newer photos above them are asked for - as the user scrolls up,
    /// and again once a page put above has settled while they stay there.
    private func newerIfNearTop() {
        guard !vm.upEnd, let top = vm.gridLayout(metrics).topItem(ofRows: scrollMemo.rows), top < 12 else { return }
        vm.loadNewerIfNeeded(at: top)
    }

    /// How much of the grid's bottom the selection bar covers.
    private static let selectionBarBand: CGFloat = 104

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
                thumbsVersion: vm.thumbsVersion,
                onTap: { openPath(it.path) },
                onToggle: { vm.toggleSelect(it.path) },
                needThumb: { vm.needThumb(it) }
            )
            .dragSelectItem(i, in: dragFrames, space: Self.gridSpace)
            // The tour's steps about selecting and opening a photo.
            .tourTarget(i == 0 ? .firstPhoto : nil)
            .task { await vm.loadMoreIfNeeded(index: i, id: it.id) }
            // Its thumbnail asked for first while it shows (GridThumbLoader).
            .onAppear { vm.tileShown(i) }
            .onDisappear { vm.tileHidden(it) }
            // Named by its photo: where another photo comes to this place
            // (a delete before it), the tile is a new one, whose tasks run.
            .id(it.id)
        } else {
            // The first page on its way, the scrubber's month, or (issue
            // #194) the top month's newer photos on their way above a
            // jump. Grey tiles say nothing to VoiceOver but, once, that
            // photos are on their way (the web's aria-busy "Loading
            // photos", Android's PHOTOS_LOADING).
            let grey = RoundedRectangle(cornerRadius: PhotoTile.corner)
                .fill(Color(.secondarySystemFill))
                .frame(width: side, height: side)
            Group {
                if i == layout.firstSlot {
                    grey
                        .accessibilityElement()
                        .accessibilityLabel(LoadProblem.photosLoading)
                        .accessibilityAddTraits(.updatesFrequently)
                } else {
                    grey.accessibilityHidden(true)
                }
            }
            // The top month's grey tiles on screen ask for its photos.
            .task {
                if layout.placeholder == .none { vm.loadNewerIfNeeded(at: i) }
            }
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
                Button(S.commonCancel) { actions.cancel() }
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
                    .accessibilityLabel(S.appShellSectionPeople)
                    .tourTarget(.peopleButton)
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
                .accessibilityLabel(S.appShellSectionCollections)
                .tourTarget(.collectionsButton)
            }
        }
        .padding(.horizontal, 8)
    }

    /// "12 items · Mar – Oct 2024", as the web's collection header: the
    /// months from the date buckets once they are this collection's, and
    /// only the years below 900 wide, as there.
    private func collectionLine(_ g: Msg_ImageGroup) -> String {
        let span = PhotoMonths.span(vm.freshBuckets.map(\.month), years: (windowWidth > 0 ? windowWidth : 390) < 900)
        return span.isEmpty
            ? PhotoMonths.items(Int(g.fileCount))
            : S.appPhotosGridCollectionItemsSpan(count: Int(g.fileCount), span: span)
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
            // The dot only sets the line apart from the name: no words.
            Text(verbatim: "· \(collectionLine(g))").foregroundStyle(.secondary).font(.caption).lineLimit(1)
                // A little smaller rather than cut ("120 elementos · feb. –
                // oct. 20…" on an iPhone SE); English fits as it was.
                .minimumScaleFactor(0.75)
            // Issue #180: the group as a gallery behind a link.
            Button {
                gallerySource = .group(g.id)
            } label: { Image(systemName: "square.and.arrow.up").font(.caption) }
            .accessibilityLabel(S.appShellShareAsGalleryMenu)
            Button {
                vm.confirmDeleteGroup = true
            } label: { Image(systemName: "trash").font(.caption) }
            .accessibilityLabel(S.appPhotosCollectionsDelete)
            .foregroundStyle(.red)
            Button("×") { vm.leaveGroup() }
                .accessibilityLabel(S.appShellSearchCloseCollection(name: g.name))
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
                vm.chips.isEmpty && vm.selectedPeople.isEmpty ? S.appShellSearchField : S.commonSearch,
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
            .accessibilityLabel(S.appShellSearchField)
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
                .accessibilityLabel(S.appShellSearchClear)
            }
        }
        .padding(.leading, 12)
        .padding(.trailing, 4)
        .frame(height: 38)
        .background(Color(.tertiarySystemFill), in: Capsule())
        .contentShape(Capsule())
        .onTapGesture { searchFocused = true }
        .tourTarget(.search)
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
                        Text(person == nil ? S.appShellSearchPerson : (name.isEmpty ? S.appShellSearchUnnamed : name))
                            .font(.subheadline)
                            .lineLimit(1)
                        chipRemove(name.isEmpty ? S.appShellSearchRemovePerson : S.appShellSearchRemove(name: name)) { vm.togglePerson(pid) }
                    }
                    .padding(.leading, 3)
                    .padding(.trailing, 4)
                    .padding(.vertical, 3)
                    .background(Color.accentColor.opacity(0.15), in: Capsule())
                }
                ForEach(vm.chips, id: \.self) { chip in
                    HStack(spacing: 4) {
                        Text(chip).font(.subheadline).lineLimit(1)
                        chipRemove(S.appShellSearchRemove(name: chip)) { vm.removeChip(chip) }
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

    /// What the grid's place says while it has no photos (Android's
    /// galleryBody): why the first page didn't come - it is asked for
    /// again meanwhile - or that there are none.
    @ViewBuilder
    private var gridMessage: some View {
        switch vm.gridBody {
        case .problem:
            PhotosProblemView(problem: vm.pageProblem ?? .failed, retrying: vm.retrying, retry: vm.retryPage)
        case .empty:
            emptyState
        default:
            EmptyView()
        }
    }

    /// The end of the grid: where the next page shows it is coming, or that
    /// it failed, with its Try again (it is asked again meanwhile).
    @ViewBuilder
    private var gridFooter: some View {
        if vm.loading {
            ProgressView().frame(height: 60)
        } else if vm.pageProblem != nil {
            MorePhotosProblem(retry: vm.retryPage)
        }
    }

    /// Issue #194: the top of the grid after a jump - where the newer
    /// photos show they are coming, or that they failed, with its Try
    /// again (they are asked again meanwhile): a later page's treatment, on
    /// a bar of its own.
    @ViewBuilder
    private var newerStatus: some View {
        if vm.upProblem != nil {
            MorePhotosProblem(retry: vm.retryNewer)
                .frame(minHeight: 44)
                .fixedSize(horizontal: false, vertical: true)
                .background(.regularMaterial, in: Capsule())
                .padding(.horizontal, 16)
        } else if vm.upLoading {
            ProgressView()
                .padding(10)
                .background(.regularMaterial, in: Circle())
        }
    }

    /// The first page is slow to come (a slow link, a busy device): over
    /// the grey tiles - the first page's, or a jump's on its way - that it
    /// is still on its way.
    private var showsStillWaiting: Bool {
        vm.slowFirstPage && (vm.gridBody == .skeleton || (vm.gridBody == .grey && vm.scrubFrac == nil))
    }

    /// Nothing to show (the web's and Android's empty states): an empty
    /// collection, a search with no match, or no photos at all.
    @ViewBuilder
    private var emptyState: some View {
        switch vm.emptyKind {
        case .collection:
            PhotosStateView(
                art: AnyView(NavIconView(.collections, size: 40, stroke: 1.2)),
                title: S.appPhotosGridEmptyCollectionTitle,
                text: S.appPhotosGridEmptyCollectionText,
                button: S.appShellCollectionsGoToImages
            ) { vm.leaveGroup() }
        case .noMatch:
            PhotosStateView(
                art: AnyView(NavIconView(.search, size: 40, stroke: 1.2)),
                title: S.appPhotosGridNoMatchTitle,
                text: vm.activeGroup.map { S.appPhotosGridNoMatchIn(name: $0.name) } ?? S.appPhotosGridNoMatchText,
                button: S.appShellSearchClear
            ) { vm.clearSearch() }
        case .noPhotos:
            PhotosStateView(
                art: AnyView(NavIconView(.images, size: 40, stroke: 1.2)),
                title: S.appPhotosGridNoPhotosTitle,
                text: S.appPhotosGridNoPhotosText
            )
        }
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
        // The big thumbnail when the viewer asked for it, before the
        // grid's small tile.
        if let big = vm.bigThumbs[it.path] { return big }
        if let d = it.thumbData { return UIImage(data: d) }
        // The grid's decoded tile: the small thumbnail at its own size
        // (tiles are decoded no smaller than 432 px on the short side).
        return GridThumbCache.cached(id: it.id, maxPt: PhotoGridMetrics.decodeSide) ?? it.thumbImage
    }
}

// MARK: - UI pieces (iOS)

/// What the grid's place shows instead of photos: a picture, a title, a
/// line, maybe a button (Android's GridMessage).
private struct PhotosStateView: View {
    let art: AnyView
    let title: String
    let text: String
    var action: AnyView? = nil

    var body: some View {
        VStack(spacing: 0) {
            art.foregroundStyle(.secondary)
                .accessibilityHidden(true)
            Text(title)
                .font(.headline)
                .multilineTextAlignment(.center)
                .accessibilityAddTraits(.isHeader)
                .padding(.top, 16)
            Text(text)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 420)
                .padding(.top, 8)
            if let action {
                action.padding(.top, 20)
            }
        }
        .padding(.horizontal, 32)
        .padding(.vertical, 56)
        .frame(maxWidth: .infinity)
    }
}

extension PhotosStateView {
    /// With a plain button, `button`, doing `run`.
    init(art: AnyView, title: String, text: String, button: String, run: @escaping () -> Void) {
        self.init(art: art, title: title, text: text, action: AnyView(Button(button, action: run).buttonStyle(.bordered)))
    }
}

/// The first page failed (`problem`: why) and is asked for again
/// meanwhile: "Couldn't load your photos", why in plain words, and Try
/// again - "Trying…" while a retry is on its way. In the grid's place,
/// under any header (Android's PhotosProblem).
struct PhotosProblemView: View {
    let problem: LoadProblem
    let retrying: Bool
    let retry: () -> Void

    var body: some View {
        PhotosStateView(
            art: AnyView(Image(systemName: problem == .offline ? "wifi.slash" : "icloud.slash")
                .font(.system(size: 36, weight: .light))
                .frame(height: 40)),
            title: LoadProblem.photosTitle,
            text: problem.photosText,
            action: AnyView(
                Button(action: retry) {
                    HStack(spacing: 8) {
                        if retrying { ProgressView().controlSize(.small) }
                        Text(retrying ? LoadProblem.trying : LoadProblem.tryAgain)
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(retrying)
            )
        )
        // Said to VoiceOver when it appears and when its reason changes
        // (Android's polite live region, the web's role="status").
        .onAppear { Announce.polite("\(LoadProblem.photosTitle). \(problem.photosText)") }
        .onChange(of: problem) { _, p in Announce.polite(p.photosText) }
    }
}

/// A later page failed (the web's foot): it is asked for again meanwhile,
/// or now with Try again.
private struct MorePhotosProblem: View {
    let retry: () -> Void

    var body: some View {
        HStack(spacing: 4) {
            Text(LoadProblem.morePhotos)
                .font(.subheadline)
                .foregroundStyle(.secondary)
            Button(LoadProblem.tryAgain, action: retry)
                .font(.subheadline.weight(.semibold))
        }
        .padding(.horizontal, 16)
        .frame(maxWidth: .infinity)
        .frame(minHeight: 60)
        .onAppear { Announce.polite(LoadProblem.morePhotos) }
    }
}

/// The first page is slow to come: a pill over the grey tiles (Android's
/// StillWaiting).
private struct StillWaitingPill: View {
    var body: some View {
        HStack(spacing: 10) {
            ProgressView().controlSize(.small)
            Text(LoadProblem.photosSlow)
                .font(.subheadline)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .background(.regularMaterial, in: Capsule())
        .shadow(color: .black.opacity(0.18), radius: 6, y: 2)
        .accessibilityElement(children: .combine)
        .onAppear { Announce.polite(LoadProblem.photosSlow) }
    }
}

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
                    Text(filter.total.map { S.appPhotosGridPersonItems(count: $0) } ?? " ")
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
        return n.isEmpty ? S.appShellSearchUnnamed : n
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

/// Issue #194: where the grid's rows on the screen are (their frames in the
/// scroll view, as they are drawn), so that photos put above them leave the
/// one at the top where it was: noted right before (take), and scrolled
/// back to by its name right after (PhotoGalleryView). A plain object, so
/// scrolling draws nothing again.
final class GridAnchor {
    /// The scroll view's height - what scrollTo lines rows up in, the part
    /// under the tab bar included - and its insets: a row only under them
    /// isn't on the screen.
    struct Viewport: Equatable {
        var height: CGFloat = 0
        var top: CGFloat = 0
        var bottom: CGFloat = 0
    }
    private var rows: [String: CGRect] = [:]
    var viewport = Viewport()
    private var kept: (id: String, unitPoint: UnitPoint)?

    func record(_ id: String, _ frame: CGRect) { rows[id] = frame }
    func gone(_ id: String) { rows[id] = nil }

    /// Notes the row at the top of the screen and how far it is from the
    /// screen's top: where it goes back to. The grid's first row (`first`)
    /// gets `grows` more room above it once rows are put above it (a line's
    /// gap, not the grid's top): it goes back that much higher, so its
    /// photos stay where they were.
    func take(first: String?, grows: CGFloat) {
        let v = viewport
        let shown = rows.filter { $0.value.maxY > v.top + 0.5 && $0.value.minY < v.height - v.bottom }
        guard let top = shown.min(by: { $0.value.minY < $1.value.minY }) else {
            kept = nil
            return
        }
        let extra = top.key == first ? grows : 0
        kept = (top.key, Self.unitPoint(y: top.value.minY - extra, rowHeight: top.value.height + extra, height: v.height))
    }

    /// The row noted, and where it goes (once).
    func taken() -> (id: String, unitPoint: UnitPoint)? {
        defer { kept = nil }
        return kept
    }

    /// The anchor that puts a row `rowHeight` tall at `y` from the top of a
    /// scroll view `height` tall: scrollTo lines up the row's point at this
    /// fraction of its height with the same fraction of the view's.
    static func unitPoint(y: CGFloat, rowHeight: CGFloat, height: CGFloat) -> UnitPoint {
        let room = height - rowHeight
        guard abs(room) > 1 else { return .top }
        return UnitPoint(x: 0, y: y / room)
    }
}

/// The rows on the screen, as the grid scrolls: where the grid goes back
/// to when its width changes (their first photo, PhotoGridLayout.topItem).
/// A plain object, so that scrolling changes no state and draws nothing
/// again.
final class GridScrollMemo {
    private(set) var rows: [String] = []
    /// While the grid is being brought back: the rows seen meanwhile are
    /// the new width's at the old offset, not where the user was.
    var restoring = false

    func visible(_ ids: [String]) {
        guard !restoring else { return }
        rows = ids
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
    /// The grid's thumbnails changed (PhotoGalleryVM.thumbsVersion): the
    /// image is looked up again.
    let thumbsVersion: Int
    let onTap: () -> Void
    let onToggle: () -> Void
    /// It has no image to show: from the thumbnail cache, else the device.
    let needThumb: () -> Void

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
        // The long press that selects, and the drag on from it, are the
        // grid's (issue #200, PhotoGalleryView.dragSelect); the finger
        // lifting at the end of one is no tap.
        .onTapGesture {
            guard DragSelect.tapAllowed else { return }
            if hasSelection { onToggle() } else { onTap() }
        }
        // VoiceOver: one element per tile, its own square - not the
        // photo's uncropped frame reaching over the month's title - read
        // as the web's tiles are ("Photo, 5 October 2026").
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(PhotoMonths.tileLabel(video: item.mime.hasPrefix("video/"), date: item.created))
        .accessibilityAddTraits(isSelected ? [.isButton, .isSelected] : .isButton)
        .accessibilityAction {
            if hasSelection { onToggle() } else { onTap() }
        }
        .accessibilityAction(named: isSelected ? S.commonDeselect : S.commonSelect) { onToggle() }
    }

    private var thumb: some View {
        // Decoded once at tile size and cached, not on every pass of this
        // body. A local file is read from disk, as before.
        let img = GridThumbCache.image(id: item.id, data: item.thumbData,
                                       localURL: item.localURL, maxPt: Self.decodeSide)
        return Group {
            if let img {
                Image(uiImage: img).resizable().scaledToFill()
            } else {
                Color.gray.opacity(0.2)
            }
        }
        // None (yet, or no longer - the decoded tiles' memory let it go):
        // asked for, from the cache on this phone or the device.
        .task(id: img == nil) { if img == nil { needThumb() } }
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
    /// Delete the open item: true for every copy of it (issue #201).
    let delete: (Bool) -> Void

    @State private var confirmDelete = false
    /// The item Delete asks about is a video: the question says so (the
    /// web's delete_video_title). Taken when Delete is tapped, so the words
    /// don't change under the question.
    @State private var confirmDeleteVideo = false
    /// Issue #201: the item Delete asks about is held at this many paths
    /// (more than one): only this copy, or all of them. Taken with it.
    @State private var confirmCopies: Int?
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
            GeometryReader { screen in
                // In landscape the controls move to the sides, into the
                // black the photo leaves there anyway, so the photo gets
                // the whole height instead of what a top and a bottom bar
                // left it (the owner's rule: as large as it can be without
                // covering a button). Portrait is unchanged. The strip
                // stays at the same place in the tree either way, so a
                // rotation re-lays it out without starting it (or the
                // video in it) again.
                let landscape = Self.controlsBeside(screen.size)
                VStack(spacing: 0) {
                    if !landscape {
                        HStack {
                            infoButton(landscape: false)
                            Spacer()
                            closeButton(landscape: false)
                        }
                        .padding()
                    }

                    HStack(spacing: 0) {
                        if landscape {
                            // Leading column: info at the top.
                            infoButton(landscape: true)
                                .frame(maxHeight: .infinity, alignment: .top)
                                .frame(width: Self.sideColumnWidth)
                                .padding(.top, 8)
                        }
                        strip
                        if landscape {
                            // Trailing column: close at the top, share,
                            // save and delete in the middle, under the
                            // right thumb.
                            VStack(spacing: 20) { actionButtons(landscape: true) }
                                .frame(maxHeight: .infinity)
                                .overlay(alignment: .top) { closeButton(landscape: true).padding(.top, 8) }
                                .frame(width: Self.sideColumnWidth)
                        }
                    }

                    if !landscape {
                        HStack(spacing: 48) { actionButtons(landscape: false) }
                            .font(.title2)
                            .foregroundStyle(.white)
                            .padding(.vertical, 12)
                    }
                }
            }
        }
        .confirmationDialog(Self.deleteTitle(video: confirmDeleteVideo), isPresented: $confirmDelete, titleVisibility: .visible) {
            let ask = confirmCopies.map { CopiesAsk.one(count: $0) } ?? .plain
            if let only = ask.onlyShown, let all = ask.everyCopy {
                Button(only, role: .destructive) { delete(false) }
                Button(all, role: .destructive) { delete(true) }
            } else {
                Button(Self.deleteButton(video: confirmDeleteVideo), role: .destructive) { delete(false) }
            }
            Button(S.commonCancel, role: .cancel) {}
        } message: {
            if let n = confirmCopies, let text = CopiesAsk.one(count: n).text { Text(text) }
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
        // Its menu's Not this person and Someone else… (PersonMove.swift).
        .modifier(PersonActionsPresenter(vm: vm, from: .viewer))
        // What happened while it is open (Save, Delete, its menu), over it:
        // presented from the screen under it, an alert would close it.
        .alert(vm.notice?.text ?? "", isPresented: vm.alertBinding(overViewer: true)) { Button(S.commonOk, role: .cancel) {} }
        .deviceNeedsUpdate(vm.needsUpdateBinding(overViewer: true))
    }

    /// The delete question and its button: "Delete this video?" and
    /// "Delete Video" for a video (Android's ImageModal says the same).
    static func deleteTitle(video: Bool) -> String {
        video ? S.appPhotosViewerDeleteVideoTitle : S.appPhotosViewerDeleteTitle
    }
    static func deleteButton(video: Bool) -> String {
        video ? S.appPhotosViewerDeleteVideo : S.appPhotosViewerDelete
    }

    /// How wide each side column is in landscape: a 44 pt button with a
    /// little room either side.
    private static let sideColumnWidth: CGFloat = 56
    /// Roughly what the top bar (~66 pt) and the bottom one (~54 pt) take
    /// from the photo's height in portrait.
    private static let barsHeight: CGFloat = 120

    /// Whether the controls go in columns beside the photo rather than in
    /// bars above and below it: only in landscape, and only when that
    /// leaves a typical (4:3) landscape photo more room - always on an
    /// iPhone, not necessarily on a nearly square screen like an iPad's
    /// (Android's controlsBeside).
    static func controlsBeside(_ size: CGSize) -> Bool {
        guard size.width > size.height else { return false }
        func area(_ w: CGFloat, _ h: CGFloat) -> CGFloat {
            let unit = max(0, min(w / 4, h / 3))
            return unit * unit * 12
        }
        return area(size.width - 2 * sideColumnWidth, size.height) > area(size.width, size.height - barsHeight)
    }

    /// Landscape gives every button a full 44 pt target, as it stands
    /// alone in a column; portrait keeps the bars as they always were.
    private func infoButton(landscape: Bool) -> some View {
        Button { vm.openInfo() } label: {
            Image(systemName: "info.circle").font(.title2).foregroundStyle(.white)
                .frame(width: landscape ? 44 : nil, height: landscape ? 44 : nil)
                .contentShape(Rectangle())
        }
        .accessibilityLabel(S.appPhotosViewerMoreInfo)
    }

    private func closeButton(landscape: Bool) -> some View {
        Button { vm.closeModal() } label: {
            Image(systemName: "xmark.circle.fill").font(.title).foregroundStyle(.white)
                .frame(width: landscape ? 44 : nil, height: landscape ? 44 : nil)
                .contentShape(Rectangle())
        }
        .accessibilityLabel(S.commonClose)
    }

    /// Share, save and delete: a row under the photo in portrait, a
    /// column beside it in landscape.
    @ViewBuilder
    private func actionButtons(landscape: Bool) -> some View {
        let side: CGFloat? = landscape ? 44 : nil
        Group {
            Button { share() } label: {
                Image(systemName: "square.and.arrow.up").frame(width: side, height: side).contentShape(Rectangle())
            }
            .accessibilityLabel(S.commonShare)
            // Issue #201: a spinner while the original downloads and is
            // saved.
            let saving = vm.savingPath != nil && vm.savingPath == vm.openPath
            Button { save() } label: {
                Group {
                    if saving {
                        ProgressView().tint(.white)
                    } else {
                        Image(systemName: "arrow.down.circle")
                    }
                }
                .frame(width: side, height: side).contentShape(Rectangle())
            }
            .disabled(vm.savingPath != nil)
            .accessibilityLabel(saving ? S.appPhotosViewerSaving : S.commonSave)
            Button(role: .destructive) {
                confirmDeleteVideo = vm.openIndex.map { vm.items.indices.contains($0) && vm.items[$0].mime.hasPrefix("video/") } ?? false
                confirmCopies = vm.openCopies
                confirmDelete = true
            } label: {
                Image(systemName: "trash").frame(width: side, height: side).contentShape(Rectangle())
            }
            .accessibilityLabel(S.commonDelete)
            // Among one person's photos: face recognition filed this one
            // under the wrong person - off them, or to someone else.
            if vm.personInFilter != nil {
                personMenu(side: side)
            }
        }
        .font(.title2)
        .foregroundStyle(.white)
    }

    /// Not this person and Someone else… for the open photo, in a menu
    /// named by the person it is about; a spinner while the device answers.
    private func personMenu(side: CGFloat?) -> some View {
        let busy = vm.preparing == .notThisPerson || vm.preparing == .moveToPerson
        return Menu {
            Button {
                vm.askNotThisPerson(from: .viewer)
            } label: {
                Label { Text(S.appShellNotThisPerson) } icon: { NavIcon.notThisPerson.image() }
            }
            Button {
                vm.askMoveToPerson(from: .viewer)
            } label: {
                Label { Text(S.appShellMoveToPerson) } icon: { NavIcon.moveToPerson.image() }
            }
        } label: {
            Group {
                if busy {
                    ProgressView().tint(.white)
                } else {
                    Image(systemName: "ellipsis.circle")
                }
            }
            .frame(width: side, height: side)
            .contentShape(Rectangle())
        }
        // As the selection bar has them, whichever way the menu opens.
        .menuOrder(.fixed)
        .disabled(vm.preparing != nil)
        .accessibilityLabel(PersonMove.menuLabel(name: vm.personInFilterName))
    }

    /// The previous, open and next photos side by side, slid by a drag.
    private var strip: some View {
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
                } else if let img = vm.hiResImages[item.path] ?? vm.bigThumbs[item.path] ?? thumb(item) {
                    // Until the full size comes, the grid's small tile;
                    // where the thumbnail stays (the full size failed or
                    // can't be decoded, a video that can't be fetched or
                    // played), the big one, asked for then
                    // (PhotoGalleryVM.thumbnailStays).
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
                } else if !unplayable(i) {
                    ProgressView().tint(.white)
                }
            }
        }
        .frame(width: size.width, height: size.height)
        .overlay(alignment: .bottom) {
            if unplayable(i) {
                VideoUnplayable { vm.retryVideo() }
            }
        }
    }

    /// The open video couldn't be fetched or played: its poster stays,
    /// saying so, with Try again.
    private func unplayable(_ i: Int) -> Bool {
        i == vm.openIndex && vm.items.indices.contains(i) && vm.unplayable.contains(vm.items[i].path)
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
        // The tile's decoded image (its bytes are in the thumbnail cache,
        // not the item: PhotoGalleryVM.open has it decoded again if it was
        // let go).
        return GridThumbCache.cached(id: item.id, maxPt: PhotoGridMetrics.decodeSide) ?? item.thumbImage
    }
}

/// Over a video's poster: the player couldn't play it (or it couldn't be
/// fetched), and Try again (Android's VideoUnplayable).
private struct VideoUnplayable: View {
    let retry: () -> Void

    var body: some View {
        HStack(spacing: 4) {
            Text(LoadProblem.videoUnplayable)
                .font(.subheadline)
            Button(LoadProblem.tryAgain, action: retry)
                .font(.subheadline.weight(.semibold))
        }
        .foregroundStyle(.white)
        .tint(.white)
        .padding(.leading, 16)
        .padding(.trailing, 8)
        .padding(.vertical, 8)
        .background(.black.opacity(0.7), in: Capsule())
        .padding(16)
        .onAppear { Announce.polite(LoadProblem.videoUnplayable) }
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
            Text(S.appPhotosViewerLowRes).font(.system(size: 11, weight: .semibold))
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
                            row(S.appPhotosViewerInfoCamera, [info.cameraMake, info.cameraModel].filter { !$0.isEmpty }.joined(separator: " "))
                        }
                        if info.hasTakenAt {
                            row(S.appPhotosViewerInfoTaken, info.takenAt.date.formatted(Date.FormatStyle(date: .abbreviated, time: .shortened, locale: L10n.shared.locale)))
                        }
                        if info.width > 0 && info.height > 0 {
                            row(S.appPhotosViewerInfoDimensions, "\(info.width) × \(info.height)")
                        }
                        if !info.exposureTime.isEmpty { row(S.appPhotosViewerInfoExposure, info.exposureTime) }
                        if !info.fNumber.isEmpty { row(S.appPhotosViewerInfoAperture, info.fNumber) }
                        if info.iso > 0 { row(S.appPhotosViewerInfoIso, "\(info.iso)") }
                        if !info.focalLength.isEmpty { row(S.appPhotosViewerInfoFocalLength, info.focalLength) }
                        if !info.city.isEmpty || !info.country.isEmpty {
                            row(S.appPhotosViewerInfoLocation, [info.city, info.country].filter { !$0.isEmpty }.joined(separator: ", "))
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
                            Text(S.appPhotosViewerInfoNoExif).foregroundColor(.secondary)
                        }
                    }
                } else {
                    Text(S.appPhotosViewerInfoNone).foregroundColor(.secondary)
                }
            }
            .navigationTitle(S.appPhotosViewerInfoTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button(S.commonDone) { dismiss() }
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

/// Issue #198: where the date scrubber sits on a phone turned on its side.
/// The grid stays inside the safe area, which on a phone with a notch or
/// the Dynamic Island leaves a band down both sides (44-50 pt on a notch
/// phone, 59 pt or more on an island one) - so the scrubber, on the grid's
/// edge, was that far in from the screen's, where a thumb coming in from
/// the edge doesn't land. It reaches across the band instead: its rail,
/// its handle and its grab strip at the screen's edge, as on a phone held
/// upright; its labels and month bubble stay over the grid (clear of the
/// island, when that is the side it is on). And there, at the screen's
/// edge, its ends keep clear of the rounded corners (and the home
/// indicator under the bottom one).
///
/// Except beside a notch. The island floats some 11 pt in from the edge,
/// so the handle (10 pt, at the very edge) passes outside it; a notch
/// runs from the very edge 30-36 pt inwards along some 60% of the track,
/// with no screen and no touch under it - the handle would vanish behind
/// it mid-track and a drag couldn't start there. So on the notch's side
/// (which way round the phone is turned) the scrubber reaches only to the
/// notch's inner edge; on the other side of the same phone, and on either
/// side of an island phone, all the way. Nothing changes where the grid
/// already reaches the edge: a phone held upright, an iPad.
struct ScrubberPlacement: Equatable {
    /// How far the grid stops short of the screen's trailing side (the
    /// safe area's band there), and how far its top is below the
    /// screen's top and its bottom above the screen's bottom.
    struct Gaps: Equatable {
        var trailing: CGFloat = 0
        var top: CGFloat = 0
        var bottom: CGFloat = 0
    }

    /// How far past the grid's trailing edge the scrubber reaches: the
    /// safe area's band there (beside a notch, the part of it clear of
    /// the notch), or nothing.
    var reach: CGFloat = 0
    /// How much shorter the track is at each end than the grid.
    var top: CGFloat = 0
    var bottom: CGFloat = 0

    /// How far from the screen's top and bottom the track ends when it is
    /// at the screen's side. The corners curve in from about 70 pt away on
    /// the roundest of these phones (the iPhone Air's, then the Pros',
    /// as the simulator's screen masks draw them); from 64 pt the handle
    /// (10 pt, at the very edge) loses at most half a point to the curve.
    static let cornerClearance: CGFloat = 64

    /// How far in from the screen's edge a notch reaches, at most, as the
    /// simulator's screen masks draw them: 30 pt on the iPhone X, XS and
    /// 11 Pro, 32 on the 12, 33 on the XR, 11 and 12 mini, 33.75 on the 13,
    /// 14, 16e and 17e, 36 on the 13 mini.
    static let notchDepth: CGFloat = 36

    /// A band narrower than this is a notch phone's: its notch's band is
    /// 44-50 pt (as is its top inset held upright), the Dynamic Island's
    /// 59 pt or more.
    static let notchBandLimit: CGFloat = 55

    init() {}

    /// For a grid `gaps` short of the screen's sides, with the phone's
    /// camera (notch or island) on the scrubber's side or not.
    init(_ gaps: Gaps, cameraAtTrailing: Bool) {
        // Half a point is rounding, not a band.
        guard gaps.trailing >= 1 else { return }
        if cameraAtTrailing && gaps.trailing < Self.notchBandLimit {
            // Beside a notch: up to its inner edge, no further. That far
            // in from the side the corners don't reach the track's ends.
            let clear = gaps.trailing - Self.notchDepth
            guard clear >= 1 else { return }
            reach = clear.rounded()
            return
        }
        reach = gaps.trailing.rounded()
        top = max(0, Self.cornerClearance - max(0, gaps.top)).rounded()
        bottom = max(0, Self.cornerClearance - max(0, gaps.bottom)).rounded()
    }

    /// Whether a phone turned to `orientation` has its camera on the
    /// scrubber's (trailing) side: turned to landscapeLeft its top - the
    /// camera - is at the right, to landscapeRight at the left; the
    /// trailing side is the right one, or the left one right to left.
    /// Held upright there is no band at the side. Not known yet: as if
    /// it were, so a notch is never reached under.
    static func cameraAtTrailing(_ orientation: UIInterfaceOrientation, rightToLeft: Bool) -> Bool {
        switch orientation {
        case .landscapeLeft: return !rightToLeft
        case .landscapeRight: return rightToLeft
        case .portrait, .portraitUpsideDown: return false
        default: return true
        }
    }
}

/// Issue #198: the window scene's interface orientation, at once and
/// whenever it changes - also when the phone turns from one side straight
/// over to the other, which changes no size or safe area SwiftUI could see
/// (the bands down both sides are alike), only which side the camera is on.
private struct InterfaceOrientationReader: UIViewRepresentable {
    let changed: (UIInterfaceOrientation) -> Void

    func makeUIView(context: Context) -> Probe { Probe(changed: changed) }
    func updateUIView(_ probe: Probe, context: Context) { probe.changed = changed }

    final class Probe: UIView {
        var changed: (UIInterfaceOrientation) -> Void
        private var watch: NSKeyValueObservation?
        private var last: UIInterfaceOrientation?

        init(changed: @escaping (UIInterfaceOrientation) -> Void) {
            self.changed = changed
            super.init(frame: .zero)
            isUserInteractionEnabled = false
        }

        required init?(coder: NSCoder) { nil }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            // The scene's geometry is key-value observable (iOS 16 on).
            watch = window?.windowScene?.observe(\.effectiveGeometry, options: [.initial, .new]) { [weak self] scene, _ in
                let orientation = scene.effectiveGeometry.interfaceOrientation
                // Out of the update that changed it, and out of SwiftUI's
                // own pass when it comes from the first layout.
                DispatchQueue.main.async { self?.report(orientation) }
            }
        }

        private func report(_ orientation: UIInterfaceOrientation) {
            guard orientation != last else { return }
            last = orientation
            changed(orientation)
        }
    }
}

private struct PhotoDateScrubber: View {
    @ObservedObject var vm: PhotoGalleryVM
    let scrollProxy: ScrollViewProxy
    /// Issue #198: across the safe area's band to the screen's side, and
    /// clear of its corners there (ScrubberPlacement).
    var placement = ScrubberPlacement()

    private static func label(for month: String) -> String { PhotoMonths.scrubLabel(month) }

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
        // The width (64 pt, plus the band it reaches across, issue #198)
        // has to constrain the GeometryReader itself, not
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
        // report that width on the inside (64 pt, plus the band it
        // reaches across, issue #198), and lets .overlay(alignment: .trailing)
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
                            // Over the grid, not out in the band.
                            .padding(.trailing, 16 + placement.reach)
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
                        // Over the grid: on the island's side the band
                        // has the island in it.
                        .padding(.trailing, 16 + placement.reach)
                        .offset(y: geo.size.height * frac - 12)
                    Circle()
                        .fill(Color.yellow)
                        .frame(width: 10, height: 10)
                        .offset(y: geo.size.height * frac - 5)
                }
            }
            .frame(width: geo.size.width, height: geo.size.height, alignment: .topTrailing)
            // Issue #113: the drag lives on a narrow strip at the very
            // edge rather than the whole column (64 pt, plus the band it
            // reaches across, issue #198). The column is that
            // wide so the year labels have room, but making all of it
            // grabbable meant a finger starting a scroll anywhere near
            // the right edge was taken as a scrub - and with
            // minimumDistance 0 (a tap has to jump) it was claimed the
            // instant you touched down, before the gesture's direction
            // was knowable.
            //
            // This does not undo the widening the .frame(width:) comment
            // below describes (64 pt, plus the band it reaches across,
            // issue #198): the year labels only render while
            // a drag is already in progress, so at rest there is nothing
            // out there to tap, and a drag once started keeps tracking
            // outside the strip anyway.
            .overlay(alignment: .trailing) {
                Color.clear
                    .frame(width: cScrubberTouchWidth)
                    .tourTarget(.scrubber)
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
        // Issue #198: 64 pt, plus the band it reaches across.
        .frame(width: 64 + placement.reach)
        // The track (and with it the fraction a drag maps to) ends clear
        // of the screen's corners when it is at the screen's side.
        .padding(.top, placement.top)
        .padding(.bottom, placement.bottom)
        // Laid out on the grid's edge as before, drawn and grabbed out to
        // the screen's: the band past the grid is the safe area's, which
        // nothing else uses there.
        .padding(.trailing, -placement.reach)
    }
}

/// A NotificationCenter observer, removed once nothing holds it.
private final class ObserverToken {
    private let token: NSObjectProtocol
    init(_ token: NSObjectProtocol) { self.token = token }
    deinit { NotificationCenter.default.removeObserver(token) }
}
