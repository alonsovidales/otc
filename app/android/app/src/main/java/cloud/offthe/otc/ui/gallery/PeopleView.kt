// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import androidx.activity.compose.BackHandler
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
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
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.MoreHoriz
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.scale
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.DeletePerson
import cloud.offthe.otc.proto.MergePeople
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.RenamePerson
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.HiddenPageHeading
import cloud.offthe.otc.ui.common.NAV_STROKE
import cloud.offthe.otc.ui.common.NavIcons
import cloud.offthe.otc.ui.common.OTCTextField
import cloud.offthe.otc.ui.common.circlePath
import cloud.offthe.otc.ui.common.gridCellPx
import cloud.offthe.otc.ui.common.outlineIcon
import cloud.offthe.otc.ui.common.rememberTileThumb
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import java.text.NumberFormat

// Port of the web's PeopleView.tsx, opened from Images' People button (next
// to Collections) while face recognition is on: the faces the device found,
// named people first, each a circle that opens Images on that person. A
// face's "more" button (and "Add a name" under an unnamed one) names it,
// starts a merge or deletes it. A long press picks a face, then taps pick
// and unpick, and the bar that takes the page's top merges them into one
// (face matching split one person in several) or deletes them.

// How long the first listing may take before the page offers to ask again.
private const val SLOW_MS = 10_000L
private const val TOAST_MS = 3_200L
private const val ERROR_TOAST_MS = 6_000L
// people.name is a varchar(150).
private const val MAX_NAME = 100
// Deletions out at once: one request per person, a few at a time.
private const val DELETE_PARALLEL = 3
private const val SKELETON_FACES = 12

private val numbers: NumberFormat get() = NumberFormat.getIntegerInstance()
private fun counted(n: Int, one: String, many: String) = "${numbers.format(n)} ${if (n == 1) one else many}"
private fun photosLabel(n: Int) = counted(n, "photo", "photos")
private fun tidy(s: String) = s.replace(Regex("\\s+"), " ").trim()
private fun fold(s: String) = tidy(s).lowercase()

// "Ana", "Ana and Leo", "Ana, Leo and Rosa".
private fun andList(xs: List<String>) =
    if (xs.size < 2) xs.firstOrNull() ?: "" else xs.dropLast(1).joinToString(", ") + " and " + xs.last()

/** The different names among some people, each once (Ana and ana are one). */
internal fun namesOf(list: List<Person>): List<String> {
    val seen = mutableSetOf<String>()
    return list.mapNotNull { p -> tidy(p.name).takeIf { it.isNotEmpty() && seen.add(fold(it)) } }
}

/**
 * The face a merge keeps, preselected when the choice is plain: the one
 * named person (the one of them with the most photos, when several share
 * that name), or, when nobody has a name, the face with the most photos.
 * People with different names need a pick (null): only one name can stay.
 */
internal fun firstKeep(list: List<Person>): String? {
    if (list.isEmpty()) return null
    val named = list.filter { tidy(it.name).isNotEmpty() }
    if (namesOf(named).size > 1) return null
    val from = named.ifEmpty { list }
    return from.reduce { a, b -> if (b.faceCount > a.faceCount) b else a }.id
}

/** The page's order: named people first, then the unnamed, each by photos (most first). */
internal fun peopleOrder(all: List<Person>, gone: Set<String>): List<Person> {
    val shown = all.filter { it.id !in gone }
    val (named, unnamed) = shown.partition { it.name.isNotBlank() }
    return named.sortedByDescending { it.faceCount } + unnamed.sortedByDescending { it.faceCount }
}

/**
 * Where the people live: the gallery (PhotoGalleryViewModel), whose list
 * the search shares. What the People page changes is told to it.
 */
interface PeopleStore {
    /** Asks the device for the list again: true once it came. */
    suspend fun loadPeople(): Boolean
    fun renamePersonLocally(id: String, name: String)
    fun forgetPeople(ids: Set<String>)
}

/** A request to the device, and its answer (it throws when nothing answered). */
typealias PeopleRequest = suspend ((ReqEnvelope.Builder) -> Unit) -> RespEnvelope

/**
 * The page's state, kept here rather than in the composition so a merge or
 * a deletion under way finishes (and says how it went) even when the page
 * is left or the screen turns. [send] is the device (a stand-in in tests).
 */
class PeopleViewModel(private val send: PeopleRequest = { OTCConnection.request(build = it) }) : ViewModel() {
    sealed interface Confirm {
        data object Merge : Confirm
        /** From a face's menu (one) or from the selection bar. */
        data class Delete(val ids: List<String>) : Confirm
    }
    enum class Listing { NO, RUNNING, DONE }
    data class Toast(val text: String, val error: Boolean, val at: Long = System.nanoTime())

    data class State(
        /** Picked, in the order they were picked; the page shows them in its own. */
        val selection: List<String> = emptyList(),
        /** Started from a face's "Merge with others…": the bar says what to pick next. */
        val mergeHint: Boolean = false,
        val confirm: Confirm? = null,
        val busy: Boolean = false,
        val confirmError: String? = null,
        /** How far a deletion of several has got. */
        val progress: Pair<Int, Int>? = null,
        val toast: Toast? = null,
        /** Merged away or deleted: hidden at once, before the list comes again. */
        val gone: Set<String> = emptySet(),
        /** Names on their way to the device, shown (dimmed) until it answers. */
        val saving: Map<String, String> = emptyMap(),
        val listing: Listing = Listing.NO,
        val slow: Boolean = false,
    )

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state

    /**
     * The page shown: the list asked for again (new faces are found as
     * photos arrive) while what the gallery holds shows. A listing that
     * fails, or a first one that is slow, gets a "Try again".
     */
    fun reload(store: PeopleStore) {
        _state.update { it.copy(listing = Listing.RUNNING, slow = false) }
        viewModelScope.launch {
            val slow = launch { delay(SLOW_MS); _state.update { it.copy(slow = true) } }
            store.loadPeople()
            slow.cancel()
            _state.update { it.copy(listing = Listing.DONE) }
        }
    }

    /** Opened from Images' button: a new visit, with nothing picked or asked from the last one. */
    fun enter() {
        if (!_state.value.busy) _state.update { it.copy(selection = emptyList(), mergeHint = false, confirm = null, confirmError = null) }
    }

    // ---- picking ----

    fun toggle(id: String) = _state.update { st ->
        st.copy(selection = if (id in st.selection) st.selection - id else st.selection + id)
            .let { if (it.selection.isEmpty()) it.copy(mergeHint = false) else it }
    }

    fun pick(id: String) = _state.update { st -> if (id in st.selection) st else st.copy(selection = st.selection + id) }

    fun clearSelection() = _state.update { it.copy(selection = emptyList(), mergeHint = false) }

    /** Someone merged or deleted elsewhere (a new listing) is no longer picked. */
    fun keepOnly(ids: Set<String>) = _state.update { st ->
        val next = st.selection.filter { it in ids }
        if (next.size == st.selection.size) st else st.copy(selection = next, mergeHint = st.mergeHint && next.isNotEmpty())
    }

    /** A face's "Merge with others…": a selection with this face in it. */
    fun mergeWithOthers(id: String) = _state.update { it.copy(selection = listOf(id), mergeHint = true) }

    // ---- questions ----

    fun askMerge() {
        if (_state.value.selection.size < 2) { say("Select 2 or more faces to merge them into one."); return }
        _state.update { it.copy(confirm = Confirm.Merge, confirmError = null) }
    }

    fun askDelete(ids: List<String>) = _state.update { it.copy(confirm = Confirm.Delete(ids), confirmError = null) }

    /** Cancel, back, or a tap outside: not while the device is at work, so its answer has somewhere to show. */
    fun dismissConfirm() { if (!_state.value.busy) _state.update { it.copy(confirm = null, confirmError = null) } }

    fun say(text: String, error: Boolean = false) = _state.update { it.copy(toast = Toast(text, error)) }
    fun toastShown(t: Toast) = _state.update { if (it.toast == t) it.copy(toast = null) else it }

    // ---- the device ----

    /** A request the device answers with an Ack: true once it is done. Throws when nothing answered. */
    private suspend fun ack(build: (ReqEnvelope.Builder) -> Unit): Boolean {
        val r = send(build)
        return r.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && r.respAck.ok
    }

    /** The list again, after a change, without holding up what comes next. */
    private fun refresh(store: PeopleStore) { viewModelScope.launch { store.loadPeople() } }

    /**
     * Names [p] (an empty [typed] takes the name off). The name shows at
     * once, dimmed, until the device answers.
     */
    fun rename(store: PeopleStore, p: Person, typed: String) {
        val name = tidy(typed)
        if (name == tidy(p.name)) return
        _state.update { it.copy(saving = it.saving + (p.id to name)) }
        viewModelScope.launch { renameNow(store, p.id, name) }
    }

    internal suspend fun renameNow(store: PeopleStore, id: String, name: String) {
        val ok = try {
            ack { it.setReqRenamePerson(RenamePerson.newBuilder().setId(id).setName(name)) }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) { false }
        if (ok) store.renamePersonLocally(id, name) else say("The name couldn't be saved. Try again.", error = true)
        _state.update { it.copy(saving = it.saving - id) }
    }

    /** Merges the picked faces into [keep], named [name] (see [mergeNow]). */
    fun merge(store: PeopleStore, keep: Person, name: String) {
        if (_state.value.busy || _state.value.selection.none { it != keep.id }) return
        _state.update { it.copy(busy = true, confirmError = null) }
        viewModelScope.launch { if (mergeNow(store, keep, name)) refresh(store) }
    }

    /**
     * The merge itself, [busy][State.busy] already set: true once done.
     * The merged person keeps the kept face's name, so a name it lacks (one
     * only another of them has, or one typed) is given to it first: merged
     * first and named after, a failed rename would lose a name whose only
     * owner was just merged away. This way round a failure loses nothing -
     * at worst the kept face is named and the others are still there to
     * merge again.
     */
    internal suspend fun mergeNow(store: PeopleStore, keep: Person, name: String): Boolean {
        val sources = _state.value.selection.filter { it != keep.id }
        try {
            if (name.isNotEmpty() && name != tidy(keep.name)) {
                if (!ack { it.setReqRenamePerson(RenamePerson.newBuilder().setId(keep.id).setName(name)) }) {
                    _state.update { it.copy(confirmError = "The name couldn't be saved, so nothing was merged. Try again.") }
                    return false
                }
                store.renamePersonLocally(keep.id, name)
            }
            if (!ack { it.setReqMergePeople(MergePeople.newBuilder().setTargetId(keep.id).addAllSourceIds(sources)) }) {
                _state.update { it.copy(confirmError = "They couldn't be merged. Try again.") }
                return false
            }
            store.forgetPeople(sources.toSet())
            _state.update { it.copy(gone = it.gone + sources, confirm = null, selection = emptyList(), mergeHint = false) }
            val n = sources.size + 1
            say(if (name.isNotEmpty()) "Merged $n into $name" else "Merged $n faces")
            return true
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            _state.update { it.copy(confirmError = "Your device didn't answer. Try again.") }
            return false
        } finally {
            _state.update { it.copy(busy = false) }
        }
    }

    /** Deletes [ids] (see [removeNow]). */
    fun remove(store: PeopleStore, ids: List<String>) {
        if (ids.isEmpty() || _state.value.busy) return
        _state.update { it.copy(busy = true, confirmError = null, progress = 0 to ids.size) }
        viewModelScope.launch { if (removeNow(store, ids)) refresh(store) }
    }

    /**
     * One DeletePerson per person, a few at a time, [busy][State.busy]
     * already set: true when any went. What couldn't be deleted stays
     * picked, to try again; when nothing could, the dialog stays open and
     * says so.
     */
    internal suspend fun removeNow(store: PeopleStore, ids: List<String>): Boolean {
        val deleted = mutableSetOf<String>()
        var unanswered = 0
        val lock = Mutex()
        val queue = ids.iterator()
        suspend fun next(): String? = lock.withLock { if (queue.hasNext()) queue.next() else null }
        try {
            coroutineScope {
                List(minOf(DELETE_PARALLEL, ids.size)) {
                    async {
                        while (true) {
                            val id = next() ?: break
                            try {
                                if (ack { it.setReqDeletePerson(DeletePerson.newBuilder().setId(id)) }) lock.withLock { deleted += id }
                            } catch (e: CancellationException) {
                                throw e
                            } catch (_: Exception) {
                                lock.withLock { unanswered++ }
                            }
                            _state.update { st -> st.copy(progress = st.progress?.let { (d, t) -> (d + 1) to t }) }
                        }
                    }
                }.awaitAll()
            }
        } finally {
            _state.update { it.copy(busy = false, progress = null) }
        }
        if (deleted.isEmpty()) {
            val why = when {
                unanswered == ids.size -> "Your device didn't answer. Try again."
                ids.size == 1 -> "This person couldn't be deleted. Try again."
                else -> "They couldn't be deleted. Try again."
            }
            _state.update { it.copy(confirmError = why) }
            return false
        }
        store.forgetPeople(deleted)
        _state.update { st ->
            val left = st.selection.filter { it !in deleted }
            st.copy(gone = st.gone + deleted, selection = left, mergeHint = st.mergeHint && left.isNotEmpty(), confirm = null)
        }
        val failed = ids.size - deleted.size
        if (failed == 0) {
            say(if (ids.size == 1) "Person deleted" else "Deleted ${counted(ids.size, "person", "people")}")
        } else {
            // "1 of 2 people couldn't be deleted and is still selected."
            // Only a deletion of several gets here: one is all or nothing.
            val verb = if (failed == 1) "is" else "are"
            say("${numbers.format(failed)} of ${counted(ids.size, "person", "people")} couldn't be deleted and $verb still selected. Try again.", error = true)
        }
        return true
    }
}

// ---- icons, in the style of NavIcons (PeopleView.tsx's own set) ----

private object PeopleIcons {
    val Close: ImageVector by lazy { outlineIcon("Close", "M6.5 6.5l11 11M17.5 6.5l-11 11") }
    val Back: ImageVector by lazy { outlineIcon("Back", "M19 12H5m6-6-6 6 6 6") }
    val Pencil: ImageVector by lazy { outlineIcon("Pencil", "M4 20h4L19 9a2.83 2.83 0 0 0-4-4L4 16v4Z", "m13.5 6.5 4 4") }
    /** Two lines joining into one. */
    val Merge: ImageVector by lazy {
        outlineIcon("Merge", "M6 20v-3.5a4 4 0 0 1 1.17-2.83L12 9", "M18 20v-3.5a4 4 0 0 0-1.17-2.83L12 9V4", "m8.5 7.5 3.5-3.5 3.5 3.5")
    }
    val Trash: ImageVector by lazy {
        outlineIcon(
            "Trash", "M4.5 7h15", "M9.5 7V5.5A1.5 1.5 0 0 1 11 4h2a1.5 1.5 0 0 1 1.5 1.5V7",
            "m6.5 7 .8 11.2A2 2 0 0 0 9.3 20h5.4a2 2 0 0 0 2-1.8L17.5 7", "M10 11v5M14 11v5",
        )
    }
    /** Heavier, to read small on a filled circle. */
    val Check: ImageVector by lazy { outlineIcon("Check", "m5.5 12.5 4 4 9-9", stroke = 2.6f) }
    /** A head and shoulders, for a face without a picture. */
    val Person: ImageVector by lazy { outlineIcon("Person", circlePath(12f, 9f, 3.5f), "M5 20c.9-3.6 3.7-5.5 7-5.5s6.1 1.9 7 5.5", stroke = NAV_STROKE * 0.7f) }
    val Problem: ImageVector by lazy { outlineIcon("Problem", circlePath(12f, 12f, 9f), "M12 7.5V13M12 16.5h.01", stroke = 1.4f) }
}

private val Danger = Color(0xFFE53935)

// ---- the page ----

/**
 * The People page, in place of Images' grid. [onOpenPhotos]: a face was
 * tapped (the gallery already searches for that person): show the photos.
 * [onBack]: back to Images - the system back, and the arrow in the page's
 * bar. [showBack] false is the wide layout, whose menu has Images and names
 * the page: no bar at all then, but the selection's.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun PeopleView(gallery: PhotoGalleryViewModel, onOpenPhotos: () -> Unit, onBack: () -> Unit, showBack: Boolean = true) {
    val vm: PeopleViewModel = viewModel(key = "people")
    val st by vm.state.collectAsState()
    val gst by gallery.state.collectAsState()
    val colors = MaterialTheme.colorScheme
    val haptics = LocalHapticFeedback.current

    LaunchedEffect(Unit) { vm.reload(gallery) }

    val people = remember(gst.allPeople, st.gone) { peopleOrder(gst.allPeople, st.gone) }
    LaunchedEffect(people) { vm.keepOnly(people.map { it.id }.toSet()) }
    val selPeople = remember(people, st.selection) { people.filter { it.id in st.selection } }
    val selecting = selPeople.isNotEmpty()
    val canMerge = selPeople.size > 1
    val loaded = gst.peopleLoaded
    // The face being named (its id: the tile may be renamed or gone meanwhile).
    var renaming by rememberSaveable { mutableStateOf<String?>(null) }

    // Back: the selection goes first, then the page.
    BackHandler { if (selecting) vm.clearSelection() else onBack() }

    // Who a question is about went away meanwhile (another phone merged or
    // deleted them): no question left.
    val deleting = st.confirm as? PeopleViewModel.Confirm.Delete
    val deletePeople = deleting?.let { d -> people.filter { it.id in d.ids } } ?: emptyList()
    val moot = (st.confirm == PeopleViewModel.Confirm.Merge && !canMerge) || (deleting != null && deletePeople.isEmpty())
    LaunchedEffect(moot, st.busy) { if (moot && !st.busy) vm.dismissConfirm() }

    st.toast?.let { t -> LaunchedEffect(t) { delay(if (t.error) ERROR_TOAST_MS else TOAST_MS); vm.toastShown(t) } }

    Column(Modifier.fillMaxSize()) {
        if (selecting) {
            SelectionBar(
                count = selPeople.size, hint = st.mergeHint && !canMerge, canMerge = canMerge,
                onClose = { vm.clearSelection() }, onMerge = { vm.askMerge() },
                onDelete = { vm.askDelete(selPeople.map { it.id }) },
            )
        } else if (showBack) {
            Row(
                Modifier.fillMaxWidth().height(64.dp).background(colors.surfaceContainerLow).padding(start = 4.dp, end = 16.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                IconButton(onClick = onBack) { Icon(PeopleIcons.Back, "Back to photos") }
                Spacer(Modifier.width(4.dp))
                Text("People", style = MaterialTheme.typography.titleLarge)
            }
        } else {
            // The wide layout's menu names the page, as the web's does: no
            // title on screen, only for TalkBack.
            HiddenPageHeading("People")
        }

        Box(Modifier.weight(1f).fillMaxWidth()) {
            when {
                loaded && people.isEmpty() -> StateMessage(
                    art = { EmptyArt() }, title = "No faces found yet",
                    text = "Face recognition runs on the device after photos are uploaded. You can switch it on in Settings.",
                )
                loaded -> BoxWithConstraints(Modifier.fillMaxSize()) {
                    val density = LocalDensity.current
                    val cellPx = gridCellPx(constraints.maxWidth, density, 12.dp, 8.dp, minSize = 112.dp)
                    val face = with(density) { (cellPx.toDp() - 16.dp).coerceIn(72.dp, 112.dp) }
                    LazyVerticalGrid(
                        // Wide, no bar above: the web's 24 from the top bar.
                        columns = GridCells.Adaptive(112.dp), contentPadding = PaddingValues(start = 12.dp, end = 12.dp, top = if (showBack || selecting) 16.dp else 24.dp, bottom = 80.dp),
                        horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(20.dp),
                        modifier = Modifier.fillMaxSize(),
                    ) {
                        items(people, key = { it.id }) { p ->
                            PersonTile(
                                p, face, name = tidy(st.saving[p.id] ?: p.name), pending = p.id in st.saving,
                                selecting = selecting, selected = p.id in st.selection, canMerge = people.size > 1,
                                onTap = {
                                    if (selecting) vm.toggle(p.id)
                                    else { gallery.showPerson(p.id); onOpenPhotos() }
                                },
                                onLongPress = {
                                    if (selecting) vm.toggle(p.id)
                                    else { haptics.performHapticFeedback(HapticFeedbackType.LongPress); vm.pick(p.id) }
                                },
                                onRename = { renaming = p.id },
                                onMergeWithOthers = { vm.mergeWithOthers(p.id) },
                                onDelete = { vm.askDelete(listOf(p.id)) },
                            )
                        }
                    }
                }
                st.listing == PeopleViewModel.Listing.DONE || st.slow -> {
                    val failed = st.listing == PeopleViewModel.Listing.DONE
                    StateMessage(
                        art = { Icon(PeopleIcons.Problem, null, Modifier.size(40.dp), tint = colors.onSurfaceVariant) },
                        title = if (failed) "Couldn't load people" else "Still waiting for your device",
                        text = if (failed) "Check that your device is online, then try again." else "People are taking longer than usual to load.",
                        action = "Try again", onAction = { vm.reload(gallery) },
                    )
                }
                else -> Skeleton()
            }
            st.toast?.let { t ->
                Surface(
                    Modifier.align(Alignment.BottomCenter).padding(16.dp).widthIn(max = 480.dp),
                    shape = RoundedCornerShape(12.dp), tonalElevation = 6.dp, shadowElevation = 6.dp,
                    color = if (t.error) colors.errorContainer else colors.inverseSurface,
                ) {
                    Text(
                        t.text, Modifier.padding(horizontal = 16.dp, vertical = 12.dp), style = MaterialTheme.typography.bodyMedium,
                        color = if (t.error) colors.onErrorContainer else colors.inverseOnSurface,
                    )
                }
            }
        }
    }

    val naming = renaming?.let { id -> people.firstOrNull { it.id == id } }
    LaunchedEffect(renaming, naming) { if (renaming != null && naming == null && loaded) renaming = null }
    naming?.let { cur ->
        NameDialog(tidy(st.saving[cur.id] ?: cur.name), onDismiss = { renaming = null }) { typed ->
            renaming = null
            vm.rename(gallery, cur, typed)
        }
    }
    if (st.confirm == PeopleViewModel.Confirm.Merge && canMerge) {
        MergeDialog(
            selPeople, busy = st.busy, error = st.confirmError,
            onCancel = { vm.dismissConfirm() }, onConfirm = { keep, name -> vm.merge(gallery, keep, name) },
        )
    }
    if (deleting != null && deletePeople.isNotEmpty()) {
        val one = deletePeople.singleOrNull()
        ConfirmPeopleDialog(
            art = { FaceStack(deletePeople) },
            title = when {
                one == null -> "Delete ${numbers.format(deletePeople.size)} people?"
                tidy(one.name).isNotEmpty() -> "Delete ${tidy(one.name)}?"
                else -> "Delete this person?"
            },
            // What DeletePerson does: the person and every face matched to
            // them go, the photos stay, and a face in a photo added later
            // is found as someone new.
            text = "Their photos stay; only the faces matched to them are forgotten. This can't be undone, and they'll come back as new people in photos added later.",
            action = "Delete", danger = true, busy = st.busy,
            busyText = st.progress?.takeIf { it.second > 1 }?.let { (d, t) -> "${minOf(d + 1, t)} of $t" },
            error = st.confirmError,
            onCancel = { vm.dismissConfirm() }, onConfirm = { vm.remove(gallery, deletePeople.map { it.id }) },
        )
    }
}

/** What takes the page's top while faces are picked: close, the count, Merge and Delete. */
@Composable
private fun SelectionBar(count: Int, hint: Boolean, canMerge: Boolean, onClose: () -> Unit, onMerge: () -> Unit, onDelete: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    BoxWithConstraints(Modifier.fillMaxWidth().height(64.dp).background(colors.surfaceContainerHigh)) {
        // Narrow: the actions are icons (named for TalkBack).
        val compact = maxWidth < 360.dp
        Row(Modifier.fillMaxSize().padding(start = 4.dp, end = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = onClose) { Icon(PeopleIcons.Close, "Clear selection") }
            Column(Modifier.weight(1f).padding(start = 4.dp)) {
                Text("${numbers.format(count)} selected", style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (hint) Text(
                    "Select the faces of the same person", style = MaterialTheme.typography.bodySmall,
                    color = colors.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis,
                )
            }
            // Merge with one face picked: there, dimmed, and saying why when pressed.
            BarButton("Merge", PeopleIcons.Merge, compact, dimmed = !canMerge, tint = colors.onSurface, onClick = onMerge)
            BarButton("Delete", PeopleIcons.Trash, compact, tint = Danger, onClick = onDelete)
        }
    }
}

@Composable
private fun BarButton(label: String, icon: ImageVector, compact: Boolean, tint: Color, dimmed: Boolean = false, onClick: () -> Unit) {
    val a = if (dimmed) 0.45f else 1f
    val description = if (dimmed) "$label (select 2 or more to merge)" else label
    if (compact) {
        IconButton(onClick = onClick) { Icon(icon, description, Modifier.alpha(a), tint = tint) }
    } else {
        TextButton(onClick = onClick, contentPadding = PaddingValues(start = 10.dp, end = 14.dp), modifier = Modifier.semantics { contentDescription = description }) {
            Icon(icon, null, Modifier.size(22.dp).alpha(a), tint = tint)
            Spacer(Modifier.width(8.dp))
            Text(label, color = tint.copy(alpha = a), fontWeight = FontWeight.SemiBold)
        }
    }
}

/** One face: the picture, the name (or "Add a name"), the photos; the "more" menu on its corner. */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun PersonTile(
    p: Person, face: Dp, name: String, pending: Boolean, selecting: Boolean, selected: Boolean, canMerge: Boolean,
    onTap: () -> Unit, onLongPress: () -> Unit, onRename: () -> Unit, onMergeWithOthers: () -> Unit, onDelete: () -> Unit,
) {
    val colors = MaterialTheme.colorScheme
    var menu by remember { mutableStateOf(false) }
    val label = "${name.ifEmpty { "Unnamed person" }}, ${photosLabel(p.faceCount)}"
    Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.CenterHorizontally) {
        Box(Modifier.size(face)) {
            // Picked: a ring around a face that steps back a little.
            Box(
                Modifier.fillMaxSize().scale(if (selected) 0.9f else 1f)
                    .let { if (selected) it.border(3.dp, colors.primary, CircleShape).padding(5.dp) else it }
                    .clip(CircleShape)
                    .combinedClickable(onClick = onTap, onLongClick = onLongPress, onLongClickLabel = "Select")
                    .semantics {
                        contentDescription = label
                        if (selecting) this.selected = selected
                    },
            ) { FacePicture(p, face) }
            if (selecting) {
                Box(
                    Modifier.align(Alignment.TopStart).offset((-2).dp, (-2).dp).size(26.dp)
                        .background(if (selected) colors.primary else Color.Black.copy(alpha = 0.35f), CircleShape)
                        .border(2.dp, if (selected) colors.primary else Color.White.copy(alpha = 0.92f), CircleShape),
                    contentAlignment = Alignment.Center,
                ) { if (selected) Icon(PeopleIcons.Check, null, Modifier.size(16.dp), tint = colors.onPrimary) }
            } else {
                Box(Modifier.align(Alignment.TopEnd).offset(4.dp, (-4).dp)) {
                    Surface(
                        onClick = { menu = true }, shape = CircleShape, color = colors.surfaceContainerHighest,
                        shadowElevation = 3.dp, modifier = Modifier.size(32.dp).semantics { contentDescription = "More options for ${name.ifEmpty { "this person" }}" },
                    ) { Box(contentAlignment = Alignment.Center) { Icon(Icons.Default.MoreHoriz, null, Modifier.size(20.dp)) } }
                    DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                        DropdownMenuItem(
                            text = { Text(if (name.isNotEmpty()) "Rename" else "Add a name") },
                            leadingIcon = { Icon(PeopleIcons.Pencil, null) },
                            onClick = { menu = false; onRename() },
                        )
                        if (canMerge) DropdownMenuItem(
                            text = { Text("Merge with others…") },
                            leadingIcon = { Icon(PeopleIcons.Merge, null) },
                            onClick = { menu = false; onMergeWithOthers() },
                        )
                        DropdownMenuItem(
                            text = { Text("Delete", color = Danger) },
                            leadingIcon = { Icon(PeopleIcons.Trash, null, tint = Danger) },
                            onClick = { menu = false; onDelete() },
                        )
                    }
                }
            }
        }
        // Every tile is as tall whatever it shows, so naming or picking
        // someone moves nothing else.
        Box(Modifier.fillMaxWidth().height(32.dp).padding(top = 6.dp), contentAlignment = Alignment.Center) {
            when {
                name.isNotEmpty() -> Text(
                    name, style = MaterialTheme.typography.bodyMedium, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    color = if (selected) colors.primary else colors.onSurface, modifier = Modifier.alpha(if (pending) 0.55f else 1f),
                )
                selecting -> Text("Unnamed", style = MaterialTheme.typography.bodyMedium, fontStyle = FontStyle.Italic, color = colors.onSurfaceVariant)
                else -> Text(
                    "Add a name", style = MaterialTheme.typography.bodyMedium, fontStyle = FontStyle.Italic, color = colors.onSurfaceVariant,
                    modifier = Modifier.clip(RoundedCornerShape(8.dp)).clickable(role = Role.Button, onClick = onRename).padding(horizontal = 8.dp, vertical = 2.dp),
                )
            }
        }
        Text(photosLabel(p.faceCount), style = MaterialTheme.typography.bodySmall, fontSize = 12.sp, color = colors.onSurfaceVariant)
    }
}

/** A person's cover, filling a round box; a head and shoulders when there is none. */
@Composable
private fun FacePicture(p: Person, size: Dp) {
    val px = with(LocalDensity.current) { size.roundToPx() }
    // Keyed as the search's PersonPic keys a face (and cached per size).
    val bmp = rememberTileThumb(if (p.coverThumbnail.isEmpty) null else "p:${p.id}:${p.coverThumbnail.hashCode()}", px) { p.coverThumbnail.toByteArray() }
    Box(Modifier.fillMaxSize().clip(CircleShape).background(MaterialTheme.colorScheme.surfaceContainerHighest), contentAlignment = Alignment.Center) {
        if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
        else Icon(PeopleIcons.Person, null, Modifier.fillMaxSize(0.5f), tint = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/** Loading: the tiles' own shapes, pulsing. */
@Composable
private fun Skeleton() {
    val pulse by rememberInfiniteTransition(label = "pulse").animateFloat(
        0.35f, 0.8f, infiniteRepeatable(tween(700), RepeatMode.Reverse), label = "alpha",
    )
    val fill = MaterialTheme.colorScheme.surfaceContainerHighest
    LazyVerticalGrid(
        columns = GridCells.Adaptive(112.dp), contentPadding = PaddingValues(start = 12.dp, end = 12.dp, top = 16.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(20.dp), userScrollEnabled = false,
        modifier = Modifier.fillMaxSize().semantics { contentDescription = "Loading people" },
    ) {
        items(SKELETON_FACES) { i ->
            Column(Modifier.fillMaxWidth().alpha(pulse), horizontalAlignment = Alignment.CenterHorizontally) {
                Box(Modifier.size(96.dp).background(fill, CircleShape))
                Box(Modifier.padding(top = 16.dp).width(listOf(64, 52, 76)[i % 3].dp).height(12.dp).background(fill, RoundedCornerShape(6.dp)))
                Box(Modifier.padding(top = 8.dp).width(44.dp).height(8.dp).background(fill, RoundedCornerShape(4.dp)))
            }
        }
    }
}

@Composable
private fun StateMessage(art: @Composable () -> Unit, title: String, text: String, action: String? = null, onAction: () -> Unit = {}) {
    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 32.dp, vertical = 48.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        art()
        Spacer(Modifier.height(16.dp))
        Text(title, style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
        Spacer(Modifier.height(8.dp))
        Text(text, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center, modifier = Modifier.widthIn(max = 420.dp))
        if (action != null) {
            Spacer(Modifier.height(20.dp))
            Button(onClick = onAction) { Text(action) }
        }
    }
}

/** A face in a viewfinder: faces the device finds (PeopleView.tsx's EmptyArt, without the sparkles). */
@Composable
private fun EmptyArt() {
    Icon(NavIcons.People, null, Modifier.size(80.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
}

// ---- dialogs ----

/** Naming a face: an empty field takes a name off. */
@Composable
private fun NameDialog(current: String, onDismiss: () -> Unit, onSave: (String) -> Unit) {
    // Opened with the name selected, as the web's field: typing replaces it.
    var value by rememberSaveable(stateSaver = TextFieldValue.Saver) { mutableStateOf(TextFieldValue(current, TextRange(0, current.length))) }
    val focus = remember { FocusRequester() }
    val typed = tidy(value.text)
    val changed = typed != current
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(if (current.isNotEmpty()) "Rename" else "Name this person") },
        text = {
            // The field takes the focus (and the keyboard comes up) once
            // the dialog's window has it: opened from a face's menu, the
            // menu's closing window held it a moment longer, and a request
            // made before then was lost.
            val windowFocused = LocalWindowInfo.current.isWindowFocused
            var asked by remember { mutableStateOf(false) }
            LaunchedEffect(windowFocused) { if (windowFocused && !asked) { asked = true; focus.requestFocus() } }
            OTCTextField(
                value = value, onValueChange = { if (it.text.length <= MAX_NAME) value = it },
                placeholder = { Text("Name") }, singleLine = true,
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Words, autoCorrectEnabled = false, imeAction = ImeAction.Done),
                keyboardActions = KeyboardActions(onDone = { if (changed) onSave(typed) else onDismiss() }),
                modifier = Modifier.fillMaxWidth().focusRequester(focus),
            )
        },
        confirmButton = { TextButton(onClick = { onSave(typed) }, enabled = changed) { Text("Save") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

/**
 * The merge: which face to keep (preselected when the choice is plain),
 * and a name when the kept face has none and no other has one either.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun MergeDialog(people: List<Person>, busy: Boolean, error: String?, onCancel: () -> Unit, onConfirm: (Person, String) -> Unit) {
    val colors = MaterialTheme.colorScheme
    var keepId by rememberSaveable { mutableStateOf(firstKeep(people)) }
    var typed by rememberSaveable { mutableStateOf("") }
    val names = namesOf(people)
    val keep = people.firstOrNull { it.id == keepId }
    // The name the merged person gets: the kept face's own, or the only
    // name among them; failing both, what is typed.
    val carried = keep?.let { k -> tidy(k.name).ifEmpty { if (names.size == 1) names[0] else "" } } ?: ""
    val askName = keep != null && carried.isEmpty()
    val name = carried.ifEmpty { tidy(typed) }
    val dropped = if (keep != null) names.filter { fold(it) != fold(name) } else emptyList()

    ConfirmPeopleDialog(
        title = "Merge ${numbers.format(people.size)} people into one?",
        text = "Pick the face to keep: it keeps its name and picture, and gets the photos of the others. This can't be undone.",
        action = "Merge", danger = false, ready = keep != null, busy = busy, error = error,
        onCancel = onCancel, onConfirm = { keep?.let { onConfirm(it, name) } },
    ) {
        FlowRow(
            Modifier.fillMaxWidth().padding(top = 12.dp), horizontalArrangement = Arrangement.spacedBy(4.dp, Alignment.CenterHorizontally),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            people.forEach { p ->
                val chosen = p.id == keepId
                val label = tidy(p.name)
                Column(
                    Modifier.width(92.dp).clip(RoundedCornerShape(12.dp))
                        .background(if (chosen) colors.primary.copy(alpha = 0.12f) else Color.Transparent)
                        .clickable(enabled = !busy, role = Role.RadioButton) { keepId = p.id }
                        .semantics { this.selected = chosen; contentDescription = "Keep ${label.ifEmpty { "Unnamed" }}, ${photosLabel(p.faceCount)}" }
                        .padding(vertical = 8.dp, horizontal = 4.dp),
                    horizontalAlignment = Alignment.CenterHorizontally,
                ) {
                    Box(Modifier.size(64.dp)) {
                        Box(Modifier.fillMaxSize().let { if (chosen) it.border(2.dp, colors.primary, CircleShape).padding(3.dp) else it }) { FacePicture(p, 64.dp) }
                        if (chosen) Row(
                            Modifier.align(Alignment.BottomCenter).offset(y = 6.dp).background(colors.primary, RoundedCornerShape(8.dp)).padding(horizontal = 6.dp, vertical = 1.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Icon(PeopleIcons.Check, null, Modifier.size(12.dp), tint = colors.onPrimary)
                            Spacer(Modifier.width(2.dp))
                            Text("Keep", fontSize = 11.sp, fontWeight = FontWeight.SemiBold, color = colors.onPrimary)
                        }
                    }
                    Spacer(Modifier.height(10.dp))
                    Text(
                        label.ifEmpty { "Unnamed" }, style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis,
                        fontWeight = if (label.isEmpty()) FontWeight.Normal else FontWeight.SemiBold, fontStyle = if (label.isEmpty()) FontStyle.Italic else FontStyle.Normal,
                        color = if (label.isEmpty()) colors.onSurfaceVariant else colors.onSurface,
                    )
                    Text(photosLabel(p.faceCount), fontSize = 11.sp, color = colors.onSurfaceVariant, maxLines = 1)
                }
            }
        }
        val note = buildAnnotatedString {
            if (keep == null) append("They have different names, and only one can stay. Pick the face to keep.")
            else if (name.isNotEmpty() && !askName) {
                append("The merged person will be called ")
                withStyle(SpanStyle(fontWeight = FontWeight.Bold)) { append(name) }
                append(".")
            }
            if (dropped.isNotEmpty()) {
                if (length > 0) append(" ")
                append(if (dropped.size == 1) "The name " else "The names ")
                append(andList(dropped))
                append(" will be dropped.")
            }
        }
        if (note.isNotEmpty()) Text(note, style = MaterialTheme.typography.bodyMedium, color = colors.onSurfaceVariant, modifier = Modifier.padding(top = 12.dp))
        if (askName) {
            Text(
                buildAnnotatedString {
                    append("Name ")
                    withStyle(SpanStyle(color = colors.onSurfaceVariant, fontWeight = FontWeight.Normal)) { append("(optional)") }
                },
                style = MaterialTheme.typography.labelLarge, modifier = Modifier.padding(top = 16.dp, bottom = 6.dp),
            )
            OTCTextField(
                value = typed, onValueChange = { if (!busy && it.length <= MAX_NAME) typed = it },
                placeholder = { Text("Add a name") }, singleLine = true, enabled = !busy,
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Words, autoCorrectEnabled = false, imeAction = ImeAction.Done),
                keyboardActions = KeyboardActions(onDone = { if (!busy) keep?.let { onConfirm(it, name) } }),
                modifier = Modifier.fillMaxWidth(),
            )
        }
    }
}

/**
 * A question over the page: the art and title, the text, more of the
 * question under it ([content]), an error, Cancel and the action. While the
 * device is at work it can't be dismissed, and the action shows a spinner
 * ([busyText]: how far it has got).
 */
@Composable
private fun ConfirmPeopleDialog(
    title: String, text: String, action: String, danger: Boolean, busy: Boolean, error: String?,
    onCancel: () -> Unit, onConfirm: () -> Unit,
    art: (@Composable () -> Unit)? = null, ready: Boolean = true, busyText: String? = null,
    content: @Composable () -> Unit = {},
) {
    val colors = MaterialTheme.colorScheme
    AlertDialog(
        onDismissRequest = { if (!busy) onCancel() },
        icon = art,
        title = { Text(title, textAlign = TextAlign.Center) },
        text = {
            Column(Modifier.fillMaxWidth().heightIn(max = 520.dp).verticalScroll(rememberScrollState())) {
                Text(text, style = MaterialTheme.typography.bodyMedium)
                content()
                if (error != null) Text(error, color = colors.error, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.padding(top = 12.dp))
            }
        },
        confirmButton = {
            Button(
                onClick = { if (!busy && ready) onConfirm() }, enabled = ready || busy,
                colors = if (danger) ButtonDefaults.buttonColors(containerColor = Danger, contentColor = Color.White) else ButtonDefaults.buttonColors(),
            ) {
                if (busy) {
                    CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp, color = if (danger) Color.White else colors.onPrimary)
                    if (busyText != null) { Spacer(Modifier.width(8.dp)); Text(busyText) }
                } else Text(action)
            }
        },
        dismissButton = { TextButton(onClick = { if (!busy) onCancel() }, enabled = !busy) { Text("Cancel") } },
    )
}

/** The faces a deletion is about: one large, or a few overlapping with how many more there are. */
@Composable
private fun FaceStack(people: List<Person>) {
    if (people.size == 1) { Box(Modifier.size(64.dp)) { FacePicture(people[0], 64.dp) }; return }
    val shown = people.take(if (people.size > 4) 3 else 4)
    val more = people.size - shown.size
    val ring = MaterialTheme.colorScheme.surfaceContainerHigh
    Row(horizontalArrangement = Arrangement.spacedBy((-14).dp)) {
        shown.forEach { p -> Box(Modifier.size(52.dp).border(2.dp, ring, CircleShape).padding(2.dp)) { FacePicture(p, 48.dp) } }
        if (more > 0) Box(
            Modifier.size(52.dp).border(2.dp, ring, CircleShape).padding(2.dp).background(MaterialTheme.colorScheme.surfaceContainerHighest, CircleShape),
            contentAlignment = Alignment.Center,
        ) { Text("+${numbers.format(more)}", style = MaterialTheme.typography.labelLarge) }
    }
}
