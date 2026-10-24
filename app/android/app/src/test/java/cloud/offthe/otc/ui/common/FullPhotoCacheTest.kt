// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File
import java.util.concurrent.atomic.AtomicInteger
import kotlin.random.Random

// The viewer's full-size photos for the session: by content hash, within a
// budget (each photo at most a quarter of it), the least recently used out
// first, emptied at a launch's first use and by Clear, and whatever the
// system or several threads at once do leaving it consistent.
class FullPhotoCacheTest {
    @get:Rule val tmp = TemporaryFolder()

    private fun dir() = File(tmp.root, "full-photos")
    private fun open(budget: Long = 1_000) = FullPhotoCache(dir(), budget)
    private fun bytes(n: Int, b: Int = 1) = ByteArray(n) { b.toByte() }
    // Hex hashes as the device's are (SHA-256).
    private fun h(i: Int) = "%064x".format(i)
    private fun files(): Set<String> = dir().list().orEmpty().toSet()

    // ---- keeping and budget ----------------------------------------------------------

    @Test fun keepsBytesByHashAndCountsThem() {
        val c = open()
        assertTrue(c.put(h(1), bytes(100)))
        assertTrue(c.put(h(2), bytes(200, 2)))
        assertEquals(300, c.usedBytes)
        assertEquals(2, c.count)
        assertArrayEquals(bytes(200, 2), c.get(h(2)))
        assertNull(c.get(h(3)))
        // On disk as the device sent them, one file per hash.
        assertEquals(setOf(h(1), h(2)), files())
        assertArrayEquals(bytes(100), File(dir(), h(1)).readBytes())
        // The same hash again is the same content: nothing written, nothing added.
        assertTrue(c.put(h(1), bytes(100, 9)))
        assertArrayEquals(bytes(100), c.get(h(1)))
        assertEquals(300, c.usedBytes)
        c.remove(h(1))
        assertEquals(200, c.usedBytes)
        assertEquals(setOf(h(2)), files())
    }

    @Test fun onlyDeviceHashesAndSomethingToKeep() {
        val c = open()
        // Not 64 lowercase hex digits (an item without a hash is ""): never a file name here.
        for (bad in listOf("", "a/../b c", h(0xabcdef).uppercase(), h(7).drop(1), "../${h(7).drop(3)}")) {
            assertFalse(c.put(bad, bytes(10)))
            assertNull(c.get(bad))
        }
        assertFalse(c.put(h(1), ByteArray(0)))
        assertEquals(0, c.count)
        assertTrue(files().isEmpty())
    }

    @Test fun theLeastRecentlyUsedGoUntilTheRestFitTheBudget() {
        val c = open(budget = 1_000)
        c.put(h(1), bytes(100))
        c.put(h(2), bytes(100))
        for (i in 3..5) assertTrue(c.put(h(i), bytes(250)))
        assertEquals(950, c.usedBytes)
        // Past the budget: the least recently used go until the rest fit -
        // two small ones here - and no more: down to the budget, not below.
        c.put(h(6), bytes(250))
        assertEquals(1_000, c.usedBytes)
        assertEquals((3..6).map { h(it) }.toSet(), (1..6).map { h(it) }.filter { c.has(it) }.toSet())
        // One is room enough: one goes, the oldest.
        c.put(h(7), bytes(100))
        assertEquals(850, c.usedBytes)
        assertFalse(c.has(h(3)))
        assertTrue(c.has(h(4)))
        // Their files go with them.
        assertEquals(setOf(h(4), h(5), h(6), h(7)), files())
    }

    @Test fun aHitCountsAsAUse() {
        val c = open(budget = 1_000)
        for (i in 1..4) c.put(h(i), bytes(250))
        // 1 seen again (swiped back to): 2 is now the least recently used.
        assertTrue(c.get(h(1)) != null)
        c.put(h(5), bytes(250))
        assertTrue(c.has(h(1)))
        assertFalse(c.has(h(2)))
        // Kept again (fetched by another viewer meanwhile) counts too.
        c.put(h(3), bytes(250))
        c.put(h(6), bytes(250))
        assertTrue(c.has(h(3)))
        assertFalse(c.has(h(4)))
        // Looking without reading doesn't: 1 goes next all the same.
        c.has(h(1))
        c.put(h(7), bytes(250))
        assertFalse(c.has(h(1)))
        assertTrue(c.has(h(5)))
    }

    @Test fun onePhotoTakesAtMostAQuarterOfTheBudget() {
        val c = open(budget = 1_000)
        for (i in 1..5) c.put(h(i), bytes(150))
        // A quarter is kept; one byte over is shown, not kept - and pushes nothing out.
        assertTrue(c.put(h(6), bytes(250)))
        assertFalse(c.put(h(7), bytes(251)))
        assertFalse(c.has(h(7)))
        assertEquals(1_000, c.usedBytes)
        assertEquals((1..6).map { h(it) }.toSet(), files())
        // A smaller budget (a smaller size picked): what is over its quarter
        // goes, then the least recently used until the rest fit.
        c.setBudget(600)
        assertFalse(c.has(h(6)))
        assertFalse(c.has(h(1)))
        assertEquals(600, c.usedBytes)
        assertEquals((2..5).map { h(it) }.toSet(), files())
        assertFalse(c.put(h(8), bytes(151)))
        assertTrue(c.put(h(8), bytes(150)))
        assertEquals(600, c.usedBytes)
        assertFalse(c.has(h(2)))
    }

    // ---- the session --------------------------------------------------------------

    @Test fun clearTakesEverythingAndKeepsAgainAfter() {
        val c = open()
        c.put(h(1), bytes(100))
        c.put(h(2), bytes(100))
        c.clear()
        assertEquals(0, c.usedBytes)
        assertEquals(0, c.count)
        assertNull(c.get(h(1)))
        assertTrue(files().isEmpty())
        // What the viewer fetches next is kept as before.
        assertTrue(c.put(h(3), bytes(100)))
        assertArrayEquals(bytes(100), c.get(h(3)))
        assertEquals(setOf(h(3)), files())
    }

    @Test fun aLaunchStartsWithoutTheLastOnesPhotos() {
        val last = open()
        last.put(h(1), bytes(100))
        last.put(h(2), bytes(100))
        File(dir(), "${h(3)}.17.tmp").writeBytes(bytes(5)) // a store a killed process left half done
        // The next launch: everything the last one left goes at the first
        // use, not read back - even a photo under the same hash.
        val next = open()
        assertEquals(0, next.count)
        assertNull(next.get(h(1)))
        assertTrue(files().isEmpty())
        assertTrue(next.put(h(1), bytes(50, 2)))
        assertArrayEquals(bytes(50, 2), next.get(h(1)))
        assertEquals(setOf(h(1)), files())
        // start() itself (what ThumbStoreCore runs at launch, in the
        // background) does the same, once: later calls leave what this
        // launch kept.
        val third = open()
        third.start()
        assertTrue(files().isEmpty())
        third.put(h(4), bytes(10))
        third.start()
        assertEquals(setOf(h(4)), files())
        // A first store empties first too.
        val fourth = open()
        assertTrue(fourth.put(h(5), bytes(10)))
        assertEquals(setOf(h(5)), files())
    }

    @Test fun aFileGoneOrCutShortIsAMiss() {
        val c = open()
        c.put(h(1), bytes(100))
        c.put(h(2), bytes(100))
        c.put(h(3), bytes(100))
        // The system clearing cache space while the app runs.
        File(dir(), h(1)).delete()
        File(dir(), h(2)).writeBytes(bytes(7))
        assertNull(c.get(h(1)))
        assertNull(c.get(h(2)))
        assertEquals(100, c.usedBytes)
        assertEquals(1, c.count)
        assertEquals(setOf(h(3)), files())
        // The whole folder gone: a store makes it again.
        dir().deleteRecursively()
        assertNull(c.get(h(3)))
        assertTrue(c.put(h(4), bytes(10)))
        assertArrayEquals(bytes(10), c.get(h(4)))
    }

    // ---- concurrency ----------------------------------------------------------------

    @Test fun manyThreadsAtOnceLeaveItConsistent() {
        val c = open(budget = 40_000)
        val hashes = (0 until 200).map { h(it) }
        // Each hash's content: the same bytes whoever keeps it.
        fun fill(i: Int) = 1 + i % 100
        val wrong = AtomicInteger()
        val threads = (0 until 12).map { t ->
            Thread {
                val r = Random(t)
                repeat(1_500) {
                    val i = r.nextInt(hashes.size)
                    when (r.nextInt(20)) {
                        in 0..8 -> c.put(hashes[i], bytes(100 + r.nextInt(9_000), fill(i)))
                        in 9..16 -> c.get(hashes[i])?.let { got -> if (got.any { it != fill(i).toByte() }) wrong.incrementAndGet() }
                        17 -> c.remove(hashes[i])
                        18 -> if (r.nextInt(20) == 0) c.clear()
                        else -> if (r.nextInt(10) == 0) c.setBudget(20_000L + r.nextInt(30_000))
                    }
                }
            }
        }
        threads.forEach { it.start() }
        threads.forEach { it.join() }
        // A hit is always one photo's bytes whole.
        assertEquals(0, wrong.get())
        // What the index says is what is on disk: the sizes add up, no file
        // (nor a temporary one) is left over, and the budget holds.
        val kept = hashes.filter { c.has(it) }.toSet()
        assertEquals(kept.size, c.count)
        val onDisk = dir().listFiles().orEmpty().associate { it.name to it.length() }
        assertEquals(kept, onDisk.keys)
        assertEquals(onDisk.values.sum(), c.usedBytes)
        assertTrue(c.usedBytes <= c.budgetBytes)
        for (k in kept) assertEquals(onDisk.getValue(k).toInt(), c.get(k)!!.size)
    }
}
