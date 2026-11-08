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
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
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
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableDoubleStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.Saver
import androidx.compose.runtime.saveable.listSaver
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
import androidx.media3.ui.PlayerView
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidTrimCant
import cloud.offthe.otc.i18n.androidTrimPlayCut
import cloud.offthe.otc.i18n.appPhotosEditSaving
import cloud.offthe.otc.i18n.appSocialTrimCantOpen
import cloud.offthe.otc.i18n.appSocialTrimKeeping
import cloud.offthe.otc.i18n.appSocialTrimNoLength
import cloud.offthe.otc.i18n.appSocialTrimReading
import cloud.offthe.otc.i18n.appSocialTrimRemove
import cloud.offthe.otc.i18n.appSocialTrimTitle
import cloud.offthe.otc.i18n.commonApply
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonSave
import cloud.offthe.otc.net.MediaStream
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
// clip that goes into a post; the cut itself happens on the device. The
// viewer's Edit on a video (VideoEditor.kt) uses it too, with Save in
// place of Apply (TrimCut).
data class TrimRange(val start: Double, val end: Double) { val length get() = maxOf(0.0, end - start) }

private const val minTrimSecs = 0.5

/**
 * How near a clip's start and end a handle still keeps them: within it the
 * cut is the whole clip, and an end there is the clip's end - the device's
 * own slack for a cut's end (edit_video.go's cutRange).
 */
internal const val TRIM_WHOLE_SLACK_SECS = 0.05

/** TalkBack's step for a trimmer handle: a hundredth of the clip, in tenths of a second, at least one (a minute's clip moves 0.6 s a swipe) - iOS's DualRangeSlider.step. */
fun trimStep(duration: Double): Double = maxOf(0.1, Math.round(duration / 10).toDouble() / 10)

fun formatTimecode(secs: Double): String {
    val s = if (secs.isFinite() && secs > 0) secs else 0.0
    return "%d:%02d.%d".format((s.toInt() / 60), (s.toInt() % 60), ((s * 10).toInt() % 10))
}

/**
 * Whether the handles at [start]..[end] seconds (an end of 0: the clip's
 * end) keep the whole of a clip [length] seconds long - the end within
 * [TRIM_WHOLE_SLACK_SECS] of it. A length not known yet is the whole clip:
 * nothing has been cut.
 */
fun trimKeepsWhole(start: Double, end: Double, length: Double?): Boolean {
    if (length == null) return true
    val to = if (end > 0) end else length
    return start <= 0 && to >= length - TRIM_WHOLE_SLACK_SECS
}

/**
 * Whether [start]..[end] of a clip [length] long is a cut to apply (the
 * composer) or save (the viewer's cut): not the whole clip, and keeping at
 * least half a second - the device refuses less.
 */
fun trimApplies(start: Double, end: Double, length: Double?): Boolean {
    if (length == null || trimKeepsWhole(start, end, length)) return false
    val to = if (end > 0) end else length
    return maxOf(0.0, to - start) >= minTrimSecs
}

/**
 * The trimmer's two handles, in seconds from the clip's start (an end of 0
 * until the clip's length is known), and that length once the player has
 * read it. The caller's to keep: the composer's for one opening, the
 * viewer's cut through the screen being made again (rememberSaveable with
 * [Saver]; the length is read again).
 */
@Stable
class TrimHandles(start: Double = 0.0, end: Double = 0.0) {
    var start by mutableDoubleStateOf(start)
    var end by mutableDoubleStateOf(end)
    var length by mutableStateOf<Double?>(null)

    companion object {
        val Saver: Saver<TrimHandles, Any> = listSaver(save = { listOf(it.start, it.end) }, restore = { TrimHandles(it[0], it[1]) })
    }
}

/**
 * The trimmer as the viewer's Edit on a video uses it (VideoEditor.kt): its
 * bar's Save in place of Apply - [saving]: "Saving…", and nothing to move
 * or press until the device answers -, no Remove trim, [note] in small
 * print under the slider, and [failed]: why the video can't be played here
 * (the viewer couldn't). [dialogs]: the caller's questions, drawn over it.
 */
class TrimCut(val saving: Boolean, val note: UiText, val failed: UiText?, val dialogs: @Composable () -> Unit)

/**
 * The trimmer over [uri] (null: none to play yet - the viewer's stream on
 * its way, or nothing it could play), from [existing] or the whole clip:
 * the video, the two handles with their times, "Keeping … of …", and Play
 * cut. Apply hands [onApply] the cut, Remove trim null; Cancel and Back
 * are [onCancel]. [cut]: the viewer's cut in place of the composer's
 * (TrimCut) - Save is [onApply] there.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun VideoTrimmerView(
    uri: String?, existing: TrimRange?, onApply: (TrimRange?) -> Unit, onCancel: () -> Unit,
    handles: TrimHandles = remember { TrimHandles(existing?.start ?: 0.0, existing?.end ?: 0.0) },
    cut: TrimCut? = null,
) {
    val context = LocalContext.current
    // On the device's own origin at home, through the pinned client
    // (MediaStream.player): the viewer's stream URL may be one.
    val player = remember(uri) { uri?.let { u -> MediaStream.player(context, u).apply { setMediaItem(MediaItem.fromUri(u)); prepare() } } }
    // Paused when the app leaves the screen.
    cloud.offthe.otc.ui.common.PauseWhenStopped(player)
    var playingCut by remember { mutableStateOf(false) }
    // Why the clip can't be trimmed, worded when shown.
    var loadError by remember { mutableStateOf<UiText?>(null) }
    val saving = cut?.saving == true

    DisposableEffect(player) {
        if (player == null) return@DisposableEffect onDispose {}
        val l = object : Player.Listener {
            override fun onPlaybackStateChanged(state: Int) {
                if (state == Player.STATE_READY && handles.length == null) {
                    val d = player.duration
                    if (d > 0) { handles.length = d / 1000.0; if (handles.end <= 0 || handles.end > d / 1000.0) handles.end = d / 1000.0 }
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
            val p = player ?: break
            if (p.currentPosition / 1000.0 >= handles.end) { p.pause(); playingCut = false }
            delay(50)
        }
    }

    val start = handles.start
    val effectiveEnd = if (handles.end > 0) handles.end else (handles.length ?: 0.0)
    val cutLength = maxOf(0.0, effectiveEnd - start)
    val applies = trimApplies(start, handles.end, handles.length)

    Dialog(onDismissRequest = onCancel, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Scaffold(topBar = {
            TopAppBar(title = { Text(S.appSocialTrimTitle().resolve()) }, navigationIcon = { TextButton(onClick = onCancel, enabled = !saving) { Text(S.commonCancel().resolve()) } },
                actions = {
                    if (cut == null) TextButton(onClick = { onApply(TrimRange(handles.start, effectiveEnd)) }, enabled = applies) { Text(S.commonApply().resolve()) }
                    else TrimSave(saving, enabled = applies && !saving) { onApply(TrimRange(handles.start, effectiveEnd)) }
                })
        }) { pad ->
            Column(Modifier.padding(pad).padding(16.dp)) {
                AndroidView(factory = { PlayerView(it).apply { this.player = player; useController = true } },
                    update = { it.player = player },
                    modifier = Modifier.fillMaxWidth().height(260.dp).clip(RoundedCornerShape(10.dp)))
                Spacer(Modifier.height(14.dp))
                val d = handles.length
                val err = cut?.failed ?: loadError
                when {
                    d != null -> {
                        Row(Modifier.fillMaxWidth(), verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
                            Text(formatTimecode(start), style = MaterialTheme.typography.bodySmall, modifier = Modifier.width(52.dp))
                            val setRange = { r: ClosedFloatingPointRange<Float> ->
                                val ns = r.start.toDouble().coerceIn(0.0, maxOf(0.0, effectiveEnd - minTrimSecs))
                                val ne = r.endInclusive.toDouble().coerceIn(handles.start + minTrimSecs, d)
                                if (ns != handles.start) { handles.start = ns; playingCut = false; player?.pause(); player?.seekTo((ns * 1000).toLong()) }
                                if (ne != handles.end) { handles.end = ne; playingCut = false; player?.pause(); player?.seekTo((ne * 1000).toLong()) }
                            }
                            Box(Modifier.weight(1f)) {
                                // The drag is the slider's; TalkBack gets each handle
                                // as one element of its own, named, read as a time,
                                // and stepped a hundredth of the clip (trimStep) -
                                // the slider's own thumbs said only a percentage.
                                // Nothing moves while the cut is being saved.
                                RangeSlider(
                                    value = start.toFloat()..effectiveEnd.toFloat(), valueRange = 0f..d.toFloat(),
                                    onValueChange = setRange, enabled = !saving,
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
                                        setProgress { v -> if (saving) false else { setRange(v..effectiveEnd.toFloat()); true } }
                                    })
                                    Box(Modifier.weight(1f).fillMaxHeight().semantics {
                                        contentDescription = endName
                                        stateDescription = formatTimecode(effectiveEnd)
                                        progressBarRangeInfo = ProgressBarRangeInfo(effectiveEnd.toFloat(), 0f..d.toFloat(), stepCount)
                                        setProgress { v -> if (saving) false else { setRange(handles.start.toFloat()..v); true } }
                                    })
                                }
                            }
                            Text(formatTimecode(effectiveEnd), style = MaterialTheme.typography.bodySmall, modifier = Modifier.width(52.dp))
                        }
                        Text(S.appSocialTrimKeeping(formatTimecode(cutLength), formatTimecode(d)).resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        // The viewer's cut: the device copies the video as it
                        // is, so the cut starts at a keyframe - said here.
                        cut?.let { Text(it.note.resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 4.dp)) }
                        Spacer(Modifier.height(8.dp))
                        // Whole buttons on a second line when both don't fit
                        // (Dutch, French at 360 dp), rather than one squeezed.
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(onClick = { player?.seekTo((handles.start * 1000).toLong()); playingCut = true; player?.play() }, enabled = !saving) { Text(S.androidTrimPlayCut().resolve()) }
                            if (existing != null && cut == null) OutlinedButton(onClick = { onApply(null) }) { Text(S.appSocialTrimRemove().resolve()) }
                        }
                    }
                    err != null -> Text(S.androidTrimCant(err.resolve()).resolve(), color = MaterialTheme.colorScheme.error)
                    else -> Row { CircularProgressIndicator(Modifier.width(20.dp), strokeWidth = 2.dp); Spacer(Modifier.width(8.dp)); Text(S.appSocialTrimReading().resolve()) }
                }
            }
        }
        cut?.dialogs?.invoke()
    }
}

/**
 * The viewer's cut's Save, in the bar's place of Apply; [saving]:
 * "Saving…" with a spinner, legible but not to be tapped, until the device
 * answers - as the photo editor's.
 */
@Composable
private fun TrimSave(saving: Boolean, enabled: Boolean, onClick: () -> Unit) {
    val primary = MaterialTheme.colorScheme.primary
    TextButton(
        onClick = onClick, enabled = enabled,
        colors = if (saving) ButtonDefaults.textButtonColors(disabledContentColor = primary) else ButtonDefaults.textButtonColors(),
    ) {
        if (saving) {
            CircularProgressIndicator(Modifier.size(16.dp), color = primary, strokeWidth = 2.dp)
            Spacer(Modifier.width(8.dp))
            Text(S.appPhotosEditSaving().resolve())
        } else Text(S.commonSave().resolve())
    }
}
