// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appShellSetupOtherDomainText
import cloud.offthe.otc.i18n.appShellSetupLeaveText
import cloud.offthe.otc.i18n.appShellSetupLeaveTitle
import cloud.offthe.otc.i18n.appShellSetupNotFound
import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// The Bluetooth setup (A11, A12): leaving asks first only while the device
// installs, and half a minute of looking with nothing found says what to
// check. As BluetoothSetupTests.swift.
class BluetoothSetupTest {
    @Test fun leavingAsksOnlyWhileTheDeviceInstalls() {
        // Before the install: nothing to lose but the page.
        assertFalse(setupLeaveAsks(installStarted = false, readyDomain = null))
        assertTrue(setupLeaveAsks(installStarted = true, readyDomain = null))
        // Online ("Open my device" shows): no install left to follow.
        assertFalse(setupLeaveAsks(installStarted = true, readyDomain = "pit.off-the.cloud"))
        assertFalse(setupLeaveAsks(installStarted = true, readyDomain = ""))
        assertEquals("Leave the setup?", S.appShellSetupLeaveTitle().english())
        assertEquals("The device goes on installing. Open the setup again to follow it, or sign in once it's done.", S.appShellSetupLeaveText().english())
    }

    /** An address the wizard reports that isn't an off-the.cloud name is opened only once the owner has seen it. */
    @Test fun anotherDomainIsOpenedOnlyOnceSeen() {
        assertFalse(domainNeedsConfirm("pit.off-the.cloud"))
        assertFalse(domainNeedsConfirm("Pit.Off-The.Cloud"))
        // No bridge: otc.local at home.
        assertFalse(domainNeedsConfirm(""))
        // A look-alike, a deeper name, a bridge of one's own.
        assertTrue(domainNeedsConfirm("pit.off-the.cloud.example.net"))
        assertTrue(domainNeedsConfirm("a.pit.off-the.cloud"))
        assertTrue(domainNeedsConfirm("off-the.cloud"))
        assertTrue(domainNeedsConfirm("photos.example.net"))
        assertTrue(domainNeedsConfirm("pit-off-the.cloud"))
        assertEquals("wss://pit.off-the.cloud/ws", endpointForDomain("pit.off-the.cloud"))
        assertEquals("wss://pit.off-the.cloud/ws", endpointForDomain("Pit.Off-The.Cloud"))
        assertEquals(
            "This device says its address is ⁨photos.example.net⁩, which isn't an off-the.cloud address. Open it only if you set the device up with a bridge of your own: if not, it may not be your device, and your photos would go there.",
            S.appShellSetupOtherDomainText("photos.example.net").english(),
        )
    }

    /**
     * The pending setup, saved from what the wizard's page said before any
     * question, is asked about at Onboarding's Save & Continue as "Open my
     * device" asks: it signed in to a look-alike unasked.
     */
    @Test fun aPendingSetupAtAnotherDomainIsAskedAbout() {
        // Saved as the setup saves them (endpointForDomain).
        assertNull(setupAddressToConfirm(endpointForDomain("pit.off-the.cloud")))
        assertNull(setupAddressToConfirm(endpointForDomain("Pit.Off-The.Cloud")))
        assertNull(setupAddressToConfirm(endpointForDomain("")))
        assertEquals("pit.off-the.cloud.example.net", setupAddressToConfirm(endpointForDomain("pit.off-the.cloud.example.net")))
        assertEquals("a.pit.off-the.cloud", setupAddressToConfirm(endpointForDomain("a.pit.off-the.cloud")))
        assertEquals("photos.example.net", setupAddressToConfirm(endpointForDomain("photos.example.net")))
        // A bridge name at another port or path isn't the bridge's own.
        assertEquals("pit.off-the.cloud", setupAddressToConfirm("wss://pit.off-the.cloud:8443/ws"))
        assertEquals("pit.off-the.cloud", setupAddressToConfirm("ws://pit.off-the.cloud/ws"))
        // Nothing readable: the address as it is.
        assertEquals("wss://bad host/ws", setupAddressToConfirm("wss://bad host/ws"))
    }

    @Test fun nothingFoundForHalfAMinuteSaysWhatToCheck() {
        assertEquals(30_000L, SETUP_NOT_FOUND_MS)
        assertEquals(
            "No device found yet. Check that it's on, with its disks connected, and keep the phone next to it.",
            S.appShellSetupNotFound().english(),
        )
    }
}
