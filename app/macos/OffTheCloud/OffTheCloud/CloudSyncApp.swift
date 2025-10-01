import SwiftUI

@main
struct CloudSyncApp: App {
    // Singletons kept alive for the whole app
    @StateObject private var settings  = SettingsStore()
    @StateObject private var syncModel = SyncModel()

    var body: some Scene {
        // One popover-only UI in the menu bar
        MenuBarExtra("Off The Cloud", systemImage: "server.rack") {
            PopoverView()
                .environmentObject(settings)
                .environmentObject(syncModel)
        }
        .menuBarExtraStyle(.window) // resizable popover
    }
}
