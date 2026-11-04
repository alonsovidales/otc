// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Waits (`seconds` at most) for `done`.
@MainActor
private func until(_ seconds: Double = 5, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 20_000_000)
    }
}

private let pinA = Data(repeating: 0xA1, count: 32)
private let pinB = Data(repeating: 0xB2, count: 32)

// MARK: - The rules

struct DeviceChangeRuleTests {
    @Test func theSameDeviceOnlyWhenBothPinsAreKnownAndEqual() {
        #expect(DeviceChange.verdict(before: pinA, now: pinA) == .sameDevice)
        #expect(DeviceChange.verdict(before: pinA, now: pinB) == .anotherDevice)
        // The new one can't say, or the last one never said.
        #expect(DeviceChange.verdict(before: pinA, now: nil) == .anotherDevice)
        #expect(DeviceChange.verdict(before: Data(), now: pinA) == .anotherDevice)
        #expect(DeviceChange.verdict(before: Data(), now: nil) == .anotherDevice)
    }

    @Test func aCheckWaitingKeepsTheDeviceItBeganWith() throws {
        let d = try #require(UserDefaults(suiteName: "DeviceChangeTests.\(UUID().uuidString)"))
        #expect(!DeviceChange.pending(d))
        #expect(DeviceChange.take(pin: pinA, defaults: d) == nil, "nothing waits: no verdict")
        DeviceChange.begin(before: pinA, defaults: d)
        // An address that never answered, then the last one again.
        DeviceChange.begin(before: nil, defaults: d)
        #expect(DeviceChange.pending(d))
        #expect(DeviceChange.take(pin: pinA, defaults: d) == .sameDevice)
        #expect(!DeviceChange.pending(d))
        // One that had no pin is another device whatever comes.
        DeviceChange.begin(before: nil, defaults: d)
        #expect(DeviceChange.take(pin: pinA, defaults: d) == .anotherDevice)
    }
}

// MARK: - Another address, signed in to

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct DeviceChangeSignInTests {
    /// Signs in to `a` (which says it is `pinA`), saves `b`'s address, signs
    /// in there, and gives what came of the check.
    private func change(to bPin: Data?, sameDevice: Bool = false) async throws -> (pendingAfterSave: Bool, asked: Bool, session: Int) {
        let c = OTCConnection.shared
        let a = try StandInAnswers()
        a.homePin = pinA
        let b = try StandInAnswers()
        b.homePin = bPin
        let deviceA = try DeviceStandIn(answersPings: true, reply: { a.reply($0) }) { _ in 0 }
        let deviceB = try DeviceStandIn(answersPings: true, reply: { b.reply($0) }) { _ in 0 }
        let urlA = try await deviceA.start()
        let urlB = try await deviceB.start()
        // Not the session models' reset: other suites read them meanwhile.
        DeviceChange.onAddressChange = {}
        defer {
            DeviceChange.onAddressChange = { DeviceChange.addressChanged() }
            deviceA.stop()
            deviceB.stop()
            LocalEndpoint.clear()
            UserDefaults.standard.removeObject(forKey: DeviceChange.pendingKey)
            UserDefaults.standard.set(false, forKey: SyncChoice.pendingKey)
            let s = SecretsStore.loadOrCreate()
            s.saveCredentials(endpoint: "", password: "")
            c.signInTimeout = 120
            c.reset()
        }
        let s = SecretsStore.loadOrCreate()
        s.saveCredentials(endpoint: urlA.absoluteString, password: "test-dummy")
        c.reset()
        c.signInTimeout = 5
        UserDefaults.standard.set(false, forKey: SyncChoice.pendingKey)
        _ = try await c.request(timeout: 10) { $0.payload = .reqGetTags(.init()) }
        await until { LocalEndpoint.stored(for: urlA.absoluteString) != nil }
        #expect(LocalEndpoint.stored(for: urlA.absoluteString)?.pin == pinA)
        #expect(!DeviceChange.pending, "signing in to the first device is no change")

        s.saveCredentials(endpoint: urlB.absoluteString, password: "test-dummy", sameDevice: sameDevice)
        let pendingAfterSave = DeviceChange.pending
        c.invalidate()
        _ = try await c.request(timeout: 10) { $0.payload = .reqGetTags(.init()) }
        await until { !DeviceChange.pending }
        #expect(!DeviceChange.pending)
        // What a verdict of another device asks is up at once.
        return (pendingAfterSave, SyncChoice.pending, s.deviceSession)
    }

    @Test func theSameDeviceAtAnotherAddressChangesNothing() async throws {
        guard SecretsStore.loadOrCreate().endpoint.isEmpty, HomeNetworkTests.keychainWorks else { return }
        let r = try await change(to: pinA)
        #expect(r.pendingAfterSave, "the sync waits for the new address's device to say who it is")
        #expect(r.session == 1, "the screens were made afresh")
        #expect(!r.asked, "the same device: what to sync isn't asked again")
    }

    @Test func anotherDeviceAsksWhatToSyncAgain() async throws {
        guard SecretsStore.loadOrCreate().endpoint.isEmpty, HomeNetworkTests.keychainWorks else { return }
        let r = try await change(to: pinB)
        #expect(r.pendingAfterSave)
        #expect(r.asked, "another device: what to send it is asked, as at a sign-in")
    }

    @Test func aDeviceThatCantSayIsTakenForAnotherOne() async throws {
        guard SecretsStore.loadOrCreate().endpoint.isEmpty, HomeNetworkTests.keychainWorks else { return }
        let r = try await change(to: nil)
        #expect(r.asked)
    }

    @Test func leavingTheBridgeIsNoChange() throws {
        guard SecretsStore.loadOrCreate().endpoint.isEmpty, HomeNetworkTests.keychainWorks else { return }
        let s = SecretsStore.loadOrCreate()
        // Not the session models' reset: other suites read them meanwhile.
        DeviceChange.onAddressChange = {}
        defer {
            DeviceChange.onAddressChange = { DeviceChange.addressChanged() }
            LocalEndpoint.clear()
            UserDefaults.standard.removeObject(forKey: DeviceChange.pendingKey)
            s.saveCredentials(endpoint: "", password: "")
        }
        s.saveCredentials(endpoint: "wss://cala.off-the.cloud/ws", password: "test-dummy")
        try #require(LocalEndpoint(HomeNetworkTests.answer(["192.168.1.20"]), endpoint: s.endpointURLString)).save()
        let session = s.deviceSession
        // The device itself gave its new address: nothing waits, the screens
        // stay, and its pin goes with it (the first sign-in there is pinned).
        s.saveCredentials(endpoint: "ws://192.168.1.20:8080/ws", password: "test-dummy", sameDevice: true)
        #expect(!DeviceChange.pending)
        #expect(s.deviceSession == session)
        #expect(LocalEndpoint.stored(for: s.endpointURLString)?.pin == HomeNetworkTests.goodPin)
        // Typed in Settings, the same address is a change of device until
        // the device says otherwise, and starts with nothing known.
        s.saveCredentials(endpoint: "wss://pit.off-the.cloud/ws", password: "test-dummy")
        #expect(DeviceChange.pending)
        #expect(s.deviceSession == session + 1)
        #expect(LocalEndpoint.stored(for: s.endpointURLString) == nil)
        // A password changed for the same address is none.
        s.saveCredentials(endpoint: "wss://Pit.off-the.cloud/ws", password: "another")
        #expect(s.deviceSession == session + 1)
    }
}
}
