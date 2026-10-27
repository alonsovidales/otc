// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Network
import Testing
import UserNotifications
@testable import OffTheCloud

// The sync and connection rules of the 2026-10 review (IOS-01, 06, 07, 09,
// A4, A6, storage_full), as Android's SyncRulesTest.kt.

struct WifiOnlyRuleTests {
    /// IOS-01: "Wi-Fi only" holds the sync on mobile data or a hotspot only.
    @Test func onlyWifiOnlyOnAnExpensivePathWaits() {
        #expect(PhotoSync.waitsForWifi(wifiOnly: true, expensive: true))
        #expect(!PhotoSync.waitsForWifi(wifiOnly: true, expensive: false))
        #expect(!PhotoSync.waitsForWifi(wifiOnly: false, expensive: true))
        #expect(!PhotoSync.waitsForWifi(wifiOnly: false, expensive: false))
    }

    @Test func saysWhyItWaits() {
        #expect(SyncHalt.waitingForWifi.title == S.appSettingsUploadsWaitingWifi)
        #expect(SyncHalt.waitingForWifi.detail == nil)
        #expect(SyncHalt.deviceFull("not enough space").title == S.appSettingsUploadsDeviceFull)
        #expect(SyncHalt.deviceFull("not enough space").detail == "not enough space")
        #expect(SyncHalt.deviceFull("").detail == nil)
    }
}

struct ListingRuleTests {
    /// IOS-09: the folder's listing only for a big run, or a phone that has
    /// synced nothing yet.
    @Test func smallRunsGoByHash() {
        #expect(!PhotoSync.needsListing(assets: 0, cacheEmpty: true))
        #expect(!PhotoSync.needsListing(assets: 0, cacheEmpty: false))
        #expect(!PhotoSync.needsListing(assets: 1, cacheEmpty: false))
        #expect(!PhotoSync.needsListing(assets: PhotoSync.listingFrom - 1, cacheEmpty: false))
        #expect(PhotoSync.needsListing(assets: PhotoSync.listingFrom, cacheEmpty: false))
        #expect(PhotoSync.needsListing(assets: 1, cacheEmpty: true))
        #expect(PhotoSync.listingFrom == 100)
    }

    @Test func theAssetCacheSaysWhetherItHoldsAnything() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let cache = AssetSyncCache(directory: dir)
        #expect(cache.isEmpty)
        cache.record(localIdentifier: "a", hash: "h")
        #expect(!cache.isEmpty)
        // As read back by the next launch.
        #expect(!AssetSyncCache(directory: dir).isEmpty)
        cache.clear()
        #expect(cache.isEmpty)
    }
}

struct DeviceFullTests {
    @Test func storageFullIsTheCode() {
        #expect(ErrorCodes.isStorageFull(code: "storage_full"))
        #expect(!ErrorCodes.isStorageFull(code: ""))
        #expect(!ErrorCodes.isStorageFull(code: "too_large"))
    }

    /// Automatic runs wait half an hour after the device said it was full;
    /// a clock set back doesn't hold them for ever.
    @Test func runsWaitAWhileAfterIt() {
        let since = Date(timeIntervalSince1970: 1_000_000)
        #expect(PhotoSync.waitsAfterFull(since: since, now: since))
        #expect(PhotoSync.waitsAfterFull(since: since, now: since + PhotoSync.fullPause - 1))
        #expect(!PhotoSync.waitsAfterFull(since: since, now: since + PhotoSync.fullPause))
        #expect(!PhotoSync.waitsAfterFull(since: since, now: since - 60))
    }
}

struct CleartextRuleTests {
    /// A6: ws:// and http:// only to a home-network host, as Android's.
    @Test func homeNetworkHostsMayGoUnencrypted() {
        for host in ["cala", "localhost", "cala.local", "CALA.LOCAL.", "nas.ts.net", "pi.tail1234.ts.net",
                     "10.0.0.5", "172.16.0.1", "172.31.255.255", "192.168.1.20", "127.0.0.1", "169.254.10.20",
                     "100.64.0.1", "100.127.255.254", "::1", "[::1]", "fe80::1", "fe80::1%en0", "fd7a:115c:a1e0::1", "fc00::5"] {
            #expect(CleartextRule.allowsCleartext(host: host), "\(host)")
        }
    }

    @Test func anythingElseMustBeEncrypted() {
        for host in ["", "cala.off-the.cloud", "example.com", "my.dyndns.org", "ts.net.evil.com", "cala.local.example.com", "8.8.8.8",
                     "172.32.0.1", "192.169.1.1", "100.128.0.1", "100.63.255.255", "11.0.0.1", "2001:db8::1",
                     "::ffff:192.168.1.1", "1.2.3", "1.2.3.4.5", "256.1.1.1", "192.168.001.1x", "10"] {
            #expect(!CleartextRule.allowsCleartext(host: host), "\(host)")
        }
    }

    @Test func encryptedAddressesAlwaysGo() throws {
        #expect(CleartextRule.allows(try #require(URL(string: "wss://cala.off-the.cloud/ws"))))
        #expect(CleartextRule.allows(try #require(URL(string: "https://example.com/media/x"))))
        #expect(CleartextRule.allows(try #require(URL(string: "ws://192.168.1.20:8080/ws"))))
        #expect(CleartextRule.allows(try #require(URL(string: "WS://otc.local:8080/ws"))))
        #expect(!CleartextRule.allows(try #require(URL(string: "ws://cala.off-the.cloud/ws"))))
        #expect(!CleartextRule.allows(try #require(URL(string: "http://example.com/media/x"))))
    }

    @Test func theRefusalReadsTheSameAsAppTransportSecuritys() {
        #expect(ConnectionProblem.cleartext.text == S.appShellCleartextRefused)
        #expect(ConnectionError(errorCode: 6, problem: .cleartext).errorDescription == S.appShellCleartextRefused)
    }
}

struct SavedToPhoneTests {
    @Test func whatSaveAddedIsRemembered() throws {
        let suite = "otc-tests-saved-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let saved = SavedToPhone(defaults: defaults)
        #expect(saved.ids().isEmpty)
        saved.remember("A/L0/001")
        saved.remember("B/L0/001")
        saved.remember("A/L0/001")
        saved.remember("")
        #expect(saved.ids() == ["A/L0/001", "B/L0/001"])
        // Another store over the same suite (the next launch) sees them.
        #expect(SavedToPhone(defaults: defaults).ids() == ["A/L0/001", "B/L0/001"])
    }

    /// Outside the app's own domain, which Log Out removes.
    @Test func keptApartFromWhatLogOutWipes() {
        #expect(SavedToPhone.suiteName != Bundle.main.bundleIdentifier)
        #expect(NotificationsAsk.suiteName != Bundle.main.bundleIdentifier)
    }
}

struct NotificationsAskTests {
    /// IOS-07: once, after the first sign-in, with nothing else on screen.
    @Test func askedOnceSignedInWithNothingElseUp() {
        #expect(NotificationsAsk.shouldAsk(signedIn: true, syncChoicePending: false, tourShown: false, mainVisible: true, undecided: true, askedBefore: false))
        #expect(!NotificationsAsk.shouldAsk(signedIn: false, syncChoicePending: false, tourShown: false, mainVisible: true, undecided: true, askedBefore: false))
        #expect(!NotificationsAsk.shouldAsk(signedIn: true, syncChoicePending: true, tourShown: false, mainVisible: true, undecided: true, askedBefore: false))
        #expect(!NotificationsAsk.shouldAsk(signedIn: true, syncChoicePending: false, tourShown: true, mainVisible: true, undecided: true, askedBefore: false))
        #expect(!NotificationsAsk.shouldAsk(signedIn: true, syncChoicePending: false, tourShown: false, mainVisible: false, undecided: true, askedBefore: false))
        #expect(!NotificationsAsk.shouldAsk(signedIn: true, syncChoicePending: false, tourShown: false, mainVisible: true, undecided: false, askedBefore: false))
        #expect(!NotificationsAsk.shouldAsk(signedIn: true, syncChoicePending: false, tourShown: false, mainVisible: true, undecided: true, askedBefore: true))
    }

    @Test func registeredAtLaunchOnlyOnceAllowed() {
        #expect(NotificationsAsk.allowed(.authorized))
        #expect(NotificationsAsk.allowed(.provisional))
        #expect(!NotificationsAsk.allowed(.notDetermined))
        #expect(!NotificationsAsk.allowed(.denied))
    }
}

// MARK: - IOS-06: a request is sent again only if it never left

/// A device that takes the WebSocket and drops the connection the moment a
/// request arrives, unanswered.
private final class DroppingDevice: @unchecked Sendable {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "test-dropping-device")

    init() throws {
        let ws = NWProtocolWebSocket.Options()
        ws.autoReplyPing = true
        let params = NWParameters.tcp
        params.defaultProtocolStack.applicationProtocols.insert(ws, at: 0)
        params.requiredLocalEndpoint = NWEndpoint.hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: params)
        listener.newConnectionHandler = { [weak self] conn in
            guard let self else { return }
            conn.start(queue: self.queue)
            conn.receiveMessage { data, _, _, _ in
                if data != nil { conn.cancel() }
            }
        }
    }

    func start() async throws -> URL {
        let port: UInt16 = try await withCheckedThrowingContinuation { cont in
            let once = Once()
            listener.stateUpdateHandler = { [listener] state in
                switch state {
                case .ready:
                    if once.first() { cont.resume(returning: listener.port?.rawValue ?? 0) }
                case .failed(let e):
                    if once.first() { cont.resume(throwing: e) }
                default:
                    break
                }
            }
            listener.start(queue: queue)
        }
        return URL(string: "ws://127.0.0.1:\(port)/ws")!
    }

    func stop() { listener.cancel() }
}

private final class Once: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func first() -> Bool { lock.withLock { defer { done = true }; return !done } }
}

@Suite(.serialized)
struct SentNoAnswerTests {
    @Test func aRequestThatLeftAndLostItsConnectionIsNotSentAgain() async throws {
        let device = try DroppingDevice()
        let url = try await device.start()
        defer { device.stop() }
        let c = WSClient(timing: .init(liveness: .init(pongWait: 0.3), tick: 0.05))
        try await c.connect(url: url)
        do {
            _ = try await c.request(timeout: 10) { $0.payload = .reqGetTags(Msg_GetTags()) }
            Issue.record("the device never answers: it should have failed")
        } catch let e as WSClient.SentNoAnswer {
            #expect(!OTCConnection.retriesAfter(e))
            #expect(e.localizedDescription == S.appShellSentNoAnswer)
        } catch {
            Issue.record("failed with \(error), not as sent with no answer")
        }
    }

    @Test func aRequestWithNoSocketNeverLeftAndGoesAgain() async {
        let c = WSClient()
        do {
            _ = try await c.request { $0.payload = .reqGetTags(Msg_GetTags()) }
            Issue.record("sent with no socket")
        } catch {
            #expect(error is WSClient.NotConnected)
            #expect(OTCConnection.retriesAfter(error))
        }
    }

    @Test func aTimeoutIsNotSentAgainEither() {
        #expect(!OTCConnection.retriesAfter(WSClient.TimedOut()))
        #expect(OTCConnection.retriesAfter(URLError(.networkConnectionLost)))
        #expect(OTCConnection.retriesAfter(ConnectionError(errorCode: 5, problem: .notAnswering)))
    }
}
