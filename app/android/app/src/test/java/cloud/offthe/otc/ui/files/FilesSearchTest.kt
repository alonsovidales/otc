// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import cloud.offthe.otc.proto.File as PbFile
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.ui.gallery.ABOVE_ROWS
import cloud.offthe.otc.ui.gallery.FoundFiles
import cloud.offthe.otc.ui.gallery.SearchOption
import cloud.offthe.otc.ui.gallery.TopSearchViewModel
import cloud.offthe.otc.ui.gallery.fileSearchOptions
import cloud.offthe.otc.ui.gallery.searchKeyChoice
import cloud.offthe.otc.ui.gallery.searchOptions
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Files' own search (narrow layout): files and folders only, the Search
// key's choice, an older device, and the hand-over between Files' field
// and the wide top bar's when the phone turns - the top bar (the Images
// field's search) getting its own text back.
class FilesSearchTest {
    private fun file(path: String, mime: String) = PbFile.newBuilder().setPath(path).setMime(mime).build()
    private val found = FoundFiles("tax", "tax", listOf(file("/Docs/Taxes/", "inode/directory"), file("/Docs/tax 2024.pdf", "application/pdf")))

    @After fun reset() = FilesNav.reset()

    @Test fun filesOnlyHasNoThingsOrPeopleAndDocsComeFirst() {
        // What Images offers for the same words: a tag and a person first.
        val tags = listOf("taxi").map { it to FilesNav.fold(it) }
        val named = listOf(Person.newBuilder().setId("p").setName("Taxman").setFaceCount(3).build()).map { it to FilesNav.fold(it.name) }
        val images = searchOptions("tax", tags, emptyList(), named, found, noFileSearch = false)
        assertEquals("t:taxi", images.best)
        assertTrue(images.options.any { it is SearchOption.Tag } && images.options.any { it is SearchOption.PersonHit })

        val o = fileSearchOptions("tax", found, noFileSearch = false)
        assertTrue(o.options.none { it is SearchOption.Tag || it is SearchOption.PersonHit })
        assertEquals(listOf("docs", "f:/Docs/Taxes/", "f:/Docs/tax 2024.pdf"), o.options.map { it.key })
        assertEquals("docs", o.best)
        // Nothing typed (the field trims it): no panel.
        assertTrue(fileSearchOptions("", found, noFileSearch = false).options.isEmpty())
    }

    @Test fun searchKeyListsEveryMatchUnlessARowWasPicked() {
        val o = fileSearchOptions("tax", found, noFileSearch = false)
        // The Search key: the documents searched for the words - the whole list in Files.
        assertEquals(SearchOption.Docs("tax"), searchKeyChoice("tax", o, null, noFileSearch = false))
        // Before the device answers, the same.
        assertEquals(SearchOption.Docs("tax"), searchKeyChoice("tax", fileSearchOptions("tax", null, false), null, noFileSearch = false))
        // A hardware keyboard's arrows moved to a folder: Enter opens it.
        val folder = searchKeyChoice("tax", o, "f:/Docs/Taxes/", noFileSearch = false)
        assertTrue(folder is SearchOption.FileHit && folder.file.path == "/Docs/Taxes/")
        // Above the first row: the words as typed.
        assertEquals(SearchOption.Docs("tax"), searchKeyChoice("tax", o, ABOVE_ROWS, noFileSearch = false))
        // Nothing typed: nothing to take (the field just lets go).
        assertNull(searchKeyChoice("", o, null, noFileSearch = false))
    }

    @Test fun anOlderDeviceHasNoFilesSearch() {
        val o = fileSearchOptions("tax", null, noFileSearch = true)
        assertTrue(o.options.isEmpty())
        assertNull(o.best)
        assertNull(searchKeyChoice("tax", o, null, noFileSearch = true))
        // ... and no field at all, upright or not.
        assertFalse(filesSearchFieldShown(411f, noFileSearch = true))
    }

    @Test fun theFieldShowsInTheNarrowLayoutOnly() {
        assertTrue(filesSearchFieldShown(411f, noFileSearch = false))
        assertTrue(filesSearchFieldShown(599.5f, noFileSearch = false))
        // MainView's wide layout (600dp and up): the top bar's search covers files.
        assertFalse(filesSearchFieldShown(600f, noFileSearch = false))
        assertFalse(filesSearchFieldShown(915f, noFileSearch = false))
        assertFalse(filesSearchFieldShown(1280f, noFileSearch = false))
    }

    @Test fun turningThePhoneHandsTheSearchInUseOver() {
        // Upright to sideways while typing in Files: to the top bar.
        assertEquals(SearchHandOver.TO_TOP_BAR, searchHandOver(narrow = false, filesOpen = true, filesText = "tax", topOpen = false, topText = "dog"))
        // Sideways to upright while typing in the top bar (in Files): to Files' field.
        assertEquals(SearchHandOver.TO_FILES, searchHandOver(narrow = true, filesOpen = false, filesText = "", topOpen = true, topText = "tax"))
        // Not in use, or nothing typed: each field keeps its own text.
        assertEquals(SearchHandOver.NONE, searchHandOver(narrow = false, filesOpen = false, filesText = "tax", topOpen = false, topText = ""))
        assertEquals(SearchHandOver.NONE, searchHandOver(narrow = false, filesOpen = true, filesText = "  ", topOpen = false, topText = ""))
        assertEquals(SearchHandOver.NONE, searchHandOver(narrow = true, filesOpen = false, filesText = "", topOpen = false, topText = "tax"))
        assertEquals(SearchHandOver.NONE, searchHandOver(narrow = true, filesOpen = false, filesText = "", topOpen = true, topText = ""))
        // The field of the layout shown keeps its search.
        assertEquals(SearchHandOver.NONE, searchHandOver(narrow = true, filesOpen = true, filesText = "tax", topOpen = false, topText = ""))
        assertEquals(SearchHandOver.NONE, searchHandOver(narrow = false, filesOpen = false, filesText = "", topOpen = true, topText = "tax"))
    }

    @Test fun aHandOverMovesTheTextAndTheUse() {
        // No device request from here: a device "without SearchFiles".
        FilesNav.deviceCantSearchFiles()
        val files = TopSearchViewModel()
        val top = TopSearchViewModel()
        files.type("tax")
        assertTrue(files.open)
        top.takeOver(files.handOff())
        assertEquals("", files.query)
        assertFalse(files.open)
        assertEquals("tax", top.query)
        assertTrue(top.open)
        // Its field takes the focus.
        assertEquals(1, top.focusAsks)
        assertEquals(0, files.focusAsks)
    }

    // An Images field holding "dog", not in use, and Files' field in use with "tax".
    private fun imagesDogFilesTax(): Triple<FilesSearchHandOver, TopSearchViewModel, TopSearchViewModel> {
        // No device request from here: a device "without SearchFiles".
        FilesNav.deviceCantSearchFiles()
        val top = TopSearchViewModel().apply { type("dog"); close() }
        val files = TopSearchViewModel().apply { type("tax") }
        return Triple(FilesSearchHandOver(), files, top)
    }

    @Test fun turningThePhoneThereAndBackGivesTheImagesFieldItsTextBack() {
        val (h, files, top) = imagesDogFilesTax()
        // Sideways: the top bar takes Files' search, in use, and sets "dog" aside.
        h.layoutChanged(narrow = false, noFileSearch = false, files = files, top = top)
        assertEquals("tax", top.query)
        assertTrue(top.open)
        assertEquals(1, top.focusAsks)
        assertEquals("", files.query)
        assertFalse(files.open)
        assertEquals("dog", h.topBarOwn)
        // Upright again: Files' field has its search back, in use; the Images field "dog", not in use.
        h.layoutChanged(narrow = true, noFileSearch = false, files = files, top = top)
        assertEquals("tax", files.query)
        assertTrue(files.open)
        assertEquals(1, files.focusAsks)
        assertEquals("dog", top.query)
        assertFalse(top.open)
        assertEquals(1, top.focusAsks)
        assertNull(h.topBarOwn)
    }

    @Test fun whatWasTypedOnInTheTopBarGoesBackToFiles() {
        val (h, files, top) = imagesDogFilesTax()
        h.layoutChanged(narrow = false, noFileSearch = false, files = files, top = top)
        top.type("taxes")
        // Let go of (the back arrow): it goes back as text only, without the focus.
        top.close()
        h.layoutChanged(narrow = true, noFileSearch = false, files = files, top = top)
        assertEquals("taxes", files.query)
        assertFalse(files.open)
        assertEquals(0, files.focusAsks)
        assertEquals("dog", top.query)
        assertFalse(top.open)
    }

    @Test fun leavingFilesSidewaysGivesBothTheirText() {
        val (h, files, top) = imagesDogFilesTax()
        h.layoutChanged(narrow = false, noFileSearch = false, files = files, top = top)
        // Images picked in the rail (MainView's go ends the top bar's search first).
        top.close()
        h.filesLeft(files, top)
        // The Images field - the top bar now, the header once upright - holds "dog" again ...
        assertEquals("dog", top.query)
        assertFalse(top.open)
        // ... and Files' field "tax", not in use, for when it shows again.
        assertEquals("tax", files.query)
        assertFalse(files.open)
        assertNull(h.topBarOwn)
        // Back in Files upright: nothing is handed over, each keeps its text.
        h.layoutChanged(narrow = true, noFileSearch = false, files = files, top = top)
        assertEquals("tax", files.query)
        assertEquals("dog", top.query)
    }

    @Test fun anEmptiedTopBar() {
        // Cleared (the x) or a file picked, then upright: the Images field has "dog" back, Files' field nothing.
        val (h, files, top) = imagesDogFilesTax()
        h.layoutChanged(narrow = false, noFileSearch = false, files = files, top = top)
        top.clearQuery()
        h.layoutChanged(narrow = true, noFileSearch = false, files = files, top = top)
        assertEquals("dog", top.query)
        assertFalse(top.open)
        assertEquals("", files.query)
        assertFalse(files.open)

        // A tag picked from it (the text goes, Images shows): leaving Files sets nothing back.
        val (h2, files2, top2) = imagesDogFilesTax()
        h2.layoutChanged(narrow = false, noFileSearch = false, files = files2, top = top2)
        top2.clearQuery()
        top2.close()
        h2.filesLeft(files2, top2)
        assertEquals("", top2.query)
        assertEquals("", files2.query)
        assertNull(h2.topBarOwn)
    }

    @Test fun withoutAHandOverNothingIsSetBack() {
        // Not in use going sideways: each field keeps its own text.
        val (h, files, top) = imagesDogFilesTax()
        files.close()
        h.layoutChanged(narrow = false, noFileSearch = false, files = files, top = top)
        assertEquals("tax", files.query)
        assertEquals("dog", top.query)
        assertNull(h.topBarOwn)
        h.filesLeft(files, top)
        assertEquals("tax", files.query)
        assertEquals("dog", top.query)

        // Sideways from the start, typing in the top bar in Files, then upright: Files' field takes it.
        val h2 = FilesSearchHandOver()
        val files2 = TopSearchViewModel()
        val top2 = TopSearchViewModel().apply { type("tax") }
        h2.layoutChanged(narrow = false, noFileSearch = false, files = files2, top = top2)
        h2.layoutChanged(narrow = true, noFileSearch = false, files = files2, top = top2)
        assertEquals("tax", files2.query)
        assertTrue(files2.open)
        assertEquals("", top2.query)
        assertFalse(top2.open)
    }

    @Test fun anOlderDeviceLeavesTheTopBarAsItIs() {
        val (h, files, top) = imagesDogFilesTax()
        h.layoutChanged(narrow = false, noFileSearch = false, files = files, top = top)
        // The device answered unknown_payload: Files' field is gone for good;
        // the top bar, the one search left, keeps what is being typed.
        h.layoutChanged(narrow = false, noFileSearch = true, files = files, top = top)
        assertEquals("tax", top.query)
        assertTrue(top.open)
        assertNull(h.topBarOwn)
        assertEquals("", files.query)
    }
}
