// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.collapse
import androidx.compose.ui.semantics.expand
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.DialogProperties
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appFilesCompressConfirm
import cloud.offthe.otc.i18n.appFilesCompressDone
import cloud.offthe.otc.i18n.appFilesCompressEstimating
import cloud.offthe.otc.i18n.appFilesCompressFailed
import cloud.offthe.otc.i18n.appFilesCompressJpegResave
import cloud.offthe.otc.i18n.appFilesCompressNeedsUpdate
import cloud.offthe.otc.i18n.appFilesCompressNotOffered
import cloud.offthe.otc.i18n.appFilesCompressOptionsFailed
import cloud.offthe.otc.i18n.appFilesCompressNothing
import cloud.offthe.otc.i18n.appFilesCompressPhoto12mp
import cloud.offthe.otc.i18n.appFilesCompressPngToJpeg
import cloud.offthe.otc.i18n.appFilesCompressProgress
import cloud.offthe.otc.i18n.appFilesCompressRow
import cloud.offthe.otc.i18n.appFilesCompressSlow
import cloud.offthe.otc.i18n.appFilesCompressTitle
import cloud.offthe.otc.i18n.appFilesCompressTotal
import cloud.offthe.otc.i18n.appFilesCompressVideo1080p
import cloud.offthe.otc.i18n.appFilesCompressVideo720p
import cloud.offthe.otc.i18n.appFilesCompressVideoHevc
import cloud.offthe.otc.i18n.appFilesCompressVideoNone
import cloud.offthe.otc.i18n.appFilesCompressVideos
import cloud.offthe.otc.i18n.appFilesCompressWarning
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonClose
import cloud.offthe.otc.i18n.commonDone
import cloud.offthe.otc.i18n.commonOk
import cloud.offthe.otc.i18n.commonTryAgain
import cloud.offthe.otc.proto.CompressJob
import cloud.offthe.otc.ui.common.formatBytes

// The Compress dialog (Compress.kt has its rules and model), shared by
// Images' selection bar and Settings › Largest files: the device's options
// for the picked files - the videos' choice, a switch per photos' preset,
// each with what it comes to - what can't be made smaller and why, what
// the choices free and the warning that the originals go for good; then,
// once Compress is pressed, the job's progress and how it ended. Closed,
// the job goes on on the device.

/** A preset's words in the dialog; null for one this app doesn't know (never shown). */
internal fun compressPresetText(preset: String): UiText? = when (preset) {
    CompressRules.VIDEO_1080P -> S.appFilesCompressVideo1080p()
    CompressRules.VIDEO_720P -> S.appFilesCompressVideo720p()
    CompressRules.VIDEO_HEVC -> S.appFilesCompressVideoHevc()
    CompressRules.VIDEO_NONE -> S.appFilesCompressVideoNone()
    CompressRules.PHOTO_12MP -> S.appFilesCompressPhoto12mp()
    CompressRules.JPEG_RESAVE -> S.appFilesCompressJpegResave()
    CompressRules.PNG_TO_JPEG -> S.appFilesCompressPngToJpeg()
    else -> null
}

/** The line under a choice: "3 files · 2.1 GB → about 640 MB"; none when no file would get it. */
internal fun compressRowText(sum: CompressRules.Sum): UiText? =
    if (sum.count == 0) null else S.appFilesCompressRow(sum.count, formatBytes(sum.size), formatBytes(sum.estimate))

/**
 * How a finished job ended: [done], what it compressed and freed - also
 * when nothing came of it at all - and/or [failed], how many it couldn't
 * compress (both when both).
 */
internal class CompressEnd(val done: UiText?, val failed: UiText?)

/** [CompressEnd] of a [job] that is over ([gone]: the device no longer has it, J4). */
internal fun compressEnd(job: CompressJob, gone: Boolean = false): CompressEnd {
    val done = CompressRules.done(job).size
    val notDone = CompressRules.notDone(job, gone).size
    return CompressEnd(
        if (done > 0 || notDone == 0) S.appFilesCompressDone(done, formatBytes(job.saved)) else null,
        if (notDone > 0) S.appFilesCompressFailed(notDone) else null,
    )
}

/**
 * The Compress dialog over the screen while [model] has it shown. [onSent]:
 * CompressFiles was taken (the screen's selection goes). Back closes it;
 * a tap outside doesn't (the choices and the job's progress are in it).
 */
@Composable
fun CompressDialog(model: CompressModel, onSent: () -> Unit = {}) {
    val st by model.state.collectAsState()
    if (!st.shown) return
    val colors = MaterialTheme.colorScheme
    val job = st.job
    // A refusal is said at the foot, by Compress: scrolled to, once laid out.
    val scroll = rememberScrollState()
    LaunchedEffect(st.refusal) {
        if (st.refusal == null) return@LaunchedEffect
        withFrameNanos {}
        scroll.animateScrollTo(scroll.maxValue)
    }
    AlertDialog(
        onDismissRequest = { model.close() },
        properties = DialogProperties(dismissOnClickOutside = false),
        title = { Text(S.appFilesCompressTitle(st.asked.size).resolve()) },
        text = {
            Column(Modifier.fillMaxWidth().verticalScroll(scroll), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                when (st.phase) {
                    CompressModel.Phase.ESTIMATING -> Row(verticalAlignment = Alignment.CenterVertically) {
                        CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                        Spacer(Modifier.width(12.dp))
                        Text(S.appFilesCompressEstimating().resolve(), Modifier.semantics { liveRegion = LiveRegionMode.Polite })
                    }
                    CompressModel.Phase.FAILED -> Text((st.failure ?: S.appFilesCompressOptionsFailed()).resolve(), Modifier.semantics { liveRegion = LiveRegionMode.Polite })
                    CompressModel.Phase.NEEDS_UPDATE -> Text(S.appFilesCompressNeedsUpdate().resolve())
                    CompressModel.Phase.OPTIONS -> CompressChoices(model, st)
                    CompressModel.Phase.JOB -> if (job != null) CompressJobView(job, st.gone, st.videoSent)
                }
            }
        },
        confirmButton = {
            when (st.phase) {
                CompressModel.Phase.OPTIONS -> TextButton(onClick = { model.compress(onSent) }, enabled = st.canCompress) {
                    if (st.sending) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                    else Text(S.appFilesCompressConfirm().resolve(), color = if (st.canCompress) colors.error else colors.onSurface.copy(alpha = 0.38f))
                }
                CompressModel.Phase.FAILED -> TextButton(onClick = { model.retry() }) { Text(S.commonTryAgain().resolve()) }
                CompressModel.Phase.NEEDS_UPDATE -> TextButton(onClick = { model.close() }) { Text(S.commonOk().resolve()) }
                CompressModel.Phase.JOB -> TextButton(onClick = { model.close() }) {
                    Text((if (st.over) S.commonDone() else S.commonClose()).resolve())
                }
                CompressModel.Phase.ESTIMATING -> TextButton(onClick = { model.close() }) { Text(S.commonCancel().resolve()) }
            }
        },
        dismissButton = if (st.phase == CompressModel.Phase.OPTIONS || st.phase == CompressModel.Phase.FAILED) {
            { TextButton(onClick = { model.close() }) { Text(S.commonCancel().resolve()) } }
        } else null,
    )
}

// The options: the videos' choice and the photos' switches with their
// lines, what can't be made smaller, and the foot.
@Composable
private fun CompressChoices(model: CompressModel, st: CompressModel.State) {
    val colors = MaterialTheme.colorScheme
    val options = st.options
    val videos = CompressRules.videoChoices(options)
    val photos = CompressRules.photoSwitches(options)
    val nothing = CompressRules.nothing(options)
    if (nothing) Text(S.appFilesCompressNothing().resolve())
    if (videos.isNotEmpty()) {
        Text(
            S.appFilesCompressVideos().resolve(), style = MaterialTheme.typography.titleSmall,
            modifier = Modifier.semantics { heading() },
        )
        Column(Modifier.selectableGroup()) {
            for (p in videos) {
                val label = compressPresetText(p) ?: continue
                val line = if (p == CompressRules.VIDEO_NONE) null else compressRowText(CompressRules.videoRow(options, p))
                ChoiceRow(label.resolve(), line?.resolve(), selected = st.video == p, onPick = { model.chooseVideo(p) })
            }
        }
    }
    for (p in photos) {
        val label = compressPresetText(p) ?: continue
        val on = p in st.photosOn
        val line = compressRowText(CompressRules.photoRow(options, p, st.photosOn))
        Row(
            Modifier.fillMaxWidth().heightIn(min = 48.dp).toggleable(value = on, role = Role.Switch, onValueChange = { model.setPhoto(p, it) }),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(Modifier.weight(1f)) {
                Text(label.resolve(), style = MaterialTheme.typography.bodyLarge)
                line?.let { Text(it.resolve(), style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant) }
            }
            Spacer(Modifier.width(8.dp))
            Switch(checked = on, onCheckedChange = null)
        }
    }
    val notOffered = CompressRules.notOffered(options)
    if (notOffered.isNotEmpty()) Expandable(S.appFilesCompressNotOffered(notOffered.size)) {
        for (o in notOffered) Reason(CompressRules.name(o.path), o.reason)
    }
    if (!nothing) {
        Text(
            S.appFilesCompressTotal(formatBytes(CompressRules.frees(options, st.video, st.photosOn))).resolve(),
            style = MaterialTheme.typography.titleSmall, modifier = Modifier.padding(top = 4.dp),
        )
        Text(S.appFilesCompressWarning().resolve(), style = MaterialTheme.typography.bodyMedium, color = colors.error)
        if (CompressRules.slow(st.tasks)) {
            Text(S.appFilesCompressSlow().resolve(), style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant)
        }
    }
    st.refusal?.let {
        Text(it.resolve(), color = colors.error, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
    }
}

// One of the videos' choices, with what it comes to under it; TalkBack reads it as a radio button.
@Composable
private fun ChoiceRow(title: String, detail: String?, selected: Boolean, onPick: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().heightIn(min = 48.dp).selectable(selected = selected, onClick = onPick, role = Role.RadioButton).padding(vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        RadioButton(selected = selected, onClick = null)
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.bodyLarge)
            detail?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
        }
    }
}

// The job: "Compressing 3 of 12…" with its bar while it runs (a running
// video's own progress in it; [videos]: that videos take a while, and the
// dialog can be closed); how it ended once it is over, the files it
// couldn't compress listed with the device's why.
@Composable
private fun CompressJobView(job: CompressJob, gone: Boolean, videos: Boolean) {
    if (!CompressRules.over(job, gone)) {
        Text(S.appFilesCompressProgress(job.done, job.total).resolve(), Modifier.semantics { liveRegion = LiveRegionMode.Polite })
        LinearProgressIndicator(progress = { CompressRules.progress(job) }, modifier = Modifier.fillMaxWidth())
        if (videos) Text(S.appFilesCompressSlow().resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        return
    }
    val end = compressEnd(job, gone)
    Column(Modifier.semantics { liveRegion = LiveRegionMode.Polite }, verticalArrangement = Arrangement.spacedBy(8.dp)) {
        end.done?.let { Text(it.resolve()) }
        // The failures' line opens their list.
        end.failed?.let { Expandable(it) { for (r in CompressRules.notDone(job, gone)) Reason(CompressRules.name(r.path), r.message) } }
    }
}

// A line that opens the list under it (the files not offered, the ones
// that failed); TalkBack offers to expand or collapse it.
@Composable
private fun Expandable(title: UiText, content: @Composable () -> Unit) {
    var open by rememberSaveable { mutableStateOf(false) }
    Row(
        Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable(role = Role.Button) { open = !open }
            .semantics { if (open) collapse { open = false; true } else expand { open = true; true } },
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(title.resolve(), Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium)
        Icon(if (open) Icons.Default.ExpandLess else Icons.Default.ExpandMore, null)
    }
    if (open) Column(Modifier.padding(start = 8.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) { content() }
}

// A file and why: its name, the device's words under it.
@Composable
private fun Reason(name: String, why: String) {
    Column {
        Text(name, style = MaterialTheme.typography.bodyMedium, fontWeight = FontWeight.Medium, maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (why.isNotEmpty()) Text(why, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}
