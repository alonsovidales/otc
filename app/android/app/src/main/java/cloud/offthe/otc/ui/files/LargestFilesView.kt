// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.PlayCircle
import androidx.compose.material.icons.outlined.Circle
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.Lifecycle
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appFilesLargestEmpty
import cloud.offthe.otc.i18n.appFilesLargestIntro
import cloud.offthe.otc.i18n.appFilesLargestNeedsUpdate
import cloud.offthe.otc.i18n.appFilesLargestTitle
import cloud.offthe.otc.i18n.commonBack
import cloud.offthe.otc.i18n.commonDeselect
import cloud.offthe.otc.i18n.commonLoading
import cloud.offthe.otc.i18n.commonSelect
import cloud.offthe.otc.net.byteSize
import cloud.offthe.otc.ui.common.FileTypeIcon
import cloud.offthe.otc.ui.common.SelectionActionBar
import cloud.offthe.otc.ui.common.TRYING
import cloud.offthe.otc.ui.common.TRY_AGAIN
import cloud.offthe.otc.ui.common.Toast
import cloud.offthe.otc.ui.common.formatBytes
import cloud.offthe.otc.ui.common.rememberTileThumb
import cloud.offthe.otc.ui.gallery.ImageModal
import cloud.offthe.otc.ui.gallery.PhotoGalleryViewModel
import cloud.offthe.otc.ui.gallery.ViewerAlert
import kotlinx.coroutines.awaitCancellation
import cloud.offthe.otc.proto.File as PbFile

// How close to the list's end its next page is asked for, in rows.
private const val NEAR_END_ROWS = 15

// What the list leaves at its foot for the selection bar while it shows.
private val BAR_ROOM = 132.dp

/**
 * Settings › Largest files (LargestFiles.kt): every file on the device,
 * largest first, full screen over Settings - the counterpart of iOS's
 * pushed list. A tap opens a photo or video in the photo viewer (its
 * Edit, Revert, Info and Delete; what it deletes or edits leaves the list
 * or moves in it), anything else as Files opens it; while rows are picked
 * a tap picks, and the bar's Compress opens the Compress dialog. Back
 * unpicks first, then closes. [model], [viewer] and [compress]: the
 * screen's, the debug preview's its own.
 */
@Composable
fun LargestFilesView(
    onClose: () -> Unit,
    model: LargestFilesModel = viewModel(key = "largest") { LargestFilesModel() },
    viewer: PhotoGalleryViewModel = viewModel(key = "largest-viewer") { PhotoGalleryViewModel("") },
    compress: CompressModel = viewModel(key = "compress-largest") { CompressModel() },
) {
    val st by model.state.collectAsState()
    val vst by viewer.state.collectAsState()
    val cst by compress.state.collectAsState()
    val context = LocalContext.current
    val list = rememberLazyListState()
    val selecting = st.selected.isNotEmpty()
    // Through a rotation the list is kept: only a first opening asks.
    LaunchedEffect(Unit) { model.appeared() }
    fun close() {
        model.left()
        onClose()
    }
    // The next page as the list's end comes near - again as each one lands
    // while it still is.
    val nearEnd by remember { derivedStateOf { (list.layoutInfo.visibleItemsInfo.lastOrNull()?.index ?: 0) >= list.layoutInfo.totalItemsCount - NEAR_END_ROWS } }
    LaunchedEffect(nearEnd, st.rows.size, st.loading, st.end) { if (nearEnd) model.nearEnd() }
    // What a compress did: the rows show it at once, and the list is read
    // again for its order once the job is over.
    LaunchedEffect(cst.landed) {
        val landed = cst.landed
        if (landed.isEmpty()) return@LaunchedEffect
        model.compressed(landed)
        compress.taken(landed)
    }
    val job = cst.job
    val over = cst.job != null && cst.over
    LaunchedEffect(job?.id, over) { if (job != null && over && LargestRules.rereadsAfter(job)) model.reread() }
    // While the list is in front, the job is followed with the dialog closed too.
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(lifecycle) {
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            compress.follow()
            try { awaitCancellation() } finally { compress.unfollow() }
        }
    }

    fun tap(f: PbFile) {
        if (selecting) { model.toggle(f.path); return }
        if (!LargestRules.isMedia(f)) { model.open(context, f); return }
        val items = model.viewerItems()
        val at = items.indexOfFirst { it.path == f.path }
        // A delete or an edit there: the list follows.
        viewer.showFiles(items, maxOf(0, at)) { model.viewerChanged(viewer.state.value.items) }
    }

    Dialog(
        onDismissRequest = { if (selecting) model.clearSelection() else close() },
        properties = DialogProperties(usePlatformDefaultWidth = false),
    ) {
        Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
            Box(Modifier.fillMaxSize().systemBarsPadding()) {
                Column(Modifier.fillMaxSize()) {
                    Row(Modifier.fillMaxWidth().padding(4.dp), verticalAlignment = Alignment.CenterVertically) {
                        IconButton(onClick = ::close) { Icon(Icons.AutoMirrored.Filled.ArrowBack, S.commonBack().resolve()) }
                        Text(
                            S.appFilesLargestTitle().resolve(), style = MaterialTheme.typography.titleLarge,
                            modifier = Modifier.weight(1f).semantics { heading() }, maxLines = 1, overflow = TextOverflow.Ellipsis,
                        )
                    }
                    LazyColumn(
                        Modifier.fillMaxSize(), state = list,
                        contentPadding = PaddingValues(bottom = if (selecting) BAR_ROOM else 16.dp),
                    ) {
                        item(key = "intro") {
                            Text(
                                S.appFilesLargestIntro().resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                                modifier = Modifier.padding(start = 16.dp, end = 16.dp, bottom = 8.dp),
                            )
                        }
                        when (st.body) {
                            LargestBody.LOADING -> item(key = "state") { LargestMessage(S.commonLoading().resolve(), spinner = true) }
                            LargestBody.FAILED -> item(key = "state") {
                                LargestMessage(st.problem?.resolve() ?: "") {
                                    TextButton(onClick = model::retry, enabled = !st.loading) { Text((if (st.retrying) TRYING else TRY_AGAIN).resolve()) }
                                }
                            }
                            LargestBody.NEEDS_UPDATE -> item(key = "state") { LargestMessage(S.appFilesLargestNeedsUpdate().resolve()) }
                            LargestBody.EMPTY -> item(key = "state") { LargestMessage(S.appFilesLargestEmpty().resolve()) }
                            LargestBody.ROWS -> {
                                items(st.rows, key = { it.path }) { f ->
                                    LaunchedEffect(f.path, f.hash) { model.rowShown(f) }
                                    DisposableEffect(f.path) { onDispose { model.rowGone(f.path) } }
                                    LargestRow(
                                        f, model, st.thumbKeys[f.path]?.ifEmpty { null }, selected = f.path in st.selected, selecting = selecting,
                                        opening = st.opening == f.path, onTap = { tap(f) }, onPick = { model.pick(f.path) }, onToggle = { model.toggle(f.path) },
                                    )
                                }
                                // The next page on its way, or why it didn't come.
                                when (st.foot) {
                                    LargestFoot.FAILED -> item(key = "foot") {
                                        LargestMessage(st.problem?.resolve() ?: "") {
                                            TextButton(onClick = model::retry, enabled = !st.loading) { Text((if (st.retrying) TRYING else TRY_AGAIN).resolve()) }
                                        }
                                    }
                                    LargestFoot.LOADING -> item(key = "foot") {
                                        Box(Modifier.fillMaxWidth().padding(16.dp), contentAlignment = Alignment.Center) {
                                            CircularProgressIndicator(Modifier.size(24.dp), strokeWidth = 2.dp)
                                        }
                                    }
                                    LargestFoot.NONE -> {}
                                }
                            }
                        }
                    }
                }
                if (selecting) Box(Modifier.align(Alignment.BottomCenter).padding(bottom = 8.dp)) {
                    SelectionActionBar(
                        count = st.selected.size, onShare = null, onDownload = null, onDelete = null,
                        onClear = model::clearSelection,
                        onCompress = { compress.open(model.compressItems()) }, canCompress = st.canCompress,
                    )
                }
                Toast(st.toast?.resolve(), Modifier.align(Alignment.TopCenter))
            }
            CompressDialog(compress, onSent = { model.clearSelection() })
            if (vst.openIndex != null) ImageModal(viewer, vst)
            ViewerAlert(viewer, vst)
        }
    }
}

// The wait, nothing to show, or why there is nothing ([action]: Try again).
@Composable
private fun LargestMessage(text: String, spinner: Boolean = false, action: (@Composable () -> Unit)? = null) {
    Column(Modifier.fillMaxWidth().padding(horizontal = 24.dp, vertical = 32.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        if (spinner) { CircularProgressIndicator(Modifier.size(24.dp), strokeWidth = 2.dp); Spacer(Modifier.height(12.dp)) }
        Text(
            text, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center,
            modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite },
        )
        if (action != null) { Spacer(Modifier.height(8.dp)); action() }
    }
}

/**
 * A row: the circle that picks it, its thumbnail (a photo's or video's
 * small one, else the file type's icon), its name over its folder, and
 * its size - with "in N places" under it for content at several paths.
 * A long press picks it too.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun LargestRow(
    f: PbFile, model: LargestFilesModel, thumbKey: String?, selected: Boolean, selecting: Boolean, opening: Boolean,
    onTap: () -> Unit, onPick: () -> Unit, onToggle: () -> Unit,
) {
    val colors = MaterialTheme.colorScheme
    val name = CompressRules.name(f.path)
    val places = LargestRules.places(f)?.resolve()
    Row(
        Modifier.fillMaxWidth()
            .combinedClickable(
                onClickLabel = if (selecting) (if (selected) S.commonDeselect() else S.commonSelect()).resolve() else null,
                onLongClick = onPick, onClick = onTap,
            )
            .padding(horizontal = 8.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        IconButton(onClick = onToggle, modifier = Modifier.size(36.dp)) {
            Icon(
                if (selected) Icons.Default.CheckCircle else Icons.Outlined.Circle, (if (selected) S.commonDeselect() else S.commonSelect()).resolve(),
                tint = if (selected) colors.primary else colors.onSurfaceVariant,
            )
        }
        Spacer(Modifier.width(4.dp))
        Box(Modifier.size(48.dp).clip(RoundedCornerShape(6.dp)), contentAlignment = Alignment.Center) {
            when {
                opening -> CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                LargestRules.isMedia(f) -> {
                    val side = with(LocalDensity.current) { 48.dp.roundToPx() }
                    val bmp = rememberTileThumb(thumbKey, side, onUndecodable = { thumbKey?.let { model.thumbUndecodable(f.path, it) } }) { thumbKey?.let { model.tileBytes(it) } }
                    Box(Modifier.fillMaxSize().background(colors.onSurface.copy(alpha = 0.08f)))
                    if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
                    if (f.mime.startsWith("video/")) Icon(
                        Icons.Default.PlayCircle, null, Modifier.align(Alignment.BottomEnd).padding(2.dp).size(16.dp), tint = Color.White,
                    )
                }
                else -> FileTypeIcon(name, Modifier.fillMaxSize().padding(4.dp))
            }
        }
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(name, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(
                LargestRules.folder(f.path), style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant,
                maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
        }
        Spacer(Modifier.width(8.dp))
        Column(horizontalAlignment = Alignment.End) {
            Text(formatBytes(f.byteSize), style = MaterialTheme.typography.titleSmall)
            if (places != null) Text(places, style = MaterialTheme.typography.labelSmall, color = colors.onSurfaceVariant)
        }
    }
}
