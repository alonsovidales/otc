// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.WifiJoin
import cloud.offthe.otc.proto.WifiNetwork
import cloud.offthe.otc.proto.WifiNetworks
import cloud.offthe.otc.proto.WifiStatus
import cloud.offthe.otc.ui.settings.WifiFlow.Kind
import cloud.offthe.otc.ui.settings.WifiFlow.Load
import cloud.offthe.otc.ui.settings.WifiFlow.Outcome
import cloud.offthe.otc.ui.settings.WifiFlow.Poll
import cloud.offthe.otc.ui.settings.WifiFlow.Problem
import cloud.offthe.otc.ui.settings.WifiFlow.Sent
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.time.Duration.Companion.milliseconds
import kotlin.time.Duration.Companion.seconds

// Settings › Wi-Fi's rules: the shared case table (wifi-flow-cases.md),
// row for row the same as iOS's WifiFlowTests.swift. Each assertion names
// its row.
class WifiFlowTest {
    private fun join(id: String, state: String, code: String = "", message: String = "") =
        WifiJoin.newBuilder().setId(id).setState(state).setCode(code).setMessage(message).build()

    private fun status(managed: Boolean = true, lastJoin: WifiJoin? = null, ssid: String = "", wired: Boolean = false): WifiStatus {
        val b = WifiStatus.newBuilder().setManaged(managed).setSsid(ssid).setWired(wired)
        if (lastJoin != null) b.lastJoin = lastJoin
        return b.build()
    }

    private fun statusAnswer(managed: Boolean = true, lastJoin: WifiJoin? = null): RespEnvelope =
        RespEnvelope.newBuilder().setRespWifiStatus(status(managed, lastJoin)).build()

    private fun errorAnswer(code: String = "", message: String = ""): RespEnvelope =
        RespEnvelope.newBuilder().setError(true).setErrorCode(code).setErrorMessage(message).build()

    // The bridge's answer while the device is away.
    private fun ackAnswer(message: String = ""): RespEnvelope =
        RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(false).setErrorMsg(message).setCode("device_unreachable")).build()

    // 1. Names: UTF-8 bytes, never trimmed or changed.
    @Test fun names() {
        assertEquals("N1", Problem.NAME_NEEDED, WifiFlow.checkName(""))
        assertNull("N2", WifiFlow.checkName("a"))
        assertNull("N3", WifiFlow.checkName("a".repeat(32)))
        assertEquals("N4", Problem.NAME_TOO_LONG, WifiFlow.checkName("a".repeat(33)))
        assertNull("N5", WifiFlow.checkName("é".repeat(16)))
        assertEquals("N6", Problem.NAME_TOO_LONG, WifiFlow.checkName("é".repeat(17)))
        assertNull("N7", WifiFlow.checkName("😀".repeat(8)))
        assertEquals("N8", Problem.NAME_TOO_LONG, WifiFlow.checkName("😀".repeat(9)))
        assertNull("N9", WifiFlow.checkName(" Home "))
        assertNull("N10", WifiFlow.checkName("   "))
        assertNull("N11", WifiFlow.checkName("Casa\tWifi"))
        assertNull("N12", WifiFlow.checkName("Bob's"))
    }

    // 2. Passwords: 8-63 UTF-8 bytes without U+0000-U+001F or U+007F, or exactly 64 ASCII hex digits.
    @Test fun passwords() {
        assertEquals("P1", Problem.PASSWORD_NEEDED, WifiFlow.checkPassword("", Kind.SECURED))
        assertNull("P2", WifiFlow.checkPassword("", Kind.OPEN))
        assertNull("P3", WifiFlow.checkPassword("", Kind.OTHER))
        assertNull("P4", WifiFlow.checkPassword("abc", Kind.OPEN))
        assertEquals("P5", Problem.PASSWORD_INVALID, WifiFlow.checkPassword("a".repeat(7), Kind.SECURED))
        assertNull("P6", WifiFlow.checkPassword("a".repeat(8), Kind.SECURED))
        assertNull("P7", WifiFlow.checkPassword("a".repeat(63), Kind.SECURED))
        assertNull("P8", WifiFlow.checkPassword("a".repeat(64), Kind.SECURED))
        assertNull("P9", WifiFlow.checkPassword("0".repeat(64), Kind.SECURED))
        assertNull("P10", WifiFlow.checkPassword("aF".repeat(32), Kind.SECURED))
        assertEquals("P11", Problem.PASSWORD_INVALID, WifiFlow.checkPassword("a".repeat(63) + "g", Kind.SECURED))
        assertEquals("P12", Problem.PASSWORD_INVALID, WifiFlow.checkPassword("a".repeat(65), Kind.SECURED))
        assertNull("P13", WifiFlow.checkPassword("contraseña", Kind.SECURED))
        assertNull("P14", WifiFlow.checkPassword("ñ".repeat(31), Kind.SECURED))
        assertEquals("P15", Problem.PASSWORD_INVALID, WifiFlow.checkPassword("ñ".repeat(32), Kind.SECURED))
        assertNull("P16", WifiFlow.checkPassword("€€€", Kind.SECURED))
        assertEquals("P17", Problem.PASSWORD_INVALID, WifiFlow.checkPassword("€€", Kind.SECURED))
        assertEquals("P18", Problem.PASSWORD_INVALID, WifiFlow.checkPassword("pass\tword", Kind.SECURED))
        assertEquals("P19", Problem.PASSWORD_INVALID, WifiFlow.checkPassword("password\u007f", Kind.SECURED))
        assertNull("P20", WifiFlow.checkPassword("pass word", Kind.SECURED))
        assertEquals("P21", Problem.PASSWORD_INVALID, WifiFlow.checkPassword("０".repeat(64), Kind.SECURED))
        assertEquals("P22", Problem.PASSWORD_INVALID, WifiFlow.checkPassword("1234567", Kind.OTHER))
        assertEquals("P23", Problem.PASSWORD_INVALID, WifiFlow.checkPassword("éé", Kind.SECURED))
        assertNull("P24", WifiFlow.checkPassword("ééé", Kind.SECURED))
    }

    // 3. check (the name first) and what is sent.
    @Test fun checkAndWhatIsSent() {
        assertEquals("C1", Problem.NAME_NEEDED, WifiFlow.check("", "", Kind.OTHER))
        assertEquals("C2", Problem.NAME_NEEDED, WifiFlow.check("", "short", Kind.OTHER))
        assertEquals("C3", Problem.NAME_TOO_LONG, WifiFlow.check("a".repeat(33), "", Kind.SECURED))
        assertEquals("C4", Problem.PASSWORD_NEEDED, WifiFlow.check("Home", "", Kind.SECURED))

        assertNull("C5", WifiFlow.check("Home", "secret12", Kind.SECURED))
        assertEquals("C5", "secret12", WifiFlow.sentPassword("secret12", Kind.SECURED))
        assertFalse("C5", WifiFlow.hidden(Kind.SECURED))

        assertNull("C6", WifiFlow.check("Cafe", "typed", Kind.OPEN))
        assertEquals("C6", "", WifiFlow.sentPassword("typed", Kind.OPEN))
        assertFalse("C6", WifiFlow.hidden(Kind.OPEN))

        assertNull("C7", WifiFlow.check("Hidden", "", Kind.OTHER))
        assertEquals("C7", "", WifiFlow.sentPassword("", Kind.OTHER))
        assertTrue("C7", WifiFlow.hidden(Kind.OTHER))

        assertNull("C8", WifiFlow.check("Hidden", "secret12", Kind.OTHER))
        assertEquals("C8", "secret12", WifiFlow.sentPassword("secret12", Kind.OTHER))
        assertTrue("C8", WifiFlow.hidden(Kind.OTHER))
    }

    // Each problem has its words.
    @Test fun problemTexts() {
        assertEquals("Enter the network's name.", WifiFlow.text(Problem.NAME_NEEDED).english())
        assertEquals("That name is too long: a Wi-Fi network name has at most 32 characters, and accented letters and emoji count as two or more.", WifiFlow.text(Problem.NAME_TOO_LONG).english())
        assertEquals("Enter the network's password.", WifiFlow.text(Problem.PASSWORD_NEEDED).english())
        assertEquals("A Wi-Fi password has 8 to 63 characters (accented letters and some symbols count as two or three), or exactly 64 hexadecimal digits.", WifiFlow.text(Problem.PASSWORD_INVALID).english())
    }

    // A signal as a whole percentage, kept within 0-100 (as iOS's WifiFlow.percent).
    @Test fun signalPercent() {
        assertEquals("72%", WifiFlow.percent(72).english())
        assertEquals("0%", WifiFlow.percent(-3).english())
        assertEquals("100%", WifiFlow.percent(140).english())
    }

    // An answer's words: its error message first, else the bridge Ack's.
    @Test fun words() {
        assertEquals("M", WifiFlow.words(errorAnswer("busy", "M")))
        assertEquals("M", WifiFlow.words(ackAnswer("M")))
        assertNull(WifiFlow.words(ackAnswer("")))
        assertNull(WifiFlow.words(errorAnswer("", "")))
    }

    // 4. Which confirmation message.
    @Test fun cableOnly() {
        assertTrue("K1", WifiFlow.cableOnly(true, ""))
        assertFalse("K2", WifiFlow.cableOnly(true, "Home"))
        assertFalse("K3", WifiFlow.cableOnly(false, ""))
        assertFalse("K4", WifiFlow.cableOnly(false, "Home"))
    }

    // 5. GetWifiStatus on load, and a change still running.
    @Test fun onLoad() {
        assertEquals("L1", Load.SHOW, WifiFlow.onLoad(statusAnswer(managed = true)))
        assertEquals("L2", Load.HIDE, WifiFlow.onLoad(statusAnswer(managed = false)))
        assertEquals("L3", Load.HIDE_UNTIL_SIGN_IN, WifiFlow.onLoad(errorAnswer("unknown_payload", "Unknown request")))
        assertEquals("L4", Load.HIDE_UNTIL_SIGN_IN, WifiFlow.onLoad(errorAnswer("", "unknown payload")))
        assertEquals("L5", Load.HIDE, WifiFlow.onLoad(errorAnswer("demo", "M")))
        assertEquals("L6", Load.HIDE, WifiFlow.onLoad(errorAnswer("", "Not available on this instance")))
        assertEquals("L7", Load.HIDE, WifiFlow.onLoad(ackAnswer("M")))
    }

    @Test fun resumeID() {
        assertNull("R1", WifiFlow.resumeID(status()))
        assertEquals("R2", "a1", WifiFlow.resumeID(status(lastJoin = join("a1", "joining"))))
        assertNull("R3", WifiFlow.resumeID(status(lastJoin = join("a1", "connected"))))
        assertNull("R4", WifiFlow.resumeID(status(lastJoin = join("a1", "failed"))))
    }

    // 6. JoinWifi's own answer.
    @Test fun afterJoin() {
        assertEquals("J1", Sent.Wait("a1"), WifiFlow.afterJoin(statusAnswer(lastJoin = join("a1", "joining"))))
        assertEquals("J2", Sent.Done(Outcome.Failed("busy", "M")), WifiFlow.afterJoin(statusAnswer(lastJoin = join("a1", "failed", "busy", "M"))))
        assertEquals("J3", Sent.Done(Outcome.Connected("M")), WifiFlow.afterJoin(statusAnswer(lastJoin = join("a1", "connected", "connected", "M"))))
        assertEquals("J4", Sent.Done(Outcome.Unknown), WifiFlow.afterJoin(statusAnswer()))
        assertEquals("J5", Sent.Done(Outcome.Unknown), WifiFlow.afterJoin(statusAnswer(lastJoin = join("", "joining"))))
        assertEquals("J6", Sent.HideUntilSignIn, WifiFlow.afterJoin(errorAnswer("unknown_payload", "Unknown request")))
        assertEquals("J7", Sent.Hide, WifiFlow.afterJoin(errorAnswer("not_managed", "M")))
        assertEquals("J8", Sent.Refused("M"), WifiFlow.afterJoin(errorAnswer("busy", "M")))
        assertEquals("J9", Sent.Refused("M"), WifiFlow.afterJoin(errorAnswer("bad_password", "M")))
        assertEquals("J10", Sent.Refused("M"), WifiFlow.afterJoin(errorAnswer("demo", "M")))
        assertEquals("J11", Sent.Refused(null), WifiFlow.afterJoin(errorAnswer("", "")))
        assertEquals("J12", Sent.Refused("M"), WifiFlow.afterJoin(ackAnswer("M")))
        assertEquals("J13", Sent.Refused(null), WifiFlow.afterJoin(ackAnswer("")))
        assertEquals("J13", Sent.Refused(null), WifiFlow.afterJoin(RespEnvelope.newBuilder().setRespWifiNetworks(WifiNetworks.getDefaultInstance()).build()))
    }

    // 7. A look while waiting for "a1" (a thrown look, W1, never reaches onPoll: the caller waits on).
    @Test fun onPoll() {
        val id = "a1"
        assertEquals("W2", Poll.KeepWaiting, WifiFlow.onPoll(ackAnswer("M"), id))
        assertEquals("W3", Poll.KeepWaiting, WifiFlow.onPoll(errorAnswer("unknown_payload", "M"), id))
        assertEquals("W3", Poll.KeepWaiting, WifiFlow.onPoll(errorAnswer("", ""), id))
        assertEquals("W4", Poll.KeepWaiting, WifiFlow.onPoll(statusAnswer(managed = false, lastJoin = join(id, "connected")), id))
        assertEquals("W5", Poll.KeepWaiting, WifiFlow.onPoll(statusAnswer(lastJoin = join(id, "joining")), id))
        assertEquals("W6", Poll.Done(Outcome.Connected("Connected to Home.")), WifiFlow.onPoll(statusAnswer(lastJoin = join(id, "connected", "connected", "Connected to Home.")), id))
        assertEquals("W7", Poll.Done(Outcome.Failed("wrong_password", "M")), WifiFlow.onPoll(statusAnswer(lastJoin = join(id, "failed", "wrong_password", "M")), id))
        assertEquals("W8", Poll.Done(Outcome.Failed("no_internet", "")), WifiFlow.onPoll(statusAnswer(lastJoin = join(id, "failed", "no_internet", "")), id))
        assertEquals("W9", Poll.Done(Outcome.Unknown), WifiFlow.onPoll(statusAnswer(), id))
        assertEquals("W10", Poll.Done(Outcome.Unknown), WifiFlow.onPoll(statusAnswer(lastJoin = join("b2", "joining")), id))
        assertEquals("W11", Poll.Done(Outcome.Unknown), WifiFlow.onPoll(statusAnswer(lastJoin = join("b2", "connected")), id))
        assertEquals("W12", Poll.Done(Outcome.Unknown), WifiFlow.onPoll(statusAnswer(lastJoin = join(id, "later")), id))
    }

    // 8. The failures that leave the picker open, the network still chosen.
    @Test fun keepsPicker() {
        assertTrue("K-1", WifiFlow.keepsPicker("wrong_password"))
        assertTrue("K-2", WifiFlow.keepsPicker("auth_failed"))
        assertTrue("K-3", WifiFlow.keepsPicker("not_found"))
        assertTrue("K-4", WifiFlow.keepsPicker("password_required"))
        assertTrue("K-4b", WifiFlow.keepsPicker("open_network"))
        for (code in listOf("timed_out", "no_address", "no_internet", "join_failed", "interrupted", "unsupported_security", "bad_request", "no_wifi", "busy", "update_running", "too_many", "no_answer")) {
            assertFalse("K-5 $code", WifiFlow.keepsPicker(code))
        }
        assertFalse("K-6", WifiFlow.keepsPicker(""))
        assertFalse("K-6", WifiFlow.keepsPicker("something_new"))
    }

    // Which listed networks can be chosen.
    @Test fun joinable() {
        fun net(ssid: String, secured: Boolean = true, unsupported: Boolean = false) =
            WifiNetwork.newBuilder().setSsid(ssid).setSecured(secured).setUnsupported(unsupported).setSignal(50).build()
        assertFalse("U1", WifiFlow.joinable(net("Corp", unsupported = true)))
        assertFalse("U2", WifiFlow.joinable(net("Casa\tWifi")))
        assertFalse("U3", WifiFlow.joinable(net("a".repeat(33))))
        assertTrue("U4", WifiFlow.joinable(net("Home")))
        assertTrue("U5", WifiFlow.joinable(net("Cafe", secured = false)))
    }

    // 9. Timing.
    @Test fun timing() {
        assertFalse("T1", WifiFlow.slow(0.seconds))
        assertFalse("T2", WifiFlow.slow(239_900.milliseconds))
        assertTrue("T3", WifiFlow.slow(240.seconds))
        assertTrue("T4", WifiFlow.slow(600.seconds))
        assertEquals("T5", 3.seconds, WifiFlow.pollInterval(false))
        assertEquals("T6", 10.seconds, WifiFlow.pollInterval(true))
        assertEquals("T7", 10.seconds, WifiFlow.lookTimeout)
    }
}
