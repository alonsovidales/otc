// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appPhotosPlacesPin
import cloud.offthe.otc.proto.GetPhotoPlaces
import cloud.offthe.otc.proto.PhotoPlace
import cloud.offthe.otc.proto.PhotoPlaces
import cloud.offthe.otc.proto.PlaceArea
import cloud.offthe.otc.proto.PlaceTile
import java.time.ZoneId
import kotlin.math.PI
import kotlin.math.asin
import kotlin.math.atan
import kotlin.math.cos
import kotlin.math.floor
import kotlin.math.ln
import kotlin.math.max
import kotlin.math.min
import kotlin.math.pow
import kotlin.math.sin
import kotlin.math.sinh
import kotlin.math.sqrt
import kotlin.math.tan
import cloud.offthe.otc.proto.File as PbFile

// Places: the map in Images - its arithmetic, the answers kept, the pins and
// what a tap does (PlacesModel.kt asks the device, PlacesMap.kt draws it).
// The photos and videos the device has a position for (GetPhotoPlaces,
// release 148), grouped as the device groups them: each tile of the map at
// a zoom is 4 x 4 cells, and each cell holding photos is one place.
// Zooming in splits the groups. A port of iOS's Places.swift, which works
// on MapKit's map points: here the same square Web Mercator world is 1
// across (WorldPoint). The web (placesMath.ts) follows the same rules:
// change them together.

/** A map tile at a zoom, numbered as GetPhotoPlaces takes them (slippy-map tiles, x within 0 to 2^zoom - 1). */
data class PlaceTileKey(val zoom: Int, val x: Int, val y: Int)

/** A point on the square Web Mercator world, 0 to 1 across from the antimeridian and from the top (x past 1: past the world's east edge). */
data class WorldPoint(val x: Double, val y: Double)

/** A rectangle of the world, in WorldPoint units (x past 1: across the antimeridian). */
data class WorldRect(val x: Double, val y: Double, val width: Double, val height: Double) {
    val minX get() = x
    val minY get() = y
    val maxX get() = x + width
    val maxY get() = y + height
    val midX get() = x + width / 2
    val midY get() = y + height / 2

    fun contains(p: WorldPoint) = p.x >= minX && p.x <= maxX && p.y >= minY && p.y <= maxY

    companion object {
        val WORLD = WorldRect(0.0, 0.0, 1.0, 1.0)
    }
}

/** The tile arithmetic, as the device counts its tiles (PlaceTile in messages.proto). */
object PlaceTiles {
    const val MAX_ZOOM = 20
    /** The points (dp) a tile is wide at its zoom. */
    const val TILE_SIDE = 256.0
    /** The most tiles one GetPhotoPlaces takes. */
    const val BATCH_SIZE = 64
    /** Square Web Mercator's edge. */
    const val MAX_LAT = 85.0511287798

    /** Where a position is on the world (latitude clamped to the map's edge). */
    fun point(latitude: Double, longitude: Double): WorldPoint {
        val la = latitude.coerceIn(-MAX_LAT, MAX_LAT) * PI / 180
        return WorldPoint((longitude + 180) / 360, (1 - asinh(tan(la)) / PI) / 2)
    }

    /** The position at a point: (latitude, longitude), the longitude as the x is (past 180 past the world's east edge). */
    fun coordinate(p: WorldPoint): Pair<Double, Double> =
        atan(sinh(PI * (1 - 2 * p.y))) * 180 / PI to p.x * 360 - 180

    /**
     * The zoom a map [viewWidth] points wide shows across [lonSpan]
     * degrees, as the device's tiles count it - log2(viewWidth · 360 /
     * (256 · lonSpan)), not rounded.
     */
    fun exactZoom(viewWidth: Double, lonSpan: Double): Double {
        if (viewWidth <= 0 || lonSpan <= 0) return 0.0
        return ln(viewWidth * 360 / (TILE_SIDE * lonSpan)) / ln(2.0)
    }

    /** The tiles' zoom for that span: the zoom shown, rounded down (a cell is then 64 to 128 points on the screen, room for a pin), 0 to 20. */
    fun zoom(viewWidth: Double, lonSpan: Double): Int {
        val z = exactZoom(viewWidth, lonSpan)
        if (!z.isFinite()) return 0
        return min(MAX_ZOOM, max(0, floor(z + 1e-9).toInt()))
    }

    /** The span a visible rect covers, in degrees of longitude. */
    fun lonSpan(rect: WorldRect) = rect.width * 360

    /** The tile a position is in (longitude 180 in the last column, the poles in the first and last rows). */
    fun tile(latitude: Double, longitude: Double, zoom: Int): PlaceTileKey {
        val n = 1 shl zoom
        val p = point(latitude, longitude)
        val x = min(n - 1, max(0, floor(p.x * n).toInt()))
        val y = min(n - 1, max(0, floor(p.y * n).toInt()))
        return PlaceTileKey(zoom, x, y)
    }

    /**
     * The tiles a visible rect covers at [zoom], with [margin] tiles more
     * on every side: x taken modulo 2^zoom (a map across the antimeridian),
     * rows off the map left out, each tile once, the ones nearest the
     * middle first.
     */
    fun tiles(covering: WorldRect, zoom: Int, margin: Int): List<PlaceTileKey> {
        val n = 1 shl zoom
        val side = 1.0 / n
        // Just inside the far edges: a rect ending on a tile's edge doesn't
        // reach into the next one.
        val inset = side * 1e-9
        var x0 = floor(covering.minX / side).toInt() - margin
        var x1 = floor((covering.maxX - inset) / side).toInt() + margin
        if (x1 - x0 + 1 >= n) {
            x0 = 0
            x1 = n - 1
        }
        val y0 = max(0, floor(covering.minY / side).toInt() - margin)
        val y1 = min(n - 1, floor((covering.maxY - inset) / side).toInt() + margin)
        if (y0 > y1 || x0 > x1) return emptyList()
        val midX = covering.midX / side - 0.5
        val midY = covering.midY / side - 0.5
        val seen = HashSet<PlaceTileKey>()
        val out = ArrayList<Pair<PlaceTileKey, Double>>()
        for (y in y0..y1) for (x in x0..x1) {
            val key = PlaceTileKey(zoom, ((x % n) + n) % n, y)
            if (!seen.add(key)) continue
            val dx = x - midX
            val dy = y - midY
            out += key to dx * dx + dy * dy
        }
        return out.sortedBy { it.second }.map { it.first }
    }

    /** Tiles in GetPhotoPlaces' batches, in their order. */
    fun batches(tiles: List<PlaceTileKey>, size: Int = BATCH_SIZE): List<List<PlaceTileKey>> = tiles.chunked(size)

    /** The rect an area covers (west > east: across the antimeridian, so past the world's east edge). */
    fun rect(area: PlaceArea): WorldRect {
        val nw = point(area.north, area.west)
        var se = point(area.south, area.east)
        if (area.east < area.west) se = se.copy(x = se.x + 1)
        return WorldRect(nw.x, nw.y, max(0.0, se.x - nw.x), max(0.0, se.y - nw.y))
    }

    /**
     * Where to look at an area: [pad] of its size more on each side, and
     * never less than [minWidth] across (a single spot about a town
     * across), the same shape.
     */
    fun framing(area: PlaceArea, minWidth: Double, pad: Double = 0.15): WorldRect {
        val r = rect(area)
        var w = r.width * (1 + 2 * pad)
        var h = r.height * (1 + 2 * pad)
        if (w < minWidth && h < minWidth) {
            w = minWidth
            h = minWidth
        }
        return WorldRect(r.midX - w / 2, max(0.0, r.midY - h / 2), w, min(h, 1.0))
    }

    /** About a town across: the width zoom 11 shows on a phone. */
    val townWidth: Double = 1.0 / (1 shl 11) * 1.5

    /**
     * Where a map opens on photos in [area]: framed on them, unless that
     * takes about the whole world - more than the web's quarter zoom above
     * its least, 2^-0.25 of the world's width. Then the world, its middle
     * at [middle]'s longitude (the biggest place of zoom 0, which is at a
     * photo; else 10° E) and 25° N: the smallest area holding photos from
     * Tokyo to Mexico City runs across the Pacific, and framing it cut
     * Europe in two at the edges.
     */
    fun opening(area: PlaceArea, middle: Double?, minWidth: Double): WorldRect {
        val framed = framing(area, minWidth)
        if (framed.width <= 2.0.pow(-0.25)) return framed
        val mid = point(25.0, middle ?: 10.0)
        val h = 0.5
        // x within the world: past its east edge is how a rect runs across
        // the antimeridian.
        var x = mid.x - 0.5
        if (x < 0) x += 1
        return WorldRect(x, max(0.0, min(1 - h, mid.y - h / 2)), 1.0, h)
    }

    /** The longitude of the biggest of zoom 0's places (null: none). */
    fun middle(world: List<PhotoPlace>): Double? =
        world.maxWithOrNull(compareBy<PhotoPlace> { it.count }.thenByDescending { it.longitude })?.longitude

    private fun asinh(v: Double) = ln(v + sqrt(v * v + 1))
}

/** Images' search as it is: the filters GetPhotoPlaces takes, as the grid sends them (videos always, as the grid shows them). */
data class PlacesFilter(val tags: List<String> = emptyList(), val people: List<String> = emptyList(), val group: String = "") {
    /** Nothing searched for: the whole library. */
    val isEmpty get() = tags.isEmpty() && people.isEmpty() && group.isEmpty()

    fun request(zoom: Int, tiles: List<PlaceTileKey>): GetPhotoPlaces = GetPhotoPlaces.newBuilder()
        .setZoom(zoom)
        .addAllTiles(tiles.map { PlaceTile.newBuilder().setX(it.x).setY(it.y).build() })
        .addAllTags(tags)
        .addAllPersonIds(people)
        .setGroupId(group)
        .setIncludeVideos(true)
        .build()
}

/** How many photos of a search have a position, the smallest area holding them (null when none has), and whether more are coming. */
data class PlacesExtent(val total: Int, val area: PlaceArea?, val reading: Boolean) {
    constructor(answer: PhotoPlaces) : this(
        answer.locatedTotal,
        if (answer.locatedTotal > 0 && answer.hasLocatedArea()) answer.locatedArea else null,
        answer.reading,
    )
}

/** One pin on the map: a place, or places drawn as one (PinMerge). */
data class PlacePin(
    /** Its newest photo's content, which no other pin of a zoom has - so a pin that keeps its cover keeps its place in the map across zooms. */
    val id: String,
    val latitude: Double,
    val longitude: Double,
    val count: Int,
    /** The newest photo (no content: its thumbnail is the pin's, by hash). */
    val cover: PbFile?,
    /** The smallest area holding its photos (SearchPhotos.area). */
    val area: PlaceArea,
) {
    val point: WorldPoint get() = PlaceTiles.point(latitude, longitude)

    /** What TalkBack reads: "Photo, 5 October 2026" for one, "12 items here" for a group. */
    fun label(zone: ZoneId): UiText {
        if (count == 1) {
            val c = cover
            return tileLabel(c?.mime?.startsWith("video/") == true, c?.createdMs(), zone)
        }
        return S.appPhotosPlacesPin(count)
    }

    companion object {
        fun of(p: PhotoPlace): PlacePin {
            val cover = if (p.hasCover() && p.cover.hash.isNotEmpty()) p.cover else null
            return PlacePin(
                id = cover?.let { "c:${it.hash}" } ?: "p:${p.zoom}/${p.x}/${p.y}/${p.latitude},${p.longitude}",
                latitude = p.latitude, longitude = p.longitude, count = max(1, p.count), cover = cover, area = p.area,
            )
        }

        fun newer(a: PbFile?, than: PbFile?): Boolean {
            if (a == null) return false
            if (than == null) return true
            val da = a.createdMs() ?: Long.MIN_VALUE
            val db = than.createdMs() ?: Long.MIN_VALUE
            return if (da != db) da > db else a.hash < than.hash
        }
    }
}

private fun PbFile.createdMs(): Long? = if (hasCreated()) created.seconds * 1000 + created.nanos / 1_000_000 else null

/**
 * Places closer on the screen than a pin, drawn as one: their photos
 * counted together, at the member nearest their weighted mean, the newest
 * photo's thumbnail, the area holding them all. The biggest go first and
 * take in what is near them; a pin moved by what it took in can come near
 * another, so that is done again until no two are that close.
 */
object PinMerge {
    /** The least distance between two pins' centres, in points (dp): a pin is 48 with its border, and its count sticks out at the corner. */
    const val GAP = 54.0

    /** [pins] as drawn at [zoom] (the exact one shown). */
    fun merge(pins: List<PlacePin>, zoom: Double, gap: Double = GAP): List<PlacePin> {
        var out = pins
        // Each pass leaves fewer pins, or is the last.
        while (out.size > 1) {
            val next = pass(out, zoom, gap)
            if (next.size == out.size) break
            out = next
        }
        return out
    }

    private data class Cell(val x: Int, val y: Int)

    private fun pass(pins: List<PlacePin>, zoom: Double, gap: Double): List<PlacePin> {
        if (pins.size < 2) return pins
        // World units per point at this zoom.
        val reach = gap / (PlaceTiles.TILE_SIDE * 2.0.pow(zoom))
        if (reach <= 0 || !reach.isFinite()) return pins
        val points = pins.map { it.point }
        fun cell(p: WorldPoint) = Cell(floor(p.x / reach).toInt(), floor(p.y / reach).toInt())
        val cells = HashMap<Cell, MutableList<Int>>()
        points.forEachIndexed { i, p -> cells.getOrPut(cell(p)) { mutableListOf() } += i }
        val order = pins.indices.sortedWith(compareByDescending<Int> { pins[it].count }.thenBy { pins[it].id })
        val taken = BooleanArray(pins.size)
        val out = ArrayList<PlacePin>()
        for (i in order) {
            if (taken[i]) continue
            taken[i] = true
            val members = mutableListOf(i)
            val c = cell(points[i])
            for (dx in -1..1) for (dy in -1..1) {
                for (j in cells[Cell(c.x + dx, c.y + dy)].orEmpty()) {
                    if (taken[j]) continue
                    val ddx = points[j].x - points[i].x
                    val ddy = points[j].y - points[i].y
                    if (ddx * ddx + ddy * ddy < reach * reach) {
                        taken[j] = true
                        members += j
                    }
                }
            }
            out += if (members.size == 1) pins[i] else combine(members.map { pins[it] })
        }
        return out
    }

    fun combine(pins: List<PlacePin>): PlacePin {
        var count = 0
        var x = 0.0
        var y = 0.0
        var cover: PbFile? = null
        var coverId = pins[0].id
        var south = pins[0].area.south
        var west = pins[0].area.west
        var north = pins[0].area.north
        var east = pins[0].area.east
        for (p in pins) {
            val pt = p.point
            count += p.count
            x += pt.x * p.count
            y += pt.y * p.count
            if (PlacePin.newer(p.cover, cover)) {
                cover = p.cover
                coverId = p.id
            }
            south = min(south, p.area.south)
            west = min(west, p.area.west)
            north = max(north, p.area.north)
            east = max(east, p.area.east)
        }
        // Drawn at the member nearest their weighted mean, as the device
        // draws a place at its photo nearest the mean: the mean itself can
        // be where no photo was taken.
        val mx = x / count
        val my = y / count
        var at = pins[0]
        var best = Double.POSITIVE_INFINITY
        for (p in pins) {
            val pt = p.point
            val d = (pt.x - mx) * (pt.x - mx) + (pt.y - my) * (pt.y - my)
            if (d < best) {
                best = d
                at = p
            }
        }
        val area = PlaceArea.newBuilder().setSouth(south).setWest(west).setNorth(north).setEast(east).build()
        return PlacePin(coverId, at.latitude, at.longitude, count, cover, area)
    }
}

/** What a tap on a pin does. */
sealed class PlaceTap {
    /** One photo: the viewer. */
    data class Open(val file: PbFile) : PlaceTap()
    /** Closer: the camera to this rect. */
    data class Zoom(val rect: WorldRect) : PlaceTap()
    /** Too close together to split (or zoomed in far already): the photos in Images, by their area. */
    data class ShowInImages(val area: PlaceArea) : PlaceTap()

    companion object {
        /** From zoom 16 on, and for photos within a few metres of each other, a group opens in Images. */
        const val LAST_SPLIT_ZOOM = 16.0
        const val FEW_METRES = 25.0

        /** [zoom]: the exact zoom shown; [view]: the rect on the screen. */
        fun of(pin: PlacePin, zoom: Double, view: WorldRect): PlaceTap {
            val c = pin.cover
            if (pin.count == 1 && c != null) return Open(c)
            if (zoom >= LAST_SPLIT_ZOOM || metresAcross(pin.area) < FEW_METRES) return ShowInImages(pin.area)
            // Its photos, with room around them - and at least twice as close
            // as now, so a tap always zooms in.
            var target = PlaceTiles.framing(pin.area, minWidth = 0.0, pad = 0.25)
            if (view.width > 0 && (target.width > view.width / 2 || target.height > view.height / 2)) {
                val w = view.width / 2
                val h = view.height / 2
                val p = pin.point
                target = WorldRect(p.x - w / 2, p.y - h / 2, w, h)
            }
            return Zoom(target)
        }

        /** About how many metres an area is across, corner to corner (on the sphere). */
        fun metresAcross(a: PlaceArea): Double {
            val r = 6_371_000.0
            val la1 = a.south * PI / 180
            val la2 = a.north * PI / 180
            val dLa = la2 - la1
            val dLo = (a.east - a.west) * PI / 180
            val h = sin(dLa / 2).pow(2) + cos(la1) * cos(la2) * sin(dLo / 2).pow(2)
            return 2 * r * asin(min(1.0, sqrt(h)))
        }
    }
}

/**
 * GetPhotoPlaces' answers for the session, by search, zoom and tile, each
 * for [TTL_MS] (the device's index follows a change within 15 s, so a few
 * minutes keep a map fresh enough while it is browsed); the extent of each
 * search too. Not thread-safe: PlacesModel's (the main thread).
 */
class PlacesCache {
    private data class Key(val filter: PlacesFilter, val tile: PlaceTileKey)
    private class Entry(val places: List<PhotoPlace>, val at: Long, val reading: Boolean)

    private val tiles = HashMap<Key, Entry>()
    private val extents = HashMap<PlacesFilter, PlacesExtent>()

    /** The places of a tile, when its answer is kept and fresh: empty for a tile without any. */
    fun places(tile: PlaceTileKey, filter: PlacesFilter, now: Long): List<PhotoPlace>? {
        val e = tiles[Key(filter, tile)] ?: return null
        if (now - e.at >= if (e.reading) READING_TTL_MS else TTL_MS) return null
        return e.places
    }

    /** The tiles of [wanted] without a fresh answer, in their order. */
    fun missing(wanted: List<PlaceTileKey>, filter: PlacesFilter, now: Long): List<PlaceTileKey> =
        wanted.filter { places(it, filter, now) == null }

    fun extent(filter: PlacesFilter): PlacesExtent? = extents[filter]

    /** An answer for [asked] (at its zoom): every tile asked for keeps what the answer has in it, nothing when it has nothing. */
    fun put(answer: PhotoPlaces, asked: List<PlaceTileKey>, filter: PlacesFilter, now: Long) {
        val byTile = answer.placesList.groupBy { PlaceTileKey(it.zoom, it.x, it.y) }
        // Under the tiles as asked: an answer for another zoom (never sent)
        // holds nothing for them, and isn't asked for again.
        for (t in asked) tiles[Key(filter, t)] = Entry(byTile[t].orEmpty(), now, answer.reading)
        extents[filter] = PlacesExtent(answer)
    }

    /** The library changed: nothing kept is fresh. The extents stay until the next answer, so a map opens framed as before. */
    fun clearTiles() = tiles.clear()

    fun clear() {
        tiles.clear()
        extents.clear()
    }

    companion object {
        const val TTL_MS = 5 * 60_000L
        /** An answer given while the device is still reading positions. */
        const val READING_TTL_MS = 15_000L
    }
}
