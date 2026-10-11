// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.i18n.LanguageSettings
import cloud.offthe.otc.push.FCMPush
import cloud.offthe.otc.ui.RootView
import cloud.offthe.otc.ui.theme.OTCTheme

// The counterpart of OffTheCloudApp.swift: the one Activity hosts the
// whole Compose tree, the same way the SwiftUI App hosts RootView. An
// AppCompatActivity for AppCompat's per-app language (docs/i18n.md, spike
// S2): a change of language recreates it, so state that must survive one
// is kept with rememberSaveable or in a view model.
class MainActivity : AppCompatActivity() {
    // Issue #125: Android 13+ asks before an app may show notifications -
    // at launch, like the iOS app. Declining only means pushes aren't shown.
    private val askNotifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        // Only at a real launch: a change of language recreates the
        // Activity, and someone who declined must not be asked again then.
        if (savedInstanceState == null && Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU && FCMPush.available(this) &&
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

    // A language picked in the system's per-app screen, or one the device
    // changed since the last start, is applied here - at a start, never in
    // the middle of a form (one recreation).
    override fun onStart() {
        super.onStart()
        LanguageSettings.atActivityStart(this)
    }

    // Issue #152: opening the app is reading what arrived.
    override fun onResume() {
        super.onResume()
        FCMPush.clearShown(this)
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
