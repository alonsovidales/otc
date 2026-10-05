// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import android.util.AtomicFile
import cloud.offthe.otc.OTCApp
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream

// Port of AssetSyncCache.swift (issue #58): per MediaStore id, the content
// hash it last synced under, so a reinstall doesn't re-read and re-hash
// every photo just to rediscover the device already has it.
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

    @Synchronized fun hash(id: String): String? = cache[id]

    @Synchronized fun record(id: String, hash: String) {
        if (cache[id] == hash) return
        cache[id] = hash
        // Every write is the whole map: a fixed batch made a first sync of a
        // big library O(n^2) in bytes written. At most ~5% of the entries are
        // unwritten if the process dies (re-hashed only after a reinstall);
        // the sync's end flushes the rest.
        if (++dirty >= maxOf(flushBatchSize, cache.size / 20)) flushLocked()
    }

    @Synchronized fun flush() = flushLocked()

    /** Log Out: another device knows none of these hashes. */
    @Synchronized fun clear() {
        cache.clear()
        dirty = 0
        file.delete()
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
