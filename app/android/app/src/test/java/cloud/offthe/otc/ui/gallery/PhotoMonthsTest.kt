// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import kotlinx.coroutines.runBlocking
import org.junit.Test
import java.time.Instant
import java.time.ZoneId

// Images by month (PhotoMonths.kt), as the web's PhotoGallery.tsx groups
// and lays them out.
class PhotoMonthsTest {
    private val utc = ZoneId.of("UTC")
    private val madrid = ZoneId.of("Europe/Madrid")
    private val newYork = ZoneId.of("America/New_York")

    private fun at(iso: String) = Instant.parse(iso).epochSecond

    // A phone: three across, 2dp between tiles and between months.
    private val phone = GridGeometry.fit(395f, 2f, 2f, 120f, fixedCols = 3)
    // A tablet's 600-1023dp: 120dp tiles, 4dp apart, 12dp between months.
    private val wide = GridGeometry(cols = 6, tile = 120f, gap = 4f, secGap = 12f)

    private fun bucket(month: String, count: Int, start: Int) = PhotoGalleryViewModel.DateBucket(month, count, start, start + count)
    private fun buckets(vararg mc: Pair<String, Int>): List<PhotoGalleryViewModel.DateBucket> {
        var at = 0
        return mc.map { (m, c) -> bucket(m, c, at).also { at += c } }
    }

    /** Each row as "title1|title2:start-end" or "tiles:start-end", for readable expectations. */
    private fun describe(l: GalleryLayout): List<String> = l.rows.map { r ->
        when (r) {
            is TitledRow -> r.segments.joinToString(" + ") { "${it.month ?: "-"}[${it.start}-${it.end}/${it.slots}]" }
            is TileRow -> "tiles[${r.start}-${r.end}]"
        }
    }

    private fun months(vararg counts: Pair<String?, Int>): List<String?> = counts.flatMap { (m, n) -> List(n) { m } }

    // ---- month keys -----------------------------------------------------------------

    @Test fun aPhotosMonthIsInThePhonesTimeZone() {
        // 23:30 on the last day of March in UTC is already April in Madrid.
        val t = at("2024-03-31T23:30:00Z")
        assertEquals("2024-03", monthOf(t, 0, utc))
        assertEquals("2024-04", monthOf(t, 0, madrid))
        assertEquals("2024-03", monthOf(t, 0, newYork))
        // And New Year's morning in UTC is still December in New York.
        assertEquals("2023-12", monthOf(at("2024-01-01T02:00:00Z"), 0, newYork))
        assertEquals("1970-01", monthOf(0, 0, utc))
    }

    @Test fun undatedPhotosStayWithTheMonthBeforeThem() {
        val runs = monthRuns(listOf("2024-03", null, "2024-03", "2024-02", null, null))
        assertEquals(listOf(MonthRun("2024-03", 0, 3), MonthRun("2024-02", 3, 6)), runs)
    }

    @Test fun undatedPhotosAtTheTopHaveAnEmptyTitle() {
        val runs = monthRuns(listOf(null, null, "2024-03", null, "2024-02"))
        assertEquals(listOf(MonthRun(null, 0, 2), MonthRun("2024-03", 2, 4), MonthRun("2024-02", 4, 5)), runs)
        val l = layoutGallery(runs, titled = true, g = phone)
        assertNull((l.rows.first() as TitledRow).segments.first().month)
    }

    @Test fun aMonthComingBackIsANewSection() {
        // The list's own order is kept, never re-sorted: the viewer swipes as the grid shows.
        assertEquals(3, monthRuns(listOf("2024-03", "2024-02", "2024-03")).size)
        assertEquals(emptyList<MonthRun>(), monthRuns(emptyList()))
    }

    // ---- the layout -----------------------------------------------------------------

    @Test fun onAPhoneShortMonthsShareARowOfThree() {
        val l = layoutGallery(monthRuns(months("2024-05" to 1, "2024-04" to 2, "2024-03" to 1, "2024-02" to 1, "2024-01" to 1)), true, phone)
        assertEquals(
            listOf("2024-05[0-1/1] + 2024-04[1-3/2]", "2024-03[3-4/1] + 2024-02[4-5/1] + 2024-01[5-6/1]"),
            describe(l),
        )
    }

    @Test fun twoMonthsOfTwoDontFitInThree() {
        val l = layoutGallery(monthRuns(months("2024-05" to 2, "2024-04" to 2)), true, phone)
        assertEquals(listOf("2024-05[0-2/2]", "2024-04[2-4/2]"), describe(l))
    }

    @Test fun aMonthOfARowOrMoreTakesTheWholeWidth() {
        val l = layoutGallery(monthRuns(months("2024-05" to 1, "2024-04" to 7, "2024-03" to 1)), true, phone)
        assertEquals(
            listOf("2024-05[0-1/1]", "2024-04[1-4/3]", "tiles[4-7]", "tiles[7-8]", "2024-03[8-9/1]"),
            describe(l),
        )
    }

    @Test fun onAWideWindowTheGapBetweenMonthsCounts() {
        // 3 + 3 tiles plus the 12dp between months is wider than a row of six.
        assertEquals(listOf("2024-05[0-3/3]", "2024-04[3-6/3]"), describe(layoutGallery(monthRuns(months("2024-05" to 3, "2024-04" to 3)), true, wide)))
        // 2 + 3 fits, with room to spare.
        assertEquals(listOf("2024-05[0-2/2] + 2024-04[2-5/3]"), describe(layoutGallery(monthRuns(months("2024-05" to 2, "2024-04" to 3)), true, wide)))
    }

    @Test fun theLastMonthTakesTheRoomItsBucketSays() {
        val runs = monthRuns(months("2024-05" to 1, "2024-04" to 1))
        // More pages to come and 2024-04 has 10 photos: it takes a whole row now.
        assertEquals(listOf("2024-05[0-1/1]", "2024-04[1-2/3]"), describe(layoutGallery(runs, true, phone, mapOf("2024-04" to 10), more = true)))
        // Two photos in all: still beside May, two tiles wide.
        assertEquals(listOf("2024-05[0-1/1] + 2024-04[1-2/2]"), describe(layoutGallery(runs, true, phone, mapOf("2024-04" to 2), more = true)))
        // The end reached: only what it has.
        assertEquals(listOf("2024-05[0-1/1] + 2024-04[1-2/1]"), describe(layoutGallery(runs, true, phone, mapOf("2024-04" to 10), more = false)))
    }

    @Test fun aTagSearchHasNoMonthTitles() {
        // Ordered by score, not date (State.dateOrdered is false): one block of full rows.
        val l = layoutGallery(listOf(MonthRun(null, 0, 8)), titled = false, g = phone)
        assertEquals(listOf("tiles[0-3]", "tiles[3-6]", "tiles[6-8]"), describe(l))
        assertTrue(l.rows.none { it is TitledRow })
        val st = PhotoGalleryViewModel.State(chips = listOf("dog"), dateBuckets = buckets("2024-03" to 4), bucketsKey = "|")
        assertFalse(st.dateOrdered)
        assertFalse(st.showScrubber)
    }

    @Test fun everyPhotoIsInOneRowInTheListsOrder() {
        val ms = months(null to 2, "2024-05" to 5, "2024-04" to 1, "2024-03" to 2, "2023-12" to 13, "2023-11" to 1)
        for (g in listOf(phone, wide)) {
            val l = layoutGallery(monthRuns(ms), true, g)
            assertEquals(ms.indices.toList(), l.rows.flatMap { (it.start until it.end).toList() })
            ms.indices.forEach { i -> val r = l.rows[l.rowOf(i)]; assertTrue(i in r.start until r.end) }
            assertEquals(-1, l.rowOf(ms.size))
        }
    }

    @Test fun theColumnsFitTheWidth() {
        val g = GridGeometry.fit(760f, 4f, 12f, 120f)
        assertEquals(6, g.cols)
        assertEquals(760f, g.width, 0.01f)
        assertEquals(3, phone.cols)
        assertEquals(395f, phone.width, 0.01f)
    }

    // ---- headers --------------------------------------------------------------------

    @Test fun wordsAsTheWebSaysThem() {
        assertEquals("September 2024", monthTitle("2024-09").english())
        assertEquals("Sep 2024", monthShort("2024-09").english())
        assertEquals("1 item", itemsLabel(1).english())
        assertEquals("1,234 items", itemsLabel(1234).english())
        assertEquals("Ana", peopleTitle(listOf("Ana")).english())
        assertEquals("\u2068Ana\u2069 & \u2068Leo\u2069", peopleTitle(listOf("Ana", "Leo")).english())
        assertEquals("\u2068Ana\u2069, \u2068Leo\u2069 & 2 others", peopleTitle(listOf("Ana", "Leo", "Rosa", "Unnamed")).english())
        assertEquals("\u2068Ana\u2069, \u2068Leo\u2069 & 1 other", peopleTitle(listOf("Ana", "Leo", "Rosa")).english())
    }

    @Test fun theSpanOfACollection() {
        assertEquals("", spanLabel(emptyList()).english())
        assertEquals("Mar 2024", spanLabel(buckets("2024-03" to 5)).english())
        assertEquals("Mar – Oct 2024", spanLabel(buckets("2024-10" to 1, "2024-03" to 5)).english())
        assertEquals("Mar 2024 – Oct 2026", spanLabel(buckets("2026-10" to 1, "2025-01" to 2, "2024-03" to 5)).english())
        assertEquals("2024 – 2026", spanLabel(buckets("2026-10" to 1, "2024-03" to 5), years = true).english())
    }

    @Test fun theCountsComeFromThisFiltersBuckets() {
        val list = buckets("2024-03" to 4, "2023-07" to 6)
        val st = PhotoGalleryViewModel.State(selectedPeople = listOf("p1"), dateBuckets = list, bucketsKey = PhotoGalleryViewModel.bucketsKeyOf(listOf("p1"), ""))
        assertTrue(st.bucketsFresh)
        assertEquals(10, st.totalPhotos)
        assertTrue(st.showScrubber)
        // Another person picked: the old counts are not this filter's.
        val other = st.copy(selectedPeople = listOf("p1", "p2"))
        assertFalse(other.bucketsFresh)
        assertEquals(0, other.totalPhotos)
        assertFalse(other.showScrubber)
        // A tag added keeps the people and collection's buckets, but a tag search has none.
        assertFalse(st.copy(chips = listOf("beach")).bucketsFresh)
    }

    // ---- the scrubber ---------------------------------------------------------------

    @Test fun theScrubbersMonthFollowsThePhotos() {
        val list = buckets("2024-03" to 10, "2023-07" to 30, "2019-03" to 60)
        assertNull(scrubBucketAt(emptyList(), 0.5f))
        assertEquals("2024-03", scrubBucketAt(list, 0f)?.month)
        assertEquals("2023-07", scrubBucketAt(list, 0.1f)?.month)
        assertEquals("2023-07", scrubBucketAt(list, 0.39f)?.month)
        assertEquals("2019-03", scrubBucketAt(list, 0.4f)?.month)
        assertEquals("2019-03", scrubBucketAt(list, 1f)?.month)
        assertEquals("Mar 2019", scrubLabel("2019-03").english())
        assertEquals("No date", scrubLabel("0000-00").english())
    }

    @Test fun aJumpLandsOnItsMonthsTitle() {
        // The grey tiles while it is on its way: its title, then rows.
        val grey = layoutGallery(listOf(MonthRun("2019-03", 0, 40)), true, phone)
        assertEquals(TitledRow(listOf(Segment("2019-03", 0, 3, 3))), grey.rows.first())
        assertEquals(1 + 13, grey.rows.size)
        // The photos it brings start at that month's newest: the first row is its title.
        val landed = layoutGallery(monthRuns(months("2019-03" to 12)), true, phone, mapOf("2019-03" to 60), more = true)
        assertEquals("2019-03", (landed.rows.first() as TitledRow).segments.single().month)
        assertEquals(0, landed.rowOf(0))
    }

    @Test fun aJumpSearchesFromTheLastInstantOfItsMonth() {
        assertEquals(Instant.parse("2024-02-29T23:59:59.999Z").toEpochMilli(), jumpCutoffMs("2024-02", utc))
        assertEquals(Instant.parse("2024-02-29T22:59:59.999Z").toEpochMilli(), jumpCutoffMs("2024-02", madrid))
        assertEquals(Instant.parse("2024-08-01T03:59:59.999Z").toEpochMilli(), jumpCutoffMs("2024-07", newYork))
        assertNull(jumpCutoffMs("bad", utc))
        assertNull(jumpCutoffMs("2024-13", utc))
    }

    @Test fun aJumpToNoDateFindsThePhotosWithoutOne() {
        // The device's bucket of zero dates: before every real date, as the
        // web's Date(0, 0, 0, 23, 59, 59, 999) - never no jump at all, which
        // left the scrubber's grey tiles standing.
        assertEquals("No date", scrubLabel("0000-00").english())
        assertEquals(Instant.parse("1899-12-31T23:59:59.999Z").toEpochMilli(), jumpCutoffMs("0000-00", utc))
        assertEquals(Instant.parse("1899-12-31T22:59:59.999Z").toEpochMilli(), jumpCutoffMs("0000-00", ZoneId.of("+01:00")))
        assertTrue(jumpCutoffMs("0000-00", madrid)!! < jumpCutoffMs("1900-01", madrid)!!)
    }

    // ---- TalkBack ------------------------------------------------------------------

    @Test fun aTileReadsAsTheWebsLabel() {
        val t = Instant.parse("2026-10-05T10:00:00Z").toEpochMilli()
        assertEquals("Photo, 5 October 2026", tileLabel(false, t, utc).english())
        assertEquals("Video, 5 October 2026", tileLabel(true, t, utc).english())
        // The day in the phone's time zone: New Year's morning in UTC is New Year's Eve in New York.
        assertEquals("Photo, 1 January 2024", tileLabel(false, Instant.parse("2024-01-01T02:00:00Z").toEpochMilli(), utc).english())
        assertEquals("Photo, 31 December 2023", tileLabel(false, Instant.parse("2024-01-01T02:00:00Z").toEpochMilli(), newYork).english())
        assertEquals("Photo", tileLabel(false, null, utc).english())
        assertEquals("Video", tileLabel(true, null, utc).english())
    }

    // ---- asking again -----------------------------------------------------------------

    @Test fun aFailedLoadIsAskedAgainWithABackoff() = runBlocking {
        val waits = mutableListOf<Long>()
        var tries = 0
        val ok = retryWithBackoff(sleep = { waits += it }) { ++tries == 7 }
        assertTrue(ok)
        assertEquals(7, tries)
        // 1 s doubling to 10 s, as the web's usePageRetry.
        assertEquals(listOf(1_000L, 2_000L, 4_000L, 8_000L, 10_000L, 10_000L), waits)
    }

    @Test fun aLoadNoLongerWantedIsNotAskedAgain() = runBlocking {
        var tries = 0
        var wanted = true
        val ok = retryWithBackoff(sleep = { if (tries == 2) wanted = false }, stillWanted = { wanted }) { tries++; false }
        assertFalse(ok)
        assertEquals(2, tries)
        // Done at once: never asked again.
        var once = 0
        assertTrue(retryWithBackoff(sleep = { error("no wait") }) { once++; true })
        assertEquals(1, once)
    }

    @Test fun aPageFromASearchStartedAgainIsNoticed() {
        val cutoff = Instant.parse("2019-03-31T23:59:59.999Z").toEpochMilli()
        val old = Instant.parse("2019-02-01T10:00:00Z").toEpochMilli()
        val newer = Instant.parse("2024-01-01T10:00:00Z").toEpochMilli()
        // A held token comes back as it went.
        assertFalse(lostCutoff(listOf(old to "/a"), "t1", "t1", cutoff, emptySet()))
        assertTrue(lostCutoff(listOf(old to "/a"), "t2", "t1", cutoff, emptySet()))
        // The last page has none: the photos tell.
        assertFalse(lostCutoff(listOf(old to "/a", null to "/b"), "", "t1", cutoff, setOf("/z")))
        assertTrue(lostCutoff(listOf(newer to "/a"), "", "t1", cutoff, emptySet()))
        assertTrue(lostCutoff(listOf(old to "/z"), "", "t1", cutoff, setOf("/z")))
    }

    // ---- the photo under a finger (issue #200) ----------------------------------

    // Three across, 100 px tiles 2 px apart: 304 px wide.
    private val px = GridGeometry(cols = 3, tile = 100f, gap = 2f, secGap = 2f)

    @Test fun aRowOfTilesFindsThePhotoUnderTheFinger() {
        val row = TileRow(6, 9)
        // 2 px of gap above it.
        assertEquals(6, row.itemAt(50f, 52f, px, top = 2f, title = 30f))
        assertEquals(7, row.itemAt(103f, 52f, px, top = 2f, title = 30f))
        assertEquals(8, row.itemAt(250f, 52f, px, top = 2f, title = 30f))
        // Between tiles, above them, below them: none (the drag keeps the last).
        assertNull(row.itemAt(101f, 52f, px, top = 2f, title = 30f))
        assertNull(row.itemAt(50f, 1f, px, top = 2f, title = 30f))
        assertNull(row.itemAt(50f, 102f, px, top = 2f, title = 30f))
        assertNull(row.itemAt(-1f, 52f, px, top = 2f, title = 30f))
        assertNull(row.itemAt(304f, 52f, px, top = 2f, title = 30f))
        // A last row short of tiles: its empty slots hold none.
        assertEquals(9, TileRow(9, 10).itemAt(50f, 52f, px, top = 2f, title = 30f))
        assertNull(TileRow(9, 10).itemAt(150f, 52f, px, top = 2f, title = 30f))
    }

    @Test fun monthsSideBySideFindThePhotoUnderTheirTitles() {
        // A one-photo month beside a two-photo one: 100 px, 2 px apart, 202 px.
        val row = TitledRow(listOf(Segment("2024-03", 0, 1, 1), Segment("2024-02", 1, 3, 2)))
        val y = 14f + 30f + 50f
        assertEquals(0, row.itemAt(50f, y, px, top = 14f, title = 30f))
        assertEquals(1, row.itemAt(103f, y, px, top = 14f, title = 30f))
        assertEquals(2, row.itemAt(300f, y, px, top = 14f, title = 30f))
        // Over a title, or between the months: none.
        assertNull(row.itemAt(50f, 14f + 10f, px, top = 14f, title = 30f))
        assertNull(row.itemAt(250f, 14f + 29f, px, top = 14f, title = 30f))
        assertNull(row.itemAt(101f, y, px, top = 14f, title = 30f))
    }

    @Test fun aMonthsFirstRowAndTheRoomKeptForItsNextPage() {
        // A month of a row or more: its title over a full row.
        val full = TitledRow(listOf(Segment("2024-01", 3, 6, 3)))
        assertEquals(5, full.itemAt(250f, 94f, px, top = 14f, title = 30f))
        // A last month keeping room for photos still to come: none in its empty slot.
        val room = TitledRow(listOf(Segment("2023-12", 0, 1, 2)))
        assertEquals(0, room.itemAt(50f, 94f, px, top = 14f, title = 30f))
        assertNull(room.itemAt(150f, 94f, px, top = 14f, title = 30f))
    }

    @Test fun aLaidOutGridFindsEachPhotoInDisplayOrder() {
        // The layout the grid draws, walked tile by tile: every photo once, in order.
        val l = layoutGallery(monthRuns(months("2024-03" to 1, "2024-02" to 2, "2024-01" to 4)), true, px)
        val found = ArrayList<Int>()
        for ((i, r) in l.rows.withIndex()) {
            val top = if (i == 0) 8f else if (r is TitledRow) 14f else 2f
            val y = top + (if (r is TitledRow) 30f else 0f) + 50f
            var x = 1f
            while (x < 304f) { r.itemAt(x, y, px, top, 30f)?.let { if (found.lastOrNull() != it) found += it }; x += 1f }
        }
        assertEquals((0 until 7).toList(), found)
    }

    // ---- issue #194: the room kept above a jump's photos ---------------------------

    /** Rows with their grey tiles: "month[start-end/slots]+lead" or "tiles[start-end]+lead". */
    private fun describeLead(l: GalleryLayout): List<String> = l.rows.map { r ->
        when (r) {
            is TitledRow -> r.segments.joinToString(" + ") { "${it.month ?: "-"}[${it.start}-${it.end}/${it.slots}]+${it.lead}" }
            is TileRow -> "tiles[${r.start}-${r.end}]+${r.lead}"
        }
    }

    @Test fun theRoomAboveIsTheFirstMonthsCountOnlyWhenItCameFromAbove() {
        val b = buckets("2024-07" to 2, "2024-06" to 7, "2024-05" to 4)
        // The jump's month (it came from the jump, complete at its top):
        // nothing above it - no grey room for the month newer than it.
        assertEquals(RoomAbove(null), roomAbove("2024-05", firstUp = false, b))
        // Photos of June came from above: June keeps the room its bucket says.
        assertEquals(RoomAbove(7), roomAbove("2024-06", firstUp = true, b))
        assertEquals(RoomAbove(2), roomAbove("2024-07", firstUp = true, b))
        // A month the buckets don't have, or no month: nothing.
        assertEquals(RoomAbove(null), roomAbove("2024-08", firstUp = true, b))
        assertEquals(RoomAbove(null), roomAbove(null, firstUp = true, b))
    }

    @Test fun aCompleteFirstMonthHasNothingAboveIt() {
        // The jump's May (4 photos, more to come below): its title first.
        val l = layoutGallery(monthRuns(months("2024-05" to 4)), true, phone, mapOf("2024-05" to 9), more = true, above = RoomAbove(null))
        assertEquals(listOf("2024-05[0-3/3]+0", "tiles[3-4]+0"), describeLead(l))
        assertEquals(0, l.rowOf(0))
        assertEquals("2024-05#0", l.keyOf(0))
    }

    @Test fun photosLandingFromAboveFillTheGreyTilesWithoutARowMoving() {
        val above = RoomAbove(7)
        // June's two oldest came first (oldest first, turned round), then three more.
        fun june(n: Int) = layoutGallery(monthRuns(months("2024-06" to n, "2024-05" to 4)), true, phone, mapOf("2024-05" to 4), above = above)
        val two = june(2)
        val five = june(5)
        assertEquals(listOf("2024-06[0-0/3]+3", "tiles[0-1]+2", "tiles[1-2]+0", "2024-05[2-5/3]+0", "tiles[5-6]+0"), describeLead(two))
        assertEquals(listOf("2024-06[0-1/3]+2", "tiles[1-4]+0", "tiles[4-5]+0", "2024-05[5-8/3]+0", "tiles[8-9]+0"), describeLead(five))
        // The same rows under the same keys: nothing on screen moves.
        assertEquals(two.rows.indices.map { two.keyOf(it) }, five.rows.indices.map { five.keyOf(it) })
        // June complete: nothing above it (July comes with the next page),
        // and its rows keep their keys.
        val seven = june(7)
        assertEquals(two.rows.indices.map { two.keyOf(it) }, seven.rows.indices.map { seven.keyOf(it) })
        assertEquals(
            listOf("2024-06[0-3/3]+0", "tiles[3-6]+0", "tiles[6-7]+0", "2024-05[7-10/3]+0", "tiles[10-11]+0"),
            describeLead(seven),
        )
    }

    @Test fun aMonthTheBucketsUndercountedLaysOutFromItsFirstPhoto() {
        // The bucket said 3, 5 came: no grey tiles, laid out as a month is.
        val l = layoutGallery(monthRuns(months("2024-06" to 5)), true, phone, above = RoomAbove(3))
        assertEquals(listOf("2024-06[0-3/3]+0", "tiles[3-5]+0"), describeLead(l))
    }

    @Test fun aSmallMonthFillingFromAboveSitsBesideTheNextOneAsAnyMonthDoes() {
        // June's bucket says 2, one came from above; May's 1 from the jump.
        val l = layoutGallery(monthRuns(months("2024-06" to 1, "2024-05" to 1)), true, phone, above = RoomAbove(2))
        assertEquals(listOf("2024-06[0-1/2]+1 + 2024-05[1-2/1]+0"), describeLead(l))
        assertEquals(0, l.rowOf(0))
    }

    @Test fun aGreyTileAboveIsNoPhotoUnderAFinger() {
        val px = GridGeometry(cols = 3, tile = 100f, gap = 2f, secGap = 2f)
        // One grey tile, then photos 0 and 1.
        val row = TileRow(0, 2, lead = 1)
        assertNull(row.itemAt(50f, 52f, px, top = 2f, title = 30f))
        assertEquals(0, row.itemAt(150f, 52f, px, top = 2f, title = 30f))
        assertEquals(1, row.itemAt(250f, 52f, px, top = 2f, title = 30f))
        val titled = TitledRow(listOf(Segment("2024-06", 0, 1, 3, lead = 2)))
        val y = 14f + 30f + 50f
        assertNull(titled.itemAt(50f, y, px, top = 14f, title = 30f))
        assertNull(titled.itemAt(150f, y, px, top = 14f, title = 30f))
        assertEquals(0, titled.itemAt(250f, y, px, top = 14f, title = 30f))
    }

    @Test fun aTagSearchLeavesItsRowsKeysToTheGrid() {
        val l = layoutGallery(listOf(MonthRun(null, 0, 5)), titled = false, g = phone)
        assertNull(l.keyOf(0))
        assertEquals(-1, l.rowWithKey("x"))
    }
}
