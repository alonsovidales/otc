// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.social

import cloud.offthe.otc.net.Wake
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.Friendship
import cloud.offthe.otc.proto.Friendships
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.Profile
import cloud.offthe.otc.ui.common.LoadProblem
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import java.io.IOException
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger

// Friends never reads "No friendships yet." for a list that didn't come
// (Alerts' rule, as iOS's FriendshipsTests): a spinner until the first
// answer, why it didn't come with Try again, asked again at a wake, and a
// list already shown kept while a refresh fails.
class FriendshipsTest {
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun friend(domain: String) = Friendship.newBuilder().setOriginProfile(Profile.newBuilder().setDomain(domain)).build()
    private fun list(vararg domains: String) = RespEnvelope.newBuilder().setRespFriendships(Friendships.newBuilder().addAllFriendships(domains.map { friend(it) })).build()
    private val unreachable = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setCode("device_unreachable")).build()

    // Each list asked for gets the next of [answers] (an exception is thrown); the last repeats.
    private inner class Device(vararg val answers: Any) {
        val asked = AtomicInteger()
        val send: suspend ((ReqEnvelope.Builder) -> Unit) -> RespEnvelope = { build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            check(req.payloadCase == ReqEnvelope.PayloadCase.REQ_FRIENDSHIPS_LIST)
            val a = answers[minOf(asked.getAndIncrement(), answers.size - 1)]
            if (a is Exception) throw a
            a as RespEnvelope
        }
    }

    private fun vm(dev: Device) = FriendshipsViewModel(request = dev.send, problemOf = { _, _ -> LoadProblem.FAILED }, scope = scope)

    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    @Test fun theBodysRule() {
        assertEquals(FriendsBody.LIST, friendsBody(listEmpty = false, loading = true, problem = LoadProblem.FAILED))
        assertEquals(FriendsBody.PROBLEM, friendsBody(listEmpty = true, loading = true, problem = LoadProblem.OFFLINE))
        assertEquals(FriendsBody.LOADING, friendsBody(listEmpty = true, loading = true, problem = null))
        assertEquals(FriendsBody.EMPTY, friendsBody(listEmpty = true, loading = false, problem = null))
    }

    @Test fun nothingAskedYetIsLoadingAndNoFriendsIsEmpty() {
        val vm = vm(Device(list()))
        assertEquals(FriendsBody.LOADING, vm.body)
        on { vm.reload() }
        assertEquals(FriendsBody.EMPTY, vm.body)
    }

    @Test fun aListThatDidntComeSaysSoAndComesAtAWake() {
        val dev = Device(IOException("offline"), unreachable, list("ana.off-the.cloud"))
        val vm = vm(dev)
        on { vm.reload() }
        assertEquals(FriendsBody.PROBLEM, vm.body)
        // Any answer that isn't the list is no list either (the bridge's device_unreachable).
        on { vm.reload() }
        assertEquals(FriendsBody.PROBLEM, vm.body)
        // A wake asks for it again.
        Wake.fire()
        await("the list") { vm.body == FriendsBody.LIST }
        assertEquals(3, dev.asked.get())
        assertNull(vm.state.value.loadProblem)
    }

    @Test fun aListShownStaysWhileARefreshFails() {
        val vm = vm(Device(list("ana.off-the.cloud"), IOException("offline")))
        on { vm.reload() }
        on { vm.reload() }
        assertEquals(FriendsBody.LIST, vm.body)
        assertEquals(listOf("ana.off-the.cloud"), vm.state.value.friendships.map { it.originProfile.domain })
    }
}
