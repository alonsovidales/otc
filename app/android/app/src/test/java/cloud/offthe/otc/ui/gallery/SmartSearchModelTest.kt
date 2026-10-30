// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.data.SmartSearchAnnouncement
import cloud.offthe.otc.data.SmartSearchModelState
import cloud.offthe.otc.data.SmartSearchModelState.Downloading
import cloud.offthe.otc.data.SmartSearchModelState.Missing
import cloud.offthe.otc.data.SmartSearchModelState.Ready
import cloud.offthe.otc.data.SmartSearchSupport
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.Settings
import cloud.offthe.otc.proto.SmartSearchModel
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Smart search's model (device release 152: downloaded only once Smart
// search is first turned on): where it is from the device's Settings, what
// Settings' section says and has TalkBack say, when Settings asks again,
// and the searches the device refuses.
class SmartSearchModelTest {
    @After fun tearDown() = SmartSearchSupport.reset()

    private fun settings(model: SmartSearchModel.Builder?, on: Boolean? = true): Settings {
        val b = Settings.newBuilder()
        if (on != null) b.smartSearchEnabled = on
        if (model != null) b.setSmartSearchModel(model)
        return b.build()
    }

    @Test fun whereTheModelIsFromTheDevicesSettings() {
        // A device before release 152 doesn't say: it had the model (or
        // refuses turning Smart search on) - everything as before.
        assertEquals(Ready, SmartSearchModelState.of(settings(null)))
        assertEquals(Ready, SmartSearchModelState.of(Settings.getDefaultInstance()))
        assertEquals(Ready, SmartSearchModelState.of(settings(SmartSearchModel.newBuilder().setReady(true).setDownloading(true).setPercent(40))))
        // Neither ready nor downloading: nothing asked for it yet.
        assertEquals(Missing, SmartSearchModelState.of(settings(SmartSearchModel.newBuilder())))
        assertEquals(Downloading(37), SmartSearchModelState.of(settings(SmartSearchModel.newBuilder().setDownloading(true).setPercent(37))))
        // The percent kept to 0-100; a problem of only spaces is none.
        assertEquals(Downloading(100), SmartSearchModelState.of(settings(SmartSearchModel.newBuilder().setDownloading(true).setPercent(140))))
        assertEquals(Downloading(0), SmartSearchModelState.of(settings(SmartSearchModel.newBuilder().setDownloading(true).setPercent(-3).setProblem("  "))))
        assertEquals(Downloading(12, "No internet"), SmartSearchModelState.of(settings(SmartSearchModel.newBuilder().setDownloading(true).setPercent(12).setProblem(" No internet "))))
    }

    @Test fun whatTheSectionSays() {
        assertNull(Ready.words())
        assertNull(Ready.progress)
        assertEquals("Turning it on the first time downloads about 400 MB to your device.", Missing.words()!!.english())
        assertNull(Missing.progress)
        assertEquals("Downloading what it needs… 37%", Downloading(37).words()!!.english())
        assertEquals(0.37f, Downloading(37).progress!!, 0.0001f)
        // A problem: the device's words in its place; the bar stays.
        assertEquals("No internet", Downloading(37, "No internet").words()!!.english())
        assertEquals(0.37f, Downloading(37, "No internet").progress!!, 0.0001f)
    }

    @Test fun settingsAsksAgainOnlyWhileOnAndDownloading() {
        assertEquals(3_000L, SmartSearchSupport.POLL_EVERY_MS)
        assertTrue(SmartSearchSupport.polls(true, Downloading(5)))
        assertTrue(SmartSearchSupport.polls(true, Downloading(5, "No internet")))
        assertFalse(SmartSearchSupport.polls(true, Ready))
        assertFalse(SmartSearchSupport.polls(true, Missing))
        assertFalse(SmartSearchSupport.polls(false, Downloading(5)))
        assertFalse(SmartSearchSupport.polls(null, Downloading(5)))
    }

    @Test fun talkBackIsToldOnlyOfTheStartAProblemAndTheEnd() {
        fun say(before: SmartSearchModelState?, now: SmartSearchModelState) = SmartSearchModelState.announcement(before, now)
        // The first view isn't announced, whatever it shows.
        assertNull(say(null, Downloading(40)))
        assertNull(say(null, Missing))
        // Started (turned on where it was missing).
        assertEquals(SmartSearchAnnouncement.Progress(0), say(Missing, Downloading(0)))
        assertEquals("Downloading what it needs… 0%", say(Missing, Downloading(0))!!.text().english())
        // Never at every percent.
        assertNull(say(Downloading(0), Downloading(3)))
        assertNull(say(Downloading(3), Downloading(80)))
        // A problem, once; another problem; going again after one.
        assertEquals(SmartSearchAnnouncement.Problem("No internet"), say(Downloading(10), Downloading(10, "No internet")))
        assertEquals("No internet", say(Downloading(10), Downloading(10, "No internet"))!!.text().english())
        assertNull(say(Downloading(10, "No internet"), Downloading(10, "No internet")))
        assertEquals(SmartSearchAnnouncement.Problem("Disk full"), say(Downloading(10, "No internet"), Downloading(10, "Disk full")))
        assertEquals(SmartSearchAnnouncement.Progress(11), say(Downloading(10, "No internet"), Downloading(11)))
        // Done: as 100%.
        assertEquals(SmartSearchAnnouncement.Progress(100), say(Downloading(97), Ready))
        assertEquals("Downloading what it needs… 100%", say(Downloading(97), Ready)!!.text().english())
        // Turned off (the download stops), or there all along: nothing.
        assertNull(say(Downloading(40), Missing))
        assertNull(say(Ready, Ready))
        assertNull(say(Missing, Ready))
    }

    @Test fun theSwitchAndTheModelFromOneAnswerAndBackAtLogOut() {
        SmartSearchSupport.deviceSaid(settings(SmartSearchModel.newBuilder().setDownloading(true).setPercent(20)))
        assertEquals(true, SmartSearchSupport.enabled.value)
        assertEquals(Downloading(20), SmartSearchSupport.model.value)
        assertFalse(SmartSearchSupport.searchable)
        // Asked again while on its way: it stays where it is.
        SmartSearchSupport.modelRequested()
        assertEquals(Downloading(20), SmartSearchSupport.model.value)
        SmartSearchSupport.deviceSaid(settings(SmartSearchModel.newBuilder().setReady(true)))
        assertTrue(SmartSearchSupport.searchable)
        // An older device's Settings: off, the model taken as there.
        SmartSearchSupport.deviceSaid(Settings.getDefaultInstance())
        assertEquals(false, SmartSearchSupport.enabled.value)
        assertEquals(Ready, SmartSearchSupport.model.value)
        // Turned on where it is missing, or a search told
        // smart_search_downloading: on its way at once.
        SmartSearchSupport.deviceSaid(settings(SmartSearchModel.newBuilder(), on = false))
        assertEquals(Missing, SmartSearchSupport.model.value)
        SmartSearchSupport.modelRequested()
        assertEquals(Downloading(0), SmartSearchSupport.model.value)
        // Log Out: the next device says for itself.
        SmartSearchSupport.reset()
        assertNull(SmartSearchSupport.enabled.value)
        assertEquals(Ready, SmartSearchSupport.model.value)
    }

    @Test fun theSearchesTheDeviceRefuses() {
        fun refused(code: String, words: String = "") =
            SmartSearchSupport.refusal(RespEnvelope.newBuilder().setError(true).setErrorCode(code).setErrorMessage(words).build())
        assertEquals(SmartSearchSupport.Refusal("Off", SmartSearchSupport.Refused.OFF), refused("smart_search_off", "Off"))
        assertEquals(SmartSearchSupport.Refusal(null, SmartSearchSupport.Refused.UNAVAILABLE), refused("smart_search_unavailable"))
        assertEquals(SmartSearchSupport.Refusal("Still downloading", SmartSearchSupport.Refused.DOWNLOADING), refused("smart_search_downloading", "Still downloading"))
        assertNull(refused("device_unreachable", "Unreachable"))
        assertNull(SmartSearchSupport.refusal(null))
        assertNull(SmartSearchSupport.refusal(RespEnvelope.getDefaultInstance()))
    }
}
