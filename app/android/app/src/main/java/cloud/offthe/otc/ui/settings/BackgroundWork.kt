// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appSettingsBackgroundCount
import cloud.offthe.otc.proto.Status

/**
 * Settings > Background work: what the device works through by itself for
 * this library (Status.background_tasks, release 152) - the analysis of new
 * uploads, the passes after an update or a switch turned on (thumbnails,
 * places, On this day's measures, Smart search's descriptions, photo
 * dates...). The section shows only while the list isn't empty, from the
 * Status answers Settings already asks for every 5 s while it is in front
 * (StatusViewModel): an older device sends none. iOS's BackgroundWork.
 */
object BackgroundWork {
    /**
     * One piece of work: the device's [text] (in the request's language,
     * shown as it comes), [done] of [total] photos or videos - [total] 0
     * when not known yet. [kind] is the device's stable name, for keys only.
     */
    data class Task(val kind: String, val text: String, val done: Long, val total: Long) {
        /** How far isn't known yet: an indeterminate bar, and no count. */
        val indeterminate: Boolean get() = total <= 0

        /** The bar's share (0-1); 0 while [indeterminate]. */
        val progress: Float get() = if (total <= 0) 0f else (done.toDouble() / total).toFloat()

        /** "4,210 of 12,800", the numbers as the app's other counts; null while [indeterminate]. */
        fun count(): UiText? =
            if (total <= 0) null else S.appSettingsBackgroundCount(done.toCountInt(), total.toCountInt())
    }

    /** The rows, in the device's order; empty for an older device, nothing to do, or no answer yet. */
    fun tasks(status: Status?): List<Task> = status?.backgroundTasksList.orEmpty().mapIndexed { i, t ->
        val total = t.total.coerceAtLeast(0)
        Task(
            kind = t.kind.ifEmpty { "#$i" }, // a key of its own, never shown
            text = t.text.trim(),
            done = if (total > 0) t.done.coerceIn(0, total) else t.done.coerceAtLeast(0),
            total = total,
        )
    }.distinctBy { it.kind }

    private fun Long.toCountInt(): Int = coerceIn(0, Int.MAX_VALUE.toLong()).toInt()
}
