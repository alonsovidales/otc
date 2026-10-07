// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import cloud.offthe.otc.proto.File as PbFile
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import java.text.Normalizer
import java.util.Locale

// Port of the web's filesNav.ts. Files sent somewhere from outside it -
// the Images search (TopSearch): a folder to show and, maybe, a file in it
// to open as a tap there would; or a text to search every file and folder
// for, shown as a list of results over the folder (which stays as it was).
// MainView shows the Files tab for a request, and FilesExplorerView takes
// it (take) whether it was open already or opens now.

sealed interface FilesRequest {
    /** A folder (a path ending in "/"), and a file in it to open. */
    data class Show(val dir: String, val file: PbFile? = null) : FilesRequest
    /** Every file and folder whose path holds the text. */
    data class Search(val text: String) : FilesRequest
    /** Files picked in the tab bar: whatever results show, back to the folder. */
    data object Leave : FilesRequest
}

/** [start, end) of the part of a label that matches what was typed. */
typealias Span = IntRange

/**
 * Text compared without case or accents, so "jose" finds "José"; [from]
 * maps each of its characters back to the original, to bold the match.
 * Each character goes to upper case and back, as the device's searchFold
 * does: "ς" and "Σ" both become "σ", so "ΟΔΟΣ" finds "Οδός".
 */
class Folded(val text: String, val from: IntArray)

data class Match(val rank: Int, val span: Span)

/** A found path's name and the folder it is in ("/a/b"), with the part the text matched. */
data class FoundParts(val name: String, val dir: String, val nameSpan: Span?, val dirSpan: Span?)

object FilesNav {
    private val _pending = MutableStateFlow<FilesRequest?>(null)
    /** The request not yet taken, if any. */
    val pending: StateFlow<FilesRequest?> = _pending

    fun showInFiles(dir: String, file: PbFile? = null) { _pending.value = FilesRequest.Show(dir, file) }
    fun searchInFiles(text: String) { _pending.value = FilesRequest.Search(text.trim()) }
    fun leaveFilesSearch() { _pending.value = FilesRequest.Leave }

    /** Files has it: it isn't done again when Files shows next time. */
    fun take(r: FilesRequest) { _pending.compareAndSet(r, null) }

    // A device older than SearchFiles answers it "unknown_payload".
    // Whichever asked first - the Images search, or Files for "Search
    // documents" - says so here, and the search offers neither files nor
    // that row again until the app starts over or signs in again.
    private val _noFileSearch = MutableStateFlow(false)
    val noFileSearch: StateFlow<Boolean> = _noFileSearch
    fun deviceCantSearchFiles() { _noFileSearch.value = true }

    /** Log Out: another device may come next. */
    fun reset() {
        _pending.value = null
        _noFileSearch.value = false
    }

    /** The folder a path is in, as Files writes folders: "/a/b/". */
    fun parentFolder(path: String): String {
        val clean = if (path.length > 1 && path.endsWith("/")) path.dropLast(1) else path
        val at = clean.lastIndexOf('/')
        return if (at <= 0) "/" else clean.substring(0, at + 1)
    }

    /** A folder's own path as Files writes it: "/a/b/". */
    fun asFolder(path: String) = if (path.endsWith("/")) path else "$path/"

    /** A found file's name, and the folder it is in as shown with it ("/a/b"). */
    fun pathParts(path: String): Pair<String, String> {
        val clean = if (path.length > 1 && path.endsWith("/")) path.dropLast(1) else path
        val name = clean.substring(clean.lastIndexOf('/') + 1).ifEmpty { clean }
        val dir = parentFolder(clean)
        return name to (if (dir.length > 1) dir.dropLast(1) else dir)
    }

    private val MARKS = Regex("\\p{M}+")

    fun fold(s: String): Folded {
        val text = StringBuilder()
        val from = ArrayList<Int>(s.length)
        var at = 0
        while (at < s.length) {
            val len = Character.charCount(s.codePointAt(at))
            val f = Normalizer.normalize(s.substring(at, at + len), Normalizer.Form.NFD).replace(MARKS, "")
                .uppercase(Locale.ROOT).lowercase(Locale.ROOT)
            repeat(f.length) { from.add(at) }
            text.append(f)
            at += len
        }
        return Folded(text.toString(), from.toIntArray())
    }

    private fun isWordChar(c: Char) = when (Character.getType(c).toByte()) {
        Character.UPPERCASE_LETTER, Character.LOWERCASE_LETTER, Character.TITLECASE_LETTER, Character.MODIFIER_LETTER,
        Character.OTHER_LETTER, Character.DECIMAL_DIGIT_NUMBER, Character.LETTER_NUMBER, Character.OTHER_NUMBER -> true
        else -> false
    }

    /**
     * Where the folded query [q] best matches a label: rank 0 at its
     * start, 1 at the start of a word in it, 2 inside a word; with the part
     * of the label it covers. null when it isn't there.
     */
    fun matchIn(label: String, f: Folded, q: String): Match? {
        if (q.isEmpty()) return null
        var rank = -1
        var at = -1
        var j = f.text.indexOf(q)
        while (j != -1) {
            val r = if (j == 0) 0 else if (isWordChar(f.text[j - 1])) 2 else 1
            if (rank < 0 || r < rank) { rank = r; at = j }
            if (r < 2) break
            j = f.text.indexOf(q, j + 1)
        }
        if (rank < 0) return null
        val last = f.from[at + q.length - 1]
        val end = last + if (label.codePointAt(last) > 0xffff) 2 else 1
        return Match(rank, f.from[at] until end)
    }

    /** A found path's name and folder, with the part the text matched: in the name when it is there, else in the folder. */
    fun foundParts(path: String, q: String): FoundParts {
        val (name, dir) = pathParts(path)
        val nameSpan = matchIn(name, fold(name), q)?.span
        val dirSpan = if (nameSpan != null) null else matchIn(dir, fold(dir), q)?.span
        return FoundParts(name, dir, nameSpan, dirSpan)
    }
}
