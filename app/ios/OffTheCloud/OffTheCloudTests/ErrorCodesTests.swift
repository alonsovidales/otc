// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Codes before prose: a device since the localization release says it in
/// the code (its message may be in another language), an older one only in
/// English; a message that comes with a code is never read. Same cases as
/// Android's ErrorCodesTest.
struct ErrorCodesTests {
    @Test func duplicatedFileByCodeOrOlderDevicesMessage() {
        #expect(ErrorCodes.isDuplicatedFile(code: "duplicated_file", message: "error trying to link file: Duplicated file"))
        #expect(ErrorCodes.isDuplicatedFile(code: "duplicated_file", message: "No se pudo enlazar el archivo: Duplicated file"))
        #expect(ErrorCodes.isDuplicatedFile(code: "duplicated_file", message: "Ya hay otro archivo con ese nombre"))
        // Devices before the localization release.
        #expect(ErrorCodes.isDuplicatedFile(code: "", message: "error trying to link file: Duplicated file"))
        #expect(ErrorCodes.isDuplicatedFile(code: "", message: "error finishing the upload: Duplicated file"))
        #expect(!ErrorCodes.isDuplicatedFile(code: "", message: "error finishing the upload: upload incomplete: 1 of 2 bytes"))
        #expect(!ErrorCodes.isDuplicatedFile(code: "", message: "Duplicated file, or not"))
        #expect(!ErrorCodes.isDuplicatedFile(code: "", message: ""))
        // Another code: the message doesn't count.
        #expect(!ErrorCodes.isDuplicatedFile(code: "disk_full", message: "error finishing the upload: Duplicated file"))
    }

    @Test func unknownPayloadByCodeOrOlderDevicesMessage() {
        #expect(ErrorCodes.isUnknownPayload(code: "unknown_payload", message: "This device does not understand that request: its software is older than the app. Check for updates in Settings."))
        #expect(ErrorCodes.isUnknownPayload(code: "unknown_payload", message: "Este dispositivo no entiende esa petición"))
        // Builds before v9.
        #expect(ErrorCodes.isUnknownPayload(code: "", message: "unknown payload"))
        #expect(!ErrorCodes.isUnknownPayload(code: "", message: "not authenticated"))
        #expect(!ErrorCodes.isUnknownPayload(code: "", message: ""))
        #expect(!ErrorCodes.isUnknownPayload(code: "local_unavailable", message: "unknown payload"))
    }

    @Test func aReplyNeedsAnError() {
        var resp = Msg_RespEnvelope()
        resp.errorMessage = "error trying to link file: Duplicated file"
        #expect(!ErrorCodes.isDuplicatedFile(resp))
        resp.error = true
        #expect(ErrorCodes.isDuplicatedFile(resp))
        resp.errorCode = "duplicated_file"
        resp.errorMessage = "Ya hay otro archivo con ese nombre"
        #expect(ErrorCodes.isDuplicatedFile(resp))
        resp.errorCode = "busy"
        resp.errorMessage = "error trying to link file: Duplicated file"
        #expect(!ErrorCodes.isDuplicatedFile(resp))
    }

    /// uploadChunked's thrown refusals carry the code; other errors don't.
    @Test func aThrownRefusalKeepsItsCode() {
        let refused = NSError(domain: "ChunkedUpload", code: -1, userInfo: [
            NSLocalizedDescriptionKey: "Ya hay otro archivo con ese nombre",
            ErrorCodes.userInfoKey: "duplicated_file",
        ])
        #expect(ErrorCodes.code(of: refused) == "duplicated_file")
        #expect(ErrorCodes.isDuplicatedFile(refused))
        let older = NSError(domain: "ChunkedUpload", code: -1, userInfo: [
            NSLocalizedDescriptionKey: "error finishing the upload: Duplicated file",
        ])
        #expect(ErrorCodes.code(of: older) == "")
        #expect(ErrorCodes.isDuplicatedFile(older))
        let other = NSError(domain: "ws", code: -1, userInfo: [NSLocalizedDescriptionKey: "Not connected"])
        #expect(!ErrorCodes.isDuplicatedFile(other))
    }
}
