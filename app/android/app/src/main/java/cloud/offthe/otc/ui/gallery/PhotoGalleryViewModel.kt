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
import cloud.offthe.otc.data.ImagesChanged
import cloud.offthe.otc.net.ChunkedDownload
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
import cloud.offthe.otc.proto.RenameImageGroup
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.ReqPhotoDateBuckets
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.proto.ShareFilesLink
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.SelectionActionTask
import cloud.offthe.otc.ui.common.backOnline
import cloud.offthe.otc.ui.common.loadProblem
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.decodeBitmap
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
import java.util.UUID

// SearchPhotos.limit for the request that starts a photo search (no
// token): a small first page paints quickly over a slow upload. Pages that
// continue the token send none and get the device's own size; a device
// before release 97 ignores it and answers its default, 30.
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
    // Where its work runs: the view model's own scope, a test's in tests.
    scope: CoroutineScope? = null,
) : ViewModel(), PeopleStore {
    private val scope: CoroutineScope = scope ?: viewModelScope

    // thumbKey: the grid tile's (small) thumbnail in ThumbStore
    // (ThumbStore.tileKey), null when the device sent none. preview: an already decoded placeholder
    // (the Files grid's thumbnail), used when there are no thumb bytes.
    // month: "2024-03" in the phone's time zone, null without a date
    // (monthOf) - the Images grid's month titles. created: the photo's
    // date (epoch ms, null without one), for the tile's label (tileLabel).
    data class Item(
        val id: String, val path: String, val mime: String, val size: Long, val thumbKey: String?,
        val preview: Bitmap? = null, val month: String? = null, val created: Long? = null,
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
        val alert: String? = null,
        val preparing: SelectionActionTask? = null,
        val selected: Set<String> = emptySet(),
        val infoOpen: Boolean = false,
        val infoLoading: Boolean = false,
        val infoData: FileExifInfo? = null,
    ) {
        /** A tag search is sorted by how well photos match: no month titles, no scrubber (the web's isDateOrdered). */
        val dateOrdered get() = chips.isEmpty()
        /** Whose date buckets this filter needs: null when it has none (a tag search). */
        val bucketsKeyNow: String? get() = if (dateOrdered) bucketsKeyOf(selectedPeople, activeGroup?.id ?: "") else null
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
    }

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state
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

    /** Files section: view [items] (a folder's photos and videos, in its order) starting at [startAt]. */
    fun showFiles(items: List<Item>, startAt: Int, onDeleted: () -> Unit) {
        fixedList = true
        this.onDeleted = onDeleted
        searchJob?.cancel()
        searchGeneration += 1
        stopPageRetry()
        token = null
        _state.update { it.copy(items = items, endReached = true, loading = false, selected = emptySet(), pageProblem = null, slowFirstPage = false) }
        open(startAt)
    }

    // The first page goes out at once: the tags and people come alongside,
    // not ahead of it (a slow or lost answer to either used to hold the
    // photos back, on a blank page).
    fun onAppearInitial() = scope.launch {
        watchFaceRecognition()
        watchImagesChanged()
        watchWake()
        listsAsked = true
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
        if (st.chips.isEmpty() && st.selectedPeople.isEmpty() && st.activeGroup == null) return
        _state.update { it.copy(chips = emptyList(), selectedPeople = emptyList(), activeGroup = null) }
        restartSearch()
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
        retryWithBackoff(stillWanted = { gen == bucketGeneration && _state.value.bucketsKeyNow == key }, sleep = pause) {
            loadDateBucketsOnce(people, group, key, gen)
        }
    }

    /** One ask for the buckets: false when it failed and is worth asking again. */
    private suspend fun loadDateBucketsOnce(people: List<String>, group: String, key: String, gen: Int): Boolean {
        val resp = try {
            ask("buckets", OTCConnection.LIST_TIMEOUT_MS) {
                it.setReqPhotoDateBuckets(ReqPhotoDateBuckets.newBuilder().addAllPersonIds(people).setGroupId(group).setIncludeVideos(true))
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
        _state.update { it.copy(loading = false, endReached = false, items = emptyList(), pageProblem = null, slowFirstPage = false) }
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
                return true
            }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {}
        return false
    }

    /** A collection's photos: a new search, the tags and people gone (the web's openGroup). */
    fun openGroup(g: ImageGroup) { _state.update { it.copy(chips = emptyList(), selectedPeople = emptyList(), activeGroup = g) }; restartSearch() }
    fun leaveGroup() { _state.update { it.copy(activeGroup = null) }; restartSearch() }

    suspend fun renameActiveGroup(newName: String) {
        val g = _state.value.activeGroup ?: return
        val name = newName.trim()
        if (name.isEmpty() || name == g.name) return
        val ok = try {
            val r = call { it.setReqRenameImageGroup(RenameImageGroup.newBuilder().setId(g.id).setName(name)) }
            r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok
        } catch (e: Exception) { false }
        if (!ok) { alert("Could not rename the collection."); return }
        val renamed = g.toBuilder().setName(name).build()
        _state.update { st -> st.copy(activeGroup = renamed, groups = st.groups.map { if (it.id == g.id) renamed else it }) }
    }

    suspend fun deleteActiveGroup() {
        val g = _state.value.activeGroup ?: return
        val ok = try {
            val r = call { it.setReqDeleteImageGroup(DeleteImageGroup.newBuilder().setId(g.id)) }
            r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok
        } catch (e: Exception) { false }
        if (!ok) { alert("Could not delete the collection."); return }
        _state.update { st -> st.copy(groups = st.groups.filter { it.id != g.id }) }
        leaveGroup()
    }

    suspend fun createGroupFromSelection(rawName: String) {
        val name = rawName.trim()
        if (name.isEmpty()) return
        val paths = _state.value.selected.toList()
        try {
            val r = call { it.setReqCreateImageGroup(CreateImageGroup.newBuilder().setName(name).addAllPaths(paths).setSmallThumbnails(true)) }
            if (r.payloadCase != RespEnvelope.PayloadCase.RESP_IMAGE_GROUP) { alert("Could not create the collection."); return }
            _state.update { st -> st.copy(groups = listOf(r.respImageGroup.group) + st.groups, selected = emptySet()) }
            alert("Collection \"$name\" created.")
        } catch (e: Exception) { alert("Could not create the collection.") }
    }

    suspend fun addSelectionToGroup(g: ImageGroup) {
        val paths = _state.value.selected.toList()
        val ok = try {
            val r = call { it.setReqAddToImageGroup(AddToImageGroup.newBuilder().setGroupId(g.id).addAllPaths(paths)) }
            r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok
        } catch (e: Exception) { false }
        if (!ok) { alert("Could not add to the collection."); return }
        _state.update { it.copy(selected = emptySet()) }
        loadGroups()
        if (_state.value.activeGroup?.id == g.id) restartSearch()
        alert("Added to \"${g.name}\".")
    }

    fun togglePerson(id: String) {
        _state.update { st -> st.copy(selectedPeople = if (id in st.selectedPeople) st.selectedPeople - id else st.selectedPeople + id) }
        restartSearch()
    }

    /** One person's photos, from the People page: a new search (the web's showPerson). */
    fun showPerson(id: String) {
        _state.update { it.copy(chips = emptyList(), selectedPeople = listOf(id), activeGroup = null) }
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
        // The selection stays, as the web's does across a filter change
        // (leaving for People or Collections clears it, as the web's page
        // change does: PhotoGalleryView).
        _state.update {
            it.copy(
                loading = false, endReached = false, items = emptyList(), pageProblem = null, slowFirstPage = false,
                scrubFrac = null, placeholderCount = null, placeholderMonth = null, searchesStarted = it.searchesStarted + 1,
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
            val requestToken = overrideToken ?: token ?: ""
            val have = st0.items.size
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
                        // an older device sends big ones).
                        val sp = SearchPhotos.newBuilder().addAllTags(tags).addAllPersonIds(people).setGroupId(group).setIncludeVideos(true).setToken(tok).setHave(have)
                            .setSmallThumbnails(true)
                        // A search starting here (opening, a filter, the
                        // scrubber's jump) gets a small first page; scrolling
                        // on, the device's own size.
                        if (tok.isEmpty()) sp.limit = FIRST_PHOTO_PAGE_LIMIT
                        if (cutoffMs != null) sp.before = Timestamp.newBuilder().setSeconds(Math.floorDiv(cutoffMs, 1000L)).setNanos(Math.floorMod(cutoffMs, 1000L).toInt() * 1_000_000).build()
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
            val withThumb = lof.filesList.filter { it.hasContent() }.map { f -> ThumbStore.tileKey(f.path, f.hash, f.byteSize) to f.content.toByteArray() }
            if (withThumb.isNotEmpty()) ThumbStore.putAll(withThumb)
            if (mine != searchGeneration) return PageResult.SKIPPED
            // Each photo's month, once, here: the grid lays the months out
            // from these (PhotoMonths.kt) without looking at a date again.
            val zone = ZoneId.systemDefault()
            val newItems = lof.filesList.map { f ->
                val month = if (f.hasCreated()) monthOf(f.created.seconds, f.created.nanos, zone) else null
                val created = if (f.hasCreated()) f.created.seconds * 1000 + f.created.nanos / 1_000_000 else null
                Item("${f.path}#${f.hash}#${f.byteSize}", f.path, f.mime, f.byteSize, if (f.hasContent()) ThumbStore.tileKey(f.path, f.hash, f.byteSize) else null, month = month, created = created)
            }
            token = lof.token.ifEmpty { null }
            // A jump's first page (asked again after a failure too): the grid
            // goes to its month's title.
            val jumped = jumpLanding && requestToken.isEmpty()
            if (jumped) jumpLanding = false
            pageFailures = 0
            retryJob?.cancel()
            retryJob = null
            slowJob?.cancel()
            _state.update { st ->
                val existing = st.items.map { it.id }.toSet()
                st.copy(
                    items = st.items + newItems.filter { it.id !in existing }, endReached = token == null,
                    pageProblem = null, slowFirstPage = false, jumpsLanded = if (jumped) st.jumpsLanded + 1 else st.jumpsLanded,
                )
            }
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
        scope.launch { fetchHiRes(index) }
        // The viewer slides towards the neighbours, so have them ready.
        for (n in listOf(index - 1, index + 1)) if (n in _state.value.items.indices) scope.launch { fetchHiRes(n, prefetch = true) }
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

    /** prefetch: a neighbour readied for the slide - its image is fetched, a video is not started until opened. */
    private suspend fun fetchHiRes(index: Int, prefetch: Boolean = false) {
        val it = _state.value.items.getOrNull(index) ?: return
        if (it.mime.startsWith("video/")) { if (!prefetch && it.path !in _state.value.unplayable) fetchVideo(it); return }
        if (it.path in _state.value.hiResImages) { touchHiRes(it.path); return }
        if (it.path in inFlightHiRes) return
        inFlightHiRes += it.path
        _state.update { s -> s.copy(hiResLoading = s.hiResLoading + it.path) }
        var got = false
        try {
            val resp = call { e -> e.setReqGetFile(GetFile.newBuilder().setPath(it.path)) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_FILE) {
                val bmp = withContext(Dispatchers.Default) { decodeBitmap(resp.respFile.content.toByteArray()) }
                got = bmp != null
                if (bmp != null && _state.value.openIndex != null) cacheHiRes(it.path, bmp)
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
        // asked again when it is swiped to).
        if (!got) thumbnailStays(it.path)
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
        st.hiRes?.let { return it }
        val idx = st.openIndex ?: return null
        val item = st.items.getOrNull(idx) ?: return null
        st.bigThumbs[item.path]?.let { b -> withContext(Dispatchers.Default) { decodeBitmap(b) } }?.let { return it }
        return item.thumbKey?.let { ThumbStore.load(it) }?.let { decodeBitmap(it) } ?: item.preview
    }

    /** Issue #9: write the loaded image to a temp file and hand it to the share sheet. */
    suspend fun shareCurrentPhoto(context: Context) {
        val img = currentImage() ?: run { alert("Image isn't loaded yet"); return }
        try {
            val tmp = File(context.cacheDir, "${UUID.randomUUID()}.jpg")
            withContext(Dispatchers.IO) { tmp.outputStream().use { img.compress(Bitmap.CompressFormat.JPEG, 90, it) } }
            Share.file(context, tmp, "image/jpeg")
        } catch (e: Exception) { alert("Share failed: ${e.message}") }
    }

    /** Issue #9: save to the phone's gallery through MediaStore (no permission needed on API 29+). */
    suspend fun saveToPhotos(context: Context) {
        val img = currentImage() ?: run { alert("Image isn't loaded yet"); return }
        try {
            withContext(Dispatchers.IO) {
                val values = ContentValues().apply {
                    put(MediaStore.Images.Media.DISPLAY_NAME, "otc-${System.currentTimeMillis()}.jpg")
                    put(MediaStore.Images.Media.MIME_TYPE, "image/jpeg")
                    put(MediaStore.Images.Media.RELATIVE_PATH, "Pictures/Off The Cloud")
                }
                val uri = context.contentResolver.insert(MediaStore.Images.Media.EXTERNAL_CONTENT_URI, values) ?: throw IllegalStateException("insert failed")
                context.contentResolver.openOutputStream(uri)?.use { img.compress(Bitmap.CompressFormat.JPEG, 92, it) }
            }
            alert("Saved to Photos ✅")
        } catch (e: Exception) { alert("Save failed: ${e.message}") }
    }

    fun deleteCurrentPhoto() = scope.launch {
        val idx = _state.value.openIndex ?: return@launch
        val item = _state.value.items.getOrNull(idx) ?: return@launch
        try {
            val resp = call { it.setReqDelFile(DelFile.newBuilder().setPath(item.path)) }
            if (resp.error) { alert("Delete failed: ${resp.errorMessage}"); return@launch }
        } catch (e: Exception) { alert("Delete failed: ${e.message}"); return@launch }
        _state.update { st -> st.copy(items = st.items.filterIndexed { i, _ -> i != idx }, selected = st.selected - item.path) }
        if (_state.value.items.isEmpty()) closeModal() else open(minOf(idx, _state.value.items.size - 1))
        onDeleted?.invoke()
        countsChanged()
    }

    // Photos deleted from Images: the months' counts (the headers, the
    // scrubber) and the open collection's count follow.
    private fun countsChanged() {
        if (fixedList) return
        refillIfEmptied()
        reloadDateBuckets()
        if (_state.value.activeGroup != null) scope.launch { loadGroups() }
    }

    fun toggleSelect(path: String) = _state.update { st -> st.copy(selected = if (path in st.selected) st.selected - path else st.selected + path) }

    /** Nothing selected any more (Back, or leaving for People or Collections). */
    fun clearSelection() { if (_state.value.selected.isNotEmpty()) _state.update { it.copy(selected = emptySet()) } }

    fun deleteSelected() = scope.launch {
        val paths = _state.value.selected.toList()
        if (paths.isEmpty()) return@launch
        val deleted = mutableSetOf<String>()
        for (p in paths) {
            try {
                val resp = call { it.setReqDelFile(DelFile.newBuilder().setPath(p)) }
                if (resp.error) { alert("Delete failed: ${resp.errorMessage}"); continue }
            } catch (e: Exception) { alert("Delete failed: ${e.message}"); continue }
            deleted += p
        }
        if (deleted.isNotEmpty()) {
            _state.update { st -> st.copy(items = st.items.filter { it.path !in deleted }, selected = st.selected - deleted) }
            countsChanged()
        }
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
        try { shareLink()?.let { Share.link(context, it) } ?: alert("Could not create share link.") }
        finally { _state.update { it.copy(preparing = null) } }
    }

    fun downloadZip(context: Context) = scope.launch {
        _state.update { it.copy(preparing = SelectionActionTask.DOWNLOAD) }
        try { shareLink()?.let { Share.openInBrowser(context, it) } ?: alert("Could not create download link.") }
        finally { _state.update { it.copy(preparing = null) } }
    }

    fun alert(m: String) = _state.update { it.copy(alert = m) }
    fun dismissAlert() = _state.update { it.copy(alert = null) }

    companion object {
        // While the scrubber is dragged, a month that hasn't loaded shows
        // this many grey tiles at most: enough to read as "a lot", not
        // thousands of views (the web's cMaxPlaceholders).
        const val MAX_PLACEHOLDERS = 300

        /** Whose date buckets: the people and the open collection (tags never change them). */
        fun bucketsKeyOf(people: List<String>, groupId: String) = "${people.joinToString(",")}|$groupId"
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

/** Why there is nothing to show (the web's three empty states). */
enum class EmptyKind { COLLECTION, NO_MATCH, NO_PHOTOS }

fun emptyKind(st: PhotoGalleryViewModel.State): EmptyKind = when {
    st.chips.isNotEmpty() || st.selectedPeople.isNotEmpty() -> EmptyKind.NO_MATCH
    st.activeGroup != null -> EmptyKind.COLLECTION
    else -> EmptyKind.NO_PHOTOS
}
