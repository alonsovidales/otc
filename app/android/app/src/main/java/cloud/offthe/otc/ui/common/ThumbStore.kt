// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import android.util.LruCache
import cloud.offthe.otc.OTCApp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File
import java.security.MessageDigest

/**
 * The device's grid thumbnails by [tileKey]: the small ones (release 111:
 * 400 px on the shorter side, ~38 KB) that every grid asks for
 * (small_thumbnails) - an older device's big ones (up to 1000 px, ~100 KB)
 * when it ignores the flag. The lists used to keep every page's bytes in
 * their items for the session: scrolling 1,500 photos held ~250 MB of Java
 * heap and ended in an OutOfMemoryError. The most recent stay in memory;
 * the rest wait in cacheDir/thumbs, which this process starts empty (Log
 * Out wipes the cache dir too). Keys carry the hash, so lists can share
 * entries. A big thumbnail asked for on purpose (the viewer's, when the
 * thumbnail stays on screen) is never kept here: the answers carry no mark
 * of which size came, so the two must never share an entry.
 */
object ThumbStore {
    /** A grid tile's entry: the file's path, hash and size, and that it is a tile's (small) thumbnail. */
    fun tileKey(path: String, hash: String, size: Long) = "$path#$hash#$size#small"

    // Made on first use: tileKey needs none of it.
    private val mem by lazy {
        object : LruCache<String, ByteArray>(minOf(Runtime.getRuntime().maxMemory() / 8, 32L shl 20).toInt()) {
            override fun sizeOf(key: String, value: ByteArray) = value.size
        }
    }

    private val dir: File by lazy {
        val cache = OTCApp.instance.cacheDir
        val d = File(cache, "thumbs")
        // An earlier process's: set aside at once, deleted in the background,
        // so this one's files are never caught by the delete.
        d.renameTo(File(cache, "thumbs-old-${System.nanoTime()}"))
        Thread {
            cache.listFiles { f -> f.name.startsWith("thumbs-old-") }?.forEach { it.deleteRecursively() }
        }.start()
        d
    }

    /** Keeps a page's thumbnails; call before the items naming them are shown. */
    suspend fun putAll(entries: List<Pair<String, ByteArray>>) {
        if (entries.isEmpty()) return
        withContext(Dispatchers.IO) {
            val d = dir.apply { mkdirs() }
            for ((key, bytes) in entries) {
                mem.put(key, bytes)
                val f = File(d, fileName(key))
                if (f.exists()) continue
                // Whole or not at all, so load() never reads half a file. A
                // failed write leaves it in memory only: at worst a blank tile.
                var tmp: File? = null
                try {
                    tmp = File.createTempFile("thumb", ".part", d)
                    tmp.writeBytes(bytes)
                    if (tmp.renameTo(f)) tmp = null
                } catch (_: Exception) {
                } finally { tmp?.delete() }
            }
        }
    }

    /** In memory already, or null; cheap enough for the main thread. */
    fun peek(key: String): ByteArray? = mem.get(key)

    suspend fun load(key: String): ByteArray? = mem.get(key) ?: withContext(Dispatchers.IO) {
        try { File(dir, fileName(key)).readBytes() } catch (_: Exception) { null }?.also { mem.put(key, it) }
    }

    /** Log Out: the files go with the cache dir. */
    fun clear() = mem.evictAll()

    private fun fileName(key: String): String =
        MessageDigest.getInstance("SHA-1").digest(key.toByteArray()).joinToString("") { "%02x".format(it) }
}

