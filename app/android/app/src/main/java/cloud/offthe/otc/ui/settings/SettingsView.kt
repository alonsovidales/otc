// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import android.Manifest
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.PickVisualMediaRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
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
import kotlinx.coroutines.launch
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
    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {}
    var confirmLogout by remember { mutableStateOf(false) }

    LaunchedEffect(Unit) {
        device.loadSettings()
        status.start()
        device.loadReprocessStatus()
        if (device.state.value.reprocessStatus == "running") device.pollReprocessStatusWhileRunning()
    }
    DisposableEffect(Unit) { onDispose { status.stop(); device.stopPollingReprocessStatus() } }

    Box(Modifier.fillMaxSize()) {
        Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(vertical = 8.dp)) {
            ProfileEditorSection()
            UsersManagementSection()

            Section("Bridge Shared Secret", "This must match what the bridge has on record for this device — it can't learn a new one from here. To rotate it: on the bridge's admin panel, delete and re-add this device's domain (it'll hand you a new secret), then paste that value above.") {
                Row(Modifier.fillMaxWidth()) {
                    Text("Current", modifier = Modifier.width(80.dp))
                    Text(dst.currentBridgeSecret, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                OTCTextField(value = dst.newBridgeSecret, onValueChange = device::setNewBridgeSecret, placeholder = { Text("Secret from the bridge") }, singleLine = true,
                    keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false), modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp))
                RowButton(if (dst.savingSecret) "Saving…" else "Save Secret", enabled = !dst.savingSecret && dst.newBridgeSecret.isNotBlank()) { scope.launch { device.saveBridgeSecret() } }
            }

            Section("People", "Detect faces in newly uploaded photos so you can search by person. Off by default. Turning this on only affects photos uploaded from now on — it never scans photos you already have, even after you enable it.") {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text("Face Recognition", Modifier.weight(1f))
                    Switch(checked = dst.faceRecognitionEnabled, enabled = !dst.savingFaceRecognition, onCheckedChange = { v -> scope.launch { device.toggleFaceRecognition(v) } })
                }
            }

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

            Section("Change Password") {
                PasswordField(dst.oldKey, "Current password", device::setOldKey)
                PasswordField(dst.newKey, "New password", device::setNewKey)
                PasswordField(dst.confirmKey, "Confirm new password", device::setConfirmKey)
                RowButton(if (dst.savingKey) "Changing…" else "Change Password", enabled = !dst.savingKey && dst.oldKey.isNotEmpty() && dst.newKey.isNotEmpty()) { scope.launch { device.changePassword(secrets) } }
            }

            TailscaleSection()

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

            Section("Connection") {
                ConnectionEndpointFields(endpoint = endpoint, onEndpointChange = secrets::setEndpoint)
                PasswordField(password, "Password", secrets::setPassword)
                Caption("Device ID: $deviceId")
                RowButton("Save Connection") { secrets.persist(); OTCConnection.invalidate() }
                RowButton("Log Out", destructive = true) { confirmLogout = true }
            }

            Section("Sync Options") {
                ToggleRow("Wi-Fi only", wifiOnly, secrets::setWifiOnly)
                ToggleRow("Include videos", includeVideos, secrets::setIncludeVideos)
                ToggleRow("Sync from cloud", downloadFromCloud, secrets::setDownloadFromCloud)
                RowButton("Authorize Photos Access") { permission.launch(mediaPermissions()) }
            }

            Section("Sync") {
                RowButton("Sync Now") { secrets.persist(); PhotoSync.runForegroundAsync() }
                RowButton("Sync All") { secrets.persist(); PhotoSync.lastSyncMs = 0; PhotoSync.runForegroundAsync() }
            }

            UpdateSection()
            Spacer(Modifier.height(24.dp))
        }
        Toast(dst.toast, Modifier.align(Alignment.TopCenter))
    }

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

@Composable
private fun PasswordField(value: String, placeholder: String, onChange: (String) -> Unit) {
    OTCTextField(
        value = value, onValueChange = onChange, placeholder = { Text(placeholder) }, singleLine = true,
        visualTransformation = PasswordVisualTransformation(), keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
        modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp),
    )
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
private fun logOut(context: Context, secrets: SecretsStore) {
    NotificationsModel.reset()
    UploadModel.reset()
    SocialFeedViewModel.reset()
    OTCConnection.reset()
    MediaStream.reset()
    SyncScheduler.cancel()
    AssetSyncCache.clear()
    secrets.logOut()
    val activity = context as? Activity ?: return
    activity.startActivity(Intent(activity, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK))
    activity.finish()
}

@Composable
private fun ProfileEditorSection() {
    val vm: ProfileEditorViewModel = viewModel()
    val st by vm.state.collectAsState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val picker = rememberLauncherForActivityResult(ActivityResultContracts.PickVisualMedia()) { uri ->
        if (uri != null) scope.launch { vm.setImage(context.contentResolver.openInputStream(uri)?.use { it.readBytes() }) }
    }
    LaunchedEffect(Unit) { vm.load() }
    Section("Profile") {
        Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.CenterHorizontally) {
            AvatarView(data = st.imageData, size = 96.dp)
            TextButton(onClick = { picker.launch(PickVisualMediaRequest(ActivityResultContracts.PickVisualMedia.ImageOnly)) }) { Text("Change photo") }
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
            Caption("RAID used: ${s.raidUsage} MB / ${s.raidSize} MB")
            Caption("Disk: ${s.diskUsage} MB / ${s.diskSize} MB")
            Caption("CPU: %.1f%% · Mem: ${s.memUsage} MB / ${s.memSize} MB".format(s.cpuUsagePrc))
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
    LaunchedEffect(Unit) { vm.load() }
    if (!st.isPrimary) return
    Section("Device version") {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text(if (st.currentVersion > 0) "Version ${st.currentVersion}" else "Version —")
            if (st.hasUpdate) Text(" → ${st.latestVersion}", fontWeight = FontWeight.Bold)
            Spacer(Modifier.weight(1f))
            if (!st.hasUpdate && !st.running && st.checkError.isEmpty()) Caption("Up to date")
        }
        st.pending.forEach { r -> Row { Text("${r.version} ", fontWeight = FontWeight.Bold, style = MaterialTheme.typography.bodySmall); Caption(r.summary) } }
        if (st.running) Row(verticalAlignment = Alignment.CenterVertically) { CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp); Spacer(Modifier.width(8.dp)); Caption(st.message.ifEmpty { "Updating…" }) }
        if (st.state == "failed") Caption(if (st.lastUpdated.isEmpty()) "Update failed: ${st.message}" else "Update failed on ${st.lastUpdated}: ${st.message}", Color(0xFFE53935))
        if (st.checkError.isNotEmpty()) Caption("Couldn't reach the update server just now, so this may be out of date.")
        RowButton(if (st.checking) "Checking…" else "Check again", enabled = !st.checking && !st.running) { scope.launch { vm.check() } }
        if (st.hasUpdate) RowButton(if (st.starting) "Starting…" else "Update to ${st.latestVersion}", enabled = !st.starting && !st.running) { scope.launch { vm.apply() } }
        if (st.hasUpdate || st.running) Caption("The device rebuilds itself and restarts, which takes a few minutes and drops this connection on the way. Your photos and settings are left alone.")
        st.error?.let { Caption(it, Color(0xFFE53935)) }
    }
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
