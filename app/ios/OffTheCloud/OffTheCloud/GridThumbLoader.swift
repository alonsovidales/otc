// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  GridThumbLoader.swift
//  OffTheCloud
//
//  A photo grid's tiles (Images, the post composer's Synced photos, the
//  profile photo picker) from the thumbnail cache on this phone
//  (ThumbDiskCache), asking the device only for what isn't there.
//
//  Every grid asks for its pages without content (SearchPhotos with
//  small_thumbnails and omit_thumbnails, release 113) - the same rows,
//  token and `have` as before - and for each entry:
//    - with content (a device before 113 ignores omit): shown, and kept
//      with the kind its thumbnail_small says (unset: unknown);
//    - without: looked up by its hash. A small one is used, and so is a big
//      or unknown one while the entry says the device has no small one yet
//      (thumbnail_small false). A big or unknown one while the device has
//      the small one now (true) is shown and asked for again, once a launch.
//      Nothing kept: its path is asked for.
//  Only what is on screen or about to be is asked for: a page's misses are
//  noted, and asked for as their tiles show (need, appeared) or come
//  within reach of the scroll (readAhead, about two screens around the
//  tiles shown) - a page of 120 is ~200-270 ms a path on a Pi. The paths
//  are asked for with GetThumbnails{paths, small_thumbnails}, 24 at a time,
//  the tiles on screen alone first, one request at a time per grid, asked
//  again after a failure with the pages' backoff (PageRetry: 1 s doubling
//  to 10 s, at once on a wake). Each answer entry is kept under the
//  answer's own hash with its kind. Paths from ask_again_from on are asked
//  for again; a path before it missing from the answer has no thumbnail now
//  (its tile stays grey; nothing is kept). A device before 113 doesn't say
//  where it stopped, so a path missing from its answer is never taken as
//  having none: it is asked for again when its tile shows again (and the
//  paths after the last one answered, likely cut by the answer's size, at
//  once). A big or unknown entry asked for again that comes big again is
//  recorded (ThumbDiskCache.noteStillBig): not asked for again for days.
//
//  Tiles keep no bytes when the cache has them (GridThumbCache holds the
//  decoded images, the cache the bytes): a tile whose image is gone asks
//  for it (need), from the cache, else from the device. While the phone's
//  protected data is unavailable (locked) nothing is read or asked for:
//  it waits, and goes on once the data is back (ProtectedDataWatch).
//

import UIKit
import Combine

/// SearchPhotos.limit for grids that ask for pages without content
/// (release 113): those may be bigger (up to 200). A device before 113
/// sends content anyway, in pages of at most its default (30): a first page
/// is asked as small as before (cFirstPhotoPageLimit) until the device is
/// known to leave the content out (ThumbDiskCache.deviceOmits).
enum PhotoPageLimit {
    static let firstWithoutThumbnails: Int32 = 60
    static let next: Int32 = 120

    static func first(deviceOmits: Bool?) -> Int32 {
        deviceOmits == true ? firstWithoutThumbnails : cFirstPhotoPageLimit
    }
}

/// What a grid does with a page's entry (GridThumbLoader.takePage).
enum ThumbPlan: Equatable {
    /// It brought its content (a device before release 113): shown, and kept.
    case inline
    /// The cache has what the device would send: shown from it.
    case cached
    /// The cache has a big or unknown one while the device has the small
    /// one now: shown, and asked for again (once a launch).
    case cachedThenAsk
    /// Not in the cache: asked for (GetThumbnails).
    case ask

    /// `thumbnailSmall`: the entry's (nil when unset); `cached`: what the
    /// cache holds for its hash.
    static func of(hasContent: Bool, thumbnailSmall: Bool?, cached: ThumbDiskCache.Kind?) -> ThumbPlan {
        if hasContent { return .inline }
        guard let cached else { return .ask }
        if cached == .small { return .cached }
        return thumbnailSmall == true ? .cachedThenAsk : .cached
    }
}

/// What a GetThumbnails answer says about the paths it was asked for,
/// beyond the thumbnails it carries.
struct ThumbAnswer: Equatable {
    /// Not looked at (or, from a device before release 113, likely cut by
    /// the answer's size): asked for again.
    var askAgain: [String] = []
    /// Looked at, and without a thumbnail now.
    var none: [String] = []
    /// Missing from a device before release 113's answer: maybe none, maybe
    /// cut - asked for again when the tile shows again.
    var unsure: [String] = []

    /// `deviceIsNew`: the device is known to be release 113 or later (it
    /// left the content out of a page); an answer that says where it
    /// stopped, or whether an entry is small, is one too.
    static func of(asked: [String], answer: Msg_ListOfFiles, deviceIsNew: Bool) -> ThumbAnswer {
        let answered = Set(answer.files.filter { !$0.content.isEmpty }.map(\.path))
        var out = ThumbAnswer()
        let isNew = deviceIsNew || answer.askAgainFrom > 0 || answer.files.contains { $0.hasThumbnailSmall }
        if isNew {
            let cut = answer.askAgainFrom > 0 ? min(Int(answer.askAgainFrom), asked.count) : asked.count
            out.none = asked[..<cut].filter { !answered.contains($0) }
            out.askAgain = asked[cut...].filter { !answered.contains($0) }
        } else if let last = asked.lastIndex(where: { answered.contains($0) }) {
            out.unsure = asked[..<last].filter { !answered.contains($0) }
            out.askAgain = asked[(last + 1)...].filter { !answered.contains($0) }
        } else {
            out.unsure = asked
        }
        return out
    }
}

@MainActor
final class GridThumbLoader {
    /// A tile: the decoded image's key (its item's id), the path
    /// GetThumbnails asks by, and the hash the page gave it.
    struct Want: Equatable, Sendable {
        let id: String
        let path: String
        let hash: String
    }

    typealias Request = @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope

    /// Paths per GetThumbnails (the device takes 48 and about 8 MB).
    static let batchSize = 24
    /// A page's tiles decoded before it is shown: about two screens (the
    /// decoded tiles' memory holds ~85 of Images').
    static let prewarmMax = 48
    /// How long tiles that show together are gathered before an ask goes
    /// (they ask one by one as they are laid out).
    static let gather: TimeInterval = 0.03

    let cache: ThumbDiskCache
    /// The size the tiles are decoded for (GridThumbCache).
    let maxPt: CGFloat
    /// Sends GetThumbnails. Tests answer in the device's place.
    var request: Request
    /// The wait after a GetThumbnails that failed (the pages' backoff).
    let retry = PageRetry()
    /// Tiles got their images: the grid draws them.
    var onImages: (@MainActor () -> Void)?

    /// Bumped by reset(): what was on its way for the grid before lands
    /// nowhere.
    private var generation = 0
    /// A page's tiles the cache can't give (or gives big while the device
    /// has the small one), by path: asked for once they show or come near.
    private var known: [String: Want] = [:]
    /// Of those, the ones asked for again for the small one.
    private var reasks = Set<String>()
    /// Waiting to be asked for, in the order they came.
    private var queue: [Want] = []
    /// The paths waiting or on their way.
    private var queued = Set<String>()
    private var sending = false
    private var pumpSoon = false
    /// Paths whose tiles are on screen: asked for first.
    private var visible = Set<String>()
    /// Paths the device said have no thumbnail now.
    private var noThumb = Set<String>()
    /// Tiles being read from the cache.
    private var reading = Set<String>()
    /// Tiles not read while protected data was unavailable, by id.
    private var putOff: [String: Want] = [:]
    /// A path's hash as the last answer for it had it (the file changed
    /// since its page): where its thumbnail was kept.
    private var answeredHash: [String: String] = [:]
    private var watches: [AnyCancellable] = []

    init(maxPt: CGFloat, cache: ThumbDiskCache = .shared, request: @escaping Request) {
        self.maxPt = maxPt
        self.cache = cache
        self.request = request
        // Signed in again, a network came up, back in front: a failed ask
        // goes again now.
        watches.append(OTCConnection.shared.$wakes
            .dropFirst()
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.retry.wake() })
        // The phone unlocked: what was put off goes on.
        watches.append(NotificationCenter.default.publisher(for: ThumbDiskCache.becameAvailable, object: cache)
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.dataBack() })
    }

    /// Through OTCConnection, as a kind of its own (Patience): a batch is
    /// about a page's worth of bytes.
    static let deviceRequest: Request = { payload in
        try await OTCConnection.shared.ask("tiles", base: OTCConnection.pageTimeout) { $0.payload = payload }
    }

    /// A new search: nothing asked for the old grid matters.
    func reset() {
        generation += 1
        known = [:]
        reasks = []
        queue = []
        queued = []
        sending = false
        visible = []
        noThumb = []
        reading = []
        putOff = [:]
        answeredHash = [:]
        retry.reset()
    }

    // MARK: Pages

    /// A page asked for without content came: `files`, each to be the
    /// tile `ids[i]`. Content that came is kept, the page's first tiles are
    /// decoded (from the content or the cache) before the grid shows them,
    /// and what the cache lacks is noted, to be asked for as its tiles
    /// show. Returns, for each file, the bytes its item must hold itself:
    /// nil when the cache has them (or they come by GetThumbnails).
    func takePage(_ files: [Msg_File], ids: [String]) async -> [Data?] {
        guard !files.isEmpty else { return [] }
        // Entries with content: a device that doesn't leave it out.
        cache.noteOmits(!files.contains { $0.hasContent })
        let gen = generation
        let cache = cache
        let maxPt = maxPt
        let prewarm = Self.prewarmMax
        struct Row: Sendable {
            let id: String
            let hash: String
            let content: Data?
            let small: Bool?
        }
        let rows = zip(files, ids).map { f, id in
            Row(id: id, hash: f.hash, content: f.hasContent && !f.content.isEmpty ? f.content : nil,
                small: f.hasThumbnailSmall ? f.thumbnailSmall : nil)
        }
        let decided: [(keep: Data?, plan: ThumbPlan)] = await Task.detached(priority: .userInitiated) {
            var out: [(keep: Data?, plan: ThumbPlan)] = []
            var decode: [(id: String, hash: String, data: Data?)] = []
            for r in rows {
                let plan: ThumbPlan
                var keep: Data?
                if let c = r.content {
                    plan = .inline
                    keep = cache.store(r.hash, kind: ThumbDiskCache.Kind(thumbnailSmall: r.small), data: c) ? nil : c
                } else {
                    plan = ThumbPlan.of(hasContent: false, thumbnailSmall: r.small, cached: cache.kind(of: r.hash))
                }
                out.append((keep, plan))
                if plan != .ask, decode.count < prewarm, GridThumbCache.cached(id: r.id, maxPt: maxPt) == nil {
                    decode.append((r.id, r.hash, r.content))
                }
            }
            _ = GridThumbLoader.decodeEach(decode.count) { i in
                let d = decode[i]
                guard let data = d.data ?? cache.lookup(d.hash)?.data else { return false }
                guard let img = GridThumbCache.decodeTile(data, maxPt: maxPt) else {
                    if d.data == nil { cache.discard(d.hash) }
                    return false
                }
                GridThumbCache.put(img, id: d.id, maxPt: maxPt)
                return true
            }
            return out
        }.value
        guard gen == generation else { return decided.map(\.keep) }
        #if DEBUG
        let inline = decided.filter { $0.plan == .inline }.count
        let cached = decided.filter { $0.plan == .cached || $0.plan == .cachedThenAsk }.count
        print("[GridThumbs] page of \(files.count): \(inline) with content, \(cached) from the cache, \(files.count - inline - cached) to ask")
        #endif
        for (i, d) in decided.enumerated() {
            let f = files[i]
            let w = Want(id: ids[i], path: f.path, hash: f.hash)
            switch d.plan {
            case .ask:
                known[f.path] = w
            case .cachedThenAsk where cache.firstReask(f.hash):
                known[f.path] = w
                reasks.insert(f.path)
            default:
                break
            }
        }
        // Tiles already on screen (a page that fills the grid's end).
        enqueue(visible.compactMap { known.removeValue(forKey: $0) }, front: true)
        return decided.map(\.keep)
    }

    // MARK: Tiles

    /// A tile came on screen: its path is asked for first (if its page
    /// noted it).
    func appeared(_ path: String) {
        visible.insert(path)
        if let w = known.removeValue(forKey: path) { enqueue([w], front: true) }
    }

    func disappeared(_ path: String) {
        visible.remove(path)
    }

    /// A tile on screen has no image (it was never decoded, or its decoded
    /// image was let go): from the cache, else asked for, first.
    func need(_ w: Want) {
        visible.insert(w.path)
        if let noted = known.removeValue(forKey: w.path) {
            enqueue([noted], front: true)
            return
        }
        guard !noThumb.contains(w.path), !queued.contains(w.path) else { return }
        read([w], front: true)
    }

    /// Tiles about to come on screen (about two screens around the ones
    /// shown): decoded from the cache ahead of them, and what the cache
    /// lacks asked for after the tiles on screen.
    func readAhead(_ ws: [Want]) {
        var asks: [Want] = []
        var reads: [Want] = []
        for w in ws {
            if let noted = known.removeValue(forKey: w.path) {
                asks.append(noted)
            } else if !noThumb.contains(w.path), !queued.contains(w.path),
                      GridThumbCache.cached(id: w.id, maxPt: maxPt) == nil {
                reads.append(w)
            }
        }
        enqueue(asks, front: false)
        read(reads, front: false)
    }

    private func read(_ wants: [Want], front: Bool) {
        let ws = wants.filter { !reading.contains($0.id) }
        guard !ws.isEmpty else { return }
        // Locked: nothing can be read, and a miss now isn't one.
        guard cache.dataAvailable else {
            for w in ws { putOff[w.id] = w }
            return
        }
        for w in ws { putOff[w.id] = nil }
        reading.formUnion(ws.map(\.id))
        let gen = generation
        let cache = cache
        let maxPt = maxPt
        let keys = ws.map { answeredHash[$0.path] ?? $0.hash }
        Task { [weak self] in
            let got: [Bool] = await Task.detached(priority: .userInitiated) {
                GridThumbLoader.decodeEach(ws.count) { i in
                    guard let hit = cache.lookup(keys[i]) else { return false }
                    guard let img = GridThumbCache.decodeTile(hit.data, maxPt: maxPt) else {
                        cache.discard(keys[i])
                        return false
                    }
                    GridThumbCache.put(img, id: ws[i].id, maxPt: maxPt)
                    return true
                }
            }.value
            guard let self, gen == self.generation else { return }
            self.reading.subtract(ws.map(\.id))
            if got.contains(true) { self.onImages?() }
            // Locked meanwhile: those reads said nothing.
            guard self.cache.dataAvailable else {
                for (w, ok) in zip(ws, got) where !ok { self.putOff[w.id] = w }
                return
            }
            self.enqueue(zip(ws, got).filter { !$0.1 }.map(\.0), front: front)
        }
    }

    /// The phone's protected data is back: what was put off goes on.
    private func dataBack() {
        let waiting = Array(putOff.values)
        putOff = [:]
        read(waiting, front: true)
        pump()
    }

    // MARK: Asking the device

    private func enqueue(_ wants: [Want], front: Bool) {
        var fresh: [Want] = []
        for w in wants where !queued.contains(w.path) && !noThumb.contains(w.path) {
            queued.insert(w.path)
            fresh.append(w)
        }
        guard !fresh.isEmpty else { return }
        if front {
            queue.insert(contentsOf: fresh, at: 0)
        } else {
            queue.append(contentsOf: fresh)
        }
        // The tiles that show together ask one by one: gathered first.
        guard !pumpSoon else { return }
        pumpSoon = true
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(Self.gather * 1_000_000_000))
            guard let self else { return }
            self.pumpSoon = false
            self.pump()
        }
    }

    /// The next batch, unless one is on its way, waits for its retry, or
    /// the phone is locked: the tiles on screen alone while there are any,
    /// else the rest in the order they came.
    private func pump() {
        guard !sending, !retry.waiting, !queue.isEmpty, cache.dataAvailable else { return }
        var batch = Array(queue.lazy.filter { self.visible.contains($0.path) }.prefix(Self.batchSize))
        if batch.isEmpty { batch = Array(queue.prefix(Self.batchSize)) }
        let picked = Set(batch.map(\.path))
        queue.removeAll { picked.contains($0.path) }
        sending = true
        let gen = generation
        Task { [weak self] in await self?.send(batch, gen) }
    }

    private func send(_ batch: [Want], _ gen: Int) async {
        var g = Msg_GetThumbnails()
        g.paths = batch.map(\.path)
        g.smallThumbnails = true
        #if DEBUG
        print("[GridThumbs] GetThumbnails for \(batch.count)")
        #endif
        let resp = try? await request(.reqGetThumbnails(g))
        guard gen == generation else { return }
        sending = false
        guard let resp, case .respListOfFiles(let lof) = resp.payload else {
            if resp?.errorCode == "unknown_payload" {
                // A device before release 80 has no GetThumbnails: the
                // tiles stay grey.
                for w in batch {
                    queued.remove(w.path)
                    noThumb.insert(w.path)
                }
                pump()
                return
            }
            // Asked again after the wait, first.
            queue.insert(contentsOf: batch, at: 0)
            retry.failed { [weak self] in self?.pump() }
            return
        }
        retry.reset()
        let outcome = ThumbAnswer.of(asked: g.paths, answer: lof, deviceIsNew: cache.deviceOmits == true)
        let byPath = Dictionary(grouping: batch, by: \.path)
        let entries = lof.files.filter { !$0.content.isEmpty && byPath[$0.path] != nil }
        let cache = cache
        let maxPt = maxPt
        // Kept under the answer's hash, and decoded for every tile of its
        // path, off the main thread.
        let decoded: [Bool] = await Task.detached(priority: .userInitiated) {
            GridThumbLoader.decodeEach(entries.count) { i in
                let f = entries[i]
                guard let img = GridThumbCache.decodeTile(f.content, maxPt: maxPt) else { return false }
                cache.store(f.hash, kind: ThumbDiskCache.Kind(f), data: f.content)
                for w in byPath[f.path] ?? [] { GridThumbCache.put(img, id: w.id, maxPt: maxPt) }
                return true
            }
        }.value
        guard gen == generation else { return }
        for (f, ok) in zip(entries, decoded) {
            queued.remove(f.path)
            // Asked again for its small one, and the big one came: the
            // device's small one can't be read. Not asked for again for
            // days (the answer's marker is the one that counts).
            if reasks.remove(f.path) != nil, f.hasThumbnailSmall, !f.thumbnailSmall {
                cache.noteStillBig(f.hash)
            }
            if ok {
                answeredHash[f.path] = f.hash
            } else {
                // A thumbnail that doesn't decode: none to show.
                noThumb.insert(f.path)
            }
        }
        for p in outcome.none {
            queued.remove(p)
            noThumb.insert(p)
        }
        for p in outcome.unsure { queued.remove(p) }
        let again = Set(outcome.askAgain)
        // Still queued: first in line.
        queue.insert(contentsOf: batch.filter { again.contains($0.path) }, at: 0)
        if decoded.contains(true) { onImages?() }
        pump()
    }

    // MARK: Decoding

    /// Runs `body` for 0..<n on every core, its results in order.
    nonisolated static func decodeEach(_ n: Int, _ body: (Int) -> Bool) -> [Bool] {
        guard n > 0 else { return [] }
        let results = DecodeResults(n)
        DispatchQueue.concurrentPerform(iterations: n) { i in results.set(i, body(i)) }
        return results.values
    }
}

/// decodeEach's results: each iteration writes its own slot, under a lock.
private final class DecodeResults: @unchecked Sendable {
    private let lock = NSLock()
    private var slots: [Bool]

    init(_ n: Int) { slots = Array(repeating: false, count: n) }

    func set(_ i: Int, _ v: Bool) { lock.withLock { slots[i] = v } }

    var values: [Bool] { lock.withLock { slots } }
}

/// Keeps ThumbDiskCache.shared told whether the phone's protected data is
/// available (UIApplication.isProtectedDataAvailable): while it isn't, the
/// thumbnail cache reads and writes nothing, so a background launch on a
/// locked phone (the photo sync) neither misses nor loses what it holds.
@MainActor
enum ProtectedDataWatch {
    private static var observers: [NSObjectProtocol] = []

    static func start(_ cache: ThumbDiskCache = .shared) {
        guard observers.isEmpty else { return }
        cache.setDataAvailable(UIApplication.shared.isProtectedDataAvailable)
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: UIApplication.protectedDataWillBecomeUnavailableNotification,
                                            object: nil, queue: .main) { _ in cache.setDataAvailable(false) })
        observers.append(center.addObserver(forName: UIApplication.protectedDataDidBecomeAvailableNotification,
                                            object: nil, queue: .main) { _ in cache.setDataAvailable(true) })
    }
}
