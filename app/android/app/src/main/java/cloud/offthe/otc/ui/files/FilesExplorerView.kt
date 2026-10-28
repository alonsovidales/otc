// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import android.content.Context
import android.net.Uri
import android.util.LruCache
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
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
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.ui.TourTarget
import cloud.offthe.otc.ui.tourTarget
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidFilesAllowDeletions
import cloud.offthe.otc.i18n.appFilesCannotRead
import cloud.offthe.otc.i18n.appFilesUploading
import cloud.offthe.otc.i18n.androidFilesFolderUpdateFailedDetail
import cloud.offthe.otc.i18n.androidFilesInsideKeptOut
import cloud.offthe.otc.i18n.androidFilesInsideKeptOutBanner
import cloud.offthe.otc.i18n.androidFilesInsideKeptOutInfo
import cloud.offthe.otc.i18n.androidFilesKeptOutSaid
import cloud.offthe.otc.i18n.androidFilesShowNamed
import cloud.offthe.otc.i18n.androidFilesShownSaid
import cloud.offthe.otc.i18n.androidFilesWaitDownloadLink
import cloud.offthe.otc.i18n.androidFilesWaitFolder
import cloud.offthe.otc.i18n.androidFilesWaitShareLink
import cloud.offthe.otc.i18n.appFilesAllowDeletionsMessage
import cloud.offthe.otc.i18n.appFilesAllowDeletionsTitle
import cloud.offthe.otc.i18n.appFilesBackToFiles
import cloud.offthe.otc.i18n.appFilesBackToFolder
import cloud.offthe.otc.i18n.appFilesClearUploadOnly
import cloud.offthe.otc.i18n.appFilesDeleteConfirm
import cloud.offthe.otc.i18n.appFilesDeleteRefused
import cloud.offthe.otc.i18n.appFilesDownloadFailed
import cloud.offthe.otc.i18n.appFilesDownloadLinkFailed
import cloud.offthe.otc.i18n.appFilesFolderUpdateFailed
import cloud.offthe.otc.i18n.appFilesInUploadOnly
import cloud.offthe.otc.i18n.appFilesKeepOutMessage
import cloud.offthe.otc.i18n.appFilesKeepOutTitle
import cloud.offthe.otc.i18n.appFilesKeptOut
import cloud.offthe.otc.i18n.appFilesKeptOutBanner
import cloud.offthe.otc.i18n.appFilesKeptOutExplain
import cloud.offthe.otc.i18n.appFilesListFailed
import cloud.offthe.otc.i18n.appFilesMakeUploadOnly
import cloud.offthe.otc.i18n.appFilesMakeUploadOnlyMessage
import cloud.offthe.otc.i18n.appFilesMakeUploadOnlyTitle
import cloud.offthe.otc.i18n.appFilesNoResults
import cloud.offthe.otc.i18n.appFilesOpenFailed
import cloud.offthe.otc.i18n.appFilesResultsCount
import cloud.offthe.otc.i18n.appFilesResultsLimited
import cloud.offthe.otc.i18n.appFilesResultsTitle
import cloud.offthe.otc.i18n.appFilesSearchNoAnswer
import cloud.offthe.otc.i18n.appFilesSearchTooOld
import cloud.offthe.otc.i18n.appFilesSearchUnreached
import cloud.offthe.otc.i18n.appFilesSearching
import cloud.offthe.otc.i18n.appFilesSelectionUploadOnly
import cloud.offthe.otc.i18n.appFilesShareLinkFailed
import cloud.offthe.otc.i18n.appFilesSortBy
import cloud.offthe.otc.i18n.appPhotosNoticeDeleteFailed
import cloud.offthe.otc.i18n.appFilesViewGrid
import cloud.offthe.otc.i18n.appFilesViewList
import cloud.offthe.otc.i18n.appFilesViewStorage
import cloud.offthe.otc.i18n.appFilesViewOptions
import cloud.offthe.otc.i18n.LanguageSettings
import androidx.core.content.edit
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.stateDescription
import cloud.offthe.otc.i18n.appFilesShowMessage
import cloud.offthe.otc.i18n.appFilesShowTitle
import cloud.offthe.otc.i18n.appFilesUnexpectedResponse
import cloud.offthe.otc.i18n.appFilesUploadFailed
import cloud.offthe.otc.i18n.appFilesUploadOnly
import cloud.offthe.otc.i18n.appFilesUploadOnlyFileInfo
import cloud.offthe.otc.i18n.appFilesVersionCurrent
import cloud.offthe.otc.i18n.appFilesVersionOpenFailed
import cloud.offthe.otc.i18n.appFilesVersionReplaced
import cloud.offthe.otc.i18n.appFilesVersionsExplain
import cloud.offthe.otc.i18n.appFilesVersionsTitle
import cloud.offthe.otc.i18n.appFilesVideo
import cloud.offthe.otc.i18n.appShellKeepOutOfImages
import cloud.offthe.otc.i18n.appShellShowInImages
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonDelete
import cloud.offthe.otc.i18n.commonDeselect
import cloud.offthe.otc.i18n.commonDone
import cloud.offthe.otc.i18n.commonOk
import cloud.offthe.otc.i18n.commonSelect
import cloud.offthe.otc.i18n.commonTryAgain
import cloud.offthe.otc.ui.common.LoadSubject
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
import cloud.offthe.otc.R
import cloud.offthe.otc.data.ImagesChanged
import cloud.offthe.otc.net.ChunkedDownload
import cloud.offthe.otc.net.ChunkedUpload
import cloud.offthe.otc.net.NetworkWatch
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.retryWaitMs
import cloud.offthe.otc.net.sleepOrWake
import cloud.offthe.otc.ui.common.DEVICE_UNREACHABLE
import cloud.offthe.otc.ui.common.DragSelection
import cloud.offthe.otc.ui.common.dragSelect
import cloud.offthe.otc.ui.common.keysView
import cloud.offthe.otc.ui.common.keyAt
import cloud.offthe.otc.ui.common.rememberDragSelect
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.TextIOException
import cloud.offthe.otc.ui.common.backOnline
import cloud.offthe.otc.ui.common.loadProblem
import cloud.offthe.otc.ui.common.loadProblemText
import cloud.offthe.otc.net.byteSize
import cloud.offthe.otc.proto.DelFile
import cloud.offthe.otc.proto.GetFolderUsage
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
import cloud.offthe.otc.ui.common.now
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
import androidx.compose.material.icons.filled.PieChart
import androidx.compose.material.icons.filled.GridView
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import cloud.offthe.otc.ui.common.FileTypeIcon
import cloud.offthe.otc.ui.common.THUMB_BATCH
import cloud.offthe.otc.ui.common.TILES_KIND
import cloud.offthe.otc.ui.common.ThumbKind
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.decodeBitmap
import cloud.offthe.otc.ui.common.takeAnswer
import cloud.offthe.otc.ui.common.thumbTileKey
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
internal fun outOfImagesFailure(resp: RespEnvelope): UiText? = when {
    resp.error -> deviceSays(resp.errorMessage) { S.appFilesFolderUpdateFailed() }
    resp.payloadCase != RespEnvelope.PayloadCase.RESP_ACK || !resp.respAck.ok -> deviceSays(resp.respAck.errorMsg) { S.appFilesFolderUpdateFailed() }
    else -> null
}

/** The device's own words when it gave some (it wrote them in the app's language), else ours. */
private inline fun deviceSays(words: String, ours: () -> UiText): UiText = if (words.isEmpty()) ours() else UiText.Raw(words)

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
internal fun outOfImagesWait(task: SelectionActionTask): UiText = when (task) {
    SelectionActionTask.SHARE -> S.androidFilesWaitShareLink()
    SelectionActionTask.DOWNLOAD -> S.androidFilesWaitDownloadLink()
    // UNCOLLECT, NOT_PERSON and MOVE_PERSON are Images' alone, never under way in Files.
    SelectionActionTask.OUT_OF_IMAGES, SelectionActionTask.UNCOLLECT,
    SelectionActionTask.NOT_PERSON, SelectionActionTask.MOVE_PERSON -> S.androidFilesWaitFolder()
}

/**
 * Issue #201: a row's delete - with the content the row showed (DelFile.hash)
 * for a file whose hash the listing gave, so one changed since (synced over,
 * edited) is refused as "changed" rather than deleted unseen. A folder's
 * path goes alone, as before.
 */
internal fun filesDelete(full: String, row: FileRow?): DelFile {
    val b = DelFile.newBuilder().setPath(full)
    if (row != null && !row.isDir && row.raw.hash.isNotEmpty()) b.hash = row.raw.hash
    return b.build()
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

/** What the system Back does in Files, before it leaves the section. */
internal enum class FilesBack { CLOSE_RESULTS, CLEAR_SELECTION }

/**
 * The system Back in Files: the search's results go back to the folder
 * (they show in its place, its selection and bar hidden under them); over
 * the folder, a selection is cleared, as the pill's × does - not while
 * the search is open ([searchOpen]: its own Back closes it, as Images').
 * Null: Back leaves Files as before. Whatever is drawn over Files (the
 * viewer, a dialog, a menu) takes Back first, in its own window.
 */
internal fun filesBack(selection: Boolean, results: Boolean, searchOpen: Boolean): FilesBack? = when {
    results -> FilesBack.CLOSE_RESULTS
    selection && !searchOpen -> FilesBack.CLEAR_SELECTION
    else -> null
}

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
data class SearchResults(val text: String, val phase: Phase, val files: List<PbFile> = emptyList(), val error: UiText? = null, val retry: Boolean = true) {
    enum class Phase { LOADING, DONE, ERROR }
}
private const val SEARCH_LIMIT = 50

// The grid's padding on its sides: where its tiles start, for the drag to select's hit test (issue #200).
private val FILES_GRID_SIDE = 8.dp

// uploadOnly/versions: issue #132 - inside (or itself) an upload-only
// folder, and how many older versions the device keeps for the file.
// outOfImages: issue #192 - inside (or itself) a folder kept out of Images.
data class FileRow(val path: String, val name: String, val isDir: Boolean, val size: Long, val raw: PbFile,
                   val uploadOnly: Boolean = false, val versions: Int = 0, val outOfImages: Boolean = false)

class FilesExplorerViewModel(initialPath: String) : ViewModel() {
    data class State(
        val path: String,
        // The folder's rows as shown: the listing in the order chosen (issue #196).
        val rows: List<FileRow> = emptyList(),
        val loading: Boolean = false,
        // Issue #196: the order of the list, the grid and the search's
        // results, one choice for the app (FilesSort.kt), kept as the view
        // mode is ("files_sort"); a big folder's rows being put in order
        // (FilesSort.OFF_MAIN_ABOVE); and the new order, said to TalkBack.
        val sort: FilesSortOrder = FilesSortOrder.DEFAULT,
        val sorting: Boolean = false,
        val sortSaid: UiText? = null,
        val error: UiText? = null,
        val selected: Set<String> = emptySet(),
        val toast: UiText? = null,
        val openingPath: String? = null,
        val confirmDeleteSelected: Boolean = false,
        val preparing: SelectionActionTask? = null,
        // Issue #132: the versions pop-up - the file and its older versions.
        val versionsOf: Pair<FileRow, List<PbFile>>? = null,
        val versionsLoading: Boolean = false,
        // List, grid or (issue #195) Storage, kept across launches ("files_view_mode").
        val view: FilesView = FilesView.parse(prefs.getString(VIEW_MODE_KEY, null)),
        // Issue #195: the Storage view's answer for the folder shown (null
        // in the list and the grid, which never ask for it).
        val usage: UsageState? = null,
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
        val outOfImagesSaid: UiText? = null,
        // The files picked to upload, while they go: "Uploading N of M"
        // over the bar, with Cancel (UploadQueue); null: none.
        val uploads: UploadProgress? = null,
    ) {
        val grid: Boolean get() = view == FilesView.GRID
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
        private val prefs get() = OTCApp.instance.getSharedPreferences("otc_settings", Context.MODE_PRIVATE)
    }

    // Issue #196: the last listing as the device sent it, the folder the
    // rows shown are of (a new folder's listing being put in order doesn't
    // keep the last folder's rows on screen meanwhile), and a count bumped
    // by every arrangement: only the last one asked for lands.
    @Volatile private var listing: List<FileRow> = emptyList()
    @Volatile private var rowsFolder: String? = null
    private val sortSeq = java.util.concurrent.atomic.AtomicInteger()
    /** The language names are compared in: the app's. */
    private fun sortLocale() = LanguageSettings.numberLocale

    // Picked files upload two at a time: each one hashes and holds a chunk
    // while it waits for ChunkedUpload's send permits. In the view model's
    // scope, not the screen's: leaving Files used to cancel them, each
    // reported as failed ("was cancelled").
    private val uploads = UploadQueue<UploadPick>(
        viewModelScope, slots = 2,
        send = { pick -> uploadTo(pick) },
        onChange = { p -> _state.update { it.copy(uploads = p) } },
    )

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

    // Settings' Clear thumbnail cache: the decoded tiles (the folder's and the
    // search results') and the "none" marks go too; the tiles ask again.
    init {
        viewModelScope.launch {
            var seen = ThumbStore.clears.value
            ThumbStore.clears.collect { n ->
                if (n == seen) return@collect
                seen = n
                thumbCache.evictAll()
                synchronized(thumbLock) { noThumb.clear() }
                _state.update { it.copy(thumbs = emptyMap(), thumbGen = it.thumbGen + 1) }
            }
        }
    }

    private val _state = MutableStateFlow(State(path = initialPath, sort = FilesSortOrder.parse(prefs.getString(FilesSortOrder.STORAGE_KEY, null))))
    val state: StateFlow<State> = _state
    val path get() = _state.value.path

    // Bumped by every listing that lands: ListOutOfImages' answer is for
    // the listing that asked, not one that replaced it meanwhile.
    private val listings = java.util.concurrent.atomic.AtomicInteger()

    // A listing that failed on the way (no answer in time, a dropped
    // connection, the bridge's "device unreachable") is asked again, 1 s
    // doubling to 10 s and at once on a Wake, while its folder is still the
    // one shown and nothing newer was asked for (listingAsks). The device's
    // own refusal (a folder that isn't there) is not.
    private val listingAsks = java.util.concurrent.atomic.AtomicInteger()
    @Volatile private var listingFailures = 0
    // Why the listing of failedPath failed: said while it is asked again
    // (not cleared at each try, which flickered and was announced anew).
    @Volatile private var listingProblem: LoadProblem? = null
    @Volatile private var failedPath: String? = null

    private fun listingFailed(p: String, problem: LoadProblem) {
        listingProblem = problem
        failedPath = p
        _state.update { it.copy(error = loadProblemText(problem, LoadSubject.FILES)) }
        val ask = listingAsks.get()
        val wait = retryWaitMs(++listingFailures)
        viewModelScope.launch {
            sleepOrWake(wait)
            if (listingAsks.get() == ask && path == p) load()
        }
    }

    suspend fun load() {
        // The folder asked for: a listing that comes back after another
        // folder was opened (the search opening one as Files appears) is
        // not that folder's, and is dropped.
        val p = path
        // Issue #195: in the Storage view, its sizes too - the folder
        // opened, a refresh, an upload done.
        if (_state.value.view == FilesView.STORAGE) askUsage()
        listingAsks.incrementAndGet()
        val still = listingErrorWhileAsking(listingProblem, failedPath, p, NetworkWatch.online())
        _state.update { it.copy(loading = true, error = still) }
        var resp: RespEnvelope? = null
        try {
            // A folder of thousands of names is a page of its own, with
            // more time after a timeout (OTCConnection.ask).
            resp = OTCConnection.ask("listing", OTCConnection.PAGE_TIMEOUT_MS) { it.setReqListFiles(ListFiles.newBuilder().setPath(p)) }
            if (path != p) return
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.code == DEVICE_UNREACHABLE) {
                listingFailed(p, problemOf(resp, null))
            } else if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) {
                listingFailures = 0
                listingProblem = null
                _state.update { it.copy(error = null) }
                val lof = resp.respListOfFiles
                val rows = listingRows(lof, p)
                val folderOut = lof.outOfImagesSupported && lof.folderOutOfImages
                val listing = listings.incrementAndGet()
                // Issue #196: in the order chosen - a big folder's off the
                // main thread, its rows coming once they are in order.
                val order = _state.value.sort
                val mine = sortSeq.incrementAndGet()
                this.listing = rows
                val now = if (rows.size <= FilesSort.OFF_MAIN_ABOVE) FilesSort.sortedRows(rows, order, sortLocale()) else null
                val sameFolder = rowsFolder == p
                rowsFolder = p
                // Issue #200: a new listing ends a drag to select under way,
                // before its selection (none) is set: the finger's later
                // moves do nothing and its lift is still no tap.
                dragSelection = null
                _state.update {
                    it.copy(rows = now ?: if (sameFolder) it.rows else emptyList(), sorting = now == null,
                        selected = emptySet(), thumbGen = it.thumbGen + 1, listedPath = p,
                        outOfImagesSupported = lof.outOfImagesSupported, folderOutOfImages = lof.folderOutOfImages, outOfImagesCover = null)
                }
                // Issue #192: in a folder kept out, which folder keeps it
                // out - asked after the listing shows, so a slow answer
                // never holds it up.
                if (folderOut) viewModelScope.launch { askCover(p, listing) }
                if (now == null) {
                    val sorted = withContext(Dispatchers.Default) { FilesSort.sortedRows(rows, order, sortLocale()) }
                    if (sortSeq.get() == mine) _state.update { it.copy(rows = sorted, sorting = false, thumbGen = it.thumbGen + 1) }
                }
            } else if (resp.error) {
                if (mediaToOpen?.first == p) mediaToOpen = null
                listingProblem = null
                _state.update { it.copy(error = deviceSays(resp.errorMessage) { S.appFilesListFailed() }) }
            } else {
                listingProblem = null
                _state.update { it.copy(error = S.appFilesUnexpectedResponse()) }
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            if (path == p) listingFailed(p, problemOf(resp, e))
        } finally {
            if (path == p) _state.update { it.copy(loading = false) }
        }
    }

    private fun problemOf(resp: RespEnvelope?, error: Throwable?) =
        loadProblem(resp, error, NetworkWatch.online(), OTCConnection.statusCode.value)

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
            fun fail(error: UiText, retry: Boolean = true) {
                if (mine == searchSeq) _state.update { it.copy(results = SearchResults(text, SearchResults.Phase.ERROR, error = error, retry = retry)) }
            }
            val resp = try {
                OTCConnection.request { it.setReqSearchFiles(SearchFiles.newBuilder().setQuery(text).setLimit(SEARCH_LIMIT)) }
            } catch (e: Exception) {
                fail(S.appFilesSearchUnreached())
                return@launch
            }
            // The Images search stops offering it on such a device.
            if (resp.isUnknownPayload()) FilesNav.deviceCantSearchFiles()
            if (mine != searchSeq) return@launch
            when {
                resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES -> {
                    // In the folder's order (issue #196): at most SEARCH_LIMIT of them.
                    val files = FilesSort.sortedFiles(resp.respListOfFiles.filesList, _state.value.sort, sortLocale())
                    _state.update { it.copy(results = SearchResults(text, SearchResults.Phase.DONE, files)) }
                    resultThumbnails(files)
                }
                resp.isUnknownPayload() -> fail(S.appFilesSearchTooOld(), retry = false)
                else -> fail(deviceSays(resp.errorMessage) { S.appFilesSearchNoAnswer() })
            }
        }
    }

    /** Back to the folder. */
    fun closeResults() {
        if (_state.value.results == null) return
        searchSeq++
        _state.update { it.copy(results = null) }
    }

    /** The results' photos and videos, by their thumbnails: the phone's cache first, the rest asked for together, 24 paths at a time. */
    private suspend fun resultThumbnails(files: List<PbFile>) {
        val want = files.filter { isMediaFile(it) && thumbCache.get(resultThumbKey(it)) == null }.map { TileAsk(resultThumbKey(it), it.path) }
        if (want.isEmpty()) return
        var rest = try { thumbsFromCache(want) } catch (_: Exception) { want }
        if (rest.size < want.size || rest.any { it.reask }) _state.update { it.copy(thumbs = thumbCache.snapshot()) }
        while (rest.isNotEmpty()) {
            val batch = rest.take(THUMB_BATCH)
            rest = rest.drop(THUMB_BATCH)
            try {
                // Paths the answer didn't get to (ask_again_from): first in line.
                rest = askThumbnails(batch).second + rest
            } catch (_: Exception) { continue }
            _state.update { it.copy(thumbs = thumbCache.snapshot()) }
        }
    }

    /** A tile to ask the device for: its key (full path NUL hash), its full path, and whether it is asked again for its small one. */
    private data class TileAsk(val key: String, val path: String, val reask: Boolean = false)

    /**
     * Tiles ([TileAsk.key], [TileAsk.path]) from the phone's thumbnail cache
     * (ThumbStore), decoded into thumbCache: a small one is enough; a big or
     * unknown one shows at once and is asked for again once a launch
     * (ThumbStore.claimFilesReasks: Files' own, under the still-big rule).
     * Bytes that don't decode are dropped and asked for. The ones to ask the
     * device for.
     */
    private suspend fun thumbsFromCache(wanted: List<TileAsk>): List<TileAsk> {
        val kinds = ThumbStore.kinds(wanted.map { it.key.substringAfter('\u0000') }.filter { it.isNotEmpty() })
        val stale = wanted.map { it.key.substringAfter('\u0000') }.filter { kinds[it] == ThumbKind.BIG || kinds[it] == ThumbKind.UNKNOWN }
        val reask = ThumbStore.claimFilesReasks(stale)
        val ask = ArrayList<TileAsk>()
        for (w in wanted) {
            val hash = w.key.substringAfter('\u0000')
            val kind = kinds[hash]
            if (kind != null) {
                val tile = thumbTileKey(hash, kind)
                val bytes = ThumbStore.load(tile)
                val bmp = bytes?.let { b -> withContext(Dispatchers.Default) { decodeBitmap(b, maxSide = 512) } }
                if (bmp != null) {
                    thumbCache.put(w.key, bmp.asImageBitmap())
                    if (hash !in reask) continue
                    ask += w.copy(reask = true)
                    continue
                }
                // Kept but not decodable: dropped, asked for.
                if (bytes != null) ThumbStore.discard(tile)
            }
            ask += w
        }
        return ask
    }

    /**
     * One GetThumbnails (small ones) for [ask] (at most THUMB_BATCH): each
     * thumbnail kept in the phone's cache under the answer's hash and decoded
     * into thumbCache. The keys the device has none for (release 113: looked
     * at and left out - never from an older device, whose answers can be cut
     * at 8 MB unsaid - and, from any, bytes that don't decode, which are
     * dropped), and the ones to ask again (not looked at: ask_again_from). A
     * re-ask answered big again is noted still big. Throws when the request
     * fails.
     */
    private suspend fun askThumbnails(ask: List<TileAsk>): Pair<Set<String>, List<TileAsk>> {
        val paths = ask.map { it.path }
        val scope = ThumbStore.scope()
        val resp = OTCConnection.ask(TILES_KIND, OTCConnection.PAGE_TIMEOUT_MS) { it.setReqGetThumbnails(GetThumbnails.newBuilder().addAllPaths(paths).setSmallThumbnails(true)) }
        if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) throw java.io.IOException("no thumbnails")
        val answer = ThumbStore.takeAnswer(
            paths, resp.respListOfFiles, knownDevice113 = ThumbStore.omitsThumbnails(), scope = scope,
            reasked = ask.filter { it.reask }.mapTo(HashSet()) { it.path },
        )
        if (answer.dropped) return emptySet<String>() to emptyList()
        val none = HashSet<String>()
        for (a in ask) {
            if (!answer.landed.containsKey(a.path)) continue
            val tile = answer.landed[a.path]
            if (tile == null) { none += a.key; continue }
            val bmp = ThumbStore.load(tile)?.let { b -> withContext(Dispatchers.Default) { decodeBitmap(b, maxSide = 512) } }
            if (bmp != null) thumbCache.put(a.key, bmp.asImageBitmap())
            else { ThumbStore.discard(tile); none += a.key }
        }
        val again = answer.again.toHashSet()
        return none to ask.filter { it.path in again }
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

    /**
     * List, grid or Storage (issue #195), kept. Storage asks for the open
     * folder's sizes and has no selection (the list is for that); leaving
     * it drops what it showed.
     */
    fun setView(view: FilesView) {
        if (view == _state.value.view) return
        prefs.edit { putString(VIEW_MODE_KEY, view.stored) }
        if (view == FilesView.STORAGE) dragSelection = null
        _state.update { it.copy(view = view, usage = null, selected = if (view == FilesView.STORAGE) emptySet() else it.selected) }
        if (view == FilesView.STORAGE) askUsage()
    }

    // Issue #195: only the last GetFolderUsage asked for lands, and only
    // for the folder still shown (FolderUsage.path, usageFits). A device
    // before it answers unknown_payload: not asked again until the next
    // sign-in (OTCConnection.signIns).
    private val usageAsks = java.util.concurrent.atomic.AtomicInteger()
    @Volatile private var usageUnsupportedAt: Int? = null

    /** The Storage view's sizes for the folder shown: asked now (Try again, too). */
    fun askUsage() {
        if (_state.value.view != FilesView.STORAGE) return
        if (usageUnsupportedAt == OTCConnection.signIns.value) { _state.update { it.copy(usage = UsageState.NeedsUpdate) }; return }
        val p = path
        val mine = usageAsks.incrementAndGet()
        _state.update { it.copy(usage = UsageState.Loading) }
        viewModelScope.launch {
            val resp = try {
                OTCConnection.ask("usage", OTCConnection.LIST_TIMEOUT_MS) { it.setReqGetFolderUsage(GetFolderUsage.newBuilder().setPath(p)) }
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) { null }
            if (usageAsks.get() != mine || path != p || _state.value.view != FilesView.STORAGE) return@launch
            val next = if (resp?.payloadCase == RespEnvelope.PayloadCase.RESP_FOLDER_USAGE) {
                // Another folder's (requests overlap): not this one's.
                if (!usageFits(resp.respFolderUsage.path, p)) return@launch
                UsageState.Shown(resp.respFolderUsage)
            } else usageFailure(resp)
            if (next == UsageState.NeedsUpdate) usageUnsupportedAt = OTCConnection.signIns.value
            _state.update { it.copy(usage = next) }
        }
    }

    /**
     * Issue #196: the order by [key] (the same key again: the other way),
     * kept, and the folder and the search's results put in it - a big
     * folder off the main thread, dropped if the listing or the order
     * changes meanwhile. TalkBack hears the new order.
     */
    fun pickSort(key: FilesSortKey) {
        val order = _state.value.sort.picking(key)
        prefs.edit { putString(FilesSortOrder.STORAGE_KEY, order.stored) }
        val locale = sortLocale()
        val said = FilesSortText.sorted(order)
        val mine = sortSeq.incrementAndGet()
        val source = listing
        val now = if (source.size <= FilesSort.OFF_MAIN_ABOVE) FilesSort.sortedRows(source, order, locale) else null
        _state.update { s ->
            s.copy(sort = order, sortSaid = said, rows = now ?: s.rows, sorting = now == null,
                results = s.results?.let { r -> r.copy(files = FilesSort.sortedFiles(r.files, order, locale)) })
        }
        viewModelScope.launch {
            if (now == null) {
                val sorted = withContext(Dispatchers.Default) { FilesSort.sortedRows(source, order, locale) }
                if (sortSeq.get() == mine) _state.update { it.copy(rows = sorted, sorting = false) }
            }
            // Said for a few seconds, so the same words said again are heard again.
            delay(5000)
            _state.update { if (it.sortSaid === said) it.copy(sortSaid = null) else it }
        }
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
     * Sends the queued tiles in batches of 24 full paths, after looking
     * each one up in the phone's thumbnail cache (thumbsFromCache: a small
     * one kept is all a tile needs). Paths the device looked at and left out
     * have no thumbnail (release 113; see askThumbnails); ones it didn't get
     * to are first in line again. Tiles of a folder that was left are
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
                val ask = thumbsFromCache(batch.map { TileAsk(it.first, it.second) })
                if ((ask.size < batch.size || ask.any { it.reask }) && path == folder) _state.update { it.copy(thumbs = thumbCache.snapshot()) }
                // The tiles' small thumbnails (release 111; an older device
                // sends big ones). The viewer shows them only until the full
                // size arrives, and asks for the big one itself where it
                // would stay (PhotoGalleryViewModel.thumbnailStays).
                if (ask.isNotEmpty()) {
                    val (none, again) = askThumbnails(ask)
                    synchronized(thumbLock) {
                        noThumb.addAll(none)
                        for (a in again.asReversed()) thumbQueue.addFirst(Triple(a.key, a.path, folder))
                    }
                    if (path == folder) _state.update { it.copy(thumbs = thumbCache.snapshot()) }
                }
            } catch (_: Exception) {
            } finally {
                // What waits again stays pending.
                synchronized(thumbLock) { thumbsPending.removeAll(batch.map { it.first }.filter { k -> thumbQueue.none { it.first == k } }.toSet()) }
            }
        }
    }

    fun toggleSelect(p: String) = _state.update {
        it.copy(selected = if (p in it.selected) it.selected - p else it.selected + p)
    }

    // Issue #200: the drag to select under way (DragSelect.kt), from a long
    // press. Null once a new listing lands (load(), maybe off the main thread).
    @Volatile private var dragSelection: DragSelection<String>? = null

    /** What a drag passes over, in the order shown: every row but "..", which is never selected. */
    private fun selectable(rows: List<FileRow>): List<String> = rows.mapNotNull { r -> r.path.takeIf { it != ".." } }

    /** A long press on [p]: it is selected, and a drag from it starts. */
    fun beginDragSelect(p: String) {
        val sel = DragSelection(_state.value.selected.toList(), p)
        dragSelection = sel
        selectTo(sel, p)
    }

    // The selection with [sel]'s range at [to]; nothing once a listing that
    // landed meanwhile ended the drag - its selection stands.
    private fun selectTo(sel: DragSelection<String>, to: String) = _state.update { st ->
        if (dragSelection !== sel) st else sel.apply(selectable(st.rows), to)?.let { st.copy(selected = it.toSet()) } ?: st
    }

    /**
     * The drag reached [to]: every row from the pressed one to it is
     * selected, besides what was before. False once the drag is over (the
     * pressed row went away, a new listing): the selection stays as it was then.
     */
    fun dragSelect(to: String): Boolean {
        val sel = dragSelection ?: return false
        selectTo(sel, to)
        return dragSelection === sel && !sel.over
    }

    /** Whether the drag goes on: not once a new listing landed, nor with the pressed row gone. */
    fun dragGoesOn(): Boolean {
        val sel = dragSelection ?: return false
        return sel.goesOn(_state.value.rows.keysView { it.path })
    }

    /** The finger lifted: the selection stays as the drag left it. */
    fun endDragSelect() { dragSelection = null }

    /** Nothing selected any more (the pill's ×, or Back): a drag under way ends with it. */
    fun clearSelection() {
        dragSelection = null
        if (_state.value.selected.isNotEmpty()) _state.update { it.copy(selected = emptySet()) }
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
            showToast(S.appFilesOpenFailed())
        } catch (e: Exception) {
            showToast(S.appFilesDownloadFailed(e.message.toString()))
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

    /**
     * The selection, one after another, then the folder again. The view
     * model's own work, not the screen's: leaving Files used to cut it
     * short, the rest of the selection silently left.
     */
    fun deleteSelected() {
        val rows = _state.value.rows
        val selected = _state.value.selected.toList()
        val folder = path
        viewModelScope.launch {
            for (p in selected) {
                try {
                    val full = if (p.contains("/")) p else joinPath(folder, p)
                    val resp = OTCConnection.request { it.setReqDelFile(filesDelete(full, rows.firstOrNull { r -> r.path == p })) }
                    if (resp.error && resp.errorCode == "upload_only") {
                        showToast(S.appFilesDeleteRefused(leafName(full)))
                        break
                    }
                    // Issue #201: the file holds other content than the row
                    // showed: nothing was deleted - the device says so, and the
                    // folder is listed again below.
                    if (resp.error && resp.errorCode == "changed") {
                        showToast(deviceSays(resp.errorMessage) { S.appPhotosNoticeDeleteFailed(resp.errorCode) })
                        continue
                    }
                    // A file gone from the device: its thumbnail goes from the
                    // phone's cache too (a folder's contents aren't followed).
                    if (!resp.error) rows.firstOrNull { it.path == p && !it.isDir }?.raw?.hash?.takeIf { it.isNotEmpty() }?.let { ThumbStore.discard(it) }
                } catch (e: CancellationException) {
                    throw e
                } catch (_: Exception) {}
            }
            load()
        }
    }

    /** Issue #132: the lock on a folder - flag it upload only, or clear it. */
    suspend fun toggleUploadOnly(row: FileRow) {
        try {
            val resp = OTCConnection.request { it.setReqSetUploadOnly(SetUploadOnly.newBuilder().setPath(fullPath(row)).setUploadOnly(!row.uploadOnly)) }
            if (resp.error) showToast(deviceSays(resp.errorMessage) { S.appFilesFolderUpdateFailed() })
        } catch (e: Exception) { showToast(S.androidFilesFolderUpdateFailedDetail(e.message.toString())) }
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
                showToast(S.androidFilesFolderUpdateFailedDetail(e.message.toString()))
            } finally {
                _state.update { it.copy(preparing = null) }
            }
            load()
        }
    }

    /** Issue #192: [m] for TalkBack (State.outOfImagesSaid), for a few seconds, so the same words said again are heard again. */
    private fun say(m: UiText) {
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
            showToast(S.appFilesVersionOpenFailed())
        } catch (e: Exception) {
            showToast(S.appFilesDownloadFailed(e.message.toString()))
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
            val link = shareLink() ?: run { showToast(S.appFilesDownloadLinkFailed()); return }
            Share.openInBrowser(context, link)
        } finally { _state.update { it.copy(preparing = null) } }
    }

    suspend fun shareSelected(context: Context) {
        _state.update { it.copy(preparing = SelectionActionTask.SHARE) }
        try {
            val link = shareLink() ?: run { showToast(S.appFilesShareLinkFailed()); return }
            Share.link(context, link)
        } finally { _state.update { it.copy(preparing = null) } }
    }

    /**
     * Files picked to upload into the folder shown now (read before they
     * wait their turn: the owner may open another folder meanwhile), two at
     * a time, "Uploading N of M" meanwhile (UploadQueue).
     */
    fun upload(picks: List<Pair<String, () -> InputStream>>) {
        val folder = path
        uploads.add(picks.map { (name, open) -> UploadPick(joinPath(folder, name), name, open) })
    }

    /** The progress line's Cancel: what waits never goes, what goes stops; none of it is reported as failed. */
    fun cancelUploads() = uploads.cancel()

    /**
     * Issue #58: hash first; content the device already has is linked, not re-sent.
     * Issue #165: the file is read twice as a stream (hash, then chunked upload),
     * never loaded whole. A file the phone can't read says so (it was the
     * system's words, or nothing); a cancel stops it without a word.
     */
    private suspend fun uploadTo(pick: UploadPick) {
        try {
            val digest = try {
                withContext(Dispatchers.IO) { ChunkedUpload.digest(pick.open) }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                showToast(UiText.Res(R.string.app_files_upload_failed, listOf(S.appFilesCannotRead(pick.name))))
                return
            }
            val hash = digest.sha256
            val has = OTCConnection.request { it.setReqHasFile(HasFile.newBuilder().setHash(hash)) }
            val exists = has.payloadCase == RespEnvelope.PayloadCase.RESP_FILE_EXISTS && has.respFileExists.exists
            val resp = if (exists) {
                OTCConnection.request { it.setReqLinkFile(LinkFile.newBuilder().setHash(hash).setPath(pick.target).setForceOverride(false)) }
            } else {
                withContext(Dispatchers.IO) { ChunkedUpload.upload(pick.target, digest.size, pick.open, forceOverride = false, sha256 = hash) }
            }
            // The device's words, in the app's language, as the detail.
            if (resp.error) showToast(S.appFilesUploadFailed(resp.errorMessage))
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            showToast(S.appFilesUploadFailed(e.message.toString()))
        }
        load()
    }

    fun showToast(m: UiText) {
        _state.update { it.copy(toast = m) }
        // How long it stays goes by its length in the language shown now.
        val length = m.now().length
        kotlinx.coroutines.GlobalScope.launch {
            // Long enough to read: a refusal naming two folders (issue
            // #186's and #192's) runs to a hundred characters or more.
            delay((2500L + 50L * (length - 50).coerceAtLeast(0)).coerceAtMost(8000L))
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
    var lockInfo by remember { mutableStateOf<UiText?>(null) }
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
    // Wide (the side rail or menu beside the page, from MENU_RAIL_MIN_DP),
    // the list has the web's columns: headings that sort, Size, Created and
    // Modified, widths given on this phone (FilesColumns.kt). Narrow, its
    // rows stay a name over its size.
    val columnsOn = !narrow && st.view == FilesView.LIST
    val columns = remember { FilesColumnsState(OTCApp.instance.getSharedPreferences("otc_settings", Context.MODE_PRIVATE)) }
    val cellDates = rememberCellDates()
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
            PhotoGalleryViewModel.Item("$full#${r.raw.hash}#${r.size}", full, r.raw.mime, r.size, null, st.thumbs[vm.thumbKey(r)]?.asAndroidBitmap(), hash = r.raw.hash)
        }
        viewer.showFiles(items, media.indexOf(row)) { vm.launchLoad() }
    }

    // Kept here, not in the lists: the folder keeps its place while search
    // results show over it.
    val listState = rememberLazyListState()
    val gridState = rememberLazyGridState()
    val storageState = rememberLazyListState()

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
                storageState.requestScrollToItem(0)
                if (f != null && !isMediaFile(f)) scope.launch { vm.open(context, foundRow(f)) }
            }
        }
    }
    // On every listing (thumbGen): the folder listed again, with the same
    // rows, when the search picks a photo in the folder already open. A big
    // folder's rows once they are in order (issue #196).
    LaunchedEffect(st.listedPath, st.thumbGen, st.sorting) {
        val (dir, file) = vm.mediaToOpen ?: return@LaunchedEffect
        if (st.listedPath != dir || st.sorting) return@LaunchedEffect
        vm.mediaToOpen = null
        val media = st.rows.filter { isMedia(it) }
        val row = media.firstOrNull { vm.fullPath(it) == file.path }
        // Not in the listing (the file went): the file alone.
        if (row != null) openRow(row)
        else viewer.showFiles(listOf(PhotoGalleryViewModel.Item("${file.path}#${file.hash}#${file.byteSize}", file.path, file.mime, file.byteSize, null, hash = file.hash)), 0) { vm.launchLoad() }
    }
    // The system back leaves the results for the folder; over the folder
    // it clears the selection, as the pill's × does (filesBack). Here,
    // ahead of the search field's, so an open search's own Back comes first.
    val back = filesBack(st.selected.isNotEmpty(), st.results != null, searchOpen = if (showSearch) fileSearch.open else topSearch.open)
    BackHandler(enabled = back != null) {
        when (back) {
            FilesBack.CLOSE_RESULTS -> vm.closeResults()
            FilesBack.CLEAR_SELECTION -> vm.clearSelection()
            null -> {}
        }
    }

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
                PhotoGalleryViewModel.Item("${x.path}#${x.hash}#${x.byteSize}", x.path, x.mime, x.byteSize, null, st.thumbs[vm.resultThumbKey(x)]?.asAndroidBitmap(), hash = x.hash)
            }
            viewer.showFiles(items, maxOf(0, media.indexOf(f))) { vm.startSearch(results.text) }
            return
        }
        if (st.openingPath == null) scope.launch { vm.open(context, foundRow(f)) }
    }

    val importer = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris: List<Uri> ->
        if (uris.isEmpty()) return@rememberLauncherForActivityResult
        val resolver = context.applicationContext.contentResolver
        scope.launch {
            val picks = withContext(Dispatchers.IO) {
                uris.map { uri ->
                    val name = queryDisplayName(context, uri) ?: "file"
                    name to { resolver.openInputStream(uri) ?: throw TextIOException(S.appFilesCannotRead(name)) }
                }
            }
            // The view model's from here on: leaving Files doesn't stop them.
            vm.upload(picks)
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
                    results, back = if (st.path == "/") S.appFilesBackToFiles() else S.appFilesBackToFolder(leafName(st.path)), thumbs = st.thumbs, openingPath = st.openingPath,
                    thumbKey = vm::resultThumbKey, onBack = { vm.closeResults() }, onRetry = { vm.startSearch(results.text) }, onOpen = ::openFound,
                ) else BoxWithConstraints(Modifier.fillMaxSize()) {
                    // The list's room, for its columns on a wide screen.
                    val room = maxWidth.value.toInt()
                    Column(Modifier.fillMaxSize()) {
                        Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                            OTCTextField(
                                value = pathField, onValueChange = { pathField = it }, singleLine = true, label = { Text("/path/") },
                                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, imeAction = ImeAction.Go),
                                keyboardActions = KeyboardActions(onGo = { vm.navigate(pathField) }),
                                modifier = Modifier.weight(1f),
                            )
                            if (st.loading || st.sorting) { Spacer(Modifier.width(8.dp)); CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp) }
                            Box(Modifier.tourTarget(TourTarget.VIEW_OPTIONS)) {
                                ViewOptionsMenu(view = st.view, sort = st.sort, onView = vm::setView, onSort = vm::pickSort)
                            }
                        }
                        // Said to TalkBack when it appears or changes.
                        st.error?.let {
                            Text(
                                it.resolve(), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall,
                                modifier = Modifier.padding(horizontal = 12.dp).semantics { liveRegion = LiveRegionMode.Polite },
                            )
                        }
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

                        // Issue #200: a long press on a file or folder selects it, and
                        // a drag from there every one to the one under the finger,
                        // in the order shown ("..", never selected, is skipped). Once
                        // something is selected a tap picks or drops one instead of
                        // opening it ("..", still goes up). TalkBack: the circles.
                        val selecting = st.selected.isNotEmpty()
                        fun tap(row: FileRow) { if (selecting && row.path != "..") vm.toggleSelect(row.path) else openRow(row) }
                        fun selectable(key: Any?): String? = (key as? String)?.takeIf { it != ".." }
                        // Wide, the columns' headings over the list (FilesColumnsView.kt):
                        // a tap sorts, a handle resizes; the rows follow their layout.
                        val colMetrics = if (columnsOn) rememberFilesColumnMetrics(cellDates) else null
                        val colLayout = colMetrics?.let { columns.layout(room, it) }
                        if (colLayout != null && colMetrics != null) FilesColumnHeadings(colLayout, colMetrics, st.sort, columns, onSort = vm::pickSort)
                        PullToRefreshBox(
                            isRefreshing = refreshing,
                            onRefresh = { scope.launch { refreshing = true; vm.load(); refreshing = false } },
                            modifier = Modifier.weight(1f),
                        ) {
                            if (st.view == FilesView.STORAGE) {
                                // Issue #195: what takes the space here. A folder
                                // opens in this view; a file as a tap in the list.
                                FilesStorageView(
                                    st.usage, st.rows, st.path, st.listedPath, st.openingPath, storageState,
                                    onUp = if (st.path == "/") null else { { vm.navigate(dirnamePath(st.path)) } },
                                    onOpen = { r ->
                                        val row = r.listed
                                        if (r.isDir) vm.navigate(normPath(r.path))
                                        else if (row != null) openRow(row)
                                        else openRow(FileRow(r.path, r.name, false, r.size, PbFile.newBuilder().setPath(r.path).build()))
                                    },
                                    onRetry = vm::askUsage,
                                )
                            } else if (st.grid) {
                                val left = with(LocalDensity.current) { FILES_GRID_SIDE.toPx() }
                                val drag = rememberDragSelect(
                                    gridState, hit = { p -> selectable(gridState.layoutInfo.keyAt(p.x, p.y, left)) },
                                    onBegin = vm::beginDragSelect, onMove = vm::dragSelect, onEnd = vm::endDragSelect, goesOn = vm::dragGoesOn,
                                )
                                LazyVerticalGrid(
                                    GridCells.Adaptive(104.dp), Modifier.fillMaxSize().dragSelect(drag), state = gridState,
                                    contentPadding = PaddingValues(horizontal = FILES_GRID_SIDE, vertical = 6.dp),
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
                                            selecting = selecting,
                                            onOpen = { tap(row) },
                                            onToggle = { vm.toggleSelect(row.path) },
                                            onVersions = { scope.launch { vm.openVersions(row) } },
                                            onLock = { lockPrompt = row },
                                            outOfImages = st.outOfImagesSupported && row.outOfImages,
                                            onOutOfImages = { explainOutOfImages(row) },
                                        )
                                    }
                                }
                            } else {
                                val drag = rememberDragSelect(
                                    listState, hit = { p -> selectable(listState.layoutInfo.keyAt(p.y)) },
                                    onBegin = vm::beginDragSelect, onMove = vm::dragSelect, onEnd = vm::endDragSelect, goesOn = vm::dragGoesOn,
                                )
                                LazyColumn(Modifier.fillMaxSize().dragSelect(drag), state = listState) {
                                    items(st.rows, key = { it.path }) { row ->
                                        val selected = row.path in st.selected
                                        val picks = selecting && row.path != ".."
                                        Row(
                                            Modifier.fillMaxWidth()
                                                .clickable(
                                                    enabled = picks || st.openingPath == null,
                                                    onClickLabel = if (picks) (if (selected) S.commonDeselect() else S.commonSelect()).resolve() else null,
                                                ) { tap(row) }
                                                .padding(horizontal = 8.dp, vertical = 6.dp),
                                            verticalAlignment = Alignment.CenterVertically,
                                        ) {
                                            if (row.path != "..") {
                                                IconButton(onClick = { vm.toggleSelect(row.path) }, modifier = Modifier.size(36.dp)) {
                                                    Icon(if (selected) Icons.Default.CheckCircle else Icons.Outlined.Circle, (if (selected) S.commonDeselect() else S.commonSelect()).resolve(),
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
                                            // Wide, the name alone - a long one gives way to the icons
                                            // after it, down to 10 characters (FilesColumns) - and its
                                            // size and dates in their columns.
                                            if (colLayout == null) Column(Modifier.weight(1f)) {
                                                Text(row.name, maxLines = 1)
                                                if (!row.isDir) Text(formatBytes(row.size), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                            } else Text(row.name, Modifier.weight(1f), maxLines = 1, overflow = TextOverflow.Ellipsis)
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
                                                    Icon(Icons.Outlined.HideImage, OutOfImagesText.STATE.resolve(), tint = MaterialTheme.colorScheme.primary)
                                                }
                                            }
                                            if (row.path != ".." && row.isDir) {
                                                // The tour's step about uploading: the first folder's lock.
                                                val firstFolder = row.path == st.rows.firstOrNull { it.path != ".." && it.isDir }?.path
                                                IconButton(onClick = { lockPrompt = row }, modifier = Modifier.size(36.dp).tourTarget(if (firstFolder) TourTarget.FOLDER_LOCK else null)) {
                                                    Icon(if (row.uploadOnly) Icons.Default.Lock else Icons.Outlined.LockOpen,
                                                        (if (row.uploadOnly) S.appFilesClearUploadOnly() else S.appFilesMakeUploadOnly()).resolve(),
                                                        tint = if (row.uploadOnly) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant)
                                                }
                                            } else if (row.uploadOnly) {
                                                Box(Modifier.size(36.dp).clip(CircleShape).clickable { lockInfo = UploadOnlyText.FILE_INFO }, contentAlignment = Alignment.Center) {
                                                    Icon(Icons.Default.Lock, S.appFilesInUploadOnly().resolve(), tint = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.size(18.dp))
                                                }
                                            }
                                            if (colLayout != null) FilesColumnCells(row, colLayout, cellDates)
                                        }
                                    }
                                }
                            }
                        }

                        // The files picked, while they go: how far along, and Cancel.
                        st.uploads?.let { u -> UploadProgressRow(u) { vm.cancelUploads() } }
                        SelectionActionBar(
                            count = st.selected.size,
                            busy = st.preparing,
                            onShare = { scope.launch { vm.shareSelected(context) } },
                            onDownload = { scope.launch { vm.downloadSelected(context) } },
                            onDelete = {
                                if (vm.selectionUploadOnly) vm.showToast(S.appFilesSelectionUploadOnly())
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
                            // The pill goes; the bar stays, for Upload.
                            onClear = vm::clearSelection,
                        )
                        Spacer(Modifier.size(8.dp))
                    }
                }
                // Its suggestions, over everything under the field.
                if (showSearch) FilesSearchPanel(fileSearch, fileOptions)
            }
        }
        Toast(st.toast?.resolve(), Modifier.align(Alignment.TopCenter).padding(horizontal = 16.dp))
        // Issue #192: a change for Images, once done, said to TalkBack (the
        // web's status line). Nothing to see: the mark and the banner show it.
        // Issue #196: a new order too.
        val said = (st.outOfImagesSaid ?: st.sortSaid)?.resolve() ?: ""
        Box(Modifier.align(Alignment.BottomStart).size(1.dp).semantics {
            liveRegion = LiveRegionMode.Polite
            contentDescription = said
        })
    }

    // Issue #132: the versions pop-up - the current file and every older
    // version with when it was replaced and its size; a tap opens that
    // version the way a file opens.
    st.versionsOf?.let { (row, versions) ->
        AlertDialog(
            onDismissRequest = { vm.closeVersions() },
            title = { Text(S.appFilesVersionsTitle(row.name).resolve()) },
            text = {
                Column {
                    Text(S.appFilesVersionsExplain().resolve(),
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Spacer(Modifier.size(8.dp))
                    Row(Modifier.fillMaxWidth().clickable { scope.launch { vm.openVersion(context, row, "") } }.padding(vertical = 8.dp)) {
                        Text(S.appFilesVersionCurrent().resolve(), fontWeight = FontWeight.SemiBold, modifier = Modifier.weight(1f))
                        Text(formatBytes(row.size), color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    if (st.versionsLoading) CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                    versions.forEach { v ->
                        Row(Modifier.fillMaxWidth().clickable { scope.launch { vm.openVersion(context, row, v.hash) } }.padding(vertical = 8.dp)) {
                            Text(S.appFilesVersionReplaced(if (v.hasModified()) DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(v.modified.seconds * 1000)) else "—").resolve(),
                                modifier = Modifier.weight(1f))
                            Text(formatBytes(v.byteSize), color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                }
            },
            confirmButton = { TextButton(onClick = { vm.closeVersions() }) { Text(S.commonDone().resolve()) } },
        )
    }
    gallerySource?.let { src -> SharedGalleryShareFlow(src, onDismiss = { gallerySource = null }) }
    lockPrompt?.let { row ->
        AlertDialog(
            onDismissRequest = { lockPrompt = null },
            title = { Text(UploadOnlyText.promptTitle(row.name, row.uploadOnly).resolve()) },
            text = { Text((if (row.uploadOnly) UploadOnlyText.CLEAR_MESSAGE else UploadOnlyText.MAKE_MESSAGE).resolve()) },
            confirmButton = {
                TextButton(onClick = { lockPrompt = null; scope.launch { vm.toggleUploadOnly(row) } }) {
                    Text((if (row.uploadOnly) S.androidFilesAllowDeletions() else S.appFilesMakeUploadOnly()).resolve())
                }
            },
            dismissButton = { TextButton(onClick = { lockPrompt = null }) { Text(S.commonCancel().resolve()) } },
        )
    }
    lockInfo?.let {
        AlertDialog(onDismissRequest = { lockInfo = null }, title = { Text(S.appFilesUploadOnly().resolve()) }, text = { Text(it.resolve()) },
            confirmButton = { TextButton(onClick = { lockInfo = null }) { Text(S.commonOk().resolve()) } })
    }
    outOfImagesAsk?.let { a ->
        AlertDialog(
            onDismissRequest = { outOfImagesAsk = null },
            title = { Text((if (a.out) OutOfImagesText.showTitle(a.name) else OutOfImagesText.keepTitle(a.name)).resolve()) },
            text = { Text((if (a.out) OutOfImagesText.SHOW_MESSAGE else OutOfImagesText.KEEP_MESSAGE).resolve()) },
            confirmButton = {
                // Not while a share or download link is being made: it
                // would wait for that, so the button does.
                TextButton(onClick = { outOfImagesAsk = null; vm.toggleOutOfImages(a.full, a.out) }, enabled = st.preparing == null) {
                    Text((if (a.out) OutOfImagesText.SHOW else OutOfImagesText.KEEP).resolve())
                }
            },
            dismissButton = { TextButton(onClick = { outOfImagesAsk = null }) { Text(S.commonCancel().resolve()) } },
        )
    }
    outOfImagesInfo?.let { info ->
        AlertDialog(
            onDismissRequest = { outOfImagesInfo = null },
            title = { Text(OutOfImagesText.STATE.resolve()) },
            text = { Text((info.by?.let { OutOfImagesText.byParentInfo(it) } ?: OutOfImagesText.EXPLAIN).resolve()) },
            confirmButton = { TextButton(onClick = { outOfImagesInfo = null }) { Text(S.commonOk().resolve()) } },
            dismissButton = if (info.by != null) null else {
                { TextButton(onClick = { outOfImagesInfo = null; askOutOfImages(info.row) }, enabled = st.preparing == null) { Text(OutOfImagesText.SHOW.resolve()) } }
            },
        )
    }
    if (st.confirmDeleteSelected) {
        val n = st.selected.size
        AlertDialog(
            onDismissRequest = { vm.setConfirmDelete(false) },
            title = { Text(S.appFilesDeleteConfirm(n).resolve()) },
            confirmButton = { TextButton(onClick = { vm.setConfirmDelete(false); vm.deleteSelected() }) { Text(S.commonDelete().resolve(), color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { vm.setConfirmDelete(false) }) { Text(S.commonCancel().resolve()) } },
        )
    }
    if (vst.openIndex != null) ImageModal(viewer, vst)
    vst.alert?.let {
        AlertDialog(onDismissRequest = { viewer.dismissAlert() }, text = { Text(it.resolve()) }, confirmButton = { TextButton(onClick = { viewer.dismissAlert() }) { Text(S.commonOk().resolve()) } })
    }
}

/** "Uploading 2 of 5…" with a spinner, and Cancel - over the bar while picked files go. */
@Composable
private fun UploadProgressRow(progress: UploadProgress, onCancel: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 2.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp)
        Spacer(Modifier.width(10.dp))
        Text(
            S.appFilesUploading(progress.done, progress.total).resolve(), style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f).semantics { liveRegion = LiveRegionMode.Polite },
        )
        TextButton(onClick = onCancel) { Text(S.commonCancel().resolve()) }
    }
}

/**
 * List, grid or Storage (issue #195), and the order (issue #196), in one
 * menu, as iOS's View options: the button shows the view in use. The view
 * in use and the order in use have the check; the order its arrow and,
 * under its name, its direction in words. Picking that order again
 * reverses it. Storage keeps the device's order (largest first): no order
 * to pick there.
 */
@Composable
private fun ViewOptionsMenu(view: FilesView, sort: FilesSortOrder, onView: (FilesView) -> Unit, onSort: (FilesSortKey) -> Unit) {
    var open by remember { mutableStateOf(false) }
    val colors = MaterialTheme.colorScheme
    val sorted = FilesSortText.sorted(sort).resolve()
    fun iconOf(v: FilesView) = when (v) {
        FilesView.LIST -> Icons.AutoMirrored.Filled.ViewList
        FilesView.GRID -> Icons.Default.GridView
        FilesView.STORAGE -> Icons.Default.PieChart
    }
    Box {
        IconButton(onClick = { open = true }, modifier = Modifier.semantics { if (view != FilesView.STORAGE) stateDescription = sorted }) {
            Icon(iconOf(view), S.appFilesViewOptions().resolve())
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            @Composable
            fun check(on: Boolean) = if (on) Icon(Icons.Default.Check, null) else Spacer(Modifier.size(24.dp))
            for (v in FilesView.entries) {
                val name = when (v) {
                    FilesView.LIST -> S.appFilesViewList()
                    FilesView.GRID -> S.appFilesViewGrid()
                    FilesView.STORAGE -> S.appFilesViewStorage()
                }
                DropdownMenuItem(
                    text = { Text(name.resolve()) }, onClick = { open = false; onView(v) },
                    leadingIcon = { check(view == v) }, trailingIcon = { Icon(iconOf(v), null) },
                    modifier = Modifier.semantics { selected = view == v },
                )
            }
            if (view == FilesView.STORAGE) return@DropdownMenu
            HorizontalDivider()
            Text(
                S.appFilesSortBy().resolve(), style = MaterialTheme.typography.labelMedium, color = colors.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 12.dp, vertical = 8.dp).semantics { heading() },
            )
            for (key in FilesSortKey.entries) {
                val on = sort.key == key
                DropdownMenuItem(
                    text = {
                        Column {
                            Text(FilesSortText.name(key).resolve())
                            if (on) Text(FilesSortText.direction(sort).resolve(), style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant)
                        }
                    },
                    onClick = { open = false; onSort(key) },
                    leadingIcon = { check(on) },
                    trailingIcon = if (on) {
                        { Icon(if (sort.ascending) Icons.Default.KeyboardArrowUp else Icons.Default.KeyboardArrowDown, null) }
                    } else null,
                    modifier = Modifier.semantics { selected = on },
                )
            }
        }
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
    // Something is selected: a tap ([onOpen]) picks or drops this one (issue #200).
    selecting: Boolean = false,
) {
    val picks = selecting && row.path != ".."
    Column(Modifier.fillMaxWidth()) {
        Box(
            Modifier.fillMaxWidth().aspectRatio(1f).clip(RoundedCornerShape(10.dp))
                .background(MaterialTheme.colorScheme.surfaceContainerLow)
                .clickable(onClickLabel = if (picks) (if (selected) S.commonDeselect() else S.commonSelect()).resolve() else null, onClick = onOpen),
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
                    Icon(Icons.Default.PlayArrow, S.appFilesVideo().resolve(), tint = Color.White, modifier = Modifier.size(16.dp))
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
            // No long-press menu here (a long press selects, issue #200): a
            // tap on the lock offers to clear it.
            if (row.isDir && row.uploadOnly) {
                Box(Modifier.align(Alignment.TopEnd).padding(6.dp).size(24.dp).clip(CircleShape)
                    .background(MaterialTheme.colorScheme.surface.copy(alpha = 0.85f)).clickable(onClick = onLock),
                    contentAlignment = Alignment.Center) {
                    Icon(Icons.Default.Lock, S.appFilesUploadOnly().resolve(), tint = MaterialTheme.colorScheme.primary, modifier = Modifier.size(14.dp))
                }
            }
            // Issue #192: left of the lock, which keeps its corner.
            if (row.isDir && outOfImages) {
                Box(Modifier.align(Alignment.TopEnd).padding(top = 6.dp, end = if (row.uploadOnly) 34.dp else 6.dp).size(24.dp).clip(CircleShape)
                    .background(MaterialTheme.colorScheme.surface.copy(alpha = 0.85f)).clickable(onClick = onOutOfImages),
                    contentAlignment = Alignment.Center) {
                    Icon(Icons.Outlined.HideImage, OutOfImagesText.STATE.resolve(), tint = MaterialTheme.colorScheme.primary, modifier = Modifier.size(14.dp))
                }
            }
            if (row.path != "..") {
                Box(Modifier.align(Alignment.TopStart).padding(4.dp).size(28.dp).clip(CircleShape)
                    .background(MaterialTheme.colorScheme.surface.copy(alpha = 0.7f)).clickable(onClick = onToggle),
                    contentAlignment = Alignment.Center) {
                    Icon(if (selected) Icons.Default.CheckCircle else Icons.Outlined.Circle, (if (selected) S.commonDeselect() else S.commonSelect()).resolve(),
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
    results: SearchResults, back: UiText, thumbs: Map<String, ImageBitmap>, openingPath: String?,
    thumbKey: (PbFile) -> String, onBack: () -> Unit, onRetry: () -> Unit, onOpen: (PbFile) -> Unit,
) {
    val colors = MaterialTheme.colorScheme
    val q = remember(results.text) { FilesNav.fold(results.text).text }
    val found = remember(results) { results.files.map { it to FilesNav.foundParts(it.path, q) } }
    Column(Modifier.fillMaxSize()) {
        TextButton(onClick = onBack, modifier = Modifier.padding(start = 4.dp, top = 4.dp)) {
            Icon(Icons.AutoMirrored.Filled.ArrowBack, null, Modifier.size(18.dp))
            Spacer(Modifier.width(8.dp))
            Text(back.resolve(), maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        Text(S.appFilesResultsTitle(results.text).resolve(), style = MaterialTheme.typography.titleMedium, maxLines = 2, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(horizontal = 16.dp))
        // Its line kept while searching, so the list doesn't move.
        Text(
            if (results.phase == SearchResults.Phase.DONE && found.isNotEmpty())
                (if (found.size == SEARCH_LIMIT) S.appFilesResultsLimited(SEARCH_LIMIT) else S.appFilesResultsCount(found.size)).resolve()
            else "",
            style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant, modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
        )
        when (results.phase) {
            SearchResults.Phase.LOADING -> Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
                CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                Spacer(Modifier.width(10.dp))
                Text(S.appFilesSearching().resolve(), color = colors.onSurfaceVariant)
            }
            SearchResults.Phase.ERROR -> Column(Modifier.padding(16.dp)) {
                Text(results.error?.resolve() ?: "", color = colors.error)
                if (results.retry) TextButton(onClick = onRetry) { Text(S.commonTryAgain().resolve()) }
            }
            SearchResults.Phase.DONE -> if (found.isEmpty()) {
                Text(S.appFilesNoResults(results.text).resolve(), color = colors.onSurfaceVariant, modifier = Modifier.padding(16.dp))
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
                                contentAlignment = Alignment.Center) { Icon(Icons.Default.PlayArrow, S.appFilesVideo().resolve(), tint = Color.White, modifier = Modifier.size(11.dp)) }
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
                                Icon(Icons.Outlined.HideImage, OutOfImagesText.STATE.resolve(), Modifier.size(16.dp), tint = colors.primary)
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
 *  UploadOnlyText and the web lock's tooltip, from the catalog (app.files). */
private object UploadOnlyText {
    fun promptTitle(name: String, uploadOnly: Boolean) =
        if (uploadOnly) S.appFilesAllowDeletionsTitle(name) else S.appFilesMakeUploadOnlyTitle(name)
    val MAKE_MESSAGE get() = S.appFilesMakeUploadOnlyMessage()
    val CLEAR_MESSAGE get() = S.appFilesAllowDeletionsMessage()
    val FILE_INFO get() = S.appFilesUploadOnlyFileInfo()
}

/** Issue #192: the folder a confirmation is for - its whole path, its name, and whether it is kept out of Images now. */
private data class OutOfImagesAsk(val full: String, val name: String, val out: Boolean)

/** Issue #192: a folder's mark, tapped - [by]: the folder keeping it out when it is inside one kept out (no Show of its own then). */
private data class OutOfImagesInfo(val row: FileRow, val by: String?)

/** What keeping a folder out of Images says (issue #192). The same words as
 *  iOS's OutOfImagesText (in its Title Case for menus) and the web's, from
 *  the catalog: UiText, worded when shown. */
internal object OutOfImagesText {
    val KEEP get() = S.appShellKeepOutOfImages()
    val SHOW get() = S.appShellShowInImages()
    val STATE get() = S.appFilesKeptOut()
    val EXPLAIN get() = S.appFilesKeptOutExplain()
    fun keepTitle(name: String) = S.appFilesKeepOutTitle(name)
    val KEEP_MESSAGE get() = S.appFilesKeepOutMessage()
    fun showTitle(name: String) = S.appFilesShowTitle(name)
    val SHOW_MESSAGE get() = S.appFilesShowMessage()
    val BANNER get() = S.appFilesKeptOutBanner()
    // Inside a folder kept out because one above it is (the web's words):
    // the banner names that one, and its button shows it.
    fun bannerByParent(parent: String) = S.androidFilesInsideKeptOutBanner(parent)
    fun showNamed(name: String) = S.androidFilesShowNamed(name)
    fun byParent(parent: String) = S.androidFilesInsideKeptOut(parent)
    // byParent and EXPLAIN as one text: two sentences, never joined here.
    fun byParentInfo(parent: String) = S.androidFilesInsideKeptOutInfo(parent)
    // Said to TalkBack once a change is done.
    fun keptSaid(name: String) = S.androidFilesKeptOutSaid(name)
    fun shownSaid(name: String) = S.androidFilesShownSaid(name)
}

/** Issue #192: over a folder kept out of Images - what that means, and the way to show it again ([show]: its label; none yet when null). */
@Composable
private fun OutOfImagesBanner(text: UiText, show: UiText?, busy: Boolean, enabled: Boolean, onShow: () -> Unit) {
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
            Text(text.resolve(), style = MaterialTheme.typography.bodySmall, modifier = Modifier.weight(1f))
        }
        if (show != null) TextButton(onClick = onShow, enabled = enabled, modifier = Modifier.align(Alignment.End)) {
            if (busy) { CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp); Spacer(Modifier.width(6.dp)) }
            Text(show.resolve(), maxLines = 2, overflow = TextOverflow.Ellipsis, textAlign = TextAlign.End)
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

/**
 * What Files says while the folder [p] is listed: for the folder whose
 * listing failed ([failedPath], [problem]), why - it stays up while it is
 * asked again rather than flickering away at each try - and the neutral
 * line once the phone is back [online]; nothing for another folder.
 */
internal fun listingErrorWhileAsking(problem: LoadProblem?, failedPath: String?, p: String, online: Boolean): UiText? =
    problem?.takeIf { failedPath == p }?.backOnline(online)?.let { loadProblemText(it, LoadSubject.FILES) }
