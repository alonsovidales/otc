// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.withContext
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.Executor
import kotlin.coroutines.CoroutineContext

/** The tiles' bytes in memory, by hash, bounded by their total size, least recently used out first. Thread safe. */
class ByteLru(private val maxBytes: Long) {
    private val map = LinkedHashMap<String, ByteArray>(64, 0.75f, true)
    private var size = 0L

    @Synchronized fun get(key: String): ByteArray? = map[key]

    @Synchronized fun put(key: String, value: ByteArray) {
        map.remove(key)?.let { size -= it.size }
        if (value.size > maxBytes) return
        map[key] = value
        size += value.size
        val it = map.entries.iterator()
        while (size > maxBytes && it.hasNext()) { size -= it.next().value.size; it.remove() }
    }

    @Synchronized fun remove(key: String) { map.remove(key)?.let { size -= it.size } }

    @Synchronized fun clear() { map.clear(); size = 0 }
}

/**
 * ThumbStore's workings, apart from Android (unit tested): the memory LRU
 * in front of the disk cache (ThumbDiskCache) of the device and account
 * signed in to, as the iOS app's ThumbDiskCache.swift.
 *
 * The *scope* is that device and account: a digest of the normalized
 * endpoint signed in to (every OTC user is an instance of its own, at an
 * endpoint of its own), a folder of its own in [cacheRoot]. It is bound
 * only by a successful sign-in ([signedIn]) - or, once per launch, from
 * the endpoint saved then ([savedEndpoint]) - never by an endpoint merely
 * saved: a mistyped Save Connection or leaving the bridge keeps the old
 * cache until the new endpoint signs in. Binding another scope deletes
 * every other scope's folder; [logOut] deletes them all, with the size
 * picked and what each device was seen to do. Every write carries the
 * scope it was asked under ([scope]): one that comes back after the
 * binding changed (an answer from the last device, a fetch retried across
 * a Log Out) is dropped.
 *
 * In [stateDir] (filesDir: kept when the system clears cache space, wiped by
 * Log Out): the size limit (ThumbCacheLimit) and whether this scope's device
 * leaves thumbnails out of its pages (the first page's size, GridThumbs.kt):
 * a page none of whose rows had content says it does, one with content says
 * it doesn't.
 *
 * A thumbnail that doesn't decode ([decodable]: its header, Android's
 * BitmapFactory bounds) counts as none and is never kept; one read from disk
 * that doesn't is dropped (a file zero-filled by a power cut). A big or
 * unknown entry is asked for again (for the small one) at most once a launch
 * - the grids ([claimReasks]) and Files ([claimFilesReasks]) each once - and
 * not at all for [REASK_AFTER_MS] once that answer came big again
 * (ThumbDiskCache.noteStillBig).
 */
class ThumbStoreCore(
    private val cacheRoot: File,
    private val stateDir: File,
    private val savedEndpoint: () -> String,
    private val decodable: (ByteArray) -> Boolean,
    private val onMemoryDropped: () -> Unit = {},
    memoryBytes: Long = 32L shl 20,
    private val io: CoroutineContext = Dispatchers.IO,
    private val background: Executor = ThumbDiskCache.BACKGROUND,
    private val nowMs: () -> Long = System::currentTimeMillis,
    private val version: Int = CACHE_VERSION,
    private val log: (String) -> Unit = {},
) : GridThumbStore {
    /** Settings' "Using {used} of {limit}". */
    data class Usage(val used: Long, val limit: Long)

    private class Opened(val gen: Long, val cache: ThumbDiskCache)

    private val mem = ByteLru(memoryBytes)
    private val limitSetting = ThumbCacheLimit(File(stateDir, LIMIT_FILE))
    private val omitsFile = File(stateDir, OMITS_FILE)

    // Under lock: the scope bound (null: none), its generation (every bind,
    // unbind and Log Out bumps it), whether the launch's saved endpoint was
    // looked at, what its device does, and the hashes asked again this launch.
    private val lock = Any()
    private var scopeId: String? = null
    @Volatile private var gen = 0L
    private var launchBound = false
    private var omits: Boolean? = null
    private val reasked = HashSet<String>()
    private val filesReasked = HashSet<String>()

    // The bound scope's cache, opened on first use (one opener at a time).
    @Volatile private var opened: Opened? = null
    private val openLock = Any()

    private val _usage = MutableStateFlow(Usage(0, THUMB_CACHE_DEFAULT))
    val usage: StateFlow<Usage> = _usage

    private val _clears = MutableStateFlow(0)
    override val clears: StateFlow<Int> = _clears

    /** The scope a page or fetch is asked under, for its writes. */
    override suspend fun scope(): Long = withContext(io) { ensureLaunchBound(); gen }

    /** Signed in to [endpoint] (normalized): its scope from now on; another's is deleted. */
    fun signedIn(endpoint: String) {
        if (endpoint.isEmpty()) return
        synchronized(lock) {
            launchBound = true
            bindLocked(scopeIdOf(endpoint))
        }
    }

    /** Log Out: nothing kept, nothing stored until a sign-in binds a scope again; every folder, the size and the markers go. */
    fun logOut() {
        synchronized(lock) {
            opened?.cache?.close()
            opened = null
            scopeId = null
            gen++
            launchBound = true
            omits = null
            reasked.clear()
            filesReasked.clear()
        }
        mem.clear()
        onMemoryDropped()
        cacheRoot.deleteRecursively()
        omitsFile.delete()
        File(stateDir, LIMIT_FILE).delete()
        _usage.value = Usage(0, THUMB_CACHE_DEFAULT)
    }

    /** In memory already, or null; cheap enough for the main thread. */
    fun peek(key: String): ByteArray? = mem.get(thumbKeyHash(key))

    override suspend fun kinds(hashes: Collection<String>): Map<String, ThumbKind> =
        withContext(io) { disk()?.kinds(hashes) ?: emptyMap() }

    override suspend fun put(entries: List<ThumbPut>, scope: Long): Map<String, ThumbKind>? = withContext(io) {
        if (scope != gen) return@withContext null
        val d = disk()
        val out = HashMap<String, ThumbKind>()
        for (e in entries) {
            // Doesn't decode: none to show, nothing kept.
            if (!decodable(e.bytes)) continue
            // Not written (not a device hash, over 8 MB, a full disk): in memory alone.
            val kept = d?.put(e.hash, e.bytes, e.kind) ?: e.kind
            if (kept == e.kind) mem.put(e.hash, e.bytes)
            out[e.hash] = kept
        }
        d?.let { publish(it) }
        if (scope != gen) null else out
    }

    override suspend fun load(key: String): ByteArray? {
        val hash = thumbKeyHash(key)
        mem.get(hash)?.let { return it }
        return withContext(io) {
            val d = disk() ?: return@withContext null
            val bytes = d.get(hash)?.first ?: return@withContext null
            if (!decodable(bytes)) { d.remove(hash); publish(d); return@withContext null }
            mem.put(hash, bytes)
            bytes
        }
    }

    override suspend fun discard(key: String) = withContext(io) {
        val hash = thumbKeyHash(key)
        mem.remove(hash)
        disk()?.let { it.remove(hash); publish(it) }
        Unit
    }

    override suspend fun omitsThumbnails(): Boolean = withContext(io) {
        ensureLaunchBound()
        synchronized(lock) { omits == true }
    }

    override suspend fun notePage(omitted: Boolean, scope: Long) = withContext(io) {
        val id = synchronized(lock) {
            if (scope != gen || scopeId == null || omits == omitted) return@withContext
            omits = omitted
            scopeId
        }
        writeOmits("$id:${if (omitted) 1 else 0}")
    }

    override suspend fun claimReasks(hashes: Collection<String>): Set<String> = claim(hashes, reasked)

    /** Files' grid's own once a launch (its listings don't say whether the device has the small one), under the same still-big rule. */
    suspend fun claimFilesReasks(hashes: Collection<String>): Set<String> = claim(hashes, filesReasked)

    private suspend fun claim(hashes: Collection<String>, set: HashSet<String>): Set<String> = withContext(io) {
        if (hashes.isEmpty()) return@withContext emptySet<String>()
        val settled = disk()?.stillBig(hashes, REASK_AFTER_MS) ?: emptySet()
        synchronized(lock) { hashes.filterTo(HashSet()) { it !in settled && set.add(it) } }
    }

    override suspend fun noteStillBig(hash: String, scope: Long) = withContext(io) {
        if (scope == gen) disk()?.noteStillBig(hash)
        Unit
    }

    override fun warmUp() { background.execute { disk() } }

    /** Settings: what the cache holds now. */
    suspend fun refreshUsage() = withContext(io) {
        disk()?.let { publish(it) } ?: run { _usage.value = Usage(0, limitSetting.get()) }
    }

    /** Settings: another size limit, kept on this phone; past it the least recently used go. */
    suspend fun setLimit(bytes: Long) = withContext(io) {
        limitSetting.set(bytes)
        disk()?.let { it.setLimit(bytes); publish(it) } ?: run { _usage.value = Usage(0, bytes) }
    }

    /** Settings' Clear thumbnail cache: the scope's thumbnails, the tiles in memory and this launch's re-asks go. */
    suspend fun clearAll() = withContext(io) {
        val d = disk()
        synchronized(lock) { reasked.clear(); filesReasked.clear() }
        mem.clear()
        onMemoryDropped()
        d?.let { it.clear(); publish(it) }
        _clears.value++
        Unit
    }

    /** Uses written out (the app going to the background). */
    fun flush() { opened?.cache?.flush() }

    /** The bound scope's cache as it is now (tests). */
    internal fun cacheNow(): ThumbDiskCache? = disk()

    // ---- inside ----------------------------------------------------------------------

    private fun ensureLaunchBound() {
        synchronized(lock) {
            if (launchBound) return
            launchBound = true
            val ep = savedEndpoint()
            if (ep.isNotEmpty()) bindLocked(scopeIdOf(ep))
        }
    }

    // Under lock: another scope (or the same again: nothing). The old cache is
    // let go, memory emptied, and every other scope's folder deleted.
    private fun bindLocked(id: String) {
        if (id == scopeId) return
        opened?.cache?.close()
        opened = null
        scopeId = id
        gen++
        reasked.clear()
        filesReasked.clear()
        omits = readOmits(id)
        mem.clear()
        onMemoryDropped()
        background.execute { cacheRoot.listFiles()?.forEach { if (it.name != id) it.deleteRecursively() } }
    }

    private fun disk(): ThumbDiskCache? {
        ensureLaunchBound()
        opened?.let { if (it.gen == gen) return it.cache }
        synchronized(openLock) {
            opened?.let { if (it.gen == gen) return it.cache }
            val (g, id) = synchronized(lock) { gen to scopeId }
            id ?: return null
            val c = ThumbDiskCache(File(cacheRoot, id), limitSetting.get(), version, background, nowMs)
            synchronized(lock) {
                // Bound elsewhere (or logged out) while it opened.
                if (g != gen) { c.close(); return null }
                opened = Opened(g, c)
            }
            log("opened: ${c.count} thumbnails, ${c.usedBytes} of ${c.limitBytes} bytes")
            publish(c)
            return c
        }
    }

    private fun publish(cache: ThumbDiskCache) {
        _usage.value = Usage(cache.usedBytes, cache.limitBytes)
    }

    private fun readOmits(id: String): Boolean? {
        val s = try { omitsFile.readText().trim() } catch (_: Exception) { return null }
        if (!s.startsWith("$id:")) return null
        return s.endsWith(":1")
    }

    private fun writeOmits(line: String) {
        val tmp = File(stateDir, "$OMITS_FILE.tmp")
        try {
            stateDir.mkdirs()
            tmp.writeText(line)
            if (!tmp.renameTo(omitsFile)) tmp.delete()
        } catch (_: Exception) { tmp.delete() }
    }

    companion object {
        /** Bump when the grids' small thumbnails change (their size, their format): every phone's cache starts over. */
        const val CACHE_VERSION = 1
        /** A big one asked for again that came big again isn't asked for again for a week (as iOS's reaskAfter). */
        const val REASK_AFTER_MS = 7 * 24 * 3_600_000L
        const val LIMIT_FILE = "thumbnail-cache-limit"
        const val OMITS_FILE = "thumbnail-cache-omits"

        /** The scope of an endpoint: the first 16 bytes of its SHA-256, in hex (as iOS's scopeID). */
        fun scopeIdOf(endpoint: String): String =
            MessageDigest.getInstance("SHA-256").digest(endpoint.toByteArray()).take(16).joinToString("") { "%02x".format(it) }
    }
}
