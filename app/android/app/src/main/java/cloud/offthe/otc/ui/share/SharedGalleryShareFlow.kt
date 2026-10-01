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

/** The expiry choices: label to ttl_hours. */
private val expiryOptions = listOf("1 day" to 24, "7 days" to 168, "30 days" to 720)

class SharedGalleryShareViewModel : ViewModel() {
    sealed interface Phase {
        data object Loading : Phase
        data class Confirm(val preview: SharedGalleryPreview) : Phase
        data class Copying(val job: SharedGalleryJob) : Phase
        data class Done(val link: String) : Phase
        data class Failed(val message: String) : Phase
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
            description = "Shared Media " + SimpleDateFormat("dd-MM-yyyy HH:mm", Locale.getDefault()).format(Date()),
        )
        work = viewModelScope.launch {
            try {
                val resp = OTCConnection.request { it.setReqPreviewSharedGallery(PreviewSharedGallery.newBuilder().setSource(source)) }
                val phase = when {
                    resp.payloadCase == RespEnvelope.PayloadCase.RESP_SHARED_GALLERY_PREVIEW -> {
                        val p = resp.respSharedGalleryPreview
                        if (p.files > 0) Phase.Confirm(p)
                        else Phase.Failed(if (p.skipped > 0) "There are no photos or videos here (${p.skipped} other file${if (p.skipped == 1) "" else "s"})." else "There are no photos or videos here.")
                    }
                    resp.error -> Phase.Failed(resp.errorMessage.ifEmpty { "Could not prepare the gallery" })
                    else -> Phase.Failed("Unexpected response")
                }
                _state.update { it.copy(phase = phase) }
            } catch (e: Exception) {
                _state.update { it.copy(phase = Phase.Failed(e.message ?: "Could not prepare the gallery")) }
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
                    resp.error -> { fail(resp.errorMessage.ifEmpty { "Could not create the gallery" }); return@launch }
                    else -> { fail("Unexpected response"); return@launch }
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
                        else if (r.error) { fail(r.errorMessage.ifEmpty { "The copy failed" }); return@launch }
                        else misses++
                    } catch (e: Exception) {
                        if (++misses >= 5) throw e
                    }
                    if (misses >= 5) { fail("Lost track of the copy"); return@launch }
                }
                if (job.error.isNotEmpty()) fail(job.error)
                else if (job.link.isEmpty()) fail("The device did not return a link")
                else _state.update { it.copy(phase = Phase.Done(job.link)) }
            } catch (e: Exception) {
                fail(e.message ?: "Could not create the gallery")
            }
        }
    }

    private fun fail(m: String) = _state.update { it.copy(phase = Phase.Failed(m)) }

    fun copyLink(context: Context, link: String) {
        val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        cm.setPrimaryClip(ClipData.newPlainText("Shared gallery link", link))
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
                    is SharedGalleryShareViewModel.Phase.Done -> "Gallery Ready"
                    is SharedGalleryShareViewModel.Phase.Failed -> "Couldn't Share"
                    else -> "Share as Gallery"
                },
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
                        Text("Looking at what will be shared…")
                    }
                    is SharedGalleryShareViewModel.Phase.Confirm -> ConfirmContent(phase.preview, st, vm)
                    is SharedGalleryShareViewModel.Phase.Copying -> {
                        val j = phase.job
                        Text("Copying ${minOf(j.done + 1, maxOf(j.total, 1))} of ${j.total}")
                        Spacer(Modifier.height(10.dp))
                        LinearProgressIndicator(
                            progress = { if (j.bytesTotal > 0) (j.bytesDone.toFloat() / j.bytesTotal).coerceIn(0f, 1f) else 0f },
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(6.dp))
                        Text("${formatBytes(j.bytesDone)} of ${formatBytes(j.bytesTotal)}", style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant)
                        Spacer(Modifier.height(10.dp))
                        Text("Keep this open: the link is shown when the copy finishes.", style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    is SharedGalleryShareViewModel.Phase.Done -> {
                        Text("This link is shown only now - the device never stores it. Share it or copy it before closing.")
                        Spacer(Modifier.height(10.dp))
                        SelectionContainer {
                            Text(phase.link, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace)
                        }
                        if (st.copied) {
                            Spacer(Modifier.height(6.dp))
                            Text("Link copied", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.primary)
                        }
                    }
                    is SharedGalleryShareViewModel.Phase.Failed -> Text(phase.message)
                }
            }
        },
        confirmButton = {
            when (val phase = st.phase) {
                is SharedGalleryShareViewModel.Phase.Confirm ->
                    TextButton(onClick = { vm.share() }) { Text("Share") }
                is SharedGalleryShareViewModel.Phase.Done ->
                    TextButton(onClick = { Share.link(context, phase.link) }) { Text("Share Link") }
                is SharedGalleryShareViewModel.Phase.Failed ->
                    TextButton(onClick = close) { Text("OK") }
                else -> {}
            }
        },
        dismissButton = {
            when (val phase = st.phase) {
                SharedGalleryShareViewModel.Phase.Loading, is SharedGalleryShareViewModel.Phase.Confirm ->
                    TextButton(onClick = close) { Text("Cancel") }
                is SharedGalleryShareViewModel.Phase.Done -> Row {
                    TextButton(onClick = { vm.copyLink(context, phase.link) }) { Text("Copy Link") }
                    TextButton(onClick = close) { Text("Done") }
                }
                else -> {}
            }
        },
    )
}

@Composable
private fun ConfirmContent(preview: SharedGalleryPreview, st: SharedGalleryShareViewModel.State, vm: SharedGalleryShareViewModel) {
    Text("${preview.files} photos and videos, ${formatBytes(preview.bytes)}",
        style = MaterialTheme.typography.bodyLarge)
    if (preview.skipped > 0) {
        Spacer(Modifier.height(4.dp))
        Text("${preview.skipped} other file${if (preview.skipped == 1) " is" else "s are"} not included",
            style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
    Spacer(Modifier.height(8.dp))
    Text("They are copied and re-encrypted with a key of their own. Only people with the link can see them, until it expires.",
        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    Spacer(Modifier.height(12.dp))
    Text("Description", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.padding(bottom = 4.dp))
    OTCTextField(
        value = st.description, onValueChange = vm::setDescription, singleLine = true,
        placeholder = { Text("Description") }, modifier = Modifier.fillMaxWidth(),
        keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Sentences),
    )
    Spacer(Modifier.height(12.dp))
    Row(Modifier.fillMaxWidth().clickable { vm.setLowRes(!st.lowRes) }, verticalAlignment = Alignment.CenterVertically) {
        Text("Low resolution only", Modifier.weight(1f), style = MaterialTheme.typography.bodyLarge)
        Switch(checked = st.lowRes, onCheckedChange = vm::setLowRes)
    }
    Text(
        if (st.lowRes) "Only small copies of the photos are shared (about 1000 pixels wide); the originals never leave your device." +
            (if (preview.videos > 0) " ${preview.videos} video${if (preview.videos == 1) " is" else "s are"} left out." else "")
        else "The full-size originals are shared.",
        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
    )
    Spacer(Modifier.height(12.dp))
    Text("Link expires after", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.padding(bottom = 4.dp))
    SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
        expiryOptions.forEachIndexed { i, (label, hours) ->
            SegmentedButton(
                selected = st.ttlHours == hours, onClick = { vm.setTtlHours(hours) },
                shape = SegmentedButtonDefaults.itemShape(index = i, count = expiryOptions.size),
            ) { Text(label, maxLines = 1) }
        }
    }
}
