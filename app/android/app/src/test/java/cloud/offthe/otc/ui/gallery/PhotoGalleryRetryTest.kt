// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.Patience
import cloud.offthe.otc.net.WSClient
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ImageGroups
import cloud.offthe.otc.proto.ListOfFiles
import com.google.protobuf.ByteString
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.proto.TagsList
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.FIRST_PAGE_LIMIT_WITHOUT_THUMBS
import cloud.offthe.otc.ui.common.NEXT_PAGE_LIMIT_WITHOUT_THUMBS
import cloud.offthe.otc.ui.common.TestThumbStore
import cloud.offthe.otc.ui.common.hx
import com.google.protobuf.Timestamp
import kotlinx.coroutines.CompletableDeferred
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
import java.time.ZoneId
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

// Images' paging against a stand-in device: a page that fails - the first
// or a later one - is asked again with the web's backoff, a new search
// leaves the old one's retries and answers behind, a Wake asks at once, and
// the grid always says what is happening (GalleryBody).
class PhotoGalleryRetryTest {
    // One thread, as the view model's main thread is: its work never races itself.
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    // The tiles' thumbnail cache: empty, a device not known to leave thumbnails out.
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    /** One request as the view model asked it: what, of which kind (null: not a screen's load), with what usual time. */
    private data class Sent(val req: ReqEnvelope, val kind: String?, val baseMs: Long)

    /** A stand-in device: [answer] for each request (null: none came - an IOException), and what was asked. */
    private class Device(@Volatile var answer: suspend (ReqEnvelope) -> RespEnvelope?) {
        val sent = CopyOnWriteArrayList<Sent>()
        val send: GalleryRequest = { kind, baseMs, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += Sent(req, kind, baseMs)
            answer(req) ?: throw IOException("no answer")
        }
        fun searches(): List<SearchPhotos> = sent.map { it.req }.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS }.map { it.reqSearchPhotos }
        fun of(case: ReqEnvelope.PayloadCase) = sent.filter { it.req.payloadCase == case }
        fun basesOf(case: ReqEnvelope.PayloadCase) = of(case).map { it.baseMs }.toSet()
        fun kindsOf(case: ReqEnvelope.PayloadCase) = of(case).map { it.kind }.toSet()
    }

    /** The waits between retries, as asked for; [gate]: until it opens, a wait doesn't end. */
    private class Waits {
        val asked = CopyOnWriteArrayList<Long>()
        @Volatile var gate: CompletableDeferred<Unit>? = null
        val pause: suspend (Long) -> Unit = { ms -> asked += ms; gate?.await() }
    }

    private fun photo(path: String, at: String = "2026-10-01T10:00:00Z"): File {
        val t = java.time.Instant.parse(at)
        return File.newBuilder().setPath(path).setHash(hx(path)).setMime("image/jpeg")
            .setCreated(Timestamp.newBuilder().setSeconds(t.epochSecond)).build()
    }
    private fun page(files: List<File>, token: String = "") =
        RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(files).setToken(token)).build()
    private fun page(vararg paths: String, token: String = "") = page(paths.map { photo(it) }, token)
    private val noBuckets = RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
    private val deviceError = RespEnvelope.newBuilder().setError(true).setErrorMessage("internal error").build()
    // The grid's GetThumbnails for the photos' tiles (none here).
    private val noThumbs = RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()

    private fun vm(dev: Device, waits: Waits = Waits(), problem: LoadProblem = LoadProblem.FAILED, online: () -> Boolean = { true }) =
        PhotoGalleryViewModel("test", send = dev.send, pause = waits.pause, online = online, problemOf = { _, _ -> problem }, thumbs = thumbs, scope = scope)

    /** Runs [block] on the view model's own thread. */
    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun start(vm: PhotoGalleryViewModel) { scope.launch { vm.resetAndLoadFirstPage() } }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    /** Lets whatever the view model's thread has queued run (a test asserting that nothing more happens). */
    private fun settle() { repeat(5) { on { kotlinx.coroutines.yield() }; Thread.sleep(10) } }

    // A device answering the date buckets; the searches by [searches].
    private fun device(searches: suspend (SearchPhotos) -> RespEnvelope?) = Device { req ->
        when (req.payloadCase) {
            ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> noBuckets
            ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> searches(req.reqSearchPhotos)
            ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> noThumbs
            else -> null
        }
    }

    // ---- the first page ------------------------------------------------------------

    @Test fun aFirstPageThatFailsIsAskedAgainUntilItComes() {
        var asks = 0
        val dev = device { if (++asks <= 3) null else page("a", "b") }
        val waits = Waits()
        val vm = vm(dev, waits)
        start(vm)
        await("the photos") { vm.state.value.items.size == 2 }
        // 1 s doubling, as the web's usePageRetry; each ask the search's first page again.
        assertEquals(listOf(1_000L, 2_000L, 4_000L), waits.asked.toList())
        assertEquals(4, dev.searches().size)
        dev.searches().forEach { assertEquals("", it.token); assertEquals(FIRST_PHOTO_PAGE_LIMIT, it.limit) }
        val st = vm.state.value
        assertNull(st.pageProblem)
        assertEquals(GalleryBody.PHOTOS, st.body)
        assertTrue(st.endReached)
        // Pages wait as long as a page may take; the buckets as a small list
        // - each a kind of its own, which a timeout gives more time
        // (OTCConnection.ask).
        assertEquals(setOf(OTCConnection.PAGE_TIMEOUT_MS), dev.basesOf(ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS))
        assertEquals(setOf<String?>("page"), dev.kindsOf(ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS))
        assertEquals(setOf(OTCConnection.LIST_TIMEOUT_MS), dev.basesOf(ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS))
        assertEquals(setOf<String?>("buckets"), dev.kindsOf(ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS))
    }

    @Test fun anErrorAnswerIsAFailureNotTheEnd() {
        var asks = 0
        val dev = device { if (++asks == 1) deviceError else page("a") }
        val vm = vm(dev)
        start(vm)
        await("the photo") { vm.state.value.items.size == 1 }
        assertEquals(2, dev.searches().size)
    }

    @Test fun whileTheFirstPageFailsTheGridSaysWhyAndKeepsSayingItThroughTheRetries() {
        val answer = CompletableDeferred<RespEnvelope?>()
        var asks = 0
        val dev = device { if (++asks == 1) null else answer.await() }
        val waits = Waits().apply { gate = CompletableDeferred() }
        val vm = vm(dev, waits, problem = LoadProblem.UNREACHABLE)
        // Before anything came: grey tiles, never a blank page.
        assertEquals(GalleryBody.SKELETON, vm.state.value.body)
        start(vm)
        await("the failure") { vm.state.value.let { it.pageProblem != null && !it.loading } }
        var st = vm.state.value
        assertEquals(LoadProblem.UNREACHABLE, st.pageProblem)
        assertEquals(GalleryBody.PROBLEM, st.body)
        assertFalse(st.loading)
        assertFalse(st.retrying)
        // Try again: on its way, the problem stays up ("Trying…"), not grey tiles.
        on { vm.retryPage() }
        await("the retry") { vm.state.value.loading }
        st = vm.state.value
        assertEquals(GalleryBody.PROBLEM, st.body)
        assertTrue(st.retrying)
        answer.complete(page("a"))
        await("the photo") { vm.state.value.items.size == 1 }
        st = vm.state.value
        assertNull(st.pageProblem)
        assertEquals(GalleryBody.PHOTOS, st.body)
        // Try again didn't wait: the only wait asked for was the first one's.
        assertEquals(listOf(1_000L), waits.asked.toList())
    }

    @Test fun aWakeAsksAtOnce() {
        var asks = 0
        val dev = device { if (++asks == 1) null else page("a") }
        val waits = Waits().apply { gate = CompletableDeferred() } // a wait that never ends
        val vm = vm(dev, waits)
        start(vm)
        await("the failure") { vm.state.value.let { it.pageProblem != null && !it.loading } }
        // Signed in again, a network came up, the app back in the foreground.
        on { vm.wake() }
        await("the photo") { vm.state.value.items.size == 1 }
        assertEquals(2, dev.searches().size)
        assertNull(vm.state.value.pageProblem)
        // Nothing failed: a Wake asks for nothing.
        on { vm.wake() }
        settle()
        assertEquals(2, dev.searches().size)
    }

    @Test fun aSlowDeviceGetsMoreTimeAfterEachTimeoutAndLessOnceItAnswers() {
        // The view model's requests through a Patience, as OTCConnection.ask
        // sends them: the time each page had, and the device's pace.
        val patience = Patience()
        val limits = CopyOnWriteArrayList<Long>()
        var asks = 0
        val dev = Device { req ->
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> noBuckets
                // Three timeouts, then an answer in 80 s - within the usual 2 minutes.
                else -> if (++asks <= 3) throw WSClient.RequestTimeout() else page("a")
            }
        }
        val send: GalleryRequest = { kind, baseMs, build ->
            if (kind != "page") dev.send(kind, baseMs, build)
            else patience.run(kind, baseMs) { limit -> limits += limit; WSClient.Answer(dev.send(kind, baseMs, build), 80_000) }
        }
        val vm = PhotoGalleryViewModel("test", send = send, pause = Waits().pause, online = { true }, problemOf = { _, _ -> LoadProblem.SLOW }, thumbs = thumbs, scope = scope)
        start(vm)
        await("the photo") { vm.state.value.items.size == 1 }
        // 2 minutes, then twice that, then at most 5 minutes: never cut off for good.
        assertEquals(listOf(120_000L, 240_000L, 300_000L, 300_000L), limits.toList())
        // Answered within the usual time: the next page has that again.
        assertEquals(120_000L, patience.timeoutFor("page", OTCConnection.PAGE_TIMEOUT_MS))
    }

    @Test fun backOnlineTheOfflineLineGivesWayWhileThePageIsAskedAgain() {
        var online = false
        val answer = CompletableDeferred<RespEnvelope?>()
        var asks = 0
        val dev = device { if (++asks == 1) null else answer.await() }
        val waits = Waits().apply { gate = CompletableDeferred() }
        val vm = PhotoGalleryViewModel(
            "test", send = dev.send, pause = waits.pause, online = { online },
            problemOf = { _, _ -> if (online) LoadProblem.FAILED else LoadProblem.OFFLINE }, thumbs = thumbs, scope = scope,
        )
        start(vm)
        await("the failure") { vm.state.value.let { it.pageProblem != null && !it.loading } }
        assertEquals(LoadProblem.OFFLINE, vm.state.value.pageProblem)
        // A Wake while still offline changes nothing in what it says.
        on { vm.wake() }
        await("the retry") { vm.state.value.loading }
        assertEquals(LoadProblem.OFFLINE, vm.state.value.pageProblem)
        answer.complete(null)
        await("the second failure") { vm.state.value.let { it.pageProblem != null && !it.loading } }
        // The network is back (NetworkWatch's Wake): asked again at once, and
        // "offline" no longer says why while the page takes its time.
        online = true
        val third = CompletableDeferred<RespEnvelope?>()
        dev.answer = { req -> if (req.payloadCase == ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS) third.await() else noBuckets }
        on { vm.wake() }
        await("the retry") { vm.state.value.loading }
        assertEquals(LoadProblem.FAILED, vm.state.value.pageProblem)
        assertEquals(GalleryBody.PROBLEM, vm.state.value.body)
        third.complete(page("a"))
        await("the photo") { vm.state.value.items.size == 1 }
        assertNull(vm.state.value.pageProblem)
    }

    @Test fun pagesThatAddNothingAreAskedAgainInAWhile() {
        // A device that starts the search over each time: a token, and the
        // same photo the grid already has.
        var round = 1
        val dev = device { sp ->
            if (sp.token.isEmpty()) page("a", token = "t") else if (round == 1) page("a", token = "t") else page("b")
        }
        val waits = Waits().apply { gate = CompletableDeferred() }
        val vm = vm(dev, waits)
        start(vm)
        await("the first page") { vm.state.value.items.size == 1 }
        on { vm.loadMoreIfNeeded(0) }
        await("the give-up") { vm.state.value.let { it.pageProblem != null && !it.loading } }
        // Twelve pages in a row added nothing: said, and asked again after
        // 10 s rather than never (the grid promises the photos).
        assertEquals(1 + 12, dev.searches().size)
        assertEquals(LoadProblem.FAILED, vm.state.value.pageProblem)
        await("the retry's wait") { waits.asked.isNotEmpty() }
        assertEquals(listOf(10_000L), waits.asked.toList())
        round = 2
        waits.gate!!.complete(Unit)
        await("the next photo") { vm.state.value.items.size == 2 }
        assertNull(vm.state.value.pageProblem)
    }

    // ---- generations ----------------------------------------------------------------

    @Test fun aNewSearchLeavesTheOldOnesRetryBehind() {
        val dev = device { sp -> if (sp.tagsList.isEmpty()) null else page("beach1") }
        val waits = Waits().apply { gate = CompletableDeferred() }
        val vm = vm(dev, waits)
        start(vm)
        await("the old search's retry waiting") { waits.asked.size == 1 }
        on { vm.addChip("beach") }
        await("the new search's photo") { vm.state.value.items.map { it.path } == listOf("beach1") }
        // The old search's wait ends: nothing is asked for it any more.
        waits.gate!!.complete(Unit)
        settle()
        assertEquals(1, dev.searches().count { it.tagsList.isEmpty() })
        assertEquals(listOf("beach1"), vm.state.value.items.map { it.path })
        assertNull(vm.state.value.pageProblem)
    }

    @Test fun anAnswerToAnOldSearchLandsNowhere() {
        val old = CompletableDeferred<RespEnvelope?>()
        val dev = device { sp -> if (sp.tagsList.isEmpty()) old.await() else page("new1") }
        val vm = vm(dev)
        start(vm)
        await("the first ask") { dev.searches().isNotEmpty() }
        on { vm.addChip("x") }
        await("the new search's photo") { vm.state.value.items.size == 1 }
        // The old search's answer comes late: dropped, and no failure either.
        old.complete(page("old1", "old2"))
        settle()
        assertEquals(listOf("new1"), vm.state.value.items.map { it.path })
        assertNull(vm.state.value.pageProblem)
    }

    @Test fun anOldSearchFailingLateIsNoFailureOfTheNewOne() {
        val old = CompletableDeferred<RespEnvelope?>()
        val dev = device { sp -> if (sp.tagsList.isEmpty()) old.await() else page("new1") }
        val waits = Waits()
        val vm = vm(dev, waits)
        start(vm)
        await("the first ask") { dev.searches().isNotEmpty() }
        on { vm.addChip("x") }
        await("the new search's photo") { vm.state.value.items.size == 1 }
        old.complete(null)
        settle()
        assertNull(vm.state.value.pageProblem)
        assertEquals(listOf("new1"), vm.state.value.items.map { it.path })
        // No retry was waited for: nothing of the new search failed.
        assertTrue(waits.asked.isEmpty())
        assertEquals(2, dev.searches().size)
    }

    // ---- later pages ----------------------------------------------------------------

    @Test fun aLaterPageThatFailsSaysSoAtTheEndAndIsAskedAgain() {
        val first = (1..12).map { "p$it" }.toTypedArray()
        var nextAsks = 0
        val dev = device { sp ->
            if (sp.token.isEmpty()) page(*first, token = "t1")
            else if (++nextAsks <= 2) null else page("p13")
        }
        val waits = Waits().apply { gate = CompletableDeferred() }
        val vm = vm(dev, waits)
        start(vm)
        await("the first page") { vm.state.value.items.size == 12 }
        on { vm.loadMoreIfNeeded(11) }
        await("the failure") { vm.state.value.let { it.pageProblem != null && !it.loading } }
        var st = vm.state.value
        // The photos stay; the end of the grid says it failed.
        assertEquals(GalleryBody.PHOTOS, st.body)
        assertEquals(12, st.items.size)
        // Scrolling doesn't ask again before the retry does.
        on { vm.loadMoreIfNeeded(11) }
        settle()
        assertEquals(2, dev.searches().size)
        // The waits end: asked again with the token until it comes.
        waits.gate = null
        on { vm.wake() }
        await("the next page") { vm.state.value.items.size == 13 }
        st = vm.state.value
        assertNull(st.pageProblem)
        assertTrue(st.endReached)
        assertEquals(listOf("", "t1", "t1", "t1"), dev.searches().map { it.token })
        // Continuing with the token: a bigger page (a device before release
        // 113 answers at most its default, 30, whatever is asked).
        assertEquals(NEXT_PAGE_LIMIT_WITHOUT_THUMBS, dev.searches().last().limit)
    }

    // ---- the scrubber's jump --------------------------------------------------------

    @Test fun aJumpThatFailsIsAskedAgainFromItsMonthAndLandsThere() {
        var jumpAsks = 0
        val dev = device { sp ->
            if (!sp.hasBefore()) page("new")
            else if (++jumpAsks == 1) null
            else page(listOf(photo("old", "2019-03-10T10:00:00Z")))
        }
        val vm = vm(dev)
        start(vm)
        await("the newest photos") { vm.state.value.items.size == 1 }
        on { vm.previewMonth(PhotoGalleryViewModel.DateBucket("2019-03", 1, 0, 1)) }
        on { vm.jumpToDate("2019-03") }
        await("the jump's photo") { vm.state.value.items.map { it.path } == listOf("old") }
        val st = vm.state.value
        assertEquals(1, st.jumpsLanded)
        assertNull(st.placeholderCount)
        assertNull(st.pageProblem)
        // Both asks from the same month's end, as the first page of a search
        // - the bigger one: the page before came without thumbnails, so the
        // device is known to leave them out.
        val cutoff = jumpCutoffMs("2019-03", ZoneId.systemDefault())!!
        val jumps = dev.searches().filter { it.hasBefore() }
        assertEquals(2, jumps.size)
        assertEquals(FIRST_PHOTO_PAGE_LIMIT, dev.searches().first().limit)
        jumps.forEach {
            assertEquals(Math.floorDiv(cutoff, 1000L), it.before.seconds)
            assertEquals("", it.token)
            assertEquals(FIRST_PAGE_LIMIT_WITHOUT_THUMBS, it.limit)
        }
    }

    // ---- the lists ------------------------------------------------------------------

    @Test fun theTagsAndCollectionsAreAskedAgainUntilTheyCome() {
        var tagAsks = 0
        var groupAsks = 0
        val dev = Device { req ->
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_GET_TAGS -> if (++tagAsks <= 2) null else RespEnvelope.newBuilder().setRespTagsList(TagsList.newBuilder().addTags("beach")).build()
                ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS -> if (++groupAsks <= 1) deviceError
                    else RespEnvelope.newBuilder().setRespImageGroups(ImageGroups.newBuilder().addGroups(ImageGroup.newBuilder().setId("g1").setName("Trip"))).build()
                else -> null
            }
        }
        val waits = Waits()
        val vm = vm(dev, waits)
        on { vm.loadListsOnce() }
        await("the tags") { vm.state.value.tags == listOf("beach") }
        assertFalse(on { vm.loadGroups() })
        await("the collections") { vm.state.value.groupsLoaded }
        assertEquals(listOf("Trip"), vm.state.value.groups.map { it.name })
        assertEquals(3, tagAsks)
        assertEquals(2, groupAsks)
        assertEquals(setOf(OTCConnection.LIST_TIMEOUT_MS), dev.basesOf(ReqEnvelope.PayloadCase.REQ_GET_TAGS))
        assertEquals(setOf(OTCConnection.PAGE_TIMEOUT_MS), dev.basesOf(ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS))
        // The covers as a grid's tiles: small thumbnails (release 111).
        dev.of(ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS).forEach { assertTrue(it.req.reqListImageGroups.smallThumbnails) }
    }

    // ---- small thumbnails (release 111) ---------------------------------------------

    @Test fun everyPageAsksForSmallThumbnails() {
        val first = (1..12).map { "p$it" }.toTypedArray()
        val dev = device { sp ->
            when {
                sp.hasBefore() -> page(listOf(photo("old", "2019-03-10T10:00:00Z")))
                sp.token.isEmpty() -> page(*first, token = "t1")
                else -> page("p13")
            }
        }
        val vm = vm(dev)
        // The grid's rows on screen: their tiles' thumbnails are asked for.
        on { vm.rowShown(0, 1_000) }
        start(vm)
        await("the first page") { vm.state.value.items.size == 12 }
        on { vm.loadMoreIfNeeded(11) }
        await("the next page") { vm.state.value.items.size == 13 }
        on { vm.jumpToDate("2019-03") }
        await("the jump") { vm.state.value.items.map { it.path } == listOf("old") }
        // The first page, a later one and a jump's: the grid's tiles.
        val searches = dev.searches()
        assertEquals(listOf("", "t1", ""), searches.map { it.token })
        assertTrue(searches.last().hasBefore())
        searches.forEach { assertTrue(it.smallThumbnails) }
        // And without them (release 113): the tiles come from the cache, and
        // what it lacks from GetThumbnails - small ones too.
        searches.forEach { assertTrue(it.omitThumbnails) }
        // Asked for after a moment's gathering.
        await("the tiles' thumbnails") { dev.of(ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS).isNotEmpty() }
        dev.of(ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS).forEach { assertTrue(it.req.reqGetThumbnails.smallThumbnails) }
    }

    @Test fun aNewCollectionsCoverIsSmallToo() {
        val dev = Device { req ->
            if (req.payloadCase == ReqEnvelope.PayloadCase.REQ_CREATE_IMAGE_GROUP) {
                RespEnvelope.newBuilder().setRespImageGroup(cloud.offthe.otc.proto.RespImageGroup.newBuilder().setGroup(ImageGroup.newBuilder().setId("g").setName("Trip"))).build()
            } else null
        }
        val vm = vm(dev)
        on { vm.toggleSelect("/a.jpg") }
        on { vm.createGroupFromSelection("Trip") }
        val create = dev.of(ReqEnvelope.PayloadCase.REQ_CREATE_IMAGE_GROUP).single()
        assertTrue(create.req.reqCreateImageGroup.smallThumbnails)
        // Not a screen's load: the socket's own time.
        assertNull(create.kind)
    }

    // ---- the viewer: the big thumbnail where the thumbnail stays ---------------------

    private fun bigThumbsAsked(dev: Device, path: String) = dev.of(ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS).filter { it.req.reqGetThumbnails.pathsList == listOf(path) }

    // A device with three photos (a, b, c) whose full size answers by [full],
    // and whose thumbnails by [thumbs].
    private fun viewerDevice(full: (String) -> RespEnvelope?, thumbs: (String) -> RespEnvelope?) = Device { req ->
        when (req.payloadCase) {
            ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> noBuckets
            ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> page("a", "b", "c")
            ReqEnvelope.PayloadCase.REQ_GET_FILE -> full(req.reqGetFile.path)
            // The grid's tiles (small ones) apart from the viewer's big one.
            ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS ->
                if (req.reqGetThumbnails.smallThumbnails) noThumbs else thumbs(req.reqGetThumbnails.getPaths(0))
            else -> null
        }
    }

    private fun bigThumb(path: String, bytes: String) = RespEnvelope.newBuilder().setRespListOfFiles(
        ListOfFiles.newBuilder().addFiles(File.newBuilder().setPath(path).setContent(ByteString.copyFromUtf8(bytes))),
    ).build()

    @Test fun aFullSizeThatFailsGetsTheBigThumbnailAskedUntilItComes() {
        var asks = 0
        val dev = viewerDevice(full = { null }, thumbs = { p -> if (++asks <= 2) null else bigThumb(p, "big $p") })
        val waits = Waits()
        val vm = vm(dev, waits)
        start(vm)
        await("the photos") { vm.state.value.items.size == 3 }
        on { vm.open(1) }
        await("the big thumbnail") { vm.state.value.bigThumbs.containsKey("b") }
        assertEquals("big b", String(vm.state.value.bigThumbs.getValue("b")))
        // Asked without small_thumbnails, again after 1 s and 2 s (a grid's
        // backoff), never for the neighbours whose full size failed too.
        val asked = bigThumbsAsked(dev, "b")
        assertEquals(3, asked.size)
        asked.forEach { assertFalse(it.req.reqGetThumbnails.smallThumbnails) }
        assertTrue(listOf(1_000L, 2_000L).all { it in waits.asked })
        assertTrue(bigThumbsAsked(dev, "a").isEmpty())
        assertTrue(bigThumbsAsked(dev, "c").isEmpty())
        // The full size is asked for as a full size: not a screen's load.
        assertTrue(dev.of(ReqEnvelope.PayloadCase.REQ_GET_FILE).all { it.kind == null })
    }

    @Test fun aFullSizeThePhoneCantDecodeGetsTheBigThumbnail() {
        // An answer whose bytes aren't an image the phone decodes.
        val undecodable = RespEnvelope.newBuilder().setRespFile(File.newBuilder().setPath("/a").setContent(ByteString.copyFromUtf8("not an image"))).build()
        val dev = viewerDevice(full = { undecodable }, thumbs = { p -> bigThumb(p, "big $p") })
        val vm = vm(dev)
        start(vm)
        await("the photos") { vm.state.value.items.size == 3 }
        on { vm.open(0) }
        await("the big thumbnail") { vm.state.value.bigThumbs.containsKey("a") }
        assertEquals(1, bigThumbsAsked(dev, "a").size)
    }

    @Test fun theBigThumbnailIsAskedOnlyWhileTheViewerStaysOnTheItem() {
        val dev = viewerDevice(full = { null }, thumbs = { p -> if (p == "c") bigThumb(p, "big c") else null })
        val waits = Waits().apply { gate = CompletableDeferred() }
        val vm = vm(dev, waits)
        start(vm)
        await("the photos") { vm.state.value.items.size == 3 }
        on { vm.open(1) }
        await("the first ask's wait") { bigThumbsAsked(dev, "b").size == 1 && waits.asked.size == 1 }
        // Swiped on: b's is no longer asked for, c's is.
        on { vm.open(2) }
        await("c's") { vm.state.value.bigThumbs.containsKey("c") }
        val gate = waits.gate!!
        waits.gate = null
        gate.complete(Unit)
        settle()
        assertEquals(1, bigThumbsAsked(dev, "b").size)
        // Closed: nothing more is asked, and nothing kept.
        on { vm.closeModal() }
        val before = dev.of(ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS).size
        settle()
        assertEquals(before, dev.of(ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS).size)
        assertTrue(vm.state.value.bigThumbs.isEmpty())
    }

    @Test fun noBigThumbnailToHaveIsNotAskedAgain() {
        // A device before release 80 (no GetThumbnails), and one that leaves the path out.
        val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
        val leftOut = RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
        for (answer in listOf(unknown, leftOut)) {
            val dev = viewerDevice(full = { null }, thumbs = { answer })
            val waits = Waits()
            val vm = vm(dev, waits)
            start(vm)
            await("the photos") { vm.state.value.items.size == 3 }
            on { vm.open(0) }
            await("the ask") { bigThumbsAsked(dev, "a").isNotEmpty() }
            settle()
            assertEquals(1, bigThumbsAsked(dev, "a").size)
            assertTrue(waits.asked.isEmpty())
            assertTrue(vm.state.value.bigThumbs.isEmpty())
            on { vm.closeModal() }
        }
    }

    // ---- what the grid shows --------------------------------------------------------

    @Test fun theGridIsNeverBlank() {
        val s = PhotoGalleryViewModel.State()
        val items = listOf(PhotoGalleryViewModel.Item("1", "/a", "image/jpeg", 1, null))
        // A search started and nothing came yet (asked or about to be): grey tiles.
        assertEquals(GalleryBody.SKELETON, galleryBody(s))
        assertEquals(GalleryBody.SKELETON, galleryBody(s.copy(loading = true)))
        // The first page failed: why, even while it is asked again.
        assertEquals(GalleryBody.PROBLEM, galleryBody(s.copy(pageProblem = LoadProblem.OFFLINE)))
        assertEquals(GalleryBody.PROBLEM, galleryBody(s.copy(pageProblem = LoadProblem.SLOW, loading = true)))
        // Nothing there: the empty state.
        assertEquals(GalleryBody.EMPTY, galleryBody(s.copy(endReached = true)))
        // Photos: the grid, a later page's failure at its end.
        assertEquals(GalleryBody.PHOTOS, galleryBody(s.copy(items = items)))
        assertEquals(GalleryBody.PHOTOS, galleryBody(s.copy(items = items, pageProblem = LoadProblem.FAILED)))
        // The scrubber's grey tiles over everything.
        assertEquals(GalleryBody.GREY, galleryBody(s.copy(items = items, placeholderCount = 5)))
        assertEquals(GalleryBody.GREY, galleryBody(s.copy(pageProblem = LoadProblem.FAILED, placeholderCount = 5)))
    }

    @Test fun anEmptyGridSaysWhy() {
        val s = PhotoGalleryViewModel.State(endReached = true)
        val g = ImageGroup.newBuilder().setId("g").setName("Trip").build()
        assertEquals(EmptyKind.NO_PHOTOS, emptyKind(s))
        assertEquals(EmptyKind.COLLECTION, emptyKind(s.copy(activeGroup = g)))
        assertEquals(EmptyKind.NO_MATCH, emptyKind(s.copy(chips = listOf("beach"))))
        assertEquals(EmptyKind.NO_MATCH, emptyKind(s.copy(activeGroup = g, selectedPeople = listOf("p1"))))
    }
}
