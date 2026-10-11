// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// The user's language (docs/i18n.md, "The stored choice"): the rules of
/// LanguageState, and LanguageSettings against a fake store and a fake
/// device - never the app's real settings. The same cases as otc-sync's
/// engine/language_test.go. Runs in a scratch SwiftPM package holding only
/// LanguageSettings.swift and i18n/L10n.swift + Languages.swift (module
/// OffTheCloud), like SyncPathsTests; with a make i18n DRAFT=1
/// Languages.swift the cases that need more than English run too.
@MainActor
@Suite(.serialized)
final class LanguageSettingsTests {
    final class FakeStore: LanguageStore {
        var language = ""
        var languagePendingSince: Date?
        var languageExpected: String?
    }

    /// A device keeping the language as the real one does: nil predates
    /// localization; a change is kept only while the stored value equals
    /// expected (when sent), else "changed".
    final class FakeDevice {
        var stored: String?
        var fail = false
        var sets: [String] = []

        func set(_ language: String, expected: String?) -> LanguageState.Answer {
            sets.append(language + "/" + (expected ?? "-"))
            guard let stored else { return .unknownPayload }
            if fail { return .failed }
            if let expected, expected != stored { return .changed }
            self.stored = language
            return .ok
        }
    }

    private static func has(_ code: String) -> Bool { L10nLanguage.all.contains { $0.code == code } }
    private static func carriedOr(_ code: String) -> String { has(code) ? code : L10nLanguage.english.code }

    // MARK: Resolution

    @Test func effectiveLanguage() {
        #expect(LanguageSettings.effective("en") == "en")
        #expect(LanguageSettings.effective("es") == Self.carriedOr("es"))
        // Added after this build: shown in English, kept as the choice.
        #expect(LanguageSettings.effective("sv") == "en")
        // Automatic: the Mac's languages, by base language, never the
        // pseudo-locale.
        #expect(LanguageSettings.effective("", preferences: ["es-MX"]) == Self.carriedOr("es"))
        #expect(LanguageSettings.effective("", preferences: ["pt-BR"]) == Self.carriedOr("pt"))
        #expect(LanguageSettings.effective("", preferences: ["ca-ES", "es-ES"]) == Self.carriedOr("es"))
        #expect(LanguageSettings.effective("", preferences: ["ja-JP"]) == "en")
        #expect(LanguageSettings.effective("", preferences: ["en-US"]) == "en")
        #expect(LanguageSettings.choosable == (L10nLanguage.all.count > 1))
    }

    // MARK: The rules

    @Test func aChangeIsPendingUntilAcknowledged() {
        let now = Date()
        var s = LanguageState()
        let seen = s.deviceReported("", askedAt: s.epoch, now: now)
        #expect(!seen)
        let chosen = s.choose("es", now: now)
        #expect(chosen)
        let chosenAgain = s.choose("es", now: now) // already the choice
        #expect(!chosenAgain)
        #expect(s.pending && s.expected == "")
        let send = s.nextSend()
        #expect(send?.language == "es" && send?.expected == "")
        let second = s.nextSend() // one at a time
        #expect(second == nil)
        let reread = s.answered(.ok, sent: "es")
        #expect(!reread)
        #expect(!s.pending && s.expected == nil && s.deviceSeen == "es")
        let more = s.nextSend()
        #expect(more == nil)
    }

    @Test func aPendingChangeOutweighsTheDeviceForAWhile() {
        let now = Date()
        var s = LanguageState()
        _ = s.deviceReported("", askedAt: s.epoch, now: now)
        _ = s.choose("es", now: now)
        _ = s.nextSend()
        let reread = s.answered(.failed, sent: "es")
        #expect(!reread)
        #expect(s.pending)
        // Within the window: from before the change.
        let adoptedEarly = s.deviceReported("", askedAt: s.epoch, now: now.addingTimeInterval(10))
        #expect(!adoptedEarly)
        #expect(s.choice == "es" && s.pending)
        // Past it: the device's wins.
        let adoptedLate = s.deviceReported("", askedAt: s.epoch, now: now.addingTimeInterval(31))
        #expect(adoptedLate)
        #expect(s.choice == "" && !s.pending)
        // Its own value seen clears it.
        _ = s.choose("fr", now: now)
        let adoptedOwn = s.deviceReported("fr", askedAt: s.epoch, now: now)
        #expect(!adoptedOwn)
        #expect(!s.pending && s.choice == "fr")
    }

    @Test func aStatusFromBeforeASetLanguageIsLeftOut() {
        let now = Date()
        var s = LanguageState(choice: "")
        _ = s.choose("es", now: now.addingTimeInterval(-3600))
        let asked = s.epoch
        _ = s.nextSend()
        // Answered while it is under way, or after: either way it may
        // predate the change.
        let whileSending = s.deviceReported("", askedAt: asked, now: now)
        #expect(!whileSending)
        _ = s.answered(.ok, sent: "es")
        let afterAnswer = s.deviceReported("", askedAt: asked, now: now)
        #expect(!afterAnswer)
        #expect(s.choice == "es" && s.deviceSeen == "es")
        // One asked for since is the device's word.
        let adopted = s.deviceReported("it", askedAt: s.epoch, now: now)
        #expect(adopted)
        #expect(s.choice == "it")
    }

    @Test func aChangeMadeWhileSendingGoesNextAgainstTheValueSent() {
        let now = Date()
        var s = LanguageState()
        _ = s.deviceReported("", askedAt: s.epoch, now: now)
        _ = s.choose("es", now: now)
        _ = s.nextSend()
        _ = s.choose("fr", now: now)
        let second = s.nextSend()
        #expect(second == nil)
        let reread = s.answered(.ok, sent: "es")
        #expect(!reread)
        #expect(s.pending && s.expected == "es")
        let next = s.nextSend()
        #expect(next?.language == "fr" && next?.expected == "es")
    }

    @Test func changedElsewhereAndOldDevices() {
        let now = Date()
        var s = LanguageState()
        _ = s.choose("es", now: now)
        _ = s.nextSend()
        let reread = s.answered(.changed, sent: "es") // read the device again
        #expect(reread)
        #expect(!s.pending)
        let adopted = s.deviceReported("it", askedAt: s.epoch, now: now)
        #expect(adopted)
        #expect(s.choice == "it")

        _ = s.choose("pt", now: now)
        _ = s.nextSend()
        let rereadOld = s.answered(.unknownPayload, sent: "pt")
        #expect(!rereadOld)
        // Still pending: sent again at the next connection, when the
        // device may have been updated.
        #expect(s.pending && s.choice == "pt" && s.deviceTooOld)
        _ = s.choose("nl", now: now)
        let toOldDevice = s.nextSend() // not to a device that can't
        #expect(toOldDevice == nil)
        let adoptedNothing = s.deviceReported(nil, askedAt: s.epoch, now: now) // the copy stands
        #expect(!adoptedNothing)
        #expect(s.choice == "nl")
        s.connected()
        let afterReconnect = s.nextSend()
        #expect(afterReconnect?.language == "nl")
    }

    @Test func anotherDeviceForgetsWhatTheOldOneSaid() {
        var s = LanguageState()
        _ = s.deviceReported("es", askedAt: s.epoch, now: Date())
        _ = s.choose("de", now: Date()) // never reached the old device
        s.forgetDevice()
        // The copy stays; the change made against the old device's value
        // doesn't go to the new one.
        #expect(s.deviceSeen == nil && s.choice == "de" && !s.pending && s.expected == nil)
        #expect(s.nextSend() == nil)
        _ = s.choose("fr", now: Date())
        #expect(s.expected == nil)
    }

    // MARK: LanguageSettings

    /// Waits for the SetLanguage tasks to settle.
    private func settle(_ settings: LanguageSettings) async {
        for _ in 0..<200 {
            await Task.yield()
            if settings.state.sending == nil { return }
            try? await Task.sleep(for: .milliseconds(5))
        }
    }

    @Test func choosingSavesShowsAndSends() async {
        let store = FakeStore(), device = FakeDevice()
        device.stored = ""
        let settings = LanguageSettings(store: store)
        settings.send = { language, expected in device.set(language, expected: expected) }
        settings.deviceReported("", askedAt: settings.epoch)
        let target = Self.has("es") ? "es" : "en"

        settings.choose(target)
        #expect(store.language == target && store.languagePendingSince != nil)
        #expect(L10n.shared.code == target)
        await settle(settings)

        #expect(device.sets == [target + "/"])
        #expect(store.languagePendingSince == nil && store.languageExpected == nil)
        #expect(settings.shownChoice == target)

        // A language this build doesn't have: kept, shown as English.
        settings.choose("sv")
        #expect(store.language == "sv" && settings.shownChoice == "en" && L10n.shared.code == "en")
        await settle(settings)
        L10n.shared.set(L10nLanguage.english.code)
    }

    @Test func aPendingChangeSurvivesARelaunchAndGoesAtConnect() async {
        let store = FakeStore(), device = FakeDevice()
        store.language = "de"
        store.languagePendingSince = Date().addingTimeInterval(-3600)
        store.languageExpected = ""
        device.stored = ""
        let settings = LanguageSettings(store: store)
        settings.send = { language, expected in device.set(language, expected: expected) }
        #expect(settings.choice == "de" && settings.state.pending)

        let asked = settings.epoch
        settings.connected()
        // The status the connection polls first comes back meanwhile.
        settings.deviceReported("", askedAt: asked)
        await settle(settings)

        #expect(device.sets == ["de/"])
        #expect(store.language == "de" && store.languagePendingSince == nil)
        L10n.shared.set(L10nLanguage.english.code)
    }

    @Test func changedReadsTheDeviceAgain() async {
        let store = FakeStore(), device = FakeDevice()
        device.stored = "it"
        let settings = LanguageSettings(store: store)
        settings.send = { language, expected in device.set(language, expected: expected) }
        settings.reread = { [weak settings] in
            guard let settings else { return }
            settings.deviceReported(device.stored, askedAt: settings.epoch)
        }
        settings.deviceReported("", askedAt: settings.epoch) // seen before another app changed it
        device.stored = "it"
        settings.choose("es")
        await settle(settings)

        #expect(device.stored == "it" && store.language == "it" && store.languagePendingSince == nil)
        L10n.shared.set(L10nLanguage.english.code)
    }
}
