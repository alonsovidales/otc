// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.PhotoRating
import cloud.offthe.otc.proto.RatingFilter
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SearchPhotos
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// A collection's Rating (Rating.kt): the Show menu's filters, the stars a
// tap leaves, the comment's limits, when the grid is ranked, and what the
// device's answers mean. The iPhone app's RatingTests.swift has the same cases.
class RatingTest {
    private val rated = ImageGroup.newBuilder().setId("g").setName("Wedding").setRating(true).build()
    private val plain = ImageGroup.newBuilder().setId("h").setName("Trip").build()

    @Test fun showMapsToTheDevicesFilter() {
        assertNull(RatingShow.ALL.filter)
        assertEquals(RatingFilter.newBuilder().setMinStars(5).build(), RatingShow.FIVE.filter)
        assertEquals(4, RatingShow.FOUR.filter!!.minStars)
        assertEquals(3, RatingShow.THREE.filter!!.minStars)
        assertEquals(2, RatingShow.TWO.filter!!.minStars)
        // Rated is one star or more.
        assertEquals(RatingFilter.newBuilder().setMinStars(1).build(), RatingShow.RATED.filter)
        assertEquals(RatingFilter.newBuilder().setUnrated(true).build(), RatingShow.UNRATED.filter)
        assertEquals(RatingFilter.newBuilder().setWithComment(true).build(), RatingShow.COMMENTED.filter)
        assertEquals(8, RatingShow.entries.size)
    }

    @Test fun theMenusSayTheirChoices() {
        assertEquals(
            listOf("All photos", "5 stars", "4 stars or more", "3 stars or more", "2 stars or more", "Rated", "Not rated", "With a comment"),
            RatingShow.entries.map { it.label.english() },
        )
        assertEquals(listOf("Date", "Rating"), RatingSort.entries.map { it.label.english() })
    }

    @Test fun aTapGivesThatManyStarsOrTakesThemOff() {
        assertEquals(4, RatingRules.stars(0, 4))
        assertEquals(2, RatingRules.stars(4, 2))
        assertEquals(5, RatingRules.stars(3, 5))
        // The photo's own number again: none.
        assertEquals(0, RatingRules.stars(3, 3))
        assertEquals(0, RatingRules.stars(1, 1))
    }

    @Test fun aCommentIsTrimmedAndKeptToTwoThousandCharacters() {
        assertEquals("Lighter, please", RatingRules.comment("  Lighter, please \n"))
        assertEquals("", RatingRules.comment("   "))
        val long = "a".repeat(2500)
        assertEquals(2000, RatingRules.comment(long).length)
        assertEquals(2000, RatingRules.fit(long).length)
        // Characters, not UTF-16 units: an emoji is never cut in two.
        val emoji = "😀".repeat(2001)
        val kept = RatingRules.fit(emoji)
        assertEquals(2000, kept.codePointCount(0, kept.length))
        assertEquals(4000, kept.length)
        // A short text stays as typed (the box keeps its spaces until it is sent).
        assertEquals(" hi ", RatingRules.fit(" hi "))
    }

    @Test fun saveIsForAChangedComment() {
        assertFalse(RatingRules.changed("", ""))
        assertFalse(RatingRules.changed("", "   "))
        assertFalse(RatingRules.changed("Crop it", " Crop it "))
        assertTrue(RatingRules.changed("Crop it", "Crop it more"))
        assertTrue(RatingRules.changed("Crop it", ""))
    }

    @Test fun onlyARatedCollectionSortedOrFilteredByStarsIsRanked() {
        assertFalse(RatingRules.ranked(rated, RatingSort.DATE, RatingShow.ALL))
        assertTrue(RatingRules.ranked(rated, RatingSort.RATING, RatingShow.ALL))
        assertTrue(RatingRules.ranked(rated, RatingSort.DATE, RatingShow.UNRATED))
        // Rating off, or no collection: never.
        assertFalse(RatingRules.ranked(plain, RatingSort.RATING, RatingShow.FIVE))
        assertFalse(RatingRules.ranked(null, RatingSort.RATING, RatingShow.FIVE))
        // The grid follows: no month titles, no scrubber.
        val st = PhotoGalleryViewModel.State(activeGroup = rated, ratingSort = RatingSort.RATING)
        assertFalse(st.dateOrdered)
        assertNull(st.bucketsKeyNow)
        assertTrue(PhotoGalleryViewModel.State(activeGroup = rated).dateOrdered)
        assertTrue(PhotoGalleryViewModel.State(activeGroup = plain, ratingShow = RatingShow.FIVE).dateOrdered)
    }

    @Test fun theSearchAsksForTheStarsOnlyInARatedCollection() {
        fun asked(g: ImageGroup?, sort: RatingSort, show: RatingShow) = SearchPhotos.newBuilder().also { RatingRules.ask(it, g, sort, show) }.build()
        val none = asked(rated, RatingSort.DATE, RatingShow.ALL)
        assertFalse(none.hasRating())
        assertFalse(none.byRating)
        val both = asked(rated, RatingSort.RATING, RatingShow.THREE)
        assertEquals(3, both.rating.minStars)
        assertTrue(both.byRating)
        val off = asked(plain, RatingSort.RATING, RatingShow.THREE)
        assertFalse(off.hasRating())
        assertFalse(off.byRating)
    }

    @Test fun theRequestCarriesBothStarsAndTheTrimmedComment() {
        val r = RatingRules.request("g", "/Photos/a.jpg", 4, "  more light ")
        assertEquals("g", r.groupId)
        assertEquals("/Photos/a.jpg", r.path)
        assertEquals(4, r.stars)
        assertEquals("more light", r.comment)
        // Clearing: no stars, no comment.
        val clear = RatingRules.request("g", "a", 0, " ")
        assertEquals(0, clear.stars)
        assertEquals("", clear.comment)
    }

    @Test fun aTileShowsOnlyAPhotoWithStarsOrAComment() {
        assertFalse(RatingRules.shows(null))
        assertFalse(RatingRules.shows(PhotoRating.getDefaultInstance()))
        assertTrue(RatingRules.shows(PhotoRating.newBuilder().setStars(1).build()))
        assertTrue(RatingRules.shows(PhotoRating.newBuilder().setComment("x").build()))
        assertEquals(listOf("4 stars", "Comment"), ratingLabels(PhotoRating.newBuilder().setStars(4).setComment("x").build()).map { it.english() })
        assertEquals(listOf("1 star"), ratingLabels(PhotoRating.newBuilder().setStars(1).build()).map { it.english() })
        assertTrue(ratingLabels(null).isEmpty())
    }

    @Test fun theDevicesAnswers() {
        val stored = PhotoRating.newBuilder().setStars(3).setComment("ok").build()
        val kept = ratingAnswer(RespEnvelope.newBuilder().setRespPhotoRating(stored).build(), photo = true)
        assertEquals(RatingAnswer.Kept(stored), kept)
        val ack = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
        assertEquals(RatingAnswer.Kept(null), ratingAnswer(ack, photo = false))
        val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
        assertEquals(RatingAnswer.NeedsUpdate, ratingAnswer(unknown, photo = true))
        assertEquals(RatingAnswer.NeedsUpdate, ratingAnswer(unknown, photo = false))
        val off = RespEnvelope.newBuilder().setError(true).setErrorCode("rating_off").setErrorMessage("Rating is off for this collection.").build()
        assertEquals("Rating is off for this collection.", (ratingAnswer(off, photo = true) as RatingAnswer.Off).words.english())
        val demo = RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo.").build()
        assertEquals("This is a demo.", (ratingAnswer(demo, photo = true) as RatingAnswer.Refused).words.english())
        // No words, or no answer: ours.
        assertEquals("Couldn't save the rating. Try again.", (ratingAnswer(null, photo = true) as RatingAnswer.Refused).words.english())
        val refusedAck = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(false)).build()
        assertEquals("Couldn't change Rating. Try again.", (ratingAnswer(refusedAck, photo = false) as RatingAnswer.Refused).words.english())
        // An Ack is no answer to a photo's rating.
        assertEquals("Couldn't save the rating. Try again.", (ratingAnswer(ack, photo = true) as RatingAnswer.Refused).words.english())
    }
}
