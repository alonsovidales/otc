// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appPhotosEditRevertText
import cloud.offthe.otc.i18n.appPhotosEditVideoRevertText
import cloud.offthe.otc.proto.EditCrop
import cloud.offthe.otc.proto.EditImage
import cloud.offthe.otc.proto.EditVideo
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.RevertImage
import cloud.offthe.otc.ui.compose.TRIM_WHOLE_SLACK_SECS
import cloud.offthe.otc.ui.compose.trimApplies
import cloud.offthe.otc.ui.compose.trimKeepsWhole
import cloud.offthe.otc.ui.settings.deviceOr
import kotlin.math.abs
import kotlin.math.max
import kotlin.math.min
import cloud.offthe.otc.proto.File as PbFile

/**
 * Photo edit: the part of a photo an edit keeps, as EditCrop has it -
 * fractions (0 to 1) of the photo as it is shown, already turned, from its
 * top left corner. [WHOLE] keeps all of it.
 */
data class CropRect(val x: Double, val y: Double, val w: Double, val h: Double) {
    val right: Double get() = x + w
    val bottom: Double get() = y + h

    companion object {
        val WHOLE = CropRect(0.0, 0.0, 1.0, 1.0)
    }
}

/** The editor's Aspect menu: the crop's shape. */
enum class EditAspect { FREE, ORIGINAL, SQUARE, FOUR_THREE, SIXTEEN_NINE }

/** Where a finger took hold of the crop frame: inside it (to move it), an edge or a corner. */
enum class CropGrab {
    MOVE, LEFT, TOP, RIGHT, BOTTOM, TOP_LEFT, TOP_RIGHT, BOTTOM_LEFT, BOTTOM_RIGHT;

    val left: Boolean get() = this == LEFT || this == TOP_LEFT || this == BOTTOM_LEFT
    val right: Boolean get() = this == RIGHT || this == TOP_RIGHT || this == BOTTOM_RIGHT
    val top: Boolean get() = this == TOP || this == TOP_LEFT || this == TOP_RIGHT
    val bottom: Boolean get() = this == BOTTOM || this == BOTTOM_LEFT || this == BOTTOM_RIGHT
    val corner: Boolean get() = (left || right) && (top || bottom)
}

/** What the viewer waits on from the device: an edit being saved (Saving…), or the original put back (Reverting…). */
enum class EditBusy { SAVING, REVERTING }

/** What the device answered an EditImage or a RevertImage. */
sealed interface EditAnswer {
    /** Done: the file as it is now (the copy's, for Save as Copy). */
    data class Done(val file: PbFile) : EditAnswer

    /** Refused, or no answer: the device's error_code ("" without one) and what to say. */
    data class Refused(val code: String, val text: UiText) : EditAnswer
}

/**
 * Photo edit (the device's EditImage, RevertImage and GetEditState): which
 * photos the viewer offers Edit and Revert to Original for, the crop
 * frame's arithmetic, and the requests. Pure, for the JVM tests
 * (PhotoEditTest); the editor itself is PhotoEditor.kt. iOS and the web
 * follow the same rules.
 */
object PhotoEdit {
    // What the device can edit, by MIME type. Not videos, GIFs (animations),
    // camera raws (image/x-*, DNG), TIFFs or SVGs: it answers "not_editable".
    private val EDITABLE_TYPES = setOf("image/jpeg", "image/png", "image/heic", "image/heif", "image/avif", "image/webp")

    // How far a crop may be from the whole photo and still be the whole
    // photo: what a drag to the edges or a turn's sums leave over.
    private const val WHOLE_SLACK = 1e-6

    /** The smallest a crop may be, as a part of the photo's side (and the editor adds a few dozen dp on screen). */
    const val MIN_SIDE = 0.05

    /**
     * Whether the viewer offers Edit for a file of [mime] at [path]: a photo
     * the device can turn and crop - by its type, or a HEIC or HEIF by its
     * name (some phones store them with no type the device names).
     */
    fun editable(mime: String, path: String): Boolean {
        val type = mime.lowercase()
        if (type.startsWith("video/")) return false
        if (type in EDITABLE_TYPES) return true
        val name = path.lowercase()
        return name.endsWith(".heic") || name.endsWith(".heif")
    }

    /** [editable] for one of the viewer's items. */
    fun editable(item: PhotoGalleryViewModel.Item): Boolean = editable(item.mime, item.path)

    /**
     * Whether the viewer offers Revert to Original: the content is an edit
     * the device keeps the original of (File.original_hash) - not a
     * compressed one, whose original it deleted (File.original_removed).
     */
    fun revertable(item: PhotoGalleryViewModel.Item): Boolean = item.originalHash.isNotEmpty() && !item.originalRemoved

    /** Whether Info says the photo was compressed and its original deleted (File.original_removed; compress.compressed_note). */
    fun compressedNote(item: PhotoGalleryViewModel.Item?): Boolean = item?.originalRemoved == true

    /** Whether [crop] keeps the whole photo: no EditCrop is sent then. */
    fun whole(crop: CropRect): Boolean =
        abs(crop.x) < WHOLE_SLACK && abs(crop.y) < WHOLE_SLACK && abs(crop.w - 1) < WHOLE_SLACK && abs(crop.h - 1) < WHOLE_SLACK

    /** Whether [turns] quarter turns and [crop] change the photo: Save and Reset only then. */
    fun changed(turns: Int, crop: CropRect): Boolean = Math.floorMod(turns, 4) != 0 || !whole(crop)

    /** [crop] once the photo is turned a quarter turn clockwise: it turns with it. */
    fun turnedRight(crop: CropRect): CropRect = CropRect(1 - crop.y - crop.h, crop.x, crop.h, crop.w)

    /** [crop] once the photo is turned a quarter turn anticlockwise. */
    fun turnedLeft(crop: CropRect): CropRect = CropRect(crop.y, 1 - crop.x - crop.w, crop.h, crop.w)

    /**
     * The width over the height, in pixels, that [aspect] gives the crop of
     * a photo shown [width] x [height] (turned): null for Free. 4:3 and 16:9
     * follow the photo's orientation - an upright one gets 3:4 and 9:16 -
     * and a square photo counts as a wide one.
     */
    fun ratio(aspect: EditAspect, width: Double, height: Double): Double? {
        val wide = width >= height
        return when (aspect) {
            EditAspect.FREE -> null
            EditAspect.ORIGINAL -> if (width > 0 && height > 0) width / height else null
            EditAspect.SQUARE -> 1.0
            EditAspect.FOUR_THREE -> if (wide) 4.0 / 3.0 else 3.0 / 4.0
            EditAspect.SIXTEEN_NINE -> if (wide) 16.0 / 9.0 else 9.0 / 16.0
        }
    }

    /**
     * An Aspect picked: the largest frame of [ratio] (width over height, in
     * pixels) the photo holds - [photo] is its own width over its height,
     * turned - centred on where [crop] was centred, moved in only as far as
     * the photo's edges need. Picking one shape after another never shrinks
     * the frame step by step.
     */
    fun fitted(crop: CropRect, ratio: Double, photo: Double): CropRect {
        // The frame's width over its height in fractions of the photo.
        val k = ratio / photo
        val w = if (k >= 1) 1.0 else k
        val h = if (k >= 1) 1 / k else 1.0
        val cx = crop.x + crop.w / 2
        val cy = crop.y + crop.h / 2
        return CropRect(within(cx - w / 2, 0.0, 1 - w), within(cy - h / 2, 0.0, 1 - h), w, h)
    }

    /**
     * Where [at] (x, y in fractions of the photo shown) takes hold of the
     * frame [crop]: a corner, an edge, inside it, or nothing (null).
     * [reachX] and [reachY]: how far from a side a finger still takes it,
     * as fractions of the photo's width and height (a touch target's size
     * on screen). Inside the frame the reach is at most a quarter of its
     * size, so even a small frame keeps a middle to move it by.
     */
    fun grabAt(crop: CropRect, x: Double, y: Double, reachX: Double, reachY: Double): CropGrab? {
        val inX = min(reachX, crop.w / 4)
        val inY = min(reachY, crop.h / 4)
        val alongY = y >= crop.y - reachY && y <= crop.bottom + reachY
        val alongX = x >= crop.x - reachX && x <= crop.right + reachX
        val left = alongY && x >= crop.x - reachX && x <= crop.x + inX
        val right = alongY && x >= crop.right - inX && x <= crop.right + reachX
        val top = alongX && y >= crop.y - reachY && y <= crop.y + inY
        val bottom = alongX && y >= crop.bottom - inY && y <= crop.bottom + reachY
        return when {
            top && left -> CropGrab.TOP_LEFT
            top && right -> CropGrab.TOP_RIGHT
            bottom && left -> CropGrab.BOTTOM_LEFT
            bottom && right -> CropGrab.BOTTOM_RIGHT
            left -> CropGrab.LEFT
            right -> CropGrab.RIGHT
            top -> CropGrab.TOP
            bottom -> CropGrab.BOTTOM
            x >= crop.x && x <= crop.right && y >= crop.y && y <= crop.bottom -> CropGrab.MOVE
            else -> null
        }
    }

    /**
     * The frame a drag makes of [start] (the frame as the finger took hold
     * of it at [grab]), moved [dx], [dy] since (fractions of the photo's
     * width and height): always inside the photo, and never smaller than
     * [minW] x [minH] where the photo has the room. Measured from where the
     * drag started rather than step by step, so a finger that went past an
     * edge and came back finds the frame under it again. [ratio] (width
     * over height in pixels; null: Free) keeps the frame's shape: a corner
     * follows the finger along the frame's diagonal, its opposite corner
     * staying put; an edge sets that side's length, the frame staying
     * centred across it. [photo]: the photo's width over its height, turned.
     */
    fun dragged(
        start: CropRect, grab: CropGrab, dx: Double, dy: Double,
        ratio: Double?, photo: Double, minW: Double, minH: Double,
    ): CropRect {
        if (grab == CropGrab.MOVE) {
            return start.copy(x = within(start.x + dx, 0.0, 1 - start.w), y = within(start.y + dy, 0.0, 1 - start.h))
        }
        if (ratio == null) {
            val l = if (grab.left) within(start.x + dx, 0.0, start.right - minW) else start.x
            val r = if (grab.right) within(start.right + dx, start.x + minW, 1.0) else start.right
            val t = if (grab.top) within(start.y + dy, 0.0, start.bottom - minH) else start.y
            val b = if (grab.bottom) within(start.bottom + dy, start.y + minH, 1.0) else start.bottom
            return CropRect(l, t, r - l, b - t)
        }
        // The frame's width over its height in fractions of the photo.
        val k = ratio / photo
        if (grab.corner) {
            // The corner opposite the one held stays where it was.
            val ax = if (grab.right) start.x else start.right
            val ay = if (grab.bottom) start.y else start.bottom
            val sx = if (grab.right) 1.0 else -1.0
            val sy = if (grab.bottom) 1.0 else -1.0
            // Where the held corner is now, from that one, outwards positive.
            val vx = sx * ((if (grab.right) start.right else start.x) + dx - ax)
            val vy = sy * ((if (grab.bottom) start.bottom else start.y) + dy - ay)
            // Along the diagonal, measured where the photo's pixels are
            // square (its height 1, its width [photo]): the finger's motion
            // on either axis counts, as it looks on screen.
            val h0 = (vx * photo * ratio + vy) / (ratio * ratio + 1)
            val roomW = if (grab.right) 1 - ax else ax
            val roomH = if (grab.bottom) 1 - ay else ay
            val h = sized(h0, max(minH, minW / k), min(roomH, roomW / k))
            val w = h * k
            return CropRect(if (grab.right) ax else ax - w, if (grab.bottom) ay else ay - h, w, h)
        }
        if (grab.left || grab.right) {
            val ax = if (grab.right) start.x else start.right
            val w0 = if (grab.right) start.w + dx else start.w - dx
            // At most the photo's height in shape, and the room on that side.
            val w = sized(w0, max(minW, minH * k), min(if (grab.right) 1 - ax else ax, k))
            val h = w / k
            val cy = start.y + start.h / 2
            return CropRect(if (grab.right) ax else ax - w, within(cy - h / 2, 0.0, 1 - h), w, h)
        }
        val ay = if (grab.bottom) start.y else start.bottom
        val h0 = if (grab.bottom) start.h + dy else start.h - dy
        val h = sized(h0, max(minH, minW / k), min(if (grab.bottom) 1 - ay else ay, 1 / k))
        val w = h * k
        val cx = start.x + start.w / 2
        return CropRect(within(cx - w / 2, 0.0, 1 - w), if (grab.bottom) ay else ay - h, w, h)
    }

    /**
     * Save's EditImage for the photo at [path], shown with [hash] (the
     * device refuses it as "changed" if the path holds other content now):
     * [turns] quarter turns clockwise, then [crop] of the turned photo (none
     * sent for the whole photo). [asCopy]: a new photo next to it;
     * otherwise [everyCopy] replaces it at every path holding it.
     */
    fun request(path: String, hash: String, turns: Int, crop: CropRect, asCopy: Boolean, everyCopy: Boolean = false): EditImage {
        val b = EditImage.newBuilder().setPath(path).setHash(hash).setQuarterTurns(Math.floorMod(turns, 4)).setAsCopy(asCopy)
        if (!whole(crop)) {
            val x = within(crop.x, 0.0, 1.0)
            val y = within(crop.y, 0.0, 1.0)
            b.crop = EditCrop.newBuilder().setX(x).setY(y).setWidth(within(crop.w, 0.0, 1 - x)).setHeight(within(crop.h, 0.0, 1 - y)).build()
        }
        if (!asCopy && everyCopy) b.everyCopy = true
        return b.build()
    }

    /** Revert to Original's RevertImage for the photo at [path], shown with [hash]; [everyCopy]: at every path holding the edit. */
    fun revertRequest(path: String, hash: String, everyCopy: Boolean = false): RevertImage =
        RevertImage.newBuilder().setPath(path).setHash(hash).setEveryCopy(everyCopy).build()

    /**
     * How many paths hold the photo, from a GetEditState answer: at least 1.
     * No answer, an error or an older device's unknown_payload count as one -
     * then nobody is asked about copies.
     */
    fun copies(resp: RespEnvelope?): Int =
        if (resp?.payloadCase == RespEnvelope.PayloadCase.RESP_EDIT_STATE) max(1, resp.respEditState.copies) else 1

    /**
     * What an EditImage or RevertImage answer means: the file, or the
     * refusal in the device's words (the demo's, an older device's
     * "update" words, "changed", "not_editable", "storage_full" - always in
     * the owner's language), else [failed] (no answer, or none in words).
     */
    fun answer(resp: RespEnvelope?, failed: UiText): EditAnswer = when {
        resp == null -> EditAnswer.Refused("", failed)
        resp.payloadCase == RespEnvelope.PayloadCase.RESP_FILE -> EditAnswer.Done(resp.respFile)
        resp.error -> EditAnswer.Refused(resp.errorCode, deviceOr(resp.errorMessage, failed))
        else -> EditAnswer.Refused("", failed)
    }

    /** The name a copy was saved under (saved_copy): its path's last part. */
    fun fileName(path: String): String = path.trimEnd('/').substringAfterLast('/')

    // [v] between [lo] and [hi]. Where they cross - a frame that can't be
    // its smallest there - [hi], never below 0: the frame stays inside the
    // photo, smaller than its smallest.
    private fun within(v: Double, lo: Double, hi: Double): Double = if (hi < lo) max(0.0, hi) else max(lo, min(hi, v))

    // A length between its smallest and the room it has - the room first.
    private fun sized(v: Double, smallest: Double, room: Double): Double = min(room, max(v, min(smallest, room)))
}

/**
 * Video edit (the device's EditVideo; RevertImage and GetEditState as for a
 * photo): a cut by time, which the device makes without re-encoding - it
 * copies the streams, so the cut starts at the keyframe before the start
 * chosen (the cut screen's note says so). Which videos the viewer offers
 * Edit for, what the cut screen lets Save, the request, and Revert to
 * Original's words for a video. Everything else - the Save and copies
 * questions, the answers, what the viewer shows after - is the photo
 * edit's (PhotoEdit): a cut is an edit as a turn or a crop is, with a hash
 * of its own and the original kept. Pure, for the JVM tests
 * (PhotoEditTest); the cut screen is VideoEditor.kt, on the composer's
 * trimmer. iOS and the web follow the same rules.
 */
object VideoEdit {
    // What the device can cut, by MIME type (edit_video.go's cutFormats):
    // the containers it copies the streams back into. Not MPEG-TS, WMV,
    // FLV...: it answers "not_editable".
    private val CUTTABLE_TYPES = setOf(
        "video/mp4", "video/quicktime", "video/x-m4v", "video/3gpp", "video/3gpp2", "video/x-matroska", "video/webm", "video/x-msvideo",
    )

    // The same by the name's extension (cutExtFormats), for a file typed as
    // nothing in particular: no type, or application/octet-stream.
    private val CUTTABLE_EXTENSIONS = setOf("mp4", "mov", "m4v", "3gp", "3g2", "mkv", "webm", "avi")
    private val UNTYPED = setOf("", "application/octet-stream")

    // A type without its parameters, in lower case.
    private fun typeOf(mime: String): String = mime.substringBefore(';').trim().lowercase()

    /**
     * Whether the viewer offers Edit (the cut) for a file of [mime] at
     * [path]: a video the device can cut - by its type, or, typed as
     * nothing in particular, by its name in any case. Never a photo, nor a
     * video in a format the device can't cut.
     */
    fun editable(mime: String, path: String): Boolean {
        val type = typeOf(mime)
        if (type in CUTTABLE_TYPES) return true
        if (type !in UNTYPED) return false
        val name = path.trimEnd('/').substringAfterLast('/')
        return name.contains('.') && name.substringAfterLast('.').lowercase() in CUTTABLE_EXTENSIONS
    }

    /** [editable] for one of the viewer's items. */
    fun editable(item: PhotoGalleryViewModel.Item): Boolean = editable(item.mime, item.path)

    /** Whether [item] is a video, for the words the edit's questions say and for the viewer to play it: by its type, or as [editable] finds one by its name. */
    fun isVideo(item: PhotoGalleryViewModel.Item): Boolean = typeOf(item.mime).startsWith("video/") || editable(item)

    /** Revert to Original's question when one path holds the edit: a video's says the cut is undone. */
    fun revertText(item: PhotoGalleryViewModel.Item): UiText =
        if (isVideo(item)) S.appPhotosEditVideoRevertText() else S.appPhotosEditRevertText()

    /**
     * Whether the cut screen's Save is on for the handles at [start]..[end]
     * seconds of a video [length] long (null: not read yet): a real cut -
     * not the whole video - that keeps at least half a second, the
     * trimmer's own rule (less, the device refuses as "too_short").
     */
    fun savable(start: Double, end: Double, length: Double?): Boolean = trimApplies(start, end, length)

    /** Whether the handles cut anything: Cancel asks before it is lost (discard_title). */
    fun changed(start: Double, end: Double, length: Double?): Boolean = !trimKeepsWhole(start, end, length)

    /**
     * Save's EditVideo for the video at [path], shown with [hash] (the
     * device refuses it as "changed" if the path holds other content now):
     * from [start] to [end] seconds of a video [length] long. An end at
     * the video's end - within the trimmer's slack of it, or the length not
     * known - goes as 0, "to the end": the device's own length decides,
     * which a player's may miss by a frame. [asCopy]: a new video next to
     * it; otherwise [everyCopy] replaces it at every path holding it.
     */
    fun request(path: String, hash: String, start: Double, end: Double, length: Double?, asCopy: Boolean, everyCopy: Boolean = false): EditVideo {
        val toEnd = length == null || end <= 0 || end >= length - TRIM_WHOLE_SLACK_SECS
        val b = EditVideo.newBuilder().setPath(path).setHash(hash).setStartSecs(max(0.0, start)).setEndSecs(if (toEnd) 0.0 else end).setAsCopy(asCopy)
        if (!asCopy && everyCopy) b.everyCopy = true
        return b.build()
    }
}
