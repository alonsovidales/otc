// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import android.content.Context
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.Settings
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * Whether On this day shows (the memories at the top of Images): the
 * signed-in device has it (release 150) and its user didn't turn it off
 * (Settings.memories_enabled, on by default). Told by the device's
 * Settings - asked once after sign-in with face recognition's
 * (FaceRecognition.watch, the same answer) and whenever Settings loads -,
 * by Settings' switch, and by memories told "memories_off" (turned off on
 * another app) or "unknown_payload" (a device before it). PlacesSupport's
 * twin.
 *
 * Until the device has answered, the answer this phone had last time
 * stands in (the same prefs file as face recognition's). null (never
 * answered, or Log Out): hidden.
 */
object MemoriesSupport {
    private const val PREFS = "otc_device_state"
    private const val KEY = "memories"

    private fun prefs() = OTCApp.instance.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    private val _enabled = MutableStateFlow(remembered())
    /** true or false as the device said (or last said here); null: not known yet. False too on a device before it. */
    val enabled: StateFlow<Boolean?> = _enabled

    private fun remembered(): Boolean? = try {
        val p = prefs()
        if (p.contains(KEY)) p.getBoolean(KEY, false) else null
    } catch (_: Exception) { null }

    /** What the device's Settings say: on or off - false for a device before On this day (no memories_enabled). */
    fun verdict(s: Settings): Boolean = s.hasMemoriesEnabled() && s.memoriesEnabled

    /** What an answer says: null for anything but Settings. */
    fun verdict(resp: RespEnvelope?): Boolean? =
        if (resp?.payloadCase == RespEnvelope.PayloadCase.RESP_SETTINGS) verdict(resp.respSettings) else null

    /** The device's answer: its Settings, Settings' switch acknowledged, or memories told "memories_off". */
    fun set(on: Boolean) {
        try { prefs().edit().putBoolean(KEY, on).apply() } catch (_: Exception) {}
        _enabled.value = on
    }

    private val _changes = MutableStateFlow(0)
    /** Bumped when something outside Images changed what the memories hold (Settings' Show hidden memories again): asked again. */
    val changes: StateFlow<Int> = _changes

    fun changed() { _changes.value += 1 }

    /** Log Out: the next device says for itself. */
    fun reset() {
        try { prefs().edit().remove(KEY).apply() } catch (_: Exception) {}
        _enabled.value = null
    }
}
