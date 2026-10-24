// SPDX-License-Identifier: AGPL-3.0-or-later

import CryptoKit
import Foundation
import Testing
import UIKit
@testable import OffTheCloud

/// A content hash as the device writes them: 64 lowercase hex digits.
private func contentHash(_ s: String) -> String {
    SHA256.hash(data: Data(s.utf8)).map { String(format: "%02x", $0) }.joined()
}

private func tempRoot() -> URL {
    FileManager.default.temporaryDirectory.appendingPathComponent("fullphotos-\(UUID().uuidString)", isDirectory: true)
}

/// A cache in a directory of its own, its launch's emptying done: a test
/// never touches the app's.
private func freshCache(root: URL = tempRoot(), limit: Int64 = 4_000) async -> FullPhotoCache {
    let c = FullPhotoCache(root: root, limit: limit)
    await c.drain()
    return c
}

/// Waits (`seconds` at most) for `done`.
@MainActor
private func waitUntil(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

/// A photo as the device sends one.
private func jpeg() -> Data {
    let fmt = UIGraphicsImageRendererFormat()
    fmt.scale = 1
    return UIGraphicsImageRenderer(size: CGSize(width: 40, height: 30), format: fmt).image { ctx in
        UIColor.orange.setFill()
        ctx.fill(CGRect(x: 0, y: 0, width: 40, height: 30))
    }.jpegData(compressionQuality: 0.8)!
}

/// The files in `dir`, by name.
private func names(in dir: URL) -> [String] {
    ((try? FileManager.default.contentsOfDirectory(atPath: dir.path)) ?? []).sorted()
}

/// The viewer's full-size photos kept for the session: the budget, the
/// order they go in, and what empties them.
struct FullPhotoCacheTests {
    @Test func whatIsStoredIsReadBackAsItCame() async {
        let root = tempRoot()
        let c = await freshCache(root: root)
        let h = contentHash("a")
        let bytes = Data(repeating: 9, count: 700)
        #expect(c.store(h, data: bytes))
        #expect(await c.lookup(h) == bytes)
        #expect(names(in: root) == [h])
        #expect(await c.usage() == 700)
        // The same content again counts once.
        c.store(h, data: bytes)
        #expect(await c.usage() == 700)
    }

    @Test func theLeastRecentlyUsedGoFirstUntilWithinTheBudget() async {
        let c = await freshCache(limit: 4_000)
        let h = (0..<9).map { contentHash("\($0)") }
        for hash in h.prefix(8) { c.store(hash, data: Data(repeating: 7, count: 500)) }
        #expect(await c.usage() == 4_000)
        // Over 4000 by 1000: the two oldest go, and only they.
        c.store(h[8], data: Data(repeating: 7, count: 1_000))
        #expect(await c.usage() == 4_000)
        #expect(await c.lookup(h[0]) == nil)
        #expect(await c.lookup(h[1]) == nil)
        for hash in h.dropFirst(2) { #expect(await c.lookup(hash) != nil) }
    }

    @Test func aHitIsTheMostRecentlyUsed() async {
        let c = await freshCache(limit: 4_000)
        let entry = Data(repeating: 3, count: 1_000)
        let h = ["a", "b", "c", "d", "e", "f"].map(contentHash)
        for hash in h.prefix(4) { c.store(hash, data: entry) }
        // "a", the oldest, is read: "b" is the oldest now, and goes.
        #expect(await c.lookup(h[0]) != nil)
        c.store(h[4], data: entry)
        #expect(await c.usage() == 4_000)
        #expect(await c.lookup(h[1]) == nil)
        // Storing what is kept already is a use too: "c" is the newest,
        // "d" the oldest.
        c.store(h[2], data: entry)
        c.store(h[5], data: entry)
        #expect(await c.usage() == 4_000)
        #expect(await c.lookup(h[3]) == nil)
        for hash in [h[0], h[2], h[4], h[5]] { #expect(await c.lookup(hash) != nil) }
    }

    @Test func aPhotoOverAQuarterOfTheBudgetIsNotKept() async {
        let root = tempRoot()
        let c = await freshCache(root: root, limit: 4_000)
        #expect(c.maxEntryBytes == 1_000)
        #expect(!c.store(contentHash("big"), data: Data(repeating: 1, count: 1_001)))
        #expect(c.store(contentHash("fits"), data: Data(repeating: 1, count: 1_000)))
        await c.drain()
        #expect(names(in: root) == [contentHash("fits")])
        // Nothing but content hashes, and nothing empty.
        #expect(!c.store("not-a-hash", data: Data([1])))
        #expect(!c.store(contentHash("empty"), data: Data()))
        #expect(await c.lookup("not-a-hash") == nil)
        #expect(await c.lookup("") == nil)
        // A smaller budget evicts at once, and lowers the most one may take.
        c.store(contentHash("two"), data: Data(repeating: 2, count: 1_000))
        await c.drain()
        c.limitBytes = 1_200
        #expect(await c.usage() == 1_000)
        #expect(await c.lookup(contentHash("fits")) == nil)
        #expect(await c.lookup(contentHash("two")) != nil)
        #expect(!c.store(contentHash("three"), data: Data(repeating: 3, count: 301)))
    }

    @Test func clearEmptiesItAndItFillsAgain() async {
        let root = tempRoot()
        let c = await freshCache(root: root)
        let h = contentHash("x")
        c.store(h, data: Data("x".utf8))
        c.store(contentHash("y"), data: Data("y".utf8))
        await c.drain()
        c.clear()
        #expect(await c.lookup(h) == nil)
        #expect(await c.usage() == 0)
        #expect(names(in: root).isEmpty)
        #expect(c.store(h, data: Data("x2".utf8)))
        #expect(await c.lookup(h) == Data("x2".utf8))
        #expect(await c.usage() == 2)
    }

    @Test func aLaunchEmptiesWhatTheLastOneKept() async {
        let root = tempRoot()
        let first = await freshCache(root: root)
        let h = contentHash("kept")
        first.store(h, data: Data("kept".utf8))
        await first.drain()
        try? Data("stray".utf8).write(to: root.appendingPathComponent("stray"))
        #expect(names(in: root).count == 2)
        // The next launch's cache, on the same directory: nothing is left.
        let next = await freshCache(root: root)
        #expect(names(in: root).isEmpty)
        #expect(await next.lookup(h) == nil)
        #expect(await next.usage() == 0)
        // And a cache made over no directory at all makes one.
        let none = tempRoot()
        let made = await freshCache(root: none)
        #expect(made.store(h, data: Data("kept".utf8)))
        #expect(await made.lookup(h) == Data("kept".utf8))
    }

    @Test func aFileGoneOrDamagedIsAMissAndLeaves() async {
        let root = tempRoot()
        let c = await freshCache(root: root)
        let gone = contentHash("gone"), damaged = contentHash("damaged")
        c.store(gone, data: Data("gone".utf8))
        c.store(damaged, data: Data("damaged".utf8))
        await c.drain()
        // The system purged one; another was cut short.
        try? FileManager.default.removeItem(at: root.appendingPathComponent(gone))
        try? Data("dam".utf8).write(to: root.appendingPathComponent(damaged))
        #expect(await c.lookup(gone) == nil)
        #expect(await c.lookup(damaged) == nil)
        #expect(await c.usage() == 0)
        #expect(names(in: root).isEmpty)
        // A photo whose bytes don't decode is discarded by the viewer.
        let bad = contentHash("bad")
        c.store(bad, data: Data("bad".utf8))
        c.discard(bad)
        #expect(await c.lookup(bad) == nil)
        #expect(await c.usage() == 0)
        // Purged with its directory: written again where it was.
        try? FileManager.default.removeItem(at: root)
        #expect(c.store(gone, data: Data("again".utf8)))
        #expect(await c.lookup(gone) == Data("again".utf8))
    }

    @Test func aTenthOfTheSizeChosenTheThumbnailsTheRest() {
        for total in ThumbCacheLimit.choices {
            #expect(ThumbCacheLimit.fullPhotoBytes(of: total) == total / 10)
            #expect(ThumbCacheLimit.thumbnailBytes(of: total) + ThumbCacheLimit.fullPhotoBytes(of: total) == total)
        }
        #expect(ThumbCacheLimit.fullPhotoBytes(of: 1_000_000_000) == 100_000_000)
        #expect(ThumbCacheLimit.thumbnailBytes(of: 1_000_000_000) == 900_000_000)
        #expect(ThumbCacheLimit.fullPhotoBytes(of: 250_000_000) == 25_000_000)
    }
}

// MARK: - The viewer

/// The viewer's full-size photos: the open one alone first, then the next
/// and the previous once it is on screen and the viewer stays on it, and
/// nothing asked of the device for what this session kept.
@MainActor
@Suite(.serialized)
struct ViewerReadAheadTests {
    private struct Unreachable: Error {}

    /// The device's GetFiles, as they start and end (50 ms each).
    private final class Device {
        var events: [String] = []
        var fail = Set<String>()
        var asked: [String] { events.filter { $0.hasPrefix("start ") }.map { String($0.dropFirst(6)) } }
    }

    /// A viewer over `n` photos, with a device and a cache of its own.
    private func viewer(_ n: Int) -> (PhotoGalleryVM, Device, [PhotoGalleryVM.Item], FullPhotoCache) {
        let vm = PhotoGalleryVM()
        let device = Device()
        let cache = FullPhotoCache(root: tempRoot(), limit: 100_000_000)
        vm.fullPhotos = cache
        let run = UUID().uuidString
        let items = (0..<n).map { i in
            PhotoGalleryVM.Item(id: "/p/\(i).jpg", path: "/p/\(i).jpg", mime: "image/jpeg", size: 1,
                                thumbData: nil, localURL: nil, isLocalOnly: false, hash: contentHash("\(run)\(i)"))
        }
        let picture = jpeg()
        vm.photoRequest = { payload, _ in
            var resp = Msg_RespEnvelope()
            guard case .reqGetFile(let g) = payload else {
                // A failed photo's big thumbnail: none.
                resp.payload = .respListOfFiles(Msg_ListOfFiles())
                return resp
            }
            device.events.append("start \(g.path)")
            try? await Task.sleep(for: .milliseconds(50))
            device.events.append("end \(g.path)")
            if device.fail.contains(g.path) { throw Unreachable() }
            var f = Msg_File()
            f.path = g.path
            f.hash = items.first { $0.path == g.path }?.hash ?? ""
            f.content = picture
            resp.payload = .respFile(f)
            return resp
        }
        return (vm, device, items, cache)
    }

    @Test func theOpenPhotoAloneFirstThenTheNextThenThePrevious() async {
        let (vm, device, items, cache) = viewer(5)
        vm.showFiles(items, startAt: 2)
        await waitUntil { device.events.count == 6 }
        #expect(device.events == ["start /p/2.jpg", "end /p/2.jpg", "start /p/3.jpg", "end /p/3.jpg",
                                  "start /p/1.jpg", "end /p/1.jpg"])
        await waitUntil { vm.hiResImages.count == 3 }
        #expect(Set(vm.hiResImages.keys) == ["/p/1.jpg", "/p/2.jpg", "/p/3.jpg"])
        // Each kept for the session, under its hash.
        await cache.drain()
        for i in 1...3 { #expect(await cache.lookup(items[i].hash) == jpeg()) }
    }

    @Test func whatThisSessionKeptAsksTheDeviceNothing() async {
        let (vm, device, items, cache) = viewer(5)
        cache.store(items[2].hash, data: jpeg())
        cache.store(items[3].hash, data: jpeg())
        vm.showFiles(items, startAt: 2)
        await waitUntil { vm.hiResImages.count == 3 }
        // Only the previous one, which wasn't kept.
        #expect(device.asked == ["/p/1.jpg"])
        // Closed and opened again: all three from the phone.
        vm.closeModal()
        #expect(vm.hiResImages.isEmpty)
        vm.showFiles(items, startAt: 2)
        await waitUntil { vm.hiResImages.count == 3 }
        #expect(device.asked == ["/p/1.jpg"])
        // Save and Share still have the full bytes.
        #expect(await vm.fullImage(of: "/p/2.jpg") != nil)
    }

    @Test func aPhotoSteppedPastReadsNothingAround() async {
        let (vm, device, items, _) = viewer(7)
        vm.showFiles(items, startAt: 0)
        await waitUntil { vm.hiResImages["/p/0.jpg"] != nil }
        // On screen, and stepped past within rapidStep: nothing around it.
        vm.open(index: 2)
        await waitUntil { vm.hiResImages["/p/2.jpg"] != nil }
        vm.open(index: 4)
        // The viewer stays on the fifth: its next, then its previous.
        await waitUntil { device.asked.count == 5 }
        try? await Task.sleep(for: .milliseconds(400))
        #expect(device.asked == ["/p/0.jpg", "/p/2.jpg", "/p/4.jpg", "/p/5.jpg", "/p/3.jpg"])
    }

    @Test func aPhotoThatFailedReadsNothingAhead() async {
        let (vm, device, items, _) = viewer(3)
        device.fail = ["/p/1.jpg"]
        vm.showFiles(items, startAt: 1)
        await waitUntil { vm.hiResFailed.contains("/p/1.jpg") }
        try? await Task.sleep(for: .milliseconds(400))
        #expect(device.asked == ["/p/1.jpg"])
    }

    @Test func aNeighbourOnItsWayIsWaitedForNotAskedAgain() async {
        let (vm, device, items, _) = viewer(5)
        vm.showFiles(items, startAt: 1)
        // Stepped onto the next one while it is read ahead.
        await waitUntil { device.asked.contains("/p/2.jpg") }
        vm.open(index: 2)
        // Its own next once it came (the previous one is here already),
        // and nothing more around the one it left.
        await waitUntil { device.asked.count == 3 }
        try? await Task.sleep(for: .milliseconds(400))
        #expect(device.asked == ["/p/1.jpg", "/p/2.jpg", "/p/3.jpg"])
    }
}
