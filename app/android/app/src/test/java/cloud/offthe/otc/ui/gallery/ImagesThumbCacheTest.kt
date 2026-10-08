// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.ui.common.FIRST_PAGE_LIMIT_WITHOUT_THUMBS
import cloud.offthe.otc.ui.common.NEXT_PAGE_LIMIT_WITHOUT_THUMBS
import cloud.offthe.otc.ui.common.TestThumbStore
import cloud.offthe.otc.ui.common.hx
import cloud.offthe.otc.ui.common.ThumbKind
import com.google.protobuf.ByteString
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.IOException
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

// Images over the phone's thumbnail cache: pages asked without thumbnails
// (and bigger once the device is known to leave them out), every entry
// counted in `have`, the tiles from the cache or fetched with GetThumbnails,
// a device before release 113's thumbnails kept for the next start.
class ImagesThumbCacheTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private val sent = CopyOnWriteArrayList<ReqEnvelope>()
    private fun searches(): List<SearchPhotos> = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS }.map { it.reqSearchPhotos }
    private fun thumbAsks(): List<List<String>> = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS }.map { it.reqGetThumbnails.pathsList }

    /** A device: SearchPhotos by [pages], GetThumbnails by [thumbs]. */
    private fun send(pages: (SearchPhotos) -> RespEnvelope?, thumbs: (List<String>) -> RespEnvelope?): GalleryRequest = { _, _, build ->
        val req = ReqEnvelope.newBuilder().also(build).build()
        sent += req
        when (req.payloadCase) {
            ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
            ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> pages(req.reqSearchPhotos)
            ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> { assertTrue(req.reqGetThumbnails.smallThumbnails); thumbs(req.reqGetThumbnails.pathsList) }
            else -> null
        } ?: throw IOException("no answer")
    }

    private fun row(path: String, says: Boolean? = null, content: String? = null): File {
        val b = File.newBuilder().setPath(path).setHash(hx(path)).setMime("image/jpeg")
        if (says != null) b.thumbnailSmall = says
        if (content != null) b.content = ByteString.copyFromUtf8(content)
        return b.build()
    }

    private fun page(rows: List<File>, token: String = "") =
        RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(rows).setToken(token)).build()

    private fun small(paths: List<String>) = page(paths.map { row(it, says = true, content = "small $it") })

    private fun vm(store: TestThumbStore, send: GalleryRequest) =
        PhotoGalleryViewModel("test", send = send, pause = {}, online = { true }, problemOf = { _, _ -> cloud.offthe.otc.ui.common.LoadProblem.FAILED }, thumbs = store, scope = scope)

    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    /** The grid shows every row, as far as the tiles go: what a page lacks is asked for at once. */
    private fun showsEverything(vm: PhotoGalleryViewModel) = on { vm.rowShown(0, 1_000) }

    private fun keys(vm: PhotoGalleryViewModel) = vm.state.value.let { st -> st.items.associate { it.path to st.thumbKeyOf(it) } }

    @Test fun pagesLeaveTheThumbnailsOutAndGrowOnceTheDeviceDoes() {
        val store = TestThumbStore(tmp.newFolder())
        // A device of release 113: rows without content, each saying it has its small one.
        val dev = send(
            pages = { sp ->
                when {
                    sp.tagsCount > 0 -> page(listOf(row("x", says = true)))
                    sp.token.isEmpty() -> page(listOf(row("a", says = true), row("b", says = true), row("c", says = true)), token = "t1")
                    else -> page(listOf(row("d", says = true), row("e", says = true)))
                }
            },
            thumbs = { small(it) },
        )
        val vm = vm(store, dev)
        showsEverything(vm)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the tiles") { keys(vm) == mapOf("a" to "${hx("a")}#S", "b" to "${hx("b")}#S", "c" to "${hx("c")}#S") }
        val first = searches().single()
        assertTrue(first.smallThumbnails)
        assertTrue(first.omitThumbnails)
        // Not known yet to leave them out: the small first page, as before.
        assertEquals(FIRST_PHOTO_PAGE_LIMIT, first.limit)
        assertEquals(listOf(listOf("a", "b", "c")), thumbAsks())
        assertEquals(ThumbKind.SMALL, store.cache.kindOf(hx("b")))
        on { vm.loadMoreIfNeeded(2) }
        await("the next page") { vm.state.value.items.size == 5 }
        val next = searches().last()
        assertEquals("t1", next.token)
        // Every entry counts, without content as with.
        assertEquals(3, next.have)
        assertEquals(NEXT_PAGE_LIMIT_WITHOUT_THUMBS, next.limit)
        assertTrue(next.omitThumbnails)
        // Known now: a new search's first page is the bigger one.
        on { vm.addChip("x") }
        await("the new search") { vm.state.value.items.map { it.path } == listOf("x") }
        assertEquals(FIRST_PAGE_LIMIT_WITHOUT_THUMBS, searches().last().limit)
        await("its tile") { keys(vm)["x"] == "${hx("x")}#S" }
        assertEquals("small x", String(on { vm.loadThumb("${hx("x")}#S") }!!))
    }

    @Test fun anOlderDevicesThumbnailsAreKeptForTheNextStart() {
        val dir = tmp.newFolder()
        // Release 112: the flag ignored, the thumbnails in the page, saying nothing of their kind.
        val old = send(
            pages = { page(listOf(row("a", content = "A"), row("b", content = "B"), row("c", content = "C"))) },
            thumbs = { throw AssertionError("asked for $it") },
        )
        val firstStore = TestThumbStore(dir)
        val first = vm(firstStore, old)
        scope.launch { first.resetAndLoadFirstPage() }
        await("the photos") { first.state.value.items.size == 3 }
        assertEquals(mapOf("a" to "${hx("a")}#U", "b" to "${hx("b")}#U", "c" to "${hx("c")}#U"), keys(first))
        assertTrue(thumbAsks().isEmpty())
        // Pages with content: the device doesn't leave them out.
        assertFalse(on { firstStore.omitsThumbnails() })
        firstStore.cache.close()

        // The app starts again (a new cache instance on the same folder); the
        // device is on 113 now. a: no small one yet (false) - the cached one
        // does; b: has its small one now - shown from the cache and fetched;
        // d: not cached - fetched.
        sent.clear()
        val restarted = TestThumbStore(dir)
        val dev = send(
            pages = { page(listOf(row("a", says = false), row("b", says = true), row("d", says = true))) },
            thumbs = { small(it) },
        )
        val second = vm(restarted, dev)
        showsEverything(second)
        scope.launch { second.resetAndLoadFirstPage() }
        await("the tiles") { keys(second) == mapOf("a" to "${hx("a")}#U", "b" to "${hx("b")}#S", "d" to "${hx("d")}#S") }
        assertEquals(listOf(listOf("b", "d")), thumbAsks())
        assertEquals("A", String(on { second.loadThumb("${hx("a")}#U") }!!))
        assertEquals(ThumbKind.SMALL, restarted.cache.kindOf(hx("b")))
    }

    @Test fun aTileWhoseThumbnailIsGoneIsFetchedAgain() {
        val store = TestThumbStore(tmp.newFolder())
        val dev = send(pages = { page(listOf(row("a", says = true))) }, thumbs = { small(it) })
        val vm = vm(store, dev)
        showsEverything(vm)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the tile") { keys(vm)["a"] == "${hx("a")}#S" }
        // Settings cleared the cache: the tile finds nothing and says so.
        on { store.core.clearAll() }
        assertEquals(null, on { vm.loadThumb("${hx("a")}#S") })
        on {
            vm.thumbLost("a")
            assertEquals(null, keys(vm)["a"])
        }
        await("fetched again") { thumbAsks().size == 2 && keys(vm)["a"] == "${hx("a")}#S" }
        assertEquals(ThumbKind.SMALL, store.cache.kindOf(hx("a")))
    }

    @Test fun aPagesMissesWaitForTheirTiles() {
        val store = TestThumbStore(tmp.newFolder())
        val dev = send(pages = { page((0 until 60).map { row("p$it", says = true) }) }, thumbs = { small(it) })
        val vm = vm(store, dev)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the photos") { vm.state.value.items.size == 60 }
        Thread.sleep(100)
        // Nothing on screen yet: nothing asked for.
        assertTrue(thumbAsks().isEmpty())
        // The first rows show (tiles 0-8): those, and the ones within reach (to 38).
        on { vm.rowShown(0, 3); vm.rowShown(3, 6); vm.rowShown(6, 9) }
        await("within reach") { thumbAsks().sumOf { it.size } == 39 }
        Thread.sleep(100)
        assertEquals(39, thumbAsks().sumOf { it.size })
    }

    @Test fun deletingPhotosDropsTheirThumbnails() {
        val store = TestThumbStore(tmp.newFolder())
        val ok = RespEnvelope.newBuilder().setRespAck(cloud.offthe.otc.proto.Ack.newBuilder().setOk(true)).build()
        val dev: GalleryRequest = { kind, base, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            if (req.payloadCase == ReqEnvelope.PayloadCase.REQ_DEL_FILE) { sent += req; ok }
            else send(pages = { page(listOf(row("a", content = "A"), row("b", content = "B"))) }, thumbs = { null })(kind, base, build)
        }
        val vm = vm(store, dev)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the photos") { vm.state.value.items.size == 2 }
        assertEquals(ThumbKind.UNKNOWN, store.cache.kindOf(hx("a")))
        on { vm.toggleSelect("a") }
        on { vm.deleteSelected() }
        await("the delete") { vm.state.value.items.map { it.path } == listOf("b") }
        assertNull(store.cache.kindOf(hx("a")))
        assertEquals(ThumbKind.UNKNOWN, store.cache.kindOf(hx("b")))
    }

    @Test fun clearingTheCacheLetsTilesWithoutAThumbnailAskAgain() {
        val store = TestThumbStore(tmp.newFolder())
        var hasIt = false
        // A device of release 113 without a's thumbnail yet.
        val dev = send(pages = { page(listOf(row("a", says = false))) }, thumbs = { p -> if (hasIt) small(p) else page(emptyList()) })
        val vm = vm(store, dev)
        on { vm.watchThumbnailClears() }
        showsEverything(vm)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the answer") { thumbAsks().size == 1 && vm.state.value.thumbKeys["a"] == "" }
        // It has none: its tile showing again asks for nothing.
        on { vm.tileShown("a", needsThumb = true) }
        Thread.sleep(100)
        assertEquals(1, thumbAsks().size)
        // Settings' Clear: the mark goes, and the tile asks again.
        hasIt = true
        on { store.core.clearAll() }
        await("the mark gone") { !vm.state.value.thumbKeys.containsKey("a") }
        on { vm.tileShown("a", needsThumb = true) }
        await("asked again") { keys(vm)["a"] == "${hx("a")}#S" }
        assertEquals(2, thumbAsks().size)
    }
}
