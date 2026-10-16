// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.PubKey
import cloud.offthe.otc.proto.RespEnvelope
import com.google.protobuf.ByteString
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

// "Try the demo" (OnboardingView, OTCConnection.demoPassword): GetPubKey's
// answer, before signing in, decides whether there is a demo device to sign
// in to and with which password; anything else says why in the words the
// connection card would.
class DemoDeviceTest {
    private fun key(demo: Boolean, password: String) = RespEnvelope.newBuilder().setRespPubKey(
        PubKey.newBuilder().setPublicKey(ByteString.copyFromUtf8("der")).setDemo(demo).setDemoPassword(password),
    ).build()

    private fun refused(resp: RespEnvelope): String {
        val a = DemoDevice.answer(resp)
        assertTrue("expected a refusal, got $a", a is DemoDevice.Answer.Refused)
        return (a as DemoDevice.Answer.Refused).text.english()
    }

    @Test fun itIsTheDeviceNamedDemoOnTheBridge() {
        assertEquals("wss://demo.off-the.cloud/ws", DemoDevice.endpoint)
        // What the forms make of the name, and back.
        assertEquals(SecretsStore.bridgeEndpoint("demo"), DemoDevice.endpoint)
        assertEquals("demo", SecretsStore.bridgeName(DemoDevice.endpoint))
    }

    @Test fun aDemoDeviceSignsInWithItsPublishedPassword() {
        val a = DemoDevice.answer(key(demo = true, password = "look-around-2026"))
        assertTrue(a is DemoDevice.Answer.SignIn)
        assertEquals("look-around-2026", (a as DemoDevice.Answer.SignIn).password)
    }

    @Test fun anyOtherDeviceIsNotSignedInTo() {
        // An ordinary device, one that says demo with no password, or a
        // password without demo (it never publishes one then).
        assertEquals("Authentication failed", refused(key(demo = false, password = "")))
        assertEquals("Authentication failed", refused(key(demo = true, password = "")))
        assertEquals("Authentication failed", refused(key(demo = false, password = "x")))
    }

    @Test fun noKeySaysWhyAsTheSignInDoes() {
        fun ack(code: String, msg: String) = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(false).setCode(code).setErrorMsg(msg)).build()
        // The bridge's verdicts, worded by their code (its message is English only).
        assertEquals("This device isn't available right now", refused(ack("device_unreachable", "device not connected")))
        assertEquals("This account has been disabled", refused(ack("account_disabled", "disabled")))
        // The device's own words, else ours.
        assertEquals("Ocupado", refused(ack("", "Ocupado")))
        assertEquals("Unable to fetch the connection's public key", refused(ack("", "")))
        assertEquals("Unable to fetch the connection's public key", refused(RespEnvelope.getDefaultInstance()))
    }
}
