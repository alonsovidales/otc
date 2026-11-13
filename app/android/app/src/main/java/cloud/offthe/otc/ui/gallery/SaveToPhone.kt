// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.androidPhotosSaveToPhone
import cloud.offthe.otc.i18n.androidPhotosSavedToPhone
import cloud.offthe.otc.i18n.appPhotosDownloadAlready
import cloud.offthe.otc.i18n.appPhotosDownloadSaving
import cloud.offthe.otc.i18n.appPhotosDownloadTitle
import cloud.offthe.otc.i18n.appPhotosDownloadZip
import cloud.offthe.otc.i18n.commonCancel

// Save to this phone (owner, 2026-11-13): the selection bar's Download in
// Images asks how - the photos and videos saved in this phone's own library
// as photos and videos, or the one ZIP it downloaded before. They are saved
// one at a time (PhotoGalleryViewModel.saveSelectedToPhone), each as the
// viewer's Save saves its original, with a pill over the bar saying which
// one and Cancel; what the library holds already - the photo sync sent it
// from here, or Free Up Space read it here (DeletePhoneCopyModel.inLibrary)
// - and what this view model saved before are left out. The viewer's Save,
// once its photo or video is saved, shows a check mark where it showed an
// alert. The same rules as SaveToPhone.swift on iOS.

/** The pure rules, tested as iOS's SaveToPhoneTests. */
object SaveToPhone {
    /** What a saved photo or video is remembered by: its content's [hash] (an edit has another), else its [path]. */
    fun key(hash: String, path: String): String = if (hash.isNotEmpty()) hash else "path:$path"

    /** What [plan] saves, in order, and how many of the picked it leaves out as already on this phone. */
    data class Plan(val save: List<PhotoGalleryViewModel.Item>, val already: Int)

    /**
     * The picked [items] to save, in their order: the same content picked
     * twice (one hash, two paths) once; none whose hash this phone's library
     * holds ([onPhone]) or that was saved from here ([saved]: keys) - those
     * count as already there, once each. Without a hash there is nothing to
     * tell it by: always saved.
     */
    fun plan(items: List<PhotoGalleryViewModel.Item>, onPhone: Set<String>, saved: Set<String>): Plan {
        val save = mutableListOf<PhotoGalleryViewModel.Item>()
        val seen = HashSet<String>()
        var already = 0
        for (item in items) {
            val hash = item.hash
            if (hash.isNotEmpty()) {
                // Another copy of content picked already: nothing more.
                if (!seen.add(hash)) continue
                if (hash in onPhone || key(hash, item.path) in saved) {
                    already++
                    continue
                }
            }
            save += item
        }
        return Plan(save, already)
    }

    /**
     * The note once they are saved and none failed, its sentences one after
     * the other (a UiText can't join two keys): how many were saved, then
     * how many were already there - either alone. Empty when neither.
     */
    fun note(saved: Int, already: Int): List<UiText> = buildList {
        if (saved > 0) add(S.androidPhotosSavedToPhone(saved))
        if (already > 0) add(S.appPhotosDownloadAlready(already))
    }

    /** The one being saved (from 1) of how many are: the pill's "Saving 2 of 5…". */
    data class Progress(val done: Int, val total: Int)
}

/**
 * The selection bar's Download in Images: Save to this phone ([onPhone])
 * or Download as ZIP ([onZip], what it did before) for the [count] picked -
 * one above the other with Cancel, as CopiesDialog's, their labels being
 * long; in the usual colour, as nothing is deleted.
 */
@Composable
internal fun SaveToPhoneDialog(count: Int, onPhone: () -> Unit, onZip: () -> Unit, onDismiss: () -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss, title = { Text(S.appPhotosDownloadTitle(count).resolve()) },
        confirmButton = {
            Column(horizontalAlignment = Alignment.End) {
                TextButton(onClick = onPhone) { Text(S.androidPhotosSaveToPhone().resolve(), textAlign = TextAlign.End) }
                TextButton(onClick = onZip) { Text(S.appPhotosDownloadZip().resolve(), textAlign = TextAlign.End) }
                TextButton(onClick = onDismiss) { Text(S.commonCancel().resolve()) }
            }
        },
    )
}

/**
 * "Saving 2 of 5…" after a small spinner, and Cancel: where the grid's note
 * shows, while the selection is saved to this phone - in the Toast's
 * capsule. The screen stays on meanwhile, so the phone doesn't lock in the
 * middle of a run of big videos (the setup's install does the same).
 */
@Composable
internal fun SavingToPhonePill(progress: SaveToPhone.Progress, onCancel: () -> Unit, modifier: Modifier = Modifier) {
    val view = LocalView.current
    DisposableEffect(view) {
        view.keepScreenOn = true
        onDispose { view.keepScreenOn = false }
    }
    Surface(modifier, shape = MaterialTheme.shapes.extraLarge, tonalElevation = 6.dp, shadowElevation = 4.dp) {
        Row(Modifier.padding(start = 12.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp)
            Spacer(Modifier.width(10.dp))
            Text(
                S.appPhotosDownloadSaving(progress.done, progress.total).resolve(), style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.weight(1f, fill = false).semantics { liveRegion = LiveRegionMode.Polite },
            )
            TextButton(onClick = onCancel) { Text(S.commonCancel().resolve()) }
        }
    }
}
