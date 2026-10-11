// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import androidx.compose.runtime.Composable
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.Dp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appShellTimeDaysAgo
import cloud.offthe.otc.i18n.appShellTimeHoursAgo
import cloud.offthe.otc.i18n.appShellTimeJustNow
import cloud.offthe.otc.i18n.appShellTimeMinutesAgo
import java.text.DateFormat
import java.util.Date

/**
 * Decodes an image, scaled down by a power of two until its longest side is
 * at most [maxSide] px. A full-size original decoded as-is can pass what a
 * Canvas will draw (about 100 MB): a 45-megapixel photo opened in the viewer
 * took the app down with "trying to draw too large bitmap". 4096 px is still
 * sharper than any phone screen, and at most 64 MB.
 */
fun decodeBitmap(bytes: ByteArray, maxSide: Int = 4096): Bitmap? = try {
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
    var sample = 1
    while (maxOf(bounds.outWidth, bounds.outHeight) / sample > maxSide) sample *= 2
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size, BitmapFactory.Options().apply { inSampleSize = sample })
} catch (e: Exception) { null } catch (e: OutOfMemoryError) { null }

/**
 * Decodes a thumbnail for a square tile [minSidePx] wide: scaled down by
 * powers of two while its shorter side stays at least that, so it still
 * fills the tile when cropped. decodeBitmap's 4096 px bound kept a 1000 px
 * thumbnail whole - several times the pixels a tile shows.
 */
fun decodeThumbnail(bytes: ByteArray, minSidePx: Int): Bitmap? = try {
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
    val short = minOf(bounds.outWidth, bounds.outHeight)
    var sample = 1
    while (minSidePx > 0 && short / (sample * 2) >= minSidePx) sample *= 2
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size, BitmapFactory.Options().apply { inSampleSize = sample })
} catch (e: Exception) { null } catch (e: OutOfMemoryError) { null }

/** An image's width / height from its header alone; null when it can't be read. */
fun imageAspect(bytes: ByteArray): Float? {
    val o = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    try { BitmapFactory.decodeByteArray(bytes, 0, bytes.size, o) } catch (e: Exception) { return null }
    return if (o.outWidth > 0 && o.outHeight > 0) o.outWidth.toFloat() / o.outHeight else null
}

/**
 * A grid cell's side in px, as LazyVerticalGrid lays it out in [widthPx]
 * with [padding] on each side and [spacing] between: [count] columns, or
 * as many as fit at [minSize] (GridCells.Adaptive's rule). Rounded up.
 */
fun gridCellPx(widthPx: Int, density: Density, padding: Dp, spacing: Dp, minSize: Dp? = null, count: Int? = null): Int = with(density) {
    val avail = widthPx - 2 * padding.roundToPx()
    val s = spacing.roundToPx()
    val n = count ?: maxOf((avail + s) / (minSize!!.roundToPx() + s), 1)
    maxOf((avail - s * (n - 1) + n - 1) / n, 1)
}

/** "3m ago" / "2h ago" / "5d ago", then the date - the feed's own convention - in the language shown. */
@Composable
@ReadOnlyComposable
fun relativeTime(epochMs: Long): String = relativeTimeText(epochMs).resolve()

/** [relativeTime] as text resolved where it is shown, for a view model; the date in the default locale's own format. */
fun relativeTimeText(epochMs: Long): UiText {
    val mins = ((System.currentTimeMillis() - epochMs) / 60_000).toInt()
    if (mins < 1) return S.appShellTimeJustNow()
    if (mins < 60) return S.appShellTimeMinutesAgo(mins)
    val hours = mins / 60
    if (hours < 24) return S.appShellTimeHoursAgo(hours)
    val days = hours / 24
    if (days < 7) return S.appShellTimeDaysAgo(days)
    return UiText.Raw(DateFormat.getDateInstance(DateFormat.MEDIUM).format(Date(epochMs)))
}

fun formatBytes(bytes: Long): String = when {
    bytes >= 1L shl 30 -> String.format("%.1f GB", bytes / (1024.0 * 1024 * 1024))
    bytes >= 1L shl 20 -> String.format("%.1f MB", bytes / (1024.0 * 1024))
    bytes >= 1L shl 10 -> String.format("%.0f KB", bytes / 1024.0)
    else -> "$bytes B"
}
