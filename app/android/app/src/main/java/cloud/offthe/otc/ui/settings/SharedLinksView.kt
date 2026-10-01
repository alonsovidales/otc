// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.DeleteSharedLink
import cloud.offthe.otc.proto.ListSharedLinks
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SharedLinkInfo
import cloud.offthe.otc.ui.common.formatBytes
import cloud.offthe.otc.ui.common.relativeTime
import com.google.protobuf.Timestamp
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import java.text.DateFormat
import java.util.Date

/*
 * Port of SharedLinksView.swift (issue #180): Settings > Shared Links, every
 * share link the device holds - galleries and the zips of "Share" in Files
 * and Images - with when it was made, when it expires, how often it was
 * opened and what it takes on disk. Deleting one removes the shared copies
 * and the link stops working. The links themselves can't be shown again:
 * the device never stores their secret.
 */

class SharedLinksViewModel : ViewModel() {
    data class State(
        val links: List<SharedLinkInfo> = emptyList(),
        val loading: Boolean = false,
        val loaded: Boolean = false,
        val error: String? = null,
        val confirmDelete: SharedLinkInfo? = null,
        val deleting: String? = null,
    )
    val state = MutableStateFlow(State())

    suspend fun load() {
        state.update { it.copy(loading = true, error = null) }
        try {
            val resp = OTCConnection.request { it.setReqListSharedLinks(ListSharedLinks.getDefaultInstance()) }
            when {
                resp.payloadCase == RespEnvelope.PayloadCase.RESP_SHARED_LINKS ->
                    state.update { it.copy(links = resp.respSharedLinks.linksList, loaded = true) }
                resp.error -> state.update { it.copy(error = resp.errorMessage.ifEmpty { "Could not load the shared links" }) }
                else -> state.update { it.copy(error = "Unexpected response") }
            }
        } catch (e: Exception) {
            state.update { it.copy(error = e.message ?: "Could not load the shared links") }
        } finally {
            state.update { it.copy(loading = false) }
        }
    }

    fun askDelete(link: SharedLinkInfo?) = state.update { it.copy(confirmDelete = link) }

    suspend fun delete(link: SharedLinkInfo) {
        state.update { it.copy(confirmDelete = null, deleting = link.uuid) }
        try {
            val resp = OTCConnection.request { it.setReqDeleteSharedLink(DeleteSharedLink.newBuilder().setUuid(link.uuid)) }
            if (resp.error) state.update { it.copy(error = resp.errorMessage.ifEmpty { "Could not delete the link" }) }
            else if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && !resp.respAck.ok)
                state.update { it.copy(error = resp.respAck.errorMsg.ifEmpty { "Could not delete the link" }) }
        } catch (e: Exception) {
            state.update { it.copy(error = e.message ?: "Could not delete the link") }
        } finally {
            state.update { it.copy(deleting = null) }
        }
        load()
    }
}

private fun formatDate(ts: Timestamp): String =
    DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(ts.seconds * 1000))

private fun linkTitle(l: SharedLinkInfo): String = when {
    l.description.isNotEmpty() -> l.description
    l.kind == "archive" -> "Shared files (zip)"
    else -> "Shared gallery"
}

/** Full screen, over Settings - the counterpart of iOS's pushed list. */
@Composable
fun SharedLinksView(onClose: () -> Unit) {
    val vm: SharedLinksViewModel = viewModel(key = "sharedLinks")
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    LaunchedEffect(Unit) { vm.load() }

    Dialog(onDismissRequest = onClose, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Column(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.surface).systemBarsPadding()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Text("Shared Links", style = MaterialTheme.typography.titleLarge, modifier = Modifier.weight(1f).padding(start = 8.dp))
                if (st.loading) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                TextButton(onClick = onClose) { Text("Done") }
            }
            st.error?.let { Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp)) }
            if (st.loaded && st.links.isEmpty()) {
                Box(Modifier.weight(1f).fillMaxWidth().padding(24.dp), contentAlignment = Alignment.Center) {
                    Column(horizontalAlignment = Alignment.CenterHorizontally) {
                        Text("No shared links", style = MaterialTheme.typography.titleMedium)
                        Text("Links made with Share or Share as Gallery in Images and Files show up here until they expire.",
                            style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                            modifier = Modifier.padding(top = 6.dp))
                    }
                }
            } else {
                LazyColumn(Modifier.weight(1f).fillMaxWidth(), contentPadding = PaddingValues(bottom = 16.dp)) {
                    items(st.links, key = { it.uuid }) { l ->
                        SharedLinkRow(l, deleting = st.deleting == l.uuid, onDelete = { vm.askDelete(l) })
                        HorizontalDivider(Modifier.padding(horizontal = 16.dp))
                    }
                }
            }
        }

        st.confirmDelete?.let { l ->
            AlertDialog(
                onDismissRequest = { vm.askDelete(null) },
                title = { Text("Delete this link?") },
                text = { Text("The shared copies are deleted and the link stops working.") },
                confirmButton = { TextButton(onClick = { scope.launch { vm.delete(l) } }) { Text("Delete", color = Color(0xFFE53935)) } },
                dismissButton = { TextButton(onClick = { vm.askDelete(null) }) { Text("Cancel") } },
            )
        }
    }
}

@Composable
private fun SharedLinkRow(l: SharedLinkInfo, deleting: Boolean, onDelete: () -> Unit) {
    Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 4.dp, top = 10.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(linkTitle(l), style = MaterialTheme.typography.bodyLarge, maxLines = 2, overflow = TextOverflow.Ellipsis)
            val caption = MaterialTheme.typography.bodySmall
            val muted = MaterialTheme.colorScheme.onSurfaceVariant
            if (l.hasCreated()) Text("Created ${formatDate(l.created)}", style = caption, color = muted)
            if (l.hasExpires()) Text("Expires ${formatDate(l.expires)}", style = caption, color = muted)
            Text(
                if (l.opens > 0) "Opened ${l.opens} time${if (l.opens == 1) "" else "s"}" +
                    (if (l.hasLastOpened()) ", last ${relativeTime(l.lastOpened.seconds * 1000)}" else "")
                else "Never opened",
                style = caption, color = muted,
            )
            Text("${formatBytes(l.bytes)} · ${l.files} file${if (l.files == 1) "" else "s"}", style = caption, color = muted)
        }
        Box(Modifier.size(48.dp), contentAlignment = Alignment.Center) {
            if (deleting) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
            else IconButton(onClick = onDelete) { Icon(Icons.Default.Delete, "Delete link", tint = Color(0xFFE53935)) }
        }
    }
}
