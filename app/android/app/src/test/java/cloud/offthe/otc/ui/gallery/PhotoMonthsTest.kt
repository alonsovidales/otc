// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

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
        assertEquals("September 2024", monthTitle("2024-09"))
        assertEquals("Sep 2024", monthShort("2024-09"))
        assertEquals("1 item", countLabel(1, "item"))
        assertEquals("1,234 items", countLabel(1234, "item"))
        assertEquals("Ana", peopleTitle(listOf("Ana")))
        assertEquals("Ana & Leo", peopleTitle(listOf("Ana", "Leo")))
        assertEquals("Ana, Leo & 2 others", peopleTitle(listOf("Ana", "Leo", "Rosa", "Unnamed")))
        assertEquals("Ana, Leo & 1 other", peopleTitle(listOf("Ana", "Leo", "Rosa")))
    }

    @Test fun theSpanOfACollection() {
        assertEquals("", spanLabel(emptyList()))
        assertEquals("Mar 2024", spanLabel(buckets("2024-03" to 5)))
        assertEquals("Mar – Oct 2024", spanLabel(buckets("2024-10" to 1, "2024-03" to 5)))
        assertEquals("Mar 2024 – Oct 2026", spanLabel(buckets("2026-10" to 1, "2025-01" to 2, "2024-03" to 5)))
        assertEquals("2024 – 2026", spanLabel(buckets("2026-10" to 1, "2024-03" to 5), years = true))
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
        assertEquals("Mar 2019", scrubLabel("2019-03"))
        assertEquals("No date", scrubLabel("0000-00"))
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
        assertEquals("No date", scrubLabel("0000-00"))
        assertEquals(Instant.parse("1899-12-31T23:59:59.999Z").toEpochMilli(), jumpCutoffMs("0000-00", utc))
        assertEquals(Instant.parse("1899-12-31T22:59:59.999Z").toEpochMilli(), jumpCutoffMs("0000-00", ZoneId.of("+01:00")))
        assertTrue(jumpCutoffMs("0000-00", madrid)!! < jumpCutoffMs("1900-01", madrid)!!)
    }

    // ---- TalkBack ------------------------------------------------------------------

    @Test fun aTileReadsAsTheWebsLabel() {
        val t = Instant.parse("2026-10-05T10:00:00Z").toEpochMilli()
        assertEquals("Photo, 5 October 2026", tileLabel(false, t, utc))
        assertEquals("Video, 5 October 2026", tileLabel(true, t, utc))
        // The day in the phone's time zone: New Year's morning in UTC is New Year's Eve in New York.
        assertEquals("Photo, 1 January 2024", tileLabel(false, Instant.parse("2024-01-01T02:00:00Z").toEpochMilli(), utc))
        assertEquals("Photo, 31 December 2023", tileLabel(false, Instant.parse("2024-01-01T02:00:00Z").toEpochMilli(), newYork))
        assertEquals("Photo", tileLabel(false, null, utc))
        assertEquals("Video", tileLabel(true, null, utc))
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
}
