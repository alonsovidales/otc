// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftUI
import Testing
@testable import OffTheCloud

/// The Social feed, Friends, the composer, the trimmer and Alerts word
/// their text from the catalog (app.social, ios.*): in English each text is
/// exactly the literal it replaced. The expected strings are those literals
/// as the code had them; a name or other user text comes between isolation
/// marks (FSI/PDI), the one difference allowed. Android's SocialTextTest is
/// the same.
struct SocialTextTests {
    /// A user-controlled value as a text shows it.
    private func iso(_ s: String) -> String { "\u{2068}" + s + "\u{2069}" }

    private var bold: AttributeContainer {
        var b = AttributeContainer()
        b.inlinePresentationIntent = .stronglyEmphasized
        return b
    }

    private var dim: AttributeContainer {
        var d = AttributeContainer()
        d.foregroundColor = .secondary
        return d
    }

    /// The text of an AttributedString, and the part of it in `container`.
    private func split(_ a: AttributedString, _ container: AttributeContainer) -> (all: String, styled: String) {
        var styled = ""
        for run in a.runs where run.attributes.inlinePresentationIntent == container.inlinePresentationIntent
            && (container.inlinePresentationIntent != nil || run.attributes.swiftUI.foregroundColor == container.swiftUI.foregroundColor) {
            styled += String(a[run.range].characters)
        }
        return (String(a.characters), styled)
    }

    @Test func friendsToasts() {
        let expected: [(FriendsToast, String)] = [
            (.loadFailed, "Could not load friendships"),
            (.nameInvalid, "Enter the device's name, like pit - letters, digits and dashes."),
            (.friendsAgain, "You're friends again ✅"),
            (.requestSent, "Friend request sent ✅"),
            (.requestFailed, "Request failed"),
            (.unexpectedResponse, "Unexpected response"),
            (.sendError, "Error sending request"),
            (.statusUpdated, "Status updated ✅"),
            (.updateFailed, "Update failed"),
            (.updateError, "Error updating status"),
            (.requestCancelled, "Request cancelled"),
            (.requestDeleted, "Request deleted"),
            (.deleteFailed, "Delete failed"),
            (.deleteError, "Error deleting request"),
            (.waitingForTheirDevice, "Waiting for their device"),
            (.removed, "Friend removed"),
            (.removeFailed, "Remove failed"),
            (.removeError, "Error removing friend"),
            // The device's words are shown as they came.
            (.device("Ya sois amigos"), "Ya sois amigos"),
        ]
        for (toast, english) in expected { #expect(toast.text == english) }
    }

    @Test func friendsScreen() {
        #expect(S.appSocialFriendsWaitingAnswer == "Waiting for your answer")
        #expect(S.appSocialFriendsNoName == "(no name)")
        #expect(S.appSocialFriendsNoDomain == "(no domain)")
        #expect(S.appSocialFriendsAddHeading == "Add a friend")
        #expect(S.appSocialFriendsNamePlaceholder == "name")
        #expect(S.appSocialFriendsEmpty == "No friendships yet.")
        #expect(S.appSocialFriendsListHeading == "Friends")
        #expect(S.appSocialFriendsTitle == "Friends")
        #expect(S.appShellSectionFriends == "Friends")
        #expect(S.appSocialFriendsCancelRequest == "Cancel request")
        #expect(S.appSocialFriendsDeleteRequest == "Delete request")
        #expect(S.appSocialFriendsLeavingNote == "Waiting for their device to delete what you shared.")
        #expect(S.appSocialFriendsRemoveNow == "Remove Now")
        #expect(S.appSocialFriendsCancelRequestButton == "Cancel Request")
        #expect(S.appSocialFriendsRemoveButton == "Remove…")
        #expect(S.appSocialFriendsCancelConfirmTitle == "Cancel this friend request?")
        #expect(S.appSocialFriendsDeleteConfirmTitle == "Delete this friend request?")
        #expect(S.appSocialFriendsCancelConfirmText == "The request will be withdrawn on both devices.")
        #expect(S.appSocialFriendsDeleteConfirmText == "The request will be removed here and on the sender's device.")
        #expect(S.appSocialFriendsRemoveNowTitle(name: "Ana") == "Remove \(iso("Ana")) now?")
        #expect(S.appSocialFriendsRemoveNowText == "Stops waiting for their device. Whatever it hasn't deleted yet stays there.")
        // statusLabel + (f.sent && !f.leaving ? " (sent)" : "")
        #expect(S.appSocialFriendsStatusLeaving == "Leaving")
        #expect(S.appSocialFriendsStatusAccepted == "Accepted")
        #expect(S.appSocialFriendsStatusBlocked == "Blocked")
        #expect(S.appSocialFriendsStatusPending == "Pending")
        #expect(S.appSocialFriendsStatusAcceptedSent == "Accepted" + " (sent)")
        #expect(S.appSocialFriendsStatusBlockedSent == "Blocked" + " (sent)")
        #expect(S.appSocialFriendsStatusPendingSent == "Pending" + " (sent)")
        #expect(S.appSocialFriendsBlock == "Block")
        #expect(S.appSocialFriendsSetPending == "Set Pending")
        #expect(S.commonAccept == "Accept")
        #expect(S.commonDecline == "Decline")
        #expect(S.commonSend == "Send")
        #expect(S.commonRefresh == "Refresh")
        #expect(S.commonDone == "Done")
        #expect(S.commonCancel == "Cancel")
        // RemoveFriendSheet.
        #expect(S.iosFriendsDeleteTheirs(name: "Ana") == "Delete everything from \(iso("Ana")) on this iPhone")
        #expect(S.appSocialFriendsDeleteTheirsText == "Their posts and photos, and their comments and likes on any post.")
        #expect(S.appSocialFriendsAskTheirs(name: "Ana") == "Ask \(iso("Ana"))'s device to delete what I shared")
        #expect(S.appSocialFriendsAskTheirsText == "Your posts, comments and likes on their device. If it's offline, it happens the next time it connects; until then they show as leaving.")
        #expect(S.iosFriendsRemoveFriend == "Remove Friend")
        #expect(S.appSocialFriendsRemoveTitle(name: "Ana") == "Remove \(iso("Ana"))?")
    }

    @Test func feedAndPosts() {
        #expect(S.commonLoading == "Loading…")
        #expect(S.appSocialFeedEmptyTitle == "No social posts")
        #expect(S.appSocialFeedEmptyText == "Share a photo or a video with your friends - it goes from your device to theirs, with no cloud in between.")
        #expect(S.appShellNewPost == "New post")
        #expect(S.appSocialPostUnnamed == "User")
        #expect(S.appSocialPostDeletePost == "Delete Post")
        // "\(post.likes) like\(post.likes == 1 ? "" : "s")"
        #expect(S.appSocialPostLikes(count: 1) == "1 like")
        #expect(S.appSocialPostLikes(count: 2) == "2 likes")
        #expect(S.appSocialPostLikes(count: 0) == "0 likes")
        #expect(S.appSocialPostCommentPlaceholder == "Add a comment…")
        #expect(S.appSocialPostSendComment == "Post")
        #expect(S.appSocialPostDeletePostTitle == "Delete this post?")
        #expect(S.appSocialPostDeleteCommentTitle == "Delete this comment?")
        #expect(S.appSocialPostDeleteComment == "Delete Comment")
        #expect(S.appSocialLikesEmpty == "No likes yet")
        #expect(S.appSocialLikesTitle == "Likes")
        // Text(publisher).bold() + Text(": " + comment)
        let named = split(S.appSocialPostComment(name: "Ana", comment: "Nice <b>one</b>", b: bold), bold)
        #expect(named.all == iso("Ana") + ": " + iso("Nice <b>one</b>"))
        #expect(named.styled == iso("Ana"))
        let unnamed = split(S.appSocialPostCommentUnnamed(comment: "Hi", b: bold), bold)
        #expect(unnamed.all == "User" + ": " + iso("Hi"))
        #expect(unnamed.styled == "User")
    }

    @Test func postTimes() {
        #expect(S.appShellTimeJustNow == "just now")
        #expect(S.appShellTimeMinutesAgo(count: 1) == "1m ago")
        #expect(S.appShellTimeMinutesAgo(count: 59) == "59m ago")
        #expect(S.appShellTimeHoursAgo(count: 3) == "3h ago")
        #expect(S.appShellTimeDaysAgo(count: 6) == "6d ago")
    }

    @Test func alerts() {
        // Text(name).bold() + Text(" " + describe(n))
        let rows: [(AttributedString, String)] = [
            (S.appSocialAlertsLikedPost(name: "Ana", b: bold), "liked your post"),
            (S.appSocialAlertsLikedComment(name: "Ana", b: bold), "liked your comment"),
            (S.appSocialAlertsCommented(name: "Ana", b: bold), "commented on your post"),
            (S.appSocialAlertsFriendRequest(name: "Ana", b: bold), "sent you a friend request"),
            (S.appSocialAlertsFriendAccepted(name: "Ana", b: bold), "accepted your friend request"),
        ]
        for (line, what) in rows {
            let s = split(line, bold)
            #expect(s.all == iso("Ana") + " " + what)
            #expect(s.styled == iso("Ana"))
        }
        // Text(title).bold() + Text(" +\(occurrences - 1) more").foregroundStyle(.secondary)
        let more = S.appSocialAlertsErrorMore(title: "Photo could not be processed", count: 2, b: bold, dim: dim)
        #expect(String(more.characters) == "Photo could not be processed" + " +2 more")
        #expect(split(more, bold).styled == "Photo could not be processed")
        #expect(split(more, dim).styled == "+2 more")
        #expect(String(S.appSocialAlertsErrorMore(title: "T", count: 1, b: bold, dim: dim).characters) == "T +1 more")
        // "\(title), \(occurrences) error\(occurrences == 1 ? "" : "s"), \(expanded ? "collapse" : "expand")"
        #expect(S.iosAlertsErrorsExpand(title: "T", count: 1) == "T, 1 error, expand")
        #expect(S.iosAlertsErrorsExpand(title: "T", count: 3) == "T, 3 errors, expand")
        #expect(S.iosAlertsErrorsCollapse(title: "T", count: 1) == "T, 1 error, collapse")
        #expect(S.iosAlertsErrorsCollapse(title: "T", count: 3) == "T, 3 errors, collapse")
        #expect(S.appSocialAlertsEmpty == "Nothing yet")
        #expect(S.appSocialAlertsTitle == "Notifications")
        #expect(S.appShellSectionAlerts == "Alerts")
        #expect(S.iosAlertsFriends == "Friends")
    }

    @Test func composer() {
        typealias VM = NewPostPickerVM
        #expect(VM.PublishStatus.uploading(done: 1, total: 3).text == "Uploading 1 of 3…")
        #expect(VM.PublishStatus.publishing.text == "Publishing…")
        let failure = NSError(domain: "test", code: 1, userInfo: [NSLocalizedDescriptionKey: "No space left"])
        #expect(VM.ComposerAlert.photosAccess.text == "Photos access is needed to pick from your phone.")
        #expect(VM.ComposerAlert.captureSaveFailed(failure).text == "Couldn't save the capture to Photos: No space left")
        #expect(VM.ComposerAlert.captureUnreadable.text == "The capture was saved to Photos but couldn't be read back.")
        #expect(VM.ComposerAlert.trimLoadFailed(failure).text == "Could not load that video to trim it: No space left")
        #expect(VM.ComposerAlert.publishRefused("Duplicated file").text == "Could not publish: Duplicated file")
        #expect(VM.ComposerAlert.publishFailed.text == "Could not publish")
        #expect(VM.ComposerAlert.publishError(failure).text == "Could not publish: No space left")
        // The NSErrors they replaced: their descriptions, domain and codes.
        let errors: [(VM.ComposeError, String, Int)] = [
            (.videoUnavailable, "This video is not available on the phone", -3),
            (.emptyResponse, "Empty response", -4),
            (.unreadablePhoto, "Could not read this photo", -1),
            (.refused("Refused here"), "Refused here", -2),
            (.uploadFailed(""), "Upload failed", -3),
            (.uploadFailed("Disk full"), "Disk full", -3),
        ]
        for (e, english, code) in errors {
            #expect(e.localizedDescription == english)
            #expect((e as NSError).domain == "NewPostPicker")
            #expect((e as NSError).code == code)
        }
        #expect(VM.ComposerAlert.trimLoadFailed(VM.ComposeError.emptyResponse).text == "Could not load that video to trim it: Empty response")
        #expect(S.iosComposeSource == "Source")
        #expect(S.appSocialComposeSourcePhone == "Phone")
        #expect(S.appSocialComposeSourceSynced == "Synced")
        #expect(S.appSocialComposeFilterPlaceholder == "Filter by tag…")
        #expect(S.commonSearch == "Search")
        #expect(S.appSocialComposeCamera == "Take a photo or video")
        #expect(S.appSocialComposeCaptionPlaceholder == "Write a caption…")
        // "Publish\(selectedOrder.isEmpty ? "" : " (\(selectedOrder.count))")"
        #expect(S.appSocialComposePublish == "Publish")
        #expect(S.appSocialComposePublishCount(number: 3) == "Publish" + " (3)")
        #expect(S.appSocialComposeTitle == "New Post")
        #expect(S.commonOk == "OK")
        #expect(S.appSocialComposeOrder == "Order in post")
        #expect(S.appSocialComposeTrimBadge == "\u{2702} Trim")
    }

    @Test func trimmer() {
        #expect(S.appSocialTrimKeeping(kept: formatTimecode(12.5), total: formatTimecode(61)) == "Keeping 0:12.5 of 1:01.0")
        #expect(S.iosTrimPlayCut == "Play cut")
        #expect(S.appSocialTrimRemove == "Remove trim")
        #expect(S.iosTrimCantTitle == "Can't trim this video")
        #expect(S.appSocialTrimReading == "Reading the clip…")
        #expect(S.appSocialTrimTitle == "Trim video")
        #expect(S.commonApply == "Apply")
        #expect(S.appSocialTrimCantOpen == "This video could not be opened.")
        #expect(S.appSocialTrimNoLength == "This video's length could not be read.")
        #expect(S.iosTrimStartHandle == "Start of the cut")
        #expect(S.iosTrimEndHandle == "End of the cut")
    }
}
