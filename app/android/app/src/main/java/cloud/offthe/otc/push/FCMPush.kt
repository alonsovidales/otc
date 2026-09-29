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
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull

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
     * Log Out (issue #131's counterpart): tell the device to forget this
     * phone, best effort and within 5 seconds, then forget the token here.
     */
    suspend fun unregister(context: Context) {
        val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val token = prefs.getString(TOKEN_KEY, null)
        if (!token.isNullOrEmpty()) {
            withTimeoutOrNull(5_000) {
                try {
                    OTCConnection.request { it.setReqUnregisterFcmToken(UnregisterFcmToken.newBuilder().setToken(token)) }
                } catch (_: Exception) {}
            }
        }
        prefs.edit().remove(TOKEN_KEY).apply()
    }
}
