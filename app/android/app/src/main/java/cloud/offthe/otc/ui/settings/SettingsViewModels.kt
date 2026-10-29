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
import cloud.offthe.otc.proto.GetWifiStatus
import cloud.offthe.otc.proto.JoinWifi
import cloud.offthe.otc.proto.ListWifiNetworks
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
import cloud.offthe.otc.proto.WifiNetwork
import cloud.offthe.otc.proto.WifiStatus
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
import cloud.offthe.otc.i18n.appSettingsTailscaleKeyIgnored
import cloud.offthe.otc.i18n.appShellNoPublicKey
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
import cloud.offthe.otc.i18n.appSettingsUsersCreatedNoPassword
import cloud.offthe.otc.i18n.appSettingsUsersDeleteFailed
import cloud.offthe.otc.i18n.appSettingsUsersDeleted
import cloud.offthe.otc.i18n.appSettingsUsersUpdateFailed
import cloud.offthe.otc.i18n.appSettingsWifiFailed
import cloud.offthe.otc.i18n.appSettingsWifiJoining
import cloud.offthe.otc.i18n.appSettingsWifiScanFailed
import cloud.offthe.otc.i18n.appSettingsWifiUnknownResult
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
import kotlin.time.Duration.Companion.milliseconds

// Ports of the view models behind SettingsView.swift, ProfileEditor.swift,
// UpdateSection.swift, TailscaleSection.swift and UsersManagementView.swift.
// What they show is UiText, worded where it is drawn (docs/i18n.md, "Each
// platform"): our own words through the S accessors, the device's as
// UiText.Raw (it already wrote them in the request's language).

/**
 * Whether the account signed in is the device's owner (the primary
 * instance), which alone has Settings' Users; null when it couldn't be
 * asked. The tour asks it too, for its step about users.
 */
internal suspend fun askIsPrimary(): Boolean? = try {
    val r = OTCConnection.request { it.setReqGetInstanceRole(ReqGetInstanceRole.getDefaultInstance()) }
    if (r.payloadCase == RespEnvelope.PayloadCase.RESP_INSTANCE_ROLE) r.respInstanceRole.isPrimary else null
} catch (e: Exception) { null }

/**
 * The role as Settings' sections take it: whether this is the primary
 * instance - a reply that isn't the role (an error) counts as not - or null
 * when no answer came, so a section that doesn't know yet keeps its spinner
 * and is asked again (LoadingSection).
 */
internal suspend fun answeredRole(): Boolean? = try {
    val r = OTCConnection.request { it.setReqGetInstanceRole(ReqGetInstanceRole.getDefaultInstance()) }
    r.payloadCase == RespEnvelope.PayloadCase.RESP_INSTANCE_ROLE && r.respInstanceRole.isPrimary
} catch (e: CancellationException) {
    throw e
} catch (e: Exception) { null }

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
    is WSClient.SentNoAnswer -> text
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
                secrets.saveCredentials(secrets.endpoint.value, s.newKey)
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
            }
        } catch (e: Exception) { state.update { it.copy(errorText = e.shownText()) } }
    }
}

/** Issue #94: in-place updates, primary instance only. */
class UpdateViewModel : ViewModel() {
    data class State(
        // answered: the role is known and, on the primary, the first check came back.
        val answered: Boolean = false,
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

    /** The role, then on the primary the update check; with no answer the section stays as it was. */
    suspend fun load() {
        val primary = answeredRole() ?: return
        state.update { it.copy(isPrimary = primary) }
        if (primary) check()
        state.update { it.copy(answered = true) }
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
        // answered: the role is known and, on the primary, the status asked once.
        val answered: Boolean = false,
        val isPrimary: Boolean = false, val installed: Boolean = false, val loggedIn: Boolean = false, val funnelOn: Boolean = false,
        val publicUrl: String = "", val loginUrl: String = "", val authKey: String = "", val busy: Boolean = false, val note: UiText? = null, val error: UiText? = null,
    )
    val state = MutableStateFlow(State())

    fun setAuthKey(v: String) = state.update { it.copy(authKey = v) }

    /** The role, then on the primary Funnel's status; with no answer the section stays as it was. */
    suspend fun load() {
        val primary = answeredRole() ?: return
        state.update { it.copy(isPrimary = primary) }
        if (primary) refresh()
        state.update { it.copy(answered = true) }
    }

    suspend fun refresh() {
        try { apply(OTCConnection.request { it.setReqGetTailscaleStatus(ReqGetTailscaleStatus.getDefaultInstance()) }) }
        catch (e: Exception) { state.update { it.copy(error = e.shownText()) } }
    }

    suspend fun configure(enable: Boolean) {
        state.update { it.copy(busy = true, note = null, error = null) }
        try {
            val key = if (enable) state.value.authKey.trim() else ""
            // The key sealed for this connection, as a password is (PwCrypto,
            // GetPubKey): the bridge relays this request, and the key signs a
            // machine in to the owner's tailnet. Never in the clear: a device
            // before release 143 ignores the sealed field and takes this as
            // no key at all (keyIgnored below).
            var sealed = ByteString.EMPTY
            if (key.isNotEmpty()) {
                val pk = OTCConnection.request { it.setReqGetPubKey(GetPubKey.getDefaultInstance()) }
                if (pk.payloadCase != RespEnvelope.PayloadCase.RESP_PUB_KEY) {
                    state.update { it.copy(error = deviceOr(pk.errorMessage, S.appShellNoPublicKey())) }
                    return
                }
                sealed = ByteString.copyFrom(PwCrypto.encryptPassword(key, pk.respPubKey.publicKey.toByteArray()))
            }
            val resp = OTCConnection.request { it.setReqSetupTailscale(ReqSetupTailscale.newBuilder().setAuthKeySealed(sealed).setEnable(enable)) }
            if (!apply(resp)) { state.update { it.copy(error = if (resp.error) UiText.Raw(resp.errorMessage) else S.appSettingsTailscaleChangeFailed()) }; return }
            val s = state.value
            val note = when {
                !enable -> S.appSettingsTailscaleOff()
                !s.installed -> null
                // With a key the device signs in by itself; asking for the
                // sign-in page anyway means it read no key: one too old for
                // the sealed field.
                s.loginUrl.isNotEmpty() -> if (key.isEmpty()) S.appSettingsTailscaleAuthorise() else S.appSettingsTailscaleKeyIgnored()
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
        // answered: the role is known and, on the primary, the users asked once.
        val answered: Boolean = false,
        val isPrimary: Boolean = false, val users: List<User> = emptyList(), val loading: Boolean = false, val metrics: Map<String, RespUserMetrics> = emptyMap(),
        val newUsername: String = "", val newPort: String = "", val creating: Boolean = false,
        // Issue #103: an address on the bridge for the new user, on by default as in the web app.
        val requestBridgeAccess: Boolean = true,
        // The dialog Add opens: the new account's first password, set on the
        // device as the account is created, so nobody can sign in first and
        // claim it. Its person changes it later in Change Password.
        val passwordFor: String? = null, val newPassword: String = "", val confirmPassword: String = "", val passwordError: UiText? = null,
        val deleteTargetUuid: String? = null, val deleteConfirmText: String = "", val deleting: Boolean = false,
        val busyActiveUuid: String? = null, val toast: UiText? = null,
    ) {
        // The same 8 the device asks for (session.MinPasswordLen).
        val passwordTooShort get() = newPassword.isNotEmpty() && newPassword.length < 8
        val passwordMismatch get() = confirmPassword.isNotEmpty() && newPassword != confirmPassword
        val canCreate get() = newPassword.length >= 8 && newPassword == confirmPassword
    }
    val state = MutableStateFlow(State())

    fun setNewUsername(v: String) = state.update { it.copy(newUsername = v) }
    fun setNewPassword(v: String) = state.update { it.copy(newPassword = v) }
    fun setConfirmPassword(v: String) = state.update { it.copy(confirmPassword = v) }
    fun askPassword() = state.update { it.copy(passwordFor = it.newUsername.trim(), newPassword = "", confirmPassword = "", passwordError = null) }
    fun cancelPassword() = state.update { if (it.creating) it else it.copy(passwordFor = null, newPassword = "", confirmPassword = "") }
    fun setNewPort(v: String) = state.update { it.copy(newPort = v) }
    fun setRequestBridgeAccess(v: Boolean) = state.update { it.copy(requestBridgeAccess = v) }
    fun setDeleteConfirmText(v: String) = state.update { it.copy(deleteConfirmText = v) }
    fun toggleDeleteTarget(uuid: String) = state.update { it.copy(deleteTargetUuid = if (it.deleteTargetUuid == uuid) null else uuid, deleteConfirmText = "") }
    fun dismissToast() = state.update { it.copy(toast = null) }

    /** The role, then on the primary the users; with no answer the section stays as it was. */
    suspend fun checkRole() {
        val primary = answeredRole() ?: return
        state.update { it.copy(isPrimary = primary) }
        if (primary) loadUsers()
        state.update { it.copy(answered = true) }
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
        val s = state.value
        val username = s.passwordFor ?: return
        if (!s.canCreate || s.creating) return
        state.update { it.copy(creating = true, passwordError = null) }
        try {
            // Same RSA-OAEP flow as sign-in (issue #2).
            val pk = OTCConnection.request { it.setReqGetPubKey(GetPubKey.getDefaultInstance()) }
            if (pk.payloadCase != RespEnvelope.PayloadCase.RESP_PUB_KEY) { state.update { it.copy(passwordError = S.appSettingsPasswordNoKey()) }; return }
            val password = PwCrypto.encryptPassword(s.newPassword, pk.respPubKey.publicKey.toByteArray())
            val resp = OTCConnection.request { it.setReqCreateUser(ReqCreateUser.newBuilder().setUsername(username).setPort(s.newPort.toIntOrNull() ?: 0).setRequestBridgeAccess(s.requestBridgeAccess).setPassword(ByteString.copyFrom(password))) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_USERS) {
                // Unset: a device older than the password, which created the
                // account without one (its first sign-in sets it).
                val created = if (resp.respUsers.hasPasswordSet()) S.appSettingsUsersCreated(username) else S.appSettingsUsersCreatedNoPassword(username)
                state.update { it.copy(users = resp.respUsers.usersList, newUsername = "", newPort = "", passwordFor = null, newPassword = "", confirmPassword = "", toast = created) }
            } else state.update { it.copy(passwordError = deviceOr(resp.errorMessage, S.appSettingsUsersCreateFailed())) }
        } catch (e: Exception) { state.update { it.copy(passwordError = e.shownText()) } }
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

/**
 * Settings › Wi-Fi: the network the device uses (the device's own Wi-Fi,
 * not this phone's), changed by its owner - another network, or the same
 * one with its new password. Owner of the primary instance, and only a
 * device that manages its Wi-Fi (GetWifiStatus managed); a device before
 * the section answers unknown_payload and isn't asked again until the next
 * sign-in. The password lives here only (never in saved state), is sealed
 * under the connection's key (as Change Password) and cleared once sealed
 * or when the section goes. After JoinWifi the device may not answer for
 * about a minute: GetWifiStatus is asked every 3 s (10 s once it is slow)
 * until the change ends, while the section is on screen. The rules are
 * WifiFlow's. Mirrors iOS's WifiViewModel and the web app's WifiPanel.
 */
class WifiViewModel : ViewModel() {
    enum class Phase { IDLE, SENDING, WAITING, SLOW }

    /** The network picked: a listed one (its name), or Other Network… (the name is typed, [State.name]). */
    data class Chosen(val ssid: String, val kind: WifiFlow.Kind)

    data class State(
        val visible: Boolean = false,
        // Shown or hidden, as the device answered; until then Settings shows a spinner.
        val answered: Boolean = false,
        // The device's network now.
        val ssid: String = "", val signal: Int = 0, val wired: Boolean = false,
        // The picker: what the device sees, open after Change Network.
        val picking: Boolean = false, val scanning: Boolean = false, val scanned: Boolean = false,
        val networks: List<WifiNetwork> = emptyList(),
        val chosen: Chosen? = null, val name: String = "", val password: String = "",
        val confirming: Boolean = false,
        // The network a change was sent for (and the name typed for it):
        // after a wrong password the picker opens on it again, even when
        // Settings was left meanwhile (leave() closes the picker).
        val tried: Chosen? = null, val triedName: String = "",
        val phase: Phase = Phase.IDLE,
        // The change waited for, and the network it goes to.
        val joinID: String = "", val joiningSsid: String = "",
        val note: UiText? = null, val error: UiText? = null,
    ) {
        /** The name Connect sends: the listed network's, or the one typed. */
        val target: String get() = chosen?.let { if (it.kind == WifiFlow.Kind.OTHER) name else it.ssid } ?: ""
        val busy: Boolean get() = phase != Phase.IDLE
    }

    val state = MutableStateFlow(State())
    private var poll: Job? = null
    // When the wait began (elapsed realtime), so it turns slow on time across a pause.
    private var waitStarted = 0L
    // The sign-in (OTCConnection.signIns) at which the device answered
    // unknown_payload: not asked again until the next one.
    @Volatile private var unsupportedAt: Int? = null
    // The section is on screen with the app in front: the wait polls only then.
    @Volatile private var shown = false

    // Only a change typed clears the error: the password field also reports
    // the view model's own clearing back, which must not wipe a refusal.
    fun setName(v: String) = state.update { if (it.name == v) it else it.copy(name = v, error = null) }
    fun setPassword(v: String) = state.update { if (it.password == v) it else it.copy(password = v, error = null) }

    /** On screen (or back from the background): the role, then the status - unless a change is being waited for, which goes on. */
    suspend fun load() {
        shown = true
        val s = state.value
        if (s.joinID.isNotEmpty()) { startPolling(); return }
        if (s.phase == Phase.SENDING) return
        if (unsupportedAt == OTCConnection.signIns.value) { hide(); return }
        if (!s.visible) {
            val primary = try {
                val r = OTCConnection.request { it.setReqGetInstanceRole(ReqGetInstanceRole.getDefaultInstance()) }
                r.payloadCase == RespEnvelope.PayloadCase.RESP_INSTANCE_ROLE && r.respInstanceRole.isPrimary
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                // Not known now: what is shown stays as it is (iOS's rule).
                return
            }
            if (!primary) { hide(); return }
        }
        refresh()
    }

    /** GetWifiStatus: whether the section shows, the network now, and a change still running. */
    suspend fun refresh() {
        if (state.value.busy) return
        val resp = try {
            OTCConnection.request(OTCConnection.LIST_TIMEOUT_MS) { it.setReqGetWifiStatus(GetWifiStatus.getDefaultInstance()) }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) { return }
        if (state.value.busy) {
            // The wait reads its own answers; this only keeps the network shown up to date.
            if (!resp.error && resp.payloadCase == RespEnvelope.PayloadCase.RESP_WIFI_STATUS && resp.respWifiStatus.managed) current(resp.respWifiStatus)
            return
        }
        when (WifiFlow.onLoad(resp)) {
            WifiFlow.Load.HIDE_UNTIL_SIGN_IN -> { unsupportedAt = OTCConnection.signIns.value; hide() }
            WifiFlow.Load.HIDE -> hide()
            WifiFlow.Load.SHOW -> {
                val st = resp.respWifiStatus
                current(st)
                state.update { it.copy(visible = true, answered = true) }
                WifiFlow.resumeID(st)?.let { startWaiting(it, st.lastJoin.ssid) }
            }
        }
    }

    /** Change Network: the picker, with what the device sees. */
    fun change() {
        if (state.value.busy) return
        state.update { it.copy(picking = true, chosen = null, name = "", password = "", note = null, error = null, networks = emptyList(), scanned = false) }
        scan()
    }

    /** Scan Again, and the picker's first look. */
    fun scan() {
        if (state.value.scanning) return
        state.update { it.copy(scanning = true, error = null) }
        viewModelScope.launch {
            try {
                val resp = OTCConnection.request(OTCConnection.LIST_TIMEOUT_MS) { it.setReqListWifiNetworks(ListWifiNetworks.getDefaultInstance()) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_WIFI_NETWORKS) state.update { it.copy(networks = resp.respWifiNetworks.networksList) }
                else state.update { it.copy(error = deviceOr(WifiFlow.words(resp).orEmpty(), S.appSettingsWifiScanFailed())) }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) { state.update { it.copy(error = e.shownText()) } }
            finally { state.update { it.copy(scanning = false, scanned = true) } }
        }
    }

    /** A listed network (one the device can't join stays unpicked). */
    fun choose(n: WifiNetwork) {
        val s = state.value
        if (!WifiFlow.joinable(n) || s.busy || (s.chosen?.kind != WifiFlow.Kind.OTHER && s.chosen?.ssid == n.ssid)) return
        state.update { it.copy(chosen = Chosen(n.ssid, if (n.secured) WifiFlow.Kind.SECURED else WifiFlow.Kind.OPEN), password = "", error = null, note = null) }
    }

    /** Other Network…: a name typed by hand, its password optional. */
    fun chooseOther() {
        if (state.value.busy || state.value.chosen?.kind == WifiFlow.Kind.OTHER) return
        state.update { it.copy(chosen = Chosen("", WifiFlow.Kind.OTHER), name = "", password = "", error = null, note = null) }
    }

    fun closePicker() = state.update { if (it.busy) it else it.copy(picking = false, chosen = null, name = "", password = "", confirming = false, error = null) }

    /** Connect: checked here as the device would, then confirmed. */
    fun connect() {
        val s = state.value
        val kind = s.chosen?.kind ?: return
        if (s.busy) return
        val problem = WifiFlow.check(s.target, s.password, kind)
        state.update { it.copy(error = problem?.let(WifiFlow::text), note = null, confirming = problem == null) }
    }

    fun cancelConfirm() = state.update { it.copy(confirming = false) }

    /** The confirmation's Connect: sealed and sent; the device answers at once and changes networks after. */
    fun join() {
        val s = state.value
        val kind = s.chosen?.kind ?: return
        if (s.busy) return
        val ssid = s.target
        val password = WifiFlow.sentPassword(s.password, kind)
        state.update { it.copy(confirming = false, phase = Phase.SENDING, note = null, error = null, tried = s.chosen, triedName = s.name) }
        viewModelScope.launch {
            val sealed = try {
                if (password.isEmpty()) ByteString.EMPTY else {
                    // Same RSA-OAEP flow as Change Password (issue #2).
                    val pk = OTCConnection.request { it.setReqGetPubKey(GetPubKey.getDefaultInstance()) }
                    if (pk.payloadCase != RespEnvelope.PayloadCase.RESP_PUB_KEY) {
                        state.update { it.copy(phase = Phase.IDLE, error = S.appSettingsPasswordNoKey()) }
                        return@launch
                    }
                    ByteString.copyFrom(PwCrypto.encryptPassword(password, pk.respPubKey.publicKey.toByteArray()))
                }
            } catch (e: CancellationException) {
                state.update { it.copy(phase = Phase.IDLE) }
                throw e
            } catch (e: Exception) {
                // Nothing sent: the password stays for another try.
                state.update { it.copy(phase = Phase.IDLE, error = e.shownText() ?: S.appSettingsWifiFailed()) }
                return@launch
            }
            state.update { it.copy(password = "") }
            val resp = try {
                OTCConnection.request(OTCConnection.LIST_TIMEOUT_MS) {
                    it.setReqJoinWifi(JoinWifi.newBuilder().setSsid(ssid).setPassword(sealed).setHidden(WifiFlow.hidden(kind)))
                }
            } catch (e: CancellationException) {
                state.update { it.copy(phase = Phase.IDLE) }
                throw e
            } catch (e: Exception) {
                // The device may have taken it before the connection went:
                // the status says, and a change under way is waited for.
                state.update { it.copy(phase = Phase.IDLE, error = e.shownText() ?: S.appSettingsWifiFailed()) }
                refresh()
                return@launch
            }
            if (!resp.error && resp.payloadCase == RespEnvelope.PayloadCase.RESP_WIFI_STATUS && resp.respWifiStatus.managed) current(resp.respWifiStatus)
            when (val sent = WifiFlow.afterJoin(resp)) {
                is WifiFlow.Sent.Wait -> startWaiting(sent.id, ssid)
                is WifiFlow.Sent.Done -> finish(sent.outcome)
                is WifiFlow.Sent.Refused -> {
                    state.update { it.copy(phase = Phase.IDLE, error = deviceOr(sent.message.orEmpty(), S.appSettingsWifiFailed())) }
                    // A change already under way (busy) is waited for.
                    refresh()
                }
                WifiFlow.Sent.Hide -> {
                    // Doesn't manage its Wi-Fi after all: its words, and the
                    // status again, which hides the section.
                    state.update { it.copy(phase = Phase.IDLE, error = deviceOr(WifiFlow.words(resp).orEmpty(), S.appSettingsWifiFailed())) }
                    refresh()
                }
                WifiFlow.Sent.HideUntilSignIn -> {
                    unsupportedAt = OTCConnection.signIns.value
                    state.update { it.copy(phase = Phase.IDLE) }
                    hide()
                }
            }
        }
    }

    /** Off screen: no more looks (a change still running is waited for again on load), and nothing typed is kept. */
    fun leave() {
        pause()
        state.update { it.copy(picking = false, chosen = null, name = "", password = "", confirming = false, note = if (it.busy) it.note else null, error = if (it.busy) it.error else null) }
    }

    /** In the background: no more looks until the app is back (load). */
    fun pause() { shown = false; poll?.cancel(); poll = null }

    private fun startWaiting(id: String, ssid: String) {
        waitStarted = android.os.SystemClock.elapsedRealtime()
        state.update { it.copy(phase = Phase.WAITING, joinID = id, joiningSsid = ssid, note = S.appSettingsWifiJoining(ssid), error = null, confirming = false) }
        startPolling()
    }

    private fun startPolling() {
        if (!shown || poll?.isActive == true) return
        val id = state.value.joinID
        if (id.isEmpty()) return
        poll = viewModelScope.launch {
            while (isActive) {
                val slow = WifiFlow.slow((android.os.SystemClock.elapsedRealtime() - waitStarted).milliseconds)
                if (slow && state.value.phase == Phase.WAITING) state.update { it.copy(phase = Phase.SLOW) }
                delay(WifiFlow.pollInterval(slow))
                // Through the bridge a device that is away still answers - the
                // bridge's error - and a dropped connection throws: both wait on.
                val resp = try {
                    OTCConnection.request(WifiFlow.lookTimeout.inWholeMilliseconds) { it.setReqGetWifiStatus(GetWifiStatus.getDefaultInstance()) }
                } catch (e: CancellationException) {
                    throw e
                } catch (_: Exception) { continue }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_WIFI_STATUS && resp.respWifiStatus.managed) current(resp.respWifiStatus)
                val p = WifiFlow.onPoll(resp, id)
                if (p is WifiFlow.Poll.Done) { finish(p.outcome); break }
            }
        }
    }

    private fun finish(o: WifiFlow.Outcome) {
        poll?.cancel(); poll = null
        // Reopened after Settings was left: its list is gone, look again.
        val relist = o is WifiFlow.Outcome.Failed && WifiFlow.keepsPicker(o.code) && state.value.tried != null && state.value.networks.isEmpty()
        state.update {
            val base = it.copy(phase = Phase.IDLE, joinID = "", joiningSsid = "", confirming = false, tried = null, triedName = "")
            when (o) {
                is WifiFlow.Outcome.Connected -> base.copy(picking = false, chosen = null, name = "", password = "", note = o.message.ifEmpty { null }?.let { m -> UiText.Raw(m) }, error = null)
                is WifiFlow.Outcome.Failed -> {
                    // Open again on the network tried, Settings left meanwhile or not.
                    val tried = it.tried
                    val keep = WifiFlow.keepsPicker(o.code) && tried != null
                    base.copy(picking = keep, chosen = if (keep) tried else null, name = if (keep && tried?.kind == WifiFlow.Kind.OTHER) it.triedName else it.name,
                        password = "", note = null, error = deviceOr(o.message, S.appSettingsWifiFailed()))
                }
                WifiFlow.Outcome.Unknown -> base.copy(picking = false, chosen = null, name = "", password = "", note = S.appSettingsWifiUnknownResult(), error = null)
            }
        }
        if (relist) scan()
    }

    private fun current(s: WifiStatus) = state.update { it.copy(ssid = s.ssid, signal = s.signal, wired = s.wired) }

    private fun hide() = state.update {
        it.copy(visible = false, answered = true, picking = false, chosen = null, name = "", password = "", confirming = false, networks = emptyList(), note = null, error = null,
            tried = null, triedName = "")
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
        // answered: the device has said whether the section is this account's.
        val answered: Boolean = false,
        val visible: Boolean = false, val enabled: Boolean = false, val domain: String = "", val bridge: String = "off-the.cloud",
        val pending: Boolean = false, val leftReason: String = "", val localAddress: String = "", val busy: Boolean = false, val note: UiText? = null, val error: UiText? = null,
    )
    val state = MutableStateFlow(State())

    suspend fun load() {
        try {
            val resp = OTCConnection.request { it.setReqGetBridgeAccess(ReqGetBridgeAccess.getDefaultInstance()) }
            // An additional user's instance answers with an error: not theirs.
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_BRIDGE_ACCESS) { state.update { it.copy(visible = false, answered = true) }; return }
            apply(resp.respBridgeAccess)
            state.update { it.copy(visible = true, answered = true) }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            // No answer: what is shown stays as it is, and it is asked again.
        }
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
            secrets.saveCredentials(localEndpoint(state.value.localAddress), secrets.password.value)
            OTCConnection.invalidate()
            state.update { it.copy(note = S.appSettingsBridgeLeft(it.localAddress.ifEmpty { "otc.local" })) }
            return true
        } catch (e: Exception) {
            state.update { it.copy(error = e.shownText() ?: S.appSettingsBridgeLeaveFailed()) }
            return false
        } finally { state.update { it.copy(busy = false) } }
    }
}

/**
 * Settings > Logs' row, the primary instance only: whether to show it, as
 * the device answered (iOS's LogsSectionViewModel). A view model, so a
 * known answer stays while Settings comes and goes, as the other sections'.
 */
class LogsSectionViewModel : ViewModel() {
    data class State(val answered: Boolean = false, val isPrimary: Boolean = false)
    val state = MutableStateFlow(State())

    suspend fun load() {
        val primary = answeredRole() ?: return
        state.update { State(answered = true, isPrimary = primary) }
    }
}
