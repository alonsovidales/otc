// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withTimeoutOrNull
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString
import java.io.IOException
import java.util.concurrent.TimeUnit
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

// Port of WSClient.swift: one WebSocket, requests correlated with
// responses by envelope id. OkHttp delivers listener callbacks on its own
// threads, so every access to the waiters map goes through the mutex -
// the same reason the Swift version is an actor.
//
// One per connection attempt (issue #190): the home-network race opens
// several at once, and a route switch signs in on the next socket before
// the one in use goes. [client] is the bridge's, or one pinned to the
// device's own certificate (HomeNetwork).
class WSClient(private val client: OkHttpClient = defaultClient) {
    // socket and gen are guarded by the waiters monitor, like the map.
    private var socket: WebSocket? = null
    @Volatile var connected = false
        private set
    private var nextId = 1
    private val waiters = HashMap<Int, (Result<RespEnvelope>) -> Unit>()
    private val lock = Mutex()
    // Which socket is the current one: bumped by connect(), close() and
    // failAndClose(), so a replaced socket's late callbacks (OkHttp delivers
    // them on its own threads) can't fail the new one's requests or close it.
    private var gen = 0

    companion object {
        // As the macOS client and otc-sync: long enough for ApplyUpdate and a
        // 4 MiB chunk on a slow link, but a request can no longer hang forever.
        private const val requestTimeoutMs = 30 * 60_000L

        /** Every socket's, to the configured endpoint; one pool and dispatcher for all. */
        val defaultClient: OkHttpClient = OkHttpClient.Builder()
            .pingInterval(30, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS) // long-lived socket
            .build()
    }

    /** Requests sent and not answered yet. */
    val pending: Int get() = synchronized(waiters) { waiters.size }

    /** Fired once, when the socket breaks. The owner reconnects; this class never retries. */
    @Volatile var onDisconnect: (() -> Unit)? = null

    suspend fun connect(url: String) {
        if (connected) return
        val opened = CompletableDeferred<Unit>()
        val req = Request.Builder().url(url).build()
        val myGen = synchronized(waiters) { ++gen }
        fun current() = synchronized(waiters) { gen == myGen }
        val ws = client.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                val mine = synchronized(waiters) { (gen == myGen).also { if (it) connected = true } }
                if (!mine) {
                    webSocket.cancel()
                    opened.completeExceptionally(IOException("connection closed"))
                    return
                }
                opened.complete(Unit)
            }

            override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                if (!current()) return
                val env = try { RespEnvelope.parseFrom(bytes.toByteArray()) } catch (e: Exception) { return }
                val cb = synchronized(waiters) { waiters.remove(env.id) }
                cb?.invoke(Result.success(env))
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                val err = if (response != null && !connected) {
                    IOException("bad response from the server (${response.code})", t)
                } else t
                // onDisconnect first, while the connect it ends is still in
                // flight: that is what makes OTCConnection schedule a retry.
                failAndClose(err, myGen)
                // Always, so a connect() whose socket was replaced never hangs.
                if (!opened.isCompleted) opened.completeExceptionally(err)
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(code, reason)
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                failAndClose(IOException("connection closed ($code)"), myGen)
            }
        })
        val replaced = synchronized(waiters) { (gen != myGen).also { if (!it) socket = ws } }
        if (replaced) ws.cancel()
        try {
            opened.await()
        } catch (e: CancellationException) {
            // A dropped attempt (invalidate(), or a race another address
            // won) must not open behind its caller's back.
            close()
            throw e
        }
    }

    /**
     * A deliberate close: no onDisconnect, so no reconnect is scheduled.
     * What was waiting on the socket fails now - its own callbacks are
     * ignored from here on, and used to leave those requests hanging.
     */
    fun close() {
        val (s, pending) = detach()
        s?.cancel()
        pending.forEach { it(Result.failure(IOException("connection closed"))) }
    }

    // Fires onDisconnect once per socket, a connect that failed before it
    // opened included (OTCConnection's backoff retry relies on that).
    private fun failAndClose(error: Throwable, myGen: Int) {
        val (s, pending) = synchronized(waiters) { if (gen != myGen) return; detach() }
        pending.forEach { it(Result.failure(error)) }
        s?.cancel()
        onDisconnect?.invoke()
    }

    private fun detach(): Pair<WebSocket?, List<(Result<RespEnvelope>) -> Unit>> = synchronized(waiters) {
        gen++
        connected = false
        val s = socket
        socket = null
        val p = waiters.values.toList()
        waiters.clear()
        s to p
    }

    suspend fun request(build: (ReqEnvelope.Builder) -> Unit): RespEnvelope {
        if (!connected) throw IOException("Not connected")
        val id = lock.withLock { nextId++ }
        val b = ReqEnvelope.newBuilder()
        build(b)
        // Re-assert the id after build(), as the Swift client does (issue
        // #77): a call site that replaces the whole envelope would send
        // id 0 and collide with every other in-flight request.
        b.id = id
        val bytes = b.build().toByteArray()
        return withTimeoutOrNull(requestTimeoutMs) {
            suspendCancellableCoroutine { cont ->
                val s = synchronized(waiters) {
                    waiters[id] = { r -> r.fold({ cont.resume(it) }, { cont.resumeWithException(it) }) }
                    socket
                }
                val ok = s?.send(bytes.toByteString()) ?: false
                if (!ok) {
                    val cb = synchronized(waiters) { waiters.remove(id) }
                    cb?.invoke(Result.failure(IOException("send failed")))
                }
                cont.invokeOnCancellation { synchronized(waiters) { waiters.remove(id) } }
            }
        } ?: throw IOException("The device did not answer in time")
    }
}
