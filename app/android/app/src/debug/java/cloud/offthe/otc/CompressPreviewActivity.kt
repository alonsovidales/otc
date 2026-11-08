// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.net.byteSize
import cloud.offthe.otc.proto.CompressChoice
import cloud.offthe.otc.proto.CompressItem
import cloud.offthe.otc.proto.CompressJob
import cloud.offthe.otc.proto.CompressOption
import cloud.offthe.otc.proto.CompressOptions
import cloud.offthe.otc.proto.CompressResult
import cloud.offthe.otc.proto.EditState
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.FileExifInfo
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.SelectionActionBar
import cloud.offthe.otc.ui.files.CompressDialog
import cloud.offthe.otc.ui.files.CompressModel
import cloud.offthe.otc.ui.files.CompressRules
import cloud.offthe.otc.ui.files.LargestFilesModel
import cloud.offthe.otc.ui.files.LargestFilesView
import cloud.offthe.otc.ui.gallery.GalleryRequest
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import cloud.offthe.otc.ui.settings.LargestFilesSection
import cloud.offthe.otc.ui.theme.OTCTheme
import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.util.concurrent.ConcurrentHashMap
import kotlin.math.min
import kotlin.math.roundToLong

/**
 * Debug builds only: Settings › Largest files, its list and the Compress
 * dialog on a pretend device, no device needed (adb shell am start -n
 * cloud.offthe.otc/.CompressPreviewActivity). The device holds 150 files -
 * 4K, 1080p and 720p videos, 48 MP HEICs and JPEGs, big JPEGs, PNGs (one
 * with transparency), PDFs, a ZIP, a photo at three paths and one already
 * compressed (no Revert, Info's note) - listed largest first in pages of
 * 50, their thumbnails drawn. Compress estimates as the device does and
 * then works through the tasks: a video's percent goes up, a PNG comes
 * back as a JPEG, "broken.mov" fails and "tiny.jpg" comes out no smaller.
 * `--es show largest` opens the list at once, `--es show compress` the
 * dialog over every photo and video, `--es show bar` Images' selection bar
 * as it is in the whole library, a collection and a person's photos;
 * `--es device demo` refuses Compress as the demo device does, `--es device
 * old` is a device before Largest files and Compress, `--es device slow`
 * fails the second page once.
 */
class CompressPreviewActivity : ComponentActivity() {
    // A pretend file and what its header says: a video's size, codec,
    // length and frame rate; a photo's pixels and whether it has alpha.
    private class Held(
        val file: File, val w: Int = 0, val h: Int = 0, val codec: String = "", val secs: Double = 0.0, val fps: Double = 30.0,
        val alpha: Boolean = false,
    )

    private val held = ConcurrentHashMap<String, Held>()
    private var device = ""
    private var made = 0
    private var failedOnce = false

    // The job the pretend device works through, one task at a time.
    @Volatile private var job: CompressJob? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        device = intent.getStringExtra("device") ?: ""
        val show = intent.getStringExtra("show") ?: ""
        library()
        val send: GalleryRequest = { _, _, build -> answer(ReqEnvelope.newBuilder().also(build).build()) }
        setContent {
            OTCTheme {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    var largest by rememberSaveable { mutableStateOf(show == "largest") }
                    val model: LargestFilesModel = viewModel(key = "preview-largest") {
                        LargestFilesModel(send, thumbs = MemoriesPreviewActivity.SampleThumbs, scope = lifecycleScope)
                    }
                    val viewer: PhotoGalleryViewModel = viewModel(key = "preview-largest-viewer") {
                        PhotoGalleryViewModel("preview", send = send, thumbs = MemoriesPreviewActivity.SampleThumbs, scope = lifecycleScope)
                    }
                    val compress: CompressModel = viewModel(key = "preview-compress") { CompressModel(send, scope = lifecycleScope) }
                    val dialog: CompressModel = viewModel(key = "preview-compress-dialog") { CompressModel(send, scope = lifecycleScope) }
                    val everything = {
                        dialog.open(held.values.map { it.file }.filter { it.mime.startsWith("image/") || it.mime.startsWith("video/") }
                            .sortedByDescending { it.byteSize }.map { CompressItem.newBuilder().setPath(it.path).setHash(it.hash).build() })
                    }
                    LaunchedEffect(Unit) { if (show == "compress") everything() }
                    Column(
                        Modifier.fillMaxSize().systemBarsPadding().verticalScroll(rememberScrollState()).padding(vertical = 8.dp),
                        verticalArrangement = Arrangement.spacedBy(12.dp),
                    ) {
                        // As Settings shows it, after In progress.
                        LargestFilesSection { largest = true }
                        Button(onClick = everything, modifier = Modifier.padding(horizontal = 16.dp)) { Text("Compress every photo and video") } // i18n-ignore: a debug preview's own control
                        if (show == "bar") ImagesBars()
                    }
                    CompressDialog(dialog)
                    if (largest) LargestFilesView(onClose = { largest = false }, model = model, viewer = viewer, compress = compress)
                }
            }
        }
    }

    // Images' selection bar as it is in the whole library, a collection, and a person's photos (one picked: Use this face too).
    @androidx.compose.runtime.Composable
    private fun ImagesBars() {
        for ((collection, person) in listOf(false to false, true to false, false to true)) {
            Box(Modifier.fillMaxWidth().padding(vertical = 8.dp), contentAlignment = Alignment.Center) {
                SelectionActionBar(
                    count = 1, onShare = {}, onDownload = {}, onDelete = {}, onGroup = {},
                    onUncollect = if (collection) { {} } else null,
                    onNotThisPerson = if (person) { {} } else null, onMoveToPerson = if (person) { {} } else null, onUseFace = if (person) { {} } else null,
                    onGallery = {}, onClear = {}, onCompress = {},
                )
            }
        }
    }

    private fun library() {
        val mb = 1L shl 20
        val t = Timestamp.newBuilder().setSeconds(1_760_000_000).build()
        fun f(path: String, mime: String, size: Long, hash: String = "h${path.hashCode().toUInt().toString(16)}", copies: Int = 1, original: String = "", removed: Boolean = false) =
            File.newBuilder().setPath(path).setMime(mime).setSize64(size).setSize(size.toInt()).setHash(hash).setCopies(copies).setCreated(t)
                .setOriginalHash(original).setOriginalRemoved(removed).build()
        fun add(h: Held) { held[h.file.path] = h }
        add(Held(f("/Camera/VID_4K_HEVC.MOV", "video/quicktime", 2_900 * mb), 3840, 2160, "hevc", 300.0))
        add(Held(f("/Camera/VID_4K_60.MP4", "video/mp4", 3_400 * mb), 3840, 2160, "h264", 180.0, fps = 60.0))
        add(Held(f("/Camera/VID_1080.MP4", "video/mp4", 820 * mb), 1920, 1080, "h264", 400.0))
        add(Held(f("/Camera/VID_PORTRAIT.MOV", "video/quicktime", 1_300 * mb), 2160, 3840, "hevc", 140.0))
        add(Held(f("/Camera/VID_720.MOV", "video/quicktime", 140 * mb), 1280, 720, "hevc", 200.0))
        add(Held(f("/Camera/broken.mov", "video/quicktime", 900 * mb), 3840, 2160, "h264", 120.0))
        add(Held(f("/Camera/old.avi", "video/x-msvideo", 700 * mb), 1920, 1080, "mpeg4", 600.0))
        add(Held(f("/Docs/Manual.pdf", "application/pdf", 260 * mb)))
        add(Held(f("/Backups/phone.zip", "application/zip", 1_800 * mb)))
        add(Held(f("/Music/Live.mp3", "audio/mpeg", 95 * mb)))
        add(Held(f("/Camera/IMG_48MP.HEIC", "image/heic", 9 * mb, copies = 3), 8064, 6048))
        add(Held(f("/Scans/poster.png", "image/png", 38 * mb), 6000, 4000))
        add(Held(f("/Scans/logo.png", "image/png", 22 * mb), 5000, 5000, alpha = true))
        add(Held(f("/Camera/tiny.jpg", "image/jpeg", 6 * mb), 4000, 3000))
        add(Held(f("/Camera/IMG_DONE.JPG", "image/jpeg", 4 * mb, original = "h-original", removed = true), 4000, 3000))
        // Lots of photos to page through: 48 MP JPEGs, big 12 MP JPEGs, small ones.
        for (i in 1..135) {
            val kind = i % 3
            val size = when (kind) { 0 -> (14 + i % 9) * mb; 1 -> (6 + i % 5) * mb; else -> (2 + i % 3) * mb }
            val (w, h) = if (kind == 0) 8000 to 6000 else 4000 to 3000
            add(Held(f("/Camera/2025/IMG_${1000 + i}.JPG", "image/jpeg", size + i * 1000), w, h))
        }
    }

    // What the pretend device answers.
    private suspend fun answer(req: ReqEnvelope): RespEnvelope {
        val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload")
            .setErrorMessage("Your device needs an update for this. Update it in Settings › Device version.").build()
        return when (req.payloadCase) {
            ReqEnvelope.PayloadCase.REQ_LIST_LARGEST_FILES -> if (device == "old") { delay(400); unknown } else {
                delay(500)
                val r = req.reqListLargestFiles
                if (device == "slow" && r.after.isNotEmpty() && !failedOnce) {
                    failedOnce = true
                    return RespEnvelope.newBuilder().setError(true).setErrorMessage("The device is busy; try again in a moment.").build()
                }
                // Largest first, then by hash, cut after the token's (size, hash) as the device does.
                val all = held.values.map { it.file }.sortedWith(compareByDescending<File> { it.byteSize }.thenBy { it.hash })
                val from = if (r.after.isEmpty()) 0 else {
                    val (size, hash) = r.after.split(':').let { (it[0].toLongOrNull() ?: Long.MAX_VALUE) to it.getOrElse(1) { "" } }
                    all.indexOfFirst { it.byteSize < size || (it.byteSize == size && it.hash > hash) }.let { if (it < 0) all.size else it }
                }
                val n = if (r.limit <= 0) 50 else min(r.limit, 200)
                val page = all.drop(from).take(n)
                val token = if (from + page.size < all.size) page.last().let { "${it.byteSize}:${it.hash}" } else ""
                RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(page).setToken(token)).build()
            }
            ReqEnvelope.PayloadCase.REQ_GET_COMPRESS_OPTIONS -> if (device == "old") unknown else {
                delay(900)
                val opts = req.reqGetCompressOptions.itemsList.map { options(it) }
                RespEnvelope.newBuilder().setRespCompressOptions(CompressOptions.newBuilder().addAllItems(opts)).build()
            }
            ReqEnvelope.PayloadCase.REQ_COMPRESS_FILES -> when (device) {
                "old" -> unknown
                "demo" -> RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo device: changes aren't kept.").build()
                else -> {
                    delay(400)
                    start(req.reqCompressFiles.tasksList.map { it.path to it.preset })
                    RespEnvelope.newBuilder().setRespCompressJob(job!!).build()
                }
            }
            ReqEnvelope.PayloadCase.REQ_GET_COMPRESS_JOB -> {
                val j = job
                if (device == "old") unknown
                else if (j == null || (req.reqGetCompressJob.id.isNotEmpty() && req.reqGetCompressJob.id != j.id))
                    RespEnvelope.newBuilder().setError(true).setErrorCode("not_found").setErrorMessage("There is no compress job.").build()
                else RespEnvelope.newBuilder().setRespCompressJob(j).build()
            }
            ReqEnvelope.PayloadCase.REQ_GET_FILE -> {
                delay(300)
                val h = held[req.reqGetFile.path] ?: return unknown
                RespEnvelope.newBuilder().setRespFile(h.file.toBuilder().setContent(ByteString.copyFrom(MemoriesPreviewActivity.picture(h.file.hash, 1200, 900)))).build()
            }
            ReqEnvelope.PayloadCase.REQ_GET_FILE_INFO -> {
                val h = held[req.reqGetFileInfo.path]
                RespEnvelope.newBuilder().setRespFileInfo(FileExifInfo.newBuilder().setWidth(h?.w ?: 0).setHeight(h?.h ?: 0).setCameraMake("Google").setCameraModel("Pixel 8")).build()
            }
            ReqEnvelope.PayloadCase.REQ_GET_EDIT_STATE -> {
                val h = held[req.reqGetEditState.path]
                RespEnvelope.newBuilder().setRespEditState(EditState.newBuilder().setCopies(h?.file?.copies ?: 1).setEdited(h?.file?.originalHash?.isNotEmpty() == true)
                    .setOriginalRemoved(h?.file?.originalRemoved == true)).build()
            }
            ReqEnvelope.PayloadCase.REQ_DEL_FILE -> {
                delay(300)
                held.remove(req.reqDelFile.path)
                RespEnvelope.newBuilder().setRespAck(cloud.offthe.otc.proto.Ack.newBuilder().setOk(true)).build()
            }
            ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
            else -> unknown
        }
    }

    // What the device would offer for an item, as compress.go estimates it.
    private fun options(item: CompressItem): CompressOption {
        val h = held[item.path]
        val b = CompressOption.newBuilder().setPath(item.path).setHash(item.hash).setCopies(1)
        if (h == null || h.file.hash != item.hash) {
            return b.setReasonCode("changed").setReason("This file has changed since it was shown: refresh and try again.").build()
        }
        val f = h.file
        b.setSize(f.byteSize).setCopies(f.copies)
        val choices = ArrayList<CompressChoice>()
        fun offer(preset: String, est: Long) { if (est <= f.byteSize * 0.85) choices += CompressChoice.newBuilder().setPreset(preset).setEstimatedSize(est).build() }
        when {
            f.mime.startsWith("video/") -> {
                if (f.mime !in setOf("video/mp4", "video/quicktime")) return b.setReasonCode("nothing_to_gain").setReason("Your device can't make this file noticeably smaller.").build()
                val shorter = min(h.w, h.h)
                val scale = (h.fps / 30).coerceIn(1.0, 2.0)
                val sourceVideo = f.byteSize * 8 / h.secs - 160_000
                fun est(target: Double) = (h.secs * (min(target * scale, sourceVideo) + 160_000) / 8 * 1.01).roundToLong()
                if (shorter > 1200) offer(CompressRules.VIDEO_1080P, est(6e6))
                if (shorter > 800) offer(CompressRules.VIDEO_720P, est(3e6))
                if (h.codec !in setOf("hevc", "av1", "vp9")) offer(CompressRules.VIDEO_HEVC, est(0.55 * sourceVideo / scale))
            }
            f.mime.startsWith("image/") -> {
                val px = h.w.toDouble() * h.h
                if (px > 13e6) offer(CompressRules.PHOTO_12MP, (12e6 * 0.32).roundToLong())
                if (f.mime == "image/jpeg" && f.byteSize / px > 0.45) offer(CompressRules.JPEG_RESAVE, (px * 0.24).roundToLong())
                if (f.mime == "image/png" && !h.alpha) offer(CompressRules.PNG_TO_JPEG, (px * 0.30).roundToLong())
            }
            else -> return b.setReasonCode("not_media").setReason("Only photos and videos can be compressed.").build()
        }
        if (choices.isEmpty()) return b.setReasonCode("nothing_to_gain").setReason("Your device can't make this file noticeably smaller.").build()
        return b.addAllChoices(choices).build()
    }

    // A job for [tasks] (joining the one that runs), worked through in the background.
    private fun start(tasks: List<Pair<String, String>>) {
        val running = job?.takeIf { !it.finished }
        val results = (running?.resultsList.orEmpty()) + tasks.map { (path, _) -> CompressResult.newBuilder().setPath(path).setState("waiting").build() }
        val presets = tasks.toMap()
        job = CompressJob.newBuilder().setId(running?.id ?: "job${++made}").setTotal(results.size).setDone(running?.done ?: 0).setSaved(running?.saved ?: 0)
            .addAllResults(results).build()
        if (running != null) return
        lifecycleScope.launch {
            while (true) {
                val j = job ?: return@launch
                val at = j.resultsList.indexOfFirst { it.state == "waiting" }
                if (at < 0) { job = j.toBuilder().setFinished(true).build(); return@launch }
                val r = j.resultsList[at]
                val h = held[r.path]
                val preset = presets[r.path] ?: CompressRules.PHOTO_12MP
                val video = preset in CompressRules.VIDEOS
                // A video's percent goes up as it encodes.
                for (p in if (video) (0..100 step 10) else listOf(0)) {
                    job = job!!.toBuilder().setResults(at, r.toBuilder().setState("running").setPercent(p)).build()
                    delay(if (video) 600 else 1200)
                }
                val end = when {
                    h == null || r.path.contains("broken") ->
                        r.toBuilder().setState("failed").setPercent(0).setMessage("Your device couldn't compress this file.").build()
                    r.path.contains("tiny") ->
                        r.toBuilder().setState("skipped").setPercent(0).setMessage("Compressed, it came out no smaller, so the file was left as it was.").build()
                    else -> {
                        val est = options(CompressItem.newBuilder().setPath(r.path).setHash(h.file.hash).build()).choicesList.firstOrNull { it.preset == preset }?.estimatedSize ?: (h.file.byteSize / 2)
                        val path = if (preset == CompressRules.PNG_TO_JPEG) r.path.substringBeforeLast('.') + ".jpg" else r.path
                        val now = h.file.toBuilder().setPath(path).setHash("c${++made}").setMime(if (preset == CompressRules.PNG_TO_JPEG) "image/jpeg" else h.file.mime)
                            .setSize64(est).setSize(est.toInt()).setOriginalHash(h.file.hash).setOriginalRemoved(true).build()
                        held.remove(r.path)
                        val (w, hh) = if (preset == CompressRules.PHOTO_12MP) 4000 to 3000 else if (preset == CompressRules.VIDEO_1080P) 1920 to 1080 else if (preset == CompressRules.VIDEO_720P) 1280 to 720 else h.w to h.h
                        held[path] = Held(now, w, hh, if (video) "hevc" else "", h.secs, h.fps)
                        job = job!!.toBuilder().setSaved(job!!.saved + h.file.byteSize - est).build()
                        r.toBuilder().setState("done").setPercent(100).setFile(now).build()
                    }
                }
                job = job!!.toBuilder().setResults(at, end).setDone(job!!.done + 1).build()
            }
        }
    }
}
