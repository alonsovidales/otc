// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import cloud.offthe.otc.ui.common.LoadProblem
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// What Files says while a folder whose listing failed is asked again.
class FilesListingTest {
    @Test fun whyStaysUpWhileTheSameFolderIsAskedAgain() {
        assertEquals(
            "Your device isn't reachable right now. The files will appear as soon as it answers.",
            listingErrorWhileAsking(LoadProblem.UNREACHABLE, "/Photos", "/Photos", online = true),
        )
        // Another folder: nothing yet.
        assertNull(listingErrorWhileAsking(LoadProblem.UNREACHABLE, "/Photos", "/Docs", online = true))
        assertNull(listingErrorWhileAsking(null, null, "/Photos", online = true))
    }

    @Test fun backOnlineTheOfflineLineGivesWay() {
        assertEquals(
            "This phone is offline. The files will appear once it's back online.",
            listingErrorWhileAsking(LoadProblem.OFFLINE, "/", "/", online = false),
        )
        assertEquals("The files will appear as soon as your device answers.", listingErrorWhileAsking(LoadProblem.OFFLINE, "/", "/", online = true))
    }
}
