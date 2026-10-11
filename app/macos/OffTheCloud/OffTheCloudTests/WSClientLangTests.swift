// SPDX-License-Identifier: AGPL-3.0-or-later

import Testing
@testable import OffTheCloud

/// Every request carries the app's language as ReqEnvelope.lang
/// (docs/i18n.md) - also when its build replaces the whole envelope: lang
/// and the id are set after the build, in WSClient.envelope alone. Runs in
/// a scratch SwiftPM package holding WSClient.swift and what it needs
/// (messages.pb.swift, LocalRoute.swift, PwCrypto.swift, i18n/), like
/// SyncPathsTests.
struct WSClientLangTests {
    @Test func aBuildThatReplacesTheEnvelopeStillSendsTheLanguage() {
        let req = WSClient.envelope(id: 7, lang: "es") { req in
            var fresh = Req()
            fresh.payload = .reqGetStatus(Msg_GetStatus())
            req = fresh
        }
        #expect(req.lang == "es")
        #expect(req.id == 7)
        if case .reqGetStatus = req.payload {} else { Issue.record("the build's payload was lost") }
    }

    @Test func theLanguageIsTheOneTheAppShows() {
        let req = WSClient.envelope(id: 1) { $0.payload = .reqGetStatus(Msg_GetStatus()) }
        #expect(req.lang == L10n.shared.code)
        #expect(!req.lang.isEmpty)
    }
}
