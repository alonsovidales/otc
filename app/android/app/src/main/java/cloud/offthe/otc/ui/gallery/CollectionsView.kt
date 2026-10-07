// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import androidx.activity.compose.BackHandler
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.lazy.grid.rememberLazyGridState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.ui.common.NavIcons
import cloud.offthe.otc.ui.common.circlePath
import cloud.offthe.otc.ui.common.gridCellPx
import cloud.offthe.otc.ui.common.outlineIcon
import cloud.offthe.otc.ui.common.rememberTileThumb
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.text.NumberFormat

// Port of the web's CollectionsView.tsx: the Collections page of the wide
// layout (MainView), where the menu has Collections as a section of its
// own - on a phone's narrow window Images' Collections button lists them
// in a sheet instead. Every collection (an image group in the protocol) as
// a card with its cover, as the device lists them; a card opens Images on
// that collection, which is where one is renamed, shared or deleted.

private const val SKELETON_CARDS = 8
// How long the first listing may take before the page offers to ask again.
private const val SLOW_MS = 10_000L

private val numbers: NumberFormat get() = NumberFormat.getIntegerInstance()
private fun itemsLabel(n: Int) = "${numbers.format(n)} ${if (n == 1) "item" else "items"}"
private fun nameOf(g: ImageGroup) = g.name.trim().ifEmpty { "Untitled collection" }

private val ProblemIcon by lazy { outlineIcon("Problem", circlePath(12f, 12f, 9f), "M12 7.5V13M12 16.5h.01", stroke = 1.4f) }

/**
 * The page. [onOpen]: a card was tapped (the gallery has that collection
 * open already): show Images. [onShowPhotos]: "Go to Images" (nothing to
 * show here), and the system back.
 */
@Composable
fun CollectionsView(gallery: PhotoGalleryViewModel, onOpen: () -> Unit, onShowPhotos: () -> Unit) {
    val st by gallery.state.collectAsState()
    val colors = MaterialTheme.colorScheme
    val gridState = rememberLazyGridState()
    // The list is asked for again on every visit (covers and counts change
    // as photos are added elsewhere); what the gallery holds shows meanwhile.
    var asking by remember { mutableStateOf(true) }
    var slow by remember { mutableStateOf(false) }
    var tries by remember { mutableStateOf(0) }
    LaunchedEffect(tries) {
        asking = true
        slow = false
        val timer = launch { delay(SLOW_MS); slow = true }
        gallery.loadGroups()
        timer.cancel()
        asking = false
    }
    val loaded = st.groupsLoaded
    val failed = !loaded && !asking

    BackHandler { onShowPhotos() }

    Column(Modifier.fillMaxSize()) {
        Row(
            Modifier.fillMaxWidth().height(64.dp).background(colors.surfaceContainerLow).padding(start = 20.dp, end = 16.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) { Text("Collections", style = MaterialTheme.typography.titleLarge) }

        Box(Modifier.weight(1f).fillMaxWidth()) {
            when {
                loaded && st.groups.isEmpty() -> StateMessage(
                    art = { Icon(NavIcons.Collections, null, Modifier.size(80.dp), tint = colors.onSurfaceVariant) },
                    title = "No collections yet", text = "Select photos in Images, then choose Add to collection.",
                    action = "Go to Images", onAction = { gallery.showAll(); onShowPhotos() },
                )
                loaded -> BoxWithConstraints(Modifier.fillMaxSize()) {
                    val density = LocalDensity.current
                    // The covers' side, as the grid lays them out: what they decode to.
                    val cellPx = gridCellPx(constraints.maxWidth, density, 24.dp, 16.dp, minSize = 180.dp)
                    LazyVerticalGrid(
                        columns = GridCells.Adaptive(180.dp), state = gridState,
                        contentPadding = PaddingValues(start = 24.dp, end = 24.dp, top = 24.dp, bottom = 64.dp),
                        horizontalArrangement = Arrangement.spacedBy(16.dp), verticalArrangement = Arrangement.spacedBy(24.dp),
                        modifier = Modifier.fillMaxSize(),
                    ) {
                        items(st.groups, key = { it.id }) { g ->
                            CollectionCard(g, cellPx) {
                                gallery.openGroup(g)
                                onOpen()
                            }
                        }
                    }
                }
                failed || slow -> StateMessage(
                    art = { Icon(ProblemIcon, null, Modifier.size(40.dp), tint = colors.onSurfaceVariant) },
                    title = if (failed) "Couldn't load your collections" else "Still waiting for your device",
                    text = if (failed) "Check that your device is online, then try again." else "Your collections are taking longer than usual to load.",
                    action = "Try again", onAction = { tries++ },
                )
                else -> Skeleton()
            }
        }
    }
}

/** A collection: its square cover (the icon when it has none), the name and how many items. */
@Composable
private fun CollectionCard(g: ImageGroup, coverPx: Int, onClick: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    val name = nameOf(g)
    val cover = rememberTileThumb(
        if (g.coverThumbnail.isEmpty) null else "g:${g.id}:${g.coverThumbnail.hashCode()}", coverPx,
    ) { g.coverThumbnail.toByteArray() }
    Column(
        Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).clickable(onClick = onClick)
            .semantics(mergeDescendants = true) { contentDescription = "$name, ${itemsLabel(g.fileCount)}" },
    ) {
        Box(
            Modifier.fillMaxWidth().aspectRatio(1f).clip(RoundedCornerShape(12.dp)).background(colors.surfaceContainerHighest),
            contentAlignment = Alignment.Center,
        ) {
            if (cover != null) Image(cover.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
            else Icon(NavIcons.Collections, null, Modifier.size(36.dp), tint = colors.onSurfaceVariant)
        }
        Text(
            name, Modifier.padding(top = 8.dp), fontSize = 15.sp, lineHeight = 20.sp, fontWeight = FontWeight.SemiBold,
            maxLines = 1, overflow = TextOverflow.Ellipsis, color = colors.onSurface,
        )
        Text(itemsLabel(g.fileCount), fontSize = 13.sp, lineHeight = 20.sp, color = colors.onSurfaceVariant, maxLines = 1)
    }
}

/** Loading: the cards' own boxes, pulsing, so the real ones land in place. */
@Composable
private fun Skeleton() {
    val pulse by rememberInfiniteTransition(label = "pulse").animateFloat(
        0.35f, 0.8f, infiniteRepeatable(tween(700), RepeatMode.Reverse), label = "alpha",
    )
    val fill = MaterialTheme.colorScheme.surfaceContainerHighest
    LazyVerticalGrid(
        columns = GridCells.Adaptive(180.dp), contentPadding = PaddingValues(24.dp),
        horizontalArrangement = Arrangement.spacedBy(16.dp), verticalArrangement = Arrangement.spacedBy(24.dp), userScrollEnabled = false,
        modifier = Modifier.fillMaxSize().semantics { contentDescription = "Loading collections" },
    ) {
        items(SKELETON_CARDS) { i ->
            Column(Modifier.fillMaxWidth().alpha(pulse)) {
                Box(Modifier.fillMaxWidth().aspectRatio(1f).background(fill, RoundedCornerShape(12.dp)))
                Box(Modifier.padding(top = 12.dp).width(listOf(96, 72, 120)[i % 3].dp).height(12.dp).background(fill, RoundedCornerShape(6.dp)))
                Box(Modifier.padding(top = 10.dp).width(56.dp).height(10.dp).background(fill, RoundedCornerShape(5.dp)))
            }
        }
    }
}

@Composable
private fun StateMessage(art: @Composable () -> Unit, title: String, text: String, action: String, onAction: () -> Unit) {
    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 32.dp, vertical = 48.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        art()
        Spacer(Modifier.height(16.dp))
        Text(title, style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
        Spacer(Modifier.height(8.dp))
        Text(text, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center, modifier = Modifier.widthIn(max = 420.dp))
        Spacer(Modifier.height(20.dp))
        Button(onClick = onAction) { Text(action) }
    }
}
