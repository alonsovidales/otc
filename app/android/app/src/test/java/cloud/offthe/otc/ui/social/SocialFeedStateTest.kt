// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.social

import cloud.offthe.otc.ui.common.LoadProblem
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Social's feed: what a load leaves it saying, and the next page asked once.
class SocialFeedStateTest {
    private val failedMore = SocialFeedViewModel.State(loading = false, hasLoadedOnce = true, moreFailed = true)

    @Test fun aRefreshThatWorkedTakesCouldntLoadMoreAway() {
        val after = failedMore.afterLoad(null)
        assertFalse(after.moreFailed)
        assertNull(after.problem)
        assertTrue(after.hasLoadedOnce)
    }

    @Test fun aRefreshThatFailedLeavesTheEndAsItWas() {
        val after = failedMore.afterLoad(LoadProblem.UNREACHABLE)
        assertTrue(after.moreFailed)
        assertEquals(LoadProblem.UNREACHABLE, after.problem)
        // The first load failing says why, and isn't "loaded".
        val first = SocialFeedViewModel.State().afterLoad(LoadProblem.OFFLINE)
        assertFalse(first.hasLoadedOnce)
        assertEquals(LoadProblem.OFFLINE, first.problem)
    }

    @Test fun theNextPageIsClaimedOnce() {
        // A Wake's retryMore and the last post's effect both asking: the
        // first claims it (and takes the failure line away), the second finds it taken.
        val claimed = failedMore.claimMore()
        assertNotNull(claimed)
        assertTrue(claimed!!.loadingMore)
        assertFalse(claimed.moreFailed)
        assertNull(claimed.claimMore())
        // Nor while the feed itself is loading.
        assertNull(SocialFeedViewModel.State(loading = true).claimMore())
    }
}
