// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appPhotosEditFailed
import cloud.offthe.otc.proto.EditState
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.RespEnvelope
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Photo edit (PhotoEdit.kt): which photos the viewer offers Edit and Revert
// to Original for, the crop frame's arithmetic - turning with the photo,
// the shapes, the drags, kept inside the photo - and the requests the
// editor sends. The iPhone app and the web follow the same rules.
class PhotoEditTest {
    private fun item(mime: String, path: String = "/Camera/IMG_1.JPG", originalHash: String = "") =
        PhotoGalleryViewModel.Item(path, path, mime, 1, null, hash = "h", originalHash = originalHash)

    private fun assertRect(expected: CropRect, actual: CropRect, what: String = "") {
        val d = 1e-9
        assertEquals("$what x", expected.x, actual.x, d)
        assertEquals("$what y", expected.y, actual.y, d)
        assertEquals("$what w", expected.w, actual.w, d)
        assertEquals("$what h", expected.h, actual.h, d)
    }

    // The frame is always inside the photo.
    private fun assertInside(c: CropRect) {
        assertTrue("x $c", c.x >= -1e-12)
        assertTrue("y $c", c.y >= -1e-12)
        assertTrue("right $c", c.right <= 1 + 1e-12)
        assertTrue("bottom $c", c.bottom <= 1 + 1e-12)
    }

    @Test fun editIsOfferedForThePhotosTheDeviceCanEdit() {
        for (mime in listOf("image/jpeg", "image/png", "image/heic", "image/heif", "image/avif", "image/webp", "IMAGE/JPEG")) {
            assertTrue(mime, PhotoEdit.editable(item(mime)))
        }
        // A HEIC or HEIF by its name, in any case, whatever type it was stored with.
        assertTrue(PhotoEdit.editable(item("application/octet-stream", "/Camera/IMG_2.HEIC")))
        assertTrue(PhotoEdit.editable(item("", "/Camera/img_3.heif")))
        // Not videos, animations, camera raws, TIFFs or drawings.
        for (mime in listOf("video/mp4", "video/quicktime", "image/gif", "image/x-canon-cr2", "image/x-adobe-dng", "image/dng", "image/tiff", "image/svg+xml", "application/pdf")) {
            assertFalse(mime, PhotoEdit.editable(item(mime, "/Camera/file")))
        }
        // A video named like a photo is still a video.
        assertFalse(PhotoEdit.editable(item("video/quicktime", "/Camera/IMG_4.HEIC")))
    }

    @Test fun revertIsOfferedOnlyForAnEdit() {
        assertFalse(PhotoEdit.revertable(item("image/jpeg")))
        assertTrue(PhotoEdit.revertable(item("image/jpeg", originalHash = "abc")))
    }

    @Test fun nothingChangedUntilATurnOrACrop() {
        assertFalse(PhotoEdit.changed(0, CropRect.WHOLE))
        // Four turns are none.
        assertFalse(PhotoEdit.changed(4, CropRect.WHOLE))
        assertFalse(PhotoEdit.changed(-4, CropRect(0.0, 0.0, 1.0, 1.0 - 1e-9)))
        assertTrue(PhotoEdit.changed(1, CropRect.WHOLE))
        assertTrue(PhotoEdit.changed(-1, CropRect.WHOLE))
        assertTrue(PhotoEdit.changed(0, CropRect(0.1, 0.0, 0.9, 1.0)))
    }

    @Test fun theCropTurnsWithThePhoto() {
        val c = CropRect(0.1, 0.2, 0.3, 0.4)
        // Clockwise: (1-y-h, x, h, w); anticlockwise: (y, 1-x-w, h, w).
        assertRect(CropRect(0.4, 0.1, 0.4, 0.3), PhotoEdit.turnedRight(c), "right")
        assertRect(CropRect(0.2, 0.6, 0.4, 0.3), PhotoEdit.turnedLeft(c), "left")
        // One way and back, and four the same way, is where it started.
        assertRect(c, PhotoEdit.turnedLeft(PhotoEdit.turnedRight(c)))
        var r = c
        repeat(4) { r = PhotoEdit.turnedRight(r) }
        assertRect(c, r, "four turns")
        // The whole photo stays the whole photo.
        assertRect(CropRect.WHOLE, PhotoEdit.turnedRight(CropRect.WHOLE))
    }

    @Test fun theShapesFollowThePhotosOrientation() {
        assertNull(PhotoEdit.ratio(EditAspect.FREE, 4000.0, 3000.0))
        assertEquals(4.0 / 3.0, PhotoEdit.ratio(EditAspect.ORIGINAL, 4000.0, 3000.0)!!, 1e-12)
        assertEquals(1.0, PhotoEdit.ratio(EditAspect.SQUARE, 3000.0, 4000.0)!!, 1e-12)
        // Wide photos get 4:3 and 16:9, upright ones 3:4 and 9:16.
        assertEquals(4.0 / 3.0, PhotoEdit.ratio(EditAspect.FOUR_THREE, 4000.0, 3000.0)!!, 1e-12)
        assertEquals(3.0 / 4.0, PhotoEdit.ratio(EditAspect.FOUR_THREE, 3000.0, 4000.0)!!, 1e-12)
        assertEquals(16.0 / 9.0, PhotoEdit.ratio(EditAspect.SIXTEEN_NINE, 4000.0, 3000.0)!!, 1e-12)
        assertEquals(9.0 / 16.0, PhotoEdit.ratio(EditAspect.SIXTEEN_NINE, 3000.0, 4000.0)!!, 1e-12)
        // A square photo counts as a wide one.
        assertEquals(16.0 / 9.0, PhotoEdit.ratio(EditAspect.SIXTEEN_NINE, 1000.0, 1000.0)!!, 1e-12)
    }

    @Test fun aShapeIsTheLargestFrameThePhotoHoldsCentredWhereTheFrameWas() {
        // A 4:3 photo: its largest square is three quarters of its width, in the middle.
        val square = PhotoEdit.fitted(CropRect.WHOLE, 1.0, 4.0 / 3.0)
        assertRect(CropRect(0.125, 0.0, 0.75, 1.0), square, "square")
        // 16:9 in it: the whole width, 3/4 of the height... in pixels 4 x 2.25 of 4 x 3.
        val wide = PhotoEdit.fitted(square, 16.0 / 9.0, 4.0 / 3.0)
        assertRect(CropRect(0.0, 0.125, 1.0, 0.75), wide, "16:9")
        // Original again: the whole photo, never shrinking step by step.
        assertRect(CropRect.WHOLE, PhotoEdit.fitted(wide, 4.0 / 3.0, 4.0 / 3.0), "original")
        // Centred on where the frame was, moved in only as far as the edges need.
        val leftCorner = CropRect(0.0, 0.0, 0.2, 0.2)
        assertRect(CropRect(0.0, 0.0, 0.75, 1.0), PhotoEdit.fitted(leftCorner, 1.0, 4.0 / 3.0), "at the left")
        val nearRight = CropRect(0.7, 0.4, 0.2, 0.2) // centred at 0.8
        assertRect(CropRect(0.25, 0.0, 0.75, 1.0), PhotoEdit.fitted(nearRight, 1.0, 4.0 / 3.0), "at the right")
        // In pixels the frame has the shape asked for: an upright photo's 9:16.
        val upright = PhotoEdit.fitted(CropRect.WHOLE, 9.0 / 16.0, 3.0 / 4.0)
        assertEquals(9.0 / 16.0, upright.w * 3 / (upright.h * 4), 1e-12)
        assertInside(upright)
    }

    @Test fun whereAFingerTakesTheFrame() {
        val c = CropRect(0.2, 0.2, 0.6, 0.6)
        val r = 0.05
        assertEquals(CropGrab.TOP_LEFT, PhotoEdit.grabAt(c, 0.2, 0.2, r, r))
        // Just outside a corner still takes it.
        assertEquals(CropGrab.TOP_LEFT, PhotoEdit.grabAt(c, 0.16, 0.17, r, r))
        assertEquals(CropGrab.BOTTOM_RIGHT, PhotoEdit.grabAt(c, 0.82, 0.79, r, r))
        assertEquals(CropGrab.TOP_RIGHT, PhotoEdit.grabAt(c, 0.8, 0.2, r, r))
        assertEquals(CropGrab.BOTTOM_LEFT, PhotoEdit.grabAt(c, 0.2, 0.8, r, r))
        // Along a side, anywhere: its edge.
        assertEquals(CropGrab.LEFT, PhotoEdit.grabAt(c, 0.21, 0.5, r, r))
        assertEquals(CropGrab.RIGHT, PhotoEdit.grabAt(c, 0.83, 0.3, r, r))
        assertEquals(CropGrab.TOP, PhotoEdit.grabAt(c, 0.5, 0.18, r, r))
        assertEquals(CropGrab.BOTTOM, PhotoEdit.grabAt(c, 0.6, 0.81, r, r))
        // Inside: it moves.
        assertEquals(CropGrab.MOVE, PhotoEdit.grabAt(c, 0.5, 0.5, r, r))
        // Away from it: nothing.
        assertNull(PhotoEdit.grabAt(c, 0.05, 0.5, r, r))
        assertNull(PhotoEdit.grabAt(c, 0.5, 0.95, r, r))
        // A small frame keeps a middle to move it by: a corner reaches at most a quarter in.
        val small = CropRect(0.45, 0.45, 0.08, 0.08)
        assertEquals(CropGrab.MOVE, PhotoEdit.grabAt(small, 0.49, 0.49, r, r))
        assertEquals(CropGrab.TOP_LEFT, PhotoEdit.grabAt(small, 0.455, 0.455, r, r))
        assertEquals(CropGrab.TOP_LEFT, PhotoEdit.grabAt(small, 0.42, 0.42, r, r))
    }

    @Test fun movingKeepsTheFrameInsideThePhoto() {
        val c = CropRect(0.2, 0.3, 0.5, 0.4)
        assertRect(CropRect(0.3, 0.2, 0.5, 0.4), PhotoEdit.dragged(c, CropGrab.MOVE, 0.1, -0.1, null, 1.5, 0.05, 0.05))
        // Pushed past the edges, it stops there, its size kept.
        assertRect(CropRect(0.5, 0.0, 0.5, 0.4), PhotoEdit.dragged(c, CropGrab.MOVE, 0.9, -0.9, null, 1.5, 0.05, 0.05))
        assertRect(CropRect(0.0, 0.6, 0.5, 0.4), PhotoEdit.dragged(c, CropGrab.MOVE, -0.9, 0.9, null, 1.5, 0.05, 0.05))
    }

    @Test fun aFreeFrameResizesBySidesAndCorners() {
        val c = CropRect(0.2, 0.2, 0.6, 0.6)
        // A corner moves its two sides; the opposite ones stay.
        assertRect(CropRect(0.3, 0.1, 0.5, 0.7), PhotoEdit.dragged(c, CropGrab.TOP_LEFT, 0.1, -0.1, null, 1.0, 0.05, 0.05), "corner")
        // An edge moves only its side.
        assertRect(CropRect(0.2, 0.2, 0.7, 0.6), PhotoEdit.dragged(c, CropGrab.RIGHT, 0.1, 0.3, null, 1.0, 0.05, 0.05), "edge")
        assertRect(CropRect(0.2, 0.2, 0.6, 0.3), PhotoEdit.dragged(c, CropGrab.BOTTOM, 0.4, -0.3, null, 1.0, 0.05, 0.05), "bottom")
        // Never past the photo's edges.
        assertRect(CropRect(0.0, 0.0, 0.8, 0.8), PhotoEdit.dragged(c, CropGrab.TOP_LEFT, -0.5, -0.5, null, 1.0, 0.05, 0.05), "past the edge")
        assertRect(CropRect(0.2, 0.2, 0.8, 0.8), PhotoEdit.dragged(c, CropGrab.BOTTOM_RIGHT, 0.5, 0.5, null, 1.0, 0.05, 0.05), "past the far edge")
        // Never smaller than its smallest, nor turned inside out.
        assertRect(CropRect(0.75, 0.2, 0.05, 0.6), PhotoEdit.dragged(c, CropGrab.LEFT, 0.9, 0.0, null, 1.0, 0.05, 0.05), "smallest")
        assertRect(CropRect(0.2, 0.2, 0.6, 0.1), PhotoEdit.dragged(c, CropGrab.BOTTOM, 0.0, -0.9, null, 1.0, 0.1, 0.1), "smallest height")
        // Measured from where the drag started: back where it began, as it was.
        assertRect(c, PhotoEdit.dragged(c, CropGrab.TOP_LEFT, 0.0, 0.0, null, 1.0, 0.05, 0.05), "back")
    }

    @Test fun aShapedFrameKeepsItsShapeAsItIsDragged() {
        // A 4:3 photo cropped square: the frame's pixels stay square.
        val photo = 4.0 / 3.0
        val square = PhotoEdit.fitted(CropRect.WHOLE, 1.0, photo)
        fun pixelRatio(c: CropRect) = c.w * photo / c.h
        for (grab in CropGrab.entries.filter { it != CropGrab.MOVE }) {
            for ((dx, dy) in listOf(-0.3 to -0.2, -0.1 to 0.0, 0.0 to -0.1, 0.2 to 0.3, -0.5 to 0.4, 0.6 to -0.6)) {
                val c = PhotoEdit.dragged(square, grab, dx, dy, 1.0, photo, 0.05, 0.05)
                assertEquals("$grab $dx $dy", 1.0, pixelRatio(c), 1e-9)
                assertInside(c)
                assertTrue("$grab $dx $dy smallest", c.w >= 0.05 - 1e-12 && c.h >= 0.05 - 1e-12)
            }
        }
        // The corner opposite the one held stays put.
        val shrunk = PhotoEdit.dragged(square, CropGrab.BOTTOM_RIGHT, -0.3, -0.3, 1.0, photo, 0.05, 0.05)
        assertEquals(square.x, shrunk.x, 1e-12)
        assertEquals(square.y, shrunk.y, 1e-12)
        assertTrue(shrunk.w < square.w)
        // A motion along one axis only still resizes it, along the diagonal.
        val sideways = PhotoEdit.dragged(square, CropGrab.BOTTOM_RIGHT, -0.3, 0.0, 1.0, photo, 0.05, 0.05)
        assertTrue(sideways.w < square.w)
        // An edge keeps the frame centred across it.
        val c = CropRect(0.3, 0.3, 0.3, 0.4) // 0.4 x 0.4 in pixel units of a 4:3 photo: square
        val wider = PhotoEdit.dragged(c, CropGrab.RIGHT, 0.075, 0.0, 1.0, photo, 0.05, 0.05)
        assertRect(CropRect(0.3, 0.25, 0.375, 0.5), wider, "edge")
    }

    @Test fun aSmallerThanSmallestFrameStillStaysInside() {
        // A frame below the smallest (the screen grew the smallest since)
        // dragged against an edge stays inside the photo.
        val c = CropRect(0.0, 0.0, 0.02, 0.02)
        val left = PhotoEdit.dragged(c, CropGrab.LEFT, -0.1, 0.0, null, 1.0, 0.05, 0.05)
        assertInside(left)
        val right = PhotoEdit.dragged(CropRect(0.98, 0.0, 0.02, 0.02), CropGrab.RIGHT, 0.1, 0.0, null, 1.0, 0.05, 0.05)
        assertInside(right)
    }

    @Test fun theRequestCarriesTheTurnsTheCropAndWhatWasShown() {
        val crop = CropRect(0.1, 0.2, 0.5, 0.6)
        val replace = PhotoEdit.request("/Camera/IMG_1.HEIC", "abc", 5, crop, asCopy = false, everyCopy = true)
        assertEquals("/Camera/IMG_1.HEIC", replace.path)
        assertEquals("abc", replace.hash)
        // Clockwise, 0 to 3.
        assertEquals(1, replace.quarterTurns)
        assertTrue(replace.hasCrop())
        assertEquals(0.1, replace.crop.x, 1e-12)
        assertEquals(0.2, replace.crop.y, 1e-12)
        assertEquals(0.5, replace.crop.width, 1e-12)
        assertEquals(0.6, replace.crop.height, 1e-12)
        assertFalse(replace.asCopy)
        assertTrue(replace.everyCopy)
        // Anticlockwise is three clockwise turns.
        assertEquals(3, PhotoEdit.request("p", "h", -1, CropRect.WHOLE, asCopy = false).quarterTurns)
        // The whole photo: no crop at all.
        val turnOnly = PhotoEdit.request("p", "h", 2, CropRect.WHOLE, asCopy = false)
        assertFalse(turnOnly.hasCrop())
        assertFalse(turnOnly.everyCopy)
        // A copy never replaces the other copies.
        val copy = PhotoEdit.request("p", "h", 0, crop, asCopy = true, everyCopy = true)
        assertTrue(copy.asCopy)
        assertFalse(copy.everyCopy)
        // A crop a hair past the edge is sent inside it.
        val past = PhotoEdit.request("p", "h", 0, CropRect(-1e-9, 0.5, 1.0, 0.5 + 1e-9), asCopy = false)
        assertEquals(0.0, past.crop.x, 0.0)
        assertTrue(past.crop.x + past.crop.width <= 1.0)
        assertTrue(past.crop.y + past.crop.height <= 1.0)
    }

    @Test fun theRevertRequest() {
        val r = PhotoEdit.revertRequest("/Camera/IMG_1.JPG", "abc", everyCopy = true)
        assertEquals("/Camera/IMG_1.JPG", r.path)
        assertEquals("abc", r.hash)
        assertTrue(r.everyCopy)
        assertFalse(PhotoEdit.revertRequest("p", "h").everyCopy)
    }

    @Test fun theCopiesTheDeviceCounts() {
        fun state(copies: Int) = RespEnvelope.newBuilder().setRespEditState(EditState.newBuilder().setCopies(copies).setEdited(true)).build()
        assertEquals(3, PhotoEdit.copies(state(3)))
        assertEquals(1, PhotoEdit.copies(state(0)))
        // No answer, an error, an older device: one - nobody is asked about copies.
        assertEquals(1, PhotoEdit.copies(null))
        assertEquals(1, PhotoEdit.copies(RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("Update").build()))
    }

    @Test fun whatAnAnswerMeans() {
        val file = File.newBuilder().setPath("/Camera/IMG_1.JPG").setHash("new").build()
        val done = PhotoEdit.answer(RespEnvelope.newBuilder().setRespFile(file).build(), S.appPhotosEditFailed())
        assertEquals(EditAnswer.Done(file), done)
        // The device's words as they come, with its code.
        val demo = PhotoEdit.answer(RespEnvelope.newBuilder().setError(true).setErrorCode("demo").setErrorMessage("This is a demo.").build(), S.appPhotosEditFailed())
        assertEquals("demo", (demo as EditAnswer.Refused).code)
        assertEquals("This is a demo.", demo.text.english())
        // None in words, or no answer: ours.
        val silent = PhotoEdit.answer(RespEnvelope.newBuilder().setError(true).setErrorCode("storage_full").build(), S.appPhotosEditFailed())
        assertEquals("storage_full", (silent as EditAnswer.Refused).code)
        assertEquals("Couldn't save the edit. Try again.", silent.text.english())
        assertEquals("", (PhotoEdit.answer(null, S.appPhotosEditFailed()) as EditAnswer.Refused).code)
        assertEquals("", (PhotoEdit.answer(RespEnvelope.getDefaultInstance(), S.appPhotosEditFailed()) as EditAnswer.Refused).code)
    }

    @Test fun aCopysName() {
        assertEquals("IMG_1 (edited).jpg", PhotoEdit.fileName("/Camera/IMG_1 (edited).jpg"))
        assertEquals("IMG_1.jpg", PhotoEdit.fileName("IMG_1.jpg"))
    }
}
