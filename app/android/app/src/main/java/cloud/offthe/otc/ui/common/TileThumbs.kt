// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import android.graphics.Bitmap
import android.util.LruCache
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * [compute]d off the composition, once per [key]; [cached] when it is
 * already at hand, so the first frame shows it. Decoding in remember {}
 * ran on the main thread, 10-30 ms a tile on a slow phone: dropped frames
 * on every row of a fling.
 */
@Composable
fun <T : Any> rememberOffMain(key: Any?, cached: () -> T?, compute: suspend () -> T?): T? {
    val state = remember(key) { mutableStateOf(cached()) }
    LaunchedEffect(key) { if (state.value == null) state.value = compute() }
    return state.value
}

/** Grid tiles already decoded, by item and size (the composer's phone items by id), bounded by bytes. */
object ThumbCache {
    private val bitmaps = object : LruCache<String, Bitmap>(32 shl 20) {
        override fun sizeOf(key: String, value: Bitmap) = value.byteCount
    }
    fun get(key: String): Bitmap? = bitmaps.get(key)
    fun put(key: String, bmp: Bitmap) { bitmaps.put(key, bmp) }
    fun clear() = bitmaps.evictAll()
}

/**
 * A tile's thumbnail, decoded on a background thread to the tile's size
 * ([sidePx]) and cached by [key] (null: no thumbnail), so a tile scrolled
 * back shows on its first frame. [key] must change when the image does
 * (path#hash#size for a file). [bytes]: the encoded thumbnail.
 */
@Composable
fun rememberTileThumb(key: String?, sidePx: Int, bytes: suspend () -> ByteArray?): Bitmap? {
    if (key == null) return null
    val cacheKey = "$key@$sidePx"
    return rememberOffMain(cacheKey, { ThumbCache.get(cacheKey) }) {
        val b = bytes() ?: return@rememberOffMain null
        withContext(Dispatchers.Default) { decodeThumbnail(b, sidePx) }?.also { ThumbCache.put(cacheKey, it) }
    }
}
