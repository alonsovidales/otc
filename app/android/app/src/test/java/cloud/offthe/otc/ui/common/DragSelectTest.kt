// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.unit.IntSize
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Issue #200: the drag to select's pure part (DragSelect.kt), the same
// cases as iOS DragSelectTests.
class DragSelectTest {
    private val order = listOf("a", "b", "c", "d", "e", "f")

    @Test fun aLongPressAloneSelectsThePressedItem() {
        val s = DragSelection(emptyList(), "c")
        assertEquals(listOf("c"), s.apply(order, "c"))
    }

    @Test fun draggingForwardSelectsEveryItemFromThePressedOne() {
        val s = DragSelection(emptyList(), "b")
        assertEquals(listOf("b", "c", "d", "e"), s.apply(order, "e"))
    }

    @Test fun draggingBackwardSelectsInDisplayOrder() {
        val s = DragSelection(emptyList(), "e")
        assertEquals(listOf("b", "c", "d", "e"), s.range(order, "b"))
        assertEquals(listOf("b", "c", "d", "e"), s.apply(order, "b"))
    }

    @Test fun theRangeAddsToWhatWasSelected() {
        // "f" picked before, then a drag from "b" to "c".
        val s = DragSelection(listOf("f"), "b")
        assertEquals(listOf("f", "b", "c"), s.apply(order, "c"))
    }

    @Test fun shrinkingBackRestoresWhatTheRangePassedOver() {
        // "d" was selected before the drag; "c" wasn't.
        val s = DragSelection(listOf("d"), "a")
        assertEquals(listOf("d", "a", "b", "c", "e"), s.apply(order, "e"))
        // Back to "b": "c" and "e" go back to unselected, "d" stays selected.
        assertEquals(listOf("d", "a", "b"), s.apply(order, "b"))
        // Back to the start: only the pressed one joins.
        assertEquals(listOf("d", "a"), s.apply(order, "a"))
    }

    @Test fun crossingThePressedItemFlipsTheRange() {
        val s = DragSelection(emptyList(), "c")
        assertEquals(listOf("c", "d", "e"), s.apply(order, "e"))
        assertEquals(listOf("a", "b", "c"), s.apply(order, "a"))
    }

    @Test fun aLongPressOnASelectedItemKeepsItSelected() {
        // Never a removal: the range only adds (the owner's rule).
        val s = DragSelection(listOf("c", "e"), "c")
        assertEquals(listOf("c", "e"), s.apply(order, "c"))
        assertEquals(listOf("c", "e", "d"), s.apply(order, "e"))
    }

    @Test fun pagesLoadedDuringTheDragExtendTheRange() {
        val s = DragSelection(emptyList(), "e")
        assertEquals(listOf("e", "f"), s.apply(order, "f"))
        val more = order + listOf("g", "h")
        assertEquals(listOf("e", "f", "g", "h"), s.apply(more, "h"))
    }

    @Test fun thePressedItemVanishingEndsTheDragAsItStands() {
        val s = DragSelection(emptyList(), "c")
        assertEquals(listOf("c", "d", "e"), s.apply(order, "e"))
        assertFalse(s.over)
        // "c" is deleted elsewhere (a new listing): the drag is over, and
        // the selection stays as it is (null: no change).
        val without = listOf("a", "b", "d", "e", "f")
        assertNull(s.apply(without, "f"))
        assertTrue(s.over)
        // Later moves change nothing, even if it comes back.
        assertNull(s.apply(without, "b"))
        assertNull(s.apply(order, "a"))
        assertNull(s.range(order, "f"))
    }

    @Test fun theFingersItemVanishingKeepsTheSelectionUntilAnotherIsReached() {
        val s = DragSelection(emptyList(), "b")
        assertEquals(listOf("b", "c", "d"), s.apply(order, "d"))
        // "d", under the finger, is deleted elsewhere: no change, the drag goes on.
        val without = listOf("a", "b", "c", "e", "f")
        assertNull(s.apply(without, "d"))
        assertFalse(s.over)
        // The finger over another item: the range from the pressed one again.
        assertEquals(listOf("b", "c", "e"), s.apply(without, "e"))
        assertEquals(listOf("b", "c"), s.apply(without, "c"))
    }

    @Test fun theDragGoesOnUntilThePressedItemGoesForGood() {
        val s = DragSelection(emptyList(), "c")
        assertTrue(s.goesOn(order))
        // "e", not the pressed one, goes: it goes on.
        assertTrue(s.goesOn(listOf("a", "b", "c", "d", "f")))
        // "c" goes: over, and still over once it is back.
        assertFalse(s.goesOn(listOf("a", "b", "d")))
        assertTrue(s.over)
        assertFalse(s.goesOn(order))
        assertNull(s.apply(order, "d"))
    }

    @Test fun aPickGoneMidDragDoesNotComeBackWithTheRange() {
        // "x" and "y" picked before a drag from "b".
        val s = DragSelection(listOf("x", "y"), "b")
        assertEquals(listOf("x", "y", "b", "c"), s.apply(order, "c"))
        // "x" goes from the items (the view drops it from its selection too).
        s.forget { it == "x" }
        assertEquals(listOf("y"), s.base)
        assertEquals(listOf("y", "b", "c", "d"), s.apply(order, "d"))
        assertEquals(listOf("y", "b"), s.apply(order, "b"))
        // Nothing of the base gone: as it was.
        s.forget { it == "q" }
        assertEquals(listOf("y"), s.base)
    }

    @Test fun anEmptyListEndsTheDrag() {
        val s = DragSelection(listOf("x"), "c")
        assertNull(s.apply(emptyList(), "c"))
        assertTrue(s.over)
    }

    @Test fun filesParentRowIsLeftOutOfTheOrder() {
        // FilesExplorerViewModel passes the rows without "..": a range over it skips it.
        val rows = listOf("..", "Docs", "Trips", "a.jpg").filter { it != ".." }
        val s = DragSelection(emptyList(), "a.jpg")
        assertEquals(listOf("Docs", "Trips", "a.jpg"), s.apply(rows, "Docs"))
    }

    // ---- the gesture's state ----

    @Test fun theEdgesWaitForTheFingerToMoveOnFromThePress() {
        // A press held still in an edge band never scrolls: only a move
        // past the slop after it fired arms the edges.
        val st = DragSelectState<String>()
        st.slop = 10f
        st.hit = { "a" }
        st.begin("a", Offset(100f, 5f))
        assertFalse(st.armed)
        st.moveTo(Offset(106f, 12f))
        assertFalse(st.armed)
        st.moveTo(Offset(100f, 20f))
        assertTrue(st.armed)
        st.end()
    }

    @Test fun onceThePressedItemIsGoneMovesDoNothingUntilTheLift() {
        val st = DragSelectState<String>()
        val reached = mutableListOf<String>()
        var under = "b"
        st.hit = { under }
        // The view model says the drag is over when it reaches "c".
        st.onMove = { reached += it; it != "c" }
        st.begin("a", Offset.Zero)
        st.moveTo(Offset(0f, 10f))
        under = "c"
        st.moveTo(Offset(0f, 20f))
        assertTrue(st.over)
        under = "d"
        st.moveTo(Offset(0f, 30f))
        st.rehit()
        assertEquals(listOf("b", "c"), reached)
        // The finger is still down: the lift is the drag's, no tap.
        assertTrue(st.dragging)
        st.end()
        assertFalse(st.dragging)
        assertFalse(st.over)
    }

    @Test fun thePressedItemGoingEndsTheDragWithTheFingerOnTheSameItem() {
        // The finger stays over "b" while "a", the pressed one, goes (a
        // refresh): the next move ends the drag, without another item reached.
        val st = DragSelectState<String>()
        val sel = DragSelection(emptyList(), "a")
        var order = listOf("a", "b", "c")
        val reached = mutableListOf<String>()
        st.hit = { "b" }
        st.goesOn = { sel.goesOn(order) }
        st.onMove = { reached += it; sel.apply(order, it); !sel.over }
        st.begin("a", Offset.Zero)
        st.moveTo(Offset(0f, 10f))
        assertEquals(listOf("b"), reached)
        assertFalse(st.over)
        order = listOf("b", "c")
        st.moveTo(Offset(0f, 11f))
        assertTrue(st.over)
        // The edges stop with it: a scroll step asks first.
        assertFalse(st.goingOn())
        // "a" back before the lift: still over.
        order = listOf("a", "b", "c")
        st.moveTo(Offset(0f, 12f))
        st.rehit()
        assertTrue(st.over)
        assertEquals(listOf("b"), reached)
        // The lift is still the drag's, no tap.
        assertTrue(st.dragging)
        st.end()
        assertFalse(st.dragging)
    }

    @Test fun aNewListingEndsTheDragAtTheNextMoveOrStep() {
        // Files: a listing that lands drops the view model's drag, which
        // then says it doesn't go on - the finger still over the same row.
        val st = DragSelectState<String>()
        var listed = false
        val reached = mutableListOf<String>()
        st.hit = { "b" }
        st.goesOn = { !listed }
        st.onMove = { reached += it; !listed }
        st.begin("a", Offset.Zero)
        st.moveTo(Offset(0f, 10f))
        assertTrue(st.goingOn())
        listed = true
        // A scroll step (the finger held in an edge band) asks before scrolling.
        assertFalse(st.goingOn())
        assertTrue(st.over)
        st.moveTo(Offset(0f, 20f))
        assertEquals(listOf("b"), reached)
        assertTrue(st.dragging)
        st.end()
        assertFalse(st.over)
    }

    // ---- the edges ----

    @Test fun nothingScrollsAwayFromTheEdges() {
        assertEquals(0f, dragScrollSpeed(300f, 0f, 1000f, 100f, 1200f, 8f), 0f)
        assertEquals(0f, dragScrollSpeed(100f, 0f, 1000f, 100f, 1200f, 8f), 0f)
        assertEquals(0f, dragScrollSpeed(900f, 0f, 1000f, 100f, 1200f, 8f), 0f)
    }

    @Test fun speedGrowsTowardTheEdge() {
        assertEquals(-600f, dragScrollSpeed(50f, 0f, 1000f, 100f, 1200f, 8f), 0.01f)
        assertEquals(-1200f, dragScrollSpeed(0f, 0f, 1000f, 100f, 1200f, 8f), 0.01f)
        assertEquals(300f, dragScrollSpeed(925f, 0f, 1000f, 100f, 1200f, 8f), 0.01f)
        assertEquals(1200f, dragScrollSpeed(1000f, 0f, 1000f, 100f, 1200f, 8f), 0.01f)
    }

    @Test fun pastTheEdgeIsTheFastest() {
        // Above the list (over the search field), or over the selection bar below it.
        assertEquals(-1200f, dragScrollSpeed(-80f, 0f, 1000f, 100f, 1200f, 8f), 0.01f)
        assertEquals(1200f, dragScrollSpeed(1100f, 0f, 1000f, 100f, 1200f, 8f), 0.01f)
    }

    @Test fun theBottomBandStartsAboveWhatFloatsOverTheList() {
        // A selection bar 200 px tall over the bottom: the list's visible part ends at 800.
        assertEquals(0f, dragScrollSpeed(650f, 0f, 800f, 100f, 1200f, 8f), 0f)
        assertTrue(dragScrollSpeed(750f, 0f, 800f, 100f, 1200f, 8f) > 0f)
    }

    @Test fun aShortListHasAThirdOfItForEachBand() {
        // 120 px tall: bands of 40, not 100 (nor half of it).
        assertEquals(-1200f, dragScrollSpeed(0f, 0f, 120f, 100f, 1200f, 8f), 0.01f)
        assertEquals(-600f, dragScrollSpeed(20f, 0f, 120f, 100f, 1200f, 8f), 0.01f)
        assertEquals(1200f, dragScrollSpeed(120f, 0f, 120f, 100f, 1200f, 8f), 0.01f)
        assertEquals(600f, dragScrollSpeed(100f, 0f, 120f, 100f, 1200f, 8f), 0.01f)
        assertEquals(0f, dragScrollSpeed(45f, 0f, 120f, 100f, 1200f, 8f), 0f)
        assertEquals(0f, dragScrollSpeed(60f, 0f, 120f, 100f, 1200f, 8f), 0f)
        assertEquals(0f, dragScrollSpeed(75f, 0f, 120f, 100f, 1200f, 8f), 0f)
        assertEquals(0f, dragScrollSpeed(10f, 0f, 0f, 100f, 1200f, 8f), 0f)
    }

    @Test fun aBandUnderTheLeastDepthDoesNotScroll() {
        // 23 px tall: a third is under 8, so neither edge scrolls.
        assertEquals(0f, dragScrollSpeed(0f, 0f, 23f, 100f, 1200f, 8f), 0f)
        assertEquals(0f, dragScrollSpeed(23f, 0f, 23f, 100f, 1200f, 8f), 0f)
        assertEquals(0f, dragScrollSpeed(-50f, 0f, 23f, 100f, 1200f, 8f), 0f)
        // 24 px: bands of exactly 8 still do.
        assertEquals(-1200f, dragScrollSpeed(0f, 0f, 24f, 100f, 1200f, 8f), 0.01f)
        assertEquals(1200f, dragScrollSpeed(24f, 0f, 24f, 100f, 1200f, 8f), 0.01f)
    }

    @Test fun aFingerPastAnEdgePointsAtTheEdgeOfWhatIsShown() {
        val size = IntSize(1000, 2000)
        // Inside: where it is.
        assertEquals(Offset(300f, 900f), clampToShown(Offset(300f, 900f), size, 0f))
        // Above the list (the search field), below it (the Files bar), past its sides.
        assertEquals(Offset(300f, 0f), clampToShown(Offset(300f, -120f), size, 0f))
        assertEquals(Offset(300f, 1999f), clampToShown(Offset(300f, 2300f), size, 0f))
        assertEquals(Offset(0f, 900f), clampToShown(Offset(-5f, 900f), size, 0f))
        assertEquals(Offset(999f, 900f), clampToShown(Offset(1200f, 900f), size, 0f))
        // Over the gallery's floating bar (300 px): just above it.
        assertEquals(Offset(300f, 1699f), clampToShown(Offset(300f, 1850f), size, 300f))
        // Not laid out yet: as it is.
        assertEquals(Offset(5f, 5f), clampToShown(Offset(5f, 5f), IntSize.Zero, 0f))
    }
}
