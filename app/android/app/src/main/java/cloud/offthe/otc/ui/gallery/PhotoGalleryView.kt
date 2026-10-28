// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import android.graphics.Bitmap
import android.graphics.Rect
import android.graphics.Region
import android.os.Build
import android.view.RoundedCorner
import android.view.View
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.displayCutout
import androidx.compose.foundation.layout.systemBars
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.boundsInRoot
import androidx.compose.ui.layout.findRootCoordinates
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
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
import androidx.compose.foundation.layout.displayCutoutPadding
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
import androidx.compose.runtime.derivedStateOf
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
import androidx.compose.ui.input.nestedscroll.NestedScrollConnection
import androidx.compose.ui.input.nestedscroll.NestedScrollSource
import androidx.compose.ui.input.nestedscroll.nestedScroll
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
import cloud.offthe.otc.ui.common.DeviceNeedsUpdateDialog
import cloud.offthe.otc.ui.common.NavIcons
import cloud.offthe.otc.ui.common.dragSelect
import cloud.offthe.otc.ui.common.rememberDragSelect
import cloud.offthe.otc.ui.common.SelectionActionBar
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.decodeBitmap
import cloud.offthe.otc.ui.common.rememberOffMain
import cloud.offthe.otc.ui.common.rememberTileThumb
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.text.DateFormat
import java.util.Date
import kotlin.math.abs
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
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
import androidx.compose.ui.semantics.onLongClick
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
import androidx.compose.foundation.layout.widthIn
import androidx.compose.material.icons.outlined.CloudOff
import androidx.compose.material.icons.outlined.Search
import androidx.compose.material.icons.outlined.WifiOff
import androidx.compose.material3.Button
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.Surface
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.text.style.TextAlign
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.MORE_PHOTOS_PROBLEM
import cloud.offthe.otc.ui.common.PHOTOS_LOADING
import cloud.offthe.otc.ui.common.PHOTOS_PROBLEM_TITLE
import cloud.offthe.otc.ui.common.PHOTOS_SLOW
import cloud.offthe.otc.ui.common.TRY_AGAIN
import cloud.offthe.otc.ui.common.TRYING
import cloud.offthe.otc.ui.common.VIDEO_UNPLAYABLE
import cloud.offthe.otc.ui.common.photosProblemText
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.compose.ui.platform.LocalResources
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.ui.TourTarget
import cloud.offthe.otc.ui.tourTarget
import cloud.offthe.otc.i18n.androidPhotosOpenInMaps
import cloud.offthe.otc.i18n.appPhotosCollectionsAddTitle
import cloud.offthe.otc.i18n.appPhotosCollectionsDelete
import cloud.offthe.otc.i18n.appPhotosCollectionsDeleteMessage
import cloud.offthe.otc.i18n.appPhotosCollectionsDeleteTitle
import cloud.offthe.otc.i18n.appPhotosCollectionsNamePlaceholder
import cloud.offthe.otc.i18n.appPhotosCollectionsNewItem
import cloud.offthe.otc.i18n.appPhotosCollectionsNewMessage
import cloud.offthe.otc.i18n.appPhotosCollectionsNewTitle
import cloud.offthe.otc.i18n.appPhotosCollectionsPictures
import cloud.offthe.otc.i18n.appPhotosCollectionsRename
import cloud.offthe.otc.i18n.appPhotosCollectionsSheetEmpty
import cloud.offthe.otc.i18n.appPhotosCopiesAll
import cloud.offthe.otc.i18n.appPhotosCopiesEvery
import cloud.offthe.otc.i18n.appPhotosCopiesOnlyThese
import cloud.offthe.otc.i18n.appPhotosCopiesOnlyThis
import cloud.offthe.otc.i18n.appPhotosCopiesSeveralText
import cloud.offthe.otc.i18n.appPhotosCopiesSingleText
import cloud.offthe.otc.i18n.appPhotosGridCollectionItemsSpan
import cloud.offthe.otc.i18n.appPhotosGridEmptyCollectionText
import cloud.offthe.otc.i18n.appPhotosGridEmptyCollectionTitle
import cloud.offthe.otc.i18n.appPhotosGridNoMatchIn
import cloud.offthe.otc.i18n.appPhotosGridNoMatchText
import cloud.offthe.otc.i18n.appPhotosGridNoMatchTitle
import cloud.offthe.otc.i18n.appPhotosGridNoPhotosText
import cloud.offthe.otc.i18n.appPhotosGridNoPhotosTitle
import cloud.offthe.otc.i18n.appPhotosGridPersonItems
import cloud.offthe.otc.i18n.appPhotosSelectionDeleteTitle
import cloud.offthe.otc.i18n.appPhotosViewerDelete
import cloud.offthe.otc.i18n.appPhotosViewerDeleteTitle
import cloud.offthe.otc.i18n.appPhotosViewerDeleteVideo
import cloud.offthe.otc.i18n.appPhotosViewerDeleteVideoTitle
import cloud.offthe.otc.i18n.appPhotosViewerInfoAperture
import cloud.offthe.otc.i18n.appPhotosViewerInfoCamera
import cloud.offthe.otc.i18n.appPhotosViewerInfoDimensions
import cloud.offthe.otc.i18n.appPhotosViewerInfoExposure
import cloud.offthe.otc.i18n.appPhotosViewerInfoFocalLength
import cloud.offthe.otc.i18n.appPhotosViewerInfoIso
import cloud.offthe.otc.i18n.appPhotosViewerInfoLocation
import cloud.offthe.otc.i18n.appPhotosViewerInfoNoExif
import cloud.offthe.otc.i18n.appPhotosViewerInfoNone
import cloud.offthe.otc.i18n.appPhotosViewerInfoTaken
import cloud.offthe.otc.i18n.appPhotosViewerInfoTitle
import cloud.offthe.otc.i18n.appPhotosViewerLowRes
import cloud.offthe.otc.i18n.appPhotosViewerMoreInfo
import cloud.offthe.otc.i18n.appPhotosViewerSaving
import cloud.offthe.otc.i18n.appShellCollectionsGoToImages
import cloud.offthe.otc.i18n.appShellSearchClear
import cloud.offthe.otc.i18n.appShellSearchUnnamed
import cloud.offthe.otc.i18n.appShellSectionCollections
import cloud.offthe.otc.i18n.appShellSectionPeople
import cloud.offthe.otc.i18n.appShellShareAsGallery
import cloud.offthe.otc.i18n.appShellMoveToPerson
import cloud.offthe.otc.i18n.appShellNotThisPerson
import cloud.offthe.otc.i18n.appPhotosPeopleMoreOptions
import cloud.offthe.otc.i18n.appPhotosPeopleMoreOptionsUnnamed
import cloud.offthe.otc.i18n.appPhotosPersonRemoveText
import cloud.offthe.otc.i18n.commonRemove
import cloud.offthe.otc.i18n.appPhotosWallpaperMenu
import cloud.offthe.otc.i18n.appPhotosWallpaperMenuOn
import androidx.compose.material.icons.outlined.Wallpaper
import cloud.offthe.otc.wallpaper.Wallpaper
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonClose
import cloud.offthe.otc.i18n.commonCreate
import cloud.offthe.otc.i18n.commonDelete
import cloud.offthe.otc.i18n.commonDeselect
import cloud.offthe.otc.i18n.commonDone
import cloud.offthe.otc.i18n.commonOk
import cloud.offthe.otc.i18n.commonSave
import cloud.offthe.otc.i18n.commonSelect
import cloud.offthe.otc.i18n.commonShare

// Port of PhotoGalleryView (PhotoGallery.swift): the search (TopSearch.kt:
// tags, people and files, as the web's top bar), the People button and page
// (PeopleView.kt, while face recognition is on), the group chip and sheet, the
// adaptive grid with long-press selection (a drag from there selects the
// photos it passes, issue #200), the date scrubber overlay,
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
    // Not this person's question, and Someone else…'s list, for the
    // selection (the one person whose photos show: State.personShown).
    var confirmNotPerson by remember { mutableStateOf(false) }
    var pickPerson by remember { mutableStateOf(false) }
    // The open collection as the phone's wallpaper: its box (WallpaperBox.kt).
    var wallpaperFor by remember { mutableStateOf<ImageGroup?>(null) }
    val wallpaper by Wallpaper.active.collectAsState()
    val wallpaperProgress by Wallpaper.progress.collectAsState()
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
        // Back: the selection goes first, as the pill's × does (the web's
        // selection bar has its ✕ too). An open search panel's own Back
        // comes before it, and the viewer's and dialogs' (their windows).
        BackHandler(enabled = st.selected.isNotEmpty() && !search.open) { vm.clearSelection() }
        // Wide, the search is in the top bar and People and Collections in
        // the menu. An open collection is the field's first chip either way
        // (its x closes it), as on the web, and its header - name, count,
        // the months it spans - heads the photos.
        if (!wide) Column(Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerLow).padding(top = 8.dp, bottom = 8.dp)) {
            Row(Modifier.padding(start = 8.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                TopSearchField(search, vm, st, options, Modifier.weight(1f).tourTarget(TourTarget.SEARCH), showGroup = true)
                // The ways into People and collections, as the web's menu
                // has them (Images, People, Collections); the tooltips name
                // the icons for sighted users too.
                if (faces == true) {
                    val people: PeopleViewModel = viewModel(key = "people")
                    val peopleLabel = S.appShellSectionPeople().resolve()
                    TooltipBox(positionProvider = TooltipDefaults.rememberPlainTooltipPositionProvider(), tooltip = { PlainTooltip { Text(peopleLabel) } }, state = rememberTooltipState()) {
                        IconButton(onClick = { people.enter(); onPage(GalleryPage.PEOPLE) }, Modifier.tourTarget(TourTarget.PEOPLE_BUTTON)) { Icon(NavIcons.People, peopleLabel) }
                    }
                }
                val collectionsLabel = S.appShellSectionCollections().resolve()
                TooltipBox(positionProvider = TooltipDefaults.rememberPlainTooltipPositionProvider(), tooltip = { PlainTooltip { Text(collectionsLabel) } }, state = rememberTooltipState()) {
                    IconButton(onClick = { onPage(GalleryPage.COLLECTIONS) }, Modifier.tourTarget(TourTarget.COLLECTIONS_BUTTON)) { Icon(NavIcons.Collections, collectionsLabel) }
                }
            }
        }

        // Grid + scrubber + selection bar. Issue #198: where it stops short
        // of the screen's sides is where the date scrubber goes.
        var gridGaps by remember { mutableStateOf(GridGaps()) }
        val ltr = LocalLayoutDirection.current == LayoutDirection.Ltr
        BoxWithConstraints(Modifier.weight(1f).fillMaxWidth().onGloballyPositioned { gridGaps = GridGaps.of(it, ltr) }) {
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
            // Issue #194: after a jump, while newer photos are to come, the
            // room the top month keeps for those of it still to come (from
            // the buckets), so they land there without moving what is on
            // screen. Nothing above a complete month: the spinner says more
            // are coming.
            val above = remember(st.items, dated, st.upOn, buckets) {
                val first = st.items.firstOrNull()
                if (dated && st.upOn && first != null) roomAbove(first.month, first.up, buckets) else null
            }
            val layout = remember(st.items, dated, geo, monthCounts, st.endReached, above) {
                val n = st.items.size
                val runs = if (dated) monthRuns(st.items.map { it.month }) else if (n > 0) listOf(MonthRun(null, 0, n)) else emptyList()
                layoutGallery(runs, dated, geo, monthCounts, more = !st.endReached, above = above)
            }
            // Grey tiles: the scrubber's month under its title, or the first
            // page on its way (a grey title too, in date order) - from the
            // moment a search starts, so never a blank page. A first page
            // that failed says so in their place (GalleryBody.PROBLEM).
            val ph = st.placeholderCount
            val body = st.body
            val skeleton = body == GalleryBody.SKELETON
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
            // Each row's key: its month and place in it in date order (stable
            // while photos land above or fill its grey tiles, issue #194),
            // else its first photo; the grey tiles' own.
            fun rowKey(i: Int): Any {
                val r = shown.rows[i]
                val tag = if (r is TitledRow) "l" else "r"
                if (greyLayout != null) return "g:$tag:${r.start}"
                return layout.keyOf(i)?.let { "m:$it" } ?: "$tag:${st.items.getOrNull(r.start)?.id ?: r.start}"
            }

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
            // Issue #194: the rows as the last frame drew them, to keep the
            // ones on screen in place when photos land above them.
            val drawn = remember { DrawnRows(st.jumpsLanded, st.upLanded) }
            SideEffect {
                // Grey tiles stand for no photo: no place is read off them.
                if (greyLayout != null) { anchor.layout = null; drawn.layout = null; return@SideEffect }
                // A jump's photos landed (the scrubber's grey tiles gone): at
                // the top, its month's title. The newer photos come above it
                // once the person scrolls up (PhotoGalleryViewModel.scrolledUp).
                if (st.jumpsLanded != drawn.jumps && st.items.isNotEmpty()) {
                    drawn.jumps = st.jumpsLanded
                    listState.requestScrollToItem(headerCount + layout.rowOf(st.items.indexOfFirst { !it.up }.coerceAtLeast(0)).coerceAtLeast(0))
                } else if (st.upLanded != drawn.ups || st.upOn != drawn.upOn) {
                    // The room over a row (px): the grid's top over its first, a
                    // line's gap over a title, a tile's gap over a row of tiles.
                    val padPx: (GalleryLayout, Int) -> Int = { l, i ->
                        with(density) { (if (i == 0) metrics.top else if (l.rows.getOrNull(i) is TitledRow) metrics.lineGap else metrics.gap).roundToPx() }
                    }
                    keepRowsInPlace(listState, drawn, layout, st.items, headerCount, padPx)
                }
                drawn.ups = st.upLanded
                drawn.upOn = st.upOn
                drawn.layout = layout
                drawn.items = st.items
                drawn.headerCount = headerCount
                // What the list's place is read against, when it scrolls.
                anchor.layout = layout
                anchor.items = st.items
                anchor.headerCount = headerCount
                anchor.tilePx = geo.tile * density.density
                val oldGeo = lastGeo[0]
                lastGeo[0] = geo
                if (oldGeo != null && oldGeo != geo) anchor.pending = anchor.place
                val (id, tiles) = anchor.pending ?: return@SideEffect
                val row = layout.rowOf(st.items.indexOfFirst { it.id == id })
                if (row < 0 || listState.isScrollInProgress) { anchor.pending = null; return@SideEffect }
                val index = headerCount + row
                val offset = (tiles * geo.tile * density.density).roundToInt()
                if (oldGeo == geo && listState.firstVisibleItemIndex == index && listState.firstVisibleItemScrollOffset == offset) anchor.pending = null
                else listState.requestScrollToItem(index, offset)
            }
            // A new list (a search, a jump): no place in it yet but its top.
            LaunchedEffect(st.searchesStarted, st.jumpsLanded) { anchor.place = null; anchor.pending = null }
            // Issue #194: scrolled up after a jump - the newer photos above
            // may come. However the person scrolls: a drag or fling (at the
            // very top too, where the list itself can't move: its nested
            // scroll still sees it), and anything else that scrolls the list
            // up (TalkBack's scroll, a key), never the grid's own moves.
            val upScroll = remember(vm) {
                object : NestedScrollConnection {
                    override fun onPreScroll(available: Offset, source: NestedScrollSource): Offset {
                        if (available.y > 0f) vm.scrolledUp()
                        return Offset.Zero
                    }
                }
            }
            LaunchedEffect(listState) {
                var last = listState.firstVisibleItemIndex to listState.firstVisibleItemScrollOffset
                snapshotFlow { Triple(listState.isScrollInProgress, listState.firstVisibleItemIndex, listState.firstVisibleItemScrollOffset) }
                    .collect { (moving, index, offset) ->
                        if (moving && (index < last.first || (index == last.first && offset < last.second))) vm.scrolledUp()
                        last = index to offset
                    }
            }
            var searchesSeen by remember { mutableIntStateOf(st.searchesStarted) }
            LaunchedEffect(st.searchesStarted) {
                if (st.searchesStarted != searchesSeen) { searchesSeen = st.searchesStarted; listState.scrollToItem(0) }
            }

            val tileColor = tileBackground()
            val hasSelection = st.selected.isNotEmpty()
            // Issue #200: a long press on a photo selects it, and a drag from
            // there every photo to the one under the finger, in the grid's
            // order (DragSelect.kt). The photo under a point: the row there
            // (the list's own layout), then the tile in it as GalleryRowView
            // draws it (GalleryRow.itemAt). Not over grey tiles.
            var barHeight by remember { mutableIntStateOf(0) }
            val gridPx = remember(geo, density) { GridGeometry(geo.cols, geo.tile * density.density, geo.gap * density.density, geo.secGap * density.density) }
            val padPx = with(density) { metrics.pad.toPx() }
            val firstTopPx = with(density) { metrics.top.toPx() }
            val lineGapPx = with(density) { metrics.lineGap.toPx() }
            val gapPx = with(density) { metrics.gap.toPx() }
            // MonthTitle's line: 4dp over its 20dp, and the gap under it.
            val titlePx = with(density) { (4.dp + 20.dp + metrics.titleBottom + metrics.gap).toPx() }
            val widthPx = constraints.maxWidth.toFloat()
            val drag = rememberDragSelect(
                listState,
                hit = { p ->
                    val info = listState.layoutInfo
                    val at = p.y + info.viewportStartOffset
                    val shownRow = info.visibleItemsInfo.firstOrNull { at >= it.offset && at < it.offset + it.size }
                    val r = shownRow?.let { it.index - headerCount } ?: -1
                    val row = if (greyLayout == null) layout.rows.getOrNull(r) else null
                    if (shownRow == null || row == null) null else {
                        val top = when {
                            r == 0 -> firstTopPx
                            row is TitledRow -> lineGapPx
                            else -> gapPx
                        }
                        val x = if (ltr) p.x - padPx else widthPx - padPx - p.x
                        row.itemAt(x, at - shownRow.offset, gridPx, top, titlePx)?.let { st.items.getOrNull(it)?.path }
                    }
                },
                onBegin = { vm.beginDragSelect(it) }, onMove = { vm.dragSelect(it) }, onEnd = { vm.endDragSelect() }, goesOn = { vm.dragGoesOn() },
                bottomInset = if (hasSelection) barHeight.toFloat() else 0f,
            )
            LazyColumn(state = listState, modifier = Modifier.fillMaxSize().nestedScroll(upScroll).dragSelect(drag, enabled = greyLayout == null)) {
                if (group != null) item(key = "head:group", contentType = "head") {
                    CollectionHeader(
                        group, span = spanLabel(buckets, years = compact).resolve(), compact = compact, metrics = metrics,
                        // Leaving it, not just looking away: the search is the
                        // whole library again.
                        onBack = { vm.leaveGroup(); onPage(GalleryPage.COLLECTIONS) },
                        onRename = { renameGroupName = group.name },
                        onShare = { gallerySource = SharedGallerySource.newBuilder().setGroupId(group.id).build() },
                        onDelete = { confirmDeleteGroup = true },
                        wallpaperOn = wallpaper?.groupId == group.id,
                        onWallpaper = { wallpaperFor = group },
                        wallpaperProgress = wallpaperProgress?.takeIf { it.groupId == group.id && wallpaper?.groupId == group.id },
                    )
                }
                if (personHeader) item(key = "head:people", contentType = "head") {
                    PersonHeader(st.allPeople, st.peopleLoaded, st.selectedPeople, total = if (st.bucketsFresh) st.totalPhotos else null, metrics = metrics)
                }
                val rows = shown.rows
                val grey = greyLayout != null
                items(
                    count = rows.size,
                    key = ::rowKey,
                    contentType = { i -> if (rows[i] is TitledRow) "line" else "tiles" },
                ) { i ->
                    val row = rows[i]
                    val top = when {
                        i == 0 -> metrics.top
                        row is TitledRow -> metrics.lineGap
                        else -> metrics.gap
                    }
                    if (!grey) LaunchedEffect(row.end) { vm.loadMoreIfNeeded(row.end - 1) }
                    // Its tiles and those within reach of the scroll get their
                    // thumbnails; near the top after a jump (issue #194), once
                    // the person scrolled up, it asks for the newer photos -
                    // again after each page of them.
                    if (!grey) {
                        val key = rowKey(i)
                        DisposableEffect(key, row.start, row.end, st.upLanded) {
                            vm.rowShown(key, row.start, row.end)
                            onDispose { vm.rowGone(key) }
                        }
                    }
                    // Grey tiles say nothing to TalkBack but, once, that
                    // photos are on their way (the web's aria-busy "Loading photos").
                    val loading = PHOTOS_LOADING.resolve()
                    val rowSemantics = if (!grey) Modifier else Modifier.clearAndSetSemantics { if (i == 0) contentDescription = loading }
                    GalleryRowView(
                        row, geo, metrics, Modifier.padding(start = metrics.pad, end = metrics.pad, top = top).then(rowSemantics), titleSkeleton = skeleton && ph == null,
                        // Issue #194: the top month's room for its newer photos, said as the grey tiles are.
                        greyTile = { first, cell -> Box(cell.aspectRatio(1f).clip(TILE_SHAPE).background(tileColor).clearAndSetSemantics { if (first) contentDescription = loading }) },
                    ) { k, cell ->
                        if (grey) Box(cell.aspectRatio(1f).clip(TILE_SHAPE).background(tileColor))
                        else {
                            val item = st.items[k]
                            PhotoTile(
                                item, st.thumbKeyOf(item), vm, tilePx, isSelected = item.path in st.selected, hasSelection = hasSelection,
                                onTap = { vm.open(k) }, onToggle = { vm.toggleSelect(item.path) },
                                // The tour's steps about selecting and opening a photo.
                                modifier = cell.tourTarget(if (k == 0) TourTarget.FIRST_PHOTO else null),
                            )
                        }
                    }
                }
                // Nothing to show: why - the first page failed (and is being
                // asked for again), or there is nothing (the web's states).
                if (body == GalleryBody.PROBLEM || body == GalleryBody.EMPTY) item(key = "state:$body", contentType = "state") {
                    if (body == GalleryBody.PROBLEM) PhotosProblem(st.pageProblem ?: LoadProblem.FAILED, st.retrying) { vm.retryPage() }
                    else PhotosEmpty(emptyKind(st), st.activeGroup?.name, onLeaveGroup = { vm.leaveGroup() }, onClearSearch = { vm.clearSearch() })
                }
                // The end of the grid: where the next page shows it is coming,
                // or that it failed, with its Try again (it is asked again
                // meanwhile). A fixed height, so the spinner coming and going
                // moves nothing. Not without rows: the list would hold on to
                // it as the first item when they come, and open at its end.
                if (rows.isNotEmpty()) item(key = "foot", contentType = "foot") {
                    Box(Modifier.fillMaxWidth().heightIn(min = 72.dp).padding(top = 8.dp, bottom = 24.dp), contentAlignment = Alignment.Center) {
                        if (ph == null && st.items.isNotEmpty()) {
                            if (st.loading) CircularProgressIndicator()
                            else if (st.pageProblem != null) MorePhotosProblem { vm.retryPage() }
                        }
                    }
                }
            }
            // The first page is slow to come (a slow link, a busy device):
            // over the grey tiles, that it is still on its way.
            if (st.slowFirstPage && (body == GalleryBody.SKELETON || (body == GalleryBody.GREY && st.scrubFrac == null))) {
                StillWaiting(Modifier.align(Alignment.BottomCenter).padding(bottom = if (st.selected.isNotEmpty()) 104.dp else 24.dp))
            }
            // Issue #194: the newer photos above a jump - a page on its way
            // (a small spinner, as the web's and iOS's), or one that failed
            // (the foot's words and Try again; it is asked again meanwhile):
            // laid over the top of the grid, so nothing under it moves, and
            // only while the top is in sight.
            val upProblem = st.upProblem
            if ((upProblem != null || st.upLoading) && body == GalleryBody.PHOTOS) {
                // Near the top: the first photos (or the top month's grey tiles) on screen.
                val topRow = headerCount + layout.rowOf(minOf(UP_NEAR, st.items.lastIndex)).coerceAtLeast(0)
                val nearTop by remember(topRow) { derivedStateOf { listState.firstVisibleItemIndex <= topRow } }
                if (nearTop && upProblem == null) Surface(
                    Modifier.align(Alignment.TopCenter).padding(top = 8.dp),
                    shape = CircleShape, tonalElevation = 6.dp, shadowElevation = 3.dp,
                ) {
                    CircularProgressIndicator(Modifier.padding(8.dp).size(24.dp), strokeWidth = 2.dp)
                } else if (nearTop) Surface(
                    Modifier.align(Alignment.TopCenter).padding(top = 8.dp, start = 16.dp, end = 16.dp),
                    shape = RoundedCornerShape(24.dp), tonalElevation = 6.dp, shadowElevation = 3.dp,
                ) {
                    Box(Modifier.padding(horizontal = 12.dp, vertical = 2.dp)) {
                        if (st.upLoading) CircularProgressIndicator(Modifier.padding(8.dp).size(24.dp), strokeWidth = 2.dp)
                        else MorePhotosProblem { vm.retryUp() }
                    }
                }
            }
            if (st.showScrubber) {
                PhotoDateScrubber(
                    vm, st, Modifier.align(Alignment.CenterEnd).fillMaxHeight(), scrubberPlacement(gridGaps),
                    onEngage = { scope.launch { listState.scrollToItem(headerCount) } },
                )
            }
            // Not this person and Someone else…: one person's photos, while face recognition is on.
            val personActions = st.personShown != null && faces == true
            if (st.selected.isNotEmpty()) {
                // How much of the grid's bottom it floats over: a drag scrolls from above it.
                Box(Modifier.align(Alignment.BottomCenter).onSizeChanged { barHeight = it.height }) {
                    SelectionActionBar(
                        count = st.selected.size, busy = st.preparing,
                        onShare = { vm.shareSelected(context) }, onDownload = { vm.downloadZip(context) },
                        onDelete = { confirmDeleteSelected = true }, onGroup = { scope.launch { vm.loadGroups() }; showGroupPicker = true },
                        // Only inside a collection: out of it.
                        onUncollect = if (st.activeGroup != null) { { vm.removeSelectionFromGroup() } } else null,
                        // Only among one person's photos: face recognition was wrong.
                        onNotThisPerson = if (personActions) { { confirmNotPerson = true } } else null,
                        onMoveToPerson = if (personActions) { { pickPerson = true } } else null,
                        // Issue #180: the selected photos, as a gallery - no group needed.
                        onGallery = { gallerySource = SharedGallerySource.newBuilder().addAllPaths(st.selected.sorted()).build() },
                        // Out of selection mode: a tap opens a photo again.
                        onClear = { vm.clearSelection() },
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
                    Text(S.appShellSectionCollections().resolve(), style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                    TextButton(onClick = { onPage(GalleryPage.PHOTOS) }) { Text(S.commonDone().resolve()) }
                }
                if (st.groups.isEmpty()) Text(S.appPhotosCollectionsSheetEmpty().resolve(), color = MaterialTheme.colorScheme.onSurfaceVariant)
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
                            Text(S.appPhotosCollectionsPictures(g.fileCount).resolve(), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                        IconButton(onClick = {
                            onPage(GalleryPage.PHOTOS)
                            gallerySource = SharedGallerySource.newBuilder().setGroupId(g.id).build()
                        }) { Icon(Icons.Default.Share, S.appShellShareAsGallery().resolve(), tint = MaterialTheme.colorScheme.primary) }
                    }
                }
            }
        }
    }
    if (showGroupPicker) {
        ModalBottomSheet(onDismissRequest = { showGroupPicker = false }) {
            Column(Modifier.fillMaxWidth().padding(16.dp)) {
                Text(S.appPhotosCollectionsAddTitle(st.selected.size).resolve(), style = MaterialTheme.typography.titleMedium)
                st.groups.forEach { g -> TextButton(onClick = { showGroupPicker = false; scope.launch { vm.addSelectionToGroup(g) } }) { Text(g.name) } }
                TextButton(onClick = { showGroupPicker = false; newGroupName = "" }) { Text(S.appPhotosCollectionsNewItem().resolve()) }
            }
        }
    }
    newGroupName?.let { name ->
        TextDialog(
            S.appPhotosCollectionsNewTitle().resolve(), S.appPhotosCollectionsNewMessage(st.selected.size).resolve(), name,
            S.appPhotosCollectionsNamePlaceholder().resolve(), S.commonCreate().resolve(),
            onChange = { newGroupName = it }, onConfirm = { newGroupName = null; scope.launch { vm.createGroupFromSelection(name) } }, onDismiss = { newGroupName = null },
        )
    }
    renameGroupName?.let { name ->
        TextDialog(
            S.appPhotosCollectionsRename().resolve(), null, name, S.appPhotosCollectionsNamePlaceholder().resolve(), S.commonSave().resolve(),
            onChange = { renameGroupName = it }, onConfirm = { renameGroupName = null; scope.launch { vm.renameActiveGroup(name) } }, onDismiss = { renameGroupName = null },
        )
    }
    gallerySource?.let { src -> SharedGalleryShareFlow(src, onDismiss = { gallerySource = null }) }
    wallpaperFor?.let { g -> WallpaperBox(g, onDismiss = { wallpaperFor = null }) }
    // What the wallpaper says once it started, stopped or failed - only
    // while fresh (it may have happened in the background meanwhile).
    val wallpaperNote by Wallpaper.note.collectAsState()
    wallpaperNote?.let { n ->
        if (System.currentTimeMillis() - n.at > 120_000L) LaunchedEffect(n) { Wallpaper.noteShown(n) }
        else AlertDialog(
            onDismissRequest = { Wallpaper.noteShown(n) },
            text = { Text(n.text.resolve(), color = if (n.error) MaterialTheme.colorScheme.error else Color.Unspecified) },
            confirmButton = { TextButton(onClick = { Wallpaper.noteShown(n) }) { Text(S.commonOk().resolve()) } },
        )
    }
    if (confirmDeleteGroup) {
        ConfirmDialog(
            S.appPhotosCollectionsDeleteTitle(st.activeGroup?.name ?: "").resolve(), S.appPhotosCollectionsDeleteMessage().resolve(),
            S.appPhotosCollectionsDelete().resolve(),
            onConfirm = { confirmDeleteGroup = false; scope.launch { vm.deleteActiveGroup() } }, onDismiss = { confirmDeleteGroup = false },
        )
    }
    if (confirmDeleteSelected) {
        // Issue #201: photos the device holds at several paths ask which
        // to delete: only these, or every copy (only a device that counts them).
        // Picks a jump or a reload took off the grid as they were last
        // shown (pickedItems), so their copies count too.
        val picked = vm.pickedItems()
        val title = S.appPhotosSelectionDeleteTitle(st.selected.size).resolve()
        when (copiesAsk(picked, st.selected.size)) {
            CopiesAsk.SINGLE -> CopiesDialog(
                title, S.appPhotosCopiesSingleText(picked[0].copies ?: 2).resolve(),
                S.appPhotosCopiesOnlyThis().resolve(), S.appPhotosCopiesAll(picked[0].copies ?: 2).resolve(),
                onOnly = { confirmDeleteSelected = false; vm.deleteSelected() }, onAll = { confirmDeleteSelected = false; vm.deleteSelected(everyCopy = true) },
                onDismiss = { confirmDeleteSelected = false },
            )
            CopiesAsk.SEVERAL -> CopiesDialog(
                title, S.appPhotosCopiesSeveralText(withCopies(picked)).resolve(),
                S.appPhotosCopiesOnlyThese().resolve(), S.appPhotosCopiesEvery().resolve(),
                onOnly = { confirmDeleteSelected = false; vm.deleteSelected() }, onAll = { confirmDeleteSelected = false; vm.deleteSelected(everyCopy = true) },
                onDismiss = { confirmDeleteSelected = false },
            )
            CopiesAsk.PLAIN -> ConfirmDialog(
                title, null, S.commonDelete().resolve(),
                onConfirm = { confirmDeleteSelected = false; vm.deleteSelected() }, onDismiss = { confirmDeleteSelected = false },
            )
        }
    }
    st.alert?.let {
        AlertDialog(
            onDismissRequest = { vm.dismissAlert() },
            // Issue #201: a delete's refusals, one paragraph per kind.
            text = { Column(verticalArrangement = Arrangement.spacedBy(12.dp)) { Text(it.resolve()); for (m in st.alertMore) Text(m.resolve()) } },
            confirmButton = { TextButton(onClick = { vm.dismissAlert() }) { Text(S.commonOk().resolve()) } },
        )
    }

    // Not this person: asked first, the photos named by how many and whose.
    // Gone with the selection or the person (another search) meanwhile.
    val personShown = st.personShown
    val personPicks = personShown != null && st.selected.isNotEmpty()
    LaunchedEffect(personPicks) { if (!personPicks) { confirmNotPerson = false; pickPerson = false } }
    if (confirmNotPerson && personPicks) {
        ConfirmDialog(
            notThisPersonTitle(st.personShownListed, st.selected.size).resolve(), S.appPhotosPersonRemoveText().resolve(), S.commonRemove().resolve(),
            onConfirm = { confirmNotPerson = false; vm.takeSelectionOffPerson() }, onDismiss = { confirmNotPerson = false },
        )
    }
    if (pickPerson && personPicks && personShown != null) {
        // Asked again as it opens: whoever was found since is in it.
        LaunchedEffect(Unit) { vm.loadPeople() }
        MoveToPersonSheet(
            st.allPeople, st.peopleLoaded, personShown, st.selected.size,
            onPick = { p -> pickPerson = false; vm.takeSelectionOffPerson(to = p) }, onDismiss = { pickPerson = false },
        )
    }

    st.needsUpdate?.let { DeviceNeedsUpdateDialog(it, onDismiss = { vm.dismissNeedsUpdate() }) }

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
                // Across the dialog, as every other dialog's field: on its own
                // it is only as wide as its text (a short box on the Fold).
                OTCTextField(value = value, onValueChange = onChange, placeholder = { Text(placeholder) }, singleLine = true,
                    modifier = Modifier.fillMaxWidth())
            }
        },
        confirmButton = { TextButton(onClick = onConfirm, enabled = value.isNotBlank()) { Text(confirmLabel) } },
        dismissButton = { TextButton(onClick = onDismiss) { Text(S.commonCancel().resolve()) } },
    )
}

@Composable
fun ConfirmDialog(title: String, message: String?, confirmLabel: String, onConfirm: () -> Unit, onDismiss: () -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss, title = { Text(title) }, text = message?.let { m -> { Text(m) } },
        confirmButton = { TextButton(onClick = onConfirm) { Text(confirmLabel, color = Color(0xFFE53935)) } },
        dismissButton = { TextButton(onClick = onDismiss) { Text(S.commonCancel().resolve()) } },
    )
}

/**
 * Issue #201: deleting a photo the device holds at several paths (or a
 * selection with some): [text] says so, and the two deletes - only what
 * Images shows ([onOnly]) or every copy ([onAll]) - stand one above the
 * other with Cancel, their labels being long.
 */
@Composable
private fun CopiesDialog(title: String, text: String, onlyLabel: String, allLabel: String, onOnly: () -> Unit, onAll: () -> Unit, onDismiss: () -> Unit) {
    val danger = Color(0xFFE53935)
    AlertDialog(
        onDismissRequest = onDismiss, title = { Text(title) }, text = { Text(text) },
        confirmButton = {
            Column(horizontalAlignment = Alignment.End) {
                TextButton(onClick = onOnly) { Text(onlyLabel, color = danger, textAlign = TextAlign.End) }
                TextButton(onClick = onAll) { Text(allLabel, color = danger, textAlign = TextAlign.End) }
                TextButton(onClick = onDismiss) { Text(S.commonCancel().resolve()) }
            }
        },
    )
}

/**
 * A photo of the grid: a tap opens it, or picks it (or drops it) while
 * photos are being picked. A long press is the grid's (issue #200: it
 * selects the photo and starts a drag to select, DragSelect.kt); TalkBack's
 * own long press, and its actions, still pick this one ([onToggle]).
 */
@Composable
private fun PhotoTile(
    item: PhotoGalleryViewModel.Item, thumbKey: String?, vm: PhotoGalleryViewModel, sidePx: Int, isSelected: Boolean, hasSelection: Boolean,
    onTap: () -> Unit, onToggle: () -> Unit, modifier: Modifier = Modifier,
) {
    // On screen: a tile without its thumbnail has it fetched first; one
    // whose thumbnail is no longer kept (Settings cleared the cache) has it
    // fetched again.
    val path = item.path
    DisposableEffect(path, thumbKey == null) {
        vm.tileShown(path, needsThumb = thumbKey == null)
        onDispose { vm.tileGone(path) }
    }
    val bmp = rememberTileThumb(thumbKey, sidePx, onUndecodable = { thumbKey?.let { vm.thumbUndecodable(path, it) } }) {
        thumbKey?.let { k -> vm.loadThumb(k) ?: run { vm.thumbLost(path); null } }
    }
    val video = item.mime.startsWith("video/")
    val pick = if (isSelected) S.commonDeselect().resolve() else S.commonSelect().resolve()
    // The tile's label is worked out only when TalkBack asks for it, in the
    // language of the composition's resources.
    val resources = LocalResources.current
    Box(
        modifier.aspectRatio(1f).clip(TILE_SHAPE).background(tileBackground())
            .clickable(onClickLabel = if (hasSelection) pick else null) { if (hasSelection) onToggle() else onTap() }
            // TalkBack reads a tile as the web's ("Photo, 5 October 2026"),
            // and while photos are being picked whether it is one of them
            // (the web's aria-pressed). Worked out only when asked for.
            .semantics {
                contentDescription = tileLabel(video, item.created, ZoneId.systemDefault()).resolve(resources)
                if (hasSelection) selected = isSelected
                onLongClick(label = pick) { onToggle(); true }
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

// ---- when there are no photos to show -------------------------------------------

/**
 * The first page failed ([problem]: why) and is asked for again meanwhile:
 * "Couldn't load your photos", why in plain words, and Try again - "Trying…"
 * while a retry is on its way. In the grid's place, under any header.
 */
@Composable
private fun PhotosProblem(problem: LoadProblem, retrying: Boolean, onRetry: () -> Unit) {
    GridMessage(
        icon = if (problem == LoadProblem.OFFLINE) Icons.Outlined.WifiOff else Icons.Outlined.CloudOff,
        title = PHOTOS_PROBLEM_TITLE.resolve(), text = photosProblemText(problem).resolve(), announce = true,
    ) {
        Button(onClick = onRetry, enabled = !retrying) {
            if (retrying) {
                CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp, color = LocalContentColor.current)
                Spacer(Modifier.width(8.dp))
                Text(TRYING.resolve())
            } else Text(TRY_AGAIN.resolve())
        }
    }
}

/** Nothing to show (the web's empty states): an empty collection, a search with no match, or no photos at all. */
@Composable
private fun PhotosEmpty(kind: EmptyKind, groupName: String?, onLeaveGroup: () -> Unit, onClearSearch: () -> Unit) {
    when (kind) {
        EmptyKind.COLLECTION -> GridMessage(NavIcons.Collections, S.appPhotosGridEmptyCollectionTitle().resolve(), S.appPhotosGridEmptyCollectionText().resolve()) {
            OutlinedButton(onClick = onLeaveGroup) { Text(S.appShellCollectionsGoToImages().resolve()) }
        }
        EmptyKind.NO_MATCH -> GridMessage(
            Icons.Outlined.Search, S.appPhotosGridNoMatchTitle().resolve(),
            (if (groupName != null) S.appPhotosGridNoMatchIn(groupName) else S.appPhotosGridNoMatchText()).resolve(),
        ) {
            OutlinedButton(onClick = onClearSearch) { Text(S.appShellSearchClear().resolve()) }
        }
        EmptyKind.NO_PHOTOS -> GridMessage(NavIcons.Images, S.appPhotosGridNoPhotosTitle().resolve(), S.appPhotosGridNoPhotosText().resolve())
    }
}

// [announce]: said to TalkBack when it appears or its reason changes (a
// polite live region), as the web's role="status".
@Composable
private fun GridMessage(icon: ImageVector, title: String, text: String, announce: Boolean = false, action: (@Composable () -> Unit)? = null) {
    Column(
        Modifier.fillMaxWidth().padding(horizontal = 32.dp, vertical = 56.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Icon(icon, null, Modifier.size(40.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
        Spacer(Modifier.height(16.dp))
        Text(title, Modifier.semantics { heading() }, style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
        Spacer(Modifier.height(8.dp))
        Text(
            text, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
            textAlign = TextAlign.Center,
            modifier = Modifier.widthIn(max = 420.dp).then(if (announce) Modifier.semantics { liveRegion = LiveRegionMode.Polite } else Modifier),
        )
        if (action != null) {
            Spacer(Modifier.height(20.dp))
            action()
        }
    }
}

/** A later page failed (the web's foot): it is asked for again meanwhile, or now with Try again. */
@Composable
private fun MorePhotosProblem(onRetry: () -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text(
            MORE_PHOTOS_PROBLEM.resolve(), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite },
        )
        TextButton(onClick = onRetry) { Text(TRY_AGAIN.resolve()) }
    }
}

/** The first page is slow to come: a pill over the grey tiles. */
@Composable
private fun StillWaiting(modifier: Modifier) {
    Surface(modifier.semantics { liveRegion = LiveRegionMode.Polite }, shape = CircleShape, tonalElevation = 6.dp, shadowElevation = 3.dp) {
        Row(Modifier.padding(horizontal = 16.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically) {
            CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp)
            Spacer(Modifier.width(10.dp))
            Text(PHOTOS_SLOW.resolve(), style = MaterialTheme.typography.bodyMedium)
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
    var items: List<PhotoGalleryViewModel.Item> = emptyList()
    var headerCount = 0
    var tilePx = 1f
    // The photo (its id: photos landing above it move its index, issue
    // #194) and the offset into its row, in tiles; null at the top.
    var place: Pair<String, Float>? = null
    // The place a change of width is still taking the list back to.
    var pending: Pair<String, Float>? = null

    fun capture(index: Int, offset: Int) {
        val row = layout?.rows?.getOrNull(index - headerCount)
        place = row?.let { r -> items.getOrNull(r.start)?.let { it.id to offset / tilePx.coerceAtLeast(1f) } }
        pending = null
    }
}

/**
 * Issue #194: the grid's rows as the last frame drew them (PhotoGalleryView's
 * SideEffect): the jumps and pages above already handled, and the layout,
 * photos and headers they were drawn with.
 */
private class DrawnRows(var jumps: Int, var ups: Int) {
    var upOn = false
    var layout: GalleryLayout? = null
    var items: List<PhotoGalleryViewModel.Item> = emptyList()
    var headerCount = 0
}

/**
 * Issue #194: newer photos landed above the rows on screen, or the top
 * month's room for them went (the top reached, or a device that can't).
 * The list keeps its first row where it was by its key - a row keeps its
 * key while photos land above it or fill its grey tiles - but where that
 * row is gone (a month the buckets miscounted laid out again, grey tiles no
 * photo came for), or is a header (the list would keep the header and push
 * the photos under it down), the first row of photos on screen that is
 * still there - by its key, or the row its first photo is in now - stays
 * where the frame before ([drawn]) had it: never a jump to other photos.
 * (Nothing left above it, it goes to the top.) A row that was the grid's
 * first has a line's gap over it now, not the grid's top ([padPx]): its
 * photos stay where they were all the same.
 */
private fun keepRowsInPlace(
    list: LazyListState, drawn: DrawnRows, layout: GalleryLayout, items: List<PhotoGalleryViewModel.Item>, headerCount: Int,
    padPx: (GalleryLayout, Int) -> Int,
) {
    val old = drawn.layout ?: return
    val shown = list.layoutInfo.visibleItemsInfo
    val first = shown.firstOrNull() ?: return
    fun rowNow(key: Any): Int = layout.rowWithKey((key as? String)?.removePrefix("m:"))
    // How much more room the row now has over it than it had.
    fun grows(oldRow: Int, row: Int) = padPx(layout, row) - padPx(old, oldRow)
    // A row still there: the list keeps it in place by its key (its top;
    // the photos, should the room over it change).
    val overHeader = first.index < drawn.headerCount
    if (!overHeader) {
        val row = rowNow(first.key)
        if (row >= 0) {
            val more = grows(first.index - drawn.headerCount, row)
            if (more != 0) list.requestScrollToItem(headerCount + row, -first.offset + more)
            return
        }
    }
    for (v in shown) {
        val oldRow = v.index - drawn.headerCount
        if (oldRow !in old.rows.indices) continue
        var row = rowNow(v.key)
        if (row < 0) {
            val r = old.rows[oldRow]
            val id = if (r.end > r.start) drawn.items.getOrNull(r.start)?.id else null
            if (id != null) row = layout.rowOf(items.indexOfFirst { it.id == id })
        }
        if (row < 0) continue
        // Under a header, nothing landed above it: all stays as it is.
        if (!overHeader || headerCount + row != v.index) list.requestScrollToItem(headerCount + row, -v.offset + grows(oldRow, row))
        return
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
    // Issue #194: a grey tile of the room kept above (the row's first one: [first]).
    greyTile: @Composable RowScope.(first: Boolean, Modifier) -> Unit = { _, _ -> },
    cell: @Composable RowScope.(Int, Modifier) -> Unit,
) {
    when (row) {
        is TileRow -> TileCells(row.start, row.end, geo.cols, m.gap, modifier.fillMaxWidth(), row.lead, greyTile, cell)
        is TitledRow -> MonthsSideBySide(row.segments, geo.cols, m, modifier.fillMaxWidth()) { seg ->
            val slots = minOf(seg.slots, geo.cols)
            // One group per month, so TalkBack reads a month's title and
            // then its tiles before the next month's title beside it (the
            // web's order) - not both titles, which share a line, first.
            Column(Modifier.semantics { isTraversalGroup = true }) {
                MonthTitle(seg.month, oneTile = slots == 1, m, skeleton = titleSkeleton)
                TileCells(seg.start, seg.end, slots, m.gap, Modifier.fillMaxWidth(), seg.lead, greyTile, cell)
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

/**
 * Tiles [start, end) in a row [slots] wide, after [lead] grey ones (issue
 * #194): the empty slots keep every tile the size of a full row's.
 */
@Composable
private fun TileCells(
    start: Int, end: Int, slots: Int, gap: Dp, modifier: Modifier, lead: Int,
    greyTile: @Composable RowScope.(first: Boolean, Modifier) -> Unit, cell: @Composable RowScope.(Int, Modifier) -> Unit,
) {
    Row(modifier, horizontalArrangement = Arrangement.spacedBy(gap)) {
        for (g in 0 until lead) greyTile(g == 0, Modifier.weight(1f))
        for (k in start until end) cell(k, Modifier.weight(1f))
        repeat(slots - lead - (end - start)) { Spacer(Modifier.weight(1f)) }
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
        val whole = monthTitle(month).resolve()
        val heading = Modifier.semantics { heading(); contentDescription = whole }
        if (!oneTile) {
            Text(whole, heading, style = style, maxLines = 1, overflow = TextOverflow.Ellipsis)
        } else {
            var short by remember(month) { mutableStateOf(false) }
            Text(
                if (short) monthShort(month).resolve() else whole, heading, style = style, maxLines = 1, softWrap = false,
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
    // The phone's wallpaper (WallpaperBox.kt): this collection is it now
    // ([wallpaperOn]: "Wallpaper settings…", else "Use as wallpaper…"), and
    // how far its copies are while they are stored.
    wallpaperOn: Boolean = false, onWallpaper: () -> Unit = {}, wallpaperProgress: Wallpaper.Progress? = null,
) {
    val colors = MaterialTheme.colorScheme
    val danger = Color(0xFFE53935)
    val rename = S.appPhotosCollectionsRename().resolve()
    val share = S.appShellShareAsGallery().resolve()
    val delete = S.appPhotosCollectionsDelete().resolve()
    val wallpaper = (if (wallpaperOn) S.appPhotosWallpaperMenuOn() else S.appPhotosWallpaperMenu()).resolve()
    Column(Modifier.fillMaxWidth().padding(start = metrics.pad, end = metrics.pad, top = if (metrics.phone) 8.dp else 16.dp, bottom = 4.dp)) {
        Row(
            Modifier.offset(x = (-8).dp).height(36.dp).clip(CircleShape).clickable(onClick = onBack).padding(start = 8.dp, end = 14.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(Icons.AutoMirrored.Filled.ArrowBack, null, Modifier.size(20.dp), tint = colors.onSurfaceVariant)
            Spacer(Modifier.width(6.dp))
            Text(S.appShellSectionCollections().resolve(), fontSize = 14.sp, fontWeight = FontWeight.SemiBold, color = colors.onSurfaceVariant)
        }
        Spacer(Modifier.height(4.dp))
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(if (metrics.phone) 12.dp else 16.dp)) {
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.Top) {
                    // The page's heading (the web's h1); a tap renames it.
                    Text(
                        g.name,
                        Modifier.weight(1f, fill = false).clip(RoundedCornerShape(8.dp))
                            .clickable(onClickLabel = rename, onClick = onRename).semantics { heading() },
                        style = headerTitleStyle(metrics), maxLines = 2, overflow = TextOverflow.Ellipsis,
                    )
                    IconButton(onClick = onRename, Modifier.size(if (metrics.phone) 30.dp else 36.dp)) {
                        Icon(Icons.Outlined.Edit, rename, Modifier.size(20.dp), tint = colors.onSurfaceVariant)
                    }
                }
                Text(
                    (if (span.isEmpty()) itemsLabel(g.fileCount) else S.appPhotosGridCollectionItemsSpan(g.fileCount, span)).resolve(),
                    Modifier.padding(top = 2.dp), fontSize = 14.sp, lineHeight = 20.sp, color = colors.onSurfaceVariant,
                    maxLines = 1, overflow = TextOverflow.Ellipsis,
                )
            }
            if (compact) Row {
                IconButton(onClick = onShare) { Icon(Icons.Outlined.Public, share, tint = colors.onSurfaceVariant) }
                IconButton(onClick = onWallpaper) { Icon(Icons.Outlined.Wallpaper, wallpaper, tint = if (wallpaperOn) colors.primary else colors.onSurfaceVariant) }
                IconButton(onClick = onDelete) { Icon(Icons.Outlined.Delete, delete, tint = danger) }
            } else Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedButton(onClick = onShare, contentPadding = PaddingValues(start = 14.dp, end = 18.dp)) {
                    Icon(Icons.Outlined.Public, null, Modifier.size(20.dp))
                    Spacer(Modifier.width(8.dp))
                    Text(share)
                }
                OutlinedButton(onClick = onWallpaper, contentPadding = PaddingValues(start = 14.dp, end = 18.dp)) {
                    Icon(Icons.Outlined.Wallpaper, null, Modifier.size(20.dp))
                    Spacer(Modifier.width(8.dp))
                    Text(wallpaper)
                }
                OutlinedButton(
                    onClick = onDelete, contentPadding = PaddingValues(start = 14.dp, end = 18.dp),
                    colors = ButtonDefaults.outlinedButtonColors(contentColor = danger),
                ) {
                    Icon(Icons.Outlined.Delete, null, Modifier.size(20.dp))
                    Spacer(Modifier.width(8.dp))
                    Text(delete)
                }
            }
        }
        // Its copies being stored for the wallpaper, or why they weren't.
        wallpaperProgress?.let { CopiesProgress(it, Modifier.widthIn(max = 480.dp)) }
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
                val unnamed = S.appShellSearchUnnamed().resolve()
                Text(
                    peopleTitle(picked.map { it.name.trim().ifEmpty { unnamed } }).resolve(), Modifier.semantics { heading() },
                    style = headerTitleStyle(metrics), maxLines = 2, overflow = TextOverflow.Ellipsis,
                )
                Text(total?.let { S.appPhotosGridPersonItems(it).resolve() } ?: "\u00a0", Modifier.padding(top = 2.dp), fontSize = 14.sp, lineHeight = 20.sp, color = colors.onSurfaceVariant, maxLines = 1)
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

/**
 * Issue #198: where the date scrubber sits on a phone turned on its side
 * (iOS ScrubberPlacement). The grid stays inside the safe area, which beside
 * a camera cutout leaves a band down that side of the screen - so the
 * scrubber, on the grid's edge, was that far in from the screen's, where a
 * thumb coming in from the edge doesn't land. It reaches across the band
 * instead: its rail, its handle and its grab strip at the screen's edge, as
 * on a phone held upright; its year labels and month bubble stay over the
 * grid. And there, at the screen's edge, its ends keep clear of the rounded
 * corners. Only the cutout's band is crossed, never a navigation bar on that
 * side; and nothing changes where the grid already reaches the edge: a phone
 * held upright, a tablet, the side without the cutout. All in dp.
 *
 * Except where the cutout itself is in the way. A punch hole floats in from
 * the edge, and the handle passes outside it (as on an iPhone beside the
 * Dynamic Island); but a notch - a teardrop, a V, a wide notch - runs from
 * the very edge across the band, with no screen and no touch in it, and out
 * there the handle would vanish behind it mid-track and a drag couldn't
 * start there. So the scrubber stops short of any cutout along its track
 * that would come over its handle: its handle and grab strip just inside it,
 * or, where nothing of the band is left, on the grid's edge as before.
 */
internal data class ScrubberPlacement(
    /** How far past the grid's end edge the scrubber reaches. */
    val reach: Float = 0f,
    /** How much shorter the track is at each end than the grid. */
    val top: Float = 0f,
    val bottom: Float = 0f,
) {
    /**
     * A piece of the display cutout (cutoutShapes), in dp: its sides [near]
     * and [far] in from the screen's end edge, its [top] and [bottom] down
     * from the screen's top.
     */
    data class Cutout(val near: Float, val far: Float, val top: Float, val bottom: Float)

    companion object {
        /**
         * How far from the screen's top and bottom the track ends at the
         * screen's side when the screen doesn't say how round its corners
         * are (before Android 12): iOS's, measured on the roundest iPhones.
         */
        const val CORNER_CLEARANCE = 64f

        /** The handle's size, at the scrubber's outer edge (PhotoDateScrubber). */
        const val HANDLE = 10f

        /** The grab strip's width, at the scrubber's outer edge (PhotoDateScrubber). */
        const val GRAB_STRIP = 16f

        /**
         * For a grid [endGap] short of the screen's end side, where a camera
         * cutout's band is [cutoutEnd] wide and a navigation bar's [barsEnd]
         * (never crossed); its top [topGap] below the screen's top and its
         * bottom [bottomGap] above its bottom, on a screen [screenHeight]
         * tall; [clearance] how far from the screen's top and bottom its
         * corners are round; [cutouts] the display cutout's pieces.
         */
        fun of(
            endGap: Float, cutoutEnd: Float, barsEnd: Float, topGap: Float, bottomGap: Float,
            clearance: Float = CORNER_CLEARANCE, screenHeight: Float = 0f, cutouts: List<Cutout> = emptyList(),
        ): ScrubberPlacement {
            val band = minOf(endGap, cutoutEnd) - maxOf(0f, barsEnd)
            // Under a dp is rounding, not a band.
            if (band < 1f) return ScrubberPlacement()
            // Where the scrubber's outer edge - its handle's, its grab
            // strip's - would be, in from the screen's end edge; pushed in
            // past every cutout along the track (the grid's height, and the
            // handle's half past either end) that comes within the handle's
            // width of it. One that leaves the handle room, a punch hole
            // clear of the edge, has the grab strip run past it as an
            // iPhone's has the island.
            val start = endGap - band
            val alongTop = maxOf(0f, topGap) - HANDLE / 2
            val alongBottom = screenHeight - maxOf(0f, bottomGap) + HANDLE / 2
            var edge = start
            var moved = true
            while (moved) {
                moved = false
                for (c in cutouts) {
                    if (c.bottom <= alongTop || c.top >= alongBottom) continue
                    if (c.far > edge && c.near < edge + HANDLE) {
                        edge = c.far
                        moved = true
                    }
                }
            }
            if (edge > start) {
                // Stopped beside a cutout, its grab strip just inside it; that
                // far in from the side the corners don't reach the track.
                val reach = endGap - edge
                return if (reach < 1f) ScrubberPlacement() else ScrubberPlacement(reach = reach.roundToInt().toFloat())
            }
            return ScrubberPlacement(
                reach = band.roundToInt().toFloat(),
                top = maxOf(0f, clearance - maxOf(0f, topGap)).roundToInt().toFloat(),
                bottom = maxOf(0f, clearance - maxOf(0f, bottomGap)).roundToInt().toFloat(),
            )
        }
    }
}

/**
 * Issue #198: how far the grid stops short of the screen's end side, top and
 * bottom, and the screen's size, in pixels.
 */
private data class GridGaps(
    val end: Float = 0f, val top: Float = 0f, val bottom: Float = 0f,
    val width: Float = 0f, val height: Float = 0f,
) {
    companion object {
        fun of(grid: LayoutCoordinates, ltr: Boolean): GridGaps {
            val screen = grid.findRootCoordinates().size
            val b = grid.boundsInRoot()
            return GridGaps(
                end = if (ltr) screen.width - b.right else b.left, top = b.top, bottom = screen.height - b.bottom,
                width = screen.width.toFloat(), height = screen.height.toFloat(),
            )
        }
    }
}

/** Issue #198: the scrubber's place beside a grid [gaps] short of the screen's sides (ScrubberPlacement). */
@Composable
private fun scrubberPlacement(gaps: GridGaps): ScrubberPlacement {
    val density = LocalDensity.current
    val dir = LocalLayoutDirection.current
    val ltr = dir == LayoutDirection.Ltr
    val cutout = WindowInsets.displayCutout
    val bars = WindowInsets.systemBars
    val cutoutEnd = if (ltr) cutout.getRight(density, dir) else cutout.getLeft(density, dir)
    val barsEnd = if (ltr) bars.getRight(density, dir) else bars.getLeft(density, dir)
    val view = LocalView.current
    val clearance = scrubberCornerClearance(view, ltr, density)
    return with(density) {
        // The cutout's shape, read when its band changes (a turn of the
        // phone recomposes this through WindowInsets.displayCutout).
        val cutouts = cutoutShapes(view).map { r ->
            val near = if (ltr) gaps.width - r.right else r.left.toFloat()
            val far = if (ltr) gaps.width - r.left else r.right.toFloat()
            ScrubberPlacement.Cutout(near = near.toDp().value, far = far.toDp().value, top = r.top.toDp().value, bottom = r.bottom.toDp().value)
        }
        ScrubberPlacement.of(
            endGap = gaps.end.toDp().value, cutoutEnd = cutoutEnd.toDp().value, barsEnd = barsEnd.toDp().value,
            topGap = gaps.top.toDp().value, bottomGap = gaps.bottom.toDp().value, clearance = clearance,
            screenHeight = gaps.height.toDp().value, cutouts = cutouts,
        )
    }
}

/**
 * Issue #198: the display cutout's pieces, in the window's pixels: each
 * side's bounding rect, narrowed to the cutout's own outline inside it where
 * the screen gives that (Android 12 on). A bounding rect may run out to the
 * screen's edge past what the cutout covers - the Pixel 8's punch hole, 11 to
 * 39 dp in from the edge, has one from the edge to 50 dp - and only the
 * outline tells a hole clear of the edge from a notch that touches it.
 */
private fun cutoutShapes(view: View): List<Rect> {
    val cutout = view.rootWindowInsets?.displayCutout ?: return emptyList()
    val rects = cutout.boundingRects
    if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) return rects
    val outline = cutout.cutoutPath ?: return rects
    return rects.map { r ->
        val piece = Region()
        if (piece.setPath(outline, Region(r))) piece.bounds else r
    }
}

/**
 * How far from the screen's top and bottom its corners on the end side are
 * round, in dp: their radius, where the screen says (Android 12 on), else
 * [ScrubberPlacement.CORNER_CLEARANCE]. The handle, centred that far up,
 * clears the curve.
 */
private fun scrubberCornerClearance(view: View, ltr: Boolean, density: Density): Float {
    if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) return ScrubberPlacement.CORNER_CLEARANCE
    val insets = view.rootWindowInsets ?: return ScrubberPlacement.CORNER_CLEARANCE
    val top = insets.getRoundedCorner(if (ltr) RoundedCorner.POSITION_TOP_RIGHT else RoundedCorner.POSITION_TOP_LEFT)
    val bottom = insets.getRoundedCorner(if (ltr) RoundedCorner.POSITION_BOTTOM_RIGHT else RoundedCorner.POSITION_BOTTOM_LEFT)
    if (top == null && bottom == null) return ScrubberPlacement.CORNER_CLEARANCE
    return with(density) { maxOf(top?.radius ?: 0, bottom?.radius ?: 0).toDp().value }
}

/** Issue #77/#113: the Google-Photos-style scrubber on the grid's right edge. */
@Composable
private fun PhotoDateScrubber(vm: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State, modifier: Modifier, placement: ScrubberPlacement, onEngage: () -> Unit) {
    val density = LocalDensity.current
    // Issue #198: laid out on the grid's edge as before, moved out by the
    // band it reaches across to the screen's edge (and so as much wider);
    // its track ends clear of the screen's corners there.
    BoxWithConstraints(
        modifier.offset(x = placement.reach.dp)
            .padding(top = placement.top.dp, bottom = placement.bottom.dp)
            .width((64 + placement.reach).dp),
    ) {
        // Labels and the month bubble over the grid, not out in the band.
        val labelEnd = (16 + placement.reach).dp
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
                    modifier = Modifier.align(Alignment.TopEnd).padding(end = labelEnd).offset { IntOffset(0, (px - 6.dp.toPx()).toInt()) },
                )
            }
            val target = st.scrubTarget
            val frac = st.scrubFrac
            if (target != null && frac != null) {
                Text(
                    scrubLabel(target.month).resolve(), style = MaterialTheme.typography.labelMedium, fontWeight = FontWeight.Bold,
                    modifier = Modifier.align(Alignment.TopEnd).padding(end = labelEnd).offset { IntOffset(0, (heightPx * frac - 12.dp.toPx()).toInt()) }
                        .background(MaterialTheme.colorScheme.surfaceContainerHigh, RoundedCornerShape(6.dp)).padding(horizontal = 8.dp, vertical = 4.dp),
                )
                Box(Modifier.align(Alignment.TopEnd).offset { IntOffset(0, (heightPx * frac - 5.dp.toPx()).toInt()) }.size(ScrubberPlacement.HANDLE.dp).background(Color.Yellow, CircleShape))
            }
        }
        Box(
            Modifier.align(Alignment.CenterEnd).width(ScrubberPlacement.GRAB_STRIP.dp).fillMaxHeight().tourTarget(TourTarget.SCRUBBER).pointerInput(heightPx) {
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
    // Here, not in ViewerActions, which moves between the bottom bar and
    // the side column on a rotation: a share or save under way carries on.
    val scope = rememberCoroutineScope()
    var confirmDelete by remember { mutableStateOf(false) }
    // Not this person's question and Someone else…'s list, for the photo open.
    var confirmNotPerson by remember { mutableStateOf(false) }
    var pickPerson by remember { mutableStateOf(false) }
    val idx = st.openIndex ?: return
    // Among one person's photos (Images only: the Files viewer has none),
    // while face recognition is on: the menu with Not this person and
    // Someone else… for the photo open.
    val faces by FaceRecognition.enabled.collectAsState()
    val personShown = st.personShown?.takeIf { faces == true }
    val personMenu: (@Composable () -> Unit)? = personShown?.let {
        {
            ViewerPersonMenu(
                st.personShownListed, enabled = st.preparing == null,
                onNotThisPerson = { confirmNotPerson = true }, onMoveToPerson = { pickPerson = true },
            )
        }
    }
    val pagerState = rememberPagerState(initialPage = idx) { st.items.size }
    // By the photo, not its place: newer ones landing above it (issue #194) leave it open.
    val pageKey: (Int) -> Any = { st.items.getOrNull(it)?.id ?: it }
    // The pager owns the swipe; the view model follows it (so the hi-res
    // fetch and the video start when a page settles), and is followed by
    // it when the index changes for another reason (a delete).
    LaunchedEffect(pagerState.settledPage) { if (pagerState.settledPage != vm.state.value.openIndex) vm.open(pagerState.settledPage) }
    LaunchedEffect(idx) { if (pagerState.currentPage != idx && !pagerState.isScrollInProgress) pagerState.scrollToPage(idx) }

    Dialog(onDismissRequest = { vm.closeModal() }, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        Box(Modifier.fillMaxSize().background(Color.Black)) {
            // Edge to edge, so the toolbar has to step down from under the
            // status bar itself or its buttons can't be tapped - and, in
            // landscape, step in from a camera cutout at the side.
            BoxWithConstraints(Modifier.fillMaxSize().systemBarsPadding().displayCutoutPadding()) {
                // In landscape the controls move to the sides, into the black
                // the photo leaves there anyway, so the photo gets the whole
                // height instead of what a top and a bottom bar left it (the
                // owner's rule: as large as it can be without covering a
                // button). Portrait is unchanged. The pager stays at the same
                // place in the composition either way, so a rotation re-lays
                // it out without disposing it (or restarting the video in it).
                val landscape = controlsBeside(maxWidth, maxHeight)
                Column(Modifier.fillMaxSize()) {
                    if (!landscape) Row(Modifier.fillMaxWidth().padding(8.dp)) {
                        ViewerInfoButton(vm)
                        Spacer(Modifier.weight(1f))
                        personMenu?.invoke()
                        ViewerCloseButton(vm)
                    }
                    Row(Modifier.weight(1f).fillMaxWidth()) {
                        // Leading column: info at the top.
                        if (landscape) Column(Modifier.width(VIEWER_SIDE_COLUMN).fillMaxHeight().padding(top = 8.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                            ViewerInfoButton(vm)
                            personMenu?.invoke()
                        }
                        HorizontalPager(state = pagerState, modifier = Modifier.weight(1f).fillMaxHeight(), beyondViewportPageCount = 1, key = pageKey) { page ->
                            ViewerPage(vm, st, page, isCurrent = page == idx)
                        }
                        // Trailing column: close at the top, share, save and
                        // delete in the middle, under the right thumb.
                        if (landscape) Box(Modifier.width(VIEWER_SIDE_COLUMN).fillMaxHeight()) {
                            Box(Modifier.align(Alignment.TopCenter).padding(top = 8.dp)) { ViewerCloseButton(vm) }
                            Column(Modifier.align(Alignment.Center), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                                ViewerActions(st.saving, onShare = { scope.launch { vm.shareCurrentPhoto(context) } }, onSave = { vm.saveToPhotos(context) }, onDelete = { confirmDelete = true })
                            }
                        }
                    }
                    if (!landscape) Row(Modifier.fillMaxWidth().padding(vertical = 12.dp), horizontalArrangement = Arrangement.spacedBy(48.dp, Alignment.CenterHorizontally)) {
                        ViewerActions(st.saving, onShare = { scope.launch { vm.shareCurrentPhoto(context) } }, onSave = { vm.saveToPhotos(context) }, onDelete = { confirmDelete = true })
                    }
                }
            }
        }
    }
    if (confirmDelete) {
        // A video's says so (issue #199's follow-up), as the web's viewer does.
        val item = st.items.getOrNull(idx)
        val video = item?.mime?.startsWith("video/") == true
        val title = (if (video) S.appPhotosViewerDeleteVideoTitle() else S.appPhotosViewerDeleteTitle()).resolve()
        // Issue #201: held at several paths, it asks which to delete.
        if (item != null && copiesAsk(listOf(item)) == CopiesAsk.SINGLE) {
            val copies = item.copies ?: 2
            CopiesDialog(
                title, S.appPhotosCopiesSingleText(copies).resolve(), S.appPhotosCopiesOnlyThis().resolve(), S.appPhotosCopiesAll(copies).resolve(),
                onOnly = { confirmDelete = false; vm.deleteCurrentPhoto() }, onAll = { confirmDelete = false; vm.deleteCurrentPhoto(everyCopy = true) },
                onDismiss = { confirmDelete = false },
            )
        } else ConfirmDialog(
            title, null, (if (video) S.appPhotosViewerDeleteVideo() else S.appPhotosViewerDelete()).resolve(),
            onConfirm = { confirmDelete = false; vm.deleteCurrentPhoto() }, onDismiss = { confirmDelete = false },
        )
    }
    LaunchedEffect(personShown) { if (personShown == null) { confirmNotPerson = false; pickPerson = false } }
    if (confirmNotPerson && personShown != null) {
        ConfirmDialog(
            notThisPersonTitle(st.personShownListed, 1).resolve(), S.appPhotosPersonRemoveText().resolve(), S.commonRemove().resolve(),
            onConfirm = { confirmNotPerson = false; vm.takeOpenOffPerson() }, onDismiss = { confirmNotPerson = false },
        )
    }
    if (pickPerson && personShown != null) {
        LaunchedEffect(Unit) { vm.loadPeople() }
        MoveToPersonSheet(
            st.allPeople, st.peopleLoaded, personShown, 1,
            onPick = { p -> pickPerson = false; vm.takeOpenOffPerson(to = p) }, onDismiss = { pickPerson = false },
        )
    }
    // Fully open, and scrollable (FileInfoView): half-open, the location
    // map - the last thing in the sheet - sat below the visible part, so
    // it was never drawn nor its tiles fetched.
    if (st.infoOpen) ModalBottomSheet(
        onDismissRequest = { vm.closeInfo() },
        sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true),
    ) { FileInfoView(st.infoLoading, st.infoData) }
}

/** How wide each side column of the viewer is in landscape: a 48 dp button with a little room. */
private val VIEWER_SIDE_COLUMN = 56.dp

/** What the top bar (64 dp) and the bottom one (72 dp) take from the photo's height in portrait. */
private val VIEWER_BARS_HEIGHT = 136.dp

/**
 * Whether the viewer's controls go in columns beside the photo rather than
 * in bars above and below it: only in landscape, and only when that leaves
 * a typical (4:3) landscape photo more room - always on a phone, not on a
 * nearly square screen like an unfolded Fold's (iOS ImageModal.controlsBeside).
 */
private fun controlsBeside(width: Dp, height: Dp): Boolean {
    if (width <= height) return false
    fun area(w: Float, h: Float): Float {
        val unit = minOf(w / 4f, h / 3f).coerceAtLeast(0f)
        return unit * unit * 12f
    }
    return area((width - VIEWER_SIDE_COLUMN * 2).value, height.value) > area(width.value, (height - VIEWER_BARS_HEIGHT).value)
}

@Composable
private fun ViewerInfoButton(vm: PhotoGalleryViewModel) {
    IconButton(onClick = { vm.openInfo() }) { Icon(Icons.Default.Info, S.appPhotosViewerMoreInfo().resolve(), tint = Color.White) }
}

/**
 * The viewer's menu among one person's photos ([person]: as listed, null
 * when not): Not this person and Someone else… for the photo open, named
 * for TalkBack as a face's menu on the People page is.
 */
@Composable
private fun ViewerPersonMenu(person: Person?, enabled: Boolean, onNotThisPerson: () -> Unit, onMoveToPerson: () -> Unit) {
    var open by remember { mutableStateOf(false) }
    val name = personName(person)
    val label = (if (name.isEmpty()) S.appPhotosPeopleMoreOptionsUnnamed() else S.appPhotosPeopleMoreOptions(name)).resolve()
    Box {
        IconButton(onClick = { open = true }, enabled = enabled) { Icon(Icons.Default.MoreVert, label, tint = if (enabled) Color.White else Color.White.copy(alpha = 0.4f)) }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            DropdownMenuItem(
                text = { Text(S.appShellNotThisPerson().resolve()) }, leadingIcon = { Icon(NavIcons.NotThisPerson, null) },
                onClick = { open = false; onNotThisPerson() },
            )
            DropdownMenuItem(
                text = { Text(S.appShellMoveToPerson().resolve()) }, leadingIcon = { Icon(NavIcons.MoveToPerson, null) },
                onClick = { open = false; onMoveToPerson() },
            )
        }
    }
}

@Composable
private fun ViewerCloseButton(vm: PhotoGalleryViewModel) {
    IconButton(onClick = { vm.closeModal() }) { Icon(Icons.Default.Cancel, S.commonClose().resolve(), tint = Color.White) }
}

/**
 * Share, save and delete: a row under the photo in portrait, a column
 * beside it in landscape. [saving] (issue #201): the original is on its
 * way to the phone's library - Save spins, and waits.
 */
@Composable
private fun ViewerActions(saving: Boolean, onShare: () -> Unit, onSave: () -> Unit, onDelete: () -> Unit) {
    IconButton(onClick = onShare) { Icon(Icons.Default.Share, S.commonShare().resolve(), tint = Color.White) }
    if (saving) {
        val label = S.appPhotosViewerSaving().resolve()
        IconButton(onClick = {}, enabled = false, modifier = Modifier.semantics { contentDescription = label }) {
            CircularProgressIndicator(Modifier.size(22.dp), color = Color.White, strokeWidth = 2.dp)
        }
    } else IconButton(onClick = onSave) { Icon(Icons.Default.ArrowCircleDown, S.commonSave().resolve(), tint = Color.White) }
    IconButton(onClick = onDelete) { Icon(Icons.Default.Delete, S.commonDelete().resolve(), tint = Color.White) }
}

@Composable
private fun ViewerPage(vm: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State, page: Int, isCurrent: Boolean) {
    val context = LocalContext.current
    val item = st.items.getOrNull(page) ?: return
    val video = if (isCurrent) st.videoUrl else null
    val path = item.path
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        if (video != null) {
            val player = remember(video) { MediaStream.player(context, video).apply { setMediaItem(MediaItem.fromUri(video)); prepare(); playWhenReady = true } }
            DisposableEffect(video) {
                // What the player can't play (a codec this phone lacks, a
                // stream that broke) goes back to its poster, saying so,
                // rather than a black box.
                val listener = object : Player.Listener {
                    override fun onPlayerError(error: PlaybackException) = vm.videoFailed(path)
                }
                player.addListener(listener)
                onDispose { player.removeListener(listener); player.release() }
            }
            AndroidView(factory = { PlayerView(it).apply { this.player = player; useController = true } }, modifier = Modifier.fillMaxSize())
            return@Box
        }
        val hiRes = st.hiResImages[path]
        // Where the thumbnail stays (the full size failed or can't be
        // decoded, a video that can't be fetched or played): the big one,
        // asked for then. Until the full size comes, the grid's small tile.
        val bigBytes = st.bigThumbs[path]
        val big = rememberOffMain(bigBytes, { null }) { bigBytes?.let { b -> withContext(Dispatchers.Default) { decodeBitmap(b) } } }
        val image = hiRes ?: big ?: item.preview ?: rememberThumb(st.thumbKeyOf(item))
        val unplayable = isCurrent && path in st.unplayable
        if (image == null) {
            if (unplayable) VideoUnplayable(Modifier.align(Alignment.BottomCenter)) { vm.retryVideo() }
            else CircularProgressIndicator(color = Color.White)
            return@Box
        }
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
                LowResPill(loading = path in st.hiResLoading, modifier = Modifier.align(Alignment.BottomEnd).padding(12.dp))
            }
        }
        if (unplayable) VideoUnplayable(Modifier.align(Alignment.BottomCenter)) { vm.retryVideo() }
    }
}

/** Over a video's poster: the player couldn't play it, and Try again. */
@Composable
private fun VideoUnplayable(modifier: Modifier, onRetry: () -> Unit) {
    Row(
        modifier.padding(16.dp).background(Color.Black.copy(alpha = 0.7f), RoundedCornerShape(50)).padding(start = 16.dp, end = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        // The message wraps rather than squeezing the button: measured first
        // at its whole width, it left "Erneut versuchen" no room at all.
        Text(VIDEO_UNPLAYABLE.resolve(), color = Color.White, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f, fill = false).semantics { liveRegion = LiveRegionMode.Polite })
        TextButton(onClick = onRetry) { Text(TRY_AGAIN.resolve(), color = Color.White, maxLines = 1) }
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
        Text(S.appPhotosViewerLowRes().resolve(), color = Color.White, fontSize = 12.sp, fontWeight = FontWeight.SemiBold)
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
        Text(S.appPhotosViewerInfoTitle().resolve(), style = MaterialTheme.typography.titleMedium)
        Spacer(Modifier.height(12.dp))
        when {
            loading -> CircularProgressIndicator()
            info == null -> Text(S.appPhotosViewerInfoNone().resolve(), color = MaterialTheme.colorScheme.onSurfaceVariant)
            else -> {
                val cam = listOf(info.cameraMake, info.cameraModel).filter { it.isNotEmpty() }.joinToString(" ")
                if (cam.isNotEmpty()) InfoRow(S.appPhotosViewerInfoCamera().resolve(), cam)
                if (info.hasTakenAt()) InfoRow(S.appPhotosViewerInfoTaken().resolve(), DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(info.takenAt.seconds * 1000)))
                if (info.width > 0 && info.height > 0) InfoRow(S.appPhotosViewerInfoDimensions().resolve(), "${info.width} × ${info.height}")
                if (info.exposureTime.isNotEmpty()) InfoRow(S.appPhotosViewerInfoExposure().resolve(), info.exposureTime)
                if (info.fNumber.isNotEmpty()) InfoRow(S.appPhotosViewerInfoAperture().resolve(), info.fNumber)
                if (info.iso > 0) InfoRow(S.appPhotosViewerInfoIso().resolve(), "${info.iso}")
                if (info.focalLength.isNotEmpty()) InfoRow(S.appPhotosViewerInfoFocalLength().resolve(), info.focalLength)
                val loc = listOf(info.city, info.country).filter { it.isNotEmpty() }.joinToString(", ")
                if (loc.isNotEmpty()) InfoRow(S.appPhotosViewerInfoLocation().resolve(), loc)
                if (info.hasGps) {
                    // Issue #127: the same 0.05° region around the point the iOS
                    // panel shows, on OpenStreetMap; a tap on the caption opens
                    // the point in the phone's maps app.
                    LocationMap(info.latitude, info.longitude)
                    TextButton(onClick = { Share.openInBrowser(context, "geo:${info.latitude},${info.longitude}?q=${info.latitude},${info.longitude}") }) {
                        Text(S.androidPhotosOpenInMaps("%.5f".format(info.latitude), "%.5f".format(info.longitude)).resolve())
                    }
                }
                if (cam.isEmpty() && !info.hasGps && info.exposureTime.isEmpty()) Text(S.appPhotosViewerInfoNoExif().resolve(), color = MaterialTheme.colorScheme.onSurfaceVariant)
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
