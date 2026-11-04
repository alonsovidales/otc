// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.PubKey
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import com.google.protobuf.ByteString
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException
import java.net.InetAddress
import java.net.ServerSocket
import java.security.MessageDigest
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicInteger

// A ws:// address whose device this phone has reached over its pinned link
// goes only that way (PinnedAddress, as iOS's PinnedAddressTests): never the
// password in the clear to whoever holds the address. OTCConnection can't run
// on the JVM (its stores need an Android context), so the rules are checked
// as they are, and its moves (PinnedAddress.find with HomeNetwork.contest and
// PinnedAddress.probe) against stand-in devices: the pinned listener
// (HomeNetworkTest.WsServer) and the address in the clear (WSClientTest.Device).
class PinnedAddressTest {
    private val closeables = CopyOnWriteArrayList<AutoCloseable>()
    private val pinA = sha256(HomeNetworkTest.cert(HomeNetworkTest.CERT_A).encoded)
    private val pinB = sha256(HomeNetworkTest.cert(HomeNetworkTest.CERT_B).encoded)

    @After fun tearDown() = closeables.forEach { runCatching { it.close() } }

    private fun ep(reached: Boolean, pin: ByteArray = pinA, port: Int = 8443) = HomeEndpoint(listOf("192.168.1.20"), port, pin, reached)

    @Test fun onlyAReachedWsAddressGoesThePinnedWayAlone() {
        assertTrue(PinnedAddress.pinnedOnly("ws://otc.local:8080/ws", ep(reached = true)))
        assertTrue(PinnedAddress.pinnedOnly("WS://192.168.1.20:8080/ws", ep(reached = true)))
        // Not reached yet (a first sign-in learnt it; a device whose TLS port
        // the phone can't reach), or no pin at all: as before.
        assertFalse(PinnedAddress.pinnedOnly("ws://otc.local:8080/ws", ep(reached = false)))
        assertFalse(PinnedAddress.pinnedOnly("ws://otc.local:8080/ws", null))
        // Encrypted: the address itself is safe to sign in at.
        assertFalse(PinnedAddress.pinnedOnly("wss://name.off-the.cloud/ws", ep(reached = true)))
    }

    /**
     * A pinned address's failed connect is tried again with the backoff, as
     * the endpoint's is: nothing tried again after a device restarted for an
     * update until a request, a wake or Save & Retry. Not a password turned
     * down, the device's storage, or an attempt dropped by new settings.
     */
    @Test fun aFailedPinnedConnectIsTriedAgain() {
        val unreachable = IOException("connection refused")
        assertTrue(PinnedAddress.retriesAfter(unreachable, passwordRefused = false, storageHeld = false))
        // NotKnownDevice and HomePortUnreachable: the card's words, tried again.
        assertTrue(PinnedAddress.retriesAfter(OTCConnection.RequestError("another device"), passwordRefused = false, storageHeld = false))
        assertFalse(PinnedAddress.retriesAfter(OTCConnection.RequestError("Wrong password"), passwordRefused = true, storageHeld = false))
        assertFalse(PinnedAddress.retriesAfter(OTCConnection.RequestError("no storage"), passwordRefused = false, storageHeld = true))
        assertFalse(PinnedAddress.retriesAfter(kotlinx.coroutines.CancellationException("new settings"), passwordRefused = false, storageHeld = false))
    }

    @Test fun aWsAddressRacesItsOwnHostAtThePinnedPort() {
        val stored = ep(reached = true)
        assertEquals(listOf("192.168.1.20", "otc.local"), PinnedAddress.raceTargets("ws://otc.local:8080/ws", stored).addresses)
        assertEquals(listOf("192.168.1.20", "100.101.102.103"), PinnedAddress.raceTargets("ws://100.101.102.103:8080/ws", stored).addresses)
        assertEquals(listOf("192.168.1.20", "fd00::5"), PinnedAddress.raceTargets("ws://[fd00::5]:8080/ws", stored).addresses)
        assertEquals(8443, PinnedAddress.raceTargets("ws://otc.local:8080/ws", stored).port)
        assertTrue(PinnedAddress.raceTargets("ws://otc.local:8080/ws", stored).reached)
        // Listed already, or an encrypted endpoint: what the device listed.
        assertSame(stored, PinnedAddress.raceTargets("ws://192.168.1.20:8080/ws", stored))
        assertSame(stored, PinnedAddress.raceTargets("wss://name.off-the.cloud/ws", stored))
    }

    /** DeviceChangeTests.theSameAddressSpelledOtherwiseKeepsItsPin and Leave the Bridge's move, as iOS's. */
    @Test fun theSameDeviceAtAnotherAddressKeepsItsPin() {
        val home = "ws://otc.local:8080/ws" to ep(reached = true)
        // The same address spelled otherwise (the host's case): kept for the new spelling.
        val respelled = PinnedAddress.homeAfterSave(home, "ws://otc.local:8080/ws", "ws://OTC.local:8080/ws", sameDevice = false)
        assertEquals("ws://OTC.local:8080/ws", respelled?.first)
        assertTrue(respelled!!.second.reached)
        // Leave the Bridge: the same device at its home address.
        val bridge = "wss://name.off-the.cloud/ws" to ep(reached = true)
        val left = PinnedAddress.homeAfterSave(bridge, "wss://name.off-the.cloud/ws", "ws://192.168.1.20:8080/ws", sameDevice = true)
        assertEquals("ws://192.168.1.20:8080/ws", left?.first)
        assertTrue(left!!.second.reached)
        assertTrue(PinnedAddress.pinnedOnly(left.first, left.second))
        // Another address: none.
        assertNull(PinnedAddress.homeAfterSave(home, "ws://otc.local:8080/ws", "ws://192.168.1.30:8080/ws", sameDevice = false))
        // Nothing stored for the old one: nothing to move.
        assertNull(PinnedAddress.homeAfterSave("ws://other:8080/ws" to ep(reached = true), "ws://otc.local:8080/ws", "ws://OTC.local:8080/ws", sameDevice = false))
        assertNull(PinnedAddress.homeAfterSave(null, "a", "b", sameDevice = true))
        // Already the new one's: kept as it is.
        assertSame(home, PinnedAddress.homeAfterSave(home, "ws://x:8080/ws", "ws://otc.local:8080/ws", sameDevice = false))
        assertTrue(SecretsStore.sameAddress("otc.local:8080", "WSS://OTC.LOCAL:8080/ws"))
        assertFalse(SecretsStore.sameAddress("ws://otc.local:8080/ws", "ws://otc.local:8081/ws"))
    }

    /** Media: http:// only while the session itself is in the clear. */
    @Test fun httpMediaOnlyWhileSignedInInTheClear() {
        assertTrue(MediaStream.mayUse("http://otc.local:8080/media/t", signedInInClear = true))
        assertFalse(MediaStream.mayUse("http://otc.local:8080/media/t", signedInInClear = false))
        assertTrue(MediaStream.mayUse("https://name.off-the.cloud/media/t", signedInInClear = false))
    }

    // What the stand-in at the address in the clear was sent, by kind.
    private class Seen { val pubKeys = AtomicInteger(); val auths = AtomicInteger(); val other = AtomicInteger() }

    private fun clearDevice(seen: Seen, answer: (ReqEnvelope) -> RespEnvelope?) = WSClientTest.Device { req, reply ->
        when (req.payloadCase) {
            ReqEnvelope.PayloadCase.REQ_GET_PUB_KEY -> seen.pubKeys.incrementAndGet()
            ReqEnvelope.PayloadCase.REQ_AUTH -> seen.auths.incrementAndGet()
            else -> seen.other.incrementAndGet()
        }
        answer(req)?.let(reply)
    }.also { closeables += it }

    private fun key(id: Int) = RespEnvelope.newBuilder().setId(id).setRespPubKey(PubKey.newBuilder().setPublicKey(ByteString.copyFrom(ByteArray(4)))).build()

    @Test fun theProbeAsksForTheKeyAloneAndNeverSignsIn() = runBlocking {
        val seen = Seen()
        val device = clearDevice(seen) { key(it.id) }
        assertEquals(PinnedAddress.Probe.Answered, PinnedAddress.probe(device.url, 5_000))
        assertEquals(1, seen.pubKeys.get())
        assertEquals(0, seen.auths.get())
        assertEquals(0, seen.other.get())

        // An Ack is an answer too: the network is up.
        val acks = clearDevice(Seen()) { RespEnvelope.newBuilder().setId(it.id).setRespAck(Ack.newBuilder().setOk(false)).build() }
        assertEquals(PinnedAddress.Probe.Answered, PinnedAddress.probe(acks.url, 5_000))

        // A device without its storage (issue #207).
        val storage = clearDevice(Seen()) { RespEnvelope.newBuilder().setId(it.id).setError(true).setErrorCode(ErrorCodes.STORAGE_UNAVAILABLE).setErrorMessage("unverified words").build() }
        assertEquals(PinnedAddress.Probe.Storage, PinnedAddress.probe(storage.url, 5_000))

        // Nothing listening, and a device that never answers: failed.
        val closed = ServerSocket(0, 50, InetAddress.getByName("127.0.0.1")).let { s -> s.localPort.also { s.close() } }
        assertTrue(PinnedAddress.probe("ws://127.0.0.1:$closed/ws", 5_000) is PinnedAddress.Probe.Failed)
        val silent = clearDevice(Seen()) { null }
        assertTrue(PinnedAddress.probe(silent.url, 300) is PinnedAddress.Probe.Failed)
    }

    private fun contest(winner: HomeNetwork.Opened? = null, other: Boolean = false) = HomeNetwork.Contest(winner, other)

    @Test fun findsTheLinkOrSaysWhyNot() = runBlocking {
        val races = AtomicInteger()
        val probes = AtomicInteger()
        val opened = HomeNetwork.Opened(WSClient(), "192.168.1.20", HomeNetwork.Via.DEFAULT)
        suspend fun find(vararg results: HomeNetwork.Contest, probe: PinnedAddress.Probe = PinnedAddress.Probe.Answered): PinnedAddress.Find {
            races.set(0); probes.set(0)
            return PinnedAddress.find(8443, race = { results[races.getAndIncrement()] }, probe = { probes.incrementAndGet(); probe })
        }

        // The first race wins: nothing asked at the address.
        assertTrue(find(contest(opened)) is PinnedAddress.Find.Found)
        assertEquals(0, probes.get())

        // Nothing answers at the address either: its words, no second race.
        val failed = find(contest(), probe = PinnedAddress.Probe.Failed(IOException("refused")))
        assertEquals("refused", (failed as PinnedAddress.Find.Unreachable).error.message)
        assertEquals(1, races.get())
        assertEquals(PinnedAddress.Find.Storage, find(contest(), probe = PinnedAddress.Probe.Storage))

        // It answers: raced once more.
        assertTrue(find(contest(), contest(opened)) is PinnedAddress.Find.Found)
        assertEquals(2, races.get())
        // Still nothing: another certificate seen in either race, or only silence and refusals.
        assertEquals(PinnedAddress.Find.NotKnownDevice, find(contest(other = true), contest()))
        assertEquals(PinnedAddress.Find.NotKnownDevice, find(contest(), contest(other = true)))
        assertEquals(8443, (find(contest(), contest()) as PinnedAddress.Find.HomePortUnreachable).port)
    }

    /** A missed first race (no budget) is raced again once the address answers, and the pinned link found. */
    @Test fun aMissedFirstRaceIsRacedAgain() = runBlocking {
        val pinned = HomeNetworkTest.WsServer().also { closeables += it }
        val seen = Seen()
        val clear = clearDevice(seen) { key(it.id) }
        val targets = HomeEndpoint(listOf("127.0.0.1"), pinned.port, pinA, reached = true)
        val budgets = CopyOnWriteArrayList<Long>()
        val find = PinnedAddress.find(
            targets.port,
            race = { ms -> budgets += ms; HomeNetwork.contest(targets, listOf(HomeNetwork.Via.DEFAULT), ms) },
            probe = { PinnedAddress.probe(clear.url, 5_000) },
            firstMs = 0,
        )
        assertTrue("found $find", find is PinnedAddress.Find.Found)
        (find as PinnedAddress.Find.Found).opened.socket.close()
        assertEquals(listOf(0L, HomeNetwork.SECOND_BUDGET_MS), budgets.toList())
        assertEquals(1, seen.pubKeys.get())
        assertEquals(0, seen.auths.get())
    }

    /** Once reached, a stopped TLS port says so, and nothing but GetPubKey reaches the address. */
    @Test fun aStoppedHomePortIsSaidAndThePasswordNeverSent() = runBlocking {
        val seen = Seen()
        val clear = clearDevice(seen) { key(it.id) }
        val stopped = ServerSocket(0, 50, InetAddress.getByName("127.0.0.1")).let { s -> s.localPort.also { s.close() } }
        val targets = HomeEndpoint(listOf("127.0.0.1"), stopped, pinA, reached = true)
        val find = PinnedAddress.find(
            targets.port,
            race = { ms -> HomeNetwork.contest(targets, listOf(HomeNetwork.Via.DEFAULT), ms) },
            probe = { PinnedAddress.probe(clear.url, 5_000) },
            secondMs = 500,
        )
        assertEquals(stopped, (find as PinnedAddress.Find.HomePortUnreachable).port)
        assertEquals(1, seen.pubKeys.get())
        assertEquals(0, seen.auths.get())
    }

    /** Something else at the device's port: not the device this phone knows, and nothing sent. */
    @Test fun anotherCertificateAtThePortIsNotTheKnownDevice() = runBlocking {
        val impostor = HomeNetworkTest.WsServer().also { closeables += it }
        val seen = Seen()
        val clear = clearDevice(seen) { key(it.id) }
        // The phone knows device B; whoever is at the address shows A's certificate.
        val targets = HomeEndpoint(listOf("127.0.0.1"), impostor.port, pinB, reached = true)
        val find = PinnedAddress.find(
            targets.port,
            race = { ms -> HomeNetwork.contest(targets, listOf(HomeNetwork.Via.DEFAULT), ms) },
            probe = { PinnedAddress.probe(clear.url, 5_000) },
            secondMs = 2_000,
        )
        assertEquals(PinnedAddress.Find.NotKnownDevice, find)
        assertEquals(0, impostor.upgrades.get())
        assertEquals(0, seen.auths.get())
    }

    private companion object {
        fun sha256(b: ByteArray): ByteArray = MessageDigest.getInstance("SHA-256").digest(b)
    }
}
