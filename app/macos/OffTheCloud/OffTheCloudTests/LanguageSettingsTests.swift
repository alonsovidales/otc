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
        var languageForNextDevice = false
        var hasDevice = true
    }

    /// A device keeping the language as the real one does: nil predates
    /// localization; a change is kept only while the stored value equals
    /// expected (when sent), else "changed".
    final class FakeDevice {
        var stored: String?
        var fail = false
        /// False: not connected - nothing reaches it.
        var reachable = true
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
        let send = s.nextSend(now: now)
        #expect(send?.language == "es" && send?.expected == "")
        let second = s.nextSend(now: now) // one at a time
        #expect(second == nil)
        let reread = s.answered(.ok, sent: "es")
        #expect(!reread)
        #expect(!s.pending && s.expected == nil && s.deviceSeen == "es")
        let more = s.nextSend(now: now)
        #expect(more == nil)
    }

    @Test func aPendingChangeOutweighsTheDeviceForAWhile() {
        let now = Date()
        var s = LanguageState()
        _ = s.deviceReported("", askedAt: s.epoch, now: now)
        _ = s.choose("es", now: now)
        _ = s.nextSend(now: now)
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

    /// The window runs from the change or its last send: a change made
    /// minutes before signing in whose first SetLanguage failed isn't
    /// replaced by the next poll, which may predate it.
    @Test func theWindowRunsFromTheLastSend() {
        let now = Date()
        var s = LanguageState()
        _ = s.deviceReported("", askedAt: s.epoch, now: now.addingTimeInterval(-700))
        _ = s.choose("es", now: now.addingTimeInterval(-600))
        #expect(!s.forNextDevice && s.expected == "")
        s.connected()
        _ = s.nextSend(now: now)
        _ = s.answered(.failed, sent: "es")
        let early = s.deviceReported("", askedAt: s.epoch, now: now.addingTimeInterval(5))
        #expect(!early && s.choice == "es" && s.pending)
        // Sent again at the next connection: the window starts again.
        s.connected()
        _ = s.nextSend(now: now.addingTimeInterval(60))
        _ = s.answered(.failed, sent: "es")
        let again = s.deviceReported("", askedAt: s.epoch, now: now.addingTimeInterval(85))
        #expect(!again && s.choice == "es" && s.pending)
        // Past it: the device's wins.
        let late = s.deviceReported("", askedAt: s.epoch, now: now.addingTimeInterval(91))
        #expect(late && s.choice == "" && !s.pending)
    }

    @Test func aStatusFromBeforeASetLanguageIsLeftOut() {
        let now = Date()
        var s = LanguageState(choice: "")
        _ = s.choose("es", now: now.addingTimeInterval(-3600))
        let asked = s.epoch
        _ = s.nextSend(now: now)
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
        _ = s.nextSend(now: now)
        _ = s.choose("fr", now: now)
        let second = s.nextSend(now: now)
        #expect(second == nil)
        let reread = s.answered(.ok, sent: "es")
        #expect(!reread)
        #expect(s.pending && s.expected == "es")
        let next = s.nextSend(now: now)
        #expect(next?.language == "fr" && next?.expected == "es")
    }

    @Test func changedElsewhereAndOldDevices() {
        let now = Date()
        var s = LanguageState()
        _ = s.choose("es", now: now)
        _ = s.nextSend(now: now)
        let reread = s.answered(.changed, sent: "es") // read the device again
        #expect(reread)
        #expect(!s.pending)
        let adopted = s.deviceReported("it", askedAt: s.epoch, now: now)
        #expect(adopted)
        #expect(s.choice == "it")

        _ = s.choose("pt", now: now)
        _ = s.nextSend(now: now)
        let rereadOld = s.answered(.unknownPayload, sent: "pt")
        #expect(!rereadOld)
        // Still pending: sent again at the next connection, when the
        // device may have been updated.
        #expect(s.pending && s.choice == "pt" && s.deviceTooOld)
        _ = s.choose("nl", now: now)
        let toOldDevice = s.nextSend(now: now) // not to a device that can't
        #expect(toOldDevice == nil)
        let adoptedNothing = s.deviceReported(nil, askedAt: s.epoch, now: now) // the copy stands
        #expect(!adoptedNothing)
        #expect(s.choice == "nl")
        s.connected()
        let afterReconnect = s.nextSend(now: now)
        #expect(afterReconnect?.language == "nl")
    }

    @Test func anotherDeviceForgetsWhatTheOldOneSaid() {
        let now = Date()
        var s = LanguageState()
        _ = s.deviceReported("es", askedAt: s.epoch, now: Date())
        _ = s.choose("de", now: Date()) // never reached the old device
        s.forgetDevice()
        // The copy stays; the change made against the old device's value
        // doesn't go to the new one.
        #expect(s.deviceSeen == nil && s.choice == "de" && !s.pending && s.expected == nil)
        #expect(s.nextSend(now: now) == nil)
        // Nothing of the new one seen yet: for the device signed in to next.
        _ = s.choose("fr", now: Date())
        #expect(s.expected == nil && s.forNextDevice)
    }

    @Test func aChangeWithNoDeviceGoesToTheNextOne() {
        let now = Date()
        var s = LanguageState()
        // Nothing set up: nothing to compare with.
        _ = s.choose("es", now: now, noDevice: true)
        #expect(s.pending && s.forNextDevice && s.expected == nil)
        // Not connected: the send fails, it stays.
        _ = s.nextSend(now: now)
        _ = s.answered(.failed, sent: "es")
        // A device and a password, then a wrong one typed again: it stays
        // for whichever signs in.
        s.forgetDevice()
        s.forgetDevice()
        #expect(s.pending && s.forNextDevice && s.expected == nil && s.choice == "es")
        // Chosen again before signing in: still for that device.
        _ = s.choose("fr", now: now)
        #expect(s.forNextDevice && s.expected == nil)
        // Signed in: this device's now - sent without expected.
        s.connected()
        #expect(!s.forNextDevice && s.pending)
        let send = s.nextSend(now: now)
        #expect(send?.language == "fr" && send?.expected == nil)
        _ = s.answered(.ok, sent: "fr")
        #expect(!s.pending && s.deviceSeen == "fr")
    }

    /// A device and password saved but never signed in to (a wrong
    /// password): nothing of it was seen, so a change is for the device
    /// signed in to next - Disconnect, the only way to fix the password,
    /// keeps it.
    @Test func aChangeBeforeSigningInToTheDeviceSetUpIsForTheNextOne() {
        let now = Date()
        var s = LanguageState()
        _ = s.choose("es", now: now)
        #expect(s.pending && s.forNextDevice && s.expected == nil)
        _ = s.nextSend(now: now) // not signed in: fails
        _ = s.answered(.failed, sent: "es")
        s.forgetDevice() // Disconnect
        s.forgetDevice() // the right password
        #expect(s.pending && s.forNextDevice && s.expected == nil && s.choice == "es")
        s.connected()
        let send = s.nextSend(now: now)
        #expect(send?.language == "es" && send?.expected == nil)
        _ = s.answered(.ok, sent: "es")
        // Its value seen: a change is made against it.
        _ = s.choose("fr", now: now)
        #expect(!s.forNextDevice && s.expected == "es")
    }

    @Test func aChangeForTheNextDeviceIsDroppedOnceItSignedIn() {
        let now = Date()
        var s = LanguageState()
        _ = s.choose("es", now: Date(), noDevice: true)
        s.forgetDevice()
        s.connected()
        _ = s.nextSend(now: now)
        _ = s.answered(.failed, sent: "es") // never reached that device
        s.forgetDevice() // another device: not for it
        #expect(!s.pending && !s.forNextDevice && s.choice == "es")
    }

    @Test func theWizardCarriesItsChoiceToTheDeviceItSetUp() {
        let now = Date()
        var s = LanguageState()
        _ = s.deviceReported("", askedAt: s.epoch, now: now) // signed in to device A
        _ = s.choose("de", now: now) // in the wizard: A takes it
        _ = s.nextSend(now: now)
        _ = s.answered(.ok, sent: "de")
        #expect(!s.pending)
        s.carryToNextDevice(now: now) // "Sync this Mac with my device"
        // A last status from A, saying the same, before the switch: not
        // the new device's word on it.
        let fromA = s.deviceReported("de", askedAt: s.epoch, now: now)
        #expect(!fromA && s.pending && s.forNextDevice)
        s.forgetDevice()
        #expect(s.pending && s.forNextDevice && s.expected == nil && s.deviceSeen == nil)
        s.connected()
        let send = s.nextSend(now: now)
        #expect(send?.language == "de" && send?.expected == nil)
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

    /// A fresh install: the language is picked in the connect form, the
    /// device is set up and signed in to - it gets the choice, whatever it
    /// had (a new one has Automatic), instead of the choice giving way to
    /// the device's value.
    @Test func aChoiceMadeBeforeTheFirstSignInReachesTheDevice() async {
        let store = FakeStore(), device = FakeDevice()
        store.hasDevice = false
        device.stored = ""
        device.reachable = false
        let settings = LanguageSettings(store: store)
        settings.send = { language, expected in device.reachable ? device.set(language, expected: expected) : .failed }
        let target = Self.has("es") ? "es" : "sv"

        settings.choose(target)
        await settle(settings)
        #expect(store.language == target && store.languagePendingSince != nil && store.languageForNextDevice)
        #expect(device.sets.isEmpty)

        // Connect: the device and password are applied, then it signs in
        // and polls its status, which still says Automatic.
        store.hasDevice = true
        settings.forgetDevice()
        device.reachable = true
        let asked = settings.epoch
        settings.connected()
        settings.deviceReported("", askedAt: asked)
        await settle(settings)

        #expect(device.sets == [target + "/-"] && device.stored == target)
        #expect(store.language == target && store.languagePendingSince == nil && !store.languageForNextDevice)
        // The next status says the same: nothing changes.
        settings.deviceReported(target, askedAt: settings.epoch)
        #expect(settings.choice == target)
        L10n.shared.set(L10nLanguage.english.code)
    }

    /// The same with a device saved: a wrong password, never signed in to.
    @Test func aChoiceMadeBeforeSigningInSurvivesDisconnect() async {
        let store = FakeStore(), device = FakeDevice()
        device.stored = "it"
        device.reachable = false
        let settings = LanguageSettings(store: store)
        settings.send = { language, expected in device.reachable ? device.set(language, expected: expected) : .failed }
        let target = Self.has("es") ? "es" : "sv"

        settings.choose(target)
        await settle(settings)
        #expect(store.languagePendingSince != nil && store.languageForNextDevice && store.languageExpected == nil)

        // Disconnect clears the address and password; then the right one.
        store.hasDevice = false
        settings.forgetDevice()
        store.hasDevice = true
        settings.forgetDevice()
        device.reachable = true
        let asked = settings.epoch
        settings.connected()
        settings.deviceReported("it", askedAt: asked)
        await settle(settings)

        #expect(device.sets == [target + "/-"] && device.stored == target)
        #expect(store.language == target && store.languagePendingSince == nil && !store.languageForNextDevice)
        L10n.shared.set(L10nLanguage.english.code)
    }

    /// Relaunched with a change still waiting for the next device.
    @Test func aChangeForTheNextDeviceSurvivesARelaunch() {
        let store = FakeStore()
        store.language = "fr"
        store.languagePendingSince = Date()
        store.languageForNextDevice = true
        let settings = LanguageSettings(store: store)
        settings.forgetDevice()
        #expect(settings.state.pending && settings.state.forNextDevice)
        // A stale flag without a pending change means nothing.
        let stale = FakeStore()
        stale.languageForNextDevice = true
        #expect(!LanguageSettings(store: stale).state.forNextDevice)
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
