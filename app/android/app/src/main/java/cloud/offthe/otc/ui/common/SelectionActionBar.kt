// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Download
import androidx.compose.material.icons.filled.HideImage
import androidx.compose.material.icons.filled.Image
import androidx.compose.material.icons.filled.PhotoLibrary
import androidx.compose.material.icons.filled.Share
import androidx.compose.material.icons.filled.Upload
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.ripple
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.zIndex
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.ui.TourTarget
import cloud.offthe.otc.ui.tourTarget
import cloud.offthe.otc.i18n.appShellAddToCollection
import cloud.offthe.otc.i18n.appShellClearSelection
import cloud.offthe.otc.i18n.appShellKeepOutOfImages
import cloud.offthe.otc.i18n.appShellRemoveFromCollection
import cloud.offthe.otc.i18n.appShellSelected
import cloud.offthe.otc.i18n.appShellShareAsGallery
import cloud.offthe.otc.i18n.appShellShowInImages
import cloud.offthe.otc.i18n.commonDelete
import cloud.offthe.otc.i18n.commonDownload
import cloud.offthe.otc.i18n.commonShare
import cloud.offthe.otc.i18n.commonUpload

/** Which action is waiting on the device, if any. */
enum class SelectionActionTask { SHARE, DOWNLOAD, OUT_OF_IMAGES, UNCOLLECT }

// One item of the bar, as it is drawn.
private class BarItem(
    val title: String, val icon: ImageVector, val enabled: Boolean, val onClick: () -> Unit,
    val busy: Boolean = false, val tint: Color? = null,
)

// Port of SelectionActionBar.swift: what Images and Files both show once
// something is selected - a floating pill with icon-over-label items.
@Composable
fun SelectionActionBar(
    count: Int,
    busy: SelectionActionTask? = null,
    onShare: () -> Unit,
    onDownload: () -> Unit,
    onDelete: () -> Unit,
    onGroup: (() -> Unit)? = null,
    // Remove from collection: the picked photos out of the collection open
    // in Images - only there.
    onUncollect: (() -> Unit)? = null,
    onUpload: (() -> Unit)? = null,
    // Issue #180: "Share as gallery", offered when the selection is one folder.
    onGallery: (() -> Unit)? = null,
    // Issue #192: keep that one folder out of Images, or show it there
    // again ([keptOutOfImages]: it is kept out now) - Files, on a device
    // that can.
    onOutOfImages: (() -> Unit)? = null,
    keptOutOfImages: Boolean = false,
    // The "N selected" pill's ×: everything unpicked, selection mode left
    // (the screen's own clear, which ends a drag to select too).
    onClear: (() -> Unit)? = null,
) {
    val items = buildList {
        if (onUpload != null) add(BarItem(S.commonUpload().resolve(), Icons.Default.Upload, busy == null, onUpload))
        add(BarItem(S.commonShare().resolve(), Icons.Default.Share, count > 0 && busy == null, onShare, busy = busy == SelectionActionTask.SHARE))
        if (onGallery != null) add(BarItem(S.appShellShareAsGallery().resolve(), Icons.Default.PhotoLibrary, busy == null, onGallery))
        if (onOutOfImages != null) add(BarItem(
            (if (keptOutOfImages) S.appShellShowInImages() else S.appShellKeepOutOfImages()).resolve(),
            if (keptOutOfImages) Icons.Default.Image else Icons.Default.HideImage,
            busy == null, onOutOfImages, busy = busy == SelectionActionTask.OUT_OF_IMAGES,
        ))
        add(BarItem(S.commonDownload().resolve(), Icons.Default.Download, count > 0 && busy == null, onDownload, busy = busy == SelectionActionTask.DOWNLOAD))
        if (onGroup != null) add(BarItem(S.appShellAddToCollection().resolve(), NavIcons.AddToCollection, count > 0 && busy == null, onGroup))
        if (onUncollect != null) add(BarItem(
            S.appShellRemoveFromCollection().resolve(), NavIcons.RemoveFromCollection, count > 0 && busy == null, onUncollect,
            busy = busy == SelectionActionTask.UNCOLLECT,
        ))
        add(BarItem(S.commonDelete().resolve(), Icons.Default.Delete, count > 0 && busy == null, onDelete, tint = Color(0xFFE53935)))
    }
    BoxWithConstraints {
        // Five or six items share a phone's width, and a label word wider
        // than its slot ("Download", "collection") used to break mid-word:
        // every label gets one size, the largest at which each word fits on
        // a line and each label in two (barFit). Where none does - six
        // items at 360 dp in Spanish, German or Dutch ("Compartir como
        // galería", "Herunterladen", "Buiten Afbeeldingen houden") - the bar
        // takes nearly the whole width, and its labels three lines if need be.
        val labelStyle = barLabelStyle()
        val fit = barFit(items.map { it.title }, maxWidth, labelStyle)
        // Its lines as close as the style's: a smaller size in the style's
        // line height left a gap in two-line labels.
        val itemStyle = labelStyle.copy(fontSize = fit.size, lineHeight = labelStyle.lineHeight * (fit.size.value / labelStyle.fontSize.value))
        Column(Modifier.padding(horizontal = fit.inset), horizontalAlignment = Alignment.CenterHorizontally) {
            if (count > 0) {
                // Above the bar: the ×'s touches reach over the gap into
                // the bar's top padding (SelectedPill).
                SelectedPill(count, onClear, Modifier.zIndex(1f))
                Spacer(Modifier.height(6.dp))
            }
            Surface(shape = MaterialTheme.shapes.extraLarge, tonalElevation = 3.dp, shadowElevation = 4.dp) {
                Row(Modifier.fillMaxWidth().padding(vertical = 6.dp, horizontal = fit.side)) {
                    for (item in items) Item(
                        item, itemStyle, fit.lines,
                        // Files' Upload: the tour's step about uploading rings it.
                        Modifier.weight(1f).tourTarget(if (onUpload != null && item.onClick === onUpload) TourTarget.UPLOAD else null),
                    )
                }
            }
        }
    }
}

/**
 * The "N selected" pill, with [onClear]'s × at its start - the way out of
 * selection mode (Google Photos' ×; Back does the same). As small as the
 * count drawn, but the × takes touches over a whole [ClearTarget] square,
 * past the pill's edges: the pill is drawn under the row, not around it,
 * so nothing clips them.
 */
@Composable
private fun SelectedPill(count: Int, onClear: (() -> Unit)?, modifier: Modifier = Modifier) {
    val color = MaterialTheme.colorScheme.onSurfaceVariant
    Box(modifier) {
        Surface(Modifier.matchParentSize(), shape = MaterialTheme.shapes.extraLarge, tonalElevation = 3.dp, shadowElevation = 2.dp) {}
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (onClear != null) {
                val label = S.appShellClearSelection().resolve()
                // It grows with the text (the font size chosen), up to 24 dp.
                val icon = with(LocalDensity.current) { 16.sp.toDp() }.coerceAtMost(24.dp)
                Box(
                    Modifier.padding(start = 6.dp).touchTarget(icon + 4.dp, ClearTarget)
                        .clickable(interactionSource = null, indication = ripple(bounded = false, radius = icon * 0.75f), role = Role.Button, onClick = onClear)
                        .semantics { contentDescription = label },
                    contentAlignment = Alignment.Center,
                ) { Icon(Icons.Default.Close, null, Modifier.size(icon), tint = color) }
            }
            Text(S.appShellSelected(count).resolve(), style = MaterialTheme.typography.labelSmall, color = color,
                modifier = Modifier.padding(start = if (onClear != null) 2.dp else 12.dp, end = 12.dp, top = 5.dp, bottom = 5.dp))
        }
    }
}

// The square the pill's × is touched in.
private val ClearTarget = 48.dp

/**
 * Laid out as a [shown] square, but measured and touched as a [target]
 * square centred on it: a control drawn small that is easy to hit.
 */
private fun Modifier.touchTarget(shown: Dp, target: Dp) = layout { measurable, _ ->
    val t = target.roundToPx()
    val s = shown.roundToPx()
    val placeable = measurable.measure(Constraints.fixed(t, t))
    layout(s, s) { placeable.place((s - t) / 2, (s - t) / 2) }
}

/** How the bar is laid out: its inset from the sides, its own padding, the labels' size and lines. */
private class BarFit(val inset: Dp, val side: Dp, val size: TextUnit, val lines: Int)

private val BarInset = 24.dp
private val BarSidePadding = 8.dp
// Where the labels don't fit otherwise: nearly the whole width.
private val BarTightInset = 8.dp
private val BarTightSidePadding = 4.dp
private val ItemSidePadding = 2.dp

// The label style without its letter spacing, so the labels take less room.
@Composable
private fun barLabelStyle(): TextStyle = MaterialTheme.typography.labelSmall.copy(letterSpacing = 0.sp, textAlign = TextAlign.Center)

/**
 * The bar's layout for [titles] in [width]: inset [BarInset] from the
 * sides, every label at one size - the label style's own, or smaller
 * when a word of one of them wouldn't fit on a line of its slot, or a
 * label in two lines; never below 8sp as drawn at the default font size.
 * When even that doesn't fit, the bar is inset [BarTightInset] only, with
 * less padding of its own, and then, if they need it, its labels take
 * three lines (words wider than that break after all). With the largest font sizes the user's
 * choice gives way to the words staying whole.
 */
@Composable
private fun barFit(titles: List<String>, width: Dp, style: TextStyle): BarFit {
    val measurer = rememberTextMeasurer(cacheSize = 0)
    val density = LocalDensity.current
    return remember(titles, width, style, density) {
        val max = style.fontSize.value
        val min = minOf(max, BAR_LABEL_MIN_SP / density.fontScale)
        fun attempt(inset: Dp, side: Dp, maxLines: Int): BarFit? {
            // A pixel to spare: the row may hand a slot a pixel less.
            val labelWidth = with(density) { ((width - inset * 2 - side * 2) / titles.size - ItemSidePadding * 2).toPx().toInt() - 1 }
            val wordFits = { word: String, size: Float ->
                measurer.measure(word, style.copy(fontSize = size.sp), softWrap = false, maxLines = 1, density = density).size.width <= labelWidth
            }
            val lines = { title: String, size: Float ->
                measurer.measure(title, style.copy(fontSize = size.sp), constraints = Constraints(maxWidth = maxOf(labelWidth, 1)), density = density).lineCount
            }
            val size = sharedLabelSize(titles, max = max, min = min, maxLines = maxLines, wordFits = wordFits, lines = lines)
            return if (labelsFit(titles, size, maxLines, wordFits, lines)) BarFit(inset, side, size.sp, maxLines) else null
        }
        attempt(BarInset, BarSidePadding, 2)
            ?: attempt(BarTightInset, BarTightSidePadding, 2)
            ?: attempt(BarTightInset, BarTightSidePadding, 3)
            ?: BarFit(BarTightInset, BarTightSidePadding, min.sp, 3)
    }
}

/** The smallest a bar label gets, in sp at the default font size. */
internal const val BAR_LABEL_MIN_SP = 8f

/**
 * The largest size, from [max] down to [min] in [step]s, at which every
 * word of [titles] fits on one line ([wordFits]) and every title takes
 * [maxLines] [lines] at most; [min] when none does. One size for them all:
 * labels side by side at different sizes look like a mistake.
 */
internal fun sharedLabelSize(
    titles: List<String>, max: Float, min: Float, step: Float = 0.5f, maxLines: Int = 2,
    wordFits: (word: String, size: Float) -> Boolean, lines: (title: String, size: Float) -> Int,
): Float {
    var size = max
    while (size > min) {
        if (labelsFit(titles, size, maxLines, wordFits, lines)) return size
        size = maxOf(min, size - step)
    }
    return min
}

/** Whether, at [size], every word of [titles] fits on a line and every title in [maxLines]. */
internal fun labelsFit(
    titles: List<String>, size: Float, maxLines: Int,
    wordFits: (word: String, size: Float) -> Boolean, lines: (title: String, size: Float) -> Int,
): Boolean {
    val words = titles.flatMap { it.split(' ') }.filter { it.isNotEmpty() }.distinct()
    return words.all { wordFits(it, size) } && titles.all { lines(it, size) <= maxLines }
}

@Composable
private fun Item(item: BarItem, labelStyle: TextStyle, lines: Int, modifier: Modifier = Modifier) {
    val tint = item.tint ?: MaterialTheme.colorScheme.primary
    val color = if (item.enabled) tint else tint.copy(alpha = 0.4f)
    // Inside TextButton's own 12dp sides there was no room for a word: the
    // label gets nearly the whole slot (barFit measures that). Not
    // the button's pill shape either: it clips what it holds, and its
    // round ends cut off the last letter of a label as wide as the slot.
    TextButton(onClick = item.onClick, enabled = item.enabled, modifier = modifier, shape = RoundedCornerShape(8.dp),
        contentPadding = PaddingValues(horizontal = ItemSidePadding, vertical = 8.dp)) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            Box(Modifier.height(22.dp), contentAlignment = Alignment.Center) {
                if (item.busy) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                else Icon(item.icon, contentDescription = item.title, tint = color)
            }
            Text(item.title, style = labelStyle, color = color, maxLines = lines, overflow = TextOverflow.Ellipsis, modifier = Modifier.fillMaxWidth())
        }
    }
}
