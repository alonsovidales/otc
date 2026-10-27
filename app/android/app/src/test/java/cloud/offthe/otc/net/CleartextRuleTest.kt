// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** A6: ws:// and http:// only to a home-network host, as iOS's CleartextRuleTests. */
class CleartextRuleTest {
    @Test fun homeNetworkHostsMayGoUnencrypted() {
        for (host in listOf(
            "cala", "localhost", "cala.local", "CALA.LOCAL.", "nas.ts.net", "pi.tail1234.ts.net",
            "10.0.0.5", "172.16.0.1", "172.31.255.255", "192.168.1.20", "127.0.0.1", "169.254.10.20",
            "100.64.0.1", "100.127.255.254", "::1", "[::1]", "fe80::1", "fe80::1%en0", "fd7a:115c:a1e0::1", "fc00::5",
        )) assertTrue(host, CleartextRule.allowsCleartext(host))
    }

    @Test fun anythingElseMustBeEncrypted() {
        for (host in listOf(
            "", "cala.off-the.cloud", "example.com", "my.dyndns.org", "ts.net.evil.com", "cala.local.example.com", "8.8.8.8",
            "172.32.0.1", "192.169.1.1", "100.128.0.1", "100.63.255.255", "11.0.0.1", "2001:db8::1",
            "::ffff:192.168.1.1", "1.2.3", "1.2.3.4.5", "256.1.1.1", "192.168.001.1x", "10",
        )) assertFalse(host, CleartextRule.allowsCleartext(host))
    }

    @Test fun encryptedAddressesAlwaysGo() {
        assertTrue(CleartextRule.allows("wss://cala.off-the.cloud/ws"))
        assertTrue(CleartextRule.allows("https://example.com/media/x"))
        assertTrue(CleartextRule.allows("ws://192.168.1.20:8080/ws"))
        assertTrue(CleartextRule.allows("WS://otc.local:8080/ws"))
        // The address a device that left the bridge answers at (LOCAL_DEVICE_ENDPOINT).
        assertTrue(CleartextRule.allows("ws://otc.local:8080/ws"))
        assertFalse(CleartextRule.allows("ws://cala.off-the.cloud/ws"))
        assertFalse(CleartextRule.allows("http://example.com/media/x"))
        assertFalse(CleartextRule.allows("ws://"))
    }
}
