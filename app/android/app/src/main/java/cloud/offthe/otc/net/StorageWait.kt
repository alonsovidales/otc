// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appShellNoStorage
import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch

/**
 * Issue #207: while the device can't reach its storage disks it answers
 * every request, GetPubKey first, with `storage_unavailable`
 * ([ErrorCodes.STORAGE_UNAVAILABLE]) and its own words. That is neither a
 * wrong password nor a device that is away: OTCConnection shows the words
 * where the "isn't available" card goes (`statusCode`), keeps the
 * credentials and everything else as they are, and asks again every
 * [HOLD_MS] - at once when the app comes back to the front or a network
 * comes up (a [Wake]) - instead of at the pace of every screen's retries.
 * As soon as an answer is anything else, [leave] ends it and signing in
 * goes on as always. iOS's StorageWait (OTCConnection.swift) is the same.
 *
 * [enter] starts the hold: for [HOLD_MS] a connect fails at once with the
 * device's words ([held]) instead of dialling. The re-ask loop (one at a
 * time) lifts it and runs `reask` after [HOLD_MS], or sooner at a wake,
 * for as long as the state lasts. [holdMs], [clock] and [pause] are the
 * tests'.
 */
class StorageWait(
    private val scope: CoroutineScope,
    private val holdMs: Long = HOLD_MS,
    private val clock: () -> Long = { System.nanoTime() / 1_000_000 },
    private val pause: suspend (Long) -> Unit = { sleepOrWake(it) },
) {
    companion object {
        /** How long a storage_unavailable answer holds: the device is asked again after it. */
        const val HOLD_MS = 30_000L

        /** What [r] says: the device's own words, else the app's (app.shell.no_storage). */
        fun text(r: RespEnvelope): UiText {
            val ack = if (r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) r.respAck.errorMsg else ""
            val words = r.errorMessage.ifEmpty { ack }
            return if (words.isNotEmpty()) UiText.Raw(words) else S.appShellNoStorage()
        }
    }

    private val lock = Any()
    // When the hold began; null once it was lifted (or there is none).
    private var since: Long? = null
    // The device's words while in the state; null outside it.
    private var words: UiText? = null
    private var loop: Job? = null

    /** In the state: the device last said it can't reach its storage. */
    val active: Boolean get() = synchronized(lock) { words != null }

    /** The device said so, with [text]: the hold starts again, and [reask] runs every [HOLD_MS] (or at a wake) while it lasts. */
    fun enter(text: UiText, reask: suspend () -> Unit) {
        synchronized(lock) {
            since = clock()
            words = text
            if (loop?.isActive == true) return
            loop = scope.launch { run(reask) }
        }
    }

    /** A connect asked for now: the device's words to fail with instead of dialling, null to dial. */
    fun held(): UiText? = synchronized(lock) {
        val at = since ?: return null
        words?.takeIf { clock() - at < holdMs }
    }

    /** New credentials or an explicit retry: the next connect dials; the state and its re-asks stay until an answer. */
    fun lift() {
        synchronized(lock) { since = null }
    }

    /** An answer that isn't storage_unavailable, or Log Out: out of the state. */
    fun leave() {
        val l = synchronized(lock) {
            since = null
            words = null
            loop.also { loop = null }
        }
        l?.cancel()
    }

    private suspend fun run(reask: suspend () -> Unit) {
        while (true) {
            val waitMs = synchronized(lock) {
                if (words == null) return
                // Until HOLD_MS after the last answer; a whole HOLD_MS
                // after a try nothing answered (no network: a wake brings
                // it at once when one comes).
                val at = since ?: return@synchronized holdMs
                (holdMs - (clock() - at)).coerceIn(0, holdMs)
            }
            pause(waitMs)
            synchronized(lock) {
                if (words == null) return
                since = null
            }
            try {
                reask()
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                // Answered again (enter has started the next hold), or nothing
                // answered: asked again on the next turn either way.
            }
        }
    }
}
