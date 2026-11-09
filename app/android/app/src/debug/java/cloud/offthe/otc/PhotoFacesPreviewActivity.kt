// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.commonOpen
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.FileExifInfo
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.People
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.PhotoFace
import cloud.offthe.otc.proto.PhotoFaces
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.FullPhotoStore
import cloud.offthe.otc.ui.gallery.GalleryRequest
import cloud.offthe.otc.ui.gallery.ImageModal
import cloud.offthe.otc.ui.gallery.PersonNav
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import cloud.offthe.otc.ui.gallery.ViewerAlert
import cloud.offthe.otc.ui.theme.OTCTheme
import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.delay

/**
 * Debug builds only: the viewer's Info with its People on sample data, no
 * device needed (adb shell am start -n cloud.offthe.otc/.PhotoFacesPreviewActivity):
 * three pretend photos and a video opened with Info showing - the first
 * with four faces (one unnamed, one without its crop, one with a long
 * name) and someone added by hand, the second with one face, the third
 * with no one (Add a person alone; its list has the field to find a name),
 * the video with one person added by hand. A face's × takes it off (the
 * faces are asked again), one added by hand's without a question; Add a
 * person's list adds whoever is picked. `--es device demo` refuses both
 * as the demo device does, `--es device noadd` is a device before people
 * added by hand (faces, no Add, no one in the video), `--es device old`
 * one before GetPhotoFaces (no People), `--es device off` one with face
 * recognition off. A face tapped closes the viewer, and the person whose
 * photos Images would show is named under Open.
 */
class PhotoFacesPreviewActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        val device = intent.getStringExtra("device") ?: ""
        val library = (0 until 3).map { File.newBuilder().setPath("/Photos/f$it.jpg").setHash("f$it").setMime("image/jpeg").build() } +
            File.newBuilder().setPath("/Photos/clip.mp4").setHash("clip").setMime("video/mp4").build()
        val adds = device != "noadd"
        // People: a dozen, so a list with no one taken out has its field
        // (one without a face of their own: People shows none).
        val names = listOf("Ana", "", "Leo", "María José Fernández de la Vega", "Zoe", "Bruno", "Carmen", "", "Diego", "Elena", "Félix", "Grace")
        val people = names.mapIndexed { i, n ->
            Person.newBuilder().setId("p${i + 1}").setName(n).setFaceCount(40 - i * 3)
                .also { if (i != 5) it.coverThumbnail = ByteString.copyFrom(MemoriesPreviewActivity.picture("face-p${i + 1}", 96, 96)) }.build()
        }
        fun face(id: String, name: String, x: Int, crop: Boolean = true) = PhotoFace.newBuilder().setPersonId(id).setName(name).setX(x).setY(80).setW(60).setH(60)
            .also { if (crop) it.thumbnail = ByteString.copyFrom(MemoriesPreviewActivity.picture("face-$id", 96, 96)) }.build()
        // Added by hand: no box, their People face as the crop.
        fun byHand(id: String): PhotoFace {
            val p = people.first { it.id == id }
            return PhotoFace.newBuilder().setPersonId(id).setName(p.name).setThumbnail(p.coverThumbnail).setByHand(true).build()
        }
        val faces = ConcurrentHashMap<String, List<PhotoFace>>().apply {
            put(library[0].path, listOf(face("p1", "Ana", 40), face("p2", "", 200), face("p3", "Leo", 380, crop = false), face("p4", "María José Fernández de la Vega", 560)) +
                if (adds) listOf(byHand("p5")) else emptyList())
            put(library[1].path, listOf(face("p1", "Ana", 120)))
            if (adds) put(library[3].path, listOf(byHand("p1")))
        }
        val demo = RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo device: changes aren't kept.").build()
        val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
        val send: GalleryRequest = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_GET_PHOTO_FACES -> if (device == "old") unknown else {
                    delay(500)
                    val path = req.reqGetPhotoFaces.path
                    RespEnvelope.newBuilder().setRespPhotoFaces(
                        PhotoFaces.newBuilder().setFaceRecognition(device != "off").setHash(library.first { it.path == path }.hash).setAddsPeople(adds)
                            .addAllFaces(if (device == "off") emptyList() else faces[path].orEmpty()),
                    ).build()
                }
                ReqEnvelope.PayloadCase.REQ_LIST_PEOPLE -> RespEnvelope.newBuilder().setRespPeople(People.newBuilder().addAllPeople(people)).build()
                ReqEnvelope.PayloadCase.REQ_ADD_TO_PERSON -> {
                    delay(700)
                    if (device == "demo") demo
                    else {
                        val r = req.reqAddToPerson
                        for (f in library.filter { it.hash in r.hashesList }) faces.compute(f.path) { _, fs ->
                            if (fs.orEmpty().any { it.personId == r.personId }) fs else fs.orEmpty() + byHand(r.personId)
                        }
                        RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
                    }
                }
                ReqEnvelope.PayloadCase.REQ_REMOVE_FROM_PERSON -> {
                    delay(400)
                    if (device == "demo") demo
                    else {
                        val r = req.reqRemoveFromPerson
                        for (f in library.filter { it.hash in r.hashesList }) faces.computeIfPresent(f.path) { _, fs -> fs.filter { it.personId != r.personId } }
                        RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
                    }
                }
                ReqEnvelope.PayloadCase.REQ_GET_FILE_INFO -> RespEnvelope.newBuilder().setRespFileInfo(
                    FileExifInfo.newBuilder().setCameraMake("Google").setCameraModel("Pixel 8").setWidth(4080).setHeight(3072)
                        .setTakenAt(Timestamp.newBuilder().setSeconds(1_761_990_000)).setFNumber("f/1.7").setIso(120),
                ).build()
                ReqEnvelope.PayloadCase.REQ_GET_FILE -> {
                    val p = req.reqGetFile.path
                    RespEnvelope.newBuilder().setRespFile(
                        File.newBuilder().setPath(p).setMime("image/jpeg").setContent(ByteString.copyFrom(MemoriesPreviewActivity.picture(p, 1200, 900))),
                    ).build()
                }
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.getDefaultInstance()).build()
                else -> unknown
            }
        }
        setContent {
            OTCTheme {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    val vm: PhotoGalleryViewModel = viewModel(key = "preview-faces") {
                        PhotoGalleryViewModel("preview", send = send, thumbs = MemoriesPreviewActivity.SampleThumbs, fullPhotos = NoFullPhotos, scope = lifecycleScope)
                    }
                    val st by vm.state.collectAsState()
                    // The person a face opened (MainView would show their photos).
                    var shown by remember { mutableStateOf("") }
                    val asked by PersonNav.pending.collectAsState()
                    LaunchedEffect(asked) { asked?.let { PersonNav.take(it); shown = it } }
                    // Info a moment after the viewer: a sheet whose window came
                    // with the viewer's would sit under it.
                    var opened by remember { mutableStateOf(0) }
                    val open: () -> Unit = { vm.showFiles(library.map { PhotoGalleryViewModel.Item(it.path, it.path, it.mime, 1, null, hash = it.hash) }, 0) {}; opened++ }
                    LaunchedEffect(Unit) { open() }
                    LaunchedEffect(opened) { if (opened > 0) { delay(600); vm.openInfo() } }
                    Column(Modifier.fillMaxSize().systemBarsPadding(), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
                        Button(onClick = open) { Text(S.commonOpen().resolve()) }
                        Text(shown)
                    }
                    if (st.openIndex != null) ImageModal(vm, st)
                    ViewerAlert(vm, st)
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
