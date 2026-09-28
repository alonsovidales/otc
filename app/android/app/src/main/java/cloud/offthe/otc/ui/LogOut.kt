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
import cloud.offthe.otc.sync.AssetSyncCache
import cloud.offthe.otc.sync.PhotoSync
import cloud.offthe.otc.sync.SyncScheduler
import cloud.offthe.otc.ui.social.SocialFeedViewModel

/**
 * Log Out, shared by Settings and the "device isn't available" card (whose
 * close button is how a phone leaves a device that died, to set a new one
 * up) - the same as AppLogOut in SettingsView.swift: stop everything that
 * talks to the device, forget what it told us, wipe what this phone
 * stores, and restart the activity on the connection screen (the
 * activity-scoped view models are the one thing a wipe of the stores
 * can't reach).
 */
fun logOut(context: Context, secrets: SecretsStore) {
    PhotoSync.cancel() // a sync in progress must not outlive the session
    NotificationsModel.reset()
    UploadModel.reset()
    SocialFeedViewModel.reset()
    OTCConnection.reset()
    MediaStream.reset()
    SyncScheduler.cancel()
    AssetSyncCache.clear()
    secrets.logOut()
    val activity = context as? Activity ?: return
    activity.startActivity(Intent(activity, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK))
    activity.finish()
}
