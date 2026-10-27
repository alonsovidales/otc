// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appSettingsUploadsDeviceFull
import cloud.offthe.otc.i18n.appSettingsUploadsWaitingWifi
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update

/**
 * Why the photo sync isn't sending now, for Settings' Uploads (as iOS's
 * SyncHalt): "Wi-Fi only" on a metered network, or the device full
 * (storage_full, with its words).
 */
sealed interface SyncHalt {
    val title: UiText
    /** The device's own words under the line, when it gave some. */
    val detail: String? get() = null

    data object WaitingForWifi : SyncHalt {
        override val title: UiText get() = S.appSettingsUploadsWaitingWifi()
    }

    data class DeviceFull(val words: String) : SyncHalt {
        override val title: UiText get() = S.appSettingsUploadsDeviceFull()
        override val detail: String? get() = words.ifEmpty { null }
    }
}

// Port of UploadModel.swift: the photo-sync progress shown in Settings.
object UploadModel {
    data class State(
        val totalPending: Int = 0,
        val currentName: String = "",
        val progress: Float = 0f,
        val isUploading: Boolean = false,
        // Issue #30: checked between uploads, doesn't cancel one in flight.
        val isPaused: Boolean = false,
        // Why the last run stopped short of the library, until a run goes through.
        val halt: SyncHalt? = null,
    )

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state
    val isPaused: Boolean get() = _state.value.isPaused

    fun togglePause() = _state.update { it.copy(isPaused = !it.isPaused) }

    fun begin(total: Int) = _state.update {
        State(totalPending = total, progress = 0f, isUploading = total > 0, isPaused = false)
    }

    fun step(file: String, index: Int, total: Int) = _state.update {
        it.copy(
            currentName = file,
            totalPending = maxOf(0, total - index),
            progress = if (total > 0) index.toFloat() / total else 0f,
            isUploading = index < total,
        )
    }

    fun complete() = _state.update { State(progress = 1f) }

    fun halted(halt: SyncHalt?) = _state.update { it.copy(halt = halt) }

    /** Log Out. */
    fun reset() { _state.value = State() }
}
