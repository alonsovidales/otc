// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.ui.gallery.DeleteRefusal
import cloud.offthe.otc.ui.gallery.deleteFailed
import cloud.offthe.otc.ui.gallery.deleteRefusalNotes
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// Files' multi-delete says what wasn't deleted as Images does (as iOS's
// filesSaysWhyADeleteFailed): it used to say nothing at all for a delete
// that got no answer or a refusal other than "changed", and a single
// "changed" read as the device's bare words.
class FilesDeleteTest {
    @Test fun filesSaysWhyADeleteFailed() {
        assertNull(deleteKeptToast(emptyList()))
        assertEquals(
            "Delete failed: this file has changed since it was shown",
            deleteKeptToast(listOf(DeleteRefusal("changed", "this file has changed since it was shown")))?.english(),
        )
        // No answer: the phone's words.
        assertEquals("Delete failed: No connection", deleteKeptToast(listOf(DeleteRefusal("", "No connection")))?.english())
    }

    /**
     * A delete that got no answer says why in the app's words, never the
     * network stack's English ("Failed to connect to /192.168.1.20:8080")
     * or a bare class name - Images' too.
     */
    @Test fun noAnswerIsSaidInTheAppsWords() {
        val noAnswer = "Delete failed: The device isn't answering. It may be off, or the address may be wrong."
        assertEquals(noAnswer, deleteKeptToast(listOf(deleteFailed(java.net.ConnectException("Failed to connect to /192.168.1.20:8080"), online = true)))?.english())
        assertEquals(noAnswer, deleteKeptToast(listOf(deleteFailed(java.io.EOFException(), online = true)))?.english())
        // Ours stay ours; the device's words stay its own.
        assertEquals(
            "Delete failed: Wrong password (said by the device)",
            deleteKeptToast(listOf(deleteFailed(OTCConnection.RequestError("Wrong password (said by the device)"), online = true)))?.english(),
        )
        assertEquals("Delete failed: " + OTCConnection.OFFLINE_MESSAGE.english(), deleteKeptToast(listOf(deleteFailed(java.io.IOException("x"), online = false)))?.english())
        // Two the same: said once.
        val twice = deleteRefusalNotes(listOf(deleteFailed(java.io.EOFException(), true), deleteFailed(java.net.ConnectException("y"), true)))
        assertEquals(listOf(noAnswer), twice.map { it.english() })
    }
}
