// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.files

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Description
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Image
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material.icons.outlined.HideImage
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appFilesUsageEmpty
import cloud.offthe.otc.i18n.appFilesUsageFailed
import cloud.offthe.otc.i18n.appFilesUsageNeedsUpdate
import cloud.offthe.otc.i18n.appFilesUsageNote
import cloud.offthe.otc.i18n.appFilesUsageRow
import cloud.offthe.otc.i18n.appFilesUsageRowLabel
import cloud.offthe.otc.i18n.appFilesUsageShareSmall
import cloud.offthe.otc.i18n.appFilesUsageTotal
import cloud.offthe.otc.i18n.appFilesUsageVersions
import cloud.offthe.otc.i18n.appFilesInUploadOnly
import cloud.offthe.otc.i18n.appFilesUploadOnly
import cloud.offthe.otc.i18n.appShellPercent
import cloud.offthe.otc.i18n.commonLoading
import cloud.offthe.otc.i18n.commonTryAgain
import cloud.offthe.otc.net.ErrorCodes
import cloud.offthe.otc.proto.FolderUsage
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.DEVICE_UNREACHABLE
import cloud.offthe.otc.ui.common.formatBytes
import kotlin.math.roundToInt

// Issue #195: Files' Storage view - what takes the space in the open folder.
// The third view beside the list and the grid (FilesView), it asks the
// device GetFolderUsage for the folder shown while it is open, and only
// then (the list and the grid never size folders): each direct child,
// largest first as the device orders them, with a bar of its share of the
// folder, its size and that share; the folder's total and what older
// versions take of it above, and a note below on what the sizes count.
// Icons, the lock and the eye come from the folder's listing, joined by
// (path, is_dir). The same rules as iOS's FilesUsage.swift and the web's
// filesUsage.ts.

/** Files' views, kept across launches as files_view_mode ("list", "grid", "storage"). */
enum class FilesView(val stored: String) {
    LIST("list"), GRID("grid"), STORAGE("storage");

    companion object {
        /** A stored view; anything else (none yet) is the list. */
        fun parse(s: String?): FilesView = entries.firstOrNull { it.stored == s } ?: LIST
    }
}

/** What the Storage view shows: its rows' answer, or why there are none. */
sealed interface UsageState {
    /** Asked; the answer is on its way. */
    data object Loading : UsageState
    /** The device's answer for the folder shown. */
    data class Shown(val usage: FolderUsage) : UsageState
    /** A device before #195 (unknown_payload): not asked again until the next sign-in. */
    data object NeedsUpdate : UsageState
    /** No answer in time, no connection, or the device's own refusal ([text]); Try again asks again. */
    data class Failed(val text: UiText) : UsageState
}

/**
 * One row: a direct child of the folder ([path] as ListFiles lists it, a
 * folder's without its slash), its whole size - its files and their older
 * versions - and its share of the folder's (null when the folder takes
 * nothing: no bar then); [listed]: the listing's row for it, for its icon,
 * marks and opening (null for a folder holding only older versions, which
 * ListFiles doesn't list).
 */
data class UsageRow(val path: String, val name: String, val isDir: Boolean, val size: Long, val fileCount: Int, val share: Double?, val listed: FileRow?)

/** A folder as FolderUsage.path names it: with its trailing slash, "/" for the root. */
internal fun usageFolder(path: String): String = if (path.endsWith("/")) path else "$path/"

/** Whether an answer for [answered] (FolderUsage.path) is for the folder [shown]: a later one may have been opened meanwhile. */
internal fun usageFits(answered: String, shown: String): Boolean = answered == usageFolder(shown)

/** The folder's whole size: its files and their older versions, what the rows' shares are of. */
internal fun usageTotal(u: FolderUsage): Long = u.filesSize + u.versionsSize

/** [part] of [total], 0-1; null when the folder takes nothing. */
internal fun usageShare(part: Long, total: Long): Double? = if (total <= 0) null else (part.toDouble() / total).coerceIn(0.0, 1.0)

/**
 * A share as the rows say it: a whole percentage, rounded ("45%", as the
 * web's and iOS's), "under 1%" for one that takes some space but less than
 * 1%; "0%" when the folder takes nothing (for TalkBack: the row shows no
 * share then, usageRowText).
 */
internal fun usageShareText(share: Double?): UiText = when {
    share == null || share <= 0.0 -> S.appShellPercent("0")
    share < 0.01 -> S.appFilesUsageShareSmall()
    else -> S.appShellPercent("${(share * 100).roundToInt().coerceIn(1, 100)}")
}

/**
 * What a row says under its name: its size and its share ("2.9 GB · 31%"),
 * or only its size when the folder takes nothing (no share to show, as
 * the web's; no bar either).
 */
internal fun usageRowText(row: UsageRow, size: String, share: String): UiText =
    if (row.share == null) UiText.Raw(size) else S.appFilesUsageRow(size, share)

/**
 * What the listing's marks on a row say to TalkBack, as iOS reads them: a
 * folder kept out of Images, and the lock of an upload-only folder (or of
 * a file inside one). Empty without a listing's row, or marks.
 */
internal fun usageMarks(row: UsageRow): List<UiText> {
    val listed = row.listed ?: return emptyList()
    return buildList {
        if (row.isDir && listed.outOfImages) add(OutOfImagesText.STATE)
        if (listed.uploadOnly) add(if (row.isDir) S.appFilesUploadOnly() else S.appFilesInUploadOnly())
    }
}

/**
 * The answer's entries as rows, in the device's order (largest first, by
 * path, a folder before a file of the same name: never sorted again here),
 * each joined with the [listing] of [folder] by its path and whether it is
 * a folder - a file and a folder can share a name.
 */
internal fun usageRows(u: FolderUsage, listing: List<FileRow>, folder: String): List<UsageRow> {
    val base = usageFolder(folder)
    fun full(p: String) = (if (p.startsWith("/")) p else base + p).trimEnd('/').ifEmpty { "/" }
    val listed = HashMap<Pair<String, Boolean>, FileRow>()
    for (r in listing) if (r.path != "..") listed[full(r.path) to r.isDir] = r
    val total = usageTotal(u)
    return u.entriesList.map { e ->
        val path = e.path.trimEnd('/')
        val size = e.filesSize + e.versionsSize
        UsageRow(path, path.substringAfterLast('/'), e.isDir, size, e.fileCount, usageShare(size, total), listed[full(path) to e.isDir])
    }
}

/**
 * What a GetFolderUsage that brought no FolderUsage leaves to say: a device
 * before #195 (unknown_payload), the device's own words, or - no answer in
 * time, no connection, the bridge's "device unreachable" - that the space
 * couldn't be found out (with Try again either way).
 */
internal fun usageFailure(resp: RespEnvelope?): UsageState = when {
    resp == null -> UsageState.Failed(S.appFilesUsageFailed())
    resp.error && ErrorCodes.isUnknownPayload(resp.errorCode, resp.errorMessage) -> UsageState.NeedsUpdate
    resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.code == DEVICE_UNREACHABLE -> UsageState.Failed(S.appFilesUsageFailed())
    resp.error && resp.errorMessage.isNotEmpty() -> UsageState.Failed(UiText.Raw(resp.errorMessage))
    else -> UsageState.Failed(S.appFilesUsageFailed())
}

/**
 * The Storage view of [folder]: the summary, a row per child with its
 * bar (a tap opens it - a folder here, in this view; a file as the list
 * opens it), the note; or, in their place, the wait, "nothing takes
 * space", the old device's text or the failure with Try again. No
 * selection here: the list is for that.
 */
@Composable
internal fun FilesStorageView(
    usage: UsageState?, listing: List<FileRow>, folder: String, listingOf: String?, opening: String?,
    state: LazyListState, onUp: (() -> Unit)?, onOpen: (UsageRow) -> Unit, onRetry: () -> Unit, modifier: Modifier = Modifier,
) {
    val colors = MaterialTheme.colorScheme
    LazyColumn(modifier.fillMaxSize(), state = state) {
        // What the folder takes, above its rows.
        if (usage is UsageState.Shown) item(key = "summary") {
            val u = usage.usage
            Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp)) {
                Text(S.appFilesUsageTotal(formatBytes(usageTotal(u)), u.fileCount).resolve(), style = MaterialTheme.typography.titleSmall)
                if (u.versionsSize > 0) Text(
                    S.appFilesUsageVersions(formatBytes(u.versionsSize)).resolve(),
                    style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant,
                )
            }
        }
        // The way up, as the list has it: no bar, never a child.
        if (onUp != null) item(key = "..") {
            Row(
                Modifier.fillMaxWidth().clickable(onClick = onUp).padding(horizontal = 12.dp, vertical = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Icon(Icons.Default.Folder, null, Modifier.size(28.dp), tint = colors.primary)
                Spacer(Modifier.width(12.dp))
                Text("..")
            }
        }
        when (usage) {
            null, UsageState.Loading -> item(key = "state") {
                UsageMessage(S.commonLoading().resolve(), spinner = true)
            }
            UsageState.NeedsUpdate -> item(key = "state") { UsageMessage(S.appFilesUsageNeedsUpdate().resolve()) }
            is UsageState.Failed -> item(key = "state") {
                UsageMessage(usage.text.resolve()) { TextButton(onClick = onRetry) { Text(S.commonTryAgain().resolve()) } }
            }
            is UsageState.Shown -> {
                val u = usage.usage
                // The listing joins only when it is this folder's.
                val rows = usageRows(u, if (listingOf == u.path || listingOf == folder) listing else emptyList(), folder)
                if (rows.isEmpty()) item(key = "state") { UsageMessage(S.appFilesUsageEmpty().resolve()) }
                items(rows, key = { "${it.path}\u0000${it.isDir}" }) { row -> UsageRowView(row, opening = opening == row.listed?.path, onOpen = { onOpen(row) }) }
                item(key = "note") {
                    Text(
                        S.appFilesUsageNote().resolve(), style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 16.dp),
                    )
                }
            }
        }
    }
}

// The wait, nothing to show, or why there is nothing ([action]: Try again).
@Composable
private fun UsageMessage(text: String, spinner: Boolean = false, action: (@Composable () -> Unit)? = null) {
    Column(Modifier.fillMaxWidth().padding(horizontal = 24.dp, vertical = 32.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        if (spinner) { CircularProgressIndicator(Modifier.size(24.dp), strokeWidth = 2.dp); Spacer(Modifier.height(12.dp)) }
        Text(
            text, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center,
            modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite },
        )
        if (action != null) { Spacer(Modifier.height(8.dp)); action() }
    }
}

/**
 * A child: its icon (with the listing's lock and eye), its name, and under
 * it the bar of its share with "size · share" (only the size, and no bar,
 * when the folder takes nothing). TalkBack hears the row as one: "name,
 * size, share of this folder", and the marks' meaning as its state.
 */
@Composable
private fun UsageRowView(row: UsageRow, opening: Boolean, onOpen: () -> Unit) {
    val colors = MaterialTheme.colorScheme
    val size = formatBytes(row.size)
    val share = usageShareText(row.share).resolve()
    val label = S.appFilesUsageRowLabel(row.name, size, share).resolve()
    // Each its own phrase: TalkBack pauses at the comma.
    val marks = usageMarks(row).map { it.resolve() }.joinToString(", ")
    val listed = row.listed
    Row(
        Modifier.fillMaxWidth().clickable(onClick = onOpen).clearAndSetSemantics {
            contentDescription = label
            if (marks.isNotEmpty()) stateDescription = marks
            onClick { onOpen(); true }
        }.padding(horizontal = 12.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.size(28.dp), contentAlignment = Alignment.Center) {
            if (opening) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
            else Icon(
                if (row.isDir) Icons.Default.Folder else if (listed != null && listed.raw.mime.startsWith("image/")) Icons.Default.Image else Icons.Default.Description,
                null, tint = if (row.isDir) colors.primary else colors.onSurfaceVariant,
            )
        }
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(row.name, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                if (listed != null && row.isDir && listed.outOfImages) {
                    Spacer(Modifier.width(6.dp))
                    Icon(Icons.Outlined.HideImage, null, Modifier.size(16.dp), tint = colors.primary)
                }
                if (listed?.uploadOnly == true) {
                    Spacer(Modifier.width(6.dp))
                    Icon(Icons.Default.Lock, null, Modifier.size(16.dp), tint = if (row.isDir) colors.primary else colors.onSurfaceVariant)
                }
            }
            Spacer(Modifier.height(4.dp))
            // No bar when the folder takes nothing.
            row.share?.let { s ->
                Box(Modifier.fillMaxWidth().height(6.dp).clip(RoundedCornerShape(3.dp)).background(colors.surfaceContainerHighest)) {
                    Box(Modifier.fillMaxWidth(s.toFloat()).height(6.dp).clip(RoundedCornerShape(3.dp)).background(colors.primary))
                }
                Spacer(Modifier.height(4.dp))
            }
            Text(usageRowText(row, size, share).resolve(), style = MaterialTheme.typography.labelSmall, color = colors.onSurfaceVariant)
        }
    }
}
