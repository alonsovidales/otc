// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import android.content.Context
import android.graphics.Bitmap
import android.graphics.ImageDecoder
import android.graphics.Paint
import android.graphics.RectF
import android.net.Uri
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.Slider
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.ClipOp
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.clipPath
import androidx.compose.ui.graphics.drawscope.drawIntoCanvas
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import java.io.ByteArrayOutputStream

/*
 * The profile photo's circle crop - it mirrors the setup wizard's crop
 * (scripts/setup_wizard.py, issue #178): the photo always covers a square,
 * zoom 1x-4x on top of the cover scale, drag to centre the face, and what's
 * inside the square becomes a 320x320 JPEG small enough (<= 45 KB) to travel
 * with the profile. The circle is only a guide; the avatar views clip it.
 */

private const val OUT = 320
private const val MAX_BYTES = 45 * 1024
private const val MAX_DECODE = 2048

/** Decodes a pick upright (ImageDecoder applies EXIF), longest side <= 2048. Call off the main thread. */
fun decodeProfilePhoto(context: Context, uri: Uri): Bitmap? = try {
    ImageDecoder.decodeBitmap(ImageDecoder.createSource(context.contentResolver, uri)) { dec, info, _ ->
        val w = info.size.width; val h = info.size.height
        val longest = maxOf(w, h)
        if (longest > MAX_DECODE) {
            val k = MAX_DECODE.toFloat() / longest
            dec.setTargetSize(maxOf(1, (w * k).toInt()), maxOf(1, (h * k).toInt()))
        }
        // Software, so the final render can draw it on a plain Canvas.
        dec.allocator = ImageDecoder.ALLOCATOR_SOFTWARE
    }
} catch (e: Exception) { null }

// The crop state: zoom on top of the cover scale, and the pan in pixels of
// the on-screen square (the wizard's crop.z / crop.x / crop.y).
private fun coverScale(bmp: Bitmap, side: Float, z: Float) = z * maxOf(side / bmp.width, side / bmp.height)

private fun clampPan(bmp: Bitmap, side: Float, z: Float, p: Offset): Offset {
    val s = coverScale(bmp, side, z)
    val mx = maxOf(0f, (bmp.width * s - side) / 2); val my = maxOf(0f, (bmp.height * s - side) / 2)
    return Offset(p.x.coerceIn(-mx, mx), p.y.coerceIn(-my, my))
}

/** Where the photo lands in a square of `size` px (the wizard's cropPaint). */
private fun photoRect(bmp: Bitmap, side: Float, z: Float, pan: Offset, size: Float): RectF {
    val k = size / side
    val s = coverScale(bmp, side, z) * k
    val w = bmp.width * s; val h = bmp.height * s
    val l = (size - w) / 2 + pan.x * k; val t = (size - h) / 2 + pan.y * k
    return RectF(l, t, l + w, t + h)
}

/** Renders the square to 320x320 and compresses it, lowering the quality until it fits. Call off the main thread. */
fun renderProfilePhoto(bmp: Bitmap, side: Float, z: Float, pan: Offset): ByteArray {
    val out = Bitmap.createBitmap(OUT, OUT, Bitmap.Config.ARGB_8888)
    android.graphics.Canvas(out).apply {
        drawColor(0xFF1E1F22.toInt())
        drawBitmap(bmp, null, photoRect(bmp, side, z, pan, OUT.toFloat()), Paint(Paint.FILTER_BITMAP_FLAG or Paint.ANTI_ALIAS_FLAG))
    }
    var bytes = ByteArray(0)
    for (q in intArrayOf(85, 70, 55, 40)) {
        bytes = ByteArrayOutputStream().also { out.compress(Bitmap.CompressFormat.JPEG, q, it) }.toByteArray()
        if (bytes.size <= MAX_BYTES) break
    }
    out.recycle()
    return bytes
}

/**
 * Full-screen crop dialog. `onUse` gets the side of the on-screen square,
 * the zoom and the pan - the caller renders them with renderProfilePhoto
 * off the main thread.
 */
@Composable
fun ProfilePhotoCropDialog(bitmap: Bitmap, onCancel: () -> Unit, onUse: (side: Float, zoom: Float, pan: Offset) -> Unit) {
    var side by remember { mutableFloatStateOf(0f) }
    var zoom by remember(bitmap) { mutableFloatStateOf(1f) }
    var panX by remember(bitmap) { mutableFloatStateOf(0f) }
    var panY by remember(bitmap) { mutableFloatStateOf(0f) }
    val paint = remember { Paint(Paint.FILTER_BITMAP_FLAG or Paint.ANTI_ALIAS_FLAG) }

    // Zoom to a new level keeping `focus` (relative to the square's centre) still.
    fun zoomTo(newZoom: Float, focus: Offset = Offset.Zero, pan: Offset = Offset.Zero) {
        val z = newZoom.coerceIn(1f, 4f)
        val r = z / zoom
        val p = (Offset(panX, panY) - focus) * r + focus + pan
        zoom = z
        clampPan(bitmap, side, z, p).let { panX = it.x; panY = it.y }
    }

    Dialog(onDismissRequest = onCancel, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Column(
            Modifier.fillMaxSize().background(Color(0xFF111214)).padding(16.dp),
            verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Text("Drag the photo to centre your face", color = Color.White)
            Canvas(
                Modifier.padding(vertical = 16.dp).fillMaxWidth().aspectRatio(1f)
                    .onSizeChanged { side = it.width.toFloat() }
                    .pointerInput(bitmap) {
                        detectTransformGestures { centroid, pan, gestureZoom, _ ->
                            if (side <= 0f) return@detectTransformGestures
                            zoomTo(zoom * gestureZoom, centroid - Offset(side / 2, side / 2), pan)
                        }
                    },
            ) {
                if (side <= 0f) return@Canvas
                drawRect(Color(0xFF1E1F22))
                drawIntoCanvas { it.nativeCanvas.drawBitmap(bitmap, null, photoRect(bitmap, side, zoom, Offset(panX, panY), size.width), paint) }
                // Darken everything outside the circle.
                val circle = Path().apply { addOval(androidx.compose.ui.geometry.Rect(Offset(size.width / 2, size.height / 2), size.width / 2 - 2f)) }
                clipPath(circle, ClipOp.Difference) { drawRect(Color.Black.copy(alpha = 0.55f)) }
            }
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                Text("Zoom", color = Color.White, modifier = Modifier.padding(end = 12.dp))
                Slider(value = zoom, onValueChange = { if (side > 0f) zoomTo(it) }, valueRange = 1f..4f, modifier = Modifier.weight(1f))
            }
            Row(Modifier.fillMaxWidth().padding(top = 8.dp), horizontalArrangement = Arrangement.End) {
                TextButton(onClick = onCancel) { Text("Cancel") }
                Button(onClick = { if (side > 0f) onUse(side, zoom, Offset(panX, panY)) }, modifier = Modifier.padding(start = 8.dp)) { Text("Use photo") }
            }
        }
    }
}
