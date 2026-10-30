// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SmartSearch.swift
//  OffTheCloud
//
//  Smart search (beta, device release 151): photos found by describing
//  them ("kids in the snow", "un perro en la playa"), worked out on the
//  device (SigLIP 2) and kept encrypted there. Android follows the same
//  rules:
//    - Only while the user has it on (Settings.smart_search_enabled, off by
//      default; absent = a device before it: neither the switch nor the
//      free-text search, the search exactly as before).
//    - Settings: a "Smart search" switch with a Beta badge after On this
//      day; turning it off asks first (it deletes what the device learned);
//      a device without the model refuses turning it on
//      (smart_search_unavailable) in its own words.
//    - The search: typing works as before (tags and people become chips);
//      while it is on, the panel's first row is "Search photos for “…”",
//      and the Search key takes it unless the words are exactly a tag or a
//      person. It becomes a chip of its own, and Images shows the device's
//      ranking (SearchPhotos.text) within the other chips - no months, no
//      date scrubber, as a tag search. While the device is still describing
//      the photos it held (ListOfFiles.smart_search_reading) a quiet note
//      says so. smart_search_off (turned off on another app) and
//      smart_search_unavailable drop the words with the device's words, or
//      app.photos.search.smart_failed.
//

import SwiftUI

/// Whether the signed-in device has Smart search and it is on, from its
/// Settings: asked with every connection's Settings (PlacesSupport) and
/// Settings' load, set at once by the switch and by a search told
/// smart_search_off. nil: not known yet, or a device before it.
@MainActor
final class SmartSearchSupport: ObservableObject {
    static let shared = SmartSearchSupport()

    @Published private(set) var enabled: Bool?

    /// The free-text search is offered.
    var isOn: Bool { enabled == true }

    func deviceSaid(settings s: Msg_Settings) {
        set(s.hasSmartSearchEnabled ? s.smartSearchEnabled : nil)
    }

    func set(_ on: Bool?) {
        if enabled != on { enabled = on }
    }

    /// Log Out: the next device may say otherwise.
    func reset() {
        enabled = nil
    }

    static let off = "smart_search_off"
    static let unavailable = "smart_search_unavailable"

    /// A SearchPhotos answer that refuses the text: the device's words (nil
    /// when it gave none), and whether it says Smart search is off. nil for
    /// any other answer.
    static func refusal(_ resp: Msg_RespEnvelope?) -> (words: String?, off: Bool)? {
        guard let resp, resp.error, resp.errorCode == off || resp.errorCode == unavailable else { return nil }
        return (resp.errorMessage.isEmpty ? nil : resp.errorMessage, resp.errorCode == off)
    }

    /// The words as searched for: trimmed, nil when nothing is left.
    static func text(_ typed: String) -> String? {
        let t = typed.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }
}

/// "Smart search  Beta": the switch's label in Settings.
struct SmartSearchLabel: View {
    var body: some View {
        HStack(spacing: 8) {
            Text(S.appSettingsSmartSearchTitle)
            Text(S.appSettingsSmartSearchBeta)
                .font(.caption2.weight(.semibold))
                .textCase(.uppercase)
                .tracking(0.4)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .foregroundStyle(MenuStyle.gold)
                .background(MenuStyle.activeFill, in: Capsule())
        }
        .accessibilityElement(children: .combine)
    }
}
