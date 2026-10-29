// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.proto.GetPhotoPlaces
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.PhotoPlace
import cloud.offthe.otc.proto.PhotoPlaces
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.ReqPhotoDateBuckets
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.TestThumbStore
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

// The map's model (PlacesModel.kt) against a pretend device, and Images
// narrowed to a place's area - iOS's PlacesModelTests, case for case.
class PlacesModelTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    @get:Rule val tmp = TemporaryFolder()
    private val thumbs by lazy { TestThumbStore(tmp.newFolder()) }

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!on { cond() }) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(5)
        }
    }

    /** The device, for the map: answers GetPhotoPlaces from a few places, and counts what it is asked. */
    private class Device {
        val asks = CopyOnWriteArrayList<GetPhotoPlaces>()
        @Volatile var fail = false
        /** Places turned off (on another app): every ask is told so. */
        @Volatile var off = false
        val places = listOf(Triple(40.4168, -3.7038, 3), Triple(40.4200, -3.7000, 2), Triple(41.3874, 2.1686, 4))

        fun answer(r: GetPhotoPlaces): RespEnvelope {
            asks += r
            if (fail) return RespEnvelope.newBuilder().setError(true).setErrorMessage("busy").build()
            if (off) return RespEnvelope.newBuilder().setError(true).setErrorCode("places_off").setErrorMessage("Places is off").build()
            val a = PhotoPlaces.newBuilder().setZoom(r.zoom).setLocatedTotal(9).setLocatedArea(area(40.4, -3.71, 41.39, 2.17))
            val z = r.zoom
            val asked = r.tilesList.map { PlaceTileKey(z, it.x, it.y) }.toSet()
            places.forEachIndexed { i, (lat, lon, n) ->
                val t = PlaceTiles.tile(lat, lon, z)
                if (t !in asked) return@forEachIndexed
                a.addPlaces(
                    PhotoPlace.newBuilder().setLatitude(lat).setLongitude(lon).setCount(n).setArea(area(lat, lon, lat, lon))
                        .setZoom(z).setX(t.x).setY(t.y).setCover(file("h$i", i.toDouble())),
                )
            }
            return RespEnvelope.newBuilder().setRespPhotoPlaces(a).build()
        }
    }

    private var noMaps = 0

    private fun model(d: Device) = PlacesModel(
        scope,
        request = { d.answer(it) },
        thumbs = thumbs,
        sendThumbs = { throw IOException("no thumbnails here") },
        pause = { kotlinx.coroutines.delay(50) },
        noMap = { noMaps++ },
    ).apply {
        settleMs = 10
        prefetchMs = 50
    }

    /** Madrid and around, on a phone 390 points wide, at about zoom 10. */
    private val madrid: WorldRect = run {
        val c = PlaceTiles.point(40.418, -3.702)
        val w = 1.0 / (1 shl 10) * (390.0 / 256)
        WorldRect(c.x - w / 2, c.y - w, w, 2 * w)
    }

    @Test fun theExtentFirstThenTheScreensTilesThenTheNextZooms() {
        val d = Device()
        val m = on { model(d) }
        on { m.appeared(PlacesFilter()) }
        await("the extent") { m.state.value.extent != null }
        assertEquals(0, d.asks.first().zoom)
        assertEquals(1, d.asks.first().tilesCount)
        assertEquals(9, m.state.value.extent?.total)
        on { m.cameraChanged(madrid, 390.0) }
        assertEquals(10, on { m.zoom })
        await("the pins") { m.state.value.pins.size == 1 }
        // Madrid's two places are one pin at this zoom; Barcelona is off the screen.
        assertEquals(5, m.state.value.pins.first().count)
        val z10 = d.asks.filter { it.zoom == 10 }
        assertEquals(1, z10.size)
        assertTrue(z10[0].tilesCount <= PlaceTiles.BATCH_SIZE)
        assertTrue(z10[0].includeVideos)
        // Once it rests, the next zoom's, for a zoom in.
        await("the next zoom") { d.asks.any { it.zoom == 11 } }
        // Back to where it was: nothing asked again.
        val count = d.asks.size
        on { m.cameraChanged(madrid, 390.0) }
        Thread.sleep(150)
        assertEquals(count, d.asks.size)
        assertEquals(5, m.state.value.pins.first().count)
        // And it is where the map comes back to, for this search.
        assertEquals(madrid, m.savedRect)
        assertEquals(PlacesFilter(), m.savedFor)
    }

    /** Places turned off on another app: the map is told "places_off" and goes, with no banner to try again. */
    @Test fun aMapToldPlacesOffGoes() {
        val d = Device().apply { off = true }
        val m = on { model(d) }
        on { m.appeared(PlacesFilter()) }
        await("no map") { noMaps == 1 }
        assertFalse(m.state.value.problem)
    }

    @Test fun offTheScreenNothingIsAsked() {
        val d = Device()
        val m = on { model(d) }
        // A delete in the grid before the map was ever shown.
        on {
            m.libraryChanged()
            m.setFilter(PlacesFilter(tags = listOf("dog")))
        }
        Thread.sleep(100)
        assertTrue(d.asks.isEmpty())
        on { m.appeared(PlacesFilter()) }
        await("the extent") { m.state.value.extent != null }
        on { m.cameraChanged(madrid, 390.0) }
        await("the pins") { m.state.value.pins.isNotEmpty() }
        on { m.disappeared() }
        val n = d.asks.size
        on { m.libraryChanged() }
        Thread.sleep(150)
        assertEquals(n, d.asks.size)
    }

    @Test fun aFailureSaysSoAndIsAskedAgain() {
        val d = Device().apply { fail = true }
        val m = on { model(d) }
        on { m.appeared(PlacesFilter()) }
        await("the problem") { m.state.value.problem }
        d.fail = false
        await("the extent") { m.state.value.extent != null }
        assertFalse(m.state.value.problem)
        assertTrue(d.asks.size >= 2)
    }

    @Test fun anotherSearchDrawsOnlyItsOwnPins() {
        val d = Device()
        val m = on { model(d) }
        on { m.appeared(PlacesFilter()) }
        await("the extent") { m.state.value.extent != null }
        on { m.cameraChanged(madrid, 390.0) }
        await("the pins") { m.state.value.pins.isNotEmpty() }
        on { m.setFilter(PlacesFilter(people = listOf("p1"))) }
        assertTrue(m.state.value.pins.isEmpty())
        assertNull(m.state.value.extent)
        await("its own pins") { m.state.value.pins.isNotEmpty() }
        assertEquals(listOf("p1"), d.asks.last().personIdsList)
    }

    @Test fun theWorldOpensWhereMostAre() {
        val d = Device()
        val m = on { model(d) }
        on { m.appeared(PlacesFilter()) }
        await("the extent") { m.state.value.extent != null }
        // Zoom 0's biggest place (this device answers each one apart): Barcelona's 4.
        assertEquals(2.1686, on { m.worldMiddle() }!!, 1e-9)
    }

    /** Images narrowed to a place's area: every page and the month counts carry it; x takes it away. */
    @Test fun theMapsAreaGoesWithImagesRequests() {
        val searches = CopyOnWriteArrayList<SearchPhotos>()
        val buckets = CopyOnWriteArrayList<ReqPhotoDateBuckets>()
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> {
                    searches += req.reqSearchPhotos
                    RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                }
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> {
                    buckets += req.reqPhotoDateBuckets
                    RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
                }
                else -> throw IOException("not answered")
            }
        }
        val vm = PhotoGalleryViewModel(
            "test", send = send, pause = {}, online = { true }, problemOf = { _, _ -> LoadProblem.FAILED },
            thumbs = thumbs, fullPhotos = thumbs, scope = scope, signIns = { 1 },
        )
        val a = area(40.41, -3.71, 40.42, -3.70)
        on { vm.showArea(a) }
        await("a search and the counts") { searches.isNotEmpty() && buckets.isNotEmpty() }
        assertEquals(a, searches.last().area)
        assertEquals(a, buckets.last().area)
        assertEquals(EmptyKind.NO_MATCH, emptyKind(vm.state.value))
        // The map shows the search without the area.
        assertEquals(PlacesFilter(), vm.state.value.placesFilter)
        val n = searches.size
        on { vm.leaveArea() }
        await("the search again") { searches.size > n }
        assertFalse(searches.last().hasArea())
        // Images from the menu: no area either.
        on { vm.showArea(a) }
        await("the area again") { searches.last().hasArea() }
        on { vm.showAll() }
        assertNull(vm.state.value.activeArea)
    }
}
