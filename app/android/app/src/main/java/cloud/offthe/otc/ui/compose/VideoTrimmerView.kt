// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.compose

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.RangeSlider
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.media3.common.MediaItem
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.ui.PlayerView
import kotlinx.coroutines.delay

// Port of VideoTrimmerView.swift (issue #108): pick the start/end of the
// clip that goes into a post; the cut itself happens on the device.
data class TrimRange(val start: Double, val end: Double) { val length get() = maxOf(0.0, end - start) }

private const val minTrimSecs = 0.5

fun formatTimecode(secs: Double): String {
    val s = if (secs.isFinite() && secs > 0) secs else 0.0
    return "%d:%02d.%d".format((s.toInt() / 60), (s.toInt() % 60), ((s * 10).toInt() % 10))
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun VideoTrimmerView(uri: String, existing: TrimRange?, onApply: (TrimRange?) -> Unit, onCancel: () -> Unit) {
    val context = LocalContext.current
    val player = remember { ExoPlayer.Builder(context).build().apply { setMediaItem(MediaItem.fromUri(uri)); prepare() } }
    var duration by remember { mutableStateOf<Double?>(null) }
    var start by remember { mutableStateOf(existing?.start ?: 0.0) }
    var end by remember { mutableStateOf(existing?.end ?: 0.0) }
    var playingCut by remember { mutableStateOf(false) }
    var loadError by remember { mutableStateOf<String?>(null) }

    DisposableEffect(Unit) {
        val l = object : Player.Listener {
            override fun onPlaybackStateChanged(state: Int) {
                if (state == Player.STATE_READY && duration == null) {
                    val d = player.duration
                    if (d > 0) { duration = d / 1000.0; if (end <= 0 || end > d / 1000.0) end = d / 1000.0 }
                    else loadError = "This video's length could not be read."
                }
            }
            override fun onPlayerError(error: androidx.media3.common.PlaybackException) { loadError = "This video could not be opened." }
        }
        player.addListener(l)
        onDispose { player.removeListener(l); player.release() }
    }
    LaunchedEffect(playingCut) {
        while (playingCut) {
            if (player.currentPosition / 1000.0 >= end) { player.pause(); playingCut = false }
            delay(50)
        }
    }

    val effectiveEnd = if (end > 0) end else (duration ?: 0.0)
    val cutLength = maxOf(0.0, effectiveEnd - start)
    val isWholeClip = duration?.let { start <= 0 && effectiveEnd >= it - 0.05 } ?: true

    Dialog(onDismissRequest = onCancel, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Scaffold(topBar = {
            TopAppBar(title = { Text("Trim video") }, navigationIcon = { TextButton(onClick = onCancel) { Text("Cancel") } },
                actions = { TextButton(onClick = { onApply(TrimRange(start, effectiveEnd)) }, enabled = !isWholeClip && cutLength >= minTrimSecs) { Text("Apply") } })
        }) { pad ->
            Column(Modifier.padding(pad).padding(16.dp)) {
                AndroidView(factory = { PlayerView(it).apply { this.player = player; useController = true } },
                    modifier = Modifier.fillMaxWidth().height(260.dp).clip(RoundedCornerShape(10.dp)))
                Spacer(Modifier.height(14.dp))
                val d = duration
                when {
                    d != null -> {
                        Row(Modifier.fillMaxWidth(), verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
                            Text(formatTimecode(start), style = MaterialTheme.typography.bodySmall, modifier = Modifier.width(52.dp))
                            RangeSlider(
                                value = start.toFloat()..effectiveEnd.toFloat(), valueRange = 0f..d.toFloat(),
                                onValueChange = { r ->
                                    val ns = r.start.toDouble().coerceIn(0.0, maxOf(0.0, effectiveEnd - minTrimSecs))
                                    val ne = r.endInclusive.toDouble().coerceIn(start + minTrimSecs, d)
                                    if (ns != start) { start = ns; playingCut = false; player.pause(); player.seekTo((ns * 1000).toLong()) }
                                    if (ne != end) { end = ne; playingCut = false; player.pause(); player.seekTo((ne * 1000).toLong()) }
                                },
                                modifier = Modifier.weight(1f),
                            )
                            Text(formatTimecode(effectiveEnd), style = MaterialTheme.typography.bodySmall, modifier = Modifier.width(52.dp))
                        }
                        Text("Keeping ${formatTimecode(cutLength)} of ${formatTimecode(d)}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        Spacer(Modifier.height(8.dp))
                        Row {
                            OutlinedButton(onClick = { player.seekTo((start * 1000).toLong()); playingCut = true; player.play() }) { Text("▶ Play cut") }
                            if (existing != null) { Spacer(Modifier.width(8.dp)); OutlinedButton(onClick = { onApply(null) }) { Text("Remove trim") } }
                        }
                    }
                    loadError != null -> Text("Can't trim this video: $loadError", color = MaterialTheme.colorScheme.error)
                    else -> Row { CircularProgressIndicator(Modifier.width(20.dp), strokeWidth = 2.dp); Spacer(Modifier.width(8.dp)); Text("Reading the clip…") }
                }
            }
        }
    }
}
