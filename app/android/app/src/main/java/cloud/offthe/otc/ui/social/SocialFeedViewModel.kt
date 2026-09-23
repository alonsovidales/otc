// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.social

import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.Comment
import cloud.offthe.otc.proto.DelSocialComment
import cloud.offthe.otc.proto.DelSocialPublication
import cloud.offthe.otc.proto.GetCommentLikers
import cloud.offthe.otc.proto.GetPublicationLikers
import cloud.offthe.otc.proto.GetSocialPublications
import cloud.offthe.otc.proto.LikeComment
import cloud.offthe.otc.proto.LikePublication
import cloud.offthe.otc.proto.NewSocialComment
import cloud.offthe.otc.proto.Profile
import cloud.offthe.otc.proto.ReqGetPublication
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SocialPublication
import cloud.offthe.otc.proto.SocialPublications
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.util.UUID

// Port of SocialFeedViewModel (SocialFeedView.swift): a shared singleton,
// paged 4 at a time (issue #15), optimistic likes/comments (issue #17), a
// launch-time snapshot of the first page on disk (issue #22), and the
// auto-load that keeps retrying until the device answers (issue #11).
object SocialFeedViewModel {
    private const val pageSize = 4
    private const val maxCachedFeedBytes = 4 shl 20

    data class State(
        val posts: List<SocialPublication> = emptyList(),
        val loading: Boolean = true,
        val loadingMore: Boolean = false,
        val hasLoadedOnce: Boolean = false,
        val scrollTargetPub: String? = null,
        val highlightPub: String? = null,
        val highlightComment: String? = null,
    )

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state
    private var endReached = false
    private var pollJob: Job? = null
    private val cacheFile get() = File(OTCApp.instance.cacheDir, "social_feed_cache.pb")

    init {
        scope.launch { loadCachedPosts() }
        startAutoLoad()
    }

    private suspend fun loadCachedPosts() {
        val cached = try {
            val f = cacheFile
            if (!f.exists() || f.length() > maxCachedFeedBytes) emptyList()
            else SocialPublications.parseFrom(f.readBytes()).publicationsList
        } catch (e: Exception) { emptyList() }
        _state.update { if (it.posts.isEmpty() && cached.isNotEmpty()) it.copy(posts = cached) else it }
    }

    private fun saveCachedPosts() {
        val snapshot = _state.value.posts.take(pageSize)
        scope.launch {
            try {
                cacheFile.writeBytes(SocialPublications.newBuilder().addAllPublications(snapshot).build().toByteArray())
            } catch (_: Exception) {}
        }
    }

    private fun startAutoLoad() {
        if (pollJob != null) return
        pollJob = scope.launch {
            while (isActive) {
                if (loadFeed()) { _state.update { it.copy(hasLoadedOnce = true) }; break }
                delay(4_000)
            }
            pollJob = null
        }
    }

    suspend fun loadFeed(): Boolean {
        _state.update { it.copy(loading = true) }
        try {
            endReached = false
            val count = maxOf(_state.value.posts.size, pageSize)
            return fetchPage(count, emptyList(), replacing = true)
        } finally {
            _state.update { it.copy(loading = false) }
        }
    }

    suspend fun loadMoreIfNeeded(post: SocialPublication?) {
        val st = _state.value
        if (post == null || st.loading || st.loadingMore || endReached) return
        val idx = st.posts.indexOfFirst { it.uuid == post.uuid }
        if (idx < 0 || idx < st.posts.size - 2) return
        _state.update { it.copy(loadingMore = true) }
        try { fetchPage(pageSize, st.posts.map { it.uuid }, replacing = false) }
        finally { _state.update { it.copy(loadingMore = false) } }
    }

    private suspend fun refreshCurrentlyLoaded() {
        fetchPage(maxOf(_state.value.posts.size, pageSize), emptyList(), replacing = true)
    }

    private suspend fun fetchPage(total: Int, excludeUuids: List<String>, replacing: Boolean): Boolean = try {
        val resp = OTCConnection.request {
            it.setReqGetSocialPublications(GetSocialPublications.newBuilder().setTotal(total).addAllExcludeUuids(excludeUuids))
        }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_SOCIAL_PUBLICATIONS) {
            val sp = resp.respSocialPublications.publicationsList
            _state.update { st ->
                if (replacing) st.copy(posts = sp) else {
                    val existing = st.posts.map { it.uuid }.toSet()
                    st.copy(posts = st.posts + sp.filter { it.uuid !in existing })
                }
            }
            if (sp.size < total) endReached = true
            saveCachedPosts()
        }
        true
    } catch (e: Exception) {
        false
    }

    private fun mutatePost(uuid: String, f: (SocialPublication.Builder) -> Unit) = _state.update { st ->
        st.copy(posts = st.posts.map { p -> if (p.uuid == uuid) p.toBuilder().also(f).build() else p })
    }

    suspend fun likePublication(uuid: String) {
        val post = _state.value.posts.firstOrNull { it.uuid == uuid } ?: return
        val wasLiked = post.liked
        mutatePost(uuid) { it.liked = !wasLiked; it.likes = it.likes + if (wasLiked) -1 else 1 }
        val ok = try {
            val resp = OTCConnection.request { it.setReqLikePublication(LikePublication.newBuilder().setPubUuid(uuid)) }
            !(resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && !resp.respAck.ok)
        } catch (e: Exception) { false }
        if (!ok) mutatePost(uuid) { it.liked = wasLiked; it.likes = it.likes + if (wasLiked) 1 else -1 }
    }

    private fun mutateComment(commentUuid: String, f: (Comment.Builder) -> Unit) = _state.update { st ->
        st.copy(posts = st.posts.map { p ->
            if (p.commentsList.none { it.commentUuid == commentUuid }) p else {
                val b = p.toBuilder()
                val comments = p.commentsList.map { c -> if (c.commentUuid == commentUuid) c.toBuilder().also(f).build() else c }
                b.clearComments().addAllComments(comments).build()
            }
        })
    }

    suspend fun likeComment(uuid: String) {
        val c = _state.value.posts.flatMap { it.commentsList }.firstOrNull { it.commentUuid == uuid } ?: return
        val wasLiked = c.liked
        mutateComment(uuid) { it.liked = !wasLiked; it.likes = it.likes + if (wasLiked) -1 else 1 }
        val ok = try {
            val resp = OTCConnection.request { it.setReqLikeComment(LikeComment.newBuilder().setCommentUuid(uuid)) }
            !(resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && !resp.respAck.ok)
        } catch (e: Exception) { false }
        if (!ok) mutateComment(uuid) { it.liked = wasLiked; it.likes = it.likes + if (wasLiked) 1 else -1 }
    }

    suspend fun addComment(pubUuid: String, text: String) {
        val trimmed = text.trim()
        if (trimmed.isEmpty() || _state.value.posts.none { it.uuid == pubUuid }) return
        val optimistic = Comment.newBuilder().setPubUuid(pubUuid).setCommentUuid("pending-${UUID.randomUUID()}").setComment(trimmed).setPublisher("me").build()
        mutatePost(pubUuid) { it.addComments(optimistic) }
        val ok = try {
            val resp = OTCConnection.request { it.setReqNewSocialComment(NewSocialComment.newBuilder().setPubUuid(pubUuid).setComment(trimmed).setPublisher("me")) }
            resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.ok
        } catch (e: Exception) { false }
        if (ok) refreshCurrentlyLoaded()
        else mutatePost(pubUuid) { b ->
            val kept = b.commentsList.filter { it.commentUuid != optimistic.commentUuid }
            b.clearComments().addAllComments(kept)
        }
    }

    suspend fun fetchPublicationLikers(pubUuid: String): List<Profile> = try {
        val resp = OTCConnection.request { it.setReqGetPublicationLikers(GetPublicationLikers.newBuilder().setPubUuid(pubUuid)) }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIKERS) resp.respLikers.likersList else emptyList()
    } catch (e: Exception) { emptyList() }

    suspend fun fetchCommentLikers(commentUuid: String): List<Profile> = try {
        val resp = OTCConnection.request { it.setReqGetCommentLikers(GetCommentLikers.newBuilder().setCommentUuid(commentUuid)) }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_LIKERS) resp.respLikers.likersList else emptyList()
    } catch (e: Exception) { emptyList() }

    suspend fun deletePublication(pubUuid: String) {
        try {
            val resp = OTCConnection.request { it.setReqDelSocialPublication(DelSocialPublication.newBuilder().setPubUuid(pubUuid)) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.ok) {
                _state.update { st -> st.copy(posts = st.posts.filter { it.uuid != pubUuid }) }
                saveCachedPosts()
            }
        } catch (_: Exception) {}
    }

    suspend fun deleteComment(commentUuid: String) {
        try {
            val resp = OTCConnection.request { it.setReqDelSocialComment(DelSocialComment.newBuilder().setCommentUuid(commentUuid)) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.ok) {
                _state.update { st ->
                    st.copy(posts = st.posts.map { p ->
                        val b = p.toBuilder(); val kept = p.commentsList.filter { it.commentUuid != commentUuid }
                        b.clearComments().addAllComments(kept).build()
                    })
                }
                saveCachedPosts()
            }
        } catch (_: Exception) {}
    }

    /** Issue #78: open a post from a tapped notification. */
    suspend fun openPost(pubUuid: String, commentUuid: String?) {
        if (_state.value.posts.none { it.uuid == pubUuid }) {
            try {
                val resp = OTCConnection.request { it.setReqGetPublication(ReqGetPublication.newBuilder().setPubUuid(pubUuid)) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_PUBLICATION && resp.respPublication.hasPublication()) {
                    val p = resp.respPublication.publication
                    _state.update { st -> if (st.posts.any { it.uuid == p.uuid }) st else st.copy(posts = listOf(p) + st.posts) }
                }
            } catch (e: Exception) { return }
        }
        _state.update { it.copy(scrollTargetPub = pubUuid, highlightPub = pubUuid, highlightComment = commentUuid) }
    }

    fun consumeScrollTarget() {
        _state.update { it.copy(scrollTargetPub = null) }
        scope.launch { delay(2500); _state.update { it.copy(highlightPub = null, highlightComment = null) } }
    }
}
