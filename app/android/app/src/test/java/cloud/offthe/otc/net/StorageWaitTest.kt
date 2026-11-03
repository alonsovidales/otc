// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.GetPubKey
import cloud.offthe.otc.proto.PubKey
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import com.google.protobuf.ByteString
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicInteger

// Issue #207 against a stand-in device on a real WebSocket: while it answers
// GetPubKey with storage_unavailable, the phone shows its words, dials
// nothing more within the hold, asks again when it ends or at once at a
// wake, and leaves the state at the first answer that isn't the code.
// OTCConnection can't run on the JVM (its stores need an Android context),
// so [Phone] makes the three moves it makes - the hold checked before
// dialling (connectAndAuth), StorageWait entered with the device's words
// (record/enterStorage), and left on any other answer (leaveStorage) -
// with the real StorageWait, ErrorCodes, WSClient and Wake.
class StorageWaitTest {
    private val closeables = CopyOnWriteArrayList<AutoCloseable>()

    @After fun tearDown() = closeables.forEach { runCatching { it.close() } }

    private val english = "Your device can't reach its storage disks. Check they're plugged in and switched on; it starts again by itself once they're back."

    private fun storageAnswer(id: Int, words: String) = RespEnvelope.newBuilder().setId(id)
        .setError(true).setErrorCode(ErrorCodes.STORAGE_UNAVAILABLE).setErrorMessage(words).build()

    @Test fun theCodeOnTheEnvelopeOrAnAckAndTheDevicesWordsOrOurs() {
        assertTrue(ErrorCodes.isStorageUnavailable(storageAnswer(1, "Disks missing")))
        val ack = RespEnvelope.newBuilder().setId(1).setRespAck(Ack.newBuilder().setCode(ErrorCodes.STORAGE_UNAVAILABLE).setErrorMsg("On an Ack")).build()
        assertTrue(ErrorCodes.isStorageUnavailable(ack))
        // The bridge's verdict and the device's other codes are something else.
        val unreachable = RespEnvelope.newBuilder().setId(1).setRespAck(Ack.newBuilder().setCode("device_unreachable")).build()
        assertFalse(ErrorCodes.isStorageUnavailable(unreachable))
        assertFalse(ErrorCodes.isStorageUnavailable(RespEnvelope.newBuilder().setError(true).setErrorCode(ErrorCodes.STORAGE_FULL).build()))
        assertFalse(ErrorCodes.isStorageUnavailable(RespEnvelope.getDefaultInstance()))

        assertEquals("Disks missing", StorageWait.text(storageAnswer(1, "Disks missing")).english())
        assertEquals("On an Ack", StorageWait.text(ack).english())
        // No words: the app's own copy.
        assertEquals(english, StorageWait.text(storageAnswer(1, "")).english())
    }

    @Test fun heldAfterTheAnswerAskedAgainAtAWakeAndLeftOnceTheDeviceAnswersOtherwise() = runBlocking {
        val device = StandIn(STORAGE).also { closeables += it }
        // A long hold: whatever is asked again before it ends is the wake's.
        val phone = Phone(device.url, holdMs = 20_000).also { closeables += it }

        // The sign-in's GetPubKey: the device's words where the card goes.
        assertTrue(runCatching { phone.connect() }.isFailure)
        assertEquals(ErrorCodes.STORAGE_UNAVAILABLE, phone.statusCode)
        assertEquals("The disks are unplugged (test)", phone.words)
        assertEquals(1, device.pubKeys.get())
        assertEquals(1, phone.dials.get())

        // Within the hold every other connect (a screen's retry, a drop's
        // backoff) fails at once with those words and dials nothing.
        repeat(3) {
            val failure = runCatching { phone.connect() }.exceptionOrNull()
            assertEquals("The disks are unplugged (test)", failure?.message)
        }
        assertEquals(1, phone.dials.get())
        assertEquals(1, device.pubKeys.get())

        // The app back in front, or a network: asked again at once. (A
        // moment first, so the re-ask loop is already waiting on the wake.)
        delay(200)
        Wake.fire()
        assertTrue("asked again after a wake", until(5_000) { device.pubKeys.get() == 2 })
        assertEquals(ErrorCodes.STORAGE_UNAVAILABLE, phone.statusCode)
        // Answered the same: held again, nothing more until the next wake.
        delay(300)
        assertEquals(2, device.pubKeys.get())
        assertTrue(phone.storage.held() != null)

        // The disks are back: the next answer is the key, and the state goes.
        device.mode = KEY
        Wake.fire()
        assertTrue("left once the device answered", until(5_000) { phone.statusCode == null })
        assertEquals(3, device.pubKeys.get())
        assertNull(phone.words)
        assertFalse(phone.storage.active)
        assertNull(phone.storage.held())
        // And nothing is asked again behind the normal sign-in's back.
        Wake.fire()
        delay(300)
        assertEquals(3, device.pubKeys.get())
    }

    @Test fun askedAgainEveryHoldWhileTheDeviceSaysSo() = runBlocking {
        val device = StandIn(STORAGE).also { closeables += it }
        val phone = Phone(device.url, holdMs = 250).also { closeables += it }
        assertTrue(runCatching { phone.connect() }.isFailure)
        // No wake: once a hold, by itself.
        assertTrue("asked again by itself", until(5_000) { device.pubKeys.get() >= 4 })
        assertEquals(ErrorCodes.STORAGE_UNAVAILABLE, phone.statusCode)

        // Anything else - here the bridge saying the device is away - ends
        // it too: that state has its own card and retries.
        device.mode = UNREACHABLE
        assertTrue("left on another answer", until(5_000) { phone.statusCode == "device_unreachable" })
        val asked = device.pubKeys.get()
        delay(800)
        assertEquals(asked, device.pubKeys.get())
        assertFalse(phone.storage.active)
    }

    @Test fun leftAtLogOutAndLiftedForNewCredentials() = runBlocking {
        val device = StandIn(STORAGE).also { closeables += it }
        val phone = Phone(device.url, holdMs = 20_000).also { closeables += it }
        assertTrue(runCatching { phone.connect() }.isFailure)
        // New credentials (OTCConnection.invalidate): the next connect dials
        // at once; the state stays until the device answers.
        phone.storage.lift()
        assertNull(phone.storage.held())
        assertTrue(phone.storage.active)
        assertTrue(runCatching { phone.connect() }.isFailure)
        assertEquals(2, phone.dials.get())
        // Log Out (OTCConnection.reset): out of it, nothing asked again.
        phone.storage.leave()
        assertFalse(phone.storage.active)
        Wake.fire()
        delay(300)
        assertEquals(2, device.pubKeys.get())
    }

    private suspend fun until(ms: Long, ok: () -> Boolean): Boolean {
        val end = System.nanoTime() + ms * 1_000_000
        while (System.nanoTime() < end) {
            if (ok()) return true
            delay(20)
        }
        return ok()
    }

    private companion object {
        const val STORAGE = 0
        const val KEY = 1
        const val UNREACHABLE = 2
    }

    /** The device: GetPubKey answered by [mode], counted (WSClientTest.Device underneath). */
    private class StandIn(@Volatile var mode: Int) : AutoCloseable {
        val pubKeys = AtomicInteger()
        private val server = WSClientTest.Device answer@{ req, reply ->
            if (req.payloadCase != ReqEnvelope.PayloadCase.REQ_GET_PUB_KEY) return@answer
            pubKeys.incrementAndGet()
            val r = RespEnvelope.newBuilder().setId(req.id)
            when (mode) {
                STORAGE -> r.setError(true).setErrorCode(ErrorCodes.STORAGE_UNAVAILABLE).setErrorMessage("The disks are unplugged (test)")
                KEY -> r.setRespPubKey(PubKey.newBuilder().setPublicKey(ByteString.copyFrom(byteArrayOf(1, 2, 3))))
                else -> r.setRespAck(Ack.newBuilder().setCode("device_unreachable").setErrorMsg("Not connected (test)"))
            }
            reply(r.build())
        }
        val url get() = server.url

        override fun close() = server.close()
    }

    /** OTCConnection's moves around a sign-in's GetPubKey, for the storage state (see the top). */
    private class Phone(private val url: String, holdMs: Long) : AutoCloseable {
        private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        val storage = StorageWait(scope, holdMs = holdMs)
        val dials = AtomicInteger()
        @Volatile var statusCode: String? = null
        @Volatile var words: String? = null

        suspend fun connect() {
            // connectAndAuth: held, nothing dialled.
            storage.held()?.let { throw IOException(it.english()) }
            dials.incrementAndGet()
            val c = WSClient()
            try {
                c.connect(url, timeoutMs = 5_000)
                val r = c.request(5_000) { it.setReqGetPubKey(GetPubKey.getDefaultInstance()) }
                if (ErrorCodes.isStorageUnavailable(r)) {
                    // record -> enterStorage.
                    val text = StorageWait.text(r)
                    statusCode = ErrorCodes.STORAGE_UNAVAILABLE
                    words = text.english()
                    storage.enter(text) { connect() }
                    throw IOException(words)
                }
                // Anything else: out of the state (leaveStorage, record).
                storage.leave()
                statusCode = if (r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) r.respAck.code.ifEmpty { null } else null
                words = null
            } finally {
                c.close()
            }
        }

        override fun close() {
            storage.leave()
            scope.cancel()
        }
    }
}
