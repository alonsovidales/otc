// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import cloud.offthe.otc.ui.common.ThumbStore
import android.app.Activity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.IntentSenderRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.ui.Alignment
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.androidSecretsStartOver
import cloud.offthe.otc.i18n.androidSecretsStartOverText
import cloud.offthe.otc.i18n.androidSecretsStartOverTitle
import cloud.offthe.otc.i18n.androidSecretsUnavailable
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonTryAgain
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.TextButton
import androidx.compose.runtime.rememberCoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelStore
import androidx.lifecycle.ViewModelStoreOwner
import androidx.lifecycle.viewmodel.compose.LocalViewModelStoreOwner
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.Wake
import cloud.offthe.otc.sync.FreeUpSpace
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.sync.SyncScheduler
import cloud.offthe.otc.ui.compose.mediaPermissions
import cloud.offthe.otc.ui.gallery.DeletePhoneCopyDialog
import cloud.offthe.otc.ui.social.SocialFeedViewModel

/**
 * The view models of one device session (SecretsStore.deviceSession): a
 * new store for each, the last one cleared - iOS's `.id(deviceSession)` on
 * MainView. An Activity-scoped view model itself, so it outlives a
 * recreation.
 */
class SessionStores : ViewModel() {
    private var session = -1
    private var store = ViewModelStore()

    fun owner(session: Int): ViewModelStoreOwner {
        if (session != this.session) {
            if (this.session >= 0) store.clear()
            store = ViewModelStore()
            this.session = session
        }
        val s = store
        return object : ViewModelStoreOwner { override val viewModelStore = s }
    }

    override fun onCleared() = store.clear()
}

// Port of RootView.swift: onboarding until the connection is configured,
// then the tabs; foreground/background transitions drive the photo sync
// the way scenePhase does on iOS (issue #70).
@Composable
fun RootView() {
    // The secure store, opened once; while the Keystore can't open it (busy,
    // not ready after an OS update), a screen that says so and tries again -
    // nothing is wiped (SecretsStore.Unavailable) unless the owner starts
    // over from it, or it has failed on every launch for a day.
    var attempt by remember { mutableStateOf(0) }
    val secrets = remember(attempt) { SecretsStore.openOrNull() }
    if (secrets == null) {
        SecretsUnavailableView(onRetry = { attempt++ }, onStartedOver = { attempt++ })
        return
    }
    RootContent(secrets)
}

/**
 * The secure store couldn't be opened: said, with Try again, and Start over
 * (asked first) for a key the Keystore will never take again - it used to
 * be the only way out but clearing the app's data in system settings.
 */
@Composable
private fun SecretsUnavailableView(onRetry: () -> Unit, onStartedOver: () -> Unit) {
    var confirm by remember { mutableStateOf(false) }
    var working by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
        Column(
            Modifier.fillMaxSize().systemBarsPadding().verticalScroll(rememberScrollState()).padding(28.dp),
            verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Text(S.androidSecretsUnavailable().resolve(), style = MaterialTheme.typography.bodyLarge, textAlign = TextAlign.Center)
            Spacer(Modifier.height(16.dp))
            Button(onClick = onRetry, enabled = !working) { Text(S.commonTryAgain().resolve()) }
            Spacer(Modifier.height(4.dp))
            TextButton(onClick = { confirm = true }, enabled = !working) {
                Text(S.androidSecretsStartOver().resolve(), color = MaterialTheme.colorScheme.error)
            }
        }
    }
    if (confirm) {
        AlertDialog(
            onDismissRequest = { confirm = false },
            title = { Text(S.androidSecretsStartOverTitle().resolve()) },
            text = { Text(S.androidSecretsStartOverText().resolve()) },
            confirmButton = {
                TextButton(onClick = {
                    confirm = false
                    working = true
                    scope.launch {
                        // Off the main thread: a Keystore key made again.
                        withContext(Dispatchers.IO) { runCatching { SecretsStore.startOver() } }
                        working = false
                        onStartedOver()
                    }
                }) { Text(S.androidSecretsStartOver().resolve(), color = MaterialTheme.colorScheme.error) }
            },
            dismissButton = { TextButton(onClick = { confirm = false }) { Text(S.commonCancel().resolve()) } },
        )
    }
}

@Composable
private fun RootContent(secrets: SecretsStore) {
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
    // it), the restart after Log Out + sign in - and after another address
    // was saved (DeviceChange reset the feed and the Alerts poll).
    val deviceSession by secrets.deviceSession.collectAsState()
    LaunchedEffect(configured, deviceSession) {
        if (!configured) return@LaunchedEffect
        SocialFeedViewModel.startAutoLoad()
        if (deviceSession > 0 && lifecycleOwner.lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED)) NotificationsModel.startPolling()
    }
    // The view models MainView's screens make, one store per device
    // session: another address saved makes every screen afresh, none keeping
    // the last device's lists. Kept by the Activity's own view model, so a
    // recreation (a change of language) keeps them as before.
    val sessions: SessionStores = viewModel()

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
                key(deviceSession) {
                    val owner = remember { sessions.owner(deviceSession) }
                    CompositionLocalProvider(LocalViewModelStoreOwner provides owner) {
                        MainView(secrets = secrets)
                    }
                }
            }
            if (tourShown) TourView()
        }
        // Notifications, asked once after the first sign-in
        // (NotificationsAsk.kt), never at launch.
        NotificationsAskDialog(signedIn, choicePending, tourShown, mainVisible)
        // After a delete on the device: also delete it from this phone?
        // (DeletePhoneCopy.kt), over whatever is on screen.
        DeletePhoneCopyDialog()
        // The first sync then starts from the effects above, now that the
        // choice is no longer pending. Its dialog, as every dialog, opens
        // above the tour.
        if (choicePending) SyncChoiceView(secrets) {}
    } else {
        OnboardingView(secrets = secrets, onSaved = { configured = secrets.isConfigured })
    }
}
