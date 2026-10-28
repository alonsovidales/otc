// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  Wallpaper.swift
//  OffTheCloud
//
//  A collection as the iPhone's wallpaper (owner's request, 2026-10-28:
//  "select a gallery and use it as background image when the phone is
//  locked or background image, rotating at random"). Apple doesn't let an
//  app set the wallpaper, so the collection's photos go two ways, each
//  started by the person after a notice that copies are stored on the
//  phone (WallpaperView.swift):
//
//  - the lock screen: copies in an album of the Photos app ("Off The
//    Cloud · <name>"), which the lock screen's Photo Shuffle rotates. The
//    assets and the album the app made are remembered (WallpaperAlbumState,
//    in defaults of their own) and every asset goes into SavedToPhone as
//    it is made, so the photo sync never sends them back to the device and
//    Free Up Space never deletes them. Stop deletes them and the album in
//    one change, which iOS asks about first.
//  - a home screen widget: copies in the App Group's container
//    (WallpaperShared.swift), shown by OffTheCloudWidget, a new one every
//    hour. Stop deletes them.
//
//  Copies: photos only, sized for the screen (the album) or for a large
//  widget (WallpaperRules.widgetSide) from the file the viewer reads
//  (FileDownload), never the originals; at most `cap` per collection, a
//  random `cap` of a bigger one. They follow "Wi-Fi only" and the app's
//  connection (home network, bridge, Tailscale). One collection at a time
//  each way. Kept in step on coming to the front: photos added to the
//  collection are added (up to the cap), photos taken out leave the album
//  (iOS asks before any delete, so their copies go at Stop) or are deleted
//  from the widget's; a collection deleted stops both, a renamed one goes
//  on. Log Out (and so a change of device) stops both.
//

import Foundation
import ImageIO
import Photos
import UIKit
import UniformTypeIdentifiers
import WidgetKit

/// A photo of the collection: what is copied.
struct WallpaperPhoto: Equatable, Sendable {
    let hash: String
    let path: String
    let mime: String
}

/// The rules, apart from PhotoKit, the files and the device, to be tested.
enum WallpaperRules {
    /// The most photos copied for a collection, each way.
    static let cap = 100
    /// The longer side of a widget's copy: a large widget's size, as a
    /// widget extension has little memory.
    static let widgetSide = 1200
    /// The most photos a page of the collection's listing holds (a page
    /// without thumbnails, SearchPhotos.omit_thumbnails).
    static let pageLimit: Int32 = 200

    /// The photos of a listing: no videos, each content once (its first
    /// path), in the listing's order.
    static func photos(_ files: [Msg_File]) -> [WallpaperPhoto] {
        var seen = Set<String>()
        var out: [WallpaperPhoto] = []
        for f in files where f.mime.lowercased().hasPrefix("image/") && !f.hash.isEmpty {
            if seen.insert(f.hash).inserted { out.append(WallpaperPhoto(hash: f.hash, path: f.path, mime: f.mime)) }
        }
        return out
    }

    /// How many copies a collection of `photos` photos gets.
    static func copyCount(_ photos: Int) -> Int { min(max(0, photos), cap) }

    /// At most `cap` of `hashes`: all of them when they fit, else a random
    /// `cap`.
    static func pick<R: RandomNumberGenerator>(_ hashes: [String], cap: Int = cap, using rng: inout R) -> [String] {
        guard hashes.count > cap else { return hashes }
        guard cap > 0 else { return [] }
        return Array(hashes.shuffled(using: &rng).prefix(cap))
    }

    /// What keeping in step with the collection does.
    struct Plan: Equatable {
        /// Photos to copy: new in the collection, as many as there is room
        /// for under the cap.
        var add: [String]
        /// Photos copied that left the collection.
        var drop: [String]
    }

    /// The plan for a collection now holding `members` (content hashes, in
    /// its order) whose copies are `stored`.
    static func plan<R: RandomNumberGenerator>(members: [String], stored: Set<String>, cap: Int = cap,
                                              using rng: inout R) -> Plan {
        let present = Set(members)
        let drop = stored.filter { !present.contains($0) }.sorted()
        let room = max(0, cap - (stored.count - drop.count))
        var seen = Set<String>()
        let fresh = members.filter { !stored.contains($0) && seen.insert($0).inserted }
        return Plan(add: pick(fresh, cap: room, using: &rng), drop: drop)
    }

    /// The album's title in Photos: ios.wallpaper_album_name, without the
    /// marks that isolate a name inside a sentence.
    static func albumTitle(_ collection: String) -> String {
        S.iosWallpaperAlbumName(name: collection).unicodeScalars
            .filter { $0 != "\u{2068}" && $0 != "\u{2069}" }
            .reduce(into: "") { $0.unicodeScalars.append($1) }
    }

    /// A copy of the picture at `url` whose longer side is at most `side`
    /// pixels, as JPEG with no metadata (no place, no camera); nil for one
    /// that can't be read.
    static func resized(at url: URL, side: Int) -> Data? {
        guard let src = CGImageSourceCreateWithURL(url as CFURL, nil) else { return nil }
        let opts: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: side,
        ]
        guard let cg = CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary) else { return nil }
        let out = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(out, UTType.jpeg.identifier as CFString, 1, nil) else { return nil }
        CGImageDestinationAddImage(dest, cg, [kCGImageDestinationLossyCompressionQuality: 0.85] as CFDictionary)
        guard CGImageDestinationFinalize(dest) else { return nil }
        return out as Data
    }
}

/// The collection shown on the lock screen through a Photos album: the
/// album and every asset the app made for it.
struct WallpaperAlbumState: Codable, Equatable {
    var collectionID: String
    var collectionName: String
    var albumID: String
    /// The copies in the album, by the photo's content hash: their assets'
    /// local identifiers.
    var copies: [String: String]
    /// Copies taken out of the album (their photo left the collection):
    /// still in the library until Stop, as iOS asks before any delete.
    var outOfAlbum: [String] = []

    /// Every asset the app made: what Stop deletes.
    var assets: [String] { copies.values.sorted() + outOfAlbum }
}

/// Where the album's state is kept: defaults of their own, outside the
/// domain Log Out removes, as SavedToPhone's (the assets stay the app's
/// until they are deleted).
struct WallpaperStateStore {
    static let suiteName = "cloud.off-the.OffTheCloud.wallpaper"
    static let albumKey = "album"

    let defaults: UserDefaults

    static let shared = WallpaperStateStore(defaults: UserDefaults(suiteName: suiteName) ?? .standard)

    func album() -> WallpaperAlbumState? {
        guard let data = defaults.data(forKey: Self.albumKey) else { return nil }
        return try? JSONDecoder().decode(WallpaperAlbumState.self, from: data)
    }

    func setAlbum(_ state: WallpaperAlbumState?) {
        if let state, let data = try? JSONEncoder().encode(state) {
            defaults.set(data, forKey: Self.albumKey)
        } else {
            defaults.removeObject(forKey: Self.albumKey)
        }
    }
}

/// Whether the app may make the album: full access to Photos.
enum WallpaperAccess: Equatable {
    case full, notDetermined, notFull
}

/// One copy for the album.
struct WallpaperImage: Sendable {
    let hash: String
    let data: Data
}

/// The Photos library, as the album needs it (PhotosAlbumStore; tests
/// stand in for it).
@MainActor
protocol WallpaperAlbumStoring {
    func access() -> WallpaperAccess
    func requestAccess() async -> WallpaperAccess
    /// Adds the images to the album `album` - made, titled `title`, when
    /// nil or gone - and returns the album and the new assets, in order.
    func add(_ images: [WallpaperImage], album: String?, title: String) async throws -> (album: String, assets: [String])
    /// Takes assets out of the album (they stay in the library).
    func remove(_ assets: [String], fromAlbum album: String) async throws
    func rename(album: String, to title: String) async throws
    /// Deletes the assets and the album in one change, which iOS asks about
    /// first: false when the person said no.
    func delete(assets: [String], album: String) async throws -> Bool
    func albumExists(_ album: String) -> Bool
}

/// The device, as the wallpaper needs it (tests answer in its place).
struct WallpaperDevice {
    /// The collections.
    var groups: @MainActor () async throws -> [Msg_ImageGroup]
    /// A collection's photos, all its pages.
    var photos: @MainActor (_ group: String) async throws -> [WallpaperPhoto]
    /// Writes a photo's file (as the viewer reads it) to `dest`.
    var fetch: @MainActor (_ photo: WallpaperPhoto, _ dest: URL) async throws -> Void

    static let live = WallpaperDevice(
        groups: {
            var lg = Msg_ListImageGroups()
            lg.smallThumbnails = true
            let resp = try await OTCConnection.shared.ask("groups", base: OTCConnection.pageTimeout) { $0.payload = .reqListImageGroups(lg) }
            guard case .respImageGroups(let g) = resp.payload else { throw WallpaperError.noAnswer }
            return g.groups
        },
        photos: { group in
            var files: [Msg_File] = []
            var token = ""
            for _ in 0..<50 {
                var sp = Msg_SearchPhotos()
                sp.groupID = group
                sp.token = token
                sp.have = Int32(files.count)
                sp.omitThumbnails = true
                sp.limit = WallpaperRules.pageLimit
                let resp = try await OTCConnection.shared.ask("page", base: OTCConnection.pageTimeout) { $0.payload = .reqSearchPhotos(sp) }
                guard case .respListOfFiles(let lof) = resp.payload else { throw WallpaperError.noAnswer }
                files += lof.files
                if lof.token.isEmpty || lof.files.isEmpty { break }
                token = lof.token
            }
            return WallpaperRules.photos(files)
        },
        fetch: { photo, dest in
            try await FileDownload.download(path: photo.path, mime: photo.mime, to: dest)
        }
    )
}

/// The randomness of the picks: the system's, or a test's own sequence.
struct WallpaperRandom: RandomNumberGenerator {
    var source: () -> UInt64 = {
        var system = SystemRandomNumberGenerator()
        return system.next()
    }

    mutating func next() -> UInt64 { source() }
}

enum WallpaperError: Error {
    case noAnswer, unreadable, declined
}

/// The wallpaper's state and its work: the screen shows it, Images' chip
/// says whether a collection is in use, Log Out and a deleted collection
/// stop it. One for the app (`shared`); tests make their own.
@MainActor
final class WallpaperModel: ObservableObject {
    static let shared = WallpaperModel()

    enum Way: Equatable { case album, widget }

    /// What a way is doing, for the screen.
    enum Phase: Equatable {
        case idle
        /// Storing copies: `done` of `total`; `waitingForWifi` while held
        /// by "Wi-Fi only" on a mobile network.
        case storing(done: Int, total: Int, waitingForWifi: Bool)
        /// Copies stored (`count`) and kept in step.
        case ready(count: Int)
        /// The copies couldn't be stored: app.photos.wallpaper.failed.
        case failed
        /// Stopped: its copies were deleted.
        case stopped
    }

    @Published private(set) var album: WallpaperAlbumState?
    @Published private(set) var widget: WidgetCatalog.Collection?
    @Published private(set) var albumPhase: Phase = .idle
    @Published private(set) var widgetPhase: Phase = .idle
    /// Full access to Photos is needed and missing (the album).
    @Published private(set) var needsFullAccess = false

    var device: WallpaperDevice
    let albumStore: WallpaperAlbumStoring
    let stateStore: WallpaperStateStore
    /// The widget's copies' root; nil without the App Group (no copies).
    let widgetRoot: URL?
    /// "Wi-Fi only" now, and whether the network is a mobile one.
    var wifiOnly: () -> Bool = { SecretsStore.wifiOnlyNow }
    var expensive: () -> Bool = { NetworkWatch.shared.isExpensive }
    /// The App Group's defaults (the widget's names and shuffle); tests
    /// use their own.
    var groupDefaults: UserDefaults? = WallpaperGroup.defaults
    /// The widget's timelines go again (tests count).
    var reloadWidget: () -> Void = { WidgetCenter.shared.reloadTimelines(ofKind: WallpaperGroup.widgetKind) }
    /// The longer side of an album copy: the screen's, in pixels.
    var albumSide: Int = {
        let b = UIScreen.main.nativeBounds
        return Int(max(b.width, b.height))
    }()
    /// How long "Wi-Fi only" is waited for between checks.
    var wifiPoll: TimeInterval = 2
    var rng = WallpaperRandom()
    /// When the collections were last kept in step, and how often at most.
    private var lastStep: Date?
    var stepEvery: TimeInterval = 15 * 60
    /// Bumped by Log Out: work started before it stops writing.
    private var generation = 0
    private var busy = false

    init(device: WallpaperDevice = .live,
         albumStore: WallpaperAlbumStoring = PhotosAlbumStore(),
         stateStore: WallpaperStateStore = .shared,
         widgetRoot: URL? = WallpaperGroup.container.map(WallpaperGroup.widgetRoot(in:))) {
        self.device = device
        self.albumStore = albumStore
        self.stateStore = stateStore
        self.widgetRoot = widgetRoot
        album = stateStore.album()
        widget = widgetRoot.flatMap { WidgetCatalog.load(root: $0).collections.first }
        if let album { albumPhase = .ready(count: album.copies.count) }
        if let widget { widgetPhase = .ready(count: widget.files.count) }
    }

    /// The collection is shown one way or the other: its menu item says
    /// "Wallpaper settings…".
    func inUse(_ collection: String) -> Bool {
        album?.collectionID == collection || widget?.collectionID == collection
    }

    /// A way is at work: nothing else starts meanwhile.
    var working: Bool { busy }

    /// The screen opened for another collection than a way's: what that
    /// way said last (stopped, failed) is no longer news.
    func forgetNews() {
        if case .stopped = albumPhase { albumPhase = album.map { .ready(count: $0.copies.count) } ?? .idle }
        if case .failed = albumPhase, !busy { albumPhase = album.map { .ready(count: $0.copies.count) } ?? .idle }
        if case .stopped = widgetPhase { widgetPhase = widget.map { .ready(count: $0.files.count) } ?? .idle }
        if case .failed = widgetPhase, !busy { widgetPhase = widget.map { .ready(count: $0.files.count) } ?? .idle }
    }

    /// The photos a collection would show, for the notice's count.
    func photos(of collection: String) async throws -> [WallpaperPhoto] {
        try await device.photos(collection)
    }

    // MARK: The lock screen's album

    /// Whether Photos may be written as the album needs, asking the first
    /// time; `needsFullAccess` says so when it may not.
    func checkAccess() async -> Bool {
        var a = albumStore.access()
        if a == .notDetermined { a = await albumStore.requestAccess() }
        needsFullAccess = a != .full
        return a == .full
    }

    /// Stores copies of `photos` (the collection's, as counted for the
    /// notice) in a new album, after the notice. Another collection's album
    /// is stopped first (iOS asks); if the person says no, nothing starts.
    func startAlbum(collection: String, name: String, photos: [WallpaperPhoto]) async {
        guard !busy, !photos.isEmpty else { return }
        guard await checkAccess() else { return }
        if let current = album, current.collectionID != collection {
            guard await stopAlbum() else { return }
        }
        busy = true
        defer { busy = false }
        let gen = generation
        let picks = WallpaperRules.pick(photos.map(\.hash), using: &rng)
        let byHash = Dictionary(photos.map { ($0.hash, $0) }, uniquingKeysWith: { a, _ in a })
        var state = album ?? WallpaperAlbumState(collectionID: collection, collectionName: name, albumID: "", copies: [:])
        // The screen shows whose album is being made from the start.
        album = state
        let ok = await addToAlbum(picks.compactMap { byHash[$0] }, state: &state, gen: gen)
        guard gen == generation else { return }
        if state.albumID.isEmpty {
            // Nothing was made.
            album = nil
            albumPhase = .failed
            return
        }
        albumPhase = ok ? .ready(count: state.copies.count) : .failed
    }

    /// Copies `photos` into the album, ten at a time, keeping the state
    /// after each batch; false when any couldn't be stored.
    private func addToAlbum(_ photos: [WallpaperPhoto], state: inout WallpaperAlbumState, gen: Int) async -> Bool {
        var done = 0
        var allOK = true
        albumPhase = .storing(done: 0, total: photos.count, waitingForWifi: false)
        var batch: [WallpaperImage] = []
        func flush() async -> Bool {
            guard !batch.isEmpty else { return true }
            do {
                let made = try await albumStore.add(batch, album: state.albumID.isEmpty ? nil : state.albumID,
                                                    title: WallpaperRules.albumTitle(state.collectionName))
                guard gen == generation else { return false }
                state.albumID = made.album
                for (img, id) in zip(batch, made.assets) { state.copies[img.hash] = id }
                album = state
                stateStore.setAlbum(state)
                batch = []
                return true
            } catch {
                batch = []
                return false
            }
        }
        for p in photos {
            guard gen == generation else { return false }
            guard await waitForWifi(way: .album, done: done, total: photos.count, gen: gen) else { return false }
            if let data = await copy(p, side: albumSide) {
                batch.append(WallpaperImage(hash: p.hash, data: data))
            } else {
                allOK = false
            }
            done += 1
            if gen == generation { albumPhase = .storing(done: done, total: photos.count, waitingForWifi: false) }
            if batch.count >= 10 { allOK = await flush() && allOK }
        }
        allOK = await flush() && allOK
        return allOK && !state.copies.isEmpty
    }

    /// Stop: the album and every copy the app made for it, in one change iOS
    /// asks about first. If the person says no, nothing changes (false).
    @discardableResult
    func stopAlbum() async -> Bool {
        guard let state = album else { return true }
        guard let deleted = try? await albumStore.delete(assets: state.assets, album: state.albumID), deleted else { return false }
        album = nil
        stateStore.setAlbum(nil)
        albumPhase = .stopped
        return true
    }

    // MARK: The home screen widget

    /// Stores copies of `photos` for the widget, after the notice; another
    /// collection's copies go first.
    func startWidget(collection: String, name: String, photos: [WallpaperPhoto]) async {
        guard !busy, !photos.isEmpty, let root = widgetRoot else {
            if widgetRoot == nil { widgetPhase = .failed }
            return
        }
        if let current = widget, current.id != collection { stopWidget() }
        busy = true
        defer { busy = false }
        let gen = generation
        let picks = WallpaperRules.pick(photos.map(\.hash), using: &rng)
        let byHash = Dictionary(photos.map { ($0.hash, $0) }, uniquingKeysWith: { a, _ in a })
        var c = widget ?? WidgetCatalog.Collection(id: collection, name: name, folder: WidgetCatalog.folderName(for: collection), files: [:])
        // The screen shows whose copies are being stored from the start.
        widget = c
        let ok = await addToWidget(picks.compactMap { byHash[$0] }, collection: &c, root: root, gen: gen)
        guard gen == generation else { return }
        if c.files.isEmpty {
            // Nothing was stored.
            try? FileManager.default.removeItem(at: root.appendingPathComponent(c.folder, isDirectory: true))
            saveWidget(nil, root: root)
            widgetPhase = .failed
            return
        }
        widgetPhase = ok ? .ready(count: c.files.count) : .failed
    }

    private func addToWidget(_ photos: [WallpaperPhoto], collection c: inout WidgetCatalog.Collection, root: URL, gen: Int) async -> Bool {
        let dir = root.appendingPathComponent(c.folder, isDirectory: true)
        var allOK = true
        var done = 0
        widgetPhase = .storing(done: 0, total: photos.count, waitingForWifi: false)
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        } catch {
            return false
        }
        for p in photos {
            guard gen == generation else { return false }
            guard await waitForWifi(way: .widget, done: done, total: photos.count, gen: gen) else { return false }
            if let data = await copy(p, side: WallpaperRules.widgetSide) {
                let name = WidgetCatalog.folderName(for: p.hash) + ".jpg"
                do {
                    try data.write(to: dir.appendingPathComponent(name), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
                    c.files[p.hash] = name
                } catch {
                    allOK = false
                }
            } else {
                allOK = false
            }
            done += 1
            if gen == generation { widgetPhase = .storing(done: done, total: photos.count, waitingForWifi: false) }
            // Every ten, the catalog: the widget can show them already.
            if done % 10 == 0 || done == photos.count { saveWidget(c, root: root) }
        }
        saveWidget(c, root: root)
        return allOK && !c.files.isEmpty
    }

    private func saveWidget(_ c: WidgetCatalog.Collection?, root: URL) {
        var catalog = WidgetCatalog()
        if let c {
            catalog.collections = [c]
            WidgetNames.remember(id: c.id, name: c.name, in: groupDefaults)
        }
        try? catalog.save(root: root)
        widget = c
        reloadWidget()
    }

    /// Stop: the widget's copies are deleted and it asks for a collection
    /// again.
    func stopWidget() {
        guard let root = widgetRoot, let c = widget else { return }
        try? FileManager.default.removeItem(at: root.appendingPathComponent(c.folder, isDirectory: true))
        groupDefaults?.removeObject(forKey: WallpaperShuffle.key(c.id))
        saveWidget(nil, root: root)
        widgetPhase = .stopped
    }

    // MARK: Keeping in step

    /// On coming to the front (at most every `stepEvery`, unless `force`):
    /// photos added to a collection in use are copied, photos taken out
    /// leave it, a deleted collection stops, a renamed one keeps its new
    /// name.
    func keepInStep(force: Bool = false) async {
        guard !busy, album != nil || widget != nil else { return }
        if !force, let lastStep, Date().timeIntervalSince(lastStep) < stepEvery { return }
        // The first time after a launch the widget is drawn again (a new
        // version of the app draws it differently).
        if lastStep == nil, widget != nil { reloadWidget() }
        lastStep = Date()
        guard let groups = try? await device.groups() else { return }
        busy = true
        defer { busy = false }
        let gen = generation
        if var state = album {
            if !albumStore.albumExists(state.albumID) {
                // The album was deleted in Photos: nothing left to show.
                album = nil
                stateStore.setAlbum(nil)
                albumPhase = .idle
            } else if let g = groups.first(where: { $0.id == state.collectionID }) {
                if g.name != state.collectionName {
                    state.collectionName = g.name
                    try? await albumStore.rename(album: state.albumID, to: WallpaperRules.albumTitle(g.name))
                    album = state
                    stateStore.setAlbum(state)
                }
                if albumStore.access() == .full, let photos = try? await device.photos(g.id), gen == generation {
                    await stepAlbum(photos, state: state, gen: gen)
                }
            } else {
                // The collection was deleted: so is its album, if the
                // person agrees; either way the app lets go of it.
                if !(await stopAlbum()) { forgetAlbum() }
            }
        }
        guard gen == generation else { return }
        if var c = widget, let root = widgetRoot {
            if let g = groups.first(where: { $0.id == c.id }) {
                if g.name != c.name {
                    c.name = g.name
                    saveWidget(c, root: root)
                }
                if let photos = try? await device.photos(g.id), gen == generation {
                    await stepWidget(photos, collection: c, root: root, gen: gen)
                }
            } else {
                stopWidget()
            }
        }
    }

    private func stepAlbum(_ photos: [WallpaperPhoto], state: WallpaperAlbumState, gen: Int) async {
        var state = state
        let plan = WallpaperRules.plan(members: photos.map(\.hash), stored: Set(state.copies.keys), using: &rng)
        if !plan.drop.isEmpty {
            let ids = plan.drop.compactMap { state.copies[$0] }
            if (try? await albumStore.remove(ids, fromAlbum: state.albumID)) != nil {
                for h in plan.drop { state.copies[h] = nil }
                state.outOfAlbum += ids
                album = state
                stateStore.setAlbum(state)
            }
        }
        guard !plan.add.isEmpty, gen == generation else { return }
        let byHash = Dictionary(photos.map { ($0.hash, $0) }, uniquingKeysWith: { a, _ in a })
        let ok = await addToAlbum(plan.add.compactMap { byHash[$0] }, state: &state, gen: gen)
        guard gen == generation else { return }
        albumPhase = ok ? .ready(count: state.copies.count) : .failed
    }

    private func stepWidget(_ photos: [WallpaperPhoto], collection: WidgetCatalog.Collection, root: URL, gen: Int) async {
        var c = collection
        let plan = WallpaperRules.plan(members: photos.map(\.hash), stored: Set(c.files.keys), using: &rng)
        if !plan.drop.isEmpty {
            let dir = root.appendingPathComponent(c.folder, isDirectory: true)
            for h in plan.drop {
                if let name = c.files[h] { try? FileManager.default.removeItem(at: dir.appendingPathComponent(name)) }
                c.files[h] = nil
            }
            saveWidget(c, root: root)
        }
        guard !plan.add.isEmpty, gen == generation else { return }
        let byHash = Dictionary(photos.map { ($0.hash, $0) }, uniquingKeysWith: { a, _ in a })
        let ok = await addToWidget(plan.add.compactMap { byHash[$0] }, collection: &c, root: root, gen: gen)
        guard gen == generation else { return }
        widgetPhase = ok ? .ready(count: c.files.count) : .failed
    }

    /// The collection open in Images was deleted: both ways stop.
    func collectionDeleted(_ id: String) async {
        if widget?.id == id { stopWidget() }
        if album?.collectionID == id {
            // Said no to deleting them: the album stays in Photos, no
            // longer the app's to keep in step (its assets stay out of the
            // sync: SavedToPhone).
            if !(await stopAlbum()) { forgetAlbum() }
        }
    }

    /// Log Out (and so a change of device): both stop - their photos are
    /// that device's. The album is deleted if the person agrees; either way
    /// the app lets go of it (its assets stay out of the sync).
    func signedOut() async {
        generation += 1
        lastStep = nil
        stopWidget()
        // The names of that device's collections go too.
        WidgetNames.forget(in: groupDefaults)
        if album != nil, !(await stopAlbum()) { forgetAlbum() }
        albumPhase = .idle
        widgetPhase = .idle
        needsFullAccess = false
    }

    private func forgetAlbum() {
        album = nil
        stateStore.setAlbum(nil)
        albumPhase = .idle
    }

    // MARK: Copies

    /// One copy: the photo's file, as the viewer reads it, fetched to a
    /// temporary file that goes at once, and sized down to `side`.
    private func copy(_ p: WallpaperPhoto, side: Int) async -> Data? {
        let tmp = FileManager.default.temporaryDirectory.appendingPathComponent("wallpaper-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: tmp) }
        do {
            try await device.fetch(p, tmp)
        } catch {
            return nil
        }
        return await Task.detached(priority: .utility) { WallpaperRules.resized(at: tmp, side: side) }.value
    }

    /// Holds while "Wi-Fi only" is on and the network is a mobile one,
    /// saying so; false once Log Out has stopped the work.
    private func waitForWifi(way: Way, done: Int, total: Int, gen: Int) async -> Bool {
        while wifiOnly() && expensive() {
            guard gen == generation else { return false }
            let waiting = Phase.storing(done: done, total: total, waitingForWifi: true)
            if way == .album { albumPhase = waiting } else { widgetPhase = waiting }
            try? await Task.sleep(nanoseconds: UInt64(wifiPoll * 1_000_000_000))
        }
        return gen == generation
    }
}

extension WidgetCatalog.Collection {
    /// The collection's id on the device (as WallpaperAlbumState's).
    var collectionID: String { id }
}

/// The Photos library behind the album.
@MainActor
struct PhotosAlbumStore: WallpaperAlbumStoring {
    nonisolated init() {}

    /// An asset the album made: a copy of the device's photo, never one for
    /// the photo sync to send back nor for Free Up Space (SavedToPhone,
    /// which both leave out).
    nonisolated static func noteMade(_ localIdentifier: String, saved: SavedToPhone = .shared) {
        saved.remember(localIdentifier)
    }

    func access() -> WallpaperAccess {
        switch PHPhotoLibrary.authorizationStatus(for: .readWrite) {
        case .authorized: return .full
        case .notDetermined: return .notDetermined
        default: return .notFull
        }
    }

    func requestAccess() async -> WallpaperAccess {
        _ = await PHPhotoLibrary.requestAuthorization(for: .readWrite)
        return access()
    }

    /// The assets made in a change block, read once it is done.
    private final class Made: @unchecked Sendable {
        var album: String?
        var assets: [String] = []
    }

    func add(_ images: [WallpaperImage], album: String?, title: String) async throws -> (album: String, assets: [String]) {
        let made = Made()
        let existing = album.flatMap { PHAssetCollection.fetchAssetCollections(withLocalIdentifiers: [$0], options: nil).firstObject }
        let datas = images.map(\.data)
        try await PHPhotoLibrary.shared().performChanges {
            let albumRequest: PHAssetCollectionChangeRequest?
            if let existing {
                albumRequest = PHAssetCollectionChangeRequest(for: existing)
                made.album = existing.localIdentifier
            } else {
                let r = PHAssetCollectionChangeRequest.creationRequestForAssetCollection(withTitle: title)
                made.album = r.placeholderForCreatedAssetCollection.localIdentifier
                albumRequest = r
            }
            var placeholders: [PHObjectPlaceholder] = []
            for data in datas {
                let request = PHAssetCreationRequest.forAsset()
                request.addResource(with: .photo, data: data, options: nil)
                guard let ph = request.placeholderForCreatedAsset else { continue }
                // Noted in the change block, before the change it brings
                // can start a sync.
                Self.noteMade(ph.localIdentifier)
                placeholders.append(ph)
                made.assets.append(ph.localIdentifier)
            }
            albumRequest?.addAssets(placeholders as NSArray)
        }
        guard let id = made.album else { throw WallpaperError.unreadable }
        return (id, made.assets)
    }

    func remove(_ assets: [String], fromAlbum album: String) async throws {
        guard let collection = PHAssetCollection.fetchAssetCollections(withLocalIdentifiers: [album], options: nil).firstObject else { return }
        let found = PHAsset.fetchAssets(withLocalIdentifiers: assets, options: nil)
        try await PHPhotoLibrary.shared().performChanges {
            PHAssetCollectionChangeRequest(for: collection)?.removeAssets(found)
        }
    }

    func rename(album: String, to title: String) async throws {
        guard let collection = PHAssetCollection.fetchAssetCollections(withLocalIdentifiers: [album], options: nil).firstObject else { return }
        try await PHPhotoLibrary.shared().performChanges {
            PHAssetCollectionChangeRequest(for: collection)?.title = title
        }
    }

    func delete(assets: [String], album: String) async throws -> Bool {
        let found = PHAsset.fetchAssets(withLocalIdentifiers: assets, options: nil)
        let collections = PHAssetCollection.fetchAssetCollections(withLocalIdentifiers: [album], options: nil)
        if found.count == 0 && collections.count == 0 { return true }
        do {
            try await PHPhotoLibrary.shared().performChanges {
                if found.count > 0 { PHAssetChangeRequest.deleteAssets(found) }
                if collections.count > 0 { PHAssetCollectionChangeRequest.deleteAssetCollections(collections) }
            }
            return true
        } catch {
            // iOS's question answered no (PHPhotosError.userCancelled), or
            // the change failed: nothing was deleted either way.
            return false
        }
    }

    func albumExists(_ album: String) -> Bool {
        !album.isEmpty && PHAssetCollection.fetchAssetCollections(withLocalIdentifiers: [album], options: nil).count > 0
    }
}

/// The language chosen in the app, for the widget's texts: kept in the App
/// Group, and the widget drawn again when it changes.
enum WallpaperLanguage {
    @MainActor static func share(_ code: String) {
        guard let defaults = WallpaperGroup.defaults, defaults.string(forKey: WallpaperGroup.languageKey) != code else { return }
        defaults.set(code, forKey: WallpaperGroup.languageKey)
        WidgetCenter.shared.reloadTimelines(ofKind: WallpaperGroup.widgetKind)
    }
}
