// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosPeopleFaceLabel
import cloud.offthe.otc.i18n.appPhotosPeopleFaceLabelUnnamed
import cloud.offthe.otc.i18n.appPhotosPeoplePhotoCount
import cloud.offthe.otc.i18n.appPhotosPersonMoveNone
import cloud.offthe.otc.i18n.appPhotosPersonMoveTitle
import cloud.offthe.otc.i18n.appShellSearchClear
import cloud.offthe.otc.i18n.appShellSearchUnnamed
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonSearch
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.ui.common.OTCTextField
import java.text.Normalizer

// "Someone else…" (Images, while one person's photos show: the selection
// bar, and the viewer's menu): the other people face recognition found, to
// file the photos under the right one - each face with their name (the
// unnamed as "Unnamed") and photos, in the People page's order (named
// first, most photos first), with a field that filters them by name once
// the list is long. Picking one sends RemoveFromPerson with to_person_id
// (PhotoGalleryViewModel.takeOffPerson). The list is the one the gallery
// holds (ListPeople, with each person's cover), asked for again as the
// sheet opens. Info's Add a person (PhotoFacesInfo.kt) is the same sheet
// (PersonPickSheet) over everyone not in the photo or video yet.

/** Past how many people in the list a field filters it by name (iOS PersonMove.filterAbove). */
internal const val NAME_FILTER_ABOVE = 8

/** Whether a list of [count] people gets the field that filters it by name. */
internal fun wantsNameFilter(count: Int): Boolean = count > NAME_FILTER_ABOVE

/** A name as it is matched: spaces tidied, lower case, accents gone ("José" is "jose"). */
internal fun foldName(s: String): String =
    Normalizer.normalize(s.replace(Regex("\\s+"), " ").trim().lowercase(), Normalizer.Form.NFD).replace(Regex("\\p{Mn}+"), "")

/**
 * Those of [people] whose name holds [query], ignoring case, accents and
 * extra spaces - the unnamed then go; all of them, in their order, with
 * nothing typed.
 */
internal fun withName(people: List<Person>, query: String): List<Person> {
    val q = foldName(query)
    if (q.isEmpty()) return people
    return people.filter { foldName(it.name).contains(q) }
}

/**
 * Someone else…'s list: everyone in [all] but [current], in the People
 * page's order (peopleOrder); with a [query] typed, those whose name holds
 * it (withName).
 */
internal fun movePeople(all: List<Person>, current: String, query: String): List<Person> =
    withName(peopleOrder(all, setOf(current)), query)

/**
 * The sheet: [count] photos to move ("Move 3 photos to…", "Move this
 * photo to…") to one of [people] other than [current] - a spinner until
 * the list has come ([loaded]), "No one else has been found yet." when
 * nobody else is in it. [onPick] closes it and moves them.
 */
@Composable
internal fun MoveToPersonSheet(
    people: List<Person>, loaded: Boolean, current: String, count: Int,
    onPick: (Person) -> Unit, onDismiss: () -> Unit,
) {
    val others = remember(people, current) { movePeople(people, current, "") }
    PersonPickSheet(
        S.appPhotosPersonMoveTitle(count).resolve(), others, loaded,
        none = S.appPhotosPersonMoveNone().resolve(), find = S.commonSearch().resolve(),
        onPick = onPick, onDismiss = onDismiss,
    )
}

/**
 * A list of people to pick one from (Someone else…, Add a person): [title]
 * over [offered], in the order given - a spinner until the people have
 * come ([loaded]), [none] when nobody is offered, and with more than
 * NAME_FILTER_ABOVE a field ([find]: its placeholder and TalkBack name)
 * that filters them by name, [noMatch] when that leaves no one. [busy]: a
 * change is on its way (a bar under the title; the rows wait); [error]:
 * what came of the last pick, said under the title. [onCancel]: a Cancel
 * under the list (without it the sheet goes by a swipe, Back or a tap
 * outside, as ever).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun PersonPickSheet(
    title: String, offered: List<Person>, loaded: Boolean, none: String, find: String,
    noMatch: String? = null, busy: Boolean = false, error: String? = null,
    onPick: (Person) -> Unit, onDismiss: () -> Unit, onCancel: (() -> Unit)? = null,
) {
    val colors = MaterialTheme.colorScheme
    var query by rememberSaveable { mutableStateOf("") }
    val shown = remember(offered, query) { withName(offered, query) }
    ModalBottomSheet(onDismissRequest = onDismiss, sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)) {
        Column(Modifier.fillMaxWidth().imePadding()) {
            Text(
                title, style = MaterialTheme.typography.titleMedium,
                modifier = Modifier.padding(start = 24.dp, end = 24.dp, bottom = 12.dp).semantics { heading() },
            )
            if (busy) LinearProgressIndicator(Modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, bottom = 8.dp))
            if (error != null) Text(
                error, style = MaterialTheme.typography.bodyMedium, color = colors.error,
                modifier = Modifier.padding(start = 24.dp, end = 24.dp, bottom = 8.dp).semantics { liveRegion = LiveRegionMode.Polite },
            )
            if (wantsNameFilter(offered.size)) {
                OTCTextField(
                    value = query, onValueChange = { query = it },
                    modifier = Modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, bottom = 8.dp).semantics { contentDescription = find },
                    placeholder = { Text(find) }, singleLine = true,
                    keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Words, autoCorrectEnabled = false, imeAction = ImeAction.Search),
                    trailingIcon = if (query.isEmpty()) null else {
                        { IconButton(onClick = { query = "" }, Modifier.size(36.dp)) { Icon(Icons.Default.Close, S.appShellSearchClear().resolve(), Modifier.size(18.dp)) } }
                    },
                )
            }
            when {
                offered.isEmpty() && !loaded -> Box(Modifier.fillMaxWidth().padding(32.dp), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
                offered.isEmpty() -> PickNote(none)
                shown.isEmpty() && noMatch != null -> PickNote(noMatch)
                else -> LazyColumn(Modifier.fillMaxWidth().weight(1f, fill = false), contentPadding = PaddingValues(bottom = if (onCancel == null) 16.dp else 0.dp)) {
                    items(shown, key = { it.id }) { p -> PersonPickRow(p, enabled = !busy) { onPick(p) } }
                }
            }
            if (onCancel != null) TextButton(onClick = onCancel, Modifier.align(Alignment.End).padding(end = 12.dp, bottom = 8.dp)) {
                Text(S.commonCancel().resolve())
            }
        }
    }
}

/** What the sheet says in the list's place: nobody to offer, or no name that matches. */
@Composable
private fun PickNote(text: String) {
    Text(
        text, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 8.dp, bottom = 32.dp),
    )
}

/** One person to pick: their face, name and photos; TalkBack hears the People page's name for the face. [enabled]: false while a pick is on its way. */
@Composable
private fun PersonPickRow(p: Person, enabled: Boolean, onPick: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    val name = personName(p)
    val label = (if (name.isEmpty()) S.appPhotosPeopleFaceLabelUnnamed(p.faceCount) else S.appPhotosPeopleFaceLabel(name, p.faceCount)).resolve()
    Row(
        Modifier.fillMaxWidth().clickable(enabled = enabled, role = Role.Button, onClick = onPick).semantics { contentDescription = label }
            .padding(horizontal = 24.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.size(48.dp).clearAndSetSemantics {}) { FacePicture(p, 48.dp) }
        Spacer(Modifier.width(16.dp))
        Column(Modifier.weight(1f).clearAndSetSemantics {}) {
            if (name.isNotEmpty()) Text(name, style = MaterialTheme.typography.bodyLarge, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
            else Text(S.appShellSearchUnnamed().resolve(), style = MaterialTheme.typography.bodyLarge, fontStyle = FontStyle.Italic, color = colors.onSurfaceVariant, maxLines = 1)
            Text(S.appPhotosPeoplePhotoCount(p.faceCount).resolve(), style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant, maxLines = 1)
        }
    }
}
