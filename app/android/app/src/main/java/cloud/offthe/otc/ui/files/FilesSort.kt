// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import android.icu.text.Collator
import android.icu.text.RuleBasedCollator
import cloud.offthe.otc.R
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appFilesSortAZ
import cloud.offthe.otc.i18n.appFilesSortCreated
import cloud.offthe.otc.i18n.appFilesSortLargest
import cloud.offthe.otc.i18n.appFilesSortModified
import cloud.offthe.otc.i18n.appFilesSortName
import cloud.offthe.otc.i18n.appFilesSortNewest
import cloud.offthe.otc.i18n.appFilesSortOldest
import cloud.offthe.otc.i18n.appFilesSortSize
import cloud.offthe.otc.i18n.appFilesSortSmallest
import cloud.offthe.otc.i18n.appFilesSortZA
import cloud.offthe.otc.net.byteSize
import cloud.offthe.otc.proto.File as PbFile
import java.util.Locale

// Issue #196: the order of Files' list, grid and search results, one
// choice for the app (not per folder, nor on the device). Port of iOS's
// FilesSort.swift, with the web's filesSort.ts's rules and the same test
// cases (FilesSortTest.kt):
// - by Name, Size, Date created or Date modified; each starts in its
//   natural direction (A to Z, largest first, newest first) and picking
//   the same one again reverses it;
// - ".." first, then the folders, then the files, in every order;
// - folders carry no size or dates in a listing, so by Size or a date
//   they stay in name order (A to Z) above the files;
// - names in the app language's collation, ignoring case and accents,
//   with the numbers in them compared by value (file2 before file10):
//   ICU's, as the web's Intl.Collator and iOS's String.compare use, so
//   the three put names in the same order (NameCollator);
// - a file without the date sorted by goes last, either way;
// - ties by name (A to Z), then by path code unit by code unit, so the
//   order never depends on the order the device sent.

/** What Files can be sorted by: the columns of the web's list. */
enum class FilesSortKey(val stored: String) {
    NAME("name"), SIZE("size"), CREATED("created"), MODIFIED("modified");

    /** The direction it starts in: A to Z, largest first, newest first. */
    val naturalAscending: Boolean get() = this == NAME
}

/** The order chosen: a key and its direction. */
data class FilesSortOrder(val key: FilesSortKey, val ascending: Boolean) {
    /** As it is kept: "size:desc". */
    val stored: String get() = "${key.stored}:${if (ascending) "asc" else "desc"}"

    /** The order after picking [key]: the same key the other way, another one in its natural direction. */
    fun picking(key: FilesSortKey) = if (key == this.key) copy(ascending = !ascending) else FilesSortOrder(key, key.naturalAscending)

    companion object {
        val DEFAULT = FilesSortOrder(FilesSortKey.NAME, ascending = true)

        /** Where it is kept: otc_settings, as files_view_mode (iOS's "files.sort"). */
        const val STORAGE_KEY = "files_sort"

        /** A stored order, or the default for anything else. */
        fun parse(stored: String?): FilesSortOrder {
            val parts = (stored ?: "").split(':')
            if (parts.size != 2) return DEFAULT
            val key = FilesSortKey.entries.firstOrNull { it.stored == parts[0] } ?: return DEFAULT
            return when (parts[1]) {
                "asc" -> FilesSortOrder(key, true)
                "desc" -> FilesSortOrder(key, false)
                else -> DEFAULT
            }
        }
    }
}

/** What a row is sorted by: its name as shown, its path, its size and dates in ms (null when the device sent none). */
data class FilesSortFields(val name: String, val path: String, val isDir: Boolean, val size: Long, val created: Long?, val modified: Long?)

/**
 * Names in a language's order, as the web's Intl.Collator (numeric,
 * sensitivity "base") and iOS's String.compare (.numeric, .caseInsensitive,
 * .diacriticInsensitive) put them: ICU's collation for the language at
 * primary strength (case and accents ignored) with numeric collation on, so
 * a number - in any script's decimal digits, fullwidth ones too - counts by
 * its value wherever it sits in the name ("photo.jpg", "photo1.jpg",
 * "photo10.jpg"), and a number against text goes as ICU puts a digit
 * against that text. A name's key, worked out once, compares with another
 * (unsigned, byte by byte) as the names do: one order, whatever order the
 * names come in.
 */
fun interface NameCollator {
    fun key(name: String): ByteArray

    companion object {
        /**
         * The phone's: android.icu (API 24+). JVM tests, which only have
         * android.jar's stubs of it, pass the same collator from ICU4J
         * (FilesSortTest.kt) - change both together.
         */
        fun icu(locale: Locale): NameCollator {
            val c = Collator.getInstance(locale)
            c.strength = Collator.PRIMARY
            c.decomposition = Collator.CANONICAL_DECOMPOSITION
            (c as? RuleBasedCollator)?.numericCollation = true
            return NameCollator { c.getCollationKey(it).toByteArray() }
        }
    }
}

object FilesSort {
    /**
     * Above this many rows the order is worked out off the main thread: a
     * phone's whole library can sit in one folder, and comparing tens of
     * thousands of names in the app's language takes a while.
     */
    const val OFF_MAIN_ABOVE = 5000

    /** Collation keys, unsigned byte by byte (as ICU's CollationKey.compareTo). */
    private fun compareKeys(a: ByteArray, b: ByteArray): Int {
        val n = minOf(a.size, b.size)
        for (i in 0 until n) {
            val c = (a[i].toInt() and 0xff) - (b[i].toInt() and 0xff)
            if (c != 0) return c
        }
        return a.size - b.size
    }

    private class Keyed<T>(val item: T, val f: FilesSortFields, val name: ByteArray)

    // ".." first, then folders, then files.
    private fun group(f: FilesSortFields) = if (f.path == "..") 0 else if (f.isDir) 1 else 2

    // Dates the order's way, a missing one last either way.
    private fun compareDates(a: Long?, b: Long?, ascending: Boolean): Int = when {
        a == null || b == null -> if (a == b) 0 else if (a == null) 1 else -1
        ascending -> a.compareTo(b)
        else -> b.compareTo(a)
    }

    private fun <T> compare(a: Keyed<T>, b: Keyed<T>, order: FilesSortOrder): Int {
        val g = group(a.f).compareTo(group(b.f))
        if (g != 0) return g
        val byName = compareKeys(a.name, b.name).takeIf { it != 0 } ?: a.f.path.compareTo(b.f.path)
        if (order.key == FilesSortKey.NAME) return if (order.ascending) byName else -byName
        // Folders have no size or dates of their own: name order, A to Z.
        if (!a.f.isDir) {
            val c = when (order.key) {
                FilesSortKey.SIZE -> if (order.ascending) a.f.size.compareTo(b.f.size) else b.f.size.compareTo(a.f.size)
                FilesSortKey.CREATED -> compareDates(a.f.created, b.f.created, order.ascending)
                FilesSortKey.MODIFIED -> compareDates(a.f.modified, b.f.modified, order.ascending)
                FilesSortKey.NAME -> 0
            }
            if (c != 0) return c
        }
        return byName
    }

    /** A copy of [items] in [order]; [fields] says what each one is sorted by, [collator] how names compare in [locale]. */
    fun <T> sorted(
        items: List<T>, fields: (T) -> FilesSortFields, order: FilesSortOrder, locale: Locale,
        collator: (Locale) -> NameCollator = NameCollator::icu,
    ): List<T> {
        val names = collator(locale)
        val keyed = items.map { val f = fields(it); Keyed(it, f, names.key(f.name)) }
        return keyed.sortedWith { a, b -> compare(a, b, order) }.map { it.item }
    }

    fun sortedRows(rows: List<FileRow>, order: FilesSortOrder, locale: Locale, collator: (Locale) -> NameCollator = NameCollator::icu): List<FileRow> =
        sorted(rows, FileRow::sortFields, order, locale, collator)

    /** Files as the search finds them (SearchFiles), in [order]. */
    fun sortedFiles(files: List<PbFile>, order: FilesSortOrder, locale: Locale, collator: (Locale) -> NameCollator = NameCollator::icu): List<PbFile> =
        sorted(files, { f -> sortFieldsOf(f, f.path.split('/').lastOrNull { it.isNotEmpty() } ?: f.path) }, order, locale, collator)

    private fun sortFieldsOf(f: PbFile, name: String) =
        FilesSortFields(name, f.path, f.mime == "inode/directory", f.byteSize, f.createdMs, f.modifiedMs)
}

/** A date the device sent, in ms; null when it sent none. */
private val PbFile.createdMs: Long? get() = if (hasCreated()) created.seconds * 1000 + created.nanos / 1_000_000 else null
private val PbFile.modifiedMs: Long? get() = if (hasModified()) modified.seconds * 1000 + modified.nanos / 1_000_000 else null

val FileRow.sortFields: FilesSortFields get() = FilesSortFields(name, path, isDir, size, raw.createdMs, raw.modifiedMs)

/**
 * What the order is called on screen (the View options menu) and to
 * TalkBack: the same words as iOS's FilesSortText, from the catalog
 * (app.files.*), worded when shown.
 */
internal object FilesSortText {
    fun name(key: FilesSortKey): UiText = when (key) {
        FilesSortKey.NAME -> S.appFilesSortName()
        FilesSortKey.SIZE -> S.appFilesSortSize()
        FilesSortKey.CREATED -> S.appFilesSortCreated()
        FilesSortKey.MODIFIED -> S.appFilesSortModified()
    }

    /** Which way, under the key in the menu: "A to Z", "Largest first"... */
    fun direction(order: FilesSortOrder): UiText = when (order.key) {
        FilesSortKey.NAME -> if (order.ascending) S.appFilesSortAZ() else S.appFilesSortZA()
        FilesSortKey.SIZE -> if (order.ascending) S.appFilesSortSmallest() else S.appFilesSortLargest()
        FilesSortKey.CREATED, FilesSortKey.MODIFIED -> if (order.ascending) S.appFilesSortOldest() else S.appFilesSortNewest()
    }

    /** Said once the order changes: "Sorted by Size, Largest first" - the key's and the direction's words inside it. */
    fun sorted(order: FilesSortOrder): UiText = UiText.Res(R.string.app_files_sorted, listOf(name(order.key), direction(order)))
}
