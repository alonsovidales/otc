// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.MaterialTheme
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appShellEndpointAddress
import cloud.offthe.otc.i18n.appShellEndpointCustom
import cloud.offthe.otc.i18n.appShellEndpointCustomNote
import cloud.offthe.otc.i18n.appShellEndpointDeviceName
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.activity.compose.LocalActivity
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.Saver
import androidx.compose.runtime.saveable.listSaver
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.data.SecretsStore

// Port of ConnectionEndpointFields.swift (issue #121): the device's name
// is all the bridge needs; a custom address is behind a toggle, and an
// existing custom address opens the form in that mode. What the fields
// show lives in [entry], next to the address it stands for, so whatever
// keeps one through the Activity's recreation (a change of language with
// the screen's language menu) keeps the other: the name shown is always
// the address Save sends. [onNext]: the keyboard's Next, to the password
// after it.
@Composable
fun ConnectionEndpointFields(entry: EndpointEntry, onNext: () -> Unit = {}) {
    val next = KeyboardActions(onNext = { onNext() })
    Column {
        if (entry.customAddress) {
            OTCTextField(
                value = entry.endpoint, onValueChange = { entry.typeAddress(it) },
                label = { Text(S.appShellEndpointAddress().resolve()) }, singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, capitalization = KeyboardCapitalization.None, imeAction = ImeAction.Next),
                keyboardActions = next,
                modifier = Modifier.fillMaxWidth(),
            )
        } else {
            Row(verticalAlignment = Alignment.CenterVertically) {
                OTCTextField(
                    value = entry.deviceName, onValueChange = { entry.typeName(it) },
                    label = { Text(S.appShellEndpointDeviceName().resolve()) }, singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Ascii, capitalization = KeyboardCapitalization.None, imeAction = ImeAction.Next),
                    keyboardActions = next,
                    modifier = Modifier.weight(1f),
                )
                Text(".${SecretsStore.bridgeDomain}", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 6.dp))
            }
        }
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth().padding(top = 8.dp)) {
            Text(S.appShellEndpointCustom().resolve(), modifier = Modifier.weight(1f))
            Switch(checked = entry.customAddress, onCheckedChange = { entry.useCustomAddress(it) })
        }
        if (entry.customAddress) {
            Text(
                S.appShellEndpointCustomNote().resolve(),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

/**
 * The address a connection form is typed into, and how: the device's name
 * on the bridge, or the address in full. One holder for the three, so the
 * name shown can't stand for another address than the one saved (a
 * saveable name beside an address held elsewhere outlived it, and Save
 * sent a device the field didn't show). Starts from [endpoint]: its name
 * when it is a plain bridge address, else that address in custom mode.
 */
@Stable
class EndpointEntry private constructor(endpoint: String, deviceName: String, customAddress: Boolean) {
    constructor(endpoint: String) : this(endpoint, SecretsStore.bridgeName(endpoint))

    private constructor(endpoint: String, name: String?) : this(endpoint, name ?: "", name == null && endpoint.isNotEmpty())

    /** The endpoint as stored - always the full address, whichever way it was entered. */
    var endpoint by mutableStateOf(endpoint)
        private set
    var deviceName by mutableStateOf(deviceName)
        private set
    var customAddress by mutableStateOf(customAddress)
        private set

    /** The name field: the address follows it ("" while it is blank). */
    fun typeName(name: String) {
        deviceName = name
        endpoint = if (name.trim().isEmpty()) "" else SecretsStore.bridgeEndpoint(name)
    }

    /** The address field, in custom mode. */
    fun typeAddress(address: String) {
        endpoint = address
    }

    /** Switching to the name keeps whatever name the address had; switching to custom keeps the address. */
    fun useCustomAddress(custom: Boolean) {
        customAddress = custom
        if (!custom) typeName(SecretsStore.bridgeName(endpoint) ?: "")
    }

    companion object {
        /** For rememberSaveable: the address with its name and mode, never apart. */
        val Saver: Saver<EndpointEntry, Any> = listSaver(
            save = { listOf(it.endpoint, it.deviceName, it.customAddress) },
            restore = { EndpointEntry(it[0] as String, it[1] as String, it[2] as Boolean) },
        )
    }
}

/**
 * What a connection form holds while it is typed in (the welcome screen,
 * the "can't connect" card), kept through the Activity's recreation on a
 * change of language with the screen's language menu. A view model rather
 * than saved state, so the password never leaves the app's memory; the
 * address goes with it, so a process that dies loses the whole form at
 * once, never the address alone. null: not shown yet / nothing typed, the
 * screen's starting values show.
 */
class ConnectionFormModel : ViewModel() {
    var entry: EndpointEntry? = null
    val password: MutableState<String?> = mutableStateOf(null)
}

/** A connection form's fields: what was typed, else the values the screen started from. */
@Stable
class ConnectionForm internal constructor(
    private val model: ConnectionFormModel,
    startEndpoint: String,
    private val startPassword: String,
) {
    /** The address fields (ConnectionEndpointFields). */
    val entry: EndpointEntry = model.entry ?: EndpointEntry(startEndpoint).also { model.entry = it }

    /** The address Save sends. */
    val endpoint: String get() = entry.endpoint

    var password: String
        get() = model.password.value ?: startPassword
        set(value) { model.password.value = value }
}

/**
 * The form under [key], starting from [endpoint] and [password] each time
 * the screen shows; a recreation of the Activity keeps what was typed.
 */
@Composable
fun rememberConnectionForm(key: String, endpoint: () -> String, password: () -> String): ConnectionForm {
    val model: ConnectionFormModel = viewModel(key = key)
    val form = remember(model) { ConnectionForm(model, endpoint(), password()) }
    val activity = LocalActivity.current
    DisposableEffect(model) {
        onDispose {
            if (activity?.isChangingConfigurations != true) {
                model.entry = null
                model.password.value = null
            }
        }
    }
    return form
}
