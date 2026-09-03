//
//  SocialFeedView.swift
//  OffTheCloud
//
//  Native port of the web app's Social tab (web/src/components/Social.tsx):
//  scrollable feed of publications with like/comment, and a full-screen
//  viewer for multi-image posts (with a hi-res fetch once opened).

import SwiftUI

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

@MainActor
final class SocialFeedViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    /// How many publications to fetch per page (issue #15) — loading 50 at
    /// once, each with its own files/comments, was a big chunk of why the
    /// feed felt slow to appear, especially while competing with a large
    /// photo sync on the same shared connection.
    private static let pageSize: Int32 = 4

    @Published var posts: [Msg_SocialPublication] = []
    @Published var loading = false
    @Published var loadingMore = false
    private var endReached = false

    private var pollTask: Task<Void, Never>?

    /// On-disk snapshot of the last successfully loaded page (issue #22):
    /// read synchronously at init so the feed shows *something* the instant
    /// the app launches — even before the network request that refreshes it
    /// has a chance to complete — instead of a blank screen.
    private static let cacheURL = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("social_feed_cache.pb")

    init() {
        loadCachedPosts()
        // Nothing cached yet (first-ever launch) — mark loading right away,
        // synchronously, so the very first render shows the loading state
        // rather than a flash of "No posts yet" before the async fetch
        // below has had a chance to even start.
        if posts.isEmpty { loading = true }
        // Issue #22: start fetching immediately when the app launches,
        // rather than waiting for the user to actually tap the Social tab —
        // by the time they do, the feed is often already there. This
        // ViewModel is constructed once, up front, along with every other
        // tab's (TabView builds all its tabs' state eagerly even though
        // only one is visible), so `init` firing this is enough; no
        // view-appearance hook needed.
        startAutoLoad()
    }

    private func loadCachedPosts() {
        guard let data = try? Data(contentsOf: Self.cacheURL),
              let cached = try? Msg_SocialPublications(serializedData: data) else { return }
        posts = cached.publications
    }

    private func saveCachedPosts() {
        var msg = Msg_SocialPublications()
        msg.publications = posts
        guard let data = try? msg.serializedData() else { return }
        try? data.write(to: Self.cacheURL, options: .atomic)
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
                if await self.loadFeed() { break }
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

}

struct SocialFeedView: View {
    @StateObject private var vm = SocialFeedViewModel()

    // Issue #32: "+" opens a dedicated compose screen (tag filter + tap to
    // select + a single Publish action) — not the Images tab reused.
    @State private var showingPicker = false

    var body: some View {
        NavigationView {
            Group {
                // Issue #22: an empty ScrollView while the very first fetch
                // is still in flight used to just look frozen — show an
                // actual loading state instead, distinct from "genuinely no
                // posts".
                if vm.posts.isEmpty && vm.loading {
                    VStack(spacing: 12) {
                        ProgressView()
                        Text("Loading…").foregroundColor(.secondary)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if vm.posts.isEmpty {
                    ContentUnavailableView("No posts yet", systemImage: "photo.on.rectangle.angled")
                } else {
                    ScrollView {
                        // No outer padding and no per-post spacing (issue
                        // #12/Instagram-style ask): a horizontal inset here
                        // would margin the images too, and this app already
                        // separates posts with a Divider instead of gaps.
                        LazyVStack(spacing: 0) {
                            ForEach(vm.posts, id: \.uuid) { post in
                                PostCard(
                                    post: post,
                                    onLikePub: { Task { await vm.likePublication(post.uuid) } },
                                    onLikeComment: { c in Task { await vm.likeComment(c) } },
                                    onComment: { text in Task { await vm.addComment(pubUuid: post.uuid, text: text) } },
                                    fetchPublicationLikers: { await vm.fetchPublicationLikers($0) },
                                    fetchCommentLikers: { await vm.fetchCommentLikers($0) },
                                    onDeletePub: { Task { await vm.deletePublication(post.uuid) } },
                                    onDeleteComment: { c in Task { await vm.deleteComment(c) } }
                                )
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
                }
            }
            // No nav title here (issue #12): the tab bar already labels this
            // screen "Social", repeating it as a large title above the feed
            // was redundant chrome.
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button { showingPicker = true } label: {
                        Image(systemName: "plus.circle.fill")
                    }
                    .accessibilityLabel("New post")
                }
            }
        }
        .sheet(isPresented: $showingPicker) {
            NewPostPickerView {
                Task { await vm.loadFeed() }
            }
        }
    }
}

private struct PostCard: View {
    let post: Msg_SocialPublication
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
                    // Just the image — no tap action of its own. Timeline
                    // images are browse-only on iOS; the full-screen popup
                    // only makes sense on the web (where there's no native
                    // photo app to fall back on).
                    mediaContent(for: file)
                        .contentShape(Rectangle())
                        .scaleEffect(pinchScale, anchor: pinchAnchor)
                        .animation(.spring(response: 0.3, dampingFraction: 0.7), value: pinchScale)
                        .zIndex(pinchScale > 1 ? 1 : 0)

                    if post.files.count > 1 {
                        // Issue #20: tapping the left/right half of the
                        // image also pages through it — no visible buttons,
                        // just invisible zones alongside the drag gesture
                        // below.
                        HStack(spacing: 0) {
                            Color.clear
                                .contentShape(Rectangle())
                                .onTapGesture { if currentImage > 0 { currentImage -= 1 } }
                            Color.clear
                                .contentShape(Rectangle())
                                .onTapGesture { if currentImage < post.files.count - 1 { currentImage += 1 } }
                        }

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
                    }
                }
                // Swipe between a post's images (issue #13) — pure
                // finger-drag, no arrow buttons; the dots above are the only
                // other position affordance.
                .gesture(
                    DragGesture(minimumDistance: 20)
                        .onEnded { value in
                            guard post.files.count > 1 else { return }
                            if value.translation.width < -30, currentImage < post.files.count - 1 {
                                currentImage += 1
                            } else if value.translation.width > 30, currentImage > 0 {
                                currentImage -= 1
                            }
                        }
                )
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
    /// the view wider than the screen.
    @ViewBuilder
    private func mediaContent(for file: Msg_File) -> some View {
        if file.hasContent, let ui = UIImage(data: file.content) {
            Image(uiImage: ui)
                .resizable()
                .aspectRatio(contentMode: .fit)
                .frame(maxWidth: .infinity, maxHeight: carouselHeight)
                .frame(height: carouselHeight)
        } else {
            Rectangle()
                .fill(Color.secondary.opacity(0.08))
                .frame(maxWidth: .infinity, minHeight: 200, maxHeight: carouselHeight ?? 320)
                .overlay {
                    Image(systemName: "photo").font(.largeTitle).foregroundColor(.secondary)
                }
        }
    }

    /// Issue #27: with more than one image in a post, fix the media area's
    /// height to the tallest of them (capped at 3/4 of the screen) instead
    /// of letting it resize per-image — swiping between mixed aspect ratios
    /// used to make the whole card jump taller/shorter on every image.
    /// `nil` for a single-image post, which keeps its own natural height.
    private var carouselHeight: CGFloat? {
        guard post.files.count > 1 else { return nil }
        let width = UIScreen.main.bounds.width
        let heights = post.files.compactMap { f -> CGFloat? in
            guard f.hasContent, let ui = UIImage(data: f.content), ui.size.width > 0 else { return nil }
            return width * (ui.size.height / ui.size.width)
        }
        guard let tallest = heights.max() else { return nil }
        return min(tallest, UIScreen.main.bounds.height * 0.75)
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
