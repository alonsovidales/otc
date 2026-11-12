// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import android.content.Context
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.GetSettings
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.Settings
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * Whether People shows: while face recognition is on
 * (Settings.face_recognition_enabled, off by default) or, since device
 * release 180, Pets (Settings.pets_enabled, on by default: the pets it
 * finds are people in People, unreachable otherwise) - the people in the
 * Images search, a person in it, and later its own page and menu item. A
 * device whose Settings lack pets_enabled is as before: face recognition
 * alone. The web's faceRecognition.ts, iOS's FaceRecognition. Asked once
 * after sign-in ([watch], from MainView) and set by Settings when it loads
 * or one of the two switches is turned, so People comes and goes at once.
 *
 * Until the device has answered, the answer this phone had last time
 * stands in (its own prefs file, outside the backup: it belongs to the
 * device signed in to), so a relaunch doesn't show People and then take
 * it away. With no answer ever it is null and People stays hidden until
 * the device says: face recognition is off on most devices, and People
 * appearing is gentler than People vanishing.
 */
object FaceRecognition {
    private const val PREFS = "otc_device_state"
    private const val KEY = "face_recognition"
    private const val PETS_KEY = "pets"
    private const val RETRY_FIRST_MS = 1_000L
    private const val RETRY_MAX_MS = 10_000L

    private fun prefs() = OTCApp.instance.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    // The two switches as the device said them (or last said here): face
    // recognition's, and Pets' (null: a device before it, or not known yet).
    private var faces: Boolean? = remembered(KEY)
    private var pets: Boolean? = remembered(PETS_KEY)

    private val _enabled = MutableStateFlow(peopleShown(faces, pets))
    /** Whether People shows: true while face recognition or Pets is on, false while neither is, as the device said (or last said here); null: not known yet. */
    val enabled: StateFlow<Boolean?> = _enabled

    private fun remembered(key: String): Boolean? = try {
        val p = prefs()
        if (p.contains(key)) p.getBoolean(key, false) else null
    } catch (_: Exception) { null }

    /**
     * People shows while face recognition or Pets is on; with neither on,
     * whatever face recognition's switch says (null: not known yet) - so a
     * device without Pets ([pets] null) is as before.
     */
    fun peopleShown(faces: Boolean?, pets: Boolean?): Boolean? = if (faces == true || pets == true) true else faces

    /** The device's Settings: both switches. */
    fun deviceSaid(s: Settings) = keep(s.faceRecognitionEnabled, if (s.hasPetsEnabled()) s.petsEnabled else null)

    /** Face recognition's switch, as the device acknowledged it. */
    fun set(on: Boolean) = keep(on, pets)

    /** Pets' switch, as the device acknowledged it. */
    fun setPets(on: Boolean) = keep(faces, on)

    @Synchronized
    private fun keep(faces: Boolean?, pets: Boolean?) {
        this.faces = faces
        this.pets = pets
        try {
            val e = prefs().edit()
            if (faces != null) e.putBoolean(KEY, faces) else e.remove(KEY)
            if (pets != null) e.putBoolean(PETS_KEY, pets) else e.remove(PETS_KEY)
            e.apply()
        } catch (_: Exception) {}
        _enabled.value = peopleShown(faces, pets)
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
                    deviceSaid(resp.respSettings)
                    // The same answer says whether Places is on (the map in Images).
                    PlacesSupport.set(PlacesSupport.verdict(resp.respSettings))
                    // And whether On this day is on (the memories at the top of Images).
                    MemoriesSupport.set(MemoriesSupport.verdict(resp.respSettings))
                    // And whether Smart search is on (text searches), and where its
                    // model is - and Text in photos (text searches too).
                    SmartSearchSupport.deviceSaid(resp.respSettings)
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
    @Synchronized
    fun reset() {
        try { prefs().edit().clear().apply() } catch (_: Exception) {}
        faces = null
        pets = null
        _enabled.value = null
    }
}
