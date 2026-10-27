// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appSocialAlertsAcceptFailed
import cloud.offthe.otc.i18n.appSocialAlertsLoadFailed
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.FriendShipStatus
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.LoadSubject
import cloud.offthe.otc.ui.common.loadProblemText
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException
import java.util.concurrent.atomic.AtomicInteger

// Alerts (A16, the polling, the Accept): a list that failed says why with
// Try again instead of "Nothing yet"; the unread count is asked for at
// once, on a kick (a push, the app back in front) and otherwise only every
// 45 s; an Accept the device didn't take says why. As AlertsTests.swift.
class AlertsTest {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    @After fun tearDown() { scope.cancel() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    @Test fun aListThatFailedSaysWhyInsteadOfNothingYet() {
        assertEquals(AlertsBody.PROBLEM, alertsBody(listEmpty = true, loading = false, problem = LoadProblem.UNREACHABLE))
        // Asked again meanwhile: still why, with Try again (Trying…).
        assertEquals(AlertsBody.PROBLEM, alertsBody(listEmpty = true, loading = true, problem = LoadProblem.FAILED))
        assertEquals(AlertsBody.EMPTY, alertsBody(listEmpty = true, loading = false, problem = null))
        assertEquals(AlertsBody.LOADING, alertsBody(listEmpty = true, loading = true, problem = null))
        // A list already shown stays while a refresh of it fails.
        assertEquals(AlertsBody.LIST, alertsBody(listEmpty = false, loading = false, problem = LoadProblem.OFFLINE))
        assertEquals("Couldn't load your notifications", S.appSocialAlertsLoadFailed().english())
        assertEquals("This phone is offline. Your notifications will appear once it's back online.", loadProblemText(LoadProblem.OFFLINE, LoadSubject.ALERTS).english())
        assertEquals("Your notifications will appear as soon as your device answers.", loadProblemText(LoadProblem.FAILED, LoadSubject.ALERTS).english())
    }

    @Test fun anAcceptTheDeviceDidntTakeSaysWhy() = runBlocking {
        val sent = mutableListOf<ReqEnvelope>()
        fun device(answer: () -> RespEnvelope): suspend ((ReqEnvelope.Builder) -> Unit) -> RespEnvelope = { build ->
            sent += ReqEnvelope.newBuilder().also(build).build()
            answer()
        }
        assertNull(acceptRefusal(device { RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build() }, "ana.off-the.cloud"))
        assertEquals("ana.off-the.cloud", sent.single().reqChangeFriendStatus.domain)
        assertEquals(FriendShipStatus.Accepted, sent.single().reqChangeFriendStatus.status)
        val demo = RespEnvelope.newBuilder().setError(true).setErrorMessage("Demo")
            .setRespAck(Ack.newBuilder().setOk(false).setErrorMsg("Demo").setCode("demo")).build()
        assertEquals(UiText.Raw("Demo"), acceptRefusal(device { demo }, "ana.off-the.cloud"))
        assertEquals(S.appSocialAlertsAcceptFailed(), acceptRefusal(device { RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(false)).build() }, "x"))
        assertEquals(S.appSocialAlertsAcceptFailed(), acceptRefusal(device { throw IOException("no answer") }, "x"))
        assertEquals("Couldn't accept the friend request", S.appSocialAlertsAcceptFailed().english())
    }

    @Test fun theCountIsAskedAtOnceThenOnAKickNotEveryFewSeconds() {
        val asks = AtomicInteger()
        val poll = CountPoll(scope, waitMs = 60_000) { asks.incrementAndGet() }
        poll.start()
        await("the first ask") { asks.get() == 1 }
        // Started again while running: nothing new.
        poll.start()
        Thread.sleep(100)
        assertEquals(1, asks.get())
        // A push (or the app back in front) asks at once, not in a minute.
        poll.kick()
        await("the kicked ask") { asks.get() == 2 }
        poll.kick()
        await("another") { asks.get() == 3 }
        poll.stop()
        poll.kick()
        Thread.sleep(100)
        assertEquals(3, asks.get())
        // Back in front: started again, asked at once.
        poll.start()
        await("the ask on return") { asks.get() == 4 }
        poll.stop()
    }

    @Test fun aKickDuringTheAskAsksOnceMore() {
        val asks = AtomicInteger()
        val release = java.util.concurrent.CountDownLatch(1)
        val poll = CountPoll(scope, waitMs = 60_000) {
            if (asks.incrementAndGet() == 1) release.await()
        }
        poll.start()
        await("the first ask, held") { asks.get() == 1 }
        poll.kick()
        release.countDown()
        await("the ask the kick wanted") { asks.get() == 2 }
        poll.stop()
    }

    @Test fun theWaitIsBetweenHalfAMinuteAndAMinute() {
        assertTrue(NotificationsModel.POLL_MS in 30_000L..60_000L)
    }
}
