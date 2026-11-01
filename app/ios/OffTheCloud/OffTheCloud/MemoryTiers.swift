// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  MemoryTiers.swift
//  OffTheCloud
//
//  What a device's memory allows (owner, 2026-11-01: "the 4 gigabytes is
//  going to don't have tagging, and the 2 gigabytes is not going to have
//  tagging and users"; 8 GB everything). Since release 160 a device with
//  less than 6 GB has no Image Tagging and one with less than 3 GB takes
//  no other users; it says so, and Settings follows: the tagging switch
//  off and disabled, and a note in place of Add user. An older device
//  says neither, and has both, as before.
//

import Foundation

enum MemoryTiers {
    /// Whether Image Tagging can be turned on (Settings.image_tagging_
    /// available): false on a device with less than 6 GB, where turning it
    /// on is refused (tagging_unavailable). Unset: a device before it,
    /// which has it.
    static func taggingAvailable(_ s: Msg_Settings) -> Bool {
        !s.hasImageTaggingAvailable || s.imageTaggingAvailable
    }

    /// Whether the device takes another user (RespUsers.can_add): false on
    /// one with less than 3 GB, where CreateUser is refused
    /// (users_unavailable); the users it has keep working. Unset: a device
    /// before it, which does.
    static func canAddUsers(_ r: Msg_RespUsers) -> Bool {
        !r.hasCanAdd || r.canAdd
    }
}
