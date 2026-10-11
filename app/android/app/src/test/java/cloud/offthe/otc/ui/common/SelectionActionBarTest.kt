// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// The bar's labels share one size, at which no word breaks (issue #192's
// sixth item in Files made "Download" break as "Downloa" / "d").
class SelectionActionBarTest {
    // A stand-in for the text measurer: every character, the space too, is
    // 0.5em wide, and words wrap greedily onto lines of [width].
    private fun wordFits(width: Int) = { word: String, size: Float -> word.length * size * 0.5f <= width }
    private fun lines(width: Int) = { title: String, size: Float ->
        var lines = 1
        var used = 0f
        for (w in title.split(' ')) {
            val ww = w.length * size * 0.5f
            val space = if (used > 0f) size * 0.5f else 0f
            if (used > 0f && used + space + ww > width) { lines++; used = ww } else used += space + ww
        }
        lines
    }
    private fun size(titles: List<String>, width: Int, min: Float = 8f) =
        sharedLabelSize(titles, 11f, min, wordFits = wordFits(width), lines = lines(width))

    private val files = listOf("Upload", "Share", "Share as gallery", "Keep out of Images", "Download", "Delete")

    @Test fun roomEnoughKeepsTheStylesSize() {
        assertEquals(11f, size(files, 60))
    }

    @Test fun theWidestWordSetsTheSize() {
        // "Download", 8 characters, is 4em: in 42 px it fits at 10.5, not 11.
        val words = listOf("Share", "Download", "Delete")
        assertEquals(10.5f, size(words, 42))
        // Narrower: down in half steps to the largest that fits.
        assertEquals(9f, size(words, 37))
    }

    @Test fun everyLabelInTwoLinesAtMost() {
        // At 10.5 in 42 px every word fits, but "Keep out of Images" takes
        // three lines ("Keep out" / "of" / "Images") until 9, where "of
        // Images" is one line - and every label gets that size.
        assertEquals(9f, size(files, 42))
        // Four short words, one per line at 11 in 20 px: two to a line at 8.
        assertEquals(8f, size(listOf("aa bb cc dd"), 20, min = 6f))
    }

    @Test fun neverBelowTheSmallest() {
        // Nothing fits: the smallest size, words breaking as a last resort.
        assertEquals(8f, size(files, 10))
        // A smallest that isn't a whole number of steps below is reached too
        // (8sp at the default font size is 6.15 at 1.3).
        assertEquals(6.15f, size(listOf("Download"), 25, min = 6.15f), 0.001f)
    }

    @Test fun aThirdLineWhereTwoNeverFit() {
        // Six items at 360 dp in Dutch: "Buiten Afbeeldingen" is 9.5em, wider
        // than 60 px even at 8, so the label never takes two lines...
        val dutch = listOf("Buiten Afbeeldingen houden")
        assertEquals(8f, size(dutch, 60))
        assertFalse(labelsFit(dutch, 8f, 2, wordFits(60), lines(60)))
        // ...and the bar lets it take three: one word a line, from 10
        // ("Afbeeldingen", 6em, in 60 px).
        assertEquals(10f, sharedLabelSize(dutch, 11f, 8f, maxLines = 3, wordFits = wordFits(60), lines = lines(60)))
        assertTrue(labelsFit(dutch, 10f, 3, wordFits(60), lines(60)))
    }

    @Test fun aWordTooWideNeverFits() {
        // "Herunterladen", 6.5em: 52 px at 8 is more than a 50 px slot.
        assertFalse(labelsFit(listOf("Herunterladen"), 8f, 3, wordFits(50), lines(50)))
        assertTrue(labelsFit(listOf("Herunterladen"), 8f, 3, wordFits(52), lines(52)))
    }
}
