// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.proto.Auth
import cloud.offthe.otc.proto.GetPubKey
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import com.google.protobuf.ByteString
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.IOException
import java.net.ConnectException
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import javax.net.ssl.SSLException

// Port of OTCConnection.swift: the app-wide connection + auth state. Every
// screen goes through OTCConnection.request(...), which connects and
// authenticates on first use, re-authenticates after a drop, and retries
// a request once end-to-end if anything in that path fails.
object OTCConnection {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val ws = WSClient().also { c -> c.onDisconnect = { handleDisconnect() } }

    private val _authenticated = MutableStateFlow(false)
    private val _lastError = MutableStateFlow<String?>(null)
    private val _statusCode = MutableStateFlow<String?>(null)
    private val _connectionFailed = MutableStateFlow(false)
    val authenticated: StateFlow<Boolean> = _authenticated
    val lastError: StateFlow<String?> = _lastError
    /** Issue #56: "device_unreachable" / "account_disabled" (Ack.code), null when the device answers. */
    val statusCode: StateFlow<String?> = _statusCode
    /** Set whenever connecting or signing in fails; MainView shows the connection form while set. */
    val connectionFailed: StateFlow<Boolean> = _connectionFailed

    // Non-null exactly while a connect is running (cleared by its own
    // completion), so handleDisconnect's check means what it says.
    @Volatile private var connectJob: Deferred<Unit>? = null
    private val connectLock = Any()
    private var backoffMs = 1_000L
    private const val maxBackoffMs = 30_000L

    class RequestError(message: String) : IOException(message)

    suspend fun request(build: (ReqEnvelope.Builder) -> Unit): RespEnvelope = withContext(Dispatchers.IO) {
        try {
            ensureConnected()
            ws.request(build)
        } catch (e: CancellationException) {
            // The caller went away (a closed screen, a cancelled search): the
            // connection is fine, and signing in again would cost the device
            // an Argon2id derivation for nothing.
            throw e
        } catch (e: Exception) {
            _authenticated.value = false
            ensureConnected()
            ws.request(build)
        }
    }

    /** Connects and authenticates if not already; concurrent callers share the one attempt. */
    suspend fun ensureConnected() {
        while (true) {
            if (_authenticated.value) return
            val job = synchronized(connectLock) {
                connectJob ?: scope.async { connectAndAuth() }.also { d ->
                    connectJob = d
                    d.invokeOnCompletion { synchronized(connectLock) { if (connectJob === d) connectJob = null } }
                }
            }
            try {
                job.await()
                return
            } catch (e: CancellationException) {
                // Our own cancellation ends here; an attempt invalidate()
                // dropped (new settings) is followed by one for the new ones.
                currentCoroutineContext().ensureActive()
                if (!job.isCancelled) throw e
            }
        }
    }

    /** After the endpoint/password changed: the next request re-authenticates. */
    fun invalidate() {
        // An attempt still running dials the old address, and close() is
        // about to cancel its socket under it.
        synchronized(connectLock) { connectJob?.cancel(); connectJob = null }
        ws.close()
        _authenticated.value = false
        backoffMs = 1_000L
        MediaStream.reset()
    }

    /** Log Out: drop the connection and forget what went wrong with the last one. */
    fun reset() {
        invalidate()
        _lastError.value = null
        _statusCode.value = null
        _connectionFailed.value = false
    }

    private suspend fun connectAndAuth() {
        val secrets = SecretsStore.loadOrCreate()
        // Read once: Log Out wipes the store while a request it interrupted
        // may be retrying, and a sign-in with the old address but the wiped
        // password would count as a failed attempt on the device.
        val url = secrets.endpointURLString
        val password = secrets.password.value
        val deviceId = secrets.deviceId.value
        if (url.isEmpty() || !(url.startsWith("ws://") || url.startsWith("wss://"))) {
            _lastError.value = "The address \"${secrets.endpoint.value}\" isn't valid."
            _connectionFailed.value = true
            throw RequestError(_lastError.value!!)
        }
        try {
            ws.connect(url)
        } catch (e: Exception) {
            // Dropped by invalidate(): no "Canceled" on the card.
            currentCoroutineContext().ensureActive()
            _lastError.value = describe(e)
            _connectionFailed.value = true
            throw e
        }
        try {
            val pubKeyResp = ws.request { it.setReqGetPubKey(GetPubKey.getDefaultInstance()) }
            if (pubKeyResp.payloadCase != RespEnvelope.PayloadCase.RESP_PUB_KEY) {
                var msg = "Unable to fetch the connection's public key"
                if (pubKeyResp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) {
                    val ack = pubKeyResp.respAck
                    if (ack.errorMsg.isNotEmpty()) msg = ack.errorMsg
                    _statusCode.value = ack.code.ifEmpty { null }
                }
                _lastError.value = msg
                throw RequestError(msg)
            }
            val encrypted = PwCrypto.encryptPassword(password, pubKeyResp.respPubKey.publicKey.toByteArray())
            val auth = Auth.newBuilder()
                .setUuid(deviceId)
                .setKey(ByteString.copyFrom(encrypted))
                .setCreate(false)
                .build()
            val resp = ws.request { it.setReqAuth(auth) }
            val ok = resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.ok
            if (!ok) {
                val msg = if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) {
                    _statusCode.value = resp.respAck.code.ifEmpty { null }
                    resp.respAck.errorMsg
                } else "Authentication failed"
                _lastError.value = msg
                throw RequestError(msg)
            }
        } catch (e: Exception) {
            // Dropped by invalidate(): the socket ws holds now may already be
            // the next attempt's, so leave it, and the card, alone.
            currentCoroutineContext().ensureActive()
            // A handshake that failed holds a bridge pool slot for nothing: close it.
            ws.close()
            if (_lastError.value == null) _lastError.value = describe(e)
            _connectionFailed.value = true
            throw e
        }
        currentCoroutineContext().ensureActive()
        _lastError.value = null
        _statusCode.value = null
        _connectionFailed.value = false
        backoffMs = 1_000L
        _authenticated.value = true
        // On every sign-in, not only when the main screen first shows: a
        // device set up (or reinstalled) while the app was running would
        // otherwise never learn this phone's token.
        cloud.offthe.otc.push.FCMPush.registerKnown(cloud.offthe.otc.OTCApp.instance)
    }

    /** Plain words for the errors the network stack hands back. */
    private fun describe(e: Throwable): String = when {
        e is UnknownHostException -> "That address can't be found. Check the device name or address."
        e is ConnectException || e is SocketTimeoutException -> "The device isn't answering. It may be off, or the address may be wrong."
        e is SSLException -> "Couldn't make a secure connection to that address."
        e.message?.contains("bad response", ignoreCase = true) == true ->
            "The address answered, but not as an Off The Cloud device. Check that it ends in /ws and points at your device or its bridge name."
        else -> e.message ?: e.javaClass.simpleName
    }

    private fun handleDisconnect() {
        if (!_authenticated.value && connectJob == null) return
        _authenticated.value = false
        val delayMs = backoffMs
        backoffMs = minOf(backoffMs * 2, maxBackoffMs)
        scope.launch {
            delay(delayMs)
            try { ensureConnected() } catch (_: Exception) {}
        }
    }
}
