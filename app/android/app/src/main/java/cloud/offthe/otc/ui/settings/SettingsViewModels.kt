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
import cloud.offthe.otc.proto.ReqDisableBridge
import cloud.offthe.otc.proto.ReqGetBridgeAccess
import cloud.offthe.otc.proto.ReqDeleteUser
import cloud.offthe.otc.proto.ReqGetInstanceRole
import cloud.offthe.otc.proto.ReqGetTailscaleStatus
import cloud.offthe.otc.proto.ReqGetUserMetrics
import cloud.offthe.otc.proto.ReqListUsers
import cloud.offthe.otc.proto.ReqSetUserActive
import cloud.offthe.otc.proto.ReqSetupTailscale
import cloud.offthe.otc.proto.RestartDevice
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespUserMetrics
import cloud.offthe.otc.proto.SetFaceRecognitionEnabled
import cloud.offthe.otc.data.FaceRecognition
import cloud.offthe.otc.proto.SetImageTaggingEnabled
import cloud.offthe.otc.proto.StartReprocess
import cloud.offthe.otc.proto.Status
import cloud.offthe.otc.proto.StopReprocess
import cloud.offthe.otc.proto.UpdateRelease
import cloud.offthe.otc.proto.User
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appSettingsBridgeLeaveFailed
import cloud.offthe.otc.i18n.appSettingsBridgeLeft
import cloud.offthe.otc.i18n.appSettingsErrorSetting
import cloud.offthe.otc.i18n.appSettingsErrorUnexpected
import cloud.offthe.otc.i18n.appSettingsErrorUpdateFailed
import cloud.offthe.otc.i18n.appSettingsPasswordChanged
import cloud.offthe.otc.i18n.appSettingsPasswordError
import cloud.offthe.otc.i18n.appSettingsPasswordFailed
import cloud.offthe.otc.i18n.appSettingsPasswordFillAll
import cloud.offthe.otc.i18n.appSettingsPasswordMismatch
import cloud.offthe.otc.i18n.appSettingsPasswordNoKey
import cloud.offthe.otc.i18n.appSettingsProfileSaveError
import cloud.offthe.otc.i18n.appSettingsProfileSaveFailed
import cloud.offthe.otc.i18n.appSettingsProfileSaved
import cloud.offthe.otc.i18n.appSettingsReprocessStartError
import cloud.offthe.otc.i18n.appSettingsReprocessStartFailed
import cloud.offthe.otc.i18n.appSettingsReprocessStopError
import cloud.offthe.otc.i18n.appSettingsReprocessStopFailed
import cloud.offthe.otc.i18n.appSettingsTailscaleAuthorise
import cloud.offthe.otc.i18n.appSettingsTailscaleChangeFailed
import cloud.offthe.otc.i18n.appSettingsTailscaleDone
import cloud.offthe.otc.i18n.appSettingsTailscaleNoFunnel
import cloud.offthe.otc.i18n.appSettingsTailscaleNotInstalledOn
import cloud.offthe.otc.i18n.appSettingsTailscaleOff
import cloud.offthe.otc.i18n.appSettingsUpdateBuild
import cloud.offthe.otc.i18n.appSettingsUpdateLabelBuild
import cloud.offthe.otc.i18n.appSettingsUpdateRestartFailed
import cloud.offthe.otc.i18n.appSettingsUpdateRestartOld
import cloud.offthe.otc.i18n.appSettingsUpdateStartFailed
import cloud.offthe.otc.i18n.appSettingsUpdateStartingStatus
import cloud.offthe.otc.i18n.appSettingsUpdateUpdateTo
import cloud.offthe.otc.i18n.appSettingsUpdateUpdateToBuild
import cloud.offthe.otc.i18n.appSettingsUsersCreateFailed
import cloud.offthe.otc.i18n.appSettingsUsersCreated
import cloud.offthe.otc.i18n.appSettingsUsersDeleteFailed
import cloud.offthe.otc.i18n.appSettingsUsersDeleted
import cloud.offthe.otc.i18n.appSettingsUsersUpdateFailed
import cloud.offthe.otc.net.WSClient
import cloud.offthe.otc.ui.common.TextIOException
import com.google.protobuf.ByteString
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

// Ports of the view models behind SettingsView.swift, ProfileEditor.swift,
// UpdateSection.swift, TailscaleSection.swift and UsersManagementView.swift.
// What they show is UiText, worded where it is drawn (docs/i18n.md, "Each
// platform"): our own words through the S accessors, the device's as
// UiText.Raw (it already wrote them in the request's language).

internal suspend fun isPrimaryInstance(): Boolean = try {
    val r = OTCConnection.request { it.setReqGetInstanceRole(ReqGetInstanceRole.getDefaultInstance()) }
    r.payloadCase == RespEnvelope.PayloadCase.RESP_INSTANCE_ROLE && r.respInstanceRole.isPrimary
} catch (e: Exception) { false }

private fun RespEnvelope.ackOk() = payloadCase == RespEnvelope.PayloadCase.RESP_ACK && respAck.ok
private fun RespEnvelope.ackError(default: UiText): UiText =
    if (payloadCase == RespEnvelope.PayloadCase.RESP_ACK) deviceOr(respAck.errorMsg, default) else S.appSettingsErrorUnexpected()

/** The device's words, or ours when it gave none. */
internal fun deviceOr(text: String, ours: UiText): UiText = if (text.isEmpty()) ours else UiText.Raw(text)

/**
 * What an exception says, for the screen: ours worded where it is shown,
 * anyone else's message as it came, null when it has none (as e.message).
 */
internal fun Throwable.shownText(): UiText? = when (this) {
    is TextIOException -> text
    is WSClient.RequestTimeout -> text
    else -> message?.let { UiText.Raw(it) }
}

class ProfileEditorViewModel : ViewModel() {
    data class State(val name: String = "", val bio: String = "", val imageData: ByteArray? = null, val saving: Boolean = false, val toast: UiText? = null)
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
            toast(if (resp.ackOk()) S.appSettingsProfileSaved() else resp.ackError(S.appSettingsProfileSaveFailed()))
        } catch (e: Exception) { toast(S.appSettingsProfileSaveError()) }
        finally { state.update { it.copy(saving = false) } }
    }

    private fun toast(m: UiText) {
        state.update { it.copy(toast = m) }
        viewModelScope.launch { delay(2500); state.update { if (it.toast == m) it.copy(toast = null) else it } }
    }
}

class DeviceSettingsViewModel : ViewModel() {
    enum class ReprocessConfirm { RESUME, RESTART }
    data class State(
        val oldKey: String = "", val newKey: String = "", val confirmKey: String = "", val savingKey: Boolean = false,
        val toast: UiText? = null,
        val faceRecognitionEnabled: Boolean = false, val savingFaceRecognition: Boolean = false,
        val imageTaggingEnabled: Boolean = true, val savingImageTagging: Boolean = false,
        val reprocessStatus: String = "", val reprocessTotal: Int = 0, val reprocessProcessed: Int = 0,
        val startingReprocess: Boolean = false, val cancellingReprocess: Boolean = false, val reprocessConfirm: ReprocessConfirm? = null,
    ) {
        val reprocessPercent: Int get() = if (reprocessTotal <= 0) 0 else ((reprocessProcessed.toDouble() / reprocessTotal) * 100).toInt().coerceIn(0, 100)
    }
    val state = MutableStateFlow(State())
    private var reprocessPoll: Job? = null

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
            if (resp.ackOk()) pollReprocessStatusWhileRunning() else toast(resp.ackError(S.appSettingsReprocessStartFailed()))
        } catch (e: Exception) { toast(S.appSettingsReprocessStartError()) }
        finally { state.update { it.copy(startingReprocess = false) } }
    }

    suspend fun stopReprocess() {
        state.update { it.copy(cancellingReprocess = true) }
        try {
            val resp = OTCConnection.request { it.setReqStopReprocess(StopReprocess.getDefaultInstance()) }
            // The poll loop keeps running and exits on its own once it sees "stopped".
            if (!resp.ackOk()) toast(resp.ackError(S.appSettingsReprocessStopFailed()))
        } catch (e: Exception) { toast(S.appSettingsReprocessStopError()) }
        finally { state.update { it.copy(cancellingReprocess = false) } }
    }

    suspend fun loadSettings() {
        try {
            val resp = OTCConnection.request { it.setReqGetSettings(GetSettings.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_SETTINGS) {
                state.update { it.copy(faceRecognitionEnabled = resp.respSettings.faceRecognitionEnabled, imageTaggingEnabled = resp.respSettings.imageTaggingEnabled) }
                // The one place the rest of the app reads it from (People in the search).
                FaceRecognition.set(resp.respSettings.faceRecognitionEnabled)
                cloud.offthe.otc.i18n.LanguageSettings.deviceSaid(resp.respSettings)
            }
        } catch (_: Exception) {}
    }

    suspend fun toggleFaceRecognition(enabled: Boolean) {
        state.update { it.copy(savingFaceRecognition = true, faceRecognitionEnabled = enabled) }
        try {
            val resp = OTCConnection.request { it.setReqSetFaceRecognitionEnabled(SetFaceRecognitionEnabled.newBuilder().setEnabled(enabled)) }
            if (!resp.ackOk()) { state.update { it.copy(faceRecognitionEnabled = !enabled) }; toast(resp.ackError(S.appSettingsErrorUpdateFailed())) }
            // Acknowledged: People comes or goes at once, everywhere.
            else FaceRecognition.set(enabled)
        } catch (e: Exception) { state.update { it.copy(faceRecognitionEnabled = !enabled) }; toast(S.appSettingsErrorSetting()) }
        finally { state.update { it.copy(savingFaceRecognition = false) } }
    }

    /** Issue #181: the tagging model, like face recognition, can be off. */
    suspend fun toggleImageTagging(enabled: Boolean) {
        state.update { it.copy(savingImageTagging = true, imageTaggingEnabled = enabled) }
        try {
            val resp = OTCConnection.request { it.setReqSetImageTaggingEnabled(SetImageTaggingEnabled.newBuilder().setEnabled(enabled)) }
            if (!resp.ackOk()) { state.update { it.copy(imageTaggingEnabled = !enabled) }; toast(resp.ackError(S.appSettingsErrorUpdateFailed())) }
        } catch (e: Exception) { state.update { it.copy(imageTaggingEnabled = !enabled) }; toast(S.appSettingsErrorSetting()) }
        finally { state.update { it.copy(savingImageTagging = false) } }
    }

    /** Same RSA-OAEP flow as sign-in (issue #2); the stored password follows on success. */
    suspend fun changePassword(secrets: SecretsStore) {
        val s = state.value
        if (s.oldKey.isEmpty() || s.newKey.isEmpty()) { toast(S.appSettingsPasswordFillAll()); return }
        if (s.newKey != s.confirmKey) { toast(S.appSettingsPasswordMismatch()); return }
        state.update { it.copy(savingKey = true) }
        try {
            val pk = OTCConnection.request { it.setReqGetPubKey(GetPubKey.getDefaultInstance()) }
            if (pk.payloadCase != RespEnvelope.PayloadCase.RESP_PUB_KEY) { toast(S.appSettingsPasswordNoKey()); return }
            val der = pk.respPubKey.publicKey.toByteArray()
            val encOld = PwCrypto.encryptPassword(s.oldKey, der)
            val encNew = PwCrypto.encryptPassword(s.newKey, der)
            val resp = OTCConnection.request { it.setReqChangeKey(ChangeKey.newBuilder().setOldKey(ByteString.copyFrom(encOld)).setNewKey(ByteString.copyFrom(encNew))) }
            if (resp.ackOk()) {
                secrets.setPassword(s.newKey); secrets.persist()
                state.update { it.copy(oldKey = "", newKey = "", confirmKey = "") }
                toast(S.appSettingsPasswordChanged())
            } else toast(resp.ackError(S.appSettingsPasswordFailed()))
        } catch (e: Exception) {
            // The detail as it reads now ("null" when the exception has no message, as before).
            toast(S.appSettingsPasswordError("${e.message}"))
        }
        finally { state.update { it.copy(savingKey = false) } }
    }

    fun toast(m: UiText) {
        state.update { it.copy(toast = m) }
        viewModelScope.launch { delay(2500); state.update { if (it.toast == m) it.copy(toast = null) else it } }
    }
}

class StatusViewModel : ViewModel() {
    data class State(val status: Status? = null, val errorText: UiText? = null)
    val state = MutableStateFlow(State())
    private var poll: Job? = null
    private var pollMs = 0L
    // When the last ask went out (elapsed realtime), so a restarted poll
    // doesn't ask again sooner than its period.
    private var askedAt = 0L

    /**
     * Asks every [periodMs] (Settings: 5 s; the wide layout's menu more
     * slowly); a new period restarts the poll, its first ask a period after
     * the last one (at once if that is past).
     */
    fun start(periodMs: Long = 5000) {
        if (poll != null && periodMs == pollMs) return
        poll?.cancel()
        pollMs = periodMs
        poll = viewModelScope.launch {
            val wait = if (askedAt == 0L) 0L else askedAt + periodMs - android.os.SystemClock.elapsedRealtime()
            if (wait > 0) delay(wait)
            while (isActive) { fetch(); delay(periodMs) }
        }
    }

    fun stop() { poll?.cancel(); poll = null }

    private suspend fun fetch() {
        askedAt = android.os.SystemClock.elapsedRealtime()
        try {
            val resp = OTCConnection.request { it.setReqGetStatus(GetStatus.getDefaultInstance()) }
            if (resp.error) state.update { it.copy(errorText = UiText.Raw(resp.errorMessage), status = null) }
            else if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_STATUS) {
                state.update { it.copy(status = resp.respStatus, errorText = null) }
                cloud.offthe.otc.i18n.LanguageSettings.deviceSaid(resp.respStatus)
                // Every Status answer says whether it is a demo device.
                OTCConnection.deviceSaid(resp.respStatus)
            }
        } catch (e: Exception) { state.update { it.copy(errorText = e.shownText()) } }
    }
}

/** Issue #94: in-place updates, primary instance only. */
class UpdateViewModel : ViewModel() {
    data class State(
        val isPrimary: Boolean = false, val currentVersion: Int = 0, val latestVersion: Int = 0, val pending: List<UpdateRelease> = emptyList(),
        val currentLabel: String = "", val latestLabel: String = "",
        // message: the device's own words about the update in progress; startedHere: it was just
        // started from here and the device hasn't said anything since ("Starting" stands in).
        val state: String = "", val message: String = "", val startedHere: Boolean = false, val checkError: String = "", val lastUpdated: String = "",
        val checking: Boolean = false, val starting: Boolean = false, val error: UiText? = null,
        // Settings > Restart device: "" (nothing asked), "asking", "restarting", "back" or "slow".
        val restartPhase: String = "", val restartError: UiText? = null,
    ) {
        val hasUpdate get() = pending.isNotEmpty()
        val running get() = state == "running"
        val restarting get() = restartPhase == "asking" || restartPhase == "restarting"
        /** Issue #183: "1.1 (build 45)", or "build 45" for an older release. */
        val installedText: UiText get() =
            if (currentLabel.isNotEmpty()) S.appSettingsUpdateLabelBuild(currentLabel, "$currentVersion") else S.appSettingsUpdateBuild("$currentVersion")
        val latestText: UiText get() = if (latestLabel.isEmpty()) S.appSettingsUpdateBuild("$latestVersion") else UiText.Raw(latestLabel)
        /** The update button: "Update to 1.2", or "Update to build 46". */
        val updateToText: UiText get() =
            if (latestLabel.isEmpty()) S.appSettingsUpdateUpdateToBuild("$latestVersion") else S.appSettingsUpdateUpdateTo(latestLabel)
        /** What the update in progress is doing: the device's words, or "Starting" right after it was started here. */
        val statusMessage: UiText get() = if (startedHere) S.appSettingsUpdateStartingStatus() else UiText.Raw(message)
        val latestKind get() = pending.firstOrNull { it.version == latestVersion }?.kind ?: ""
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
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_UPDATE_INFO) { if (resp.error) state.update { it.copy(error = UiText.Raw(resp.errorMessage)) }; return }
            val i = resp.respUpdateInfo
            state.update { it.copy(currentVersion = i.currentVersion, latestVersion = i.latestVersion, currentLabel = i.currentLabel, latestLabel = i.latestLabel, pending = i.pendingList, state = i.state, message = i.message, startedHere = false, checkError = i.checkError, lastUpdated = i.lastUpdated, error = null) }
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
            if (resp.ackOk()) { state.update { it.copy(state = "running", message = "", startedHere = true) }; startPollingIfRunning() }
            else state.update { it.copy(error = if (resp.error) UiText.Raw(resp.errorMessage) else S.appSettingsUpdateStartFailed()) }
        } catch (e: Exception) { state.update { it.copy(error = e.shownText()) } }
        finally { state.update { it.copy(starting = false) } }
    }

    /**
     * Restarts the whole device (owner of the primary only; the device checks
     * too), then waits for it to answer again. An answer only counts once a
     * request has failed since, or 90 s have passed: until the restart
     * really begins the device still answers.
     */
    suspend fun restart() {
        state.update { it.copy(restartPhase = "asking", restartError = null) }
        try {
            val resp = OTCConnection.request { it.setReqRestartDevice(RestartDevice.getDefaultInstance()) }
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_RESTARTING_DEVICE) {
                val why = when {
                    resp.error && resp.errorCode == "unknown_payload" -> S.appSettingsUpdateRestartOld()
                    resp.error -> deviceOr(resp.errorMessage, S.appSettingsUpdateRestartFailed())
                    else -> S.appSettingsUpdateRestartFailed()
                }
                state.update { it.copy(restartPhase = "", restartError = why) }
                return
            }
        } catch (e: Exception) {
            state.update { it.copy(restartPhase = "", restartError = e.shownText() ?: S.appSettingsUpdateRestartFailed()) }
            return
        }
        state.update { it.copy(restartPhase = "restarting") }
        restartPoll?.cancel()
        restartPoll = viewModelScope.launch { waitUntilBack(System.currentTimeMillis()) }
    }

    private var restartPoll: Job? = null

    private suspend fun waitUntilBack(asked: Long) {
        var failed = false
        delay(20_000)
        while (System.currentTimeMillis() - asked < 5 * 60_000) {
            // Through the bridge a device that is away still gets an answer -
            // the bridge's error - which isn't the device.
            val answered = try {
                OTCConnection.request { it.setReqGetInstanceRole(ReqGetInstanceRole.getDefaultInstance()) }
                    .payloadCase == RespEnvelope.PayloadCase.RESP_INSTANCE_ROLE
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) { false }
            if (!answered) failed = true
            if (answered && (failed || System.currentTimeMillis() - asked >= 90_000)) {
                state.update { it.copy(restartPhase = "back") }
                check()
                return
            }
            delay(5000)
        }
        state.update { it.copy(restartPhase = "slow") }
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
        val publicUrl: String = "", val loginUrl: String = "", val authKey: String = "", val busy: Boolean = false, val note: UiText? = null, val error: UiText? = null,
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
        catch (e: Exception) { state.update { it.copy(error = e.shownText()) } }
    }

    suspend fun configure(enable: Boolean) {
        state.update { it.copy(busy = true, note = null, error = null) }
        try {
            val resp = OTCConnection.request { it.setReqSetupTailscale(ReqSetupTailscale.newBuilder().setAuthKey(state.value.authKey.trim()).setEnable(enable)) }
            if (!apply(resp)) { state.update { it.copy(error = if (resp.error) UiText.Raw(resp.errorMessage) else S.appSettingsTailscaleChangeFailed()) }; return }
            val s = state.value
            val note = when {
                !enable -> S.appSettingsTailscaleOff()
                !s.installed -> null
                s.loginUrl.isNotEmpty() -> S.appSettingsTailscaleAuthorise()
                s.funnelOn -> S.appSettingsTailscaleDone(s.publicUrl)
                else -> S.appSettingsTailscaleNoFunnel()
            }
            state.update { it.copy(note = note, error = if (enable && !s.installed) S.appSettingsTailscaleNotInstalledOn() else it.error) }
        } catch (e: Exception) { state.update { it.copy(error = e.shownText()) } }
        finally { state.update { it.copy(busy = false) } }
    }

    private fun apply(resp: RespEnvelope): Boolean {
        if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_TAILSCALE_STATUS) return false
        val s = resp.respTailscaleStatus
        state.update { it.copy(installed = s.installed, loggedIn = s.loggedIn, funnelOn = s.funnelOn, publicUrl = s.publicUrl, loginUrl = s.loginUrl, error = if (s.error.isEmpty()) it.error else UiText.Raw(s.error)) }
        return true
    }
}

/** Issue #82: additional users on one device, primary only. */
class UsersManagementViewModel : ViewModel() {
    data class State(
        val isPrimary: Boolean = false, val users: List<User> = emptyList(), val loading: Boolean = false, val metrics: Map<String, RespUserMetrics> = emptyMap(),
        val newUsername: String = "", val newPort: String = "", val creating: Boolean = false,
        val deleteTargetUuid: String? = null, val deleteConfirmText: String = "", val deleting: Boolean = false,
        val busyActiveUuid: String? = null, val toast: UiText? = null,
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
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_USERS) state.update { it.copy(users = resp.respUsers.usersList, newUsername = "", newPort = "", toast = S.appSettingsUsersCreated(username)) }
            else state.update { it.copy(toast = deviceOr(resp.errorMessage, S.appSettingsUsersCreateFailed())) }
        } catch (e: Exception) { state.update { it.copy(toast = e.shownText()) } }
        finally { state.update { it.copy(creating = false) } }
    }

    suspend fun confirmDelete(u: User) {
        state.update { it.copy(deleting = true) }
        try {
            val resp = OTCConnection.request { it.setReqDeleteUser(ReqDeleteUser.newBuilder().setUuid(u.uuid).setConfirmUsername(state.value.deleteConfirmText)) }
            if (resp.ackOk()) { state.update { it.copy(toast = S.appSettingsUsersDeleted(u.username), deleteTargetUuid = null, deleteConfirmText = "") }; loadUsers() }
            else state.update { it.copy(toast = deviceOr(resp.errorMessage, S.appSettingsUsersDeleteFailed())) }
        } catch (e: Exception) { state.update { it.copy(toast = e.shownText()) } }
        finally { state.update { it.copy(deleting = false) } }
    }

    suspend fun toggleActive(u: User) {
        state.update { it.copy(busyActiveUuid = u.uuid) }
        try {
            val resp = OTCConnection.request { it.setReqSetUserActive(ReqSetUserActive.newBuilder().setUuid(u.uuid).setActive(!u.active)) }
            if (resp.ackOk()) loadUsers() else state.update { it.copy(toast = deviceOr(resp.errorMessage, S.appSettingsUsersUpdateFailed())) }
        } catch (e: Exception) { state.update { it.copy(toast = e.shownText()) } }
        finally { state.update { it.copy(busyActiveUuid = null) } }
    }
}

/** The address a local-only device answers at when it didn't say its own (issue #182). */
const val LOCAL_DEVICE_ENDPOINT = "ws://otc.local:8080/ws"

/** The endpoint for a device's home-network address ("192.168.1.20:8080"). */
fun localEndpoint(address: String) = if (address.isEmpty()) LOCAL_DEVICE_ENDPOINT else "ws://$address/ws"

/**
 * Issue #182: the bridge and the Off The Cloud account, from Settings.
 * Leaving gives the device's name back; it restarts local-only and the app
 * then talks to it at [LOCAL_DEVICE_ENDPOINT]. Mirrors iOS's
 * BridgeAccountViewModel and the web app's BridgePanel.
 */
class BridgeAccountViewModel : ViewModel() {
    data class State(
        val visible: Boolean = false, val enabled: Boolean = false, val domain: String = "", val bridge: String = "off-the.cloud",
        val pending: Boolean = false, val leftReason: String = "", val localAddress: String = "", val busy: Boolean = false, val note: UiText? = null, val error: UiText? = null,
    )
    val state = MutableStateFlow(State())

    suspend fun load() {
        try {
            val resp = OTCConnection.request { it.setReqGetBridgeAccess(ReqGetBridgeAccess.getDefaultInstance()) }
            // An additional user's instance answers with an error: not theirs.
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_BRIDGE_ACCESS) { state.update { it.copy(visible = false) }; return }
            apply(resp.respBridgeAccess)
            state.update { it.copy(visible = true) }
        } catch (_: Exception) { state.update { it.copy(visible = false) } }
    }

    private fun apply(a: cloud.offthe.otc.proto.RespBridgeAccess) = state.update {
        it.copy(enabled = a.enabled, domain = a.domain, bridge = a.bridge.ifEmpty { "off-the.cloud" }, pending = a.pending,
            leftReason = a.leftReason, localAddress = a.localAddress.ifEmpty { it.localAddress }, error = if (a.error.isEmpty()) it.error else UiText.Raw(a.error))
    }

    /** Takes the device off the bridge and points the app at its local address. */
    suspend fun leave(secrets: SecretsStore): Boolean {
        state.update { it.copy(busy = true, note = null, error = null) }
        try {
            val resp = OTCConnection.request { it.setReqDisableBridge(ReqDisableBridge.getDefaultInstance()) }
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_BRIDGE_ACCESS) {
                state.update { it.copy(error = deviceOr(resp.errorMessage, S.appSettingsBridgeLeaveFailed())) }
                return false
            }
            apply(resp.respBridgeAccess)
            secrets.setEndpoint(localEndpoint(state.value.localAddress))
            secrets.persist()
            OTCConnection.invalidate()
            state.update { it.copy(note = S.appSettingsBridgeLeft(it.localAddress.ifEmpty { "otc.local" })) }
            return true
        } catch (e: Exception) {
            state.update { it.copy(error = e.shownText() ?: S.appSettingsBridgeLeaveFailed()) }
            return false
        } finally { state.update { it.copy(busy = false) } }
    }
}
