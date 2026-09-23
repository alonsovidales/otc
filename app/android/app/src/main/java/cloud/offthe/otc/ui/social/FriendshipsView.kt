// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.social

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.ChangeFriendStatus
import cloud.offthe.otc.proto.DeleteFriendship
import cloud.offthe.otc.proto.FriendShipStatus
import cloud.offthe.otc.proto.Friendship
import cloud.offthe.otc.proto.FriendshipRequest
import cloud.offthe.otc.proto.FriendshipsList
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.AvatarView
import cloud.offthe.otc.ui.common.Toast
import kotlinx.coroutines.GlobalScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

// Port of FriendshipsView.swift: send a friend request by domain and manage
// incoming/outgoing friendships (issue #25: either side can remove a
// pending request).
class FriendshipsViewModel : ViewModel() {
    data class State(
        val targetDomain: String = "",
        val sendingRequest: Boolean = false,
        val friendships: List<Friendship> = emptyList(),
        val loading: Boolean = false,
        val toast: String? = null,
    )
    private val _state = MutableStateFlow(State())
    val state = _state

    fun setTarget(v: String) = _state.update { it.copy(targetDomain = v) }

    suspend fun reload() {
        _state.update { it.copy(loading = true) }
        try {
            val resp = OTCConnection.request { it.setReqFriendshipsList(FriendshipsList.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_FRIENDSHIPS) {
                _state.update { it.copy(friendships = resp.respFriendships.friendshipsList) }
            }
        } catch (e: Exception) { toast("Could not load friendships") }
        finally { _state.update { it.copy(loading = false) } }
    }

    suspend fun sendRequest() {
        val domain = _state.value.targetDomain.trim()
        if (domain.isEmpty()) return
        _state.update { it.copy(sendingRequest = true) }
        try {
            val resp = OTCConnection.request { it.setReqFriendshipRequest(FriendshipRequest.newBuilder().setDomain(domain)) }
            val ack = if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) resp.respAck else null
            when {
                ack?.ok == true -> { toast("Friend request sent ✅"); _state.update { it.copy(targetDomain = "") }; reload() }
                ack != null -> toast(ack.errorMsg.ifEmpty { "Request failed" })
                else -> toast("Unexpected response")
            }
        } catch (e: Exception) { toast("Error sending request") }
        finally { _state.update { it.copy(sendingRequest = false) } }
    }

    suspend fun changeStatus(f: Friendship, status: FriendShipStatus) {
        try {
            val resp = OTCConnection.request { it.setReqChangeFriendStatus(ChangeFriendStatus.newBuilder().setDomain(f.originProfile.domain).setStatus(status)) }
            val ack = if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) resp.respAck else null
            when {
                ack?.ok == true -> { reload(); toast("Status updated ✅") }
                ack != null -> toast(ack.errorMsg.ifEmpty { "Update failed" })
                else -> toast("Unexpected response")
            }
        } catch (e: Exception) { toast("Error updating status") }
    }

    suspend fun delete(f: Friendship) {
        try {
            val resp = OTCConnection.request { it.setReqDeleteFriendship(DeleteFriendship.newBuilder().setDomain(f.originProfile.domain)) }
            val ack = if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) resp.respAck else null
            when {
                ack?.ok == true -> { reload(); toast(if (f.sent) "Request cancelled" else "Request deleted") }
                resp.error -> toast(resp.errorMessage)
                else -> toast("Delete failed")
            }
        } catch (e: Exception) { toast("Error deleting request") }
    }

    private fun toast(m: String) {
        _state.update { it.copy(toast = m) }
        GlobalScope.launch { delay(2500); _state.update { if (it.toast == m) it.copy(toast = null) else it } }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FriendshipsView(onDone: () -> Unit) {
    val vm: FriendshipsViewModel = viewModel()
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    LaunchedEffect(Unit) { vm.reload() }

    Scaffold(topBar = {
        TopAppBar(title = { Text("Friends") }, navigationIcon = { TextButton(onClick = onDone) { Text("Done") } })
    }) { pad ->
        Box(Modifier.padding(pad).fillMaxSize()) {
            LazyColumn(Modifier.fillMaxSize()) {
                item {
                    Text("Add a friend", style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(16.dp, 12.dp, 16.dp, 4.dp))
                    Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                        OTCTextField(
                            value = st.targetDomain, onValueChange = vm::setTarget, singleLine = true,
                            placeholder = { Text("friend-domain.example") },
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, capitalization = KeyboardCapitalization.None),
                            modifier = Modifier.weight(1f),
                        )
                        TextButton(onClick = { scope.launch { vm.sendRequest() } }, enabled = !st.sendingRequest && st.targetDomain.isNotBlank()) { Text("Send") }
                    }
                }
                item {
                    Row(Modifier.fillMaxWidth().padding(16.dp, 20.dp, 16.dp, 4.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text("Friend requests", style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
                        if (st.loading) CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp)
                        else TextButton(onClick = { scope.launch { vm.reload() } }) { Text("Refresh") }
                    }
                }
                if (st.friendships.isEmpty()) {
                    item { Text("No friendships yet.", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(16.dp)) }
                }
                items(st.friendships, key = { it.originProfile.domain }) { f ->
                    FriendRow(f, onChange = { s -> scope.launch { vm.changeStatus(f, s) } }, onDelete = { scope.launch { vm.delete(f) } })
                }
            }
            Toast(st.toast, Modifier.align(Alignment.TopCenter))
        }
    }
}

@Composable
private fun FriendRow(f: Friendship, onChange: (FriendShipStatus) -> Unit, onDelete: () -> Unit) {
    var menu by remember { mutableStateOf(false) }
    var confirmingDelete by remember { mutableStateOf(false) }
    val canDelete = f.status == FriendShipStatus.Pending
    val deleteLabel = if (f.sent) "Cancel request" else "Delete request"
    val statusLabel = when (f.status) { FriendShipStatus.Accepted -> "Accepted"; FriendShipStatus.Blocked -> "Blocked"; else -> "Pending" }
    val options: List<Pair<String, FriendShipStatus>> = if (f.sent) emptyList() else when (f.status) {
        FriendShipStatus.Pending -> listOf("Accept" to FriendShipStatus.Accepted, "Block" to FriendShipStatus.Blocked)
        FriendShipStatus.Accepted -> listOf("Set Pending" to FriendShipStatus.Pending, "Block" to FriendShipStatus.Blocked)
        FriendShipStatus.Blocked -> listOf("Accept" to FriendShipStatus.Accepted, "Set Pending" to FriendShipStatus.Pending)
        else -> emptyList()
    }

    Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        AvatarView(data = if (f.originProfile.hasImage()) f.originProfile.image.toByteArray() else null, size = 44.dp)
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(f.originProfile.name.ifEmpty { "(no name)" }, style = MaterialTheme.typography.titleSmall)
            Text(f.originProfile.domain.ifEmpty { "(no domain)" }, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Text(statusLabel + if (f.sent) " (sent)" else "", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        if (options.isNotEmpty() || canDelete) {
            Box {
                IconButton(onClick = { menu = true }) { Icon(Icons.Default.MoreVert, "Actions") }
                DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                    options.forEach { (label, s) -> DropdownMenuItem(text = { Text(label) }, onClick = { menu = false; onChange(s) }) }
                    if (canDelete) DropdownMenuItem(text = { Text(deleteLabel, color = Color(0xFFE53935)) }, onClick = { menu = false; confirmingDelete = true })
                }
            }
        }
    }
    if (confirmingDelete) {
        AlertDialog(
            onDismissRequest = { confirmingDelete = false },
            title = { Text(if (f.sent) "Cancel this friend request?" else "Delete this friend request?") },
            text = { Text(if (f.sent) "The request will be withdrawn on both devices." else "The request will be removed here and on the sender's device.") },
            confirmButton = { TextButton(onClick = { confirmingDelete = false; onDelete() }) { Text(deleteLabel, color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { confirmingDelete = false }) { Text("Cancel") } },
        )
    }
}
