// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.PhotoLibrary
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.outlined.Forum
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Icon
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
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.ui.files.FilesExplorerView
import cloud.offthe.otc.ui.gallery.PhotoGalleryView
import cloud.offthe.otc.ui.settings.SettingsView
import cloud.offthe.otc.ui.social.SocialFeedView

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

    LaunchedEffect(deepLink) { if (deepLink != null) selected = 1 }
    LaunchedEffect(selected) { built.value = built.value + selected }

    Scaffold(bottomBar = {
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
        Box(Modifier.fillMaxSize().padding(pad)) {
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
            if (code != null) {
                DeviceUnreachableView(message = lastError ?: "", code = code)
            } else if (connectionFailed) {
                ConnectionProblemView(secrets = secrets)
            }
        }
    }
}
