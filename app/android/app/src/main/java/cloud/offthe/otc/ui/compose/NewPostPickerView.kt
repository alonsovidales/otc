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
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.core.content.ContextCompat
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.ChunkedDownload
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.GetTags
import cloud.offthe.otc.proto.NewSocialPublication
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.proto.VideoTrim
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.ui.common.ThumbCache
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.gridCellPx
import cloud.offthe.otc.ui.common.rememberOffMain
import cloud.offthe.otc.ui.common.rememberTileThumb
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

    // thumbKey: a synced item's thumbnail in ThumbStore. A phone item's comes
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
        val selectedOrder: List<String> = emptyList(),
        val trims: Map<String, TrimRange> = emptyMap(),
        val trimming: Pair<String, String>? = null,
        val trimLoadingId: String? = null,
        val caption: String = "",
        val publishing: Boolean = false,
        val publishStatus: String = "",
        val alert: String? = null,
    )

    private val _state = MutableStateFlow(State())
    val state = _state
    private var token: String? = null
    private var localAssets: List<PhotoSync.Asset>? = null
    private var localLoadedCount = 0
    private var didAppear = false
    private val trimPreviewUrls = mutableMapOf<String, String>()
    private val localPageSize = 60

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

    suspend fun resetAndLoadFirstPage() {
        _state.update { it.copy(loading = false, endReached = false, items = emptyList(), selectedOrder = emptyList()) }
        when (_state.value.source) {
            Source.SYNCED -> { token = ""; fetchPage(overrideToken = "") }
            Source.PHONE -> { localAssets = null; localLoadedCount = 0; loadLocalPage() }
        }
    }

    suspend fun loadMoreIfNeeded(item: Item?) {
        val st = _state.value
        if (item == null || st.loading || st.endReached) return
        val idx = st.items.indexOf(item)
        if (idx >= 0 && idx >= st.items.size - 12) when (st.source) { Source.SYNCED -> fetchPage(); Source.PHONE -> loadLocalPage() }
    }

    private suspend fun fetchPage(overrideToken: String? = null) {
        if (_state.value.loading || _state.value.endReached) return
        _state.update { it.copy(loading = true) }
        try {
            val chips = _state.value.chips
            val resp = OTCConnection.request { it.setReqSearchPhotos(SearchPhotos.newBuilder().addAllTags(chips).setToken(overrideToken ?: token ?: "").setIncludeVideos(true)) }
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) return
            val lof = resp.respListOfFiles
            ThumbStore.putAll(lof.filesList.filter { it.hasContent() }.map { f -> "${f.path}#${f.hash}#${f.size}" to f.content.toByteArray() })
            val newItems = lof.filesList.map { f -> "${f.path}#${f.hash}#${f.size}".let { id -> Item(id, f.path, thumbKey = if (f.hasContent()) id else null, isVideo = f.mime.startsWith("video/")) } }
            _state.update { st -> val existing = st.items.map { it.id }.toSet(); st.copy(items = st.items + newItems.filter { it.id !in existing }) }
            token = lof.token.ifEmpty { null }
            _state.update { it.copy(endReached = token == null) }
        } catch (_: Exception) {
        } finally {
            _state.update { it.copy(loading = false) }
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
            if (_state.value.source != Source.PHONE) switchSource(Source.PHONE)
            _state.update { st ->
                st.copy(
                    items = listOf(item) + st.items.filter { it.id != item.id },
                    selectedOrder = if (item.id in st.selectedOrder) st.selectedOrder else st.selectedOrder + item.id,
                )
            }
        }
    }

    /** Issue #49: the camera roll, newest first, thumbnails from MediaStore. */
    private suspend fun loadLocalPage() {
        if (_state.value.loading || _state.value.endReached) return
        _state.update { it.copy(loading = true) }
        try {
            if (!PhotoSync.hasPermission()) {
                _state.update { it.copy(alert = "Photos access is needed to pick from your phone.", endReached = true) }
                return
            }
            val all = localAssets ?: withContext(Dispatchers.IO) { PhotoSync.fetchNewAssets(includeVideos = true, sinceMs = 0, newestFirst = true) }.also { localAssets = it }
            if (localLoadedCount >= all.size) { _state.update { it.copy(endReached = true) }; return }
            val end = minOf(localLoadedCount + localPageSize, all.size)
            val newItems = (localLoadedCount until end).map { i -> all[i].let { a -> Item("local#${a.id}", "", asset = a, isVideo = a.isVideo) } }
            _state.update { it.copy(items = it.items + newItems) }
            localLoadedCount = end
            _state.update { it.copy(endReached = localLoadedCount >= all.size) }
        } finally {
            _state.update { it.copy(loading = false) }
        }
    }

    fun toggleSelect(id: String) = _state.update { st ->
        st.copy(selectedOrder = if (id in st.selectedOrder) st.selectedOrder - id else st.selectedOrder + id)
    }

    /** Issue #48: explicit reordering from the "Selected" strip. */
    fun moveSelected(id: String, offset: Int) = _state.update { st ->
        val idx = st.selectedOrder.indexOf(id)
        val newIdx = idx + offset
        if (idx < 0 || newIdx !in st.selectedOrder.indices) st
        else st.copy(selectedOrder = st.selectedOrder.toMutableList().also { val t = it[idx]; it[idx] = it[newIdx]; it[newIdx] = t })
    }

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
                _state.update { it.copy(alert = "Could not load that video to trim it: ${e.message}") }
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
            throw IllegalStateException(e.message?.ifEmpty { null } ?: "Empty response")
        }
        if (meta.size == 0L) { f.delete(); throw IllegalStateException("Empty response") }
        return f.toURI().toString()
    }

    fun publish(onPosted: () -> Unit) {
        val st = _state.value
        if (st.selectedOrder.isEmpty() || st.caption.isBlank() || st.publishing) return
        _state.update { it.copy(publishing = true) }
        viewModelScope.launch {
            try {
                val byId = st.items.associateBy { it.id }
                val selectedItems = st.selectedOrder.mapNotNull { byId[it] }
                val phoneCount = selectedItems.count { it.asset != null }
                val secrets = SecretsStore.loadOrCreate()
                val targetDir = "/android/${secrets.deviceId.value}/"
                val resolved = mutableListOf<String>()
                var uploaded = 0
                for (item in selectedItems) {
                    if (item.asset != null) {
                        uploaded += 1
                        _state.update { it.copy(publishStatus = "Uploading $uploaded of $phoneCount…") }
                        resolved += withContext(Dispatchers.IO) { PhotoSync.uploadIfNeeded(item.asset, targetDir) }
                    } else {
                        resolved += item.path
                    }
                }
                _state.update { it.copy(publishStatus = "Publishing…") }
                val pbTrims = selectedItems.mapIndexedNotNull { i, item ->
                    val r = st.trims[item.id] ?: return@mapIndexedNotNull null
                    VideoTrim.newBuilder().setPath(resolved[i]).setStartSecs(r.start).setEndSecs(r.end).build()
                }
                val resp = OTCConnection.request { it.setReqNewSocialPublication(NewSocialPublication.newBuilder().setText(st.caption).addAllPaths(resolved).addAllTrims(pbTrims)) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_NEW_SOCIAL && !resp.error) onPosted()
                else _state.update { it.copy(alert = if (resp.error) "Could not publish: ${resp.errorMessage}" else "Could not publish") }
            } catch (e: Exception) {
                _state.update { it.copy(alert = "Could not publish: ${e.message}") }
            } finally {
                _state.update { it.copy(publishing = false, publishStatus = "") }
            }
        }
    }

    fun dismissAlert() = _state.update { it.copy(alert = null) }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun NewPostPickerView(onDismiss: () -> Unit, onPosted: () -> Unit) {
    val vm: NewPostPickerViewModel = viewModel()
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
            TopAppBar(title = { Text("New Post") }, navigationIcon = { TextButton(onClick = onDismiss) { Text("Cancel") } })
        }) { pad ->
            Column(Modifier.padding(pad).fillMaxSize()) {
                SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp)) {
                    NewPostPickerViewModel.Source.values().forEachIndexed { i, s ->
                        SegmentedButton(selected = st.source == s, onClick = { vm.switchSource(s) }, shape = SegmentedButtonDefaults.itemShape(i, 2)) {
                            Text(if (s == NewPostPickerViewModel.Source.PHONE) "Phone" else "Synced")
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
                                value = query, onValueChange = { query = it; showSuggest = true }, singleLine = true, placeholder = { Text("Filter by tag…") },
                                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search), keyboardActions = KeyboardActions(onSearch = { acceptQuery() }),
                                modifier = Modifier.weight(1f),
                            )
                            TextButton(onClick = { acceptQuery() }) { Text("Search") }
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
                    LazyVerticalGrid(columns = GridCells.Fixed(3), contentPadding = PaddingValues(10.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.fillMaxSize()) {
                        items(st.items, key = { it.id }) { item ->
                            LaunchedEffect(item.id) { vm.loadMoreIfNeeded(item) }
                            val n = st.selectedOrder.indexOf(item.id).let { if (it < 0) null else it + 1 }
                            PickTile(item, n, tilePx) { vm.toggleSelect(item.id) }
                        }
                        if (st.loading) item(span = { GridItemSpan(maxLineSpan) }) { Box(Modifier.fillMaxWidth().height(60.dp), contentAlignment = Alignment.Center) { CircularProgressIndicator() } }
                    }
                }

                if (st.selectedOrder.isNotEmpty()) SelectedOrderStrip(vm, st)
                if (st.publishing && st.publishStatus.isNotEmpty()) {
                    Text(st.publishStatus, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 12.dp, vertical = 4.dp))
                }
                HorizontalDivider()
                Row(Modifier.fillMaxWidth().padding(10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Box {
                        IconButton(onClick = { captureMenu = true }, enabled = !st.publishing) {
                            Icon(Icons.Default.PhotoCamera, contentDescription = "Take a photo or video")
                        }
                        DropdownMenu(expanded = captureMenu, onDismissRequest = { captureMenu = false }) {
                            DropdownMenuItem(text = { Text("Take a photo") }, onClick = { captureMenu = false; capture(video = false) })
                            DropdownMenuItem(text = { Text("Record a video") }, onClick = { captureMenu = false; capture(video = true) })
                        }
                    }
                    OTCTextField(value = st.caption, onValueChange = vm::setCaption, placeholder = { Text("Write a caption…") }, modifier = Modifier.weight(1f))
                    Button(onClick = { vm.publish { onDismiss(); onPosted() } }, enabled = st.selectedOrder.isNotEmpty() && st.caption.isNotBlank() && !st.publishing) {
                        Text(if (st.publishing) "Publishing…" else "Publish" + if (st.selectedOrder.isEmpty()) "" else " (${st.selectedOrder.size})")
                    }
                }
            }
        }
    }

    st.alert?.let { AlertDialog(onDismissRequest = { vm.dismissAlert() }, text = { Text(it) }, confirmButton = { TextButton(onClick = { vm.dismissAlert() }) { Text("OK") } }) }
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
private fun thumbOf(item: NewPostPickerViewModel.Item, sidePx: Int): Bitmap? {
    val asset = item.asset
    if (asset != null) return rememberOffMain(item.id, { ThumbCache.get(item.id) }) {
        withContext(Dispatchers.IO) { phoneThumbnail(asset) }?.also { ThumbCache.put(item.id, it) }
    }
    return rememberTileThumb(item.thumbKey, sidePx) { item.thumbKey?.let { ThumbStore.load(it) } }
}

@Composable
private fun PickTile(item: NewPostPickerViewModel.Item, selectionNumber: Int?, sidePx: Int, onTap: () -> Unit) {
    val bmp = thumbOf(item, sidePx)
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
        Text("Order in post", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 12.dp))
        Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            st.selectedOrder.forEachIndexed { index, id ->
                val item = st.items.firstOrNull { it.id == id } ?: return@forEachIndexed
                SelectedThumb(
                    item, position = index + 1, canMoveLeft = index > 0, canMoveRight = index < st.selectedOrder.size - 1,
                    moveLeft = { vm.moveSelected(id, -1) }, moveRight = { vm.moveSelected(id, 1) }, remove = { vm.toggleSelect(id) },
                    trim = if (item.isVideo) ({ vm.openTrimmer(item) }) else null, trimRange = st.trims[id], trimLoading = st.trimLoadingId == id,
                )
            }
        }
    }
}

@Composable
private fun SelectedThumb(
    item: NewPostPickerViewModel.Item, position: Int, canMoveLeft: Boolean, canMoveRight: Boolean,
    moveLeft: () -> Unit, moveRight: () -> Unit, remove: () -> Unit, trim: (() -> Unit)?, trimRange: TrimRange?, trimLoading: Boolean,
) {
    val bmp = thumbOf(item, with(LocalDensity.current) { 60.dp.roundToPx() })
    Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Box(Modifier.size(64.dp)) {
            Box(Modifier.size(60.dp).clip(RoundedCornerShape(6.dp)).background(Color(0x33808080)).clickable(enabled = trim != null && !trimLoading) { trim?.invoke() }) {
                if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
                if (trim != null) {
                    Box(Modifier.align(Alignment.BottomStart).padding(2.dp).background(if (trimRange == null) Color(0x99000000) else MaterialTheme.colorScheme.primary, CircleShape).padding(horizontal = 6.dp, vertical = 2.dp)) {
                        if (trimLoading) CircularProgressIndicator(Modifier.size(10.dp), strokeWidth = 1.dp, color = Color.White)
                        else Text(trimRange?.let { "✂ ${formatTimecode(it.length)}" } ?: "✂ Trim", fontSize = 11.sp, fontWeight = FontWeight.SemiBold, color = Color.White)
                    }
                }
            }
            Icon(Icons.Default.Cancel, "Remove", Modifier.align(Alignment.TopEnd).offset(x = 2.dp, y = (-2).dp).size(18.dp).clickable(onClick = remove), tint = Color.White)
        }
        Row(verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = moveLeft, enabled = canMoveLeft, modifier = Modifier.size(22.dp)) { Icon(Icons.Default.ChevronLeft, "Move left", Modifier.size(18.dp)) }
            Text("$position", style = MaterialTheme.typography.labelSmall)
            IconButton(onClick = moveRight, enabled = canMoveRight, modifier = Modifier.size(22.dp)) { Icon(Icons.Default.ChevronRight, "Move right", Modifier.size(18.dp)) }
        }
    }
}
