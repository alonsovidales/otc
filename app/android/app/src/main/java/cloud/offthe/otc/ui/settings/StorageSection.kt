// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.i18n.LanguageSettings
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appSettingsStorageAdding
import cloud.offthe.otc.i18n.appSettingsStorageConfirm
import cloud.offthe.otc.i18n.appSettingsStorageConfirmText
import cloud.offthe.otc.i18n.appSettingsStorageConfirmTitle
import cloud.offthe.otc.i18n.appSettingsStorageCopying
import cloud.offthe.otc.i18n.appSettingsStorageFailed
import cloud.offthe.otc.i18n.appSettingsStorageLookAlike
import cloud.offthe.otc.i18n.appSettingsStorageNewDisk
import cloud.offthe.otc.i18n.appSettingsStorageNotAdded
import cloud.offthe.otc.i18n.appSettingsStorageTitle
import cloud.offthe.otc.i18n.appSettingsStorageUse
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.MirrorDiskCandidate
import cloud.offthe.otc.proto.RaidState
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.Status
import cloud.offthe.otc.proto.UseMirrorDisk
import cloud.offthe.otc.ui.common.DeviceRequest
import cloud.offthe.otc.ui.common.decimalText
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import java.util.Locale

// Settings › Storage (issue #208), as the web's MirrorDiskPanel.tsx: a disk
// plugged in while the mirror lacks one, which the device didn't add by
// itself because something is on it - most new disks and cards come
// formatted (Status.mirror_candidates). Its owner may have it erased and
// added: the confirmation says plainly that everything on that disk is
// erased and that it becomes the mirror's second disk. The device's root
// watcher checks everything again before it erases anything; then the
// mirror copies every file onto the disk, its progress shown here.
//
// Shown only while there is something to say: a disk offered, or one asked
// for from here, until the copy onto it ends. The device offers disks to the
// owner of the primary instance alone, never in a container nor on a demo
// device. Two disks that look the same - one name, one size - can't be
// chosen here: the owner couldn't tell which one would be erased. iOS's
// StorageSection.swift is the same.

/** A disk the device offers for the mirror (Status.mirror_candidates). */
data class MirrorDisk(val id: String, val model: String, val size: Long) {
    constructor(c: MirrorDiskCandidate) : this(c.id, c.model, c.size)

    /**
     * The disk as its owner knows it: what it calls itself and its size, in
     * decimal units as on its label ("Generic Card Reader · 512.1 GB"), or
     * the size alone when it gives no name. Made when shown.
     */
    fun name(locale: Locale = LanguageSettings.numberLocale): String {
        val model = model.trim()
        val size = MirrorDisks.sizeText(size, locale)
        return if (model.isEmpty()) size else "$model · $size" // i18n-ignore: the disk's own name and its size, as on the web
    }
}

/** The disk asked for from here ([at]: the Ack, elapsed ms), until the copy onto it ends or it turns out it wasn't added. */
data class MirrorDiskAsked(val disk: MirrorDisk, val at: Long, val copying: Boolean = false)

/** What a Status means for the disk asked for. */
enum class MirrorDiskStep {
    /** Nothing new: still waiting, or still copying. */
    UNCHANGED,
    /** The mirror started copying onto it. */
    COPYING,
    /** Over: a whole mirror (added and copied), or a copy that stopped - the mirror's own Alert says why. */
    DONE,
    /** Nothing copying [MirrorDisks.NOT_ADDED_AFTER_MS] after the Ack: it wasn't added (an Alert says why). */
    NOT_ADDED,
}

/** What the device said to UseMirrorDisk. */
sealed interface MirrorDiskAnswer {
    /** Taken (the Ack): its watcher takes it up within seconds. */
    data object Taken : MirrorDiskAnswer

    /** Turned down (not_candidate, not_degraded, busy, not_in_container, demo): the device's words, null when it gave none. */
    data class Refused(val words: String?) : MirrorDiskAnswer

    companion object {
        fun of(r: RespEnvelope): MirrorDiskAnswer {
            val ack = if (r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) r.respAck else null
            return when {
                !r.error && ack?.ok == true -> Taken
                r.error && r.errorMessage.isNotEmpty() -> Refused(r.errorMessage)
                !ack?.errorMsg.isNullOrEmpty() -> Refused(ack?.errorMsg)
                else -> Refused(null)
            }
        }
    }
}

/** The section's rules, as the web's. */
object MirrorDisks {
    /**
     * The device answers at once and its watcher acts within seconds: a disk
     * asked for that is neither being copied onto nor in a whole mirror
     * after this long wasn't added.
     */
    const val NOT_ADDED_AFTER_MS = 20_000L

    /** Settings' Status poll while the device takes a disk up, and otherwise. */
    const val FAST_POLL_MS = 2_000L
    const val POLL_MS = 5_000L

    private val units = listOf("B", "kB", "MB", "GB", "TB", "PB") // i18n-ignore: unit symbols, the same in every language

    /**
     * A size in bytes in decimal units with at most one decimal, as on a
     * disk's label: "512.1 GB", "2 TB", "500 GB" ("512,1 GB" in Spanish:
     * decimalText). A size that rounds up to 1000 of a unit is one of the
     * next ("1 TB").
     */
    fun sizeText(bytes: Long, locale: Locale = LanguageSettings.numberLocale): String {
        var n = bytes.coerceAtLeast(0).toDouble()
        var unit = 0
        while (n >= 1000 && unit < units.size - 1) {
            n /= 1000
            unit++
        }
        if (unit == 0) return "${n.toLong()} ${units[0]}" // i18n-ignore: a size and its unit's symbol
        var tenths = Math.round(n * 10).toDouble()
        if (tenths >= 10_000 && unit < units.size - 1) {
            unit++
            tenths = Math.round(tenths / 1000).toDouble()
        }
        val whole = tenths % 10 == 0.0
        val number = decimalText(if (whole) "%.0f" else "%.1f", tenths / 10, locale)
        return "$number ${units[unit]}" // i18n-ignore: a size and its unit's symbol
    }

    /** Another disk offered with the same name and size: neither gets a button, only the hint that they can't be told apart. */
    fun lookAlike(d: MirrorDisk, offered: List<MirrorDisk>): Boolean =
        offered.any { it.id != d.id && it.model == d.model && it.size == d.size }

    /** How the disk asked for goes, from a Status ([now]: elapsed ms, as [MirrorDiskAsked.at]). */
    fun step(asked: MirrorDiskAsked, raidState: RaidState, now: Long): MirrorDiskStep = when {
        raidState == RaidState.RaidSyncing -> if (asked.copying) MirrorDiskStep.UNCHANGED else MirrorDiskStep.COPYING
        raidState == RaidState.RaidInSync || asked.copying -> MirrorDiskStep.DONE
        now - asked.at >= NOT_ADDED_AFTER_MS -> MirrorDiskStep.NOT_ADDED
        else -> MirrorDiskStep.UNCHANGED
    }

    /** Whether the section shows: a disk offered, one asked for, or one that wasn't added. */
    fun shown(offered: List<MirrorDisk>, asked: MirrorDiskAsked?, notAdded: MirrorDisk?): Boolean =
        asked != null || notAdded != null || offered.isNotEmpty()

    /** The disks a Status offers, in its order. */
    fun offered(s: Status?): List<MirrorDisk> = s?.mirrorCandidatesList.orEmpty().map(::MirrorDisk)
}

/** Settings › Storage's state, fed with Settings' Status answers. */
class MirrorDiskViewModel(
    private val request: DeviceRequest = { build -> OTCConnection.request(build = build) },
    /** The clock, in milliseconds that only go forward (tests set their own). */
    private val clock: () -> Long = { System.nanoTime() / 1_000_000 },
) : ViewModel() {
    data class State(
        /** The disk asked for from here, until the copy onto it ends. */
        val asked: MirrorDiskAsked? = null,
        /** The disk asked for that the device didn't add. */
        val notAdded: MirrorDisk? = null,
        /** The disk whose confirmation shows. */
        val confirming: MirrorDisk? = null,
        /** A request on its way: the buttons wait. */
        val sending: Boolean = false,
        /** The device turned the disk down (or nothing answered). */
        val failure: UiText? = null,
    ) {
        /** Settings' Status asks every 2 s while the device takes the disk up. */
        val fastPoll: Boolean get() = asked != null && !asked.copying
    }

    val state = MutableStateFlow(State())

    fun confirm(d: MirrorDisk?) = state.update { it.copy(confirming = d) }

    /** Each Status Settings gets. */
    fun sawStatus(s: Status?) {
        if (s == null) return
        state.update { st ->
            val a = st.asked ?: return@update st
            when (MirrorDisks.step(a, s.raidState, clock())) {
                MirrorDiskStep.UNCHANGED -> st
                MirrorDiskStep.COPYING -> st.copy(asked = a.copy(copying = true))
                MirrorDiskStep.DONE -> st.copy(asked = null)
                MirrorDiskStep.NOT_ADDED -> st.copy(asked = null, notAdded = a.disk)
            }
        }
    }

    /** Erase [d] and add it to the mirror (after the confirmation). */
    suspend fun use(d: MirrorDisk) {
        state.update { it.copy(confirming = null, sending = true, failure = null) }
        try {
            val r = request { it.setReqUseMirrorDisk(UseMirrorDisk.newBuilder().setDisk(d.id)) }
            when (val answer = MirrorDiskAnswer.of(r)) {
                MirrorDiskAnswer.Taken -> state.update { it.copy(notAdded = null, asked = MirrorDiskAsked(d, clock())) }
                is MirrorDiskAnswer.Refused -> state.update { it.copy(failure = answer.words?.let { w -> UiText.Raw(w) } ?: S.appSettingsStorageFailed()) }
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            state.update { it.copy(failure = e.shownText() ?: S.appSettingsStorageFailed()) }
        } finally {
            state.update { it.copy(sending = false) }
        }
    }
}

/**
 * Settings › Storage, right after Status: from [status]'s answers (asked
 * every 2 s while the device takes a disk up - SettingsView sets the
 * period from [MirrorDiskViewModel.State.fastPoll]).
 */
@Composable
internal fun StorageSection(status: StatusViewModel, vm: MirrorDiskViewModel = viewModel()) {
    val st by vm.state.collectAsState()
    val sst by status.state.collectAsState()
    // Every answer, equal ones too: the 20 s wait is counted at each.
    LaunchedEffect(sst.asks) { vm.sawStatus(sst.status) }
    val s = sst.status
    val offered = MirrorDisks.offered(s)
    if (!MirrorDisks.shown(offered, st.asked, st.notAdded)) return
    Section(S.appSettingsStorageTitle().resolve()) {
        val asked = st.asked
        if (asked != null && !asked.copying) {
            Row(Modifier.fillMaxWidth().semantics(mergeDescendants = true) {}, verticalAlignment = Alignment.CenterVertically) {
                CircularProgressIndicator(Modifier.width(18.dp), strokeWidth = 2.dp)
                Spacer(Modifier.width(8.dp))
                Caption(S.appSettingsStorageAdding(asked.disk.name()).resolve())
            }
        }
        if (asked != null && asked.copying && s != null) {
            Column(Modifier.fillMaxWidth()) {
                Text(S.appSettingsStorageCopying(asked.disk.name()).resolve(), style = MaterialTheme.typography.bodyMedium)
                LinearProgressIndicator(progress = { (s.raidSyncPercent / 100f).coerceIn(0f, 1f) }, modifier = Modifier.fillMaxWidth().padding(vertical = 6.dp))
                Caption(raidStateLabel(s).resolve())
            }
        }
        st.notAdded?.let { Text(S.appSettingsStorageNotAdded(it.name()).resolve(), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.error) }
        if (asked == null) {
            offered.forEachIndexed { i, d ->
                androidx.compose.runtime.key(d.id) {
                    Column(Modifier.fillMaxWidth().padding(top = if (i > 0) 10.dp else 0.dp)) {
                        Text(S.appSettingsStorageNewDisk(d.name()).resolve(), style = MaterialTheme.typography.bodyMedium)
                        if (MirrorDisks.lookAlike(d, offered)) Caption(S.appSettingsStorageLookAlike().resolve())
                        else RowButton(S.appSettingsStorageUse().resolve(), enabled = !st.sending, destructive = true) { vm.confirm(d) }
                    }
                }
            }
        }
        st.failure?.let { Caption(it.resolve(), MaterialTheme.colorScheme.error) }
    }
    // Cancel is the default; erasing is the red button.
    st.confirming?.let { d ->
        cloud.offthe.otc.ui.gallery.ConfirmDialog(
            S.appSettingsStorageConfirmTitle().resolve(), S.appSettingsStorageConfirmText(d.name()).resolve(), S.appSettingsStorageConfirm().resolve(),
            onConfirm = { vm.viewModelScope.launch { vm.use(d) } }, onDismiss = { vm.confirm(null) },
        )
    }
}
