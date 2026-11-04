// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import android.content.Context

/**
 * How long the secure store (SecretsStore.openSecure) has been failing with
 * an error that may pass: when it first failed, and on how many launches
 * (processes - the app opened, a background sync) since. A Keystore busy
 * or not ready after an OS update passes, and the next open that works
 * forgets it ([opened]); a key the Keystore can no longer use fails the
 * same way on every launch, and would hold the owner at RootView's "can't
 * open" screen for good. Once it has failed on [STUCK_LAUNCHES] launches
 * over [STUCK_MS] at least, [failed] says to start over.
 *
 * Kept in plain preferences of their own (`otc_secrets_health`), outside
 * the encrypted store they are about and outside the backups (the
 * data-extraction rules include only otc_settings).
 */
internal class SecretsFailures(private val store: Store, private val launch: String) {
    /** The first failure, the launches it has lasted, and the last of them. */
    data class Record(val firstAt: Long, val launches: Int, val lastLaunch: String)

    interface Store {
        fun read(): Record?
        fun write(r: Record?)
    }

    /**
     * The store failed again at [now] (wall-clock ms): counted, once per
     * launch. True when it has failed for long enough to be taken as
     * unusable: on [STUCK_LAUNCHES] launches, the first [STUCK_MS] ago or
     * more. A first failure "in the future" (the clock set back since)
     * counts from now.
     */
    fun failed(now: Long): Boolean {
        val r = store.read()
        val next = when {
            r == null || r.firstAt > now -> Record(now, 1, launch)
            r.lastLaunch == launch -> r
            else -> r.copy(launches = r.launches + 1, lastLaunch = launch)
        }
        if (next != r) store.write(next)
        return next.launches >= STUCK_LAUNCHES && now - next.firstAt >= STUCK_MS
    }

    /** The store opened (or started over): whatever failed before is forgotten. */
    fun opened() {
        if (store.read() != null) store.write(null)
    }

    companion object {
        const val STUCK_LAUNCHES = 3
        const val STUCK_MS = 24L * 60 * 60 * 1000

        fun prefsStore(ctx: Context): Store = object : Store {
            private val prefs = ctx.getSharedPreferences("otc_secrets_health", Context.MODE_PRIVATE)

            override fun read(): Record? {
                val at = prefs.getLong("firstAt", -1)
                if (at < 0) return null
                return Record(at, prefs.getInt("launches", 1), prefs.getString("lastLaunch", "") ?: "")
            }

            override fun write(r: Record?) {
                val e = prefs.edit()
                if (r == null) e.clear() else e.putLong("firstAt", r.firstAt).putInt("launches", r.launches).putString("lastLaunch", r.lastLaunch)
                // commit(): a launch that is killed right after counts all the same.
                e.commit()
            }
        }
    }
}
