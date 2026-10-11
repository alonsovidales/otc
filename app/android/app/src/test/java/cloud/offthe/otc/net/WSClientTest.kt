// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.DataInputStream
import java.io.InputStream
import java.io.OutputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.security.MessageDigest
import java.util.Base64
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.concurrent.thread

// A request's own timeout (WSClient.request's timeoutMs) against a real
// WebSocket: the request fails, the socket stays, and an answer that comes
// after is dropped rather than handed to whoever asks next.
class WSClientTest {
    private val closeables = CopyOnWriteArrayList<AutoCloseable>()

    @After fun tearDown() = closeables.forEach { runCatching { it.close() } }

    private fun ack(id: Int, msg: String) = RespEnvelope.newBuilder().setId(id).setRespAck(Ack.newBuilder().setOk(true).setErrorMsg(msg)).build()

    @Test fun aRequestAnsweredInTimeGetsItsAnswer() = runBlocking {
        val server = Device { req, reply -> reply(ack(req.id, "hello ${req.id}")) }.also { closeables += it }
        val c = WSClient().also { closeables += AutoCloseable { it.close() } }
        c.connect(server.url)
        val resp = c.request(timeoutMs = 5_000) { it.setReqGetTags(cloud.offthe.otc.proto.GetTags.getDefaultInstance()) }
        assertEquals("hello 1", resp.respAck.errorMsg)
        assertEquals(0, c.pending)
    }

    @Test fun aRequestNotAnsweredInTimeFailsAndItsLateAnswerIsDropped() = runBlocking {
        val first = CountDownLatch(1)
        // The first request is answered only when the second comes, right
        // before the second's own answer: the late one arrives first.
        val server = Device { req, reply ->
            if (req.id == 1) { first.countDown(); return@Device }
            reply(ack(1, "late answer to 1"))
            reply(ack(req.id, "answer to ${req.id}"))
        }.also { closeables += it }
        val c = WSClient().also { closeables += AutoCloseable { it.close() } }
        c.connect(server.url)

        val started = System.nanoTime()
        try {
            c.request(timeoutMs = 300) { it.setReqGetTags(cloud.offthe.otc.proto.GetTags.getDefaultInstance()) }
            fail("no answer should time out")
        } catch (e: WSClient.RequestTimeout) {
            // The one that was asked: no other error.
        }
        val tookMs = (System.nanoTime() - started) / 1_000_000
        assertTrue("timed out after $tookMs ms", tookMs in 250..3_000)
        assertTrue(first.await(1, TimeUnit.SECONDS))
        // Nothing is waited on any more, and the socket is still up.
        assertEquals(0, c.pending)
        assertTrue(c.connected)

        val second = c.request(timeoutMs = 5_000) { it.setReqGetTags(cloud.offthe.otc.proto.GetTags.getDefaultInstance()) }
        assertEquals(2, second.id)
        assertEquals("answer to 2", second.respAck.errorMsg)
        assertEquals(0, c.pending)
        assertTrue(c.connected)
    }

    @Test fun eachRequestHasItsOwnTimeout() = runBlocking {
        // Nothing is ever answered: a short timeout fails first, a longer
        // one still waits.
        val server = Device { _, _ -> }.also { closeables += it }
        val c = WSClient().also { closeables += AutoCloseable { it.close() } }
        c.connect(server.url)
        val short = runCatching { c.request(timeoutMs = 200) { it.setReqGetTags(cloud.offthe.otc.proto.GetTags.getDefaultInstance()) } }
        assertTrue(short.exceptionOrNull() is WSClient.RequestTimeout)
        val long = runCatching { c.request(timeoutMs = 600) { it.setReqGetTags(cloud.offthe.otc.proto.GetTags.getDefaultInstance()) } }
        assertTrue(long.exceptionOrNull() is WSClient.RequestTimeout)
        // What a screen asks with stays well inside the socket's own limit.
        assertTrue(OTCConnection.LIST_TIMEOUT_MS < OTCConnection.PAGE_TIMEOUT_MS)
        assertTrue(OTCConnection.PAGE_TIMEOUT_MS < WSClient.DEFAULT_TIMEOUT_MS)
        // Above the bridge's own forward timeout (90 s): through the bridge
        // its "device unreachable" comes first.
        assertTrue(OTCConnection.PAGE_TIMEOUT_MS > 90_000)
    }

    @Test fun aStalledUpgradeFailsTheConnectInTime() = runBlocking {
        // The connection is taken and the upgrade never answered: OkHttp
        // alone (readTimeout 0, for the long-lived socket) waited for good.
        val server = Device(answerUpgrade = false) { _, _ -> }.also { closeables += it }
        val c = WSClient().also { closeables += AutoCloseable { it.close() } }
        var disconnects = 0
        c.onDisconnect = { disconnects++ }
        val started = System.nanoTime()
        val failure = runCatching { c.connect(server.url, timeoutMs = 300) }.exceptionOrNull()
        val tookMs = (System.nanoTime() - started) / 1_000_000
        assertTrue("failed with $failure", failure is WSClient.ConnectTimeout)
        assertTrue("took $tookMs ms", tookMs in 250..3_000)
        assertFalse(c.connected)
        // A failed connect like any other: its owner retries (OTCConnection's backoff).
        assertEquals(1, disconnects)
        // The default is bounded too, and well inside a page's time.
        assertTrue(WSClient.CONNECT_TIMEOUT_MS < OTCConnection.PAGE_TIMEOUT_MS)
    }

    @Test fun theClockStartsOnceTheRequestHasLeftThePhone() = runBlocking {
        // A big upload chunk goes first, and the device reads nothing for
        // 1.5 s: the small request behind it can't have left before then,
        // and its 600 ms only count from there.
        val server = Device(pauseReadingMs = 1_500) { req, reply -> reply(ack(req.id, "answer to ${req.id}")) }.also { closeables += it }
        val c = WSClient().also { closeables += AutoCloseable { it.close() } }
        c.connect(server.url)
        val chunk = cloud.offthe.otc.proto.UploadChunk.newBuilder().setUploadId("u").setData(com.google.protobuf.ByteString.copyFrom(ByteArray(15_000_000))).build()
        val big = async(Dispatchers.IO) { runCatching { c.request { it.setReqUploadChunk(chunk) } } }
        Thread.sleep(50)
        val started = System.nanoTime()
        val small = c.exchange(timeoutMs = 600) { it.setReqGetTags(cloud.offthe.otc.proto.GetTags.getDefaultInstance()) }
        val tookMs = (System.nanoTime() - started) / 1_000_000
        assertEquals("answer to 2", small.resp.respAck.errorMsg)
        // It waited behind the chunk, yet didn't time out; the wait for the
        // answer itself was short.
        assertTrue("took $tookMs ms", tookMs >= 1_000)
        assertTrue("waited ${small.waitedMs} ms for the answer", small.waitedMs < 600)
        assertEquals("answer to 1", big.await().getOrThrow().respAck.errorMsg)
    }

    @Test fun anAnswerSaysHowLongItWasWaitedFor() = runBlocking {
        val server = Device { req, reply -> Thread.sleep(200); reply(ack(req.id, "late")) }.also { closeables += it }
        val c = WSClient().also { closeables += AutoCloseable { it.close() } }
        c.connect(server.url)
        val a = c.exchange(timeoutMs = 5_000) { it.setReqGetTags(cloud.offthe.otc.proto.GetTags.getDefaultInstance()) }
        assertTrue("waited ${a.waitedMs} ms", a.waitedMs in 150..3_000)
    }

    @Test fun aBuildThatReplacesTheEnvelopeStillSendsLang() = runBlocking {
        // Localization: the app's language goes on every request, set after
        // build() - so a build that clears the envelope, or brings its own
        // lang, can't drop it.
        val got = CopyOnWriteArrayList<ReqEnvelope>()
        val server = Device { req, reply -> got += req; reply(ack(req.id, "ok")) }.also { closeables += it }
        val c = WSClient().also { closeables += AutoCloseable { it.close() } }
        val before = cloud.offthe.otc.i18n.LanguageSettings.wireCode
        cloud.offthe.otc.i18n.LanguageSettings.wireCode = "es"
        try {
            c.connect(server.url)
            c.request(timeoutMs = 5_000) {
                it.clear()
                it.mergeFrom(ReqEnvelope.newBuilder().setLang("zz").setReqGetTags(cloud.offthe.otc.proto.GetTags.getDefaultInstance()).build())
            }
            c.request(timeoutMs = 5_000) { it.setReqGetStatus(cloud.offthe.otc.proto.GetStatus.getDefaultInstance()) }
        } finally {
            cloud.offthe.otc.i18n.LanguageSettings.wireCode = before
        }
        assertEquals(2, got.size)
        assertEquals(listOf("es", "es"), got.map { it.lang })
        assertEquals(listOf(1, 2), got.map { it.id })
        assertTrue(got[0].hasReqGetTags())
    }

    /**
     * A stand-in device: plain ws://, a bare WebSocket upgrade, then every
     * binary message read as a ReqEnvelope and handed to [answer] with a way
     * to reply.
     */
    private class Device(
        // false: the connection is taken, the upgrade never answered.
        private val answerUpgrade: Boolean = true,
        // After the upgrade, nothing is read for this long (a busy link).
        private val pauseReadingMs: Long = 0,
        private val answer: (ReqEnvelope, (RespEnvelope) -> Unit) -> Unit,
    ) : AutoCloseable {
        private val server = ServerSocket(0, 50, InetAddress.getByName("127.0.0.1"))
        private val conns = CopyOnWriteArrayList<Socket>()
        val url get() = "ws://127.0.0.1:${server.localPort}/ws"

        init {
            thread(isDaemon = true) {
                while (!server.isClosed) {
                    val s = try { server.accept() } catch (_: Exception) { break }
                    conns += s
                    thread(isDaemon = true) { serve(s) }
                }
            }
        }

        private fun serve(s: Socket) {
            try {
                val input = DataInputStream(s.getInputStream())
                val head = readHead(input)
                if (!answerUpgrade) { while (!s.isClosed) Thread.sleep(20); return }
                val key = Regex("(?im)^Sec-WebSocket-Key:\\s*(\\S+)").find(head)?.groupValues?.get(1) ?: return
                val accept = Base64.getEncoder().encodeToString(
                    MessageDigest.getInstance("SHA-1").digest((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").toByteArray()),
                )
                val out = s.getOutputStream()
                synchronized(out) {
                    out.write("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: $accept\r\n\r\n".toByteArray())
                    out.flush()
                }
                if (pauseReadingMs > 0) Thread.sleep(pauseReadingMs)
                while (true) {
                    val (opcode, payload) = readFrame(input) ?: return
                    when (opcode) {
                        0x2 -> answer(ReqEnvelope.parseFrom(payload)) { resp -> send(out, resp.toByteArray()) }
                        0x8 -> return
                        else -> {} // pings and the like: nothing this test needs
                    }
                }
            } catch (_: Exception) {
            } finally {
                s.close()
            }
        }

        private fun send(out: OutputStream, payload: ByteArray) = synchronized(out) {
            out.write(0x82)
            when {
                payload.size < 126 -> out.write(payload.size)
                payload.size < 65536 -> { out.write(126); out.write(payload.size shr 8); out.write(payload.size and 0xff) }
                else -> { out.write(127); for (i in 7 downTo 0) out.write(((payload.size.toLong() shr (8 * i)) and 0xff).toInt()) }
            }
            out.write(payload)
            out.flush()
        }

        // A client's frame: always masked. Null at the end of the stream.
        private fun readFrame(input: DataInputStream): Pair<Int, ByteArray>? {
            val b0 = input.read()
            if (b0 < 0) return null
            val b1 = input.readUnsignedByte()
            var len = (b1 and 0x7f).toLong()
            if (len == 126L) len = input.readUnsignedShort().toLong()
            else if (len == 127L) len = input.readLong()
            val mask = ByteArray(4).also { input.readFully(it) }
            val payload = ByteArray(len.toInt()).also { input.readFully(it) }
            for (i in payload.indices) payload[i] = (payload[i].toInt() xor mask[i % 4].toInt()).toByte()
            return (b0 and 0x0f) to payload
        }

        private fun readHead(input: InputStream): String {
            val sb = StringBuilder()
            while (!sb.endsWith("\r\n\r\n")) {
                val b = input.read()
                if (b < 0) break
                sb.append(b.toChar())
            }
            return sb.toString()
        }

        override fun close() {
            server.close()
            conns.forEach { runCatching { it.close() } }
        }
    }
}
