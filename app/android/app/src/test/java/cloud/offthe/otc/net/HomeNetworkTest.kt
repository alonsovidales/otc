// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.LocalEndpoint
import cloud.offthe.otc.proto.RespEnvelope
import com.google.protobuf.ByteString
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.async
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.InputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.security.KeyFactory
import java.security.KeyStore
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import java.security.spec.PKCS8EncodedKeySpec
import java.util.Base64
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicInteger
import javax.net.ssl.KeyManagerFactory
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLServerSocket
import javax.net.ssl.SSLSocket
import kotlin.concurrent.thread

// Issue #190: the pin check, the endpoint the device gives, the race over
// its addresses and a real pinned TLS WebSocket to a stand-in device.
class HomeNetworkTest {
    private val certA = cert(CERT_A)
    private val certB = cert(CERT_B)
    private val pinA = sha256(certA.encoded)
    private val pinB = sha256(certB.encoded)
    private val closeables = ArrayList<AutoCloseable>()

    @After fun tearDown() = closeables.forEach { runCatching { it.close() } }

    @Test fun pinIsTheSha256OfTheLeafDer() {
        assertEquals("034656788075af10e4a1d464104551134d2d2fde2fd476a7709bfe7732714ff2", pinA.joinToString("") { "%02x".format(it) })
        assertTrue(HomeNetwork.matches(certA, pinA))
        assertFalse(HomeNetwork.matches(certB, pinA))
        assertFalse(HomeNetwork.matches(certA, pinA.copyOf(31)))
    }

    @Test fun trustManagerAcceptsOnlyThePinnedCertificate() {
        val tm = HomeNetwork.PinnedTrustManager(pinA)
        tm.checkServerTrusted(arrayOf(certA), "ECDHE_ECDSA")
        // Its own certificate first, whatever else the chain carries.
        tm.checkServerTrusted(arrayOf(certA, certB), "ECDHE_ECDSA")
        for (chain in listOf(arrayOf(certB), arrayOf(certB, certA), emptyArray(), null)) {
            try {
                tm.checkServerTrusted(chain, "ECDHE_ECDSA")
                fail("accepted ${chain?.size} certificate(s) without the pinned one first")
            } catch (_: CertificateException) {}
        }
        try {
            tm.checkClientTrusted(arrayOf(certA), "ECDHE_ECDSA")
            fail("trusted a client")
        } catch (_: CertificateException) {}
        assertEquals(0, tm.acceptedIssuers.size)
        try {
            HomeNetwork.PinnedTrustManager(pinB).checkServerTrusted(arrayOf(certA), "ECDHE_ECDSA")
            fail("accepted another device's certificate")
        } catch (_: CertificateException) {}
    }

    @Test fun endpointKeepsOnlyUsableAnswers() {
        val ep = HomeEndpoint.valid(listOf("192.168.1.20", " fd00::1 ", "otc.local", "999.1.1.1", "1.2.3", "fd00::1::2", "10.0.0.5", "192.168.1.20", ""), 8443, pinA)!!
        assertEquals(listOf("192.168.1.20", "fd00::1", "10.0.0.5"), ep.addresses)
        assertNull(HomeEndpoint.valid(listOf("otc.local"), 8443, pinA))
        assertNull(HomeEndpoint.valid(listOf("192.168.1.20"), 0, pinA))
        assertNull(HomeEndpoint.valid(listOf("192.168.1.20"), 70000, pinA))
        assertNull(HomeEndpoint.valid(listOf("192.168.1.20"), 8443, pinA.copyOf(16)))

        val back = HomeEndpoint.decode(ep.encode())!!
        assertEquals(ep.addresses, back.addresses)
        assertEquals(8443, back.port)
        assertTrue(back.certSha256.contentEquals(pinA))
        for (bad in listOf(null, "", "8443|zz|192.168.1.20", "x|${hex(pinA)}|192.168.1.20", "8443|${hex(pinA)}")) assertNull(HomeEndpoint.decode(bad))
    }

    @Test fun urlsBracketIPv6() {
        assertEquals("wss://192.168.1.20:8443/ws", HomeNetwork.socketURL("192.168.1.20", 8443))
        assertEquals("wss://[fd00::1]:8444/ws", HomeNetwork.socketURL("fd00::1", 8444))
        assertEquals("https://[fd00::1]:8443", HomeNetwork.origin("fd00::1", 8443))
    }

    @Test fun answerToGetLocalEndpoint() {
        fun resp(b: RespEnvelope.Builder.() -> Unit) = RespEnvelope.newBuilder().apply(b).build()
        val ok = resp {
            respLocalEndpoint = LocalEndpoint.newBuilder().addAddresses("192.168.1.20").setPort(8443).setCertSha256(ByteString.copyFrom(pinA)).build()
        }
        val stored = HomeNetwork.answer(ok) as HomeNetwork.Answer.Store
        assertEquals(listOf("192.168.1.20"), stored.endpoint.addresses)
        // A device before #190, one with no way in at home, and builds before v9.
        assertEquals(HomeNetwork.Answer.Forget, HomeNetwork.answer(resp { error = true; errorCode = "unknown_payload" }))
        assertEquals(HomeNetwork.Answer.Forget, HomeNetwork.answer(resp { error = true; errorCode = "local_unavailable"; errorMessage = "no listener" }))
        assertEquals(HomeNetwork.Answer.Forget, HomeNetwork.answer(resp { error = true; errorMessage = "unknown payload" }))
        // Nothing usable in it: nothing to try.
        assertEquals(HomeNetwork.Answer.Forget, HomeNetwork.answer(resp {
            respLocalEndpoint = LocalEndpoint.newBuilder().addAddresses("otc.local").setPort(8443).setCertSha256(ByteString.copyFrom(pinA)).build()
        }))
        // Anything else is no news.
        assertEquals(HomeNetwork.Answer.Keep, HomeNetwork.answer(resp { error = true; errorCode = "not_authenticated" }))
        assertEquals(HomeNetwork.Answer.Keep, HomeNetwork.answer(resp { error = true; errorCode = "device_unreachable" }))
        // A message that comes with another code is never read.
        assertEquals(HomeNetwork.Answer.Keep, HomeNetwork.answer(resp { error = true; errorCode = "device_unreachable"; errorMessage = "unknown payload" }))
        assertEquals(HomeNetwork.Answer.Keep, HomeNetwork.answer(resp {}))
    }

    @Test fun mobileDataAloneIsNotTried() {
        assertFalse(NetworkWatch.tryDefault(cellular = true, vpn = false))
        assertTrue(NetworkWatch.tryDefault(cellular = true, vpn = true))
        assertTrue(NetworkWatch.tryDefault(cellular = false, vpn = false))
    }

    private class Conn(val name: String) { @Volatile var closed = false }

    @Test fun raceTakesTheFirstAndClosesTheRest() = runBlocking {
        val made = CopyOnWriteArrayList<Conn>()
        val won = HomeNetwork.race(listOf(300L, 50L, 150L), 2_000, close = { it.closed = true }) { ms ->
            delay(ms)
            Conn("$ms").also { made += it }
        }
        assertEquals("50", won!!.name)
        assertFalse(won.closed)
        // The slower ones were cancelled before they finished, or closed when they did.
        assertTrue(made.filter { it !== won }.all { it.closed })
    }

    @Test fun raceSkipsFailuresAndGivesUpAtTheBudget() = runBlocking {
        val won = HomeNetwork.race(listOf(true, false), 2_000, close = { _: Conn -> }) { fails ->
            if (fails) throw java.io.IOException("refused") else Conn("ok")
        }
        assertEquals("ok", won!!.name)
        assertNull(HomeNetwork.race(listOf(1, 2), 2_000, close = { _: Conn -> }) { throw java.io.IOException("refused") })
        assertNull(HomeNetwork.race(emptyList<Int>(), 2_000, close = { _: Conn -> }) { Conn("never") })

        val late = CopyOnWriteArrayList<Conn>()
        val start = System.nanoTime()
        assertNull(HomeNetwork.race(listOf(1), 200, close = { it.closed = true }) {
            try { delay(5_000) } catch (e: CancellationException) { late += Conn("cancelled"); throw e }
            Conn("late")
        })
        assertTrue("took ${(System.nanoTime() - start) / 1_000_000} ms", System.nanoTime() - start < 1_500_000_000L)
        assertEquals(1, late.size)
    }

    @Test fun raceStopsEveryAttemptWhenTheCallerIsCancelled() = runBlocking {
        val stopped = AtomicInteger()
        val job = async {
            HomeNetwork.race(listOf(1, 2), 5_000, close = { _: Conn -> }) {
                try { delay(5_000) } catch (e: CancellationException) { stopped.incrementAndGet(); throw e }
                Conn("late")
            }
        }
        delay(100)
        job.cancel()
        job.join()
        assertEquals(2, stopped.get())
    }

    @Test fun pinnedClientIsBuiltOncePerPinAndNetwork() {
        val a = HomeNetwork.client(pinA, HomeNetwork.Via.DEFAULT)
        assertSame(a, HomeNetwork.client(pinA.copyOf(), HomeNetwork.Via.DEFAULT))
        assertTrue(a !== HomeNetwork.client(pinB, HomeNetwork.Via.DEFAULT))
        assertTrue(a !== HomeNetwork.client(pinA, HomeNetwork.Via("net:100", null)))
    }

    @Test fun socketOpensOnlyToThePinnedDevice() = runBlocking {
        val device = WsServer().also { closeables += it }

        val ws = WSClient(HomeNetwork.socketClient(pinA, HomeNetwork.Via.DEFAULT))
        ws.connect("wss://127.0.0.1:${device.port}/ws")
        assertTrue(ws.connected)
        ws.close()
        assertEquals(1, device.upgrades.get())

        // Another certificate at that address: refused in the TLS handshake,
        // before the WebSocket upgrade (or anything else) is sent.
        val impostor = WSClient(HomeNetwork.socketClient(pinB, HomeNetwork.Via.DEFAULT))
        try {
            impostor.connect("wss://127.0.0.1:${device.port}/ws")
            fail("connected to a device with another certificate")
        } catch (e: Exception) {
            assertFalse(e is CancellationException)
        }
        assertFalse(impostor.connected)
        Thread.sleep(200)
        assertEquals(1, device.upgrades.get())
    }

    @Test fun openRacesTheAddressesOverTLS() = runBlocking {
        val device = WsServer().also { closeables += it }
        // Nothing answers at [::1] on that port; the device is at 127.0.0.1.
        val ep = HomeEndpoint(listOf("::1", "127.0.0.1"), device.port, pinA)
        val opened = HomeNetwork.open(ep, listOf(HomeNetwork.Via.DEFAULT))
        assertNotNull(opened)
        assertEquals("127.0.0.1", opened!!.address)
        assertTrue(opened.socket.connected)
        opened.socket.close()

        // The wrong pin: nothing opens, within the budget.
        assertNull(HomeNetwork.open(HomeEndpoint(listOf("127.0.0.1"), device.port, pinB), listOf(HomeNetwork.Via.DEFAULT)))
        assertEquals(1, device.upgrades.get())

        // A host that takes the connection and never answers.
        val silent = ServerSocket(0, 50, InetAddress.getByName("127.0.0.1")).also { closeables += it }
        val held = CopyOnWriteArrayList<Socket>()
        thread(isDaemon = true) { while (!silent.isClosed) try { held += silent.accept() } catch (_: Exception) { break } }
        closeables += AutoCloseable { held.forEach { it.close() } }
        val start = System.nanoTime()
        assertNull(HomeNetwork.open(HomeEndpoint(listOf("127.0.0.1"), silent.localPort, pinA), listOf(HomeNetwork.Via.DEFAULT), budgetMs = 300))
        assertTrue(System.nanoTime() - start < 2_000_000_000L)
    }

    // A stand-in for the device's home-network listener: TLS with cert A,
    // then a bare WebSocket upgrade.
    private class WsServer : AutoCloseable {
        private val server: SSLServerSocket
        val upgrades = AtomicInteger()
        private val conns = CopyOnWriteArrayList<Socket>()

        init {
            val key = KeyFactory.getInstance("EC").generatePrivate(PKCS8EncodedKeySpec(pem(KEY_A)))
            val ks = KeyStore.getInstance("PKCS12").apply {
                load(null, null)
                setKeyEntry("device", key, "x".toCharArray(), arrayOf(cert(CERT_A)))
            }
            val kmf = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm()).apply { init(ks, "x".toCharArray()) }
            val ctx = SSLContext.getInstance("TLS").apply { init(kmf.keyManagers, null, null) }
            server = ctx.serverSocketFactory.createServerSocket(0, 50, InetAddress.getByName("127.0.0.1")) as SSLServerSocket
            thread(isDaemon = true) {
                while (!server.isClosed) {
                    val s = try { server.accept() as SSLSocket } catch (_: Exception) { break }
                    conns += s
                    thread(isDaemon = true) { serve(s) }
                }
            }
        }

        val port: Int get() = server.localPort

        private fun serve(s: SSLSocket) {
            try {
                s.startHandshake()
                val head = readHead(s.inputStream)
                val key = Regex("(?im)^Sec-WebSocket-Key:\\s*(\\S+)").find(head)?.groupValues?.get(1) ?: return
                upgrades.incrementAndGet()
                val accept = Base64.getEncoder().encodeToString(
                    MessageDigest.getInstance("SHA-1").digest((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").toByteArray()),
                )
                s.outputStream.write("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: $accept\r\n\r\n".toByteArray())
                s.outputStream.flush()
                while (s.inputStream.read() >= 0) {}
            } catch (_: Exception) {
            } finally {
                s.close()
            }
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

    private companion object {
        fun pem(s: String): ByteArray = Base64.getMimeDecoder().decode(s.lines().filter { !it.startsWith("-----") }.joinToString(""))

        fun cert(s: String) = CertificateFactory.getInstance("X.509").generateCertificate(s.byteInputStream()) as X509Certificate

        fun sha256(b: ByteArray): ByteArray = MessageDigest.getInstance("SHA-256").digest(b)

        fun hex(b: ByteArray) = b.joinToString("") { "%02x".format(it) }

        // Two self-signed P-256 certificates for "otc-lan", 100 years, as
        // the device makes; A's key serves the stand-in device. Test-only.
        const val KEY_A = """-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQg/y54gX/8gpHrM0tG
BdziQhT1fL0tBQXHgAOhSTTkFl6hRANCAATzLXXdsqlP6My7QDyiCvYrZqdNRbqX
Nff4h8TvATpj8GA+BkbYFHRLzbmqEEXxMpz8AG/eLJPkyD0mjiNHP+qY
-----END PRIVATE KEY-----"""

        const val CERT_A = """-----BEGIN CERTIFICATE-----
MIIBpTCCAUqgAwIBAgIUAVl2cRL4KqSXReSu+DkF+0kr9d4wCgYIKoZIzj0EAwIw
EjEQMA4GA1UEAwwHb3RjLWxhbjAgFw0yNjEwMDYwODQ4NTBaGA8yMTI2MDkxMjA4
NDg1MFowEjEQMA4GA1UEAwwHb3RjLWxhbjBZMBMGByqGSM49AgEGCCqGSM49AwEH
A0IABPMtdd2yqU/ozLtAPKIK9itmp01Fupc19/iHxO8BOmPwYD4GRtgUdEvNuaoQ
RfEynPwAb94sk+TIPSaOI0c/6pijfDB6MB0GA1UdDgQWBBQtSfDHEI0DhgbnE5tm
QPOv+OSs1jAfBgNVHSMEGDAWgBQtSfDHEI0DhgbnE5tmQPOv+OSs1jAPBgNVHRMB
Af8EBTADAQH/MBIGA1UdEQQLMAmCB290Yy1sYW4wEwYDVR0lBAwwCgYIKwYBBQUH
AwEwCgYIKoZIzj0EAwIDSQAwRgIhAOrclR4oWS3WJec3ZC6YYsUEC/dJQ5rBw9h9
SSQgLuj7AiEAtroZMf2iI7puheKt2yEN6KUElfRGUvZArUJq3o3hsIA=
-----END CERTIFICATE-----"""

        const val CERT_B = """-----BEGIN CERTIFICATE-----
MIIBpTCCAUqgAwIBAgIUK7SrMnvC/ntveta7ESF+qoSK8HkwCgYIKoZIzj0EAwIw
EjEQMA4GA1UEAwwHb3RjLWxhbjAgFw0yNjEwMDYwODQ4NTBaGA8yMTI2MDkxMjA4
NDg1MFowEjEQMA4GA1UEAwwHb3RjLWxhbjBZMBMGByqGSM49AgEGCCqGSM49AwEH
A0IABHoQZ4e9V6xLtNMKsX0Y6Hmway2gi+ZkGYYqi8Tksx/TlCYbAh/n8vcwKMgp
7OMaQqzeAjjNFjzujisEOqvElfWjfDB6MB0GA1UdDgQWBBTw1i96M1EF9darOhUB
P4k7m3UpbDAfBgNVHSMEGDAWgBTw1i96M1EF9darOhUBP4k7m3UpbDAPBgNVHRMB
Af8EBTADAQH/MBIGA1UdEQQLMAmCB290Yy1sYW4wEwYDVR0lBAwwCgYIKwYBBQUH
AwEwCgYIKoZIzj0EAwIDSQAwRgIhAL1gZHuyZBEi2dKT+KwbHpgeghvMsaN6zyZG
njhwy7UGAiEA0Pk6llnsnDCvq3TLaFIrvgoxHrkvrP6IH2swzY5LM2Y=
-----END CERTIFICATE-----"""
    }
}
