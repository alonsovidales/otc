// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.theme

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.luminance
import org.junit.Assert.assertTrue
import org.junit.Test

// Text in the app's colours reads at WCAG's 4.5:1 on what it sits on, in
// both themes: filled buttons, text and dialog buttons, the composer's
// picked number, what removes or signs out, and the critical update's
// banner. Light mode used white on ember (2.82:1) and ember text (2.7:1).
class ThemeContrastTest {
    private fun ratio(a: Color, b: Color): Double {
        val la = a.luminance() + 0.05
        val lb = b.luminance() + 0.05
        return (maxOf(la, lb) / minOf(la, lb)).toDouble()
    }

    private fun readable(what: String, text: Color, on: Color) {
        val r = ratio(text, on)
        assertTrue("$what: %.2f:1".format(r), r >= 4.5)
    }

    @Test fun lightTheme() {
        readable("a filled button's text", Light.onPrimary, Light.primary)
        readable("a text button on the screen", Light.primary, Light.surface)
        readable("a text button in a dialog", Light.primary, Light.surfaceContainerHigh)
        readable("a red button in a dialog", Light.error, Light.surfaceContainerHigh)
        readable("a red line on the screen", Light.error, Light.surface)
    }

    @Test fun darkTheme() {
        readable("a filled button's text", Dark.onPrimary, Dark.primary)
        readable("a text button on the screen", Dark.primary, Dark.surface)
        readable("a text button in a dialog", Dark.primary, Dark.surfaceContainerHigh)
        readable("a red button in a dialog", Dark.error, Dark.surfaceContainerHigh)
    }

    @Test fun inkOnEmberAndWhiteOnTheAlertRed() {
        readable("ember-ink on ember", EmberInk, Ember)
        readable("white on the critical update's red", Color.White, AlertRed)
        readable("ember-ink on the major update's orange", EmberInk, Color(0xFFFF9800))
    }
}
