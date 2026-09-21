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

    static func loadOrCreate() -> SecretsStore {
        let endpoint = Keychain.loadString(key: "endpoint") ?? ""
        let password = Keychain.loadString(key: "password") ?? ""
        let deviceId = Keychain.loadString(key: "device_id") ?? {
            let id = UUID().uuidString
            Keychain.saveString(key: "device_id", value: id)
            return id
        }()

        let wifiOnly = UserDefaults.standard.bool(forKey: "wifiOnly")
        let includeVideos = UserDefaults.standard.object(forKey: "includeVideos") as? Bool ?? true
        let downloadFromiCloud = UserDefaults.standard.object(forKey: "downloadFromiCloud") as? Bool ?? true

        return SecretsStore(endpoint: endpoint, password: password, deviceId: deviceId, wifiOnly: wifiOnly, includeVideos: includeVideos, downloadFromiCloud: downloadFromiCloud)
    }

    func persist() {
        Keychain.saveString(key: "endpoint", value: endpoint)
        Keychain.saveString(key: "password", value: password)
        Keychain.saveString(key: "device_id", value: deviceId)
        UserDefaults.standard.set(wifiOnly, forKey: "wifiOnly")
        UserDefaults.standard.set(includeVideos, forKey: "includeVideos")
        UserDefaults.standard.set(downloadFromiCloud, forKey: "downloadFromiCloud")
    }
}

// Tiny Keychain helper
enum Keychain {
    static func saveString(key: String, value: String) {
        let data = Data(value.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrAccount as String: key,
            kSecAttrService as String: "OffTheCloud",
            kSecValueData as String: data
        ]
        let delStatus = SecItemDelete(query as CFDictionary)
        let addStatus = SecItemAdd(query as CFDictionary, nil)
        // Never log `value` here — this is also used for the account password.
        print("Keychain save key=\(key) deleteStatus=\(delStatus) addStatus=\(addStatus)")
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
