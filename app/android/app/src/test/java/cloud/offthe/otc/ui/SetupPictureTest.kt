// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** The setup page's picture (SetupPicture): which pictures go as a JPEG copy, and at what size. */
class SetupPictureTest {
    @Test fun aHeifGoesAsAJpegByItsType() {
        for (t in listOf("image/heif", "image/heic", "image/HEIC", "image/heif-sequence", "image/heic-sequence", "image/heic; x=1"))
            assertTrue(t, SetupPicture.needsJpeg(t, "photo.jpg"))
    }

    @Test fun aPictureThePageOpensGoesAsItIs() {
        for (t in listOf("image/jpeg", "image/png", "image/webp", "image/avif", "image/gif"))
            assertFalse(t, SetupPicture.needsJpeg(t, "x.heic"))
    }

    @Test fun withNoUsefulTypeItsNameDecides() {
        assertTrue(SetupPicture.needsJpeg(null, "6819ff47-fc0_all_880.heif"))
        assertTrue(SetupPicture.needsJpeg("application/octet-stream", "IMG_0001.HEIC"))
        assertTrue(SetupPicture.needsJpeg(null, "a.hif"))
        assertFalse(SetupPicture.needsJpeg(null, "a.jpg"))
        assertFalse(SetupPicture.needsJpeg(null, null))
        assertFalse(SetupPicture.needsJpeg("application/octet-stream", "heic"))
    }

    @Test fun theCopyKeepsThePicturesName() {
        assertEquals("6819ff47-fc0_all_880.jpg", SetupPicture.jpegName("6819ff47-fc0_all_880.heif"))
        assertEquals("IMG.1.jpg", SetupPicture.jpegName("IMG.1.HEIC"))
        assertEquals("photo.jpg", SetupPicture.jpegName(null))
        assertEquals("photo.jpg", SetupPicture.jpegName(".heic"))
    }

    @Test fun aLargePictureIsDecodedWithinTheCapKeepingItsShape() {
        assertEquals(1200 to 800, SetupPicture.decodeSize(1200, 800))
        assertEquals(2048 to 2048, SetupPicture.decodeSize(2048, 2048))
        assertEquals(2048 to 1536, SetupPicture.decodeSize(4032, 3024))
        assertEquals(1536 to 2048, SetupPicture.decodeSize(3024, 4032))
        assertEquals(2048 to 1, SetupPicture.decodeSize(100_000, 10))
    }
}
