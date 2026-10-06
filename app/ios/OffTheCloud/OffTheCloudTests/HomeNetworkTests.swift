// SPDX-License-Identifier: AGPL-3.0-or-later

import CryptoKit
import Foundation
import Network
import Security
import Testing
@testable import OffTheCloud

/// Issue #190: the home-network route - the pin check, what the device's
/// answers keep or forget, when a network change switches, and real
/// pinned TLS connections to servers on 127.0.0.1 holding the device's
/// certificate or an impostor's.
@Suite(.serialized)
struct HomeNetworkTests {
    // Self-signed ECDSA P-256 certificates like the device's (CN and SAN
    // otc-lan, serverAuth, 100 years), made with openssl: "good" is the
    // device, "bad" an impostor. The PKCS#12 passphrase is "otc".
    static let goodDER = Data(base64Encoded: """
MIIBozCCAUqgAwIBAgIUPtyYZQMt2KGRzdHc//9NmzyPOE8wCgYIKoZIzj0EAwIwEjEQMA4GA1UEAwwHb3RjLWxhbjAgFw0yNjEw
MDYwODQ2NTVaGA8yMTI2MDkxMjA4NDY1NVowEjEQMA4GA1UEAwwHb3RjLWxhbjBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABIK4
eaaeW+rvHOyHsbUtOwGkVL2i7UuYE27snqWql53p/0TdwBOKl//WpdowyqJG1MecEzpybzpkH+MZOcEuTNajfDB6MB0GA1UdDgQW
BBQap/b9M1/OjATPg2hejd8FTNGuNjAfBgNVHSMEGDAWgBQap/b9M1/OjATPg2hejd8FTNGuNjAPBgNVHRMBAf8EBTADAQH/MBIG
A1UdEQQLMAmCB290Yy1sYW4wEwYDVR0lBAwwCgYIKwYBBQUHAwEwCgYIKoZIzj0EAwIDRwAwRAIgK6hrA+ApLEcAkH/bMW8720wc
81NA+5V5jQtnz38a3gMCIHwHs23thdSUmwiBNQhW5grCBrvYq0orNyYvn7Elrm2l
""", options: .ignoreUnknownCharacters)!
    static let goodP12 = Data(base64Encoded: """
MIIDqgIBAzCCA2gGCSqGSIb3DQEHAaCCA1kEggNVMIIDUTCCAkcGCSqGSIb3DQEHBqCCAjgwggI0AgEAMIICLQYJKoZIhvcNAQcB
MBwGCiqGSIb3DQEMAQMwDgQImFdmg1SNPdUCAggAgIICAAJiiMd+ljm9PuVEYIcGTO/s36GzP/62h0HNJKgNuXtlMa3FEt+MMD14
oH505y+WSAN/uFjS1/QyjCW5gwPsY4Fvd2flvQVFKUdzDyWwIIk/bu6iw+na3kuqV5qTdtrwiF7CshQm1icBHLqlxq0HY19I0Jti
OptI0vtjQYlFQEu3FC+KZZ6gyDbzZSUpuv3SOsXjKCoEGWnelJR2floF1qh8j7keouRRHs+EaVj3d8xPI0nNIF+gHIF9biJAIaui
1PaSlrfUoloJ4OlREgZMdxW/gVtwf+dHBYm6fRJcOrVE5z2f9ocxwmA08h0rG2f79Im0hEWDRGjH6bXl5RLJdaTchUIFVqUa72Oz
yp3PR7yyaNyQ+lpj0JGcPwcvHm9z1H7Ubw3WIQA3XxCgcXQMqdrI74vUGeIeoq5IbETqcuTvQi2fN7qvO+QG28fnO2sRbLu6DVJv
JVjrn065Ke5ELtOIBbgPTuro9027wY0OTVUbiUkaff9xdJWhd0Dm68IYR50o/qKDwl8vGXdO96/ptFfhHqVM+9Bvyu49OMbRX6cS
3qkDeefHExqvEjBF04h2PC1hJ7M6QjCxr+gD9aiePUIeOHNV3uuS4t1+xgKUJZ2Bp1dZrlFFIwtpuq3S0lg+Efuxk5sw0MSxpqch
3diKtzU7uw/+FLU0NmO+411cvVQvMIIBAgYJKoZIhvcNAQcBoIH0BIHxMIHuMIHrBgsqhkiG9w0BDAoBAqCBtDCBsTAcBgoqhkiG
9w0BDAEDMA4ECF9l4qvJnrVlAgIIAASBkGl5RORiulsW18vaw5SH25VK3hNgtsPUEAWwG8mz2kOXUocSlr77fzGSmD67bVX/7Fz2
yCMu7yQmHXEOv2/S9iMYPY5OkcCf8lOghVF4gWQZw2SrXJUq5KTPKLWTG5gI9010WK4MS6XpSWdVfu/YqhPF3CBiA58lS/MIHhg4
+Hu3blLjwyzSZIvtU3nY7ERhlzElMCMGCSqGSIb3DQEJFTEWBBQqsILQuSDEGcThD5EYh8ldYGcm/DA5MCEwCQYFKw4DAhoFAAQU
id7L/aDwvxiXdmSqErRhBP4A77IEENQ/f3BWLyKeI+LuBm22qq0CAggA
""", options: .ignoreUnknownCharacters)!
    static let badP12 = Data(base64Encoded: """
MIIDsgIBAzCCA3AGCSqGSIb3DQEHAaCCA2EEggNdMIIDWTCCAk8GCSqGSIb3DQEHBqCCAkAwggI8AgEAMIICNQYJKoZIhvcNAQcB
MBwGCiqGSIb3DQEMAQMwDgQIGTJsey81chMCAggAgIICCHo8/n3rd89F21NTsONUH1tZ0CXSlmH9biVwenc1En0OlSqIWs7aPJRD
8Bt9Ukr7IwQiZq676mGKcdmX52bD0LDVkb7K0ZiA4EH3CC0m+g2GdNFiDD5lxO9MR33K6BZV2s6cg1Vfo0pJDKVbwhEJcRNdz159
3e91dSdb3UEEpqulkaRfkrKjtKaQ27KhHyD/jMRc+5pS97UbYSgsuToq/onDtUOivgoYknmkmFd3NiadvwJ1BeQs3cCSK0QzcCrw
l8fFY2HYs4wM4QqoyLHyxOVH7XTy83Jm9IMI1jekJ2RwxpTCtRWwzNkgJtqgJXneszU/gKhpPYMqKLZfiMBN5Sn96zpXqbvfHhwX
MGEdr1uYwvKUxJP9CgRG5tptB2/0SGpmQfgdB8h7WURNHpJOzYJbhFik/QUH6odotxXvX+fQ3DW7YxIJLVblNVfXsPZXhl1m7gA7
OcH70zXwGeIeg4MJk67XBJ5NYB3KhYFoNrn3EzKD3uJ3eEOxJ9ptwRGHqZJfjjGDP2ck5AQT+OvGESg6gkGQVlldpb2Mn3j/ZA3f
22Gh87TPfyZ5TUWhNZDO3rSIG86VmcuL6y9t3mzWdQuRm5lIpZjSmcpUV1Di4eCshzy7xUGBdjc03Nlx2OnuQs+YipRvzERQYBYK
awBXX9KqJv0ZVJje8Nqas2l9kD/cfwoKIdXRzsYwggECBgkqhkiG9w0BBwGggfQEgfEwge4wgesGCyqGSIb3DQEMCgECoIG0MIGx
MBwGCiqGSIb3DQEMAQMwDgQIKW2nd5BDvJoCAggABIGQbeckUXyMMHvMUh5Ol3vszBhmcTQRYQRZ4TMir//pElx2vkI4i9SyOdH5
lJmYgCJb7mtTr+yQM0aLY3gkTMuaOCYS2nHbCdeO5RJl+qW7/hetogva8qArvmoFDR0Vj1uTNdm2itrPAyO/GxWlXzaBDqUMAomZ
0+njpaVo0D+8jI/dxcSSLwQM1HLiBHxjUJ0SMSUwIwYJKoZIhvcNAQkVMRYEFDCtp8CuFs7QUYk8fJXoGjvlFwQMMDkwITAJBgUr
DgMCGgUABBR+1dGhR5jekE6FFuZrpwXE0PpSMgQQRxHu9Z2J2jjcmw37u5wv0wICCAA=
""", options: .ignoreUnknownCharacters)!

    static var goodPin: Data { Data(SHA256.hash(data: goodDER)) }

    // MARK: The pin

    @Test func thePinIsTheSHA256OfTheLeafDER() throws {
        // What the device computes (Go: sha256.Sum256(cert.Certificate[0])).
        let cert = try #require(SecCertificateCreateWithData(nil, Self.goodDER as CFData))
        let hex = CertificatePin.sha256(of: cert).map { String(format: "%02x", $0) }.joined()
        #expect(hex == "a17d2bb3e29cc69dc8f3516f62c32ba53b6f9e6278d5fd89b6308ab9bdcdbdd7")
    }

    @Test func theLeafWhosePinMatchesIsAccepted() throws {
        #expect(CertificatePin.leafMatches(try Self.trust(Self.goodDER), pin: Self.goodPin))
    }

    @Test func anyOtherPinIsRefused() throws {
        let trust = try Self.trust(Self.goodDER)
        var flipped = Self.goodPin
        flipped[flipped.startIndex] ^= 1
        #expect(!CertificatePin.leafMatches(trust, pin: flipped))
        #expect(!CertificatePin.leafMatches(trust, pin: Data()))
        #expect(!CertificatePin.leafMatches(trust, pin: Self.goodPin.prefix(16)))
    }

    @Test func anotherCertificateIsRefused() throws {
        let bad = try TestServer.certificateDER(Self.badP12)
        #expect(!CertificatePin.leafMatches(try Self.trust(bad), pin: Self.goodPin))
    }

    // MARK: What is kept

    static func answer(_ addresses: [String], port: Int32 = 8443, pin: Data = goodPin) -> Msg_LocalEndpoint {
        var m = Msg_LocalEndpoint()
        m.addresses = addresses
        m.port = port
        m.certSha256 = pin
        return m
    }

    @Test func onlyHomeAddressesAreKept() throws {
        let m = Self.answer(["192.168.1.20", "fd12:3456::20", "10.1.2.3", "172.20.0.4", "evil.example", "fe80::1%en0", "fe80::1",
                             "8.8.8.8", "172.32.0.1", "100.64.0.1", "127.0.0.1", "2001:db8::1", " 10.0.0.1", ""])
        let ep = try #require(LocalEndpoint(m, endpoint: "wss://cala.off-the.cloud/ws"))
        #expect(ep.addresses == ["192.168.1.20", "fd12:3456::20", "10.1.2.3", "172.20.0.4"])
        #expect(ep.port == 8443)
        #expect(ep.pin == Self.goodPin)
        #expect(ep.socketURL(for: "192.168.1.20")?.absoluteString == "wss://192.168.1.20:8443/ws")
        #expect(ep.socketURL(for: "fd12:3456::20")?.absoluteString == "wss://[fd12:3456::20]:8443/ws")
    }

    @Test func anAnswerWithNothingUsableIsNone() {
        #expect(LocalEndpoint(Self.answer([]), endpoint: "e") == nil)
        #expect(LocalEndpoint(Self.answer(["otc.local"]), endpoint: "e") == nil)
        #expect(LocalEndpoint(Self.answer(["10.0.0.2"], port: 0), endpoint: "e") == nil)
        #expect(LocalEndpoint(Self.answer(["10.0.0.2"], port: 70000), endpoint: "e") == nil)
        #expect(LocalEndpoint(Self.answer(["10.0.0.2"], pin: Data(count: 20)), endpoint: "e") == nil)
        #expect(LocalEndpoint(Self.answer((1...20).map { "10.0.0.\($0)" }), endpoint: "e")?.addresses.count == 8)
    }

    @Test func theDevicesAnswersDecide() throws {
        let endpoint = "wss://cala.off-the.cloud/ws"
        var ok = Msg_RespEnvelope()
        ok.respLocalEndpoint = Self.answer(["192.168.1.20"])
        let expected = try #require(LocalEndpoint(Self.answer(["192.168.1.20"]), endpoint: endpoint))
        #expect(LocalEndpoint.update(for: ok, endpoint: endpoint) == .store(expected))

        // A device from before this release, and one with no home route.
        for code in ["unknown_payload", "local_unavailable"] {
            var e = Msg_RespEnvelope()
            e.error = true
            e.errorCode = code
            #expect(LocalEndpoint.update(for: e, endpoint: endpoint) == .clear)
        }

        var useless = Msg_RespEnvelope()
        useless.respLocalEndpoint = Self.answer([])
        #expect(LocalEndpoint.update(for: useless, endpoint: endpoint) == .clear)

        // The bridge's answer for a device it can't reach says nothing
        // about the home network, nor does any other error.
        var bridge = Msg_RespEnvelope()
        var ack = Msg_Ack()
        ack.code = "device_unreachable"
        bridge.respAck = ack
        #expect(LocalEndpoint.update(for: bridge, endpoint: endpoint) == .keep)
        var other = Msg_RespEnvelope()
        other.error = true
        other.errorCode = "not_authenticated"
        #expect(LocalEndpoint.update(for: other, endpoint: endpoint) == .keep)
    }

    @Test func whatANetworkChangeDoes() {
        typealias C = HomeNetwork.Check
        func check(signedIn: Bool = true, onHome: Bool = false, busy: Bool = false, hasStored: Bool = true, onlyCellular: Bool = false) -> C {
            HomeNetwork.check(signedIn: signedIn, onHome: onHome, busy: busy, hasStored: hasStored, onlyCellular: onlyCellular)
        }
        #expect(check() == .tryHome)
        #expect(check(onHome: true) == .pingHome)
        // An upload, download or sync keeps its connection.
        #expect(check(busy: true) == .nothing)
        #expect(check(onHome: true, busy: true) == .nothing)
        // Not connected: the next connect tries home first anyway.
        #expect(check(signedIn: false) == .nothing)
        #expect(check(hasStored: false) == .nothing)
        #expect(check(onlyCellular: true) == .nothing)
    }

    /// An unsigned test host (CODE_SIGNING_ALLOWED=NO) has no Keychain.
    static var keychainWorks: Bool {
        Keychain.saveString(key: "home_network_test_probe", value: "1")
        defer { Keychain.delete(key: "home_network_test_probe") }
        return Keychain.loadString(key: "home_network_test_probe") == "1"
    }

    @Test(.enabled(if: keychainWorks)) func theStoredEndpointBelongsToItsConnection() throws {
        let cala = "wss://cala.off-the.cloud/ws"
        let saved = LocalEndpoint.stored(for: cala).map { _ in Keychain.loadString(key: "local_endpoint") } ?? nil
        defer {
            if let saved { Keychain.saveString(key: "local_endpoint", value: saved) } else { LocalEndpoint.clear() }
        }
        let ep = try #require(LocalEndpoint(Self.answer(["192.168.1.20"]), endpoint: cala))
        ep.save()
        #expect(LocalEndpoint.stored(for: cala) == ep)
        #expect(LocalEndpoint.stored(for: "wss://pit.off-the.cloud/ws") == nil)
        LocalEndpoint.clear(unlessFor: cala)
        #expect(LocalEndpoint.stored(for: cala) == ep)
        // Saving another device's connection forgets it.
        LocalEndpoint.clear(unlessFor: "ws://otc.local:8080/ws")
        #expect(LocalEndpoint.stored(for: cala) == nil)
    }

    @MainActor @Test func theRouteLine() {
        #expect(OTCConnection.Route.home.description == "Connected over your home network")
        #expect(OTCConnection.Route.remote(host: "cala.off-the.cloud").description == "Connected through off-the.cloud")
        #expect(OTCConnection.Route.remote(host: "otc.local").description == "Connected through otc.local")
    }

    // MARK: Over real TLS

    @Test func theFirstAddressToAnswerWinsAndCarriesRequests() async throws {
        let server = try await TestServer.start(Self.goodP12, mode: .webSocketEcho)
        defer { server.stop() }
        // TEST-NET-1 never answers; the device on loopback does.
        let ep = LocalEndpoint(endpoint: "e", addresses: ["192.0.2.1", "127.0.0.1"], port: server.port, pin: Self.goodPin)
        let won = try #require(await PinnedSession.forPin(Self.goodPin).race(ep))
        #expect(won.link.address == "127.0.0.1")

        let ws = WSClient()
        await ws.adopt(won.task)
        // The server echoes, so an envelope with only an id comes back as
        // the answer to itself.
        let resp = try await ws.request { _ in }
        #expect(resp.id == 1)
        #expect(await ws.ping(within: 2))
        await ws.close()
    }

    @Test func anImpostorIsRefusedBeforeAnythingIsSent() async throws {
        let server = try await TestServer.start(Self.badP12, mode: .raw)
        defer { server.stop() }
        let ep = LocalEndpoint(endpoint: "e", addresses: ["127.0.0.1"], port: server.port, pin: Self.goodPin)
        let started = Date()
        let won = await PinnedSession.forPin(Self.goodPin).race(ep)
        #expect(won == nil)
        // Refused during the handshake, not left to time out.
        #expect(Date().timeIntervalSince(started) < HomeNetwork.raceBudget)
        try await Task.sleep(for: .milliseconds(300))
        #expect(server.connections > 0)
        #expect(server.receivedBytes == 0)
    }

    @Test func nothingAnsweringEndsAtTheBudget() async {
        let ep = LocalEndpoint(endpoint: "e", addresses: ["192.0.2.1"], port: 8443, pin: Self.goodPin)
        let started = Date()
        let won = await PinnedSession.forPin(Self.goodPin).race(ep, budget: 0.5)
        #expect(won == nil)
        #expect(Date().timeIntervalSince(started) < 2)
    }

    static func trust(_ der: Data) throws -> SecTrust {
        let cert = try #require(SecCertificateCreateWithData(nil, der as CFData))
        var trust: SecTrust?
        let status = SecTrustCreateWithCertificates(cert, SecPolicyCreateBasicX509(), &trust)
        #expect(status == errSecSuccess)
        return try #require(trust)
    }
}

/// A TLS server on loopback with a certificate from a PKCS#12: a
/// WebSocket echo, a byte-range HTTP server, or a sink that counts what
/// arrives after the handshake.
final class TestServer: @unchecked Sendable {
    enum Mode {
        case webSocketEcho
        case http(Data, mime: String)
        case raw
    }
    struct Request {
        let path: String
        let range: String?
    }

    private let listener: NWListener
    private let mode: Mode
    private let queue = DispatchQueue(label: "HomeNetworkTests.server")
    private let lock = NSLock()
    private var _connections = 0
    private var _received = 0
    private var _requests: [Request] = []
    private(set) var port = 0

    var connections: Int { lock.withLock { _connections } }
    var receivedBytes: Int { lock.withLock { _received } }
    var requests: [Request] { lock.withLock { _requests } }

    static func identity(_ p12: Data) throws -> SecIdentity {
        var items: CFArray?
        let options = [kSecImportExportPassphrase as String: "otc", kSecImportToMemoryOnly as String: true] as CFDictionary
        let status = SecPKCS12Import(p12 as CFData, options, &items)
        #expect(status == errSecSuccess)
        let first = try #require((items as? [[String: Any]])?.first)
        let identity = try #require(first[kSecImportItemIdentity as String])
        return identity as! SecIdentity // swiftlint:disable:this force_cast
    }

    static func certificateDER(_ p12: Data) throws -> Data {
        var cert: SecCertificate?
        SecIdentityCopyCertificate(try identity(p12), &cert)
        return SecCertificateCopyData(try #require(cert)) as Data
    }

    private init(listener: NWListener, mode: Mode) {
        self.listener = listener
        self.mode = mode
    }

    static func start(_ p12: Data, mode: Mode) async throws -> TestServer {
        let tls = NWProtocolTLS.Options()
        sec_protocol_options_set_local_identity(tls.securityProtocolOptions, try #require(sec_identity_create(try identity(p12))))
        let params = NWParameters(tls: tls)
        if case .webSocketEcho = mode {
            let ws = NWProtocolWebSocket.Options()
            ws.autoReplyPing = true
            params.defaultProtocolStack.applicationProtocols.insert(ws, at: 0)
        }
        let server = TestServer(listener: try NWListener(using: params, on: .any), mode: mode)
        try await server.run()
        return server
    }

    private func run() async throws {
        listener.newConnectionHandler = { [weak self] conn in self?.accept(conn) }
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            // Only ever touched on `queue`.
            final class Once: @unchecked Sendable { var done = false }
            let once = Once()
            listener.stateUpdateHandler = { state in
                guard !once.done else { return }
                switch state {
                case .ready:
                    once.done = true
                    cont.resume()
                case .failed(let error):
                    once.done = true
                    cont.resume(throwing: error)
                default:
                    break
                }
            }
            listener.start(queue: queue)
        }
        port = Int(listener.port?.rawValue ?? 0)
    }

    func stop() {
        listener.cancel()
    }

    private func accept(_ conn: NWConnection) {
        lock.withLock { _connections += 1 }
        conn.start(queue: queue)
        switch mode {
        case .webSocketEcho: echo(conn)
        case .http(let body, let mime): serve(conn, body: body, mime: mime, buffer: Data())
        case .raw: sink(conn)
        }
    }

    private func echo(_ conn: NWConnection) {
        conn.receiveMessage { [weak self] content, _, _, error in
            guard let self, error == nil else { conn.cancel(); return }
            if let content, !content.isEmpty {
                let meta = NWProtocolWebSocket.Metadata(opcode: .binary)
                let ctx = NWConnection.ContentContext(identifier: "echo", metadata: [meta])
                conn.send(content: content, contentContext: ctx, isComplete: true, completion: .idempotent)
            }
            self.echo(conn)
        }
    }

    private func sink(_ conn: NWConnection) {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 65536) { [weak self] content, _, done, error in
            guard let self else { return }
            if let content { self.lock.withLock { self._received += content.count } }
            if done || error != nil { conn.cancel(); return }
            self.sink(conn)
        }
    }

    private func serve(_ conn: NWConnection, body: Data, mime: String, buffer: Data) {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 65536) { [weak self] content, _, done, error in
            guard let self else { return }
            var buffer = buffer + (content ?? Data())
            while let end = buffer.range(of: Data("\r\n\r\n".utf8)) {
                let head = String(decoding: buffer[buffer.startIndex..<end.lowerBound], as: UTF8.self)
                buffer = Data(buffer[end.upperBound...])
                conn.send(content: self.reply(to: head, body: body, mime: mime), completion: .idempotent)
            }
            if done || error != nil { conn.cancel(); return }
            self.serve(conn, body: body, mime: mime, buffer: buffer)
        }
    }

    /// 206 for the range asked for, as the device's http.ServeContent does.
    private func reply(to head: String, body: Data, mime: String) -> Data {
        let lines = head.components(separatedBy: "\r\n")
        let path = lines.first?.split(separator: " ").dropFirst().first.map(String.init) ?? ""
        let range = lines.dropFirst().first { $0.lowercased().hasPrefix("range:") }
            .map { $0.dropFirst("range:".count).trimmingCharacters(in: .whitespaces) }
        lock.withLock { _requests.append(Request(path: path, range: range)) }
        var start = 0
        var end = body.count - 1
        if let range, range.hasPrefix("bytes=") {
            let parts = range.dropFirst("bytes=".count).split(separator: "-", omittingEmptySubsequences: false)
            start = Int(parts[0]) ?? 0
            if parts.count > 1, let e = Int(parts[1]) { end = min(e, body.count - 1) }
        }
        let slice = body[start...end]
        let header = "HTTP/1.1 206 Partial Content\r\nContent-Type: \(mime)\r\nContent-Range: bytes \(start)-\(end)/\(body.count)\r\nContent-Length: \(slice.count)\r\nAccept-Ranges: bytes\r\n\r\n"
        return Data(header.utf8) + slice
    }
}
