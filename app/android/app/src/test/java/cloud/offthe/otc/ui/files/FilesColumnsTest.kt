// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appFilesColumnCreated
import cloud.offthe.otc.i18n.appFilesColumnModified
import cloud.offthe.otc.i18n.appFilesColumnName
import cloud.offthe.otc.i18n.appFilesColumnNarrower
import cloud.offthe.otc.i18n.appFilesColumnReset
import cloud.offthe.otc.i18n.appFilesColumnResize
import cloud.offthe.otc.i18n.appFilesColumnSize
import cloud.offthe.otc.i18n.appFilesColumnWider
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.ui.files.FilesSortKey.CREATED
import cloud.offthe.otc.ui.files.FilesSortKey.MODIFIED
import cloud.offthe.otc.ui.files.FilesSortKey.NAME
import cloud.offthe.otc.ui.files.FilesSortKey.SIZE
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test

// The widths of Files' list columns on a wide screen (FilesColumns.kt):
// the minimums, the columns hidden as the list narrows, a resize, a reset
// and the widths kept on the phone - the web's files-columns-test.mjs, in dp.
class FilesColumnsTest {
    private val c = FilesColumns

    // What a list in English measured: the cells' padding, "0000000000…",
    // "1023.0 MB" / "2 KB" and a date with and without its time.
    private val samples = FilesColumns.Samples(pad = 16f, name = 100f, size = 70f, sizeShort = 40f, date = 150f, dateShort = 80f)
    private val m = c.metrics(samples)
    private fun total(l: FilesColumns.Layout) = m.frame + l.shown.sumOf { l.width.getValue(it) }
    private fun widths(l: FilesColumns.Layout, vararg k: FilesSortKey) = k.map { l.width.getValue(it) }

    // ---- minimums and defaults ----

    @Test fun nameHasRoomForTenCharactersAndItsIcons() {
        assertEquals(100, m.nameText)
        assertEquals(36 + 100 + 94 + 2, m.nameMin)
        assertEquals(36, FilesColumns.NAME_LEAD)
        assertEquals(94, FilesColumns.NAME_TRAIL)
        assertEquals(52, m.frame)
        // The date columns show only while Name keeps more than its minimum.
        assertTrue(m.nameRoom > m.nameMin)
    }

    @Test fun theOthersFitALongValueByDefaultAndAShortOneAtTheirMinimum() {
        assertEquals(mapOf(SIZE to 90, CREATED to 170, MODIFIED to 170), m.def)
        assertEquals(mapOf(SIZE to 58, CREATED to 100, MODIFIED to 100), m.min)
        for (k in listOf(SIZE, CREATED, MODIFIED)) assertTrue(m.min.getValue(k) < m.def.getValue(k))
    }

    @Test fun measuredWidthsRoundUpNeverDown() {
        val up = c.metrics(samples.copy(name = 100.2f, date = 150.01f))
        assertEquals(101, up.nameText)
        assertEquals(171, up.def[CREATED])
    }

    // ---- the columns a width shows ----

    @Test fun wideAllFourNameTakingWhatTheOthersLeave() {
        val l = c.fit(1000, emptyMap(), m)
        assertEquals(listOf(NAME, SIZE, CREATED, MODIFIED), l.shown)
        assertEquals(listOf(90, 170, 170), widths(l, SIZE, CREATED, MODIFIED))
        assertEquals(1000 - 52 - 90 - 170 - 170, l.width[NAME])
        assertEquals(1000, total(l))
    }

    @Test fun narrowerCreatedGoesFirstThenModifiedSizeAlwaysStays() {
        val all = m.frame + m.nameRoom + 90 + 170 + 170
        assertEquals(listOf(NAME, SIZE, CREATED, MODIFIED), c.shown(all, m))
        assertEquals(listOf(NAME, SIZE, MODIFIED), c.shown(all - 1, m))
        val two = m.frame + m.nameRoom + 90 + 170
        assertEquals(listOf(NAME, SIZE, MODIFIED), c.shown(two, m))
        assertEquals(listOf(NAME, SIZE), c.shown(two - 1, m))
        assertEquals(listOf(NAME, SIZE), c.shown(100, m))
        val l = c.fit(two - 1, emptyMap(), m)
        assertEquals(listOf(SIZE), l.fixed)
        assertEquals(two - 1 - 52 - 90, l.width[NAME])
    }

    @Test fun theFoldsInnerScreenWithTheRail() {
        // An open Fold 7 upright, about 840dp, less the 80dp rail.
        val l = c.fit(760, emptyMap(), m)
        assertEquals(listOf(NAME, SIZE, MODIFIED), l.shown)
        assertEquals(760 - 52 - 90 - 170, l.width[NAME])
    }

    @Test fun whichColumnsShowDoesntHangOnTheWidthsGiven() {
        // A drag never hides one.
        val wide = c.fit(900, mapOf(CREATED to 600, MODIFIED to 600), m)
        assertEquals(c.fit(900, emptyMap(), m).shown, wide.shown)
    }

    // ---- Name's minimum is kept ----

    @Test fun columnsGivenMoreThanFitsGiveWayTheLastFirst() {
        val room = 830
        val l = c.fit(room, mapOf(SIZE to 150, CREATED to 400, MODIFIED to 300), m)
        assertEquals(listOf(NAME, SIZE, CREATED, MODIFIED), l.shown)
        assertEquals(m.nameMin, l.width[NAME])
        assertEquals(room, total(l))
        // Modified gave first, then Created; Size kept what it was given.
        assertEquals(m.min[MODIFIED], l.width[MODIFIED])
        assertEquals(150, l.width[SIZE])
        assertTrue(l.width.getValue(CREATED) in (m.min.getValue(CREATED) + 1) until 400)
    }

    @Test fun aListTooNarrowEvenForTheMinimums() {
        val l = c.fit(150, emptyMap(), m)
        assertEquals(listOf(NAME, SIZE), l.shown)
        assertEquals(m.min[SIZE], l.width[SIZE])
        assertEquals(150 - 52 - 58, l.width[NAME])
        // Never less than nothing.
        assertEquals(0, c.fit(20, emptyMap(), m).width[NAME])
    }

    @Test fun aWidthGivenBelowAMinimumIsTheMinimum() {
        assertEquals(m.min[SIZE], c.fit(1000, mapOf(SIZE to 10), m).width[SIZE])
    }

    // ---- a resize ----

    @Test fun nameWiderTakesFromSizeThenCreatedThenModified() {
        val l = c.fit(1000, emptyMap(), m)
        val a = c.resize(l, m, NAME, 20f)
        assertEquals(l.width.getValue(NAME) + 20, a.width[NAME])
        assertEquals(l.width.getValue(SIZE) - 20, a.width[SIZE])
        assertEquals(l.width[CREATED], a.width[CREATED])
        val far = c.resize(l, m, NAME, 5000f)
        assertEquals(listOf(58, 100, 100), widths(far, SIZE, CREATED, MODIFIED))
        // The list stays as wide as it was: nothing scrolls sideways.
        assertEquals(1000, total(far))
    }

    @Test fun narrowerGivesTheRoomToTheNextColumnAndStopsAtItsMinimum() {
        val l = c.fit(1000, emptyMap(), m)
        val a = c.resize(l, m, NAME, -30f)
        assertEquals(l.width.getValue(NAME) - 30, a.width[NAME])
        assertEquals(l.width.getValue(SIZE) + 30, a.width[SIZE])
        val far = c.resize(l, m, NAME, -5000f)
        assertEquals(m.nameMin, far.width[NAME])
        assertEquals(1000, total(far))
        val created = c.resize(l, m, CREATED, -5000f)
        assertEquals(m.min[CREATED], created.width[CREATED])
        assertEquals(l.width.getValue(MODIFIED) + (l.width.getValue(CREATED) - m.min.getValue(CREATED)), created.width[MODIFIED])
        // A column before it doesn't move.
        assertEquals(l.width[SIZE], created.width[SIZE])
    }

    @Test fun aResizeGoesFromWhereTheDragBeganSoGoingBackUndoesIt() {
        val l = c.fit(1000, emptyMap(), m)
        assertSame(l, c.resize(l, m, SIZE, 0f))
        // Under half a dp is nothing.
        assertEquals(l.width, c.resize(l, m, SIZE, 0.4f).width)
        assertTrue(c.resize(l, m, SIZE, 5000f).width.getValue(SIZE) > l.width.getValue(SIZE))
        assertEquals(l, c.resize(c.resize(l, m, SIZE, 0f), m, SIZE, 0f))
    }

    @Test fun theLastColumnShownHasNoEdgeNorDoesAHiddenOne() {
        val l = c.fit(1000, emptyMap(), m)
        assertSame(l, c.resize(l, m, MODIFIED, 40f))
        val narrow = c.fit(400, emptyMap(), m)
        assertEquals(listOf(NAME, SIZE), narrow.shown)
        assertSame(narrow, c.resize(narrow, m, CREATED, 40f))
        assertSame(narrow, c.resize(narrow, m, SIZE, 40f))
        // With Created hidden, Size's edge gives to and takes from Modified.
        val mid = c.fit(m.frame + m.nameRoom + 90 + 170, emptyMap(), m)
        assertEquals(listOf(NAME, SIZE, MODIFIED), mid.shown)
        val s = c.resize(mid, m, SIZE, 10f)
        assertEquals(mid.width.getValue(MODIFIED) - 10, s.width[MODIFIED])
    }

    @Test fun talkBacksStep() {
        assertEquals(16, FilesColumns.STEP)
    }

    @Test fun whatAHandleSaysItsWidthItsMinimumAndItsMost() {
        val l = c.fit(1000, emptyMap(), m)
        val v = c.edge(l, m, SIZE)
        assertEquals(l.width[SIZE], v.now)
        assertEquals(m.min[SIZE], v.min)
        assertEquals(l.width.getValue(SIZE) + (l.width.getValue(CREATED) - 100) + (l.width.getValue(MODIFIED) - 100), v.max)
        assertEquals(m.nameMin, c.edge(l, m, NAME).min)
        assertEquals(v.max, c.resize(l, m, SIZE, 10_000f).width[SIZE])
    }

    // ---- the widths kept ----

    @Test fun onlyTheColumnsAResizeChangedAreKept() {
        val l = c.fit(1000, mapOf(MODIFIED to 200), m)
        val a = c.resize(l, m, NAME, 20f)
        assertEquals(mapOf(MODIFIED to 200, SIZE to l.width.getValue(SIZE) - 20), c.widthsAfter(mapOf(MODIFIED to 200), l, a))
    }

    @Test fun aDoubleTapPutsThatColumnBackNamesPutsEveryColumnBack() {
        assertEquals(mapOf(CREATED to 120), c.reset(mapOf(SIZE to 150, CREATED to 120), SIZE))
        assertEquals(emptyMap<FilesSortKey, Int>(), c.reset(mapOf(SIZE to 150, CREATED to 120), NAME))
        assertEquals(emptyMap<FilesSortKey, Int>(), c.reset(emptyMap(), MODIFIED))
        assertEquals(c.fit(1000, emptyMap(), m), c.fit(1000, c.reset(mapOf(SIZE to 150), SIZE), m))
    }

    @Test fun keptAsTextAndReadBack() {
        assertNull(c.format(emptyMap()))
        assertEquals("size:150,modified:210", c.format(mapOf(MODIFIED to 210, SIZE to 150)))
        assertEquals(mapOf(SIZE to 150, MODIFIED to 210), c.parse("size:150,modified:210"))
        assertEquals(mapOf(SIZE to 150, MODIFIED to 210), c.parse(c.format(mapOf(SIZE to 150, MODIFIED to 210))))
    }

    @Test fun aKeptValueThatIsntASaneWidthIsLeftOut() {
        assertEquals(emptyMap<FilesSortKey, Int>(), c.parse(null))
        assertEquals(emptyMap<FilesSortKey, Int>(), c.parse(""))
        assertEquals(emptyMap<FilesSortKey, Int>(), c.parse("not widths"))
        assertEquals(emptyMap<FilesSortKey, Int>(), c.parse("{\"size\":120}"))
        assertEquals(emptyMap<FilesSortKey, Int>(), c.parse("size:-5,created:abc,modified:1000000000,name:300"))
        assertEquals(mapOf(SIZE to 121, MODIFIED to 180), c.parse("size:121,created:0,modified:180,size"))
    }

    @Test fun englishText() {
        assertEquals("Name", S.appFilesColumnName().english())
        assertEquals("Size", S.appFilesColumnSize().english())
        assertEquals("Created", S.appFilesColumnCreated().english())
        assertEquals("Modified", S.appFilesColumnModified().english())
        assertEquals("Resize the Size column", S.appFilesColumnResize("Size").english())
        assertEquals("Wider", S.appFilesColumnWider().english())
        assertEquals("Narrower", S.appFilesColumnNarrower().english())
        assertEquals("Reset width", S.appFilesColumnReset().english())
    }
}
