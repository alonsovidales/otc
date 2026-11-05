// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * The profile read again over the fields (ProfileFields.merge): what was
 * changed here and not saved stays. iOS's ProfileEditorTests, case for case.
 */
class ProfileEditorTest {
    private val old = byteArrayOf(1, 2, 3)
    private val picked = byteArrayOf(9, 9)

    /** P1: the first read fills every field. */
    @Test fun theFirstReadFillsEveryField() {
        val device = ProfileFields("Ana", "Hi", old)
        assertEquals(device, ProfileFields.merge(ProfileFields(), null, device))
    }

    /** P2: a photo picked and not saved stays when Settings reads again (shown again). */
    @Test fun aPickedPhotoStaysThroughAReadAgain() {
        val device = ProfileFields("Ana", "Hi", old)
        val current = ProfileFields("Ana", "Hi", picked)
        assertEquals(current, ProfileFields.merge(current, device, device))
    }

    /** P3: a name or text typed and not saved stays; the fields not changed take what the device says now. */
    @Test fun whatWasTypedStaysAndTheRestFollowsTheDevice() {
        val shown = ProfileFields("Ana", "Hi", old)
        val current = ProfileFields("Ana María", "Hi", old)
        val device = ProfileFields("Ana", "Hello", picked)
        assertEquals(ProfileFields("Ana María", "Hello", picked), ProfileFields.merge(current, shown, device))
    }

    /** P4: a photo taken away here (none) stays away until saved. */
    @Test fun aPhotoTakenAwayStaysAway() {
        val shown = ProfileFields("Ana", "", old)
        assertNull(ProfileFields.merge(ProfileFields("Ana", "", null), shown, shown).image)
    }

    /** P5: once saved, the next read takes the device's again (it may have made the picture smaller). */
    @Test fun afterASaveTheDeviceIsReadAgain() {
        val saved = ProfileFields("Ana", "Hi", picked)
        val device = ProfileFields("Ana", "Hi", byteArrayOf(7))
        assertEquals(device, ProfileFields.merge(saved, saved, device))
    }
}
