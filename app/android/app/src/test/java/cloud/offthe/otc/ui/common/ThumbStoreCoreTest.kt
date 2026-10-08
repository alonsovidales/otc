// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File
import kotlin.coroutines.EmptyCoroutineContext

// ThumbStore's workings: the cache of the device signed in to (bound by a
// sign-in or the launch's saved endpoint, never an address merely saved),
// another device's deleted, Log Out leaving nothing, writes asked of
// another device dropped, the "omits" marker kept with the settings,
// thumbnails that don't decode never kept, and the re-asks.
class ThumbStoreCoreTest {
    @get:Rule val tmp = TemporaryFolder()

    private val pit = "wss://pit.off-the.cloud/ws"
    private val cala = "wss://cala.off-the.cloud/ws"

    private fun core(saved: () -> String = { pit }, nowMs: () -> Long = System::currentTimeMillis, dropped: () -> Unit = {}) = ThumbStoreCore(
        cacheRoot = File(tmp.root, "cache/grid-thumbs"), stateDir = File(tmp.root, "files"), savedEndpoint = saved,
        decodable = ::testDecodable, onMemoryDropped = dropped, io = EmptyCoroutineContext, background = DIRECT, nowMs = nowMs,
    )

    private fun scopeDir(endpoint: String) = File(tmp.root, "cache/grid-thumbs/${ThumbStoreCore.scopeIdOf(endpoint)}")
    private fun put(c: ThumbStoreCore, name: String, kind: ThumbKind = ThumbKind.SMALL, bytes: ByteArray = name.toByteArray()) =
        runBlocking { c.put(listOf(ThumbPut(hx(name), bytes, kind)), c.scope()) }

    @Test fun boundAtLaunchFromTheSavedEndpointAndThenOnlyBySigningIn() {
        var saved = pit
        val c = core(saved = { saved })
        put(c, "a")
        assertEquals(ThumbKind.SMALL, runBlocking { c.kinds(listOf(hx("a"))) }[hx("a")])
        assertTrue(scopeDir(pit).isDirectory)
        // Save Connection with another address (mistyped, or leaving the
        // bridge): nothing changes until it signs in.
        saved = cala
        assertEquals(ThumbKind.SMALL, runBlocking { c.kinds(listOf(hx("a"))) }[hx("a")])
        assertTrue(scopeDir(pit).isDirectory)
        // Signed in to the same device again: nothing changes either.
        c.signedIn(pit)
        assertEquals(ThumbKind.SMALL, runBlocking { c.kinds(listOf(hx("a"))) }[hx("a")])
        // Signed in to another device: its own cache, the other one deleted.
        c.signedIn(cala)
        assertTrue(runBlocking { c.kinds(listOf(hx("a"))) }.isEmpty())
        assertFalse(scopeDir(pit).exists())
        put(c, "b")
        assertTrue(scopeDir(cala).isDirectory)
    }

    @Test fun nothingIsBoundWithoutAnEndpoint() {
        val c = core(saved = { "" })
        put(c, "a")
        // Shown from memory, nothing on disk.
        assertArrayEquals("a".toByteArray(), runBlocking { c.load(thumbTileKey(hx("a"), ThumbKind.SMALL)) })
        assertFalse(File(tmp.root, "cache/grid-thumbs").exists())
    }

    @Test fun aWriteAskedOfAnotherDeviceIsDropped() {
        val c = core()
        val asked = runBlocking { c.scope() }
        c.signedIn(cala)
        assertNull(runBlocking { c.put(listOf(ThumbPut(hx("a"), "a".toByteArray(), ThumbKind.SMALL)), asked) })
        runBlocking { c.notePage(true, asked) }
        assertFalse(runBlocking { c.omitsThumbnails() })
        assertTrue(runBlocking { c.kinds(listOf(hx("a"))) }.isEmpty())
        assertNull(runBlocking { c.load(thumbTileKey(hx("a"), ThumbKind.SMALL)) })
    }

    @Test fun logOutLeavesNothingAndKeepsNothingUntilTheNextSignIn() {
        var memoryDropped = 0
        val c = core(dropped = { memoryDropped++ })
        put(c, "a")
        runBlocking { c.setLimit(5_000_000_000L); c.notePage(true, c.scope()) }
        val before = runBlocking { c.scope() }
        c.logOut()
        // Every cache, the size picked and the marker gone.
        assertFalse(File(tmp.root, "cache/grid-thumbs").exists())
        assertFalse(File(tmp.root, "files/${ThumbStoreCore.LIMIT_FILE}").exists())
        assertFalse(File(tmp.root, "files/${ThumbStoreCore.OMITS_FILE}").exists())
        assertEquals(ThumbStoreCore.Usage(0, THUMB_CACHE_DEFAULT), c.usage.value)
        assertFalse(runBlocking { c.omitsThumbnails() })
        assertTrue(memoryDropped >= 1)
        // A fetch retried across it (its scope from before) writes nothing,
        // and a new one finds nothing bound: the folder isn't made again.
        assertNull(runBlocking { c.put(listOf(ThumbPut(hx("b"), "b".toByteArray(), ThumbKind.SMALL)), before) })
        put(c, "c")
        runBlocking { c.notePage(true, c.scope()) }
        assertFalse(File(tmp.root, "cache/grid-thumbs").exists())
        assertFalse(File(tmp.root, "files/${ThumbStoreCore.OMITS_FILE}").exists())
        // Signed in again: an empty cache of the default size.
        c.signedIn(pit)
        put(c, "d")
        runBlocking { c.refreshUsage() }
        assertEquals(THUMB_CACHE_DEFAULT, c.usage.value.limit)
        assertEquals(setOf(hx("d")), runBlocking { c.kinds(listOf(hx("a"), hx("d"))) }.keys)
    }

    @Test fun theOmitsMarkerIsKeptWithTheSettingsForItsDeviceOnly() {
        val c = core()
        assertFalse(runBlocking { c.omitsThumbnails() })
        runBlocking { c.notePage(true, c.scope()) }
        assertTrue(runBlocking { c.omitsThumbnails() })
        // Kept in filesDir: a restart, and the system clearing the cache folder, keep it.
        File(tmp.root, "cache").deleteRecursively()
        assertTrue(runBlocking { core().omitsThumbnails() })
        // Another device's isn't this one's.
        val other = core(saved = { cala })
        assertFalse(runBlocking { other.omitsThumbnails() })
        // A page with content says it doesn't (any more).
        val again = core()
        runBlocking { again.notePage(false, again.scope()) }
        assertFalse(runBlocking { core().omitsThumbnails() })
    }

    @Test fun thumbnailsThatDontDecodeAreNeverKeptAndDroppedWhenRead() {
        val c = core()
        // An answer's bytes that don't decode: none, nothing kept.
        val got = runBlocking { c.put(listOf(ThumbPut(hx("z"), ByteArray(40), ThumbKind.SMALL), ThumbPut(hx("a"), "a".toByteArray(), ThumbKind.SMALL)), c.scope()) }
        assertEquals(setOf(hx("a")), got!!.keys)
        assertTrue(runBlocking { c.kinds(listOf(hx("z"))) }.isEmpty())
        // A file a power cut left zeroed (the right size): dropped when read, after a restart.
        put(c, "b", bytes = "bbbb".toByteArray())
        File(File(scopeDir(pit), hx("b").take(2)), hx("b")).writeBytes(ByteArray(4))
        val restarted = core()
        assertNull(runBlocking { restarted.load(thumbTileKey(hx("b"), ThumbKind.SMALL)) })
        assertTrue(runBlocking { restarted.kinds(listOf(hx("b"))) }.isEmpty())
        // A tile that didn't decode is discarded.
        runBlocking { restarted.discard(thumbTileKey(hx("a"), ThumbKind.SMALL)) }
        assertTrue(runBlocking { restarted.kinds(listOf(hx("a"))) }.isEmpty())
    }

    @Test fun reasksAreOnceALaunchEachForTheGridsAndFilesAndClearedWithTheCacheOrADevice() {
        val c = core()
        put(c, "a", ThumbKind.BIG)
        val h = listOf(hx("a"))
        assertEquals(h.toSet(), runBlocking { c.claimReasks(h) })
        assertTrue(runBlocking { c.claimReasks(h) }.isEmpty())
        // Files' own, apart from the grids'.
        assertEquals(h.toSet(), runBlocking { c.claimFilesReasks(h) })
        assertTrue(runBlocking { c.claimFilesReasks(h) }.isEmpty())
        // Clear starts them over.
        runBlocking { c.clearAll() }
        assertEquals(h.toSet(), runBlocking { c.claimReasks(h) })
        // So does another device.
        c.signedIn(cala)
        assertEquals(h.toSet(), runBlocking { c.claimFilesReasks(h) })
    }

    @Test fun aBigOneAskedAgainThatCameBigAgainWaitsAWeekAcrossLaunches() {
        var now = 1_000_000_000_000L
        val c = core(nowMs = { now })
        put(c, "a", ThumbKind.BIG)
        val h = listOf(hx("a"))
        assertEquals(h.toSet(), runBlocking { c.claimReasks(h) })
        runBlocking { c.noteStillBig(hx("a"), c.scope()) }
        // The next launch: not asked again, by the grids nor Files.
        val next = core(nowMs = { now })
        assertTrue(runBlocking { next.claimReasks(h) }.isEmpty())
        assertTrue(runBlocking { next.claimFilesReasks(h) }.isEmpty())
        // A week on: asked once more.
        now += ThumbStoreCore.REASK_AFTER_MS + 1_000
        assertEquals(h.toSet(), runBlocking { core(nowMs = { now }).claimReasks(h) })
    }

    @Test fun theSizePickedIsKeptAndAppliedAtOnce() {
        val c = core()
        repeat(5) { put(c, "p$it") }
        runBlocking { c.setLimit(250_000_000L) }
        assertEquals(250_000_000L, c.usage.value.limit)
        assertTrue(c.usage.value.used > 0)
        val restarted = core()
        runBlocking { restarted.refreshUsage() }
        assertEquals(250_000_000L, restarted.usage.value.limit)
        assertEquals(c.usage.value.used, restarted.usage.value.used)
        // Clear: nothing used, the size kept.
        runBlocking { restarted.clearAll() }
        assertEquals(ThumbStoreCore.Usage(0, 250_000_000L), restarted.usage.value)
    }

    @Test fun theScopeIsTheEndpointsDigest() {
        assertEquals(32, ThumbStoreCore.scopeIdOf(pit).length)
        assertTrue(ThumbStoreCore.scopeIdOf(pit) != ThumbStoreCore.scopeIdOf(cala))
    }
}
