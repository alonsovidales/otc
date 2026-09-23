// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.theme

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color

// The iOS app's accent: the "ember" orange used for the + button and
// highlights (Color(red: 1.0, green: 0.42, blue: 0.29) in SwiftUI).
val Ember = Color(0xFFFF6B4A)

private val Light = lightColorScheme(primary = Ember)
private val Dark = darkColorScheme(primary = Ember)

@Composable
fun OTCTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = if (isSystemInDarkTheme()) Dark else Light,
        content = content,
    )
}
