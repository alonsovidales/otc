// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.gallery

import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

// Save to this phone (SaveToPhone.kt): which of the picked photos and videos
// are saved, which are left out as already on this phone, and what the note
// says once they are. The same cases as iOS's SaveToPhoneTests.
class SaveToPhoneTest {
    private fun item(path: String, hash: String = "") = PhotoGalleryViewModel.Item(path, path, "image/jpeg", 1, null, hash = hash)
    private fun saved(plan: SaveToPhone.Plan) = plan.save.map { it.path }

    /** A saved photo is remembered by its content, else by its path - never one for the other. */
    @Test fun rememberedByTheHashElseThePath() {
        assertEquals(SaveToPhone.key("h1", "/a.jpg"), SaveToPhone.key("h1", "/b.jpg"))
        assertNotEquals(SaveToPhone.key("h1", "/a.jpg"), SaveToPhone.key("h2", "/a.jpg"))
        assertEquals(SaveToPhone.key("", "/a.jpg"), SaveToPhone.key("", "/a.jpg"))
        assertNotEquals(SaveToPhone.key("", "/a.jpg"), SaveToPhone.key("", "/b.jpg"))
        assertNotEquals(SaveToPhone.key("", "/a.jpg"), SaveToPhone.key("/a.jpg", "/x.jpg"))
    }

    /** Without a hash there is nothing to tell it by: always saved, however often picked. */
    @Test fun noHashIsAlwaysSaved() {
        val plan = SaveToPhone.plan(
            listOf(item("/a.jpg"), item("/b.jpg")),
            onPhone = setOf(""), saved = setOf(SaveToPhone.key("", "/a.jpg")),
        )
        assertEquals(listOf("/a.jpg", "/b.jpg"), saved(plan))
        assertEquals(0, plan.already)
    }

    /** What this phone's library holds is left out, counted as already there. */
    @Test fun whatThePhoneHoldsIsLeftOut() {
        val plan = SaveToPhone.plan(listOf(item("/a.jpg", "h1"), item("/b.jpg", "h2"), item("/c.jpg", "h3")), onPhone = setOf("h2", "h9"), saved = emptySet())
        assertEquals(listOf("/a.jpg", "/c.jpg"), saved(plan))
        assertEquals(1, plan.already)
    }

    /** What this view model saved already (the viewer's Save, an earlier Save to this phone) too. */
    @Test fun whatWasSavedFromHereIsLeftOut() {
        val plan = SaveToPhone.plan(
            listOf(item("/a.jpg", "h1"), item("/b.jpg", "h2")),
            onPhone = emptySet(), saved = setOf(SaveToPhone.key("h1", "/elsewhere.jpg")),
        )
        assertEquals(listOf("/b.jpg"), saved(plan))
        assertEquals(1, plan.already)
        // An edit is other content: saved again.
        val edited = SaveToPhone.plan(listOf(item("/a.jpg", "h1-edited")), onPhone = emptySet(), saved = setOf(SaveToPhone.key("h1", "/a.jpg")))
        assertEquals(listOf("/a.jpg"), saved(edited))
        assertEquals(0, edited.already)
    }

    /** The same content picked twice (two paths, one hash) is saved once - and counted once when already there. */
    @Test fun twoCopiesAreSavedOnce() {
        val plan = SaveToPhone.plan(listOf(item("/a.jpg", "h1"), item("/copy/a.jpg", "h1"), item("/b.jpg", "h2")), onPhone = emptySet(), saved = emptySet())
        assertEquals(listOf("/a.jpg", "/b.jpg"), saved(plan))
        assertEquals(0, plan.already)
        val there = SaveToPhone.plan(listOf(item("/a.jpg", "h1"), item("/copy/a.jpg", "h1")), onPhone = setOf("h1"), saved = emptySet())
        assertTrue(there.save.isEmpty())
        assertEquals(1, there.already)
    }

    /** In the order they were picked, whatever is left out between them. */
    @Test fun theOrderIsKept() {
        val picked = listOf(item("/5.jpg", "h5"), item("/1.jpg", "h1"), item("/x.jpg"), item("/3.jpg", "h3"), item("/9.jpg", "h9"), item("/2.jpg", "h2"))
        val plan = SaveToPhone.plan(picked, onPhone = setOf("h3"), saved = setOf(SaveToPhone.key("h9", "/9.jpg")))
        assertEquals(listOf("/5.jpg", "/1.jpg", "/x.jpg", "/2.jpg"), saved(plan))
        assertEquals(2, plan.already)
    }

    /** The note: how many were saved, then how many were there already; either alone; nothing when neither. */
    @Test fun theNotesThreeForms() {
        fun note(saved: Int, already: Int) = SaveToPhone.note(saved, already).joinToString(" ") { it.english() }
        assertEquals("Saved 3 to this phone.", note(3, 0))
        assertEquals("Saved 1 to this phone.", note(1, 0))
        assertEquals("Saved 3 to this phone. 2 were already on this phone.", note(3, 2))
        assertEquals("Saved 2 to this phone. 1 was already on this phone.", note(2, 1))
        assertEquals("2 were already on this phone.", note(0, 2))
        assertEquals("1 was already on this phone.", note(0, 1))
        assertTrue(SaveToPhone.note(0, 0).isEmpty())
    }
}
