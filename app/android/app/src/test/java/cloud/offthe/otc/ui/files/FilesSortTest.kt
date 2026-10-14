// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.File as PbFile
import cloud.offthe.otc.proto.ListOfFiles
import com.google.protobuf.Timestamp
import com.ibm.icu.text.Collator
import com.ibm.icu.text.RuleBasedCollator
import org.junit.Assert.assertEquals
import org.junit.Test
import java.util.Locale

// Issue #196: Files' order (FilesSort.kt). The same cases as the web's
// files-sort-test.mjs and iOS's FilesSortTests.swift: change all three
// together.
class FilesSortTest {
    private val en: Locale = Locale.ENGLISH

    // The phone compares names with android.icu's collator
    // (NameCollator.icu), which JVM tests only have android.jar's stubs of:
    // ICU4J is the same library, set up the same way - change both
    // together. ICU 63 to 78 put every case here in the same order.
    private val icu4j: (Locale) -> NameCollator = { locale ->
        val c = Collator.getInstance(locale)
        c.strength = Collator.PRIMARY
        c.decomposition = Collator.CANONICAL_DECOMPOSITION
        (c as RuleBasedCollator).numericCollation = true
        NameCollator { c.getCollationKey(it).toByteArray() }
    }

    private fun sortedRows(rows: List<FileRow>, order: FilesSortOrder, locale: Locale = en) = FilesSort.sortedRows(rows, order, locale, icu4j)

    private fun file(path: String, dir: Boolean = false, size: Long = 0, created: Long? = null, modified: Long? = null): PbFile {
        val b = PbFile.newBuilder().setPath(path)
            .setMime(if (dir) "inode/directory" else if (path.endsWith(".jpg")) "image/jpeg" else "text/plain")
            .setSize64(size).setSize(size.toInt())
        if (created != null) b.setCreated(Timestamp.newBuilder().setSeconds(created))
        if (modified != null) b.setModified(Timestamp.newBuilder().setSeconds(modified))
        return b.build()
    }

    // A folder's listing: "..", two folders, and files whose sizes and
    // dates tie in places, one without a creation date, one without a
    // modification date, and two names that differ only by an accent.
    private val files = listOf(
        file("/d/IMG_10.jpg", size = 300, created = 3000, modified = 1000),
        file("/d/é.txt", size = 50, modified = 500),
        file("/d/Zeta", dir = true),
        file("/d/apple.txt", size = 200, created = 2000, modified = 2000),
        file("/d/IMG_2.jpg", size = 100, created = 4000, modified = 3000),
        file("/d/Banana.txt", size = 200, created = 2000),
        file("/d/alpha", dir = true),
        file("/d/e.txt", size = 50, modified = 500),
    )
    // As Files lists it: ".." in the middle here, to show it goes first.
    private val listing = listingRows(ListOfFiles.newBuilder().addAllFiles(files).build(), "/d/").let { rows ->
        rows.drop(1).take(4) + rows[0] + rows.drop(5)
    }

    private fun names(key: FilesSortKey, ascending: Boolean, rows: List<FileRow> = listing) =
        sortedRows(rows, FilesSortOrder(key, ascending)).map { it.name }

    @Test fun nameAToZCountsNumbersAndIgnoresCaseAndAccentsWithFoldersFirst() {
        assertEquals(listOf("..", "alpha", "Zeta", "apple.txt", "Banana.txt", "e.txt", "é.txt", "IMG_2.jpg", "IMG_10.jpg"), names(FilesSortKey.NAME, true))
    }

    @Test fun nameZToA() {
        assertEquals(listOf("..", "Zeta", "alpha", "IMG_10.jpg", "IMG_2.jpg", "é.txt", "e.txt", "Banana.txt", "apple.txt"), names(FilesSortKey.NAME, false))
    }

    @Test fun sizeKeepsFoldersByNameAboveTheFilesAndBreaksTiesByNameThenPath() {
        assertEquals(listOf("..", "alpha", "Zeta", "IMG_10.jpg", "apple.txt", "Banana.txt", "IMG_2.jpg", "e.txt", "é.txt"), names(FilesSortKey.SIZE, false))
        assertEquals(listOf("..", "alpha", "Zeta", "e.txt", "é.txt", "IMG_2.jpg", "apple.txt", "Banana.txt", "IMG_10.jpg"), names(FilesSortKey.SIZE, true))
    }

    @Test fun createdPutsAMissingDateLastEitherWay() {
        assertEquals(listOf("..", "alpha", "Zeta", "IMG_2.jpg", "IMG_10.jpg", "apple.txt", "Banana.txt", "e.txt", "é.txt"), names(FilesSortKey.CREATED, false))
        assertEquals(listOf("..", "alpha", "Zeta", "apple.txt", "Banana.txt", "IMG_10.jpg", "IMG_2.jpg", "e.txt", "é.txt"), names(FilesSortKey.CREATED, true))
    }

    @Test fun modifiedPutsAMissingDateLastEitherWay() {
        assertEquals(listOf("..", "alpha", "Zeta", "IMG_2.jpg", "apple.txt", "IMG_10.jpg", "e.txt", "é.txt", "Banana.txt"), names(FilesSortKey.MODIFIED, false))
        assertEquals(listOf("..", "alpha", "Zeta", "e.txt", "é.txt", "IMG_10.jpg", "apple.txt", "IMG_2.jpg", "Banana.txt"), names(FilesSortKey.MODIFIED, true))
    }

    @Test fun theOrderTheDeviceSentDoesntMatter() {
        for (key in FilesSortKey.entries) for (ascending in listOf(true, false)) {
            assertEquals("$key $ascending", names(key, ascending), names(key, ascending, listing.reversed()))
        }
    }

    @Test fun searchResultsGoByTheirNames() {
        val found = listOf(file("/b/file10", size = 1), file("/a/file2", size = 1), file("/c/Docs", dir = true), file("/a/file2.bak", size = 1))
        assertEquals(listOf("/c/Docs", "/a/file2", "/a/file2.bak", "/b/file10"), FilesSort.sortedFiles(found, FilesSortOrder.DEFAULT, en, icu4j).map { it.path })
    }

    // Names the three apps once put in different orders: a name ending
    // where another goes on with a number, a space before the number,
    // numbers inside names, case and accents (ties, which the paths decide),
    // and digits that aren't ASCII - a fullwidth 5 is the number 5, a
    // superscript 2 and a circled 5 aren't numbers at all (ICU's numeric
    // collation, which all three use).
    private val tricky = listingRows(
        ListOfFiles.newBuilder().addAllFiles(
            listOf(
                "photo10.jpg", "IMG_10.jpg", "Photo 2.jpg", "é.txt", "photo.jpg", "10.txt", "img_2.jpg", "５.txt",
                "photo1.jpg", "E.txt", "IMG_2.jpg", "5.txt", "e.txt", "⑤.txt", "².txt", "2.txt",
            ).map { file("/d/$it") },
        ).build(),
        "/",
    )
    private val trickyAToZ = listOf(
        "2.txt", "5.txt", "５.txt", "10.txt", "².txt", "⑤.txt", "E.txt", "e.txt", "é.txt",
        "IMG_2.jpg", "img_2.jpg", "IMG_10.jpg", "Photo 2.jpg", "photo.jpg", "photo1.jpg", "photo10.jpg",
    )

    @Test fun trickyNamesGoInTheSameOrderAsOnTheWebAndTheIPhone() {
        assertEquals(trickyAToZ, names(FilesSortKey.NAME, true, tricky))
        assertEquals(trickyAToZ.reversed(), names(FilesSortKey.NAME, false, tricky))
    }

    @Test fun trickyNamesGoInOneOrderWhicheverOrderTheyComeIn() {
        for (from in listOf(tricky, tricky.reversed())) for (k in from.indices) {
            assertEquals("from $k", trickyAToZ, names(FilesSortKey.NAME, true, from.drop(k) + from.take(k)))
        }
    }

    @Test fun namesFollowTheAppsLanguage() {
        val rows = listingRows(ListOfFiles.newBuilder().addFiles(file("/d/ä")).addFiles(file("/d/z")).addFiles(file("/d/a")).build(), "/")
        assertEquals(listOf("a", "z", "ä"), sortedRows(rows, FilesSortOrder.DEFAULT, Locale.forLanguageTag("sv")).map { it.name })
    }

    @Test fun eachKeyStartsItsNaturalWayAndTheSameKeyAgainReverses() {
        assertEquals(FilesSortOrder(FilesSortKey.NAME, true), FilesSortOrder.DEFAULT)
        var o = FilesSortOrder.DEFAULT.picking(FilesSortKey.NAME)
        assertEquals(FilesSortOrder(FilesSortKey.NAME, false), o)
        o = o.picking(FilesSortKey.SIZE)
        assertEquals(FilesSortOrder(FilesSortKey.SIZE, false), o)
        o = o.picking(FilesSortKey.SIZE)
        assertEquals(FilesSortOrder(FilesSortKey.SIZE, true), o)
        assertEquals(FilesSortOrder(FilesSortKey.CREATED, false), o.picking(FilesSortKey.CREATED))
        assertEquals(FilesSortOrder(FilesSortKey.MODIFIED, false), o.picking(FilesSortKey.MODIFIED))
    }

    @Test fun theStoredOrderIsReadBackAndAnythingElseIsTheDefault() {
        for (key in FilesSortKey.entries) for (ascending in listOf(true, false)) {
            val o = FilesSortOrder(key, ascending)
            assertEquals(o, FilesSortOrder.parse(o.stored))
        }
        assertEquals("size:desc", FilesSortOrder(FilesSortKey.SIZE, false).stored)
        for (bad in listOf(null, "", "garbage", "size", "name:up", "kind:asc", "SIZE:DESC", " size:desc", "size:desc:x")) {
            assertEquals("$bad", FilesSortOrder.DEFAULT, FilesSortOrder.parse(bad))
        }
    }

    @Test fun theWordsSayTheOrder() {
        assertEquals("Date created", FilesSortText.name(FilesSortKey.CREATED).english())
        assertEquals("Largest first", FilesSortText.direction(FilesSortOrder(FilesSortKey.SIZE, false)).english())
        assertEquals("Oldest first", FilesSortText.direction(FilesSortOrder(FilesSortKey.MODIFIED, true)).english())
        assertEquals("Sorted by Name, A to Z", FilesSortText.sorted(FilesSortOrder.DEFAULT).english())
    }

    @Test fun aBigFolderIsSortedTheSameWay() {
        val n = FilesSort.OFF_MAIN_ABOVE + 500
        val rows = listingRows(ListOfFiles.newBuilder().addAllFiles((n - 1 downTo 0).map { file("/d/IMG_$it.jpg") }).build(), "/d/")
        val sorted = sortedRows(rows, FilesSortOrder.DEFAULT)
        assertEquals(listOf("..", "IMG_0.jpg", "IMG_1.jpg", "IMG_2.jpg"), sorted.take(4).map { it.name })
        assertEquals("IMG_${n - 1}.jpg", sorted.last().name)
    }
}
