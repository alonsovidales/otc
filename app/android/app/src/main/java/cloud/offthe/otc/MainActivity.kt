// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import cloud.offthe.otc.ui.RootView
import cloud.offthe.otc.ui.theme.OTCTheme

// The counterpart of OffTheCloudApp.swift: the one Activity hosts the
// whole Compose tree, the same way the SwiftUI App hosts RootView.
class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        setContent {
            OTCTheme {
                RootView()
            }
        }
    }
}
