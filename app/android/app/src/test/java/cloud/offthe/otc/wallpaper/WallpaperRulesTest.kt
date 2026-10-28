// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.wallpaper

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.androidWallpaperReplacesOther
import cloud.offthe.otc.i18n.androidWallpaperStart
import cloud.offthe.otc.i18n.androidWallpaperStarted
import cloud.offthe.otc.i18n.androidWallpaperTitle
import cloud.offthe.otc.i18n.appPhotosWallpaperLockNote
import cloud.offthe.otc.i18n.appPhotosWallpaperProgress
import cloud.offthe.otc.i18n.appPhotosWallpaperStoredText
import cloud.offthe.otc.i18n.appPhotosWallpaperStoredTitle
import cloud.offthe.otc.i18n.english
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.random.Random

// A collection as the phone's wallpaper: which photos are copied (at most
// 100, at random), how the copies follow the collection, the order they
// show in (no repeat until all were shown), the choices kept on the phone,
// and the crop to the screen.
class WallpaperRulesTest {
    private fun hash(i: Int) = "%064x".format(i)
    private fun photo(i: Int) = WallpaperPhoto(hash(i), "/Photos/p$i.jpg")
    private fun photos(n: Int) = (1..n).map(::photo)

    // ---- what the notice counts, and which photos ----

    @Test fun theNoticeCountsAtMostTheCap() {
        assertEquals(100, WallpaperPlan.CAP)
        assertEquals(0, WallpaperPlan.copies(0))
        assertEquals(1, WallpaperPlan.copies(1))
        assertEquals(40, WallpaperPlan.copies(40))
        assertEquals(100, WallpaperPlan.copies(100))
        assertEquals(100, WallpaperPlan.copies(5000))
        assertEquals(0, WallpaperPlan.copies(-3))
    }

    @Test fun aSmallCollectionIsCopiedWhole() {
        val p = WallpaperPlan.plan(photos(40), emptySet(), random = Random(1))
        assertEquals(photos(40).toSet(), p.add.toSet())
        assertTrue(p.keep.isEmpty() && p.remove.isEmpty())
    }

    @Test fun aBigOneGivesARandomHundredEachOnce() {
        val all = photos(1000)
        val a = WallpaperPlan.plan(all, emptySet(), random = Random(1))
        val b = WallpaperPlan.plan(all, emptySet(), random = Random(2))
        assertEquals(100, a.add.size)
        assertEquals(100, a.add.map { it.hash }.toSet().size)
        assertTrue(all.containsAll(a.add))
        // Not the first hundred, and not the same hundred each time.
        assertFalse(a.add.toSet() == all.take(100).toSet())
        assertFalse(a.add.toSet() == b.add.toSet())
    }

    @Test fun aPhotoListedTwiceIsOnePhoto() {
        val p = WallpaperPlan.plan(photos(3) + photo(2).copy(path = "/Elsewhere/p2.jpg"), emptySet(), random = Random(1))
        assertEquals(3, p.add.size)
        assertEquals(3, p.add.map { it.hash }.toSet().size)
    }

    // ---- the copies kept in step ----

    @Test fun photosAddedAreAddedAndRemovedOnesDropped() {
        val stored = (1..10).map(::hash).toSet()
        // 3 and 7 left the collection; 11 and 12 came.
        val now = photos(12).filter { it.hash != hash(3) && it.hash != hash(7) }
        val p = WallpaperPlan.plan(now, stored, random = Random(1))
        assertEquals(stored - hash(3) - hash(7), p.keep)
        assertEquals(setOf(hash(3), hash(7)), p.remove)
        assertEquals(setOf(photo(11), photo(12)), p.add.toSet())
    }

    @Test fun addedOnlyUpToTheCap() {
        val stored = (1..100).map(::hash).toSet()
        val p = WallpaperPlan.plan(photos(150), stored, random = Random(1))
        assertEquals(stored, p.keep)
        assertTrue(p.add.isEmpty())
        assertTrue(p.remove.isEmpty())
        // Five gone: five of the others take their place.
        val q = WallpaperPlan.plan(photos(150).drop(5), stored, random = Random(1))
        assertEquals(95, q.keep.size)
        assertEquals(5, q.add.size)
        assertTrue(q.add.all { it.hash !in stored })
        assertEquals((1..5).map(::hash).toSet(), q.remove)
    }

    @Test fun overALoweredCapTheExtraGo() {
        val stored = (1..10).map(::hash).toSet()
        val p = WallpaperPlan.plan(photos(10), stored, cap = 6, random = Random(1))
        assertEquals(6, p.keep.size)
        assertEquals(4, p.remove.size)
        assertEquals(stored, p.keep + p.remove)
        assertTrue(p.add.isEmpty())
    }

    @Test fun anEmptiedCollectionDropsEverything() {
        val stored = (1..4).map(::hash).toSet()
        val p = WallpaperPlan.plan(emptyList(), stored, random = Random(1))
        assertEquals(stored, p.remove)
        assertTrue(p.keep.isEmpty() && p.add.isEmpty())
    }

    // ---- the order they show in ----

    @Test fun everyCopyShowsOnceBeforeAnyAgain() {
        val all = (1..20).map(::hash)
        val rnd = Random(7)
        var shown = emptySet<String>()
        var last: String? = null
        val round = mutableListOf<String>()
        repeat(20) {
            val (pick, next) = WallpaperShuffle.next(all, shown, last, rnd)!!
            round += pick
            shown = next
            last = pick
        }
        assertEquals(all.toSet(), round.toSet())
        assertEquals(20, round.size)
        // The next round starts afresh, never with the photo on screen.
        repeat(50) {
            val (pick, next) = WallpaperShuffle.next(all, all.toSet(), last, Random(it))!!
            assertTrue(pick != last)
            assertEquals(setOf(pick), next)
        }
    }

    @Test fun copiesGoneOrAddedMeanwhile() {
        val all = listOf(hash(1), hash(2), hash(3))
        // Shown ones no longer stored are forgotten; a new copy can come next.
        val (pick, next) = WallpaperShuffle.next(all, setOf(hash(1), hash(2), hash(9)), hash(2), Random(1))!!
        assertEquals(hash(3), pick)
        assertEquals(setOf(hash(1), hash(2), hash(3)), next)
        // One copy: it, again and again.
        assertEquals(hash(5), WallpaperShuffle.next(listOf(hash(5)), setOf(hash(5)), hash(5), Random(1))!!.first)
        assertNull(WallpaperShuffle.next(emptyList(), emptySet(), null, Random(1)))
    }

    @Test fun theShownRoundAsKept() {
        val s = setOf(hash(1), hash(2))
        assertEquals(s, WallpaperShuffle.parse(WallpaperShuffle.format(s)))
        assertEquals(emptySet<String>(), WallpaperShuffle.parse(null))
        assertEquals(emptySet<String>(), WallpaperShuffle.parse(""))
        // Only what can be a copy's name.
        assertEquals(setOf(hash(1)), WallpaperShuffle.parse("${hash(1)},../x,abc"))
        assertTrue(isHash(hash(1)))
        assertFalse(isHash("../../etc"))
        assertFalse(isHash("abc"))
    }

    // ---- the choices kept ----

    @Test fun settingsKeptAndReadBack() {
        val s = WallpaperSettings("g1", "Trip “2026”", WallpaperTarget.LOCK, WallpaperInterval.DAY, "wss://pit.off-the.cloud/ws")
        assertEquals(s, WallpaperSettings.fromMap(s.toMap()))
        assertEquals(WallpaperSettings.KEYS.toSet(), s.toMap().keys)
        for (t in WallpaperTarget.entries) for (i in WallpaperInterval.entries) {
            val x = s.copy(target = t, interval = i)
            assertEquals(x, WallpaperSettings.fromMap(x.toMap()))
        }
    }

    @Test fun nothingKeptOrNotWholeIsNoWallpaper() {
        assertNull(WallpaperSettings.fromMap(emptyMap()))
        val whole = WallpaperSettings("g1", "Trip", WallpaperTarget.BOTH, WallpaperInterval.HOUR, "e").toMap()
        assertNotNull(WallpaperSettings.fromMap(whole))
        assertNull(WallpaperSettings.fromMap(whole + (WallpaperSettings.KEY_GROUP to "")))
        assertNull(WallpaperSettings.fromMap(whole + (WallpaperSettings.KEY_TARGET to "sideways")))
        assertNull(WallpaperSettings.fromMap(whole + (WallpaperSettings.KEY_INTERVAL to null)))
        assertNull(WallpaperSettings.fromMap(whole - WallpaperSettings.KEY_ENDPOINT))
    }

    @Test fun theBoxsFirstChoicesAndWhatTheyMean() {
        assertEquals(WallpaperTarget.BOTH, WallpaperTarget.DEFAULT)
        assertEquals(WallpaperInterval.HOUR, WallpaperInterval.DEFAULT)
        // WallpaperManager's FLAG_SYSTEM (1) and FLAG_LOCK (2).
        assertEquals(2, WallpaperTarget.LOCK.flags)
        assertEquals(1, WallpaperTarget.HOME.flags)
        assertEquals(3, WallpaperTarget.BOTH.flags)
        assertTrue(WallpaperTarget.LOCK.showsOnLock && WallpaperTarget.BOTH.showsOnLock)
        assertFalse(WallpaperTarget.HOME.showsOnLock)
        // WorkManager's shortest period is 15 minutes.
        assertEquals(listOf(15L, 60L, 1440L), WallpaperInterval.entries.map { it.minutes })
    }

    // ---- the crop to the screen ----

    @Test fun aLandscapePhotoOnAPortraitScreen() {
        // 4000x3000 on 1080x2400: the middle 1350x3000, scaled to the screen.
        val c = WallpaperCrop.of(4000, 3000, 1080, 2400)!!
        assertEquals(1350, c.width)
        assertEquals(3000, c.height)
        assertEquals((4000 - 1350) / 2, c.left)
        assertEquals(0, c.top)
        assertEquals(1080 to 2400, c.outW to c.outH)
    }

    @Test fun aTallPhotoOnAWideScreenAndOneThatIsSmall() {
        val c = WallpaperCrop.of(1000, 3000, 2000, 1000)!!
        assertEquals(1000 to 500, c.width to c.height)
        assertEquals(0 to 1250, c.left to c.top)
        // Never made bigger than it is.
        assertEquals(1000 to 500, c.outW to c.outH)
        assertNull(WallpaperCrop.of(0, 10, 10, 10))
    }

    @Test fun decodedSmallerOnlyWhileItStillCoversTheScreen() {
        assertEquals(1, wallpaperSample(1080, 2400, 1080, 2400))
        assertEquals(2, wallpaperSample(4000, 3000, 1080, 1440))
        // 4000/4 = 1000 < 1080: not 4.
        assertEquals(2, wallpaperSample(4000, 3000, 1080, 1400))
        assertEquals(4, wallpaperSample(8000, 6000, 1080, 1440))
        assertEquals(1, wallpaperSample(800, 600, 1080, 2400))
    }

    @Test fun englishText() {
        assertEquals("These photos will be stored on this phone", S.appPhotosWallpaperStoredTitle().english())
        assertEquals(
            "To show them as your wallpaper, copies of 40 photos from “⁨Trip⁩” are saved on this phone, sized for its screen. They stay there until you stop using the collection as wallpaper.",
            S.appPhotosWallpaperStoredText(40, "Trip").english(),
        )
        assertEquals(
            "To show it as your wallpaper, a copy of the photo in “⁨Trip⁩” is saved on this phone, sized for its screen. It stays there until you stop using the collection as wallpaper.",
            S.appPhotosWallpaperStoredText(1, "Trip").english(),
        )
        assertEquals("Anyone who sees your lock screen will see these photos.", S.appPhotosWallpaperLockNote().english())
        assertEquals("Storing photos: 3 of 40", S.appPhotosWallpaperProgress(3, 40).english())
        assertEquals("Store photos and start", S.androidWallpaperStart().english())
        assertEquals("Use “⁨Trip⁩” as wallpaper", S.androidWallpaperTitle("Trip").english())
        assertEquals("“⁨Trip⁩” is now your wallpaper.", S.androidWallpaperStarted("Trip").english())
        assertEquals("“⁨Home⁩” is your wallpaper now. This collection takes its place, and its copies are deleted.", S.androidWallpaperReplacesOther("Home").english())
    }
}
