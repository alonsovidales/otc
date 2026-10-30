// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.data.SmartSearchModelState
import cloud.offthe.otc.data.SmartSearchSupport
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.proto.Settings
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.TestThumbStore
import cloud.offthe.otc.ui.common.hx
import cloud.offthe.otc.ui.files.FilesNav
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

// Smart search in Images: the text sent only while the device has it on
// (with its model there), with the chips as filters, ranked (no months, no
// scrubber); the note while the device still reads; its refusals - a model
// still on its way among them - and the search's row.
class PhotoGallerySmartSearchTest {
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
    private val a = photo("a", "2026-10-03T10:00:00Z")
    private val b = photo("b", "2024-01-02T10:00:00Z")

    /** The device: by date without a text; with one, b first (ranked); [textError] refuses a text. */
    private inner class Device {
        val searches = CopyOnWriteArrayList<SearchPhotos>()
        @Volatile var reading = false
        @Volatile var textError: Pair<String, String>? = null
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_GET_TAGS -> RespEnvelope.newBuilder().setRespTagsList(cloud.offthe.otc.proto.TagsList.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_LIST_PEOPLE -> RespEnvelope.newBuilder().setRespPeople(cloud.offthe.otc.proto.People.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> {
                    val sp = req.reqSearchPhotos
                    searches += sp
                    val err = textError
                    when {
                        sp.text.isNotEmpty() && err != null ->
                            RespEnvelope.newBuilder().setError(true).setErrorCode(err.first).setErrorMessage(err.second).build()
                        sp.text.isNotEmpty() ->
                            RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addFiles(b).addFiles(a).setSmartSearchReading(reading)).build()
                        else -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addFiles(a).addFiles(b)).build()
                    }
                }
                // Tags, people and the rest: a device without them (asked once, not again).
                else -> RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
            }
        }
    }

    private var turnedOff = 0
    @Volatile private var modelAsked = 0

    private fun vm(dev: Device, on: Boolean = true) = PhotoGalleryViewModel(
        "test", send = dev.send, pause = {}, online = { true }, problemOf = { _, _ -> LoadProblem.FAILED },
        thumbs = thumbs, fullPhotos = thumbs, scope = scope, signIns = { 1 },
        smartOn = { on }, smartOff = { turnedOff++ }, smartDownloading = { modelAsked++ },
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

    private fun started(dev: Device, enabled: Boolean = true): PhotoGalleryViewModel {
        val vm = vm(dev, enabled)
        on { vm.onAppearInitial() }
        await("the library") { paths(vm) == listOf("a", "b") && !vm.state.value.loading }
        return vm
    }

    @Test fun aTextIsSentWithTheChipsAndKeepsTheDevicesRanking() {
        val dev = Device()
        val vm = started(dev)
        on { vm.addChip("beach") }
        on { vm.searchText("  kids in the snow ") }
        await("the text's page") { dev.searches.any { it.text == "kids in the snow" } && paths(vm) == listOf("b", "a") }
        val sp = dev.searches.last { it.text.isNotEmpty() }
        assertEquals(listOf("beach"), sp.tagsList)
        assertFalse(sp.hasBefore())
        assertFalse(sp.hasNewerThan())
        val st = vm.state.value
        assertEquals("kids in the snow", st.smartText)
        // Ranked: no months, no scrubber.
        assertFalse(st.dateOrdered)
        assertFalse(st.showScrubber)
        assertNull(st.bucketsKeyNow)
        // Its chip's x: the chips' photos again, by date, no text.
        on { vm.clearText() }
        await("by date again") { paths(vm) == listOf("a", "b") && vm.state.value.smartText == null }
        assertEquals("", dev.searches.last().text)
        assertTrue(vm.state.value.dateOrdered.not()) // the beach chip stays: still ranked by tags
    }

    @Test fun offOrAnOlderDeviceSendsNoText() {
        val dev = Device()
        val vm = started(dev, enabled = false)
        on { vm.searchText("kids in the snow") }
        Thread.sleep(50)
        assertNull(vm.state.value.smartText)
        assertTrue(dev.searches.all { it.text.isEmpty() })
        // An older device's Settings lack the field: off.
        assertFalse(SmartSearchSupport.verdict(Settings.getDefaultInstance()))
        assertTrue(SmartSearchSupport.verdict(Settings.newBuilder().setSmartSearchEnabled(true).build()))
        assertFalse(SmartSearchSupport.verdict(Settings.newBuilder().setSmartSearchEnabled(false).build()))
    }

    @Test fun theNoteShowsWhileTheDeviceStillReads() {
        val dev = Device()
        dev.reading = true
        val vm = started(dev)
        assertFalse(vm.state.value.smartReading)
        on { vm.searchText("dog") }
        await("the note") { vm.state.value.smartReading }
        on { vm.clearSearch() }
        await("gone") { vm.state.value.smartText == null && !vm.state.value.smartReading }
    }

    @Test fun turnedOffElsewhereTheTextGoesInTheDevicesWords() {
        val dev = Device()
        dev.textError = "smart_search_off" to "Smart search is off"
        val vm = started(dev)
        on { vm.searchText("dog") }
        await("the alert") { vm.state.value.alert != null }
        assertEquals("Smart search is off", vm.state.value.alert!!.english())
        assertNull(vm.state.value.smartText)
        assertEquals(1, turnedOff)
        await("by date again") { paths(vm) == listOf("a", "b") && !vm.state.value.loading }
    }

    @Test fun aDeviceWithoutTheModelSaysSoAndStaysOn() {
        val dev = Device()
        dev.textError = "smart_search_unavailable" to ""
        val vm = started(dev)
        on { vm.searchText("dog") }
        await("the alert") { vm.state.value.alert != null }
        assertEquals("Smart search isn't available right now.", vm.state.value.alert!!.english())
        assertNull(vm.state.value.smartText)
        assertEquals(0, turnedOff)
    }

    @Test fun aModelStillOnItsWayDropsTheTextKeepsTheSwitchAndAsksWhereItIs() {
        val dev = Device()
        dev.textError = "smart_search_downloading" to "Smart search is still downloading what it needs (37%)."
        val vm = started(dev)
        on { vm.searchText("dog") }
        await("the alert") { vm.state.value.alert != null }
        // The device's words, as smart_search_unavailable's.
        assertEquals("Smart search is still downloading what it needs (37%).", vm.state.value.alert!!.english())
        assertNull(vm.state.value.smartText)
        // The switch stays on; where the model is is asked.
        assertEquals(0, turnedOff)
        await("the model asked about") { modelAsked == 1 }
        await("by date again") { paths(vm) == listOf("a", "b") && !vm.state.value.loading && dev.searches.last().text.isEmpty() }
    }

    @Test fun aModelOnItsWayWithoutWordsSaysOurs() {
        val dev = Device()
        dev.textError = "smart_search_downloading" to ""
        val vm = started(dev)
        on { vm.searchText("dog") }
        await("the alert") { vm.state.value.alert != null }
        assertEquals("Smart search isn't available right now.", vm.state.value.alert!!.english())
        assertEquals(0, turnedOff)
    }

    // ---- the search's row ----

    private fun tags(vararg t: String) = t.map { it to FilesNav.fold(it) }

    @Test fun theSmartRowIsFirstUnlessAThingIsNamedExactly() {
        val free = searchOptions("kids in the snow", tags("kids", "snow"), emptyList(), null, null, noFileSearch = false, smart = true)
        assertEquals("smart", free.options.first().key)
        assertEquals("smart", free.best)
        assertTrue(searchKeyChoice("kids in the snow", free, null, false) is SearchOption.Smart)
        val exact = searchOptions("dog", tags("dog", "dogs"), emptyList(), null, null, noFileSearch = false, smart = true)
        assertEquals("t:dog", exact.best)
        assertEquals(listOf("t:dog", "t:dogs", "smart", "docs"), exact.options.map { it.key })
        // Off: as before - no row, the documents for words nothing matches.
        val off = searchOptions("kids in the snow", tags("cat"), emptyList(), null, null, noFileSearch = false)
        assertTrue(off.options.none { it is SearchOption.Smart })
        assertEquals("docs", off.best)
    }

    @Test fun noRowWhileTheModelIsNotThere() {
        // On, with the model on its way or not asked for yet: no row, and the
        // Search key takes what it took before Smart search.
        for (model in listOf(SmartSearchModelState.Downloading(37), SmartSearchModelState.Downloading(37, "No internet"), SmartSearchModelState.Missing)) {
            val smart = SmartSearchSupport.searchable(true, model)
            assertFalse(smart)
            val o = searchOptions("kids in the snow", tags("cat"), emptyList(), null, null, noFileSearch = false, smart = smart)
            assertTrue(o.options.none { it is SearchOption.Smart })
            assertEquals("docs", o.best)
            assertTrue(searchKeyChoice("kids in the snow", o, null, false) is SearchOption.Docs)
        }
        // There (or a device before release 152, which doesn't say): as today.
        assertTrue(SmartSearchSupport.searchable(true, SmartSearchModelState.Ready))
        assertTrue(SmartSearchSupport.searchable(true, SmartSearchModelState.of(Settings.newBuilder().setSmartSearchEnabled(true).build())))
        // Off or unknown: never.
        assertFalse(SmartSearchSupport.searchable(false, SmartSearchModelState.Ready))
        assertFalse(SmartSearchSupport.searchable(null, SmartSearchModelState.Ready))
    }
}
