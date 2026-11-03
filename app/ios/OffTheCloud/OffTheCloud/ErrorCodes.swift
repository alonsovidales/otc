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

    /// The device has no room for what was sent (release 143: an upload
    /// refused before it started, or one the disk filled under). The photo
    /// sync stops on it instead of trying file after file.
    static let storageFull = "storage_full"

    static func isStorageFull(code: String) -> Bool {
        code == storageFull
    }

    /// The device is older than the request. Builds before v9 send only the
    /// message, "unknown payload".
    static func isUnknownPayload(code: String, message: String) -> Bool {
        code == "unknown_payload" || (code.isEmpty && message.contains("unknown payload"))
    }

    static func isDuplicatedFile(_ resp: Msg_RespEnvelope) -> Bool {
        resp.error && isDuplicatedFile(code: resp.errorCode, message: resp.errorMessage)
    }

    /// Issue #207: the device can't reach its storage disks (missing, not
    /// mounted, the mirror not assembled, its database unable to start on
    /// them) and answers every request with this, GetPubKey first, until
    /// they are back. Not a verdict on the password: nothing the phone holds
    /// is touched for it (StorageWait). Devices before #207 never send it.
    static let storageUnavailable = "storage_unavailable"

    /// Whether `resp` is that answer: the envelope's error_code, or an Ack
    /// carrying it.
    static func isStorageUnavailable(_ resp: Msg_RespEnvelope) -> Bool {
        if resp.errorCode == storageUnavailable { return true }
        if case .respAck(let ack) = resp.payload { return ack.code == storageUnavailable }
        return false
    }

    static func isDuplicatedFile(_ error: Error) -> Bool {
        isDuplicatedFile(code: code(of: error), message: error.localizedDescription)
    }

    /// The device's error_code a thrown error carries ("" when none).
    static func code(of error: Error) -> String {
        (error as NSError).userInfo[userInfoKey] as? String ?? ""
    }
}
