// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp

// The app's text field: the size and feel of SwiftUI's .roundedBorder
// field (about 36dp tall, body text, hint inside) instead of Material's
// 56dp floating-label box, which read as oversized next to the iOS app.
// `label` is accepted for call-site compatibility and shown as the hint,
// exactly as an iOS TextField's title is.
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun OTCTextField(
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    label: @Composable (() -> Unit)? = null,
    placeholder: @Composable (() -> Unit)? = null,
    singleLine: Boolean = false,
    minLines: Int = 1,
    enabled: Boolean = true,
    keyboardOptions: KeyboardOptions = KeyboardOptions.Default,
    keyboardActions: KeyboardActions = KeyboardActions.Default,
    visualTransformation: VisualTransformation = VisualTransformation.None,
) {
    val interaction = remember { MutableInteractionSource() }
    val textStyle = MaterialTheme.typography.bodyMedium.copy(color = MaterialTheme.colorScheme.onSurface)
    val hint = placeholder ?: label
    BasicTextField(
        value = value,
        onValueChange = onValueChange,
        modifier = modifier.heightIn(min = 36.dp),
        enabled = enabled,
        singleLine = singleLine,
        minLines = minLines,
        textStyle = textStyle,
        keyboardOptions = keyboardOptions,
        keyboardActions = keyboardActions,
        visualTransformation = visualTransformation,
        cursorBrush = SolidColor(MaterialTheme.colorScheme.primary),
        interactionSource = interaction,
        decorationBox = { inner ->
            OutlinedTextFieldDefaults.DecorationBox(
                value = value,
                innerTextField = inner,
                enabled = enabled,
                singleLine = singleLine,
                visualTransformation = visualTransformation,
                interactionSource = interaction,
                placeholder = hint?.let { h ->
                    { androidx.compose.runtime.CompositionLocalProvider(androidx.compose.material3.LocalTextStyle provides textStyle.copy(color = MaterialTheme.colorScheme.onSurfaceVariant)) { h() } }
                },
                contentPadding = PaddingValues(horizontal = 10.dp, vertical = 7.dp),
                container = {
                    OutlinedTextFieldDefaults.Container(
                        enabled = enabled, isError = false, interactionSource = interaction,
                        shape = MaterialTheme.shapes.small, focusedBorderThickness = 1.5.dp, unfocusedBorderThickness = 1.dp,
                    )
                },
            )
        },
    )
}

/** Convenience for the common "hint text only" case. */
@Composable
fun OTCTextField(
    value: String,
    onValueChange: (String) -> Unit,
    hint: String,
    modifier: Modifier = Modifier,
    singleLine: Boolean = true,
    keyboardOptions: KeyboardOptions = KeyboardOptions.Default,
    keyboardActions: KeyboardActions = KeyboardActions.Default,
    visualTransformation: VisualTransformation = VisualTransformation.None,
) = OTCTextField(
    value = value, onValueChange = onValueChange, modifier = modifier, placeholder = { Text(hint) },
    singleLine = singleLine, keyboardOptions = keyboardOptions, keyboardActions = keyboardActions, visualTransformation = visualTransformation,
)
