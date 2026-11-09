// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.outlined.PersonAdd
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appPhotosPersonRemovedVideo
import cloud.offthe.otc.i18n.appPhotosPersonRemovedVideoUnnamed
import cloud.offthe.otc.i18n.appPhotosViewerFaceAdd
import cloud.offthe.otc.i18n.appPhotosViewerFaceAddAll
import cloud.offthe.otc.i18n.appPhotosViewerFaceAddFailed
import cloud.offthe.otc.i18n.appPhotosViewerFaceAddFind
import cloud.offthe.otc.i18n.appPhotosViewerFaceAddNoMatch
import cloud.offthe.otc.i18n.appPhotosViewerFaceAddNone
import cloud.offthe.otc.i18n.appPhotosViewerFaceAddTitlePhoto
import cloud.offthe.otc.i18n.appPhotosViewerFaceAddTitleVideo
import cloud.offthe.otc.i18n.appShellNotThisPerson
import cloud.offthe.otc.i18n.appShellSearchRemove
import cloud.offthe.otc.i18n.appShellSearchRemovePerson
import cloud.offthe.otc.i18n.appShellSearchUnnamed
import cloud.offthe.otc.i18n.appShellSectionPeople
import cloud.offthe.otc.proto.AddToPerson
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.PhotoFace
import cloud.offthe.otc.proto.PhotoFaces
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.rememberTileThumb
import cloud.offthe.otc.ui.settings.deviceOr
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

// A photo's faces in the viewer's Info (owner, 2026-11-01, from a user:
// "see what face it has, especially on new images ... and be able to
// remove faces from there"): GetPhotoFaces lists the faces found in the
// photo open, left to right, each with its person and crop, and Info shows
// them under People - not for a video, nor while face recognition is off,
// nor on a device from before (unknown_payload: nothing, quietly, until
// the next sign-in). A face opens its person's photos in Images; its ×
// asks Not this person's question and sends RemoveFromPerson with the
// photo's hash (every face of theirs in it). PhotoGalleryViewModel asks
// (openInfo, removeInfoFace); ImageModal shows it.
//
// People added by hand (owner, 2026-11-09: "add people to the photos and
// videos by hand ... in the faces we will have an add button"): Info asks
// for a video too (a device from before answers no one), lists them after
// the faces (PhotoFace.by_hand: no box, their People face as the crop),
// and, where the device takes AddToPerson (PhotoFaces.adds_people), ends
// with Add a person - the people People lists but those in it already.
// Picking one sends AddToPerson with the content's hash and asks for its
// people again (addInfoPerson). A by-hand row's × takes them off at once:
// Not this person's question is about face recognition, and adding them
// back is a tap away.

/**
 * Info's People for the photo or video at [path]: its faces and the people
 * added to it by hand, as the device listed them; its content [hash]
 * (RemoveFromPerson's and AddToPerson's); [addsPeople]: the device takes
 * AddToPerson (PhotoFaces.adds_people).
 */
data class PhotoFacesInfo(val path: String, val hash: String, val faces: List<PhotoFace>, val addsPeople: Boolean = false) {
    /** Whether People ends with Add a person: the device takes it, and there is a hash to name the content by. */
    val adds: Boolean get() = addsPeople && hash.isNotEmpty()

    /** Whether there is anything to show: someone in it, or Add. */
    val shows: Boolean get() = faces.isNotEmpty() || adds

    /** Without [personId] (both faces when they are twice in it, and by hand); null when nothing is left to show. */
    fun without(personId: String): PhotoFacesInfo? = copy(faces = faces.filter { it.personId != personId }).takeIf { it.shows }
}

/** Whether Info asks for [item]'s people: a photo, or a video (people added by hand; a device from before answers none). */
internal fun asksPhotoFaces(item: PhotoGalleryViewModel.Item?): Boolean = item != null

/**
 * What Info's People shows for [path] after GetPhotoFaces' [answer]: its
 * faces and the people added by hand, with Add where the device takes it -
 * or nothing (face recognition off; no one in it on a device that doesn't
 * add people).
 */
internal fun photoFacesOf(path: String, answer: PhotoFaces): PhotoFacesInfo? =
    if (!answer.faceRecognition) null else PhotoFacesInfo(path, answer.hash, answer.facesList, answer.addsPeople).takeIf { it.shows }

/**
 * Add a person's list for [info]: everyone in [all] but those in it already
 * (a face of theirs found, or added before), in the People page's order;
 * with a [query] typed, those whose name holds it (withName).
 */
internal fun peopleToAdd(all: List<Person>, info: PhotoFacesInfo, query: String = ""): List<Person> =
    withName(peopleOrder(all, info.faces.mapTo(HashSet()) { it.personId }), query)

/** What Add a person's list says when it offers no one: nobody found yet, or everyone in it already. */
internal fun addPersonNone(all: List<Person>): UiText =
    if (all.isEmpty()) S.appPhotosViewerFaceAddNone() else S.appPhotosViewerFaceAddAll()

/** AddToPerson: [personId] put in the contents with [hashes] by hand. */
internal fun addToPersonRequest(personId: String, hashes: List<String>): AddToPerson =
    AddToPerson.newBuilder().setPersonId(personId).addAllHashes(hashes).build()

/**
 * What an AddToPerson answer means: null when the device took it, else
 * what the list says - the device's words (a refusal, in the request's
 * language; the demo device's), else face_add_failed (no answer, a device
 * from before - which never offers Add -, or none in words).
 */
internal fun addToPersonRefusal(resp: RespEnvelope?): UiText? = when {
    resp == null || resp.isUnknownPayload() -> S.appPhotosViewerFaceAddFailed()
    resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.ok -> null
    resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK -> deviceOr(resp.respAck.errorMsg, S.appPhotosViewerFaceAddFailed())
    resp.error -> deviceOr(resp.errorMessage, S.appPhotosViewerFaceAddFailed())
    else -> S.appPhotosViewerFaceAddFailed()
}

/** Add a person's list, open for the photo or video at [path]; [error]: what the device said of the last pick there (null: nothing yet). */
data class InfoAdd(val path: String, val error: UiText? = null)

/** [face]'s person, for Not this person's question and note (notThisPersonTitle, personTakenOffNote). */
internal fun facePerson(face: PhotoFace): Person = Person.newBuilder().setId(face.personId).setName(face.name).build()

/**
 * The note once [face]'s person was taken off the item Info shows: a
 * video's says so (someone added by hand - no face is looked for in one),
 * a photo's is Not this person's for one photo.
 */
internal fun infoTakenOffNote(face: PhotoFace, video: Boolean): UiText {
    if (!video) return personTakenOffNote(1, facePerson(face), null)
    val name = personName(facePerson(face))
    return if (name.isEmpty()) S.appPhotosPersonRemovedVideoUnnamed() else S.appPhotosPersonRemovedVideo(name)
}

/**
 * A person's photos asked for by a face in a viewer's Info - Images' own,
 * or Files', the map's or a memory's (each a viewer of its own): MainView
 * shows Images, whose gallery takes it (PhotoGalleryView) and shows them
 * as a face opened from People does.
 */
object PersonNav {
    private val _pending = MutableStateFlow<String?>(null)
    /** The person asked for and not yet shown, if any. */
    val pending: StateFlow<String?> = _pending

    fun show(personId: String) { _pending.value = personId }

    /** Images has it: it isn't done again when Images shows next time. */
    fun take(personId: String) { _pending.compareAndSet(personId, null) }

    /** Log Out: another device may come next. */
    fun reset() { _pending.value = null }
}

/**
 * Info's People: under the details, the photo's faces, then the people
 * added by hand, one row each - the crop, the name (or "Unnamed"), and the
 * × - and Add a person where the device takes it ([PhotoFacesInfo.adds]).
 * [busy]: a change is on its way (the × waits). [onShow] opens the
 * person's photos; [onRemove] asks first for a face (ImageModal); [onAdd]
 * opens Add a person's list.
 */
@Composable
internal fun PhotoFacesSection(info: PhotoFacesInfo, busy: Boolean, onShow: (PhotoFace) -> Unit, onRemove: (PhotoFace) -> Unit, onAdd: () -> Unit) {
    Column(Modifier.fillMaxWidth()) {
        Spacer(Modifier.height(16.dp))
        Text(S.appShellSectionPeople().resolve(), style = MaterialTheme.typography.titleSmall, modifier = Modifier.semantics { heading() })
        Spacer(Modifier.height(4.dp))
        for (face in info.faces) PhotoFaceRow(face, busy, onShow = { onShow(face) }, onRemove = { onRemove(face) })
        if (info.adds) AddPersonRow(onAdd)
    }
}

/**
 * One face, or one person added by hand: the crop and name, a button that
 * opens their photos (TalkBack hears the name), then the × (48 dp, "Not
 * this person" - "Remove Ana" for one added by hand, who goes without a
 * question). A long name wraps onto a second line at large text rather
 * than squeezing the ×.
 */
@Composable
private fun PhotoFaceRow(face: PhotoFace, busy: Boolean, onShow: () -> Unit, onRemove: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    val name = personName(facePerson(face))
    val removeLabel = when {
        !face.byHand -> S.appShellNotThisPerson()
        name.isEmpty() -> S.appShellSearchRemovePerson()
        else -> S.appShellSearchRemove(name)
    }.resolve()
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        Row(
            Modifier.weight(1f).heightIn(min = 48.dp).clip(RoundedCornerShape(8.dp)).clickable(role = Role.Button, onClick = onShow).padding(vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            PhotoFacePicture(face, 36.dp)
            Spacer(Modifier.width(12.dp))
            if (name.isNotEmpty()) Text(name, style = MaterialTheme.typography.bodyLarge, maxLines = 2, overflow = TextOverflow.Ellipsis)
            else Text(S.appShellSearchUnnamed().resolve(), style = MaterialTheme.typography.bodyLarge, fontStyle = FontStyle.Italic, color = colors.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        IconButton(onClick = onRemove, enabled = !busy) {
            Icon(Icons.Default.Close, removeLabel, tint = if (busy) colors.onSurfaceVariant.copy(alpha = 0.38f) else colors.onSurfaceVariant)
        }
    }
}

/**
 * Add a person, at People's end: a row as tall as a face's (48 dp at the
 * least), the icon in a round box where a face's crop is; TalkBack hears
 * its words.
 */
@Composable
private fun AddPersonRow(onAdd: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    Row(
        Modifier.fillMaxWidth().heightIn(min = 48.dp).clip(RoundedCornerShape(8.dp)).clickable(role = Role.Button, onClick = onAdd).padding(vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.size(36.dp).clip(CircleShape).background(colors.surfaceContainerHighest), contentAlignment = Alignment.Center) {
            Icon(Icons.Outlined.PersonAdd, null, Modifier.size(20.dp), tint = colors.primary)
        }
        Spacer(Modifier.width(12.dp))
        Text(S.appPhotosViewerFaceAdd().resolve(), style = MaterialTheme.typography.bodyLarge, color = colors.primary, maxLines = 2, overflow = TextOverflow.Ellipsis)
    }
}

/**
 * Add a person's list, over Info (PersonPickSheet, as Someone else…'s):
 * "Who is in this photo?" ([video]: "…video?"), everyone in [people] but
 * those in [info] already, with a field to find a name in a long list.
 * [busy]: a change is on its way (the pick waits); [error]: the device's
 * answer to the last pick. Picking sends AddToPerson ([onPick]); the
 * list closes once the device took it.
 */
@Composable
internal fun AddPersonSheet(
    people: List<Person>, loaded: Boolean, info: PhotoFacesInfo, video: Boolean, busy: Boolean, error: UiText?,
    onPick: (Person) -> Unit, onDismiss: () -> Unit,
) {
    val offered = remember(people, info) { peopleToAdd(people, info) }
    PersonPickSheet(
        (if (video) S.appPhotosViewerFaceAddTitleVideo() else S.appPhotosViewerFaceAddTitlePhoto()).resolve(), offered, loaded,
        none = addPersonNone(people).resolve(), find = S.appPhotosViewerFaceAddFind().resolve(), noMatch = S.appPhotosViewerFaceAddNoMatch().resolve(),
        busy = busy, error = error?.resolve(), onPick = onPick, onDismiss = onDismiss, onCancel = onDismiss,
    )
}

/** A face's crop in a round box [size] wide; a blank circle when the device sent none. */
@Composable
private fun PhotoFacePicture(face: PhotoFace, size: Dp) {
    val px = with(LocalDensity.current) { size.roundToPx() }
    // Keyed by the crop itself, as People's faces are by their cover (FacePicture).
    val bmp = rememberTileThumb(if (face.thumbnail.isEmpty) null else "f:${face.personId}:${face.thumbnail.hashCode()}", px) { face.thumbnail.toByteArray() }
    Box(Modifier.size(size).clip(CircleShape).background(MaterialTheme.colorScheme.surfaceContainerHighest)) {
        if (bmp != null) Image(bmp.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
    }
}
