// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.proto.BeginUpload
import cloud.offthe.otc.proto.FinishUpload
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.UploadChunk
import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp
import java.io.IOException
import java.io.InputStream
import java.security.MessageDigest

// Issue #165: a whole-file UploadFile made the device hold the whole file
// (a phone video can be GBs) in one websocket message. Every upload goes
// through here instead: BeginUpload, the content in 4 MiB UploadChunks read
// straight from the source, then FinishUpload with its SHA-256 - so neither
// side ever has more than one chunk in memory.
object ChunkedUpload {
    const val chunkSize = 4 shl 20

    /** A source's SHA-256 (hex) and byte count, read in [chunkSize] pieces. */
    data class Digest(val sha256: String, val size: Long)

    fun digest(open: () -> InputStream): Digest {
        val md = MessageDigest.getInstance("SHA-256")
        val buf = ByteArray(chunkSize)
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

    /**
     * Uploads [size] bytes from [open] to [path]; answers with FinishUpload's
     * response (a RESP_FILE, like UploadFile's). [sha256] is the content's hash
     * when already computed (for HasFile), else it is hashed while sending.
     * Any error answer throws - the caller's retry (the sync pass) handles it.
     */
    suspend fun upload(
        path: String,
        size: Long,
        open: () -> InputStream,
        forceOverride: Boolean = false,
        created: Timestamp? = null,
        modified: Timestamp? = null,
        cloudId: String? = null,
        sha256: String? = null,
    ): RespEnvelope {
        val begin = BeginUpload.newBuilder().setPath(path).setSize(size).setForceOverride(forceOverride)
        created?.let { begin.setCreated(it) }
        modified?.let { begin.setModified(it) }
        if (!cloudId.isNullOrEmpty()) begin.setCloudId(cloudId)
        val started = OTCConnection.request { it.setReqBeginUpload(begin) }
        check(started, RespEnvelope.PayloadCase.RESP_UPLOAD_STARTED, "BeginUpload")
        val uploadId = started.respUploadStarted.uploadId

        val md = if (sha256 == null) MessageDigest.getInstance("SHA-256") else null
        val buf = ByteArray(chunkSize)
        var offset = 0L
        open().use { input ->
            while (true) {
                // Fill the whole chunk (a stream may return less per read).
                var n = 0
                while (n < chunkSize) {
                    val r = input.read(buf, n, chunkSize - n)
                    if (r < 0) break
                    n += r
                }
                if (n == 0) break
                if (offset + n > size) throw IOException("$path grew past $size bytes while uploading")
                md?.update(buf, 0, n)
                val at = offset
                val progress = OTCConnection.request {
                    it.setReqUploadChunk(UploadChunk.newBuilder().setUploadId(uploadId).setOffset(at).setData(ByteString.copyFrom(buf, 0, n)))
                }
                check(progress, RespEnvelope.PayloadCase.RESP_UPLOAD_PROGRESS, "UploadChunk")
                if (progress.respUploadProgress.received != offset + n) {
                    throw IOException("UploadChunk: device has ${progress.respUploadProgress.received} bytes, expected ${offset + n}")
                }
                offset += n
                if (n < chunkSize) break
            }
        }
        if (offset != size) throw IOException("$path is $offset bytes, expected $size")

        val hash = sha256 ?: hex(md!!.digest())
        val resp = OTCConnection.request { it.setReqFinishUpload(FinishUpload.newBuilder().setUploadId(uploadId).setSha256(hash)) }
        if (resp.error) throw OTCConnection.RequestError(resp.errorMessage.ifEmpty { "FinishUpload failed" })
        return resp
    }

    private fun check(resp: RespEnvelope, want: RespEnvelope.PayloadCase, what: String) {
        if (resp.error) throw OTCConnection.RequestError(resp.errorMessage.ifEmpty { "$what failed" })
        if (resp.payloadCase != want) throw OTCConnection.RequestError("$what: unexpected ${resp.payloadCase}")
    }

    private fun hex(bytes: ByteArray): String = bytes.joinToString("") { "%02x".format(it) }
}
