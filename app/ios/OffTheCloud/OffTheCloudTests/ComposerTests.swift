// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// The composer (IOS-11, A2): picks kept across a change of source or tag
/// and published from there in their order, and the line saying why Publish
/// waits. Android's ComposerTest is the same.
struct ComposerTests {
    private func synced(_ path: String) -> NewPostPickerVM.Item { NewPostPickerVM.Item(id: "\(path)#h#1", path: path) }
    private func phone(_ id: Int) -> NewPostPickerVM.Item { NewPostPickerVM.Item(id: "local#\(id)", path: "") }

    @Test func picksStayWhenTheGridShowsAnotherSource() {
        // Two from Synced, then the grid starts over on Phone (its items
        // gone): the picks don't go with them.
        var picks = ComposerPicks()
        picks.toggle(synced("/a.jpg"))
        picks.toggle(synced("/b.jpg"))
        let gridAfterSwitch = [phone(1), phone(2)]
        picks.toggle(gridAfterSwitch[1])
        #expect(picks.picked.map(\.id) == ["/a.jpg#h#1", "/b.jpg#h#1", "local#2"])
        #expect(picks.count == 3)
        // Numbered where the grid has them, and in the strip all three.
        #expect(picks.number(of: "local#2") == 3)
        #expect(picks.number(of: "local#1") == nil)
        #expect(picks.item("/a.jpg#h#1")?.path == "/a.jpg")
    }

    @Test func aSecondTapUnpicksAndTheRestKeepTheirOrder() {
        var picks = ComposerPicks()
        for p in ["/a.jpg", "/b.jpg", "/c.jpg"] { picks.toggle(synced(p)) }
        picks.toggle(synced("/b.jpg"))
        #expect(picks.order == ["/a.jpg#h#1", "/c.jpg#h#1"])
        #expect(!picks.contains("/b.jpg#h#1"))
        picks.remove("/nothing")
        #expect(picks.count == 2)
    }

    @Test func aCaptureIsPickedLastAndKeepsItsPlaceIfPickedAlready() {
        var picks = ComposerPicks()
        picks.toggle(synced("/a.jpg"))
        picks.add(phone(9))
        #expect(picks.order == ["/a.jpg#h#1", "local#9"])
        // Added again (the capture came back from its page): still one, same place.
        picks.add(phone(9))
        picks.toggle(synced("/b.jpg"))
        picks.add(phone(9))
        #expect(picks.order == ["/a.jpg#h#1", "local#9", "/b.jpg#h#1"])
    }

    @Test func theStripMovesAPickOnePlace() {
        var picks = ComposerPicks()
        for p in ["/a.jpg", "/b.jpg", "/c.jpg"] { picks.toggle(synced(p)) }
        var moved = picks
        moved.move("/a.jpg#h#1", by: 1)
        #expect(moved.order == ["/b.jpg#h#1", "/a.jpg#h#1", "/c.jpg#h#1"])
        moved = picks
        moved.move("/c.jpg#h#1", by: -1)
        #expect(moved.order == ["/a.jpg#h#1", "/c.jpg#h#1", "/b.jpg#h#1"])
        // Past either end, or not picked: as it was.
        for (id, offset) in [("/a.jpg#h#1", -1), ("/c.jpg#h#1", 1), ("/z.jpg#h#1", 1)] {
            moved = picks
            moved.move(id, by: offset)
            #expect(moved.order == picks.order)
        }
    }

    @Test func publishWaitsForACaptionAndSaysSo() {
        #expect(NewPostPickerVM.captionNeeded(picked: 1, caption: "", publishing: false))
        #expect(NewPostPickerVM.captionNeeded(picked: 2, caption: "  \n", publishing: false))
        // Nothing picked: plain to see; a caption: nothing to say;
        // publishing: its own line.
        #expect(!NewPostPickerVM.captionNeeded(picked: 0, caption: "", publishing: false))
        #expect(!NewPostPickerVM.captionNeeded(picked: 1, caption: "Hi", publishing: false))
        #expect(!NewPostPickerVM.captionNeeded(picked: 1, caption: "", publishing: true))
        #expect(S.appSocialComposeCaptionNeeded == "Add a caption to publish")
    }
}
