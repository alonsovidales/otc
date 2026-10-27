// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Block
import androidx.compose.material.icons.filled.CloudOff
import androidx.compose.material.icons.filled.WifiOff
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appShellAccountDisabled
import cloud.offthe.otc.i18n.appShellCantConnect
import cloud.offthe.otc.i18n.appShellCloseAndLeave
import cloud.offthe.otc.i18n.appShellConnectionHeading
import cloud.offthe.otc.i18n.appShellDeviceUnavailable
import cloud.offthe.otc.i18n.appShellLeave
import cloud.offthe.otc.i18n.appShellLeaveButton
import cloud.offthe.otc.i18n.appShellLeaveMessageConnection
import cloud.offthe.otc.i18n.appShellLeaveMessageStart
import cloud.offthe.otc.i18n.appShellLeaveTitle
import cloud.offthe.otc.i18n.appShellSaveRetry
import cloud.offthe.otc.i18n.appShellStillTrying
import cloud.offthe.otc.i18n.appShellUnreachableWifiHint
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonConnecting
import cloud.offthe.otc.i18n.commonPassword
import cloud.offthe.otc.ui.common.PasswordField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.material3.TextButton
import androidx.compose.material3.IconButton
import androidx.compose.material3.AlertDialog
import androidx.compose.material.icons.filled.Close
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.ui.common.ConnectionEndpointFields
import cloud.offthe.otc.ui.common.rememberConnectionForm
import cloud.offthe.otc.ui.settings.LanguageMenu
import kotlinx.coroutines.launch

// Port of ConnectionProblemView.swift: shown over the tabs when the app
// can't connect or sign in, with the reason and the settings to fix it.
@Composable
fun ConnectionProblemView(secrets: SecretsStore, onLeave: ((endpoint: String, password: String) -> Unit)? = null) {
    val lastError by OTCConnection.lastError.collectAsState()
    // The form edits a copy until Save & Retry: every reconnect dials the
    // store's values, and pollers keep retrying while this card shows, so a
    // half-typed device name would hand the password to whichever device
    // has that name.
    // What is typed survives the recreation a pick in the language menu
    // brings.
    val form = rememberConnectionForm("connection-problem", endpoint = { secrets.endpoint.value }, password = { secrets.password.value })
    val endpoint by form::endpoint
    var password by form::password
    var retrying by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val passwordFocus = remember { FocusRequester() }
    val canSave = !retrying && endpoint.isNotEmpty() && password.isNotEmpty()
    // Save & Retry, and Go on the password's keyboard.
    fun saveAndRetry() {
        if (!canSave) return
        retrying = true
        secrets.saveCredentials(endpoint, password)
        OTCConnection.invalidate()
        scope.launch {
            try { OTCConnection.ensureConnected() } catch (_: Exception) {}
            retrying = false
        }
    }
    // Leaving this device for the start screen - how a phone moves on to
    // set up a new device, or another one, when this one can't be reached
    // or signed in to (a device being re-imaged, say).
    var confirmLeave by remember { mutableStateOf(false) }
    if (confirmLeave && onLeave != null) {
        AlertDialog(
            onDismissRequest = { confirmLeave = false },
            title = { Text(S.appShellLeaveTitle().resolve()) },
            text = { Text(S.appShellLeaveMessageStart().resolve()) },
            confirmButton = { TextButton(onClick = { confirmLeave = false; onLeave(endpoint, password) }) { Text(S.appShellLeave().resolve(), color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { confirmLeave = false }) { Text(S.commonCancel().resolve()) } },
        )
    }

    Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background.copy(alpha = 0.92f)), contentAlignment = Alignment.Center) {
        Card(Modifier.padding(24.dp).widthIn(max = 420.dp)) {
            Column(Modifier.padding(24.dp).verticalScroll(rememberScrollState()), horizontalAlignment = Alignment.CenterHorizontally) {
                Icon(Icons.Default.WifiOff, null, tint = Color(0xFFFF9500), modifier = Modifier.size(48.dp))
                Spacer(Modifier.height(12.dp))
                Text(S.appShellCantConnect().resolve(), style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
                val why = lastError?.resolve()
                if (!why.isNullOrEmpty()) {
                    Spacer(Modifier.height(8.dp))
                    Text(why, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
                }
                Spacer(Modifier.height(18.dp))
                Column(Modifier.fillMaxWidth()) {
                    Text(S.appShellConnectionHeading().resolve(), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    ConnectionEndpointFields(form.entry, onNext = { passwordFocus.requestFocus() })
                    Spacer(Modifier.height(8.dp))
                    PasswordField(
                        value = password, onValueChange = { password = it }, hint = S.commonPassword().resolve(),
                        onAction = ::saveAndRetry, modifier = Modifier.fillMaxWidth().focusRequester(passwordFocus),
                    )
                }
                Spacer(Modifier.height(18.dp))
                Button(
                    onClick = ::saveAndRetry,
                    enabled = canSave,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        if (retrying) { CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp); Spacer(Modifier.width(8.dp)) }
                        Text((if (retrying) S.commonConnecting() else S.appShellSaveRetry()).resolve())
                    }
                }
                if (onLeave != null) {
                    Spacer(Modifier.height(4.dp))
                    TextButton(onClick = { confirmLeave = true }) { Text(S.appShellLeaveButton().resolve()) }
                }
                // The language, while this covers the app and Settings with
                // it: shown at once, and sent at the next sign-in.
                LanguageMenu()
            }
        }
    }
}

// Port of DeviceUnreachableView.swift (issue #56): the bridge's verdict
// that the device is registered but not connected, or disabled.
@Composable
fun DeviceUnreachableView(message: String, code: String, onClose: (() -> Unit)? = null) {
    val disabled = code == "account_disabled"
    // The close button: leave this device for the connection screen - how
    // a phone moves on from a device that died (as DeviceUnreachableView.swift).
    var confirmClose by remember { mutableStateOf(false) }
    if (confirmClose && onClose != null) {
        AlertDialog(
            onDismissRequest = { confirmClose = false },
            title = { Text(S.appShellLeaveTitle().resolve()) },
            text = { Text(S.appShellLeaveMessageConnection().resolve()) },
            confirmButton = { TextButton(onClick = { confirmClose = false; onClose() }) { Text(S.appShellLeave().resolve(), color = Color(0xFFE53935)) } },
            dismissButton = { TextButton(onClick = { confirmClose = false }) { Text(S.commonCancel().resolve()) } },
        )
    }
    Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background.copy(alpha = 0.92f)), contentAlignment = Alignment.Center) {
        Card(Modifier.padding(24.dp).widthIn(max = 360.dp)) {
          Box {
            if (onClose != null) {
                IconButton(onClick = { confirmClose = true }, modifier = Modifier.align(Alignment.TopEnd)) {
                    Icon(Icons.Default.Close, contentDescription = S.appShellCloseAndLeave().resolve(), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            Column(Modifier.padding(28.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                Icon(if (disabled) Icons.Default.Block else Icons.Default.CloudOff, null, tint = Color(0xFFFF9500), modifier = Modifier.size(56.dp))
                Spacer(Modifier.height(16.dp))
                Text((if (disabled) S.appShellAccountDisabled() else S.appShellDeviceUnavailable()).resolve(),
                    style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
                if (message.isNotEmpty()) {
                    Spacer(Modifier.height(8.dp))
                    Text(message, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
                }
                if (!disabled) {
                    // A device that lost the home Wi-Fi (its name or password
                    // changed) comes back over a cable, then Settings › Wi-Fi.
                    // Before "still trying", as on iOS.
                    Spacer(Modifier.height(10.dp))
                    Text(S.appShellUnreachableWifiHint().resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
                    Spacer(Modifier.height(12.dp))
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp)
                        Spacer(Modifier.width(8.dp))
                        Text(S.appShellStillTrying().resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                // The language, while this covers the app and Settings with
                // it: shown at once, and sent once the device answers again
                // (or to the next device, when this one is left). At the
                // bottom: the close button has the card's top corner.
                Spacer(Modifier.height(4.dp))
                LanguageMenu()
            }
          }
        }
    }
}
