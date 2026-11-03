// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat
import cloud.offthe.otc.MainActivity
import cloud.offthe.otc.R
import cloud.offthe.otc.i18n.LanguageSettings
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.androidBackupChannel
import cloud.offthe.otc.i18n.androidBackupChannelDescription
import cloud.offthe.otc.i18n.appShellBackupProgress
import cloud.offthe.otc.i18n.appShellBackupTitle
import cloud.offthe.otc.i18n.commonCancel

/**
 * Issue #205: a big photo sync, as a foreground job. A run started in the
 * background is a WorkManager worker, which WorkManager stops after 10
 * minutes - a 3 GB video recorded away from home never finished. While the
 * app is on screen (Android 14 lets a data-sync job start only then) and
 * more than [THRESHOLD_BYTES] is queued, the run goes on in
 * SyncScheduler.LongSyncWorker instead: a foreground service of type
 * dataSync with an ongoing notification - "Backing up N photos and videos",
 * its progress and Cancel - that WorkManager doesn't stop, and that keeps
 * the process alive after the app is left. iOS 26 does the same with
 * BGContinuedProcessingTask (LongSync.swift), with the same threshold and
 * texts.
 */
object LongSync {
    /** ~300 MB: below it a run is over in minutes, inside the app's time in front and WorkManager's 10. */
    const val THRESHOLD_BYTES = 300L shl 20

    /** The notification's Cancel pressed: not offered again until the app is next brought to the front (PhotoSync.inFront). */
    @Volatile var declined = false

    /** Pure: whether a run that has [queuedBytes] to send goes as the foreground job. */
    fun wanted(visible: Boolean, declined: Boolean, queuedBytes: Long): Boolean =
        visible && !declined && queuedBytes > THRESHOLD_BYTES

    /** The run's progress, for the notification. */
    val progress = BackupProgress()

    const val CHANNEL_ID = "backup"
    const val NOTIFICATION_ID = 205

    /** The notification's channel: quiet (a progress bar, no sound), named in the app's language - made again when it changes (OTCApp). */
    fun createChannel(context: Context) {
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        val local = LanguageSettings.localizedContext(context)
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_ID, S.androidBackupChannel().resolve(local), NotificationManager.IMPORTANCE_LOW).apply {
                description = S.androidBackupChannelDescription().resolve(local)
                setShowBadge(false)
            },
        )
    }

    /** The ongoing notification for [s]: the title, "N of M", the bar, and Cancel. */
    fun notification(context: Context, s: BackupProgress.Snapshot): Notification {
        val local = LanguageSettings.localizedContext(context)
        val open = PendingIntent.getActivity(
            context, 0,
            Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val cancel = PendingIntent.getBroadcast(
            context, 0, Intent(context, LongSyncCancel::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_otc)
            .setContentTitle(S.appShellBackupTitle(s.total).resolve(local))
            .setContentText(S.appShellBackupProgress(s.done, s.total).resolve(local))
            .setProgress(BackupProgress.PER_ASSET * s.total.coerceAtLeast(1), s.units.toInt(), s.total == 0)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setSilent(true)
            .setCategory(NotificationCompat.CATEGORY_PROGRESS)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .setContentIntent(open)
            .addAction(0, S.commonCancel().resolve(local), cancel)
            .build()
    }
}

/**
 * Where a run is, by asset: those dealt with and the share of those on
 * their way the device holds (the uploads' acknowledged bytes), so the bar
 * moves through a single long video too. Thread-safe: three assets go at
 * once. As iOS's BackupProgress.
 */
class BackupProgress {
    data class Snapshot(val done: Int, val total: Int, val units: Long)

    private val lock = Any()
    private var total = 0
    private var done = 0
    private val sending = HashMap<Long, Double>()

    /** A pass begins with [total] assets to go through. */
    fun begin(total: Int) = synchronized(lock) {
        this.total = total
        done = 0
        sending.clear()
    }

    /** [sent] of [size] bytes of asset [id] are on the device. */
    fun sending(id: Long, sent: Long, size: Long) = synchronized(lock) {
        if (size > 0) sending[id] = sent.toDouble() / size
    }

    /** Asset [id] was dealt with, whatever came of it. */
    fun done(id: Long) = synchronized(lock) {
        sending.remove(id)
        done++
    }

    fun snapshot(): Snapshot = synchronized(lock) {
        Snapshot(minOf(done, total), total, units(done, total, sending.values))
    }

    companion object {
        /** The bar's units an asset counts for. */
        const val PER_ASSET = 100

        /** Pure: the bar's position for [done] of [total] assets and the shares of those on their way, never past the end. */
        fun units(done: Int, total: Int, sending: Collection<Double>): Long {
            val partial = sending.sumOf { (it.coerceIn(0.0, 1.0) * PER_ASSET).toLong() }
            return (done.toLong() * PER_ASSET + partial).coerceIn(0, total.toLong() * PER_ASSET)
        }
    }
}

/** The notification's Cancel: the run stops at its next step, as it would at Log Out but wiping nothing, and the job ends. */
class LongSyncCancel : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        LongSync.declined = true
        PhotoSync.stopRun()
        SyncScheduler.stopLong(context)
    }
}
