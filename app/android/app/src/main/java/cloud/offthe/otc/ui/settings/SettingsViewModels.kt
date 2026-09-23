// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.PwCrypto
import cloud.offthe.otc.proto.ChangeKey
import cloud.offthe.otc.proto.GetProfile
import cloud.offthe.otc.proto.GetPubKey
import cloud.offthe.otc.proto.GetReprocessStatus
import cloud.offthe.otc.proto.GetSettings
import cloud.offthe.otc.proto.GetStatus
import cloud.offthe.otc.proto.Profile
import cloud.offthe.otc.proto.ReqApplyUpdate
import cloud.offthe.otc.proto.ReqCheckUpdate
import cloud.offthe.otc.proto.ReqCreateUser
import cloud.offthe.otc.proto.ReqDeleteUser
import cloud.offthe.otc.proto.ReqGetInstanceRole
import cloud.offthe.otc.proto.ReqGetTailscaleStatus
import cloud.offthe.otc.proto.ReqGetUserMetrics
import cloud.offthe.otc.proto.ReqListUsers
import cloud.offthe.otc.proto.ReqSetUserActive
import cloud.offthe.otc.proto.ReqSetupTailscale
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespUserMetrics
import cloud.offthe.otc.proto.SetBridgeSecret
import cloud.offthe.otc.proto.SetFaceRecognitionEnabled
import cloud.offthe.otc.proto.StartReprocess
import cloud.offthe.otc.proto.Status
import cloud.offthe.otc.proto.StopReprocess
import cloud.offthe.otc.proto.UpdateRelease
import cloud.offthe.otc.proto.User
import com.google.protobuf.ByteString
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

// Ports of the view models behind SettingsView.swift, ProfileEditor.swift,
// UpdateSection.swift, TailscaleSection.swift and UsersManagementView.swift.

private suspend fun isPrimaryInstance(): Boolean = try {
    val r = OTCConnection.request { it.setReqGetInstanceRole(ReqGetInstanceRole.getDefaultInstance()) }
    r.payloadCase == RespEnvelope.PayloadCase.RESP_INSTANCE_ROLE && r.respInstanceRole.isPrimary
} catch (e: Exception) { false }

private fun RespEnvelope.ackOk() = payloadCase == RespEnvelope.PayloadCase.RESP_ACK && respAck.ok
private fun RespEnvelope.ackError(default: String) = if (payloadCase == RespEnvelope.PayloadCase.RESP_ACK) respAck.errorMsg.ifEmpty { default } else "Unexpected response"

class ProfileEditorViewModel : ViewModel() {
    data class State(val name: String = "", val bio: String = "", val imageData: ByteArray? = null, val saving: Boolean = false, val toast: String? = null)
    val state = MutableStateFlow(State())

    fun setName(v: String) = state.update { it.copy(name = v) }
    fun setBio(v: String) = state.update { it.copy(bio = v) }
    fun setImage(v: ByteArray?) = state.update { it.copy(imageData = v) }

    suspend fun load() {
        try {
            val resp = OTCConnection.request { it.setReqGetProfile(GetProfile.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_PROFILE) {
                val p = resp.respProfile
                state.update { it.copy(name = p.name, bio = p.text, imageData = if (p.hasImage()) p.image.toByteArray() else null) }
            }
        } catch (_: Exception) {}
    }

    suspend fun save() {
        state.update { it.copy(saving = true) }
        try {
            val s = state.value
            val p = Profile.newBuilder().setName(s.name).setText(s.bio)
            s.imageData?.let { p.image = ByteString.copyFrom(it) }
            val resp = OTCConnection.request { it.setReqSetProfile(p) }
            toast(if (resp.ackOk()) "Profile updated ✅" else resp.ackError("Profile update failed"))
        } catch (e: Exception) { toast("Error saving profile") }
        finally { state.update { it.copy(saving = false) } }
    }

    private fun toast(m: String) {
        state.update { it.copy(toast = m) }
        viewModelScope.launch { delay(2500); state.update { if (it.toast == m) it.copy(toast = null) else it } }
    }
}

class DeviceSettingsViewModel : ViewModel() {
    enum class ReprocessConfirm { RESUME, RESTART }
    data class State(
        val currentBridgeSecret: String = "", val newBridgeSecret: String = "", val savingSecret: Boolean = false,
        val oldKey: String = "", val newKey: String = "", val confirmKey: String = "", val savingKey: Boolean = false,
        val toast: String? = null,
        val faceRecognitionEnabled: Boolean = false, val savingFaceRecognition: Boolean = false,
        val reprocessStatus: String = "", val reprocessTotal: Int = 0, val reprocessProcessed: Int = 0,
        val startingReprocess: Boolean = false, val cancellingReprocess: Boolean = false, val reprocessConfirm: ReprocessConfirm? = null,
    ) {
        val reprocessPercent: Int get() = if (reprocessTotal <= 0) 0 else ((reprocessProcessed.toDouble() / reprocessTotal) * 100).toInt().coerceIn(0, 100)
    }
    val state = MutableStateFlow(State())
    private var reprocessPoll: Job? = null

    fun setNewBridgeSecret(v: String) = state.update { it.copy(newBridgeSecret = v) }
    fun setOldKey(v: String) = state.update { it.copy(oldKey = v) }
    fun setNewKey(v: String) = state.update { it.copy(newKey = v) }
    fun setConfirmKey(v: String) = state.update { it.copy(confirmKey = v) }
    fun setReprocessConfirm(v: ReprocessConfirm?) = state.update { it.copy(reprocessConfirm = v) }

    suspend fun loadReprocessStatus() {
        try {
            val resp = OTCConnection.request { it.setReqGetReprocessStatus(GetReprocessStatus.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_REPROCESS_STATUS) {
                val s = resp.respReprocessStatus
                state.update { it.copy(reprocessStatus = s.status, reprocessTotal = s.total, reprocessProcessed = s.processed) }
            }
        } catch (_: Exception) {}
    }

    fun pollReprocessStatusWhileRunning() {
        reprocessPoll?.cancel()
        reprocessPoll = viewModelScope.launch {
            loadReprocessStatus()
            while (isActive && state.value.reprocessStatus == "running") { delay(1500); loadReprocessStatus() }
        }
    }

    fun stopPollingReprocessStatus() { reprocessPoll?.cancel(); reprocessPoll = null }

    suspend fun startReprocess(forceRestart: Boolean) {
        state.update { it.copy(reprocessConfirm = null, startingReprocess = true) }
        try {
            val resp = OTCConnection.request { it.setReqStartReprocess(StartReprocess.newBuilder().setForceRestart(forceRestart)) }
            if (resp.ackOk()) pollReprocessStatusWhileRunning() else toast(resp.ackError("Could not start reprocessing"))
        } catch (e: Exception) { toast("Error starting reprocessing") }
        finally { state.update { it.copy(startingReprocess = false) } }
    }

    suspend fun stopReprocess() {
        state.update { it.copy(cancellingReprocess = true) }
        try {
            val resp = OTCConnection.request { it.setReqStopReprocess(StopReprocess.getDefaultInstance()) }
            // The poll loop keeps running and exits on its own once it sees "stopped".
            if (!resp.ackOk()) toast(resp.ackError("Could not stop reprocessing"))
        } catch (e: Exception) { toast("Error stopping reprocessing") }
        finally { state.update { it.copy(cancellingReprocess = false) } }
    }

    suspend fun loadSettings() {
        try {
            val resp = OTCConnection.request { it.setReqGetSettings(GetSettings.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_SETTINGS) {
                state.update { it.copy(currentBridgeSecret = resp.respSettings.bridgeSecret, faceRecognitionEnabled = resp.respSettings.faceRecognitionEnabled) }
            }
        } catch (_: Exception) {}
    }

    suspend fun toggleFaceRecognition(enabled: Boolean) {
        state.update { it.copy(savingFaceRecognition = true, faceRecognitionEnabled = enabled) }
        try {
            val resp = OTCConnection.request { it.setReqSetFaceRecognitionEnabled(SetFaceRecognitionEnabled.newBuilder().setEnabled(enabled)) }
            if (!resp.ackOk()) { state.update { it.copy(faceRecognitionEnabled = !enabled) }; toast(resp.ackError("Update failed")) }
        } catch (e: Exception) { state.update { it.copy(faceRecognitionEnabled = !enabled) }; toast("Error updating this setting") }
        finally { state.update { it.copy(savingFaceRecognition = false) } }
    }

    suspend fun saveBridgeSecret() {
        val trimmed = state.value.newBridgeSecret.trim()
        if (trimmed.isEmpty()) return
        state.update { it.copy(savingSecret = true) }
        try {
            val resp = OTCConnection.request { it.setReqSetBridgeSecret(SetBridgeSecret.newBuilder().setSecret(trimmed)) }
            if (resp.ackOk()) { state.update { it.copy(currentBridgeSecret = trimmed, newBridgeSecret = "") }; toast("Bridge secret updated ✅") }
            else toast(resp.ackError("Update failed"))
        } catch (e: Exception) { toast("Error updating bridge secret") }
        finally { state.update { it.copy(savingSecret = false) } }
    }

    /** Same RSA-OAEP flow as sign-in (issue #2); the stored password follows on success. */
    suspend fun changePassword(secrets: SecretsStore) {
        val s = state.value
        if (s.oldKey.isEmpty() || s.newKey.isEmpty()) { toast("Fill in all fields"); return }
        if (s.newKey != s.confirmKey) { toast("Passwords don't match"); return }
        state.update { it.copy(savingKey = true) }
        try {
            val pk = OTCConnection.request { it.setReqGetPubKey(GetPubKey.getDefaultInstance()) }
            if (pk.payloadCase != RespEnvelope.PayloadCase.RESP_PUB_KEY) { toast("Could not fetch the connection's public key"); return }
            val der = pk.respPubKey.publicKey.toByteArray()
            val encOld = PwCrypto.encryptPassword(s.oldKey, der)
            val encNew = PwCrypto.encryptPassword(s.newKey, der)
            val resp = OTCConnection.request { it.setReqChangeKey(ChangeKey.newBuilder().setOldKey(ByteString.copyFrom(encOld)).setNewKey(ByteString.copyFrom(encNew))) }
            if (resp.ackOk()) {
                secrets.setPassword(s.newKey); secrets.persist()
                state.update { it.copy(oldKey = "", newKey = "", confirmKey = "") }
                toast("Password changed ✅")
            } else toast(resp.ackError("Change failed"))
        } catch (e: Exception) { toast("Error changing password: ${e.message}") }
        finally { state.update { it.copy(savingKey = false) } }
    }

    fun toast(m: String) {
        state.update { it.copy(toast = m) }
        viewModelScope.launch { delay(2500); state.update { if (it.toast == m) it.copy(toast = null) else it } }
    }
}

class StatusViewModel : ViewModel() {
    data class State(val status: Status? = null, val errorText: String? = null)
    val state = MutableStateFlow(State())
    private var poll: Job? = null

    fun start() {
        if (poll != null) return
        poll = viewModelScope.launch { while (isActive) { fetch(); delay(5000) } }
    }

    fun stop() { poll?.cancel(); poll = null }

    private suspend fun fetch() {
        try {
            val resp = OTCConnection.request { it.setReqGetStatus(GetStatus.getDefaultInstance()) }
            if (resp.error) state.update { it.copy(errorText = resp.errorMessage, status = null) }
            else if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_STATUS) state.update { it.copy(status = resp.respStatus, errorText = null) }
        } catch (e: Exception) { state.update { it.copy(errorText = e.message) } }
    }
}

/** Issue #94: in-place updates, primary instance only. */
class UpdateViewModel : ViewModel() {
    data class State(
        val isPrimary: Boolean = false, val currentVersion: Int = 0, val latestVersion: Int = 0, val pending: List<UpdateRelease> = emptyList(),
        val state: String = "", val message: String = "", val checkError: String = "", val lastUpdated: String = "",
        val checking: Boolean = false, val starting: Boolean = false, val error: String? = null,
    ) {
        val hasUpdate get() = pending.isNotEmpty()
        val running get() = state == "running"
    }
    val state = MutableStateFlow(State())
    private var poll: Job? = null

    suspend fun load() {
        val primary = isPrimaryInstance()
        state.update { it.copy(isPrimary = primary) }
        if (primary) check()
    }

    suspend fun check() {
        state.update { it.copy(checking = true) }
        try {
            val resp = OTCConnection.request { it.setReqCheckUpdate(ReqCheckUpdate.getDefaultInstance()) }
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_UPDATE_INFO) { if (resp.error) state.update { it.copy(error = resp.errorMessage) }; return }
            val i = resp.respUpdateInfo
            state.update { it.copy(currentVersion = i.currentVersion, latestVersion = i.latestVersion, pending = i.pendingList, state = i.state, message = i.message, checkError = i.checkError, lastUpdated = i.lastUpdated, error = null) }
        } catch (_: Exception) {
        } finally {
            state.update { it.copy(checking = false) }
            startPollingIfRunning()
        }
    }

    suspend fun apply() {
        state.update { it.copy(starting = true) }
        try {
            val resp = OTCConnection.request { it.setReqApplyUpdate(ReqApplyUpdate.getDefaultInstance()) }
            if (resp.ackOk()) { state.update { it.copy(state = "running", message = "Starting") }; startPollingIfRunning() }
            else state.update { it.copy(error = if (resp.error) resp.errorMessage else "Could not start the update.") }
        } catch (e: Exception) { state.update { it.copy(error = e.message) } }
        finally { state.update { it.copy(starting = false) } }
    }

    private fun startPollingIfRunning() {
        if (!state.value.running) { poll?.cancel(); poll = null; return }
        if (poll != null) return
        poll = viewModelScope.launch {
            while (isActive) { delay(5000); if (!state.value.running) break; check() }
            poll = null
        }
    }
}

/** Issue #80: Tailscale Funnel as an alternative to the bridge, primary only. */
class TailscaleViewModel : ViewModel() {
    data class State(
        val isPrimary: Boolean = false, val installed: Boolean = false, val loggedIn: Boolean = false, val funnelOn: Boolean = false,
        val publicUrl: String = "", val loginUrl: String = "", val authKey: String = "", val busy: Boolean = false, val note: String? = null, val error: String? = null,
    )
    val state = MutableStateFlow(State())

    fun setAuthKey(v: String) = state.update { it.copy(authKey = v) }

    suspend fun load() {
        val primary = isPrimaryInstance()
        state.update { it.copy(isPrimary = primary) }
        if (primary) refresh()
    }

    suspend fun refresh() {
        try { apply(OTCConnection.request { it.setReqGetTailscaleStatus(ReqGetTailscaleStatus.getDefaultInstance()) }) }
        catch (e: Exception) { state.update { it.copy(error = e.message) } }
    }

    suspend fun configure(enable: Boolean) {
        state.update { it.copy(busy = true, note = null, error = null) }
        try {
            val resp = OTCConnection.request { it.setReqSetupTailscale(ReqSetupTailscale.newBuilder().setAuthKey(state.value.authKey.trim()).setEnable(enable)) }
            if (!apply(resp)) { state.update { it.copy(error = if (resp.error) resp.errorMessage else "Could not change the setting.") }; return }
            val s = state.value
            val note = when {
                !enable -> "Funnel is off. This device is reachable through the bridge again."
                !s.installed -> null
                s.loginUrl.isNotEmpty() -> "Authorise this device in the page that just opened, then press Enable again."
                s.funnelOn -> "Done — this device is reachable at ${s.publicUrl}"
                else -> "Tailscale is connected, but Funnel didn't come up. Check that Funnel is enabled for your tailnet."
            }
            state.update { it.copy(note = note, error = if (enable && !s.installed) "Tailscale isn't installed on this device, so Funnel can't be turned on here." else it.error) }
        } catch (e: Exception) { state.update { it.copy(error = e.message) } }
        finally { state.update { it.copy(busy = false) } }
    }

    private fun apply(resp: RespEnvelope): Boolean {
        if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_TAILSCALE_STATUS) return false
        val s = resp.respTailscaleStatus
        state.update { it.copy(installed = s.installed, loggedIn = s.loggedIn, funnelOn = s.funnelOn, publicUrl = s.publicUrl, loginUrl = s.loginUrl, error = s.error.ifEmpty { it.error }) }
        return true
    }
}

/** Issue #82: additional users on one device, primary only. */
class UsersManagementViewModel : ViewModel() {
    data class State(
        val isPrimary: Boolean = false, val users: List<User> = emptyList(), val loading: Boolean = false, val metrics: Map<String, RespUserMetrics> = emptyMap(),
        val newUsername: String = "", val newPort: String = "", val creating: Boolean = false,
        val deleteTargetUuid: String? = null, val deleteConfirmText: String = "", val deleting: Boolean = false,
        val busyActiveUuid: String? = null, val toast: String? = null,
    )
    val state = MutableStateFlow(State())

    fun setNewUsername(v: String) = state.update { it.copy(newUsername = v) }
    fun setNewPort(v: String) = state.update { it.copy(newPort = v) }
    fun setDeleteConfirmText(v: String) = state.update { it.copy(deleteConfirmText = v) }
    fun toggleDeleteTarget(uuid: String) = state.update { it.copy(deleteTargetUuid = if (it.deleteTargetUuid == uuid) null else uuid, deleteConfirmText = "") }
    fun dismissToast() = state.update { it.copy(toast = null) }

    suspend fun checkRole() {
        val primary = isPrimaryInstance()
        state.update { it.copy(isPrimary = primary) }
        if (primary) loadUsers()
    }

    suspend fun loadUsers() {
        state.update { it.copy(loading = true) }
        try {
            val resp = OTCConnection.request { it.setReqListUsers(ReqListUsers.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_USERS) state.update { it.copy(users = resp.respUsers.usersList) }
        } catch (_: Exception) {}
        finally { state.update { it.copy(loading = false) } }
    }

    suspend fun fetchMetrics(uuid: String) {
        try {
            val resp = OTCConnection.request { it.setReqGetUserMetrics(ReqGetUserMetrics.newBuilder().setUuid(uuid)) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_USER_METRICS) state.update { it.copy(metrics = it.metrics + (uuid to resp.respUserMetrics)) }
        } catch (_: Exception) {}
    }

    suspend fun createUser() {
        state.update { it.copy(creating = true) }
        val username = state.value.newUsername.trim()
        try {
            val resp = OTCConnection.request { it.setReqCreateUser(ReqCreateUser.newBuilder().setUsername(username).setPort(state.value.newPort.toIntOrNull() ?: 0)) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_USERS) state.update { it.copy(users = resp.respUsers.usersList, newUsername = "", newPort = "", toast = "User \"$username\" created.") }
            else state.update { it.copy(toast = resp.errorMessage.ifEmpty { "Could not create user." }) }
        } catch (e: Exception) { state.update { it.copy(toast = e.message) } }
        finally { state.update { it.copy(creating = false) } }
    }

    suspend fun confirmDelete(u: User) {
        state.update { it.copy(deleting = true) }
        try {
            val resp = OTCConnection.request { it.setReqDeleteUser(ReqDeleteUser.newBuilder().setUuid(u.uuid).setConfirmUsername(state.value.deleteConfirmText)) }
            if (resp.ackOk()) { state.update { it.copy(toast = "User \"${u.username}\" deleted.", deleteTargetUuid = null, deleteConfirmText = "") }; loadUsers() }
            else state.update { it.copy(toast = resp.errorMessage.ifEmpty { "Could not delete user." }) }
        } catch (e: Exception) { state.update { it.copy(toast = e.message) } }
        finally { state.update { it.copy(deleting = false) } }
    }

    suspend fun toggleActive(u: User) {
        state.update { it.copy(busyActiveUuid = u.uuid) }
        try {
            val resp = OTCConnection.request { it.setReqSetUserActive(ReqSetUserActive.newBuilder().setUuid(u.uuid).setActive(!u.active)) }
            if (resp.ackOk()) loadUsers() else state.update { it.copy(toast = resp.errorMessage.ifEmpty { "Could not update this user." }) }
        } catch (e: Exception) { state.update { it.copy(toast = e.message) } }
        finally { state.update { it.copy(busyActiveUuid = null) } }
    }
}
