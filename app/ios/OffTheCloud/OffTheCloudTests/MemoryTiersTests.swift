// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// What a device's memory allows (MemoryTiers.swift): Settings' Image
/// Tagging switch and Add user follow what the device says, and a device
/// that says nothing (before release 160) has both.
struct MemoryTiersTests {
    @Test func taggingIsAvailableUnlessTheDeviceSaysNot() {
        // An older device leaves the field unset: available, as before.
        var s = Msg_Settings()
        #expect(!s.hasImageTaggingAvailable)
        #expect(MemoryTiers.taggingAvailable(s))

        s.imageTaggingAvailable = true
        #expect(MemoryTiers.taggingAvailable(s))

        // Less than 6 GB: off and disabled.
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

    @Test func theNoteReadsAsTheCatalogSays() {
        #expect(S.appSettingsUsersNeedMemory == "Adding users needs a device with at least 4 GB of memory.")
        #expect(S.appSettingsTaggingFooter.hasSuffix("It needs a device with at least 6 GB of memory."))
    }
}
