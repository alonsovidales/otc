// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import android.graphics.Bitmap
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.lazy.grid.rememberLazyGridState
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.sleepOrWake
import cloud.offthe.otc.proto.GetFile
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ListImageGroups
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.ui.common.FIRST_PAGE_LIMIT_WITHOUT_THUMBS
import cloud.offthe.otc.ui.common.GridThumbFetcher
import cloud.offthe.otc.ui.common.NEXT_PAGE_LIMIT_WITHOUT_THUMBS
import cloud.offthe.otc.ui.common.TILES_KIND
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.land
import cloud.offthe.otc.ui.common.gridCellPx
import cloud.offthe.otc.ui.common.rememberTileThumb
import cloud.offthe.otc.ui.gallery.FIRST_PHOTO_PAGE_LIMIT
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/*
 * Issue #178: the profile photo can also come from the photos already on
 * the device, not only from this phone. A small full-screen browser - "All
 * photos" or one image group, a grid of thumbnails paged with SearchPhotos
 * the way the Photo Gallery pages them - and a tap fetches the full photo
 * (GetFile; the device hands HEIC over as JPEG) and decodes it for the
 * circle crop, which the caller opens exactly as for a phone pick.
 */

// thumbKey: the tile's thumbnail in ThumbStore (thumbTileKey; null: none yet).
private data class PickItem(val path: String, val thumbKey: String?)

@Composable
fun DevicePhotoPicker(onCancel: () -> Unit, onPicked: (Bitmap) -> Unit) {
    val scope = rememberCoroutineScope()
    var groups by remember { mutableStateOf<List<ImageGroup>>(emptyList()) }
    var groupId by remember { mutableStateOf("") }
    var items by remember { mutableStateOf<List<PickItem>>(emptyList()) }
    // null once the last page is in; "" asks for the first one.
    var token by remember { mutableStateOf<String?>("") }
    var loading by remember { mutableStateOf(false) }
    // Bumped by a chip change so a page for the old group lands nowhere.
    var generation by remember { mutableIntStateOf(0) }
    var fetching by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    // Pages that failed in a row, so the grid's filling asks again after a
    // pause, twice at most, rather than in a tight loop.
    var failures by remember { mutableIntStateOf(0) }
    // A page that then comes through takes this away (not a failed pick's).
    val pageError = "Could not load your photos."
    // Tiles whose bytes failed to decode in this grid: a second time, none.
    val undecodable = remember { HashSet<String>() }
    // The tiles' missing thumbnails (pages come without them, release 113),
    // a batch at a time, the tiles on screen first.
    val tiles = remember {
        GridThumbFetcher(
            scope, ThumbStore,
            send = { b -> OTCConnection.ask(TILES_KIND, OTCConnection.PAGE_TIMEOUT_MS) { it.setReqGetThumbnails(b) } },
            pause = { sleepOrWake(it) },
        ) { landed -> items = items.map { if (landed.containsKey(it.path)) it.copy(thumbKey = landed[it.path]) else it } }
    }

    val grid = rememberLazyGridState()
    // The tiles within reach of the scroll (about 12 before the first on
    // screen and 30 after the last) get their thumbnails.
    fun nearTiles() {
        val v = grid.layoutInfo.visibleItemsInfo
        val first = v.firstOrNull()?.index ?: return
        val last = v.lastOrNull()?.index ?: return
        val from = maxOf(0, first - GridThumbFetcher.REACH_BEFORE)
        val to = minOf(items.size, last + 1 + GridThumbFetcher.REACH_AFTER)
        if (from < to) tiles.near(items.subList(from, to).map { it.path })
    }
    // Settings' Clear thumbnail cache (from another screen): the tiles marked as having none are asked for again.
    LaunchedEffect(Unit) {
        var seen = ThumbStore.clears.value
        ThumbStore.clears.collect { n -> if (n != seen) { seen = n; tiles.forgetNone(); undecodable.clear() } }
    }
    LaunchedEffect(grid) {
        snapshotFlow { grid.layoutInfo.visibleItemsInfo.let { v -> (v.firstOrNull()?.index ?: 0) to (v.lastOrNull()?.index ?: -1) } }
            .collect { nearTiles() }
    }

    suspend fun loadPage() {
        val t = token ?: return
        if (loading) return
        val mine = generation
        loading = true
        try {
            // Bigger pages from a device known to leave the thumbnails out (GridThumbs.kt).
            val firstLimit = if (ThumbStore.omitsThumbnails()) FIRST_PAGE_LIMIT_WITHOUT_THUMBS else FIRST_PHOTO_PAGE_LIMIT
            // The device this page is asked of: what it brings is kept for it only.
            val thumbScope = ThumbStore.scope()
            val resp = OTCConnection.request { e ->
                // A grid: its tiles' small thumbnails (release 111), left out
                // of the page (release 113: from the cache, or GetThumbnails).
                val sp = SearchPhotos.newBuilder().setGroupId(groupId).setIncludeVideos(false).setToken(t).setHave(items.size)
                    .setSmallThumbnails(true).setOmitThumbnails(true)
                // A new search (opening, another chip) gets a first page of
                // its own size; scrolling on, a bigger one.
                sp.limit = if (t.isEmpty()) firstLimit else NEXT_PAGE_LIMIT_WITHOUT_THUMBS
                e.setReqSearchPhotos(sp)
            }
            if (mine != generation) return
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) { error = pageError; failures += 1; return }
            val lof = resp.respListOfFiles
            val landed = ThumbStore.land(lof.filesList, thumbScope)
            if (mine != generation) return
            val seen = items.map { it.path }.toSet()
            items = items + lof.filesList.filter { it.path !in seen }.map { f -> PickItem(f.path, landed.keys[f.path]) }
            // Asked for as their tiles show or come near.
            tiles.note(landed.fetch, landed.refetch)
            nearTiles()
            token = lof.token.ifEmpty { null }
            failures = 0
            if (error == pageError) error = null
        } catch (e: Exception) {
            if (mine == generation) { error = pageError; failures += 1 }
        } finally {
            if (mine == generation) loading = false
        }
    }

    fun selectGroup(id: String) {
        if (id == groupId) return
        generation += 1
        tiles.reset()
        undecodable.clear()
        groupId = id; items = emptyList(); token = ""; loading = false; error = null; failures = 0
        scope.launch { loadPage() }
    }

    fun pick(item: PickItem) {
        if (fetching != null) return
        fetching = item.path; error = null
        scope.launch {
            val bmp = try {
                val resp = OTCConnection.request { it.setReqGetFile(GetFile.newBuilder().setPath(item.path)) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_FILE && resp.respFile.hasContent()) {
                    withContext(Dispatchers.IO) { decodeProfilePhoto(resp.respFile.content.toByteArray()) }
                } else null
            } catch (e: Exception) { null }
            fetching = null
            if (bmp != null) onPicked(bmp) else error = "Could not open that photo."
        }
    }

    // Near the end of the grid (and nothing in flight): fetch the next page.
    // Re-evaluated when a page lands, so a short first page keeps filling.
    // The page is launched apart from this effect: loadPage sets loading,
    // one of its keys, and the restart cancelled the request it had just
    // sent (shown as "Could not load your photos.", then asked again).
    val nearEnd by remember { derivedStateOf { (grid.layoutInfo.visibleItemsInfo.lastOrNull()?.index ?: 0) >= items.size - 12 } }
    LaunchedEffect(nearEnd, loading, token, generation) {
        if (nearEnd && !loading && token != null && items.isNotEmpty() && failures <= 2) {
            if (failures > 0) delay(failures * 1500L)
            scope.launch { loadPage() }
        }
    }

    LaunchedEffect(Unit) {
        launch {
            try {
                val resp = OTCConnection.request { it.setReqListImageGroups(ListImageGroups.newBuilder().setSmallThumbnails(true)) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_IMAGE_GROUPS) groups = resp.respImageGroups.groupsList
            } catch (_: Exception) {}
        }
        loadPage()
    }

    Dialog(onDismissRequest = onCancel, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Column(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.surface).systemBarsPadding()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Text("Choose a photo", style = MaterialTheme.typography.titleLarge, modifier = Modifier.weight(1f).padding(start = 8.dp))
                TextButton(onClick = onCancel) { Text("Cancel") }
            }
            LazyRow(contentPadding = PaddingValues(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                item { FilterChip(selected = groupId == "", onClick = { selectGroup("") }, label = { Text("All photos") }) }
                items(groups, key = { it.id }) { g ->
                    FilterChip(selected = groupId == g.id, onClick = { selectGroup(g.id) }, label = { Text(g.name, maxLines = 1, overflow = TextOverflow.Ellipsis) })
                }
            }
            error?.let { Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp)) }
            BoxWithConstraints(Modifier.weight(1f).fillMaxWidth()) {
                // The tiles' side, as the grid lays them out: what thumbnails decode to.
                val tilePx = if (constraints.hasBoundedWidth) gridCellPx(constraints.maxWidth, LocalDensity.current, 8.dp, 2.dp, minSize = 110.dp)
                    else with(LocalDensity.current) { 220.dp.roundToPx() }
                LazyVerticalGrid(
                    columns = GridCells.Adaptive(110.dp), state = grid, contentPadding = PaddingValues(8.dp),
                    horizontalArrangement = Arrangement.spacedBy(2.dp), verticalArrangement = Arrangement.spacedBy(2.dp), modifier = Modifier.fillMaxSize(),
                ) {
                    items(items, key = { it.path }) { item ->
                        DisposableEffect(item.path, item.thumbKey == null) {
                            tiles.shown(item.path, needsThumb = item.thumbKey == null)
                            onDispose { tiles.gone(item.path) }
                        }
                        PickTile(item, tilePx, busy = fetching == item.path, onLost = {
                            // No longer kept (the cache was cleared): fetched again.
                            items = items.map { if (it.path == item.path) it.copy(thumbKey = null) else it }
                        }, onUndecodable = { key ->
                            // Didn't decode: dropped and fetched again - none the second time.
                            scope.launch { ThumbStore.discard(key) }
                            items = items.map { if (it.path == item.path) it.copy(thumbKey = null) else it }
                            if (!undecodable.add(item.path)) tiles.markNone(item.path)
                        }) { pick(item) }
                    }
                    if (loading) {
                        item(span = { GridItemSpan(maxLineSpan) }) {
                            Box(Modifier.fillMaxWidth().height(60.dp), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
                        }
                    } else if (items.isEmpty() && token == null) {
                        item(span = { GridItemSpan(maxLineSpan) }) {
                            Text("No photos here yet.", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(16.dp))
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun PickTile(item: PickItem, sidePx: Int, busy: Boolean, onLost: () -> Unit, onUndecodable: (String) -> Unit, onTap: () -> Unit) {
    val bmp = rememberTileThumb(item.thumbKey, sidePx, onUndecodable = { item.thumbKey?.let(onUndecodable) }) {
        item.thumbKey?.let { ThumbStore.load(it) ?: run { onLost(); null } }
    }
    Box(Modifier.aspectRatio(1f).background(Color(0x1A808080)).clickable(onClick = onTap)) {
        if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
        if (busy) {
            Box(Modifier.fillMaxSize().background(Color.Black.copy(alpha = 0.45f)), contentAlignment = Alignment.Center) {
                CircularProgressIndicator(Modifier.size(28.dp), color = Color.White, strokeWidth = 3.dp)
            }
        }
    }
}
