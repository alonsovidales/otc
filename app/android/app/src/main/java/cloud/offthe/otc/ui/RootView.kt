// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import cloud.offthe.otc.ui.common.ThumbStore
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
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.Wake
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.sync.SyncScheduler
import cloud.offthe.otc.ui.compose.mediaPermissions
import cloud.offthe.otc.ui.social.SocialFeedViewModel

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
    // Once per launch: a change of language recreates the Activity
    // (LanguageSettings), and the prompt must not come back with it.
    var askedPermissions by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(configured) {
        if (!configured || PhotoSync.hasPermission() || askedPermissions) return@LaunchedEffect
        askedPermissions = true
        val perms = mediaPermissions().toMutableList()
        if (Build.VERSION.SDK_INT >= 33) perms += Manifest.permission.POST_NOTIFICATIONS
        askPermissions.launch(perms.toTypedArray())
    }

    DisposableEffect(lifecycleOwner, configured) {
        val observer = LifecycleEventObserver { _, event ->
            if (!configured) return@LifecycleEventObserver
            when (event) {
                // Issue #190: the route first - back home, the sync goes over
                // the home network rather than holding the bridge route for
                // as long as it runs.
                // Back in the foreground: what failed while away is asked
                // for again now (Wake), not at the end of its wait.
                Lifecycle.Event.ON_RESUME -> {
                    PhotoSync.runForegroundAsync(after = OTCConnection.reconsiderRoute())
                    NotificationsModel.startPolling()
                    Wake.fire()
                }
                // The thumbnail cache's uses since its last write go out too.
                Lifecycle.Event.ON_STOP -> { SyncScheduler.scheduleNext(); NotificationsModel.stopPolling(); ThumbStore.flushSoon() }
                else -> {}
            }
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose { lifecycleOwner.lifecycle.removeObserver(observer) }
    }

    // The first sync right after setup (or sign-in): the app is already in
    // the foreground then, and the resume the lifecycle observer above
    // relies on never came - photos waited until the app was reopened. A
    // no-op when a sync is already running; without media permission the
    // permission prompt's result starts it instead.
    LaunchedEffect(configured) { if (configured && PhotoSync.hasPermission()) PhotoSync.runForegroundAsync() }

    // Issue #133: a no-op on first launch (the model's init already started
    // it), the restart after Log Out + sign in.
    LaunchedEffect(configured) { if (configured) SocialFeedViewModel.startAutoLoad() }

    if (configured) {
        MainView(secrets = secrets)
    } else {
        OnboardingView(secrets = secrets, onSaved = { configured = secrets.isConfigured })
    }
}
