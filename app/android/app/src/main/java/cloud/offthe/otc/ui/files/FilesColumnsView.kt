// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import android.content.SharedPreferences
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.LocalTextStyle
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import androidx.core.content.edit
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appFilesColumnCreated
import cloud.offthe.otc.i18n.appFilesColumnModified
import cloud.offthe.otc.i18n.appFilesColumnName
import cloud.offthe.otc.i18n.appFilesColumnNarrower
import cloud.offthe.otc.i18n.appFilesColumnReset
import cloud.offthe.otc.i18n.appFilesColumnResize
import cloud.offthe.otc.i18n.appFilesColumnSize
import cloud.offthe.otc.i18n.appFilesColumnWider
import cloud.offthe.otc.ui.common.formatBytes
import java.text.DateFormat
import java.util.Calendar
import java.util.Date

// Files' list columns on a wide screen, as drawn (the rules are
// FilesColumns.kt): a row of headings over the list - each sorts by its
// column when tapped (FilesSort's keys, again: the other way), its arrow
// showing the order - with a handle at each one's end edge but the last
// that a drag resizes (a double tap puts it back; TalkBack's actions Wider,
// Narrower and Reset width), and each row's Size, Created and Modified
// cells after its name. The heading row and the rows share the same frame
// (the row's padding and selection circle before Name, its padding after),
// so the columns line up. Widths given are kept in otc_settings
// (FilesColumns.STORAGE_KEY); while a handle is dragged the list shows
// the layout the drag makes, and keeps it when the finger lifts.

/** The start and end padding of a Size, Created or Modified cell. */
private val CELL_PAD = 8.dp

/** The heading row's height. */
private val HEADING_HEIGHT = 40.dp

/** A resize handle's width, at its heading's end, where it takes touches. */
private val HANDLE_WIDTH = 20.dp

/** Before Name: the row's start padding and its selection circle (FilesColumns.FRAME, less the end padding). */
private val NAME_START = 44.dp

/** The widths given on this phone, kept in [prefs], and the layout a drag shows meanwhile ([dragged], [draggedColumn]'s handle). */
@Stable
internal class FilesColumnsState(private val prefs: SharedPreferences?) {
    var given: Map<FilesSortKey, Int> by mutableStateOf(FilesColumns.parse(prefs?.getString(FilesColumns.STORAGE_KEY, null)))
        private set
    var dragged: FilesColumns.Layout? by mutableStateOf(null)
    var draggedColumn: FilesSortKey? by mutableStateOf(null)

    /** [widths] from now on, kept for the next launch. */
    fun keep(widths: Map<FilesSortKey, Int>) {
        if (widths == given) return
        given = widths
        val text = FilesColumns.format(widths)
        prefs?.edit { if (text == null) remove(FilesColumns.STORAGE_KEY) else putString(FilesColumns.STORAGE_KEY, text) }
    }

    /** The layout for a list [room] dp wide: the drag's while one goes on. */
    fun layout(room: Int, metrics: FilesColumns.Metrics): FilesColumns.Layout = dragged ?: FilesColumns.fit(room, given, metrics)
}

/** A date in a cell: the date and time, as the versions' list shows them. */
@Composable
internal fun rememberCellDates(): DateFormat {
    val locale = LocalConfiguration.current.locales[0]
    return remember(locale) { DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT) }
}

/**
 * What the columns need, measured in the list's fonts and the app's
 * language (FilesColumns.Samples): ten characters of a name, a long size
 * and a short one, a long date and time and the date alone.
 */
@Composable
internal fun rememberFilesColumnMetrics(dates: DateFormat): FilesColumns.Metrics {
    val nameStyle = LocalTextStyle.current
    val cellStyle = MaterialTheme.typography.bodyMedium
    val measurer = rememberTextMeasurer(cacheSize = 0)
    val density = LocalDensity.current
    return remember(nameStyle, cellStyle, density, dates) {
        fun width(text: String, style: TextStyle) =
            with(density) { measurer.measure(text, style, softWrap = false, maxLines = 1, density = density).size.width.toDp().value }
        val sample = Calendar.getInstance().apply { set(2026, Calendar.DECEMBER, 28, 22, 58, 58) }.time
        FilesColumns.metrics(
            FilesColumns.Samples(
                pad = CELL_PAD.value * 2,
                name = width("0000000000…", nameStyle),
                size = width(formatBytes(1023L shl 20), cellStyle),
                sizeShort = width(formatBytes(1536), cellStyle),
                date = width(dates.format(sample), cellStyle),
                dateShort = width(DateFormat.getDateInstance(DateFormat.SHORT).format(sample), cellStyle),
            ),
        )
    }
}

/** A column's heading. */
internal fun columnLabel(k: FilesSortKey): UiText = when (k) {
    FilesSortKey.NAME -> S.appFilesColumnName()
    FilesSortKey.SIZE -> S.appFilesColumnSize()
    FilesSortKey.CREATED -> S.appFilesColumnCreated()
    FilesSortKey.MODIFIED -> S.appFilesColumnModified()
}

/** What a row shows in [k]'s cell: "—" for what the device didn't send (a folder has no size or dates), nothing on "..". */
internal fun cellText(row: FileRow, k: FilesSortKey, dates: DateFormat): String {
    if (row.path == "..") return ""
    return when (k) {
        FilesSortKey.NAME -> row.name
        FilesSortKey.SIZE -> if (row.isDir) "—" else formatBytes(row.size)
        FilesSortKey.CREATED -> row.raw.createdMs?.let { dates.format(Date(it)) } ?: "—"
        FilesSortKey.MODIFIED -> row.raw.modifiedMs?.let { dates.format(Date(it)) } ?: "—"
    }
}

/**
 * The headings over the list, laid out as [layout]: a tap sorts by the
 * column ([onSort]: FilesExplorerViewModel.pickSort, which says the new
 * order to TalkBack), and the handle at each one's end edge but the last
 * resizes it.
 */
@Composable
internal fun FilesColumnHeadings(
    layout: FilesColumns.Layout, metrics: FilesColumns.Metrics, sort: FilesSortOrder, state: FilesColumnsState,
    onSort: (FilesSortKey) -> Unit,
) {
    val colors = MaterialTheme.colorScheme
    Row(
        Modifier.fillMaxWidth().height(HEADING_HEIGHT).background(colors.surfaceContainerLow).padding(start = NAME_START, end = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        for (k in layout.shown) {
            val last = k == layout.shown.last()
            val label = columnLabel(k).resolve()
            Box(Modifier.columnWidth(this, k, layout).fillMaxHeight()) {
                SortHeading(k, label, sort, Modifier.fillMaxSize().padding(end = if (last) 0.dp else HANDLE_WIDTH), onSort)
                if (!last) ResizeHandle(k, label, layout, metrics, state, Modifier.align(Alignment.CenterEnd))
            }
        }
    }
    HorizontalDivider(color = colors.outlineVariant)
}

/** [k]'s width in [layout], in [row]: Name takes what the others leave. */
private fun Modifier.columnWidth(row: RowScope, k: FilesSortKey, layout: FilesColumns.Layout): Modifier =
    if (k == FilesSortKey.NAME) with(row) { this@columnWidth.weight(1f) } else width(layout.width.getValue(k).dp)

/** A heading: its label, and the order's arrow on the column the list is sorted by (its direction said to TalkBack). */
@Composable
private fun SortHeading(k: FilesSortKey, label: String, sort: FilesSortOrder, modifier: Modifier, onSort: (FilesSortKey) -> Unit) {
    val colors = MaterialTheme.colorScheme
    val on = sort.key == k
    val direction = if (on) FilesSortText.direction(sort).resolve() else null
    Row(
        modifier.clickable(role = Role.Button) { onSort(k) }
            .semantics {
                selected = on
                if (direction != null) stateDescription = direction
            }
            .padding(start = if (k == FilesSortKey.NAME) 0.dp else CELL_PAD, end = 2.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            label, style = MaterialTheme.typography.labelLarge, fontWeight = if (on) FontWeight.SemiBold else FontWeight.Medium,
            color = if (on) colors.onSurface else colors.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f, fill = false),
        )
        if (on) Icon(if (sort.ascending) Icons.Default.KeyboardArrowUp else Icons.Default.KeyboardArrowDown, null, Modifier.size(18.dp), tint = colors.primary)
    }
}

/**
 * The handle at [col]'s end edge: a drag moves the edge (FilesColumns.resize,
 * from the layout as the drag began, so going back undoes it - right to
 * left, the edge is on the left and a drag that way widens), the widths
 * kept when the finger lifts; a double tap puts the column back (Name's:
 * every column). TalkBack: "Resize the Size column", with Wider, Narrower
 * (FilesColumns.STEP) and Reset width.
 */
@Composable
private fun ResizeHandle(
    col: FilesSortKey, label: String, layout: FilesColumns.Layout, metrics: FilesColumns.Metrics, state: FilesColumnsState, modifier: Modifier,
) {
    val colors = MaterialTheme.colorScheme
    val current by rememberUpdatedState(layout)
    val m by rememberUpdatedState(metrics)
    val rtl = LocalLayoutDirection.current == LayoutDirection.Rtl
    val active = state.draggedColumn == col
    val name = S.appFilesColumnResize(label).resolve()
    val wider = S.appFilesColumnWider().resolve()
    val narrower = S.appFilesColumnNarrower().resolve()
    val reset = S.appFilesColumnReset().resolve()
    fun moveBy(dp: Int): Boolean {
        val before = current
        val after = FilesColumns.resize(before, m, col, dp.toFloat())
        if (after == before) return false
        state.keep(FilesColumns.widthsAfter(state.given, before, after))
        return true
    }
    Box(
        modifier.width(HANDLE_WIDTH).fillMaxHeight()
            .pointerInput(col) { detectTapGestures(onDoubleTap = { state.keep(FilesColumns.reset(state.given, col)) }) }
            .pointerInput(col, rtl) {
                var start: FilesColumns.Layout? = null
                var moved = 0f
                fun end(keep: Boolean) {
                    val from = start
                    val to = state.dragged
                    if (keep && from != null && to != null) state.keep(FilesColumns.widthsAfter(state.given, from, to))
                    state.dragged = null
                    state.draggedColumn = null
                    start = null
                }
                detectHorizontalDragGestures(
                    onDragStart = {
                        start = current
                        moved = 0f
                        state.draggedColumn = col
                        state.dragged = current
                    },
                    onDragEnd = { end(keep = true) },
                    onDragCancel = { end(keep = false) },
                ) { change, dx ->
                    val from = start ?: return@detectHorizontalDragGestures
                    change.consume()
                    moved += (if (rtl) -dx else dx).toDp().value
                    state.dragged = FilesColumns.resize(from, m, col, moved)
                }
            }
            .semantics {
                contentDescription = name
                customActions = listOf(
                    CustomAccessibilityAction(wider) { moveBy(FilesColumns.STEP) },
                    CustomAccessibilityAction(narrower) { moveBy(-FilesColumns.STEP) },
                    CustomAccessibilityAction(reset) { state.keep(FilesColumns.reset(state.given, col)); true },
                )
            },
        contentAlignment = Alignment.CenterEnd,
    ) {
        Box(Modifier.width(if (active) 2.dp else 1.dp).fillMaxHeight(if (active) 0.8f else 0.5f).background(if (active) colors.primary else colors.outlineVariant))
    }
}

/** A row's Size, Created and Modified cells, as [layout] has them. */
@Composable
internal fun RowScope.FilesColumnCells(row: FileRow, layout: FilesColumns.Layout, dates: DateFormat) {
    val colors = MaterialTheme.colorScheme
    for (k in layout.fixed) {
        Text(
            cellText(row, k, dates), Modifier.columnWidth(this, k, layout).padding(horizontal = CELL_PAD),
            style = MaterialTheme.typography.bodyMedium, color = colors.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis,
        )
    }
}
