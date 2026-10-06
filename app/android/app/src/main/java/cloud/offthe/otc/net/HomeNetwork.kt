// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.cancelChildren
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.joinAll
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.OkHttpClient
import java.security.MessageDigest
import java.security.cert.Certificate
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.util.concurrent.TimeUnit
import javax.net.SocketFactory
import javax.net.ssl.HostnameVerifier
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLSession
import javax.net.ssl.X509TrustManager

/**
 * Issue #190: where the device answers on the home network - its private
 * addresses, the port of its own TLS listener and the SHA-256 of that
 * listener's certificate (DER), as GetLocalEndpoint gave them through the
 * signed-in bridge session.
 */
class HomeEndpoint(val addresses: List<String>, val port: Int, val certSha256: ByteArray) {
    /** One line for the secure store; [decode] reads it back. */
    fun encode(): String = "$port|${hex(certSha256)}|${addresses.joinToString(",")}"

    companion object {
        fun decode(s: String?): HomeEndpoint? {
            val parts = s?.split("|") ?: return null
            if (parts.size != 3) return null
            val pin = unhex(parts[1]) ?: return null
            return valid(parts[2].split(","), parts[0].toIntOrNull() ?: return null, pin)
        }

        /** Only usable answers: IP literals (no name to look up), a real port, a 32-byte pin. */
        fun valid(addresses: List<String>, port: Int, pin: ByteArray): HomeEndpoint? {
            val ok = addresses.map { it.trim() }.filter { isHomeAddress(it) }.distinct()
            if (ok.isEmpty() || port !in 1..65535 || pin.size != 32) return null
            return HomeEndpoint(ok, port, pin)
        }

        private val ipv6 = Regex("""^[0-9A-Fa-f:.]+$""")

        // A home-network IP literal only (RFC 1918, IPv6 ULA fc00::/7), as
        // the device lists them and as iOS, macOS and otc-sync check: a name
        // would be looked up, and a public address is no home network.
        internal fun isHomeAddress(a: String): Boolean = if (a.contains(':')) {
            // OkHttp's parser checks the address itself; the first group
            // ("" before "::") holds the top byte.
            ipv6.matches(a) && "https://[$a]/".toHttpUrlOrNull() != null &&
                (a.substringBefore(':').ifEmpty { "0" }.toIntOrNull(16) ?: 0).let { (it shr 8) and 0xfe == 0xfc }
        } else {
            a.split('.').let { p ->
                p.size == 4 && p.all { o -> o.length in 1..3 && o.all { it in '0'..'9' } && o.toInt() <= 255 } &&
                    p[0].toInt().let { b0 -> val b1 = p[1].toInt(); b0 == 10 || (b0 == 172 && b1 and 0xf0 == 16) || (b0 == 192 && b1 == 168) }
            }
        }
    }
}

object HomeNetwork {
    /** The whole home-network attempt, every address at once, as in the other apps. */
    const val BUDGET_MS = 2_500L

    /** An IPv6 address needs brackets in a URL. */
    fun host(address: String) = if (address.contains(':')) "[$address]" else address

    fun socketURL(address: String, port: Int) = "wss://${host(address)}:$port/ws"

    /** What media URLs resolve against while the home route is in use. */
    fun origin(address: String, port: Int) = "https://${host(address)}:$port"

    /** The leaf certificate is the pinned one: its DER's SHA-256, nothing else. */
    fun matches(cert: Certificate, pin: ByteArray): Boolean =
        pin.size == 32 && MessageDigest.isEqual(MessageDigest.getInstance("SHA-256").digest(cert.encoded), pin)

    /**
     * Trusts exactly one certificate, the device's own. Its name and issuer
     * don't matter (it is self-signed, for "otc-lan"), nor its dates (a
     * phone with a wrong clock still reaches it): the pin came through the
     * signed-in bridge session. Anything else fails the TLS handshake, so
     * not a byte of the WebSocket upgrade, let alone the password, goes out.
     */
    class PinnedTrustManager(private val pin: ByteArray) : X509TrustManager {
        override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
            val leaf = chain?.firstOrNull() ?: throw CertificateException("no certificate")
            if (!matches(leaf, pin)) throw CertificateException("not this device's certificate")
        }

        override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) =
            throw CertificateException("no client certificates here")

        override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
    }

    /**
     * The address is one GetLocalEndpoint gave, and the certificate names
     * none: what identifies the device is the session's certificate being
     * the pinned one (checked again here, after the trust manager).
     */
    class PinnedHostnameVerifier(private val pin: ByteArray) : HostnameVerifier {
        override fun verify(hostname: String?, session: SSLSession?): Boolean = try {
            session?.peerCertificates?.firstOrNull()?.let { matches(it, pin) } == true
        } catch (e: Exception) {
            false
        }
    }

    /**
     * The network a home connection goes out on: [socketFactory] binds its
     * sockets to one (the home Wi-Fi even when Android sends everything
     * else over mobile data, as it does when the home internet is down);
     * null leaves them on the default network. [key] names it.
     */
    class Via(val key: String, val socketFactory: SocketFactory?) {
        companion object {
            val DEFAULT = Via("default", null)
        }
    }

    private val clients = HashMap<String, OkHttpClient>()

    /**
     * A client that talks only to the device holding [pin], over [via]: for
     * its media (ExoPlayer's OkHttpDataSource) and, through [socketClient],
     * its WebSocket. Built once per pin and network; it shares the bridge
     * client's connection pool and threads, but never a connection (OkHttp
     * keys them on the TLS setup and socket factory too).
     */
    fun client(pin: ByteArray, via: Via): OkHttpClient = synchronized(clients) {
        // A network that went away never comes back under the same key.
        if (clients.size > 16) clients.clear()
        clients.getOrPut(hex(pin) + "/" + via.key) {
            val tm = PinnedTrustManager(pin.copyOf())
            val tls = SSLContext.getInstance("TLS").apply { init(null, arrayOf(tm), null) }
            WSClient.defaultClient.newBuilder()
                .apply { via.socketFactory?.let { socketFactory(it) } }
                .sslSocketFactory(tls.socketFactory, tm)
                .hostnameVerifier(PinnedHostnameVerifier(pin.copyOf()))
                .connectTimeout(BUDGET_MS, TimeUnit.MILLISECONDS)
                // Media: OkHttp's default reads, so a stalled stream errors
                // out instead of hanging the player (the socket's are below).
                .readTimeout(10, TimeUnit.SECONDS)
                .pingInterval(0, TimeUnit.MILLISECONDS)
                .build()
        }
    }

    /** [client], set up as the long-lived socket WSClient.defaultClient is. */
    fun socketClient(pin: ByteArray, via: Via): OkHttpClient =
        client(pin, via).newBuilder().pingInterval(30, TimeUnit.SECONDS).readTimeout(0, TimeUnit.MILLISECONDS).build()

    /** A socket open to the device at [address] over [via], its pin checked; nothing sent on it yet. */
    class Opened(val socket: WSClient, val address: String, val via: Via)

    /**
     * Every address of [ep] over every one of [vias] at once; the first
     * WebSocket handshake to complete wins and the rest are closed. null
     * when none did within [budgetMs] - the caller goes through the bridge,
     * as before.
     */
    suspend fun open(ep: HomeEndpoint, vias: List<Via>, budgetMs: Long = BUDGET_MS): Opened? {
        val candidates = vias.flatMap { v -> ep.addresses.map { a -> v to a } }
        if (candidates.isEmpty()) return null
        return race(candidates, budgetMs, close = { it.socket.close() }) { (via, address) ->
            val ws = WSClient(socketClient(ep.certSha256, via))
            try {
                ws.connect(socketURL(address, ep.port))
            } catch (e: Exception) {
                ws.close()
                throw e
            }
            Opened(ws, address, via)
        }
    }

    /**
     * The first of [candidates] whose [attempt] completes within [budgetMs],
     * or null. Every other result is handed to [close], however late it
     * comes, and so is the winner when the caller is cancelled.
     */
    suspend fun <C, T : Any> race(candidates: List<C>, budgetMs: Long, close: (T) -> Unit, attempt: suspend (C) -> T): T? = coroutineScope {
        val lock = Any()
        var winner: T? = null
        var over = false
        val decided = CompletableDeferred<Unit>()
        val attempts = candidates.map { c ->
            launch {
                val r = try { attempt(c) } catch (e: Exception) { return@launch }
                val won = synchronized(lock) { (!over && winner == null).also { if (it) winner = r } }
                if (won) decided.complete(Unit) else close(r)
            }
        }
        launch { attempts.joinAll(); decided.complete(Unit) }
        try {
            withTimeoutOrNull(budgetMs) { decided.await() }
        } catch (e: CancellationException) {
            synchronized(lock) { winner.also { winner = null } }?.let(close)
            throw e
        } finally {
            // An attempt finishing from here on closes its own.
            synchronized(lock) { over = true }
            coroutineContext.cancelChildren()
        }
        synchronized(lock) { winner }
    }

    /** What to do with the home endpoint stored for this device, given its answer to GetLocalEndpoint. */
    sealed interface Answer {
        class Store(val endpoint: HomeEndpoint) : Answer
        /** Not reachable that way (no listener, no private address) or a device from before #190. */
        data object Forget : Answer
        /** Anything else (a bridge error, an odd answer): no news, keep what is stored. */
        data object Keep : Answer
    }

    fun answer(resp: RespEnvelope): Answer {
        if (!resp.error && resp.payloadCase == RespEnvelope.PayloadCase.RESP_LOCAL_ENDPOINT) {
            val le = resp.respLocalEndpoint
            return HomeEndpoint.valid(le.addressesList, le.port, le.certSha256.toByteArray())?.let { Answer.Store(it) } ?: Answer.Forget
        }
        // Builds before v9 answered an unknown request with only the message.
        if (resp.error && (resp.errorCode == "unknown_payload" || resp.errorCode == "local_unavailable" || resp.errorMessage == "unknown payload")) {
            return Answer.Forget
        }
        return Answer.Keep
    }
}

private fun hex(b: ByteArray) = b.joinToString("") { "%02x".format(it) }

private fun unhex(s: String): ByteArray? {
    if (s.length % 2 != 0) return null
    return try { ByteArray(s.length / 2) { i -> s.substring(2 * i, 2 * i + 2).toInt(16).toByte() } } catch (e: NumberFormatException) { null }
}
