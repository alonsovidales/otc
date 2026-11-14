// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RespUpdateInfo
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException

// Settings > Device version > Automatic updates: the switch is there only
// when the device says it has one, shows a change at once and goes back
// when the device refuses or doesn't answer.
class AutoUpdateTest {
    private fun ack(ok: Boolean = true, errorMsg: String = ""): RespEnvelope =
        RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(ok).setErrorMsg(errorMsg)).build()

    private fun refusal(code: String, message: String): RespEnvelope =
        RespEnvelope.newBuilder().setError(true).setErrorCode(code).setErrorMessage(message).build()

    private fun info(auto: Boolean?): RespEnvelope =
        RespEnvelope.newBuilder().setRespUpdateInfo(RespUpdateInfo.newBuilder().setCurrentVersion(183).also { if (auto != null) it.setAutoUpdate(auto) }).build()

    /** Device version's view model, answered in the device's place. */
    private fun section(answer: (ReqEnvelope) -> RespEnvelope): Pair<UpdateViewModel, MutableList<ReqEnvelope>> {
        val sent = mutableListOf<ReqEnvelope>()
        val vm = UpdateViewModel { build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            answer(req)
        }
        return vm to sent
    }

    @Test fun theSwitchIsThereOnlyWhenTheDeviceHasOne() {
        assertNull(autoUpdateShown(RespUpdateInfo.getDefaultInstance()))
        assertEquals(true, autoUpdateShown(RespUpdateInfo.newBuilder().setAutoUpdate(true).build()))
        // Off is said too: a switch that is off, not no switch.
        assertEquals(false, autoUpdateShown(RespUpdateInfo.newBuilder().setAutoUpdate(false).build()))
    }

    @Test fun anAnswerIsAYesOrSaysWhyNot() {
        assertNull(autoUpdateRefusal(ack()))
        assertEquals("This is a demo device.", autoUpdateRefusal(refusal("demo", "This is a demo device."))?.english())
        assertEquals("Not now.", autoUpdateRefusal(ack(ok = false, errorMsg = "Not now."))?.english())
        // No words from the device: ours.
        assertEquals("Could not change automatic updates.", autoUpdateRefusal(refusal("", ""))?.english())
        assertEquals("Could not change automatic updates.", autoUpdateRefusal(ack(ok = false))?.english())
        assertEquals("Could not change automatic updates.", autoUpdateRefusal(RespEnvelope.getDefaultInstance())?.english())
        // An Ack on an envelope that says error isn't a yes.
        assertEquals("Could not change automatic updates.", autoUpdateRefusal(ack().toBuilder().setError(true).build())?.english())
    }

    @Test fun theCheckSaysWhetherThereIsASwitch() = runBlocking {
        val (older, _) = section { info(null) }
        older.check()
        assertNull(older.state.value.autoUpdate)
        // Without a switch there is nothing to change: nothing is sent.
        older.setAutoUpdate(true)
        assertNull(older.state.value.autoUpdate)

        val (on, _) = section { info(true) }
        on.check()
        assertEquals(true, on.state.value.autoUpdate)

        val (off, _) = section { info(false) }
        off.check()
        assertEquals(false, off.state.value.autoUpdate)
    }

    @Test fun aChangeShowsAtOnceAndStaysOnTheDevicesYes() = runBlocking {
        lateinit var vm: UpdateViewModel
        var during: UpdateViewModel.State? = null
        val made = section { req ->
            if (req.payloadCase == ReqEnvelope.PayloadCase.REQ_SET_AUTO_UPDATE) { during = vm.state.value; ack() } else info(true)
        }
        vm = made.first
        vm.check()
        vm.setAutoUpdate(false)
        val sent = made.second.last()
        assertEquals(ReqEnvelope.PayloadCase.REQ_SET_AUTO_UPDATE, sent.payloadCase)
        assertFalse(sent.reqSetAutoUpdate.enabled)
        // While the device is asked: already off, and the switch can't be pressed.
        assertEquals(false, during?.autoUpdate)
        assertEquals(true, during?.savingAutoUpdate)
        val st = vm.state.value
        assertEquals(false, st.autoUpdate)
        assertFalse(st.savingAutoUpdate)
        assertNull(st.autoUpdateError)
    }

    @Test fun aRefusalPutsItBackWithTheDevicesWords() = runBlocking {
        val (vm, _) = section { req ->
            if (req.payloadCase == ReqEnvelope.PayloadCase.REQ_SET_AUTO_UPDATE) refusal("demo", "This is a demo device.") else info(true)
        }
        vm.check()
        vm.setAutoUpdate(false)
        var st = vm.state.value
        assertEquals(true, st.autoUpdate)
        assertFalse(st.savingAutoUpdate)
        assertEquals("This is a demo device.", st.autoUpdateError?.english())

        // The next try clears what the last one said.
        val (again, _) = section { req ->
            if (req.payloadCase == ReqEnvelope.PayloadCase.REQ_SET_AUTO_UPDATE) ack(ok = false) else info(false)
        }
        again.check()
        again.setAutoUpdate(true)
        st = again.state.value
        assertEquals(false, st.autoUpdate)
        assertEquals("Could not change automatic updates.", st.autoUpdateError?.english())
    }

    @Test fun noAnswerPutsItBack() = runBlocking {
        var fail = false
        val (vm, _) = section { if (fail) throw IOException() else info(true) }
        vm.check()
        fail = true
        vm.setAutoUpdate(false)
        val st = vm.state.value
        assertEquals(true, st.autoUpdate)
        assertFalse(st.savingAutoUpdate)
        assertEquals("Could not change automatic updates.", st.autoUpdateError?.english())
        assertTrue(st.autoUpdateError != null)
    }
}
