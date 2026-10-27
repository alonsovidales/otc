// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// The feed's comments and deletes (IOS-14/A14, IOS-15) against a stand-in
/// device: a comment the device didn't take says why - its words, or ours -
/// and hands its text back; one it took asks for that post alone; a refused
/// delete says why. Android's SocialFeedActionsTest is the same.
@MainActor
struct SocialFeedActionsTests {
    private func post(_ uuid: String, comments: [String] = []) -> Msg_SocialPublication {
        var p = Msg_SocialPublication()
        p.uuid = uuid
        p.comments = comments.map { id in
            var c = Msg_Comment()
            c.commentUuid = id
            c.comment = "Nice"
            return c
        }
        return p
    }

    private var ok: Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        var ack = Msg_Ack()
        ack.ok = true
        r.payload = .respAck(ack)
        return r
    }

    /// What a demo device answers every change with: the envelope's error and the Ack's.
    private var demo: Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        r.error = true
        r.errorCode = "demo"
        r.errorMessage = "This is a demo device"
        var ack = Msg_Ack()
        ack.ok = false
        ack.errorMsg = "This is a demo device"
        ack.code = "demo"
        r.payload = .respAck(ack)
        return r
    }

    private func vm(_ posts: [Msg_SocialPublication]) -> SocialFeedViewModel {
        let vm = SocialFeedViewModel(autoLoad: false)
        vm.posts = posts
        return vm
    }

    @Test func aRefusedCommentGoesAndSaysTheDevicesWords() async {
        let vm = vm([post("p1")])
        var sent: [Msg_ReqEnvelope.OneOf_Payload] = []
        vm.socialRequest = { p in sent.append(p); return self.demo }
        #expect(await vm.addComment(pubUuid: "p1", text: "Nice") == false)
        // The placeholder went; the card puts the text back (false).
        #expect(vm.posts[0].comments.isEmpty)
        #expect(vm.notice == .device("This is a demo device"))
        #expect(vm.notice?.text == "This is a demo device")
        #expect(sent.count == 1)
    }

    @Test func aCommentWithNoAnswerSaysOurs() async {
        let vm = vm([post("p1")])
        vm.socialRequest = { _ in throw URLError(.networkConnectionLost) }
        #expect(await vm.addComment(pubUuid: "p1", text: "Nice") == false)
        #expect(vm.notice == .commentFailed)
        #expect(FeedNotice.commentFailed.text == "Couldn't post your comment")
    }

    @Test func aCommentThatWorkedAsksForThatPostAlone() async {
        let vm = vm([post("p1"), post("p2"), post("p3")])
        var sent: [Msg_ReqEnvelope.OneOf_Payload] = []
        let fresh = post("p2", comments: ["c1"])
        vm.socialRequest = { p in
            sent.append(p)
            if case .reqGetPublication = p {
                var r = Msg_RespEnvelope()
                var rp = Msg_RespPublication()
                rp.publication = fresh
                r.payload = .respPublication(rp)
                return r
            }
            return self.ok
        }
        var pagesAsked = 0
        vm.feedRequest = { _ in pagesAsked += 1; throw URLError(.badServerResponse) }
        #expect(await vm.addComment(pubUuid: "p2", text: "Nice"))
        #expect(sent.count == 2)
        guard sent.count == 2, case .reqGetPublication(let req) = sent[1] else {
            Issue.record("no ReqGetPublication: \(sent)")
            return
        }
        #expect(req.pubUuid == "p2")
        // Not the feed's pages, and the pending comment is the device's now.
        #expect(pagesAsked == 0)
        #expect(vm.posts.map(\.uuid) == ["p1", "p2", "p3"])
        #expect(vm.posts[1].comments.map(\.commentUuid) == ["c1"])
        #expect(vm.notice == nil)
    }

    @Test func aRefusedDeleteKeepsThePostAndSaysWhy() async {
        let vm = vm([post("p1", comments: ["c1"])])
        vm.socialRequest = { _ in self.demo }
        await vm.deletePublication("p1")
        #expect(vm.posts.count == 1)
        #expect(vm.notice == .device("This is a demo device"))
        var silent = Msg_RespEnvelope()
        silent.payload = .respAck(Msg_Ack())
        vm.socialRequest = { _ in silent }
        await vm.deleteComment("c1")
        #expect(vm.posts[0].comments.count == 1)
        #expect(vm.notice == .deleteCommentFailed)
        vm.socialRequest = { _ in throw URLError(.timedOut) }
        await vm.deletePublication("p1")
        #expect(vm.notice == .deleteFailed)
        #expect(FeedNotice.deleteFailed.text == "Couldn't delete the post")
        #expect(FeedNotice.deleteCommentFailed.text == "Couldn't delete the comment")
        // Taken: gone.
        vm.socialRequest = { _ in self.ok }
        await vm.deleteComment("c1")
        #expect(vm.posts[0].comments.isEmpty)
        await vm.deletePublication("p1")
        #expect(vm.posts.isEmpty)
    }

    @Test func theDevicesWords() {
        #expect(SocialFeedViewModel.refusal(demo) == "This is a demo device")
        #expect(SocialFeedViewModel.refusal(ok) == nil)
        #expect(SocialFeedViewModel.ackOK(ok))
        #expect(!SocialFeedViewModel.ackOK(demo))
        var onlyEnvelope = Msg_RespEnvelope()
        onlyEnvelope.error = true
        onlyEnvelope.errorMessage = "No"
        #expect(SocialFeedViewModel.refusal(onlyEnvelope) == "No")
    }
}
