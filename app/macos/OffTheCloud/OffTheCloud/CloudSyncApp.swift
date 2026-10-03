// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI

@main
struct CloudSyncApp: App {
    init() {
        // Connect and start syncing at launch. This used to happen in
        // PopoverView.onAppear, and MenuBarExtra doesn't build its content
        // until the icon is clicked - so after login the app sat idle,
        // not even connected, until someone opened the popover.
        Task { @MainActor in
            SyncModel.shared.bind(settings: SettingsStore.shared)
        }
    }

    var body: some Scene {
        // One popover-only UI in the menu bar. PopoverView reads
        // SettingsStore.shared/SyncModel.shared directly as @ObservedObject,
        // so there's no need to inject them via .environmentObject() here —
        // see PopoverView's declaration comment for why that switch was made.
        // Issue #69: the icon itself reports the RAID - see RaidMenuIcon.
        // A drawn label rather than a system image, so the two drives can
        // take different colours. Rendered as an image so the menu bar
        // gets a fixed-size bitmap rather than live view content.
        MenuBarExtra {
            PopoverView()
        } label: {
            MenuBarLabel()
        }
        .menuBarExtraStyle(.window) // resizable popover

        // Issue #184: the new-device wizard, opened from the popover. Never
        // at launch - this is a menu bar app.
        Window("Set Up a New Device", id: "setup") {
            SetupWizardView()
        }
        .windowResizability(.contentSize)
        .defaultPosition(.center)
        .defaultLaunchBehavior(.suppressed)
    }
}


/// The menu bar label: the RAID icon, re-rendered whenever the health
/// changes. MenuBarExtra's label must resolve to an Image on macOS (view
/// content isn't laid out in the status bar), so the Canvas is rendered
/// to one here.
private struct MenuBarLabel: View {
    @ObservedObject private var sync = SyncModel.shared

    var body: some View {
        if let cg = Self.render(sync.raidHealth) {
            Image(cg, scale: 2, label: Text(sync.raidHealth.summary))
        } else {
            Image(systemName: "server.rack")
        }
    }

    private static func render(_ health: RaidHealth) -> CGImage? {
        let renderer = ImageRenderer(content: RaidMenuIcon(health: health))
        renderer.scale = 2
        return renderer.cgImage
    }
}
