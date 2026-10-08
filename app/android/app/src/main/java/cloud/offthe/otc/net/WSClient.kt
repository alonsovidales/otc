// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
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
import java.net.SocketTimeoutException
import java.util.concurrent.TimeUnit

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

    // Under sendLock: the bytes this socket's requests handed to OkHttp, in
    // the order it queued them - what tells when one has left the phone.
    private val sendLock = Any()
    private var sentBytes = 0L

    /**
     * No answer within the time the request was given: the socket may be
     * fine (a dead one fails what it has in flight within a ping or two,
     * 30 s each). Its answer, should it still come, is dropped: the request
     * is no longer waited on, and its id is never used again on this socket.
     * [answerTimedOut]: false when the time ran out before the request was
     * sent - still connecting (OTCConnection) - which says nothing about
     * how fast the device answers (Patience).
     */
    class RequestTimeout(
        message: String = "The device did not answer in time",
        val answerTimedOut: Boolean = true,
    ) : IOException(message)

    /** The WebSocket upgrade wasn't answered within [CONNECT_TIMEOUT_MS]. */
    class ConnectTimeout : SocketTimeoutException("The device didn't answer the connection in time")

    /** A request's answer, and how long it was waited for once the request had left the phone. */
    class Answer(val resp: RespEnvelope, val waitedMs: Long)

    companion object {
        // As the macOS client and otc-sync: long enough for ApplyUpdate and a
        // 4 MiB chunk on a slow link, but a request can no longer hang forever.
        // What a screen shows asks with a shorter one (OTCConnection.PAGE_TIMEOUT_MS).
        const val DEFAULT_TIMEOUT_MS = 30 * 60_000L

        /**
         * How long a connect may take, upgrade included. OkHttp bounds the
         * TCP and TLS handshakes (10 s each) but not the wait for the
         * upgrade's answer - the socket's readTimeout is 0, for a socket
         * that lives for hours - so a stalled one (a bridge node that took
         * the connection and never answered) left everything waiting on it
         * for good: grey tiles and "Still waiting for your device…" with
         * nothing failing, so nothing asked again.
         */
        const val CONNECT_TIMEOUT_MS = 20_000L

        // How often a request still queued behind others (a photo sync's
        // chunks) checks whether it has left; its answer ends the wait at once.
        private const val QUEUE_POLL_MS = 50L

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

    /** Opens the socket: within [timeoutMs], else [ConnectTimeout] - a failed connect like any other (onDisconnect fires). */
    suspend fun connect(url: String, timeoutMs: Long = CONNECT_TIMEOUT_MS) {
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
        val done = try {
            withTimeoutOrNull(timeoutMs) { opened.await() }
        } catch (e: CancellationException) {
            // A dropped attempt (invalidate(), or a race another address
            // won) must not open behind its caller's back.
            close()
            throw e
        }
        if (done == null) {
            // Failed like a connect that was refused: the socket goes, and
            // its owner hears of it (OTCConnection's backoff retry).
            val err = ConnectTimeout()
            failAndClose(err, myGen)
            throw err
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

    /** Sends a request and waits [timeoutMs] for its answer, else throws [RequestTimeout]. */
    suspend fun request(timeoutMs: Long = DEFAULT_TIMEOUT_MS, build: (ReqEnvelope.Builder) -> Unit): RespEnvelope =
        exchange(timeoutMs, build).resp

    /**
     * [request], with how long the answer took. The clock starts once the
     * request has left the phone (OkHttp has written it to the socket): on
     * the one socket it may queue behind a photo sync's chunks (up to three
     * of 4 MiB), and the device can't answer what it hasn't got - timed from
     * the send, a page could "take too long" and be asked again before it
     * ever left. A socket that stops moving fails what it holds (OkHttp's
     * write timeout, its pings), so the queue's wait is never endless.
     */
    suspend fun exchange(timeoutMs: Long = DEFAULT_TIMEOUT_MS, build: (ReqEnvelope.Builder) -> Unit): Answer {
        if (!connected) throw IOException("Not connected")
        val id = lock.withLock { nextId++ }
        val b = ReqEnvelope.newBuilder()
        build(b)
        // Re-assert the id after build(), as the Swift client does (issue
        // #77): a call site that replaces the whole envelope would send
        // id 0 and collide with every other in-flight request.
        b.id = id
        val bytes = b.build().toByteArray().toByteString()
        val answer = CompletableDeferred<RespEnvelope>()
        val s = synchronized(waiters) {
            waiters[id] = { r -> r.fold({ answer.complete(it) }, { answer.completeExceptionally(it) }) }
            socket
        }
        try {
            // Where this request ends in the socket's queue: it has left
            // once that many bytes have been written.
            val end = if (s == null) -1L else synchronized(sendLock) {
                if (s.send(bytes)) { sentBytes += bytes.size; sentBytes } else -1L
            }
            if (end < 0) {
                val cb = synchronized(waiters) { waiters.remove(id) }
                cb?.invoke(Result.failure(IOException("send failed")))
            }
            // Usually written within a few ms: looked at often at first,
            // then every QUEUE_POLL_MS behind a long queue.
            var poll = 2L
            while (!answer.isCompleted && synchronized(sendLock) { sentBytes - s!!.queueSize() } < end) {
                // Its answer (or the socket's failure) ends this at once.
                withTimeoutOrNull(poll) { answer.await() }
                poll = minOf(poll * 2, QUEUE_POLL_MS)
            }
            val started = System.nanoTime()
            // Timed out, the waiter goes (finally): a late answer finds no
            // one in onMessage and is dropped.
            val resp = withTimeoutOrNull(timeoutMs) { answer.await() } ?: throw RequestTimeout()
            return Answer(resp, (System.nanoTime() - started) / 1_000_000)
        } finally {
            synchronized(waiters) { waiters.remove(id) }
        }
    }
}
