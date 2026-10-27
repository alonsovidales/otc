// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appShellConnectTitle
import cloud.offthe.otc.i18n.appShellOnboardingLastDevice
import cloud.offthe.otc.i18n.appShellOnboardingPending
import cloud.offthe.otc.i18n.appShellOnboardingSetupFooter
import cloud.offthe.otc.i18n.appShellPrivacy
import cloud.offthe.otc.i18n.appShellSaveContinue
import cloud.offthe.otc.i18n.appShellSetupNewDevice
import cloud.offthe.otc.i18n.appShellWelcome
import cloud.offthe.otc.i18n.commonPassword
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
import cloud.offthe.otc.ui.common.rememberConnectionForm
import cloud.offthe.otc.ui.settings.LanguageMenu

// Port of OnboardingView.swift: the first-run connection form.
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun OnboardingView(secrets: SecretsStore, onSaved: () -> Unit) {
    // A device set up from this phone fills the form (its install may have
    // outlived the Bluetooth link that would have signed in straight away).
    val pending = remember { if (secrets.isConfigured) null else SecretsStore.pendingSetup() }
    // Or the device this phone left while it was down (logOut keepDevice).
    val last = remember { if (secrets.isConfigured || pending != null) null else SecretsStore.lastDevice() }
    // What is typed survives the recreation a pick in the language menu
    // brings (a view model: the password stays out of saved state).
    val form = rememberConnectionForm(
        "onboarding",
        endpoint = { pending?.first ?: last?.first ?: secrets.endpoint.value },
        password = { pending?.second ?: last?.second ?: secrets.password.value },
    )
    val endpoint by form::endpoint
    var password by form::password
    // Issue #137: a brand-new device is set up from here over Bluetooth;
    // when it is done its address lands in the form and the first sign in
    // sets the owner password.
    var setupOpen by remember { mutableStateOf(false) }
    if (setupOpen) {
        BluetoothSetupView(
            onUseDevice = { newEndpoint, newPassword ->
                // Straight in, as iOS: save both and open the app - what to
                // sync is asked first, so no sync starts before it (SyncChoice).
                SyncChoice.ask()
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

    Scaffold(topBar = {
        TopAppBar(
            title = { Text(S.appShellWelcome().resolve()) },
            // The language, before any device: Settings, where it lives,
            // needs one. Kept for the device signed in to next.
            actions = { LanguageMenu() },
        )
    }) { pad ->
        // Scrolls, above the keyboard (the window is edge to edge): Save &
        // Continue stayed under the keyboard on a small phone, and at a big
        // font size the buttons ran off the screen.
        Column(Modifier.padding(pad).consumeWindowInsets(pad).fillMaxSize().imePadding().verticalScroll(rememberScrollState()).padding(16.dp)) {
            Text(S.appShellConnectTitle().resolve(), style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Spacer(Modifier.height(8.dp))
            ConnectionEndpointFields(form.entry)
            Spacer(Modifier.height(8.dp))
            OTCTextField(
                value = password, onValueChange = { password = it }, label = { Text(S.commonPassword().resolve()) },
                singleLine = true, visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                modifier = Modifier.fillMaxWidth(),
            )
            if (last != null) {
                Spacer(Modifier.height(6.dp))
                Text(
                    S.appShellOnboardingLastDevice().resolve(),
                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            if (pending != null) {
                Spacer(Modifier.height(6.dp))
                Text(
                    S.appShellOnboardingPending().resolve(),
                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Spacer(Modifier.height(20.dp))
            Button(
                onClick = {
                    // Asked first, so no sync starts before it (SyncChoice).
                    SyncChoice.ask()
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
            ) { Text(S.appShellSaveContinue().resolve()) }
            Spacer(Modifier.height(28.dp))
            androidx.compose.material3.OutlinedButton(onClick = { setupOpen = true }, modifier = Modifier.fillMaxWidth()) {
                Text(S.appShellSetupNewDevice().resolve())
            }
            Spacer(Modifier.height(6.dp))
            Text(
                S.appShellOnboardingSetupFooter().resolve(),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            // Issue #175: what the bridge keeps, before signing in.
            val context = androidx.compose.ui.platform.LocalContext.current
            androidx.compose.material3.TextButton(onClick = { cloud.offthe.otc.ui.common.Share.openInBrowser(context, "https://off-the.cloud/privacy") }) { Text(S.appShellPrivacy().resolve()) }
        }
    }
}
