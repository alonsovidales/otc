// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.androidNoAppForLink
import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Test

// A photo's place opens in the phone's maps app, else on OpenStreetMap in
// the browser: a phone without Google's apps often has no app for geo:,
// and startActivity threw then, taking the app down.
class ShareTest {
    @Test fun aPlaceOpensInTheMapsAppElseOnOpenStreetMap() {
        assertEquals(
            listOf(
                "geo:43.36,-5.85?q=43.36,-5.85",
                "https://www.openstreetmap.org/?mlat=43.36&mlon=-5.85#map=16/43.36/-5.85",
            ),
            Share.mapLinks(43.36, -5.85),
        )
    }

    @Test fun noAppIsSaid() {
        assertEquals("No app on this phone can open this link.", S.androidNoAppForLink().english())
    }
}
