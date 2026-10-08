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
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.calculatePan
import androidx.compose.foundation.gestures.calculateZoom
import androidx.compose.ui.input.pointer.positionChanged
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
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowCircleDown
import androidx.compose.material.icons.filled.Cancel
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.PlayCircle
import androidx.compose.material.icons.filled.Share
import androidx.compose.material3.AlertDialog
import cloud.offthe.otc.data.FaceRecognition
import cloud.offthe.otc.net.MediaStream
import cloud.offthe.otc.proto.SharedGallerySource
import cloud.offthe.otc.ui.share.SharedGalleryShareFlow
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.PlainTooltip
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TooltipBox
import androidx.compose.material3.TooltipDefaults
import androidx.compose.material3.rememberTooltipState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
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
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.media3.common.MediaItem
import androidx.media3.ui.PlayerView
import cloud.offthe.otc.proto.FileExifInfo
import cloud.offthe.otc.ui.common.NavIcons
import cloud.offthe.otc.ui.common.SelectionActionBar
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.decodeBitmap
import cloud.offthe.otc.ui.common.rememberOffMain
import cloud.offthe.otc.ui.common.rememberTileThumb
import kotlinx.coroutines.launch
import java.text.DateFormat
import java.util.Date
import kotlin.math.abs
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.outlined.Delete
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.Face
import androidx.compose.material.icons.outlined.Public
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.OutlinedButton
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.zIndex
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.isTraversalGroup
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.activity.compose.BackHandler
import java.time.ZoneId
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.Dp
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.Person
import kotlin.math.roundToInt

// Port of PhotoGalleryView (PhotoGallery.swift): the search (TopSearch.kt:
// tags, people and files, as the web's top bar), the People button and page
// (PeopleView.kt, while face recognition is on), the group chip and sheet, the
// adaptive grid with long-press selection, the date scrubber overlay,
// the selection bar and the full-screen viewer with share/save/delete
// and the EXIF panel (issue #41).
//
// The wide layout (MainView, a window 600dp or more across) has the search
// in its top bar and People and Collections in its menu: Images there is
// the grid alone (with an open collection's bar), and People and
// Collections are pages of their own in its place (CollectionsView.kt) -
// still drawn from here, so the grid and its scroll stay put under them,
// and a rotation or a fold between the two layouts keeps both.

/** What the Images section shows: the photos, or the People or Collections page over them. */
enum class GalleryPage { PHOTOS, PEOPLE, COLLECTIONS }

// Full size: the viewer's placeholder, until the full image arrives. Tiles
// use rememberTileThumb (decoded off the main thread, to the tile's size).
// Bytes ThumbStore still holds in memory show on the first frame, ones it
// moved to disk a moment later.
@Composable
private fun rememberThumb(key: String?): Bitmap? {
    val bytes = rememberOffMain(key, { key?.let(ThumbStore::peek) }) { key?.let { ThumbStore.load(it) } }
    return remember(bytes) { bytes?.let { decodeBitmap(it) } }
}

/**
 * Images. [page]: the photos, People (shown in their place on any width)
 * or Collections (a sheet over the photos when narrow, the page in their
 * place when [wide]); [onPage] moves between them - the section MainView
 * keeps, so the wide menu and a change of layout know it too.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun PhotoGalleryView(deviceId: String, wide: Boolean = false, page: GalleryPage = GalleryPage.PHOTOS, onPage: (GalleryPage) -> Unit = {}) {
    val vm: PhotoGalleryViewModel = viewModel(key = "gallery") { PhotoGalleryViewModel(deviceId) }
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    // Narrow: Collections is the sheet of collections over the photos.
    val showGroups = page == GalleryPage.COLLECTIONS && !wide
    var showGroupPicker by remember { mutableStateOf(false) }
    var newGroupName by remember { mutableStateOf<String?>(null) }
    var renameGroupName by remember { mutableStateOf<String?>(null) }
    var confirmDeleteGroup by remember { mutableStateOf(false) }
    // Issue #180: "Share as gallery" on a group - what is being shared.
    var gallerySource by remember { mutableStateOf<SharedGallerySource?>(null) }
    var confirmDeleteSelected by remember { mutableStateOf(false) }
    // The rows of the grid (months, PhotoMonths.kt), with the open
    // collection's or person's header first. Kept here, above the People
    // and Collections pages, so the photos' place stays while those show.
    val listState = rememberLazyListState()
    // The search (TopSearch.kt): its field here, its panel over the grid.
    val search: TopSearchViewModel = viewModel(key = "topsearch")
    val options = rememberSearchOptions(search, st)
    // The Collections sheet over the photos ends the search, as going to
    // Collections does (MainView's go) - the only way here with it still
    // open is the wide Collections page turned narrow, the section kept;
    // its panel and field would stay in use behind the sheet. Ahead of the
    // field below, so that the field doesn't take the focus back for it.
    val focus = LocalFocusManager.current
    LaunchedEffect(showGroups) { if (showGroups && search.open) { search.close(); focus.clearFocus() } }
    // The People page (PeopleView.kt), shown in place of the photos while
    // face recognition is on; it goes with it.
    val faces by FaceRecognition.enabled.collectAsState()
    val showPeople = page == GalleryPage.PEOPLE
    LaunchedEffect(faces) { if (faces != true && showPeople) onPage(GalleryPage.PHOTOS) }

    LaunchedEffect(Unit) { vm.onAppearInitial() }
    // The selection outlives a jump and a filter change, as on the web, but
    // not the photos left for People or Collections (pages of their own
    // there, so the web's grid starts again without one).
    LaunchedEffect(page) { if (page != GalleryPage.PHOTOS) vm.clearSelection() }

    if (showPeople && faces == true) {
        PeopleView(
            vm,
            onOpenPhotos = { onPage(GalleryPage.PHOTOS); scope.launch { listState.scrollToItem(0) } },
            onBack = { onPage(GalleryPage.PHOTOS) },
            showBack = !wide,
        )
    } else if (page == GalleryPage.COLLECTIONS && wide) {
        CollectionsView(
            vm,
            onOpen = { onPage(GalleryPage.PHOTOS); scope.launch { listState.scrollToItem(0) } },
            onShowPhotos = { onPage(GalleryPage.PHOTOS) },
        )
    } else Column(Modifier.fillMaxSize()) {
        // Back: the selection goes first (the web's selection bar has its
        // ✕ for that). An open search panel's own Back comes before it.
        BackHandler(enabled = st.selected.isNotEmpty() && !search.open) { vm.clearSelection() }
        // Wide, the search is in the top bar and People and Collections in
        // the menu. An open collection is the field's first chip either way
        // (its x closes it), as on the web, and its header - name, count,
        // the months it spans - heads the photos.
        if (!wide) Column(Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerLow).padding(top = 8.dp, bottom = 8.dp)) {
            Row(Modifier.padding(start = 8.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                TopSearchField(search, vm, st, options, Modifier.weight(1f), showGroup = true)
                // The ways into People and collections, as the web's menu
                // has them (Images, People, Collections); the tooltips name
                // the icons for sighted users too.
                if (faces == true) {
                    val people: PeopleViewModel = viewModel(key = "people")
                    TooltipBox(positionProvider = TooltipDefaults.rememberPlainTooltipPositionProvider(), tooltip = { PlainTooltip { Text("People") } }, state = rememberTooltipState()) {
                        IconButton(onClick = { people.enter(); onPage(GalleryPage.PEOPLE) }) { Icon(NavIcons.People, "People") }
                    }
                }
                TooltipBox(positionProvider = TooltipDefaults.rememberPlainTooltipPositionProvider(), tooltip = { PlainTooltip { Text("Collections") } }, state = rememberTooltipState()) {
                    IconButton(onClick = { onPage(GalleryPage.COLLECTIONS) }) { Icon(NavIcons.Collections, "Collections") }
                }
            }
        }

        // Grid + scrubber + selection bar
        BoxWithConstraints(Modifier.weight(1f).fillMaxWidth()) {
            // The grid's sizes (the web's PhotoGallery.css) and its columns
            // for this width; the tiles' side is what thumbnails decode to.
            val density = LocalDensity.current
            val windowDp = LocalConfiguration.current.screenWidthDp
            val metrics = remember(wide, windowDp) { gridMetrics(wide, windowDp) }
            val contentWidth = if (constraints.hasBoundedWidth) maxWidth - metrics.pad * 2 else 360.dp
            val geo = remember(contentWidth, metrics) {
                GridGeometry.fit(contentWidth.value, metrics.gap.value, metrics.secGap.value, metrics.tileMin.value, if (metrics.phone) PHONE_COLUMNS else null)
            }
            val tilePx = with(density) { geo.tile.dp.roundToPx() }
            // Below 900dp, as on the web, the collection's actions are icons
            // and its span is years only.
            val compact = windowDp < 900
            val dated = st.dateOrdered
            val buckets = st.freshBuckets
            // The months' layout: once per page, filter or width - the
            // photos' months were worked out as they came (Item.month).
            val monthCounts = remember(buckets) { if (buckets.isEmpty()) null else buckets.associate { it.month to it.count } }
            val layout = remember(st.items, dated, geo, monthCounts, st.endReached) {
                val n = st.items.size
                val runs = if (dated) monthRuns(st.items.map { it.month }) else if (n > 0) listOf(MonthRun(null, 0, n)) else emptyList()
                layoutGallery(runs, dated, geo, monthCounts, more = !st.endReached)
            }
            // Grey tiles: the scrubber's month under its title, or the first
            // page on its way (a grey title too, in date order).
            val ph = st.placeholderCount
            val skeleton = ph == null && st.items.isEmpty() && st.loading
            val greyLayout = remember(ph, st.placeholderMonth, skeleton, dated, geo) {
                when {
                    ph != null -> layoutGallery(if (ph > 0) listOf(MonthRun(st.placeholderMonth, 0, ph)) else emptyList(), true, geo)
                    skeleton -> layoutGallery(listOf(MonthRun(null, 0, SKELETON_TILES)), dated, geo)
                    else -> null
                }
            }
            val shown = greyLayout ?: layout

            // The headers above the photos: the open collection, or the
            // people searched for (without tags) - the web's CollectionHeader
            // and PersonHeader.
            val group = st.activeGroup
            val personHeader = group == null && st.selectedPeople.isNotEmpty() && st.chips.isEmpty() && faces == true
            val headerCount = (if (group != null) 1 else 0) + (if (personHeader) 1 else 0)

            // A change of width (turned, unfolded, the menu) changes the
            // columns and so every row: the photo that was first on screen
            // goes back to the top, as the grid it replaces kept it. Where
            // that is (the photo, and how far into its row, in tiles) is
            // taken from the person's own scrolling, never from a frame in
            // the middle of a rotation, where the list can be too short to
            // be scrolled that far and would hand back a clamped place.
            // (A page that lands keeps the rows already there, and the list
            // its place by their keys: only a new geometry needs this.)
            val anchor = remember { GridAnchor() }
            LaunchedEffect(listState) {
                var moving = false
                snapshotFlow { Triple(listState.isScrollInProgress, listState.firstVisibleItemIndex, listState.firstVisibleItemScrollOffset) }
                    .collect { (inProgress, index, offset) ->
                        // While a scroll goes on, and where it ends.
                        if (inProgress || moving) anchor.capture(index, offset)
                        moving = inProgress
                    }
            }
            // Asked for again at each recomposition until the list is there
            // (the window settles over a few frames), or the person scrolls.
            val lastGeo = remember { arrayOfNulls<GridGeometry>(1) }
            SideEffect {
                // Grey tiles stand for no photo: no place is read off them.
                if (greyLayout != null) { anchor.layout = null; return@SideEffect }
                // What the list's place is read against, when it scrolls.
                anchor.layout = layout
                anchor.headerCount = headerCount
                anchor.tilePx = geo.tile * density.density
                val oldGeo = lastGeo[0]
                lastGeo[0] = geo
                if (oldGeo != null && oldGeo != geo) anchor.pending = anchor.place
                val (item, tiles) = anchor.pending ?: return@SideEffect
                val row = layout.rowOf(item)
                if (row < 0 || listState.isScrollInProgress) { anchor.pending = null; return@SideEffect }
                val index = headerCount + row
                val offset = (tiles * geo.tile * density.density).roundToInt()
                if (oldGeo == geo && listState.firstVisibleItemIndex == index && listState.firstVisibleItemScrollOffset == offset) anchor.pending = null
                else listState.requestScrollToItem(index, offset)
            }
            // A new list (a search, a jump): no place in it yet but its top.
            LaunchedEffect(st.searchesStarted, st.jumpsLanded) { anchor.place = null; anchor.pending = null }
            // A jump's photos landed: at the top, its month's title. A new
            // search (a filter) starts at the top of the page.
            var jumpsSeen by remember { mutableIntStateOf(st.jumpsLanded) }
            LaunchedEffect(st.jumpsLanded) {
                if (st.jumpsLanded != jumpsSeen) { jumpsSeen = st.jumpsLanded; listState.scrollToItem(headerCount) }
            }
            var searchesSeen by remember { mutableIntStateOf(st.searchesStarted) }
            LaunchedEffect(st.searchesStarted) {
                if (st.searchesStarted != searchesSeen) { searchesSeen = st.searchesStarted; listState.scrollToItem(0) }
            }

            val tileColor = tileBackground()
            val hasSelection = st.selected.isNotEmpty()
            LazyColumn(state = listState, modifier = Modifier.fillMaxSize()) {
                if (group != null) item(key = "head:group", contentType = "head") {
                    CollectionHeader(
                        group, span = spanLabel(buckets, years = compact), compact = compact, metrics = metrics,
                        // Leaving it, not just looking away: the search is the
                        // whole library again.
                        onBack = { vm.leaveGroup(); onPage(GalleryPage.COLLECTIONS) },
                        onRename = { renameGroupName = group.name },
                        onShare = { gallerySource = SharedGallerySource.newBuilder().setGroupId(group.id).build() },
                        onDelete = { confirmDeleteGroup = true },
                    )
                }
                if (personHeader) item(key = "head:people", contentType = "head") {
                    PersonHeader(st.allPeople, st.peopleLoaded, st.selectedPeople, total = if (st.bucketsFresh) st.totalPhotos else null, metrics = metrics)
                }
                val rows = shown.rows
                val grey = greyLayout != null
                items(
                    count = rows.size,
                    key = { i ->
                        val r = rows[i]
                        val tag = if (r is TitledRow) "l" else "r"
                        if (grey) "g:$tag:${r.start}" else "$tag:${st.items.getOrNull(r.start)?.id ?: r.start}"
                    },
                    contentType = { i -> if (rows[i] is TitledRow) "line" else "tiles" },
                ) { i ->
                    val row = rows[i]
                    val top = when {
                        i == 0 -> metrics.top
                        row is TitledRow -> metrics.lineGap
                        else -> metrics.gap
                    }
                    if (!grey) LaunchedEffect(row.end) { vm.loadMoreIfNeeded(row.end - 1) }
                    GalleryRowView(row, geo, metrics, Modifier.padding(start = metrics.pad, end = metrics.pad, top = top), titleSkeleton = skeleton && ph == null) { k, cell ->
                        if (grey) Box(cell.aspectRatio(1f).clip(TILE_SHAPE).background(tileColor))
                        else {
                            val item = st.items[k]
                            PhotoTile(
                                item, tilePx, isSelected = item.path in st.selected, hasSelection = hasSelection,
                                onTap = { vm.open(k) }, onLongPress = { vm.toggleSelect(item.path) }, modifier = cell,
                            )
                        }
                    }
                }
                // The end of the grid: where the next page shows it is coming.
                // A fixed height, so the spinner coming and going moves nothing.
                // Not without rows: the list would hold on to it as the first
                // item when they come, and open at its end.
                if (rows.isNotEmpty()) item(key = "foot", contentType = "foot") {
                    Box(Modifier.fillMaxWidth().heightIn(min = 72.dp).padding(top = 8.dp, bottom = 24.dp), contentAlignment = Alignment.Center) {
                        if (ph == null && st.loading && st.items.isNotEmpty()) CircularProgressIndicator()
                    }
                }
            }
            if (st.showScrubber) {
                PhotoDateScrubber(vm, st, Modifier.align(Alignment.CenterEnd).fillMaxHeight(), onEngage = { scope.launch { listState.scrollToItem(headerCount) } })
            }
            if (st.selected.isNotEmpty()) {
                Box(Modifier.align(Alignment.BottomCenter)) {
                    SelectionActionBar(
                        count = st.selected.size, busy = st.preparing,
                        onShare = { vm.shareSelected(context) }, onDownload = { vm.downloadZip(context) },
                        onDelete = { confirmDeleteSelected = true }, onGroup = { scope.launch { vm.loadGroups() }; showGroupPicker = true },
                        // Issue #180: the selected photos, as a gallery - no group needed.
                        onGallery = { gallerySource = SharedGallerySource.newBuilder().addAllPaths(st.selected.sorted()).build() },
                    )
                }
            }
            // Wide, the panel hangs from the top bar's field (MainView).
            if (!wide) TopSearchPanel(search, vm, st, options)
        }
    }

    // Sheets & dialogs
    if (showGroups) {
        // Asked again each time it opens (covers and counts change).
        LaunchedEffect(Unit) { vm.loadGroups() }
        ModalBottomSheet(onDismissRequest = { onPage(GalleryPage.PHOTOS) }) {
            Column(Modifier.fillMaxWidth().padding(16.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("Collections", style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                    TextButton(onClick = { onPage(GalleryPage.PHOTOS) }) { Text("Done") }
                }
                if (st.groups.isEmpty()) Text("No collections yet — select some pictures and choose Add to collection.", color = MaterialTheme.colorScheme.onSurfaceVariant)
                st.groups.forEach { g ->
                    Row(Modifier.fillMaxWidth().clickable { onPage(GalleryPage.PHOTOS); vm.openGroup(g) }.padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                        val cover = rememberTileThumb(
                            if (g.coverThumbnail.isEmpty) null else "g:${g.id}:${g.coverThumbnail.hashCode()}",
                            with(LocalDensity.current) { 44.dp.roundToPx() },
                        ) { g.coverThumbnail.toByteArray() }
                        if (cover != null) Image(cover.asImageBitmap(), null, Modifier.size(44.dp).clip(RoundedCornerShape(6.dp)), contentScale = ContentScale.Crop)
                        else Box(Modifier.size(44.dp).clip(RoundedCornerShape(6.dp)).background(Color(0x26808080)), contentAlignment = Alignment.Center) { Icon(NavIcons.Collections, null) }
                        Spacer(Modifier.width(12.dp))
                        Column(Modifier.weight(1f)) {
                            Text(g.name)
                            Text("${g.fileCount} ${if (g.fileCount == 1) "picture" else "pictures"}", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                        IconButton(onClick = {
                            onPage(GalleryPage.PHOTOS)
                            gallerySource = SharedGallerySource.newBuilder().setGroupId(g.id).build()
                        }) { Icon(Icons.Default.Share, "Share as gallery", tint = MaterialTheme.colorScheme.primary) }
                    }
                }
            }
        }
    }
    if (showGroupPicker) {
        ModalBottomSheet(onDismissRequest = { showGroupPicker = false }) {
            Column(Modifier.fillMaxWidth().padding(16.dp)) {
                Text("Add ${st.selected.size} to a collection", style = MaterialTheme.typography.titleMedium)
                st.groups.forEach { g -> TextButton(onClick = { showGroupPicker = false; scope.launch { vm.addSelectionToGroup(g) } }) { Text(g.name) } }
                TextButton(onClick = { showGroupPicker = false; newGroupName = "" }) { Text("New collection…") }
            }
        }
    }
    newGroupName?.let { name ->
        TextDialog(
            "New collection", "${st.selected.size} ${if (st.selected.size == 1) "picture" else "pictures"} will be added to it.", name, "Collection name", "Create",
            onChange = { newGroupName = it }, onConfirm = { newGroupName = null; scope.launch { vm.createGroupFromSelection(name) } }, onDismiss = { newGroupName = null },
        )
    }
    renameGroupName?.let { name ->
        TextDialog(
            "Rename collection", null, name, "Collection name", "Save",
            onChange = { renameGroupName = it }, onConfirm = { renameGroupName = null; scope.launch { vm.renameActiveGroup(name) } }, onDismiss = { renameGroupName = null },
        )
    }
    gallerySource?.let { src -> SharedGalleryShareFlow(src, onDismiss = { gallerySource = null }) }
    if (confirmDeleteGroup) {
        ConfirmDialog(
            "Delete the collection \"${st.activeGroup?.name ?: ""}\"?", "The pictures themselves are kept.", "Delete collection",
            onConfirm = { confirmDeleteGroup = false; scope.launch { vm.deleteActiveGroup() } }, onDismiss = { confirmDeleteGroup = false },
        )
    }
    if (confirmDeleteSelected) {
        ConfirmDialog(
            "Delete ${st.selected.size} item${if (st.selected.size == 1) "" else "s"}?", null, "Delete",
            onConfirm = { confirmDeleteSelected = false; vm.deleteSelected() }, onDismiss = { confirmDeleteSelected = false },
        )
    }
    st.alert?.let {
        AlertDialog(onDismissRequest = { vm.dismissAlert() }, text = { Text(it) }, confirmButton = { TextButton(onClick = { vm.dismissAlert() }) { Text("OK") } })
    }

    if (st.openIndex != null) ImageModal(vm, st)
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

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun PhotoTile(
    item: PhotoGalleryViewModel.Item, sidePx: Int, isSelected: Boolean, hasSelection: Boolean,
    onTap: () -> Unit, onLongPress: () -> Unit, modifier: Modifier = Modifier,
) {
    val bmp = rememberTileThumb(item.thumbKey, sidePx) { item.thumbKey?.let { ThumbStore.load(it) } }
    val video = item.mime.startsWith("video/")
    val pick = if (isSelected) "Deselect" else "Select"
    Box(
        modifier.aspectRatio(1f).clip(TILE_SHAPE).background(tileBackground())
            .combinedClickable(
                onClickLabel = if (hasSelection) pick else null, onLongClickLabel = pick,
                onClick = { if (hasSelection) onLongPress() else onTap() }, onLongClick = onLongPress,
            )
            // TalkBack reads a tile as the web's ("Photo, 5 October 2026"),
            // and while photos are being picked whether it is one of them
            // (the web's aria-pressed). Worked out only when asked for.
            .semantics {
                contentDescription = tileLabel(video, item.created, ZoneId.systemDefault())
                if (hasSelection) selected = isSelected
            },
    ) {
        if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
        if (video) Icon(Icons.Default.PlayCircle, null, Modifier.align(Alignment.BottomStart).padding(4.dp).size(18.dp), tint = Color.White)
        if (isSelected) {
            Box(Modifier.fillMaxSize().border(3.dp, MaterialTheme.colorScheme.primary, TILE_SHAPE))
            Icon(Icons.Default.CheckCircle, null, Modifier.align(Alignment.TopStart).padding(6.dp), tint = MaterialTheme.colorScheme.primary)
        }
    }
}

// ---- months ---------------------------------------------------------------------

/**
 * Where the grid is, kept across a change of width (PhotoGalleryView): the
 * first photo of the row at the top and how far into that row, in tiles.
 * [capture] reads it off the list with the rows on screen.
 */
private class GridAnchor {
    var layout: GalleryLayout? = null
    var headerCount = 0
    var tilePx = 1f
    // The photo's index and the offset into its row, in tiles; null at the top.
    var place: Pair<Int, Float>? = null
    // The place a change of width is still taking the list back to.
    var pending: Pair<Int, Float>? = null

    fun capture(index: Int, offset: Int) {
        val row = layout?.rows?.getOrNull(index - headerCount)
        place = row?.let { it.start to offset / tilePx.coerceAtLeast(1f) }
        pending = null
    }
}

// Grey tiles while the first page is on its way (the web's cSkeletonTiles).
private const val SKELETON_TILES = 24
// A phone's columns, whatever its width (the web's --pg-cols-fixed).
private const val PHONE_COLUMNS = 3
private val TILE_SHAPE = RoundedCornerShape(2.dp)

/** A tile's colour before its thumbnail (and a grey tile's), in either theme. */
@Composable
private fun tileBackground() = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.08f)

/**
 * The grid's sizes, as PhotoGallery.css has them: a phone three across,
 * 2dp between tiles and between months side by side (a one- and a
 * two-photo month share a row; their titles keep them apart); a wider
 * window as many 120dp tiles as fit (150dp from 1024dp), 4dp apart, and
 * more room between months.
 */
internal data class GridMetrics(
    val phone: Boolean, val pad: Dp, val gap: Dp, val secGap: Dp,
    // Above a line of months (their titles), and above the first.
    val lineGap: Dp, val top: Dp, val tileMin: Dp,
    // A title's size, its inset from the tiles' edge and the room under it.
    val titleSp: Int, val titleInset: Dp, val titleBottom: Dp,
)

internal fun gridMetrics(wide: Boolean, windowDp: Int): GridMetrics = if (!wide) {
    GridMetrics(phone = true, pad = 8.dp, gap = 2.dp, secGap = 2.dp, lineGap = 14.dp, top = 8.dp, tileMin = 120.dp, titleSp = 14, titleInset = 4.dp, titleBottom = 2.dp)
} else {
    val whole = windowDp >= 1024
    GridMetrics(
        phone = false, pad = 16.dp, gap = 4.dp, secGap = if (whole) 16.dp else 12.dp, lineGap = 20.dp, top = 12.dp,
        tileMin = if (whole) 150.dp else 120.dp, titleSp = 15, titleInset = 0.dp, titleBottom = 4.dp,
    )
}

/**
 * A row of the grid: months side by side (each its title over its own
 * tiles, as wide as they are) or a row of tiles across the width. [cell]
 * draws the photo at an index with the modifier that sizes it.
 */
@Composable
private fun GalleryRowView(
    row: GalleryRow, geo: GridGeometry, m: GridMetrics, modifier: Modifier, titleSkeleton: Boolean,
    cell: @Composable RowScope.(Int, Modifier) -> Unit,
) {
    when (row) {
        is TileRow -> TileCells(row.start, row.end, geo.cols, m.gap, modifier.fillMaxWidth(), cell)
        is TitledRow -> MonthsSideBySide(row.segments, geo.cols, m, modifier.fillMaxWidth()) { seg ->
            val slots = minOf(seg.slots, geo.cols)
            // One group per month, so TalkBack reads a month's title and
            // then its tiles before the next month's title beside it (the
            // web's order) - not both titles, which share a line, first.
            Column(Modifier.semantics { isTraversalGroup = true }) {
                MonthTitle(seg.month, oneTile = slots == 1, m, skeleton = titleSkeleton)
                TileCells(seg.start, seg.end, slots, m.gap, Modifier.fillMaxWidth(), cell)
            }
        }
    }
}

/**
 * Months side by side, each as wide as its own tiles: worked out from the
 * width the row is measured at, as the full rows' tiles are (weights), so a
 * month beside another always has tiles of the same size.
 */
@Composable
private fun MonthsSideBySide(segments: List<Segment>, cols: Int, m: GridMetrics, modifier: Modifier, content: @Composable (Segment) -> Unit) {
    Layout(content = { segments.forEach { content(it) } }, modifier = modifier) { measurables, constraints ->
        val width = constraints.maxWidth
        val gap = m.gap.toPx()
        val between = m.secGap.roundToPx()
        val tile = (width - (cols - 1) * gap) / cols
        val placeables = measurables.mapIndexed { i, child ->
            val slots = minOf(segments[i].slots, cols)
            val w = if (slots >= cols) width else (slots * tile + (slots - 1) * gap).roundToInt().coerceIn(0, width)
            child.measure(Constraints(minWidth = w, maxWidth = w, maxHeight = constraints.maxHeight))
        }
        layout(width, placeables.maxOfOrNull { it.height } ?: 0) {
            var x = 0
            placeables.forEach { p -> p.placeRelative(x, 0); x += p.width + between }
        }
    }
}

/** Tiles [start, end) in a row [slots] wide: the empty slots keep every tile the size of a full row's. */
@Composable
private fun TileCells(start: Int, end: Int, slots: Int, gap: Dp, modifier: Modifier, cell: @Composable RowScope.(Int, Modifier) -> Unit) {
    Row(modifier, horizontalArrangement = Arrangement.spacedBy(gap)) {
        for (k in start until end) cell(k, Modifier.weight(1f))
        repeat(slots - (end - start)) { Spacer(Modifier.weight(1f)) }
    }
}

/**
 * A month's title, one line of the same height in every month, so months
 * side by side keep their tiles level - "March 2024", or "Mar 2024" in a
 * month one tile wide where the whole name doesn't fit. Photos without a
 * date at the top get an empty line. [skeleton]: the first page's grey bar.
 */
@Composable
private fun MonthTitle(month: String?, oneTile: Boolean, m: GridMetrics, skeleton: Boolean) {
    Box(
        Modifier.fillMaxWidth().padding(start = m.titleInset, end = m.titleInset, top = 4.dp, bottom = m.titleBottom + m.gap).height(20.dp),
        contentAlignment = Alignment.CenterStart,
    ) {
        if (skeleton) {
            Box(Modifier.width(140.dp).height(14.dp).clip(RoundedCornerShape(7.dp)).background(tileBackground()))
            return@Box
        }
        if (month == null) return@Box
        val style = TextStyle(fontSize = m.titleSp.sp, fontWeight = FontWeight.SemiBold, lineHeight = 20.sp, color = MaterialTheme.colorScheme.onSurface)
        val whole = monthTitle(month)
        val heading = Modifier.semantics { heading(); contentDescription = whole }
        if (!oneTile) {
            Text(whole, heading, style = style, maxLines = 1, overflow = TextOverflow.Ellipsis)
        } else {
            var short by remember(month) { mutableStateOf(false) }
            Text(
                if (short) monthShort(month) else whole, heading, style = style, maxLines = 1, softWrap = false,
                overflow = if (short) TextOverflow.Ellipsis else TextOverflow.Clip,
                onTextLayout = { if (!short && it.hasVisualOverflow) short = true },
            )
        }
    }
}

/**
 * An open collection, above its photos (the web's CollectionHeader): back
 * to Collections, its name (a tap renames it), how many photos and the
 * months they span, and sharing it as a gallery or deleting it - icons
 * when [compact], labelled buttons on a wide window.
 */
@Composable
private fun CollectionHeader(
    g: ImageGroup, span: String, compact: Boolean, metrics: GridMetrics,
    onBack: () -> Unit, onRename: () -> Unit, onShare: () -> Unit, onDelete: () -> Unit,
) {
    val colors = MaterialTheme.colorScheme
    val danger = Color(0xFFE53935)
    Column(Modifier.fillMaxWidth().padding(start = metrics.pad, end = metrics.pad, top = if (metrics.phone) 8.dp else 16.dp, bottom = 4.dp)) {
        Row(
            Modifier.offset(x = (-8).dp).height(36.dp).clip(CircleShape).clickable(onClick = onBack).padding(start = 8.dp, end = 14.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(Icons.AutoMirrored.Filled.ArrowBack, null, Modifier.size(20.dp), tint = colors.onSurfaceVariant)
            Spacer(Modifier.width(6.dp))
            Text("Collections", fontSize = 14.sp, fontWeight = FontWeight.SemiBold, color = colors.onSurfaceVariant)
        }
        Spacer(Modifier.height(4.dp))
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(if (metrics.phone) 12.dp else 16.dp)) {
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.Top) {
                    // The page's heading (the web's h1); a tap renames it.
                    Text(
                        g.name,
                        Modifier.weight(1f, fill = false).clip(RoundedCornerShape(8.dp))
                            .clickable(onClickLabel = "Rename collection", onClick = onRename).semantics { heading() },
                        style = headerTitleStyle(metrics), maxLines = 2, overflow = TextOverflow.Ellipsis,
                    )
                    IconButton(onClick = onRename, Modifier.size(if (metrics.phone) 30.dp else 36.dp)) {
                        Icon(Icons.Outlined.Edit, "Rename collection", Modifier.size(20.dp), tint = colors.onSurfaceVariant)
                    }
                }
                Text(
                    countLabel(g.fileCount, "item") + if (span.isNotEmpty()) " · $span" else "",
                    Modifier.padding(top = 2.dp), fontSize = 14.sp, lineHeight = 20.sp, color = colors.onSurfaceVariant,
                    maxLines = 1, overflow = TextOverflow.Ellipsis,
                )
            }
            if (compact) Row {
                IconButton(onClick = onShare) { Icon(Icons.Outlined.Public, "Share as gallery", tint = colors.onSurfaceVariant) }
                IconButton(onClick = onDelete) { Icon(Icons.Outlined.Delete, "Delete collection", tint = danger) }
            } else Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedButton(onClick = onShare, contentPadding = PaddingValues(start = 14.dp, end = 18.dp)) {
                    Icon(Icons.Outlined.Public, null, Modifier.size(20.dp))
                    Spacer(Modifier.width(8.dp))
                    Text("Share as gallery")
                }
                OutlinedButton(
                    onClick = onDelete, contentPadding = PaddingValues(start = 14.dp, end = 18.dp),
                    colors = ButtonDefaults.outlinedButtonColors(contentColor = danger),
                ) {
                    Icon(Icons.Outlined.Delete, null, Modifier.size(20.dp))
                    Spacer(Modifier.width(8.dp))
                    Text("Delete collection")
                }
            }
        }
    }
}

/**
 * The people searched for, above their photos (the web's PersonHeader):
 * their faces, their names, and how many photos ([total], from the date
 * buckets; the line is kept until it comes, so nothing moves).
 */
@Composable
private fun PersonHeader(people: List<Person>, loaded: Boolean, ids: List<String>, total: Int?, metrics: GridMetrics) {
    val colors = MaterialTheme.colorScheme
    val face = if (metrics.phone) 48.dp else 56.dp
    val picked = if (loaded) ids.mapNotNull { id -> people.firstOrNull { it.id == id } } else emptyList()
    if (loaded && picked.isEmpty()) return
    Row(
        Modifier.fillMaxWidth().padding(start = metrics.pad, end = metrics.pad, top = if (metrics.phone) 8.dp else 16.dp, bottom = 4.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(if (metrics.phone) 12.dp else 16.dp),
    ) {
        // Overlapping, the first on top.
        Row(horizontalArrangement = Arrangement.spacedBy((-16).dp)) {
            if (!loaded) FaceCircle(null, face, 0f)
            picked.take(3).forEachIndexed { i, p -> FaceCircle(p, face, 3f - i) }
        }
        Column(Modifier.weight(1f)) {
            if (!loaded) {
                // The same height as the header it stands in for.
                Box(Modifier.padding(vertical = 7.dp).width(160.dp).height(16.dp).clip(RoundedCornerShape(8.dp)).background(tileBackground()))
                Box(Modifier.padding(top = 6.dp, bottom = 4.dp).width(72.dp).height(12.dp).clip(RoundedCornerShape(6.dp)).background(tileBackground()))
            } else {
                Text(
                    peopleTitle(picked.map { it.name.trim().ifEmpty { "Unnamed" } }), Modifier.semantics { heading() },
                    style = headerTitleStyle(metrics), maxLines = 2, overflow = TextOverflow.Ellipsis,
                )
                Text(total?.let { countLabel(it, "item") } ?: "\u00a0", Modifier.padding(top = 2.dp), fontSize = 14.sp, lineHeight = 20.sp, color = colors.onSurfaceVariant, maxLines = 1)
            }
        }
    }
}

@Composable
private fun headerTitleStyle(m: GridMetrics) = TextStyle(
    fontSize = if (m.phone) 22.sp else 28.sp, lineHeight = if (m.phone) 30.sp else 36.sp,
    fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.onSurface,
)

/** A person's face in the header, ringed in the page's colour so faces overlapping read apart. */
@Composable
private fun FaceCircle(p: Person?, size: Dp, z: Float) {
    val px = with(LocalDensity.current) { size.roundToPx() }
    val bmp = rememberTileThumb(if (p == null || p.coverThumbnail.isEmpty) null else "p:${p.id}:${p.coverThumbnail.hashCode()}", px) { p?.coverThumbnail?.toByteArray() }
    val colors = MaterialTheme.colorScheme
    Box(
        Modifier.zIndex(z).size(size).clip(CircleShape).background(colors.surfaceContainerHighest).border(2.dp, colors.surface, CircleShape),
        contentAlignment = Alignment.Center,
    ) {
        // The ring (the Box's border) is drawn over the face.
        if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
        else if (p != null) Icon(Icons.Outlined.Face, null, Modifier.size(size * 0.5f), tint = colors.onSurfaceVariant)
    }
}

/** Issue #77/#113: the Google-Photos-style scrubber on the grid's right edge. */
@Composable
private fun PhotoDateScrubber(vm: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State, modifier: Modifier, onEngage: () -> Unit) {
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
                    scrubLabel(target.month), style = MaterialTheme.typography.labelMedium, fontWeight = FontWeight.Bold,
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
                        // The month under the finger, as grey tiles under its
                        // title: no request, the buckets have its count.
                        scrubBucketAt(vm.state.value.freshBuckets, frac)?.let { if (it.month != vm.state.value.placeholderMonth) vm.previewMonth(it) }
                    },
                    onDragEnd = {
                        val target = vm.state.value.scrubTarget
                        vm.setScrubFrac(null)
                        if (target != null) { vm.previewMonth(target); vm.jumpToDate(target.month) } else vm.previewMonth(null)
                        engaged = false
                    },
                    onDragCancel = { vm.setScrubFrac(null); vm.previewMonth(null); engaged = false },
                )
            },
        )
    }
}

/**
 * The full-screen viewer: a pager that slides between photos (the
 * neighbours are drawn from the view model's small full-size cache),
 * pinch to zoom (issue #36), share/save/delete (issue #9), info (issue
 * #41). Mirrors the iOS viewer's strip of previous/current/next. Also
 * the Files section's viewer (FilesExplorerView, via showFiles).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun ImageModal(vm: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State) {
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
    // Fully open, and scrollable (FileInfoView): half-open, the location
    // map - the last thing in the sheet - sat below the visible part, so
    // it was never drawn nor its tiles fetched.
    if (st.infoOpen) ModalBottomSheet(
        onDismissRequest = { vm.closeInfo() },
        sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true),
    ) { FileInfoView(st.infoLoading, st.infoData) }
}

@Composable
private fun ViewerPage(vm: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State, page: Int, isCurrent: Boolean) {
    val context = LocalContext.current
    val item = st.items.getOrNull(page) ?: return
    val video = if (isCurrent) st.videoUrl else null
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        if (video != null) {
            val player = remember(video) { MediaStream.player(context, video).apply { setMediaItem(MediaItem.fromUri(video)); prepare(); playWhenReady = true } }
            DisposableEffect(video) { onDispose { player.release() } }
            AndroidView(factory = { PlayerView(it).apply { this.player = player; useController = true } }, modifier = Modifier.fillMaxSize())
            return@Box
        }
        val hiRes = st.hiResImages[item.path]
        val image = hiRes ?: item.preview ?: rememberThumb(item.thumbKey)
        if (image == null) { CircularProgressIndicator(color = Color.White); return@Box }
        // Still the thumbnail: the full-size image hasn't arrived (or
        // failed). Photos only - a video page's poster is never "low res".
        val lowRes = hiRes == null && !item.mime.startsWith("video/")
        // Pinch to zoom, and pan while zoomed in. Everything else - a
        // one-finger swipe on a photo at normal size - is left unconsumed
        // for the pager, which is what turns the page. (detectTransform-
        // Gestures took every drag, so swiping never changed the photo.)
        // The zoom snaps back when the page changes.
        var scale by remember(page) { mutableStateOf(1f) }
        var pan by remember(page) { mutableStateOf(Offset.Zero) }
        Image(
            image.asImageBitmap(), null, contentScale = ContentScale.Fit,
            modifier = Modifier.fillMaxSize()
                .graphicsLayer { scaleX = scale; scaleY = scale; translationX = pan.x; translationY = pan.y }
                .pointerInput(page) {
                    awaitEachGesture {
                        awaitFirstDown(requireUnconsumed = false)
                        do {
                            val event = awaitPointerEvent()
                            val pinching = event.changes.count { it.pressed } > 1
                            if (pinching || scale > 1f) {
                                scale = (scale * event.calculateZoom()).coerceIn(1f, 5f)
                                pan = if (scale > 1f) pan + event.calculatePan() else Offset.Zero
                                event.changes.forEach { if (it.positionChanged()) it.consume() }
                            }
                        } while (event.changes.any { it.pressed })
                    }
                },
        )
        if (lowRes) BoxWithConstraints(Modifier.fillMaxSize()) {
            // The pill sits in the photo's own lower-right corner, so
            // the fitted (letterboxed) rectangle, not the page's.
            val fit = minOf(maxWidth / image.width.toFloat(), maxHeight / image.height.toFloat())
            Box(Modifier.align(Alignment.Center).size(fit * image.width.toFloat(), fit * image.height.toFloat())) {
                LowResPill(loading = item.path in st.hiResLoading, modifier = Modifier.align(Alignment.BottomEnd).padding(12.dp))
            }
        }
    }
}

/**
 * Shown over a full-screen photo while it is still drawn from its
 * thumbnail; loading adds a small spinner (the full-size image is on
 * its way, rather than having failed).
 */
@Composable
internal fun LowResPill(loading: Boolean, modifier: Modifier = Modifier) {
    Row(
        modifier.background(Color.Black.copy(alpha = 0.55f), RoundedCornerShape(50)).padding(horizontal = 8.dp, vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (loading) {
            CircularProgressIndicator(color = Color.White, strokeWidth = 1.5.dp, modifier = Modifier.size(10.dp))
            Spacer(Modifier.width(6.dp))
        }
        Text("Low res", color = Color.White, fontSize = 12.sp, fontWeight = FontWeight.SemiBold)
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
    Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(16.dp)) {
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
