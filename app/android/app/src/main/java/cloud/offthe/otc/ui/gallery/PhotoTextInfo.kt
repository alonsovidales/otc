// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.outlined.ContentCopy
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosViewerTextCopied
import cloud.offthe.otc.i18n.appPhotosViewerTextCopy
import cloud.offthe.otc.i18n.appPhotosViewerTextTitle
import cloud.offthe.otc.proto.FileExifInfo
import kotlinx.coroutines.delay

// Text in photos (device release 180): the device reads the text in each
// photo - signs, documents, screenshots - and Info shows it
// (FileExifInfo.text, a line of it a line there) under "Text in this
// photo", after the details and before People, as it came and selectable,
// with Copy beside the heading - "Copied" for a moment once copied. The
// search's text while it is on: SmartSearchSupport.textInPhotos; its switch:
// Settings' TextInPhotosSection. iOS's TextInPhotos.swift.

/** How long Copy says "Copied" after the last copy. */
const val PHOTO_TEXT_COPIED_MS = 2_500L

/**
 * The text Info shows, as it came - null when there is none to show: no
 * answer, a device before Text in photos (it never sets it), a photo it
 * found no text in or hasn't read yet, or Text in photos off (all "").
 */
fun photoText(info: FileExifInfo?): String? = info?.text?.takeIf { it.isNotBlank() }

/** All of [text] on the clipboard, labelled as Info's block. */
private fun copyPhotoText(context: Context, text: String) {
    val clip = context.getSystemService(ClipboardManager::class.java) ?: return
    clip.setPrimaryClip(ClipData.newPlainText(S.appPhotosViewerTextTitle().resolve(context), text))
}

/**
 * Info's "Text in this photo": the heading with Copy beside it, then the
 * text as the device read it, line by line, selectable (the system's own
 * selection and Copy). Copy says "Copied" until [PHOTO_TEXT_COPIED_MS] after
 * the last tap, and TalkBack hears it once (a polite live region). iOS's
 * PhotoTextSection.
 */
@Composable
internal fun PhotoTextSection(text: String) {
    val context = LocalContext.current
    // Bumped by each Copy, back to 0 once "Copied" has shown long enough
    // after the last; another photo's text starts with nothing copied.
    var copies by remember(text) { mutableIntStateOf(0) }
    LaunchedEffect(copies) {
        if (copies > 0) {
            delay(PHOTO_TEXT_COPIED_MS)
            copies = 0
        }
    }
    val copied = copies > 0
    val copiedWords = S.appPhotosViewerTextCopied().resolve()
    Column(Modifier.fillMaxWidth()) {
        Spacer(Modifier.height(16.dp))
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text(
                S.appPhotosViewerTextTitle().resolve(), style = MaterialTheme.typography.titleSmall,
                modifier = Modifier.weight(1f).semantics { heading() },
            )
            TextButton(onClick = { copyPhotoText(context, text); copies++ }) {
                Icon(if (copied) Icons.Filled.Check else Icons.Outlined.ContentCopy, null, Modifier.size(18.dp))
                Spacer(Modifier.width(6.dp))
                Text(if (copied) copiedWords else S.appPhotosViewerTextCopy().resolve())
            }
        }
        SelectionContainer {
            Text(text, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.fillMaxWidth())
        }
        // "Copied", said once - not "Copy" as it comes back.
        Box(Modifier.size(1.dp).semantics {
            liveRegion = LiveRegionMode.Polite
            if (copied) contentDescription = copiedWords
        })
    }
}
