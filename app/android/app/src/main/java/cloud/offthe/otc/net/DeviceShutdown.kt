// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appSettingsUpdateShuttingDownNote
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.TextIOException

/**
 * What a ShutDownDevice answer comes to (Settings > Shut down device,
 * issue #202; iOS's ShutDownAnswer).
 */
sealed interface ShutDownAnswer {
    /** The device said yes (the generic Ack): it is turning itself off. */
    data object ShuttingDown : ShutDownAnswer

    /** A device before Settings > Shut down device (unknown_payload): DeviceNeedsUpdate. */
    data object NeedsUpdate : ShutDownAnswer

    /**
     * Turned down (a user's instance, a container, the demo, an update or a
     * Wi-Fi change under way): the device's words, null when it gave none.
     */
    data class Refused(val words: String?) : ShutDownAnswer

    companion object {
        fun of(r: RespEnvelope): ShutDownAnswer {
            val ack = if (r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) r.respAck else null
            return when {
                !r.error && ack?.ok == true -> ShuttingDown
                r.error && ErrorCodes.isUnknownPayload(r.errorCode, r.errorMessage) -> NeedsUpdate
                r.error && r.errorMessage.isNotEmpty() -> Refused(r.errorMessage)
                !ack?.errorMsg.isNullOrEmpty() -> Refused(ack?.errorMsg)
                else -> Refused(null)
            }
        }
    }
}

/**
 * Issue #202: once the device has said yes to Shut down device it turns
 * itself off and stays off until its power is unplugged and plugged in
 * again - it isn't coming back by itself. So for a moment ([QUIET_MS]) the
 * app stops treating it as merely away: no reconnects to a device that is
 * powering off (the disconnect's own retry and every poller's request would
 * each dial it, at home or through the bridge), and so no "isn't available"
 * or "can't connect" card over the Settings that say it is shutting down. A
 * request in that moment fails at once, saying so ([Quiet]). Afterwards
 * everything is as always: the next request dials, and the usual card shows
 * until the device is back. iOS's DeviceShutdown.swift is the same.
 */
object DeviceShutdown {
    /** A Raspberry Pi is off well within it, and one plugged in again at once takes about as long to start. */
    const val QUIET_MS = 90_000L

    /** Thrown by a connect asked for in that moment, instead of dialling: the section's words. */
    class Quiet : TextIOException(S.appSettingsUpdateShuttingDownNote())

    /** The clock, in milliseconds that only go forward (tests set their own). */
    @Volatile var clock: () -> Long = { System.nanoTime() / 1_000_000 }

    // When the device said yes; null when it hasn't, or that was forgotten.
    @Volatile private var since: Long? = null

    /** The device said yes to ShutDownDevice. */
    fun began() { since = clock() }

    /** Another device or Log Out (OTCConnection.invalidate): whatever comes next is a device of its own. */
    fun forget() { since = null }

    /** Milliseconds of that moment left: 0 once it is over, or with none. */
    fun quietLeftMs(): Long {
        val at = since ?: return 0
        return (QUIET_MS - (clock() - at)).coerceAtLeast(0)
    }

    /** The app leaves the device alone now. */
    fun quiet(): Boolean = quietLeftMs() > 0
}
