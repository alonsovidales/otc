// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appFilesUsageNeedsUpdate
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.FolderUsage
import cloud.offthe.otc.proto.FolderUsageEntry
import cloud.offthe.otc.proto.RespEnvelope
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Issue #195: Files' Storage view - which answer is the folder's, its rows
// joined with the listing, the shares as the rows say them, and why there
// are none. Issue #201: a row's delete carries the content it showed.
class FilesUsageTest {
    private fun entry(path: String, dir: Boolean, files: Long, count: Int, versions: Long = 0) =
        FolderUsageEntry.newBuilder().setPath(path).setIsDir(dir).setFilesSize(files).setFileCount(count).setVersionsSize(versions).build()

    private fun usage(path: String, vararg entries: FolderUsageEntry): FolderUsage = FolderUsage.newBuilder().setPath(path).addAllEntries(entries.toList())
        .setFilesSize(entries.sumOf { it.filesSize }).setFileCount(entries.sumOf { it.fileCount }).setVersionsSize(entries.sumOf { it.versionsSize }).build()

    private fun row(path: String, dir: Boolean, uploadOnly: Boolean = false, hash: String = "") =
        FileRow(path, path.substringAfterLast('/'), dir, 0, File.newBuilder().setPath(path).setMime(if (dir) "inode/directory" else "image/jpeg").setHash(hash).build(), uploadOnly = uploadOnly)

    @Test fun theViewIsKeptByName() {
        assertEquals(FilesView.STORAGE, FilesView.parse("storage"))
        assertEquals(FilesView.GRID, FilesView.parse("grid"))
        assertEquals(FilesView.LIST, FilesView.parse("list"))
        // Nothing kept yet, or a value from elsewhere: the list.
        assertEquals(FilesView.LIST, FilesView.parse(null))
        assertEquals(FilesView.LIST, FilesView.parse("tiles"))
        assertEquals(listOf("list", "grid", "storage"), FilesView.entries.map { it.stored })
    }

    @Test fun onlyTheAnswerForTheFolderShownLands() {
        assertTrue(usageFits("/", "/"))
        assertTrue(usageFits("/Music/", "/Music/"))
        assertTrue(usageFits("/Music/", "/Music"))
        // Asked for another folder (opened since), or a folder that only starts the same.
        assertFalse(usageFits("/Music/", "/Photos/"))
        assertFalse(usageFits("/Music/Live/", "/Music/"))
        assertFalse(usageFits("/Music", "/Music/"))
    }

    @Test fun rowsKeepTheDevicesOrderAndJoinTheListingByPathAndKind() {
        // Largest first, a folder before a file of the same name (the device's order).
        val u = usage(
            "/Music/",
            entry("/Music/Live", true, 400, 2),
            entry("/Music/Live", false, 400, 1),
            entry("/Music/Drafts", true, 0, 0, versions = 100),
            entry("/Music/a.mp3", false, 100, 1),
        )
        val listing = listOf(row("..", true), row("/Music/a.mp3", false), row("/Music/Live", false), row("/Music/Live", true, uploadOnly = true))
        val rows = usageRows(u, listing, "/Music/")
        assertEquals(listOf("/Music/Live" to true, "/Music/Live" to false, "/Music/Drafts" to true, "/Music/a.mp3" to false), rows.map { it.path to it.isDir })
        assertEquals(listOf("Live", "Live", "Drafts", "a.mp3"), rows.map { it.name })
        // Each joined with its own kind: the folder's lock, not the file's.
        assertTrue(rows[0].listed!!.isDir && rows[0].listed!!.uploadOnly)
        assertFalse(rows[1].listed!!.isDir)
        // Only older versions: ListFiles doesn't list it.
        assertNull(rows[2].listed)
        // Sizes with their older versions, shares of the whole (1000 bytes).
        assertEquals(listOf(400L, 400L, 100L, 100L), rows.map { it.size })
        assertEquals(listOf(0.4, 0.4, 0.1, 0.1), rows.map { it.share })
        assertEquals(1000L, usageTotal(u))
    }

    @Test fun aListingOfLeafNamesJoinsToo() {
        val u = usage("/", entry("/Docs", true, 10, 1))
        val rows = usageRows(u, listOf(row("Docs", true)), "/")
        assertEquals("Docs", rows.single().listed?.path)
    }

    @Test fun sharesAreRoundedWholePercentagesAndUnder1PercentIsSaidSo() {
        assertEquals("45%", usageShareText(0.45).english())
        assertEquals("46%", usageShareText(0.455).english())
        assertEquals("1%", usageShareText(0.0101).english())
        assertEquals("under 1%", usageShareText(0.004).english())
        assertEquals("100%", usageShareText(0.996).english())
        assertEquals("100%", usageShareText(1.0).english())
        assertEquals("0%", usageShareText(0.0).english())
        // A folder that takes nothing: no bar, and 0%.
        assertNull(usageShare(0, 0))
        assertEquals("0%", usageShareText(null).english())
        assertEquals(0.25, usageShare(25, 100)!!, 1e-9)
    }

    @Test fun aFolderThatTakesNothingShowsNoShareButTalkBackHearsZero() {
        // An empty answer: no rows (the view shows the summary, the empty text and the note).
        val empty = usage("/Empty/")
        assertTrue(usageRows(empty, emptyList(), "/Empty/").isEmpty())
        assertEquals(0L, usageTotal(empty))
        // Entries of nothing in a folder of nothing: no share, only the size on screen.
        val zero = usageRows(usage("/Z/", entry("/Z/a.txt", false, 0, 1)), emptyList(), "/Z/").single()
        assertNull(zero.share)
        assertEquals("0 B", usageRowText(zero, "0 B", usageShareText(zero.share).english()).english())
        // TalkBack's label still says 0%.
        assertEquals("0%", usageShareText(zero.share).english())
        // A row of nothing in a folder that takes some space: its 0%, as the web's.
        val some = usageRows(usage("/S/", entry("/S/big", false, 100, 1), entry("/S/none", false, 0, 1)), emptyList(), "/S/")
        assertEquals("0 B · 0%", usageRowText(some[1], "0 B", usageShareText(some[1].share).english()).english())
        assertEquals("100 B · 100%", usageRowText(some[0], "100 B", usageShareText(some[0].share).english()).english())
    }

    @Test fun talkBackHearsTheListingsMarks() {
        val u = usage(
            "/",
            entry("/Backups", true, 400, 2),
            entry("/Projects", true, 300, 2),
            entry("/Both", true, 200, 2),
            entry("/Backups.txt", false, 50, 1),
            entry("/Drafts", true, 0, 0, versions = 10),
        )
        val listing = listOf(
            row("/Backups", true, uploadOnly = true),
            row("/Projects", true).copy(outOfImages = true),
            row("/Both", true, uploadOnly = true).copy(outOfImages = true),
            row("/Backups.txt", false, uploadOnly = true),
        )
        val rows = usageRows(u, listing, "/")
        assertEquals(
            listOf(listOf("Upload only"), listOf("Kept out of Images"), listOf("Kept out of Images", "Upload only"), listOf("In an upload-only folder"), emptyList()),
            rows.map { r -> usageMarks(r).map { it.english() } },
        )
    }

    @Test fun whyThereAreNoRows() {
        val old = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
        assertEquals(UsageState.NeedsUpdate, usageFailure(old))
        assertEquals("Your device needs an update to show how much space folders take.", S.appFilesUsageNeedsUpdate().english())
        // No answer in time, no connection, the bridge's "device unreachable".
        assertEquals("Couldn't find out how much space this folder takes.", (usageFailure(null) as UsageState.Failed).text.english())
        val unreachable = RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setCode("device_unreachable")).build()
        assertEquals("Couldn't find out how much space this folder takes.", (usageFailure(unreachable) as UsageState.Failed).text.english())
        // The device's own words, as they came.
        val said = RespEnvelope.newBuilder().setError(true).setErrorMessage("no such folder").build()
        assertEquals("no such folder", (usageFailure(said) as UsageState.Failed).text.english())
    }

    @Test fun aFilesDeleteCarriesTheContentTheRowShowed() {
        val withHash = filesDelete("/Photos/a.jpg", row("/Photos/a.jpg", false, hash = "abc"))
        assertEquals("/Photos/a.jpg", withHash.path)
        assertEquals("abc", withHash.hash)
        assertFalse(withHash.hasEveryCopy())
        // A file whose content is missing (no hash), a folder, a pick not listed: the path alone.
        assertFalse(filesDelete("/Photos/b.jpg", row("/Photos/b.jpg", false)).hasHash())
        assertFalse(filesDelete("/Photos", row("/Photos", true, hash = "x")).hasHash())
        assertFalse(filesDelete("/Photos/c.jpg", null).hasHash())
    }
}
