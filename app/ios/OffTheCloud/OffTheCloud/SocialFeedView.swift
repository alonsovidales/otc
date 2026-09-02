//
//  SocialFeedView.swift
//  OffTheCloud
//
//  Native port of the web app's Social tab (web/src/components/Social.tsx):
//  scrollable feed of publications with like/comment, and a full-screen
//  viewer for multi-image posts (with a hi-res fetch once opened).

import SwiftUI

@MainActor
final class SocialFeedViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    /// How many publications to fetch per page (issue #15) — loading 50 at
    /// once, each with its own files/comments, was a big chunk of why the
    /// feed felt slow to appear, especially while competing with a large
    /// photo sync on the same shared connection.
    private static let pageSize: Int32 = 4

    @Published var posts: [Msg_SocialPublication] = []
    @Published var busyLikePub: String?
    @Published var busyLikeComment: String?
    @Published var loading = false
    @Published var loadingMore = false
    private var endReached = false

    private var pollTask: Task<Void, Never>?

    /// Keeps retrying every few seconds until a request actually completes
    /// (issue #11). A short handful of quick retries wasn't enough: the
    /// shared connection can be legitimately busy for a long time — e.g. a
    /// large photo sync uploading thousands of files — so "give up after a
    /// couple of seconds" just reproduced the bug. This only stops once the
    /// server actually answers (even with zero publications); it doesn't
    /// stop just because a request throws.
    func startAutoLoad() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                if await self.loadFeed() { break }
                try? await Task.sleep(nanoseconds: 4_000_000_000)
            }
            self?.pollTask = nil
        }
    }

    func stopAutoLoad() {
        pollTask?.cancel()
        pollTask = nil
    }

    /// Resets to the first page. Returns whether the server actually
    /// answered (regardless of how many publications came back) so callers
    /// can tell "legitimately empty" apart from "the request never
    /// completed".
    @discardableResult
    func loadFeed() async -> Bool {
        loading = true
        defer { loading = false }
        endReached = false
        return await fetchPage(total: Self.pageSize, excludeUuids: [], replacing: true)
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
            }
            return true
        } catch {
            print("Social feed load failed, will retry:", error)
            return false
        }
    }

    func likePublication(_ uuid: String) async {
        busyLikePub = uuid
        defer { busyLikePub = nil }
        var req = Msg_LikePublication()
        req.pubUuid = uuid
        _ = try? await ws.request { $0.payload = .reqLikePublication(req) }
        await refreshCurrentlyLoaded()
    }

    func likeComment(_ uuid: String) async {
        busyLikeComment = uuid
        defer { busyLikeComment = nil }
        var req = Msg_LikeComment()
        req.commentUuid = uuid
        _ = try? await ws.request { $0.payload = .reqLikeComment(req) }
        await refreshCurrentlyLoaded()
    }

    func addComment(pubUuid: String, text: String) async {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        var req = Msg_NewSocialComment()
        req.pubUuid = pubUuid
        req.comment = trimmed
        req.publisher = "me"
        _ = try? await ws.request { $0.payload = .reqNewSocialComment(req) }
        await refreshCurrentlyLoaded()
    }

}

struct SocialFeedView: View {
    @StateObject private var vm = SocialFeedViewModel()

    var body: some View {
        NavigationView {
            Group {
                if vm.posts.isEmpty && !vm.loading {
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
                                    isLikingPub: vm.busyLikePub == post.uuid,
                                    likingCommentUuid: vm.busyLikeComment,
                                    onLikePub: { Task { await vm.likePublication(post.uuid) } },
                                    onLikeComment: { c in Task { await vm.likeComment(c) } },
                                    onComment: { text in Task { await vm.addComment(pubUuid: post.uuid, text: text) } }
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
        }
        .task { vm.startAutoLoad() }
        .onDisappear { vm.stopAutoLoad() }
    }
}

private struct PostCard: View {
    let post: Msg_SocialPublication
    let isLikingPub: Bool
    let likingCommentUuid: String?
    let onLikePub: () -> Void
    let onLikeComment: (String) -> Void
    let onComment: (String) -> Void

    @State private var currentImage = 0
    @State private var commentText = ""

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
                Text(post.publisher.name.isEmpty ? "User" : post.publisher.name)
                    .font(.subheadline).bold()
            }
            .padding(.horizontal, sidePadding)
            .padding(.top, 10)

            if !post.files.isEmpty {
                let file = post.files[min(currentImage, post.files.count - 1)]
                ZStack(alignment: .bottom) {
                    // Just the image — no tap action. Timeline images are
                    // browse-only on iOS; the full-screen popup only makes
                    // sense on the web (where there's no native photo app to
                    // fall back on).
                    mediaContent(for: file)
                        .contentShape(Rectangle())

                    if post.files.count > 1 {
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
            }

            HStack(spacing: 16) {
                Button {
                    onLikePub()
                } label: {
                    Label("\(post.likes)", systemImage: post.liked ? "heart.fill" : "heart")
                        .foregroundColor(post.liked ? .red : .primary)
                }
                .disabled(isLikingPub)
            }
            .font(.title3)
            .padding(.horizontal, sidePadding)
            .padding(.top, 4)

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
                            Button {
                                onLikeComment(c.commentUuid)
                            } label: {
                                Label("\(c.likes)", systemImage: c.liked ? "heart.fill" : "heart")
                                    .foregroundColor(c.liked ? .red : .secondary)
                            }
                            .disabled(likingCommentUuid == c.commentUuid)
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
                .frame(maxWidth: .infinity)
        } else {
            Rectangle()
                .fill(Color.secondary.opacity(0.08))
                .frame(maxWidth: .infinity, minHeight: 200, maxHeight: 320)
                .overlay {
                    Image(systemName: "photo").font(.largeTitle).foregroundColor(.secondary)
                }
        }
    }

}
