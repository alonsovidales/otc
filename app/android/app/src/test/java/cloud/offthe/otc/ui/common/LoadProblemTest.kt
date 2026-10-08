// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import cloud.offthe.otc.net.WSClient
import cloud.offthe.otc.net.retryWaitMs
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.RespEnvelope
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException

// What a screen says when a load failed, and how long it waits to ask again.
class LoadProblemTest {
    private val unreachable = RespEnvelope.newBuilder().setError(true).setErrorMessage("The device is not connected")
        .setRespAck(Ack.newBuilder().setOk(false).setCode(DEVICE_UNREACHABLE).setErrorMsg("The device is not connected")).build()
    private val deviceError = RespEnvelope.newBuilder().setError(true).setErrorMessage("internal error").build()

    @Test fun offlineComesFirstWhateverFailed() {
        assertEquals(LoadProblem.OFFLINE, loadProblem(null, IOException("x"), online = false, statusCode = null))
        assertEquals(LoadProblem.OFFLINE, loadProblem(unreachable, null, online = false, statusCode = null))
        assertEquals(LoadProblem.OFFLINE, loadProblem(null, WSClient.RequestTimeout(), online = false, statusCode = null))
    }

    @Test fun theBridgesVerdictIsUnreachable() {
        // Its answer to the request itself, or at sign-in (OTCConnection threw).
        assertEquals(LoadProblem.UNREACHABLE, loadProblem(unreachable, null, online = true, statusCode = null))
        assertEquals(LoadProblem.UNREACHABLE, loadProblem(null, IOException("refused"), online = true, statusCode = DEVICE_UNREACHABLE))
    }

    @Test fun noAnswerInTimeIsSlow() {
        assertEquals(LoadProblem.SLOW, loadProblem(null, WSClient.RequestTimeout(), online = true, statusCode = null))
    }

    @Test fun anythingElseIsAFailure() {
        assertEquals(LoadProblem.FAILED, loadProblem(deviceError, null, online = true, statusCode = null))
        assertEquals(LoadProblem.FAILED, loadProblem(null, IOException("connection closed"), online = true, statusCode = null))
        // A sign-in the account turned down is not the device being away.
        assertEquals(LoadProblem.FAILED, loadProblem(null, IOException("x"), online = true, statusCode = "account_disabled"))
    }

    @Test fun eachProblemSaysWhyInPlainWords() {
        assertEquals("Couldn't load your photos", PHOTOS_PROBLEM_TITLE)
        assertEquals("This phone is offline. Your photos will appear once it's back online.", photosProblemText(LoadProblem.OFFLINE))
        assertEquals("Your device isn't reachable right now. Your photos will appear as soon as it answers.", photosProblemText(LoadProblem.UNREACHABLE))
        assertEquals("Your device took too long to answer. Your photos will appear as soon as it does.", photosProblemText(LoadProblem.SLOW))
        assertEquals("Your photos will appear as soon as your device answers.", photosProblemText(LoadProblem.FAILED))
        assertEquals("This phone is offline. The posts will appear once it's back online.", loadProblemText(LoadProblem.OFFLINE, "The posts"))
        assertEquals("Couldn't load more photos.", MORE_PHOTOS_PROBLEM)
        // Every text differs: the reason is what tells them apart.
        assertEquals(4, LoadProblem.entries.map { photosProblemText(it) }.toSet().size)
    }

    @Test fun backOnlineOfflineGivesWayToTheNeutralLine() {
        assertEquals(LoadProblem.FAILED, LoadProblem.OFFLINE.backOnline(online = true))
        assertEquals(LoadProblem.OFFLINE, LoadProblem.OFFLINE.backOnline(online = false))
        // Any other reason still says why.
        for (p in listOf(LoadProblem.UNREACHABLE, LoadProblem.SLOW, LoadProblem.FAILED)) assertEquals(p, p.backOnline(online = true))
    }

    @Test fun theWordingIOSCopies() {
        assertEquals("Try again", TRY_AGAIN)
        assertEquals("Trying…", TRYING)
        assertEquals("Couldn't load more posts.", MORE_POSTS_PROBLEM)
        assertEquals("Still waiting for your device…", PHOTOS_SLOW)
        assertEquals("Loading photos", PHOTOS_LOADING)
        assertEquals("Couldn't play this video.", VIDEO_UNPLAYABLE)
        assertEquals("Your device took too long to answer. The posts will appear as soon as it does.", loadProblemText(LoadProblem.SLOW, "The posts"))
        assertEquals("The files will appear as soon as your device answers.", loadProblemText(LoadProblem.FAILED, "The files"))
    }

    @Test fun theWaitDoublesFromOneSecondToTen() {
        assertEquals(listOf(1_000L, 2_000L, 4_000L, 8_000L, 10_000L, 10_000L, 10_000L), (1..7).map { retryWaitMs(it) })
        assertEquals(1_000L, retryWaitMs(0))
        assertTrue(retryWaitMs(1_000) == 10_000L)
    }
}
