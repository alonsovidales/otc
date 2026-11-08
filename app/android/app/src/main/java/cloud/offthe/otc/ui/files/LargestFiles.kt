// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import android.content.Context
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appFilesDownloadFailed
import cloud.offthe.otc.i18n.appFilesLargestFailed
import cloud.offthe.otc.i18n.appFilesLargestPlaces
import cloud.offthe.otc.i18n.appFilesOpenFailed
import cloud.offthe.otc.net.ChunkedDownload
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.byteSize
import cloud.offthe.otc.net.retryWaitMs
import cloud.offthe.otc.net.sleepOrWake
import cloud.offthe.otc.proto.CompressItem
import cloud.offthe.otc.proto.CompressJob
import cloud.offthe.otc.proto.CompressResult
import cloud.offthe.otc.proto.ListLargestFiles
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.DEVICE_UNREACHABLE
import cloud.offthe.otc.ui.common.GridThumbFetcher
import cloud.offthe.otc.ui.common.GridThumbStore
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.TILES_KIND
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.land
import cloud.offthe.otc.ui.gallery.GalleryRequest
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import cloud.offthe.otc.ui.gallery.isUnknownPayload
import cloud.offthe.otc.ui.settings.deviceOr
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import cloud.offthe.otc.proto.File as PbFile

// Settings › Largest files (owner, 2026-11-08: "so we can identify what is
// taking more space"): every file on the device, largest first - one row
// per content, at its first path, the same photo at three paths "in 3
// places" - read with ListLargestFiles in pages of 50 as the list
// scrolls, a page that fails asked again 1 s doubling to 10 s (at once on
// a Wake or Try again), as Images' pages are. Photos and videos show their
// small thumbnails (GetThumbnails by path, as Files' grid) and open in the
// photo viewer, paging through the list's photos and videos; anything else
// opens as Files opens it. Long press or the circle picks rows, and the
// selection bar's Compress opens the Compress dialog (Compress.kt). The
// screen is LargestFilesView.kt; the rules here are pure, for the JVM
// tests (LargestFilesTest). iOS and the web follow the same ones.

/** L4: what the list's place shows - the first page's wait, its failure, an older device, nothing at all, or the rows. */
enum class LargestBody { LOADING, FAILED, NEEDS_UPDATE, EMPTY, ROWS }

/** L4: what the rows have at their foot - the next page on its way, why it didn't come (Try again), or nothing. */
enum class LargestFoot { LOADING, FAILED, NONE }

object LargestRules {
    /** A page's rows. */
    const val PAGE = 50

    /** The most rows ListLargestFiles gives in one page: what the list is read again in. */
    const val MOST = 200

    /** How many rows the list reads again at most after a change (the rest come again as it scrolls). */
    const val REREAD_MOST = 1000

    /** L1: the page after [after] ("" for the first; else the last page's token), of [limit] rows. */
    fun request(after: String, limit: Int = PAGE): ListLargestFiles = ListLargestFiles.newBuilder().setAfter(after).setLimit(limit).build()

    /** What a ListLargestFiles brought. */
    sealed interface Answer {
        /** A page: its rows, and the next page's token ("" once the list is over). */
        data class Page(val files: List<PbFile>, val token: String) : Answer
        /** A device before Largest files (unknown_payload): never asked again on this screen. */
        data object NeedsUpdate : Answer
        /** No answer, the bridge's "device unreachable", or the device's own refusal ([text]): asked again. */
        data class Failed(val text: UiText) : Answer
    }

    /** L2: what a ListLargestFiles answer ([resp]; null: none came) means - a failure in the device's words when it said why. */
    fun answer(resp: RespEnvelope?): Answer = when {
        resp == null -> Answer.Failed(S.appFilesLargestFailed())
        resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES -> Answer.Page(resp.respListOfFiles.filesList, resp.respListOfFiles.token)
        resp.isUnknownPayload() -> Answer.NeedsUpdate
        resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.code == DEVICE_UNREACHABLE -> Answer.Failed(S.appFilesLargestFailed())
        resp.error -> Answer.Failed(deviceOr(resp.errorMessage, S.appFilesLargestFailed()))
        else -> Answer.Failed(S.appFilesLargestFailed())
    }

    /**
     * L3: [rows] with what the next [page] adds after them, one row per
     * content. Each page is read afresh (cut by size and content, not by
     * place): a file added or deleted between pages can bring one shown
     * already - or one compressed since, at its new size - and that one
     * stays where it is shown. Folders never come.
     */
    fun merged(rows: List<PbFile>, page: List<PbFile>): List<PbFile> {
        if (page.isEmpty()) return rows
        val paths = rows.mapTo(HashSet()) { it.path }
        val hashes = rows.mapNotNullTo(HashSet()) { it.hash.ifEmpty { null } }
        return rows + page.filter { f ->
            f.mime != "inode/directory" && paths.add(f.path) && (f.hash.isEmpty() || hashes.add(f.hash))
        }
    }

    /** L4: what the rows' foot shows: why the last page failed, the next one on its way, or nothing. */
    fun foot(loading: Boolean, problem: UiText?): LargestFoot = when {
        problem != null -> LargestFoot.FAILED
        loading -> LargestFoot.LOADING
        else -> LargestFoot.NONE
    }

    /** L5: the next page is asked for only while there is one, nothing is on its way and none is failing (asked again by itself). */
    fun wantsMore(loaded: Boolean, end: Boolean, loading: Boolean, problem: UiText?, needsUpdate: Boolean): Boolean =
        loaded && !end && !loading && problem == null && !needsUpdate

    /** L6: "in 3 places" under a content the device holds at several paths; null at one (or uncounted). */
    fun places(f: PbFile): UiText? = if (f.hasCopies() && f.copies > 1) S.appFilesLargestPlaces(f.copies) else null

    /**
     * L7: whether [f] is a photo or a video - a thumbnail, the viewer,
     * Compress - by its type, or a HEIC or HEIF by its name whatever its
     * type.
     */
    fun isMedia(f: PbFile): Boolean {
        if (f.mime == "inode/directory") return false
        val name = f.path.lowercase()
        return f.mime.startsWith("image/") || f.mime.startsWith("video/") || name.endsWith(".heic") || name.endsWith(".heif")
    }

    // A picked row Compress takes: a photo or video.
    private fun compressible(f: PbFile, selected: Set<String>) = f.path in selected && isMedia(f)

    /** L8: Compress is on while a photo or video is among the [selected] rows. */
    fun canCompress(rows: List<PbFile>, selected: Set<String>): Boolean = rows.any { compressible(it, selected) }

    /** What Compress asks about: the picked photos and videos in the list's order, with the content each showed. */
    fun compressItems(rows: List<PbFile>, selected: Set<String>): List<CompressItem> =
        rows.filter { compressible(it, selected) }.map { CompressItem.newBuilder().setPath(it.path).setHash(it.hash).build() }

    /**
     * J6: [rows] once the device compressed [results]: each one's file in
     * place of the row at its path (a new content and size, maybe a new
     * name: a PNG becomes .jpg), as many places as it had
     * (CompressRules.fileAfter). The list is read again for its order once
     * the job is over (L9).
     */
    fun compressed(rows: List<PbFile>, results: List<CompressResult>): List<PbFile> {
        val by = results.filter { it.state == CompressRules.DONE && it.hasFile() }.associateBy { it.path }
        if (by.isEmpty()) return rows
        return rows.map { row -> by[row.path]?.let { CompressRules.fileAfter(row, it) } ?: row }
    }

    /** L9: the list is read again after a job that compressed anything. */
    fun rereadsAfter(job: CompressJob): Boolean = CompressRules.done(job).isNotEmpty()

    /** L10: the folder a row is in, as its line under the name says it: "/Camera/2024", "/" for the top. */
    fun folder(path: String): String = path.trimEnd('/').substringBeforeLast('/', "").ifEmpty { "/" }

    /** A row as the photo viewer takes it ([thumbKey]: its tile until the full size comes). */
    fun viewerItem(f: PbFile, thumbKey: String?): PhotoGalleryViewModel.Item = PhotoGalleryViewModel.Item(
        "${f.path}#${f.hash}#${f.byteSize}", f.path, f.mime, f.byteSize, thumbKey?.ifEmpty { null },
        created = if (f.hasCreated()) f.created.seconds * 1000 else null, hash = f.hash,
        copies = if (f.hasCopies()) f.copies else null, originalHash = f.originalHash, originalRemoved = f.originalRemoved,
    )

    /** A row's key in the viewer's list and the list's: its path and its content. */
    fun key(path: String, hash: String): String = "$path\u0000$hash"
}

/**
 * Settings › Largest files' list (LargestFilesView.kt): its pages, the
 * picks, the thumbnails, a file being opened. Asked only while the screen
 * shows ([appeared]..[left]); left, it forgets the list, and the next time
 * starts from the top.
 */
class LargestFilesModel(
    private val send: GalleryRequest = { kind, baseMs, build ->
        if (kind == null) OTCConnection.request(baseMs, build) else OTCConnection.ask(kind, baseMs, build)
    },
    // A failed page's wait before it is asked again, cut short by a Wake.
    private val pause: suspend (Long) -> Unit = { sleepOrWake(it) },
    // Where the rows' thumbnails are kept (tests: a cache of their own).
    private val thumbs: GridThumbStore = ThumbStore,
    // Where its work runs: the view model's own scope, a test's in tests.
    scope: CoroutineScope? = null,
) : ViewModel() {
    private val scope: CoroutineScope = scope ?: viewModelScope

    /**
     * [rows]: the list so far. [loaded]: a first page came; [end]: the last
     * one did. [loading]: a page is on its way. [problem]: the last page
     * failed, and why (asked again meanwhile). [thumbKeys]: the photos' and
     * videos' tiles by path. [opening]: a file being fetched to open it.
     */
    data class State(
        val rows: List<PbFile> = emptyList(),
        val loaded: Boolean = false,
        val end: Boolean = false,
        val loading: Boolean = false,
        val problem: UiText? = null,
        val needsUpdate: Boolean = false,
        val selected: Set<String> = emptySet(),
        val thumbKeys: Map<String, String> = emptyMap(),
        val opening: String? = null,
        val toast: UiText? = null,
    ) {
        /** L4. */
        val body: LargestBody get() = when {
            needsUpdate -> LargestBody.NEEDS_UPDATE
            !loaded -> if (problem != null) LargestBody.FAILED else LargestBody.LOADING
            rows.isEmpty() && end -> LargestBody.EMPTY
            else -> LargestBody.ROWS
        }
        /** L4: the rows' foot. */
        val foot: LargestFoot get() = LargestRules.foot(loading, problem)
        /** L5. */
        val wantsMore: Boolean get() = LargestRules.wantsMore(loaded, end, loading, problem, needsUpdate)
        /** A failed page is being asked for again (Try again's "Trying…"). */
        val retrying: Boolean get() = problem != null && loading
        val canCompress: Boolean get() = LargestRules.canCompress(rows, selected)
    }

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state

    // The next page's token; bumped by each start over, so a late page of
    // the list before lands nowhere.
    private var token = ""
    private var generation = 0
    private var job: Job? = null
    private var failures = 0
    private var shown = false
    // A failed reading again of the list (reread), not a next page: what Try again does.
    private var rereading = false
    // The rows the viewer was opened over (their keys): what it no longer holds went.
    private var inViewer: Set<String> = emptySet()

    private val tiles by lazy {
        GridThumbFetcher(this.scope, thumbs, send = { b -> send(TILES_KIND, OTCConnection.PAGE_TIMEOUT_MS) { it.setReqGetThumbnails(b) } }, pause = pause) { landed ->
            _state.update { st -> st.copy(thumbKeys = st.thumbKeys + landed.mapValues { it.value ?: "" }) }
        }
    }

    /** The screen shows: the first page is asked for, unless it is here or on its way. */
    fun appeared() {
        shown = true
        val st = _state.value
        if (!st.loaded && !st.loading && !st.needsUpdate) next()
    }

    /** The screen closed: nothing more is asked, and the next opening starts from the top. */
    fun left() {
        shown = false
        generation++
        job?.cancel()
        tiles.reset()
        token = ""
        failures = 0
        rereading = false
        _state.value = State()
    }

    /** The list's end came near: its next page, unless it is over, on its way or failing (asked again by itself). */
    fun nearEnd() {
        val st = _state.value
        if (shown && st.wantsMore) next()
    }

    /** Try again: the page that failed, now. */
    fun retry() {
        if (_state.value.problem == null) return
        failures = 0
        if (rereading) reread() else next()
    }

    // The next page (the first with no token), then each one that fails
    // again after its wait, while the screen shows.
    private fun next() {
        val gen = generation
        job?.cancel()
        _state.update { it.copy(loading = true) }
        job = scope.launch {
            val answer = LargestRules.answer(ask(token, LargestRules.PAGE))
            if (gen != generation) return@launch
            when (answer) {
                is LargestRules.Answer.Page -> {
                    failures = 0
                    token = answer.token
                    _state.update { s ->
                        s.copy(rows = LargestRules.merged(s.rows, answer.files), loaded = true, end = answer.token.isEmpty(), loading = false, problem = null)
                    }
                    landThumbs(answer.files)
                }
                LargestRules.Answer.NeedsUpdate -> _state.update { it.copy(needsUpdate = true, loading = false, problem = null) }
                is LargestRules.Answer.Failed -> {
                    _state.update { it.copy(loading = false, problem = answer.text) }
                    pause(retryWaitMs(++failures))
                    if (gen == generation && shown && _state.value.problem != null) next()
                }
            }
        }
    }

    /**
     * The list read again from the top, as far as it was shown (up to
     * [LargestRules.REREAD_MOST] rows, in the device's biggest pages), and
     * put in place at once - after a delete, an edit or a compress, so the
     * rows are where they now fall and the list keeps its place. Failing,
     * the rows stay as they are and it is asked again, as a page is.
     */
    fun reread() {
        if (!shown) return
        val gen = ++generation
        job?.cancel()
        val want = _state.value.rows.size.coerceIn(LargestRules.PAGE, LargestRules.REREAD_MOST)
        rereading = true
        _state.update { it.copy(loading = true) }
        job = scope.launch {
            var after = ""
            var got = emptyList<PbFile>()
            while (true) {
                val answer = LargestRules.answer(ask(after, minOf(LargestRules.MOST, want - got.size).coerceAtLeast(1)))
                if (gen != generation) return@launch
                when (answer) {
                    is LargestRules.Answer.Page -> {
                        got = LargestRules.merged(got, answer.files)
                        after = answer.token
                        if (after.isEmpty() || got.size >= want || answer.files.isEmpty()) break
                    }
                    LargestRules.Answer.NeedsUpdate -> {
                        rereading = false
                        _state.update { it.copy(needsUpdate = true, loading = false, problem = null) }
                        return@launch
                    }
                    is LargestRules.Answer.Failed -> {
                        _state.update { it.copy(loading = false, problem = answer.text) }
                        pause(retryWaitMs(++failures))
                        if (gen == generation && shown && _state.value.problem != null) reread()
                        return@launch
                    }
                }
            }
            failures = 0
            rereading = false
            token = after
            // A tile the device had none of before (a compressed video's,
            // made since) is asked for again.
            tiles.forgetNone()
            val paths = got.mapTo(HashSet()) { it.path }
            _state.update { s ->
                s.copy(rows = got, loaded = true, end = after.isEmpty(), loading = false, problem = null, selected = s.selected.filterTo(HashSet()) { it in paths })
            }
            landThumbs(got)
        }
    }

    private suspend fun ask(after: String, limit: Int): RespEnvelope? = try {
        send("largest", OTCConnection.LIST_TIMEOUT_MS) { it.setReqListLargestFiles(LargestRules.request(after, limit)) }
    } catch (e: CancellationException) {
        throw e
    } catch (_: Exception) { null }

    // The photos' and videos' tiles of [files]: from the phone's cache,
    // else asked for as their rows show. A path whose content changed
    // forgets the tile it had.
    private suspend fun landThumbs(files: List<PbFile>) {
        val media = files.filter { LargestRules.isMedia(it) }
        if (media.isEmpty()) return
        val landed = try { thumbs.land(media, thumbs.scope()) } catch (e: CancellationException) { throw e } catch (_: Exception) { return }
        _state.update { s -> s.copy(thumbKeys = s.thumbKeys - landed.keys.filterValues { it == null }.keys + landed.keys.filterValues { it != null }.mapValues { it.value!! }) }
        tiles.note(landed.fetch, landed.refetch)
    }

    /** A row is on screen: its tile is fetched first if it has none. */
    fun rowShown(f: PbFile) {
        if (LargestRules.isMedia(f)) tiles.shown(f.path, needsThumb = _state.value.thumbKeys[f.path].isNullOrEmpty())
    }

    /** The row left the screen. */
    fun rowGone(path: String) = tiles.gone(path)

    /** A tile's bytes ([key]) didn't decode: dropped from the cache and fetched again. */
    fun thumbUndecodable(path: String, key: String) = scope.launch {
        thumbs.discard(key)
        _state.update { it.copy(thumbKeys = it.thumbKeys - path) }
        tiles.shown(path, needsThumb = true)
    }

    /** A tile's bytes by its key. */
    suspend fun tileBytes(key: String): ByteArray? = thumbs.load(key)

    fun toggle(path: String) = _state.update { it.copy(selected = if (path in it.selected) it.selected - path else it.selected + path) }

    /** A long press: the row is picked (a press never unpicks). */
    fun pick(path: String) = _state.update { it.copy(selected = it.selected + path) }

    /** Nothing picked any more (the pill's ×, Back, Compress sent). */
    fun clearSelection() { if (_state.value.selected.isNotEmpty()) _state.update { it.copy(selected = emptySet()) } }

    /** What Compress asks about: the picked rows, in the list's order. */
    fun compressItems(): List<CompressItem> = _state.value.let { LargestRules.compressItems(it.rows, it.selected) }

    /**
     * The device compressed [results]: each row shows its file at once
     * (CompressDialog), and once the job is over ([over]) the list is read
     * again for the order.
     */
    fun compressed(results: List<CompressResult>, over: Boolean = false) {
        if (results.isNotEmpty()) {
            val before = _state.value.rows
            _state.update { s -> s.copy(rows = LargestRules.compressed(s.rows, results), thumbKeys = s.thumbKeys - results.map { it.path }.toSet()) }
            val now = _state.value.rows
            scope.launch {
                // The old content's tile leaves this phone's cache; the new
                // one's is found as a page's are.
                for (r in results) before.firstOrNull { it.path == r.path && it.hash.isNotEmpty() && it.hash != r.file.hash }?.let { thumbs.discard(it.hash) }
                landThumbs(now.filter { f -> results.any { it.file.hash == f.hash } })
            }
        }
        if (over) reread()
    }

    /** The viewer's items for the list's photos and videos, in its order. */
    fun viewerItems(): List<PhotoGalleryViewModel.Item> {
        val st = _state.value
        val media = st.rows.filter { LargestRules.isMedia(it) }
        inViewer = media.mapTo(HashSet()) { LargestRules.key(it.path, it.hash) }
        return media.map { LargestRules.viewerItem(it, st.thumbKeys[it.path]) }
    }

    /**
     * The viewer deleted or edited a file ([now]: its items as they are):
     * a row it no longer holds goes at once, and the list is read again -
     * an edit comes back where its size puts it.
     */
    fun viewerChanged(now: List<PhotoGalleryViewModel.Item>) {
        val kept = now.mapTo(HashSet()) { LargestRules.key(it.path, it.hash) }
        val gone = inViewer - kept
        inViewer = inViewer intersect kept
        if (gone.isNotEmpty()) _state.update { s ->
            s.copy(rows = s.rows.filter { LargestRules.key(it.path, it.hash) !in gone }, selected = s.selected.filterTo(HashSet()) { p -> gone.none { it.startsWith("$p\u0000") } })
        }
        reread()
    }

    /** A file that isn't a photo or video, opened as Files opens it: fetched to the cache and handed to the phone's apps. */
    fun open(context: Context, f: PbFile) {
        if (_state.value.opening != null) return
        _state.update { it.copy(opening = f.path) }
        scope.launch {
            try {
                val (tmp, mime) = fetchToPreview(context, f.path, f.mime)
                Share.preview(context, tmp, mime)
            } catch (e: CancellationException) {
                throw e
            } catch (_: ChunkedDownload.Refused) {
                toast(S.appFilesOpenFailed())
            } catch (e: Exception) {
                toast(S.appFilesDownloadFailed(e.message.toString()))
            } finally {
                _state.update { it.copy(opening = null) }
            }
        }
    }

    // A few seconds at the top, as Files' toasts.
    private fun toast(m: UiText) {
        _state.update { it.copy(toast = m) }
        scope.launch {
            delay(TOAST_MS)
            _state.update { if (it.toast == m) it.copy(toast = null) else it }
        }
    }

    private companion object {
        const val TOAST_MS = 3_000L
    }
}
