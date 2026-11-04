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

    /// An address the wizard reports that isn't an off-the.cloud name is
    /// opened only once the owner has seen it: something nearby passing for
    /// the device could report a look-alike, and the photos would go there.
    @Test func anotherDomainIsOpenedOnlyOnceSeen() {
        #expect(!BluetoothSetupView.domainNeedsConfirm("pit.off-the.cloud"))
        #expect(!BluetoothSetupView.domainNeedsConfirm("Pit.Off-The.Cloud"))
        // No bridge: otc.local at home.
        #expect(!BluetoothSetupView.domainNeedsConfirm(""))
        // A look-alike, a deeper name, a bridge of one's own.
        #expect(BluetoothSetupView.domainNeedsConfirm("pit.off-the.cloud.example.net"))
        #expect(BluetoothSetupView.domainNeedsConfirm("a.pit.off-the.cloud"))
        #expect(BluetoothSetupView.domainNeedsConfirm("off-the.cloud"))
        #expect(BluetoothSetupView.domainNeedsConfirm("photos.example.net"))
        #expect(BluetoothSetupView.domainNeedsConfirm("pit-off-the.cloud"))
        #expect(BluetoothSetupView.domainNeedsConfirm("-pit.off-the.cloud"))
        #expect(BluetoothSetupView.endpoint(forDomain: "pit.off-the.cloud") == "wss://pit.off-the.cloud/ws")
        #expect(BluetoothSetupView.endpoint(forDomain: "Pit.Off-The.Cloud") == "wss://pit.off-the.cloud/ws")
        #expect(S.appShellSetupOtherDomainText(domain: "photos.example.net") == "This device says its address is \u{2068}photos.example.net\u{2069}, which isn't an off-the.cloud address. Open it only if you set the device up with a bridge of your own: if not, it may not be your device, and your photos would go there.")
    }

    /// The pending setup, saved from what the wizard's page said before any
    /// question, is asked about at Onboarding's Save & Continue as "Open my
    /// device" asks: it signed in to a look-alike unasked.
    @Test func aPendingSetupAtAnotherDomainIsAskedAbout() {
        let pending = { (d: String) in BluetoothSetupView.setupAddressToConfirm(BluetoothSetupView.endpoint(forDomain: d)) }
        #expect(pending("pit.off-the.cloud") == nil)
        #expect(pending("Pit.Off-The.Cloud") == nil)
        #expect(pending("") == nil)
        #expect(pending("pit.off-the.cloud.example.net") == "pit.off-the.cloud.example.net")
        #expect(pending("a.pit.off-the.cloud") == "a.pit.off-the.cloud")
        #expect(pending("photos.example.net") == "photos.example.net")
        // A bridge name at another port or scheme isn't the bridge's own.
        #expect(BluetoothSetupView.setupAddressToConfirm("wss://pit.off-the.cloud:8443/ws") == "pit.off-the.cloud")
        #expect(BluetoothSetupView.setupAddressToConfirm("ws://pit.off-the.cloud/ws") == "pit.off-the.cloud")
    }

    @Test func nothingFoundForHalfAMinuteSaysWhatToCheck() {
        #expect(BluetoothSetupView.notFoundSeconds == 30)
        #expect(S.appShellSetupNotFound == "No device found yet. Check that it's on, with its disks connected, and keep the phone next to it.")
        #expect(S.iosOpenSettings == "Open Settings")
    }
}
