// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.data.FaceRecognition
import cloud.offthe.otc.data.SmartSearchModelState
import cloud.offthe.otc.data.SmartSearchSupport
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.FileExifInfo
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.proto.Settings
import cloud.offthe.otc.proto.SmartSearchModel
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.TestThumbStore
import cloud.offthe.otc.ui.common.hx
import cloud.offthe.otc.ui.files.FilesNav
import cloud.offthe.otc.ui.settings.DeviceSettingsViewModel
import com.google.protobuf.Timestamp
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

// Text in photos (device release 180): the search's text offered while it
// is on - with Smart search off or its model not there yet too -, the text
// sent then, a refusal taking it off, Settings' switches (and Pets') only
// on a device that has them, and what Info shows of a photo's text. And
// Pets (the same release): People shows while it is on too
// (FaceRecognition). iOS's TextInPhotosTests has the same rules.
class TextInPhotosTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
        SmartSearchSupport.reset()
        FaceRecognition.reset()
    }

    /** Settings with Smart search's switch, its model (null: a device before 152) and Text in photos' (null: a device before 180). */
    private fun settings(smart: Boolean? = null, model: SmartSearchModel.Builder? = null, text: Boolean? = null, pets: Boolean? = null): Settings {
        val b = Settings.newBuilder()
        if (smart != null) b.smartSearchEnabled = smart
        if (model != null) b.setSmartSearchModel(model)
        if (text != null) b.textInPhotosEnabled = text
        if (pets != null) b.petsEnabled = pets
        return b.build()
    }

    private val downloading get() = SmartSearchModel.newBuilder().setDownloading(true).setPercent(30)

    // ---- when the text is offered ----

    @Test fun theTextIsOfferedWhileEitherAnswersIt() {
        assertTrue(SmartSearchSupport.searchable(false, SmartSearchModelState.Ready, true))
        assertTrue(SmartSearchSupport.searchable(null, SmartSearchModelState.Ready, true))
        assertTrue(SmartSearchSupport.searchable(true, SmartSearchModelState.Missing, true))
        assertTrue(SmartSearchSupport.searchable(true, SmartSearchModelState.Downloading(5), true))
        assertTrue(SmartSearchSupport.searchable(true, SmartSearchModelState.Ready, false))
        assertFalse(SmartSearchSupport.searchable(false, SmartSearchModelState.Ready, false))
        assertFalse(SmartSearchSupport.searchable(true, SmartSearchModelState.Missing, null))
        assertFalse(SmartSearchSupport.searchable(null, SmartSearchModelState.Ready, null))

        SmartSearchSupport.deviceSaid(settings(smart = false, text = true))
        assertEquals(true, SmartSearchSupport.textInPhotos.value)
        assertTrue(SmartSearchSupport.searchable)
        // Smart search's model on its way: the text answers meanwhile.
        SmartSearchSupport.deviceSaid(settings(smart = true, model = downloading, text = true))
        assertTrue(SmartSearchSupport.searchable)
        assertTrue(runBlocking { SmartSearchSupport.searchableFlow.first() })
        SmartSearchSupport.deviceSaid(settings(smart = true, model = downloading, text = false))
        assertFalse(SmartSearchSupport.searchable)
        assertFalse(runBlocking { SmartSearchSupport.searchableFlow.first() })
        // A device before it: off, and the search as before.
        SmartSearchSupport.deviceSaid(settings(smart = true))
        assertEquals(false, SmartSearchSupport.textInPhotos.value)
        assertTrue(SmartSearchSupport.searchable)
        assertFalse(SmartSearchSupport.textVerdict(Settings.getDefaultInstance()))
    }

    @Test fun theSwitchARefusalAndLogOut() {
        // Settings' switch acknowledged.
        SmartSearchSupport.setTextInPhotos(true)
        assertTrue(SmartSearchSupport.searchable)
        // The device refused a text: off there too.
        SmartSearchSupport.textRefused()
        assertEquals(false, SmartSearchSupport.textInPhotos.value)
        assertFalse(SmartSearchSupport.searchable)
        // Log Out: the next device says for itself - and not known stays so.
        SmartSearchSupport.setTextInPhotos(true)
        SmartSearchSupport.reset()
        assertNull(SmartSearchSupport.textInPhotos.value)
        SmartSearchSupport.textRefused()
        assertNull(SmartSearchSupport.textInPhotos.value)
    }

    private fun tags(vararg t: String) = t.map { it to FilesNav.fold(it) }

    @Test fun theSearchsRowWithTextInPhotosOn() {
        // Smart search off and its model not there: the row only with Text in photos.
        val without = searchOptions("GTS-04-R", tags("cat"), emptyList(), null, null, noFileSearch = false,
            smart = SmartSearchSupport.searchable(false, SmartSearchModelState.Missing, false))
        assertTrue(without.options.none { it is SearchOption.Smart })
        assertEquals("docs", without.best)
        val with = searchOptions("GTS-04-R", tags("cat"), emptyList(), null, null, noFileSearch = false,
            smart = SmartSearchSupport.searchable(false, SmartSearchModelState.Missing, true))
        assertEquals("smart", with.options.first().key)
        assertEquals("smart", with.best)
        assertTrue(searchKeyChoice("GTS-04-R", with, null, false) is SearchOption.Smart)
    }

    // ---- Images: the text sent, and refused ----

    private fun photo(path: String, at: String): File {
        val t = java.time.Instant.parse(at)
        return File.newBuilder().setPath(path).setHash(hx(path)).setMime("image/jpeg").setCreated(Timestamp.newBuilder().setSeconds(t.epochSecond)).build()
    }
    private val a = photo("a", "2026-10-03T10:00:00Z")
    private val b = photo("b", "2024-01-02T10:00:00Z")

    /** The device: by date without a text; with one, b first (ranked); [textError] refuses a text. */
    private inner class Device {
        val searches = CopyOnWriteArrayList<SearchPhotos>()
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
                        sp.text.isNotEmpty() -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addFiles(b).addFiles(a)).build()
                        else -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addFiles(a).addFiles(b)).build()
                    }
                }
                else -> RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
            }
        }
    }

    private var turnedOff = 0
    private var textOff = 0

    /** Images as the app makes it: the text offered as SmartSearchSupport says. */
    private fun started(dev: Device): PhotoGalleryViewModel {
        val vm = PhotoGalleryViewModel(
            "test", send = dev.send, pause = {}, online = { true }, problemOf = { _, _ -> LoadProblem.FAILED },
            thumbs = thumbs, fullPhotos = thumbs, scope = scope, signIns = { 1 },
            smartOn = { SmartSearchSupport.searchable }, smartSearchable = SmartSearchSupport.searchableFlow,
            smartOff = { turnedOff++ }, smartDownloading = {}, textRefused = { textOff++; SmartSearchSupport.textRefused() },
        )
        runBlocking(dispatcher) { vm.onAppearInitial() }
        await("the library") { paths(vm) == listOf("a", "b") && !vm.state.value.loading }
        return vm
    }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    private fun paths(vm: PhotoGalleryViewModel) = vm.state.value.items.map { it.path }

    @Test fun withSmartSearchOffTheTextIsSent() {
        SmartSearchSupport.deviceSaid(settings(smart = false, model = downloading, text = true))
        val dev = Device()
        val vm = started(dev)
        runBlocking(dispatcher) { vm.searchText(" GTS-04-R ") }
        await("the text's page") { dev.searches.any { it.text == "GTS-04-R" } && paths(vm) == listOf("b", "a") }
        assertEquals("GTS-04-R", vm.state.value.smartText)
        assertFalse(vm.state.value.dateOrdered)
    }

    @Test fun offOnADeviceBeforeItNoTextIsSent() {
        SmartSearchSupport.deviceSaid(settings(smart = false))
        val dev = Device()
        val vm = started(dev)
        runBlocking(dispatcher) { vm.searchText("GTS-04-R") }
        Thread.sleep(50)
        assertNull(vm.state.value.smartText)
        assertTrue(dev.searches.all { it.text.isEmpty() })
    }

    @Test fun aRefusalTakesTheTextAndTextInPhotosAway() {
        SmartSearchSupport.deviceSaid(settings(smart = false, text = true))
        val dev = Device()
        // Turned off on another app: the device answers as before Text in photos.
        dev.textError = "smart_search_off" to "Smart search is off"
        val vm = started(dev)
        runBlocking(dispatcher) { vm.searchText("GTS-04-R") }
        await("the alert") { vm.state.value.alert != null }
        assertEquals("Smart search is off", vm.state.value.alert!!.english())
        assertNull(vm.state.value.smartText)
        assertEquals(1, textOff)
        assertEquals(1, turnedOff)
        assertEquals(false, SmartSearchSupport.textInPhotos.value)
        await("by date again") { paths(vm) == listOf("a", "b") && !vm.state.value.loading && dev.searches.last().text.isEmpty() }
    }

    // ---- Settings ----

    @Test fun settingsShowsTheSwitchesOnlyOnADeviceThatHasThem() {
        val before = DeviceSettingsViewModel.State().withSettings(settings(smart = true))
        assertNull(before.textInPhotosEnabled)
        assertNull(before.petsEnabled)
        val on = DeviceSettingsViewModel.State().withSettings(settings(text = true, pets = true))
        assertEquals(true, on.textInPhotosEnabled)
        assertEquals(true, on.petsEnabled)
        val off = on.withSettings(settings(text = false, pets = false))
        assertEquals(false, off.textInPhotosEnabled)
        assertEquals(false, off.petsEnabled)
        assertFalse(off.confirmTextOff)
    }

    // ---- Pets: People shows with them ----

    @Test fun peopleShowsWithFaceRecognitionOrPets() {
        assertEquals(true, FaceRecognition.peopleShown(true, null))
        assertEquals(true, FaceRecognition.peopleShown(false, true))
        assertEquals(true, FaceRecognition.peopleShown(null, true))
        assertEquals(true, FaceRecognition.peopleShown(true, false))
        assertEquals(false, FaceRecognition.peopleShown(false, false))
        // A device without Pets: as before.
        assertEquals(false, FaceRecognition.peopleShown(false, null))
        assertNull(FaceRecognition.peopleShown(null, null))
        assertNull(FaceRecognition.peopleShown(null, false))
    }

    @Test fun peopleComesWithPetsAndGoesWithBoth() {
        FaceRecognition.deviceSaid(settings(pets = true).toBuilder().setFaceRecognitionEnabled(false).build())
        assertEquals(true, FaceRecognition.enabled.value)
        // Pets' switch turned off, face recognition's on: People as they say.
        FaceRecognition.setPets(false)
        assertEquals(false, FaceRecognition.enabled.value)
        FaceRecognition.set(true)
        assertEquals(true, FaceRecognition.enabled.value)
        // A device before Pets: face recognition alone, as before.
        FaceRecognition.deviceSaid(Settings.getDefaultInstance())
        assertEquals(false, FaceRecognition.enabled.value)
        FaceRecognition.reset()
        assertNull(FaceRecognition.enabled.value)
    }

    // ---- Info ----

    @Test fun infoShowsTheTextAsItCame() {
        assertNull(photoText(null))
        // None read (or not yet, or off), or a device before it.
        assertNull(photoText(FileExifInfo.getDefaultInstance()))
        assertNull(photoText(FileExifInfo.newBuilder().setText("  \n ").build()))
        val text = "GTS-04-R\nNO PARKING\n  Mon-Fri 8-20"
        assertEquals(text, photoText(FileExifInfo.newBuilder().setText(text).build()))
        assertEquals(2_500L, PHOTO_TEXT_COPIED_MS)
    }
}
