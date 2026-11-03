// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosDeletePhoneSome
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.TestThumbStore
import com.google.protobuf.Timestamp
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.IOException
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

// Also delete from this phone? (DeletePhoneCopy.kt): which deleted photos
// are asked about, which of the phone's assets, and what the question says.
// Cases D1-D9 are iOS's DeletePhoneCopyTests'.
class DeletePhoneCopyTest {
    private fun d(hash: String, copies: Int? = null, every: Boolean = false) = DeletedOnDevice(hash, copies, every)
    private fun asset(id: Long, hash: String, video: Boolean = false) = PhoneAsset(id, hash, video)

    /** D1: only what the device no longer holds - every copy deleted, or the only one; a copy that stays is never asked about. */
    @Test fun onlyWhatTheDeviceNoLongerHolds() {
        // The only copy, or a device that didn't count them.
        assertEquals(setOf("a"), DeletePhoneCopy.gone(listOf(d("a", copies = 1))))
        assertEquals(setOf("a"), DeletePhoneCopy.gone(listOf(d("a"))))
        // Delete only this copy: the others stay on the device.
        assertTrue(DeletePhoneCopy.gone(listOf(d("a", copies = 3))).isEmpty())
        assertTrue(DeletePhoneCopy.gone(listOf(d("a", copies = 2))).isEmpty())
        // Every copy.
        assertEquals(setOf("a"), DeletePhoneCopy.gone(listOf(d("a", copies = 3, every = true))))
        assertEquals(setOf("a"), DeletePhoneCopy.gone(listOf(d("a", copies = 1, every = true))))
        // No hash: nothing to match.
        assertTrue(DeletePhoneCopy.gone(listOf(d("", copies = 1))).isEmpty())
        assertEquals(setOf("b", "c"), DeletePhoneCopy.gone(listOf(d("a", copies = 2), d("b"), d("c", copies = 2, every = true), d("", copies = null))))
        assertTrue(DeletePhoneCopy.gone(emptyList()).isEmpty())
    }

    /** D2: the phone's assets are those recorded under a gone hash that are still in the library - two assets of one hash both. */
    @Test fun thePhonesAssetsRecordedUnderAGoneHash() {
        val recorded = mapOf(1L to "a", 2L to "a", 3L to "b", 4L to "c")
        val inLibrary = mapOf(1L to false, 2L to true, 3L to false)
        // 3's content wasn't deleted; 4 is gone from the library.
        assertEquals(listOf(asset(1, "a"), asset(2, "a", video = true)), DeletePhoneCopy.onPhone(setOf("a", "c"), recorded, inLibrary, emptySet()))
        assertTrue(DeletePhoneCopy.onPhone(emptySet(), recorded, inLibrary, emptySet()).isEmpty())
        assertTrue(DeletePhoneCopy.onPhone(setOf("c"), recorded, inLibrary, emptySet()).isEmpty())
    }

    /** D3: never what the app saved to the library itself (the viewer's Save, in Pictures|Movies/Off The Cloud). */
    @Test fun neverWhatTheAppSavedThere() {
        val recorded = mapOf(1L to "a", 9L to "a")
        val inLibrary = mapOf(1L to false, 9L to false)
        assertEquals(listOf(asset(1, "a")), DeletePhoneCopy.onPhone(setOf("a"), recorded, inLibrary, setOf(9L)))
        assertTrue(DeletePhoneCopy.onPhone(setOf("a"), mapOf(9L to "a"), inLibrary, setOf(9L)).isEmpty())
    }

    /** D4: what the device says it still holds is left out; nothing left, no question. */
    @Test fun whatTheDeviceStillHoldsIsLeftOut() {
        val deleted = listOf(d("a"), d("b"))
        val onPhone = listOf(asset(1, "a"), asset(2, "b"))
        val ask = DeletePhoneCopy.ask(deleted, onPhone, setOf("a"))
        assertEquals(listOf(2L), ask?.ids)
        assertEquals(1, ask?.found)
        assertEquals(2, ask?.deleted)
        assertNull(DeletePhoneCopy.ask(deleted, onPhone, setOf("a", "b")))
        assertNull(DeletePhoneCopy.ask(deleted, emptyList(), emptySet()))
        // A deleted item only this copy of which went is never counted, even when the phone has its content.
        assertNull(DeletePhoneCopy.ask(listOf(d("a", copies = 2)), listOf(asset(1, "a")), emptySet()))
    }

    /** D5: one photo deleted and on the phone. */
    @Test fun onePhoto() {
        val ask = DeletePhoneCopy.ask(listOf(d("a", copies = 1)), listOf(asset(1, "a")), emptySet())
        assertEquals(PhoneDeleteAsk(listOf(asset(1, "a")), found = 1, deleted = 1), ask)
        assertEquals("Also delete from this phone?", ask!!.title().english())
        assertEquals("This photo is also on this phone.", ask.also().english())
        assertEquals(
            "This photo is also on this phone. Android asks you to confirm, and the phone's trash keeps what you delete for 30 days.",
            ask.text().english(),
        )
    }

    /** D6: one video; two assets of it on the phone are one item found. */
    @Test fun oneVideoOnThePhoneTwice() {
        val ask = DeletePhoneCopy.ask(listOf(d("v", copies = 3, every = true)), listOf(asset(1, "v", video = true), asset(2, "v", video = true)), emptySet())!!
        assertEquals(listOf(1L, 2L), ask.ids)
        assertEquals(1, ask.found)
        assertEquals("Also delete from this phone?", ask.title().english())
        assertEquals("This video is also on this phone.", ask.also().english())
    }

    /** D7: several deleted, one of them on the phone. */
    @Test fun severalDeletedOneOnThePhone() {
        val ask = DeletePhoneCopy.ask(listOf(d("a"), d("b"), d("c", copies = 2), d("e")), listOf(asset(2, "b")), emptySet())!!
        assertEquals(1, ask.found)
        assertEquals(4, ask.deleted)
        assertEquals("Also delete from this phone?", ask.title().english())
        assertEquals("1 of these photos and videos is also on this phone.", ask.also().english())
    }

    /** D8: several deleted, several on the phone. */
    @Test fun severalOnThePhone() {
        val deleted = listOf(d("a"), d("b", copies = 1), d("c", copies = 2, every = true))
        val ask = DeletePhoneCopy.ask(deleted, listOf(asset(1, "a"), asset(3, "c", video = true)), emptySet())!!
        assertEquals(2, ask.found)
        assertEquals("Also delete them from this phone?", ask.title().english())
        assertEquals("2 of these photos and videos are also on this phone.", ask.also().english())
        assertNotEquals(S.appPhotosDeletePhoneSome(1).english(), S.appPhotosDeletePhoneSome(2).english())
        assertEquals(
            "2 of these photos and videos are also on this phone. Android asks you to confirm, and the phone's trash keeps what you delete for 30 days.",
            ask.text().english(),
        )
    }

    /** D9: asked only where the trash request exists (Android 11) and with full media access. */
    @Test fun android11AndFullAccessOnly() {
        assertTrue(DeletePhoneCopy.asks(sdk = 30, permission = true, limited = false))
        assertTrue(DeletePhoneCopy.asks(sdk = 36, permission = true, limited = false))
        assertFalse(DeletePhoneCopy.asks(sdk = 29, permission = true, limited = false))
        assertFalse(DeletePhoneCopy.asks(sdk = 34, permission = true, limited = true))
        assertFalse(DeletePhoneCopy.asks(sdk = 34, permission = false, limited = false))
    }

    /** Files' rows: only photos and videos (or a type not said). */
    @Test fun filesRowsThatMayBeInTheLibrary() {
        assertTrue(DeletePhoneCopy.isMedia("image/heic"))
        assertTrue(DeletePhoneCopy.isMedia("video/mp4"))
        assertTrue(DeletePhoneCopy.isMedia(""))
        assertFalse(DeletePhoneCopy.isMedia("application/pdf"))
        assertFalse(DeletePhoneCopy.isMedia("text/plain"))
    }

    /** The records the other way round (AssetSyncCache's, and Free Up Space's): every id recorded under the hashes asked for. */
    @Test fun theRecordsFoundByHash() {
        val records = mapOf("101" to "aa", "102" to "bb", "103" to "aa", "x" to "aa")
        assertEquals(mapOf(101L to "aa", 103L to "aa"), DeletePhoneCopy.recordedUnder(records, setOf("aa")))
        assertEquals(mapOf(102L to "bb"), DeletePhoneCopy.recordedUnder(records, setOf("bb", "zz")))
        assertTrue(DeletePhoneCopy.recordedUnder(records, emptySet()).isEmpty())
    }
}

// What Images hands on after a delete: only what the device deleted, as it
// was shown (its hash, copies and every_copy). iOS's
// DeletePhoneCopyGalleryTests.
class DeletePhoneCopyGalleryTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    // The photos: copies 3, not counted, 1, 2 (CopiesDevice on iOS).
    private val copies = listOf(3, null, 1, 2)
    private fun photo(i: Int): File {
        val b = File.newBuilder().setPath("/photos/$i.jpg").setHash("h$i").setMime("image/jpeg")
            .setCreated(Timestamp.newBuilder().setSeconds(1_780_000_000L - i * 3600L))
        copies[i]?.let { b.copies = it }
        return b.build()
    }

    private inner class Device {
        val deletes = CopyOnWriteArrayList<String>()
        @Volatile var refuse: Map<String, String> = emptyMap()
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(copies.indices.map(::photo))).build()
                ReqEnvelope.PayloadCase.REQ_DEL_FILE -> {
                    deletes += req.reqDelFile.path
                    refuse[req.reqDelFile.path]?.let { RespEnvelope.newBuilder().setError(true).setErrorCode(it).setErrorMessage("refused").build() }
                        ?: RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
                }
                else -> throw IOException("not answered")
            }
        }
    }

    private val told = CopyOnWriteArrayList<List<DeletedOnDevice>>()

    private fun vm(dev: Device) = PhotoGalleryViewModel(
        "test", send = dev.send, pause = {}, online = { true }, problemOf = { _, _ -> LoadProblem.FAILED },
        thumbs = thumbs, fullPhotos = thumbs, scope = scope, deletedOnDevice = { told += it },
    )

    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    private fun loaded(dev: Device): PhotoGalleryViewModel {
        val vm = vm(dev)
        scope.launch { vm.resetAndLoadFirstPage() }
        await("the photos") { vm.state.value.items.size == 4 }
        return vm
    }

    @Test fun theViewerHandsOnWhatTheDeviceDeleted() {
        val dev = Device()
        val vm = loaded(dev)
        on { vm.open(0) }
        on { vm.deleteCurrentPhoto(everyCopy = true) }
        await("the delete") { told.size == 1 }
        assertEquals(listOf(DeletedOnDevice("h0", 3, true)), told[0])
        assertEquals(setOf("h0"), DeletePhoneCopy.gone(told[0]))
        // Only this copy of one with copies: handed on, never asked about.
        await("the grid") { vm.state.value.items.size == 3 }
        on { vm.open(2) }
        on { vm.deleteCurrentPhoto() }
        await("the second delete") { told.size == 2 }
        assertEquals(listOf(DeletedOnDevice("h3", 2, false)), told[1])
        assertTrue(DeletePhoneCopy.gone(told[1]).isEmpty())
        assertEquals(2, dev.deletes.size)
    }

    @Test fun aRefusedDeleteHandsOnNothing() {
        val dev = Device().apply { refuse = mapOf("/photos/2.jpg" to "copy_upload_only") }
        val vm = loaded(dev)
        on { vm.open(2) }
        on { vm.deleteCurrentPhoto() }
        await("the refusal") { vm.state.value.alert != null }
        Thread.sleep(50)
        assertTrue(told.isEmpty())
    }

    @Test fun theSelectionHandsOnOnlyWhatWasDeleted() {
        val dev = Device().apply { refuse = mapOf("/photos/1.jpg" to "changed") }
        val vm = loaded(dev)
        on {
            vm.toggleSelect("/photos/0.jpg")
            vm.toggleSelect("/photos/1.jpg")
            vm.toggleSelect("/photos/2.jpg")
        }
        on { vm.deleteSelected() }
        await("the deletes") { told.size == 1 }
        val byHash = told[0].associateBy { it.hash }
        assertEquals(2, byHash.size)
        assertEquals(DeletedOnDevice("h0", 3, false), byHash["h0"])
        assertEquals(DeletedOnDevice("h2", 1, false), byHash["h2"])
        // h0 keeps its other copies on the device: only h2 is asked about.
        assertEquals(setOf("h2"), DeletePhoneCopy.gone(told[0]))
    }
}
