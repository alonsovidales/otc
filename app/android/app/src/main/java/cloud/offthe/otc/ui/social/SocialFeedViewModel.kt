// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.social

import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.net.NetworkWatch
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.Wake
import cloud.offthe.otc.net.sleepOrWake
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
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.backOnline
import cloud.offthe.otc.ui.common.loadProblem
import kotlinx.coroutines.CancellationException
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
        // The last load of the feed failed, and why (null: it didn't): said
        // under "Loading…" until the first one comes.
        val problem: LoadProblem? = null,
        // The next page failed: the end of the feed says so, with Try again.
        val moreFailed: Boolean = false,
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
        // Signed in again, a network came up, back in the foreground: a
        // page that failed is asked for again (the first load's wait is cut
        // short by sleepOrWake).
        scope.launch {
            var seen = Wake.count.value
            Wake.count.collect { n ->
                if (n == seen) return@collect
                seen = n
                backOnline()
                if (_state.value.moreFailed) retryMore()
            }
        }
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

    /** Log Out: an empty feed, no cached snapshot, and the auto-load stopped. */
    fun reset() {
        pollJob?.cancel()
        pollJob = null
        endReached = false
        _state.value = State(loading = false)
        cacheFile.delete()
    }

    /** Also what a new sign-in calls (issue #133): Log Out cancels this, and
     *  `init` only ever ran once per process, so the feed after signing in
     *  again sat on "No social posts" until the app was killed. */
    fun startAutoLoad() {
        if (pollJob != null) return
        pollJob = scope.launch {
            while (isActive) {
                if (loadFeed()) { _state.update { it.copy(hasLoadedOnce = true) }; break }
                sleepOrWake(4_000)
            }
            pollJob = null
        }
    }

    // Back online: "This phone is offline" no longer says why the posts
    // aren't here while they are asked for again.
    private fun backOnline() {
        if (_state.value.problem != LoadProblem.OFFLINE) return
        val on = NetworkWatch.online()
        _state.update { it.copy(problem = it.problem?.backOnline(on)) }
    }

    suspend fun loadFeed(): Boolean {
        _state.update { it.copy(loading = true) }
        backOnline()
        try {
            endReached = false
            val count = maxOf(_state.value.posts.size, pageSize)
            val problem = fetchPage(count, emptyList(), replacing = true)
            _state.update { it.afterLoad(problem) }
            return problem == null
        } finally {
            _state.update { it.copy(loading = false) }
        }
    }

    suspend fun loadMoreIfNeeded(post: SocialPublication?) {
        val st = _state.value
        if (post == null || st.loading || st.loadingMore || endReached) return
        val idx = st.posts.indexOfFirst { it.uuid == post.uuid }
        if (idx < 0 || idx < st.posts.size - 2) return
        // Claimed in one step: a Wake's retryMore and the last post's own
        // effect can both get here, and only one asks.
        var mine = false
        _state.update { cur -> cur.claimMore().also { mine = it != null } ?: cur }
        if (!mine) return
        try {
            val failed = fetchPage(pageSize, st.posts.map { it.uuid }, replacing = false) != null
            _state.update { it.copy(moreFailed = failed) }
        } finally { _state.update { it.copy(loadingMore = false) } }
    }

    /** The end of the feed's Try again (and a Wake): the next page, asked for again. */
    fun retryMore() {
        scope.launch { loadMoreIfNeeded(_state.value.posts.lastOrNull()) }
    }

    private suspend fun refreshCurrentlyLoaded() {
        fetchPage(maxOf(_state.value.posts.size, pageSize), emptyList(), replacing = true)
    }

    // null once the page is in; otherwise why not. An answer that isn't the
    // feed (the bridge's "device unreachable", an error) is no page: the
    // first load used to take it for one, stop asking, and leave the feed
    // reading "No social posts".
    private suspend fun fetchPage(total: Int, excludeUuids: List<String>, replacing: Boolean): LoadProblem? {
        var resp: RespEnvelope? = null
        return try {
            // More time after a timeout, as Images' pages (OTCConnection.ask).
            resp = OTCConnection.ask("feed", OTCConnection.PAGE_TIMEOUT_MS) {
                it.setReqGetSocialPublications(GetSocialPublications.newBuilder().setTotal(total).addAllExcludeUuids(excludeUuids))
            }
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_SOCIAL_PUBLICATIONS) return problemOf(resp, null)
            val sp = resp.respSocialPublications.publicationsList
            _state.update { st ->
                if (replacing) st.copy(posts = sp) else {
                    val existing = st.posts.map { it.uuid }.toSet()
                    st.copy(posts = st.posts + sp.filter { it.uuid !in existing })
                }
            }
            if (sp.size < total) endReached = true
            saveCachedPosts()
            null
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            problemOf(resp, e)
        }
    }

    private fun problemOf(resp: RespEnvelope?, error: Throwable?) =
        loadProblem(resp, error, NetworkWatch.online(), OTCConnection.statusCode.value)

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

/**
 * The feed after a load of it: [problem] null when it came. A refresh that
 * worked also takes away "Couldn't load more posts" - the end of the feed
 * is a new one.
 */
internal fun SocialFeedViewModel.State.afterLoad(problem: LoadProblem?) =
    copy(problem = problem, hasLoadedOnce = hasLoadedOnce || problem == null, moreFailed = moreFailed && problem != null)

/** The next page, claimed: null when a load of the feed or of a page is already on its way. */
internal fun SocialFeedViewModel.State.claimMore(): SocialFeedViewModel.State? =
    if (loading || loadingMore) null else copy(loadingMore = true, moreFailed = false)
