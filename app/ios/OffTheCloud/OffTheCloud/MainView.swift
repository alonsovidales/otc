import SwiftUI

struct MainView: View {
    @EnvironmentObject var secrets: SecretsStore
    @EnvironmentObject var upload: UploadModel

    var body: some View {
        ZStack(alignment: .bottom) {
            TabView {
                FriendshipsView()
                    .tabItem { Label("Profile", systemImage: "person.crop.circle") }

                SocialFeedView()
                    .tabItem { Label("Social", systemImage: "bubble.left.and.bubble.right") }

                FilesExplorerView(initialPath: "/")
                    .tabItem { Label("Files", systemImage: "folder") }

                PhotoGalleryView(deviceID: secrets.deviceId, localPhotosFolder: nil)
                    .tabItem { Label("Images", systemImage: "photo.on.rectangle") }

                SettingsView()
                    .tabItem { Label("Settings", systemImage: "gearshape") }
            }

            if upload.totalPending > 0 || upload.isUploading {
                UploadBar()
                    .padding(.horizontal, 12)
                    .padding(.bottom, 56) // sit just above the tab bar
            }
        }
    }
}
