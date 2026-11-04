// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.ui.gallery.DeleteRefusal
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
}
