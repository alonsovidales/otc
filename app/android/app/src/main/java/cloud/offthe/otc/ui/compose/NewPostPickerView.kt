// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.compose

import android.Manifest
import android.graphics.Bitmap
import android.net.Uri
import android.os.Build
import android.util.Size
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Cancel
import androidx.compose.material.icons.filled.ChevronLeft
import androidx.compose.material.icons.filled.ChevronRight
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.PhotoCamera
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LocalTextStyle
import androidx.compose.material3.MaterialTheme
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.foundation.lazy.grid.rememberLazyGridState
import androidx.compose.runtime.snapshotFlow
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.core.content.ContextCompat
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelStore
import androidx.lifecycle.ViewModelStoreOwner
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.ui.common.FitText
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidComposeMoveLeft
import cloud.offthe.otc.i18n.androidComposeMoveRight
import cloud.offthe.otc.i18n.androidComposeRecordVideo
import cloud.offthe.otc.i18n.androidComposeTakePhoto
import cloud.offthe.otc.i18n.appSocialComposeCamera
import cloud.offthe.otc.i18n.appSocialComposeCaptionNeeded
import cloud.offthe.otc.i18n.appSocialComposeCaptionPlaceholder
import cloud.offthe.otc.i18n.appSocialComposeEmptyResponse
import cloud.offthe.otc.i18n.appSocialComposeFilterPlaceholder
import cloud.offthe.otc.i18n.appSocialComposeOrder
import cloud.offthe.otc.i18n.appSocialComposePhotosAccess
import cloud.offthe.otc.i18n.appSocialComposePublish
import cloud.offthe.otc.i18n.appSocialComposePublishCount
import cloud.offthe.otc.i18n.appSocialComposePublishFailed
import cloud.offthe.otc.i18n.appSocialComposePublishFailedDetail
import cloud.offthe.otc.i18n.appSocialComposePublishing
import cloud.offthe.otc.i18n.appSocialComposeSourcePhone
import cloud.offthe.otc.i18n.appSocialComposeSourceSynced
import cloud.offthe.otc.i18n.appSocialComposeTitle
import cloud.offthe.otc.i18n.appSocialComposeTrimBadge
import cloud.offthe.otc.i18n.appSocialComposeTrimLoadFailed
import cloud.offthe.otc.i18n.appSocialComposeUploading
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonOk
import cloud.offthe.otc.i18n.commonRemove
import cloud.offthe.otc.i18n.commonSearch
import cloud.offthe.otc.net.ChunkedDownload
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.byteSize
import cloud.offthe.otc.proto.GetTags
import cloud.offthe.otc.proto.NewSocialPublication
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.proto.VideoTrim
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.net.sleepOrWake
import cloud.offthe.otc.ui.common.FIRST_PAGE_LIMIT_WITHOUT_THUMBS
import cloud.offthe.otc.ui.common.GridThumbFetcher
import cloud.offthe.otc.ui.common.NEXT_PAGE_LIMIT_WITHOUT_THUMBS
import cloud.offthe.otc.ui.common.TILES_KIND
import cloud.offthe.otc.ui.common.TextIOException
import cloud.offthe.otc.ui.common.ThumbCache
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.land
import cloud.offthe.otc.ui.common.gridCellPx
import cloud.offthe.otc.ui.common.rememberOffMain
import cloud.offthe.otc.ui.common.rememberTileThumb
import cloud.offthe.otc.ui.gallery.FIRST_PHOTO_PAGE_LIMIT
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.util.UUID

// Port of NewPostPicker.swift (issue #32): Phone / Synced source (issue
// #49), tag filter for Synced, numbered selection with an "Order in post"
// strip (issue #48), trims for videos (issue #108), and publish.
class NewPostPickerViewModel : ViewModel() {
    enum class Source { PHONE, SYNCED }

    // thumbKey: a synced item's thumbnail in ThumbStore (thumbTileKey; null
    // while it has none - fetched by `tiles`). A phone item's comes
    // from MediaStore when its tile shows (see thumbOf), so pages scrolled
    // through don't each keep 60 bitmaps.
    data class Item(val id: String, val path: String, val thumbKey: String? = null, val asset: PhotoSync.Asset? = null, val isVideo: Boolean = false) {
        override fun equals(other: Any?) = other is Item && other.id == id
        override fun hashCode() = id.hashCode()
    }

    data class State(
        val source: Source = Source.PHONE,
        val tags: List<String> = emptyList(),
        val chips: List<String> = emptyList(),
        val items: List<Item> = emptyList(),
        val loading: Boolean = false,
        val endReached: Boolean = false,
        // What is picked, in post order, whichever source or tags the grid
        // shows now (ComposerPicks).
        val picks: ComposerPicks = ComposerPicks(),
        // Synced tiles' thumbnails that landed after their page (by path; a
        // null value: none, or let go), over the key the page brought.
        val thumbKeys: Map<String, String?> = emptyMap(),
        val trims: Map<String, TrimRange> = emptyMap(),
        val trimming: Pair<String, String>? = null,
        val trimLoadingId: String? = null,
        val caption: String = "",
        val publishing: Boolean = false,
        /** The line above the composer bar while publishing (issue #49); null: none. */
        val publishStatus: UiText? = null,
        val alert: UiText? = null,
    ) {
        /** Publish is greyed out for want of a caption alone: the composer says so. */
        val captionNeeded: Boolean get() = captionNeeded(picks.size, caption, publishing)

        /** A synced item's thumbnail in ThumbStore: the one that landed for it, else its page's; null: none yet. */
        fun thumbKeyOf(item: Item): String? = if (item.path in thumbKeys) thumbKeys[item.path] else item.thumbKey

        /** After a post went out: no caption, picks or trims left for the next one; the grid stays. */
        fun posted(): State = copy(caption = "", picks = ComposerPicks(), trims = emptyMap(), trimming = null)
    }

    companion object {
        /**
         * Something is picked but the caption is empty: Publish waits for
         * one (issue #96), and the line above the bar says so - not while
         * nothing is picked (that is plain to see) or while publishing.
         */
        fun captionNeeded(picked: Int, caption: String, publishing: Boolean): Boolean =
            picked > 0 && caption.isBlank() && !publishing
    }

    private val _state = MutableStateFlow(State())
    val state = _state
    private var token: String? = null
    // Bumped when the grid starts over (source, tags), so a page still in
    // flight for the old one lands nowhere.
    private var searchGeneration = 0
    private var localAssets: List<PhotoSync.Asset>? = null
    private var localLoadedCount = 0
    private var didAppear = false
    private val trimPreviewUrls = mutableMapOf<String, String>()
    private val localPageSize = 60

    // Synced: the tiles' missing thumbnails (pages come without them,
    // release 113), fetched a batch at a time, the tiles on screen first.
    private val tiles by lazy {
        GridThumbFetcher(
            viewModelScope, ThumbStore,
            send = { b -> OTCConnection.ask(TILES_KIND, OTCConnection.PAGE_TIMEOUT_MS) { it.setReqGetThumbnails(b) } },
            pause = { sleepOrWake(it) },
        ) { landed ->
            // By path, beside the items: an Item equals another by its id
            // alone, so a copy with only its key changed was an equal State
            // the flow dropped, and the tiles stayed grey. Kept for the
            // picks too: a synced pick shows in the order strip while the
            // grid shows the phone's photos.
            _state.update { st -> st.copy(thumbKeys = st.thumbKeys + landed) }
        }
    }

    // The grid's visible tiles (indexes), for the tiles within reach of the scroll.
    private var visible: IntRange? = null

    /** The grid shows the tiles [first]..[last]: those within reach get their thumbnails. */
    fun visibleRange(first: Int, last: Int) {
        visible = first..last
        nearChanged()
    }

    private fun nearChanged() {
        val v = visible ?: return
        val items = _state.value.items
        val from = maxOf(0, v.first - GridThumbFetcher.REACH_BEFORE)
        val to = minOf(items.size, v.last + 1 + GridThumbFetcher.REACH_AFTER)
        if (from < to) tiles.near(items.subList(from, to).filter { it.asset == null }.map { it.path })
    }

    // Settings' Clear thumbnail cache: the tiles marked as having none are asked for again.
    init {
        viewModelScope.launch {
            var seen = ThumbStore.clears.value
            ThumbStore.clears.collect { n -> if (n != seen) { seen = n; tiles.forgetNone(); undecodable.clear() } }
        }
    }

    /** A synced tile is on screen (no thumbnail key yet: fetched first). */
    fun tileShown(item: Item) { if (item.asset == null) tiles.shown(item.path, needsThumb = _state.value.thumbKeyOf(item) == null) }

    fun tileGone(item: Item) { if (item.asset == null) tiles.gone(item.path) }

    /** A synced tile's thumbnail is no longer kept (the cache was cleared): fetched again. */
    fun thumbLost(item: Item) {
        if (item.asset != null) return
        forgetThumb(item)
        tiles.shown(item.path, needsThumb = true)
    }

    private fun forgetThumb(item: Item) = _state.update { st -> st.copy(thumbKeys = st.thumbKeys + (item.path to null)) }

    // Synced tiles whose bytes failed to decode this search: a second time, none.
    private val undecodable = HashSet<String>()

    /** A synced tile's bytes ([key]) didn't decode: dropped and fetched again, or none the second time. */
    fun thumbUndecodable(item: Item, key: String) {
        if (item.asset != null) return
        viewModelScope.launch { ThumbStore.discard(key) }
        if (undecodable.add(item.path)) { thumbLost(item); return }
        forgetThumb(item)
        tiles.markNone(item.path)
    }

    fun onAppearInitial() {
        if (didAppear) return
        didAppear = true
        viewModelScope.launch { loadTags() }
        viewModelScope.launch { resetAndLoadFirstPage() }
    }

    fun cleanUpTrimPreviews() {
        for (u in trimPreviewUrls.values) if (u.contains("otc-trim-")) try { File(Uri.parse(u).path ?: "").delete() } catch (_: Exception) {}
        trimPreviewUrls.clear()
    }

    fun switchSource(s: Source) {
        if (_state.value.source == s) return
        _state.update { it.copy(source = s) }
        viewModelScope.launch { resetAndLoadFirstPage() }
    }

    fun setCaption(c: String) = _state.update { it.copy(caption = c) }

    fun addChip(t: String) {
        val x = t.trim()
        if (x.isEmpty() || x in _state.value.chips) return
        _state.update { it.copy(chips = it.chips + x) }
        viewModelScope.launch { resetAndLoadFirstPage() }
    }

    fun removeChip(t: String) {
        _state.update { it.copy(chips = it.chips - t) }
        viewModelScope.launch { resetAndLoadFirstPage() }
    }

    private suspend fun loadTags() {
        try {
            val resp = OTCConnection.request { it.setReqGetTags(GetTags.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_TAGS_LIST) _state.update { it.copy(tags = resp.respTagsList.tagsList) }
        } catch (_: Exception) {}
    }

    /**
     * The grid starts over (a new source, a tag added or removed). What is
     * picked stays picked (State.picks): it is published from there, not
     * from the grid, so a photo picked from Phone and one from Synced can
     * go in the same post.
     */
    suspend fun resetAndLoadFirstPage() {
        searchGeneration += 1
        tiles.reset()
        undecodable.clear()
        _state.update { it.copy(loading = false, endReached = false, items = emptyList()) }
        when (_state.value.source) {
            Source.SYNCED -> { token = ""; fetchPage(overrideToken = "") }
            Source.PHONE -> { localAssets = null; localLoadedCount = 0; loadLocalPage() }
        }
    }

    fun loadMoreIfNeeded(item: Item?) {
        val st = _state.value
        if (item == null || st.loading || st.endReached) return
        val idx = st.items.indexOf(item)
        if (idx < 0 || idx < st.items.size - 12) return
        // In the ViewModel's scope, not the asking tile's: with a 12-photo
        // first page that tile is the first one, and scrolling it away
        // cancelled the page, with nothing left to ask again.
        viewModelScope.launch { when (st.source) { Source.SYNCED -> fetchPage(); Source.PHONE -> loadLocalPage() } }
    }

    private suspend fun fetchPage(overrideToken: String? = null) {
        if (_state.value.loading || _state.value.endReached) return
        val mine = searchGeneration
        _state.update { it.copy(loading = true) }
        try {
            val chips = _state.value.chips
            val requestToken = overrideToken ?: token ?: ""
            // Bigger pages from a device known to leave the thumbnails out (GridThumbs.kt).
            val firstLimit = if (ThumbStore.omitsThumbnails()) FIRST_PAGE_LIMIT_WITHOUT_THUMBS else FIRST_PHOTO_PAGE_LIMIT
            // The device this page is asked of: what it brings is kept for it only.
            val thumbScope = ThumbStore.scope()
            val resp = OTCConnection.request {
                // A grid: its tiles' small thumbnails (release 111), left out
                // of the page (release 113: from the cache, or GetThumbnails).
                val sp = SearchPhotos.newBuilder().addAllTags(chips).setToken(requestToken).setIncludeVideos(true).setSmallThumbnails(true).setOmitThumbnails(true)
                // A new search (Synced, a tag added or removed) gets a first
                // page of its own size; scrolling on, a bigger one.
                if (requestToken.isEmpty()) sp.limit = firstLimit
                else {
                    sp.limit = NEXT_PAGE_LIMIT_WITHOUT_THUMBS
                    // The photos the grid holds: a page asked again (its answer
                    // was lost) comes back as the same page (SearchPhotos.have).
                    sp.have = _state.value.items.size
                }
                it.setReqSearchPhotos(sp)
            }
            if (mine != searchGeneration) return
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) return
            val lof = resp.respListOfFiles
            val landed = ThumbStore.land(lof.filesList, thumbScope)
            if (mine != searchGeneration) return
            val newItems = lof.filesList.map { f -> Item("${f.path}#${f.hash}#${f.byteSize}", f.path, thumbKey = landed.keys[f.path], isVideo = f.mime.startsWith("video/")) }
            _state.update { st -> val existing = st.items.map { it.id }.toSet(); st.copy(items = st.items + newItems.filter { it.id !in existing }) }
            // Asked for as their tiles show or come near.
            tiles.note(landed.fetch, landed.refetch)
            nearChanged()
            token = lof.token.ifEmpty { null }
            _state.update { it.copy(endReached = token == null) }
        } catch (_: Exception) {
        } finally {
            if (mine == searchGeneration) _state.update { it.copy(loading = false) }
        }
    }

    /**
     * Issue #150: where the system camera saves a new photo or video - a
     * fresh entry in the phone's gallery (DCIM/Camera, as the Camera app
     * would), so once taken it is an ordinary phone item. No permission is
     * needed for an app's own MediaStore entries. Null if it can't be made.
     */
    fun newCaptureUri(video: Boolean): android.net.Uri? {
        val stamp = java.text.SimpleDateFormat("yyyyMMdd_HHmmss", java.util.Locale.US).format(java.util.Date())
        val values = android.content.ContentValues().apply {
            put(android.provider.MediaStore.MediaColumns.DISPLAY_NAME, (if (video) "VID_" else "IMG_") + stamp + if (video) ".mp4" else ".jpg")
            put(android.provider.MediaStore.MediaColumns.MIME_TYPE, if (video) "video/mp4" else "image/jpeg")
            put(android.provider.MediaStore.MediaColumns.RELATIVE_PATH, android.os.Environment.DIRECTORY_DCIM + "/Camera")
        }
        val collection = if (video) android.provider.MediaStore.Video.Media.EXTERNAL_CONTENT_URI else android.provider.MediaStore.Images.Media.EXTERNAL_CONTENT_URI
        return try { OTCApp.instance.contentResolver.insert(collection, values) } catch (e: Exception) { null }
    }

    /**
     * The camera came back: a saved capture becomes the first phone item,
     * selected; a cancelled one's empty gallery entry is removed.
     */
    fun onCaptured(uri: android.net.Uri, video: Boolean, saved: Boolean) {
        val resolver = OTCApp.instance.contentResolver
        if (!saved) {
            try { resolver.delete(uri, null, null) } catch (_: Exception) {}
            return
        }
        viewModelScope.launch {
            val item = withContext(Dispatchers.IO) {
                var name = ""
                var mime = if (video) "video/mp4" else "image/jpeg"
                resolver.query(uri, arrayOf(android.provider.MediaStore.MediaColumns.DISPLAY_NAME, android.provider.MediaStore.MediaColumns.MIME_TYPE), null, null, null)?.use { c ->
                    if (c.moveToFirst()) { name = c.getString(0) ?: ""; mime = c.getString(1) ?: mime }
                }
                val asset = PhotoSync.Asset(android.content.ContentUris.parseId(uri), uri, name, mime, System.currentTimeMillis(), video)
                // Ready before the tile first draws, so the capture shows at once.
                phoneThumbnail(asset)?.let { ThumbCache.put("local#${asset.id}", it) }
                Item("local#${asset.id}", "", asset = asset, isVideo = video)
            }
            // To the phone's photos first, waited for: a start-over still to
            // come would empty the grid after the capture went in (it took
            // the capture out, and its page brought it back a second time).
            if (_state.value.source != Source.PHONE) {
                _state.update { it.copy(source = Source.PHONE) }
                resetAndLoadFirstPage()
            }
            _state.update { st ->
                st.copy(items = listOf(item) + st.items.filter { it.id != item.id }, picks = st.picks.add(item))
            }
        }
    }

    /** Issue #49: the camera roll, newest first, thumbnails from MediaStore. */
    private suspend fun loadLocalPage() {
        if (_state.value.loading || _state.value.endReached) return
        val mine = searchGeneration
        _state.update { it.copy(loading = true) }
        try {
            if (!PhotoSync.hasPermission()) {
                _state.update { it.copy(alert = S.appSocialComposePhotosAccess(), endReached = true) }
                return
            }
            val all = localAssets ?: withContext(Dispatchers.IO) { PhotoSync.fetchNewAssets(includeVideos = true, sinceMs = 0, newestFirst = true) }
            if (mine != searchGeneration) return
            localAssets = all
            if (localLoadedCount >= all.size) { _state.update { it.copy(endReached = true) }; return }
            val end = minOf(localLoadedCount + localPageSize, all.size)
            val newItems = (localLoadedCount until end).map { i -> all[i].let { a -> Item("local#${a.id}", "", asset = a, isVideo = a.isVideo) } }
            _state.update { it.copy(items = it.items + newItems) }
            localLoadedCount = end
            _state.update { it.copy(endReached = localLoadedCount >= all.size) }
        } finally {
            if (mine == searchGeneration) _state.update { it.copy(loading = false) }
        }
    }

    /** A tile tapped: picked, last in the post, or no longer picked. */
    fun toggleSelect(item: Item) = _state.update { st -> st.copy(picks = st.picks.toggle(item)) }

    /** The order strip's ×: no longer picked, whichever source it came from. */
    fun unpick(id: String) = _state.update { st -> st.copy(picks = st.picks.remove(id)) }

    /** Issue #48: explicit reordering from the "Selected" strip. */
    fun moveSelected(id: String, offset: Int) = _state.update { st -> st.copy(picks = st.picks.move(id, offset)) }

    /** Issue #108: trimming needs the real video. */
    fun openTrimmer(item: Item) {
        trimPreviewUrls[item.id]?.let { _state.update { s -> s.copy(trimming = item.id to it) }; return }
        _state.update { it.copy(trimLoadingId = item.id) }
        viewModelScope.launch {
            try {
                val url = if (item.asset != null) item.asset.uri.toString() else downloadForTrimming(item.path)
                trimPreviewUrls[item.id] = url
                _state.update { it.copy(trimming = item.id to url) }
            } catch (e: Exception) {
                // An error of ours is worded now (TextIOException), the
                // device's or the system's as they come.
                _state.update { it.copy(alert = S.appSocialComposeTrimLoadFailed("${e.message}")) }
            } finally {
                _state.update { it.copy(trimLoadingId = null) }
            }
        }
    }

    fun applyTrim(range: TrimRange?) {
        val t = _state.value.trimming ?: return
        _state.update { st -> st.copy(trims = if (range != null) st.trims + (t.first to range) else st.trims - t.first, trimming = null) }
    }

    fun closeTrimmer() = _state.update { it.copy(trimming = null) }

    // In pieces: a synced video can be far bigger than the app's heap.
    private suspend fun downloadForTrimming(path: String): String {
        val ext = path.substringAfterLast('.', "mp4")
        val f = File(OTCApp.instance.cacheDir, "otc-trim-${UUID.randomUUID()}.$ext")
        val meta = try {
            ChunkedDownload.download(path, "", f)
        } catch (e: ChunkedDownload.Refused) {
            // The device's own words, or "Unexpected response" when it gave
            // none (as iOS) - worded when the alert shows it.
            throw TextIOException(e.text)
        }
        if (meta.size == 0L) { f.delete(); throw TextIOException(S.appSocialComposeEmptyResponse()) }
        return f.toURI().toString()
    }

    fun publish(onPosted: () -> Unit) {
        val st = _state.value
        if (st.picks.isEmpty() || st.caption.isBlank() || st.publishing) return
        _state.update { it.copy(publishing = true) }
        // Not the composer's own scope: closing it while the post goes out
        // doesn't stop the post, as on iOS (its publish task outlives the
        // sheet), and the feed is told once it is in.
        ComposerPublish.scope.launch {
            try {
                // Issue #48: in the picks' order, from whichever source each came.
                val selectedItems = st.picks.picked
                val phoneCount = selectedItems.count { it.asset != null }
                val secrets = SecretsStore.loadOrCreate()
                val targetDir = "/android/${secrets.deviceId.value}/"
                val resolved = mutableListOf<String>()
                var uploaded = 0
                for (item in selectedItems) {
                    if (item.asset != null) {
                        uploaded += 1
                        _state.update { it.copy(publishStatus = S.appSocialComposeUploading(uploaded, phoneCount)) }
                        resolved += withContext(Dispatchers.IO) { PhotoSync.uploadIfNeeded(item.asset, targetDir) }
                    } else {
                        resolved += item.path
                    }
                }
                _state.update { it.copy(publishStatus = S.appSocialComposePublishing()) }
                val pbTrims = selectedItems.mapIndexedNotNull { i, item ->
                    val r = st.trims[item.id] ?: return@mapIndexedNotNull null
                    VideoTrim.newBuilder().setPath(resolved[i]).setStartSecs(r.start).setEndSecs(r.end).build()
                }
                val resp = OTCConnection.request { it.setReqNewSocialPublication(NewSocialPublication.newBuilder().setText(st.caption).addAllPaths(resolved).addAllTrims(pbTrims)) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_NEW_SOCIAL && !resp.error) {
                    // Posted: nothing of it is left for the next post (one
                    // more tap used to post it twice).
                    _state.update { it.posted() }
                    onPosted()
                }
                // The device's own words, already in the app's language.
                else _state.update { it.copy(alert = if (resp.error) S.appSocialComposePublishFailedDetail(resp.errorMessage) else S.appSocialComposePublishFailed()) }
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Exception) {
                _state.update { it.copy(alert = S.appSocialComposePublishFailedDetail("${e.message}")) }
            } finally {
                _state.update { it.copy(publishing = false, publishStatus = null) }
            }
        }
    }

    fun dismissAlert() = _state.update { it.copy(alert = null) }
}

/**
 * What the composer has picked, in post order (issue #48), each with the
 * item it is: a pick stays picked while the grid shows the other source or
 * other tags, and publish takes the items from here, never from the grid -
 * which used to drop every pick at a change of source or tag (it published
 * only what the grid held). As ComposerPicks in NewPostPicker.swift.
 */
data class ComposerPicks(
    val order: List<String> = emptyList(),
    private val byId: Map<String, NewPostPickerViewModel.Item> = emptyMap(),
) {
    val size: Int get() = order.size

    fun isEmpty(): Boolean = order.isEmpty()

    operator fun contains(id: String): Boolean = id in byId

    /** Its place in the post, from 1; null when it isn't picked. */
    fun number(id: String): Int? = order.indexOf(id).takeIf { it >= 0 }?.plus(1)

    fun item(id: String): NewPostPickerViewModel.Item? = byId[id]

    /** The picks, in post order. */
    val picked: List<NewPostPickerViewModel.Item> get() = order.mapNotNull { byId[it] }

    /** A tap on a tile: picked, last in the post - or no longer picked. */
    fun toggle(item: NewPostPickerViewModel.Item): ComposerPicks = if (item.id in byId) remove(item.id) else add(item)

    /** Picked (last in the post) if it wasn't; one already picked keeps its place. */
    fun add(item: NewPostPickerViewModel.Item): ComposerPicks =
        if (item.id in byId) copy(byId = byId + (item.id to item)) else ComposerPicks(order + item.id, byId + (item.id to item))

    fun remove(id: String): ComposerPicks = if (id !in byId) this else ComposerPicks(order - id, byId - id)

    /** Issue #48: one place earlier ([offset] -1) or later (1) in the post. */
    fun move(id: String, offset: Int): ComposerPicks {
        val idx = order.indexOf(id)
        val to = idx + offset
        if (idx < 0 || to !in order.indices) return this
        return copy(order = order.toMutableList().also { val t = it[idx]; it[idx] = it[to]; it[to] = t })
    }
}

/** Where posts are published: the app's, so closing the composer doesn't stop one on its way. */
private object ComposerPublish {
    val scope by lazy { kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.SupervisorJob() + Dispatchers.Main.immediate) }
}

/**
 * The composer's own view model store: a new view model each time it
 * opens - nothing of the last opening left (its caption, picks and trims
 * survived a post, and one more tap posted it twice), and the phone's
 * photos read again - let go when it closes. As iOS's @StateObject, made
 * with each sheet.
 */
private class ComposerStore : ViewModelStoreOwner {
    override val viewModelStore = ViewModelStore()
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun NewPostPickerView(onDismiss: () -> Unit, onPosted: () -> Unit) {
    // Its own view model for this opening (ComposerStore), let go as it closes.
    val store = remember { ComposerStore() }
    DisposableEffect(store) { onDispose { store.viewModelStore.clear() } }
    val vm: NewPostPickerViewModel = viewModel(viewModelStoreOwner = store)
    val st by vm.state.collectAsState()
    val context = LocalContext.current
    var query by remember { mutableStateOf("") }
    var showSuggest by remember { mutableStateOf(false) }

    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { vm.onAppearInitial() }
    LaunchedEffect(Unit) {
        if (PhotoSync.hasPermission(context)) vm.onAppearInitial()
        else permission.launch(mediaPermissions())
    }
    DisposableEffect(Unit) { onDispose { vm.cleanUpTrimPreviews() } }

    // Issue #150: the system camera, for a photo or a video - no CAMERA
    // permission needed, the camera app does the capturing.
    var pendingCapture by remember { mutableStateOf<Pair<android.net.Uri, Boolean>?>(null) }
    var captureMenu by remember { mutableStateOf(false) }
    val takePhoto = rememberLauncherForActivityResult(ActivityResultContracts.TakePicture()) { saved ->
        pendingCapture?.let { (uri, video) -> vm.onCaptured(uri, video, saved) }
        pendingCapture = null
    }
    val takeVideo = rememberLauncherForActivityResult(ActivityResultContracts.CaptureVideo()) { saved ->
        pendingCapture?.let { (uri, video) -> vm.onCaptured(uri, video, saved) }
        pendingCapture = null
    }
    fun capture(video: Boolean) {
        val uri = vm.newCaptureUri(video) ?: return
        pendingCapture = uri to video
        try {
            if (video) takeVideo.launch(uri) else takePhoto.launch(uri)
        } catch (e: android.content.ActivityNotFoundException) {
            vm.onCaptured(uri, video, false)
            pendingCapture = null
        }
    }

    val suggestions = remember(query, st.tags) {
        val q = query.trim().lowercase()
        if (q.isEmpty()) emptyList() else st.tags.filter { it.lowercase().startsWith(q) }.take(12)
    }
    fun acceptQuery() {
        val q = query.trim()
        if (q.isEmpty()) return
        vm.addChip(if (suggestions.size == 1) suggestions[0] else q)
        query = ""
        showSuggest = false
    }

    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Scaffold(topBar = {
            TopAppBar(title = { Text(S.appSocialComposeTitle().resolve()) }, navigationIcon = { TextButton(onClick = onDismiss) { Text(S.commonCancel().resolve()) } })
        }) { pad ->
            Column(Modifier.padding(pad).fillMaxSize()) {
                SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp)) {
                    NewPostPickerViewModel.Source.values().forEachIndexed { i, s ->
                        SegmentedButton(selected = st.source == s, onClick = { vm.switchSource(s) }, shape = SegmentedButtonDefaults.itemShape(i, 2)) {
                            Text((if (s == NewPostPickerViewModel.Source.PHONE) S.appSocialComposeSourcePhone() else S.appSocialComposeSourceSynced()).resolve())
                        }
                    }
                }
                if (st.source == NewPostPickerViewModel.Source.SYNCED) {
                    Column(Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerLow).padding(vertical = 8.dp)) {
                        if (st.chips.isNotEmpty()) {
                            Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                st.chips.forEach { chip ->
                                    Row(Modifier.background(Color(0x262196F3), CircleShape).padding(horizontal = 8.dp, vertical = 4.dp)) {
                                        Text(chip); Spacer(Modifier.width(6.dp)); Text("×", Modifier.clickable { vm.removeChip(chip) })
                                    }
                                }
                            }
                        }
                        Row(Modifier.padding(horizontal = 12.dp), verticalAlignment = Alignment.CenterVertically) {
                            OTCTextField(
                                value = query, onValueChange = { query = it; showSuggest = true }, singleLine = true, placeholder = { Text(S.appSocialComposeFilterPlaceholder().resolve()) },
                                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search), keyboardActions = KeyboardActions(onSearch = { acceptQuery() }),
                                modifier = Modifier.weight(1f),
                            )
                            TextButton(onClick = { acceptQuery() }) { Text(S.commonSearch().resolve()) }
                        }
                        if (showSuggest && suggestions.isNotEmpty()) {
                            Column(Modifier.padding(horizontal = 12.dp).clip(RoundedCornerShape(8.dp)).background(Color(0x14808080))) {
                                suggestions.forEach { s -> Text(s, Modifier.fillMaxWidth().clickable { vm.addChip(s); query = ""; showSuggest = false }.padding(10.dp, 6.dp)) }
                            }
                        }
                    }
                }

                BoxWithConstraints(Modifier.weight(1f)) {
                    // The tiles' side, as the grid lays them out: what thumbnails decode to.
                    val tilePx = if (constraints.hasBoundedWidth) gridCellPx(constraints.maxWidth, LocalDensity.current, 10.dp, 8.dp, count = 3)
                        else with(LocalDensity.current) { 240.dp.roundToPx() }
                    val gridState = rememberLazyGridState()
                    // The tiles on screen: those within reach get their thumbnails.
                    LaunchedEffect(gridState) {
                        snapshotFlow { gridState.layoutInfo.visibleItemsInfo.let { v -> (v.firstOrNull()?.index ?: 0) to (v.lastOrNull()?.index ?: -1) } }
                            .collect { (first, last) -> if (last >= first) vm.visibleRange(first, last) }
                    }
                    LazyVerticalGrid(columns = GridCells.Fixed(3), state = gridState, contentPadding = PaddingValues(10.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.fillMaxSize()) {
                        items(st.items, key = { it.id }) { item ->
                            val thumbKey = st.thumbKeyOf(item)
                            LaunchedEffect(item.id) { vm.loadMoreIfNeeded(item) }
                            DisposableEffect(item.id, thumbKey == null) {
                                vm.tileShown(item)
                                onDispose { vm.tileGone(item) }
                            }
                            PickTile(item, thumbKey, st.picks.number(item.id), tilePx, onLost = { vm.thumbLost(item) }, onUndecodable = { k -> vm.thumbUndecodable(item, k) }) { vm.toggleSelect(item) }
                        }
                        if (st.loading) item(span = { GridItemSpan(maxLineSpan) }) { Box(Modifier.fillMaxWidth().height(60.dp), contentAlignment = Alignment.Center) { CircularProgressIndicator() } }
                    }
                }

                if (!st.picks.isEmpty()) SelectedOrderStrip(vm, st)
                val status = st.publishStatus
                if (st.publishing && status != null) {
                    Text(status.resolve(), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 12.dp, vertical = 4.dp))
                } else if (st.captionNeeded) {
                    // Why Publish is greyed out (issue #96: a post needs a caption).
                    Text(
                        S.appSocialComposeCaptionNeeded().resolve(), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(horizontal = 12.dp, vertical = 4.dp).semantics { liveRegion = LiveRegionMode.Polite },
                    )
                }
                HorizontalDivider()
                Row(Modifier.fillMaxWidth().padding(10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Box {
                        IconButton(onClick = { captureMenu = true }, enabled = !st.publishing) {
                            Icon(Icons.Default.PhotoCamera, contentDescription = S.appSocialComposeCamera().resolve())
                        }
                        DropdownMenu(expanded = captureMenu, onDismissRequest = { captureMenu = false }) {
                            DropdownMenuItem(text = { Text(S.androidComposeTakePhoto().resolve()) }, onClick = { captureMenu = false; capture(video = false) })
                            DropdownMenuItem(text = { Text(S.androidComposeRecordVideo().resolve()) }, onClick = { captureMenu = false; capture(video = true) })
                        }
                    }
                    // The placeholder smaller rather than broken beside a long
                    // Publish ("Bildunterschrift…" beside "Veröffentlichen (3)").
                    OTCTextField(value = st.caption, onValueChange = vm::setCaption, placeholder = { FitText(S.appSocialComposeCaptionPlaceholder().resolve(), minScale = 0.8f) }, modifier = Modifier.weight(1f))
                    Button(onClick = { vm.publish { onDismiss(); onPosted() } }, enabled = !st.picks.isEmpty() && st.caption.isNotBlank() && !st.publishing) {
                        Text(
                            when {
                                st.publishing -> S.appSocialComposePublishing()
                                st.picks.isEmpty() -> S.appSocialComposePublish()
                                else -> S.appSocialComposePublishCount(st.picks.size)
                            }.resolve()
                        )
                    }
                }
            }
        }
    }

    st.alert?.let { AlertDialog(onDismissRequest = { vm.dismissAlert() }, text = { Text(it.resolve()) }, confirmButton = { TextButton(onClick = { vm.dismissAlert() }) { Text(S.commonOk().resolve()) } }) }
    st.trimming?.let { (id, url) ->
        VideoTrimmerView(uri = url, existing = st.trims[id], onApply = { vm.applyTrim(it) }, onCancel = { vm.closeTrimmer() })
    }
}

fun mediaPermissions(): Array<String> = if (Build.VERSION.SDK_INT >= 33) {
    // ACCESS_MEDIA_LOCATION (issue #127) rides along with the media
    // permission: it is what lets the sync upload a photo's GPS tags intact.
    arrayOf(Manifest.permission.READ_MEDIA_IMAGES, Manifest.permission.READ_MEDIA_VIDEO, Manifest.permission.ACCESS_MEDIA_LOCATION)
} else if (Build.VERSION.SDK_INT >= 29) {
    arrayOf(Manifest.permission.READ_EXTERNAL_STORAGE, Manifest.permission.ACCESS_MEDIA_LOCATION)
} else {
    arrayOf(Manifest.permission.READ_EXTERNAL_STORAGE)
}

/** MediaStore's own 450 px thumbnail of a phone item (its cache makes this quick). */
private fun phoneThumbnail(asset: PhotoSync.Asset): Bitmap? = try {
    if (Build.VERSION.SDK_INT >= 29) OTCApp.instance.contentResolver.loadThumbnail(asset.uri, Size(450, 450), null) else null
} catch (e: Exception) { null }

// A phone item's bitmap is MediaStore's 450 px thumbnail, loaded when its
// tile shows and kept in ThumbCache; a synced one's is decoded off the main
// thread to [sidePx].
@Composable
private fun thumbOf(
    item: NewPostPickerViewModel.Item, thumbKey: String?, sidePx: Int, onLost: () -> Unit = {}, onUndecodable: (String) -> Unit = {},
): Bitmap? {
    val asset = item.asset
    if (asset != null) return rememberOffMain(item.id, { ThumbCache.get(item.id) }) {
        withContext(Dispatchers.IO) { phoneThumbnail(asset) }?.also { ThumbCache.put(item.id, it) }
    }
    return rememberTileThumb(thumbKey, sidePx, onUndecodable = { thumbKey?.let(onUndecodable) }) {
        thumbKey?.let { ThumbStore.load(it) ?: run { onLost(); null } }
    }
}

// thumbKey apart from item: Item compares by id alone, and a thumbnail that
// lands changes only its key.
@Composable
private fun PickTile(
    item: NewPostPickerViewModel.Item, thumbKey: String?, selectionNumber: Int?, sidePx: Int, onLost: () -> Unit, onUndecodable: (String) -> Unit, onTap: () -> Unit,
) {
    val bmp = thumbOf(item, thumbKey, sidePx, onLost, onUndecodable)
    val selected = selectionNumber != null
    Box(Modifier.aspectRatio(1f).clip(RoundedCornerShape(8.dp)).background(Color(0x33808080)).clickable(onClick = onTap)) {
        if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize().alpha(if (selected) 0.75f else 1f), contentScale = ContentScale.Crop)
        if (selected) Box(Modifier.fillMaxSize().border(3.dp, MaterialTheme.colorScheme.primary, RoundedCornerShape(8.dp)))
        if (item.isVideo) {
            Box(Modifier.align(Alignment.BottomStart).padding(6.dp).size(20.dp).background(Color(0x8C000000), CircleShape), contentAlignment = Alignment.Center) {
                Icon(Icons.Default.PlayArrow, null, tint = Color.White, modifier = Modifier.size(12.dp))
            }
        }
        if (selectionNumber != null) {
            Box(Modifier.align(Alignment.TopEnd).padding(6.dp).size(22.dp).background(MaterialTheme.colorScheme.primary, CircleShape), contentAlignment = Alignment.Center) {
                Text("$selectionNumber", color = Color.White, fontSize = 12.sp, fontWeight = FontWeight.Bold)
            }
        }
    }
}

@Composable
private fun SelectedOrderStrip(vm: NewPostPickerViewModel, st: NewPostPickerViewModel.State) {
    Column(Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerLow).padding(vertical = 6.dp)) {
        Text(S.appSocialComposeOrder().resolve(), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 12.dp))
        Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            // Every pick, the ones the grid doesn't show now (the other
            // source, other tags) included.
            st.picks.order.forEachIndexed { index, id ->
                val item = st.picks.item(id) ?: return@forEachIndexed
                val thumbKey = st.thumbKeyOf(item)
                // A synced pick without a thumbnail, while the grid shows
                // something else: asked for all the same.
                LaunchedEffect(id, thumbKey == null) { if (thumbKey == null) vm.tileShown(item) }
                SelectedThumb(
                    item, thumbKey, position = index + 1, canMoveLeft = index > 0, canMoveRight = index < st.picks.size - 1,
                    moveLeft = { vm.moveSelected(id, -1) }, moveRight = { vm.moveSelected(id, 1) }, remove = { vm.unpick(id) },
                    trim = if (item.isVideo) ({ vm.openTrimmer(item) }) else null, trimRange = st.trims[id], trimLoading = st.trimLoadingId == id,
                )
            }
        }
    }
}

@Composable
private fun SelectedThumb(
    item: NewPostPickerViewModel.Item, thumbKey: String?, position: Int, canMoveLeft: Boolean, canMoveRight: Boolean,
    moveLeft: () -> Unit, moveRight: () -> Unit, remove: () -> Unit, trim: (() -> Unit)?, trimRange: TrimRange?, trimLoading: Boolean,
) {
    val bmp = thumbOf(item, thumbKey, with(LocalDensity.current) { 60.dp.roundToPx() })
    Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Box(Modifier.size(64.dp)) {
            Box(Modifier.size(60.dp).clip(RoundedCornerShape(6.dp)).background(Color(0x33808080)).clickable(enabled = trim != null && !trimLoading) { trim?.invoke() }) {
                if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
                if (trim != null) {
                    Box(Modifier.align(Alignment.BottomStart).padding(2.dp).background(if (trimRange == null) Color(0x99000000) else MaterialTheme.colorScheme.primary, CircleShape).padding(horizontal = 6.dp, vertical = 2.dp)) {
                        if (trimLoading) CircularProgressIndicator(Modifier.size(10.dp), strokeWidth = 1.dp, color = Color.White)
                        // Smaller rather than broken inside the word, in the
                        // thumbnail's width (FitText).
                        else FitText(trimRange?.let { "✂ ${formatTimecode(it.length)}" } ?: S.appSocialComposeTrimBadge().resolve(),
                            style = LocalTextStyle.current.copy(fontSize = 11.sp, fontWeight = FontWeight.SemiBold), color = Color.White, minScale = 0.7f)
                    }
                }
            }
            Icon(Icons.Default.Cancel, S.commonRemove().resolve(), Modifier.align(Alignment.TopEnd).offset(x = 2.dp, y = (-2).dp).size(18.dp).clickable(onClick = remove), tint = Color.White)
        }
        Row(verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = moveLeft, enabled = canMoveLeft, modifier = Modifier.size(22.dp)) { Icon(Icons.Default.ChevronLeft, S.androidComposeMoveLeft().resolve(), Modifier.size(18.dp)) }
            Text("$position", style = MaterialTheme.typography.labelSmall)
            IconButton(onClick = moveRight, enabled = canMoveRight, modifier = Modifier.size(22.dp)) { Icon(Icons.Default.ChevronRight, S.androidComposeMoveRight().resolve(), Modifier.size(18.dp)) }
        }
    }
}
