import SwiftUI

struct MainView: View {
    @EnvironmentObject var secrets: SecretsStore
    @EnvironmentObject var upload: UploadModel
    @State private var showSettings = false
    
    var body: some View {
        NavigationView {
            ZStack(alignment: .bottom) {
                WebContainerView(
                    config: WebConfig(
                        endpoint: secrets.endpoint,
                        password: secrets.password,
                        deviceId: secrets.deviceId,
                    ),
                    showSettings: $showSettings
                )

                if upload.totalPending > 0 || upload.isUploading {
                    UploadBar()
                        .padding(.horizontal, 12)
                        .padding(.bottom, 12)   
                }
            }
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button {
                        showSettings = true
                    } label: {
                        Image(systemName: "gearshape.fill")
                    }
                }
            }
            .sheet(isPresented: $showSettings) {
                SettingsView()
                    .environmentObject(secrets)
                    .environmentObject(upload)
            }
        }
    }
}
    
