// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.Path
import android.graphics.Shader
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.lifecycle.lifecycleScope
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.Memories
import cloud.offthe.otc.proto.Memory
import cloud.offthe.otc.proto.MemoryPeriod
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.GridThumbStore
import cloud.offthe.otc.ui.common.ThumbKind
import cloud.offthe.otc.ui.common.ThumbPut
import cloud.offthe.otc.ui.gallery.MemoriesModel
import cloud.offthe.otc.ui.gallery.MemoriesStrip
import cloud.offthe.otc.ui.gallery.MemoryGrid
import cloud.offthe.otc.ui.gallery.MemoryViewer
import cloud.offthe.otc.ui.theme.OTCTheme
import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import java.io.ByteArrayOutputStream
import kotlin.math.abs

/**
 * Debug builds only: On this day's strip over a grid, and with `--ez open
 * true` a memory's slideshow, on sample memories and drawn pictures - no
 * device needed (the screenshots in the change that added it).
 */
class MemoriesPreviewActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        val openFirst = intent.getBooleanExtra("open", false)
        val model = MemoriesModel(
            lifecycleScope,
            getMemories = { r -> RespEnvelope.newBuilder().setRespMemories(sample(r.period)).build() },
            hideMemory = { RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build() },
            bigThumbAsk = { path ->
                val f = File.newBuilder().setPath(path).setContent(ByteString.copyFrom(picture(path, 1000, 750))).build()
                RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addFiles(f)).build()
            },
            thumbs = SampleThumbs,
            sendThumbs = { RespEnvelope.getDefaultInstance() },
            pause = {},
        )
        val people = listOf(Person.newBuilder().setId("p1").setName("Ana").build(), Person.newBuilder().setId("p2").setName("Leo").build())
        setContent {
            OTCTheme {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    val st by model.state.collectAsState()
                    var open by remember { mutableStateOf<Memory?>(null) }
                    var seeAll by remember { mutableStateOf<Memory?>(null) }
                    LaunchedEffect(Unit) { model.appeared() }
                    LaunchedEffect(st.shown.isNotEmpty()) { if (openFirst && st.shown.isNotEmpty()) open = st.shown.first() }
                    Column(Modifier.fillMaxSize().systemBarsPadding()) {
                        if (st.visible) MemoriesStrip(model, st, wide = false, pad = 12.dp, top = 12.dp) { open = it }
                        Text(cloud.offthe.otc.ui.gallery.monthTitle("2026-10").resolve(), style = MaterialTheme.typography.titleSmall, modifier = Modifier.padding(start = 12.dp, top = 12.dp, bottom = 8.dp))
                        LazyVerticalGrid(GridCells.Fixed(4), Modifier.fillMaxWidth().padding(horizontal = 12.dp)) {
                            items(24) { i ->
                                Box(Modifier.aspectRatio(1f).padding(1.dp).background(Color(0xFF000000.toInt() or tint("g$i"))))
                            }
                        }
                    }
                    open?.let { m -> MemoryViewer(model, m, people, onClose = { open = null }, onSeeAll = { seeAll = it }) }
                    seeAll?.let { m -> MemoryGrid(model, m, onClose = { seeAll = null }) }
                }
            }
        }
    }

    private fun file(hash: String, seconds: Long) = File.newBuilder().setHash(hash).setPath("/Photos/$hash.jpg").setMime("image/jpeg")
        .setCreated(Timestamp.newBuilder().setSeconds(seconds)).build()

    private fun memory(key: String, years: Int, start: String, end: String, place: String, people: List<String>, n: Int, seconds: Long): Memory {
        val files = (0 until n).map { file("${key.replace(':', '-')}-$it", seconds + it * 600L) }
        return Memory.newBuilder().setKey(key).setYearsAgo(years).setStartDate(start).setEndDate(end).setPlace(place)
            .addAllPeople(people).setCover(files[n / 2]).addAllFiles(files).setPhotoCount(n * 3).build()
    }

    private fun sample(p: MemoryPeriod): Memories = when (p) {
        MemoryPeriod.MemoryDay -> Memories.newBuilder()
            .addMemories(memory("day:2025-10-30", 1, "2025-10-30", "2025-10-30", "Lisbon", listOf("Ana", "Leo"), 8, 1_761_800_000))
            .addMemories(memory("day:2023-10-30", 3, "2023-10-30", "2023-10-30", "Asturias", listOf("Leo"), 6, 1_698_660_000))
            .addMemories(memory("day:2020-10-30", 6, "2020-10-30", "2020-10-30", "", emptyList(), 4, 1_604_052_000))
            .build()
        MemoryPeriod.MemoryWeek -> Memories.newBuilder()
            .addMemories(memory("week:2024-10-27", 2, "2024-10-27", "2024-11-02", "Porto", emptyList(), 10, 1_730_050_000))
            .build()
        else -> Memories.getDefaultInstance()
    }

    private object SampleThumbs : GridThumbStore {
        override suspend fun scope() = 1L
        override suspend fun kinds(hashes: Collection<String>) = hashes.associateWith { ThumbKind.SMALL }
        override suspend fun put(entries: List<ThumbPut>, scope: Long): Map<String, ThumbKind>? = null
        override suspend fun load(key: String): ByteArray = picture(key.substringBefore('#'), 400, 400)
        override suspend fun discard(key: String) {}
        override suspend fun omitsThumbnails() = true
        override suspend fun notePage(omitted: Boolean, scope: Long) {}
        override suspend fun claimReasks(hashes: Collection<String>) = emptySet<String>()
        override suspend fun noteStillBig(hash: String, scope: Long) {}
        override val clears: StateFlow<Int> = MutableStateFlow(0)
    }

    companion object {
        /** A colour from a name, the same each time. */
        fun tint(name: String): Int {
            val h = abs(name.hashCode())
            val hues = intArrayOf(0x4F7CAC, 0xC97B4A, 0x6A9F6E, 0xB5577A, 0x8A6FB8, 0xD2A04A, 0x4AA3A0)
            return hues[h % hues.size]
        }

        /** A drawn landscape (sky, sun, hills), its colours from [name]: a stand-in photo. */
        fun picture(name: String, w: Int, h: Int): ByteArray {
            val bmp = Bitmap.createBitmap(w, h, Bitmap.Config.ARGB_8888)
            val c = Canvas(bmp)
            val top = 0xFF000000.toInt() or tint(name)
            val bottom = 0xFF000000.toInt() or tint(name.reversed())
            c.drawRect(0f, 0f, w.toFloat(), h.toFloat(), Paint().apply { shader = LinearGradient(0f, 0f, 0f, h.toFloat(), top, bottom, Shader.TileMode.CLAMP) })
            c.drawCircle(w * 0.72f, h * 0.3f, minOf(w, h) * 0.11f, Paint().apply { color = 0xFFFFE9B0.toInt(); isAntiAlias = true })
            val hill = Path().apply {
                moveTo(0f, h * 0.75f); quadTo(w * 0.3f, h * 0.5f, w * 0.6f, h * 0.72f); quadTo(w * 0.8f, h * 0.82f, w.toFloat(), h * 0.66f)
                lineTo(w.toFloat(), h.toFloat()); lineTo(0f, h.toFloat()); close()
            }
            c.drawPath(hill, Paint().apply { color = 0xCC1E2A22.toInt(); isAntiAlias = true })
            val out = ByteArrayOutputStream()
            bmp.compress(Bitmap.CompressFormat.JPEG, 85, out)
            return out.toByteArray()
        }
    }
}
