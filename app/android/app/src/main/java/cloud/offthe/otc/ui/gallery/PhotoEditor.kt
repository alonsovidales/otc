// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import android.graphics.Bitmap
import android.graphics.Matrix
import android.graphics.Paint
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.drag
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.displayCutoutPadding
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.RotateLeft
import androidx.compose.material.icons.automirrored.filled.RotateRight
import androidx.compose.material.icons.filled.AspectRatio
import androidx.compose.material.icons.filled.Check
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.listSaver
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.ClipOp
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.FilterQuality
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.clipRect
import androidx.compose.ui.graphics.drawscope.rotate
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.input.pointer.positionChange
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosEditAsCopy
import cloud.offthe.otc.i18n.appPhotosEditAspect
import cloud.offthe.otc.i18n.appPhotosEditAspect169
import cloud.offthe.otc.i18n.appPhotosEditAspect43
import cloud.offthe.otc.i18n.appPhotosEditAspectFree
import cloud.offthe.otc.i18n.appPhotosEditAspectOriginal
import cloud.offthe.otc.i18n.appPhotosEditAspectSquare
import cloud.offthe.otc.i18n.appPhotosEditCopiesAll
import cloud.offthe.otc.i18n.appPhotosEditCopiesOnlyThis
import cloud.offthe.otc.i18n.appPhotosEditCopiesText
import cloud.offthe.otc.i18n.appPhotosEditDiscard
import cloud.offthe.otc.i18n.appPhotosEditDiscardTitle
import cloud.offthe.otc.i18n.appPhotosEditReplace
import cloud.offthe.otc.i18n.appPhotosEditReset
import cloud.offthe.otc.i18n.appPhotosEditRotateLeft
import cloud.offthe.otc.i18n.appPhotosEditRotateRight
import cloud.offthe.otc.i18n.appPhotosEditSaveText
import cloud.offthe.otc.i18n.appPhotosEditSaveTitle
import cloud.offthe.otc.i18n.appPhotosEditSaving
import cloud.offthe.otc.i18n.commonCancel
import cloud.offthe.otc.i18n.commonOk
import cloud.offthe.otc.i18n.commonSave
import kotlinx.coroutines.launch
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt

// Photo edit: the viewer's Edit opens this over the photo - turn it a
// quarter at a time and crop it, then Save puts the edit on the device
// (EditImage) in the photo's place or as a copy next to it. The device
// does the work, on the full-size original; this only shows the picture
// the viewer already has (its full size, or its thumbnail until that
// comes) and where the frame is. The rules and the arithmetic are
// PhotoEdit.kt's. As iOS's PhotoEditor.

// The frame's handles: a corner's arms and an edge's bar, how thick, and
// how far from a side a finger still takes it.
private val HANDLE_ARM = 20.dp
private val HANDLE_WIDTH = 3.dp
private val HANDLE_REACH = 24.dp

// The smallest the frame gets on screen (besides PhotoEdit.MIN_SIDE of the photo).
private val FRAME_MIN = 40.dp

// Room around the picture for the handles that stand out of it, and for a
// finger to take them.
private val PICTURE_MARGIN = 24.dp

// In landscape, the column beside the picture holding the controls.
private val EDITOR_SIDE_COLUMN = 128.dp

// What is outside the frame: dimmed, as the profile photo's crop.
private val OUTSIDE_DIM = Color.Black.copy(alpha = 0.55f)

// The crop kept with the screen's saved state, as its four fractions.
private val CROP_SAVER = listSaver<CropRect, Double>(save = { listOf(it.x, it.y, it.w, it.h) }, restore = { CropRect(it[0], it[1], it[2], it[3]) })

/**
 * The editor, full screen over the viewer, for [item] (the open photo):
 * the picture fitted to the screen with the crop frame on it, Rotate Left,
 * Rotate Right and Aspect, Reset once something changed, Cancel, and Save -
 * enabled once something changed, "Saving…" and everything waiting while
 * the device saves. Save asks whether to replace the photo or save a copy;
 * Replace asks the device how many paths hold the photo first, and with
 * more than one whether to replace them all. Leaving with changes asks
 * first. Shown while State.editing.
 */
@Composable
internal fun PhotoEditor(vm: PhotoGalleryViewModel, st: PhotoGalleryViewModel.State, item: PhotoGalleryViewModel.Item) {
    val image = viewerImage(st, item)
    // The edit so far: quarter turns clockwise, the crop of the turned
    // photo, the shape picked - kept if the screen is made again.
    var turns by rememberSaveable(item.id) { mutableIntStateOf(0) }
    var crop by rememberSaveable(item.id, stateSaver = CROP_SAVER) { mutableStateOf(CropRect.WHOLE) }
    var aspect by rememberSaveable(item.id) { mutableStateOf(EditAspect.FREE) }
    // Save's question, then Replace's about the copies (how many), and Cancel's with changes.
    var saveAsk by remember { mutableStateOf(false) }
    var copiesAsk by remember { mutableStateOf<Int?>(null) }
    var discardAsk by remember { mutableStateOf(false) }
    // Replace's GetEditState on its way: the editor waits, as it does for the save.
    var asking by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val saving = asking || st.editBusy == EditBusy.SAVING
    val changed = PhotoEdit.changed(turns, crop)

    // The turned picture's width and height (any unit), for the shapes.
    fun turnedSize(q: Int): Pair<Double, Double> {
        val w = (image?.width ?: 1).toDouble()
        val h = (image?.height ?: 1).toDouble()
        return if (q % 2 == 0) w to h else h to w
    }
    val (shownW, shownH) = turnedSize(turns)
    val ratio = PhotoEdit.ratio(aspect, shownW, shownH)

    fun leave() {
        if (saving) return
        if (changed) discardAsk = true else vm.closeEditor()
    }
    fun turn(right: Boolean) {
        crop = if (right) PhotoEdit.turnedRight(crop) else PhotoEdit.turnedLeft(crop)
        turns = Math.floorMod(turns + if (right) 1 else -1, 4)
    }
    fun pick(a: EditAspect) {
        aspect = a
        PhotoEdit.ratio(a, shownW, shownH)?.let { r -> crop = PhotoEdit.fitted(crop, r, shownW / shownH) }
    }
    fun reset() {
        turns = 0
        crop = CropRect.WHOLE
        aspect = EditAspect.FREE
    }
    fun replace() {
        asking = true
        scope.launch {
            val copies = try { vm.openCopies() } finally { asking = false }
            if (copies > 1) copiesAsk = copies else vm.saveEdit(turns, crop, asCopy = false)
        }
    }

    Dialog(
        onDismissRequest = { leave() },
        properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false, dismissOnClickOutside = false),
    ) {
        Box(Modifier.fillMaxSize().background(Color.Black)) {
            // Edge to edge as the viewer: its own room from the status bar
            // and a camera cutout, or its buttons can't be tapped.
            BoxWithConstraints(Modifier.fillMaxSize().systemBarsPadding().displayCutoutPadding()) {
                val tools: @Composable () -> Unit = {
                    EditorTools(enabled = !saving && image != null, aspect, onRotate = ::turn, onAspect = ::pick)
                }
                val picture: @Composable (Modifier) -> Unit = { m ->
                    EditorPicture(image, turns, crop, ratio, enabled = !saving, onCrop = { crop = it }, modifier = m)
                }
                if (maxWidth > maxHeight) Row(Modifier.fillMaxSize()) {
                    // Landscape: the controls in a column beside the picture,
                    // which keeps the whole height.
                    picture(Modifier.weight(1f).fillMaxHeight())
                    Column(
                        Modifier.width(EDITOR_SIDE_COLUMN).fillMaxHeight().padding(vertical = 8.dp),
                        horizontalAlignment = Alignment.CenterHorizontally,
                    ) {
                        EditorSave(saving, enabled = changed && !saving) { saveAsk = true }
                        EditorCancel(enabled = !saving) { leave() }
                        Spacer(Modifier.weight(1f))
                        Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(8.dp)) { tools() }
                        Spacer(Modifier.weight(1f))
                        if (changed) EditorReset(enabled = !saving) { reset() } else Spacer(Modifier.height(48.dp))
                    }
                } else Column(Modifier.fillMaxSize()) {
                    Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                        EditorCancel(enabled = !saving) { leave() }
                        Spacer(Modifier.weight(1f))
                        if (changed) EditorReset(enabled = !saving) { reset() }
                        Spacer(Modifier.weight(1f))
                        EditorSave(saving, enabled = changed && !saving) { saveAsk = true }
                    }
                    picture(Modifier.weight(1f).fillMaxWidth())
                    Row(
                        Modifier.fillMaxWidth().padding(vertical = 12.dp),
                        horizontalArrangement = Arrangement.spacedBy(32.dp, Alignment.CenterHorizontally),
                    ) { tools() }
                }
            }
        }
        if (saveAsk) ChoiceDialog(
            S.appPhotosEditSaveTitle().resolve(), S.appPhotosEditSaveText().resolve(),
            listOf(
                S.appPhotosEditReplace().resolve() to { saveAsk = false; replace() },
                S.appPhotosEditAsCopy().resolve() to { saveAsk = false; vm.saveEdit(turns, crop, asCopy = true) },
            ),
            onDismiss = { saveAsk = false },
        )
        copiesAsk?.let { n ->
            ChoiceDialog(
                S.appPhotosEditSaveTitle().resolve(), S.appPhotosEditCopiesText(n).resolve(),
                listOf(
                    S.appPhotosEditCopiesAll(n).resolve() to { copiesAsk = null; vm.saveEdit(turns, crop, asCopy = false, everyCopy = true) },
                    S.appPhotosEditCopiesOnlyThis().resolve() to { copiesAsk = null; vm.saveEdit(turns, crop, asCopy = false) },
                ),
                onDismiss = { copiesAsk = null },
            )
        }
        if (discardAsk) ConfirmDialog(
            S.appPhotosEditDiscardTitle().resolve(), null, S.appPhotosEditDiscard().resolve(),
            onConfirm = { discardAsk = false; vm.closeEditor() }, onDismiss = { discardAsk = false },
        )
        // Save didn't go through: why, and the editor stays as it was, to try again.
        st.editError?.let { e ->
            AlertDialog(
                onDismissRequest = { vm.editErrorShown() }, text = { Text(e.resolve()) },
                confirmButton = { TextButton(onClick = { vm.editErrorShown() }) { Text(S.commonOk().resolve()) } },
            )
        }
    }
}

/** The editor's Cancel, in the viewer's white. */
@Composable
private fun EditorCancel(enabled: Boolean, onClick: () -> Unit) {
    TextButton(onClick = onClick, enabled = enabled, colors = editorTextColors()) { Text(S.commonCancel().resolve()) }
}

/** Reset: back to the photo as it opened, shown once something changed. */
@Composable
private fun EditorReset(enabled: Boolean, onClick: () -> Unit) {
    TextButton(onClick = onClick, enabled = enabled, colors = editorTextColors()) { Text(S.appPhotosEditReset().resolve()) }
}

@Composable
private fun editorTextColors() = ButtonDefaults.textButtonColors(contentColor = Color.White, disabledContentColor = Color.White.copy(alpha = 0.4f))

/**
 * Save, in the app's colour once something changed; [saving]: "Saving…"
 * with a spinner, legible but not to be tapped, until the device answers.
 */
@Composable
private fun EditorSave(saving: Boolean, enabled: Boolean, onClick: () -> Unit) {
    val primary = MaterialTheme.colorScheme.primary
    val onPrimary = MaterialTheme.colorScheme.onPrimary
    Button(
        onClick = onClick, enabled = enabled,
        colors = ButtonDefaults.buttonColors(
            containerColor = primary, contentColor = onPrimary,
            disabledContainerColor = if (saving) primary else Color.White.copy(alpha = 0.12f),
            disabledContentColor = if (saving) onPrimary else Color.White.copy(alpha = 0.4f),
        ),
    ) {
        if (saving) {
            CircularProgressIndicator(Modifier.size(16.dp), color = onPrimary, strokeWidth = 2.dp)
            Spacer(Modifier.width(8.dp))
            Text(S.appPhotosEditSaving().resolve())
        } else Text(S.commonSave().resolve())
    }
}

/**
 * Rotate Left, Rotate Right and Aspect - a menu of the frame's shapes, the
 * one in use ticked - as icons in the viewer's white, laid out by the
 * caller's row or column.
 */
@Composable
private fun EditorTools(enabled: Boolean, aspect: EditAspect, onRotate: (right: Boolean) -> Unit, onAspect: (EditAspect) -> Unit) {
    val tint = if (enabled) Color.White else Color.White.copy(alpha = 0.4f)
    IconButton(onClick = { onRotate(false) }, enabled = enabled) { Icon(Icons.AutoMirrored.Filled.RotateLeft, S.appPhotosEditRotateLeft().resolve(), tint = tint) }
    IconButton(onClick = { onRotate(true) }, enabled = enabled) { Icon(Icons.AutoMirrored.Filled.RotateRight, S.appPhotosEditRotateRight().resolve(), tint = tint) }
    var open by remember { mutableStateOf(false) }
    Box {
        IconButton(onClick = { open = true }, enabled = enabled) { Icon(Icons.Default.AspectRatio, S.appPhotosEditAspect().resolve(), tint = tint) }
        DropdownMenu(expanded = open && enabled, onDismissRequest = { open = false }) {
            for (a in EditAspect.entries) {
                val on = a == aspect
                DropdownMenuItem(
                    text = { Text(aspectName(a)) },
                    leadingIcon = { if (on) Icon(Icons.Default.Check, null) else Spacer(Modifier.size(24.dp)) },
                    onClick = { open = false; onAspect(a) },
                    modifier = Modifier.semantics { selected = on },
                )
            }
        }
    }
}

@Composable
private fun aspectName(a: EditAspect): String = when (a) {
    EditAspect.FREE -> S.appPhotosEditAspectFree()
    EditAspect.ORIGINAL -> S.appPhotosEditAspectOriginal()
    EditAspect.SQUARE -> S.appPhotosEditAspectSquare()
    EditAspect.FOUR_THREE -> S.appPhotosEditAspect43()
    EditAspect.SIXTEEN_NINE -> S.appPhotosEditAspect169()
}.resolve()

/**
 * The picture, turned [turns] quarter turns and fitted to the room, with
 * the frame [crop] on it: the outside dimmed, handles at the corners and
 * edges, and the rule of thirds while it is dragged. A corner or an edge
 * resizes it, inside it moves it ([onCrop]); [ratio] (width over height,
 * null: Free) keeps its shape. Nothing moves while not [enabled]. A
 * spinner until there is a picture.
 */
@Composable
private fun EditorPicture(
    image: Bitmap?, turns: Int, crop: CropRect, ratio: Double?, enabled: Boolean,
    onCrop: (CropRect) -> Unit, modifier: Modifier,
) {
    BoxWithConstraints(modifier.clearAndSetSemantics {}, contentAlignment = Alignment.Center) {
        if (image == null) {
            CircularProgressIndicator(color = Color.White)
            return@BoxWithConstraints
        }
        val density = LocalDensity.current
        val margin = with(density) { PICTURE_MARGIN.toPx() }
        val boxW = constraints.maxWidth.toFloat()
        val boxH = constraints.maxHeight.toFloat()
        val odd = turns % 2 == 1
        val turnedW = (if (odd) image.height else image.width).toFloat()
        val turnedH = (if (odd) image.width else image.height).toFloat()
        val scale = max(0f, min((boxW - 2 * margin) / turnedW, (boxH - 2 * margin) / turnedH))
        // Where the turned picture lies, in the box's pixels.
        val shown = PictureRect((boxW - turnedW * scale) / 2, (boxH - turnedH * scale) / 2, turnedW * scale, turnedH * scale)
        val bitmap = remember(image) { image.asImageBitmap() }
        var dragging by remember { mutableStateOf(false) }
        // Read by the drag as it goes: the frame may be turned or reshaped between drags.
        val now = rememberUpdatedState(DragInputs(shown, crop, ratio, enabled, onCrop))
        val reach = with(density) { HANDLE_REACH.toPx() }
        val smallest = with(density) { FRAME_MIN.toPx() }
        Canvas(
            Modifier.fillMaxSize().pointerInput(Unit) {
                awaitEachGesture {
                    val down = awaitFirstDown()
                    val at = now.value
                    if (!at.enabled || at.shown.w <= 0f || at.shown.h <= 0f) return@awaitEachGesture
                    val g = at.shown
                    val grab = PhotoEdit.grabAt(
                        at.crop, ((down.position.x - g.left) / g.w).toDouble(), ((down.position.y - g.top) / g.h).toDouble(),
                        (reach / g.w).toDouble(), (reach / g.h).toDouble(),
                    ) ?: return@awaitEachGesture
                    down.consume()
                    val start = at.crop
                    val minW = min(1.0, max(PhotoEdit.MIN_SIDE, (smallest / g.w).toDouble()))
                    val minH = min(1.0, max(PhotoEdit.MIN_SIDE, (smallest / g.h).toDouble()))
                    var moved = Offset.Zero
                    try {
                        drag(down.id) { change ->
                            moved += change.positionChange()
                            change.consume()
                            dragging = true
                            val inputs = now.value
                            if (inputs.enabled) inputs.onCrop(
                                PhotoEdit.dragged(start, grab, (moved.x / g.w).toDouble(), (moved.y / g.h).toDouble(), inputs.ratio, (g.w / g.h).toDouble(), minW, minH),
                            )
                        }
                    } finally {
                        dragging = false
                    }
                }
            },
        ) {
            // The picture, turned about its centre: drawn upright at its
            // size, then turned into the place fitted for it turned.
            val cx = shown.left + shown.w / 2
            val cy = shown.top + shown.h / 2
            val bw = image.width * scale
            val bh = image.height * scale
            rotate(90f * turns, Offset(cx, cy)) {
                drawImage(
                    bitmap, dstOffset = IntOffset((cx - bw / 2).roundToInt(), (cy - bh / 2).roundToInt()),
                    dstSize = IntSize(bw.roundToInt().coerceAtLeast(1), bh.roundToInt().coerceAtLeast(1)), filterQuality = FilterQuality.Medium,
                )
            }
            val frame = PictureRect(
                shown.left + (crop.x * shown.w).toFloat(), shown.top + (crop.y * shown.h).toFloat(),
                (crop.w * shown.w).toFloat(), (crop.h * shown.h).toFloat(),
            )
            clipRect(frame.left, frame.top, frame.left + frame.w, frame.top + frame.h, ClipOp.Difference) {
                drawRect(OUTSIDE_DIM, Offset(shown.left, shown.top), Size(shown.w, shown.h))
            }
            drawRect(Color.White.copy(alpha = 0.9f), Offset(frame.left, frame.top), Size(frame.w, frame.h), style = Stroke(1.dp.toPx()))
            if (dragging) drawThirds(frame)
            drawHandles(frame)
        }
    }
}

// A rectangle in the picture box's pixels.
private class PictureRect(val left: Float, val top: Float, val w: Float, val h: Float)

// What a drag under way reads, as it is now.
private class DragInputs(val shown: PictureRect, val crop: CropRect, val ratio: Double?, val enabled: Boolean, val onCrop: (CropRect) -> Unit)

// The rule of thirds inside the frame, while it is dragged.
private fun DrawScope.drawThirds(f: PictureRect) {
    val line = Color.White.copy(alpha = 0.6f)
    val width = 1.dp.toPx()
    for (i in 1..2) {
        val x = f.left + f.w * i / 3
        val y = f.top + f.h * i / 3
        drawLine(line, Offset(x, f.top), Offset(x, f.top + f.h), width)
        drawLine(line, Offset(f.left, y), Offset(f.left + f.w, y), width)
    }
}

// The handles: an L at each corner and a bar in the middle of each side
// long enough to have room for one, just outside the frame's line.
private fun DrawScope.drawHandles(f: PictureRect) {
    val width = HANDLE_WIDTH.toPx()
    val half = width / 2
    val armX = min(HANDLE_ARM.toPx(), f.w / 2)
    val armY = min(HANDLE_ARM.toPx(), f.h / 2)
    val l = f.left - half
    val t = f.top - half
    val r = f.left + f.w + half
    val b = f.top + f.h + half
    fun line(x1: Float, y1: Float, x2: Float, y2: Float) = drawLine(Color.White, Offset(x1, y1), Offset(x2, y2), width, StrokeCap.Square)
    line(l, t, l + armX, t); line(l, t, l, t + armY)
    line(r, t, r - armX, t); line(r, t, r, t + armY)
    line(l, b, l + armX, b); line(l, b, l, b - armY)
    line(r, b, r - armX, b); line(r, b, r, b - armY)
    val bar = HANDLE_ARM.toPx()
    if (f.w > bar * 3) {
        val cx = f.left + f.w / 2
        line(cx - bar / 2, t, cx + bar / 2, t)
        line(cx - bar / 2, b, cx + bar / 2, b)
    }
    if (f.h > bar * 3) {
        val cy = f.top + f.h / 2
        line(l, cy - bar / 2, l, cy + bar / 2)
        line(r, cy - bar / 2, r, cy + bar / 2)
    }
}

/**
 * A question whose answers are long (Replace Photo, Save as Copy; Replace
 * All 3, Only Here): [title], [text], each of [choices] - its label and
 * what it does - one above the other, and Cancel last, as the delete's
 * copies question. [danger]: the answers undo something (Revert), in the
 * error colour.
 */
@Composable
internal fun ChoiceDialog(title: String, text: String, choices: List<Pair<String, () -> Unit>>, onDismiss: () -> Unit, danger: Boolean = false) {
    val color = if (danger) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.primary
    AlertDialog(
        onDismissRequest = onDismiss, title = { Text(title) }, text = { Text(text) },
        confirmButton = {
            Column(horizontalAlignment = Alignment.End) {
                for ((label, act) in choices) TextButton(onClick = act) { Text(label, color = color, textAlign = TextAlign.End) }
                TextButton(onClick = onDismiss) { Text(S.commonCancel().resolve()) }
            }
        },
    )
}

/**
 * [source] (the picture the viewer showed, upright) as the device edits
 * it: [turns] quarter turns clockwise, then [crop] of the turned picture,
 * at most [maxSide] px on its longest side - what the viewer shows of a
 * replaced photo until its full size comes. Null when it can't be made (a
 * hardware bitmap, no memory). Call off the main thread.
 */
internal fun editedPicture(source: Bitmap, turns: Int, crop: CropRect, maxSide: Int = 1024): Bitmap? = try {
    val q = Math.floorMod(turns, 4)
    val tw = if (q % 2 == 0) source.width else source.height
    val th = if (q % 2 == 0) source.height else source.width
    // The crop in the turned picture's pixels, at least one.
    val cl = (crop.x * tw).roundToInt().coerceIn(0, tw - 1)
    val ct = (crop.y * th).roundToInt().coerceIn(0, th - 1)
    val cw = (crop.w * tw).roundToInt().coerceIn(1, tw - cl)
    val ch = (crop.h * th).roundToInt().coerceIn(1, th - ct)
    val k = min(1f, maxSide.toFloat() / max(cw, ch))
    val out = Bitmap.createBitmap(max(1, (cw * k).roundToInt()), max(1, (ch * k).roundToInt()), Bitmap.Config.ARGB_8888)
    // Turned about its corner and moved back into view, the crop's corner
    // brought to the origin, then scaled.
    val m = Matrix().apply {
        postRotate(90f * q)
        when (q) {
            1 -> postTranslate(source.height.toFloat(), 0f)
            2 -> postTranslate(source.width.toFloat(), source.height.toFloat())
            3 -> postTranslate(0f, source.width.toFloat())
        }
        postTranslate(-cl.toFloat(), -ct.toFloat())
        postScale(k, k)
    }
    android.graphics.Canvas(out).drawBitmap(source, m, Paint(Paint.FILTER_BITMAP_FLAG or Paint.ANTI_ALIAS_FLAG))
    out
} catch (_: Exception) {
    null
} catch (_: OutOfMemoryError) {
    null
}
