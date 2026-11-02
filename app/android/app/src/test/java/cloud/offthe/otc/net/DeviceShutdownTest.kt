// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appSettingsUpdateShutDown
import cloud.offthe.otc.i18n.appSettingsUpdateShutDownConfirm
import cloud.offthe.otc.i18n.appSettingsUpdateShutDownFailed
import cloud.offthe.otc.i18n.appSettingsUpdateShutDownMessage
import cloud.offthe.otc.i18n.appSettingsUpdateShutDownNeedsUpdate
import cloud.offthe.otc.i18n.appSettingsUpdateShutDownTitle
import cloud.offthe.otc.i18n.appSettingsUpdateShuttingDownNote
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.settings.UpdateViewModel
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException

// Settings > Shut down device (issue #202, DeviceShutdown.kt): what the
// device's answers come to, the moment the app leaves a device that is
// turning off alone, and Device version's section. iOS's DeviceShutdownTests.
class DeviceShutdownTest {
    @After fun tearDown() {
        DeviceShutdown.forget()
        DeviceShutdown.clock = { System.nanoTime() / 1_000_000 }
    }

    private fun ack(ok: Boolean = true, errorMsg: String = ""): RespEnvelope =
        RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(ok).setErrorMsg(errorMsg)).build()

    private fun refusal(code: String, message: String): RespEnvelope =
        RespEnvelope.newBuilder().setError(true).setErrorCode(code).setErrorMessage(message).build()

    @Test fun theAckIsShuttingDown() {
        assertEquals(ShutDownAnswer.ShuttingDown, ShutDownAnswer.of(ack()))
    }

    @Test fun anOlderDeviceNeedsAnUpdate() {
        assertEquals(ShutDownAnswer.NeedsUpdate, ShutDownAnswer.of(refusal("unknown_payload", "")))
        // Builds before v9 said it in words alone.
        assertEquals(ShutDownAnswer.NeedsUpdate, ShutDownAnswer.of(refusal("", "unknown payload")))
    }

    @Test fun otherRefusalsAreTheDevicesWords() {
        // The demo, a container, a user's instance, an update under way.
        assertEquals(ShutDownAnswer.Refused("This is a demo device."), ShutDownAnswer.of(refusal("demo", "This is a demo device.")))
        assertEquals(ShutDownAnswer.Refused("An update is running."), ShutDownAnswer.of(refusal("update_running", "An update is running.")))
        // Words only in the Ack.
        assertEquals(ShutDownAnswer.Refused("Not now."), ShutDownAnswer.of(ack(ok = false, errorMsg = "Not now.")))
        // A localized device's words that happen to say "unknown payload"
        // with another code are its words.
        assertEquals(ShutDownAnswer.Refused("unknown payload here"), ShutDownAnswer.of(refusal("busy", "unknown payload here")))
    }

    @Test fun noWordsAreOurs() {
        assertEquals(ShutDownAnswer.Refused(null), ShutDownAnswer.of(refusal("", "")))
        assertEquals(ShutDownAnswer.Refused(null), ShutDownAnswer.of(ack(ok = false)))
        assertEquals(ShutDownAnswer.Refused(null), ShutDownAnswer.of(RespEnvelope.getDefaultInstance()))
        // An Ack on an envelope that says error isn't a yes.
        assertEquals(ShutDownAnswer.Refused(null), ShutDownAnswer.of(ack().toBuilder().setError(true).build()))
    }

    @Test fun theDeviceIsLeftAloneForAMomentAfterItsYes() {
        var now = 1_000_000L
        DeviceShutdown.clock = { now }
        // Nothing asked: as always.
        assertFalse(DeviceShutdown.quiet())
        assertEquals(0L, DeviceShutdown.quietLeftMs())

        DeviceShutdown.began()
        assertTrue(DeviceShutdown.quiet())
        assertEquals(DeviceShutdown.QUIET_MS, DeviceShutdown.quietLeftMs())

        now += 60_000
        assertTrue(DeviceShutdown.quiet())
        assertEquals(DeviceShutdown.QUIET_MS - 60_000, DeviceShutdown.quietLeftMs())

        // Afterwards the app looks for it again (and shows the usual card).
        now += DeviceShutdown.QUIET_MS
        assertFalse(DeviceShutdown.quiet())
        assertEquals(0L, DeviceShutdown.quietLeftMs())
    }

    @Test fun newCredentialsOrLogOutForgetIt() {
        DeviceShutdown.began()
        assertTrue(DeviceShutdown.quiet())
        DeviceShutdown.forget()
        assertFalse(DeviceShutdown.quiet())
    }

    @Test fun aRequestInThatMomentSaysSo() {
        val quiet = DeviceShutdown.Quiet()
        assertTrue(quiet is IOException)
        assertEquals("Shutting down… Once its lights stop blinking you can unplug it.", quiet.text.english())
    }

    /** Device version's view model, answered in the device's place. */
    private fun section(answer: () -> RespEnvelope): Pair<UpdateViewModel, MutableList<ReqEnvelope>> {
        val sent = mutableListOf<ReqEnvelope>()
        val vm = UpdateViewModel { build ->
            sent += ReqEnvelope.newBuilder().also(build).build()
            answer()
        }
        return vm to sent
    }

    @Test fun shutDownSaysItIsShuttingDownAndLeavesTheDeviceAlone() = runBlocking {
        val (vm, sent) = section { ack() }
        vm.shutDown()
        assertEquals(ReqEnvelope.PayloadCase.REQ_SHUT_DOWN_DEVICE, sent.single().payloadCase)
        val st = vm.state.value
        assertEquals("shutting_down", st.shutDownPhase)
        assertTrue(st.shuttingDown)
        assertNull(st.shutDownError)
        assertNull(st.needsUpdate)
        assertTrue(DeviceShutdown.quiet())
    }

    @Test fun shutDownOnAnOlderDeviceAsksForItsUpdate() = runBlocking {
        val (vm, _) = section { refusal("unknown_payload", "") }
        vm.shutDown()
        val st = vm.state.value
        assertEquals("", st.shutDownPhase)
        assertEquals("Your device needs an update before it can be shut down from here.", st.needsUpdate?.title?.english())
        // The section is the primary's: Update is offered.
        assertEquals(true, st.needsUpdate?.primary)
        assertFalse(DeviceShutdown.quiet())
        vm.dismissNeedsUpdate()
        assertNull(vm.state.value.needsUpdate)
    }

    @Test fun shutDownRefusedSaysTheDevicesWordsOrOurs() = runBlocking {
        val (vm, _) = section { refusal("demo", "This is a demo device.") }
        vm.shutDown()
        assertEquals("", vm.state.value.shutDownPhase)
        assertEquals(UiText.Raw("This is a demo device."), vm.state.value.shutDownError)
        assertFalse(DeviceShutdown.quiet())

        val (silent, _) = section { refusal("", "") }
        silent.shutDown()
        assertEquals("Could not shut down the device.", silent.state.value.shutDownError?.english())
    }

    @Test fun shutDownWithNoAnswerSaysWhy() = runBlocking {
        val (vm, _) = section { throw IOException("The connection went.") }
        vm.shutDown()
        assertEquals("", vm.state.value.shutDownPhase)
        assertEquals("The connection went.", vm.state.value.shutDownError?.english())
        assertFalse(DeviceShutdown.quiet())
    }

    @Test fun everyTextReadsAsWritten() {
        assertEquals("Shut Down Device", S.appSettingsUpdateShutDown().english())
        assertEquals("Shut down your device?", S.appSettingsUpdateShutDownTitle().english())
        assertEquals("It turns off completely and stays off until you start it again: unplug its power and plug it back in. Nobody can reach it meanwhile.", S.appSettingsUpdateShutDownMessage().english())
        assertEquals("Shut Down", S.appSettingsUpdateShutDownConfirm().english())
        assertEquals("Shutting down… Once its lights stop blinking you can unplug it.", S.appSettingsUpdateShuttingDownNote().english())
        assertEquals("Could not shut down the device.", S.appSettingsUpdateShutDownFailed().english())
        assertEquals("Your device needs an update before it can be shut down from here.", S.appSettingsUpdateShutDownNeedsUpdate().english())
    }
}
