// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.compose

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appSocialComposeCaptionNeeded
import cloud.offthe.otc.i18n.appSocialComposePosition
import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// The composer (A2, IOS-11): picks kept across a change of source or tag and
// published from there in their order, a post leaving nothing behind, and
// the line saying why Publish waits. As ComposerTests.swift.
class ComposerTest {
    private fun synced(path: String) = NewPostPickerViewModel.Item("$path#h#1", path)
    private fun phone(id: Long) = NewPostPickerViewModel.Item("local#$id", "")

    /** TalkBack names a synced tile by its File's date, as Images' tile ("Photo, 5 October 2026"). */
    @Test fun aSyncedTileIsNamedByItsDate() {
        val at = java.time.ZonedDateTime.of(2026, 10, 5, 12, 0, 0, 0, java.time.ZoneOffset.UTC).toInstant().toEpochMilli()
        val photo = NewPostPickerViewModel.Item("/a.jpg#h#1", "/a.jpg", createdMs = at)
        assertEquals("Photo, 5 October 2026", cloud.offthe.otc.ui.gallery.tileLabel(false, photo.takenMs, java.time.ZoneOffset.UTC).english())
        val video = NewPostPickerViewModel.Item("/b.mp4#h#1", "/b.mp4", isVideo = true)
        assertNull(video.takenMs)
        assertEquals("Video", cloud.offthe.otc.ui.gallery.tileLabel(true, video.takenMs, java.time.ZoneOffset.UTC).english())
        assertEquals("Number 2 in the post", S.appSocialComposePosition(2).english())
    }

    /** TalkBack steps a trimmer handle a hundredth of the clip, a tenth of a second at least (iOS's DualRangeSlider.step). */
    @Test fun aTrimmerHandleStepsAHundredthOfTheClip() {
        assertEquals(0.6, trimStep(60.0), 1e-9)
        assertEquals(0.1, trimStep(3.0), 1e-9)
        assertEquals(1.2, trimStep(118.0), 1e-9)
    }

    @Test fun picksStayWhenTheGridShowsAnotherSource() {
        // Two from Synced, then the grid starts over on Phone (its items
        // gone): the picks don't go with them.
        var picks = ComposerPicks().toggle(synced("/a.jpg")).toggle(synced("/b.jpg"))
        val gridAfterSwitch = listOf(phone(1), phone(2))
        picks = picks.toggle(gridAfterSwitch[1])
        assertEquals(listOf("/a.jpg#h#1", "/b.jpg#h#1", "local#2"), picks.picked.map { it.id })
        assertEquals(3, picks.size)
        // Shown numbered where the grid has them, and in the strip all three.
        assertEquals(3, picks.number("local#2"))
        assertNull(picks.number("local#1"))
        assertEquals("/a.jpg", picks.item("/a.jpg#h#1")?.path)
    }

    @Test fun aSecondTapUnpicksAndTheRestKeepTheirOrder() {
        var picks = ComposerPicks().toggle(synced("/a.jpg")).toggle(synced("/b.jpg")).toggle(synced("/c.jpg"))
        picks = picks.toggle(synced("/b.jpg"))
        assertEquals(listOf("/a.jpg#h#1", "/c.jpg#h#1"), picks.order)
        assertFalse("/b.jpg#h#1" in picks)
        assertEquals(picks, picks.remove("/nothing"))
    }

    @Test fun aCaptureIsPickedLastAndKeepsItsPlaceIfPickedAlready() {
        var picks = ComposerPicks().toggle(synced("/a.jpg"))
        picks = picks.add(phone(9))
        assertEquals(listOf("/a.jpg#h#1", "local#9"), picks.order)
        // Added again (the capture came back from its page): still one, same place.
        picks = picks.add(phone(9)).toggle(synced("/b.jpg")).add(phone(9))
        assertEquals(listOf("/a.jpg#h#1", "local#9", "/b.jpg#h#1"), picks.order)
    }

    @Test fun theStripMovesAPickOnePlace() {
        val picks = ComposerPicks().toggle(synced("/a.jpg")).toggle(synced("/b.jpg")).toggle(synced("/c.jpg"))
        assertEquals(listOf("/b.jpg#h#1", "/a.jpg#h#1", "/c.jpg#h#1"), picks.move("/a.jpg#h#1", 1).order)
        assertEquals(listOf("/a.jpg#h#1", "/c.jpg#h#1", "/b.jpg#h#1"), picks.move("/c.jpg#h#1", -1).order)
        // Past either end, or not picked: as it was.
        assertEquals(picks.order, picks.move("/a.jpg#h#1", -1).order)
        assertEquals(picks.order, picks.move("/c.jpg#h#1", 1).order)
        assertEquals(picks.order, picks.move("/z.jpg#h#1", 1).order)
    }

    @Test fun aPostLeavesNothingForTheNext() {
        val st = NewPostPickerViewModel.State(
            caption = "Sunday", picks = ComposerPicks().toggle(synced("/a.jpg")),
            trims = mapOf("/a.jpg#h#1" to TrimRange(1.0, 2.0)), items = listOf(synced("/a.jpg")),
        )
        val after = st.posted()
        assertEquals("", after.caption)
        assertTrue(after.picks.isEmpty())
        assertTrue(after.trims.isEmpty())
        // The grid stays while the composer closes.
        assertEquals(st.items, after.items)
        // A new opening starts with nothing (its own view model, ComposerStore).
        val fresh = NewPostPickerViewModel.State()
        assertEquals("", fresh.caption)
        assertTrue(fresh.picks.isEmpty())
        assertEquals(NewPostPickerViewModel.Source.PHONE, fresh.source)
    }

    @Test fun publishWaitsForACaptionAndSaysSo() {
        assertTrue(NewPostPickerViewModel.captionNeeded(picked = 1, caption = "", publishing = false))
        assertTrue(NewPostPickerViewModel.captionNeeded(picked = 2, caption = "  \n", publishing = false))
        // Nothing picked: plain to see; a caption: nothing to say; publishing: its own line.
        assertFalse(NewPostPickerViewModel.captionNeeded(picked = 0, caption = "", publishing = false))
        assertFalse(NewPostPickerViewModel.captionNeeded(picked = 1, caption = "Hi", publishing = false))
        assertFalse(NewPostPickerViewModel.captionNeeded(picked = 1, caption = "", publishing = true))
        assertTrue(NewPostPickerViewModel.State(picks = ComposerPicks().toggle(synced("/a.jpg"))).captionNeeded)
        assertEquals("Add a caption to publish", S.appSocialComposeCaptionNeeded().english())
    }

    @Test fun aThumbnailThatLandsLaterIsTheTilesKey() {
        // An Item equals another by its id alone: a key that lands goes in
        // thumbKeys (an equal State with a new Item key was dropped).
        val item = synced("/a.jpg")
        val st = NewPostPickerViewModel.State(items = listOf(item))
        // Why: the old way, an item copied with its key, is an equal State -
        // a StateFlow keeps the old one, and the tile stayed grey.
        assertEquals(st, st.copy(items = st.items.map { it.copy(thumbKey = "k1") }))
        assertNull(st.thumbKeyOf(item))
        val landed = st.copy(thumbKeys = mapOf("/a.jpg" to "k1"))
        assertFalse(landed == st)
        assertEquals("k1", landed.thumbKeyOf(item))
        // Let go: none, over the page's own key.
        assertNull(landed.copy(thumbKeys = mapOf("/a.jpg" to null)).thumbKeyOf(item.copy(thumbKey = "page")))
        assertEquals("page", st.thumbKeyOf(item.copy(thumbKey = "page")))
    }
}
