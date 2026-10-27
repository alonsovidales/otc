// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import android.util.AtomicFile
import cloud.offthe.otc.OTCApp
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream

// Port of AssetSyncCache.swift (issue #58): per MediaStore id, the content
// hash it last synced under, so a reinstall doesn't re-read and re-hash
// every photo just to rediscover the device already has it. Also keeps the
// sync's retry list (PendingList), as iOS's does.
object AssetSyncCache {
    // AtomicFile: a kill mid-write used to leave half a JSON file, which the
    // loader then swapped for an empty map. It reads the plain file older
    // releases wrote as it is.
    private val file by lazy { AtomicFile(File(OTCApp.instance.filesDir, "asset_sync_cache.json")) }
    private val cache: MutableMap<String, String> by lazy {
        try { val o = JSONObject(file.readFully().decodeToString()); o.keys().asSequence().associateWith { o.getString(it) }.toMutableMap() }
        catch (e: Exception) { mutableMapOf() }
    }
    private var dirty = 0
    private const val flushBatchSize = 25
    // Bumped by clear(): a sync that began before Log Out finishes its
    // in-flight uploads after the wipe, and must not write their hashes
    // (or flush the map) into the next device's empty cache.
    private var epoch = 0

    /** A5: the assets a run attempted and couldn't finish (asset_sync_pending.txt). */
    val pending by lazy { PendingList(File(OTCApp.instance.filesDir, "asset_sync_pending.txt")) }

    @Synchronized fun epoch(): Int = epoch

    @Synchronized fun hash(id: String): String? = cache[id]

    /** Nothing synced from this phone yet (a first sync, or after Log Out): the sync lists the device's folder first then (PhotoSync.needsListing). */
    @Synchronized fun isEmpty(): Boolean = cache.isEmpty()

    @Synchronized fun record(id: String, hash: String, since: Int = epoch) {
        if (since != epoch || cache[id] == hash) return
        cache[id] = hash
        // Every write is the whole map: a fixed batch made a first sync of a
        // big library O(n^2) in bytes written. At most ~5% of the entries are
        // unwritten if the process dies (re-hashed only after a reinstall);
        // the sync's end flushes the rest.
        if (++dirty >= maxOf(flushBatchSize, cache.size / 20)) flushLocked()
    }

    @Synchronized fun flush(since: Int = epoch) { if (since == epoch) flushLocked() }

    /** Log Out: another device knows none of these hashes, nor what failed for it. */
    @Synchronized fun clear() {
        epoch++
        cache.clear()
        dirty = 0
        file.delete()
        pending.clear()
    }

    private fun flushLocked() {
        if (dirty == 0) return
        var out: FileOutputStream? = null
        try {
            val bytes = JSONObject(cache as Map<*, *>).toString().toByteArray()
            out = file.startWrite()
            out.write(bytes)
            file.finishWrite(out)
        } catch (_: Exception) {
            out?.let { file.failWrite(it) }
        }
        dirty = 0
    }
}

/**
 * The photo sync's retry list (A5), as iOS's asset_sync_pending.log: the
 * assets a run attempted and couldn't finish, by MediaStore id, tried again
 * by later runs although the watermark has moved past them. The watermark
 * used to hold before the first failed upload, so one asset that kept
 * failing - a large video that never finished in a background run's time -
 * held every newer photo back. One id a line, rewritten whole on a change
 * (it stays short) and swapped in by a rename, so a kill mid-write leaves
 * the last list; not backed up (filesDir, only otc_settings is), wiped at
 * Log Out.
 */
class PendingList(private val file: File) {
    private var ids: MutableSet<Long>? = null

    private fun load(): MutableSet<Long> = ids ?: try {
        file.readLines().mapNotNullTo(LinkedHashSet()) { it.trim().toLongOrNull() }
    } catch (e: Exception) {
        LinkedHashSet<Long>()
    }.also { ids = it }

    @Synchronized fun all(): Set<Long> = load().toSet()

    /** Failed this run: tried again by the next. */
    @Synchronized fun mark(failed: Collection<Long>) {
        val s = load()
        if (s.addAll(failed)) save(s)
    }

    /** Done, given up on, or gone from the phone. */
    @Synchronized fun resolve(done: Collection<Long>) {
        val s = load()
        if (s.removeAll(done.toSet())) save(s)
    }

    /** Sync From Now (everything already on the phone is skipped) and Log Out. */
    @Synchronized fun clear() {
        ids = LinkedHashSet()
        file.delete()
    }

    private fun save(s: Set<Long>) {
        if (s.isEmpty()) { file.delete(); return }
        val tmp = File(file.absoluteFile.parentFile, file.name + ".tmp")
        try {
            file.absoluteFile.parentFile?.mkdirs()
            tmp.writeText(s.joinToString("\n", postfix = "\n"))
            if (!tmp.renameTo(file)) tmp.delete()
        } catch (_: Exception) {
            tmp.delete()
        }
    }
}
