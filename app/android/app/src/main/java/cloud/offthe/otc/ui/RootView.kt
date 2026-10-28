// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import cloud.offthe.otc.ui.common.ThumbStore
import android.app.Activity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.IntentSenderRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.Wake
import cloud.offthe.otc.sync.FreeUpSpace
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
    // After a sign-in: what to sync from this phone is asked before any sync
    // starts, and before the media permission prompt (SyncChoice).
    val choicePending by SyncChoice.pending.collectAsState()
    val lifecycleOwner = LocalLifecycleOwner.current

    // Media access is asked right after the sync question is answered (its
    // Start Sync, as iOS asks for Photos there), never before a device is
    // set up, and the first sync starts the moment it is granted.
    // Notifications are no longer asked with it (UX 1): NotificationsAsk,
    // below, asks once after the first sign-in.
    val askPermissions = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        if (PhotoSync.hasPermission()) PhotoSync.runForegroundAsync()
    }
    // Once per launch: a change of language recreates the Activity
    // (LanguageSettings), and the prompt must not come back with it.
    var askedPermissions by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(configured, choicePending) {
        if (!configured || choicePending || PhotoSync.hasPermission() || askedPermissions) return@LaunchedEffect
        askedPermissions = true
        askPermissions.launch(mediaPermissions())
    }

    // Free up space: Android's question before photos go to the trash,
    // which needs an Activity (FreeUpSpace makes the request, after a sync).
    // Whether the request on screen left others for the next one: saved,
    // as the answer can come to an Activity made again.
    var trashMore by rememberSaveable { mutableStateOf(false) }
    val trash = rememberLauncherForActivityResult(ActivityResultContracts.StartIntentSenderForResult()) {
        FreeUpSpace.requestAnswered(it.resultCode == Activity.RESULT_OK, trashMore)
    }
    val trashRequest by FreeUpSpace.request.collectAsState()
    LaunchedEffect(trashRequest) {
        val r = FreeUpSpace.takeRequest() ?: return@LaunchedEffect
        trashMore = r.more
        try { trash.launch(IntentSenderRequest.Builder(r.sender).build()) } catch (e: Exception) { FreeUpSpace.requestAnswered(false) }
    }

    DisposableEffect(lifecycleOwner, configured) {
        val observer = LifecycleEventObserver { _, event ->
            // Free up space asks only while the app is in front, and a sync
            // that waited for Wi-Fi starts from the network watch only then.
            if (event == Lifecycle.Event.ON_RESUME) { FreeUpSpace.inFront = true; PhotoSync.inFront = true }
            if (event == Lifecycle.Event.ON_STOP) { FreeUpSpace.inFront = false; PhotoSync.inFront = false }
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
    LaunchedEffect(configured, choicePending) { if (configured && !choicePending && PhotoSync.hasPermission()) PhotoSync.runForegroundAsync() }

    // Issue #133: a no-op on first launch (the model's init already started
    // it), the restart after Log Out + sign in.
    LaunchedEffect(configured) { if (configured) SocialFeedViewModel.startAutoLoad() }

    // The first-run tour (Tour.kt): opens by itself once signed in, with
    // MainView up and no card over it, after the sync question.
    val signedIn by OTCConnection.authenticated.collectAsState()
    val statusCode by OTCConnection.statusCode.collectAsState()
    val connectionFailed by OTCConnection.connectionFailed.collectAsState()
    val mainVisible = configured && statusCode == null && !connectionFailed
    LaunchedEffect(choicePending, signedIn, mainVisible) { TourModel.evaluate(choicePending, signedIn, mainVisible) }
    val tourShown by TourModel.shown.collectAsState()
    // While it shows, the controls it rings say where they are (TourTargets).
    LaunchedEffect(tourShown) { TourTargets.live = tourShown }

    if (configured) {
        Box(Modifier.fillMaxSize()) {
            // Out of TalkBack's reach while the tour lies over it.
            Box(Modifier.fillMaxSize().then(if (tourShown) Modifier.clearAndSetSemantics {} else Modifier)) {
                MainView(secrets = secrets)
            }
            if (tourShown) TourView()
        }
        // Notifications, asked once after the first sign-in
        // (NotificationsAsk.kt), never at launch.
        NotificationsAskDialog(signedIn, choicePending, tourShown, mainVisible)
        // The first sync then starts from the effects above, now that the
        // choice is no longer pending. Its dialog, as every dialog, opens
        // above the tour.
        if (choicePending) SyncChoiceView(secrets) {}
    } else {
        OnboardingView(secrets = secrets, onSaved = { configured = secrets.isConfigured })
    }
}
