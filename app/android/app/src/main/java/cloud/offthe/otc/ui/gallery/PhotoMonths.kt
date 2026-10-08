// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import java.time.Instant
import java.time.YearMonth
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale
import kotlin.math.max
import kotlin.math.min

// Images by month, as the web's PhotoGallery.tsx lays them out: newest
// first under month titles ("March 2024"), a photo without a date staying
// with the month before it. Months short of a row sit side by side while
// they fit (a one- and a two-photo month share a phone's row of three; on
// a wider window a bigger gap keeps them apart), and a month of a row or
// more takes the whole width. A tag search is sorted by how well photos
// match, not by date: then one block of tiles without titles. Pure, so
// the grid (PhotoGalleryView) only draws what this works out, once per
// page or change of width - never per frame - and the tests can check it.

private val MONTH_NAMES = listOf(
    "January", "February", "March", "April", "May", "June",
    "July", "August", "September", "October", "November", "December",
)

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

private fun monthParts(key: String): Pair<Int, String>? {
    val p = key.split("-")
    val y = p.getOrNull(0)?.toIntOrNull() ?: return null
    val m = p.getOrNull(1)?.toIntOrNull() ?: 0
    // The web's (m || 1): a month 0 reads as January.
    val name = MONTH_NAMES.getOrNull((if (m == 0) 1 else m) - 1) ?: return null
    return y to name
}

/** "March 2024" - a month's title. */
fun monthTitle(key: String): String = monthParts(key)?.let { (y, name) -> "$name $y" } ?: key

/** "Mar 2024" - where "September 2026" doesn't fit (a month one tile wide). */
fun monthShort(key: String): String = monthParts(key)?.let { (y, name) -> "${name.take(3)} $y" } ?: key

/**
 * The scrubber's bubble: "Mar 2024", or "No date" for the device's bucket
 * of photos without one (a zero month) - the web's DateScrubber monthLabel.
 */
fun scrubLabel(month: String): String {
    val m = month.drop(5).take(2).toIntOrNull()
    val y = month.take(4).toIntOrNull()
    return if (m != null && m in 1..12 && y != null && y > 0) "${MONTH_NAMES[m - 1].take(3)} $y" else "No date"
}

/**
 * The months the photos span, from the date buckets (newest first): "Mar
 * 2024", "Mar – Oct 2024" or "Mar 2024 – Oct 2026" ("2024 – 2026" with
 * [years], which fits a narrow header's line). The web's spanLabel.
 */
fun spanLabel(buckets: List<PhotoGalleryViewModel.DateBucket>, years: Boolean = false): String {
    if (buckets.isEmpty()) return ""
    val from = monthParts(buckets.last().month) ?: return ""
    val to = monthParts(buckets.first().month) ?: return ""
    fun short(p: Pair<Int, String>) = p.second.take(3)
    if (from == to) return "${short(to)} ${to.first}"
    if (from.first == to.first) return "${short(from)} – ${short(to)} ${to.first}"
    if (years) return "${from.first} – ${to.first}"
    return "${short(from)} ${from.first} – ${short(to)} ${to.first}"
}

/** "1 item", "1,234 items" - the web's count(), in English as the rest of the app. */
fun countLabel(n: Int, one: String, many: String = "${one}s"): String =
    "${String.format(Locale.US, "%,d", n)} ${if (n == 1) one else many}"

/** A person filter's title: "Ana", "Ana & Leo", "Ana, Leo & 2 others". */
fun peopleTitle(names: List<String>): String =
    if (names.size <= 2) names.joinToString(" & ")
    else "${names.take(2).joinToString(", ")} & ${countLabel(names.size - 2, "other")}"

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

// The web's tile label date (Intl en-GB: day, long month, year): "5 October 2026".
private val TILE_DAY: DateTimeFormatter = DateTimeFormatter.ofPattern("d MMMM y", Locale.UK)

/**
 * A tile as TalkBack reads it, the web's tile label: "Photo, 5 October
 * 2026", "Video, ..." - the day in [zone], the phone's own - or "Photo"
 * alone without a date. [createdMs]: the photo's date, epoch ms.
 */
fun tileLabel(video: Boolean, createdMs: Long?, zone: ZoneId): String {
    val kind = if (video) "Video" else "Photo"
    if (createdMs == null) return kind
    return "$kind, ${TILE_DAY.format(Instant.ofEpochMilli(createdMs).atZone(zone))}"
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

/** One month's part of a titled row: its title over its tiles [start, end), [slots] tiles wide. */
data class Segment(val month: String?, val start: Int, val end: Int, val slots: Int)

/** A row of the list: tiles [start, end) of the photos. */
sealed interface GalleryRow {
    val start: Int
    val end: Int
}

/** Months side by side, each its title over one row of its tiles; or a month's first row under its title. */
data class TitledRow(val segments: List<Segment>) : GalleryRow {
    override val start get() = segments.first().start
    override val end get() = segments.last().end
}

/** A full-width row of tiles: a long month's next ones, or a tag search's. */
data class TileRow(override val start: Int, override val end: Int) : GalleryRow

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

class GalleryLayout(val rows: List<GalleryRow>, private val rowOfItem: IntArray, val cols: Int) {
    /** The row a photo is in, or -1. */
    fun rowOf(item: Int): Int = rowOfItem.getOrElse(item) { -1 }
}

/**
 * The rows the grid draws. [titled]: date order, so months with titles;
 * otherwise one block of full rows. [monthCounts]: the date buckets' count
 * per month, and [more]: more pages to come - the last month then takes
 * the room its photos will need now (as wide as the device says it is), so
 * the next page fills it in instead of moving it off a row it shared.
 */
fun layoutGallery(
    runs: List<MonthRun>, titled: Boolean, g: GridGeometry,
    monthCounts: Map<String, Int>? = null, more: Boolean = false,
): GalleryLayout {
    val cols = g.cols
    val total = runs.lastOrNull()?.end ?: 0
    val rows = ArrayList<GalleryRow>()
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
        val line = ArrayList<Segment>()
        var lineWidth = 0f
        fun flush() {
            if (line.isEmpty()) return
            rows += TitledRow(line.toList())
            line.clear()
            lineWidth = 0f
        }
        runs.forEachIndexed { r, run ->
            val n = run.end - run.start
            val room = if (r == runs.lastIndex && more && run.month != null) max(n, monthCounts?.get(run.month) ?: 0) else n
            if (room >= cols) {
                flush()
                val firstEnd = min(run.start + cols, run.end)
                rows += TitledRow(listOf(Segment(run.month, run.start, firstEnd, cols)))
                fullRows(firstEnd, run.end)
            } else {
                val w = g.widthOf(room)
                // Half a unit of slack, as the web's tiles are half a pixel
                // narrower: rounding never pushes a month that fits off the row.
                if (line.isNotEmpty() && lineWidth + g.secGap + w > g.width + 0.5f) flush()
                lineWidth = if (line.isEmpty()) w else lineWidth + g.secGap + w
                line += Segment(run.month, run.start, run.end, room)
            }
        }
        flush()
    }
    val rowOf = IntArray(total) { -1 }
    rows.forEachIndexed { i, row -> for (k in row.start until row.end) rowOf[k] = i }
    return GalleryLayout(rows, rowOf, cols)
}
