// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FaceRecognition.swift
//  OffTheCloud
//
//  Whether face recognition is on (Settings.face_recognition_enabled, off
//  by default): People - the people in the search, a person in the Images
//  search, and the People page - is there only while it is. The web's
//  faceRecognition.ts, kept the same way: asked once the app is signed in
//  (MainView, and again whenever the app comes back to the front) and set
//  by Settings when its switch is turned, so People comes and goes at once.
//
//  Until the device has answered, the answer this phone had last time
//  stands in, so a launch neither shows People and then takes it away nor
//  the other way round. With no answer ever (a first sign-in) it is nil
//  and People stays hidden until the device says: it is off on most
//  devices, and People appearing is gentler than People vanishing.
//

import Foundation

@MainActor
final class FaceRecognition: ObservableObject {
    static let shared = FaceRecognition()

    /// true or false as the device said (or last said to this phone); nil:
    /// not known yet, so People stays hidden.
    @Published private(set) var enabled: Bool?

    /// Shorthand for the places that show People.
    var isOn: Bool { enabled == true }

    private static let key = "faceRecognitionEnabled"
    private var asking: Task<Void, Never>?

    private init() {
        enabled = UserDefaults.standard.object(forKey: Self.key) as? Bool
    }

    /// The device's answer: from Settings, or a change it acknowledged.
    func set(_ on: Bool) {
        UserDefaults.standard.set(on, forKey: Self.key)
        if enabled != on { enabled = on }
    }

    /// Asks the device: at once, then again after a failure (1 s doubling
    /// to 10 s) until it answers. One question at a time.
    func refresh() {
        guard asking == nil else { return }
        asking = Task { [weak self] in
            var wait: UInt64 = 1
            while !Task.isCancelled {
                let resp = try? await OTCConnection.shared.request { $0.payload = .reqGetSettings(Msg_GetSettings()) }
                guard let self, !Task.isCancelled else { return }
                if case .respSettings(let s)? = resp?.payload {
                    self.set(s.faceRecognitionEnabled)
                    // Asked at sign-in and back in front: the language too.
                    LanguageSettings.shared.deviceSaid(settings: s)
                    break
                }
                try? await Task.sleep(nanoseconds: wait * 1_000_000_000)
                wait = min(wait * 2, 10)
            }
            // Cancelled: reset() has let go of it already, and a newer
            // question may have taken its place.
            guard !Task.isCancelled else { return }
            self?.asking = nil
        }
    }

    /// Log Out: the next device may say otherwise.
    func reset() {
        asking?.cancel()
        asking = nil
        UserDefaults.standard.removeObject(forKey: Self.key)
        enabled = nil
    }
}
