// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
import UIKit
@testable import OffTheCloud

/// Photo edit (PhotoEditor.swift): the crop as the picture turns, the
/// aspects and the handles, what Save and Revert to Original send, when
/// the viewer offers them, and what the viewer shows once the device has
/// answered. The same rules as Android's and the web's.

private func near(_ a: CropRect, _ b: CropRect, _ slack: Double = 1e-9) -> Bool {
    abs(a.x - b.x) <= slack && abs(a.y - b.y) <= slack && abs(a.width - b.width) <= slack && abs(a.height - b.height) <= slack
}

/// The crop's shape in pixels, on a picture of `size` turned by `edit`.
private func pixelShape(_ edit: PhotoEdit, _ size: CGSize) -> Double {
    let s = edit.shown(size)
    return edit.crop.width * Double(s.width) / (edit.crop.height * Double(s.height))
}

private func item(_ path: String, mime: String = "image/jpeg", local: Bool = false, original: String = "") -> PhotoGalleryVM.Item {
    PhotoGalleryVM.Item(id: path, path: path, mime: mime, size: 1, thumbData: nil, localURL: nil,
                        isLocalOnly: local, hash: "h", originalHash: original)
}

@MainActor
struct PhotoEditCropTests {
    @Test func aTurnTakesTheCropWithThePicture() {
        let c = CropRect(x: 0.1, y: 0.2, width: 0.3, height: 0.4)
        // Clockwise: (1-y-h, x, h, w); anticlockwise: (y, 1-x-w, h, w).
        #expect(near(c.turnedRight, CropRect(x: 0.4, y: 0.1, width: 0.4, height: 0.3)))
        #expect(near(c.turnedLeft, CropRect(x: 0.2, y: 0.6, width: 0.4, height: 0.3)))
        // One way and back, or four the same way: as it was.
        #expect(near(c.turnedRight.turnedLeft, c))
        #expect(near(c.turnedLeft.turnedRight, c))
        #expect(near(c.turnedRight.turnedRight.turnedRight.turnedRight, c))
        #expect(CropRect.whole.turnedRight == .whole && CropRect.whole.turnedLeft == .whole)
    }

    @Test func theEditCountsItsTurnsAndKnowsWhenNothingChanged() {
        var e = PhotoEdit()
        #expect(!e.changed)
        e.turnRight()
        #expect(e.turns == 1 && e.changed)
        e.turnLeft()
        e.turnLeft()
        #expect(e.turns == 3)
        e.turnRight()
        #expect(e.turns == 0 && !e.changed)
        for _ in 0..<4 { e.turnLeft() }
        #expect(e.turns == 0 && !e.changed)
        // A frame dragged out and back again is no crop.
        e.crop = CropRect(x: 0.00002, y: 0, width: 0.99996, height: 1)
        #expect(!e.changed)
        e.crop = CropRect(x: 0.1, y: 0, width: 0.9, height: 1)
        #expect(e.changed)
        e.turnRight()
        e.choose(.square, picture: CGSize(width: 400, height: 300))
        e.reset()
        #expect(e == PhotoEdit())
    }

    @Test func anAspectIsItsLargestFrameCentredWhereTheFrameWas() {
        let landscape = CGSize(width: 4000, height: 3000)
        var e = PhotoEdit()
        e.choose(.square, picture: landscape)
        // 3000 x 3000 of 4000 x 3000: three quarters of the width, centred.
        #expect(near(e.crop, CropRect(x: 0.125, y: 0, width: 0.75, height: 1)))
        #expect(e.aspect == .square)
        e.choose(.sixteenNine, picture: landscape)
        // 4000 x 2250: the whole width, three quarters of the height.
        #expect(near(e.crop, CropRect(x: 0, y: 0.125, width: 1, height: 0.75)))
        // The photo's own shape, and 4:3 on a 4:3 photo: all of it.
        e.choose(.fourThree, picture: landscape)
        #expect(e.crop.isWhole)
        e.choose(.original, picture: landscape)
        #expect(e.crop.isWhole)
        // Free leaves the frame where it is, and holds it to nothing.
        e.crop = CropRect(x: 0.7, y: 0.2, width: 0.2, height: 0.2)
        e.choose(.free, picture: landscape)
        #expect(near(e.crop, CropRect(x: 0.7, y: 0.2, width: 0.2, height: 0.2)))
        #expect(e.ratio == nil)
        // Centred where the frame was (0.8 across), moved inside the photo.
        e.choose(.square, picture: landscape)
        #expect(near(e.crop, CropRect(x: 0.25, y: 0, width: 0.75, height: 1)))
    }

    @Test func anUprightPictureGetsUprightShapes() {
        let upright = CGSize(width: 3000, height: 4000)
        var e = PhotoEdit()
        e.choose(.sixteenNine, picture: upright)
        // 9:16, 2250 x 4000: three quarters of the width.
        #expect(near(e.crop, CropRect(x: 0.125, y: 0, width: 0.75, height: 1)))
        #expect(abs(pixelShape(e, upright) - 9.0 / 16.0) < 1e-9)
        e.choose(.fourThree, picture: upright)
        #expect(e.crop.isWhole)
        // A landscape picture turned is upright: its shapes are too.
        var t = PhotoEdit()
        t.turnRight()
        let landscape = CGSize(width: 4000, height: 3000)
        t.choose(.fourThree, picture: landscape)
        #expect(t.crop.isWhole)
        t.choose(.sixteenNine, picture: landscape)
        #expect(abs(pixelShape(t, landscape) - 9.0 / 16.0) < 1e-9)
    }

    @Test func theShapeTurnsWithThePictureAndAHandleKeepsIt() {
        let picture = CGSize(width: 4000, height: 3000)
        var e = PhotoEdit()
        e.choose(.sixteenNine, picture: picture)
        e.turnRight()
        // 16:9 on the landscape picture is 9:16 on it turned upright.
        #expect(near(e.crop, CropRect(x: 0.125, y: 0, width: 0.75, height: 1)))
        #expect(abs(pixelShape(e, picture) - 9.0 / 16.0) < 1e-9)
        e.drag(from: e.crop, .bottomRight, dx: -0.3, dy: -0.1, minWidth: 0.05, minHeight: 0.05)
        #expect(abs(pixelShape(e, picture) - 9.0 / 16.0) < 1e-9)
        #expect(e.crop.width < 0.75)
        e.drag(from: e.crop, .top, dx: 0, dy: 0.2, minWidth: 0.05, minHeight: 0.05)
        #expect(abs(pixelShape(e, picture) - 9.0 / 16.0) < 1e-9)
        e.turnLeft()
        #expect(abs(pixelShape(e, picture) - 16.0 / 9.0) < 1e-9)
    }

    @Test func freeHandlesStopAtThePhotosEdgesAndTheMinimum() {
        let c = CropRect(x: 0.2, y: 0.2, width: 0.5, height: 0.5)
        // Out past the photo: to its edge; the other edges stay.
        #expect(near(c.dragged(.right, dx: 0.6, dy: 0.3, ratio: nil, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0.2, y: 0.2, width: 0.8, height: 0.5)))
        #expect(near(c.dragged(.left, dx: -0.5, dy: 0, ratio: nil, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0, y: 0.2, width: 0.7, height: 0.5)))
        #expect(near(c.dragged(.bottom, dx: 0.4, dy: 0.1, ratio: nil, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0.2, y: 0.2, width: 0.5, height: 0.6)))
        // A corner in past the opposite one: the minimum, at that corner.
        #expect(near(c.dragged(.topLeft, dx: 0.9, dy: 0.9, ratio: nil, minWidth: 0.1, minHeight: 0.05),
                     CropRect(x: 0.6, y: 0.65, width: 0.1, height: 0.05)))
        #expect(near(c.dragged(.bottomRight, dx: 0.1, dy: -0.1, ratio: nil, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0.2, y: 0.2, width: 0.6, height: 0.4)))
    }

    @Test func movingKeepsTheFrameInsideAndItsSize() {
        let c = CropRect(x: 0.2, y: 0.2, width: 0.5, height: 0.5)
        #expect(near(c.dragged(.move, dx: 0.6, dy: -0.4, ratio: nil, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0.5, y: 0, width: 0.5, height: 0.5)))
        #expect(near(c.dragged(.move, dx: 0.1, dy: 0.1, ratio: 2, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0.3, y: 0.3, width: 0.5, height: 0.5)))
    }

    @Test func aHeldShapeIsKeptByCornersAndEdges() {
        // Twice as wide as high, in fractions.
        let c = CropRect(x: 0.2, y: 0.3, width: 0.4, height: 0.2)
        // A corner moved mostly sideways: the width leads.
        #expect(near(c.dragged(.bottomRight, dx: 0.2, dy: 0.01, ratio: 2, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0.2, y: 0.3, width: 0.6, height: 0.3)))
        // Mostly down: the height leads.
        #expect(near(c.dragged(.bottomRight, dx: 0.01, dy: 0.2, ratio: 2, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0.2, y: 0.3, width: 0.8, height: 0.4)))
        // Past the photo: as large as fits from the opposite corner.
        #expect(near(c.dragged(.bottomRight, dx: 1, dy: 0, ratio: 2, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0.2, y: 0.3, width: 0.8, height: 0.4)))
        #expect(near(c.dragged(.topLeft, dx: -1, dy: -1, ratio: 2, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0, y: 0.2, width: 0.6, height: 0.3)))
        // An edge: the frame grows from the opposite edge, centred across.
        #expect(near(c.dragged(.right, dx: 0.2, dy: 0, ratio: 2, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0.2, y: 0.25, width: 0.6, height: 0.3)))
        #expect(near(c.dragged(.bottom, dx: 0, dy: 0.1, ratio: 2, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0.1, y: 0.3, width: 0.6, height: 0.3)))
        // ...moved inside where it would cross the photo's edge.
        let atTop = CropRect(x: 0.2, y: 0, width: 0.4, height: 0.2)
        #expect(near(atTop.dragged(.left, dx: -0.2, dy: 0, ratio: 2, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0, y: 0, width: 0.6, height: 0.3)))
        // Never under the minimum, in its shape: wide enough and high enough.
        #expect(near(c.dragged(.topLeft, dx: 1, dy: 1, ratio: 2, minWidth: 0.05, minHeight: 0.05),
                     CropRect(x: 0.5, y: 0.45, width: 0.1, height: 0.05)))
    }

    @Test func aTouchTakesTheNearestHandle() {
        let f = CGRect(x: 100, y: 100, width: 200, height: 100)
        #expect(CropHandle.at(CGPoint(x: 95, y: 98), frame: f, reach: 24) == .topLeft)
        #expect(CropHandle.at(CGPoint(x: 310, y: 95), frame: f, reach: 24) == .topRight)
        #expect(CropHandle.at(CGPoint(x: 110, y: 210), frame: f, reach: 24) == .bottomLeft)
        #expect(CropHandle.at(CGPoint(x: 310, y: 205), frame: f, reach: 24) == .bottomRight)
        #expect(CropHandle.at(CGPoint(x: 200, y: 90), frame: f, reach: 24) == .top)
        #expect(CropHandle.at(CGPoint(x: 200, y: 215), frame: f, reach: 24) == .bottom)
        #expect(CropHandle.at(CGPoint(x: 85, y: 150), frame: f, reach: 24) == .left)
        #expect(CropHandle.at(CGPoint(x: 290, y: 150), frame: f, reach: 24) == .right)
        #expect(CropHandle.at(CGPoint(x: 200, y: 150), frame: f, reach: 24) == .move)
        // Too far out: nothing.
        #expect(CropHandle.at(CGPoint(x: 20, y: 150), frame: f, reach: 24) == nil)
        #expect(CropHandle.at(CGPoint(x: 200, y: 400), frame: f, reach: 24) == nil)
        // A small frame: still resizable by its corners, and moved by its
        // middle.
        let s = CGRect(x: 100, y: 100, width: 30, height: 30)
        #expect(CropHandle.at(CGPoint(x: 128, y: 129), frame: s, reach: 24) == .bottomRight)
        #expect(CropHandle.at(CGPoint(x: 102, y: 101), frame: s, reach: 24) == .topLeft)
        #expect(CropHandle.at(CGPoint(x: 92, y: 135), frame: s, reach: 24) == .bottomLeft)
        #expect(CropHandle.at(CGPoint(x: 115, y: 115), frame: s, reach: 24) == .move)
    }

    @Test func theLeastFrameIsATwentiethOrAFingersWidth() {
        let big = PhotoEditRules.minimum(shownAt: CGSize(width: 1000, height: 400))
        #expect(big.width == 0.05)
        #expect(abs(big.height - 0.1) < 1e-9)
        let tiny = PhotoEditRules.minimum(shownAt: CGSize(width: 20, height: 0))
        #expect(tiny.width == 1 && tiny.height == 1)
    }
}

@MainActor
struct PhotoEditRequestTests {
    @Test func saveSendsTheTurnsTheCropAndTheContentShown() {
        var e = PhotoEdit()
        e.turnLeft()
        let turned = PhotoEditRules.request(path: "/p/IMG_1.HEIC", hash: "h1", edit: e, asCopy: false, everyCopy: true)
        #expect(turned.path == "/p/IMG_1.HEIC" && turned.hash == "h1")
        #expect(turned.quarterTurns == 3)
        // Only turned: no crop at all.
        #expect(!turned.hasCrop)
        #expect(turned.everyCopy && !turned.asCopy)
        e.crop = CropRect(x: 0.1, y: 0.2, width: 0.5, height: 0.6)
        let cropped = PhotoEditRules.request(path: "/p/IMG_1.HEIC", hash: "h1", edit: e, asCopy: true, everyCopy: true)
        #expect(cropped.hasCrop)
        #expect(cropped.crop.x == 0.1 && cropped.crop.y == 0.2 && cropped.crop.width == 0.5 && cropped.crop.height == 0.6)
        // A copy changes no other copy.
        #expect(cropped.asCopy && !cropped.everyCopy)
        // Sent inside the photo.
        e.crop = CropRect(x: -0.01, y: 0.5, width: 0.6, height: 0.6)
        let inside = PhotoEditRules.request(path: "/a.jpg", hash: "", edit: e, asCopy: false, everyCopy: false)
        #expect(inside.crop.x == 0 && abs(inside.crop.y - 0.4) < 1e-12)
        #expect(!inside.everyCopy)
        let revert = PhotoEditRules.revertRequest(path: "/a.jpg", hash: "h2", everyCopy: true)
        #expect(revert.path == "/a.jpg" && revert.hash == "h2" && revert.everyCopy)
    }

    @Test func theCopiesAreTheDevicesOrOne() {
        func state(_ n: Int32) -> Msg_RespEnvelope {
            var s = Msg_EditState()
            s.copies = n
            var r = Msg_RespEnvelope()
            r.payload = .respEditState(s)
            return r
        }
        var old = Msg_RespEnvelope()
        old.error = true
        old.errorCode = "unknown_payload"
        old.errorMessage = "This device does not understand that request"
        #expect(PhotoEditRules.copies(state(3)) == 3)
        #expect(PhotoEditRules.copies(state(1)) == 1)
        #expect(PhotoEditRules.copies(state(0)) == 1)
        // No answer, or a device before photo edits: no copies question.
        #expect(PhotoEditRules.copies(nil) == 1)
        #expect(PhotoEditRules.copies(old) == 1)
        #expect(PhotoEditRules.words(old) == "This device does not understand that request")
        #expect(PhotoEditRules.words(state(2)) == nil)
        var bare = Msg_RespEnvelope()
        bare.error = true
        #expect(PhotoEditRules.words(bare) == nil)
    }

    @Test func editIsOfferedForWhatTheDeviceCanEdit() {
        for m in ["image/jpeg", "image/png", "image/heic", "image/heif", "image/avif", "image/webp", "IMAGE/JPEG"] {
            #expect(PhotoEditRules.editable(mime: m, path: "/p/x"), "\(m)")
        }
        for m in ["video/mp4", "video/quicktime", "image/gif", "image/tiff", "image/svg+xml", "image/x-canon-cr2",
                  "image/x-adobe-dng", "image/dng", "application/pdf", ""] {
            #expect(!PhotoEditRules.editable(mime: m, path: "/p/x.jpg"), "\(m)")
        }
        // A HEIC by its name, in any case, whatever type it came with -
        // but never a video.
        #expect(PhotoEditRules.editable(mime: "application/octet-stream", path: "/p/IMG_1.HEIC"))
        #expect(PhotoEditRules.editable(mime: "", path: "/p/x.heif"))
        #expect(!PhotoEditRules.editable(mime: "video/quicktime", path: "/p/x.heic"))
        // Only the owner's photos on the device.
        #expect(PhotoEditRules.offersEdit(item("/p/a.jpg")))
        #expect(!PhotoEditRules.offersEdit(item("/p/a.jpg", local: true)))
        #expect(!PhotoEditRules.offersEdit(item("")))
        #expect(!PhotoEditRules.offersEdit(item("/p/a.mov", mime: "video/quicktime")))
    }

    @Test func revertIsOfferedForAnEditOnly() {
        #expect(!PhotoEditRules.offersRevert(item("/p/a.jpg")))
        #expect(PhotoEditRules.offersRevert(item("/p/a.jpg", original: "o1")))
        #expect(!PhotoEditRules.offersRevert(item("/p/a.jpg", local: true, original: "o1")))
    }
}

/// A picture of `size` px, its left half red and its right half blue.
private func halves(_ size: CGSize) -> UIImage {
    let format = UIGraphicsImageRendererFormat()
    format.scale = 1
    return UIGraphicsImageRenderer(size: size, format: format).image { ctx in
        UIColor.red.setFill()
        ctx.fill(CGRect(x: 0, y: 0, width: size.width / 2, height: size.height))
        UIColor.blue.setFill()
        ctx.fill(CGRect(x: size.width / 2, y: 0, width: size.width / 2, height: size.height))
    }
}

/// Whether the pixel at (x, y) is mostly red (true) or mostly blue.
private func isRed(_ image: UIImage, _ x: Int, _ y: Int) -> Bool? {
    guard let cg = image.cgImage else { return nil }
    var px = [UInt8](repeating: 0, count: 4)
    guard let ctx = CGContext(data: &px, width: 1, height: 1, bitsPerComponent: 8, bytesPerRow: 4,
                              space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return nil }
    // The pixel wanted at the context's only one (its origin is the
    // bottom left).
    ctx.draw(cg, in: CGRect(x: -x, y: -(cg.height - 1 - y), width: cg.width, height: cg.height))
    return px[0] > px[2]
}

@MainActor
struct PhotoEditRenderTests {
    @Test func thePreviewIsTurnedThenCropped() throws {
        let picture = halves(CGSize(width: 4, height: 2))
        let right = try #require(PhotoEditRender.render(picture, turns: 1, crop: .whole))
        #expect(right.size == CGSize(width: 2, height: 4))
        // Turned clockwise, the left half is the top.
        #expect(isRed(right, 0, 0) == true)
        #expect(isRed(right, 1, 3) == false)
        let left = try #require(PhotoEditRender.render(picture, turns: 3, crop: .whole))
        #expect(isRed(left, 0, 0) == false)
        #expect(isRed(left, 1, 3) == true)
        // The crop is of the turned picture: its lower half is the blue.
        let lower = try #require(PhotoEditRender.render(picture, turns: 1, crop: CropRect(x: 0, y: 0.5, width: 1, height: 0.5)))
        #expect(lower.size == CGSize(width: 2, height: 2))
        #expect(isRed(lower, 0, 0) == false && isRed(lower, 1, 1) == false)
        // Never more than asked for on its longer side.
        let big = try #require(PhotoEditRender.render(halves(CGSize(width: 400, height: 200)), turns: 0, crop: .whole, maxSide: 100))
        #expect(big.size == CGSize(width: 100, height: 50))
    }
}

/// The device, for the viewer's edits: Images' pages, GetEditState,
/// EditImage and RevertImage as told.
@MainActor
final class EditDevice {
    var searches = 0
    var editStates: [String] = []
    var edits: [Msg_EditImage] = []
    var reverts: [Msg_RevertImage] = []
    var gets: [String] = []
    /// GetEditState's copies; 0 answers unknown_payload (an older device).
    var copies: Int32 = 1
    /// How an edit or a revert is refused (nil: done).
    var refuse: (code: String, message: String)?
    /// No answer at all to an edit (the connection dropped).
    var drop = false

    func file(_ i: Int) -> Msg_File {
        var f = Msg_File()
        f.path = i == 0 ? "/photos/0.HEIC" : "/photos/\(i).jpg"
        f.mime = i == 0 ? "image/heic" : "image/jpeg"
        f.hash = "h\(i)"
        f.size64 = 100
        f.created = Google_Protobuf_Timestamp(date: Date(timeIntervalSince1970: 1_780_000_000 - Double(i) * 3600))
        // The second one is an edit already.
        if i == 1 { f.originalHash = "o1" }
        return f
    }

    private func refused() -> Msg_RespEnvelope? {
        guard let refuse else { return nil }
        var r = Msg_RespEnvelope()
        r.error = true
        r.errorCode = refuse.code
        r.errorMessage = refuse.message
        return r
    }

    func attach(_ vm: PhotoGalleryVM) {
        vm.pageRetry.delay = { _ in 0.05 }
        vm.bucketRetry.delay = { _ in 0.05 }
        vm.bigThumbDelay = { _ in 10 }
        let photos = FullPhotoCache(root: FileManager.default.temporaryDirectory.appendingPathComponent("edit-\(UUID().uuidString)"),
                                    limit: 100_000_000)
        vm.fullPhotos = photos
        let hearts = FavouritesModel()
        hearts.request = { _ in throw CancellationError() }
        vm.favourites = hearts
        vm.photoRequest = { [weak self] payload, _ in
            guard let self else { throw CancellationError() }
            var r = Msg_RespEnvelope()
            switch payload {
            case .reqSearchPhotos:
                self.searches += 1
                var lof = Msg_ListOfFiles()
                lof.files = (0..<3).map(self.file)
                r.payload = .respListOfFiles(lof)
            case .reqPhotoDateBuckets:
                r.payload = .respPhotoDateBuckets(Msg_RespPhotoDateBuckets())
            case .reqGetEditState(let g):
                self.editStates.append(g.path)
                if self.copies == 0 {
                    r.error = true
                    r.errorCode = "unknown_payload"
                    r.errorMessage = "This device does not understand that request"
                } else {
                    var s = Msg_EditState()
                    s.copies = self.copies
                    r.payload = .respEditState(s)
                }
            case .reqEditImage(let e):
                self.edits.append(e)
                if self.drop { throw WSClient.TimedOut() }
                if let no = self.refused() { return no }
                var f = Msg_File()
                // A HEIC replaced becomes a JPEG; a copy goes next to it.
                f.path = e.asCopy ? "/photos/0 (edited).jpg" : e.path.replacingOccurrences(of: ".HEIC", with: ".jpg")
                f.mime = "image/jpeg"
                f.hash = "e-" + e.hash
                f.size64 = 90
                f.originalHash = e.hash
                r.payload = .respFile(f)
            case .reqRevertImage(let rv):
                self.reverts.append(rv)
                if let no = self.refused() { return no }
                var f = Msg_File()
                f.path = rv.path
                f.mime = "image/jpeg"
                f.hash = "orig-" + rv.hash
                f.size64 = 120
                r.payload = .respFile(f)
            case .reqGetFile(let g):
                // The full size never comes: the thumbnail stays.
                self.gets.append(g.path)
                r.error = true
            case .reqGetThumbnails:
                r.error = true
                r.errorCode = "unknown_payload"
            default:
                throw CancellationError()
            }
            return r
        }
        vm.libraryRequest = { _, _ in throw CancellationError() }
        vm.peopleRequest = { _, _ in throw CancellationError() }
    }
}

@MainActor
private func waitUntil(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct PhotoEditGalleryTests {
    private func model() async -> (PhotoGalleryVM, EditDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = EditDevice()
        device.attach(vm)
        await vm.resetAndLoadFirstPage()
        return (vm, device)
    }

    private func turned() -> PhotoEdit {
        var e = PhotoEdit()
        e.turnRight()
        return e
    }

    @Test func theViewerOffersEditAndRevertByWhatItShows() async {
        let (vm, _) = await model()
        #expect(!vm.editOffered && !vm.revertOffered)
        vm.open(index: 0)
        #expect(vm.items[0].originalHash.isEmpty)
        #expect(vm.editOffered && !vm.revertOffered)
        vm.open(index: 1)
        #expect(vm.items[1].originalHash == "o1")
        #expect(vm.editOffered && vm.revertOffered)
        vm.closeModal()
    }

    @Test func replacedTheEditTakesThePhotosPlace() async {
        let (vm, device) = await model()
        vm.open(index: 0)
        vm.selected = ["/photos/0.HEIC"]
        let shown = vm.items[0]
        #expect(await vm.editCopies(of: shown.path) == 1)
        #expect(device.editStates == ["/photos/0.HEIC"])
        let picture = halves(CGSize(width: 40, height: 20))
        let outcome = await vm.saveEdit(turned(), of: shown, picture: picture, copies: 1, asCopy: false, everyCopy: false)
        #expect(outcome == .saved)
        let sent = device.edits.first
        #expect(sent?.path == "/photos/0.HEIC" && sent?.hash == "h0" && sent?.quarterTurns == 1)
        #expect(sent?.asCopy == false && sent?.everyCopy == false && sent?.hasCrop == false)
        // In its place, by its new path and content: an edit now.
        let now = vm.items[0]
        #expect(now.path == "/photos/0.jpg" && now.mime == "image/jpeg")
        #expect(now.hash == "e-h0" && now.originalHash == "h0")
        #expect(now.created == shown.created && now.month == shown.month)
        #expect(vm.items.count == 3)
        #expect(vm.openIndex == 0 && vm.revertOffered)
        #expect(vm.selected == ["/photos/0.jpg"])
        // Shown from the edit made here until the device's full size comes,
        // which is asked for.
        #expect(now.thumbImage?.size == CGSize(width: 20, height: 40))
        await waitUntil { device.gets.contains("/photos/0.jpg") }
        #expect(device.gets.contains("/photos/0.jpg"))
        // Nothing else changed: the grid isn't read again.
        let searches = device.searches
        vm.closeModal()
        try? await Task.sleep(nanoseconds: 200_000_000)
        #expect(device.searches == searches)
    }

    @Test func everyCopyOrOnlyHereAndTheGridAgainForTheCopiesLeft() async {
        let (vm, device) = await model()
        device.copies = 3
        vm.open(index: 2)
        #expect(await vm.editCopies(of: "/photos/2.jpg") == 3)
        #expect(await vm.saveEdit(turned(), of: vm.items[2], picture: nil, copies: 3, asCopy: false, everyCopy: true) == .saved)
        #expect(device.edits.last?.everyCopy == true)
        // Every copy holds the edit now.
        #expect(vm.items[2].copies == 3)
        #expect(vm.items[2].thumbImage == nil)
        // Only here: the other copies keep the photo, so the grid is read
        // again - once the viewer closes, not under it.
        let searches = device.searches
        #expect(await vm.saveEdit(turned(), of: vm.items[2], picture: nil, copies: 3, asCopy: false, everyCopy: false) == .saved)
        #expect(device.edits.last?.everyCopy == false)
        #expect(vm.items[2].copies == nil)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.searches == searches)
        vm.closeModal()
        await waitUntil { device.searches > searches }
        #expect(device.searches == searches + 1)
    }

    @Test func anOlderDeviceOrNoAnswerAsksNoCopiesQuestion() async {
        let (vm, device) = await model()
        device.copies = 0
        #expect(await vm.editCopies(of: "/photos/2.jpg") == 1)
        vm.photoRequest = { _, _ in throw WSClient.TimedOut() }
        #expect(await vm.editCopies(of: "/photos/2.jpg") == 1)
    }

    @Test func aCopyIsSaidByItsNameAndAppearsAsTheViewerCloses() async {
        let (vm, device) = await model()
        vm.open(index: 0)
        let searches = device.searches
        var e = PhotoEdit()
        e.crop = CropRect(x: 0.1, y: 0.1, width: 0.5, height: 0.5)
        #expect(await vm.saveEdit(e, of: vm.items[0], picture: nil, copies: 1, asCopy: true, everyCopy: true) == .saved)
        #expect(device.edits.last?.asCopy == true && device.edits.last?.everyCopy == false)
        #expect(device.edits.last?.hasCrop == true)
        // Over the viewer, which stays on the photo as it was.
        #expect(vm.showAlert && vm.alertShown(overViewer: true))
        #expect(vm.notice?.text == S.appPhotosEditSavedCopy(name: "0 (edited).jpg"))
        #expect(vm.openIndex == 0 && vm.items[0].hash == "h0")
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.searches == searches)
        vm.showAlert = false
        vm.closeModal()
        await waitUntil { device.searches > searches }
        #expect(device.searches == searches + 1)
    }

    @Test func aRefusalSaysTheDevicesWordsAndChangesNothing() async {
        let (vm, device) = await model()
        vm.open(index: 0)
        device.refuse = ("demo", "This is a demo device: nothing can be changed.")
        #expect(await vm.saveEdit(turned(), of: vm.items[0], picture: nil, copies: 1, asCopy: false, everyCopy: false)
                == .failed("This is a demo device: nothing can be changed."))
        device.refuse = ("unknown_payload", "This device does not understand that request: its software is older than the app.")
        #expect(await vm.saveEdit(turned(), of: vm.items[0], picture: nil, copies: 1, asCopy: true, everyCopy: false)
                == .failed("This device does not understand that request: its software is older than the app."))
        // Without the device's words, or without an answer: failed.
        device.refuse = ("storage_full", "")
        #expect(await vm.saveEdit(turned(), of: vm.items[0], picture: nil, copies: 1, asCopy: false, everyCopy: false)
                == .failed(S.appPhotosEditFailed))
        device.refuse = nil
        device.drop = true
        #expect(await vm.saveEdit(turned(), of: vm.items[0], picture: nil, copies: 1, asCopy: false, everyCopy: false)
                == .failed(S.appPhotosEditFailed))
        #expect(vm.items[0].hash == "h0" && vm.items[0].path == "/photos/0.HEIC")
        #expect(!vm.showAlert)
        vm.closeModal()
    }

    @Test func revertAsksAboutTheCopiesThenPutsTheOriginalBack() async {
        let (vm, device) = await model()
        device.copies = 2
        vm.open(index: 1)
        vm.askRevert()
        #expect(vm.revertingPath == "/photos/1.jpg")
        await waitUntil { vm.revertAsk != nil }
        #expect(vm.revertingPath == nil)
        let ask = vm.revertAsk
        #expect(ask?.copies == 2 && ask?.item.path == "/photos/1.jpg")
        guard let ask else { return }
        vm.revert(ask, everyCopy: true)
        #expect(vm.revertAsk == nil && vm.revertingPath == "/photos/1.jpg")
        await waitUntil { vm.revertingPath == nil }
        #expect(device.reverts.count == 1)
        #expect(device.reverts[0].path == "/photos/1.jpg" && device.reverts[0].hash == "h1" && device.reverts[0].everyCopy)
        // The original in its place: no edit any more.
        #expect(vm.items[1].hash == "orig-h1" && vm.items[1].originalHash.isEmpty)
        #expect(vm.items[1].copies == 2)
        #expect(vm.openIndex == 1 && !vm.revertOffered)
        vm.closeModal()
    }

    @Test func aRefusedRevertSaysWhy() async {
        let (vm, device) = await model()
        vm.open(index: 1)
        device.refuse = ("not_edited", "This photo is not an edit.")
        let searches = device.searches
        vm.revert(RevertAsk(item: vm.items[1], copies: 1), everyCopy: true)
        await waitUntil { vm.showAlert }
        // One copy: never every copy.
        #expect(device.reverts.last?.everyCopy == false)
        #expect(vm.notice?.text == "This photo is not an edit.")
        // Said over the viewer, which stays; nothing read again.
        #expect(vm.openIndex == 1 && vm.alertShown(overViewer: true))
        #expect(vm.items[1].hash == "h1")
        #expect(device.searches == searches)
        vm.showAlert = false
        device.refuse = ("demo", "")
        vm.revert(RevertAsk(item: vm.items[1], copies: 1), everyCopy: false)
        await waitUntil { vm.showAlert }
        #expect(vm.notice?.text == S.appPhotosEditRevertFailed)
        vm.closeModal()
    }

    /// "changed": the photo holds other content than was shown, so Save
    /// (or Revert) would only fail the same way - the viewer closes, the
    /// device's words are said under it, and the photos are read again, as
    /// Delete's "changed" (Android the same).
    @Test func changedClosesTheViewerAndReadsThePhotosAgain() async {
        let (vm, device) = await model()
        vm.open(index: 0)
        device.refuse = ("changed", "This photo has changed since it was shown.")
        var searches = device.searches
        #expect(await vm.saveEdit(turned(), of: vm.items[0], picture: nil, copies: 1, asCopy: false, everyCopy: false) == .outOfDate)
        #expect(vm.openIndex == nil)
        #expect(vm.showAlert && vm.alertShown(overViewer: false))
        #expect(vm.notice?.text == "This photo has changed since it was shown.")
        await waitUntil { device.searches > searches }
        #expect(device.searches == searches + 1)
        await waitUntil { !vm.loading && vm.items.count == 3 }
        // A copy refused so too.
        vm.showAlert = false
        vm.open(index: 0)
        searches = device.searches
        #expect(await vm.saveEdit(turned(), of: vm.items[0], picture: nil, copies: 1, asCopy: true, everyCopy: false) == .outOfDate)
        #expect(vm.openIndex == nil)
        await waitUntil { device.searches > searches }
        await waitUntil { !vm.loading && vm.items.count == 3 }
        // Without the device's words: failed.
        vm.showAlert = false
        device.refuse = ("changed", "")
        vm.open(index: 0)
        searches = device.searches
        #expect(await vm.saveEdit(turned(), of: vm.items[0], picture: nil, copies: 1, asCopy: false, everyCopy: false) == .outOfDate)
        #expect(vm.notice?.text == S.appPhotosEditFailed)
        await waitUntil { device.searches > searches }
        await waitUntil { !vm.loading && vm.items.count == 3 }
        // Revert to Original the same.
        vm.showAlert = false
        device.refuse = ("changed", "This photo has changed since it was shown.")
        vm.open(index: 1)
        searches = device.searches
        vm.revert(RevertAsk(item: vm.items[1], copies: 1), everyCopy: false)
        await waitUntil { vm.showAlert }
        #expect(vm.openIndex == nil && vm.alertShown(overViewer: false))
        #expect(vm.notice?.text == "This photo has changed since it was shown.")
        await waitUntil { device.searches > searches }
        #expect(device.searches == searches + 1)
    }

    @Test func changedInFilesClosesTheViewerAndListsTheFolderAgain() async {
        let viewer = PhotoGalleryVM()
        let device = EditDevice()
        device.attach(viewer)
        device.refuse = ("changed", "This photo has changed since it was shown.")
        var relisted: [String] = []
        viewer.onDeleted = { relisted.append($0) }
        let photo = PhotoGalleryVM.Item(id: "/Docs/a.jpg#ha", path: "/Docs/a.jpg", mime: "image/jpeg", size: 1,
                                        thumbData: nil, localURL: nil, isLocalOnly: false, hash: "ha", originalHash: "o")
        viewer.showFiles([photo], startAt: 0)
        #expect(await viewer.saveEdit(turned(), of: photo, picture: nil, copies: 1, asCopy: false, everyCopy: false) == .outOfDate)
        #expect(viewer.openIndex == nil)
        #expect(relisted == ["/Docs/a.jpg"])
        #expect(viewer.notice?.text == "This photo has changed since it was shown.")
        viewer.showAlert = false
        viewer.showFiles([photo], startAt: 0)
        viewer.revert(RevertAsk(item: photo, copies: 1), everyCopy: false)
        await waitUntil { viewer.showAlert }
        #expect(viewer.openIndex == nil)
        #expect(relisted == ["/Docs/a.jpg", "/Docs/a.jpg"])
        // A fixed list reads no search of its own.
        #expect(device.searches == 0)
    }

    @Test func filesViewerListsItsFolderAgainAfterAnEdit() async {
        let viewer = PhotoGalleryVM()
        let device = EditDevice()
        device.attach(viewer)
        var relisted: [String] = []
        viewer.onDeleted = { relisted.append($0) }
        viewer.showFiles([PhotoGalleryVM.Item(id: "/Docs/a.jpg#ha", path: "/Docs/a.jpg", mime: "image/jpeg", size: 1,
                                              thumbData: nil, localURL: nil, isLocalOnly: false, hash: "ha")], startAt: 0)
        #expect(viewer.editOffered)
        #expect(await viewer.saveEdit(turned(), of: viewer.items[0], picture: nil, copies: 1, asCopy: false, everyCopy: false) == .saved)
        #expect(viewer.items[0].hash == "e-ha" && viewer.revertOffered)
        #expect(await viewer.saveEdit(turned(), of: viewer.items[0], picture: nil, copies: 1, asCopy: true, everyCopy: false) == .saved)
        #expect(relisted == ["/Docs/a.jpg", "/photos/0 (edited).jpg"])
        #expect(viewer.notice?.text == S.appPhotosEditSavedCopy(name: "0 (edited).jpg"))
        // A fixed list has no grid to read again as it closes.
        viewer.closeModal()
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.searches == 0)
    }

    /// The full size of the photo as it was, still on its way when the
    /// edit replaced it at the same path, is not shown for the edit: the
    /// edit's own is asked for and kept.
    @Test func aFullSizeOnItsWayForTheOldContentIsDropped() async {
        let viewer = PhotoGalleryVM()
        let device = EditDevice()
        device.attach(viewer)
        let wide = halves(CGSize(width: 40, height: 20)).jpegData(compressionQuality: 0.9) ?? Data()
        let high = halves(CGSize(width: 20, height: 40)).jpegData(compressionQuality: 0.9) ?? Data()
        let edit = viewer.photoRequest
        var asked: [String] = []
        viewer.photoRequest = { payload, kind in
            guard case .reqGetFile(let g) = payload else { return try await edit(payload, kind) }
            asked.append(g.path)
            var f = Msg_File()
            f.path = g.path
            if asked.count == 1 {
                // The old content's, slow.
                try? await Task.sleep(nanoseconds: 300_000_000)
                f.hash = "ha"
                f.content = wide
            } else {
                f.hash = "e-ha"
                f.content = high
            }
            var r = Msg_RespEnvelope()
            r.payload = .respFile(f)
            return r
        }
        viewer.showFiles([PhotoGalleryVM.Item(id: "/Docs/a.jpg#ha", path: "/Docs/a.jpg", mime: "image/jpeg", size: 1,
                                              thumbData: nil, localURL: nil, isLocalOnly: false, hash: "ha")], startAt: 0)
        await waitUntil { asked.count == 1 }
        #expect(await viewer.saveEdit(turned(), of: viewer.items[0], picture: nil, copies: 1, asCopy: false, everyCopy: false) == .saved)
        #expect(viewer.items[0].path == "/Docs/a.jpg" && viewer.items[0].hash == "e-ha")
        await waitUntil { asked.count == 2 && viewer.hiResImages["/Docs/a.jpg"] != nil }
        // The old one lands after it, and is not kept.
        try? await Task.sleep(nanoseconds: 400_000_000)
        #expect(asked == ["/Docs/a.jpg", "/Docs/a.jpg"])
        #expect(viewer.hiResImages["/Docs/a.jpg"]?.size == CGSize(width: 20, height: 40))
        #expect(!viewer.hiResFailed.contains("/Docs/a.jpg"))
        viewer.closeModal()
    }
}
}
