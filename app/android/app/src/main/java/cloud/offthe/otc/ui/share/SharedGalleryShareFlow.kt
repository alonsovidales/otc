// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.share

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidFilesGalleryClipLabel
import cloud.offthe.otc.i18n.androidFilesGalleryCopyFailed
import cloud.offthe.otc.i18n.androidFilesGalleryEmpty
import cloud.offthe.otc.i18n.androidFilesGalleryEmptySkipped
import cloud.offthe.otc.i18n.androidFilesGalleryExpires
import cloud.offthe.otc.i18n.androidFilesGalleryFailedTitle
import cloud.offthe.otc.i18n.androidFilesGalleryKeepOpen
import cloud.offthe.otc.i18n.androidFilesGalleryLinkCopied
import cloud.offthe.otc.i18n.androidFilesGalleryLinkOnce
import cloud.offthe.otc.i18n.androidFilesGalleryLooking
import cloud.offthe.otc.i18n.androidFilesGalleryLost
import cloud.offthe.otc.i18n.androidFilesGalleryReady
import cloud.offthe.otc.i18n.androidFilesGallerySkipped
import cloud.offthe.otc.i18n.appFilesGalleryBytes
import cloud.offthe.otc.i18n.appFilesGalleryContents
import cloud.offthe.otc.i18n.appFilesGalleryCopyLink
import cloud.offthe.otc.i18n.appFilesGalleryCopying
import cloud.offthe.otc.i18n.appFilesGalleryCreateFailed
import cloud.offthe.otc.i18n.appFilesGalleryDays
import cloud.offthe.otc.i18n.appFilesGalleryDefaultDescription
import cloud.offthe.otc.i18n.appFilesGalleryDescription
import cloud.offthe.otc.i18n.appFilesGalleryFullResNote
import cloud.offthe.otc.i18n.appFilesGalleryLowRes
import cloud.offthe.otc.i18n.appFilesGalleryLowResNote
import cloud.offthe.otc.i18n.appFilesGalleryLowResNoteVideos
import cloud.offthe.otc.i18n.appFilesGalleryNoLink
import cloud.offthe.otc.i18n.appFilesGalleryPrepareFailed
import cloud.offthe.otc.i18n.appFilesGalleryPrivacy
import cloud.offthe.otc.i18n.appFilesGalleryShareLink
import cloud.offthe.otc.i18n.appFilesGalleryTitle
import cloud.offthe.otc.i18n.appFilesUnexpectedResponse
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonDone
import cloud.offthe.otc.i18n.commonOk
import cloud.offthe.otc.i18n.commonShare
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.CreateSharedGallery
import cloud.offthe.otc.proto.GetSharedGalleryJob
import cloud.offthe.otc.proto.PreviewSharedGallery
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SharedGalleryJob
import cloud.offthe.otc.proto.SharedGalleryPreview
import cloud.offthe.otc.proto.SharedGallerySource
import cloud.offthe.otc.ui.common.OTCTextField
import cloud.offthe.otc.ui.common.Share
import cloud.offthe.otc.ui.common.formatBytes
import cloud.offthe.otc.ui.common.now
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import androidx.compose.foundation.clickable
import androidx.compose.material3.Switch

/*
 * Port of SharedGalleryShareFlow.swift (issue #180): "Share as gallery" for
 * an image group (Images) or a folder (Files). The device is asked what the
 * source holds (PreviewSharedGallery), the owner confirms with a description
 * and an expiry, the device copies and re-encrypts the photos and videos in
 * the background (CreateSharedGallery, then GetSharedGalleryJob every
 * 700 ms) and finally hands back the link - once: it is never stored on the
 * device, so this is the one moment it can be shared or copied.
 */

/** The expiry choices, as ttl_hours: 1, 7 and 30 days (labelled when shown). */
private val expiryOptions = listOf(24, 168, 720)

class SharedGalleryShareViewModel : ViewModel() {
    sealed interface Phase {
        data object Loading : Phase
        data class Confirm(val preview: SharedGalleryPreview) : Phase
        data class Copying(val job: SharedGalleryJob) : Phase
        data class Done(val link: String) : Phase
        data class Failed(val message: UiText) : Phase
    }

    data class State(
        val source: SharedGallerySource? = null,
        val phase: Phase = Phase.Loading,
        val description: String = "",
        val ttlHours: Int = 168,
        // Only small copies of the photos (their thumbnails), no videos.
        val lowRes: Boolean = false,
        val copied: Boolean = false,
    )

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state
    private var work: Job? = null

    /** A new share: forget the previous one and ask the device what this source holds. */
    fun start(source: SharedGallerySource) {
        if (_state.value.source == source) return
        work?.cancel()
        _state.value = State(
            source = source,
            // In the language shown now: from here on it is the owner's
            // text, which they may change.
            description = S.appFilesGalleryDefaultDescription(SimpleDateFormat("dd-MM-yyyy HH:mm", Locale.getDefault()).format(Date())).now(),
        )
        work = viewModelScope.launch {
            try {
                val resp = OTCConnection.request { it.setReqPreviewSharedGallery(PreviewSharedGallery.newBuilder().setSource(source)) }
                val phase = when {
                    resp.payloadCase == RespEnvelope.PayloadCase.RESP_SHARED_GALLERY_PREVIEW -> {
                        val p = resp.respSharedGalleryPreview
                        if (p.files > 0) Phase.Confirm(p)
                        else Phase.Failed(if (p.skipped > 0) S.androidFilesGalleryEmptySkipped(p.skipped) else S.androidFilesGalleryEmpty())
                    }
                    resp.error -> Phase.Failed(said(resp.errorMessage) { S.appFilesGalleryPrepareFailed() })
                    else -> Phase.Failed(S.appFilesUnexpectedResponse())
                }
                _state.update { it.copy(phase = phase) }
            } catch (e: Exception) {
                _state.update { it.copy(phase = Phase.Failed(e.message?.let { m -> UiText.Raw(m) } ?: S.appFilesGalleryPrepareFailed())) }
            }
        }
    }

    fun setDescription(v: String) = _state.update { it.copy(description = v) }
    fun setTtlHours(v: Int) = _state.update { it.copy(ttlHours = v) }
    fun setLowRes(v: Boolean) = _state.update { it.copy(lowRes = v) }

    fun share() {
        val st = _state.value
        val source = st.source ?: return
        val preview = (st.phase as? Phase.Confirm)?.preview ?: return
        _state.update {
            it.copy(phase = Phase.Copying(SharedGalleryJob.newBuilder().setTotal(preview.files).setBytesTotal(preview.bytes).build()))
        }
        work?.cancel()
        work = viewModelScope.launch {
            try {
                val resp = OTCConnection.request {
                    it.setReqCreateSharedGallery(
                        CreateSharedGallery.newBuilder().setSource(source).setDescription(st.description.trim()).setTtlHours(st.ttlHours).setLowRes(st.lowRes),
                    )
                }
                var job = when {
                    resp.payloadCase == RespEnvelope.PayloadCase.RESP_SHARED_GALLERY_JOB -> resp.respSharedGalleryJob
                    resp.error -> { fail(said(resp.errorMessage) { S.appFilesGalleryCreateFailed() }); return@launch }
                    else -> { fail(S.appFilesUnexpectedResponse()); return@launch }
                }
                // A dropped connection mid-copy is retried by OTCConnection;
                // a few failures in a row end the wait.
                var misses = 0
                while (isActive && !job.finished) {
                    _state.update { it.copy(phase = Phase.Copying(job)) }
                    delay(700)
                    try {
                        val r = OTCConnection.request { it.setReqGetSharedGalleryJob(GetSharedGalleryJob.newBuilder().setJobId(job.jobId)) }
                        if (r.payloadCase == RespEnvelope.PayloadCase.RESP_SHARED_GALLERY_JOB) { job = r.respSharedGalleryJob; misses = 0 }
                        else if (r.error) { fail(said(r.errorMessage) { S.androidFilesGalleryCopyFailed() }); return@launch }
                        else misses++
                    } catch (e: Exception) {
                        if (++misses >= 5) throw e
                    }
                    if (misses >= 5) { fail(S.androidFilesGalleryLost()); return@launch }
                }
                if (job.error.isNotEmpty()) fail(UiText.Raw(job.error))
                else if (job.link.isEmpty()) fail(S.appFilesGalleryNoLink())
                else _state.update { it.copy(phase = Phase.Done(job.link)) }
            } catch (e: Exception) {
                fail(e.message?.let { m -> UiText.Raw(m) } ?: S.appFilesGalleryCreateFailed())
            }
        }
    }

    private fun fail(m: UiText) = _state.update { it.copy(phase = Phase.Failed(m)) }

    /** The device's own words when it gave some (it wrote them in the app's language), else ours. */
    private inline fun said(words: String, ours: () -> UiText): UiText =
        if (words.isEmpty()) ours() else UiText.Raw(words)

    fun copyLink(context: Context, link: String) {
        val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        cm.setPrimaryClip(ClipData.newPlainText(S.androidFilesGalleryClipLabel().now(context), link))
        _state.update { it.copy(copied = true) }
    }

    /** The flow was closed: the next share starts from scratch. */
    fun reset() {
        work?.cancel()
        work = null
        _state.value = State()
    }
}

/**
 * The whole flow as one dialog that changes with the phase. While the copy
 * runs it can't be dismissed: the link only exists at the end of it.
 */
@Composable
fun SharedGalleryShareFlow(source: SharedGallerySource, onDismiss: () -> Unit) {
    val vm: SharedGalleryShareViewModel = viewModel(key = "sharedGalleryShare")
    val st by vm.state.collectAsState()
    val context = LocalContext.current
    LaunchedEffect(source) { vm.start(source) }
    // Before start() has run, the state may still be the previous share's.
    if (st.source != source) return

    val close = { vm.reset(); onDismiss() }
    // Finished: straight to the share sheet, as iOS does; the dialog stays
    // behind it with Copy Link and Share Link for a second go.
    val doneLink = (st.phase as? SharedGalleryShareViewModel.Phase.Done)?.link
    LaunchedEffect(doneLink) { if (doneLink != null) Share.link(context, doneLink) }
    val copying = st.phase is SharedGalleryShareViewModel.Phase.Copying

    AlertDialog(
        onDismissRequest = { if (!copying) close() },
        properties = DialogProperties(dismissOnBackPress = !copying, dismissOnClickOutside = false),
        title = {
            Text(
                when (st.phase) {
                    is SharedGalleryShareViewModel.Phase.Done -> S.androidFilesGalleryReady()
                    is SharedGalleryShareViewModel.Phase.Failed -> S.androidFilesGalleryFailedTitle()
                    else -> S.appFilesGalleryTitle()
                }.resolve(),
            )
        },
        // The text slot is the part AlertDialog lets shrink, so the content
        // scrolls and the buttons stay on screen (keyboard up, folded phone).
        text = {
            Column(Modifier.verticalScroll(rememberScrollState())) {
                when (val phase = st.phase) {
                    SharedGalleryShareViewModel.Phase.Loading -> Row(verticalAlignment = Alignment.CenterVertically) {
                        CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                        Spacer(Modifier.width(12.dp))
                        Text(S.androidFilesGalleryLooking().resolve())
                    }
                    is SharedGalleryShareViewModel.Phase.Confirm -> ConfirmContent(phase.preview, st, vm)
                    is SharedGalleryShareViewModel.Phase.Copying -> {
                        val j = phase.job
                        Text(S.appFilesGalleryCopying(minOf(j.done + 1, maxOf(j.total, 1)), j.total).resolve())
                        Spacer(Modifier.height(10.dp))
                        LinearProgressIndicator(
                            progress = { if (j.bytesTotal > 0) (j.bytesDone.toFloat() / j.bytesTotal).coerceIn(0f, 1f) else 0f },
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(6.dp))
                        Text(S.appFilesGalleryBytes(formatBytes(j.bytesDone), formatBytes(j.bytesTotal)).resolve(), style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant)
                        Spacer(Modifier.height(10.dp))
                        Text(S.androidFilesGalleryKeepOpen().resolve(), style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    is SharedGalleryShareViewModel.Phase.Done -> {
                        Text(S.androidFilesGalleryLinkOnce().resolve())
                        Spacer(Modifier.height(10.dp))
                        SelectionContainer {
                            Text(phase.link, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace)
                        }
                        if (st.copied) {
                            Spacer(Modifier.height(6.dp))
                            Text(S.androidFilesGalleryLinkCopied().resolve(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.primary)
                        }
                    }
                    is SharedGalleryShareViewModel.Phase.Failed -> Text(phase.message.resolve())
                }
            }
        },
        confirmButton = {
            when (val phase = st.phase) {
                is SharedGalleryShareViewModel.Phase.Confirm ->
                    TextButton(onClick = { vm.share() }) { Text(S.commonShare().resolve()) }
                is SharedGalleryShareViewModel.Phase.Done ->
                    TextButton(onClick = { Share.link(context, phase.link) }) { Text(S.appFilesGalleryShareLink().resolve()) }
                is SharedGalleryShareViewModel.Phase.Failed ->
                    TextButton(onClick = close) { Text(S.commonOk().resolve()) }
                else -> {}
            }
        },
        dismissButton = {
            when (val phase = st.phase) {
                SharedGalleryShareViewModel.Phase.Loading, is SharedGalleryShareViewModel.Phase.Confirm ->
                    TextButton(onClick = close) { Text(S.commonCancel().resolve()) }
                is SharedGalleryShareViewModel.Phase.Done -> Row {
                    TextButton(onClick = { vm.copyLink(context, phase.link) }) { Text(S.appFilesGalleryCopyLink().resolve()) }
                    TextButton(onClick = close) { Text(S.commonDone().resolve()) }
                }
                else -> {}
            }
        },
    )
}

@Composable
private fun ConfirmContent(preview: SharedGalleryPreview, st: SharedGalleryShareViewModel.State, vm: SharedGalleryShareViewModel) {
    Text(S.appFilesGalleryContents(preview.files, formatBytes(preview.bytes)).resolve(),
        style = MaterialTheme.typography.bodyLarge)
    if (preview.skipped > 0) {
        Spacer(Modifier.height(4.dp))
        Text(S.androidFilesGallerySkipped(preview.skipped).resolve(),
            style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
    Spacer(Modifier.height(8.dp))
    Text(S.appFilesGalleryPrivacy().resolve(),
        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    Spacer(Modifier.height(12.dp))
    Text(S.appFilesGalleryDescription().resolve(), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.padding(bottom = 4.dp))
    OTCTextField(
        value = st.description, onValueChange = vm::setDescription, singleLine = true,
        placeholder = { Text(S.appFilesGalleryDescription().resolve()) }, modifier = Modifier.fillMaxWidth(),
        keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Sentences),
    )
    Spacer(Modifier.height(12.dp))
    Row(Modifier.fillMaxWidth().clickable { vm.setLowRes(!st.lowRes) }, verticalAlignment = Alignment.CenterVertically) {
        Text(S.appFilesGalleryLowRes().resolve(), Modifier.weight(1f), style = MaterialTheme.typography.bodyLarge)
        Switch(checked = st.lowRes, onCheckedChange = vm::setLowRes)
    }
    Text(
        when {
            !st.lowRes -> S.appFilesGalleryFullResNote()
            preview.videos > 0 -> S.appFilesGalleryLowResNoteVideos(preview.videos)
            else -> S.appFilesGalleryLowResNote()
        }.resolve(),
        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
    )
    Spacer(Modifier.height(12.dp))
    Text(S.androidFilesGalleryExpires().resolve(), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.padding(bottom = 4.dp))
    SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
        expiryOptions.forEachIndexed { i, hours ->
            SegmentedButton(
                selected = st.ttlHours == hours, onClick = { vm.setTtlHours(hours) },
                shape = SegmentedButtonDefaults.itemShape(index = i, count = expiryOptions.size),
            ) { Text(S.appFilesGalleryDays(hours / 24).resolve(), maxLines = 1) }
        }
    }
}
