// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
import UIKit
@testable import OffTheCloud

/// A repeatable sequence for the picks (SplitMix64).
struct SeededRandom: RandomNumberGenerator {
    var state: UInt64
    mutating func next() -> UInt64 {
        state &+= 0x9E37_79B9_7F4A_7C15
        var z = state
        z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
        z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
        return z ^ (z >> 31)
    }
}

private func file(_ path: String, _ hash: String, mime: String = "image/jpeg") -> Msg_File {
    var f = Msg_File()
    f.path = path
    f.hash = hash
    f.mime = mime
    return f
}

private func hashes(_ n: Int, from: Int = 0) -> [String] { (from..<(from + n)).map { "h\($0)" } }

/// The texts of the notice and the screen, as written.
struct WallpaperTextTests {
    @Test func theNoticeReadsAsWritten() {
        #expect(S.appPhotosWallpaperStoredTitle == "These photos will be stored on this phone")
        #expect(S.appPhotosWallpaperStoredText(count: 3, name: "Trip")
                == "To show them as your wallpaper, copies of 3 photos from “\u{2068}Trip\u{2069}” are saved on this phone, sized for its screen. They stay there until you stop using the collection as wallpaper.")
        #expect(S.appPhotosWallpaperMenu == "Use as wallpaper…")
        #expect(S.appPhotosWallpaperMenuOn == "Wallpaper settings…")
        #expect(S.iosWallpaperLockButton == "Store copies in an album")
        #expect(S.iosWallpaperWidgetButton == "Store copies for the widget")
        #expect(S.iosWidgetChoose == "Touch and hold to choose a collection.")
    }

    /// The album's title in Photos has no isolation marks.
    @Test func theAlbumIsNamedAfterTheCollection() {
        #expect(WallpaperRules.albumTitle("Trip") == "Off The Cloud · Trip")
        #expect(!WallpaperRules.albumTitle("Ana").unicodeScalars.contains { $0 == "\u{2068}" || $0 == "\u{2069}" })
    }
}

/// What is copied: photos only, each once, at most the cap, at random above
/// it; and what keeping in step adds and drops.
struct WallpaperRulesTests {
    @Test func photosOnlyEachContentOnce() {
        let files = [
            file("/a/1.jpg", "h1"),
            file("/a/clip.mov", "h2", mime: "video/quicktime"),
            file("/b/1-copy.jpg", "h1"),
            file("/a/2.heic", "h3", mime: "image/heic"),
            file("/a/nohash.jpg", ""),
        ]
        let photos = WallpaperRules.photos(files)
        #expect(photos.map(\.hash) == ["h1", "h3"])
        #expect(photos.map(\.path) == ["/a/1.jpg", "/a/2.heic"])
    }

    @Test func theCountIsThePhotosUpToTheCap() {
        #expect(WallpaperRules.cap == 100)
        #expect(WallpaperRules.copyCount(0) == 0)
        #expect(WallpaperRules.copyCount(37) == 37)
        #expect(WallpaperRules.copyCount(100) == 100)
        #expect(WallpaperRules.copyCount(2500) == 100)
    }

    @Test func underTheCapAllInTheirOrder() {
        var rng = SeededRandom(state: 1)
        #expect(WallpaperRules.pick(hashes(40), using: &rng) == hashes(40))
        #expect(WallpaperRules.pick(hashes(100), using: &rng) == hashes(100))
    }

    @Test func overTheCapARandomHundredEachOnce() {
        var a = SeededRandom(state: 7)
        var b = SeededRandom(state: 8)
        let all = hashes(250)
        let one = WallpaperRules.pick(all, using: &a)
        let other = WallpaperRules.pick(all, using: &b)
        #expect(one.count == 100)
        #expect(Set(one).count == 100)
        #expect(Set(one).isSubset(of: Set(all)))
        // At random: another draw is another hundred.
        #expect(Set(one) != Set(other))
        #expect(WallpaperRules.pick(all, cap: 0, using: &a).isEmpty)
    }

    @Test func keepingInStepAddsTheNewAndDropsTheGone() {
        var rng = SeededRandom(state: 3)
        let plan = WallpaperRules.plan(members: ["h1", "h2", "h4", "h5"], stored: ["h1", "h2", "h3"], using: &rng)
        #expect(plan.drop == ["h3"])
        #expect(plan.add == ["h4", "h5"])
        // Nothing changed: nothing to do.
        #expect(WallpaperRules.plan(members: ["h1"], stored: ["h1"], using: &rng) == .init(add: [], drop: []))
    }

    @Test func newPhotosOnlyUpToTheCap() {
        var rng = SeededRandom(state: 4)
        let stored = Set(hashes(98))
        let plan = WallpaperRules.plan(members: hashes(98) + hashes(10, from: 500), stored: stored, using: &rng)
        #expect(plan.drop.isEmpty)
        #expect(plan.add.count == 2)
        #expect(Set(plan.add).isSubset(of: Set(hashes(10, from: 500))))
        // A photo gone makes room for one more.
        let after = WallpaperRules.plan(members: Array(hashes(98).dropFirst()) + hashes(10, from: 500), stored: stored, using: &rng)
        #expect(after.drop == ["h0"])
        #expect(after.add.count == 3)
        // Full: none.
        #expect(WallpaperRules.plan(members: hashes(150), stored: Set(hashes(100)), using: &rng).add.isEmpty)
    }

    @Test func aCopyIsSizedDownAndCarriesNoMetadata() throws {
        let big = UIGraphicsImageRenderer(size: CGSize(width: 3000, height: 2000), format: {
            let f = UIGraphicsImageRendererFormat(); f.scale = 1; return f
        }()).image { ctx in
            UIColor.orange.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 3000, height: 2000))
        }
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("wallpaper-test-\(UUID().uuidString).jpg")
        defer { try? FileManager.default.removeItem(at: url) }
        try #require(big.jpegData(compressionQuality: 0.9)).write(to: url)
        let data = try #require(WallpaperRules.resized(at: url, side: 1200))
        let img = try #require(UIImage(data: data))
        #expect(max(img.size.width, img.size.height) == 1200)
        #expect(WallpaperRules.resized(at: URL(fileURLWithPath: "/nonexistent"), side: 1200) == nil)
    }
}

/// The widget's order: at random, every photo once before any again.
struct WallpaperShuffleTests {
    @Test func noRepeatsUntilAllAreShown() {
        var rng = SeededRandom(state: 11)
        let all = (0..<10).map { "p\($0)" }
        var shown: [String] = []
        var seen: [String] = []
        // Three timelines of 4: the first ten picks are all ten photos.
        for _ in 0..<3 {
            let next = WallpaperShuffle.next(4, from: all, shown: shown, using: &rng)
            #expect(next.picks.count == 4)
            seen += next.picks
            shown = next.shown
        }
        #expect(Set(seen.prefix(10)) == Set(all))
        #expect(seen.prefix(10).count == 10)
        // The new round started where the first ended.
        #expect(shown.count == 2)
    }

    @Test func aDayOfFewPhotosGoesRoundAndNeverRepeatsBackToBack() {
        var rng = SeededRandom(state: 12)
        let all = ["a", "b", "c"]
        let next = WallpaperShuffle.next(24, from: all, shown: [], using: &rng)
        #expect(next.picks.count == 24)
        for i in stride(from: 0, to: 24, by: 3) {
            #expect(Set(next.picks[i..<(i + 3)]) == Set(all))
        }
        for (x, y) in zip(next.picks, next.picks.dropFirst()) { #expect(x != y) }
    }

    @Test func aPhotoNoLongerStoredIsForgotten() {
        var rng = SeededRandom(state: 13)
        let next = WallpaperShuffle.next(2, from: ["a", "b", "c"], shown: ["gone", "a"], using: &rng)
        #expect(!next.picks.contains("a"))
        #expect(!next.shown.contains("gone"))
        #expect(WallpaperShuffle.next(5, from: [], shown: ["a"], using: &rng).picks.isEmpty)
        // One photo: it, every hour.
        #expect(WallpaperShuffle.next(3, from: ["only"], shown: [], using: &rng).picks == ["only", "only", "only"])
    }
}

/// What is kept: the album's state outside the domain Log Out removes, the
/// widget's catalog in the App Group.
struct WallpaperSettingsTests {
    @Test func theAlbumsStateIsKeptAndReadBack() throws {
        let suite = "otc-tests-wallpaper-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let store = WallpaperStateStore(defaults: defaults)
        #expect(store.album() == nil)
        let state = WallpaperAlbumState(collectionID: "g1", collectionName: "Trip", albumID: "ALB/L0/040",
                                        copies: ["h1": "A/L0/001", "h2": "B/L0/001"], outOfAlbum: ["C/L0/001"])
        store.setAlbum(state)
        #expect(WallpaperStateStore(defaults: defaults).album() == state)
        #expect(state.assets == ["A/L0/001", "B/L0/001", "C/L0/001"])
        store.setAlbum(nil)
        #expect(store.album() == nil)
        #expect(WallpaperStateStore.suiteName != Bundle.main.bundleIdentifier)
    }

    @Test func theWidgetsCatalogIsKeptAndReadBack() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("widget-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: root) }
        #expect(WidgetCatalog.load(root: root) == WidgetCatalog())
        let c = WidgetCatalog.Collection(id: "g/1", name: "Trip", folder: WidgetCatalog.folderName(for: "g/1"), files: ["h1": "h1.jpg"])
        try WidgetCatalog(collections: [c]).save(root: root)
        #expect(WidgetCatalog.load(root: root).collection("g/1") == c)
        #expect(c.folder == "g1")
        #expect(WidgetCatalog.folderName(for: "../..") == "collection")
        #expect(WidgetCatalog.files(of: c, root: root).map(\.lastPathComponent) == ["h1.jpg"])
    }
}

/// The album's copies never go back to the device as this phone's photos.
struct WallpaperBackupTests {
    @Test func theAlbumsAssetsAreLeftOutOfTheSync() throws {
        let suite = "otc-tests-saved-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let saved = SavedToPhone(defaults: defaults)
        PhotosAlbumStore.noteMade("W/L0/001", saved: saved)
        PhotosAlbumStore.noteMade("W/L0/002", saved: saved)
        let fresh = ["P/L0/001", "W/L0/001", "P/L0/002", "W/L0/002"]
        let skipped = PhotoSync.leftOut(albums: [], saved: saved.ids(), fresh: Set(fresh))
        #expect(skipped == ["W/L0/001", "W/L0/002"])
        let run = PhotoSources.runList(retried: [], oneOff: [], fresh: fresh, skipped: skipped)
        #expect(run.ids == ["P/L0/001", "P/L0/002"])
        // A retried one the album made is left out too (the run's filter
        // on `saved`, as for the viewer's Save).
        #expect(saved.ids().contains("W/L0/001"))
    }
}

// MARK: - The work, with the device, Photos and the files stood in for

/// Photos, as the album needs it.
@MainActor
final class FakeAlbumStore: WallpaperAlbumStoring {
    var accessNow: WallpaperAccess = .full
    var askedAccess = 0
    var grantOnAsk: WallpaperAccess = .full
    var albums: [String: [String]] = [:]
    var titles: [String: String] = [:]
    var library: Set<String> = []
    var declineDelete = false
    var deletes = 0
    var made = 0

    func access() -> WallpaperAccess { accessNow }
    func requestAccess() async -> WallpaperAccess {
        askedAccess += 1
        accessNow = grantOnAsk
        return accessNow
    }
    func add(_ images: [WallpaperImage], album: String?, title: String) async throws -> (album: String, assets: [String]) {
        let id = album.flatMap { albums[$0] != nil ? $0 : nil } ?? "ALB/\(albums.count)"
        if albums[id] == nil { albums[id] = []; titles[id] = title }
        let ids = images.map { _ -> String in made += 1; return "A/\(made)" }
        albums[id]! += ids
        library.formUnion(ids)
        return (id, ids)
    }
    func remove(_ assets: [String], fromAlbum album: String) async throws {
        albums[album]?.removeAll { assets.contains($0) }
    }
    func rename(album: String, to title: String) async throws { titles[album] = title }
    func delete(assets: [String], album: String) async throws -> Bool {
        deletes += 1
        if declineDelete { return false }
        library.subtract(assets)
        albums[album] = nil
        return true
    }
    func albumExists(_ album: String) -> Bool { albums[album] != nil }
}

/// The device: collections and their photos, and each photo's file (a small
/// JPEG).
@MainActor
final class FakeWallpaperDevice {
    var groups: [Msg_ImageGroup] = []
    var members: [String: [WallpaperPhoto]] = [:]
    var fetched: [String] = []
    var failing: Set<String> = []
    static let jpeg: Data = {
        let f = UIGraphicsImageRendererFormat()
        f.scale = 1
        return UIGraphicsImageRenderer(size: CGSize(width: 64, height: 48), format: f).image { ctx in
            UIColor.blue.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 64, height: 48))
        }.jpegData(compressionQuality: 0.8)!
    }()

    func set(_ id: String, _ name: String, photos n: Int, from: Int = 0) {
        var g = Msg_ImageGroup()
        g.id = id
        g.name = name
        groups.removeAll { $0.id == id }
        groups.append(g)
        members[id] = hashes(n, from: from).map { WallpaperPhoto(hash: $0, path: "/p/\($0).jpg", mime: "image/jpeg") }
    }

    var device: WallpaperDevice {
        WallpaperDevice(
            groups: { [unowned self] in self.groups },
            photos: { [unowned self] id in self.members[id] ?? [] },
            fetch: { [unowned self] p, dest in
                self.fetched.append(p.hash)
                if self.failing.contains(p.hash) { throw WallpaperError.noAnswer }
                try Self.jpeg.write(to: dest)
            }
        )
    }
}

@MainActor
@Suite(.serialized)
struct WallpaperModelTests {
    private func model(_ device: FakeWallpaperDevice, album: FakeAlbumStore? = nil) throws -> (WallpaperModel, URL, UserDefaults, String) {
        let album = album ?? FakeAlbumStore()
        let suite = "otc-tests-wallpaper-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("widget-\(UUID().uuidString)", isDirectory: true)
        let m = WallpaperModel(device: device.device, albumStore: album, stateStore: WallpaperStateStore(defaults: defaults), widgetRoot: root)
        m.wifiOnly = { false }
        m.expensive = { false }
        m.reloadWidget = {}
        m.groupDefaults = defaults
        m.albumSide = 300
        m.wifiPoll = 0.02
        var seeded = SeededRandom(state: 5)
        m.rng = WallpaperRandom(source: { seeded.next() })
        return (m, root, defaults, suite)
    }

    @Test func theWidgetsCopiesAreStoredUpToTheCapAndStopDeletesThem() async throws {
        let device = FakeWallpaperDevice()
        device.set("g1", "Trip", photos: 130)
        let (m, root, defaults, suite) = try model(device)
        defer { try? FileManager.default.removeItem(at: root); defaults.removePersistentDomain(forName: suite) }
        let photos = try await m.photos(of: "g1")
        #expect(WallpaperRules.copyCount(photos.count) == 100)
        var reloads = 0
        m.reloadWidget = { reloads += 1 }
        await m.startWidget(collection: "g1", name: "Trip", photos: photos)
        #expect(m.widgetPhase == .ready(count: 100))
        #expect(device.fetched.count == 100)
        let c = try #require(WidgetCatalog.load(root: root).collection("g1"))
        #expect(c.files.count == 100)
        #expect(WidgetCatalog.files(of: c, root: root).allSatisfy { FileManager.default.fileExists(atPath: $0.path) })
        #expect(m.inUse("g1"))
        #expect(reloads > 0)
        m.stopWidget()
        #expect(m.widgetPhase == .stopped)
        #expect(!m.inUse("g1"))
        // Its name stays (no photo): a widget still set to it says its
        // copies are gone.
        #expect(WidgetNames.all(defaults) == ["g1": "Trip"])
        #expect(WidgetCatalog.load(root: root).collections.isEmpty)
        #expect(!FileManager.default.fileExists(atPath: root.appendingPathComponent(c.folder).path))
    }

    @Test func theWidgetFollowsTheCollection() async throws {
        let device = FakeWallpaperDevice()
        device.set("g1", "Trip", photos: 5)
        let (m, root, defaults, suite) = try model(device)
        defer { try? FileManager.default.removeItem(at: root); defaults.removePersistentDomain(forName: suite) }
        await m.startWidget(collection: "g1", name: "Trip", photos: try await m.photos(of: "g1"))
        // Two photos go, three come, and the collection is renamed.
        device.set("g1", "Summer", photos: 6, from: 2)
        await m.keepInStep(force: true)
        let c = try #require(WidgetCatalog.load(root: root).collection("g1"))
        #expect(Set(c.files.keys) == Set(hashes(6, from: 2)))
        #expect(c.name == "Summer")
        #expect(m.widgetPhase == .ready(count: 6))
        // Not again so soon unless forced.
        let fetched = device.fetched.count
        device.set("g1", "Summer", photos: 7, from: 2)
        await m.keepInStep()
        #expect(device.fetched.count == fetched)
        // Deleted: stopped, its copies gone.
        device.groups = []
        await m.keepInStep(force: true)
        #expect(m.widget == nil)
        #expect(WidgetCatalog.load(root: root).collections.isEmpty)
    }

    @Test func oneCollectionAtATimeForTheWidget() async throws {
        let device = FakeWallpaperDevice()
        device.set("g1", "Trip", photos: 3)
        device.set("g2", "Home", photos: 2, from: 10)
        let (m, root, defaults, suite) = try model(device)
        defer { try? FileManager.default.removeItem(at: root); defaults.removePersistentDomain(forName: suite) }
        await m.startWidget(collection: "g1", name: "Trip", photos: try await m.photos(of: "g1"))
        await m.startWidget(collection: "g2", name: "Home", photos: try await m.photos(of: "g2"))
        #expect(WidgetCatalog.load(root: root).collections.map(\.id) == ["g2"])
        #expect(!m.inUse("g1") && m.inUse("g2"))
    }

    @Test func aFailedCopyIsSaidAndNothingIsKeptWhenNoneCame() async throws {
        let device = FakeWallpaperDevice()
        device.set("g1", "Trip", photos: 2)
        device.failing = ["h0", "h1"]
        let (m, root, defaults, suite) = try model(device)
        defer { try? FileManager.default.removeItem(at: root); defaults.removePersistentDomain(forName: suite) }
        await m.startWidget(collection: "g1", name: "Trip", photos: try await m.photos(of: "g1"))
        #expect(m.widgetPhase == .failed)
        #expect(m.widget == nil)
        #expect(WidgetCatalog.load(root: root).collections.isEmpty)
        // One of two: kept, and said.
        device.failing = ["h1"]
        await m.startWidget(collection: "g1", name: "Trip", photos: try await m.photos(of: "g1"))
        #expect(m.widgetPhase == .failed)
        #expect(m.widget?.files.count == 1)
    }

    @Test func wifiOnlyHoldsTheCopiesOnAMobileNetwork() async throws {
        let device = FakeWallpaperDevice()
        device.set("g1", "Trip", photos: 2)
        let (m, root, defaults, suite) = try model(device)
        defer { try? FileManager.default.removeItem(at: root); defaults.removePersistentDomain(forName: suite) }
        var mobile = true
        m.wifiOnly = { true }
        m.expensive = { mobile }
        let photos = try await m.photos(of: "g1")
        let run = Task { await m.startWidget(collection: "g1", name: "Trip", photos: photos) }
        let end = Date() + 2
        while m.widgetPhase != .storing(done: 0, total: 2, waitingForWifi: true) && Date() < end {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        #expect(m.widgetPhase == .storing(done: 0, total: 2, waitingForWifi: true))
        #expect(device.fetched.isEmpty)
        mobile = false
        await run.value
        #expect(m.widgetPhase == .ready(count: 2))
    }

    @Test func theAlbumIsMadeAndStopDeletesItsAssetsIfThePersonAgrees() async throws {
        let device = FakeWallpaperDevice()
        device.set("g1", "Trip", photos: 25)
        let album = FakeAlbumStore()
        let (m, root, defaults, suite) = try model(device, album: album)
        defer { try? FileManager.default.removeItem(at: root); defaults.removePersistentDomain(forName: suite) }
        await m.startAlbum(collection: "g1", name: "Trip", photos: try await m.photos(of: "g1"))
        #expect(m.albumPhase == .ready(count: 25))
        let state = try #require(m.album)
        #expect(album.albums[state.albumID]?.count == 25)
        #expect(album.titles[state.albumID] == WallpaperRules.albumTitle("Trip"))
        // Kept for the next launch.
        #expect(WallpaperStateStore(defaults: defaults).album() == state)
        // Said no: nothing changes.
        album.declineDelete = true
        #expect(await m.stopAlbum() == false)
        #expect(m.album == state)
        #expect(album.library.count == 25)
        album.declineDelete = false
        #expect(await m.stopAlbum())
        #expect(m.album == nil)
        #expect(m.albumPhase == .stopped)
        #expect(album.library.isEmpty)
        #expect(WallpaperStateStore(defaults: defaults).album() == nil)
    }

    @Test func withoutFullAccessNothingIsStored() async throws {
        let device = FakeWallpaperDevice()
        device.set("g1", "Trip", photos: 3)
        let album = FakeAlbumStore()
        album.accessNow = .notDetermined
        album.grantOnAsk = .notFull
        let (m, root, defaults, suite) = try model(device, album: album)
        defer { try? FileManager.default.removeItem(at: root); defaults.removePersistentDomain(forName: suite) }
        await m.startAlbum(collection: "g1", name: "Trip", photos: try await m.photos(of: "g1"))
        #expect(album.askedAccess == 1)
        #expect(m.needsFullAccess)
        #expect(m.album == nil)
        #expect(device.fetched.isEmpty)
    }

    @Test func theAlbumFollowsTheCollectionAndStopDeletesWhatLeftIt() async throws {
        let device = FakeWallpaperDevice()
        device.set("g1", "Trip", photos: 4)
        let album = FakeAlbumStore()
        let (m, root, defaults, suite) = try model(device, album: album)
        defer { try? FileManager.default.removeItem(at: root); defaults.removePersistentDomain(forName: suite) }
        await m.startAlbum(collection: "g1", name: "Trip", photos: try await m.photos(of: "g1"))
        let albumID = try #require(m.album?.albumID)
        let h0 = try #require(m.album?.copies["h0"])
        // h0 leaves the collection, h4 joins; renamed.
        device.set("g1", "Summer", photos: 4, from: 1)
        await m.keepInStep(force: true)
        let state = try #require(m.album)
        #expect(Set(state.copies.keys) == ["h1", "h2", "h3", "h4"])
        // Out of the album only: iOS asks before any delete.
        #expect(state.outOfAlbum == [h0])
        #expect(album.library.contains(h0))
        #expect(!(album.albums[albumID] ?? []).contains(h0))
        #expect(album.titles[albumID] == WallpaperRules.albumTitle("Summer"))
        // Stop deletes it too.
        #expect(await m.stopAlbum())
        #expect(album.library.isEmpty)
    }

    @Test func aDeletedCollectionStopsBothWays() async throws {
        let device = FakeWallpaperDevice()
        device.set("g1", "Trip", photos: 3)
        let album = FakeAlbumStore()
        let (m, root, defaults, suite) = try model(device, album: album)
        defer { try? FileManager.default.removeItem(at: root); defaults.removePersistentDomain(forName: suite) }
        let photos = try await m.photos(of: "g1")
        await m.startAlbum(collection: "g1", name: "Trip", photos: photos)
        await m.startWidget(collection: "g1", name: "Trip", photos: photos)
        album.declineDelete = true
        await m.collectionDeleted("g1")
        // The widget's copies go; the album stays in Photos if the person
        // said no, but is no longer the app's.
        #expect(m.widget == nil)
        #expect(m.album == nil)
        #expect(album.deletes == 1)
        #expect(!m.inUse("g1"))
    }

    @Test func logOutStopsBothWays() async throws {
        let device = FakeWallpaperDevice()
        device.set("g1", "Trip", photos: 3)
        let album = FakeAlbumStore()
        let (m, root, defaults, suite) = try model(device, album: album)
        defer { try? FileManager.default.removeItem(at: root); defaults.removePersistentDomain(forName: suite) }
        let photos = try await m.photos(of: "g1")
        await m.startAlbum(collection: "g1", name: "Trip", photos: photos)
        await m.startWidget(collection: "g1", name: "Trip", photos: photos)
        await m.signedOut()
        #expect(WidgetNames.all(defaults).isEmpty)
        #expect(m.album == nil && m.widget == nil)
        #expect(album.library.isEmpty)
        #expect(WidgetCatalog.load(root: root).collections.isEmpty)
        #expect(WallpaperStateStore(defaults: defaults).album() == nil)
        #expect(m.albumPhase == .idle && m.widgetPhase == .idle)
    }

    @Test func anotherCollectionsAlbumIsStoppedFirst() async throws {
        let device = FakeWallpaperDevice()
        device.set("g1", "Trip", photos: 2)
        device.set("g2", "Home", photos: 2, from: 10)
        let album = FakeAlbumStore()
        let (m, root, defaults, suite) = try model(device, album: album)
        defer { try? FileManager.default.removeItem(at: root); defaults.removePersistentDomain(forName: suite) }
        await m.startAlbum(collection: "g1", name: "Trip", photos: try await m.photos(of: "g1"))
        // Said no to deleting the first: the second doesn't start.
        album.declineDelete = true
        await m.startAlbum(collection: "g2", name: "Home", photos: try await m.photos(of: "g2"))
        #expect(m.album?.collectionID == "g1")
        album.declineDelete = false
        await m.startAlbum(collection: "g2", name: "Home", photos: try await m.photos(of: "g2"))
        #expect(m.album?.collectionID == "g2")
        #expect(album.library.count == 2)
    }
}
