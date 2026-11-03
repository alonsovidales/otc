// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.BeginUpload
import cloud.offthe.otc.proto.File
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.UploadProgress
import cloud.offthe.otc.proto.UploadStarted
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.io.InputStream
import java.security.MessageDigest

// Issue #205 against a stand-in device: BeginUpload names the content, an
// upload starts at what the device already holds, one cut off mid-file is
// begun again with the same key and goes on from there - never reading the
// bytes before it out of the file - and a device before release 166, which
// answers 0 and forgot the upload with its connection, gets it whole again.
// As iOS's ChunkedUploadTests.
class ChunkedUploadTest {
    private val chunk = ChunkedUpload.chunkSize

    /** [n] bytes of content that differ from chunk to chunk. */
    private fun content(n: Int) = ByteArray(n) { (it * 31 + it / 7).toByte() }

    private fun sha(b: ByteArray) = MessageDigest.getInstance("SHA-256").digest(b).joinToString("") { "%02x".format(it) }

    /** The file as a stream that counts what is read out of it and what is skipped. */
    private class Source(private val bytes: ByteArray) : InputStream() {
        var pos = 0
        var read = 0L
        var skipped = 0L
        var skipWorks = true

        override fun read(): Int {
            if (pos >= bytes.size) return -1
            read++
            return bytes[pos++].toInt() and 0xff
        }

        override fun read(b: ByteArray, off: Int, len: Int): Int {
            if (pos >= bytes.size) return -1
            val n = minOf(len, bytes.size - pos)
            System.arraycopy(bytes, pos, b, off, n)
            pos += n
            read += n
            return n
        }

        override fun skip(n: Long): Long {
            if (!skipWorks) return 0
            val s = minOf(n, (bytes.size - pos).toLong()).toInt()
            pos += s
            skipped += s
            return s.toLong()
        }
    }

    /** Every stream the uploader opened, in order. */
    private class Opens(private val bytes: ByteArray) {
        val streams = mutableListOf<Source>()
        fun open(): InputStream = Source(bytes).also { streams += it }
    }

    /**
     * The device's side of BeginUpload/UploadChunk/FinishUpload. [resumes]:
     * release 166, which keeps an unfinished upload across connections and
     * answers BeginUpload of the same path, size and sha256 with what it
     * holds; otherwise an older device, which drops it with its connection
     * and always answers 0. [dropAfter]: the connection goes once the device
     * has taken that chunk (its answer lost), each count once.
     */
    private class Device(val resumes: Boolean = true, val dropAfter: MutableSet<Int> = mutableSetOf()) {
        class Upload(val id: String, val key: String, val size: Long, val data: ByteArrayOutputStream = ByteArrayOutputStream())

        val uploads = mutableMapOf<String, Upload>()
        val begins = mutableListOf<BeginUpload>()
        val offsets = mutableListOf<Long>()
        val stored = mutableMapOf<String, ByteArray>()
        var chunksTaken = 0
        var nextId = 0
        /** What the next UploadChunk is answered with instead, once. */
        var chunkAnswer: RespEnvelope? = null
        /** BeginUploads that fail on the way, from the second on. */
        var beginDrops = 0

        fun keyOf(b: BeginUpload) = "${b.path}|${b.size}|${b.sha256}"

        /** An unfinished upload this device already holds, as another connection left it. */
        fun holds(path: String, size: Long, sha256: String, bytes: ByteArray) {
            val key = "$path|$size|$sha256"
            uploads[key] = Upload("u${nextId++}", key, size).also { it.data.write(bytes) }
        }

        fun drop(): Nothing {
            // A device before 166 aborts what the connection had going.
            if (!resumes) uploads.clear()
            throw IOException("connection dropped")
        }

        val link = ChunkedUpload.Link { build ->
            val env = ReqEnvelope.newBuilder().also(build).build()
            val out = RespEnvelope.newBuilder().setId(env.id)
            when (env.payloadCase) {
                ReqEnvelope.PayloadCase.REQ_BEGIN_UPLOAD -> {
                    val b = env.reqBeginUpload
                    begins += b
                    if (begins.size > 1 && beginDrops > 0) { beginDrops--; throw IOException("still no network") }
                    val key = keyOf(b)
                    val held = uploads[key]?.takeIf { resumes && b.sha256.isNotEmpty() }
                    val up = held ?: Upload("u${nextId++}", key, b.size).also { uploads[key] = it }
                    out.setRespUploadStarted(UploadStarted.newBuilder().setUploadId(up.id).setReceived(up.data.size().toLong()))
                }
                ReqEnvelope.PayloadCase.REQ_UPLOAD_CHUNK -> {
                    val c = env.reqUploadChunk
                    offsets += c.offset
                    chunkAnswer?.let { chunkAnswer = null; return@Link it }
                    val up = uploads.values.firstOrNull { it.id == c.uploadId }
                        ?: return@Link out.setError(true).setErrorMessage("unknown upload").build()
                    if (c.offset != up.data.size().toLong()) {
                        return@Link out.setError(true).setErrorMessage("out of order").build()
                    }
                    up.data.write(c.data.toByteArray())
                    chunksTaken++
                    if (dropAfter.remove(chunksTaken)) drop()
                    out.setRespUploadProgress(UploadProgress.newBuilder().setReceived(up.data.size().toLong()))
                }
                ReqEnvelope.PayloadCase.REQ_FINISH_UPLOAD -> {
                    val f = env.reqFinishUpload
                    val up = uploads.values.firstOrNull { it.id == f.uploadId }
                        ?: return@Link out.setError(true).setErrorMessage("unknown upload").build()
                    val bytes = up.data.toByteArray()
                    val hash = MessageDigest.getInstance("SHA-256").digest(bytes).joinToString("") { "%02x".format(it) }
                    if (hash != f.sha256 || bytes.size.toLong() != up.size) {
                        return@Link out.setError(true).setErrorMessage("content doesn't match its SHA-256").build()
                    }
                    uploads.remove(up.key)
                    val path = up.key.substringBefore('|')
                    stored[path] = bytes
                    out.setRespFile(File.newBuilder().setPath(path).setHash(hash))
                }
                else -> out.setError(true).setErrorMessage("unknown payload")
            }
            out.build()
        }
    }

    private class Run(
        val pauses: MutableList<Int> = mutableListOf(),
        val sent: MutableList<Long> = mutableListOf(),
        /** What the caller answers when asked whether a cut goes on. */
        var mayResume: Boolean = true,
    )

    private fun upload(device: Device, bytes: ByteArray, opens: Opens, run: Run = Run(), path: String = "/android/x/v.mp4"): RespEnvelope = runBlocking {
        ChunkedUpload.send(
            path, bytes.size.toLong(), opens::open, forceOverride = false, created = null, modified = null, cloudId = null,
            sha256 = sha(bytes), onSent = { run.sent += it }, link = device.link, pause = { run.pauses += it },
            mayResume = { run.mayResume }, log = {},
        )
    }

    @Test fun beginUploadNamesTheContent() {
        val bytes = content(chunk + 10)
        val device = Device()
        val resp = upload(device, bytes, Opens(bytes))
        assertEquals(RespEnvelope.PayloadCase.RESP_FILE, resp.payloadCase)
        val begin = device.begins.single()
        assertEquals(sha(bytes), begin.sha256)
        assertEquals(bytes.size.toLong(), begin.size)
        assertEquals("/android/x/v.mp4", begin.path)
        assertArrayEquals(bytes, device.stored["/android/x/v.mp4"])
        assertEquals(listOf(0L, chunk.toLong()), device.offsets)
    }

    /** The device already holds part of it (a run cut short before): the chunks start there, the part before skipped, never read. */
    @Test fun receivedAboveZeroGoesOnFromThere() {
        val bytes = content(3 * chunk + 3)
        val device = Device()
        val held = chunk + 5
        device.holds("/android/x/v.mp4", bytes.size.toLong(), sha(bytes), bytes.copyOf(held))
        val opens = Opens(bytes)
        val run = Run()
        upload(device, bytes, opens, run)
        assertArrayEquals(bytes, device.stored["/android/x/v.mp4"])
        assertEquals(listOf(held.toLong(), held.toLong() + chunk), device.offsets)
        val s = opens.streams.single()
        assertEquals(held.toLong(), s.skipped)
        assertEquals((bytes.size - held).toLong(), s.read)
        assertEquals(held.toLong(), run.sent.first())
        assertEquals(bytes.size.toLong(), run.sent.last())
        assertTrue(run.pauses.isEmpty())
    }

    /** All of it there already (FinishUpload's answer lost last time): straight to FinishUpload. */
    @Test fun everythingReceivedGoesStraightToFinish() {
        val bytes = content(chunk + 1)
        val device = Device()
        device.holds("/android/x/v.mp4", bytes.size.toLong(), sha(bytes), bytes)
        val opens = Opens(bytes)
        upload(device, bytes, opens)
        assertTrue(device.offsets.isEmpty())
        // Nothing read out of the file (the look for more finds its end).
        assertEquals(0L, opens.streams.single().read)
        assertEquals(bytes.size.toLong(), opens.streams.single().skipped)
        assertArrayEquals(bytes, device.stored["/android/x/v.mp4"])
    }

    /** The connection goes after the second chunk reached the device: begun again with the same key, it goes on from the third. */
    @Test fun aDropMidFileResumesWhereTheDeviceIs() {
        val bytes = content(3 * chunk + 100)
        val device = Device(dropAfter = mutableSetOf(2))
        val opens = Opens(bytes)
        val run = Run()
        upload(device, bytes, opens, run)
        assertArrayEquals(bytes, device.stored["/android/x/v.mp4"])
        assertEquals(2, device.begins.size)
        assertEquals(device.begins[0], device.begins[1])
        // No chunk sent twice: the second stream starts where the device was.
        assertEquals(listOf(0L, chunk.toLong(), 2L * chunk, 3L * chunk), device.offsets)
        assertEquals(2, opens.streams.size)
        assertEquals(2L * chunk, opens.streams[1].skipped)
        assertEquals((bytes.size - 2 * chunk).toLong(), opens.streams[1].read)
        assertEquals(listOf(1), run.pauses)
    }

    /** An older device answers 0 and forgot the upload with its connection: the file goes again from the start. */
    @Test fun anOldDeviceAnsweringZeroStartsOver() {
        val bytes = content(3 * chunk)
        val device = Device(resumes = false, dropAfter = mutableSetOf(2))
        val opens = Opens(bytes)
        val run = Run()
        upload(device, bytes, opens, run)
        assertArrayEquals(bytes, device.stored["/android/x/v.mp4"])
        assertEquals(listOf(0L, chunk.toLong(), 0L, chunk.toLong(), 2L * chunk), device.offsets)
        assertEquals(0L, opens.streams[1].skipped)
        assertEquals(listOf(1), run.pauses)
    }

    /** Cuts that keep coming with nothing gained give up after MAX_RESUMES, with the last error, for the sync's retry list. */
    @Test fun cutsWithNothingGainedGiveUp() {
        val bytes = content(2 * chunk)
        // An older device whose connection drops at every first chunk: it never gets further.
        val device = Device(resumes = false, dropAfter = mutableSetOf(1, 2, 3, 4, 5, 6, 7, 8))
        val run = Run()
        try {
            upload(device, bytes, Opens(bytes), run)
            fail("expected the cut to be thrown")
        } catch (e: IOException) {
            assertEquals("connection dropped", e.message)
        }
        assertEquals(ChunkedUpload.MAX_RESUMES + 1, device.begins.size)
        assertEquals((1..ChunkedUpload.MAX_RESUMES).toList(), run.pauses)
        assertTrue(device.stored.isEmpty())
    }

    /** Each cut that came after the device got further is a new count: a long file over a flaky link gets there. */
    @Test fun cutsAfterProgressKeepGoing() {
        val bytes = content(8 * chunk)
        val device = Device(dropAfter = (1..7).toMutableSet())
        val run = Run()
        upload(device, bytes, Opens(bytes), run)
        assertArrayEquals(bytes, device.stored["/android/x/v.mp4"])
        assertEquals(8, device.begins.size)
        // Each one resumed the moment after the device had more than before.
        assertEquals(List(7) { 1 }, run.pauses)
        assertEquals((0 until 8).map { it.toLong() * chunk }, device.offsets)
    }

    /** No network yet when it is begun again: another try after a longer pause, the same key. */
    @Test fun aBeginThatFailsWhileResumingIsAnotherTry() {
        val bytes = content(2 * chunk)
        val device = Device(dropAfter = mutableSetOf(1)).apply { beginDrops = 2 }
        val run = Run()
        upload(device, bytes, Opens(bytes), run)
        assertArrayEquals(bytes, device.stored["/android/x/v.mp4"])
        assertEquals(4, device.begins.size)
        assertEquals(listOf(1, 2, 3), run.pauses)
    }

    /** The caller says not now after the pause (the sync's Wi-Fi only, the phone on mobile data): the cut is the caller's, not resumed. */
    @Test fun aCutTheCallerHoldsIsNotResumed() {
        val bytes = content(2 * chunk)
        val device = Device(dropAfter = mutableSetOf(1))
        val run = Run(mayResume = false)
        try {
            upload(device, bytes, Opens(bytes), run)
            fail("expected the cut to be thrown")
        } catch (e: IOException) {
            assertEquals("connection dropped", e.message)
        }
        assertEquals(1, device.begins.size)
        assertEquals(listOf(1), run.pauses)
        // What the device took is kept for the next run, which resumes there.
        val again = Run()
        upload(device, bytes, Opens(bytes), again)
        assertArrayEquals(bytes, device.stored["/android/x/v.mp4"])
        assertEquals(listOf(0L, chunk.toLong()), device.offsets)
        assertEquals(chunk.toLong(), again.sent.first())
    }

    /** The device losing its place answers an error rather than a drop: resumed from what it says it holds. */
    @Test fun aRefusedChunkResumesFromTheDevice() {
        val bytes = content(2 * chunk)
        val device = Device()
        device.chunkAnswer = RespEnvelope.newBuilder().setError(true).setErrorMessage("unknown upload").build()
        val run = Run()
        upload(device, bytes, Opens(bytes), run)
        assertArrayEquals(bytes, device.stored["/android/x/v.mp4"])
        assertEquals(2, device.begins.size)
        assertEquals(listOf(1), run.pauses)
    }

    /** storage_full is the device's answer, not a cut: thrown at once with its code (the sync stops on it). */
    @Test fun storageFullIsNotResumed() {
        val bytes = content(2 * chunk)
        val device = Device()
        device.chunkAnswer = RespEnvelope.newBuilder().setError(true).setErrorCode(ErrorCodes.STORAGE_FULL).setErrorMessage("full").build()
        val run = Run()
        try {
            upload(device, bytes, Opens(bytes), run)
            fail("expected storage_full")
        } catch (e: OTCConnection.RequestError) {
            assertEquals(ErrorCodes.STORAGE_FULL, e.code)
        }
        assertEquals(1, device.begins.size)
        assertTrue(run.pauses.isEmpty())
    }

    /** The first BeginUpload failing is the caller's, as before: no pause, no second try. */
    @Test fun theFirstBeginFailsAsBefore() {
        val bytes = content(10)
        val link = ChunkedUpload.Link { throw IOException("offline") }
        val pauses = mutableListOf<Int>()
        try {
            runBlocking {
                ChunkedUpload.send(
                    "/a", 10, { bytes.inputStream() }, false, null, null, null, sha(bytes), null,
                    link = link, pause = { pauses += it }, log = {},
                )
            }
            fail("expected the failure")
        } catch (e: IOException) {
            assertEquals("offline", e.message)
        }
        assertTrue(pauses.isEmpty())
    }

    /** FinishUpload still checks the whole content: one that doesn't match is refused, not resumed. */
    @Test fun finishStillChecksTheWholeContent() {
        val bytes = content(chunk + 1)
        val device = Device()
        // What the device holds of it isn't this file's start.
        device.holds("/android/x/v.mp4", bytes.size.toLong(), sha(bytes), ByteArray(chunk))
        try {
            upload(device, bytes, Opens(bytes))
            fail("expected the hash check to refuse it")
        } catch (e: OTCConnection.RequestError) {
            assertTrue(e.message!!.contains("SHA-256"))
        }
        assertFalse(device.stored.containsKey("/android/x/v.mp4"))
    }

    /** A stream that skips nothing still gets to the offset, a byte at a time. */
    @Test fun skipFullyFallsBackToReading() {
        val s = Source(content(100)).apply { skipWorks = false }
        ChunkedUpload.skipFully(s, 40)
        assertEquals(40, s.pos)
        val t = Source(content(100))
        ChunkedUpload.skipFully(t, 40)
        assertEquals(40L, t.skipped)
        assertEquals(0L, t.read)
        try {
            ChunkedUpload.skipFully(Source(content(10)).apply { skipWorks = false }, 20)
            fail("expected the end of the file")
        } catch (e: IOException) {
            assertTrue(e.message!!.contains("ended"))
        }
    }

    @Test fun resumePausesDouble() {
        assertEquals(listOf(2000L, 4000L, 8000L, 16000L), (1..ChunkedUpload.MAX_RESUMES).map { ChunkedUpload.resumePauseMs(it) })
    }
}
