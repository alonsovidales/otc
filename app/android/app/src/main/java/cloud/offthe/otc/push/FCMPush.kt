// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.push

import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.util.Log
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.RegisterFcmToken
import cloud.offthe.otc.proto.UnregisterFcmToken
import com.google.firebase.FirebaseApp
import com.google.firebase.messaging.FirebaseMessaging
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withTimeoutOrNull
import kotlin.coroutines.resume

/**
 * Issue #125: push notifications through Firebase Cloud Messaging - the
 * counterpart of the iOS AppDelegate's APNs registration. The app hands its
 * FCM token to the device (RegisterFcmToken), the device keeps it and
 * reports it to the bridge, and the bridge - the only place holding the
 * Firebase key - sends. The notification text is the same as on iOS: a
 * friend's name and what they did, never content.
 *
 * A build without app/google-services.json has no Firebase at all: every
 * call here is then a no-op.
 */
object FCMPush {
    const val CHANNEL_ID = "social"
    /** The data key the bridge puts on every push; a tap carrying it opens Alerts. */
    const val DATA_KEY = "otc"
    private const val TAG = "FCMPush"
    private const val PREFS = "otc_push"
    private const val TOKEN_KEY = "fcmToken"

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    fun available(context: Context): Boolean = FirebaseApp.getApps(context).isNotEmpty()

    /** The channel pushes are shown in - created once, at app start. */
    fun createChannel(context: Context) {
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_ID, "Friends and posts", NotificationManager.IMPORTANCE_DEFAULT).apply {
                description = "Friend requests, new posts, likes and comments"
            },
        )
    }

    /**
     * Issue #152: removes this app's notifications from the shade - which
     * is also what clears the number on the app icon, since Android counts
     * the notifications still there. Done when the app is opened and when
     * Alerts is, where they are read.
     */
    fun clearShown(context: Context) {
        androidx.core.app.NotificationManagerCompat.from(context).cancelAll()
    }

    /** Sends the token already known to the device (after each sign-in; the device keeps one row per token). */
    fun registerKnown(context: Context) {
        val token = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString(TOKEN_KEY, null) ?: return
        scope.launch {
            runCatching { OTCConnection.request { it.setReqRegisterFcmToken(RegisterFcmToken.newBuilder().setToken(token)) } }
                .onFailure { Log.w(TAG, "could not register the FCM token after signing in: ${it.message}") }
        }
    }

    /** Fetches this install's token and registers it with the device - once signed in. */
    fun register(context: Context) {
        if (!available(context)) return
        FirebaseMessaging.getInstance().token
            .addOnSuccessListener { token -> tokenChanged(context, token) }
            .addOnFailureListener { e -> Log.w(TAG, "could not get the FCM token", e) }
    }

    /**
     * A new or refreshed token (also OTCMessagingService.onNewToken). Sent
     * with retries: this can run before the connection has signed in, the
     * same race the iOS registration retries around.
     */
    fun tokenChanged(context: Context, token: String) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString(TOKEN_KEY, token).apply()
        scope.launch {
            var delayMs = 1_000L
            for (attempt in 1..6) {
                try {
                    val resp = OTCConnection.request { it.setReqRegisterFcmToken(RegisterFcmToken.newBuilder().setToken(token)) }
                    // An answer, but a refusal - a device older than this
                    // app (it has no FCM yet). Retrying won't change that;
                    // the next launch registers again.
                    if (resp.error) Log.w(TAG, "the device did not take the FCM token: ${resp.errorMessage}")
                    else Log.i(TAG, "registered the FCM token with the device")
                    return@launch
                } catch (e: Exception) {
                    Log.w(TAG, "could not register the FCM token (attempt $attempt/6): ${e.message}")
                    if (attempt == 6) return@launch
                    delay(delayMs)
                    delayMs = (delayMs * 2).coerceAtMost(30_000L)
                }
            }
        }
    }

    /**
     * Log Out (issue #131's counterpart) and leaving a device: tell the
     * device to forget this phone, best effort and within 5 seconds
     * ([tellDevice] false when it can't be reached), then forget the token
     * here. The token itself is deleted at FCM meanwhile: the device keeps
     * it and reports it to the bridge, and only that stops a device left
     * behind from pushing here once it is back. The next sign-in registers
     * a fresh one; the bridge prunes the dead one on its next send.
     */
    suspend fun unregister(context: Context, tellDevice: Boolean = true) = coroutineScope {
        val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val token = prefs.getString(TOKEN_KEY, null)
        val told = if (tellDevice && !token.isNullOrEmpty()) launch {
            withTimeoutOrNull(5_000) {
                try {
                    OTCConnection.request { it.setReqUnregisterFcmToken(UnregisterFcmToken.newBuilder().setToken(token)) }
                } catch (_: Exception) {}
            }
        } else null
        if (available(context)) {
            // Waited for, so a sign-in right after can't be handed the old
            // token from FCM's cache. Leaving a device was instant: kept short.
            withTimeoutOrNull(if (tellDevice) 5_000L else 2_000L) {
                suspendCancellableCoroutine<Unit> { c ->
                    FirebaseMessaging.getInstance().deleteToken().addOnCompleteListener { t ->
                        if (!t.isSuccessful) Log.w(TAG, "could not delete the FCM token: ${t.exception?.message}")
                        if (c.isActive) c.resume(Unit)
                    }
                }
            } ?: Log.w(TAG, "deleting the FCM token is taking long, not waiting")
        }
        told?.join()
        prefs.edit().remove(TOKEN_KEY).apply()
    }

    /** Drops the stored token without telling anyone (SecretsStore's restore recovery). */
    fun forgetToken(context: Context) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().remove(TOKEN_KEY).apply()
    }
}
