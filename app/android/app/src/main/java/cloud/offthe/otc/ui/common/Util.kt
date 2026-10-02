// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import android.graphics.Bitmap
import android.graphics.BitmapFactory
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

/** "3m ago" / "2h ago" / "5d ago", then the date - the feed's own convention. */
fun relativeTime(epochMs: Long): String {
    val mins = ((System.currentTimeMillis() - epochMs) / 60_000).toInt()
    if (mins < 1) return "just now"
    if (mins < 60) return "${mins}m ago"
    val hours = mins / 60
    if (hours < 24) return "${hours}h ago"
    val days = hours / 24
    if (days < 7) return "${days}d ago"
    return DateFormat.getDateInstance(DateFormat.MEDIUM).format(Date(epochMs))
}

fun formatBytes(bytes: Long): String = when {
    bytes >= 1L shl 30 -> String.format("%.1f GB", bytes / (1024.0 * 1024 * 1024))
    bytes >= 1L shl 20 -> String.format("%.1f MB", bytes / (1024.0 * 1024))
    bytes >= 1L shl 10 -> String.format("%.0f KB", bytes / 1024.0)
    else -> "$bytes B"
}
