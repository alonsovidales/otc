// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.content.ContentResolver
import android.content.Context
import android.graphics.Bitmap
import android.graphics.ImageDecoder
import android.media.MediaMetadataRetriever
import android.net.Uri
import android.provider.OpenableColumns
import android.util.Log
import androidx.core.content.FileProvider
import cloud.offthe.otc.ui.common.TempFiles
import java.io.IOException

/**
 * The setup page's profile picture (issue #178), as the page can open it.
 * The page crops the picture in a canvas, and Android's web view can't
 * decode HEIF/HEIC - what most phone cameras save - so a HEIF picked in the
 * Bluetooth setup left the crop empty without a word (2026-11-05). The app
 * decodes it itself (ImageDecoder, turned as its metadata says) and hands the
 * page a JPEG copy of at most [MAX_SIDE] px a side, under the photo's own
 * name with .jpg; any other picture goes as it was picked. iOS's and the
 * Mac's web views open HEIC themselves.
 */
object SetupPicture {
    /** Far more than the page's crop needs (it sends 320 px). */
    const val MAX_SIDE = 2048

    private val heifTypes = setOf("image/heif", "image/heic", "image/heif-sequence", "image/heic-sequence")
    private val heifExtensions = setOf("heif", "heic", "hif")

    /** Whether the page can't open the picture as it is: a HEIF by its type, or by its name when the type says nothing. */
    fun needsJpeg(mime: String?, name: String?): Boolean {
        val type = mime?.substringBefore(';')?.trim()?.lowercase()
        if (type in heifTypes) return true
        if (type != null && type.startsWith("image/")) return false
        return name?.substringAfterLast('.', "")?.lowercase() in heifExtensions
    }

    /** The copy's name: the picture's own with .jpg. */
    fun jpegName(name: String?): String {
        val base = name?.substringBeforeLast('.')?.trim()?.ifEmpty { null } ?: "photo" // i18n-ignore: a file name
        return "$base.jpg"
    }

    /** The size to decode a [width] x [height] picture at: within [MAX_SIDE], its shape kept. */
    fun decodeSize(width: Int, height: Int): Pair<Int, Int> {
        val side = maxOf(width, height)
        if (side <= MAX_SIDE) return width to height
        return maxOf(1, (width.toLong() * MAX_SIDE / side).toInt()) to maxOf(1, (height.toLong() * MAX_SIDE / side).toInt())
    }

    /**
     * What to hand the page for [uri]: a JPEG copy when it needs one, else
     * [uri] - and [uri] too when no copy can be made, so the page says it
     * can't open it. Reads and writes files: call off the main thread.
     */
    fun forPage(context: Context, uri: Uri): Uri {
        val resolver = context.contentResolver
        val name = displayName(resolver, uri)
        if (!needsJpeg(resolver.getType(uri), name)) return uri
        return try {
            val bitmap = decode(context, uri)
            TempFiles.clear(context.cacheDir, TempFiles.Kind.SETUP)
            val file = TempFiles.named(context.cacheDir, TempFiles.Kind.SETUP, jpegName(name))
            try {
                file.outputStream().use { if (!bitmap.compress(Bitmap.CompressFormat.JPEG, 90, it)) throw IOException("not encoded") } // i18n-ignore: never shown
            } finally {
                bitmap.recycle()
            }
            FileProvider.getUriForFile(context, "cloud.offthe.otc.fileprovider", file)
        } catch (e: Exception) {
            Log.w("SetupPicture", "the picture could not be made a JPEG: ${e.javaClass.simpleName} ${e.message}") // i18n-ignore: the log
            uri
        }
    }

    /**
     * The picture, within [MAX_SIDE]: ImageDecoder, else the media framework's
     * own HEIF reader (MediaMetadataRetriever's primary image), which some
     * builds' ImageDecoder can't use for a picture stored as a grid of tiles.
     */
    private fun decode(context: Context, uri: Uri): Bitmap = try {
        ImageDecoder.decodeBitmap(ImageDecoder.createSource(context.contentResolver, uri)) { decoder, info, _ ->
            val (w, h) = decodeSize(info.size.width, info.size.height)
            if (w != info.size.width || h != info.size.height) decoder.setTargetSize(w, h)
            decoder.allocator = ImageDecoder.ALLOCATOR_SOFTWARE
        }
    } catch (e: Exception) {
        Log.w("SetupPicture", "ImageDecoder could not read the picture: ${e.javaClass.simpleName} ${e.message}") // i18n-ignore: the log
        val full = MediaMetadataRetriever().use { r ->
            r.setDataSource(context, uri)
            r.primaryImage ?: throw IOException("no primary image") // i18n-ignore: never shown
        }
        val (w, h) = decodeSize(full.width, full.height)
        if (w == full.width && h == full.height) full else Bitmap.createScaledBitmap(full, w, h, true).also { full.recycle() }
    }

    private fun displayName(resolver: ContentResolver, uri: Uri): String? = try {
        resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
            if (c.moveToFirst()) c.getString(0) else null
        }
    } catch (_: Exception) {
        null
    }
}
