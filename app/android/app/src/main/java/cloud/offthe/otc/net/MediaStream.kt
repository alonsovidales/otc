// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.ReqGetMediaURL
import cloud.offthe.otc.proto.RespEnvelope
import java.net.URI

// Port of MediaStream.swift (issue #110): a URL the player can stream from
// (HTTP range requests) instead of pulling the whole video over the socket.
// null is a normal answer meaning "fetch it the old way".
object MediaStream {
    suspend fun url(forPath: String): String? = url {
        it.setReqGetMediaUrl(ReqGetMediaURL.newBuilder().setPath(forPath))
    }

    suspend fun url(forPublication: String, hash: String): String? = url {
        it.setReqGetMediaUrl(ReqGetMediaURL.newBuilder().setPubUuid(forPublication).setHash(hash))
    }

    private suspend fun url(build: (ReqEnvelope.Builder) -> Unit): String? = try {
        val resp = OTCConnection.request(build)
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_MEDIA_URL && resp.respMediaUrl.url.isNotEmpty())
            absolute(resp.respMediaUrl.url) else null
    } catch (e: Exception) {
        null
    }

    @Volatile private var cachedBase: URI? = null
    // Bumped by reset(): a base worked out from the endpoint before it
    // changed is used once but not cached over the new one.
    private var generation = 0
    private val lock = Any()

    /**
     * Endpoint changed (OTCConnection.invalidate, which Log Out goes
     * through too): the next device may have another address, and a token
     * resolved against the old one would go there, maybe over plain HTTP.
     */
    fun reset() = synchronized(lock) { cachedBase = null; generation++ }

    /** The device answers with a path ("/media/<token>"); resolve it against the endpoint in use. */
    fun absolute(path: String): String? {
        val base = cachedBase ?: run {
            val gen = synchronized(lock) { generation }
            val ep = try { URI(SecretsStore.loadOrCreate().endpointURLString) } catch (e: Exception) { return null }
            val scheme = if (ep.scheme == "ws") "http" else "https"
            URI(scheme, null, ep.host, ep.port, "/", null, null).also { b ->
                synchronized(lock) { if (gen == generation) cachedBase = b }
            }
        }
        return base.resolve(path).toString()
    }
}
