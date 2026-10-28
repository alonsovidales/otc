// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  LanguageSettings.swift
//  OffTheCloud
//
//  The language the app shows (docs/i18n.md, "The stored choice"). The
//  user's choice lives on the device, per user - Settings.language and
//  Status.language: "" for Automatic, absent on a device that predates
//  it - and every app follows it. This phone keeps a copy of its own, which
//  survives Log Out, for the time before the device has said (or a device
//  that can't): the copy is what the app shows, and the device's value,
//  once seen, becomes the copy. Android's i18n/LanguageSettings.kt is the
//  same.
//
//  A change made here is shown at once (L10n.shared.set: SwiftUI follows
//  live, spike S1) and sent as SetLanguage once a sign-in is accepted -
//  never before: the device answers not_authenticated, and asking would
//  only start a sign-in of its own. It goes expecting the device value this
//  phone last saw when the change was made, so a change made meanwhile from
//  another app isn't overwritten: the device answers "changed" and that one
//  is adopted. A device too old to keep it (unknown_payload) leaves the
//  change pending and nothing more is sent until the next connection, when
//  the device may have been updated.
//
//  A change made before any value of a device was seen - no device set up
//  (the welcome screen, a fresh install, after Log Out), the session ended
//  by the device (a password it turned down) or new credentials saved, not
//  signed in since launch, a device that predates the field - is for the
//  next device: nothing was seen to compare it with, so it goes without
//  `expected` (never "") at the next sign-in the device accepts, which
//  makes it that device's, whatever another app set there (a new device
//  has Automatic). Log Out and a relaunch keep it; values a device reports
//  before that sign-in don't replace it. Leaving a device from the
//  "isn't available" and "can't connect" cards carries a change still
//  pending to the next device the same way: it never reached the one left.
//

import Foundation

/// The choice and what the device's answers do to it, as plain values.
struct LanguageChoice: Equatable, Sendable {
    /// The local copy: "" (Automatic) or a language code - possibly one
    /// this build doesn't have, which shows English and is kept as chosen.
    var language = ""
    /// Chosen on this phone and not yet taken by the device: sent again at
    /// the next connection.
    var pending = false
    /// When it was chosen (or last sent). For `window` after it, a
    /// different value from the device is an answer given before ours was
    /// applied, and ignored.
    var pendingAt: Date?
    /// The device value the pending change was made against, SetLanguage's
    /// `expected`; nil when none had been seen (left out, never "").
    var expected: String?
    /// The pending change is for the device this phone signs in to next:
    /// made before any value of a device was seen, or carried from a device
    /// left. Log Out keeps it; the next accepted sign-in (connected) makes
    /// it that device's.
    var forNextDevice = false
    /// The device's value this phone last saw in this session; nil when it
    /// has seen none since launch, the last sign-in ended, or Log Out.
    /// Kept in memory only.
    var seen: String?

    static let window: TimeInterval = 30

    /// The language a choice shows: Automatic is the system's language
    /// among this build's (else English), and a code this build doesn't
    /// have is English.
    static func effective(_ language: String, system: [String] = Locale.preferredLanguages,
                          among languages: [L10nLanguage] = L10nLanguage.all) -> String {
        if language.isEmpty { return L10n.systemLanguage(system, among: languages) }
        return languages.contains { $0.code == language } ? language : L10nLanguage.english.code
    }

    /// Chosen on this phone: shown at once, and pending until the device
    /// takes it - against the value seen, or for the next device when none
    /// was (or a change for the next device is still waiting).
    mutating func choose(_ code: String, at now: Date) {
        forNextDevice = seen == nil || (forNextDevice && pending)
        expected = forNextDevice ? nil : seen
        language = code
        pending = true
        pendingAt = now
    }

    /// Being sent: the window counts from now.
    mutating func sending(at now: Date) {
        pendingAt = now
    }

    /// The device took `sent`. A change made while it was on its way goes
    /// next, against `sent`.
    mutating func taken(_ sent: String) {
        seen = sent
        if language == sent {
            pending = false
            expected = nil
            forNextDevice = false
        } else if pending {
            expected = sent
            forNextDevice = false
        }
    }

    /// The device refused `sent` because its value had changed meanwhile
    /// (Ack code "changed"), and holds `value`: adopted, unless this phone
    /// has chosen again since - that one goes next, expecting `value`.
    mutating func refused(_ sent: String, deviceHolds value: String) {
        seen = value
        guard language == sent else {
            expected = value
            forNextDevice = false
            return
        }
        language = value
        clearPending()
    }

    /// "changed" with the device's value unread: `sent` is no longer
    /// pending, and the device's next Status or Settings value is adopted.
    mutating func refused(_ sent: String) {
        seen = nil
        if language == sent { clearPending() }
    }

    /// The device's value, from its Status or Settings (nil: a device that
    /// predates the field). Returns whether the choice changed.
    @discardableResult
    mutating func deviceSaid(_ value: String?, at now: Date) -> Bool {
        guard let value else { return false }
        // Waiting for the next device's sign-in: what a device says before
        // it doesn't count.
        if pending && forNextDevice { return false }
        if value == language {
            // Ours, seen: nothing left to wait for.
            seen = value
            clearPending()
            return false
        }
        if let at = pendingAt {
            let age = now.timeIntervalSince(at)
            if age >= 0 && age < Self.window { return false }
        }
        seen = value
        language = value
        clearPending()
        return true
    }

    /// A sign-in the device accepted: a change for the next device is this
    /// device's from now on (sent to it now, and dropped like any other by
    /// a later Log Out).
    mutating func connected() {
        forNextDevice = false
    }

    /// The session ended without Log Out (the device turned the password
    /// down, or new credentials were saved): the value seen goes with it,
    /// so a change made before the next sign-in goes without `expected`. A
    /// change already pending keeps its own.
    mutating func sessionEnded() {
        seen = nil
    }

    /// Log Out: the copy stays; what belonged to the device signed in to
    /// goes - a change for the next device stays, and so does one still
    /// pending when `carryPending` (leaving from the cards: it never reached
    /// the device left), as one for the next device.
    mutating func loggedOut(carryPending: Bool = false) {
        seen = nil
        if pending && (forNextDevice || carryPending) {
            expected = nil
            forNextDevice = true
        } else {
            clearPending()
        }
    }

    private mutating func clearPending() {
        pending = false
        pendingAt = nil
        expected = nil
        forNextDevice = false
    }
}

/// What the device answered to SetLanguage.
enum LanguageAnswer: Equatable {
    case taken, changed, tooOld, failed

    static func of(_ resp: Msg_RespEnvelope) -> LanguageAnswer {
        if resp.error {
            return ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) ? .tooOld : .failed
        }
        guard case .respAck(let ack) = resp.payload else { return .failed }
        if ack.ok { return .taken }
        switch ack.code {
        case "changed": return .changed
        case "unknown_payload": return .tooOld
        default: return .failed
        }
    }
}

/// The app's choice of language: kept here, shown through L10n, told to
/// the device. One for the app (`shared`); tests make their own.
@MainActor
final class LanguageSettings: ObservableObject {
    static let shared = LanguageSettings()

    /// Where the copy is kept: defaults of its own, apart from the app's,
    /// whose whole domain Log Out removes (SecretsStore.logOut).
    nonisolated static let suiteName = "cloud.off-the.OffTheCloud.language"

    /// What the device is told and asked, and what a new language means for
    /// the connection.
    struct Device {
        /// Whether a sign-in has been accepted and holds: nothing is sent
        /// before (the connection would start one of its own).
        var signedIn: @MainActor () -> Bool
        var setLanguage: (_ language: String, _ expected: String?) async throws -> Msg_RespEnvelope
        var settings: () async -> Msg_Settings?
        /// The language shown changed: the push token goes again, so the
        /// device writes this phone's pushes in it.
        var languageChanged: @MainActor () -> Void
    }

    @Published private(set) var choice: LanguageChoice
    /// Whether the device keeps the choice for every app: nil until it has
    /// said, false for one too old to (the Language screen says so).
    @Published private(set) var deviceKeepsIt: Bool?

    let languages: [L10nLanguage]
    private let defaults: UserDefaults
    private let device: Device
    private let system: () -> [String]
    private let show: @MainActor (String) -> Void
    private let now: () -> Date
    /// The language last handed to `show`.
    private var shown: String?
    /// unknown_payload on this connection: nothing is sent until the next,
    /// and what was chosen stays pending.
    private var tooOld = false
    private var sending = false
    /// Signed in again while a SetLanguage was on its way: if that one
    /// fails (it went on the session that ended), it goes again at once.
    private var signedInWhileSending = false

    init(defaults: UserDefaults = UserDefaults(suiteName: LanguageSettings.suiteName) ?? .standard,
         device: Device = .live,
         languages: [L10nLanguage] = L10nLanguage.all,
         system: @escaping () -> [String] = { Locale.preferredLanguages },
         show: @escaping @MainActor (String) -> Void = { L10n.shared.set($0); WallpaperLanguage.share($0) },
         now: @escaping () -> Date = Date.init) {
        self.defaults = defaults
        self.device = device
        self.languages = languages
        self.system = system
        self.show = show
        self.now = now
        choice = Self.load(defaults)
    }

    /// The picker is offered only while the build has more than one
    /// language: English alone has nothing to choose.
    var pickerShown: Bool { languages.count > 1 }

    /// The code the app shows now.
    var effective: String { LanguageChoice.effective(choice.language, system: system(), among: languages) }

    /// The system's language among this build's: what Automatic shows.
    var systemLanguage: String { LanguageChoice.effective("", system: system(), among: languages) }

    /// The row the Language screen marks: "" for Automatic; a code this
    /// build doesn't have marks English, which is what it shows.
    var selected: String { choice.language.isEmpty ? "" : effective }

    /// A language's own name ("Español"); English's for a code this build
    /// doesn't have.
    func name(of code: String) -> String {
        (languages.first { $0.code == code } ?? L10nLanguage.english).name
    }

    /// The Settings row's value: "Automatic", or the language's own name.
    var currentName: String {
        choice.language.isEmpty ? S.appSettingsLanguageAutomatic : name(of: effective)
    }

    /// At launch, before the first view: the language the copy says. A
    /// change still pending stays so (a relaunch is no change of device).
    func start() {
        showChoice()
    }

    /// Back in front: Automatic follows a system language changed meanwhile.
    func cameToForeground() {
        showChoice()
    }

    /// Picked on the Language screen or a screen's language menu: shown at
    /// once, and sent now if signed in, else at the next sign-in.
    func choose(_ code: String) {
        choice.choose(code, at: now())
        save()
        showChoice()
        sendPending()
    }

    func deviceSaid(status: Msg_Status) {
        deviceSaid(status.hasLanguage ? status.language : nil)
    }

    func deviceSaid(settings: Msg_Settings) {
        deviceSaid(settings.hasLanguage ? settings.language : nil)
    }

    /// The device's value (nil: a device that predates it), from a Status
    /// poll or a Settings fetch: adopted, unless it is an older answer to a
    /// change of ours still on its way, or a change for the next device
    /// waits for its sign-in.
    func deviceSaid(_ value: String?) {
        guard let value else {
            deviceKeepsIt = false
            return
        }
        deviceKeepsIt = true
        let before = choice
        let changed = choice.deviceSaid(value, at: now())
        if choice != before { save() }
        if changed { showChoice() }
    }

    /// A sign-in the device accepted (a password, stored credentials, a
    /// reconnect; OTCConnection, before that session's first Status or
    /// Settings): a device that was too old may have been updated, and a
    /// change that didn't reach it - or one made for the next device - goes
    /// now.
    func connected() {
        tooOld = false
        if choice.forNextDevice {
            choice.connected()
            save()
        }
        if sending { signedInWhileSending = true }
        sendPending()
    }

    /// The session ended without Log Out (the device turned the password
    /// down, or new credentials were saved): the device value seen goes.
    func sessionEnded() {
        choice.sessionEnded()
    }

    /// Log Out: the copy stays, and with it the language shown. `carryPending`:
    /// leaving a device from the cards that say it can't be reached or
    /// signed in to - a change still pending goes to the next device.
    func loggedOut(carryPending: Bool = false) {
        choice.loggedOut(carryPending: carryPending)
        tooOld = false
        deviceKeepsIt = nil
        save()
    }

    /// Hands the language to L10n when it differs from the one shown.
    private func showChoice() {
        let code = effective
        guard code != shown else { return }
        let before = shown
        show(code)
        shown = code
        if before != nil { device.languageChanged() }
    }

    private func sendPending() {
        guard !sending, choice.pending, !tooOld, device.signedIn() else { return }
        sending = true
        signedInWhileSending = false
        // The window counts from now, before the task runs: a Status
        // answered meanwhile (at a connection, the first poll) doesn't
        // undo a change made long ago that is only now being sent.
        choice.sending(at: now())
        save()
        Task { await sendLoop() }
    }

    /// One SetLanguage at a time, again while a newer choice waits or a
    /// sign-in came while one failed (at most three in a row; the rest goes
    /// at the next connection).
    private func sendLoop() async {
        defer { sending = false }
        for _ in 0..<3 {
            guard choice.pending, !tooOld, device.signedIn() else { return }
            let sent = choice.language
            let expected = choice.expected
            signedInWhileSending = false
            choice.sending(at: now())
            save()
            let answer: LanguageAnswer
            do {
                answer = .of(try await device.setLanguage(sent, expected))
            } catch {
                answer = .failed
            }
            switch answer {
            case .taken:
                deviceKeepsIt = true
                choice.taken(sent)
            case .changed:
                if let s = await device.settings(), s.hasLanguage {
                    choice.refused(sent, deviceHolds: s.language)
                } else {
                    choice.refused(sent)
                }
            case .tooOld:
                // Still pending: sent again at the next connection.
                tooOld = true
                deviceKeepsIt = false
                return
            case .failed:
                // Still pending: sent again at the next connection - now,
                // when one came while this was on its way.
                guard signedInWhileSending else { return }
            }
            save()
            showChoice()
        }
    }

    // MARK: Storage

    private enum Key {
        static let language = "language"
        static let pending = "pending"
        static let pendingAt = "pendingAt"
        static let expected = "expected"
        static let forNextDevice = "forNextDevice"
    }

    private static func load(_ d: UserDefaults) -> LanguageChoice {
        var c = LanguageChoice()
        c.language = d.string(forKey: Key.language) ?? ""
        c.pending = d.bool(forKey: Key.pending)
        if let at = d.object(forKey: Key.pendingAt) as? Double { c.pendingAt = Date(timeIntervalSince1970: at) }
        if c.pending {
            c.expected = d.string(forKey: Key.expected)
            c.forNextDevice = d.bool(forKey: Key.forNextDevice)
        }
        return c
    }

    private func save() {
        defaults.set(choice.language, forKey: Key.language)
        defaults.set(choice.pending, forKey: Key.pending)
        if let at = choice.pendingAt {
            defaults.set(at.timeIntervalSince1970, forKey: Key.pendingAt)
        } else {
            defaults.removeObject(forKey: Key.pendingAt)
        }
        if let expected = choice.expected {
            defaults.set(expected, forKey: Key.expected)
        } else {
            defaults.removeObject(forKey: Key.expected)
        }
        defaults.set(choice.forNextDevice, forKey: Key.forNextDevice)
    }
}

extension LanguageSettings.Device {
    /// Through the app's connection.
    static let live = Self(
        signedIn: { OTCConnection.shared.authenticated },
        setLanguage: { language, expected in
            try await OTCConnection.shared.request(timeout: 60) { e in
                var m = Msg_SetLanguage()
                m.language = language
                if let expected { m.expected = expected }
                e.payload = .reqSetLanguage(m)
            }
        },
        settings: {
            guard let resp = try? await OTCConnection.shared.request(timeout: 60, { $0.payload = .reqGetSettings(Msg_GetSettings()) }),
                  case .respSettings(let s) = resp.payload else { return nil }
            return s
        },
        languageChanged: { OTCConnection.shared.languageChanged() }
    )
}
