// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import androidx.compose.ui.text.buildAnnotatedString
import cloud.offthe.otc.ui.logOut
import android.Manifest
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.PickVisualMediaRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.Check
import androidx.compose.material3.Icon
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import cloud.offthe.otc.ui.common.THUMB_CACHE_CHOICES
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.formatCacheBytes
import cloud.offthe.otc.ui.common.decimalText
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.data.UploadModel
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.RaidState
import cloud.offthe.otc.proto.Status
import cloud.offthe.otc.proto.User
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.ui.AvatarView
import cloud.offthe.otc.ui.common.ConnectionEndpointFields
import cloud.offthe.otc.ui.common.EndpointEntry
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.Toast
import cloud.offthe.otc.ui.compose.mediaPermissions
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import cloud.offthe.otc.MainActivity
import cloud.offthe.otc.i18n.LanguageSettings
import cloud.offthe.otc.i18n.RichText
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidSettingsBridgeLeaveDelete
import cloud.offthe.otc.i18n.androidSettingsLogsFooter
import cloud.offthe.otc.i18n.androidSettingsProfileDescription
import cloud.offthe.otc.i18n.androidSettingsProfileFromPhone
import cloud.offthe.otc.i18n.androidSettingsSharingFooter
import cloud.offthe.otc.i18n.androidSettingsSyncCloud
import cloud.offthe.otc.i18n.androidSettingsSyncTitle
import cloud.offthe.otc.i18n.androidSettingsThumbsChangeSize
import cloud.offthe.otc.i18n.androidSettingsThumbsMaxSizeA11y
import cloud.offthe.otc.i18n.androidSettingsThumbsSelected
import cloud.offthe.otc.i18n.androidSettingsThumbsTitle
import cloud.offthe.otc.i18n.androidSettingsUpdateVersionUnknown
import cloud.offthe.otc.i18n.androidSettingsUploadPending
import cloud.offthe.otc.i18n.androidSettingsUploadWaiting
import cloud.offthe.otc.i18n.androidSettingsUploading
import cloud.offthe.otc.i18n.appSettingsBridgeAccountPage
import cloud.offthe.otc.i18n.appSettingsBridgeAtHome
import cloud.offthe.otc.i18n.appSettingsBridgeDeleteAccount
import cloud.offthe.otc.i18n.appSettingsBridgeDeleteMessage
import cloud.offthe.otc.i18n.appSettingsBridgeDeleteTitle
import cloud.offthe.otc.i18n.appSettingsBridgeFooter
import cloud.offthe.otc.i18n.appSettingsBridgeLeave
import cloud.offthe.otc.i18n.appSettingsBridgeLeaveConfirm
import cloud.offthe.otc.i18n.appSettingsBridgeLeaveMessage
import cloud.offthe.otc.i18n.appSettingsBridgeLeaveTitle
import cloud.offthe.otc.i18n.appSettingsBridgeLocalOnly
import cloud.offthe.otc.i18n.appSettingsBridgeReachable
import cloud.offthe.otc.i18n.appSettingsBridgeReleased
import cloud.offthe.otc.i18n.appSettingsBridgeTitle
import cloud.offthe.otc.i18n.appSettingsConnectionDeviceId
import cloud.offthe.otc.i18n.appSettingsConnectionLogOut
import cloud.offthe.otc.i18n.appSettingsConnectionLogOutConfirm
import cloud.offthe.otc.i18n.appSettingsConnectionLogOutMessage
import cloud.offthe.otc.i18n.appSettingsConnectionLogOutTitle
import cloud.offthe.otc.i18n.appSettingsConnectionSave
import cloud.offthe.otc.i18n.appSettingsConnectionTitle
import cloud.offthe.otc.i18n.appSettingsFacesFooter
import cloud.offthe.otc.i18n.appSettingsFacesTitle
import cloud.offthe.otc.i18n.appSettingsFacesToggle
import cloud.offthe.otc.i18n.appSettingsLanguage
import cloud.offthe.otc.i18n.appSettingsLinksTitle
import cloud.offthe.otc.i18n.appSettingsLogsTitle
import cloud.offthe.otc.i18n.appSettingsPasswordChange
import cloud.offthe.otc.i18n.appSettingsPasswordChanging
import cloud.offthe.otc.i18n.appSettingsPasswordConfirm
import cloud.offthe.otc.i18n.appSettingsPasswordCurrent
import cloud.offthe.otc.i18n.appSettingsPasswordNew
import cloud.offthe.otc.i18n.appSettingsPasswordTitle
import cloud.offthe.otc.i18n.appSettingsProfileChangePhoto
import cloud.offthe.otc.i18n.appSettingsProfileFromDevice
import cloud.offthe.otc.i18n.appSettingsProfileName
import cloud.offthe.otc.i18n.appSettingsProfileSave
import cloud.offthe.otc.i18n.appSettingsProfileTitle
import cloud.offthe.otc.i18n.appSettingsReprocessAll
import cloud.offthe.otc.i18n.appSettingsReprocessCompleted
import cloud.offthe.otc.i18n.appSettingsReprocessConfirmRestart
import cloud.offthe.otc.i18n.appSettingsReprocessConfirmResume
import cloud.offthe.otc.i18n.appSettingsReprocessFailed
import cloud.offthe.otc.i18n.appSettingsReprocessFooter
import cloud.offthe.otc.i18n.appSettingsReprocessRestartButton
import cloud.offthe.otc.i18n.appSettingsReprocessResume
import cloud.offthe.otc.i18n.appSettingsReprocessResumeButton
import cloud.offthe.otc.i18n.appSettingsReprocessRunning
import cloud.offthe.otc.i18n.appSettingsReprocessStartOver
import cloud.offthe.otc.i18n.appSettingsReprocessStarting
import cloud.offthe.otc.i18n.appSettingsReprocessStopped
import cloud.offthe.otc.i18n.appSettingsReprocessStopping
import cloud.offthe.otc.i18n.appSettingsReprocessTitle
import cloud.offthe.otc.i18n.appSettingsSharingTitle
import cloud.offthe.otc.i18n.appSettingsStatusCpuMem
import cloud.offthe.otc.i18n.appSettingsStatusDisk
import cloud.offthe.otc.i18n.appSettingsStatusRaid
import cloud.offthe.otc.i18n.appSettingsStatusRaidDegraded
import cloud.offthe.otc.i18n.appSettingsStatusRaidInSync
import cloud.offthe.otc.i18n.appSettingsStatusRaidLevel
import cloud.offthe.otc.i18n.appSettingsStatusRaidNone
import cloud.offthe.otc.i18n.appSettingsStatusRaidSyncing
import cloud.offthe.otc.i18n.appSettingsStatusRaidUnknown
import cloud.offthe.otc.i18n.appSettingsStatusRaidUsed
import cloud.offthe.otc.i18n.appSettingsStatusTitle
import cloud.offthe.otc.i18n.appSettingsStatusUsedPercent
import cloud.offthe.otc.i18n.appSettingsSyncAll
import cloud.offthe.otc.i18n.appSettingsSyncAuthorize
import cloud.offthe.otc.i18n.appSettingsSyncFooter
import cloud.offthe.otc.i18n.appSettingsSyncFromNow
import cloud.offthe.otc.i18n.appSettingsSyncTitle
import cloud.offthe.otc.i18n.appSettingsSyncVideos
import cloud.offthe.otc.i18n.appSettingsSyncWifiOnly
import cloud.offthe.otc.i18n.appSettingsFreeUpTitle
import cloud.offthe.otc.i18n.androidSettingsFreeUpFooter
import cloud.offthe.otc.sync.FreeUpSpace
import cloud.offthe.otc.i18n.appSettingsTaggingFooter
import cloud.offthe.otc.i18n.appSettingsTaggingTitle
import cloud.offthe.otc.i18n.appSettingsTailscaleAuthKey
import cloud.offthe.otc.i18n.appSettingsTailscaleEnable
import cloud.offthe.otc.i18n.appSettingsTailscaleIntro
import cloud.offthe.otc.i18n.appSettingsTailscaleKeyHint
import cloud.offthe.otc.i18n.appSettingsTailscaleLimitAccount
import cloud.offthe.otc.i18n.appSettingsTailscaleLimitBandwidth
import cloud.offthe.otc.i18n.appSettingsTailscaleLimitOneUser
import cloud.offthe.otc.i18n.appSettingsTailscaleLimitSocial
import cloud.offthe.otc.i18n.appSettingsTailscaleNotInstalled
import cloud.offthe.otc.i18n.appSettingsTailscaleServed
import cloud.offthe.otc.i18n.appSettingsTailscaleSuits
import cloud.offthe.otc.i18n.appSettingsTailscaleTurnOff
import cloud.offthe.otc.i18n.appSettingsTailscaleWorking
import cloud.offthe.otc.i18n.appSettingsThumbsClear
import cloud.offthe.otc.i18n.appSettingsThumbsFooter
import cloud.offthe.otc.i18n.appSettingsThumbsMaxSize
import cloud.offthe.otc.i18n.appSettingsUpdateBack
import cloud.offthe.otc.i18n.appSettingsUpdateBuild
import cloud.offthe.otc.i18n.appSettingsUpdateCheckAgain
import cloud.offthe.otc.i18n.appSettingsUpdateCheckError
import cloud.offthe.otc.i18n.appSettingsUpdateChecking
import cloud.offthe.otc.i18n.appSettingsUpdateCritical
import cloud.offthe.otc.i18n.appSettingsUpdateFailed
import cloud.offthe.otc.i18n.appSettingsUpdateFailedOn
import cloud.offthe.otc.i18n.appSettingsUpdateMajor
import cloud.offthe.otc.i18n.appSettingsUpdateRestart
import cloud.offthe.otc.i18n.appSettingsUpdateRestartConfirm
import cloud.offthe.otc.i18n.appSettingsUpdateRestartMessage
import cloud.offthe.otc.i18n.appSettingsUpdateRestartNote
import cloud.offthe.otc.i18n.appSettingsUpdateRestartTitle
import cloud.offthe.otc.i18n.appSettingsUpdateRestarting
import cloud.offthe.otc.i18n.appSettingsUpdateRestartingNote
import cloud.offthe.otc.i18n.appSettingsUpdateSlow
import cloud.offthe.otc.i18n.appSettingsUpdateStarting
import cloud.offthe.otc.i18n.appSettingsUpdateTitle
import cloud.offthe.otc.i18n.appSettingsUpdateUpToDate
import cloud.offthe.otc.i18n.appSettingsUploadsTitle
import cloud.offthe.otc.i18n.appSettingsUsersConfirmDelete
import cloud.offthe.otc.i18n.appSettingsUsersConfirmHint
import cloud.offthe.otc.i18n.appSettingsUsersCreate
import cloud.offthe.otc.i18n.appSettingsUsersDisable
import cloud.offthe.otc.i18n.appSettingsUsersEnable
import cloud.offthe.otc.i18n.appSettingsUsersInactive
import cloud.offthe.otc.i18n.appSettingsUsersIntro
import cloud.offthe.otc.i18n.appSettingsUsersLoadUsage
import cloud.offthe.otc.i18n.appSettingsUsersLocalHint
import cloud.offthe.otc.i18n.appSettingsUsersPassword
import cloud.offthe.otc.i18n.appSettingsUsersPasswordConfirm
import cloud.offthe.otc.i18n.appSettingsUsersPasswordHint
import cloud.offthe.otc.i18n.appSettingsUsersPasswordMismatch
import cloud.offthe.otc.i18n.appSettingsUsersPasswordTitle
import cloud.offthe.otc.i18n.appSettingsUsersPasswordTooShort
import cloud.offthe.otc.i18n.appSettingsUsersPort
import cloud.offthe.otc.i18n.appSettingsUsersPortDomain
import cloud.offthe.otc.i18n.appSettingsUsersReachableAt
import cloud.offthe.otc.i18n.appSettingsUsersReachableOwn
import cloud.offthe.otc.i18n.appSettingsUsersRequestBridge
import cloud.offthe.otc.i18n.appSettingsUsersTitle
import cloud.offthe.otc.i18n.appSettingsUsersUsage
import cloud.offthe.otc.i18n.appSettingsUsersUsername
import cloud.offthe.otc.i18n.appShellPrivacy
import cloud.offthe.otc.i18n.appShellRouteConfigured
import cloud.offthe.otc.i18n.appShellRouteHome
import cloud.offthe.otc.i18n.appShellRouteThrough
import cloud.offthe.otc.i18n.appShellThumbCacheUsage
import cloud.offthe.otc.i18n.commonAdd
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonDelete
import cloud.offthe.otc.i18n.commonDeleting
import cloud.offthe.otc.i18n.commonOk
import cloud.offthe.otc.i18n.commonPassword
import cloud.offthe.otc.i18n.commonPause
import cloud.offthe.otc.i18n.commonResume
import cloud.offthe.otc.i18n.commonUpdating
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.net.MediaStream
import cloud.offthe.otc.sync.AssetSyncCache
import cloud.offthe.otc.sync.SyncScheduler
import cloud.offthe.otc.ui.social.SocialFeedViewModel
import cloud.offthe.otc.ui.common.FitText

// Port of SettingsView.swift and its sections: Profile (issue #84),
// Users (#82), bridge secret (#40), face recognition (#52), reprocess
// (#73), password change, Tailscale (#80), status, uploads (#122),
// connection, sync options and device updates (#94).

@Composable
private fun Section(title: String, footer: String? = null, content: @Composable () -> Unit) {
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp)) {
        Text(title.uppercase(), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(bottom = 6.dp))
        Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).background(MaterialTheme.colorScheme.surfaceContainer).padding(12.dp)) { content() }
        footer?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 6.dp)) }
    }
}

@Composable
private fun Caption(text: String, color: Color = MaterialTheme.colorScheme.onSurfaceVariant) =
    Text(text, style = MaterialTheme.typography.bodySmall, color = color, modifier = Modifier.padding(vertical = 2.dp))

// An action: a filled button, red for what removes or signs out.
@Composable
private fun RowButton(label: String, enabled: Boolean = true, destructive: Boolean = false, onClick: () -> Unit) {
    val colors = if (destructive) ButtonDefaults.filledTonalButtonColors(containerColor = MaterialTheme.colorScheme.errorContainer, contentColor = MaterialTheme.colorScheme.onErrorContainer)
        else ButtonDefaults.filledTonalButtonColors()
    FilledTonalButton(onClick = onClick, enabled = enabled, colors = colors, modifier = Modifier.padding(vertical = 2.dp)) { Text(label) }
}

// What opens another screen or a web page stays a link.
@Composable
private fun RowLink(label: String, onClick: () -> Unit) {
    TextButton(onClick = onClick) { Text(label, color = MaterialTheme.colorScheme.primary) }
}

@Composable
fun SettingsView(secrets: SecretsStore) {
    val device: DeviceSettingsViewModel = viewModel()
    val status: StatusViewModel = viewModel()
    val dst by device.state.collectAsState()
    val upload by UploadModel.state.collectAsState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val endpoint by secrets.endpoint.collectAsState()
    val password by secrets.password.collectAsState()
    val deviceId by secrets.deviceId.collectAsState()
    val wifiOnly by secrets.wifiOnly.collectAsState()
    val includeVideos by secrets.includeVideos.collectAsState()
    val downloadFromCloud by secrets.downloadFromCloud.collectAsState()
    val signedIn by OTCConnection.authenticated.collectAsState()
    val route by OTCConnection.route.collectAsState()
    // Authorize Photos Access shows only while the app can't read the
    // photos. The first tap asks; after a refusal Android shows no dialog
    // again, so the next one opens the app's page in system settings.
    var photosAllowed by remember { mutableStateOf(PhotoSync.hasPermission(context)) }
    var photosAsked by remember { mutableStateOf(false) }
    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        photosAllowed = PhotoSync.hasPermission(context)
        photosAsked = true
    }
    val lifecycle = LocalLifecycleOwner.current
    DisposableEffect(lifecycle) {
        val obs = LifecycleEventObserver { _, e -> if (e == Lifecycle.Event.ON_RESUME) photosAllowed = PhotoSync.hasPermission(context) }
        lifecycle.lifecycle.addObserver(obs)
        onDispose { lifecycle.lifecycle.removeObserver(obs) }
    }
    var confirmLogout by remember { mutableStateOf(false) }
    // Issue #180: the shared links list (SharedLinksView.kt).
    var showSharedLinks by remember { mutableStateOf(false) }
    // Settings > Logs (LogsView.kt), primary instance only like Tailscale and Users.
    var showLogs by remember { mutableStateOf(false) }
    // Settings > Language (LanguageView.kt): saveable, since picking one
    // recreates the Activity and the screen stays open through it.
    var showLanguage by rememberSaveable { mutableStateOf(false) }
    val language by LanguageSettings.choice.collectAsState()
    var isPrimary by remember { mutableStateOf(false) }

    LaunchedEffect(Unit) {
        device.loadSettings()
        status.start()
        device.loadReprocessStatus()
        if (device.state.value.reprocessStatus == "running") device.pollReprocessStatusWhileRunning()
    }
    LaunchedEffect(Unit) { isPrimary = isPrimaryInstance() }
    DisposableEffect(Unit) { onDispose { status.stop(); device.stopPollingReprocessStatus() } }

    Box(Modifier.fillMaxSize()) {
        Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(vertical = 8.dp)) {
            ProfileEditorSection()

            // The language of every app signed in to the device; only while
            // this build has more than one.
            if (LanguageSettings.pickerShown) {
                Section(S.appSettingsLanguage().resolve()) {
                    Row(
                        Modifier.fillMaxWidth().clickable(role = Role.Button) { showLanguage = true }.padding(vertical = 6.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Text(LanguageSettings.currentName(language.language).resolve(), Modifier.weight(1f))
                        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, contentDescription = null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
            }

            Section(S.appSettingsStatusTitle().resolve()) { StatusSectionContent(status) }

            // The device's accounts, right under its status.
            UsersManagementSection()

            if (upload.totalPending > 0 || upload.isUploading) {
                Section(S.appSettingsUploadsTitle().resolve()) {
                    Text((if (upload.isUploading) S.androidSettingsUploading(upload.currentName) else S.androidSettingsUploadWaiting()).resolve(), style = MaterialTheme.typography.bodyMedium)
                    LinearProgressIndicator(progress = { upload.progress }, modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp))
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Caption(S.androidSettingsUploadPending(upload.totalPending).resolve())
                        Spacer(Modifier.weight(1f))
                        RowButton((if (upload.isPaused) S.commonResume() else S.commonPause()).resolve()) { UploadModel.togglePause() }
                    }
                }
            }

            Section(S.appSettingsSharingTitle().resolve(), S.androidSettingsSharingFooter().resolve()) {
                RowLink(S.appSettingsLinksTitle().resolve()) { showSharedLinks = true }
            }

            UpdateSection()

            Section(S.appSettingsPasswordTitle().resolve()) {
                PasswordField(dst.oldKey, S.appSettingsPasswordCurrent().resolve(), device::setOldKey)
                PasswordField(dst.newKey, S.appSettingsPasswordNew().resolve(), device::setNewKey)
                PasswordField(dst.confirmKey, S.appSettingsPasswordConfirm().resolve(), device::setConfirmKey)
                RowButton((if (dst.savingKey) S.appSettingsPasswordChanging() else S.appSettingsPasswordChange()).resolve(), enabled = !dst.savingKey && dst.oldKey.isNotEmpty() && dst.newKey.isNotEmpty()) { scope.launch { device.changePassword(secrets) } }
            }

            Section(S.appSettingsSyncTitle().resolve()) {
                ToggleRow(S.appSettingsSyncWifiOnly().resolve(), wifiOnly, secrets::setWifiOnly)
                ToggleRow(S.appSettingsSyncVideos().resolve(), includeVideos, secrets::setIncludeVideos)
                ToggleRow(S.androidSettingsSyncCloud().resolve(), downloadFromCloud, secrets::setDownloadFromCloud)
                if (!photosAllowed) RowButton(S.appSettingsSyncAuthorize().resolve()) {
                    if (!photosAsked) permission.launch(mediaPermissions())
                    else context.startActivity(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", context.packageName, null)))
                }
            }

            Section(S.androidSettingsSyncTitle().resolve(), S.appSettingsSyncFooter().resolve()) {
                RowButton(S.appSettingsSyncAll().resolve()) { secrets.persist(); PhotoSync.setWatermark(0); PhotoSync.runForegroundAsync() }
                RowButton(S.appSettingsSyncFromNow().resolve()) { secrets.persist(); PhotoSync.setWatermark(System.currentTimeMillis()) }
            }

            // Android 11 and later: MediaStore's trash request.
            if (FreeUpSpace.supported) Section(S.appSettingsFreeUpTitle().resolve(), S.androidSettingsFreeUpFooter().resolve()) { FreeUpSpaceControls() }

            ThumbnailCacheSection()

            Section(S.appSettingsTaggingTitle().resolve(), S.appSettingsTaggingFooter().resolve()) {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text(S.appSettingsTaggingTitle().resolve(), Modifier.weight(1f))
                    Switch(checked = dst.imageTaggingEnabled, enabled = !dst.savingImageTagging, onCheckedChange = { v -> scope.launch { device.toggleImageTagging(v) } })
                }
            }

            Section(S.appSettingsFacesTitle().resolve(), S.appSettingsFacesFooter().resolve()) {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text(S.appSettingsFacesToggle().resolve(), Modifier.weight(1f))
                    Switch(checked = dst.faceRecognitionEnabled, enabled = !dst.savingFaceRecognition, onCheckedChange = { v -> scope.launch { device.toggleFaceRecognition(v) } })
                }
            }

            Section(S.appSettingsConnectionTitle().resolve()) {
                // Edits stay here until Save Connection: a reconnect (a socket
                // drop while typing) dials the store's values. Keyed on the
                // saved ones, so a password change or leaving the bridge still
                // shows up in the fields. The address is saveable with the
                // device name it stands for, one holder for both, as Settings >
                // Language recreates the Activity with this screen open; the
                // password never goes into saved state.
                val editEntry = rememberSaveable(endpoint, saver = EndpointEntry.Saver) { EndpointEntry(endpoint) }
                var editPassword by remember(password) { mutableStateOf(password) }
                ConnectionEndpointFields(editEntry)
                PasswordField(editPassword, S.commonPassword().resolve()) { editPassword = it }
                routeLine(signedIn, route, endpoint)?.let { Caption(it.resolve()) }
                Caption(S.appSettingsConnectionDeviceId(deviceId).resolve())
                RowButton(S.appSettingsConnectionSave().resolve()) {
                    secrets.setEndpoint(editEntry.endpoint); secrets.setPassword(editPassword)
                    secrets.persist(); OTCConnection.invalidate()
                }
                RowButton(S.appSettingsConnectionLogOut().resolve(), destructive = true) { confirmLogout = true }
            }

            TailscaleSection()

            BridgeAccountSection(secrets)

            Section(S.appSettingsReprocessTitle().resolve(), S.appSettingsReprocessFooter().resolve()) {
                when (dst.reprocessStatus) {
                    "running" -> {
                        LinearProgressIndicator(progress = { if (dst.reprocessTotal > 0) dst.reprocessProcessed.toFloat() / dst.reprocessTotal else 0f }, modifier = Modifier.fillMaxWidth())
                        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                            // The rest of the row, wrapping, so the button keeps its
                            // room ("Wird neu verarbeitet… 42 % (…)" beside Abbrechen).
                            Text(S.appSettingsReprocessRunning(dst.reprocessPercent, dst.reprocessProcessed, dst.reprocessTotal).resolve(),
                                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                                modifier = Modifier.weight(1f).padding(vertical = 2.dp))
                            RowButton((if (dst.cancellingReprocess) S.appSettingsReprocessStopping() else S.commonCancel()).resolve(), enabled = !dst.cancellingReprocess, destructive = true) { scope.launch { device.stopReprocess() } }
                        }
                    }
                    "stopped" -> {
                        Caption(S.appSettingsReprocessStopped(dst.reprocessPercent, dst.reprocessProcessed, dst.reprocessTotal).resolve())
                        // At both ends of one line while they fit, else Start Over
                        // on a line of its own (Neuverarbeitung fortsetzen, Von vorn
                        // beginnen at 360 dp).
                        FlowRow(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                            RowButton((if (dst.startingReprocess) S.appSettingsReprocessStarting() else S.appSettingsReprocessResume()).resolve(), enabled = !dst.startingReprocess) { device.setReprocessConfirm(DeviceSettingsViewModel.ReprocessConfirm.RESUME) }
                            RowButton(S.appSettingsReprocessStartOver().resolve(), enabled = !dst.startingReprocess) { device.setReprocessConfirm(DeviceSettingsViewModel.ReprocessConfirm.RESTART) }
                        }
                    }
                    else -> {
                        RowButton((if (dst.startingReprocess) S.appSettingsReprocessStarting() else S.appSettingsReprocessAll()).resolve(), enabled = !dst.startingReprocess) { device.setReprocessConfirm(DeviceSettingsViewModel.ReprocessConfirm.RESTART) }
                        if (dst.reprocessStatus == "completed") Caption(S.appSettingsReprocessCompleted(dst.reprocessProcessed).resolve())
                        else if (dst.reprocessStatus == "failed") Caption(S.appSettingsReprocessFailed(dst.reprocessProcessed).resolve(), Color(0xFFE53935))
                    }
                }
            }

            if (isPrimary) {
                Section(S.appSettingsLogsTitle().resolve(), S.androidSettingsLogsFooter().resolve()) {
                    RowLink(S.appSettingsLogsTitle().resolve()) { showLogs = true }
                }
            }
            Spacer(Modifier.height(24.dp))
        }
        Toast(dst.toast?.resolve(), Modifier.align(Alignment.TopCenter))
    }

    if (showLanguage) LanguageView(onClose = { showLanguage = false })
    if (showSharedLinks) SharedLinksView(onClose = { showSharedLinks = false })
    if (showLogs) LogsView(onClose = { showLogs = false })
    if (confirmLogout) {
        AlertDialog(
            onDismissRequest = { confirmLogout = false },
            title = { Text(S.appSettingsConnectionLogOutTitle().resolve()) },
            text = { Text(S.appSettingsConnectionLogOutMessage().resolve()) },
            confirmButton = { TextButton(onClick = { confirmLogout = false; logOut(context, secrets) }) { Text(S.appSettingsConnectionLogOutConfirm().resolve(), color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { confirmLogout = false }) { Text(S.commonCancel().resolve()) } },
        )
    }
    dst.reprocessConfirm?.let { action ->
        val resume = action == DeviceSettingsViewModel.ReprocessConfirm.RESUME
        AlertDialog(
            onDismissRequest = { device.setReprocessConfirm(null) },
            text = { Text((if (resume) S.appSettingsReprocessConfirmResume() else S.appSettingsReprocessConfirmRestart()).resolve()) },
            confirmButton = { TextButton(onClick = { scope.launch { device.startReprocess(forceRestart = !resume) } }) { Text((if (resume) S.appSettingsReprocessResumeButton() else S.appSettingsReprocessRestartButton()).resolve(), color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { device.setReprocessConfirm(null) }) { Text(S.commonCancel().resolve()) } },
        )
    }
}

/** Issue #190: which way the app reaches the device, worded as in the other apps; nothing while not connected. */
private fun routeLine(signedIn: Boolean, route: OTCConnection.Route?, endpoint: String): UiText? {
    if (!signedIn || route == null) return null
    if (route == OTCConnection.Route.HOME) return S.appShellRouteHome()
    val host = try { java.net.URI(SecretsStore.normalizedEndpoint(endpoint)).host } catch (e: Exception) { null }
    if (host.isNullOrEmpty()) return S.appShellRouteConfigured()
    // Host names aren't case-sensitive; "Cala.Off-The.Cloud" is the bridge too, as on macOS.
    val h = host.lowercase()
    val bridge = SecretsStore.bridgeDomain
    return S.appShellRouteThrough(if (h == bridge || h.endsWith(".$bridge")) bridge else host)
}

@Composable
private fun PasswordField(value: String, placeholder: String, onChange: (String) -> Unit) {
    OTCTextField(
        value = value, onValueChange = onChange, placeholder = { Text(placeholder) }, singleLine = true,
        visualTransformation = PasswordVisualTransformation(), keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
        modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp),
    )
}

/**
 * This phone's thumbnail cache (ThumbStore): its maximum size (250 MB to
 * 5 GB, 1 GB unless changed - kept on this phone until Log Out), what it
 * holds, and Clear (the tiles in memory go too). The strings are the iOS
 * app's.
 */
@Composable
private fun ThumbnailCacheSection() {
    val usage by ThumbStore.usage.collectAsState()
    val scope = rememberCoroutineScope()
    var menu by remember { mutableStateOf(false) }
    var clearing by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) { ThumbStore.refreshUsage() }
    val limitText = formatCacheBytes(usage.limit).resolve()
    // Resolved here: the semantics lambda below isn't composable.
    val changeSize = S.androidSettingsThumbsChangeSize().resolve()
    val rowLabel = S.androidSettingsThumbsMaxSizeA11y(limitText).resolve()
    val selected = S.androidSettingsThumbsSelected().resolve()
    Section(S.androidSettingsThumbsTitle().resolve(), S.appSettingsThumbsFooter().resolve()) {
        Row(
            Modifier.fillMaxWidth().clip(RoundedCornerShape(8.dp))
                .clickable(onClickLabel = changeSize, role = Role.DropdownList) { menu = true }
                .semantics { contentDescription = rowLabel }
                .padding(vertical = 8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(S.appSettingsThumbsMaxSize().resolve(), Modifier.weight(1f))
            Box {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(limitText, color = MaterialTheme.colorScheme.primary)
                    Icon(Icons.Filled.ArrowDropDown, null, tint = MaterialTheme.colorScheme.primary)
                }
                DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                    for (choice in THUMB_CACHE_CHOICES) {
                        DropdownMenuItem(
                            text = { Text(formatCacheBytes(choice).resolve()) },
                            trailingIcon = if (choice == usage.limit) ({ Icon(Icons.Filled.Check, selected) }) else null,
                            onClick = { menu = false; scope.launch { ThumbStore.setLimit(choice) } },
                        )
                    }
                }
            }
        }
        Caption(S.appShellThumbCacheUsage(formatCacheBytes(usage.used).resolve(), limitText).resolve())
        RowButton(S.appSettingsThumbsClear().resolve(), enabled = !clearing) {
            clearing = true
            scope.launch { try { ThumbStore.clearAll() } finally { clearing = false } }
        }
    }
}

@Composable
private fun ToggleRow(label: String, checked: Boolean, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        Text(label, Modifier.weight(1f))
        Switch(checked = checked, onCheckedChange = onChange)
    }
}

/**
 * Log Out (the same as SettingsView.swift's logOut): stop everything that
 * talks to the device, forget what it told us, wipe what this phone stores,
 * then start the app over so onboarding comes up on a clean slate - the
 * activity-scoped view models (gallery, files, settings) are the one thing
 * a wipe of the stores can't reach.
 */

@Composable
private fun ProfileEditorSection() {
    val vm: ProfileEditorViewModel = viewModel()
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    // A pick goes through the circle crop (ProfilePhotoCrop.kt) before it
    // replaces the photo; Cancel keeps the previous one.
    var cropSource by remember { mutableStateOf<android.graphics.Bitmap?>(null) }
    val picker = rememberLauncherForActivityResult(ActivityResultContracts.PickVisualMedia()) { uri ->
        if (uri != null) scope.launch { cropSource = withContext(Dispatchers.IO) { decodeProfilePhoto(context, uri) } }
    }
    // Issue #178: "Change photo" offers this phone's picker or the photos on
    // the device (DevicePhotoPicker.kt); both land in the same crop.
    var photoMenu by remember { mutableStateOf(false) }
    var devicePicker by remember { mutableStateOf(false) }
    if (devicePicker) DevicePhotoPicker(onCancel = { devicePicker = false }) { bmp -> devicePicker = false; cropSource = bmp }
    cropSource?.let { bmp ->
        ProfilePhotoCropDialog(bmp, onCancel = { cropSource = null }) { side, zoom, pan ->
            scope.launch {
                vm.setImage(withContext(Dispatchers.Default) { renderProfilePhoto(bmp, side, zoom, pan) })
                cropSource = null
            }
        }
    }
    LaunchedEffect(Unit) { vm.load() }
    Section(S.appSettingsProfileTitle().resolve()) {
        Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.CenterHorizontally) {
            AvatarView(data = st.imageData, size = 96.dp)
            Box {
                TextButton(onClick = { photoMenu = true }) { Text(S.appSettingsProfileChangePhoto().resolve()) }
                DropdownMenu(expanded = photoMenu, onDismissRequest = { photoMenu = false }) {
                    DropdownMenuItem(text = { Text(S.androidSettingsProfileFromPhone().resolve()) }, onClick = {
                        photoMenu = false
                        picker.launch(PickVisualMediaRequest(ActivityResultContracts.PickVisualMedia.ImageOnly))
                    })
                    DropdownMenuItem(text = { Text(S.appSettingsProfileFromDevice().resolve()) }, onClick = { photoMenu = false; devicePicker = true })
                }
            }
        }
        OTCTextField(value = st.name, onValueChange = vm::setName, placeholder = { Text(S.appSettingsProfileName().resolve()) }, singleLine = true, modifier = Modifier.fillMaxWidth())
        OTCTextField(value = st.bio, onValueChange = vm::setBio, placeholder = { Text(S.androidSettingsProfileDescription().resolve()) }, minLines = 3, modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            RowButton(S.appSettingsProfileSave().resolve(), enabled = !st.saving) { scope.launch { vm.save() } }
            if (st.saving) CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp)
        }
        st.toast?.let { Caption(it.resolve()) }
    }
}

private fun raidStateLabel(s: Status): UiText = when (s.raidState) {
    RaidState.RaidNone -> S.appSettingsStatusRaidNone()
    RaidState.RaidInSync -> S.appSettingsStatusRaidInSync()
    RaidState.RaidSyncing -> S.appSettingsStatusRaidSyncing(s.raidSyncPercent.toInt())
    RaidState.RaidDegraded -> S.appSettingsStatusRaidDegraded()
    else -> S.appSettingsStatusRaidUnknown()
}

@Composable
private fun StatusSectionContent(vm: StatusViewModel) {
    val st by vm.state.collectAsState()
    val s = st.status
    when {
        s != null -> {
            val usedPct = if (s.raidSize > 0) s.raidUsage.toDouble() / s.raidSize * 100 else 0.0
            val tint = if (usedPct >= 90) Color(0xFFE53935) else if (usedPct >= 70) Color(0xFFFFC107) else Color(0xFF43A047)
            RaidUsageBar(usedPct, tint)
            Caption(S.appSettingsStatusRaidUsed(sizeText(s.raidUsage.toDouble()), sizeText(s.raidSize.toDouble())).resolve())
            Caption(S.appSettingsStatusDisk(sizeText(s.diskUsage.toDouble()), sizeText(s.diskSize.toDouble())).resolve())
            Caption(S.appSettingsStatusCpuMem(decimalText("%.1f", s.cpuUsagePrc.toDouble()), sizeText(s.memUsage.toDouble()), sizeText(s.memSize.toDouble())).resolve())
            val state = raidStateLabel(s).resolve()
            Caption((if (s.raidLevel.isEmpty()) S.appSettingsStatusRaid(state, s.disks) else S.appSettingsStatusRaidLevel(s.raidLevel, state, s.disks)).resolve())
            // The device's own words (in the request's language) after a sign.
            s.errorsList.forEach { Caption("⚠️ ${it.message}", Color(0xFFE53935)) } // i18n-ignore: a sign before the device's own words
        }
        st.errorText != null -> Caption(st.errorText!!.resolve(), Color(0xFFE53935))
        else -> CircularProgressIndicator()
    }
}

@Composable
private fun RaidUsageBar(percent: Double, tint: Color) {
    val clamped = percent.coerceIn(0.0, 100.0)
    Box(Modifier.fillMaxWidth().height(14.dp).clip(RoundedCornerShape(7.dp)).background(MaterialTheme.colorScheme.surfaceVariant)) {
        Box(Modifier.fillMaxWidth(clamped.toFloat() / 100f).height(14.dp).background(tint, RoundedCornerShape(7.dp)))
        // A line as tall as the bar: the inherited 24sp line cut the digits' lower half.
        Text(S.appSettingsStatusUsedPercent(clamped.toInt()).resolve(), fontSize = 9.sp, lineHeight = 12.sp, color = Color.White, modifier = Modifier.align(Alignment.CenterEnd).padding(end = 3.dp).background(Color(0x59000000), RoundedCornerShape(4.dp)).padding(horizontal = 4.dp))
    }
}

@Composable
private fun UpdateSection() {
    val vm: UpdateViewModel = viewModel()
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    var confirmRestart by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) { vm.load() }
    if (!st.isPrimary) return
    Section(S.appSettingsUpdateTitle().resolve()) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text((if (st.currentVersion > 0) st.installedText else S.androidSettingsUpdateVersionUnknown()).resolve())
            if (st.hasUpdate) { Text(" → ${st.latestText.resolve()}", fontWeight = FontWeight.Bold); KindBadge(st.latestKind) } // i18n-ignore: an arrow before the newest version, no words
            Spacer(Modifier.weight(1f))
            if (!st.hasUpdate && !st.running && st.checkError.isEmpty()) Caption(S.appSettingsUpdateUpToDate().resolve())
        }
        st.pending.forEach { r ->
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(r.label.ifEmpty { S.appSettingsUpdateBuild("${r.version}").resolve() }, fontWeight = FontWeight.Bold, style = MaterialTheme.typography.bodySmall)
                KindBadge(r.kind)
                Spacer(Modifier.width(6.dp))
                Caption(r.summary)
            }
        }
        val message = st.statusMessage.resolve()
        if (st.running) Row(verticalAlignment = Alignment.CenterVertically) { CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp); Spacer(Modifier.width(8.dp)); Caption(message.ifEmpty { S.commonUpdating().resolve() }) }
        if (st.state == "failed") Caption((if (st.lastUpdated.isEmpty()) S.appSettingsUpdateFailed(message) else S.appSettingsUpdateFailedOn(st.lastUpdated, message)).resolve(), Color(0xFFE53935))
        if (st.checkError.isNotEmpty()) Caption(S.appSettingsUpdateCheckError().resolve())
        RowButton((if (st.checking) S.appSettingsUpdateChecking() else S.appSettingsUpdateCheckAgain()).resolve(), enabled = !st.checking && !st.running) { scope.launch { vm.check() } }
        if (st.hasUpdate) RowButton((if (st.starting) S.appSettingsUpdateStarting() else st.updateToText).resolve(), enabled = !st.starting && !st.running) { scope.launch { vm.apply() } }
        if (st.hasUpdate || st.running) Caption(S.appSettingsUpdateRestartNote().resolve())
        st.error?.let { Caption(it.resolve(), Color(0xFFE53935)) }
        // Settings > Restart device: the whole machine, owner of the primary only.
        RowButton((if (st.restarting) S.appSettingsUpdateRestarting() else S.appSettingsUpdateRestart()).resolve(), enabled = !st.restarting && !st.running) { confirmRestart = true }
        when (st.restartPhase) {
            "restarting" -> Row(verticalAlignment = Alignment.CenterVertically) { CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp); Spacer(Modifier.width(8.dp)); Caption(S.appSettingsUpdateRestartingNote().resolve()) }
            "back" -> Caption(S.appSettingsUpdateBack().resolve())
            "slow" -> Caption(S.appSettingsUpdateSlow().resolve(), Color(0xFFFF9800))
        }
        st.restartError?.let { Caption(it.resolve(), Color(0xFFE53935)) }
    }
    if (confirmRestart) AlertDialog(
        onDismissRequest = { confirmRestart = false },
        title = { Text(S.appSettingsUpdateRestartTitle().resolve()) },
        text = { Text(S.appSettingsUpdateRestartMessage().resolve()) },
        confirmButton = { TextButton(onClick = { confirmRestart = false; vm.viewModelScope.launch { vm.restart() } }) { Text(S.appSettingsUpdateRestartConfirm().resolve(), color = Color(0xFFE53935)) } },
        dismissButton = { TextButton(onClick = { confirmRestart = false }) { Text(S.commonCancel().resolve()) } },
    )
}

/** Issue #183: "Major" (orange) or "Critical" (red) next to a release; nothing for a minor one. */
@Composable
private fun KindBadge(kind: String) {
    val (label, color) = when (kind) {
        "major" -> S.appSettingsUpdateMajor().resolve() to Color(0xFFFF9800)
        "critical" -> S.appSettingsUpdateCritical().resolve() to Color(0xFFE53935)
        else -> return
    }
    Text(label, color = Color.White, fontSize = 10.sp, fontWeight = FontWeight.Bold,
        modifier = Modifier.padding(start = 6.dp).background(color, RoundedCornerShape(4.dp)).padding(horizontal = 5.dp, vertical = 1.dp))
}

@Composable
private fun TailscaleSection() {
    val vm: TailscaleViewModel = viewModel()
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val lifecycle = LocalLifecycleOwner.current
    LaunchedEffect(Unit) { vm.load() }
    // Authorising happens in the browser, so coming back is when the device has news.
    DisposableEffect(lifecycle) {
        val obs = LifecycleEventObserver { _, e -> if (e == Lifecycle.Event.ON_RESUME && vm.state.value.isPrimary) scope.launch { vm.refresh() } }
        lifecycle.lifecycle.addObserver(obs)
        onDispose { lifecycle.lifecycle.removeObserver(obs) }
    }
    if (!st.isPrimary) return
    Section("Tailscale Funnel") {
        if (st.funnelOn) Caption(S.appSettingsTailscaleServed(st.publicUrl).resolve())
        if (st.installed) {
            Caption(S.appSettingsTailscaleIntro().resolve())
            Limitation(S.appSettingsTailscaleLimitSocial())
            Limitation(S.appSettingsTailscaleLimitOneUser())
            Limitation(S.appSettingsTailscaleLimitBandwidth())
            Limitation(S.appSettingsTailscaleLimitAccount())
            Caption(S.appSettingsTailscaleSuits().resolve())
            if (!st.funnelOn) OTCTextField(value = st.authKey, onValueChange = vm::setAuthKey, placeholder = { Text(S.appSettingsTailscaleAuthKey().resolve()) }, singleLine = true,
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false), modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp))
            RowButton((if (st.busy) S.appSettingsTailscaleWorking() else if (st.funnelOn) S.appSettingsTailscaleTurnOff() else S.appSettingsTailscaleEnable()).resolve(), enabled = !st.busy) {
                scope.launch {
                    vm.configure(enable = !st.funnelOn)
                    val login = vm.state.value.loginUrl
                    if (login.isNotEmpty()) Share.openInBrowser(context, login)
                }
            }
            if (!st.funnelOn) Caption(S.appSettingsTailscaleKeyHint().resolve())
        } else {
            Caption(S.appSettingsTailscaleNotInstalled().resolve())
        }
        st.note?.let { Caption(it.resolve()) }
        st.error?.let { Caption(it.resolve(), Color(0xFFE53935)) }
    }
}

/** A limitation of Tailscale Funnel after a bullet, as plain text (its lead is bold on the iPhone). */
@Composable
private fun Limitation(text: RichText) {
    val line = buildAnnotatedString { append("• "); append(text.resolve(emptyMap())) } // i18n-ignore: the list's bullet
    Text(line, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(vertical = 2.dp))
}

/** Issue #182: leaving the bridge and deleting the account. Mirrors iOS's BridgeAccountSection. */
@Composable
private fun BridgeAccountSection(secrets: SecretsStore) {
    val vm: BridgeAccountViewModel = viewModel()
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var confirmLeave by remember { mutableStateOf(false) }
    var confirmDelete by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) { vm.load() }
    if (!st.visible) return
    fun open(path: String) = Share.openInBrowser(context, "https://${st.bridge}/$path")
    Section(S.appSettingsBridgeTitle().resolve(), if (st.enabled) S.appSettingsBridgeFooter(st.localAddress.ifEmpty { "otc.local:8080" }).resolve() else null) {
        if (st.enabled) {
            Caption(S.appSettingsBridgeReachable(st.domain).resolve())
            if (st.localAddress.isNotEmpty()) Caption(S.appSettingsBridgeAtHome(st.localAddress).resolve())
            RowButton(S.appSettingsBridgeLeave().resolve(), enabled = !st.busy, destructive = true) { confirmLeave = true }
            RowButton(S.appSettingsBridgeDeleteAccount().resolve(), enabled = !st.busy, destructive = true) { confirmDelete = true }
        } else {
            if (st.leftReason == "released") Caption(S.appSettingsBridgeReleased(st.bridge).resolve(), Color(0xFFFF9800))
            Caption(S.appSettingsBridgeLocalOnly().resolve())
            RowLink(S.appSettingsBridgeAccountPage().resolve()) { open("account") }
        }
        RowLink(S.appShellPrivacy().resolve()) { open("privacy") }
        st.note?.let { Caption(it.resolve()) }
        st.error?.let { Caption(it.resolve(), Color(0xFFE53935)) }
    }
    if (confirmLeave) AlertDialog(
        onDismissRequest = { confirmLeave = false },
        title = { Text(S.appSettingsBridgeLeaveTitle().resolve()) },
        text = { Text(S.appSettingsBridgeLeaveMessage(st.domain).resolve()) },
        confirmButton = { TextButton(onClick = { confirmLeave = false; scope.launch { vm.leave(secrets) } }) { Text(S.appSettingsBridgeLeaveConfirm().resolve(), color = Color(0xFFE53935)) } },
        dismissButton = { TextButton(onClick = { confirmLeave = false }) { Text(S.commonCancel().resolve()) } },
    )
    if (confirmDelete) AlertDialog(
        onDismissRequest = { confirmDelete = false },
        title = { Text(S.appSettingsBridgeDeleteTitle().resolve()) },
        text = { Text(S.appSettingsBridgeDeleteMessage().resolve()) },
        confirmButton = { TextButton(onClick = { confirmDelete = false; scope.launch { if (vm.leave(secrets)) open("account?delete=1") } }) { Text(S.androidSettingsBridgeLeaveDelete().resolve(), color = Color(0xFFE53935)) } },
        dismissButton = { TextButton(onClick = { confirmDelete = false }) { Text(S.commonCancel().resolve()) } },
    )
}

@Composable
private fun UsersManagementSection() {
    val vm: UsersManagementViewModel = viewModel()
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    LaunchedEffect(Unit) { vm.checkRole() }
    if (!st.isPrimary) return
    val usernameValid = st.newUsername.trim().let { it.isNotEmpty() && Regex("^[a-z0-9-]+$").matches(it) }
    Section(S.appSettingsUsersTitle().resolve()) {
        Caption(S.appSettingsUsersIntro().resolve())
        st.users.forEach { u -> UserRow(vm, st, u) }
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            OTCTextField(value = st.newUsername, onValueChange = vm::setNewUsername, placeholder = { FitText(S.appSettingsUsersUsername().resolve(), minScale = 0.8f) }, singleLine = true,
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false), modifier = Modifier.weight(1f))
            Spacer(Modifier.width(6.dp))
            OTCTextField(value = st.newPort, onValueChange = vm::setNewPort, placeholder = { Text(S.appSettingsUsersPort().resolve()) }, singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number), modifier = Modifier.width(90.dp))
            RowButton(S.commonAdd().resolve(), enabled = !st.creating && usernameValid) { vm.askPassword() }
        }
        // Issue #103, as the web app: the address the new user gets on the bridge.
        ToggleRow(S.appSettingsUsersRequestBridge().resolve(), st.requestBridgeAccess, vm::setRequestBridgeAccess)
        val name = st.newUsername.trim()
        val bridgeDomain = st.users.firstOrNull { it.bridgeAccess && it.subdomain.isNotEmpty() }?.subdomain?.substringAfter('.', "") ?: ""
        Caption(((if (name.isNotEmpty() && bridgeDomain.isNotEmpty()) S.appSettingsUsersReachableAt("$name.$bridgeDomain") else S.appSettingsUsersReachableOwn()).resolve()) + " " + S.appSettingsUsersLocalHint().resolve())
    }
    st.passwordFor?.let { name -> FirstPasswordDialog(vm, st, name) }
    st.toast?.let { AlertDialog(onDismissRequest = { vm.dismissToast() }, text = { Text(it.resolve()) }, confirmButton = { TextButton(onClick = { vm.dismissToast() }) { Text(S.commonOk().resolve()) } }) }
}

/** The new account's first password, asked for when Add is tapped. */
@Composable
private fun FirstPasswordDialog(vm: UsersManagementViewModel, st: UsersManagementViewModel.State, name: String) {
    val scope = rememberCoroutineScope()
    val error = MaterialTheme.colorScheme.error
    AlertDialog(
        onDismissRequest = { vm.cancelPassword() },
        title = { Text(S.appSettingsUsersPasswordTitle(name).resolve()) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OTCTextField(value = st.newPassword, onValueChange = vm::setNewPassword, placeholder = { Text(S.appSettingsUsersPassword().resolve()) }, singleLine = true,
                    visualTransformation = PasswordVisualTransformation(), keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false), modifier = Modifier.fillMaxWidth())
                OTCTextField(value = st.confirmPassword, onValueChange = vm::setConfirmPassword, placeholder = { Text(S.appSettingsUsersPasswordConfirm().resolve()) }, singleLine = true,
                    visualTransformation = PasswordVisualTransformation(), keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false), modifier = Modifier.fillMaxWidth())
                if (st.passwordTooShort) Text(S.appSettingsUsersPasswordTooShort().resolve(), color = error, style = MaterialTheme.typography.bodySmall)
                if (st.passwordMismatch) Text(S.appSettingsUsersPasswordMismatch().resolve(), color = error, style = MaterialTheme.typography.bodySmall)
                st.passwordError?.let { Text(it.resolve(), color = error, style = MaterialTheme.typography.bodySmall) }
                Caption(S.appSettingsUsersPasswordHint(name).resolve())
            }
        },
        confirmButton = {
            TextButton(onClick = { scope.launch { vm.createUser() } }, enabled = st.canCreate && !st.creating) {
                Text(if (st.creating) "…" else S.appSettingsUsersCreate().resolve()) // i18n-ignore: an ellipsis while the user is being added
            }
        },
        dismissButton = { TextButton(onClick = { vm.cancelPassword() }, enabled = !st.creating) { Text(S.commonCancel().resolve()) } },
    )
}

@Composable
private fun UserRow(vm: UsersManagementViewModel, st: UsersManagementViewModel.State, u: User) {
    val scope = rememberCoroutineScope()
    Column(Modifier.fillMaxWidth().padding(vertical = 4.dp)) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                // The space before the label keeps it apart from the name.
                Row { Text(u.username, fontWeight = FontWeight.Bold); if (!u.active) Text(" " + S.appSettingsUsersInactive().resolve(), style = MaterialTheme.typography.labelSmall, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                Caption(S.appSettingsUsersPortDomain("${u.port}", u.subdomain).resolve())
            }
            val m = st.metrics[u.uuid]
            // Half the row at most, right-aligned, wrapping inside it: measured
            // first at its whole width ("2 connessioni attive"), it left the name
            // a sliver and its INACTIVE label broke letter by letter.
            if (m != null) Box(Modifier.weight(1f), contentAlignment = Alignment.CenterEnd) {
                Text(S.appSettingsUsersUsage("${m.storageMb.toInt()}", decimalText("%.1f", m.storagePct), m.activeConnections).resolve(),
                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.End,
                    modifier = Modifier.padding(vertical = 2.dp))
            }
            else RowButton(S.appSettingsUsersLoadUsage().resolve()) { scope.launch { vm.fetchMetrics(u.uuid) } }
        }
        Row(Modifier.fillMaxWidth()) {
            RowButton((if (u.active) S.appSettingsUsersDisable() else S.appSettingsUsersEnable()).resolve(), enabled = st.busyActiveUuid != u.uuid) { scope.launch { vm.toggleActive(u) } }
            Spacer(Modifier.weight(1f))
            RowButton(S.commonDelete().resolve(), destructive = true) { vm.toggleDeleteTarget(u.uuid) }
        }
        if (st.deleteTargetUuid == u.uuid) {
            Caption(S.appSettingsUsersConfirmHint(u.username).resolve())
            OTCTextField(value = st.deleteConfirmText, onValueChange = vm::setDeleteConfirmText, placeholder = { Text(u.username) }, singleLine = true,
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false), modifier = Modifier.fillMaxWidth())
            RowButton((if (st.deleting) S.commonDeleting() else S.appSettingsUsersConfirmDelete()).resolve(), enabled = !st.deleting && st.deleteConfirmText == u.username, destructive = true) { scope.launch { vm.confirmDelete(u) } }
        }
        HorizontalDivider(Modifier.padding(top = 4.dp))
    }
}


/**
 * A size the device reports in MB, readable: "4.6 GB", "984 GB", "820 MB"
 * ("4,6 GB" in Spanish: decimalText). Same as iOS's sizeText.
 */
internal fun sizeText(mb: Double): String = when {
    mb >= 100_000 -> decimalText("%.0f GB", mb / 1000) // i18n-ignore: a size, the unit's symbol in every language
    mb >= 1000 -> decimalText("%.1f GB", mb / 1000) // i18n-ignore: a size, the unit's symbol in every language
    else -> decimalText("%.0f MB", mb) // i18n-ignore: a size, the unit's symbol in every language
}
