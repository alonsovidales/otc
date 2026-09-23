// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import android.content.ContentValues
import android.content.Context
import android.graphics.Bitmap
import android.provider.MediaStore
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.net.MediaStream
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.AddToImageGroup
import cloud.offthe.otc.proto.CreateImageGroup
import cloud.offthe.otc.proto.DelFile
import cloud.offthe.otc.proto.DeleteImageGroup
import cloud.offthe.otc.proto.DeletePerson
import cloud.offthe.otc.proto.FileExifInfo
import cloud.offthe.otc.proto.GetFile
import cloud.offthe.otc.proto.GetFileInfo
import cloud.offthe.otc.proto.GetTags
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ListImageGroups
import cloud.offthe.otc.proto.ListPeople
import cloud.offthe.otc.proto.MergePeople
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.RenameImageGroup
import cloud.offthe.otc.proto.RenamePerson
import cloud.offthe.otc.proto.ReqPhotoDateBuckets
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.proto.ShareFilesLink
import cloud.offthe.otc.ui.common.SelectionActionTask
import cloud.offthe.otc.ui.common.Share
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

// Port of PhotoGalleryVM (PhotoGallery.swift). Tag chips, the person
// filter (issue #52, AND semantics), image groups (issue #115), the date
// scrubber (issue #77), a search generation counter that discards stale
// replies, and the paging that keeps asking until a page adds something.
class PhotoGalleryViewModel(private val deviceId: String) : ViewModel() {
    data class Item(val id: String, val path: String, val mime: String, val size: Int, val thumb: ByteArray?)
    data class DateBucket(val month: String, val count: Int, val start: Int, val end: Int)
    data class PendingMerge(val target: Person, val source: Person)

    data class State(
        val tags: List<String> = emptyList(),
        val chips: List<String> = emptyList(),
        val allPeople: List<Person> = emptyList(),
        val selectedPeople: List<String> = emptyList(),
        val editingPersonId: String? = null,
        val groups: List<ImageGroup> = emptyList(),
        val activeGroup: ImageGroup? = null,
        val mergeTargetId: String? = null,
        val pendingMerge: PendingMerge? = null,
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
    private var morePending = false
    private val maxPagesWithoutProgress = 12
    private val maxFetchRetries = 2

    fun onAppearInitial() = viewModelScope.launch {
        loadTags(); loadPeople(); resetAndLoadFirstPage()
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
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_TAGS_LIST) _state.update { it.copy(tags = resp.respTagsList.tagsList) }
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
        _state.update { it.copy(loading = false, endReached = false, items = emptyList(), selected = emptySet()) }
        fetchPage(overrideToken = "", beforeMs = before)
        if (mine == searchGeneration) _state.update { it.copy(placeholderCount = null) }
    }

    private suspend fun loadPeople() {
        try {
            val resp = OTCConnection.request { it.setReqListPeople(ListPeople.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_PEOPLE) _state.update { it.copy(allPeople = resp.respPeople.peopleList) }
        } catch (_: Exception) {}
    }

    suspend fun loadGroups() {
        try {
            val resp = OTCConnection.request { it.setReqListImageGroups(ListImageGroups.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_IMAGE_GROUPS) {
                val gs = resp.respImageGroups.groupsList
                _state.update { st -> st.copy(groups = gs, activeGroup = st.activeGroup?.let { open -> gs.firstOrNull { it.id == open.id } ?: open }) }
            }
        } catch (_: Exception) {}
    }

    fun openGroup(g: ImageGroup) { _state.update { it.copy(activeGroup = g) }; restartSearch() }
    fun leaveGroup() { _state.update { it.copy(activeGroup = null) }; restartSearch() }

    suspend fun renameActiveGroup(newName: String) {
        val g = _state.value.activeGroup ?: return
        val name = newName.trim()
        if (name.isEmpty() || name == g.name) return
        val ok = try {
            val r = OTCConnection.request { it.setReqRenameImageGroup(RenameImageGroup.newBuilder().setId(g.id).setName(name)) }
            r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok
        } catch (e: Exception) { false }
        if (!ok) { alert("Could not rename the group."); return }
        val renamed = g.toBuilder().setName(name).build()
        _state.update { st -> st.copy(activeGroup = renamed, groups = st.groups.map { if (it.id == g.id) renamed else it }) }
    }

    suspend fun deleteActiveGroup() {
        val g = _state.value.activeGroup ?: return
        val ok = try {
            val r = OTCConnection.request { it.setReqDeleteImageGroup(DeleteImageGroup.newBuilder().setId(g.id)) }
            r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok
        } catch (e: Exception) { false }
        if (!ok) { alert("Could not delete the group."); return }
        _state.update { st -> st.copy(groups = st.groups.filter { it.id != g.id }) }
        leaveGroup()
    }

    suspend fun createGroupFromSelection(rawName: String) {
        val name = rawName.trim()
        if (name.isEmpty()) return
        val paths = _state.value.selected.toList()
        try {
            val r = OTCConnection.request { it.setReqCreateImageGroup(CreateImageGroup.newBuilder().setName(name).addAllPaths(paths)) }
            if (r.payloadCase != RespEnvelope.PayloadCase.RESP_IMAGE_GROUP) { alert("Could not create the group."); return }
            _state.update { st -> st.copy(groups = listOf(r.respImageGroup.group) + st.groups, selected = emptySet()) }
            alert("Group \"$name\" created.")
        } catch (e: Exception) { alert("Could not create the group.") }
    }

    suspend fun addSelectionToGroup(g: ImageGroup) {
        val paths = _state.value.selected.toList()
        val ok = try {
            val r = OTCConnection.request { it.setReqAddToImageGroup(AddToImageGroup.newBuilder().setGroupId(g.id).addAllPaths(paths)) }
            r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok
        } catch (e: Exception) { false }
        if (!ok) { alert("Could not add to the group."); return }
        _state.update { it.copy(selected = emptySet()) }
        loadGroups()
        if (_state.value.activeGroup?.id == g.id) restartSearch()
        alert("Added to \"${g.name}\".")
    }

    fun togglePerson(id: String) {
        _state.update { st -> st.copy(selectedPeople = if (id in st.selectedPeople) st.selectedPeople - id else st.selectedPeople + id) }
        restartSearch()
    }

    fun startRenamePerson(p: Person) = _state.update { it.copy(editingPersonId = p.id) }

    suspend fun commitRenamePerson(id: String, rawName: String) {
        val name = rawName.trim()
        _state.update { it.copy(editingPersonId = null) }
        try {
            val r = OTCConnection.request { it.setReqRenamePerson(RenamePerson.newBuilder().setId(id).setName(name)) }
            if (r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok) {
                _state.update { st -> st.copy(allPeople = st.allPeople.map { if (it.id == id) it.toBuilder().setName(name).build() else it }) }
            }
        } catch (_: Exception) {}
    }

    suspend fun deletePerson(id: String) {
        try {
            val r = OTCConnection.request { it.setReqDeletePerson(DeletePerson.newBuilder().setId(id)) }
            if (r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok) {
                _state.update { st -> st.copy(allPeople = st.allPeople.filter { it.id != id }, selectedPeople = st.selectedPeople - id) }
            }
        } catch (_: Exception) {}
    }

    fun setMergeTarget(id: String?) = _state.update { it.copy(mergeTargetId = id) }

    fun pickMergeTarget(p: Person) {
        val st = _state.value
        if (st.mergeTargetId == p.id) { _state.update { it.copy(mergeTargetId = null) }; return }
        val target = st.allPeople.firstOrNull { it.id == st.mergeTargetId } ?: return
        _state.update { it.copy(mergeTargetId = null, pendingMerge = PendingMerge(target, p)) }
    }

    fun cancelMerge() = _state.update { it.copy(pendingMerge = null) }

    suspend fun confirmMerge() {
        val m = _state.value.pendingMerge ?: return
        _state.update { it.copy(pendingMerge = null) }
        try {
            val r = OTCConnection.request { it.setReqMergePeople(MergePeople.newBuilder().setTargetId(m.target.id).addSourceIds(m.source.id)) }
            if (r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok) {
                _state.update { st -> st.copy(selectedPeople = st.selectedPeople - m.source.id) }
                loadPeople()
            }
        } catch (_: Exception) {}
    }

    suspend fun resetAndLoadFirstPage() {
        searchGeneration += 1
        token = ""
        _state.update { it.copy(loading = false, endReached = false, items = emptyList(), selected = emptySet(), scrubFrac = null, placeholderCount = null) }
        val buckets = viewModelScope.launch { loadDateBuckets() }
        fetchPage(overrideToken = "")
        buckets.join()
    }

    suspend fun loadMoreIfNeeded(item: Item?) {
        val st = _state.value
        if (item == null || st.endReached) return
        val idx = st.items.indexOf(item)
        if (idx < 0 || idx < st.items.size - 12) return
        if (st.loading) { morePending = true; return }
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
                    if (beforeMs != null) sp.before = Timestamp.newBuilder().setSeconds(beforeMs / 1000).build()
                    e.setReqSearchPhotos(sp)
                }
            } catch (e: Exception) { return false }
            if (mine != searchGeneration) return false
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) return false
            val lof = resp.respListOfFiles
            val newItems = lof.filesList.map { f -> Item("${f.path}#${f.hash}#${f.size}", f.path, f.mime, f.size, if (f.hasContent()) f.content.toByteArray() else null) }
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
                if (morePending && !_state.value.endReached) { morePending = false; viewModelScope.launch { fetchUntilProgress() } }
            }
        }
    }

    // Viewer
    private val hiResOrder = mutableListOf<String>()
    private val inFlightHiRes = mutableSetOf<String>()
    private val hiResCacheSize = 8

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
    }

    private fun cacheHiRes(path: String, bmp: Bitmap) {
        if (path !in _state.value.hiResImages) hiResOrder += path
        val evicted = mutableListOf<String>()
        while (hiResOrder.size > hiResCacheSize) evicted += hiResOrder.removeAt(0)
        _state.update { it.copy(hiResImages = (it.hiResImages - evicted.toSet()) + (path to bmp)) }
    }
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
        if (it.path in _state.value.hiResImages || it.path in inFlightHiRes) return
        inFlightHiRes += it.path
        try {
            val resp = OTCConnection.request { e -> e.setReqGetFile(GetFile.newBuilder().setPath(it.path)) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_FILE) {
                val bmp = withContext(Dispatchers.Default) { decodeBitmap(resp.respFile.content.toByteArray()) }
                if (bmp != null && _state.value.openIndex != null) cacheHiRes(it.path, bmp)
            }
        } catch (_: Exception) {
        } finally {
            inFlightHiRes -= it.path
        }
    }

    /** Issue #110: streamed when the device offers a URL, downloaded otherwise. */
    private suspend fun fetchVideo(it: Item) {
        MediaStream.url(forPath = it.path)?.let { url -> _state.update { s -> s.copy(videoUrl = url) }; return }
        try {
            val resp = OTCConnection.request { e -> e.setReqGetFile(GetFile.newBuilder().setPath(it.path)) }
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_FILE || !resp.respFile.hasContent()) return
            val ext = when (resp.respFile.mime.lowercase()) {
                "video/quicktime" -> "mov"
                "video/mp4", "video/x-m4v" -> "mp4"
                "video/x-matroska" -> "mkv"
                "video/3gpp" -> "3gp"
                else -> it.path.substringAfterLast('.', "mp4").lowercase()
            }
            val tmp = File(OTCApp.instance.cacheDir, "${UUID.randomUUID()}.$ext")
            withContext(Dispatchers.IO) { tmp.writeBytes(resp.respFile.content.toByteArray()) }
            _state.update { s -> s.copy(videoUrl = tmp.toURI().toString()) }
        } catch (_: Exception) {}
    }

    fun currentImage(): Bitmap? {
        val st = _state.value
        st.hiRes?.let { return it }
        val idx = st.openIndex ?: return null
        return st.items.getOrNull(idx)?.thumb?.let { decodeBitmap(it) }
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
