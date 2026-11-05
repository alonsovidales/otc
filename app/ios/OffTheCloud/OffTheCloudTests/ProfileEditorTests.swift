// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// The profile read again over the fields (ProfileFields.merge): what was
/// changed here and not saved stays. Android's ProfileEditorTest, case for
/// case.
struct ProfileEditorTests {
    let old = Data([1, 2, 3])
    let picked = Data([9, 9])

    /// P1: the first read fills every field.
    @Test func theFirstReadFillsEveryField() {
        let device = ProfileFields(name: "Ana", bio: "Hi", image: old)
        #expect(ProfileFields.merge(current: ProfileFields(), shown: nil, device: device) == device)
    }

    /// P2: a photo picked and not saved stays when the section reads again
    /// (scrolled back on screen).
    @Test func aPickedPhotoStaysThroughAReadAgain() {
        let device = ProfileFields(name: "Ana", bio: "Hi", image: old)
        let current = ProfileFields(name: "Ana", bio: "Hi", image: picked)
        #expect(ProfileFields.merge(current: current, shown: device, device: device).image == picked)
    }

    /// P3: a name or text typed and not saved stays; the fields not changed
    /// take what the device says now.
    @Test func whatWasTypedStaysAndTheRestFollowsTheDevice() {
        let shown = ProfileFields(name: "Ana", bio: "Hi", image: old)
        let current = ProfileFields(name: "Ana María", bio: "Hi", image: old)
        let device = ProfileFields(name: "Ana", bio: "Hello", image: picked)
        #expect(ProfileFields.merge(current: current, shown: shown, device: device)
                == ProfileFields(name: "Ana María", bio: "Hello", image: picked))
    }

    /// P4: a photo taken away here (none) stays away until saved.
    @Test func aPhotoTakenAwayStaysAway() {
        let shown = ProfileFields(name: "Ana", bio: "", image: old)
        let current = ProfileFields(name: "Ana", bio: "", image: nil)
        #expect(ProfileFields.merge(current: current, shown: shown, device: shown).image == nil)
    }

    /// P5: once saved, the next read takes the device's again (it may have
    /// made the picture smaller).
    @Test func afterASaveTheDeviceIsReadAgain() {
        let saved = ProfileFields(name: "Ana", bio: "Hi", image: picked)
        let device = ProfileFields(name: "Ana", bio: "Hi", image: Data([7]))
        #expect(ProfileFields.merge(current: saved, shown: saved, device: device) == device)
    }
}
