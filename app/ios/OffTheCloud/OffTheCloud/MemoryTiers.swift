// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  MemoryTiers.swift
//  OffTheCloud
//
//  What a device's memory allows (owner, 2026-11-01; since release 164,
//  issue #209, 2026-11-03: "Opt-in on 4 GB"). With 6 GB or more,
//  everything. Under 6 GB (the 4 GB boards) Image Tagging is there but off
//  until its user turns it on, and Settings says under the switch what it
//  costs there (Settings.image_tagging_heavy). Under 3 GB there is no Image
//  Tagging - the switch off and disabled, the footer saying it needs at
//  least 4 GB - and no other users, a note in place of Add user. An older
//  device says none of it, and has everything, as before.
//

import Foundation

enum MemoryTiers {
    /// Whether Image Tagging can be turned on (Settings.image_tagging_
    /// available): false on a device with less than 3 GB (6 GB before
    /// release 164), where turning it on is refused (tagging_unavailable).
    /// Unset: a device before it, which has it.
    static func taggingAvailable(_ s: Msg_Settings) -> Bool {
        !s.hasImageTaggingAvailable || s.imageTaggingAvailable
    }

    /// Whether Settings says, under the Image Tagging switch, that it takes
    /// about 1-2 GB more memory and makes new photos slower to process
    /// here (Settings.image_tagging_heavy: a device under 6 GB, release
    /// 164). Only where it can be turned on at all; unset elsewhere and on
    /// older devices.
    static func taggingHeavy(_ s: Msg_Settings) -> Bool {
        taggingAvailable(s) && s.hasImageTaggingHeavy && s.imageTaggingHeavy
    }

    /// Whether the device takes another user (RespUsers.can_add): false on
    /// one with less than 3 GB, where CreateUser is refused
    /// (users_unavailable); the users it has keep working. Unset: a device
    /// before it, which does.
    static func canAddUsers(_ r: Msg_RespUsers) -> Bool {
        !r.hasCanAdd || r.canAdd
    }
}
