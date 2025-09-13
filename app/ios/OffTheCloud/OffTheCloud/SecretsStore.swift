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
    @Published var wifiOnly: Bool
    @Published var includeVideos: Bool
    @Published var downloadFromiCloud: Bool
    
    var isConfigured: Bool { !endpoint.isEmpty && !password.isEmpty }

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
        print("Saving string \(value) for key \(key)")
        let data = Data(value.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrAccount as String: key,
            kSecAttrService as String: "OffTheCloud",
            kSecValueData as String: data
        ]
        SecItemDelete(query as CFDictionary)
        SecItemAdd(query as CFDictionary, nil)
    }

    static func loadString(key: String) -> String? {
        print("Loading string for key \(key)")
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
