// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ImageGroups
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.PhotoRating
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespInstanceRole
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.TestThumbStore
import cloud.offthe.otc.ui.common.hx
import com.google.protobuf.Timestamp
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
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
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

// A collection's Rating in Images: the header's switch, the tiles' ratings
// from the pages, Sort by and Show (ranked pages), and the viewer's stars and
// comment - saved at once, put back when refused, a comment saved on moving
// on. The iPhone app follows the same rules.
class PhotoGalleryRatingTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun photo(path: String, at: String): File {
        val t = java.time.Instant.parse(at)
        return File.newBuilder().setPath(path).setHash(hx(path)).setMime("image/jpeg").setCreated(Timestamp.newBuilder().setSeconds(t.epochSecond)).build()
    }
    private val a = photo("a", "2026-10-05T10:00:00Z")
    private val b = photo("b", "2026-10-04T10:00:00Z")
    private val c = photo("c", "2026-10-03T10:00:00Z")
    private val group = ImageGroup.newBuilder().setId("g1").setName("Wedding").setFileCount(3).build()

    private val ok = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
    private val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
    private val demo = RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo: nothing can be changed.").build()

    /**
     * A device with one collection of a, b and c, whose Rating is [on] and
     * whose photos' ratings are [ratings]. [toggle]/[rate]: what
     * SetImageGroupRating/SetPhotoRating answer instead (null: as a device
     * with Rating does); [hold]: SetPhotoRating waits for it.
     */
    private inner class Device(@Volatile var on: Boolean, ratings: Map<String, PhotoRating> = emptyMap(), val toggle: RespEnvelope? = null, @Volatile var rate: RespEnvelope? = null) {
        val sent = CopyOnWriteArrayList<ReqEnvelope>()
        val stored = ConcurrentHashMap(ratings)
        @Volatile var hold: CompletableDeferred<Unit>? = null
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS -> RespEnvelope.newBuilder().setRespImageGroups(ImageGroups.newBuilder().addGroups(group.toBuilder().setRating(on))).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(page(req.reqSearchPhotos))).build()
                ReqEnvelope.PayloadCase.REQ_SET_IMAGE_GROUP_RATING -> {
                    val answer = toggle ?: ok
                    if (answer == ok) on = req.reqSetImageGroupRating.on
                    answer
                }
                ReqEnvelope.PayloadCase.REQ_SET_PHOTO_RATING -> {
                    hold?.await()
                    val r = req.reqSetPhotoRating
                    rate ?: run {
                        val kept = PhotoRating.newBuilder().setStars(r.stars).setComment(r.comment).build()
                        if (r.stars == 0 && r.comment.isEmpty()) stored.remove(r.path) else stored[r.path] = kept
                        RespEnvelope.newBuilder().setRespPhotoRating(kept).build()
                    }
                }
                ReqEnvelope.PayloadCase.REQ_GET_INSTANCE_ROLE -> RespEnvelope.newBuilder().setRespInstanceRole(RespInstanceRole.newBuilder().setIsPrimary(true)).build()
                else -> throw IOException("not answered")
            }
        }

        // The collection's photos as the device would list them: rated while
        // its Rating is on, filtered and sorted by the stars when asked.
        private fun page(sp: SearchPhotos): List<File> {
            var files = listOf(a, b, c).map { f -> stored[f.path]?.takeIf { on }?.let { f.toBuilder().setRating(it).build() } ?: f }
            if (on && sp.hasRating()) {
                val r = sp.rating
                files = files.filter { f ->
                    val stars = stored[f.path]?.stars ?: 0
                    when {
                        r.unrated -> stars == 0
                        r.withComment -> stored[f.path]?.comment?.isNotEmpty() == true
                        else -> stars >= r.minStars
                    }
                }
            }
            if (on && sp.byRating) files = files.sortedByDescending { stored[it.path]?.stars ?: 0 }
            return files
        }

        fun searches() = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS }.map { it.reqSearchPhotos }
        fun rates() = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_SET_PHOTO_RATING }.map { it.reqSetPhotoRating }
        fun toggles() = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_SET_IMAGE_GROUP_RATING }.map { it.reqSetImageGroupRating }
    }

    @Volatile private var firstHint = true

    private fun vm(dev: Device) = PhotoGalleryViewModel(
        "test", send = dev.send, pause = {}, online = { true }, problemOf = { _, _ -> LoadProblem.FAILED },
        thumbs = thumbs, fullPhotos = thumbs, scope = scope, signIns = { 1 },
        ratingHintFirst = { firstHint.also { firstHint = false } },
    )

    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    private fun settle() { Thread.sleep(50); on { } }

    private fun paths(vm: PhotoGalleryViewModel) = vm.state.value.items.map { it.path }
    private fun stars(vm: PhotoGalleryViewModel, path: String) = vm.state.value.ratings[path]?.stars ?: 0

    private val four = PhotoRating.newBuilder().setStars(4).build()
    private val two = PhotoRating.newBuilder().setStars(2).setComment("Crop it").setFromLink(true).build()

    // The collection open, its photos in.
    private fun opened(dev: Device): PhotoGalleryViewModel {
        val vm = vm(dev)
        on { vm.openGroup(group.toBuilder().setRating(dev.on).build()) }
        await("the collection") { paths(vm) == listOf("a", "b", "c") && !vm.state.value.loading }
        return vm
    }

    @Test fun theTilesHaveTheirRatingsFromThePages() {
        val dev = Device(on = true, ratings = mapOf("a" to four, "b" to two))
        val vm = opened(dev)
        assertEquals(4, stars(vm, "a"))
        assertEquals(two, vm.state.value.ratings["b"])
        assertNull(vm.state.value.ratings["c"])
        assertTrue(vm.state.value.rates)
        // By date, every photo: no rating asked for, months as ever.
        val asked = dev.searches().last()
        assertFalse(asked.hasRating())
        assertFalse(asked.byRating)
        assertTrue(vm.state.value.dateOrdered)
    }

    @Test fun sortAndShowAskForRankedPages() {
        val dev = Device(on = true, ratings = mapOf("a" to two, "b" to four))
        val vm = opened(dev)
        on { vm.setRatingSort(RatingSort.RATING) }
        await("by the stars") { paths(vm) == listOf("b", "a", "c") }
        assertTrue(dev.searches().last().byRating)
        assertFalse(vm.state.value.dateOrdered)
        on { vm.setRatingShow(RatingShow.THREE) }
        await("three or more") { paths(vm) == listOf("b") }
        val asked = dev.searches().last()
        assertEquals(3, asked.rating.minStars)
        assertTrue(asked.byRating)
        // None left: its own empty state, and the way back.
        on { vm.setRatingShow(RatingShow.FIVE) }
        await("none") { vm.state.value.items.isEmpty() && vm.state.value.endReached && !vm.state.value.loading }
        assertEquals(EmptyKind.RATING, emptyKind(vm.state.value))
        on { vm.setRatingShow(RatingShow.ALL) }
        await("all again") { paths(vm).size == 3 }
        // Another collection opens by date, every photo.
        on { vm.openGroup(group.toBuilder().setRating(true).build()) }
        assertEquals(RatingSort.DATE, vm.state.value.ratingSort)
        assertEquals(RatingShow.ALL, vm.state.value.ratingShow)
    }

    @Test fun withRatingOffNothingIsSortedOrFiltered() {
        val dev = Device(on = false, ratings = mapOf("a" to four))
        val vm = opened(dev)
        on { vm.setRatingSort(RatingSort.RATING); vm.setRatingShow(RatingShow.FIVE) }
        settle()
        assertEquals(RatingSort.DATE, vm.state.value.ratingSort)
        assertEquals(RatingShow.ALL, vm.state.value.ratingShow)
        assertFalse(dev.searches().any { it.hasRating() || it.byRating })
        assertFalse(vm.state.value.rates)
    }

    @Test fun theSwitchTurnsRatingOnAndTheRatingsCome() {
        val dev = Device(on = false, ratings = mapOf("a" to four))
        val vm = opened(dev)
        assertTrue(vm.state.value.ratings.isEmpty())
        on { vm.toggleRating() }
        await("on") { vm.state.value.activeGroup?.rating == true && stars(vm, "a") == 4 }
        assertEquals(listOf(true), dev.toggles().map { it.on })
        assertEquals("g1", dev.toggles().single().id)
        assertTrue(vm.state.value.groups.isEmpty() || vm.state.value.groups.all { it.rating })
        // The first time, what it does.
        assertEquals("Rating on: rate photos with stars and comments", vm.state.value.gridNote?.english())
        on { vm.gridNoteShown() }
        // Off: sorted by stars before, the order goes and the photos come by date.
        on { vm.setRatingSort(RatingSort.RATING) }
        await("ranked") { !vm.state.value.dateOrdered && !vm.state.value.loading }
        val before = dev.searches().size
        on { vm.toggleRating() }
        await("off") { vm.state.value.activeGroup?.rating == false && dev.searches().size > before && !vm.state.value.loading }
        assertEquals(RatingSort.DATE, vm.state.value.ratingSort)
        assertTrue(vm.state.value.dateOrdered)
        assertFalse(dev.searches().last().byRating)
        // On again: no note the second time.
        on { vm.toggleRating() }
        await("on again") { vm.state.value.activeGroup?.rating == true }
        assertNull(vm.state.value.gridNote)
    }

    @Test fun anOlderDeviceNeedsAnUpdate() {
        val dev = Device(on = false, toggle = unknown)
        val vm = opened(dev)
        on { vm.toggleRating() }
        await("the question") { vm.state.value.needsUpdate != null }
        assertEquals("Your device needs an update to rate photos.", vm.state.value.needsUpdate!!.title.english())
        assertEquals(false, vm.state.value.activeGroup?.rating)
        assertFalse(vm.state.value.ratingBusy)
    }

    @Test fun aRefusedSwitchSaysTheDevicesWords() {
        val dev = Device(on = false, toggle = demo)
        val vm = opened(dev)
        on { vm.toggleRating() }
        await("the alert") { vm.state.value.alert != null }
        assertEquals("This is a demo: nothing can be changed.", vm.state.value.alert!!.english())
        assertEquals(false, vm.state.value.activeGroup?.rating)
    }

    @Test fun ratingTurnedOnElsewhereIsPickedUpWhenListed() {
        val dev = Device(on = false, ratings = mapOf("b" to four))
        val vm = opened(dev)
        dev.on = true
        on { vm.loadGroups() }
        await("rated") { vm.state.value.rates && stars(vm, "b") == 4 }
    }

    @Test fun starsSaveAtOnceAndATapOnTheSameNumberTakesThemOff() {
        val dev = Device(on = true, ratings = mapOf("b" to two))
        val vm = opened(dev)
        on { vm.open(0) }
        assertTrue(vm.state.value.viewerRates)
        dev.hold = CompletableDeferred()
        on { vm.rateOpen(4) }
        // Shown before the device answered.
        assertEquals(4, stars(vm, "a"))
        await("the request") { dev.rates().size == 1 }
        assertEquals(listOf(4), dev.rates().map { it.stars })
        assertEquals("g1", dev.rates().single().groupId)
        assertEquals("a", dev.rates().single().path)
        dev.hold!!.complete(Unit)
        await("kept") { dev.stored["a"]?.stars == 4 }
        on { vm.rateOpen(4) }
        await("cleared") { dev.stored["a"] == null }
        assertNull(vm.state.value.ratings["a"])
        // The comment is kept with the stars: b's goes with its new stars.
        on { vm.open(1) }
        on { vm.rateOpen(5) }
        await("b's") { dev.stored["b"]?.stars == 5 }
        assertEquals("Crop it", dev.rates().last().comment)
        // The owner's now, not the link's.
        await("shown") { vm.state.value.ratings["b"]?.fromLink == false }
    }

    @Test fun aRefusalPutsTheStarsBackAndSaysWhy() {
        val dev = Device(on = true, ratings = mapOf("a" to four))
        val vm = opened(dev)
        on { vm.open(0) }
        dev.rate = demo
        on { vm.rateOpen(2) }
        await("the note") { vm.state.value.ratingNote != null }
        assertEquals("This is a demo: nothing can be changed.", vm.state.value.ratingNote!!.english())
        assertEquals(4, stars(vm, "a"))
        // No answer: ours.
        dev.rate = null
        val hold = CompletableDeferred<Unit>()
        dev.hold = hold
        on { vm.ratingNoteShown() }
        on { vm.rateOpen(1) }
        assertEquals(1, stars(vm, "a"))
        hold.complete(Unit)
        await("kept") { dev.stored["a"]?.stars == 1 }
    }

    @Test fun aCommentIsSavedWithSaveOrOnMovingOn() {
        val dev = Device(on = true, ratings = mapOf("a" to four))
        val vm = opened(dev)
        on { vm.open(0) }
        on { vm.editRatingComment("  Warmer, please ") }
        assertEquals("  Warmer, please ", vm.state.value.ratingDraft?.text)
        on { vm.saveRatingComment() }
        await("saved") { dev.stored["a"]?.comment == "Warmer, please" }
        assertEquals(4, dev.stored["a"]?.stars)
        // Unchanged: nothing more to send.
        val n = dev.rates().size
        on { vm.saveRatingComment() }
        settle()
        assertEquals(n, dev.rates().size)
        // Written and not saved: the next photo saves it.
        on { vm.editRatingComment("Darker") }
        on { vm.next() }
        await("saved on moving on") { dev.stored["a"]?.comment == "Darker" }
        assertNull(vm.state.value.ratingDraft)
        // And closing the viewer.
        on { vm.editRatingComment("Sharper") }
        on { vm.closeModal() }
        await("saved on closing") { dev.stored["b"]?.comment == "Sharper" }
        assertEquals(0, dev.stored["b"]?.stars)
    }

    @Test fun aTileTappedWithACommentInTheBoxSendsItWithTheStars() {
        val dev = Device(on = true)
        val vm = opened(dev)
        on { vm.open(2) }
        on { vm.editRatingComment("Love it") }
        on { vm.rateOpen(5) }
        await("both") { dev.stored["c"]?.stars == 5 && dev.stored["c"]?.comment == "Love it" }
    }

    @Test fun aRankedGridIsAskedAgainAsTheViewerCloses() {
        val dev = Device(on = true, ratings = mapOf("a" to four, "b" to two))
        val vm = opened(dev)
        on { vm.setRatingShow(RatingShow.RATED) }
        await("rated ones") { paths(vm) == listOf("a", "b") }
        on { vm.open(0) }
        on { vm.rateOpen(4) } // a: 4 -> none
        await("cleared") { dev.stored["a"] == null }
        on { vm.closeModal() }
        await("asked again") { paths(vm) == listOf("b") }
    }

    @Test fun noRatingInAFixedListsViewer() {
        val dev = Device(on = true)
        val vm = vm(dev)
        on { vm.showFiles(listOf(PhotoGalleryViewModel.Item("a", "a", "image/jpeg", 1, null, hash = a.hash)), 0) {} }
        assertFalse(vm.state.value.viewerRates)
        on { vm.rateOpen(3) }
        settle()
        assertTrue(dev.rates().isEmpty())
    }
}
