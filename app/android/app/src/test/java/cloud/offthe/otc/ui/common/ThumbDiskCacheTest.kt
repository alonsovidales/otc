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
import java.util.concurrent.Executor
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import kotlin.random.Random

// The phones' persistent thumbnail cache: one entry per hash with its kind,
// the bytes accounted and the least recently used evicted past the limit
// (down to 90% of it), kept across a restart (the journal), and whatever a
// killed process, a full disk or the system leaves behind tolerated.
class ThumbDiskCacheTest {
    @get:Rule val tmp = TemporaryFolder()

    private fun dir() = File(tmp.root, "owner")
    private fun open(limit: Long = 1_000, version: Int = 1, background: Executor = DIRECT, nowMs: () -> Long = System::currentTimeMillis) =
        ThumbDiskCache(dir(), limit, version, background, nowMs)
    private fun bytes(n: Int, b: Int = 1) = ByteArray(n) { b.toByte() }
    // Hex hashes as the device's are (SHA-256).
    private fun h(i: Int) = "%064x".format(i)

    // ---- accounting and eviction ---------------------------------------------------

    @Test fun keepsBytesAndKindsAndCountsThem() {
        val c = open()
        assertEquals(ThumbKind.SMALL, c.put(h(1), bytes(100), ThumbKind.SMALL))
        assertEquals(ThumbKind.BIG, c.put(h(2), bytes(250, 2), ThumbKind.BIG))
        assertEquals(ThumbKind.UNKNOWN, c.put(h(3), bytes(50, 3), ThumbKind.UNKNOWN))
        assertEquals(400, c.usedBytes)
        assertEquals(3, c.count)
        val (b, k) = c.get(h(2))!!
        assertArrayEquals(bytes(250, 2), b)
        assertEquals(ThumbKind.BIG, k)
        assertEquals(mapOf(h(1) to ThumbKind.SMALL, h(3) to ThumbKind.UNKNOWN), c.kinds(listOf(h(1), h(3), h(9))))
        assertNull(c.get(h(9)))
        c.remove(h(1))
        assertEquals(300, c.usedBytes)
        assertNull(c.kindOf(h(1)))
    }

    @Test fun theLeastRecentlyUsedGoFirstDownTo90PercentOfTheLimit() {
        val c = open(limit = 1_000)
        for (i in 1..10) c.put(h(i), bytes(100), ThumbKind.SMALL)
        assertEquals(1_000, c.usedBytes)
        // 1 used: 2 and 3 are now the least recently used.
        assertTrue(c.get(h(1)) != null)
        // Past the limit: down to 900 in one pass - two go, not one.
        c.put(h(11), bytes(100), ThumbKind.SMALL)
        assertEquals(900, c.usedBytes)
        assertNull(c.kindOf(h(2)))
        assertNull(c.kindOf(h(3)))
        assertEquals(ThumbKind.SMALL, c.kindOf(h(1)))
        // Looking a kind up doesn't count as a use: 4 goes next.
        c.kindOf(h(4))
        c.put(h(12), bytes(100), ThumbKind.SMALL)
        c.put(h(13), bytes(100), ThumbKind.SMALL)
        assertNull(c.kindOf(h(4)))
        // Their files go with them (in the background: here at once).
        assertEquals(c.count, dataFiles().size)
    }

    @Test fun aSmallerLimitEvictsAtOnce() {
        val c = open(limit = 10_000)
        for (i in 1..10) c.put(h(i), bytes(100), ThumbKind.SMALL)
        c.setLimit(350)
        // Down to 315: three left.
        assertEquals(300, c.usedBytes)
        assertEquals(setOf(h(8), h(9), h(10)), c.kinds((1..10).map { h(it) }).keys)
        assertEquals(3, dataFiles().size)
        // Kept across a restart under the new limit.
        c.close()
        assertEquals(300, open(limit = 350).usedBytes)
    }

    @Test fun manyEntriesStayQuick() {
        // ~30k entries (1 GB of 38 KB tiles): kept, reopened and evicted
        // without stat'ing each one.
        val n = 30_000
        val c = open(limit = n * 10L)
        val t0 = System.nanoTime()
        for (i in 0 until n) c.put(h(i), bytes(10), ThumbKind.SMALL)
        c.close()
        val reopened = open(limit = n * 10L)
        assertEquals(n, reopened.count)
        assertEquals(n * 10L, reopened.usedBytes)
        reopened.setLimit(n * 5L)
        // Down to 90% of the new limit.
        assertEquals(n * 5 / 10 * 9 / 10, reopened.count)
        assertNull(reopened.kindOf(h(0)))
        assertEquals(ThumbKind.SMALL, reopened.kindOf(h(n - 1)))
        val secs = (System.nanoTime() - t0) / 1e9
        assertTrue("took $secs s", secs < 60)
    }

    // ---- kinds and replacement ------------------------------------------------------

    @Test fun aSmallOneReplacesABigOrUnknownOneNeverTheOtherWay() {
        val c = open()
        c.put(h(1), bytes(200, 1), ThumbKind.BIG)
        assertEquals(ThumbKind.SMALL, c.put(h(1), bytes(50, 2), ThumbKind.SMALL))
        assertEquals(ThumbKind.SMALL, c.get(h(1))!!.second)
        assertArrayEquals(bytes(50, 2), c.get(h(1))!!.first)
        assertEquals(50, c.usedBytes)
        // A big one (or one that doesn't say) after it: the small one stays.
        assertEquals(ThumbKind.SMALL, c.put(h(1), bytes(200, 3), ThumbKind.BIG))
        assertEquals(ThumbKind.SMALL, c.put(h(1), bytes(200, 3), ThumbKind.UNKNOWN))
        assertArrayEquals(bytes(50, 2), c.get(h(1))!!.first)
        assertEquals(1, c.count)
        assertEquals(50, c.usedBytes)
        // A big one (the device said so) replaces one that didn't say; not
        // the other way round; the same kind again is the same picture.
        c.put(h(2), bytes(70), ThumbKind.UNKNOWN)
        assertEquals(ThumbKind.BIG, c.put(h(2), bytes(80), ThumbKind.BIG))
        assertEquals(ThumbKind.BIG, c.put(h(2), bytes(60), ThumbKind.UNKNOWN))
        assertEquals(ThumbKind.BIG, c.put(h(2), bytes(90), ThumbKind.BIG))
        assertEquals(80, c.get(h(2))!!.first.size)
        assertEquals(130, c.usedBytes)
        // The whole table.
        val k = ThumbKind.entries
        val replacing = k.flatMap { new -> k.map { old -> Triple(new, old, ThumbDiskCache.replaces(new, old)) } }.filter { it.third }.map { it.first to it.second }
        assertEquals(setOf(ThumbKind.SMALL to ThumbKind.BIG, ThumbKind.SMALL to ThumbKind.UNKNOWN, ThumbKind.BIG to ThumbKind.UNKNOWN), replacing.toSet())
    }

    @Test fun onlyDeviceHashesAndEntriesUpTo8MBAreKept() {
        val c = open(limit = 100L shl 20)
        // Not 64 lowercase hex digits: never a file name here.
        for (bad in listOf("a/../b c", "h-photo", h(0xabcdef).uppercase(), h(1).drop(1), "")) {
            assertNull(c.put(bad, bytes(10), ThumbKind.SMALL))
            assertNull(c.get(bad))
        }
        assertEquals(0, c.count)
        // Over 8 MB: shown, not kept; 8 MB exactly is kept.
        assertNull(c.put(h(1), bytes(ThumbDiskCache.MAX_ENTRY_BYTES + 1), ThumbKind.BIG))
        assertEquals(ThumbKind.BIG, c.put(h(2), bytes(ThumbDiskCache.MAX_ENTRY_BYTES), ThumbKind.BIG))
        assertTrue(ThumbDiskCache.isHash(h(3)))
        assertFalse(ThumbDiskCache.isHash(h(0xabc).uppercase()))
    }

    @Test fun stillBigIsKeptAcrossARestartUntilTheEntryIsReplaced() {
        var now = 1_000_000_000_000L
        val c = open(nowMs = { now })
        c.put(h(1), bytes(10), ThumbKind.BIG)
        c.noteStillBig(h(1))
        assertEquals(setOf(h(1)), c.stillBig(listOf(h(1), h(2)), withinMs = 60_000))
        c.close()
        val again = open(nowMs = { now })
        assertEquals(setOf(h(1)), again.stillBig(listOf(h(1)), withinMs = 60_000))
        // Older than asked for: not settled any more.
        now += 120_000
        assertTrue(again.stillBig(listOf(h(1)), withinMs = 60_000).isEmpty())
        now -= 120_000
        // A small one replaces it, and the mark with it.
        again.put(h(1), bytes(5), ThumbKind.SMALL)
        assertTrue(again.stillBig(listOf(h(1)), withinMs = 60_000).isEmpty())
    }

    // ---- across a restart -----------------------------------------------------------

    @Test fun keptAcrossARestartInTheSameOrder() {
        val c = open(limit = 1_000)
        for (i in 1..10) c.put(h(i), bytes(100, i), ThumbKind.entries[i % 3])
        c.get(h(1))
        c.get(h(2))
        // A process killed without closing: what was flushed is what counts.
        c.flush()
        val again = open(limit = 1_000)
        assertEquals(10, again.count)
        assertEquals(1_000, again.usedBytes)
        assertEquals(ThumbKind.entries[5 % 3], again.kindOf(h(5)))
        assertArrayEquals(bytes(100, 7), again.get(h(7))!!.first)
        // Used last before the restart: 1 and 2, then 7 now - so 3 and 4 go first.
        again.put(h(11), bytes(100), ThumbKind.SMALL)
        assertNull(again.kindOf(h(3)))
        assertNull(again.kindOf(h(4)))
        assertEquals(ThumbKind.entries[1 % 3], again.kindOf(h(1)))
    }

    @Test fun usesAreWrittenOutEverySoManyEvenWithoutAWrite() {
        // A session that only reads: its uses reach the journal every
        // FLUSH_EVERY_LINES (and every FLUSH_EVERY_MS), not only with a write.
        val c = open(limit = 10_000)
        for (i in 1..10) c.put(h(i), bytes(10), ThumbKind.SMALL)
        repeat(ThumbDiskCache.FLUSH_EVERY_LINES) { c.get(h(1)) }
        // Killed now (no close, no flush): a fresh open sees 1 as the most
        // recently used - over a smaller limit, 2 goes and 1 stays.
        val again = open(limit = 50)
        assertEquals(ThumbKind.SMALL, again.kindOf(h(1)))
        assertNull(again.kindOf(h(2)))
    }

    @Test fun anotherVersionStartsOver() {
        val c = open()
        c.put(h(1), bytes(100), ThumbKind.SMALL)
        c.close()
        File(dir(), "keep-me").writeText("")
        val v2 = open(version = 2)
        assertEquals(0, v2.count)
        assertEquals(0, v2.usedBytes)
        assertTrue(dataFiles().isEmpty())
        // What isn't the cache's stays; nothing set aside is left over.
        assertTrue(File(dir(), "keep-me").exists())
        assertTrue(tmp.root.listFiles()!!.none { it.name.contains("trash") })
    }

    @Test fun clearTakesEverythingAndStaysEmptyAfterARestart() {
        val c = open()
        c.put(h(1), bytes(100), ThumbKind.SMALL)
        c.put(h(2), bytes(100), ThumbKind.SMALL)
        c.clear()
        assertEquals(0, c.usedBytes)
        assertNull(c.get(h(1)))
        assertTrue(dataFiles().isEmpty())
        // Usable at once.
        c.put(h(3), bytes(10), ThumbKind.SMALL)
        c.close()
        val again = open()
        assertEquals(1, again.count)
        assertEquals(ThumbKind.SMALL, again.kindOf(h(3)))
        assertTrue(tmp.root.listFiles()!!.none { it.name.contains("trash") })
    }

    @Test fun manyUsesAreWrittenOverIntoAShortJournalInTheBackground() {
        val ran = AtomicInteger()
        val counting = Executor { ran.incrementAndGet(); it.run() }
        val c = open(limit = 10_000, background = counting)
        for (i in 1..10) c.put(h(i), bytes(10), ThumbKind.SMALL)
        repeat(5_000) { c.get(h(1 + it % 10)) }
        c.flush()
        val lines = File(dir(), "journal").readLines()
        assertTrue("journal of ${lines.size} lines", lines.size < 2_500)
        assertTrue(ran.get() >= 2)
        c.close()
        assertEquals(10, open(limit = 10_000).count)
    }

    @Test fun aRewriteThatFailsIsNotTriedOnEveryUse() {
        val ran = AtomicInteger()
        val counting = Executor { ran.incrementAndGet(); it.run() }
        val c = open(limit = 10_000, background = counting)
        for (i in 1..10) c.put(h(i), bytes(10), ThumbKind.SMALL)
        // The folder can't take the rewrite's file (a full disk, as far as it can tell).
        val d = dir()
        assertTrue(d.setWritable(false))
        try {
            repeat(2_500) { c.get(h(1 + it % 10)) }
            val afterFirst = ran.get()
            assertTrue(afterFirst >= 1)
            repeat(1_000) { c.get(h(1 + it % 10)) }
            // Not again until the journal has doubled.
            assertEquals(afterFirst, ran.get())
        } finally {
            d.setWritable(true)
        }
        // Still whole: every entry, after a restart.
        c.flush()
        assertEquals(10, open(limit = 10_000).count)
    }

    // ---- what a crash, a power cut or the system leaves ------------------------------

    @Test fun aJournalCutShortOrGarbledLosesOnlyWhatItCantRead() {
        val c = open()
        c.put(h(1), bytes(100), ThumbKind.SMALL)
        c.put(h(2), bytes(100), ThumbKind.BIG)
        c.close()
        val j = File(dir(), "journal")
        // A line of nonsense, and a last line cut short by a kill.
        j.appendText("X what\nP ${h(1)} Q 12\nP ${h(3)} S 1")
        File(File(dir(), h(3).take(2)), h(3)).writeBytes(bytes(100))
        val again = open()
        assertEquals(2, again.count)
        assertEquals(200, again.usedBytes)
        assertEquals(ThumbKind.SMALL, again.kindOf(h(1)))
        // The entry whose line was cut is an orphan: its file goes.
        assertNull(again.kindOf(h(3)))
        assertFalse(File(File(dir(), h(3).take(2)), h(3)).exists())
        // Appending goes on cleanly after the cut line.
        again.put(h(4), bytes(10), ThumbKind.SMALL)
        again.close()
        assertEquals(3, open().count)
    }

    @Test fun filesGoneOrNotWholeAreDropped() {
        val c = open()
        c.put(h(1), bytes(100), ThumbKind.SMALL)
        c.put(h(2), bytes(100), ThumbKind.SMALL)
        c.put(h(3), bytes(100), ThumbKind.SMALL)
        // The system clearing cache space, a rename not on disk at a power cut.
        File(File(dir(), h(1).take(2)), h(1)).delete()
        File(File(dir(), h(2).take(2)), h(2)).writeBytes(bytes(7))
        assertNull(c.get(h(1)))
        assertNull(c.get(h(2)))
        assertEquals(100, c.usedBytes)
        assertEquals(1, c.count)
        c.close()
        // Gone while the app was away: dropped at open, without a read.
        File(File(dir(), h(3).take(2)), h(3)).delete()
        val again = open()
        assertEquals(0, again.count)
        assertEquals(0, again.usedBytes)
    }

    @Test fun leftoversAreDeletedAtOpen() {
        val c = open()
        c.put(h(1), bytes(100), ThumbKind.SMALL)
        c.close()
        val sub = File(dir(), h(1).take(2))
        File(sub, "${h(1)}.17.tmp").writeBytes(bytes(5))
        File(sub, h(9)).writeBytes(bytes(5)) // written, its journal line never was
        File(sub, "not-ours").writeBytes(bytes(5))
        val again = open()
        assertEquals(1, again.count)
        assertEquals(listOf(h(1)), sub.list()!!.toList())
    }

    // ---- concurrency ----------------------------------------------------------------

    @Test fun manyThreadsAtOnceLeaveItConsistent() {
        val bg = Executors.newSingleThreadExecutor()
        try {
            val c = open(limit = 40_000, background = bg)
            val hashes = (0 until 300).map { h(it) }
            val threads = (0 until 12).map { t ->
                Thread {
                    val r = Random(t)
                    repeat(2_000) {
                        val hash = hashes[r.nextInt(hashes.size)]
                        when (r.nextInt(10)) {
                            in 0..3 -> c.put(hash, bytes(50 + r.nextInt(400), 1 + r.nextInt(100)), ThumbKind.entries[r.nextInt(3)])
                            in 4..7 -> c.get(hash)
                            8 -> c.remove(hash)
                            else -> if (r.nextInt(50) == 0) c.setLimit(20_000L + r.nextInt(30_000)) else c.kinds(hashes.take(20))
                        }
                    }
                }
            }
            threads.forEach { it.start() }
            threads.forEach { it.join() }
            // The background deletes done.
            bg.submit {}.get(30, TimeUnit.SECONDS)
            bg.submit {}.get(30, TimeUnit.SECONDS)
            val kept = c.kinds(hashes).keys
            assertEquals(kept.size, c.count)
            // What the index says is what is on disk: the sizes add up, and no file is left over.
            val onDisk = dataFiles().associate { it.name to it.length() }
            assertEquals(kept, onDisk.keys)
            assertEquals(onDisk.values.sum(), c.usedBytes)
            for (k in kept) assertEquals(onDisk.getValue(k).toInt(), c.get(k)!!.first.size)
            // Reopened under the limit the threads left (it moved under them).
            val limit = c.limitBytes
            c.close()
            val again = open(limit = limit, background = bg)
            assertEquals(kept, again.kinds(hashes).keys)
            assertEquals(onDisk.values.sum(), again.usedBytes)
        } finally {
            bg.shutdownNow()
        }
    }

    private fun dataFiles(): List<File> =
        dir().listFiles().orEmpty().filter { it.isDirectory && it.name.length == 2 }.flatMap { it.listFiles().orEmpty().toList() }
}
