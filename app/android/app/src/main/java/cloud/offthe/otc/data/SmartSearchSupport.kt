// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import android.content.Context
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.proto.Settings
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * Whether Smart search is on (finding photos by a description typed in the
 * search): the signed-in device has it (release 151) and its user turned it
 * on (Settings.smart_search_enabled, off by default). Told by the device's
 * Settings - asked once after sign-in with face recognition's
 * (FaceRecognition.watch, the same answer) and whenever Settings loads -,
 * by Settings' switch, and by a search told "smart_search_off" (turned off
 * on another app). PlacesSupport's twin. Only while it is true does the
 * search offer a text search, and only then is SearchPhotos.text sent: a
 * device before it would ignore the text and answer the whole library.
 *
 * Until the device has answered, the answer this phone had last time
 * stands in (the same prefs file as face recognition's). null (never
 * answered, or Log Out): off.
 */
object SmartSearchSupport {
    private const val PREFS = "otc_device_state"
    private const val KEY = "smart_search"

    private fun prefs() = OTCApp.instance.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    private val _enabled = MutableStateFlow(remembered())
    /** true or false as the device said (or last said here); null: not known yet. False too on a device before it. */
    val enabled: StateFlow<Boolean?> = _enabled

    private fun remembered(): Boolean? = try {
        val p = prefs()
        if (p.contains(KEY)) p.getBoolean(KEY, false) else null
    } catch (_: Exception) { null }

    /** What the device's Settings say: on or off - false for a device before Smart search (no smart_search_enabled). */
    fun verdict(s: Settings): Boolean = s.hasSmartSearchEnabled() && s.smartSearchEnabled

    /** The device's answer: its Settings, Settings' switch acknowledged, or a search told "smart_search_off". */
    fun set(on: Boolean) {
        try { prefs().edit().putBoolean(KEY, on).apply() } catch (_: Exception) {}
        _enabled.value = on
    }

    /** Log Out: the next device says for itself. */
    fun reset() {
        try { prefs().edit().remove(KEY).apply() } catch (_: Exception) {}
        _enabled.value = null
    }
}
