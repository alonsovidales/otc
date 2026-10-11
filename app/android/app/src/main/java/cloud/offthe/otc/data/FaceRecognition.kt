// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import android.content.Context
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.GetSettings
import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * Whether face recognition is on (Settings.face_recognition_enabled, off
 * by default): People - the people in the Images search, a person in it,
 * and later its own page and menu item - is there only while it is. The
 * web's faceRecognition.ts. Asked once after sign-in ([watch], from
 * MainView) and set by Settings when it loads or its switch is turned, so
 * People comes and goes at once.
 *
 * Until the device has answered, the answer this phone had last time
 * stands in (its own prefs file, outside the backup: it belongs to the
 * device signed in to), so a relaunch doesn't show People and then take
 * it away. With no answer ever it is null and People stays hidden until
 * the device says: it is off on most devices, and People appearing is
 * gentler than People vanishing.
 */
object FaceRecognition {
    private const val PREFS = "otc_device_state"
    private const val KEY = "face_recognition"
    private const val RETRY_FIRST_MS = 1_000L
    private const val RETRY_MAX_MS = 10_000L

    private fun prefs() = OTCApp.instance.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    private val _enabled = MutableStateFlow(remembered())
    /** true or false as the device said (or last said here); null: not known yet. */
    val enabled: StateFlow<Boolean?> = _enabled

    private fun remembered(): Boolean? = try {
        val p = prefs()
        if (p.contains(KEY)) p.getBoolean(KEY, false) else null
    } catch (_: Exception) { null }

    /** The device's answer: from Settings, or a change it acknowledged. */
    fun set(on: Boolean) {
        try { prefs().edit().putBoolean(KEY, on).apply() } catch (_: Exception) {}
        _enabled.value = on
    }

    /**
     * Asks the device until it answers: at once, then again after a
     * failure (1 s doubling to 10 s). Runs in the caller's scope, so
     * leaving the signed-in screen stops it.
     */
    suspend fun watch() {
        var fails = 0
        while (true) {
            try {
                val resp = OTCConnection.request { it.setReqGetSettings(GetSettings.getDefaultInstance()) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_SETTINGS) {
                    set(resp.respSettings.faceRecognitionEnabled)
                    // Asked once signed in: the language too.
                    cloud.offthe.otc.i18n.LanguageSettings.deviceSaid(resp.respSettings)
                    return
                }
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (_: Exception) {}
            delay(minOf(RETRY_FIRST_MS shl minOf(fails++, 4), RETRY_MAX_MS))
        }
    }

    /** Log Out: the next device says for itself. */
    fun reset() {
        try { prefs().edit().clear().apply() } catch (_: Exception) {}
        _enabled.value = null
    }
}
