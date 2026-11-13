// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import java.security.MessageDigest

/**
 * The little capsule the iOS screens show at the top for a couple of
 * seconds. A polite live region: TalkBack never moves to it, so without
 * that a refusal (a folder inside one kept out of Images or upload only)
 * looked to its users as if nothing happened. iOS announces its toasts.
 * [icon]: a mark before the text (a check mark once something is done),
 * for the eye only - the text says it. [onTap]: what a tap on it does
 * (hides it early).
 */
@Composable
fun Toast(message: String?, modifier: Modifier = Modifier, icon: ImageVector? = null, onTap: (() -> Unit)? = null) {
    AnimatedVisibility(visible = message != null, modifier = modifier.padding(top = 8.dp)) {
        val tap = if (onTap == null) Modifier else Modifier.pointerInput(onTap) { detectTapGestures { onTap() } }
        Surface(tap, shape = MaterialTheme.shapes.extraLarge, tonalElevation = 6.dp, shadowElevation = 4.dp) {
            Row(Modifier.padding(horizontal = 12.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                if (icon != null) {
                    Icon(icon, null, Modifier.size(18.dp), tint = MaterialTheme.colorScheme.primary)
                    Spacer(Modifier.width(8.dp))
                }
                Text(message ?: "", style = MaterialTheme.typography.bodyMedium, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
            }
        }
    }
}

fun sha256Hex(data: ByteArray): String =
    MessageDigest.getInstance("SHA-256").digest(data).joinToString("") { "%02x".format(it) }
