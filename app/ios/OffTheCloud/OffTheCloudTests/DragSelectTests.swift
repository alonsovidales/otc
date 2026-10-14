// SPDX-License-Identifier: AGPL-3.0-or-later

import CoreGraphics
import Foundation
import Testing
@testable import OffTheCloud

/// Issue #200: a long press selects an item and a drag on from it selects
/// every item up to the finger, in the order shown (DragSelect.swift) -
/// the same cases as Android's DragSelectTest and the web's
/// drag-select-test.mjs.
struct DragSelectTests {
    private let order = ["a", "b", "c", "d", "e", "f"]

    @Test func thePressAloneSelectsThePressedItem() {
        let drag = DragSelection(base: [], start: "c")
        #expect(drag.range(in: order) == ["c"])
        #expect(drag.selection(in: order) == ["c"])
    }

    @Test func aDragSelectsFromThePressedItemToTheFingerEitherWay() {
        var drag = DragSelection<String>(base: [], start: "b")
        let down = drag.move(to: "e")
        #expect(down)
        #expect(drag.range(in: order) == ["b", "c", "d", "e"])
        // Upward: the same range, still in the order shown.
        let up = drag.move(to: "a")
        #expect(up)
        #expect(drag.range(in: order) == ["a", "b"])
        #expect(drag.selection(in: order) == ["a", "b"])
    }

    @Test func theSameItemAgainChangesNothing() {
        var drag = DragSelection<String>(base: [], start: "b")
        let first = drag.move(to: "d")
        let again = drag.move(to: "d")
        #expect(first)
        #expect(!again)
    }

    @Test func draggingBackGivesTheItemsPassedOverTheirEarlierState() {
        // "d" was selected before; "c" and "e" weren't.
        var drag = DragSelection<String>(base: ["d", "f"], start: "b")
        drag.move(to: "e")
        #expect(drag.selection(in: order) == ["b", "c", "d", "e", "f"])
        drag.move(to: "c")
        #expect(drag.selection(in: order) == ["b", "c", "d", "f"])
        drag.move(to: "b")
        #expect(drag.selection(in: order) == ["b", "d", "f"])
    }

    @Test func theRangeAddsToTheSelectionAndAPressOnASelectedItemKeepsIt() {
        // The owner's rule: a long press selects, whether or not the item
        // already was; the range never takes anything out.
        var drag = DragSelection<String>(base: ["a", "c"], start: "c")
        #expect(drag.selection(in: order) == ["a", "c"])
        drag.move(to: "e")
        #expect(drag.selection(in: order) == ["a", "c", "d", "e"])
    }

    @Test func thePressedItemGoneEndsTheDragAsItStands() {
        var drag = DragSelection<String>(base: ["x"], start: "b")
        drag.move(to: "d")
        let before = drag.goesOn(in: order)
        #expect(before)
        // A refresh took the pressed item away: the drag is over, and the
        // caller keeps the selection it had (b...d and x).
        let gone = ["a", "c", "d", "e", "f"]
        #expect(drag.range(in: gone) == nil)
        #expect(drag.selection(in: gone) == nil)
        let after = drag.goesOn(in: gone)
        #expect(!after)
        #expect(drag.isOver)
        // Later moves change nothing, even with it back.
        let back = drag.goesOn(in: order)
        #expect(!back)
        let later = drag.move(to: "f")
        #expect(!later)
        #expect(drag.range(in: order) == nil)
        #expect(drag.selection(keeping: ["x"], in: order) == nil)
    }

    @Test func theFingersItemGoneKeepsTheSelectionUntilAnother() {
        var drag = DragSelection<String>(base: ["x"], start: "b")
        drag.move(to: "d")
        // The one under the finger went: the selection stays as it stands.
        let gone = ["a", "b", "c", "e", "f"]
        let goes = drag.goesOn(in: gone)
        #expect(goes)
        #expect(drag.range(in: gone) == nil)
        #expect(drag.selection(in: gone) == nil)
        #expect(drag.selection(keeping: ["x"], in: gone) == nil)
        // Over another item: the range from the pressed one again.
        let over = drag.move(to: "e")
        #expect(over)
        #expect(drag.selection(in: gone) == ["x", "b", "c", "e"])
        let back = drag.move(to: "a")
        #expect(back)
        #expect(drag.range(in: gone) == ["a", "b"])
    }

    @Test func aSelectionKeptInOrderAppendsTheRangesNewItems() {
        // People's picks: the earlier ones first, as they were.
        var drag = DragSelection<String>(base: ["e", "b"], start: "c")
        drag.move(to: "a")
        #expect(drag.selection(keeping: ["e", "b"], in: order) == ["e", "b", "a", "c"])
        drag.move(to: "c")
        #expect(drag.selection(keeping: ["e", "b"], in: order) == ["e", "b", "c"])
    }

    @Test func anEarlierPickGoneMidDragDoesntComeBack() {
        // "a" and "e" were picked before the press on "c".
        var drag = DragSelection<String>(base: ["a", "e"], start: "c")
        drag.move(to: "d")
        #expect(drag.selection(in: order) == ["a", "c", "d", "e"])
        // "e" went (deleted, or a refresh dropped it): the view unpicked
        // it, and the drag forgets it - the next move doesn't pick it again.
        let gone = ["a", "b", "c", "d", "f"]
        drag.forget { !gone.contains($0) }
        #expect(drag.base == ["a"])
        let goes = drag.goesOn(in: gone)
        #expect(goes)
        drag.move(to: "f")
        #expect(drag.selection(in: gone) == ["a", "c", "d", "f"])
        // Even back in the list, it stays out.
        drag.move(to: "d")
        #expect(drag.selection(in: order) == ["a", "c", "d"])
        // People's, kept in order: the earlier picks but the one gone.
        #expect(drag.selection(keeping: ["e", "a"], in: order) == ["a", "c", "d"])
        // Nothing gone: nothing forgotten.
        drag.forget { !order.contains($0) }
        #expect(drag.base == ["a"])
    }

    // MARK: Which item the finger is over

    /// Three tiles of 100 a row, 2 apart, rows 102 apart, under a 30 tall
    /// title (nothing there).
    private var grid: [Int: CGRect] {
        var f: [Int: CGRect] = [:]
        for i in 0..<5 {
            f[i] = CGRect(x: CGFloat(i % 3) * 102, y: 30 + CGFloat(i / 3) * 102, width: 100, height: 100)
        }
        return f
    }

    @Test func theItemTheFingerIsIn() {
        #expect(DragSelectHit.item(at: CGPoint(x: 50, y: 80), in: grid) == 0)
        #expect(DragSelectHit.item(at: CGPoint(x: 250, y: 80), in: grid) == 2)
        #expect(DragSelectHit.item(at: CGPoint(x: 150, y: 150), in: grid) == 4)
    }

    @Test func besideItemsTheNearestInTheRow() {
        // The gap between two tiles: the nearer one, the one before on a tie.
        #expect(DragSelectHit.item(at: CGPoint(x: 100.5, y: 80), in: grid) == 0)
        #expect(DragSelectHit.item(at: CGPoint(x: 101.5, y: 80), in: grid) == 1)
        #expect(DragSelectHit.item(at: CGPoint(x: 101, y: 80), in: grid) == 0)
        // Past the end of a short row: its last tile.
        #expect(DragSelectHit.item(at: CGPoint(x: 280, y: 150), in: grid) == 4)
        // Left of the grid.
        #expect(DragSelectHit.item(at: CGPoint(x: -20, y: 150), in: grid) == 3)
    }

    @Test func overATitleOrBetweenRowsOrPastThemNothing() {
        // The drag keeps the item it was last over.
        #expect(DragSelectHit.item(at: CGPoint(x: 50, y: 10), in: grid) == nil)
        #expect(DragSelectHit.item(at: CGPoint(x: 50, y: 131), in: grid) == nil)
        #expect(DragSelectHit.item(at: CGPoint(x: 50, y: 400), in: grid) == nil)
        #expect(DragSelectHit.item(at: CGPoint(x: 50, y: 50), in: [Int: CGRect]()) == nil)
    }

    @Test func aPressStartsOnlyOnAnItemItself() {
        // Inside a tile: that one.
        #expect(DragSelectHit.item(containing: CGPoint(x: 50, y: 80), in: grid) == 0)
        #expect(DragSelectHit.item(containing: CGPoint(x: 150, y: 150), in: grid) == 4)
        #expect(DragSelectHit.item(containing: CGPoint(x: 102, y: 30), in: grid) == 1)
        // Beside tiles in the same row band: nothing - not the nearest, as
        // a move would take (besideItemsTheNearestInTheRow).
        #expect(DragSelectHit.item(containing: CGPoint(x: 101, y: 80), in: grid) == nil)
        #expect(DragSelectHit.item(containing: CGPoint(x: 280, y: 150), in: grid) == nil)
        #expect(DragSelectHit.item(containing: CGPoint(x: -20, y: 150), in: grid) == nil)
        // Nor over a title, between rows or past them.
        #expect(DragSelectHit.item(containing: CGPoint(x: 50, y: 10), in: grid) == nil)
        #expect(DragSelectHit.item(containing: CGPoint(x: 50, y: 131), in: grid) == nil)
        #expect(DragSelectHit.item(containing: CGPoint(x: 50, y: 400), in: grid) == nil)
    }

    @Test func aPressOnARowsFramelessFirstCellStartsNothing() {
        // Files' grid: ".." (16...120) has no frame, its two folders do. A
        // press on ".." starts nothing (it goes up), though a move there
        // is over the nearest folder.
        let f: [String: CGRect] = [
            "/Photos": CGRect(x: 130, y: 16, width: 104, height: 130),
            "/Docs": CGRect(x: 244, y: 16, width: 104, height: 130),
        ]
        #expect(DragSelectHit.item(containing: CGPoint(x: 60, y: 70), in: f) == nil)
        #expect(DragSelectHit.item(at: CGPoint(x: 60, y: 70), in: f) == "/Photos")
        #expect(DragSelectHit.item(containing: CGPoint(x: 180, y: 70), in: f) == "/Photos")
        // Images: a month of two photos in a row of four, a press on the
        // empty slots after them.
        let month: [Int: CGRect] = [0: CGRect(x: 0, y: 0, width: 90, height: 90),
                                    1: CGRect(x: 92, y: 0, width: 90, height: 90)]
        #expect(DragSelectHit.item(containing: CGPoint(x: 320, y: 45), in: month) == nil)
        #expect(DragSelectHit.item(at: CGPoint(x: 320, y: 45), in: month) == 1)
    }

    @Test func monthsSideBySide() {
        // Two months on one line, a 2 wide gap between them: one tile of
        // the first month (room for two), two of the next.
        let f: [String: CGRect] = [
            "jan1": CGRect(x: 0, y: 20, width: 100, height: 100),
            "feb1": CGRect(x: 206, y: 20, width: 100, height: 100),
            "feb2": CGRect(x: 308, y: 20, width: 100, height: 100),
        ]
        #expect(DragSelectHit.item(at: CGPoint(x: 120, y: 60), in: f) == "jan1")
        #expect(DragSelectHit.item(at: CGPoint(x: 190, y: 60), in: f) == "feb1")
        #expect(DragSelectHit.item(at: CGPoint(x: 320, y: 60), in: f) == "feb2")
        // Their titles, above the tiles: nothing.
        #expect(DragSelectHit.item(at: CGPoint(x: 220, y: 10), in: f) == nil)
    }

    @Test func onlyItemsOnScreenCount() {
        let frames = DragSelectFrames<String>()
        frames.record("a", CGRect(x: 0, y: 0, width: 100, height: 100))
        // Recorded, but not on screen (yet, or any more).
        #expect(frames.item(at: CGPoint(x: 50, y: 50)) == nil)
        frames.appeared("a")
        #expect(frames.item(at: CGPoint(x: 50, y: 50)) == "a")
        // A new view of it (rows built again) came before the old one went.
        frames.appeared("a")
        frames.disappeared("a")
        #expect(frames.item(at: CGPoint(x: 50, y: 50)) == "a")
        // Moved: its latest frame.
        frames.record("a", CGRect(x: 0, y: 200, width: 100, height: 100))
        #expect(frames.item(at: CGPoint(x: 50, y: 50)) == nil)
        #expect(frames.item(at: CGPoint(x: 50, y: 250)) == "a")
        frames.disappeared("a")
        #expect(frames.item(at: CGPoint(x: 50, y: 250)) == nil)
        // A stray disappearance keeps nothing negative.
        frames.disappeared("a")
        frames.appeared("a")
        #expect(frames.item(at: CGPoint(x: 50, y: 250)) == "a")
    }

    @Test func aPressStartsOnlyInsideAnItemOnScreen() {
        let frames = DragSelectFrames<String>()
        frames.record("a", CGRect(x: 0, y: 0, width: 100, height: 100))
        frames.record("b", CGRect(x: 102, y: 0, width: 100, height: 100))
        frames.appeared("b")
        // "a" isn't on screen: nothing there, though "b" is the nearest.
        #expect(frames.item(containing: CGPoint(x: 50, y: 50)) == nil)
        #expect(frames.item(at: CGPoint(x: 50, y: 50)) == "b")
        frames.appeared("a")
        #expect(frames.item(containing: CGPoint(x: 50, y: 50)) == "a")
        // The gap between them: nothing to start on.
        #expect(frames.item(containing: CGPoint(x: 101, y: 50)) == nil)
        #expect(frames.item(containing: CGPoint(x: 150, y: 50)) == "b")
    }

    // MARK: Scrolling near the edges

    @Test func theViewScrollsInTheEdgeBandsFasterTowardTheEdge() {
        // A view 600 tall: bands of 56 at either end.
        #expect(DragSelect.scrollSpeed(y: 300, bottom: 600) == 0)
        #expect(DragSelect.scrollSpeed(y: 57, bottom: 600) == 0)
        #expect(DragSelect.scrollSpeed(y: 543, bottom: 600) == 0)
        #expect(DragSelect.scrollSpeed(y: 28, bottom: 600) == -600)
        #expect(DragSelect.scrollSpeed(y: 572, bottom: 600) == 600)
        // At the edge and past it: the most there is.
        #expect(DragSelect.scrollSpeed(y: 0, bottom: 600) == -1200)
        #expect(DragSelect.scrollSpeed(y: -40, bottom: 600) == -1200)
        #expect(DragSelect.scrollSpeed(y: 650, bottom: 600) == 1200)
    }

    @Test func theBandsStartWhereTheVisiblePartDoes() {
        // A top inset of 100, and a bar over the bottom 104 of 800.
        #expect(DragSelect.scrollSpeed(y: 128, top: 100, bottom: 696) == -600)
        #expect(DragSelect.scrollSpeed(y: 157, top: 100, bottom: 696) == 0)
        #expect(DragSelect.scrollSpeed(y: 668, top: 100, bottom: 696) == 600)
        #expect(DragSelect.scrollSpeed(y: 750, top: 100, bottom: 696) == 1200)
    }

    @Test func aShortViewHasNarrowerBands() {
        // 120 tall: bands of 40.
        #expect(DragSelect.scrollSpeed(y: 60, bottom: 120) == 0)
        #expect(DragSelect.scrollSpeed(y: 20, bottom: 120) == -600)
        #expect(DragSelect.scrollSpeed(y: 100, bottom: 120) == 600)
        // Too short to scroll by itself at all.
        #expect(DragSelect.scrollSpeed(y: 0, bottom: 20) == 0)
    }

    @Test func noBandsNarrowerThan8Points() {
        // 24 tall: bands of 8, the narrowest there are.
        #expect(DragSelect.scrollSpeed(y: 0, bottom: 24) == -1200)
        #expect(DragSelect.scrollSpeed(y: 4, bottom: 24) == -600)
        #expect(DragSelect.scrollSpeed(y: 12, bottom: 24) == 0)
        #expect(DragSelect.scrollSpeed(y: 24, bottom: 24) == 1200)
        // 23 tall: they would be under 8, so there are none, even at or
        // past the edges.
        #expect(DragSelect.scrollSpeed(y: 0, bottom: 23) == 0)
        #expect(DragSelect.scrollSpeed(y: 23, bottom: 23) == 0)
        #expect(DragSelect.scrollSpeed(y: -40, bottom: 23) == 0)
        // What can be seen below a top inset is what counts.
        #expect(DragSelect.scrollSpeed(y: 100, top: 100, bottom: 124) == -1200)
        #expect(DragSelect.scrollSpeed(y: 100, top: 100, bottom: 123) == 0)
    }

    @Test func theEdgesScrollOnlyOnceTheFingerMovedOnFromThePress() {
        // The press fired in the bottom band: held still, or moved no more
        // than a press may, the view stays put.
        let pressed = CGPoint(x: 100, y: 580)
        #expect(!DragSelect.armed(from: pressed, to: pressed))
        #expect(!DragSelect.armed(from: pressed, to: CGPoint(x: 100, y: 590)))
        #expect(!DragSelect.armed(from: pressed, to: CGPoint(x: 106, y: 572)))
        #expect(!DragSelect.armed(from: pressed, to: CGPoint(x: 93, y: 587)))
        // Moved on past that, any way: it scrolls.
        #expect(DragSelect.armed(from: pressed, to: CGPoint(x: 100, y: 590.5)))
        #expect(DragSelect.armed(from: pressed, to: CGPoint(x: 89, y: 580)))
        #expect(DragSelect.armed(from: pressed, to: CGPoint(x: 108, y: 572)))
    }
}

/// Issue #200 in People: a press on a face's "more" button, which lies
/// over the face's corner while nothing is picked, starts no drag (as
/// Android's onFace and the web's faceAt).
@MainActor
struct PeopleDragSelectTests {
    @Test func aPressOnAFacesMoreButtonStartsNothing() {
        // A face of 96 at (16, 20); its 44 button stands 6 past its top
        // right corner.
        let faces = DragSelectFrames<String>(), buttons = DragSelectFrames<String>()
        faces.record("p", CGRect(x: 16, y: 20, width: 96, height: 96))
        faces.appeared("p")
        buttons.record("p", CGRect(x: 74, y: 14, width: 44, height: 44))
        buttons.appeared("p")
        #expect(PeopleView.faceToStart(at: CGPoint(x: 40, y: 70), faces: faces, buttons: buttons) == "p")
        // On the button: its own press.
        #expect(PeopleView.faceToStart(at: CGPoint(x: 100, y: 30), faces: faces, buttons: buttons) == nil)
        #expect(PeopleView.faceToStart(at: CGPoint(x: 75, y: 57), faces: faces, buttons: buttons) == nil)
        // Just below or left of it: the face.
        #expect(PeopleView.faceToStart(at: CGPoint(x: 100, y: 59), faces: faces, buttons: buttons) == "p")
        #expect(PeopleView.faceToStart(at: CGPoint(x: 73, y: 30), faces: faces, buttons: buttons) == "p")
        // Under the face (its name, its count): nothing.
        #expect(PeopleView.faceToStart(at: CGPoint(x: 60, y: 140), faces: faces, buttons: buttons) == nil)
        // Once something is picked the buttons go: the corner is the face's.
        buttons.disappeared("p")
        #expect(PeopleView.faceToStart(at: CGPoint(x: 100, y: 30), faces: faces, buttons: buttons) == "p")
    }
}

/// Issue #199 on the phones: the viewer's delete question says video for a
/// video (Android's ImageModal says the same).
@MainActor
struct ViewerDeleteTextTests {
    @Test func aVideoIsCalledAVideo() {
        let was = L10n.shared.code
        L10n.shared.set("en")
        let words = [ImageModal.deleteTitle(video: false), ImageModal.deleteButton(video: false),
                     ImageModal.deleteTitle(video: true), ImageModal.deleteButton(video: true)]
        L10n.shared.set(was)
        #expect(words == ["Delete this photo?", "Delete Photo", "Delete this video?", "Delete Video"])
    }
}

/// Issue #200 in Files: the drag over the rows (never ".."), and the bar's
/// folder actions, once the long-press menu's, as Android's bar has them.
@MainActor
struct FilesDragSelectTests {
    private func file(_ path: String, dir: Bool = false, out: Bool = false) -> Msg_File {
        var f = Msg_File()
        f.path = path
        f.mime = dir ? "inode/directory" : "image/jpeg"
        f.outOfImages = out
        return f
    }

    /// Files at /Trips/, listed: "..", A, B (folders first), c.jpg, d.jpg.
    private func explorer(supported: Bool = true, folderOut: Bool = false) async -> FilesExplorerViewModel {
        let suite = "drag-select-\(UUID().uuidString)"
        let vm = FilesExplorerViewModel(initialPath: "/Trips/", defaults: UserDefaults(suiteName: suite)!)
        UserDefaults().removePersistentDomain(forName: suite)
        vm.thumbDisk = testThumbCache()
        var lof = Msg_ListOfFiles()
        lof.files = [file("/Trips/d.jpg"), file("/Trips/B", dir: true, out: true), file("/Trips/c.jpg"), file("/Trips/A", dir: true)]
        lof.outOfImagesSupported = supported
        lof.folderOutOfImages = folderOut
        var listing = Msg_RespEnvelope()
        listing.payload = .respListOfFiles(lof)
        vm.request = { _ in listing }
        await vm.load()
        return vm
    }

    @Test func aDragSelectsTheRowsButNeverTheParent() async {
        let vm = await explorer()
        #expect(vm.rows.map(\.path) == ["..", "/Trips/A", "/Trips/B", "/Trips/c.jpg", "/Trips/d.jpg"])
        vm.beginDragSelect("..")
        vm.dragSelect(to: "/Trips/B")
        #expect(vm.selected.isEmpty)

        vm.beginDragSelect("/Trips/d.jpg")
        #expect(vm.selected == ["/Trips/d.jpg"])
        vm.dragSelect(to: "/Trips/A")
        #expect(vm.selected == ["/Trips/A", "/Trips/B", "/Trips/c.jpg", "/Trips/d.jpg"])
        // Over "..": the range stays as it was.
        vm.dragSelect(to: "..")
        #expect(vm.selected.count == 4)
        vm.dragSelect(to: "/Trips/c.jpg")
        #expect(vm.selected == ["/Trips/c.jpg", "/Trips/d.jpg"])
        vm.endDragSelect()
        // Ended: the finger going on changes nothing.
        vm.dragSelect(to: "/Trips/A")
        #expect(vm.selected == ["/Trips/c.jpg", "/Trips/d.jpg"])
        // While something is selected a tap picks or leaves out.
        vm.toggleSelected("/Trips/A")
        vm.toggleSelected("/Trips/c.jpg")
        #expect(vm.selected == ["/Trips/A", "/Trips/d.jpg"])
    }

    @Test func aNewListingMidDragEndsIt() async {
        let vm = await explorer()
        vm.beginDragSelect("/Trips/A")
        vm.dragSelect(to: "/Trips/c.jpg")
        #expect(vm.selected == ["/Trips/A", "/Trips/B", "/Trips/c.jpg"])
        // The folder is listed again (a refresh, an upload done) while the
        // finger is still down: the selection is what the listing makes
        // it, and the drag is over - the finger going on changes nothing.
        await vm.load()
        #expect(vm.selected.isEmpty)
        #expect(!vm.dragSelect(to: "/Trips/d.jpg"))
        #expect(!vm.dragSelect(to: "/Trips/B"))
        #expect(vm.selected.isEmpty)
        vm.endDragSelect()
        // The next press starts a drag as usual.
        vm.beginDragSelect("/Trips/B")
        #expect(vm.dragSelect(to: "/Trips/c.jpg"))
        #expect(vm.selected == ["/Trips/B", "/Trips/c.jpg"])
    }

    @Test func oneFolderPickedOffersItsGalleryAndItsSwitchForImages() async {
        let vm = await explorer()
        vm.selected = ["/Trips/B"]
        #expect(vm.selectedFolder?.path == "/Trips/B")
        #expect(vm.outOfImagesSwitch?.path == "/Trips/B")
        // B is kept out: the bar offers Show in Images.
        #expect(vm.outOfImagesSwitch?.outOfImages == true)
        // A file, or more than one thing: neither.
        vm.selected = ["/Trips/c.jpg"]
        #expect(vm.selectedFolder == nil)
        vm.selected = ["/Trips/A", "/Trips/B"]
        #expect(vm.selectedFolder == nil)
        #expect(vm.outOfImagesSwitch == nil)
    }

    @Test func noSwitchForImagesWhereTheDeviceCantOrInsideAFolderKeptOut() async {
        let old = await explorer(supported: false)
        old.selected = ["/Trips/A"]
        #expect(old.selectedFolder?.path == "/Trips/A")
        #expect(old.outOfImagesSwitch == nil)
        let inside = await explorer(folderOut: true)
        inside.selected = ["/Trips/A"]
        #expect(inside.selectedFolder?.path == "/Trips/A")
        #expect(inside.outOfImagesSwitch == nil)
    }
}

/// Issue #200 in Images: the photos' order is the grid's, months side by
/// side included (its items, as PhotoGridLayout lays them).
@MainActor
struct ImagesDragSelectTests {
    private func item(_ i: Int) -> PhotoGalleryVM.Item {
        PhotoGalleryVM.Item(id: "i\(i)", path: "/p/\(i).jpg", mime: "image/jpeg", size: 1, thumbData: nil,
                            localURL: nil, isLocalOnly: false, month: "2026-0\(1 + i / 3)")
    }

    @Test func aDragSelectsThePhotosInTheGridsOrder() {
        let vm = PhotoGalleryVM()
        vm.items = (0..<8).map(item)
        vm.selected = ["/p/7.jpg"]
        vm.beginDragSelect(at: 5)
        #expect(vm.selected == ["/p/5.jpg", "/p/7.jpg"])
        vm.dragSelect(to: 2)
        #expect(vm.selected == ["/p/2.jpg", "/p/3.jpg", "/p/4.jpg", "/p/5.jpg", "/p/7.jpg"])
        // Back past the pressed photo: the other way, the earlier ones as they were.
        vm.dragSelect(to: 6)
        #expect(vm.selected == ["/p/5.jpg", "/p/6.jpg", "/p/7.jpg"])
        // A page more comes in mid-drag: the range carries on into it.
        vm.items += (8..<10).map(item)
        vm.dragSelect(to: 9)
        #expect(vm.selected == Set((5...9).map { "/p/\($0).jpg" }))
        // Off the end: ignored.
        vm.dragSelect(to: 40)
        #expect(vm.selected.count == 5)
        vm.endDragSelect()
        vm.dragSelect(to: 0)
        #expect(vm.selected.count == 5)
    }

    @Test func thePressedPhotoGoneEndsTheDragAsItStands() {
        let vm = PhotoGalleryVM()
        vm.items = (0..<8).map(item)
        vm.beginDragSelect(at: 2)
        #expect(vm.dragSelect(to: 4))
        // Over a title or a gap: no photo, the drag goes on as it was.
        #expect(vm.dragSelect(to: nil))
        #expect(vm.selected == ["/p/2.jpg", "/p/3.jpg", "/p/4.jpg"])
        // A refresh took the pressed photo away: the drag is over (the
        // gesture stops following), the selection as it stands.
        vm.items.remove(at: 2)
        #expect(!vm.dragSelect(to: 5))
        #expect(vm.selected == ["/p/2.jpg", "/p/3.jpg", "/p/4.jpg"])
        // Later moves do nothing, even with it back.
        vm.items.insert(item(2), at: 2)
        #expect(!vm.dragSelect(to: 6))
        #expect(!vm.dragSelect(to: nil))
        #expect(vm.selected == ["/p/2.jpg", "/p/3.jpg", "/p/4.jpg"])
        vm.endDragSelect()
    }

    @Test func aPhotoPickedBeforeTheDragAndDeletedMidDragStaysOut() {
        let vm = PhotoGalleryVM()
        vm.items = (0..<8).map(item)
        vm.selected = ["/p/0.jpg", "/p/7.jpg"]
        vm.beginDragSelect(at: 3)
        #expect(vm.dragSelect(to: 4))
        #expect(vm.selected == ["/p/0.jpg", "/p/3.jpg", "/p/4.jpg", "/p/7.jpg"])
        // /p/7.jpg is deleted meanwhile (the viewer, the bar): it leaves the
        // grid and the selection, as a delete does.
        vm.items.removeAll { $0.path == "/p/7.jpg" }
        vm.selected.remove("/p/7.jpg")
        // The next moves don't pick it again.
        #expect(vm.dragSelect(to: 5))
        #expect(vm.selected == ["/p/0.jpg", "/p/3.jpg", "/p/4.jpg", "/p/5.jpg"])
        #expect(vm.dragSelect(to: 2))
        #expect(vm.selected == ["/p/0.jpg", "/p/2.jpg", "/p/3.jpg"])
        // A page more coming in forgets nothing still there (/p/9.jpg is
        // the 9th photo now).
        vm.items += (8..<10).map(item)
        #expect(vm.dragSelect(to: 8))
        #expect(vm.selected == Set([0, 3, 4, 5, 6, 8, 9].map { "/p/\($0).jpg" }))
        vm.endDragSelect()
    }
}

/// The selection bar's labels, broken at spaces only (BarLabelLines): with
/// six items (Files, one folder picked) a German label's quote made Text
/// break the next word instead ("In „Bilder“ au-sblenden").
struct BarLabelLinesTests {
    @Test func oneLineWhenItFits() {
        #expect(BarLabelLines.arrange(widths: [30, 12, 40], space: 3, width: 90) == [[0, 1, 2]])
        #expect(BarLabelLines.arrange(widths: [30], space: 3, width: 10) == [[0]])
        #expect(BarLabelLines.arrange(widths: [], space: 3, width: 10) == [])
    }

    @Test func fewestLinesTheLastNoShorter() {
        // "Share" over "as gallery", as Text had it - not "Share as" over "gallery".
        #expect(BarLabelLines.arrange(widths: [30, 12, 40], space: 3, width: 60) == [[0], [1, 2]])
        // "In „Bilder“" over "ausblenden": the quote is no reason to break the word.
        #expect(BarLabelLines.arrange(widths: [10, 42, 58], space: 3, width: 61) == [[0, 1], [2]])
        // Three lines where two don't fit: "Zur" / "Sammlung" / "hinzufügen".
        #expect(BarLabelLines.arrange(widths: [18, 50, 58], space: 3, width: 60) == [[0], [1], [2]])
    }

    @Test func theNarrowestWhenNoneRises() {
        #expect(BarLabelLines.arrange(widths: [40, 20, 10], space: 3, width: 45) == [[0], [1, 2]])
    }

    @Test func aWordWiderThanTheItemHasALineOfItsOwn() {
        #expect(BarLabelLines.arrange(widths: [10, 70], space: 3, width: 60) == [[0], [1]])
    }
}
