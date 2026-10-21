// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.social

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.font.FontWeight
import cloud.offthe.otc.i18n.EnglishStrings
import cloud.offthe.otc.i18n.RichTag
import cloud.offthe.otc.i18n.RichText
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiTextFormat
import cloud.offthe.otc.i18n.androidAlertsCollapse
import cloud.offthe.otc.i18n.androidAlertsExpand
import cloud.offthe.otc.i18n.androidAlertsFriends
import cloud.offthe.otc.i18n.androidComposeMoveLeft
import cloud.offthe.otc.i18n.androidComposeMoveRight
import cloud.offthe.otc.i18n.androidComposeRecordVideo
import cloud.offthe.otc.i18n.androidComposeTakePhoto
import cloud.offthe.otc.i18n.androidFriendsAskTheirsUnnamed
import cloud.offthe.otc.i18n.androidFriendsDeleteTheirs
import cloud.offthe.otc.i18n.androidFriendsDeleteTheirsUnnamed
import cloud.offthe.otc.i18n.androidFriendsKeep
import cloud.offthe.otc.i18n.androidFriendsKeepWaiting
import cloud.offthe.otc.i18n.androidFriendsRemoveFriend
import cloud.offthe.otc.i18n.androidFriendsRemoveNow
import cloud.offthe.otc.i18n.androidFriendsRemoveNowTitleUnnamed
import cloud.offthe.otc.i18n.androidFriendsRemoveTitleUnnamed
import cloud.offthe.otc.i18n.androidSocialDeleteComment
import cloud.offthe.otc.i18n.androidSocialLike
import cloud.offthe.otc.i18n.androidSocialLikeComment
import cloud.offthe.otc.i18n.androidSocialMute
import cloud.offthe.otc.i18n.androidSocialPlay
import cloud.offthe.otc.i18n.androidSocialReplay
import cloud.offthe.otc.i18n.androidSocialUnlike
import cloud.offthe.otc.i18n.androidSocialUnlikeComment
import cloud.offthe.otc.i18n.androidTrimCant
import cloud.offthe.otc.i18n.androidTrimPlayCut
import cloud.offthe.otc.i18n.appShellNewPost
import cloud.offthe.otc.i18n.appShellSectionFriends
import cloud.offthe.otc.i18n.appSocialAlertsCommented
import cloud.offthe.otc.i18n.appSocialAlertsEmpty
import cloud.offthe.otc.i18n.appSocialAlertsErrorMore
import cloud.offthe.otc.i18n.appSocialAlertsFriendAccepted
import cloud.offthe.otc.i18n.appSocialAlertsFriendRequest
import cloud.offthe.otc.i18n.appSocialAlertsLikedComment
import cloud.offthe.otc.i18n.appSocialAlertsLikedPost
import cloud.offthe.otc.i18n.appSocialAlertsTitle
import cloud.offthe.otc.i18n.appSocialComposeCamera
import cloud.offthe.otc.i18n.appSocialComposeCaptionPlaceholder
import cloud.offthe.otc.i18n.appSocialComposeEmptyResponse
import cloud.offthe.otc.i18n.appSocialComposeFilterPlaceholder
import cloud.offthe.otc.i18n.appSocialComposeOrder
import cloud.offthe.otc.i18n.appSocialComposePhotosAccess
import cloud.offthe.otc.i18n.appSocialComposePublish
import cloud.offthe.otc.i18n.appSocialComposePublishCount
import cloud.offthe.otc.i18n.appSocialComposePublishFailed
import cloud.offthe.otc.i18n.appSocialComposePublishFailedDetail
import cloud.offthe.otc.i18n.appSocialComposePublishing
import cloud.offthe.otc.i18n.appSocialComposeSourcePhone
import cloud.offthe.otc.i18n.appSocialComposeSourceSynced
import cloud.offthe.otc.i18n.appSocialComposeTitle
import cloud.offthe.otc.i18n.appSocialComposeTrimBadge
import cloud.offthe.otc.i18n.appSocialComposeTrimLoadFailed
import cloud.offthe.otc.i18n.appSocialComposeUploading
import cloud.offthe.otc.i18n.appSocialFeedEmptyText
import cloud.offthe.otc.i18n.appSocialFeedEmptyTitle
import cloud.offthe.otc.i18n.appSocialFriendsAddHeading
import cloud.offthe.otc.i18n.appSocialFriendsAskTheirs
import cloud.offthe.otc.i18n.appSocialFriendsAskTheirsText
import cloud.offthe.otc.i18n.appSocialFriendsBlock
import cloud.offthe.otc.i18n.appSocialFriendsCancelConfirmText
import cloud.offthe.otc.i18n.appSocialFriendsCancelConfirmTitle
import cloud.offthe.otc.i18n.appSocialFriendsCancelRequest
import cloud.offthe.otc.i18n.appSocialFriendsCancelRequestButton
import cloud.offthe.otc.i18n.appSocialFriendsDeleteConfirmText
import cloud.offthe.otc.i18n.appSocialFriendsDeleteConfirmTitle
import cloud.offthe.otc.i18n.appSocialFriendsDeleteError
import cloud.offthe.otc.i18n.appSocialFriendsDeleteFailed
import cloud.offthe.otc.i18n.appSocialFriendsDeleteRequest
import cloud.offthe.otc.i18n.appSocialFriendsDeleteTheirsText
import cloud.offthe.otc.i18n.appSocialFriendsEmpty
import cloud.offthe.otc.i18n.appSocialFriendsFriendsAgain
import cloud.offthe.otc.i18n.appSocialFriendsLeavingNote
import cloud.offthe.otc.i18n.appSocialFriendsListHeading
import cloud.offthe.otc.i18n.appSocialFriendsLoadFailed
import cloud.offthe.otc.i18n.appSocialFriendsNameInvalid
import cloud.offthe.otc.i18n.appSocialFriendsNamePlaceholder
import cloud.offthe.otc.i18n.appSocialFriendsNoDomain
import cloud.offthe.otc.i18n.appSocialFriendsNoName
import cloud.offthe.otc.i18n.appSocialFriendsRemoveButton
import cloud.offthe.otc.i18n.appSocialFriendsRemoveError
import cloud.offthe.otc.i18n.appSocialFriendsRemoveFailed
import cloud.offthe.otc.i18n.appSocialFriendsRemoveNow
import cloud.offthe.otc.i18n.appSocialFriendsRemoveNowText
import cloud.offthe.otc.i18n.appSocialFriendsRemoveNowTitle
import cloud.offthe.otc.i18n.appSocialFriendsRemoveTitle
import cloud.offthe.otc.i18n.appSocialFriendsRemoved
import cloud.offthe.otc.i18n.appSocialFriendsRequestCancelled
import cloud.offthe.otc.i18n.appSocialFriendsRequestDeleted
import cloud.offthe.otc.i18n.appSocialFriendsRequestFailed
import cloud.offthe.otc.i18n.appSocialFriendsRequestSent
import cloud.offthe.otc.i18n.appSocialFriendsSendError
import cloud.offthe.otc.i18n.appSocialFriendsSetPending
import cloud.offthe.otc.i18n.appSocialFriendsStatusAccepted
import cloud.offthe.otc.i18n.appSocialFriendsStatusAcceptedSent
import cloud.offthe.otc.i18n.appSocialFriendsStatusBlocked
import cloud.offthe.otc.i18n.appSocialFriendsStatusBlockedSent
import cloud.offthe.otc.i18n.appSocialFriendsStatusLeaving
import cloud.offthe.otc.i18n.appSocialFriendsStatusPending
import cloud.offthe.otc.i18n.appSocialFriendsStatusPendingSent
import cloud.offthe.otc.i18n.appSocialFriendsStatusUpdated
import cloud.offthe.otc.i18n.appSocialFriendsTitle
import cloud.offthe.otc.i18n.appSocialFriendsUnexpectedResponse
import cloud.offthe.otc.i18n.appSocialFriendsUpdateError
import cloud.offthe.otc.i18n.appSocialFriendsUpdateFailed
import cloud.offthe.otc.i18n.appSocialFriendsWaitingAnswer
import cloud.offthe.otc.i18n.appSocialFriendsWaitingTheirDevice
import cloud.offthe.otc.i18n.appSocialLikesEmpty
import cloud.offthe.otc.i18n.appSocialLikesTitle
import cloud.offthe.otc.i18n.appSocialPostComment
import cloud.offthe.otc.i18n.appSocialPostCommentPlaceholder
import cloud.offthe.otc.i18n.appSocialPostCommentUnnamed
import cloud.offthe.otc.i18n.appSocialPostDeleteComment
import cloud.offthe.otc.i18n.appSocialPostDeleteCommentTitle
import cloud.offthe.otc.i18n.appSocialPostDeletePost
import cloud.offthe.otc.i18n.appSocialPostDeletePostTitle
import cloud.offthe.otc.i18n.appSocialPostLikes
import cloud.offthe.otc.i18n.appSocialPostSendComment
import cloud.offthe.otc.i18n.appSocialPostUnnamed
import cloud.offthe.otc.i18n.appSocialTrimCantOpen
import cloud.offthe.otc.i18n.appSocialTrimKeeping
import cloud.offthe.otc.i18n.appSocialTrimNoLength
import cloud.offthe.otc.i18n.appSocialTrimReading
import cloud.offthe.otc.i18n.appSocialTrimRemove
import cloud.offthe.otc.i18n.appSocialTrimTitle
import cloud.offthe.otc.i18n.commonAccept
import cloud.offthe.otc.i18n.commonApply
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonDecline
import cloud.offthe.otc.i18n.commonDone
import cloud.offthe.otc.i18n.commonLoading
import cloud.offthe.otc.i18n.commonOk
import cloud.offthe.otc.i18n.commonRefresh
import cloud.offthe.otc.i18n.commonRemove
import cloud.offthe.otc.i18n.commonSearch
import cloud.offthe.otc.i18n.commonSend
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.ui.compose.formatTimecode
import org.junit.Assert.assertEquals
import org.junit.Test
import java.text.NumberFormat
import java.util.Locale

// The Social feed, Friends, the composer, the trimmer and Alerts word their
// text from the catalog (app.social, android.*): in English each text is
// exactly the literal it replaced. The expected strings are those literals
// as the code had them; a name or other user text comes between isolation
// marks (FSI/PDI), the one difference allowed. iOS's SocialTextTests is the
// same.
class SocialTextTest {
    /** A user-controlled value as a text shows it. */
    private fun iso(s: String) = "⁨$s⁩"

    private val bold = SpanStyle(fontWeight = FontWeight.Bold)
    private val grey = SpanStyle(color = Color.Gray)
    private val tags = mapOf("b" to RichTag(style = bold), "dim" to RichTag(style = grey))

    /** A rich text in English, as RichText.resolve builds it from the English resources. */
    private fun RichText.english(): AnnotatedString {
        val (template, args) = when (this) {
            is RichText.Res -> EnglishStrings.string(id).first to args
            is RichText.Plural -> EnglishStrings.plural(id, count).first to args
        }
        val values = UiTextFormat.values(args, Locale.US, { n, l -> NumberFormat.getIntegerInstance(l).format(n) }) { it.english() }
        return UiTextFormat.rich(template, values, Locale.US, tags)
    }

    /** The part of [a] in [style]. */
    private fun styled(a: AnnotatedString, style: SpanStyle) =
        a.spanStyles.filter { it.item == style }.joinToString("") { a.text.substring(it.start, it.end) }

    @Test fun friendsToasts() {
        assertEquals("Could not load friendships", S.appSocialFriendsLoadFailed().english())
        assertEquals("Enter the device's name, like pit - letters, digits and dashes.", S.appSocialFriendsNameInvalid().english())
        assertEquals("You're friends again ✅", S.appSocialFriendsFriendsAgain().english())
        assertEquals("Friend request sent ✅", S.appSocialFriendsRequestSent().english())
        assertEquals("Request failed", S.appSocialFriendsRequestFailed().english())
        assertEquals("Unexpected response", S.appSocialFriendsUnexpectedResponse().english())
        assertEquals("Error sending request", S.appSocialFriendsSendError().english())
        assertEquals("Status updated ✅", S.appSocialFriendsStatusUpdated().english())
        assertEquals("Update failed", S.appSocialFriendsUpdateFailed().english())
        assertEquals("Error updating status", S.appSocialFriendsUpdateError().english())
        assertEquals("Request cancelled", S.appSocialFriendsRequestCancelled().english())
        assertEquals("Request deleted", S.appSocialFriendsRequestDeleted().english())
        assertEquals("Delete failed", S.appSocialFriendsDeleteFailed().english())
        assertEquals("Error deleting request", S.appSocialFriendsDeleteError().english())
        assertEquals("Waiting for their device", S.appSocialFriendsWaitingTheirDevice().english())
        assertEquals("Friend removed", S.appSocialFriendsRemoved().english())
        assertEquals("Remove failed", S.appSocialFriendsRemoveFailed().english())
        assertEquals("Error removing friend", S.appSocialFriendsRemoveError().english())
    }

    @Test fun friendsScreen() {
        assertEquals("Friends", S.appSocialFriendsTitle().english())
        assertEquals("Done", S.commonDone().english())
        assertEquals("Waiting for your answer", S.appSocialFriendsWaitingAnswer().english())
        assertEquals("(no name)", S.appSocialFriendsNoName().english())
        assertEquals("(no domain)", S.appSocialFriendsNoDomain().english())
        assertEquals("Add a friend", S.appSocialFriendsAddHeading().english())
        assertEquals("name", S.appSocialFriendsNamePlaceholder().english())
        assertEquals("Friends", S.appSocialFriendsListHeading().english())
        assertEquals("No friendships yet.", S.appSocialFriendsEmpty().english())
        assertEquals("Cancel request", S.appSocialFriendsCancelRequest().english())
        assertEquals("Delete request", S.appSocialFriendsDeleteRequest().english())
        // statusLabel + if (f.sent && !f.leaving) " (sent)" else ""
        assertEquals("Leaving", S.appSocialFriendsStatusLeaving().english())
        assertEquals("Accepted", S.appSocialFriendsStatusAccepted().english())
        assertEquals("Blocked", S.appSocialFriendsStatusBlocked().english())
        assertEquals("Pending", S.appSocialFriendsStatusPending().english())
        assertEquals("Accepted" + " (sent)", S.appSocialFriendsStatusAcceptedSent().english())
        assertEquals("Blocked" + " (sent)", S.appSocialFriendsStatusBlockedSent().english())
        assertEquals("Pending" + " (sent)", S.appSocialFriendsStatusPendingSent().english())
        assertEquals("Block", S.appSocialFriendsBlock().english())
        assertEquals("Pending", S.appSocialFriendsSetPending().english())
        assertEquals("Waiting for their device to delete what you shared.", S.appSocialFriendsLeavingNote().english())
        assertEquals("Remove Now", S.appSocialFriendsRemoveNow().english())
        assertEquals("Cancel Request", S.appSocialFriendsCancelRequestButton().english())
        assertEquals("Remove", S.appSocialFriendsRemoveButton().english())
        // "Remove $name now?" and the rest, with name = profile name, else domain, else "this friend".
        assertEquals("Remove ${iso("Ana")} now?", S.appSocialFriendsRemoveNowTitle("Ana").english())
        assertEquals("Remove this friend now?", S.androidFriendsRemoveNowTitleUnnamed().english())
        assertEquals("Stops waiting for their device. Whatever it hasn't deleted yet stays there.", S.appSocialFriendsRemoveNowText().english())
        assertEquals("Remove now", S.androidFriendsRemoveNow().english())
        assertEquals("Keep waiting", S.androidFriendsKeepWaiting().english())
        assertEquals("Cancel this friend request?", S.appSocialFriendsCancelConfirmTitle().english())
        assertEquals("Delete this friend request?", S.appSocialFriendsDeleteConfirmTitle().english())
        assertEquals("The request will be withdrawn on both devices.", S.appSocialFriendsCancelConfirmText().english())
        assertEquals("The request will be removed here and on the sender's device.", S.appSocialFriendsDeleteConfirmText().english())
        assertEquals("Remove ${iso("Ana")}?", S.appSocialFriendsRemoveTitle("Ana").english())
        assertEquals("Remove this friend?", S.androidFriendsRemoveTitleUnnamed().english())
        assertEquals("Delete everything from ${iso("Ana")} on this phone", S.androidFriendsDeleteTheirs("Ana").english())
        assertEquals("Delete everything from this friend on this phone", S.androidFriendsDeleteTheirsUnnamed().english())
        assertEquals("Their posts and photos, and their comments and likes on any post.", S.appSocialFriendsDeleteTheirsText().english())
        assertEquals("Ask ${iso("Ana")}'s device to delete what I shared", S.appSocialFriendsAskTheirs("Ana").english())
        assertEquals("Ask this friend's device to delete what I shared", S.androidFriendsAskTheirsUnnamed().english())
        assertEquals(
            "Your posts, comments and likes on their device. If it's offline, it happens the next time it connects; until then they show as leaving.",
            S.appSocialFriendsAskTheirsText().english(),
        )
        assertEquals("Remove friend", S.androidFriendsRemoveFriend().english())
        assertEquals("Keep", S.androidFriendsKeep().english())
    }

    @Test fun feedAndPosts() {
        assertEquals("Friends", S.appShellSectionFriends().english())
        assertEquals("New post", S.appShellNewPost().english())
        assertEquals("No social posts", S.appSocialFeedEmptyTitle().english())
        assertEquals(
            "Share a photo or a video with your friends - it goes from your device to theirs, with no cloud in between.",
            S.appSocialFeedEmptyText().english(),
        )
        assertEquals("User", S.appSocialPostUnnamed().english())
        assertEquals("Delete Post", S.appSocialPostDeletePost().english())
        assertEquals("Unlike", S.androidSocialUnlike().english())
        assertEquals("Like", S.androidSocialLike().english())
        // "${post.likes} like${if (post.likes == 1) "" else "s"}"
        assertEquals("1 like", S.appSocialPostLikes(1).english())
        assertEquals("2 likes", S.appSocialPostLikes(2).english())
        assertEquals("0 likes", S.appSocialPostLikes(0).english())
        assertEquals("Unlike comment", S.androidSocialUnlikeComment().english())
        assertEquals("Like comment", S.androidSocialLikeComment().english())
        assertEquals("Delete comment", S.androidSocialDeleteComment().english())
        assertEquals("Add a comment…", S.appSocialPostCommentPlaceholder().english())
        assertEquals("Post", S.appSocialPostSendComment().english())
        assertEquals("Delete this post?", S.appSocialPostDeletePostTitle().english())
        assertEquals("Delete this comment?", S.appSocialPostDeleteCommentTitle().english())
        assertEquals("Delete Comment", S.appSocialPostDeleteComment().english())
        assertEquals("Play", S.androidSocialPlay().english())
        assertEquals("Replay", S.androidSocialReplay().english())
        assertEquals("Mute", S.androidSocialMute().english())
        assertEquals("Likes", S.appSocialLikesTitle().english())
        assertEquals("No likes yet", S.appSocialLikesEmpty().english())
        // withStyle(bold) { append(publisher) }; append(": " + comment)
        val named = S.appSocialPostComment("Ana", "Nice <b>one</b>").english()
        assertEquals(iso("Ana") + ": " + iso("Nice <b>one</b>"), named.text)
        assertEquals(iso("Ana"), styled(named, bold))
        val unnamed = S.appSocialPostCommentUnnamed("Hi").english()
        assertEquals("User" + ": " + iso("Hi"), unnamed.text)
        assertEquals("User", styled(unnamed, bold))
    }

    @Test fun alerts() {
        assertEquals("Notifications", S.appSocialAlertsTitle().english())
        assertEquals("Nothing yet", S.appSocialAlertsEmpty().english())
        // withStyle(bold) { append(name) }; append(" " + describe(n))
        val rows = listOf(
            S.appSocialAlertsLikedPost("Ana") to "liked your post",
            S.appSocialAlertsLikedComment("Ana") to "liked your comment",
            S.appSocialAlertsCommented("Ana") to "commented on your post",
            S.appSocialAlertsFriendRequest("Ana") to "sent you a friend request",
            S.appSocialAlertsFriendAccepted("Ana") to "accepted your friend request",
        )
        for ((line, what) in rows) {
            val a = line.english()
            assertEquals(iso("Ana") + " " + what, a.text)
            assertEquals(iso("Ana"), styled(a, bold))
        }
        assertEquals("Friends ✓", S.androidAlertsFriends().english())
        // withStyle(bold) { append(title) }; withStyle(grey) { append(" +${n.occurrences - 1} more") }
        val more = S.appSocialAlertsErrorMore("Photo could not be processed", 2).english()
        assertEquals("Photo could not be processed" + " +2 more", more.text)
        assertEquals("Photo could not be processed", styled(more, bold))
        assertEquals("+2 more", styled(more, grey))
        assertEquals("T +1 more", S.appSocialAlertsErrorMore("T", 1).english().text)
        assertEquals("Collapse", S.androidAlertsCollapse().english())
        assertEquals("Expand", S.androidAlertsExpand().english())
    }

    @Test fun composer() {
        assertEquals("Photos access is needed to pick from your phone.", S.appSocialComposePhotosAccess().english())
        assertEquals("Could not load that video to trim it: Disk full", S.appSocialComposeTrimLoadFailed("Disk full").english())
        assertEquals("Could not load that video to trim it: Empty response", S.appSocialComposeTrimLoadFailed(S.appSocialComposeEmptyResponse().english()).english())
        assertEquals("Empty response", S.appSocialComposeEmptyResponse().english())
        assertEquals("Uploading 1 of 3…", S.appSocialComposeUploading(1, 3).english())
        assertEquals("Publishing…", S.appSocialComposePublishing().english())
        assertEquals("Could not publish: Duplicated file", S.appSocialComposePublishFailedDetail("Duplicated file").english())
        assertEquals("Could not publish", S.appSocialComposePublishFailed().english())
        assertEquals("New Post", S.appSocialComposeTitle().english())
        assertEquals("Phone", S.appSocialComposeSourcePhone().english())
        assertEquals("Synced", S.appSocialComposeSourceSynced().english())
        assertEquals("Filter by tag…", S.appSocialComposeFilterPlaceholder().english())
        assertEquals("Take a photo or video", S.appSocialComposeCamera().english())
        assertEquals("Take a photo", S.androidComposeTakePhoto().english())
        assertEquals("Record a video", S.androidComposeRecordVideo().english())
        assertEquals("Write a caption…", S.appSocialComposeCaptionPlaceholder().english())
        // "Publish" + if (st.selectedOrder.isEmpty()) "" else " (${st.selectedOrder.size})"
        assertEquals("Publish", S.appSocialComposePublish().english())
        assertEquals("Publish" + " (3)", S.appSocialComposePublishCount(3).english())
        assertEquals("Order in post", S.appSocialComposeOrder().english())
        assertEquals("✂ Trim", S.appSocialComposeTrimBadge().english())
        assertEquals("Move left", S.androidComposeMoveLeft().english())
        assertEquals("Move right", S.androidComposeMoveRight().english())
    }

    @Test fun commonWordsReused() {
        // Literals these screens had, now common.* keys.
        assertEquals("Accept", S.commonAccept().english())
        assertEquals("Decline", S.commonDecline().english())
        assertEquals("Send", S.commonSend().english())
        assertEquals("Refresh", S.commonRefresh().english())
        assertEquals("Cancel", S.commonCancel().english())
        assertEquals("Loading…", S.commonLoading().english())
        assertEquals("Search", S.commonSearch().english())
        assertEquals("OK", S.commonOk().english())
        assertEquals("Remove", S.commonRemove().english())
        assertEquals("Apply", S.commonApply().english())
    }

    @Test fun trimmer() {
        assertEquals("Trim video", S.appSocialTrimTitle().english())
        assertEquals("Keeping 0:12.5 of 1:01.0", S.appSocialTrimKeeping(formatTimecode(12.5), formatTimecode(61.0)).english())
        assertEquals("▶ Play cut", S.androidTrimPlayCut().english())
        assertEquals("Remove trim", S.appSocialTrimRemove().english())
        assertEquals("This video's length could not be read.", S.appSocialTrimNoLength().english())
        assertEquals("This video could not be opened.", S.appSocialTrimCantOpen().english())
        assertEquals("Can't trim this video: This video could not be opened.", S.androidTrimCant(S.appSocialTrimCantOpen().english()).english())
        assertEquals("Reading the clip…", S.appSocialTrimReading().english())
    }
}
