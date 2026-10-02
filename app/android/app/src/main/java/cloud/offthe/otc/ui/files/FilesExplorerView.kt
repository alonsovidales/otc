// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import android.content.Context
import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Description
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Image
import androidx.compose.material.icons.outlined.Circle
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.net.ChunkedUpload
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.DelFile
import cloud.offthe.otc.proto.File as PbFile
import cloud.offthe.otc.proto.GetFile
import cloud.offthe.otc.proto.GetThumbnails
import cloud.offthe.otc.proto.HasFile
import cloud.offthe.otc.proto.LinkFile
import cloud.offthe.otc.proto.ListFiles
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.ShareFilesLink
import cloud.offthe.otc.proto.SharedGallerySource
import cloud.offthe.otc.ui.share.SharedGalleryShareFlow
import cloud.offthe.otc.ui.common.SelectionActionBar
import cloud.offthe.otc.ui.common.SelectionActionTask
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.Toast
import cloud.offthe.otc.ui.common.formatBytes
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.io.InputStream
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.material.icons.filled.History
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material.icons.outlined.LockOpen
import androidx.compose.ui.text.font.FontWeight
import cloud.offthe.otc.proto.ListFileVersions
import cloud.offthe.otc.proto.SetUploadOnly
import java.text.DateFormat
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.automirrored.filled.ViewList
import androidx.compose.material.icons.filled.GridView
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import cloud.offthe.otc.ui.common.FileTypeIcon
import cloud.offthe.otc.ui.common.decodeBitmap
import androidx.compose.ui.graphics.asAndroidBitmap
import cloud.offthe.otc.ui.gallery.ImageModal
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import java.util.Date

// Port of FilesExplorerView.swift: path navigation, per-row checkboxes,
// upload from the phone, share/download/delete of the selection, and
// opening a file in whatever app handles it - photos and videos in the
// Images section's viewer instead. Shown as a list or a grid of tiles
// (photo/video thumbnails from GetThumbnails, else FileTypeIcon).

private fun isDir(f: PbFile) = f.mime == "inode/directory"
private fun isImg(f: PbFile) = f.mime.startsWith("image/")
private fun isVideo(f: PbFile) = f.mime.startsWith("video/")
// The grid asks the device for a thumbnail only for these, and these open
// in the Images viewer.
private fun isMedia(row: FileRow) = !row.isDir &&
    (isImg(row.raw) || isVideo(row.raw) || row.name.endsWith(".heic", ignoreCase = true))
private fun joinPath(base: String, leaf: String) = base.trimEnd('/') + "/" + leaf.trimStart('/')
private fun dirnamePath(p: String): String {
    val clean = if (p.endsWith("/") && p != "/") p.dropLast(1) else p
    val idx = clean.lastIndexOf('/')
    return if (idx <= 0) "/" else clean.substring(0, idx)
}
private fun normPath(p: String): String {
    var s = p.trim()
    if (!s.startsWith("/")) s = "/$s"
    if (!s.endsWith("/")) s += "/"
    return s
}
private fun leafName(full: String) = full.split('/').lastOrNull { it.isNotEmpty() } ?: full

// uploadOnly/versions: issue #132 - inside (or itself) an upload-only
// folder, and how many older versions the device keeps for the file.
data class FileRow(val path: String, val name: String, val isDir: Boolean, val size: Int, val raw: PbFile,
                   val uploadOnly: Boolean = false, val versions: Int = 0)

class FilesExplorerViewModel(initialPath: String) : ViewModel() {
    data class State(
        val path: String,
        val rows: List<FileRow> = emptyList(),
        val loading: Boolean = false,
        val error: String? = null,
        val selected: Set<String> = emptySet(),
        val toast: String? = null,
        val openingPath: String? = null,
        val confirmDeleteSelected: Boolean = false,
        val preparing: SelectionActionTask? = null,
        // Issue #132: the versions pop-up - the file and its older versions.
        val versionsOf: Pair<FileRow, List<PbFile>>? = null,
        val versionsLoading: Boolean = false,
        // List or grid, kept across launches ("files_view_mode").
        val grid: Boolean = prefs.getString(VIEW_MODE_KEY, "list") == "grid",
        // Grid thumbnails for the session, by thumbKey.
        val thumbs: Map<String, ImageBitmap> = emptyMap(),
    )

    companion object {
        private const val VIEW_MODE_KEY = "files_view_mode"
        private const val THUMB_BATCH = 24
        private val prefs get() = OTCApp.instance.getSharedPreferences("otc_settings", Context.MODE_PRIVATE)
    }

    // Thumbnails asked for and not answered yet, or that the device said it
    // has none of - neither is asked again this session.
    private val thumbsPending = mutableSetOf<String>()
    private val noThumb = mutableSetOf<String>()

    private val _state = MutableStateFlow(State(path = initialPath))
    val state: StateFlow<State> = _state
    val path get() = _state.value.path

    suspend fun load() {
        _state.update { it.copy(loading = true, error = null) }
        try {
            val resp = OTCConnection.request { it.setReqListFiles(ListFiles.newBuilder().setPath(path)) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) {
                val files = resp.respListOfFiles.filesList.toMutableList()
                if (path != "/") files.add(0, PbFile.newBuilder().setMime("inode/directory").setPath("..").build())
                val rows = files.map { f ->
                    FileRow(f.path, if (f.path == "..") ".." else leafName(f.path), isDir(f), f.size, f, f.uploadOnly, f.versions)
                }
                _state.update { it.copy(rows = rows, selected = emptySet()) }
                if (_state.value.grid) launchThumbnails()
            } else if (resp.error) {
                _state.update { it.copy(error = resp.errorMessage.ifEmpty { "Failed to list path" }) }
            } else {
                _state.update { it.copy(error = "Unexpected response") }
            }
        } catch (e: Exception) {
            _state.update { it.copy(error = e.message ?: "Error") }
        } finally {
            _state.update { it.copy(loading = false) }
        }
    }

    fun navigate(newPath: String) {
        _state.update { it.copy(path = normPath(newPath)) }
        launchLoad()
    }

    fun launchLoad() = kotlinx.coroutines.GlobalScope.launch(Dispatchers.IO) { load() }

    fun fullPath(row: FileRow) = if (row.path.contains("/")) row.path else joinPath(path, row.path)

    fun thumbKey(row: FileRow) = fullPath(row) + "\u0000" + row.raw.hash

    fun setGrid(grid: Boolean) {
        prefs.edit().putString(VIEW_MODE_KEY, if (grid) "grid" else "list").apply()
        _state.update { it.copy(grid = grid) }
        if (grid) launchThumbnails()
    }

    private fun launchThumbnails() = kotlinx.coroutines.GlobalScope.launch(Dispatchers.IO) { loadThumbnails() }

    /**
     * The folder's photos and videos, in batches of 24 full paths. Paths the
     * answer leaves out have no thumbnail; anything that arrives after the
     * folder changed is dropped.
     */
    private suspend fun loadThumbnails() {
        val folder = path
        val have = _state.value.thumbs
        val wanted = synchronized(thumbsPending) {
            _state.value.rows.filter { isMedia(it) }.map { thumbKey(it) to fullPath(it) }
                .filter { (k, _) -> k !in have && k !in noThumb && k !in thumbsPending }
                .also { list -> thumbsPending.addAll(list.map { it.first }) }
        }
        for (batch in wanted.chunked(THUMB_BATCH)) {
            if (path != folder) break
            try {
                val resp = OTCConnection.request { it.setReqGetThumbnails(GetThumbnails.newBuilder().addAllPaths(batch.map { b -> b.second })) }
                if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) continue
                val byPath = resp.respListOfFiles.filesList.associateBy { it.path }
                val got = mutableMapOf<String, ImageBitmap>()
                for ((key, full) in batch) {
                    val bmp = byPath[full]?.let { decodeBitmap(it.content.toByteArray(), maxSide = 512) }
                    if (bmp != null) got[key] = bmp.asImageBitmap()
                    else synchronized(thumbsPending) { noThumb.add(key) }
                }
                if (path == folder) _state.update { it.copy(thumbs = it.thumbs + got) }
            } catch (_: Exception) {
            } finally {
                synchronized(thumbsPending) { thumbsPending.removeAll(batch.map { it.first }.toSet()) }
            }
        }
        synchronized(thumbsPending) { thumbsPending.removeAll(wanted.map { it.first }.toSet()) }
    }

    fun toggleSelect(p: String) = _state.update {
        it.copy(selected = if (p in it.selected) it.selected - p else it.selected + p)
    }

    fun setConfirmDelete(v: Boolean) = _state.update { it.copy(confirmDeleteSelected = v) }
    fun selectOnly(p: String) = _state.update { it.copy(selected = setOf(p)) }

    /** Issue #71: single-flight; the opened file is written to the cache and handed to a viewer. */
    suspend fun open(context: Context, row: FileRow) {
        if (row.isDir) {
            navigate(if (row.path == "..") dirnamePath(path) else normPath(joinPath(path, row.name)))
            return
        }
        if (_state.value.openingPath != null) return
        _state.update { it.copy(openingPath = row.path) }
        try {
            val resp = OTCConnection.request { it.setReqGetFile(GetFile.newBuilder().setPath(fullPath(row))) }
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_FILE) { showToast("Could not fetch file"); return }
            val f = resp.respFile
            val tmp = File(context.cacheDir, leafName(row.path))
            withContext(Dispatchers.IO) { tmp.writeBytes(f.content.toByteArray()) }
            Share.preview(context, tmp, f.mime.ifEmpty { null })
        } catch (e: Exception) {
            showToast("Download failed: ${e.message}")
        } finally {
            _state.update { it.copy(openingPath = null) }
        }
    }

    /** Issue #132: the selection touches an upload-only folder - the device refuses those deletes. */
    val selectionUploadOnly: Boolean get() = _state.value.rows.any { it.path in _state.value.selected && it.uploadOnly }

    suspend fun deleteSelected() {
        for (p in _state.value.selected) {
            try {
                val full = if (p.contains("/")) p else joinPath(path, p)
                val resp = OTCConnection.request { it.setReqDelFile(DelFile.newBuilder().setPath(full)) }
                if (resp.error && resp.errorCode == "upload_only") {
                    showToast("${leafName(full)} is in an upload-only folder and cannot be deleted")
                    break
                }
            } catch (_: Exception) {}
        }
        load()
    }

    /** Issue #132: the lock on a folder - flag it upload only, or clear it. */
    suspend fun toggleUploadOnly(row: FileRow) {
        try {
            val resp = OTCConnection.request { it.setReqSetUploadOnly(SetUploadOnly.newBuilder().setPath(fullPath(row)).setUploadOnly(!row.uploadOnly)) }
            if (resp.error) showToast(resp.errorMessage.ifEmpty { "Could not update the folder" })
        } catch (e: Exception) { showToast("Could not update the folder: ${e.message}") }
        load()
    }

    /** Issue #132: the versions badge - list the file's older versions. */
    suspend fun openVersions(row: FileRow) {
        _state.update { it.copy(versionsOf = row to emptyList(), versionsLoading = true) }
        try {
            val resp = OTCConnection.request { it.setReqListFileVersions(ListFileVersions.newBuilder().setPath(fullPath(row))) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_FILE_VERSIONS) {
                _state.update { it.copy(versionsOf = row to resp.respFileVersions.versionsList) }
            }
        } catch (_: Exception) {
        } finally { _state.update { it.copy(versionsLoading = false) } }
    }

    fun closeVersions() = _state.update { it.copy(versionsOf = null) }

    /** A version opens the way a file does; an empty hash is the current one. */
    suspend fun openVersion(context: Context, row: FileRow, hash: String) {
        try {
            val resp = OTCConnection.request { it.setReqGetFile(GetFile.newBuilder().setPath(fullPath(row)).setHash(hash)) }
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_FILE) { showToast("Could not fetch that version"); return }
            val f = resp.respFile
            val tmp = File(context.cacheDir, leafName(row.path))
            withContext(Dispatchers.IO) { tmp.writeBytes(f.content.toByteArray()) }
            closeVersions()
            Share.preview(context, tmp, f.mime.ifEmpty { null })
        } catch (e: Exception) {
            showToast("Download failed: ${e.message}")
        }
    }

    suspend fun shareLink(): String? {
        val sel = _state.value.selected
        if (sel.isEmpty()) return null
        return try {
            val resp = OTCConnection.request {
                it.setReqShareFilesLink(ShareFilesLink.newBuilder().addAllPaths(sel.map { p -> if (p.contains("/")) p else joinPath(path, p) }))
            }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_SHARE_LINK) resp.respShareLink.link else null
        } catch (e: Exception) { null }
    }

    suspend fun downloadSelected(context: Context) {
        _state.update { it.copy(preparing = SelectionActionTask.DOWNLOAD) }
        try {
            val link = shareLink() ?: run { showToast("Could not create download link"); return }
            Share.openInBrowser(context, link)
        } finally { _state.update { it.copy(preparing = null) } }
    }

    suspend fun shareSelected(context: Context) {
        _state.update { it.copy(preparing = SelectionActionTask.SHARE) }
        try {
            val link = shareLink() ?: run { showToast("Could not create share link"); return }
            Share.link(context, link)
        } finally { _state.update { it.copy(preparing = null) } }
    }

    /**
     * Issue #58: hash first; content the device already has is linked, not re-sent.
     * Issue #165: [open] is read twice as a stream (hash, then chunked upload),
     * never loaded whole.
     */
    suspend fun upload(open: () -> InputStream, filename: String) {
        val target = joinPath(path, filename)
        try {
            val digest = ChunkedUpload.digest(open)
            val hash = digest.sha256
            val has = OTCConnection.request { it.setReqHasFile(HasFile.newBuilder().setHash(hash)) }
            val exists = has.payloadCase == RespEnvelope.PayloadCase.RESP_FILE_EXISTS && has.respFileExists.exists
            val resp = if (exists) {
                OTCConnection.request { it.setReqLinkFile(LinkFile.newBuilder().setHash(hash).setPath(target).setForceOverride(false)) }
            } else {
                ChunkedUpload.upload(target, digest.size, open, forceOverride = false, sha256 = hash)
            }
            if (resp.error) showToast("Upload failed: ${resp.errorMessage}")
        } catch (e: Exception) {
            showToast("Upload failed: ${e.message}")
        }
        load()
    }

    fun showToast(m: String) {
        _state.update { it.copy(toast = m) }
        kotlinx.coroutines.GlobalScope.launch {
            delay(2500)
            _state.update { if (it.toast == m) it.copy(toast = null) else it }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FilesExplorerView(initialPath: String) {
    val vm: FilesExplorerViewModel = viewModel(key = "files") { FilesExplorerViewModel(initialPath) }
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var pathField by remember { mutableStateOf(initialPath) }
    var refreshing by remember { mutableStateOf(false) }
    // Issue #180: "Share as gallery" on a folder - what is being shared.
    var gallerySource by remember { mutableStateOf<SharedGallerySource?>(null) }
    val selectedFolder = st.selected.singleOrNull()?.let { p -> st.rows.firstOrNull { it.path == p && it.isDir && it.path != ".." } }

    // Photos and videos open in the Images section's viewer, its own
    // instance, paging through this folder's photos and videos only.
    val viewer: PhotoGalleryViewModel = viewModel(key = "files-viewer") { PhotoGalleryViewModel("") }
    val vst by viewer.state.collectAsState()

    LaunchedEffect(Unit) { vm.load() }
    LaunchedEffect(st.path) { pathField = st.path }

    fun openRow(row: FileRow) {
        if (!isMedia(row)) { if (st.openingPath == null) scope.launch { vm.open(context, row) }; return }
        val media = st.rows.filter { isMedia(it) }
        // The grid's thumbnail is the placeholder until the full size arrives.
        val items = media.map { r ->
            val full = vm.fullPath(r)
            PhotoGalleryViewModel.Item("$full#${r.raw.hash}#${r.size}", full, r.raw.mime, r.size, null, st.thumbs[vm.thumbKey(r)]?.asAndroidBitmap())
        }
        viewer.showFiles(items, media.indexOf(row)) { vm.launchLoad() }
    }

    val importer = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris: List<Uri> ->
        for (uri in uris) {
            scope.launch(Dispatchers.IO) {
                val name = queryDisplayName(context, uri) ?: "file"
                vm.upload({ context.contentResolver.openInputStream(uri) ?: throw java.io.IOException("cannot read $uri") }, name)
            }
        }
    }

    Box(Modifier.fillMaxSize()) {
        Column(Modifier.fillMaxSize()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                OTCTextField(
                    value = pathField, onValueChange = { pathField = it }, singleLine = true, label = { Text("/path/") },
                    keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, imeAction = ImeAction.Go),
                    keyboardActions = KeyboardActions(onGo = { vm.navigate(pathField) }),
                    modifier = Modifier.weight(1f),
                )
                if (st.loading) { Spacer(Modifier.width(8.dp)); CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp) }
                // List <-> grid; the icon is the mode a tap switches to.
                IconButton(onClick = { vm.setGrid(!st.grid) }) {
                    Icon(if (st.grid) Icons.AutoMirrored.Filled.ViewList else Icons.Default.GridView, if (st.grid) "Show as list" else "Show as grid")
                }
            }
            st.error?.let { Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(horizontal = 12.dp)) }

            PullToRefreshBox(
                isRefreshing = refreshing,
                onRefresh = { scope.launch { refreshing = true; vm.load(); refreshing = false } },
                modifier = Modifier.weight(1f),
            ) {
                if (st.grid) LazyVerticalGrid(
                    GridCells.Adaptive(104.dp), Modifier.fillMaxSize(),
                    contentPadding = PaddingValues(horizontal = 8.dp, vertical = 6.dp),
                    horizontalArrangement = Arrangement.spacedBy(10.dp),
                    verticalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    items(st.rows, key = { it.path }) { row ->
                        FileGridCell(
                            row, selected = row.path in st.selected, opening = st.openingPath == row.path,
                            thumb = if (isMedia(row)) st.thumbs[vm.thumbKey(row)] else null,
                            onOpen = { openRow(row) },
                            onToggle = { vm.toggleSelect(row.path) },
                            onVersions = { scope.launch { vm.openVersions(row) } },
                        )
                    }
                } else LazyColumn(Modifier.fillMaxSize()) {
                    items(st.rows, key = { it.path }) { row ->
                        val selected = row.path in st.selected
                        Row(
                            Modifier.fillMaxWidth()
                                .clickable(enabled = st.openingPath == null) { openRow(row) }
                                .padding(horizontal = 8.dp, vertical = 6.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            if (row.path != "..") {
                                IconButton(onClick = { vm.toggleSelect(row.path) }, modifier = Modifier.size(36.dp)) {
                                    Icon(if (selected) Icons.Default.CheckCircle else Icons.Outlined.Circle, if (selected) "Deselect" else "Select",
                                        tint = if (selected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant)
                                }
                            } else Spacer(Modifier.size(36.dp))
                            Box(Modifier.size(28.dp), contentAlignment = Alignment.Center) {
                                if (st.openingPath == row.path) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                                else Icon(
                                    if (row.isDir) Icons.Default.Folder else if (isImg(row.raw)) Icons.Default.Image else Icons.Default.Description,
                                    null, tint = if (row.isDir) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
                                )
                            }
                            Spacer(Modifier.width(8.dp))
                            Column(Modifier.weight(1f)) {
                                Text(row.name, maxLines = 1)
                                if (!row.isDir) Text(formatBytes(row.size.toLong()), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                            // Issue #132: the versions badge opens the pop-up;
                            // the lock on a folder toggles upload only, on a
                            // file it just says it is inside one.
                            if (!row.isDir && row.versions > 0) {
                                TextButton(onClick = { scope.launch { vm.openVersions(row) } }, contentPadding = PaddingValues(horizontal = 8.dp, vertical = 0.dp)) {
                                    Icon(Icons.Default.History, null, Modifier.size(16.dp))
                                    Spacer(Modifier.width(4.dp))
                                    Text("${row.versions}", style = MaterialTheme.typography.labelMedium)
                                }
                            }
                            if (row.path != ".." && row.isDir) {
                                IconButton(onClick = { scope.launch { vm.toggleUploadOnly(row) } }, modifier = Modifier.size(36.dp)) {
                                    Icon(if (row.uploadOnly) Icons.Default.Lock else Icons.Outlined.LockOpen,
                                        if (row.uploadOnly) "Clear upload only" else "Make upload only",
                                        tint = if (row.uploadOnly) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant)
                                }
                            } else if (row.uploadOnly) {
                                Box(Modifier.size(36.dp), contentAlignment = Alignment.Center) {
                                    Icon(Icons.Default.Lock, "In an upload-only folder", tint = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.size(18.dp))
                                }
                            }
                        }
                    }
                }
            }

            SelectionActionBar(
                count = st.selected.size,
                busy = st.preparing,
                onShare = { scope.launch { vm.shareSelected(context) } },
                onDownload = { scope.launch { vm.downloadSelected(context) } },
                onDelete = {
                    if (vm.selectionUploadOnly) vm.showToast("The selection is in an upload-only folder and cannot be deleted")
                    else vm.setConfirmDelete(true)
                },
                onUpload = { importer.launch(arrayOf("*/*")) },
                onGallery = selectedFolder?.let { row ->
                    { gallerySource = SharedGallerySource.newBuilder().setDirectory(vm.fullPath(row)).build() }
                },
            )
            Spacer(Modifier.size(8.dp))
        }
        Toast(st.toast, Modifier.align(Alignment.TopCenter))
    }

    // Issue #132: the versions pop-up - the current file and every older
    // version with when it was replaced and its size; a tap opens that
    // version the way a file opens.
    st.versionsOf?.let { (row, versions) ->
        AlertDialog(
            onDismissRequest = { vm.closeVersions() },
            title = { Text("Versions of ${row.name}") },
            text = {
                Column {
                    Text("The file is in an upload-only folder, so each upload to this path kept the one before it.",
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Spacer(Modifier.size(8.dp))
                    Row(Modifier.fillMaxWidth().clickable { scope.launch { vm.openVersion(context, row, "") } }.padding(vertical = 8.dp)) {
                        Text("Current", fontWeight = FontWeight.SemiBold, modifier = Modifier.weight(1f))
                        Text(formatBytes(row.size.toLong()), color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    if (st.versionsLoading) CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                    versions.forEach { v ->
                        Row(Modifier.fillMaxWidth().clickable { scope.launch { vm.openVersion(context, row, v.hash) } }.padding(vertical = 8.dp)) {
                            Text("Replaced " + (if (v.hasModified()) DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(v.modified.seconds * 1000)) else "—"),
                                modifier = Modifier.weight(1f))
                            Text(formatBytes(v.size.toLong()), color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                }
            },
            confirmButton = { TextButton(onClick = { vm.closeVersions() }) { Text("Done") } },
        )
    }
    gallerySource?.let { src -> SharedGalleryShareFlow(src, onDismiss = { gallerySource = null }) }
    if (st.confirmDeleteSelected) {
        val n = st.selected.size
        AlertDialog(
            onDismissRequest = { vm.setConfirmDelete(false) },
            title = { Text("Delete $n item${if (n == 1) "" else "s"}?") },
            confirmButton = { TextButton(onClick = { vm.setConfirmDelete(false); scope.launch { vm.deleteSelected() } }) { Text("Delete", color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { vm.setConfirmDelete(false) }) { Text("Cancel") } },
        )
    }
    if (vst.openIndex != null) ImageModal(viewer, vst)
    vst.alert?.let {
        AlertDialog(onDismissRequest = { viewer.dismissAlert() }, text = { Text(it) }, confirmButton = { TextButton(onClick = { viewer.dismissAlert() }) { Text("OK") } })
    }
}

/**
 * One grid tile: the folder, thumbnail or file-type icon, with the list's
 * selection circle (top left), lock (folders, top right), versions count
 * (bottom right) and a play badge on videos (bottom left); the name below.
 */
@Composable
private fun FileGridCell(
    row: FileRow, selected: Boolean, opening: Boolean, thumb: ImageBitmap?,
    onOpen: () -> Unit, onToggle: () -> Unit, onVersions: () -> Unit,
) {
    Column(Modifier.fillMaxWidth()) {
        Box(
            Modifier.fillMaxWidth().aspectRatio(1f).clip(RoundedCornerShape(10.dp))
                .background(MaterialTheme.colorScheme.surfaceContainerLow).clickable(onClick = onOpen),
            contentAlignment = Alignment.Center,
        ) {
            when {
                row.isDir -> Icon(Icons.Default.Folder, null, Modifier.fillMaxSize(0.62f), tint = MaterialTheme.colorScheme.primary)
                thumb != null -> Image(thumb, null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
                else -> FileTypeIcon(row.name, Modifier.fillMaxSize().padding(14.dp))
            }
            if (!row.isDir && isVideo(row.raw) && thumb != null) {
                Box(Modifier.align(Alignment.BottomStart).padding(6.dp).size(24.dp).background(Color.Black.copy(alpha = 0.55f), CircleShape),
                    contentAlignment = Alignment.Center) {
                    Icon(Icons.Default.PlayArrow, "Video", tint = Color.White, modifier = Modifier.size(16.dp))
                }
            }
            if (!row.isDir && row.versions > 0) {
                Row(
                    Modifier.align(Alignment.BottomEnd).padding(4.dp).clip(RoundedCornerShape(50))
                        .background(MaterialTheme.colorScheme.surface.copy(alpha = 0.85f)).clickable(onClick = onVersions)
                        .padding(horizontal = 6.dp, vertical = 2.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Icon(Icons.Default.History, null, Modifier.size(12.dp), tint = MaterialTheme.colorScheme.primary)
                    Spacer(Modifier.width(2.dp))
                    Text("${row.versions}", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.primary)
                }
            }
            if (row.isDir && row.uploadOnly) {
                Box(Modifier.align(Alignment.TopEnd).padding(6.dp).size(24.dp).background(MaterialTheme.colorScheme.surface.copy(alpha = 0.85f), CircleShape),
                    contentAlignment = Alignment.Center) {
                    Icon(Icons.Default.Lock, "Upload only", tint = MaterialTheme.colorScheme.primary, modifier = Modifier.size(14.dp))
                }
            }
            if (row.path != "..") {
                Box(Modifier.align(Alignment.TopStart).padding(4.dp).size(28.dp).clip(CircleShape)
                    .background(MaterialTheme.colorScheme.surface.copy(alpha = 0.7f)).clickable(onClick = onToggle),
                    contentAlignment = Alignment.Center) {
                    Icon(if (selected) Icons.Default.CheckCircle else Icons.Outlined.Circle, if (selected) "Deselect" else "Select",
                        tint = if (selected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            if (opening) CircularProgressIndicator(Modifier.size(28.dp), strokeWidth = 2.dp)
        }
        Spacer(Modifier.size(4.dp))
        Text(row.name, style = MaterialTheme.typography.bodySmall, maxLines = 2, overflow = TextOverflow.Ellipsis,
            textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth())
    }
}

private fun queryDisplayName(context: Context, uri: Uri): String? =
    context.contentResolver.query(uri, null, null, null, null)?.use { c ->
        val idx = c.getColumnIndex(android.provider.OpenableColumns.DISPLAY_NAME)
        if (idx >= 0 && c.moveToFirst()) c.getString(idx) else null
    }
