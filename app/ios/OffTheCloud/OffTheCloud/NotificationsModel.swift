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
import Combine

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

    /// The count's wait between asks while the app is in front: a push or
    /// a return to the front asks at once (Android's POLL_MS). It used to
    /// be asked for every 5 s whether or not anything could have changed.
    nonisolated static let pollSeconds: TimeInterval = 45

    @Published var unacknowledgedCount: Int = 0
    @Published var notifications: [Msg_Notification] = []
    @Published var loadingList = false
    /// The last load of the list failed, and why (nil: it didn't) - said
    /// in place of "Nothing yet", with Try again.
    @Published private(set) var loadProblem: LoadProblem?
    /// A short message at the top of Alerts (an Accept the device didn't
    /// take), worded when shown.
    @Published private(set) var notice: AlertsNotice?
    @Published var pendingDeepLink: DeepLink?
    /// Domains whose friend request is still waiting for this owner: a
    /// friend-request alert from one of them gets an Accept button.
    @Published var awaitingAnswer: Set<String> = []
    /// Accepted from Alerts during this open, shown as "Friends".
    @Published var acceptedHere: Set<String> = []

    /// Sends Alerts' requests. Tests answer in the device's place.
    var request: @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope = { payload in
        try await OTCConnection.shared.request { $0.payload = payload }
    }
    /// Whether the phone has a network now. Tests decide in the phone's place.
    var online: @MainActor () -> Bool = { !NetworkWatch.shared.offline }
    /// Why a load failed. Tests decide in the phone's place.
    var problemOf: @MainActor (Msg_RespEnvelope?, Error?) -> LoadProblem = { LoadProblem.now(resp: $0, error: $1) }

    private lazy var poll = CountPoll(wait: Self.pollSeconds) { [weak self] in await self?.fetchCount() }
    /// A list that failed is asked for again, 1 s doubling to 10 s and at
    /// once on a wake, while Alerts shows (Android's listFailed).
    let listRetry = PageRetry()
    private var panelShown = false
    private var watches: Set<AnyCancellable> = []
    private var notices = 0

    init() {
        OTCConnection.shared.$wakes
            .dropFirst()
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.listRetry.wake() }
            .store(in: &watches)
        // Back in front: the count now (Android's ON_RESUME).
        NotificationCenter.default.publisher(for: UIApplication.didBecomeActiveNotification)
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.cameToFront() }
            .store(in: &watches)
    }

    /// The app came to the front: the count now, then every pollSeconds.
    func startPolling() {
        if poll.running { poll.kick() } else { poll.start() }
    }

    func stopPolling() {
        poll.stop()
    }

    private func cameToFront() {
        if poll.running { poll.kick() }
    }

    /// A push arrived while the app is open (the app delegate's
    /// willPresent): something new on the device - the count at once, and
    /// the list too while Alerts shows it, rather than at the next round of
    /// the poll.
    func pushArrived() {
        if poll.running { poll.kick() }
        if panelShown { Task { await openPanel() } }
    }

    /// Log Out: nothing unread, nothing listed, nothing to jump to.
    func reset() {
        stopPolling()
        listRetry.reset()
        unacknowledgedCount = 0
        notifications = []
        loadingList = false
        loadProblem = nil
        notice = nil
        pendingDeepLink = nil
    }

    private func fetchCount() async {
        guard let resp = try? await request(.reqGetNotificationCount(.init())) else { return }
        if case .respNotificationCount(let c) = resp.payload {
            unacknowledgedCount = Int(c.unacknowledgedCount)
        }
    }

    /// Alerts appeared (`shown`) or went: a list that failed is asked for
    /// again only while it shows.
    func panelShown(_ shown: Bool) {
        panelShown = shown
        if !shown { listRetry.reset() }
    }

    // Issue #78: "when the user opens the section all the notifications
    // will change to acknowledged" - fetch the list, then immediately
    // mark everything read and zero the badge, rather than waiting for
    // the next poll tick. A list that doesn't come says why (it used to
    // read "Nothing yet").
    func openPanel() async {
        loadingList = true
        defer { loadingList = false }
        if let problem = loadProblem { loadProblem = problem.backOnline(online()) }
        var l = Msg_ReqListNotifications()
        l.limit = 50
        let resp: Msg_RespEnvelope
        do {
            resp = try await request(.reqListNotifications(l))
        } catch {
            listFailed(problemOf(nil, error))
            return
        }
        guard case .respNotifications(let r) = resp.payload else {
            listFailed(problemOf(resp, nil))
            return
        }
        listRetry.reset()
        loadProblem = nil
        notifications = r.notifications
        await loadAwaitingAnswer()
        unacknowledgedCount = 0
        _ = try? await request(.reqMarkNotificationsAcknowledged(.init()))
    }

    /// The list's Try again: asked for now, the backoff from the start.
    func retryList() {
        listRetry.reset()
        Task { await openPanel() }
    }

    private func listFailed(_ problem: LoadProblem) {
        loadProblem = problem
        guard panelShown else { return }
        listRetry.failed { [weak self] in
            Task { [weak self] in
                guard let self, self.panelShown else { return }
                await self.openPanel()
            }
        }
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
              let resp = try? await request(.reqFriendshipsList(Msg_FriendshipsList())),
              case .respFriendships(let f) = resp.payload else { return }
        awaitingAnswer = Set(f.friendships.filter { $0.status == .pending && !$0.sent }.map { $0.originProfile.domain })
    }

    /// Accept, straight from a friend-request alert; one the device didn't
    /// take says why (it used to do nothing).
    func accept(domain: String) async {
        var req = Msg_ChangeFriendStatus()
        req.domain = domain
        req.status = .accepted
        do {
            let resp = try await request(.reqChangeFriendStatus(req))
            guard SocialFeedViewModel.ackOK(resp) else {
                say(SocialFeedViewModel.refusal(resp).map(AlertsNotice.device) ?? .acceptFailed)
                return
            }
        } catch {
            say(.acceptFailed)
            return
        }
        awaitingAnswer.remove(domain)
        acceptedHere.insert(domain)
    }

    /// Shows `n` at the top of Alerts for a few seconds, longer for a long
    /// sentence (the device's words), and says it to VoiceOver.
    private func say(_ n: AlertsNotice) {
        notices += 1
        let mine = notices
        notice = n
        let m = n.text
        Announce.polite(m)
        let seconds = max(2.5, Double(m.count) / 18)
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
            if self?.notices == mine { self?.notice = nil }
        }
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

    /// What Alerts shows in place of its list (Android's AlertsBody).
    enum Body: Equatable {
        case loading, problem, empty, list
    }

    /// A list that failed to load says why, with Try again - it used to
    /// read "Nothing yet" as if there were none. A list already shown stays
    /// while a refresh of it fails. Android's alertsBody.
    nonisolated static func body(listEmpty: Bool, loading: Bool, problem: LoadProblem?) -> Body {
        if !listEmpty { return .list }
        if problem != nil { return .problem }
        return loading ? .loading : .empty
    }
}

/// A short message at the top of Alerts, kept as what happened and worded
/// when it shows (as FriendsToast).
enum AlertsNotice: Equatable {
    case acceptFailed
    /// The device's own words, already in the app's language.
    case device(String)

    var text: String {
        switch self {
        case .acceptFailed: return S.appSocialAlertsAcceptFailed
        case .device(let words): return words
        }
    }
}

/// A count asked for at once and then every `wait` seconds while started,
/// and at once again when kicked (a push, the app back in front) - as
/// Android's CountPoll.
@MainActor
final class CountPoll {
    private let wait: TimeInterval
    private let fetch: @MainActor () async -> Void
    private var loop: Task<Void, Never>?
    private var nap: Task<Void, Never>?
    private var kicked = false

    init(wait: TimeInterval, fetch: @escaping @MainActor () async -> Void) {
        self.wait = wait
        self.fetch = fetch
    }

    var running: Bool { loop != nil }

    /// Started (the app came to the front): asked for now, then every
    /// wait. A no-op while running.
    func start() {
        guard loop == nil else { return }
        let wait = self.wait
        loop = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                self.kicked = false
                await self.fetch()
                // A kick during the fetch: again at once.
                if self.kicked || Task.isCancelled { continue }
                let nap = Task<Void, Never> { _ = try? await Task.sleep(nanoseconds: UInt64(wait * 1_000_000_000)) }
                self.nap = nap
                await nap.value
                self.nap = nil
            }
        }
    }

    func stop() {
        loop?.cancel()
        loop = nil
        nap?.cancel()
        nap = nil
    }

    /// Ask now, not at the end of the wait.
    func kick() {
        kicked = true
        nap?.cancel()
    }
}
