import SwiftUI

struct MainView: View {
    @EnvironmentObject var secrets: SecretsStore
    @EnvironmentObject var upload: UploadModel

    var body: some View {
        ZStack(alignment: .bottom) {
            TabView {
                SocialFeedView()
                    .tabItem { Label("Social", systemImage: "bubble.left.and.bubble.right") }

                FriendshipsView()
                    .tabItem { Label("Profile", systemImage: "person.crop.circle") }

                FilesExplorerView(initialPath: "/")
                    .tabItem { Label("Files", systemImage: "folder") }

                PhotoGalleryView(deviceID: secrets.deviceId, localPhotosFolder: nil)
                    .tabItem { Label("Images", systemImage: "photo.on.rectangle") }

                SettingsView()
                    .tabItem { Label("Settings", systemImage: "gearshape") }
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
    }
}
