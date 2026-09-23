// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.Manifest
import android.os.Build
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.sync.SyncScheduler
import cloud.offthe.otc.ui.compose.mediaPermissions

// Port of RootView.swift: onboarding until the connection is configured,
// then the tabs; foreground/background transitions drive the photo sync
// the way scenePhase does on iOS (issue #70).
@Composable
fun RootView() {
    val secrets = remember { SecretsStore.loadOrCreate() }
    val endpoint by secrets.endpoint.collectAsState()
    val password by secrets.password.collectAsState()
    var configured by remember { mutableStateOf(secrets.isConfigured) }
    val lifecycleOwner = LocalLifecycleOwner.current

    // iOS asks for Photos access the first time the sync runs; Android
    // needs the runtime prompt, so it is asked once here, right after the
    // tabs first appear, and the first sync starts the moment it is granted.
    val askPermissions = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        if (PhotoSync.hasPermission()) PhotoSync.runForegroundAsync()
    }
    LaunchedEffect(configured) {
        if (!configured || PhotoSync.hasPermission()) return@LaunchedEffect
        val perms = mediaPermissions().toMutableList()
        if (Build.VERSION.SDK_INT >= 33) perms += Manifest.permission.POST_NOTIFICATIONS
        askPermissions.launch(perms.toTypedArray())
    }

    DisposableEffect(lifecycleOwner, configured) {
        val observer = LifecycleEventObserver { _, event ->
            if (!configured) return@LifecycleEventObserver
            when (event) {
                Lifecycle.Event.ON_RESUME -> { PhotoSync.runForegroundAsync(); NotificationsModel.startPolling() }
                Lifecycle.Event.ON_STOP -> { SyncScheduler.scheduleNext(); NotificationsModel.stopPolling() }
                else -> {}
            }
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose { lifecycleOwner.lifecycle.removeObserver(observer) }
    }

    if (configured) {
        MainView(secrets = secrets)
    } else {
        OnboardingView(secrets = secrets, onSaved = { configured = secrets.isConfigured })
    }
}
