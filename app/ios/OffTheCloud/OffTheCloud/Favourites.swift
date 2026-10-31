// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  Favourites.swift
//  OffTheCloud
//
//  Favourites (the owner, 2026-10-31): a collection every device has,
//  listed first whatever the Sort by (CollectionsSort), that can't be
//  renamed or deleted, and filled from a heart in the photo viewer
//  (ImageModal). The Android app follows the same rules:
//    - The heart is for the device's own photos and videos (Images, its
//      search, People, Collections, Places, a memory's See all, Files) -
//      never a photo only on this phone. Empty, or full (ember) while the
//      photo is in Favourites; a member is a photo, by its content, so any
//      copy counts.
//    - Each photo shown asks GetFavourites for it and its neighbours in one
//      request, so a swipe finds its answer here (FavouritesModel, by
//      path). Until the open photo's answer is known its place stays empty:
//      never an empty heart that fills a moment later.
//    - A tap shows the new state at once and sends SetFavourite (one at a
//      time per photo: a tap meanwhile is sent once the device answered);
//      refused, the heart goes back and the viewer says the device's words,
//      or app.photos.viewer.favourite_failed.
//    - A device from before Favourites answers unknown_payload: no heart
//      until the next sign-in (OTCConnection.connected), nothing to update.
//    - Inside the open Favourites, a heart emptied keeps its photo in the
//      viewer (it can be filled again); it leaves the grid as the viewer
//      closes (PhotoGalleryVM.dropUnfavourited), as Remove from collection
//      takes photos out.
//

import Combine
import SwiftUI

/// A photo the heart is about: its path, and the content it was shown with
/// (its copies change with it).
struct FavouritePhoto: Equatable, Sendable {
    let path: String
    let hash: String
}

/// What the viewer asks about.
enum FavouriteAsk {
    /// Whether the viewer has a heart for this item: one the device holds,
    /// never a photo only on this phone (not uploaded yet).
    static func eligible(_ item: PhotoGalleryVM.Item) -> Bool {
        !item.isLocalOnly && !item.path.isEmpty
    }

    /// Showing the item at `index`: it, then the next and the previous -
    /// what a swipe brings - those with a heart, each path once.
    static func around(_ index: Int, in items: [PhotoGalleryVM.Item]) -> [FavouritePhoto] {
        var seen = Set<String>()
        return [index, index + 1, index - 1].compactMap { i in
            guard items.indices.contains(i), eligible(items[i]), seen.insert(items[i].path).inserted else { return nil }
            return FavouritePhoto(path: items[i].path, hash: items[i].hash)
        }
    }
}

/// How a heart's tap ended.
enum FavouriteOutcome: Equatable {
    /// The device took it (or it is sent after the one on its way).
    case done
    /// Refused, or no answer: the heart is back as it was. The device's
    /// words when it gave any.
    case failed(String?)
    /// A device from before Favourites: no heart any more.
    case unsupported
}

/// A heart the device took: which viewer's (`by`, the view model that
/// tapped it), so the one that tapped it doesn't take it again.
struct FavouriteChange: Equatable {
    let count: Int
    let by: ObjectIdentifier?
}

/// Which photos are in Favourites, as the device said, and the hearts on
/// their way. One for the session: every viewer (Images', Files', the
/// map's, a memory's) shows the same.
@MainActor
final class FavouritesModel: ObservableObject {
    static let shared = FavouritesModel()

    typealias Request = @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope

    /// Whether each path's photo is in Favourites: as the device last said,
    /// or as a heart since made it. A path never answered for isn't here.
    @Published private(set) var state: [String: Bool] = [:]
    /// The device answered unknown_payload (before Favourites): no heart
    /// until the next sign-in, which asks again.
    @Published private(set) var unsupported = false
    /// The last heart the device took: the collections' counts and covers,
    /// and an open Favourites' photos, are out of date.
    @Published private(set) var change = FavouriteChange(count: 0, by: nil)

    /// Sends the requests. Tests answer in the device's place.
    var request: Request = { payload in
        try await OTCConnection.shared.request { $0.payload = payload }
    }

    /// The content each path was shown with: a heart changes every copy
    /// known here.
    private var hashes: [String: String] = [:]
    /// When each path was last set, by `seq`: an answer asked for before a
    /// heart was tapped, or before its request was answered, is out of date
    /// (the device may have read it first), and so is one asked before
    /// another answer that came already.
    private var stamps: [String: Int] = [:]
    private var seq = 0
    /// Paths with a SetFavourite on its way.
    private var sending: Set<String> = []
    /// Bumped when what is known goes (Log Out, another device): answers
    /// from before land nowhere.
    private var epoch = 0
    /// The device the answers came from.
    private var endpoint: String?

    init() {}

    /// Whether the photo at `path` is in Favourites; nil while not known
    /// (no heart shown) or on a device without Favourites.
    func favourite(_ path: String) -> Bool? {
        unsupported ? nil : state[path]
    }

    /// Asks whether these photos are in Favourites, in one request. False
    /// when no answer came (asked again with the next photo shown, or a
    /// wake).
    @discardableResult
    func ask(_ photos: [FavouritePhoto]) async -> Bool {
        guard !unsupported, !photos.isEmpty else { return false }
        for p in photos where !p.hash.isEmpty { hashes[p.path] = p.hash }
        seq += 1
        let mine = seq
        let e = epoch
        var g = Msg_GetFavourites()
        g.paths = photos.map(\.path)
        guard let resp = try? await request(.reqGetFavourites(g)), e == epoch else { return false }
        if resp.error, ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) {
            unsupported = true
            return false
        }
        guard case .respFavourites(let f) = resp.payload else { return false }
        let yes = Set(f.paths)
        // Not while a heart on it is on its way: the device may have read
        // it before taking the heart.
        let busy = withCopies(sending)
        var next = state
        for p in g.paths where (stamps[p] ?? 0) < mine && !busy.contains(p) {
            next[p] = yes.contains(p)
            stamps[p] = mine
        }
        if next != state { state = next }
        return true
    }

    /// The heart on `photo` tapped: the other state at once (in this very
    /// call, with the tap's haptic), then SetFavourite. One request at a
    /// time per photo: a tap while one is on its way is sent once it is
    /// answered, if the heart still differs from what the device then
    /// holds. Refused, the heart goes back to what the device holds. `by`:
    /// the view model that tapped it (`change.by`). The task ends with what
    /// came of it.
    @discardableResult
    func toggle(_ photo: FavouritePhoto, by: ObjectIdentifier? = nil) -> Task<FavouriteOutcome, Never> {
        guard !unsupported, let now = state[photo.path] else { return Task { .done } }
        if !photo.hash.isEmpty { hashes[photo.path] = photo.hash }
        set(photo.path, !now)
        guard sending.insert(photo.path).inserted else { return Task { .done } }
        let e = epoch
        return Task { await send(photo, held: now, by: by, epoch: e) }
    }

    /// SetFavourite for `photo` until the device holds what its heart
    /// shows (`held`: what it holds now).
    private func send(_ photo: FavouritePhoto, held: Bool, by: ObjectIdentifier?, epoch e: Int) async -> FavouriteOutcome {
        defer {
            if e == epoch {
                sending.remove(photo.path)
                // Answers asked before this ended may have been read before it.
                stamp(withCopies([photo.path]))
            }
        }
        guard e == epoch else { return .done }
        var held = held
        while let want = state[photo.path], want != held {
            var s = Msg_SetFavourite()
            s.paths = [photo.path]
            s.favourite = want
            let resp = try? await request(.reqSetFavourite(s))
            guard e == epoch else { return .done }
            switch Self.verdict(resp) {
            case .ok:
                held = want
                change = FavouriteChange(count: change.count + 1, by: by)
            case .unknownPayload:
                set(photo.path, held)
                unsupported = true
                return .unsupported
            case .refused(let words):
                set(photo.path, held)
                return .failed(words)
            }
        }
        return .done
    }

    /// Photos put in Favourites, or taken out, another way (Add to
    /// collection, Remove from collection): their hearts follow.
    func took(_ photos: [FavouritePhoto], on: Bool) {
        for p in photos where !p.hash.isEmpty { hashes[p.path] = p.hash }
        set(photos.map(\.path), on)
    }

    /// Signed in (again): a device from before Favourites is asked again
    /// (it may have been updated); another device's answers go.
    func connected(endpoint: String) {
        if let was = self.endpoint, was != endpoint { forget() }
        self.endpoint = endpoint
        unsupported = false
    }

    /// Log Out: nothing of this device's is kept.
    func reset() {
        forget()
        endpoint = nil
        unsupported = false
    }

    private func forget() {
        epoch += 1
        state = [:]
        hashes = [:]
        stamps = [:]
        sending = []
    }

    /// `paths` and the copies known here (the same content) set to `on`.
    private func set(_ paths: [String], _ on: Bool) {
        let all = withCopies(paths)
        var next = state
        for p in all { next[p] = on }
        stamp(all)
        if next != state { state = next }
    }

    private func set(_ path: String, _ on: Bool) { set([path], on) }

    private func stamp(_ paths: Set<String>) {
        seq += 1
        for p in paths { stamps[p] = seq }
    }

    /// `paths` and every other path known to hold the content of one.
    private func withCopies<S: Sequence>(_ paths: S) -> Set<String> where S.Element == String {
        var all = Set(paths)
        let contents = Set(all.compactMap { hashes[$0] }.filter { !$0.isEmpty })
        if !contents.isEmpty {
            for (p, h) in hashes where contents.contains(h) { all.insert(p) }
        }
        return all
    }

    enum Verdict: Equatable {
        case ok
        case unknownPayload
        /// The device's words, when it gave any.
        case refused(String?)
    }

    /// What an answer to SetFavourite says. The bridge answering for a
    /// device it can't reach has no words of the device's.
    static func verdict(_ resp: Msg_RespEnvelope?) -> Verdict {
        guard let resp else { return .refused(nil) }
        if resp.error {
            if ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) { return .unknownPayload }
            return .refused(words(resp.errorMessage))
        }
        guard case .respAck(let a) = resp.payload else { return .refused(nil) }
        if a.ok { return .ok }
        if a.code == LoadProblem.deviceUnreachable { return .refused(nil) }
        return .refused(words(a.errorMsg))
    }

    private static func words(_ s: String) -> String? {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }
}

/// The viewer's heart for the open item (ImageModal): empty, or full while
/// it is in Favourites. Until its answer is known the heart keeps its place
/// unseen, so nothing moves when it shows; a tap that fills it gives a
/// light tap of the phone and a small bounce (none with Reduce Motion).
struct FavouriteButton: View {
    @ObservedObject var vm: PhotoGalleryVM
    @ObservedObject var store: FavouritesModel
    /// The target's side in landscape; nil in portrait's row.
    let side: CGFloat?
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    /// Taps that filled it: the haptic and the bounce.
    @State private var filled = 0

    var body: some View {
        // No room at all on a device without Favourites, or for a photo
        // only on this phone.
        if vm.heartShown { heart }
    }

    private var heart: some View {
        let on = vm.openFavourite
        return Button {
            guard let on else { return }
            if !on { filled += 1 }
            vm.toggleFavourite()
        } label: {
            Image(systemName: on == true ? "heart.fill" : "heart")
                .foregroundStyle(on == true ? MenuStyle.ember : .white)
                .symbolEffect(.bounce, options: .nonRepeating, value: reduceMotion ? 0 : filled)
                .frame(width: side, height: side)
                .contentShape(Rectangle())
        }
        .opacity(on == nil ? 0 : 1)
        .disabled(on == nil)
        .accessibilityHidden(on == nil)
        .accessibilityLabel(on == true ? S.appPhotosViewerFavouriteRemove : S.appPhotosViewerFavouriteAdd)
        .accessibilityAddTraits(on == true ? .isSelected : [])
        .sensoryFeedback(.impact(weight: .light), trigger: filled)
    }
}
