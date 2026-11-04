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
        readable("a filled red button's text", Light.onError, Light.error)
        readable("a warning caption", Light.warning, Light.surface)
        readable("a warning caption in a dialog", Light.warning, Light.surfaceContainerHighest)
    }

    @Test fun darkTheme() {
        readable("a filled button's text", Dark.onPrimary, Dark.primary)
        readable("a text button on the screen", Dark.primary, Dark.surface)
        readable("a text button in a dialog", Dark.primary, Dark.surfaceContainerHigh)
        readable("a red button in a dialog", Dark.error, Dark.surfaceContainerHigh)
        readable("a filled red button's text", Dark.onError, Dark.error)
        readable("a warning caption", Dark.warning, Dark.surface)
        readable("a warning caption in a dialog", Dark.warning, Dark.surfaceContainerHighest)
    }

    @Test fun inkOnEmberAndWhiteOnTheAlertRed() {
        readable("ember-ink on ember", EmberInk, Ember)
        readable("white on the critical update's red", Color.White, AlertRed)
        readable("ember-ink on the major update's orange", EmberInk, WarningOrange)
    }

    /**
     * No screen draws text, an icon or a button's content in the red the
     * theme replaced (#E53935: 3.45:1 in a light dialog, 3.4:1 in a dark
     * one) or the warning orange (#FF9800: 2:1 on light surfaces): they take
     * the theme's error and warning (the theme's own WarningOrange is for
     * dark surfaces and badges with ember-ink on them). What isn't text
     * keeps them: the tour's drawings of the app, the storage bar's fill.
     */
    @Test fun noScreenHardCodesTheOldRedOrOrange() {
        val root = java.io.File("src/main/java")
        assertTrue("run from the app module", root.isDirectory)
        val old = Regex("""Color\(0xFF(E53935|FF9800)\)""")
        val notText = listOf("usedPct")
        val found = root.walkTopDown().filter { it.isFile && it.extension == "kt" && it.name !in setOf("TourArt.kt", "Theme.kt") }.flatMap { f ->
            f.readLines().mapIndexedNotNull { i, line ->
                if (old.containsMatchIn(line) && notText.none { line.contains(it) }) "${f.path}:${i + 1}: ${line.trim()}" else null
            }
        }.toList()
        assertTrue(found.joinToString("\n"), found.isEmpty())
    }
}
