// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import android.os.SystemClock
import android.util.Log
import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.Auth
import cloud.offthe.otc.proto.GetLocalEndpoint
import cloud.offthe.otc.proto.GetPubKey
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import com.google.protobuf.ByteString
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import java.io.IOException
import java.net.ConnectException
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import java.security.MessageDigest
import javax.net.ssl.SSLException

// Port of OTCConnection.swift: the app-wide connection + auth state. Every
// screen goes through OTCConnection.request(...), which connects and
// authenticates on first use, re-authenticates after a drop, and retries
// a request once end-to-end if anything in that path fails.
//
// Issue #190: when the device gave its home-network endpoint (GetLocalEndpoint,
// asked after every sign-in through the configured endpoint), every connect
// first tries the device itself at home, over TLS pinned to its own
// certificate, and only then the configured endpoint (the bridge) as before.
object OTCConnection {
    private const val TAG = "OTC/Connection"
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    // The socket requests go through: a new one for every connection
    // attempt, since the home-network race opens several at once and a
    // route switch signs in on the next before this one is let go.
    @Volatile private var ws = WSClient()

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

    /** Issue #190: which way the socket in use reaches the device; BRIDGE is the configured endpoint, whatever it is. */
    enum class Route { HOME, BRIDGE }
    private val _route = MutableStateFlow<Route?>(null)
    /** Meaningful while [authenticated] (Settings shows it). */
    val route: StateFlow<Route?> = _route

    /** The device's own origin (https://<address>:<port>), its pin and the network it is reached over. */
    class Home(val origin: String, val pin: ByteArray, val via: HomeNetwork.Via)
    /** Set while the home route is in use: media URLs resolve against it (MediaStream). */
    @Volatile var home: Home? = null
        private set

    // Non-null exactly while a connect is running (cleared by its own
    // completion), so handleDisconnect's check means what it says.
    @Volatile private var connectJob: Deferred<Unit>? = null
    private val connectLock = Any()
    // The rest are guarded by connectLock. A route switch in progress
    // (reconsiderRoute), at most one.
    private var switchJob: Job? = null
    // Issue #190: home attempts after a sign-in through the bridge, and
    // how many have been made (see scheduleHomeRetry).
    private var homeRetryJob: Job? = null
    private var homeRetries = 0
    // Bumped by invalidate(): a sign-in from before neither stores what the
    // device said about its home network over the new settings nor swaps
    // its socket in.
    private var configGen = 0
    // Uploads, downloads in pieces and photo syncs running (transfer()).
    private var transfers = 0
    // A route check that a transfer or a connect put off: made once that
    // is over. NetworkWatch reports a change only once, so a dropped one
    // left the app on the bridge at home until the next resume.
    private var recheckPending = false
    // A home sign-in that hangs gives way to the bridge; Argon2id on a busy
    // device takes seconds, not this.
    private const val homeSignInMs = 15_000L
    private const val homeRetryFirstMs = 30_000L
    private const val homeRetryEveryMs = 120_000L
    private const val homeRetryMax = 5
    private var backoffMs = 1_000L
    private const val maxBackoffMs = 30_000L

    // The device turned the password down. Every poller (notifications, the
    // feed, MainView's retries) and request()'s own retry used to send the
    // same rejected password again at once, which tripped the device's
    // lockout (5 failures a minute per address - the household's public IP
    // through the bridge) for every client behind it. Until notBeforeMs, an
    // attempt with the same credentials fails with the same message without
    // dialling. Memory only, keyed on a hash of the credentials, so saving
    // new ones lifts it at once (as OTCConnection.swift does).
    private class AuthRejection(val credKey: String, val message: String, val notBeforeMs: Long)
    @Volatile private var authRejection: AuthRejection? = null
    // 5 s doubling: at most 4 failures in the first minute, under the lockout.
    @Volatile private var authBackoffMs = 5_000L
    private const val maxAuthBackoffMs = 300_000L

    class RequestError(message: String) : IOException(message)

    /**
     * How long what a screen shows waits for its answer before the request
     * fails and the screen asks again (its own retry, 1 s doubling to 10 s),
     * instead of hanging until the socket drops - through [ask], which gives
     * a kind that timed out more time (Patience). PAGE: a page of photos,
     * posts or covers, a folder's listing - a first page of Images from Pit
     * measured 6-70 s over the bridge, so 2 minutes, above the bridge's own
     * 90 s forward timeout (cForwardTimeout): through the bridge its "device
     * unreachable" always comes first, and this only ends a request nothing
     * will answer (at home, straight to the device, nothing else would).
     * LIST: the small lists (tags, date buckets), which still queue behind a
     * page on the device's upload. The clock starts once the request has
     * left the phone (WSClient.exchange), and connecting gets the same time
     * on its own. Everything else keeps the socket's 30 minutes (uploads,
     * downloads in pieces, ApplyUpdate).
     */
    const val PAGE_TIMEOUT_MS = 120_000L
    const val LIST_TIMEOUT_MS = 60_000L

    /**
     * GetPubKey and Auth while signing in: above the bridge's 90 s, so its
     * own "device unreachable" comes first, and far above an Argon2id
     * derivation on a busy device. They used to have the socket's 30
     * minutes, and every request waits on a sign-in in progress.
     */
    private const val SIGN_IN_TIMEOUT_MS = 120_000L

    private val patience = Patience()

    /** Why connecting failed when the phone has no network at all (the connection card's line). */
    const val OFFLINE_MESSAGE = "This phone is offline. Connect to Wi-Fi or mobile data."

    /** A request with the socket's own time (or [timeoutMs]): connecting, sending, waiting. */
    suspend fun request(
        timeoutMs: Long = WSClient.DEFAULT_TIMEOUT_MS,
        build: (ReqEnvelope.Builder) -> Unit,
    ): RespEnvelope = exchange(timeoutMs, build).resp

    /**
     * What a screen shows, of [kind] ("page", "feed", "listing", "tags"...):
     * [baseMs] for its answer (PAGE_TIMEOUT_MS, LIST_TIMEOUT_MS), twice as
     * long after each timeout in a row up to 5 minutes, and back down once
     * answers come in time again - per kind and route (Patience). Throws
     * WSClient.RequestTimeout when the time ran out, connecting included.
     */
    suspend fun ask(kind: String, baseMs: Long, build: (ReqEnvelope.Builder) -> Unit): RespEnvelope =
        patience.run("$kind/${_route.value ?: Route.BRIDGE}", baseMs) { exchange(it, build) }

    private suspend fun exchange(
        timeoutMs: Long,
        build: (ReqEnvelope.Builder) -> Unit,
    ): WSClient.Answer = withContext(Dispatchers.IO) {
        var used: WSClient? = null
        try {
            connectWithin(timeoutMs)
            ws.also { used = it }.exchange(timeoutMs, build)
        } catch (e: CancellationException) {
            // The caller went away (a closed screen, a cancelled search): the
            // connection is fine, and signing in again would cost the device
            // an Argon2id derivation for nothing.
            throw e
        } catch (e: WSClient.RequestTimeout) {
            // No answer in time on a socket that is still up (a dead one
            // fails what it has in flight within a ping or two), or still
            // connecting when the time ran out: the caller asks again.
            // Signing in again first would only add an Argon2id derivation
            // to a device that is already slow, and asking again here would
            // double the wait before the screen can say so.
            throw e
        } catch (e: Exception) {
            // Not when a route switch retired the socket under it: the one
            // in use is fine, and the retry goes there.
            if (used == null || used === ws) _authenticated.value = false
            connectWithin(timeoutMs)
            ws.exchange(timeoutMs, build)
        }
    }

    /**
     * [ensureConnected] within a request's own time: a connect stuck
     * somewhere (each attempt is bounded - WSClient.CONNECT_TIMEOUT_MS,
     * SIGN_IN_TIMEOUT_MS - but a request may come in behind one) no longer
     * leaves a screen waiting with nothing failed; the attempt itself goes
     * on for whoever asks next. A sign-in's own timeout is the connection
     * failing, not this request's answer.
     */
    private suspend fun connectWithin(timeoutMs: Long) {
        val done = try {
            if (timeoutMs >= WSClient.DEFAULT_TIMEOUT_MS) ensureConnected() else withTimeoutOrNull(timeoutMs) { ensureConnected() }
        } catch (e: WSClient.RequestTimeout) {
            throw IOException(e.message, e)
        }
        if (done == null) throw WSClient.RequestTimeout("Still connecting to the device", answerTimedOut = false)
    }

    /**
     * Issue #190: an upload, a download in pieces or a photo sync. A
     * chunked upload belongs to the socket it began on (the device drops it
     * with that connection), so the route isn't switched while one runs:
     * the next reconnect picks it instead.
     */
    inline fun <T> transfer(block: () -> T): T {
        transferStarted()
        try {
            return block()
        } finally {
            transferEnded()
        }
    }

    /** [transfer] in two halves, for a run whose start and end are apart (the photo sync); always paired. */
    @PublishedApi internal fun transferStarted() { synchronized(connectLock) { transfers++ } }
    @PublishedApi internal fun transferEnded() {
        val recheck = synchronized(connectLock) {
            transfers--
            (transfers == 0 && recheckPending).also { if (it) recheckPending = false }
        }
        if (recheck) scope.launch { reconsiderRoute() }
    }

    /** Connects and authenticates if not already; concurrent callers share the one attempt. */
    suspend fun ensureConnected() {
        while (true) {
            if (_authenticated.value) return
            val job = synchronized(connectLock) {
                connectJob ?: scope.async { connectAndAuth() }.also { d ->
                    connectJob = d
                    d.invokeOnCompletion {
                        val recheck = synchronized(connectLock) {
                            if (connectJob !== d) return@synchronized false
                            connectJob = null
                            recheckPending.also { recheckPending = false }
                        }
                        if (recheck) scope.launch { reconsiderRoute() }
                    }
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
        synchronized(connectLock) {
            connectJob?.cancel(); connectJob = null
            switchJob?.cancel(); switchJob = null
            homeRetryJob?.cancel(); homeRetryJob = null
            recheckPending = false
            configGen++
        }
        ws.close()
        home = null
        _authenticated.value = false
        backoffMs = 1_000L
        // An explicit retry or new credentials: try at once.
        authRejection = null
        authBackoffMs = 5_000L
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
        val credKey = credentialsKey(url, deviceId, password)
        authRejection?.let { r ->
            if (r.credKey == credKey && SystemClock.elapsedRealtime() < r.notBeforeMs) throw RequestError(r.message)
        }
        val gen = synchronized(connectLock) { configGen }

        // A socket still open (a request on it timed out): signed in again
        // where it is, as before.
        val open = ws
        if (open.connected) {
            signInOrFail(open, password, deviceId, credKey)
            currentCoroutineContext().ensureActive()
            signedIn()
            if (_route.value != Route.HOME) refreshHomeEndpoint(open, url, gen)
            return
        }

        secrets.homeEndpoint(url)?.let { ep -> if (signInAtHome(ep, password, deviceId, credKey)) return }

        // Through the configured endpoint, as always. The socket is the
        // current one from the start, so a connect that fails schedules the
        // backoff retry (handleDisconnect).
        val c = WSClient()
        install(c, Route.BRIDGE, null)
        try {
            c.connect(url)
        } catch (e: Exception) {
            // Dropped by invalidate(): no "Canceled" on the card.
            currentCoroutineContext().ensureActive()
            _lastError.value = describe(e)
            _connectionFailed.value = true
            throw e
        }
        signInOrFail(c, password, deviceId, credKey)
        currentCoroutineContext().ensureActive()
        signedIn()
        refreshHomeEndpoint(c, url, gen)
        scheduleHomeRetry(first = true)
    }

    /**
     * Issue #190: on the bridge with a home endpoint stored and a Wi-Fi or
     * wired network up, the home network is tried again: 30 s after a
     * sign-in through the bridge, then every 2 minutes, at most 5 times. A
     * home attempt that found nothing at a cold start (the Wi-Fi waking,
     * the device busy) otherwise kept the phone on the bridge at home for
     * as long as the app stayed open; the Mac retries the same way.
     */
    private fun scheduleHomeRetry(first: Boolean) {
        synchronized(connectLock) {
            homeRetryJob?.cancel()
            if (first) homeRetries = 0
            if (homeRetries >= homeRetryMax) return
            val gen = configGen
            homeRetryJob = scope.launch {
                delay(if (first) homeRetryFirstMs else homeRetryEveryMs)
                synchronized(connectLock) {
                    if (gen != configGen) return@launch
                    homeRetries++
                }
                if (_route.value != Route.BRIDGE || !_authenticated.value) return@launch
                val secrets = SecretsStore.loadOrCreate()
                if (secrets.homeEndpoint(secrets.endpointURLString) == null || NetworkWatch.homeRoutes().isEmpty()) return@launch
                reconsiderRoute()?.join()
                if (_route.value == Route.BRIDGE) scheduleHomeRetry(first = false)
            }
        }
    }

    /**
     * Issue #190: every stored address at once, the pin checked before a
     * byte is sent, then the usual sign-in. true once signed in there;
     * false to go through the configured endpoint instead. Throws only
     * when the device itself turned the password down - the bridge would
     * say the same, and count one more failure.
     */
    private suspend fun signInAtHome(ep: HomeEndpoint, password: String, deviceId: String, credKey: String): Boolean {
        val vias = NetworkWatch.homeRoutes()
        if (vias.isEmpty()) return false
        val opened = try {
            HomeNetwork.open(ep, vias)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            null
        }
        if (opened == null) {
            Log.i(TAG, "the home network didn't answer: through the bridge")
            return false
        }
        val c = opened.socket
        var kept = false
        try {
            var timedOut = true
            val refusal = try {
                withTimeoutOrNull(homeSignInMs) { signIn(c, password, deviceId).also { timedOut = false } }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                Log.i(TAG, "signing in over the home network failed ($e): through the bridge")
                return false
            }
            if (timedOut) {
                Log.i(TAG, "signing in over the home network timed out: through the bridge")
                return false
            }
            if (refusal != null) {
                if (!refusal.auth || refusal.ack == null) return false
                currentCoroutineContext().ensureActive()
                record(refusal, credKey)
                _connectionFailed.value = true
                throw RequestError(refusal.message)
            }
            currentCoroutineContext().ensureActive()
            // Dropped right after signing in: the bridge, rather than a dead socket.
            if (!c.connected) return false
            install(c, Route.HOME, Home(HomeNetwork.origin(opened.address, ep.port), ep.certSha256, opened.via))
            kept = true
            signedIn()
            Log.i(TAG, "signed in over the home network")
            return true
        } finally {
            if (!kept) c.close()
        }
    }

    // What answered instead of signing us in: GetPubKey's or Auth's reply.
    private class Refusal(val message: String, val ack: Ack?, val auth: Boolean)

    /** GetPubKey, then Auth with the password sealed to that key. null once signed in; throws when the socket fails. */
    private suspend fun signIn(c: WSClient, password: String, deviceId: String): Refusal? {
        val pubKeyResp = c.request(SIGN_IN_TIMEOUT_MS) { it.setReqGetPubKey(GetPubKey.getDefaultInstance()) }
        if (pubKeyResp.payloadCase != RespEnvelope.PayloadCase.RESP_PUB_KEY) {
            val ack = if (pubKeyResp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) pubKeyResp.respAck else null
            return Refusal(ack?.errorMsg?.ifEmpty { null } ?: "Unable to fetch the connection's public key", ack, auth = false)
        }
        val encrypted = PwCrypto.encryptPassword(password, pubKeyResp.respPubKey.publicKey.toByteArray())
        val auth = Auth.newBuilder()
            .setUuid(deviceId)
            .setKey(ByteString.copyFrom(encrypted))
            .setCreate(false)
            .build()
        val resp = c.request(SIGN_IN_TIMEOUT_MS) { it.setReqAuth(auth) }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.ok) return null
        val ack = if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) resp.respAck else null
        return Refusal(ack?.errorMsg ?: "Authentication failed", ack, auth = true)
    }

    /** [signIn] on the configured endpoint's socket: any failure goes on the card and closes it. */
    private suspend fun signInOrFail(c: WSClient, password: String, deviceId: String, credKey: String) {
        val refusal = try {
            signIn(c, password, deviceId)
        } catch (e: Exception) {
            // Dropped by invalidate(): leave the card alone.
            currentCoroutineContext().ensureActive()
            // A handshake that failed holds a bridge pool slot for nothing: close it.
            c.close()
            if (_lastError.value == null) _lastError.value = describe(e)
            _connectionFailed.value = true
            throw e
        }
        if (refusal != null) {
            currentCoroutineContext().ensureActive()
            c.close()
            record(refusal, credKey)
            _connectionFailed.value = true
            throw RequestError(refusal.message)
        }
    }

    private fun record(r: Refusal, credKey: String) {
        r.ack?.let { _statusCode.value = it.code.ifEmpty { null } }
        _lastError.value = r.message
        val ack = r.ack ?: return
        if (!r.auth) return
        // The device's own verdict on the password; the bridge's
        // (device_unreachable, account_disabled) keeps today's retries.
        val rejectedForMs = when (ack.code) {
            "" -> authBackoffMs
            "too_many_attempts" -> maxOf((ack.retryAfterSeconds + 1) * 1_000L, authBackoffMs)
            else -> return
        }
        authRejection = AuthRejection(credKey, r.message, SystemClock.elapsedRealtime() + rejectedForMs)
        authBackoffMs = minOf(authBackoffMs * 2, maxAuthBackoffMs)
    }

    private fun signedIn() {
        _lastError.value = null
        _statusCode.value = null
        _connectionFailed.value = false
        backoffMs = 1_000L
        authRejection = null
        authBackoffMs = 5_000L
        _authenticated.value = true
        // On every sign-in, not only when the main screen first shows: a
        // device set up (or reinstalled) while the app was running would
        // otherwise never learn this phone's token.
        cloud.offthe.otc.push.FCMPush.registerKnown(cloud.offthe.otc.OTCApp.instance)
        // Back after a drop: what failed meanwhile is asked for again now.
        Wake.fire()
    }

    /** Makes [c] the socket requests go through; only its own drop reconnects. */
    private fun install(c: WSClient, route: Route, h: Home?) {
        c.onDisconnect = { if (ws === c) handleDisconnect() }
        ws = c
        home = h
        _route.value = route
    }

    /**
     * Issue #190: after a sign-in through the configured endpoint, where
     * does the device answer at home? Stored with the endpoint; a device
     * from before #190 ("unknown_payload") or with no way in at home
     * ("local_unavailable") clears it, and no answer keeps it.
     */
    private fun refreshHomeEndpoint(c: WSClient, url: String, gen: Int) {
        scope.launch {
            val resp = try {
                c.request { it.setReqGetLocalEndpoint(GetLocalEndpoint.getDefaultInstance()) }
            } catch (e: Exception) {
                return@launch
            }
            val answer = HomeNetwork.answer(resp)
            val learnt = synchronized(connectLock) {
                if (gen != configGen) return@launch
                val secrets = SecretsStore.loadOrCreate()
                when (answer) {
                    is HomeNetwork.Answer.Store -> {
                        val before = secrets.homeEndpoint(url)
                        secrets.saveHomeEndpoint(url, answer.endpoint)
                        before?.encode() != answer.endpoint.encode()
                    }
                    HomeNetwork.Answer.Forget -> { secrets.clearHomeEndpoint(); false }
                    HomeNetwork.Answer.Keep -> false
                }
            }
            // A new or changed endpoint is tried now, as on the Mac and in
            // otc-sync: otherwise a phone that stays on the home Wi-Fi
            // with the app open moved home only at its next foreground or
            // network change. reconsiderRoute waits for a connect or a
            // transfer still running.
            if (learnt) reconsiderRoute()
        }
    }

    /**
     * Issue #190: the phone changed networks, or the app came back to the
     * foreground. At home, over to the device itself; away, off a home
     * socket that no longer reaches it. Make before break: the socket in
     * use stays until the next one has signed in. Not while a transfer
     * runs, nor while connecting (which may have tried the home network
     * before this change): the check is made once the last transfer ends
     * or the connect is over. The check running, if any, for the caller
     * to wait on.
     */
    fun reconsiderRoute(): Job? {
        synchronized(connectLock) {
            switchJob?.takeIf { it.isActive }?.let { return it }
            if (transfers > 0 || connectJob != null) {
                recheckPending = true
                return null
            }
            recheckPending = false
            // Not signed in and not connecting: the next connect tries the home network first.
            if (!_authenticated.value) return null
            return scope.launch {
                try {
                    switchRoute()
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    Log.w(TAG, "route switch failed: $e")
                }
            }.also { switchJob = it }
        }
    }

    private suspend fun switchRoute() {
        val secrets = SecretsStore.loadOrCreate()
        val url = secrets.endpointURLString
        val password = secrets.password.value
        val deviceId = secrets.deviceId.value
        val gen = synchronized(connectLock) { configGen }
        val old = ws
        val h = home
        val from = _route.value ?: return
        // Nothing stored: the configured endpoint is the only way.
        val ep = secrets.homeEndpoint(url) ?: return
        val vias = NetworkWatch.homeRoutes()
        // Home on a live socket over a network the phone is still on: stay,
        // as macOS does on a wake. A probe that missed its budget on a busy
        // device or LAN would otherwise move it onto the bridge at home.
        // Leaving that network kills the socket, and its drop reconnects.
        if (from == Route.HOME && old.connected && h != null && vias.any { it.key == h.via.key }) return
        val opened = if (vias.isEmpty()) null else HomeNetwork.open(ep, vias)
        if (from == Route.HOME) {
            if (opened != null) {
                // Still at home: the socket in use stays.
                opened.socket.close()
                return
            }
            val c = WSClient()
            var kept = false
            try {
                c.connect(url)
                if (signIn(c, password, deviceId) != null) return
                kept = swap(old, c, gen, Route.BRIDGE, null)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                return
            } finally {
                if (!kept) c.close()
            }
            if (!kept) return
            Log.i(TAG, "left the home network: through the bridge")
            refreshHomeEndpoint(c, url, gen)
        } else {
            if (opened == null) return
            val c = opened.socket
            var kept = false
            try {
                var timedOut = true
                val refusal = withTimeoutOrNull(homeSignInMs) { signIn(c, password, deviceId).also { timedOut = false } }
                if (timedOut || refusal != null) return
                kept = swap(old, c, gen, Route.HOME, Home(HomeNetwork.origin(opened.address, ep.port), ep.certSha256, opened.via))
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                return
            } finally {
                if (!kept) c.close()
            }
            if (kept) Log.i(TAG, "at home: over the home network")
        }
    }

    // Only if nothing changed meanwhile: new settings, a reconnect, a
    // transfer that started, a socket that died while signing in.
    private fun swap(old: WSClient, c: WSClient, gen: Int, route: Route, h: Home?): Boolean {
        synchronized(connectLock) {
            if (gen != configGen || ws !== old || !_authenticated.value || transfers > 0 || connectJob != null || !c.connected) return false
            install(c, route, h)
        }
        retire(old)
        return true
    }

    // The socket a switch replaced: what it still has in flight is answered
    // there (retried on the next socket, a request could run twice), then
    // it goes. Nothing new is sent on it.
    private fun retire(old: WSClient) {
        scope.launch {
            val until = SystemClock.elapsedRealtime() + 10 * 60_000L
            while (old.pending > 0 && old.connected && SystemClock.elapsedRealtime() < until) delay(500)
            old.close()
        }
    }

    /** What the auth gate compares: a hash, so no second copy of the password is kept. */
    private fun credentialsKey(url: String, deviceId: String, password: String): String =
        MessageDigest.getInstance("SHA-256").digest("$url\u0000$deviceId\u0000$password".toByteArray())
            .joinToString("") { "%02x".format(it) }

    /** Plain words for the errors the network stack hands back. */
    private fun describe(e: Throwable): String = when {
        // No network at all: not the address's fault, whatever failed.
        !NetworkWatch.online() -> OFFLINE_MESSAGE
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
