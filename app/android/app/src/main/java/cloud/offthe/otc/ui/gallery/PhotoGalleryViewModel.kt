// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import android.app.ActivityManager
import android.content.ContentValues
import android.content.Context
import android.graphics.Bitmap
import android.provider.MediaStore
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.data.FaceRecognition
import cloud.offthe.otc.data.PlacesSupport
import cloud.offthe.otc.data.ImagesChanged
import cloud.offthe.otc.net.ChunkedDownload
import cloud.offthe.otc.net.ErrorCodes
import cloud.offthe.otc.net.MediaStream
import cloud.offthe.otc.net.NetworkWatch
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.WSClient
import cloud.offthe.otc.net.Wake
import cloud.offthe.otc.net.byteSize
import cloud.offthe.otc.net.retryWaitMs
import cloud.offthe.otc.net.sleepOrWake
import cloud.offthe.otc.proto.AddToImageGroup
import cloud.offthe.otc.proto.CreateImageGroup
import cloud.offthe.otc.proto.DelFile
import cloud.offthe.otc.proto.DeleteImageGroup
import cloud.offthe.otc.proto.FileExifInfo
import cloud.offthe.otc.proto.GetFile
import cloud.offthe.otc.proto.GetFileInfo
import cloud.offthe.otc.proto.GetTags
import cloud.offthe.otc.proto.GetThumbnails
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ListImageGroups
import cloud.offthe.otc.proto.ListPeople
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.PlaceArea
import cloud.offthe.otc.proto.RemoveFromImageGroup
import cloud.offthe.otc.proto.RemoveFromPerson
import cloud.offthe.otc.proto.RenameImageGroup
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.ReqPhotoDateBuckets
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.proto.ShareFilesLink
import cloud.offthe.otc.ui.common.DeviceNeedsUpdate
import cloud.offthe.otc.ui.common.DragSelection
import cloud.offthe.otc.ui.common.deviceNeedsUpdate
import cloud.offthe.otc.ui.common.keysView
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.SelectionActionTask
import cloud.offthe.otc.ui.common.backOnline
import cloud.offthe.otc.ui.common.loadProblem
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.FIRST_PAGE_LIMIT_WITHOUT_THUMBS
import cloud.offthe.otc.ui.common.FullPhotoStore
import cloud.offthe.otc.ui.common.GridThumbFetcher
import cloud.offthe.otc.ui.common.GridThumbStore
import cloud.offthe.otc.ui.common.NEXT_PAGE_LIMIT_WITHOUT_THUMBS
import cloud.offthe.otc.ui.common.TILES_KIND
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.decodeBitmap
import cloud.offthe.otc.ui.common.land
import com.google.protobuf.Timestamp
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.time.ZoneId
import cloud.offthe.otc.proto.File as PbFile
import java.util.UUID
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appPhotosCollectionsRemoveNeedsUpdate
import cloud.offthe.otc.i18n.appPhotosNoticeAddFailed
import cloud.offthe.otc.i18n.appPhotosNoticeAdded
import cloud.offthe.otc.i18n.appPhotosNoticeCreateFailed
import cloud.offthe.otc.i18n.appPhotosNoticeCreated
import cloud.offthe.otc.i18n.appPhotosNoticeDeleteCollectionFailed
import cloud.offthe.otc.i18n.appPhotosNoticeDeleteChanged
import cloud.offthe.otc.i18n.appPhotosNoticeDeleteCopyUploadOnly
import cloud.offthe.otc.i18n.appPhotosNoticeDeleteFailed
import cloud.offthe.otc.i18n.appPhotosNoticeNoDownloadLink
import cloud.offthe.otc.i18n.appPhotosNoticeNoShareLink
import cloud.offthe.otc.i18n.appPhotosNoticeNotLoaded
import cloud.offthe.otc.i18n.appPhotosNoticeRemoveFailed
import cloud.offthe.otc.i18n.appPhotosNoticeRemoved
import cloud.offthe.otc.i18n.appPhotosNoticeRenameFailed
import cloud.offthe.otc.i18n.appPhotosNoticeSaved
import cloud.offthe.otc.i18n.appPhotosNoticeShareFailed
import cloud.offthe.otc.i18n.appPhotosPersonMoveFailed
import cloud.offthe.otc.i18n.appPhotosPersonMoved
import cloud.offthe.otc.i18n.appPhotosPersonMovedUnnamed
import cloud.offthe.otc.i18n.appPhotosPersonNeedsUpdate
import cloud.offthe.otc.i18n.appPhotosPersonRemoveFailed
import cloud.offthe.otc.i18n.appPhotosPersonRemoveTitle
import cloud.offthe.otc.i18n.appPhotosPersonRemoveTitleUnnamed
import cloud.offthe.otc.i18n.appPhotosPersonRemoved
import cloud.offthe.otc.i18n.appPhotosPersonRemovedUnnamed
import cloud.offthe.otc.i18n.appFilesUnexpectedResponse
import cloud.offthe.otc.R
import cloud.offthe.otc.ui.common.TextIOException
import cloud.offthe.otc.wallpaper.Wallpaper

// SearchPhotos.limit for the request that starts a photo search (no
// token) on a device not known to leave thumbnails out of pages (before
// release 113, which ignores omit_thumbnails and sends them): a small first
// page paints quickly over a slow upload. One known to leave them out gets
// FIRST_PAGE_LIMIT_WITHOUT_THUMBS (GridThumbs.kt), and pages that continue
// the token NEXT_PAGE_LIMIT_WITHOUT_THUMBS - a device before 113 answers at
// most its default, 30, whatever is asked; before release 97 it ignores it.
const val FIRST_PHOTO_PAGE_LIMIT = 12

// How old the tag and people lists may get before a search that ends
// fetches them again.
private const val LISTS_REFRESH_MS = 5 * 60_000L

// How long the first page may take before the grey tiles say it is still
// coming (People's and Collections' SLOW_MS): a first page from Pit over
// the bridge measured up to 70 s.
private const val SLOW_FIRST_PAGE_MS = 10_000L

/**
 * One request to the device. [kind]: what a screen waits on ("page",
 * "tags"...), asked through OTCConnection.ask - [baseMs] for its answer,
 * more after a timeout; null: anything else, with the socket's own time.
 * Tests answer in the device's place.
 */
typealias GalleryRequest = suspend (kind: String?, baseMs: Long, build: (ReqEnvelope.Builder) -> Unit) -> RespEnvelope

// How long a page that came back with nothing new, again and again (a
// device starting the search over each time), waits before the pages are
// asked for again: the usual backoff, but from 10 s up to a minute - each
// round is up to 12 pages.
private const val NO_PROGRESS_FIRST_MS = 10_000L
private const val NO_PROGRESS_MAX_MS = 60_000L

// The viewer's big thumbnails kept (by path), for the items whose
// thumbnail stays on screen: the open one and the last few swiped past.
private const val BIG_THUMBS_KEPT = 4

// How long the viewer stays on a photo shown at full size before its
// neighbours are fetched: a run of quick swipes is past it by then, so only
// the photo it stops on readies them (the web viewer's cRapidStepMs).
private const val NEIGHBOURS_AFTER_MS = 250L

// Port of PhotoGalleryVM (PhotoGallery.swift). Tag chips, the person
// filter (issue #52, AND semantics), image groups (issue #115, which the
// app calls collections), the date scrubber (issue #77), a search
// generation counter that discards stale replies, and the paging that
// keeps asking until a page adds something. Each photo carries its month
// (PhotoMonths.kt), worked out once as its page lands, for the grid's
// month titles.
//
// The same view model also drives the viewer opened from the Files section
// (showFiles): a separate instance holding just that folder's photos and
// videos, with no search or paging.
//
// A page that fails - the first or a later one - is asked again, 1 s
// doubling to 10 s (the web's usePageRetry), at once on a Wake (signed in
// again, a network came up, back in the foreground) or Try again, for as
// long as its search is the current one (searchGeneration). The grid says
// so meanwhile (pageProblem), never a blank page. The lists the search and
// headers use (tags, people, collections, date buckets) are asked again the
// same way until they come.
//
// Pages come without thumbnails (omit_thumbnails, release 113): the tiles
// come from the phone's thumbnail cache (ThumbStore), and what it lacks is
// fetched with GetThumbnails, the tiles on screen first (GridThumbFetcher,
// retried like the pages). A device before 113 sends them in the page: they
// are shown and kept. See GridThumbs.kt.
//
// Grids show small thumbnails (release 111, small_thumbnails): the viewer
// shows an item's small tile only until its full size arrives. Where the
// thumbnail stays on screen - the full size failed or can't be decoded, a
// video couldn't be fetched or played - it asks for the big one
// (GetThumbnails without the flag), again with the same backoff for as long
// as the viewer stays on that item (State.bigThumbs).
class PhotoGalleryViewModel(
    private val deviceId: String,
    private val send: GalleryRequest = { kind, baseMs, build ->
        if (kind == null) OTCConnection.request(build = build) else OTCConnection.ask(kind, baseMs, build)
    },
    // A failed load's wait before it is asked again, cut short by a Wake.
    private val pause: suspend (Long) -> Unit = { sleepOrWake(it) },
    // Whether the phone has a network (NetworkWatch.online).
    private val online: () -> Boolean = { NetworkWatch.online() },
    // Why a load failed, as the grid says it.
    private val problemOf: (RespEnvelope?, Throwable?) -> LoadProblem = { resp, error ->
        loadProblem(resp, error, online(), OTCConnection.statusCode.value)
    },
    // Where the tiles' thumbnails are kept (tests: a cache of their own).
    private val thumbs: GridThumbStore = ThumbStore,
    // Where the viewer's full-size photos are kept for the session (tests: a cache of their own).
    private val fullPhotos: FullPhotoStore = ThumbStore,
    // Where its work runs: the view model's own scope, a test's in tests.
    scope: CoroutineScope? = null,
    // How many sign-ins there have been (OTCConnection.signIns): what a
    // device was found unable to do holds until the next one.
    private val signIns: () -> Int = { OTCConnection.signIns.value },
) : ViewModel(), PeopleStore {
    private val scope: CoroutineScope = scope ?: viewModelScope

    // The tiles' missing thumbnails, fetched a batch at a time; each answer
    // updates State.thumbKeys.
    private val tiles by lazy {
        GridThumbFetcher(this.scope, thumbs, send = { b -> ask(TILES_KIND, OTCConnection.PAGE_TIMEOUT_MS) { it.setReqGetThumbnails(b) } }, pause = pause) { landed ->
            _state.update { st -> st.copy(thumbKeys = st.thumbKeys + landed.mapValues { it.value ?: "" }) }
        }
    }

    // thumbKey: the grid tile's thumbnail in ThumbStore (thumbTileKey) as
    // the page left it, null when there was none yet - State.thumbKeyOf has
    // what was fetched since. preview: an already decoded placeholder
    // (the Files grid's thumbnail), used when there are no thumb bytes.
    // month: "2024-03" in the phone's time zone, null without a date
    // (monthOf) - the Images grid's month titles. created: the photo's
    // date (epoch ms, null without one), for the tile's label (tileLabel).
    // hash: its content's, as the page listed it. copies (issue #201): how
    // many paths on the device hold it (File.copies), null when the device
    // didn't say - then deleting never offers "every copy". up (issue
    // #194): the upward search above a jump brought it, not the downward one.
    data class Item(
        val id: String, val path: String, val mime: String, val size: Long, val thumbKey: String?,
        val preview: Bitmap? = null, val month: String? = null, val created: Long? = null, val hash: String = "",
        val copies: Int? = null, val up: Boolean = false,
    )
    data class DateBucket(val month: String, val count: Int, val start: Int, val end: Int)

    data class State(
        val tags: List<String> = emptyList(),
        val chips: List<String> = emptyList(),
        // Everyone the device found (ListPeople: most photos first), for
        // the search and the People page; peopleLoaded once a list came.
        val allPeople: List<Person> = emptyList(),
        val peopleLoaded: Boolean = false,
        val selectedPeople: List<String> = emptyList(),
        val groups: List<ImageGroup> = emptyList(),
        // A list of collections came (the Collections page's skeleton until then).
        val groupsLoaded: Boolean = false,
        val activeGroup: ImageGroup? = null,
        // Places: a group of the map's photos opened in Images, by its area
        // (SearchPhotos.area, the "Map area" chip) - one more filter on the
        // same search, as a collection is. Set only from the map, so only on
        // a device that answers GetPhotoPlaces.
        val activeArea: PlaceArea? = null,
        // Photo counts per month (issue #77), newest first, for the
        // scrubber, the headers' count and span, the room the last month
        // takes and the grey tiles of a month that hasn't loaded - and
        // whose they are (bucketsKey: the people and the open collection;
        // tags never change them), so a filter's old ones count for
        // nothing until the new ones come (freshBuckets).
        val dateBuckets: List<DateBucket> = emptyList(),
        val bucketsKey: String? = null,
        val scrubFrac: Float? = null,
        // Grey tiles in place of the grid while the scrubber is dragged and
        // until the jump it ends with has its photos, under their month's
        // title (placeholderMonth), so the photos land where they were.
        val placeholderCount: Int? = null,
        val placeholderMonth: String? = null,
        // Counts the jumps whose photos have landed: the grid goes to its top
        // then, the month's title.
        val jumpsLanded: Int = 0,
        // Counts the searches started anew (a filter, a refresh): the grid
        // goes back to its top, as the web's does.
        val searchesStarted: Int = 0,
        val items: List<Item> = emptyList(),
        // The tiles' thumbnails fetched since their page landed (by path; ""
        // when the device has none now): apart from items, so a batch that
        // lands doesn't lay the months out again.
        val thumbKeys: Map<String, String> = emptyMap(),
        val loading: Boolean = false,
        val endReached: Boolean = false,
        // The last page asked for failed, and why (null: none did). It is
        // asked again meanwhile; the grid shows this until a page lands.
        val pageProblem: LoadProblem? = null,
        // The first page has been on its way a while (SLOW_FIRST_PAGE_MS).
        val slowFirstPage: Boolean = false,
        val openIndex: Int? = null,
        // Full-size images by path, kept for the last few opened so the
        // pager can draw the neighbour it slides towards and swiping back
        // doesn't fetch again. hiRes is the open one's (mirrors iOS).
        val hiResImages: Map<String, Bitmap> = emptyMap(),
        // Paths whose full-size image is being fetched: until it arrives
        // the page shows the thumbnail with a "Low res" pill and a spinner;
        // after a failure, the pill alone.
        val hiResLoading: Set<String> = emptySet(),
        // The big thumbnails (JPEG bytes, by path) of items whose thumbnail
        // stays on screen in the viewer: shown instead of the grid's small
        // tile. Never in ThumbStore, which holds the tiles.
        val bigThumbs: Map<String, ByteArray> = emptyMap(),
        // Videos the player couldn't play: their poster stays, saying so.
        val unplayable: Set<String> = emptySet(),
        val videoUrl: String? = null,
        // The alert's text, worded when it shows (the device's words as they
        // came), and its further paragraphs (issue #201: a delete's
        // refusals summed up per kind).
        val alert: UiText? = null,
        val alertMore: List<UiText> = emptyList(),
        // A request the device is too old for: its "needs an update" dialog.
        val needsUpdate: DeviceNeedsUpdate? = null,
        val preparing: SelectionActionTask? = null,
        val selected: Set<String> = emptySet(),
        val infoOpen: Boolean = false,
        val infoLoading: Boolean = false,
        val infoData: FileExifInfo? = null,
        // Issue #194, after a jump: newer photos are still to come above
        // (the top keeps their room, RoomAbove), one of their pages is on
        // its way, or the last one failed (and is asked again meanwhile).
        // upLanded counts the pages added above: the grid keeps the photos
        // on screen where they were.
        val upOn: Boolean = false,
        val upLoading: Boolean = false,
        val upProblem: LoadProblem? = null,
        val upLanded: Int = 0,
        // Issue #201: the viewer's Save is downloading an original (its spinner).
        val saving: Boolean = false,
    ) {
        /** A tag search is sorted by how well photos match: no month titles, no scrubber (the web's isDateOrdered). */
        val dateOrdered get() = chips.isEmpty()
        /**
         * The one person whose photos Images shows - a face opened from
         * People, or one person in the search, tags or a collection with
         * them or not - whom Not this person and Someone else… take photos
         * off. Null with nobody or several (and in the Files viewer).
         */
        val personShown: String? get() = selectedPeople.singleOrNull()
        /** [personShown] as the people's list has them (null: not listed, or none). */
        val personShownListed: Person? get() = personShown?.let { id -> allPeople.firstOrNull { it.id == id } }
        /** Whose date buckets this filter needs: null when it has none (a tag search). */
        val bucketsKeyNow: String? get() = if (dateOrdered) bucketsKeyOf(selectedPeople, activeGroup?.id ?: "", activeArea) else null
        /** What the map shows: the search as the grid has it, but the map's own area. */
        val placesFilter: PlacesFilter get() = PlacesFilter(chips, selectedPeople, activeGroup?.id ?: "")
        val bucketsFresh get() = bucketsKeyNow != null && bucketsKey == bucketsKeyNow
        val freshBuckets: List<DateBucket> get() = if (bucketsFresh) dateBuckets else emptyList()
        val totalPhotos get() = freshBuckets.lastOrNull()?.end ?: 0
        val showScrubber get() = dateOrdered && freshBuckets.isNotEmpty()
        val yearTicks: List<Pair<String, Float>> get() {
            if (totalPhotos <= 0) return emptyList()
            val out = mutableListOf<Pair<String, Float>>()
            var last = ""
            for (b in freshBuckets) {
                val y = b.month.take(4)
                if (y != last) { out += y to b.start.toFloat() / totalPhotos; last = y }
            }
            return out
        }
        val hiRes: Bitmap? get() = openIndex?.let { items.getOrNull(it) }?.let { hiResImages[it.path] }
        val scrubTarget: DateBucket? get() = scrubFrac?.let { scrubBucketAt(freshBuckets, it) }
        /** What the grid's place shows now (galleryBody). */
        val body: GalleryBody get() = galleryBody(this)
        /** A failed page is being asked for again (Try again's "Trying…"). */
        val retrying: Boolean get() = pageProblem != null && loading
        /** [item]'s tile key now: fetched since, or as its page left it; null while it has none. */
        fun thumbKeyOf(item: Item): String? {
            val k = thumbKeys[item.path] ?: return item.thumbKey
            return k.ifEmpty { null }
        }
    }

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state

    /** The map's places (PlacesModel.kt): one for the session, so the map comes back where it was left. */
    val places: PlacesModel by lazy {
        PlacesModel(
            this.scope,
            request = { r -> ask("places", OTCConnection.LIST_TIMEOUT_MS) { it.setReqGetPhotoPlaces(r) } },
            thumbs = thumbs,
            sendThumbs = { b -> ask(TILES_KIND, OTCConnection.PAGE_TIMEOUT_MS) { it.setReqGetThumbnails(b) } },
            pause = pause,
        )
    }
    /** On this day (MemoriesModel.kt): one for the session, so the Day/Week/Month picked stays. */
    val memories: MemoriesModel by lazy {
        MemoriesModel(
            this.scope,
            getMemories = { r -> ask("memories", OTCConnection.LIST_TIMEOUT_MS) { it.setReqGetMemories(r) } },
            hideMemory = { r -> call { it.setReqHideMemory(r) } },
            bigThumbAsk = { path ->
                ask("thumbnail", OTCConnection.LIST_TIMEOUT_MS) { it.setReqGetThumbnails(GetThumbnails.newBuilder().addPaths(path).setSmallThumbnails(false)) }
            },
            thumbs = thumbs,
            sendThumbs = { b -> ask(TILES_KIND, OTCConnection.PAGE_TIMEOUT_MS) { it.setReqGetThumbnails(b) } },
            pause = pause,
        )
    }
    private var token: String? = null
    private var searchGeneration = 0
    // A jump's cutoff (epoch ms), for the search it started: a first page
    // asked again starts at that month again, and a page that went on by
    // its token is checked against it (lostCutoff).
    private var jumpBefore: Long? = null
    private var bucketGeneration = 0
    private var bucketJob: Job? = null
    private var searchJob: Job? = null
    // The furthest tile (its index) that asked for more while a page was
    // loading, so the page that lands can honour it.
    private var morePendingAt: Int? = null
    private val maxPagesWithoutProgress = 12
    // A page asked for by scrolling or a retry: the view model's, so a tile
    // scrolled away (whose effect asked) never cancels a page on its way.
    private var pageJob: Job? = null
    // A failed page's wait before it is asked again, and how many failed in
    // a row (the wait doubles with each).
    private var retryJob: Job? = null
    private var pageFailures = 0
    // The first page's "still waiting" timer (SLOW_FIRST_PAGE_MS).
    private var slowJob: Job? = null
    // A jump whose first page hasn't landed yet: when it does - asked again
    // after a failure, too - the grid goes to its month's title (jumpsLanded).
    private var jumpLanding = false
    // Files viewer: a fixed list (no search, no paging), and what to do
    // after a delete there (refresh the folder listing).
    private var fixedList = false
    private var onDeleted: (() -> Unit)? = null

    // Issue #194: each search's own `have` - the photos the downward search
    // (the one every grid has) delivered, less those deleted since - and,
    // after a jump, the upward one's cursor: its token, its `have`, whether
    // it reached the newest photo, and its page's retry. Never mixed: the
    // device takes a token of the other direction for one it doesn't hold.
    private var downHave = 0
    private var upToken = ""
    private var upHave = 0
    private var upEnd = true
    // The person scrolled up since the jump (scrolledUp): from then on, the
    // rows near the top ask for the newer photos. Never at the jump itself,
    // which lands at the top: a new upward search costs the device a
    // whole-library query.
    private var upArmed = false
    private var upJob: Job? = null
    private var upRetryJob: Job? = null
    private var upFailures = 0
    // The sign-in (signIns) at which the device answered newer_than with an
    // ordinary page (a device before #194): no upward search until the next.
    private var newerUnsupportedAt: Int? = null

    /** Files section: view [items] (a folder's photos and videos, in its order) starting at [startAt]. */
    fun showFiles(items: List<Item>, startAt: Int, onDeleted: () -> Unit) {
        fixedList = true
        this.onDeleted = onDeleted
        pickedAway = emptyMap()
        searchJob?.cancel()
        searchGeneration += 1
        stopPageRetry()
        resetCursors(jump = false)
        tiles.reset()
        undecodable.clear()
        token = null
        _state.update {
            it.copy(items = items, thumbKeys = emptyMap(), endReached = true, loading = false, selected = emptySet(), pageProblem = null, slowFirstPage = false,
                upOn = false, upLoading = false, upProblem = null)
        }
        open(startAt)
    }

    /**
     * Issue #194: a new search or a jump - both searches' counts and the
     * upward cursor start over; only a jump in date order, on a device not
     * found unable since this sign-in, has newer photos to bring above.
     */
    private fun resetCursors(jump: Boolean) {
        downHave = 0
        upToken = ""
        upHave = 0
        upArmed = false
        upFailures = 0
        upJob?.cancel()
        upJob = null
        upRetryJob?.cancel()
        upRetryJob = null
        upEnd = !(jump && !fixedList && _state.value.dateOrdered && newerUnsupportedAt != signIns())
    }

    // The first page goes out at once: the tags and people come alongside,
    // not ahead of it (a slow or lost answer to either used to hold the
    // photos back, on a blank page).
    fun onAppearInitial() = scope.launch {
        watchThumbnailClears()
        watchFaceRecognition()
        watchPlaces()
        watchImagesChanged()
        watchWake()
        listsAsked = true
        thumbs.warmUp()
        restartSearch()
        launch { loadTags() }
        // Not while face recognition is off (as iOS): nobody to offer. Turned
        // on later, watchFaceRecognition asks.
        launch { if (FaceRecognition.enabled.value != false) loadPeople() }
    }

    // The search in the wide layout's top bar (MainView) works from any
    // section, before Images was ever opened: its tags and people are
    // fetched once for it then. Images' own first appearance fetches them
    // too, with its photos.
    private var listsAsked = false
    fun loadListsOnce() {
        if (listsAsked) return
        listsAsked = true
        watchFaceRecognition()
        watchPlaces()
        watchImagesChanged()
        scope.launch {
            loadTags()
            if (FaceRecognition.enabled.value == true) loadPeople()
        }
    }

    /**
     * Images picked in the wide layout's menu: the whole library, as the
     * web's menu does (photoFilter.showAll) - whatever was searched for, and
     * an open collection, are left. Nothing to leave, nothing reloads.
     */
    fun showAll() {
        val st = _state.value
        if (st.chips.isEmpty() && st.selectedPeople.isEmpty() && st.activeGroup == null && st.activeArea == null) return
        _state.update { it.copy(chips = emptyList(), selectedPeople = emptyList(), activeGroup = null, activeArea = null) }
        restartSearch()
    }

    // ---- Places' map area ----

    /** A group of the map's photos too close together to split, in the grid: the search as it was, narrowed to their area. */
    fun showArea(area: PlaceArea) {
        _state.update { it.copy(activeArea = area) }
        restartSearch()
    }

    /** The Map area chip's x: every place's photos again. */
    fun leaveArea() {
        if (_state.value.activeArea == null) return
        _state.update { it.copy(activeArea = null) }
        restartSearch()
    }

    // Places shows only while the device says it is on (PlacesSupport):
    // turned off - here, on another app, or a device without it - the area
    // filter goes (the device would ignore it) and so does what the map
    // held. Started once, by the Images grid or the wide top bar's search.
    private var watchingPlaces = false
    private fun watchPlaces() {
        if (watchingPlaces) return
        watchingPlaces = true
        scope.launch {
            PlacesSupport.enabled.collect { on ->
                if (on == true) return@collect
                leaveArea()
                places.clear()
            }
        }
    }

    // People in the search exist only while face recognition is on
    // (FaceRecognition): turned on, the people are fetched; turned off,
    // nobody is searched for any more. Started once, by the Images grid
    // (not the Files viewer's instance).
    private var watchingFaces = false
    private fun watchFaceRecognition() {
        if (watchingFaces) return
        watchingFaces = true
        scope.launch {
            var was = FaceRecognition.enabled.value
            FaceRecognition.enabled.collect { on ->
                if (on == was) return@collect
                was = on
                if (on == true) { loadPeople(); listsFetchedAt = System.currentTimeMillis() }
                if (on == false && _state.value.selectedPeople.isNotEmpty()) {
                    _state.update { it.copy(selectedPeople = emptyList()) }
                    restartSearch()
                }
            }
        }
    }

    // Issue #192: a folder kept out of Images, or shown there again
    // (Files, ImagesChanged), changes what is here: its photos leave or come
    // back, their tags and faces are deleted (or found again later), an
    // unnamed person left with no face goes and the collections' counts
    // and covers change. The search starts over, and the tags, the people
    // (a person searched for who has gone stops being searched for, as
    // after deleting them in People) and the collections are asked for
    // again. Started once, by the Images grid or the wide top bar's search
    // (not the Files viewer's instance).
    private var watchingImages = false
    private fun watchImagesChanged() {
        if (watchingImages) return
        watchingImages = true
        scope.launch {
            var seen = ImagesChanged.generation.value
            ImagesChanged.generation.collect { g ->
                if (g == seen) return@collect
                seen = g
                imagesChanged()
            }
        }
    }

    // Settings' Clear thumbnail cache: the viewer's big thumbnails and the
    // tiles marked as having none go too (asked for again as they show).
    private var watchingClears = false
    internal fun watchThumbnailClears() {
        if (watchingClears) return
        watchingClears = true
        scope.launch {
            var seen = thumbs.clears.value
            thumbs.clears.collect { n ->
                if (n == seen) return@collect
                seen = n
                tiles.forgetNone()
                undecodable.clear()
                _state.update { st -> st.copy(bigThumbs = emptyMap(), thumbKeys = st.thumbKeys.filterValues { it.isNotEmpty() }) }
            }
        }
    }

    // Signed in again, a network came up, back in the foreground (Wake): a
    // page that failed is asked for at once (the lists' waits are cut short
    // by sleepOrWake). Started once, by the Images grid.
    private var watchingWake = false
    private fun watchWake() {
        if (watchingWake) return
        watchingWake = true
        scope.launch {
            var seen = Wake.count.value
            Wake.count.collect { n ->
                if (n == seen) return@collect
                seen = n
                wake()
            }
        }
    }

    /** A Wake: a page that failed is asked for again now, not at the end of its wait. */
    internal fun wake() {
        if (_state.value.pageProblem != null) retryPage()
        if (_state.value.upProblem != null) retryUp()
    }

    // The phone is online again: "This phone is offline" no longer says
    // why, while the page is asked for again - the neutral line instead
    // (it stayed up for as long as the page then took).
    private fun backOnline() {
        if (_state.value.pageProblem != LoadProblem.OFFLINE) return
        val on = online()
        _state.update { it.copy(pageProblem = it.pageProblem?.backOnline(on)) }
    }

    private fun imagesChanged() {
        listsFetchedAt = System.currentTimeMillis()
        restartSearch()
        // The map's places hold what left Images.
        places.libraryChanged()
        scope.launch {
            loadTags()
            if (FaceRecognition.enabled.value == true && loadPeople()) {
                val ids = _state.value.allPeople.map { it.id }.toSet()
                forgetPeople(_state.value.selectedPeople.filter { it !in ids }.toSet())
            }
        }
        if (_state.value.groupsLoaded || _state.value.activeGroup != null) scope.launch { loadGroups() }
    }

    // New photos bring new tags and faces. The lists are fetched again when
    // a search ends, if the last time was a while ago: current for the next
    // one, and nothing moves under the finger while picking.
    private var listsFetchedAt = System.currentTimeMillis()
    fun refreshListsIfStale() {
        if (System.currentTimeMillis() - listsFetchedAt < LISTS_REFRESH_MS) return
        listsFetchedAt = System.currentTimeMillis()
        scope.launch {
            loadTags()
            if (FaceRecognition.enabled.value == true) loadPeople()
        }
    }

    /** The search field's clear button: tags and people go, an open collection stays. */
    fun clearSearch() {
        val st = _state.value
        if (st.chips.isEmpty() && st.selectedPeople.isEmpty()) return
        _state.update { it.copy(chips = emptyList(), selectedPeople = emptyList()) }
        restartSearch()
    }

    private fun restartSearch() {
        searchJob?.cancel()
        searchJob = scope.launch { resetAndLoadFirstPage() }
    }

    fun addChip(t: String) {
        val x = t.trim()
        if (x.isEmpty() || x in _state.value.chips) return
        _state.update { it.copy(chips = it.chips + x) }
        restartSearch()
    }

    fun removeChip(t: String) {
        _state.update { it.copy(chips = it.chips - t) }
        restartSearch()
    }

    // What the grid and its lists wait on: [baseMs] for the answer, more
    // after a timeout of that kind (OTCConnection.ask, Patience).
    private suspend fun ask(kind: String, baseMs: Long, build: (ReqEnvelope.Builder) -> Unit): RespEnvelope =
        send(kind, baseMs, build)

    // Anything else (a rename, a delete, the full-size photo): the socket's own time.
    private suspend fun call(build: (ReqEnvelope.Builder) -> Unit): RespEnvelope =
        send(null, WSClient.DEFAULT_TIMEOUT_MS, build)

    // The lists the search and the headers use: one that failed is asked
    // again in the background, 1 s doubling to 10 s and at once on a Wake,
    // until it comes or is no longer [wanted] - one loop per list at a time.
    private val listRetries = HashMap<String, Job>()
    private fun keepAsking(list: String, wanted: () -> Boolean = { true }, load: suspend () -> Boolean) {
        if (listRetries[list]?.isActive == true) return
        listRetries[list] = scope.launch { retryWithBackoff(stillWanted = wanted, sleep = pause, waitFirst = true, attempt = load) }
    }

    private suspend fun loadTags() {
        if (!loadTagsOnce()) keepAsking("tags") { loadTagsOnce() }
    }

    private suspend fun loadTagsOnce(): Boolean {
        try {
            val resp = ask("tags", OTCConnection.LIST_TIMEOUT_MS) { it.setReqGetTags(GetTags.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_TAGS_LIST) {
                _state.update { it.copy(tags = resp.respTagsList.tagsList) }
                listsFetchedAt = System.currentTimeMillis()
                return true
            }
            // A device without it: nothing to ask again.
            if (resp.isUnknownPayload()) return true
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {}
        return false
    }

    /** The date buckets of the filter as it is now: a load of its own, in place of any still going. */
    private fun reloadDateBuckets() {
        bucketJob?.cancel()
        bucketJob = scope.launch { loadDateBuckets() }
    }

    // A load that failed is asked again, 1 s doubling to 10 s (the web's
    // usePageRetry), for as long as it is still this filter's: the headers'
    // count and span, the scrubber and the last month's room all wait on it.
    // A filter changed meanwhile has asked for its own.
    private suspend fun loadDateBuckets() {
        val gen = ++bucketGeneration
        val st = _state.value
        val key = st.bucketsKeyNow ?: run { _state.update { it.copy(dateBuckets = emptyList(), bucketsKey = null) }; return }
        val people = st.selectedPeople
        val group = st.activeGroup?.id ?: ""
        val area = st.activeArea
        retryWithBackoff(stillWanted = { gen == bucketGeneration && _state.value.bucketsKeyNow == key }, sleep = pause) {
            loadDateBucketsOnce(people, group, area, key, gen)
        }
    }

    /** One ask for the buckets: false when it failed and is worth asking again. */
    private suspend fun loadDateBucketsOnce(people: List<String>, group: String, area: PlaceArea?, key: String, gen: Int): Boolean {
        val resp = try {
            ask("buckets", OTCConnection.LIST_TIMEOUT_MS) {
                val b = ReqPhotoDateBuckets.newBuilder().addAllPersonIds(people).setGroupId(group).setIncludeVideos(true)
                // Places' map area.
                if (area != null) b.area = area
                it.setReqPhotoDateBuckets(b)
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) { return false }
        // A newer filter asked for its own meanwhile: nothing left to do here.
        if (gen != bucketGeneration) return true
        // A device without them: no scrubber, no counts in the headers, and
        // nothing to ask again.
        if (resp.isUnknownPayload()) { _state.update { it.copy(dateBuckets = emptyList(), bucketsKey = key) }; return true }
        if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_PHOTO_DATE_BUCKETS) return false
        var cum = 0
        val buckets = resp.respPhotoDateBuckets.bucketsList.map { pb -> val s = cum; cum += pb.count; DateBucket(pb.month, pb.count, s, cum) }
        _state.update { it.copy(dateBuckets = buckets, bucketsKey = key) }
        return true
    }

    fun setScrubFrac(f: Float?) = _state.update { it.copy(scrubFrac = f) }

    /**
     * While the scrubber is dragged: the month under it as grey tiles under
     * its title - no request, the count comes from the buckets already
     * loaded (at most [MAX_PLACEHOLDERS]); null puts the photos back.
     */
    fun previewMonth(bucket: DateBucket?) = _state.update {
        if (bucket == null) it.copy(placeholderCount = null, placeholderMonth = null)
        else it.copy(placeholderCount = minOf(bucket.count, MAX_PLACEHOLDERS), placeholderMonth = bucket.month)
    }

    fun jumpToDate(month: String) {
        // Not a month (the device's "No date" bucket has its own cutoff): no
        // jump, the photos as they were - never grey tiles left standing.
        val before = jumpCutoffMs(month, ZoneId.systemDefault()) ?: run { previewMonth(null); return }
        searchJob?.cancel()
        searchJob = scope.launch { performJump(before) }
    }

    // A fresh search like a filter change, anchored at `before` (the last
    // instant of the month, jumpCutoffMs). The grey tiles go once this
    // search is done, unless a newer one took over or a new drag has its
    // own. The selection stays, as on the web: picking photos from two
    // dates is what the scrubber is for.
    //
    // A first page that fails takes the grey tiles away for the grid's
    // "Couldn't load your photos" (as the web's jump does), and is asked
    // again from the same month (jumpBefore); when it lands, the grid goes
    // to that month's title (jumpLanding).
    private suspend fun performJump(before: Long) {
        searchGeneration += 1
        val mine = searchGeneration
        stopPageRetry()
        token = ""
        jumpBefore = before
        jumpLanding = true
        morePendingAt = null
        // Issue #194: the photos newer than the jump come above it, asked
        // for only once the person scrolls up to them (loadNewerIfNeeded).
        resetCursors(jump = true)
        tiles.reset()
        undecodable.clear()
        keepPicks()
        _state.update {
            it.copy(loading = false, endReached = false, items = emptyList(), thumbKeys = emptyMap(), pageProblem = null, slowFirstPage = false,
                upOn = !upEnd, upLoading = false, upProblem = null)
        }
        watchSlowFirstPage(mine)
        fetchUntilProgress()
        if (mine == searchGeneration && _state.value.scrubFrac == null) _state.update { it.copy(placeholderCount = null, placeholderMonth = null) }
    }

    /**
     * The people, asked again: true once the device's list is in. One that
     * failed keeps being asked in the background while face recognition
     * isn't off (People's page shows its own "Couldn't load people" until).
     */
    override suspend fun loadPeople(): Boolean {
        if (loadPeopleOnce()) return true
        keepAsking("people", wanted = { FaceRecognition.enabled.value != false }) { loadPeopleOnce() }
        return false
    }

    private suspend fun loadPeopleOnce(): Boolean {
        try {
            // A page of faces: each person comes with their cover.
            val resp = ask("people", OTCConnection.PAGE_TIMEOUT_MS) { it.setReqListPeople(ListPeople.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_PEOPLE) {
                _state.update { it.copy(allPeople = resp.respPeople.peopleList, peopleLoaded = true) }
                return true
            }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {}
        return false
    }

    /**
     * The collections, asked again: true once the device's list is in
     * (groupsLoaded). One that failed keeps being asked in the background:
     * the open collection's header and the Collections page fill in when it
     * comes.
     */
    suspend fun loadGroups(): Boolean {
        if (loadGroupsOnce()) return true
        keepAsking("groups") { loadGroupsOnce() }
        return false
    }

    private suspend fun loadGroupsOnce(): Boolean {
        try {
            // Each collection comes with its cover.
            // Their covers as the small thumbnails a grid shows (release 111).
            val resp = ask("groups", OTCConnection.PAGE_TIMEOUT_MS) { it.setReqListImageGroups(ListImageGroups.newBuilder().setSmallThumbnails(true)) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_IMAGE_GROUPS) {
                val gs = resp.respImageGroups.groupsList
                _state.update { st -> st.copy(groups = gs, groupsLoaded = true, activeGroup = st.activeGroup?.let { open -> gs.firstOrNull { it.id == open.id } ?: open }) }
                // The collection shown as wallpaper: renamed or deleted elsewhere, it follows.
                Wallpaper.collectionsListed(gs)
                return true
            }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {}
        return false
    }

    /** A collection's photos: a new search, the tags and people gone (the web's openGroup). */
    fun openGroup(g: ImageGroup) { _state.update { it.copy(chips = emptyList(), selectedPeople = emptyList(), activeGroup = g, activeArea = null) }; restartSearch() }
    fun leaveGroup() { _state.update { it.copy(activeGroup = null) }; restartSearch() }

    suspend fun renameActiveGroup(newName: String) {
        val g = _state.value.activeGroup ?: return
        val name = newName.trim()
        if (name.isEmpty() || name == g.name) return
        val ok = try {
            val r = call { it.setReqRenameImageGroup(RenameImageGroup.newBuilder().setId(g.id).setName(name)) }
            r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok
        } catch (e: Exception) { false }
        if (!ok) { alert(S.appPhotosNoticeRenameFailed()); return }
        val renamed = g.toBuilder().setName(name).build()
        _state.update { st -> st.copy(activeGroup = renamed, groups = st.groups.map { if (it.id == g.id) renamed else it }) }
        Wallpaper.collectionRenamed(g.id, name)
    }

    suspend fun deleteActiveGroup() {
        val g = _state.value.activeGroup ?: return
        val ok = try {
            val r = call { it.setReqDeleteImageGroup(DeleteImageGroup.newBuilder().setId(g.id)) }
            r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok
        } catch (e: Exception) { false }
        if (!ok) { alert(S.appPhotosNoticeDeleteCollectionFailed()); return }
        _state.update { st -> st.copy(groups = st.groups.filter { it.id != g.id }) }
        // Its wallpaper stops, and the copies on the phone go.
        Wallpaper.collectionDeleted(g.id)
        leaveGroup()
    }

    suspend fun createGroupFromSelection(rawName: String) {
        val name = rawName.trim()
        if (name.isEmpty()) return
        val paths = _state.value.selected.toList()
        try {
            val r = call { it.setReqCreateImageGroup(CreateImageGroup.newBuilder().setName(name).addAllPaths(paths).setSmallThumbnails(true)) }
            if (r.payloadCase != RespEnvelope.PayloadCase.RESP_IMAGE_GROUP) { alert(S.appPhotosNoticeCreateFailed()); return }
            pickedAway = emptyMap()
            _state.update { st -> st.copy(groups = listOf(r.respImageGroup.group) + st.groups, selected = emptySet()) }
            alert(S.appPhotosNoticeCreated(name))
        } catch (e: Exception) { alert(S.appPhotosNoticeCreateFailed()) }
    }

    suspend fun addSelectionToGroup(g: ImageGroup) {
        val paths = _state.value.selected.toList()
        val ok = try {
            val r = call { it.setReqAddToImageGroup(AddToImageGroup.newBuilder().setGroupId(g.id).addAllPaths(paths)) }
            r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok
        } catch (e: Exception) { false }
        if (!ok) { alert(S.appPhotosNoticeAddFailed()); return }
        pickedAway = emptyMap()
        _state.update { it.copy(selected = emptySet()) }
        Wallpaper.collectionChanged(g.id)
        loadGroups()
        if (_state.value.activeGroup?.id == g.id) restartSearch()
        alert(S.appPhotosNoticeAdded(g.name))
    }

    /**
     * Remove from collection: the picked photos out of the open collection -
     * the membership only; they stay on the device and in other collections
     * - and so off its grid, as deleted ones would go (with the months and
     * the collection's count). A member is a photo (issue #201), so every
     * tile of one goes. A device from before the request answers
     * unknown_payload: it needs an update (needsUpdate's dialog).
     */
    fun removeSelectionFromGroup() = scope.launch {
        val st = _state.value
        val g = st.activeGroup ?: return@launch
        val paths = st.selected.toList()
        if (paths.isEmpty() || st.preparing != null) return@launch
        _state.update { it.copy(preparing = SelectionActionTask.UNCOLLECT) }
        try {
            val r = try {
                call { it.setReqRemoveFromImageGroup(RemoveFromImageGroup.newBuilder().setGroupId(g.id).addAllPaths(paths)) }
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) { null }
            when {
                r != null && r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok -> {
                    val hashes = pickedByPath(st.items, pickedAway, st.selected).values.mapNotNull { it.hash.ifEmpty { null } }.toSet()
                    removeDeleted(paths.toSet() + _state.value.items.filter { it.hash in hashes }.map { it.path }, keepThumbs = true)
                    clearSelection()
                    countsChanged()
                    Wallpaper.collectionChanged(g.id)
                    alert(S.appPhotosNoticeRemoved(g.name))
                }
                r != null && r.error && ErrorCodes.isUnknownPayload(r.errorCode, r.errorMessage) -> {
                    val ask = deviceNeedsUpdate(S.appPhotosCollectionsRemoveNeedsUpdate()) { build -> call(build) }
                    _state.update { it.copy(needsUpdate = ask) }
                }
                else -> alert(S.appPhotosNoticeRemoveFailed())
            }
        } finally { _state.update { it.copy(preparing = null) } }
    }

    fun dismissNeedsUpdate() = _state.update { it.copy(needsUpdate = null) }

    /**
     * "Not this person" ([to] null) or "Someone else…" ([to]: the person
     * picked) on the selection: face recognition filed the picked photos
     * under the person Images shows ([State.personShown]) by mistake (see
     * [takeOffPerson]). Done, the selection is left.
     */
    fun takeSelectionOffPerson(to: Person? = null) = scope.launch {
        val st = _state.value
        val person = st.personShown ?: return@launch
        if (st.selected.isEmpty() || st.preparing != null || to?.id == person) return@launch
        val picked = pickedByPath(st.items, pickedAway, st.selected)
        _state.update { it.copy(preparing = personTask(to)) }
        try {
            if (takeOffPerson(person, st.selected, picked.values, to)) clearSelection()
        } finally { _state.update { it.copy(preparing = null) } }
    }

    /**
     * The viewer's Not this person ([to] null) and Someone else… ([to]: the
     * person picked), for the photo open (see [takeOffPerson]). Done, the
     * viewer moves on to the next photo, or closes after the last, as after
     * a delete.
     */
    fun takeOpenOffPerson(to: Person? = null) = scope.launch {
        val st = _state.value
        val person = st.personShown ?: return@launch
        val item = st.openIndex?.let { st.items.getOrNull(it) } ?: return@launch
        if (st.preparing != null || to?.id == person) return@launch
        _state.update { it.copy(preparing = personTask(to)) }
        try {
            val at = st.items.indexOfFirst { it.id == item.id }
            if (!takeOffPerson(person, setOf(item.path), listOf(item), to)) return@launch
            // Not if it was closed meanwhile.
            if (_state.value.openIndex == null) return@launch
            val left = _state.value.items
            if (left.isEmpty()) closeModal() else open(minOf(at.coerceAtLeast(0), left.size - 1))
        } finally { _state.update { it.copy(preparing = null) } }
    }

    private fun personTask(to: Person?) = if (to == null) SelectionActionTask.NOT_PERSON else SelectionActionTask.MOVE_PERSON

    /**
     * RemoveFromPerson: [items] (by their content's hash) off [person] - the
     * match only, the photos stay - moved to [to], or left matched to no one.
     * True once the device took it: the photos leave the grid at once (every
     * tile of each, with [paths]: picks off the grid too), as photos taken
     * out of a collection do, the months' counts follow, the people are
     * asked again (their counts and covers; the People page asks again as
     * it shows), and the note says how many went, and where. A device from
     * before the request answers unknown_payload: it needs an update
     * (needsUpdate's dialog); anything else is "Couldn't remove/move them".
     */
    private suspend fun takeOffPerson(person: String, paths: Set<String>, items: Collection<Item>, to: Person?): Boolean {
        val hashes = items.mapNotNull { it.hash.ifEmpty { null } }.distinct()
        val failed = if (to == null) S.appPhotosPersonRemoveFailed() else S.appPhotosPersonMoveFailed()
        // Nothing to name them by (no device sends photos without their hash now).
        if (hashes.isEmpty()) { alert(failed); return false }
        val from = _state.value.personShownListed
        val r = try {
            call { it.setReqRemoveFromPerson(removeFromPersonRequest(person, hashes, to?.id)) }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) { null }
        when {
            r != null && r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok -> {
                val gone = hashes.toSet()
                removeDeleted(paths + _state.value.items.filter { it.hash in gone }.map { it.path }, keepThumbs = true)
                countsChanged()
                scope.launch { loadPeople() }
                alert(personTakenOffNote(hashes.size, from, to))
                return true
            }
            r != null && r.error && ErrorCodes.isUnknownPayload(r.errorCode, r.errorMessage) -> {
                val ask = deviceNeedsUpdate(S.appPhotosPersonNeedsUpdate()) { build -> call(build) }
                _state.update { it.copy(needsUpdate = ask) }
            }
            else -> alert(failed)
        }
        return false
    }

    fun togglePerson(id: String) {
        _state.update { st -> st.copy(selectedPeople = if (id in st.selectedPeople) st.selectedPeople - id else st.selectedPeople + id) }
        restartSearch()
    }

    /** One person's photos, from the People page: a new search (the web's showPerson). */
    fun showPerson(id: String) {
        _state.update { it.copy(chips = emptyList(), selectedPeople = listOf(id), activeGroup = null, activeArea = null) }
        restartSearch()
    }

    /** A name the device took (PeopleView): the search and the chips have it at once. */
    override fun renamePersonLocally(id: String, name: String) {
        _state.update { st -> st.copy(allPeople = st.allPeople.map { if (it.id == id) it.toBuilder().setName(name).build() else it }) }
    }

    /** People merged away or deleted (PeopleView): gone from the list, and from the search. */
    override fun forgetPeople(ids: Set<String>) {
        if (ids.isEmpty()) return
        val searched = _state.value.selectedPeople.any { it in ids }
        _state.update { st -> st.copy(allPeople = st.allPeople.filter { it.id !in ids }, selectedPeople = st.selectedPeople - ids) }
        if (searched) restartSearch()
    }

    suspend fun resetAndLoadFirstPage() {
        if (fixedList) return
        searchGeneration += 1
        stopPageRetry()
        token = ""
        jumpBefore = null
        jumpLanding = false
        morePendingAt = null
        resetCursors(jump = false)
        tiles.reset()
        undecodable.clear()
        // The selection stays, as the web's does across a filter change
        // (leaving for People or Collections clears it, as the web's page
        // change does: PhotoGalleryView).
        keepPicks()
        _state.update {
            it.copy(
                loading = false, endReached = false, items = emptyList(), thumbKeys = emptyMap(), pageProblem = null, slowFirstPage = false,
                scrubFrac = null, placeholderCount = null, placeholderMonth = null, searchesStarted = it.searchesStarted + 1,
                upOn = false, upLoading = false, upProblem = null,
            )
        }
        reloadDateBuckets()
        watchSlowFirstPage(searchGeneration)
        fetchUntilProgress()
    }

    /** The grid drew the photo at [idx]: the next page, once it is near the end. */
    fun loadMoreIfNeeded(idx: Int) {
        val st = _state.value
        if (st.endReached || fixedList) return
        if (idx < 0 || idx < st.items.size - 12) return
        // A page that failed waits for its retry (or Try again, or a Wake),
        // not for the next tile drawn: the web's retryReady().
        if (st.pageProblem != null) return
        if (st.loading) { morePendingAt = maxOf(morePendingAt ?: idx, idx); return }
        askForPages()
    }

    // The rows of tiles the grid has composed, by the row's key: their
    // photos [start, end), for the tiles within reach of the scroll
    // (GridThumbFetcher.near) and for knowing the person is near the top.
    private class ComposedRow(val start: Int, val end: Int)
    private val composedRows = HashMap<Any, ComposedRow>()

    /**
     * A row of tiles (items [start] until [end]) is on screen or about to
     * be. Near the top after a jump, once the person scrolled up (issue
     * #194), it asks for the newer photos.
     */
    fun rowShown(key: Any, start: Int, end: Int) {
        if (fixedList) return
        composedRows[key] = ComposedRow(start, end)
        nearChanged()
        loadNewerIfNeeded()
    }

    /**
     * Issue #194: the grid was scrolled up - however the person scrolls (a
     * drag or fling, at the top too, the wheel, TalkBack, a key): after a
     * jump, from now on the rows near the top ask for the newer photos,
     * these first if they are near it. Not before the jump's photos are
     * there, nor while the scrubber is dragged or its jump is on its way
     * (it scrolls the grid itself).
     */
    fun scrolledUp() {
        val st = _state.value
        if (fixedList || upEnd || !st.upOn || st.items.isEmpty() || st.scrubFrac != null || st.placeholderCount != null) return
        upArmed = true
        loadNewerIfNeeded()
    }

    fun rowGone(key: Any) { composedRows.remove(key) }

    // About 12 tiles before the first row composed and 30 after the last:
    // their thumbnails are asked for, after the tiles on screen.
    private fun nearChanged() {
        if (composedRows.isEmpty()) return
        val items = _state.value.items
        val from = maxOf(0, composedRows.values.minOf { it.start } - GridThumbFetcher.REACH_BEFORE)
        val to = minOf(items.size, composedRows.values.maxOf { it.end } + GridThumbFetcher.REACH_AFTER)
        if (from < to) tiles.near(items.subList(from, to).map { it.path })
    }

    // Issue #194: the person scrolled up since the jump, and the grid is
    // drawn near its top - a row of the first photos (or of the grey tiles
    // before them, in a month still filling from above).
    private fun nearTop(): Boolean = upArmed && composedRows.values.any { it.start < UP_NEAR }

    /**
     * Issue #194: the next page of newer photos, when the person is near
     * the top of a jump's photos and more are to come. Never at the jump
     * itself: only once the person scrolled up (scrolledUp).
     */
    fun loadNewerIfNeeded() {
        val st = _state.value
        if (fixedList || upEnd || !st.upOn || st.upLoading || st.upProblem != null) return
        // Not before the jump's own photos are there, nor over the scrubber's grey tiles.
        if (st.items.isEmpty() || st.placeholderCount != null) return
        if (!nearTop()) return
        askUp()
    }

    // On its way from here (upLoading), so a second ask meanwhile is none.
    private fun askUp() {
        if (_state.value.upLoading) return
        val gen = searchGeneration
        _state.update { it.copy(upLoading = true) }
        upJob = scope.launch {
            try {
                if (gen == searchGeneration) fetchUpPage()
            } finally {
                if (gen == searchGeneration) _state.update { it.copy(upLoading = false) }
            }
        }
    }

    /** The newer photos' Try again (and a Wake): asked for now, the wait started over. */
    fun retryUp() {
        if (fixedList || upEnd || _state.value.upProblem == null) return
        backOnline()
        upFailures = 0
        upRetryJob?.cancel()
        upRetryJob = null
        _state.update { it.copy(upProblem = it.upProblem?.backOnline(online())) }
        askUp()
    }

    // A page of newer photos failed while its jump is the current search:
    // the top says so, and it is asked again, 1 s doubling to 10 s, with the
    // same token and have.
    private fun upFailed(gen: Int, problem: LoadProblem) {
        if (gen != searchGeneration) return
        _state.update { it.copy(upProblem = problem) }
        upRetryJob?.cancel()
        val wait = retryWaitMs(++upFailures)
        upRetryJob = scope.launch {
            pause(wait)
            if (gen == searchGeneration && !upEnd) askUp()
        }
    }

    /**
     * Issue #194: the next page of the photos newer than the jump's cutoff
     * (SearchPhotos.newer_than, the same filters), oldest first: turned
     * round and put above the photos, without one already there. A device
     * before #194 answers an ordinary page (oldest_first false): dropped,
     * and not asked again until the next sign-in. An error is a failed page.
     */
    private suspend fun fetchUpPage() {
        val cutoff = jumpBefore ?: return
        val st0 = _state.value
        if (upEnd || fixedList) return
        val mine = searchGeneration
        val tok = upToken
        val have = upHave
        val firstLimit = if (thumbs.omitsThumbnails()) FIRST_PAGE_LIMIT_WITHOUT_THUMBS else FIRST_PHOTO_PAGE_LIMIT
        val thumbScope = thumbs.scope()
        var resp: RespEnvelope? = null
        var error: Exception? = null
        try {
            resp = ask("page", OTCConnection.PAGE_TIMEOUT_MS) { e ->
                val sp = SearchPhotos.newBuilder().addAllTags(st0.chips).addAllPersonIds(st0.selectedPeople).setGroupId(st0.activeGroup?.id ?: "")
                    .setIncludeVideos(true).setToken(tok).setHave(have).setSmallThumbnails(true).setOmitThumbnails(true)
                st0.activeArea?.let { sp.area = it }
                sp.limit = if (tok.isEmpty()) firstLimit else NEXT_PAGE_LIMIT_WITHOUT_THUMBS
                // On every page of it, retries too; never `before`.
                sp.newerThan = timestampOf(cutoff)
                e.setReqSearchPhotos(sp)
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            error = e
        }
        if (mine != searchGeneration) return
        val got = resp
        if (got == null || got.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) {
            upFailed(mine, problemOf(got, error))
            return
        }
        val lof = got.respListOfFiles
        if (!lof.oldestFirst) {
            newerUnsupportedAt = signIns()
            upEnd = true
            upRetryJob?.cancel()
            _state.update { it.copy(upOn = false, upProblem = null) }
            return
        }
        val landed = thumbs.land(lof.filesList, thumbScope)
        if (mine != searchGeneration) return
        val zone = ZoneId.systemDefault()
        val newer = lof.filesList.asReversed().map { itemOf(it, landed.keys[it.path], zone, up = true) }
        upHave += lof.filesCount
        upToken = lof.token
        upEnd = lof.token.isEmpty() || lof.filesCount == 0
        upFailures = 0
        upRetryJob?.cancel()
        upRetryJob = null
        var added = 0
        // Done with it in the same change: the rows drawn again then
        // (upLanded) may ask for the next page at once.
        _state.update { st ->
            val fresh = withoutShown(newer, st.items)
            added = fresh.size
            st.copy(
                items = fresh + st.items, upOn = !upEnd, upProblem = null, upLanded = st.upLanded + 1, upLoading = false,
                // The viewer stays on its photo.
                openIndex = st.openIndex?.let { it + fresh.size },
            )
        }
        morePendingAt = morePendingAt?.let { it + added }
        // The rows drawn are of the grid before these: each one drawn now
        // says what it is again (its effect is keyed on upLanded), with its
        // photos' places as they are now - still near the top, the next
        // page is asked for.
        composedRows.clear()
        tiles.note(landed.fetch, landed.refetch)
    }

    /** A page's File as a tile (its month and date worked out once, here). */
    private fun itemOf(f: PbFile, thumbKey: String?, zone: ZoneId, up: Boolean = false): Item {
        val month = if (f.hasCreated()) monthOf(f.created.seconds, f.created.nanos, zone) else null
        val created = if (f.hasCreated()) f.created.seconds * 1000 + f.created.nanos / 1_000_000 else null
        return Item(
            "${f.path}#${f.hash}#${f.byteSize}", f.path, f.mime, f.byteSize, thumbKey, month = month, created = created, hash = f.hash,
            copies = if (f.hasCopies()) f.copies else null, up = up,
        )
    }

    /** A tile is on screen ([needsThumb]: without a thumbnail): its thumbnail is fetched before the others. */
    fun tileShown(path: String, needsThumb: Boolean) { if (!fixedList) tiles.shown(path, needsThumb) }

    /** The tile left the screen. */
    fun tileGone(path: String) { if (!fixedList) tiles.gone(path) }

    /** A tile's thumbnail is gone from the cache (cleared in Settings, or by the system): fetched again. */
    fun thumbLost(path: String) {
        if (fixedList) return
        _state.update { it.copy(thumbKeys = it.thumbKeys + (path to "")) }
        tiles.shown(path, needsThumb = true)
    }

    // Tiles whose bytes failed to decode this search: a second time, none.
    private val undecodable = HashSet<String>()

    /**
     * A tile's bytes ([key]) didn't decode (a file a power cut left zeroed
     * passes the size check): dropped from the cache and fetched again - and
     * if those don't decode either, the tile has none (never a loop).
     */
    fun thumbUndecodable(path: String, key: String) {
        if (fixedList) return
        scope.launch { thumbs.discard(key) }
        if (undecodable.add(path)) { thumbLost(path); return }
        _state.update { it.copy(thumbKeys = it.thumbKeys + (path to "")) }
        tiles.markNone(path)
    }

    /** Tiles' bytes by key (State.thumbKeyOf): memory, then the cache on disk. */
    suspend fun loadThumb(key: String): ByteArray? = thumbs.load(key)

    /** Try again (and a Wake): the page that failed, asked for now, its wait started over. */
    fun retryPage() {
        if (fixedList || _state.value.endReached) return
        backOnline()
        pageFailures = 0
        retryJob?.cancel()
        retryJob = null
        askForPages()
    }

    // Pages in a job of the view model's own: a tile's effect that asked
    // may go (scrolled away) while its page is still on its way.
    private fun askForPages() {
        val gen = searchGeneration
        pageJob = scope.launch { if (gen == searchGeneration) fetchUntilProgress() }
    }

    // A new search (or the Files viewer): the old one's retry, pages and
    // timer go with it.
    private fun stopPageRetry() {
        pageFailures = 0
        retryJob?.cancel()
        retryJob = null
        pageJob?.cancel()
        pageJob = null
        slowJob?.cancel()
        slowJob = null
    }

    /**
     * The page asked for failed ([problem]: why) while its search is the
     * current one: the grid says so, and it is asked again after 1 s,
     * doubling to 10 s (the web's usePageRetry) - [noProgress]: from 10 s to
     * a minute - the wait cut short by a Wake or Try again.
     */
    private fun pageFailed(gen: Int, problem: LoadProblem, noProgress: Boolean = false) {
        if (gen != searchGeneration) return
        slowJob?.cancel()
        _state.update { it.copy(pageProblem = problem, slowFirstPage = false) }
        retryJob?.cancel()
        retryJob = null
        val wait = if (noProgress) retryWaitMs(++pageFailures, NO_PROGRESS_FIRST_MS, NO_PROGRESS_MAX_MS) else retryWaitMs(++pageFailures)
        retryJob = scope.launch {
            pause(wait)
            if (gen == searchGeneration) askForPages()
        }
    }

    // The first page still on its way after a while: the grey tiles say so.
    private fun watchSlowFirstPage(gen: Int) {
        slowJob?.cancel()
        slowJob = scope.launch {
            delay(SLOW_FIRST_PAGE_MS)
            val st = _state.value
            if (gen == searchGeneration && st.items.isEmpty() && st.pageProblem == null) _state.update { it.copy(slowFirstPage = true) }
        }
    }

    // Photos deleted until none is left while more pages are to come:
    // nothing on screen would ask for them.
    private fun refillIfEmptied() {
        val st = _state.value
        if (st.items.isEmpty() && !st.endReached && !st.loading && st.pageProblem == null) askForPages()
    }

    private suspend fun fetchUntilProgress() {
        val gen = searchGeneration
        repeat(maxPagesWithoutProgress) {
            val before = _state.value.items.size
            // A failure has its retry waiting already (pageFailed).
            if (fetchPage() != PageResult.LANDED) return
            if (gen != searchGeneration || _state.value.endReached || _state.value.items.size > before) return
        }
        // Page after page that added nothing (a device starting the search
        // over and over): said with its Try again, and asked for again in a
        // while rather than on and on - the grid promises the photos.
        pageFailed(gen, LoadProblem.FAILED, noProgress = true)
    }

    private enum class PageResult { LANDED, SKIPPED, FAILED }

    // SKIPPED: another page is on its way, the end was reached, or a newer
    // search took over meanwhile - nothing failed.
    private suspend fun fetchPage(overrideToken: String? = null, beforeMs: Long? = null): PageResult {
        val st0 = _state.value
        if (st0.loading || st0.endReached) return PageResult.SKIPPED
        val mine = searchGeneration
        _state.update { it.copy(loading = true) }
        backOnline()
        try {
            val tags = st0.chips
            val people = st0.selectedPeople
            val group = st0.activeGroup?.id ?: ""
            val area = st0.activeArea
            val requestToken = overrideToken ?: token ?: ""
            // What this search brought, not what the grid holds: photos
            // above a jump came from another one (issue #194).
            val have = downHave
            // A device known to leave the thumbnails out gets a bigger first
            // page: its rows are ~200 bytes each, the tiles come from the
            // cache or GetThumbnails. One that sends them (before release
            // 113) gets the small first page, as before.
            val firstLimit = if (thumbs.omitsThumbnails()) FIRST_PAGE_LIMIT_WITHOUT_THUMBS else FIRST_PHOTO_PAGE_LIMIT
            // The device this page is asked of: what it brings is kept for it only.
            val thumbScope = thumbs.scope()
            // What came instead of a page (null: nothing came), and what was
            // thrown instead of an answer: why the grid says it failed.
            var resp: RespEnvelope? = null
            var error: Exception? = null
            suspend fun page(tok: String, cutoffMs: Long?) {
                resp = null
                error = null
                try {
                    resp = ask("page", OTCConnection.PAGE_TIMEOUT_MS) { e ->
                        // The grid's tiles: small thumbnails (release 111;
                        // an older device sends big ones), left out of the
                        // page (release 113; an older device sends them).
                        val sp = SearchPhotos.newBuilder().addAllTags(tags).addAllPersonIds(people).setGroupId(group).setIncludeVideos(true).setToken(tok).setHave(have)
                            .setSmallThumbnails(true).setOmitThumbnails(true)
                        // Places: a group of the map's photos, by its area.
                        if (area != null) sp.area = area
                        // A search starting here (opening, a filter, the
                        // scrubber's jump) gets a first page of its own size;
                        // scrolling on, a bigger one.
                        sp.limit = if (tok.isEmpty()) firstLimit else NEXT_PAGE_LIMIT_WITHOUT_THUMBS
                        if (cutoffMs != null) sp.before = timestampOf(cutoffMs)
                        e.setReqSearchPhotos(sp)
                    }
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    error = e
                }
            }
            // The cutoff goes only on the request that starts the search (a
            // first page asked again after a jump starts at its month again);
            // the token carries the place after.
            page(requestToken, if (requestToken.isEmpty()) beforeMs ?: jumpBefore else null)
            if (mine != searchGeneration) return PageResult.SKIPPED
            // After a jump, a page from a search the device started again
            // (lostCutoff) is from the wrong end of the library. Asked once
            // more with the cutoff, the device runs the jump's search,
            // skipping the `have` photos the grid holds, whatever the token.
            val cutoff = jumpBefore
            val first = resp
            if (requestToken.isNotEmpty() && cutoff != null && first != null && first.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) {
                val lof = first.respListOfFiles
                if (lostCutoff(lof.filesList.map { (if (it.hasCreated()) it.created.seconds * 1000 + it.created.nanos / 1_000_000 else null) to it.path }, lof.token, requestToken, cutoff, st0.items.mapTo(HashSet()) { it.path })) {
                    page(requestToken, cutoff)
                    if (mine != searchGeneration) return PageResult.SKIPPED
                }
            }
            val got = resp
            // An error answer (the bridge's "device unreachable", the device's
            // own) is no page and no end of the library either.
            if (got == null || got.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) {
                pageFailed(mine, problemOf(got, error))
                return PageResult.FAILED
            }
            val lof = got.respListOfFiles
            // Each tile from the page (a device before release 113) or the
            // cache; what it lacks is fetched once the page is in.
            val landed = thumbs.land(lof.filesList, thumbScope)
            if (mine != searchGeneration) return PageResult.SKIPPED
            // Each photo's month, once, here: the grid lays the months out
            // from these (PhotoMonths.kt) without looking at a date again.
            val zone = ZoneId.systemDefault()
            val newItems = lof.filesList.map { f -> itemOf(f, landed.keys[f.path], zone) }
            token = lof.token.ifEmpty { null }
            // The device counts every photo of the page as delivered, one
            // already shown above a jump too (its `after`).
            downHave += lof.filesCount
            // A jump's first page (asked again after a failure too): the grid
            // goes to its month's title.
            val jumped = jumpLanding && requestToken.isEmpty()
            if (jumped) jumpLanding = false
            pageFailures = 0
            retryJob?.cancel()
            retryJob = null
            slowJob?.cancel()
            _state.update { st ->
                st.copy(
                    items = st.items + withoutShown(newItems, st.items), endReached = token == null,
                    pageProblem = null, slowFirstPage = false, jumpsLanded = if (jumped) st.jumpsLanded + 1 else st.jumpsLanded,
                )
            }
            // Asked for as their tiles show or come near.
            tiles.note(landed.fetch, landed.refetch)
            nearChanged()
            return PageResult.LANDED
        } finally {
            if (mine == searchGeneration) {
                _state.update { it.copy(loading = false) }
                // Only if that tile is still near the end: every tile of a
                // small first page asks at once, and the page that just
                // landed moved the end well past them - honouring them
                // anyway pulled a third page nobody had scrolled to. Not
                // after a failure: that page waits for its retry.
                val at = morePendingAt
                morePendingAt = null
                val st = _state.value
                if (at != null && !st.endReached && st.pageProblem == null && at >= st.items.size - 12) scope.launch { fetchUntilProgress() }
            }
        }
    }

    // Viewer. hiResOrder: the cached full-size images, least recently used first.
    private val hiResOrder = mutableListOf<String>()
    private val inFlightHiRes = mutableSetOf<String>()
    private val hiResCacheSize = 8
    // And a byte budget: a 12 MP photo decodes to 48 MB, so eight of them
    // could hold ~400 MB. The open photo and its neighbours stay regardless.
    private val hiResBudgetBytes: Long by lazy {
        val am = OTCApp.instance.getSystemService(ActivityManager::class.java)
        val mb = when {
            am == null -> 128
            am.isLowRamDevice -> 64
            else -> (am.memoryClass / 2).coerceIn(96, 192)
        }
        mb.toLong() shl 20
    }

    fun open(index: Int) {
        if (index !in _state.value.items.indices) return
        // Another item: the last one's big thumbnail is no longer asked for.
        if (bigThumbFor != _state.value.items[index].path) stopBigThumb()
        _state.update { it.copy(openIndex = index, videoUrl = null, infoOpen = false, infoData = null) }
        // Alone: its neighbours wait until it is on screen (readyNeighbours).
        scope.launch { fetchHiRes(index) }
    }

    /**
     * The open photo [path] is on screen at full size, and the viewer slides
     * towards its neighbours, so they are readied - after it, never with it:
     * fetched at the same moment, they shared the connection with the photo
     * being looked at, which then came later. The next one first (the usual
     * way through), then the previous, one after the other; fetchHiRes skips
     * one already shown or on its way, and one kept this session
     * (FullPhotoCache) is only read and decoded. Only while the viewer stays
     * on [path], from [NEIGHBOURS_AFTER_MS] on: a run of quick swipes
     * readies the photo it stops on, not every one it passed, and an item
     * opened meanwhile readies its own. Never after a failure or for a video
     * (fetchHiRes doesn't call it then).
     */
    private fun readyNeighbours(path: String) {
        if (openPath() != path) return
        scope.launch {
            delay(NEIGHBOURS_AFTER_MS)
            for (step in intArrayOf(1, -1)) {
                if (openPath() != path) return@launch
                val n = (_state.value.openIndex ?: return@launch) + step
                if (n in _state.value.items.indices) fetchHiRes(n, prefetch = true)
            }
        }
    }

    fun closeModal() {
        hiResOrder.clear()
        stopBigThumb()
        _state.update { it.copy(openIndex = null, hiResImages = emptyMap(), videoUrl = null, bigThumbs = emptyMap(), unplayable = emptySet()) }
        // The Files viewer's list only lives while it is open.
        if (fixedList) _state.update { it.copy(items = emptyList()) }
    }

    private fun cacheHiRes(path: String, bmp: Bitmap) {
        hiResOrder -= path
        hiResOrder += path
        val st = _state.value
        val keep = st.openIndex?.let { i -> (i - 1..i + 1).mapNotNull { st.items.getOrNull(it)?.path }.toSet() } ?: emptySet()
        val images = st.hiResImages + (path to bmp)
        var total = images.values.sumOf { it.allocationByteCount.toLong() }
        val evicted = mutableSetOf<String>()
        val oldest = hiResOrder.iterator()
        while (oldest.hasNext() && (hiResOrder.size > hiResCacheSize || total > hiResBudgetBytes)) {
            val p = oldest.next()
            if (p in keep) continue
            oldest.remove()
            evicted += p
            total -= images[p]?.allocationByteCount?.toLong() ?: 0
        }
        // The new one may be evicted too (far from the open photo, budget
        // spent): then it isn't kept untracked by hiResOrder either.
        _state.update { it.copy(hiResImages = (it.hiResImages + (path to bmp)) - evicted) }
    }

    // Swiped back to: the newest again, so it isn't the next one evicted while shown.
    private fun touchHiRes(path: String) { if (hiResOrder.remove(path)) hiResOrder += path }

    // The open item, by path (null: the viewer is closed).
    private fun openPath(): String? = _state.value.let { st -> st.openIndex?.let { st.items.getOrNull(it)?.path } }

    // The big thumbnail asked for (bigThumbJob), and whose.
    private var bigThumbJob: Job? = null
    private var bigThumbFor: String? = null

    private fun stopBigThumb() {
        bigThumbJob?.cancel()
        bigThumbJob = null
        bigThumbFor = null
    }

    /**
     * The open item's thumbnail stays on screen ([path]): the full size
     * failed or can't be decoded, the video couldn't be fetched or played.
     * The grid's small tile scaled up would be all there is, so the big
     * thumbnail is asked for (GetThumbnails without small_thumbnails) - and
     * asked again like a grid's page, 1 s doubling to 10 s and at once on a
     * Wake, for as long as the viewer stays on it (the full size usually
     * failed because the connection did, and then so does this). The web's
     * MediaViewer does the same.
     */
    private fun thumbnailStays(path: String) {
        if (openPath() != path || path in _state.value.bigThumbs) return
        if (bigThumbFor == path && bigThumbJob?.isActive == true) return
        bigThumbJob?.cancel()
        bigThumbFor = path
        bigThumbJob = scope.launch {
            var failures = 0
            while (openPath() == path) {
                val got = askBigThumb(path)
                if (got != null) {
                    if (got.isNotEmpty() && openPath() == path) _state.update { st ->
                        // The open one's and the last few: about 100 KB each.
                        val kept = (st.bigThumbs - path).entries.toList().takeLast(BIG_THUMBS_KEPT - 1).associate { it.key to it.value }
                        st.copy(bigThumbs = kept + (path to got))
                    }
                    break
                }
                pause(retryWaitMs(++failures))
            }
            if (bigThumbFor == path) bigThumbFor = null
        }
    }

    /**
     * One ask for [path]'s big thumbnail: its bytes; empty when there is
     * none to have (the device left the path out, or a device before
     * release 80 has no GetThumbnails - the grid's own stays); null when
     * the ask failed and is worth repeating.
     */
    private suspend fun askBigThumb(path: String): ByteArray? {
        val resp = try {
            ask("thumbnail", OTCConnection.LIST_TIMEOUT_MS) { it.setReqGetThumbnails(GetThumbnails.newBuilder().addPaths(path).setSmallThumbnails(false)) }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            return null
        }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) {
            return resp.respListOfFiles.filesList.firstOrNull { it.path == path && it.hasContent() }?.content?.toByteArray() ?: ByteArray(0)
        }
        if (resp.isUnknownPayload()) return ByteArray(0)
        // The bridge answering for a device it can't reach, an error.
        return null
    }

    /** The open video ([path]) couldn't be fetched or played: its poster stays, saying so. */
    fun videoFailed(path: String) {
        if (openPath() != path) return
        _state.update { it.copy(unplayable = it.unplayable + path, videoUrl = null) }
        thumbnailStays(path)
    }

    /** The unplayable video's Try again: fetched and played again. */
    fun retryVideo() {
        val idx = _state.value.openIndex ?: return
        val path = openPath() ?: return
        _state.update { it.copy(unplayable = it.unplayable - path) }
        open(idx)
    }
    fun prev() { _state.value.openIndex?.let { if (it > 0) open(it - 1) } }
    fun next() { _state.value.openIndex?.let { if (it < _state.value.items.size - 1) open(it + 1) } }

    fun openInfo() {
        val idx = _state.value.openIndex ?: return
        val path = _state.value.items.getOrNull(idx)?.path ?: return
        _state.update { it.copy(infoOpen = true, infoLoading = true, infoData = null) }
        scope.launch {
            try {
                val resp = call { it.setReqGetFileInfo(GetFileInfo.newBuilder().setPath(path)) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_FILE_INFO) _state.update { it.copy(infoData = resp.respFileInfo) }
            } catch (_: Exception) {
            } finally {
                _state.update { it.copy(infoLoading = false) }
            }
        }
    }

    fun closeInfo() = _state.update { it.copy(infoOpen = false, infoData = null) }

    /** prefetch: a neighbour readied for the slide (readyNeighbours) - its image is fetched, a video is not started until opened. */
    private suspend fun fetchHiRes(index: Int, prefetch: Boolean = false) {
        val it = _state.value.items.getOrNull(index) ?: return
        if (it.mime.startsWith("video/")) { if (!prefetch && it.path !in _state.value.unplayable) fetchVideo(it); return }
        if (it.path in _state.value.hiResImages) { touchHiRes(it.path); readyNeighbours(it.path); return }
        // On its way (a neighbour readied, now opened): it readies the
        // neighbours itself when it lands, if it is still the open one.
        if (it.path in inFlightHiRes) return
        inFlightHiRes += it.path
        _state.update { s -> s.copy(hiResLoading = s.hiResLoading + it.path) }
        var got = false
        try {
            // Seen this session (FullPhotoCache): decoded from the phone's
            // copy, nothing asked of the device. One that doesn't decode is
            // forgotten and asked for as if it had never been kept.
            val kept = if (it.hash.isEmpty()) null else fullPhotos.loadFullPhoto(it.hash)
            if (kept != null) {
                got = showHiRes(it.path, kept)
                if (!got) fullPhotos.forgetFullPhoto(it.hash)
            }
            if (!got) {
                val asked = fullPhotos.fullPhotoScope()
                val resp = call { e -> e.setReqGetFile(GetFile.newBuilder().setPath(it.path)) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_FILE) {
                    val bytes = resp.respFile.content.toByteArray()
                    got = showHiRes(it.path, bytes)
                    // Kept for the next time, as the device sent it (a HEIC
                    // comes as JPEG), under the hash its answer carries - the
                    // file's now - rather than the one listed: a path whose
                    // content changed since isn't kept as the old content.
                    val hash = resp.respFile.hash.ifEmpty { it.hash }
                    if (got && hash.isNotEmpty()) fullPhotos.keepFullPhoto(hash, bytes, asked)
                }
            }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
        } finally {
            inFlightHiRes -= it.path
            _state.update { s -> s.copy(hiResLoading = s.hiResLoading - it.path) }
        }
        // Failed, or a format the phone can't decode: the thumbnail stays,
        // the big one if it is the open photo (a neighbour's full size is
        // asked again when it is swiped to). Shown: if it is the open one,
        // its neighbours are next.
        if (!got) thumbnailStays(it.path) else readyNeighbours(it.path)
    }

    // A full-size photo's [bytes] decoded off the main thread and kept with
    // the viewer's images while it is open; false when they don't decode.
    private suspend fun showHiRes(path: String, bytes: ByteArray): Boolean {
        val bmp = withContext(Dispatchers.Default) { decodeBitmap(bytes) } ?: return false
        if (_state.value.openIndex != null) cacheHiRes(path, bmp)
        return true
    }

    /** Issue #110: streamed when the device offers a URL, downloaded otherwise. */
    private suspend fun fetchVideo(it: Item) {
        // Still on this video? Stepping on before a slow stream URL or
        // download came back used to start the previous video over the next one.
        fun stillOpen() = openPath() == it.path
        MediaStream.url(forPath = it.path)?.let { url -> if (stillOpen()) _state.update { s -> s.copy(videoUrl = url) }; return }
        var got = false
        try {
            // In pieces (a video can be far bigger than the heap), stopped as
            // soon as the viewer moves on; named once its mime is known.
            val uuid = UUID.randomUUID().toString()
            val raw = File(OTCApp.instance.cacheDir, uuid)
            val meta = ChunkedDownload.download(it.path, "", raw, keepGoing = { stillOpen() })
            if (meta.size == 0L) { raw.delete(); return }
            val ext = when (meta.mime.lowercase()) {
                "video/quicktime" -> "mov"
                "video/mp4", "video/x-m4v" -> "mp4"
                "video/x-matroska" -> "mkv"
                "video/3gpp" -> "3gp"
                else -> it.path.substringAfterLast('.', "mp4").lowercase()
            }
            val tmp = File(OTCApp.instance.cacheDir, "$uuid.$ext")
            if (!stillOpen() || !raw.renameTo(tmp)) { raw.delete(); return }
            got = true
            _state.update { s -> s.copy(videoUrl = tmp.toURI().toString()) }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
        } finally {
            // Neither streamed nor downloaded: its poster stays (the big
            // one), saying so, with Try again.
            if (!got && stillOpen()) videoFailed(it.path)
        }
    }

    suspend fun currentImage(): Bitmap? {
        val st = _state.value
        val idx = st.openIndex ?: return null
        return imageOf(st.items.getOrNull(idx) ?: return null)
    }

    // The best picture of [item] the phone has: its full size, its big
    // thumbnail, its tile.
    private suspend fun imageOf(item: Item): Bitmap? {
        val st = _state.value
        st.hiResImages[item.path]?.let { return it }
        st.bigThumbs[item.path]?.let { b -> withContext(Dispatchers.Default) { decodeBitmap(b) } }?.let { return it }
        return st.thumbKeyOf(item)?.let { thumbs.load(it) }?.let { decodeBitmap(it) } ?: item.preview
    }

    /** Issue #9: write the loaded image to a temp file and hand it to the share sheet. */
    suspend fun shareCurrentPhoto(context: Context) {
        val img = currentImage() ?: run { alert(S.appPhotosNoticeNotLoaded()); return }
        try {
            val tmp = File(context.cacheDir, "${UUID.randomUUID()}.jpg")
            withContext(Dispatchers.IO) { tmp.outputStream().use { img.compress(Bitmap.CompressFormat.JPEG, 90, it) } }
            Share.file(context, tmp, "image/jpeg")
        } catch (e: Exception) { alert(S.appPhotosNoticeShareFailed("${e.message}")) }
    }

    /**
     * Issue #9, #201: the open photo or video saved to the phone's own
     * library (MediaStore, no permission needed on API 29+) as the original
     * file - its bytes as the device keeps them, in 4 MiB ReadFile pieces
     * (ChunkedDownload) - so it keeps its date, place and content: the
     * photo sync then finds the same hash and Images still shows it once,
     * at its own date. DATE_TAKEN is the photo's date, as Images shows it.
     * The Save button spins meanwhile (State.saving); the view model's own
     * work, so closing the viewer doesn't cut a big video short. Only a
     * type the phone's library refuses is saved as the picture shown, as
     * a JPEG, as before.
     */
    fun saveToPhotos(context: Context) {
        val st = _state.value
        if (st.saving) return
        val item = st.openIndex?.let { st.items.getOrNull(it) } ?: return
        val app = context.applicationContext
        _state.update { it.copy(saving = true) }
        scope.launch {
            val tmp = File(app.cacheDir, "save-${UUID.randomUUID()}")
            try {
                val meta = ChunkedDownload.download(item.path, "", tmp)
                val mime = meta.mime.ifEmpty { item.mime }
                val name = item.path.substringAfterLast('/').ifEmpty { "otc-${System.currentTimeMillis()}" }
                val saved = withContext(Dispatchers.IO) {
                    try {
                        insertIntoLibrary(app, name, mime, item.created) { out -> tmp.inputStream().use { it.copyTo(out) } }
                        true
                    } catch (_: UnsupportedType) { false }
                }
                if (!saved) saveShownImage(app, item)
                else alert(S.appPhotosNoticeSaved())
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                alert(saveFailed(e))
            } finally {
                tmp.delete()
                _state.update { it.copy(saving = false) }
            }
        }
    }

    // The library won't take a file of this type.
    private class UnsupportedType : Exception()

    /**
     * A new entry in the phone's library - hidden (IS_PENDING) until
     * [write] has written all of it, removed if that fails - named [name],
     * of [mime], taken at [takenMs], in Pictures (or Movies) / Off The Cloud.
     */
    private fun insertIntoLibrary(context: Context, name: String, mime: String, takenMs: Long?, write: (java.io.OutputStream) -> Unit) {
        val video = mime.startsWith("video/")
        val collection = if (video) MediaStore.Video.Media.getContentUri(MediaStore.VOLUME_EXTERNAL_PRIMARY)
            else MediaStore.Images.Media.getContentUri(MediaStore.VOLUME_EXTERNAL_PRIMARY)
        val values = ContentValues().apply {
            put(MediaStore.MediaColumns.DISPLAY_NAME, name)
            put(MediaStore.MediaColumns.MIME_TYPE, mime)
            put(MediaStore.MediaColumns.RELATIVE_PATH, saveFolder(mime))
            put(MediaStore.MediaColumns.IS_PENDING, 1)
            if (takenMs != null) put(MediaStore.MediaColumns.DATE_TAKEN, takenMs)
        }
        val resolver = context.contentResolver
        val uri = try {
            resolver.insert(collection, values)
        } catch (_: IllegalArgumentException) {
            throw UnsupportedType()
        } ?: throw TextIOException(S.appFilesUnexpectedResponse())
        try {
            // No entry, or no way to write it, and no word why: the
            // library answered other than asked.
            val out = resolver.openOutputStream(uri) ?: throw TextIOException(S.appFilesUnexpectedResponse())
            out.use(write)
            resolver.update(uri, ContentValues().apply { put(MediaStore.MediaColumns.IS_PENDING, 0) }, null, null)
        } catch (e: Exception) {
            resolver.delete(uri, null, null)
            throw e
        }
    }

    // A type the library refuses: the picture shown, as a JPEG (the save
    // before #201) - [item]'s, whichever photo the viewer shows by now.
    private suspend fun saveShownImage(context: Context, item: Item) {
        val img = if (item.mime.startsWith("video/")) null else imageOf(item)
        if (img == null) { alert(S.appPhotosNoticeNotLoaded()); return }
        withContext(Dispatchers.IO) {
            val stem = item.path.substringAfterLast('/').substringBeforeLast('.').ifEmpty { "otc-${System.currentTimeMillis()}" }
            insertIntoLibrary(context, "$stem.jpg", "image/jpeg", item.created) { img.compress(Bitmap.CompressFormat.JPEG, 92, it) }
        }
        alert(S.appPhotosNoticeSaved())
    }

    /**
     * The viewer's Delete. Issue #201: [everyCopy] - "Delete all N copies",
     * for a photo the device holds at several paths. A refusal (a copy in
     * an upload-only folder, content changed since it was shown) deletes
     * nothing: the device's words say why, and the photo stays; changed,
     * the photos are asked for again.
     */
    fun deleteCurrentPhoto(everyCopy: Boolean = false) = scope.launch {
        val idx = _state.value.openIndex ?: return@launch
        val item = _state.value.items.getOrNull(idx) ?: return@launch
        try {
            val resp = call { it.setReqDelFile(delFileFor(item, everyCopy)) }
            if (resp.error) {
                alert(S.appPhotosNoticeDeleteFailed(resp.errorMessage))
                if (deleteChanged(resp)) contentChanged()
                return@launch
            }
        } catch (e: Exception) { alert(S.appPhotosNoticeDeleteFailed("${e.message}")); return@launch }
        val at = _state.value.items.indexOfFirst { it.id == item.id }
        removeDeleted(setOf(item.path))
        if (_state.value.items.isEmpty()) closeModal() else open(minOf(if (at >= 0) at else idx, _state.value.items.size - 1))
        onDeleted?.invoke()
        countsChanged()
    }

    /**
     * Issue #201: a path held other content than its tile showed, so
     * nothing was deleted: the viewer closes and Images is asked for again
     * from where its search started - the jump's month after a jump (its
     * newer photos above it start over too), else the newest photo - with
     * its months and the open collection's count (the web's reloadGrid).
     * The selection stays. In Files, its folder is listed again.
     */
    private fun contentChanged() {
        if (_state.value.openIndex != null) closeModal()
        if (fixedList) { onDeleted?.invoke(); return }
        // The map's places hold what was deleted.
        places.libraryChanged()
        val jump = jumpBefore
        if (jump != null) {
            searchJob?.cancel()
            searchJob = scope.launch { performJump(jump) }
            reloadDateBuckets()
        } else restartSearch()
        if (_state.value.activeGroup != null) scope.launch { loadGroups() }
    }

    /**
     * Photos the device deleted ([paths]): off the grid and the selection,
     * their thumbnails off the phone, and each one's search (issue #194)
     * counts one photo fewer - one tile per photo since #201. [keepThumbs]:
     * photos still on the device, only out of the open collection.
     */
    private suspend fun removeDeleted(paths: Set<String>, keepThumbs: Boolean = false) {
        val gone = _state.value.items.filter { it.path in paths }
        for (i in gone) {
            if (i.hash.isNotEmpty() && !keepThumbs) thumbs.discard(i.hash)
            if (i.up) upHave = maxOf(0, upHave - 1) else downHave = maxOf(0, downHave - 1)
        }
        // Picks off the grid: only their thumbnails (no search on now counted them).
        val away = pickedAway
        if (!keepThumbs) for ((p, i) in away) if (p in paths && i.hash.isNotEmpty()) thumbs.discard(i.hash)
        pickedAway = away - paths
        _state.update { st -> st.copy(items = st.items.filter { it.path !in paths }, selected = st.selected - paths) }
        // A drag begun meanwhile doesn't bring them back (issue #200).
        dragSelection?.forget { it in paths }
    }

    // Photos deleted from Images: the months' counts (the headers, the
    // scrubber) and the open collection's count follow.
    private fun countsChanged() {
        if (fixedList) return
        places.libraryChanged()
        refillIfEmptied()
        reloadDateBuckets()
        if (_state.value.activeGroup != null) scope.launch { loadGroups() }
    }

    fun toggleSelect(path: String) {
        if (path in _state.value.selected) pickedAway = pickedAway - path
        _state.update { st -> st.copy(selected = if (path in st.selected) st.selected - path else st.selected + path) }
    }

    /**
     * Issue #201: picked photos the grid no longer holds - a jump or a
     * reload replaced it while the selection stayed - as they were last
     * shown (keptPicks): what their delete sends (the content they were
     * shown with, every_copy by their copies) and what the copies
     * question counts, until a page shows them again. A photo that
     * changed meanwhile is refused again ("changed"), never deleted
     * unseen - as iOS's pickedAway and the web.
     */
    @Volatile private var pickedAway: Map<String, Item> = emptyMap()

    // The grid is about to be replaced and the selection stays.
    private fun keepPicks() {
        val st = _state.value
        pickedAway = keptPicks(pickedAway, st.items, st.selected)
    }

    /** The picked photos: as the grid shows them, else as last shown - what Delete asks about and sends. */
    fun pickedItems(): List<Item> {
        val st = _state.value
        return pickedByPath(st.items, pickedAway, st.selected).values.toList()
    }

    // Issue #200: the drag to select under way (DragSelect.kt), from a long press.
    private var dragSelection: DragSelection<String>? = null

    // The photos' paths in the grid's order, without copying them.
    private fun pathsOf(items: List<Item>): List<String> = items.keysView { it.path }

    /** A long press on [path]: it is selected, and a drag from it starts. */
    fun beginDragSelect(path: String) {
        val st = _state.value
        val sel = DragSelection(st.selected.toList(), path)
        dragSelection = sel
        sel.apply(pathsOf(st.items), path)?.let { next -> _state.update { it.copy(selected = next.toSet()) } }
    }

    /**
     * The drag reached [to]: every photo from the pressed one to it is
     * selected, besides what was before. False once the drag is over (the
     * pressed photo went away): the selection stays as it was then.
     */
    fun dragSelect(to: String): Boolean {
        val sel = dragSelection ?: return false
        sel.apply(pathsOf(_state.value.items), to)?.let { next -> _state.update { it.copy(selected = next.toSet()) } }
        return !sel.over
    }

    /** Whether the drag goes on: not once the pressed photo has gone from the grid. */
    fun dragGoesOn(): Boolean = dragSelection?.goesOn(pathsOf(_state.value.items)) ?: false

    /** The finger lifted: the selection stays as the drag left it. */
    fun endDragSelect() { dragSelection = null }

    /** Nothing selected any more (the pill's ×, Back, or leaving for People or Collections). */
    fun clearSelection() {
        dragSelection = null // a drag under way ends with it (Back mid-drag)
        pickedAway = emptyMap()
        if (_state.value.selected.isNotEmpty()) _state.update { it.copy(selected = emptySet()) }
    }

    /**
     * The selection's Delete. Issue #201: [everyCopy] - "Delete all N
     * copies" (one photo) or "Delete every copy" (several): every_copy for
     * those the device holds at several paths, a plain delete for the rest.
     * Each sends the content its tile showed. A refusal deletes nothing of
     * that photo: its tile stays, picked, and once all are done the alert
     * says why (deleteRefusalNotes). One whose content changed has the
     * photos asked for again - at the jump after one - with the months and
     * the open collection's count; else what was deleted updates them.
     */
    fun deleteSelected(everyCopy: Boolean = false) = scope.launch {
        val st = _state.value
        val paths = st.selected.toList()
        if (paths.isEmpty()) return@launch
        // A pick no longer on the grid (a jump or a reload since): as it was last shown.
        val shown = pickedByPath(st.items, pickedAway, st.selected)
        val deleted = mutableSetOf<String>()
        val refusals = mutableListOf<DeleteRefusal>()
        for (p in paths) {
            // Never shown (nothing kept of it): its path alone.
            val req = shown[p]?.let { delFileFor(it, everyCopy) } ?: DelFile.newBuilder().setPath(p).build()
            try {
                val resp = call { it.setReqDelFile(req) }
                if (resp.error) {
                    refusals += DeleteRefusal(resp.errorCode, resp.errorMessage)
                    continue
                }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) { refusals += DeleteRefusal("", "${e.message}"); continue }
            deleted += p
        }
        if (deleted.isNotEmpty()) removeDeleted(deleted)
        val notes = deleteRefusalNotes(refusals)
        if (notes.isNotEmpty()) _state.update { it.copy(alert = notes.first(), alertMore = notes.drop(1)) }
        // The grid again (it reloads the counts too), or what was deleted
        // off the counts.
        if (refusals.any { it.code == DELETE_CHANGED }) contentChanged()
        else if (deleted.isNotEmpty()) countsChanged()
    }

    private suspend fun shareLink(): String? {
        val paths = _state.value.selected.toList()
        return try {
            val r = call { it.setReqShareFilesLink(ShareFilesLink.newBuilder().addAllPaths(paths)) }
            if (r.payloadCase == RespEnvelope.PayloadCase.RESP_SHARE_LINK) r.respShareLink.link else null
        } catch (e: Exception) { null }
    }

    fun shareSelected(context: Context) = scope.launch {
        _state.update { it.copy(preparing = SelectionActionTask.SHARE) }
        try { shareLink()?.let { Share.link(context, it) } ?: alert(S.appPhotosNoticeNoShareLink()) }
        finally { _state.update { it.copy(preparing = null) } }
    }

    fun downloadZip(context: Context) = scope.launch {
        _state.update { it.copy(preparing = SelectionActionTask.DOWNLOAD) }
        try { shareLink()?.let { Share.openInBrowser(context, it) } ?: alert(S.appPhotosNoticeNoDownloadLink()) }
        finally { _state.update { it.copy(preparing = null) } }
    }

    fun alert(m: UiText) = _state.update { it.copy(alert = m, alertMore = emptyList()) }
    fun dismissAlert() = _state.update { it.copy(alert = null, alertMore = emptyList()) }

    // Save failed, and why: our words, the system's or the device's - and
    // where none came, that the answer wasn't what was asked for.
    private fun saveFailed(e: Exception): UiText {
        val why = when (e) {
            is TextIOException -> e.text
            is WSClient.RequestTimeout -> e.text
            else -> e.message?.takeIf { it.isNotEmpty() }?.let { UiText.Raw(it) } ?: S.appFilesUnexpectedResponse()
        }
        // app.photos.notice.save_failed, with a detail that is ours to word.
        return UiText.Res(R.string.app_photos_notice_save_failed, listOf(why))
    }

    companion object {
        // While the scrubber is dragged, a month that hasn't loaded shows
        // this many grey tiles at most: enough to read as "a lot", not
        // thousands of views (the web's cMaxPlaceholders).
        const val MAX_PLACEHOLDERS = 300

        /** Whose date buckets: the people, the open collection and the map's area (tags never change them). */
        fun bucketsKeyOf(people: List<String>, groupId: String, area: PlaceArea? = null) =
            "${people.joinToString(",")}|$groupId" + (area?.let { "|${it.south},${it.west},${it.north},${it.east}" } ?: "")
    }
}

/**
 * Runs [attempt] until it succeeds (true), waiting [firstMs] after the first
 * failure and twice as long after each next one, up to [maxMs] - the web's
 * usePageRetry - and only while [stillWanted] after each wait. [waitFirst]:
 * the first attempt failed already (the caller's), so the wait comes first.
 * True once an attempt succeeded, false when it was no longer wanted.
 */
internal suspend fun retryWithBackoff(
    firstMs: Long = 1_000, maxMs: Long = 10_000,
    stillWanted: () -> Boolean = { true },
    sleep: suspend (Long) -> Unit = { delay(it) },
    waitFirst: Boolean = false,
    attempt: suspend () -> Boolean,
): Boolean {
    var wait = firstMs
    var tryNow = !waitFirst
    while (true) {
        if (tryNow && attempt()) return true
        tryNow = true
        sleep(wait)
        if (!stillWanted()) return false
        wait = minOf(wait * 2, maxMs)
    }
}

/**
 * Whether a page that went on with a jump's search (by its token) came from
 * a search the device started again: one that no longer holds the token
 * (unused for five minutes, or a restart) searches again without the jump's
 * cutoff, from the newest photo. A held token comes back as it was sent;
 * the last page of either has none, and then the photos tell - newer than
 * the cutoff, or already on the screen. [files]: each one's date (epoch ms,
 * null without one) and path. The web's lostCutoff.
 */
fun lostCutoff(files: List<Pair<Long?, String>>, token: String, sent: String, cutoffMs: Long, shown: Set<String>): Boolean {
    if (token.isNotEmpty()) return token != sent
    if (files.any { (created, _) -> created != null && created > cutoffMs }) return true
    return files.any { (_, path) -> path in shown }
}

/** An instant (epoch ms) as the protocol's Timestamp. */
internal fun timestampOf(ms: Long): Timestamp =
    Timestamp.newBuilder().setSeconds(Math.floorDiv(ms, 1000L)).setNanos(Math.floorMod(ms, 1000L).toInt() * 1_000_000).build()

// Issue #194: once the person has scrolled up after a jump, a row with a
// photo among the first this many drawn asks for newer ones (the downward
// search's 12 from the end, iOS's and the web's 12 from the top).
internal const val UP_NEAR = 12

/**
 * [page]'s photos not on the grid already ([shown]) - by content where its
 * hash is known (issue #194: between searches a photo's oldest copy can
 * change, and it can then come from either half), else as the same entry.
 */
internal fun withoutShown(page: List<PhotoGalleryViewModel.Item>, shown: List<PhotoGalleryViewModel.Item>): List<PhotoGalleryViewModel.Item> {
    val ids = shown.mapTo(HashSet()) { it.id }
    val hashes = shown.mapNotNullTo(HashSet()) { it.hash.ifEmpty { null } }
    // Nor twice from one page.
    return page.filter { it.id !in ids && (it.hash.isEmpty() || it.hash !in hashes) }.distinctBy { it.hash.ifEmpty { it.id } }
}

/**
 * Issue #201: what deleting [items] from Images asks. PLAIN - the usual
 * confirmation (no copies, or a device that doesn't count them); SINGLE -
 * one photo held at several paths: only this copy, or all of them;
 * SEVERAL - a selection where some have copies: only these, or every copy.
 */
enum class CopiesAsk { PLAIN, SINGLE, SEVERAL }

fun copiesAsk(items: List<PhotoGalleryViewModel.Item>, count: Int = items.size): CopiesAsk = when {
    // [count]: how many are being deleted - a selection can hold picks no
    // longer on the grid (another search since), of which nothing is known.
    count == 1 && items.size == 1 && (items[0].copies ?: 0) > 1 -> CopiesAsk.SINGLE
    count > 1 && items.any { (it.copies ?: 0) > 1 } -> CopiesAsk.SEVERAL
    else -> CopiesAsk.PLAIN
}

/** Issue #201: how many of [items] have other copies on the device (the several-text's count). */
fun withCopies(items: List<PhotoGalleryViewModel.Item>): Int = items.count { (it.copies ?: 0) > 1 }

/**
 * Issue #201: the grid ([items]) is about to be replaced while the
 * selection ([selected]) stays: the picked photos it shows, as shown, join
 * those kept from before ([away]) that are still picked - none once
 * nothing is. Without their picture (Files' viewer only has one).
 */
internal fun keptPicks(
    away: Map<String, PhotoGalleryViewModel.Item>, items: List<PhotoGalleryViewModel.Item>, selected: Set<String>,
): Map<String, PhotoGalleryViewModel.Item> {
    if (selected.isEmpty()) return emptyMap()
    val kept = away.filterTo(LinkedHashMap()) { it.key in selected }
    for (i in items) if (i.path in selected) kept[i.path] = if (i.preview == null) i else i.copy(preview = null)
    return kept
}

/**
 * Issue #201: the picked photos ([selected]) by path - as the grid
 * ([items]) shows them (a page that brought one back: its content now),
 * else as they were last shown ([away], keptPicks). A pick in neither is
 * left out.
 */
internal fun pickedByPath(
    items: List<PhotoGalleryViewModel.Item>, away: Map<String, PhotoGalleryViewModel.Item>, selected: Set<String>,
): Map<String, PhotoGalleryViewModel.Item> {
    val picked = LinkedHashMap<String, PhotoGalleryViewModel.Item>()
    if (selected.isEmpty()) return picked
    for (i in items) if (i.path in selected) picked.putIfAbsent(i.path, i)
    for ((p, i) in away) if (p in selected) picked.putIfAbsent(p, i)
    return picked
}

/**
 * Issue #201: the delete for [item]: its path, with the content shown for
 * it (DelFile.hash: the device refuses it as "changed" if the path holds
 * other content now), and [everyCopy] only where the device counted copies
 * - a device that doesn't would delete just the path while the dialog said all.
 */
fun delFileFor(item: PhotoGalleryViewModel.Item, everyCopy: Boolean): DelFile {
    val b = DelFile.newBuilder().setPath(item.path)
    if (item.hash.isNotEmpty()) b.hash = item.hash
    if (everyCopy && (item.copies ?: 0) > 1) b.everyCopy = true
    return b.build()
}

/** Issue #201: the path holds other content than the tile showed: the grid is asked for again. */
internal fun deleteChanged(resp: RespEnvelope): Boolean = resp.error && resp.errorCode == DELETE_CHANGED

// Issue #201: DelFile's refusals - the path holds other content than the
// tile showed; a copy is in an upload-only folder (every_copy).
internal const val DELETE_CHANGED = "changed"
internal const val DELETE_COPY_UPLOAD_ONLY = "copy_upload_only"

/**
 * Issue #201: a photo of the selection's Delete that wasn't deleted: the
 * device's error_code ("" when no answer came) and its words (or the
 * phone's, when none came).
 */
data class DeleteRefusal(val code: String, val said: String)

/**
 * Issue #201: what the selection's Delete says about the photos it
 * couldn't delete ([refusals]), as the web's: one - its own words, the
 * device's message as it came ("Delete failed: …"); two or more - summed
 * up per kind: those whose content changed since they were shown and
 * those with a copy in an upload-only folder, each counted
 * (app.photos.notice.delete_changed, delete_copy_upload_only); any other
 * refusal in the device's words, each text once (as iOS's keptText). One
 * paragraph each, in that order; none when everything was deleted.
 */
fun deleteRefusalNotes(refusals: List<DeleteRefusal>): List<UiText> {
    if (refusals.isEmpty()) return emptyList()
    if (refusals.size == 1) return listOf(S.appPhotosNoticeDeleteFailed(refusals[0].said))
    val changed = refusals.count { it.code == DELETE_CHANGED }
    val copies = refusals.count { it.code == DELETE_COPY_UPLOAD_ONLY }
    val others = refusals.filter { it.code != DELETE_CHANGED && it.code != DELETE_COPY_UPLOAD_ONLY }.map { it.said }.distinct()
    return buildList {
        if (changed > 0) add(S.appPhotosNoticeDeleteChanged(changed))
        if (copies > 0) add(S.appPhotosNoticeDeleteCopyUploadOnly(copies))
        for (said in others) add(S.appPhotosNoticeDeleteFailed(said))
    }
}

/**
 * Issue #201: where the viewer's Save puts an original of [mime] in the
 * phone's own library - a video with the videos, as the camera's are.
 */
internal fun saveFolder(mime: String): String = if (mime.startsWith("video/")) "Movies/Off The Cloud" else "Pictures/Off The Cloud"

/**
 * What Images shows in the grid's place (the web's PhotoGallery body):
 * GREY - the scrubber's month as grey tiles (dragged, or its jump on its
 * way); SKELETON - grey tiles while the first page is on its way (from the
 * moment a search starts: never a blank page); PROBLEM - the first page
 * failed and is being asked for again ("Couldn't load your photos" with
 * Try again: it stays up through the retries rather than flipping back to
 * grey tiles); EMPTY - nothing to show (emptyKind says which); PHOTOS - the
 * grid, a later page's failure at its end.
 */
enum class GalleryBody { GREY, SKELETON, PROBLEM, EMPTY, PHOTOS }

fun galleryBody(st: PhotoGalleryViewModel.State): GalleryBody = when {
    st.placeholderCount != null -> GalleryBody.GREY
    st.items.isNotEmpty() -> GalleryBody.PHOTOS
    st.pageProblem != null -> GalleryBody.PROBLEM
    st.loading || !st.endReached -> GalleryBody.SKELETON
    else -> GalleryBody.EMPTY
}

/** A person's name as shown: spaces tidied, "" for none (or no person). */
internal fun personName(p: Person?): String = p?.name?.replace(Regex("\\s+"), " ")?.trim() ?: ""

/** RemoveFromPerson: the photos with [hashes] off [personId], to [toPersonId] (null: matched to no one). */
internal fun removeFromPersonRequest(personId: String, hashes: List<String>, toPersonId: String?): RemoveFromPerson =
    RemoveFromPerson.newBuilder().setPersonId(personId).addAllHashes(hashes).also { if (toPersonId != null) it.toPersonId = toPersonId }.build()

/** Not this person's question, for [count] photos off [person] (null: not in the people's list). */
fun notThisPersonTitle(person: Person?, count: Int): UiText {
    val name = personName(person)
    return if (name.isEmpty()) S.appPhotosPersonRemoveTitleUnnamed(count) else S.appPhotosPersonRemoveTitle(name, count)
}

/** The note once [count] photos left [from]: moved to [to] (named, or not), or taken off them. */
fun personTakenOffNote(count: Int, from: Person?, to: Person?): UiText {
    if (to != null) {
        val name = personName(to)
        return if (name.isEmpty()) S.appPhotosPersonMovedUnnamed(count) else S.appPhotosPersonMoved(count, name)
    }
    val name = personName(from)
    return if (name.isEmpty()) S.appPhotosPersonRemovedUnnamed(count) else S.appPhotosPersonRemoved(count, name)
}

/** Why there is nothing to show (the web's three empty states). */
enum class EmptyKind { COLLECTION, NO_MATCH, NO_PHOTOS }

fun emptyKind(st: PhotoGalleryViewModel.State): EmptyKind = when {
    st.chips.isNotEmpty() || st.selectedPeople.isNotEmpty() || st.activeArea != null -> EmptyKind.NO_MATCH
    st.activeGroup != null -> EmptyKind.COLLECTION
    else -> EmptyKind.NO_PHOTOS
}
