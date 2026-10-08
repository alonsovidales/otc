// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File as PbFile
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.SelectionActionTask
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Issue #192 in Files: folders kept out of Images, as the spec and the
// web's FilesExplorer have them.
class OutOfImagesTest {
    private fun folder(path: String, out: Boolean = false, uploadOnly: Boolean = false) =
        PbFile.newBuilder().setPath(path).setMime("inode/directory").setOutOfImages(out).setUploadOnly(uploadOnly).build()
    private fun file(path: String, out: Boolean = false) =
        PbFile.newBuilder().setPath(path).setMime("image/jpeg").setOutOfImages(out).build()
    private fun state(path: String, listed: String?, supported: Boolean, folderOut: Boolean, rows: List<FileRow> = emptyList(), selected: Set<String> = emptySet()) =
        FilesExplorerViewModel.State(
            path = path, rows = rows, selected = selected, grid = false, listedPath = listed,
            outOfImagesSupported = supported, folderOutOfImages = folderOut,
        )

    @Test fun listingCarriesTheMarks() {
        val lof = ListOfFiles.newBuilder()
            .addFiles(folder("Private", out = true, uploadOnly = true)).addFiles(folder("Trips")).addFiles(file("a.jpg", out = true))
            .setOutOfImagesSupported(true).build()
        val rows = listingRows(lof, "/Photos/")
        assertEquals(listOf("..", "Private", "Trips", "a.jpg"), rows.map { it.name })
        assertEquals(listOf(false, true, false, true), rows.map { it.outOfImages })
        assertTrue(rows[1].uploadOnly && rows[1].isDir)
        // No way up from the root.
        assertEquals(listOf("Private", "Trips", "a.jpg"), listingRows(lof, "/").map { it.name })
    }

    @Test fun bannerOnlyOverTheFolderListedOnADeviceThatCan() {
        assertTrue(state("/Photos/Private/", "/Photos/Private/", supported = true, folderOut = true).outOfImagesBanner)
        // Another folder asked for, its listing not in yet (or failed).
        assertFalse(state("/Photos/", "/Photos/Private/", supported = true, folderOut = true).outOfImagesBanner)
        assertFalse(state("/Photos/", "/Photos/", supported = true, folderOut = false).outOfImagesBanner)
        // A device before release 108: nothing at all.
        assertFalse(state("/Photos/", "/Photos/", supported = false, folderOut = true).outOfImagesBanner)
    }

    @Test fun theSwitchActsOnOneFolderPicked() {
        val rows = listingRows(
            ListOfFiles.newBuilder().addFiles(folder("Private", out = true)).addFiles(folder("Trips")).addFiles(file("a.jpg")).build(), "/Photos/",
        )
        fun picked(vararg p: String) = state("/Photos/", "/Photos/", true, false, rows, p.toSet()).selectedFolder
        assertEquals("Private", picked("Private")?.name)
        assertTrue(picked("Private")!!.outOfImages)
        assertFalse(picked("Trips")!!.outOfImages)
        assertNull(picked("a.jpg"))
        assertNull(picked("Private", "Trips"))
        assertNull(picked(".."))
        assertNull(picked())
    }

    @Test fun theRequestFlipsTheFolder() {
        val keep = setOutOfImagesRequest("/Photos/Private", out = false)
        assertEquals("/Photos/Private", keep.path)
        assertTrue(keep.outOfImages)
        assertFalse(setOutOfImagesRequest("/Photos/Private", out = true).outOfImages)
    }

    @Test fun aRefusalIsShownAsTheDeviceWroteIt() {
        val byParent = "Trip is inside /Photos, which is kept out of Images - show /Photos in Images to show Trip"
        assertEquals(byParent, outOfImagesFailure(
            RespEnvelope.newBuilder().setError(true).setErrorCode("out_of_images_by_parent").setErrorMessage(byParent).build(),
        ))
        assertEquals("Could not update the folder", outOfImagesFailure(RespEnvelope.newBuilder().setError(true).build()))
        assertNull(outOfImagesFailure(RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(true)).build()))
        assertEquals("no", outOfImagesFailure(RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setOk(false).setErrorMsg("no")).build()))
        assertEquals("Could not update the folder", outOfImagesFailure(RespEnvelope.getDefaultInstance()))
    }

    @Test fun theFolderWhoseShowTheDeviceTakesIsTheOutermostKeptOut() {
        val kept = listOf("/Photos/Private/", "/Photos/Private/Trip/", "/Other/")
        assertEquals("/Photos/Private", outermostKeptOut(kept, "/Photos/Private/Trip/Day1/"))
        assertEquals("/Photos/Private", outermostKeptOut(kept, "/Photos/Private/Trip"))
        assertEquals("/Photos/Private", outermostKeptOut(kept, "/Photos/Private/"))
        // A name that only starts like a folder kept out isn't inside it.
        assertNull(outermostKeptOut(kept, "/Photos/PrivateNot/"))
        assertNull(outermostKeptOut(kept, "/Photos/"))
        // Entries without their slash (never sent) are left out, as is "/".
        assertNull(outermostKeptOut(listOf("/Photos/Private", "/"), "/Photos/Private/Trip/"))
        assertNull(outermostKeptOut(emptyList(), "/Photos/Private/"))
    }

    @Test fun insideAFolderKeptOutTheFoldersHaveNoSwitchAndTheBannerShowsTheOneAbove() {
        val rows = listingRows(
            ListOfFiles.newBuilder().addFiles(folder("Day1", out = true)).addFiles(file("a.jpg", out = true)).setOutOfImagesSupported(true).setFolderOutOfImages(true).build(),
            "/Photos/Private/Trip/",
        )
        val inside = state("/Photos/Private/Trip/", "/Photos/Private/Trip/", supported = true, folderOut = true, rows, setOf("Day1"))
        // The folder is picked (Share as gallery), but no switch for Images.
        assertEquals("Day1", inside.selectedFolder?.name)
        assertNull(inside.outOfImagesSwitch)
        assertEquals("/Photos/Private/Trip", inside.listedFolder)
        // Until ListOutOfImages answers: no button, and the folder listed is
        // what the marks name.
        assertTrue(inside.outOfImagesBanner)
        assertNull(inside.outOfImagesCover)
        assertFalse(inside.bannerByParent)
        assertEquals("/Photos/Private/Trip", inside.keptOutBy)
        // Kept out by a folder above: the banner names it and shows it.
        val byParent = inside.copy(outOfImagesCover = "/Photos/Private")
        assertTrue(byParent.bannerByParent)
        assertEquals("/Photos/Private", byParent.keptOutBy)
        // Kept out itself, nothing above: its own Show.
        val itself = inside.copy(outOfImagesCover = "/Photos/Private/Trip")
        assertFalse(itself.bannerByParent)
        assertEquals("/Photos/Private/Trip", itself.keptOutBy)
        // Not inside one: the folder's own switch.
        val outside = state("/Photos/", "/Photos/", supported = true, folderOut = false, listingRows(
            ListOfFiles.newBuilder().addFiles(folder("Private", out = true)).build(), "/Photos/"), setOf("Private"))
        assertEquals("Private", outside.outOfImagesSwitch?.name)
        // A device before release 108: none.
        assertNull(outside.copy(outOfImagesSupported = false).outOfImagesSwitch)
        assertEquals("/", state("/", "/", supported = true, folderOut = false).listedFolder)
    }

    @Test fun aChangeWaitsForAShareOrDownloadAndSaysSo() {
        assertEquals("Wait for the share link to be ready", outOfImagesWait(SelectionActionTask.SHARE))
        assertEquals("Wait for the download link to be ready", outOfImagesWait(SelectionActionTask.DOWNLOAD))
        assertEquals("Wait for the folder to be updated", outOfImagesWait(SelectionActionTask.OUT_OF_IMAGES))
    }

    @Test fun theSpecsWords() {
        assertEquals("Keep out of Images", OutOfImagesText.KEEP)
        assertEquals("Show in Images", OutOfImagesText.SHOW)
        assertEquals("Kept out of Images", OutOfImagesText.STATE)
        assertEquals("Keep “Trip” out of Images?", OutOfImagesText.keepTitle("Trip"))
        assertEquals("Show “Trip” in Images?", OutOfImagesText.showTitle("Trip"))
        assertEquals("Photos and videos here aren't tagged, searched for faces or shown in Images. Files still shows them.", OutOfImagesText.EXPLAIN)
        assertTrue(OutOfImagesText.BANNER.startsWith("Kept out of Images - "))
        // The web's words inside a folder kept out by one above it.
        assertEquals("Inside /Photos, which is kept out of Images", OutOfImagesText.byParent("/Photos"))
        assertEquals("Inside /Photos, which is kept out of Images - photos and videos here aren't tagged, searched for faces or shown in Images.",
            OutOfImagesText.bannerByParent("/Photos"))
        assertEquals("Show “Photos” in Images", OutOfImagesText.showNamed("Photos"))
        assertEquals("Trip kept out of Images", OutOfImagesText.keptSaid("Trip"))
        assertEquals("Trip shown in Images", OutOfImagesText.shownSaid("Trip"))
    }
}
