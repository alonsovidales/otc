// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.clipPath
import androidx.compose.ui.graphics.lerp
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp

// The grid's stand-in for a file with no thumbnail: a generic document
// (folded top-right corner) with a coloured band naming the type. Shared
// look with the web and iOS FileTypeIcon - deliberately not brand logos.

private const val ASPECT = 0.78f

/** The band's label and colour for a file name, by lowercased extension. */
fun fileTypeBadge(name: String): Pair<String, Color> {
    val dot = name.lastIndexOf('.')
    val ext = if (dot in 0 until name.length - 1) name.substring(dot + 1).lowercase() else ""
    val upper = ext.uppercase()
    return when (ext) {
        "pdf" -> "PDF" to Color(0xFFE5252A)
        "doc", "docx", "odt", "rtf", "pages" -> "DOC" to Color(0xFF2B579A)
        "xls", "xlsx", "ods", "csv", "numbers" -> "XLS" to Color(0xFF217346)
        "ppt", "pptx", "odp", "key" -> "PPT" to Color(0xFFD24726)
        "txt", "md", "log" -> "TXT" to Color(0xFF6B7280)
        "zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "xz" -> "ZIP" to Color(0xFFB7791F)
        "mp3", "wav", "flac", "m4a", "aac", "ogg", "opus" -> upper.take(4) to Color(0xFF7C3AED)
        "json", "js", "ts", "go", "py", "swift", "kt", "java", "c", "cpp", "h",
        "html", "css", "xml", "yml", "yaml", "sh" -> "</>" to Color(0xFF0F766E)
        "apk" -> "APK" to Color(0xFF3DDC84)
        "jpg", "jpeg", "png", "heic", "gif", "webp", "tiff", "bmp", "dng", "raw", "cr2", "nef", "arw" -> upper to Color(0xFF0EA5E9)
        "mp4", "mov", "m4v", "mkv", "avi", "webm", "3gp" -> upper to Color(0xFFDB2777)
        else -> (if (ext.isEmpty()) "FILE" else upper.take(4)) to Color(0xFF64748B)
    }
}

/** Fills the space it is given, keeping the document's portrait shape centred. */
@Composable
fun FileTypeIcon(name: String, modifier: Modifier = Modifier) {
    val (label, band) = fileTypeBadge(name)
    val body = MaterialTheme.colorScheme.surfaceVariant
    val fold = lerp(body, Color.Black, 0.18f)
    val edge = MaterialTheme.colorScheme.outlineVariant
    Box(modifier, contentAlignment = Alignment.Center) {
        BoxWithConstraints(Modifier.aspectRatio(ASPECT, matchHeightConstraintsFirst = true)) {
            val w = maxWidth
            Canvas(Modifier.fillMaxSize()) {
                val r = size.width * 0.08f
                val f = size.width * 0.26f
                // Rounded rect with the top-right corner cut off diagonally.
                val doc = Path().apply {
                    moveTo(r, 0f)
                    lineTo(size.width - f, 0f)
                    lineTo(size.width, f)
                    lineTo(size.width, size.height - r)
                    arcTo(Rect(Offset(size.width - 2 * r, size.height - 2 * r), Size(2 * r, 2 * r)), 0f, 90f, false)
                    lineTo(r, size.height)
                    arcTo(Rect(Offset(0f, size.height - 2 * r), Size(2 * r, 2 * r)), 90f, 90f, false)
                    lineTo(0f, r)
                    arcTo(Rect(Offset.Zero, Size(2 * r, 2 * r)), 180f, 90f, false)
                    close()
                }
                drawPath(doc, body)
                clipPath(doc) {
                    drawRect(band, Offset(0f, size.height * 0.56f), Size(size.width, size.height * 0.26f))
                }
                drawPath(doc, edge, style = Stroke(width = 1.dp.toPx()))
                val flap = Path().apply {
                    moveTo(size.width - f, 0f)
                    lineTo(size.width - f, f)
                    lineTo(size.width, f)
                    close()
                }
                drawPath(flap, fold)
            }
            // Bold white label sized to the band: its height, or its width
            // for the label's length, whichever is tighter.
            val bandH = w / ASPECT * 0.26f
            val byWidth = w * 0.84f / (maxOf(label.length, 2) * 0.68f)
            val sizeDp = minOf(bandH * 0.62f, byWidth)
            val fontSize = with(LocalDensity.current) { sizeDp.toSp() }
            Box(Modifier.offset(y = w / ASPECT * 0.56f).fillMaxWidth().height(bandH), contentAlignment = Alignment.Center) {
                Text(
                    label, color = Color.White, fontWeight = FontWeight.Bold, fontSize = fontSize,
                    lineHeight = fontSize, maxLines = 1, softWrap = false, textAlign = TextAlign.Center,
                )
            }
        }
    }
}
