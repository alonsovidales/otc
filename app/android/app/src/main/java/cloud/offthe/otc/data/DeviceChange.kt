// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import android.content.Context
import android.util.Log
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.ui.SyncChoice
import cloud.offthe.otc.ui.common.ThumbCache
import cloud.offthe.otc.ui.files.FilesNav
import cloud.offthe.otc.ui.gallery.PersonNav
import cloud.offthe.otc.ui.social.SocialFeedViewModel
import cloud.offthe.otc.wallpaper.Wallpaper

/**
 * Port of DeviceChange.swift. Settings › Connection's Save, or the "can't
 * connect" card's Save & Retry, with another address: another device, or
 * the same one reached another way (its bridge name, its home address,
 * Tailscale)? Until this, the photo sync went on from the last device's
 * watermark - the new device never got the photos taken before it, unless
 * Sync All - the wallpaper stopped at every address saved, and the screens
 * kept the last device's lists.
 *
 * What shows is made afresh at once ([addressChanged]): the screens and
 * what their models keep, as Log Out lets go of them, the sign-in and this
 * phone's own settings staying. Whether it is another device is told by the
 * device's own identity, its home-network certificate (the pin
 * GetLocalEndpoint answers): until the new address's device has said
 * ([pending]), no photo sync runs and the wallpaper isn't kept in step. The
 * same pin: nothing else changes, and the sync goes on. Another pin, or
 * none to compare (a device that can't say, a phone that never learnt the
 * last one's): another device - the question every sign-in to a device asks
 * (SyncChoice) is asked again, and the wallpaper stops ([apply]). Leave the
 * Bridge is the same device by definition, and no change
 * (SecretsStore.saveCredentials' sameDevice).
 */
object DeviceChange {
    private const val TAG = "OTC/DeviceChange"

    /**
     * Where the check waiting is kept: the otc_sync prefs (Log Out wipes
     * them, the backup rules leave them out), as hex - empty when the last
     * device's pin wasn't known.
     */
    interface Store {
        fun get(): String?
        fun set(value: String?)
    }

    private object Prefs : Store {
        private const val KEY = "deviceChangePin"
        private val prefs get() = OTCApp.instance.getSharedPreferences("otc_sync", Context.MODE_PRIVATE)
        override fun get(): String? = prefs.getString(KEY, null)
        override fun set(value: String?) {
            prefs.edit().apply { if (value == null) remove(KEY) else putString(KEY, value) }.apply()
        }
    }

    /** The store the app uses; tests put their own. */
    @Volatile var store: Store = Prefs

    /** The new address's device hasn't said who it is yet. */
    val pending: Boolean get() = try { store.get() != null } catch (_: UninitializedPropertyAccessException) { false }

    enum class Verdict { SAME_DEVICE, ANOTHER_DEVICE }

    /** Pure: the same device only when both pins are known and equal. */
    fun verdict(before: ByteArray, now: ByteArray?): Verdict =
        if (before.size == 32 && now != null && now.contentEquals(before)) Verdict.SAME_DEVICE else Verdict.ANOTHER_DEVICE

    /**
     * Pure: whether saving [after] over [before] is a change of device to
     * check: from one device's address to another's - not a first sign-in
     * (from none), not the address cleared, not the same address spelled
     * otherwise, and not [sameDevice] (Leave the Bridge).
     */
    fun isChange(before: String, after: String, sameDevice: Boolean): Boolean =
        !sameDevice && before.isNotEmpty() && after.isNotEmpty() && !SecretsStore.sameAddress(before, after)

    /**
     * The saved address changed: from the device whose pin is [before]
     * (null: none known). A check already waiting keeps its own: an address
     * that never answered, then the last one again, is no change.
     */
    fun begin(before: ByteArray?) {
        if (store.get() != null) return
        store.set((before ?: ByteArray(0)).joinToString("") { "%02x".format(it) })
    }

    /**
     * What the device at the new address said about itself: [pin] its
     * certificate's, null when it can't say. The verdict of the check
     * waiting, which ends; null when none was.
     */
    fun take(pin: ByteArray?): Verdict? {
        val before = store.get() ?: return null
        store.set(null)
        return verdict(unhex(before) ?: ByteArray(0), pin)
    }

    /** What a verdict does (tests watch it). */
    @Volatile var onVerdict: (Verdict) -> Unit = { act(it) }

    /** OTCConnection, once the device at the new address has said (or a device that can't say has been asked often enough). */
    fun resolve(pin: ByteArray?) {
        val v = take(pin) ?: return
        onVerdict(v)
    }

    private fun act(v: Verdict) {
        when (v) {
            Verdict.SAME_DEVICE -> {
                Log.i(TAG, "the same device at the new address")
                // The wallpaper goes on at the new address; the sync held for
                // the check goes now.
                Wallpaper.sameDeviceAt(SecretsStore.loadOrCreate().endpointURLString)
                PhotoSync.runForegroundAsync()
            }
            Verdict.ANOTHER_DEVICE -> {
                Log.i(TAG, "another device at the new address")
                apply()
            }
        }
    }

    /**
     * Another device: what this phone did for the last one stops - a run
     * still going, the wallpaper made of its photos - and what to send to
     * this one is asked, as at every sign-in to a device (SyncChoice: all, or
     * only what comes from now on).
     */
    private fun apply() {
        PhotoSync.cancel()
        SyncChoice.ask()
        Wallpaper.deviceChanged()
    }

    /**
     * Save with another address, at once: the run going stops (it read the
     * last device's watermark), the wallpaper's work under way stops writing
     * (its next requests go to the new address), and what the session's
     * models hold for the last device goes, as at Log Out - asked again from
     * the new one as MainView is made again (RootView keys it on
     * SecretsStore.deviceSession).
     */
    fun addressChanged() {
        PhotoSync.cancel()
        Wallpaper.addressChanged()
        NotificationsModel.reset()
        UploadModel.reset()
        SocialFeedViewModel.reset()
        FaceRecognition.reset()
        PlacesSupport.reset()
        MemoriesSupport.reset()
        SmartSearchSupport.reset()
        FilesNav.reset()
        PersonNav.reset()
        ThumbCache.clear()
    }

    private fun unhex(s: String): ByteArray? {
        if (s.length % 2 != 0) return null
        return try { ByteArray(s.length / 2) { i -> s.substring(2 * i, 2 * i + 2).toInt(16).toByte() } } catch (e: NumberFormatException) { null }
    }
}
