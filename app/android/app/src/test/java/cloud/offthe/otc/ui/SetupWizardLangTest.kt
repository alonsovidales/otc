// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import org.junit.Assert.assertEquals
import org.junit.Test

// The setup wizard speaks the app's language (docs/i18n.md, "Setup
// wizard"): over Bluetooth only the path travels, so every /api call
// carries ?lang= (the page is loaded with it). As SetupWizardLangTests.swift.
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
}
