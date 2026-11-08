// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.commonOpen
import cloud.offthe.otc.proto.EditState
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.FullPhotoStore
import cloud.offthe.otc.ui.gallery.CropRect
import cloud.offthe.otc.ui.gallery.GalleryRequest
import cloud.offthe.otc.ui.gallery.ImageModal
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import cloud.offthe.otc.ui.gallery.ViewerAlert
import cloud.offthe.otc.ui.gallery.editedPicture
import cloud.offthe.otc.ui.theme.OTCTheme
import com.google.protobuf.ByteString
import java.io.ByteArrayOutputStream
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.delay

/**
 * Debug builds only: the photo editor and the video cut on sample data, no
 * device needed (adb shell am start -n
 * cloud.offthe.otc/.PhotoEditPreviewActivity): the viewer over three
 * pretend photos - a wide JPEG, an upright HEIC the device holds at two
 * paths (Replace asks about the copies; it comes back as a JPEG), and an
 * edit (Revert to Original) - and three pretend videos: an MP4 held at two
 * paths, a MOV already cut (Revert to Original, a video's words), and a
 * WMV the device can't cut (no Edit). The pretend device takes a moment
 * and edits as the real one does, so the photo comes back turned and
 * cropped (a video's cut keeps its path, with a new hash; the sample plays
 * whole again, the pretend device not cutting it); a copy is listed by its
 * name. The videos play the debug assets' sample (`--es video <uri>` plays
 * another). `--ez editor true` opens the editor at once, `--ei photo N` on
 * the Nth file (3: the MP4); `--es device demo` refuses every change as
 * the demo device does, `--es device old` is a device before photo edits.
 */
class PhotoEditPreviewActivity : ComponentActivity() {
    // A pretend file: its picture's name and size - or, for an edit, what
    // it was made [from], turned and cropped - and the first original
    // Revert puts back.
    private class Held(
        val file: File, val picture: String, val w: Int, val h: Int,
        val from: Held? = null, val turns: Int = 0, val crop: CropRect = CropRect.WHOLE, val original: Held? = null,
    )

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        val device = intent.getStringExtra("device") ?: ""
        val editor = intent.getBooleanExtra("editor", false)
        val start = intent.getIntExtra("photo", 0)
        // What the pretend videos play: the debug assets' sample, 8 s with its seconds counted.
        val video = intent.getStringExtra("video") ?: "asset:///video-cut-sample.mp4"
        fun file(path: String, hash: String, mime: String = "image/jpeg", original: String = "") =
            File.newBuilder().setPath(path).setHash(hash).setMime(mime).setOriginalHash(original).build()
        val held = ConcurrentHashMap<String, Held>()
        val wide = Held(file("/Camera/IMG_0001.JPG", "p1"), "lake", 1600, 1200)
        val upright = Held(file("/Camera/IMG_0002.HEIC", "p2", "image/heic"), "hills", 1200, 1600)
        val first = Held(file("/Camera/IMG_0003.JPG", "p3-original"), "dusk", 1600, 1200)
        val edit = Held(file("/Camera/IMG_0003.JPG", "p3", original = "p3-original"), "dusk", 1600, 1200, from = first, crop = CropRect(0.2, 0.1, 0.6, 0.8), original = first)
        // The videos: their pictures are never drawn (they stream), only named.
        val clip = Held(file("/Camera/VID_0004.MP4", "v4", "video/mp4"), "clip", 320, 180)
        val firstClip = Held(file("/Camera/VID_0005.MOV", "v5-original", "video/quicktime"), "clip-cut", 320, 180)
        val cutClip = Held(file("/Camera/VID_0005.MOV", "v5", "video/quicktime", original = "v5-original"), "clip-cut", 320, 180, original = firstClip)
        val wmv = Held(file("/Camera/VID_0006.WMV", "v6", "video/x-ms-wmv"), "clip-wmv", 320, 180)
        for (h in listOf(wide, upright, edit, clip, cutClip, wmv)) held[h.file.path] = h
        var made = 0
        val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload")
            .setErrorMessage("Your device needs an update for this. Update it in Settings › Device version.").build()
        val demo = RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo device: changes aren't kept.").build()
        fun answer(h: Held) = RespEnvelope.newBuilder().setRespFile(h.file).build()
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_GET_FILE -> {
                    delay(500)
                    val h = held[req.reqGetFile.path]
                    if (h == null) unknown else RespEnvelope.newBuilder().setRespFile(h.file.toBuilder().setContent(ByteString.copyFrom(render(h)))).build()
                }
                ReqEnvelope.PayloadCase.REQ_GET_EDIT_STATE -> if (device == "old") unknown else {
                    delay(300)
                    val h = held[req.reqGetEditState.path]
                    val twice = h?.picture == "hills" || h?.picture == "clip"
                    RespEnvelope.newBuilder().setRespEditState(EditState.newBuilder().setCopies(if (twice) 2 else 1).setEdited(h?.original != null)).build()
                }
                ReqEnvelope.PayloadCase.REQ_EDIT_IMAGE -> {
                    delay(1500)
                    val e = req.reqEditImage
                    val h = held[e.path]
                    when {
                        device == "old" -> unknown
                        device == "demo" -> demo
                        h == null -> RespEnvelope.newBuilder().setError(true).setErrorCode("changed").setErrorMessage("This file has changed since it was shown: refresh and try again.").build()
                        else -> {
                            val crop = if (e.hasCrop()) CropRect(e.crop.x, e.crop.y, e.crop.width, e.crop.height) else CropRect.WHOLE
                            val stem = e.path.substringBeforeLast('.')
                            val path = if (e.asCopy) "$stem (edited).jpg" else "$stem.JPG"
                            val hash = "edit${++made}"
                            // Its original is the first one, through edits of edits.
                            val root = h.original ?: h
                            val next = Held(
                                file(path, hash, original = if (e.asCopy) "" else root.file.hash), h.picture, h.w, h.h,
                                from = h, turns = e.quarterTurns, crop = crop, original = if (e.asCopy) null else root,
                            )
                            if (!e.asCopy) held.remove(e.path)
                            held[path] = next
                            answer(next)
                        }
                    }
                }
                ReqEnvelope.PayloadCase.REQ_EDIT_VIDEO -> {
                    // A long video takes a while on a Pi: Saving… shows.
                    delay(2500)
                    val e = req.reqEditVideo
                    val h = held[e.path]
                    when {
                        device == "old" -> unknown
                        device == "demo" -> demo
                        h == null -> RespEnvelope.newBuilder().setError(true).setErrorCode("changed").setErrorMessage("This file has changed since it was shown: refresh and try again.").build()
                        e.endSecs > 0 && e.endSecs - e.startSecs < 0.5 -> RespEnvelope.newBuilder().setError(true).setErrorCode("too_short").setErrorMessage("The cut is too short: keep at least half a second.").build()
                        else -> {
                            // A cut keeps its path; a copy is "<name> (edited).<ext>".
                            val path = if (e.asCopy) "${e.path.substringBeforeLast('.')} (edited).${e.path.substringAfterLast('.')}" else e.path
                            val root = h.original ?: h
                            val next = Held(
                                file(path, "cut${++made}", h.file.mime, original = if (e.asCopy) "" else root.file.hash), h.picture, h.w, h.h,
                                original = if (e.asCopy) null else root,
                            )
                            held[path] = next
                            answer(next)
                        }
                    }
                }
                ReqEnvelope.PayloadCase.REQ_REVERT_IMAGE -> {
                    delay(1000)
                    val h = held[req.reqRevertImage.path]
                    when {
                        device == "old" -> unknown
                        device == "demo" -> demo
                        h?.original == null -> RespEnvelope.newBuilder().setError(true).setErrorCode("not_edited").setErrorMessage("This photo isn't an edit.").build()
                        else -> {
                            held.remove(h.file.path)
                            held[h.original.file.path] = h.original
                            answer(h.original)
                        }
                    }
                }
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                else -> unknown
            }
        }
        setContent {
            OTCTheme {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    val vm: PhotoGalleryViewModel = viewModel(key = "preview-photo-edit") {
                        PhotoGalleryViewModel(
                            "preview", send = send, thumbs = MemoriesPreviewActivity.SampleThumbs, fullPhotos = NoFullPhotos, scope = lifecycleScope,
                            // Every pretend video streams the sample.
                            streamUrl = { path -> delay(300); video.takeIf { held[path]?.file?.mime?.startsWith("video/") == true } },
                        )
                    }
                    val st by vm.state.collectAsState()
                    // The folder as the pretend device holds it now, copies and edits included.
                    val open = {
                        val items = held.values.map { it.file }.sortedBy { it.path }.map {
                            PhotoGalleryViewModel.Item("${it.path}#${it.hash}", it.path, it.mime, 1, null, hash = it.hash, originalHash = it.originalHash)
                        }
                        vm.showFiles(items, start.coerceIn(0, items.size - 1)) {}
                    }
                    LaunchedEffect(Unit) {
                        open()
                        if (editor) {
                            // Once its full size is in (a video playing), as a person would.
                            while (vm.state.value.hiRes == null && vm.state.value.videoUrl == null) delay(100)
                            vm.openEditor()
                        }
                    }
                    Box(Modifier.fillMaxSize().systemBarsPadding(), contentAlignment = Alignment.Center) {
                        Button(onClick = open) { Text(S.commonOpen().resolve()) }
                    }
                    if (st.openIndex != null) ImageModal(vm, st)
                    ViewerAlert(vm, st)
                }
            }
        }
    }

    // A pretend file's picture: an edit's drawn from what it was made of, as the device would.
    private fun render(h: Held): ByteArray {
        val from = h.from ?: return MemoriesPreviewActivity.picture(h.picture, h.w, h.h)
        val bytes = render(from)
        val src = BitmapFactory.decodeByteArray(bytes, 0, bytes.size) ?: return bytes
        val out = editedPicture(src, h.turns, h.crop, maxSide = 4096) ?: return bytes
        return ByteArrayOutputStream().also { out.compress(Bitmap.CompressFormat.JPEG, 85, it) }.toByteArray()
    }

    // Nothing kept between openings: each photo is "fetched" again.
    private object NoFullPhotos : FullPhotoStore {
        override suspend fun loadFullPhoto(hash: String): ByteArray? = null
        override suspend fun fullPhotoScope() = 0L
        override suspend fun keepFullPhoto(hash: String, bytes: ByteArray, scope: Long) {}
        override suspend fun forgetFullPhoto(hash: String) {}
    }
}
