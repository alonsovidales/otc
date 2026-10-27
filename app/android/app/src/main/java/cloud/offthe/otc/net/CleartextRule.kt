// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import java.net.URI

/**
 * Where the app talks to a device without encryption - ws://, and the
 * http:// media URLs that go with it - as iOS's CleartextRule.swift: only
 * on the home network (A6). A ws:// address anywhere else (a public name, a
 * dynamic DNS address) would carry the sign-in over the internet in the
 * clear, the connection's public key included, so whoever sat in between
 * could swap it and read the password. OTCConnection refuses such an
 * address before dialling, with app.shell.cleartext_refused, and WSClient
 * and MediaStream check again. The network security config can't name an
 * address range, so this is the rule; that file keeps the bridge's own
 * names encrypted whatever happens.
 */
object CleartextRule {
    /** Whether [url] may be used: anything encrypted, and ws:// or http:// only to a home-network host. */
    fun allows(url: String): Boolean {
        val u = try { URI(url) } catch (e: Exception) { return false }
        val scheme = u.scheme?.lowercase() ?: return false
        if (scheme != "ws" && scheme != "http") return true
        return allowsCleartext(u.host ?: "")
    }

    /**
     * A home-network host: a .local name, a single word, a private,
     * link-local or loopback address, or Tailscale's (100.64.0.0/10, a
     * *.ts.net name, its fd7a:115c:a1e0::/48 inside fc00::/7). The private
     * IPv4 and IPv6 ranges are HomeEndpoint.isHomeAddress's.
     */
    fun allowsCleartext(raw: String): Boolean {
        var h = raw.lowercase()
        if (h.startsWith("[") && h.endsWith("]")) h = h.substring(1, h.length - 1)
        // A zone (fe80::1%en0) says which interface, not which address.
        h = h.substringBefore('%')
        h = h.trimEnd('.')
        if (h.isEmpty()) return false
        if (h.contains(':')) return ipv6Local(h)
        ipv4(h)?.let { b ->
            return HomeEndpoint.isHomeAddress(h) || b[0] == 127 || (b[0] == 169 && b[1] == 254) || (b[0] == 100 && b[1] and 0xc0 == 64)
        }
        // Digits and dots that aren't four parts: no name, no address.
        if (h.all { it in '0'..'9' || it == '.' }) return false
        return !h.contains('.') || h.endsWith(".local") || h.endsWith(".ts.net")
    }

    /** A dotted IPv4 literal's four bytes: exactly four parts of one to three digits, each at most 255. */
    private fun ipv4(h: String): IntArray? {
        val parts = h.split('.')
        if (parts.size != 4) return null
        return IntArray(4) { i ->
            val p = parts[i]
            if (p.length !in 1..3 || !p.all { it in '0'..'9' }) return null
            p.toInt().takeIf { it <= 255 } ?: return null
        }
    }

    /** fc00::/7 (HomeEndpoint.isHomeAddress), ::1 and fe80::/10, read from the literal itself, never looked up. */
    private fun ipv6Local(h: String): Boolean {
        if (HomeEndpoint.isHomeAddress(h)) return true
        val bytes = ipv6Bytes(h) ?: return false
        val loopback = (0 until 15).all { bytes[it] == 0 } && bytes[15] == 1
        val linkLocal = bytes[0] == 0xfe && bytes[1] and 0xc0 == 0x80
        return loopback || linkLocal
    }

    /** An IPv6 literal's 16 bytes (no embedded IPv4 form: none of those is a home address here), or null. */
    private fun ipv6Bytes(h: String): IntArray? {
        if (h.count { it == ':' } < 2 || !h.all { it in '0'..'9' || it in 'a'..'f' || it == ':' }) return null
        val halves = h.split("::")
        if (halves.size > 2) return null
        fun groups(s: String): List<Int>? = if (s.isEmpty()) emptyList() else s.split(':').map { g ->
            if (g.length !in 1..4) return null
            g.toInt(16)
        }
        val head = groups(halves[0]) ?: return null
        val tail = if (halves.size == 2) groups(halves[1]) ?: return null else emptyList()
        val missing = 8 - head.size - tail.size
        if (halves.size == 1 && missing != 0) return null
        if (halves.size == 2 && missing < 1) return null
        val all = head + List(missing) { 0 } + tail
        return IntArray(16) { i -> (all[i / 2] shr (if (i % 2 == 0) 8 else 0)) and 0xff }
    }
}
