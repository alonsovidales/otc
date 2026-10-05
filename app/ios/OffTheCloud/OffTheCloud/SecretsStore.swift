// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SecretsStore.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//


import Foundation
import Combine

final class SecretsStore: ObservableObject {
    @Published var endpoint: String
    @Published var password: String
    @Published var deviceId: String

    // Settings
    // These three auto-persist on every change (unlike endpoint/password/
    // deviceId above, which stay explicit-Save-button-only to avoid a
    // Keychain write per keystroke) — a toggle here used to only survive
    // an app relaunch if the user happened to also tap "Save Connection"/
    // "Sync Now"/"Sync All" afterward, since nothing else ever called
    // persist() for these.
    @Published var wifiOnly: Bool {
        didSet { persist() }
    }
    @Published var includeVideos: Bool {
        didSet { persist() }
    }
    @Published var downloadFromiCloud: Bool {
        didSet { persist() }
    }
    
    var isConfigured: Bool { !endpoint.isEmpty && !password.isEmpty }

    /// The endpoint as a URL the device will actually accept.
    ///
    /// People type the host and stop - "cala.off-the.cloud", or
    /// "wss://cala.off-the.cloud" - and the device's WebSocket lives at
    /// /ws, nowhere else. Without the path the bridge answers the
    /// handshake with its landing page, which URLSession reports as a
    /// bare "bad response from the server" (-1011) with nothing to say
    /// what was wrong. So the scheme and path are filled in here rather
    /// than demanded: no scheme becomes wss (a bare host is only ever a
    /// public name; the LAN case is typed with ws:// on purpose), and a
    /// missing or bare "/" path becomes /ws. Anything explicit is kept.
    static func normalizedEndpoint(_ raw: String) -> String {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !s.isEmpty else { return s }
        if !s.contains("://") {
            s = "wss://" + s
        }
        guard var parts = URLComponents(string: s) else { return s }
        if parts.path.isEmpty || parts.path == "/" {
            parts.path = "/ws"
        }
        return parts.string ?? s
    }

    /// endpoint, normalized - what every connection should dial.
    var endpointURLString: String { Self.normalizedEndpoint(endpoint) }

    // Issue #121: most devices are reached through the public bridge, where
    // the address is entirely determined by the device's name - so the
    // app asks for the name and builds the rest, and only someone with a
    // device elsewhere (their own bridge, Tailscale, the LAN) types an
    // address. These two turn a name into that address and back, so the
    // form can show whichever the stored endpoint really is.
    static let bridgeDomain = "off-the.cloud"

    static func bridgeEndpoint(forName name: String) -> String {
        "wss://" + name.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() + "." + bridgeDomain + "/ws"
    }

    /// The device name if `endpoint` is a plain bridge address, else nil -
    /// which is how the form knows to open the custom field instead.
    static func bridgeName(fromEndpoint endpoint: String) -> String? {
        guard let parts = URLComponents(string: normalizedEndpoint(endpoint)),
              parts.scheme == "wss", parts.port == nil,
              parts.path == "/ws", parts.query == nil,
              let host = parts.host, host.hasSuffix("." + bridgeDomain) else { return nil }
        let name = String(host.dropLast(bridgeDomain.count + 1))
        return name.isEmpty || name.contains(".") ? nil : name
    }

    private init(endpoint: String, password: String, deviceId: String, wifiOnly: Bool, includeVideos: Bool, downloadFromiCloud: Bool) {
        self.endpoint = endpoint
        self.password = password
        self.deviceId = deviceId
        self.wifiOnly = wifiOnly
        self.includeVideos = includeVideos
        self.downloadFromiCloud = downloadFromiCloud
    }

    /// Several threads call loadOrCreate at once (RootView, the
    /// connection, the photo sync, media URLs). Serializes the one-time
    /// migration and the device id's create-if-missing, so two callers
    /// can't both find it missing and each make one.
    private static let keychainLock = NSLock()

    static func loadOrCreate() -> SecretsStore {
        let deviceId: String = keychainLock.withLock {
            // Items saved before they were made readable after the first
            // unlock: updated once, while the phone is unlocked, so a
            // background launch with the phone locked (the photo sync)
            // still reads them - it used to read nothing and show the
            // connection screen as if the device had been forgotten.
            if !UserDefaults.standard.bool(forKey: "keychainAfterFirstUnlock"), Keychain.readable {
                for key in ["endpoint", "password", "device_id", "setup_endpoint", "setup_password", "last_endpoint", "last_password"] {
                    Keychain.makeReadableAfterFirstUnlock(key: key)
                }
                UserDefaults.standard.set(true, forKey: "keychainAfterFirstUnlock")
            }
            return Keychain.loadString(key: "device_id") ?? {
                let id = UUID().uuidString
                // Never replace the stored id because it couldn't be read.
                if Keychain.readable { Keychain.saveString(key: "device_id", value: id) }
                return id
            }()
        }
        // Writes update in place, so these are never briefly missing.
        let endpoint = Keychain.loadString(key: "endpoint") ?? ""
        let password = Keychain.loadString(key: "password") ?? ""

        let wifiOnly = UserDefaults.standard.bool(forKey: "wifiOnly")
        let includeVideos = UserDefaults.standard.object(forKey: "includeVideos") as? Bool ?? true
        let downloadFromiCloud = UserDefaults.standard.object(forKey: "downloadFromiCloud") as? Bool ?? true

        return SecretsStore(endpoint: endpoint, password: password, deviceId: deviceId, wifiOnly: wifiOnly, includeVideos: includeVideos, downloadFromiCloud: downloadFromiCloud)
    }

    /// Log Out (SettingsView): forget the connection and wipe everything
    /// this phone holds about the device - the Keychain items, the
    /// defaults, the sync history, the caches - and start over with a fresh
    /// device id, so the next sign-in looks like a first install. Nothing
    /// on the device itself is touched. Clearing endpoint/password is what
    /// flips RootView back to onboarding.
    func logOut() {
        Keychain.delete(key: "endpoint")
        Keychain.delete(key: "password")
        // device_id is replaced in place below, never deleted: a reader
        // landing in between would make (and save) an id of its own.
        Self.clearPendingSetup()
        if let domain = Bundle.main.bundleIdentifier {
            UserDefaults.standard.removePersistentDomain(forName: domain)
        }
        let fm = FileManager.default
        var dirs = [fm.temporaryDirectory]
        for kind in [FileManager.SearchPathDirectory.cachesDirectory, .applicationSupportDirectory] {
            dirs += fm.urls(for: kind, in: .userDomainMask)
        }
        for dir in dirs {
            for item in (try? fm.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? [] {
                try? fm.removeItem(at: item)
            }
        }
        endpoint = ""
        password = ""
        wifiOnly = false
        includeVideos = true
        downloadFromiCloud = true
        deviceId = UUID().uuidString
        Keychain.saveString(key: "device_id", value: deviceId)
    }

    /// A device set up from this phone whose install may still be running
    /// (BluetoothSetupView saves it as soon as the wizard has the address
    /// and password): Onboarding fills its form from it, and it goes once
    /// the app is configured.
    static func savePendingSetup(endpoint: String, password: String) {
        Keychain.saveString(key: "setup_endpoint", value: endpoint)
        Keychain.saveString(key: "setup_password", value: password)
    }

    static func pendingSetup() -> (endpoint: String, password: String)? {
        guard let e = Keychain.loadString(key: "setup_endpoint"), let p = Keychain.loadString(key: "setup_password"), !p.isEmpty else { return nil }
        return (e, p)
    }

    static func clearPendingSetup() {
        Keychain.delete(key: "setup_endpoint")
        Keychain.delete(key: "setup_password")
    }

    /// The device this phone left from the "isn't available" or "can't
    /// connect" screens: leaving wipes everything else, but a device that
    /// is down for a while (a restart, a re-image) is usually the one the
    /// phone comes back to, so Onboarding fills its form from it.
    static func saveLastDevice(endpoint: String, password: String) {
        guard !endpoint.isEmpty else { return }
        Keychain.saveString(key: "last_endpoint", value: endpoint)
        Keychain.saveString(key: "last_password", value: password)
    }

    static func lastDevice() -> (endpoint: String, password: String)? {
        guard let e = Keychain.loadString(key: "last_endpoint"), !e.isEmpty else { return nil }
        return (e, Keychain.loadString(key: "last_password") ?? "")
    }

    static func clearLastDevice() {
        Keychain.delete(key: "last_endpoint")
        Keychain.delete(key: "last_password")
    }

    func persist() {
        if isConfigured {
            Self.clearPendingSetup()
            Self.clearLastDevice()
        }
        Keychain.saveString(key: "endpoint", value: endpoint)
        Keychain.saveString(key: "password", value: password)
        // Not device_id: only loadOrCreate and logOut set it, and both save
        // it. A store built while the Keychain was locked holds a
        // throwaway id that must not replace the real one here.
        UserDefaults.standard.set(wifiOnly, forKey: "wifiOnly")
        UserDefaults.standard.set(includeVideos, forKey: "includeVideos")
        UserDefaults.standard.set(downloadFromiCloud, forKey: "downloadFromiCloud")
    }
}

// Tiny Keychain helper
enum Keychain {
    /// False while the phone is locked before its first unlock (or, for
    /// items from before the change above, while it is locked at all).
    static var readable: Bool {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrAccount as String: "device_id",
            kSecAttrService as String: "OffTheCloud",
            kSecReturnData as String: kCFBooleanTrue!,
            kSecMatchLimit as String: kSecMatchLimitOne
        ]
        var item: CFTypeRef?
        return SecItemCopyMatching(query as CFDictionary, &item) != errSecInteractionNotAllowed
    }

    /// Updates in place, adding only when the item doesn't exist. It used
    /// to delete and re-add: a concurrent reader could find the item
    /// missing (and loadOrCreate then made a new device id), and a save
    /// while the Keychain was locked deleted the item and failed to add
    /// it back.
    static func saveString(key: String, value: String) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrAccount as String: key,
            kSecAttrService as String: "OffTheCloud",
        ]
        let attrs: [String: Any] = [
            kSecValueData as String: Data(value.utf8),
            // Readable after the first unlock (until a restart), not only
            // while unlocked: the background photo sync runs with the
            // phone locked.
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        var status = SecItemUpdate(query as CFDictionary, attrs as CFDictionary)
        if status == errSecItemNotFound {
            status = SecItemAdd(query.merging(attrs) { $1 } as CFDictionary, nil)
        }
        // Never log `value` here — this is also used for the account password.
        print("Keychain save key=\(key) status=\(status)")
    }

    /// The accessibility change alone, for the one-time migration; no
    /// secret is read into memory for it.
    static func makeReadableAfterFirstUnlock(key: String) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrAccount as String: key,
            kSecAttrService as String: "OffTheCloud",
        ]
        let attrs: [String: Any] = [
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        let status = SecItemUpdate(query as CFDictionary, attrs as CFDictionary)
        print("Keychain migrate key=\(key) status=\(status)")
    }

    static func delete(key: String) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrAccount as String: key,
            kSecAttrService as String: "OffTheCloud"
        ]
        SecItemDelete(query as CFDictionary)
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
        print("Keychain load key=\(key) status=\(status)")
        if status == errSecSuccess, let data = item as? Data {
            return String(data: data, encoding: .utf8)
        }
        return nil
    }
}

