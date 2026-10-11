// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosGridTilePhotoOn
import cloud.offthe.otc.i18n.appPhotosGridTileVideoOn
import cloud.offthe.otc.i18n.appPhotosMonthsDay
import cloud.offthe.otc.i18n.appPhotosMonthsRange
import cloud.offthe.otc.i18n.appPhotosMonthsRangeInYear
import cloud.offthe.otc.i18n.appPhotosMonthsShortTitle
import cloud.offthe.otc.i18n.appPhotosMonthsTitle
import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Test
import java.time.Instant
import java.time.ZoneId

// PhotoMonths.kt builds the keys that hold a month's name itself, with the
// name as a nested UiText (the accessors take Strings): each must put its
// arguments in the order its generated accessor does.
class PhotoTextTest {
    private val utc = ZoneId.of("UTC")
    private val names = listOf(
        "January", "February", "March", "April", "May", "June",
        "July", "August", "September", "October", "November", "December",
    )

    private fun buckets(vararg months: String) =
        months.mapIndexed { i, m -> PhotoGalleryViewModel.DateBucket(m, 1, i, i + 1) }

    @Test fun everyMonthHasItsNames() {
        for ((i, name) in names.withIndex()) {
            val key = "2024-%02d".format(i + 1)
            assertEquals(S.appPhotosMonthsTitle(name, "2024").english(), monthTitle(key).english())
            assertEquals(S.appPhotosMonthsShortTitle(name.take(3), "2024").english(), monthShort(key).english())
            assertEquals(S.appPhotosMonthsShortTitle(name.take(3), "2024").english(), scrubLabel(key).english())
        }
    }

    @Test fun theSpansArgumentsAreInTheAccessorsOrder() {
        assertEquals(S.appPhotosMonthsRangeInYear("Mar", "Oct", "2024").english(), spanLabel(buckets("2024-10", "2024-03")).english())
        assertEquals(
            S.appPhotosMonthsRange(S.appPhotosMonthsShortTitle("Mar", "2024").english(), S.appPhotosMonthsShortTitle("Oct", "2026").english()).english(),
            spanLabel(buckets("2026-10", "2024-03")).english(),
        )
    }

    @Test fun aTilesDayIsInTheAccessorsOrder() {
        val t = Instant.parse("2026-10-05T10:00:00Z").toEpochMilli()
        val day = S.appPhotosMonthsDay(5, "October", "2026").english()
        assertEquals(S.appPhotosGridTilePhotoOn(day).english(), tileLabel(false, t, utc).english())
        assertEquals(S.appPhotosGridTileVideoOn(day).english(), tileLabel(true, t, utc).english())
    }
}
