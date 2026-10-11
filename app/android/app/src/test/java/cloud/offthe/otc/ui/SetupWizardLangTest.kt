// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// The setup wizard speaks the app's language (docs/i18n.md, "Setup
// wizard"): over Bluetooth only the path travels, so every /api call
// carries ?lang= (the page is loaded with it), and an open page follows a
// change (window.otcSetupLang, or a reload once the install has started).
// As SetupWizardLangTests.swift; the web view's side (the page's answers,
// sessionStorage across its own reload) was checked on an emulator.
class SetupWizardLangTest {
    @Test fun everyApiCallCarriesIt() {
        assertEquals("/api/state?lang=es", withLang("/api/state", "es"))
        assertEquals("/api/scan?band=5&lang=fr", withLang("/api/scan?band=5", "fr"))
        assertEquals("/api?lang=de", withLang("/api", "de"))
    }

    @Test fun aLanguageAlreadyThereAndOtherPathsAreLeftAlone() {
        assertEquals("/api/state?lang=fr", withLang("/api/state?lang=fr", "es"))
        assertEquals("/", withLang("/", "es"))
        assertEquals("/__otc/release", withLang("/__otc/release", "es"))
        assertEquals("/apiary", withLang("/apiary", "es"))
    }

    @Test fun thePageLoadsInTheAppsLanguage() {
        assertEquals("https://device/?lang=es", BLESetupLang.pageUrl("es"))
        assertEquals("https://device/?lang=pt", BLESetupLang.pageUrl("pt"))
    }

    @Test fun anOpenPageIsAskedToReWordItselfWithTheCodeQuoted() {
        // JSONObject.quote on the phone; JVM tests have no org.json.
        val quote = { c: String -> "\"" + c.replace("\\", "\\\\").replace("\"", "\\\"") + "\"" }
        assertEquals("window.otcSetupLang&&window.otcSetupLang(\"es\")", BLESetupLang.switchScript("es", quote))
        assertEquals("window.otcSetupLang&&window.otcSetupLang(\"a\\\"b\")", BLESetupLang.switchScript("a\"b", quote))
    }

    @Test fun onlyABooleanAnswerMeansThePageSwitches() {
        assertTrue(BLESetupLang.pageSwitches("true"))
        // false: nothing to do (already in it).
        assertTrue(BLESetupLang.pageSwitches("false"))
        // An older wizard: undefined and null both come back as "null".
        assertFalse(BLESetupLang.pageSwitches("null"))
        assertFalse(BLESetupLang.pageSwitches(null))
        assertFalse(BLESetupLang.pageSwitches("1"))
        assertFalse(BLESetupLang.pageSwitches("\"true\""))
    }

    @Test fun aReloadWaitsForTheInstall() {
        assertFalse(BLESetupLang.installStarted(alreadyInstalled = false, phase = null))
        assertFalse(BLESetupLang.installStarted(alreadyInstalled = false, phase = "idle"))
        assertTrue(BLESetupLang.installStarted(alreadyInstalled = false, phase = "installing"))
        assertTrue(BLESetupLang.installStarted(alreadyInstalled = false, phase = "failed"))
        assertTrue(BLESetupLang.installStarted(alreadyInstalled = true, phase = "idle"))
    }

    @Test fun thePagesLanguageIsItsAddresss() {
        assertEquals("fr", BLESetupLang.lang("https://device/?lang=fr"))
        assertEquals("pt", BLESetupLang.lang("https://device/?step=2&lang=pt#top"))
        assertNull(BLESetupLang.lang("https://device/"))
        assertNull(BLESetupLang.lang(null))
    }
}
