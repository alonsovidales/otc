import Foundation
import Combine

/// Persisted settings. Changes trigger re-connect/sync automatically.
@MainActor
final class SettingsStore: ObservableObject {
    @Published var domain: String {
        didSet { save() }
    }
    @Published var password: String {  // keep simple; swap to Keychain if you want
        didSet { save() }
    }

    init() {
        let d = UserDefaults.standard.string(forKey: "domain") ?? ""
        let p = UserDefaults.standard.string(forKey: "password") ?? ""
        domain = d
        password = p
    }

    private func save() {
        UserDefaults.standard.set(domain, forKey: "domain")
        UserDefaults.standard.set(password, forKey: "password")
    }

    var ready: Bool { !domain.isEmpty && !password.isEmpty }
}
