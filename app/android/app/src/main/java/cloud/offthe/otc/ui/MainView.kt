// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.PhotoLibrary
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material.icons.outlined.Forum
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.TextButton
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.repeatOnLifecycle
import androidx.compose.ui.graphics.vector.ImageVector
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.GetStatus
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.UpdateAlert
import cloud.offthe.otc.ui.files.FilesExplorerView
import cloud.offthe.otc.ui.gallery.PhotoGalleryView
import cloud.offthe.otc.ui.settings.SettingsView
import cloud.offthe.otc.ui.social.SocialFeedView
import kotlinx.coroutines.delay

// Port of MainView.swift: the five tabs (Alerts, Social, Files, Images,
// Settings), a badge on Alerts, and the connection overlays (issue #56).
// Tabs are built lazily on first selection, like LazyTab on iOS.
private data class Tab(val id: Int, val label: String, val icon: ImageVector)

private val tabs = listOf(
    Tab(0, "Alerts", Icons.Default.Notifications),
    Tab(1, "Social", Icons.Outlined.Forum),
    Tab(3, "Files", Icons.Default.Folder),
    Tab(4, "Images", Icons.Default.PhotoLibrary),
    Tab(5, "Settings", Icons.Default.Settings),
)

@Composable
fun MainView(secrets: SecretsStore) {
    var selected by rememberSaveable { mutableStateOf(1) }
    val built = remember { mutableStateOf(setOf(1)) }
    val unread by NotificationsModel.unacknowledgedCount.collectAsState()
    val deepLink by NotificationsModel.pendingDeepLink.collectAsState()
    val statusCode by OTCConnection.statusCode.collectAsState()
    val lastError by OTCConnection.lastError.collectAsState()
    val connectionFailed by OTCConnection.connectionFailed.collectAsState()

    // Issue #151: one failed attempt is not a connection problem - at
    // launch the first try often goes out before the network is up, and
    // the next one works. The card with the connection settings is shown
    // only once connecting has kept failing for a few seconds, retried
    // meanwhile.
    var showConnectionProblem by remember { mutableStateOf(false) }
    LaunchedEffect(connectionFailed) {
        if (!connectionFailed) { showConnectionProblem = false; return@LaunchedEffect }
        repeat(3) {
            kotlinx.coroutines.delay(2_500)
            try { OTCConnection.ensureConnected(); return@LaunchedEffect } catch (_: Exception) {}
        }
        showConnectionProblem = OTCConnection.connectionFailed.value
    }

    val alertsRequested by NotificationsModel.alertsRequested.collectAsState()
    val appContext = androidx.compose.ui.platform.LocalContext.current.applicationContext

    LaunchedEffect(deepLink) { if (deepLink != null) selected = 1 }
    // Issue #125: a tapped push lands on Alerts.
    LaunchedEffect(alertsRequested) {
        if (alertsRequested) { selected = 0; NotificationsModel.consumeAlertsRequest() }
    }
    // Issue #125: signed in to a device - hand it this phone's push token.
    LaunchedEffect(Unit) { cloud.offthe.otc.push.FCMPush.register(appContext) }
    // Issue #183: a tapped update alert lands on Settings.
    val settingsRequested by NotificationsModel.settingsRequested.collectAsState()
    LaunchedEffect(settingsRequested) {
        if (settingsRequested) { selected = 5; NotificationsModel.consumeSettingsRequest() }
    }
    LaunchedEffect(selected) { built.value = built.value + selected }

    // Issue #183: the device's status, at launch and every 5 minutes while
    // in the foreground, for its update alert. A failed fetch keeps the
    // last answer.
    var updateAlert by remember { mutableStateOf<UpdateAlert?>(null) }
    val lifecycle = androidx.compose.ui.platform.LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(lifecycle) {
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            while (true) {
                try {
                    val resp = OTCConnection.request { it.setReqGetStatus(GetStatus.getDefaultInstance()) }
                    if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_STATUS) {
                        updateAlert = if (resp.respStatus.hasUpdateAlert()) resp.respStatus.updateAlert else null
                    }
                } catch (_: Exception) {}
                delay(5 * 60_000L)
            }
        }
    }
    val critical = updateAlert?.takeIf { it.level == "critical" }

    Scaffold(topBar = {
        if (critical != null) CriticalUpdateBanner(critical) { selected = 5 }
    }, bottomBar = {
        NavigationBar {
            tabs.forEach { t ->
                NavigationBarItem(
                    selected = selected == t.id,
                    onClick = { selected = t.id },
                    icon = {
                        if (t.id == 0 && unread > 0) {
                            BadgedBox(badge = { Badge { Text(unread.toString()) } }) { Icon(t.icon, t.label) }
                        } else Icon(t.icon, t.label)
                    },
                    label = { Text(t.label) },
                )
            }
        }
    }) { pad ->
        // The window is edge to edge, where adjustResize no longer lifts
        // anything above the keyboard: imePadding does, for every tab (the
        // feed's comment field sat under the keyboard, invisible).
        Box(Modifier.fillMaxSize().padding(pad).consumeWindowInsets(pad).imePadding()) {
            // Every built tab stays in the tree (hidden) so its state and
            // scroll position survive switching, as on iOS.
            for (t in tabs) {
                if (t.id !in built.value) continue
                Box(Modifier.fillMaxSize().let { m -> m }, content = {
                    if (selected == t.id) when (t.id) {
                        0 -> NotificationsListView()
                        1 -> SocialFeedView()
                        3 -> FilesExplorerView(initialPath = "/")
                        4 -> PhotoGalleryView(deviceId = secrets.deviceId.value)
                        5 -> SettingsView(secrets = secrets)
                    }
                })
            }
            val code = statusCode
            val context = androidx.compose.ui.platform.LocalContext.current
            if (code != null) {
                DeviceUnreachableView(message = lastError ?: "", code = code, onClose = { logOut(context, secrets, unregisterPush = false) })
            } else if (showConnectionProblem) {
                ConnectionProblemView(secrets = secrets, onLeave = { logOut(context, secrets, unregisterPush = false) })
            }
        }
    }
}

/** Issue #183: stays up while the device has a critical update not installed. */
@Composable
private fun CriticalUpdateBanner(alert: UpdateAlert, onUpdate: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().background(Color(0xFFE53935)).statusBarsPadding().padding(start = 16.dp, end = 8.dp, top = 8.dp, bottom = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(Icons.Default.Warning, null, tint = Color.White)
        Spacer(Modifier.width(12.dp))
        Text(
            "Critical update ${alert.version} available. ${alert.summary.trim().let { if (it.isEmpty()) "" else "$it " }}Install it as soon as possible.",
            color = Color.White, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f),
        )
        TextButton(onClick = onUpdate) { Text("Update", color = Color.White, fontWeight = FontWeight.Bold) }
    }
}
