// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  DeviceShutdown.swift
//  OffTheCloud
//
//  Issue #202: Settings > Shut down device (UpdateSection, next to Restart
//  device). Once the device has said yes it turns itself off and stays off
//  until its power is unplugged and plugged in again - it isn't coming back
//  by itself. So for a moment (`quietFor`) the app stops treating it as
//  merely away: no reconnects to a device that is powering off (the
//  disconnect's own retry and every poller's request would each dial it, at
//  home or through the bridge), and so no "isn't available" or "can't
//  connect" card over the Settings that say it is shutting down. A request
//  in that moment fails at once, saying so (`Quiet`). Afterwards everything
//  is as always: the next request dials, and the usual card shows until
//  the device is back. Android's net/DeviceShutdown.kt is the same.
//

import Foundation

/// What a ShutDownDevice answer comes to.
enum ShutDownAnswer: Equatable {
    /// The device said yes (the generic Ack): it is turning itself off.
    case shuttingDown
    /// A device before Settings > Shut down device (unknown_payload):
    /// DeviceNeedsUpdate.
    case needsUpdate
    /// Turned down (a user's instance, a container, the demo, an update or
    /// a Wi-Fi change under way): the device's words, nil when it gave none.
    case refused(String?)

    static func of(_ resp: Msg_RespEnvelope) -> ShutDownAnswer {
        if !resp.error, case .respAck(let ack) = resp.payload, ack.ok { return .shuttingDown }
        if resp.error && ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) {
            return .needsUpdate
        }
        if resp.error && !resp.errorMessage.isEmpty { return .refused(resp.errorMessage) }
        if case .respAck(let ack) = resp.payload, !ack.errorMsg.isEmpty { return .refused(ack.errorMsg) }
        return .refused(nil)
    }
}

@MainActor
final class DeviceShutdown {
    static let shared = DeviceShutdown()

    /// How long after the device's yes the app leaves it alone: a Raspberry
    /// Pi is off well within it, and one plugged in again at once takes
    /// about as long to start.
    nonisolated static let quietFor: TimeInterval = 90

    /// Thrown by a connect asked for in that moment, instead of dialling;
    /// its words are the section's ("Shutting down… Once its lights stop
    /// blinking you can unplug it.").
    struct Quiet: LocalizedError {
        var errorDescription: String? { S.appSettingsUpdateShuttingDownNote }
    }

    /// When the device said yes; nil when it hasn't, or that moment was
    /// forgotten (new credentials, Log Out).
    private(set) var since: Date?
    /// The clock (tests set their own).
    var now: () -> Date = Date.init

    /// The device said yes to ShutDownDevice.
    func began() {
        since = now()
    }

    /// Another device or Log Out (OTCConnection.invalidate): whatever comes
    /// next is a device of its own.
    func forget() {
        since = nil
    }

    /// Seconds of that moment left: 0 once it is over or with none.
    var quietLeft: TimeInterval {
        guard let since else { return 0 }
        return max(0, Self.quietFor - now().timeIntervalSince(since))
    }

    /// The app leaves the device alone now.
    var quiet: Bool { quietLeft > 0 }
}
