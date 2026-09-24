// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import cloud.offthe.otc.OTCApp
import org.json.JSONObject
import java.io.File

// Port of AssetSyncCache.swift (issue #58): per MediaStore id, the content
// hash it last synced under, so a reinstall doesn't re-read and re-hash
// every photo just to rediscover the device already has it.
object AssetSyncCache {
    private val file by lazy { File(OTCApp.instance.filesDir, "asset_sync_cache.json") }
    private val cache: MutableMap<String, String> by lazy {
        try { val o = JSONObject(file.readText()); o.keys().asSequence().associateWith { o.getString(it) }.toMutableMap() }
        catch (e: Exception) { mutableMapOf() }
    }
    private var dirty = 0
    private const val flushBatchSize = 25

    @Synchronized fun hash(id: String): String? = cache[id]

    @Synchronized fun record(id: String, hash: String) {
        if (cache[id] == hash) return
        cache[id] = hash
        if (++dirty >= flushBatchSize) flushLocked()
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
        try { file.writeText(JSONObject(cache as Map<*, *>).toString()) } catch (_: Exception) {}
        dirty = 0
    }
}
