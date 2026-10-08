// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Start at login only with consent (App Store guideline 2.4.5(iii)).
/// Against a fake login item and a throwaway UserDefaults suite: never the
/// app's real registration or settings. Runs in a scratch SwiftPM package
/// holding only LoginItem.swift (module OffTheCloud), like SyncPathsTests.
@MainActor
@Suite(.serialized)
final class LoginItemTests {
    /// One throwaway suite, emptied before each test (Swift Testing makes an
    /// instance per test; serialized, so they never share it at once) and
    /// removed after.
    nonisolated static let suite = "OffTheCloudTests.LoginItem"

    deinit { UserDefaults().removePersistentDomain(forName: Self.suite) }

    final class FakeLoginItem: LoginItemService {
        var state: LoginItemState
        var registers = 0
        var unregisters = 0
        var opened = 0
        var failUnregister = false

        init(_ state: LoginItemState) { self.state = state }

        func register() throws {
            registers += 1
            state = .enabled
        }

        func unregister() throws {
            unregisters += 1
            if failUnregister { throw CocoaError(.featureUnsupported) }
            state = .notRegistered
        }

        func openSystemSettings() { opened += 1 }
    }

    private func defaults() -> UserDefaults {
        let d = UserDefaults(suiteName: Self.suite)!
        d.removePersistentDomain(forName: Self.suite)
        return d
    }

    @Test func firstRunRegistersNothingAndWaitsForAConnection() {
        let d = defaults(), item = FakeLoginItem(.notRegistered)
        let login = LoginItemSettings(defaults: d, service: item)
        login.launch()
        #expect(item.registers == 0)
        #expect(!login.isOn)
        #expect(login.choice == nil)
        #expect(!login.offerDue)
        login.deviceConnected()
        #expect(login.offerDue)
        #expect(item.registers == 0)
    }

    @Test func startAtLoginRegistersAndIsRecorded() {
        let d = defaults(), item = FakeLoginItem(.notRegistered)
        let login = LoginItemSettings(defaults: d, service: item)
        login.launch()
        login.deviceConnected()
        login.choose(true)
        #expect(item.registers == 1)
        #expect(login.isOn)
        #expect(!login.offerDue)
        #expect(d.object(forKey: LoginItemSettings.choiceKey) as? Bool == true)
    }

    @Test func notNowRecordsTheChoiceAndRegistersNothing() {
        let d = defaults(), item = FakeLoginItem(.notRegistered)
        let login = LoginItemSettings(defaults: d, service: item)
        login.launch()
        login.deviceConnected()
        login.choose(false)
        #expect(item.registers == 0)
        #expect(!login.offerDue)
        #expect(d.object(forKey: LoginItemSettings.choiceKey) as? Bool == false)
        // And the next launch neither asks nor registers.
        let again = LoginItemSettings(defaults: d, service: item)
        again.launch()
        again.deviceConnected()
        #expect(!again.offerDue)
        #expect(item.registers == 0)
    }

    @Test func consentReassertsADroppedRegistrationAtLaunch() {
        let d = defaults(), item = FakeLoginItem(.notRegistered)
        d.set(true, forKey: LoginItemSettings.choiceKey)
        let login = LoginItemSettings(defaults: d, service: item)
        login.launch()
        #expect(item.registers == 1)
        #expect(login.isOn)
    }

    @Test func switchedOffInSystemSettingsIsLeftAlone() {
        let d = defaults(), item = FakeLoginItem(.requiresApproval)
        d.set(true, forKey: LoginItemSettings.choiceKey)
        let login = LoginItemSettings(defaults: d, service: item)
        login.launch()
        #expect(item.registers == 0)
        #expect(item.unregisters == 0)
        #expect(!login.isOn)
        #expect(login.offInSystemSettings)
        // Ticking the box again sends the owner to System Settings rather
        // than registering over their choice there.
        login.choose(true)
        #expect(item.registers == 0)
        #expect(item.opened == 1)
        #expect(item.state == .requiresApproval)
    }

    @Test func legacySelfRegistrationIsRemovedAndOfferedOnce() {
        // 1.0 wrote startAtLogin = true and registered itself, unasked.
        let d = defaults(), item = FakeLoginItem(.enabled)
        d.set(true, forKey: LoginItemSettings.legacyKey)
        let login = LoginItemSettings(defaults: d, service: item)
        login.launch()
        #expect(item.unregisters == 1)
        #expect(item.registers == 0)
        #expect(!login.isOn)
        #expect(login.choice == nil)
        #expect(d.object(forKey: LoginItemSettings.legacyKey) == nil)
        login.deviceConnected()
        #expect(login.offerDue)
    }

    @Test func legacyKeyWithNothingRegisteredUnregistersNothing() {
        let d = defaults(), item = FakeLoginItem(.notRegistered)
        d.set(true, forKey: LoginItemSettings.legacyKey)
        let login = LoginItemSettings(defaults: d, service: item)
        login.launch()
        #expect(item.unregisters == 0)
        #expect(d.object(forKey: LoginItemSettings.legacyKey) == nil)
    }

    @Test func legacyUnregisterFailureIsRetriedNextLaunch() {
        let d = defaults(), item = FakeLoginItem(.enabled)
        item.failUnregister = true
        d.set(true, forKey: LoginItemSettings.legacyKey)
        LoginItemSettings(defaults: d, service: item).launch()
        #expect(d.object(forKey: LoginItemSettings.legacyKey) as? Bool == true)
        item.failUnregister = false
        LoginItemSettings(defaults: d, service: item).launch()
        #expect(item.state == .notRegistered)
        #expect(d.object(forKey: LoginItemSettings.legacyKey) == nil)
    }

    @Test func legacyUntickedBoxIsAnExplicitNo() {
        let d = defaults(), item = FakeLoginItem(.notRegistered)
        d.set(false, forKey: LoginItemSettings.legacyKey)
        let login = LoginItemSettings(defaults: d, service: item)
        login.launch()
        login.deviceConnected()
        #expect(login.choice == false)
        #expect(!login.offerDue)
        #expect(item.registers == 0)
    }

    @Test func recordedChoiceWinsOverTheLegacyKey() {
        let d = defaults(), item = FakeLoginItem(.enabled)
        d.set(true, forKey: LoginItemSettings.choiceKey)
        d.set(true, forKey: LoginItemSettings.legacyKey)
        let login = LoginItemSettings(defaults: d, service: item)
        login.launch()
        #expect(item.unregisters == 0)
        #expect(login.isOn)
        #expect(d.object(forKey: LoginItemSettings.legacyKey) == nil)
    }

    @Test func untickingUnregisters() {
        let d = defaults(), item = FakeLoginItem(.enabled)
        d.set(true, forKey: LoginItemSettings.choiceKey)
        let login = LoginItemSettings(defaults: d, service: item)
        login.launch()
        login.choose(false)
        #expect(item.unregisters == 1)
        #expect(!login.isOn)
        #expect(login.choice == false)
    }
}
