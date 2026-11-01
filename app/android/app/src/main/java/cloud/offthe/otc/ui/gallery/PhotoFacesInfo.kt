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
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
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
import cloud.offthe.otc.i18n.appShellNotThisPerson
import cloud.offthe.otc.i18n.appShellSearchUnnamed
import cloud.offthe.otc.i18n.appShellSectionPeople
import cloud.offthe.otc.proto.Person
import cloud.offthe.otc.proto.PhotoFace
import cloud.offthe.otc.proto.PhotoFaces
import cloud.offthe.otc.ui.common.rememberTileThumb
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

/** Info's People for the photo at [path]: its faces as the device listed them, and its content [hash] (RemoveFromPerson's). */
data class PhotoFacesInfo(val path: String, val hash: String, val faces: List<PhotoFace>) {
    /** Without [personId]'s faces (both, when they are twice in the photo); null when none is left. */
    fun without(personId: String): PhotoFacesInfo? = faces.filter { it.personId != personId }.takeIf { it.isNotEmpty() }?.let { copy(faces = it) }
}

/** Whether Info asks for [item]'s faces: a photo, not a video. */
internal fun asksPhotoFaces(item: PhotoGalleryViewModel.Item?): Boolean = item != null && !item.mime.startsWith("video/")

/** What Info's People shows for the photo at [path] after GetPhotoFaces' [answer]: its faces, or nothing (face recognition off, none found). */
internal fun photoFacesOf(path: String, answer: PhotoFaces): PhotoFacesInfo? =
    if (!answer.faceRecognition || answer.facesCount == 0) null else PhotoFacesInfo(path, answer.hash, answer.facesList)

/** [face]'s person, for Not this person's question and note (notThisPersonTitle, personTakenOffNote). */
internal fun facePerson(face: PhotoFace): Person = Person.newBuilder().setId(face.personId).setName(face.name).build()

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
 * Info's People: under the details, the photo's faces, one row each - the
 * crop, the name (or "Unnamed"), and Not this person's ×. [busy]: a change
 * is on its way (the × waits). [onShow] opens the person's photos;
 * [onRemove] asks first (ImageModal).
 */
@Composable
internal fun PhotoFacesSection(info: PhotoFacesInfo, busy: Boolean, onShow: (PhotoFace) -> Unit, onRemove: (PhotoFace) -> Unit) {
    Column(Modifier.fillMaxWidth()) {
        Spacer(Modifier.height(16.dp))
        Text(S.appShellSectionPeople().resolve(), style = MaterialTheme.typography.titleSmall, modifier = Modifier.semantics { heading() })
        Spacer(Modifier.height(4.dp))
        for (face in info.faces) PhotoFaceRow(face, busy, onShow = { onShow(face) }, onRemove = { onRemove(face) })
    }
}

/**
 * One face: its crop and name, a button that opens their photos (TalkBack
 * hears the name), then the × (48 dp, "Not this person"). A long name
 * wraps onto a second line at large text rather than squeezing the ×.
 */
@Composable
private fun PhotoFaceRow(face: PhotoFace, busy: Boolean, onShow: () -> Unit, onRemove: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    val name = personName(facePerson(face))
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
            Icon(Icons.Default.Close, S.appShellNotThisPerson().resolve(), tint = if (busy) colors.onSurfaceVariant.copy(alpha = 0.38f) else colors.onSurfaceVariant)
        }
    }
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
