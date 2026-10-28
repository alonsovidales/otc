// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosPersonMoveFailed
import cloud.offthe.otc.i18n.appPhotosPersonMoveNone
import cloud.offthe.otc.i18n.appPhotosPersonMoveTitle
import cloud.offthe.otc.i18n.appPhotosPersonNeedsUpdate
import cloud.offthe.otc.i18n.appPhotosPersonRemoveFailed
import cloud.offthe.otc.i18n.appPhotosPersonRemoveText
import cloud.offthe.otc.i18n.appShellMoveToPerson
import cloud.offthe.otc.i18n.appShellNotThisPerson
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.People
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.RemoveFromPerson
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespInstanceRole
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.SelectionActionTask
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
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

// Not this person and Someone else…: face recognition filed photos under
// the wrong person, and the picked ones (or the one open in the viewer)
// are taken off them with RemoveFromPerson - moved to someone else, or
// matched to no one. They leave the grid at once; a device from before the
// request says it needs an update.
class PhotoGalleryPersonTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun person(id: String, name: String, faces: Int) = Person.newBuilder().setId(id).setName(name).setFaceCount(faces).build()
    private val ana = person("p1", "Ana", 3)
    private val leo = person("p2", "Leo", 9)
    private val nobody = person("p3", "", 4)

    private fun photo(path: String, at: String): File {
        val t = java.time.Instant.parse(at)
        return File.newBuilder().setPath(path).setHash(hx(path)).setMime("image/jpeg").setCreated(Timestamp.newBuilder().setSeconds(t.epochSecond)).build()
    }
    private val a = photo("a", "2026-10-03T10:00:00Z")
    private val b = photo("b", "2026-10-02T10:00:00Z")
    private val c = photo("c", "2026-10-01T10:00:00Z")
    private val ok = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
    private fun role(primary: Boolean) = RespEnvelope.newBuilder().setRespInstanceRole(RespInstanceRole.newBuilder().setIsPrimary(primary)).build()
    private val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()

    /**
     * A device whose people are [people], each with the photos [photos]
     * (whoever is searched for); [remove] answers RemoveFromPerson (null:
     * no answer), [gate] holds that answer until it is completed.
     */
    private inner class Device(
        val remove: RespEnvelope?, val people: List<Person> = listOf(ana, leo, nobody),
        val gate: CompletableDeferred<Unit>? = null,
    ) {
        val sent = CopyOnWriteArrayList<ReqEnvelope>()
        @Volatile var members = listOf(a, b, c)
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_LIST_PEOPLE -> RespEnvelope.newBuilder().setRespPeople(People.newBuilder().addAllPeople(people)).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(members)).build()
                ReqEnvelope.PayloadCase.REQ_REMOVE_FROM_PERSON -> {
                    gate?.await()
                    val gone = req.reqRemoveFromPerson.hashesList.toSet()
                    if (remove == ok) members = members.filter { it.hash !in gone }
                    remove ?: throw IOException("no answer")
                }
                ReqEnvelope.PayloadCase.REQ_GET_INSTANCE_ROLE -> role(true)
                else -> throw IOException("not answered")
            }
        }
        fun removes(): List<RemoveFromPerson> = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_REMOVE_FROM_PERSON }.map { it.reqRemoveFromPerson }
        fun listings() = sent.count { it.payloadCase == ReqEnvelope.PayloadCase.REQ_LIST_PEOPLE }
    }

    private fun vm(dev: Device) = PhotoGalleryViewModel(
        "test", send = dev.send, pause = {}, online = { true }, problemOf = { _, _ -> LoadProblem.FAILED },
        thumbs = thumbs, fullPhotos = thumbs, scope = scope, signIns = { 1 },
    )

    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    private fun paths(vm: PhotoGalleryViewModel) = vm.state.value.items.map { it.path }

    // [who]'s photos shown (from People), the people listed.
    private fun showing(dev: Device, who: Person = ana): PhotoGalleryViewModel {
        val vm = vm(dev)
        on { vm.loadPeople() }
        on { vm.showPerson(who.id) }
        await("their photos") { paths(vm) == dev.members.map { it.path } && !vm.state.value.loading }
        return vm
    }

    // ... and a and c picked.
    private fun picked(dev: Device, who: Person = ana): PhotoGalleryViewModel {
        val vm = showing(dev, who)
        on { vm.toggleSelect("a"); vm.toggleSelect("c") }
        return vm
    }

    @Test fun onlyAmongOnePersonsPhotos() {
        val vm = vm(Device(ok))
        assertNull(vm.state.value.personShown)
        on { vm.loadPeople() }
        on { vm.showPerson("p1") }
        assertEquals("p1", vm.state.value.personShown)
        assertEquals(ana, vm.state.value.personShownListed)
        // A tag with them: still one person.
        on { vm.addChip("beach") }
        assertEquals("p1", vm.state.value.personShown)
        // Two people: none of them.
        on { vm.togglePerson("p2") }
        assertNull(vm.state.value.personShown)
        on { vm.togglePerson("p1"); vm.togglePerson("p2") }
        assertNull(vm.state.value.personShown)
    }

    @Test fun notThisPersonTakesThePicksOffAndOutOfTheGrid() {
        val dev = Device(ok)
        val vm = picked(dev)
        val listed = dev.listings()
        on { vm.takeSelectionOffPerson() }
        await("the note") { vm.state.value.alert != null }
        val sent = dev.removes().single()
        assertEquals("p1", sent.personId)
        assertEquals(setOf(hx("a"), hx("c")), sent.hashesList.toSet())
        assertEquals("", sent.toPersonId)
        assertEquals(listOf("b"), paths(vm))
        assertTrue(vm.state.value.selected.isEmpty())
        assertEquals("Removed 2 photos from \u2068Ana\u2069", vm.state.value.alert?.english())
        assertNull(vm.state.value.preparing)
        assertNull(vm.state.value.needsUpdate)
        // The people's counts and covers are asked for again.
        await("the people again") { dev.listings() > listed }
    }

    @Test fun someoneElseMovesThemToThePersonPicked() {
        val dev = Device(ok)
        val vm = picked(dev)
        on { vm.takeSelectionOffPerson(to = leo) }
        await("the note") { vm.state.value.alert != null }
        val sent = dev.removes().single()
        assertEquals("p1", sent.personId)
        assertEquals("p2", sent.toPersonId)
        assertEquals(setOf(hx("a"), hx("c")), sent.hashesList.toSet())
        assertEquals(listOf("b"), paths(vm))
        assertEquals("Moved 2 photos to \u2068Leo\u2069", vm.state.value.alert?.english())
    }

    @Test fun unnamedPeopleAreSaidSo() {
        val dev = Device(ok)
        val vm = showing(dev, nobody)
        on { vm.toggleSelect("b") }
        on { vm.takeSelectionOffPerson() }
        await("the note") { vm.state.value.alert != null }
        assertEquals("Removed 1 photo from this person", vm.state.value.alert?.english())
        on { vm.dismissAlert(); vm.toggleSelect("a") }
        on { vm.takeSelectionOffPerson(to = nobody.toBuilder().setId("p4").build()) }
        await("the note") { vm.state.value.alert != null }
        assertEquals("Moved 1 photo to an unnamed person", vm.state.value.alert?.english())
    }

    @Test fun aPickNoLongerOnTheGridGoesByItsContent() {
        // Picked, then a tag searched for with the person: the grid is
        // replaced, the pick stays (issue #201's pickedAway), as last shown.
        val dev = Device(ok)
        val vm = showing(dev)
        on { vm.toggleSelect("a") }
        dev.members = listOf(b, c)
        on { vm.addChip("beach") }
        await("the new grid") { paths(vm) == listOf("b", "c") && !vm.state.value.loading }
        assertEquals("p1", vm.state.value.personShown)
        on { vm.takeSelectionOffPerson() }
        await("the note") { vm.state.value.alert != null }
        assertEquals(listOf(hx("a")), dev.removes().single().hashesList)
        assertEquals(listOf("b", "c"), paths(vm))
        assertTrue(vm.state.value.selected.isEmpty())
        assertEquals("Removed 1 photo from \u2068Ana\u2069", vm.state.value.alert?.english())
    }

    @Test fun busyWhileTheDeviceIsAtIt() {
        val gate = CompletableDeferred<Unit>()
        val dev = Device(ok, gate = gate)
        val vm = picked(dev)
        on { vm.takeSelectionOffPerson(to = leo) }
        await("the request") { dev.removes().isNotEmpty() }
        assertEquals(SelectionActionTask.MOVE_PERSON, vm.state.value.preparing)
        // A second tap meanwhile sends nothing.
        on { vm.takeSelectionOffPerson() }
        gate.complete(Unit)
        await("the note") { vm.state.value.alert != null }
        assertEquals(1, dev.removes().size)
        assertNull(vm.state.value.preparing)
    }

    @Test fun anOlderDeviceNeedsAnUpdate() {
        val dev = Device(unknown)
        val vm = picked(dev)
        on { vm.takeSelectionOffPerson() }
        await("the question") { vm.state.value.needsUpdate != null }
        assertEquals("Your device needs an update to change who is in a photo.", vm.state.value.needsUpdate!!.title.english())
        assertTrue(vm.state.value.needsUpdate!!.primary)
        // Nothing left the grid or the selection; no note besides.
        assertEquals(listOf("a", "b", "c"), paths(vm))
        assertEquals(setOf("a", "c"), vm.state.value.selected)
        assertNull(vm.state.value.alert)
    }

    @Test fun anyOtherFailureSaysSoAndKeepsThePicks() {
        for (answer in listOf(RespEnvelope.newBuilder().setError(true).setErrorMessage("database is locked").build(), null)) {
            for (to in listOf(null, leo)) {
                val dev = Device(answer)
                val vm = picked(dev)
                on { vm.takeSelectionOffPerson(to) }
                await("the note") { vm.state.value.alert != null }
                assertEquals(if (to == null) "Couldn't remove them. Try again." else "Couldn't move them. Try again.", vm.state.value.alert?.english())
                assertEquals(listOf("a", "b", "c"), paths(vm))
                assertEquals(setOf("a", "c"), vm.state.value.selected)
                assertNull(vm.state.value.needsUpdate)
                assertNull(vm.state.value.preparing)
            }
        }
    }

    @Test fun nothingIsSentWithoutOnePerson() {
        val dev = Device(ok)
        val vm = vm(dev)
        on { vm.resetAndLoadFirstPage() }
        await("the photos") { paths(vm) == listOf("a", "b", "c") && !vm.state.value.loading }
        on { vm.toggleSelect("a"); vm.takeSelectionOffPerson().join() }
        on { vm.togglePerson("p1"); vm.togglePerson("p2") }
        await("their photos") { !vm.state.value.loading && paths(vm).isNotEmpty() }
        on { vm.takeSelectionOffPerson().join() }
        assertTrue(dev.removes().isEmpty())
        assertEquals(setOf("a"), vm.state.value.selected)
    }

    @Test fun movingToTheSamePersonSendsNothing() {
        val dev = Device(ok)
        val vm = picked(dev)
        on { vm.takeSelectionOffPerson(to = ana).join() }
        assertTrue(dev.removes().isEmpty())
    }

    @Test fun theViewerMovesOnAsAfterADelete() {
        val dev = Device(ok)
        val vm = showing(dev)
        on { vm.open(1) }
        on { vm.takeOpenOffPerson() }
        await("the note") { vm.state.value.alert != null }
        assertEquals(listOf(hx("b")), dev.removes().single().hashesList)
        assertEquals(listOf("a", "c"), paths(vm))
        // On to the next photo, at the same place.
        assertEquals(1, vm.state.value.openIndex)
        assertEquals("Removed 1 photo from \u2068Ana\u2069", vm.state.value.alert?.english())
        // The last one: the one before it.
        on { vm.dismissAlert() }
        on { vm.takeOpenOffPerson(to = leo) }
        await("the note") { vm.state.value.alert != null }
        assertEquals("p2", dev.removes().last().toPersonId)
        assertEquals(listOf("a"), paths(vm))
        assertEquals(0, vm.state.value.openIndex)
        // The only one: the viewer closes.
        on { vm.dismissAlert() }
        on { vm.takeOpenOffPerson() }
        await("the note") { vm.state.value.alert != null }
        assertTrue(paths(vm).isEmpty())
        assertNull(vm.state.value.openIndex)
    }

    @Test fun theViewersFailureKeepsThePhotoOpen() {
        val dev = Device(null)
        val vm = showing(dev)
        on { vm.open(2) }
        on { vm.takeOpenOffPerson() }
        await("the note") { vm.state.value.alert != null }
        assertEquals("Couldn't remove them. Try again.", vm.state.value.alert?.english())
        assertEquals(listOf("a", "b", "c"), paths(vm))
        assertEquals(2, vm.state.value.openIndex)
    }

    @Test fun theViewersOlderDeviceNeedsAnUpdate() {
        val dev = Device(unknown)
        val vm = showing(dev)
        on { vm.open(0) }
        on { vm.takeOpenOffPerson(to = leo) }
        await("the question") { vm.state.value.needsUpdate != null }
        assertEquals(0, vm.state.value.openIndex)
        assertEquals(listOf("a", "b", "c"), paths(vm))
    }

    @Test fun theListOfOthers() {
        val zoe = person("p5", "Zoë", 1)
        val jose = person("p6", "  José   María ", 2)
        val all = listOf(nobody, ana, leo, zoe, jose)
        // Named first, then the unnamed, each by photos; never the person shown.
        assertEquals(listOf("p2", "p6", "p5", "p3"), movePeople(all, "p1", "").map { it.id })
        // By name, ignoring case, accents and spaces; the unnamed go.
        assertEquals(listOf("p5"), movePeople(all, "p1", "zoe").map { it.id })
        assertEquals(listOf("p6"), movePeople(all, "p1", " jose  MARIA").map { it.id })
        assertEquals(listOf("p2", "p6", "p5"), movePeople(all, "p1", "e").map { it.id })
        assertTrue(movePeople(all, "p1", "ana").isEmpty())
        assertEquals(movePeople(all, "p1", ""), movePeople(all, "p1", "   "))
        assertTrue(movePeople(listOf(ana), "p1", "").isEmpty())
    }

    @Test fun theQuestionAndTheNotes() {
        assertEquals("Remove this photo from \u2068Ana\u2069?", notThisPersonTitle(ana, 1).english())
        assertEquals("Remove 3 photos from \u2068Ana\u2069?", notThisPersonTitle(ana, 3).english())
        assertEquals("Remove 2 photos from this person?", notThisPersonTitle(nobody, 2).english())
        assertEquals("Remove this photo from this person?", notThisPersonTitle(null, 1).english())
        assertEquals("Removed 1 photo from \u2068Ana\u2069", personTakenOffNote(1, ana, null).english())
        assertEquals("Removed 4 photos from this person", personTakenOffNote(4, null, null).english())
        assertEquals("Moved 2 photos to \u2068Leo\u2069", personTakenOffNote(2, ana, leo).english())
        assertEquals("Moved 1 photo to an unnamed person", personTakenOffNote(1, ana, nobody).english())
        val req = removeFromPersonRequest("p1", listOf("h1", "h2"), null)
        assertEquals("p1", req.personId)
        assertEquals(listOf("h1", "h2"), req.hashesList)
        assertFalse(req.toPersonId.isNotEmpty())
        assertEquals("p2", removeFromPersonRequest("p1", listOf("h1"), "p2").toPersonId)
    }

    @Test fun englishText() {
        assertEquals("Not this person", S.appShellNotThisPerson().english())
        assertEquals("Someone else…", S.appShellMoveToPerson().english())
        assertEquals("The photos stay on your device, and face recognition won't put them back.", S.appPhotosPersonRemoveText().english())
        assertEquals("Move this photo to…", S.appPhotosPersonMoveTitle(1).english())
        assertEquals("Move 5 photos to…", S.appPhotosPersonMoveTitle(5).english())
        assertEquals("No one else has been found yet.", S.appPhotosPersonMoveNone().english())
        assertEquals("Couldn't remove them. Try again.", S.appPhotosPersonRemoveFailed().english())
        assertEquals("Couldn't move them. Try again.", S.appPhotosPersonMoveFailed().english())
        assertEquals("Your device needs an update to change who is in a photo.", S.appPhotosPersonNeedsUpdate().english())
    }
}
