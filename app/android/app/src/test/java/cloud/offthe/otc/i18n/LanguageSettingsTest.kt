// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.i18n

import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.RespEnvelope
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Locale

// The stored choice of language (docs/i18n.md): resolution, the device's
// answers, the 30 s window and the copy kept through Log Out. The iOS app's
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
