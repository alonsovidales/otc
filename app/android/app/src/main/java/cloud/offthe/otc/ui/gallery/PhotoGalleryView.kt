// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import android.graphics.Bitmap
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.gestures.detectDragGestures
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.lazy.grid.rememberLazyGridState
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowCircleDown
import androidx.compose.material.icons.filled.Book
import androidx.compose.material.icons.filled.Cancel
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.Person
import androidx.compose.material.icons.filled.PlayCircle
import androidx.compose.material.icons.filled.Share
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.media3.common.MediaItem
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.ui.PlayerView
import cloud.offthe.otc.proto.FileExifInfo
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.ui.common.SelectionActionBar
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.decodeBitmap
import kotlinx.coroutines.launch
import java.text.DateFormat
import java.util.Date
import kotlin.math.abs

// Port of PhotoGalleryView (PhotoGallery.swift): chips + tag search,
// the person strip (rename/merge/delete), the group chip and sheet, the
// adaptive grid with long-press selection, the date scrubber overlay,
// the selection bar and the full-screen viewer with share/save/delete
// and the EXIF panel (issue #41).

@Composable
fun rememberThumb(bytes: ByteArray?): Bitmap? = remember(bytes) { bytes?.let { decodeBitmap(it) } }

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun PhotoGalleryView(deviceId: String) {
    val vm: PhotoGalleryViewModel = viewModel(key = "gallery") { PhotoGalleryViewModel(deviceId) }
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var query by remember { mutableStateOf("") }
    var showSuggest by remember { mutableStateOf(false) }
    var showGroups by remember { mutableStateOf(false) }
    var showGroupPicker by remember { mutableStateOf(false) }
    var newGroupName by remember { mutableStateOf<String?>(null) }
    var renameGroupName by remember { mutableStateOf<String?>(null) }
    var confirmDeleteGroup by remember { mutableStateOf(false) }
    var confirmDeleteSelected by remember { mutableStateOf(false) }
    var personPendingDelete by remember { mutableStateOf<String?>(null) }
    var editingPersonName by remember { mutableStateOf("") }
    val gridState = rememberLazyGridState()

    LaunchedEffect(Unit) { vm.onAppearInitial() }

    val suggestions = remember(query, st.tags) {
        val q = query.trim().lowercase()
        if (q.isEmpty()) emptyList() else st.tags.filter { it.lowercase().startsWith(q) }.take(12)
    }
    fun acceptQuery() {
        val q = query.trim()
        if (q.isEmpty()) return
        vm.addChip(if (suggestions.size == 1) suggestions[0] else q)
        query = ""
        showSuggest = false
    }

    Column(Modifier.fillMaxSize()) {
        // Chips + search bar
        Column(Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerLow).padding(vertical = 8.dp)) {
            if (st.chips.isNotEmpty()) {
                Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    st.chips.forEach { chip -> Chip(chip, Color(0x262196F3)) { vm.removeChip(chip) } }
                }
            }
            Row(Modifier.padding(horizontal = 8.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                OTCTextField(
                    value = query, onValueChange = { query = it; showSuggest = true }, singleLine = true, placeholder = { Text("Type a tag…") },
                    keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
                    keyboardActions = KeyboardActions(onSearch = { acceptQuery() }), modifier = Modifier.weight(1f),
                )
                TextButton(onClick = { acceptQuery() }) { Text("Search") }
                IconButton(onClick = { scope.launch { vm.loadGroups() }; showGroups = true }) { Icon(Icons.Default.Book, "Groups") }
            }
            st.activeGroup?.let { g ->
                Row(
                    Modifier.padding(horizontal = 8.dp).background(Color(0x26FF9800), CircleShape).padding(horizontal = 10.dp, vertical = 5.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Icon(Icons.Default.Book, null, Modifier.size(14.dp))
                    Spacer(Modifier.width(6.dp))
                    Text(g.name, Modifier.clickable { renameGroupName = g.name })
                    Text(" · ${g.fileCount}", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Spacer(Modifier.width(6.dp))
                    Icon(Icons.Default.Delete, "Delete group", Modifier.size(16.dp).clickable { confirmDeleteGroup = true }, tint = Color(0xFFE53935))
                    Spacer(Modifier.width(6.dp))
                    Text("×", Modifier.clickable { vm.leaveGroup() })
                }
            }
            if (showSuggest && suggestions.isNotEmpty()) {
                Column(Modifier.padding(horizontal = 8.dp).clip(RoundedCornerShape(8.dp)).background(Color(0x14808080))) {
                    suggestions.forEach { s ->
                        Text(s, Modifier.fillMaxWidth().clickable { vm.addChip(s); query = ""; showSuggest = false }.padding(10.dp, 6.dp))
                    }
                }
            }
            if (st.allPeople.isNotEmpty()) {
                HorizontalDivider(Modifier.padding(horizontal = 8.dp, vertical = 4.dp))
                st.mergeTargetId?.let { tid ->
                    val name = st.allPeople.firstOrNull { it.id == tid }?.name?.ifEmpty { null } ?: "Unnamed"
                    Row(Modifier.padding(horizontal = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text(
                            "Merging into $name — tap another person to merge them in.",
                            style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f),
                        )
                        TextButton(onClick = { vm.setMergeTarget(null) }) { Text("Cancel") }
                    }
                }
                Row(
                    Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 8.dp),
                    horizontalArrangement = Arrangement.spacedBy(12.dp), verticalAlignment = Alignment.Top,
                ) {
                    st.allPeople.forEach { p ->
                        PersonFilterChip(
                            person = p, isSelected = p.id in st.selectedPeople, isMergeTarget = st.mergeTargetId == p.id,
                            isEditing = st.editingPersonId == p.id, editingName = editingPersonName, onEditingNameChange = { editingPersonName = it },
                            onTap = { if (st.mergeTargetId != null) vm.pickMergeTarget(p) else vm.togglePerson(p.id) },
                            onStartRename = { editingPersonName = p.name; vm.startRenamePerson(p) },
                            onCommitRename = { scope.launch { vm.commitRenamePerson(p.id, editingPersonName) } },
                            onMerge = { vm.setMergeTarget(p.id) }, onDelete = { personPendingDelete = p.id },
                        )
                    }
                }
            }
        }

        // Grid + scrubber + selection bar
        Box(Modifier.weight(1f).fillMaxWidth()) {
            LazyVerticalGrid(
                columns = GridCells.Adaptive(120.dp), state = gridState, contentPadding = PaddingValues(10.dp),
                horizontalArrangement = Arrangement.spacedBy(1.dp), verticalArrangement = Arrangement.spacedBy(1.dp), modifier = Modifier.fillMaxSize(),
            ) {
                val ph = st.placeholderCount
                if (ph != null) {
                    items(minOf(ph, 300)) { Box(Modifier.aspectRatio(1f).clip(RoundedCornerShape(8.dp)).background(Color.Black)) }
                } else {
                    items(st.items, key = { it.id }) { item ->
                        LaunchedEffect(item.id) { vm.loadMoreIfNeeded(item) }
                        PhotoTile(
                            item, isSelected = item.path in st.selected, hasSelection = st.selected.isNotEmpty(),
                            onTap = { vm.open(st.items.indexOfFirst { it.path == item.path }) }, onLongPress = { vm.toggleSelect(item.path) },
                        )
                    }
                }
                if (ph == null && st.loading) {
                    item(span = { GridItemSpan(maxLineSpan) }) {
                        Box(Modifier.fillMaxWidth().height(60.dp), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
                    }
                }
            }
            if (st.showScrubber) {
                PhotoDateScrubber(vm, st, Modifier.align(Alignment.CenterEnd).fillMaxHeight(), onEngage = { scope.launch { gridState.scrollToItem(0) } })
            }
            if (st.selected.isNotEmpty()) {
                Box(Modifier.align(Alignment.BottomCenter)) {
                    SelectionActionBar(
                        count = st.selected.size, busy = st.preparing,
                        onShare = { vm.shareSelected(context) }, onDownload = { vm.downloadZip(context) },
                        onDelete = { confirmDeleteSelected = true }, onGroup = { scope.launch { vm.loadGroups() }; showGroupPicker = true },
                    )
                }
            }
        }
    }

    // Sheets & dialogs
    if (showGroups) {
        ModalBottomSheet(onDismissRequest = { showGroups = false }) {
            Column(Modifier.fillMaxWidth().padding(16.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("Groups", style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                    TextButton(onClick = { showGroups = false }) { Text("Done") }
                }
                if (st.groups.isEmpty()) Text("No groups yet — select some pictures and choose Group.", color = MaterialTheme.colorScheme.onSurfaceVariant)
                st.groups.forEach { g ->
                    Row(Modifier.fillMaxWidth().clickable { showGroups = false; vm.openGroup(g) }.padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                        val cover = rememberThumb(if (g.coverThumbnail.isEmpty) null else g.coverThumbnail.toByteArray())
                        if (cover != null) Image(cover.asImageBitmap(), null, Modifier.size(44.dp).clip(RoundedCornerShape(6.dp)), contentScale = ContentScale.Crop)
                        else Box(Modifier.size(44.dp).clip(RoundedCornerShape(6.dp)).background(Color(0x26808080)), contentAlignment = Alignment.Center) { Icon(Icons.Default.Book, null) }
                        Spacer(Modifier.width(12.dp))
                        Column {
                            Text(g.name)
                            Text("${g.fileCount} ${if (g.fileCount == 1) "picture" else "pictures"}", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                }
            }
        }
    }
    if (showGroupPicker) {
        ModalBottomSheet(onDismissRequest = { showGroupPicker = false }) {
            Column(Modifier.fillMaxWidth().padding(16.dp)) {
                Text("Add ${st.selected.size} to a group", style = MaterialTheme.typography.titleMedium)
                st.groups.forEach { g -> TextButton(onClick = { showGroupPicker = false; scope.launch { vm.addSelectionToGroup(g) } }) { Text(g.name) } }
                TextButton(onClick = { showGroupPicker = false; newGroupName = "" }) { Text("New group…") }
            }
        }
    }
    newGroupName?.let { name ->
        TextDialog(
            "New group", "${st.selected.size} ${if (st.selected.size == 1) "picture" else "pictures"} will be added to it.", name, "Group name", "Create",
            onChange = { newGroupName = it }, onConfirm = { newGroupName = null; scope.launch { vm.createGroupFromSelection(name) } }, onDismiss = { newGroupName = null },
        )
    }
    renameGroupName?.let { name ->
        TextDialog(
            "Rename group", null, name, "Group name", "Save",
            onChange = { renameGroupName = it }, onConfirm = { renameGroupName = null; scope.launch { vm.renameActiveGroup(name) } }, onDismiss = { renameGroupName = null },
        )
    }
    if (confirmDeleteGroup) {
        ConfirmDialog(
            "Delete the group \"${st.activeGroup?.name ?: ""}\"?", "The pictures themselves are kept.", "Delete group",
            onConfirm = { confirmDeleteGroup = false; scope.launch { vm.deleteActiveGroup() } }, onDismiss = { confirmDeleteGroup = false },
        )
    }
    if (confirmDeleteSelected) {
        ConfirmDialog(
            "Delete ${st.selected.size} item${if (st.selected.size == 1) "" else "s"}?", null, "Delete",
            onConfirm = { confirmDeleteSelected = false; vm.deleteSelected() }, onDismiss = { confirmDeleteSelected = false },
        )
    }
    personPendingDelete?.let { id ->
        ConfirmDialog(
            "Delete this person?", "This removes every face matched to them — it can't be undone.", "Delete",
            onConfirm = { personPendingDelete = null; scope.launch { vm.deletePerson(id) } }, onDismiss = { personPendingDelete = null },
        )
    }
    st.pendingMerge?.let { m ->
        val s = m.source.name.ifEmpty { "Unnamed" }
        val t = m.target.name.ifEmpty { "Unnamed" }
        ConfirmDialog(
            "Merge $s into $t?", "Every photo of $s will show up under $t instead — this can't be undone.", "Merge",
            onConfirm = { scope.launch { vm.confirmMerge() } }, onDismiss = { vm.cancelMerge() },
        )
    }
    st.alert?.let {
        AlertDialog(onDismissRequest = { vm.dismissAlert() }, text = { Text(it) }, confirmButton = { TextButton(onClick = { vm.dismissAlert() }) { Text("OK") } })
    }

    if (st.openIndex != null) ImageModal(vm, st)
}

@Composable
private fun Chip(text: String, bg: Color, onRemove: () -> Unit) {
    Row(Modifier.background(bg, CircleShape).padding(horizontal = 8.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text)
        Spacer(Modifier.width(6.dp))
        Text("×", Modifier.clickable(onClick = onRemove))
    }
}

@Composable
fun TextDialog(
    title: String, message: String?, value: String, placeholder: String, confirmLabel: String,
    onChange: (String) -> Unit, onConfirm: () -> Unit, onDismiss: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onDismiss, title = { Text(title) },
        text = {
            Column {
                message?.let { Text(it); Spacer(Modifier.height(8.dp)) }
                OTCTextField(value = value, onValueChange = onChange, placeholder = { Text(placeholder) }, singleLine = true)
            }
        },
        confirmButton = { TextButton(onClick = onConfirm, enabled = value.isNotBlank()) { Text(confirmLabel) } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
fun ConfirmDialog(title: String, message: String?, confirmLabel: String, onConfirm: () -> Unit, onDismiss: () -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss, title = { Text(title) }, text = message?.let { m -> { Text(m) } },
        confirmButton = { TextButton(onClick = onConfirm) { Text(confirmLabel, color = Color(0xFFE53935)) } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun PersonFilterChip(
    person: Person, isSelected: Boolean, isMergeTarget: Boolean, isEditing: Boolean, editingName: String, onEditingNameChange: (String) -> Unit,
    onTap: () -> Unit, onStartRename: () -> Unit, onCommitRename: () -> Unit, onMerge: () -> Unit, onDelete: () -> Unit,
) {
    val bmp = rememberThumb(if (person.coverThumbnail.isEmpty) null else person.coverThumbnail.toByteArray())
    val ring = if (isMergeTarget) Color.Red else if (isSelected) MaterialTheme.colorScheme.primary else Color.Transparent
    Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Box(Modifier.size(44.dp).clip(CircleShape).border(3.dp, ring, CircleShape).clickable(onClick = onTap), contentAlignment = Alignment.Center) {
            if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
            else Box(Modifier.fillMaxSize().background(Color(0x33808080)), contentAlignment = Alignment.Center) { Icon(Icons.Default.Person, null) }
        }
        if (isEditing) {
            BasicTextField(
                value = editingName, onValueChange = onEditingNameChange, singleLine = true,
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done), keyboardActions = KeyboardActions(onDone = { onCommitRename() }),
                textStyle = MaterialTheme.typography.labelSmall.copy(color = MaterialTheme.colorScheme.onSurface),
                modifier = Modifier.width(60.dp).background(Color(0x14808080), RoundedCornerShape(4.dp)).padding(2.dp),
            )
        } else {
            Text(
                person.name.ifEmpty { "Unnamed" }, style = MaterialTheme.typography.labelSmall, maxLines = 1, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.width(60.dp).clickable(onClick = onStartRename),
            )
        }
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Icon(Icons.Default.Link, "Merge", Modifier.size(12.dp).clickable(onClick = onMerge), tint = MaterialTheme.colorScheme.onSurfaceVariant)
            Icon(Icons.Default.Delete, "Delete", Modifier.size(12.dp).clickable(onClick = onDelete), tint = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun PhotoTile(item: PhotoGalleryViewModel.Item, isSelected: Boolean, hasSelection: Boolean, onTap: () -> Unit, onLongPress: () -> Unit) {
    val bmp = rememberThumb(item.thumb)
    Box(
        Modifier.aspectRatio(1f).clip(RoundedCornerShape(8.dp)).background(Color(0x1A808080))
            .combinedClickable(onClick = { if (hasSelection) onLongPress() else onTap() }, onLongClick = onLongPress),
    ) {
        if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
        if (item.mime.startsWith("video/")) Icon(Icons.Default.PlayCircle, null, Modifier.align(Alignment.BottomStart).padding(4.dp).size(18.dp), tint = Color.White)
        if (isSelected) {
            Box(Modifier.fillMaxSize().border(3.dp, MaterialTheme.colorScheme.primary, RoundedCornerShape(8.dp)))
            Icon(Icons.Default.CheckCircle, null, Modifier.align(Alignment.TopStart).padding(6.dp), tint = MaterialTheme.colorScheme.primary)
        }
    }
}

/** Issue #77/#113: the Google-Photos-style scrubber on the grid's right edge. */
@Composable
private fun PhotoDateScrubber(vm: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State, modifier: Modifier, onEngage: () -> Unit) {
    val monthNames = listOf("Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec")
    fun label(month: String): String {
        val p = month.split("-")
        val m = p.getOrNull(1)?.toIntOrNull() ?: return month
        return if (m in 1..12) "${monthNames[m - 1]} ${p[0]}" else month
    }
    val density = LocalDensity.current
    BoxWithConstraints(modifier.width(64.dp)) {
        val heightPx = with(density) { maxHeight.toPx() }
        val minGapPx = with(density) { 14.dp.toPx() }
        val engagePx = with(density) { 8.dp.toPx() }
        var engaged by remember { mutableStateOf(false) }
        var startY by remember { mutableStateOf(0f) }
        Box(Modifier.align(Alignment.TopEnd).padding(end = 6.dp).width(3.dp).fillMaxHeight().background(Color(0x26FFFFFF), CircleShape))
        if (st.scrubFrac != null) {
            var lastPx = Float.NEGATIVE_INFINITY
            st.yearTicks.forEach { (year, pct) ->
                val px = heightPx * pct
                if (px - lastPx < minGapPx) return@forEach
                lastPx = px
                Text(
                    year, fontSize = 10.sp, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.align(Alignment.TopEnd).padding(end = 16.dp).offset { IntOffset(0, (px - 6.dp.toPx()).toInt()) },
                )
            }
            val target = st.scrubTarget
            val frac = st.scrubFrac
            if (target != null && frac != null) {
                Text(
                    label(target.month), style = MaterialTheme.typography.labelMedium, fontWeight = FontWeight.Bold,
                    modifier = Modifier.align(Alignment.TopEnd).padding(end = 16.dp).offset { IntOffset(0, (heightPx * frac - 12.dp.toPx()).toInt()) }
                        .background(MaterialTheme.colorScheme.surfaceContainerHigh, RoundedCornerShape(6.dp)).padding(horizontal = 8.dp, vertical = 4.dp),
                )
                Box(Modifier.align(Alignment.TopEnd).offset { IntOffset(0, (heightPx * frac - 5.dp.toPx()).toInt()) }.size(10.dp).background(Color.Yellow, CircleShape))
            }
        }
        Box(
            Modifier.align(Alignment.CenterEnd).width(16.dp).fillMaxHeight().pointerInput(heightPx) {
                detectDragGestures(
                    onDragStart = { startY = it.y; engaged = false },
                    onDrag = { change, _ ->
                        if (!engaged && abs(change.position.y - startY) < engagePx) return@detectDragGestures
                        if (!engaged) { engaged = true; onEngage() }
                        val frac = (change.position.y / heightPx).coerceIn(0f, 1f)
                        vm.setScrubFrac(frac)
                        val cur = vm.state.value
                        if (cur.totalPhotos > 0) {
                            val idx = (frac * cur.totalPhotos).toInt()
                            val bucket = cur.dateBuckets.firstOrNull { idx >= it.start && idx < it.end } ?: cur.dateBuckets.lastOrNull()
                            bucket?.let { vm.setPlaceholderCount(it.count) }
                        }
                    },
                    onDragEnd = {
                        val target = vm.state.value.scrubTarget
                        vm.setScrubFrac(null)
                        if (target != null) vm.jumpToDate(target.month) else vm.setPlaceholderCount(null)
                        engaged = false
                    },
                    onDragCancel = { vm.setScrubFrac(null); vm.setPlaceholderCount(null); engaged = false },
                )
            },
        )
    }
}

/**
 * The full-screen viewer: a pager that slides between photos (the
 * neighbours are drawn from the view model's small full-size cache),
 * pinch to zoom (issue #36), share/save/delete (issue #9), info (issue
 * #41). Mirrors the iOS viewer's strip of previous/current/next.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun ImageModal(vm: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var confirmDelete by remember { mutableStateOf(false) }
    val idx = st.openIndex ?: return
    val pagerState = rememberPagerState(initialPage = idx) { st.items.size }
    // The pager owns the swipe; the view model follows it (so the hi-res
    // fetch and the video start when a page settles), and is followed by
    // it when the index changes for another reason (a delete).
    LaunchedEffect(pagerState.settledPage) { if (pagerState.settledPage != vm.state.value.openIndex) vm.open(pagerState.settledPage) }
    LaunchedEffect(idx) { if (pagerState.currentPage != idx && !pagerState.isScrollInProgress) pagerState.scrollToPage(idx) }

    Dialog(onDismissRequest = { vm.closeModal() }, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        Box(Modifier.fillMaxSize().background(Color.Black)) {
            // Edge to edge, so the toolbar has to step down from under the
            // status bar itself or its buttons can't be tapped.
            Column(Modifier.fillMaxSize().systemBarsPadding()) {
                Row(Modifier.fillMaxWidth().padding(8.dp)) {
                    IconButton(onClick = { vm.openInfo() }) { Icon(Icons.Default.Info, "More info", tint = Color.White) }
                    Spacer(Modifier.weight(1f))
                    IconButton(onClick = { vm.closeModal() }) { Icon(Icons.Default.Cancel, "Close", tint = Color.White) }
                }
                HorizontalPager(state = pagerState, modifier = Modifier.weight(1f).fillMaxWidth(), beyondViewportPageCount = 1) { page ->
                    ViewerPage(vm, st, page, isCurrent = page == idx)
                }
                Row(Modifier.fillMaxWidth().padding(vertical = 12.dp), horizontalArrangement = Arrangement.spacedBy(48.dp, Alignment.CenterHorizontally)) {
                    IconButton(onClick = { scope.launch { vm.shareCurrentPhoto(context) } }) { Icon(Icons.Default.Share, "Share", tint = Color.White) }
                    IconButton(onClick = { scope.launch { vm.saveToPhotos(context) } }) { Icon(Icons.Default.ArrowCircleDown, "Save", tint = Color.White) }
                    IconButton(onClick = { confirmDelete = true }) { Icon(Icons.Default.Delete, "Delete", tint = Color.White) }
                }
            }
        }
    }
    if (confirmDelete) {
        ConfirmDialog("Delete this photo?", null, "Delete Photo", onConfirm = { confirmDelete = false; vm.deleteCurrentPhoto() }, onDismiss = { confirmDelete = false })
    }
    if (st.infoOpen) ModalBottomSheet(onDismissRequest = { vm.closeInfo() }) { FileInfoView(st.infoLoading, st.infoData) }
}

@Composable
private fun ViewerPage(vm: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State, page: Int, isCurrent: Boolean) {
    val context = LocalContext.current
    val item = st.items.getOrNull(page) ?: return
    val video = if (isCurrent) st.videoUrl else null
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        if (video != null) {
            val player = remember(video) { ExoPlayer.Builder(context).build().apply { setMediaItem(MediaItem.fromUri(video)); prepare(); playWhenReady = true } }
            DisposableEffect(video) { onDispose { player.release() } }
            AndroidView(factory = { PlayerView(it).apply { this.player = player; useController = true } }, modifier = Modifier.fillMaxSize())
            return@Box
        }
        val image = st.hiResImages[item.path] ?: rememberThumb(item.thumb)
        if (image == null) { CircularProgressIndicator(color = Color.White); return@Box }
        // Pinch to zoom; the pager keeps the horizontal swipe, so the zoom
        // snaps back when the page changes.
        var scale by remember(page) { mutableStateOf(1f) }
        var pan by remember(page) { mutableStateOf(Offset.Zero) }
        Image(
            image.asImageBitmap(), null, contentScale = ContentScale.Fit,
            modifier = Modifier.fillMaxSize()
                .graphicsLayer { scaleX = scale; scaleY = scale; translationX = pan.x; translationY = pan.y }
                .pointerInput(page) {
                    detectTransformGestures { _, p, zoom, _ ->
                        scale = (scale * zoom).coerceIn(1f, 5f)
                        pan = if (scale > 1f) pan + p else Offset.Zero
                    }
                },
        )
    }
}

@Composable
private fun InfoRow(label: String, value: String) {
    Row(Modifier.fillMaxWidth().padding(vertical = 4.dp)) {
        Text(label, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Spacer(Modifier.weight(1f))
        Text(value)
    }
}

@Composable
private fun FileInfoView(loading: Boolean, info: FileExifInfo?) {
    val context = LocalContext.current
    Column(Modifier.fillMaxWidth().padding(16.dp)) {
        Text("More Info", style = MaterialTheme.typography.titleMedium)
        Spacer(Modifier.height(12.dp))
        when {
            loading -> CircularProgressIndicator()
            info == null -> Text("No metadata found", color = MaterialTheme.colorScheme.onSurfaceVariant)
            else -> {
                val cam = listOf(info.cameraMake, info.cameraModel).filter { it.isNotEmpty() }.joinToString(" ")
                if (cam.isNotEmpty()) InfoRow("Camera", cam)
                if (info.hasTakenAt()) InfoRow("Taken", DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(info.takenAt.seconds * 1000)))
                if (info.width > 0 && info.height > 0) InfoRow("Dimensions", "${info.width} × ${info.height}")
                if (info.exposureTime.isNotEmpty()) InfoRow("Exposure", info.exposureTime)
                if (info.fNumber.isNotEmpty()) InfoRow("Aperture", info.fNumber)
                if (info.iso > 0) InfoRow("ISO", "${info.iso}")
                if (info.focalLength.isNotEmpty()) InfoRow("Focal length", info.focalLength)
                val loc = listOf(info.city, info.country).filter { it.isNotEmpty() }.joinToString(", ")
                if (loc.isNotEmpty()) InfoRow("Location", loc)
                if (info.hasGps) {
                    // Issue #127: the same 0.05° region around the point the iOS
                    // panel shows, on OpenStreetMap; a tap on the caption opens
                    // the point in the phone's maps app.
                    LocationMap(info.latitude, info.longitude)
                    TextButton(onClick = { Share.openInBrowser(context, "geo:${info.latitude},${info.longitude}?q=${info.latitude},${info.longitude}") }) {
                        Text("Open in Maps (%.5f, %.5f)".format(info.latitude, info.longitude))
                    }
                }
                if (cam.isEmpty() && !info.hasGps && info.exposureTime.isEmpty()) Text("No EXIF metadata in this file", color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
        Spacer(Modifier.height(24.dp))
    }
}

/** An OpenStreetMap view centred on the photo's position with a marker (issue #127). */
@Composable
private fun LocationMap(latitude: Double, longitude: Double) {
    val context = LocalContext.current
    AndroidView(
        factory = { ctx ->
            org.osmdroid.config.Configuration.getInstance().apply {
                userAgentValue = ctx.packageName
                osmdroidBasePath = ctx.cacheDir
                osmdroidTileCache = java.io.File(ctx.cacheDir, "osmdroid-tiles")
            }
            org.osmdroid.views.MapView(ctx).apply {
                setTileSource(org.osmdroid.tileprovider.tilesource.TileSourceFactory.MAPNIK)
                setMultiTouchControls(true)
                zoomController.setVisibility(org.osmdroid.views.CustomZoomButtonsController.Visibility.NEVER)
                val point = org.osmdroid.util.GeoPoint(latitude, longitude)
                // Zoom 13 spans roughly 0.05° of latitude on a phone-width map.
                controller.setZoom(13.0)
                controller.setCenter(point)
                overlays.add(org.osmdroid.views.overlay.Marker(this).apply {
                    position = point
                    setAnchor(org.osmdroid.views.overlay.Marker.ANCHOR_CENTER, org.osmdroid.views.overlay.Marker.ANCHOR_BOTTOM)
                })
                // OpenStreetMap's tile policy asks for the credit on the map.
                overlays.add(org.osmdroid.views.overlay.CopyrightOverlay(ctx))
            }
        },
        modifier = Modifier.fillMaxWidth().height(200.dp).clip(RoundedCornerShape(8.dp)),
    )
}
