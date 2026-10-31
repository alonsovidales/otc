// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

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
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.Favourites
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ImageGroups
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.FullPhotoStore
import cloud.offthe.otc.ui.gallery.CollectionsView
import cloud.offthe.otc.ui.gallery.GalleryRequest
import cloud.offthe.otc.ui.gallery.ImageModal
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import cloud.offthe.otc.ui.theme.OTCTheme
import com.google.protobuf.ByteString
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.delay

/**
 * Debug builds only: Favourites on sample data, no device needed (adb shell
 * am start -n cloud.offthe.otc/.FavouritesPreviewActivity): the photo
 * viewer's heart over six pretend photos, the second in Favourites - the
 * device takes a moment to say, so the heart's room shows empty first.
 * `--es device demo` refuses every heart as the demo device does, `--es
 * device old` is a device before Favourites (no heart), `--es device
 * silent` one that takes a minute to say (the heart's room, empty); `--es
 * show collections` is the Collections page with an empty Favourites first.
 */
class FavouritesPreviewActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        val device = intent.getStringExtra("device") ?: ""
        val collections = intent.getStringExtra("show") == "collections"
        val library = (0 until 6).map { File.newBuilder().setPath("/Photos/p$it.jpg").setHash("p$it").setMime("image/jpeg").build() }
        val favourites: MutableSet<String> = ConcurrentHashMap.newKeySet<String>().apply { add(library[1].path) }
        val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_GET_FAVOURITES -> if (device == "old") unknown else {
                    delay(if (device == "silent") 60_000 else 700)
                    RespEnvelope.newBuilder().setRespFavourites(Favourites.newBuilder().addAllPaths(req.reqGetFavourites.pathsList.filter { it in favourites })).build()
                }
                ReqEnvelope.PayloadCase.REQ_SET_FAVOURITE -> {
                    delay(400)
                    if (device == "demo") RespEnvelope.newBuilder().setError(true).setErrorCode("demo")
                        .setErrorMessage("This is a demo device: changes aren't kept.").build()
                    else {
                        val s = req.reqSetFavourite
                        if (s.favourite) favourites.addAll(s.pathsList) else favourites.removeAll(s.pathsList.toSet())
                        RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
                    }
                }
                ReqEnvelope.PayloadCase.REQ_GET_FILE -> {
                    val p = req.reqGetFile.path
                    RespEnvelope.newBuilder().setRespFile(
                        File.newBuilder().setPath(p).setMime("image/jpeg").setContent(ByteString.copyFrom(MemoriesPreviewActivity.picture(p, 1200, 900))),
                    ).build()
                }
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS -> RespEnvelope.newBuilder().setRespImageGroups(
                    ImageGroups.newBuilder()
                        .addGroups(ImageGroup.newBuilder().setId("favourites").setName("Favourites").setFavourites(true))
                        .addGroups(ImageGroup.newBuilder().setId("trip").setName("Trip").setFileCount(12)
                            .setCoverThumbnail(ByteString.copyFrom(MemoriesPreviewActivity.picture("trip", 400, 400))))
                        .addGroups(ImageGroup.newBuilder().setId("birthday").setName("Birthday")),
                ).build()
                else -> unknown
            }
        }
        setContent {
            OTCTheme {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    val vm: PhotoGalleryViewModel = viewModel(key = "preview-favourites") {
                        PhotoGalleryViewModel("preview", send = send, thumbs = MemoriesPreviewActivity.SampleThumbs, fullPhotos = NoFullPhotos, scope = lifecycleScope)
                    }
                    if (collections) Box(Modifier.fillMaxSize().systemBarsPadding()) { CollectionsView(vm, onOpen = {}, onShowPhotos = {}) }
                    else {
                        val st by vm.state.collectAsState()
                        val open = { vm.showFiles(library.map { PhotoGalleryViewModel.Item(it.path, it.path, it.mime, 1, null, hash = it.hash) }, 0) {} }
                        LaunchedEffect(Unit) { open() }
                        Box(Modifier.fillMaxSize().systemBarsPadding(), contentAlignment = Alignment.Center) {
                            Button(onClick = open) { Text(S.commonOpen().resolve()) }
                        }
                        if (st.openIndex != null) ImageModal(vm, st)
                    }
                }
            }
        }
    }

    // Nothing kept between openings: each photo is "fetched" again.
    private object NoFullPhotos : FullPhotoStore {
        override suspend fun loadFullPhoto(hash: String): ByteArray? = null
        override suspend fun fullPhotoScope() = 0L
        override suspend fun keepFullPhoto(hash: String, bytes: ByteArray, scope: Long) {}
        override suspend fun forgetFullPhoto(hash: String) {}
    }
}
