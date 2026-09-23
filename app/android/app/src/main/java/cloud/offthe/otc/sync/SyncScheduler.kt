// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import android.content.Context
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import cloud.offthe.otc.OTCApp
import java.util.concurrent.TimeUnit

// Port of SyncScheduler.swift: the background sync, via WorkManager (the
// BGProcessingTask counterpart) - runs once network is available, at the
// earliest 15 minutes after being asked, re-armed on every backgrounding.
object SyncScheduler {
    private const val name = "otc-photo-sync"

    fun scheduleNext(context: Context = OTCApp.instance) {
        val req = OneTimeWorkRequestBuilder<SyncWorker>()
            .setInitialDelay(15, TimeUnit.MINUTES)
            .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
            .build()
        WorkManager.getInstance(context).enqueueUniqueWork(name, ExistingWorkPolicy.REPLACE, req)
    }

    class SyncWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
        override suspend fun doWork(): Result {
            scheduleNext(applicationContext)
            return try { PhotoSync.runForeground(); Result.success() } catch (e: Exception) { Result.retry() }
        }
    }
}
