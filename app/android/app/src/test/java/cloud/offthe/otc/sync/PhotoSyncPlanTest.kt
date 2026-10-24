// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import cloud.offthe.otc.sync.PhotoSync.AccessStep
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** What a sync run asks the MediaStore for, its one-off passes and limited photo access. */
class PhotoSyncPlanTest {
    /** Nothing turned off: the query is the one the sync always made, byte for byte. */
    @Test fun selectionAsBeforeWithNothingSkipped() {
        assertEquals("media_type IN (1,3)", PhotoSync.selection(includeVideos = true, sinceMs = 0))
        assertEquals("media_type IN (1) AND date_added > 1700000000", PhotoSync.selection(includeVideos = false, sinceMs = 1_700_000_000_123))
        assertEquals(PhotoSync.selection(true, 5_000), PhotoSync.selection(true, 5_000, skipped = emptySet()))
    }

    @Test fun selectionLeavesSkippedFoldersOut() {
        assertEquals(
            "media_type IN (1,3) AND date_added > 5 AND (bucket_id IS NULL OR bucket_id NOT IN (12,-5))",
            PhotoSync.selection(true, 5_000, skipped = setOf("12", "-5")),
        )
    }

    /** A folder turned back on: all it holds, with no date, still minus what is off. */
    @Test fun backfillSelectionIsOnlyThoseFolders() {
        assertEquals(
            "media_type IN (1) AND (bucket_id IS NULL OR bucket_id NOT IN (12)) AND bucket_id IN (7)",
            PhotoSync.selection(false, 0, skipped = setOf("12"), only = setOf("7", PhotoSync.CATCH_UP)),
        )
        // Naming no folder, it fetches nothing - never the whole library.
        assertNull(PhotoSync.selection(true, 0, only = setOf(PhotoSync.CATCH_UP)))
        assertNull(PhotoSync.selection(true, 0, only = emptySet()))
    }

    /** A pass that went through takes off only what it took: what was queued meanwhile stays. */
    @Test fun afterPassKeepsWhatCameMeanwhile() {
        val stored = setOf("7", PhotoSync.CATCH_UP, "9")
        val taken = setOf("7", PhotoSync.CATCH_UP)
        assertEquals(setOf("9"), PhotoSync.afterPass(stored, taken, finished = true))
        assertEquals(stored, PhotoSync.afterPass(stored, taken, finished = false))
        // Turned off and dropped while the pass ran: gone either way.
        assertEquals(emptySet<String>(), PhotoSync.afterPass(setOf(PhotoSync.CATCH_UP), taken, finished = true))
    }

    /** Turned off while the run went through its pass, and on again: the photos it let go come with the pass, still queued. */
    @Test fun afterPassKeepsAFolderCutShort() {
        // 7 taken, then off (dropped) and on (queued again); 9 queued meanwhile.
        assertEquals(setOf("7", "9"), PhotoSync.afterPass(setOf("7", "9"), setOf("7"), finished = true, cutShort = setOf("7")))
        // Still off at the end: dropped as it was turned off, and stays so.
        assertEquals(setOf("9"), PhotoSync.afterPass(setOf("9"), setOf("7"), finished = true, cutShort = setOf("7")))
        // Cut short in the main pass only: queued again, it stays.
        assertEquals(setOf("7"), PhotoSync.afterPass(setOf("7", PhotoSync.CATCH_UP), setOf(PhotoSync.CATCH_UP), finished = true, cutShort = setOf("7")))
        // What else it took is done.
        assertEquals(setOf("7"), PhotoSync.afterPass(setOf("7", "8"), setOf("7", "8"), finished = true, cutShort = setOf("7")))
    }

    /** Under limited access a pass sees only the picked photos: none is done until a run with full access goes through it. */
    @Test fun afterPassUnderLimitedAccessKeepsEveryPass() {
        val stored = setOf("7", PhotoSync.CATCH_UP)
        assertEquals(stored, PhotoSync.afterPass(stored, stored, finished = true, limited = true))
        assertEquals(emptySet<String>(), PhotoSync.afterPass(stored, stored, finished = true, limited = false))
    }

    // A MediaStore row as the fake query below sees it; added in ms.
    private data class Row(val id: Long, val bucket: String, val added: Long, val video: Boolean = false)

    /** The sync's query over [rows] (since, just these folders, Include videos, the folders turned off), noting every call. */
    private class Library(val rows: List<Row>, val includeVideos: Boolean = true, val skipped: Set<String> = emptySet()) {
        val calls = mutableListOf<Pair<Long, Set<String>?>>()
        fun fetch(since: Long, only: Set<String>?): List<Row> {
            calls += since to only
            return rows.filter { (includeVideos || !it.video) && it.bucket !in skipped && it.added > since && (only == null || it.bucket in only) }
        }
    }

    private fun oneOff(lib: Library, taken: Set<String>, floor: Long? = null, picked: Set<Long> = emptySet(), fresh: Set<Long> = emptySet()) =
        PhotoSync.oneOffList(taken, floor, picked, fresh, Row::id, lib::fetch).map { it.id }

    private val library = listOf(
        Row(1, "7", 100), Row(2, "7", 5_000), Row(3, "9", 200), Row(4, "9", 6_000),
        Row(5, "12", 300), Row(6, "9", 400, video = true), Row(7, "7", 7_000, video = true),
    )

    /** Nothing taken and nothing picked: nothing asked, nothing sent. */
    @Test fun oneOffListEmptyAsksNothing() {
        val lib = Library(library)
        assertTrue(oneOff(lib, emptySet()).isEmpty())
        assertTrue(lib.calls.isEmpty())
    }

    /** A folder turned back on: all it holds, with no date, and only it. */
    @Test fun oneOffListFolderPassHasNoDate() {
        val lib = Library(library)
        assertEquals(listOf(1L, 2L, 7L), oneOff(lib, setOf("7")))
        assertEquals(listOf(0L to setOf("7")), lib.calls)
    }

    /** The catch-up goes from the floor, and only when the floor is known. */
    @Test fun oneOffListCatchUpOnlyWithAKnownFloor() {
        val unknown = Library(library)
        assertEquals(listOf(1L, 2L, 7L), oneOff(unknown, setOf("7", PhotoSync.CATCH_UP), floor = null))
        assertEquals(listOf(0L to setOf("7")), unknown.calls)
        val known = Library(library)
        assertEquals(listOf(2L, 4L, 7L), oneOff(known, setOf(PhotoSync.CATCH_UP), floor = 1_000))
        assertEquals(listOf(1_000L to null), known.calls)
        // Sync All's floor: everything.
        assertEquals(library.map { it.id }, oneOff(Library(library), setOf(PhotoSync.CATCH_UP), floor = 0))
    }

    /** A pick goes only as the sync's query returns it: not a video while Include videos is off, nothing from a folder turned off. */
    @Test fun oneOffListPicksFollowTheSettings() {
        val lib = Library(library, includeVideos = false, skipped = setOf("12"))
        assertEquals(listOf(1L, 3L), oneOff(lib, emptySet(), picked = setOf(1L, 3L, 5L, 6L)))
        assertEquals(listOf(0L to null), lib.calls)
    }

    /** Each photo once, in the order the passes come, and none the main list already has. */
    @Test fun oneOffListOnceAndNotInTheMainList() {
        val lib = Library(library)
        assertEquals(
            listOf(1L, 7L, 4L, 3L),
            oneOff(lib, setOf("7", PhotoSync.CATCH_UP), floor = 1_000, picked = setOf(1L, 3L, 4L), fresh = setOf(2L)),
        )
    }

    @Test fun newlyVisibleIsWhatWasPickedSince() {
        // The first time only notes what is there.
        assertTrue(PhotoSync.newlyVisible(null, setOf(1L, 2L, 3L)).isEmpty())
        assertEquals(setOf(3L), PhotoSync.newlyVisible(setOf(1L, 2L), setOf(1L, 2L, 3L)))
        // Photos no longer picked don't matter.
        assertEquals(setOf(4L), PhotoSync.newlyVisible(setOf(1L, 2L), setOf(2L, 4L)))
        assertTrue(PhotoSync.newlyVisible(setOf(1L, 2L), setOf(1L)).isEmpty())
    }

    /** Only full access after limited access does anything: the catch-up when the starting point is known, else the question again. */
    @Test fun accessStepEveryCase() {
        for (limited in listOf(false, true)) for (full in listOf(false, true)) for (was in listOf(false, true)) for (known in listOf(false, true)) {
            val want = when {
                !limited && full && was -> if (known) AccessStep.CATCH_UP else AccessStep.ASK_AGAIN
                else -> AccessStep.NONE
            }
            assertEquals("limited=$limited full=$full was=$was known=$known", want, PhotoSync.accessStep(limited, full, was, known))
        }
        // Access taken away after limited access isn't full.
        assertEquals(AccessStep.NONE, PhotoSync.accessStep(limited = false, full = false, wasLimited = true, floorKnown = true))
    }

    @Test fun limitedIsAndroid14WithPickedPhotosOnly() {
        assertTrue(PhotoSync.limited(34, allImages = false, userSelected = true))
        assertTrue(PhotoSync.limited(36, allImages = false, userSelected = true))
        assertFalse(PhotoSync.limited(34, allImages = true, userSelected = true))
        assertFalse(PhotoSync.limited(34, allImages = false, userSelected = false))
        assertFalse(PhotoSync.limited(33, allImages = false, userSelected = true))
    }
}
