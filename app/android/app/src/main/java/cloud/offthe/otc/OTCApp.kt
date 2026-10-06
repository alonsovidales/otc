// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.app.Application
import cloud.offthe.otc.net.NetworkWatch
import cloud.offthe.otc.push.FCMPush

// Process-wide context for the singletons that need one (SecretsStore's
// encrypted preferences, WorkManager). The counterpart of what the iOS
// app gets for free from Foundation.
class OTCApp : Application() {
    override fun onCreate() {
        super.onCreate()
        instance = this
        // Issue #125: before any push can arrive.
        FCMPush.createChannel(this)
        // Issue #190: a network change may change the way to the device.
        NetworkWatch.start(this)
    }

    companion object {
        lateinit var instance: OTCApp
            private set
    }
}
