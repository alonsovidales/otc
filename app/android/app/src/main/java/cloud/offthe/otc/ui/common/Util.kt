// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import java.text.DateFormat
import java.util.Date

fun decodeBitmap(bytes: ByteArray): Bitmap? = try {
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size)
} catch (e: Exception) { null }

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
