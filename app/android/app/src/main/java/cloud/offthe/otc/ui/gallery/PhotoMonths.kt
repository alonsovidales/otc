// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import androidx.annotation.StringRes
import cloud.offthe.otc.R
import cloud.offthe.otc.i18n.UiText
import java.time.Instant
import java.time.YearMonth
import java.time.ZoneId
import kotlin.math.max
import kotlin.math.min
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosGridPeopleMore
import cloud.offthe.otc.i18n.appPhotosGridPeopleTwo
import cloud.offthe.otc.i18n.appPhotosGridTilePhoto
import cloud.offthe.otc.i18n.appPhotosGridTileVideo
import cloud.offthe.otc.i18n.appPhotosMonthsApril
import cloud.offthe.otc.i18n.appPhotosMonthsAprilShort
import cloud.offthe.otc.i18n.appPhotosMonthsAugust
import cloud.offthe.otc.i18n.appPhotosMonthsAugustShort
import cloud.offthe.otc.i18n.appPhotosMonthsDecember
import cloud.offthe.otc.i18n.appPhotosMonthsDecemberShort
import cloud.offthe.otc.i18n.appPhotosMonthsFebruary
import cloud.offthe.otc.i18n.appPhotosMonthsFebruaryShort
import cloud.offthe.otc.i18n.appPhotosMonthsJanuary
import cloud.offthe.otc.i18n.appPhotosMonthsJanuaryShort
import cloud.offthe.otc.i18n.appPhotosMonthsJuly
import cloud.offthe.otc.i18n.appPhotosMonthsJulyShort
import cloud.offthe.otc.i18n.appPhotosMonthsJune
import cloud.offthe.otc.i18n.appPhotosMonthsJuneShort
import cloud.offthe.otc.i18n.appPhotosMonthsMarch
import cloud.offthe.otc.i18n.appPhotosMonthsMarchShort
import cloud.offthe.otc.i18n.appPhotosMonthsMay
import cloud.offthe.otc.i18n.appPhotosMonthsMayShort
import cloud.offthe.otc.i18n.appPhotosMonthsNoDate
import cloud.offthe.otc.i18n.appPhotosMonthsNovember
import cloud.offthe.otc.i18n.appPhotosMonthsNovemberShort
import cloud.offthe.otc.i18n.appPhotosMonthsOctober
import cloud.offthe.otc.i18n.appPhotosMonthsOctoberShort
import cloud.offthe.otc.i18n.appPhotosMonthsRange
import cloud.offthe.otc.i18n.appPhotosMonthsSeptember
import cloud.offthe.otc.i18n.appPhotosMonthsSeptemberShort
import cloud.offthe.otc.i18n.appPhotosMonthsTitle
import cloud.offthe.otc.i18n.appShellCollectionItems

// Images by month, as the web's PhotoGallery.tsx lays them out: newest
// first under month titles ("March 2024"), a photo without a date staying
// with the month before it. Months short of a row sit side by side while
// they fit (a one- and a two-photo month share a phone's row of three; on
// a wider window a bigger gap keeps them apart), and a month of a row or
// more takes the whole width. A tag search is sorted by how well photos
// match, not by date: then one block of tiles without titles. Pure, so
// the grid (PhotoGalleryView) only draws what this works out, once per
// page or change of width - never per frame - and the tests can check it.

// The months' names, whole and short, from the catalog (app.photos.months.*,
// the iOS app's keys): its English is the web's ("Sep", where en-GB writes
// "Sept"), and a translation brings its own.
private val MONTH_NAMES: List<UiText> = listOf(
    S.appPhotosMonthsJanuary(), S.appPhotosMonthsFebruary(), S.appPhotosMonthsMarch(), S.appPhotosMonthsApril(),
    S.appPhotosMonthsMay(), S.appPhotosMonthsJune(), S.appPhotosMonthsJuly(), S.appPhotosMonthsAugust(),
    S.appPhotosMonthsSeptember(), S.appPhotosMonthsOctober(), S.appPhotosMonthsNovember(), S.appPhotosMonthsDecember(),
)
private val MONTH_SHORT: List<UiText> = listOf(
    S.appPhotosMonthsJanuaryShort(), S.appPhotosMonthsFebruaryShort(), S.appPhotosMonthsMarchShort(),
    S.appPhotosMonthsAprilShort(), S.appPhotosMonthsMayShort(), S.appPhotosMonthsJuneShort(),
    S.appPhotosMonthsJulyShort(), S.appPhotosMonthsAugustShort(), S.appPhotosMonthsSeptemberShort(),
    S.appPhotosMonthsOctoberShort(), S.appPhotosMonthsNovemberShort(), S.appPhotosMonthsDecemberShort(),
)

/**
 * A key whose text arguments include a month's name, with the name as a
 * UiText: the generated accessors (S.appPhotosMonthsTitle(month, year)...)
 * take text arguments as Strings, which these pure functions can't word
 * yet, and UiText words a nested one along with the rest when it is shown
 * (UiTextFormat.values). [args] go in the key's declared order;
 * PhotoTextTest checks each against its accessor.
 */
private fun nested(@StringRes id: Int, vararg args: Any): UiText = UiText.Res(id, args.toList())

/**
 * "2024-03" for a photo's date in [zone] - the phone's own time zone, as
 * the web uses the viewer's. A photo without a date (no `created`) has no
 * month: the caller passes none (the web's monthOf).
 */
fun monthOf(epochSeconds: Long, nanos: Int, zone: ZoneId): String {
    val d = Instant.ofEpochSecond(epochSeconds, nanos.toLong()).atZone(zone)
    // Four digits, as the device's buckets ('%Y-%m') and the iOS app have them.
    return "${d.year.toString().padStart(4, '0')}-${d.monthValue.toString().padStart(2, '0')}"
}

/** The year and month (1-12) a key names. */
private fun monthParts(key: String): Pair<Int, Int>? {
    val p = key.split("-")
    val y = p.getOrNull(0)?.toIntOrNull() ?: return null
    val m = p.getOrNull(1)?.toIntOrNull() ?: 0
    // The web's (m || 1): a month 0 reads as January.
    val month = if (m == 0) 1 else m
    if (month !in 1..12) return null
    return y to month
}

/** "Mar 2024": a month's short name and its year (the year as it is, never grouped). */
private fun shortTitle(p: Pair<Int, Int>): UiText = nested(R.string.app_photos_months_short_title, MONTH_SHORT[p.second - 1], "${p.first}")

/** "March 2024" - a month's title. */
fun monthTitle(key: String): UiText =
    monthParts(key)?.let { (y, m) -> nested(R.string.app_photos_months_title, MONTH_NAMES[m - 1], "$y") } ?: UiText.Raw(key)

/** "Mar 2024" - where "September 2026" doesn't fit (a month one tile wide). */
fun monthShort(key: String): UiText = monthParts(key)?.let(::shortTitle) ?: UiText.Raw(key)

/**
 * The scrubber's bubble: "Mar 2024", or "No date" for the device's bucket
 * of photos without one (a zero month) - the web's DateScrubber monthLabel.
 */
fun scrubLabel(month: String): UiText {
    val m = month.drop(5).take(2).toIntOrNull()
    val y = month.take(4).toIntOrNull()
    return if (m != null && m in 1..12 && y != null && y > 0) shortTitle(y to m) else S.appPhotosMonthsNoDate()
}

/**
 * The months the photos span, from the date buckets (newest first): "Mar
 * 2024", "Mar – Oct 2024" or "Mar 2024 – Oct 2026" ("2024 – 2026" with
 * [years], which fits a narrow header's line). The web's spanLabel.
 * Nothing (an empty text) without buckets.
 */
fun spanLabel(buckets: List<PhotoGalleryViewModel.DateBucket>, years: Boolean = false): UiText {
    val none = UiText.Raw("")
    if (buckets.isEmpty()) return none
    val from = monthParts(buckets.last().month) ?: return none
    val to = monthParts(buckets.first().month) ?: return none
    if (from == to) return shortTitle(to)
    if (from.first == to.first) {
        return nested(R.string.app_photos_months_range_in_year, MONTH_SHORT[from.second - 1], MONTH_SHORT[to.second - 1], "${to.first}")
    }
    if (years) return S.appPhotosMonthsRange("${from.first}", "${to.first}")
    return nested(R.string.app_photos_months_range, shortTitle(from), shortTitle(to))
}

/** "1 item", "1,234 items" - the web's count(): how many photos and videos an open collection holds. */
fun itemsLabel(n: Int): UiText = S.appShellCollectionItems(n)

/** A person filter's title: "Ana", "Ana & Leo", "Ana, Leo & 2 others". */
fun peopleTitle(names: List<String>): UiText = when (names.size) {
    0, 1 -> UiText.Raw(names.firstOrNull() ?: "")
    2 -> S.appPhotosGridPeopleTwo(names[0], names[1])
    else -> S.appPhotosGridPeopleMore(names[0], names[1], names.size - 2)
}

/**
 * The scrubber's jump: the last instant of [month] in [zone] (epoch ms), so
 * the search starts at that month's newest photo and reads back from there
 * as scrolling there would - the web's `new Date(y, m, 0, 23, 59, 59, 999)`.
 * A zero month is the device's bucket of photos without a date ("No date",
 * a zero datetime): there the web's Date(0, 0, 0, ...) is the last instant
 * of 31 December 1899, before every real date, which finds those photos.
 * Null only for what is no month at all.
 */
fun jumpCutoffMs(month: String, zone: ZoneId): Long? {
    val p = month.split("-").mapNotNull { it.toIntOrNull() }
    if (p.size != 2) return null
    val ym = when (p[1]) {
        0 -> YearMonth.of(1899, 12)
        in 1..12 -> YearMonth.of(p[0], p[1])
        else -> return null
    }
    return ym.atEndOfMonth().atTime(23, 59, 59, 999_000_000).atZone(zone).toInstant().toEpochMilli()
}

/**
 * A tile as TalkBack reads it, the web's tile label: "Photo, 5 October
 * 2026", "Video, ..." - the day in [zone], the phone's own - or "Photo"
 * alone without a date. [createdMs]: the photo's date, epoch ms. The day's
 * words come from the catalog (app.photos.months.day), so the English stays
 * the web's en-GB "5 October 2026".
 */
fun tileLabel(video: Boolean, createdMs: Long?, zone: ZoneId): UiText {
    if (createdMs == null) return if (video) S.appPhotosGridTileVideo() else S.appPhotosGridTilePhoto()
    val d = Instant.ofEpochMilli(createdMs).atZone(zone)
    val day = nested(R.string.app_photos_months_day, d.dayOfMonth, MONTH_NAMES[d.monthValue - 1], "${d.year}")
    return nested(if (video) R.string.app_photos_grid_tile_video_on else R.string.app_photos_grid_tile_photo_on, day)
}

/** The bucket under the scrubber at [frac] of the track (0 = the newest photo). */
fun scrubBucketAt(buckets: List<PhotoGalleryViewModel.DateBucket>, frac: Float): PhotoGalleryViewModel.DateBucket? {
    val total = buckets.lastOrNull()?.end ?: return null
    if (total <= 0) return null
    val idx = (frac.coerceIn(0f, 1f) * total).toInt()
    return buckets.firstOrNull { idx >= it.start && idx < it.end } ?: buckets.last()
}

// ---- the layout ---------------------------------------------------------------

/** Photos [start, end) of one month in a row of the list ([month] null: no date at the top). */
data class MonthRun(val month: String?, val start: Int, val end: Int)

/**
 * The photos' months as runs, in the list's own order (never re-sorted, so
 * the viewer swipes in the order the grid shows): a photo without a date
 * stays with the month before it; at the very top it gets a run of its
 * own, with an empty title line.
 */
fun monthRuns(months: List<String?>): List<MonthRun> {
    val runs = ArrayList<MonthRun>()
    var month: String? = null
    var start = 0
    for (i in months.indices) {
        val m = months[i]
        if (i == 0) { month = m; continue }
        if (m == null || m == month) continue
        runs += MonthRun(month, start, i)
        month = m
        start = i
    }
    if (months.isNotEmpty()) runs += MonthRun(month, start, months.size)
    return runs
}

/**
 * One month's part of a titled row: its title over its tiles [start, end),
 * [slots] tiles wide - after [lead] grey tiles (issue #194: a month still
 * filling from above, RoomAbove).
 */
data class Segment(val month: String?, val start: Int, val end: Int, val slots: Int, val lead: Int = 0)

/** A row of the list: tiles [start, end) of the photos, after [lead] grey tiles. */
sealed interface GalleryRow {
    val start: Int
    val end: Int
    val lead: Int
}

/** Months side by side, each its title over one row of its tiles; or a month's first row under its title. */
data class TitledRow(val segments: List<Segment>) : GalleryRow {
    override val start get() = segments.first().start
    override val end get() = segments.last().end
    // Only a line's first month can be the one filling from above.
    override val lead get() = segments.first().lead
}

/**
 * A full-width row of tiles: a long month's next ones, or a tag search's -
 * [lead] grey ones first in a month still filling from above (issue #194).
 */
data class TileRow(override val start: Int, override val end: Int, override val lead: Int = 0) : GalleryRow

/**
 * Issue #194: the room the top of the grid keeps while newer photos are
 * still to come above a jump, from the date buckets. [firstRoom]: the first
 * month's count when its photos came from the upward search - they arrive
 * oldest first, so they fill it from its end and its first slots stay grey
 * until they come, without a tile moving (as iOS's firstRoom and the web's
 * topReserve). Nothing is kept above a month that is complete - the
 * jump's own: the newer photos show they are coming with the spinner at
 * the top, and land above it.
 */
data class RoomAbove(val firstRoom: Int?)

/**
 * Issue #194: the room to keep above the photos ([firstMonth]: the first
 * photo's month; [firstUp]: it came from the upward search) while newer
 * ones are to come, from the date buckets (newest first). A month the
 * buckets don't have keeps nothing.
 */
fun roomAbove(firstMonth: String?, firstUp: Boolean, buckets: List<PhotoGalleryViewModel.DateBucket>): RoomAbove {
    if (firstMonth == null || !firstUp) return RoomAbove(null)
    return RoomAbove(buckets.firstOrNull { it.month == firstMonth }?.count)
}

/**
 * The tiles' grid: [cols] across, each [tile] wide, [gap] between tiles,
 * [secGap] between months side by side (any unit, the same for all).
 */
data class GridGeometry(val cols: Int, val tile: Float, val gap: Float, val secGap: Float) {
    val width get() = cols * tile + (cols - 1) * gap
    /** How wide a month of [n] tiles is: as many as it has, up to a row. */
    fun widthOf(n: Int) = min(width, n * (tile + gap) - gap)

    companion object {
        /** As many columns of at least [minTile] as fit in [width] ([fixedCols]: a phone's three), sharing it exactly. */
        fun fit(width: Float, gap: Float, secGap: Float, minTile: Float, fixedCols: Int? = null): GridGeometry {
            val cols = fixedCols ?: max(1, ((width + gap) / (minTile + gap)).toInt())
            return GridGeometry(cols, max(1f, (width - (cols - 1) * gap) / cols), gap, secGap)
        }
    }
}

/**
 * The rows, and in date order each row's key for the list: its month and
 * its place in the month ("2024-03#0" is the month's title row), so a row
 * keeps its key while photos land above it or fill its grey tiles (issue
 * #194). Null keys: the caller keys the rows (a tag search's).
 */
class GalleryLayout(val rows: List<GalleryRow>, private val rowOfItem: IntArray, val cols: Int, private val keys: List<String>? = null) {
    /** The row a photo is in, or -1. */
    fun rowOf(item: Int): Int = rowOfItem.getOrElse(item) { -1 }

    /** Row [i]'s key for the list, null where the caller keys the rows. */
    fun keyOf(i: Int): String? = keys?.getOrNull(i)

    /** The row with [key], or -1. */
    fun rowWithKey(key: Any?): Int = keys?.indexOf(key) ?: -1
}

/**
 * The rows the grid draws. [titled]: date order, so months with titles;
 * otherwise one block of full rows. [monthCounts]: the date buckets' count
 * per month, and [more]: more pages to come - the last month then takes
 * the room its photos will need now (as wide as the device says it is), so
 * the next page fills it in instead of moving it off a row it shared.
 * [above] (issue #194, newer photos to come above a jump): the first month
 * keeps the room the buckets give it, its first slots grey until its
 * photos come.
 */
fun layoutGallery(
    runs: List<MonthRun>, titled: Boolean, g: GridGeometry,
    monthCounts: Map<String, Int>? = null, more: Boolean = false,
    above: RoomAbove? = null,
): GalleryLayout {
    val cols = g.cols
    val total = runs.lastOrNull()?.end ?: 0
    val rows = ArrayList<GalleryRow>()
    val keys = ArrayList<String>()
    fun fullRows(from: Int, to: Int) {
        var s = from
        while (s < to) {
            val e = min(s + cols, to)
            rows += TileRow(s, e)
            s = e
        }
    }
    if (!titled) {
        if (total > 0) fullRows(runs.first().start, total)
    } else {
        // Issue #194: grey slots before the first month's photos, while
        // it is still filling from above.
        val leads = IntArray(runs.size)
        if (above != null && runs.isNotEmpty() && runs.first().month != null) {
            val first = runs.first()
            leads[0] = max(0, (above.firstRoom ?: 0) - (first.end - first.start))
        }
        val used = HashSet<String>()
        fun keyFor(month: String?, idx: Int, run: Int): String {
            val k = "${month ?: "-"}#$idx"
            // A month is one run; should one come twice, its rows still get keys of their own.
            return if (used.add(k)) k else "$k~$run".also { used.add(it) }
        }
        val line = ArrayList<Segment>()
        var lineKey = ""
        var lineWidth = 0f
        fun flush() {
            if (line.isEmpty()) return
            rows += TitledRow(line.toList())
            keys += lineKey
            line.clear()
            lineWidth = 0f
        }
        runs.forEachIndexed { r, run ->
            val n = run.end - run.start
            val lead = leads[r]
            // The places of the month: [lead] grey slots, then its photos.
            val places = n + lead
            val room = if (r == runs.lastIndex && more && run.month != null) max(places, monthCounts?.get(run.month) ?: 0) else places
            // Places [from, to) of this run as tiles: its photos and the grey slots before them.
            fun part(from: Int, to: Int) = Triple(run.start + max(0, from - lead), run.start + max(0, to - lead), max(0, min(to, lead) - from))
            if (room >= cols) {
                flush()
                var idx = 0
                val firstEnd = min(cols, places)
                val (s0, e0, l0) = part(0, firstEnd)
                rows += TitledRow(listOf(Segment(run.month, s0, e0, cols, l0)))
                keys += keyFor(run.month, idx++, r)
                var at = firstEnd
                while (at < places) {
                    val to = min(at + cols, places)
                    val (s, e, l) = part(at, to)
                    rows += TileRow(s, e, l)
                    keys += keyFor(run.month, idx++, r)
                    at = to
                }
            } else {
                val w = g.widthOf(room)
                // Half a unit of slack, as the web's tiles are half a pixel
                // narrower: rounding never pushes a month that fits off the row.
                if (line.isNotEmpty() && lineWidth + g.secGap + w > g.width + 0.5f) flush()
                if (line.isEmpty()) lineKey = keyFor(run.month, 0, r)
                lineWidth = if (line.isEmpty()) w else lineWidth + g.secGap + w
                line += Segment(run.month, run.start, run.end, room, lead)
            }
        }
        flush()
    }
    val rowOf = IntArray(total) { -1 }
    rows.forEachIndexed { i, row -> for (k in row.start until row.end) rowOf[k] = i }
    return GalleryLayout(rows, rowOf, cols, if (titled) keys else null)
}

// ---- the photo under a finger (issue #200) ---------------------------------------

/**
 * Issue #200: the photo at ([x], [y]) in this row as the grid draws it
 * (PhotoGalleryView's GalleryRowView), or null over a gap, a month's title
 * or an empty slot - the drag to select keeps the last photo reached then.
 * [x] from the tiles' start edge, [y] from the row's top, above the [top]
 * gap it has over it; [title] how tall a month's title line is, with the
 * gap under it. [g] and the rest in pixels. The iOS PhotoGridLayout.item.
 */
fun GalleryRow.itemAt(x: Float, y: Float, g: GridGeometry, top: Float, title: Float): Int? {
    val ty = y - top
    return when (this) {
        is TileRow -> cellAt(start, end, g.cols, g.width, x, ty, g.gap, lead)
        is TitledRow -> {
            // Months side by side, each as wide as its own tiles
            // (MonthsSideBySide), [secGap] apart.
            var sx = 0f
            for (seg in segments) {
                val slots = min(seg.slots, g.cols)
                val w = if (slots >= g.cols) g.width else slots * g.tile + (slots - 1) * g.gap
                if (x < sx) return null
                if (x < sx + w) return cellAt(seg.start, seg.end, slots, w, x - sx, ty - title, g.gap, seg.lead)
                sx += w + g.secGap
            }
            null
        }
    }
}

// Tiles [start, end) after [lead] grey ones in a row [slots] wide and
// [width] across, [gap] apart: the one at ([x], [y]) from the row's corner,
// null over a gap, a grey tile or an empty slot.
private fun cellAt(start: Int, end: Int, slots: Int, width: Float, x: Float, y: Float, gap: Float, lead: Int = 0): Int? {
    if (slots <= 0 || x < 0f || x >= width || y < 0f) return null
    val tile = (width - (slots - 1) * gap) / slots
    if (y >= tile) return null
    val col = (x / (tile + gap)).toInt()
    if (x - col * (tile + gap) >= tile) return null
    if (col < lead) return null
    return if (col - lead < end - start) start + col - lead else null
}
