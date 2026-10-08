// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

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
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/** Which action is waiting on the device, if any. */
enum class SelectionActionTask { SHARE, DOWNLOAD, OUT_OF_IMAGES }

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
    onUpload: (() -> Unit)? = null,
    // Issue #180: "Share as gallery", offered when the selection is one folder.
    onGallery: (() -> Unit)? = null,
    // Issue #192: keep that one folder out of Images, or show it there
    // again ([keptOutOfImages]: it is kept out now) - Files, on a device
    // that can.
    onOutOfImages: (() -> Unit)? = null,
    keptOutOfImages: Boolean = false,
) {
    val items = buildList {
        if (onUpload != null) add(BarItem("Upload", Icons.Default.Upload, busy == null, onUpload))
        add(BarItem("Share", Icons.Default.Share, count > 0 && busy == null, onShare, busy = busy == SelectionActionTask.SHARE))
        if (onGallery != null) add(BarItem("Share as gallery", Icons.Default.PhotoLibrary, busy == null, onGallery))
        if (onOutOfImages != null) add(BarItem(
            if (keptOutOfImages) "Show in Images" else "Keep out of Images",
            if (keptOutOfImages) Icons.Default.Image else Icons.Default.HideImage,
            busy == null, onOutOfImages, busy = busy == SelectionActionTask.OUT_OF_IMAGES,
        ))
        add(BarItem("Download", Icons.Default.Download, count > 0 && busy == null, onDownload, busy = busy == SelectionActionTask.DOWNLOAD))
        if (onGroup != null) add(BarItem("Add to collection", NavIcons.AddToCollection, count > 0 && busy == null, onGroup))
        add(BarItem("Delete", Icons.Default.Delete, count > 0 && busy == null, onDelete, tint = Color(0xFFE53935)))
    }
    Column(Modifier.padding(horizontal = 24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        if (count > 0) {
            Surface(shape = MaterialTheme.shapes.extraLarge, tonalElevation = 3.dp, shadowElevation = 2.dp) {
                Text("$count selected", style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 12.dp, vertical = 5.dp))
            }
            Spacer(Modifier.height(6.dp))
        }
        Surface(shape = MaterialTheme.shapes.extraLarge, tonalElevation = 3.dp, shadowElevation = 4.dp) {
            BoxWithConstraints {
                // Five or six items share a phone's width, and a label word
                // wider than its slot ("Download", "collection") used to
                // break mid-word: every label gets one size, the largest at
                // which each word fits on a line and each label in two.
                // (A pixel to spare: the row may hand a slot a pixel less.)
                val labelStyle = barLabelStyle()
                val labelWidth = with(LocalDensity.current) { ((maxWidth - BarSidePadding * 2) / items.size - ItemSidePadding * 2).toPx().toInt() - 1 }
                val labelSize = barLabelSize(items.map { it.title }, labelWidth, labelStyle)
                // Its lines as close as the style's: a smaller size in the
                // style's line height left a gap in two-line labels.
                val itemStyle = labelStyle.copy(fontSize = labelSize, lineHeight = labelStyle.lineHeight * (labelSize.value / labelStyle.fontSize.value))
                Row(Modifier.padding(vertical = 6.dp, horizontal = BarSidePadding)) {
                    for (item in items) Item(item, itemStyle, Modifier.weight(1f))
                }
            }
        }
    }
}

private val BarSidePadding = 8.dp
private val ItemSidePadding = 2.dp

// The label style without its letter spacing, so the labels take less room.
@Composable
private fun barLabelStyle(): TextStyle = MaterialTheme.typography.labelSmall.copy(letterSpacing = 0.sp, textAlign = TextAlign.Center)

/**
 * The size every label of the bar is drawn at: the label style's own, or
 * smaller when a word of one of [titles] wouldn't fit on a line of [width]
 * pixels, or a label in two lines. Never below 8sp as drawn at the
 * default font size (with the largest font sizes the user's choice gives
 * way to the words staying whole); words wider than that break after all.
 */
@Composable
private fun barLabelSize(titles: List<String>, width: Int, style: TextStyle): TextUnit {
    val measurer = rememberTextMeasurer(cacheSize = 0)
    val density = LocalDensity.current
    return remember(titles, width, style, density) {
        val max = style.fontSize.value
        sharedLabelSize(
            titles, max = max, min = minOf(max, BAR_LABEL_MIN_SP / density.fontScale),
            wordFits = { word, size ->
                measurer.measure(word, style.copy(fontSize = size.sp), softWrap = false, maxLines = 1, density = density).size.width <= width
            },
            lines = { title, size ->
                measurer.measure(title, style.copy(fontSize = size.sp), constraints = Constraints(maxWidth = maxOf(width, 1)), density = density).lineCount
            },
        ).sp
    }
}

/** The smallest a bar label gets, in sp at the default font size. */
internal const val BAR_LABEL_MIN_SP = 8f

/**
 * The largest size, from [max] down to [min] in [step]s, at which every
 * word of [titles] fits on one line ([wordFits]) and every title takes two
 * [lines] at most; [min] when none does. One size for them all: labels
 * side by side at different sizes look like a mistake.
 */
internal fun sharedLabelSize(
    titles: List<String>, max: Float, min: Float, step: Float = 0.5f,
    wordFits: (word: String, size: Float) -> Boolean, lines: (title: String, size: Float) -> Int,
): Float {
    val words = titles.flatMap { it.split(' ') }.filter { it.isNotEmpty() }.distinct()
    var size = max
    while (size > min) {
        if (words.all { wordFits(it, size) } && titles.all { lines(it, size) <= 2 }) return size
        size = maxOf(min, size - step)
    }
    return min
}

@Composable
private fun Item(item: BarItem, labelStyle: TextStyle, modifier: Modifier = Modifier) {
    val tint = item.tint ?: MaterialTheme.colorScheme.primary
    val color = if (item.enabled) tint else tint.copy(alpha = 0.4f)
    // Inside TextButton's own 12dp sides there was no room for a word: the
    // label gets nearly the whole slot (barLabelSize measures that). Not
    // the button's pill shape either: it clips what it holds, and its
    // round ends cut off the last letter of a label as wide as the slot.
    TextButton(onClick = item.onClick, enabled = item.enabled, modifier = modifier, shape = RoundedCornerShape(8.dp),
        contentPadding = PaddingValues(horizontal = ItemSidePadding, vertical = 8.dp)) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            Box(Modifier.height(22.dp), contentAlignment = Alignment.Center) {
                if (item.busy) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                else Icon(item.icon, contentDescription = item.title, tint = color)
            }
            Text(item.title, style = labelStyle, color = color, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.fillMaxWidth())
        }
    }
}
