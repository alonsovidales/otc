// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Test

// The feed's and Alerts' "3m ago" move on while they show: reckoned from
// the clock the screen passes, which ticks at each turn of the minute. As
// MinuteClockTests.swift.
class MinuteClockTest {
    private val t0 = 1_800_000_000_000L // a whole minute

    @Test fun theSameTimeReadsOlderAsTheClockMovesOn() {
        assertEquals("just now", relativeTimeText(t0, nowMs = t0 + 30_000).english())
        assertEquals("1m ago", relativeTimeText(t0, nowMs = t0 + 60_000).english())
        assertEquals("59m ago", relativeTimeText(t0, nowMs = t0 + 59 * 60_000L).english())
        assertEquals("1h ago", relativeTimeText(t0, nowMs = t0 + 60 * 60_000L).english())
        assertEquals("3d ago", relativeTimeText(t0, nowMs = t0 + 3 * 24 * 3_600_000L).english())
    }

    @Test fun theClockTicksAtTheTurnOfTheMinute() {
        assertEquals(60_000L, msToNextMinute(t0))
        assertEquals(30_000L, msToNextMinute(t0 + 30_000))
        assertEquals(1_000L, msToNextMinute(t0 + 59_000))
        // A hair before the turn: not twice in a row.
        assertEquals(1_000L, msToNextMinute(t0 + 59_999))
    }
}
