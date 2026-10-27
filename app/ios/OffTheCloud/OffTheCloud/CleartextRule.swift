// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Network

/// Where the app talks to a device without encryption - ws://, and the
/// http:// media URLs that go with it - as Android's net/CleartextRule.kt:
/// only on the home network. A ws:// address anywhere else (a public name,
/// a dynamic DNS address) would carry the sign-in over the internet in the
/// clear, the connection's public key included, so whoever sat in between
/// could swap it and read the password. OTCConnection refuses such an
/// address before dialling, with app.shell.cleartext_refused; App Transport
/// Security (NSAllowsLocalNetworking) refuses some of what this lets
/// through (a *.ts.net name), and that refusal reads the same.
enum CleartextRule {
    /// Whether `url` may be used: anything encrypted, and ws:// or http://
    /// only to a home-network host.
    static func allows(_ url: URL) -> Bool {
        guard let scheme = url.scheme?.lowercased(), scheme == "ws" || scheme == "http" else { return true }
        return allowsCleartext(host: url.host ?? "")
    }

    /// A home-network host: a .local name, a single word, a private,
    /// link-local or loopback address, or Tailscale's (100.64.0.0/10, a
    /// *.ts.net name, its fd7a:115c:a1e0::/48 inside fc00::/7).
    static func allowsCleartext(host raw: String) -> Bool {
        var h = raw.lowercased()
        if h.hasPrefix("[") && h.hasSuffix("]") { h = String(h.dropFirst().dropLast()) }
        // A zone (fe80::1%en0) says which interface, not which address.
        if let zone = h.firstIndex(of: "%") { h = String(h[..<zone]) }
        while h.hasSuffix(".") { h.removeLast() }
        guard !h.isEmpty else { return false }
        if h.contains(":") {
            guard let v6 = IPv6Address(h) else { return false }
            let b = [UInt8](v6.rawValue)
            let loopback = b.dropLast().allSatisfy { $0 == 0 } && b.last == 1
            let linkLocal = b[0] == 0xfe && b[1] & 0xc0 == 0x80
            let unique = b[0] & 0xfe == 0xfc
            return loopback || linkLocal || unique
        }
        if let b = ipv4(h) {
            return b[0] == 10 || (b[0] == 172 && b[1] & 0xf0 == 16) || (b[0] == 192 && b[1] == 168)
                || b[0] == 127 || (b[0] == 169 && b[1] == 254) || (b[0] == 100 && b[1] & 0xc0 == 64)
        }
        // Digits and dots that aren't four parts: no name, no address.
        if h.allSatisfy({ $0.isASCII && ($0.isNumber || $0 == ".") }) { return false }
        return !h.contains(".") || h.hasSuffix(".local") || h.hasSuffix(".ts.net")
    }

    /// A dotted IPv4 literal's four bytes: exactly four parts of one to three
    /// digits, each at most 255, as Android reads them.
    private static func ipv4(_ h: String) -> [Int]? {
        let parts = h.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 4 else { return nil }
        var out: [Int] = []
        for p in parts {
            guard (1...3).contains(p.count), p.allSatisfy({ $0.isASCII && $0.isNumber }), let v = Int(p), v <= 255 else { return nil }
            out.append(v)
        }
        return out
    }
}
