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
 * Debug builds only: the photo editor on sample data, no device needed
 * (adb shell am start -n cloud.offthe.otc/.PhotoEditPreviewActivity): the
 * viewer over three pretend photos - a wide JPEG, an upright HEIC the
 * device holds at two paths (Replace asks about the copies; it comes back
 * as a JPEG), and an edit (Revert to Original). The pretend device takes a
 * moment and edits as the real one does, so the photo comes back turned
 * and cropped; a copy is listed by its name. `--ez editor true` opens the
 * editor at once, `--ei photo N` on the Nth photo; `--es device demo`
 * refuses every change as the demo device does, `--es device old` is a
 * device before photo edits.
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
        fun file(path: String, hash: String, mime: String = "image/jpeg", original: String = "") =
            File.newBuilder().setPath(path).setHash(hash).setMime(mime).setOriginalHash(original).build()
        val held = ConcurrentHashMap<String, Held>()
        val wide = Held(file("/Camera/IMG_0001.JPG", "p1"), "lake", 1600, 1200)
        val upright = Held(file("/Camera/IMG_0002.HEIC", "p2", "image/heic"), "hills", 1200, 1600)
        val first = Held(file("/Camera/IMG_0003.JPG", "p3-original"), "dusk", 1600, 1200)
        val edit = Held(file("/Camera/IMG_0003.JPG", "p3", original = "p3-original"), "dusk", 1600, 1200, from = first, crop = CropRect(0.2, 0.1, 0.6, 0.8), original = first)
        for (h in listOf(wide, upright, edit)) held[h.file.path] = h
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
                    RespEnvelope.newBuilder().setRespEditState(EditState.newBuilder().setCopies(if (h?.picture == "hills") 2 else 1).setEdited(h?.original != null)).build()
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
                        PhotoGalleryViewModel("preview", send = send, thumbs = MemoriesPreviewActivity.SampleThumbs, fullPhotos = NoFullPhotos, scope = lifecycleScope)
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
                            // Once its full size is in, as a person would.
                            while (vm.state.value.hiRes == null) delay(100)
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
