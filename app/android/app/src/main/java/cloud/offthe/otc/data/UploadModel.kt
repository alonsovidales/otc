// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update

// Port of UploadModel.swift: the photo-sync progress shown in Settings.
object UploadModel {
    data class State(
        val totalPending: Int = 0,
        val currentName: String = "",
        val progress: Float = 0f,
        val isUploading: Boolean = false,
        // Issue #30: checked between uploads, doesn't cancel one in flight.
        val isPaused: Boolean = false,
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

    /** Log Out. */
    fun reset() { _state.value = State() }
}
