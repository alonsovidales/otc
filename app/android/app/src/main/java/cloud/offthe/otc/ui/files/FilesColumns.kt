// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import kotlin.math.ceil
import kotlin.math.roundToInt

// The widths of Files' list columns on a wide screen (the rules; the list
// draws them in FilesColumnsView.kt). The owner, on an open Fold 7, asked
// for the web app's columns "when the screen is wide": a port of the web's
// filesColumns.ts (and of its tests, FilesColumnsTest.kt), in dp.
//
// - Only in the wide layout (MainView's, a window MENU_RAIL_MIN_DP = 600dp
//   or more across, the side rail or menu beside the page) and the List
//   view; a narrow window keeps its rows of a name over its size.
// - Name takes what the other columns leave, and never less than room for
//   10 characters of the name with its row's icons (nameMin): the file's
//   icon before it, and after it a file's versions pill and lock, or a
//   folder's mark for Images and lock.
// - Size, Created and Modified have a width each: the one given on this
//   phone (otc_settings, STORAGE_KEY), else a default that fits a long size
//   or date in the app's language.
// - As the list narrows, Created goes, then Modified, once Name would keep
//   less than nameRoom with them at their defaults; Size always stays. What
//   shows is fitted to the room: columns wider than it allows give way, the
//   last first, down to their minimums. The list never scrolls sideways.
// - The handle at a heading's right edge (end edge, right to left) resizes
//   that column: wider takes the room from the columns after it, nearest
//   first, down to their minimums; narrower gives it to the next column. A
//   double tap puts the column back (Name: every column); TalkBack's Wider
//   and Narrower move it by STEP.

object FilesColumns {
    /** The columns, Name first: the keys Files sorts by (FilesSort.kt), which a heading's tap picks. */
    val ALL: List<FilesSortKey> = listOf(FilesSortKey.NAME, FilesSortKey.SIZE, FilesSortKey.CREATED, FilesSortKey.MODIFIED)

    // Hidden in this order as the list narrows.
    private val HIDE_ORDER = listOf(FilesSortKey.CREATED, FilesSortKey.MODIFIED)

    /** Where the widths given on this phone are kept: otc_settings, beside files_sort ("size:120,modified:210"). */
    const val STORAGE_KEY = "files_columns"

    /** What TalkBack's Wider and Narrower move a column's edge by, in dp (the web's arrow keys' cKeyStep). */
    const val STEP = 16

    // Never kept wider than this (a stored value is checked against it).
    private const val MAX_STORED = 4000

    /** Before Name, the row's start padding (8) and selection circle (36); after the last column, its end padding (8). */
    const val FRAME = 8 + 36 + 8

    /** Name's cell before the name: the file's icon (28) and the gap after it (8). */
    const val NAME_LEAD = 28 + 8

    /**
     * After the name, at most: a file's versions pill (a text button, 58
     * at least) and its lock (36); a folder's mark for Images and lock take 72.
     */
    const val NAME_TRAIL = 58 + 36

    /** A column of the list that isn't Name. */
    fun isFixed(k: FilesSortKey) = k != FilesSortKey.NAME

    /**
     * What the columns are measured from, in the list's fonts and the app's
     * language, in dp: each fixed column's cells' start plus end padding;
     * "0000000000…" in a name's style; a long size and a short one; a long
     * date and time, and the date alone.
     */
    data class Samples(val pad: Float, val name: Float, val size: Float, val sizeShort: Float, val date: Float, val dateShort: Float)

    /** What the columns need (dp): Name's minimum and the room it keeps before a date column shows; the others' minimums and defaults. */
    data class Metrics(
        val frame: Int, val nameText: Int, val nameMin: Int, val nameRoom: Int,
        val min: Map<FilesSortKey, Int>, val def: Map<FilesSortKey, Int>,
    )

    /** The columns shown, Name first, and each one's width in dp (Name's is what the others leave). */
    data class Layout(val shown: List<FilesSortKey>, val width: Map<FilesSortKey, Int>) {
        val fixed: List<FilesSortKey> get() = shown.filter(::isFixed)
    }

    /** A handle's values: its column's width, its minimum and its most. */
    data class Edge(val now: Int, val min: Int, val max: Int)

    private fun up(x: Float): Int = ceil(x.toDouble()).toInt()

    /** The minimums and defaults from what the list measured. */
    fun metrics(s: Samples): Metrics {
        val nameMin = up(NAME_LEAD + s.name + NAME_TRAIL + 2)
        return Metrics(
            frame = FRAME,
            nameText = up(s.name),
            nameMin = nameMin,
            nameRoom = nameMin + up(s.name),
            min = mapOf(
                FilesSortKey.SIZE to up(s.pad + s.sizeShort + 2),
                FilesSortKey.CREATED to up(s.pad + s.dateShort + 4),
                FilesSortKey.MODIFIED to up(s.pad + s.dateShort + 4),
            ),
            def = mapOf(
                FilesSortKey.SIZE to up(s.pad + s.size + 4),
                FilesSortKey.CREATED to up(s.pad + s.date + 4),
                FilesSortKey.MODIFIED to up(s.pad + s.date + 4),
            ),
        )
    }

    private fun minWidth(k: FilesSortKey, m: Metrics): Int = if (k == FilesSortKey.NAME) m.nameMin else m.min.getValue(k)

    /** The columns a list [room] dp wide shows: Created goes first, then Modified; Size always stays. */
    fun shown(room: Int, m: Metrics): List<FilesSortKey> {
        var shown = ALL
        for (c in HIDE_ORDER) {
            if (m.frame + m.nameRoom + shown.filter(::isFixed).sumOf { m.def.getValue(it) } <= room) break
            shown = shown - c
        }
        return shown
    }

    /** The layout for a list [room] dp wide with the widths [given] on this phone. */
    fun fit(room: Int, given: Map<FilesSortKey, Int>, m: Metrics): Layout {
        val shown = shown(room, m)
        val fixed = shown.filter(::isFixed)
        val width = HashMap<FilesSortKey, Int>()
        for (k in fixed) width[k] = maxOf(m.min.getValue(k), given[k] ?: m.def.getValue(k))
        // Name keeps its minimum: the widest give way, from the last column.
        var over = m.frame + m.nameMin + fixed.sumOf { width.getValue(it) } - room
        for (k in fixed.asReversed()) {
            if (over <= 0) break
            val give = kotlin.math.min(over, width.getValue(k) - m.min.getValue(k))
            width[k] = width.getValue(k) - give
            over -= give
        }
        width[FilesSortKey.NAME] = maxOf(0, room - m.frame - fixed.sumOf { width.getValue(it) })
        return Layout(shown, width)
    }

    /**
     * [col]'s end edge moved by [dx] dp from where it was in [l]: wider takes
     * the room from the columns after it, nearest first, down to their
     * minimums; narrower, down to its own minimum, gives it to the next one.
     * The last column shown has no edge to move; nor has one not shown.
     */
    fun resize(l: Layout, m: Metrics, col: FilesSortKey, dx: Float): Layout {
        val i = l.shown.indexOf(col)
        val step = dx.roundToInt()
        if (i < 0 || i >= l.shown.size - 1 || step == 0) return l
        val w = l.shown.map { l.width.getValue(it) }.toIntArray()
        val min = l.shown.map { minWidth(it, m) }
        if (step > 0) {
            var need = step
            var j = i + 1
            while (j < w.size && need > 0) {
                val take = kotlin.math.min(need, maxOf(0, w[j] - min[j]))
                w[j] -= take
                w[i] += take
                need -= take
                j++
            }
        } else {
            val take = kotlin.math.min(-step, maxOf(0, w[i] - min[i]))
            w[i] -= take
            w[i + 1] += take
        }
        val width = HashMap(l.width)
        l.shown.forEachIndexed { j, k -> width[k] = w[j] }
        return Layout(l.shown, width)
    }

    /** The widths to keep after a resize from [before] to [after]: those it changed, beside those [given]. */
    fun widthsAfter(given: Map<FilesSortKey, Int>, before: Layout, after: Layout): Map<FilesSortKey, Int> {
        val out = LinkedHashMap(given)
        for (k in after.fixed) if (after.width[k] != before.width[k]) out[k] = after.width.getValue(k)
        return out
    }

    /** [col] put back as it was: Name puts every column back, as Name's own width is what they leave. */
    fun reset(given: Map<FilesSortKey, Int>, col: FilesSortKey): Map<FilesSortKey, Int> =
        if (col == FilesSortKey.NAME) emptyMap() else given - col

    /** [col]'s handle: its width, its minimum and the most it can take from the columns after it. */
    fun edge(l: Layout, m: Metrics, col: FilesSortKey): Edge {
        val i = l.shown.indexOf(col)
        val now = l.width[col] ?: 0
        var max = now
        if (i >= 0) for (k in l.shown.drop(i + 1)) max += maxOf(0, l.width.getValue(k) - minWidth(k, m))
        return Edge(now, kotlin.math.min(now, minWidth(col, m)), max)
    }

    /** The widths as kept: "size:120,modified:210", in the columns' order; null when none is given. */
    fun format(widths: Map<FilesSortKey, Int>): String? =
        ALL.filter { isFixed(it) && it in widths }.joinToString(",") { "${it.stored}:${widths.getValue(it)}" }.ifEmpty { null }

    /** Kept widths as read back: anything but a sane width for a column that has one is left out. */
    fun parse(raw: String?): Map<FilesSortKey, Int> {
        if (raw.isNullOrBlank()) return emptyMap()
        val out = LinkedHashMap<FilesSortKey, Int>()
        for (part in raw.split(',')) {
            val kv = part.split(':')
            if (kv.size != 2) continue
            val key = ALL.firstOrNull { it.stored == kv[0].trim() } ?: continue
            if (!isFixed(key)) continue
            val n = kv[1].trim().toIntOrNull() ?: continue
            if (n in 1..MAX_STORED) out[key] = n
        }
        return out
    }
}
