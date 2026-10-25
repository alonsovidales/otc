// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appShellPercent
import cloud.offthe.otc.i18n.appSettingsWifiNameNeeded
import cloud.offthe.otc.i18n.appSettingsWifiNameTooLong
import cloud.offthe.otc.i18n.appSettingsWifiPasswordInvalid
import cloud.offthe.otc.i18n.appSettingsWifiPasswordNeeded
import cloud.offthe.otc.net.ErrorCodes
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.WifiJoin
import cloud.offthe.otc.proto.WifiNetwork
import cloud.offthe.otc.proto.WifiStatus
import kotlin.time.Duration
import kotlin.time.Duration.Companion.seconds

/**
 * Settings › Wi-Fi's rules, apart from the screen so that the JVM tests
 * reach them: what may be sent, and what each answer of the device means
 * for the section. The same as iOS's `enum WifiFlow` (WifiSection.swift),
 * case for case (WifiFlowTest.kt and WifiFlowTests.swift test one table).
 *
 * Names and passwords follow the device's own rules (network/join.go and
 * the root runner): a name is 1-32 bytes of UTF-8; a password is empty
 * (an open network), 8-63 bytes of UTF-8 without control characters, or
 * exactly 64 hexadecimal digits. Bytes, not characters: "ñ" counts two.
 * A name with a control character is left to the device (bad_name, in its
 * own words) - a text field can hardly hold one.
 */
object WifiFlow {
    /** A listed network with a password, a listed open one, or "Other Network…" (typed by hand, hidden). */
    enum class Kind { SECURED, OPEN, OTHER }

    /** Why Connect can't send yet. */
    enum class Problem { NAME_NEEDED, NAME_TOO_LONG, PASSWORD_NEEDED, PASSWORD_INVALID }

    /** What GetWifiStatus's answer does to the section when it is loaded. */
    enum class Load { SHOW, HIDE, HIDE_UNTIL_SIGN_IN }

    /** How a change ended. */
    sealed interface Outcome {
        data class Connected(val message: String) : Outcome
        data class Failed(val code: String, val message: String) : Outcome
        /** The device answered without saying (another change, none, a state this build doesn't know). */
        data object Unknown : Outcome
    }

    /** What JoinWifi's own answer does. */
    sealed interface Sent {
        data class Wait(val id: String) : Sent
        data class Done(val outcome: Outcome) : Sent
        /** Refused before anything changed: the device's words, or null (shown as `failed`). */
        data class Refused(val message: String?) : Sent
        data object Hide : Sent
        data object HideUntilSignIn : Sent
    }

    /** What one look while waiting does. */
    sealed interface Poll {
        data object KeepWaiting : Poll
        data class Done(val outcome: Outcome) : Poll
    }

    private const val MAX_NAME_BYTES = 32
    private const val MIN_PASSWORD_BYTES = 8
    private const val MAX_PASSWORD_BYTES = 63
    private const val HEX_KEY_LENGTH = 64

    /** A look while waiting, every pollEvery until slowAfter, then every slowPollEvery; each look waits lookTimeout. */
    val pollEvery: Duration = 3.seconds
    val lookTimeout: Duration = 10.seconds
    val slowAfter: Duration = 240.seconds
    val slowPollEvery: Duration = 10.seconds

    private fun bytes(s: String): Int = s.toByteArray(Charsets.UTF_8).size

    fun checkName(ssid: String): Problem? = when {
        ssid.isEmpty() -> Problem.NAME_NEEDED
        bytes(ssid) > MAX_NAME_BYTES -> Problem.NAME_TOO_LONG
        else -> null
    }

    fun checkPassword(password: String, kind: Kind): Problem? {
        // An open row sends nothing, whatever was typed before.
        if (kind == Kind.OPEN) return null
        if (password.isEmpty()) return if (kind == Kind.SECURED) Problem.PASSWORD_NEEDED else null
        if (password.length == HEX_KEY_LENGTH && password.all { it in '0'..'9' || it in 'a'..'f' || it in 'A'..'F' }) return null
        val n = bytes(password)
        if (n < MIN_PASSWORD_BYTES || n > MAX_PASSWORD_BYTES) return Problem.PASSWORD_INVALID
        if (password.any { it < ' ' || it == '\u007f' }) return Problem.PASSWORD_INVALID
        return null
    }

    /** The name first, then the password. */
    fun check(ssid: String, password: String, kind: Kind): Problem? = checkName(ssid) ?: checkPassword(password, kind)

    /** What gets sealed and sent: nothing for an open row, else the password as typed. */
    fun sentPassword(password: String, kind: Kind): String = if (kind == Kind.OPEN) "" else password

    /** JoinWifi.hidden: a name typed by hand is joined even when the scan doesn't show it. */
    fun hidden(kind: Kind): Boolean = kind == Kind.OTHER

    /** On a cable and no Wi-Fi network: the confirmation says it stays reachable (confirm_cable_message). */
    fun cableOnly(wired: Boolean, ssid: String): Boolean = wired && ssid.isEmpty()

    fun text(problem: Problem): UiText = when (problem) {
        Problem.NAME_NEEDED -> S.appSettingsWifiNameNeeded()
        Problem.NAME_TOO_LONG -> S.appSettingsWifiNameTooLong()
        Problem.PASSWORD_NEEDED -> S.appSettingsWifiPasswordNeeded()
        Problem.PASSWORD_INVALID -> S.appSettingsWifiPasswordInvalid()
    }

    /**
     * GetWifiStatus on load: shown only for a device that manages its Wi-Fi.
     * A device before the section (unknown_payload) isn't asked again until
     * the next sign-in; any other refusal or answer (the bridge's, a child
     * instance's) hides it.
     */
    fun onLoad(answer: RespEnvelope): Load = when {
        answer.error -> if (ErrorCodes.isUnknownPayload(answer.errorCode, answer.errorMessage)) Load.HIDE_UNTIL_SIGN_IN else Load.HIDE
        answer.payloadCase == RespEnvelope.PayloadCase.RESP_WIFI_STATUS && answer.respWifiStatus.managed -> Load.SHOW
        else -> Load.HIDE
    }

    /** On load: a change still running (after the screen came back, say) is waited for again. */
    fun resumeID(status: WifiStatus): String? =
        if (status.hasLastJoin() && status.lastJoin.state == "joining" && status.lastJoin.id.isNotEmpty()) status.lastJoin.id else null

    fun afterJoin(answer: RespEnvelope): Sent = when {
        answer.error -> when {
            ErrorCodes.isUnknownPayload(answer.errorCode, answer.errorMessage) -> Sent.HideUntilSignIn
            answer.errorCode == "not_managed" -> Sent.Hide
            else -> Sent.Refused(words(answer))
        }
        answer.payloadCase == RespEnvelope.PayloadCase.RESP_WIFI_STATUS -> {
            val s = answer.respWifiStatus
            when {
                !s.hasLastJoin() -> Sent.Done(Outcome.Unknown)
                s.lastJoin.state == "joining" -> if (s.lastJoin.id.isEmpty()) Sent.Done(Outcome.Unknown) else Sent.Wait(s.lastJoin.id)
                else -> Sent.Done(outcome(s.lastJoin))
            }
        }
        // The bridge's answer while the device is away, or anything else.
        else -> Sent.Refused(words(answer))
    }

    /**
     * One look while waiting for [joinID]: the bridge's answers, refusals
     * and a device that says for a moment it doesn't manage its Wi-Fi are
     * waited through; the device's word on another change, or on none, ends
     * the wait without a result.
     */
    fun onPoll(answer: RespEnvelope, joinID: String): Poll {
        if (answer.error || answer.payloadCase != RespEnvelope.PayloadCase.RESP_WIFI_STATUS) return Poll.KeepWaiting
        val s = answer.respWifiStatus
        if (!s.managed) return Poll.KeepWaiting
        if (!s.hasLastJoin() || s.lastJoin.id != joinID) return Poll.Done(Outcome.Unknown)
        if (s.lastJoin.state == "joining") return Poll.KeepWaiting
        return Poll.Done(outcome(s.lastJoin))
    }

    /**
     * A failure the owner fixes by typing again: the picker stays open, the
     * network still chosen (open_network: a password typed for a network
     * that has none).
     */
    fun keepsPicker(code: String): Boolean = code in setOf("wrong_password", "auth_failed", "not_found", "password_required", "open_network")

    /**
     * Whether a listed network can be chosen: not one the device says it
     * can't join (enterprise, WEP, OWE), nor one whose name it wouldn't take
     * (control characters, over 32 bytes). Shown greyed out as "Not
     * supported".
     */
    fun joinable(n: WifiNetwork): Boolean =
        !n.unsupported && checkName(n.ssid) == null && n.ssid.none { it < ' ' || it == '\u007f' }

    /** [elapsed] since JoinWifi's answer (or the resume). */
    fun slow(elapsed: Duration): Boolean = elapsed >= slowAfter

    fun pollInterval(slow: Boolean): Duration = if (slow) slowPollEvery else pollEvery

    private fun outcome(j: WifiJoin): Outcome = when (j.state) {
        "connected" -> Outcome.Connected(j.message)
        "failed" -> Outcome.Failed(j.code, j.message)
        else -> Outcome.Unknown
    }

    /** An answer's words: its error message, else a bridge Ack's; null when it has none. */
    fun words(answer: RespEnvelope): String? = when {
        answer.errorMessage.isNotEmpty() -> answer.errorMessage
        answer.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && answer.respAck.errorMsg.isNotEmpty() -> answer.respAck.errorMsg
        else -> null
    }

    /** A signal (0-100) as a whole percentage in the app's language ("72%", "72 %"). */
    fun percent(signal: Int): UiText = S.appShellPercent("${signal.coerceIn(0, 100)}")
}
