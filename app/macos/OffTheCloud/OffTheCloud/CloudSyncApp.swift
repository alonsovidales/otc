import SwiftUI

@main
struct CloudSyncApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    // Singletons kept alive for the whole app
    @StateObject private var settings  = SettingsStore.shared
    @StateObject private var syncModel = SyncModel.shared

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

// Issue #37: sync has to actually run in the background from launch, not
// only once the user opens the menu bar popover — PopoverView.onAppear
// (still there as a harmless, idempotent safety net) previously was the
// *only* place this got kicked off, so a folder added in a previous
// session would just sit there un-synced until someone happened to click
// the menu bar icon.
final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        SyncModel.shared.bind(settings: SettingsStore.shared)
    }
}
