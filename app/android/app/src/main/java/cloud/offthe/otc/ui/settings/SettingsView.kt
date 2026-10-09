// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import cloud.offthe.otc.ui.logOut
import android.Manifest
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.PickVisualMediaRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.Check
import androidx.compose.material3.Icon
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import cloud.offthe.otc.ui.common.THUMB_CACHE_CHOICES
import cloud.offthe.otc.ui.common.ThumbStore
import cloud.offthe.otc.ui.common.formatCacheBytes
import androidx.compose.foundation.layout.Box
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
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
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
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.Toast
import cloud.offthe.otc.ui.compose.mediaPermissions
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import android.app.Activity
import android.content.Context
import android.content.Intent
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import cloud.offthe.otc.MainActivity
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.net.MediaStream
import cloud.offthe.otc.sync.AssetSyncCache
import cloud.offthe.otc.sync.SyncScheduler
import cloud.offthe.otc.ui.social.SocialFeedViewModel

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

@Composable
private fun RowButton(label: String, enabled: Boolean = true, destructive: Boolean = false, onClick: () -> Unit) {
    TextButton(onClick = onClick, enabled = enabled) { Text(label, color = if (destructive) Color(0xFFE53935) else MaterialTheme.colorScheme.primary) }
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
    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {}
    var confirmLogout by remember { mutableStateOf(false) }
    // Issue #180: the shared links list (SharedLinksView.kt).
    var showSharedLinks by remember { mutableStateOf(false) }
    // Settings > Logs (LogsView.kt), primary instance only like Tailscale and Users.
    var showLogs by remember { mutableStateOf(false) }
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

            Section("Status") { StatusSectionContent(status) }

            if (upload.totalPending > 0 || upload.isUploading) {
                Section("Uploads") {
                    Text(if (upload.isUploading) "Uploading ${upload.currentName}" else "Waiting", style = MaterialTheme.typography.bodyMedium)
                    LinearProgressIndicator(progress = { upload.progress }, modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp))
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Caption("${upload.totalPending} pending")
                        Spacer(Modifier.weight(1f))
                        RowButton(if (upload.isPaused) "Resume" else "Pause") { UploadModel.togglePause() }
                    }
                }
            }

            Section("Sharing", "Galleries and download links you shared, until they expire. Deleting one removes the shared copies and the link stops working.") {
                RowButton("Shared Links") { showSharedLinks = true }
            }

            UpdateSection()

            Section("Change Password") {
                PasswordField(dst.oldKey, "Current password", device::setOldKey)
                PasswordField(dst.newKey, "New password", device::setNewKey)
                PasswordField(dst.confirmKey, "Confirm new password", device::setConfirmKey)
                RowButton(if (dst.savingKey) "Changing…" else "Change Password", enabled = !dst.savingKey && dst.oldKey.isNotEmpty() && dst.newKey.isNotEmpty()) { scope.launch { device.changePassword(secrets) } }
            }

            Section("Sync Options") {
                ToggleRow("Wi-Fi only", wifiOnly, secrets::setWifiOnly)
                ToggleRow("Include videos", includeVideos, secrets::setIncludeVideos)
                ToggleRow("Sync from cloud", downloadFromCloud, secrets::setDownloadFromCloud)
                RowButton("Authorize Photos Access") { permission.launch(mediaPermissions()) }
            }

            Section("Sync", "Sync All goes through the whole library again. Sync From Now skips everything already in it: only photos and videos taken from now on are uploaded.") {
                RowButton("Sync Now") { secrets.persist(); PhotoSync.runForegroundAsync() }
                RowButton("Sync All") { secrets.persist(); PhotoSync.setWatermark(0); PhotoSync.runForegroundAsync() }
                RowButton("Sync From Now") { secrets.persist(); PhotoSync.setWatermark(System.currentTimeMillis()) }
            }

            ThumbnailCacheSection()

            Section("Image Tagging", "Recognise what newly uploaded photos and videos show (a beach, a dog, a birthday cake) so you can search for it. It runs on this device and nothing leaves it. Turning it off saves processing time; places from a photo's own location data are still searchable. It only affects what is uploaded while it is off.") {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text("Image Tagging", Modifier.weight(1f))
                    Switch(checked = dst.imageTaggingEnabled, enabled = !dst.savingImageTagging, onCheckedChange = { v -> scope.launch { device.toggleImageTagging(v) } })
                }
            }

            Section("People", "Detect faces in newly uploaded photos so you can search by person. Off unless you turn it on (here or during setup), and faces never leave the device. It looks at everyone in your photos, not just you. It only affects photos uploaded while it is on — use Reprocess Media below to scan the ones you already have.") {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text("Face Recognition", Modifier.weight(1f))
                    Switch(checked = dst.faceRecognitionEnabled, enabled = !dst.savingFaceRecognition, onCheckedChange = { v -> scope.launch { device.toggleFaceRecognition(v) } })
                }
            }

            Section("Connection") {
                // Edits stay here until Save Connection: a reconnect (a socket
                // drop while typing) dials the store's values. Keyed on the
                // saved ones, so a password change or leaving the bridge still
                // shows up in the fields.
                var editEndpoint by remember(endpoint) { mutableStateOf(endpoint) }
                var editPassword by remember(password) { mutableStateOf(password) }
                ConnectionEndpointFields(endpoint = editEndpoint, onEndpointChange = { editEndpoint = it })
                PasswordField(editPassword, "Password") { editPassword = it }
                routeLine(signedIn, route, endpoint)?.let { Caption(it) }
                Caption("Device ID: $deviceId")
                RowButton("Save Connection") {
                    secrets.setEndpoint(editEndpoint); secrets.setPassword(editPassword)
                    secrets.persist(); OTCConnection.invalidate()
                }
                RowButton("Log Out", destructive = true) { confirmLogout = true }
            }

            TailscaleSection()

            BridgeAccountSection(secrets)

            UsersManagementSection()

            Section("Reprocess Media", "Re-run tagging and face detection on every photo and video already in your library — useful after a detection fix or model update. This clears existing tags and recognized people first and rebuilds them from scratch.") {
                when (dst.reprocessStatus) {
                    "running" -> {
                        LinearProgressIndicator(progress = { if (dst.reprocessTotal > 0) dst.reprocessProcessed.toFloat() / dst.reprocessTotal else 0f }, modifier = Modifier.fillMaxWidth())
                        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                            Caption("Reprocessing… ${dst.reprocessPercent}% (${dst.reprocessProcessed} / ${dst.reprocessTotal})")
                            Spacer(Modifier.weight(1f))
                            RowButton(if (dst.cancellingReprocess) "Stopping…" else "Cancel", enabled = !dst.cancellingReprocess, destructive = true) { scope.launch { device.stopReprocess() } }
                        }
                    }
                    "stopped" -> {
                        Caption("Stopped at ${dst.reprocessPercent}% (${dst.reprocessProcessed} / ${dst.reprocessTotal}).")
                        Row(Modifier.fillMaxWidth()) {
                            RowButton(if (dst.startingReprocess) "Starting…" else "Resume Reprocessing", enabled = !dst.startingReprocess) { device.setReprocessConfirm(DeviceSettingsViewModel.ReprocessConfirm.RESUME) }
                            Spacer(Modifier.weight(1f))
                            RowButton("Start Over", enabled = !dst.startingReprocess) { device.setReprocessConfirm(DeviceSettingsViewModel.ReprocessConfirm.RESTART) }
                        }
                    }
                    else -> {
                        RowButton(if (dst.startingReprocess) "Starting…" else "Reprocess All Media", enabled = !dst.startingReprocess) { device.setReprocessConfirm(DeviceSettingsViewModel.ReprocessConfirm.RESTART) }
                        if (dst.reprocessStatus == "completed") Caption("Last run completed — ${dst.reprocessProcessed} file(s) processed.")
                        else if (dst.reprocessStatus == "failed") Caption("Last run failed after ${dst.reprocessProcessed} file(s) — try again.", Color(0xFFE53935))
                    }
                }
            }

            if (isPrimary) {
                Section("Logs", "What the device and its updates write down as they run - live, to read, share or send to us when something goes wrong.") {
                    RowButton("Logs") { showLogs = true }
                }
            }
            Spacer(Modifier.height(24.dp))
        }
        Toast(dst.toast, Modifier.align(Alignment.TopCenter))
    }

    if (showSharedLinks) SharedLinksView(onClose = { showSharedLinks = false })
    if (showLogs) LogsView(onClose = { showLogs = false })
    if (confirmLogout) {
        AlertDialog(
            onDismissRequest = { confirmLogout = false },
            title = { Text("Log out of this device?") },
            text = { Text("The connection, the sync history and everything cached from the device are removed from this phone. Nothing on the device itself is deleted.") },
            confirmButton = { TextButton(onClick = { confirmLogout = false; logOut(context, secrets) }) { Text("Log Out", color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { confirmLogout = false }) { Text("Cancel") } },
        )
    }
    dst.reprocessConfirm?.let { action ->
        val resume = action == DeviceSettingsViewModel.ReprocessConfirm.RESUME
        AlertDialog(
            onDismissRequest = { device.setReprocessConfirm(null) },
            text = { Text(if (resume) "Resume reprocessing where it left off? It can take a while." else "Reprocess every photo and video in your library? This deletes all existing tags and recognized people and rebuilds them from scratch. It can take a while and can't be undone.") },
            confirmButton = { TextButton(onClick = { scope.launch { device.startReprocess(forceRestart = !resume) } }) { Text(if (resume) "Resume" else "Reprocess", color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { device.setReprocessConfirm(null) }) { Text("Cancel") } },
        )
    }
}

/** Issue #190: which way the app reaches the device, worded as in the other apps; nothing while not connected. */
private fun routeLine(signedIn: Boolean, route: OTCConnection.Route?, endpoint: String): String? {
    if (!signedIn || route == null) return null
    if (route == OTCConnection.Route.HOME) return "Connected over your home network"
    val host = try { java.net.URI(SecretsStore.normalizedEndpoint(endpoint)).host } catch (e: Exception) { null }
    if (host.isNullOrEmpty()) return "Connected through the configured address"
    // Host names aren't case-sensitive; "Cala.Off-The.Cloud" is the bridge too, as on macOS.
    val h = host.lowercase()
    val bridge = SecretsStore.bridgeDomain
    return "Connected through " + if (h == bridge || h.endsWith(".$bridge")) bridge else host
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
    Section("Thumbnail cache", "Thumbnails are kept on this phone so Images opens without downloading them again.") {
        Row(
            Modifier.fillMaxWidth().clip(RoundedCornerShape(8.dp))
                .clickable(onClickLabel = "Change the size", role = Role.DropdownList) { menu = true }
                .semantics { contentDescription = "Maximum size, ${formatCacheBytes(usage.limit)}" }
                .padding(vertical = 8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text("Maximum size", Modifier.weight(1f))
            Box {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(formatCacheBytes(usage.limit), color = MaterialTheme.colorScheme.primary)
                    Icon(Icons.Filled.ArrowDropDown, null, tint = MaterialTheme.colorScheme.primary)
                }
                DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                    for (choice in THUMB_CACHE_CHOICES) {
                        DropdownMenuItem(
                            text = { Text(formatCacheBytes(choice)) },
                            trailingIcon = if (choice == usage.limit) ({ Icon(Icons.Filled.Check, "Selected") }) else null,
                            onClick = { menu = false; scope.launch { ThumbStore.setLimit(choice) } },
                        )
                    }
                }
            }
        }
        Caption("Using ${formatCacheBytes(usage.used)} of ${formatCacheBytes(usage.limit)}")
        RowButton("Clear thumbnail cache", enabled = !clearing) {
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
    Section("Profile") {
        Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.CenterHorizontally) {
            AvatarView(data = st.imageData, size = 96.dp)
            Box {
                TextButton(onClick = { photoMenu = true }) { Text("Change photo") }
                DropdownMenu(expanded = photoMenu, onDismissRequest = { photoMenu = false }) {
                    DropdownMenuItem(text = { Text("From this phone") }, onClick = {
                        photoMenu = false
                        picker.launch(PickVisualMediaRequest(ActivityResultContracts.PickVisualMedia.ImageOnly))
                    })
                    DropdownMenuItem(text = { Text("From your device") }, onClick = { photoMenu = false; devicePicker = true })
                }
            }
        }
        OTCTextField(value = st.name, onValueChange = vm::setName, placeholder = { Text("Name") }, singleLine = true, modifier = Modifier.fillMaxWidth())
        OTCTextField(value = st.bio, onValueChange = vm::setBio, placeholder = { Text("Description") }, minLines = 3, modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            RowButton("Save Profile", enabled = !st.saving) { scope.launch { vm.save() } }
            if (st.saving) CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp)
        }
        st.toast?.let { Caption(it) }
    }
}

private fun raidStateLabel(s: Status): String = when (s.raidState) {
    RaidState.RaidNone -> "No RAID"
    RaidState.RaidInSync -> "In sync"
    RaidState.RaidSyncing -> "Syncing (${s.raidSyncPercent.toInt()}%)"
    RaidState.RaidDegraded -> "Degraded"
    else -> "Unknown"
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
            Caption("RAID used: ${sizeText(s.raidUsage.toDouble())} of ${sizeText(s.raidSize.toDouble())}")
            Caption("Disk: ${sizeText(s.diskUsage.toDouble())} of ${sizeText(s.diskSize.toDouble())}")
            Caption("CPU: %.1f%% · Mem: ${sizeText(s.memUsage.toDouble())} of ${sizeText(s.memSize.toDouble())}".format(s.cpuUsagePrc))
            Caption("RAID: ${if (s.raidLevel.isEmpty()) raidStateLabel(s) else "${s.raidLevel} — ${raidStateLabel(s)}"} · Disks: ${s.disks}")
            s.errorsList.forEach { Caption("⚠️ ${it.message}", Color(0xFFE53935)) }
        }
        st.errorText != null -> Caption(st.errorText!!, Color(0xFFE53935))
        else -> CircularProgressIndicator()
    }
}

@Composable
private fun RaidUsageBar(percent: Double, tint: Color) {
    val clamped = percent.coerceIn(0.0, 100.0)
    Box(Modifier.fillMaxWidth().height(14.dp).clip(RoundedCornerShape(7.dp)).background(MaterialTheme.colorScheme.surfaceVariant)) {
        Box(Modifier.fillMaxWidth(clamped.toFloat() / 100f).height(14.dp).background(tint, RoundedCornerShape(7.dp)))
        Text("${clamped.toInt()}%", fontSize = 9.sp, color = Color.White, modifier = Modifier.align(Alignment.CenterEnd).padding(end = 3.dp).background(Color(0x59000000), RoundedCornerShape(4.dp)).padding(horizontal = 4.dp))
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
    Section("Device version") {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text(if (st.currentVersion > 0) st.installedText else "Version —")
            if (st.hasUpdate) { Text(" → ${st.latestText}", fontWeight = FontWeight.Bold); KindBadge(st.latestKind) }
            Spacer(Modifier.weight(1f))
            if (!st.hasUpdate && !st.running && st.checkError.isEmpty()) Caption("Up to date")
        }
        st.pending.forEach { r ->
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(r.label.ifEmpty { "build ${r.version}" }, fontWeight = FontWeight.Bold, style = MaterialTheme.typography.bodySmall)
                KindBadge(r.kind)
                Spacer(Modifier.width(6.dp))
                Caption(r.summary)
            }
        }
        if (st.running) Row(verticalAlignment = Alignment.CenterVertically) { CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp); Spacer(Modifier.width(8.dp)); Caption(st.message.ifEmpty { "Updating…" }) }
        if (st.state == "failed") Caption(if (st.lastUpdated.isEmpty()) "Update failed: ${st.message}" else "Update failed on ${st.lastUpdated}: ${st.message}", Color(0xFFE53935))
        if (st.checkError.isNotEmpty()) Caption("Couldn't reach the update server just now, so this may be out of date.")
        RowButton(if (st.checking) "Checking…" else "Check again", enabled = !st.checking && !st.running) { scope.launch { vm.check() } }
        if (st.hasUpdate) RowButton(if (st.starting) "Starting…" else "Update to ${st.latestText}", enabled = !st.starting && !st.running) { scope.launch { vm.apply() } }
        if (st.hasUpdate || st.running) Caption("The device rebuilds itself and restarts, which takes a few minutes and drops this connection on the way. Your photos and settings are left alone.")
        st.error?.let { Caption(it, Color(0xFFE53935)) }
        // Settings > Restart device: the whole machine, owner of the primary only.
        RowButton(if (st.restarting) "Restarting…" else "Restart Device", enabled = !st.restarting && !st.running) { confirmRestart = true }
        when (st.restartPhase) {
            "restarting" -> Row(verticalAlignment = Alignment.CenterVertically) { CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp); Spacer(Modifier.width(8.dp)); Caption("Restarting… the device will be back in about a minute.") }
            "back" -> Caption("The device is back.")
            "slow" -> Caption("The device hasn't come back yet. It can take a little longer; if it doesn't come back, unplug it and plug it back in.", Color(0xFFFF9800))
        }
        st.restartError?.let { Caption(it, Color(0xFFE53935)) }
    }
    if (confirmRestart) AlertDialog(
        onDismissRequest = { confirmRestart = false },
        title = { Text("Restart the device?") },
        text = { Text("It will be unreachable for about a minute.") },
        confirmButton = { TextButton(onClick = { confirmRestart = false; vm.viewModelScope.launch { vm.restart() } }) { Text("Restart", color = Color(0xFFE53935)) } },
        dismissButton = { TextButton(onClick = { confirmRestart = false }) { Text("Cancel") } },
    )
}

/** Issue #183: "Major" (orange) or "Critical" (red) next to a release; nothing for a minor one. */
@Composable
private fun KindBadge(kind: String) {
    val (label, color) = when (kind) {
        "major" -> "Major" to Color(0xFFFF9800)
        "critical" -> "Critical" to Color(0xFFE53935)
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
        if (st.funnelOn) Caption("Served over Tailscale Funnel at ${st.publicUrl}")
        if (st.installed) {
            Caption("Tailscale Funnel is an alternative to the Off The Cloud bridge, it publishes this device on your own ts.net address but with the next limitations:")
            Caption("• The social side won't work. Friends find each other through the bridge, so sharing and friends' timelines are unavailable.")
            Caption("• One user only. Extra users need a web address each, and Funnel gives this machine a single one.")
            Caption("• Bandwidth is capped. Tailscale limits what can pass through Funnel and doesn't publish the limit.")
            Caption("• You'll need a Tailscale account, with Funnel enabled for your tailnet.")
            Caption("It suits a device you want purely as your own private NAS — your files and photos, reachable from anywhere, nothing shared with anyone.")
            if (!st.funnelOn) OTCTextField(value = st.authKey, onValueChange = vm::setAuthKey, placeholder = { Text("Auth key (optional)") }, singleLine = true,
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false), modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp))
            RowButton(if (st.busy) "Working…" else if (st.funnelOn) "Turn Funnel off" else "Enable Tailscale Funnel", enabled = !st.busy) {
                scope.launch {
                    vm.configure(enable = !st.funnelOn)
                    val login = vm.state.value.loginUrl
                    if (login.isNotEmpty()) Share.openInBrowser(context, login)
                }
            }
            if (!st.funnelOn) Caption("With a key from your Tailscale admin console this finishes on its own. Without one, you'll be taken to Tailscale to authorise this device.")
        } else {
            Caption("Tailscale isn't installed on this device, so Funnel isn't available here.")
        }
        st.note?.let { Caption(it) }
        st.error?.let { Caption(it, Color(0xFFE53935)) }
    }
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
    Section("Bridge and Account", if (st.enabled) "Leaving gives the name back; the device keeps working at home, at http://${st.localAddress.ifEmpty { "otc.local:8080" }}." else null) {
        if (st.enabled) {
            Caption("Reachable from anywhere at ${st.domain}")
            if (st.localAddress.isNotEmpty()) Caption("At home: ${st.localAddress}")
            RowButton("Leave the Bridge", enabled = !st.busy, destructive = true) { confirmLeave = true }
            RowButton("Delete My Account…", enabled = !st.busy, destructive = true) { confirmDelete = true }
        } else {
            if (st.leftReason == "released") Caption("This device left the bridge: ${st.bridge} no longer knew its name (its account was deleted, or the name released). Everything on it is still there.", Color(0xFFFF9800))
            Caption("Only reachable on your home network. Join the bridge from Settings in the device's web app.")
            RowButton("Your Account Page") { open("account") }
        }
        RowButton("Privacy") { open("privacy") }
        st.note?.let { Caption(it) }
        st.error?.let { Caption(it, Color(0xFFE53935)) }
    }
    if (confirmLeave) AlertDialog(
        onDismissRequest = { confirmLeave = false },
        title = { Text("Leave the bridge?") },
        text = { Text("${st.domain} is given back and the device restarts. It keeps everything and works at home, but is no longer reachable from outside or by your friends. You can join again later.") },
        confirmButton = { TextButton(onClick = { confirmLeave = false; scope.launch { vm.leave(secrets) } }) { Text("Leave the Bridge", color = Color(0xFFE53935)) } },
        dismissButton = { TextButton(onClick = { confirmLeave = false }) { Text("Cancel") } },
    )
    if (confirmDelete) AlertDialog(
        onDismissRequest = { confirmDelete = false },
        title = { Text("Delete your account?") },
        text = { Text("This device leaves the bridge first and goes on working at home. Then your account page opens: sign in there and delete the account, which releases any other devices' names too.") },
        confirmButton = { TextButton(onClick = { confirmDelete = false; scope.launch { if (vm.leave(secrets)) open("account?delete=1") } }) { Text("Leave and Delete", color = Color(0xFFE53935)) } },
        dismissButton = { TextButton(onClick = { confirmDelete = false }) { Text("Cancel") } },
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
    Section("Users") {
        Caption("Each user runs as its own fully separate instance — own port, own database, own storage — managed only from here.")
        st.users.forEach { u -> UserRow(vm, st, u) }
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            OTCTextField(value = st.newUsername, onValueChange = vm::setNewUsername, placeholder = { Text("username") }, singleLine = true,
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false), modifier = Modifier.weight(1f))
            Spacer(Modifier.width(6.dp))
            OTCTextField(value = st.newPort, onValueChange = vm::setNewPort, placeholder = { Text("port") }, singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number), modifier = Modifier.width(90.dp))
            RowButton(if (st.creating) "…" else "Add", enabled = !st.creating && usernameValid) { scope.launch { vm.createUser() } }
        }
    }
    st.toast?.let { AlertDialog(onDismissRequest = { vm.dismissToast() }, text = { Text(it) }, confirmButton = { TextButton(onClick = { vm.dismissToast() }) { Text("OK") } }) }
}

@Composable
private fun UserRow(vm: UsersManagementViewModel, st: UsersManagementViewModel.State, u: User) {
    val scope = rememberCoroutineScope()
    Column(Modifier.fillMaxWidth().padding(vertical = 4.dp)) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Row { Text(u.username, fontWeight = FontWeight.Bold); if (!u.active) Text(" INACTIVE", style = MaterialTheme.typography.labelSmall, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                Caption("port ${u.port} · ${u.subdomain}")
            }
            val m = st.metrics[u.uuid]
            if (m != null) Caption("${m.storageMb.toInt()} MB (%.1f%%) · ${m.activeConnections} active".format(m.storagePct))
            else RowButton("Load usage") { scope.launch { vm.fetchMetrics(u.uuid) } }
        }
        Row(Modifier.fillMaxWidth()) {
            RowButton(if (u.active) "Disable" else "Enable", enabled = st.busyActiveUuid != u.uuid) { scope.launch { vm.toggleActive(u) } }
            Spacer(Modifier.weight(1f))
            RowButton("Delete", destructive = true) { vm.toggleDeleteTarget(u.uuid) }
        }
        if (st.deleteTargetUuid == u.uuid) {
            Caption("Type \"${u.username}\" to confirm deletion.")
            OTCTextField(value = st.deleteConfirmText, onValueChange = vm::setDeleteConfirmText, placeholder = { Text(u.username) }, singleLine = true,
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false), modifier = Modifier.fillMaxWidth())
            RowButton(if (st.deleting) "Deleting…" else "Confirm Delete", enabled = !st.deleting && st.deleteConfirmText == u.username, destructive = true) { scope.launch { vm.confirmDelete(u) } }
        }
        HorizontalDivider(Modifier.padding(top = 4.dp))
    }
}


/** A size the device reports in MB, readable: "4.6 GB", "984 GB", "820 MB". Same as iOS's sizeText. */
internal fun sizeText(mb: Double): String = when {
    mb >= 100_000 -> "%.0f GB".format(mb / 1000)
    mb >= 1000 -> "%.1f GB".format(mb / 1000)
    else -> "%.0f MB".format(mb)
}
