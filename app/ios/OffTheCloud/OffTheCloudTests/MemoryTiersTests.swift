// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// What a device's memory allows (MemoryTiers.swift): Settings' Image
/// Tagging switch, the note under it (release 164) and Add user follow what
/// the device says, and a device that says nothing (before release 160) has
/// both, with no note.
struct MemoryTiersTests {
    @Test func taggingIsAvailableUnlessTheDeviceSaysNot() {
        // An older device leaves the field unset: available, as before.
        var s = Msg_Settings()
        #expect(!s.hasImageTaggingAvailable)
        #expect(MemoryTiers.taggingAvailable(s))

        s.imageTaggingAvailable = true
        #expect(MemoryTiers.taggingAvailable(s))

        // Under 3 GB (6 GB before release 164): off and disabled.
        s.imageTaggingAvailable = false
        #expect(s.hasImageTaggingAvailable)
        #expect(!MemoryTiers.taggingAvailable(s))
    }

    @Test func taggingAvailabilityDoesNotFollowTheSwitch() {
        // Turned off by its owner on a device that has the memory: the
        // switch stays usable.
        var s = Msg_Settings()
        s.imageTaggingEnabled = false
        #expect(MemoryTiers.taggingAvailable(s))
        s.imageTaggingAvailable = true
        #expect(MemoryTiers.taggingAvailable(s))
    }

    @Test func usersCanBeAddedUnlessTheDeviceSaysNot() {
        // An older device leaves the field unset: Add user, as before.
        var r = Msg_RespUsers()
        #expect(!r.hasCanAdd)
        #expect(MemoryTiers.canAddUsers(r))

        r.canAdd = true
        #expect(MemoryTiers.canAddUsers(r))

        // Less than 3 GB: the note in place of Add user.
        r.canAdd = false
        #expect(r.hasCanAdd)
        #expect(!MemoryTiers.canAddUsers(r))
    }

    @Test func theUsersADeviceHasDoNotChangeIt() {
        // Existing users keep working where no other can be added: the
        // list and the answer are separate.
        var u = Msg_User()
        u.username = "pepe"
        var r = Msg_RespUsers()
        r.users = [u]
        #expect(MemoryTiers.canAddUsers(r))
        r.canAdd = false
        #expect(!MemoryTiers.canAddUsers(r))
        #expect(r.users.map(\.username) == ["pepe"])
    }

    @Test func theTaggingNoteShowsOnlyWhereTheDeviceSaysItIsHeavy() {
        // An older device, and one with 6 GB or more, say nothing: no note.
        var s = Msg_Settings()
        #expect(!s.hasImageTaggingHeavy)
        #expect(!MemoryTiers.taggingHeavy(s))
        s.imageTaggingAvailable = true
        #expect(!MemoryTiers.taggingHeavy(s))

        // Release 164 on 4 GB (issue #209): there, off until turned on,
        // with the note - whether the switch is on or off.
        s.imageTaggingHeavy = true
        #expect(MemoryTiers.taggingHeavy(s))
        s.imageTaggingEnabled = true
        #expect(MemoryTiers.taggingHeavy(s))
        #expect(MemoryTiers.taggingAvailable(s))

        // A device that doesn't say whether it has it, has it.
        var unsaid = Msg_Settings()
        unsaid.imageTaggingHeavy = true
        #expect(MemoryTiers.taggingHeavy(unsaid))

        s.imageTaggingHeavy = false
        #expect(!MemoryTiers.taggingHeavy(s))
    }

    @Test func noTaggingNoteWhereThereIsNoTagging() {
        // Under 3 GB the switch is off and disabled and the footer says
        // why: the note about its cost doesn't show.
        var s = Msg_Settings()
        s.imageTaggingAvailable = false
        s.imageTaggingHeavy = true
        #expect(!MemoryTiers.taggingAvailable(s))
        #expect(!MemoryTiers.taggingHeavy(s))
    }

    @Test func theNoteReadsAsTheCatalogSays() {
        #expect(S.appSettingsUsersNeedMemory == "Adding users needs a device with at least 4 GB of memory.")
        #expect(S.appSettingsTaggingFooter.hasSuffix("It needs a device with at least 4 GB of memory."))
        #expect(S.appSettingsTaggingHeavy == "On this device Image Tagging uses about 1–2 GB more memory, and new photos take longer to process.")
    }
}
