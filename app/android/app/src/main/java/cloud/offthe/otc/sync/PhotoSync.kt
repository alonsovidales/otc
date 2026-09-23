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
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.HasFile
import cloud.offthe.otc.proto.LinkFile
import cloud.offthe.otc.proto.ListFiles
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.UploadFile
import cloud.offthe.otc.ui.common.sha256Hex
import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.launch
import java.util.concurrent.atomic.AtomicBoolean

// Port of PhotoSync.swift: uploads new camera-roll photos/videos to
// /android/<deviceId>/<filename> on the device, hash-first (issue #58),
// three at a time (issue #10), pausable (issue #30), watermarked by the
// newest date synced so far.
object PhotoSync {
    data class Asset(val id: Long, val uri: Uri, val name: String, val mime: String, val dateAddedMs: Long, val isVideo: Boolean)

    private const val maxConcurrentUploads = 3
    private val syncing = AtomicBoolean(false)
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val prefs get() = OTCApp.instance.getSharedPreferences("otc_sync", Context.MODE_PRIVATE)

    var lastSyncMs: Long
        get() = prefs.getLong("lastSyncMs", 0)
        set(v) { prefs.edit().putLong("lastSyncMs", v).apply() }

    fun hasPermission(context: Context = OTCApp.instance): Boolean {
        val imgs = ContextCompat.checkSelfPermission(context, Manifest.permission.READ_MEDIA_IMAGES) == PackageManager.PERMISSION_GRANTED
        val partial = android.os.Build.VERSION.SDK_INT >= 34 &&
            ContextCompat.checkSelfPermission(context, Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED) == PackageManager.PERMISSION_GRANTED
        return imgs || partial
    }

    fun runForegroundAsync() { scope.launch { try { runForeground() } catch (e: Exception) { Log.w(tag, "sync failed: ${e.message}") } } }

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
                out += Asset(id, ContentUris.withAppendedId(base, id), c.getString(1) ?: "file", c.getString(2) ?: "application/octet-stream", taken, isVideo)
            }
        }
        return out
    }

    /**
     * The asset's bytes as they are on disk. Since Android 10 the MediaStore
     * serves a copy with the GPS EXIF tags blanked (0/0 rationals, which the
     * device reads as NaN) unless the app holds ACCESS_MEDIA_LOCATION and
     * asks for the original - issue #127's map needs the real position, the
     * way PhotoKit hands it to the iOS app.
     */
    fun readData(uri: Uri): ByteArray {
        val context = OTCApp.instance
        val src = if (Build.VERSION.SDK_INT >= 29 &&
            ContextCompat.checkSelfPermission(context, Manifest.permission.ACCESS_MEDIA_LOCATION) == PackageManager.PERMISSION_GRANTED
        ) MediaStore.setRequireOriginal(uri) else uri
        return context.contentResolver.openInputStream(src)?.use { it.readBytes() } ?: throw IllegalStateException("cannot read $uri")
    }

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

        var data: ByteArray? = null
        var hash = AssetSyncCache.hash(cacheKey) ?: run { data = readData(asset.uri); sha256Hex(data!!) }
        var already = hasFile(hash)
        if (!already && data == null) { data = readData(asset.uri); hash = sha256Hex(data!!); already = hasFile(hash) }

        val resp = if (already) {
            OTCConnection.request { it.setReqLinkFile(LinkFile.newBuilder().setHash(hash).setPath(path).setForceOverride(false).setCreated(created)) }
        } else {
            OTCConnection.request { it.setReqUploadFile(UploadFile.newBuilder().setPath(path).setContent(ByteString.copyFrom(data!!)).setForceOverride(false).setCreated(created)) }
        }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_FILE) { AssetSyncCache.record(cacheKey, hash); return path }
        throw IllegalStateException(resp.errorMessage.ifEmpty { "Upload failed" })
    }

    private const val tag = "OTC/PhotoSync"

    suspend fun runForeground() {
        if (!syncing.compareAndSet(false, true)) { Log.i(tag, "sync already running"); return }
        try {
            if (!hasPermission()) { Log.w(tag, "no media permission, sync skipped"); return }
            val secrets = SecretsStore.loadOrCreate()
            OTCConnection.ensureConnected()
            val assets = fetchNewAssets(secrets.includeVideos.value, lastSyncMs)
            Log.i(tag, "sync start: ${assets.size} new asset(s) since $lastSyncMs")
            UploadModel.begin(assets.size)
            val targetDir = "/android/${secrets.deviceId.value}/"
            val known = try {
                val resp = OTCConnection.request { it.setReqListFiles(ListFiles.newBuilder().setPath(targetDir)) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) resp.respListOfFiles.filesList.map { it.path }.toSet() else emptySet()
            } catch (e: Exception) { emptySet() }

            var idx = 0
            for (chunk in assets.chunked(maxConcurrentUploads)) {
                coroutineScope { ensureActive() }
                while (UploadModel.isPaused) delay(500)
                coroutineScope {
                    chunk.map { asset ->
                        idx += 1; val position = idx
                        async {
                            try {
                                UploadModel.step(asset.name, position - 1, assets.size)
                                uploadIfNeeded(asset, targetDir, known)
                            } catch (e: Exception) { Log.w(tag, "upload failed for ${asset.name}: ${e.message}") }
                        }
                    }.awaitAll()
                }
                chunk.maxOfOrNull { it.dateAddedMs }?.let { lastSyncMs = it }
            }
            AssetSyncCache.flush()
            UploadModel.complete()
            lastSyncMs = System.currentTimeMillis()
            Log.i(tag, "sync done")
        } finally {
            syncing.set(false)
        }
    }
}
