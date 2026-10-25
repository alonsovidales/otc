// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Settings › Wi-Fi (WifiSection.swift's WifiFlow): what may be sent, and
/// what each answer means for the section. The same case table as
/// Android's WifiFlowTest.kt (the row names, N1, P1..., are that table's).
@MainActor
struct WifiFlowTests {
    // MARK: Answers

    private func join(_ id: String, _ state: String, code: String = "", message: String = "") -> Msg_WifiJoin {
        var j = Msg_WifiJoin()
        j.id = id
        j.state = state
        j.code = code
        j.message = message
        j.ssid = "Home"
        return j
    }

    private func status(managed: Bool = true, ssid: String = "", wired: Bool = false, last: Msg_WifiJoin? = nil) -> Msg_WifiStatus {
        var s = Msg_WifiStatus()
        s.managed = managed
        s.ssid = ssid
        s.wired = wired
        if let last { s.lastJoin = last }
        return s
    }

    private func answer(_ s: Msg_WifiStatus) -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        r.payload = .respWifiStatus(s)
        return r
    }

    private func refusal(_ code: String, _ message: String = "") -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        r.error = true
        r.errorCode = code
        r.errorMessage = message
        return r
    }

    private func bridgeAck(_ message: String) -> Msg_RespEnvelope {
        var a = Msg_Ack()
        a.ok = false
        a.code = "device_unreachable"
        a.errorMsg = message
        var r = Msg_RespEnvelope()
        r.payload = .respAck(a)
        return r
    }

    private func s(_ part: String, _ n: Int) -> String { String(repeating: part, count: n) }

    // MARK: 1. Names

    @Test func names() {
        #expect(WifiFlow.checkName("") == .nameNeeded)                       // N1
        #expect(WifiFlow.checkName("a") == nil)                              // N2
        #expect(WifiFlow.checkName(s("a", 32)) == nil)                       // N3
        #expect(WifiFlow.checkName(s("a", 33)) == .nameTooLong)              // N4
        #expect(WifiFlow.checkName(s("é", 16)) == nil)                       // N5
        #expect(WifiFlow.checkName(s("é", 17)) == .nameTooLong)              // N6
        #expect(WifiFlow.checkName(s("😀", 8)) == nil)                       // N7
        #expect(WifiFlow.checkName(s("😀", 9)) == .nameTooLong)              // N8
        #expect(WifiFlow.checkName(" Home ") == nil)                         // N9
        #expect(WifiFlow.checkName("   ") == nil)                            // N10
        #expect(WifiFlow.checkName("Casa\tWifi") == nil)                     // N11
        #expect(WifiFlow.checkName("Bob's") == nil)                          // N12
    }

    // MARK: 2. Passwords

    @Test func passwords() {
        #expect(WifiFlow.checkPassword("", kind: .secured) == .passwordNeeded)           // P1
        #expect(WifiFlow.checkPassword("", kind: .open) == nil)                          // P2
        #expect(WifiFlow.checkPassword("", kind: .other) == nil)                         // P3
        #expect(WifiFlow.checkPassword("abc", kind: .open) == nil)                       // P4
        #expect(WifiFlow.checkPassword(s("a", 7), kind: .secured) == .passwordInvalid)   // P5
        #expect(WifiFlow.checkPassword(s("a", 8), kind: .secured) == nil)                // P6
        #expect(WifiFlow.checkPassword(s("a", 63), kind: .secured) == nil)               // P7
        #expect(WifiFlow.checkPassword(s("a", 64), kind: .secured) == nil)               // P8
        #expect(WifiFlow.checkPassword(s("0", 64), kind: .secured) == nil)               // P9
        #expect(WifiFlow.checkPassword(s("aF", 32), kind: .secured) == nil)              // P10
        #expect(WifiFlow.checkPassword(s("a", 63) + "g", kind: .secured) == .passwordInvalid) // P11
        #expect(WifiFlow.checkPassword(s("a", 65), kind: .secured) == .passwordInvalid)  // P12
        #expect(WifiFlow.checkPassword("contraseña", kind: .secured) == nil)             // P13
        #expect(WifiFlow.checkPassword(s("ñ", 31), kind: .secured) == nil)               // P14
        #expect(WifiFlow.checkPassword(s("ñ", 32), kind: .secured) == .passwordInvalid)  // P15
        #expect(WifiFlow.checkPassword("€€€", kind: .secured) == nil)                    // P16
        #expect(WifiFlow.checkPassword("€€", kind: .secured) == .passwordInvalid)        // P17
        #expect(WifiFlow.checkPassword("pass\tword", kind: .secured) == .passwordInvalid) // P18
        #expect(WifiFlow.checkPassword("password\u{7f}", kind: .secured) == .passwordInvalid) // P19
        #expect(WifiFlow.checkPassword("pass word", kind: .secured) == nil)              // P20
        #expect(WifiFlow.checkPassword(s("\u{FF10}", 64), kind: .secured) == .passwordInvalid) // P21
        #expect(WifiFlow.checkPassword("1234567", kind: .other) == .passwordInvalid)     // P22
        #expect(WifiFlow.checkPassword("e\u{301}e\u{301}", kind: .secured) == .passwordInvalid) // P23
        #expect(WifiFlow.checkPassword("e\u{301}e\u{301}e\u{301}", kind: .secured) == nil) // P24
    }

    // MARK: 3. check and what is sent

    @Test func checkNameFirstAndWhatIsSent() {
        #expect(WifiFlow.check(ssid: "", password: "", kind: .other) == .nameNeeded)              // C1
        #expect(WifiFlow.check(ssid: "", password: "short", kind: .other) == .nameNeeded)         // C2
        #expect(WifiFlow.check(ssid: s("a", 33), password: "", kind: .secured) == .nameTooLong)   // C3
        #expect(WifiFlow.check(ssid: "Home", password: "", kind: .secured) == .passwordNeeded)    // C4

        // C5
        #expect(WifiFlow.check(ssid: "Home", password: "secret12", kind: .secured) == nil)
        #expect(WifiFlow.sentPassword("secret12", kind: .secured) == "secret12")
        #expect(!WifiFlow.hidden(.secured))
        // C6
        #expect(WifiFlow.check(ssid: "Cafe", password: "typed", kind: .open) == nil)
        #expect(WifiFlow.sentPassword("typed", kind: .open) == "")
        #expect(!WifiFlow.hidden(.open))
        // C7
        #expect(WifiFlow.check(ssid: "Hidden", password: "", kind: .other) == nil)
        #expect(WifiFlow.sentPassword("", kind: .other) == "")
        #expect(WifiFlow.hidden(.other))
        // C8
        #expect(WifiFlow.check(ssid: "Hidden", password: "secret12", kind: .other) == nil)
        #expect(WifiFlow.sentPassword("secret12", kind: .other) == "secret12")
    }

    // MARK: 4. Which confirmation

    @Test func cableOnly() {
        #expect(WifiFlow.cableOnly(wired: true, ssid: ""))         // K1
        #expect(!WifiFlow.cableOnly(wired: true, ssid: "Home"))    // K2
        #expect(!WifiFlow.cableOnly(wired: false, ssid: ""))       // K3
        #expect(!WifiFlow.cableOnly(wired: false, ssid: "Home"))   // K4
    }

    // MARK: 5. On load

    @Test func onLoad() {
        #expect(WifiFlow.onLoad(answer(status(managed: true))) == .show)                       // L1
        #expect(WifiFlow.onLoad(answer(status(managed: false))) == .hide)                      // L2
        #expect(WifiFlow.onLoad(refusal("unknown_payload", "Unknown request")) == .hideUntilSignIn) // L3
        #expect(WifiFlow.onLoad(refusal("", "unknown payload")) == .hideUntilSignIn)           // L4
        #expect(WifiFlow.onLoad(refusal("demo", "M")) == .hide)                                // L5
        #expect(WifiFlow.onLoad(refusal("", "Not on this instance")) == .hide)                 // L6
        #expect(WifiFlow.onLoad(bridgeAck("M")) == .hide)                                      // L7
    }

    @Test func resumeID() {
        #expect(WifiFlow.resumeID(status()) == nil)                                    // R1
        #expect(WifiFlow.resumeID(status(last: join("a1", "joining"))) == "a1")        // R2
        #expect(WifiFlow.resumeID(status(last: join("a1", "connected"))) == nil)       // R3
        #expect(WifiFlow.resumeID(status(last: join("a1", "failed"))) == nil)          // R4
    }

    // MARK: 6. JoinWifi's answer

    @Test func afterJoin() {
        #expect(WifiFlow.afterJoin(answer(status(last: join("a1", "joining")))) == .wait("a1"))   // J1
        #expect(WifiFlow.afterJoin(answer(status(last: join("a1", "failed", code: "busy", message: "M"))))
                == .done(.failed(code: "busy", message: "M")))                                    // J2
        #expect(WifiFlow.afterJoin(answer(status(last: join("a1", "connected", message: "M"))))
                == .done(.connected("M")))                                                        // J3
        #expect(WifiFlow.afterJoin(answer(status())) == .done(.unknown))                          // J4
        #expect(WifiFlow.afterJoin(answer(status(last: join("", "joining")))) == .done(.unknown)) // J5
        #expect(WifiFlow.afterJoin(refusal("unknown_payload", "M")) == .hideUntilSignIn)          // J6
        #expect(WifiFlow.afterJoin(refusal("not_managed", "M")) == .hide)                         // J7
        #expect(WifiFlow.afterJoin(refusal("busy", "M")) == .refused("M"))                        // J8
        #expect(WifiFlow.afterJoin(refusal("bad_password", "M")) == .refused("M"))                // J9
        #expect(WifiFlow.afterJoin(refusal("demo", "M")) == .refused("M"))                        // J10
        #expect(WifiFlow.afterJoin(refusal("", "")) == .refused(nil))                             // J11
        #expect(WifiFlow.afterJoin(bridgeAck("M")) == .refused("M"))                              // J12
        #expect(WifiFlow.afterJoin(bridgeAck("")) == .refused(nil))                               // J13
        var other = Msg_RespEnvelope()
        other.payload = .respWifiNetworks(Msg_WifiNetworks())
        #expect(WifiFlow.afterJoin(other) == .refused(nil))                                       // J13
    }

    // MARK: 7. While waiting

    @Test func onPoll() {
        let id = "a1"
        // W1 (a thrown look) never reaches onPoll: the wait just goes on.
        #expect(WifiFlow.onPoll(bridgeAck("away"), joinID: id) == .keepWaiting)                    // W2
        #expect(WifiFlow.onPoll(refusal("unknown_payload", "M"), joinID: id) == .keepWaiting)      // W3
        #expect(WifiFlow.onPoll(refusal("", "M"), joinID: id) == .keepWaiting)                     // W3
        #expect(WifiFlow.onPoll(answer(status(managed: false)), joinID: id) == .keepWaiting)       // W4
        #expect(WifiFlow.onPoll(answer(status(last: join(id, "joining"))), joinID: id) == .keepWaiting) // W5
        #expect(WifiFlow.onPoll(answer(status(last: join(id, "connected", message: "Connected to Home."))), joinID: id)
                == .done(.connected("Connected to Home.")))                                        // W6
        #expect(WifiFlow.onPoll(answer(status(last: join(id, "failed", code: "wrong_password", message: "M"))), joinID: id)
                == .done(.failed(code: "wrong_password", message: "M")))                           // W7
        #expect(WifiFlow.onPoll(answer(status(last: join(id, "failed", code: "no_internet", message: ""))), joinID: id)
                == .done(.failed(code: "no_internet", message: "")))                               // W8
        #expect(WifiFlow.onPoll(answer(status()), joinID: id) == .done(.unknown))                  // W9
        #expect(WifiFlow.onPoll(answer(status(last: join("b2", "joining"))), joinID: id) == .done(.unknown))   // W10
        #expect(WifiFlow.onPoll(answer(status(last: join("b2", "connected"))), joinID: id) == .done(.unknown)) // W11
        #expect(WifiFlow.onPoll(answer(status(last: join(id, "later"))), joinID: id) == .done(.unknown))       // W12
    }

    // MARK: 8. Which failures keep the picker open

    @Test func keepsPicker() {
        for code in ["wrong_password", "auth_failed", "not_found", "password_required",     // K-1..K-4
                     "open_network"] {                                                       // K-4b
            #expect(WifiFlow.keepsPicker(code), "\(code)")
        }
        for code in ["timed_out", "no_address", "no_internet", "join_failed", "interrupted",
                     "unsupported_security", "bad_request", "no_wifi", "busy", "update_running",
                     "too_many", "no_answer", "", "something_new"] {                         // K-5, K-6
            #expect(!WifiFlow.keepsPicker(code), "\(code)")
        }
    }

    // MARK: Which listed networks can be chosen

    private func network(_ ssid: String, secured: Bool = true, unsupported: Bool = false) -> Msg_WifiNetwork {
        var n = Msg_WifiNetwork()
        n.ssid = ssid
        n.secured = secured
        n.unsupported = unsupported
        n.signal = 50
        return n
    }

    @Test func joinable() {
        #expect(!WifiFlow.joinable(network("Corp", unsupported: true)))   // U1
        #expect(!WifiFlow.joinable(network("Casa\tWifi")))                // U2
        #expect(!WifiFlow.joinable(network(s("a", 33))))                  // U3
        #expect(WifiFlow.joinable(network("Home")))                       // U4
        #expect(WifiFlow.joinable(network("Cafe", secured: false)))       // U5
    }

    // MARK: 9. Timing

    @Test func timing() {
        #expect(!WifiFlow.slow(0))                                // T1
        #expect(!WifiFlow.slow(239.9))                            // T2
        #expect(WifiFlow.slow(240))                               // T3
        #expect(WifiFlow.slow(600))                               // T4
        #expect(WifiFlow.pollInterval(slow: false) == 3)          // T5
        #expect(WifiFlow.pollInterval(slow: true) == 10)          // T6
        #expect(WifiFlow.lookTimeout == 10)                       // T7
    }

    // MARK: iOS only

    /// The signal as a whole percentage in the app's language.
    @Test func signalPercent() {
        let en = Locale(identifier: "en_US")
        #expect(WifiFlow.percent(72, locale: en) == "72%")
        #expect(WifiFlow.percent(0, locale: en) == "0%")
        #expect(WifiFlow.percent(150, locale: en) == "100%")
        #expect(WifiFlow.percent(-5, locale: en) == "0%")
        #expect(WifiFlow.percent(72, locale: Locale(identifier: "de_DE")) == "72\u{a0}%")
    }
}
