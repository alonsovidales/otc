// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  ThumbDiskCache.swift
//  OffTheCloud
//
//  The grids' thumbnails, kept on this phone between launches (the owner's
//  "LRU caching of thumbnails up to 1 GB"), so Images opens from here
//  instead of downloading every tile again. Release 113 pages
//  (SearchPhotos.omit_thumbnails) come without content; each entry's
//  content hash is looked up here, and only what is missing is asked for
//  with GetThumbnails (GridThumbLoader). Pages from older devices still
//  carry the bytes, which are kept here as they arrive.
//
//  Only grid tiles: the small thumbnails every grid asks for (or, where the
//  device has none yet or is older, what it sent instead - each entry says
//  which: Kind). The viewer's big thumbnail (PhotoGalleryVM.bigThumbs)
//  never comes here, so the two never share an entry. One entry per hash:
//  a small one replaces a big or unknown one, a big one an unknown one
//  (replaces), nothing replaces a small one.
//
//  On disk, in Caches (the system may purge it when space runs short,
//  which only means downloading again):
//
//    grid-thumbs/v<formatVersion>/<scope>/<first 2 hex>/<hash>.<s|b|u>
//    grid-thumbs/v<formatVersion>/<scope>/still-big.log
//
//  `scope` is the signed-in device and account: a hash of the endpoint
//  the app signs in to (every OTC user is an instance of its own, with an
//  endpoint of its own). Binding to another one (Save Connection with
//  another device, a sign-in after Log Out) deletes every other scope's
//  directory; Log Out deletes everything (forget, and SecretsStore.logOut
//  empties Caches as well). The kind is the file's suffix, so a scan of
//  names knows it. Each file is an 8-byte header ("OTCT" + the payload's
//  length, big endian) and the JPEG as the device sent it: a file that
//  doesn't match (cut short, overwritten, not ours) is dropped as a miss.
//  An entry goes only when its file is gone or damaged - a file that can't
//  be read now (the phone locked, before its first unlock) is a miss and
//  stays. Files are written atomically (a temporary file renamed over),
//  protected until the phone's first unlock (as the app's Keychain items:
//  a background launch on a locked phone can use them); while iOS says
//  protected data is unavailable (setDataAvailable, kept by
//  ProtectedDataWatch) nothing is read or written at all.
//
//  Least recently used goes first, by file date: there is no index file
//  to keep in step or to corrupt. The first time a scope is bound in a
//  launch its directory is listed once, on the cache's background queue,
//  for sizes and dates into an in-memory index - measured on an M-series
//  Mac, about 0.3 s and +12 MB for 25k files (1 GB of small thumbnails),
//  1.2-1.3 s and +42 MB for 120k (5 GB), most of it the index itself
//  (each shard is listed in its own autorelease pool, or the listing's
//  objects pile up); reads touch an
//  entry in memory, and its file's date once it is more than stampEvery
//  old (one metadata write per entry per 10 minutes, not one per tile
//  drawn). Over the limit, the oldest are deleted until the cache is under
//  90% of it - a sort of the index each ~10% of the limit written, not one
//  per write. Lookups never wait for the listing: they go to the file
//  system (three names to try at most).
//
//  A big or unknown entry is asked for again (for the small one) at most
//  once a launch while a page says the device has the small one; when
//  that answer is the big one again (the device's small one can't be
//  read), still-big.log records it and it isn't asked for again for
//  reaskAfter (Files' grid, whose listings don't say, has its own once a
//  launch: firstFilesReask).
//
//  Thread-safe: lookups run on the caller's thread (never the main one -
//  the grids read off it), every change to the files on one serial queue.
//

import Foundation
import CryptoKit

final class ThumbDiskCache: @unchecked Sendable {
    /// Which thumbnail an entry holds: the file's suffix.
    enum Kind: String, CaseIterable, Sendable {
        /// The small one, for grids (release 111).
        case small = "s"
        /// The big one: the device has no small one yet (it makes one
        /// shortly) - an entry of a release 113 answer whose
        /// thumbnail_small was false.
        case big = "b"
        /// A device before release 113 didn't say which (asked for small:
        /// the small one, or the big one where it had none yet).
        case unknown = "u"

        /// An answer entry's File.thumbnail_small: unset (a device before
        /// release 113) is unknown.
        init(thumbnailSmall: Bool?) {
            switch thumbnailSmall {
            case true?: self = .small
            case false?: self = .big
            case nil: self = .unknown
            }
        }

        init(_ f: Msg_File) {
            self.init(thumbnailSmall: f.hasThumbnailSmall ? f.thumbnailSmall : nil)
        }
    }

    struct Hit: Equatable, Sendable {
        let kind: Kind
        let data: Data
    }

    /// Bumped when the layout or the header changes: the directory of an
    /// older version is deleted when a scope is bound.
    static let formatVersion = 1
    /// No single thumbnail is kept above this (a big one made before
    /// release 111 can be a very long screenshot); GetThumbnails answers
    /// stop at about 8 MB.
    static let maxEntryBytes = 8 << 20
    /// An entry's file date is brought up to date at most this often.
    static let stampEvery: TimeInterval = 600
    /// A hash whose small one was asked for and came big again isn't asked
    /// for again for this long.
    static let reaskAfter: TimeInterval = 7 * 24 * 3600
    /// Posted (object: the cache) when files can be read again
    /// (setDataAvailable): what was put off is read now.
    static let becameAvailable = Notification.Name("ThumbDiskCacheBecameAvailable")
    private static let magic: [UInt8] = Array("OTCT".utf8)
    private static let headerSize = 8
    private static let stillBigLog = "still-big.log"

    static let shared = ThumbDiskCache(
        root: FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("grid-thumbs", isDirectory: true),
        limit: ThumbCacheLimit.stored())

    /// Where every scope's directory lives (Caches/grid-thumbs).
    let root: URL
    private let fm = FileManager.default
    private let queue = DispatchQueue(label: "otc.thumb-disk-cache", qos: .utility)
    private let defaults: UserDefaults
    private let clock: @Sendable () -> TimeInterval

    private struct Entry {
        var size: Int64
        var kind: Kind
        /// When it was last read or written (seconds since 1970).
        var used: TimeInterval
        /// The date its file has (see stampEvery).
        var stamped: TimeInterval
    }

    // Everything below is guarded by `lock`. The index and the total only
    // change on `queue`.
    private let lock = NSLock()
    private var scope: String?
    private var dir: URL?
    /// Bumped by every bind, clear and forget: work queued before one
    /// belongs to what is gone.
    private var epoch = 0
    /// Written by store, not on disk yet: what lookups find meanwhile.
    private var pending: [String: Hit] = [:]
    private var index: [String: Entry] = [:]
    /// The scope's directory has been listed into `index`.
    private var indexed = false
    private var total: Int64 = 0
    private var limit: Int64
    /// Hashes asked for again this launch: from a page (firstReask) and
    /// from Files' grid (firstFilesReask).
    private var reasked = Set<String>()
    private var filesReasked = Set<String>()
    /// Hashes whose small one was asked for and came big, and when.
    private var stillBig: [String: TimeInterval] = [:]
    /// Whether the signed-in device leaves the content out of pages asked
    /// to (release 113): nil until a page said.
    private var omits: Bool?
    /// Whether files can be read and written now (iOS's protected data).
    private var available = true

    init(root: URL, limit: Int64, defaults: UserDefaults = .standard,
         clock: @escaping @Sendable () -> TimeInterval = { Date().timeIntervalSince1970 }) {
        self.root = root
        self.limit = limit
        self.defaults = defaults
        self.clock = clock
    }

    // MARK: The signed-in device

    /// The scope of an endpoint: the device and account signed in to.
    static func scopeID(endpoint: String) -> String {
        SHA256.hash(data: Data(endpoint.utf8)).prefix(16).map { String(format: "%02x", $0) }.joined()
    }

    /// The app is signed in to `endpoint` (normalized,
    /// SecretsStore.endpointURLString): its thumbnails from now on. Another
    /// device's (or account's) are deleted.
    func use(endpoint: String) {
        use(scopeID: Self.scopeID(endpoint: endpoint))
    }

    func use(scopeID id: String) {
        let ep: Int? = lock.withLock {
            guard scope != id else { return nil }
            scope = id
            dir = root.appendingPathComponent("v\(Self.formatVersion)", isDirectory: true)
                .appendingPathComponent(id, isDirectory: true)
            epoch += 1
            pending = [:]
            index = [:]
            indexed = false
            total = 0
            reasked = []
            filesReasked = []
            stillBig = [:]
            omits = Self.storedOmits(defaults, scope: id)
            return epoch
        }
        guard let ep else { return }
        queue.async { self.prepare(ep) }
    }

    /// Log Out: nothing is kept, and nothing is stored until the next
    /// sign-in binds a scope again.
    func forget() {
        lock.withLock {
            scope = nil
            dir = nil
            epoch += 1
            pending = [:]
            index = [:]
            indexed = false
            total = 0
            reasked = []
            filesReasked = []
            stillBig = [:]
            omits = nil
        }
        defaults.removeObject(forKey: Self.omitsKey)
        let root = root
        queue.async { try? FileManager.default.removeItem(at: root) }
    }

    /// Settings' "Clear thumbnail cache": the scope's thumbnails go; the
    /// cache goes on filling from what is downloaded next.
    func clear() {
        let ep: Int? = lock.withLock {
            guard dir != nil else { return nil }
            epoch += 1
            pending = [:]
            // Empty from now on: lookups miss at once, before the files
            // are deleted (on the queue, ahead of any write that follows).
            index = [:]
            indexed = true
            total = 0
            reasked = []
            filesReasked = []
            stillBig = [:]
            return epoch
        }
        guard let ep else { return }
        queue.async {
            guard let d = self.current(ep) else { return }
            // What a bind's preparing (which this clear overtook, if it
            // hadn't run yet) would have deleted.
            self.removeOthers(than: d)
            try? self.fm.removeItem(at: d)
            self.makeDirectory(d)
        }
    }

    // MARK: Protected data

    /// Whether files can be read and written now: false while iOS says the
    /// phone's protected data is unavailable (ProtectedDataWatch).
    var dataAvailable: Bool { lock.withLock { available } }

    /// iOS's protected data became available (true) or is going away
    /// (false). Back available, what was put off is read again
    /// (becameAvailable).
    func setDataAvailable(_ value: Bool) {
        let changed: Bool = lock.withLock {
            guard available != value else { return false }
            available = value
            return true
        }
        if changed && value {
            NotificationCenter.default.post(name: Self.becameAvailable, object: self)
        }
    }

    // MARK: The limit and what is used

    /// The most the thumbnails may take, in bytes. Lowering it deletes the
    /// least recently used at once.
    var limitBytes: Int64 {
        get { lock.withLock { limit } }
        set {
            let ep: Int = lock.withLock {
                limit = newValue
                return epoch
            }
            queue.async { self.evict(ep) }
        }
    }

    /// The bytes the scope's thumbnails take, once its directory has been
    /// listed (waits for that); nil while no device is signed in to.
    func usage() async -> Int64? {
        await withCheckedContinuation { cont in
            queue.async {
                cont.resume(returning: self.lock.withLock { self.dir != nil && self.indexed ? self.total : nil })
            }
        }
    }

    /// Everything queued so far is done (tests).
    func drain() async {
        await withCheckedContinuation { cont in queue.async { cont.resume() } }
    }

    // MARK: What the device does

    /// Whether the signed-in device leaves the content out of the pages
    /// that ask it to (release 113): nil until one of its pages said.
    /// Kept per scope: a first page is then asked big from the start.
    var deviceOmits: Bool? { lock.withLock { omits } }

    /// A page asked without content came (`omitted`: its entries had none).
    /// Only for a bound scope: what a device does is kept with its cache.
    func noteOmits(_ omitted: Bool) {
        let id: String? = lock.withLock {
            guard scope != nil, omits != omitted else { return nil }
            omits = omitted
            return scope
        }
        if let id { defaults.set("\(id):\(omitted ? 1 : 0)", forKey: Self.omitsKey) }
    }

    private static let omitsKey = "thumbCacheDeviceOmits"

    private static func storedOmits(_ defaults: UserDefaults, scope: String) -> Bool? {
        guard let s = defaults.string(forKey: omitsKey), s.hasPrefix(scope + ":") else { return nil }
        return s.hasSuffix(":1")
    }

    /// True the first time `hash` is asked for again this launch from a
    /// page that says the device has its small one - unless its small one
    /// was asked for and came big within reaskAfter (noteStillBig).
    func firstReask(_ hash: String) -> Bool {
        let now = clock()
        return lock.withLock {
            if let t = stillBig[hash], now - t < Self.reaskAfter { return false }
            return reasked.insert(hash).inserted
        }
    }

    /// Files' grid's own once a launch (its listings don't say whether the
    /// device has the small one), under the same still-big rule.
    func firstFilesReask(_ hash: String) -> Bool {
        let now = clock()
        return lock.withLock {
            if let t = stillBig[hash], now - t < Self.reaskAfter { return false }
            return filesReasked.insert(hash).inserted
        }
    }

    /// `hash`'s small one was asked for again and the device sent the big
    /// one (its small one can't be read): not asked for again for
    /// reaskAfter, launches included.
    func noteStillBig(_ hash: String) {
        guard Self.isHash(hash) else { return }
        let now = clock()
        let ep: Int? = lock.withLock {
            guard dir != nil else { return nil }
            stillBig[hash] = now
            return epoch
        }
        guard let ep else { return }
        queue.async {
            guard let d = self.current(ep), self.dataAvailable else { return }
            AppendLog(url: d.appendingPathComponent(Self.stillBigLog)).append(hash, String(Int(now)))
        }
    }

    // MARK: Entries

    /// Device content hashes are 64 lowercase hex digits (dao.IsContentHash):
    /// nothing else names a file here.
    static func isHash(_ h: String) -> Bool {
        h.utf8.count == 64 && h.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
    }

    /// Whether a `new` thumbnail replaces an `old` one: a small one
    /// replaces any other, and a big one an unknown one (an answer that
    /// says which it sent knows better than one that didn't - a device can
    /// say a photo's small one is there and then send the big one, when
    /// the small one can't be read; that big one is kept, and not asked
    /// for again: noteStillBig). Nothing replaces its own kind.
    static func replaces(_ new: Kind, _ old: Kind) -> Bool {
        rank(new) > rank(old)
    }

    private static func rank(_ k: Kind) -> Int {
        switch k {
        case .small: return 2
        case .big: return 1
        case .unknown: return 0
        }
    }

    /// Keeps `data` (a grid's thumbnail of the content `hash`, of `kind`).
    /// True when it is kept (or one at least as good already is): readable
    /// by lookup from now on, so the caller needn't hold the bytes. False
    /// when nothing can keep it (no device signed in to, protected data
    /// unavailable, not a content hash, empty, too big).
    @discardableResult
    func store(_ hash: String, kind: Kind, data: Data) -> Bool {
        guard Self.isHash(hash), !data.isEmpty, data.count <= Self.maxEntryBytes else { return false }
        let hit = Hit(kind: kind, data: data)
        enum Verdict { case refused, kept(Int), write(Int) }
        let verdict: Verdict = lock.withLock {
            guard dir != nil, available else { return .refused }
            if let p = pending[hash], !Self.replaces(kind, p.kind) { return .kept(epoch) }
            if indexed, let e = index[hash], !Self.replaces(kind, e.kind) { return .kept(epoch) }
            pending[hash] = hit
            if kind == .small { stillBig[hash] = nil }
            return .write(epoch)
        }
        switch verdict {
        case .refused:
            return false
        case .kept(let ep):
            queue.async { self.touch(hash, ep) }
            return true
        case .write(let ep):
            queue.async { self.write(hash, hit, ep) }
            return true
        }
    }

    /// The thumbnail kept for `hash`, or nil. Reads the file: call it off
    /// the main thread. A file that is there but can't be read now (the
    /// phone locked, no permission) is a miss and stays; only a file gone
    /// or damaged loses its entry.
    func lookup(_ hash: String) -> Hit? {
        guard Self.isHash(hash) else { return nil }
        let (d, ep, waiting, known, absent, readable): (URL?, Int, Hit?, Kind?, Bool, Bool) = lock.withLock {
            (dir, epoch, pending[hash], index[hash]?.kind, indexed && index[hash] == nil, available)
        }
        if let waiting { return waiting }
        guard let d, !absent, readable else { return nil }
        for kind in known.map({ [$0] }) ?? Kind.allCases {
            let raw: Data
            do {
                raw = try Data(contentsOf: fileURL(d, hash, kind))
            } catch let e as CocoaError where e.code == .fileReadNoSuchFile {
                continue
            } catch {
                return nil
            }
            guard let payload = Self.payload(of: raw) else {
                queue.async { self.drop(hash, ep, only: kind) }
                return nil
            }
            queue.async { self.touch(hash, ep) }
            return Hit(kind: kind, data: payload)
        }
        // The index had it, the file is gone (the system purged Caches) -
        // unless another kind replaced it meanwhile.
        if let known { queue.async { self.drop(hash, ep, only: known) } }
        return nil
    }

    /// The kind kept for `hash`, without reading it (from the index once
    /// the directory is listed, else by the file names).
    func kind(of hash: String) -> Kind? {
        guard Self.isHash(hash) else { return nil }
        let (d, waiting, known, isIndexed): (URL?, Kind?, Kind?, Bool) = lock.withLock {
            (dir, pending[hash]?.kind, index[hash]?.kind, indexed)
        }
        if let waiting { return waiting }
        guard let d else { return nil }
        if isIndexed { return known }
        return Kind.allCases.first { fm.fileExists(atPath: fileURL(d, hash, $0).path) }
    }

    /// The entry for `hash` goes: its image didn't decode, or the content
    /// was deleted from the device here (nothing of it stays on the phone).
    func discard(_ hash: String) {
        guard Self.isHash(hash) else { return }
        let ep: Int = lock.withLock {
            pending[hash] = nil
            return epoch
        }
        queue.async { self.drop(hash, ep) }
    }

    // MARK: On the queue

    /// The bound directory, while `ep` is still the current state.
    private func current(_ ep: Int) -> URL? {
        lock.withLock { epoch == ep ? dir : nil }
    }

    private func fileURL(_ d: URL, _ hash: String, _ kind: Kind) -> URL {
        d.appendingPathComponent(String(hash.prefix(2)), isDirectory: true)
            .appendingPathComponent("\(hash).\(kind.rawValue)", isDirectory: false)
    }

    private func makeDirectory(_ d: URL) {
        try? fm.createDirectory(at: d, withIntermediateDirectories: true,
                                attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication])
    }

    private static func payload(of raw: Data) -> Data? {
        guard raw.count > headerSize, raw.prefix(4).elementsEqual(magic) else { return nil }
        let n = raw.dropFirst(4).prefix(4).reduce(0) { $0 << 8 | Int($1) }
        guard n == raw.count - headerSize else { return nil }
        return Data(raw[(raw.startIndex + headerSize)...])
    }

    private static func framed(_ data: Data) -> Data {
        var out = Data(capacity: headerSize + data.count)
        out.append(contentsOf: magic)
        let n = UInt32(data.count)
        out.append(contentsOf: [UInt8(n >> 24 & 0xff), UInt8(n >> 16 & 0xff), UInt8(n >> 8 & 0xff), UInt8(n & 0xff)])
        out.append(data)
        return out
    }

    /// Older formats' and other scopes' directories: only `d` stays.
    private func removeOthers(than d: URL) {
        let version = d.deletingLastPathComponent()
        for item in (try? fm.contentsOfDirectory(at: root, includingPropertiesForKeys: nil)) ?? []
        where item.lastPathComponent != version.lastPathComponent {
            try? fm.removeItem(at: item)
        }
        for item in (try? fm.contentsOfDirectory(at: version, includingPropertiesForKeys: nil)) ?? []
        where item.lastPathComponent != d.lastPathComponent {
            try? fm.removeItem(at: item)
        }
    }

    /// A scope was bound: older formats' and other scopes' directories go,
    /// and this one is listed.
    private func prepare(_ ep: Int) {
        guard let d = current(ep) else { return }
        removeOthers(than: d)
        makeDirectory(d)
        scan(d, ep)
        loadStillBig(d, ep)
    }

    /// The hashes asked for again that came big, still within reaskAfter
    /// (the log is rewritten without the others once most have expired).
    private func loadStillBig(_ d: URL, _ ep: Int) {
        let log = AppendLog(url: d.appendingPathComponent(Self.stillBigLog))
        let now = clock()
        var fresh: [String: TimeInterval] = [:]
        for (hash, t) in log.read() {
            guard Self.isHash(hash), let at = TimeInterval(t), now - at < Self.reaskAfter else { continue }
            fresh[hash] = max(fresh[hash] ?? 0, at)
        }
        if log.lines > max(64, 2 * fresh.count) {
            log.replace(with: fresh.map { ($0.key, String(Int($0.value))) })
        }
        lock.withLock {
            guard epoch == ep else { return }
            stillBig.merge(fresh) { max($0, $1) }
        }
    }

    /// Lists the scope's files into the index: sizes, kinds (the names)
    /// and when each was last used (the file dates). Anything else in the
    /// directory goes (a temporary file a kill left behind), and of two
    /// files for one hash (a kill between writing a small one and removing
    /// the big one) the better stays.
    private func scan(_ d: URL, _ ep: Int) {
        let keys: [URLResourceKey] = [.fileSizeKey, .contentModificationDateKey, .isDirectoryKey]
        var found: [String: Entry] = [:]
        var sum: Int64 = 0
        for shard in (try? fm.contentsOfDirectory(at: d, includingPropertiesForKeys: [.isDirectoryKey])) ?? [] {
            if shard.lastPathComponent == Self.stillBigLog { continue }
            guard (try? shard.resourceValues(forKeys: [.isDirectoryKey]))?.isDirectory == true else {
                try? fm.removeItem(at: shard)
                continue
            }
            // The URLs and their resource values are autoreleased: let a
            // shard's go before the next (tens of MB otherwise, at 25k).
            autoreleasepool {
                for file in (try? fm.contentsOfDirectory(at: shard, includingPropertiesForKeys: keys)) ?? [] {
                    let name = file.lastPathComponent
                    let parts = name.split(separator: ".", omittingEmptySubsequences: false)
                    guard parts.count == 2, Self.isHash(String(parts[0])), String(parts[0]).hasPrefix(shard.lastPathComponent),
                          let kind = Kind(rawValue: String(parts[1])),
                          let values = try? file.resourceValues(forKeys: Set(keys)), values.isDirectory != true,
                          let size = values.fileSize, size > Self.headerSize else {
                        try? fm.removeItem(at: file)
                        continue
                    }
                    let hash = String(parts[0])
                    let date = values.contentModificationDate?.timeIntervalSince1970 ?? 0
                    let entry = Entry(size: Int64(size), kind: kind, used: date, stamped: date)
                    if let other = found[hash] {
                        // The one that would have replaced the other stays.
                        let keepNew = Self.replaces(kind, other.kind)
                        let loser = keepNew ? other.kind : kind
                        try? fm.removeItem(at: fileURL(d, hash, loser))
                        if keepNew {
                            sum -= other.size
                            found[hash] = entry
                            sum += entry.size
                        }
                        continue
                    }
                    found[hash] = entry
                    sum += entry.size
                }
            }
        }
        let applied: Bool = lock.withLock {
            guard epoch == ep else { return false }
            index = found
            total = sum
            indexed = true
            return true
        }
        if applied { evict(ep) }
    }

    private func write(_ hash: String, _ hit: Hit, _ ep: Int) {
        defer {
            lock.withLock {
                if epoch == ep, pending[hash] == hit { pending[hash] = nil }
            }
        }
        guard let d = current(ep), dataAvailable else { return }
        let existing: Entry? = lock.withLock { index[hash] }
        if let existing, !Self.replaces(hit.kind, existing.kind) {
            touch(hash, ep)
            return
        }
        let url = fileURL(d, hash, hit.kind)
        let bytes = Self.framed(hit.data)
        let options: Data.WritingOptions = [.atomic, .completeFileProtectionUntilFirstUserAuthentication]
        do {
            try bytes.write(to: url, options: options)
        } catch {
            // The shard's directory isn't there yet (or Caches was purged).
            makeDirectory(url.deletingLastPathComponent())
            guard (try? bytes.write(to: url, options: options)) != nil else {
                print("[ThumbDiskCache] write failed: \(error.localizedDescription)")
                return
            }
        }
        for kind in Kind.allCases where kind != hit.kind {
            try? fm.removeItem(at: fileURL(d, hash, kind))
        }
        let now = clock()
        // The file's date is when it was last used, the cache's own clock.
        try? fm.setAttributes([.modificationDate: Date(timeIntervalSince1970: now)], ofItemAtPath: url.path)
        let kept: Bool = lock.withLock {
            guard epoch == ep else { return false }
            if let old = index[hash] { total -= old.size }
            index[hash] = Entry(size: Int64(bytes.count), kind: hit.kind, used: now, stamped: now)
            total += Int64(bytes.count)
            return true
        }
        // Cleared or unbound while it was written: it doesn't stay.
        guard kept else {
            try? fm.removeItem(at: url)
            return
        }
        evict(ep)
    }

    private func touch(_ hash: String, _ ep: Int) {
        let now = clock()
        let stamp: (URL, Kind)? = lock.withLock {
            guard epoch == ep, available, let d = dir, var e = index[hash] else { return nil }
            e.used = now
            var restamp = false
            if now - e.stamped >= Self.stampEvery {
                e.stamped = now
                restamp = true
            }
            index[hash] = e
            return restamp ? (d, e.kind) : nil
        }
        if let (d, kind) = stamp {
            try? fm.setAttributes([.modificationDate: Date(timeIntervalSince1970: now)],
                                  ofItemAtPath: fileURL(d, hash, kind).path)
        }
    }

    /// The entry for `hash` goes: every kind's file, or `only` that kind's
    /// (what a lookup found missing or damaged - a write since may have
    /// replaced it with another).
    private func drop(_ hash: String, _ ep: Int, only: Kind? = nil) {
        guard let d = current(ep) else { return }
        for kind in only.map({ [$0] }) ?? Kind.allCases { try? fm.removeItem(at: fileURL(d, hash, kind)) }
        lock.withLock {
            guard epoch == ep, let old = index[hash], only == nil || old.kind == only else { return }
            index[hash] = nil
            total -= old.size
        }
    }

    /// Over the limit: the least recently used go until the thumbnails
    /// take under 90% of it.
    private func evict(_ ep: Int) {
        let snapshot: (URL, [String: Entry], Int64, Int64)? = lock.withLock {
            guard epoch == ep, indexed, let d = dir, total > limit else { return nil }
            return (d, index, total, limit)
        }
        guard let (d, entries, used, cap) = snapshot else { return }
        let target = cap - cap / 10
        var left = used
        var victims: [(String, Entry)] = []
        for (hash, e) in entries.sorted(by: { $0.value.used < $1.value.used }) {
            if left <= target { break }
            victims.append((hash, e))
            left -= e.size
        }
        var gone: [(String, Kind)] = []
        lock.withLock {
            guard epoch == ep else { return }
            for (hash, e) in victims {
                // Rewritten meanwhile (another kind, or read since): not this one.
                guard let now = index[hash], now.kind == e.kind, now.used == e.used else { continue }
                index[hash] = nil
                total -= now.size
                gone.append((hash, now.kind))
            }
        }
        for (hash, kind) in gone { try? fm.removeItem(at: fileURL(d, hash, kind)) }
    }
}

/// Settings' "Thumbnail Cache" size, kept per phone (UserDefaults; Log Out
/// resets it with the rest).
enum ThumbCacheLimit {
    static let key = "thumbCacheLimit"
    /// 250 MB, 500 MB, 1 GB, 2 GB, 5 GB - decimal, as the sizes are shown.
    static let choices: [Int64] = [250_000_000, 500_000_000, 1_000_000_000, 2_000_000_000, 5_000_000_000]
    static let defaultBytes: Int64 = 1_000_000_000

    /// The chosen size; the default for nothing chosen, or anything not
    /// among the choices.
    static func stored(_ defaults: UserDefaults = .standard) -> Int64 {
        let v = (defaults.object(forKey: key) as? NSNumber)?.int64Value ?? defaultBytes
        return choices.contains(v) ? v : defaultBytes
    }

    static func save(_ bytes: Int64, _ defaults: UserDefaults = .standard) {
        defaults.set(NSNumber(value: bytes), forKey: key)
    }

    /// "250 MB", "1 GB", "12.3 MB" (ByteCountFormatter's file sizes).
    static func text(_ bytes: Int64) -> String {
        let f = ByteCountFormatter()
        f.countStyle = .file
        f.allowsNonnumericFormatting = false
        return f.string(fromByteCount: bytes)
    }

    /// Settings' line: "Using 120 MB of 1 GB".
    static func usage(_ used: Int64, of limit: Int64) -> String {
        "Using \(text(used)) of \(text(limit))"
    }
}

/// Posted when Settings clears the thumbnail cache: screens let the
/// thumbnails they keep in memory go too (Files' grid, the viewer's big
/// thumbnails).
extension Notification.Name {
    static let otcThumbnailCacheCleared = Notification.Name("otcThumbnailCacheCleared")
}
