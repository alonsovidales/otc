// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.social

import android.graphics.Bitmap
import android.util.LruCache
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
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
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import cloud.offthe.otc.R
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.viewinterop.AndroidView
import androidx.media3.common.MediaItem
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
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
import cloud.offthe.otc.ui.common.relativeTime
import cloud.offthe.otc.ui.compose.NewPostPickerView
import cloud.offthe.otc.ui.theme.Ember
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.util.UUID
import androidx.compose.foundation.layout.WindowInsets

// Port of SocialFeedView.swift: the feed, PostCard, likers sheet, video
// autoplay (issue #114) with a shared mute preference, carousel paging
// (issue #109), and the empty state (issue #79).

private const val feedMaxWidthDp = 600
private const val feedMinAspect = 4f / 5f
private const val feedMaxAspect = 1.91f
private const val maxMediaHeightFraction = 0.8f

/** Decoded thumbnails by hash, bounded by bytes (see MediaSizeCache on iOS). */
private object MediaCache {
    private val images = object : LruCache<String, Bitmap>(64 shl 20) {
        override fun sizeOf(key: String, value: Bitmap) = value.byteCount
    }
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
        if (idx >= 0) listState.animateScrollToItem(idx + 1)
        vm.consumeScrollTarget()
    }

    // MainView's Scaffold already keeps the tabs below the status bar, so
    // this bar must not pad for it again (it did, and the masthead sat
    // under a status bar's worth of empty space); 48dp, the icons' own
    // height, is as short as the row can be.
    Scaffold(contentWindowInsets = WindowInsets(0), topBar = {
        // Issue #81 (as on iOS): the bar itself carries no title - the logo
        // is the feed's first row, so it scrolls away once reading starts.
        TopAppBar(
            title = {},
            windowInsets = WindowInsets(0),
            expandedHeight = 48.dp,
            actions = {
                IconButton(onClick = { showingFriendships = true }) { Icon(Icons.Default.Group, "Friends") }
                IconButton(onClick = { showingPicker = true }) { Icon(Icons.Outlined.AddCircleOutline, "New post", tint = Ember) }
            },
        )
    }) { pad ->
        Box(Modifier.padding(pad).fillMaxSize()) {
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
        ModalBottomSheet(onDismissRequest = { showingFriendships = false }) {
            Box(Modifier.fillMaxWidth().heightIn(min = 400.dp)) { FriendshipsView(onDone = { showingFriendships = false }) }
        }
    }
}

/** The OTCLogo masthead: 28dp high at the leading edge, like the iOS logoHeader (issue #126). */
@Composable
private fun LogoHeader() {
    Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 2.dp)) {
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

private data class LikersTarget(val isPublication: Boolean, val uuid: String)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun PostCard(
    post: SocialPublication,
    listState: LazyListState,
    index: Int,
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

        if (post.filesCount > 0) PostMedia(post, listState, index)

        Row(Modifier.padding(horizontal = side).padding(top = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = onLikePub, modifier = Modifier.size(36.dp)) {
                Icon(if (post.liked) Icons.Default.Favorite else Icons.Default.FavoriteBorder, "Like", tint = if (post.liked) Color.Red else MaterialTheme.colorScheme.onSurface)
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
                    Icon(if (c.liked) Icons.Default.Favorite else Icons.Default.FavoriteBorder, null, tint = if (c.liked) Color.Red else MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.size(16.dp))
                }
                if (post.own) IconButton(onClick = { commentPendingDelete = c.commentUuid }, modifier = Modifier.size(28.dp)) {
                    Icon(Icons.Default.Delete, null, tint = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.size(16.dp))
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

/** The feed's media box: the first item's ratio, clamped like Instagram's. */
private fun boxAspect(post: SocialPublication): Float {
    val first = post.filesList.firstOrNull() ?: return feedMinAspect
    val bmp = MediaCache.image(first) ?: return feedMinAspect
    if (bmp.width <= 0 || bmp.height <= 0) return feedMinAspect
    return (bmp.width.toFloat() / bmp.height).coerceIn(feedMinAspect, feedMaxAspect)
}

@Composable
private fun PostMedia(post: SocialPublication, listState: LazyListState, index: Int) {
    val aspect = remember(post.uuid) { boxAspect(post) }
    val screenH = LocalConfiguration.current.screenHeightDp
    val maxH = (screenH * maxMediaHeightFraction).dp
    val pagerState = rememberPagerState { post.filesCount }
    // Issue #114: mostly on screen -> play; scrolled away -> pause.
    val visible by remember { derivedStateOf {
        val info = listState.layoutInfo.visibleItemsInfo.firstOrNull { it.index == index } ?: return@derivedStateOf false
        val vp = listState.layoutInfo.viewportEndOffset - listState.layoutInfo.viewportStartOffset
        val top = maxOf(info.offset, 0); val bottom = minOf(info.offset + info.size, vp)
        (bottom - top).toFloat() / info.size >= 0.6f
    } }

    Box(Modifier.fillMaxWidth().aspectRatio(aspect).heightIn(max = maxH).background(Color.Black)) {
        if (post.filesCount > 1) {
            HorizontalPager(state = pagerState, modifier = Modifier.fillMaxSize()) { page ->
                MediaContent(post, post.filesList[page], playing = visible && pagerState.currentPage == page)
            }
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
        val bmp = remember(file.hash) { MediaCache.image(file) }
        if (bmp != null) Image(bmp.asImageBitmap(), null, contentScale = ContentScale.Fit, modifier = Modifier.fillMaxSize())
        else Box(Modifier.fillMaxSize().background(Color(0x14808080)))
    }
}

/** Issue #60/#107/#110/#114: poster + play, streamed when the device offers a URL, autoplay muted. */
@Composable
private fun VideoContent(post: SocialPublication, file: PbFile, playing: Boolean) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var player by remember(file.hash) { mutableStateOf<ExoPlayer?>(null) }
    var loading by remember(file.hash) { mutableStateOf(false) }
    var ended by remember(file.hash) { mutableStateOf(false) }
    val muted = FeedAudio.muted
    val poster = remember(file.hash) { MediaCache.image(file) }

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
                val p = ExoPlayer.Builder(context).build().apply {
                    setMediaItem(MediaItem.fromUri(url)); volume = if (muted) 0f else 1f; prepare(); playWhenReady = true
                    addListener(object : Player.Listener {
                        override fun onPlaybackStateChanged(state: Int) { if (state == Player.STATE_ENDED) ended = true }
                    })
                }
                player = p
            } finally { loading = false }
        }
    }

    LaunchedEffect(playing) { if (playing) { if (player == null) load() else player?.play() } else player?.pause() }
    LaunchedEffect(muted) { player?.volume = if (muted) 0f else 1f }
    DisposableEffect(file.hash) { onDispose { player?.release(); player = null } }

    Box(Modifier.fillMaxSize()) {
        val p = player
        if (p != null) {
            AndroidView(factory = { ctx -> (android.view.LayoutInflater.from(ctx).inflate(R.layout.player_texture, null) as PlayerView).apply { this.player = p } },
                update = { it.player = p }, modifier = Modifier.fillMaxSize().clipToBounds())
            if (ended) IconButton(onClick = { ended = false; p.seekTo(0); p.play() }, modifier = Modifier.align(Alignment.Center).size(72.dp)) {
                Icon(Icons.Default.Replay, "Replay", tint = Color.White, modifier = Modifier.size(56.dp))
            }
        } else {
            if (poster != null) Image(poster.asImageBitmap(), null, contentScale = ContentScale.Crop, modifier = Modifier.fillMaxSize().clickable { load() })
            else Box(Modifier.fillMaxSize().background(Color(0x14808080)).clickable { load() })
            if (loading) CircularProgressIndicator(color = Color.White, modifier = Modifier.align(Alignment.Center))
            else Icon(Icons.Default.PlayCircle, "Play", tint = Color.White, modifier = Modifier.align(Alignment.Center).size(56.dp))
        }
        IconButton(onClick = { FeedAudio.muted = !FeedAudio.muted }, modifier = Modifier.align(Alignment.TopEnd).padding(10.dp).size(32.dp).background(Color(0x73000000), CircleShape)) {
            Icon(if (muted) Icons.Default.VolumeOff else Icons.Default.VolumeUp, "Mute", tint = Color.White, modifier = Modifier.size(16.dp))
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
