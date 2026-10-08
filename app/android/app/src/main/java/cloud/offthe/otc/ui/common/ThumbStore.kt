// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import android.graphics.BitmapFactory
import android.util.Log
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.data.SecretsStore
import kotlinx.coroutines.flow.StateFlow
import java.io.File

/**
 * The grids' thumbnails on this phone, by content hash ([thumbTileKey]):
 * the small ones (release 111: 400 px on the shorter side, ~38 KB) every
 * grid asks for, or a big one where the device had no small one yet, or
 * an answer from a device before release 113 that doesn't say which
 * (ThumbKind). The most recent stay in memory (up to 32 MB); all of them in
 * a persistent LRU cache on disk (ThumbDiskCache) of the size picked in
 * Settings > Thumbnail cache (1 GB unless changed), so Images opens from it
 * after a restart and asks the device only for what it lacks (GridThumbs.kt).
 * Its workings - the scope of the device signed in to, Log Out, the markers -
 * are ThumbStoreCore's; this binds them to the app: cacheDir/grid-thumbs,
 * filesDir, the endpoint saved, BitmapFactory, the decoded tiles' ThumbCache.
 *
 * OTCConnection calls [signedIn] after every sign-in; Log Out calls
 * [logOut] before and after it wipes the phone; RootView calls [flushSoon]
 * when the app goes to the background. A big thumbnail asked for on
 * purpose (the viewer's, when the thumbnail stays on screen) is never kept
 * here: tiles and viewers never share an entry.
 */
object ThumbStore : GridThumbStore {
    private const val TAG = "OTC/Thumbs"

    private val core by lazy {
        val app = OTCApp.instance
        // The cache before this one (cacheDir/thumbs, which every start emptied).
        ThumbDiskCache.BACKGROUND.execute {
            app.cacheDir.listFiles()?.forEach { if (it.name == "thumbs" || it.name.startsWith("thumbs-old-")) it.deleteRecursively() }
        }
        ThumbStoreCore(
            cacheRoot = File(app.cacheDir, "grid-thumbs"),
            stateDir = app.filesDir,
            savedEndpoint = { SecretsStore.loadOrCreate().endpointURLString },
            decodable = ::decodable,
            onMemoryDropped = { ThumbCache.clear() },
            memoryBytes = minOf(Runtime.getRuntime().maxMemory() / 8, 32L shl 20),
            log = { Log.i(TAG, it) },
        )
    }

    // A thumbnail's header read: enough to tell a JPEG from what a power cut
    // or a broken answer leaves (zeros, half a file).
    private fun decodable(bytes: ByteArray): Boolean {
        val o = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeByteArray(bytes, 0, bytes.size, o)
        return o.outWidth > 0 && o.outHeight > 0
    }

    /** Settings' "Using {used} of {limit}". */
    val usage: StateFlow<ThumbStoreCore.Usage> get() = core.usage

    /** Signed in to [endpoint] (normalized): its cache from now on, another device's deleted. */
    fun signedIn(endpoint: String) = core.signedIn(endpoint)

    /** Log Out (off the main thread): every cache, the size picked and the markers go; nothing is kept until the next sign-in. */
    fun logOut() = core.logOut()

    /** The app went to the background: the uses kept since the last write are written out. */
    fun flushSoon() = ThumbDiskCache.BACKGROUND.execute { core.flush() }

    /** In memory already, or null; cheap enough for the main thread. */
    fun peek(key: String): ByteArray? = core.peek(key)

    override suspend fun scope(): Long = core.scope()
    override suspend fun kinds(hashes: Collection<String>) = core.kinds(hashes)
    override suspend fun put(entries: List<ThumbPut>, scope: Long) = core.put(entries, scope)
    override suspend fun load(key: String) = core.load(key)
    override suspend fun discard(key: String) = core.discard(key)
    override suspend fun omitsThumbnails() = core.omitsThumbnails()
    override suspend fun notePage(omitted: Boolean, scope: Long) = core.notePage(omitted, scope)
    override suspend fun claimReasks(hashes: Collection<String>) = core.claimReasks(hashes)
    override suspend fun noteStillBig(hash: String, scope: Long) = core.noteStillBig(hash, scope)
    override fun warmUp() = core.warmUp()
    override val clears: StateFlow<Int> get() = core.clears

    /** Files' grid's own re-asks: once a launch, under the same still-big rule. */
    suspend fun claimFilesReasks(hashes: Collection<String>) = core.claimFilesReasks(hashes)

    /** Settings: what the cache holds now. */
    suspend fun refreshUsage() = core.refreshUsage()

    /** Settings: another size limit, kept on this phone. */
    suspend fun setLimit(bytes: Long) = core.setLimit(bytes)

    /** Settings' Clear thumbnail cache: the grids fetch their tiles again as they show them. */
    suspend fun clearAll() = core.clearAll()
}
