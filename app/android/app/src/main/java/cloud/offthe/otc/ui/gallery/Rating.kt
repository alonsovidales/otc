// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import android.content.Context
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appPhotosRatingComment
import cloud.offthe.otc.i18n.appPhotosRatingShowFive
import cloud.offthe.otc.i18n.appPhotosRatingShowFour
import cloud.offthe.otc.i18n.appPhotosRatingShowThree
import cloud.offthe.otc.i18n.appPhotosRatingShowTwo
import cloud.offthe.otc.i18n.appPhotosRatingSaveFailed
import cloud.offthe.otc.i18n.appPhotosRatingShowAll
import cloud.offthe.otc.i18n.appPhotosRatingShowCommented
import cloud.offthe.otc.i18n.appPhotosRatingShowRated
import cloud.offthe.otc.i18n.appPhotosRatingShowUnrated
import cloud.offthe.otc.i18n.appPhotosRatingSortDate
import cloud.offthe.otc.i18n.appPhotosRatingSortRating
import cloud.offthe.otc.i18n.appPhotosRatingStars
import cloud.offthe.otc.i18n.appPhotosRatingToggleFailed
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.PhotoRating
import cloud.offthe.otc.proto.RatingFilter
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SearchPhotos
import cloud.offthe.otc.proto.SetPhotoRating
import cloud.offthe.otc.ui.settings.deviceOr

// A collection's Rating (release 163; owner, 2026-11-02): with it on, each
// photo of the collection gets 1 to 5 stars and a comment, kept on the device
// for the photo in that collection. The owner sees one rating a photo, the
// latest of anyone's (PhotoRating.from_link for one through a shared link);
// a shared link's visitors see and change only their link's own (release
// 169). The collection can be sorted and filtered by them. The same rules as
// iOS's (Rating.swift) and the web's - change them together. Tests:
// RatingTest.kt.

/** The collection's Show menu: which of its photos the grid shows (SearchPhotos.rating). */
enum class RatingShow {
    ALL, FIVE, FOUR, THREE, TWO, RATED, UNRATED, COMMENTED;

    val label: UiText get() = when (this) {
        ALL -> S.appPhotosRatingShowAll()
        FIVE -> S.appPhotosRatingShowFive()
        FOUR -> S.appPhotosRatingShowFour()
        THREE -> S.appPhotosRatingShowThree()
        TWO -> S.appPhotosRatingShowTwo()
        RATED -> S.appPhotosRatingShowRated()
        UNRATED -> S.appPhotosRatingShowUnrated()
        COMMENTED -> S.appPhotosRatingShowCommented()
    }

    /** What the device is asked for: null for All photos (no filter). */
    val filter: RatingFilter? get() = when (this) {
        ALL -> null
        FIVE -> RatingFilter.newBuilder().setMinStars(5).build()
        FOUR -> RatingFilter.newBuilder().setMinStars(4).build()
        THREE -> RatingFilter.newBuilder().setMinStars(3).build()
        TWO -> RatingFilter.newBuilder().setMinStars(2).build()
        RATED -> RatingFilter.newBuilder().setMinStars(1).build()
        UNRATED -> RatingFilter.newBuilder().setUnrated(true).build()
        COMMENTED -> RatingFilter.newBuilder().setWithComment(true).build()
    }
}

/** The collection's Sort by: the date taken (the default) or the stars (SearchPhotos.by_rating). */
enum class RatingSort {
    DATE, RATING;

    val label: UiText get() = if (this == RATING) S.appPhotosRatingSortRating() else S.appPhotosRatingSortDate()
}

/** A comment being written in the viewer, for the photo at [path] (as typed: trimmed only when sent). */
data class RatingDraft(val path: String, val text: String)

object RatingRules {
    /** The most a comment may hold, in characters (code points; the device's runes). */
    const val COMMENT_MAX = 2000

    /** The stars a tap on the [tapped]th star leaves the photo with: that many, or none when it already had them. */
    fun stars(current: Int, tapped: Int): Int = if (tapped == current) 0 else tapped.coerceIn(1, 5)

    /** [text] as the comment box takes it: at most [COMMENT_MAX] characters, a character never cut in two. */
    fun fit(text: String): String {
        if (text.codePointCount(0, text.length) <= COMMENT_MAX) return text
        return text.substring(0, text.offsetByCodePoints(0, COMMENT_MAX))
    }

    /** [text] as it is sent and kept: trimmed, at most [COMMENT_MAX] characters. */
    fun comment(text: String): String = fit(text.trim()).trim()

    /** Whether the box's [draft] differs from the comment kept ([saved]): Save is enabled, and it is saved on leaving. */
    fun changed(saved: String, draft: String): Boolean = comment(draft) != saved

    /** The collection open in Images has Rating on: its photos are rated, sorted and filtered by it. */
    fun rates(group: ImageGroup?): Boolean = group?.rating == true

    /**
     * A sort or filter by the stars: the grid is ranked as a tag search's
     * is (no month titles, no date scrubber, pages by token only).
     */
    fun ranked(group: ImageGroup?, sort: RatingSort, show: RatingShow): Boolean =
        rates(group) && (sort == RatingSort.RATING || show != RatingShow.ALL)

    /** [sp] asks for the collection's photos as Sort by and Show say - only in a collection with Rating on. */
    fun ask(sp: SearchPhotos.Builder, group: ImageGroup?, sort: RatingSort, show: RatingShow) {
        if (!rates(group)) return
        show.filter?.let { sp.rating = it }
        if (sort == RatingSort.RATING) sp.byRating = true
    }

    /** SetPhotoRating for [path] in [groupId]: both the stars and the comment, replacing what it had (0 and "" clear it). */
    fun request(groupId: String, path: String, stars: Int, comment: String): SetPhotoRating =
        SetPhotoRating.newBuilder().setGroupId(groupId).setPath(path).setStars(stars.coerceIn(0, 5)).setComment(comment(comment)).build()

    /** Whether [r] says anything: a tile without stars or a comment shows nothing. */
    fun shows(r: PhotoRating?): Boolean = r != null && (r.stars > 0 || r.comment.isNotEmpty())

    /** The rating as shown at once for a change the owner made (then what the device stored). */
    fun local(stars: Int, comment: String): PhotoRating =
        PhotoRating.newBuilder().setStars(stars.coerceIn(0, 5)).setComment(comment(comment)).build()

    /** Whether [said] holds [stars] and [comment] (null: the photo has no rating). */
    fun holds(said: PhotoRating?, stars: Int, comment: String): Boolean =
        (said?.stars ?: 0) == stars && (said?.comment ?: "") == comment
}

/** What the device answered a SetPhotoRating (or SetImageGroupRating) with. */
sealed interface RatingAnswer {
    /** Kept: as the device stored it (SetPhotoRating), or the Ack (SetImageGroupRating: [rating] null). */
    data class Kept(val rating: PhotoRating?) : RatingAnswer
    /** A device from before Rating: it needs an update. */
    data object NeedsUpdate : RatingAnswer
    /** The collection's Rating is off (turned off elsewhere): the device's words, else ours. */
    data class Off(val words: UiText) : RatingAnswer
    /** Refused or unanswered: the device's words, else ours. */
    data class Refused(val words: UiText) : RatingAnswer
}

/** The error_code of a rating asked for in a collection whose Rating is off. */
internal const val RATING_OFF = "rating_off"

/**
 * What [resp] (null: no answer) to a SetPhotoRating ([photo]) or a
 * SetImageGroupRating means. A refusal says the device's words - the demo's,
 * "rating_off", "not_in_collection" - else ours: save_failed for a photo,
 * toggle_failed for the collection.
 */
internal fun ratingAnswer(resp: RespEnvelope?, photo: Boolean): RatingAnswer {
    val ours = if (photo) S.appPhotosRatingSaveFailed() else S.appPhotosRatingToggleFailed()
    return when {
        resp == null -> RatingAnswer.Refused(ours)
        resp.isUnknownPayload() -> RatingAnswer.NeedsUpdate
        resp.error && resp.errorCode == RATING_OFF -> RatingAnswer.Off(deviceOr(resp.errorMessage, ours))
        resp.error -> RatingAnswer.Refused(deviceOr(resp.errorMessage, ours))
        photo && resp.payloadCase == RespEnvelope.PayloadCase.RESP_PHOTO_RATING -> RatingAnswer.Kept(resp.respPhotoRating)
        !photo && resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.ok -> RatingAnswer.Kept(null)
        resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK -> RatingAnswer.Refused(deviceOr(resp.respAck.errorMsg, ours))
        else -> RatingAnswer.Refused(ours)
    }
}

/**
 * "Rating on: rate photos with stars and comments" is said the first time
 * Rating is turned on from this phone: kept in otc_settings (backed up with
 * it, wiped at Log Out), as the pages' Sort by are.
 */
internal object RatingHint {
    private const val KEY = "rating_hint_seen"

    /** True the first time it is asked (and from then on false); false when the store can't be read. */
    fun firstTime(): Boolean = try {
        val prefs = OTCApp.instance.getSharedPreferences("otc_settings", Context.MODE_PRIVATE)
        if (prefs.getBoolean(KEY, false)) false else { prefs.edit().putBoolean(KEY, true).apply(); true }
    } catch (_: Exception) { false }
}

/** How a rated tile is read out after its date: its stars, and "Comment" when it has one (nothing for an unrated photo). */
internal fun ratingLabels(r: PhotoRating?): List<UiText> = buildList {
    if (r == null) return@buildList
    if (r.stars > 0) add(S.appPhotosRatingStars(r.stars))
    if (r.comment.isNotEmpty()) add(S.appPhotosRatingComment())
}
