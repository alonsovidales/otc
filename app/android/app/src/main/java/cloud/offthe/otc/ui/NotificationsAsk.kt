// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import androidx.core.content.ContextCompat
import kotlinx.coroutines.delay
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appShellNotificationsContinue
import cloud.offthe.otc.i18n.appShellNotificationsLater
import cloud.offthe.otc.i18n.appShellNotificationsText
import cloud.offthe.otc.i18n.appShellNotificationsTitle
import cloud.offthe.otc.push.FCMPush

/**
 * Android UX 1, as iOS's NotificationsAsk.swift: notifications are asked for
 * once, after the first accepted sign-in to a device, with a line on what
 * they bring (app.shell.notifications_text) before Android's own question -
 * no longer at the first launch over the welcome screen, and again with the
 * media question after the sync choice.
 */
object NotificationsAsk {
    // Its own preferences, as the tour's mark: not wiped at Log Out (asked
    // once on this phone, whichever device comes next) and, like every file
    // but otc_settings, not in the backup rules. commit(): written before the
    // question shows, so a kill can't show it twice.
    private const val PREFS = "otc_prompts"
    private const val ASKED = "notifications_asked"

    /** Pure: whether the question shows now - signed in, the sync question answered, the tour closed, no card over the app, Android not asked yet and the question not shown before. */
    fun shouldAsk(signedIn: Boolean, syncChoicePending: Boolean, tourShown: Boolean, mainVisible: Boolean, undecided: Boolean, askedBefore: Boolean): Boolean =
        signedIn && !syncChoicePending && !tourShown && mainVisible && undecided && !askedBefore

    /** Android 13 and later asks before an app may show notifications (and only with Firebase there is anything to show). */
    fun undecided(context: Context): Boolean =
        Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU && FCMPush.available(context) &&
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED

    fun askedBefore(context: Context): Boolean = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getBoolean(ASKED, false)

    fun markAsked(context: Context) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putBoolean(ASKED, true).commit()
    }
}

private const val QUESTION_SETTLE_MS = 600L

/** RootView's question, over MainView, shown the first time everything it waits for holds; the mark is written as it shows. */
@Composable
fun NotificationsAskDialog(signedIn: Boolean, syncChoicePending: Boolean, tourShown: Boolean, mainVisible: Boolean) {
    val context = LocalContext.current
    // Saved: a change of language recreates the Activity with it up.
    var showing by rememberSaveable { mutableStateOf(false) }
    // Declining only means pushes aren't shown.
    val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) {}
    LaunchedEffect(signedIn, syncChoicePending, tourShown, mainVisible) {
        if (showing) return@LaunchedEffect
        if (!NotificationsAsk.shouldAsk(signedIn, syncChoicePending, tourShown, mainVisible, undecided = true, askedBefore = false)) return@LaunchedEffect
        // The tour opens from an effect of this same moment (TourModel.evaluate):
        // a beat for it, then the tour as it stands now - a change meanwhile
        // starts this again.
        delay(QUESTION_SETTLE_MS)
        val undecided = NotificationsAsk.undecided(context)
        if (!NotificationsAsk.shouldAsk(signedIn, syncChoicePending, TourModel.shown.value, mainVisible, undecided, NotificationsAsk.askedBefore(context))) return@LaunchedEffect
        NotificationsAsk.markAsked(context)
        showing = true
    }
    if (!showing) return
    AlertDialog(
        onDismissRequest = { showing = false },
        title = { Text(S.appShellNotificationsTitle().resolve()) },
        text = { Text(S.appShellNotificationsText().resolve()) },
        confirmButton = {
            TextButton(onClick = {
                showing = false
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) ask.launch(Manifest.permission.POST_NOTIFICATIONS)
            }) { Text(S.appShellNotificationsContinue().resolve()) }
        },
        dismissButton = { TextButton(onClick = { showing = false }) { Text(S.appShellNotificationsLater().resolve()) } },
    )
}
