// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.data.SmartSearchModelState
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosViewerInfoTitle
import cloud.offthe.otc.ui.gallery.PhotoTextSection
import cloud.offthe.otc.ui.settings.PeopleSection
import cloud.offthe.otc.ui.settings.SmartSearchSection
import cloud.offthe.otc.ui.settings.TextInPhotosSection
import cloud.offthe.otc.ui.theme.OTCTheme

/**
 * Debug builds only: Text in photos and Pets (device release 180) on sample
 * data, no device needed - Settings' People section with Pets under Face
 * Recognition (as a new device has them: face recognition off, Pets on),
 * then Smart search's section and Text in photos' after it (turning either
 * off asks first); `--es show info` Info's Text in this photo with Copy.
 */
class TextInPhotosPreviewActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        val info = intent.getStringExtra("show") == "info"
        setContent {
            OTCTheme {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    Column(Modifier.fillMaxSize().systemBarsPadding().verticalScroll(rememberScrollState()).padding(if (info) 16.dp else 0.dp)) {
                        if (info) InfoPreview() else SettingsPreview()
                    }
                }
            }
        }
    }

    @Composable
    private fun SettingsPreview() {
        var faces by remember { mutableStateOf(false) }
        var pets by remember { mutableStateOf(true) }
        var smart by remember { mutableStateOf(true) }
        var smartAsking by remember { mutableStateOf(false) }
        var text by remember { mutableStateOf(true) }
        var textAsking by remember { mutableStateOf(false) }
        PeopleSection(faces, savingFaces = false, onFaces = { faces = it }, pets, savingPets = false, onPets = { pets = it })
        SmartSearchSection(
            smart, saving = false, confirmOff = smartAsking, model = SmartSearchModelState.Ready,
            onToggle = { v -> if (v) smart = true else smartAsking = true },
            onConfirmOff = { smartAsking = false; smart = false }, onCancelOff = { smartAsking = false },
        )
        TextInPhotosSection(
            text, saving = false, confirmOff = textAsking,
            onToggle = { v -> if (v) text = true else textAsking = true },
            onConfirmOff = { textAsking = false; text = false }, onCancelOff = { textAsking = false },
        )
    }

    @Composable
    private fun InfoPreview() {
        Text(S.appPhotosViewerInfoTitle().resolve(), style = MaterialTheme.typography.titleMedium)
        PhotoTextSection("GTS-04-R\nNO PARKING\nMon-Fri 8:00-20:00\nTow-away zone")
    }
}
