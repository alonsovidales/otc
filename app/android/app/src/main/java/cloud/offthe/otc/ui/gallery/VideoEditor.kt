// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosEditAsCopy
import cloud.offthe.otc.i18n.appPhotosEditCopiesAll
import cloud.offthe.otc.i18n.appPhotosEditCopiesOnlyThis
import cloud.offthe.otc.i18n.appPhotosEditDiscard
import cloud.offthe.otc.i18n.appPhotosEditDiscardTitle
import cloud.offthe.otc.i18n.appPhotosEditSaveTitle
import cloud.offthe.otc.i18n.appPhotosEditVideoCopiesText
import cloud.offthe.otc.i18n.appPhotosEditVideoCutNote
import cloud.offthe.otc.i18n.appPhotosEditVideoReplace
import cloud.offthe.otc.i18n.appPhotosEditVideoSaveText
import cloud.offthe.otc.i18n.appSocialTrimCantOpen
import cloud.offthe.otc.i18n.commonOk
import cloud.offthe.otc.ui.compose.TrimCut
import cloud.offthe.otc.ui.compose.TrimHandles
import cloud.offthe.otc.ui.compose.TrimRange
import cloud.offthe.otc.ui.compose.VideoTrimmerView
import kotlinx.coroutines.launch

// Video edit: the viewer's Edit on a video opens this over it - the post
// composer's trimmer (VideoTrimmerView) with Save in place of Apply - and
// Save puts the cut on the device (EditVideo) in the video's place or as a
// copy next to it, with the photo editor's questions (PhotoEditor.kt). The
// device cuts the stored video without re-encoding it; this only plays
// what the viewer already plays (its stream URL, or the copy it fetched
// whole) to choose the start and the end. The rules are VideoEdit's
// (PhotoEdit.kt). As iOS's and the web's VideoEditor.

/**
 * The cut screen over the viewer, for [item] (the open video): the
 * trimmer's video, handles, "Keeping … of …" and Play cut, the note that
 * the cut may start a little early, Cancel, and Save - enabled for a real
 * cut of at least half a second, "Saving…" and everything waiting while
 * the device cuts. Save asks whether to replace the video or save a copy;
 * Replace asks the device how many paths hold the video first, and with
 * more than one whether to replace them all. Leaving with a cut chosen
 * asks first. Shown while State.editing.
 */
@Composable
internal fun VideoEditor(vm: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State, item: PhotoGalleryViewModel.Item) {
    // The handles so far - kept if the screen is made again.
    val handles = rememberSaveable(item.id, saver = TrimHandles.Saver) { TrimHandles() }
    // The cut Save was pressed with; then its question, Replace's about
    // the copies (how many), and Cancel's with a cut chosen.
    var cut by remember { mutableStateOf<TrimRange?>(null) }
    var saveAsk by remember { mutableStateOf(false) }
    var copiesAsk by remember { mutableStateOf<Int?>(null) }
    var discardAsk by remember { mutableStateOf(false) }
    // Replace's GetEditState on its way: the screen waits, as it does for the save.
    var asking by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val saving = asking || st.editBusy == EditBusy.SAVING

    fun leave() {
        if (saving) return
        if (VideoEdit.changed(handles.start, handles.end, handles.length)) discardAsk = true else vm.closeEditor()
    }
    fun save(asCopy: Boolean, everyCopy: Boolean = false) {
        val c = cut ?: return
        vm.saveCut(c.start, c.end, handles.length, asCopy, everyCopy)
    }
    fun replace() {
        asking = true
        scope.launch {
            val copies = try { vm.openCopies() } finally { asking = false }
            if (copies > 1) copiesAsk = copies else save(asCopy = false)
        }
    }

    VideoTrimmerView(
        // What the viewer plays: none while it is on its way, nor when the
        // viewer couldn't play it - then the trimmer says so.
        uri = st.videoUrl, existing = null,
        onApply = { r -> if (r != null && !saving) { cut = r; saveAsk = true } },
        onCancel = { leave() },
        handles = handles,
        cut = TrimCut(
            saving, S.appPhotosEditVideoCutNote(),
            failed = if (item.path in st.unplayable) S.appSocialTrimCantOpen() else null,
        ) {
            if (saveAsk) ChoiceDialog(
                S.appPhotosEditSaveTitle().resolve(), S.appPhotosEditVideoSaveText().resolve(),
                listOf(
                    S.appPhotosEditVideoReplace().resolve() to { saveAsk = false; replace() },
                    S.appPhotosEditAsCopy().resolve() to { saveAsk = false; save(asCopy = true) },
                ),
                onDismiss = { saveAsk = false },
            )
            copiesAsk?.let { n ->
                ChoiceDialog(
                    S.appPhotosEditSaveTitle().resolve(), S.appPhotosEditVideoCopiesText(n).resolve(),
                    listOf(
                        S.appPhotosEditCopiesAll(n).resolve() to { copiesAsk = null; save(asCopy = false, everyCopy = true) },
                        S.appPhotosEditCopiesOnlyThis().resolve() to { copiesAsk = null; save(asCopy = false) },
                    ),
                    onDismiss = { copiesAsk = null },
                )
            }
            if (discardAsk) ConfirmDialog(
                S.appPhotosEditDiscardTitle().resolve(), null, S.appPhotosEditDiscard().resolve(),
                onConfirm = { discardAsk = false; vm.closeEditor() }, onDismiss = { discardAsk = false },
            )
            // Save didn't go through: why, and the screen stays as it was, to try again.
            st.editError?.let { e ->
                AlertDialog(
                    onDismissRequest = { vm.editErrorShown() }, text = { Text(e.resolve()) },
                    confirmButton = { TextButton(onClick = { vm.editErrorShown() }) { Text(S.commonOk().resolve()) } },
                )
            }
        },
    )
}
