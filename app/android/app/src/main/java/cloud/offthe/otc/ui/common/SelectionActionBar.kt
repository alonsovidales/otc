// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Download
import androidx.compose.material.icons.filled.MenuBook
import androidx.compose.material.icons.filled.Share
import androidx.compose.material.icons.filled.Upload
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.unit.dp

/** Which action is waiting on the device, if any. */
enum class SelectionActionTask { SHARE, DOWNLOAD }

// Port of SelectionActionBar.swift: what Images and Files both show once
// something is selected - a floating pill with icon-over-label items.
@Composable
fun SelectionActionBar(
    count: Int,
    busy: SelectionActionTask? = null,
    onShare: () -> Unit,
    onDownload: () -> Unit,
    onDelete: () -> Unit,
    onGroup: (() -> Unit)? = null,
    onUpload: (() -> Unit)? = null,
) {
    Column(Modifier.padding(horizontal = 24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        if (count > 0) {
            Surface(shape = MaterialTheme.shapes.extraLarge, tonalElevation = 3.dp, shadowElevation = 2.dp) {
                Text("$count selected", style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 12.dp, vertical = 5.dp))
            }
            Spacer(Modifier.height(6.dp))
        }
        Surface(shape = MaterialTheme.shapes.extraLarge, tonalElevation = 3.dp, shadowElevation = 4.dp) {
            Row(Modifier.padding(vertical = 6.dp, horizontal = 8.dp)) {
                if (onUpload != null) Item("Upload", Icons.Default.Upload, enabled = busy == null, onClick = onUpload, modifier = Modifier.weight(1f))
                Item("Share", Icons.Default.Share, busy = busy == SelectionActionTask.SHARE, enabled = count > 0 && busy == null, onClick = onShare, modifier = Modifier.weight(1f))
                Item("Download", Icons.Default.Download, busy = busy == SelectionActionTask.DOWNLOAD, enabled = count > 0 && busy == null, onClick = onDownload, modifier = Modifier.weight(1f))
                if (onGroup != null) Item("Group", Icons.Default.MenuBook, enabled = count > 0 && busy == null, onClick = onGroup, modifier = Modifier.weight(1f))
                Item("Delete", Icons.Default.Delete, tint = Color(0xFFE53935), enabled = count > 0 && busy == null, onClick = onDelete, modifier = Modifier.weight(1f))
            }
        }
    }
}

@Composable
private fun Item(
    title: String, icon: ImageVector, tint: Color = MaterialTheme.colorScheme.primary,
    busy: Boolean = false, enabled: Boolean, onClick: () -> Unit, modifier: Modifier = Modifier,
) {
    TextButton(onClick = onClick, enabled = enabled, modifier = modifier) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            Box(Modifier.height(22.dp), contentAlignment = Alignment.Center) {
                if (busy) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                else Icon(icon, contentDescription = title, tint = if (enabled) tint else tint.copy(alpha = 0.4f))
            }
            Text(title, style = MaterialTheme.typography.labelSmall, color = if (enabled) tint else tint.copy(alpha = 0.4f))
        }
    }
}
