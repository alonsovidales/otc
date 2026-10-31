// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.Favourites
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ImageGroups
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.TestThumbStore
import cloud.offthe.otc.ui.common.hx
import com.google.protobuf.Timestamp
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
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

// Favourites, the viewer's heart: asked for the photo shown and its
// neighbours, drawn only once known, tapped optimistically and put back
// when the device refuses, none on a device before Favourites, and inside
// Favourites the photos taken out leave the grid as the viewer closes.
// The iPhone app follows the same rules.
class PhotoGalleryFavouritesTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun photo(path: String, at: String, hash: String = hx(path)): File {
        val t = java.time.Instant.parse(at)
        return File.newBuilder().setPath(path).setHash(hash).setMime("image/jpeg").setCreated(Timestamp.newBuilder().setSeconds(t.epochSecond)).build()
    }
    private val a = photo("a", "2026-10-05T10:00:00Z")
    private val b = photo("b", "2026-10-04T10:00:00Z")
    private val c = photo("c", "2026-10-03T10:00:00Z")
    private val d = photo("d", "2026-10-02T10:00:00Z")
    private val favouritesGroup = ImageGroup.newBuilder().setId("favourites").setName("Favourites").setFavourites(true).setFileCount(3).build()

    private val ok = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
    private val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
    private val demo = RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo: nothing can be changed.").build()

    /**
     * A device whose Favourites hold [favourites] (by hash: any copy
     * counts). [get]/[set]: what GetFavourites/SetFavourite answer instead
     * (null: as a device with Favourites does); [hold]: SetFavourite waits
     * for it. Images lists [library] for any search.
     */
    private inner class Device(
        favourites: Set<String>, val get: RespEnvelope? = null, @Volatile var set: RespEnvelope? = null,
        val library: List<File> = listOf(a, b, c, d),
    ) {
        val sent = CopyOnWriteArrayList<ReqEnvelope>()
        val hashes: MutableSet<String> = ConcurrentHashMap.newKeySet<String>().also { it.addAll(favourites) }
        @Volatile var hold: CompletableDeferred<Unit>? = null
        @Volatile var holdGet: CompletableDeferred<Unit>? = null
        @Volatile var noAnswer = false
        private fun hashOf(path: String) = library.firstOrNull { it.path == path }?.hash ?: hx(path)
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS -> RespEnvelope.newBuilder().setRespImageGroups(
                    ImageGroups.newBuilder().addGroups(favouritesGroup.toBuilder().setFileCount(library.count { it.hash in hashes })),
                ).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> {
                    val inFavourites = req.reqSearchPhotos.groupId == favouritesGroup.id
                    RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(library.filter { !inFavourites || it.hash in hashes })).build()
                }
                ReqEnvelope.PayloadCase.REQ_GET_FAVOURITES -> {
                    holdGet?.await()
                    if (noAnswer) throw IOException("no answer")
                    get ?: RespEnvelope.newBuilder().setRespFavourites(
                        Favourites.newBuilder().addAllPaths(req.reqGetFavourites.pathsList.filter { hashOf(it) in hashes }),
                    ).build()
                }
                ReqEnvelope.PayloadCase.REQ_SET_FAVOURITE -> {
                    hold?.await()
                    val answer = set ?: ok
                    if (answer == ok) for (p in req.reqSetFavourite.pathsList) {
                        if (req.reqSetFavourite.favourite) hashes += hashOf(p) else hashes -= hashOf(p)
                    }
                    answer
                }
                else -> throw IOException("not answered")
            }
        }
        fun asked() = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_GET_FAVOURITES }.map { it.reqGetFavourites.pathsList }
        fun sets() = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_SET_FAVOURITE }.map { it.reqSetFavourite }
    }

    @Volatile private var signIn = 1

    // [pause]: a failed ask's wait (a moment, where a test leaves one failing).
    private fun vm(dev: Device, pause: suspend (Long) -> Unit = {}) = PhotoGalleryViewModel(
        "test", send = dev.send, pause = pause, online = { true }, problemOf = { _, _ -> LoadProblem.FAILED },
        thumbs = thumbs, fullPhotos = thumbs, scope = scope, signIns = { signIn },
    )

    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    // A moment for anything still on its way (an answer that must not land).
    private fun settle() { Thread.sleep(50); on { } }

    private fun itemOf(f: File) = PhotoGalleryViewModel.Item(f.path, f.path, f.mime, 1, null, hash = f.hash)

    /** The viewer over a fixed list (as Files', Places' and memories' are), open at [at]. */
    private fun viewer(dev: Device, at: Int, files: List<File> = listOf(a, b, c, d), pause: suspend (Long) -> Unit = {}): PhotoGalleryViewModel {
        val vm = vm(dev, pause)
        on { vm.showFiles(files.map(::itemOf), at) {} }
        return vm
    }

    private fun hearts(vm: PhotoGalleryViewModel) = vm.state.value.favourites

    @Test fun thePhotoShownAndItsNeighboursAreAskedTogether() {
        val dev = Device(setOf(b.hash))
        val vm = viewer(dev, 1)
        await("the hearts") { hearts(vm).size == 3 }
        assertEquals(listOf(listOf("a", "b", "c")), dev.asked())
        assertEquals(mapOf("a" to false, "b" to true, "c" to false), hearts(vm))
        // A swipe: the next photo's heart is known already, and its own
        // neighbours are asked for (the device may have changed one).
        on { vm.next() }
        await("the next hearts") { "d" in hearts(vm) }
        assertEquals(listOf("b", "c", "d"), dev.asked().last())
        // The first photo has no neighbour before it.
        on { vm.open(0) }
        await("the first's ask") { dev.asked().size == 3 }
        assertEquals(listOf("a", "b"), dev.asked().last())
    }

    @Test fun noHeartUntilTheDeviceHasSaid() {
        val dev = Device(setOf(a.hash))
        dev.noAnswer = true
        val vm = viewer(dev, 0, pause = { delay(50) })
        await("the ask") { dev.asked().size >= 2 } // asked again after no answer
        assertNull(hearts(vm)["a"])
        assertFalse(vm.state.value.favouritesOff)
        // A tap on a heart not drawn does nothing.
        on { vm.toggleFavourite() }
        assertTrue(dev.sets().isEmpty())
        dev.noAnswer = false
        await("the heart") { hearts(vm)["a"] == true }
    }

    @Test fun noAnswerIsAskedAgainAFewTimesThenLeftToTheNextPhoto() {
        val dev = Device(emptySet())
        dev.noAnswer = true
        val vm = viewer(dev, 1)
        await("the asks") { dev.asked().size == 5 }
        settle()
        assertEquals(5, dev.asked().size)
        assertTrue(hearts(vm).isEmpty())
        dev.noAnswer = false
        on { vm.next() }
        await("the next photo's") { hearts(vm)["c"] == false }
    }

    @Test fun aTapFillsAtOnceAndTheDeviceTakesIt() {
        val dev = Device(emptySet())
        val vm = viewer(dev, 1)
        await("the hearts") { hearts(vm)["b"] == false }
        dev.hold = CompletableDeferred()
        on { vm.toggleFavourite() }
        // Drawn before the device answered.
        assertEquals(true, hearts(vm)["b"])
        await("the request") { dev.sets().size == 1 }
        val sent = dev.sets().single()
        assertEquals(listOf("b"), sent.pathsList)
        assertTrue(sent.favourite)
        dev.hold!!.complete(Unit)
        await("the device") { b.hash in dev.hashes }
        settle()
        assertEquals(true, hearts(vm)["b"])
        assertNull(vm.state.value.favouriteNote)
        // And out again.
        on { vm.toggleFavourite() }
        await("out") { b.hash !in dev.hashes }
        assertEquals(false, hearts(vm)["b"])
        assertFalse(dev.sets().last().favourite)
    }

    @Test fun aRefusalPutsTheHeartBackAndSaysTheDevicesWords() {
        val dev = Device(emptySet(), set = demo)
        val vm = viewer(dev, 0)
        await("the hearts") { hearts(vm)["a"] == false }
        on { vm.toggleFavourite() }
        await("the note") { vm.state.value.favouriteNote != null }
        assertEquals(false, hearts(vm)["a"])
        assertEquals("This is a demo: nothing can be changed.", vm.state.value.favouriteNote?.english())
        on { vm.favouriteNoteShown() }
        assertNull(vm.state.value.favouriteNote)
        // No answer at all: our words.
        dev.set = RespEnvelope.getDefaultInstance()
        on { vm.toggleFavourite() }
        await("the note again") { vm.state.value.favouriteNote != null }
        assertEquals(false, hearts(vm)["a"])
        assertEquals("Couldn't change Favourites. Try again.", vm.state.value.favouriteNote?.english())
    }

    @Test fun quickTapsEndWithTheLastOneOneRequestAtATime() {
        val dev = Device(emptySet())
        val vm = viewer(dev, 0)
        await("the hearts") { hearts(vm)["a"] == false }
        dev.hold = CompletableDeferred()
        on { vm.toggleFavourite() } // in
        await("the first request") { dev.sets().size == 1 }
        on { vm.toggleFavourite() } // out, while the first is on its way
        on { vm.toggleFavourite() } // in again
        on { vm.toggleFavourite() } // out
        assertEquals(false, hearts(vm)["a"])
        // Nothing more goes until the first is answered.
        settle()
        assertEquals(1, dev.sets().size)
        dev.hold!!.complete(Unit)
        await("the last tap") { dev.sets().size == 2 }
        settle()
        assertEquals(listOf(true, false), dev.sets().map { it.favourite })
        assertFalse(a.hash in dev.hashes)
        assertEquals(false, hearts(vm)["a"])
    }

    @Test fun aFailureAfterAnotherTapGoesBackToWhatTheDeviceSaid() {
        // In, refused; out tapped meanwhile is then nothing to send.
        val dev = Device(setOf(a.hash), set = demo)
        val vm = viewer(dev, 0)
        await("the hearts") { hearts(vm)["a"] == true }
        dev.hold = CompletableDeferred()
        on { vm.toggleFavourite() } // out
        await("the request") { dev.sets().size == 1 }
        on { vm.toggleFavourite() } // in again, while it is on its way
        dev.hold!!.complete(Unit)
        await("the note") { vm.state.value.favouriteNote != null }
        settle()
        assertEquals(1, dev.sets().size)
        assertEquals(true, hearts(vm)["a"])
    }

    @Test fun anAnswerAskedBeforeATapDoesntUndoIt() {
        val dev = Device(emptySet())
        val vm = viewer(dev, 0)
        await("the hearts") { hearts(vm)["a"] == false }
        dev.hold = CompletableDeferred()
        on { vm.toggleFavourite() }
        // A swipe away and back asks again while the change is on its way:
        // the device still says no, which the heart must not show.
        on { vm.next(); vm.prev() }
        await("the asks") { dev.asked().size == 3 }
        settle()
        assertEquals(true, hearts(vm)["a"])
        dev.hold!!.complete(Unit)
        await("the device") { a.hash in dev.hashes }
        settle()
        assertEquals(true, hearts(vm)["a"])
    }

    @Test fun everyCopyOfThePhotoFollowsItsHeart() {
        // Two paths with the same content: one photo, one heart.
        val copy = photo("copy-of-a", "2026-10-05T10:00:00Z", hash = a.hash)
        val dev = Device(emptySet(), library = listOf(a, copy, b))
        val vm = viewer(dev, 0, listOf(a, copy, b))
        await("the hearts") { hearts(vm).size == 2 }
        on { vm.toggleFavourite() }
        assertEquals(true, hearts(vm)["a"])
        assertEquals(true, hearts(vm)["copy-of-a"])
        await("the device") { a.hash in dev.hashes }
        assertEquals(listOf("a"), dev.sets().single().pathsList)
    }

    @Test fun aDeviceBeforeFavouritesHasNoHeartUntilTheNextSignIn() {
        val dev = Device(emptySet(), get = unknown)
        val vm = viewer(dev, 1)
        await("the answer") { vm.state.value.favouritesOff }
        assertTrue(hearts(vm).isEmpty())
        // Not asked again for this connection.
        on { vm.next() }
        settle()
        assertEquals(1, dev.asked().size)
        // Signed in again (maybe to an updated device): asked again.
        signIn = 2
        on { vm.prev() }
        await("asked again") { dev.asked().size == 2 }
        settle()
        assertTrue(vm.state.value.favouritesOff) // the same answer
    }

    @Test fun theHeartsAreForgottenAsTheViewerCloses() {
        val dev = Device(setOf(a.hash))
        val vm = viewer(dev, 0)
        await("the hearts") { hearts(vm)["a"] == true }
        on { vm.closeModal() }
        assertTrue(hearts(vm).isEmpty())
        // Taken out elsewhere meanwhile: opened again, no heart until the device says.
        dev.hashes.clear()
        dev.holdGet = CompletableDeferred()
        on { vm.showFiles(listOf(a, b).map(::itemOf), 0) {} }
        await("the ask") { dev.asked().size == 2 }
        assertNull(hearts(vm)["a"])
        dev.holdGet!!.complete(Unit)
        await("the heart") { hearts(vm)["a"] == false }
    }

    @Test fun insideFavouritesPhotosTakenOutLeaveAsTheViewerCloses() {
        val dev = Device(setOf(a.hash, b.hash, c.hash))
        val vm = vm(dev)
        on { vm.openGroup(favouritesGroup) }
        await("Favourites") { vm.state.value.items.map { it.path } == listOf("a", "b", "c") && !vm.state.value.loading }
        on { vm.open(0) }
        await("the hearts") { hearts(vm)["a"] == true && hearts(vm)["b"] == true }
        on { vm.toggleFavourite() } // a out
        await("a out") { a.hash !in dev.hashes }
        on { vm.next() }
        await("c's heart") { hearts(vm)["c"] == true }
        on { vm.toggleFavourite() } // b out...
        await("b out") { b.hash !in dev.hashes }
        on { vm.toggleFavourite() } // ...and back in
        await("b in") { b.hash in dev.hashes }
        // Still all there while the viewer is open.
        assertEquals(listOf("a", "b", "c"), vm.state.value.items.map { it.path })
        on { vm.closeModal() }
        await("a gone") { vm.state.value.items.map { it.path } == listOf("b", "c") }
        // The collection's count follows.
        await("the count") { vm.state.value.activeGroup?.fileCount == 2 }
    }

    @Test fun outsideFavouritesNothingLeavesTheGrid() {
        val dev = Device(setOf(a.hash))
        val vm = vm(dev)
        on { vm.openGroup(ImageGroup.newBuilder().setId("trip").setName("Trip").build()) }
        await("a collection") { vm.state.value.items.size == 4 && !vm.state.value.loading }
        on { vm.open(0) }
        await("the hearts") { hearts(vm)["a"] == true }
        on { vm.toggleFavourite() }
        await("a out") { a.hash !in dev.hashes }
        on { vm.closeModal() }
        settle()
        assertEquals(4, vm.state.value.items.size)
    }

    // ---- The rules on their own ----

    @Test fun pathsAskedAreTheOpenOnesAndItsNeighbours() {
        val items = listOf(a, b, c).map(::itemOf)
        assertEquals(listOf("a", "b"), favouritePaths(items, 0))
        assertEquals(listOf("a", "b", "c"), favouritePaths(items, 1))
        assertEquals(listOf("b", "c"), favouritePaths(items, 2))
        assertEquals(listOf("a"), favouritePaths(items.take(1), 0))
    }

    @Test fun samePhotoIsByHash() {
        val copy = itemOf(photo("copy", "2026-10-05T10:00:00Z", hash = a.hash))
        val items = listOf(itemOf(a), itemOf(b), copy)
        assertEquals(listOf("copy", "a"), samePhoto(items, copy))
        assertEquals(listOf("b"), samePhoto(items, itemOf(b)))
        // Without a hash: the path alone.
        val bare = PhotoGalleryViewModel.Item("x", "x", "image/jpeg", 1, null)
        assertEquals(listOf("x"), samePhoto(items + bare, bare))
    }

    @Test fun unfavouritedAreTheKnownEmptyHearts() {
        val items = listOf(a, b, c).map(::itemOf)
        assertEquals(setOf("b"), unfavourited(items, mapOf("a" to true, "b" to false)))
        assertTrue(unfavourited(items, emptyMap()).isEmpty())
    }

    @Test fun whatASetFavouriteAnswerSays() {
        assertNull(favouriteRefusal(ok))
        assertEquals("This is a demo: nothing can be changed.", favouriteRefusal(demo)?.english())
        val ackWords = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(false).setErrorMsg("Nope.")).build()
        assertEquals("Nope.", favouriteRefusal(ackWords)?.english())
        val failed = "Couldn't change Favourites. Try again."
        assertEquals(failed, favouriteRefusal(null)?.english())
        assertEquals(failed, favouriteRefusal(unknown)?.english())
        assertEquals(failed, favouriteRefusal(RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(false)).build())?.english())
        assertEquals(failed, favouriteRefusal(RespEnvelope.newBuilder().setError(true).build())?.english())
        assertEquals(failed, favouriteRefusal(RespEnvelope.getDefaultInstance())?.english())
    }
}
