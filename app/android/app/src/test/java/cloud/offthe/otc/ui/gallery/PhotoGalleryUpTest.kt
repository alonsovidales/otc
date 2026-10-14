// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.DelFile
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ImageGroups
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.ui.common.FIRST_PAGE_LIMIT_WITHOUT_THUMBS
import cloud.offthe.otc.ui.common.LoadProblem
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

// Issue #194: after a jump, the photos newer than its cutoff come above it -
// asked for only once the person scrolls up near the top, with a cursor of
// their own (newer_than, token, have) that never mixes with the downward
// one's. Issue #201: deleting a photo held at several paths, and
// the content each delete sends.
class PhotoGalleryUpTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }
    @Volatile private var signIns = 1

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private class Device(@Volatile var answer: suspend (ReqEnvelope) -> RespEnvelope?) {
        val sent = CopyOnWriteArrayList<ReqEnvelope>()
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            answer(req) ?: throw IOException("no answer")
        }
        fun searches(): List<SearchPhotos> = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS }.map { it.reqSearchPhotos }
        fun ups() = searches().filter { it.hasNewerThan() }
        fun downs() = searches().filter { !it.hasNewerThan() }
        fun deletes(): List<DelFile> = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_DEL_FILE }.map { it.reqDelFile }
    }

    private class Waits {
        val asked = CopyOnWriteArrayList<Long>()
        @Volatile var gate: CompletableDeferred<Unit>? = null
        val pause: suspend (Long) -> Unit = { ms -> asked += ms; gate?.await() }
    }

    private fun photo(path: String, at: String, copies: Int? = null): File {
        val t = java.time.Instant.parse(at)
        val b = File.newBuilder().setPath(path).setHash(hx(path)).setMime("image/jpeg").setCreated(Timestamp.newBuilder().setSeconds(t.epochSecond))
        if (copies != null) b.copies = copies
        return b.build()
    }
    private fun page(files: List<File>, token: String = "", oldestFirst: Boolean = false) =
        RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(files).setToken(token).setOldestFirst(oldestFirst)).build()
    private val noBuckets = RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
    private val noThumbs = RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
    private val ok = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
    private fun refused(code: String, message: String) = RespEnvelope.newBuilder().setError(true).setErrorCode(code).setErrorMessage(message).build()

    // The library's newest photo, the jump's month (March 2019) and the
    // months above it.
    private val newest = photo("new", "2026-10-01T10:00:00Z")
    private val old1 = photo("old1", "2019-03-20T10:00:00Z")
    private val old2 = photo("old2", "2019-03-10T10:00:00Z")
    private val apr1 = photo("apr1", "2019-04-02T10:00:00Z")
    private val apr2 = photo("apr2", "2019-04-20T10:00:00Z")
    private val may1 = photo("may1", "2019-05-05T10:00:00Z")

    /**
     * A device: a plain search answers [newest]; the jump [down] (its token
     * [downToken]); a later downward page [more]; the upward search
     * [upPages] one after another (null: no answer) - or, [old], an
     * ordinary page as a device before #194 does.
     */
    private fun device(
        down: List<File> = listOf(old1, old2), downToken: String = "",
        upPages: MutableList<RespEnvelope?> = mutableListOf(), more: List<File> = emptyList(),
        del: suspend (DelFile) -> RespEnvelope = { ok },
    ) = Device { req ->
        when (req.payloadCase) {
            ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> noBuckets
            ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> noThumbs
            ReqEnvelope.PayloadCase.REQ_DEL_FILE -> del(req.reqDelFile)
            ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> {
                val sp = req.reqSearchPhotos
                when {
                    sp.hasNewerThan() -> if (upPages.isEmpty()) page(emptyList(), oldestFirst = true) else upPages.removeAt(0)
                    sp.hasBefore() -> page(down, downToken)
                    sp.token.isNotEmpty() -> page(more)
                    else -> page(listOf(newest))
                }
            }
            else -> null
        }
    }

    private fun vm(dev: Device, waits: Waits = Waits()) = PhotoGalleryViewModel(
        "test", send = dev.send, pause = waits.pause, online = { true }, problemOf = { _, _ -> LoadProblem.FAILED },
        thumbs = thumbs, scope = scope, signIns = { signIns },
    )

    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    private fun settle() { repeat(5) { on { kotlinx.coroutines.yield() }; Thread.sleep(10) } }

    private fun paths(vm: PhotoGalleryViewModel) = vm.state.value.items.map { it.path }

    // Opened, then the scrubber's jump to March 2019: its photos land.
    private fun jumped(dev: Device): PhotoGalleryViewModel {
        val vm = vm(dev)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the newest photos") { paths(vm) == listOf("new") }
        on { vm.jumpToDate("2019-03") }
        await("the jump's photos") { vm.state.value.jumpsLanded == 1 && !vm.state.value.loading }
        return vm
    }

    private val cutoff get() = jumpCutoffMs("2019-03", ZoneId.systemDefault())!!

    // The grid scrolled up with the row of its first photos (keyed [key]) drawn.
    private fun greyShown(vm: PhotoGalleryViewModel, key: String = "2019-03#0") = on { vm.rowShown(key, 0, 1); vm.scrolledUp() }

    @Test fun newerPhotosAreAskedForOnTheFirstScrollUpNearTheTop() {
        val ups = mutableListOf<RespEnvelope?>(page(listOf(apr1, apr2), token = "u1", oldestFirst = true))
        val dev = device(upPages = ups)
        val vm = jumped(dev)
        assertTrue(vm.state.value.upOn)
        // The jump lands at the top, its own photos drawn: nothing asked yet.
        on { vm.rowShown("2019-03#0", 0, 2) }
        settle()
        assertTrue(dev.ups().isEmpty())
        // Scrolled up away from the top (rows further down drawn): not yet.
        on { vm.rowGone("2019-03#0"); vm.rowShown("2019-01#3", UP_NEAR, UP_NEAR + 3); vm.scrolledUp() }
        settle()
        assertTrue(dev.ups().isEmpty())
        // The first photos come back on screen: asked.
        on { vm.rowShown("2019-03#0", 0, 2) }
        await("the newer photos") { paths(vm) == listOf("apr2", "apr1", "old1", "old2") }
        val up = dev.ups().single()
        // The jump's own instant, the same filters, a first page's size - never `before`.
        assertEquals(Math.floorDiv(cutoff, 1000L), up.newerThan.seconds)
        assertFalse(up.hasBefore())
        assertEquals("", up.token)
        assertEquals(0, up.have)
        assertEquals(FIRST_PAGE_LIMIT_WITHOUT_THUMBS, up.limit)
        assertTrue(up.smallThumbnails && up.omitThumbnails)
        val st = vm.state.value
        assertEquals(listOf(true, true, false, false), st.items.map { it.up })
        assertEquals(1, st.upLanded)
        assertFalse(st.upLoading)
        assertTrue(st.upOn)
    }

    @Test fun theNextPageAboveGoesOnWithItsOwnTokenAndHaveUntilTheTop() {
        val ups = mutableListOf<RespEnvelope?>(
            page(listOf(apr1, apr2), token = "u1", oldestFirst = true),
            // The device served apr2 again (its oldest copy changed meanwhile): shown once.
            page(listOf(apr2, may1), token = "", oldestFirst = true),
        )
        val dev = device(upPages = ups)
        val vm = jumped(dev)
        greyShown(vm)
        await("the first page above") { vm.state.value.upLanded == 1 }
        // Drawn again after it landed (the grid's rows re-register), still near the top.
        on { vm.rowShown("2019-04#0", 0, 2) }
        await("the second page above") { vm.state.value.upLanded == 2 }
        assertEquals(listOf("may1", "apr2", "apr1", "old1", "old2"), paths(vm))
        val second = dev.ups()[1]
        assertEquals("u1", second.token)
        assertEquals(2, second.have)
        assertEquals(NEXT_PAGE_LIMIT_WITHOUT_THUMBS, second.limit)
        // Token "": the newest photo is in - no room above, nothing more asked.
        assertFalse(vm.state.value.upOn)
        greyShown(vm, "2019-06#0")
        settle()
        assertEquals(2, dev.ups().size)
    }

    @Test fun anEmptyPageAboveIsTheTop() {
        val dev = device(upPages = mutableListOf(page(emptyList(), token = "u1", oldestFirst = true)))
        val vm = jumped(dev)
        greyShown(vm)
        await("the answer") { !vm.state.value.upOn }
        assertEquals(listOf("old1", "old2"), paths(vm))
    }

    @Test fun aDeviceBeforeNewerThanIsNotAskedAgainUntilTheNextSignIn() {
        // It ignores newer_than: an ordinary newest-first page.
        val dev = device(upPages = mutableListOf(page(listOf(newest), token = "x")))
        val vm = jumped(dev)
        greyShown(vm)
        await("the answer") { !vm.state.value.upOn && !vm.state.value.upLoading }
        // Dropped, not shown above the jump.
        assertEquals(listOf("old1", "old2"), paths(vm))
        assertNull(vm.state.value.upProblem)
        // Another jump on the same sign-in: no room above, nothing asked.
        on { vm.jumpToDate("2019-03") }
        await("the jump") { vm.state.value.jumpsLanded == 2 && !vm.state.value.loading }
        assertFalse(vm.state.value.upOn)
        greyShown(vm)
        settle()
        assertEquals(1, dev.ups().size)
        // Signed in again (maybe to an updated device): asked again.
        signIns = 2
        on { vm.jumpToDate("2019-03") }
        await("the jump") { vm.state.value.jumpsLanded == 3 && !vm.state.value.loading }
        assertTrue(vm.state.value.upOn)
        greyShown(vm)
        await("asked again") { dev.ups().size == 2 }
    }

    @Test fun aFailedPageAboveIsAskedAgainWithTheSameTokenAndHave() {
        val ups = mutableListOf<RespEnvelope?>(
            page(listOf(apr1, apr2), token = "u1", oldestFirst = true),
            null, // no answer
            // An error answer: a failed page too, never an old device.
            RespEnvelope.newBuilder().setError(true).setErrorMessage("internal error").build(),
            page(listOf(may1), token = "", oldestFirst = true),
        )
        val dev = device(upPages = ups)
        val waits = Waits().apply { gate = CompletableDeferred() }
        val vm = PhotoGalleryViewModel(
            "test", send = dev.send, pause = waits.pause, online = { true }, problemOf = { _, _ -> LoadProblem.UNREACHABLE },
            thumbs = thumbs, scope = scope, signIns = { signIns },
        )
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the newest photos") { paths(vm) == listOf("new") }
        on { vm.jumpToDate("2019-03") }
        await("the jump") { vm.state.value.jumpsLanded == 1 && !vm.state.value.loading }
        greyShown(vm)
        await("the first page above") { vm.state.value.upLanded == 1 }
        greyShown(vm, "2019-05#0")
        await("the failure") { vm.state.value.upProblem != null && !vm.state.value.upLoading }
        assertEquals(LoadProblem.UNREACHABLE, vm.state.value.upProblem)
        // Waiting for its retry, a grey tile drawn asks nothing more.
        greyShown(vm, "2019-05#1")
        settle()
        assertEquals(2, dev.ups().size)
        // Try again: the same page, then the error answer, then a Wake.
        on { vm.retryUp() }
        await("the error answer") { dev.ups().size == 3 && vm.state.value.upProblem != null && !vm.state.value.upLoading }
        assertTrue(vm.state.value.upOn)
        on { vm.wake() }
        await("the page") { vm.state.value.upLanded == 2 }
        assertNull(vm.state.value.upProblem)
        dev.ups().drop(1).forEach { assertEquals("u1", it.token); assertEquals(2, it.have) }
        assertEquals(listOf("may1", "apr2", "apr1", "old1", "old2"), paths(vm))
    }

    @Test fun eachSearchCountsOnlyWhatItBroughtAndWhatWasDeletedOfIt() {
        val ups = mutableListOf<RespEnvelope?>(page(listOf(apr1, apr2), token = "u1", oldestFirst = true), page(listOf(may1), token = "", oldestFirst = true))
        val dev = device(down = listOf(old1, old2), downToken = "d1", upPages = ups, more = listOf(photo("old3", "2019-02-01T10:00:00Z")))
        val vm = jumped(dev)
        greyShown(vm)
        await("the page above") { vm.state.value.upLanded == 1 }
        // An upward photo and a downward one deleted.
        on { vm.toggleSelect("apr1"); vm.toggleSelect("old2") }
        on { vm.deleteSelected() }
        await("the deletes") { paths(vm) == listOf("apr2", "old1") }
        // Scrolled to the end: the downward search's own count (2 brought, 1 deleted), never the grid's.
        on { vm.loadMoreIfNeeded(1) }
        await("the next page below") { paths(vm).last() == "old3" }
        val next = dev.downs().last()
        assertEquals("d1", next.token)
        assertEquals(1, next.have)
        assertFalse(next.hasNewerThan())
        // And the upward one's: 2 brought, 1 deleted.
        greyShown(vm, "2019-05#0")
        await("the page above") { vm.state.value.upLanded == 2 }
        assertEquals("u1", dev.ups().last().token)
        assertEquals(1, dev.ups().last().have)
    }

    @Test fun aScrollUpWhileTheScrubberIsDraggedArmsNothing() {
        val dev = device(upPages = mutableListOf(page(listOf(apr1), token = "", oldestFirst = true)))
        val vm = jumped(dev)
        // The scrubber dragged (it scrolls the grid to its top itself).
        on { vm.setScrubFrac(0.5f) }
        greyShown(vm)
        settle()
        assertTrue(dev.ups().isEmpty())
        on { vm.setScrubFrac(null) }
        // Not armed by that: the first photos drawn again ask nothing.
        on { vm.rowShown("2019-03#0", 0, 2) }
        settle()
        assertTrue(dev.ups().isEmpty())
        // The person's own scroll up does.
        greyShown(vm)
        await("the newer photos") { dev.ups().size == 1 }
    }

    @Test fun aNewJumpWaitsForTheNextScrollUp() {
        val dev = device(upPages = mutableListOf(page(listOf(apr1), token = "u1", oldestFirst = true)))
        val vm = jumped(dev)
        greyShown(vm)
        await("the page above") { vm.state.value.upLanded == 1 }
        on { vm.jumpToDate("2019-03") }
        await("the jump") { vm.state.value.jumpsLanded == 2 && !vm.state.value.loading }
        // It lands at the top: its first photos drawn ask nothing.
        on { vm.rowShown("2019-03#0", 0, 2) }
        settle()
        assertEquals(1, dev.ups().size)
        greyShown(vm)
        await("asked again") { dev.ups().size == 2 }
        // A search of its own: from its start.
        assertEquals("", dev.ups()[1].token)
        assertEquals(0, dev.ups()[1].have)
    }

    @Test fun aNewSearchDropsTheCursorAbove() {
        val dev = device(upPages = mutableListOf(page(listOf(apr1), token = "u1", oldestFirst = true)))
        val vm = jumped(dev)
        greyShown(vm)
        await("the page above") { vm.state.value.upLanded == 1 }
        on { vm.addChip("beach") }
        await("the search") { paths(vm) == listOf("new") && !vm.state.value.loading }
        assertFalse(vm.state.value.upOn)
        greyShown(vm)
        settle()
        assertEquals(1, dev.ups().size)
    }

    // ---- issue #201: copies -----------------------------------------------------------

    private fun item(path: String, copies: Int?, hash: String = hx(path)) = PhotoGalleryViewModel.Item("$path#$hash", path, "image/jpeg", 1, null, hash = hash, copies = copies)

    @Test fun deletingAPhotoHeldAtSeveralPathsAsksWhichToDelete() {
        assertEquals(CopiesAsk.SINGLE, copiesAsk(listOf(item("a", 3))))
        assertEquals(CopiesAsk.PLAIN, copiesAsk(listOf(item("a", 1))))
        // A device that doesn't count them: never "every copy".
        assertEquals(CopiesAsk.PLAIN, copiesAsk(listOf(item("a", null))))
        assertEquals(CopiesAsk.SEVERAL, copiesAsk(listOf(item("a", 1), item("b", 2))))
        assertEquals(CopiesAsk.PLAIN, copiesAsk(listOf(item("a", 1), item("b", null))))
        // Two picked, one no longer on the grid: of the one shown, its copies.
        assertEquals(CopiesAsk.SEVERAL, copiesAsk(listOf(item("a", 4)), count = 2))
        assertEquals(2, withCopies(listOf(item("a", 2), item("b", 1), item("c", 5), item("d", null))))
    }

    @Test fun aDeleteSendsTheContentShownAndEveryCopyOnlyWhereTheDeviceCountedThem() {
        val a = delFileFor(item("a", 3), everyCopy = true)
        assertEquals("a", a.path)
        assertEquals(hx("a"), a.hash)
        assertTrue(a.hasEveryCopy() && a.everyCopy)
        // Only this copy: the flag stays unset.
        assertFalse(delFileFor(item("a", 3), everyCopy = false).hasEveryCopy())
        // One copy, or none counted: a plain delete, whatever was asked.
        assertFalse(delFileFor(item("b", 1), everyCopy = true).hasEveryCopy())
        assertFalse(delFileFor(item("c", null), everyCopy = true).hasEveryCopy())
        assertEquals(hx("c"), delFileFor(item("c", null), everyCopy = true).hash)
        // No hash known: none sent.
        assertFalse(delFileFor(item("d", 2, hash = ""), everyCopy = false).hasHash())
    }

    @Test fun everyCopyGoesOnlyWithThePhotosThatHaveCopiesAndARefusalKeepsTheTile() {
        val a = photo("a", "2026-10-03T10:00:00Z", copies = 3)
        val b = photo("b", "2026-10-02T10:00:00Z", copies = 1)
        val c = photo("c", "2026-10-01T10:00:00Z", copies = 2)
        val dev = Device { req ->
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> noBuckets
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> noThumbs
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> page(listOf(a, b, c))
                ReqEnvelope.PayloadCase.REQ_DEL_FILE ->
                    if (req.reqDelFile.path == "c") refused("copy_upload_only", "a copy is in the upload-only folder /Backups: no copy was deleted") else ok
                else -> null
            }
        }
        val vm = vm(dev)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the photos") { paths(vm) == listOf("a", "b", "c") }
        assertEquals(listOf(3, 1, 2), vm.state.value.items.map { it.copies })
        on { vm.toggleSelect("a"); vm.toggleSelect("b"); vm.toggleSelect("c") }
        on { vm.deleteSelected(everyCopy = true) }
        await("the deletes") { dev.deletes().size == 3 && paths(vm) == listOf("c") }
        val sent = dev.deletes().associateBy { it.path }
        assertTrue(sent.getValue("a").everyCopy)
        assertFalse(sent.getValue("b").hasEveryCopy())
        assertTrue(sent.getValue("c").everyCopy)
        assertEquals(listOf(hx("a"), hx("b"), hx("c")), listOf("a", "b", "c").map { sent.getValue(it).hash })
        // Nothing of "c" was deleted: it stays, picked, and the device says why.
        assertEquals(setOf("c"), vm.state.value.selected)
        assertEquals("Delete failed: a copy is in the upload-only folder /Backups: no copy was deleted", vm.state.value.alert?.english())
    }

    @Test fun contentChangedSinceShownDeletesNothingAndAsksForThePhotosAgain() {
        var searches = 0
        val dev = Device { req ->
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> noBuckets
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> noThumbs
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> { searches++; page(listOf(photo("a", "2026-10-03T10:00:00Z", copies = 2))) }
                ReqEnvelope.PayloadCase.REQ_DEL_FILE -> refused("changed", "this file has changed since it was shown, so nothing was deleted: refresh and try again")
                else -> null
            }
        }
        val vm = vm(dev)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the photo") { paths(vm) == listOf("a") }
        on { vm.open(0) }
        on { vm.deleteCurrentPhoto(everyCopy = true) }
        await("asked again") { searches == 2 && paths(vm) == listOf("a") }
        assertTrue(dev.deletes().single().everyCopy)
        assertNull(vm.state.value.openIndex)
        assertEquals("Delete failed: this file has changed since it was shown, so nothing was deleted: refresh and try again", vm.state.value.alert?.english())
    }

    @Test fun changedRefusalsAfterAJumpReloadAtTheJumpKeepThePicksAndAreSummedUp() {
        val dev = device(del = { refused("changed", "this file has changed since it was shown, so nothing was deleted: refresh and try again") })
        val vm = jumped(dev)
        val bucketsBefore = dev.sent.count { it.payloadCase == ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS }
        on { vm.toggleSelect("old1"); vm.toggleSelect("old2") }
        on { vm.deleteSelected() }
        await("the jump again") { vm.state.value.jumpsLanded == 2 && !vm.state.value.loading }
        // At the jump's month again, never from the newest photo.
        assertEquals(listOf("old1", "old2"), paths(vm))
        val starts = dev.downs().filter { it.token.isEmpty() }
        assertEquals(1, starts.count { !it.hasBefore() })
        assertEquals(listOf(Math.floorDiv(cutoff, 1000L), Math.floorDiv(cutoff, 1000L)), starts.filter { it.hasBefore() }.map { it.before.seconds })
        // Refused: still picked, and the two said as one.
        assertEquals(setOf("old1", "old2"), vm.state.value.selected)
        assertEquals("2 items changed on your device since they were shown, so they weren't deleted. Check them and try again.", vm.state.value.alert?.english())
        assertTrue(vm.state.value.alertMore.isEmpty())
        // The months asked for again; the newer photos above start over, on the next scroll up.
        await("the buckets") { dev.sent.count { it.payloadCase == ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS } > bucketsBefore }
        assertTrue(vm.state.value.upOn)
        assertTrue(dev.ups().isEmpty())
    }

    @Test fun aDeleteWithAChangedRefusalStillRefreshesTheCollectionsCount() {
        val g = ImageGroup.newBuilder().setId("g1").setName("Trip").setFileCount(3).build()
        val groupsAsked = java.util.concurrent.atomic.AtomicInteger()
        val dev = Device { req ->
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> noBuckets
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> noThumbs
                ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS -> {
                    groupsAsked.incrementAndGet()
                    RespEnvelope.newBuilder().setRespImageGroups(ImageGroups.newBuilder().addGroups(g.toBuilder().setFileCount(2))).build()
                }
                ReqEnvelope.PayloadCase.REQ_DEL_FILE ->
                    if (req.reqDelFile.path == "old2") refused("changed", "this file has changed since it was shown, so nothing was deleted: refresh and try again") else ok
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> if (req.reqSearchPhotos.hasBefore()) page(listOf(old1, old2)) else page(listOf(newest))
                else -> null
            }
        }
        val vm = vm(dev)
        on { vm.openGroup(g) }
        await("the collection") { paths(vm) == listOf("new") && !vm.state.value.loading }
        on { vm.jumpToDate("2019-03") }
        await("the jump") { vm.state.value.jumpsLanded == 1 && !vm.state.value.loading }
        on { vm.toggleSelect("old1"); vm.toggleSelect("old2") }
        on { vm.deleteSelected() }
        await("the jump again and the count") { vm.state.value.jumpsLanded == 2 && !vm.state.value.loading && vm.state.value.activeGroup?.fileCount == 2 }
        assertEquals(1, groupsAsked.get())
        // One refusal: the device's own words.
        assertEquals("Delete failed: this file has changed since it was shown, so nothing was deleted: refresh and try again", vm.state.value.alert?.english())
        assertEquals(setOf("old2"), vm.state.value.selected)
    }

    @Test fun refusalsAreSummedUpPerKindFromTwoOn() {
        val changed = DeleteRefusal("changed", "this file has changed since it was shown")
        val copy = DeleteRefusal("copy_upload_only", "a copy is in the upload-only folder /Backups: no copy was deleted")
        val locked = DeleteRefusal("upload_only", "/Backups is upload-only: nothing in it can be deleted")
        val noAnswer = DeleteRefusal("", "timed out")
        fun notes(vararg r: DeleteRefusal) = deleteRefusalNotes(r.toList()).map { it.english() }
        assertEquals(emptyList<String>(), notes())
        // One: its own words, whatever its kind.
        assertEquals(listOf("Delete failed: this file has changed since it was shown"), notes(changed))
        assertEquals(listOf("Delete failed: a copy is in the upload-only folder /Backups: no copy was deleted"), notes(copy))
        // Two or more: counted per kind.
        assertEquals(listOf("2 items changed on your device since they were shown, so they weren't deleted. Check them and try again."), notes(changed, changed))
        assertEquals(listOf("3 items weren't deleted: they have copies in an upload-only folder"), notes(copy, copy, copy))
        assertEquals(
            listOf(
                "1 item changed on your device since it was shown, so it wasn't deleted. Check it and try again.",
                "1 item wasn't deleted: a copy of it is in an upload-only folder",
            ),
            notes(copy, changed),
        )
        // Any other kind in the device's words, the last of them.
        assertEquals(
            listOf("2 items changed on your device since they were shown, so they weren't deleted. Check them and try again.", "Delete failed: timed out"),
            notes(changed, locked, changed, noAnswer),
        )
        assertEquals(listOf("Delete failed: timed out"), notes(locked, noAnswer))
    }

    @Test fun theViewersDeleteOfOnlyThisCopyIsAPlainDeleteWithItsHash() {
        val dev = device()
        val vm = vm(dev)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the photo") { paths(vm) == listOf("new") }
        on { vm.open(0) }
        on { vm.deleteCurrentPhoto() }
        await("the delete") { paths(vm).isEmpty() }
        val d = dev.deletes().single()
        assertEquals("new", d.path)
        assertEquals(hx("new"), d.hash)
        assertFalse(d.hasEveryCopy())
    }

    @Test fun anOriginalIsSavedWithTheVideosOrThePictures() {
        assertEquals("Movies/Off The Cloud", saveFolder("video/mp4"))
        assertEquals("Pictures/Off The Cloud", saveFolder("image/heic"))
        assertEquals("Pictures/Off The Cloud", saveFolder("image/jpeg"))
    }
}
