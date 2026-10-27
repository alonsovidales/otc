// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.content.Intent
import android.content.res.Configuration
import android.os.Build
import android.os.Bundle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.appcompat.app.AppCompatActivity
import androidx.core.os.ConfigurationCompat
import androidx.core.os.LocaleListCompat
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.i18n.LanguageSettings
import cloud.offthe.otc.push.FCMPush
import cloud.offthe.otc.ui.RootView
import cloud.offthe.otc.ui.theme.OTCTheme

// The counterpart of OffTheCloudApp.swift: the one Activity hosts the
// whole Compose tree, the same way the SwiftUI App hosts RootView. An
// AppCompatActivity for AppCompat's per-app language (docs/i18n.md, spike
// S2): a change of language recreates it, so state that must survive one
// is kept with rememberSaveable or in a view model. The manifest declares
// locale changes handled only so that the one screen that can't be
// recreated keeps them in place (onConfigurationChanged).
class MainActivity : AppCompatActivity() {
    // The languages the screen was drawn in.
    private var shownLocales: LocaleListCompat? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        shownLocales = ConfigurationCompat.getLocales(resources.configuration)
        enableEdgeToEdge()
        // Issue #125: notifications are no longer asked for here, at launch
        // and over the welcome screen (UX 1): RootView asks once, after the
        // first sign-in, saying what they bring (NotificationsAsk.kt).
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

    // A new language - the app's, or the system's under Automatic -
    // recreates the Activity, as it always did, except while a screen
    // follows it in place (LanguageSettings.holdInPlace): the Bluetooth
    // setup, whose link to the device and wizard page a recreation would
    // tear down. There Compose re-reads the resources, which Android (and
    // below API 33 AppCompat) has already updated; below 33 the views are
    // told here, as only the resources changed.
    override fun onConfigurationChanged(newConfig: Configuration) {
        super.onConfigurationChanged(newConfig)
        val locales = ConfigurationCompat.getLocales(newConfig)
        if (locales == shownLocales) return
        if (!LanguageSettings.followsInPlace) {
            recreate()
            return
        }
        shownLocales = locales
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) window.decorView.dispatchConfigurationChanged(newConfig)
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
