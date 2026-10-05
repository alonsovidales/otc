// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import android.Manifest
import android.content.ContentUris
import android.content.Context
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.provider.MediaStore
import android.util.Log
import androidx.core.content.ContextCompat
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.data.UploadModel
import cloud.offthe.otc.net.ChunkedUpload
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.HasFile
import cloud.offthe.otc.proto.LinkFile
import cloud.offthe.otc.proto.ListFiles
import cloud.offthe.otc.proto.RespEnvelope
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
import kotlinx.coroutines.launch
import java.io.InputStream
import java.util.concurrent.atomic.AtomicBoolean

// Port of PhotoSync.swift: uploads new camera-roll photos/videos to
// /android/<deviceId>/<filename> on the device, hash-first (issue #58),
// three at a time (issue #10), pausable (issue #30), watermarked by the
// newest DATE_ADDED synced so far.
object PhotoSync {
    // dateAddedMs is DATE_TAKEN when there is one (the upload's created
    // time); addedSec is DATE_ADDED, the column the watermark is compared to.
    data class Asset(val id: Long, val uri: Uri, val name: String, val mime: String, val dateAddedMs: Long, val isVideo: Boolean, val addedSec: Long = 0)

    private const val maxConcurrentUploads = 3
    private val syncing = AtomicBoolean(false)
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val prefs get() = OTCApp.instance.getSharedPreferences("otc_sync", Context.MODE_PRIVATE)

    var lastSyncMs: Long
        get() = prefs.getLong("lastSyncMs", 0)
        set(v) { prefs.edit().putLong("lastSyncMs", v).apply() }

    // Checks what mediaPermissions() (NewPostPickerView.kt) asks for on this
    // API level: READ_MEDIA_IMAGES doesn't exist before 33, so on Android
    // 10-12 it was always denied and nothing ever synced.
    fun hasPermission(context: Context = OTCApp.instance): Boolean {
        fun granted(p: String) = ContextCompat.checkSelfPermission(context, p) == PackageManager.PERMISSION_GRANTED
        if (Build.VERSION.SDK_INT < 33) return granted(Manifest.permission.READ_EXTERNAL_STORAGE)
        return granted(Manifest.permission.READ_MEDIA_IMAGES) ||
            (Build.VERSION.SDK_INT >= 34 && granted(Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED))
    }

    // The sync in progress, so Log Out can stop it (as PhotoSync.swift's
    // cancel()): every network call in it is a suspension point. Set by the
    // run that owns the sync, so a call that found one already running
    // (every resume during a long sync) can't swap it for a finished no-op;
    // the WorkManager run is covered too.
    @Volatile private var currentSync: Job? = null
    // Bumped by cancel(): a run from before Log Out writes no sync state
    // into the store it just wiped.
    @Volatile private var generation = 0

    fun runForegroundAsync() {
        scope.launch { try { runForeground() } catch (e: Exception) { Log.w(tag, "sync failed: ${e.message}") } }
    }

    /** Log Out: stop the sync in progress. */
    fun cancel() { generation++; currentSync?.cancel() }

    fun fetchNewAssets(includeVideos: Boolean, sinceMs: Long, limit: Int = 0, newestFirst: Boolean = false): List<Asset> {
        val cr = OTCApp.instance.contentResolver
        val proj = arrayOf(MediaStore.Files.FileColumns._ID, MediaStore.Files.FileColumns.DISPLAY_NAME, MediaStore.Files.FileColumns.MIME_TYPE,
            MediaStore.Files.FileColumns.DATE_ADDED, MediaStore.Files.FileColumns.MEDIA_TYPE, MediaStore.Files.FileColumns.DATE_TAKEN)
        val types = if (includeVideos) "(${MediaStore.Files.FileColumns.MEDIA_TYPE_IMAGE},${MediaStore.Files.FileColumns.MEDIA_TYPE_VIDEO})" else "(${MediaStore.Files.FileColumns.MEDIA_TYPE_IMAGE})"
        val sel = "${MediaStore.Files.FileColumns.MEDIA_TYPE} IN $types" + if (sinceMs > 0) " AND ${MediaStore.Files.FileColumns.DATE_ADDED} > ${sinceMs / 1000}" else ""
        val order = "${MediaStore.Files.FileColumns.DATE_ADDED} ${if (newestFirst) "DESC" else "ASC"}" + if (limit > 0) " LIMIT $limit" else ""
        val out = mutableListOf<Asset>()
        cr.query(MediaStore.Files.getContentUri("external"), proj, sel, null, order)?.use { c ->
            while (c.moveToNext()) {
                val id = c.getLong(0); val type = c.getInt(4)
                val isVideo = type == MediaStore.Files.FileColumns.MEDIA_TYPE_VIDEO
                val base = if (isVideo) MediaStore.Video.Media.EXTERNAL_CONTENT_URI else MediaStore.Images.Media.EXTERNAL_CONTENT_URI
                val taken = c.getLong(5).takeIf { it > 0 } ?: c.getLong(3) * 1000
                out += Asset(id, ContentUris.withAppendedId(base, id), c.getString(1) ?: "file", c.getString(2) ?: "application/octet-stream", taken, isVideo, addedSec = c.getLong(3))
            }
        }
        return out
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

    /** Uploads (or links) one asset; returns the server path. Shared with the composer. */
    suspend fun uploadIfNeeded(asset: Asset, targetDir: String, knownPaths: Set<String> = emptySet()): String {
        val cleanName = asset.name.replace("/", "_")
        val path = "$targetDir$cleanName"
        if (path in knownPaths) return path
        val created = timestamp(asset.dateAddedMs)
        val cacheKey = asset.id.toString()

        suspend fun hasFile(h: String): Boolean {
            val r = OTCConnection.request { it.setReqHasFile(HasFile.newBuilder().setHash(h)) }
            return r.payloadCase == RespEnvelope.PayloadCase.RESP_FILE_EXISTS && r.respFileExists.exists
        }

        // Issue #165: hashed as a stream (4 MiB at a time), never read whole.
        var digest: ChunkedUpload.Digest? = null
        var hash = AssetSyncCache.hash(cacheKey) ?: digestOf(asset).also { digest = it }.sha256
        var already = hasFile(hash)
        if (!already && digest == null) { digest = digestOf(asset); hash = digest!!.sha256; already = hasFile(hash) }

        val resp = if (already) {
            OTCConnection.request { it.setReqLinkFile(LinkFile.newBuilder().setHash(hash).setPath(path).setForceOverride(false).setCreated(created)) }
        } else {
            ChunkedUpload.upload(path, digest!!.size, { openData(asset.uri) }, forceOverride = false, created = created, sha256 = hash)
        }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_FILE) { AssetSyncCache.record(cacheKey, hash); return path }
        throw IllegalStateException(resp.errorMessage.ifEmpty { "Upload failed" })
    }

    private const val tag = "OTC/PhotoSync"

    suspend fun runForeground() {
        if (!syncing.compareAndSet(false, true)) { Log.i(tag, "sync already running"); return }
        val job = currentCoroutineContext()[Job]
        currentSync = job
        val gen = generation
        try {
            if (!hasPermission()) { Log.w(tag, "no media permission, sync skipped"); return }
            val secrets = SecretsStore.loadOrCreate()
            OTCConnection.ensureConnected()
            // The watermark never passes the second the query ran in: media
            // added while the run goes on (or in that same second) is the
            // next run's. Writing "now" at the end used to skip it for good.
            val startedSec = System.currentTimeMillis() / 1000
            // A watermark ahead of the clock (set while it was wrong) would
            // hide everything taken until then; the old "now" undid that too.
            if (lastSyncMs / 1000 > startedSec && gen == generation) lastSyncMs = (startedSec - 1) * 1000
            val assets = fetchNewAssets(secrets.includeVideos.value, lastSyncMs)
            Log.i(tag, "sync start: ${assets.size} new asset(s) since $lastSyncMs")
            UploadModel.begin(assets.size)
            val targetDir = "/android/${secrets.deviceId.value}/"
            // Nothing new (most resumes and background runs): no listing of a
            // folder that holds every photo this phone ever synced.
            val known = if (assets.isEmpty()) emptySet() else try {
                val resp = OTCConnection.request { it.setReqListFiles(ListFiles.newBuilder().setPath(targetDir)) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) resp.respListOfFiles.filesList.map { it.path }.toSet() else emptySet()
            } catch (e: CancellationException) { throw e } catch (e: Exception) { emptySet() }

            var idx = 0
            // Set by the first failed upload: the watermark stays just before
            // it, so the next run tries it again (what synced after it is
            // then skipped as known). The run itself carries on.
            var held = false
            for (start in assets.indices step maxConcurrentUploads) {
                val chunk = assets.subList(start, minOf(start + maxConcurrentUploads, assets.size))
                coroutineScope { ensureActive() }
                while (UploadModel.isPaused) delay(500)
                val ok = coroutineScope {
                    chunk.map { asset ->
                        idx += 1; val position = idx
                        async {
                            try {
                                UploadModel.step(asset.name, position - 1, assets.size)
                                uploadIfNeeded(asset, targetDir, known)
                                true
                            } catch (e: CancellationException) {
                                throw e // Log Out: no watermark past what was cut short
                            } catch (e: UnreadableAsset) {
                                Log.w(tag, "skipped ${asset.name}, it can't be read: ${e.message}")
                                true
                            } catch (e: Exception) {
                                Log.w(tag, "upload failed for ${asset.name}: ${e.message}")
                                false
                            }
                        }
                    }.awaitAll()
                }
                val failed = chunk.filterIndexed { i, _ -> !ok[i] }
                if (!held && gen == generation) {
                    // Assets come in DATE_ADDED order: everything up to doneSec
                    // is on the device. A second the next asset shares isn't
                    // done yet (an interrupted run resumes inside it).
                    val doneSec = if (failed.isEmpty()) {
                        val last = chunk.last().addedSec
                        if (assets.getOrNull(start + chunk.size)?.addedSec == last) last - 1 else last
                    } else failed.minOf { it.addedSec } - 1
                    lastSyncMs = minOf(doneSec, startedSec - 1) * 1000
                    held = failed.isNotEmpty()
                }
                if (failed.isNotEmpty()) {
                    // The device gone: the rest would only be read and hashed
                    // to fail the same way.
                    try { OTCConnection.ensureConnected() } catch (e: CancellationException) { throw e } catch (e: Exception) {
                        Log.w(tag, "sync stopped, the device can't be reached: ${e.message}")
                        break
                    }
                }
            }
            UploadModel.complete()
            Log.i(tag, "sync done")
        } finally {
            // Also when stopped (WorkManager, a dropped device): what was
            // hashed isn't hashed again. Not after Log Out wiped it.
            if (gen == generation) AssetSyncCache.flush()
            // Before releasing the flag, so the next run's handle isn't wiped.
            if (currentSync === job) currentSync = null
            syncing.set(false)
        }
    }
}
