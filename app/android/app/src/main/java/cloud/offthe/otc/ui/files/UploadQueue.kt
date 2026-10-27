// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import java.io.InputStream

/** One file picked to upload: where it goes, its name, and how to read it. */
class UploadPick(val target: String, val name: String, val open: () -> InputStream)

/**
 * Where the picked files' uploads are: [done] is the number of the one
 * going now (from 1) - the ones finished and the next - of [total] picked,
 * the line over Files' bar ("Uploading 2 of 5…").
 */
data class UploadProgress(val done: Int, val total: Int)

/**
 * Files' uploads, as UploadQueue in UploadQueue.swift: at most [slots] at
 * once, the others waiting their turn, counted for the progress line; files
 * picked while some go join the same count. Cancel stops them all - what
 * waits never goes, what goes stops at its next request - and none of it
 * counts as failed: [send] reports its own failures and must let a
 * CancellationException through. [onChange] gets the progress, null once
 * nothing is left.
 */
class UploadQueue<T>(
    private val scope: CoroutineScope,
    slots: Int,
    private val send: suspend (T) -> Unit,
    private val onChange: (UploadProgress?) -> Unit,
) {
    private val permits = Semaphore(slots)
    private val lock = Any()
    private val jobs = LinkedHashSet<Job>()
    private var total = 0
    private var finished = 0

    /** Queued, behind those already picked. */
    fun add(items: List<T>) {
        if (items.isEmpty()) return
        val added = synchronized(lock) {
            val added = items.map { item ->
                val job = scope.launch(start = CoroutineStart.LAZY) { permits.withPermit { send(item) } }
                jobs += job
                job.invokeOnCompletion { cause -> ended(job, cancelled = cause is CancellationException) }
                job
            }
            total += items.size
            // Told under the lock, so the lines arrive in the order they happened.
            onChange(progressLocked())
            added
        }
        // Started once counted: one that ends at once still finds itself in jobs.
        added.forEach { it.start() }
    }

    /** Cancel: nothing more goes up; nothing is reported for it. */
    fun cancel() {
        synchronized(lock) { jobs.toList() }.forEach { it.cancel() }
    }

    /** Whether any is waiting or going. */
    val busy: Boolean get() = synchronized(lock) { jobs.isNotEmpty() }

    private fun ended(job: Job, cancelled: Boolean) = synchronized(lock) {
        if (!jobs.remove(job)) return
        if (!cancelled) finished += 1
        if (jobs.isEmpty()) {
            total = 0
            finished = 0
            onChange(null)
        } else onChange(progressLocked())
    }

    private fun progressLocked() = UploadProgress(minOf(finished + 1, total), total)
}
