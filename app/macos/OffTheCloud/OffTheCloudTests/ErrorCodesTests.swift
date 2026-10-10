// SPDX-License-Identifier: AGPL-3.0-or-later

import Testing
@testable import OffTheCloud

/// Codes before prose: a device says it in the code (its message may be in
/// another language), a build before v9 only in English; a message that
/// comes with a code is never read. Same cases as otc-sync's
/// TestUnknownPayload. Runs in a scratch SwiftPM package holding only
/// ErrorCodes.swift, like SyncPathsTests.
struct ErrorCodesTests {
    @Test func unknownPayloadByCodeOrOlderDevicesMessage() {
        #expect(ErrorCodes.isUnknownPayload(code: "unknown_payload", message: "This device does not understand that request: its software is older than the app. Check for updates in Settings."))
        #expect(ErrorCodes.isUnknownPayload(code: "unknown_payload", message: "Este dispositivo no entiende esa petición"))
        // Builds before v9.
        #expect(ErrorCodes.isUnknownPayload(code: "", message: "unknown payload"))
        #expect(!ErrorCodes.isUnknownPayload(code: "", message: "database is busy"))
        #expect(!ErrorCodes.isUnknownPayload(code: "", message: ""))
        #expect(!ErrorCodes.isUnknownPayload(code: "local_unavailable", message: "unknown payload"))
    }
}
