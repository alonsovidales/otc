// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosViewerPathsTitle
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.FileExifInfo
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
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.IOException
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors

// The viewer's Info shows the details of the photo open: an answer for the
// photo before (Info opened on A, the viewer moved on to B, Info opened
// again) used to show A's camera, date and map under B, and its end
// cleared B's spinner early.
class PhotoGalleryInfoTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun file(path: String, at: String): File {
        val t = java.time.Instant.parse(at)
        return File.newBuilder().setPath(path).setHash(hx(path)).setMime("image/jpeg").setCreated(Timestamp.newBuilder().setSeconds(t.epochSecond)).build()
    }
    private val members = listOf(file("a", "2026-10-03T10:00:00Z"), file("b", "2026-10-02T10:00:00Z"))

    // GetFileInfo answers each path with its own camera, and its [infoPaths] (issue #213), once its gate opens; [failing] paths get no answer.
    private val gates = ConcurrentHashMap<String, CompletableDeferred<Unit>>()
    private val failing = ConcurrentHashMap.newKeySet<String>()
    private val infoPaths = ConcurrentHashMap<String, List<String>>()
    // DelFile: each one sent, answered once [deleteGate] opens.
    private val deletes = java.util.concurrent.CopyOnWriteArrayList<String>()
    @Volatile private var deleteGate: CompletableDeferred<Unit>? = null

    private val send: GalleryRequest = { _, _, build ->
        val req = ReqEnvelope.newBuilder().also(build).build()
        when (req.payloadCase) {
            ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
            ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
            ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(members)).build()
            ReqEnvelope.PayloadCase.REQ_GET_FILE_INFO -> {
                val path = req.reqGetFileInfo.path
                gates[path]?.await()
                if (path in failing) throw IOException("no answer")
                RespEnvelope.newBuilder().setRespFileInfo(
                    FileExifInfo.newBuilder().setCameraMake("camera of $path").addAllPaths(infoPaths[path].orEmpty()),
                ).build()
            }
            ReqEnvelope.PayloadCase.REQ_DEL_FILE -> {
                deletes += req.reqDelFile.path
                deleteGate?.await()
                RespEnvelope.newBuilder().setRespAck(cloud.offthe.otc.proto.Ack.newBuilder().setOk(true)).build()
            }
            else -> throw IOException("not answered")
        }
    }

    private fun vm() = PhotoGalleryViewModel(
        "test", send = send, pause = {}, online = { true }, problemOf = { _, _ -> LoadProblem.FAILED },
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

    private fun library(): PhotoGalleryViewModel {
        val vm = vm()
        on { vm.resetAndLoadFirstPage() }
        await("the photos") { vm.state.value.items.map { it.path } == listOf("a", "b") && !vm.state.value.loading }
        return vm
    }

    /** A bulk Delete holds the bar, its Delete a spinner, and a second one meanwhile sends nothing (as iOS's aDeleteUnderWayHoldsTheBarAndGoesOnce). */
    @Test fun aDeleteUnderWayHoldsTheBarAndGoesOnce() {
        val vm = library()
        val gate = CompletableDeferred<Unit>().also { deleteGate = it }
        on { vm.toggleSelect("a"); vm.toggleSelect("b") }
        on { vm.deleteSelected() }
        assertEquals(cloud.offthe.otc.ui.common.SelectionActionTask.DELETE, vm.state.value.preparing)
        await("the first delete") { deletes.size == 1 }
        // Tapped again while it runs: nothing more is sent.
        assertNull(on { vm.deleteSelected() })
        gate.complete(Unit)
        await("the deletes") { vm.state.value.preparing == null }
        assertEquals(listOf("a", "b"), deletes.toList())
    }

    @Test fun aLateAnswerForThePhotoBeforeIsDropped() {
        val vm = library()
        val aGate = CompletableDeferred<Unit>().also { gates["a"] = it }
        val bGate = CompletableDeferred<Unit>().also { gates["b"] = it }
        on { vm.open(0); vm.openInfo() }
        on { vm.open(1); vm.openInfo() }
        // A's answer lands while B's Info is open: neither its details nor its end show.
        aGate.complete(Unit)
        Thread.sleep(100)
        on { }
        assertNull(vm.state.value.infoData)
        assertTrue(vm.state.value.infoLoading)
        bGate.complete(Unit)
        await("B's details") { !vm.state.value.infoLoading }
        assertEquals("camera of b", vm.state.value.infoData?.cameraMake)
    }

    @Test fun aFailedAnswerLeavesNothingOfThePhotoBefore() {
        val vm = library()
        on { vm.open(0); vm.openInfo() }
        await("A's details") { !vm.state.value.infoLoading }
        assertEquals("camera of a", vm.state.value.infoData?.cameraMake)
        failing += "b"
        on { vm.open(1); vm.openInfo() }
        await("B's answer") { !vm.state.value.infoLoading }
        assertNull(vm.state.value.infoData)
        // Info closed before its answer: nothing lands after.
        failing.clear()
        val gate = CompletableDeferred<Unit>().also { gates["b"] = it }
        on { vm.closeInfo(); vm.openInfo(); vm.closeInfo() }
        gate.complete(Unit)
        Thread.sleep(100)
        on { }
        assertNull(vm.state.value.infoData)
    }

    /** Issue #213: Info's Stored at - the paths the device sent, and how many copies from 2 on (iOS's PhotoPathsTests, the web's photo-copies-test). */
    @Test fun storedAtListsThePathsAndTheCopiesFromTwoOn() {
        // No answer, or a device before it (or one that couldn't list them): no section.
        assertNull(photoPaths(null))
        assertNull(photoPaths(FileExifInfo.getDefaultInstance()))
        assertEquals(listOf("/Phone/a.jpg"), photoPaths(FileExifInfo.newBuilder().addPaths("/Phone/a.jpg").build()))
        assertNull(photoPathsCopies(listOf("/Phone/a.jpg")))
        val paths = listOf("/Mac/Pictures/a.jpg", "/Phone/a.jpg", "/Private/a.jpg")
        assertEquals(paths, photoPaths(FileExifInfo.newBuilder().addAllPaths(paths).build()))
        assertEquals("3 copies", photoPathsCopies(paths)?.english())
        assertEquals("2 copies", photoPathsCopies(paths.take(2))?.english())
        assertEquals("Stored at", S.appPhotosViewerPathsTitle().english())
    }

    /** The viewer's Info keeps the paths the device answered for the photo open; a device before #213 sends none. */
    @Test fun infoKeepsWhereThePhotoIsStored() {
        val vm = library()
        infoPaths["a"] = listOf("/Mac/a", "a", "/Private/a")
        on { vm.open(0); vm.openInfo() }
        await("A's details") { !vm.state.value.infoLoading }
        assertEquals(listOf("/Mac/a", "a", "/Private/a"), photoPaths(vm.state.value.infoData))
        on { vm.open(1); vm.openInfo() }
        await("B's details") { !vm.state.value.infoLoading }
        assertEquals("camera of b", vm.state.value.infoData?.cameraMake)
        assertNull(photoPaths(vm.state.value.infoData))
    }
}
