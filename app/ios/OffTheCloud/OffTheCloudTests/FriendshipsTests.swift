// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Friends' list: never "No friendships yet." for a list that didn't come.
/// Android's FriendshipsView follows the same rules.
@MainActor
struct FriendshipsLoadTests {
    @Test func whatTheListShows() {
        typealias B = FriendshipsViewModel.Body
        #expect(FriendshipsViewModel.body(listEmpty: true, loading: true, problem: nil) == B.loading)
        #expect(FriendshipsViewModel.body(listEmpty: true, loading: false, problem: nil) == B.empty)
        #expect(FriendshipsViewModel.body(listEmpty: true, loading: false, problem: .unreachable) == B.problem)
        #expect(FriendshipsViewModel.body(listEmpty: true, loading: true, problem: .failed) == B.problem)
        // A list shown stays while a refresh of it fails.
        #expect(FriendshipsViewModel.body(listEmpty: false, loading: false, problem: .failed) == B.list)
    }

    @Test func aListThatDidntComeSaysSoAndComesOnTryAgain() async {
        let vm = FriendshipsViewModel()
        // Nothing asked yet: loading, not empty.
        #expect(vm.body == .loading)
        var answers: [Result<Msg_RespEnvelope, Error>] = []
        vm.request = { _ in try answers.removeFirst().get() }
        // The bridge's answer for a device it can't reach.
        var unreachable = Msg_RespEnvelope()
        var ack = Msg_Ack()
        ack.code = "device_unreachable"
        unreachable.payload = .respAck(ack)
        answers = [.success(unreachable)]
        await vm.reloadFriendships()
        #expect(vm.body == .problem)
        #expect(vm.loadProblem != nil)
        // No answer at all.
        answers = [.failure(WSClient.TimedOut())]
        await vm.reloadFriendships()
        #expect(vm.body == .problem)
        // Try again: the list comes.
        var f = Msg_Friendship()
        f.originProfile.domain = "pepe.off-the.cloud"
        f.status = .accepted
        var list = Msg_Friendships()
        list.friendships = [f]
        var ok = Msg_RespEnvelope()
        ok.payload = .respFriendships(list)
        answers = [.success(ok)]
        await vm.reloadFriendships()
        #expect(vm.body == .list)
        #expect(vm.loadProblem == nil)
        // Truly none: "No friendships yet."
        var none = Msg_RespEnvelope()
        none.payload = .respFriendships(Msg_Friendships())
        answers = [.success(none)]
        await vm.reloadFriendships()
        #expect(vm.body == .empty)
    }
}
