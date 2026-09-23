// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.MaterialTheme
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.data.SecretsStore

// Port of ConnectionEndpointFields.swift (issue #121): the device's name
// is all the bridge needs; a custom address is behind a toggle, and an
// existing custom address opens the form in that mode.
@Composable
fun ConnectionEndpointFields(endpoint: String, onEndpointChange: (String) -> Unit) {
    var deviceName by remember { mutableStateOf("") }
    var customAddress by remember { mutableStateOf(false) }
    var loaded by remember { mutableStateOf(false) }

    LaunchedEffect(Unit) {
        if (loaded) return@LaunchedEffect
        loaded = true
        val name = SecretsStore.bridgeName(endpoint)
        if (name != null) deviceName = name else if (endpoint.isNotEmpty()) customAddress = true
    }

    Column {
        if (customAddress) {
            OTCTextField(
                value = endpoint, onValueChange = onEndpointChange,
                label = { Text("Address (wss://host/ws)") }, singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, capitalization = KeyboardCapitalization.None),
                modifier = Modifier.fillMaxWidth(),
            )
        } else {
            Row(verticalAlignment = Alignment.CenterVertically) {
                OTCTextField(
                    value = deviceName,
                    onValueChange = { name ->
                        deviceName = name
                        onEndpointChange(if (name.trim().isEmpty()) "" else SecretsStore.bridgeEndpoint(name))
                    },
                    label = { Text("Device name") }, singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Ascii, capitalization = KeyboardCapitalization.None),
                    modifier = Modifier.weight(1f),
                )
                Text(".${SecretsStore.bridgeDomain}", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 6.dp))
            }
        }
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth().padding(top = 8.dp)) {
            Text("Custom address", modifier = Modifier.weight(1f))
            Switch(checked = customAddress, onCheckedChange = { custom ->
                customAddress = custom
                if (!custom) {
                    deviceName = SecretsStore.bridgeName(endpoint) ?: ""
                    onEndpointChange(if (deviceName.isEmpty()) "" else SecretsStore.bridgeEndpoint(deviceName))
                }
            })
        }
        if (customAddress) {
            Text(
                "For a device not on the Off The Cloud bridge - your own bridge, Tailscale Funnel, or the local network (ws://192.168.…:8080/ws).",
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}
