// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.isImeVisible
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import cloud.offthe.otc.data.FaceRecognition
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.File as PbFile
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SearchFiles
import cloud.offthe.otc.ui.common.NavIcons
import cloud.offthe.otc.ui.common.circlePath
import cloud.offthe.otc.ui.common.outlineIcon
import cloud.offthe.otc.ui.common.rectPath
import cloud.offthe.otc.ui.common.rememberTileThumb
import cloud.offthe.otc.ui.files.FilesNav
import cloud.offthe.otc.ui.files.Folded
import cloud.offthe.otc.ui.files.FoundParts
import cloud.offthe.otc.ui.files.Span
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.text.NumberFormat

// Port of the web's TopSearch.tsx, at the top of Images where the tag
// field was. The field holds what Images is narrowed to - people and
// tags - as chips (an open collection keeps its own bar under it, with
// its actions). While something is typed a panel over the photos offers,
// in this order, the things (tags) that match, the named people that
// match (while face recognition is on), and the files and folders whose
// path matches (SearchFiles, asked of the device as the typing pauses),
// with a row to search the documents for the word: every file and folder
// it finds, listed in Files (FilesNav.searchInFiles). That row comes
// first, and is what the keyboard's Search key takes, when no tag or
// person matches; otherwise it follows the files. A device without
// SearchFiles has neither. Picking a tag or a person narrows Images;
// picking a folder shows it in Files, and a file opens there as a tap on
// it would. The Search key never opens a file. With nothing typed there
// is no panel.

private const val MATCH_TAGS = 5
private const val MATCH_PEOPLE = 5
private const val MATCH_FILES = 8
// The files are asked for once the typing pauses this long.
private const val FILES_DELAY_MS = 150L

enum class FileKind { FOLDER, PHOTO, VIDEO, DOC }

private val HEIF = Regex("\\.(heic|heif)$", RegexOption.IGNORE_CASE)

fun fileKind(f: PbFile): FileKind = when {
    f.mime == "inode/directory" -> FileKind.FOLDER
    f.mime.startsWith("video/") -> FileKind.VIDEO
    f.mime.startsWith("image/") || HEIF.containsMatchIn(f.path) -> FileKind.PHOTO
    else -> FileKind.DOC
}

/** What the panel offers, in the order shown. */
sealed interface SearchOption {
    val key: String
    data class Tag(val tag: String, val span: Span?) : SearchOption { override val key get() = "t:$tag" }
    data class PersonHit(val person: Person, val span: Span?) : SearchOption { override val key get() = "p:${person.id}" }
    /** The documents searched for the word as typed. */
    data class Docs(val text: String) : SearchOption { override val key get() = "docs" }
    data class FileHit(val file: PbFile, val kind: FileKind, val parts: FoundParts) : SearchOption { override val key get() = "f:${file.path}" }
}

/** The files the device found for [typed] ([q] folded). */
data class FoundFiles(val typed: String, val q: String, val files: List<PbFile>)

/** The options, and the key of the one the Search key takes (null: none). */
data class SearchOptions(val options: List<SearchOption>, val best: String?)

/**
 * The options for [typed]. [tags] and [named] come folded; [named] is
 * null while face recognition is off. Tags in [inSearch] are chips, not
 * suggestions. The files are the device's answer for the text as it is
 * now; until that comes, the answer for the text before stands in only
 * while the typing goes on from it, kept to what still matches. The
 * answer for some other text is never shown.
 */
fun searchOptions(
    typed: String, tags: List<Pair<String, Folded>>, inSearch: List<String>, named: List<Pair<Person, Folded>>?,
    found: FoundFiles?, noFileSearch: Boolean,
): SearchOptions {
    val q = FilesNav.fold(typed).text
    if (q.isEmpty()) return SearchOptions(emptyList(), null)
    val out = mutableListOf<SearchOption>()
    // What the Search key takes: an exact match, or else the first.
    var exact: String? = null
    val taken = inSearch.map { FilesNav.fold(it).text }.toSet()
    tags.asSequence().filter { it.second.text !in taken }
        .mapNotNull { (t, f) -> FilesNav.matchIn(t, f, q)?.let { Triple(t, f, it) } }
        .sortedBy { it.third.rank }.take(MATCH_TAGS)
        .forEach { (t, f, m) ->
            val o = SearchOption.Tag(t, m.span)
            out += o
            if (exact == null && f.text == q) exact = o.key
        }
    // The people with the most photos among those whose name matches.
    named?.asSequence()
        ?.mapNotNull { (p, f) -> FilesNav.matchIn(p.name.trim(), f, q)?.let { Triple(p, f, it) } }
        ?.sortedWith(compareByDescending<Triple<Person, Folded, cloud.offthe.otc.ui.files.Match>> { it.first.faceCount }.thenBy { it.third.rank })
        ?.take(MATCH_PEOPLE)
        ?.forEach { (p, f, m) ->
            val o = SearchOption.PersonHit(p, m.span)
            out += o
            if (exact == null && f.text == q) exact = o.key
        }
    val files = mutableListOf<SearchOption.FileHit>()
    if (found != null) {
        val current = found.typed == typed
        if (current || q.contains(found.q)) for (file in found.files) {
            if (!current && !FilesNav.fold(file.path).text.contains(q)) continue
            files += SearchOption.FileHit(file, fileKind(file), FilesNav.foundParts(file.path, q))
            if (files.size == MATCH_FILES) break
        }
    }
    // The documents row: first, and what the Search key takes, when
    // nothing above matches; below the files otherwise. Files and folders
    // are never what the key takes.
    val docs = if (noFileSearch) emptyList() else listOf(SearchOption.Docs(typed))
    if (out.isEmpty()) { out += docs; out += files } else { out += files; out += docs }
    return SearchOptions(out, exact ?: out.firstOrNull { it !is SearchOption.FileHit }?.key)
}

/** The search's own state: what is typed, whether the panel is wanted, the files found. */
class TopSearchViewModel : ViewModel() {
    var query by mutableStateOf("")
        private set
    /** The field is in use (the panel shows while something is typed too). */
    var open by mutableStateOf(false)
        private set
    var found by mutableStateOf<FoundFiles?>(null)
        private set

    // The last file search sent: an answer to an earlier one is dropped.
    private var seq = 0
    private var asked = ""
    private var waiting: Job? = null

    fun type(text: String) {
        query = text
        open = true
        askFiles()
    }

    fun opened() { open = true }

    fun close() { open = false }

    fun clearQuery() {
        query = ""
        askFiles()
    }

    // The files whose path holds what is typed, once the typing pauses.
    // Emptying the field forgets the last answer, and one still on its way.
    private fun askFiles() {
        val typed = query.trim()
        if (typed == asked) return
        asked = typed
        waiting?.cancel()
        if (typed.isEmpty()) {
            seq++
            found = null
            return
        }
        if (FilesNav.noFileSearch.value) return
        waiting = viewModelScope.launch {
            delay(FILES_DELAY_MS)
            val mine = ++seq
            // Not cancelled by the next key: its answer is only dropped.
            viewModelScope.launch {
                val resp = try {
                    OTCConnection.request { it.setReqSearchFiles(SearchFiles.newBuilder().setQuery(typed).setLimit(MATCH_FILES)) }
                } catch (e: Exception) { null }
                if (resp != null && resp.isUnknownPayload()) FilesNav.deviceCantSearchFiles()
                if (mine != seq) return@launch
                val files = if (resp?.payloadCase == RespEnvelope.PayloadCase.RESP_LIST_OF_FILES) resp.respListOfFiles.filesList else emptyList()
                found = FoundFiles(typed, FilesNav.fold(typed).text, files)
            }
        }
    }
}

/** A device older than the request: it says so in the code, or (very old) only in the message. */
fun RespEnvelope.isUnknownPayload() = error && (errorCode == "unknown_payload" || errorMessage == "unknown payload")

/** The options for what is typed now, from the gallery's tags and people. */
@Composable
fun rememberSearchOptions(search: TopSearchViewModel, st: PhotoGalleryViewModel.State): SearchOptions {
    val faces by FaceRecognition.enabled.collectAsState()
    val noFileSearch by FilesNav.noFileSearch.collectAsState()
    val tagIndex = remember(st.tags) { st.tags.map { it to FilesNav.fold(it) } }
    val named = remember(st.allPeople) { st.allPeople.filter { it.name.isNotBlank() }.map { it to FilesNav.fold(it.name.trim()) } }
    val query = search.query
    val found = search.found
    return remember(query, found, tagIndex, named, faces, noFileSearch, st.chips) {
        searchOptions(query.trim(), tagIndex, st.chips, if (faces == true) named else null, found, noFileSearch)
    }
}

/**
 * Picking an option: the text goes, and the photos, Files or the viewer
 * show what was picked. [onShowPhotos]: a tag or a person was picked - the
 * wide layout's top bar shows Images then, from whichever section (Files
 * follows FilesNav by itself).
 */
private fun pick(o: SearchOption, search: TopSearchViewModel, gallery: PhotoGalleryViewModel, onShowPhotos: () -> Unit) {
    search.clearQuery()
    search.close()
    gallery.refreshListsIfStale()
    when (o) {
        is SearchOption.Tag -> { gallery.addChip(o.tag); onShowPhotos() }
        is SearchOption.PersonHit -> { gallery.togglePerson(o.person.id); onShowPhotos() }
        is SearchOption.Docs -> FilesNav.searchInFiles(o.text)
        // A folder opens; a file opens in its folder, as a tap there.
        is SearchOption.FileHit ->
            if (o.kind == FileKind.FOLDER) FilesNav.showInFiles(FilesNav.asFolder(o.file.path))
            else FilesNav.showInFiles(FilesNav.parentFolder(o.file.path), o.file)
    }
}

// ---- Icons, drawn like NavIcons (TopSearch.tsx's own set) ----

private object SearchIcons {
    val Search: ImageVector by lazy { outlineIcon("Search", circlePath(10.5f, 10.5f, 6f), "m15 15 5 5") }
    /** A page with a magnifier over its corner: search the documents. */
    val DocSearch: ImageVector by lazy {
        outlineIcon("DocSearch", "M10.5 20.5h-4a2 2 0 0 1-2-2v-13a2 2 0 0 1 2-2h7l4 4v3", "M13.5 3.5v4h4M8 11h4M8 14.5h2", circlePath(16f, 16f, 3.2f), "m18.4 18.4 2.6 2.6")
    }
    val Back: ImageVector by lazy { outlineIcon("Back", "M19 12H5m6-6-6 6 6 6") }
    val Close: ImageVector by lazy { outlineIcon("Close", "m6.5 6.5 11 11m0-11-11 11", stroke = 2f) }
    val Tag: ImageVector by lazy {
        outlineIcon("Tag", "M3.5 12V5.5a2 2 0 0 1 2-2H12a2 2 0 0 1 1.4.6l6.9 6.9a2 2 0 0 1 0 2.8l-6.6 6.6a2 2 0 0 1-2.8 0l-6.9-6.9A2 2 0 0 1 3.5 12Z", circlePath(8.5f, 8.5f, 1.4f))
    }
    val Face: ImageVector by lazy { outlineIcon("Face", circlePath(12f, 9.5f, 3.5f), "M5.5 19.5c1.1-3.2 3.6-4.8 6.5-4.8s5.4 1.6 6.5 4.8") }
    val Check: ImageVector by lazy { outlineIcon("Check", "m5 12.5 4.5 4.5L19 7.5", stroke = 3f) }
    val Video: ImageVector by lazy { outlineIcon("Video", rectPath(3.5f, 5.5f, 17f, 13f, 2.5f), "m10.5 9.5 4 2.5-4 2.5Z") }
    val Doc: ImageVector by lazy { outlineIcon("Doc", "M6.5 3.5h7l4 4v11a2 2 0 0 1-2 2h-9a2 2 0 0 1-2-2v-13a2 2 0 0 1 2-2Z", "M13.5 3.5v4h4M8.5 12.5h7M8.5 16h5") }
}

private fun kindIcon(k: FileKind) = when (k) {
    FileKind.FOLDER -> NavIcons.Files
    FileKind.PHOTO -> NavIcons.Images
    FileKind.VIDEO -> SearchIcons.Video
    FileKind.DOC -> SearchIcons.Doc
}

private val numbers: NumberFormat get() = NumberFormat.getIntegerInstance()
private fun photosLabel(n: Int) = "${numbers.format(n)} ${if (n == 1) "photo" else "photos"}"
private fun personLabel(p: Person) = p.name.trim().ifEmpty { "Unnamed" }

/** A label with the part that matched in bold (and [markColor], for a dim line). */
private fun marked(text: String, span: Span?, markColor: Color? = null): AnnotatedString = buildAnnotatedString {
    if (span == null || span.first < 0 || span.last >= text.length) { append(text); return@buildAnnotatedString }
    append(text.substring(0, span.first))
    withStyle(SpanStyle(fontWeight = FontWeight.Bold, color = markColor ?: Color.Unspecified)) { append(text.substring(span.first, span.last + 1)) }
    append(text.substring(span.last + 1))
}

/**
 * The field: a glass (a way back while the panel shows), the chips, the
 * text, and a button that clears the text and the chips (an open
 * collection stays, as on the web). At the top of Images, or in the wide
 * layout's top bar (MainView), where [onShowPhotos] shows Images for a tag
 * or a person picked from another section.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun TopSearchField(
    search: TopSearchViewModel, gallery: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State, options: SearchOptions,
    modifier: Modifier = Modifier, onShowPhotos: () -> Unit = {},
) {
    val focus = LocalFocusManager.current
    val focusRequester = remember { FocusRequester() }
    var focused by remember { mutableStateOf(false) }
    val noFileSearch by FilesNav.noFileSearch.collectAsState()
    val faces by FaceRecognition.enabled.collectAsState()
    val typed = search.query.trim()
    val shown = search.open && FilesNav.fold(typed).text.isNotEmpty()
    val people = if (faces == true) st.selectedPeople else emptyList()
    val chipCount = people.size + st.chips.size
    val colors = MaterialTheme.colorScheme

    fun dismiss() {
        search.close()
        gallery.refreshListsIfStale()
        focus.clearFocus()
    }
    // A change of section ends the search (MainView's go); a change of
    // layout - the window turned or unfolded - only moves this field
    // between the Images header and the wide top bar: in use, it takes
    // the focus back where it lands, and the keyboard with it.
    LaunchedEffect(Unit) { if (search.open) runCatching { focusRequester.requestFocus() } }
    // The text with its cursor: at the end of what is typed when the field
    // appears (the focus given back above), or when the text changes from
    // outside (cleared, a pick).
    var edit by remember { mutableStateOf(TextFieldValue(search.query, TextRange(search.query.length))) }
    val fieldValue = if (edit.text == search.query) edit else TextFieldValue(search.query, TextRange(search.query.length))
    // The system back: first the keyboard goes (the keyboard's own back,
    // which a handler registered after it would otherwise beat), then the
    // panel.
    val keyboardUp = WindowInsets.isImeVisible
    BackHandler(enabled = shown && !keyboardUp) { dismiss() }

    BoxWithConstraints(modifier) {
        // At rest the chips may take most of the field; in use, the text
        // gets room of its own.
        val textMin = if (focused || typed.isNotEmpty()) 96.dp else 24.dp
        val chipsMax = (maxWidth - 36.dp - 40.dp - textMin).coerceAtLeast(0.dp)
        Row(
            Modifier.fillMaxWidth().height(48.dp).clip(RoundedCornerShape(12.dp))
                .background(if (focused) colors.surfaceContainerHighest else colors.surfaceContainerHigh)
                .border(1.dp, if (focused) colors.outline.copy(alpha = 0.5f) else Color.Transparent, RoundedCornerShape(12.dp))
                .clickable(interactionSource = remember { MutableInteractionSource() }, indication = null) {
                    focusRequester.requestFocus()
                    search.opened()
                }
                .padding(start = 4.dp, end = 2.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Box(Modifier.size(36.dp).clip(CircleShape).let { if (shown) it.clickable(onClick = ::dismiss) else it }, contentAlignment = Alignment.Center) {
                if (shown) Icon(SearchIcons.Back, "Close search", Modifier.size(22.dp), tint = colors.onSurface)
                else Icon(SearchIcons.Search, null, Modifier.size(22.dp), tint = if (focused) colors.onSurface else colors.onSurfaceVariant)
            }
            if (chipCount > 0) {
                val scroll = rememberScrollState()
                // A chip added goes at the end of the row: scrolled to.
                var shownChips by remember { mutableStateOf(chipCount) }
                LaunchedEffect(chipCount) {
                    if (chipCount > shownChips) scroll.animateScrollTo(scroll.maxValue)
                    shownChips = chipCount
                }
                Row(
                    Modifier.widthIn(max = chipsMax).horizontalScroll(scroll).padding(start = 2.dp),
                    horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically,
                ) {
                    val byId = st.allPeople.associateBy { it.id }
                    people.forEach { pid ->
                        val p = byId[pid]
                        SearchChip(
                            label = p?.let(::personLabel) ?: "Person", dim = p?.name.isNullOrBlank(),
                            removeLabel = p?.name?.trim()?.ifEmpty { null }?.let { "Remove $it" } ?: "Remove this person",
                            onRemove = { gallery.togglePerson(pid) },
                        ) { PersonPic(p, 24) }
                    }
                    st.chips.forEach { t -> SearchChip(label = t, removeLabel = "Remove $t", onRemove = { gallery.removeChip(t) }) }
                }
            }
            Box(Modifier.weight(1f).padding(horizontal = 8.dp), contentAlignment = Alignment.CenterStart) {
                if (search.query.isEmpty() && chipCount == 0) {
                    Text("Search photos and files", style = MaterialTheme.typography.bodyLarge, color = colors.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                BasicTextField(
                    value = fieldValue,
                    onValueChange = {
                        edit = it
                        if (it.text != search.query) search.type(it.text)
                    },
                    singleLine = true,
                    textStyle = MaterialTheme.typography.bodyLarge.copy(color = colors.onSurface),
                    cursorBrush = SolidColor(colors.primary),
                    keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.None, autoCorrectEnabled = false, imeAction = ImeAction.Search),
                    keyboardActions = KeyboardActions(onSearch = {
                        // The best match; else the documents searched for
                        // the words. A device that can't do that leaves
                        // the panel showing that nothing matches.
                        val best = options.options.firstOrNull { it.key == options.best }
                        when {
                            typed.isNotEmpty() && best != null -> { pick(best, search, gallery, onShowPhotos); focus.clearFocus() }
                            typed.isNotEmpty() && !noFileSearch -> { pick(SearchOption.Docs(typed), search, gallery, onShowPhotos); focus.clearFocus() }
                            typed.isNotEmpty() -> search.opened()
                            else -> dismiss()
                        }
                    }),
                    modifier = Modifier.fillMaxWidth().focusRequester(focusRequester)
                        .semantics { contentDescription = "Search photos and files" }
                        .onFocusChanged {
                            focused = it.isFocused
                            if (it.isFocused) search.opened()
                        },
                )
            }
            if (search.query.isNotEmpty() || chipCount > 0) {
                Box(
                    Modifier.size(40.dp).clip(CircleShape).clickable {
                        search.clearQuery()
                        gallery.clearSearch()
                    },
                    contentAlignment = Alignment.Center,
                ) { Icon(SearchIcons.Close, "Clear search", Modifier.size(20.dp), tint = colors.onSurfaceVariant) }
            }
        }
    }
}

@Composable
private fun SearchChip(label: String, removeLabel: String, onRemove: () -> Unit, dim: Boolean = false, leading: (@Composable () -> Unit)? = null) {
    val colors = MaterialTheme.colorScheme
    Row(
        Modifier.height(32.dp).widthIn(max = 168.dp).clip(RoundedCornerShape(8.dp))
            .background(colors.primary.copy(alpha = 0.12f)).border(1.dp, colors.primary.copy(alpha = 0.3f), RoundedCornerShape(8.dp))
            .padding(start = if (leading != null) 3.dp else 12.dp, end = 2.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (leading != null) { leading(); Spacer(Modifier.width(6.dp)) }
        Text(label, style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis,
            color = if (dim) colors.onSurfaceVariant else colors.onSurface, modifier = Modifier.weight(1f, fill = false))
        Box(Modifier.size(28.dp).clip(CircleShape).clickable(onClick = onRemove), contentAlignment = Alignment.Center) {
            Icon(SearchIcons.Close, removeLabel, Modifier.size(16.dp), tint = colors.onSurfaceVariant)
        }
    }
}

/** A person's face (their cover), round; the outline of one without. */
@Composable
private fun PersonPic(p: Person?, sizeDp: Int) {
    val px = with(LocalDensity.current) { sizeDp.dp.roundToPx() }
    val bmp = rememberTileThumb(
        if (p == null || p.coverThumbnail.isEmpty) null else "p:${p.id}:${p.coverThumbnail.hashCode()}", px,
    ) { p?.coverThumbnail?.toByteArray() }
    Box(Modifier.size(sizeDp.dp).clip(CircleShape).background(MaterialTheme.colorScheme.surfaceContainerHighest), contentAlignment = Alignment.Center) {
        if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
        else Icon(SearchIcons.Face, null, Modifier.size((sizeDp * 0.7f).dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/**
 * The panel, laid over the photos (the box it is placed in) while
 * something is typed: Things, People, Files and the documents row, over a
 * dimmed grid whose tap ends the search. [dropdown]: the wide layout's,
 * hanging from the field in the top bar as the web's does on a window
 * that wide - a card [dropdownWidth] wide at [dropdownOffset] in the box,
 * no dimming, and a tap anywhere else in the box ends the search.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun TopSearchPanel(
    search: TopSearchViewModel, gallery: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State, options: SearchOptions,
    modifier: Modifier = Modifier, onShowPhotos: () -> Unit = {},
    dropdown: Boolean = false, dropdownOffset: IntOffset = IntOffset.Zero, dropdownWidth: Dp = 0.dp,
) {
    val focus = LocalFocusManager.current
    val typed = search.query.trim()
    val q = FilesNav.fold(typed).text
    if (!search.open || q.isEmpty()) return
    val colors = MaterialTheme.colorScheme
    fun choose(o: SearchOption) {
        pick(o, search, gallery, onShowPhotos)
        focus.clearFocus()
    }
    // The dropdown's own back, registered as it opens, so it comes before
    // the page under it (People's, Collections') - the keyboard's first.
    if (dropdown) {
        val keyboardUp = WindowInsets.isImeVisible
        BackHandler(enabled = !keyboardUp) {
            search.close()
            gallery.refreshListsIfStale()
            focus.clearFocus()
        }
    }
    val tags = options.options.filterIsInstance<SearchOption.Tag>()
    val people = options.options.filterIsInstance<SearchOption.PersonHit>()
    val files = options.options.filterIsInstance<SearchOption.FileHit>()
    val docs = options.options.filterIsInstance<SearchOption.Docs>().firstOrNull()
    // The row to search the documents: first when no tag or person matches.
    val docsFirst = tags.isEmpty() && people.isEmpty()
    val alreadyIn = st.chips.any { FilesNav.fold(it).text == q }

    BoxWithConstraints(modifier.fillMaxSize()) {
        Box(
            Modifier.fillMaxSize().background(if (dropdown) Color.Transparent else Color.Black.copy(alpha = 0.5f))
                .clickable(interactionSource = remember { MutableInteractionSource() }, indication = null) {
                    search.close()
                    gallery.refreshListsIfStale()
                    focus.clearFocus()
                },
        )
        Surface(
            if (dropdown) {
                // Under the field, as wide as it (320 at least), and no
                // taller than 640 or 70% of the room left (the keyboard's
                // already taken out of it).
                Modifier.offset { dropdownOffset }.width(dropdownWidth.coerceAtLeast(320.dp).coerceAtMost(maxWidth))
                    .heightIn(max = minOf(640.dp, (maxHeight - with(LocalDensity.current) { dropdownOffset.y.toDp() } - 8.dp).coerceAtLeast(120.dp)))
                    .border(1.dp, colors.outlineVariant, RoundedCornerShape(16.dp))
            } else Modifier.fillMaxWidth(),
            shape = if (dropdown) RoundedCornerShape(16.dp) else RoundedCornerShape(bottomStart = 16.dp, bottomEnd = 16.dp),
            color = colors.surfaceContainer, tonalElevation = 3.dp, shadowElevation = 8.dp,
        ) {
            LazyColumn(contentPadding = PaddingValues(top = 4.dp, bottom = 8.dp)) {
                fun section(title: String) = item(key = "h:$title") { SectionHead(title) }
                if (tags.isNotEmpty()) {
                    section("Things")
                    items(tags, key = { it.key }) { o ->
                        OptionRow(o.key == options.best, { choose(o) }) {
                            RowIcon(SearchIcons.Tag)
                            Text(marked(o.tag, o.span), style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                        }
                    }
                }
                if (people.isNotEmpty()) {
                    section("People")
                    items(people, key = { it.key }) { o ->
                        val on = o.person.id in st.selectedPeople
                        OptionRow(o.key == options.best, { choose(o) }) {
                            Box(if (on) Modifier.border(2.dp, colors.primary, CircleShape).padding(2.dp) else Modifier) { PersonPic(o.person, 36) }
                            Column(Modifier.weight(1f)) {
                                Text(marked(personLabel(o.person), o.span), style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                Text(photosLabel(o.person.faceCount), style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant, maxLines = 1)
                            }
                            if (on) Icon(SearchIcons.Check, "In the search", Modifier.size(20.dp), tint = colors.primary)
                        }
                    }
                }
                if (docs != null && docsFirst) item(key = "docs") { DocsRow(docs, docs.key == options.best) { choose(docs) } }
                if (files.isNotEmpty()) {
                    section("Files")
                    items(files, key = { it.key }) { o ->
                        OptionRow(false, { choose(o) }) {
                            val folder = o.kind == FileKind.FOLDER
                            Box(
                                Modifier.size(36.dp).clip(RoundedCornerShape(8.dp)).background(colors.onSurface.copy(alpha = 0.06f)),
                                contentAlignment = Alignment.Center,
                            ) { Icon(kindIcon(o.kind), null, Modifier.size(22.dp), tint = if (folder) colors.primary else colors.onSurface) }
                            Column(Modifier.weight(1f)) {
                                Text(marked(o.parts.name, o.parts.nameSpan), style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                // Long folders lose their start, not the end nearest the file.
                                Text(
                                    marked(o.parts.dir, o.parts.dirSpan, colors.onSurface), style = MaterialTheme.typography.bodySmall,
                                    color = colors.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.StartEllipsis,
                                )
                            }
                        }
                    }
                }
                if (docs != null && !docsFirst) item(key = "docs") { DocsRow(docs, docs.key == options.best) { choose(docs) } }
                if (options.options.isEmpty()) item(key = "hint") {
                    Text(
                        if (alreadyIn) "“$typed” is already in the search." else "Nothing matches “$typed”.",
                        style = MaterialTheme.typography.bodyMedium, color = colors.onSurfaceVariant,
                        modifier = Modifier.padding(horizontal = 20.dp, vertical = 12.dp),
                    )
                }
            }
        }
    }
}

@Composable
private fun SectionHead(title: String) {
    Text(
        title.uppercase(), style = MaterialTheme.typography.labelMedium, fontWeight = FontWeight.SemiBold,
        color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 20.dp, end = 20.dp, top = 10.dp, bottom = 4.dp),
    )
}

@Composable
private fun RowIcon(icon: ImageVector) {
    Box(Modifier.size(36.dp), contentAlignment = Alignment.Center) {
        Icon(icon, null, Modifier.size(22.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/** One option: the best one (what the Search key takes) highlighted. */
@Composable
private fun OptionRow(active: Boolean, onClick: () -> Unit, content: @Composable RowScope.() -> Unit) {
    Row(
        Modifier.fillMaxWidth().padding(horizontal = 8.dp).clip(RoundedCornerShape(10.dp))
            .background(if (active) MaterialTheme.colorScheme.onSurface.copy(alpha = 0.08f) else Color.Transparent)
            .clickable(onClick = onClick).heightIn(min = 52.dp).padding(horizontal = 12.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp), content = content,
    )
}

@Composable
private fun DocsRow(o: SearchOption.Docs, active: Boolean, onClick: () -> Unit) {
    Column(Modifier.padding(top = 4.dp)) {
        OptionRow(active, onClick) {
            RowIcon(SearchIcons.DocSearch)
            Text(
                buildAnnotatedString {
                    append("Search documents for “")
                    withStyle(SpanStyle(fontWeight = FontWeight.Bold)) { append(o.text) }
                    append("”")
                },
                style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f),
            )
        }
    }
}
