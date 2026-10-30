// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.graphics.BitmapFactory
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.data.SmartSearchModelState
import cloud.offthe.otc.data.SmartSearchSupport
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosSmartSearchReading
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.People
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespPhotoDateBuckets
import cloud.offthe.otc.proto.Settings
import cloud.offthe.otc.proto.SmartSearchModel
import cloud.offthe.otc.proto.TagsList
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import cloud.offthe.otc.ui.gallery.TopSearchField
import cloud.offthe.otc.ui.gallery.TopSearchPanel
import cloud.offthe.otc.ui.gallery.TopSearchViewModel
import cloud.offthe.otc.ui.gallery.rememberSearchOptions
import cloud.offthe.otc.ui.settings.SmartSearchSection
import cloud.offthe.otc.ui.theme.OTCTheme
import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp

/**
 * Debug builds only: Smart search on sample data, no device needed -
 * `--es show settings` its Settings section (on and off, Off asks first),
 * otherwise Images' search over a pretend library: the real field, panel
 * and view model; a text search answers 9 photos ranked, the device "still
 * reading". `--es model missing|downloading|problem|ready` says where the
 * model is (release 152; Settings starts at missing, the search at ready):
 * turned on from missing, the section's download runs to 100% in a few
 * seconds; the search offers no Smart row until the model is ready.
 */
class SmartSearchPreviewActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        val settings = intent.getStringExtra("show") == "settings"
        val model = intent.getStringExtra("model") ?: if (settings) "missing" else "ready"
        // What a device's Settings would say: the switch on (off while the
        // model is missing), and where the model is.
        SmartSearchSupport.deviceSaid(
            Settings.newBuilder().setSmartSearchEnabled(model != "missing").setSmartSearchModel(sampleModel(model)).build(),
        )
        val library = (0 until 24).map { file("p$it", 1_761_800_000L - it * 86_400L) }
        val found = listOf(3, 17, 8, 21, 1, 12, 5, 19, 10).map { library[it] }
        val send: suspend (String?, Long, (ReqEnvelope.Builder) -> Unit) -> RespEnvelope = { _, _, build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_GET_TAGS -> RespEnvelope.newBuilder().setRespTagsList(TagsList.newBuilder().addAllTags(listOf("beach", "snow", "dog", "mountain", "kids"))).build()
                ReqEnvelope.PayloadCase.REQ_LIST_PEOPLE -> RespEnvelope.newBuilder().setRespPeople(People.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_PHOTO_DATE_BUCKETS -> RespEnvelope.newBuilder().setRespPhotoDateBuckets(RespPhotoDateBuckets.getDefaultInstance()).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> {
                    val text = req.reqSearchPhotos.text.isNotEmpty()
                    RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addAllFiles(if (text) found else library).setSmartSearchReading(text)).build()
                }
                ReqEnvelope.PayloadCase.REQ_GET_THUMBNAILS -> RespEnvelope.newBuilder().setRespListOfFiles(
                    ListOfFiles.newBuilder().addAllFiles(req.reqGetThumbnails.pathsList.map { p ->
                        File.newBuilder().setPath(p).setHash(p).setContent(ByteString.copyFrom(MemoriesPreviewActivity.picture(p, 300, 300))).setThumbnailSmall(true).build()
                    }),
                ).build()
                else -> RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
            }
        }
        setContent {
            OTCTheme {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    if (settings) SettingsPreview(model != "missing") else SearchPreview(send)
                }
            }
        }
    }

    @androidx.compose.runtime.Composable
    private fun SettingsPreview(startOn: Boolean) {
        var on by remember { mutableStateOf(startOn) }
        var asking by remember { mutableStateOf(false) }
        val model by SmartSearchSupport.model.collectAsState()
        // Turned on where the model is missing: a pretend download, 9% a step.
        LaunchedEffect(on) {
            if (!on || model != SmartSearchModelState.Missing) return@LaunchedEffect
            SmartSearchSupport.modelRequested()
            var p = 0
            while (p < 100) {
                delay(700)
                p = minOf(100, p + 9)
                SmartSearchSupport.deviceSaid(Settings.newBuilder().setSmartSearchEnabled(true).setSmartSearchModel(
                    if (p < 100) SmartSearchModel.newBuilder().setDownloading(true).setPercent(p) else SmartSearchModel.newBuilder().setReady(true),
                ).build())
            }
        }
        Column(Modifier.fillMaxSize().systemBarsPadding().verticalScroll(rememberScrollState()).padding(16.dp)) {
            SmartSearchSection(
                on, saving = false, confirmOff = asking, model = model,
                onToggle = { v -> if (v) on = true else asking = true },
                onConfirmOff = { asking = false; on = false }, onCancelOff = { asking = false },
            )
        }
    }

    /** Where the model is, as a device would say it. */
    private fun sampleModel(name: String): SmartSearchModel = when (name) {
        "ready" -> SmartSearchModel.newBuilder().setReady(true)
        "downloading" -> SmartSearchModel.newBuilder().setDownloading(true).setPercent(37)
        "problem" -> SmartSearchModel.newBuilder().setDownloading(true).setPercent(37)
            .setProblem("The download stopped: the device has no internet connection. It tries again in a minute.")
        else -> SmartSearchModel.newBuilder()
    }.build()

    @androidx.compose.runtime.Composable
    private fun SearchPreview(send: suspend (String?, Long, (ReqEnvelope.Builder) -> Unit) -> RespEnvelope) {
        val vm: PhotoGalleryViewModel = viewModel(key = "preview-gallery") {
            PhotoGalleryViewModel("preview", send = send, thumbs = MemoriesPreviewActivity.SampleThumbs, scope = lifecycleScope)
        }
        val st by vm.state.collectAsState()
        val search: TopSearchViewModel = viewModel(key = "preview-search")
        val options = rememberSearchOptions(search, st)
        LaunchedEffect(Unit) { vm.onAppearInitial() }
        Box(Modifier.fillMaxSize().systemBarsPadding()) {
            Column(Modifier.fillMaxSize()) {
                Box(Modifier.background(MaterialTheme.colorScheme.surfaceContainerLow).padding(8.dp)) {
                    TopSearchField(search, vm, st, options, Modifier.fillMaxWidth())
                }
                Box(Modifier.fillMaxSize()) {
                    Column {
                        if (st.smartText != null && st.smartReading) Text(
                            S.appPhotosSmartSearchReading().resolve(), style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 12.dp, end = 12.dp, top = 12.dp),
                        )
                        LazyVerticalGrid(GridCells.Fixed(4), Modifier.fillMaxSize().padding(12.dp)) {
                            items(st.items, key = { it.id }) { item ->
                                val bmp = remember(item.path) { MemoriesPreviewActivity.picture(item.path, 300, 300).let { BitmapFactory.decodeByteArray(it, 0, it.size) } }
                                Image(bmp.asImageBitmap(), null, Modifier.aspectRatio(1f).padding(1.dp), contentScale = ContentScale.Crop)
                            }
                        }
                    }
                    TopSearchPanel(search, vm, st, options)
                }
            }
        }
    }

    private fun file(name: String, seconds: Long) = File.newBuilder().setPath("/Photos/$name.jpg").setHash(name).setMime("image/jpeg")
        .setCreated(Timestamp.newBuilder().setSeconds(seconds)).setSize(1000).build()
}
