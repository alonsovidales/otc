// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

// Codes before prose (docs/i18n.md): the app acts on a reply's error_code
// and reads its message only when it has no code. A device from before the
// code existed says it in English alone; a localized device writes the
// message in the owner's language, so a message that comes with a code is
// never matched. The iOS app's ErrorCodes.swift is the same.
object ErrorCodes {
    /**
     * The path holds different content (LinkFile, FinishUpload, UploadFile;
     * the same content answers with the existing row). Devices before the
     * localization release send only the message, "<what>: Duplicated file".
     */
    fun isDuplicatedFile(code: String?, message: String?): Boolean =
        code == "duplicated_file" || (code.isNullOrEmpty() && message.orEmpty().endsWith("Duplicated file"))

    /** The device is older than the request. Builds before v9 send only the message, "unknown payload". */
    fun isUnknownPayload(code: String?, message: String?): Boolean =
        code == "unknown_payload" || (code.isNullOrEmpty() && message.orEmpty().contains("unknown payload"))
}
