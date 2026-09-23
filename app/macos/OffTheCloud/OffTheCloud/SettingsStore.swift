// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Combine
import ServiceManagement

/// Persisted settings. Changes trigger re-connect/sync automatically.
@MainActor
final class SettingsStore: ObservableObject {
    // Issue #37: shared so AppDelegate can bind sync at launch, before
    // (and independent of) the menu bar popover ever being opened.
    static let shared = SettingsStore()

    // Issue #101 is about the browser keeping the account password in
    // plain localStorage, but this app had exactly the same problem in its
    // own store: the password went straight into UserDefaults, i.e. a
    // plain, unencrypted plist under ~/Library/Preferences that any
    // process running as this user (and any backup of the home directory)
    // can read. It lives in the Keychain now, same as the iOS app already
    // did (see SecretsStore.swift, which this Keychain helper is lifted
    // from) — the domain, which isn't a secret, stays in UserDefaults.
    private static let cPasswordKey = "password"
    private static let cDomainKey = "domain"

    // Issue #121: most devices are reached through the public bridge, where
    // the address is entirely determined by the device's name - so the
    // form asks for the name and builds the rest, and only someone with a
    // device elsewhere (their own bridge, Tailscale, the LAN) types an
    // address. The stored `domain` stays what WSClient.configure already
    // takes: a bare host for the bridge, or a full URL for anything else.
    static let bridgeDomain = "off-the.cloud"

    static func bridgeDomain(forName name: String) -> String {
        name.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() + "." + bridgeDomain
    }

    /// The device name if `domain` is a plain bridge host, else nil - which
    /// is how the form knows to open the custom field instead.
    static func bridgeName(fromDomain domain: String) -> String? {
        var d = domain.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        // A full bridge URL someone typed before this existed is still just
        // a name: wss://cala.off-the.cloud/ws is "cala". Anything with a
        // port, a non-wss scheme or another path is genuinely custom.
        if d.hasPrefix("wss://") {
            d.removeFirst("wss://".count)
            if d.hasSuffix("/ws") { d.removeLast("/ws".count) }
            d = d.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        }
        guard !d.contains("://"), !d.contains("/"), !d.contains(":"),
              d.hasSuffix("." + bridgeDomain) else { return nil }
        let name = String(d.dropLast(bridgeDomain.count + 1))
        return name.isEmpty || name.contains(".") ? nil : name
    }

    // Start at login, like a sync client is expected to (the Windows and
    // Linux clients register themselves too). On by default the first
    // time the app runs; the toggle in Settings turns it off.
    private static let cLoginItemKey = "startAtLogin"
    @Published var startAtLogin: Bool {
        didSet {
            UserDefaults.standard.set(startAtLogin, forKey: Self.cLoginItemKey)
            LoginItem.set(enabled: startAtLogin)
        }
    }

    @Published var domain: String {
        didSet { save() }
    }
    @Published var password: String {
        didSet { save() }
    }

    init() {
        domain = UserDefaults.standard.string(forKey: Self.cDomainKey) ?? ""
        password = Keychain.loadString(key: Self.cPasswordKey)
            ?? Self.migrateLegacyPlaintextPassword()
            ?? ""
        if UserDefaults.standard.object(forKey: Self.cLoginItemKey) == nil {
            startAtLogin = true
            UserDefaults.standard.set(true, forKey: Self.cLoginItemKey)
            LoginItem.set(enabled: true)
        } else {
            startAtLogin = UserDefaults.standard.bool(forKey: Self.cLoginItemKey)
            // Re-assert it, so a registration the system dropped (an app
            // moved on disk, say) comes back without anyone noticing.
            if startAtLogin { LoginItem.set(enabled: true) }
        }
    }

    /// One-off migration for an install that still has its password in
    /// UserDefaults from before this moved to the Keychain: move it over
    /// and delete the plaintext copy, so an already-configured app doesn't
    /// silently lose its connection *and* doesn't keep the plaintext
    /// lying around either. Returns nil when there was nothing to migrate.
    private static func migrateLegacyPlaintextPassword() -> String? {
        guard let legacy = UserDefaults.standard.string(forKey: cPasswordKey), !legacy.isEmpty else {
            UserDefaults.standard.removeObject(forKey: cPasswordKey)
            return nil
        }
        Keychain.saveString(key: cPasswordKey, value: legacy)
        UserDefaults.standard.removeObject(forKey: cPasswordKey)
        return legacy
    }

    private func save() {
        UserDefaults.standard.set(domain, forKey: Self.cDomainKey)
        Keychain.saveString(key: Self.cPasswordKey, value: password)
    }

    var ready: Bool { !domain.isEmpty && !password.isEmpty }
}

// Tiny Keychain helper — same shape as the iOS app's own (SecretsStore.swift).
enum Keychain {
    static func saveString(key: String, value: String) {
        let data = Data(value.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrAccount as String: key,
            kSecAttrService as String: "OffTheCloud",
            kSecValueData as String: data
        ]
        SecItemDelete(query as CFDictionary)
        SecItemAdd(query as CFDictionary, nil)
        // Never log `value` here — this is the account password.
    }

    static func loadString(key: String) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrAccount as String: key,
            kSecAttrService as String: "OffTheCloud",
            kSecReturnData as String: kCFBooleanTrue!,
            kSecMatchLimit as String: kSecMatchLimitOne
        ]
        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        if status == errSecSuccess, let data = item as? Data {
            return String(data: data, encoding: .utf8)
        }
        return nil
    }
}

/// SMAppService is macOS 13+'s way for an app to start itself at login -
/// no helper bundle, and the user can see and change it under System
/// Settings > General > Login Items.
enum LoginItem {
    static func set(enabled: Bool) {
        do {
            if enabled {
                if SMAppService.mainApp.status != .enabled { try SMAppService.mainApp.register() }
            } else {
                if SMAppService.mainApp.status == .enabled { try SMAppService.mainApp.unregister() }
            }
        } catch {
            print("Login item:", error)
        }
    }
}
