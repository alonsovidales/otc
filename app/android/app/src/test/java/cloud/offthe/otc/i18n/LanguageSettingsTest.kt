// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.i18n

import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException
import java.util.Locale

// The stored choice of language (docs/i18n.md): resolution, the device's
// answers, the 30 s window, the copy kept through Log Out, and a choice made
// with no device signed in, for the next one. The iOS app's
// LanguageTests.swift is the same.
class LanguageSettingsTest {
    /** The seven languages, as a build that ships them all lists them, plus the pseudo-locale of draft builds. */
    private val seven = listOf(
        Language("en", "en", "English"),
        Language("es", "es", "Español"),
        Language("fr", "fr", "Français"),
        Language("de", "de", "Deutsch"),
        Language("it", "it", "Italiano"),
        Language("pt", "pt-PT", "Português"),
        Language("nl", "nl", "Nederlands"),
        Language("qps", "en-Qaaa", "Pseudo"),
    )
    private val t0 = 1_800_000_000_000L

    private fun sys(vararg tags: String) = tags.map { Locale.forLanguageTag(it) }

    @Test fun theSystemLanguageMatchesByBaseLanguage() {
        assertEquals("es", LanguageChoice.effective("", sys("es-MX"), seven))
        assertEquals("pt", LanguageChoice.effective("", sys("pt-BR"), seven))
        assertEquals("nl", LanguageChoice.effective("", sys("nl-BE"), seven))
        // The whole list counts.
        assertEquals("es", LanguageChoice.effective("", sys("ca-ES", "es-ES"), seven))
        assertEquals("fr", LanguageChoice.effective("", sys("fr-CA", "de-DE"), seven))
    }

    @Test fun noMatchIsEnglishAndThePseudoLocaleNeverMatches() {
        assertEquals("en", LanguageChoice.effective("", sys("ja-JP"), seven))
        assertEquals("en", LanguageChoice.effective("", sys("en-US"), seven))
        assertEquals("en", LanguageChoice.effective("", sys("en-Qaaa"), seven))
        assertEquals("en", LanguageChoice.effective("", emptyList(), seven))
        // Only the pseudo-locale and a language it doesn't speak: still English.
        assertEquals("en", LanguageChoice.matchSystem(sys("en-Qaaa"), listOf(seven[7], seven[1])))
    }

    @Test fun aChosenLanguageWinsAndACodeThisBuildDoesntHaveIsEnglish() {
        assertEquals("de", LanguageChoice.effective("de", sys("es-ES"), seven))
        assertEquals("qps", LanguageChoice.effective("qps", sys("es-ES"), seven))
        assertEquals("en", LanguageChoice.effective("es", sys("es-ES"), listOf(seven[0])))
        assertEquals("en", LanguageChoice.effective("", sys("es-ES"), listOf(seven[0])))
        assertEquals("en", LanguageChoice.effective("xx", sys("es-ES"), seven))
    }

    @Test fun androidIsHandedTheFullTag() {
        assertEquals("", LanguageChoice.tagFor("", seven))
        assertEquals("pt-PT", LanguageChoice.tagFor("pt", seven))
        assertEquals("es", LanguageChoice.tagFor("es", seven))
        // A code this build doesn't have shows English.
        assertEquals("en", LanguageChoice.tagFor("xx", seven))
        assertEquals("en-Qaaa", LanguageChoice.tagFor("qps", seven))
    }

    @Test fun androidsTagsMapBackToALanguage() {
        assertEquals("", LanguageChoice.codeFor("", seven))
        assertEquals("pt", LanguageChoice.codeFor("pt-PT", seven))
        // A pick in the system's screen may carry a region.
        assertEquals("de", LanguageChoice.codeFor("de-AT", seven))
        assertEquals("pt", LanguageChoice.codeFor("pt-BR", seven))
        assertEquals("qps", LanguageChoice.codeFor("en-Qaaa", seven))
        assertEquals("en", LanguageChoice.codeFor("en-GB", seven))
        assertEquals("", LanguageChoice.codeFor("ja", seven))
        assertTrue(LanguageChoice.showsAlready("de-AT", "de"))
        assertTrue(LanguageChoice.showsAlready("", ""))
        assertFalse(LanguageChoice.showsAlready("", "es"))
        assertFalse(LanguageChoice.showsAlready("es", ""))
        assertFalse(LanguageChoice.showsAlready("es", "fr"))
        // The pseudo-locale isn't English, nor English the pseudo-locale.
        assertFalse(LanguageChoice.showsAlready("en", "en-Qaaa"))
        assertFalse(LanguageChoice.showsAlready("en-Qaaa", "en"))
        assertTrue(LanguageChoice.showsAlready("pt-BR", "pt-PT"))
    }

    @Test fun aChangeIsPendingUntilTheDeviceTakesIt() {
        val c = LanguageChoice(seen = "").chosen("es", t0)
        assertTrue(c.pending)
        val taken = c.taken("es")
        assertFalse(taken.pending)
        assertEquals("es", taken.seen)
    }

    @Test fun whilePendingAnotherValueIsIgnoredForThirtySeconds() {
        val c = LanguageChoice("en", seen = "en").chosen("es", t0)
        // An answer given before ours reached the device.
        assertEquals(c, c.deviceSaid("en", t0 + 5_000))
        assertEquals(c, c.deviceSaid("en", t0 + 29_999))
        // After 30 s the device's value wins.
        val later = c.deviceSaid("en", t0 + 30_000)
        assertEquals("en", later.language)
        assertFalse(later.pending)
        assertEquals("en", later.seen)
    }

    @Test fun seeingItsOwnValueEndsTheWait() {
        val c = LanguageChoice("en", seen = "en").chosen("es", t0).deviceSaid("es", t0 + 1_000)
        assertFalse(c.pending)
        assertEquals(0L, c.pendingAt)
        // A change made in another app right after is taken at once.
        assertEquals("fr", c.deviceSaid("fr", t0 + 2_000).language)
    }

    @Test fun aStaleAnswerAfterTheAckIsStillIgnoredInTheWindow() {
        val c = LanguageChoice("en", seen = "en").chosen("es", t0).taken("es")
        assertEquals("es", c.deviceSaid("en", t0 + 3_000).language)
    }

    @Test fun sendingAgainRestartsTheWindow() {
        val c = LanguageChoice("en", seen = "en").chosen("es", t0).sending(t0 + 600_000)
        val after = c.deviceSaid("en", t0 + 610_000)
        assertEquals("es", after.language)
        assertTrue(after.pending)
    }

    @Test fun aValueFromAnotherAppIsAdoptedAndAnOldDeviceChangesNothing() {
        val c = LanguageChoice(seen = "")
        assertEquals("de", c.deviceSaid("de", t0).language)
        val local = LanguageChoice("es")
        assertEquals(local, local.deviceSaid(null, t0))
    }

    @Test fun changedAdoptsTheDevicesValueUnlessThereIsANewerChoice() {
        val base = LanguageChoice("en", seen = "en").chosen("es", t0)
        val adopted = base.refused("es", "fr")
        assertEquals(LanguageChoice("fr", pending = false, pendingAt = 0L, seen = "fr"), adopted)
        val newer = base.chosen("de", t0 + 1_000).refused("es", "fr")
        assertEquals("de", newer.language)
        assertTrue(newer.pending)
        assertEquals("fr", newer.seen)
    }

    @Test fun aChangeSentLongAfterItWasMadeOutweighsTheDevicesDefault() {
        // Kept pending through a device too old to take it, and sent at the
        // connection after its update, an hour later: the device's default
        // from its first Status doesn't undo it.
        val c = LanguageChoice("en").chosen("es", t0).sending(t0 + 3_600_000).deviceSaid("", t0 + 3_601_000)
        assertEquals("es", c.language)
        assertTrue(c.pending)
    }

    @Test fun logOutKeepsTheCopyAndForgetsTheDevice() {
        val c = LanguageChoice("en", seen = "en").chosen("it", t0).loggedOut()
        assertEquals("it", c.language)
        assertFalse(c.pending)
        assertEquals(0L, c.pendingAt)
        assertNull(c.seen)
        // Log Out wipes otc_secrets, otc_settings, otc_sync and the files: the copy has its own prefs file.
        assertEquals("otc_language", LanguageSettings.PREFS)
    }

    // ---- picked with no device signed in (docs/i18n.md, the next device) ----

    @Test fun aChoiceWithNoValueSeenIsForTheNextDeviceWithoutExpected() {
        val c = LanguageChoice("en").chosen("es", t0)
        assertTrue(c.pending && c.forNextDevice)
        // Left out, never "": a device holding a language would refuse "".
        assertNull(c.expected)
        // Seen "" (a device on Automatic): a change against that.
        val d = LanguageChoice("", seen = "").chosen("es", t0)
        assertFalse(d.forNextDevice)
        assertEquals("", d.expected)
    }

    @Test fun aChoiceForTheNextDeviceIgnoresWhatADeviceSaysUntilTheSignIn() {
        val c = LanguageChoice("en").chosen("es", t0)
        // Long after the window: still waiting for the next sign-in.
        val later = c.deviceSaid("fr", t0 + 3_600_000)
        assertEquals(c, later)
        assertNull(later.seen)
        val signedIn = later.connected()
        assertTrue(signedIn.pending)
        assertFalse(signedIn.forNextDevice)
        assertNull(signedIn.expected)
    }

    @Test fun logOutAndADroppedSessionKeepAChoiceForTheNextDevice() {
        val c = LanguageChoice("en").chosen("nl", t0).sessionEnded().loggedOut()
        assertEquals("nl", c.language)
        assertTrue(c.pending && c.forNextDevice)
        assertNull(c.expected)
    }

    @Test fun aSignedInChangeKeepsItsExpectedWhenTheSessionEnds() {
        val c = LanguageChoice("en", seen = "en").chosen("es", t0).sessionEnded()
        assertNull(c.seen)
        assertTrue(c.pending)
        assertFalse(c.forNextDevice)
        assertEquals("en", c.expected)
        // Picked again before the next sign-in: for the next device.
        val again = c.chosen("de", t0 + 1_000)
        assertTrue(again.forNextDevice)
        assertNull(again.expected)
    }

    @Test fun leavingFromTheCardsCarriesAPendingChangeToTheNextDevice() {
        val c = LanguageChoice("en", seen = "en").chosen("pt", t0).loggedOut(carryPending = true)
        assertEquals("pt", c.language)
        assertTrue(c.pending && c.forNextDevice)
        assertNull(c.expected)
        assertNull(c.seen)
        // Nothing pending: nothing to carry.
        val d = LanguageChoice("pt", seen = "pt").loggedOut(carryPending = true)
        assertFalse(d.pending || d.forNextDevice)
    }

    @Test fun aChangeMadeWhileOneWasOnItsWayGoesNextAgainstTheValueSent() {
        val c = LanguageChoice("en").chosen("es", t0).chosen("de", t0 + 1_000).taken("es")
        assertEquals("de", c.language)
        assertTrue(c.pending)
        assertEquals("es", c.expected)
        assertFalse(c.forNextDevice)
    }

    @Test fun aRelaunchKeepsAPendingChangeButNotTheDeviceValue() {
        val pending = LanguageChoice("en", seen = "en").chosen("es", t0)
        val kept = pending.toStored()
        assertFalse(kept.containsKey("seen"))
        assertEquals(pending.copy(seen = null), LanguageChoice.stored(kept))
        val forNext = LanguageChoice("en").chosen("de", t0)
        assertEquals(forNext, LanguageChoice.stored(forNext.toStored()))
        // Not pending: expected and forNextDevice are left alone.
        val taken = LanguageChoice.stored(mapOf("language" to "it", "pending" to false, "pendingAt" to 0L, "expected" to "en", "forNextDevice" to true))
        assertEquals(LanguageChoice("it"), taken)
        // A seen kept by an earlier build is no longer read.
        assertEquals(LanguageChoice("fr"), LanguageChoice.stored(mapOf("language" to "fr", "seen" to "fr")))
    }

    @Test fun answersAreReadCodesFirst() {
        fun ack(ok: Boolean, code: String = "") = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(ok).setCode(code)).build()
        assertEquals(LanguageAnswer.TAKEN, LanguageAnswer.of(ack(true)))
        assertEquals(LanguageAnswer.CHANGED, LanguageAnswer.of(ack(false, "changed")))
        assertEquals(LanguageAnswer.FAILED, LanguageAnswer.of(ack(false, "forbidden")))
        val old = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("Este dispositivo no entiende esa petición").build()
        assertEquals(LanguageAnswer.TOO_OLD, LanguageAnswer.of(old))
        val older = RespEnvelope.newBuilder().setError(true).setErrorMessage("unknown payload").build()
        assertEquals(LanguageAnswer.TOO_OLD, LanguageAnswer.of(older))
        val other = RespEnvelope.newBuilder().setError(true).setErrorMessage("Not authenticated").build()
        assertEquals(LanguageAnswer.FAILED, LanguageAnswer.of(other))
    }

    @Test fun thisBuildsTableIsEnglishFirst() {
        // The picker stays hidden while English is the only language.
        assertEquals(Languages.SOURCE, Languages.all.first().code)
        assertEquals(Languages.all.size > 1, LanguageSettings.pickerShown)
    }
}

/**
 * A device stand-in for LanguageSync: answers SetLanguage from a queue -
 * or, with [stored] set and nothing queued, as the device does
 * (settings.SetLanguage: written when expected is left out or matches,
 * else "changed") - and records what it was sent. As FakeDevice in
 * LanguageTests.swift.
 */
private class FakeDevice : LanguageDevice {
    val answers = ArrayDeque<Result<RespEnvelope>>()
    val sent = mutableListOf<Pair<String, String?>>()
    var settingsLanguage: String? = null
    /** Whether a sign-in has been accepted (OTCConnection.authenticated). */
    var signedIn = true
    /** The device's own value, when it answers as the device does. */
    var stored: String? = null
    /** Holds each SetLanguage until released (one in flight). */
    var holding = false
    private val held = mutableListOf<CompletableDeferred<Unit>>()

    fun release() {
        val h = held.toList()
        held.clear()
        h.forEach { it.complete(Unit) }
    }

    override fun signedIn(): Boolean = signedIn

    override suspend fun setLanguage(language: String, expected: String?): RespEnvelope {
        sent += language to expected
        if (holding) CompletableDeferred<Unit>().also { held += it }.await()
        if (answers.isEmpty()) {
            val value = stored ?: throw IOException("offline")
            if (expected != null && expected != value) return ack(false, "changed")
            stored = language
            settingsLanguage = language
            return ack(true)
        }
        return answers.removeFirst().getOrThrow()
    }

    override suspend fun language(): String? = stored ?: settingsLanguage

    companion object {
        fun ack(ok: Boolean, code: String = ""): RespEnvelope =
            RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(ok).setCode(code)).build()

        val unknownPayload: RespEnvelope =
            RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("This device does not understand that request").build()
    }
}

// The rules with the device's answers (LanguageSync), as LanguageSettingsTests
// in LanguageTests.swift. The sends run unconfined: each one is over (or
// held by the stand-in) by the time the call that made it returns.
class LanguageSyncTest {
    private val t0 = 1_800_000_000_000L
    private var now = t0
    private val fake = FakeDevice()

    /** The prefs file: what save() keeps, read back at each launch. */
    private class Prefs {
        val values = mutableMapOf<String, Any?>()
        fun save(c: LanguageChoice) {
            for ((k, v) in c.toStored()) if (v == null) values.remove(k) else values[k] = v
        }
    }

    /** A launch: the copy as kept, and the device stand-in. */
    private fun make(prefs: Prefs, device: FakeDevice = fake) =
        LanguageSync(device, save = prefs::save, scope = CoroutineScope(Dispatchers.Unconfined), now = { now })
            .also { it.restore(LanguageChoice.stored(prefs.values)) }

    private fun ok() = Result.success(FakeDevice.ack(true))

    @Test fun aChangeIsSentExpectingWhatWasSeen() {
        val s = make(Prefs())
        s.deviceSaid("")
        fake.answers += ok()
        s.choose("es")
        assertEquals(listOf("es" to ""), fake.sent)
        assertFalse(s.choice.value.pending)
        assertEquals(true, s.deviceKeepsIt.value)
    }

    @Test fun neverSeeingTheDeviceSendsNoExpected() {
        val s = make(Prefs())
        fake.answers += ok()
        s.choose("de")
        assertEquals(listOf("de" to null), fake.sent)
    }

    @Test fun changedAdoptsTheDevicesCurrentValue() {
        val s = make(Prefs())
        s.deviceSaid("en")
        fake.answers += Result.success(FakeDevice.ack(false, "changed"))
        fake.settingsLanguage = "fr"
        s.choose("es")
        assertEquals("fr", s.choice.value.language)
        assertFalse(s.choice.value.pending)
        assertEquals("fr", s.choice.value.seen)
    }

    @Test fun unknownPayloadKeepsItPendingUntilTheNextConnection() {
        val s = make(Prefs())
        fake.answers += Result.success(FakeDevice.unknownPayload)
        s.choose("es")
        assertEquals("es", s.choice.value.language)
        assertTrue(s.choice.value.pending)
        assertEquals(false, s.deviceKeepsIt.value)
        // Another change on the same connection isn't sent; it stays pending.
        s.choose("de")
        assertEquals(1, fake.sent.size)
        assertTrue(s.choice.value.pending)
        // Long after, the device was updated: the next connection sends it,
        // and the device's default from its first Status doesn't undo it.
        now = t0 + 3_600_000
        fake.answers += ok()
        s.connected()
        s.deviceSaid("")
        assertEquals("de", s.choice.value.language)
        assertEquals(2, fake.sent.size)
        assertEquals("de" to null, fake.sent.last())
        assertFalse(s.choice.value.pending)
    }

    @Test fun aFailureStaysPendingAndGoesAtTheNextConnection() {
        val prefs = Prefs()
        val s = make(prefs)
        s.deviceSaid("en")
        // No answers queued: the request fails as if offline.
        s.choose("nl")
        assertTrue(s.choice.value.pending)
        assertEquals(1, fake.sent.size)
        // Still pending after a relaunch.
        val again = make(prefs)
        assertTrue(again.choice.value.pending)
        assertEquals("nl", again.choice.value.language)
        fake.answers += ok()
        again.connected()
        assertEquals(2, fake.sent.size)
        assertEquals("nl" to "en", fake.sent.last())
        assertFalse(again.choice.value.pending)
    }

    @Test fun theDevicesValueWaitsThirtySecondsWhilePending() {
        val s = make(Prefs())
        s.deviceSaid("en")
        s.choose("es") // fails: stays pending
        now = t0 + 10_000
        s.deviceSaid("en")
        assertEquals("es", s.choice.value.language)
        now = t0 + 31_000
        s.deviceSaid("en")
        assertEquals("en", s.choice.value.language)
    }

    @Test fun theCopySurvivesLogOutAndARelaunch() {
        val prefs = Prefs()
        val s = make(prefs)
        s.deviceSaid("en")
        fake.answers += ok()
        s.choose("pt")
        s.loggedOut(carryPending = false)
        assertEquals("pt", s.choice.value.language)
        assertNull(s.choice.value.seen)
        assertNull(s.deviceKeepsIt.value)
        val relaunched = make(prefs)
        assertEquals("pt", relaunched.choice.value.language)
        assertFalse(relaunched.choice.value.pending)
        assertFalse(prefs.values.containsKey("seen"))
    }

    // ---- picked with no device signed in (docs/i18n.md, the next device) ----

    @Test fun aChoiceMadeSignedOutIsSentAtTheFirstSignInWithoutExpected() {
        fake.signedIn = false
        val s = make(Prefs())
        s.choose("es")
        // Kept, and nothing sent before a sign-in.
        assertTrue(fake.sent.isEmpty())
        assertTrue(s.choice.value.pending && s.choice.value.forNextDevice)
        assertNull(s.choice.value.expected)
        // Signed in long after; the session's first Status still holds the
        // device's older value and must not undo the choice.
        now = t0 + 3_600_000
        fake.signedIn = true
        fake.answers += ok()
        s.connected()
        s.deviceSaid("fr")
        assertEquals("es", s.choice.value.language)
        assertEquals(listOf("es" to null), fake.sent)
        assertFalse(s.choice.value.pending)
        // An older Status answered after the ok is still ignored.
        now = t0 + 3_605_000
        s.deviceSaid("fr")
        assertEquals("es", s.choice.value.language)
    }

    @Test fun aChoiceForTheNextDeviceReachesANewDeviceAndOneAnotherAppSet() {
        for (held in listOf("", "it")) {
            val device = FakeDevice()
            // Signed in to another device before: Log Out, then picked on
            // the welcome screen.
            val s = make(Prefs(), device)
            s.deviceSaid("nl")
            s.loggedOut(carryPending = false)
            device.signedIn = false
            s.choose("de")
            // The next device: a new one ("", Automatic) or one where
            // another app set a language.
            device.stored = held
            device.signedIn = true
            s.connected()
            assertEquals(listOf("de" to null), device.sent)
            assertEquals("de", device.stored)
            assertFalse(s.choice.value.pending)
            assertEquals("de", s.choice.value.language)
        }
    }

    @Test fun unknownPayloadKeepsAChoiceMadeSignedOutPending() {
        fake.signedIn = false
        val s = make(Prefs())
        s.choose("it")
        fake.signedIn = true
        fake.answers += Result.success(FakeDevice.unknownPayload)
        s.connected()
        assertEquals("it", s.choice.value.language)
        assertTrue(s.choice.value.pending)
        assertEquals(false, s.deviceKeepsIt.value)
        // A Status of that device (no field) changes nothing; the next
        // connection sends it again.
        s.deviceSaid(null)
        assertTrue(s.choice.value.pending)
        fake.answers += ok()
        s.connected()
        assertEquals(2, fake.sent.size)
        assertEquals("it" to null, fake.sent.last())
        assertFalse(s.choice.value.pending)
    }

    @Test fun aSignInWhileASendIsInFlightDoesntSendTwice() {
        fake.signedIn = false
        val s = make(Prefs())
        s.choose("fr")
        fake.signedIn = true
        fake.holding = true
        fake.answers += ok()
        s.connected()
        assertEquals(1, fake.sent.size)
        // Another accepted sign-in (a reconnect) while it is on its way.
        s.connected()
        assertEquals(1, fake.sent.size)
        fake.release()
        assertEquals(1, fake.sent.size)
        assertFalse(s.choice.value.pending)
    }

    @Test fun aSendThatFailsAfterASignInCameMeanwhileGoesAgainOnce() {
        val s = make(Prefs())
        s.deviceSaid("en")
        fake.holding = true
        // The first one fails (the session it went on ended)...
        fake.answers += Result.failure(IOException("connection lost"))
        fake.answers += ok()
        s.choose("pt")
        // ...after the connection signed in again.
        s.connected()
        fake.holding = false
        fake.release()
        assertEquals(listOf("pt" to "en", "pt" to "en"), fake.sent)
        assertFalse(s.choice.value.pending)
    }

    @Test fun aDroppedSessionForgetsTheDeviceValue() {
        val s = make(Prefs())
        s.deviceSaid("es")
        // The device turned the password down: re-imaged, it holds "".
        s.sessionEnded()
        fake.signedIn = false
        s.choose("fr")
        assertNull(s.choice.value.expected)
        assertTrue(s.choice.value.forNextDevice)
        fake.stored = ""
        fake.signedIn = true
        s.connected()
        assertEquals(listOf("fr" to null), fake.sent)
        // Not refused against the "es" of the session that ended.
        assertEquals("fr", fake.stored)
        assertEquals("fr", s.choice.value.language)
        assertFalse(s.choice.value.pending)
    }

    @Test fun aSignedInChangeKeepsItsExpectedAndChangedAdoptsTheOtherAppsValue() {
        val s = make(Prefs())
        s.deviceSaid("en")
        // Made signed in; its send fails.
        s.choose("es")
        assertEquals(listOf("es" to "en"), fake.sent)
        assertTrue(s.choice.value.pending)
        // The session drops, and meanwhile another app sets Italiano.
        s.sessionEnded()
        fake.signedIn = false
        fake.stored = "it"
        fake.signedIn = true
        s.connected()
        assertEquals(2, fake.sent.size)
        assertEquals("es" to "en", fake.sent.last())
        // "changed": the other app's value wins, read again.
        assertEquals("it", fake.stored)
        assertEquals("it", s.choice.value.language)
        assertFalse(s.choice.value.pending)
    }

    @Test fun aRelaunchDoesntDropAChoiceMadeSignedOut() {
        val prefs = Prefs()
        fake.signedIn = false
        make(prefs).choose("nl")
        // Relaunched: the copy, still pending and for the next device.
        val again = make(prefs)
        assertEquals("nl", again.choice.value.language)
        assertTrue(again.choice.value.pending && again.choice.value.forNextDevice)
        assertNull(again.choice.value.expected)
        fake.stored = "en"
        fake.signedIn = true
        again.connected()
        again.deviceSaid("en")
        assertEquals(listOf("nl" to null), fake.sent)
        assertEquals("nl", fake.stored)
        assertEquals("nl", again.choice.value.language)
    }

    @Test fun logOutKeepsAChoiceForTheNextDeviceAndDropsOneMadeSignedIn() {
        // Signed in: the send fails, Log Out drops it.
        val s = make(Prefs())
        s.deviceSaid("en")
        s.choose("es")
        s.loggedOut(carryPending = false)
        assertEquals("es", s.choice.value.language)
        assertFalse(s.choice.value.pending)
        // Picked on the welcome screen: Log Out keeps it.
        fake.signedIn = false
        s.choose("de")
        s.loggedOut(carryPending = false)
        assertTrue(s.choice.value.pending && s.choice.value.forNextDevice)
        // Leaving from the cards carries one made signed in.
        val other = make(Prefs())
        fake.signedIn = true
        other.deviceSaid("en")
        other.choose("fr")
        other.loggedOut(carryPending = true)
        assertTrue(other.choice.value.pending && other.choice.value.forNextDevice)
        assertNull(other.choice.value.expected)
    }
}
