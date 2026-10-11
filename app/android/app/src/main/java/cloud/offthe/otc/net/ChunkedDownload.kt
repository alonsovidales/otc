// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appFilesUnexpectedResponse
import cloud.offthe.otc.i18n.androidDownloadChanged
import cloud.offthe.otc.i18n.androidDownloadEmptyPiece
import cloud.offthe.otc.i18n.androidDownloadWrongPiece
import cloud.offthe.otc.proto.GetFile
import cloud.offthe.otc.proto.ReadFile
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.TextIOException
import cloud.offthe.otc.ui.common.now
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.io.OutputStream
import java.nio.file.Files
import java.nio.file.StandardCopyOption

// Issue #165's other half: a file opened, trimmed or played from the device
// came in one GetFile message - OkHttp's frame, its copy, the parsed proto
// and the bytes written: 3-4 times the file on the Java heap, and a big
// video or zip ended in an OutOfMemoryError (an Error, so the app died).
// ReadFile hands the original bytes over in ChunkedUpload.chunkSize pieces,
// written out as they arrive - the way otc-sync and the macOS app download.
// Not for photos shown as images: GetFile turns a HEIC into a JPEG.
object ChunkedDownload {
    /**
     * The device answered with an error, or with something else than the
     * file. Worded when read: the device's own [words], else the app's
     * "Unexpected response" in the language shown (the trimmer's alert
     * shows it after its own sentence) - as FileDownload.Refused on iOS.
     * Files shows its own "could not open" text for it instead.
     */
    class Refused(val words: String) : IOException(words) {
        val text: UiText get() = if (words.isEmpty()) S.appFilesUnexpectedResponse() else UiText.Raw(words)

        override val message: String get() = text.now()

        // What a log line prints: never the rendered text.
        override fun toString(): String = "Refused(empty=${words.isEmpty()})"
    }

    /** What the device says the file is. */
    data class Meta(val mime: String, val size: Long)

    /**
     * Writes the original bytes of [path] (its older version [hash], when
     * not empty) to [dest], through a ".part" file that only takes dest's
     * place once complete. [keepGoing] is asked after each piece; false
     * stops the download (CancellationException) and nothing is left.
     */
    suspend fun download(path: String, hash: String, dest: File, keepGoing: () -> Boolean = { true }): Meta = OTCConnection.transfer { downloadTo(path, hash, dest, keepGoing) }

    private suspend fun downloadTo(path: String, hash: String, dest: File, keepGoing: () -> Boolean): Meta = withContext(Dispatchers.IO) {
        val part = File(dest.parentFile, dest.name + ".part")
        var done = false
        try {
            val meta = FileOutputStream(part).use { out -> readInto(path, hash, out, keepGoing) }
            Files.move(part.toPath(), dest.toPath(), StandardCopyOption.REPLACE_EXISTING)
            done = true
            meta
        } finally {
            if (!done) part.delete()
        }
    }

    private suspend fun readInto(path: String, hash: String, out: OutputStream, keepGoing: () -> Boolean): Meta {
        var offset = 0L
        var size = -1L
        var mime = ""
        var fileHash: String? = null
        while (size < 0 || offset < size) {
            val at = offset
            val resp = OTCConnection.request {
                it.setReqReadFile(ReadFile.newBuilder().setPath(path).setHash(hash).setOffset(at).setLength(ChunkedUpload.chunkSize))
            }
            if (resp.error) {
                // A device from before ReadFile (v40): the whole file at once,
                // as before. Builds before v9 answer with no error code, only
                // the bare message.
                if (at == 0L && ErrorCodes.isUnknownPayload(resp.errorCode, resp.errorMessage)) return wholeFile(path, hash, out)
                throw Refused(resp.errorMessage)
            }
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_FILE_CHUNK) throw Refused("")
            val c = resp.respFileChunk
            // The current version can be replaced mid-download: never mix two files.
            if (fileHash == null) { fileHash = c.hash; mime = c.mime }
            else if (c.hash != fileHash) throw TextIOException(S.androidDownloadChanged())
            size = c.size
            val n = c.data.size()
            if (c.offset != at || at + n > size) throw TextIOException(S.androidDownloadWrongPiece())
            if (n == 0 && at < size) throw TextIOException(S.androidDownloadEmptyPiece())
            c.data.writeTo(out)
            offset += n
            if (!keepGoing()) throw CancellationException("the download is no longer wanted") // i18n-ignore: a cancellation, never shown
        }
        return Meta(mime, size.coerceAtLeast(0))
    }

    private suspend fun wholeFile(path: String, hash: String, out: OutputStream): Meta {
        val resp = OTCConnection.request { it.setReqGetFile(GetFile.newBuilder().setPath(path).setHash(hash)) }
        if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_FILE) throw Refused(resp.errorMessage)
        resp.respFile.content.writeTo(out)
        return Meta(resp.respFile.mime, resp.respFile.content.size().toLong())
    }
}
