// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

// How long a screen's request waits: more after a timeout, back down once
// answers come in time again, per kind and route.
class PatienceTest {
    private val page = OTCConnection.PAGE_TIMEOUT_MS
    private val ok = RespEnvelope.getDefaultInstance()

    @Test fun moreTimeAfterEachTimeoutUpToFiveMinutes() {
        assertEquals(listOf(120_000L, 240_000L, 300_000L, 300_000L), (0..3).map { Patience.timeoutAfter(page, it) })
        assertEquals(300_000L, Patience.timeoutAfter(page, 50))
        assertEquals(listOf(60_000L, 120_000L, 240_000L, 300_000L), (0..3).map { Patience.timeoutAfter(OTCConnection.LIST_TIMEOUT_MS, it) })
        // A longer base than the cap keeps its own.
        assertEquals(WSClient.DEFAULT_TIMEOUT_MS, Patience.timeoutAfter(WSClient.DEFAULT_TIMEOUT_MS, 3))
    }

    @Test fun anAnswerWithinTheUsualTimeBringsItBack() {
        val p = Patience()
        repeat(2) { p.timedOut("page/BRIDGE") }
        assertEquals(300_000L, p.timeoutFor("page/BRIDGE", page))
        // Pit's pages: 60-90 s. One answer is enough to be back at 2 minutes,
        // not "under half of it" as before (60 s), which a slow device
        // never got to, leaving every page 4-5 minutes for good.
        p.answered("page/BRIDGE", page, 85_000)
        assertEquals(120_000L, p.timeoutFor("page/BRIDGE", page))
    }

    @Test fun aSlowerAnswerStepsDownOnlyAsFarAsItWouldHaveFit() {
        val p = Patience()
        repeat(3) { p.timedOut("page/HOME") }
        // 150 s needed the doubled time: 4 minutes, not 5, not 2.
        p.answered("page/HOME", page, 150_000)
        assertEquals(240_000L, p.timeoutFor("page/HOME", page))
        // Never up on an answer.
        p.answered("page/HOME", page, 290_000)
        assertEquals(240_000L, p.timeoutFor("page/HOME", page))
        p.answered("page/HOME", page, 3_000)
        assertEquals(120_000L, p.timeoutFor("page/HOME", page))
    }

    @Test fun eachKindAndRouteKeepsItsOwn() {
        val p = Patience()
        p.timedOut("page/BRIDGE")
        assertEquals(240_000L, p.timeoutFor("page/BRIDGE", page))
        assertEquals(120_000L, p.timeoutFor("page/HOME", page))
        assertEquals(120_000L, p.timeoutFor("feed/BRIDGE", page))
    }

    @Test fun runCountsTimeoutsOfTheAnswerOnly() = runBlocking {
        val p = Patience()
        val limits = mutableListOf<Long>()
        suspend fun ask(fail: Throwable?, tookMs: Long = 1_000): Boolean = try {
            p.run("listing/BRIDGE", page) { limit -> limits += limit; if (fail != null) throw fail; WSClient.Answer(ok, tookMs) }
            true
        } catch (e: WSClient.RequestTimeout) { false }
        assertTrue(!ask(WSClient.RequestTimeout()))
        // Ran out while still connecting: says nothing about the device's pace.
        assertTrue(!ask(WSClient.RequestTimeout("Still connecting", answerTimedOut = false)))
        assertTrue(!ask(WSClient.RequestTimeout()))
        assertTrue(ask(null, tookMs = 30_000))
        assertTrue(ask(null))
        assertEquals(listOf(120_000L, 240_000L, 240_000L, 300_000L, 120_000L), limits)
    }
}
