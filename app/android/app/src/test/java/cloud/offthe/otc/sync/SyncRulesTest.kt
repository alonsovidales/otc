// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.sync

import androidx.work.NetworkType
import cloud.offthe.otc.data.SyncHalt
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.WSClient
import cloud.offthe.otc.ui.NotificationsAsk
import cloud.offthe.otc.ui.gallery.saveFolder
import kotlinx.coroutines.CancellationException
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.io.IOException
import java.nio.file.Files

// The sync and connection rules of the 2026-10 review (A1, A4, A5, A7, A9,
// UX 1, storage_full), as iOS's SyncRulesTests.swift.
class SyncRulesTest {
    /** A1: "Wi-Fi only" holds the sync on a metered network only, and the background runs ask for an unmetered one. */
    @Test fun wifiOnlyOnAMeteredNetworkWaits() {
        assertTrue(PhotoSync.waitsForWifi(wifiOnly = true, metered = true))
        assertFalse(PhotoSync.waitsForWifi(wifiOnly = true, metered = false))
        assertFalse(PhotoSync.waitsForWifi(wifiOnly = false, metered = true))
        assertFalse(PhotoSync.waitsForWifi(wifiOnly = false, metered = false))
        assertEquals(NetworkType.UNMETERED, SyncScheduler.networkType(wifiOnly = true))
        assertEquals(NetworkType.CONNECTED, SyncScheduler.networkType(wifiOnly = false))
    }

    @Test fun saysWhyItWaits() {
        assertEquals("Waiting for Wi-Fi", SyncHalt.WaitingForWifi.title.english())
        assertNull(SyncHalt.WaitingForWifi.detail)
        assertEquals("Your device is full", SyncHalt.DeviceFull("not enough space").title.english())
        assertEquals("not enough space", SyncHalt.DeviceFull("not enough space").detail)
        assertNull(SyncHalt.DeviceFull("").detail)
    }

    /** A9: the folder's listing only for a big run, or a phone that has synced nothing yet. */
    @Test fun smallRunsGoByHash() {
        assertFalse(PhotoSync.needsListing(0, cacheEmpty = true))
        assertFalse(PhotoSync.needsListing(0, cacheEmpty = false))
        assertFalse(PhotoSync.needsListing(1, cacheEmpty = false))
        assertFalse(PhotoSync.needsListing(PhotoSync.LISTING_FROM - 1, cacheEmpty = false))
        assertTrue(PhotoSync.needsListing(PhotoSync.LISTING_FROM, cacheEmpty = false))
        assertTrue(PhotoSync.needsListing(1, cacheEmpty = true))
        assertEquals(100, PhotoSync.LISTING_FROM)
    }

    /** Automatic runs wait half an hour after the device said it was full; a clock set back doesn't hold them for ever. */
    @Test fun runsWaitAWhileAfterTheDeviceWasFull() {
        val since = 1_000_000_000L
        assertTrue(PhotoSync.waitsAfterFull(since, since))
        assertTrue(PhotoSync.waitsAfterFull(since, since + PhotoSync.FULL_PAUSE_MS - 1))
        assertFalse(PhotoSync.waitsAfterFull(since, since + PhotoSync.FULL_PAUSE_MS))
        assertFalse(PhotoSync.waitsAfterFull(since, since - 60_000))
        assertFalse(PhotoSync.waitsAfterFull(0, since))
        assertTrue(cloud.offthe.otc.net.ErrorCodes.isStorageFull("storage_full"))
        assertFalse(cloud.offthe.otc.net.ErrorCodes.isStorageFull(""))
        assertFalse(cloud.offthe.otc.net.ErrorCodes.isStorageFull(null))
    }

    /** A5: what failed is tried again by later runs, whatever the watermark; kept through a restart. */
    @Test fun theRetryList() {
        val dir = Files.createTempDirectory("otc-pending").toFile()
        try {
            val file = File(dir, "asset_sync_pending.txt")
            val list = PendingList(file)
            assertTrue(list.all().isEmpty())
            list.mark(listOf(5L, 7L, 5L))
            list.mark(listOf(9L))
            assertEquals(setOf(5L, 7L, 9L), list.all())
            // As the next launch reads it.
            assertEquals(setOf(5L, 7L, 9L), PendingList(file).all())
            list.resolve(listOf(7L, 42L))
            assertEquals(setOf(5L, 9L), PendingList(file).all())
            list.resolve(listOf(5L, 9L))
            assertTrue(list.all().isEmpty())
            assertFalse("an empty list leaves no file", file.exists())
            list.mark(listOf(3L))
            list.clear()
            assertTrue(PendingList(file).all().isEmpty())
            // A torn or odd file reads as what it holds that makes sense.
            file.writeText("12\nnot an id\n\n13")
            assertEquals(setOf(12L, 13L), PendingList(file).all())
        } finally {
            dir.deleteRecursively()
        }
    }

    /** A4: what the viewer's Save wrote stays out of the sync, its folder list and Free Up Space. */
    @Test fun savedFromTheDeviceStaysOut() {
        assertEquals(listOf(saveFolder("image/jpeg") + "/", saveFolder("video/mp4") + "/"), PhotoFolders.savedFolders)
        assertTrue(PhotoFolders.isSaved("Pictures/Off The Cloud/"))
        assertTrue(PhotoFolders.isSaved("Movies/Off The Cloud/"))
        assertTrue(PhotoFolders.isSaved("pictures/off the cloud/"))
        assertFalse(PhotoFolders.isSaved("Pictures/Off The Cloud Backup/"))
        assertFalse(PhotoFolders.isSaved("DCIM/Camera/"))
        assertFalse(PhotoFolders.isSaved(null))
        assertEquals(
            "(relative_path IS NULL OR (relative_path NOT LIKE 'Pictures/Off The Cloud/%' AND relative_path NOT LIKE 'Movies/Off The Cloud/%'))",
            PhotoFolders.savedClause,
        )
        // The sync's selection leaves it out; the composer's (the default) doesn't.
        assertEquals("media_type IN (1,3) AND ${PhotoFolders.savedClause}", PhotoSync.selection(true, 0, leaveOutSaved = true))
        assertEquals("media_type IN (1,3)", PhotoSync.selection(true, 0))
        assertEquals(
            "media_type IN (1) AND date_added > 5 AND (bucket_id IS NULL OR bucket_id NOT IN (12)) AND ${PhotoFolders.savedClause}",
            PhotoSync.selection(false, 5_000, skipped = setOf("12"), leaveOutSaved = true),
        )
    }

    /** A7: a request goes again by itself only when it never left the phone. */
    @Test fun onlyWhatNeverLeftIsSentAgain() {
        assertFalse(OTCConnection.retriesAfter(WSClient.SentNoAnswer(IOException("gone"))))
        assertFalse(OTCConnection.retriesAfter(WSClient.RequestTimeout()))
        assertFalse(OTCConnection.retriesAfter(CancellationException("closed")))
        assertTrue(OTCConnection.retriesAfter(IOException("connection closed")))
        assertTrue(OTCConnection.retriesAfter(OTCConnection.RequestError("no")))
        assertEquals("Sent, but no answer came back. Check whether it went through before trying again.", WSClient.SentNoAnswer(IOException()).text.english())
    }

    /** UX 1: once, after the first sign-in, with nothing else on screen. */
    @Test fun notificationsAreAskedOnceSignedInWithNothingElseUp() {
        assertTrue(NotificationsAsk.shouldAsk(signedIn = true, syncChoicePending = false, tourShown = false, mainVisible = true, undecided = true, askedBefore = false))
        assertFalse(NotificationsAsk.shouldAsk(signedIn = false, syncChoicePending = false, tourShown = false, mainVisible = true, undecided = true, askedBefore = false))
        assertFalse(NotificationsAsk.shouldAsk(signedIn = true, syncChoicePending = true, tourShown = false, mainVisible = true, undecided = true, askedBefore = false))
        assertFalse(NotificationsAsk.shouldAsk(signedIn = true, syncChoicePending = false, tourShown = true, mainVisible = true, undecided = true, askedBefore = false))
        assertFalse(NotificationsAsk.shouldAsk(signedIn = true, syncChoicePending = false, tourShown = false, mainVisible = false, undecided = true, askedBefore = false))
        assertFalse(NotificationsAsk.shouldAsk(signedIn = true, syncChoicePending = false, tourShown = false, mainVisible = true, undecided = false, askedBefore = false))
        assertFalse(NotificationsAsk.shouldAsk(signedIn = true, syncChoicePending = false, tourShown = false, mainVisible = true, undecided = true, askedBefore = true))
    }

    /** A19: ids and kinds in the log, never what a file's exception says. */
    @Test fun failuresAreLoggedByKind() {
        assertEquals("FileNotFoundException", PhotoSync.logged(java.io.FileNotFoundException("/storage/emulated/0/DCIM/Camera/IMG_1234.jpg: open failed")))
        assertEquals("RequestError(code=storage_full)", PhotoSync.logged(OTCConnection.RequestError("no room for IMG_1234.jpg", "storage_full")))
    }
}
