// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.wallpaper

import cloud.offthe.otc.proto.Ack
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ImageGroup
import cloud.offthe.otc.proto.ImageGroups
import cloud.offthe.otc.proto.ListOfFiles
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import com.google.protobuf.ByteString
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException
import kotlin.random.Random

// A collection's copies brought in step with it (WallpaperFetch): what is
// asked of the device, which photos are fetched and how (GetFile, never the
// original's ReadFile), what is deleted, and what a failure leaves.
class WallpaperFetchTest {
    private fun hash(i: Int) = "%064x".format(i)
    private fun photo(i: Int, mime: String = "image/jpeg") = File.newBuilder().setHash(hash(i)).setPath("/Photos/p$i.jpg").setMime(mime).build()

    /** The copies on the phone, in memory; [undecodable] can't be made into a copy. */
    private class Copies(initial: Set<String> = emptySet(), val undecodable: Set<String> = emptySet()) : WallpaperCopies {
        val stored = LinkedHashMap<String, ByteArray>().apply { for (h in initial) put(h, ByteArray(1)) }
        val deleted = mutableListOf<String>()
        override fun hashes() = stored.keys.toSet()
        override fun save(hash: String, bytes: ByteArray): Boolean {
            if (hash in undecodable) return false
            stored[hash] = bytes
            return true
        }
        override fun delete(hash: String) { stored.remove(hash); deleted += hash }
    }

    /**
     * A device with collection g1 ("Trip") holding [members], in pages of
     * [pageSize]; [groups] false: it has no collections left; [failAt]: the
     * request kinds that go unanswered; [refused]: paths whose GetFile it refuses.
     */
    private inner class Device(
        var members: List<File>, val pageSize: Int = 1000, val groups: Boolean = true,
        val failAt: Set<ReqEnvelope.PayloadCase> = emptySet(), val refused: Set<String> = emptySet(),
        val unreachableFiles: Boolean = false,
    ) {
        val sent = mutableListOf<ReqEnvelope>()
        val send: WallpaperRequest = { build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            sent += req
            if (req.payloadCase in failAt) throw IOException("no answer")
            when (req.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS -> RespEnvelope.newBuilder().setRespImageGroups(
                    ImageGroups.newBuilder().apply { if (groups) addGroups(ImageGroup.newBuilder().setId("g1").setName("Trip 2026").setFileCount(members.size)) },
                ).build()
                ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS -> {
                    val s = req.reqSearchPhotos
                    val from = if (s.token.isEmpty()) 0 else s.token.toInt()
                    val page = members.drop(from).take(pageSize)
                    val next = from + page.size
                    RespEnvelope.newBuilder().setRespListOfFiles(
                        ListOfFiles.newBuilder().addAllFiles(page).setToken(if (next < members.size) "$next" else ""),
                    ).build()
                }
                ReqEnvelope.PayloadCase.REQ_GET_FILE -> when {
                    unreachableFiles -> RespEnvelope.newBuilder().setRespAck(Ack.newBuilder().setCode("device_unreachable")).build()
                    req.reqGetFile.path in refused -> RespEnvelope.newBuilder().setError(true).setErrorMessage("no such file").build()
                    else -> RespEnvelope.newBuilder().setRespFile(
                        File.newBuilder().setPath(req.reqGetFile.path).setContent(ByteString.copyFromUtf8("jpeg of ${req.reqGetFile.path}")),
                    ).build()
                }
                else -> throw IOException("not answered")
            }
        }
        fun of(case: ReqEnvelope.PayloadCase) = sent.filter { it.payloadCase == case }
    }

    private fun fetch(dev: Device, copies: Copies) = WallpaperFetch(dev.send, copies, Random(3))

    @Test fun aFirstFetchStoresEveryPhotoSizedForTheScreen() = runBlocking {
        val dev = Device((1..5).map { photo(it) })
        val copies = Copies()
        val progress = mutableListOf<Pair<Int, Int>>()
        val stored = mutableListOf<String>()
        val out = fetch(dev, copies).run("g1", progress = { d, t -> progress += d to t }, stored = { stored += it })
        assertEquals(WallpaperFetch.Outcome.Done("Trip 2026", 5), out)
        assertEquals((1..5).map(::hash).toSet(), copies.hashes())
        // As the viewer shows a photo full screen: GetFile, by path.
        assertEquals((1..5).map { "/Photos/p$it.jpg" }.toSet(), dev.of(ReqEnvelope.PayloadCase.REQ_GET_FILE).map { it.reqGetFile.path }.toSet())
        assertTrue(dev.sent.none { it.payloadCase == ReqEnvelope.PayloadCase.REQ_READ_FILE })
        assertEquals(listOf(0 to 5, 1 to 5, 2 to 5, 3 to 5, 4 to 5, 5 to 5), progress)
        assertEquals(5, stored.size)
        // The collection's photos, without thumbnails.
        val search = dev.of(ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS).single().reqSearchPhotos
        assertEquals("g1", search.groupId)
        assertTrue(search.omitThumbnails)
        assertFalse(search.includeVideos)
    }

    @Test fun videosAreNeverCopied() = runBlocking {
        val dev = Device(listOf(photo(1), photo(2, mime = "video/mp4"), photo(3, mime = "image/heic")))
        val copies = Copies()
        fetch(dev, copies).run("g1")
        assertEquals(setOf(hash(1), hash(3)), copies.hashes())
    }

    @Test fun pagesAreFollowedToTheEnd() = runBlocking {
        val dev = Device((1..25).map { photo(it) }, pageSize = 10)
        val list = WallpaperFetch.listPhotos(dev.send, "g1")!!
        assertEquals(25, list.size)
        val pages = dev.of(ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS).map { it.reqSearchPhotos }
        assertEquals(listOf("", "10", "20"), pages.map { it.token })
        assertEquals(listOf(0, 10, 20), pages.map { it.have })
    }

    @Test fun aBigCollectionGivesAHundred() = runBlocking {
        val dev = Device((1..300).map { photo(it) }, pageSize = 200)
        val copies = Copies()
        val out = fetch(dev, copies).run("g1")
        assertEquals(WallpaperFetch.Outcome.Done("Trip 2026", 100), out)
        assertEquals(100, dev.of(ReqEnvelope.PayloadCase.REQ_GET_FILE).size)
    }

    @Test fun inStepAddsTheNewAndDeletesTheRemoved() = runBlocking {
        val dev = Device(listOf(photo(1), photo(2), photo(4)))
        val copies = Copies(setOf(hash(1), hash(2), hash(3)))
        val out = fetch(dev, copies).run("g1")
        assertEquals(WallpaperFetch.Outcome.Done("Trip 2026", 3), out)
        assertEquals(listOf(hash(3)), copies.deleted)
        assertEquals(setOf(hash(1), hash(2), hash(4)), copies.hashes())
        // Only the new one is fetched.
        assertEquals(listOf("/Photos/p4.jpg"), dev.of(ReqEnvelope.PayloadCase.REQ_GET_FILE).map { it.reqGetFile.path })
    }

    @Test fun aDeletedCollectionIsGone() = runBlocking {
        val dev = Device(listOf(photo(1)), groups = false)
        val copies = Copies(setOf(hash(1)))
        assertEquals(WallpaperFetch.Outcome.Gone, fetch(dev, copies).run("g1"))
        // The caller stops it and deletes the folder; nothing fetched.
        assertTrue(dev.of(ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS).isEmpty())
    }

    @Test fun aListThatDidntComeWholeDeletesNothing() = runBlocking {
        for (fail in listOf(ReqEnvelope.PayloadCase.REQ_LIST_IMAGE_GROUPS, ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS)) {
            val dev = Device(listOf(photo(1)), failAt = setOf(fail))
            val copies = Copies(setOf(hash(1), hash(2)))
            assertEquals(WallpaperFetch.Outcome.Failed, fetch(dev, copies).run("g1"))
            assertEquals(setOf(hash(1), hash(2)), copies.hashes())
            assertTrue(copies.deleted.isEmpty())
        }
        // A device that starts its search over each time: not trusted either.
        val looping: WallpaperRequest = { build ->
            val req = ReqEnvelope.newBuilder().also(build).build()
            RespEnvelope.newBuilder().setRespListOfFiles(ListOfFiles.newBuilder().addFiles(photo(1)).setToken("again")).build()
                .takeIf { req.payloadCase == ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS } ?: throw IOException()
        }
        assertNull(WallpaperFetch.listPhotos(looping, "g1"))
    }

    @Test fun aRefusedOrUndecodablePhotoIsSkipped() = runBlocking {
        val dev = Device((1..3).map { photo(it) }, refused = setOf("/Photos/p2.jpg"))
        val copies = Copies(undecodable = setOf(hash(3)))
        val progress = mutableListOf<Pair<Int, Int>>()
        val out = fetch(dev, copies).run("g1", progress = { d, t -> progress += d to t })
        assertEquals(WallpaperFetch.Outcome.Done("Trip 2026", 1), out)
        assertEquals(setOf(hash(1)), copies.hashes())
        assertEquals(1 to 3, progress.last())
    }

    @Test fun noAnswerForAPhotoStopsAndKeepsWhatIsStored() = runBlocking {
        val dev = Device((1..3).map { photo(it) }, failAt = setOf(ReqEnvelope.PayloadCase.REQ_GET_FILE))
        val copies = Copies(setOf(hash(9)))
        assertEquals(WallpaperFetch.Outcome.Failed, fetch(dev, copies).run("g1"))
        // 9 is no longer in the collection: deleted once the list came whole.
        assertTrue(copies.hashes().isEmpty())
        // The bridge saying the device is unreachable is no answer either.
        val away = Device((1..3).map { photo(it) }, unreachableFiles = true)
        assertEquals(WallpaperFetch.Outcome.Failed, fetch(away, Copies()).run("g1"))
    }

    @Test fun theNoticesCountIsTheCollectionsPhotos() = runBlocking {
        // A collection's search brings its videos too: they aren't counted.
        val dev = Device((1..5).map { photo(it) } + photo(6, mime = "video/mp4"), pageSize = 2)
        assertEquals(5, WallpaperFetch.countPhotos(dev.send, "g1"))
        assertEquals("g1", dev.of(ReqEnvelope.PayloadCase.REQ_SEARCH_PHOTOS).first().reqSearchPhotos.groupId)
        // Nothing downloaded to count them.
        assertTrue(dev.of(ReqEnvelope.PayloadCase.REQ_GET_FILE).isEmpty())
        assertNull(WallpaperFetch.countPhotos({ throw IOException("no answer") }, "g1"))
    }
}
