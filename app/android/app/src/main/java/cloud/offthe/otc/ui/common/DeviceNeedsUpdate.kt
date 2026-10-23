// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.foundation.layout.size
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appSettingsUpdateStartFailed
import cloud.offthe.otc.i18n.appShellDeviceUpdateAskOwner
import cloud.offthe.otc.i18n.appShellDeviceUpdateStarted
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonOk
import cloud.offthe.otc.i18n.commonUpdate
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.ReqApplyUpdate
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.ReqGetInstanceRole
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.settings.shownText
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/** One request to the device, answered (tests answer in its place). */
typealias DeviceRequest = suspend (build: (ReqEnvelope.Builder) -> Unit) -> RespEnvelope

/**
 * "Your device needs an update to ...": what a feature shows when the
 * device answers its request unknown_payload (ErrorCodes.isUnknownPayload)
 * - this app is newer than the device. [title] says what needed it.
 * [primary]: the account that set the device up, which gets an Update
 * button that starts the update at once (Settings › Device version's);
 * anyone else is told to ask that account.
 */
data class DeviceNeedsUpdate(val title: UiText, val primary: Boolean)

/** [title]'s question, with the role the device gives [request] (not primary when it can't be read). */
suspend fun deviceNeedsUpdate(title: UiText, request: DeviceRequest): DeviceNeedsUpdate {
    val primary = try {
        val r = request { it.setReqGetInstanceRole(ReqGetInstanceRole.getDefaultInstance()) }
        r.payloadCase == RespEnvelope.PayloadCase.RESP_INSTANCE_ROLE && r.respInstanceRole.isPrimary
    } catch (e: CancellationException) {
        throw e
    } catch (_: Exception) { false }
    return DeviceNeedsUpdate(title, primary)
}

/** Starts the device's update, as Settings' Update button does: what to say then - that it started, or why not. */
suspend fun startDeviceUpdate(request: DeviceRequest): UiText = try {
    val r = request { it.setReqApplyUpdate(ReqApplyUpdate.getDefaultInstance()) }
    when {
        r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok -> S.appShellDeviceUpdateStarted()
        r.error && r.errorMessage.isNotEmpty() -> UiText.Raw(r.errorMessage)
        else -> S.appSettingsUpdateStartFailed()
    }
} catch (e: CancellationException) {
    throw e
} catch (e: Exception) { e.shownText() ?: S.appSettingsUpdateStartFailed() }

/**
 * [ask]'s dialog: its title, then Update and Cancel for the account that
 * set the device up - Update starts the update and the dialog says how
 * that went, with OK - or for anyone else who to ask, with OK.
 */
@Composable
fun DeviceNeedsUpdateDialog(
    ask: DeviceNeedsUpdate,
    onDismiss: () -> Unit,
    start: suspend () -> UiText = { startDeviceUpdate { build -> OTCConnection.request(build = build) } },
) {
    val scope = rememberCoroutineScope()
    var starting by remember(ask) { mutableStateOf(false) }
    // What starting it said: the dialog's text from then on.
    var said by remember(ask) { mutableStateOf<UiText?>(null) }
    val outcome = said
    val asking = ask.primary && outcome == null
    AlertDialog(
        onDismissRequest = { if (!starting) onDismiss() },
        title = { Text(ask.title.resolve()) },
        text = when {
            outcome != null -> { { Text(outcome.resolve()) } }
            !ask.primary -> { { Text(S.appShellDeviceUpdateAskOwner().resolve()) } }
            else -> null
        },
        confirmButton = {
            if (asking) TextButton(onClick = { starting = true; scope.launch { said = start(); starting = false } }, enabled = !starting) {
                if (starting) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp) else Text(S.commonUpdate().resolve())
            } else TextButton(onClick = onDismiss) { Text(S.commonOk().resolve()) }
        },
        dismissButton = if (asking) { { TextButton(onClick = onDismiss, enabled = !starting) { Text(S.commonCancel().resolve()) } } } else null,
    )
}
