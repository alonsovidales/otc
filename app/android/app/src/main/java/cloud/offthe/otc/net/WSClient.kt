// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
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
class WSClient {
    private val client = OkHttpClient.Builder()
        .pingInterval(30, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.MILLISECONDS) // long-lived socket
        .build()

    private var socket: WebSocket? = null
    @Volatile var connected = false
        private set
    private var nextId = 1
    private val waiters = HashMap<Int, (Result<RespEnvelope>) -> Unit>()
    private val lock = Mutex()

    /** Fired once, when the socket breaks. The owner reconnects; this class never retries. */
    var onDisconnect: (() -> Unit)? = null

    suspend fun connect(url: String) {
        if (connected) return
        val opened = CompletableDeferred<Unit>()
        val req = Request.Builder().url(url).build()
        socket = client.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                connected = true
                opened.complete(Unit)
            }

            override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                val env = try { RespEnvelope.parseFrom(bytes.toByteArray()) } catch (e: Exception) { return }
                val cb = synchronized(waiters) { waiters.remove(env.id) }
                cb?.invoke(Result.success(env))
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                val err = if (response != null && !connected) {
                    IOException("bad response from the server (${response.code})", t)
                } else t
                if (!opened.isCompleted) opened.completeExceptionally(err)
                failAndClose(err)
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(code, reason)
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                failAndClose(IOException("connection closed ($code)"))
            }
        })
        opened.await()
    }

    fun close() {
        connected = false
        socket?.cancel()
        socket = null
    }

    private fun failAndClose(error: Throwable) {
        if (!connected && socket == null) return
        connected = false
        val pending = synchronized(waiters) { val p = waiters.values.toList(); waiters.clear(); p }
        pending.forEach { it(Result.failure(error)) }
        socket?.cancel()
        socket = null
        onDisconnect?.invoke()
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
        return suspendCancellableCoroutine { cont ->
            synchronized(waiters) {
                waiters[id] = { r -> r.fold({ cont.resume(it) }, { cont.resumeWithException(it) }) }
            }
            val ok = socket?.send(bytes.toByteString()) ?: false
            if (!ok) {
                val cb = synchronized(waiters) { waiters.remove(id) }
                cb?.invoke(Result.failure(IOException("send failed")))
            }
            cont.invokeOnCancellation { synchronized(waiters) { waiters.remove(id) } }
        }
    }
}
