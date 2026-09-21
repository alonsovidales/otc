// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI

struct MainView: View {
    @EnvironmentObject var secrets: SecretsStore
    @EnvironmentObject var upload: UploadModel
    @EnvironmentObject var notifications: NotificationsModel
    @EnvironmentObject var social: SocialFeedViewModel
    // Issue #78: bare-TabView-with-no-selection couldn't be switched
    // programmatically at all - a tapped notification needs to be able to
    // jump to Social or Profile on its own, not just rely on the user
    // already being there. Issue #79: Social (not Notifications) is the
    // default landing tab - see routeInitialTabIfNeeded below for the
    // "fall back to Images if Social is empty" part.
    @State private var selectedTab = 1
    @State private var didRouteInitialTab = false
    // Issue #56: the bridge's verdict on whether this device is reachable
    // at all. Observed rather than stored so this clears itself as soon as
    // OTCConnection's own reconnect succeeds.
    @ObservedObject private var connection = OTCConnection.shared

    var body: some View {
        ZStack(alignment: .bottom) {
            // Every tab is wrapped in LazyTab - see its doc comment for why
            // the ones not showing must not be built at launch.
            TabView(selection: $selectedTab) {
                // Issue #78 follow-up: notifications became a section of
                // its own (was a header/toolbar bell+sheet) - leftmost tab,
                // per the user's explicit ask ("in the ios app, it should
                // be at the left").
                LazyTab(tag: 0, selection: $selectedTab) {
                    NotificationsListView(model: notifications)
                }
                .tabItem { Label("Alerts", systemImage: "bell.fill") }
                .badge(notifications.unacknowledgedCount)
                .tag(0)

                LazyTab(tag: 1, selection: $selectedTab) {
                    SocialFeedView()
                }
                .tabItem { Label("Social", systemImage: "bubble.left.and.bubble.right") }
                .tag(1)

                // Issue #84: Friendships moved from here into a sheet
                // presented by SocialFeedView's own toolbar (tag 2 left
                // unused rather than renumbering everything after it -
                // same convention as a removed proto field).
                LazyTab(tag: 3, selection: $selectedTab) {
                    FilesExplorerView(initialPath: "/")
                }
                .tabItem { Label("Files", systemImage: "folder") }
                .tag(3)

                LazyTab(tag: 4, selection: $selectedTab) {
                    PhotoGalleryView(deviceID: secrets.deviceId, localPhotosFolder: nil)
                }
                .tabItem { Label("Images", systemImage: "photo.on.rectangle") }
                .tag(4)

                LazyTab(tag: 5, selection: $selectedTab) {
                    SettingsView()
                }
                .tabItem { Label("Settings", systemImage: "gearshape") }
                .tag(5)
            }

            if (upload.totalPending > 0 || upload.isUploading) && !upload.suppressed {
                // Full-width hairline sitting right above the tab bar (issue
                // #14) — no side margins or card shadow, so it reads as a
                // thin status rule rather than a floating panel that could
                // cover another screen's own bottom UI (e.g. the Files tab's
                // Edit-mode selection toolbar, or the Images tab's
                // multi-select action bar). It only grows when tapped.
                UploadBar()
                    .padding(.bottom, 56) // sit right above the tab bar
            }
        }
        // A like/comment or friend-request notification is handled by
        // SocialFeedView itself now (it observes
        // `notifications.pendingDeepLink` directly, presenting its own
        // Friendships sheet for the latter since issue #84 removed that
        // tab) - this just switches to Social either way, so that view is
        // actually on screen to react to it.
        .onChange(of: notifications.pendingDeepLink) { _, link in
            switch link {
            case .post, .friendRequests:
                selectedTab = 1
            case nil:
                break
            }
        }
        // Issue #79: "if there is nothing in the social timeline, open the
        // images section by default instead" - a one-time launch decision,
        // not a standing redirect (didRouteInitialTab guards it so it never
        // fires again once the user has actually navigated). Checked both
        // on appear (covers the cache already having posts, so no need to
        // wait on the network) and whenever hasLoadedOnce flips true
        // (covers a cold launch with an empty/no cache, where the very
        // first server round-trip is what actually settles the question).
        .onAppear { routeInitialTabIfNeeded() }
        .onChange(of: social.hasLoadedOnce) { _, _ in routeInitialTabIfNeeded() }
        // Also cancels the pending routing decision if the user manually
        // taps a tab before the network resolves (this fires for our own
        // programmatic selectedTab=4 above too, but didRouteInitialTab is
        // already true by then, so it's a no-op in that case).
        .onChange(of: selectedTab) { _, _ in didRouteInitialTab = true }
        // Issue #56: covers the tabs rather than sitting inside one of
        // them - while the device can't be reached there is nothing behind
        // this worth interacting with, since every tab's content comes
        // from that device. Only for the two verdicts the bridge gives us
        // explicitly; an ordinary dropped connection stays silent and
        // reconnects in the background as it always did, because that
        // recovers in a second or two and is not worth a full-screen
        // interruption.
        .overlay {
            if let code = connection.statusCode {
                DeviceUnreachableView(message: connection.lastError ?? "", code: code)
                    .transition(.opacity)
            }
        }
        .animation(.default, value: connection.statusCode)
    }

    private func routeInitialTabIfNeeded() {
        guard !didRouteInitialTab else { return }
        if !social.posts.isEmpty {
            didRouteInitialTab = true // Social already has content - stay put.
        } else if social.hasLoadedOnce {
            selectedTab = 4 // Images
            didRouteInitialTab = true
        }
        // Otherwise: cache was empty and the network hasn't answered yet -
        // stay on Social (already the default) and re-check once it does.
    }
}


/// A tab whose content is not built until the tab is first selected.
///
/// TabView constructs every tab's view tree up front, whether or not it
/// is showing. That was the remaining cold-launch cost after the feed's
/// PostBox fix: each tab still held generated protobuf values in its views
/// - the Files rows' Msg_File, the people chips' Msg_Person, the alerts
/// list's [Msg_Notification] - and AttributeGraph builds a layout
/// descriptor for every one of those types by recursively walking its
/// fields (see PostBox's doc comment for the trace that showed this).
/// Doing it for five tabs' worth of types at once, before the first
/// frame, is what a cold start was waiting on; the runtime then caches
/// it, which is why only a cold start ever paid.
///
/// Deferring the build changes nothing else: a tab that isn't showing
/// never had its onAppear fire at launch anyway, so each one still loads
/// its data the first time it is selected, exactly as before. Once built,
/// a tab stays built, so switching back is instant.
private struct LazyTab<Content: View>: View {
    let tag: Int
    @Binding var selection: Int
    @ViewBuilder let content: () -> Content

    @State private var built = false

    var body: some View {
        if built || selection == tag {
            content()
                .onAppear { built = true }
        } else {
            // Something has to occupy the slot so the tab item exists;
            // it is never seen, since the tab isn't selected.
            Color.clear
        }
    }
}
