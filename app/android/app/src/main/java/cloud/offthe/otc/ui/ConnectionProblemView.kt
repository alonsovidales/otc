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
import androidx.compose.foundation.text.KeyboardOptions
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
import cloud.offthe.otc.ui.common.OTCTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.ui.common.ConnectionEndpointFields
import kotlinx.coroutines.launch

// Port of ConnectionProblemView.swift: shown over the tabs when the app
// can't connect or sign in, with the reason and the settings to fix it.
@Composable
fun ConnectionProblemView(secrets: SecretsStore) {
    val lastError by OTCConnection.lastError.collectAsState()
    val endpoint by secrets.endpoint.collectAsState()
    val password by secrets.password.collectAsState()
    var retrying by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background.copy(alpha = 0.92f)), contentAlignment = Alignment.Center) {
        Card(Modifier.padding(24.dp).widthIn(max = 420.dp)) {
            Column(Modifier.padding(24.dp).verticalScroll(rememberScrollState()), horizontalAlignment = Alignment.CenterHorizontally) {
                Icon(Icons.Default.WifiOff, null, tint = Color(0xFFFF9500), modifier = Modifier.size(48.dp))
                Spacer(Modifier.height(12.dp))
                Text("Can't connect to your device", style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
                if (!lastError.isNullOrEmpty()) {
                    Spacer(Modifier.height(8.dp))
                    Text(lastError!!, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
                }
                Spacer(Modifier.height(18.dp))
                Column(Modifier.fillMaxWidth()) {
                    Text("Connection", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    ConnectionEndpointFields(endpoint = endpoint, onEndpointChange = { secrets.setEndpoint(it) })
                    Spacer(Modifier.height(8.dp))
                    OTCTextField(
                        value = password, onValueChange = { secrets.setPassword(it) }, label = { Text("Password") },
                        singleLine = true, visualTransformation = PasswordVisualTransformation(),
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
                Spacer(Modifier.height(18.dp))
                Button(
                    onClick = {
                        retrying = true
                        secrets.persist()
                        OTCConnection.invalidate()
                        scope.launch {
                            try { OTCConnection.ensureConnected() } catch (_: Exception) {}
                            retrying = false
                        }
                    },
                    enabled = !retrying && secrets.isConfigured,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        if (retrying) { CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp); Spacer(Modifier.width(8.dp)) }
                        Text(if (retrying) "Connecting…" else "Save & Retry")
                    }
                }
            }
        }
    }
}

// Port of DeviceUnreachableView.swift (issue #56): the bridge's verdict
// that the device is registered but not connected, or disabled.
@Composable
fun DeviceUnreachableView(message: String, code: String) {
    val disabled = code == "account_disabled"
    Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background.copy(alpha = 0.92f)), contentAlignment = Alignment.Center) {
        Card(Modifier.padding(24.dp).widthIn(max = 360.dp)) {
            Column(Modifier.padding(28.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                Icon(if (disabled) Icons.Default.Block else Icons.Default.CloudOff, null, tint = Color(0xFFFF9500), modifier = Modifier.size(56.dp))
                Spacer(Modifier.height(16.dp))
                Text(if (disabled) "This account has been disabled" else "This device isn't available right now",
                    style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
                if (message.isNotEmpty()) {
                    Spacer(Modifier.height(8.dp))
                    Text(message, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
                }
                if (!disabled) {
                    Spacer(Modifier.height(12.dp))
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp)
                        Spacer(Modifier.width(8.dp))
                        Text("Still trying…", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
            }
        }
    }
}
