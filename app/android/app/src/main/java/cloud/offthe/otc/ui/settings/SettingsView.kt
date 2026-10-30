// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import androidx.compose.ui.text.buildAnnotatedString
import android.text.InputType
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.inputmethod.EditorInfo
import android.widget.EditText
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.selection.selectable
import androidx.compose.material.icons.filled.Lock
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.widget.doAfterTextChanged
import cloud.offthe.otc.proto.WifiNetwork
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
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
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
import androidx.compose.foundation.layout.size
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
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.awaitCancellation
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.data.SmartSearchAnnouncement
import cloud.offthe.otc.data.SmartSearchModelState
import cloud.offthe.otc.data.SmartSearchSupport
import cloud.offthe.otc.data.UploadModel
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.RaidState
import cloud.offthe.otc.proto.Status
import cloud.offthe.otc.proto.User
import cloud.offthe.otc.sync.PhotoFolders
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.ui.AvatarView
import cloud.offthe.otc.ui.common.ConnectionEndpointFields
import cloud.offthe.otc.ui.common.EndpointEntry
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.Toast
import cloud.offthe.otc.ui.compose.mediaPermissions
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.compose.runtime.mutableIntStateOf
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
import cloud.offthe.otc.i18n.androidSettingsFolders
import cloud.offthe.otc.i18n.androidSettingsLogsFooter
import cloud.offthe.otc.i18n.androidSettingsProfileDescription
import cloud.offthe.otc.i18n.androidSettingsProfileFromPhone
import cloud.offthe.otc.i18n.androidSettingsSharingFooter
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
import cloud.offthe.otc.i18n.appSettingsPlacesFooter
import cloud.offthe.otc.i18n.appSettingsPlacesTitle
import cloud.offthe.otc.i18n.appSettingsMemoriesFooter
import cloud.offthe.otc.i18n.appSettingsSmartSearchBeta
import cloud.offthe.otc.i18n.appSettingsSmartSearchFooter
import cloud.offthe.otc.i18n.appSettingsSmartSearchOffConfirm
import cloud.offthe.otc.i18n.appSettingsSmartSearchOffText
import cloud.offthe.otc.i18n.appSettingsSmartSearchOffTitle
import cloud.offthe.otc.i18n.appSettingsSmartSearchTitle
import cloud.offthe.otc.i18n.appSettingsMemoriesShowHidden
import cloud.offthe.otc.i18n.appSettingsMemoriesTitle
import cloud.offthe.otc.i18n.appSettingsMemoriesToggle
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
import cloud.offthe.otc.i18n.appSettingsWifiChange
import cloud.offthe.otc.i18n.appSettingsWifiConfirmCableMessage
import cloud.offthe.otc.i18n.appSettingsWifiConfirmMessage
import cloud.offthe.otc.i18n.appSettingsWifiConfirmTitle
import cloud.offthe.otc.i18n.appSettingsWifiCurrentBadge
import cloud.offthe.otc.i18n.appSettingsWifiIntro
import cloud.offthe.otc.i18n.appSettingsWifiName
import cloud.offthe.otc.i18n.appSettingsWifiNoNetworks
import cloud.offthe.otc.i18n.appSettingsWifiNoWifi
import cloud.offthe.otc.i18n.appSettingsWifiOnCable
import cloud.offthe.otc.i18n.appSettingsWifiOnWifi
import cloud.offthe.otc.i18n.appSettingsWifiOpenNetwork
import cloud.offthe.otc.i18n.appSettingsWifiOther
import cloud.offthe.otc.i18n.appSettingsWifiPasswordOptional
import cloud.offthe.otc.i18n.appSettingsWifiScan
import cloud.offthe.otc.i18n.appSettingsWifiScanning
import cloud.offthe.otc.i18n.appSettingsWifiSecured
import cloud.offthe.otc.i18n.appSettingsWifiSlow
import cloud.offthe.otc.i18n.appSettingsWifiTitle
import cloud.offthe.otc.i18n.appSettingsWifiUnsupported
import cloud.offthe.otc.i18n.appShellPrivacy
import cloud.offthe.otc.i18n.appShellRouteConfigured
import cloud.offthe.otc.i18n.appShellRouteHome
import cloud.offthe.otc.i18n.appShellRouteThrough
import cloud.offthe.otc.i18n.appShellThumbCacheUsage
import cloud.offthe.otc.i18n.appTourSettingsFooter
import cloud.offthe.otc.i18n.appTourSettingsTitle
import cloud.offthe.otc.i18n.appTourShow
import cloud.offthe.otc.ui.TourModel
import cloud.offthe.otc.ui.TourRules
import cloud.offthe.otc.ui.TourTarget
import cloud.offthe.otc.ui.TourTargets
import cloud.offthe.otc.ui.MENU_RAIL_MIN_DP
import cloud.offthe.otc.ui.tourTarget
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.boundsInWindow
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import cloud.offthe.otc.net.retryWaitMs
import cloud.offthe.otc.net.sleepOrWake
import cloud.offthe.otc.i18n.commonAdd
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonConnect
import cloud.offthe.otc.i18n.commonConnecting
import cloud.offthe.otc.i18n.commonDelete
import cloud.offthe.otc.i18n.commonDeleting
import cloud.offthe.otc.i18n.commonLoading
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
private fun Section(title: String, footer: String? = null, below: (@Composable () -> Unit)? = null, content: @Composable () -> Unit) {
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp)) {
        Text(title.uppercase(), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(bottom = 6.dp))
        Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).background(MaterialTheme.colorScheme.surfaceContainer).padding(12.dp)) { content() }
        footer?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 6.dp)) }
        below?.invoke()
    }
}

/**
 * A section the device hasn't answered for yet (Users, Device version,
 * Wi-Fi, Tailscale, the bridge, Logs): its header and a spinner, so it is
 * there from the start, and gone only once the device says it has none (an
 * additional user's account, an older device). As iOS's LoadingSection.
 */
@Composable
private fun LoadingSection(title: String) {
    Section(title) {
        Row(Modifier.fillMaxWidth().padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp)
            Spacer(Modifier.width(8.dp))
            Caption(S.commonLoading().resolve())
        }
    }
}

/**
 * A section's questions to the device: asked as Settings shows it (unless
 * the section asks for itself, [askFirst] false), and while the device
 * hasn't answered, again 5 s later doubling to a minute - at once at a wake
 * (signed in again, a network up, the app back in front) - never while the
 * app is in the background.
 */
@Composable
private fun AskDevice(answered: Boolean, askFirst: Boolean = true, ask: suspend () -> Unit) {
    val unanswered by rememberUpdatedState(!answered)
    val latest by rememberUpdatedState(ask)
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(lifecycle) {
        if (askFirst) latest()
        var failures = 0
        while (unanswered) {
            sleepOrWake(retryWaitMs(++failures, firstMs = 5_000, maxMs = 60_000))
            lifecycle.currentStateFlow.first { it.isAtLeast(Lifecycle.State.STARTED) }
            if (unanswered) latest()
        }
    }
}

/** Settings > Logs, the primary instance only. */
@Composable
private fun LogsSection(onOpen: () -> Unit) {
    val vm: LogsSectionViewModel = viewModel()
    val st by vm.state.collectAsState()
    AskDevice(st.answered) { vm.load() }
    if (!st.answered) { LoadingSection(S.appSettingsLogsTitle().resolve()); return }
    if (!st.isPrimary) return
    Section(S.appSettingsLogsTitle().resolve(), S.androidSettingsLogsFooter().resolve()) {
        RowLink(S.appSettingsLogsTitle().resolve()) { onOpen() }
    }
}

@Composable
internal fun Caption(text: String, color: Color = MaterialTheme.colorScheme.onSurfaceVariant) =
    Text(text, style = MaterialTheme.typography.bodySmall, color = color, modifier = Modifier.padding(vertical = 2.dp))

// An action: a filled button, red for what removes or signs out.
@Composable
internal fun RowButton(label: String, enabled: Boolean = true, destructive: Boolean = false, onClick: () -> Unit) {
    val colors = if (destructive) ButtonDefaults.filledTonalButtonColors(containerColor = MaterialTheme.colorScheme.errorContainer, contentColor = MaterialTheme.colorScheme.onErrorContainer)
        else ButtonDefaults.filledTonalButtonColors()
    FilledTonalButton(onClick = onClick, enabled = enabled, colors = colors, modifier = Modifier.padding(vertical = 2.dp)) { Text(label) }
}

// What opens another screen or a web page stays a link.
@Composable
private fun RowLink(label: String, modifier: Modifier = Modifier, onClick: () -> Unit) {
    TextButton(onClick = onClick, modifier = modifier) { Text(label, color = MaterialTheme.colorScheme.primary) }
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
    val signedIn by OTCConnection.authenticated.collectAsState()
    val route by OTCConnection.route.collectAsState()
    // Authorize Photos Access shows only while the app can't read the
    // photos. The first tap asks; after a refusal Android shows no dialog
    // again, so the next one opens the app's page in system settings.
    var photosAllowed by remember { mutableStateOf(PhotoSync.hasPermission(context)) }
    // Android 14's limited access: only the photos picked are backed up.
    var photosLimited by remember { mutableStateOf(PhotoSync.accessLimited(context)) }
    var photosAsked by remember { mutableStateOf(false) }
    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        photosAllowed = PhotoSync.hasPermission(context)
        photosLimited = PhotoSync.accessLimited(context)
        photosAsked = true
    }
    // Bumped when the app comes back (photos taken or folders moved
    // meanwhile), not by the resume that comes as the screen opens.
    var resumes by remember { mutableIntStateOf(0) }
    val lifecycle = LocalLifecycleOwner.current
    DisposableEffect(lifecycle) {
        var opened = false
        val obs = LifecycleEventObserver { _, e ->
            if (e == Lifecycle.Event.ON_RESUME) {
                photosAllowed = PhotoSync.hasPermission(context)
                photosLimited = PhotoSync.accessLimited(context)
                if (opened) resumes++ else opened = true
            }
        }
        lifecycle.lifecycle.addObserver(obs)
        onDispose { lifecycle.lifecycle.removeObserver(obs) }
    }
    // Settings > Sync Options > Folders (FoldersView.kt): saveable, like
    // Language, so the screen stays open through a change of language.
    var showFolders by rememberSaveable { mutableStateOf(false) }
    val skippedFolders by secrets.skippedFolders.collectAsState()
    // The Folders row's value: nothing until it is known. Nothing turned off
    // is All without looking.
    var foldersValue by remember { mutableStateOf<UiText?>(null) }
    LaunchedEffect(photosAllowed, includeVideos, skippedFolders, resumes) {
        if (!photosAllowed) return@LaunchedEffect
        foldersValue = if (skippedFolders.isEmpty()) PhotoFolders.rowValue(emptyList(), skippedFolders)
            else try { withContext(Dispatchers.IO) { PhotoFolders.rowValue(PhotoFolders.list(includeVideos), skippedFolders) } }
            catch (e: CancellationException) { throw e } catch (e: Exception) { null }
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

    LaunchedEffect(Unit) {
        device.loadSettings()
        device.loadReprocessStatus()
        if (device.state.value.reprocessStatus == "running") device.pollReprocessStatusWhileRunning()
    }
    // A8: the status poll only while the app is in front (STARTED), as the
    // wide layout's menu: it used to ask every 5 s with the Activity stopped.
    LaunchedEffect(lifecycle) {
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            status.start()
            try { awaitCancellation() } finally { status.stop() }
        }
    }
    DisposableEffect(Unit) { onDispose { status.stop(); device.stopPollingReprocessStatus() } }
    // Smart search's model on its way (release 152): asked again every 3 s
    // while the switch is on and Settings is in front - not once it is
    // there, with the switch off, Settings left or the app in the background.
    val smartModel by SmartSearchSupport.model.collectAsState()
    val smartPolls = SmartSearchSupport.polls(dst.smartSearchEnabled, smartModel)
    LaunchedEffect(smartPolls, lifecycle) {
        if (!smartPolls) return@LaunchedEffect
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            while (true) {
                delay(SmartSearchSupport.POLL_EVERY_MS)
                device.refreshSmartSearch()
            }
        }
    }

    // The first-run tour's steps about Settings scroll to what they ring,
    // where the card leaves it in sight (TourRules.settingsScroll).
    val scroll = rememberScrollState()
    var viewport by remember { mutableStateOf(androidx.compose.ui.geometry.Rect.Zero) }
    val tourShown by TourModel.shown.collectAsState()
    val tourStep by TourModel.step.collectAsState()
    val tourWide = LocalConfiguration.current.screenWidthDp >= MENU_RAIL_MIN_DP
    LaunchedEffect(tourShown, tourStep, tourWide) {
        if (!tourShown) return@LaunchedEffect
        val (target, at) = TourRules.settingsScroll(tourStep, tourWide) ?: return@LaunchedEffect
        // Laid out first (Users only once the role is known), and again as
        // the sections above it come in (Status, Users).
        var rect = TourTargets.rects(target).firstOrNull()
        repeat(20) { if (rect == null || viewport.height <= 0f) { delay(100); rect = TourTargets.rects(target).firstOrNull() } }
        for (wait in longArrayOf(150, 500, 800)) {
            delay(wait)
            val r = TourTargets.rects(target).firstOrNull() ?: return@LaunchedEffect
            val delta = r.top - viewport.top - at * viewport.height
            if (kotlin.math.abs(delta) > 4f) scroll.animateScrollTo((scroll.value + delta).toInt().coerceIn(0, scroll.maxValue))
        }
    }

    Box(Modifier.fillMaxSize()) {
        Column(Modifier.fillMaxSize().onGloballyPositioned { viewport = it.boundsInWindow() }.verticalScroll(scroll).padding(vertical = 8.dp)) {
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

            // The first-run tour again, from its first page (Tour.kt).
            Section(S.appTourSettingsTitle().resolve(), S.appTourSettingsFooter().resolve()) {
                RowLink(S.appTourShow().resolve(), Modifier.tourTarget(TourTarget.SHOW_TOUR)) { TourModel.replay() }
            }

            Section(S.appSettingsStatusTitle().resolve()) { StatusSectionContent(status) }

            // The device's accounts, right under its status.
            UsersManagementSection()

            // Also while a run waits: for Wi-Fi, or room on the device.
            val halt = upload.halt
            if (halt != null && !upload.isUploading) {
                Section(S.appSettingsUploadsTitle().resolve()) {
                    Text(halt.title.resolve(), style = MaterialTheme.typography.bodyMedium)
                    halt.detail?.let { Caption(it) }
                }
            } else if (upload.totalPending > 0 || upload.isUploading) {
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
                val canChange = !dst.savingKey && dst.oldKey.isNotEmpty() && dst.newKey.isNotEmpty()
                val change = { if (canChange) scope.launch { device.changePassword(secrets) } }
                val newFocus = remember { FocusRequester() }
                val confirmFocus = remember { FocusRequester() }
                SettingsPasswordField(dst.oldKey, S.appSettingsPasswordCurrent().resolve(), device::setOldKey, ImeAction.Next, { newFocus.requestFocus() })
                SettingsPasswordField(dst.newKey, S.appSettingsPasswordNew().resolve(), device::setNewKey, ImeAction.Next, { confirmFocus.requestFocus() }, Modifier.focusRequester(newFocus))
                SettingsPasswordField(dst.confirmKey, S.appSettingsPasswordConfirm().resolve(), device::setConfirmKey, ImeAction.Go, { change() }, Modifier.focusRequester(confirmFocus))
                RowButton((if (dst.savingKey) S.appSettingsPasswordChanging() else S.appSettingsPasswordChange()).resolve(), enabled = canChange) { change() }
            }

            Section(S.appSettingsSyncTitle().resolve()) {
                ToggleRow(S.appSettingsSyncWifiOnly().resolve(), wifiOnly, secrets::setWifiOnly, Modifier.tourTarget(TourTarget.WIFI_ONLY))
                ToggleRow(S.appSettingsSyncVideos().resolve(), includeVideos, secrets::setIncludeVideos)
                if (photosAllowed) {
                    Row(
                        Modifier.fillMaxWidth().clickable(role = Role.Button) { showFolders = true }.padding(vertical = 6.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Text(S.androidSettingsFolders().resolve(), Modifier.weight(1f))
                        foldersValue?.let { Text(it.resolve(), color = MaterialTheme.colorScheme.onSurfaceVariant) }
                        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, contentDescription = null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                if (photosLimited) LimitedAccessNotice {
                    photosAllowed = PhotoSync.hasPermission(context)
                    photosLimited = PhotoSync.accessLimited(context)
                }
                if (!photosAllowed) RowButton(S.appSettingsSyncAuthorize().resolve()) {
                    if (!photosAsked) permission.launch(mediaPermissions())
                    else context.startActivity(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", context.packageName, null)))
                }
            }

            Section(S.androidSettingsSyncTitle().resolve(), S.appSettingsSyncFooter().resolve()) {
                // A run going goes through once more from the new starting
                // point (setWatermark), as on iOS.
                RowButton(S.appSettingsSyncAll().resolve()) { PhotoSync.setWatermark(0); PhotoSync.runForegroundAsync() }
                RowButton(S.appSettingsSyncFromNow().resolve()) { PhotoSync.setWatermark(System.currentTimeMillis()) }
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

            // Places, the map in Images: only on a device that has it, off
            // unless turned on (the footer says the positions are kept
            // unencrypted, and that turning it off deletes them).
            dst.placesEnabled?.let { on ->
                Section(S.appSettingsPlacesTitle().resolve(), S.appSettingsPlacesFooter().resolve()) {
                    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                        Text(S.appSettingsPlacesTitle().resolve(), Modifier.weight(1f))
                        Switch(checked = on, enabled = !dst.savingPlaces, onCheckedChange = { v -> scope.launch { device.togglePlaces(v) } })
                    }
                }
            }

            // On this day (the memories at the top of Images): only on a
            // device that has it, on unless turned off; Show hidden memories
            // again while something is hidden.
            dst.memoriesEnabled?.let { on ->
                Section(S.appSettingsMemoriesTitle().resolve(), S.appSettingsMemoriesFooter().resolve()) {
                    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                        Text(S.appSettingsMemoriesToggle().resolve(), Modifier.weight(1f))
                        Switch(checked = on, enabled = !dst.savingMemories, onCheckedChange = { v -> scope.launch { device.toggleMemories(v) } })
                    }
                    if (on && dst.memoriesHidden > 0) {
                        TextButton(onClick = { scope.launch { device.showHiddenMemories() } }, enabled = !dst.showingHidden) {
                            Text(S.appSettingsMemoriesShowHidden(dst.memoriesHidden).resolve())
                        }
                    }
                }
            }

            // Smart search (beta): only on a device that has it, off unless
            // turned on; turning it off asks first (what the device learned
            // is deleted). Under it, where its model is (release 152).
            dst.smartSearchEnabled?.let { on ->
                SmartSearchSection(
                    on, saving = dst.savingSmartSearch, confirmOff = dst.confirmSmartOff, model = smartModel,
                    onToggle = { v -> device.askSmartSearch(v) { work -> scope.launch { work() } } },
                    onConfirmOff = { scope.launch { device.setSmartSearch(false) } }, onCancelOff = { device.cancelSmartOff() },
                )
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
                val passwordFocus = remember { FocusRequester() }
                // Never an empty address or password: that would be Log Out
                // without the wipe.
                val canSave = editEntry.endpoint.trim().isNotEmpty() && editPassword.isNotEmpty()
                val save = {
                    if (canSave) {
                        secrets.saveCredentials(editEntry.endpoint.trim(), editPassword)
                        OTCConnection.invalidate()
                    }
                }
                ConnectionEndpointFields(editEntry, onNext = { passwordFocus.requestFocus() })
                cloud.offthe.otc.ui.common.PasswordField(
                    value = editPassword, onValueChange = { editPassword = it }, hint = S.commonPassword().resolve(), onAction = save,
                    modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp).focusRequester(passwordFocus),
                )
                routeLine(signedIn, route, endpoint)?.let { Caption(it.resolve()) }
                Caption(S.appSettingsConnectionDeviceId(deviceId).resolve())
                RowButton(S.appSettingsConnectionSave().resolve(), enabled = canSave) { save() }
                RowButton(S.appSettingsConnectionLogOut().resolve(), destructive = true) { confirmLogout = true }
            }

            // The device's own Wi-Fi (owner of the primary instance, a device that manages it).
            WifiSection()

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

            LogsSection { showLogs = true }
            Spacer(Modifier.height(24.dp))
        }
        Toast(dst.toast?.resolve(), Modifier.align(Alignment.TopCenter))
    }

    if (showLanguage) LanguageView(onClose = { showLanguage = false })
    if (showFolders) FoldersView(secrets, onClose = { showFolders = false })
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

/** Change Password's fields: the app's PasswordField, Next to the following one and Go to change it. */
@Composable
private fun SettingsPasswordField(value: String, placeholder: String, onChange: (String) -> Unit, ime: ImeAction, onDone: () -> Unit, modifier: Modifier = Modifier) {
    cloud.offthe.otc.ui.common.PasswordField(
        value = value, onValueChange = onChange, hint = placeholder, imeAction = ime, onAction = onDone,
        modifier = modifier.fillMaxWidth().padding(vertical = 4.dp),
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
private fun ToggleRow(label: String, checked: Boolean, onChange: (Boolean) -> Unit, modifier: Modifier = Modifier) {
    Row(modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
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
    AskDevice(st.answered) { vm.load() }
    if (!st.answered) { LoadingSection(S.appSettingsUpdateTitle().resolve()); return }
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
    AskDevice(st.answered) { vm.load() }
    // Authorising happens in the browser, so coming back is when the device has news.
    DisposableEffect(lifecycle) {
        val obs = LifecycleEventObserver { _, e -> if (e == Lifecycle.Event.ON_RESUME && vm.state.value.isPrimary) scope.launch { vm.refresh() } }
        lifecycle.lifecycle.addObserver(obs)
        onDispose { lifecycle.lifecycle.removeObserver(obs) }
    }
    if (!st.answered) { LoadingSection(TAILSCALE_FUNNEL); return }
    if (!st.isPrimary) return
    Section(TAILSCALE_FUNNEL) {
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

/** The Tailscale section's header: the product's name, in every language. */
private const val TAILSCALE_FUNNEL = "Tailscale Funnel" // i18n-ignore: a product name

/** A limitation of Tailscale Funnel after a bullet, as plain text (its lead is bold on the iPhone). */
@Composable
private fun Limitation(text: RichText) {
    val line = buildAnnotatedString { append("• "); append(text.resolve(emptyMap())) } // i18n-ignore: the list's bullet
    Text(line, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(vertical = 2.dp))
}

/**
 * Settings › Wi-Fi (WifiViewModel): the network the device is on now and
 * Change Network - the networks it sees, Other Network… for one that hides
 * its name, its password where it needs one, a confirmation, then the
 * wait while the device switches. Mirrors iOS's WifiSection.
 */
@Composable
private fun WifiSection() {
    val vm: WifiViewModel = viewModel()
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    val lifecycle = LocalLifecycleOwner.current
    // Asked as it opens and whenever the app comes back (a change may have
    // ended meanwhile); no looks while the app is in the background.
    DisposableEffect(lifecycle) {
        val obs = LifecycleEventObserver { _, e ->
            when (e) {
                Lifecycle.Event.ON_RESUME -> scope.launch { vm.load() }
                Lifecycle.Event.ON_STOP -> vm.pause()
                else -> {}
            }
        }
        lifecycle.lifecycle.addObserver(obs)
        onDispose { lifecycle.lifecycle.removeObserver(obs) }
    }
    // Leaving Settings: no more looks, and nothing typed is kept.
    DisposableEffect(Unit) { onDispose { vm.leave() } }
    // Asked again while the device hasn't said whether it shows.
    AskDevice(st.answered, askFirst = false) { vm.load() }
    if (!st.visible) {
        if (!st.answered) LoadingSection(S.appSettingsWifiTitle().resolve())
        return
    }
    Section(S.appSettingsWifiTitle().resolve(), S.appSettingsWifiIntro().resolve()) {
        Caption((if (st.ssid.isNotEmpty()) S.appSettingsWifiOnWifi(st.ssid, WifiFlow.percent(st.signal).resolve()) else S.appSettingsWifiNoWifi()).resolve())
        if (st.wired) Caption(S.appSettingsWifiOnCable().resolve())
        if (st.picking) WifiPicker(vm, st)
        else RowButton(S.appSettingsWifiChange().resolve(), enabled = !st.busy) { vm.change() }
        // Sending: Connecting…; waiting: the device switching, with a
        // spinner; slow (4 minutes on): what to do if it doesn't come back.
        when (st.phase) {
            WifiViewModel.Phase.SENDING, WifiViewModel.Phase.WAITING -> Row(verticalAlignment = Alignment.CenterVertically) {
                CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp)
                Spacer(Modifier.width(8.dp))
                Caption(((if (st.phase == WifiViewModel.Phase.SENDING) null else st.note) ?: S.commonConnecting()).resolve())
            }
            WifiViewModel.Phase.SLOW -> Caption(S.appSettingsWifiSlow().resolve(), Color(0xFFFF9800))
            WifiViewModel.Phase.IDLE -> st.note?.let { Caption(it.resolve()) }
        }
        st.error?.let { Caption(it.resolve(), Color(0xFFE53935)) }
    }
    if (st.confirming) AlertDialog(
        onDismissRequest = { vm.cancelConfirm() },
        title = { Text(S.appSettingsWifiConfirmTitle(st.target).resolve()) },
        text = { Text((if (WifiFlow.cableOnly(st.wired, st.ssid)) S.appSettingsWifiConfirmCableMessage() else S.appSettingsWifiConfirmMessage()).resolve()) },
        confirmButton = { TextButton(onClick = { vm.join() }) { Text(S.commonConnect().resolve()) } },
        dismissButton = { TextButton(onClick = { vm.cancelConfirm() }) { Text(S.commonCancel().resolve()) } },
    )
}

/** Change Network's list: the networks in the device's order (its own first), then Other Network…; the picked one's fields under it. */
@Composable
private fun WifiPicker(vm: WifiViewModel, st: WifiViewModel.State) {
    val chosen = st.chosen
    if (st.scanning) Row(verticalAlignment = Alignment.CenterVertically) {
        CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp)
        Spacer(Modifier.width(8.dp))
        Caption(S.appSettingsWifiScanning().resolve())
    } else if (st.scanned && st.networks.isEmpty()) Caption(S.appSettingsWifiNoNetworks().resolve())
    st.networks.forEach { n ->
        val picked = chosen != null && chosen.kind != WifiFlow.Kind.OTHER && chosen.ssid == n.ssid
        WifiNetworkRow(n, picked, enabled = !st.busy) { vm.choose(n) }
        if (picked) WifiChosenFields(vm, st)
    }
    val other = chosen?.kind == WifiFlow.Kind.OTHER
    Row(
        Modifier.fillMaxWidth().selectable(selected = other, enabled = !st.busy, role = Role.RadioButton) { vm.chooseOther() }.padding(vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(S.appSettingsWifiOther().resolve(), Modifier.weight(1f))
        if (other) Icon(Icons.Filled.Check, null, tint = MaterialTheme.colorScheme.primary)
    }
    if (other) WifiChosenFields(vm, st)
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        RowButton(S.appSettingsWifiScan().resolve(), enabled = !st.scanning && !st.busy) { vm.scan() }
        Spacer(Modifier.weight(1f))
        TextButton(onClick = { vm.closePicker() }, enabled = !st.busy) { Text(S.commonCancel().resolve()) }
    }
    HorizontalDivider(Modifier.padding(vertical = 4.dp))
}

/** One network: its name, Connected for the device's own, a lock (or Open), its signal; greyed out when the device can't join it. */
@Composable
private fun WifiNetworkRow(n: WifiNetwork, picked: Boolean, enabled: Boolean, onClick: () -> Unit) {
    val usable = WifiFlow.joinable(n)
    val dim = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.38f)
    val secondary = MaterialTheme.colorScheme.onSurfaceVariant
    Row(
        Modifier.fillMaxWidth().selectable(selected = picked, enabled = enabled && usable, role = Role.RadioButton, onClick = onClick).padding(vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(n.ssid, Modifier.weight(1f), color = if (usable) Color.Unspecified else dim, maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (n.current) Text(S.appSettingsWifiCurrentBadge().resolve(), color = Color.White, fontSize = 10.sp, fontWeight = FontWeight.Bold,
            modifier = Modifier.padding(start = 6.dp).background(Color(0xFF43A047), RoundedCornerShape(4.dp)).padding(horizontal = 5.dp, vertical = 1.dp))
        Spacer(Modifier.width(6.dp))
        if (!usable) Text(S.appSettingsWifiUnsupported().resolve(), style = MaterialTheme.typography.bodySmall, color = dim, modifier = Modifier.padding(end = 6.dp))
        if (n.secured) Icon(Icons.Filled.Lock, contentDescription = S.appSettingsWifiSecured().resolve(), tint = secondary, modifier = Modifier.width(16.dp))
        else Text(S.appSettingsWifiOpenNetwork().resolve(), style = MaterialTheme.typography.bodySmall, color = secondary)
        Text(WifiFlow.percent(n.signal).resolve(), style = MaterialTheme.typography.bodySmall, color = if (usable) secondary else dim, modifier = Modifier.padding(start = 6.dp))
        if (picked) Icon(Icons.Filled.Check, null, tint = MaterialTheme.colorScheme.primary, modifier = Modifier.padding(start = 4.dp))
    }
}

/** Under the picked network: the name typed (Other Network…), the password where it can have one, and Connect. */
@Composable
private fun WifiChosenFields(vm: WifiViewModel, st: WifiViewModel.State) {
    val kind = st.chosen?.kind ?: return
    // A network's name, as typed: no capitals, no corrections.
    if (kind == WifiFlow.Kind.OTHER) OTCTextField(
        value = st.name, onValueChange = vm::setName, placeholder = { Text(S.appSettingsWifiName().resolve()) }, singleLine = true, enabled = !st.busy,
        keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false, keyboardType = KeyboardType.Text, imeAction = ImeAction.Next),
        modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp),
    )
    when (kind) {
        // Done on the keyboard is Connect, as Return is on iOS.
        WifiFlow.Kind.SECURED -> WifiPasswordField(st.password, S.commonPassword().resolve(), enabled = !st.busy, onChange = vm::setPassword, onDone = vm::connect)
        WifiFlow.Kind.OTHER -> WifiPasswordField(st.password, S.appSettingsWifiPasswordOptional().resolve(), enabled = !st.busy, onChange = vm::setPassword, onDone = vm::connect)
        WifiFlow.Kind.OPEN -> {}
    }
    RowButton(S.commonConnect().resolve(), enabled = !st.busy) { vm.connect() }
}

/**
 * The Wi-Fi password: a platform field with importantForAutofill = no, so
 * a password manager neither fills this app's password in nor saves the
 * Wi-Fi's as the app's (Compose's own text fields always take part in
 * autofill). No suggestions or learning, no saved view state: the text
 * lives in WifiViewModel alone. Drawn like OTCTextField.
 */
@Composable
private fun WifiPasswordField(value: String, placeholder: String, enabled: Boolean, onChange: (String) -> Unit, onDone: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    val textSize = MaterialTheme.typography.bodyMedium.fontSize.value
    val textColor = colors.onSurface.toArgb()
    val hintColor = colors.onSurfaceVariant.toArgb()
    val density = LocalDensity.current
    val padH = with(density) { 10.dp.roundToPx() }
    val padV = with(density) { 7.dp.roundToPx() }
    val onChangeNow by rememberUpdatedState(onChange)
    val onDoneNow by rememberUpdatedState(onDone)
    var focused by remember { mutableStateOf(false) }
    AndroidView(
        factory = { ctx ->
            EditText(ctx).apply {
                importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO
                isSaveEnabled = false
                freezesText = false
                inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
                imeOptions = EditorInfo.IME_ACTION_DONE or EditorInfo.IME_FLAG_NO_PERSONALIZED_LEARNING
                gravity = Gravity.CENTER_VERTICAL or Gravity.START
                background = null
                setPadding(padH, padV, padH, padV)
                doAfterTextChanged { onChangeNow(it?.toString().orEmpty()) }
                setOnEditorActionListener { _, action, _ ->
                    if (action == EditorInfo.IME_ACTION_DONE) { onDoneNow(); true } else false
                }
                setOnFocusChangeListener { _, f -> focused = f }
            }
        },
        update = { et ->
            // Cleared by the view model once sealed; typing keeps them equal.
            if (et.text.toString() != value) et.setText(value)
            et.hint = placeholder
            et.isEnabled = enabled
            et.setTextColor(textColor)
            et.setHintTextColor(hintColor)
            et.setTextSize(TypedValue.COMPLEX_UNIT_SP, textSize)
        },
        modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp).heightIn(min = 36.dp)
            .border(if (focused) 1.5.dp else 1.dp, if (focused) colors.primary else colors.outline, MaterialTheme.shapes.small),
    )
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
    AskDevice(st.answered) { vm.load() }
    if (!st.visible) {
        if (!st.answered) LoadingSection(S.appSettingsBridgeTitle().resolve())
        return
    }
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
    AskDevice(st.answered) { vm.checkRole() }
    if (!st.answered) { LoadingSection(S.appSettingsUsersTitle().resolve()); return }
    if (!st.isPrimary) return
    val usernameValid = st.newUsername.trim().let { it.isNotEmpty() && Regex("^[a-z0-9-]+$").matches(it) }
    Section(S.appSettingsUsersTitle().resolve()) {
        Caption(S.appSettingsUsersIntro().resolve())
        st.users.forEach { u -> UserRow(vm, st, u) }
        Row(Modifier.fillMaxWidth().tourTarget(TourTarget.ADD_USER), verticalAlignment = Alignment.CenterVertically) {
            OTCTextField(value = st.newUsername, onValueChange = vm::setNewUsername, placeholder = { FitText(S.appSettingsUsersUsername().resolve(), minScale = 0.8f) }, singleLine = true,
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false), modifier = Modifier.weight(1f))
            Spacer(Modifier.width(6.dp))
            OTCTextField(value = st.newPort, onValueChange = vm::setNewPort, placeholder = { Text(S.appSettingsUsersPort().resolve()) }, singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number), modifier = Modifier.width(90.dp))
            RowButton(S.commonAdd().resolve(), enabled = !st.creating && usernameValid) { vm.askPassword() }
        }
        // Issue #103, as the web app: the address the new user gets on the bridge.
        ToggleRow(S.appSettingsUsersRequestBridge().resolve(), st.requestBridgeAccess, vm::setRequestBridgeAccess, Modifier.tourTarget(TourTarget.BRIDGE_ACCESS))
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

/**
 * Smart search's section: its switch with a Beta badge and the footer; off
 * asks first ([confirmOff]: the device deletes what it learned). Under the
 * footer, where its [model] is (release 152): the download note while it
 * isn't on the device, a progress bar and how far it is - or the device's
 * words for why it waits, it tries again by itself - while it comes,
 * nothing once it is there.
 */
@Composable
internal fun SmartSearchSection(
    on: Boolean, saving: Boolean, confirmOff: Boolean, model: SmartSearchModelState,
    onToggle: (Boolean) -> Unit, onConfirmOff: () -> Unit, onCancelOff: () -> Unit,
) {
    val title = S.appSettingsSmartSearchTitle().resolve()
    Section(title, S.appSettingsSmartSearchFooter().resolve(), below = { SmartSearchModelLines(model) }) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
                Text(title, Modifier.weight(1f, fill = false))
                BetaBadge(S.appSettingsSmartSearchBeta().resolve())
            }
            Switch(checked = on, enabled = !saving, onCheckedChange = onToggle)
        }
    }
    if (confirmOff) {
        cloud.offthe.otc.ui.gallery.ConfirmDialog(
            S.appSettingsSmartSearchOffTitle().resolve(), S.appSettingsSmartSearchOffText().resolve(), S.appSettingsSmartSearchOffConfirm().resolve(),
            onConfirm = onConfirmOff, onDismiss = onCancelOff,
        )
    }
}

/**
 * Where Smart search's model is, under the section's footer. The bar is a
 * percentage to TalkBack (its progress semantics); the line beside it
 * isn't announced as it changes - a polite live region says only that the
 * download started, ran into a problem or is done
 * (SmartSearchModelState.announcement).
 */
@Composable
private fun SmartSearchModelLines(model: SmartSearchModelState) {
    model.progress?.let { p ->
        LinearProgressIndicator(progress = { p }, modifier = Modifier.fillMaxWidth().padding(top = 8.dp))
    }
    model.words()?.let {
        Text(it.resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 6.dp))
    }
    // What to say, once per change worth saying: the model as last shown
    // (null: nothing yet - the first view isn't announced).
    var shown by remember { mutableStateOf<SmartSearchModelState?>(null) }
    var said by remember { mutableStateOf<Pair<Int, SmartSearchAnnouncement>?>(null) }
    LaunchedEffect(model) {
        SmartSearchModelState.announcement(shown, model)?.let { said = (said?.first ?: 0) + 1 to it }
        shown = model
    }
    // Emptied again a little later, so swiping through Settings never lands
    // on a node that only repeats what was said.
    LaunchedEffect(said?.first) {
        if (said != null) { delay(5_000); said = null }
    }
    val words = said?.second?.text()?.resolve()
    Box(Modifier.size(1.dp).semantics {
        liveRegion = LiveRegionMode.Polite
        if (words != null) contentDescription = words
    })
}

/** A small "Beta" badge beside a setting's label (Smart search). */
@Composable
private fun BetaBadge(text: String) {
    androidx.compose.material3.Surface(
        Modifier.padding(start = 8.dp), shape = androidx.compose.foundation.shape.RoundedCornerShape(6.dp),
        color = MaterialTheme.colorScheme.secondaryContainer,
    ) {
        Text(
            text, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSecondaryContainer,
            modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp),
        )
    }
}
