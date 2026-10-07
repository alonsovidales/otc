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
import cloud.offthe.otc.net.ChunkedDownload
import cloud.offthe.otc.net.MediaStream
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.byteSize
import cloud.offthe.otc.proto.AddToImageGroup
import cloud.offthe.otc.proto.CreateImageGroup
import cloud.offthe.otc.proto.DelFile
import cloud.offthe.otc.proto.DeleteImageGroup
import cloud.offthe.otc.proto.FileExifInfo
import cloud.offthe.otc.proto.GetFile
import cloud.offthe.otc.proto.GetFileInfo
import cloud.offthe.otc.proto.GetTags
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ListImageGroups
import cloud.offthe.otc.proto.ListPeople
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.RenameImageGroup
import cloud.offthe.otc.proto.ReqPhotoDateBuckets
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.proto.ShareFilesLink
import cloud.offthe.otc.ui.common.SelectionActionTask
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.decodeBitmap
import com.google.protobuf.Timestamp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.util.Calendar
import java.util.UUID

// SearchPhotos.limit for the request that starts a photo search (no
// token): a small first page paints quickly over a slow upload. Pages that
// continue the token send none and get the device's own size; a device
// before release 97 ignores it and answers its default, 30.
const val FIRST_PHOTO_PAGE_LIMIT = 12

// How old the tag and people lists may get before a search that ends
// fetches them again.
private const val LISTS_REFRESH_MS = 5 * 60_000L

// Port of PhotoGalleryVM (PhotoGallery.swift). Tag chips, the person
// filter (issue #52, AND semantics), image groups (issue #115, which the
// app calls collections), the date scrubber (issue #77), a search
// generation counter that discards stale replies, and the paging that
// keeps asking until a page adds something.
//
// The same view model also drives the viewer opened from the Files section
// (showFiles): a separate instance holding just that folder's photos and
// videos, with no search or paging.
class PhotoGalleryViewModel(private val deviceId: String) : ViewModel(), PeopleStore {
    // thumbKey: the thumbnail's bytes in ThumbStore (the item's id), null
    // when the device sent none. preview: an already decoded placeholder
    // (the Files grid's thumbnail), used when there are no thumb bytes.
    data class Item(val id: String, val path: String, val mime: String, val size: Long, val thumbKey: String?, val preview: Bitmap? = null)
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
        val dateBuckets: List<DateBucket> = emptyList(),
        val scrubFrac: Float? = null,
        val placeholderCount: Int? = null,
        val items: List<Item> = emptyList(),
        val loading: Boolean = false,
        val endReached: Boolean = false,
        val openIndex: Int? = null,
        // Full-size images by path, kept for the last few opened so the
        // pager can draw the neighbour it slides towards and swiping back
        // doesn't fetch again. hiRes is the open one's (mirrors iOS).
        val hiResImages: Map<String, Bitmap> = emptyMap(),
        // Paths whose full-size image is being fetched: until it arrives
        // the page shows the thumbnail with a "Low res" pill and a spinner;
        // after a failure, the pill alone.
        val hiResLoading: Set<String> = emptySet(),
        val videoUrl: String? = null,
        val alert: String? = null,
        val preparing: SelectionActionTask? = null,
        val selected: Set<String> = emptySet(),
        val infoOpen: Boolean = false,
        val infoLoading: Boolean = false,
        val infoData: FileExifInfo? = null,
    ) {
        val totalPhotos get() = dateBuckets.lastOrNull()?.end ?: 0
        val showScrubber get() = chips.isEmpty() && dateBuckets.isNotEmpty()
        val yearTicks: List<Pair<String, Float>> get() {
            if (totalPhotos <= 0) return emptyList()
            val out = mutableListOf<Pair<String, Float>>()
            var last = ""
            for (b in dateBuckets) {
                val y = b.month.take(4)
                if (y != last) { out += y to b.start.toFloat() / totalPhotos; last = y }
            }
            return out
        }
        val hiRes: Bitmap? get() = openIndex?.let { items.getOrNull(it) }?.let { hiResImages[it.path] }
        val scrubTarget: DateBucket? get() {
            val f = scrubFrac ?: return null
            if (totalPhotos <= 0) return null
            val idx = (f * totalPhotos).toInt()
            return dateBuckets.firstOrNull { idx >= it.start && idx < it.end } ?: dateBuckets.lastOrNull()
        }
    }

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state
    private var token: String? = null
    private var searchGeneration = 0
    private var searchJob: Job? = null
    // The furthest tile (its index) that asked for more while a page was
    // loading, so the page that lands can honour it.
    private var morePendingAt: Int? = null
    private val maxPagesWithoutProgress = 12
    private val maxFetchRetries = 2
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
        token = null
        _state.update { it.copy(items = items, endReached = true, loading = false, selected = emptySet()) }
        open(startAt)
    }

    fun onAppearInitial() = viewModelScope.launch {
        watchFaceRecognition()
        listsAsked = true
        loadTags(); loadPeople(); resetAndLoadFirstPage()
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
        viewModelScope.launch {
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
        viewModelScope.launch {
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

    // New photos bring new tags and faces. The lists are fetched again when
    // a search ends, if the last time was a while ago: current for the next
    // one, and nothing moves under the finger while picking.
    private var listsFetchedAt = System.currentTimeMillis()
    fun refreshListsIfStale() {
        if (System.currentTimeMillis() - listsFetchedAt < LISTS_REFRESH_MS) return
        listsFetchedAt = System.currentTimeMillis()
        viewModelScope.launch {
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
        searchJob = viewModelScope.launch { resetAndLoadFirstPage() }
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

    private suspend fun loadTags() {
        try {
            val resp = OTCConnection.request { it.setReqGetTags(GetTags.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_TAGS_LIST) {
                _state.update { it.copy(tags = resp.respTagsList.tagsList) }
                listsFetchedAt = System.currentTimeMillis()
            }
        } catch (_: Exception) {}
    }

    private suspend fun loadDateBuckets() {
        val st = _state.value
        if (st.chips.isNotEmpty()) { _state.update { it.copy(dateBuckets = emptyList()) }; return }
        val resp = try {
            OTCConnection.request {
                it.setReqPhotoDateBuckets(ReqPhotoDateBuckets.newBuilder().addAllPersonIds(st.selectedPeople).setGroupId(st.activeGroup?.id ?: "").setIncludeVideos(true))
            }
        } catch (e: Exception) { return }
        if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_PHOTO_DATE_BUCKETS) return
        var cum = 0
        val buckets = resp.respPhotoDateBuckets.bucketsList.map { pb -> val s = cum; cum += pb.count; DateBucket(pb.month, pb.count, s, cum) }
        _state.update { it.copy(dateBuckets = buckets) }
    }

    fun setScrubFrac(f: Float?) = _state.update { it.copy(scrubFrac = f) }
    fun setPlaceholderCount(n: Int?) = _state.update { it.copy(placeholderCount = n) }

    fun jumpToDate(month: String) {
        searchJob?.cancel()
        searchJob = viewModelScope.launch { performJump(month) }
    }

    private suspend fun performJump(month: String) {
        val parts = month.split("-").mapNotNull { it.toIntOrNull() }
        if (parts.size != 2) return
        val cal = Calendar.getInstance().apply { clear(); set(parts[0], parts[1] - 1, 1); add(Calendar.MONTH, 1) }
        val before = cal.timeInMillis - 1000
        searchGeneration += 1
        val mine = searchGeneration
        token = ""
        morePendingAt = null
        _state.update { it.copy(loading = false, endReached = false, items = emptyList(), selected = emptySet()) }
        fetchPage(overrideToken = "", beforeMs = before)
        if (mine == searchGeneration) _state.update { it.copy(placeholderCount = null) }
    }

    /** The people, asked again: true once the device's list is in. */
    override suspend fun loadPeople(): Boolean {
        try {
            val resp = OTCConnection.request { it.setReqListPeople(ListPeople.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_PEOPLE) {
                _state.update { it.copy(allPeople = resp.respPeople.peopleList, peopleLoaded = true) }
                return true
            }
        } catch (e: kotlinx.coroutines.CancellationException) {
            throw e
        } catch (_: Exception) {}
        return false
    }

    /** The collections, asked again: true once the device's list is in (groupsLoaded). */
    suspend fun loadGroups(): Boolean {
        try {
            val resp = OTCConnection.request { it.setReqListImageGroups(ListImageGroups.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_IMAGE_GROUPS) {
                val gs = resp.respImageGroups.groupsList
                _state.update { st -> st.copy(groups = gs, groupsLoaded = true, activeGroup = st.activeGroup?.let { open -> gs.firstOrNull { it.id == open.id } ?: open }) }
                return true
            }
        } catch (e: kotlinx.coroutines.CancellationException) {
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
            val r = OTCConnection.request { it.setReqRenameImageGroup(RenameImageGroup.newBuilder().setId(g.id).setName(name)) }
            r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok
        } catch (e: Exception) { false }
        if (!ok) { alert("Could not rename the collection."); return }
        val renamed = g.toBuilder().setName(name).build()
        _state.update { st -> st.copy(activeGroup = renamed, groups = st.groups.map { if (it.id == g.id) renamed else it }) }
    }

    suspend fun deleteActiveGroup() {
        val g = _state.value.activeGroup ?: return
        val ok = try {
            val r = OTCConnection.request { it.setReqDeleteImageGroup(DeleteImageGroup.newBuilder().setId(g.id)) }
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
            val r = OTCConnection.request { it.setReqCreateImageGroup(CreateImageGroup.newBuilder().setName(name).addAllPaths(paths)) }
            if (r.payloadCase != RespEnvelope.PayloadCase.RESP_IMAGE_GROUP) { alert("Could not create the collection."); return }
            _state.update { st -> st.copy(groups = listOf(r.respImageGroup.group) + st.groups, selected = emptySet()) }
            alert("Collection \"$name\" created.")
        } catch (e: Exception) { alert("Could not create the collection.") }
    }

    suspend fun addSelectionToGroup(g: ImageGroup) {
        val paths = _state.value.selected.toList()
        val ok = try {
            val r = OTCConnection.request { it.setReqAddToImageGroup(AddToImageGroup.newBuilder().setGroupId(g.id).addAllPaths(paths)) }
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
        token = ""
        morePendingAt = null
        _state.update { it.copy(loading = false, endReached = false, items = emptyList(), selected = emptySet(), scrubFrac = null, placeholderCount = null) }
        val buckets = viewModelScope.launch { loadDateBuckets() }
        fetchPage(overrideToken = "")
        buckets.join()
    }

    suspend fun loadMoreIfNeeded(item: Item?) {
        val st = _state.value
        if (item == null || st.endReached || fixedList) return
        val idx = st.items.indexOf(item)
        if (idx < 0 || idx < st.items.size - 12) return
        if (st.loading) { morePendingAt = maxOf(morePendingAt ?: idx, idx); return }
        fetchUntilProgress()
    }

    private suspend fun fetchUntilProgress() {
        var failures = 0
        for (i in 0 until maxPagesWithoutProgress) {
            val before = _state.value.items.size
            if (fetchPage()) {
                failures = 0
                if (_state.value.endReached || _state.value.items.size > before) return
                continue
            }
            failures += 1
            if (failures > maxFetchRetries) return
            delay(failures * 1500L)
        }
    }

    private suspend fun fetchPage(overrideToken: String? = null, beforeMs: Long? = null): Boolean {
        val st0 = _state.value
        if (st0.loading || st0.endReached) return false
        val mine = searchGeneration
        _state.update { it.copy(loading = true) }
        try {
            val tags = st0.chips
            val people = st0.selectedPeople
            val group = st0.activeGroup?.id ?: ""
            val requestToken = overrideToken ?: token ?: ""
            val have = st0.items.size
            val resp = try {
                OTCConnection.request { e ->
                    val sp = SearchPhotos.newBuilder().addAllTags(tags).addAllPersonIds(people).setGroupId(group).setIncludeVideos(true).setToken(requestToken).setHave(have)
                    // A search starting here (opening, a filter, the
                    // scrubber's jump) gets a small first page; scrolling
                    // on, the device's own size.
                    if (requestToken.isEmpty()) sp.limit = FIRST_PHOTO_PAGE_LIMIT
                    if (beforeMs != null) sp.before = Timestamp.newBuilder().setSeconds(beforeMs / 1000).build()
                    e.setReqSearchPhotos(sp)
                }
            } catch (e: Exception) { return false }
            if (mine != searchGeneration) return false
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) return false
            val lof = resp.respListOfFiles
            val withThumb = lof.filesList.filter { it.hasContent() }.map { f -> "${f.path}#${f.hash}#${f.byteSize}" to f.content.toByteArray() }
            ThumbStore.putAll(withThumb)
            if (mine != searchGeneration) return false
            val newItems = lof.filesList.map { f -> "${f.path}#${f.hash}#${f.byteSize}".let { id -> Item(id, f.path, f.mime, f.byteSize, if (f.hasContent()) id else null) } }
            _state.update { st ->
                val existing = st.items.map { it.id }.toSet()
                st.copy(items = st.items + newItems.filter { it.id !in existing })
            }
            token = lof.token.ifEmpty { null }
            _state.update { it.copy(endReached = token == null) }
            return true
        } finally {
            if (mine == searchGeneration) {
                _state.update { it.copy(loading = false) }
                // Only if that tile is still near the end: every tile of a
                // small first page asks at once, and the page that just
                // landed moved the end well past them - honouring them
                // anyway pulled a third page nobody had scrolled to.
                val at = morePendingAt
                morePendingAt = null
                if (at != null && !_state.value.endReached && at >= _state.value.items.size - 12) viewModelScope.launch { fetchUntilProgress() }
            }
        }
    }

    // Viewer. hiResOrder: the cached full-size images, least recently used first.
    private val hiResOrder = mutableListOf<String>()
    private val inFlightHiRes = mutableSetOf<String>()
    private val hiResCacheSize = 8
    // And a byte budget: a 12 MP photo decodes to 48 MB, so eight of them
    // could hold ~400 MB. The open photo and its neighbours stay regardless.
    private val hiResBudgetBytes: Long = run {
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
        _state.update { it.copy(openIndex = index, videoUrl = null, infoOpen = false, infoData = null) }
        viewModelScope.launch { fetchHiRes(index) }
        // The viewer slides towards the neighbours, so have them ready.
        for (n in listOf(index - 1, index + 1)) if (n in _state.value.items.indices) viewModelScope.launch { fetchHiRes(n, prefetch = true) }
    }

    fun closeModal() {
        hiResOrder.clear()
        _state.update { it.copy(openIndex = null, hiResImages = emptyMap(), videoUrl = null) }
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
    fun prev() { _state.value.openIndex?.let { if (it > 0) open(it - 1) } }
    fun next() { _state.value.openIndex?.let { if (it < _state.value.items.size - 1) open(it + 1) } }

    fun openInfo() {
        val idx = _state.value.openIndex ?: return
        val path = _state.value.items.getOrNull(idx)?.path ?: return
        _state.update { it.copy(infoOpen = true, infoLoading = true, infoData = null) }
        viewModelScope.launch {
            try {
                val resp = OTCConnection.request { it.setReqGetFileInfo(GetFileInfo.newBuilder().setPath(path)) }
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
        if (it.mime.startsWith("video/")) { if (!prefetch) fetchVideo(it); return }
        if (it.path in _state.value.hiResImages) { touchHiRes(it.path); return }
        if (it.path in inFlightHiRes) return
        inFlightHiRes += it.path
        _state.update { s -> s.copy(hiResLoading = s.hiResLoading + it.path) }
        try {
            val resp = OTCConnection.request { e -> e.setReqGetFile(GetFile.newBuilder().setPath(it.path)) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_FILE) {
                val bmp = withContext(Dispatchers.Default) { decodeBitmap(resp.respFile.content.toByteArray()) }
                if (bmp != null && _state.value.openIndex != null) cacheHiRes(it.path, bmp)
            }
        } catch (_: Exception) {
        } finally {
            inFlightHiRes -= it.path
            _state.update { s -> s.copy(hiResLoading = s.hiResLoading - it.path) }
        }
    }

    /** Issue #110: streamed when the device offers a URL, downloaded otherwise. */
    private suspend fun fetchVideo(it: Item) {
        // Still on this video? Stepping on before a slow stream URL or
        // download came back used to start the previous video over the next one.
        fun stillOpen() = _state.value.openIndex?.let { i -> _state.value.items.getOrNull(i)?.path } == it.path
        MediaStream.url(forPath = it.path)?.let { url -> if (stillOpen()) _state.update { s -> s.copy(videoUrl = url) }; return }
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
            _state.update { s -> s.copy(videoUrl = tmp.toURI().toString()) }
        } catch (_: Exception) {}
    }

    suspend fun currentImage(): Bitmap? {
        val st = _state.value
        st.hiRes?.let { return it }
        val idx = st.openIndex ?: return null
        val item = st.items.getOrNull(idx) ?: return null
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

    fun deleteCurrentPhoto() = viewModelScope.launch {
        val idx = _state.value.openIndex ?: return@launch
        val item = _state.value.items.getOrNull(idx) ?: return@launch
        try {
            val resp = OTCConnection.request { it.setReqDelFile(DelFile.newBuilder().setPath(item.path)) }
            if (resp.error) { alert("Delete failed: ${resp.errorMessage}"); return@launch }
        } catch (e: Exception) { alert("Delete failed: ${e.message}"); return@launch }
        _state.update { st -> st.copy(items = st.items.filterIndexed { i, _ -> i != idx }, selected = st.selected - item.path) }
        if (_state.value.items.isEmpty()) closeModal() else open(minOf(idx, _state.value.items.size - 1))
        onDeleted?.invoke()
    }

    fun toggleSelect(path: String) = _state.update { st -> st.copy(selected = if (path in st.selected) st.selected - path else st.selected + path) }

    fun deleteSelected() = viewModelScope.launch {
        val paths = _state.value.selected.toList()
        if (paths.isEmpty()) return@launch
        val deleted = mutableSetOf<String>()
        for (p in paths) {
            try {
                val resp = OTCConnection.request { it.setReqDelFile(DelFile.newBuilder().setPath(p)) }
                if (resp.error) { alert("Delete failed: ${resp.errorMessage}"); continue }
            } catch (e: Exception) { alert("Delete failed: ${e.message}"); continue }
            deleted += p
        }
        if (deleted.isNotEmpty()) _state.update { st -> st.copy(items = st.items.filter { it.path !in deleted }, selected = st.selected - deleted) }
    }

    private suspend fun shareLink(): String? {
        val paths = _state.value.selected.toList()
        return try {
            val r = OTCConnection.request { it.setReqShareFilesLink(ShareFilesLink.newBuilder().addAllPaths(paths)) }
            if (r.payloadCase == RespEnvelope.PayloadCase.RESP_SHARE_LINK) r.respShareLink.link else null
        } catch (e: Exception) { null }
    }

    fun shareSelected(context: Context) = viewModelScope.launch {
        _state.update { it.copy(preparing = SelectionActionTask.SHARE) }
        try { shareLink()?.let { Share.link(context, it) } ?: alert("Could not create share link.") }
        finally { _state.update { it.copy(preparing = null) } }
    }

    fun downloadZip(context: Context) = viewModelScope.launch {
        _state.update { it.copy(preparing = SelectionActionTask.DOWNLOAD) }
        try { shareLink()?.let { Share.openInBrowser(context, it) } ?: alert("Could not create download link.") }
        finally { _state.update { it.copy(preparing = null) } }
    }

    fun alert(m: String) = _state.update { it.copy(alert = m) }
    fun dismissAlert() = _state.update { it.copy(alert = null) }
}
