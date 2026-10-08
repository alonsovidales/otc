// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.GetThumbnails
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.RespEnvelope
import com.google.protobuf.ByteString
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.IOException
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

// The grids' tiles over the thumbnail cache: what each entry of a page gets
// (shown from the page, from the cache, from the cache and fetched again,
// or fetched), what a GetThumbnails answer means (its own hash, the kind it
// says, ask_again_from, a path left out - never "none" from a device before
// release 113 - content that doesn't decode, a re-ask that comes big again),
// and how the fetcher sends its batches.
class GridThumbsTest {
    @get:Rule val tmp = TemporaryFolder()

    // One thread, as the grids' main thread is.
    private val executor = Executors.newSingleThreadExecutor()
    private val dispatcher = executor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)

    @After fun tearDown() {
        scope.cancel()
        executor.shutdownNow()
    }

    private fun <T> on(block: suspend () -> T): T = runBlocking(dispatcher) { block() }

    private fun await(what: String, cond: () -> Boolean) {
        val until = System.nanoTime() + 5_000_000_000L
        while (!cond()) {
            if (System.nanoTime() > until) throw AssertionError("timed out waiting for $what")
            Thread.sleep(2)
        }
    }

    private fun settle() { repeat(5) { on { kotlinx.coroutines.yield() }; Thread.sleep(10) } }

    private fun store(dir: java.io.File = tmp.newFolder(), omits: Boolean = false) = TestThumbStore(dir, omits)

    /** A tile key for [name]'s hash. */
    private fun k(name: String, kind: Char) = "${hx(name)}#$kind"

    /** A page's row (no content: omit_thumbnails) or an answer's entry (with [content]); [says]: thumbnail_small, null unset. */
    private fun entry(path: String, hash: String = hx(path), says: Boolean? = null, content: String? = null): File {
        val b = File.newBuilder().setPath(path).setHash(hash).setMime("image/jpeg")
        if (says != null) b.thumbnailSmall = says
        if (content != null) b.content = ByteString.copyFromUtf8(content)
        return b.build()
    }

    private fun zeros(path: String, says: Boolean? = null): File {
        val b = File.newBuilder().setPath(path).setHash(hx(path)).setMime("image/jpeg").setContent(ByteString.copyFrom(ByteArray(64)))
        if (says != null) b.thumbnailSmall = says
        return b.build()
    }

    private fun lof(files: List<File>, askAgainFrom: Int = 0) = ListOfFiles.newBuilder().addAllFiles(files).setAskAgainFrom(askAgainFrom).build()
    private fun answer(files: List<File>, askAgainFrom: Int = 0) = RespEnvelope.newBuilder().setRespListOfFiles(lof(files, askAgainFrom)).build()

    private fun TestThumbStore.landNow(files: List<File>) = on { land(files, scope()) }
    private fun TestThumbStore.answerNow(asked: List<String>, l: ListOfFiles, known113: Boolean, reasked: Set<String> = emptySet()) =
        on { takeAnswer(asked, l, known113, scope(), reasked) }

    // ---- what each entry gets -------------------------------------------------------

    @Test fun whatEachEntryGets() {
        val k = ThumbKind.entries.toList() + listOf(null)
        // Carried by the page (a device before 113 ignores omit_thumbnails): shown and kept.
        for (cached in k) for (says in listOf(true, false, null)) assertEquals(TileAction.SHOW_CONTENT, tileAction(true, says, cached))
        // Not kept: fetched.
        for (says in listOf(true, false, null)) assertEquals(TileAction.FETCH, tileAction(false, says, null))
        // A small one kept is all a tile needs.
        for (says in listOf(true, false, null)) assertEquals(TileAction.USE_CACHED, tileAction(false, says, ThumbKind.SMALL))
        // A big or unknown one: fetched again once the device has the small one.
        for (cached in listOf(ThumbKind.BIG, ThumbKind.UNKNOWN)) {
            assertEquals(TileAction.USE_CACHED_AND_REFETCH, tileAction(false, true, cached))
            assertEquals(TileAction.USE_CACHED, tileAction(false, false, cached))
            assertEquals(TileAction.USE_CACHED, tileAction(false, null, cached))
        }
    }

    @Test fun aPageAgainstTheCache() {
        val s = store()
        on {
            s.put(listOf(
                ThumbPut(hx("x"), "x".toByteArray(), ThumbKind.SMALL), ThumbPut(hx("y"), "y".toByteArray(), ThumbKind.BIG),
                ThumbPut(hx("z"), "z".toByteArray(), ThumbKind.UNKNOWN), ThumbPut(hx("w"), "w".toByteArray(), ThumbKind.BIG),
            ), s.scope())
        }
        val page = listOf(
            entry("x", says = true), entry("y", says = true), entry("z", says = false), entry("w"), entry("m", says = true),
        )
        val landed = s.landNow(page)
        assertEquals(mapOf("x" to k("x", 'S'), "y" to k("y", 'B'), "z" to k("z", 'U'), "w" to k("w", 'B'), "m" to null), landed.keys)
        // The stale big one and the miss, in the page's order; the stale one is a re-ask.
        assertEquals(listOf("y", "m"), landed.fetch)
        assertEquals(setOf("y"), landed.refetch)
        // No row had content: the device leaves thumbnails out.
        assertTrue(on { s.omitsThumbnails() })
        // The same page again (this launch): the stale one isn't asked for twice.
        assertEquals(listOf("m"), s.landNow(page).fetch)
    }

    @Test fun aPageWithContentIsShownAndKeptAndSaysTheDeviceSendsThem() {
        val s = store(omits = true)
        val landed = s.landNow(listOf(
            entry("a", content = "A"),                     // a device before 113: content, says nothing
            entry("b", says = true, content = "B"),
            entry("c", says = false, content = "C"),
            zeros("d"),                                    // doesn't decode: not kept, its tile asks
        ))
        assertEquals(mapOf("a" to k("a", 'U'), "b" to k("b", 'S'), "c" to k("c", 'B'), "d" to null), landed.keys)
        assertTrue(landed.fetch.isEmpty())
        assertEquals(ThumbKind.UNKNOWN, s.cache.kindOf(hx("a")))
        assertEquals(ThumbKind.SMALL, s.cache.kindOf(hx("b")))
        assertEquals(ThumbKind.BIG, s.cache.kindOf(hx("c")))
        assertNull(s.cache.kindOf(hx("d")))
        assertEquals("B", String(on { s.load(k("b", 'S')) }!!))
        // A row had content: the marker goes back to "sends them" (12 first).
        assertFalse(on { s.omitsThumbnails() })
    }

    @Test fun rowsWithoutContentSayTheDeviceLeavesThumbnailsOutAndItIsKeptForTheNextLaunch() {
        val dir = tmp.newFolder()
        val s = store(dir)
        assertFalse(on { s.omitsThumbnails() })
        s.landNow(listOf(entry("a")))
        assertTrue(on { s.omitsThumbnails() })
        // The next launch (and after the system cleared the cache's folder).
        java.io.File(dir, "cache").deleteRecursively()
        assertTrue(on { store(dir).omitsThumbnails() })
        // An empty page says nothing.
        val again = store(dir)
        again.landNow(emptyList())
        assertTrue(on { again.omitsThumbnails() })
        assertEquals("tiles", TILES_KIND)
    }

    // The device's row says a small one is stored; GetThumbnails, whose word
    // counts, sends the big one again (the small file is unreadable): it is
    // kept, and not asked for again - this launch nor the next ones, for a week.
    @Test fun aStaleBigOneIsAskedAgainOnceAndABigAnswerIsKeptAndRemembered() {
        val dir = tmp.newFolder()
        var now = 1_000_000_000_000L
        val s = TestThumbStore(dir, nowMs = { now })
        on { s.put(listOf(ThumbPut(hx("y"), "old".toByteArray(), ThumbKind.UNKNOWN)), s.scope()) }
        val first = s.landNow(listOf(entry("y", says = true)))
        assertEquals(k("y", 'U'), first.keys["y"])
        assertEquals(listOf("y"), first.fetch)
        val got = s.answerNow(listOf("y"), lof(listOf(entry("y", says = false, content = "big"))), known113 = true, reasked = first.refetch)
        // A big one (the device said so) replaces one that didn't say.
        assertEquals(mapOf("y" to k("y", 'B')), got.landed)
        assertEquals(ThumbKind.BIG, s.cache.kindOf(hx("y")))
        assertEquals("big", String(on { s.load(k("y", 'B')) }!!))
        s.cache.close()
        // The next launch: the row still says true - shown from the cache, not asked again.
        val next = TestThumbStore(dir, nowMs = { now })
        val again = next.landNow(listOf(entry("y", says = true)))
        assertEquals(k("y", 'B'), again.keys["y"])
        assertTrue(again.fetch.isEmpty())
        next.cache.close()
        // A week later it is asked once more.
        now += ThumbStoreCore.REASK_AFTER_MS + 1
        val later = TestThumbStore(dir, nowMs = { now })
        assertEquals(listOf("y"), later.landNow(listOf(entry("y", says = true))).fetch)
        // A small one that comes after all replaces it.
        later.answerNow(listOf("y"), lof(listOf(entry("y", says = true, content = "small"))), known113 = true, reasked = setOf("y"))
        assertEquals(ThumbKind.SMALL, later.cache.kindOf(hx("y")))
    }

    // ---- what an answer means -------------------------------------------------------

    @Test fun anAnswerIsKeptUnderItsOwnHash() {
        val s = store()
        // The path holds other content than when it was listed.
        val got = s.answerNow(listOf("p"), lof(listOf(entry("p", hash = hx("new"), says = true, content = "N"))), known113 = false)
        assertEquals(mapOf("p" to "${hx("new")}#S"), got.landed)
        assertEquals(ThumbKind.SMALL, s.cache.kindOf(hx("new")))
        assertNull(s.cache.kindOf(hx("p")))
        assertTrue(got.device113)
    }

    @Test fun askAgainFromAsksTheRestAgainAndOneLeftOutBeforeItHasNone() {
        val s = store()
        val asked = listOf("a", "b", "c", "d", "e")
        val got = s.answerNow(asked, lof(listOf(entry("a", says = true, content = "A"), entry("c", says = false, content = "C")), askAgainFrom = 3), known113 = false)
        // b was looked at and left out: none now (a placeholder, nothing kept).
        assertEquals(mapOf("a" to k("a", 'S'), "c" to k("c", 'B'), "b" to null), got.landed)
        assertNull(s.cache.kindOf(hx("b")))
        // d and e weren't looked at.
        assertEquals(listOf("d", "e"), got.again)
        // A device known to be 113 or later whose answer holds nothing: none for all.
        val empty = s.answerNow(listOf("x", "y"), lof(emptyList()), known113 = true)
        assertEquals(mapOf("x" to null, "y" to null), empty.landed)
        assertTrue(empty.again.isEmpty())
    }

    @Test fun aDeviceBefore113NeverSaysNoneButContentThatDoesntDecodeIsNone() {
        val s = store()
        val asked = listOf("a", "b", "c", "d")
        // It can cut an answer at 8 MB unsaid: b (before the last one
        // answered) has none but isn't recorded so; d (after it) may only have
        // been cut - asked again.
        val got = s.answerNow(asked, lof(listOf(entry("a", content = "A"), entry("c", content = "C"))), known113 = false)
        assertEquals(mapOf("a" to k("a", 'U'), "c" to k("c", 'U')), got.landed)
        assertEquals(listOf("d"), got.again)
        assertFalse(got.device113)
        // An answer with nothing: nothing recorded, nothing asked again (a cut answer holds one at least).
        val empty = s.answerNow(asked, lof(emptyList()), known113 = false)
        assertTrue(empty.landed.isEmpty())
        assertTrue(empty.again.isEmpty())
        // Bytes that don't decode, from any device: none, nothing kept.
        val broken = s.answerNow(listOf("z"), lof(listOf(zeros("z"))), known113 = false)
        assertEquals(mapOf("z" to null), broken.landed)
        assertNull(s.cache.kindOf(hx("z")))
    }

    @Test fun anAnswerForAnotherDeviceIsDropped() {
        val s = store()
        val asked = on { s.scope() }
        // Signed in to another device while the answer was on its way.
        s.core.signedIn("wss://cala.off-the.cloud/ws")
        val got = on { s.takeAnswer(listOf("a"), lof(listOf(entry("a", says = true, content = "A"))), true, asked) }
        assertTrue(got.dropped)
        assertTrue(got.landed.isEmpty())
        assertNull(s.cache.kindOf(hx("a")))
    }

    // ---- the fetcher ----------------------------------------------------------------

    /** A stand-in device for GetThumbnails: [answer] for each request (null: none came). */
    private class Device(@Volatile var answer: suspend (GetThumbnails) -> RespEnvelope?) {
        val asked = CopyOnWriteArrayList<GetThumbnails>()
        val send: suspend (GetThumbnails.Builder) -> RespEnvelope = { b ->
            val req = b.build()
            asked += req
            answer(req) ?: throw IOException("no answer")
        }
    }

    /** Answers every path asked with its small thumbnail. */
    private fun allSmall(req: GetThumbnails) = answer(req.pathsList.map { entry(it, says = true, content = "t:$it") })

    private class Landed {
        val all = CopyOnWriteArrayList<Map<String, String?>>()
        val cb: (Map<String, String?>) -> Unit = { all += it }
        fun keys(): Map<String, String?> = all.fold(emptyMap()) { acc, m -> acc + m }
    }

    private fun fetcher(s: GridThumbStore, dev: Device, landed: Landed, waits: MutableList<Long> = CopyOnWriteArrayList(), gate: CompletableDeferred<Unit>? = null, gatherMs: Long = 0) =
        GridThumbFetcher(scope, s, dev.send, pause = { ms -> waits += ms; gate?.await() }, gatherMs = gatherMs, onLanded = landed.cb)

    /** A page's misses, all within reach of the scroll at once. */
    private fun GridThumbFetcher.want(p: List<String>) { note(p); near(p) }

    private val paths = (0 until 50).map { "p$it" }

    @Test fun batchesOfAtMost24InOrderOneAtATime() {
        val s = store()
        val open = CompletableDeferred<Unit>()
        val dev = Device { req -> open.await(); allSmall(req) }
        val landed = Landed()
        val f = fetcher(s, dev, landed)
        on { f.want(paths) }
        await("the first batch") { dev.asked.size == 1 }
        settle()
        // One on its way at a time, of 24 small ones, in order.
        assertEquals(1, dev.asked.size)
        assertEquals(paths.take(24), dev.asked[0].pathsList)
        assertTrue(dev.asked[0].smallThumbnails)
        open.complete(Unit)
        await("all of them") { landed.keys().size == 50 }
        assertEquals(listOf(24, 24, 2), dev.asked.map { it.pathsCount })
        assertEquals(paths.associateWith { k(it, 'S') }, landed.keys())
        assertEquals(ThumbKind.SMALL, s.cache.kindOf(hx("p49")))
        assertEquals(0, f.pending)
    }

    @Test fun tilesOnScreenGoFirst() {
        val s = store()
        val open = CompletableDeferred<Unit>()
        val dev = Device { req -> open.await(); allSmall(req) }
        val landed = Landed()
        val f = fetcher(s, dev, landed)
        on { f.want(paths.take(30)) }
        await("the first batch") { dev.asked.size == 1 }
        on {
            f.shown("p28", needsThumb = true)
            f.shown("p25", needsThumb = true)
            // Shown and left again: back to its turn.
            f.shown("p29", needsThumb = true)
            f.gone("p29")
            // A tile without one that wasn't waiting (a page's earlier miss): asked for first too.
            f.shown("q", needsThumb = true)
        }
        open.complete(Unit)
        await("the rest") { dev.asked.size == 3 }
        // While a tile on screen waits, a batch holds only tiles on screen.
        assertEquals(listOf("p28", "p25", "q"), dev.asked[1].pathsList)
        assertEquals(listOf("p24", "p26", "p27", "p29"), dev.asked[2].pathsList)
    }

    @Test fun aPagesMissesAreAskedForOnlyAsTheirTilesShowOrComeNear() {
        val s = store()
        val dev = Device { req -> allSmall(req) }
        val landed = Landed()
        val f = fetcher(s, dev, landed)
        val page = (0 until 60).map { "p$it" }
        on { f.note(page) }
        settle()
        assertTrue(dev.asked.isEmpty())
        assertEquals(60, f.waitingToShow)
        // The first screen's tiles: asked for alone.
        on { (0 until 9).forEach { f.shown("p$it", needsThumb = true) } }
        await("the screen") { landed.keys().size == 9 }
        assertEquals((0 until 9).map { "p$it" }, dev.asked[0].pathsList)
        // Within reach of the scroll (12 before, 30 after): asked for next, 24 at a time.
        on { f.near(page.subList(0, 9 + GridThumbFetcher.REACH_AFTER)) }
        await("within reach") { landed.keys().size == 39 }
        assertEquals(listOf(9, 24, 6), dev.asked.map { it.pathsCount })
        // The rest wait for their tiles.
        settle()
        assertEquals(3, dev.asked.size)
        assertEquals(21, f.waitingToShow)
        // A noted tile that shows without needing anything else is asked for too.
        on { f.shown("p59", needsThumb = false) }
        await("p59") { landed.keys().containsKey("p59") }
    }

    @Test fun asksAreGatheredForAMoment() {
        val s = store()
        val dev = Device { req -> allSmall(req) }
        val landed = Landed()
        val f = fetcher(s, dev, landed, gatherMs = GridThumbFetcher.GATHER_MS)
        // Tiles showing over a few frames of a scroll.
        on { f.shown("a", needsThumb = true) }
        on { f.shown("b", needsThumb = true); f.shown("c", needsThumb = true) }
        await("the tiles") { landed.keys().size == 3 }
        assertEquals(listOf(listOf("a", "b", "c")), dev.asked.map { it.pathsList })
    }

    @Test fun aFailedBatchIsAskedAgainWithTheGridsBackoff() {
        val s = store()
        var asks = 0
        val dev = Device { req -> if (++asks <= 2) null else allSmall(req) }
        val landed = Landed()
        val waits = CopyOnWriteArrayList<Long>()
        val f = fetcher(s, dev, landed, waits)
        on { f.want(listOf("a", "b")) }
        await("the thumbnails") { landed.keys().size == 2 }
        assertEquals(listOf(1_000L, 2_000L), waits.toList())
        assertEquals(3, dev.asked.size)
        dev.asked.forEach { assertEquals(listOf("a", "b"), it.pathsList) }
        // An error answer (the bridge's "device unreachable") is a failure too.
        val err = RespEnvelope.newBuilder().setError(true).setErrorCode("device_unreachable").build()
        var asks2 = 0
        dev.answer = { req -> if (++asks2 == 1) err else allSmall(req) }
        on { f.want(listOf("c")) }
        await("c") { landed.keys().containsKey("c") }
        assertEquals(1_000L, waits.last())
    }

    @Test fun whatTheAnswerDidntGetToIsAskedNext() {
        val s = store()
        val dev = Device { req ->
            // The first batch: cut after 10 paths (8 MB).
            if (req.pathsList.first() == "p0") answer(req.pathsList.take(10).map { entry(it, says = true, content = "t") }, askAgainFrom = 10)
            else allSmall(req)
        }
        val landed = Landed()
        val f = fetcher(s, dev, landed)
        on { f.want(paths.take(30)) }
        await("all 30") { landed.keys().size == 30 }
        assertEquals(paths.subList(10, 30).take(24), dev.asked[1].pathsList)
    }

    @Test fun oneWithoutAThumbnailIsNotAskedAgainUntilTheGridStartsOver() {
        val s = store()
        // A device of release 113 without p1's thumbnail (not processed yet).
        val dev = Device { req -> answer(req.pathsList.filter { it != "p1" }.map { entry(it, says = true, content = "t") }) }
        val landed = Landed()
        val f = fetcher(s, dev, landed)
        on { f.want(listOf("p0", "p1")) }
        await("the answer") { landed.keys().size == 2 }
        assertNull(landed.keys()["p1"])
        assertTrue(landed.keys().containsKey("p1"))
        // Its tile shows: no thumbnail, and nothing asked.
        on { f.shown("p1", needsThumb = true) }
        settle()
        assertEquals(1, dev.asked.size)
        // A new search: asked again when it shows.
        on { f.reset(); f.shown("p1", needsThumb = true) }
        await("asked again") { dev.asked.size == 2 }
        assertEquals(listOf("p1"), dev.asked[1].pathsList)
    }

    @Test fun aDeviceKnownToLeaveThumbnailsOutHasNoneForWhatItLeavesOut() {
        // The marker (a page without content) seeds what an answer means: an
        // empty answer from it is none for all, not a cut.
        val s = store(omits = true)
        val dev = Device { answer(emptyList()) }
        val landed = Landed()
        val f = fetcher(s, dev, landed)
        on { f.want(listOf("a", "b")) }
        await("the answer") { landed.keys().size == 2 }
        assertEquals(mapOf("a" to null, "b" to null), landed.keys())
        assertTrue(f.device113)
    }

    @Test fun aNewSearchDropsWhatWaitsAndTheOldAnswerLandsNowhere() {
        val s = store()
        val open = CompletableDeferred<Unit>()
        val dev = Device { req -> open.await(); allSmall(req) }
        val landed = Landed()
        val f = fetcher(s, dev, landed)
        on { f.want(paths) }
        await("the first batch") { dev.asked.size == 1 }
        on { f.reset(); f.want(listOf("new")) }
        open.complete(Unit)
        await("the new one") { landed.keys().containsKey("new") }
        // The old search's batch is kept in the cache but not landed; its others were never asked.
        assertEquals(listOf(listOf("new")), dev.asked.drop(1).map { it.pathsList })
        assertEquals(setOf("new"), landed.keys().keys)
        assertEquals(ThumbKind.SMALL, s.cache.kindOf(hx("p0")))
    }

    @Test fun aTileWhoseBytesKeepFailingIsMarkedNone() {
        val s = store()
        val dev = Device { req -> allSmall(req) }
        val landed = Landed()
        val f = fetcher(s, dev, landed)
        on { f.markNone("a"); f.shown("a", needsThumb = true); f.want(listOf("a")) }
        settle()
        assertTrue(dev.asked.isEmpty())
    }

    @Test fun aDeviceWithoutGetThumbnailsIsNotAskedAgain() {
        val s = store()
        val unknown = RespEnvelope.newBuilder().setError(true).setErrorCode("unknown_payload").setErrorMessage("unknown payload").build()
        val dev = Device { unknown }
        val landed = Landed()
        val waits = CopyOnWriteArrayList<Long>()
        val f = fetcher(s, dev, landed, waits)
        on { f.want(listOf("a")) }
        await("the ask") { dev.asked.size == 1 }
        on { f.want(listOf("b")); f.shown("c", needsThumb = true) }
        settle()
        assertEquals(1, dev.asked.size)
        assertTrue(waits.isEmpty())
    }
}
