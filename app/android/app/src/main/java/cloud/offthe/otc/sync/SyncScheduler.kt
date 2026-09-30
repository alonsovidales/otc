// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import android.content.Context
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import cloud.offthe.otc.OTCApp
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
object SyncScheduler {
    private const val name = "otc-photo-sync-periodic"
    private const val oldName = "otc-photo-sync" // the one-time chain, before

    fun scheduleNext(context: Context = OTCApp.instance) {
        val wm = WorkManager.getInstance(context)
        wm.cancelUniqueWork(oldName)
        val req = PeriodicWorkRequestBuilder<SyncWorker>(15, TimeUnit.MINUTES)
            .setInitialDelay(15, TimeUnit.MINUTES)
            .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
            .build()
        wm.enqueueUniquePeriodicWork(name, ExistingPeriodicWorkPolicy.UPDATE, req)
    }

    /** Log Out: nothing to sync until someone signs in again. */
    fun cancel(context: Context = OTCApp.instance) {
        WorkManager.getInstance(context).cancelUniqueWork(name)
        WorkManager.getInstance(context).cancelUniqueWork(oldName)
    }

    class SyncWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
        override suspend fun doWork(): Result =
            try { PhotoSync.runForeground(); Result.success() } catch (e: Exception) { Result.retry() }
    }
}
