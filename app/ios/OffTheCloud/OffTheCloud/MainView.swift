// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI

struct MainView: View {
    @EnvironmentObject var secrets: SecretsStore
    @EnvironmentObject var upload: UploadModel
    @EnvironmentObject var notifications: NotificationsModel
    // Issue #78: bare-TabView-with-no-selection couldn't be switched
    // programmatically at all - a tapped notification needs to be able to
    // jump to Social or Profile on its own, not just rely on the user
    // already being there.
    @State private var selectedTab = 0

    var body: some View {
        ZStack(alignment: .bottom) {
            TabView(selection: $selectedTab) {
                SocialFeedView()
                    .tabItem { Label("Social", systemImage: "bubble.left.and.bubble.right") }
                    .tag(0)

                FriendshipsView()
                    .tabItem { Label("Profile", systemImage: "person.crop.circle") }
                    .tag(1)

                FilesExplorerView(initialPath: "/")
                    .tabItem { Label("Files", systemImage: "folder") }
                    .tag(2)

                PhotoGalleryView(deviceID: secrets.deviceId, localPhotosFolder: nil)
                    .tabItem { Label("Images", systemImage: "photo.on.rectangle") }
                    .tag(3)

                SettingsView()
                    .tabItem { Label("Settings", systemImage: "gearshape") }
                    .tag(4)
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

            // Issue #78: top-leading overlay, mirroring how UploadBar above
            // is already a top-level overlay on this same ZStack (just
            // anchored opposite) - none of the 5 tabs share a nav bar this
            // could live in as a single toolbar item instead.
            VStack {
                HStack {
                    NotificationsBellButton(model: notifications)
                    Spacer()
                }
                Spacer()
            }
            .padding(.top, 4)
            .padding(.leading, 8)
        }
        // A like/comment notification is handled by SocialFeedView itself
        // (it observes `notifications.pendingDeepLink` directly); this
        // just handles the tab switch itself, plus the .friendRequests
        // case, which has no view-local state of its own to act on.
        .onChange(of: notifications.pendingDeepLink) { _, link in
            switch link {
            case .post:
                selectedTab = 0
            case .friendRequests:
                selectedTab = 1
                notifications.pendingDeepLink = nil
            case nil:
                break
            }
        }
    }
}
