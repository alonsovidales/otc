// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.RespEnvelope

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

    /**
     * The device has no room for what was sent (release 143: an upload
     * refused before it started, or one the disk filled under). The photo
     * sync stops on it instead of trying file after file.
     */
    const val STORAGE_FULL = "storage_full"

    fun isStorageFull(code: String?): Boolean = code == STORAGE_FULL

    /** The device is older than the request. Builds before v9 send only the message, "unknown payload". */
    fun isUnknownPayload(code: String?, message: String?): Boolean =
        code == "unknown_payload" || (code.isNullOrEmpty() && message.orEmpty().contains("unknown payload"))

    /**
     * Issue #207: the device can't reach its storage disks (missing, not
     * mounted, the mirror not assembled, its database unable to start on
     * them) and answers every request with this, GetPubKey first, until
     * they are back. Not a verdict on the password: nothing the phone holds
     * is touched for it (StorageWait). Devices before #207 never send it.
     */
    const val STORAGE_UNAVAILABLE = "storage_unavailable"

    /** Whether [r] is that answer: the envelope's error_code, or an Ack carrying it. */
    fun isStorageUnavailable(r: RespEnvelope): Boolean =
        r.errorCode == STORAGE_UNAVAILABLE ||
            (r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.code == STORAGE_UNAVAILABLE)
}
