// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.gallery.isUnknownPayload
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// Codes before prose: a device since the localization release says it in
// the code (its message may be in another language), an older one only in
// English; a message that comes with a code is never read. Same cases as
// the iOS app's ErrorCodesTests.
class ErrorCodesTest {
    @Test fun duplicatedFileByCodeOrOlderDevicesMessage() {
        assertTrue(ErrorCodes.isDuplicatedFile("duplicated_file", "error trying to link file: Duplicated file"))
        assertTrue(ErrorCodes.isDuplicatedFile("duplicated_file", "No se pudo enlazar el archivo: Duplicated file"))
        assertTrue(ErrorCodes.isDuplicatedFile("duplicated_file", "Ya hay otro archivo con ese nombre"))
        // Devices before the localization release.
        assertTrue(ErrorCodes.isDuplicatedFile("", "error trying to link file: Duplicated file"))
        assertTrue(ErrorCodes.isDuplicatedFile(null, "error finishing the upload: Duplicated file"))
        assertFalse(ErrorCodes.isDuplicatedFile("", "error finishing the upload: upload incomplete: 1 of 2 bytes"))
        assertFalse(ErrorCodes.isDuplicatedFile("", "Duplicated file, or not"))
        assertFalse(ErrorCodes.isDuplicatedFile(null, null))
        // Another code: the message doesn't count.
        assertFalse(ErrorCodes.isDuplicatedFile("disk_full", "error finishing the upload: Duplicated file"))
    }

    @Test fun unknownPayloadByCodeOrOlderDevicesMessage() {
        assertTrue(ErrorCodes.isUnknownPayload("unknown_payload", "This device does not understand that request: its software is older than the app. Check for updates in Settings."))
        assertTrue(ErrorCodes.isUnknownPayload("unknown_payload", "Este dispositivo no entiende esa petición"))
        // Builds before v9.
        assertTrue(ErrorCodes.isUnknownPayload("", "unknown payload"))
        assertTrue(ErrorCodes.isUnknownPayload(null, "unknown payload"))
        assertFalse(ErrorCodes.isUnknownPayload("", "not authenticated"))
        assertFalse(ErrorCodes.isUnknownPayload(null, null))
        assertFalse(ErrorCodes.isUnknownPayload("local_unavailable", "unknown payload"))
    }

    @Test fun replyChecksNeedAnError() {
        fun resp(b: RespEnvelope.Builder.() -> Unit) = RespEnvelope.newBuilder().apply(b).build()
        assertTrue(resp { error = true; errorCode = "unknown_payload"; errorMessage = "Este dispositivo no entiende esa petición" }.isUnknownPayload())
        assertTrue(resp { error = true; errorMessage = "unknown payload" }.isUnknownPayload())
        assertFalse(resp { errorMessage = "unknown payload" }.isUnknownPayload())
        assertFalse(resp { error = true; errorCode = "busy"; errorMessage = "unknown payload" }.isUnknownPayload())
    }

    @Test fun requestErrorsCarryTheDevicesCode() {
        assertEquals("", OTCConnection.RequestError("Not connected").code)
        val e = OTCConnection.RequestError("error finishing the upload: Duplicated file", "duplicated_file")
        assertTrue(ErrorCodes.isDuplicatedFile(e.code, e.message))
        assertEquals("error finishing the upload: Duplicated file", e.message)
    }
}
