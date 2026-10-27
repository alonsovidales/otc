// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// The Bluetooth setup: leaving asks first only while the device installs,
/// and half a minute of looking with nothing found says what to check.
/// Android's BluetoothSetupTest is the same.
struct BluetoothSetupTests {
    @Test func leavingAsksOnlyWhileTheDeviceInstalls() {
        // Before the install: nothing to lose but the page.
        #expect(!BluetoothSetupView.leaveAsks(installStarted: false, readyDomain: nil))
        #expect(BluetoothSetupView.leaveAsks(installStarted: true, readyDomain: nil))
        // Online ("Open my device" shows): no install left to follow.
        #expect(!BluetoothSetupView.leaveAsks(installStarted: true, readyDomain: "pit.off-the.cloud"))
        #expect(!BluetoothSetupView.leaveAsks(installStarted: true, readyDomain: ""))
        #expect(S.appShellSetupLeaveTitle == "Leave the setup?")
        #expect(S.appShellSetupLeaveText == "The device goes on installing. Open the setup again to follow it, or sign in once it's done.")
    }

    @Test func nothingFoundForHalfAMinuteSaysWhatToCheck() {
        #expect(BluetoothSetupView.notFoundSeconds == 30)
        #expect(S.appShellSetupNotFound == "No device found yet. Check that it's on, with its disks connected, and keep the phone next to it.")
        #expect(S.iosOpenSettings == "Open Settings")
    }
}
