// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.social

import android.graphics.Bitmap
import androidx.compose.material3.rememberModalBottomSheetState
import android.util.LruCache
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.ime
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Favorite
import androidx.compose.material.icons.filled.FavoriteBorder
import androidx.compose.material.icons.filled.Group
import androidx.compose.material.icons.filled.MoreHoriz
import androidx.compose.material.icons.filled.PlayCircle
import androidx.compose.material.icons.filled.Replay
import androidx.compose.material.icons.filled.VolumeOff
import androidx.compose.material.icons.filled.VolumeUp
import androidx.compose.material.icons.outlined.AddCircleOutline
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.res.painterResource
import cloud.offthe.otc.R
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.viewinterop.AndroidView
import androidx.media3.common.MediaItem
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.ui.AspectRatioFrameLayout
import androidx.media3.ui.PlayerView
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.net.MediaStream
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.File as PbFile
import cloud.offthe.otc.proto.GetPublicationMedia
import cloud.offthe.otc.proto.Profile
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SocialPublication
import cloud.offthe.otc.ui.AvatarView
import cloud.offthe.otc.ui.common.decodeBitmap
import cloud.offthe.otc.ui.common.imageAspect
import cloud.offthe.otc.ui.common.relativeTime
import cloud.offthe.otc.ui.common.rememberOffMain
import cloud.offthe.otc.ui.compose.NewPostPickerView
import cloud.offthe.otc.ui.theme.Ember
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.util.UUID
import kotlin.math.roundToInt
import androidx.compose.foundation.layout.WindowInsets

// Port of SocialFeedView.swift: the feed, PostCard, likers sheet, video
// autoplay (issue #114) with a shared mute preference, carousel paging
// (issue #109), and the empty state (issue #79).

private const val feedMaxWidthDp = 600
/** A post's media box is never wider than this; there is no lower bound
 *  (the height cap, see rememberPostCap, keeps a tall photo on screen). */
private const val feedMaxAspect = 1.91f
/** The box's shape only while the first item's is unknown (no thumbnail). */
private const val feedUnknownAspect = 4f / 5f
/** The feed's top bar, which the posts scroll under. */
private val feedTopBarHeight = 48.dp
/** Left under a post's media when its header sits just under the top bar. */
private val mediaCapMargin = 8.dp

/** Decoded thumbnails by hash, bounded by bytes (see MediaSizeCache on iOS). */
private object MediaCache {
    private val images = object : LruCache<String, Bitmap>(64 shl 20) {
        override fun sizeOf(key: String, value: Bitmap) = value.byteCount
    }
    /** Already decoded, or null: never decodes, so it is cheap on the main thread. */
    fun peek(hash: String): Bitmap? = images.get(hash)
    /** Decodes on a miss: call it off the main thread. */
    fun image(file: PbFile): Bitmap? {
        images.get(file.hash)?.let { return it }
        if (!file.hasContent()) return null
        val bmp = decodeBitmap(file.content.toByteArray()) ?: return null
        images.put(file.hash, bmp)
        return bmp
    }
}

/** Issue #114: whether feed videos are muted, shared by every post. */
object FeedAudio {
    var muted by mutableStateOf(true)
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SocialFeedView() {
    val vm = SocialFeedViewModel
    val st by vm.state.collectAsState()
    val deepLink by NotificationsModel.pendingDeepLink.collectAsState()
    val scope = rememberCoroutineScope()
    var showingPicker by remember { mutableStateOf(false) }
    var showingFriendships by remember { mutableStateOf(false) }
    var refreshing by remember { mutableStateOf(false) }
    val listState = rememberLazyListState()
    val density = LocalDensity.current

    LaunchedEffect(deepLink) {
        when (val l = deepLink) {
            is NotificationsModel.DeepLink.Post -> { vm.openPost(l.pubUuid, l.commentUuid); NotificationsModel.consumeDeepLink() }
            is NotificationsModel.DeepLink.FriendRequests -> { showingFriendships = true; NotificationsModel.consumeDeepLink() }
            null -> {}
        }
    }
    LaunchedEffect(st.scrollTargetPub) {
        val target = st.scrollTargetPub ?: return@LaunchedEffect
        val idx = st.posts.indexOfFirst { it.uuid == target }
        // The bar is over the feed, not above it: land the post's header
        // just under the bar (the cap keeps its whole media below it), not
        // under the bar at the feed's top.
        if (idx >= 0) listState.animateScrollToItem(idx + 1, -with(density) { feedTopBarHeight.roundToPx() })
        vm.consumeScrollTarget()
    }

    // MainView's Scaffold already keeps the tabs below the status bar, so
    // this bar must not pad for it again (it did, and the masthead sat
    // under a status bar's worth of empty space); 48dp, the icons' own
    // height, is as short as the row can be.
    // The bar is see-through at the top, so the logo - the feed's first
    // row - sits level with its icons as on iOS, and turns solid once the
    // feed scrolls under it. From the list's position, not a scroll
    // behaviour: that only sees drags, and a post opened from a
    // notification is scrolled to in code, which left the bar see-through
    // over the post above it.
    val underBar by remember { derivedStateOf { listState.firstVisibleItemIndex > 0 || listState.firstVisibleItemScrollOffset > 0 } }
    val solidBar = TopAppBarDefaults.topAppBarColors().scrolledContainerColor
    Scaffold(contentWindowInsets = WindowInsets(0), topBar = {
        // Issue #81 (as on iOS): the bar itself carries no title - the logo
        // is the feed's first row, so it scrolls away once reading starts.
        TopAppBar(
            title = {},
            windowInsets = WindowInsets(0),
            expandedHeight = feedTopBarHeight,
            colors = TopAppBarDefaults.topAppBarColors(containerColor = if (underBar) solidBar else Color.Transparent),
            actions = {
                IconButton(onClick = { showingFriendships = true }) { Icon(Icons.Default.Group, "Friends") }
                IconButton(onClick = { showingPicker = true }) { Icon(Icons.Outlined.AddCircleOutline, "New post", tint = Ember) }
            },
        )
    }) { pad ->
        // Not padded for the bar: the content starts under it (see above).
        BoxWithConstraints(Modifier.padding(bottom = pad.calculateBottomPadding()).fillMaxSize()) {
            val postCap = rememberPostCap(if (constraints.hasBoundedHeight) maxHeight else LocalConfiguration.current.screenHeightDp.dp)
            when {
                // Issue #22: a real loading state while the first fetch is in
                // flight, distinct from "genuinely no posts" - both under the
                // same masthead as the feed itself.
                st.posts.isEmpty() && st.loading -> Column(Modifier.fillMaxSize()) {
                    LogoHeader()
                    Column(Modifier.fillMaxSize(), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = androidx.compose.foundation.layout.Arrangement.Center) {
                        CircularProgressIndicator(); Spacer(Modifier.height(8.dp)); Text("Loading…", color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                st.posts.isEmpty() -> Column(Modifier.fillMaxSize()) {
                    LogoHeader()
                    EmptyFeed { showingPicker = true }
                }
                else -> PullToRefreshBox(
                    isRefreshing = refreshing,
                    onRefresh = { scope.launch { refreshing = true; vm.loadFeed(); refreshing = false } },
                    modifier = Modifier.fillMaxSize(),
                ) {
                    LazyColumn(state = listState, modifier = Modifier.fillMaxSize().widthIn(max = feedMaxWidthDp.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                        item { LogoHeader() }
                        itemsIndexed(st.posts, key = { _, p -> p.uuid }) { idx, post ->
                            LaunchedEffect(post.uuid) { vm.loadMoreIfNeeded(post) }
                            PostCard(
                                post = post,
                                listState = listState,
                                index = idx + 1,
                                cap = postCap,
                                isHighlighted = post.uuid == st.highlightPub,
                                highlightCommentUuid = if (post.uuid == st.highlightPub) st.highlightComment else null,
                                onLikePub = { scope.launch { vm.likePublication(post.uuid) } },
                                onLikeComment = { c -> scope.launch { vm.likeComment(c) } },
                                onComment = { text -> scope.launch { vm.addComment(post.uuid, text) } },
                                fetchPublicationLikers = { vm.fetchPublicationLikers(it) },
                                fetchCommentLikers = { vm.fetchCommentLikers(it) },
                                onDeletePub = { scope.launch { vm.deletePublication(post.uuid) } },
                                onDeleteComment = { c -> scope.launch { vm.deleteComment(c) } },
                            )
                            HorizontalDivider()
                        }
                        if (st.loadingMore) item { Box(Modifier.fillMaxWidth().padding(16.dp), contentAlignment = Alignment.Center) { CircularProgressIndicator() } }
                    }
                }
            }
        }
    }

    if (showingPicker) {
        NewPostPickerView(onDismiss = { showingPicker = false }, onPosted = { scope.launch { vm.loadFeed() } })
    }
    if (showingFriendships) {
        // Fully open: half a sheet cut the list off, on the Fold's wide
        // screen right at the first friend.
        ModalBottomSheet(onDismissRequest = { showingFriendships = false }, sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)) {
            Box(Modifier.fillMaxWidth().heightIn(min = 400.dp)) { FriendshipsView(onDone = { showingFriendships = false }) }
        }
    }
}

/** The OTCLogo masthead: 28dp high at the leading edge, like the iOS logoHeader (issue #126). */
@Composable
private fun LogoHeader() {
    // 48dp, the top bar's height: level with its icons.
    Row(Modifier.fillMaxWidth().height(48.dp).padding(horizontal = 12.dp), verticalAlignment = Alignment.CenterVertically) {
        Image(
            painter = painterResource(R.drawable.otc_logo),
            contentDescription = "Off The Cloud",
            modifier = Modifier.height(28.dp),
            contentScale = ContentScale.Fit,
        )
    }
}

@Composable
private fun EmptyFeed(onNewPost: () -> Unit) {
    Column(Modifier.fillMaxSize().padding(24.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = androidx.compose.foundation.layout.Arrangement.Center) {
        Text("No social posts", fontSize = 30.sp, fontWeight = FontWeight.Black)
        Spacer(Modifier.height(14.dp))
        Text("Share a photo or a video with your friends - it goes from your device to theirs, with no cloud in between.",
            color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center, modifier = Modifier.widthIn(max = 320.dp))
        Spacer(Modifier.height(24.dp))
        Box(Modifier.size(96.dp).shadow(12.dp, CircleShape).background(Ember, CircleShape).clickable(onClick = onNewPost), contentAlignment = Alignment.Center) {
            Icon(Icons.Default.Add, "New post", tint = Color.White, modifier = Modifier.size(48.dp))
        }
    }
}

/**
 * The most height a post's header and media box may take together: the
 * feed's own height (the status bar, the tab bar and any update banner are
 * already outside it), less the top bar the posts scroll under and a small
 * margin - so a post scrolled up to just under the bar shows its whole
 * media. A tall photo used to run past the bottom of the screen.
 *
 * The keyboard is left out: MainView pads the tabs for it, and typing a
 * comment would otherwise shrink every post under the caret. The height
 * from before it opened is kept; a new window size (rotation, folding,
 * split screen) is taken as it comes.
 */
@Composable
private fun rememberPostCap(feedHeight: Dp): Dp {
    val imeShown = WindowInsets.ime.getBottom(LocalDensity.current) > 0
    val config = LocalConfiguration.current
    val lastWithoutIme = remember(config.screenWidthDp, config.screenHeightDp, config.orientation) { arrayOfNulls<Dp>(1) }
    val height = lastWithoutIme[0]?.takeIf { imeShown } ?: feedHeight.also { if (!imeShown) lastWithoutIme[0] = it }
    return height - feedTopBarHeight - mediaCapMargin
}

private data class LikersTarget(val isPublication: Boolean, val uuid: String)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun PostCard(
    post: SocialPublication,
    listState: LazyListState,
    index: Int,
    cap: Dp,
    isHighlighted: Boolean,
    highlightCommentUuid: String?,
    onLikePub: () -> Unit,
    onLikeComment: (String) -> Unit,
    onComment: (String) -> Unit,
    fetchPublicationLikers: suspend (String) -> List<Profile>,
    fetchCommentLikers: suspend (String) -> List<Profile>,
    onDeletePub: () -> Unit,
    onDeleteComment: (String) -> Unit,
) {
    var commentText by remember { mutableStateOf("") }
    var likersTarget by remember { mutableStateOf<LikersTarget?>(null) }
    var confirmDeletePost by remember { mutableStateOf(false) }
    var commentPendingDelete by remember { mutableStateOf<String?>(null) }
    var menu by remember { mutableStateOf(false) }
    val side = 12.dp

    Column(Modifier.fillMaxWidth().background(if (isHighlighted) Color(0x1FFFEB3B) else Color.Transparent)) {
        // The header and the Spacer under it: HeaderOverMedia measures them
        // first to know how much height the media box has left.
        val header: @Composable () -> Unit = { Column {
            Row(Modifier.padding(horizontal = side).padding(top = 10.dp), verticalAlignment = Alignment.CenterVertically) {
                AvatarView(data = if (post.publisher.hasImage()) post.publisher.image.toByteArray() else null, size = 28.dp)
                Spacer(Modifier.width(8.dp))
                Column(Modifier.weight(1f)) {
                    Text(post.publisher.name.ifEmpty { "User" }, style = MaterialTheme.typography.bodyMedium, fontWeight = FontWeight.Bold)
                    if (post.hasDateTime()) Text(relativeTime(post.dateTime.seconds * 1000), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                if (post.own) {
                    Box {
                        IconButton(onClick = { menu = true }) { Icon(Icons.Default.MoreHoriz, null, tint = MaterialTheme.colorScheme.onSurfaceVariant) }
                        DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                            DropdownMenuItem(text = { Text("Delete Post", color = Color(0xFFE53935)) }, onClick = { menu = false; confirmDeletePost = true })
                        }
                    }
                }
            }
            Spacer(Modifier.height(8.dp))
        } }

        if (post.filesCount > 0) {
            val aspect = remember(post.uuid) { boxAspect(post) }
            HeaderOverMedia(cap, aspect, header) { PostMedia(post, listState, index) }
        } else header()

        Row(Modifier.padding(horizontal = side).padding(top = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = onLikePub, modifier = Modifier.size(36.dp)) {
                Icon(if (post.liked) Icons.Default.Favorite else Icons.Default.FavoriteBorder, if (post.liked) "Unlike" else "Like", tint = if (post.liked) Color.Red else MaterialTheme.colorScheme.onSurface)
            }
        }
        if (post.likes > 0) {
            Text("${post.likes} like${if (post.likes == 1) "" else "s"}", style = MaterialTheme.typography.bodySmall, fontWeight = FontWeight.Bold,
                modifier = Modifier.padding(horizontal = side).clickable { likersTarget = LikersTarget(true, post.uuid) })
        }
        if (post.text.isNotEmpty()) Text(post.text, modifier = Modifier.padding(horizontal = side, vertical = 4.dp))

        post.commentsList.forEach { c ->
            Row(
                Modifier.padding(horizontal = side).fillMaxWidth()
                    .background(if (c.commentUuid == highlightCommentUuid) Color(0x2EFFEB3B) else Color.Transparent, RoundedCornerShape(6.dp))
                    .padding(4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(buildAnnotatedString {
                    withStyle(SpanStyle(fontWeight = FontWeight.Bold)) { append(c.publisher.ifEmpty { "User" }) }
                    append(": " + c.comment)
                }, style = MaterialTheme.typography.bodySmall, modifier = Modifier.weight(1f))
                if (c.likes > 0) Text("${c.likes}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.clickable { likersTarget = LikersTarget(false, c.commentUuid) }.padding(4.dp))
                IconButton(onClick = { onLikeComment(c.commentUuid) }, modifier = Modifier.size(28.dp)) {
                    Icon(if (c.liked) Icons.Default.Favorite else Icons.Default.FavoriteBorder, if (c.liked) "Unlike comment" else "Like comment", tint = if (c.liked) Color.Red else MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.size(16.dp))
                }
                if (post.own || c.own) IconButton(onClick = { commentPendingDelete = c.commentUuid }, modifier = Modifier.size(28.dp)) {
                    Icon(Icons.Default.Delete, "Delete comment", tint = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.size(16.dp))
                }
            }
        }

        Row(Modifier.padding(horizontal = side).padding(bottom = 10.dp, top = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            OTCTextField(value = commentText, onValueChange = { commentText = it }, placeholder = { Text("Add a comment…") }, singleLine = true, modifier = Modifier.weight(1f))
            TextButton(onClick = { onComment(commentText); commentText = "" }, enabled = commentText.isNotBlank()) { Text("Post") }
        }
    }

    likersTarget?.let { target ->
        LikersSheet(target, fetchPublicationLikers, fetchCommentLikers) { likersTarget = null }
    }
    if (confirmDeletePost) AlertDialog(
        onDismissRequest = { confirmDeletePost = false }, title = { Text("Delete this post?") },
        confirmButton = { TextButton(onClick = { confirmDeletePost = false; onDeletePub() }) { Text("Delete Post", color = Color(0xFFE53935)) } },
        dismissButton = { TextButton(onClick = { confirmDeletePost = false }) { Text("Cancel") } },
    )
    commentPendingDelete?.let { uuid ->
        AlertDialog(
            onDismissRequest = { commentPendingDelete = null }, title = { Text("Delete this comment?") },
            confirmButton = { TextButton(onClick = { commentPendingDelete = null; onDeleteComment(uuid) }) { Text("Delete Comment", color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { commentPendingDelete = null }) { Text("Cancel") } },
        )
    }
}

/** A file's own shape, from its thumbnail: decoded, or just its header. */
private fun fileAspect(file: PbFile): Float? =
    MediaCache.peek(file.hash)?.let { if (it.width > 0 && it.height > 0) it.width.toFloat() / it.height else null }
        ?: (if (file.hasContent()) imageAspect(file.content.toByteArray()) else null)

/**
 * The shape of a post's media box: its first item's, so swiping the
 * carousel never resizes the card, and never wider than 1.91:1. There is no
 * lower bound any more: a tall photo or a 9:16 video keeps its shape and
 * HeaderOverMedia caps its height instead (the old 4:5 floor shrank it into
 * a box shorter than the screen had room for).
 */
private fun boxAspect(post: SocialPublication): Float {
    val first = post.filesList.firstOrNull() ?: return feedUnknownAspect
    // From the header alone when it isn't decoded yet: the box needs its
    // height on the first frame, the pixels can come a frame later.
    return (fileAspect(first) ?: return feedUnknownAspect).coerceAtMost(feedMaxAspect)
}

/**
 * A post's header over its media box. The box is as wide as the post, with
 * [aspect]'s shape, but never taller than what [cap] leaves under the
 * header: with the header just under the top bar, the whole media is on
 * screen - in a short window too (split screen), where the box is small
 * rather than running under the tab bar. What it shows is fitted, never
 * cropped, so a box held to the cap is letterboxed. One measure pass,
 * header first, so the box never changes height a frame later - in the
 * LazyColumn that would make the posts jump.
 */
@Composable
private fun HeaderOverMedia(cap: Dp, aspect: Float, header: @Composable () -> Unit, media: @Composable () -> Unit) {
    Layout(contents = listOf(header, media)) { (headerParts, mediaParts), constraints ->
        val width = constraints.maxWidth
        val heads = headerParts.map { it.measure(constraints.copy(minWidth = 0, minHeight = 0, maxHeight = Constraints.Infinity)) }
        val headHeight = heads.sumOf { it.height }
        val room = (cap.roundToPx() - headHeight).coerceAtLeast(1)
        val boxHeight = minOf((width / aspect).roundToInt(), room)
        val boxes = mediaParts.map { it.measure(Constraints.fixed(width, boxHeight)) }
        layout(width, headHeight + boxHeight) {
            var y = 0
            heads.forEach { it.placeRelative(0, y); y += it.height }
            boxes.forEach { it.placeRelative(0, headHeight) }
        }
    }
}

/** The media box's contents; HeaderOverMedia gives it its size. */
@Composable
private fun PostMedia(post: SocialPublication, listState: LazyListState, index: Int) {
    val pagerState = rememberPagerState { post.filesCount }
    // Issue #114: mostly on screen -> play; scrolled away -> pause.
    val visible by remember { derivedStateOf {
        val info = listState.layoutInfo.visibleItemsInfo.firstOrNull { it.index == index } ?: return@derivedStateOf false
        val vp = listState.layoutInfo.viewportEndOffset - listState.layoutInfo.viewportStartOffset
        val top = maxOf(info.offset, 0); val bottom = minOf(info.offset + info.size, vp)
        (bottom - top).toFloat() / info.size >= 0.6f
    } }

    Box(Modifier.fillMaxSize().background(Color.Black)) {
        if (post.filesCount > 1) {
            HorizontalPager(state = pagerState, modifier = Modifier.fillMaxSize()) { page ->
                MediaContent(post, post.filesList[page], playing = visible && pagerState.currentPage == page)
            }
            // At the box's foot, the same place on every page: under a
            // letterboxed item the pill still reads on the black bar.
            Row(Modifier.align(Alignment.BottomCenter).padding(bottom = 8.dp).background(Color(0x4D000000), CircleShape).padding(6.dp)) {
                repeat(post.filesCount) { i ->
                    Box(Modifier.padding(horizontal = 2.dp).size(6.dp).background(if (i == pagerState.currentPage) Color.White else Color(0x66FFFFFF), CircleShape))
                }
            }
        } else {
            MediaContent(post, post.filesList[0], playing = visible)
        }
    }
}

@Composable
private fun MediaContent(post: SocialPublication, file: PbFile, playing: Boolean) {
    if (file.mime.startsWith("video/")) {
        VideoContent(post, file, playing)
    } else {
        val bmp = rememberOffMain(file.hash, { MediaCache.peek(file.hash) }) { withContext(Dispatchers.Default) { MediaCache.image(file) } }
        if (bmp != null) Image(bmp.asImageBitmap(), null, contentScale = ContentScale.Fit, modifier = Modifier.fillMaxSize())
        else Box(Modifier.fillMaxSize().background(Color(0x14808080)))
    }
}

/** Issue #60/#107/#110/#114: poster + play, streamed when the device offers a URL, autoplay muted. */
@androidx.annotation.OptIn(androidx.media3.common.util.UnstableApi::class)
@Composable
private fun VideoContent(post: SocialPublication, file: PbFile, playing: Boolean) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var player by remember(file.hash) { mutableStateOf<ExoPlayer?>(null) }
    var loading by remember(file.hash) { mutableStateOf(false) }
    var ended by remember(file.hash) { mutableStateOf(false) }
    var firstFrame by remember(file.hash) { mutableStateOf(false) }
    val muted = FeedAudio.muted
    val poster = rememberOffMain(file.hash, { MediaCache.peek(file.hash) }) { withContext(Dispatchers.Default) { MediaCache.image(file) } }
    val frameAspect = remember(file.hash) { fileAspect(file) }

    fun load() {
        if (loading || player != null) return
        loading = true
        scope.launch {
            try {
                val url = MediaStream.url(post.uuid, file.hash) ?: withContext(Dispatchers.IO) {
                    val resp = OTCConnection.request { it.setReqGetPublicationMedia(GetPublicationMedia.newBuilder().setPubUuid(post.uuid).setHash(file.hash)) }
                    if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_FILE || !resp.respFile.hasContent()) return@withContext null
                    val ext = when (resp.respFile.mime.lowercase()) { "video/quicktime" -> "mov"; "video/x-matroska" -> "mkv"; "video/3gpp" -> "3gp"; else -> "mp4" }
                    val tmp = File(context.cacheDir, "${UUID.randomUUID()}.$ext")
                    tmp.writeBytes(resp.respFile.content.toByteArray())
                    tmp.toURI().toString()
                } ?: return@launch
                val p = MediaStream.player(context, url).apply {
                    setMediaItem(MediaItem.fromUri(url)); volume = if (muted) 0f else 1f; prepare(); playWhenReady = true
                    addListener(object : Player.Listener {
                        override fun onPlaybackStateChanged(state: Int) { if (state == Player.STATE_ENDED) ended = true }
                        override fun onRenderedFirstFrame() { firstFrame = true }
                    })
                }
                player = p
            } finally { loading = false }
        }
    }

    LaunchedEffect(playing) { if (playing) { if (player == null) load() else player?.play() } else player?.pause() }
    LaunchedEffect(muted) { player?.volume = if (muted) 0f else 1f }
    DisposableEffect(file.hash) { onDispose { player?.release(); player = null } }

    val p = player
    Box(Modifier.fillMaxSize().then(if (p == null) Modifier.clickable { load() } else Modifier), contentAlignment = Alignment.Center) {
        // The video's own frame, fitted into the box - letterboxed, never
        // cropped (the poster was cropped to the box, so pressing play made
        // the picture jump). Poster and player both fill exactly this frame,
        // and the sound toggle sits on the video's corner, not on a bar.
        Box(if (frameAspect != null) Modifier.aspectRatio(frameAspect) else Modifier.fillMaxSize()) {
            if (p != null) {
                // The layout's resize_mode is zoom (cropping); fit here.
                AndroidView(factory = { ctx -> (android.view.LayoutInflater.from(ctx).inflate(R.layout.player_texture, null) as PlayerView).apply {
                        resizeMode = AspectRatioFrameLayout.RESIZE_MODE_FIT; this.player = p
                    } },
                    update = { it.player = p }, modifier = Modifier.fillMaxSize().clipToBounds())
            }
            // Over the player until it has drawn a frame, so play goes from
            // the poster straight to the video, with no black flash between.
            if (p == null || !firstFrame) {
                if (poster != null) Image(poster.asImageBitmap(), null, contentScale = ContentScale.Fit, modifier = Modifier.fillMaxSize())
                else Box(Modifier.fillMaxSize().background(Color(0x14808080)))
            }
            when {
                p == null && !loading -> Icon(Icons.Default.PlayCircle, "Play", tint = Color.White, modifier = Modifier.align(Alignment.Center).size(56.dp))
                p == null || !firstFrame -> CircularProgressIndicator(color = Color.White, modifier = Modifier.align(Alignment.Center))
                ended -> IconButton(onClick = { ended = false; p.seekTo(0); p.play() }, modifier = Modifier.align(Alignment.Center).size(72.dp)) {
                    Icon(Icons.Default.Replay, "Replay", tint = Color.White, modifier = Modifier.size(56.dp))
                }
            }
            IconButton(onClick = { FeedAudio.muted = !FeedAudio.muted }, modifier = Modifier.align(Alignment.TopEnd).padding(10.dp).size(32.dp).background(Color(0x73000000), CircleShape)) {
                Icon(if (muted) Icons.Default.VolumeOff else Icons.Default.VolumeUp, "Mute", tint = Color.White, modifier = Modifier.size(16.dp))
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun LikersSheet(target: LikersTarget, fetchPub: suspend (String) -> List<Profile>, fetchComment: suspend (String) -> List<Profile>, onDismiss: () -> Unit) {
    var likers by remember(target) { mutableStateOf<List<Profile>?>(null) }
    LaunchedEffect(target) { likers = if (target.isPublication) fetchPub(target.uuid) else fetchComment(target.uuid) }
    ModalBottomSheet(onDismissRequest = onDismiss) {
        Column(Modifier.fillMaxWidth().padding(16.dp).heightIn(min = 200.dp)) {
            Text("Likes", style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(12.dp))
            val l = likers
            when {
                l == null -> CircularProgressIndicator()
                l.isEmpty() -> Text("No likes yet", color = MaterialTheme.colorScheme.onSurfaceVariant)
                else -> l.forEach { liker ->
                    Row(Modifier.fillMaxWidth().padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                        AvatarView(data = if (liker.hasImage()) liker.image.toByteArray() else null, size = 36.dp)
                        Spacer(Modifier.width(12.dp))
                        Text(liker.name.ifEmpty { liker.domain })
                    }
                }
            }
        }
    }
}
