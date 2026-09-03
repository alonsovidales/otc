import SwiftUI

@main
struct CloudSyncApp: App {
    var body: some Scene {
        // One popover-only UI in the menu bar. PopoverView reads
        // SettingsStore.shared/SyncModel.shared directly as @ObservedObject,
        // so there's no need to inject them via .environmentObject() here —
        // see PopoverView's declaration comment for why that switch was made.
        MenuBarExtra("Off The Cloud", systemImage: "server.rack") {
            PopoverView()
        }
        .menuBarExtraStyle(.window) // resizable popover
    }
}
