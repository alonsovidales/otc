// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import android.app.Activity
import android.content.Context
import android.content.Intent
import cloud.offthe.otc.MainActivity
import cloud.offthe.otc.data.NotificationsModel
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.data.UploadModel
import cloud.offthe.otc.net.MediaStream
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.push.FCMPush
import cloud.offthe.otc.sync.AssetSyncCache
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.sync.SyncScheduler
import cloud.offthe.otc.ui.social.SocialFeedViewModel
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Log Out, shared by Settings and the "device isn't available" card (whose
 * close button is how a phone leaves a device that died, to set a new one
 * up) - the same as AppLogOut in SettingsView.swift: stop everything that
 * talks to the device, forget what it told us, wipe what this phone
 * stores, and restart the activity on the connection screen (the
 * activity-scoped view models are the one thing a wipe of the stores
 * can't reach).
 */
fun logOut(
    context: Context, secrets: SecretsStore, unregisterPush: Boolean = true, keepDevice: Boolean = false,
    lastDevice: Pair<String, String>? = null,
) {
    // keepDevice: leaving from the "isn't available"/"can't connect"
    // screens - wiped as for Log Out, but the device's address and password
    // stay for the connection screen to fill in (SecretsStore.lastDevice):
    // lastDevice when the card's form has them, else the saved ones.
    val last = lastDevice ?: (secrets.endpoint.value to secrets.password.value)
    PhotoSync.cancel() // a sync in progress must not outlive the session
    MainScope().launch {
        // Issue #125 (#131 on iOS): tell the device to stop pushing to this
        // phone first, best effort and within seconds. Skipped when the
        // device can't be reached anyway.
        if (unregisterPush) FCMPush.unregister(context.applicationContext)
        NotificationsModel.reset()
        UploadModel.reset()
        SocialFeedViewModel.reset()
        OTCConnection.reset()
        MediaStream.reset()
        SyncScheduler.cancel()
        // Off the main thread: the wipe deletes the whole cache and files dirs.
        withContext(Dispatchers.IO) {
            AssetSyncCache.clear()
            secrets.logOut()
            if (keepDevice) SecretsStore.saveLastDevice(last.first, last.second) else SecretsStore.clearLastDevice()
        }
        val activity = context as? Activity ?: return@launch
        activity.startActivity(Intent(activity, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK))
        activity.finish()
    }
}
