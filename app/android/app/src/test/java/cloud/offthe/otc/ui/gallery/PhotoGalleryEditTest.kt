// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.data.ImagesChanged
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.EditImage
import cloud.offthe.otc.proto.EditState
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ImageGroups
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.RevertImage
import cloud.offthe.otc.ui.common.LoadProblem
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
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

// Photo edit, the viewer's side (PhotoGalleryViewModel): the editor's Save
// sends the turns and the crop with the content shown, waits, and shows
// what the device made of it - the photo replaced in place (its new path
// and hash, behind it too) or a copy named and listed next to it; a
// refusal stays in the editor in the device's words, "changed" closes the
// viewer and asks for the photos again as a delete does; Revert to
// Original puts the original in the edit's place; GetEditState counts the
// copies asked about. The iPhone app follows the same rules.
class PhotoGalleryEditTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun photo(path: String, at: String, hash: String = hx(path), mime: String = "image/jpeg", originalHash: String = ""): File {
        val t = java.time.Instant.parse(at)
        return File.newBuilder().setPath(path).setHash(hash).setMime(mime).setOriginalHash(originalHash)
            .setCreated(Timestamp.newBuilder().setSeconds(t.epochSecond)).build()
    }
    private val a = photo("/Camera/a.jpg", "2026-10-05T10:00:00Z")
    private val b = photo("/Camera/b.HEIC", "2026-10-04T10:00:00Z", mime = "image/heic")
    private val c = photo("/Camera/c.jpg", "2026-10-03T10:00:00Z")
    // b once edited: a JPEG now, under another name, its original kept.
    private val bEdited = photo("/Camera/b.JPG", "2026-10-04T10:00:00Z", hash = hx("b edited"), originalHash = b.hash)
    private val bCopy = photo("/Camera/b (edited).jpg", "2026-10-04T10:00:00Z", hash = hx("b copy"))

    private val noBuckets = RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
    private val noThumbs = RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
    private fun fileAnswer(f: File) = RespEnvelope.newBuilder().setRespFile(f).build()
    private fun refused(code: String, message: String) = RespEnvelope.newBuilder().setError(true).setErrorCode(code).setErrorMessage(message).build()
    private fun editState(copies: Int) = RespEnvelope.newBuilder().setRespEditState(EditState.newBuilder().setCopies(copies)).build()

    /**
     * A device listing [library] for any search, answering EditImage with
     * [edit], RevertImage with [revert] and GetEditState with [state] (null:
     * no answer). [holdEdit]: EditImage waits for it; [holdFile]: GetFile
     * does. Collections: [groups].
     */
    private inner class Device(
        @Volatile var library: List<File> = listOf(a, b, c),
        @Volatile var edit: (EditImage) -> RespEnvelope? = { null },
        @Volatile var revert: (RevertImage) -> RespEnvelope? = { null },
        @Volatile var state: RespEnvelope? = null,
        val groups: List<ImageGroup> = emptyList(),
    ) {
        val sent = CopyOnWriteArrayList<ReqEnvelope>()
        @Volatile var holdEdit: CompletableDeferred<Unit>? = null
        @Volatile var holdFile: CompletableDeferred<Unit>? = null
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> noBuckets
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> noThumbs
                ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS -> RespEnvelope.newBuilder().setRespImageGroups(ImageGroups.newBuilder().addAllGroups(groups)).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(library)).build()
                ReqEnvelope.PayloadCase.REQ_EDIT_IMAGE -> { holdEdit?.await(); edit(req.reqEditImage) ?: throw IOException("no answer") }
                ReqEnvelope.PayloadCase.REQ_REVERT_IMAGE -> revert(req.reqRevertImage) ?: throw IOException("no answer")
                ReqEnvelope.PayloadCase.REQ_GET_EDIT_STATE -> state ?: throw IOException("no answer")
                // The full size: none to be had here (the thumbnail stays).
                ReqEnvelope.PayloadCase.REQ_GET_FILE -> { holdFile?.await(); throw IOException("not answered") }
                else -> throw IOException("not answered")
            }
        }
        fun of(case: ReqEnvelope.PayloadCase) = sent.filter { it.payloadCase == case }
        fun edits() = of(ReqEnvelope.PayloadCase.REQ_EDIT_IMAGE).map { it.reqEditImage }
        fun reverts() = of(ReqEnvelope.PayloadCase.REQ_REVERT_IMAGE).map { it.reqRevertImage }
        fun searches() = of(ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS).size
        fun fullSizes(path: String) = of(ReqEnvelope.PayloadCase.REQ_GET_FILE).count { it.reqGetFile.path == path }
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

    private fun settle() { repeat(5) { on { kotlinx.coroutines.yield() }; Thread.sleep(10) } }

    private fun paths(vm: PhotoGalleryViewModel) = vm.state.value.items.map { it.path }

    // Images, its photos in, with the one at [at] open in the viewer and the editor over it.
    private fun editing(dev: Device, at: Int = 1): PhotoGalleryViewModel {
        val vm = vm(dev)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the photos") { paths(vm) == dev.library.map { it.path } && !vm.state.value.loading }
        on { vm.open(at); vm.openEditor() }
        assertTrue(vm.state.value.editing)
        return vm
    }

    private val crop = CropRect(0.1, 0.2, 0.5, 0.6)

    @Test fun replacingSendsTheEditAndShowsThePhotoAsTheDeviceHasItNow() {
        val dev = Device(edit = { fileAnswer(bEdited) })
        val vm = editing(dev)
        on { vm.saveEdit(1, crop, asCopy = false) }
        await("saved") { !vm.state.value.editing && vm.state.value.editBusy == null }
        val sent = dev.edits().single()
        assertEquals("/Camera/b.HEIC", sent.path)
        // The content shown: refused as "changed" if the path holds other now.
        assertEquals(b.hash, sent.hash)
        assertEquals(1, sent.quarterTurns)
        assertEquals(0.1, sent.crop.x, 1e-12)
        assertEquals(0.6, sent.crop.height, 1e-12)
        assertFalse(sent.asCopy)
        assertFalse(sent.everyCopy)
        // In the photo's place, by the answer: its new name, hash and original.
        assertEquals(listOf("/Camera/a.jpg", "/Camera/b.JPG", "/Camera/c.jpg"), paths(vm))
        val shown = vm.state.value.items[1]
        assertEquals(bEdited.hash, shown.hash)
        assertEquals("image/jpeg", shown.mime)
        assertEquals(b.hash, shown.originalHash)
        assertEquals(1, vm.state.value.openIndex)
        // Its own full size is asked for, at the new path.
        await("the new full size") { dev.fullSizes("/Camera/b.JPG") >= 1 }
        // Nothing to say: the photo shows it.
        assertNull(vm.state.value.editNote)
        assertNull(vm.state.value.alert)
    }

    @Test fun replacingEveryCopy() {
        val dev = Device(edit = { fileAnswer(bEdited) }, state = editState(3))
        val vm = editing(dev)
        // The editor asks how many paths hold it first.
        assertEquals(3, on { vm.openCopies() })
        assertEquals("/Camera/b.HEIC", dev.of(ReqEnvelope.PayloadCase.REQ_GET_EDIT_STATE).single().reqGetEditState.path)
        on { vm.saveEdit(2, CropRect.WHOLE, asCopy = false, everyCopy = true) }
        await("saved") { !vm.state.value.editing }
        val sent = dev.edits().single()
        assertTrue(sent.everyCopy)
        assertEquals(2, sent.quarterTurns)
        // Turned only: no crop sent.
        assertFalse(sent.hasCrop())
    }

    @Test fun theCopiesAreOneWhenTheDeviceCantSay() {
        val dev = Device()
        val vm = editing(dev)
        assertEquals(1, on { vm.openCopies() })
        dev.state = refused("unknown_payload", "Your device needs an update for this.")
        assertEquals(1, on { vm.openCopies() })
        dev.state = editState(0)
        assertEquals(1, on { vm.openCopies() })
    }

    @Test fun aCopyIsNamedAndListedNextToThePhoto() {
        val dev = Device(edit = { fileAnswer(bCopy) })
        val vm = editing(dev)
        val buckets = dev.of(ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS).size
        on { vm.saveEdit(0, crop, asCopy = true, everyCopy = true) }
        await("saved") { !vm.state.value.editing && vm.state.value.editBusy == null }
        val sent = dev.edits().single()
        assertTrue(sent.asCopy)
        assertFalse(sent.everyCopy)
        assertEquals("Saved as ⁨b (edited).jpg⁩", vm.state.value.editNote?.english())
        // The viewer stays on the photo, the copy beside it at its date.
        assertEquals(listOf("/Camera/a.jpg", "/Camera/b.HEIC", "/Camera/b (edited).jpg", "/Camera/c.jpg"), paths(vm))
        assertEquals(1, vm.state.value.openIndex)
        assertEquals(b.hash, vm.state.value.items[1].hash)
        // The months' counts follow.
        await("the months again") { dev.of(ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS).size > buckets }
        on { vm.editNoteShown() }
        assertNull(vm.state.value.editNote)
    }

    @Test fun aCopyStaysOutOfACollection() {
        val trip = ImageGroup.newBuilder().setId("g1").setName("Trip").setFileCount(3).build()
        val dev = Device(edit = { fileAnswer(bCopy) }, groups = listOf(trip))
        val vm = vm(dev)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the photos") { paths(vm).size == 3 }
        val searches = dev.searches()
        on { vm.openGroup(trip) }
        await("the collection") { dev.searches() > searches && paths(vm).size == 3 && !vm.state.value.loading }
        on { vm.open(1); vm.openEditor() }
        on { vm.saveEdit(0, crop, asCopy = true) }
        await("saved") { !vm.state.value.editing }
        // Not in the collection: the device lists it in Images, not here.
        assertEquals(listOf("/Camera/a.jpg", "/Camera/b.HEIC", "/Camera/c.jpg"), paths(vm))
        assertEquals("Saved as ⁨b (edited).jpg⁩", vm.state.value.editNote?.english())
    }

    @Test fun aRefusalStaysInTheEditorInTheDevicesWords() {
        val dev = Device(edit = { refused("demo", "This is a demo device: nothing can be changed.") })
        val vm = editing(dev)
        on { vm.saveEdit(1, crop, asCopy = false) }
        await("refused") { vm.state.value.editError != null && vm.state.value.editBusy == null }
        assertEquals("This is a demo device: nothing can be changed.", vm.state.value.editError?.english())
        // Still open, the photo as it was, to try again.
        assertTrue(vm.state.value.editing)
        assertEquals(listOf("/Camera/a.jpg", "/Camera/b.HEIC", "/Camera/c.jpg"), paths(vm))
        on { vm.editErrorShown() }
        assertNull(vm.state.value.editError)
        // No answer at all: our words.
        dev.edit = { null }
        on { vm.saveEdit(1, crop, asCopy = false) }
        await("no answer") { vm.state.value.editError != null }
        assertEquals("Couldn't save the edit. Try again.", vm.state.value.editError?.english())
        // An older device's own words, as they come.
        on { vm.editErrorShown() }
        dev.edit = { refused("unknown_payload", "Update your device to use this.") }
        on { vm.saveEdit(1, crop, asCopy = true) }
        await("an older device") { vm.state.value.editError != null }
        assertEquals("Update your device to use this.", vm.state.value.editError?.english())
        assertTrue(vm.state.value.editing)
    }

    @Test fun contentChangedSinceShownClosesTheViewerAndAsksForThePhotosAgain() {
        val words = "this file has changed since it was shown: refresh and try again"
        val dev = Device(edit = { refused("changed", words) })
        val vm = editing(dev)
        val searches = dev.searches()
        on { vm.saveEdit(1, crop, asCopy = false) }
        await("asked again") { dev.searches() > searches && !vm.state.value.loading && paths(vm).size == 3 }
        assertNull(vm.state.value.openIndex)
        assertFalse(vm.state.value.editing)
        assertEquals(words, vm.state.value.alert?.english())
    }

    @Test fun whileSavingTheEditorWaits() {
        val dev = Device(edit = { fileAnswer(bEdited) })
        dev.holdEdit = CompletableDeferred()
        val vm = editing(dev)
        on { vm.saveEdit(1, crop, asCopy = false) }
        await("sent") { dev.edits().size == 1 }
        assertEquals(EditBusy.SAVING, vm.state.value.editBusy)
        // Neither Cancel nor a second Save while the device works.
        on { vm.closeEditor() }
        assertTrue(vm.state.value.editing)
        assertNull(on { vm.saveEdit(1, crop, asCopy = true) })
        assertNull(on { vm.revertOpen() })
        dev.holdEdit!!.complete(Unit)
        await("saved") { vm.state.value.editBusy == null }
        assertEquals(1, dev.edits().size)
        assertFalse(vm.state.value.editing)
    }

    @Test fun theOldContentsFullSizeOnItsWayIsNotTheEdits() {
        // The same path before and after (a JPEG stays a JPEG).
        val aEdited = photo("/Camera/a.jpg", "2026-10-05T10:00:00Z", hash = hx("a edited"), originalHash = a.hash)
        val dev = Device(edit = { fileAnswer(aEdited) })
        dev.holdFile = CompletableDeferred()
        val vm = editing(dev, at = 0)
        await("the old full size asked") { dev.fullSizes("/Camera/a.jpg") == 1 }
        on { vm.saveEdit(1, CropRect.WHOLE, asCopy = false) }
        await("saved") { !vm.state.value.editing }
        // The edit's own is asked for, the old one still on its way.
        await("the edit's full size") { dev.fullSizes("/Camera/a.jpg") == 2 }
        assertEquals(aEdited.hash, vm.state.value.items[0].hash)
        dev.holdFile!!.complete(Unit)
        settle()
    }

    @Test fun revertPutsTheOriginalInTheEditsPlace() {
        val edited = photo("/Camera/b.JPG", "2026-10-04T10:00:00Z", hash = hx("b edited"), originalHash = b.hash)
        val dev = Device(library = listOf(a, edited, c), revert = { fileAnswer(b) })
        val vm = editing(dev)
        on { vm.closeEditor() }
        assertTrue(PhotoEdit.revertable(vm.state.value.items[1]))
        on { vm.revertOpen(everyCopy = true) }
        await("reverted") { vm.state.value.editBusy == null && paths(vm)[1] == "/Camera/b.HEIC" }
        val sent = dev.reverts().single()
        assertEquals("/Camera/b.JPG", sent.path)
        assertEquals(edited.hash, sent.hash)
        assertTrue(sent.everyCopy)
        // The original, its own name back: no Revert any more.
        val shown = vm.state.value.items[1]
        assertEquals(b.hash, shown.hash)
        assertFalse(PhotoEdit.revertable(shown))
        assertEquals(1, vm.state.value.openIndex)
        assertNull(vm.state.value.alert)
    }

    @Test fun aRevertRefusedSaysWhy() {
        val edited = photo("/Camera/b.JPG", "2026-10-04T10:00:00Z", hash = hx("b edited"), originalHash = b.hash)
        val dev = Device(library = listOf(a, edited, c), revert = { null })
        val vm = editing(dev)
        on { vm.closeEditor() }
        on { vm.revertOpen() }
        await("refused") { vm.state.value.alert != null && vm.state.value.editBusy == null }
        assertEquals("Couldn't put the original back. Try again.", vm.state.value.alert?.english())
        assertEquals(edited.hash, vm.state.value.items[1].hash)
        on { vm.dismissAlert() }
        dev.revert = { refused("not_edited", "This photo isn't an edit.") }
        on { vm.revertOpen() }
        await("refused again") { vm.state.value.alert != null }
        assertEquals("This photo isn't an edit.", vm.state.value.alert?.english())
        assertEquals(1, vm.state.value.openIndex)
        // Nothing to revert for a photo that is no edit.
        on { vm.dismissAlert(); vm.open(0) }
        assertNull(on { vm.revertOpen() })
    }

    @Test fun editIsOnlyForWhatTheDeviceCanEdit() {
        // An animation (a video would be played here, from no device).
        val clip = photo("/Camera/clip.gif", "2026-10-02T10:00:00Z", mime = "image/gif")
        val dev = Device(library = listOf(a, clip))
        val vm = vm(dev)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the photos") { paths(vm).size == 2 }
        on { vm.open(1); vm.openEditor() }
        assertFalse(vm.state.value.editing)
        on { vm.open(0); vm.openEditor() }
        assertTrue(vm.state.value.editing)
        // Closing the viewer closes the editor.
        on { vm.closeModal() }
        assertFalse(vm.state.value.editing)
    }

    @Test fun aFixedListsScreenListsAgainAndImagesAsksForItsPhotos() {
        // The Files viewer (as the map's and a memory's): its folder listed again after an edit.
        val dev = Device(edit = { fileAnswer(bEdited) })
        val vm = vm(dev)
        var listedAgain = 0
        val item = PhotoGalleryViewModel.Item("${b.path}#${b.hash}#0", b.path, b.mime, 0, null, hash = b.hash)
        on { vm.showFiles(listOf(item), 0) { listedAgain++ } }
        on { vm.openEditor() }
        val images = ImagesChanged.generation.value
        on { vm.saveEdit(3, crop, asCopy = false) }
        await("saved") { !vm.state.value.editing && vm.state.value.editBusy == null }
        assertEquals(listOf("/Camera/b.JPG"), paths(vm))
        assertEquals(1, listedAgain)
        assertTrue(ImagesChanged.generation.value > images)
        // A copy: listed again too, the viewer's own list as it was.
        dev.edit = { fileAnswer(bCopy) }
        on { vm.openEditor() }
        on { vm.saveEdit(0, crop, asCopy = true) }
        await("the copy") { vm.state.value.editNote != null && vm.state.value.editBusy == null }
        assertEquals(listOf("/Camera/b.JPG"), paths(vm))
        assertEquals(2, listedAgain)
    }
}
