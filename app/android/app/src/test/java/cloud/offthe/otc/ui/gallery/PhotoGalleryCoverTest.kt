// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosPersonCoverAuto
import cloud.offthe.otc.i18n.appPhotosPersonCoverChange
import cloud.offthe.otc.i18n.appPhotosPersonCoverDone
import cloud.offthe.otc.i18n.appPhotosPersonCoverFailed
import cloud.offthe.otc.i18n.appPhotosPersonCoverHint
import cloud.offthe.otc.i18n.appPhotosPersonCoverNeedsUpdate
import cloud.offthe.otc.i18n.appPhotosPersonCoverUpload
import cloud.offthe.otc.i18n.appPhotosPersonCoverUse
import cloud.offthe.otc.i18n.appPhotosPersonCropHint
import cloud.offthe.otc.i18n.appPhotosPersonCropTitle
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.People
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespInstanceRole
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.SetPersonCover
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
import org.junit.Assert.assertArrayEquals
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

// A person's face chosen by the owner (owner, 2026-10-31): Use this face
// on the one photo picked among one person's photos, or the one open in
// the viewer, and Upload a photo… / Choose automatically under their face
// at the top - SetPersonCover with the photo's hash, the crop's JPEG, or
// neither. Done, a note and the people listed again; refused, the device's
// words or "Couldn't change the face"; a device from before the request
// says it needs an update.
class PhotoGalleryCoverTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun person(id: String, name: String, faces: Int, chosen: Boolean = false) =
        Person.newBuilder().setId(id).setName(name).setFaceCount(faces).setCoverChosen(chosen).build()
    private val ana = person("p1", "Ana", 3)
    private val leo = person("p2", "Leo", 9)

    private fun media(path: String, at: String, mime: String): File {
        val t = java.time.Instant.parse(at)
        return File.newBuilder().setPath(path).setHash(hx(path)).setMime(mime).setCreated(Timestamp.newBuilder().setSeconds(t.epochSecond)).build()
    }
    private val a = media("a", "2026-10-03T10:00:00Z", "image/jpeg")
    private val b = media("b", "2026-10-02T10:00:00Z", "image/heic")
    private val v = media("v", "2026-10-01T10:00:00Z", "video/mp4")
    private val ok = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
    private val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
    private fun refused(words: String, code: String = "") = RespEnvelope.newBuilder().setError(true).setErrorCode(code).setErrorMessage(words).build()
    // A small JPEG as the crop makes one: its start marker, then anything.
    private val jpeg = byteArrayOf(0xFF.toByte(), 0xD8.toByte(), 0xFF.toByte(), 0xE0.toByte(), 1, 2, 3)

    /**
     * A device whose people are Ana and Leo, with the photos a, b and the
     * video v (whoever is searched for); [cover] answers SetPersonCover
     * (null: no answer), [gate] holds that answer until it is completed.
     * Taken, Ana's face is the owner's choice from then on (cover_chosen).
     */
    private inner class Device(val cover: RespEnvelope?, val gate: CompletableDeferred<Unit>? = null) {
        val sent = CopyOnWriteArrayList<ReqEnvelope>()
        @Volatile var chosen = false
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_LIST_PEOPLE ->
                    RespEnvelope.newBuilder().setRespPeople(People.newBuilder().addPeople(ana.toBuilder().setCoverChosen(chosen)).addPeople(leo)).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(listOf(a, b, v))).build()
                ReqEnvelope.PayloadCase.REQ_SET_PERSON_COVER -> {
                    gate?.await()
                    if (cover == ok) chosen = req.reqSetPersonCover.coverCase != SetPersonCover.CoverCase.COVER_NOT_SET
                    cover ?: throw IOException("no answer")
                }
                ReqEnvelope.PayloadCase.REQ_GET_INSTANCE_ROLE -> RespEnvelope.newBuilder().setRespInstanceRole(RespInstanceRole.newBuilder().setIsPrimary(true)).build()
                else -> throw IOException("not answered")
            }
        }
        fun covers(): List<SetPersonCover> = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_SET_PERSON_COVER }.map { it.reqSetPersonCover }
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

    // Ana's photos shown (from People), the people listed.
    private fun showing(dev: Device): PhotoGalleryViewModel {
        val vm = vm(dev)
        on { vm.loadPeople() }
        on { vm.showPerson(ana.id) }
        await("her photos") { paths(vm) == listOf("a", "b", "v") && !vm.state.value.loading }
        return vm
    }

    private fun anaListed(vm: PhotoGalleryViewModel) = vm.state.value.allPeople.first { it.id == ana.id }

    @Test fun useThisFaceSendsThePhotoPicked() {
        val dev = Device(ok)
        val vm = showing(dev)
        val listed = dev.listings()
        on { vm.toggleSelect("b") }
        on { vm.useSelectionAsCover() }
        await("the note") { vm.state.value.alert != null }
        val sent = dev.covers().single()
        assertEquals("p1", sent.personId)
        assertEquals(SetPersonCover.CoverCase.HASH, sent.coverCase)
        assertEquals(hx("b"), sent.hash)
        assertEquals("Face updated", vm.state.value.alert?.english())
        // The photo stays; the selection is left.
        assertEquals(listOf("a", "b", "v"), paths(vm))
        assertTrue(vm.state.value.selected.isEmpty())
        assertNull(vm.state.value.preparing)
        assertNull(vm.state.value.needsUpdate)
        // The people are asked again: the face, and Choose automatically, change at once.
        await("the people again") { dev.listings() > listed && anaListed(vm).coverChosen }
    }

    @Test fun onlyOnePhotoAndNoVideo() {
        val dev = Device(ok)
        val vm = showing(dev)
        // Two picked, then a video alone: nothing is sent, the picks stay.
        on { vm.toggleSelect("a"); vm.toggleSelect("b") }
        on { vm.useSelectionAsCover().join() }
        on { vm.clearSelection(); vm.toggleSelect("v") }
        on { vm.useSelectionAsCover().join() }
        assertTrue(dev.covers().isEmpty())
        assertEquals(setOf("v"), vm.state.value.selected)
        assertNull(vm.state.value.alert)
        // The rule the bar and the viewer follow.
        val items = vm.state.value.items
        assertEquals("a", coverPhoto(items.filter { it.path == "a" })?.path)
        assertNull(coverPhoto(items.filter { it.path == "v" }))
        assertNull(coverPhoto(items.filter { it.path != "v" }))
        assertNull(coverPhoto(emptyList()))
    }

    @Test fun nothingIsSentWithoutOnePerson() {
        val dev = Device(ok)
        val vm = vm(dev)
        on { vm.loadPeople() }
        on { vm.resetAndLoadFirstPage() }
        await("the photos") { paths(vm) == listOf("a", "b", "v") && !vm.state.value.loading }
        on { vm.toggleSelect("a") }
        on { vm.useSelectionAsCover().join() }
        on { vm.open(0) }
        on { vm.useOpenAsCover().join() }
        // Two people: none of them.
        on { vm.togglePerson("p1"); vm.togglePerson("p2") }
        await("their photos") { !vm.state.value.loading && paths(vm).isNotEmpty() }
        on { vm.useSelectionAsCover().join() }
        assertTrue(dev.covers().isEmpty())
        assertEquals(setOf("a"), vm.state.value.selected)
    }

    @Test fun theViewerStaysOnThePhoto() {
        val dev = Device(ok)
        val vm = showing(dev)
        on { vm.open(1) }
        on { vm.useOpenAsCover() }
        await("the note") { vm.state.value.alert != null }
        assertEquals(hx("b"), dev.covers().single().hash)
        assertEquals("Face updated", vm.state.value.alert?.english())
        assertEquals(1, vm.state.value.openIndex)
        assertEquals(listOf("a", "b", "v"), paths(vm))
        // A video open: nothing.
        on { vm.dismissAlert(); vm.open(2) }
        on { vm.useOpenAsCover().join() }
        assertEquals(1, dev.covers().size)
        assertNull(vm.state.value.alert)
    }

    @Test fun anUploadedPictureGoesAsItsJpeg() {
        val dev = Device(ok)
        val vm = showing(dev)
        on { vm.uploadPersonCover("p1", jpeg) }
        await("the note") { vm.state.value.alert != null }
        val sent = dev.covers().single()
        assertEquals("p1", sent.personId)
        assertEquals(SetPersonCover.CoverCase.IMAGE, sent.coverCase)
        assertArrayEquals(jpeg, sent.image.toByteArray())
        assertEquals("Face updated", vm.state.value.alert?.english())
        await("the people again") { anaListed(vm).coverChosen }
    }

    @Test fun aPictureTheDeviceWouldRefuseIsNotSent() {
        val dev = Device(ok)
        val vm = showing(dev)
        val tooBig = ByteArray(PERSON_COVER_MAX_BYTES + 1).also { it[0] = 0xFF.toByte(); it[1] = 0xD8.toByte() }
        val png = byteArrayOf(0x89.toByte(), 'P'.code.toByte(), 'N'.code.toByte(), 'G'.code.toByte(), 0, 0)
        for (bytes in listOf(tooBig, png, ByteArray(0))) {
            on { vm.uploadPersonCover("p1", bytes).join() }
            assertEquals("Couldn't change the face. Try again.", vm.state.value.alert?.english())
            on { vm.dismissAlert() }
        }
        assertTrue(dev.covers().isEmpty())
    }

    @Test fun chooseAutomaticallySendsNeither() {
        val dev = Device(ok)
        val vm = showing(dev)
        dev.chosen = true
        on { vm.loadPeople() }
        assertTrue(anaListed(vm).coverChosen)
        on { vm.resetPersonCover("p1") }
        await("the note") { vm.state.value.alert != null }
        val sent = dev.covers().single()
        assertEquals("p1", sent.personId)
        assertEquals(SetPersonCover.CoverCase.COVER_NOT_SET, sent.coverCase)
        assertEquals("Face updated", vm.state.value.alert?.english())
        // The device's choice again: no Choose automatically.
        await("the people again") { !anaListed(vm).coverChosen }
    }

    @Test fun aRefusalSaysTheDevicesWords() {
        val words = "There's no face of this person in that photo."
        val answers = listOf(
            refused(words) to words,
            // The demo device refuses every change in its own words.
            refused("This is a demo: nothing can be changed.", "demo") to "This is a demo: nothing can be changed.",
            RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(false).setErrorMsg(words)).build() to words,
            // No words, or no answer: ours.
            refused("") to "Couldn't change the face. Try again.",
            null to "Couldn't change the face. Try again.",
        )
        for ((answer, said) in answers) {
            val dev = Device(answer)
            val vm = showing(dev)
            val listed = dev.listings()
            on { vm.toggleSelect("a") }
            on { vm.useSelectionAsCover() }
            await("the note") { vm.state.value.alert != null }
            assertEquals(said, vm.state.value.alert?.english())
            // The pick stays for another try; nothing else changes.
            assertEquals(setOf("a"), vm.state.value.selected)
            assertNull(vm.state.value.needsUpdate)
            assertNull(vm.state.value.preparing)
            assertEquals(listed, dev.listings())
        }
    }

    @Test fun anOlderDeviceNeedsAnUpdate() {
        for (ask in listOf<(PhotoGalleryViewModel) -> Unit>(
            { it.toggleSelect("a"); it.useSelectionAsCover() },
            { it.open(0); it.useOpenAsCover() },
            { it.uploadPersonCover("p1", jpeg) },
            { it.resetPersonCover("p1") },
        )) {
            val dev = Device(unknown)
            val vm = showing(dev)
            on { ask(vm) }
            await("the question") { vm.state.value.needsUpdate != null }
            assertEquals("Your device needs an update to change a person's face.", vm.state.value.needsUpdate!!.title.english())
            assertTrue(vm.state.value.needsUpdate!!.primary)
            assertNull(vm.state.value.alert)
            assertEquals(1, dev.covers().size)
        }
    }

    @Test fun busyWhileTheDeviceIsAtIt() {
        val gate = CompletableDeferred<Unit>()
        val dev = Device(ok, gate = gate)
        val vm = showing(dev)
        on { vm.toggleSelect("a") }
        on { vm.useSelectionAsCover() }
        await("the request") { dev.covers().isNotEmpty() }
        assertEquals(SelectionActionTask.PERSON_COVER, vm.state.value.preparing)
        // Another tap, the viewer's, an upload or Choose automatically meanwhile send nothing.
        on { vm.useSelectionAsCover().join() }
        on { vm.uploadPersonCover("p1", jpeg).join() }
        on { vm.resetPersonCover("p1").join() }
        gate.complete(Unit)
        await("the note") { vm.state.value.alert != null }
        assertEquals(1, dev.covers().size)
        assertNull(vm.state.value.preparing)
    }

    @Test fun theRequestAndItsRules() {
        val byPhoto = personCoverRequest("p1", hash = "h1")
        assertEquals("p1", byPhoto.personId)
        assertEquals(SetPersonCover.CoverCase.HASH, byPhoto.coverCase)
        assertEquals("h1", byPhoto.hash)
        val byPicture = personCoverRequest("p1", image = jpeg)
        assertEquals(SetPersonCover.CoverCase.IMAGE, byPicture.coverCase)
        assertArrayEquals(jpeg, byPicture.image.toByteArray())
        assertEquals(SetPersonCover.CoverCase.COVER_NOT_SET, personCoverRequest("p1").coverCase)
        // A JPEG of at most 256 KB, as the device takes.
        assertEquals(256 * 1024, PERSON_COVER_MAX_BYTES)
        assertTrue(personCoverFits(jpeg))
        assertTrue(personCoverFits(ByteArray(PERSON_COVER_MAX_BYTES).also { it[0] = 0xFF.toByte(); it[1] = 0xD8.toByte() }))
        assertFalse(personCoverFits(ByteArray(PERSON_COVER_MAX_BYTES + 1).also { it[0] = 0xFF.toByte(); it[1] = 0xD8.toByte() }))
        assertFalse(personCoverFits(byteArrayOf(0x89.toByte(), 0x50, 0x4E, 0x47)))
        assertFalse(personCoverFits(byteArrayOf(0xFF.toByte(), 0xD8.toByte())))
        assertFalse(personCoverFits(ByteArray(0)))
        // The answer: taken, the device's words, else ours.
        assertNull(personCoverRefusal(ok))
        assertEquals("No.", personCoverRefusal(refused("No."))?.english())
        assertEquals("Couldn't change the face. Try again.", personCoverRefusal(null)?.english())
        assertEquals("Couldn't change the face. Try again.", personCoverRefusal(RespEnvelope.getDefaultInstance())?.english())
    }

    @Test fun englishText() {
        assertEquals("Use this face", S.appPhotosPersonCoverUse().english())
        assertEquals("Change face", S.appPhotosPersonCoverChange().english())
        assertEquals("Upload a photo…", S.appPhotosPersonCoverUpload().english())
        assertEquals("Choose automatically", S.appPhotosPersonCoverAuto().english())
        assertEquals("Or select one of their photos and choose Use this face.", S.appPhotosPersonCoverHint().english())
        assertEquals("Face updated", S.appPhotosPersonCoverDone().english())
        assertEquals("Couldn't change the face. Try again.", S.appPhotosPersonCoverFailed().english())
        assertEquals("Choose the face", S.appPhotosPersonCropTitle().english())
        assertEquals("Drag the photo to centre the face.", S.appPhotosPersonCropHint().english())
        assertEquals("Your device needs an update to change a person's face.", S.appPhotosPersonCoverNeedsUpdate().english())
    }
}
