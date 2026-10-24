// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FullPhotoCache.swift
//  OffTheCloud
//
//  The viewer's full-size photos, kept on this phone for the rest of the
//  session: going back to a photo, or opening the viewer again, shows it
//  at once instead of the low-res tile while GetFile fetches it again (a
//  user's complaint). The viewer keeps the last few decoded only while it
//  is open (PhotoGalleryVM.hiResImages); this keeps the bytes behind them.
//
//  The bytes exactly as the device sent them (the GetFile answer: a HEIC
//  comes converted to JPEG, so nothing here assumes the item's type), by
//  content hash: the one the answer carries (the file's row), else the
//  item's. Content without a hash isn't kept. Videos never come here (they
//  stream).
//
//  The owner's choice: a tenth of Settings' Thumbnail Cache size
//  (ThumbCacheLimit.fullPhotoBytes; the thumbnails take the rest, so the
//  size chosen stays the total), and a photo over a quarter of that isn't
//  kept at all, so one panorama can't push out everything else. Least
//  recently used goes first, by an in-memory count of uses: every store
//  and every hit counts as a use.
//
//  Session only: the directory is emptied when the cache is made, which is
//  at launch (AppDelegate), so originals don't stay on the phone from one
//  launch to the next. Settings' Clear and Log Out empty it too. Nothing on
//  disk is ever listed: what the directory holds is what this launch wrote,
//  so the index is complete from the start.
//
//  On disk, in Caches (the system may purge it, which only means fetching
//  again): full-photos/<hash>, written atomically, readable only while the
//  phone is unlocked (complete protection: only the viewer reads them). A
//  file gone or of another size than written is dropped as a miss.
//
//  Thread-safe: the state is behind a lock, and everything that reads or
//  writes a file, or changes the index, runs on one serial queue - so a
//  lookup after a clear or a store sees it done.
//

import Foundation

final class FullPhotoCache: @unchecked Sendable {
    static let shared = FullPhotoCache(
        root: FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("full-photos", isDirectory: true),
        limit: ThumbCacheLimit.fullPhotoBytes(of: ThumbCacheLimit.stored()))

    /// Where the photos are (Caches/full-photos).
    let root: URL
    private let fm = FileManager.default
    // The viewer waits on lookups: not a background queue.
    private let queue = DispatchQueue(label: "otc.full-photo-cache", qos: .userInitiated)

    private struct Entry {
        var size: Int64
        /// When it was last stored or read, in uses (`uses`).
        var used: UInt64
    }

    // Guarded by `lock`; the index and the total only change on `queue`.
    private let lock = NSLock()
    private var index: [String: Entry] = [:]
    private var total: Int64 = 0
    private var limit: Int64
    private var uses: UInt64 = 0

    /// A cache in `root`, which is emptied first (on the queue): what an
    /// earlier launch kept there goes.
    init(root: URL, limit: Int64) {
        self.root = root
        self.limit = limit
        queue.async { self.empty() }
    }

    // MARK: The limit and what is used

    /// The most the photos may take, in bytes. Lowering it deletes the
    /// least recently used at once.
    var limitBytes: Int64 {
        get { lock.withLock { limit } }
        set {
            lock.withLock { limit = newValue }
            queue.async { self.evict() }
        }
    }

    /// The most one photo may take: a quarter of the limit.
    var maxEntryBytes: Int64 { limitBytes / 4 }

    /// The bytes the photos take now (after what is queued).
    func usage() async -> Int64 {
        await withCheckedContinuation { cont in
            queue.async { cont.resume(returning: self.lock.withLock { self.total }) }
        }
    }

    /// Everything queued so far is done (tests).
    func drain() async {
        await withCheckedContinuation { cont in queue.async { cont.resume() } }
    }

    // MARK: Entries

    /// The bytes kept for `hash`, or nil; a hit is the most recently used
    /// from now on.
    func lookup(_ hash: String) async -> Data? {
        guard ThumbDiskCache.isHash(hash) else { return nil }
        return await withCheckedContinuation { cont in
            queue.async { cont.resume(returning: self.read(hash)) }
        }
    }

    /// Keeps `data`, the full-size photo of the content `hash`, as the
    /// device sent it. False when it isn't kept: not a content hash, empty,
    /// or over a quarter of the limit.
    @discardableResult
    func store(_ hash: String, data: Data) -> Bool {
        guard ThumbDiskCache.isHash(hash), !data.isEmpty, Int64(data.count) <= maxEntryBytes else { return false }
        queue.async { self.write(hash, data) }
        return true
    }

    /// The entry for `hash` goes: its bytes didn't decode.
    func discard(_ hash: String) {
        guard ThumbDiskCache.isHash(hash) else { return }
        queue.async { self.drop(hash) }
    }

    /// Settings' Clear and Log Out: every photo goes; the cache goes on
    /// filling from what the viewer fetches next.
    func clear() {
        queue.async { self.empty() }
    }

    // MARK: On the queue

    private func fileURL(_ hash: String) -> URL {
        root.appendingPathComponent(hash, isDirectory: false)
    }

    private func makeDirectory() {
        try? fm.createDirectory(at: root, withIntermediateDirectories: true)
    }

    private func empty() {
        lock.withLock {
            index = [:]
            total = 0
        }
        try? fm.removeItem(at: root)
        makeDirectory()
    }

    private func read(_ hash: String) -> Data? {
        guard let entry = lock.withLock({ index[hash] }) else { return nil }
        let data: Data
        do {
            data = try Data(contentsOf: fileURL(hash))
        } catch let e as CocoaError where e.code == .fileReadNoSuchFile {
            // The system purged Caches.
            drop(hash)
            return nil
        } catch {
            // Can't be read now (the phone locked): a miss, and it stays.
            return nil
        }
        guard Int64(data.count) == entry.size else {
            drop(hash)
            return nil
        }
        lock.withLock {
            uses += 1
            index[hash]?.used = uses
        }
        return data
    }

    private func write(_ hash: String, _ data: Data) {
        let kept: Bool = lock.withLock {
            guard index[hash] != nil else { return false }
            // The same content (the hash names it): only its use counts.
            uses += 1
            index[hash]?.used = uses
            return true
        }
        // The limit may have come down since it was asked to keep it.
        guard !kept, Int64(data.count) <= maxEntryBytes else { return }
        let url = fileURL(hash)
        let options: Data.WritingOptions = [.atomic, .completeFileProtection]
        do {
            try data.write(to: url, options: options)
        } catch {
            // The directory isn't there (the system purged Caches).
            makeDirectory()
            guard (try? data.write(to: url, options: options)) != nil else {
                print("[FullPhotoCache] write failed: \(error.localizedDescription)")
                return
            }
        }
        lock.withLock {
            uses += 1
            index[hash] = Entry(size: Int64(data.count), used: uses)
            total += Int64(data.count)
        }
        evict()
    }

    private func drop(_ hash: String) {
        lock.withLock {
            if let old = index.removeValue(forKey: hash) { total -= old.size }
        }
        try? fm.removeItem(at: fileURL(hash))
    }

    /// Over the limit: the least recently used go until the photos fit.
    private func evict() {
        let gone: [String] = lock.withLock {
            guard total > limit else { return [] }
            var out: [String] = []
            for (hash, e) in index.sorted(by: { $0.value.used < $1.value.used }) {
                if total <= limit { break }
                index[hash] = nil
                total -= e.size
                out.append(hash)
            }
            return out
        }
        for hash in gone { try? fm.removeItem(at: fileURL(hash)) }
    }
}
