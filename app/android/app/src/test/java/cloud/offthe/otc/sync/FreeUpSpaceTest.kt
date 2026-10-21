// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appSettingsFreeUpDays
import cloud.offthe.otc.i18n.appSettingsFreeUpTitle
import cloud.offthe.otc.i18n.appSettingsFreeUpToggle
import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** Free up space: what may be moved to the trash, and when a run is due. iOS's FreeUpSpaceTests is the same. */
class FreeUpSpaceTest {
    private val now = 1_800_000_000_000L
    private val day = 24 * 3600_000L
    private val cutoff = now - 7 * day
    private fun row(daysAgo: Long, video: Boolean = false) = FreeUpSpace.Row(1, video, now - daysAgo * day, 100, 1)

    @Test fun onlyWhatIsOldEnough() {
        assertTrue(FreeUpSpace.eligible(row(8), cutoff, includeVideos = true))
        assertFalse(FreeUpSpace.eligible(row(6), cutoff, includeVideos = true))
        assertFalse(FreeUpSpace.eligible(row(7), cutoff, includeVideos = true))
        // No date at all: its age can't be told.
        assertFalse(FreeUpSpace.eligible(FreeUpSpace.Row(1, false, 0, 100, 1), cutoff, includeVideos = true))
    }

    /** Include videos off: the sync doesn't send them, so none goes. */
    @Test fun videosOnlyWhileSynced() {
        assertFalse(FreeUpSpace.eligible(row(30, video = true), cutoff, includeVideos = false))
        assertTrue(FreeUpSpace.eligible(row(30, video = true), cutoff, includeVideos = true))
    }

    @Test fun onceADay() {
        assertTrue(FreeUpSpace.isDue(0, now))
        assertFalse(FreeUpSpace.isDue(now - 3600_000L, now))
        assertTrue(FreeUpSpace.isDue(now - day, now))
        // Set while the clock was ahead.
        assertTrue(FreeUpSpace.isDue(now + 3600_000L, now))
    }

    @Test fun daysFallBackToSeven() {
        assertEquals(7, FreeUpSpace.days(0))
        assertEquals(30, FreeUpSpace.days(30))
        assertEquals(7, FreeUpSpace.days(5))
        assertTrue(FreeUpSpace.defaultDays in FreeUpSpace.dayChoices)
    }

    @Test fun englishText() {
        assertEquals("Free Up Space", S.appSettingsFreeUpTitle().english())
        assertEquals("Delete from this phone", S.appSettingsFreeUpToggle().english())
        assertEquals("1 day", S.appSettingsFreeUpDays(1).english())
        assertEquals("7 days", S.appSettingsFreeUpDays(7).english())
    }
}
