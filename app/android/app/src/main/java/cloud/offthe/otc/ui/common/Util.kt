// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import androidx.compose.runtime.Composable
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.produceState
import kotlinx.coroutines.delay
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.Dp
import cloud.offthe.otc.i18n.LanguageSettings
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appShellTimeDaysAgo
import cloud.offthe.otc.i18n.appShellTimeHoursAgo
import cloud.offthe.otc.i18n.appShellTimeJustNow
import cloud.offthe.otc.i18n.appShellTimeMinutesAgo
import java.text.DateFormat
import java.util.Date
import java.util.Locale

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

/**
 * "3m ago" / "2h ago" / "5d ago", then the date - the feed's own convention
 * - in the language shown, reckoned from [nowMs]: a screen passes its
 * rememberMinuteClock(), so what it shows moves on while it stays open.
 */
@Composable
@ReadOnlyComposable
fun relativeTime(epochMs: Long, nowMs: Long = System.currentTimeMillis()): String = relativeTimeText(epochMs, nowMs).resolve()

/** [relativeTime] as text resolved where it is shown, for a view model; the date in the default locale's own format. */
fun relativeTimeText(epochMs: Long, nowMs: Long = System.currentTimeMillis()): UiText {
    val mins = ((nowMs - epochMs) / 60_000).toInt()
    if (mins < 1) return S.appShellTimeJustNow()
    if (mins < 60) return S.appShellTimeMinutesAgo(mins)
    val hours = mins / 60
    if (hours < 24) return S.appShellTimeHoursAgo(hours)
    val days = hours / 24
    if (days < 7) return S.appShellTimeDaysAgo(days)
    return UiText.Raw(DateFormat.getDateInstance(DateFormat.MEDIUM).format(Date(epochMs)))
}

/**
 * Now, in ms - and again at every turn of the minute while the screen
 * shows: what the feed's and Alerts' "3m ago" are reckoned from, so they
 * move on by themselves (they stood still until something else redrew).
 * As iOS's TimelineView(.everyMinute).
 */
@Composable
fun rememberMinuteClock(): Long {
    val now by produceState(System.currentTimeMillis()) {
        while (true) {
            delay(msToNextMinute(System.currentTimeMillis()))
            value = System.currentTimeMillis()
        }
    }
    return now
}

/** From [nowMs] to the next turn of the minute; never under a second, so a clock a hair early doesn't tick twice. */
fun msToNextMinute(nowMs: Long): Long = (60_000L - Math.floorMod(nowMs, 60_000L)).coerceAtLeast(1_000L)

/** A file's size: "4.6 MB", "820 KB" ("4,6 MB" in Spanish, decimalText). */
fun formatBytes(bytes: Long): String = when {
    bytes >= 1L shl 30 -> decimalText("%.1f GB", bytes / (1024.0 * 1024 * 1024)) // i18n-ignore: a size, the unit's symbol in every language
    bytes >= 1L shl 20 -> decimalText("%.1f MB", bytes / (1024.0 * 1024)) // i18n-ignore: a size, the unit's symbol in every language
    bytes >= 1L shl 10 -> decimalText("%.0f KB", bytes / 1024.0) // i18n-ignore: a size, the unit's symbol in every language
    else -> "$bytes B"
}

/**
 * A number the app writes by hand ("%.1f"), with the decimal mark of the
 * app's language: "177,7 KB" in Spanish or German. The language's own tag,
 * not the region's (LanguageSettings.numberLocale), so English stays
 * "177.7 KB" on every phone - as iOS's decimalText.
 */
fun decimalText(format: String, value: Double, locale: Locale = LanguageSettings.numberLocale): String = String.format(locale, format, value)
