// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.Check
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appSettingsFreeUpAfter
import cloud.offthe.otc.i18n.appSettingsFreeUpDays
import cloud.offthe.otc.i18n.appSettingsFreeUpToggle
import cloud.offthe.otc.sync.FreeUpSpace

/**
 * Free Up Space's switch and its days (FreeUpSpace), in Settings and on the
 * screen after a sign-in (SyncChoiceView), each in its own section with
 * android.settings.free_up_footer under them. The days stay visible while
 * it is off: the footer's "older than that" needs its time above it. As
 * FreeUpSpaceSection on iOS.
 */
@Composable
fun FreeUpSpaceControls() {
    val enabled by FreeUpSpace.enabled.collectAsState()
    val days by FreeUpSpace.days.collectAsState()
    var menu by remember { mutableStateOf(false) }
    Row(
        Modifier.fillMaxWidth().toggleable(value = enabled, onValueChange = FreeUpSpace::setEnabled, role = Role.Switch).padding(vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(S.appSettingsFreeUpToggle().resolve(), Modifier.weight(1f), style = MaterialTheme.typography.bodyLarge)
        Switch(checked = enabled, onCheckedChange = null)
    }
    val off = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.38f)
    Row(
        Modifier.fillMaxWidth().clip(RoundedCornerShape(8.dp))
            .clickable(enabled = enabled, role = Role.DropdownList) { menu = true }
            .padding(vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            S.appSettingsFreeUpAfter().resolve(), Modifier.weight(1f), style = MaterialTheme.typography.bodyLarge,
            color = if (enabled) MaterialTheme.colorScheme.onSurface else off,
        )
        Box {
            val tint = if (enabled) MaterialTheme.colorScheme.primary else off
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(S.appSettingsFreeUpDays(days).resolve(), color = tint)
                Icon(Icons.Filled.ArrowDropDown, null, tint = tint)
            }
            DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                for (choice in FreeUpSpace.dayChoices) {
                    DropdownMenuItem(
                        text = { Text(S.appSettingsFreeUpDays(choice).resolve()) },
                        trailingIcon = if (choice == days) ({ Icon(Icons.Filled.Check, null) }) else null,
                        onClick = { menu = false; FreeUpSpace.setDays(choice) },
                        modifier = Modifier.semantics { selected = choice == days },
                    )
                }
            }
        }
    }
}
