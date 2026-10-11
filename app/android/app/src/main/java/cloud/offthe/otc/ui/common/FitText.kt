// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.foundation.text.BasicText
import androidx.compose.foundation.text.TextAutoSize
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.LocalTextStyle
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.takeOrElse
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.sp

/**
 * A label kept on one line that gets smaller, down to [minScale] of its size, rather than
 * breaking inside a word: the bottom bar's and the rail's labels, which German, French, Italian
 * and Dutch make longer than their share (Einstellungen, Afbeeldingen, Impostazioni in a 72 dp
 * tab at 360 dp). A label that fits keeps its size, so English is as before; one that can't fit
 * even at [minScale] ends in an ellipsis. The iOS counterparts are the tab bar's own shrinking
 * and the rail's `minimumScaleFactor(0.8)`.
 */
@Composable
fun FitText(
    text: String,
    modifier: Modifier = Modifier,
    style: TextStyle = LocalTextStyle.current,
    color: Color = Color.Unspecified,
    minScale: Float = 0.7f,
) {
    val shown = color.takeOrElse { style.color.takeOrElse { LocalContentColor.current } }
    val size = style.fontSize
    BasicText(
        text,
        modifier,
        style = style.merge(color = shown),
        overflow = TextOverflow.Ellipsis,
        maxLines = 1,
        autoSize = TextAutoSize.StepBased(minFontSize = size * minScale, maxFontSize = size, stepSize = 0.25.sp),
    )
}
