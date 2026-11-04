// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.atomic.AtomicInteger

// Settings' Save with another address unregisters this phone's push token
// first, for up to 3 s: run in the screen's scope, leaving Settings during
// those seconds cancelled it after the token had gone and before the
// address was saved, which then silently wasn't (OTCConnection.saveAnotherAddress).
class SaveAnotherAddressTest {
    @Test fun leavingTheScreenMeanwhileStillSaves() = runBlocking {
        val connection = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val screen = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val gate = CompletableDeferred<Unit>()
        val saved = AtomicInteger()
        val started = CompletableDeferred<Unit>()
        lateinit var job: kotlinx.coroutines.Job
        screen.launch {
            job = OTCConnection.leaveThenSave(connection, Dispatchers.Unconfined, { started.complete(Unit); gate.await() }) { saved.incrementAndGet() }
        }.join()
        started.await()
        // Settings leaves the composition while the unregister waits.
        screen.cancel()
        gate.complete(Unit)
        withTimeout(5_000) { job.join() }
        assertEquals(1, saved.get())
        connection.cancel()
    }

    @Test fun theSaveRunsEvenIfTheUnregisterIsCutShort() = runBlocking {
        val connection = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val saved = AtomicInteger()
        val started = CompletableDeferred<Unit>()
        val job = OTCConnection.leaveThenSave(connection, Dispatchers.Unconfined, { started.complete(Unit); CompletableDeferred<Unit>().await() }) { saved.incrementAndGet() }
        started.await()
        job.cancel()
        withTimeout(5_000) { job.join() }
        assertEquals(1, saved.get())
        assertTrue(job.isCancelled)
        connection.cancel()
    }
}
