// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.content.Context
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.androidSettingsSyncCloud
import cloud.offthe.otc.i18n.appSettingsSyncChoiceAll
import cloud.offthe.otc.i18n.appSettingsSyncChoiceAllDetail
import cloud.offthe.otc.i18n.appSettingsSyncChoiceFooter
import cloud.offthe.otc.i18n.appSettingsSyncChoiceIntro
import cloud.offthe.otc.i18n.appSettingsSyncChoiceNew
import cloud.offthe.otc.i18n.appSettingsSyncChoiceNewDetail
import cloud.offthe.otc.i18n.appSettingsSyncChoiceStart
import cloud.offthe.otc.i18n.appSettingsSyncChoiceTitle
import cloud.offthe.otc.i18n.appSettingsSyncChoiceWarning
import cloud.offthe.otc.i18n.appSettingsSyncVideos
import cloud.offthe.otc.i18n.appSettingsSyncWifiOnly
import cloud.offthe.otc.sync.PhotoSync
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * Port of SyncChoice.swift: what the photo sync sends from this phone, asked
 * after every sign-in to a device (Onboarding's Save & Continue and the
 * Bluetooth setup) - the whole library or only what comes from now on, with
 * the sync's own switches. Until it is answered no sync runs
 * (PhotoSync.runForeground returns straight away), so a library is never
 * uploaded to a device before its owner chose to.
 */
object SyncChoice {
    private const val KEY = "syncChoicePending"
    // The same file as PhotoSync's watermark.
    private val prefs get() = OTCApp.instance.getSharedPreferences("otc_sync", Context.MODE_PRIVATE)
    private val state by lazy { MutableStateFlow(prefs.getBoolean(KEY, false)) }

    /** RootView shows SyncChoiceView while this is true. */
    val pending: StateFlow<Boolean> get() = state

    /** Called right before a sign-in's credentials are saved, so the sheet is
     * up as the app opens and no sync starts first. */
    fun ask() {
        prefs.edit().putBoolean(KEY, true).apply()
        state.value = true
    }

    /** The answer: the same as Settings' Sync All (`all`) or Sync From Now.
     * The sync itself is started by the caller. */
    fun answer(all: Boolean) {
        PhotoSync.setWatermark(if (all) 0 else System.currentTimeMillis())
        prefs.edit().putBoolean(KEY, false).apply()
        state.value = false
    }
}

/** The screen asking SyncChoice's question. Back doesn't close it: the sync
 * waits for its answer. */
@Composable
fun SyncChoiceView(secrets: SecretsStore, onDone: () -> Unit) {
    var all by rememberSaveable { mutableStateOf(true) }
    val wifiOnly by secrets.wifiOnly.collectAsState()
    val includeVideos by secrets.includeVideos.collectAsState()
    val fromCloud by secrets.downloadFromCloud.collectAsState()

    Dialog(
        onDismissRequest = {},
        properties = DialogProperties(usePlatformDefaultWidth = false, dismissOnBackPress = false, dismissOnClickOutside = false),
    ) {
        // A Surface, not a plain background: RootView calls this outside
        // MainView's, and without one the text takes the default black
        // content colour, unreadable in dark mode.
        Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
            Box(
                Modifier.fillMaxSize().systemBarsPadding().verticalScroll(rememberScrollState()),
                contentAlignment = Alignment.TopCenter,
            ) {
                // A readable width on a foldable's or a tablet's wide screen.
                Column(Modifier.widthIn(max = 640.dp).fillMaxWidth().padding(20.dp)) {
                    Text(S.appSettingsSyncChoiceTitle().resolve(), style = MaterialTheme.typography.headlineSmall)
                    Spacer(Modifier.height(8.dp))
                    Text(S.appSettingsSyncChoiceIntro().resolve(), style = MaterialTheme.typography.bodyLarge)
                    Spacer(Modifier.height(12.dp))
                    Column(Modifier.selectableGroup()) {
                        ChoiceRow(S.appSettingsSyncChoiceAll().resolve(), S.appSettingsSyncChoiceAllDetail().resolve(), all) { all = true }
                        ChoiceRow(S.appSettingsSyncChoiceNew().resolve(), S.appSettingsSyncChoiceNewDetail().resolve(), !all) { all = false }
                    }
                    Spacer(Modifier.height(20.dp))
                    // The switches' heading, as on iOS.
                    Text(
                        S.appSettingsSyncChoiceWarning().resolve(),
                        style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    Spacer(Modifier.height(8.dp))
                    SwitchRow(S.appSettingsSyncWifiOnly().resolve(), wifiOnly, secrets::setWifiOnly)
                    SwitchRow(S.appSettingsSyncVideos().resolve(), includeVideos, secrets::setIncludeVideos)
                    SwitchRow(S.androidSettingsSyncCloud().resolve(), fromCloud, secrets::setDownloadFromCloud)
                    Spacer(Modifier.height(4.dp))
                    Text(
                        S.appSettingsSyncChoiceFooter().resolve(),
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    Spacer(Modifier.height(24.dp))
                    Button(
                        onClick = {
                            secrets.persist()
                            SyncChoice.answer(all)
                            onDone()
                        },
                        modifier = Modifier.fillMaxWidth(),
                    ) { Text(S.appSettingsSyncChoiceStart().resolve()) }
                }
            }
        }
    }
}

@Composable
private fun ChoiceRow(title: String, detail: String, selected: Boolean, onPick: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().selectable(selected = selected, onClick = onPick, role = Role.RadioButton).padding(vertical = 8.dp),
        verticalAlignment = Alignment.Top,
    ) {
        // 24 dp tall, as the title's line: level with the title.
        RadioButton(selected = selected, onClick = null)
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.bodyLarge)
            Text(detail, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

@Composable
private fun SwitchRow(label: String, checked: Boolean, onChange: (Boolean) -> Unit) {
    Row(
        Modifier.fillMaxWidth().toggleable(value = checked, onValueChange = onChange, role = Role.Switch).padding(vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(label, Modifier.weight(1f), style = MaterialTheme.typography.bodyLarge)
        Switch(checked = checked, onCheckedChange = null)
    }
}
