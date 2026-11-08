// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  PhotoEditor.swift
//  OffTheCloud
//
//  Photo edit (2026-11-07): the viewer's Edit turns and crops a photo, and
//  the device does it to the full-size photo (EditImage, RevertImage and
//  GetEditState in messages.proto - their comments are the contract). The
//  Android app and the web follow the same rules:
//    - Edit is in the viewer's controls (Images, collections, Favourites,
//      People, Files, the map's and a memory's photos) for the owner's own
//      photos the device can edit: JPEG, PNG, HEIC/HEIF, AVIF and WebP -
//      never a video, a GIF, a camera raw, a TIFF or an SVG, nor a photo
//      only on this phone.
//    - The editor shows the viewer's picture (the full size, or its
//      thumbnail until that comes) fitted to the screen, a crop frame with
//      corner and edge handles over it (drag inside to move it; the
//      outside dimmed, the thirds while dragging), Rotate Left and Right
//      (a quarter turn each, the crop turning with the picture), Aspect
//      (Free, Original, Square, 4:3, 16:9 - upright for an upright
//      picture, centred where the frame was), Reset, Cancel and Save.
//    - Save asks Replace Photo or Save as Copy. Replace asks the device how
//      many paths hold the photo first (GetEditState) and, for more than
//      one, all of them or only here. The editor waits ("Saving…") until
//      the device answers; refused, it says the device's words and stays -
//      except when the photo changed since it was shown ("changed"), when
//      the viewer closes and its photos are read again, as Delete does.
//    - Replaced, the viewer shows the edit in the photo's place (a new
//      hash: a new full size and thumbnail); a copy is said by its name and
//      the viewer stays on the photo. What is behind the viewer is read
//      again: Files' listing at once, Images' grid as the viewer closes
//      when a copy was made or other copies were left as they were.
//    - An edit (File.original_hash) has Revert to Original next to Info:
//      the copies question as Save's, then RevertImage.
//
//  Video edit (2026-11-08): the same Edit on a video the device can cut
//  (EditVideo: MP4, MOV, M4V, 3GP, MKV, WebM, AVI - never MPEG-TS, WMV,
//  FLV...) opens the post composer's trimmer over the viewer
//  (VideoEditorView, VideoTrimmerView with TrimmerCut): the two handles,
//  Play Cut, "Keeping … of …", the cut's small print (the device copies
//  the streams, so the cut can start a little before the point chosen),
//  Cancel and Save, for a real cut of at least half a second. It plays
//  what the viewer plays - the stream URL, or the file it downloaded -
//  rather than fetching the video again. Everything around the cut is the
//  photo's: Replace Video or Save as Copy, the copies question, Saving…,
//  "changed", the video in the photo's place afterwards (a new hash: a new
//  stream URL and thumbnail), and Revert to Original with the video's
//  words.
//

import AVFoundation
import SwiftUI
import UIKit

/// The part of the photo an edit keeps: fractions (0 to 1) of the photo as
/// it is shown - already turned - from its top left corner, as EditCrop
/// carries them.
struct CropRect: Equatable {
    var x: Double
    var y: Double
    var width: Double
    var height: Double

    /// The whole photo: no crop at all.
    static let whole = CropRect(x: 0, y: 0, width: 1, height: 1)

    /// How far from the edges still counts as the whole photo: a ten
    /// thousandth of a side, under a pixel of any camera's photo, so a
    /// frame dragged out and back again is no crop.
    static let slack = 1e-4

    var maxX: Double { x + width }
    var maxY: Double { y + height }
    var midX: Double { x + width / 2 }
    var midY: Double { y + height / 2 }

    var isWhole: Bool {
        x <= Self.slack && y <= Self.slack && maxX >= 1 - Self.slack && maxY >= 1 - Self.slack
    }

    /// The same part of the picture once the picture is turned a quarter
    /// turn clockwise: its left edge becomes the top.
    var turnedRight: CropRect { CropRect(x: 1 - y - height, y: x, width: height, height: width) }

    /// The same once it is turned anticlockwise: its top becomes the left.
    var turnedLeft: CropRect { CropRect(x: y, y: 1 - x - width, width: height, height: width) }

    /// Inside the photo, and never empty: what EditCrop is sent as.
    var clamped: CropRect {
        let w = min(max(width, 0), 1)
        let h = min(max(height, 0), 1)
        return CropRect(x: min(max(x, 0), 1 - w), y: min(max(y, 0), 1 - h), width: w, height: h)
    }

    /// The largest crop of this shape (`ratio`: its width over its height,
    /// in these fractions) centred on (cx, cy), moved inside the photo
    /// where that would cross an edge: what choosing an aspect does to the
    /// frame.
    static func largest(ratio: Double, centredOn cx: Double, _ cy: Double) -> CropRect {
        guard ratio > 0, ratio.isFinite else { return .whole }
        let w = ratio >= 1 ? 1 : ratio
        let h = ratio >= 1 ? 1 / ratio : 1
        return CropRect(x: min(max(cx - w / 2, 0), 1 - w), y: min(max(cy - h / 2, 0), 1 - h), width: w, height: h)
    }

    /// This frame dragged by `handle` by (dx, dy), in fractions of the
    /// photo: moved (kept inside), or resized from the edges the handle
    /// moves - never past the photo's edges nor under the minimum. With a
    /// `ratio` (an aspect chosen) the frame keeps that shape: a corner
    /// grows away from the opposite one, by the finger's larger move; an
    /// edge grows from the opposite edge and the frame stays centred
    /// across, moved inside where it would cross the photo's edge.
    func dragged(_ handle: CropHandle, dx: Double, dy: Double, ratio: Double?,
                 minWidth: Double, minHeight: Double) -> CropRect {
        if handle == .move {
            return CropRect(x: min(max(x + dx, 0), 1 - width), y: min(max(y + dy, 0), 1 - height),
                            width: width, height: height)
        }
        let minW = min(minWidth, 1)
        let minH = min(minHeight, 1)
        guard let r = ratio, r > 0, r.isFinite else {
            var left = x, right = maxX, top = y, bottom = maxY
            if handle.movesLeft { left = min(max(x + dx, 0), right - minW) }
            if handle.movesRight { right = max(min(maxX + dx, 1), left + minW) }
            if handle.movesTop { top = min(max(y + dy, 0), bottom - minH) }
            if handle.movesBottom { bottom = max(min(maxY + dy, 1), top + minH) }
            return CropRect(x: left, y: top, width: right - left, height: bottom - top)
        }
        // The least the frame may be, in this shape: wide enough for the
        // minimum width and high enough for the minimum height.
        let leastW = max(minW, minH * r)
        switch handle {
        case .left, .right:
            let anchor = handle == .left ? maxX : x
            let room = min(handle == .left ? anchor : 1 - anchor, r)
            let w = min(max(width + (handle == .left ? -dx : dx), min(leastW, room)), room)
            let h = w / r
            return CropRect(x: handle == .left ? anchor - w : anchor, y: min(max(midY - h / 2, 0), 1 - h),
                            width: w, height: h)
        case .top, .bottom:
            let anchor = handle == .top ? maxY : y
            let room = min(handle == .top ? anchor : 1 - anchor, 1 / r)
            let h = min(max(height + (handle == .top ? -dy : dy), min(leastW / r, room)), room)
            let w = h * r
            return CropRect(x: min(max(midX - w / 2, 0), 1 - w), y: handle == .top ? anchor - h : anchor,
                            width: w, height: h)
        default:
            let ax = handle.movesLeft ? maxX : x
            let ay = handle.movesTop ? maxY : y
            let wide = width + (handle.movesLeft ? -dx : dx)
            let high = height + (handle.movesTop ? -dy : dy)
            var w = abs(wide - width) >= abs(high - height) * r ? wide : high * r
            let room = min(handle.movesLeft ? ax : 1 - ax, (handle.movesTop ? ay : 1 - ay) * r)
            w = min(max(w, min(leastW, room)), room)
            let h = w / r
            return CropRect(x: handle.movesLeft ? ax - w : ax, y: handle.movesTop ? ay - h : ay, width: w, height: h)
        }
    }
}

/// What a finger on the crop frame takes hold of.
enum CropHandle: Equatable {
    case topLeft, top, topRight, right, bottomRight, bottom, bottomLeft, left, move

    var movesLeft: Bool { self == .topLeft || self == .left || self == .bottomLeft }
    var movesRight: Bool { self == .topRight || self == .right || self == .bottomRight }
    var movesTop: Bool { self == .topLeft || self == .top || self == .topRight }
    var movesBottom: Bool { self == .bottomLeft || self == .bottom || self == .bottomRight }

    /// The handle a touch at `p` takes on a frame drawn at `frame` (both in
    /// points): a corner within `reach` of it, else an edge within `reach`
    /// of it, else the inside, which moves the frame; nil further out. The
    /// corners come first, so a small frame can still be resized; inside
    /// the frame a handle reaches a third of the way in at most, so the
    /// middle of a small frame still moves it.
    static func at(_ p: CGPoint, frame: CGRect, reach: CGFloat) -> CropHandle? {
        let inX = min(reach, frame.width / 3)
        let inY = min(reach, frame.height / 3)
        let left = p.x >= frame.minX - reach && p.x <= frame.minX + inX
        let right = !left && p.x >= frame.maxX - inX && p.x <= frame.maxX + reach
        let top = p.y >= frame.minY - reach && p.y <= frame.minY + inY
        let bottom = !top && p.y >= frame.maxY - inY && p.y <= frame.maxY + reach
        let across = p.x >= frame.minX - reach && p.x <= frame.maxX + reach
        let along = p.y >= frame.minY - reach && p.y <= frame.maxY + reach
        switch (left, right, top, bottom) {
        case (true, _, true, _): return .topLeft
        case (_, true, true, _): return .topRight
        case (true, _, _, true): return .bottomLeft
        case (_, true, _, true): return .bottomRight
        default: break
        }
        if (left || right) && along { return left ? .left : .right }
        if (top || bottom) && across { return top ? .top : .bottom }
        return frame.contains(p) ? .move : nil
    }
}

/// The shapes the crop can be held to (the Aspect menu).
enum CropAspect: CaseIterable, Identifiable {
    case free, original, square, fourThree, sixteenNine

    var id: Self { self }

    var title: String {
        switch self {
        case .free: return S.appPhotosEditAspectFree
        case .original: return S.appPhotosEditAspectOriginal
        case .square: return S.appPhotosEditAspectSquare
        case .fourThree: return S.appPhotosEditAspect43
        case .sixteenNine: return S.appPhotosEditAspect169
        }
    }

    /// The crop's width over its height in pixels, on a picture shown
    /// `landscape` (wider than high, or square) whose own proportions are
    /// `own`: 4:3 and 16:9 follow the picture, so an upright one gets 3:4
    /// and 9:16. nil for Free.
    func pixelRatio(landscape: Bool, own: Double) -> Double? {
        switch self {
        case .free: return nil
        case .original: return own
        case .square: return 1
        case .fourThree: return landscape ? 4.0 / 3.0 : 3.0 / 4.0
        case .sixteenNine: return landscape ? 16.0 / 9.0 : 9.0 / 16.0
        }
    }
}

/// What the editor holds: the turns, the crop and the aspect it is held
/// to. Nothing is sent until Save (PhotoEditRules.request).
struct PhotoEdit: Equatable {
    /// Quarter turns clockwise, 0 to 3.
    private(set) var turns = 0
    var crop = CropRect.whole
    private(set) var aspect: CropAspect = .free
    /// The crop's shape while an aspect other than Free is chosen: its
    /// width over its height in fractions of the picture as shown (the
    /// picture's own proportions are 1). A turn inverts it, as it turns
    /// the frame: the shape follows the picture. nil while Free.
    private(set) var ratio: Double?

    /// Something to save: a turn, or a crop.
    var changed: Bool { turns != 0 || !crop.isWhole }

    mutating func turnRight() {
        turns = (turns + 1) % 4
        crop = crop.turnedRight
        ratio = ratio.map { 1 / $0 }
    }

    mutating func turnLeft() {
        turns = (turns + 3) % 4
        crop = crop.turnedLeft
        ratio = ratio.map { 1 / $0 }
    }

    /// A picture of `size` as the editor shows it, turned.
    func shown(_ size: CGSize) -> CGSize {
        turns % 2 == 1 ? CGSize(width: size.height, height: size.width) : size
    }

    /// Holds the crop to `aspect` on a picture of `size` (unturned): the
    /// largest frame of that shape, centred where the frame was. Free
    /// leaves the frame as it is.
    mutating func choose(_ aspect: CropAspect, picture size: CGSize) {
        self.aspect = aspect
        let s = shown(size)
        guard s.width > 0, s.height > 0,
              let pixels = aspect.pixelRatio(landscape: s.width >= s.height, own: s.width / s.height) else {
            ratio = nil
            return
        }
        let r = pixels / (s.width / s.height)
        ratio = r
        crop = .largest(ratio: r, centredOn: crop.midX, crop.midY)
    }

    /// The frame dragged by `handle` from where it was when the finger
    /// came down (`start`).
    mutating func drag(from start: CropRect, _ handle: CropHandle, dx: Double, dy: Double,
                       minWidth: Double, minHeight: Double) {
        crop = start.dragged(handle, dx: dx, dy: dy, ratio: ratio, minWidth: minWidth, minHeight: minHeight)
    }

    /// Back to the photo as it opened: no turn, no crop, Free.
    mutating func reset() {
        self = PhotoEdit()
    }
}

/// When the viewer offers Edit and Revert to Original, what Save sends,
/// and the frame's limits.
enum PhotoEditRules {
    /// The types the device edits (EditImage): it decides by the bytes
    /// themselves, these are what is offered.
    static let editableTypes: Set<String> = ["image/jpeg", "image/png", "image/heic", "image/heif", "image/avif", "image/webp"]

    /// Whether the device can edit a file of this type and name: one of
    /// the types above, or a HEIC/HEIF by its name (some arrive without
    /// a type of their own). Never a video, an animation (GIF), a camera
    /// raw (image/x-…, DNG), a TIFF or an SVG.
    static func editable(mime: String, path: String) -> Bool {
        let m = mime.lowercased()
        if m.hasPrefix("video/") || m.hasPrefix("image/x-") || ["image/gif", "image/tiff", "image/svg+xml", "image/dng"].contains(m) {
            return false
        }
        if editableTypes.contains(m) { return true }
        let ext = (path as NSString).pathExtension.lowercased()
        return ext == "heic" || ext == "heif"
    }

    /// Edit, for an item of the viewer: one of the owner's own on the
    /// device (never a photo only on this phone) that it can edit.
    static func offersEdit(_ item: PhotoGalleryVM.Item) -> Bool {
        !item.isLocalOnly && !item.path.isEmpty && editable(mime: item.mime, path: item.path)
    }

    /// Revert to Original: the item is an edit the device keeps the
    /// original of (File.original_hash) - not a compressed file, whose
    /// original the device deleted (File.original_removed, Compress.swift).
    static func offersRevert(_ item: PhotoGalleryVM.Item) -> Bool {
        !item.isLocalOnly && !item.path.isEmpty && !item.originalHash.isEmpty && !item.originalRemoved
    }

    /// The EditImage for `edit` of the photo at `path`, shown with the
    /// content `hash` (the device refuses with "changed" if the path holds
    /// something else now). The crop only when there is one;
    /// every_copy only when replacing.
    static func request(path: String, hash: String, edit: PhotoEdit, asCopy: Bool, everyCopy: Bool) -> Msg_EditImage {
        var e = Msg_EditImage()
        e.path = path
        e.hash = hash
        e.quarterTurns = Int32(((edit.turns % 4) + 4) % 4)
        if !edit.crop.isWhole {
            let c = edit.crop.clamped
            var crop = Msg_EditCrop()
            crop.x = c.x
            crop.y = c.y
            crop.width = c.width
            crop.height = c.height
            e.crop = crop
        }
        e.asCopy = asCopy
        e.everyCopy = everyCopy && !asCopy
        return e
    }

    /// The RevertImage for the edit at `path` shown with `hash`.
    static func revertRequest(path: String, hash: String, everyCopy: Bool) -> Msg_RevertImage {
        var r = Msg_RevertImage()
        r.path = path
        r.hash = hash
        r.everyCopy = everyCopy
        return r
    }

    /// The device's own words for a refusal (always in the owner's
    /// language), nil when it gave none.
    static func words(_ resp: Msg_RespEnvelope?) -> String? {
        guard let resp, resp.error, !resp.errorMessage.isEmpty else { return nil }
        return resp.errorMessage
    }

    /// How many paths hold the content, from GetEditState's answer: 1 when
    /// it didn't come or the device is older (unknown_payload), so nothing
    /// is asked about copies.
    static func copies(_ resp: Msg_RespEnvelope?) -> Int {
        guard let resp, !resp.error, case .respEditState(let s) = resp.payload else { return 1 }
        return max(1, Int(s.copies))
    }

    /// The smallest the frame may be, in fractions of the picture shown
    /// at `points`: a twentieth of each side, and never under a finger's
    /// width on the screen.
    static func minimum(shownAt points: CGSize) -> (width: Double, height: Double) {
        func side(_ p: CGFloat) -> Double { p > 0 ? min(1, max(1.0 / 20, Double(minPoints / p))) : 1 }
        return (side(points.width), side(points.height))
    }

    /// The frame's least size on the screen.
    static let minPoints: CGFloat = 40

    /// Revert to Original's question for `item`, the edit of a photo or a
    /// video held at `copies` paths: all of them or only here for several,
    /// else what the revert undoes - a cut for a video.
    static func revertText(for item: PhotoGalleryVM.Item, copies: Int) -> String {
        if copies > 1 { return S.appPhotosEditRevertCopiesText(count: copies) }
        return VideoEditRules.isVideo(mime: item.mime, path: item.path) ? S.appPhotosEditVideoRevertText : S.appPhotosEditRevertText
    }
}

/// When the viewer offers Edit on a video, and what its Save sends
/// (EditVideo). What it chooses with is the post composer's trimmer
/// (TrimRange); what it sends then and its answers are the photo's
/// (PhotoEditRules.copies, words).
enum VideoEditRules {
    /// The types the device cuts (EditVideo): it decides by the container
    /// itself, these are what is offered.
    static let cuttableTypes: Set<String> = [
        "video/mp4", "video/quicktime", "video/x-m4v", "video/3gpp", "video/3gpp2",
        "video/x-matroska", "video/webm", "video/x-msvideo",
    ]

    /// The same by name, for a video that came without a type of its own.
    static let cuttableExtensions: Set<String> = ["mp4", "mov", "m4v", "3gp", "3g2", "mkv", "webm", "avi"]

    /// A type as compared: lower case, without its parameters
    /// ("video/mp4; codecs=…").
    private static func bare(_ mime: String) -> String {
        let type = mime.split(separator: ";", maxSplits: 1, omittingEmptySubsequences: false).first ?? ""
        return type.trimmingCharacters(in: .whitespaces).lowercased()
    }

    /// Whether the device can cut a file of this type and name: one of the
    /// types above, or - with no type, or a generic one - one of their
    /// names, in any case. Never another video (MPEG-TS, WMV, FLV...), nor
    /// a photo.
    static func cuttable(mime: String, path: String) -> Bool {
        let m = bare(mime)
        if cuttableTypes.contains(m) { return true }
        guard m.isEmpty || m == "application/octet-stream" else { return false }
        return cuttableExtensions.contains((path as NSString).pathExtension.lowercased())
    }

    /// A video, as Revert to Original's question words it: one by its
    /// type, or one the device cuts by its name.
    static func isVideo(mime: String, path: String) -> Bool {
        bare(mime).hasPrefix("video/") || cuttable(mime: mime, path: path)
    }

    /// What the cut screen's player opens of those: AVFoundation plays no
    /// MKV, WebM or AVI, so Edit would open on "This video could not be
    /// opened".
    static let unplayableTypes: Set<String> = ["video/x-matroska", "video/webm", "video/x-msvideo"]
    static let unplayableExtensions: Set<String> = ["mkv", "webm", "avi"]

    /// Whether this phone plays a cuttable video in the cut screen.
    static func playableHere(mime: String, path: String) -> Bool {
        let m = bare(mime)
        if unplayableTypes.contains(m) { return false }
        guard m.isEmpty || m == "application/octet-stream" else { return true }
        return !unplayableExtensions.contains((path as NSString).pathExtension.lowercased())
    }

    /// Edit, for a video of the viewer: one of the owner's own on the
    /// device (never one only on this phone) that it can cut and this
    /// phone can play.
    static func offersEdit(_ item: PhotoGalleryVM.Item) -> Bool {
        !item.isLocalOnly && !item.path.isEmpty && cuttable(mime: item.mime, path: item.path)
            && playableHere(mime: item.mime, path: item.path)
    }

    /// Save, for the part chosen of a video `duration` long: a real cut
    /// (not the whole video) of at least half a second - the composer's
    /// Apply.
    static func canSave(_ cut: TrimRange, duration: Double) -> Bool {
        cut.isCut(of: duration)
    }

    /// The EditVideo for `cut` of the video at `path`, `duration` long,
    /// shown with the content `hash` (the device refuses with "changed" if
    /// the path holds something else now). An end at the video's end goes
    /// as 0, "to the end", as the device reads it (the slider's end is
    /// the length the phone measured, which may not be the device's to the
    /// frame); every_copy only when replacing.
    static func request(path: String, hash: String, cut: TrimRange, duration: Double,
                        asCopy: Bool, everyCopy: Bool) -> Msg_EditVideo {
        var e = Msg_EditVideo()
        e.path = path
        e.hash = hash
        e.startSecs = max(0, cut.start)
        e.endSecs = cut.end <= 0 || cut.reachesEnd(of: duration) ? 0 : cut.end
        e.asCopy = asCopy
        e.everyCopy = everyCopy && !asCopy
        return e
    }
}

/// The edit applied to a picture on this phone: the viewer shows it in
/// the photo's place until the device's own full size of the edit comes,
/// and the grid's tile with it.
enum PhotoEditRender {
    /// `image` (upright, as the viewer shows it) turned `turns` quarter
    /// turns clockwise, then cut to `crop`, at most `maxSide` pixels on
    /// its longer side.
    static func render(_ image: UIImage, turns: Int, crop: CropRect, maxSide: CGFloat = 1024) -> UIImage? {
        let size = image.size
        guard size.width > 0, size.height > 0 else { return nil }
        let t = ((turns % 4) + 4) % 4
        let shown = t % 2 == 1 ? CGSize(width: size.height, height: size.width) : size
        let c = crop.clamped
        let part = CGRect(x: c.x * shown.width, y: c.y * shown.height,
                          width: c.width * shown.width, height: c.height * shown.height)
        guard part.width >= 1, part.height >= 1 else { return nil }
        let scale = min(1, maxSide / max(part.width, part.height))
        let out = CGSize(width: max(1, (part.width * scale).rounded()), height: max(1, (part.height * scale).rounded()))
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        return UIGraphicsImageRenderer(size: out, format: format).image { ctx in
            let g = ctx.cgContext
            g.scaleBy(x: out.width / part.width, y: out.height / part.height)
            g.translateBy(x: -part.minX, y: -part.minY)
            // The picture into the turned one's place: y grows downwards,
            // so a positive angle turns clockwise.
            switch t {
            case 1:
                g.translateBy(x: shown.width, y: 0)
                g.rotate(by: .pi / 2)
            case 2:
                g.translateBy(x: shown.width, y: shown.height)
                g.rotate(by: .pi)
            case 3:
                g.translateBy(x: 0, y: shown.height)
                g.rotate(by: -.pi / 2)
            default:
                break
            }
            image.draw(in: CGRect(origin: .zero, size: size))
        }
    }

    /// The same off the main thread; nil without a picture.
    static func preview(of image: UIImage?, _ edit: PhotoEdit) async -> UIImage? {
        guard let image else { return nil }
        let turns = edit.turns, crop = edit.crop
        return await Task.detached(priority: .userInitiated) {
            render(image, turns: turns, crop: crop)
        }.value
    }
}

/// How the editor's Save ended.
enum PhotoEditSaved: Equatable {
    case saved
    /// Nothing was saved: the device's words, or app.photos.edit.failed.
    case failed(String)
    /// Nothing was saved because the photo holds other content than was
    /// shown ("changed"): Save would only fail the same way, so the viewer
    /// closed, its alert says the device's words, and what was behind it
    /// is read again - as Delete's "changed".
    case outOfDate
}

/// Revert to Original's question: the photo, and at how many paths the
/// device holds its edit.
struct RevertAsk: Identifiable, Equatable {
    let item: PhotoGalleryVM.Item
    let copies: Int
    var id: String { item.id }
}

extension PhotoGalleryVM {
    /// The item the viewer shows; nil while it is closed.
    var openItem: Item? {
        openIndex.flatMap { items.indices.contains($0) ? items[$0] : nil }
    }

    /// The viewer has Edit for the open item: a photo to turn and crop, or
    /// a video to cut.
    var editOffered: Bool {
        openItem.map { PhotoEditRules.offersEdit($0) || VideoEditRules.offersEdit($0) } ?? false
    }

    /// The viewer has Revert to Original for the open item.
    var revertOffered: Bool { openItem.map(PhotoEditRules.offersRevert) ?? false }

    /// At how many paths the device holds the content at `path`
    /// (GetEditState): 1 when it doesn't say.
    func editCopies(of path: String) async -> Int {
        var g = Msg_GetEditState()
        g.path = path
        return PhotoEditRules.copies(try? await photoRequest(.reqGetEditState(g), nil))
    }

    /// Sends the editor's `edit` of `item` (shown from `picture`): in its
    /// place - every one of its `copies` paths with `everyCopy` - or as a
    /// copy next to it. Done, the viewer shows the edit in the photo's
    /// place, or says the copy's name, and what is behind the viewer is
    /// read again (editShown, copySaved). "changed" closes the viewer and
    /// reads its photos again (contentChanged), the device's words in its
    /// alert.
    func saveEdit(_ edit: PhotoEdit, of item: Item, picture: UIImage?, copies: Int,
                  asCopy: Bool, everyCopy: Bool) async -> PhotoEditSaved {
        let req = PhotoEditRules.request(path: item.path, hash: item.hash, edit: edit,
                                         asCopy: asCopy, everyCopy: everyCopy && copies > 1)
        let resp = try? await photoRequest(.reqEditImage(req), nil)
        return await editAnswered(resp, of: item, copies: copies, asCopy: asCopy, everyCopy: req.everyCopy) {
            await PhotoEditRender.preview(of: picture, edit)
        }
    }

    /// Sends the cut chosen of the video `item`, `duration` long, as
    /// saveEdit sends a photo's edit, and shows what the device answers the
    /// same way. With the socket's own time for the answer (30 minutes, as
    /// a post's publication: no `kind`): the device reads the whole video to
    /// copy the part kept, a minute or more for a long 4K recording on a Pi.
    func saveCut(_ cut: TrimRange, duration: Double, of item: Item, copies: Int,
                 asCopy: Bool, everyCopy: Bool) async -> PhotoEditSaved {
        let req = VideoEditRules.request(path: item.path, hash: item.hash, cut: cut, duration: duration,
                                         asCopy: asCopy, everyCopy: everyCopy && copies > 1)
        let resp = try? await photoRequest(.reqEditVideo(req), nil)
        // Nothing to draw on the phone for a video: its tile waits for the
        // device's thumbnail of the cut, as after a revert.
        return await editAnswered(resp, of: item, copies: copies, asCopy: asCopy, everyCopy: req.everyCopy) { nil }
    }

    /// The device's answer to an edit of `item` (nil: none came): the edit
    /// shown in its place (`preview` drawn on the phone meanwhile), or the
    /// copy said; "changed" closes the viewer and reads its photos again,
    /// with the device's words in its alert; any other refusal is the
    /// device's words, else app.photos.edit.failed.
    private func editAnswered(_ resp: Msg_RespEnvelope?, of item: Item, copies: Int, asCopy: Bool, everyCopy: Bool,
                              preview: () async -> UIImage?) async -> PhotoEditSaved {
        guard let resp else { return .failed(S.appPhotosEditFailed) }
        guard !resp.error, case .respFile(let f) = resp.payload else {
            if resp.errorCode == "changed" {
                say(.editFailed(PhotoEditRules.words(resp)))
                contentChanged(item.path)
                return .outOfDate
            }
            // The demo device's refusal and an older device's "update"
            // too: the device's words, as Delete says them.
            return .failed(PhotoEditRules.words(resp) ?? S.appPhotosEditFailed)
        }
        if asCopy {
            copySaved(f)
        } else {
            let shown = await preview()
            editShown(f, replacing: item, copies: copies, everyCopy: everyCopy, preview: shown)
        }
        return .saved
    }

    /// What the viewer plays `item` from while it is the open video: its
    /// stream URL, the file it downloaded for it, or the phone's own. nil
    /// until the viewer has it (VideoEditorView waits for it rather than
    /// fetching the video a second time).
    func videoSource(of item: Item) -> URL? {
        guard openItem?.id == item.id, let asset = videoPlayer?.currentItem?.asset as? AVURLAsset else { return nil }
        return asset.url
    }

    /// The viewer won't have `item` to play: it couldn't fetch or play it,
    /// or it doesn't play it as a video at all (fetchHiRes goes by the
    /// type).
    func videoUnavailable(_ item: Item) -> Bool {
        unplayable.contains(item.path) || !item.mime.hasPrefix("video/")
    }

    /// Revert to Original tapped: how many paths hold the edit is asked
    /// first (the button a spinner meanwhile), then the question.
    func askRevert() {
        guard revertingPath == nil, let it = openItem, PhotoEditRules.offersRevert(it) else { return }
        revertingPath = it.path
        Task {
            let copies = await editCopies(of: it.path)
            revertingPath = nil
            // Moved on or closed meanwhile: no question about another photo.
            guard openItem?.id == it.id else { return }
            revertAsk = RevertAsk(item: it, copies: copies)
        }
    }

    /// The question answered: the original back at this path, or at every
    /// path holding the edit (`everyCopy`), then shown in its place.
    /// Refused: the device's words, else revert_failed - and for
    /// "changed" the viewer closes and its photos are read again.
    func revert(_ ask: RevertAsk, everyCopy: Bool) {
        guard revertingPath == nil else { return }
        revertAsk = nil
        let it = ask.item
        revertingPath = it.path
        Task {
            defer { revertingPath = nil }
            let req = PhotoEditRules.revertRequest(path: it.path, hash: it.hash, everyCopy: everyCopy && ask.copies > 1)
            let resp = try? await photoRequest(.reqRevertImage(req), nil)
            guard let resp, !resp.error, case .respFile(let f) = resp.payload else {
                say(.revertFailed(PhotoEditRules.words(resp)))
                // Other content than was shown: the viewer closes and its
                // photos are read again, as Delete's "changed".
                if resp?.errorCode == "changed" { contentChanged(it.path) }
                return
            }
            // No path holds the edit any more, and the device deletes it:
            // nothing of it stays on the phone either, as after a delete.
            if req.everyCopy || ask.copies <= 1 { thumbs.cache.discard(it.hash) }
            editShown(f, replacing: it, copies: ask.copies, everyCopy: req.everyCopy, preview: nil)
        }
    }

    /// The device answered an edit or a revert of `old` with `f`, the
    /// content now at its path (perhaps another path: a HEIC edited
    /// becomes a JPEG, and its name comes back with the original): the
    /// item takes its place, with its new hash, so the viewer asks for the
    /// new full size (shown meanwhile from `preview`, the edit made on
    /// this phone) and the grid for its tile. The photo keeps its place
    /// and date in the grid, its stars too.
    private func editShown(_ f: Msg_File, replacing old: Item, copies: Int, everyCopy: Bool, preview: UIImage?) {
        guard let at = items.firstIndex(where: { $0.id == old.id }) else { return }
        let new = Item(
            id: PhotoPaging.itemID(f),
            path: f.path.isEmpty ? old.path : f.path,
            mime: f.mime.isEmpty ? old.mime : f.mime,
            size: Int(f.fileSize),
            thumbData: nil,
            localURL: nil,
            isLocalOnly: false,
            thumbImage: preview,
            month: old.month,
            created: old.created,
            hash: f.hash,
            // Every copy changed: the new content is at all of them.
            copies: everyCopy && copies > 1 ? copies : nil,
            up: old.up,
            rating: old.rating,
            originalHash: f.originalHash,
            // An edit of a compressed file is one too: still nothing to
            // revert to.
            originalRemoved: f.originalRemoved
        )
        // Another copy of the photo kept the content it had, or the
        // content is in the grid already (an original put back next to a
        // copy of it): the grid has a photo more, or one twice, until it
        // is read again.
        let readAgain = (copies > 1 && !everyCopy) || items.contains { $0.id != old.id && !f.hash.isEmpty && $0.hash == f.hash }
        forgetFullSize(of: old.path)
        if new.path != old.path { forgetFullSize(of: new.path) }
        if let preview, !fixedList { GridThumbCache.put(preview, id: new.id, maxPt: PhotoGridMetrics.decodeSide) }
        items[at] = new
        if selected.remove(old.path) != nil { selected.insert(new.path) }
        if openIndex == at { open(index: at) }
        if fixedList {
            onDeleted?(new.path)
        } else {
            if readAgain { editReloadOnClose = true }
            places.libraryChanged()
        }
    }

    /// An edit saved as a copy (`f`): said by its name over the viewer,
    /// which stays on the photo. Files' listing is read again at once;
    /// Images' grid, where the copy goes at the photo's date, as the
    /// viewer closes.
    private func copySaved(_ f: Msg_File) {
        say(.savedCopy((f.path as NSString).lastPathComponent))
        if fixedList {
            onDeleted?(f.path)
        } else {
            editReloadOnClose = true
            places.libraryChanged()
        }
    }

    /// The viewer closed (closeModal): the grid again if an edit made it
    /// out of date.
    func editViewerClosed() {
        guard editReloadOnClose else { return }
        editReloadOnClose = false
        if !fixedList { reloadSearch() }
    }
}

// MARK: - The editor

/// Full screen over the viewer (ImageModal): the picture turned and with
/// its crop frame, the tools, Cancel, Reset and Save, and Save's questions.
struct PhotoEditorView: View {
    @ObservedObject var vm: PhotoGalleryVM
    let item: PhotoGalleryVM.Item
    /// The viewer's picture of it: its full size, or its thumbnail until
    /// that comes (the frame is in fractions, so the swap moves nothing).
    let picture: UIImage?
    let close: () -> Void

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var edit = PhotoEdit()
    /// The handle a finger holds, and the frame as it was when it came
    /// down; nil between drags.
    @State private var hold: (handle: CropHandle, start: CropRect)?
    /// A turn is animating: the picture and its frame spin together, then
    /// the turn is made (`spin` back to 0, nothing moving).
    @State private var spin: Double = 0
    @State private var spinScale: CGFloat = 1
    @State private var turning = false
    /// GetEditState or EditImage is on its way: the editor takes nothing.
    @State private var saving = false
    @State private var askSave = false
    /// The photo is at this many paths: all of them, or only here.
    @State private var askCopies: Int?
    @State private var askDiscard = false
    /// Save was refused: the device's words, or failed.
    @State private var failure: String?

    /// How far outside the frame's lines a finger still takes a handle.
    private static let reach: CGFloat = 24
    /// The room left around the picture for the handles.
    private static let inset: CGFloat = 24

    var body: some View {
        GeometryReader { screen in
            let landscape = ImageModal.controlsBeside(screen.size)
            VStack(spacing: 0) {
                topBar
                if landscape {
                    HStack(spacing: 0) {
                        canvas
                        VStack(spacing: 28) { tools }
                            .frame(width: 56)
                    }
                } else {
                    canvas
                    HStack(spacing: 56) { tools }
                        .padding(.vertical, 14)
                }
            }
        }
        .font(.title2)
        .foregroundStyle(.white)
        .tint(.white)
        .background(Color.black.ignoresSafeArea())
        // The editor is always dark, as the viewer under it: its menu and
        // questions too.
        .environment(\.colorScheme, .dark)
        .accessibilityAddTraits(.isModal)
        .confirmationDialog(S.appPhotosEditSaveTitle, isPresented: $askSave, titleVisibility: .visible) {
            Button(S.appPhotosEditReplace) { replace() }
            Button(S.appPhotosEditAsCopy) { Task { await send(copies: 1, asCopy: true, everyCopy: false) } }
            Button(S.commonCancel, role: .cancel) {}
        } message: {
            Text(S.appPhotosEditSaveText)
        }
        .confirmationDialog(
            S.appPhotosEditSaveTitle,
            isPresented: Binding(get: { askCopies != nil }, set: { if !$0 { askCopies = nil } }),
            titleVisibility: .visible,
            presenting: askCopies
        ) { n in
            Button(S.appPhotosEditCopiesAll(count: n)) { Task { await send(copies: n, asCopy: false, everyCopy: true) } }
            Button(S.appPhotosEditCopiesOnlyThis) { Task { await send(copies: n, asCopy: false, everyCopy: false) } }
            Button(S.commonCancel, role: .cancel) {}
        } message: { n in
            Text(S.appPhotosEditCopiesText(count: n))
        }
        .confirmationDialog(S.appPhotosEditDiscardTitle, isPresented: $askDiscard, titleVisibility: .visible) {
            Button(S.appPhotosEditDiscard, role: .destructive) { close() }
            Button(S.commonCancel, role: .cancel) {}
        }
        .alert(failure ?? "", isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } })) {
            Button(S.commonOk, role: .cancel) {}
        }
    }

    // MARK: Cancel, Reset, Save

    private var topBar: some View {
        HStack {
            Button(S.commonCancel) {
                if edit.changed { askDiscard = true } else { close() }
            }
            .disabled(saving)
            Spacer()
            saveButton
        }
        // Reset in the middle, whatever the two sides' widths.
        .overlay {
            if edit.changed && !saving {
                Button(S.appPhotosEditReset) { animate(.easeOut(duration: 0.2)) { edit.reset() } }
                    .disabled(turning)
            }
        }
        .font(.body)
        .padding(.horizontal)
        .padding(.vertical, 12)
    }

    @ViewBuilder
    private var saveButton: some View {
        if saving {
            HStack(spacing: 6) {
                ProgressView().tint(.white)
                Text(S.appPhotosEditSaving)
            }
            .accessibilityElement(children: .combine)
        } else {
            let can = edit.changed && picture != nil && !turning
            Button { askSave = true } label: {
                Text(S.commonSave).fontWeight(.semibold)
            }
            .foregroundStyle(can ? MenuStyle.ember : Color.white.opacity(0.35))
            .disabled(!can)
        }
    }

    /// Replace Photo: the copies are asked first; more than one, all of
    /// them or only here (the editor busy meanwhile).
    private func replace() {
        saving = true
        Announce.polite(S.appPhotosEditSaving)
        Task {
            let asked = Date()
            let copies = await vm.editCopies(of: item.path)
            guard copies > 1 else {
                await send(copies: 1, asCopy: false, everyCopy: false)
                return
            }
            // At home the device answers while Save's question is still
            // going away, and a question presented then can be dropped
            // unseen (DeviceNeedsUpdate's wait).
            let wait = 0.6 - Date().timeIntervalSince(asked)
            if wait > 0 { try? await Task.sleep(nanoseconds: UInt64(wait * 1_000_000_000)) }
            saving = false
            askCopies = copies
        }
    }

    /// EditImage, the editor waiting; saved, it closes (a copy is said by
    /// its name over the viewer), refused it stays to try again - unless
    /// the photo changed meanwhile, when the viewer has closed under it.
    private func send(copies: Int, asCopy: Bool, everyCopy: Bool) async {
        if !saving {
            saving = true
            Announce.polite(S.appPhotosEditSaving)
        }
        let outcome = await vm.saveEdit(edit, of: item, picture: picture, copies: copies,
                                        asCopy: asCopy, everyCopy: everyCopy)
        saving = false
        switch outcome {
        // Out of date: the viewer has closed, and the editor with it.
        case .saved, .outOfDate: close()
        case .failed(let words): failure = words
        }
    }

    // MARK: Rotate, Aspect

    @ViewBuilder
    private var tools: some View {
        let ready = picture != nil && !saving && !turning
        Button { turn(clockwise: false) } label: {
            Image(systemName: "rotate.left").frame(width: 44, height: 44).contentShape(Rectangle())
        }
        .disabled(!ready)
        .accessibilityLabel(S.appPhotosEditRotateLeft)
        Button { turn(clockwise: true) } label: {
            Image(systemName: "rotate.right").frame(width: 44, height: 44).contentShape(Rectangle())
        }
        .disabled(!ready)
        .accessibilityLabel(S.appPhotosEditRotateRight)
        Menu {
            Picker(S.appPhotosEditAspect, selection: Binding(get: { edit.aspect }, set: { choose($0) })) {
                ForEach(CropAspect.allCases) { a in Text(a.title).tag(a) }
            }
            .pickerStyle(.inline)
        } label: {
            // Filled while the crop is held to a shape.
            Image(systemName: edit.aspect == .free ? "aspectratio" : "aspectratio.fill")
                .frame(width: 44, height: 44).contentShape(Rectangle())
        }
        // Free first, whichever way the menu opens (it opens upwards from
        // the foot of the screen, which would turn the list round).
        .menuOrder(.fixed)
        .disabled(!ready)
        .accessibilityLabel(S.appPhotosEditAspect)
        .accessibilityValue(edit.aspect.title)
    }

    private func choose(_ aspect: CropAspect) {
        guard let picture else { return }
        animate(.easeOut(duration: 0.2)) { edit.choose(aspect, picture: picture.size) }
    }

    /// A quarter turn: the picture and its frame spin to it (scaled to fit
    /// the other way round), then the turn is made with nothing moving.
    private func turn(clockwise: Bool) {
        guard let picture, !turning else { return }
        func commit() { if clockwise { edit.turnRight() } else { edit.turnLeft() } }
        guard !reduceMotion, canvasSize.width > 0 else {
            commit()
            return
        }
        let now = Self.fitted(edit.shown(picture.size), in: canvasSize)
        var next = edit
        if clockwise { next.turnRight() } else { next.turnLeft() }
        let then = Self.fitted(next.shown(picture.size), in: canvasSize)
        turning = true
        withAnimation(.easeInOut(duration: 0.25)) {
            spin = clockwise ? 90 : -90
            spinScale = now.height > 0 ? then.width / now.height : 1
        } completion: {
            var t = Transaction()
            t.disablesAnimations = true
            withTransaction(t) {
                commit()
                spin = 0
                spinScale = 1
            }
            turning = false
        }
    }

    private func animate(_ animation: Animation, _ change: () -> Void) {
        if reduceMotion { change() } else { withAnimation(animation, change) }
    }

    // MARK: The picture and its frame

    /// The canvas's size, as last laid out: what a turn scales to.
    @State private var canvasSize: CGSize = .zero

    /// A picture shown at `shown` (any unit), fitted into `area` less the
    /// handles' room, in points.
    private static func fitted(_ shown: CGSize, in area: CGSize) -> CGSize {
        let room = CGSize(width: max(1, area.width - 2 * inset), height: max(1, area.height - 2 * inset))
        guard shown.width > 0, shown.height > 0 else { return .zero }
        let k = min(room.width / shown.width, room.height / shown.height)
        return CGSize(width: shown.width * k, height: shown.height * k)
    }

    private var canvas: some View {
        GeometryReader { geo in
            if let picture {
                let shown = Self.fitted(edit.shown(picture.size), in: geo.size)
                let photo = CGRect(x: (geo.size.width - shown.width) / 2, y: (geo.size.height - shown.height) / 2,
                                   width: shown.width, height: shown.height)
                let frame = CGRect(x: photo.minX + edit.crop.x * photo.width, y: photo.minY + edit.crop.y * photo.height,
                                   width: edit.crop.width * photo.width, height: edit.crop.height * photo.height)
                ZStack(alignment: .topLeading) {
                    turnedPicture(picture, shown: shown)
                        .position(x: photo.midX, y: photo.midY)
                    CropFrameView(photo: photo, frame: frame, thirds: hold != nil)
                }
                .rotationEffect(.degrees(spin))
                .scaleEffect(spinScale)
                .frame(width: geo.size.width, height: geo.size.height)
                .contentShape(Rectangle())
                .gesture(cropDrag(photo: photo, frame: frame))
                // VoiceOver: the photo, as its tile says it; the frame is
                // for fingers (Rotate and Aspect are the buttons).
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(PhotoMonths.tileLabel(video: false, date: item.created))
                .accessibilityAddTraits(.isImage)
                .onAppear { canvasSize = geo.size }
                .onChange(of: geo.size) { _, s in canvasSize = s }
            } else {
                ProgressView().tint(.white)
                    .frame(width: geo.size.width, height: geo.size.height)
            }
        }
        .allowsHitTesting(!saving && !turning)
    }

    /// The picture turned as edited, `shown` big once turned: drawn
    /// upright at the unturned size and spun into place.
    private func turnedPicture(_ picture: UIImage, shown: CGSize) -> some View {
        let sideways = edit.turns % 2 == 1
        return Image(uiImage: picture)
            .resizable()
            .frame(width: sideways ? shown.height : shown.width, height: sideways ? shown.width : shown.height)
            .rotationEffect(.degrees(Double(edit.turns) * 90))
            .frame(width: shown.width, height: shown.height)
    }

    /// A finger on the frame: a corner or an edge resizes it, inside moves
    /// it; elsewhere nothing.
    private func cropDrag(photo: CGRect, frame: CGRect) -> some Gesture {
        DragGesture(minimumDistance: 0)
            .onChanged { v in
                if hold == nil {
                    guard let handle = CropHandle.at(v.startLocation, frame: frame, reach: Self.reach) else { return }
                    hold = (handle, edit.crop)
                }
                guard let hold, photo.width > 0, photo.height > 0 else { return }
                let least = PhotoEditRules.minimum(shownAt: photo.size)
                edit.drag(from: hold.start, hold.handle,
                          dx: v.translation.width / photo.width, dy: v.translation.height / photo.height,
                          minWidth: least.width, minHeight: least.height)
            }
            .onEnded { _ in hold = nil }
    }
}

/// The crop frame over the picture: the outside dimmed, its lines, the
/// corner and edge handles, and the thirds while a finger holds it.
private struct CropFrameView: View {
    /// Where the picture is, and the frame on it, in the canvas's points.
    let photo: CGRect
    let frame: CGRect
    let thirds: Bool

    var body: some View {
        ZStack(alignment: .topLeading) {
            // Four bands around the frame rather than one even-odd path,
            // so an aspect chosen animates the frame into place.
            Group {
                band(CGRect(x: photo.minX, y: photo.minY, width: photo.width, height: frame.minY - photo.minY))
                band(CGRect(x: photo.minX, y: frame.maxY, width: photo.width, height: photo.maxY - frame.maxY))
                band(CGRect(x: photo.minX, y: frame.minY, width: frame.minX - photo.minX, height: frame.height))
                band(CGRect(x: frame.maxX, y: frame.minY, width: photo.maxX - frame.maxX, height: frame.height))
            }
            .foregroundStyle(.black.opacity(0.6))
            if thirds {
                ThirdsGrid()
                    .stroke(.white.opacity(0.5), lineWidth: 0.5)
                    .frame(width: frame.width, height: frame.height)
                    .position(x: frame.midX, y: frame.midY)
            }
            Rectangle()
                .stroke(.white.opacity(0.9), lineWidth: 1)
                .frame(width: frame.width, height: frame.height)
                .position(x: frame.midX, y: frame.midY)
            CropHandles()
                .stroke(.white, style: StrokeStyle(lineWidth: 3, lineCap: .square))
                .frame(width: frame.width + 3, height: frame.height + 3)
                .position(x: frame.midX, y: frame.midY)
        }
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }

    private func band(_ r: CGRect) -> some View {
        Rectangle()
            .frame(width: max(0, r.width), height: max(0, r.height))
            .position(x: r.midX, y: r.midY)
    }
}

/// The rule-of-thirds lines inside the frame.
private struct ThirdsGrid: Shape {
    func path(in rect: CGRect) -> Path {
        var p = Path()
        for i in 1...2 {
            let x = rect.minX + rect.width * CGFloat(i) / 3
            let y = rect.minY + rect.height * CGFloat(i) / 3
            p.move(to: CGPoint(x: x, y: rect.minY))
            p.addLine(to: CGPoint(x: x, y: rect.maxY))
            p.move(to: CGPoint(x: rect.minX, y: y))
            p.addLine(to: CGPoint(x: rect.maxX, y: y))
        }
        return p
    }
}

/// The handles: a thick corner at each corner and a short bar at the
/// middle of each edge, shorter on a small frame.
private struct CropHandles: Shape {
    func path(in rect: CGRect) -> Path {
        var p = Path()
        let arm = min(20, rect.width / 3, rect.height / 3)
        let bar = min(24, rect.width / 4, rect.height / 4)
        for (corner, dx, dy) in [(CGPoint(x: rect.minX, y: rect.minY), 1.0, 1.0),
                                 (CGPoint(x: rect.maxX, y: rect.minY), -1.0, 1.0),
                                 (CGPoint(x: rect.minX, y: rect.maxY), 1.0, -1.0),
                                 (CGPoint(x: rect.maxX, y: rect.maxY), -1.0, -1.0)] {
            p.move(to: CGPoint(x: corner.x + dx * arm, y: corner.y))
            p.addLine(to: corner)
            p.addLine(to: CGPoint(x: corner.x, y: corner.y + dy * arm))
        }
        p.move(to: CGPoint(x: rect.midX - bar / 2, y: rect.minY))
        p.addLine(to: CGPoint(x: rect.midX + bar / 2, y: rect.minY))
        p.move(to: CGPoint(x: rect.midX - bar / 2, y: rect.maxY))
        p.addLine(to: CGPoint(x: rect.midX + bar / 2, y: rect.maxY))
        p.move(to: CGPoint(x: rect.minX, y: rect.midY - bar / 2))
        p.addLine(to: CGPoint(x: rect.minX, y: rect.midY + bar / 2))
        p.move(to: CGPoint(x: rect.maxX, y: rect.midY - bar / 2))
        p.addLine(to: CGPoint(x: rect.maxX, y: rect.midY + bar / 2))
        return p
    }
}

// MARK: - A video's cut

/// Edit on a video, over the viewer (ImageModal): the post composer's
/// trimmer (VideoTrimmerView with TrimmerCut), playing what the viewer
/// plays, then Save's questions as the photo editor's.
struct VideoEditorView: View {
    @ObservedObject var vm: PhotoGalleryVM
    let item: PhotoGalleryVM.Item
    let close: () -> Void

    /// What the trimmer plays: the viewer's own source for the video, taken
    /// once it has one (it may still be on its way as Edit opens) and kept,
    /// so the trimmer isn't started again under the finger.
    @State private var source: URL?
    /// The part Save was tapped for, and the video's length then.
    @State private var chosen: (cut: TrimRange, duration: Double)?
    /// GetEditState or EditVideo is on its way: the screen takes nothing.
    @State private var saving = false
    @State private var askSave = false
    /// The video is at this many paths: all of them, or only here.
    @State private var askCopies: Int?
    /// Save was refused: the device's words, or failed.
    @State private var failure: String?

    var body: some View {
        VideoTrimmerView(
            url: source,
            existing: nil,
            cut: TrimmerCut(saving: saving, unavailable: vm.videoUnavailable(item)) { cut, duration in
                chosen = (cut, duration)
                askSave = true
            },
            onApply: { _ in },
            onCancel: close
        )
        .background(Color.black.ignoresSafeArea())
        // Always dark, as the viewer under it and the photo editor: its
        // questions too.
        .environment(\.colorScheme, .dark)
        .accessibilityAddTraits(.isModal)
        .onAppear { takeSource() }
        .onChange(of: vm.videoPlayer) { takeSource() }
        .confirmationDialog(S.appPhotosEditSaveTitle, isPresented: $askSave, titleVisibility: .visible) {
            Button(S.appPhotosEditVideoReplace) { replace() }
            Button(S.appPhotosEditAsCopy) { Task { await send(copies: 1, asCopy: true, everyCopy: false) } }
            Button(S.commonCancel, role: .cancel) {}
        } message: {
            Text(S.appPhotosEditVideoSaveText)
        }
        .confirmationDialog(
            S.appPhotosEditSaveTitle,
            isPresented: Binding(get: { askCopies != nil }, set: { if !$0 { askCopies = nil } }),
            titleVisibility: .visible,
            presenting: askCopies
        ) { n in
            Button(S.appPhotosEditCopiesAll(count: n)) { Task { await send(copies: n, asCopy: false, everyCopy: true) } }
            Button(S.appPhotosEditCopiesOnlyThis) { Task { await send(copies: n, asCopy: false, everyCopy: false) } }
            Button(S.commonCancel, role: .cancel) {}
        } message: { n in
            Text(S.appPhotosEditVideoCopiesText(count: n))
        }
        .alert(failure ?? "", isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } })) {
            Button(S.commonOk, role: .cancel) {}
        }
    }

    /// The viewer's source, once it has one.
    private func takeSource() {
        guard source == nil, let url = vm.videoSource(of: item) else { return }
        source = url
    }

    /// Replace Video: the copies are asked first; more than one, all of
    /// them or only here (the screen busy meanwhile).
    private func replace() {
        saving = true
        Announce.polite(S.appPhotosEditSaving)
        Task {
            let asked = Date()
            let copies = await vm.editCopies(of: item.path)
            guard copies > 1 else {
                await send(copies: 1, asCopy: false, everyCopy: false)
                return
            }
            // At home the device answers while Save's question is still
            // going away, and a question presented then can be dropped
            // unseen (the photo editor's wait).
            let wait = 0.6 - Date().timeIntervalSince(asked)
            if wait > 0 { try? await Task.sleep(nanoseconds: UInt64(wait * 1_000_000_000)) }
            saving = false
            askCopies = copies
        }
    }

    /// EditVideo, the screen waiting; saved, it closes (a copy is said by
    /// its name over the viewer), refused it stays to try again - unless
    /// the video changed meanwhile, when the viewer has closed under it.
    private func send(copies: Int, asCopy: Bool, everyCopy: Bool) async {
        guard let chosen else { return }
        if !saving {
            saving = true
            Announce.polite(S.appPhotosEditSaving)
        }
        let outcome = await vm.saveCut(chosen.cut, duration: chosen.duration, of: item, copies: copies,
                                       asCopy: asCopy, everyCopy: everyCopy)
        saving = false
        switch outcome {
        // Out of date: the viewer has closed, and the cut with it.
        case .saved, .outOfDate: close()
        case .failed(let words): failure = words
        }
    }
}
