// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.compose

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.FlowRow
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
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidTrimCant
import cloud.offthe.otc.i18n.androidTrimPlayCut
import cloud.offthe.otc.i18n.appSocialTrimCantOpen
import cloud.offthe.otc.i18n.appSocialTrimKeeping
import cloud.offthe.otc.i18n.appSocialTrimNoLength
import cloud.offthe.otc.i18n.appSocialTrimReading
import cloud.offthe.otc.i18n.appSocialTrimRemove
import cloud.offthe.otc.i18n.appSocialTrimTitle
import cloud.offthe.otc.i18n.commonApply
import cloud.offthe.otc.i18n.commonCancel
import kotlinx.coroutines.delay
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.ui.semantics.ProgressBarRangeInfo
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.progressBarRangeInfo
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.setProgress
import androidx.compose.ui.semantics.stateDescription
import cloud.offthe.otc.i18n.androidTrimEndHandle
import cloud.offthe.otc.i18n.androidTrimStartHandle

// Port of VideoTrimmerView.swift (issue #108): pick the start/end of the
// clip that goes into a post; the cut itself happens on the device.
data class TrimRange(val start: Double, val end: Double) { val length get() = maxOf(0.0, end - start) }

private const val minTrimSecs = 0.5

/** TalkBack's step for a trimmer handle: a hundredth of the clip, in tenths of a second, at least one (a minute's clip moves 0.6 s a swipe) - iOS's DualRangeSlider.step. */
fun trimStep(duration: Double): Double = maxOf(0.1, Math.round(duration / 10).toDouble() / 10)

fun formatTimecode(secs: Double): String {
    val s = if (secs.isFinite() && secs > 0) secs else 0.0
    return "%d:%02d.%d".format((s.toInt() / 60), (s.toInt() % 60), ((s * 10).toInt() % 10))
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun VideoTrimmerView(uri: String, existing: TrimRange?, onApply: (TrimRange?) -> Unit, onCancel: () -> Unit) {
    val context = LocalContext.current
    val player = remember { cloud.offthe.otc.net.MediaStream.playerBuilder(context).build().apply { setMediaItem(MediaItem.fromUri(uri)); prepare() } }
    // Paused when the app leaves the screen.
    cloud.offthe.otc.ui.common.PauseWhenStopped(player)
    var duration by remember { mutableStateOf<Double?>(null) }
    var start by remember { mutableStateOf(existing?.start ?: 0.0) }
    var end by remember { mutableStateOf(existing?.end ?: 0.0) }
    var playingCut by remember { mutableStateOf(false) }
    // Why the clip can't be trimmed, worded when shown.
    var loadError by remember { mutableStateOf<UiText?>(null) }

    DisposableEffect(Unit) {
        val l = object : Player.Listener {
            override fun onPlaybackStateChanged(state: Int) {
                if (state == Player.STATE_READY && duration == null) {
                    val d = player.duration
                    if (d > 0) { duration = d / 1000.0; if (end <= 0 || end > d / 1000.0) end = d / 1000.0 }
                    else loadError = S.appSocialTrimNoLength()
                }
            }
            override fun onPlayerError(error: androidx.media3.common.PlaybackException) { loadError = S.appSocialTrimCantOpen() }
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
            TopAppBar(title = { Text(S.appSocialTrimTitle().resolve()) }, navigationIcon = { TextButton(onClick = onCancel) { Text(S.commonCancel().resolve()) } },
                actions = { TextButton(onClick = { onApply(TrimRange(start, effectiveEnd)) }, enabled = !isWholeClip && cutLength >= minTrimSecs) { Text(S.commonApply().resolve()) } })
        }) { pad ->
            Column(Modifier.padding(pad).padding(16.dp)) {
                AndroidView(factory = { PlayerView(it).apply { this.player = player; useController = true } },
                    modifier = Modifier.fillMaxWidth().height(260.dp).clip(RoundedCornerShape(10.dp)))
                Spacer(Modifier.height(14.dp))
                val d = duration
                val err = loadError
                when {
                    d != null -> {
                        Row(Modifier.fillMaxWidth(), verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
                            Text(formatTimecode(start), style = MaterialTheme.typography.bodySmall, modifier = Modifier.width(52.dp))
                            val setRange = { r: ClosedFloatingPointRange<Float> ->
                                val ns = r.start.toDouble().coerceIn(0.0, maxOf(0.0, effectiveEnd - minTrimSecs))
                                val ne = r.endInclusive.toDouble().coerceIn(start + minTrimSecs, d)
                                if (ns != start) { start = ns; playingCut = false; player.pause(); player.seekTo((ns * 1000).toLong()) }
                                if (ne != end) { end = ne; playingCut = false; player.pause(); player.seekTo((ne * 1000).toLong()) }
                            }
                            Box(Modifier.weight(1f)) {
                                // The drag is the slider's; TalkBack gets each handle
                                // as one element of its own, named, read as a time,
                                // and stepped a hundredth of the clip (trimStep) -
                                // the slider's own thumbs said only a percentage.
                                RangeSlider(
                                    value = start.toFloat()..effectiveEnd.toFloat(), valueRange = 0f..d.toFloat(),
                                    onValueChange = setRange,
                                    modifier = Modifier.fillMaxWidth().clearAndSetSemantics {},
                                )
                                val step = trimStep(d)
                                val stepCount = maxOf(0, Math.round(d / step).toInt() - 1)
                                val startName = S.androidTrimStartHandle().resolve()
                                val endName = S.androidTrimEndHandle().resolve()
                                Row(Modifier.matchParentSize()) {
                                    Box(Modifier.weight(1f).fillMaxHeight().semantics {
                                        contentDescription = startName
                                        stateDescription = formatTimecode(start)
                                        progressBarRangeInfo = ProgressBarRangeInfo(start.toFloat(), 0f..d.toFloat(), stepCount)
                                        setProgress { v -> setRange(v..effectiveEnd.toFloat()); true }
                                    })
                                    Box(Modifier.weight(1f).fillMaxHeight().semantics {
                                        contentDescription = endName
                                        stateDescription = formatTimecode(effectiveEnd)
                                        progressBarRangeInfo = ProgressBarRangeInfo(effectiveEnd.toFloat(), 0f..d.toFloat(), stepCount)
                                        setProgress { v -> setRange(start.toFloat()..v); true }
                                    })
                                }
                            }
                            Text(formatTimecode(effectiveEnd), style = MaterialTheme.typography.bodySmall, modifier = Modifier.width(52.dp))
                        }
                        Text(S.appSocialTrimKeeping(formatTimecode(cutLength), formatTimecode(d)).resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        Spacer(Modifier.height(8.dp))
                        // Whole buttons on a second line when both don't fit
                        // (Dutch, French at 360 dp), rather than one squeezed.
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(onClick = { player.seekTo((start * 1000).toLong()); playingCut = true; player.play() }) { Text(S.androidTrimPlayCut().resolve()) }
                            if (existing != null) OutlinedButton(onClick = { onApply(null) }) { Text(S.appSocialTrimRemove().resolve()) }
                        }
                    }
                    err != null -> Text(S.androidTrimCant(err.resolve()).resolve(), color = MaterialTheme.colorScheme.error)
                    else -> Row { CircularProgressIndicator(Modifier.width(20.dp), strokeWidth = 2.dp); Spacer(Modifier.width(8.dp)); Text(S.appSocialTrimReading().resolve()) }
                }
            }
        }
    }
}
