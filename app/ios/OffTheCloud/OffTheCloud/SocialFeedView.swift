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

// When a post was published, shown in its header. Relative for anything
// recent (the timescale people actually care about scrolling a feed),
// falling back to an absolute date once "3d ago" stops being more useful
// than the date itself.
private func formatPostDate(_ date: Date) -> String {
    let mins = Int(Date().timeIntervalSince(date) / 60)
    if mins < 1 { return "just now" }
    if mins < 60 { return "\(mins)m ago" }
    let hours = mins / 60
    if hours < 24 { return "\(hours)h ago" }
    let days = hours / 24
    if days < 7 { return "\(days)d ago" }
    let f = DateFormatter()
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

    private let ws = OTCConnection.shared

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
    private var endReached = false

    private var pollTask: Task<Void, Never>?

    /// On-disk snapshot of the last successfully loaded page (issue #22):
    /// read synchronously at init so the feed shows *something* the instant
    /// the app launches — even before the network request that refreshes it
    /// has a chance to complete — instead of a blank screen.
    private static let cacheURL = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("social_feed_cache.pb")

    init() {
        // Mark loading right away, synchronously, so the very first render
        // shows the loading state rather than a flash of "No posts yet".
        loading = true
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
        startAutoLoad()
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
    /// server actually answers (even with zero publications); it doesn't
    /// stop just because a request throws.
    private func startAutoLoad() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                if await self.loadFeed() {
                    self.hasLoadedOnce = true
                    break
                }
                try? await Task.sleep(nanoseconds: 4_000_000_000)
            }
            self?.pollTask = nil
        }
    }

    /// Re-fetches from the start. Returns whether the server actually
    /// answered (regardless of how many publications came back) so callers
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
        defer { loading = false }
        endReached = false
        let count = Int32(max(posts.count, Int(Self.pageSize)))
        return await fetchPage(total: count, excludeUuids: [], replacing: true)
    }

    /// Loads the next page once the feed has been scrolled near the end of
    /// what's currently loaded (issue #15: 4 at a time, fetched again before
    /// the user actually runs out of already-loaded posts).
    func loadMoreIfNeeded(current post: Msg_SocialPublication?) async {
        guard let post, !loading, !loadingMore, !endReached else { return }
        guard let idx = posts.firstIndex(where: { $0.uuid == post.uuid }) else { return }
        guard idx >= posts.count - 2 else { return }
        loadingMore = true
        defer { loadingMore = false }
        await fetchPage(total: Self.pageSize, excludeUuids: posts.map(\.uuid), replacing: false)
    }

    /// Re-fetches exactly what's already on screen (not just page one) so a
    /// like/comment updates counts without discarding posts the user has
    /// already scrolled down to load via pagination.
    private func refreshCurrentlyLoaded() async {
        let count = Int32(max(posts.count, Int(Self.pageSize)))
        await fetchPage(total: count, excludeUuids: [], replacing: true)
    }

    @discardableResult
    private func fetchPage(total: Int32, excludeUuids: [String], replacing: Bool) async -> Bool {
        var req = Msg_GetSocialPublications()
        req.total = total
        req.excludeUuids = excludeUuids
        do {
            let resp = try await ws.request { $0.payload = .reqGetSocialPublications(req) }
            if case .respSocialPublications(let sp) = resp.payload {
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
            }
            return true
        } catch {
            print("Social feed load failed, will retry:", error)
            return false
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
            let resp = try await ws.request { $0.payload = .reqLikePublication(req) }
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
            let resp = try await ws.request { $0.payload = .reqLikeComment(req) }
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

    func addComment(pubUuid: String, text: String) async {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, let idx = posts.firstIndex(where: { $0.uuid == pubUuid }) else { return }

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
        do {
            let resp = try await ws.request { $0.payload = .reqNewSocialComment(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                // Swap the placeholder for the server's real comment (uuid,
                // timestamp, anything anyone else posted meanwhile) — this
                // can happen quietly in the background since the user
                // already sees their comment.
                await refreshCurrentlyLoaded()
            } else {
                removeOptimisticComment(pubUuid: pubUuid, commentUuid: optimistic.commentUuid)
            }
        } catch {
            removeOptimisticComment(pubUuid: pubUuid, commentUuid: optimistic.commentUuid)
        }
    }

    private func removeOptimisticComment(pubUuid: String, commentUuid: String) {
        guard let idx = posts.firstIndex(where: { $0.uuid == pubUuid }) else { return }
        posts[idx].comments.removeAll { $0.commentUuid == commentUuid }
    }

    // Issue #29: who liked a publication/comment.
    func fetchPublicationLikers(_ pubUuid: String) async -> [Msg_Profile] {
        var req = Msg_GetPublicationLikers()
        req.pubUuid = pubUuid
        guard let resp = try? await ws.request({ $0.payload = .reqGetPublicationLikers(req) }) else { return [] }
        if case .respLikers(let l) = resp.payload { return l.likers }
        return []
    }

    func fetchCommentLikers(_ commentUuid: String) async -> [Msg_Profile] {
        var req = Msg_GetCommentLikers()
        req.commentUuid = commentUuid
        guard let resp = try? await ws.request({ $0.payload = .reqGetCommentLikers(req) }) else { return [] }
        if case .respLikers(let l) = resp.payload { return l.likers }
        return []
    }

    // Issue #34: delete one of your own posts. Removed locally only once
    // the server confirms it — this hits the DB (files, comments, likes),
    // not something to optimistically guess at.
    func deletePublication(_ pubUuid: String) async {
        var req = Msg_DelSocialPublication()
        req.pubUuid = pubUuid
        do {
            let resp = try await ws.request { $0.payload = .reqDelSocialPublication(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                posts.removeAll { $0.uuid == pubUuid }
                saveCachedPosts()
            }
        } catch { /* leave the post in place; user can retry */ }
    }

    // Issue #35: delete a comment on one of your own posts (server-side
    // enforces the "own post" rule regardless of who wrote the comment).
    func deleteComment(_ commentUuid: String) async {
        do {
            var req = Msg_DelSocialComment()
            req.commentUuid = commentUuid
            let resp = try await ws.request { $0.payload = .reqDelSocialComment(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                for idx in posts.indices {
                    posts[idx].comments.removeAll { $0.commentUuid == commentUuid }
                }
                saveCachedPosts()
            }
        } catch { /* leave the comment in place; user can retry */ }
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
            guard let resp = try? await ws.request({ e in
                var env = ReqEnvelope()
                env.payload = .reqGetPublication(req)
                e = env
            }) else { return }
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

struct SocialFeedView: View {
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
    // nothing to actually apply a friendship to yet.
    @State private var showingFriendships = false

    // Issue #81: the nav bar had nothing on the leading side (no title, no
    // button), just the trailing "+" - reading as empty space instead of a
    // masthead. Rather than pin a logo into that fixed toolbar, it lives as
    // ordinary scrollable content at the very top of the feed instead, so
    // it scrolls away with everything else once the user starts reading -
    // same "collapses as you engage" feel as Instagram/Twitter's own
    // wordmark header.
    private var logoHeader: some View {
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
        NavigationView {
            Group {
                // Issue #22: an empty ScrollView while the very first fetch
                // is still in flight used to just look frozen — show an
                // actual loading state instead, distinct from "genuinely no
                // posts".
                if vm.posts.isEmpty && vm.loading {
                    VStack(spacing: 12) {
                        logoHeader
                        Spacer()
                        ProgressView()
                        Text("Loading…").foregroundColor(.secondary)
                        Spacer()
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if vm.posts.isEmpty {
                    VStack(spacing: 0) {
                        logoHeader
                        ContentUnavailableView("No posts yet", systemImage: "photo.on.rectangle.angled")
                    }
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
                                // offset needed there). This negative
                                // offset is a best estimate compensating
                                // for that reserved space, not a value
                                // read from a real measurement - needs a
                                // screenshot to confirm it actually lines
                                // up now.
                                logoHeader
                                    .padding(.top, -44)
                                ForEach(vm.boxedPosts, id: \.pub.uuid) { box in
                                    let post = box.pub
                                    PostCard(
                                        box: box,
                                        isHighlighted: post.uuid == vm.highlightPub,
                                        highlightCommentUuid: post.uuid == vm.highlightPub ? vm.highlightComment : nil,
                                        onLikePub: { Task { await vm.likePublication(post.uuid) } },
                                        onLikeComment: { c in Task { await vm.likeComment(c) } },
                                        onComment: { text in Task { await vm.addComment(pubUuid: post.uuid, text: text) } },
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
                                }
                            }
                        }
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
            .toolbar {
                // Issue #84: left of "+", matching the web header's own
                // arrangement (button, then "+").
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button { showingFriendships = true } label: {
                        Image(systemName: "person.2")
                    }
                    .accessibilityLabel("Friends")
                }
                ToolbarItem(placement: .navigationBarTrailing) {
                    // Outline circle + orange tint, matching the web
                    // composer's own button (an outline ring rather than a
                    // filled disc reads lighter sitting right in the nav
                    // bar next to the feed, instead of like a floating
                    // action button that wandered up from a corner).
                    Button { showingPicker = true } label: {
                        Image(systemName: "plus.circle")
                    }
                    .tint(Color(red: 1.0, green: 0.42, blue: 0.29)) // matches web's --ember (#ff6b4a)
                    .accessibilityLabel("New post")
                }
            }
        }
        .sheet(isPresented: $showingPicker) {
            NewPostPickerView {
                Task { await vm.loadFeed() }
            }
        }
        .sheet(isPresented: $showingFriendships) {
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
                showingFriendships = true
                notifications.pendingDeepLink = nil
            case nil:
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
    /// behind it.
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

    /// What holding this image actually costs in memory: its pixels, not
    /// the compressed bytes it arrived as.
    private static func cost(of image: UIImage) -> Int {
        Int(image.size.width * image.scale * image.size.height * image.scale) * 4
    }

    static func image(of file: Msg_File) -> UIImage? {
        let key = file.hash as NSString
        if let cached = images.object(forKey: key) { return cached }
        guard file.hasContent, let ui = UIImage(data: file.content) else { return nil }
        images.setObject(ui, forKey: key, cost: cost(of: ui))

        return ui
    }

    static func size(of file: Msg_File) -> CGSize? {
        guard let ui = image(of: file), ui.size.width > 0, ui.size.height > 0 else { return nil }

        return ui.size
    }
}

/// Issue #112 follow-up: how much of the screen one post's media may take.
/// A backstop for very wide windows (an iPad), where even the feed box
/// below could otherwise fill the screen and push the caption, likes and
/// comments out of view, leaving a post you can only scroll past.
private let cMaxMediaHeightFraction: CGFloat = 0.8

/// The feed's media box, in Instagram's terms: nothing taller than 4:5,
/// nothing wider than 1.91:1, and whatever falls between keeps its own
/// shape. Everything this library holds is 9:16 (0.5625), which is far
/// taller than 4:5 - shown at its own ratio it takes the whole screen,
/// which is what put the comments out of reach.
private let cFeedMinAspect: CGFloat = 4.0 / 5.0
private let cFeedMaxAspect: CGFloat = 1.91

/// A player that fills its box and crops the overflow, which is how
/// Instagram shows a 9:16 clip in a 4:5 feed slot.
///
/// SwiftUI's own VideoPlayer can't do this: it has no videoGravity of its
/// own and always letterboxes, so a 9:16 video in a 4:5 box would become
/// a narrow strip between two black bars. This is the same
/// AVPlayerViewController VideoPlayer wraps - transport controls and all
/// - with the one property it doesn't expose set to fill.
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
/// bare layer gets the identical cropping with none of it: nothing
/// observes videoBounds, so nothing recomputes videoRect during layout.
///
/// SwiftUI's own VideoPlayer is not an option either - it wraps the same
/// AVPlayerViewController and has no videoGravity, so a 9:16 clip in a 4:5
/// box would letterbox into a narrow strip.
final class PlayerLayerView: UIView {
    override static var layerClass: AnyClass { AVPlayerLayer.self }

    var playerLayer: AVPlayerLayer {
        // Safe by construction: layerClass above is what backs this view.
        layer as! AVPlayerLayer // swiftlint:disable:this force_cast
    }
}

private struct CroppingVideoPlayer: UIViewRepresentable {
    let player: AVPlayer

    func makeUIView(context: Context) -> PlayerLayerView {
        let view = PlayerLayerView()
        view.backgroundColor = .black
        view.playerLayer.videoGravity = .resizeAspectFill
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
    let onComment: (String) -> Void
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

    // Instagram-style, edge-to-edge feed (issue #12): no card background or
    // rounded frame around the whole post, and the image spans the full
    // screen width at its own real aspect ratio instead of being cropped
    // into a fixed box — everything else (header, caption, actions,
    // comments) gets its own modest horizontal inset instead.
    private let sidePadding: CGFloat = 12

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                avatarView(data: post.publisher.hasImage ? post.publisher.image : nil, size: 28)
                VStack(alignment: .leading, spacing: 1) {
                    Text(post.publisher.name.isEmpty ? "User" : post.publisher.name)
                        .font(.subheadline).bold()
                    if post.hasDateTime {
                        Text(formatPostDate(post.dateTime.date))
                            .font(.caption2)
                            .foregroundColor(.secondary)
                    }
                }
                Spacer()
                // Issue #34: delete one of your own posts.
                if post.own {
                    Menu {
                        Button("Delete Post", role: .destructive) { confirmDeletePost = true }
                    } label: {
                        Image(systemName: "ellipsis").foregroundColor(.secondary)
                            .padding(.horizontal, 6) // bigger tap target than the glyph alone
                    }
                }
            }
            .padding(.horizontal, sidePadding)
            .padding(.top, 10)

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
                    // it needs a definite height, which is exactly the
                    // one carouselHeight already computes for this case.
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
                        .overlay(alignment: .topTrailing) {
                            if file.mime.hasPrefix("video/") {
                                Button { feedAudio.toggle() } label: {
                                    Image(systemName: feedAudio.muted ? "speaker.slash.fill" : "speaker.wave.2.fill")
                                        .font(.system(size: 13, weight: .semibold))
                                        .foregroundStyle(.white)
                                        .padding(8)
                                        .background(.black.opacity(0.45), in: Circle())
                                }
                                .padding(10)
                            }
                        }
                        .contentShape(Rectangle())
                        .scaleEffect(pinchScale, anchor: pinchAnchor)
                        .animation(.spring(response: 0.3, dampingFraction: 0.7), value: pinchScale)
                        .zIndex(pinchScale > 1 ? 1 : 0)
                        .onChange(of: currentImage) { _, _ in
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
                    Text("\(post.likes) like\(post.likes == 1 ? "" : "s")")
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
                            Text(c.publisher.isEmpty ? "User" : c.publisher).bold() + Text(": " + c.comment)
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
                            // Issue #35: on your own post, any comment can
                            // be deleted — not just ones you wrote.
                            if post.own {
                                Button {
                                    commentPendingDelete = c.commentUuid
                                } label: {
                                    Image(systemName: "trash").foregroundColor(.secondary)
                                }
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
                TextField("Add a comment…", text: $commentText)
                    .textFieldStyle(.roundedBorder)
                    .font(.caption)
                Button("Post") {
                    onComment(commentText)
                    commentText = ""
                }
                .font(.caption)
                .disabled(commentText.trimmingCharacters(in: .whitespaces).isEmpty)
            }
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
        .confirmationDialog("Delete this post?", isPresented: $confirmDeletePost, titleVisibility: .visible) {
            Button("Delete Post", role: .destructive, action: onDeletePub)
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog(
            "Delete this comment?",
            isPresented: Binding(get: { commentPendingDelete != nil }, set: { if !$0 { commentPendingDelete = nil } }),
            titleVisibility: .visible
        ) {
            Button("Delete Comment", role: .destructive) {
                if let uuid = commentPendingDelete { onDeleteComment(uuid) }
            }
            Button("Cancel", role: .cancel) {}
        }
    }

    /// The actual post image, full width, at its own real aspect ratio — no
    /// cropping, no fixed box. `.aspectRatio(contentMode: .fit)` computes
    /// height from the proposed width itself, so unlike the `scaledToFill()`
    /// this replaced, it can't report an oversized ideal size that pushes
    /// the view wider than the screen. Issue #60: a video file renders as
    /// its own poster-plus-play-button (see videoContent) instead.
    @ViewBuilder
    private func mediaContent(for file: Msg_File) -> some View {
        if file.mime.hasPrefix("video/") {
            videoContent(for: file)
        } else if let ui = MediaSizeCache.image(of: file) {
            // .fit, never .fill: a photo is never cut. Its own ratio
            // shapes the box (within the allowed range), so an ordinary
            // landscape photo fills it exactly and only something taller
            // than 4:5 gets letterboxed - cropping these is what broke
            // horizontal photos, especially in a post whose first item is
            // portrait and therefore sets a tall box.
            feedBox(for: file) {
                Image(uiImage: ui).resizable().aspectRatio(contentMode: .fit)
            }
        } else {
            Rectangle()
                .fill(Color.secondary.opacity(0.08))
                .frame(maxWidth: .infinity, minHeight: 200, maxHeight: carouselHeight ?? 320)
                .overlay {
                    Image(systemName: "photo").font(.largeTitle).foregroundColor(.secondary)
                }
        }
    }

    /// The shape of a post's media, read from its own thumbnail. A
    /// video's thumbnail is a frame of that video (see UploadFile's
    /// background processing), so it carries exactly the aspect ratio the
    /// player will have - which is what lets the player be given the
    /// poster's box before a single byte of video has been decoded.
    private func mediaAspect(for file: Msg_File) -> CGFloat? {
        guard let size = MediaSizeCache.size(of: file) else { return nil }

        return size.width / size.height
    }

    /// One post's media area: a single item on its own, or a paging
    /// carousel that moves with the finger when there are several
    /// (issue #109).
    @ViewBuilder
    private func carouselContent(for current: Msg_File) -> some View {
        if post.files.count > 1, let height = carouselHeight {
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
            .frame(height: height)
        } else {
            mediaContent(for: current)
        }
    }

    /// This file's shape as the feed will show it: its own aspect ratio,
    /// clamped into the range the feed allows (see cFeedMinAspect). The
    /// media fills that box and is cropped, rather than being letterboxed
    /// inside it.
    private func displayAspect(for file: Msg_File) -> CGFloat? {
        guard let aspect = mediaAspect(for: file) else { return nil }

        return min(max(aspect, cFeedMinAspect), cFeedMaxAspect)
    }

    /// One box for this post's media, sized by boxAspect. Poster and
    /// player share it, so starting playback can't reshape the post
    /// (issue #112). What goes inside decides whether it is cropped to
    /// that box or letterboxed within it - see the call sites.
    @ViewBuilder
    private func feedBox<Content: View>(for file: Msg_File, @ViewBuilder content: @escaping () -> Content) -> some View {
        Color.black
            .aspectRatio(boxAspect(for: file), contentMode: .fit)
            .frame(maxWidth: .infinity, maxHeight: maxMediaHeight)
            .overlay { content() }
            .clipped()
    }

    /// The shape of this post's media area: one ratio for the whole post,
    /// taken from its first item as Instagram does, so swiping a carousel
    /// can't resize the card.
    private func boxAspect(for file: Msg_File) -> CGFloat {
        let source = post.files.first ?? file

        return displayAspect(for: source) ?? cFeedMinAspect
    }

    /// The tallest this post's media may be - see cMaxMediaHeightFraction.
    /// Applied to the poster and the player alike, so capping one can't
    /// reintroduce the mismatch issue #112 fixed.
    private var maxMediaHeight: CGFloat {
        UIScreen.main.bounds.height * cMaxMediaHeightFraction
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
            // nothing to do with the video's shape.
            feedBox(for: file) {
                CroppingVideoPlayer(player: player)
            }
            // Replay, over the frame the clip stopped on. Without it a
            // finished video is a dead end: play() on a player sitting at
            // the end does nothing, so scrolling away and back doesn't
            // restart it either - the seek is what does.
            .overlay {
                if videoEnded {
                    Button {
                        videoEnded = false
                        player.seek(to: .zero)
                        player.play()
                    } label: {
                        Image(systemName: "arrow.counterclockwise.circle.fill")
                            .font(.system(size: 56))
                            .foregroundStyle(.white)
                            .shadow(radius: 4)
                    }
                    .buttonStyle(.plain)
                }
            }
            // Tapping the poster is the play gesture - starting
            // playback here means that tap does what it looks like it
            // does, instead of landing on a paused player that needs a
            // second tap on its own control.
            .onAppear { player.play() }
            .onDisappear { player.pause() }
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
                if let ui = MediaSizeCache.image(of: file) {
                    // .fill here, unlike a photo: this poster stands in
                    // for a player that crops to the same box, so it has
                    // to be framed identically.
                    feedBox(for: file) {
                        Image(uiImage: ui).resizable().aspectRatio(contentMode: .fill)
                    }
                } else {
                    Rectangle()
                        .fill(Color.secondary.opacity(0.08))
                        .frame(maxWidth: .infinity, minHeight: 200, maxHeight: carouselHeight ?? 320)
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
        }
    }

    /// Issue #114: starts this post's video because it just scrolled into
    /// view. Muted, which is both what the issue asks for and the only
    /// way autoplay is acceptable in a feed - the speaker button above
    /// is what turns sound on.
    private func autoplayIfVideo(_ file: Msg_File) {
        guard file.mime.hasPrefix("video/") else { return }
        if let player = videoPlayer {
            player.isMuted = feedAudio.muted
            player.play()
            return
        }
        loadAndPlayVideo(file)
    }

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
            let asset = AVURLAsset(url: url)
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

    /// Issue #27: with more than one item in a post, the media area keeps
    /// one height rather than resizing per item - swiping between mixed
    /// aspect ratios used to make the whole card jump taller and shorter
    /// on every image. `nil` for a single-item post, which has nothing to
    /// stay consistent with.
    ///
    /// It is the same box every item already draws into (see boxAspect),
    /// just expressed as a height, because that is what a paging TabView
    /// needs to be given.
    private var carouselHeight: CGFloat? {
        guard let first = post.files.first, post.files.count > 1 else { return nil }
        let width = UIScreen.main.bounds.width

        return min(width / boxAspect(for: first), maxMediaHeight)
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
        NavigationView {
            Group {
                if let likers {
                    if likers.isEmpty {
                        ContentUnavailableView("No likes yet", systemImage: "heart")
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
            .navigationTitle("Likes")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button("Done") { dismiss() }
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
