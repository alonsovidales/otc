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
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.proto.BackgroundTask
import cloud.offthe.otc.proto.Status
import cloud.offthe.otc.ui.settings.BackgroundWork
import cloud.offthe.otc.ui.settings.BackgroundWorkSection
import cloud.offthe.otc.ui.theme.OTCTheme
import kotlinx.coroutines.delay

/**
 * Debug builds only: Settings > Background work on sample tasks, no device
 * needed (adb shell am start -n cloud.offthe.otc/.BackgroundWorkPreviewActivity).
 * The rows are a pretend Status answer read through BackgroundWork, as
 * Settings reads the device's; the first one moves on every second, and
 * one doesn't know its total yet (an indeterminate bar, no count).
 */
class BackgroundWorkPreviewActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        setContent {
            OTCTheme {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    var described by remember { mutableLongStateOf(4_210L) }
                    LaunchedEffect(Unit) {
                        while (described < 12_800L) { delay(1_000); described = minOf(12_800L, described + 137) }
                    }
                    val status = Status.newBuilder()
                        .addBackgroundTasks(task("analysis", "Looking at what you uploaded", 3, 17))
                        .addBackgroundTasks(task("smart_search", "Getting to know your photos", described, 12_800))
                        .addBackgroundTasks(task("thumbnails", "Making the small pictures for the grid", 950, 1_200))
                        .addBackgroundTasks(task("places", "Finding where your photos were taken", 0, 0))
                        .build()
                    Column(Modifier.fillMaxSize().systemBarsPadding().verticalScroll(rememberScrollState()).padding(vertical = 16.dp)) {
                        BackgroundWorkSection(BackgroundWork.tasks(status))
                    }
                }
            }
        }
    }

    private fun task(kind: String, text: String, done: Long, total: Long) =
        BackgroundTask.newBuilder().setKind(kind).setText(text).setDone(done).setTotal(total).build()
}
