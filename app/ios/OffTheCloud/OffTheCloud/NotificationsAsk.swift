// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI
import UIKit
import UserNotifications

/// IOS-07: notifications are asked for once, after the first accepted
/// sign-in to a device, with a line on what they bring
/// (app.shell.notifications_text) before iOS's own question - never at
/// launch, over Welcome, before there is a device to send any. As Android's
/// NotificationsAsk.kt.
enum NotificationsAsk {
    /// Outside the domain Log Out removes, as the tour's mark: asked once on
    /// this phone, whichever device comes next.
    static let suiteName = "cloud.off-the.OffTheCloud.prompts"
    static let askedKey = "notificationsAsked"

    /// Pure: whether the question shows now - signed in, the sync question
    /// answered, the tour closed, no card over the app, iOS not asked yet
    /// and the question not shown before.
    static func shouldAsk(signedIn: Bool, syncChoicePending: Bool, tourShown: Bool, mainVisible: Bool,
                          undecided: Bool, askedBefore: Bool) -> Bool {
        signedIn && !syncChoicePending && !tourShown && mainVisible && undecided && !askedBefore
    }

    /// Whether iOS lets the app show notifications: it then registers for
    /// them at every launch, as it always did.
    static func allowed(_ status: UNAuthorizationStatus) -> Bool {
        status == .authorized || status == .provisional || status == .ephemeral
    }

    static var defaults: UserDefaults { UserDefaults(suiteName: suiteName) ?? .standard }
}

/// RootView's question, over MainView. Shown the first time everything it
/// waits for holds; the mark is written as it shows.
struct NotificationsAskOverlay: ViewModifier {
    let configured: Bool
    let syncChoicePending: Bool
    @ObservedObject private var tour = TourModel.shared
    @ObservedObject private var connection = OTCConnection.shared
    @State private var showing = false

    init(configured: Bool, syncChoicePending: Bool) {
        self.configured = configured
        self.syncChoicePending = syncChoicePending
    }

    func body(content: Content) -> some View {
        content
            .task(id: inputs) { await evaluate() }
            .alert(S.appShellNotificationsTitle, isPresented: $showing) {
                Button(S.appShellNotificationsContinue) {
                    Task { await askIOS() }
                }
                Button(S.appShellNotificationsLater, role: .cancel) {}
            } message: {
                Text(S.appShellNotificationsText)
            }
    }

    private struct Inputs: Equatable {
        let signedIn: Bool
        let syncChoicePending: Bool
        let tourShown: Bool
        let mainVisible: Bool
    }

    private var inputs: Inputs {
        Inputs(
            signedIn: configured && connection.authenticated,
            syncChoicePending: syncChoicePending,
            tourShown: tour.shown,
            mainVisible: configured && connection.statusCode == nil && !connection.connectionFailed
        )
    }

    @MainActor
    private func evaluate() async {
        let now = inputs
        let defaults = NotificationsAsk.defaults
        // The cheap checks first: iOS is asked only when they hold.
        guard NotificationsAsk.shouldAsk(signedIn: now.signedIn, syncChoicePending: now.syncChoicePending, tourShown: now.tourShown,
                                         mainVisible: now.mainVisible, undecided: true,
                                         askedBefore: defaults.bool(forKey: NotificationsAsk.askedKey)) else { return }
        // The tour opens from the same change (TourOverlay): a beat for it,
        // then everything as it stands now - a change meanwhile cancels this
        // and asks again with the next one.
        try? await Task.sleep(for: .milliseconds(600))
        let status = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
        let current = inputs
        guard !Task.isCancelled, status == .notDetermined,
              NotificationsAsk.shouldAsk(signedIn: current.signedIn, syncChoicePending: current.syncChoicePending, tourShown: tour.shown,
                                         mainVisible: current.mainVisible, undecided: true,
                                         askedBefore: defaults.bool(forKey: NotificationsAsk.askedKey)) else { return }
        defaults.set(true, forKey: NotificationsAsk.askedKey)
        showing = true
    }

    @MainActor
    private func askIOS() async {
        let granted = (try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])) ?? false
        if granted { UIApplication.shared.registerForRemoteNotifications() }
    }
}
