// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.Language
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import cloud.offthe.otc.i18n.LanguageSettings
import cloud.offthe.otc.i18n.Languages
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appSettingsLanguage
import cloud.offthe.otc.i18n.appSettingsLanguageAutomaticNamed
import cloud.offthe.otc.i18n.appSettingsLanguageFooter
import cloud.offthe.otc.i18n.appSettingsLanguageThisPhone
import cloud.offthe.otc.i18n.commonDone

/*
 * Port of LanguageView.swift: Settings > Language (docs/i18n.md, "The
 * stored choice") - Automatic, with the language it gives, and every
 * language of this build by its own name, which is never translated. A
 * pick switches the app at once (AppCompat recreates the Activity; Settings
 * keeps this screen open with rememberSaveable) and is kept on the device
 * for every app (LanguageSettings). Offered only while the build has more
 * than one language (LanguageSettings.pickerShown).
 *
 * LanguageMenu is the same list on the screens with no device signed in
 * to, where Settings can't be reached: the welcome screen, the Bluetooth
 * setup, and the "can't connect" and "isn't available" cards.
 */

/** Full screen, over Settings - the counterpart of iOS's pushed list. */
@Composable
fun LanguageView(onClose: () -> Unit) {
    val context = LocalContext.current
    val choice by LanguageSettings.choice.collectAsState()
    val keepsIt by LanguageSettings.deviceKeepsIt.collectAsState()
    val selected = LanguageSettings.selected(choice.language)
    // A change that came from the device while this screen is open (its
    // answer "changed", another app's choice) is shown at once: there is no
    // form here to lose.
    LaunchedEffect(choice.language) { LanguageSettings.applyFromLanguageScreen(context) }

    Dialog(onDismissRequest = onClose, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Column(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.surface).systemBarsPadding()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(S.appSettingsLanguage().resolve(), style = MaterialTheme.typography.titleLarge, modifier = Modifier.weight(1f).padding(start = 8.dp))
                TextButton(onClick = onClose) { Text(S.commonDone().resolve()) }
            }
            Column(Modifier.weight(1f).fillMaxWidth().verticalScroll(rememberScrollState())) {
                val automatic = LanguageSettings.nameOf(LanguageSettings.systemLanguage(context))
                LanguageRow(S.appSettingsLanguageAutomaticNamed(automatic).resolve(), selected == "") {
                    LanguageSettings.choose(context, "")
                }
                for (language in Languages.all) {
                    HorizontalDivider(Modifier.padding(horizontal = 16.dp))
                    LanguageRow(language.name, selected == language.code) {
                        LanguageSettings.choose(context, language.code)
                    }
                }
                Text(
                    S.appSettingsLanguageFooter().resolve(),
                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 12.dp),
                )
                if (keepsIt == false) {
                    Text(
                        S.appSettingsLanguageThisPhone().resolve(),
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp),
                    )
                }
            }
        }
    }
}

@Composable
private fun LanguageRow(name: String, selected: Boolean, onClick: () -> Unit) {
    Row(
        Modifier.fillMaxWidth()
            .selectable(selected = selected, role = Role.RadioButton, onClick = { if (!selected) onClick() })
            .padding(horizontal = 16.dp, vertical = 14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(name, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f))
        if (selected) Icon(Icons.Default.Check, contentDescription = null, tint = MaterialTheme.colorScheme.primary)
    }
}

/**
 * A globe and the name of the language the app shows, opening Settings'
 * list (Automatic with the language it gives, then each language by its
 * own name) - for the screens with no device signed in to: the welcome
 * screen, the Bluetooth setup, the "can't connect" and "isn't available"
 * cards (docs/i18n.md, "The stored choice": a pick there is shown at once
 * and goes to the device at the next sign-in). TalkBack calls it
 * "Language", with the language as its state. Hidden while English is the
 * only language that ships. LanguageMenu in LanguageView.swift is the same.
 */
@Composable
fun LanguageMenu(modifier: Modifier = Modifier, textStyle: TextStyle = MaterialTheme.typography.labelLarge) {
    if (!LanguageSettings.pickerShown) return
    val context = LocalContext.current
    val choice by LanguageSettings.choice.collectAsState()
    val wire by LanguageSettings.wire.collectAsState()
    val selected = LanguageSettings.selected(choice.language)
    // The language on screen, by its own name.
    val shown = LanguageSettings.nameOf(wire)
    val label = S.appSettingsLanguage().resolve()
    var open by remember { mutableStateOf(false) }
    fun pick(code: String) {
        open = false
        if (code != selected) LanguageSettings.choose(context, code)
    }
    Box(modifier) {
        TextButton(onClick = { open = true }, modifier = Modifier.semantics { contentDescription = label; stateDescription = shown }) {
            Icon(Icons.Default.Language, contentDescription = null, modifier = Modifier.size(18.dp))
            Spacer(Modifier.width(6.dp))
            Text(shown, style = textStyle, maxLines = 1, softWrap = false, modifier = Modifier.clearAndSetSemantics {})
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            val automatic = LanguageSettings.nameOf(LanguageSettings.systemLanguage(context))
            MenuRow(S.appSettingsLanguageAutomaticNamed(automatic).resolve(), selected == "") { pick("") }
            for (language in Languages.all) MenuRow(language.name, selected == language.code) { pick(language.code) }
        }
    }
}

@Composable
private fun MenuRow(name: String, selected: Boolean, onClick: () -> Unit) {
    DropdownMenuItem(
        text = { Text(name) },
        onClick = onClick,
        // The mark where Settings' list has it, room kept on every row.
        trailingIcon = { Icon(Icons.Default.Check, contentDescription = null, tint = if (selected) MaterialTheme.colorScheme.primary else androidx.compose.ui.graphics.Color.Transparent) },
        modifier = Modifier.semantics { this.selected = selected },
    )
}
