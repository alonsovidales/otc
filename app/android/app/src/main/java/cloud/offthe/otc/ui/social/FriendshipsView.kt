// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.social

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.clickable
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
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Button
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.ButtonDefaults
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import cloud.offthe.otc.data.SecretsStore
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
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidFriendsAskTheirsUnnamed
import cloud.offthe.otc.i18n.androidFriendsDeleteTheirs
import cloud.offthe.otc.i18n.androidFriendsDeleteTheirsUnnamed
import cloud.offthe.otc.i18n.androidFriendsKeep
import cloud.offthe.otc.i18n.androidFriendsKeepWaiting
import cloud.offthe.otc.i18n.androidFriendsRemoveFriend
import cloud.offthe.otc.i18n.androidFriendsRemoveNow
import cloud.offthe.otc.i18n.androidFriendsRemoveNowTitleUnnamed
import cloud.offthe.otc.i18n.androidFriendsRemoveTitleUnnamed
import cloud.offthe.otc.i18n.appSocialFriendsAddHeading
import cloud.offthe.otc.i18n.appSocialFriendsAskTheirs
import cloud.offthe.otc.i18n.appSocialFriendsAskTheirsText
import cloud.offthe.otc.i18n.appSocialFriendsBlock
import cloud.offthe.otc.i18n.appSocialFriendsCancelConfirmText
import cloud.offthe.otc.i18n.appSocialFriendsCancelConfirmTitle
import cloud.offthe.otc.i18n.appSocialFriendsCancelRequest
import cloud.offthe.otc.i18n.appSocialFriendsCancelRequestButton
import cloud.offthe.otc.i18n.appSocialFriendsDeleteConfirmText
import cloud.offthe.otc.i18n.appSocialFriendsDeleteConfirmTitle
import cloud.offthe.otc.i18n.appSocialFriendsDeleteError
import cloud.offthe.otc.i18n.appSocialFriendsDeleteFailed
import cloud.offthe.otc.i18n.appSocialFriendsDeleteRequest
import cloud.offthe.otc.i18n.appSocialFriendsDeleteTheirsText
import cloud.offthe.otc.i18n.appSocialFriendsEmpty
import cloud.offthe.otc.i18n.appSocialFriendsFriendsAgain
import cloud.offthe.otc.i18n.appSocialFriendsLeavingNote
import cloud.offthe.otc.i18n.appSocialFriendsListHeading
import cloud.offthe.otc.i18n.appSocialFriendsLoadFailed
import cloud.offthe.otc.i18n.appSocialFriendsNameInvalid
import cloud.offthe.otc.i18n.appSocialFriendsNamePlaceholder
import cloud.offthe.otc.i18n.appSocialFriendsNoDomain
import cloud.offthe.otc.i18n.appSocialFriendsNoName
import cloud.offthe.otc.i18n.appSocialFriendsRemoveButton
import cloud.offthe.otc.i18n.appSocialFriendsRemoveError
import cloud.offthe.otc.i18n.appSocialFriendsRemoveFailed
import cloud.offthe.otc.i18n.appSocialFriendsRemoveNow
import cloud.offthe.otc.i18n.appSocialFriendsRemoveNowText
import cloud.offthe.otc.i18n.appSocialFriendsRemoveNowTitle
import cloud.offthe.otc.i18n.appSocialFriendsRemoveTitle
import cloud.offthe.otc.i18n.appSocialFriendsRemoved
import cloud.offthe.otc.i18n.appSocialFriendsRequestCancelled
import cloud.offthe.otc.i18n.appSocialFriendsRequestDeleted
import cloud.offthe.otc.i18n.appSocialFriendsRequestFailed
import cloud.offthe.otc.i18n.appSocialFriendsRequestSent
import cloud.offthe.otc.i18n.appSocialFriendsSendError
import cloud.offthe.otc.i18n.appSocialFriendsSetPending
import cloud.offthe.otc.i18n.appSocialFriendsStatusAccepted
import cloud.offthe.otc.i18n.appSocialFriendsStatusAcceptedSent
import cloud.offthe.otc.i18n.appSocialFriendsStatusBlocked
import cloud.offthe.otc.i18n.appSocialFriendsStatusBlockedSent
import cloud.offthe.otc.i18n.appSocialFriendsStatusLeaving
import cloud.offthe.otc.i18n.appSocialFriendsStatusPending
import cloud.offthe.otc.i18n.appSocialFriendsStatusPendingSent
import cloud.offthe.otc.i18n.appSocialFriendsStatusUpdated
import cloud.offthe.otc.i18n.appSocialFriendsTitle
import cloud.offthe.otc.i18n.appSocialFriendsUnexpectedResponse
import cloud.offthe.otc.i18n.appSocialFriendsUpdateError
import cloud.offthe.otc.i18n.appSocialFriendsUpdateFailed
import cloud.offthe.otc.i18n.appSocialFriendsWaitingAnswer
import cloud.offthe.otc.i18n.appSocialFriendsWaitingTheirDevice
import cloud.offthe.otc.i18n.commonAccept
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonDecline
import cloud.offthe.otc.i18n.commonDone
import cloud.offthe.otc.i18n.commonRefresh
import cloud.offthe.otc.i18n.commonSend
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
        /** A short message at the top, worded when it shows. */
        val toast: UiText? = null,
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
        } catch (e: Exception) { toast(S.appSocialFriendsLoadFailed()) }
        finally { _state.update { it.copy(loading = false) } }
    }

    suspend fun sendRequest() {
        if (_state.value.targetDomain.isBlank()) return
        val domain = friendDomain(_state.value.targetDomain)
        if (domain == null) {
            toast(S.appSocialFriendsNameInvalid())
            return
        }
        _state.update { it.copy(sendingRequest = true) }
        try {
            val resp = OTCConnection.request { it.setReqFriendshipRequest(FriendshipRequest.newBuilder().setDomain(domain)) }
            val ack = if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) resp.respAck else null
            when {
                ack?.ok == true -> { toast(if (ack.code == "accepted") S.appSocialFriendsFriendsAgain() else S.appSocialFriendsRequestSent()); _state.update { it.copy(targetDomain = "") }; reload() }
                // The device's own words, already in the app's language.
                ack != null -> toast(if (ack.errorMsg.isEmpty()) S.appSocialFriendsRequestFailed() else UiText.Raw(ack.errorMsg))
                else -> toast(S.appSocialFriendsUnexpectedResponse())
            }
        } catch (e: Exception) { toast(S.appSocialFriendsSendError()) }
        finally { _state.update { it.copy(sendingRequest = false) } }
    }

    suspend fun changeStatus(f: Friendship, status: FriendShipStatus) {
        try {
            val resp = OTCConnection.request { it.setReqChangeFriendStatus(ChangeFriendStatus.newBuilder().setDomain(f.originProfile.domain).setStatus(status)) }
            val ack = if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) resp.respAck else null
            when {
                ack?.ok == true -> { reload(); toast(S.appSocialFriendsStatusUpdated()) }
                ack != null -> toast(if (ack.errorMsg.isEmpty()) S.appSocialFriendsUpdateFailed() else UiText.Raw(ack.errorMsg))
                else -> toast(S.appSocialFriendsUnexpectedResponse())
            }
        } catch (e: Exception) { toast(S.appSocialFriendsUpdateError()) }
    }

    suspend fun delete(f: Friendship) {
        try {
            val resp = OTCConnection.request { it.setReqDeleteFriendship(DeleteFriendship.newBuilder().setDomain(f.originProfile.domain)) }
            val ack = if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) resp.respAck else null
            when {
                ack?.ok == true -> { reload(); toast(if (f.sent) S.appSocialFriendsRequestCancelled() else S.appSocialFriendsRequestDeleted()) }
                resp.error -> toast(UiText.Raw(resp.errorMessage))
                else -> toast(S.appSocialFriendsDeleteFailed())
            }
        } catch (e: Exception) { toast(S.appSocialFriendsDeleteError()) }
    }

    // Issue #174: remove an accepted or blocked friend, optionally deleting
    // what they shared here and/or asking their device to delete what this
    // one shared there. Both flags false also ends a "leaving" friendship
    // straight away.
    suspend fun remove(f: Friendship, deleteTheirData: Boolean, askThemToDeleteMine: Boolean) {
        try {
            val resp = OTCConnection.request {
                it.setReqDeleteFriendship(DeleteFriendship.newBuilder().setDomain(f.originProfile.domain)
                    .setDeleteTheirData(deleteTheirData).setAskThemToDeleteMine(askThemToDeleteMine))
            }
            val ack = if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) resp.respAck else null
            when {
                ack?.ok == true -> {
                    reload()
                    val leaving = _state.value.friendships.any { it.originProfile.domain == f.originProfile.domain && it.leaving }
                    toast(if (leaving) S.appSocialFriendsWaitingTheirDevice() else S.appSocialFriendsRemoved())
                }
                resp.error -> toast(UiText.Raw(resp.errorMessage))
                else -> toast(S.appSocialFriendsRemoveFailed())
            }
        } catch (e: Exception) { toast(S.appSocialFriendsRemoveError()) }
    }

    private fun toast(m: UiText) {
        _state.update { it.copy(toast = m) }
        GlobalScope.launch { delay(2500); _state.update { if (it.toast == m) it.copy(toast = null) else it } }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FriendshipsView(onDone: (() -> Unit)?) {
    val vm: FriendshipsViewModel = viewModel()
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    LaunchedEffect(Unit) { vm.reload() }

    // Shown in a sheet, which already sits below the status bar: no insets
    // of its own (they left a status bar's worth of gap under the handle).
    // The wide layout shows it as a page of its own, reached from its menu
    // (MainView): no Done there ([onDone] null).
    Scaffold(contentWindowInsets = WindowInsets(0), topBar = {
        TopAppBar(
            title = { Text(S.appSocialFriendsTitle().resolve()) }, windowInsets = WindowInsets(0),
            navigationIcon = { if (onDone != null) TextButton(onClick = onDone) { Text(S.commonDone().resolve()) } },
        )
    }) { pad ->
        Box(Modifier.padding(pad).fillMaxSize()) {
            LazyColumn(Modifier.fillMaxSize()) {
                // Requests waiting for an answer come first: a tapped
                // friend-request alert lands here to answer it.
                val waiting = st.friendships.filter { it.status == FriendShipStatus.Pending && !it.sent && !it.leaving }
                if (waiting.isNotEmpty()) {
                    item {
                        Text(S.appSocialFriendsWaitingAnswer().resolve(), style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(16.dp, 12.dp, 16.dp, 4.dp))
                    }
                    items(waiting, key = { "w-" + it.originProfile.domain }) { f ->
                        WaitingRow(
                            f,
                            onDecline = { scope.launch { vm.delete(f) } },
                            onAccept = { scope.launch { vm.changeStatus(f, FriendShipStatus.Accepted) } },
                        )
                    }
                }
                item {
                    Text(S.appSocialFriendsAddHeading().resolve(), style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(16.dp, 12.dp, 16.dp, 4.dp))
                    Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                        // Issue #142: the name only; the suffix is fixed.
                        OTCTextField(
                            value = st.targetDomain, onValueChange = vm::setTarget, singleLine = true,
                            placeholder = { Text(S.appSocialFriendsNamePlaceholder().resolve()) },
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false),
                            modifier = Modifier.weight(1f),
                        )
                        Text(".${SecretsStore.bridgeDomain}", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 4.dp))
                        TextButton(onClick = { scope.launch { vm.sendRequest() } }, enabled = !st.sendingRequest && st.targetDomain.isNotBlank()) { Text(S.commonSend().resolve()) }
                    }
                }
                item {
                    Row(Modifier.fillMaxWidth().padding(16.dp, 20.dp, 16.dp, 4.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text(S.appSocialFriendsListHeading().resolve(), style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
                        if (st.loading) CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp)
                        else TextButton(onClick = { scope.launch { vm.reload() } }) { Text(S.commonRefresh().resolve()) }
                    }
                }
                if (st.friendships.isEmpty()) {
                    item { Text(S.appSocialFriendsEmpty().resolve(), color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(16.dp)) }
                }
                items(st.friendships.filter { it.leaving || !(it.status == FriendShipStatus.Pending && !it.sent) }, key = { it.originProfile.domain }) { f ->
                    FriendRow(
                        f,
                        onChange = { s -> scope.launch { vm.changeStatus(f, s) } },
                        onDelete = { scope.launch { vm.delete(f) } },
                        onRemove = { theirs, mine -> scope.launch { vm.remove(f, theirs, mine) } },
                    )
                }
            }
            Toast(st.toast?.resolve(), Modifier.align(Alignment.TopCenter))
        }
    }
}

/**
 * A request waiting for an answer: who sent it, and Decline and Accept -
 * beside the name while its words and the address fit whole, else on a
 * line of their own under it (the address broke as "sophie.off-th" /
 * "e.cloud" at 360 dp, and the name with it in longer languages). As
 * iOS's FriendshipsView.
 */
@Composable
private fun WaitingRow(f: Friendship, onDecline: () -> Unit, onAccept: () -> Unit) {
    val name = f.originProfile.name.ifEmpty { S.appSocialFriendsNoName().resolve() }
    val decline = S.commonDecline().resolve()
    val accept = S.commonAccept().resolve()
    val who: @Composable () -> Unit = {
        Row(verticalAlignment = Alignment.CenterVertically) {
            AvatarView(data = if (f.originProfile.hasImage()) f.originProfile.image.toByteArray() else null, size = 44.dp)
            Spacer(Modifier.width(12.dp))
            Column {
                Text(name, style = MaterialTheme.typography.titleSmall)
                Text(f.originProfile.domain, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
    val answer: @Composable () -> Unit = {
        OutlinedButton(onClick = onDecline) { Text(decline, maxLines = 1) }
        Spacer(Modifier.width(8.dp))
        Button(onClick = onAccept) { Text(accept, maxLines = 1) }
    }
    val measurer = rememberTextMeasurer()
    val density = LocalDensity.current
    val nameStyle = MaterialTheme.typography.titleSmall
    val domainStyle = MaterialTheme.typography.bodySmall
    val buttonStyle = MaterialTheme.typography.labelLarge
    BoxWithConstraints(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp)) {
        val beside = remember(name, f.originProfile.domain, decline, accept, maxWidth, density) {
            with(density) {
                fun width(text: String, style: TextStyle) = measurer.measure(text, style, softWrap = false, maxLines = 1, density = density).size.width.toDp()
                // Material's buttons: 24dp each side of the label, 58dp at least.
                fun button(label: String) = maxOf(58.dp, width(label, buttonStyle) + 48.dp)
                val words = name.split(' ').filter { it.isNotEmpty() }.maxOfOrNull { width(it, nameStyle) } ?: 0.dp
                val column = maxOf(words, width(f.originProfile.domain, domainStyle))
                44.dp + 12.dp + column + button(decline) + 8.dp + button(accept) <= maxWidth
            }
        }
        if (beside) Row(verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.weight(1f)) { who() }
            answer()
        } else Column {
            who()
            Row(Modifier.padding(start = 56.dp, top = 6.dp)) { answer() }
        }
    }
}

@Composable
private fun FriendRow(f: Friendship, onChange: (FriendShipStatus) -> Unit, onDelete: () -> Unit, onRemove: (Boolean, Boolean) -> Unit) {
    var confirmingDelete by remember { mutableStateOf(false) }
    var confirmingRemoveNow by remember { mutableStateOf(false) }
    var confirmingRemove by remember { mutableStateOf(false) }
    // Issue #174: a "leaving" friendship waits for the friend's device to
    // delete what was shared there; its only action is to stop waiting.
    val canDelete = !f.leaving && f.status == FriendShipStatus.Pending
    val canRemove = !f.leaving && (f.status == FriendShipStatus.Accepted || f.status == FriendShipStatus.Blocked)
    val deleteLabel = if (f.sent) S.appSocialFriendsCancelRequest() else S.appSocialFriendsDeleteRequest()
    // " (sent)" for a request this side sent: one text per case, so a
    // language can word each as a whole.
    val statusLabel = if (f.leaving) S.appSocialFriendsStatusLeaving() else when (f.status) {
        FriendShipStatus.Accepted -> if (f.sent) S.appSocialFriendsStatusAcceptedSent() else S.appSocialFriendsStatusAccepted()
        FriendShipStatus.Blocked -> if (f.sent) S.appSocialFriendsStatusBlockedSent() else S.appSocialFriendsStatusBlocked()
        else -> if (f.sent) S.appSocialFriendsStatusPendingSent() else S.appSocialFriendsStatusPending()
    }
    // Who the dialogs name; null - neither a name nor an address - has
    // sentences of its own ("Remove this friend?").
    val name: String? = f.originProfile.name.ifEmpty { f.originProfile.domain }.ifEmpty { null }
    val options: List<Pair<UiText, FriendShipStatus>> = if (f.sent || f.leaving) emptyList() else when (f.status) {
        FriendShipStatus.Pending -> listOf(S.commonAccept() to FriendShipStatus.Accepted, S.appSocialFriendsBlock() to FriendShipStatus.Blocked)
        FriendShipStatus.Accepted -> listOf(S.appSocialFriendsSetPending() to FriendShipStatus.Pending, S.appSocialFriendsBlock() to FriendShipStatus.Blocked)
        FriendShipStatus.Blocked -> listOf(S.commonAccept() to FriendShipStatus.Accepted, S.appSocialFriendsSetPending() to FriendShipStatus.Pending)
        else -> emptyList()
    }

    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp)) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            AvatarView(data = if (f.originProfile.hasImage()) f.originProfile.image.toByteArray() else null, size = 44.dp)
            Spacer(Modifier.width(12.dp))
            Column(Modifier.weight(1f)) {
                Text(f.originProfile.name.ifEmpty { S.appSocialFriendsNoName().resolve() }, style = MaterialTheme.typography.titleSmall)
                Text(f.originProfile.domain.ifEmpty { S.appSocialFriendsNoDomain().resolve() }, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Text(statusLabel.resolve(), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                if (f.leaving) Text(S.appSocialFriendsLeavingNote().resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
        // Issue #179: every action is a button on the row, as on the web and iOS.
        val danger = ButtonDefaults.outlinedButtonColors(contentColor = Color(0xFFE53935))
        val pad = PaddingValues(horizontal = 12.dp, vertical = 0.dp)
        FlowRow(Modifier.padding(start = 56.dp, top = 6.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            if (f.leaving) {
                OutlinedButton(onClick = { confirmingRemoveNow = true }, colors = danger, contentPadding = pad) { Text(S.appSocialFriendsRemoveNow().resolve()) }
            } else {
                options.forEach { (label, s) ->
                    if (s == FriendShipStatus.Accepted) Button(onClick = { onChange(s) }, contentPadding = pad) { Text(label.resolve()) }
                    else OutlinedButton(onClick = { onChange(s) }, contentPadding = pad) { Text(label.resolve()) }
                }
                if (canDelete) OutlinedButton(onClick = { confirmingDelete = true }, colors = danger, contentPadding = pad) { Text((if (f.sent) S.appSocialFriendsCancelRequestButton() else S.commonDecline()).resolve()) }
                if (canRemove) OutlinedButton(onClick = { confirmingRemove = true }, colors = danger, contentPadding = pad) { Text(S.appSocialFriendsRemoveButton().resolve()) }
            }
        }
    }
    // As iOS: stopping the wait is final - without the friendship their
    // device never picks the request up.
    if (confirmingRemoveNow) {
        AlertDialog(
            onDismissRequest = { confirmingRemoveNow = false },
            title = { Text((if (name != null) S.appSocialFriendsRemoveNowTitle(name) else S.androidFriendsRemoveNowTitleUnnamed()).resolve()) },
            text = { Text(S.appSocialFriendsRemoveNowText().resolve()) },
            confirmButton = { TextButton(onClick = { confirmingRemoveNow = false; onRemove(false, false) }) { Text(S.androidFriendsRemoveNow().resolve(), color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { confirmingRemoveNow = false }) { Text(S.androidFriendsKeepWaiting().resolve()) } },
        )
    }
    if (confirmingDelete) {
        AlertDialog(
            onDismissRequest = { confirmingDelete = false },
            title = { Text((if (f.sent) S.appSocialFriendsCancelConfirmTitle() else S.appSocialFriendsDeleteConfirmTitle()).resolve()) },
            text = { Text((if (f.sent) S.appSocialFriendsCancelConfirmText() else S.appSocialFriendsDeleteConfirmText()).resolve()) },
            confirmButton = { TextButton(onClick = { confirmingDelete = false; onDelete() }) { Text(deleteLabel.resolve(), color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { confirmingDelete = false }) { Text(S.commonCancel().resolve()) } },
        )
    }
    if (confirmingRemove) {
        var deleteTheirs by remember { mutableStateOf(false) }
        var askThem by remember { mutableStateOf(false) }
        AlertDialog(
            onDismissRequest = { confirmingRemove = false },
            title = { Text((if (name != null) S.appSocialFriendsRemoveTitle(name) else S.androidFriendsRemoveTitleUnnamed()).resolve()) },
            text = {
                Column {
                    RemoveOption(
                        checked = deleteTheirs, onChange = { deleteTheirs = it },
                        title = (if (name != null) S.androidFriendsDeleteTheirs(name) else S.androidFriendsDeleteTheirsUnnamed()).resolve(),
                        detail = S.appSocialFriendsDeleteTheirsText().resolve(),
                    )
                    RemoveOption(
                        checked = askThem, onChange = { askThem = it },
                        title = (if (name != null) S.appSocialFriendsAskTheirs(name) else S.androidFriendsAskTheirsUnnamed()).resolve(),
                        detail = S.appSocialFriendsAskTheirsText().resolve(),
                    )
                }
            },
            confirmButton = { TextButton(onClick = { confirmingRemove = false; onRemove(deleteTheirs, askThem) }) { Text(S.androidFriendsRemoveFriend().resolve(), color = MaterialTheme.colorScheme.error) } },
            dismissButton = { TextButton(onClick = { confirmingRemove = false }) { Text(S.androidFriendsKeep().resolve()) } },
        )
    }
}

@Composable
private fun RemoveOption(checked: Boolean, onChange: (Boolean) -> Unit, title: String, detail: String) {
    Row(Modifier.fillMaxWidth().clickable { onChange(!checked) }.padding(vertical = 4.dp), verticalAlignment = Alignment.Top) {
        Checkbox(checked = checked, onCheckedChange = onChange)
        Column(Modifier.weight(1f).padding(top = 12.dp)) {
            Text(title, style = MaterialTheme.typography.bodyMedium)
            Text(detail, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

private val friendNamePattern = Regex("^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")

/**
 * Issue #142: friends are always <name>.off-the.cloud (the device refuses
 * anything else), so the box takes only the name. Accepts a bare name, the
 * full domain or a link to it; null when it isn't a device name. Same rule
 * as FriendshipsView.swift's friendDomain and the web's friendDomain.ts.
 */
fun friendDomain(input: String): String? {
    var s = input.trim().lowercase()
    s.indexOf("://").takeIf { it >= 0 }?.let { s = s.substring(it + 3) }
    s = s.takeWhile { it != '/' && it != '?' && it != '#' }
    val tld = "." + SecretsStore.bridgeDomain
    if (s.endsWith(tld)) s = s.dropLast(tld.length)
    return if (friendNamePattern.matches(s)) s + tld else null
}
