// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.nio.file.Files

// The temporary copies live in folders of their own and go when done with;
// what a process ended before that left, and the copies older versions left
// at the cache's top level, go at the next start - the thumbnails, the
// full-size photos and the feed's snapshot stay.
class TempFilesTest {
    private val cache: File = Files.createTempDirectory("otc-cache").toFile()

    @After fun tearDown() { cache.deleteRecursively() }

    private fun touch(path: String) = File(cache, path).also { it.parentFile?.mkdirs(); it.writeText("x") }

    @Test fun aStartRemovesWhatTheLastProcessLeft() {
        touch("tmp/viewer/0b1e.mp4")
        touch("tmp/preview/ab/Report.pdf")
        // What older versions left at the top level.
        touch("4f1c2b3a-1111-2222-3333-444455556666.mp4")
        touch("Holiday.jpg")
        touch("save-4f1c2b3a")
        touch("otc-trim-4f1c.mp4")
        touch("otc-device.log")
        // What stays.
        touch("social_feed_cache.pb")
        touch("grid-thumbs/scope/0001")
        touch("full-photos/abc")

        val later = mutableListOf<() -> Unit>()
        TempFiles.sweepAtStart(cache) { later += it }
        // Moved aside at once: this start's copies go in a new folder.
        assertFalse(File(cache, "tmp").exists())
        val fresh = TempFiles.file(cache, TempFiles.Kind.VIEWER, "mp4").also { it.writeText("new") }
        later.forEach { it() }

        assertEquals(setOf("social_feed_cache.pb", "grid-thumbs", "full-photos", "tmp"), cache.list()!!.toSet())
        assertTrue(File(cache, "grid-thumbs/scope/0001").exists())
        assertTrue(fresh.exists())
    }

    @Test fun aNamedCopyKeepsItsNameAndGoesWithItsFolder() {
        val f = TempFiles.named(cache, TempFiles.Kind.PREVIEW, "Report.pdf").also { it.writeText("x") }
        assertEquals("Report.pdf", f.name)
        val folder = f.parentFile!!
        TempFiles.delete(f)
        assertFalse(f.exists())
        assertFalse(folder.exists())
        // The kind's own folder stays.
        assertTrue(TempFiles.dir(cache, TempFiles.Kind.PREVIEW).exists())

        val shared = TempFiles.file(cache, TempFiles.Kind.SHARE, "jpg").also { it.writeText("x") }
        TempFiles.clear(cache, TempFiles.Kind.SHARE)
        assertFalse(shared.exists())
    }
}
