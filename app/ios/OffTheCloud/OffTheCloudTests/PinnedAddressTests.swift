// SPDX-License-Identifier: AGPL-3.0-or-later

import CryptoKit
import Foundation
import Network
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

/// A device for the stand-ins (plain DeviceStandIn on ws://, TestServer's
/// TLS device): what it answers, and what it was asked, by kind.
final class StandInAnswers: @unchecked Sendable {
    let key: Data
    private let lock = NSLock()
    private var _asked: [String] = []
    private var _homePin: Data?
    private var _homeAnswer: String = "endpoint"
    private var _storageGone = false
    /// Where its home-network answer says it is: a private address (the only
    /// kind kept) where nothing answers, so a race there ends at its budget.
    var homeAddresses = ["10.255.255.1"]
    var homePort: Int32 = 1

    init() throws { key = try StandInDisks.publicKeyDER() }

    var asked: [String] { lock.withLock { _asked } }
    func count(_ kind: String) -> Int { asked.filter { $0 == kind }.count }

    /// GetLocalEndpoint's answer: its pin (nil: "unknown_payload", a device
    /// from before the home network).
    var homePin: Data? {
        get { lock.withLock { _homePin } }
        set { lock.withLock { _homePin = newValue } }
    }

    var storageGone: Bool {
        get { lock.withLock { _storageGone } }
        set { lock.withLock { _storageGone = newValue } }
    }

    func reply(_ req: Msg_ReqEnvelope) -> Msg_RespEnvelope? {
        var r = Msg_RespEnvelope()
        let kind: String
        switch req.payload {
        case .reqGetPubKey: kind = "pubkey"
        case .reqAuth: kind = "auth"
        case .reqGetLocalEndpoint: kind = "local"
        case .reqGetSettings: kind = "settings"
        default: kind = "other"
        }
        lock.withLock { _asked.append(kind) }
        if storageGone {
            r.error = true
            r.errorCode = "storage_unavailable"
            r.errorMessage = "Words the phone must not show as its own (test)"
            return r
        }
        switch req.payload {
        case .reqGetPubKey:
            var pk = Msg_PubKey()
            pk.publicKey = key
            r.payload = .respPubKey(pk)
        case .reqAuth:
            var a = Msg_Ack()
            a.ok = true
            r.payload = .respAck(a)
        case .reqGetLocalEndpoint:
            guard let pin = homePin else {
                r.error = true
                r.errorCode = "unknown_payload"
                return r
            }
            var m = Msg_LocalEndpoint()
            m.addresses = homeAddresses
            m.port = homePort
            m.certSha256 = pin
            r.payload = .respLocalEndpoint(m)
        case .reqGetSettings:
            r.payload = .respSettings(Msg_Settings())
        default:
            var a = Msg_Ack()
            a.ok = true
            r.payload = .respAck(a)
        }
        return r
    }
}

/// A free TCP port on loopback that refuses connections.
private func refusingPort() throws -> Int {
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    defer { close(fd) }
    var addr = sockaddr_in()
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = 0
    addr.sin_addr.s_addr = inet_addr("127.0.0.1")
    var len = socklen_t(MemoryLayout<sockaddr_in>.size)
    let bound = withUnsafePointer(to: &addr) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, len) } }
    guard bound == 0 else { throw URLError(.cannotConnectToHost) }
    _ = withUnsafeMutablePointer(to: &addr) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(fd, $0, &len) } }
    return Int(UInt16(bigEndian: addr.sin_port))
}

// MARK: - The rules

struct PinnedAddressRuleTests {
    @Test func onlyAWsAddressWithAPinGoesThePinnedWayAlone() throws {
        let pinned = LocalEndpoint(endpoint: "e", addresses: ["192.168.1.20"], port: 8443, pin: HomeNetworkTests.goodPin)
        #expect(OTCConnection.pinnedOnly(try #require(URL(string: "ws://192.168.1.20:8080/ws")), stored: pinned))
        #expect(OTCConnection.pinnedOnly(try #require(URL(string: "WS://otc.local:8080/ws")), stored: pinned))
        // Through the bridge: the bridge is the way when home isn't.
        #expect(!OTCConnection.pinnedOnly(try #require(URL(string: "wss://cala.off-the.cloud/ws")), stored: pinned))
        // A first sign-in: nothing known yet.
        #expect(!OTCConnection.pinnedOnly(try #require(URL(string: "ws://192.168.1.20:8080/ws")), stored: nil))
    }

    @Test func theAddresssOwnHostIsRacedToo() {
        let ep = LocalEndpoint(endpoint: "e", addresses: ["192.168.1.20"], port: 8443, pin: HomeNetworkTests.goodPin)
        #expect(ep.including(host: "otc.local").addresses == ["192.168.1.20", "otc.local"])
        #expect(ep.including(host: "otc.local").socketURL(for: "otc.local")?.absoluteString == "wss://otc.local:8443/ws")
        // Already listed, nothing, or in brackets (IPv6).
        #expect(ep.including(host: "192.168.1.20") == ep)
        #expect(ep.including(host: nil) == ep)
        #expect(ep.including(host: "") == ep)
        #expect(ep.including(host: "[fd00::20]").addresses == ["192.168.1.20", "fd00::20"])
        #expect(ep.including(host: "[fd00::20]").socketURL(for: "fd00::20")?.absoluteString == "wss://[fd00::20]:8443/ws")
    }

    @Test func overTheHomeLinkOnlyNewAddressesCount() throws {
        let endpoint = "ws://otc.local:8080/ws"
        var ok = Msg_RespEnvelope()
        ok.respLocalEndpoint = HomeNetworkTests.answer(["192.168.1.21"])
        let expected = try #require(LocalEndpoint(HomeNetworkTests.answer(["192.168.1.21"]), endpoint: endpoint))
        #expect(LocalEndpoint.updateOverHome(for: ok, endpoint: endpoint) == .store(expected))
        // The pin is never dropped for what is said there.
        for code in ["unknown_payload", "local_unavailable"] {
            var e = Msg_RespEnvelope()
            e.error = true
            e.errorCode = code
            #expect(LocalEndpoint.update(for: e, endpoint: endpoint) == .clear)
            #expect(LocalEndpoint.updateOverHome(for: e, endpoint: endpoint) == .keep)
        }
    }

    @Test(.enabled(if: HomeNetworkTests.keychainWorks)) func leavingTheBridgeKeepsThePinForTheHomeAddress() throws {
        let bridge = "wss://cala.off-the.cloud/ws"
        let home = "ws://192.168.1.20:8080/ws"
        let saved = Keychain.loadString(key: "local_endpoint")
        defer {
            if let saved { Keychain.saveString(key: "local_endpoint", value: saved) } else { LocalEndpoint.clear() }
        }
        try #require(LocalEndpoint(HomeNetworkTests.answer(["192.168.1.20"]), endpoint: bridge)).save()
        LocalEndpoint.move(from: bridge, to: home)
        let moved = try #require(LocalEndpoint.stored(for: home))
        #expect(moved.pin == HomeNetworkTests.goodPin)
        #expect(moved.addresses == ["192.168.1.20"])
        #expect(LocalEndpoint.stored(for: bridge) == nil)
        // Nothing stored for where it comes from: nothing for where it goes,
        // and another address's goes, as when any other address is saved.
        let other = "wss://pit.off-the.cloud/ws"
        try #require(LocalEndpoint(HomeNetworkTests.answer(["192.168.1.30"]), endpoint: other)).save()
        LocalEndpoint.move(from: "wss://tik.off-the.cloud/ws", to: home)
        #expect(LocalEndpoint.stored(for: home) == nil)
        #expect(LocalEndpoint.stored(for: other) == nil)
    }
}

// MARK: - Signing in to a pinned ws:// address

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct PinnedAddressSignInTests {
    /// The app's own connection, at `endpoint`, with `stored` as what it knows
    /// of the device's home network; `body` runs, and everything is put back.
    private func signedOff(endpoint: String, stored: LocalEndpoint?, _ body: (OTCConnection) async throws -> Void) async throws {
        let c = OTCConnection.shared
        let s = SecretsStore.loadOrCreate()
        s.saveCredentials(endpoint: endpoint, password: "test-dummy")
        stored?.save()
        c.reset()
        c.signInTimeout = 5
        defer {
            LocalEndpoint.clear()
            UserDefaults.standard.removeObject(forKey: DeviceChange.pendingKey)
            let s = SecretsStore.loadOrCreate()
            s.saveCredentials(endpoint: "", password: "")
            c.signInTimeout = 120
            c.reset()
        }
        try await body(c)
    }

    @Test func thePasswordNeverGoesInTheClearToAnAddressWhoseDeviceIsKnown() async throws {
        // Never on a simulator signed in to a device: this configures the
        // app's own connection (a fresh test simulator has none).
        guard SecretsStore.loadOrCreate().endpoint.isEmpty, HomeNetworkTests.keychainWorks else { return }
        // Whoever holds the address on another network: answers a key, and
        // would take the password.
        let answers = try StandInAnswers()
        let impostor = try DeviceStandIn(answersPings: true, reply: { answers.reply($0) }) { _ in 0 }
        let url = try await impostor.start()
        defer { impostor.stop() }
        // The device's home-network listener isn't there (another
        // certificate answers): the pinned way finds nothing.
        let other = try await TestServer.start(HomeNetworkTests.badP12, mode: .raw)
        defer { other.stop() }
        let known = LocalEndpoint(endpoint: url.absoluteString, addresses: ["127.0.0.1"], port: other.port, pin: HomeNetworkTests.goodPin)
        try await signedOff(endpoint: url.absoluteString, stored: known) { c in
            await #expect(throws: (any Error).self) {
                _ = try await c.request(timeout: 10) { $0.payload = .reqGetTags(.init()) }
            }
            // Asked who it is, never sent the password.
            #expect(answers.count("pubkey") >= 1)
            #expect(answers.count("auth") == 0, "no password to an address whose device the pin doesn't match")
            #expect(!c.authenticated)
            #expect(c.connectionFailed)
            #expect(c.lastError == S.appShellNotKnownDevice(button: S.appShellLeaveButton))
            // Nothing went to the impostor's TLS after its handshake either.
            #expect(other.receivedBytes == 0)
            // And no media in the clear for that address.
            MediaStream.reset()
            #expect(await MediaStream.absolute("/media/t") == nil)
        }
    }

    @Test func theDeviceSignsInOverItsPinnedLink() async throws {
        guard SecretsStore.loadOrCreate().endpoint.isEmpty, HomeNetworkTests.keychainWorks else { return }
        let plain = try StandInAnswers()
        let address = try DeviceStandIn(answersPings: true, reply: { plain.reply($0) }) { _ in 0 }
        let url = try await address.start()
        defer { address.stop() }
        // The device itself, at home, with its own certificate.
        let device = try StandInAnswers()
        device.homePin = HomeNetworkTests.goodPin
        let home = try await TestServer.start(HomeNetworkTests.goodP12, mode: .webSocketDevice({ device.reply($0) }))
        defer { home.stop() }
        device.homePort = Int32(home.port)
        let known = LocalEndpoint(endpoint: url.absoluteString, addresses: ["127.0.0.1"], port: home.port, pin: HomeNetworkTests.goodPin)
        try await signedOff(endpoint: url.absoluteString, stored: known) { c in
            _ = try await c.request(timeout: 10) { $0.payload = .reqGetTags(.init()) }
            #expect(c.authenticated)
            #expect(c.route == .home)
            #expect(device.count("auth") == 1)
            // The ws:// address itself was never dialled.
            #expect(plain.asked.isEmpty)
            #expect(!c.signedInInClear)
        }
    }

    @Test func aDeviceWithoutItsStorageIsSaidInTheAppsWords() async throws {
        guard SecretsStore.loadOrCreate().endpoint.isEmpty, HomeNetworkTests.keychainWorks else { return }
        // Issue #207: a device without its storage serves no home-network
        // listener; at its ws:// address it says so, which can't be checked.
        let answers = try StandInAnswers()
        answers.storageGone = true
        let address = try DeviceStandIn(answersPings: true, reply: { answers.reply($0) }) { _ in 0 }
        let url = try await address.start()
        defer { address.stop() }
        let known = LocalEndpoint(endpoint: url.absoluteString, addresses: ["127.0.0.1"], port: try refusingPort(), pin: HomeNetworkTests.goodPin)
        try await signedOff(endpoint: url.absoluteString, stored: known) { c in
            c.storageHold = 60
            defer { c.storageHold = StorageWait.hold }
            await #expect(throws: (any Error).self) {
                _ = try await c.request(timeout: 10) { $0.payload = .reqGetTags(.init()) }
            }
            #expect(c.statusCode == "storage_unavailable")
            #expect(c.lastError == S.appShellNoStorage)
            #expect(answers.count("auth") == 0)
        }
    }

    @Test func aFirstSignInLearnsThePinAndTheNextGoesNoOtherWay() async throws {
        guard SecretsStore.loadOrCreate().endpoint.isEmpty, HomeNetworkTests.keychainWorks else { return }
        let answers = try StandInAnswers()
        answers.homePin = HomeNetworkTests.goodPin
        answers.homePort = Int32(try refusingPort())
        let address = try DeviceStandIn(answersPings: true, reply: { answers.reply($0) }) { _ in 0 }
        let url = try await address.start()
        defer { address.stop() }
        try await signedOff(endpoint: url.absoluteString, stored: nil) { c in
            // Nothing known of the device: its first sign-in goes as before.
            _ = try await c.request(timeout: 10) { $0.payload = .reqGetTags(.init()) }
            #expect(c.authenticated)
            #expect(c.signedInInClear)
            #expect(answers.count("auth") == 1)
            // ...and learns its certificate.
            await until { LocalEndpoint.stored(for: url.absoluteString) != nil }
            #expect(LocalEndpoint.stored(for: url.absoluteString)?.pin == HomeNetworkTests.goodPin)
            // From then on it goes only the pinned way: here nothing holds
            // the certificate, so whatever answers isn't sent the password.
            c.invalidate()
            await #expect(throws: (any Error).self) {
                _ = try await c.request(timeout: 15) { $0.payload = .reqGetTags(.init()) }
            }
            #expect(answers.count("auth") == 1)
            #expect(c.lastError == S.appShellNotKnownDevice(button: S.appShellLeaveButton))
        }
    }
}
}
