// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  NotificationsModel.swift
//  OffTheCloud
//
//  Issue #78: Instagram-style notifications - a bell button (see
//  NotificationsBellButton.swift) backed by this shared model, polling
//  the unread count the same hand-rolled Task-loop way
//  StatusViewModel/DeviceSettingsViewModel already do elsewhere in this
//  app (no Timer anywhere in this codebase - don't introduce one here
//  either). Instantiated once and environmentObject-injected from
//  RootView.swift, same wiring as UploadModel.
//

import SwiftUI

@MainActor
final class NotificationsModel: ObservableObject {
    static let shared = NotificationsModel()

    // Set when a notification row is tapped; MainView observes this to
    // switch tabs (and, for a post, forwards it into SocialFeedViewModel
    // to scroll to). Cleared once consumed.
    enum DeepLink: Equatable {
        case post(pubUuid: String, commentUuid: String?)
        case friendRequests
        /// Issue #183: an update alert - Settings, at its update section.
        case updates
    }

    @Published var unacknowledgedCount: Int = 0
    @Published var notifications: [Msg_Notification] = []
    @Published var loadingList = false
    @Published var pendingDeepLink: DeepLink?
    /// Domains whose friend request is still waiting for this owner: a
    /// friend-request alert from one of them gets an Accept button.
    @Published var awaitingAnswer: Set<String> = []
    /// Accepted from Alerts during this open, shown as "Friends".
    @Published var acceptedHere: Set<String> = []

    private let ws = OTCConnection.shared
    private var pollTask: Task<Void, Never>?

    func startPolling() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                await self.fetchCount()
                try? await Task.sleep(nanoseconds: 5_000_000_000)
            }
        }
    }

    func stopPolling() {
        pollTask?.cancel()
        pollTask = nil
    }

    /// Log Out: nothing unread, nothing listed, nothing to jump to.
    func reset() {
        stopPolling()
        unacknowledgedCount = 0
        notifications = []
        loadingList = false
        pendingDeepLink = nil
    }

    private func fetchCount() async {
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            req.payload = .reqGetNotificationCount(.init())
            e = req
        }) else { return }
        if case .respNotificationCount(let c) = resp.payload {
            unacknowledgedCount = Int(c.unacknowledgedCount)
        }
    }

    // Issue #78: "when the user opens the section all the notifications
    // will change to acknowledged" - fetch the list, then immediately
    // mark everything read and zero the badge, rather than waiting for
    // the next poll tick.
    func openPanel() async {
        loadingList = true
        defer { loadingList = false }
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            var l = Msg_ReqListNotifications()
            l.limit = 50
            req.payload = .reqListNotifications(l)
            e = req
        }) else { return }
        if case .respNotifications(let r) = resp.payload {
            notifications = r.notifications
        }
        await loadAwaitingAnswer()
        unacknowledgedCount = 0
        _ = try? await ws.request({ e in
            var req = ReqEnvelope()
            req.payload = .reqMarkNotificationsAcknowledged(.init())
            e = req
        })
    }

    /// A tapped push: the same place tapping it in Alerts opens - its
    /// post (and comment), or the friend requests. The keys are the
    /// bridge's (push.Target); a push without them just opens the app.
    func handlePushTap(_ userInfo: [AnyHashable: Any]) {
        let kind = userInfo["kind"] as? String ?? ""
        let pub = userInfo["pub_uuid"] as? String ?? ""
        let comment = userInfo["comment_uuid"] as? String ?? ""
        if kind == "friends" {
            pendingDeepLink = .friendRequests
        } else if !pub.isEmpty {
            pendingDeepLink = .post(pubUuid: pub, commentUuid: comment.isEmpty ? nil : comment)
        }
    }

    private func loadAwaitingAnswer() async {
        guard notifications.contains(where: { $0.type == .notificationFriendRequest }),
              let resp = try? await ws.request({ $0.payload = .reqFriendshipsList(Msg_FriendshipsList()) }),
              case .respFriendships(let f) = resp.payload else { return }
        awaitingAnswer = Set(f.friendships.filter { $0.status == .pending && !$0.sent }.map { $0.originProfile.domain })
    }

    /// Accept, straight from a friend-request alert.
    func accept(domain: String) async {
        var req = Msg_ChangeFriendStatus()
        req.domain = domain
        req.status = .accepted
        guard let resp = try? await ws.request({ $0.payload = .reqChangeFriendStatus(req) }),
              case .respAck(let ack) = resp.payload, ack.ok else { return }
        awaitingAnswer.remove(domain)
        acceptedHere.insert(domain)
    }

    func handleTap(_ n: Msg_Notification) {
        switch n.type {
        case .notificationFriendRequest, .notificationFriendAccepted:
            pendingDeepLink = .friendRequests
        case .notificationUpdate:
            pendingDeepLink = .updates
        default:
            guard !n.pubUuid.isEmpty else { return }
            pendingDeepLink = .post(pubUuid: n.pubUuid, commentUuid: n.commentUuid.isEmpty ? nil : n.commentUuid)
        }
    }
}
