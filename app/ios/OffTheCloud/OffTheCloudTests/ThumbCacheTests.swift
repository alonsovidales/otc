// SPDX-License-Identifier: AGPL-3.0-or-later

import CryptoKit
import Foundation
import SwiftProtobuf
import Testing
import UIKit
@testable import OffTheCloud

// MARK: - Helpers

/// Waits (`seconds` at most) for `done`.
@MainActor
private func waitUntil(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

/// A content hash as the device writes them: 64 lowercase hex digits.
private func contentHash(_ s: String) -> String {
    SHA256.hash(data: Data(s.utf8)).map { String(format: "%02x", $0) }.joined()
}

/// A thumbnail cache of a test's own, bound to no device: nothing is kept
/// in it and nothing about a device is learnt, so a test never touches the
/// app's (a test simulator may be signed in to a real device).
func testThumbCache() -> ThumbDiskCache {
    ThumbDiskCache(root: FileManager.default.temporaryDirectory.appendingPathComponent("thumbcache-\(UUID().uuidString)"),
                   limit: 1_000_000_000, defaults: UserDefaults(suiteName: "thumbcache-\(UUID().uuidString)")!)
}

private func tempRoot() -> URL {
    FileManager.default.temporaryDirectory.appendingPathComponent("thumbcache-\(UUID().uuidString)", isDirectory: true)
}

private func freshDefaults() -> UserDefaults {
    UserDefaults(suiteName: "thumbcache-\(UUID().uuidString)")!
}

private func picture(_ side: CGFloat = 40, _ color: UIColor = .orange) -> Data {
    let fmt = UIGraphicsImageRendererFormat()
    fmt.scale = 1
    return UIGraphicsImageRenderer(size: CGSize(width: side, height: side), format: fmt).image { ctx in
        color.setFill()
        ctx.fill(CGRect(x: 0, y: 0, width: side, height: side))
    }.jpegData(compressionQuality: 0.8)!
}

/// The cache's clock, moved by the test.
private final class TestClock: @unchecked Sendable {
    private let lock = NSLock()
    private var t: TimeInterval = 1000
    var now: TimeInterval {
        get { lock.withLock { t } }
        set { lock.withLock { t = newValue } }
    }
}

/// A cache in a directory of its own, bound to `scope`.
private func boundCache(root: URL = tempRoot(), limit: Int64 = 1_000_000_000, scope: String = "scope1",
                        clock: TestClock = TestClock(), defaults: UserDefaults = freshDefaults()) async -> ThumbDiskCache {
    let c = ThumbDiskCache(root: root, limit: limit, defaults: defaults, clock: { clock.now })
    c.use(scopeID: scope)
    await c.drain()
    return c
}

/// Files under `dir` (recursively), by name.
private func names(under dir: URL) -> [String] {
    let e = FileManager.default.enumerator(at: dir, includingPropertiesForKeys: [.isDirectoryKey])
    var out: [String] = []
    while let u = e?.nextObject() as? URL {
        if (try? u.resourceValues(forKeys: [.isDirectoryKey]))?.isDirectory == true { continue }
        out.append(u.lastPathComponent)
    }
    return out.sorted()
}

private func framed(_ payload: Data, length: UInt32? = nil) -> Data {
    let n = length ?? UInt32(payload.count)
    var d = Data("OTCT".utf8)
    d.append(contentsOf: [UInt8(n >> 24 & 0xff), UInt8(n >> 16 & 0xff), UInt8(n >> 8 & 0xff), UInt8(n & 0xff)])
    d.append(payload)
    return d
}

private func file(_ path: String, hash: String, content: Data? = nil, small: Bool? = nil) -> Msg_File {
    var f = Msg_File()
    f.path = path
    f.hash = hash
    f.mime = "image/jpeg"
    if let content { f.content = content }
    if let small { f.thumbnailSmall = small }
    return f
}

// MARK: - The cache on disk

struct ThumbDiskCacheTests {
    @Test func whatIsStoredIsReadBackAndSurvivesARelaunch() async {
        let root = tempRoot()
        let defaults = freshDefaults()
        let a = await boundCache(root: root, defaults: defaults)
        let h = contentHash("a")
        let bytes = picture()
        #expect(a.store(h, kind: .small, data: bytes))
        // Readable at once, before it is written.
        #expect(a.lookup(h) == .init(kind: .small, data: bytes))
        await a.drain()
        #expect(names(under: root) == ["\(h).s"])
        let used = await a.usage()
        #expect(used == Int64(bytes.count + 8))

        // Another launch: the same scope finds it, and counts it.
        let b = await boundCache(root: root, defaults: defaults)
        #expect(b.kind(of: h) == .small)
        #expect(b.lookup(h) == .init(kind: .small, data: bytes))
        #expect(await b.usage() == used)
    }

    @Test func oneEntryPerHashAndOnlyABetterKindReplacesIt() async {
        let root = tempRoot()
        let c = await boundCache(root: root)
        let h = contentHash("k")
        let unknown = Data("unknown".utf8), big = Data("big".utf8), small = Data("small".utf8)
        c.store(h, kind: .unknown, data: unknown)
        await c.drain()
        #expect(c.lookup(h)?.kind == .unknown)
        // A big one (the device said so) replaces an unknown one...
        c.store(h, kind: .big, data: big)
        await c.drain()
        #expect(c.lookup(h) == .init(kind: .big, data: big))
        // ...but not the other way round.
        c.store(h, kind: .unknown, data: unknown)
        await c.drain()
        #expect(c.lookup(h) == .init(kind: .big, data: big))
        // A small one replaces it, and nothing replaces a small one.
        c.store(h, kind: .small, data: small)
        await c.drain()
        c.store(h, kind: .big, data: big)
        c.store(h, kind: .unknown, data: unknown)
        await c.drain()
        #expect(c.lookup(h) == .init(kind: .small, data: small))
        #expect(names(under: root) == ["\(h).s"])
        #expect(await c.usage() == Int64(small.count + 8))
        #expect(ThumbDiskCache.replaces(.small, .big) && ThumbDiskCache.replaces(.small, .unknown)
                && ThumbDiskCache.replaces(.big, .unknown))
        #expect(!ThumbDiskCache.replaces(.big, .small) && !ThumbDiskCache.replaces(.unknown, .big)
                && !ThumbDiskCache.replaces(.small, .small))
    }

    @Test func theLeastRecentlyUsedGoFirstUnderNinetyPercent() async {
        let clock = TestClock()
        let c = await boundCache(limit: 5_000, clock: clock)
        let entry = Data(repeating: 7, count: 1000) // 1008 bytes on disk
        let h = ["a", "b", "c", "d", "e"].map(contentHash)
        for (i, hash) in h.prefix(4).enumerated() {
            clock.now = 1000 * Double(i + 1)
            c.store(hash, kind: .small, data: entry)
            await c.drain()
        }
        #expect(await c.usage() == 4 * 1008)
        // "a", the oldest, is read: it is the newest now.
        clock.now = 5000
        #expect(c.lookup(h[0]) != nil)
        await c.drain()
        clock.now = 6000
        c.store(h[4], kind: .small, data: entry)
        await c.drain()
        // Over 5000: "b" (used at 2000) goes, which brings it under 4500.
        #expect(c.lookup(h[1]) == nil)
        for hash in [h[0], h[2], h[3], h[4]] { #expect(c.lookup(hash) != nil) }
        #expect(await c.usage() == 4 * 1008)
    }

    @Test func theOrderOfUseSurvivesARelaunch() async {
        let root = tempRoot()
        let clock = TestClock()
        let defaults = freshDefaults()
        let a = await boundCache(root: root, clock: clock, defaults: defaults)
        let entry = Data(repeating: 1, count: 1000)
        let h = ["a", "b", "c"].map(contentHash)
        for (i, hash) in h.enumerated() {
            clock.now = 1000 * Double(i + 1)
            a.store(hash, kind: .small, data: entry)
            await a.drain()
        }
        // Read 10 minutes or more after it was written: its file's date moves.
        clock.now = 5000
        #expect(a.lookup(h[0]) != nil)
        await a.drain()
        // Relaunched with room for two: "b" was the least recently used.
        let b = await boundCache(root: root, limit: 2_500, clock: clock, defaults: defaults)
        #expect(b.lookup(h[1]) == nil)
        #expect(b.lookup(h[0]) != nil)
        #expect(b.lookup(h[2]) != nil)
        #expect(await b.usage() == 2 * 1008)
    }

    @Test func aSmallerLimitEvictsAtOnce() async {
        let clock = TestClock()
        let c = await boundCache(clock: clock)
        let entry = Data(repeating: 2, count: 1000)
        for i in 0..<10 {
            // An entry is used when it is written (on the cache's queue).
            clock.now = 1000 + Double(i)
            c.store(contentHash("\(i)"), kind: .small, data: entry)
            await c.drain()
        }
        #expect(await c.usage() == 10 * 1008)
        c.limitBytes = 5_000
        // Under 90% of it: the four newest stay.
        #expect(await c.usage() == 4 * 1008)
        #expect(c.lookup(contentHash("9")) != nil)
        #expect(c.lookup(contentHash("0")) == nil)
    }

    @Test func clearEmptiesTheScopeAndItFillsAgain() async {
        let root = tempRoot()
        let c = await boundCache(root: root)
        let h = contentHash("x")
        c.store(h, kind: .small, data: Data("x".utf8))
        c.store(contentHash("y"), kind: .big, data: Data("y".utf8))
        await c.drain()
        c.clear()
        #expect(c.lookup(h) == nil)
        #expect(await c.usage() == 0)
        #expect(names(under: root).isEmpty)
        #expect(c.store(h, kind: .small, data: Data("x2".utf8)))
        await c.drain()
        #expect(c.lookup(h)?.data == Data("x2".utf8))
        #expect(await c.usage() == 10)
    }

    @Test func anotherDeviceOrLogOutLeavesNothingBehind() async {
        let root = tempRoot()
        let defaults = freshDefaults()
        let c = await boundCache(root: root, scope: "one", defaults: defaults)
        let h = contentHash("p")
        c.store(h, kind: .small, data: Data("p".utf8))
        c.noteOmits(true)
        await c.drain()
        #expect(c.deviceOmits == true)
        // Another device: its own, empty cache; the first one's is deleted.
        c.use(scopeID: "two")
        await c.drain()
        #expect(c.lookup(h) == nil)
        #expect(c.deviceOmits == nil)
        #expect(!FileManager.default.fileExists(atPath: root.appendingPathComponent("v1/one").path))
        #expect(FileManager.default.fileExists(atPath: root.appendingPathComponent("v1/two").path))
        // An older format's directory goes too.
        try? FileManager.default.createDirectory(at: root.appendingPathComponent("v0/old"), withIntermediateDirectories: true)
        c.use(scopeID: "one")
        await c.drain()
        #expect(!FileManager.default.fileExists(atPath: root.appendingPathComponent("v0").path))
        // Log Out: nothing on disk, and nothing stored until a sign-in.
        c.forget()
        await c.drain()
        #expect(!FileManager.default.fileExists(atPath: root.path))
        #expect(!c.store(h, kind: .small, data: Data("p".utf8)))
        #expect(c.lookup(h) == nil)
        #expect(await c.usage() == nil)
        c.noteOmits(true)
        #expect(c.deviceOmits == nil)
    }

    @Test func whatTheDeviceDoesIsKeptPerScope() async {
        let root = tempRoot()
        let defaults = freshDefaults()
        let a = await boundCache(root: root, scope: "one", defaults: defaults)
        #expect(a.deviceOmits == nil)
        a.noteOmits(true)
        let b = await boundCache(root: root, scope: "one", defaults: defaults)
        #expect(b.deviceOmits == true)
        b.noteOmits(false)
        #expect(await boundCache(root: root, scope: "one", defaults: defaults).deviceOmits == false)
        #expect(await boundCache(root: root, scope: "two", defaults: defaults).deviceOmits == nil)
    }

    @Test func damagedOrStrayFilesAreDroppedNotShown() async {
        let root = tempRoot()
        let dir = root.appendingPathComponent("v1/scope1")
        let fm = FileManager.default
        func put(_ name: String, _ data: Data) {
            let shard = dir.appendingPathComponent(String(name.prefix(2)))
            try? fm.createDirectory(at: shard, withIntermediateDirectories: true)
            fm.createFile(atPath: shard.appendingPathComponent(name).path, contents: data)
        }
        let garbage = contentHash("garbage"), cut = contentHash("cut"), twice = contentHash("twice")
        put("\(garbage).s", Data(repeating: 0xff, count: 64))
        put("\(cut).s", framed(Data(repeating: 1, count: 100), length: 500))
        put("\(twice).b", framed(Data("big".utf8)))
        put("\(twice).s", framed(Data("small".utf8)))
        put("\(twice.prefix(2))-leftover.tmp", Data("x".utf8))
        let c = await boundCache(root: root)
        // A stray file goes; of two kinds for one hash, the small one stays.
        #expect(!names(under: dir).contains { $0.hasSuffix(".tmp") })
        #expect(c.lookup(twice) == .init(kind: .small, data: Data("small".utf8)))
        #expect(!names(under: dir).contains("\(twice).b"))
        // Not ours, or cut short: a miss, and the file goes.
        #expect(c.lookup(garbage) == nil)
        #expect(c.lookup(cut) == nil)
        await c.drain()
        #expect(names(under: dir) == ["\(twice).s"])
        #expect(await c.usage() == Int64(8 + 5))
    }

    @Test func aFileThatCantBeReadNowIsAMissNotAnEntryLost() async {
        // As a file protected while the phone is locked: there, unreadable.
        let root = tempRoot()
        let c = await boundCache(root: root)
        let h = contentHash("unreadable")
        let bytes = Data(repeating: 4, count: 1000)
        c.store(h, kind: .small, data: bytes)
        await c.drain()
        let file = root.appendingPathComponent("v1/scope1/\(h.prefix(2))/\(h).s")
        try? FileManager.default.setAttributes([.posixPermissions: 0o000], ofItemAtPath: file.path)
        #expect(c.lookup(h) == nil)
        await c.drain()
        #expect(FileManager.default.fileExists(atPath: file.path))
        #expect(c.kind(of: h) == .small)
        #expect(await c.usage() == 1008)
        // Readable again: there it is.
        try? FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: file.path)
        #expect(c.lookup(h) == .init(kind: .small, data: bytes))
    }

    @Test func whileProtectedDataIsUnavailableNothingIsReadOrWritten() async {
        let root = tempRoot()
        let c = await boundCache(root: root)
        let h = contentHash("locked")
        c.store(h, kind: .small, data: Data("kept".utf8))
        await c.drain()
        var announced = 0
        let watch = NotificationCenter.default.addObserver(forName: ThumbDiskCache.becameAvailable, object: c, queue: nil) { _ in
            announced += 1
        }
        defer { NotificationCenter.default.removeObserver(watch) }
        c.setDataAvailable(false)
        #expect(!c.dataAvailable)
        // A miss, kept; nothing new written.
        #expect(c.lookup(h) == nil)
        #expect(!c.store(contentHash("new"), kind: .small, data: Data("new".utf8)))
        await c.drain()
        #expect(names(under: root) == ["\(h).s"])
        #expect(c.kind(of: h) == .small)
        c.setDataAvailable(true)
        #expect(announced == 1)
        #expect(c.lookup(h)?.data == Data("kept".utf8))
    }

    @Test func clearRightAfterABindStillDeletesTheOtherScopes() async {
        let root = tempRoot()
        let fm = FileManager.default
        try? fm.createDirectory(at: root.appendingPathComponent("v1/old-device/ab"), withIntermediateDirectories: true)
        try? fm.createDirectory(at: root.appendingPathComponent("v0/older"), withIntermediateDirectories: true)
        let c = ThumbDiskCache(root: root, limit: 1_000_000, defaults: freshDefaults())
        c.use(scopeID: "new-device")
        c.clear()
        await c.drain()
        #expect(!fm.fileExists(atPath: root.appendingPathComponent("v1/old-device").path))
        #expect(!fm.fileExists(atPath: root.appendingPathComponent("v0").path))
        #expect(fm.fileExists(atPath: root.appendingPathComponent("v1/new-device").path))
        #expect(await c.usage() == 0)
    }

    @Test func filesAskAgainOnTheirOwnAndNeitherAsksForAStillBigOne() async {
        let clock = TestClock()
        let c = await boundCache(clock: clock)
        let h = contentHash("files"), stuck = contentHash("stuck")
        // Files' once a launch doesn't use up the pages' (nor the reverse).
        #expect(c.firstFilesReask(h))
        #expect(!c.firstFilesReask(h))
        #expect(c.firstReask(h))
        #expect(!c.firstReask(h))
        // Asked again and still big: neither asks for a week.
        c.noteStillBig(stuck)
        #expect(!c.firstReask(stuck))
        #expect(!c.firstFilesReask(stuck))
        clock.now += ThumbDiskCache.reaskAfter + 1
        #expect(c.firstReask(stuck))
        #expect(c.firstFilesReask(stuck))
    }

    @Test func onlyContentHashesNameFiles() async {
        let c = await boundCache()
        #expect(!c.store("../../etc/passwd", kind: .small, data: Data("x".utf8)))
        #expect(!c.store("h1", kind: .small, data: Data("x".utf8)))
        #expect(!c.store(contentHash("x").uppercased(), kind: .small, data: Data("x".utf8)))
        #expect(!c.store(contentHash("e"), kind: .small, data: Data()))
        #expect(c.lookup("h1") == nil)
        #expect(ThumbDiskCache.isHash(contentHash("ok")))
    }

    @Test func aKindFromWhatTheAnswerSays() {
        #expect(ThumbDiskCache.Kind(thumbnailSmall: true) == .small)
        #expect(ThumbDiskCache.Kind(thumbnailSmall: false) == .big)
        #expect(ThumbDiskCache.Kind(thumbnailSmall: nil) == .unknown)
        #expect(ThumbDiskCache.Kind(file("/p", hash: "h")) == .unknown)
        #expect(ThumbDiskCache.Kind(file("/p", hash: "h", small: false)) == .big)
    }
}

// MARK: - What a grid does with a page's entry, and with an answer

struct ThumbDecisionTests {
    @Test func aPageEntryIsShownKeptOrAskedFor() {
        // Content came (a device before release 113): shown and kept.
        #expect(ThumbPlan.of(hasContent: true, thumbnailSmall: nil, cached: nil) == .inline)
        #expect(ThumbPlan.of(hasContent: true, thumbnailSmall: true, cached: .small) == .inline)
        // Nothing kept: asked for.
        #expect(ThumbPlan.of(hasContent: false, thumbnailSmall: true, cached: nil) == .ask)
        #expect(ThumbPlan.of(hasContent: false, thumbnailSmall: false, cached: nil) == .ask)
        // A small one is all there is to have.
        #expect(ThumbPlan.of(hasContent: false, thumbnailSmall: true, cached: .small) == .cached)
        #expect(ThumbPlan.of(hasContent: false, thumbnailSmall: false, cached: .small) == .cached)
        // A big or unknown one: used while the device has no small one...
        #expect(ThumbPlan.of(hasContent: false, thumbnailSmall: false, cached: .big) == .cached)
        #expect(ThumbPlan.of(hasContent: false, thumbnailSmall: false, cached: .unknown) == .cached)
        #expect(ThumbPlan.of(hasContent: false, thumbnailSmall: nil, cached: .unknown) == .cached)
        // ...shown and asked for again once it has.
        #expect(ThumbPlan.of(hasContent: false, thumbnailSmall: true, cached: .big) == .cachedThenAsk)
        #expect(ThumbPlan.of(hasContent: false, thumbnailSmall: true, cached: .unknown) == .cachedThenAsk)
    }

    private func answer(_ paths: [String], askAgainFrom: Int32 = 0, small: Bool? = nil) -> Msg_ListOfFiles {
        var lof = Msg_ListOfFiles()
        lof.files = paths.map { file($0, hash: contentHash($0), content: Data("j".utf8), small: small) }
        lof.askAgainFrom = askAgainFrom
        return lof
    }

    @Test func aNewDeviceSaysWhereItStoppedAndWhatHasNone() {
        let asked = ["/0", "/1", "/2", "/3", "/4"]
        // Stopped at 3 (its byte cap): 3 and 4 again; 1 has none.
        let cut = ThumbAnswer.of(asked: asked, answer: answer(["/0", "/2"], askAgainFrom: 3, small: true), deviceIsNew: false)
        #expect(cut == ThumbAnswer(askAgain: ["/3", "/4"], none: ["/1"], unsure: []))
        // Looked at all of them: what is missing has none.
        let all = ThumbAnswer.of(asked: asked, answer: answer(["/0", "/2"], small: true), deviceIsNew: false)
        #expect(all == ThumbAnswer(askAgain: [], none: ["/1", "/3", "/4"], unsure: []))
        // Known to be new (it left a page's content out), with nothing to send.
        let empty = ThumbAnswer.of(asked: asked, answer: answer([]), deviceIsNew: true)
        #expect(empty == ThumbAnswer(askAgain: [], none: asked, unsure: []))
    }

    @Test func anOlderDevicesSilenceIsNeverNone() {
        let asked = ["/0", "/1", "/2", "/3", "/4"]
        // It doesn't say where it stopped: after the last one answered,
        // likely cut (asked again); before it, maybe none (not recorded).
        let r = ThumbAnswer.of(asked: asked, answer: answer(["/0", "/2"]), deviceIsNew: false)
        #expect(r == ThumbAnswer(askAgain: ["/3", "/4"], none: [], unsure: ["/1"]))
        // Nothing answered: nothing again (no progress to make), nothing none.
        let nothing = ThumbAnswer.of(asked: asked, answer: answer([]), deviceIsNew: false)
        #expect(nothing == ThumbAnswer(askAgain: [], none: [], unsure: asked))
    }

    @Test func pagesWithoutContentAreBigger() {
        #expect(PhotoPageLimit.first(deviceOmits: nil) == cFirstPhotoPageLimit)
        #expect(PhotoPageLimit.first(deviceOmits: false) == cFirstPhotoPageLimit)
        #expect(PhotoPageLimit.first(deviceOmits: true) == 60)
        #expect(PhotoPageLimit.next == 120)
        #expect(PhotoPageLimit.next <= 200)
    }
}

// MARK: - A grid's tiles: the cache first, then GetThumbnails

/// The device for GetThumbnails: answers with what `answer` makes of each
/// request (held while `holding`), or throws while `failures` lasts.
@MainActor
private final class ThumbDevice {
    var asked: [Msg_GetThumbnails] = []
    var failures = 0
    var holding = false
    private var held: [CheckedContinuation<Void, Never>] = []
    /// Every path asked for, small, under its own hash.
    var answer: (Msg_GetThumbnails) -> Msg_ListOfFiles = { g in
        var lof = Msg_ListOfFiles()
        lof.files = g.paths.map { file($0, hash: contentHash($0), content: picture(), small: true) }
        return lof
    }

    func release() {
        holding = false
        let h = held
        held = []
        h.forEach { $0.resume() }
    }

    func request(_ payload: Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope {
        guard case .reqGetThumbnails(let g) = payload else { throw CancellationError() }
        asked.append(g)
        if holding { await withCheckedContinuation { held.append($0) } }
        if failures > 0 {
            failures -= 1
            throw URLError(.timedOut)
        }
        var r = Msg_RespEnvelope()
        r.payload = .respListOfFiles(answer(g))
        return r
    }

    var paths: [[String]] { asked.map(\.paths) }
}

@MainActor
@Suite(.serialized)
struct GridThumbLoaderTests {
    private let run = UUID().uuidString.prefix(8)

    private func loader(_ cache: ThumbDiskCache, _ device: ThumbDevice) -> GridThumbLoader {
        let l = GridThumbLoader(maxPt: 40, cache: cache) { [weak device] payload in
            guard let device else { throw CancellationError() }
            return try await device.request(payload)
        }
        l.retry.delay = { _ in 0.05 }
        return l
    }

    private func id(_ path: String) -> String { "\(run)\(path)" }

    @Test func aPageAsksOnlyForWhatTheCacheLacks() async {
        let cache = await boundCache()
        let device = ThumbDevice()
        let l = loader(cache, device)
        let hs = ["a", "b", "c", "d", "e"].map { contentHash("\(run)\($0)") }
        cache.store(hs[0], kind: .small, data: picture())
        cache.store(hs[1], kind: .big, data: picture())
        cache.store(hs[2], kind: .unknown, data: picture())
        await cache.drain()
        // The device answers each path under its row's hash.
        let byPath = Dictionary(uniqueKeysWithValues: zip(["/a", "/b", "/c", "/d", "/e"], hs))
        device.answer = { g in
            var lof = Msg_ListOfFiles()
            lof.files = g.paths.map { file($0, hash: byPath[$0]!, content: picture(), small: true) }
            return lof
        }
        let rows = [
            file("/a", hash: hs[0], small: true),   // small kept: used
            file("/b", hash: hs[1], small: true),   // big kept, small there now: asked again
            file("/c", hash: hs[2], small: false),  // unknown kept, no small yet: used
            file("/d", hash: hs[3], small: true),   // nothing kept: asked
            file("/e", hash: hs[4], small: false),  // nothing kept: asked
        ]
        let keep = await l.takePage(rows, ids: rows.map { id($0.path) })
        #expect(keep == [nil, nil, nil, nil, nil])
        // The device left the content out: its next first pages are big.
        #expect(cache.deviceOmits == true)
        // Nothing is asked for until the tiles come near the screen.
        try? await Task.sleep(nanoseconds: 150_000_000)
        #expect(device.asked.isEmpty)
        l.readAhead(rows.map { .init(id: id($0.path), path: $0.path, hash: $0.hash) })
        await waitUntil { device.asked.count == 1 }
        #expect(device.paths == [["/b", "/d", "/e"]])
        #expect(device.asked.allSatisfy { $0.smallThumbnails })
        // The tiles kept were decoded before the page showed.
        #expect(GridThumbCache.cached(id: id("/a"), maxPt: 40) != nil)
        #expect(GridThumbCache.cached(id: id("/c"), maxPt: 40) != nil)
        await waitUntil { GridThumbCache.cached(id: id("/d"), maxPt: 40) != nil }
        // The answers are kept, small.
        await cache.drain()
        #expect(cache.kind(of: hs[1]) == .small)
        #expect(cache.kind(of: hs[3]) == .small)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.asked.count == 1)
    }

    @Test func aBigOneAnsweredAgainIsKeptAndNotAskedForAgainForDays() async {
        // The device says a row's small one is there (true), then sends
        // the big one (false: its small one couldn't be read). That big
        // one is kept and not asked for again - not this launch, not the
        // next - until a week has gone by.
        let root = tempRoot()
        let defaults = freshDefaults()
        let clock = TestClock()
        let cache = await boundCache(root: root, clock: clock, defaults: defaults)
        let device = ThumbDevice()
        device.answer = { g in
            var lof = Msg_ListOfFiles()
            lof.files = g.paths.map { file($0, hash: contentHash("\(self.run)big"), content: picture(), small: false) }
            return lof
        }
        let h = contentHash("\(run)big")
        cache.store(h, kind: .unknown, data: picture())
        await cache.drain()
        let row = file("/big", hash: h, small: true)
        let l = loader(cache, device)
        _ = await l.takePage([row], ids: [id("/big")])
        l.appeared("/big")
        await waitUntil { device.asked.count == 1 }
        await cache.drain()
        try? await Task.sleep(nanoseconds: 100_000_000)
        await cache.drain()
        #expect(cache.kind(of: h) == .big)
        // The same row again - a new search, another grid: not asked again.
        l.reset()
        _ = await l.takePage([row], ids: [id("/big")])
        l.appeared("/big")
        let other = loader(cache, device)
        _ = await other.takePage([row], ids: [id("/big2")])
        other.appeared("/big")
        try? await Task.sleep(nanoseconds: 200_000_000)
        #expect(device.asked.count == 1)
        // Nor after a relaunch...
        await cache.drain()
        let relaunched = await boundCache(root: root, clock: clock, defaults: defaults)
        let again = loader(relaunched, device)
        _ = await again.takePage([row], ids: [id("/big3")])
        again.appeared("/big")
        try? await Task.sleep(nanoseconds: 200_000_000)
        #expect(device.asked.count == 1)
        // ...until a week has gone by.
        clock.now += ThumbDiskCache.reaskAfter + 1
        let later = await boundCache(root: root, clock: clock, defaults: defaults)
        let laterGrid = loader(later, device)
        _ = await laterGrid.takePage([row], ids: [id("/big4")])
        laterGrid.appeared("/big")
        await waitUntil { device.asked.count == 2 }
        #expect(device.paths == [["/big"], ["/big"]])
    }

    @Test func answersAreKeptUnderTheirOwnHashAndSayWhatIsLeft() async {
        let cache = await boundCache()
        let device = ThumbDevice()
        let paths = (0..<5).map { "/p\($0)" }
        let moved = contentHash("\(run)moved")
        device.answer = { g in
            var lof = Msg_ListOfFiles()
            if g.paths.first == "/p0" {
                // p0's file changed since its page (another hash); p1 has
                // none; it stopped at p3 (its byte cap).
                lof.files = [file("/p0", hash: moved, content: picture(), small: true),
                             file("/p2", hash: contentHash("\(self.run)/p2"), content: picture(), small: true)]
                lof.askAgainFrom = 3
            } else {
                lof.files = g.paths.map { file($0, hash: contentHash("\(self.run)\($0)"), content: picture(), small: true) }
            }
            return lof
        }
        let l = loader(cache, device)
        let rows = paths.map { file($0, hash: contentHash("\(run)row\($0)"), small: true) }
        _ = await l.takePage(rows, ids: paths.map(id))
        l.readAhead(rows.map { .init(id: id($0.path), path: $0.path, hash: $0.hash) })
        await waitUntil { device.asked.count == 2 }
        #expect(device.paths == [paths, ["/p3", "/p4"]])
        await waitUntil { GridThumbCache.cached(id: id("/p4"), maxPt: 40) != nil }
        await cache.drain()
        // Under the answer's hash, not the page's.
        #expect(cache.kind(of: moved) == .small)
        #expect(cache.kind(of: contentHash("\(run)row/p0")) == nil)
        #expect(GridThumbCache.cached(id: id("/p0"), maxPt: 40) != nil)
        // p1 has none: its tile showing again asks nothing.
        l.need(.init(id: id("/p1"), path: "/p1", hash: contentHash("\(run)row/p1")))
        try? await Task.sleep(nanoseconds: 200_000_000)
        #expect(device.asked.count == 2)
        #expect(GridThumbCache.cached(id: id("/p1"), maxPt: 40) == nil)
    }

    @Test func anOlderDeviceIsAskedAgainWhenTheTileShowsAgain() async {
        // A device before release 113: answers without thumbnail_small or
        // ask_again_from, and the cache doesn't know it as new.
        let cache = await boundCache()
        let device = ThumbDevice()
        device.answer = { g in
            var lof = Msg_ListOfFiles()
            lof.files = g.paths.filter { $0 == "/q1" }.map { file($0, hash: contentHash("\(self.run)\($0)"), content: picture()) }
            return lof
        }
        let l = loader(cache, device)
        let wants = ["/q0", "/q1", "/q2"].map { GridThumbLoader.Want(id: id($0), path: $0, hash: contentHash("\(run)w\($0)")) }
        device.holding = true
        l.need(wants[0])
        await waitUntil { device.asked.count == 1 }
        // q1 and q2 wait for the first ask (one at a time), in this order.
        l.readAhead([wants[1], wants[2]])
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.asked.count == 1)
        device.release()
        // q0 not answered, alone: maybe none. Then q2, after q1 (the one
        // answered): likely cut, asked again at once - and then not
        // answered either.
        await waitUntil { device.asked.count == 3 }
        try? await Task.sleep(nanoseconds: 100_000_000)
        guard device.paths.count == 3 else {
            Issue.record("asked \(device.paths)")
            return
        }
        #expect(device.paths[0] == ["/q0"])
        #expect(device.paths[1] == ["/q1", "/q2"])
        #expect(device.paths[2] == ["/q2"])
        await cache.drain()
        // Kept as it came: unknown.
        #expect(cache.kind(of: contentHash("\(run)/q1")) == .unknown)
        // Never recorded as having none: its tile showing again asks again.
        l.need(wants[0])
        await waitUntil { device.asked.count == 4 }
        #expect(device.paths.last == ["/q0"])
    }

    @Test func aFailedAskIsAskedAgainAfterItsWait() async {
        let cache = await boundCache()
        let device = ThumbDevice()
        device.failures = 2
        let l = loader(cache, device)
        let w = GridThumbLoader.Want(id: id("/r"), path: "/r", hash: contentHash("\(run)/r"))
        l.need(w)
        await waitUntil { GridThumbCache.cached(id: id("/r"), maxPt: 40) != nil }
        #expect(device.paths == [["/r"], ["/r"], ["/r"]])
        #expect(l.retry.failures == 0)
    }

    @Test func oneAskAtATimeTheTilesOnScreenAloneFirst() async {
        let cache = await boundCache()
        let device = ThumbDevice()
        device.holding = true
        let l = loader(cache, device)
        let rows = (0..<30).map { file("/t\($0)", hash: contentHash("\(run)t\($0)"), small: true) }
        let wants = rows.map { GridThumbLoader.Want(id: id($0.path), path: $0.path, hash: $0.hash) }
        _ = await l.takePage(rows, ids: wants.map(\.id))
        // The first screen's tiles show: they alone are the first ask.
        for i in 0..<6 { l.appeared("/t\(i)") }
        await waitUntil { device.asked.count == 1 }
        #expect(device.paths[0].sorted() == (0..<6).map { "/t\($0)" }.sorted())
        // While it is on its way: the next two screens come within reach,
        // and one more tile shows. Nothing more goes yet.
        l.readAhead(wants)
        l.appeared("/t28")
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.asked.count == 1)
        device.release()
        await waitUntil { device.asked.count == 3 }
        // The tile on screen alone, then the rest in the order they came.
        #expect(device.paths[1] == ["/t28"])
        #expect(device.paths[2] == (6..<28).map { "/t\($0)" } + ["/t29"])
    }

    @Test func nothingIsReadOrAskedForWhileThePhoneIsLocked() async {
        let cache = await boundCache()
        let device = ThumbDevice()
        let l = loader(cache, device)
        var images = 0
        l.onImages = { images += 1 }
        let kept = contentHash("\(run)locked-kept")
        cache.store(kept, kind: .small, data: picture())
        await cache.drain()
        cache.setDataAvailable(false)
        let w = GridThumbLoader.Want(id: id("/locked"), path: "/locked", hash: kept)
        let missing = GridThumbLoader.Want(id: id("/locked-missing"), path: "/locked-missing", hash: contentHash("\(run)locked-missing"))
        l.need(w)
        l.need(missing)
        try? await Task.sleep(nanoseconds: 200_000_000)
        // Not read, not taken for a miss, not asked for.
        #expect(images == 0)
        #expect(device.asked.isEmpty)
        #expect(cache.kind(of: kept) == .small)
        // Unlocked: read from the cache, and only the one it lacks asked for.
        cache.setDataAvailable(true)
        await waitUntil { images >= 1 && device.asked.count == 1 }
        #expect(GridThumbCache.cached(id: id("/locked"), maxPt: 40) != nil)
        #expect(device.paths == [["/locked-missing"]])
    }

    @Test func contentThatCameIsKeptWithItsKind() async {
        let cache = await boundCache()
        let device = ThumbDevice()
        let l = loader(cache, device)
        let small = contentHash("\(run)inline-small"), unknown = contentHash("\(run)inline-unknown")
        let rows = [file("/s", hash: small, content: picture(), small: true),
                    file("/u", hash: unknown, content: picture())]
        let keep = await l.takePage(rows, ids: rows.map { id($0.path) })
        // The cache has them: the items needn't.
        #expect(keep == [nil, nil])
        #expect(cache.deviceOmits == false)
        await cache.drain()
        #expect(cache.kind(of: small) == .small)
        #expect(cache.kind(of: unknown) == .unknown)
        #expect(GridThumbCache.cached(id: id("/s"), maxPt: 40) != nil)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.asked.isEmpty)

        // No device signed in to: nothing keeps them but the items.
        let unbound = ThumbDiskCache(root: tempRoot(), limit: 1_000_000, defaults: freshDefaults())
        let bytes = picture()
        let kept = await loader(unbound, device).takePage([file("/n", hash: contentHash("\(run)n"), content: bytes)], ids: [id("/n")])
        #expect(kept == [bytes])
    }

    @Test func aTileWhoseImageWasLetGoComesBackFromTheCache() async {
        let cache = await boundCache()
        let device = ThumbDevice()
        let l = loader(cache, device)
        var images = 0
        l.onImages = { images += 1 }
        let h = contentHash("\(run)back")
        cache.store(h, kind: .small, data: picture())
        await cache.drain()
        l.need(.init(id: id("/back"), path: "/back", hash: h))
        await waitUntil { images == 1 }
        #expect(GridThumbCache.cached(id: id("/back"), maxPt: 40) != nil)
        #expect(device.asked.isEmpty)
    }
}

// MARK: - Images asks for pages without thumbnails

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct ImagesThumbCacheTests {
    @Test func imagesAsksForBiggerPagesWithoutContentAndFetchesTheTiles() async {
        let cache = await boundCache()
        let run = UUID().uuidString.prefix(8)
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: cache)
        var searches: [Msg_SearchPhotos] = []
        var tiles: [(Msg_GetThumbnails, PhotoGalleryVM.AskKind?)] = []
        var next = 0
        vm.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: false, statusCode: nil) }
        vm.photoRequest = { payload, kind in
            var r = Msg_RespEnvelope()
            switch payload {
            case .reqSearchPhotos(let sp):
                searches.append(sp)
                var lof = Msg_ListOfFiles()
                let n = sp.token.isEmpty ? 12 : 5
                // Entries without content, as a release 113 device sends them.
                lof.files = (next..<(next + n)).map { i in
                    var f = file("/img/\(run)/\(i).jpg", hash: contentHash("\(run)img\(i)"), small: true)
                    f.created = Google_Protobuf_Timestamp(date: Date(timeIntervalSince1970: 1_780_000_000 - Double(i) * 3600))
                    return f
                }
                next += n
                lof.token = searches.count < 2 ? "t1" : ""
                r.payload = .respListOfFiles(lof)
            case .reqGetThumbnails(let g):
                tiles.append((g, kind))
                var lof = Msg_ListOfFiles()
                lof.files = g.paths.map { p in
                    let i = Int(p.split(separator: "/").last!.split(separator: ".").first!)!
                    return file(p, hash: contentHash("\(run)img\(i)"), content: picture(), small: true)
                }
                r.payload = .respListOfFiles(lof)
            case .reqPhotoDateBuckets:
                r.payload = .respPhotoDateBuckets(Msg_RespPhotoDateBuckets())
            default:
                throw CancellationError()
            }
            return r
        }
        vm.libraryRequest = { _, _ in throw CancellationError() }
        vm.peopleRequest = { _, _ in throw CancellationError() }
        vm.onAppearInitial()
        await waitUntil { vm.items.count == 12 }
        // Small thumbnails, left out; a first page as small as ever while
        // the device isn't known to leave them out.
        #expect(searches[0].smallThumbnails && searches[0].omitThumbnails)
        #expect(searches[0].limit == cFirstPhotoPageLimit)
        #expect(vm.items.allSatisfy { $0.thumbData == nil && !$0.hash.isEmpty })
        // Nothing asked for until tiles show; then those, as their own kind.
        try? await Task.sleep(nanoseconds: 150_000_000)
        #expect(tiles.isEmpty)
        for i in vm.items.indices { vm.tileShown(i) }
        await waitUntil { tiles.count == 1 }
        #expect(tiles[0].0.smallThumbnails)
        #expect(tiles[0].0.paths.sorted() == vm.items.map(\.path).sorted())
        #expect(tiles[0].1 == .tiles)
        await waitUntil { vm.items.allSatisfy { GridThumbCache.cached(id: $0.id, maxPt: PhotoGridMetrics.decodeSide) != nil } }
        // Scrolling on: a bigger page, `have` counting every photo held.
        await vm.loadMoreIfNeeded(index: 11, id: vm.items[11].id)
        await waitUntil { vm.items.count == 17 }
        #expect(searches[1].token == "t1")
        #expect(searches[1].have == 12)
        #expect(searches[1].limit == PhotoPageLimit.next)
        #expect(searches[1].omitThumbnails)
        for i in 12..<17 { vm.tileShown(i) }
        await waitUntil { tiles.count == 2 }
        #expect(tiles[1].0.paths.sorted() == vm.items.suffix(5).map(\.path).sorted())
        await cache.drain()
        // A new search: the device is known to leave them out now.
        next = 0
        vm.togglePerson("someone")
        await waitUntil { searches.count == 3 }
        #expect(searches[2].token.isEmpty)
        #expect(searches[2].limit == PhotoPageLimit.firstWithoutThumbnails)
        await waitUntil { vm.items.count == 12 }
        // Its photos' tiles are in the cache: nothing more is asked for.
        for i in vm.items.indices { vm.tileShown(i) }
        try? await Task.sleep(nanoseconds: 200_000_000)
        #expect(tiles.count == 2)
        await cache.drain()
        #expect(cache.kind(of: contentHash("\(run)img0")) == .small)
    }
}
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct ThumbCacheDeleteTests {
    @Test func deletedPhotosLeaveTheCache() async {
        let cache = await boundCache()
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: cache)
        vm.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: false, statusCode: nil) }
        var deleted: [String] = []
        vm.photoRequest = { payload, _ in
            var r = Msg_RespEnvelope()
            switch payload {
            case .reqDelFile(let d):
                deleted.append(d.path)
                var a = Msg_Ack()
                a.ok = true
                r.payload = .respAck(a)
            case .reqPhotoDateBuckets:
                r.payload = .respPhotoDateBuckets(Msg_RespPhotoDateBuckets())
            default:
                throw CancellationError()
            }
            return r
        }
        let gone = contentHash("deleted-photo"), kept = contentHash("kept-photo")
        cache.store(gone, kind: .small, data: Data("gone".utf8))
        cache.store(kept, kind: .small, data: Data("kept".utf8))
        await cache.drain()
        vm.items = [
            PhotoGalleryVM.Item(id: "a", path: "/p/a.jpg", mime: "image/jpeg", size: 1, thumbData: nil,
                                localURL: nil, isLocalOnly: false, hash: gone),
            PhotoGalleryVM.Item(id: "b", path: "/p/b.jpg", mime: "image/jpeg", size: 1, thumbData: nil,
                                localURL: nil, isLocalOnly: false, hash: kept),
        ]
        vm.selected = ["/p/a.jpg"]
        vm.deleteSelected()
        await waitUntil { vm.items.count == 1 }
        await cache.drain()
        #expect(deleted == ["/p/a.jpg"])
        #expect(cache.kind(of: gone) == nil)
        #expect(cache.kind(of: kept) == .small)
    }
}
}

// MARK: - Settings' Thumbnail cache

@MainActor
struct ThumbCacheSettingsTests {
    @Test func theSizesAndTheirWords() {
        #expect(ThumbCacheLimit.choices == [250_000_000, 500_000_000, 1_000_000_000, 2_000_000_000, 5_000_000_000])
        #expect(ThumbCacheLimit.defaultBytes == 1_000_000_000)
        #expect(ThumbCacheLimit.choices.map(ThumbCacheLimit.text) == ["250 MB", "500 MB", "1 GB", "2 GB", "5 GB"])
        // (The decimal separator is the phone's.)
        #expect(ThumbCacheLimit.usage(123_400_000, of: 1_000_000_000)
                == "Using \(ThumbCacheLimit.text(123_400_000)) of 1 GB")
        #expect(["123.4 MB", "123,4 MB"].contains(ThumbCacheLimit.text(123_400_000)))
        let d = freshDefaults()
        #expect(ThumbCacheLimit.stored(d) == 1_000_000_000)
        d.set(NSNumber(value: Int64(42)), forKey: ThumbCacheLimit.key)
        #expect(ThumbCacheLimit.stored(d) == 1_000_000_000)
        ThumbCacheLimit.save(2_000_000_000, d)
        #expect(ThumbCacheLimit.stored(d) == 2_000_000_000)
    }

    @Test func choosingASizeKeepsItAndClearEmptiesTheCache() async {
        let clock = TestClock()
        let cache = await boundCache(clock: clock)
        let defaults = freshDefaults()
        var decodedCleared = false
        let model = ThumbCacheSettings(cache: cache, defaults: defaults, clearDecoded: { decodedCleared = true })
        #expect(model.limit == 1_000_000_000)
        #expect(model.usageText == nil)
        await model.refresh()
        #expect(model.used == 0)
        for i in 0..<3 {
            clock.now = 1000 + Double(i)
            cache.store(contentHash("s\(i)"), kind: .small, data: Data(repeating: 3, count: 1000))
        }
        await model.refresh()
        let three: Int64 = 3 * 1008
        #expect(model.used == three)
        #expect(model.usageText == ThumbCacheLimit.usage(three, of: 1_000_000_000))
        // Not a choice: nothing changes.
        await model.choose(123)
        #expect(model.limit == 1_000_000_000)
        await model.choose(250_000_000)
        #expect(model.limit == 250_000_000)
        #expect(ThumbCacheLimit.stored(defaults) == 250_000_000)
        #expect(cache.limitBytes == 250_000_000)
        await model.clearAll()
        #expect(decodedCleared)
        #expect(model.used == 0)
        #expect(cache.lookup(contentHash("s0")) == nil)
        #expect(!model.clearing)
    }
}
