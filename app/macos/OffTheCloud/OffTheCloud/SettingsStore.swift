// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Combine

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
