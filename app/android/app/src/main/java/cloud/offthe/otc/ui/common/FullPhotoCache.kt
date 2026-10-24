// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import java.io.File
import java.io.FileOutputStream
import java.util.concurrent.atomic.AtomicLong

/**
 * Where the viewer's full-size photos are kept for the session (ThumbStore,
 * by its FullPhotoCache; tests: a cache of their own). The viewer looks a
 * photo up by its content hash before it sends GetFile, and keeps what
 * GetFile brought.
 */
interface FullPhotoStore {
    /** [hash]'s full-size photo as the device sent it, now the most recently used; null when not kept. */
    suspend fun loadFullPhoto(hash: String): ByteArray?

    /** The scope a GetFile is asked under, for [keepFullPhoto]. */
    suspend fun fullPhotoScope(): Long

    /** Keeps [bytes], a GetFile answer's, as [hash]'s - unless asked of another device or before a Log Out ([scope]). */
    suspend fun keepFullPhoto(hash: String, bytes: ByteArray, scope: Long)

    /** Forgets [hash]'s (its bytes didn't decode): the next look-up asks the device. */
    suspend fun forgetFullPhoto(hash: String)
}

/**
 * The viewer's full-size photos on this phone for the rest of the session,
 * by content hash: going back to a photo, or opening the viewer again,
 * shows it full size at once instead of the low-res tile while GetFile asks
 * the device again (a user's complaint). The viewer keeps the last few
 * decoded only while it is open (PhotoGalleryViewModel's hiResImages); this
 * keeps the bytes behind them. As the iOS app's FullPhotoCache.swift.
 *
 * Each entry is a GetFile answer's bytes exactly as the device sent them
 * (a HEIC comes converted to JPEG, so nothing here knows a type), in
 * `<hash>` in [dir]. Together they take at most [budgetBytes] - a tenth of
 * the size picked in Settings > Thumbnail cache, the thumbnails the rest
 * (fullPhotoBytes) - and one at most a quarter of that: a bigger photo is
 * shown, not kept, so one panorama can't push out everything else. After a
 * store, the least recently used go until the rest fit; a hit counts as a
 * use. Pure JVM (unit tested), thread safe; ThumbStoreCore runs it on
 * Dispatchers.IO.
 *
 * Session only, so the originals don't stay on the phone from one launch to
 * the next: [dir] is emptied once a process, before anything else is done
 * here ([start]), and again by Settings' Clear and Log Out ([clear]). That
 * is also why there is no journal, unlike ThumbDiskCache: what [dir] holds
 * after the start is what this process wrote, so the index in memory is
 * complete. A file gone or of another size than written (the system
 * clearing cache space) is dropped as a miss.
 */
class FullPhotoCache(val dir: File, budgetBytes: Long) {
    private class Node(val size: Long)

    // Every entry by hash, least recently used first (access order: a get
    // moves an entry to the end).
    private val index = LinkedHashMap<String, Node>(16, 0.75f, true)
    private var used = 0L
    private var budget = budgetBytes
    private var started = false
    // Bumped by clear: a store written before it keeps nothing.
    private var epoch = 0
    private val tmpSeq = AtomicLong()

    /** The bytes the entries take. */
    val usedBytes: Long
        @Synchronized get() = used

    val budgetBytes: Long
        @Synchronized get() = budget

    val count: Int
        @Synchronized get() = index.size

    /** Whether [hash] is kept, without reading it or counting it as used (tests). */
    @Synchronized internal fun has(hash: String): Boolean = index.containsKey(hash)

    /** The last session's photos deleted: once a process, at the first use (off the main thread). */
    @Synchronized fun start() {
        if (started) return
        started = true
        dir.deleteRecursively()
    }

    /** [hash]'s bytes, now the most recently used; null when not kept (or no longer whole). */
    fun get(hash: String): ByteArray? {
        if (!ThumbDiskCache.isHash(hash)) return null
        val node = synchronized(this) {
            start()
            index[hash] ?: return null
        }
        val bytes = try { fileOf(hash).readBytes() } catch (_: Exception) { null }
        if (bytes == null || bytes.size.toLong() != node.size) {
            // Gone or not whole: dropped, unless it was kept anew meanwhile.
            synchronized(this) { if (index[hash] === node) drop(hash, node) }
            return null
        }
        return bytes
    }

    /**
     * Keeps [bytes] as [hash]'s, the most recently used, and lets the least
     * recently used go until the rest fit the budget. False when nothing is
     * kept: not a device hash, over a quarter of the budget, a write that
     * failed or a [clear] meanwhile. Kept already, it is counted as used
     * (the same hash is the same content).
     */
    fun put(hash: String, bytes: ByteArray): Boolean {
        if (!ThumbDiskCache.isHash(hash) || bytes.isEmpty()) return false
        val ep = synchronized(this) {
            start()
            if (bytes.size > budget / 4) return false
            if (index[hash] != null) return true
            epoch
        }
        val tmp = File(dir, "$hash.${tmpSeq.incrementAndGet()}$TMP")
        try {
            dir.mkdirs()
            FileOutputStream(tmp).use { it.write(bytes) }
        } catch (_: Exception) {
            tmp.delete()
            return false
        }
        synchronized(this) {
            // Cleared meanwhile, or the budget came down: nothing kept.
            if (ep != epoch || bytes.size > budget / 4) { tmp.delete(); return false }
            // Kept meanwhile by another fetch of the same photo.
            if (index[hash] != null) { tmp.delete(); return true }
            if (!tmp.renameTo(fileOf(hash))) { tmp.delete(); return false }
            val node = Node(bytes.size.toLong())
            index[hash] = node
            used += node.size
            evictToBudget(keep = hash)
            return true
        }
    }

    /** Forgets [hash] (its bytes didn't decode). */
    @Synchronized fun remove(hash: String) {
        index[hash]?.let { drop(hash, it) }
    }

    /** Another budget (another size picked): what is over it, or now over a quarter of it, goes at once. */
    @Synchronized fun setBudget(bytes: Long) {
        budget = bytes
        val it = index.entries.iterator()
        while (it.hasNext()) {
            val (h, n) = it.next()
            if (n.size <= budget / 4) continue
            it.remove()
            used -= n.size
            fileOf(h).delete()
        }
        evictToBudget(keep = null)
    }

    /** Everything goes (Settings' Clear thumbnail cache, Log Out); what is fetched next is kept again. */
    @Synchronized fun clear() {
        started = true
        epoch++
        index.clear()
        used = 0
        dir.deleteRecursively()
    }

    // ---- inside, under the lock ------------------------------------------------------

    private fun fileOf(hash: String) = File(dir, hash)

    private fun drop(hash: String, n: Node) {
        index.remove(hash)
        used -= n.size
        fileOf(hash).delete()
    }

    // Past the budget: the least recently used go until the rest fit, [keep]
    // (the one just stored) aside. Their files are deleted here, under the
    // lock, not in the background as ThumbDiskCache's: a store makes room
    // for one photo, a few files at most, never thousands of tiles.
    private fun evictToBudget(keep: String?) {
        val it = index.entries.iterator()
        while (used > budget && it.hasNext()) {
            val (h, n) = it.next()
            if (h == keep) continue
            it.remove()
            used -= n.size
            fileOf(h).delete()
        }
    }

    companion object {
        /** cacheDir's folder for them. */
        const val DIR = "full-photos"
        private const val TMP = ".tmp"
    }
}
