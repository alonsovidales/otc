// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui

import cloud.offthe.otc.proto.RaidState
import cloud.offthe.otc.proto.Status
import cloud.offthe.otc.proto.StatusErrors
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Locale

// The wide layout's menu, as the web's App.tsx and Sidebar.tsx lay it out.
class SidebarTest {
    @Test fun breakpointsAreTheWebs() {
        assertEquals(MenuLayout.NONE, menuLayout(411f, menuOpen = true))
        assertEquals(MenuLayout.NONE, menuLayout(599.9f, menuOpen = true))
        assertEquals(MenuLayout.RAIL, menuLayout(600f, menuOpen = true))
        // 600-1023: always the rail, whatever was chosen on a wider window.
        assertEquals(MenuLayout.RAIL, menuLayout(1023f, menuOpen = true))
        assertEquals(MenuLayout.FULL, menuLayout(1024f, menuOpen = true))
        assertEquals(MenuLayout.RAIL, menuLayout(1366f, menuOpen = false))
    }

    @Test fun groupsAndPeopleOnlyWithFaceRecognition() {
        val on = menuGroups(faces = true)
        assertEquals(listOf(null, "Sharing", "Device"), on.map { it.heading })
        assertEquals(
            listOf(Section.Images, Section.People, Section.Collections, Section.Files, Section.Social, Section.Friends, Section.Alerts, Section.Settings),
            on.flatMap { it.items },
        )
        val off = menuGroups(faces = false).flatMap { it.items }
        assertFalse(Section.People in off)
        assertEquals(7, off.size)
    }

    @Test fun narrowTabsShowTheMenusOwnSections() {
        assertEquals(Section.Images, Section.People.tab)
        assertEquals(Section.Images, Section.Collections.tab)
        assertEquals(Section.Social, Section.Friends.tab)
        for (s in listOf(Section.Alerts, Section.Social, Section.Files, Section.Images, Section.Settings)) assertEquals(s, s.tab)
        // The ids the bottom bar always saved still mean the same sections.
        assertEquals(Section.Alerts, Section.of(0))
        assertEquals(Section.Social, Section.of(1))
        assertEquals(Section.Files, Section.of(3))
        assertEquals(Section.Images, Section.of(4))
        assertEquals(Section.Settings, Section.of(5))
        assertEquals(Section.Social, Section.of(42))
        assertEquals(Section.entries.size, Section.entries.map { it.id }.toSet().size)
    }

    @Test fun anOpenCollectionIsStillCollections() {
        assertEquals(Section.Collections, menuCurrent(Section.Images, collectionOpen = true))
        assertEquals(Section.Images, menuCurrent(Section.Images, collectionOpen = false))
        assertEquals(Section.Files, menuCurrent(Section.Files, collectionOpen = true))
    }

    private fun status(used: Int, size: Int, raid: RaidState = RaidState.RaidInSync, errors: Int = 0) =
        Status.newBuilder().setRaidUsage(used).setRaidSize(size).setRaidState(raid).setRaidSyncPercent(45.678f)
            .apply { repeat(errors) { addErrors(StatusErrors.newBuilder().setMessage("disk")) } }.build()

    @Test fun storageSummary() {
        val was = Locale.getDefault()
        Locale.setDefault(Locale.US)
        try {
            val s = summarizeStorage(status(2_800, 474_000), null)
            assertEquals(0.59, s.usedPct, 0.0001)
            assertEquals(StorageLevel.OK, s.level)
            assertEquals("2.8 GB of 474 GB used", s.usedText)
            assertEquals("Mirror in sync", s.mirrorText)
            assertFalse(s.attention)

            assertEquals(StorageLevel.WARN, summarizeStorage(status(70, 100), null).level)
            assertEquals(StorageLevel.CRIT, summarizeStorage(status(90, 100), null).level)
            assertEquals(100.0, summarizeStorage(status(150, 100), null).usedPct, 0.0)

            val bad = summarizeStorage(status(10, 100, RaidState.RaidDegraded), null)
            assertTrue(bad.degraded)
            assertTrue(bad.attention)
            assertEquals("Mirror degraded", bad.mirrorText)
            assertTrue(summarizeStorage(status(10, 100, errors = 1), null).attention)
            assertEquals("Mirror syncing (45.68%)", summarizeStorage(status(10, 100, RaidState.RaidSyncing), null).mirrorText)

            assertEquals("Reading…", summarizeStorage(null, null).usedText)
            assertEquals("Storage unavailable right now", summarizeStorage(null, "offline").usedText)
            assertEquals("", summarizeStorage(null, null).mirrorText)
            assertEquals(0.0, summarizeStorage(status(0, 0), null).usedPct, 0.0)
        } finally {
            Locale.setDefault(was)
        }
    }

    @Test fun percentagesAsTheWebWritesThem() {
        assertEquals("45", pctText(45.0))
        assertEquals("45.5", pctText(45.5))
        assertEquals("45.68", pctText(45.678))
        assertEquals("0.3", pctText(0.3))
        assertEquals("100", pctText(123.0))
    }
}
