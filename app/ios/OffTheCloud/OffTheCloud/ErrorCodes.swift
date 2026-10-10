// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  ErrorCodes.swift
//  OffTheCloud
//
//  Codes before prose (docs/i18n.md): the app acts on a reply's error_code
//  and reads its message only when it has no code. A device from before the
//  code existed says it in English alone; a localized device writes the
//  message in the owner's language, so a message that comes with a code is
//  never matched. Android's net/ErrorCodes.kt is the same.

import Foundation

enum ErrorCodes {
    /// The NSError userInfo key under which a thrown device refusal carries
    /// its error_code (uploadChunked's errors), so the code survives the
    /// throw and is checked before the message.
    static let userInfoKey = "OTCErrorCode"

    /// The path holds different content (LinkFile, FinishUpload,
    /// UploadFile; the same content answers with the existing row). Devices
    /// before the localization release send only the message,
    /// "<what>: Duplicated file".
    static func isDuplicatedFile(code: String, message: String) -> Bool {
        code == "duplicated_file" || (code.isEmpty && message.hasSuffix("Duplicated file"))
    }

    /// The device is older than the request. Builds before v9 send only the
    /// message, "unknown payload".
    static func isUnknownPayload(code: String, message: String) -> Bool {
        code == "unknown_payload" || (code.isEmpty && message.contains("unknown payload"))
    }

    static func isDuplicatedFile(_ resp: Msg_RespEnvelope) -> Bool {
        resp.error && isDuplicatedFile(code: resp.errorCode, message: resp.errorMessage)
    }

    static func isDuplicatedFile(_ error: Error) -> Bool {
        isDuplicatedFile(code: code(of: error), message: error.localizedDescription)
    }

    /// The device's error_code a thrown error carries ("" when none).
    static func code(of error: Error) -> String {
        (error as NSError).userInfo[userInfoKey] as? String ?? ""
    }
}
