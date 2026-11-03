// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import android.util.Log
import cloud.offthe.otc.proto.BeginUpload
import cloud.offthe.otc.proto.FinishUpload
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.UploadChunk
import com.google.protobuf.Timestamp
import com.google.protobuf.UnsafeByteOperations
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import java.io.IOException
import java.io.InputStream
import java.security.MessageDigest

// Issue #165: a whole-file UploadFile made the device hold the whole file
// (a phone video can be GBs) in one websocket message. Every upload goes
// through here instead: BeginUpload, the content in 4 MiB UploadChunks read
// straight from the source, then FinishUpload with its SHA-256 - so neither
// side ever has more than one chunk in memory.
//
// Issue #205: BeginUpload names the content (sha256: with the path and size,
// the resume key), and the device answers with what it already holds of an
// unfinished upload of that key (UploadStarted.received, 0 for a new one and
// from every device before release 166). The chunks start there: the bytes
// before it are skipped in the stream, never read out to be sent again. A
// connection that drops mid-file (a phone moving between Wi-Fi and mobile
// data, a bridge node restarting) no longer starts the file over: the upload
// is begun again with the same key and goes on from what the device has. As
// iOS's ChunkedUpload.swift.
object ChunkedUpload {
    const val chunkSize = 4 shl 20

    // OkHttp closes the socket (1001) once more than 16 MiB is queued and
    // not yet written, failing every request on it. Three 4 MiB chunks in
    // flight keep the queue under that whatever mix of sync, composer and
    // Files uploads is running; a permit is held until the device answers.
    private const val maxChunksInFlight = 3
    private val chunkPermits = Semaphore(maxChunksInFlight)

    /**
     * Issue #205: how many times in a row an upload cut off mid-file is
     * begun again without the device getting further than it ever had - an
     * older device starting it over from 0 has to pass that point again
     * before a cut counts as new - and the pause before each (2, 4, 8, 16 s:
     * a phone that changed networks, or a bridge node restarting, is back
     * within that). After that the error is the caller's, as before: the
     * sync's retry list, where the next run resumes it.
     */
    const val MAX_RESUMES = 4

    fun resumePauseMs(attempt: Int): Long = 1000L shl attempt.coerceIn(1, MAX_RESUMES)

    private const val tag = "OTC/ChunkedUpload"

    /** A source's SHA-256 (hex) and byte count, read as a stream. */
    data class Digest(val sha256: String, val size: Long)

    fun digest(open: () -> InputStream): Digest {
        val md = MessageDigest.getInstance("SHA-256")
        // Hashing is no faster with a bigger buffer, and several uploads
        // may be hashing at once.
        val buf = ByteArray(256 shl 10)
        var size = 0L
        open().use { input ->
            while (true) {
                val n = input.read(buf)
                if (n < 0) break
                md.update(buf, 0, n); size += n
            }
        }
        return Digest(hex(md.digest()), size)
    }

    /** What an upload needs from the connection: OTCConnection's request in the app, a stand-in device in tests. */
    fun interface Link {
        suspend fun request(build: (ReqEnvelope.Builder) -> Unit): RespEnvelope
    }

    /**
     * Uploads [size] bytes from [open] to [path]; answers with FinishUpload's
     * response (a RESP_FILE, like UploadFile's). [sha256] is the content's
     * hash - every caller hashes first, for HasFile - and the resume key.
     * [onSent]: the bytes of it the device holds, as they grow (from where a
     * resumed upload starts). [mayResume]: asked after a cut, once its pause
     * is over - false leaves the cut to the caller (the photo sync's "Wi-Fi
     * only" on mobile data: a phone that left its Wi-Fi mid-file doesn't
     * finish it over mobile data). Any error answer throws, with the
     * device's message and error code - the caller's retry (the sync pass)
     * handles it.
     */
    suspend fun upload(
        path: String,
        size: Long,
        open: () -> InputStream,
        forceOverride: Boolean = false,
        created: Timestamp? = null,
        modified: Timestamp? = null,
        cloudId: String? = null,
        sha256: String,
        onSent: ((Long) -> Unit)? = null,
        mayResume: (() -> Boolean)? = null,
    ): RespEnvelope = OTCConnection.transfer {
        send(
            path, size, open, forceOverride, created, modified, cloudId, sha256, onSent,
            link = { build -> OTCConnection.request(build = build) },
            pause = { delay(resumePauseMs(it)) },
            mayResume = mayResume,
            log = { Log.i(tag, it) },
        )
    }

    /** [upload] over [link]: the protocol, resuming included. [pause] waits before resume [n]; [log] gets lines without paths (A19). */
    internal suspend fun send(
        path: String,
        size: Long,
        open: () -> InputStream,
        forceOverride: Boolean,
        created: Timestamp?,
        modified: Timestamp?,
        cloudId: String?,
        sha256: String,
        onSent: ((Long) -> Unit)?,
        link: Link,
        pause: suspend (Int) -> Unit,
        mayResume: (() -> Boolean)? = null,
        log: (String) -> Unit,
    ): RespEnvelope {
        val begin = BeginUpload.newBuilder().setPath(path).setSize(size).setForceOverride(forceOverride).setSha256(sha256)
        created?.let { begin.setCreated(it) }
        modified?.let { begin.setModified(it) }
        if (!cloudId.isNullOrEmpty()) begin.setCloudId(cloudId)

        // The most the device ever said it held, and the resumes since it last grew.
        var furthest = -1L
        var resumes = 0
        var first = true

        // After a cut: the pause, then BeginUpload again - unless that was
        // the last try, or the caller says not now; then [cause] is its.
        suspend fun again(cause: Exception, why: String) {
            if (++resumes > MAX_RESUMES) throw cause
            log("$why (${what(cause)}): resuming, try $resumes") // i18n-ignore: a log line
            pause(resumes)
            if (mayResume?.invoke() == false) throw cause
        }

        while (true) {
            // The first BeginUpload's failures are the caller's, as always.
            // After a cut, one that doesn't get through is another try - not
            // the device saying it is full.
            val started = try {
                link.request { it.setReqBeginUpload(begin) }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                if (first) throw e
                again(e, "BeginUpload failed") // i18n-ignore: a log line
                continue
            }
            if (started.error || started.payloadCase != RespEnvelope.PayloadCase.RESP_UPLOAD_STARTED) {
                val refusal = refusal(started, "BeginUpload")
                if (first || ErrorCodes.isStorageFull(refusal.code)) throw refusal
                again(refusal, "BeginUpload refused") // i18n-ignore: a log line
                continue
            }
            first = false
            val uploadId = started.respUploadStarted.uploadId
            var offset = started.respUploadStarted.received
            // No path in these: they end up in the log (A19).
            if (offset < 0 || offset > size) throw IOException("BeginUpload: device has $offset bytes of $size") // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"
            if (offset > furthest) { furthest = offset; resumes = 0 }
            if (offset > 0) log("resuming at $offset of $size bytes") // i18n-ignore: a log line
            onSent?.invoke(offset)

            // Why the chunks stopped short: the connection, or the device
            // losing its place - both resumed from what it holds.
            var cut: Exception? = null
            open().use { input ->
                skipFully(input, offset)
                while (offset < size) {
                    val want = minOf(chunkSize.toLong(), size - offset).toInt()
                    // A fresh array per chunk, wrapped rather than copied into
                    // the message: nothing writes to it once it is sent.
                    val buf = ByteArray(want)
                    // Fill the whole chunk (a stream may return less per read).
                    var n = 0
                    while (n < want) {
                        val r = input.read(buf, n, want - n)
                        if (r < 0) break
                        n += r
                    }
                    if (n < want) throw IOException("the file is ${offset + n} bytes, expected $size") // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"
                    val at = offset
                    val data = UnsafeByteOperations.unsafeWrap(buf, 0, n)
                    val progress = try {
                        chunkPermits.withPermit {
                            link.request {
                                it.setReqUploadChunk(UploadChunk.newBuilder().setUploadId(uploadId).setOffset(at).setData(data))
                            }
                        }
                    } catch (e: CancellationException) {
                        throw e
                    } catch (e: Exception) {
                        cut = e
                        break
                    }
                    if (progress.error || progress.payloadCase != RespEnvelope.PayloadCase.RESP_UPLOAD_PROGRESS) {
                        val refusal = refusal(progress, "UploadChunk")
                        if (ErrorCodes.isStorageFull(refusal.code)) throw refusal
                        cut = refusal
                        break
                    }
                    if (progress.respUploadProgress.received != at + n) {
                        cut = IOException("UploadChunk: device has ${progress.respUploadProgress.received} bytes, expected ${at + n}") // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"
                        break
                    }
                    offset = at + n
                    if (offset > furthest) { furthest = offset; resumes = 0 }
                    onSent?.invoke(offset)
                }
                if (cut == null && input.read() >= 0) throw IOException("the file grew past $size bytes while uploading") // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"
            }
            val stopped = cut
            if (stopped != null) {
                again(stopped, "upload cut off at $offset of $size bytes") // i18n-ignore: a log line
                continue
            }

            // Not resumed when it fails: the device may have made it the file
            // already. The caller's next try finds it by its hash, or resumes.
            val resp = link.request { it.setReqFinishUpload(FinishUpload.newBuilder().setUploadId(uploadId).setSha256(sha256)) }
            if (resp.error) throw OTCConnection.RequestError(resp.errorMessage.ifEmpty { "FinishUpload failed" }, resp.errorCode) // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"
            return resp
        }
    }

    /**
     * Moves [input] on by [n] bytes without reading them out where the
     * stream can (a file's skip is a seek). A stream that skips nothing
     * before its end gives one byte at a time instead.
     */
    internal fun skipFully(input: InputStream, n: Long) {
        var left = n
        while (left > 0) {
            val skipped = input.skip(left)
            if (skipped > 0) {
                left -= skipped
                continue
            }
            if (input.read() < 0) throw IOException("the file ended before byte $n") // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"
            left--
        }
    }

    /** A failure as the log has it (A19): the device's code, or the exception's kind - never its words, which may name a host. */
    private fun what(e: Throwable): String = if (e is OTCConnection.RequestError) e.toString() else e.javaClass.simpleName

    private fun refusal(resp: RespEnvelope, what: String): OTCConnection.RequestError =
        if (resp.error) OTCConnection.RequestError(resp.errorMessage.ifEmpty { "$what failed" }, resp.errorCode) // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"
        else OTCConnection.RequestError("$what: unexpected ${resp.payloadCase}") // i18n-ignore: a diagnostic naming the upload protocol, shown only after a translated "Upload failed:"

    private fun hex(bytes: ByteArray): String = bytes.joinToString("") { "%02x".format(it) }
}
