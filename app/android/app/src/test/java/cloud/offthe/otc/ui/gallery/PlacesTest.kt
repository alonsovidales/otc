// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.data.PlacesSupport
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosPlacesAreaChip
import cloud.offthe.otc.i18n.appPhotosPlacesNone
import cloud.offthe.otc.i18n.appShellSectionPlaces
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.PhotoPlace
import cloud.offthe.otc.proto.PhotoPlaces
import cloud.offthe.otc.proto.PlaceArea
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.Settings
import com.google.protobuf.Timestamp
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId
import kotlin.math.PI
import kotlin.math.abs
import kotlin.math.cos
import kotlin.math.floor
import kotlin.math.hypot
import kotlin.math.ln
import kotlin.math.max
import kotlin.math.min
import kotlin.math.pow
import kotlin.math.tan

// Places, the map in Images (Places.kt): the tiles asked for, the answers
// kept, the pins drawn and what a tap does - iOS's PlacesTests.swift, case
// for case. The web's places-test.mjs checks the same rules.

internal fun file(hash: String, day: Double, video: Boolean = false): File = File.newBuilder()
    .setHash(hash).setPath("/p/$hash.jpg").setMime(if (video) "video/mp4" else "image/jpeg")
    .setCreated(Timestamp.newBuilder().setSeconds((1_780_000_000 + day * 86400).toLong()))
    .build()

internal fun area(s: Double, w: Double, n: Double, e: Double): PlaceArea =
    PlaceArea.newBuilder().setSouth(s).setWest(w).setNorth(n).setEast(e).build()

internal fun place(lat: Double, lon: Double, count: Int, cover: File?, zoom: Int = 10): PhotoPlace {
    val t = PlaceTiles.tile(lat, lon, zoom)
    val b = PhotoPlace.newBuilder().setLatitude(lat).setLongitude(lon).setCount(count)
        .setArea(area(lat - 0.001, lon - 0.001, lat + 0.001, lon + 0.001))
        .setZoom(zoom).setX(t.x).setY(t.y)
    if (cover != null) b.cover = cover
    return b.build()
}

class PlaceTilesTest {
    /** The device's formula (PlaceTile in messages.proto). */
    private fun slippy(lat: Double, lon: Double, z: Int): Pair<Int, Int> {
        val n = (1 shl z).toDouble()
        val la = min(85.0511287798, max(-85.0511287798, lat)) * PI / 180
        val x = min(n - 1, floor((lon + 180) / 360 * n))
        val y = min(n - 1, max(0.0, floor((1 - ln(tan(la) + 1 / cos(la)) / PI) / 2 * n)))
        return x.toInt() to y.toInt()
    }

    @Test fun aPointsTileIsTheDevicesAtEveryZoom() {
        val points = listOf(40.4168 to -3.7038, -33.8688 to 151.2093, 64.1466 to -21.9426, 0.0 to 0.0, -0.0001 to -0.0001, 51.5007 to -0.1246, 35.6762 to 139.6503)
        for ((lat, lon) in points) for (z in 0..20) {
            val t = PlaceTiles.tile(lat, lon, z)
            val (x, y) = slippy(lat, lon, z)
            assertEquals(z, t.zoom)
            assertEquals("$lat,$lon at $z", x to y, t.x to t.y)
        }
    }

    @Test fun theEdgesOfTheMapAreItsLastTiles() {
        // Longitude 180 is the last column, as the device counts it; the
        // poles are the first and last rows.
        assertEquals(7, PlaceTiles.tile(0.0, 180.0, 3).x)
        assertEquals(0, PlaceTiles.tile(0.0, -180.0, 3).x)
        assertEquals(0, PlaceTiles.tile(90.0, 0.0, 4).y)
        assertEquals(15, PlaceTiles.tile(-90.0, 0.0, 4).y)
        assertEquals(PlaceTileKey(0, 0, 0), PlaceTiles.tile(89.0, 10.0, 0))
    }

    @Test fun theZoomComesFromTheSpan() {
        // The whole world on a 256-point map is zoom 0; twice as wide, 1.
        assertEquals(0, PlaceTiles.zoom(256.0, 360.0))
        assertEquals(1, PlaceTiles.zoom(512.0, 360.0))
        // A phone across the world: still 0 (rounded down).
        assertEquals(0, PlaceTiles.zoom(390.0, 360.0))
        assertEquals(ln(390.0 / 256) / ln(2.0), PlaceTiles.exactZoom(390.0, 360.0), 1e-9)
        // A street, and closer than the device's last zoom: 20.
        assertEquals(20, PlaceTiles.zoom(390.0, 0.0005))
        assertEquals(20, PlaceTiles.zoom(390.0, 0.0000001))
        // A town across a phone.
        assertEquals(10, PlaceTiles.zoom(390.0, 0.27))
        // Nothing to measure yet.
        assertEquals(0, PlaceTiles.zoom(0.0, 10.0))
        assertEquals(0, PlaceTiles.zoom(390.0, 0.0))
        // The same from a rect.
        assertEquals(180.0, PlaceTiles.lonSpan(WorldRect(0.0, 0.0, 0.5, 0.5)), 1e-12)
    }

    @Test fun theScreensTilesWithOneAroundThemNearestFirst() {
        val side = 1.0 / 4
        // Exactly tile (1, 1) at zoom 2.
        val r = WorldRect(side, side, side, side)
        val tiles = PlaceTiles.tiles(r, 2, margin = 1)
        assertEquals(9, tiles.size)
        assertEquals(PlaceTileKey(2, 1, 1), tiles.first())
        assertEquals(setOf(0, 1, 2), tiles.map { it.x }.toSet())
        assertEquals(setOf(0, 1, 2), tiles.map { it.y }.toSet())
        assertEquals(listOf(PlaceTileKey(2, 1, 1)), PlaceTiles.tiles(r, 2, margin = 0))
    }

    @Test fun theWholeWorldIsEachTileOnce() {
        assertEquals(listOf(PlaceTileKey(0, 0, 0)), PlaceTiles.tiles(WorldRect.WORLD, 0, margin = 1))
        val z2 = PlaceTiles.tiles(WorldRect.WORLD, 2, margin = 1)
        assertEquals(16, z2.size)
        assertEquals(16, z2.toSet().size)
    }

    @Test fun acrossTheAntimeridianTheTilesWrap() {
        val n = 8
        val side = 1.0 / n
        // From the last column on past the world's east edge.
        val r = WorldRect(side * 7.5, side * 3, side, side * 0.5)
        assertEquals(setOf(7, 0), PlaceTiles.tiles(r, 3, margin = 0).map { it.x }.toSet())
        // Rows off the map are left out.
        val top = WorldRect(side * 2, 0.0, side, side * 0.5)
        assertTrue(PlaceTiles.tiles(top, 3, margin = 1).all { it.y in 0 until n })
    }

    @Test fun atMost64ARequest() {
        val tiles = (0 until 130).map { PlaceTileKey(8, it, 0) }
        val b = PlaceTiles.batches(tiles)
        assertEquals(listOf(64, 64, 2), b.map { it.size })
        assertEquals(tiles, b.flatten())
        assertTrue(PlaceTiles.batches(emptyList()).isEmpty())
    }

    @Test fun anAreaAcrossTheAntimeridianIsOneRect() {
        val r = PlaceTiles.rect(area(-18.0, 177.0, -16.0, -179.0))
        // 4 degrees wide, east of the world's edge.
        assertEquals(4.0, PlaceTiles.lonSpan(r), 1e-6)
        assertTrue(r.maxX > 1)
        // A single spot is framed about a town across.
        val f = PlaceTiles.framing(area(40.4, -3.7, 40.4, -3.7), minWidth = PlaceTiles.townWidth)
        assertEquals(PlaceTiles.townWidth, f.width, 1e-12)
        assertTrue(f.contains(PlaceTiles.point(40.4, -3.7)))
    }

    /** Photos all over the world open on the world, its middle where most of them are - not on the smallest area holding them, which runs across the Pacific. */
    @Test fun photosAllOverTheWorldOpenOnTheWorldWhereMostAre() {
        // Tokyo eastwards to Nairobi, across the antimeridian: 258°.
        val wide = area(-34.6, 139.7, 64.1, 36.8)
        val world = listOf(place(40.4, -3.7, 200, null, zoom = 0), place(35.7, 139.7, 15, null, zoom = 0))
        val middle = PlaceTiles.middle(world)
        assertEquals(-3.7, middle!!, 0.0)
        val r = PlaceTiles.opening(wide, middle, PlaceTiles.townWidth)
        assertEquals(1.0, r.width, 1e-12)
        val (lat, lon) = PlaceTiles.coordinate(WorldPoint(r.midX % 1.0, r.midY))
        assertEquals(-3.7, lon, 1e-6)
        assertEquals(25.0, lat, 1e-6)
        assertTrue(r.minX >= 0 && r.minX < 1)
        // Before zoom 0's answer: 10° E.
        val early = PlaceTiles.opening(wide, null, PlaceTiles.townWidth)
        assertEquals(10.0, PlaceTiles.coordinate(WorldPoint(early.midX % 1.0, early.midY)).second, 1e-6)
        // A continent is framed as before.
        val europe = area(36.0, -9.5, 64.2, 37.6)
        assertEquals(PlaceTiles.framing(europe, PlaceTiles.townWidth), PlaceTiles.opening(europe, middle, PlaceTiles.townWidth))
        assertNull(PlaceTiles.middle(emptyList()))
    }

    @Test fun pointsAndPositionsAreEachOthers() {
        for ((lat, lon) in listOf(40.4168 to -3.7038, -33.8688 to 151.2093, 0.0 to 0.0, 80.0 to -170.0)) {
            val (la, lo) = PlaceTiles.coordinate(PlaceTiles.point(lat, lon))
            assertEquals(lat, la, 1e-9)
            assertEquals(lon, lo, 1e-9)
        }
    }
}

class PlacePinTest {
    @Test fun placesTooCloseOnTheScreenAreOnePin() {
        val a = PlacePin.of(place(40.4168, -3.7038, 3, file("a", 1.0)))
        val b = PlacePin.of(place(40.4170, -3.7036, 5, file("b", 9.0)))
        val far = PlacePin.of(place(41.3874, 2.1686, 2, file("c", 3.0)))
        val pins = PinMerge.merge(listOf(a, b, far), zoom = 10.0)
        assertEquals(2, pins.size)
        val merged = pins.first { it.count == 8 }
        // The newest photo's thumbnail, and its id: the pin keeps its place
        // across zooms while its cover stays the newest.
        assertEquals("b", merged.cover?.hash)
        assertEquals(b.id, merged.id)
        // At one of them, never between: the bigger one, nearer their
        // weighted mean (the mean can be where no photo was taken).
        assertEquals(b.latitude to b.longitude, merged.latitude to merged.longitude)
        // The area holding both.
        assertEquals(a.area.south, merged.area.south, 0.0)
        assertEquals(b.area.north, merged.area.north, 0.0)
        assertEquals(a.area.west, merged.area.west, 0.0)
        assertEquals(b.area.east, merged.area.east, 0.0)
        assertTrue(far in pins)
    }

    @Test fun noTwoPinsAreLeftCloserThanAPin() {
        // A line of places a bit more than half a pin apart: the biggest
        // takes in its neighbours, moves, and comes near the next ones.
        val z = 12.0
        val step = PinMerge.GAP * 0.6 / (256 * 2.0.pow(z))
        val start = PlaceTiles.point(40.4, -3.7)
        val line = (0 until 12).map { i ->
            val (lat, lon) = PlaceTiles.coordinate(WorldPoint(start.x + i * step, start.y))
            PlacePin.of(place(lat, lon, if (i == 5) 9 else 1 + i % 3, file("h$i", i.toDouble())))
        }
        val pins = PinMerge.merge(line, z)
        assertEquals(line.sumOf { it.count }, pins.sumOf { it.count })
        val perPoint = 256 * 2.0.pow(z)
        for (i in pins.indices) for (j in i + 1 until pins.size) {
            val d = hypot(pins[i].point.x - pins[j].point.x, pins[i].point.y - pins[j].point.y) * perPoint
            assertTrue("$d", d >= PinMerge.GAP)
        }
    }

    @Test fun zoomedInTheySplit() {
        val a = PlacePin.of(place(40.4168, -3.7038, 3, file("a", 1.0)))
        val b = PlacePin.of(place(40.4268, -3.7038, 5, file("b", 9.0)))
        assertEquals(1, PinMerge.merge(listOf(a, b), 8.0).size)
        assertEquals(2, PinMerge.merge(listOf(a, b), 15.0).size)
    }

    @Test fun whatTalkBackReads() {
        val utc = ZoneId.of("UTC")
        val one = PlacePin.of(place(1.0, 1.0, 1, file("a", 0.0)))
        assertEquals(tileLabel(false, 1_780_000_000_000L, utc).english(), one.label(utc).english())
        val video = PlacePin.of(place(1.0, 1.0, 1, file("v", 0.0, video = true)))
        assertTrue(video.label(utc).english().startsWith("Video"))
        assertEquals("12 items here", PlacePin.of(place(1.0, 1.0, 12, file("b", 0.0))).label(utc).english())
        assertEquals("No photos with a location yet", S.appPhotosPlacesNone().english())
        assertEquals("Map area", S.appPhotosPlacesAreaChip().english())
        assertEquals("Places", S.appShellSectionPlaces().english())
    }

    @Test fun aTapOpensOneZoomsIntoManyAndShowsTheClosestInImages() {
        val view = WorldRect(0.0, 0.0, 1.0 / 64, 1.0 / 64)
        // One photo: the viewer.
        val one = PlacePin.of(place(40.4, -3.7, 1, file("a", 0.0)))
        val open = PlaceTap.of(one, 6.0, view)
        assertTrue(open is PlaceTap.Open)
        assertEquals("a", (open as PlaceTap.Open).file.hash)
        // A group: closer, at least twice.
        val many = PlacePin.of(place(40.4, -3.7, 9, file("b", 0.0)))
        val zoom = PlaceTap.of(many, 6.0, view)
        assertTrue(zoom is PlaceTap.Zoom)
        val r = (zoom as PlaceTap.Zoom).rect
        assertTrue(r.width <= view.width / 2 + 1e-12)
        assertTrue(r.contains(many.point))
        // Zoomed in far already, or photos a few metres apart: Images.
        val shown = PlaceTap.of(many, 16.2, view)
        assertEquals(PlaceTap.ShowInImages(many.area), shown)
        val same = PlacePin("x", 40.4, -3.7, 4, file("c", 0.0), area(40.4, -3.7, 40.40001, -3.70001))
        assertTrue(PlaceTap.of(same, 8.0, view) is PlaceTap.ShowInImages)
    }
}

class PlacesCacheTest {
    private fun answer(places: List<PhotoPlace>, zoom: Int, total: Int = 7, reading: Boolean = false): PhotoPlaces =
        PhotoPlaces.newBuilder().addAllPlaces(places).setZoom(zoom).setLocatedTotal(total).setLocatedArea(area(40.0, -4.0, 42.0, 3.0)).setReading(reading).build()

    @Test fun everyTileAskedForIsKeptTheEmptyOnesToo() {
        val c = PlacesCache()
        val f = PlacesFilter()
        val p1 = place(40.4168, -3.7038, 3, file("a", 1.0))
        val p2 = place(41.3874, 2.1686, 2, file("b", 1.0))
        val t1 = PlaceTileKey(10, p1.x, p1.y)
        val t2 = PlaceTileKey(10, p2.x, p2.y)
        val empty = PlaceTileKey(10, 3, 3)
        val unasked = PlaceTileKey(10, 4, 4)
        val now = 1_000_000L
        assertEquals(listOf(t1, t2, empty), c.missing(listOf(t1, t2, empty), f, now))
        c.put(answer(listOf(p1, p2), 10), listOf(t1, t2, empty), f, now)
        assertEquals(1, c.places(t1, f, now)?.size)
        assertEquals("b", c.places(t2, f, now)?.first()?.cover?.hash)
        assertEquals(true, c.places(empty, f, now)?.isEmpty())
        assertEquals(listOf(unasked), c.missing(listOf(t1, unasked, empty, t2), f, now))
        assertEquals(7, c.extent(f)?.total)
        // Another search's are its own.
        val person = PlacesFilter(people = listOf("p1"))
        assertNull(c.places(t1, person, now))
        assertNull(c.extent(person))
    }

    @Test fun keptFiveMinutesOrUntilTheLibraryChanges() {
        val c = PlacesCache()
        val f = PlacesFilter(tags = listOf("beach"))
        val t = PlaceTileKey(0, 0, 0)
        val then = 1_000_000L
        c.put(answer(emptyList(), 0, total = 0), listOf(t), f, then)
        assertNotNull(c.places(t, f, then + 299_000))
        assertNull(c.places(t, f, then + 301_000))
        c.clearTiles()
        assertNull(c.places(t, f, then))
        // The extent stays, so the map opens framed as before.
        assertEquals(PlacesExtent(answer(emptyList(), 0, total = 0)), c.extent(f))
        assertNull(c.extent(f)?.area)
        c.clear()
        assertNull(c.extent(f))
    }

    @Test fun theRequestCarriesTheSearch() {
        val f = PlacesFilter(tags = listOf("dog"), people = listOf("p1", "p2"), group = "g1")
        val r = f.request(7, listOf(PlaceTileKey(7, 3, 4)))
        assertEquals(7, r.zoom)
        assertEquals(listOf(listOf(3, 4)), r.tilesList.map { listOf(it.x, it.y) })
        assertEquals(listOf("dog"), r.tagsList)
        assertEquals(listOf("p1", "p2"), r.personIdsList)
        assertEquals("g1", r.groupId)
        // Videos as the grid shows them.
        assertTrue(r.includeVideos)
        assertFalse(f.isEmpty)
        assertTrue(PlacesFilter().isEmpty)
    }

    /** Places shows only while the device's Settings say it is on; a device before Places (no places_enabled) has none. */
    @Test fun whatTheQuestionsAnswerSays() {
        fun settings(on: Boolean?): RespEnvelope {
            val s = Settings.newBuilder()
            if (on != null) s.placesEnabled = on
            return RespEnvelope.newBuilder().setRespSettings(s).build()
        }
        assertEquals(true, PlacesSupport.verdict(settings(true)))
        assertEquals(false, PlacesSupport.verdict(settings(false)))
        assertEquals(false, PlacesSupport.verdict(settings(null)))
        assertNull(PlacesSupport.verdict(RespEnvelope.newBuilder().setError(true).setErrorMessage("busy").build()))
        assertNull(PlacesSupport.verdict(null as RespEnvelope?))
    }

    /** While the device reads positions, its answers are kept 15 s, not 5 minutes, and the extent says so. */
    @Test fun readingAnswersAgeSooner() {
        val c = PlacesCache()
        val f = PlacesFilter()
        val t = PlaceTileKey(0, 0, 0)
        val then = 1_000_000L
        c.put(answer(emptyList(), 0, total = 0, reading = true), listOf(t), f, then)
        assertEquals(true, c.extent(f)?.reading)
        assertNotNull(c.places(t, f, then + PlacesCache.READING_TTL_MS - 1000))
        assertNull(c.places(t, f, then + PlacesCache.READING_TTL_MS + 1000))
        c.put(answer(emptyList(), 0, total = 0, reading = false), listOf(t), f, then)
        assertEquals(false, c.extent(f)?.reading)
        assertNotNull(c.places(t, f, then + 60_000))
    }
}

/** The map's camera: where a pin is drawn, and which pin a tap is on. */
class MapCameraTest {
    @Test fun aPinIsWhereItsPointIsOnTheScreen() {
        // Madrid in the middle, zoom 10, a 400 x 800 dp map at 2.5 pixels a dp.
        val m = PlaceTiles.point(40.4168, -3.7038)
        val c = MapCamera(m.x, m.y, 10.0, 1000f, 2000f, 2.5f)
        val mid = c.screen(m)
        assertEquals(500f, mid.x, 1e-3f)
        assertEquals(1000f, mid.y, 1e-3f)
        assertEquals(400.0, c.widthDp, 1e-9)
        // The screen's rect: 400 dp at 512 * 2^10 dp a world.
        assertEquals(400.0 / (512 * 1024), c.rect.width, 1e-12)
        // The device's zoom for it is MapLibre's plus one (256-point tiles).
        assertEquals(11.0, PlaceTiles.exactZoom(c.widthDp, PlaceTiles.lonSpan(c.rect)), 1e-9)
        // Across the antimeridian a pin is drawn on the copy beside the middle.
        val east = MapCamera(PlaceTiles.point(0.0, 179.9).x, 0.5, 8.0, 1000f, 1000f, 1f)
        val west = east.screen(PlaceTiles.point(0.0, -179.9))
        assertTrue(abs(west.x - 500f) < 200f)
    }

    @Test fun aTapIsOnThePinUnderIt() {
        val m = PlaceTiles.point(40.4168, -3.7038)
        val c = MapCamera(m.x, m.y, 10.0, 1000f, 2000f, 2f)
        val madrid = PlacePin.of(place(40.4168, -3.7038, 3, file("a", 1.0)))
        val single = PlacePin.of(place(40.4168, -3.7038, 1, file("s", 1.0)))
        val s = c.screen(madrid.point)
        assertEquals(madrid, pinAt(listOf(madrid), c, ScreenPoint(s.x + 40f, s.y - 40f)))
        // Its count sticks out at the corner; a pin of one has none.
        assertEquals(madrid, pinAt(listOf(madrid), c, ScreenPoint(s.x + 70f, s.y - 70f)))
        assertNull(pinAt(listOf(single), c, ScreenPoint(s.x + 70f, s.y - 70f)))
        assertNull(pinAt(listOf(madrid), c, ScreenPoint(s.x - 100f, s.y)))
    }

    @Test fun aRectIsShownWhole() {
        val r = PlaceTiles.framing(area(40.3, -3.9, 40.6, -3.5), PlaceTiles.townWidth)
        val pos = cameraFor(r, 400.0, 800.0, minMapZoom(400.0))
        val at = PlaceTiles.point(pos.latitude, pos.longitude)
        val c = MapCamera(at.x, at.y, pos.zoom, 400f, 800f, 1f)
        assertTrue(c.rect.width >= r.width - 1e-12)
        assertTrue(c.rect.height >= r.height - 1e-12)
        assertEquals(r.midX, c.cx, 1e-9)
        // The world: the least zoom, never more than one world across.
        assertEquals(0.0, minMapZoom(400.0), 0.0)
        assertEquals(1.0, minMapZoom(1024.0), 1e-12)
        assertEquals(0.0, cameraFor(WorldRect.WORLD, 400.0, 800.0, minMapZoom(400.0)).zoom, 0.0)
    }
}
