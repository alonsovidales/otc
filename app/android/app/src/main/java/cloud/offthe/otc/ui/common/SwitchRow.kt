// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.selection.toggleable
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.TextStyle

/**
 * A row whose words and switch are one control: the whole row toggles, and
 * TalkBack reads it once - its words, then "on"/"off, switch". A Switch
 * with its own handler beside a Text was a second, nameless focus stop
 * ("On, switch"), and a clickable row with one said neither its state nor
 * its role. As SyncChoice's and FoldersView's rows.
 */
@Composable
fun SwitchRow(
    checked: Boolean,
    onChange: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    label: @Composable RowScope.() -> Unit,
) {
    Row(
        modifier.fillMaxWidth().toggleable(value = checked, enabled = enabled, role = Role.Switch, onValueChange = onChange),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        label()
        Switch(checked = checked, onCheckedChange = null, enabled = enabled)
    }
}

/** [SwitchRow] named by [label]. */
@Composable
fun SwitchRow(
    label: String,
    checked: Boolean,
    onChange: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    style: TextStyle? = null,
) = SwitchRow(checked, onChange, modifier, enabled) {
    if (style != null) Text(label, Modifier.weight(1f), style = style) else Text(label, Modifier.weight(1f))
}
