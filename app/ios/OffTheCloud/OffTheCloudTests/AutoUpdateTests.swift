// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Settings > Device version's Automatic updates switch (UpdateSection.swift's
/// AutoUpdate): when it is drawn, and what the device's answers come to.
struct AutoUpdateTests {
    private func ack(ok: Bool = true, errorMsg: String = "") -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        var a = Msg_Ack()
        a.ok = ok
        a.errorMsg = errorMsg
        r.payload = .respAck(a)
        return r
    }

    private func refusal(_ code: String, _ message: String) -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        r.error = true
        r.errorCode = code
        r.errorMessage = message
        return r
    }

    @Test func aDeviceWithoutTheSwitchShowsNothing() {
        // An older device, a container: auto_update unset.
        #expect(AutoUpdate.shown(Msg_RespUpdateInfo()) == nil)
        var info = Msg_RespUpdateInfo()
        info.currentVersion = 183
        #expect(AutoUpdate.shown(info) == nil)
    }

    @Test func theSwitchIsAsTheDeviceSays() {
        var info = Msg_RespUpdateInfo()
        info.autoUpdate = true
        #expect(AutoUpdate.shown(info) == true)
        // Off is still a switch: set, and false.
        info.autoUpdate = false
        #expect(AutoUpdate.shown(info) == false)
        info.clearAutoUpdate()
        #expect(AutoUpdate.shown(info) == nil)
    }

    @Test func theAckIsChanged() {
        #expect(AutoUpdate.Answer.of(ack()) == .changed)
    }

    @Test func aRefusalCarriesTheDevicesWords() {
        #expect(AutoUpdate.Answer.of(refusal("demo", "This is a demo device.")) == .refused("This is a demo device."))
        #expect(AutoUpdate.Answer.of(ack(ok: false, errorMsg: "Not now.")) == .refused("Not now."))
    }

    @Test func aRefusalWithoutWordsHasNone() {
        #expect(AutoUpdate.Answer.of(refusal("", "")) == .refused(nil))
        #expect(AutoUpdate.Answer.of(ack(ok: false)) == .refused(nil))
        #expect(AutoUpdate.Answer.of(Msg_RespEnvelope()) == .refused(nil))
        // An Ack inside an error reply isn't a yes.
        var odd = ack()
        odd.error = true
        #expect(AutoUpdate.Answer.of(odd) == .refused(nil))
    }

    @Test func everyTextReadsAsWritten() {
        #expect(S.appSettingsUpdateAuto == "Automatic updates")
        #expect(S.appSettingsUpdateAutoFailed == "Could not change automatic updates.")
        #expect(S.appSettingsUpdateAutoHint == "Your device checks for a new version every 12 hours and installs it by itself. It restarts on the way, which takes a few minutes.")
    }
}
