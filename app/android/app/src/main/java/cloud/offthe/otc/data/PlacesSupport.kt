// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import android.content.Context
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.Settings
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * Whether Places shows (the map in Images): the signed-in device has it
 * (release 148) and its user turned it on (Settings.places_enabled, off by
 * default - the device keeps where each photo was taken, unencrypted, only
 * while it is on). iOS's PlacesSupport, the web's places.ts. Told by the
 * device's Settings - asked once after sign-in with face recognition's
 * (FaceRecognition.watch, the same answer) and whenever Settings loads -,
 * by Settings' switch, and by a map told "places_off" (turned off on
 * another app) or "unknown_payload" (a device before Places).
 *
 * Until the device has answered, the answer this phone had last time
 * stands in (the same prefs file as face recognition's), so a relaunch
 * doesn't show the map and then take it away. null (never answered, or
 * Log Out): hidden.
 */
object PlacesSupport {
    private const val PREFS = "otc_device_state"
    private const val KEY = "places"

    private fun prefs() = OTCApp.instance.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    private val _enabled = MutableStateFlow(remembered())
    /** true or false as the device said (or last said here); null: not known yet. False too on a device before Places. */
    val enabled: StateFlow<Boolean?> = _enabled

    private fun remembered(): Boolean? = try {
        val p = prefs()
        if (p.contains(KEY)) p.getBoolean(KEY, false) else null
    } catch (_: Exception) { null }

    /** What the device's Settings say: on or off - false for a device before Places (no places_enabled). */
    fun verdict(s: Settings): Boolean = s.hasPlacesEnabled() && s.placesEnabled

    /** What an answer says: null for anything but Settings (asked again). */
    fun verdict(resp: RespEnvelope?): Boolean? =
        if (resp?.payloadCase == RespEnvelope.PayloadCase.RESP_SETTINGS) verdict(resp.respSettings) else null

    /** The device's answer: its Settings, Settings' switch acknowledged, or a map told "places_off". */
    fun set(on: Boolean) {
        try { prefs().edit().putBoolean(KEY, on).apply() } catch (_: Exception) {}
        _enabled.value = on
    }

    /** Log Out: the next device says for itself. (FaceRecognition.reset clears the file they share.) */
    fun reset() {
        try { prefs().edit().remove(KEY).apply() } catch (_: Exception) {}
        _enabled.value = null
    }
}
