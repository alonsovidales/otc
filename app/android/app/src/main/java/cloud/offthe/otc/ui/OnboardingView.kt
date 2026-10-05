// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.ui.common.ConnectionEndpointFields

// Port of OnboardingView.swift: the first-run connection form.
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun OnboardingView(secrets: SecretsStore, onSaved: () -> Unit) {
    // A device set up from this phone fills the form (its install may have
    // outlived the Bluetooth link that would have signed in straight away).
    val pending = remember { if (secrets.isConfigured) null else SecretsStore.pendingSetup() }
    // Or the device this phone left while it was down (logOut keepDevice).
    val last = remember { if (secrets.isConfigured || pending != null) null else SecretsStore.lastDevice() }
    var endpoint by remember { mutableStateOf(pending?.first ?: last?.first ?: secrets.endpoint.value) }
    var password by remember { mutableStateOf(pending?.second ?: last?.second ?: secrets.password.value) }
    // Issue #137: a brand-new device is set up from here over Bluetooth;
    // when it is done its address lands in the form and the first sign in
    // sets the owner password.
    var setupOpen by remember { mutableStateOf(false) }
    if (setupOpen) {
        BluetoothSetupView(
            onUseDevice = { newEndpoint, newPassword ->
                // Straight in, as iOS: save both and open the app.
                secrets.setEndpoint(newEndpoint)
                secrets.setPassword(newPassword)
                secrets.persist()
                OTCConnection.invalidate()
                setupOpen = false
                onSaved()
            },
            onClose = { setupOpen = false },
        )
        return
    }

    Scaffold(topBar = { TopAppBar(title = { Text("Welcome") }) }) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().padding(16.dp)) {
            Text("Connect to Off The Cloud", style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Spacer(Modifier.height(8.dp))
            ConnectionEndpointFields(endpoint = endpoint, onEndpointChange = { endpoint = it })
            Spacer(Modifier.height(8.dp))
            OTCTextField(
                value = password, onValueChange = { password = it }, label = { Text("Password") },
                singleLine = true, visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                modifier = Modifier.fillMaxWidth(),
            )
            if (last != null) {
                Spacer(Modifier.height(6.dp))
                Text(
                    "Filled in from the device you left. Tap Save & Continue to connect to it again, or change it to use another one.",
                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            if (pending != null) {
                Spacer(Modifier.height(6.dp))
                Text(
                    "Filled in from the device you set up with this phone. Once its install has finished (about 20 minutes), tap Save & Continue.",
                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Spacer(Modifier.height(20.dp))
            Button(
                onClick = {
                    secrets.setEndpoint(endpoint.trim())
                    secrets.setPassword(password)
                    secrets.persist()
                    // A socket still signed in to the device logged out of
                    // (a request retried during Log Out) must not carry over.
                    OTCConnection.invalidate()
                    onSaved()
                },
                enabled = endpoint.isNotEmpty() && password.isNotEmpty(),
                modifier = Modifier.fillMaxWidth(),
            ) { Text("Save & Continue") }
            Spacer(Modifier.height(28.dp))
            androidx.compose.material3.OutlinedButton(onClick = { setupOpen = true }, modifier = Modifier.fillMaxWidth()) {
                Text("Set up a new device")
            }
            Spacer(Modifier.height(6.dp))
            Text(
                "For a device fresh out of the box: it runs the setup over Bluetooth, and this app becomes its app. Log out first if you are switching from another device.",
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            // Issue #175: what the bridge keeps, before signing in.
            val context = androidx.compose.ui.platform.LocalContext.current
            androidx.compose.material3.TextButton(onClick = { cloud.offthe.otc.ui.common.Share.openInBrowser(context, "https://off-the.cloud/privacy") }) { Text("Privacy") }
        }
    }
}
