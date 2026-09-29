// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.push

import android.Manifest
import android.app.PendingIntent
import android.content.Intent
import android.content.pm.PackageManager
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import cloud.offthe.otc.MainActivity
import cloud.offthe.otc.R
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage

/**
 * Issue #125: FCM's entry points. In the background Android shows a push
 * by itself (from its notification part) and a tap opens MainActivity with
 * the data keys as extras; in the foreground it is handed here instead,
 * and shown the same way.
 */
class OTCMessagingService : FirebaseMessagingService() {
    override fun onNewToken(token: String) {
        FCMPush.tokenChanged(applicationContext, token)
    }

    override fun onMessageReceived(message: RemoteMessage) {
        val n = message.notification ?: return
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return
        val open = Intent(this, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)
            .putExtra(FCMPush.DATA_KEY, "notification")
        val tap = PendingIntent.getActivity(this, 0, open, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val shown = NotificationCompat.Builder(this, FCMPush.CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_otc)
            .setContentTitle(n.title)
            .setContentText(n.body)
            .setAutoCancel(true)
            .setContentIntent(tap)
            .build()
        NotificationManagerCompat.from(this).notify(message.messageId?.hashCode() ?: System.currentTimeMillis().toInt(), shown)
    }
}
