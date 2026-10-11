// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// The setup wizard speaks the app's language (docs/i18n.md, "Setup
/// wizard"): over Bluetooth only the path travels, so the page and every
/// /api call carry ?lang=. Android's SetupWizardLangTest is the same.
struct SetupWizardLangTests {
    @Test func thePageIsLoadedWithTheLanguage() {
        #expect(BLESetupLang.pageURL(code: "es").absoluteString == "otc-setup://device/?lang=es")
    }

    @Test func everyAPICallCarriesIt() {
        #expect(BLESetupLang.adding(to: "/api/state", code: "es") == "/api/state?lang=es")
        #expect(BLESetupLang.adding(to: "/api/scan?band=5", code: "fr") == "/api/scan?band=5&lang=fr")
        #expect(BLESetupLang.adding(to: "/api", code: "de") == "/api?lang=de")
    }

    @Test func aLanguageAlreadyThereAndOtherPathsAreLeftAlone() {
        #expect(BLESetupLang.adding(to: "/api/state?lang=fr", code: "es") == "/api/state?lang=fr")
        #expect(BLESetupLang.adding(to: "/", code: "es") == "/")
        #expect(BLESetupLang.adding(to: "/__otc/release", code: "es") == "/__otc/release")
        #expect(BLESetupLang.adding(to: "/apiary", code: "es") == "/apiary")
    }
}
