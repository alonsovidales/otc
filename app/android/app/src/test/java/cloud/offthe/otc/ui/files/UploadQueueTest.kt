// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appFilesCannotRead
import cloud.offthe.otc.i18n.appFilesUploading
import cloud.offthe.otc.i18n.english
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicInteger

// Files' uploads (A3, IOS-08): two at a time in the view model's scope,
// "Uploading N of M" as they go, files picked meanwhile joining the count,
// and Cancel stopping them all without one reported as failed. As
// UploadQueueTests.swift.
class UploadQueueTest {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    @After fun tearDown() { scope.cancel() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    /** A stand-in upload: each name waits for its own go, counting how many go at once. */
    private class Uploads {
        val gates = ConcurrentHashMap<String, CompletableDeferred<Unit>>()
        val now = AtomicInteger()
        val most = AtomicInteger()
        val started = CopyOnWriteArrayList<String>()
        val finished = CopyOnWriteArrayList<String>()
        val failures = CopyOnWriteArrayList<String>()
        val cancelled = CopyOnWriteArrayList<String>()
        val send: suspend (String) -> Unit = { name ->
            started += name
            most.accumulateAndGet(now.incrementAndGet(), ::maxOf)
            try {
                gates.getOrPut(name) { CompletableDeferred() }.await()
                finished += name
            } catch (e: CancellationException) {
                cancelled += name
                throw e
            } catch (e: Exception) {
                failures += name
            } finally {
                now.decrementAndGet()
            }
        }
        fun go(name: String) { gates.getOrPut(name) { CompletableDeferred() }.complete(Unit) }
    }

    @Test fun twoAtATimeCountedAsTheyGo() {
        val up = Uploads()
        val lines = CopyOnWriteArrayList<UploadProgress?>()
        val queue = UploadQueue(scope, slots = 2, send = up.send, onChange = { lines += it })
        queue.add(listOf("a", "b", "c", "d", "e"))
        await("two going") { up.started.size == 2 }
        Thread.sleep(50)
        assertEquals(2, up.started.size)
        assertEquals(UploadProgress(1, 5), lines.last())
        up.go("a")
        await("the third") { up.started.size == 3 }
        await("2 of 5") { lines.last() == UploadProgress(2, 5) }
        // Picked meanwhile: the same count.
        queue.add(listOf("f"))
        await("2 of 6") { lines.last() == UploadProgress(2, 6) }
        listOf("b", "c", "d", "e", "f").forEach(up::go)
        await("all done") { lines.last() == null }
        assertEquals(listOf("a", "b", "c", "d", "e", "f"), up.finished.sorted())
        assertEquals(2, up.most.get())
        assertTrue(up.failures.isEmpty())
        assertTrue(!queue.busy)
    }

    @Test fun cancelStopsThemAllAndNoneIsAFailure() {
        val up = Uploads()
        val lines = CopyOnWriteArrayList<UploadProgress?>()
        val queue = UploadQueue(scope, slots = 2, send = up.send, onChange = { lines += it })
        queue.add(listOf("a", "b", "c", "d"))
        await("two going") { up.started.size == 2 }
        queue.cancel()
        await("the line gone") { lines.last() == null }
        // What went stopped; what waited never went; nothing failed.
        assertEquals(setOf("a", "b"), up.cancelled.toSet())
        assertEquals(2, up.started.size)
        assertTrue(up.failures.isEmpty())
        assertTrue(up.finished.isEmpty())
        assertTrue(!queue.busy)
        // The next pick counts from one again.
        queue.add(listOf("e"))
        await("e going") { up.started.contains("e") }
        assertEquals(UploadProgress(1, 1), lines.last())
        up.go("e")
        await("done") { lines.last() == null }
    }

    @Test fun theLineAndAnUnreadableFile() {
        assertEquals("Uploading 2 of 5…", S.appFilesUploading(2, 5).english())
        // A file name is user text: between isolation marks (FSI/PDI).
        assertEquals("cannot read \u2068photo.jpg\u2069", S.appFilesCannotRead("photo.jpg").english())
    }
}
