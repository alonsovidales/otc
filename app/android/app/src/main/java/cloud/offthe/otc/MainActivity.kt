// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.push.FCMPush
import cloud.offthe.otc.ui.RootView
import cloud.offthe.otc.ui.theme.OTCTheme

// The counterpart of OffTheCloudApp.swift: the one Activity hosts the
// whole Compose tree, the same way the SwiftUI App hosts RootView.
class MainActivity : ComponentActivity() {
    // Issue #125: Android 13+ asks before an app may show notifications -
    // at launch, like the iOS app. Declining only means pushes aren't shown.
    private val askNotifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU && FCMPush.available(this) &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            askNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
        if (savedInstanceState == null) openFromPush(intent)
        setContent {
            OTCTheme {
                RootView()
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        openFromPush(intent)
    }

    // A tapped push (issue #125) opens what it is about, as a tap in
    // Alerts does. In the background Android puts the push's data keys in
    // the intent's extras; OTCMessagingService does the same in the
    // foreground.
    private fun openFromPush(intent: Intent?) {
        if (intent?.hasExtra(FCMPush.DATA_KEY) != true) return
        NotificationsModel.handlePushTap(
            intent.getStringExtra("kind"), intent.getStringExtra("pub_uuid"), intent.getStringExtra("comment_uuid"),
        )
    }
}
