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

    @Published var posts: [Msg_SocialPublication] = []
    @Published var busyLikePub: String?
    @Published var busyLikeComment: String?
    @Published var loading = false

    func loadFeed() async {
        loading = true
        defer { loading = false }
        var req = Msg_GetSocialPublications()
        req.total = 50
        do {
            let resp = try await ws.request { $0.payload = .reqGetSocialPublications(req) }
            if case .respSocialPublications(let sp) = resp.payload {
                posts = sp.publications
            }
        } catch { /* keep whatever was already shown */ }
    }

    func likePublication(_ uuid: String) async {
        busyLikePub = uuid
        defer { busyLikePub = nil }
        var req = Msg_LikePublication()
        req.pubUuid = uuid
        _ = try? await ws.request { $0.payload = .reqLikePublication(req) }
        await loadFeed()
    }

    func likeComment(_ uuid: String) async {
        busyLikeComment = uuid
        defer { busyLikeComment = nil }
        var req = Msg_LikeComment()
        req.commentUuid = uuid
        _ = try? await ws.request { $0.payload = .reqLikeComment(req) }
        await loadFeed()
    }

    func addComment(pubUuid: String, text: String) async {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        var req = Msg_NewSocialComment()
        req.pubUuid = pubUuid
        req.comment = trimmed
        req.publisher = "me"
        _ = try? await ws.request { $0.payload = .reqNewSocialComment(req) }
        await loadFeed()
    }

    func fetchHiRes(path: String) async -> UIImage? {
        var req = Msg_GetFile()
        req.path = path
        guard let resp = try? await ws.request({ $0.payload = .reqGetFile(req) }) else { return nil }
        if case .respFile(let f) = resp.payload { return UIImage(data: f.content) }
        return nil
    }
}

struct SocialFeedView: View {
    @StateObject private var vm = SocialFeedViewModel()
    @State private var viewerState: ViewerState?

    var body: some View {
        NavigationView {
            Group {
                if vm.posts.isEmpty && !vm.loading {
                    ContentUnavailableView("No posts yet", systemImage: "photo.on.rectangle.angled")
                } else {
                    ScrollView {
                        LazyVStack(spacing: 20) {
                            ForEach(vm.posts, id: \.uuid) { post in
                                PostCard(
                                    post: post,
                                    isLikingPub: vm.busyLikePub == post.uuid,
                                    likingCommentUuid: vm.busyLikeComment,
                                    onLikePub: { Task { await vm.likePublication(post.uuid) } },
                                    onLikeComment: { c in Task { await vm.likeComment(c) } },
                                    onComment: { text in Task { await vm.addComment(pubUuid: post.uuid, text: text) } },
                                    onOpenImage: { idx in viewerState = ViewerState(post: post, index: idx) }
                                )
                            }
                        }
                        .padding()
                    }
                    .refreshable { await vm.loadFeed() }
                }
            }
            .navigationTitle("Social")
        }
        .task { await vm.loadFeed() }
        .sheet(item: $viewerState) { state in
            ImageViewerSheet(state: state, fetchHiRes: vm.fetchHiRes, onNavigate: { newIndex in
                viewerState = ViewerState(post: state.post, index: newIndex)
            })
        }
    }
}

private struct ViewerState: Identifiable {
    let post: Msg_SocialPublication
    let index: Int
    var id: String { "\(post.uuid)-\(index)" }
}

private struct PostCard: View {
    let post: Msg_SocialPublication
    let isLikingPub: Bool
    let likingCommentUuid: String?
    let onLikePub: () -> Void
    let onLikeComment: (String) -> Void
    let onComment: (String) -> Void
    let onOpenImage: (Int) -> Void

    @State private var currentImage = 0
    @State private var commentText = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                avatarView(data: post.publisher.hasImage ? post.publisher.image : nil, size: 28)
                Text(post.publisher.name.isEmpty ? "User" : post.publisher.name)
                    .font(.subheadline).bold()
            }

            if !post.files.isEmpty {
                ZStack(alignment: post.files.count > 1 ? .bottom : .center) {
                    let file = post.files[min(currentImage, post.files.count - 1)]
                    Button { onOpenImage(currentImage) } label: {
                        thumb(for: file)
                            .frame(maxWidth: .infinity)
                            .frame(height: 280)
                            .clipped()
                            .background(Color.secondary.opacity(0.08))
                            .clipShape(RoundedRectangle(cornerRadius: 10))
                    }
                    .buttonStyle(.plain)

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
                .overlay(alignment: .leading) {
                    if post.files.count > 1 && currentImage > 0 {
                        navButton("chevron.left") { currentImage -= 1 }
                    }
                }
                .overlay(alignment: .trailing) {
                    if post.files.count > 1 && currentImage < post.files.count - 1 {
                        navButton("chevron.right") { currentImage += 1 }
                    }
                }
            }

            if !post.text.isEmpty {
                Text(post.text).font(.body)
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
            .font(.subheadline)

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
        }
        .padding()
        .background(Color(.secondarySystemGroupedBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14))
    }

    @ViewBuilder
    private func thumb(for file: Msg_File) -> some View {
        if file.hasContent, let ui = UIImage(data: file.content) {
            Image(uiImage: ui).resizable().scaledToFill()
        } else {
            Image(systemName: "photo").resizable().scaledToFit().foregroundColor(.secondary).padding(60)
        }
    }

    private func navButton(_ systemImage: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .padding(8)
                .background(.black.opacity(0.35), in: Circle())
                .foregroundColor(.white)
        }
        .padding(.horizontal, 6)
    }
}

private struct ImageViewerSheet: View {
    fileprivate let state: ViewerState
    let fetchHiRes: (String) async -> UIImage?
    let onNavigate: (Int) -> Void

    @State private var hiRes: UIImage?
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            VStack {
                HStack {
                    Spacer()
                    Button { dismiss() } label: {
                        Image(systemName: "xmark.circle.fill").font(.title).foregroundStyle(.white)
                    }
                }
                .padding()

                Spacer()
                if let hiRes {
                    Image(uiImage: hiRes).resizable().scaledToFit()
                } else if state.post.files.indices.contains(state.index),
                          state.post.files[state.index].hasContent,
                          let low = UIImage(data: state.post.files[state.index].content) {
                    Image(uiImage: low).resizable().scaledToFit()
                } else {
                    ProgressView().tint(.white)
                }
                Spacer()

                HStack {
                    Button("‹ Prev") { onNavigate(state.index - 1) }
                        .disabled(state.index <= 0)
                    Spacer()
                    Button("Next ›") { onNavigate(state.index + 1) }
                        .disabled(state.index >= state.post.files.count - 1)
                }
                .foregroundColor(.white)
                .padding()
            }
        }
        .task(id: state.id) {
            hiRes = nil
            guard state.post.files.indices.contains(state.index) else { return }
            hiRes = await fetchHiRes(state.post.files[state.index].path)
        }
    }
}
