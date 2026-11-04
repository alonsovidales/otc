// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.theme

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.ColorScheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.luminance

// The iOS app's accent: the "ember" orange used for the + button and
// highlights (Color(red: 1.0, green: 0.42, blue: 0.29) in SwiftUI).
val Ember = Color(0xFFFF6B4A)

/** The web's --ember-ink: text on ember, 6.42:1 (white on ember is 2.82:1). */
val EmberInk = Color(0xFF26100A)

/**
 * Ember deepened for light backgrounds: text and lines on white at 5.95:1
 * (4.8:1 on a light dialog), and white on it as much - ember itself is
 * 2.7:1 there, which every text button, dialog button and filled button
 * of the light theme used to be.
 */
val EmberDeep = Color(0xFFB5371C)

/** A red to put white text on (a critical update): 5.6:1. */
val AlertRed = Color(0xFFC62828)

// Text in the theme's error colour reads at 4.5:1 or more on its surfaces
// and dialogs: what removes or signs out, and what failed. A filled red
// button is error with onError on it (white light, dark red dark).
internal val Light = lightColorScheme(primary = EmberDeep, onPrimary = Color.White, error = Color(0xFFB3261E))
internal val Dark = darkColorScheme(primary = Ember, onPrimary = EmberInk, error = Color(0xFFFF8A75))

/** The orange of a caption that warns (an update going slowly, a bridge let go), on dark surfaces: 5.7:1 or more. */
val WarningOrange = Color(0xFFFF9800)

/** That orange deepened for light surfaces: 5:1 or more (the orange itself is 2:1 there). */
val WarningDeep = Color(0xFF8F4E00)

/**
 * A warning caption's colour on this scheme's surfaces: [WarningOrange] on
 * a dark one, [WarningDeep] on a light one.
 */
val ColorScheme.warning: Color get() = if (surface.luminance() > 0.5f) WarningDeep else WarningOrange

@Composable
fun OTCTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = if (isSystemInDarkTheme()) Dark else Light,
        content = content,
    )
}
