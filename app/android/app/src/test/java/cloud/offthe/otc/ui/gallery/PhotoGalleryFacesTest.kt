// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.FileExifInfo
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.People
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.PhotoFace
import cloud.offthe.otc.proto.PhotoFaces
import cloud.offthe.otc.proto.RemoveFromPerson
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespInstanceRole
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.SelectionActionTask
import cloud.offthe.otc.ui.common.TestThumbStore
import cloud.offthe.otc.ui.common.hx
import com.google.protobuf.ByteString
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

// A photo's faces in the viewer's Info (owner, 2026-11-01, from a user):
// GetPhotoFaces as Info shows - not for a video, nothing while face
// recognition is off or on a device from before - and a face's × taking it
// off its person with RemoveFromPerson and the photo's hash, the photo
// leaving the grid only among that person's photos.
class PhotoGalleryFacesTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun person(id: String, name: String) = Person.newBuilder().setId(id).setName(name).setFaceCount(3).build()
    private val people = listOf(person("p1", "Ana"), person("p2", "Leo"), person("p3", ""))
    private fun face(personId: String, name: String, x: Int) =
        PhotoFace.newBuilder().setPersonId(personId).setName(name).setThumbnail(ByteString.copyFromUtf8("jpeg-$personId-$x")).setX(x).setY(10).setW(40).setH(40).build()
    private val ana = face("p1", "Ana", 10)
    private val leo = face("p2", "Leo", 200)
    private val nobody = face("p3", "", 400)

    private fun file(path: String, at: String, mime: String = "image/jpeg"): File {
        val t = java.time.Instant.parse(at)
        return File.newBuilder().setPath(path).setHash(hx(path)).setMime(mime).setCreated(Timestamp.newBuilder().setSeconds(t.epochSecond)).build()
    }
    private val a = file("a", "2026-10-03T10:00:00Z")
    private val b = file("b", "2026-10-02T10:00:00Z")
    private val c = file("c", "2026-10-01T10:00:00Z")
    private val clip = file("clip", "2026-09-30T10:00:00Z", "video/mp4")
    private val ok = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
    private val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()

    /**
     * A device whose photos a, b and c (and a video) show [faces] by path;
     * [facesOn]: face recognition. [photoFaces] answers GetPhotoFaces in
     * the device's place when set; [gate] holds that answer until completed.
     * [remove] answers RemoveFromPerson (null: no answer), held by
     * [removeGate] until completed; one it took takes the person's faces
     * off the photo with that hash.
     */
    private inner class Device(
        val remove: RespEnvelope? = ok, val facesOn: Boolean = true, val photoFaces: RespEnvelope? = null,
        val gate: CompletableDeferred<Unit>? = null, val removeGate: CompletableDeferred<Unit>? = null,
    ) {
        val sent = CopyOnWriteArrayList<ReqEnvelope>()
        @Volatile var members = listOf(a, b, c, clip)
        @Volatile var faces = mapOf("a" to listOf(ana), "b" to listOf(ana, leo, nobody), "c" to listOf(leo))
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_LIST_PEOPLE -> RespEnvelope.newBuilder().setRespPeople(People.newBuilder().addAllPeople(people)).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(members)).build()
                ReqEnvelope.PayloadCase.REQ_GET_FILE_INFO -> RespEnvelope.newBuilder().setRespFileInfo(FileExifInfo.newBuilder().setCameraMake("Pixel")).build()
                ReqEnvelope.PayloadCase.REQ_GET_PHOTO_FACES -> {
                    gate?.await()
                    val path = req.reqGetPhotoFaces.path
                    photoFaces ?: RespEnvelope.newBuilder().setRespPhotoFaces(
                        PhotoFaces.newBuilder().setFaceRecognition(facesOn).setHash(hx(path)).addAllFaces(if (facesOn) faces[path].orEmpty() else emptyList()),
                    ).build()
                }
                ReqEnvelope.PayloadCase.REQ_REMOVE_FROM_PERSON -> {
                    removeGate?.await()
                    val r = req.reqRemoveFromPerson
                    if (remove == ok) faces = faces.mapValues { (p, fs) -> if (hx(p) in r.hashesList) fs.filter { it.personId != r.personId } else fs }
                    remove ?: throw IOException("no answer")
                }
                ReqEnvelope.PayloadCase.REQ_GET_INSTANCE_ROLE -> RespEnvelope.newBuilder().setRespInstanceRole(RespInstanceRole.newBuilder().setIsPrimary(true)).build()
                else -> throw IOException("not answered")
            }
        }
        fun facesAsked(): List<String> = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_GET_PHOTO_FACES }.map { it.reqGetPhotoFaces.path }
        fun removes(): List<RemoveFromPerson> = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_REMOVE_FROM_PERSON }.map { it.reqRemoveFromPerson }
        fun listings() = sent.count { it.payloadCase == ReqEnvelope.PayloadCase.REQ_LIST_PEOPLE }
    }

    @Volatile private var signIn = 1

    private fun vm(dev: Device) = PhotoGalleryViewModel(
        "test", send = dev.send, pause = {}, online = { true }, problemOf = { _, _ -> LoadProblem.FAILED },
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

    // Waits for the work already queued on the view model's thread.
    private fun settle() = on { }

    private fun paths(vm: PhotoGalleryViewModel) = vm.state.value.items.map { it.path }

    // The whole library, the photo at [index] open with its Info.
    private fun infoOn(dev: Device, index: Int, who: String? = null): PhotoGalleryViewModel {
        val vm = vm(dev)
        on { vm.loadPeople() }
        if (who != null) on { vm.showPerson(who) } else on { vm.resetAndLoadFirstPage() }
        await("the photos") { paths(vm) == dev.members.map { it.path } && !vm.state.value.loading }
        on { vm.open(index); vm.openInfo() }
        await("the details") { !vm.state.value.infoLoading }
        return vm
    }

    @Test fun theRules() {
        val item = PhotoGalleryViewModel.Item("a#1", "a", "image/heic", 1, null)
        assertTrue(asksPhotoFaces(item))
        assertFalse(asksPhotoFaces(item.copy(mime = "video/quicktime")))
        assertFalse(asksPhotoFaces(null))
        val answer = PhotoFaces.newBuilder().setFaceRecognition(true).setHash("h").addAllFaces(listOf(ana, leo, ana)).build()
        val info = photoFacesOf("a", answer)!!
        assertEquals("a", info.path)
        assertEquals("h", info.hash)
        assertEquals(listOf(ana, leo, ana), info.faces)
        // Off, or no face: nothing to show.
        assertNull(photoFacesOf("a", answer.toBuilder().setFaceRecognition(false).build()))
        assertNull(photoFacesOf("a", PhotoFaces.newBuilder().setFaceRecognition(true).setHash("h").build()))
        // A person's faces go together (twice in the photo, both), and none left is nothing.
        assertEquals(listOf(leo), info.without("p1")!!.faces)
        assertNull(info.without("p1")!!.without("p2"))
        assertEquals(info, info.without("p9"))
        // The question and the note, as Not this person's.
        assertEquals("Remove this photo from ⁨Ana⁩?", notThisPersonTitle(facePerson(ana), 1).english())
        assertEquals("Remove this photo from this person?", notThisPersonTitle(facePerson(nobody), 1).english())
        assertEquals("Removed 1 photo from ⁨Leo⁩", personTakenOffNote(1, facePerson(leo), null).english())
    }

    @Test fun infoListsThePhotosFacesLeftToRight() {
        val dev = Device()
        val vm = infoOn(dev, 1)
        await("the faces") { vm.state.value.infoFaces != null }
        assertEquals(listOf("b"), dev.facesAsked())
        val info = vm.state.value.infoFaces!!
        assertEquals("b", info.path)
        assertEquals(hx("b"), info.hash)
        assertEquals(listOf("p1", "p2", "p3"), info.faces.map { it.personId })
        // Closed, they go; opened again, they are asked again.
        on { vm.closeInfo() }
        assertNull(vm.state.value.infoFaces)
        on { vm.openInfo() }
        await("the faces again") { vm.state.value.infoFaces != null }
        assertEquals(listOf("b", "b"), dev.facesAsked())
        // Another photo: Info closes with its faces.
        on { vm.open(2) }
        assertFalse(vm.state.value.infoOpen)
        assertNull(vm.state.value.infoFaces)
    }

    @Test fun nothingForAVideoNorWithoutFaces() {
        // A video: nothing asked.
        val dev = Device()
        val vm = infoOn(dev, 3)
        settle()
        assertTrue(dev.facesAsked().isEmpty())
        assertNull(vm.state.value.infoFaces)
        // A photo nobody is in.
        dev.faces = emptyMap()
        on { vm.open(0); vm.openInfo() }
        await("the question") { dev.facesAsked().isNotEmpty() }
        settle()
        assertNull(vm.state.value.infoFaces)
        // Face recognition off.
        val off = Device(facesOn = false)
        val vmOff = infoOn(off, 1)
        await("the question") { off.facesAsked().isNotEmpty() }
        settle()
        assertNull(vmOff.state.value.infoFaces)
        // A refusal: nothing, quietly - the details stand.
        val bad = Device(photoFaces = RespEnvelope.newBuilder().setError(true).setErrorMessage("database is locked").build())
        val vmBad = infoOn(bad, 1)
        await("the question") { bad.facesAsked().isNotEmpty() }
        settle()
        assertNull(vmBad.state.value.infoFaces)
        assertEquals("Pixel", vmBad.state.value.infoData?.cameraMake)
        assertNull(vmBad.state.value.alert)
    }

    @Test fun anOlderDeviceShowsNothingUntilTheNextSignIn() {
        val dev = Device(photoFaces = unknown)
        val vm = infoOn(dev, 1)
        await("the question") { dev.facesAsked().size == 1 }
        settle()
        assertNull(vm.state.value.infoFaces)
        assertNull(vm.state.value.alert)
        assertNull(vm.state.value.needsUpdate)
        // Not asked again until the next sign-in.
        on { vm.closeInfo(); vm.openInfo() }
        settle()
        assertEquals(1, dev.facesAsked().size)
        signIn = 2
        on { vm.closeInfo(); vm.openInfo() }
        await("the question again") { dev.facesAsked().size == 2 }
    }

    @Test fun anAnswerForInfoClosedOrAnotherPhotoIsLeft() {
        val gate = CompletableDeferred<Unit>()
        val dev = Device(gate = gate)
        val vm = infoOn(dev, 1)
        await("the question") { dev.facesAsked().size == 1 }
        on { vm.closeInfo() }
        gate.complete(Unit)
        settle()
        assertNull(vm.state.value.infoFaces)
        // Opened on another photo while b's answer was on its way: c's faces only.
        val gate2 = CompletableDeferred<Unit>()
        val dev2 = Device(gate = gate2)
        val vm2 = infoOn(dev2, 1)
        await("the question") { dev2.facesAsked().size == 1 }
        on { vm2.open(2); vm2.openInfo() }
        await("the second question") { dev2.facesAsked().size == 2 }
        gate2.complete(Unit)
        await("c's faces") { vm2.state.value.infoFaces != null }
        settle()
        assertEquals("c", vm2.state.value.infoFaces!!.path)
        assertEquals(listOf("p2"), vm2.state.value.infoFaces!!.faces.map { it.personId })
    }

    @Test fun aFacesCrossTakesItOffItsPersonAndAsksAgain() {
        val dev = Device()
        val vm = infoOn(dev, 1)
        await("the faces") { vm.state.value.infoFaces != null }
        val listed = dev.listings()
        on { vm.removeInfoFace(leo) }
        await("the note") { vm.state.value.alert != null }
        val sent = dev.removes().single()
        assertEquals("p2", sent.personId)
        assertEquals(listOf(hx("b")), sent.hashesList)
        assertEquals("", sent.toPersonId)
        assertEquals("Removed 1 photo from ⁨Leo⁩", vm.state.value.alert?.english())
        // Not among Leo's photos: the grid and the viewer stay.
        assertEquals(listOf("a", "b", "c", "clip"), paths(vm))
        assertEquals(1, vm.state.value.openIndex)
        assertTrue(vm.state.value.infoOpen)
        // Info loses Leo at once and asks the device again.
        assertEquals(listOf("p1", "p3"), vm.state.value.infoFaces?.faces?.map { it.personId })
        await("the faces again") { dev.facesAsked().size == 2 }
        await("the people again") { dev.listings() > listed }
        assertNull(vm.state.value.preparing)
        // The unnamed are said so; the last face gone, People goes.
        on { vm.dismissAlert() }
        on { vm.removeInfoFace(nobody) }
        await("the note") { vm.state.value.alert != null }
        assertEquals("Removed 1 photo from this person", vm.state.value.alert?.english())
        on { vm.dismissAlert() }
        on { vm.removeInfoFace(ana) }
        await("the note") { vm.state.value.alert != null }
        await("the faces asked again") { dev.facesAsked().size == 4 }
        settle()
        assertNull(vm.state.value.infoFaces)
    }

    @Test fun amongThatPersonsPhotosThePhotoLeavesAsAfterNotThisPerson() {
        val dev = Device()
        val vm = infoOn(dev, 1, who = "p1")
        await("the faces") { vm.state.value.infoFaces != null }
        // Someone else's face: the photo stays among Ana's.
        on { vm.removeInfoFace(leo) }
        await("the note") { vm.state.value.alert != null }
        assertEquals(listOf("a", "b", "c", "clip"), paths(vm))
        assertTrue(vm.state.value.infoOpen)
        // Ana's own: it leaves her photos, and the viewer moves on.
        on { vm.dismissAlert() }
        await("the faces again") { vm.state.value.infoFaces?.faces?.map { it.personId } == listOf("p1", "p3") }
        on { vm.removeInfoFace(ana) }
        await("the note") { vm.state.value.alert != null && paths(vm) == listOf("a", "c", "clip") }
        assertEquals(listOf(hx("b")), dev.removes().last().hashesList)
        assertEquals("Removed 1 photo from ⁨Ana⁩", vm.state.value.alert?.english())
        assertEquals(1, vm.state.value.openIndex)
        assertFalse(vm.state.value.infoOpen)
        assertNull(vm.state.value.infoFaces)
    }

    @Test fun aFailureSaysSoAndKeepsEverything() {
        for (answer in listOf(RespEnvelope.newBuilder().setError(true).setErrorMessage("The demo can't change that").build(), null)) {
            val dev = Device(remove = answer)
            val vm = infoOn(dev, 0, who = "p1")
            await("the faces") { vm.state.value.infoFaces != null }
            on { vm.removeInfoFace(ana) }
            await("the note") { vm.state.value.alert != null }
            assertEquals("Couldn't remove them. Try again.", vm.state.value.alert?.english())
            assertEquals(listOf("a", "b", "c", "clip"), paths(vm))
            assertEquals(listOf("p1"), vm.state.value.infoFaces?.faces?.map { it.personId })
            assertEquals(0, vm.state.value.openIndex)
            assertNull(vm.state.value.preparing)
            assertEquals(1, dev.facesAsked().size)
        }
    }

    @Test fun busyWhileTheDeviceIsAtIt() {
        val gate = CompletableDeferred<Unit>()
        val dev = Device(removeGate = gate)
        val vm = infoOn(dev, 1)
        await("the faces") { vm.state.value.infoFaces != null }
        on { vm.removeInfoFace(leo) }
        await("the request") { dev.removes().isNotEmpty() }
        assertEquals(SelectionActionTask.NOT_PERSON, vm.state.value.preparing)
        // A second × meanwhile sends nothing.
        on { vm.removeInfoFace(ana).join() }
        gate.complete(Unit)
        await("the note") { vm.state.value.alert != null }
        assertEquals(1, dev.removes().size)
        assertNull(vm.state.value.preparing)
    }

    @Test fun theFilesViewerKeepsThePhoto() {
        val dev = Device()
        val vm = vm(dev)
        val items = listOf(a, b).map { PhotoGalleryViewModel.Item("${it.path}#x", it.path, it.mime, 1, null, hash = it.hash) }
        on { vm.showFiles(items, 1) {} }
        on { vm.openInfo() }
        await("the faces") { vm.state.value.infoFaces != null }
        on { vm.removeInfoFace(ana) }
        await("the note") { vm.state.value.alert != null }
        assertEquals(listOf("a", "b"), paths(vm))
        assertEquals(1, vm.state.value.openIndex)
        // Nobody's list to refresh in a viewer of its own.
        assertEquals(0, dev.listings())
        await("the faces again") { dev.facesAsked().size == 2 }
    }
}
