// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appShellSetupLeaveText
import cloud.offthe.otc.i18n.appShellSetupLeaveTitle
import cloud.offthe.otc.i18n.appShellSetupNotFound
import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
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

    @Test fun nothingFoundForHalfAMinuteSaysWhatToCheck() {
        assertEquals(30_000L, SETUP_NOT_FOUND_MS)
        assertEquals(
            "No device found yet. Check that it's on, with its disks connected, and keep the phone next to it.",
            S.appShellSetupNotFound().english(),
        )
    }
}
