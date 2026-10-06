// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import CryptoKit
import Network
import Security

// Issue #190: at home every byte to the device used to go out to the
// bridge and back in, and a big upload starved the home uplink. After
// signing in through the bridge the app asks the device where it is on the
// home network (GetLocalEndpoint): its private addresses, the port of its
// TLS listener and the SHA-256 of its self-signed certificate. Every
// connection then tries those addresses first, accepting that certificate
// only, and falls back to the bridge. The pin comes through the signed-in
// bridge session, so the bridge is trusted exactly as before. Same policy
// as otc-sync and the phone apps.

/// How the app reaches the device.
enum ConnectionRoute: Equatable {
    /// Straight to the device on the home network, pinned TLS.
    case home
    /// The configured address: the bridge, or whatever was typed.
    case bridge

    /// Settings' status line once connected; worded as in the other apps.
    static func label(_ route: ConnectionRoute, domain: String) -> String {
        switch route {
        case .home:
            return "Connected over your home network"
        case .bridge:
            // A bare address is a host, maybe with a port, as configure() reads it.
            let trimmed = domain.trimmingCharacters(in: .whitespaces)
            let host = URL(string: trimmed.contains("://") ? trimmed : "wss://" + trimmed)?.host() ?? ""
            // SettingsStore.bridgeDomain, which is main-actor bound.
            let bridge = "off-the.cloud"
            if host.lowercased() == bridge || host.lowercased().hasSuffix("." + bridge) {
                return "Connected through \(bridge)"
            }
            return "Connected through \(host.isEmpty ? "the configured address" : host)"
        }
    }
}

/// Where the device said it can be reached at home.
struct LocalEndpoint: Codable, Equatable {
    /// The configured address it was learnt through: it is only ever used
    /// to reach that same device.
    let domain: String
    /// Bare IP literals, IPv4 first, as the device sends them.
    let addresses: [String]
    let port: Int
    /// SHA-256 of the DER bytes of the device's certificate.
    let pin: Data

    /// nil unless it is usable. Only IP literals are kept, so nothing the
    /// device (or the Keychain) holds can turn into another host or path in
    /// the URL.
    init?(domain: String, addresses: [String], port: Int, pin: Data) {
        // A handful at most: each is a connection attempt at once.
        let ips = Array(addresses.filter(Self.isHomeAddress).prefix(Self.maxAddresses))
        guard !domain.isEmpty, !ips.isEmpty, (1...65535).contains(port), pin.count == 32 else { return nil }
        self.domain = domain
        self.addresses = ips
        self.port = port
        self.pin = pin
    }

    static let maxAddresses = 8

    /// A home-network IP literal only (RFC 1918, IPv6 ULA), as the device
    /// lists them and as iOS, Android and otc-sync check: a name would be
    /// looked up, and a public address is no home network.
    static func isHomeAddress(_ s: String) -> Bool {
        guard !s.isEmpty, s.allSatisfy({ $0.isHexDigit || $0 == "." || $0 == ":" }) else { return false }
        if let v4 = IPv4Address(s) {
            let b = [UInt8](v4.rawValue)
            return b[0] == 10 || (b[0] == 172 && b[1] & 0xf0 == 16) || (b[0] == 192 && b[1] == 168)
        }
        if let v6 = IPv6Address(s) {
            return v6.rawValue[v6.rawValue.startIndex] & 0xfe == 0xfc
        }
        return false
    }

    /// The same addresses (in any order), port and pin: nothing new to try.
    func sameRoute(as other: LocalEndpoint) -> Bool {
        domain == other.domain && Set(addresses) == Set(other.addresses) && port == other.port && pin == other.pin
    }

    /// wss://<address>:<port>/ws for each address, an IPv6 one in brackets.
    var urls: [URL] {
        addresses.compactMap { a in
            URL(string: "wss://\(a.contains(":") ? "[\(a)]" : a):\(port)/ws")
        }
    }

    var encoded: String? {
        (try? JSONEncoder().encode(self)).flatMap { String(data: $0, encoding: .utf8) }
    }

    /// Checked again on the way back in: Codable alone would take any
    /// address, port or pin length.
    static func decode(_ s: String) -> LocalEndpoint? {
        guard let raw = try? JSONDecoder().decode(LocalEndpoint.self, from: Data(s.utf8)) else { return nil }
        return LocalEndpoint(domain: raw.domain, addresses: raw.addresses, port: raw.port, pin: raw.pin)
    }
}

/// The endpoint lives in the Keychain next to the password: whoever could
/// change the pin could have the password sent to them.
enum LocalEndpointStore {
    private static let cKey = "localEndpoint"

    /// The stored endpoint if it belongs to `domain`.
    static func load(domain: String) -> LocalEndpoint? {
        guard let s = Keychain.loadString(key: cKey), let ep = LocalEndpoint.decode(s),
              ep.domain == domain else { return nil }
        return ep
    }

    static func save(_ ep: LocalEndpoint) {
        guard let s = ep.encoded else { return }
        Keychain.saveString(key: cKey, value: s)
    }

    static func clear() {
        Keychain.delete(key: cKey)
    }
}

enum LocalPin {
    static func sha256(_ der: Data) -> Data { Data(SHA256.hash(data: der)) }

    /// Whether the certificate the server presented is the pinned one. Its
    /// name, issuer and dates don't matter: the device's certificate is
    /// self-signed and reached by IP. The TLS handshake has already
    /// proved the server holds that certificate's key.
    static func matches(_ trust: SecTrust, pin: Data) -> Bool {
        guard pin.count == 32,
              let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let leaf = chain.first else { return false }
        return sha256(SecCertificateCopyData(leaf) as Data) == pin
    }

    /// TLS 1.2 or later that accepts only the pinned certificate. The
    /// WebSocket upgrade, the first thing sent, goes out only after this
    /// check passes.
    static func tlsOptions(pin: Data, queue: DispatchQueue) -> NWProtocolTLS.Options {
        let tls = NWProtocolTLS.Options()
        let sec = tls.securityProtocolOptions
        sec_protocol_options_set_min_tls_protocol_version(sec, .TLSv12)
        // The device's listener speaks HTTP/1.1 only, and says so in ALPN.
        sec_protocol_options_add_tls_application_protocol(sec, "http/1.1")
        sec_protocol_options_set_verify_block(sec, { _, trust, complete in
            complete(matches(sec_trust_copy_ref(trust).takeRetainedValue(), pin: pin))
        }, queue)
        return tls
    }
}

/// Tries every home address at once. The first whose WebSocket handshake
/// completes wins and the others are cancelled; nil when none does within
/// the budget. Everything runs on `queue`, the WSClient's, and the
/// completion is called once, unless cancel() comes first.
final class LocalRace {
    /// About what the other apps allow: long enough for a LAN, short enough
    /// that away from home the bridge isn't noticeably later.
    static let budget: TimeInterval = 2.5

    private let queue: DispatchQueue
    private let completion: (NWConnection?) -> Void
    private var conns: [NWConnection] = []
    private var left = 0
    private var done = false

    init(queue: DispatchQueue, completion: @escaping (NWConnection?) -> Void) {
        self.queue = queue
        self.completion = completion
    }

    func start(urls: [URL], pin: Data, budget: TimeInterval = LocalRace.budget) {
        guard !urls.isEmpty else { finish(nil); return }
        left = urls.count
        for url in urls {
            let conn = NWConnection(to: .url(url), using: WSClient.parameters(tls: LocalPin.tlsOptions(pin: pin, queue: queue)))
            conns.append(conn)
            conn.stateUpdateHandler = { [weak self] state in
                guard let self, !self.done else { return }
                switch state {
                case .ready:
                    self.finish(conn)
                case .waiting(let error), .failed(let error):
                    // Refused, unreachable, or the pin didn't match: this
                    // address is out. Waiting would last until the path
                    // changes.
                    print("WSClient: home network \(url.host() ?? ""): \(error)")
                    conn.stateUpdateHandler = nil
                    conn.cancel()
                    self.left -= 1
                    if self.left == 0 { self.finish(nil) }
                default:
                    break
                }
            }
            conn.start(queue: queue)
        }
        // Strong: the race lives until it is decided, whoever holds it.
        queue.asyncAfter(deadline: .now() + budget) { self.finish(nil) }
    }

    /// Stops the race without calling the completion.
    func cancel() {
        guard !done else { return }
        done = true
        drop(except: nil)
    }

    private func finish(_ winner: NWConnection?) {
        guard !done else { return }
        done = true
        drop(except: winner)
        completion(winner)
    }

    private func drop(except winner: NWConnection?) {
        for c in conns where c !== winner {
            c.stateUpdateHandler = nil
            c.cancel()
        }
        conns = []
    }
}
