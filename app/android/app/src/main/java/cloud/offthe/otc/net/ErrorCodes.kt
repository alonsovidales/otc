// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.proto.RespEnvelope

// Codes before prose (docs/i18n.md): the app acts on a reply's error_code
// and reads its message only when it has no code. A device from before the
// code existed says it in English alone; a localized device writes the
// message in the owner's language, so a message that comes with a code is
// never matched. The iOS app's ErrorCodes.swift is the same.
object ErrorCodes {
    /**
     * A demo device (docs/demo-device.md) refused a change: every one is
     * refused there, on the envelope and in an Ack alike, its message
     * already in the request's language. The app hides what would change
     * something on one (OTCConnection.isDemo); this is for what is left.
     */
    fun isDemo(code: String?): Boolean = code == "demo"

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

/** What a demo device said as it refused this request ([ErrorCodes.isDemo]), as it came; null for any other answer. */
fun RespEnvelope.demoRefusal(): UiText? {
    val ack = if (payloadCase == RespEnvelope.PayloadCase.RESP_ACK) respAck else null
    val said = when {
        error && ErrorCodes.isDemo(errorCode) -> errorMessage.ifEmpty { ack?.errorMsg.orEmpty() }
        ack != null && !ack.ok && ErrorCodes.isDemo(ack.code) -> ack.errorMsg
        else -> return null
    }
    return UiText.Raw(said)
}
