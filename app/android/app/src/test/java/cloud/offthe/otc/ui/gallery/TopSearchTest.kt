// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.proto.File as PbFile
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.ui.files.FilesNav
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// The search's matching, as the web's TopSearch/filesNav does it.
class TopSearchTest {
    private fun tags(vararg t: String) = t.map { it to FilesNav.fold(it) }
    private fun people(vararg p: Pair<String, Int>) = p.mapIndexed { i, (name, n) ->
        Person.newBuilder().setId("p$i").setName(name).setFaceCount(n).build().let { it to FilesNav.fold(name) }
    }
    private fun file(path: String, mime: String) = PbFile.newBuilder().setPath(path).setMime(mime).build()

    @Test fun foldIgnoresCaseAndAccents() {
        assertEquals("jose", FilesNav.fold("José").text)
        assertEquals("οδοσ", FilesNav.fold("ΟΔΟΣ").text)
        assertEquals(FilesNav.fold("Οδός").text, FilesNav.fold("ΟΔΟΣ").text)
    }

    @Test fun matchRanksStartThenWordThenInside() {
        val f = FilesNav.fold("Beach at sunset")
        assertEquals(0, FilesNav.matchIn("Beach at sunset", f, "bea")!!.rank)
        assertEquals(1, FilesNav.matchIn("Beach at sunset", f, "sun")!!.rank)
        assertEquals(2, FilesNav.matchIn("Beach at sunset", f, "nse")!!.rank)
        assertNull(FilesNav.matchIn("Beach at sunset", f, "dog"))
        // The span covers the original text, accents and all.
        val j = FilesNav.matchIn("José María", FilesNav.fold("José María"), "maria")!!
        assertEquals("María", "José María".substring(j.span.first, j.span.last + 1))
    }

    @Test fun pathParts() {
        assertEquals("/a/b/", FilesNav.parentFolder("/a/b/c.txt"))
        assertEquals("/a/", FilesNav.parentFolder("/a/b/"))
        assertEquals("/", FilesNav.parentFolder("/c.txt"))
        assertEquals("c.txt" to "/a/b", FilesNav.pathParts("/a/b/c.txt"))
        assertEquals("b" to "/a", FilesNav.pathParts("/a/b/"))
        assertEquals("/x/", FilesNav.asFolder("/x"))
    }

    @Test fun optionsInOrderWithDocsAfterFiles() {
        val found = FoundFiles("tax", "tax", listOf(file("/Docs/Taxes/", "inode/directory"), file("/Docs/tax 2024.pdf", "application/pdf")))
        val o = searchOptions("tax", tags("taxi", "Tax office", "cat", "syntax"), emptyList(), people("Taxman" to 3), found, noFileSearch = false)
        val keys = o.options.map { it.key }
        assertEquals(listOf("t:taxi", "t:Tax office", "t:syntax", "p:p0", "f:/Docs/Taxes/", "f:/Docs/tax 2024.pdf", "docs"), keys)
        assertEquals("t:taxi", o.best)
        assertEquals(FileKind.FOLDER, (o.options[4] as SearchOption.FileHit).kind)
    }

    @Test fun docsFirstAndBestWhenNoTagOrPerson() {
        val o = searchOptions("invoice", tags("cat"), emptyList(), null, null, noFileSearch = false)
        assertEquals(listOf("docs"), o.options.map { it.key })
        assertEquals("docs", o.best)
    }

    @Test fun olderDeviceHasNoDocsRow() {
        val o = searchOptions("invoice", tags("cat"), emptyList(), null, null, noFileSearch = true)
        assertTrue(o.options.isEmpty())
        assertNull(o.best)
    }

    @Test fun peopleOnlyWhileFacesOnMostPhotosFirst() {
        val named = people("Ana" to 2, "Anabel" to 40, "Juana" to 9, "Bob" to 99)
        assertTrue(searchOptions("ana", emptyList(), emptyList(), null, null, false).options.none { it is SearchOption.PersonHit })
        val hits = searchOptions("ana", emptyList(), emptyList(), named, null, false).options.filterIsInstance<SearchOption.PersonHit>()
        assertEquals(listOf("Anabel", "Juana", "Ana"), hits.map { it.person.name })
        // An exact name is what the Search key takes.
        assertEquals("p:p0", searchOptions("ana", emptyList(), emptyList(), named, null, false).best)
    }

    @Test fun tagsInSearchAreNotOfferedAndAtMostFive() {
        val o = searchOptions("a", tags("a1", "a2", "a3", "a4", "a5", "a6"), listOf("A1"), null, null, true)
        assertEquals(listOf("t:a2", "t:a3", "t:a4", "t:a5", "t:a6"), o.options.map { it.key })
    }

    @Test fun anEarlierAnswerNarrowsOnlyWhileTypingOnFromIt() {
        val found = FoundFiles("ta", "ta", listOf(file("/tax.pdf", "application/pdf"), file("/tea.txt", "text/plain")))
        // "tax" goes on from "ta": kept to what still matches.
        assertEquals(listOf("f:/tax.pdf"), searchOptions("tax", emptyList(), emptyList(), null, found, false).options.filterIsInstance<SearchOption.FileHit>().map { it.key })
        // "te" does not: nothing until the device answers for it.
        assertTrue(searchOptions("te", emptyList(), emptyList(), null, found, false).options.none { it is SearchOption.FileHit })
    }
}
