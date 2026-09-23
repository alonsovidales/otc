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
import cloud.offthe.otc.ui.common.ConnectionEndpointFields

// Port of OnboardingView.swift: the first-run connection form.
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun OnboardingView(secrets: SecretsStore, onSaved: () -> Unit) {
    var endpoint by remember { mutableStateOf(secrets.endpoint.value) }
    var password by remember { mutableStateOf(secrets.password.value) }

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
            Spacer(Modifier.height(20.dp))
            Button(
                onClick = {
                    secrets.setEndpoint(endpoint.trim())
                    secrets.setPassword(password)
                    secrets.persist()
                    onSaved()
                },
                enabled = endpoint.isNotEmpty() && password.isNotEmpty(),
                modifier = Modifier.fillMaxWidth(),
            ) { Text("Save & Continue") }
        }
    }
}
