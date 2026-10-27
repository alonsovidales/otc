// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Alerts (A16, the polling, the Accept): a list that failed says why with
/// Try again instead of "Nothing yet"; the unread count is asked for at
/// once, on a kick (a push, the app back in front) and otherwise only every
/// 45 s; an Accept the device didn't take says why. Android's AlertsTest is
/// the same.
@MainActor
struct AlertsTests {
    private func until(_ done: () -> Bool) async {
        for _ in 0..<150 where !done() {
            try? await Task.sleep(for: .milliseconds(20))
        }
    }

    @Test func aListThatFailedSaysWhyInsteadOfNothingYet() {
        #expect(NotificationsModel.body(listEmpty: true, loading: false, problem: .unreachable) == .problem)
        // Asked again meanwhile: still why, with Try again (Trying…).
        #expect(NotificationsModel.body(listEmpty: true, loading: true, problem: .failed) == .problem)
        #expect(NotificationsModel.body(listEmpty: true, loading: false, problem: nil) == .empty)
        #expect(NotificationsModel.body(listEmpty: true, loading: true, problem: nil) == .loading)
        // A list already shown stays while a refresh of it fails.
        #expect(NotificationsModel.body(listEmpty: false, loading: false, problem: .offline) == .list)
        #expect(S.appSocialAlertsLoadFailed == "Couldn't load your notifications")
        #expect(LoadProblem.offline.text(.alerts) == "This phone is offline. Your notifications will appear once it's back online.")
        #expect(LoadProblem.failed.text(.alerts) == "Your notifications will appear as soon as your device answers.")
    }

    @Test func aFailedLoadKeepsItsReasonThenTheListComes() async {
        let model = NotificationsModel()
        model.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: false, statusCode: nil) }
        model.request = { _ in throw WSClient.TimedOut() }
        await model.openPanel()
        #expect(model.loadProblem == .slow)
        #expect(model.notifications.isEmpty)
        var n = Msg_Notification()
        n.uuid = "n1"
        var list = Msg_RespNotifications()
        list.notifications = [n]
        model.request = { p in
            var r = Msg_RespEnvelope()
            if case .reqListNotifications = p { r.payload = .respNotifications(list) }
            return r
        }
        model.retryList()
        await until { !model.notifications.isEmpty }
        #expect(model.notifications.map(\.uuid) == ["n1"])
        #expect(model.loadProblem == nil)
    }

    @Test func anAcceptTheDeviceDidntTakeSaysWhy() async {
        let model = NotificationsModel()
        model.awaitingAnswer = ["ana.off-the.cloud"]
        var sent: [Msg_ReqEnvelope.OneOf_Payload] = []
        var demo = Msg_RespEnvelope()
        demo.error = true
        demo.errorMessage = "Demo"
        var ack = Msg_Ack()
        ack.errorMsg = "Demo"
        demo.payload = .respAck(ack)
        model.request = { p in sent.append(p); return demo }
        await model.accept(domain: "ana.off-the.cloud")
        #expect(model.notice == .device("Demo"))
        #expect(model.awaitingAnswer == ["ana.off-the.cloud"])
        guard case .reqChangeFriendStatus(let req) = sent.first else {
            Issue.record("no ChangeFriendStatus")
            return
        }
        #expect(req.domain == "ana.off-the.cloud")
        #expect(req.status == .accepted)
        model.request = { _ in throw URLError(.timedOut) }
        await model.accept(domain: "ana.off-the.cloud")
        #expect(model.notice == .acceptFailed)
        #expect(AlertsNotice.acceptFailed.text == "Couldn't accept the friend request")
        var okAck = Msg_Ack()
        okAck.ok = true
        var ok = Msg_RespEnvelope()
        ok.payload = .respAck(okAck)
        model.request = { _ in ok }
        await model.accept(domain: "ana.off-the.cloud")
        #expect(model.awaitingAnswer.isEmpty)
        #expect(model.acceptedHere == ["ana.off-the.cloud"])
    }

    @Test func theCountIsAskedAtOnceThenOnAKickNotEveryFewSeconds() async {
        var asks = 0
        let poll = CountPoll(wait: 60) { asks += 1 }
        poll.start()
        await until { asks == 1 }
        // Started again while running: nothing new.
        poll.start()
        try? await Task.sleep(for: .milliseconds(100))
        #expect(asks == 1)
        // A push (or the app back in front) asks at once, not in a minute.
        poll.kick()
        await until { asks == 2 }
        #expect(asks == 2)
        poll.stop()
        poll.kick()
        try? await Task.sleep(for: .milliseconds(100))
        #expect(asks == 2)
        poll.start()
        await until { asks == 3 }
        #expect(asks == 3)
        poll.stop()
        #expect(NotificationsModel.pollSeconds >= 30 && NotificationsModel.pollSeconds <= 60)
    }

    @Test func aKickDuringTheAskAsksOnceMore() async {
        var asks = 0
        var release: CheckedContinuation<Void, Never>?
        let poll = CountPoll(wait: 60) {
            asks += 1
            if asks == 1 { await withCheckedContinuation { release = $0 } }
        }
        poll.start()
        await until { release != nil }
        poll.kick()
        release?.resume()
        await until { asks == 2 }
        #expect(asks == 2)
        poll.stop()
    }
}
