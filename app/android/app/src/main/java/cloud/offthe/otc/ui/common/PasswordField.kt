// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.foundation.layout.size
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Visibility
import androidx.compose.material.icons.filled.VisibilityOff
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appShellPasswordHide
import cloud.offthe.otc.i18n.appShellPasswordShow

/**
 * A password field with an eye button that shows or hides what is typed
 * (app.shell.password_show / password_hide) and the keyboard's action going
 * on with [onAction]: [ImeAction.Next] to the field after it (the caller
 * moves the focus: the eye in between takes it otherwise), [ImeAction.Go]
 * (the default) to submit. The welcome screen, the "can't connect" card and
 * Settings; Settings › Wi-Fi keeps its own field, out of autofill. As iOS's
 * PasswordField.swift.
 */
@Composable
fun PasswordField(
    value: String,
    onValueChange: (String) -> Unit,
    hint: String,
    modifier: Modifier = Modifier,
    imeAction: ImeAction = ImeAction.Go,
    onAction: () -> Unit = {},
) {
    var shown by remember { mutableStateOf(false) }
    OTCTextField(
        value = value, onValueChange = onValueChange, placeholder = { Text(hint) }, singleLine = true,
        visualTransformation = if (shown) VisualTransformation.None else PasswordVisualTransformation(),
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false, imeAction = imeAction),
        keyboardActions = KeyboardActions(onNext = { onAction() }, onGo = { onAction() }),
        trailingIcon = {
            IconButton(onClick = { shown = !shown }, modifier = Modifier.size(36.dp)) {
                Icon(
                    if (shown) Icons.Default.VisibilityOff else Icons.Default.Visibility,
                    contentDescription = (if (shown) S.appShellPasswordHide() else S.appShellPasswordShow()).resolve(),
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        },
        modifier = modifier,
    )
}
