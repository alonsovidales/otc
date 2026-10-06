// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Network
import Security
import Testing
@testable import OffTheCloud

// Issue #190: the home-network route. The certificates are throwaway test
// ones (self-signed P-256, CN otc-lan, like a device's), made for these
// tests only: `device` also comes with its key (PKCS#12, password "test")
// for a listener on 127.0.0.1, and `other` stands in for an impostor.
private enum TestCerts {
    static let deviceDER = b64("""
        MIIBpDCCAUqgAwIBAgIUVnAf5++ullclE1wrRJgVy1AW4qIwCgYIKoZIzj0EAwIwEjEQMA4GA1UE
        AwwHb3RjLWxhbjAgFw0yNjEwMDYwODQ0NTJaGA8yMTI2MDkxMjA4NDQ1MlowEjEQMA4GA1UEAwwH
        b3RjLWxhbjBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABBZLRrzf73ef8KzzClK27bH5YP4yffvl
        mELAhW9RO1cZw0pZS8mLftVZUbU62PefPYXORMIigDmWIMxz/HhVj2ijfDB6MB0GA1UdDgQWBBQJ
        8Tuv//gE9BIuHDsXfta4+Bc1GjAfBgNVHSMEGDAWgBQJ8Tuv//gE9BIuHDsXfta4+Bc1GjAPBgNV
        HRMBAf8EBTADAQH/MBIGA1UdEQQLMAmCB290Yy1sYW4wEwYDVR0lBAwwCgYIKwYBBQUHAwEwCgYI
        KoZIzj0EAwIDSAAwRQIhALyQDoLeYWf5HPH9p41UCnqYSYF1/JH3T5Pj5hcHjYu6AiBYV9cclp1o
        GWS5QEvYLB3vGnaCcCbPLEX0w0vqC62MaQ==
        """)
    /// `openssl dgst -sha256` of deviceDER.
    static let devicePin = hex("e80e5774e97294907168b00f1461d60f26dc0f60b097bdc0af00b0e75507cf09")

    static let otherDER = b64("""
        MIIBozCCAUqgAwIBAgIUFogEEBa8Es1WGsNf1rZ7IH2YYtwwCgYIKoZIzj0EAwIwEjEQMA4GA1UE
        AwwHb3RjLWxhbjAgFw0yNjEwMDYwODQ0NTJaGA8yMTI2MDkxMjA4NDQ1MlowEjEQMA4GA1UEAwwH
        b3RjLWxhbjBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABNSJbsb8g8VhgBeJfkOdiucqdzAY3d2E
        YREs1hF3z+RGLt1XicMf7DQKuO8MCl3jQLklO75Ofks5xnHD6zPhUvGjfDB6MB0GA1UdDgQWBBRI
        lk7I3oohu/+1OepiN0aWspR7lzAfBgNVHSMEGDAWgBRIlk7I3oohu/+1OepiN0aWspR7lzAPBgNV
        HRMBAf8EBTADAQH/MBIGA1UdEQQLMAmCB290Yy1sYW4wEwYDVR0lBAwwCgYIKwYBBQUHAwEwCgYI
        KoZIzj0EAwIDRwAwRAIgPmJvdVFc6g3t4Xv6gr5+xFsLksw4VA8FRavJXOZXduUCICYjNf+lAWpg
        VJJ8O2fGYjI/Zz6fP0VHUtxVS7qSaMKS
        """)

    static let deviceP12 = b64("""
        MIIDsgIBAzCCA3AGCSqGSIb3DQEHAaCCA2EEggNdMIIDWTCCAk8GCSqGSIb3DQEHBqCCAkAwggI8
        AgEAMIICNQYJKoZIhvcNAQcBMBwGCiqGSIb3DQEMAQMwDgQI3AIU3MKAm3oCAggAgIICCN1T5PTl
        Xq3BZT4av/tZmUyWD94ddTrtKmjHn21CqbtMNXodV566q1vtzQZ3PAGHkv6BWKepMoTTtFjuweU/
        5Z3AaeriBjImeY9LLWsSqVqmIGyYq+URoSSRXbxNrGn56E+KC38FSMa24tadZm9gvFh25NcmlXIl
        XogAKwds382x4S1jwGBB6KqPS/Q04IKFPZ2Ju8srIZYdKCkN2oLd4uVWOhnmVCGfmcwOK8qCCXml
        Z6lo2pK24iCEreeSc3g5IXvzUJJYEyJ7x+kUne0/fjm2iX/WckwTqmWUYbqVFg4eWmFrI4/p1WDZ
        OyKvY/aQUytqKArcXrgNOuDm+kia7dOa7bwMHkT0ZWFPKeuiGCWym6aGxtLPGMVB5dKFne4PoVTf
        z4vwfF/57/V4ZTBpZgGHvpQPf8O17aMERPUEuzdE+zeZSTQZw0DU1I149BRxmarUiuehvp2Og45e
        7oKI/QNBRu4YXwmjL5sctXSnYkK/ffRKnvxXZT/H0FcUT5KtbrxBZspqPuGZXqTvn7X1CvFeC5Gh
        XpoKVkep7Ria/b28fxP9msRbPItNqlRIFLUEp8K/H6vl0aWY/w+RrtO/Sizwq3BqC94YpgkSJ3AS
        TX6+idchQeZfmess1scpNF8AUQRD6bk61QZ7oRsTCpnAPTbAhPRc/fLlWyVBihz2uSJ6XQ6q69mU
        8aYwggECBgkqhkiG9w0BBwGggfQEgfEwge4wgesGCyqGSIb3DQEMCgECoIG0MIGxMBwGCiqGSIb3
        DQEMAQMwDgQIxGQ7ZSKUPTUCAggABIGQMLdYEV106GnXfi8BsE03yGovY9aP9rpOMV9Bgnn7wkwv
        O0Dg064fw47HGB1e1CAvkJftuqRsb+9yJTAplBlUrKHMHVLVsOq89thXl+52B5ty4Qvk/DgYtnnW
        bVoTEonjOufkEt5n26RUTfxYo0UJnW+TFDrdPiDsfDkcmEUYUI5keSfbRt3nApkcvnwvq3XKMSUw
        IwYJKoZIhvcNAQkVMRYEFITA1Cfwoc0yO2QkaDVk2Ay2ZKpRMDkwITAJBgUrDgMCGgUABBSRrSmG
        N+l9i63GDCyIq/CsjU4vvgQQU5qZL6DATHOWTDXVpgmasAICCAA=
        """)

    static func b64(_ s: String) -> Data { Data(base64Encoded: s, options: .ignoreUnknownCharacters)! }

    static func hex(_ s: String) -> Data {
        var out = Data()
        var i = s.startIndex
        while i < s.endIndex {
            let j = s.index(i, offsetBy: 2)
            out.append(UInt8(s[i..<j], radix: 16)!)
            i = j
        }
        return out
    }

    static func trust(_ der: Data) -> SecTrust {
        let cert = SecCertificateCreateWithData(nil, der as CFData)!
        var trust: SecTrust?
        SecTrustCreateWithCertificates(cert, SecPolicyCreateSSL(true, nil), &trust)
        return trust!
    }

    static func deviceIdentity() throws -> SecIdentity {
        var items: CFArray?
        let options: [String: Any] = [kSecImportExportPassphrase as String: "test",
                                      kSecImportToMemoryOnly as String: true]
        let status = SecPKCS12Import(deviceP12 as CFData, options as CFDictionary, &items)
        guard status == errSecSuccess, let first = (items as? [[String: Any]])?.first,
              let identity = first[kSecImportItemIdentity as String] else {
            throw NSError(domain: "test", code: Int(status))
        }
        return identity as! SecIdentity
    }
}

struct LocalPinTests {
    @Test func thePinIsTheSHA256OfTheCertificateDER() {
        #expect(LocalPin.sha256(TestCerts.deviceDER) == TestCerts.devicePin)
    }

    @Test func onlyThePinnedCertificateIsAccepted() {
        let device = TestCerts.trust(TestCerts.deviceDER)
        #expect(LocalPin.matches(device, pin: TestCerts.devicePin))
        #expect(!LocalPin.matches(device, pin: LocalPin.sha256(TestCerts.otherDER)))
        #expect(!LocalPin.matches(TestCerts.trust(TestCerts.otherDER), pin: TestCerts.devicePin))
        // Nothing stored, or a damaged pin, never matches.
        #expect(!LocalPin.matches(device, pin: Data()))
        #expect(!LocalPin.matches(device, pin: TestCerts.devicePin.prefix(31)))
    }
}

struct LocalEndpointTests {
    let pin = TestCerts.devicePin

    @Test func urlsForEachAddress() throws {
        let ep = try #require(LocalEndpoint(domain: "cala.off-the.cloud",
                                            addresses: ["192.168.1.20", "fd12:3456::20"], port: 8443, pin: pin))
        #expect(ep.urls.map(\.absoluteString) == ["wss://192.168.1.20:8443/ws", "wss://[fd12:3456::20]:8443/ws"])
    }

    @Test func onlyIPLiteralsAreKept() throws {
        let ep = try #require(LocalEndpoint(domain: "d", addresses: [
            "evil.example", "192.168.1.20/x", "192.168.1.20@evil", "fe80::1%en0", "", "10.0.0.5",
        ], port: 8443, pin: pin))
        #expect(ep.addresses == ["10.0.0.5"])
        #expect(LocalEndpoint(domain: "d", addresses: ["evil.example"], port: 8443, pin: pin) == nil)
        let many = (1...20).map { "10.0.0.\($0)" }
        #expect(LocalEndpoint(domain: "d", addresses: many, port: 8443, pin: pin)?.addresses == Array(many.prefix(8)))
    }

    @Test func unusableAnswersAreRefused() {
        #expect(LocalEndpoint(domain: "d", addresses: [], port: 8443, pin: pin) == nil)
        #expect(LocalEndpoint(domain: "d", addresses: ["10.0.0.5"], port: 0, pin: pin) == nil)
        #expect(LocalEndpoint(domain: "d", addresses: ["10.0.0.5"], port: 70000, pin: pin) == nil)
        #expect(LocalEndpoint(domain: "d", addresses: ["10.0.0.5"], port: 8443, pin: pin.prefix(31)) == nil)
        #expect(LocalEndpoint(domain: "", addresses: ["10.0.0.5"], port: 8443, pin: pin) == nil)
    }

    @Test func storedFormRoundTripsAndIsCheckedOnTheWayBack() throws {
        let ep = try #require(LocalEndpoint(domain: "d", addresses: ["10.0.0.5"], port: 8443, pin: pin))
        #expect(LocalEndpoint.decode(try #require(ep.encoded)) == ep)
        let tampered = #"{"domain":"d","addresses":["evil.example"],"port":8443,"pin":"\#(pin.base64EncodedString())"}"#
        #expect(LocalEndpoint.decode(tampered) == nil)
    }

    @Test func sameRouteIgnoresAddressOrder() throws {
        let a = try #require(LocalEndpoint(domain: "d", addresses: ["10.0.0.5", "fd00::5"], port: 8443, pin: pin))
        let b = try #require(LocalEndpoint(domain: "d", addresses: ["fd00::5", "10.0.0.5"], port: 8443, pin: pin))
        let c = try #require(LocalEndpoint(domain: "d", addresses: ["10.0.0.6"], port: 8443, pin: pin))
        #expect(a.sameRoute(as: b))
        #expect(!a.sameRoute(as: c))
    }
}

struct LocalAnswerTests {
    @Test func anEndpointIsStored() throws {
        var resp = Resp()
        var ep = Msg_LocalEndpoint()
        ep.addresses = ["192.168.1.20"]
        ep.port = 8443
        ep.certSha256 = TestCerts.devicePin
        resp.payload = .respLocalEndpoint(ep)
        let want = try #require(LocalEndpoint(domain: "d", addresses: ["192.168.1.20"], port: 8443, pin: TestCerts.devicePin))
        #expect(WSClient.localAnswer(resp, domain: "d") == .store(want))
        // Answered, but with nothing usable.
        ep.certSha256 = Data()
        resp.payload = .respLocalEndpoint(ep)
        #expect(WSClient.localAnswer(resp, domain: "d") == .clear)
    }

    @Test func anOlderOrUnreachableDeviceClearsIt() {
        for code in ["unknown_payload", "local_unavailable"] {
            var resp = Resp()
            resp.error = true
            resp.errorCode = code
            #expect(WSClient.localAnswer(resp, domain: "d") == .clear)
        }
    }

    @Test func anythingElseKeepsIt() {
        var resp = Resp()
        resp.error = true
        resp.errorCode = "internal"
        #expect(WSClient.localAnswer(resp, domain: "d") == .keep)
        var ack = Resp()
        var a = Ack()
        a.code = "not_authenticated"
        ack.payload = .respAck(a)
        #expect(WSClient.localAnswer(ack, domain: "d") == .keep)
    }
}

struct ConnectionRouteTests {
    @Test func labels() {
        #expect(ConnectionRoute.label(.home, domain: "cala.off-the.cloud") == "Connected over your home network")
        #expect(ConnectionRoute.label(.bridge, domain: "cala.off-the.cloud") == "Connected through off-the.cloud")
        #expect(ConnectionRoute.label(.bridge, domain: "wss://cala.off-the.cloud/ws") == "Connected through off-the.cloud")
        #expect(ConnectionRoute.label(.bridge, domain: "ws://otc.local:8080/ws") == "Connected through otc.local")
        #expect(ConnectionRoute.label(.bridge, domain: "pit.otc") == "Connected through pit.otc")
        #expect(ConnectionRoute.label(.bridge, domain: "pit.otc:8080") == "Connected through pit.otc")
        #expect(ConnectionRoute.label(.bridge, domain: "") == "Connected through the configured address")
    }
}

/// A WebSocket server on 127.0.0.1 with the device's test certificate.
/// The app's own sandbox allows no listening, so these run only outside it
/// (`swift test` on a package with these files, see CLAUDE.md).
private final class PinnedServer {
    let listener: NWListener
    private let queue = DispatchQueue(label: "test.server")
    private var opened = 0

    init(tls: Bool = true) throws {
        let params: NWParameters
        if tls {
            let options = NWProtocolTLS.Options()
            sec_protocol_options_set_local_identity(options.securityProtocolOptions,
                                                    sec_identity_create(try TestCerts.deviceIdentity())!)
            params = WSClient.parameters(tls: options)
        } else {
            // Takes the connection and never answers.
            params = NWParameters.tcp
        }
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: params)
        listener.newConnectionHandler = { [weak self] conn in
            guard let self else { return }
            conn.stateUpdateHandler = { state in
                if case .ready = state, tls { self.opened += 1 }
            }
            conn.start(queue: self.queue)
        }
    }

    /// WebSocket handshakes the server completed.
    var handshakes: Int { queue.sync { opened } }

    /// handshakes, once it reaches `n` or a second has passed: the
    /// server's side of a handshake may be told a moment after the client's.
    func handshakes(reaching n: Int) async -> Int {
        for _ in 0..<20 where handshakes < n { try? await Task.sleep(for: .milliseconds(50)) }
        return handshakes
    }

    func start() async throws -> UInt16 {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<UInt16, Error>) in
            // Cleared on the first answer: the continuation resumes once.
            listener.stateUpdateHandler = { [listener] state in
                switch state {
                case .ready:
                    listener.stateUpdateHandler = nil
                    cont.resume(returning: listener.port!.rawValue)
                case .failed(let e):
                    listener.stateUpdateHandler = nil
                    cont.resume(throwing: e)
                default: break
                }
            }
            listener.start(queue: queue)
        }
    }

    deinit { listener.cancel() }
}

/// The race's winner, as the URL it dialled.
private func race(_ urls: [URL], pin: Data, budget: TimeInterval = LocalRace.budget) async -> URL? {
    let queue = DispatchQueue(label: "test.race")
    return await withCheckedContinuation { (cont: CheckedContinuation<URL?, Never>) in
        queue.async {
            LocalRace(queue: queue) { winner in
                var won: URL?
                if case .url(let u) = winner?.endpoint { won = u }
                winner?.cancel()
                cont.resume(returning: won)
            }.start(urls: urls, pin: pin, budget: budget)
        }
    }
}

private let outsideSandbox = ProcessInfo.processInfo.environment["APP_SANDBOX_CONTAINER_ID"] == nil

@Suite(.enabled(if: outsideSandbox, "needs to listen on 127.0.0.1"))
struct LocalRaceTests {
    @Test func thePinnedDeviceWins() async throws {
        let server = try PinnedServer()
        let port = try await server.start()
        let url = URL(string: "wss://127.0.0.1:\(port)/ws")!
        #expect(await race([url], pin: TestCerts.devicePin) == url)
        #expect(await server.handshakes(reaching: 1) == 1)
    }

    @Test func anotherCertificateIsRefusedBeforeAnythingIsSent() async throws {
        let server = try PinnedServer()
        let port = try await server.start()
        let url = URL(string: "wss://127.0.0.1:\(port)/ws")!
        #expect(await race([url], pin: LocalPin.sha256(TestCerts.otherDER)) == nil)
        // The WebSocket upgrade never went out: the server finished no
        // handshake.
        #expect(server.handshakes == 0)
    }

    @Test func theAddressThatAnswersWins() async throws {
        let server = try PinnedServer()
        let port = try await server.start()
        let dead = URL(string: "wss://127.0.0.1:1/ws")!
        let good = URL(string: "wss://127.0.0.1:\(port)/ws")!
        #expect(await race([dead, good], pin: TestCerts.devicePin) == good)
    }

    @Test func nothingWithinTheBudgetFallsBack() async throws {
        let silent = try PinnedServer(tls: false)
        let port = try await silent.start()
        let started = Date()
        #expect(await race([URL(string: "wss://127.0.0.1:\(port)/ws")!], pin: TestCerts.devicePin, budget: 0.5) == nil)
        #expect(Date().timeIntervalSince(started) < 2)
    }
}
