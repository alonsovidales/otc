// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Combine

// The user's language (docs/i18n.md, "The stored choice"). The device
// keeps it per user and reports it in its status (Status.language: absent
// on a device that predates localization, "" for Automatic); this Mac
// keeps a local copy, which Disconnect leaves alone. The app's language is
// the copy's: the language chosen when this build has it, English when it
// doesn't (it stays the choice), and for Automatic the Mac's own language
// among this build's (L10n.systemLanguage).
//
// A change made here (Settings' Language picker) is saved as pending and
// shown at once, then sent with SetLanguage, against the device's value as
// this app last saw it, until the device answers:
//   - ok: no longer pending (compare-and-clear: a change made meanwhile
//     goes next, against the value just sent);
//   - Ack code "changed" (another app changed it since): no longer
//     pending, and the device's value is read again and taken;
//   - unknown_payload: a device too old to keep it - the choice stays on
//     this Mac, still pending, and nothing more is sent until the next
//     connection, when the device may have been updated;
//   - anything else: still pending, sent again at the next connection.
//
// The device's value is taken whenever it differs from the copy - except
// while a change made here is pending: a different value is then taken
// for one from before the change and ignored until the device reports the
// change, or `window` has passed. An answer to a status asked for before
// a SetLanguage was sent or answered may predate it (the device answers
// each request on its own) and is left out (`epoch`). Another device or
// password drops a pending change (it was made against the old one's
// value) and keeps the copy. otc-sync's engine/language.go does the same.

/// The language state, without any I/O: what is kept, and the rules above.
struct LanguageState: Equatable {
    /// The local copy: "" for Automatic, else a language code.
    var choice = ""
    /// When the choice was changed on this Mac, until the device has it.
    var pendingSince: Date?
    /// The device's value the pending change was made against, sent as
    /// SetLanguage.expected; nil when none had been seen (left out).
    var expected: String?
    /// The device's value as last seen since the device or password last
    /// changed; nil when not seen, or the device predates localization.
    var deviceSeen: String?
    /// The value of the SetLanguage under way.
    var sending: String?
    /// The device answered SetLanguage with unknown_payload: changes stay
    /// on this Mac until the next connection.
    var deviceTooOld = false
    /// Counts SetLanguage requests sent and answered (see deviceReported).
    var epoch = 0

    /// How long a pending change outweighs a different value the device
    /// reports.
    static let window: TimeInterval = 30

    var pending: Bool { pendingSince != nil }

    /// What the device can answer to a SetLanguage.
    enum Answer: Equatable { case ok, changed, unknownPayload, failed }

    /// A language chosen here; false when it is already the choice.
    mutating func choose(_ code: String, now: Date) -> Bool {
        guard code != choice else { return false }
        choice = code
        pendingSince = now
        expected = deviceSeen
        return true
    }

    /// The next SetLanguage to send, marked under way; nil when nothing is
    /// pending, one is under way already, or the device can't keep it.
    mutating func nextSend() -> (language: String, expected: String?)? {
        guard sending == nil, pending, !deviceTooOld else { return nil }
        sending = choice
        epoch += 1
        return (choice, expected)
    }

    /// The device's answer to the SetLanguage for `sent`. True when its
    /// value has to be read again (and then taken: "changed").
    mutating func answered(_ answer: Answer, sent: String) -> Bool {
        sending = nil
        epoch += 1
        switch answer {
        case .ok:
            deviceSeen = sent
            if choice == sent {
                clearPending()
            } else if pending {
                // A change made while this one was under way: the device
                // has `sent` now, so it is made against that.
                expected = sent
            }
            return false
        case .changed:
            if choice == sent { clearPending() }
            return true
        case .unknownPayload:
            deviceTooOld = true
            return false
        case .failed:
            return false
        }
    }

    /// The device's value from a status answer (nil: a device that
    /// predates localization - the copy stands), asked for at `askedAt`
    /// (the epoch then). True when it became the choice.
    mutating func deviceReported(_ value: String?, askedAt: Int, now: Date) -> Bool {
        // Possibly from before a SetLanguage sent or answered since.
        guard sending == nil, askedAt == epoch else { return false }
        guard let value else {
            deviceSeen = nil
            return false
        }
        deviceSeen = value
        if value == choice {
            if pending { clearPending() } // the device has it
            return false
        }
        if let since = pendingSince, now.timeIntervalSince(since) < Self.window {
            return false // from before the change, most likely
        }
        choice = value
        clearPending()
        return true
    }

    /// Signed in again: a device updated since may keep it now.
    mutating func connected() { deviceTooOld = false }

    /// Another device or password: what the old one said doesn't count,
    /// and a change made against its value isn't sent to the new one.
    mutating func forgetDevice() {
        deviceSeen = nil
        deviceTooOld = false
        clearPending()
    }

    private mutating func clearPending() {
        pendingSince = nil
        expected = nil
    }
}

/// Where the local copy is kept (SettingsStore: UserDefaults).
@MainActor
protocol LanguageStore: AnyObject {
    /// "" for Automatic, else a language code.
    var language: String { get set }
    var languagePendingSince: Date? { get set }
    var languageExpected: String? { get set }
}

/// The language the app shows, the local copy and its pending change, for
/// the Language picker and SyncModel. `shared` is SettingsStore's
/// (SettingsStore.swift).
@MainActor
final class LanguageSettings: ObservableObject {
    @Published private(set) var state: LanguageState

    /// Sends SetLanguage (SyncModel, once bound): the language and the
    /// expected value, and what the device answered.
    var send: (@MainActor (String, String?) async -> LanguageState.Answer)?
    /// Reads the device's status again (SyncModel), after "changed".
    var reread: (@MainActor () async -> Void)?

    private let store: LanguageStore
    private var localeObserver: NSObjectProtocol?

    init(store: LanguageStore) {
        self.store = store
        state = LanguageState(choice: store.language, pendingSince: store.languagePendingSince,
                              expected: store.languageExpected)
    }

    /// Whether the user can pick at all: only with more than one language
    /// in this build. While English is the only one that ships, the picker
    /// stays hidden.
    nonisolated static var choosable: Bool { L10nLanguage.all.count > 1 }

    /// The language the app shows for `choice` (docs/i18n.md): the Mac's
    /// for Automatic, the choice when this build has it, else English.
    nonisolated static func effective(_ choice: String, preferences: [String] = Locale.preferredLanguages) -> String {
        if choice.isEmpty { return L10n.systemLanguage(preferences) }
        return L10nLanguage.all.contains { $0.code == choice } ? choice : L10nLanguage.english.code
    }

    /// The local copy: "" for Automatic.
    var choice: String { state.choice }

    /// The picker's selection: a language this build doesn't have is kept
    /// as the choice but shown as English.
    var shownChoice: String {
        state.choice.isEmpty || L10nLanguage.all.contains { $0.code == state.choice }
            ? state.choice : L10nLanguage.english.code
    }

    /// The Mac's own language among this build's, by its own name - what
    /// Automatic gives.
    var automaticName: String {
        let code = L10n.systemLanguage()
        return (L10nLanguage.all.first { $0.code == code } ?? L10nLanguage.english).name
    }

    /// At launch, before anything is drawn: the copy's language, and
    /// Automatic following the Mac's language from then on.
    func start() {
        apply()
        localeObserver = NotificationCenter.default.addObserver(
            forName: NSLocale.currentLocaleDidChangeNotification, object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor [weak self] in self?.apply() }
        }
    }

    /// The Language picker: saved and shown at once, then sent.
    func choose(_ code: String) {
        guard state.choose(code, now: Date()) else { return }
        save()
        apply()
        sendPending()
    }

    /// Signed in (SyncModel's onConnect, before the status poll): a change
    /// still pending goes now, and is under way by the time the status
    /// comes back.
    func connected() {
        state.connected()
        sendPending()
    }

    /// The epoch to pass back with the status answer (deviceReported).
    var epoch: Int { state.epoch }

    /// The device's value from a status answer asked for at `askedAt`.
    func deviceReported(_ value: String?, askedAt: Int) {
        var next = state
        let adopted = next.deviceReported(value, askedAt: askedAt, now: Date())
        guard next != state else { return }
        state = next
        save()
        if adopted { apply() }
    }

    /// Another device or password (SyncModel's settings binding).
    func forgetDevice() {
        state.forgetDevice()
        save()
    }

    /// Sends what is pending, one SetLanguage at a time; marked under way
    /// before this returns.
    private func sendPending() {
        guard let send, let next = state.nextSend() else { return }
        Task { @MainActor [weak self] in
            let answer = await send(next.language, next.expected)
            guard let self else { return }
            let reread = self.state.answered(answer, sent: next.language)
            self.save()
            if reread { await self.reread?() }
            // A change made meanwhile goes now; a failure waits for the
            // next connection.
            if answer != .failed { self.sendPending() }
        }
    }

    private func save() {
        if store.language != state.choice { store.language = state.choice }
        if store.languagePendingSince != state.pendingSince { store.languagePendingSince = state.pendingSince }
        if store.languageExpected != state.expected { store.languageExpected = state.expected }
    }

    private func apply() {
        L10n.shared.set(Self.effective(state.choice))
    }
}
