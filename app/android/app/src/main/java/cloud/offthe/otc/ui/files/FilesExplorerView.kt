// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import android.content.Context
import android.net.Uri
import android.util.LruCache
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
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
import androidx.compose.runtime.DisposableEffect
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
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.data.ImagesChanged
import cloud.offthe.otc.net.ChunkedDownload
import cloud.offthe.otc.net.ChunkedUpload
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.byteSize
import cloud.offthe.otc.proto.DelFile
import cloud.offthe.otc.proto.File as PbFile
import cloud.offthe.otc.proto.GetFile
import cloud.offthe.otc.proto.GetThumbnails
import cloud.offthe.otc.proto.HasFile
import cloud.offthe.otc.proto.LinkFile
import cloud.offthe.otc.proto.ListFiles
import cloud.offthe.otc.proto.ListOfFiles
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
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
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
import cloud.offthe.otc.proto.SetOutOfImages
import cloud.offthe.otc.proto.ListOutOfImages
import kotlinx.coroutines.CancellationException
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.material.icons.outlined.HideImage
import cloud.offthe.otc.proto.SearchFiles
import cloud.offthe.otc.ui.gallery.isUnknownPayload
import java.text.DateFormat
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
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
import androidx.activity.compose.BackHandler
import androidx.activity.compose.LocalActivity
import androidx.compose.foundation.lazy.grid.rememberLazyGridState
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalWindowInfo
import cloud.offthe.otc.ui.MenuLayout
import cloud.offthe.otc.ui.menuLayout
import cloud.offthe.otc.ui.gallery.FilesSearchField
import cloud.offthe.otc.ui.gallery.FilesSearchPanel
import cloud.offthe.otc.ui.gallery.TopSearchViewModel
import cloud.offthe.otc.ui.gallery.rememberFileSearchOptions

// Port of FilesExplorerView.swift: path navigation, per-row checkboxes,
// upload from the phone, share/download/delete of the selection, and
// opening a file in whatever app handles it - photos and videos in the
// Images section's viewer instead. Shown as a list or a grid of tiles
// (photo/video thumbnails from GetThumbnails, else FileTypeIcon). In the
// narrow layout a search field for files and folders sits at the top
// (TopSearch's FilesSearchField; wide, the top bar's search covers files).

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
// A file as SearchFiles or the Images search found it (its path is whole).
private fun isMediaFile(f: PbFile) = !isDir(f) && (isImg(f) || isVideo(f) || f.path.endsWith(".heic", ignoreCase = true))
/** A folder's listing as rows: ".." first below the root, each with its marks (issues #132, #192). */
internal fun listingRows(lof: ListOfFiles, path: String): List<FileRow> {
    val files = lof.filesList.toMutableList()
    if (path != "/") files.add(0, PbFile.newBuilder().setMime("inode/directory").setPath("..").build())
    return files.map { f ->
        FileRow(f.path, if (f.path == "..") ".." else leafName(f.path), isDir(f), f.byteSize, f, f.uploadOnly, f.versions, f.outOfImages)
    }
}

/** Issue #192: the request that keeps [full] out of Images, or shows it there again ([out]: kept out now). */
internal fun setOutOfImagesRequest(full: String, out: Boolean): SetOutOfImages =
    SetOutOfImages.newBuilder().setPath(full).setOutOfImages(!out).build()

/**
 * Issue #192: what SetOutOfImages' answer leaves to say - null when it was
 * done. A refusal (a folder inside another one kept out: error_code
 * "out_of_images_by_parent") is the device's own sentence, shown as it is.
 */
internal fun outOfImagesFailure(resp: RespEnvelope): String? = when {
    resp.error -> resp.errorMessage.ifEmpty { "Could not update the folder" }
    resp.payloadCase != RespEnvelope.PayloadCase.RESP_ACK || !resp.respAck.ok -> resp.respAck.errorMsg.ifEmpty { "Could not update the folder" }
    else -> null
}

/**
 * Issue #192: of the folders kept out of Images (ListOutOfImages, each with
 * its trailing slash), the outermost one at or above [folder] - the one
 * whose "Show in Images" the device takes, as no folder above it keeps it
 * out - without its slash; null when none is.
 */
internal fun outermostKeptOut(kept: List<String>, folder: String): String? {
    val key = if (folder.endsWith("/")) folder else "$folder/"
    return kept.filter { it.endsWith("/") && it.length > 1 && key.startsWith(it) }.minByOrNull { it.length }?.trimEnd('/')
}

/** Issue #192: what to wait for before keeping a folder out of Images, or showing it, while [task] is under way. */
internal fun outOfImagesWait(task: SelectionActionTask): String = when (task) {
    SelectionActionTask.SHARE -> "Wait for the share link to be ready"
    SelectionActionTask.DOWNLOAD -> "Wait for the download link to be ready"
    SelectionActionTask.OUT_OF_IMAGES -> "Wait for the folder to be updated"
}

private fun foundRow(f: PbFile) = FileRow(f.path, leafName(f.path), isDir(f), f.byteSize, f, f.uploadOnly, f.versions, f.outOfImages)

/**
 * Files' own search field (TopSearch's FilesSearchField) shows in the
 * narrow layout only - MainView's bottom bar, under 600dp across (wide,
 * the top bar's search covers files) - and not on a device that can't
 * search its files.
 */
internal fun filesSearchFieldShown(windowWidthDp: Float, noFileSearch: Boolean) =
    menuLayout(windowWidthDp, menuOpen = true) == MenuLayout.NONE && !noFileSearch

/** Which way a change of layout hands the search in use: Files' own field and the wide top bar's take each other's place. */
internal enum class SearchHandOver { NONE, TO_TOP_BAR, TO_FILES }

/**
 * What a change of layout does with the search in use ([narrow]: the
 * layout now is the narrow one, where Files has its own field). Going wide, Files' field
 * goes and what is typed in it, while in use, moves to the top bar's; going
 * narrow, the top bar's goes and what it holds, while in use in Files,
 * moves to Files' field. A field not in use, or with nothing typed, hands
 * nothing over: each keeps its own text.
 */
internal fun searchHandOver(narrow: Boolean, filesOpen: Boolean, filesText: String, topOpen: Boolean, topText: String): SearchHandOver = when {
    !narrow && filesOpen && filesText.isNotBlank() -> SearchHandOver.TO_TOP_BAR
    narrow && topOpen && topText.isNotBlank() -> SearchHandOver.TO_FILES
    else -> SearchHandOver.NONE
}

/**
 * The hand-over between Files' own field and the wide top bar's
 * (searchHandOver), kept in Files' store. The top bar's search is the
 * Images field's too: given Files' search (going wide while typing in
 * Files' field), it sets its own text aside ([topBarOwn]) and gets it back
 * when Files' search leaves it, so neither field ends up with the other's
 * text:
 * - back to the narrow layout: Files' field takes what the top bar holds
 *   by then (typed on, or not; in use, with the focus), and the top bar its
 *   own text - also when the top bar was emptied in between (a file or
 *   folder picked, the x), which leaves Files' field empty;
 * - out of Files: Files keeps what the top bar holds (not in use) for its
 *   field. An emptied top bar there sets nothing back: it went to Images
 *   with a tag or a person picked from it, which ends Images' typed text
 *   as a pick in the Images field does.
 */
internal class FilesSearchHandOver : ViewModel() {
    var topBarOwn: String? = null
        private set

    /** The layout is now the narrow one ([narrow]) or the wide one: the search in use goes with it. */
    fun layoutChanged(narrow: Boolean, noFileSearch: Boolean, files: TopSearchViewModel, top: TopSearchViewModel) {
        if (noFileSearch) {
            // A device that can't search its files has no field: what was
            // in it goes too, and the top bar, the one search left, keeps
            // what it holds.
            files.handOff()
            topBarOwn = null
            return
        }
        if (narrow && topBarOwn != null) { giveBack(files, top, inUse = top.open, evenIfEmptied = true); return }
        when (searchHandOver(narrow, files.open, files.query, top.open, top.query)) {
            SearchHandOver.TO_TOP_BAR -> {
                if (topBarOwn == null) topBarOwn = top.query
                top.takeOver(files.handOff())
            }
            SearchHandOver.TO_FILES -> files.takeOver(top.handOff())
            SearchHandOver.NONE -> {}
        }
        // Gone with nothing handed over: no longer in use (it would take
        // the focus back when it shows again); what is typed stays.
        if (!narrow) files.close()
    }

    /** Files left (not only recreated): the top bar holding its search gives it back. */
    fun filesLeft(files: TopSearchViewModel, top: TopSearchViewModel) {
        giveBack(files, top, inUse = false, evenIfEmptied = false)
        files.close()
    }

    // [evenIfEmptied]: the top bar's own text comes back even when the top
    // bar holds nothing of Files' search any more.
    private fun giveBack(files: TopSearchViewModel, top: TopSearchViewModel, inUse: Boolean, evenIfEmptied: Boolean) {
        val own = topBarOwn ?: return
        topBarOwn = null
        val text = top.query
        if (text.isBlank() && !evenIfEmptied) return
        // In use, Files' field takes the focus as well; else only the text.
        if (text.isNotBlank()) {
            if (inUse) files.takeOver(text) else { files.type(text); files.close() }
        }
        top.type(own)
        top.close()
    }
}

/**
 * The Images search's "Search documents" (FilesNav.searchInFiles): every
 * file and folder whose path holds [text], as SearchFiles finds them - at
 * most SEARCH_LIMIT, the most it answers - listed over the folder, which
 * stays as it was for the way back. An error says what went wrong, and
 * whether trying again may help.
 */
data class SearchResults(val text: String, val phase: Phase, val files: List<PbFile> = emptyList(), val error: String? = null, val retry: Boolean = true) {
    enum class Phase { LOADING, DONE, ERROR }
}
private const val SEARCH_LIMIT = 50

// uploadOnly/versions: issue #132 - inside (or itself) an upload-only
// folder, and how many older versions the device keeps for the file.
// outOfImages: issue #192 - inside (or itself) a folder kept out of Images.
data class FileRow(val path: String, val name: String, val isDir: Boolean, val size: Long, val raw: PbFile,
                   val uploadOnly: Boolean = false, val versions: Int = 0, val outOfImages: Boolean = false)

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
        // Grid thumbnails by thumbKey: what the bounded cache below holds.
        val thumbs: Map<String, ImageBitmap> = emptyMap(),
        // Bumped by load(): tiles ask again for thumbnails that failed, as a
        // reload or pull-to-refresh always did.
        val thumbGen: Int = 0,
        // The folder the rows are the listing of (null until one arrives).
        val listedPath: String? = null,
        // The search's results, shown over the folder; null: the folder.
        val results: SearchResults? = null,
        // Issue #192, from the last listing (of listedPath): the device can
        // keep folders out of Images (devices before release 108 can't: no
        // badge, action or banner then), and that folder is kept out itself
        // or is inside one (the banner).
        val outOfImagesSupported: Boolean = false,
        val folderOutOfImages: Boolean = false,
        // Issue #192, in a folder kept out (folderOutOfImages): the folder
        // whose "Show in Images" the device takes - the outermost one kept
        // out at or above it (outermostKeptOut), no trailing slash. Null
        // while ListOutOfImages is asked (the banner has no button yet); the
        // folder listed itself when that failed (a refusal then says which).
        val outOfImagesCover: String? = null,
        // Issue #192: said to TalkBack once a change is done ("Trips kept
        // out of Images"); the screen shows it by the mark and the banner.
        val outOfImagesSaid: String? = null,
    ) {
        /** The selection when it is one folder: what "Share as gallery" (issue #180) acts on. */
        val selectedFolder: FileRow? get() = selected.singleOrNull()?.let { p -> rows.firstOrNull { it.path == p && it.isDir && it.path != ".." } }
        /**
         * Issue #192: the folder the selection's switch for Images acts on -
         * one folder picked, on a device that can. Not inside a folder kept
         * out: a folder there is kept out by it (or one above it), and the
         * device refuses its Show; the banner shows that folder instead.
         */
        val outOfImagesSwitch: FileRow? get() = selectedFolder?.takeIf { outOfImagesSupported && !folderOutOfImages }
        /** Issue #192: the banner over a folder kept out of Images - the folder listed, on a device that can. */
        val outOfImagesBanner: Boolean get() = outOfImagesSupported && folderOutOfImages && listedPath == path
        /** Issue #192: the folder listed, as a path without its trailing slash ("/" stays "/"). */
        val listedFolder: String get() = (listedPath ?: path).let { if (it.length > 1) it.trimEnd('/').ifEmpty { "/" } else it }
        /** Issue #192: the folder keeping the rows of a folder kept out out of Images, as far as known: the one whose Show the device takes, else the folder listed. */
        val keptOutBy: String get() = outOfImagesCover ?: listedFolder
        /** Issue #192: the banner names a folder above the one listed - the one its Show acts on. */
        val bannerByParent: Boolean get() = outOfImagesCover != null && outOfImagesCover != listedFolder
    }

    companion object {
        private const val VIEW_MODE_KEY = "files_view_mode"
        private const val THUMB_BATCH = 24
        private val prefs get() = OTCApp.instance.getSharedPreferences("otc_settings", Context.MODE_PRIVATE)
    }

    // Picked files upload two at a time: each one hashes and holds a chunk
    // while it waits for ChunkedUpload's send permits.
    private val uploadSlots = Semaphore(2)

    // Thumbnails decoded at 512 px (up to ~1 MB each), bounded by bytes to
    // several screens of tiles; it used to keep every one of a folder for the
    // session. Not cleared on a folder change: keys hold the full path and
    // hash, so old folders age out, and going back still shows them at once.
    private val thumbCache = object : LruCache<String, ImageBitmap>(96 shl 20) {
        override fun sizeOf(key: String, value: ImageBitmap) = value.asAndroidBitmap().byteCount
    }
    // Under thumbLock: tiles waiting for a request (key, full path, folder),
    // the ones asked for and not answered yet, and the ones the device said
    // it has none of - neither of the last two is asked again.
    private val thumbLock = Any()
    private val thumbQueue = ArrayDeque<Triple<String, String, String>>()
    private val thumbsPending = mutableSetOf<String>()
    private val noThumb = mutableSetOf<String>()
    private var pumping = false

    private val _state = MutableStateFlow(State(path = initialPath))
    val state: StateFlow<State> = _state
    val path get() = _state.value.path

    // Bumped by every listing that lands: ListOutOfImages' answer is for
    // the listing that asked, not one that replaced it meanwhile.
    private val listings = java.util.concurrent.atomic.AtomicInteger()

    suspend fun load() {
        // The folder asked for: a listing that comes back after another
        // folder was opened (the search opening one as Files appears) is
        // not that folder's, and is dropped.
        val p = path
        _state.update { it.copy(loading = true, error = null) }
        try {
            val resp = OTCConnection.request { it.setReqListFiles(ListFiles.newBuilder().setPath(p)) }
            if (path != p) return
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) {
                val lof = resp.respListOfFiles
                val rows = listingRows(lof, p)
                val folderOut = lof.outOfImagesSupported && lof.folderOutOfImages
                val listing = listings.incrementAndGet()
                _state.update {
                    it.copy(rows = rows, selected = emptySet(), thumbGen = it.thumbGen + 1, listedPath = p,
                        outOfImagesSupported = lof.outOfImagesSupported, folderOutOfImages = lof.folderOutOfImages, outOfImagesCover = null)
                }
                // Issue #192: in a folder kept out, which folder keeps it
                // out - asked after the listing shows, so a slow answer
                // never holds it up.
                if (folderOut) viewModelScope.launch { askCover(p, listing) }
            } else if (resp.error) {
                if (mediaToOpen?.first == p) mediaToOpen = null
                _state.update { it.copy(error = resp.errorMessage.ifEmpty { "Failed to list path" }) }
            } else {
                _state.update { it.copy(error = "Unexpected response") }
            }
        } catch (e: Exception) {
            if (path == p) _state.update { it.copy(error = e.message ?: "Error") }
        } finally {
            if (path == p) _state.update { it.copy(loading = false) }
        }
    }

    /**
     * Issue #192: the folder whose "Show in Images" the device takes for
     * the folder [p] kept out (State.outOfImagesCover): it is the outermost
     * one kept out at or above it, as the web's Files has it. When that
     * can't be told, [p] itself - a refusal then names the folder above.
     */
    private suspend fun askCover(p: String, listing: Int) {
        val cover = try {
            val resp = OTCConnection.request { it.setReqListOutOfImages(ListOutOfImages.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_OUT_OF_IMAGES_FOLDERS) outermostKeptOut(resp.respOutOfImagesFolders.pathsList, p) else null
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) { null }
        _state.update {
            if (listings.get() != listing || it.listedPath != p) it
            else it.copy(outOfImagesCover = cover ?: it.listedFolder)
        }
    }

    // A photo or video the search picked (its folder, and the file): opened
    // in the viewer once that folder's listing is in, to page through it.
    var mediaToOpen: Pair<String, PbFile>? = null

    private var searchSeq = 0

    /** The search's "Search documents": the results, shown over the folder. Only the last search asked for lands. */
    fun startSearch(text: String) {
        val mine = ++searchSeq
        _state.update { it.copy(results = SearchResults(text, SearchResults.Phase.LOADING)) }
        viewModelScope.launch {
            fun fail(error: String, retry: Boolean = true) {
                if (mine == searchSeq) _state.update { it.copy(results = SearchResults(text, SearchResults.Phase.ERROR, error = error, retry = retry)) }
            }
            val resp = try {
                OTCConnection.request { it.setReqSearchFiles(SearchFiles.newBuilder().setQuery(text).setLimit(SEARCH_LIMIT)) }
            } catch (e: Exception) {
                fail("The search didn't reach the device. Check the connection and try again.")
                return@launch
            }
            // The Images search stops offering it on such a device.
            if (resp.isUnknownPayload()) FilesNav.deviceCantSearchFiles()
            if (mine != searchSeq) return@launch
            when {
                resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES -> {
                    val files = resp.respListOfFiles.filesList
                    _state.update { it.copy(results = SearchResults(text, SearchResults.Phase.DONE, files)) }
                    resultThumbnails(files)
                }
                resp.isUnknownPayload() -> fail("This device can't search its files yet. Update it in Settings.", retry = false)
                else -> fail(resp.errorMessage.ifEmpty { "The device didn't answer the search." })
            }
        }
    }

    /** Back to the folder. */
    fun closeResults() {
        if (_state.value.results == null) return
        searchSeq++
        _state.update { it.copy(results = null) }
    }

    /** The results' photos and videos, by their thumbnails: asked for together, 24 paths at a time. */
    private suspend fun resultThumbnails(files: List<PbFile>) {
        val want = files.filter { isMediaFile(it) && thumbCache.get(it.path + "\u0000" + it.hash) == null }
        for (batch in want.chunked(THUMB_BATCH)) {
            try {
                val resp = OTCConnection.request { it.setReqGetThumbnails(GetThumbnails.newBuilder().addAllPaths(batch.map { f -> f.path })) }
                if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) continue
                val byPath = resp.respListOfFiles.filesList.associateBy { it.path }
                withContext(Dispatchers.Default) {
                    for (f in batch) byPath[f.path]?.let { decodeBitmap(it.content.toByteArray(), maxSide = 512) }?.let { thumbCache.put(f.path + "\u0000" + f.hash, it.asImageBitmap()) }
                }
                _state.update { it.copy(thumbs = thumbCache.snapshot()) }
            } catch (_: Exception) {}
        }
    }

    /** A result's thumbnail, if it has one by now. */
    fun resultThumbKey(f: PbFile) = f.path + "\u0000" + f.hash

    fun navigate(newPath: String) {
        val p = normPath(newPath)
        // A photo the search picked waits for its own folder only.
        if (mediaToOpen?.first != p) mediaToOpen = null
        _state.update { it.copy(path = p) }
        launchLoad()
    }

    fun launchLoad() = kotlinx.coroutines.GlobalScope.launch(Dispatchers.IO) { load() }

    fun fullPath(row: FileRow) = if (row.path.contains("/")) row.path else joinPath(path, row.path)

    fun thumbKey(row: FileRow) = fullPath(row) + "\u0000" + row.raw.hash

    fun setGrid(grid: Boolean) {
        prefs.edit().putString(VIEW_MODE_KEY, if (grid) "grid" else "list").apply()
        _state.update { it.copy(grid = grid) }
    }

    /**
     * A grid tile appeared (on screen or in the grid's prefetch, as the
     * iOS grid asks): its thumbnail is queued unless it is cached - which
     * also keeps it among the newest - asked for already, or known missing.
     */
    fun wantThumbnail(row: FileRow) {
        if (!isMedia(row)) return
        val key = thumbKey(row)
        if (thumbCache.get(key) != null) {
            // Fetched while another folder was shown: not published then.
            if (_state.value.thumbs[key] == null) _state.update { it.copy(thumbs = thumbCache.snapshot()) }
            return
        }
        synchronized(thumbLock) {
            if (key in noThumb || key in thumbsPending) return
            thumbsPending += key
            thumbQueue.addLast(Triple(key, fullPath(row), path))
            if (pumping) return
            pumping = true
        }
        kotlinx.coroutines.GlobalScope.launch(Dispatchers.IO) { pumpThumbnails() }
    }

    /** The tile went away before its turn (a fling): not asked for after all. */
    fun dropThumbnail(key: String) = synchronized(thumbLock) {
        if (thumbQueue.removeAll { it.first == key }) thumbsPending -= key
    }

    /**
     * Sends the queued tiles in batches of 24 full paths. Paths the answer
     * leaves out have no thumbnail; tiles of a folder that was left are
     * dropped before they are asked for.
     */
    private suspend fun pumpThumbnails() {
        while (true) {
            val batch = synchronized(thumbLock) {
                val left = thumbQueue.filter { it.third != path }
                thumbQueue.removeAll(left)
                thumbsPending -= left.map { it.first }.toSet()
                List(minOf(THUMB_BATCH, thumbQueue.size)) { thumbQueue.removeFirst() }.also { if (it.isEmpty()) pumping = false }
            }
            if (batch.isEmpty()) return
            val folder = batch[0].third
            try {
                val resp = OTCConnection.request { it.setReqGetThumbnails(GetThumbnails.newBuilder().addAllPaths(batch.map { b -> b.second })) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) {
                    val byPath = resp.respListOfFiles.filesList.associateBy { it.path }
                    for ((key, full, _) in batch) {
                        val bmp = byPath[full]?.let { decodeBitmap(it.content.toByteArray(), maxSide = 512) }
                        if (bmp != null) thumbCache.put(key, bmp.asImageBitmap())
                        else synchronized(thumbLock) { noThumb.add(key) }
                    }
                    if (path == folder) _state.update { it.copy(thumbs = thumbCache.snapshot()) }
                }
            } catch (_: Exception) {
            } finally {
                synchronized(thumbLock) { thumbsPending.removeAll(batch.map { it.first }.toSet()) }
            }
        }
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
            val (tmp, mime) = fetchToCache(context, row, "")
            Share.preview(context, tmp, mime)
        } catch (e: ChunkedDownload.Refused) {
            showToast("Could not fetch file")
        } catch (e: Exception) {
            showToast("Download failed: ${e.message}")
        } finally {
            _state.update { it.copy(openingPath = null) }
        }
    }

    /**
     * The file (or its version [hash]) written to the cache under its own
     * name, with its mime. In pieces (ChunkedDownload), except a HEIC: GetFile
     * hands viewers a JPEG of it, as before. Refused: the device said no.
     */
    private suspend fun fetchToCache(context: Context, row: FileRow, hash: String): Pair<File, String?> {
        val tmp = File(context.cacheDir, leafName(row.path))
        if (row.name.endsWith(".heic", ignoreCase = true) || row.raw.mime.equals("image/heic", ignoreCase = true)) {
            val resp = OTCConnection.request { it.setReqGetFile(GetFile.newBuilder().setPath(fullPath(row)).setHash(hash)) }
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_FILE) throw ChunkedDownload.Refused(resp.errorMessage)
            val f = resp.respFile
            withContext(Dispatchers.IO) { tmp.writeBytes(f.content.toByteArray()) }
            return tmp to f.mime.ifEmpty { null }
        }
        val meta = ChunkedDownload.download(fullPath(row), hash, tmp)
        return tmp to meta.mime.ifEmpty { null }
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

    /**
     * Issue #192: keep a folder ([full], its whole path) out of Images, or
     * show it there again ([out]: whether it is kept out now). Only offered
     * where the device takes it - but should it still refuse (another app
     * changed a folder above meanwhile: error_code "out_of_images_by_parent")
     * its message, naming that folder, is shown as it is. Done, Images asks
     * for its photos, tags, people and collections again (ImagesChanged)
     * and TalkBack hears it; either way the folder is listed again.
     *
     * The view model's own work, not the screen's: the device can take most
     * of a minute, and leaving Files meanwhile must neither cut it short
     * (Images not told, a false failure) nor let another change start.
     * While a share or download link is being made it waits, and says so.
     */
    fun toggleOutOfImages(full: String, out: Boolean) {
        val wait = _state.value.preparing
        if (wait != null) { showToast(outOfImagesWait(wait)); return }
        _state.update { it.copy(preparing = SelectionActionTask.OUT_OF_IMAGES, outOfImagesSaid = null) }
        viewModelScope.launch {
            try {
                val resp = OTCConnection.request { it.setReqSetOutOfImages(setOutOfImagesRequest(full, out)) }
                val failure = outOfImagesFailure(resp)
                if (failure != null) showToast(failure)
                else {
                    ImagesChanged.bump()
                    say(if (out) OutOfImagesText.shownSaid(leafName(full)) else OutOfImagesText.keptSaid(leafName(full)))
                }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                showToast("Could not update the folder: ${e.message}")
            } finally {
                _state.update { it.copy(preparing = null) }
            }
            load()
        }
    }

    /** Issue #192: [m] for TalkBack (State.outOfImagesSaid), for a few seconds, so the same words said again are heard again. */
    private fun say(m: String) {
        _state.update { it.copy(outOfImagesSaid = m) }
        viewModelScope.launch {
            delay(5000)
            _state.update { if (it.outOfImagesSaid == m) it.copy(outOfImagesSaid = null) else it }
        }
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
            val (tmp, mime) = fetchToCache(context, row, hash)
            closeVersions()
            Share.preview(context, tmp, mime)
        } catch (e: ChunkedDownload.Refused) {
            showToast("Could not fetch that version")
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
        // The folder it was picked into, read before waiting for a slot:
        // the owner may have opened another one by then.
        val target = joinPath(path, filename)
        uploadSlots.withPermit { uploadTo(target, open) }
    }

    private suspend fun uploadTo(target: String, open: () -> InputStream) {
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
            // Long enough to read: a refusal naming two folders (issue
            // #186's and #192's) runs to a hundred characters or more.
            delay((2500L + 50L * (m.length - 50).coerceAtLeast(0)).coerceAtMost(8000L))
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
    // The lock (issue #132): a tap asks first, saying what upload only
    // does - there is no hover tooltip here as on the web - and a lock that
    // only marks something explains itself.
    var lockPrompt by remember { mutableStateOf<FileRow?>(null) }
    var lockInfo by remember { mutableStateOf<String?>(null) }
    // Issue #192: keeping a folder out of Images, or showing it there
    // again, asks first - keeping it out deletes the tags and faces found
    // in it, and showing it searches it for faces when that is on. The
    // mark on a folder kept out says what that means when tapped.
    // Inside a folder kept out, the mark names the folder keeping it out
    // and has no Show: the device would refuse it (the banner's acts on
    // that folder).
    var outOfImagesAsk by remember { mutableStateOf<OutOfImagesAsk?>(null) }
    var outOfImagesInfo by remember { mutableStateOf<OutOfImagesInfo?>(null) }
    fun askOutOfImages(row: FileRow) { outOfImagesAsk = OutOfImagesAsk(vm.fullPath(row), row.name, row.outOfImages) }
    fun explainOutOfImages(row: FileRow) { outOfImagesInfo = OutOfImagesInfo(row, by = if (st.folderOutOfImages) st.keptOutBy else null) }
    val selectedFolder = st.selectedFolder

    // Photos and videos open in the Images section's viewer, its own
    // instance, paging through this folder's photos and videos only.
    val viewer: PhotoGalleryViewModel = viewModel(key = "files-viewer") { PhotoGalleryViewModel("") }
    val vst by viewer.state.collectAsState()

    // Files' own search, in the narrow layout (a phone held upright): a
    // field at the top for files and folders only (TopSearch's
    // FilesSearchField), with a model of its own, so the Images field keeps
    // its text. Wide, Files has no field: the top bar's search covers
    // files. A change of layout hands the search in use from one field to
    // the other (FilesSearchHandOver) - never two fields, nor what is being
    // typed gone from sight - and the top bar's own text comes back when
    // Files' search leaves it. The top bar's model is MainView's
    // ("topsearch", from the same store).
    val fileSearch: TopSearchViewModel = viewModel(key = "files-search")
    val topSearch: TopSearchViewModel = viewModel(key = "topsearch")
    val handOver: FilesSearchHandOver = viewModel(key = "files-search-handover")
    val fileOptions = rememberFileSearchOptions(fileSearch)
    val noFileSearch by FilesNav.noFileSearch.collectAsState()
    val widthDp = windowWidthDp()
    val narrow = menuLayout(widthDp, menuOpen = true) == MenuLayout.NONE
    val showSearch = filesSearchFieldShown(widthDp, noFileSearch)
    LaunchedEffect(narrow, noFileSearch) { handOver.layoutChanged(narrow, noFileSearch, fileSearch, topSearch) }
    // Leaving Files ends its search, as another section ends the Images
    // one (MainView's go); what is typed stays, and what the top bar holds
    // of it comes back to it. Only recreated (a change this activity
    // doesn't handle itself, as dark mode), Files is straight back with
    // the top bar as it was.
    val activity = LocalActivity.current
    DisposableEffect(Unit) {
        onDispose {
            if (activity?.isChangingConfigurations == true) fileSearch.close() else handOver.filesLeft(fileSearch, topSearch)
        }
    }

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

    // Kept here, not in the lists: the folder keeps its place while search
    // results show over it.
    val listState = rememberLazyListState()
    val gridState = rememberLazyGridState()

    // The Images search sent Files here (FilesNav): to a folder, and maybe
    // to a file in it, opened as a tap on it would; or to the files and
    // folders a text finds. A document opens at once; a photo or video
    // waits for its folder's listing, to page through the folder.
    val request by FilesNav.pending.collectAsState()
    LaunchedEffect(request) {
        val r = request ?: return@LaunchedEffect
        FilesNav.take(r)
        when (r) {
            is FilesRequest.Search -> if (r.text.isNotEmpty()) vm.startSearch(r.text)
            FilesRequest.Leave -> vm.closeResults()
            is FilesRequest.Show -> {
                vm.closeResults()
                val dir = normPath(r.dir)
                val f = r.file
                vm.mediaToOpen = if (f != null && isMediaFile(f)) dir to f else null
                vm.navigate(dir)
                listState.requestScrollToItem(0)
                gridState.requestScrollToItem(0)
                if (f != null && !isMediaFile(f)) scope.launch { vm.open(context, foundRow(f)) }
            }
        }
    }
    // On every listing (thumbGen): the folder listed again, with the same
    // rows, when the search picks a photo in the folder already open.
    LaunchedEffect(st.listedPath, st.thumbGen) {
        val (dir, file) = vm.mediaToOpen ?: return@LaunchedEffect
        if (st.listedPath != dir) return@LaunchedEffect
        vm.mediaToOpen = null
        val media = st.rows.filter { isMedia(it) }
        val row = media.firstOrNull { vm.fullPath(it) == file.path }
        // Not in the listing (the file went): the file alone.
        if (row != null) openRow(row)
        else viewer.showFiles(listOf(PhotoGalleryViewModel.Item("${file.path}#${file.hash}#${file.byteSize}", file.path, file.mime, file.byteSize, null)), 0) { vm.launchLoad() }
    }
    // The system back leaves the results for the folder.
    BackHandler(enabled = st.results != null) { vm.closeResults() }

    // A found folder opens in Files; a photo or video in the viewer, paging
    // through the results' photos and videos; anything else as a tap on it
    // in its folder would.
    fun openFound(f: PbFile) {
        val results = st.results ?: return
        if (isDir(f)) {
            vm.closeResults()
            vm.navigate(normPath(f.path))
            listState.requestScrollToItem(0)
            gridState.requestScrollToItem(0)
            return
        }
        if (isMediaFile(f)) {
            val media = results.files.filter { isMediaFile(it) }
            val items = media.map { x ->
                PhotoGalleryViewModel.Item("${x.path}#${x.hash}#${x.byteSize}", x.path, x.mime, x.byteSize, null, st.thumbs[vm.resultThumbKey(x)]?.asAndroidBitmap())
            }
            viewer.showFiles(items, maxOf(0, media.indexOf(f))) { vm.startSearch(results.text) }
            return
        }
        if (st.openingPath == null) scope.launch { vm.open(context, foundRow(f)) }
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
            // Narrow, the search field over the folder (or the results),
            // in a strip like Images' header: the path field stays as it
            // was, under it, for going to a folder by its path - their left
            // edges lined up.
            if (showSearch) {
                Box(Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerLow).padding(horizontal = 12.dp, vertical = 8.dp)) {
                    FilesSearchField(fileSearch, fileOptions, Modifier.fillMaxWidth())
                }
            }
            Box(Modifier.weight(1f).fillMaxWidth()) {
                val results = st.results
                if (results != null) SearchResultsView(
                    results, folderLabel = if (st.path == "/") "Files" else leafName(st.path), thumbs = st.thumbs, openingPath = st.openingPath,
                    thumbKey = vm::resultThumbKey, onBack = { vm.closeResults() }, onRetry = { vm.startSearch(results.text) }, onOpen = ::openFound,
                ) else Column(Modifier.fillMaxSize()) {
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
                    // Issue #192: in a folder kept out of Images (or inside one),
                    // what that means for its photos and videos - its files carry no
                    // mark of their own - and the way back. Only over the folder it
                    // was listed for.
                    // Inside a folder kept out because one above it is, it names
                    // that one and its Show acts on it - the only Show the device
                    // takes - once ListOutOfImages has said which (no button until
                    // then). Nothing else can start while something is under way.
                    if (st.outOfImagesBanner) {
                        val show = st.outOfImagesCover
                        OutOfImagesBanner(
                            text = if (st.bannerByParent) OutOfImagesText.bannerByParent(st.keptOutBy) else OutOfImagesText.BANNER,
                            show = show?.let { if (st.bannerByParent) OutOfImagesText.showNamed(leafName(it)) else OutOfImagesText.SHOW },
                            busy = st.preparing == SelectionActionTask.OUT_OF_IMAGES, enabled = st.preparing == null,
                        ) { if (show != null) outOfImagesAsk = OutOfImagesAsk(show, leafName(show), out = true) }
                    }

                    PullToRefreshBox(
                        isRefreshing = refreshing,
                        onRefresh = { scope.launch { refreshing = true; vm.load(); refreshing = false } },
                        modifier = Modifier.weight(1f),
                    ) {
                        if (st.grid) LazyVerticalGrid(
                            GridCells.Adaptive(104.dp), Modifier.fillMaxSize(), state = gridState,
                            contentPadding = PaddingValues(horizontal = 8.dp, vertical = 6.dp),
                            horizontalArrangement = Arrangement.spacedBy(10.dp),
                            verticalArrangement = Arrangement.spacedBy(10.dp),
                        ) {
                            items(st.rows, key = { it.path }) { row ->
                                // Asked for as the tile is composed (the visible rows
                                // and the grid's prefetch), again if it was evicted
                                // while shown or failed before a reload.
                                if (isMedia(row)) {
                                    val key = vm.thumbKey(row)
                                    val has = st.thumbs[key] != null
                                    LaunchedEffect(key, has, st.thumbGen) { vm.wantThumbnail(row) }
                                    DisposableEffect(key) { onDispose { vm.dropThumbnail(key) } }
                                }
                                FileGridCell(
                                    row, selected = row.path in st.selected, opening = st.openingPath == row.path,
                                    thumb = if (isMedia(row)) st.thumbs[vm.thumbKey(row)] else null,
                                    onOpen = { openRow(row) },
                                    onToggle = { vm.toggleSelect(row.path) },
                                    onVersions = { scope.launch { vm.openVersions(row) } },
                                    onLock = { lockPrompt = row },
                                    outOfImages = st.outOfImagesSupported && row.outOfImages,
                                    onOutOfImages = { explainOutOfImages(row) },
                                )
                            }
                        } else LazyColumn(Modifier.fillMaxSize(), state = listState) {
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
                                        if (!row.isDir) Text(formatBytes(row.size), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
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
                                    // Issue #192: a folder kept out of Images (or inside
                                    // one) says so, left of its lock so the locks stay
                                    // in one column; a tap says what it means. The
                                    // switch is in the selection's actions.
                                    if (row.path != ".." && row.isDir && row.outOfImages && st.outOfImagesSupported) {
                                        IconButton(onClick = { explainOutOfImages(row) }, modifier = Modifier.size(36.dp)) {
                                            Icon(Icons.Outlined.HideImage, OutOfImagesText.STATE, tint = MaterialTheme.colorScheme.primary)
                                        }
                                    }
                                    if (row.path != ".." && row.isDir) {
                                        IconButton(onClick = { lockPrompt = row }, modifier = Modifier.size(36.dp)) {
                                            Icon(if (row.uploadOnly) Icons.Default.Lock else Icons.Outlined.LockOpen,
                                                if (row.uploadOnly) "Clear upload only" else "Make upload only",
                                                tint = if (row.uploadOnly) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant)
                                        }
                                    } else if (row.uploadOnly) {
                                        Box(Modifier.size(36.dp).clip(CircleShape).clickable { lockInfo = UploadOnlyText.FILE_INFO }, contentAlignment = Alignment.Center) {
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
                        // Issue #192: one folder picked, on a device that can, and
                        // not inside a folder kept out (State.outOfImagesSwitch).
                        onOutOfImages = st.outOfImagesSwitch?.let { row -> { askOutOfImages(row) } },
                        keptOutOfImages = st.outOfImagesSwitch?.outOfImages == true,
                    )
                    Spacer(Modifier.size(8.dp))
                }
                // Its suggestions, over everything under the field.
                if (showSearch) FilesSearchPanel(fileSearch, fileOptions)
            }
        }
        Toast(st.toast, Modifier.align(Alignment.TopCenter).padding(horizontal = 16.dp))
        // Issue #192: a change for Images, once done, said to TalkBack (the
        // web's status line). Nothing to see: the mark and the banner show it.
        Box(Modifier.align(Alignment.BottomStart).size(1.dp).semantics {
            liveRegion = LiveRegionMode.Polite
            contentDescription = st.outOfImagesSaid ?: ""
        })
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
                        Text(formatBytes(row.size), color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    if (st.versionsLoading) CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                    versions.forEach { v ->
                        Row(Modifier.fillMaxWidth().clickable { scope.launch { vm.openVersion(context, row, v.hash) } }.padding(vertical = 8.dp)) {
                            Text("Replaced " + (if (v.hasModified()) DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(v.modified.seconds * 1000)) else "—"),
                                modifier = Modifier.weight(1f))
                            Text(formatBytes(v.byteSize), color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                }
            },
            confirmButton = { TextButton(onClick = { vm.closeVersions() }) { Text("Done") } },
        )
    }
    gallerySource?.let { src -> SharedGalleryShareFlow(src, onDismiss = { gallerySource = null }) }
    lockPrompt?.let { row ->
        AlertDialog(
            onDismissRequest = { lockPrompt = null },
            title = { Text(UploadOnlyText.promptTitle(row.name, row.uploadOnly)) },
            text = { Text(if (row.uploadOnly) UploadOnlyText.CLEAR_MESSAGE else UploadOnlyText.MAKE_MESSAGE) },
            confirmButton = {
                TextButton(onClick = { lockPrompt = null; scope.launch { vm.toggleUploadOnly(row) } }) {
                    Text(if (row.uploadOnly) "Allow deletions" else "Make upload only")
                }
            },
            dismissButton = { TextButton(onClick = { lockPrompt = null }) { Text("Cancel") } },
        )
    }
    lockInfo?.let {
        AlertDialog(onDismissRequest = { lockInfo = null }, title = { Text("Upload only") }, text = { Text(it) },
            confirmButton = { TextButton(onClick = { lockInfo = null }) { Text("OK") } })
    }
    outOfImagesAsk?.let { a ->
        AlertDialog(
            onDismissRequest = { outOfImagesAsk = null },
            title = { Text(if (a.out) OutOfImagesText.showTitle(a.name) else OutOfImagesText.keepTitle(a.name)) },
            text = { Text(if (a.out) OutOfImagesText.SHOW_MESSAGE else OutOfImagesText.KEEP_MESSAGE) },
            confirmButton = {
                // Not while a share or download link is being made: it
                // would wait for that, so the button does.
                TextButton(onClick = { outOfImagesAsk = null; vm.toggleOutOfImages(a.full, a.out) }, enabled = st.preparing == null) {
                    Text(if (a.out) OutOfImagesText.SHOW else OutOfImagesText.KEEP)
                }
            },
            dismissButton = { TextButton(onClick = { outOfImagesAsk = null }) { Text("Cancel") } },
        )
    }
    outOfImagesInfo?.let { info ->
        AlertDialog(
            onDismissRequest = { outOfImagesInfo = null },
            title = { Text(OutOfImagesText.STATE) },
            text = { Text(info.by?.let { OutOfImagesText.byParentInfo(it) } ?: OutOfImagesText.EXPLAIN) },
            confirmButton = { TextButton(onClick = { outOfImagesInfo = null }) { Text("OK") } },
            dismissButton = if (info.by != null) null else {
                { TextButton(onClick = { outOfImagesInfo = null; askOutOfImages(info.row) }, enabled = st.preparing == null) { Text(OutOfImagesText.SHOW) } }
            },
        )
    }
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
 * selection circle (top left), lock (folders, top right) and the mark of a
 * folder kept out of Images beside it, versions count (bottom right) and a
 * play badge on videos (bottom left); the name below.
 */
@Composable
private fun FileGridCell(
    row: FileRow, selected: Boolean, opening: Boolean, thumb: ImageBitmap?,
    onOpen: () -> Unit, onToggle: () -> Unit, onVersions: () -> Unit, onLock: () -> Unit,
    outOfImages: Boolean = false, onOutOfImages: () -> Unit = {},
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
            // No long-press menu here: a tap on the lock offers to clear it.
            if (row.isDir && row.uploadOnly) {
                Box(Modifier.align(Alignment.TopEnd).padding(6.dp).size(24.dp).clip(CircleShape)
                    .background(MaterialTheme.colorScheme.surface.copy(alpha = 0.85f)).clickable(onClick = onLock),
                    contentAlignment = Alignment.Center) {
                    Icon(Icons.Default.Lock, "Upload only", tint = MaterialTheme.colorScheme.primary, modifier = Modifier.size(14.dp))
                }
            }
            // Issue #192: left of the lock, which keeps its corner.
            if (row.isDir && outOfImages) {
                Box(Modifier.align(Alignment.TopEnd).padding(top = 6.dp, end = if (row.uploadOnly) 34.dp else 6.dp).size(24.dp).clip(CircleShape)
                    .background(MaterialTheme.colorScheme.surface.copy(alpha = 0.85f)).clickable(onClick = onOutOfImages),
                    contentAlignment = Alignment.Center) {
                    Icon(Icons.Outlined.HideImage, OutOfImagesText.STATE, tint = MaterialTheme.colorScheme.primary, modifier = Modifier.size(14.dp))
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

/**
 * The search's results over the folder: a way back to it, what was
 * searched for and how many were found, and each file or folder with the
 * folder it is in (the part the text matched in bold).
 */
@Composable
private fun SearchResultsView(
    results: SearchResults, folderLabel: String, thumbs: Map<String, ImageBitmap>, openingPath: String?,
    thumbKey: (PbFile) -> String, onBack: () -> Unit, onRetry: () -> Unit, onOpen: (PbFile) -> Unit,
) {
    val colors = MaterialTheme.colorScheme
    val q = remember(results.text) { FilesNav.fold(results.text).text }
    val found = remember(results) { results.files.map { it to FilesNav.foundParts(it.path, q) } }
    Column(Modifier.fillMaxSize()) {
        TextButton(onClick = onBack, modifier = Modifier.padding(start = 4.dp, top = 4.dp)) {
            Icon(Icons.AutoMirrored.Filled.ArrowBack, null, Modifier.size(18.dp))
            Spacer(Modifier.width(8.dp))
            Text("Back to $folderLabel", maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        Text("Files matching \u201C${results.text}\u201D", style = MaterialTheme.typography.titleMedium, maxLines = 2, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(horizontal = 16.dp))
        // Its line kept while searching, so the list doesn't move.
        Text(
            if (results.phase == SearchResults.Phase.DONE && found.isNotEmpty())
                (if (found.size == SEARCH_LIMIT) "Showing the first $SEARCH_LIMIT results" else "${found.size} ${if (found.size == 1) "result" else "results"}")
            else "",
            style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant, modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
        )
        when (results.phase) {
            SearchResults.Phase.LOADING -> Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
                CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                Spacer(Modifier.width(10.dp))
                Text("Searching\u2026", color = colors.onSurfaceVariant)
            }
            SearchResults.Phase.ERROR -> Column(Modifier.padding(16.dp)) {
                Text(results.error ?: "", color = colors.error)
                if (results.retry) TextButton(onClick = onRetry) { Text("Try again") }
            }
            SearchResults.Phase.DONE -> if (found.isEmpty()) {
                Text("No files match \u201C${results.text}\u201D.", color = colors.onSurfaceVariant, modifier = Modifier.padding(16.dp))
            }
        }
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(bottom = 12.dp)) {
            items(found, key = { it.first.path }) { (f, parts) ->
                val dir = isDir(f)
                val thumb = if (isMediaFile(f)) thumbs[thumbKey(f)] else null
                Row(
                    Modifier.fillMaxWidth().clickable(enabled = openingPath == null) { onOpen(f) }.padding(horizontal = 16.dp, vertical = 8.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Box(Modifier.size(44.dp).clip(RoundedCornerShape(8.dp)).background(colors.surfaceContainerLow), contentAlignment = Alignment.Center) {
                        when {
                            dir -> Icon(Icons.Default.Folder, null, Modifier.size(28.dp), tint = colors.primary)
                            thumb != null -> Image(thumb, null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
                            else -> FileTypeIcon(parts.name, Modifier.fillMaxSize().padding(5.dp))
                        }
                        if (thumb != null && isVideo(f)) {
                            Box(Modifier.align(Alignment.BottomStart).padding(3.dp).size(16.dp).background(Color.Black.copy(alpha = 0.55f), CircleShape),
                                contentAlignment = Alignment.Center) { Icon(Icons.Default.PlayArrow, "Video", tint = Color.White, modifier = Modifier.size(11.dp)) }
                        }
                        if (openingPath == f.path) CircularProgressIndicator(Modifier.size(22.dp), strokeWidth = 2.dp)
                    }
                    Spacer(Modifier.width(12.dp))
                    Column(Modifier.weight(1f)) {
                        // Issue #192: in (or itself) a folder kept out of
                        // Images - only a mark here; the switch is on the
                        // folder in its own listing.
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Text(marked(parts.name, parts.nameSpan), maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                            if (f.outOfImages) {
                                Spacer(Modifier.width(6.dp))
                                Icon(Icons.Outlined.HideImage, OutOfImagesText.STATE, Modifier.size(16.dp), tint = colors.primary)
                            }
                        }
                        // Long folders lose their start, not the end nearest the file.
                        Text(marked(parts.dir, parts.dirSpan, colors.onSurface), style = MaterialTheme.typography.bodySmall,
                            color = colors.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.StartEllipsis)
                    }
                    if (!dir) {
                        Spacer(Modifier.width(8.dp))
                        Text(formatBytes(f.byteSize), style = MaterialTheme.typography.labelSmall, color = colors.onSurfaceVariant)
                    }
                }
            }
        }
    }
}

/** A found name or folder with the part the text matched in bold. */
private fun marked(text: String, span: Span?, markColor: Color? = null) = buildAnnotatedString {
    if (span == null || span.first < 0 || span.last >= text.length) { append(text); return@buildAnnotatedString }
    append(text.substring(0, span.first))
    withStyle(SpanStyle(fontWeight = FontWeight.Bold, color = markColor ?: Color.Unspecified)) { append(text.substring(span.first, span.last + 1)) }
    append(text.substring(span.last + 1))
}

/** What the upload-only lock says (issue #132). The same words as iOS's
 *  UploadOnlyText and the web lock's tooltip. */
private object UploadOnlyText {
    fun promptTitle(name: String, uploadOnly: Boolean) =
        if (uploadOnly) "Allow deletions in \u201C$name\u201D again?" else "Make \u201C$name\u201D upload only?"
    const val MAKE_MESSAGE = "Nothing in this folder can be deleted - from this phone, a computer or the web - and uploading a file again keeps its older version. Good for photo archives and backups."
    const val CLEAR_MESSAGE = "Files in this folder can be deleted again, and uploading a file again replaces it. The older versions kept so far stay."
    const val FILE_INFO = "This is in an upload-only folder: it can't be deleted, and uploading it again keeps its older version. The lock on the folder changes it."
}

/** Issue #192: the folder a confirmation is for - its whole path, its name, and whether it is kept out of Images now. */
private data class OutOfImagesAsk(val full: String, val name: String, val out: Boolean)

/** Issue #192: a folder's mark, tapped - [by]: the folder keeping it out when it is inside one kept out (no Show of its own then). */
private data class OutOfImagesInfo(val row: FileRow, val by: String?)

/** What keeping a folder out of Images says (issue #192). The same words as
 *  iOS's OutOfImagesText (in its Title Case for menus) and the web's. */
internal object OutOfImagesText {
    const val KEEP = "Keep out of Images"
    const val SHOW = "Show in Images"
    const val STATE = "Kept out of Images"
    const val EXPLAIN = "Photos and videos here aren't tagged, searched for faces or shown in Images. Files still shows them."
    fun keepTitle(name: String) = "Keep \u201C$name\u201D out of Images?"
    const val KEEP_MESSAGE = "Its photos and videos won't be tagged, searched for faces or shown in Images, and the tags and faces already found in them are deleted. Files still shows them."
    fun showTitle(name: String) = "Show \u201C$name\u201D in Images?"
    const val SHOW_MESSAGE = "Its photos and videos go back to Images, and are tagged - and searched for faces, if face recognition is on - in the background."
    const val BANNER = "Kept out of Images - photos and videos here aren't tagged, searched for faces or shown in Images."
    // Inside a folder kept out because one above it is (the web's words):
    // the banner names that one, and its button shows it.
    fun bannerByParent(parent: String) = "Inside $parent, which is kept out of Images - photos and videos here aren't tagged, searched for faces or shown in Images."
    fun showNamed(name: String) = "Show \u201C$name\u201D in Images"
    fun byParent(parent: String) = "Inside $parent, which is kept out of Images"
    fun byParentInfo(parent: String) = "${byParent(parent)}. $EXPLAIN"
    // Said to TalkBack once a change is done.
    fun keptSaid(name: String) = "$name kept out of Images"
    fun shownSaid(name: String) = "$name shown in Images"
}

/** Issue #192: over a folder kept out of Images - what that means, and the way to show it again ([show]: its label; none yet when null). */
@Composable
private fun OutOfImagesBanner(text: String, show: String?, busy: Boolean, enabled: Boolean, onShow: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    // The button under the words, at the end: beside them a folder's name
    // in it ("Show “Trips 2024” in Images") left the words a narrow column.
    Column(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp).clip(RoundedCornerShape(10.dp))
            .background(colors.surfaceContainerLow).border(1.dp, colors.outlineVariant, RoundedCornerShape(10.dp))
            .padding(start = 12.dp, end = 4.dp, top = 8.dp, bottom = if (show != null) 0.dp else 8.dp),
    ) {
        Row(Modifier.padding(end = 8.dp)) {
            Icon(Icons.Outlined.HideImage, null, Modifier.padding(top = 1.dp).size(18.dp), tint = colors.primary)
            Spacer(Modifier.width(10.dp))
            Text(text, style = MaterialTheme.typography.bodySmall, modifier = Modifier.weight(1f))
        }
        if (show != null) TextButton(onClick = onShow, enabled = enabled, modifier = Modifier.align(Alignment.End)) {
            if (busy) { CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp); Spacer(Modifier.width(6.dp)) }
            Text(show, maxLines = 2, overflow = TextOverflow.Ellipsis, textAlign = TextAlign.End)
        }
    }
}

/**
 * The window's width in dp, as MainView takes it for its layout (the whole
 * window, edge to edge); the configuration's until the window is measured.
 */
@Composable
private fun windowWidthDp(): Float {
    val px = LocalWindowInfo.current.containerSize.width
    return if (px > 0) with(LocalDensity.current) { px.toDp().value } else LocalConfiguration.current.screenWidthDp.toFloat()
}

private fun queryDisplayName(context: Context, uri: Uri): String? =
    context.contentResolver.query(uri, null, null, null, null)?.use { c ->
        val idx = c.getColumnIndex(android.provider.OpenableColumns.DISPLAY_NAME)
        if (idx >= 0 && c.moveToFirst()) c.getString(idx) else null
    }
