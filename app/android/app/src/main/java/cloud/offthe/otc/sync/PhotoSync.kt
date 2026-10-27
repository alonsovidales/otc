// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import android.Manifest
import android.content.ContentUris
import android.content.Context
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.provider.MediaStore
import android.util.AtomicFile
import android.util.Log
import androidx.core.content.ContextCompat
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.data.SyncHalt
import cloud.offthe.otc.data.UploadModel
import cloud.offthe.otc.net.ChunkedUpload
import cloud.offthe.otc.net.ErrorCodes
import cloud.offthe.otc.net.NetworkWatch
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.sizeMatches
import cloud.offthe.otc.proto.HasFile
import cloud.offthe.otc.proto.LinkFile
import cloud.offthe.otc.proto.File as PbFile
import cloud.offthe.otc.proto.ListFiles
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.SyncChoice
import com.google.protobuf.Timestamp
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import org.json.JSONArray
import java.io.File
import java.io.FileOutputStream
import java.io.InputStream
import java.security.MessageDigest
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicBoolean

// Port of PhotoSync.swift: uploads new camera-roll photos/videos to
// /android/<deviceId>/<filename> on the device, hash-first (issue #58),
// three at a time (issue #10), pausable (issue #30), watermarked by the
// newest DATE_ADDED synced so far, with what failed on a retry list
// (AssetSyncCache.pending) rather than holding the watermark (A5).
//
// DATE_ADDED is when the MediaStore first listed the file, not when the
// photo was taken: a years-old photo saved, received or copied here today
// is new to the sync. Older ones it may not have sent come in one-off
// passes that leave the watermark alone (backfill()): a folder turned back
// on (PhotoFolders), photos picked under limited access, and the catch-up
// once limited access becomes full.
object PhotoSync {
    // dateAddedMs is DATE_TAKEN when there is one (the upload's created
    // time); addedSec is DATE_ADDED, the column the watermark is compared to.
    // size is MediaStore's SIZE, 0 when unknown; bucketId its folder's.
    // saved: in a folder the viewer's Save writes to (PhotoFolders.isSaved).
    data class Asset(val id: Long, val uri: Uri, val name: String, val mime: String, val dateAddedMs: Long, val isVideo: Boolean, val addedSec: Long = 0, val size: Long = 0, val bucketId: Long? = null, val saved: Boolean = false)

    private const val maxConcurrentUploads = 3
    // The otc_sync keys of the one-off passes (wiped at Log Out, not backed up).
    private const val FLOOR = "syncFloorMs"
    private const val BACKFILL = "backfillFolders"
    private const val WAS_LIMITED = "accessWasLimited"
    /** In backfill(): the catch-up, everything added since the floor. */
    const val CATCH_UP = "*"
    private val syncing = AtomicBoolean(false)
    /** A sync is going (in front, or a WorkManager run in this process). */
    val isSyncing: Boolean get() = syncing.get()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val prefs get() = OTCApp.instance.getSharedPreferences("otc_sync", Context.MODE_PRIVATE)

    var lastSyncMs: Long
        get() = prefs.getLong("lastSyncMs", 0)
        private set(v) { prefs.edit().putLong("lastSyncMs", v).apply() }

    // Checks what mediaPermissions() (NewPostPickerView.kt) asks for on this
    // API level: READ_MEDIA_IMAGES doesn't exist before 33, so on Android
    // 10-12 it was always denied and nothing ever synced.
    fun hasPermission(context: Context = OTCApp.instance): Boolean {
        fun granted(p: String) = ContextCompat.checkSelfPermission(context, p) == PackageManager.PERMISSION_GRANTED
        if (Build.VERSION.SDK_INT < 33) return granted(Manifest.permission.READ_EXTERNAL_STORAGE)
        return granted(Manifest.permission.READ_MEDIA_IMAGES) ||
            (Build.VERSION.SDK_INT >= 34 && granted(Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED))
    }

    /** Android 14's limited access: the app sees only the photos picked in the system's prompt. */
    fun accessLimited(context: Context = OTCApp.instance): Boolean {
        if (Build.VERSION.SDK_INT < 34) return false
        fun granted(p: String) = ContextCompat.checkSelfPermission(context, p) == PackageManager.PERMISSION_GRANTED
        return limited(Build.VERSION.SDK_INT, granted(Manifest.permission.READ_MEDIA_IMAGES), granted(Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED))
    }

    /** Limited: Android 14 or later, picked photos allowed and not all of them. */
    fun limited(sdk: Int, allImages: Boolean, userSelected: Boolean): Boolean = sdk >= 34 && !allImages && userSelected

    // The sync in progress, so Log Out can stop it (as PhotoSync.swift's
    // cancel()): every network call in it is a suspension point. Set by the
    // run that owns the sync, so a call that found one already running
    // (every resume during a long sync) can't swap it for a finished no-op;
    // the WorkManager run is covered too.
    @Volatile private var currentSync: Job? = null
    // Bumped by cancel(): a run from before Log Out writes no sync state
    // into the store it just wiped.
    @Volatile private var generation = 0
    // Bumped by setWatermark(): a run that started before the owner pressed
    // Sync All or Sync From Now doesn't write over that choice.
    @Volatile private var watermarkGen = 0
    private val watermarkLock = Any()
    // IOS-12, as iOS: a trigger that came while a run went (a new photo, the
    // app back in front, Sync All) used to be dropped here; the run goes
    // through once more at its end instead.
    @Volatile private var rerunWanted = false
    // The last run stopped on a metered network with "Wi-Fi only" on: the
    // next unmetered one starts a run (watchCost), while the app is in front
    // (in the background WorkManager's UNMETERED runs do).
    @Volatile private var waitingForWifi = false
    // When a run last stopped on the device's storage_full, and its words:
    // automatic runs wait FULL_PAUSE_MS after it rather than reading photos
    // only to have them refused. Sync All and Sync From Now try at once.
    @Volatile private var fullSinceMs = 0L
    @Volatile private var fullWords = ""
    const val FULL_PAUSE_MS = 30 * 60_000L

    /** RootView: the activity is in front (between ON_RESUME and ON_STOP). */
    @Volatile var inFront = false

    init {
        // A1: back on an unmetered network, a run that waited for it goes.
        NetworkWatch.watchCost { metered ->
            if (!metered && waitingForWifi && inFront) {
                waitingForWifi = false
                Log.i(tag, "on Wi-Fi again: the sync that waited for it starts")
                runForegroundAsync()
            }
        }
    }

    /**
     * IOS-09 / A9: a run with fewer new assets than this, on a phone that
     * has synced before (AssetSyncCache), doesn't download the listing of
     * /android/<deviceId>/ - every photo this phone ever synced, megabytes
     * through the bridge for a big library - to check them against: each
     * goes by its hash (HasFile, LinkFile), and a name another file holds
     * is found by the device's duplicated_file (sendUnlessTaken). As iOS's.
     */
    const val LISTING_FROM = 100

    /** Pure: whether a run of [assets] lists the folder first. */
    fun needsListing(assets: Int, cacheEmpty: Boolean): Boolean = assets > 0 && (cacheEmpty || assets >= LISTING_FROM)

    /** Pure, A1: whether "Wi-Fi only" holds the sync on this network. */
    fun waitsForWifi(wifiOnly: Boolean, metered: Boolean): Boolean = wifiOnly && metered

    /** Pure: whether an automatic run still waits after the device said it was full at [sinceMs]; a clock set back doesn't hold it for ever. */
    fun waitsAfterFull(sinceMs: Long, nowMs: Long): Boolean = sinceMs > 0 && nowMs >= sinceMs && nowMs - sinceMs < FULL_PAUSE_MS

    /** "Wi-Fi only" turned off (SecretsStore): a run that waited for Wi-Fi goes now. */
    fun wifiOnlyOff() {
        if (!waitingForWifi) return
        waitingForWifi = false
        runForegroundAsync()
    }

    /**
     * Settings > Sync All (0) and Sync From Now (now), and SyncChoice's
     * answer - which is also the point the owner chose to sync from (the
     * catch-up's). From now on, the one-off passes still to come and what
     * limited access showed so far are behind it.
     */
    fun setWatermark(ms: Long) {
        synchronized(watermarkLock) {
            watermarkGen++
            lastSyncMs = ms
            val e = prefs.edit().putLong(FLOOR, ms)
            if (ms > 0) e.remove(BACKFILL)
            e.apply()
            if (ms > 0) limitedSeen.delete()
            // Sync From Now skips what earlier runs couldn't finish too, as
            // on iOS.
            if (ms > 0) AssetSyncCache.pending.clear()
            // Asked for now: a run going goes through once more from here,
            // and a device that was full is tried again at once.
            if (syncing.get()) rerunWanted = true
            fullSinceMs = 0L
        }
    }

    // The point the owner chose to sync from: 0 for everything, or when
    // Sync From Now (or "only new ones") was pressed. null: chosen before
    // it was kept. Only the catch-up reads it.
    private val floorMs: Long? get() = if (prefs.contains(FLOOR)) prefs.getLong(FLOOR, 0) else null

    /**
     * Settings > Sync Options > Folders, a folder turned back on: the next
     * run backs up everything it holds, older than the watermark too (what
     * is already on the device is found by the listing or the hash cache
     * and not sent again). Also CATCH_UP.
     */
    fun queueBackfill(id: String) { synchronized(watermarkLock) { setBackfill(backfill() + id) } }

    /** A folder turned off again before its pass ran. */
    fun dropBackfill(id: String) { synchronized(watermarkLock) { setBackfill(backfill() - id) } }

    /** The one-off passes waiting: folders by BUCKET_ID, and CATCH_UP. */
    fun backfill(): Set<String> = PhotoFolders.storedSet(prefs, BACKFILL)

    private fun setBackfill(v: Set<String>) = PhotoFolders.storeSet(prefs.edit(), BACKFILL, v).apply()

    /**
     * The passes a run took, once they are done: those queued while it ran
     * stay. So does a pass it took but didn't go through: a folder whose
     * photos it let go as it was turned off ([cutShort]), queued again once
     * it is turned back on; and every pass under limited access ([limited]),
     * which sees only the picked photos - it stays queued until a run with
     * full access goes through it.
     */
    fun afterPass(stored: Set<String>, taken: Set<String>, finished: Boolean, cutShort: Set<String> = emptySet(), limited: Boolean = false): Set<String> =
        if (finished && !limited) stored - (taken - cutShort) else stored

    /** Under limited access, the photos picked since the last run; none the first time ([seen] null): it only notes them. */
    fun newlyVisible(seen: Set<Long>?, visible: Set<Long>): Set<Long> = if (seen == null) emptySet() else visible - seen

    enum class AccessStep { NONE, CATCH_UP, ASK_AGAIN }

    /**
     * What a run does about the photo access: once it is full after it was
     * limited, the photos the app couldn't see are behind the watermark, so
     * they are fetched from the owner's starting point - or, when that isn't
     * known, the owner is asked again (SyncChoice). Access taken away isn't
     * full.
     */
    fun accessStep(limited: Boolean, full: Boolean, wasLimited: Boolean, floorKnown: Boolean): AccessStep = when {
        limited || !full || !wasLimited -> AccessStep.NONE
        floorKnown -> AccessStep.CATCH_UP
        else -> AccessStep.ASK_AGAIN
    }

    private fun checkAccess() {
        val context = OTCApp.instance
        val limited = accessLimited(context)
        val wasLimited = prefs.getBoolean(WAS_LIMITED, false)
        if (limited && !wasLimited) prefs.edit().putBoolean(WAS_LIMITED, true).apply()
        val step = accessStep(limited, hasPermission(context) && !limited, wasLimited, floorMs != null)
        if (step == AccessStep.NONE) return
        Log.i(tag, "full photo access after limited access: $step")
        prefs.edit().remove(WAS_LIMITED).apply()
        limitedSeen.delete()
        if (step == AccessStep.CATCH_UP) queueBackfill(CATCH_UP) else SyncChoice.ask()
    }

    // What limited access let the app see after the last run that went
    // through, by MediaStore id; absent: nothing noted yet. Its own file,
    // like FreeUpSpace's (not backed up, wiped at Log Out).
    private val limitedSeen by lazy { AtomicFile(File(OTCApp.instance.filesDir, "limited_seen.json")) }

    private fun readSeen(): Set<Long>? = try {
        val a = JSONArray(limitedSeen.readFully().decodeToString())
        (0 until a.length()).mapTo(HashSet()) { a.getLong(it) }
    } catch (e: Exception) { null }

    private fun writeSeen(ids: Set<Long>) {
        var out: FileOutputStream? = null
        try {
            val bytes = JSONArray(ids.toList()).toString().toByteArray()
            out = limitedSeen.startWrite()
            out.write(bytes)
            limitedSeen.finishWrite(out)
        } catch (e: Exception) {
            out?.let { limitedSeen.failWrite(it) }
        }
    }

    // A run's own watermark, unless Log Out or the owner came first.
    private fun runWatermark(ms: Long, gen: Int, wgen: Int) = synchronized(watermarkLock) {
        if (gen == generation && wgen == watermarkGen) lastSyncMs = ms
    }

    /** [after]: a route check to wait for first (issue #190), so the sync goes the way it picks. */
    fun runForegroundAsync(after: Job? = null) {
        scope.launch {
            after?.join()
            try { runForeground() } catch (e: Exception) { Log.w(tag, "sync failed: $e") }
        }
    }

    /** Log Out: stop the sync in progress. */
    // Under the watermark lock: once it returns, no run is between its
    // generation check and its watermark write.
    fun cancel() {
        synchronized(watermarkLock) { generation++ }
        currentSync?.cancel()
        waitingForWifi = false
        fullSinceMs = 0L
    }

    /**
     * The photos (and videos) added after [sinceMs], oldest first. [skipped]:
     * folders left out (the sync's; the composer leaves none out); [only]:
     * just these folders.
     */
    fun fetchNewAssets(
        includeVideos: Boolean, sinceMs: Long, limit: Int = 0, newestFirst: Boolean = false,
        skipped: Set<String> = emptySet(), only: Set<String>? = null, leaveOutSaved: Boolean = false,
    ): List<Asset> {
        val sel = selection(includeVideos, sinceMs, skipped, only, leaveOutSaved) ?: return emptyList()
        val order = "${MediaStore.Files.FileColumns.DATE_ADDED} ${if (newestFirst) "DESC" else "ASC"}" + if (limit > 0) " LIMIT $limit" else ""
        return query(sel, order)
    }

    /**
     * The retry list's assets still on the phone, by MediaStore id, whatever
     * their type or folder (a video kept for later while Include videos is
     * off stays on the list, as on iOS), oldest first.
     */
    private fun fetchByIds(ids: Collection<Long>): List<Asset> {
        if (ids.isEmpty()) return emptyList()
        val types = "(${MediaStore.Files.FileColumns.MEDIA_TYPE_IMAGE},${MediaStore.Files.FileColumns.MEDIA_TYPE_VIDEO})"
        return ids.chunked(500).flatMap { batch ->
            query("${MediaStore.Files.FileColumns.MEDIA_TYPE} IN $types AND ${MediaStore.Files.FileColumns._ID} IN (${batch.joinToString(",")})", // i18n-ignore: a MediaStore selection, SQL
                "${MediaStore.Files.FileColumns.DATE_ADDED} ASC")
        }
    }

    private fun query(sel: String, order: String): List<Asset> {
        val cr = OTCApp.instance.contentResolver
        val proj = arrayOf(MediaStore.Files.FileColumns._ID, MediaStore.Files.FileColumns.DISPLAY_NAME, MediaStore.Files.FileColumns.MIME_TYPE,
            MediaStore.Files.FileColumns.DATE_ADDED, MediaStore.Files.FileColumns.MEDIA_TYPE, MediaStore.Files.FileColumns.DATE_TAKEN,
            MediaStore.Files.FileColumns.SIZE, MediaStore.MediaColumns.BUCKET_ID, MediaStore.MediaColumns.RELATIVE_PATH)
        val out = mutableListOf<Asset>()
        cr.query(MediaStore.Files.getContentUri("external"), proj, sel, null, order)?.use { c ->
            while (c.moveToNext()) {
                val id = c.getLong(0); val type = c.getInt(4)
                val isVideo = type == MediaStore.Files.FileColumns.MEDIA_TYPE_VIDEO
                val base = if (isVideo) MediaStore.Video.Media.EXTERNAL_CONTENT_URI else MediaStore.Images.Media.EXTERNAL_CONTENT_URI
                val taken = c.getLong(5).takeIf { it > 0 } ?: c.getLong(3) * 1000
                out += Asset(id, ContentUris.withAppendedId(base, id), c.getString(1) ?: "file", c.getString(2) ?: "application/octet-stream", taken, isVideo, addedSec = c.getLong(3), size = c.getLong(6),
                    bucketId = if (c.isNull(7)) null else c.getLong(7), saved = PhotoFolders.isSaved(c.getString(8)))
            }
        }
        return out
    }

    /**
     * fetchNewAssets' selection; null when [only] names no folder, so
     * nothing is fetched. [leaveOutSaved]: the sync's - what the viewer's
     * Save wrote stays out (A4, PhotoFolders.savedClause); the composer
     * shows it.
     */
    fun selection(includeVideos: Boolean, sinceMs: Long, skipped: Set<String> = emptySet(), only: Set<String>? = null, leaveOutSaved: Boolean = false): String? {
        val types = if (includeVideos) "(${MediaStore.Files.FileColumns.MEDIA_TYPE_IMAGE},${MediaStore.Files.FileColumns.MEDIA_TYPE_VIDEO})" else "(${MediaStore.Files.FileColumns.MEDIA_TYPE_IMAGE})"
        var sel = "${MediaStore.Files.FileColumns.MEDIA_TYPE} IN $types" + if (sinceMs > 0) " AND ${MediaStore.Files.FileColumns.DATE_ADDED} > ${sinceMs / 1000}" else ""
        PhotoFolders.skipClause(skipped)?.let { sel += " AND $it" }
        if (leaveOutSaved) sel += " AND ${PhotoFolders.savedClause}"
        if (only != null) sel += " AND " + (PhotoFolders.onlyClause(only) ?: return null)
        return sel
    }

    /**
     * The asset's bytes as they are on disk. Since Android 10 the MediaStore
     * serves a copy with the GPS EXIF tags blanked (0/0 rationals, which the
     * device reads as NaN) unless the app holds ACCESS_MEDIA_LOCATION and
     * asks for the original - issue #127's map needs the real position, the
     * way PhotoKit hands it to the iOS app. A stream, not the bytes: issue
     * #165, a video can be GBs.
     */
    fun openData(uri: Uri): InputStream {
        val context = OTCApp.instance
        val src = if (Build.VERSION.SDK_INT >= 29 &&
            ContextCompat.checkSelfPermission(context, Manifest.permission.ACCESS_MEDIA_LOCATION) == PackageManager.PERMISSION_GRANTED
        ) MediaStore.setRequireOriginal(uri) else uri
        return context.contentResolver.openInputStream(src) ?: throw IllegalStateException("cannot read $uri")
    }

    // The phone's own copy can't be read (a MediaStore row whose file is
    // gone, say): retrying won't help, so it doesn't hold the watermark.
    private class UnreadableAsset(cause: Throwable) : Exception(cause.message, cause)

    private fun digestOf(asset: Asset): ChunkedUpload.Digest =
        try { ChunkedUpload.digest { openData(asset.uri) } } catch (e: Exception) { throw UnreadableAsset(e) }

    private fun timestamp(ms: Long): Timestamp = Timestamp.newBuilder().setSeconds(ms / 1000).setNanos(((ms % 1000) * 1_000_000).toInt()).build()

    /**
     * Where an asset goes when a different file already has its name
     * (DISPLAY_NAME repeats across DCIM, Download, WhatsApp...): the name
     * with "_" and 8 hex of the SHA-256 of its MediaStore id before the
     * extension - the same asset always lands on the same path. The iOS
     * app tags its own the same way.
     */
    private fun altPath(targetDir: String, cleanName: String, id: Long): String {
        val tag = MessageDigest.getInstance("SHA-256").digest(id.toString().toByteArray())
            .take(4).joinToString("") { "%02x".format(it) }
        val dot = cleanName.lastIndexOf('.')
        return if (dot > 0 && dot < cleanName.length - 1) "$targetDir${cleanName.substring(0, dot)}_$tag${cleanName.substring(dot)}"
        else "$targetDir${cleanName}_$tag"
    }

    /**
     * Uploads (or links) one asset; returns the server path. Shared with the
     * composer, which has no listing ([known] empty). [known]: the target
     * folder's files by path.
     */
    suspend fun uploadIfNeeded(
        asset: Asset, targetDir: String, known: Map<String, PbFile> = emptyMap(), cacheEpoch: Int = AssetSyncCache.epoch(),
    ): String {
        val cleanName = asset.name.replace("/", "_")
        val path = "$targetDir$cleanName"
        val alt = altPath(targetDir, cleanName, asset.id)
        val created = timestamp(asset.dateAddedMs)
        val cacheKey = asset.id.toString()

        // Issue #165: hashed as a stream (4 MiB at a time), never read whole.
        var digest: ChunkedUpload.Digest? = null

        // The plain name is this asset's unless the file there is another
        // one: it used to count as synced on the name alone.
        var target = path
        known[path]?.let { there ->
            if (alt in known) return alt
            // Issue #141: no hash, its content is missing on the device. Sent
            // to the same path, it is restored if it is this file, or refused
            // as "Duplicated file" (then alt, below) if it isn't.
            if (there.hash.isEmpty()) return@let
            val cached = AssetSyncCache.hash(cacheKey)
            val mine = when {
                cached != null -> cached == there.hash
                // Size before reading anything (wrapped against an older device's int32).
                asset.size == 0L || there.sizeMatches(asset.size) -> true
                else -> digestOf(asset).also { digest = it }.sha256 == there.hash // a stale SIZE?
            }
            if (mine) { digest?.let { AssetSyncCache.record(cacheKey, it.sha256, cacheEpoch) }; return path }
            target = alt
        }

        suspend fun hasFile(h: String): Boolean {
            val r = OTCConnection.request { it.setReqHasFile(HasFile.newBuilder().setHash(h)) }
            return r.payloadCase == RespEnvelope.PayloadCase.RESP_FILE_EXISTS && r.respFileExists.exists
        }

        var hash = digest?.sha256 ?: AssetSyncCache.hash(cacheKey) ?: digestOf(asset).also { digest = it }.sha256
        var already = hasFile(hash)
        if (!already && digest == null) { digest = digestOf(asset); hash = digest!!.sha256; already = hasFile(hash) }

        suspend fun send(to: String): RespEnvelope = if (already) {
            OTCConnection.request { it.setReqLinkFile(LinkFile.newBuilder().setHash(hash).setPath(to).setForceOverride(false).setCreated(created)) }
        } else {
            ChunkedUpload.upload(to, digest!!.size, { openData(asset.uri) }, forceOverride = false, created = created, sha256 = hash)
        }

        // null: another file took the plain name since the listing (two
        // same-named assets in one run), or the composer met one. The device
        // says so (LinkFile and FinishUpload, every release) with error_code
        // "duplicated_file", or before the localization release only in the
        // message (ErrorCodes).
        suspend fun sendUnlessTaken(to: String): RespEnvelope? = try {
            send(to).takeUnless { to != alt && it.payloadCase != RespEnvelope.PayloadCase.RESP_FILE && ErrorCodes.isDuplicatedFile(it.errorCode, it.errorMessage) }
        } catch (e: OTCConnection.RequestError) {
            if (to == alt || !ErrorCodes.isDuplicatedFile(e.code, e.message)) throw e
            null
        }

        val resp = sendUnlessTaken(target) ?: run {
            target = alt
            // A refused FinishUpload drops its content again.
            if (!already) already = hasFile(hash)
            send(alt)
        }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_FILE) { AssetSyncCache.record(cacheKey, hash, cacheEpoch); return target }
        // With its code, which the sync reads (storage_full).
        throw OTCConnection.RequestError(resp.errorMessage.ifEmpty { "Upload failed" }, resp.errorCode) // i18n-ignore: logged by the sync, never shown
    }

    private const val tag = "OTC/PhotoSync"

    /**
     * A19: what a failure goes into the log as - ids and kinds, never a
     * photo's name or a path, which a file's own exception can carry in its
     * message (release builds log at WARN too). The device's refusals say
     * their code only (RequestError.toString).
     */
    internal fun logged(e: Throwable): String = if (e is OTCConnection.RequestError) e.toString() else e.javaClass.simpleName

    /**
     * A run's one-off list; [visible]: what limited access shows, noted once
     * the list went; [limited]: the list was made under limited access.
     */
    private class OneOff(val assets: List<Asset>, val visible: Set<Long>?, val limited: Boolean)

    /**
     * The photos a run sends besides what is new since the watermark
     * (oneOffList), none of them in a folder turned off, and what limited
     * access shows. [gen], [wgen]: the run's, as in runWatermark.
     */
    private fun oneOffPass(taken: Set<String>, includeVideos: Boolean, skipped: Set<String>, fresh: List<Asset>, gen: Int, wgen: Int): OneOff {
        val limited = accessLimited()
        var visible: Set<Long>? = null
        var picked = emptySet<Long>()
        if (limited) {
            // Only what was picked, so a short list. Every type and folder:
            // a setting changed later doesn't make an old pick look new.
            val now = fetchNewAssets(true, 0, leaveOutSaved = true).mapTo(HashSet()) { it.id }
            val seen = readSeen()
            if (seen == null) {
                // The first time, only noted: what is visible now is what the
                // watermark went through, as before. Not over Sync All or Sync
                // From Now pressed since the run began: the next run notes
                // what is visible then.
                synchronized(watermarkLock) { if (gen == generation && wgen == watermarkGen) writeSeen(now) }
            } else {
                picked = newlyVisible(seen, now)
                visible = now
            }
        }
        val assets = oneOffList(taken, floorMs, picked, fresh.mapTo(HashSet()) { it.id }, { it.id }) { since, only ->
            fetchNewAssets(includeVideos, since, skipped = skipped, only = only, leaveOutSaved = true)
        }
        return OneOff(assets, visible, limited)
    }

    /**
     * A run's one-off list, each photo once and none of [fresh] (the ids it
     * fetched as new since the watermark): everything in the folders turned
     * back on ([taken]), the catch-up (everything added since the [floor],
     * when it is taken and known), and the photos [picked] since the last
     * run under limited access. [fetch] (since, just these folders or null)
     * is the sync's query, Include videos and the folders turned off
     * applied: a pick it doesn't return isn't sent.
     */
    fun <A> oneOffList(taken: Set<String>, floor: Long?, picked: Set<Long>, fresh: Set<Long>, id: (A) -> Long, fetch: (Long, Set<String>?) -> List<A>): List<A> {
        val out = mutableListOf<A>()
        val folders = taken - CATCH_UP
        if (folders.isNotEmpty()) out += fetch(0, folders)
        if (CATCH_UP in taken && floor != null) out += fetch(floor, null)
        if (picked.isNotEmpty()) out += fetch(0, null).filter { id(it) in picked }
        return out.distinctBy(id).filter { id(it) !in fresh }
    }

    suspend fun runForeground() {
        if (!syncUnlessRunning()) return
        // Free up space, once this run has synced what it could - after the
        // sync's own flag is down. Only in front, at most once a day.
        FreeUpSpace.afterSync()
    }

    /** How one pass of the sync ended. */
    private sealed interface PassEnd {
        /** Through every chunk (failures on the retry list). */
        data object Done : PassEnd
        /** Nothing to do: the choice not made, no media permission, no device. */
        data object Skipped : PassEnd
        /** Stopped: the device can't be reached. */
        data object CutShort : PassEnd
        /** Stopped, or never started: "Wi-Fi only" on a metered network. */
        data object WaitingForWifi : PassEnd
        /** Stopped on the device's storage_full, with its words. */
        data class DeviceFull(val words: String) : PassEnd
    }

    /** What became of one asset of a chunk. */
    private sealed interface Sent {
        data object Done : Sent
        data object Retry : Sent
        data class Full(val words: String) : Sent
    }

    /** The sync itself: false when it didn't run (the choice not made yet,
     * or another run already going, which then goes through once more). */
    private suspend fun syncUnlessRunning(): Boolean {
        // First, as it may ask the question below again.
        checkAccess()
        // Signed in but not yet told what to sync (SyncChoice): nothing, the
        // WorkManager runs included, until the screen is answered.
        if (SyncChoice.pending.value) { Log.i(tag, "sync waits for the sync choice"); return false }
        if (!syncing.compareAndSet(false, true)) {
            rerunWanted = true
            Log.i(tag, "sync already running: it goes through once more at its end")
            return false
        }
        val job = currentCoroutineContext()[Job]
        currentSync = job
        val gen = generation
        rerunWanted = false
        // Issue #190: no route switch while a sync runs - except between its
        // chunks (IOS-10, transferBreak).
        OTCConnection.transferStarted()
        try {
            var ran = false
            while (true) {
                val end = onePass(gen)
                if (end != PassEnd.Skipped) ran = true
                if (end != PassEnd.Done || !currentCoroutineContext().isActive || !rerunWanted) break
                rerunWanted = false
                Log.i(tag, "sync once more: asked for while it ran")
            }
            return ran
        } finally {
            // Before releasing the flag, so the next run's handle isn't wiped.
            if (currentSync === job) currentSync = null
            OTCConnection.transferEnded()
            rerunWanted = false
            syncing.set(false)
        }
    }

    /** One pass: what holds it up first, then the sync. */
    private suspend fun onePass(gen: Int): PassEnd {
        if (SyncChoice.pending.value) return PassEnd.Skipped
        if (!hasPermission()) { Log.w(tag, "no media permission, sync skipped"); return PassEnd.Skipped }
        val secrets = SecretsStore.loadOrCreate()
        // No device yet (the welcome screen): nothing.
        if (!secrets.isConfigured) return PassEnd.Skipped
        // A1: "Wi-Fi only" on mobile data or a hotspot: nothing until the
        // phone is on Wi-Fi again (watchCost, or WorkManager's UNMETERED run).
        if (waitsForWifi(secrets.wifiOnly.value, NetworkWatch.syncMetered())) {
            Log.i(tag, "sync waits for Wi-Fi")
            return finish(PassEnd.WaitingForWifi, gen)
        }
        // The device said it was full a short while ago: not read and sent
        // to be refused again (Sync All or Sync From Now try at once).
        if (waitsAfterFull(fullSinceMs, System.currentTimeMillis())) {
            Log.i(tag, "sync waits: the device was full a short while ago")
            return PassEnd.DeviceFull(fullWords)
        }
        val wgen = watermarkGen
        val cacheEpoch = AssetSyncCache.epoch()
        try {
            return finish(syncOnce(secrets, gen, wgen, cacheEpoch), gen)
        } finally {
            // Also when stopped (WorkManager, a dropped device): what was
            // hashed isn't hashed again. Not after Log Out wiped it.
            if (gen == generation) AssetSyncCache.flush(cacheEpoch)
        }
    }

    /** What a pass that ended leaves: Settings' Uploads line, and what starts or holds the next run. */
    private fun finish(end: PassEnd, gen: Int): PassEnd {
        if (gen != generation) return end
        when (end) {
            PassEnd.WaitingForWifi -> { waitingForWifi = true; UploadModel.halted(SyncHalt.WaitingForWifi) }
            is PassEnd.DeviceFull -> { fullSinceMs = System.currentTimeMillis(); fullWords = end.words; UploadModel.halted(SyncHalt.DeviceFull(end.words)) }
            PassEnd.Done -> { waitingForWifi = false; fullSinceMs = 0L; UploadModel.halted(null) }
            PassEnd.CutShort -> UploadModel.halted(null)
            PassEnd.Skipped -> {}
        }
        return end
    }

    private suspend fun syncOnce(secrets: SecretsStore, gen: Int, wgen: Int, cacheEpoch: Int): PassEnd {
        OTCConnection.ensureConnected()
        // The watermark never passes the second the query ran in: media
        // added while the run goes on (or in that same second) is the
        // next run's. Writing "now" at the end used to skip it for good.
        val startedSec = System.currentTimeMillis() / 1000
        // A watermark ahead of the clock (set while it was wrong) would
        // hide everything taken until then; the old "now" undid that too.
        if (lastSyncMs / 1000 > startedSec) runWatermark((startedSec - 1) * 1000, gen, wgen)
        val includeVideos = secrets.includeVideos.value
        // Settings > Sync Options > Folders: a folder turned off never
        // comes back from a query; nor does what the viewer's Save wrote (A4).
        val skipped = secrets.skippedFolders.value
        val assets = fetchNewAssets(includeVideos, lastSyncMs, skipped = skipped, leaveOutSaved = true)
        val freshIds = assets.mapTo(HashSet()) { it.id }
        // A5: what earlier runs couldn't finish, which the watermark has
        // moved past: after the new ones, so one that keeps failing (a large
        // video) no longer holds them up. Gone from the phone, or saved here
        // from the device: off the list. In a folder turned off, or a video
        // while Include videos is off: left on it for later.
        val pendingIds = AssetSyncCache.pending.all()
        val found = fetchByIds(pendingIds)
        val foundIds = found.filterNot { it.saved }.mapTo(HashSet()) { it.id }
        val skippedBuckets = PhotoFolders.bucketIds(skipped)
        synchronized(watermarkLock) { if (gen == generation) AssetSyncCache.pending.resolve(pendingIds - foundIds) }
        val retried = found.filter {
            it.id in foundIds && it.id !in freshIds && (includeVideos || !it.isVideo) && (it.bucketId == null || it.bucketId !in skippedBuckets)
        }
        // Before the progress starts, so its total covers both.
        val taken = backfill()
        val pass = oneOffPass(taken, includeVideos, skipped, assets, gen, wgen)
        val retriedIds = retried.mapTo(HashSet()) { it.id }
        val oneOff = pass.assets.filter { it.id !in retriedIds }
        Log.i(tag, "sync start: ${assets.size} new asset(s) since $lastSyncMs, ${retried.size} to retry, ${oneOff.size} from a one-off pass")
        val total = assets.size + retried.size + oneOff.size
        UploadModel.begin(total)
        val targetDir = "/android/${secrets.deviceId.value}/"
        // A9: only for a big run, or a phone that has synced nothing yet
        // (needsListing) - every resume and new photo starts a run, almost
        // always with a photo or two, and the listing holds every photo this
        // phone ever synced. A small run goes by each asset's hash instead.
        val known: Map<String, PbFile> = if (!needsListing(total, AssetSyncCache.isEmpty())) emptyMap() else try {
            val resp = OTCConnection.request { it.setReqListFiles(ListFiles.newBuilder().setPath(targetDir)) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) resp.respListOfFiles.filesList.associateBy { it.path } else emptyMap()
        } catch (e: CancellationException) { throw e } catch (e: Exception) { emptyMap() }

        var idx = 0
        // Folders turned off while this run went: what of theirs it let
        // go wasn't sent, so a pass queued again for them (turned back
        // on) stays.
        val cutShort = ConcurrentHashMap.newKeySet<String>()
        // A chunk, all at once: what became of each asset.
        suspend fun send(chunk: List<Asset>): List<Sent> = coroutineScope {
            chunk.map { asset ->
                idx += 1; val position = idx
                async {
                    try {
                        // Its folder turned off since the run began: left
                        // alone from now on, as from the next run.
                        val bucket = asset.bucketId?.toString()
                        if (bucket != null && bucket in secrets.skippedFolders.value) { cutShort += bucket; return@async Sent.Done }
                        UploadModel.step(asset.name, position - 1, total)
                        uploadIfNeeded(asset, targetDir, known, cacheEpoch)
                        Sent.Done
                    } catch (e: CancellationException) {
                        throw e // Log Out: no watermark past what was cut short
                    } catch (e: UnreadableAsset) {
                        Log.w(tag, "skipped ${asset.id}, it can't be read: ${logged(e.cause ?: e)}")
                        Sent.Done
                    } catch (e: OTCConnection.RequestError) {
                        // The device has no room (storage_full): the run
                        // stops on it rather than reading asset after asset
                        // to have each refused.
                        if (ErrorCodes.isStorageFull(e.code)) return@async Sent.Full(e.message.orEmpty())
                        Log.w(tag, "upload failed for ${asset.id}: ${logged(e)}")
                        Sent.Retry
                    } catch (e: Exception) {
                        Log.w(tag, "upload failed for ${asset.id}: ${logged(e)}")
                        Sent.Retry
                    }
                }
            }.awaitAll()
        }

        // Between two chunks (IOS-10): the route check put off while the run
        // went is made now - back home, the rest goes over the home network -
        // and "Wi-Fi only" is looked at again (A1).
        suspend fun beforeChunk(): PassEnd? {
            coroutineScope { ensureActive() }
            OTCConnection.transferBreak()
            if (waitsForWifi(secrets.wifiOnly.value, NetworkWatch.syncMetered())) {
                Log.i(tag, "off Wi-Fi with Wi-Fi only on: the sync stops here")
                return PassEnd.WaitingForWifi
            }
            while (UploadModel.isPaused) delay(500)
            return null
        }

        // The device gone after a failure: the rest would only be read and
        // hashed to fail the same way.
        suspend fun deviceGone(): Boolean = try {
            OTCConnection.ensureConnected(); false
        } catch (e: CancellationException) { throw e } catch (e: Exception) {
            Log.w(tag, "sync stopped, the device can't be reached: $e")
            true
        }

        // A5: the retry list before the watermark - no asset is ever behind
        // the watermark without being on the device or on the list - and
        // not into the store Log Out wiped, nor over Sync From Now pressed
        // since the run began (what it skips).
        fun noteOutcomes(chunk: List<Asset>, sent: List<Sent>) = synchronized(watermarkLock) {
            if (gen != generation || wgen != watermarkGen) return@synchronized
            AssetSyncCache.pending.resolve(chunk.filterIndexed { i, _ -> sent[i] == Sent.Done }.map { it.id })
            AssetSyncCache.pending.mark(chunk.filterIndexed { i, _ -> sent[i] != Sent.Done }.map { it.id })
        }

        var end: PassEnd = PassEnd.Done
        // The new ones, moving the watermark.
        for (start in assets.indices step maxConcurrentUploads) {
            val chunk = assets.subList(start, minOf(start + maxConcurrentUploads, assets.size))
            beforeChunk()?.let { end = it }
            if (end != PassEnd.Done) break
            val sent = send(chunk)
            noteOutcomes(chunk, sent)
            // Assets come in DATE_ADDED order: everything up to doneSec is
            // on the device or on the retry list. A second the next asset
            // shares isn't done yet (an interrupted run resumes inside it).
            val last = chunk.last().addedSec
            val doneSec = if (assets.getOrNull(start + chunk.size)?.addedSec == last) last - 1 else last
            runWatermark(minOf(doneSec, startedSec - 1) * 1000, gen, wgen)
            (sent.firstOrNull { it is Sent.Full } as Sent.Full?)?.let { Log.i(tag, "the device is full: the sync stops here"); end = PassEnd.DeviceFull(it.words) }
            if (end != PassEnd.Done) break
            if (sent.any { it != Sent.Done } && deviceGone()) { end = PassEnd.CutShort; break }
        }
        // The retry list, then the one-off passes: no watermark. A one-off
        // pass is done only once all of it went; otherwise the next run goes
        // through it again, and what already went is found at once.
        if (end == PassEnd.Done) {
            for (start in retried.indices step maxConcurrentUploads) {
                val chunk = retried.subList(start, minOf(start + maxConcurrentUploads, retried.size))
                beforeChunk()?.let { end = it }
                if (end != PassEnd.Done) break
                val sent = send(chunk)
                noteOutcomes(chunk, sent)
                (sent.firstOrNull { it is Sent.Full } as Sent.Full?)?.let { end = PassEnd.DeviceFull(it.words) }
                if (end != PassEnd.Done) break
                if (sent.any { it != Sent.Done } && deviceGone()) { end = PassEnd.CutShort; break }
            }
        }
        if (end == PassEnd.Done) {
            var done = true
            for (start in oneOff.indices step maxConcurrentUploads) {
                val chunk = oneOff.subList(start, minOf(start + maxConcurrentUploads, oneOff.size))
                beforeChunk()?.let { end = it }
                if (end != PassEnd.Done) { done = false; break }
                val sent = send(chunk)
                (sent.firstOrNull { it is Sent.Full } as Sent.Full?)?.let { end = PassEnd.DeviceFull(it.words) }
                if (sent.all { it == Sent.Done }) continue
                done = false
                if (end != PassEnd.Done) break
                if (deviceGone()) { end = PassEnd.CutShort; break }
            }
            if (done) synchronized(watermarkLock) {
                // Not into the store Log Out just wiped.
                if (gen == generation) {
                    if (taken.isNotEmpty()) setBackfill(afterPass(backfill(), taken, finished = true, cutShort = cutShort, limited = pass.limited))
                    // Nor over Sync All or Sync From Now pressed since
                    // the run began: what was picked before that is
                    // behind the owner's choice, not new.
                    if (wgen == watermarkGen) pass.visible?.let(::writeSeen)
                }
            }
        }
        UploadModel.complete()
        Log.i(tag, "sync done: $end")
        return end
    }
}
