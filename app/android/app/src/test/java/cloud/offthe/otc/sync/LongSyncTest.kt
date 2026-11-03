// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** Issue #205: when a sync goes on as the foreground job, and what its notification's bar shows. As iOS's LongSyncTests. */
class LongSyncTest {
    private val mb = 1L shl 20

    @Test fun onlyOnScreenAndPastTheThreshold() {
        assertEquals(300 * mb, LongSync.THRESHOLD_BYTES)
        assertTrue(LongSync.wanted(visible = true, declined = false, queuedBytes = 300 * mb + 1))
        assertTrue(LongSync.wanted(visible = true, declined = false, queuedBytes = 3L shl 30))
        // At the threshold, or under it: the plain run, as always.
        assertFalse(LongSync.wanted(visible = true, declined = false, queuedBytes = 300 * mb))
        assertFalse(LongSync.wanted(visible = true, declined = false, queuedBytes = 0))
        // Off screen Android doesn't let it start; after its Cancel, not until the app is in front again.
        assertFalse(LongSync.wanted(visible = false, declined = false, queuedBytes = 3L shl 30))
        assertFalse(LongSync.wanted(visible = true, declined = true, queuedBytes = 3L shl 30))
    }

    /** Coming to the front again lifts a Cancel; staying in front doesn't. */
    @Test fun cancelHoldsUntilTheAppIsInFrontAgain() {
        PhotoSync.inFront = true
        LongSync.declined = true
        PhotoSync.inFront = true
        assertTrue(LongSync.declined)
        PhotoSync.inFront = false
        assertTrue(LongSync.declined)
        PhotoSync.inFront = true
        assertFalse(LongSync.declined)
        PhotoSync.inFront = false
    }

    @Test fun unitsCountAssetsAndWhatIsOnItsWay() {
        assertEquals(0L, BackupProgress.units(0, 4, emptyList()))
        assertEquals(250L, BackupProgress.units(2, 4, listOf(0.5)))
        // Three at once, one of them a long video half way.
        assertEquals(170L, BackupProgress.units(1, 10, listOf(0.5, 0.2, 0.0)))
        // Never outside the bar, whatever comes in.
        assertEquals(400L, BackupProgress.units(4, 4, listOf(1.0)))
        assertEquals(100L, BackupProgress.units(1, 4, listOf(-1.0)))
        assertEquals(0L, BackupProgress.units(0, 0, listOf(0.3)))
    }

    @Test fun progressFollowsTheAssets() {
        val p = BackupProgress()
        p.begin(3)
        assertEquals(BackupProgress.Snapshot(0, 3, 0), p.snapshot())
        p.sending(7, 2 shl 20, 8 shl 20)
        assertEquals(BackupProgress.Snapshot(0, 3, 25), p.snapshot())
        p.sending(7, 8 shl 20, 8 shl 20)
        p.done(7)
        p.done(8)
        assertEquals(BackupProgress.Snapshot(2, 3, 200), p.snapshot())
        // A size it doesn't know moves nothing.
        p.sending(9, 10, 0)
        assertEquals(200L, p.snapshot().units)
        // A new pass starts from nothing.
        p.begin(5)
        assertEquals(BackupProgress.Snapshot(0, 5, 0), p.snapshot())
    }
}
