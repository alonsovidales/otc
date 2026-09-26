// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Person
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.proto.Notification
import cloud.offthe.otc.proto.NotificationType
import cloud.offthe.otc.ui.common.decodeBitmap
import cloud.offthe.otc.ui.common.relativeTime
import kotlinx.coroutines.launch
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.ui.text.font.FontFamily

// Port of NotificationsBellView.swift (issue #78): the Alerts tab.
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun NotificationsListView(model: NotificationsModel = NotificationsModel) {
    val list by model.notifications.collectAsState()
    val loading by model.loadingList.collectAsState()
    val scope = rememberCoroutineScope()
    var refreshing by remember { mutableStateOf(false) }

    LaunchedEffect(Unit) { model.openPanel() }

    Scaffold(topBar = { TopAppBar(title = { Text("Notifications") }) }) { pad ->
        PullToRefreshBox(
            isRefreshing = refreshing,
            onRefresh = { scope.launch { refreshing = true; model.openPanel(); refreshing = false } },
            modifier = Modifier.padding(pad).fillMaxSize(),
        ) {
            when {
                loading && list.isEmpty() -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
                list.isEmpty() -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    Text("Nothing yet", color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                else -> LazyColumn(Modifier.fillMaxSize()) {
                    items(list, key = { it.uuid }) { n ->
                        if (n.type == NotificationType.NotificationError) ErrorRow(n)
                        else NotificationRow(n) { model.handleTap(n) }
                    }
                }
            }
        }
    }
}

private fun describe(n: Notification): String = when (n.type) {
    NotificationType.NotificationLikePublication -> "liked your post"
    NotificationType.NotificationLikeComment -> "liked your comment"
    NotificationType.NotificationNewComment -> "commented on your post"
    NotificationType.NotificationFriendRequest -> "sent you a friend request"
    NotificationType.NotificationFriendAccepted -> "accepted your friend request"
    else -> ""
}

@Composable
private fun NotificationRow(n: Notification, onTap: () -> Unit) {
    val bg = if (n.acknowledged) Color.Transparent else Color(0x14FFEB3B)
    Row(
        Modifier.fillMaxWidth().background(bg).clickable(onClick = onTap).padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        AvatarView(data = if (n.hasActorImage()) n.actorImage.toByteArray() else null, size = 40.dp)
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(buildAnnotatedString {
                withStyle(SpanStyle(fontWeight = FontWeight.Bold)) { append(n.actorName.ifEmpty { n.actorDomain }) }
                append(" " + describe(n))
            }, style = MaterialTheme.typography.bodyMedium)
            if (n.hasDt()) {
                Text(relativeTime(n.dt.seconds * 1000), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
        if (n.hasThumbnail()) {
            val bmp = remember(n.uuid) { decodeBitmap(n.thumbnail.toByteArray()) }
            if (bmp != null) {
                Image(bmp.asImageBitmap(), null, contentScale = ContentScale.Crop,
                    modifier = Modifier.size(44.dp).clip(RoundedCornerShape(6.dp)))
            }
        }
    }
}

/** Issue #64: a device error, or a group of them (port of errorRow in
 *  NotificationsBellView.swift). One line (the first error's) plus "+N
 *  more"; a tap shows every error's line, the phone's hover. */
@Composable
private fun ErrorRow(n: Notification) {
    var expanded by remember(n.uuid) { mutableStateOf(false) }
    val bg = if (n.acknowledged) Color.Transparent else Color(0x14FFEB3B)
    Row(
        Modifier.fillMaxWidth().background(bg).clickable { expanded = !expanded }.padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.Top,
    ) {
        Box(Modifier.size(40.dp), contentAlignment = Alignment.Center) {
            Icon(Icons.Default.Warning, null, tint = Color(0xFFFF9800), modifier = Modifier.size(26.dp))
        }
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(buildAnnotatedString {
                withStyle(SpanStyle(fontWeight = FontWeight.Bold)) { append(n.title) }
                if (n.occurrences > 1) withStyle(SpanStyle(color = Color.Gray)) { append(" +${n.occurrences - 1} more") }
            }, style = MaterialTheme.typography.bodyMedium)
            if (n.hasDt()) {
                Text(relativeTime(n.dt.seconds * 1000), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            if (expanded && n.details.isNotEmpty()) {
                Text(n.details, style = MaterialTheme.typography.bodySmall.copy(fontFamily = FontFamily.Monospace),
                    color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 4.dp))
            }
        }
        Icon(if (expanded) Icons.Default.KeyboardArrowUp else Icons.Default.KeyboardArrowDown, if (expanded) "Collapse" else "Expand",
            tint = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/** Shared avatar rendering - the friends list, likers and notifications. */
@Composable
fun AvatarView(data: ByteArray?, size: androidx.compose.ui.unit.Dp) {
    val bmp = remember(data?.size) { data?.let { decodeBitmap(it) } }
    if (bmp != null) {
        Image(bmp.asImageBitmap(), null, contentScale = ContentScale.Crop, modifier = Modifier.size(size).clip(CircleShape))
    } else {
        Box(Modifier.size(size).clip(CircleShape).background(MaterialTheme.colorScheme.surfaceVariant), contentAlignment = Alignment.Center) {
            Icon(Icons.Default.Person, null, tint = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.size(size * 0.6f))
        }
    }
}
