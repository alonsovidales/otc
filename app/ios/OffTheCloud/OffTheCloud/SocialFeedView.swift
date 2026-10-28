// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SocialFeedView.swift
//  OffTheCloud
//
//  Native port of the web app's Social tab (web/src/components/Social.tsx):
//  scrollable feed of publications with like/comment, and a full-screen
//  viewer for multi-image posts (with a hi-res fetch once opened).

import SwiftUI
import AVKit
import Combine
import ImageIO

// When a post was published, shown in its header. Relative for anything
// recent (the timescale people actually care about scrolling a feed),
// falling back to an absolute date once "3d ago" stops being more useful
// than the date itself. Worded when shown, in the app's language and locale.
// `now`: the feed's TimelineView passes the minute it is drawing, so the
// label moves on while the feed stays open (it stood still until something
// else redrew the post).
func formatPostDate(_ date: Date, now: Date = Date()) -> String {
    let mins = Int(now.timeIntervalSince(date) / 60)
    if mins < 1 { return S.appShellTimeJustNow }
    if mins < 60 { return S.appShellTimeMinutesAgo(count: mins) }
    let hours = mins / 60
    if hours < 24 { return S.appShellTimeHoursAgo(count: hours) }
    let days = hours / 24
    if days < 7 { return S.appShellTimeDaysAgo(count: days) }
    let f = DateFormatter()
    f.locale = L10n.shared.locale
    f.dateStyle = .medium
    f.timeStyle = .none
    return f.string(from: date)
}

/// The most a launch-time feed snapshot may weigh before it costs more
/// to read than it saves (see loadCachedPosts).
private let cMaxCachedFeedBytes = 4 << 20

/// Hands a publication to SwiftUI by reference rather than by value.
///
/// Caught with the debugger paused during the freeze: AttributeGraph -
/// SwiftUI's diffing engine - was ~170 frames deep in
/// LayoutDescriptor::make_layout, recursively walking the fields and
/// oneof cases of the generated protobuf types, because the feed handed
/// Msg_SocialPublication straight to a View as a stored property.
/// AttributeGraph builds a layout descriptor for every value type it has
/// to compare, and these are enormous: 144 message types and 460
/// conformances in messages.pb.swift, nested and mutually recursive.
///
/// It only ever pays that once per type (TypeDescriptorCache), which is
/// exactly the shape of the bug - slow on a cold launch, fine coming back
/// from the background, fine between tabs, because the cache lives as
/// long as the process.
///
/// A final class sidesteps all of it: AttributeGraph compares a reference
/// by pointer and never walks what is behind it. Boxes are rebuilt
/// whenever posts changes, so a new box *is* the signal that the content
/// changed.
final class PostBox {
    let pub: Msg_SocialPublication

    init(_ pub: Msg_SocialPublication) { self.pub = pub }
}

@MainActor
final class SocialFeedViewModel: ObservableObject {
    // Issue #79: lifted from SocialFeedView's own private @StateObject to a
    // shared singleton (same wiring as NotificationsModel/UploadModel) so
    // MainView can inspect `posts`/`hasLoadedOnce` too, to decide whether
    // to default-launch on Social or fall back to Images.
    static let shared = SocialFeedViewModel()

    /// How many publications to fetch per page (issue #15) — loading 50 at
    /// once, each with its own files/comments, was a big chunk of why the
    /// feed felt slow to appear, especially while competing with a large
    /// photo sync on the same shared connection.
    private static let pageSize: Int32 = 4

    @Published var posts: [Msg_SocialPublication] = [] {
        didSet { boxedPosts = posts.map(PostBox.init) }
    }
    /// What the feed actually renders - see PostBox.
    @Published private(set) var boxedPosts: [PostBox] = []
    @Published var loading = false
    @Published var loadingMore = false
    // Issue #79: true once the server has answered at least one loadFeed
    // call (regardless of how many posts came back) - MainView waits for
    // this before deciding "genuinely nothing in social, fall back to
    // Images" instead of racing a launch-time snapshot of an empty cache.
    @Published private(set) var hasLoadedOnce = false
    /// The last load of the feed failed, and why (nil: it didn't): said
    /// under "Loading…" until the first one comes.
    @Published private(set) var problem: LoadProblem?
    /// The next page failed: the end of the feed says so, with Try again.
    @Published private(set) var moreFailed = false
    /// One more for every "New post" in the wide layout's top bar
    /// (AppMenu.swift), which opens the composer here.
    @Published var composeRequests = 0
    private var endReached = false

    private var pollTask: Task<Void, Never>?
    private var wakeWatch: AnyCancellable?

    /// Log Out: an empty feed, no cached snapshot, and the auto-load stopped.
    func reset() {
        pollTask?.cancel()
        pollTask = nil
        posts = []
        loading = false
        loadingMore = false
        hasLoadedOnce = false
        problem = nil
        moreFailed = false
        endReached = false
        try? FileManager.default.removeItem(at: Self.cacheURL)
    }

    /// On-disk snapshot of the last successfully loaded page (issue #22):
    /// read synchronously at init so the feed shows *something* the instant
    /// the app launches — even before the network request that refreshes it
    /// has a chance to complete — instead of a blank screen.
    private static let cacheURL = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("social_feed_cache.pb")

    /// `autoLoad` false: nothing asked until told (tests).
    init(autoLoad: Bool = true) {
        // Mark loading right away, synchronously, so the very first render
        // shows the loading state rather than a flash of "No posts yet".
        loading = autoLoad
        // The cached snapshot is read off the main thread, which costs a
        // moment of empty feed and is worth it.
        //
        // This is the app's first touch of SwiftProtobuf, and that first
        // touch is not just a parse: it makes the Swift runtime
        // instantiate conformances for the generated message types - 460
        // of them across 144 messages in messages.pb.swift - which showed
        // up on a device as threads sitting in
        // swift_conformsToProtocolMaybeInstantiateSuperclasses and dyld
        // symbol lookups while the app was starting. Doing it on the main
        // thread meant launch waited for all of that, and only ever on a
        // cold start, because the runtime caches it for the life of the
        // process. Which is exactly the shape of the bug: slow when
        // launched fresh, fine coming back from the background, fine
        // between tabs.
        Task { await loadCachedPosts() }
        // Issue #22: start fetching immediately when the app launches,
        // rather than waiting for the user to actually tap the Social tab —
        // by the time they do, the feed is often already there. This
        // ViewModel is constructed once, up front, along with every other
        // tab's (TabView builds all its tabs' state eagerly even though
        // only one is visible), so `init` firing this is enough; no
        // view-appearance hook needed.
        guard autoLoad else { return }
        startAutoLoad()
        // Signed in again, a network came up, back in the foreground: a
        // page that failed is asked for again (the first load's wait is
        // cut short by sleepOrWake).
        wakeWatch = OTCConnection.shared.$wakes
            .dropFirst()
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.woke() }
    }

    /// A wake: the phone may be back online, and a page that failed is
    /// asked for again.
    func woke() {
        backOnline()
        if moreFailed { retryMore() }
    }

    /// Sends the feed's likes, comments, deletes and single posts. Tests
    /// answer in the device's place.
    var socialRequest: @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope = { payload in
        try await OTCConnection.shared.request { $0.payload = payload }
    }

    /// A short message at the top of the feed (a comment or a delete the
    /// device didn't take), worded when shown; nil: none.
    @Published private(set) var notice: FeedNotice?
    /// Bumped by every notice: only the last one's timer takes it away.
    private var notices = 0

    /// Shows `n` at the top of the feed for a few seconds, longer for a long
    /// sentence (the device's words), and says it to VoiceOver.
    func say(_ n: FeedNotice) {
        notices += 1
        let mine = notices
        notice = n
        let m = n.text
        Announce.polite(m)
        let seconds = max(2.5, Double(m.count) / 18)
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
            if self?.notices == mine { self?.notice = nil }
        }
    }

    /// The device took the request: an Ack with ok.
    nonisolated static func ackOK(_ resp: Msg_RespEnvelope) -> Bool {
        if case .respAck(let ack) = resp.payload { return ack.ok }
        return false
    }

    /// Why the device refused, in its own words (already in the app's
    /// language) - the Ack's, else the envelope's; nil when it gave none. A
    /// demo device answers both with its notice. Android's refusal().
    nonisolated static func refusal(_ resp: Msg_RespEnvelope) -> String? {
        if case .respAck(let ack) = resp.payload, !ack.errorMsg.isEmpty { return ack.errorMsg }
        if resp.error, !resp.errorMessage.isEmpty { return resp.errorMessage }
        return nil
    }

    /// Sends the feed's pages. Tests answer in the device's place.
    var feedRequest: @MainActor (Msg_GetSocialPublications) async throws -> Msg_RespEnvelope = { req in
        // More time after a timeout, as Images' pages (OTCConnection.ask).
        try await OTCConnection.shared.ask("feed", base: OTCConnection.pageTimeout) { $0.payload = .reqGetSocialPublications(req) }
    }

    /// Whether the phone has a network now. Tests decide in the phone's place.
    var online: @MainActor () -> Bool = { !NetworkWatch.shared.offline }

    /// Why a load failed. Tests decide in the phone's place.
    var problemOf: @MainActor (Msg_RespEnvelope?, Error?) -> LoadProblem = { LoadProblem.now(resp: $0, error: $1) }

    /// Back online: "This phone is offline" no longer says why the posts
    /// aren't here while they are asked for again (Android's backOnline).
    private func backOnline() {
        guard problem == .offline else { return }
        problem = problem?.backOnline(online())
    }

    /// What says how the feed's loads went, as one value (Android's State
    /// fields of the same names).
    private var loadState: FeedLoadState {
        get { FeedLoadState(loading: loading, loadingMore: loadingMore, hasLoadedOnce: hasLoadedOnce, problem: problem, moreFailed: moreFailed) }
        set {
            if loading != newValue.loading { loading = newValue.loading }
            if loadingMore != newValue.loadingMore { loadingMore = newValue.loadingMore }
            if hasLoadedOnce != newValue.hasLoadedOnce { hasLoadedOnce = newValue.hasLoadedOnce }
            if problem != newValue.problem { problem = newValue.problem }
            if moreFailed != newValue.moreFailed { moreFailed = newValue.moreFailed }
        }
    }

    private func loadCachedPosts() async {
        let url = Self.cacheURL
        let cached = await Task.detached(priority: .userInitiated) { () -> [Msg_SocialPublication] in
            guard let data = try? Data(contentsOf: url) else { return [] }
            // A cache written by an older build holds the entire
            // accumulated feed, thumbnails and all; skip an oversized one
            // and let the next save replace it with a small one.
            guard data.count <= cMaxCachedFeedBytes,
                  let msg = try? Msg_SocialPublications(serializedData: data)
            else { return [] }

            return msg.publications
        }.value
        // The network has had the whole read and parse to answer in, and
        // if it did its posts are fresher than this snapshot - so this
        // only ever fills an empty feed, never replaces a loaded one.
        guard posts.isEmpty, !cached.isEmpty else { return }
        posts = cached
    }

    /// Writes the launch snapshot, off the main thread.
    ///
    /// This class is @MainActor, so both halves of this used to run
    /// there: protobuf-encoding every post in the feed - thumbnails
    /// included, which is megabytes once a few pages have loaded - and
    /// then a synchronous atomic disk write, with the whole app blocked
    /// behind it. That is the freeze reported "for a few seconds when the
    /// Social tab is loading", and it froze every other tab too, because
    /// a blocked main thread blocks all of them.
    ///
    /// Only the first page is kept: the point of the cache is to put
    /// something on screen at launch, not to restore a whole scroll
    /// position - and a small file is also a fast one to read back
    /// synchronously in init.
    private func saveCachedPosts() {
        let snapshot = Array(posts.prefix(Int(Self.pageSize)))
        let url = Self.cacheURL
        Task.detached(priority: .utility) {
            var msg = Msg_SocialPublications()
            msg.publications = snapshot
            guard let data = try? msg.serializedData() else { return }
            try? data.write(to: url, options: .atomic)
        }
    }

    /// Keeps retrying every few seconds until a request actually completes
    /// (issue #11). A short handful of quick retries wasn't enough: the
    /// shared connection can be legitimately busy for a long time — e.g. a
    /// large photo sync uploading thousands of files — so "give up after a
    /// couple of seconds" just reproduced the bug. This only stops once the
    /// server actually answers with the feed (even with zero publications);
    /// it doesn't stop just because a request throws, nor for an error
    /// answer (the bridge's for a device it can't reach). The wait between
    /// tries is cut short by a wake (signed in again, a network came up,
    /// back in the foreground).
    /// Also what a new sign-in calls (issue #133): Log Out cancels this,
    /// and `init` only ever ran once per process, so the feed after
    /// signing in again sat on "No social posts" until the app was killed.
    func startAutoLoad() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                if await self.loadFeed() {
                    self.hasLoadedOnce = true
                    break
                }
                await OTCConnection.shared.sleepOrWake(4)
            }
            self?.pollTask = nil
        }
    }

    /// Re-fetches from the start. Returns whether the server actually
    /// answered with the feed (regardless of how many publications came
    /// back; `problem` says why not otherwise) so callers
    /// can tell "legitimately empty" apart from "the request never
    /// completed".
    ///
    /// Fetches at least as many posts as are already showing rather than
    /// hard-resetting to one page (issue #22): at launch `posts` may
    /// already hold a cached snapshot from last time (possibly more than
    /// one page's worth, if the user had scrolled), and collapsing that
    /// back down to 4 the moment the network catches up would read as data
    /// loss rather than "loaded faster".
    @discardableResult
    func loadFeed() async -> Bool {
        loading = true
        backOnline()
        defer { loading = false }
        endReached = false
        let count = Int32(max(posts.count, Int(Self.pageSize)))
        let problem = await fetchPage(total: count, excludeUuids: [], replacing: true)
        // A refresh that worked also takes "Couldn't load more posts" away:
        // the end of the feed is a new one.
        loadState = loadState.afterLoad(problem)
        return problem == nil
    }

    /// Loads the next page once the feed has been scrolled near the end of
    /// what's currently loaded (issue #15: 4 at a time, fetched again before
    /// the user actually runs out of already-loaded posts).
    func loadMoreIfNeeded(current post: Msg_SocialPublication?) async {
        guard let post, !endReached else { return }
        guard let idx = posts.firstIndex(where: { $0.uuid == post.uuid }) else { return }
        guard idx >= posts.count - 2 else { return }
        // Claimed in one step: a wake's retryMore and the last post's own
        // task can both get here, and only one asks.
        guard let claimed = loadState.claimMore() else { return }
        loadState = claimed
        defer { loadingMore = false }
        moreFailed = await fetchPage(total: Self.pageSize, excludeUuids: posts.map(\.uuid), replacing: false) != nil
    }

    /// The end of the feed's Try again (and a wake): the next page, asked
    /// for again.
    func retryMore() {
        Task { await loadMoreIfNeeded(current: posts.last) }
    }

    /// After a comment the device took: that post alone, asked for again
    /// (ReqGetPublication) - its placeholder becomes the device's comment,
    /// with anything else said on it meanwhile. Every post loaded, with its
    /// thumbnails, used to come again for one comment. A post that doesn't
    /// come keeps its placeholder until the feed loads again.
    private func refreshPost(_ pubUuid: String) async {
        var req = Msg_ReqGetPublication()
        req.pubUuid = pubUuid
        guard let resp = try? await socialRequest(.reqGetPublication(req)),
              case .respPublication(let r) = resp.payload, r.hasPublication,
              let idx = posts.firstIndex(where: { $0.uuid == r.publication.uuid }) else { return }
        posts[idx] = r.publication
        saveCachedPosts()
    }

    /// nil once the page is in; otherwise why not. An answer that isn't the
    /// feed (the bridge's "device unreachable", an error) is no page: the
    /// first load used to take it for one, stop asking, and leave the feed
    /// reading "No social posts".
    @discardableResult
    private func fetchPage(total: Int32, excludeUuids: [String], replacing: Bool) async -> LoadProblem? {
        var req = Msg_GetSocialPublications()
        req.total = total
        req.excludeUuids = excludeUuids
        do {
            let resp = try await feedRequest(req)
            guard case .respSocialPublications(let sp) = resp.payload else {
                print("Social feed load refused, will retry:", resp.errorMessage)
                return problemOf(resp, nil)
            }
            if replacing {
                posts = sp.publications
            } else {
                let existing = Set(posts.map(\.uuid))
                posts.append(contentsOf: sp.publications.filter { !existing.contains($0.uuid) })
            }
            if sp.publications.count < Int(total) {
                endReached = true
            }
            saveCachedPosts()
            return nil
        } catch {
            print("Social feed load failed, will retry:", error)
            return problemOf(nil, error)
        }
    }

    // Issue #17: flip the UI the instant the user taps, don't wait for the
    // round-trip. The server's Ack for these three requests carries no data
    // beyond ok/error, so the optimistic guess *is* the new truth on
    // success — only a rejected/failed request needs to walk it back.

    func likePublication(_ uuid: String) async {
        guard let idx = posts.firstIndex(where: { $0.uuid == uuid }) else { return }
        let wasLiked = posts[idx].liked
        posts[idx].liked = !wasLiked
        posts[idx].likes += wasLiked ? -1 : 1

        var req = Msg_LikePublication()
        req.pubUuid = uuid
        do {
            let resp = try await socialRequest(.reqLikePublication(req))
            if case .respAck(let ack) = resp.payload, !ack.ok {
                revertLike(pubUuid: uuid, liked: wasLiked)
            }
        } catch {
            revertLike(pubUuid: uuid, liked: wasLiked)
        }
    }

    private func revertLike(pubUuid: String, liked: Bool) {
        guard let idx = posts.firstIndex(where: { $0.uuid == pubUuid }) else { return }
        posts[idx].liked = liked
        posts[idx].likes += liked ? 1 : -1
    }

    func likeComment(_ uuid: String) async {
        guard let pubIdx = posts.firstIndex(where: { p in p.comments.contains { $0.commentUuid == uuid } }),
              let cIdx = posts[pubIdx].comments.firstIndex(where: { $0.commentUuid == uuid }) else { return }
        let wasLiked = posts[pubIdx].comments[cIdx].liked
        posts[pubIdx].comments[cIdx].liked = !wasLiked
        posts[pubIdx].comments[cIdx].likes += wasLiked ? -1 : 1

        var req = Msg_LikeComment()
        req.commentUuid = uuid
        do {
            let resp = try await socialRequest(.reqLikeComment(req))
            if case .respAck(let ack) = resp.payload, !ack.ok {
                revertCommentLike(commentUuid: uuid, liked: wasLiked)
            }
        } catch {
            revertCommentLike(commentUuid: uuid, liked: wasLiked)
        }
    }

    private func revertCommentLike(commentUuid: String, liked: Bool) {
        guard let pubIdx = posts.firstIndex(where: { p in p.comments.contains { $0.commentUuid == commentUuid } }),
              let cIdx = posts[pubIdx].comments.firstIndex(where: { $0.commentUuid == commentUuid }) else { return }
        posts[pubIdx].comments[cIdx].liked = liked
        posts[pubIdx].comments[cIdx].likes += liked ? 1 : -1
    }

    /// Issue #17: the comment shows at once; the device's answer decides.
    /// True once it is in. Otherwise it goes from the post and the feed
    /// says why - the device's words, or ours - and the card puts the text
    /// back in its field (it was cleared as the comment went out).
    @discardableResult
    func addComment(pubUuid: String, text: String) async -> Bool {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, let idx = posts.firstIndex(where: { $0.uuid == pubUuid }) else { return false }

        var optimistic = Msg_Comment()
        optimistic.pubUuid = pubUuid
        optimistic.commentUuid = "pending-\(UUID().uuidString)"
        optimistic.comment = trimmed
        optimistic.publisher = "me"
        posts[idx].comments.append(optimistic)

        var req = Msg_NewSocialComment()
        req.pubUuid = pubUuid
        req.comment = trimmed
        req.publisher = "me"
        let failure: FeedNotice?
        do {
            let resp = try await socialRequest(.reqNewSocialComment(req))
            failure = Self.ackOK(resp) ? nil : (Self.refusal(resp).map(FeedNotice.device) ?? .commentFailed)
        } catch {
            failure = .commentFailed
        }
        guard let failure else {
            // Swap the placeholder for the server's real comment (uuid,
            // timestamp, anything anyone else posted meanwhile) — this
            // can happen quietly in the background since the user
            // already sees their comment.
            await refreshPost(pubUuid)
            return true
        }
        removeOptimisticComment(pubUuid: pubUuid, commentUuid: optimistic.commentUuid)
        say(failure)
        return false
    }

    private func removeOptimisticComment(pubUuid: String, commentUuid: String) {
        guard let idx = posts.firstIndex(where: { $0.uuid == pubUuid }) else { return }
        posts[idx].comments.removeAll { $0.commentUuid == commentUuid }
    }

    // Issue #29: who liked a publication/comment.
    func fetchPublicationLikers(_ pubUuid: String) async -> [Msg_Profile] {
        var req = Msg_GetPublicationLikers()
        req.pubUuid = pubUuid
        guard let resp = try? await socialRequest(.reqGetPublicationLikers(req)) else { return [] }
        if case .respLikers(let l) = resp.payload { return l.likers }
        return []
    }

    func fetchCommentLikers(_ commentUuid: String) async -> [Msg_Profile] {
        var req = Msg_GetCommentLikers()
        req.commentUuid = commentUuid
        guard let resp = try? await socialRequest(.reqGetCommentLikers(req)) else { return [] }
        if case .respLikers(let l) = resp.payload { return l.likers }
        return []
    }

    // Issue #34: delete one of your own posts. Removed locally only once
    // the server confirms it — this hits the DB (files, comments, likes),
    // not something to optimistically guess at. A delete the device didn't
    // take (the demo device refuses changes) leaves the post, and the feed
    // says why - it used to do nothing visible.
    func deletePublication(_ pubUuid: String) async {
        var req = Msg_DelSocialPublication()
        req.pubUuid = pubUuid
        do {
            let resp = try await socialRequest(.reqDelSocialPublication(req))
            guard Self.ackOK(resp) else {
                say(Self.refusal(resp).map(FeedNotice.device) ?? .deleteFailed)
                return
            }
            posts.removeAll { $0.uuid == pubUuid }
            saveCachedPosts()
        } catch {
            say(.deleteFailed)
        }
    }

    // Issue #35: delete a comment on one of your own posts (server-side
    // enforces the "own post" rule regardless of who wrote the comment).
    // Issue #174: or one you wrote on a friend's post (comment.own). As
    // deletePublication, a refusal says why.
    func deleteComment(_ commentUuid: String) async {
        var req = Msg_DelSocialComment()
        req.commentUuid = commentUuid
        do {
            let resp = try await socialRequest(.reqDelSocialComment(req))
            guard Self.ackOK(resp) else {
                say(Self.refusal(resp).map(FeedNotice.device) ?? .deleteCommentFailed)
                return
            }
            for idx in posts.indices {
                posts[idx].comments.removeAll { $0.commentUuid == commentUuid }
            }
            saveCachedPosts()
        } catch {
            say(.deleteCommentFailed)
        }
    }

    // Issue #78: opening a post from a tapped notification. Fetches it
    // directly via reqGetPublication if it isn't already among whatever
    // page of the feed happens to be loaded, rather than paging through
    // everything since it, then publishes scrollTargetPub for the view's
    // ScrollViewReader to act on. highlightPub/highlightComment drive a
    // brief "here's what you tapped" flash, cleared by the view after a
    // couple of seconds.
    @Published var scrollTargetPub: String?
    @Published var highlightPub: String?
    @Published var highlightComment: String?

    func openPost(pubUuid: String, commentUuid: String?) async {
        if !posts.contains(where: { $0.uuid == pubUuid }) {
            var req = Msg_ReqGetPublication()
            req.pubUuid = pubUuid
            guard let resp = try? await socialRequest(.reqGetPublication(req)) else { return }
            if case .respPublication(let r) = resp.payload, r.hasPublication,
               !posts.contains(where: { $0.uuid == r.publication.uuid }) {
                posts.insert(r.publication, at: 0)
            }
        }
        scrollTargetPub = pubUuid
        highlightPub = pubUuid
        highlightComment = commentUuid
    }
}

/// A short message at the top of the feed, kept as what happened and worded
/// when it shows (as FriendsToast); Android's State.notice.
enum FeedNotice: Equatable {
    case commentFailed, deleteFailed, deleteCommentFailed
    /// The device's own words, already in the app's language.
    case device(String)

    var text: String {
        switch self {
        case .commentFailed: return S.appSocialPostCommentFailed
        case .deleteFailed: return S.appSocialPostDeleteFailed
        case .deleteCommentFailed: return S.appSocialPostDeleteCommentFailed
        case .device(let words): return words
        }
    }
}

struct SocialFeedView: View {
    // Issue #123: on an iPad the tab bar sits across the top, not the
    // bottom, so the negative offset below (which pulls the logo up into
    // pull-to-refresh's reserved space on a phone) would push it under
    // the tab bar instead.
    @Environment(\.horizontalSizeClass) private var hSizeClass
    // Issue #79: shared with MainView (environmentObject-injected from
    // RootView, same wiring as UploadModel/NotificationsModel) instead of
    // owned privately here, so MainView can decide whether to default-
    // launch on this tab or fall back to Images when it's empty.
    @EnvironmentObject var vm: SocialFeedViewModel
    // Issue #78: a tapped notification lands here via this shared model
    // (environmentObject-injected from RootView, same wiring as
    // UploadModel) rather than a prop, since the bell that sets it lives
    // outside this tab's own view entirely.
    @EnvironmentObject var notifications: NotificationsModel

    // Issue #32: "+" opens a dedicated compose screen (tag filter + tap to
    // select + a single Publish action) — not the Images tab reused.
    @State private var showingPicker = false
    // Issue #84: Friendships used to be its own top-level tab - now a
    // sheet presented from here instead, left of "+" (same arrangement as
    // the web app's own header button), so it isn't reachable when there's
    // nothing to actually apply a friendship to yet. MainView's, so that
    // turning the phone moves it to the wide layout's Friends page and
    // back. A wide window has Friends in its menu instead (AppMenu.swift):
    // no button here, and a friend-request alert opens that page.
    @Binding var showingFriendships: Bool
    @Environment(\.wideLayout) private var wide

    init(showingFriendships: Binding<Bool>) {
        _showingFriendships = showingFriendships
    }
    /// The feed's visible height: the scroll view less the navigation and
    /// tab bars and anything else on its safe area. Every post's media is
    /// kept inside it (rule 1, see cFeedMaxAspect). nil until the first
    /// layout has measured it - posts wait for it rather than drawing a
    /// pass at a guessed size.
    @State private var feedViewportHeight: CGFloat?
    /// The measurement feedViewportHeight was last taken from, to tell the
    /// keyboard coming up from the window really changing.
    @State private var feedViewportBase: FeedViewportMeasure?
    /// The top safe-area insets of the navigation stack (the status bar)
    /// and of the feed inside it (the status bar and the navigation bar),
    /// for logoPullUp. nil until measured.
    @State private var stackTopInset: CGFloat?
    @State private var feedTopInset: CGFloat?

    /// How far the logo is pulled up to sit level with the navigation
    /// bar's buttons: 44 pt where the bar is a full row along the top (an
    /// iPhone in portrait: 54 pt), nothing where it is shorter. iPhone
    /// Duo's cover screen puts the bar's buttons on the trailing edge and
    /// keeps only 24 pt at the top: a fixed 44 put the logo off the top of
    /// the screen, and 24 into its rounded corner.
    private var logoPullUp: CGFloat {
        guard hSizeClass != .regular else { return 0 }
        guard let stackTopInset, let feedTopInset else { return 44 }
        return feedTopInset - stackTopInset >= 44 ? 44 : 0
    }

    // Issue #81: the nav bar had nothing on the leading side (no title, no
    // button), just the trailing "+" - reading as empty space instead of a
    // masthead. Rather than pin a logo into that fixed toolbar, it lives as
    // ordinary scrollable content at the very top of the feed instead, so
    // it scrolls away with everything else once the user starts reading -
    // same "collapses as you engage" feel as Instagram/Twitter's own
    // wordmark header.
    // A wide window's top bar has the logo already (AppMenu.swift).
    @ViewBuilder
    private var logoHeader: some View {
        if !wide { logoRow }
    }

    private var logoRow: some View {
        HStack {
            Image("OTCLogo")
                .resizable()
                .scaledToFit()
                .frame(height: 28)
            Spacer()
        }
        .padding(.horizontal, 12)
        .padding(.top, 2)
        .padding(.bottom, 2)
    }

    var body: some View {
        NavigationStack {
            Group {
                // Issue #22: an empty ScrollView while the very first fetch
                // is still in flight used to just look frozen — show an
                // actual loading state instead, distinct from "genuinely no
                // posts".
                //
                // Until the first answer comes (asked again every 4 s, at
                // once on a wake), not only while a request is out: between
                // two tries it used to read "No social posts". After a
                // failure, why it isn't here yet.
                if vm.posts.isEmpty && (vm.loading || !vm.hasLoadedOnce) {
                    VStack(spacing: 12) {
                        logoHeader
                        Spacer()
                        ProgressView()
                        Text(S.commonLoading).foregroundColor(.secondary)
                        if let problem = vm.problem {
                            Text(problem.text(.posts))
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .multilineTextAlignment(.center)
                                .frame(maxWidth: 360)
                                .padding(.horizontal, 32)
                                // Said to VoiceOver when it appears or
                                // changes (Android's polite live region).
                                .onAppear { Announce.polite(problem.text(.posts)) }
                                .onChange(of: problem) { _, p in Announce.polite(p.text(.posts)) }
                        }
                        Spacer()
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if vm.posts.isEmpty {
                    // Issue #79: an empty timeline is the first thing a new
                    // owner sees, so it should say what this is for and
                    // offer the one thing to do about it - not send them
                    // to Images, as this used to.
                    VStack(spacing: 0) {
                        logoHeader
                        Spacer()
                        VStack(spacing: 14) {
                            Text(S.appSocialFeedEmptyTitle)
                                .font(.system(size: 30, weight: .heavy))
                            Text(S.appSocialFeedEmptyText)
                                .font(.body)
                                .foregroundStyle(.secondary)
                                .multilineTextAlignment(.center)
                                .frame(maxWidth: 320)
                            Button { showingPicker = true } label: {
                                // ZStack rather than a frame on the glyph:
                                // an SF Symbol's own bounds aren't square,
                                // so framing it centres its box, not the
                                // cross. Centring it inside the circle does.
                                ZStack {
                                    Circle()
                                        .fill(Color(red: 1.0, green: 0.42, blue: 0.29))
                                        .frame(width: 96, height: 96)
                                        .shadow(color: .black.opacity(0.22), radius: 12, y: 5)
                                    Image(systemName: "plus")
                                        .font(.system(size: 40, weight: .bold))
                                        .foregroundStyle(.white)
                                }
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel(S.appShellNewPost)
                            .padding(.top, 10)
                        }
                        Spacer()
                        Spacer()
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    // Issue #78: wrapped in ScrollViewReader (new to this
                    // file) purely so a tapped notification can scroll to
                    // the post it names - nothing here needed programmatic
                    // scrolling before.
                    ScrollViewReader { proxy in
                        ScrollView {
                            // No outer padding and no per-post spacing (issue
                            // #12/Instagram-style ask): a horizontal inset here
                            // would margin the images too, and this app already
                            // separates posts with a Divider instead of gaps.
                            LazyVStack(spacing: 0) {
                                // Issue #81 follow-up: still misaligned
                                // with zero/positive top padding -
                                // .refreshable below reserves its own
                                // vertical space for the pull-to-refresh
                                // control above scroll content, even at
                                // rest, which is what actually pushed the
                                // logo down past where the "+" button
                                // sits (the loading/empty states above
                                // have no .refreshable, hence no matching
                                // offset needed there). The negative
                                // offset is the navigation bar's measured
                                // height, at most 44 (logoPullUp).
                                logoHeader
                                    .padding(.top, -logoPullUp)
                                ForEach(feedViewportHeight == nil ? [] : vm.boxedPosts, id: \.pub.uuid) { box in
                                    let post = box.pub
                                    PostCard(
                                        box: box,
                                        tourMarks: post.uuid == vm.boxedPosts.first?.pub.uuid,
                                        viewportHeight: feedViewportHeight ?? 0,
                                        isHighlighted: post.uuid == vm.highlightPub,
                                        highlightCommentUuid: post.uuid == vm.highlightPub ? vm.highlightComment : nil,
                                        onLikePub: { Task { await vm.likePublication(post.uuid) } },
                                        onLikeComment: { c in Task { await vm.likeComment(c) } },
                                        onComment: { text in await vm.addComment(pubUuid: post.uuid, text: text) },
                                        fetchPublicationLikers: { await vm.fetchPublicationLikers($0) },
                                        fetchCommentLikers: { await vm.fetchCommentLikers($0) },
                                        onDeletePub: { Task { await vm.deletePublication(post.uuid) } },
                                        onDeleteComment: { c in Task { await vm.deleteComment(c) } }
                                    )
                                    .id(post.uuid)
                                    .task { await vm.loadMoreIfNeeded(current: post) }
                                    Divider()
                                }
                                if vm.loadingMore {
                                    ProgressView()
                                        .frame(maxWidth: .infinity)
                                        .padding(.vertical, 16)
                                } else if vm.moreFailed {
                                    // The next page failed: it is asked for
                                    // again on a wake, or now.
                                    HStack(spacing: 4) {
                                        Text(LoadProblem.morePosts)
                                            .font(.subheadline)
                                            .foregroundStyle(.secondary)
                                        Button(LoadProblem.tryAgain) { vm.retryMore() }
                                            .font(.subheadline.weight(.semibold))
                                    }
                                    .frame(maxWidth: .infinity)
                                    .padding(16)
                                    .onAppear { Announce.polite(LoadProblem.morePosts) }
                                }
                            }
                            // Issue #123: a centred column on an iPad,
                            // edge to edge on a phone (see cFeedMaxWidth).
                            .frame(maxWidth: cFeedMaxWidth)
                            .frame(maxWidth: .infinity)
                        }
                        // Measures feedViewportHeight. The scroll view's
                        // own frame is already the space between the bars
                        // (its content scrolls on under them, but that is
                        // drawn, not laid out) - measured on an iPhone 18
                        // Pro: 675pt of 874, with the 116pt of status and
                        // navigation bar and the 83pt tab bar outside it.
                        // Not less proxy.safeAreaInsets: here those still
                        // report the bars, which would count them twice.
                        .onGeometryChange(for: FeedViewportMeasure.self) { proxy in
                            FeedViewportMeasure(width: proxy.size.width, height: proxy.size.height, bottomInset: proxy.safeAreaInsets.bottom)
                        } action: { measure in
                            // The very first pass reports an empty frame;
                            // that is not a size.
                            guard measure.height > 0 else { return }
                            // The keyboard shrinks this frame as well -
                            // the tab's whole content is resized, above
                            // this view, so .ignoresSafeArea(.keyboard)
                            // on a measuring view doesn't help, and the
                            // keyboard notifications arrive after the
                            // resize. Typing a comment must not shrink
                            // every post's media under the finger, so the
                            // height from before the keyboard stands
                            // while it is up (see isKeyboard(after:)).
                            if let base = feedViewportBase, measure.isKeyboard(after: base) { return }
                            feedViewportBase = measure
                            feedViewportHeight = measure.height
                        }
                        // The feed's top inset, for logoPullUp. The logo's
                        // padding moves only what scrolls, never these
                        // insets, so the two can't chase each other.
                        .onGeometryChange(for: CGFloat.self) { $0.safeAreaInsets.top } action: { feedTopInset = $0 }
                        .refreshable { await vm.loadFeed() }
                        .onChange(of: vm.scrollTargetPub) { _, target in
                            guard let target else { return }
                            withAnimation { proxy.scrollTo(target, anchor: .top) }
                            vm.scrollTargetPub = nil
                            // Brief "here's what you tapped" flash, same
                            // timing as the web version's own fade-out.
                            DispatchQueue.main.asyncAfter(deadline: .now() + 2.5) {
                                vm.highlightPub = nil
                                vm.highlightComment = nil
                            }
                        }
                    }
                }
            }
            // A comment or a delete the device didn't take: why, at the top.
            .overlay(alignment: .top) {
                if let notice = vm.notice {
                    Text(notice.text)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 14).padding(.vertical, 8)
                        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                        .padding(.horizontal)
                        .padding(.top, 8)
                        .transition(.move(edge: .top).combined(with: .opacity))
                }
            }
            .animation(.default, value: vm.notice)
            // No nav title here (issue #12): the tab bar already labels this
            // screen "Social", repeating it as a large title above the feed
            // was redundant chrome. Issue #81: an *empty* title (rather
            // than none at all) still matters though - without any title
            // at all, .inline here left a tall gap of dead space reserved
            // for the large-title-to-inline collapse transition above the
            // scroll content, pushing the logo header well below the "+"
            // button instead of level with it.
            .navigationTitle("")
            .navigationBarTitleDisplayMode(.inline)
            // A wide window: "New post" is in the top bar, as on the web,
            // and Friends in the menu - nothing left for this bar.
            .toolbar(wide ? .hidden : .automatic, for: .navigationBar)
            .toolbar {
                // Issue #84: left of "+", matching the web header's own
                // arrangement (button, then "+").
                if !wide {
                    ToolbarItem(placement: .navigationBarTrailing) {
                        Button { showingFriendships = true } label: {
                            Image(systemName: "person.2")
                                .tourTarget(.friendsButton)
                        }
                        .accessibilityLabel(S.appShellSectionFriends)
                    }
                }
                ToolbarItem(placement: .navigationBarTrailing) {
                    // Outline circle + orange tint, matching the web
                    // composer's own button (an outline ring rather than a
                    // filled disc reads lighter sitting right in the nav
                    // bar next to the feed, instead of like a floating
                    // action button that wandered up from a corner).
                    Button { showingPicker = true } label: {
                        Image(systemName: "plus.circle")
                            .tourTarget(wide ? nil : .newPost)
                    }
                    .tint(Color(red: 1.0, green: 0.42, blue: 0.29)) // matches web's --ember (#ff6b4a)
                    .accessibilityLabel(S.appShellNewPost)
                }
            }
        }
        // The stack's own top inset (the status bar, outside its bar), for
        // logoPullUp.
        .onGeometryChange(for: CGFloat.self) { $0.safeAreaInsets.top } action: { stackTopInset = $0 }
        .onChange(of: vm.composeRequests) { _, _ in showingPicker = true }
        .sheet(isPresented: $showingPicker) {
            NewPostPickerView {
                Task { await vm.loadFeed() }
            }
        }
        .sheet(isPresented: Binding(get: { showingFriendships && !wide }, set: { showingFriendships = $0 })) {
            FriendshipsView()
        }
        // Issue #78/#84: a tapped like/comment notification opens that post
        // directly; a friend-request notification opens the Friendships
        // sheet from here now instead of MainView switching to a tab that
        // no longer exists.
        .onChange(of: notifications.pendingDeepLink) { _, link in
            switch link {
            case .post(let pubUuid, let commentUuid):
                Task { await vm.openPost(pubUuid: pubUuid, commentUuid: commentUuid) }
                notifications.pendingDeepLink = nil
            case .friendRequests:
                // A wide window: MainView opens the Friends page.
                guard !wide else { break }
                showingFriendships = true
                notifications.pendingDeepLink = nil
            case .updates, nil:
                // Issue #183: SettingsView's to handle.
                break
            }
        }
    }
}

/// Thumbnail dimensions, parsed once per file rather than on every layout
/// pass.
///
/// The media area's size is computed inside the view body, and the body
/// re-runs constantly while a feed scrolls - so building a UIImage from
/// the thumbnail data there meant re-parsing every image of every visible
/// post, many times a second, on the main thread. That is work the
/// scrolling itself then has to wait for. The answer only depends on the
/// file, so it is remembered by hash.
/// Issue #114: whether feed videos are muted, shared by every post.
///
/// Once someone taps unmute, the next video they scroll to stays
/// unmuted - the alternative, re-muting on every post, means tapping the
/// same button over and over down the feed.
@MainActor
final class FeedAudio: ObservableObject {
    static let shared = FeedAudio()

    @Published private(set) var muted = true

    func toggle() {
        muted.toggle()
        guard !muted else { return }
        // Without this, unmuting does nothing at all while the ringer
        // switch is on silent - the default category is silenced by it,
        // so the button would look broken. Only raised when sound is
        // actually wanted, and off the main thread because activating a
        // session is slow enough to be felt.
        Task.detached(priority: .userInitiated) {
            let session = AVAudioSession.sharedInstance()
            try? session.setCategory(.playback)
            try? session.setActive(true)
        }
    }
}

@MainActor
private enum MediaSizeCache {
    /// Decoded thumbnails, kept by file hash.
    ///
    /// Both the shape of the media area and the image drawn in it used to
    /// be derived by building a UIImage from the thumbnail bytes inside
    /// the view body - which re-runs constantly while a feed scrolls, so
    /// every visible post re-parsed every one of its images many times a
    /// second, on the main thread, with the scrolling itself waiting
    /// behind it. They are decoded off the main thread now (decode(_:)),
    /// ready to draw, and the shape is read from the header alone.
    ///
    /// NSCache rather than a plain dictionary so this gives the memory
    /// back under pressure instead of growing for the life of the app.
    /// Bounded by bytes, not just by count. A feed "thumbnail" off the
    /// device is a 1000px JPEG - measured at 1000x1333, 325KB on disk but
    /// **5.3MB** once decoded into pixels. At the old count-only limit of
    /// 200 that is over a gigabyte of images this cache would happily
    /// hold, which a phone answers with memory warnings and eventually by
    /// killing the app. Counting the decoded footprint keeps it near the
    /// 64MB below whatever the images happen to be.
    private static let images: NSCache<NSString, UIImage> = {
        let cache = NSCache<NSString, UIImage>()
        cache.countLimit = 200
        cache.totalCostLimit = 64 << 20
        return cache
    }()

    /// Each thumbnail's width / height, from its header: the box's shape
    /// on the very first frame, before its pixels are decoded (a box that
    /// changed height a frame later made the feed jump).
    private static let aspects: NSCache<NSString, NSNumber> = {
        let cache = NSCache<NSString, NSNumber>()
        cache.countLimit = 2000
        return cache
    }()

    /// What holding this image actually costs in memory: its pixels, not
    /// the compressed bytes it arrived as.
    private static func cost(of image: UIImage) -> Int {
        Int(image.size.width * image.scale * image.size.height * image.scale) * 4
    }

    /// Already decoded, or nil: never decodes, so it is cheap in a body.
    static func cachedImage(of file: Msg_File) -> UIImage? {
        images.object(forKey: file.hash as NSString)
    }

    /// Decoded off the main thread, ready to draw (preparingForDisplay),
    /// and kept; nil for no thumbnail or one that doesn't decode.
    static func decode(_ file: Msg_File) async -> UIImage? {
        if let cached = cachedImage(of: file) { return cached }
        guard file.hasContent else { return nil }
        let data = file.content
        let key = file.hash as NSString
        let ui = await Task.detached(priority: .userInitiated) { () -> UIImage? in
            guard let image = UIImage(data: data) else { return nil }
            return image.preparingForDisplay() ?? image
        }.value
        if let ui { images.setObject(ui, forKey: key, cost: cost(of: ui)) }
        return ui
    }

    /// The thumbnail's shape, from its header alone (ImageIO, its EXIF
    /// orientation applied); nil without one that can be read.
    static func aspect(of file: Msg_File) -> CGFloat? {
        let key = file.hash as NSString
        if let known = aspects.object(forKey: key) { return CGFloat(known.doubleValue) }
        if let ui = cachedImage(of: file), ui.size.width > 0, ui.size.height > 0 {
            return remember(ui.size.width / ui.size.height, for: key)
        }
        guard file.hasContent, let aspect = headerAspect(file.content) else { return nil }
        return remember(aspect, for: key)
    }

    private static func remember(_ aspect: CGFloat, for key: NSString) -> CGFloat {
        aspects.setObject(NSNumber(value: Double(aspect)), forKey: key)
        return aspect
    }

    /// Width / height from an image's header, turned as its orientation
    /// says (5-8 are a quarter turn).
    nonisolated static func headerAspect(_ data: Data) -> CGFloat? {
        guard let src = CGImageSourceCreateWithData(data as CFData, nil),
              let props = CGImageSourceCopyPropertiesAtIndex(src, 0, nil) as? [CFString: Any],
              let w = (props[kCGImagePropertyPixelWidth] as? NSNumber)?.doubleValue,
              let h = (props[kCGImagePropertyPixelHeight] as? NSNumber)?.doubleValue,
              w > 0, h > 0 else { return nil }
        let orientation = (props[kCGImagePropertyOrientation] as? NSNumber)?.intValue ?? 1
        return CGFloat(orientation >= 5 && orientation <= 8 ? h / w : w / h)
    }
}

/// A post's thumbnail, decoded off the main thread (MediaSizeCache): what
/// is decoded already shows at once, the rest after a frame or two in the
/// same box, so nothing moves.
private struct FeedThumbnail<Placeholder: View, Content: View>: View {
    let file: Msg_File
    @ViewBuilder let placeholder: (_ failed: Bool) -> Placeholder
    @ViewBuilder let content: (UIImage) -> Content
    @State private var image: UIImage?
    @State private var failed = false

    init(file: Msg_File, @ViewBuilder placeholder: @escaping (_ failed: Bool) -> Placeholder, @ViewBuilder content: @escaping (UIImage) -> Content) {
        self.file = file
        self.placeholder = placeholder
        self.content = content
        _image = State(initialValue: MediaSizeCache.cachedImage(of: file))
    }

    var body: some View {
        Group {
            if let image { content(image) } else { placeholder(failed) }
        }
        .task(id: file.hash) {
            guard image == nil else { return }
            image = await MediaSizeCache.decode(file)
            failed = image == nil
        }
    }
}

/// Issue #123: the widest the feed column gets. A phone is narrower than
/// this so it changes nothing there; on an iPad it keeps a post the shape
/// of a post (Instagram's web feed picks about the same figure) instead of
/// stretching a portrait video to 820pt of mostly letterbox.
private let cFeedMaxWidth: CGFloat = 600

/// How a post's media is framed (the owner, 2026-10-07: "the height of the
/// photos should never be more than the height of the screen or the frame
/// where they are represented, if not the user can't see the full media
/// for vertical media"). The same three rules on the web, iOS and Android:
///
/// 1. The media box is never taller than the space it is shown in: the
///    feed's visible height (the screen less the status, navigation and
///    tab bars, and the critical update banner while it shows - MainView
///    lays that out above the tabs, so it shortens this view rather than
///    covering it) less the post's own header above the media and a
///    small margin. So a post scrolled to sit right under the top bar -
///    which is where opening one from a notification puts it - shows its
///    whole media. It is measured, not assumed (see SocialFeedView's
///    feedViewportHeight), so rotation, split screen and Stage Manager
///    sizes recompute it.
/// 2. Nothing is ever cropped: photos, video posters and the playing
///    video are all shown whole (fit), letterboxed in black inside the
///    full-width box, and a poster and its player are given the very same
///    rect, so pressing play moves nothing (issue #112).
/// 3. The box takes its shape from the post's first item, one ratio for
///    the whole post, so swiping a carousel never resizes the card (issue
///    #27), clamped only on the wide side, to 1.91:1.
///
/// There used to be a 4:5 floor on that ratio as well, Instagram's, from
/// when a 9:16 item (most of this library) shown at its own ratio was
/// taller than the screen and put the comments out of reach. Rule 1 now
/// does that job, so a vertical photo or a 9:16 video gets all the height
/// the screen can give it instead of being shrunk into a 4:5 box - which
/// is also what replaced issue #112's follow-up cap of 80% of the screen
/// height, an iPad-only backstop that did nothing for a phone.
private let cFeedMaxAspect: CGFloat = 1.91

/// The box's shape while the post's first item has no thumbnail to read
/// one from (a missing or undecodable one). Rule 1 still caps it.
private let cFeedFallbackAspect: CGFloat = 4.0 / 5.0

/// Rule 1's margin: the gap left between the media's bottom edge and the
/// bottom bar when the post's header sits right under the top bar.
private let cMediaHeightMargin: CGFloat = 8

/// The least height a media box is given whatever the window, so a
/// window too short to hold even a header (an iPad's smallest Stage
/// Manager size, say) can't collapse the box to nothing.
private let cMinMediaHeight: CGFloat = 120

/// One measurement of the feed's scroll view, for rule 1's visible height.
private struct FeedViewportMeasure: Equatable, Sendable {
    let width: CGFloat
    let height: CGFloat
    /// What covers the bottom of the frame: the tab bar and home
    /// indicator, plus the keyboard when it is up.
    let bottomInset: CGFloat

    /// Whether this differs from `base` only by the keyboard: the same
    /// width, the same distance from the frame's top to the bottom of the
    /// screen, and more of it covered at the bottom. That is the keyboard
    /// coming up, or being dragged down, not the window changing - a
    /// rotation changes the width, and a banner or a taller navigation
    /// bar moves the top.
    func isKeyboard(after base: FeedViewportMeasure) -> Bool {
        abs(width - base.width) < 0.5
            && abs((height + bottomInset) - (base.height + base.bottomInset)) < 0.5
            && bottomInset > base.bottomInset + 0.5
    }
}

/// Sizes a post's media box: the whole width the feed column offers, and
/// the height that width gives the box's ratio - but never more than
/// `maxHeight` (rule 1 above). Whatever is inside is offered exactly that
/// box.
///
/// A Layout rather than `.aspectRatio(.fit).frame(maxHeight:)`: in a
/// vertical scroll view nothing proposes a height, and a frame with only a
/// maximum then lets an aspect-ratio child report its natural, taller
/// size and spill out of the frame instead of shrinking into it. It also
/// means the box needs no width worked out from the screen, which is
/// wrong in split screen.
private struct MediaBoxLayout: Layout {
    let aspect: CGFloat
    let maxHeight: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        var width = proposal.width ?? .infinity
        if !width.isFinite { width = maxHeight * aspect }

        return CGSize(width: width, height: min(width / aspect, maxHeight))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        for subview in subviews {
            subview.place(at: CGPoint(x: bounds.midX, y: bounds.midY), anchor: .center, proposal: ProposedViewSize(bounds.size))
        }
    }
}

/// The feed's video player: a plain AVPlayerLayer showing the whole frame
/// (resizeAspect), letterboxed in black. It is given the same rect as the
/// poster it replaces (see PostCard.videoContent), so the two line up.
///
/// It used to crop instead (resizeAspectFill), which is how Instagram
/// shows a 9:16 clip in a 4:5 feed slot - and which cut the sides off
/// every video in the feed. Nothing in the feed is cropped any more (rule
/// 2 above).
///
/// A plain AVPlayerLayer, deliberately not AVPlayerViewController.
///
/// The feed shows no transport controls, so the whole view controller was
/// machinery being paid for and not used - and it is not cheap machinery.
/// Caught on a device with the debugger paused during the freeze, the main
/// thread was here:
///
///     AVPlayerViewControllerContentView.layoutSubviews
///       -> _updateVideoGravityDuringLayoutSubviews...
///         -> AVPlayerLayer.setBounds:      (KVO on videoBounds fires)
///           -> AVPlayerLayer.videoRect -> _presentationSize
///             -> CFPreferencesCopyAppValue   (cold path)
///               -> os_log -> stringWithFormat -> CFString alloc/dealloc
///
/// That is a preferences lookup and a log format, synchronously, inside a
/// layout pass, every time a player layer's bounds change - once per
/// player, per layout, and the feed autoplays. Setting videoGravity on a
/// bare layer gets the identical picture with none of it: nothing
/// observes videoBounds, so nothing recomputes videoRect during layout.
///
/// SwiftUI's own VideoPlayer is not an option either, letterboxing or
/// not: it wraps the same AVPlayerViewController, freeze and all.
final class PlayerLayerView: UIView {
    override static var layerClass: AnyClass { AVPlayerLayer.self }

    var playerLayer: AVPlayerLayer {
        // Safe by construction: layerClass above is what backs this view.
        layer as! AVPlayerLayer // swiftlint:disable:this force_cast
    }
}

private struct FeedVideoPlayer: UIViewRepresentable {
    let player: AVPlayer

    func makeUIView(context: Context) -> PlayerLayerView {
        let view = PlayerLayerView()
        view.backgroundColor = .black
        view.playerLayer.videoGravity = .resizeAspect
        view.playerLayer.player = player

        return view
    }

    func updateUIView(_ view: PlayerLayerView, context: Context) {
        if view.playerLayer.player !== player { view.playerLayer.player = player }
    }

    static func dismantleUIView(_ view: PlayerLayerView, coordinator: Coordinator) {
        // Detached explicitly rather than left to deallocation: a layer
        // still holding a player keeps it decoding for as long as the
        // layer is alive.
        view.playerLayer.player = nil
    }
}

private struct PostCard: View {
    let box: PostBox
    /// The first post: its heart and comment field are what the tour's
    /// step about Social rings.
    var tourMarks = false
    /// The feed's visible height, which rule 1 (see cFeedMaxAspect) keeps
    /// this post's media inside - see SocialFeedView.feedViewportHeight.
    let viewportHeight: CGFloat

    /// Computed, not stored: a stored protobuf value is precisely what
    /// sends AttributeGraph walking (see PostBox).
    private var post: Msg_SocialPublication { box.pub }
    // Issue #78: brief "here's what you tapped" flash after opening this
    // post/comment from a notification - cleared by the parent view after
    // a couple of seconds, same as the web version's own fade-out.
    var isHighlighted: Bool = false
    var highlightCommentUuid: String? = nil
    let onLikePub: () -> Void
    let onLikeComment: (String) -> Void
    /// The comment's text; false: the device didn't take it, and the text
    /// goes back in the field.
    let onComment: (String) async -> Bool
    let fetchPublicationLikers: (String) async -> [Msg_Profile]
    let fetchCommentLikers: (String) async -> [Msg_Profile]
    let onDeletePub: () -> Void
    let onDeleteComment: (String) -> Void

    @State private var currentImage = 0
    @State private var commentText = ""
    @State private var likersTarget: LikersTarget?
    // Issues #34/#35: confirms before actually deleting — a post outright,
    // or (since #35 allows deleting any comment on your own post) whichever
    // comment's trash icon was tapped.
    @State private var confirmDeletePost = false
    @State private var commentPendingDelete: String?

    // Issue #28: pinch to zoom, snapping back to original size on release,
    // zooming around wherever the fingers actually are (MagnifyGesture's
    // startAnchor) rather than always the image's center. @GestureState
    // resets to its initial value automatically the instant the gesture
    // ends, which is exactly "zoom while pinching, let go and it's back to
    // normal" with no extra bookkeeping needed. Two separate properties
    // (rather than one tuple) since .animation(value:) needs an Equatable
    // value and tuples can't conform to that.
    @GestureState private var pinchScale: CGFloat = 1.0
    @GestureState private var pinchAnchor: UnitPoint = .center

    // Issue #60: a video post shows its thumbnail as a poster with a play
    // button until tapped, then fetches the real bytes and plays them
    // in-place. Scoped to whichever file is currently showing - reset by
    // .onChange(of: currentImage) below so paging away from a video
    // doesn't leave its player state applying to the next file.
    @State private var videoPlaybackURL: URL?
    @State private var loadingVideo = false
    // Issue #107: the player is held, not constructed inline in the body.
    // `VideoPlayer(player: AVPlayer(url:))` builds a brand new player
    // every time SwiftUI re-evaluates this view - and a feed re-evaluates
    // constantly (paging, likes, the relative-time labels ticking) - so
    // playback was torn down and restarted from zero each time, which is
    // exactly the reported "spinner shows, video never plays".
    @State private var videoPlayer: AVPlayer?
    /// Shows the replay button: this post's clip has run out.
    @State private var videoEnded = false
    @ObservedObject private var feedAudio = FeedAudio.shared
    /// Read for headerHeight, which follows the text size.
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    // Instagram-style, edge-to-edge feed (issue #12): no card background or
    // rounded frame around the whole post, and the image spans the full
    // screen width at its own real aspect ratio instead of being cropped
    // into a fixed box — everything else (header, caption, actions,
    // comments) gets its own modest horizontal inset instead.
    private let sidePadding: CGFloat = 12
    /// The card's vertical spacing, the header's top padding and the
    /// avatar's size - named because headerHeight adds them up.
    private let cardSpacing: CGFloat = 8
    private let headerTopPadding: CGFloat = 10
    private let avatarSize: CGFloat = 28
    /// A comment's writer, in bold before the comment.
    private static var bold: AttributeContainer {
        var b = AttributeContainer()
        b.inlinePresentationIntent = .stronglyEmphasized
        return b
    }

    var body: some View {
        VStack(alignment: .leading, spacing: cardSpacing) {
            HStack(spacing: 8) {
                avatarView(data: post.publisher.hasImage ? post.publisher.image : nil, size: avatarSize)
                // One line each, truncated, at every text size: headerHeight
                // counts exactly one name line and one date line, so a
                // name wrapping to two (a long one, or any at the largest
                // sizes) would push the media's bottom under the tab bar.
                VStack(alignment: .leading, spacing: 1) {
                    Text(post.publisher.name.isEmpty ? S.appSocialPostUnnamed : post.publisher.name)
                        .font(.subheadline).bold()
                        .lineLimit(1)
                        .truncationMode(.tail)
                    if post.hasDateTime {
                        // Reckoned again every minute while it shows.
                        TimelineView(.everyMinute) { context in
                            Text(formatPostDate(post.dateTime.date, now: context.date))
                                .font(.caption2)
                                .foregroundColor(.secondary)
                                .lineLimit(1)
                                .truncationMode(.tail)
                        }
                    }
                }
                Spacer()
                // Issue #34: delete one of your own posts.
                if post.own {
                    Menu {
                        Button(S.appSocialPostDeletePost, role: .destructive) { confirmDeletePost = true }
                    } label: {
                        Image(systemName: "ellipsis").foregroundColor(.secondary)
                            .padding(.horizontal, 6) // bigger tap target than the glyph alone
                    }
                    .accessibilityLabel(S.appSocialPostMoreOptions)
                }
            }
            .padding(.horizontal, sidePadding)
            .padding(.top, headerTopPadding)

            if !post.files.isEmpty {
                let file = post.files[min(currentImage, post.files.count - 1)]
                ZStack(alignment: .bottom) {
                    // Just the image — no tap action of its own, unless
                    // it's a video (issue #60), which taps to play in
                    // place. Timeline images are otherwise browse-only on
                    // iOS; the full-screen popup only makes sense on the
                    // web (where there's no native photo app to fall back
                    // on).
                    //
                    // Issue #109: with more than one, they page with the
                    // finger - the images move as you drag and snap when
                    // you let go, instead of only swapping once the
                    // gesture ended (which made a swipe feel like it had
                    // done nothing until it suddenly had). That is what a
                    // paging TabView does natively, so it replaces the
                    // hand-rolled DragGesture that used to sit below;
                    // it needs a definite size, which is exactly the box
                    // MediaBoxLayout gives every item of the post.
                    carouselContent(for: file)
                        // Issue #114: a video starts when you scroll onto
                        // it and stops when you leave, so the feed plays
                        // itself. The threshold means "mostly on screen",
                        // which is also what keeps two videos from
                        // playing at once - only one post can be that
                        // visible at a time.
                        .onScrollVisibilityChange(threshold: 0.6) { visible in
                            if visible {
                                autoplayIfVideo(file)
                            } else {
                                videoPlayer?.pause()
                            }
                        }
                        // The sound toggle sits on the video's own corner,
                        // not the box's: a vertical clip is letterboxed
                        // in a wider box (rule 2), and on the black bar
                        // beside it the button would read as belonging
                        // to nothing. Over the whole carousel rather than
                        // inside each page, so the pages' tap zones (see
                        // carouselContent) can't take its taps; the clear
                        // frame it hangs from takes no touches itself.
                        .overlay {
                            if file.mime.hasPrefix("video/") {
                                Color.clear
                                    .aspectRatio(mediaAspect(for: file) ?? boxAspect, contentMode: .fit)
                                    .overlay(alignment: .topTrailing) {
                                        Button { feedAudio.toggle() } label: {
                                            Image(systemName: feedAudio.muted ? "speaker.slash.fill" : "speaker.wave.2.fill")
                                                .font(.system(size: 13, weight: .semibold))
                                                .foregroundStyle(.white)
                                                .padding(8)
                                                .background(.black.opacity(0.45), in: Circle())
                                        }
                                        // "Mute", said as selected while the feed's
                                        // videos are muted (as Android).
                                        .accessibilityLabel(S.appSocialPostMute)
                                        .accessibilityAddTraits(feedAudio.muted ? .isSelected : [])
                                        .padding(10)
                                    }
                            }
                        }
                        .contentShape(Rectangle())
                        .scaleEffect(pinchScale, anchor: pinchAnchor)
                        .animation(.spring(response: 0.3, dampingFraction: 0.7), value: pinchScale)
                        .zIndex(pinchScale > 1 ? 1 : 0)
                        .onChange(of: currentImage) { _, _ in
                            discardDownloadedVideo()
                            videoPlayer?.pause()
                            videoPlayer = nil
                            videoPlaybackURL = nil
                            loadingVideo = false
                        }

                    if post.files.count > 1 {
                        // Issue #20's left/right tap zones used to sit
                        // here, as a Color.clear layer over the whole
                        // media area. That layer is hit-testable, so it
                        // took every touch before the paging scroll view
                        // underneath could see it - and a tap gesture
                        // doesn't hand a drag back down, so sliding
                        // between pictures did nothing at all on iOS
                        // (issue #109). They now live inside each page
                        // (see carouselContent), where a tap and the
                        // scroll view's own pan coexist normally.
                        HStack {
                            ForEach(0..<post.files.count, id: \.self) { i in
                                Circle()
                                    .fill(i == currentImage ? Color.white : Color.white.opacity(0.4))
                                    .frame(width: 6, height: 6)
                            }
                        }
                        .padding(6)
                        .background(.black.opacity(0.3), in: Capsule())
                        .padding(.bottom, 8)
                        // Purely an indicator, and it sits right where a
                        // drag is likely to start.
                        .allowsHitTesting(false)
                    }
                }
                // Issue #13's swipe now comes from the paging TabView in
                // carouselContent (issue #109), which follows the finger
                // rather than acting only once the drag has ended.
                // Issue #28: pinch to zoom, attached at this level (not
                // directly on the image) and as a *simultaneous* gesture —
                // the invisible left/right tap zones (issue #20) sit on top
                // of the image and were otherwise claiming every touch,
                // including a pinch's, before it ever reached a gesture
                // attached to the image itself. MagnifyGesture (rather than
                // the older MagnificationGesture) carries a startAnchor, so
                // the zoom centers on wherever the fingers actually are
                // instead of always the image's center.
                .simultaneousGesture(
                    MagnifyGesture()
                        .updating($pinchScale) { value, state, _ in
                            state = value.magnification
                        }
                        .updating($pinchAnchor) { value, state, _ in
                            state = value.startAnchor
                        }
                )
            }

            HStack(spacing: 16) {
                Button {
                    onLikePub()
                } label: {
                    Image(systemName: post.liked ? "heart.fill" : "heart")
                        .foregroundColor(post.liked ? .red : .primary)
                }
                // Liked is said as selected too, as on Android.
                .accessibilityLabel(post.liked ? S.appSocialPostUnlike : S.appSocialPostLike)
                .accessibilityAddTraits(post.liked ? .isSelected : [])
                .tourTarget(tourMarks ? .like : nil)
            }
            .font(.title3)
            .padding(.horizontal, sidePadding)
            .padding(.top, 4)

            // Issue #29: tap the like count to see who liked it — kept
            // separate from the heart button above, which just toggles
            // your own like.
            if post.likes > 0 {
                Button {
                    likersTarget = LikersTarget(kind: .publication(post.uuid))
                } label: {
                    Text(S.appSocialPostLikes(count: Int(post.likes)))
                        .font(.footnote).bold()
                        .foregroundColor(.primary)
                }
                .padding(.horizontal, sidePadding)
            }

            if !post.text.isEmpty {
                Text(post.text).font(.body)
                    .padding(.horizontal, sidePadding)
            }

            if !post.comments.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(post.comments, id: \.commentUuid) { c in
                        HStack {
                            Text(c.publisher.isEmpty
                                 ? S.appSocialPostCommentUnnamed(comment: c.comment, b: Self.bold)
                                 : S.appSocialPostComment(name: c.publisher, comment: c.comment, b: Self.bold))
                            Spacer()
                            // Issue #29: tapping the count (not the heart
                            // itself) shows who liked this comment.
                            if c.likes > 0 {
                                Button {
                                    likersTarget = LikersTarget(kind: .comment(c.commentUuid))
                                } label: {
                                    Text("\(c.likes)").foregroundColor(.secondary)
                                }
                            }
                            Button {
                                onLikeComment(c.commentUuid)
                            } label: {
                                Image(systemName: c.liked ? "heart.fill" : "heart")
                                    .foregroundColor(c.liked ? .red : .secondary)
                            }
                            .accessibilityLabel(c.liked ? S.appSocialPostUnlikeComment : S.appSocialPostLikeComment)
                            .accessibilityAddTraits(c.liked ? .isSelected : [])
                            // Issue #35: on your own post, any comment can
                            // be deleted — not just ones you wrote. Issue
                            // #174: and a comment you wrote can be deleted
                            // on anyone's post.
                            if post.own || c.own {
                                Button {
                                    commentPendingDelete = c.commentUuid
                                } label: {
                                    Image(systemName: "trash").foregroundColor(.secondary)
                                }
                                .accessibilityLabel(S.appSocialPostDeleteCommentLabel)
                            }
                        }
                        .font(.caption)
                        .padding(4)
                        .background(
                            c.commentUuid == highlightCommentUuid ? Color.yellow.opacity(0.18) : Color.clear,
                            in: RoundedRectangle(cornerRadius: 6)
                        )
                    }
                }
                .padding(.horizontal, sidePadding)
            }

            HStack {
                TextField(S.appSocialPostCommentPlaceholder, text: $commentText)
                    .textFieldStyle(.roundedBorder)
                    .font(.caption)
                    // The keyboard's Send posts it, as the button beside it.
                    .submitLabel(.send)
                    .onSubmit(sendComment)
                Button(S.appSocialPostSendComment, action: sendComment)
                .font(.caption)
                .disabled(commentText.trimmingCharacters(in: .whitespaces).isEmpty)
            }
            .tourTarget(tourMarks ? .comment : nil)
            .padding(.horizontal, sidePadding)
            .padding(.bottom, 10)
        }
        // Issue #78: the brief highlight after opening this post from a
        // notification.
        .background(isHighlighted ? Color.yellow.opacity(0.12) : Color.clear)
        .animation(.easeInOut(duration: 0.4), value: isHighlighted)
        .sheet(item: $likersTarget) { target in
            LikersListView(target: target, fetchPublicationLikers: fetchPublicationLikers, fetchCommentLikers: fetchCommentLikers)
        }
        .confirmationDialog(S.appSocialPostDeletePostTitle, isPresented: $confirmDeletePost, titleVisibility: .visible) {
            Button(S.appSocialPostDeletePost, role: .destructive, action: onDeletePub)
            Button(S.commonCancel, role: .cancel) {}
        }
        .confirmationDialog(
            S.appSocialPostDeleteCommentTitle,
            isPresented: Binding(get: { commentPendingDelete != nil }, set: { if !$0 { commentPendingDelete = nil } }),
            titleVisibility: .visible
        ) {
            Button(S.appSocialPostDeleteComment, role: .destructive) {
                if let uuid = commentPendingDelete { onDeleteComment(uuid) }
            }
            Button(S.commonCancel, role: .cancel) {}
        }
    }

    /// The field is cleared as the comment goes out; one the device didn't
    /// take comes back, unless something new was typed meanwhile.
    private func sendComment() {
        let text = commentText
        guard !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        commentText = ""
        Task {
            if !(await onComment(text)), commentText.isEmpty { commentText = text }
        }
    }

    /// The actual post image, full width, whole - never cropped (rule 2,
    /// see cFeedMaxAspect). `.aspectRatio(contentMode: .fit)` computes its
    /// size from the box it is offered, so unlike the `scaledToFill()` this
    /// replaced, it can't report an oversized ideal size that pushes the
    /// view wider than the screen. Issue #60: a video file renders as its
    /// own poster-plus-play-button (see videoContent) instead.
    @ViewBuilder
    private func mediaContent(for file: Msg_File) -> some View {
        if file.mime.hasPrefix("video/") {
            videoContent(for: file)
        } else {
            FeedThumbnail(file: file) { failed in
                // The same box as everything else in the post, so a
                // thumbnail that arrives later, or never, can't change the
                // card's height.
                feedBox(background: Color.secondary.opacity(0.08)) {
                    if failed { Image(systemName: "photo").font(.largeTitle).foregroundColor(.secondary) }
                }
            } content: { ui in
                // .fit, never .fill: a photo is never cut. The first item's
                // ratio shapes the box, so it fills it exactly unless it is
                // wider than 1.91:1 or taller than the screen leaves room for;
                // anything else in the post is letterboxed in that box.
                // Cropping these is what broke horizontal photos, especially
                // in a post whose first item is portrait and therefore sets a
                // tall box.
                feedBox {
                    Image(uiImage: ui).resizable().aspectRatio(contentMode: .fit)
                }
            }
        }
    }

    /// The shape of a post's media, read from its own thumbnail. A
    /// video's thumbnail is a frame of that video (see UploadFile's
    /// background processing), so it carries exactly the aspect ratio the
    /// player will have - which is what lets the player be given the
    /// poster's rect before a single byte of video has been decoded.
    private func mediaAspect(for file: Msg_File) -> CGFloat? {
        MediaSizeCache.aspect(of: file)
    }

    /// One post's media area: a single item on its own, or a paging
    /// carousel that moves with the finger when there are several
    /// (issue #109).
    ///
    /// Issue #27: with more than one item in a post, the media area keeps
    /// one size rather than resizing per item - swiping between mixed
    /// aspect ratios used to make the whole card jump taller and shorter
    /// on every image. It is the same box every item already draws into
    /// (see feedBox), given to the TabView too, which needs a definite
    /// size to page in.
    @ViewBuilder
    private func carouselContent(for current: Msg_File) -> some View {
        if post.files.count > 1 {
            MediaBoxLayout(aspect: boxAspect, maxHeight: maxMediaHeight) {
                TabView(selection: $currentImage) {
                    ForEach(0..<post.files.count, id: \.self) { i in
                        mediaContent(for: post.files[i])
                            // Issue #20, kept: tapping the left or right
                            // quarter pages through. Inside the page rather
                            // than over the whole carousel, so the scroll
                            // view still gets the drag.
                            .overlay {
                                HStack(spacing: 0) {
                                    Color.clear
                                        .contentShape(Rectangle())
                                        .onTapGesture { if currentImage > 0 { currentImage -= 1 } }
                                        .frame(maxWidth: .infinity)
                                    Color.clear.frame(maxWidth: .infinity)
                                    Color.clear
                                        .contentShape(Rectangle())
                                        .onTapGesture { if currentImage < post.files.count - 1 { currentImage += 1 } }
                                        .frame(maxWidth: .infinity)
                                }
                            }
                            .tag(i)
                    }
                }
                // The dots are drawn by this card itself, over the image -
                // TabView's own index view would sit below it and duplicate
                // them.
                .tabViewStyle(.page(indexDisplayMode: .never))
            }
        } else {
            mediaContent(for: current)
        }
    }

    /// One box for this post's media (rules 1 and 3, see cFeedMaxAspect):
    /// full width, boxAspect's shape, no taller than maxMediaHeight. Every
    /// item of the post, and a video's poster and player alike, draw into
    /// it, so neither swiping nor pressing play can reshape the post
    /// (issues #27 and #112). What goes inside is fitted, never cropped,
    /// and centred on the letterbox colour.
    private func feedBox<Content: View>(background: Color = .black, @ViewBuilder content: () -> Content) -> some View {
        MediaBoxLayout(aspect: boxAspect, maxHeight: maxMediaHeight) {
            ZStack {
                background
                content()
            }
        }
        .clipped()
    }

    /// The shape of this post's media box: one ratio for the whole post,
    /// taken from its first item as Instagram does, so swiping a carousel
    /// can't resize the card. Clamped on the wide side only (see
    /// cFeedMaxAspect); a tall item is held back by maxMediaHeight
    /// instead.
    private var boxAspect: CGFloat {
        guard let first = post.files.first, let aspect = mediaAspect(for: first) else { return cFeedFallbackAspect }

        return min(aspect, cFeedMaxAspect)
    }

    /// How much of this card sits above its media: the header row (the
    /// avatar beside the name and date lines - one line each, see the
    /// header's lineLimit), its top padding and the card's spacing below
    /// it. Worked out from the fonts at the current
    /// text size rather than measured, so it is known before the card is
    /// first laid out: a measured value would land a pass later and resize
    /// cards that are already on screen, or above it, which is what makes
    /// a feed jump.
    private var headerHeight: CGFloat {
        let traits = UITraitCollection(preferredContentSizeCategory: UIContentSizeCategory(dynamicTypeSize))
        let name = UIFont.preferredFont(forTextStyle: .subheadline, compatibleWith: traits).lineHeight
        let date = UIFont.preferredFont(forTextStyle: .caption2, compatibleWith: traits).lineHeight

        return headerTopPadding + max(avatarSize, ceil(name + 1 + date)) + cardSpacing
    }

    /// The tallest this post's media may be (rule 1, see cFeedMaxAspect):
    /// the feed's visible height less this card's header and a margin, so
    /// with the header right under the top bar the whole media shows.
    /// Applied to every item, the poster and the player alike, so capping
    /// one can't reintroduce the mismatch issue #112 fixed.
    private var maxMediaHeight: CGFloat {
        max(viewportHeight - headerHeight - cMediaHeightMargin, cMinMediaHeight)
    }

    /// Issue #60: a video post shows its thumbnail as a poster with a play
    /// button; tapping it fetches the actual file (GetFile, same RPC an
    /// image would use for a hi-res view on web - iOS has no such hi-res
    /// step for images today, so this is the first user of it here) and
    /// plays it in place once the bytes arrive.
    @ViewBuilder
    private func videoContent(for file: Msg_File) -> some View {
        if let player = videoPlayer {
            // Issue #112: the player takes the very same box the poster
            // just occupied, so pressing play can't reshape the post. It
            // used to fall back to a fixed 320pt-tall box that had
            // nothing to do with the video's shape. Within the box it
            // gets the poster's own rect too - the thumbnail's ratio,
            // fitted - so the picture doesn't shift either; the layer
            // fits the video inside that, so a thumbnail a pixel off the
            // video's shape still crops nothing.
            feedBox {
                FeedVideoPlayer(player: player)
                    .aspectRatio(mediaAspect(for: file) ?? boxAspect, contentMode: .fit)
            }
            // Replay, over the frame the clip stopped on. Without it a
            // finished video is a dead end: play() on a player sitting at
            // the end does nothing, so scrolling away and back doesn't
            // restart it either - the seek is what does.
            .overlay {
                if videoEnded {
                    Button {
                        videoEnded = false
                        if videoPlayerIsStale {
                            dropVideoPlayer()
                            loadAndPlayVideo(file)
                            return
                        }
                        player.seek(to: .zero)
                        player.play()
                    } label: {
                        Image(systemName: "arrow.counterclockwise.circle.fill")
                            .font(.system(size: 56))
                            .foregroundStyle(.white)
                            .shadow(radius: 4)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(S.appSocialPostReplay)
                }
            }
            // Tapping the poster is the play gesture - starting
            // playback here means that tap does what it looks like it
            // does, instead of landing on a paused player that needs a
            // second tap on its own control.
            .onAppear { player.play() }
            // A downloaded one (the fallback below) is deleted as it leaves
            // the screen - one file was left in tmp for every play - and
            // fetched again should it come back; a stream just pauses.
            .onDisappear {
                player.pause()
                discardDownloadedVideo()
            }
            // Deliberately only cleared by the replay button above: the
            // notification fires for whichever item finished, and a post
            // has exactly one, so anything else would hide the button
            // while the video is still sitting on its last frame.
            .onReceive(NotificationCenter.default.publisher(
                for: AVPlayerItem.didPlayToEndTimeNotification
            )) { note in
                guard let item = note.object as? AVPlayerItem,
                      item === player.currentItem else { return }
                videoEnded = true
            }
            // Issue #114: the speaker button lives on this post but the
            // preference is shared, so a toggle anywhere reaches whatever
            // is currently playing.
            .onChange(of: feedAudio.muted) { _, muted in player.isMuted = muted }
        } else {
            ZStack {
                FeedThumbnail(file: file) { _ in
                    feedBox(background: Color.secondary.opacity(0.08)) {}
                } content: { ui in
                    // .fit, like a photo and like the player that takes
                    // over from it: shown whole, in the same rect, so
                    // pressing play neither crops nor moves anything. It
                    // was .fill while the player cropped (to 4:5), which
                    // cut the sides off every vertical video.
                    feedBox {
                        Image(uiImage: ui).resizable().aspectRatio(contentMode: .fit)
                    }
                }
                if loadingVideo {
                    ProgressView().tint(.white)
                } else {
                    Image(systemName: "play.circle.fill")
                        .font(.system(size: 56))
                        .foregroundStyle(.white)
                        .shadow(radius: 4)
                }
            }
            .contentShape(Rectangle())
            .onTapGesture { loadAndPlayVideo(file) }
            // A button to VoiceOver: "Play" (as Android's).
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(S.appSocialPostPlay)
            .accessibilityAddTraits(.isButton)
            .accessibilityAction { loadAndPlayVideo(file) }
        }
    }

    /// Issue #114: starts this post's video because it just scrolled into
    /// view. Muted, which is both what the issue asks for and the only
    /// way autoplay is acceptable in a feed - the speaker button above
    /// is what turns sound on.
    private func autoplayIfVideo(_ file: Msg_File) {
        guard file.mime.hasPrefix("video/") else { return }
        if videoPlayerIsStale { dropVideoPlayer() }
        if let player = videoPlayer {
            player.isMuted = feedAudio.muted
            player.play()
            return
        }
        loadAndPlayVideo(file)
    }

    /// Issue #190: a streaming player that can't play any more. One
    /// built at home streams from the device's home address, out of reach
    /// once the app connects another way, and a failed one (a token past
    /// its hour too) stays failed. Dropped, a URL for the route in use is
    /// asked for. A downloaded file is never stale.
    private var videoPlayerIsStale: Bool {
        guard let player = videoPlayer, let url = videoPlaybackURL, !url.isFileURL else { return false }
        if player.currentItem?.status == .failed { return true }
        return HomeMediaLoader.httpsURL(url) != nil && OTCConnection.shared.homeLink == nil
    }

    private func dropVideoPlayer() {
        videoPlayer?.pause()
        videoPlayer = nil
        videoPlaybackURL = nil
        videoEnded = false
    }

    /// The fallback's file (GetPublicationMedia), gone with its player; a
    /// streamed video is left as it is.
    private func discardDownloadedVideo() {
        guard let url = videoPlaybackURL, url.isFileURL else { return }
        dropVideoPlayer()
        Task.detached(priority: .utility) { try? FileManager.default.removeItem(at: url) }
    }

    /// Without a stream URL (an older device, or the device declining) the
    /// feed asks for the whole video in one message: held in memory while
    /// it is read and written out, so above this the poster stays instead
    /// (Android's FEED_FALLBACK_MAX_BYTES). 0, not known, is let through.
    nonisolated static let fallbackMaxBytes: Int64 = 50 << 20

    nonisolated static func fallbackAllowed(size: Int64) -> Bool { size <= fallbackMaxBytes }

    private func loadAndPlayVideo(_ file: Msg_File) {
        guard !loadingVideo, videoPlaybackURL == nil else { return }
        loadingVideo = true
        Task {
            defer { loadingVideo = false }

            // Issue #110: stream it when the device offers a URL -
            // playback starts on the first chunk instead of waiting for
            // the whole clip, and nothing is written to the phone. Small
            // clips the device declines fall through unchanged.
            if let streamURL = await MediaStream.url(forPublication: post.uuid, hash: file.hash) {
                // Same handoff as the downloaded case below - the player
                // view owns starting playback, so this must not differ.
                videoPlaybackURL = streamURL
                videoPlayer = await Self.preparedPlayer(for: streamURL, muted: feedAudio.muted)
                return
            }

            guard Self.fallbackAllowed(size: file.fileSize) else { return }
            do {
                // Issue #107: by hash, through the publication. A feed
                // file has no path - social_publications_files stores
                // (pos, uuid, hash, mime, size) and nothing else - so the
                // GetFile(path:) this used to send was always asking for
                // an empty path. That is the whole reason tapping a video
                // here span the spinner and never played anything.
                let resp = try await OTCConnection.shared.request { e in
                    var gm = Msg_GetPublicationMedia()
                    gm.pubUuid = post.uuid
                    gm.hash = file.hash
                    e.payload = .reqGetPublicationMedia(gm)
                }
                guard case .respFile(let rf) = resp.payload, rf.hasContent else { return }
                // Issue #107: the extension matters. AVFoundation decides
                // how to demux a file:// URL from its path extension, and
                // this always wrote ".mp4" regardless of what the bytes
                // were - so a QuickTime recording (video/quicktime, which
                // is what an iPhone produces and what this library is full
                // of) was handed to the player mislabelled and silently
                // refused to load.
                let ext = Self.fileExtension(forMime: rf.mime, path: file.path)
                let tmp = FileManager.default.temporaryDirectory
                    .appendingPathComponent(UUID().uuidString)
                    .appendingPathExtension(ext)
                // Off the main thread: this is a whole video file, and
                // writing it here used to stall the feed for as long as
                // the disk took.
                let content = rf.content
                try await Task.detached(priority: .userInitiated) {
                    try content.write(to: tmp)
                }.value
                videoPlaybackURL = tmp
                videoPlayer = await Self.preparedPlayer(for: tmp, muted: feedAudio.muted)
            } catch {
                // Leave the poster + play button in place - tapping again
                // retries, same as the rest of this app's best-effort
                // network calls.
            }
        }
    }

    /// Builds a player whose asset has already been inspected, off the
    /// main thread.
    ///
    /// Handing AVPlayerViewController a player made straight from a URL
    /// makes the controller ask that asset for its tracks and duration
    /// while it is laying itself out - on the main thread, with the
    /// answers for a streamed URL sitting behind a network round trip.
    /// That is the other half of the first-video freeze: not decoding,
    /// asking. Loading the properties here means the controller finds them
    /// already there and never blocks for them.
    private static func preparedPlayer(for url: URL, muted: Bool) async -> AVPlayer {
        await Task.detached(priority: .userInitiated) {
            // A downloaded file:// URL comes back as a plain AVURLAsset.
            let asset = MediaStream.asset(for: url)
            // Best-effort: an asset that can't answer still plays (or
            // still fails) exactly as it did before, just without the
            // head start.
            _ = try? await asset.load(.isPlayable, .tracks)
            let player = AVPlayer(playerItem: AVPlayerItem(asset: asset))
            player.isMuted = muted

            return player
        }.value
    }

    /// Maps a video's mime to the file extension AVFoundation needs to
    /// recognise it, falling back to the original file's own extension
    /// (the device stores what the phone recorded, so that is usually
    /// right) and finally to mp4.
    private static func fileExtension(forMime mime: String, path: String) -> String {
        switch mime.lowercased() {
        case "video/quicktime": return "mov"
        case "video/mp4", "video/x-m4v": return "mp4"
        case "video/x-matroska": return "mkv"
        case "video/3gpp": return "3gp"
        default:
            let own = (path as NSString).pathExtension
            return own.isEmpty ? "mp4" : own.lowercased()
        }
    }

}

// Issue #29: who liked a publication or a comment.

private struct LikersTarget: Identifiable {
    enum Kind {
        case publication(String)
        case comment(String)
    }
    let kind: Kind
    var id: String {
        switch kind {
        case .publication(let uuid): return "pub-\(uuid)"
        case .comment(let uuid): return "comment-\(uuid)"
        }
    }
}

private struct LikersListView: View {
    let target: LikersTarget
    let fetchPublicationLikers: (String) async -> [Msg_Profile]
    let fetchCommentLikers: (String) async -> [Msg_Profile]

    @State private var likers: [Msg_Profile]?
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            Group {
                if let likers {
                    if likers.isEmpty {
                        ContentUnavailableView(S.appSocialLikesEmpty, systemImage: "heart")
                    } else {
                        List(Array(likers.enumerated()), id: \.offset) { _, liker in
                            HStack(spacing: 12) {
                                avatarView(data: liker.hasImage ? liker.image : nil, size: 36)
                                Text(liker.name.isEmpty ? liker.domain : liker.name)
                            }
                        }
                        .listStyle(.plain)
                    }
                } else {
                    ProgressView()
                }
            }
            .navigationTitle(S.appSocialLikesTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button(S.commonDone) { dismiss() }
                }
            }
        }
        .task(id: target.id) {
            switch target.kind {
            case .publication(let uuid): likers = await fetchPublicationLikers(uuid)
            case .comment(let uuid): likers = await fetchCommentLikers(uuid)
            }
        }
    }
}

/// How the feed's loads went (Android's SocialFeedViewModel.State fields of
/// the same names), and what a load does to it: plain values, so the rules
/// can be tested without a device.
struct FeedLoadState: Equatable {
    var loading = false
    var loadingMore = false
    var hasLoadedOnce = false
    /// The last load of the feed failed, and why (nil: it didn't).
    var problem: LoadProblem?
    /// The next page failed: the end of the feed says so, with Try again.
    var moreFailed = false

    /// After a load of the feed: `problem` nil when it came. A refresh that
    /// worked also takes away "Couldn't load more posts" - the end of the
    /// feed is a new one (Android's afterLoad).
    func afterLoad(_ problem: LoadProblem?) -> FeedLoadState {
        var s = self
        s.problem = problem
        s.hasLoadedOnce = hasLoadedOnce || problem == nil
        s.moreFailed = moreFailed && problem != nil
        return s
    }

    /// The next page, claimed: nil when a load of the feed or of a page is
    /// already on its way (Android's claimMore).
    func claimMore() -> FeedLoadState? {
        guard !loading, !loadingMore else { return nil }
        var s = self
        s.loadingMore = true
        s.moreFailed = false
        return s
    }
}
