// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import java.io.File
import java.math.RoundingMode
import java.text.NumberFormat
import java.util.Locale

/** Settings > Thumbnail cache: the sizes offered (in decimal units, as shown: 1 GB is 10^9 bytes). */
val THUMB_CACHE_CHOICES: List<Long> = listOf(250_000_000L, 500_000_000L, 1_000_000_000L, 2_000_000_000L, 5_000_000_000L)

/** The size until one is picked. */
const val THUMB_CACHE_DEFAULT = 1_000_000_000L

/**
 * The thumbnail cache's size limit picked on this phone, kept in [file]
 * (in filesDir: Log Out wipes it with the other settings, and the system
 * never clears it to make room as it may the cache itself). Anything but
 * one of [THUMB_CACHE_CHOICES] reads as the default.
 */
class ThumbCacheLimit(private val file: File) {
    fun get(): Long {
        val v = try { file.readText().trim().toLongOrNull() } catch (_: Exception) { null }
        return v?.takeIf { it in THUMB_CACHE_CHOICES } ?: THUMB_CACHE_DEFAULT
    }

    /** Kept whole or not at all; false when it couldn't be written. */
    fun set(bytes: Long): Boolean {
        require(bytes in THUMB_CACHE_CHOICES) { "not a thumbnail cache size: $bytes" }
        val tmp = File(file.absoluteFile.parentFile, "${file.name}.tmp")
        return try {
            file.absoluteFile.parentFile?.mkdirs()
            tmp.writeText(bytes.toString())
            tmp.renameTo(file).also { if (!it) tmp.delete() }
        } catch (_: Exception) {
            tmp.delete()
            false
        }
    }
}

/**
 * A size as Settings says it ("Using 312 MB of 1 GB"), as the iOS app says
 * it: decimal units as the sizes offered (1 GB is 10^9 bytes), "bytes"
 * below a kilobyte ("Using 0 bytes of 1 GB" for an empty cache, not "0 B"),
 * one decimal below 10 of a unit ("4.5 MB", "1.5 GB") and whole numbers from
 * there, with [locale]'s decimal separator ("1,5 GB").
 */
fun formatCacheBytes(bytes: Long, locale: Locale = Locale.getDefault()): String {
    val b = maxOf(bytes, 0L)
    if (b < 1_000L) return if (b == 1L) "1 byte" else "$b bytes"
    val units = arrayOf("KB", "MB", "GB", "TB")
    var v = b / 1_000.0
    var i = 0
    while (v >= 999.5 && i < units.lastIndex) { v /= 1_000.0; i++ }
    val nf = NumberFormat.getNumberInstance(locale).apply {
        isGroupingUsed = false
        minimumFractionDigits = 0
        maximumFractionDigits = if (v < 9.95) 1 else 0
        roundingMode = RoundingMode.HALF_UP
    }
    return "${nf.format(v)} ${units[i]}"
}
