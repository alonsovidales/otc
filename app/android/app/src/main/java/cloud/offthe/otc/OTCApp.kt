// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc

import android.app.Application

// Process-wide context for the singletons that need one (SecretsStore's
// encrypted preferences, WorkManager). The counterpart of what the iOS
// app gets for free from Foundation.
class OTCApp : Application() {
    override fun onCreate() {
        super.onCreate()
        instance = this
    }

    companion object {
        lateinit var instance: OTCApp
            private set
    }
}
