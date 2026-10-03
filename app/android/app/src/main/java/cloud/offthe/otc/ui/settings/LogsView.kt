// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import android.content.Context
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.GetLogs
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SendLogs
import cloud.offthe.otc.ui.common.OTCTextField
import cloud.offthe.otc.ui.common.Share
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.io.File

/*
 * Settings > Logs (primary instance only), the same screen as the web's and
 * iOS's: the device's own log ("app") or the updater's ("update"), live.
 * The first request takes the last 64 KB; after that each request asks from
 * nextOffset with wait_seconds, and the device holds it until new lines are
 * written - a stream over plain requests. "Send to us" has the device mail
 * the last part of both logs, with a note, through the bridge.
 */

class LogsViewModel : ViewModel() {
    data class State(
        val source: String = "app",
        val lines: List<String> = emptyList(),
        val paused: Boolean = false,
        val loading: Boolean = false,
        val error: String? = null,
        val sending: Boolean = false,
        val sendResult: String? = null,
    )
    val state = MutableStateFlow(State())

    private var job: Job? = null
    // Bumped on every (re)start: answers from an older loop are dropped.
    private var generation = 0
    // Where the loop asks from next; -1 = the last 64 KB (first request).
    private var nextOffset = -1L
    // The last line had no newline yet: the next text continues it.
    private var openLine = false
    private var totalChars = 0
    // Guards generation and the text against a stale loop's answer landing mid-switch.
    private val lock = Any()

    fun start() {
        if (job?.isActive == true) return
        state.update { it.copy(paused = false) }
        loop()
    }

    fun stop() = synchronized(lock) {
        job?.cancel()
        job = null
        generation++
    }

    /** Closing the screen: stop and forget, so it opens fresh next time. */
    fun close() = synchronized(lock) {
        stop()
        reset()
        state.update { State(source = it.source) }
    }

    fun setSource(source: String) {
        if (source == state.value.source) return
        synchronized(lock) {
            stop()
            reset()
            state.update { State(source = source, paused = it.paused) }
        }
        if (!state.value.paused) loop()
    }

    fun togglePause() {
        if (state.value.paused) start()
        else { stop(); state.update { it.copy(paused = true, loading = false) } }
    }

    fun text(): String = state.value.lines.joinToString("\n")

    private fun reset() {
        nextOffset = -1L
        openLine = false
        totalChars = 0
    }

    private fun loop() {
        val gen = ++generation
        val source = state.value.source
        job = viewModelScope.launch(Dispatchers.IO) {
            while (isActive && gen == generation) {
                val from = nextOffset
                if (from < 0) state.update { it.copy(loading = true) }
                try {
                    val req = GetLogs.newBuilder().setSource(source).setOffset(from)
                    if (from < 0) req.setMaxBytes(65536).setWaitSeconds(0)
                    else req.setMaxBytes(262144).setWaitSeconds(25)
                    val resp = OTCConnection.request { it.setReqGetLogs(req) }
                    if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_LOGS)
                        throw OTCConnection.RequestError(resp.errorMessage.ifEmpty { "Could not load the log" })
                    val logs = resp.respLogs
                    synchronized(lock) {
                        if (gen != generation) return@launch
                        if (from >= 0 && logs.size < from) append("\n— log rotated —\n", forceNewLine = true)
                        if (logs.text.isNotEmpty()) append(logs.text)
                        nextOffset = logs.nextOffset
                        state.update { it.copy(loading = false, error = null) }
                    }
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    if (gen != generation) break
                    state.update { it.copy(loading = false, error = e.message ?: "Could not load the log") }
                    delay(5_000)
                }
            }
        }
    }

    private fun append(text: String, forceNewLine: Boolean = false) {
        var parts = text.split('\n')
        val lines = ArrayList(state.value.lines)
        if (forceNewLine) {
            // A marker on its own line, whatever came before it.
            parts = parts.filter { it.isNotEmpty() }
            openLine = false
        }
        if (openLine && lines.isNotEmpty() && parts.isNotEmpty()) {
            val last = lines.removeAt(lines.size - 1)
            totalChars -= last.length
            parts = listOf(last + parts[0]) + parts.drop(1)
        }
        // "a\nb\n" splits into a, b, "" - the trailing "" means the last line ended.
        openLine = !forceNewLine && parts.lastOrNull()?.isNotEmpty() == true
        val add = if (!forceNewLine && parts.lastOrNull()?.isEmpty() == true) parts.dropLast(1) else parts
        lines.addAll(add)
        totalChars += add.sumOf { it.length + 1 }
        // Keep at most about 1 MB on screen: drop from the top.
        var drop = 0
        while (totalChars > maxChars && drop < lines.size - 1) totalChars -= lines[drop++].length + 1
        val kept = if (drop > 0) lines.subList(drop, lines.size).toList() else lines
        state.update { it.copy(lines = kept) }
    }

    fun send(note: String) {
        state.update { it.copy(sending = true) }
        viewModelScope.launch(Dispatchers.IO) {
            val result = try {
                val resp = OTCConnection.request { it.setReqSendLogs(SendLogs.newBuilder().setNote(note.trim())) }
                when {
                    resp.error -> resp.errorMessage.ifEmpty { "Could not send the logs" }
                    resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.ok -> "Sent - we'll answer at your account's email."
                    resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK -> resp.respAck.errorMsg.ifEmpty { "Could not send the logs" }
                    else -> "Unexpected response"
                }
            } catch (e: Exception) {
                e.message ?: "Could not send the logs"
            }
            state.update { it.copy(sending = false, sendResult = result) }
        }
    }

    fun dismissSendResult() = state.update { it.copy(sendResult = null) }

    override fun onCleared() { stop() }

    companion object {
        private const val maxChars = 1_000_000
    }
}

// A share intent's extras go through a ~1 MB binder buffer: a longer log goes as a file.
private fun shareLog(context: Context, source: String, text: String) {
    if (text.length <= 100_000) { Share.link(context, text); return }
    val f = File(context.cacheDir, "otc-${if (source == "update") "updates" else "device"}.log")
    f.writeText(text)
    Share.file(context, f, "text/plain")
}

/** Full screen, over Settings - like SharedLinksView. */
@Composable
fun LogsView(onClose: () -> Unit) {
    val vm: LogsViewModel = viewModel(key = "logs")
    val st by vm.state.collectAsState()
    val context = LocalContext.current
    val list = rememberLazyListState()
    // Follow the end while the user is there; scrolling up stops following.
    var follow by remember { mutableStateOf(true) }
    var showSend by remember { mutableStateOf(false) }
    var note by remember { mutableStateOf("") }

    DisposableEffect(Unit) {
        vm.start()
        onDispose { vm.close() }
    }
    LaunchedEffect(list) {
        snapshotFlow { list.isScrollInProgress }.collect { scrolling -> if (!scrolling) follow = !list.canScrollForward }
    }
    LaunchedEffect(st.lines.size, st.lines.lastOrNull()) {
        if (follow && !list.isScrollInProgress && st.lines.isNotEmpty()) list.scrollToItem(st.lines.size - 1)
    }

    Dialog(onDismissRequest = onClose, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Column(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.surface).systemBarsPadding()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Text("Logs", style = MaterialTheme.typography.titleLarge, modifier = Modifier.weight(1f).padding(start = 8.dp))
                if (st.loading) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                TextButton(onClick = onClose) { Text("Done") }
            }
            Text("Logs can include file and folder names, search words, Wi-Fi network names and your device's addresses. Check them before you share them.",
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp))
            SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp)) {
                listOf("app" to "Device", "update" to "Updates").forEachIndexed { i, (source, label) ->
                    SegmentedButton(selected = st.source == source, onClick = { follow = true; vm.setSource(source) }, shape = SegmentedButtonDefaults.itemShape(i, 2)) {
                        Text(label)
                    }
                }
            }
            Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                TextButton(onClick = { vm.togglePause() }) { Text(if (st.paused) "Resume" else "Pause") }
                Spacer(Modifier.weight(1f))
                TextButton(onClick = { shareLog(context, st.source, vm.text()) }, enabled = st.lines.isNotEmpty()) { Text("Share") }
                TextButton(onClick = { note = ""; showSend = true }, enabled = !st.sending) { Text(if (st.sending) "Sending…" else "Send to us") }
            }
            st.error?.let { Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp)) }
            Box(Modifier.weight(1f).fillMaxWidth().background(MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.4f))) {
                if (st.lines.isEmpty() && !st.loading && st.error == null) {
                    Text("Nothing logged yet.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.align(Alignment.Center))
                }
                SelectionContainer {
                    LazyColumn(Modifier.fillMaxSize(), state = list, contentPadding = PaddingValues(horizontal = 8.dp, vertical = 8.dp)) {
                        itemsIndexed(st.lines) { _, line ->
                            Text(line, fontFamily = FontFamily.Monospace, fontSize = 11.sp, lineHeight = 14.sp)
                        }
                    }
                }
            }
        }
    }

    if (showSend) AlertDialog(
        onDismissRequest = { showSend = false },
        title = { Text("Send logs to us") },
        text = {
            Column {
                Text("Sends the last part of your device's logs, your note and your account's email to info@off-the.cloud, so we can help. Logs can include file and folder names, search words, Wi-Fi network names and your device's addresses.",
                    style = MaterialTheme.typography.bodySmall)
                OTCTextField(value = note, onValueChange = { note = it }, placeholder = { Text("Note (optional)") },
                    modifier = Modifier.fillMaxWidth().padding(top = 12.dp))
            }
        },
        confirmButton = { TextButton(onClick = { showSend = false; vm.send(note) }) { Text("Send") } },
        dismissButton = { TextButton(onClick = { showSend = false }) { Text("Cancel") } },
    )
    st.sendResult?.let {
        AlertDialog(onDismissRequest = { vm.dismissSendResult() }, text = { Text(it) },
            confirmButton = { TextButton(onClick = { vm.dismissSendResult() }) { Text("OK") } })
    }
}
