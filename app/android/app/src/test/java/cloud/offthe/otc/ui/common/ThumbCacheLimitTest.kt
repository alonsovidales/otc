// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File
import java.util.Locale

// Settings > Thumbnail cache: the size picked is kept on the phone, and the
// sizes read as the iOS app shows them.
class ThumbCacheLimitTest {
    @get:Rule val tmp = TemporaryFolder()

    @Test fun theSizePickedIsKeptAndReadBack() {
        val f = File(tmp.root, "thumbnail-cache-limit")
        // Nothing picked yet: 1 GB.
        assertEquals(1_000_000_000L, ThumbCacheLimit(f).get())
        assertTrue(ThumbCacheLimit(f).set(5_000_000_000L))
        // Another instance (the app started again) reads it.
        assertEquals(5_000_000_000L, ThumbCacheLimit(f).get())
        assertTrue(ThumbCacheLimit(f).set(250_000_000L))
        assertEquals(250_000_000L, ThumbCacheLimit(f).get())
        assertFalse(File(tmp.root, "thumbnail-cache-limit.tmp").exists())
    }

    @Test fun anythingElseReadsAsTheDefault() {
        val f = File(tmp.root, "limit")
        f.writeText("garbage")
        assertEquals(THUMB_CACHE_DEFAULT, ThumbCacheLimit(f).get())
        f.writeText("123")
        assertEquals(THUMB_CACHE_DEFAULT, ThumbCacheLimit(f).get())
        f.writeText("")
        assertEquals(THUMB_CACHE_DEFAULT, ThumbCacheLimit(f).get())
        try {
            ThumbCacheLimit(f).set(123)
            throw AssertionError("a size not offered was taken")
        } catch (_: IllegalArgumentException) {}
    }

    @Test fun theChoicesAndHowSizesRead() {
        val us = Locale.US
        assertEquals(listOf("250 MB", "500 MB", "1 GB", "2 GB", "5 GB"), THUMB_CACHE_CHOICES.map { formatCacheBytes(it, us) })
        assertEquals(1_000_000_000L, THUMB_CACHE_DEFAULT)
        // An empty cache says so in words, as the iOS app.
        assertEquals("0 bytes", formatCacheBytes(0, us))
        assertEquals("1 byte", formatCacheBytes(1, us))
        assertEquals("999 bytes", formatCacheBytes(999, us))
        assertEquals("38 KB", formatCacheBytes(38_000, us))
        assertEquals("1 MB", formatCacheBytes(999_600, us))
        assertEquals("4.5 MB", formatCacheBytes(4_500_000, us))
        assertEquals("312 MB", formatCacheBytes(312_400_000, us))
        assertEquals("999 MB", formatCacheBytes(999_400_000, us))
        assertEquals("1 GB", formatCacheBytes(999_600_000, us))
        assertEquals("1.5 GB", formatCacheBytes(1_500_000_000, us))
        assertEquals("4.9 GB", formatCacheBytes(4_940_000_000, us))
        assertEquals("Using 0 bytes of 1 GB", "Using ${formatCacheBytes(0, us)} of ${formatCacheBytes(THUMB_CACHE_DEFAULT, us)}")
        // The phone's decimal separator.
        assertEquals("1,5 GB", formatCacheBytes(1_500_000_000, Locale.GERMANY))
        assertEquals("4,5 MB", formatCacheBytes(4_500_000, Locale("es", "ES")))
        assertEquals("250 MB", formatCacheBytes(250_000_000, Locale.GERMANY))
    }
}
