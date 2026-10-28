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
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
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
// sheet opens.

/** From how many people in the list a field filters it by name. */
internal const val MOVE_FILTER_FROM = 8

/** A name as it is matched: spaces tidied, lower case, accents gone ("José" is "jose"). */
internal fun foldName(s: String): String =
    Normalizer.normalize(s.replace(Regex("\\s+"), " ").trim().lowercase(), Normalizer.Form.NFD).replace(Regex("\\p{Mn}+"), "")

/**
 * Someone else…'s list: everyone in [all] but [current], in the People
 * page's order (peopleOrder); with a [query] typed, those whose name holds
 * it, ignoring case, accents and extra spaces - the unnamed then go.
 */
internal fun movePeople(all: List<Person>, current: String, query: String): List<Person> {
    val others = peopleOrder(all, setOf(current))
    val q = foldName(query)
    if (q.isEmpty()) return others
    return others.filter { foldName(it.name).contains(q) }
}

/**
 * The sheet: [count] photos to move ("Move 3 photos to…", "Move this
 * photo to…") to one of [people] other than [current] - a spinner until
 * the list has come ([loaded]), "No one else has been found yet." when
 * nobody else is in it. [onPick] closes it and moves them.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun MoveToPersonSheet(
    people: List<Person>, loaded: Boolean, current: String, count: Int,
    onPick: (Person) -> Unit, onDismiss: () -> Unit,
) {
    val colors = MaterialTheme.colorScheme
    var query by rememberSaveable { mutableStateOf("") }
    val others = remember(people, current) { movePeople(people, current, "") }
    val shown = remember(people, current, query) { movePeople(people, current, query) }
    ModalBottomSheet(onDismissRequest = onDismiss, sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)) {
        Column(Modifier.fillMaxWidth().imePadding()) {
            Text(
                S.appPhotosPersonMoveTitle(count).resolve(), style = MaterialTheme.typography.titleMedium,
                modifier = Modifier.padding(start = 24.dp, end = 24.dp, bottom = 12.dp).semantics { heading() },
            )
            if (others.size >= MOVE_FILTER_FROM) {
                OTCTextField(
                    value = query, onValueChange = { query = it },
                    modifier = Modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, bottom = 8.dp),
                    placeholder = { Text(S.commonSearch().resolve()) }, singleLine = true,
                    keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Words, autoCorrectEnabled = false, imeAction = ImeAction.Search),
                    trailingIcon = if (query.isEmpty()) null else {
                        { IconButton(onClick = { query = "" }, Modifier.size(36.dp)) { Icon(Icons.Default.Close, S.appShellSearchClear().resolve(), Modifier.size(18.dp)) } }
                    },
                )
            }
            when {
                others.isEmpty() && !loaded -> Box(Modifier.fillMaxWidth().padding(32.dp), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
                others.isEmpty() -> Text(
                    S.appPhotosPersonMoveNone().resolve(), style = MaterialTheme.typography.bodyMedium, color = colors.onSurfaceVariant,
                    modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 8.dp, bottom = 32.dp),
                )
                else -> LazyColumn(Modifier.fillMaxWidth(), contentPadding = PaddingValues(bottom = 16.dp)) {
                    items(shown, key = { it.id }) { p -> MovePersonRow(p) { onPick(p) } }
                }
            }
        }
    }
}

/** One person to move the photos to: their face, name and photos; TalkBack hears the People page's name for the face. */
@Composable
private fun MovePersonRow(p: Person, onPick: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    val name = personName(p)
    val label = (if (name.isEmpty()) S.appPhotosPeopleFaceLabelUnnamed(p.faceCount) else S.appPhotosPeopleFaceLabel(name, p.faceCount)).resolve()
    Row(
        Modifier.fillMaxWidth().clickable(role = Role.Button, onClick = onPick).semantics { contentDescription = label }
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
