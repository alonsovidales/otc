// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appShellDeviceUpdateAskOwner
import cloud.offthe.otc.i18n.appShellDeviceUpdateStarted
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespInstanceRole
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException

// A feature the device is too old for: who may update it from here, and
// what starting the update says.
class DeviceNeedsUpdateTest {
    private val title = UiText.Raw("needs an update")
    private fun answering(resp: RespEnvelope?): DeviceRequest = { build ->
        ReqEnvelope.newBuilder().also(build).build()
        resp ?: throw IOException("no answer")
    }
    private fun role(primary: Boolean) = RespEnvelope.newBuilder().setRespInstanceRole(RespInstanceRole.newBuilder().setIsPrimary(primary)).build()

    @Test fun onlyThePrimaryCanUpdateFromHere() = runBlocking {
        val asked = mutableListOf<ReqEnvelope.PayloadCase>()
        val primary = deviceNeedsUpdate(title) { build -> asked += ReqEnvelope.newBuilder().also(build).build().payloadCase; role(true) }
        assertEquals(listOf(ReqEnvelope.PayloadCase.REQ_GET_INSTANCE_ROLE), asked)
        assertTrue(primary.primary)
        assertEquals(title, primary.title)
        assertFalse(deviceNeedsUpdate(title, answering(role(false))).primary)
        // No answer, or another one: not primary.
        assertFalse(deviceNeedsUpdate(title, answering(null)).primary)
        assertFalse(deviceNeedsUpdate(title, answering(RespEnvelope.newBuilder().setError(true).build())).primary)
    }

    @Test fun startingTheUpdateSaysHowItWent() = runBlocking {
        var sent: ReqEnvelope.PayloadCase? = null
        val started = startDeviceUpdate { build ->
            sent = ReqEnvelope.newBuilder().also(build).build().payloadCase
            RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()
        }
        assertEquals(ReqEnvelope.PayloadCase.REQ_APPLY_UPDATE, sent)
        assertEquals("Your device is updating. It restarts by itself in a few minutes; then try again.", started.english())
        // The device's words, ours when it gave none, the socket's when it didn't answer.
        val refused = RespEnvelope.newBuilder().setError(true).setErrorMessage("an update is already running").build()
        assertEquals("an update is already running", startDeviceUpdate(answering(refused)).english())
        assertEquals("Could not start the update.", startDeviceUpdate(answering(RespEnvelope.newBuilder().setError(true).build())).english())
        assertEquals("no answer", startDeviceUpdate(answering(null)).english())
    }

    @Test fun englishText() {
        assertEquals("Your device is updating. It restarts by itself in a few minutes; then try again.", S.appShellDeviceUpdateStarted().english())
        assertEquals("Ask whoever set up the device to update it in Settings › Device version.", S.appShellDeviceUpdateAskOwner().english())
    }
}
