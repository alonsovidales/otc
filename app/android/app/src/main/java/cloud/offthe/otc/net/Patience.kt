// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.RespEnvelope

/**
 * How long a screen's request of one kind (a page of photos, the feed, a
 * folder's listing, the tags...) waits for its answer, by [key]: the kind
 * and the route it goes over (OTCConnection.ask), since the bridge and the
 * home network answer at their own pace.
 *
 * Its usual time ([baseMs]), twice as long after each timeout in a row, up
 * to 5 minutes ([timeoutAfter]): a device that is slow but working (Pit's
 * pages took 65-70 s even at home) is never cut off for good, while a
 * request nothing will answer still fails and is asked again. An answer
 * brings it back down to the shortest time that would have held it - to
 * the usual time when it came within that - so one slow moment doesn't
 * leave every later request waiting minutes on a device that answers in
 * seconds again.
 */
class Patience {
    // Timeouts in a row, by key; absent: the usual time.
    private val steps = HashMap<String, Int>()

    /** The time a request of [key] gets now. */
    @Synchronized fun timeoutFor(key: String, baseMs: Long): Long = timeoutAfter(baseMs, steps[key] ?: 0)

    /** A request of [key] got no answer in the time it had. */
    @Synchronized fun timedOut(key: String) {
        steps[key] = minOf((steps[key] ?: 0) + 1, MAX_STEPS)
    }

    /** A request of [key] was answered [tookMs] after it reached the socket. */
    @Synchronized fun answered(key: String, baseMs: Long, tookMs: Long) {
        val now = steps[key] ?: return
        var need = 0
        while (need < now && timeoutAfter(baseMs, need) < tookMs) need++
        if (need == 0) steps.remove(key) else steps[key] = need
    }

    /**
     * Asks through [exchange] with the time [key] gets now (its argument),
     * and keeps count: a timeout of the answer itself gives the next one
     * more time ([WSClient.RequestTimeout.answerTimedOut]; one that ran out
     * while still connecting says nothing about how fast the device answers),
     * an answer brings it back down.
     */
    suspend fun run(key: String, baseMs: Long, exchange: suspend (timeoutMs: Long) -> WSClient.Answer): RespEnvelope {
        try {
            val a = exchange(timeoutFor(key, baseMs))
            answered(key, baseMs, a.waitedMs)
            return a.resp
        } catch (e: WSClient.RequestTimeout) {
            if (e.answerTimedOut) timedOut(key)
            throw e
        }
    }

    companion object {
        const val MAX_TIMEOUT_MS = 300_000L
        private const val MAX_STEPS = 8

        /**
         * The time a request whose kind timed out [timedOut] times in a row
         * gets: twice [baseMs] each time, up to 5 minutes (a longer base
         * keeps its own).
         */
        fun timeoutAfter(baseMs: Long, timedOut: Int): Long =
            minOf(baseMs shl timedOut.coerceIn(0, MAX_STEPS), maxOf(baseMs, MAX_TIMEOUT_MS))
    }
}
