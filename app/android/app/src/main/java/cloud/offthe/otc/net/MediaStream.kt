// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import android.content.Context
import androidx.annotation.OptIn
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.DefaultDataSource
import androidx.media3.datasource.okhttp.OkHttpDataSource
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
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
    // Issue #190: the device's own origins URLs were made for, with how to
    // reach each (pin, network). A player for one of them talks only to the
    // pinned certificate, whatever route is in use by the time it plays.
    private val pinned = HashMap<String, OTCConnection.Home>()

    /**
     * Endpoint changed (OTCConnection.invalidate, which Log Out goes
     * through too): the next device may have another address, and a token
     * resolved against the old one would go there, maybe over plain HTTP.
     */
    fun reset() = synchronized(lock) { cachedBase = null; generation++; pinned.clear() }

    /**
     * The device answers with a path ("/media/<token>"); resolve it against
     * the route in use: the device itself at home (issue #190), else the
     * endpoint.
     */
    fun absolute(path: String): String? {
        OTCConnection.home?.let { h ->
            synchronized(lock) { pinned[h.origin] = h }
            return URI(h.origin + "/").resolve(path).toString()
        }
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

    /**
     * A player for [url]: one on the device's own origin streams through
     * OkHttp pinned to its certificate; anything else (the bridge, a local
     * file) with ExoPlayer's own stack, as before.
     */
    @OptIn(UnstableApi::class)
    fun player(context: Context, url: String): ExoPlayer {
        val h = synchronized(lock) { pinned.entries.firstOrNull { url.startsWith(it.key + "/") }?.value }
            ?: return ExoPlayer.Builder(context).build()
        val http = OkHttpDataSource.Factory(HomeNetwork.client(h.pin, h.via))
        return ExoPlayer.Builder(context)
            .setMediaSourceFactory(DefaultMediaSourceFactory(DefaultDataSource.Factory(context, http)))
            .build()
    }
}
