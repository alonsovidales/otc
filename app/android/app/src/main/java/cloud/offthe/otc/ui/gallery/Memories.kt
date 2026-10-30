// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import android.content.Context
import android.view.accessibility.AccessibilityManager
import androidx.compose.animation.Crossfade
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.displayCutoutPadding
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.itemsIndexed
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Pause
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.FilterChip
import androidx.compose.material3.FilterChipDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameMillis
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appPhotosMemoriesHidden
import cloud.offthe.otc.i18n.appPhotosMemoriesHideDay
import cloud.offthe.otc.i18n.appPhotosMemoriesHideMemory
import cloud.offthe.otc.i18n.appPhotosMemoriesHidePerson
import cloud.offthe.otc.i18n.appPhotosMemoriesHidePhoto
import cloud.offthe.otc.i18n.appPhotosMemoriesNextPhoto
import cloud.offthe.otc.i18n.appPhotosMemoriesOptions
import cloud.offthe.otc.i18n.appPhotosMemoriesPeriodDay
import cloud.offthe.otc.i18n.appPhotosMemoriesPeriodMonth
import cloud.offthe.otc.i18n.appPhotosMemoriesPeriodWeek
import cloud.offthe.otc.i18n.appPhotosMemoriesPhotoPosition
import cloud.offthe.otc.i18n.appPhotosMemoriesPreviousPhoto
import cloud.offthe.otc.i18n.appPhotosMemoriesSeeAll
import cloud.offthe.otc.i18n.appPhotosMemoriesTitleDay
import cloud.offthe.otc.i18n.appPhotosMemoriesTitleMonth
import cloud.offthe.otc.i18n.appPhotosMemoriesTitleWeek
import cloud.offthe.otc.i18n.appPhotosMemoriesUndo
import cloud.offthe.otc.i18n.appPhotosMemoriesYearsAgo
import cloud.offthe.otc.i18n.commonBack
import cloud.offthe.otc.i18n.commonClose
import cloud.offthe.otc.i18n.commonPause
import cloud.offthe.otc.i18n.commonResume
import cloud.offthe.otc.net.byteSize
import cloud.offthe.otc.proto.Memory
import cloud.offthe.otc.proto.MemoryHideKind
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.ui.common.decodeBitmap
import cloud.offthe.otc.ui.common.rememberTileThumb
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.time.ZoneId
import cloud.offthe.otc.proto.File as PbFile

// On this day (Memories), as iOS's Memories.swift: the strip at the top of
// Images, a memory's slideshow, its See all grid and the hide notice. The
// rules are MemoriesModel.kt's.

/** How long each photo of a memory shows before the next. */
private const val STEP_MS = 4_000L
/** How long the notice after a hide stays. */
private const val NOTICE_MS = 6_000L

private fun periodTitle(p: MemoryPeriodChoice?): UiText = when (p) {
    MemoryPeriodChoice.WEEK -> S.appPhotosMemoriesTitleWeek()
    MemoryPeriodChoice.MONTH -> S.appPhotosMemoriesTitleMonth()
    else -> S.appPhotosMemoriesTitleDay()
}

private fun periodLabel(p: MemoryPeriodChoice): UiText = when (p) {
    MemoryPeriodChoice.DAY -> S.appPhotosMemoriesPeriodDay()
    MemoryPeriodChoice.WEEK -> S.appPhotosMemoriesPeriodWeek()
    MemoryPeriodChoice.MONTH -> S.appPhotosMemoriesPeriodMonth()
}

/** "3 years ago · 30 Oct 2023 · Lisbon": a memory's line over its slideshow and its See all grid. */
@Composable
private fun memoryLine(m: Memory): String = listOfNotNull(
    memoryDays(m.key, m.startDate, m.endDate).resolve().ifEmpty { null },
    m.place.ifEmpty { null },
).joinToString(" · ")

/** A file as the viewer takes it (Images' Item), its tile the strip's. */
private fun itemOf(f: PbFile, thumbKey: String?) = PhotoGalleryViewModel.Item(
    "${f.path}#${f.hash}#${f.byteSize}", f.path, f.mime, f.byteSize, thumbKey?.ifEmpty { null },
    created = if (f.hasCreated()) f.created.seconds * 1000 else null, hash = f.hash,
)

/**
 * The strip at the top of Images: its period's title, the Day · Week ·
 * Month choice (a choice without memories can't be picked) and a card per
 * memory - the cover, how long ago, the place. Shown only while
 * [MemoriesState.visible].
 */
@Composable
internal fun MemoriesStrip(model: MemoriesModel, st: MemoriesState, wide: Boolean, pad: Dp, top: Dp, onOpen: (Memory) -> Unit) {
    val cardW = if (wide) 156.dp else 116.dp
    val cardH = if (wide) 204.dp else 152.dp
    Column(Modifier.fillMaxWidth().padding(top = top, bottom = 4.dp)) {
        Row(Modifier.fillMaxWidth().padding(horizontal = pad), verticalAlignment = Alignment.CenterVertically) {
            Text(
                periodTitle(st.period).resolve(), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f), maxLines = 2,
            )
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                for (p in MemoriesRules.ORDER) {
                    // A choice without memories can't be picked, but stays
                    // readable (the default 38% faded into the dark theme).
                    val colors = MaterialTheme.colorScheme
                    FilterChip(
                        selected = st.period == p, enabled = st.has(p),
                        onClick = { model.choose(p) },
                        label = { Text(periodLabel(p).resolve(), style = MaterialTheme.typography.labelMedium) },
                        colors = FilterChipDefaults.filterChipColors(disabledLabelColor = colors.onSurfaceVariant.copy(alpha = 0.6f)),
                        border = FilterChipDefaults.filterChipBorder(
                            enabled = st.has(p), selected = st.period == p, disabledBorderColor = colors.outline.copy(alpha = 0.45f),
                        ),
                    )
                }
            }
        }
        val density = LocalDensity.current
        val sidePx = with(density) { cardH.roundToPx() }
        LazyRow(
            contentPadding = PaddingValues(horizontal = pad, vertical = 8.dp),
            horizontalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            items(st.shown, key = { it.key }) { m ->
                MemoryCard(model, m, st.thumbKeys[m.cover.path], sidePx, Modifier.width(cardW).height(cardH)) { onOpen(m) }
            }
        }
    }
}

@Composable
private fun MemoryCard(model: MemoriesModel, m: Memory, thumbKey: String?, sidePx: Int, modifier: Modifier, onOpen: () -> Unit) {
    val key = thumbKey?.ifEmpty { null }
    val bmp = rememberTileThumb(key, sidePx) { key?.let { model.tileBytes(it) } }
    val years = S.appPhotosMemoriesYearsAgo(m.yearsAgo).resolve()
    val label = listOfNotNull(years, m.place.ifEmpty { null }).joinToString(", ")
    Box(
        modifier.clip(RoundedCornerShape(12.dp)).background(MaterialTheme.colorScheme.onSurface.copy(alpha = 0.08f))
            .clickable(role = Role.Button, onClick = onOpen)
            .clearAndSetSemantics { contentDescription = label; role = Role.Button; onClick { onOpen(); true } },
    ) {
        if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
        Box(Modifier.fillMaxSize().background(Brush.verticalGradient(0.45f to Color.Transparent, 1f to Color.Black.copy(alpha = 0.7f))))
        Column(Modifier.align(Alignment.BottomStart).padding(10.dp)) {
            Text(years, color = Color.White, style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold, maxLines = 2)
            if (m.place.isNotEmpty()) Text(m.place, color = Color.White.copy(alpha = 0.9f), style = MaterialTheme.typography.labelSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

/** Whether TalkBack (or another touch-exploring service) is on: then the slideshow starts paused. */
private fun touchExploring(context: Context): Boolean =
    (context.getSystemService(Context.ACCESSIBILITY_SERVICE) as? AccessibilityManager)?.isTouchExplorationEnabled == true

/**
 * A memory opened: its photos one after the other, 4 s each, like stories -
 * a tap on the right goes on, on the left goes back, holding pauses; the
 * progress segments on top, the line (how long ago, the days, the place) and
 * the named people, Pause, the hide menu and See all. With TalkBack it
 * starts paused, and the sides are buttons. Animations off
 * (ANIMATOR_DURATION_SCALE 0) drop the cross-fade, not the advance, which is
 * timed by frames. [memory] is looked up again in the model's answers by
 * key: hidden meanwhile (here or on another app), it closes.
 */
@Composable
internal fun MemoryViewer(model: MemoriesModel, memory: Memory, people: List<Person>, onClose: () -> Unit, onSeeAll: (Memory) -> Unit) {
    val st by model.state.collectAsState()
    val m = st.answers.values.asSequence().flatMap { it.memoriesList.asSequence() }.firstOrNull { it.key == memory.key }
    LaunchedEffect(m == null, st.answers.size) { if (m == null && st.answers.isNotEmpty()) onClose() }
    val mem = m ?: memory
    val files = mem.filesList
    if (files.isEmpty()) return
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var index by remember { mutableIntStateOf(0) }
    var elapsed by remember { mutableLongStateOf(0L) }
    var held by remember { mutableStateOf(false) }
    var userPaused by remember { mutableStateOf(touchExploring(context)) }
    var menuOpen by remember { mutableStateOf(false) }
    if (index > files.lastIndex) index = files.lastIndex
    val paused = held || userPaused || menuOpen
    fun go(to: Int) {
        index = to.coerceIn(0, files.lastIndex)
        elapsed = 0
    }
    LaunchedEffect(mem.key) { model.noteThumbs(files) }
    // Frames, not an animation: with animations off an animation ends at once.
    LaunchedEffect(index, paused, files.size) {
        if (paused) return@LaunchedEffect
        var last = withFrameMillis { it }
        while (elapsed < STEP_MS) {
            val t = withFrameMillis { it }
            elapsed += t - last
            last = t
        }
        if (index < files.lastIndex) go(index + 1)
    }
    // The next photo's picture, ahead of it.
    LaunchedEffect(index) { files.getOrNull(index + 1)?.let { model.bigThumb(it.path) } }

    Dialog(onDismissRequest = onClose, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        Box(Modifier.fillMaxSize().background(Color.Black)) {
            val f = files[index]
            Crossfade(targetState = f, label = "memory") { shown ->
                MemoryPicture(model, shown, st.thumbKeys[shown.path], Modifier.fillMaxSize())
            }
            // The sides: back a photo on the left third, on otherwise; held, paused.
            Row(Modifier.fillMaxSize().systemBarsPadding()) {
                val prev = S.appPhotosMemoriesPreviousPhoto().resolve()
                val next = S.appPhotosMemoriesNextPhoto().resolve()
                val position = S.appPhotosMemoriesPhotoPosition(index + 1, files.size).resolve()
                Box(
                    Modifier.weight(1f).fillMaxHeight()
                        .pointerInput(index) { detectTapGestures(onPress = { held = true; tryAwaitRelease(); held = false }, onTap = { go(index - 1) }) }
                        .semantics { contentDescription = prev; role = Role.Button; onClick { go(index - 1); true } },
                )
                Box(
                    Modifier.weight(2f).fillMaxHeight()
                        .pointerInput(index) { detectTapGestures(onPress = { held = true; tryAwaitRelease(); held = false }, onTap = { go(index + 1) }) }
                        .semantics { contentDescription = "$position. $next"; role = Role.Button; onClick { go(index + 1); true } },
                )
            }
            Column(Modifier.fillMaxWidth().systemBarsPadding().displayCutoutPadding().padding(top = 8.dp)) {
                Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp).height(3.dp).clearAndSetSemantics {}, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                    files.indices.forEach { i ->
                        val fill = when {
                            i < index -> 1f
                            i == index -> (elapsed.toFloat() / STEP_MS).coerceIn(0f, 1f)
                            else -> 0f
                        }
                        Box(Modifier.weight(1f).fillMaxHeight().clip(RoundedCornerShape(2.dp)).background(Color.White.copy(alpha = 0.35f))) {
                            Box(Modifier.fillMaxHeight().fillMaxWidth(fill).background(Color.White))
                        }
                    }
                }
                Row(Modifier.fillMaxWidth().padding(start = 4.dp, end = 4.dp, top = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                    IconButton(onClick = onClose) { Icon(Icons.Default.Close, S.commonClose().resolve(), tint = Color.White) }
                    Column(Modifier.weight(1f).padding(horizontal = 4.dp)) {
                        Text(S.appPhotosMemoriesYearsAgo(mem.yearsAgo).resolve(), color = Color.White, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
                        Text(memoryLine(mem), color = Color.White.copy(alpha = 0.85f), style = MaterialTheme.typography.bodySmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        if (mem.peopleCount > 0) Text(peopleTitle(mem.peopleList).resolve(), color = Color.White.copy(alpha = 0.85f), style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                    IconButton(onClick = { userPaused = !userPaused }) {
                        if (userPaused) Icon(Icons.Default.PlayArrow, S.commonResume().resolve(), tint = Color.White)
                        else Icon(Icons.Default.Pause, S.commonPause().resolve(), tint = Color.White)
                    }
                    Box {
                        IconButton(onClick = { menuOpen = true }) { Icon(Icons.Default.MoreVert, S.appPhotosMemoriesOptions().resolve(), tint = Color.White) }
                        DropdownMenu(expanded = menuOpen, onDismissRequest = { menuOpen = false }) {
                            fun hide(kind: MemoryHideKind, value: String, closes: Boolean) {
                                menuOpen = false
                                scope.launch { if (model.hide(kind, value, S.appPhotosMemoriesHidden()) && closes) onClose() }
                            }
                            DropdownMenuItem(text = { Text(S.appPhotosMemoriesHidePhoto().resolve()) }, onClick = { hide(MemoryHideKind.MemoryHidePhoto, f.hash, closes = false) })
                            DropdownMenuItem(text = { Text(S.appPhotosMemoriesHideMemory().resolve()) }, onClick = { hide(MemoryHideKind.MemoryHideMemory, mem.key, closes = true) })
                            val day = MemoriesRules.dayOf(f, ZoneId.systemDefault()) ?: mem.startDate
                            DropdownMenuItem(text = { Text(S.appPhotosMemoriesHideDay().resolve()) }, onClick = { hide(MemoryHideKind.MemoryHideDay, day, closes = true) })
                            for (p in MemoriesRules.hideablePeople(mem.peopleList, people)) {
                                DropdownMenuItem(text = { Text(S.appPhotosMemoriesHidePerson(p.name).resolve()) }, onClick = { hide(MemoryHideKind.MemoryHidePerson, p.id, closes = true) })
                            }
                        }
                    }
                }
            }
            Column(Modifier.align(Alignment.BottomCenter).fillMaxWidth().systemBarsPadding().padding(bottom = 16.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                st.notice?.let { MemoriesNoticeBar(model, it, Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) }
                FilledTonalButton(onClick = { userPaused = true; onSeeAll(mem) }) { Text(S.appPhotosMemoriesSeeAll().resolve()) }
            }
        }
    }
}

/** A photo of the slideshow: its big thumbnail once it came, the tile meanwhile (or for good). */
@Composable
private fun MemoryPicture(model: MemoriesModel, f: PbFile, thumbKey: String?, modifier: Modifier) {
    val big by produceState<android.graphics.Bitmap?>(null, f.path) {
        value = model.bigThumb(f.path)?.let { withContext(Dispatchers.Default) { decodeBitmap(it, 2048) } }
    }
    BoxWithConstraints(modifier, contentAlignment = Alignment.Center) {
        val sidePx = with(LocalDensity.current) { minOf(maxWidth, maxHeight).roundToPx() / 2 }
        val key = thumbKey?.ifEmpty { null }
        val tile = rememberTileThumb(key, sidePx.coerceAtLeast(1)) { key?.let { model.tileBytes(it) } }
        val bmp = big ?: tile
        if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Fit)
    }
}

/**
 * See all: the memory's photos in a grid, each opening in the photo viewer
 * (a viewer of its own over a fixed list, as Places' and Files' are).
 */
@Composable
internal fun MemoryGrid(model: MemoriesModel, memory: Memory, onClose: () -> Unit) {
    val st by model.state.collectAsState()
    val viewer: PhotoGalleryViewModel = viewModel(key = "memories-viewer") { PhotoGalleryViewModel("") }
    val vst by viewer.state.collectAsState()
    LaunchedEffect(memory.key) { model.noteThumbs(memory.filesList) }
    Dialog(onDismissRequest = onClose, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
            Column(Modifier.fillMaxSize().systemBarsPadding()) {
                Row(Modifier.fillMaxWidth().padding(4.dp), verticalAlignment = Alignment.CenterVertically) {
                    IconButton(onClick = onClose) { Icon(Icons.AutoMirrored.Filled.ArrowBack, S.commonBack().resolve()) }
                    Column(Modifier.weight(1f)) {
                        Text(S.appPhotosMemoriesYearsAgo(memory.yearsAgo).resolve(), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
                        Text(memoryLine(memory), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    }
                }
                val side = 112.dp
                val sidePx = with(LocalDensity.current) { side.roundToPx() }
                val zone = ZoneId.systemDefault()
                LazyVerticalGrid(GridCells.Adaptive(side), Modifier.fillMaxSize(), contentPadding = PaddingValues(4.dp),
                    horizontalArrangement = Arrangement.spacedBy(3.dp), verticalArrangement = Arrangement.spacedBy(3.dp)) {
                    itemsIndexed(memory.filesList, key = { _, f -> f.path }) { i, f ->
                        val key = st.thumbKeys[f.path]?.ifEmpty { null }
                        val bmp = rememberTileThumb(key, sidePx) { key?.let { model.tileBytes(it) } }
                        val label = tileLabel(f.mime.startsWith("video/"), if (f.hasCreated()) f.created.seconds * 1000 else null, zone).resolve()
                        Box(
                            Modifier.aspectRatio(1f).clip(RoundedCornerShape(2.dp)).background(MaterialTheme.colorScheme.onSurface.copy(alpha = 0.08f))
                                .clickable(role = Role.Button) {
                                    viewer.showFiles(memory.filesList.map { itemOf(it, st.thumbKeys[it.path]) }, i) { model.refresh() }
                                }
                                .semantics { contentDescription = label },
                        ) {
                            if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
                        }
                    }
                }
            }
            if (vst.openIndex != null) ImageModal(viewer, vst)
        }
    }
}

/**
 * The notice after a hide: what was hidden, with Undo, or why it wasn't -
 * a polite live region, gone after 6 s.
 */
@Composable
internal fun MemoriesNoticeBar(model: MemoriesModel, n: MemoriesNotice, modifier: Modifier = Modifier) {
    val scope = rememberCoroutineScope()
    LaunchedEffect(n.id) {
        delay(NOTICE_MS)
        model.dismiss(n.id)
    }
    Surface(modifier, shape = RoundedCornerShape(8.dp), color = MaterialTheme.colorScheme.inverseSurface, shadowElevation = 4.dp) {
        Row(Modifier.padding(start = 16.dp, end = 8.dp).semantics { liveRegion = LiveRegionMode.Polite }, verticalAlignment = Alignment.CenterVertically) {
            Text(n.text.resolve(), color = MaterialTheme.colorScheme.inverseOnSurface, style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.weight(1f, fill = false).padding(vertical = 14.dp))
            if (n.undo != null) {
                Spacer(Modifier.size(8.dp))
                TextButton(onClick = { scope.launch { model.undo(n) } }) {
                    Text(S.appPhotosMemoriesUndo().resolve(), color = MaterialTheme.colorScheme.inversePrimary)
                }
            }
        }
    }
}
