// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FaceRecognition.swift
//  OffTheCloud
//
//  Whether People shows: while face recognition is on
//  (Settings.face_recognition_enabled, off by default) or, since device
//  release 180, Pets (Settings.pets_enabled, off by default: the pets it
//  finds are people in People, unreachable otherwise) - People in the
//  search, a person in the Images search, and the People page. A device
//  whose Settings lack pets_enabled is as before: face recognition alone.
//  The web's faceRecognition.ts, kept the same way: asked once the app is
//  signed in (MainView, and again whenever the app comes back to the front)
//  and set by Settings when one of the two switches is turned, so People
//  comes and goes at once. Android's FaceRecognition follows the same rules.
//
//  Until the device has answered, the answer this phone had last time
//  stands in, so a launch neither shows People and then takes it away nor
//  the other way round. With no answer ever (a first sign-in) it is nil
//  and People stays hidden until the device says: face recognition is off
//  on most devices, and People appearing is gentler than People vanishing.
//

import Foundation

@MainActor
final class FaceRecognition: ObservableObject {
    static let shared = FaceRecognition()

    /// Whether People shows: true while face recognition or Pets is on,
    /// false while neither is, as the device said (or last said to this
    /// phone); nil: not known yet, so People stays hidden.
    @Published private(set) var enabled: Bool?

    /// Shorthand for the places that show People.
    var isOn: Bool { enabled == true }

    /// The two switches as the device said them: face recognition's, and
    /// Pets' (nil: a device before it, or not known yet).
    private var faces: Bool?
    private var pets: Bool?

    private static let key = "faceRecognitionEnabled"
    private static let petsKey = "petsEnabled"
    private var asking: Task<Void, Never>?

    private init() {
        faces = UserDefaults.standard.object(forKey: Self.key) as? Bool
        pets = UserDefaults.standard.object(forKey: Self.petsKey) as? Bool
        enabled = Self.peopleShown(faces: faces, pets: pets)
    }

    /// People shows while face recognition or Pets is on; with neither on,
    /// whatever face recognition's switch says (nil: not known yet) - so a
    /// device without Pets (`pets` nil) is as before.
    nonisolated static func peopleShown(faces: Bool?, pets: Bool?) -> Bool? {
        if faces == true || pets == true { return true }
        return faces
    }

    /// The device's Settings: both switches.
    func deviceSaid(settings s: Msg_Settings) {
        keep(faces: s.faceRecognitionEnabled, pets: s.hasPetsEnabled ? s.petsEnabled : nil)
    }

    /// Face recognition's switch, as the device acknowledged it.
    func set(_ on: Bool) {
        keep(faces: on, pets: pets)
    }

    /// Pets' switch, as the device acknowledged it.
    func setPets(_ on: Bool) {
        keep(faces: faces, pets: on)
    }

    private func keep(faces: Bool?, pets: Bool?) {
        self.faces = faces
        self.pets = pets
        let d = UserDefaults.standard
        if let faces { d.set(faces, forKey: Self.key) } else { d.removeObject(forKey: Self.key) }
        if let pets { d.set(pets, forKey: Self.petsKey) } else { d.removeObject(forKey: Self.petsKey) }
        let shown = Self.peopleShown(faces: faces, pets: pets)
        if enabled != shown { enabled = shown }
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
                    self.deviceSaid(settings: s)
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
        UserDefaults.standard.removeObject(forKey: Self.petsKey)
        faces = nil
        pets = nil
        enabled = nil
    }
}
