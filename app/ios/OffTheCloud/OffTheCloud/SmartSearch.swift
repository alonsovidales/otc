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
//    - Its model (device release 152): downloaded, about 400 MB, only once
//      Smart search is first turned on (Settings.smart_search_model; absent
//      = an older device, which had it: everything as before). Until it is
//      there Settings says so under the switch - the download note, or a
//      progress bar asked about again every 3 s while the switch is on and
//      Settings shows - and the search offers no row. A search told
//      smart_search_downloading drops the words as smart_search_unavailable
//      does, keeps the switch on and asks where the model is.
//

import SwiftUI

/// Where Smart search's model is, as the device's Settings say
/// (Settings.smart_search_model). Since release 152 the device downloads it,
/// about 400 MB, only once Smart search is first turned on. A device before
/// that says nothing - it had the model, or refuses turning Smart search on
/// - and is `ready`: everything as before.
enum SmartSearchModelState: Equatable {
    /// On the device: Smart search works.
    case ready
    /// Not on the device, and nothing asked for it: turning Smart search on
    /// downloads it.
    case missing
    /// Being downloaded, `percent` 0-100 - or waiting to try again, with
    /// `problem` saying why in the device's words (it tries again by itself).
    case downloading(percent: Int, problem: String?)

    init(_ s: Msg_Settings) {
        guard s.hasSmartSearchModel else {
            self = .ready
            return
        }
        let m = s.smartSearchModel
        if m.ready {
            self = .ready
        } else if m.downloading {
            let problem = m.problem.trimmingCharacters(in: .whitespacesAndNewlines)
            self = .downloading(percent: min(max(Int(m.percent), 0), 100), problem: problem.isEmpty ? nil : problem)
        } else {
            self = .missing
        }
    }

    var isDownloading: Bool {
        if case .downloading = self { return true }
        return false
    }

    /// What Settings' section says under its footer: the download note while
    /// the model isn't there, how far the download is - or the device's
    /// words for why it waits - while it comes, nothing once it is there.
    var words: String? {
        switch self {
        case .ready: return nil
        case .missing: return S.appSettingsSmartSearchDownloadNote
        case .downloading(let percent, let problem): return problem ?? S.appSettingsSmartSearchDownloading(percent: percent)
        }
    }

    /// The progress bar's share (0-1) while downloading; nil: no bar.
    var progress: Double? {
        if case .downloading(let percent, _) = self { return Double(percent) / 100 }
        return nil
    }

    /// What VoiceOver is told as Settings' section changes from `before` to
    /// `now`: when the download starts (or starts again after a problem),
    /// runs into a problem, and is done (as 100%) - never at every percent.
    /// nil: nothing new to say. The section's first view isn't announced.
    /// Android's and the web's announcement().
    static func announcement(before: SmartSearchModelState, now: SmartSearchModelState) -> SmartSearchAnnouncement? {
        if case .downloading(let percent, let problem) = now {
            let was: String?? = {
                if case .downloading(_, let p) = before { return .some(p) }
                return nil
            }()
            if let problem { return was == .some(problem) ? nil : .problem(problem) }
            return was == .some(nil) ? nil : .progress(percent)
        }
        if now == .ready, before.isDownloading { return .progress(100) }
        return nil
    }
}

/// What Settings' Smart search section has VoiceOver say
/// (SmartSearchModelState.announcement).
enum SmartSearchAnnouncement: Equatable {
    /// The download started (again) at this percent, or is done (100).
    case progress(Int)
    /// Why the download waits, in the device's words.
    case problem(String)

    var text: String {
        switch self {
        case .progress(let percent): return S.appSettingsSmartSearchDownloading(percent: percent)
        case .problem(let words): return words
        }
    }
}

/// Whether the signed-in device has Smart search and it is on, from its
/// Settings: asked with every connection's Settings (PlacesSupport) and
/// Settings' load, set at once by the switch and by a search told
/// smart_search_off. nil: not known yet, or a device before it. With it,
/// where its model is (`model`), from the same answers.
@MainActor
final class SmartSearchSupport: ObservableObject {
    static let shared = SmartSearchSupport()

    @Published private(set) var enabled: Bool?
    /// Where the model is: `ready` for a device before release 152, and
    /// after Log Out.
    @Published private(set) var model: SmartSearchModelState = .ready

    /// The switch is on.
    var isOn: Bool { enabled == true }

    /// The free-text search is offered: on, with the model there.
    var searchable: Bool { isOn && model == .ready }

    /// How often Settings asks where the model is while it downloads.
    static let pollEvery: Duration = .seconds(3)

    /// Settings asks again (every `pollEvery`) while the switch is on and the
    /// model is on its way: not once it is there, nor with the switch off.
    static func polls(on: Bool?, model: SmartSearchModelState) -> Bool {
        on == true && model.isDownloading
    }

    /// Sends the question. Tests answer in the device's place.
    var request: @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope = { payload in
        try await OTCConnection.shared.request { $0.payload = payload }
    }

    func deviceSaid(settings s: Msg_Settings) {
        set(s.hasSmartSearchEnabled ? s.smartSearchEnabled : nil)
        let m = SmartSearchModelState(s)
        if model != m { model = m }
    }

    func set(_ on: Bool?) {
        if enabled != on { enabled = on }
    }

    /// Turned on where the model isn't, or a search told
    /// smart_search_downloading: on its way, until the device says more.
    func modelRequested() {
        if !model.isDownloading { model = .downloading(percent: 0, problem: nil) }
    }

    /// Asks the device's Settings again: the switch, and where the model is.
    /// Returns them, or nil when they didn't come.
    @discardableResult
    func refresh() async -> Msg_Settings? {
        guard let resp = try? await request(.reqGetSettings(Msg_GetSettings())),
              case .respSettings(let s) = resp.payload else { return nil }
        deviceSaid(settings: s)
        return s
    }

    /// Log Out: the next device may say otherwise.
    func reset() {
        enabled = nil
        model = .ready
    }

    static let off = "smart_search_off"
    static let unavailable = "smart_search_unavailable"
    static let downloading = "smart_search_downloading"

    /// Why the device refused the words.
    enum Refused: Equatable {
        /// Turned off (on another app): the switch goes off here too.
        case off
        /// The device can't search by description: the switch stays.
        case unavailable
        /// The model is still on its way (release 152): the switch stays,
        /// and the device is asked where the model is.
        case downloading
    }

    /// A SearchPhotos answer that refuses the text: the device's words (nil
    /// when it gave none), and why. nil for any other answer.
    static func refusal(_ resp: Msg_RespEnvelope?) -> (words: String?, why: Refused)? {
        guard let resp, resp.error else { return nil }
        let why: Refused
        switch resp.errorCode {
        case off: why = .off
        case unavailable: why = .unavailable
        case downloading: why = .downloading
        default: return nil
        }
        return (resp.errorMessage.isEmpty ? nil : resp.errorMessage, why)
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

/// Under Smart search's footer in Settings, until its model is on the
/// device: the download note, or a progress bar with how far the download
/// is - or, while it waits to try again, the device's words for why.
/// VoiceOver reads one element with the percentage, taken in when focused;
/// the section announces only the start, a problem and the end
/// (SmartSearchModelState.announcement).
struct SmartSearchModelLine: View {
    let model: SmartSearchModelState

    var body: some View {
        if case .downloading(let percent, let problem) = model, let progress = model.progress, let words = model.words {
            VStack(alignment: .leading, spacing: 6) {
                ProgressView(value: progress)
                    .progressViewStyle(.linear)
                Text(words)
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(words)
            // The device's words carry no number: the percentage follows.
            .accessibilityValue(problem == nil ? "" : S.iosStoragePercent(percent: percent))
            .accessibilityAddTraits(.updatesFrequently)
        } else if let words = model.words {
            Text(words)
        }
    }
}
