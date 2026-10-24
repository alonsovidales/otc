// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosCollectionsRemoveNeedsUpdate
import cloud.offthe.otc.i18n.appPhotosNoticeRemoveFailed
import cloud.offthe.otc.i18n.appPhotosNoticeRemoved
import cloud.offthe.otc.i18n.appShellRemoveFromCollection
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ImageGroups
import cloud.offthe.otc.proto.RespInstanceRole
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.TestThumbStore
import cloud.offthe.otc.ui.common.hx
import com.google.protobuf.Timestamp
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

// Remove from collection: the picked photos leave the open collection and
// its grid, and a device from before the request says it needs an update.
class PhotoGalleryUncollectTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private val group = ImageGroup.newBuilder().setId("g1").setName("Trip").setFileCount(3).build()
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

    /** A collection of a, b and c; [remove] answers ReqRemoveFromImageGroup, [primary] the role (null: no answer). */
    private inner class Device(val remove: RespEnvelope?, val primary: Boolean? = true) {
        val sent = CopyOnWriteArrayList<ReqEnvelope>()
        @Volatile var members = listOf(a, b, c)
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS ->
                    RespEnvelope.newBuilder().setRespImageGroups(ImageGroups.newBuilder().addGroups(group.toBuilder().setFileCount(members.size))).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(members)).build()
                ReqEnvelope.PayloadCase.REQ_REMOVE_FROM_IMAGE_GROUP -> {
                    if (remove == ok) members = members.filter { it.path !in req.reqRemoveFromImageGroup.pathsList }
                    remove ?: throw IOException("no answer")
                }
                ReqEnvelope.PayloadCase.REQ_GET_INSTANCE_ROLE -> primary?.let { role(it) } ?: throw IOException("no answer")
                else -> throw IOException("not answered")
            }
        }
        fun removes() = sent.filter { it.payloadCase == ReqEnvelope.PayloadCase.REQ_REMOVE_FROM_IMAGE_GROUP }.map { it.reqRemoveFromImageGroup }
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

    // The collection open, a and c picked.
    private fun picked(dev: Device): PhotoGalleryViewModel {
        val vm = vm(dev)
        on { vm.openGroup(group) }
        await("the collection") { paths(vm) == listOf("a", "b", "c") && !vm.state.value.loading }
        on { vm.toggleSelect("a"); vm.toggleSelect("c") }
        return vm
    }

    @Test fun removedPhotosLeaveTheGridAndTheCountFollows() {
        val dev = Device(ok)
        val vm = picked(dev)
        on { vm.removeSelectionFromGroup() }
        await("the notice") { vm.state.value.alert != null }
        val sent = dev.removes().single()
        assertEquals("g1", sent.groupId)
        assertEquals(setOf("a", "c"), sent.pathsList.toSet())
        assertEquals(listOf("b"), paths(vm))
        assertTrue(vm.state.value.selected.isEmpty())
        assertEquals("Removed from \"\u2068Trip\u2069\".", vm.state.value.alert?.english())
        await("the collection's count") { vm.state.value.activeGroup?.fileCount == 1 }
        assertNull(vm.state.value.preparing)
        assertNull(vm.state.value.needsUpdate)
    }

    @Test fun anOlderDeviceNeedsAnUpdateTheOwnerCanStart() {
        val dev = Device(unknown, primary = true)
        val vm = picked(dev)
        on { vm.removeSelectionFromGroup() }
        await("the question") { vm.state.value.needsUpdate != null }
        val ask = vm.state.value.needsUpdate!!
        assertEquals("Your device needs an update to remove photos from a collection.", ask.title.english())
        assertTrue(ask.primary)
        // Nothing left the grid or the selection; no notice besides.
        assertEquals(listOf("a", "b", "c"), paths(vm))
        assertEquals(setOf("a", "c"), vm.state.value.selected)
        assertNull(vm.state.value.alert)
        on { vm.dismissNeedsUpdate() }
        assertNull(vm.state.value.needsUpdate)
    }

    @Test fun aFamilyMembersOrAnUnreadRoleIsToldWhomToAsk() {
        for (primary in listOf(false, null)) {
            val dev = Device(unknown, primary = primary)
            val vm = picked(dev)
            on { vm.removeSelectionFromGroup() }
            await("the question") { vm.state.value.needsUpdate != null }
            assertFalse("$primary", vm.state.value.needsUpdate!!.primary)
        }
    }

    @Test fun anyOtherFailureSaysSoAndKeepsThePicks() {
        for (answer in listOf(RespEnvelope.newBuilder().setError(true).setErrorMessage("database is locked").build(), null)) {
            val dev = Device(answer)
            val vm = picked(dev)
            on { vm.removeSelectionFromGroup() }
            await("the notice") { vm.state.value.alert != null }
            assertEquals("Could not remove from the collection.", vm.state.value.alert?.english())
            assertEquals(listOf("a", "b", "c"), paths(vm))
            assertEquals(setOf("a", "c"), vm.state.value.selected)
            assertNull(vm.state.value.needsUpdate)
            assertNull(vm.state.value.preparing)
        }
    }

    @Test fun outsideACollectionNothingIsSent() {
        val dev = Device(ok)
        val vm = vm(dev)
        on { vm.resetAndLoadFirstPage() }
        await("the photos") { paths(vm) == listOf("a", "b", "c") && !vm.state.value.loading }
        on { vm.toggleSelect("a"); vm.removeSelectionFromGroup().join() }
        assertTrue(dev.removes().isEmpty())
        assertEquals(setOf("a"), vm.state.value.selected)
    }

    // A name the person gave goes in isolated (U+2068 ... U+2069).
    @Test fun englishText() {
        assertEquals("Remove from collection", S.appShellRemoveFromCollection().english())
        assertEquals("Removed from \"\u2068Trip\u2069\".", S.appPhotosNoticeRemoved("Trip").english())
        assertEquals("Could not remove from the collection.", S.appPhotosNoticeRemoveFailed().english())
        assertEquals("Your device needs an update to remove photos from a collection.", S.appPhotosCollectionsRemoveNeedsUpdate().english())
    }
}
