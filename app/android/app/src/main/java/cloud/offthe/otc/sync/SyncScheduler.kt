// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import android.content.Context
import android.provider.MediaStore
import android.util.Log
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkInfo
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import cloud.offthe.otc.OTCApp
import kotlinx.coroutines.CancellationException
import java.util.concurrent.TimeUnit

// Port of SyncScheduler.swift: the background sync, via WorkManager (the
// BGProcessingTask counterpart) - every 15 minutes (WorkManager's shortest
// period) while there is a network, re-armed on every backgrounding.
//
// Periodic, not a one-time request the worker re-enqueues: the worker used
// to call scheduleNext() - enqueueUniqueWork(REPLACE) under its own name -
// as it started, which cancelled the very run doing the upload (seen live:
// "Work ... was cancelled" right after "Starting work"). UPDATE never stops
// a running worker.
//
// And a new photo or video (MediaStore changed) starts a sync within about a
// minute, when Android lets it, rather than at the next periodic run, which
// Android often puts off for much longer: a photo taken with the app closed
// went up only once it was opened. The watch is a one-shot content trigger:
// each firing arms the next one and hands the upload to a separate one-time
// sync, so a long upload (stopped and run again by WorkManager) never holds
// the watch up. WorkManager arms both again after a reboot.
object SyncScheduler {
    private const val name = "otc-photo-sync-periodic"
    private const val oldName = "otc-photo-sync" // the one-time chain, before
    private const val watchName = "otc-photo-sync-media"
    private const val soonName = "otc-photo-sync-soon"
    private const val tag = "OTC/SyncScheduler"

    fun scheduleNext(context: Context = OTCApp.instance) {
        val wm = WorkManager.getInstance(context)
        wm.cancelUniqueWork(oldName)
        val req = PeriodicWorkRequestBuilder<SyncWorker>(15, TimeUnit.MINUTES)
            .setInitialDelay(15, TimeUnit.MINUTES)
            .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
            .build()
        wm.enqueueUniquePeriodicWork(name, ExistingPeriodicWorkPolicy.UPDATE, req)
        watchMedia(context)
    }

    /** Log Out: nothing to sync until someone signs in again. */
    fun cancel(context: Context = OTCApp.instance) {
        val wm = WorkManager.getInstance(context)
        for (n in listOf(name, oldName, watchName, soonName)) wm.cancelUniqueWork(n)
    }

    /**
     * Watches MediaStore for new photos and videos. KEEP: a watch already
     * armed (or the next one, queued behind a firing) stays as it is. A
     * firing arms the next with APPEND_OR_REPLACE instead: queued behind
     * itself, it starts watching the moment the firing ends.
     */
    private fun watchMedia(context: Context, policy: ExistingWorkPolicy = ExistingWorkPolicy.KEEP) {
        val req = OneTimeWorkRequestBuilder<MediaWorker>()
            .setConstraints(
                Constraints.Builder()
                    .addContentUriTrigger(MediaStore.Images.Media.EXTERNAL_CONTENT_URI, true)
                    .addContentUriTrigger(MediaStore.Video.Media.EXTERNAL_CONTENT_URI, true)
                    // A burst of photos is one firing; a steady stream of
                    // changes still fires within a minute.
                    .setTriggerContentUpdateDelay(5, TimeUnit.SECONDS)
                    .setTriggerContentMaxDelay(1, TimeUnit.MINUTES)
                    .build(),
            )
            .build()
        WorkManager.getInstance(context).enqueueUniqueWork(watchName, policy, req)
    }

    /**
     * One sync once there is a network. One still waiting to start covers
     * this change too; one already running gets another queued behind it,
     * for what was added after it began (left to the next run).
     */
    private fun syncSoon(context: Context) {
        val wm = WorkManager.getInstance(context)
        val waiting = wm.getWorkInfosForUniqueWork(soonName).get()
            .any { it.state == WorkInfo.State.ENQUEUED || it.state == WorkInfo.State.BLOCKED }
        if (waiting) return
        val req = OneTimeWorkRequestBuilder<SoonWorker>()
            .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
            .setBackoffCriteria(BackoffPolicy.LINEAR, 30, TimeUnit.SECONDS)
            .build()
        wm.enqueueUniqueWork(soonName, ExistingWorkPolicy.APPEND_OR_REPLACE, req)
    }

    class SyncWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
        override suspend fun doWork(): Result =
            try { PhotoSync.runForeground(); Result.success() }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { Log.w(tag, "background sync failed: $e"); Result.retry() }
    }

    /** MediaStore changed: the next watch armed, the upload handed on. */
    class MediaWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
        override suspend fun doWork(): Result {
            Log.i(tag, "new photos or videos on the phone: a sync soon")
            watchMedia(applicationContext, ExistingWorkPolicy.APPEND_OR_REPLACE)
            syncSoon(applicationContext)
            return Result.success()
        }
    }

    /** The sync a new photo asked for. One already going in this process (in
     * front, or the periodic run) began before the photo came: this one
     * waits for it and goes after (retry, 30 s at a time). */
    class SoonWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
        override suspend fun doWork(): Result {
            if (PhotoSync.isSyncing) return Result.retry()
            return try { PhotoSync.runForeground(); Result.success() }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { Log.w(tag, "sync after new photos failed: $e"); Result.retry() }
        }
    }
}
