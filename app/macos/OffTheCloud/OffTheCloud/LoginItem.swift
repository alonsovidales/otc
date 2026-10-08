// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Combine
import ServiceManagement
import SwiftUI

// Start at login, only when the owner asks for it (App Store guideline
// 2.4.5(iii): a Mac App Store app may not start itself at login without the
// user's consent - 2.0 (17) was rejected for it). Version 1.0 registered
// itself the first time it ran and again at every launch; now nothing is
// registered until the owner says yes, either in the one-time offer shown
// once a device is connected (LoginItemOffer) or with Settings' "Start at
// login" checkbox. otc-sync does the same (internal/autostart, the tray's
// question after the first connection, `otc-sync autostart on|off|status`).

/// SMAppService's status, reduced to what the app acts on.
enum LoginItemState: Equatable {
    /// Registered and allowed to start at login.
    case enabled
    /// Registered, but switched off by the owner in System Settings >
    /// General > Login Items: the app leaves it that way.
    case requiresApproval
    /// Not registered (or the system can't find the app any more).
    case notRegistered
}

/// The registration itself: SMAppService.mainApp in the app, a fake in the
/// tests (which must never touch the owner's real login item).
protocol LoginItemService {
    var state: LoginItemState { get }
    func register() throws
    func unregister() throws
    /// System Settings > General > Login Items.
    func openSystemSettings()
}

/// SMAppService is macOS 13+'s way for an app to start itself at login -
/// no helper bundle, and the user can see and change it under System
/// Settings > General > Login Items.
struct MainAppLoginItem: LoginItemService {
    var state: LoginItemState {
        switch SMAppService.mainApp.status {
        case .enabled: return .enabled
        case .requiresApproval: return .requiresApproval
        default: return .notRegistered // .notRegistered, .notFound
        }
    }

    func register() throws { try SMAppService.mainApp.register() }
    func unregister() throws { try SMAppService.mainApp.unregister() }
    func openSystemSettings() { SMAppService.openSystemSettingsLoginItems() }
}

/// The owner's choice and the login item's real state.
///
/// - `startAtLoginChoice` (UserDefaults) is the recorded consent: true for
///   "Start at Login" (offer or checkbox), false for "Not Now" or the box
///   unticked, absent while never asked. Only true lets the app register.
/// - `startAtLogin`, 1.0's key, was written true by the app itself the first
///   time it ran, so it is not consent: `launch()` removes the registration
///   1.0 made and drops the key, and the offer asks once. A false there was
///   the owner unticking the box - an explicit no, kept as one.
@MainActor
final class LoginItemSettings: ObservableObject {
    static let shared = LoginItemSettings(defaults: .standard, service: MainAppLoginItem())

    static let choiceKey = "startAtLoginChoice"
    static let legacyKey = "startAtLogin"

    /// The checkbox: whether the app really starts at login now, as the
    /// system says - not what was last chosen.
    @Published private(set) var isOn = false
    /// Registered but switched off in System Settings.
    @Published private(set) var needsApproval = false
    /// The recorded choice, nil while never asked.
    @Published private(set) var choice: Bool?
    /// A device has connected since launch: the offer waits for it, so it
    /// comes when syncing is something the owner has seen work.
    @Published private(set) var connected = false

    private let defaults: UserDefaults
    private let service: LoginItemService

    init(defaults: UserDefaults, service: LoginItemService) {
        self.defaults = defaults
        self.service = service
        choice = defaults.object(forKey: Self.choiceKey) as? Bool
    }

    /// The one-time offer is due: never asked, and a device connected.
    var offerDue: Bool { choice == nil && connected }

    /// Settings' note under the checkbox: the owner said yes, then switched
    /// it off in System Settings.
    var offInSystemSettings: Bool { choice == true && needsApproval }

    /// At launch, once: carries 1.0's self-registration over, then puts
    /// back a registration the system dropped (an app moved on disk, say) -
    /// only for an owner who said yes, and never one they switched off in
    /// System Settings (`requiresApproval` is left alone).
    func launch() {
        migrateLegacy()
        if choice == true, service.state == .notRegistered {
            do { try service.register() } catch { print("Login item:", error) }
        }
        refresh()
    }

    private func migrateLegacy() {
        guard let legacy = defaults.object(forKey: Self.legacyKey) as? Bool else { return }
        if choice == nil {
            if legacy {
                // Registered by 1.0 without asking. Removed only if it is
                // there; on a failure the key stays and the next launch
                // tries again (the offer's "Not Now" removes it too).
                if service.state != .notRegistered {
                    do { try service.unregister() } catch {
                        print("Login item:", error)
                        return
                    }
                }
            } else {
                record(false)
            }
        }
        defaults.removeObject(forKey: Self.legacyKey)
    }

    /// The offer's answer or the checkbox: recorded either way, so the offer
    /// is not shown again. Yes registers (and, when the owner had switched
    /// it off in System Settings, opens Login Items there instead of
    /// fighting it); no removes the registration.
    func choose(_ on: Bool) {
        record(on)
        do {
            if on {
                if service.state == .notRegistered { try service.register() }
                if service.state == .requiresApproval { service.openSystemSettings() }
            } else if service.state != .notRegistered {
                try service.unregister()
            }
        } catch {
            print("Login item:", error)
        }
        refresh()
    }

    /// Re-reads the system's state: a change made in System Settings shows
    /// the next time the popover opens.
    func refresh() {
        let state = service.state
        if isOn != (state == .enabled) { isOn = state == .enabled }
        if needsApproval != (state == .requiresApproval) { needsApproval = state == .requiresApproval }
    }

    func openSystemSettings() { service.openSystemSettings() }

    /// SyncModel, at every connect.
    func deviceConnected() {
        if !connected { connected = true }
    }

    private func record(_ on: Bool) {
        defaults.set(on, forKey: Self.choiceKey)
        choice = on
    }
}

/// The words, the same as otc-sync's tray question (tray/autostart.go).
enum LoginItemText {
    static let title = "Start Off The Cloud when you log in?"
    static let message = "Your folders keep syncing in the background after you restart your Mac. You can change this in Settings."
    static let yes = "Start at Login"
    static let no = "Not Now"
    static let toggle = "Start at login"
    static let offInSystemSettings = "Start at login is switched off in System Settings."
    static let openLoginItems = "Open Login Items…"
}

/// The one-time offer, inline in the popover (and on the setup wizard's
/// last page): an .alert() would take key status from the MenuBarExtra
/// popover, which then closes (as Settings' Disconnect confirmation).
struct LoginItemOffer: View {
    /// Accent "Start at Login" in the popover, where the offer is the only
    /// question. The wizard's last page passes false: its "Done" is that
    /// page's one prominent button, and the offer's two answers sit level.
    var prominent = true
    let onChoose: (Bool) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(LoginItemText.title, systemImage: "power.circle")
                .font(.callout.bold())
                .fixedSize(horizontal: false, vertical: true)
            Text(LoginItemText.message)
                .font(.footnote)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                // No keyboard shortcut on either: consent is a click.
                Button(LoginItemText.no) { onChoose(false) }
                if prominent {
                    Button(LoginItemText.yes) { onChoose(true) }
                        .buttonStyle(.borderedProminent)
                } else {
                    Button(LoginItemText.yes) { onChoose(true) }
                        .buttonStyle(.bordered)
                }
            }
            .controlSize(.small)
        }
        .padding(8)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 10))
    }
}

/// Settings' "Start at login": the real state, and a change records the
/// choice.
struct StartAtLoginToggle: View {
    @ObservedObject var login: LoginItemSettings

    var body: some View {
        Toggle(LoginItemText.toggle, isOn: Binding(get: { login.isOn }, set: { login.choose($0) }))
            .toggleStyle(.checkbox)
            .font(.footnote)
    }
}

/// Under Settings' row, the width of the panel: the owner said yes, then
/// switched it off in System Settings - said so, with a way there.
struct StartAtLoginNote: View {
    @ObservedObject var login: LoginItemSettings

    var body: some View {
        if login.offInSystemSettings {
            HStack(spacing: 4) {
                Text(LoginItemText.offInSystemSettings)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Button(LoginItemText.openLoginItems) { login.openSystemSettings() }
                    .buttonStyle(.link)
                    .fixedSize()
                Spacer(minLength: 0)
            }
            .font(.caption)
        }
    }
}
