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
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ImageGroups
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.PhotoDateBucket
import cloud.offthe.otc.proto.PhotoRating
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespInstanceRole
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.ui.gallery.GalleryRequest
import cloud.offthe.otc.ui.gallery.PhotoGalleryView
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import cloud.offthe.otc.ui.theme.OTCTheme
import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.delay

/**
 * Debug builds only: a collection's Rating on sample data, no device needed
 * (adb shell am start -n cloud.offthe.otc/.RatingPreviewActivity): Images
 * over the collection "Wedding" with Rating on - its header's star, Sort by
 * and Show, the tiles' stars and comment marks (one rated through the
 * shared link), and the viewer's stars and comment box. `--ez off true`
 * opens it with Rating off (the header's star turns it on); `--es device
 * demo` refuses every change as the demo device does, `--es device old` is
 * a device before Rating (it needs an update).
 */
class RatingPreviewActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        val device = intent.getStringExtra("device") ?: ""
        var on = !intent.getBooleanExtra("off", false)
        val at = 1_790_000_000L
        val library = (0 until 12).map { i ->
            File.newBuilder().setPath("/Photos/w$i.jpg").setHash("w$i").setMime("image/jpeg")
                .setCreated(Timestamp.newBuilder().setSeconds(at - i * 3_600L)).build()
        }
        val ratings = ConcurrentHashMap<String, PhotoRating>().apply {
            put("/Photos/w0.jpg", PhotoRating.newBuilder().setStars(5).build())
            put("/Photos/w1.jpg", PhotoRating.newBuilder().setStars(4).setComment("A bit lighter, please").setFromLink(true).build())
            put("/Photos/w3.jpg", PhotoRating.newBuilder().setStars(2).build())
            put("/Photos/w5.jpg", PhotoRating.newBuilder().setComment("Crop the left side").build())
            put("/Photos/w6.jpg", PhotoRating.newBuilder().setStars(3).build())
        }
        val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
        val demo = RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo device: changes aren't kept.").build()
        fun group() = ImageGroup.newBuilder().setId("wedding").setName("Wedding").setFileCount(library.size).setRating(on)
            .setCoverThumbnail(ByteString.copyFrom(MemoriesPreviewActivity.picture("w0", 400, 400))).build()
        // The collection as a device lists it: rated while Rating is on, filtered and sorted when asked.
        fun page(sp: SearchPhotos): List<File> {
            var files = library.map { f -> ratings[f.path]?.takeIf { on }?.let { f.toBuilder().setRating(it).build() } ?: f }
            if (on && sp.hasRating()) {
                val r = sp.rating
                files = files.filter { f ->
                    val got = ratings[f.path]
                    when {
                        r.unrated -> (got?.stars ?: 0) == 0
                        r.withComment -> got?.comment?.isNotEmpty() == true
                        else -> (got?.stars ?: 0) >= r.minStars
                    }
                }
            }
            if (on && sp.byRating) files = files.sortedByDescending { ratings[it.path]?.stars ?: 0 }
            return files
        }
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS -> RespEnvelope.newBuilder().setRespImageGroups(ImageGroups.newBuilder().addGroups(group())).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(page(req.reqSearchPhotos))).build()
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
                ReqEnvelope.PayloadCase.REQ_SET_IMAGE_GROUP_RATING -> {
                    delay(400)
                    when (device) {
                        "old" -> unknown
                        "demo" -> demo
                        else -> { on = req.reqSetImageGroupRating.on; RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build() }
                    }
                }
                ReqEnvelope.PayloadCase.REQ_SET_PHOTO_RATING -> {
                    delay(300)
                    when (device) {
                        "old" -> unknown
                        "demo" -> demo
                        else -> {
                            val r = req.reqSetPhotoRating
                            val kept = PhotoRating.newBuilder().setStars(r.stars).setComment(r.comment).build()
                            if (r.stars == 0 && r.comment.isEmpty()) ratings.remove(r.path) else ratings[r.path] = kept
                            RespEnvelope.newBuilder().setRespPhotoRating(kept).build()
                        }
                    }
                }
                ReqEnvelope.PayloadCase.REQ_GET_INSTANCE_ROLE -> RespEnvelope.newBuilder().setRespInstanceRole(RespInstanceRole.newBuilder().setIsPrimary(true)).build()
                else -> unknown
            }
        }
        // PhotoGalleryView takes the activity's "gallery" view model: this one, on the sample device.
        val vm = ViewModelProvider(this, viewModelFactory {
            initializer { PhotoGalleryViewModel("preview", send = send, thumbs = MemoriesPreviewActivity.SampleThumbs, scope = lifecycleScope, ratingHintFirst = { true }) }
        })["gallery", PhotoGalleryViewModel::class.java]
        setContent {
            OTCTheme {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    Box(Modifier.fillMaxSize().systemBarsPadding()) { PhotoGalleryView("preview") }
                    LaunchedEffect(Unit) { vm.openGroup(group()) }
                }
            }
        }
    }
}
