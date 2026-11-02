// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Settings > Shut down device (issue #202, DeviceShutdown.swift): what the
/// device's answers come to, and the moment the app leaves a device that
/// is turning off alone. Android's DeviceShutdownTest is the same.
struct DeviceShutdownTests {
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

    @Test func theAckIsShuttingDown() {
        #expect(ShutDownAnswer.of(ack()) == .shuttingDown)
    }

    @Test func anOlderDeviceNeedsAnUpdate() {
        #expect(ShutDownAnswer.of(refusal("unknown_payload", "")) == .needsUpdate)
        // Builds before v9 said it in words alone.
        #expect(ShutDownAnswer.of(refusal("", "unknown payload")) == .needsUpdate)
    }

    @Test func otherRefusalsAreTheDevicesWords() {
        // The demo, a container, a user's instance, an update under way.
        #expect(ShutDownAnswer.of(refusal("demo", "This is a demo device.")) == .refused("This is a demo device."))
        #expect(ShutDownAnswer.of(refusal("update_running", "An update is running.")) == .refused("An update is running."))
        // Words only in the Ack.
        #expect(ShutDownAnswer.of(ack(ok: false, errorMsg: "Not now.")) == .refused("Not now."))
        // A localized device's words that happen to say "unknown payload"
        // with another code are its words.
        #expect(ShutDownAnswer.of(refusal("busy", "unknown payload here")) == .refused("unknown payload here"))
    }

    @Test func noWordsAreOurs() {
        #expect(ShutDownAnswer.of(refusal("", "")) == .refused(nil))
        #expect(ShutDownAnswer.of(ack(ok: false)) == .refused(nil))
        #expect(ShutDownAnswer.of(Msg_RespEnvelope()) == .refused(nil))
        // An Ack on an envelope that says error isn't a yes.
        var odd = ack()
        odd.error = true
        #expect(ShutDownAnswer.of(odd) == .refused(nil))
    }

    @MainActor
    @Test func theDeviceIsLeftAloneForAMomentAfterItsYes() {
        let shutdown = DeviceShutdown()
        var clock = Date(timeIntervalSince1970: 1_800_000_000)
        shutdown.now = { clock }
        // Nothing asked: as always.
        #expect(!shutdown.quiet)
        #expect(shutdown.quietLeft == 0)

        shutdown.began()
        #expect(shutdown.quiet)
        #expect(shutdown.quietLeft == DeviceShutdown.quietFor)

        clock += 60
        #expect(shutdown.quiet)
        #expect(shutdown.quietLeft == DeviceShutdown.quietFor - 60)

        // Afterwards the app looks for it again (and shows the usual card).
        clock += DeviceShutdown.quietFor
        #expect(!shutdown.quiet)
        #expect(shutdown.quietLeft == 0)
    }

    @MainActor
    @Test func newCredentialsOrLogOutForgetIt() {
        let shutdown = DeviceShutdown()
        shutdown.began()
        #expect(shutdown.quiet)
        shutdown.forget()
        #expect(!shutdown.quiet)
        #expect(shutdown.since == nil)
    }

    @MainActor
    @Test func aRequestInThatMomentSaysSo() {
        #expect(DeviceShutdown.Quiet().localizedDescription == "Shutting down… Once its lights stop blinking you can unplug it.")
    }

    @Test func everyTextReadsAsWritten() {
        #expect(S.appSettingsUpdateShutDown == "Shut Down Device")
        #expect(S.appSettingsUpdateShutDownTitle == "Shut down your device?")
        #expect(S.appSettingsUpdateShutDownMessage == "It turns off completely and stays off until you start it again: unplug its power and plug it back in. Nobody can reach it meanwhile.")
        #expect(S.appSettingsUpdateShutDownConfirm == "Shut Down")
        #expect(S.appSettingsUpdateShuttingDownNote == "Shutting down… Once its lights stop blinking you can unplug it.")
        #expect(S.appSettingsUpdateShutDownFailed == "Could not shut down the device.")
        #expect(S.appSettingsUpdateShutDownNeedsUpdate == "Your device needs an update before it can be shut down from here.")
    }
}
