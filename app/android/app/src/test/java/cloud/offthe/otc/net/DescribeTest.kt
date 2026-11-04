// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Test
import java.io.EOFException
import java.io.IOException
import java.net.NoRouteToHostException
import java.net.UnknownHostException

// What the can't-connect card says for what the network stack threw: in
// the app's words, never the stack's own English ("Host unreachable",
// "unexpected end of stream", a bare class name).
class DescribeTest {
    private val noAnswer = "The device isn't answering. It may be off, or the address may be wrong."

    @Test fun theStacksOwnWordsNeverReachTheCard() {
        assertEquals(noAnswer, OTCConnection.describe(NoRouteToHostException("Host unreachable"), online = true).english())
        assertEquals(noAnswer, OTCConnection.describe(IOException("unexpected end of stream on http://192.168.1.20:8080/..."), online = true).english())
        assertEquals(noAnswer, OTCConnection.describe(IOException("connection closed (1006)"), online = true).english())
        assertEquals(noAnswer, OTCConnection.describe(EOFException(), online = true).english())
        assertEquals(noAnswer, OTCConnection.describe(IllegalStateException(), online = true).english())
    }

    @Test fun whatIsWordedStaysWorded() {
        assertEquals("That address can't be found. Check the device name or address.", OTCConnection.describe(UnknownHostException("x"), online = true).english())
        assertEquals("Wrong password (said by the device)", OTCConnection.describe(OTCConnection.RequestError("Wrong password (said by the device)"), online = true).english())
        assertEquals(OTCConnection.OFFLINE_MESSAGE.english(), OTCConnection.describe(NoRouteToHostException("Host unreachable"), online = false).english())
    }
}
