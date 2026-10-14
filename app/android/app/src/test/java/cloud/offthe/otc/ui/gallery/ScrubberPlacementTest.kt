// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

// Issue #198: the date scrubber at the screen's edge beside a camera cutout
// on a phone turned on its side (ScrubberPlacement, PhotoGalleryView.kt; the
// iOS ScrubberPlacementTests).
class ScrubberPlacementTest {
    @Test fun uprightPhoneTabletAndTheOtherSideAreAsBefore() {
        // Held upright the grid reaches the screen's side; on its side the
        // other way round the cutout is at the start, not beside the
        // scrubber; a tablet has no cutout band: nothing moves.
        assertEquals(ScrubberPlacement(), ScrubberPlacement.of(endGap = 0f, cutoutEnd = 0f, barsEnd = 0f, topGap = 120f, bottomGap = 0f))
        assertEquals(ScrubberPlacement(), ScrubberPlacement.of(endGap = 0f, cutoutEnd = 0f, barsEnd = 0f, topGap = 88f, bottomGap = 25f))
        assertEquals(ScrubberPlacement(), ScrubberPlacement.of(endGap = 0.5f, cutoutEnd = 0.5f, barsEnd = 0f, topGap = 88f, bottomGap = 25f))
    }

    @Test fun besideTheCutoutItReachesTheScreensEdge() {
        // A Pixel 8 on its side with its camera at the right: a 52dp band,
        // the grid under the 88dp top bar and over the 25dp gesture bar,
        // corners 39dp round.
        val p = ScrubberPlacement.of(endGap = 52f, cutoutEnd = 52f, barsEnd = 0f, topGap = 88f, bottomGap = 25f, clearance = 39f)
        assertEquals(52f, p.reach)
        assertEquals(0f, p.top)
        // The bottom end comes up to the corner's radius from the screen's bottom.
        assertEquals(14f, p.bottom)
        // Without the radius (before Android 12), iOS's clearance.
        assertEquals(39f, ScrubberPlacement.of(endGap = 52f, cutoutEnd = 52f, barsEnd = 0f, topGap = 88f, bottomGap = 25f).bottom)
    }

    @Test fun neverUnderANavigationBar() {
        // Three-button navigation on the cutout's side: only the cutout's
        // band past the bar is crossed, up to the bar.
        assertEquals(4f, ScrubberPlacement.of(endGap = 52f, cutoutEnd = 52f, barsEnd = 48f, topGap = 88f, bottomGap = 0f).reach)
        // A bar as wide as the band or wider: nothing.
        assertEquals(ScrubberPlacement(), ScrubberPlacement.of(endGap = 48f, cutoutEnd = 30f, barsEnd = 48f, topGap = 88f, bottomGap = 0f))
        // The bar alone (no cutout on that side): nothing.
        assertEquals(ScrubberPlacement(), ScrubberPlacement.of(endGap = 48f, cutoutEnd = 0f, barsEnd = 48f, topGap = 88f, bottomGap = 0f))
    }

    @Test fun onlyTheBandBesideTheGrid() {
        // The grid closer to the edge than the cutout's inset (it is laid out
        // past it): no further than the screen's edge.
        assertEquals(20f, ScrubberPlacement.of(endGap = 20f, cutoutEnd = 52f, barsEnd = 0f, topGap = 88f, bottomGap = 25f).reach)
    }

    @Test fun aNotchAtTheEdgeIsNeverReachedUnder() {
        // A Pixel-8-sized screen on its side (411dp tall), the grid between
        // the 88dp top bar and the 25dp gesture bar, a 52dp band beside the
        // cutout. A notch (teardrop, V, wide) runs from the very edge 52dp in,
        // across the middle of the track: the scrubber stays on the grid.
        val notch = ScrubberPlacement.Cutout(near = 0f, far = 52f, top = 170f, bottom = 241f)
        assertEquals(ScrubberPlacement(), place(cutouts = listOf(notch)))
        // A shallower one in a wider band: only to the notch's inner side, its
        // handle and grab strip clear of it there - and that far in from the
        // side the corners don't shorten the track.
        val shallow = ScrubberPlacement.Cutout(near = 0f, far = 30f, top = 170f, bottom = 241f)
        val p = place(cutouts = listOf(shallow))
        assertEquals(22f, p.reach)
        assertEquals(0f, p.top)
        assertEquals(0f, p.bottom)
        // Its outer edge, 52 - 22 = 30dp in from the screen's: the strip from
        // there inwards misses the notch.
        val outer = 52f - p.reach
        assertTrue(outer >= shallow.far)
        // Within the handle's width of the edge counts as at it.
        assertEquals(22f, place(cutouts = listOf(shallow.copy(near = 6f))).reach)
        // Two pieces one past the other: past both.
        assertEquals(12f, place(cutouts = listOf(shallow, ScrubberPlacement.Cutout(near = 32f, far = 40f, top = 100f, bottom = 120f))).reach)
    }

    @Test fun aHoleClearOfTheEdgeIsPassedOutside() {
        // A punch hole 12dp in from the edge: the 10dp handle at the edge
        // passes outside it (as an iPhone's beside the Dynamic Island), so the
        // scrubber reaches the screen's edge as with no cutout.
        val hole = ScrubberPlacement.Cutout(near = 12f, far = 46f, top = 188f, bottom = 222f)
        assertEquals(place(), place(cutouts = listOf(hole)))
        assertEquals(52f, place(cutouts = listOf(hole)).reach)
    }

    @Test fun onlyCutoutsAlongTheTrackCount() {
        // This emulator's punch hole sits in a corner: on its side, 0-52dp
        // down from the top, above the grid (88dp down) and the handle's
        // half past the track's end - nothing to keep clear of.
        val corner = ScrubberPlacement.Cutout(near = 0f, far = 52f, top = 0f, bottom = 52f)
        assertEquals(place(), place(cutouts = listOf(corner)))
        // One reaching the handle's half past the track's end does count.
        assertEquals(ScrubberPlacement(), place(cutouts = listOf(corner.copy(bottom = 84f))))
        // And one at the bottom, below the grid, not.
        assertEquals(place(), place(cutouts = listOf(corner.copy(top = 392f, bottom = 411f))))
        // Held upright there is no band to cross, whatever the cutout.
        assertEquals(ScrubberPlacement(), ScrubberPlacement.of(endGap = 0f, cutoutEnd = 0f, barsEnd = 0f, topGap = 120f, bottomGap = 0f, screenHeight = 915f, cutouts = listOf(corner)))
    }

    private fun place(cutouts: List<ScrubberPlacement.Cutout> = emptyList()) = ScrubberPlacement.of(
        endGap = 52f, cutoutEnd = 52f, barsEnd = 0f, topGap = 88f, bottomGap = 25f, clearance = 39f, screenHeight = 411f, cutouts = cutouts,
    )

    @Test fun endsKeepClearOfTheCorners() {
        // A grid reaching the very top or bottom: the track ends the whole
        // clearance in; one already far enough from them is left alone.
        val p = ScrubberPlacement.of(endGap = 52f, cutoutEnd = 52f, barsEnd = 0f, topGap = 0f, bottomGap = 0f, clearance = 39f)
        assertEquals(39f, p.top)
        assertEquals(39f, p.bottom)
        val q = ScrubberPlacement.of(endGap = 52f, cutoutEnd = 52f, barsEnd = 0f, topGap = 140f, bottomGap = 80f, clearance = 39f)
        assertEquals(0f, q.top)
        assertEquals(0f, q.bottom)
        // Square corners: nothing to clear.
        assertEquals(0f, ScrubberPlacement.of(endGap = 52f, cutoutEnd = 52f, barsEnd = 0f, topGap = 0f, bottomGap = 0f, clearance = 0f).bottom)
        // A grid reported above the screen's top (mid-rotation) is taken as at it.
        assertEquals(39f, ScrubberPlacement.of(endGap = 52f, cutoutEnd = 52f, barsEnd = 0f, topGap = -30f, bottomGap = 25f, clearance = 39f).top)
    }
}
