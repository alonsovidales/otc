// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.Modifier
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import cloud.offthe.otc.data.FaceRecognition
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.People
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.PhotoDateBucket
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespInstanceRole
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.SetPersonCover
import cloud.offthe.otc.ui.gallery.GalleryRequest
import cloud.offthe.otc.ui.gallery.PhotoGalleryView
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import cloud.offthe.otc.ui.theme.OTCTheme
import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp
import kotlinx.coroutines.delay

/**
 * Debug builds only: a person's face on sample data, no device needed (adb
 * shell am start -n cloud.offthe.otc/.PersonCoverPreviewActivity): Images
 * over Ana's photos - her face at the top opens its menu (Upload a photo…,
 * Choose automatically once a face was chosen), and Use this face is in
 * the selection bar with one photo picked and in the viewer's ⋮ menu (not
 * for the video). Each choice shows as her face. `--es device demo`
 * refuses as the demo device does, `--es device old` is a device before
 * SetPersonCover (it needs an update). Face recognition is set on here, as
 * a device would say.
 */
class PersonCoverPreviewActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        val device = intent.getStringExtra("device") ?: ""
        val at = 1_790_000_000L
        val library = (0 until 9).map { i ->
            File.newBuilder().setPath("/Photos/p$i.jpg").setHash("p$i").setMime(if (i == 4) "video/mp4" else "image/jpeg")
                .setCreated(Timestamp.newBuilder().setSeconds(at - i * 86_400L)).build()
        }
        val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
        // Ana's face: the device's choice (a picture of p0), one of her photos', or a picture of the owner's own.
        var face: ByteArray = MemoriesPreviewActivity.picture("p0", 160, 160)
        var chosen = false
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_LIST_PEOPLE -> RespEnvelope.newBuilder().setRespPeople(
                    People.newBuilder()
                        .addPeople(Person.newBuilder().setId("p1").setName("Ana").setFaceCount(9).setCoverChosen(chosen).setCoverThumbnail(ByteString.copyFrom(face)))
                        .addPeople(Person.newBuilder().setId("p2").setName("Leo").setFaceCount(4)
                            .setCoverThumbnail(ByteString.copyFrom(MemoriesPreviewActivity.picture("leo", 160, 160)))),
                ).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(library)).build()
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(
                    RespPhotoDateBuckets.newBuilder().addBuckets(PhotoDateBucket.newBuilder().setMonth("2026-09").setCount(library.size)),
                ).build()
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_GET_FILE -> {
                    val p = req.reqGetFile.path
                    RespEnvelope.newBuilder().setRespFile(
                        File.newBuilder().setPath(p).setMime("image/jpeg").setContent(ByteString.copyFrom(MemoriesPreviewActivity.picture(p, 1200, 900))),
                    ).build()
                }
                ReqEnvelope.PayloadCase.REQ_SET_PERSON_COVER -> {
                    delay(600)
                    val c = req.reqSetPersonCover
                    when (device) {
                        "old" -> unknown
                        "demo" -> RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo device: changes aren't kept.").build()
                        else -> {
                            face = when (c.coverCase) {
                                SetPersonCover.CoverCase.HASH -> MemoriesPreviewActivity.picture(c.hash, 160, 160)
                                SetPersonCover.CoverCase.IMAGE -> c.image.toByteArray()
                                else -> MemoriesPreviewActivity.picture("p0", 160, 160)
                            }
                            chosen = c.coverCase != SetPersonCover.CoverCase.COVER_NOT_SET
                            RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
                        }
                    }
                }
                ReqEnvelope.PayloadCase.REQ_GET_INSTANCE_ROLE -> RespEnvelope.newBuilder().setRespInstanceRole(RespInstanceRole.newBuilder().setIsPrimary(true)).build()
                else -> unknown
            }
        }
        FaceRecognition.set(true)
        // PhotoGalleryView takes the activity's "gallery" view model: this one, on the sample device.
        val vm = ViewModelProvider(this, viewModelFactory {
            initializer { PhotoGalleryViewModel("preview", send = send, thumbs = MemoriesPreviewActivity.SampleThumbs, scope = lifecycleScope) }
        })["gallery", PhotoGalleryViewModel::class.java]
        setContent {
            OTCTheme {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    Box(Modifier.fillMaxSize().systemBarsPadding()) { PhotoGalleryView("preview") }
                    LaunchedEffect(Unit) { vm.loadPeople(); vm.showPerson("p1") }
                }
            }
        }
    }
}
