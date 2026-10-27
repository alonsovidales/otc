// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import android.app.PendingIntent
import android.content.ContentUris
import android.content.Context
import android.content.IntentSender
import android.net.Uri
import android.os.Build
import android.provider.MediaStore
import android.util.AtomicFile
import android.util.Log
import androidx.annotation.RequiresApi
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.ChunkedUpload
import cloud.offthe.otc.net.ErrorCodes
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.HasFile
import cloud.offthe.otc.proto.HasHashes
import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream

/**
 * Port of FreeUpSpace.swift: moves to this phone's trash the photos and
 * videos taken more than the chosen number of days ago that the device
 * holds an exact copy of. Off unless turned on (Settings > Free Up Space,
 * and the screen after every sign-in, SyncChoiceView). Android 11 and
 * later: MediaStore's trash request is where it starts.
 *
 * What makes a deletion safe:
 * - Each file goes by the SHA-256 of its bytes as the sync reads them
 *   (PhotoSync.openData). A file here can change in place (an editor saving
 *   over it), so a hash counts only for the version it was read from: it is
 *   kept with the row's GENERATION_MODIFIED and SIZE, and read again when
 *   either changed. AssetSyncCache's hashes say nothing about the version,
 *   so they aren't used.
 * - The device confirms every hash on its own disk right before the
 *   deletion (HasHashes: a row with it and its blob whole); an older device
 *   answers HasFile, one at a time. Nothing it doesn't confirm goes, and
 *   videos only while Include videos is on.
 * - Folders turned off (Settings > Sync Options > Folders) are left alone,
 *   even what the device got from them before: turning one off deletes
 *   nothing. Read again for the last check, so one turned off meanwhile is
 *   too.
 * - Android asks before moving them (MediaStore.createTrashRequest, which
 *   RootView launches - it needs an Activity), and the trash keeps them for
 *   30 days.
 *
 * It runs at the end of a sync while the app is in front, at most once a
 * day: a run that found nothing, or whose request was answered either way,
 * counts; one cut short doesn't. Turning it on, or changing its days, makes
 * it due at once.
 */
object FreeUpSpace {
    /** What Settings offers; iOS's are the same. */
    val dayChoices = listOf(1, 3, 7, 14, 30, 90, 365)
    const val defaultDays = 7
    /** Hashes per HasHashes; the device looks at up to 1000. */
    private const val batchSize = 500
    /** Files per trash request: its URIs travel in one Binder transaction (1 MB). */
    private const val maxPerRequest = 500
    /** A trash request older than this is dropped rather than shown: the device's answer behind it is stale. */
    private const val requestFreshMs = 120_000L
    private const val tag = "OTC/FreeUpSpace"

    /** createTrashRequest is Android 11's. */
    val supported: Boolean get() = Build.VERSION.SDK_INT >= 30

    // The sync's own file (not backed up, wiped at Log Out).
    private val prefs get() = OTCApp.instance.getSharedPreferences("otc_sync", Context.MODE_PRIVATE)
    private val _enabled by lazy { MutableStateFlow(prefs.getBoolean("freeUpSpace", false)) }
    private val _days by lazy { MutableStateFlow(days(prefs.getInt("freeUpDays", defaultDays))) }
    val enabled: StateFlow<Boolean> get() = _enabled
    val days: StateFlow<Int> get() = _days

    fun setEnabled(v: Boolean) {
        val was = _enabled.value
        _enabled.value = v
        prefs.edit().putBoolean("freeUpSpace", v).apply()
        if (v && !was) makeDue()
    }

    fun setDays(v: Int) {
        val d = days(v)
        if (d == _days.value) return
        _days.value = d
        prefs.edit().putInt("freeUpDays", d).apply()
        makeDue()
    }

    /** The stored choice, or the default for an unknown one. */
    fun days(stored: Int): Int = if (stored in dayChoices) stored else defaultDays

    private fun makeDue() = prefs.edit().remove("freeUpLastRun").apply()

    /** Once a day; a last run ahead of the clock doesn't hold it off. */
    fun isDue(lastRunMs: Long, nowMs: Long): Boolean =
        lastRunMs == 0L || lastRunMs > nowMs || nowMs - lastRunMs >= 24 * 3600_000L

    /** One MediaStore row, as far as deciding goes; [bucketId] its folder's. */
    data class Row(val id: Long, val isVideo: Boolean, val takenMs: Long, val size: Long, val generation: Long, val bucketId: Long? = null)

    /** [skipped]: the folders turned off, by BUCKET_ID. */
    fun eligible(r: Row, cutoffMs: Long, includeVideos: Boolean, skipped: Set<Long> = emptySet()): Boolean =
        r.takenMs in 1 until cutoffMs && (includeVideos || !r.isVideo) && (r.bucketId == null || r.bucketId !in skipped)

    private fun skippedFolders(): Set<Long> = PhotoFolders.bucketIds(SecretsStore.loadOrCreate().skippedFolders.value)

    /** RootView: the activity is in front (between ON_RESUME and ON_STOP). */
    @Volatile var inFront = false

    /** A trash request for RootView to launch; [more]: others were left for the next one. */
    class Request(val sender: IntentSender, val count: Int, val madeMs: Long, val more: Boolean = false)

    private val _request = MutableStateFlow<Request?>(null)
    val request: StateFlow<Request?> get() = _request

    // When the run in progress began - its trash request's answer included
    // - or 0. One whose answer never came (the Activity gone with it) holds
    // the next off for 10 minutes at most.
    private var busySince = 0L

    @Synchronized private fun begin(): Boolean {
        val now = System.currentTimeMillis()
        if (busySince != 0L && now - busySince in 0 until 600_000L) return false
        busySince = now
        return true
    }

    @Synchronized private fun end() { busySince = 0L }

    /** RootView, about to launch it: null when there is none, or it got stale (run again after the next sync). */
    fun takeRequest(): Request? {
        val r = _request.value ?: return null
        _request.value = null
        if (System.currentTimeMillis() - r.madeMs > requestFreshMs) { Log.i(tag, "trash request dropped, stale"); end(); return null }
        return r
    }

    /**
     * The trash request's answer: declined, or all moved, asked again
     * tomorrow, not after every sync; moved with more left, the next sync's
     * end goes on with them.
     */
    fun requestAnswered(moved: Boolean, more: Boolean = false) {
        Log.i(tag, if (moved) "moved to the trash" else "trash request declined")
        if (moved && more) makeDue() else prefs.edit().putLong("freeUpLastRun", System.currentTimeMillis()).apply()
        end()
    }

    /** Log Out: off, and nothing known (the prefs and files are wiped). */
    @Synchronized fun logOut() {
        _enabled.value = false
        _days.value = defaultDays
        _request.value = null
        hashes.clear()
        loaded = true
        end()
    }

    /** PhotoSync.runForeground's end: runs when on, due and in front. */
    suspend fun afterSync() {
        if (Build.VERSION.SDK_INT < 30 || !_enabled.value || !inFront) return
        if (!isDue(prefs.getLong("freeUpLastRun", 0), System.currentTimeMillis())) return
        if (!begin()) return
        var waiting = false
        try {
            val request = run()
            if (request == null) {
                prefs.edit().putLong("freeUpLastRun", System.currentTimeMillis()).apply()
            } else {
                _request.value = request
                waiting = true // the answer clears it (requestAnswered)
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            Log.w(tag, "stopped: $e")
        } finally {
            if (!waiting) end()
        }
    }

    /** The run: the trash request for what the device confirmed, or null when there is nothing to move. */
    @RequiresApi(30)
    private suspend fun run(): Request? {
        if (!PhotoSync.hasPermission()) return null
        val secrets = SecretsStore.loadOrCreate()
        if (!secrets.isConfigured) return null
        val endpoint = secrets.endpointURLString
        val includeVideos = secrets.includeVideos.value
        val cutoffMs = System.currentTimeMillis() - _days.value * 24 * 3600_000L
        val skipped = skippedFolders()
        val rows = rows(includeVideos).filter { eligible(it, cutoffMs, includeVideos, skipped) }
        Log.i(tag, "${rows.size} files old enough")
        if (rows.isEmpty()) return null

        OTCConnection.ensureConnected()
        val hashOf = HashMap<Long, String>()
        for (r in rows) {
            currentCoroutineContext().ensureActive()
            hashOf[r.id] = hash(r, uri(r)) ?: continue
        }
        flush()
        val held = held(hashOf.values.toSet())
        // Read again right before the deletion: still there, still the
        // version that was hashed, still old enough.
        val confirmed = hashOf.filterValues { it in held }.keys
        Log.i(tag, "the device holds ${confirmed.size} of ${hashOf.size} hashed")
        if (confirmed.isEmpty()) return null
        val skippedNow = skippedFolders()
        val now = rows(includeVideos).filter { it.id in confirmed && eligible(it, cutoffMs, includeVideos, skippedNow) && cached(it) == hashOf[it.id] }
        if (now.isEmpty()) return null
        // A Log Out (or another device) since the check: those hashes were another device's.
        if (SecretsStore.loadOrCreate().endpointURLString != endpoint || !inFront) throw IllegalStateException("not now") // i18n-ignore: logged, never shown
        val batch = now.take(maxPerRequest)
        val pi: PendingIntent = MediaStore.createTrashRequest(OTCApp.instance.contentResolver, batch.map(::uri), true)
        return Request(pi.intentSender, batch.size, System.currentTimeMillis(), more = now.size > batch.size)
    }

    private fun uri(r: Row): Uri = ContentUris.withAppendedId(
        if (r.isVideo) MediaStore.Video.Media.EXTERNAL_CONTENT_URI else MediaStore.Images.Media.EXTERNAL_CONTENT_URI, r.id,
    )

    /** The library's photos (and videos), as PhotoSync.fetchNewAssets reads them; not those already in the trash. */
    @RequiresApi(30)
    private fun rows(includeVideos: Boolean): List<Row> {
        val proj = arrayOf(
            MediaStore.Files.FileColumns._ID, MediaStore.Files.FileColumns.MEDIA_TYPE, MediaStore.Files.FileColumns.DATE_TAKEN,
            MediaStore.Files.FileColumns.DATE_ADDED, MediaStore.Files.FileColumns.SIZE, MediaStore.Files.FileColumns.GENERATION_MODIFIED,
            MediaStore.MediaColumns.BUCKET_ID,
        )
        val types = if (includeVideos) "(${MediaStore.Files.FileColumns.MEDIA_TYPE_IMAGE},${MediaStore.Files.FileColumns.MEDIA_TYPE_VIDEO})" else "(${MediaStore.Files.FileColumns.MEDIA_TYPE_IMAGE})"
        val out = mutableListOf<Row>()
        // A4: never what the viewer's Save wrote - a copy with the photo's
        // old date, which the device of course holds.
        OTCApp.instance.contentResolver.query(
            MediaStore.Files.getContentUri("external"), proj, "${MediaStore.Files.FileColumns.MEDIA_TYPE} IN $types AND ${PhotoFolders.savedClause}", null, null,
        )?.use { cur ->
            while (cur.moveToNext()) {
                val taken = cur.getLong(2).takeIf { it > 0 } ?: (cur.getLong(3) * 1000)
                out += Row(cur.getLong(0), cur.getInt(1) == MediaStore.Files.FileColumns.MEDIA_TYPE_VIDEO, taken, cur.getLong(4), cur.getLong(5),
                    if (cur.isNull(6)) null else cur.getLong(6))
            }
        }
        return out
    }

    /**
     * The device's say, by hash: those it holds whole, asked on its disk
     * now. An older device, without HasHashes, answers HasFile for each.
     */
    private suspend fun held(hashes: Set<String>): Set<String> {
        val all = hashes.toList()
        val out = HashSet<String>()
        var start = 0
        while (start < all.size) {
            val batch = all.subList(start, minOf(start + batchSize, all.size))
            val resp = OTCConnection.request { it.setReqHasHashes(HasHashes.newBuilder().addAllHashes(batch)) }
            when {
                resp.payloadCase == RespEnvelope.PayloadCase.RESP_HASHES_FOUND ->
                    // Only what was asked counts.
                    out += resp.respHashesFound.hashesList.filter { it in batch }
                resp.error && ErrorCodes.isUnknownPayload(resp.errorCode, resp.errorMessage) -> {
                    for (h in all.subList(start, all.size)) {
                        currentCoroutineContext().ensureActive()
                        val one = OTCConnection.request { it.setReqHasFile(HasFile.newBuilder().setHash(h)) }
                        if (one.payloadCase == RespEnvelope.PayloadCase.RESP_FILE_EXISTS && one.respFileExists.exists) out += h
                    }
                    return out
                }
                else -> throw IllegalStateException(resp.errorMessage.ifEmpty { "HasHashes failed" }) // i18n-ignore: logged, never shown
            }
            start += batchSize
        }
        return out
    }

    // Hashes by MediaStore id, each with the version it was read from:
    // "<hash> <generation> <size>". Its own file, like AssetSyncCache.
    private val file by lazy { AtomicFile(File(OTCApp.instance.filesDir, "free_up_hashes.json")) }
    private val hashes = HashMap<String, String>()
    private var loaded = false
    private var dirty = false

    @Synchronized private fun load() {
        if (loaded) return
        loaded = true
        try { val o = JSONObject(file.readFully().decodeToString()); for (k in o.keys()) hashes[k] = o.getString(k) } catch (_: Exception) {}
    }

    /** The hash read from this very version of the file, if there is one. */
    @Synchronized fun cached(r: Row): String? {
        load()
        val parts = hashes[r.id.toString()]?.split(' ') ?: return null
        return parts.getOrNull(0)?.takeIf { parts.size == 3 && parts[1] == r.generation.toString() && parts[2] == r.size.toString() }
    }

    @Synchronized private fun record(r: Row, hash: String) {
        load()
        hashes[r.id.toString()] = "$hash ${r.generation} ${r.size}"
        dirty = true
    }

    @Synchronized private fun flush() {
        if (!dirty) return
        var out: FileOutputStream? = null
        try {
            val bytes = JSONObject(hashes as Map<*, *>).toString().toByteArray()
            out = file.startWrite()
            out.write(bytes)
            file.finishWrite(out)
        } catch (_: Exception) {
            out?.let { file.failWrite(it) }
        }
        dirty = false
    }

    /** This version's hash: remembered, or read now (null when the file can't be read). */
    private fun hash(r: Row, uri: Uri): String? {
        cached(r)?.let { return it }
        val d = try { ChunkedUpload.digest { PhotoSync.openData(uri) } } catch (e: Exception) { Log.w(tag, "can't read ${r.id}: $e"); return null }
        // Its size changed while it was read: not this version's.
        if (r.size > 0 && d.size != r.size) return null
        record(r, d.sha256)
        // Now and then, so a run cut short doesn't read them all again.
        if (++unflushed >= 50) { unflushed = 0; flush() }
        return d.sha256
    }

    private var unflushed = 0
}
