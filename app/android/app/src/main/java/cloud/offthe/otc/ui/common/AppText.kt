// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import android.content.Context
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.i18n.LanguageSettings
import cloud.offthe.otc.i18n.UiText
import java.io.IOException

// Text that leaves composition as a String: an exception's message, a
// toast, an answer handed to the setup page. Screens keep UiText and
// resolve it as they draw (docs/i18n.md, "Each platform"); what can't wait
// for a screen is resolved here, in the language the app shows at that
// moment - the Activity's when it has one, else the application's own
// (LanguageSettings.localizedContext: below API 33 AppCompat never localizes
// the Application).

/** This text in the language the app shows now. */
fun UiText.now(): String = resolve(LanguageSettings.localizedContext(OTCApp.instance))

/** This text in [context]'s language: an Activity's is the app's. */
fun UiText.now(context: Context): String = resolve(context)

/**
 * An error of ours whose message is text for the screen. The message is
 * worded when it is read, in the language shown then; [toString] (what a
 * log line prints) names the error without the text, so logs stay English.
 */
class TextIOException(val text: UiText, cause: Throwable? = null) : IOException(null, cause) {
    override val message: String get() = text.now()

    override fun toString(): String = "TextIOException($text)"
}
