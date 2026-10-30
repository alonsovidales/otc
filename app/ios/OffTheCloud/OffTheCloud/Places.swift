// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  Places.swift
//  OffTheCloud
//
//  Places: the map in Images. The photos and videos the device has a
//  position for (GetPhotoPlaces, release 148), drawn where they were taken
//  on Apple's map, grouped as the device groups them: each tile of the map
//  at a zoom is 4 x 4 cells, and each cell holding photos is one place.
//  Zooming in splits the groups.
//
//    - Shown only while Places is on (PlacesSupport: Settings.places_enabled,
//      the user's switch in Settings, asked on every connection; off on
//      every device until turned on, as the device keeps where each photo
//      was taken unencrypted only then): a wide window's menu has a Places
//      page after Collections, a narrow one a grid/map switch at the top
//      of Images (MainView, PhotoGalleryView). A map told "places_off"
//      (turned off on another app) goes at once.
//    - Just turned on, the device reads where the photos were taken in
//      the background (PhotoPlaces.reading): the map says so, keeps those
//      answers only 15 s and asks again as it fills in.
//    - The map shows what Images would show: its search, people and open
//      collection (PlacesFilter), videos included as in the grid. It opens
//      framed on all of them (PhotoPlaces.located_area), or where it was
//      left for the same search.
//    - It asks for the tiles on the screen and one tile around them, at
//      the zoom its span shows (PlaceTiles), 64 at a time, once the camera
//      stops (and 200 ms more); then the next zoom's, to have them before
//      a zoom in. Answers are kept for the session by search, zoom and tile
//      (PlacesCache) - five minutes, or until the library changes - and
//      what is kept is drawn at once; a tile still on its way keeps the
//      places drawn there before.
//    - Places closer on the screen than a pin are drawn as one (PinMerge):
//      the device's cells are square, so two at a cell's edges can touch.
//    - A pin is the newest photo's small thumbnail (GridThumbLoader, from
//      the phone's cache or GetThumbnails, the pins on the screen first)
//      with the count. A tap opens a single photo in the viewer, zooms into
//      a group, and opens a group too close to split in Images
//      (SearchPhotos.area, the "Map area" chip) - PlaceTap.
//    - A group open in Images has "Back to the map" at the top of the grid
//      (AreaBackButton, the owner's request of 2026-10-30), on both
//      layouts: the map where it was, as the chip's name does.
//
//  The web (PlacesView.tsx) and Android follow the same rules.
//

import SwiftUI
import MapKit
import Combine

// MARK: - Tiles

/// A map tile at a zoom, numbered as GetPhotoPlaces takes them (PlaceTile:
/// slippy-map tiles, Web Mercator, x within 0 to 2^zoom - 1).
struct PlaceTileKey: Hashable, Sendable {
    let zoom: Int
    let x: Int
    let y: Int
}

/// The tile arithmetic, on MapKit's own Mercator map points: MKMapPoint is
/// the same projection as the device's tiles, its world 2^20 tiles of 256
/// points across.
enum PlaceTiles {
    static let maxZoom = 20
    /// The points a tile is wide at its zoom.
    static let tileSide: Double = 256
    /// The most tiles one GetPhotoPlaces takes.
    static let batchSize = 64
    static var worldWidth: Double { MKMapSize.world.width }

    /// The zoom a map `viewWidth` points wide shows across `lonSpan`
    /// degrees, as the device's tiles count it - log2(viewWidth · 360 /
    /// (256 · lonSpan)), not rounded.
    static func exactZoom(viewWidth: Double, lonSpan: Double) -> Double {
        guard viewWidth > 0, lonSpan > 0 else { return 0 }
        return log2(viewWidth * 360 / (tileSide * lonSpan))
    }

    /// The tiles' zoom for that span: the zoom shown, rounded down (so a
    /// cell is 64 to 128 points on the screen, room for a pin), 0 to 20.
    static func zoom(viewWidth: Double, lonSpan: Double) -> Int {
        let z = exactZoom(viewWidth: viewWidth, lonSpan: lonSpan)
        guard z.isFinite else { return 0 }
        return min(maxZoom, max(0, Int(floor(z + 1e-9))))
    }

    /// The span a visible map rect covers, in degrees of longitude.
    static func lonSpan(of rect: MKMapRect) -> Double {
        rect.size.width / worldWidth * 360
    }

    /// The tile a point is in (lon 180 in the last column, the poles in the
    /// first and last rows).
    static func tile(latitude: Double, longitude: Double, zoom: Int) -> PlaceTileKey {
        let n = 1 << zoom
        let p = MKMapPoint(CLLocationCoordinate2D(latitude: latitude, longitude: longitude))
        let side = worldWidth / Double(n)
        let x = min(n - 1, max(0, Int(floor(p.x / side))))
        let y = min(n - 1, max(0, Int(floor(p.y / side))))
        return PlaceTileKey(zoom: zoom, x: x, y: y)
    }

    /// The tiles a visible rect covers at `zoom`, with `margin` tiles more
    /// on every side: x taken modulo 2^zoom (a map across the antimeridian),
    /// rows off the map left out, each tile once, the ones nearest the
    /// middle first.
    static func tiles(covering rect: MKMapRect, zoom: Int, margin: Int) -> [PlaceTileKey] {
        let n = 1 << zoom
        let side = worldWidth / Double(n)
        // Just inside the far edges: a rect ending on a tile's edge doesn't
        // reach into the next one.
        let inset = side * 1e-9
        var x0 = Int(floor(rect.minX / side)) - margin
        var x1 = Int(floor((rect.maxX - inset) / side)) + margin
        if x1 - x0 + 1 >= n {
            x0 = 0
            x1 = n - 1
        }
        let y0 = max(0, Int(floor(rect.minY / side)) - margin)
        let y1 = min(n - 1, Int(floor((rect.maxY - inset) / side)) + margin)
        guard y0 <= y1, x0 <= x1 else { return [] }
        let midX = rect.midX / side - 0.5, midY = rect.midY / side - 0.5
        var seen = Set<PlaceTileKey>()
        var out: [(PlaceTileKey, Double)] = []
        for y in y0...y1 {
            for x in x0...x1 {
                let key = PlaceTileKey(zoom: zoom, x: ((x % n) + n) % n, y: y)
                guard seen.insert(key).inserted else { continue }
                let dx = Double(x) - midX, dy = Double(y) - midY
                out.append((key, dx * dx + dy * dy))
            }
        }
        return out.sorted { $0.1 < $1.1 }.map(\.0)
    }

    /// Tiles in GetPhotoPlaces' batches, in their order.
    static func batches(_ tiles: [PlaceTileKey], size: Int = batchSize) -> [[PlaceTileKey]] {
        stride(from: 0, to: tiles.count, by: size).map { Array(tiles[$0..<min(tiles.count, $0 + size)]) }
    }

    /// The map rect an area covers (west > east: across the antimeridian,
    /// so past the world's east edge).
    static func rect(of area: Msg_PlaceArea) -> MKMapRect {
        let nw = MKMapPoint(CLLocationCoordinate2D(latitude: area.north, longitude: area.west))
        var se = MKMapPoint(CLLocationCoordinate2D(latitude: area.south, longitude: area.east))
        if area.east < area.west { se.x += worldWidth }
        return MKMapRect(x: nw.x, y: nw.y, width: max(0, se.x - nw.x), height: max(0, se.y - nw.y))
    }

    /// Where to look at an area: `pad` of its size more on each side, and
    /// never less than `minWidth` map points across (a single spot about a
    /// town across), the same shape.
    static func framing(_ area: Msg_PlaceArea, minWidth: Double, pad: Double = 0.15) -> MKMapRect {
        let r = rect(of: area)
        var w = r.size.width * (1 + 2 * pad)
        var h = r.size.height * (1 + 2 * pad)
        if w < minWidth && h < minWidth {
            w = minWidth
            h = minWidth
        }
        return MKMapRect(x: r.midX - w / 2, y: max(0, r.midY - h / 2), width: w, height: min(h, MKMapSize.world.height))
    }

    /// About a town across: the width zoom 11 shows on a phone.
    static var townWidth: Double { worldWidth / Double(1 << 11) * 1.5 }

    /// Where a map opens on photos in `area`: framed on them, unless that
    /// takes about the whole world - more than the web's quarter zoom
    /// above its least, 2^-0.25 of the world's width. Then the world,
    /// its middle at `middle`'s longitude (the biggest place of zoom 0,
    /// which is at a photo; else 10° E) and 25° N: the smallest area
    /// holding photos from Tokyo to Mexico City runs across the Pacific,
    /// and framing it cut Europe in two at the edges.
    static func opening(_ area: Msg_PlaceArea, middle: Double?, minWidth: Double) -> MKMapRect {
        let framed = framing(area, minWidth: minWidth)
        guard framed.size.width > worldWidth * pow(2, -0.25) else { return framed }
        let mid = MKMapPoint(CLLocationCoordinate2D(latitude: 25, longitude: middle ?? 10))
        let h = MKMapSize.world.height / 2
        // x within the world: past its east edge is how a rect runs
        // across the antimeridian.
        var x = mid.x - worldWidth / 2
        if x < 0 { x += worldWidth }
        return MKMapRect(x: x, y: max(0, min(MKMapSize.world.height - h, mid.y - h / 2)), width: worldWidth, height: h)
    }

    /// The longitude of the biggest of zoom 0's places (nil: none).
    static func middle(of world: [Msg_PhotoPlace]) -> Double? {
        world.max { a, b in a.count != b.count ? a.count < b.count : a.longitude > b.longitude }?.longitude
    }
}

// MARK: - What the map shows

/// Images' search as it is: the filters GetPhotoPlaces takes, as the grid
/// sends them (videos always, as the grid shows them).
struct PlacesFilter: Hashable, Sendable {
    var tags: [String] = []
    var people: [String] = []
    var group: String = ""

    /// Nothing searched for: the whole library.
    var isEmpty: Bool { tags.isEmpty && people.isEmpty && group.isEmpty }

    func request(zoom: Int, tiles: [PlaceTileKey]) -> Msg_GetPhotoPlaces {
        var r = Msg_GetPhotoPlaces()
        r.zoom = Int32(zoom)
        r.tiles = tiles.map { t in
            var pt = Msg_PlaceTile()
            pt.x = Int32(t.x)
            pt.y = Int32(t.y)
            return pt
        }
        r.tags = tags
        r.personIds = people
        r.groupID = group
        r.includeVideos = true
        return r
    }
}

/// How many photos of a search have a position, and the smallest area
/// holding them (nil when none has).
struct PlacesExtent: Equatable {
    let total: Int
    let area: Msg_PlaceArea?
    /// The device is still reading where the photos were taken (Places
    /// just turned on): more come.
    let reading: Bool

    init(_ answer: Msg_PhotoPlaces) {
        total = Int(answer.locatedTotal)
        area = answer.locatedTotal > 0 && answer.hasLocatedArea ? answer.locatedArea : nil
        reading = answer.reading
    }
}

/// One pin on the map: a place, or places drawn as one (PinMerge).
struct PlacePin: Identifiable, Equatable {
    /// Its newest photo's content, which no other pin of a zoom has - so a
    /// pin that keeps its cover keeps its place in the map across zooms.
    let id: String
    let latitude: Double
    let longitude: Double
    let count: Int
    /// The newest photo (no content: its thumbnail is the pin's, by hash).
    let cover: Msg_File?
    /// The smallest area holding its photos (SearchPhotos.area).
    let area: Msg_PlaceArea

    var coordinate: CLLocationCoordinate2D { CLLocationCoordinate2D(latitude: latitude, longitude: longitude) }
    var mapPoint: MKMapPoint { MKMapPoint(coordinate) }

    init(id: String, latitude: Double, longitude: Double, count: Int, cover: Msg_File?, area: Msg_PlaceArea) {
        self.id = id
        self.latitude = latitude
        self.longitude = longitude
        self.count = count
        self.cover = cover
        self.area = area
    }

    init(_ p: Msg_PhotoPlace) {
        let cover = p.hasCover && !p.cover.hash.isEmpty ? p.cover : nil
        self.init(
            id: cover.map { "c:\($0.hash)" } ?? "p:\(p.zoom)/\(p.x)/\(p.y)/\(p.latitude),\(p.longitude)",
            latitude: p.latitude, longitude: p.longitude, count: Int(max(1, p.count)), cover: cover, area: p.area
        )
    }

    /// What VoiceOver reads: "Photo, 5 October 2026" for one, "12 items
    /// here" for a group.
    var label: String {
        if count == 1 {
            return PhotoMonths.tileLabel(video: cover?.mime.hasPrefix("video/") ?? false,
                                         date: cover.flatMap { $0.hasCreated ? $0.created.date : nil })
        }
        return S.appPhotosPlacesPin(count: count)
    }

    static func newer(_ a: Msg_File?, than b: Msg_File?) -> Bool {
        guard let a else { return false }
        guard let b else { return true }
        let da = a.hasCreated ? a.created.date : .distantPast
        let db = b.hasCreated ? b.created.date : .distantPast
        return da != db ? da > db : a.hash < b.hash
    }
}

/// Places closer on the screen than a pin, drawn as one: their photos
/// counted together, at their mean position, the newest photo's thumbnail,
/// the area holding them all. The biggest go first and take in what is
/// near them; a pin moved by what it took in can come near another, so
/// that is done again until no two are that close.
enum PinMerge {
    /// The least distance between two pins' centres, in points: a pin is 48
    /// with its border, and its count sticks out at the corner.
    static let gap: Double = 54

    private struct Cell: Hashable {
        let x: Int
        let y: Int
    }

    /// `pins` as drawn at `zoom` (the exact one shown).
    static func merge(_ pins: [PlacePin], zoom: Double, gap: Double = gap) -> [PlacePin] {
        var out = pins
        // Each pass leaves fewer pins, or is the last.
        while out.count > 1 {
            let next = pass(out, zoom: zoom, gap: gap)
            if next.count == out.count { break }
            out = next
        }
        return out
    }

    private static func pass(_ pins: [PlacePin], zoom: Double, gap: Double) -> [PlacePin] {
        guard pins.count > 1 else { return pins }
        // Map points per screen point at this zoom.
        let reach = gap * PlaceTiles.worldWidth / (PlaceTiles.tileSide * pow(2, zoom))
        guard reach > 0, reach.isFinite else { return pins }
        let points = pins.map(\.mapPoint)
        var cells: [Cell: [Int]] = [:]
        func cell(_ p: MKMapPoint) -> Cell { Cell(x: Int(floor(p.x / reach)), y: Int(floor(p.y / reach))) }
        for (i, p) in points.enumerated() { cells[cell(p), default: []].append(i) }
        let order = pins.indices.sorted { a, b in
            pins[a].count != pins[b].count ? pins[a].count > pins[b].count : pins[a].id < pins[b].id
        }
        var taken = [Bool](repeating: false, count: pins.count)
        var out: [PlacePin] = []
        for i in order where !taken[i] {
            taken[i] = true
            var members = [i]
            let c = cell(points[i])
            for dx in -1...1 {
                for dy in -1...1 {
                    for j in cells[Cell(x: c.x + dx, y: c.y + dy)] ?? [] where !taken[j] {
                        let ddx = points[j].x - points[i].x, ddy = points[j].y - points[i].y
                        if ddx * ddx + ddy * ddy < reach * reach {
                            taken[j] = true
                            members.append(j)
                        }
                    }
                }
            }
            out.append(members.count == 1 ? pins[i] : combine(members.map { pins[$0] }))
        }
        return out
    }

    static func combine(_ pins: [PlacePin]) -> PlacePin {
        var count = 0
        var x = 0.0, y = 0.0
        var cover: Msg_File?
        var coverID = pins[0].id
        var area = pins[0].area
        for p in pins {
            let pt = p.mapPoint
            count += p.count
            x += pt.x * Double(p.count)
            y += pt.y * Double(p.count)
            if PlacePin.newer(p.cover, than: cover) {
                cover = p.cover
                coverID = p.id
            }
            area.south = min(area.south, p.area.south)
            area.west = min(area.west, p.area.west)
            area.north = max(area.north, p.area.north)
            area.east = max(area.east, p.area.east)
        }
        // Drawn at the member nearest their weighted mean, as the device
        // draws a place at its photo nearest the mean: the mean itself can
        // be where no photo was taken.
        let mean = MKMapPoint(x: x / Double(count), y: y / Double(count))
        var at = pins[0]
        var best = Double.infinity
        for p in pins {
            let pt = p.mapPoint
            let d = (pt.x - mean.x) * (pt.x - mean.x) + (pt.y - mean.y) * (pt.y - mean.y)
            if d < best {
                best = d
                at = p
            }
        }
        return PlacePin(id: coverID, latitude: at.latitude, longitude: at.longitude, count: count, cover: cover, area: area)
    }
}

/// What a tap on a pin does.
enum PlaceTap {
    /// One photo: the viewer.
    case open(Msg_File)
    /// Closer: the camera to this rect.
    case zoom(MKMapRect)
    /// Too close together to split (or zoomed in far already): the photos
    /// in Images, by their area.
    case showInImages(Msg_PlaceArea)

    /// From zoom 16 on, and for photos within a few metres of each other,
    /// a group opens in Images.
    static let lastSplitZoom = 16.0
    static let fewMetres = 25.0

    /// `zoom`: the exact zoom shown; `view`: the rect on the screen.
    static func of(_ pin: PlacePin, zoom: Double, view: MKMapRect) -> PlaceTap {
        if pin.count == 1, let cover = pin.cover { return .open(cover) }
        let r = PlaceTiles.rect(of: pin.area)
        let across = MKMapPoint(x: r.minX, y: r.minY).distance(to: MKMapPoint(x: r.maxX, y: r.maxY))
        if zoom >= lastSplitZoom || across < fewMetres { return .showInImages(pin.area) }
        // Its photos, with room around them - and at least twice as close
        // as now, so a tap always zooms in.
        var target = PlaceTiles.framing(pin.area, minWidth: 0, pad: 0.25)
        if view.size.width > 0, target.size.width > view.size.width / 2 || target.size.height > view.size.height / 2 {
            let w = view.size.width / 2, h = view.size.height / 2
            let c = pin.mapPoint
            target = MKMapRect(x: c.x - w / 2, y: c.y - h / 2, width: w, height: h)
        }
        return .zoom(target)
    }
}

// MARK: - The answers kept

/// GetPhotoPlaces' answers for the session, by search, zoom and tile, each
/// for `ttl` (the device's index follows a change within 15 s, so a few
/// minutes keep a map fresh enough while it is browsed); the extent of
/// each search too.
struct PlacesCache {
    static let ttl: TimeInterval = 5 * 60
    /// An answer given while the device is still reading positions.
    static let readingTTL: TimeInterval = 15

    private struct Key: Hashable {
        let filter: PlacesFilter
        let tile: PlaceTileKey
    }

    private struct Entry {
        let places: [Msg_PhotoPlace]
        let at: Date
        let reading: Bool
    }

    private var tiles: [Key: Entry] = [:]
    private var extents: [PlacesFilter: PlacesExtent] = [:]

    /// The places of a tile, when its answer is kept and fresh: [] for a
    /// tile without any.
    func places(in tile: PlaceTileKey, for filter: PlacesFilter, now: Date = Date()) -> [Msg_PhotoPlace]? {
        guard let e = tiles[Key(filter: filter, tile: tile)],
              now.timeIntervalSince(e.at) < (e.reading ? Self.readingTTL : Self.ttl) else { return nil }
        return e.places
    }

    /// The tiles of `wanted` without a fresh answer, in their order.
    func missing(_ wanted: [PlaceTileKey], for filter: PlacesFilter, now: Date = Date()) -> [PlaceTileKey] {
        wanted.filter { places(in: $0, for: filter, now: now) == nil }
    }

    func extent(for filter: PlacesFilter) -> PlacesExtent? { extents[filter] }

    /// An answer for `asked` (at its zoom): every tile asked for keeps what
    /// the answer has in it, nothing when it has nothing.
    mutating func put(_ answer: Msg_PhotoPlaces, asked: [PlaceTileKey], for filter: PlacesFilter, now: Date = Date()) {
        var byTile: [PlaceTileKey: [Msg_PhotoPlace]] = [:]
        for p in answer.places {
            byTile[PlaceTileKey(zoom: Int(p.zoom), x: Int(p.x), y: Int(p.y)), default: []].append(p)
        }
        // Under the tiles as asked: an answer for another zoom (never
        // sent) holds nothing for them, and isn't asked for again.
        for t in asked {
            tiles[Key(filter: filter, tile: t)] = Entry(places: byTile[t] ?? [], at: now, reading: answer.reading)
        }
        extents[filter] = PlacesExtent(answer)
    }

    /// The library changed: nothing kept is fresh. The extents stay until
    /// the next answer, so a map opens framed as before.
    mutating func clearTiles() {
        tiles = [:]
    }

    mutating func clear() {
        tiles = [:]
        extents = [:]
    }
}

// MARK: - Whether the device has them

/// Whether Places shows: the signed-in device has it (release 148) and its
/// user turned it on (Settings.places_enabled, off by default). Asked on
/// every connection (it may have been updated, or turned on or off on
/// another app), with the answer this phone had last standing in
/// meanwhile, so Places doesn't come and go at each launch; Settings'
/// switch and a map told "places_off" set it at once. nil (never asked,
/// or Log Out): hidden.
@MainActor
final class PlacesSupport: ObservableObject {
    static let shared = PlacesSupport()

    @Published private(set) var supported: Bool?

    /// Shorthand for the places that show the map.
    var isOn: Bool { supported == true }

    /// Sends the question. Tests answer in the device's place.
    var request: @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope = { payload in
        try await OTCConnection.shared.ask("places", base: OTCConnection.listTimeout) { $0.payload = payload }
    }

    nonisolated private static let key = "placesOn"
    private var asking: Task<Void, Never>?

    init(stored: Bool? = UserDefaults.standard.object(forKey: PlacesSupport.key) as? Bool) {
        supported = stored
    }

    /// What the device's Settings say: on or off, false for a device before
    /// Places (no places_enabled), nil for no Settings (asked again).
    nonisolated static func verdict(_ resp: Msg_RespEnvelope?) -> Bool? {
        guard let resp, case .respSettings(let s) = resp.payload else { return nil }
        return s.hasPlacesEnabled && s.placesEnabled
    }

    /// Signed in, or connected again: asked now, then after a failure
    /// again (1 s doubling to 10 s, sooner on a wake) until it answers.
    func connected() {
        asking?.cancel()
        asking = Task { [weak self] in
            var failures = 0
            while !Task.isCancelled {
                guard let self else { return }
                let resp = try? await self.request(.reqGetSettings(Msg_GetSettings()))
                guard !Task.isCancelled else { return }
                // The same answer says whether On this day is on.
                if let resp, case .respSettings(let s) = resp.payload {
                    MemoriesModel.shared.deviceSaid(settings: s)
                    SmartSearchSupport.shared.deviceSaid(settings: s)
                }
                if let said = Self.verdict(resp) {
                    self.set(said)
                    break
                }
                failures += 1
                await OTCConnection.shared.sleepOrWake(PageRetry.delay(afterFailures: failures))
            }
            if !Task.isCancelled { self?.asking = nil }
        }
    }

    /// Settings' switch, or a map told "places_off" or "unknown_payload".
    func set(_ on: Bool) {
        UserDefaults.standard.set(on, forKey: Self.key)
        if supported != on { supported = on }
    }

    /// Log Out: the next device may say otherwise.
    func reset() {
        asking?.cancel()
        asking = nil
        UserDefaults.standard.removeObject(forKey: Self.key)
        supported = nil
    }
}

// MARK: - The map's model

/// The map's places for what Images shows: asks the device for the tiles
/// the map shows, keeps the answers and works out the pins. Images' (one
/// for the session, so the map comes back where it was).
@MainActor
final class PlacesModel: ObservableObject {
    typealias Request = @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope

    /// What is drawn.
    @Published private(set) var pins: [PlacePin] = []
    /// The search's photos with a position: nil until the device said.
    @Published private(set) var extent: PlacesExtent?
    /// The last ask failed: asked again meanwhile (retry), and the map says so.
    @Published private(set) var problem = false
    /// Bumped when pins got their thumbnails: they draw them.
    @Published private(set) var thumbsVersion = 0

    private(set) var filter = PlacesFilter()
    /// Where the map was left, and for which search: it comes back there.
    private(set) var savedRect: MKMapRect?
    private(set) var savedFor: PlacesFilter?
    /// The rect on the screen and the map's width, once the camera stopped.
    private(set) var view: (rect: MKMapRect, width: Double)?

    /// Sends GetPhotoPlaces. Tests answer in the device's place.
    var request: Request = { payload in
        try await OTCConnection.shared.ask("places", base: OTCConnection.listTimeout) { $0.payload = payload }
    }
    /// How long the camera rests before the tiles are asked for, and before
    /// the next zoom's are (tests shorten them).
    var settle: TimeInterval = 0.2
    var prefetchAfter: TimeInterval = 0.6

    let retry = PageRetry()
    let thumbs: GridThumbLoader
    private var cache = PlacesCache()
    private var asking = false
    private var settleTask: Task<Void, Never>?
    private var prefetchTask: Task<Void, Never>?
    private var lastAnswer = Date.distantPast
    /// While the device reads positions, what the map shows is asked again
    /// as its answers age.
    private var readingTask: Task<Void, Never>?
    /// The map is on the screen: only then is anything asked for (a delete
    /// in the grid only forgets what is kept).
    private var onMap = false
    /// The covers whose pins were on the screen at the last count.
    private var onScreen = Set<String>()
    private var watches: [AnyCancellable] = []

    /// The size pins' thumbnails are decoded for.
    static let pinSide: CGFloat = 48

    init(thumbCache: ThumbDiskCache = .shared) {
        thumbs = GridThumbLoader(maxPt: Self.pinSide, cache: thumbCache, request: GridThumbLoader.deviceRequest)
        thumbs.onImages = { [weak self] in self?.thumbsVersion &+= 1 }
        // Signed in again, a network came up, back in front: a failed ask
        // goes again now.
        watches.append(OTCConnection.shared.$wakes
            .dropFirst()
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.retry.wake() })
        // Settings cleared the thumbnail cache.
        watches.append(NotificationCenter.default.publisher(for: .otcThumbnailCacheCleared)
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                self?.thumbs.reset()
                self?.thumbsVersion &+= 1
            })
    }

    /// The zoom on the screen, exactly, and the tiles' zoom for it.
    var exactZoom: Double {
        guard let view else { return 0 }
        return PlaceTiles.exactZoom(viewWidth: view.width, lonSpan: PlaceTiles.lonSpan(of: view.rect))
    }

    var zoom: Int {
        guard let view else { return 0 }
        return PlaceTiles.zoom(viewWidth: view.width, lonSpan: PlaceTiles.lonSpan(of: view.rect))
    }

    /// Where a map of the whole world has its middle: the longitude of the
    /// biggest of zoom 0's places for the search shown, the tile asked for
    /// first (nil until it came).
    func worldMiddle() -> Double? {
        cache.places(in: PlaceTileKey(zoom: 0, x: 0, y: 0), for: filter).flatMap(PlaceTiles.middle)
    }

    /// The map came on screen, for this search. A map left a minute ago or
    /// more asks again for what it shows, keeping it drawn until then.
    func appeared(filter f: PlacesFilter) {
        onMap = true
        if Date().timeIntervalSince(lastAnswer) > 60 { cache.clearTiles() }
        setFilter(f)
        pump()
    }

    /// The map left the screen: nothing more is asked for until it comes
    /// back.
    func disappeared() {
        onMap = false
        settleTask?.cancel()
        prefetchTask?.cancel()
        readingTask?.cancel()
        for gone in onScreen { thumbs.disappeared(gone) }
        onScreen = []
    }

    /// Images' search changed: the map shows its photos, framed on them
    /// once their extent is known.
    func setFilter(_ f: PlacesFilter) {
        guard f != filter else { return }
        filter = f
        extent = cache.extent(for: f)
        // Another search's pins never stand in for this one's.
        pins = []
        retry.reset()
        problem = false
        redraw()
        pump()
    }

    /// The camera stopped at `rect`, on a map `width` points wide: its
    /// tiles asked for once it rests, and - `framed`: the map was on the
    /// search's photos already, not the world it opens on - kept as where
    /// the map is.
    func cameraChanged(rect: MKMapRect, width: Double, framed: Bool = true) {
        guard width > 0, rect.size.width > 0 else { return }
        view = (rect, width)
        if framed {
            savedRect = rect
            savedFor = filter
        }
        prefetchTask?.cancel()
        redraw()
        settleTask?.cancel()
        settleTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(self?.settle ?? 0))
            guard !Task.isCancelled else { return }
            self?.pump()
        }
    }

    /// The library changed (a delete, a folder kept out of Images): what is
    /// kept is asked for again, drawn as it was meanwhile.
    func libraryChanged() {
        cache.clearTiles()
        thumbs.reset()
        retry.reset()
        pump()
    }

    /// The banner's Try again.
    func retryNow() {
        retry.reset()
        problem = false
        pump()
    }

    /// An answer said the device is still reading positions: once it has
    /// aged, the screen's tiles (and the count with them) are asked again.
    private func askAgainWhileReading() {
        readingTask?.cancel()
        readingTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(PlacesCache.readingTTL + 0.5))
            guard let self, !Task.isCancelled, self.onMap else { return }
            self.pump()
        }
    }

    /// The pin's thumbnail, when decoded.
    func image(for pin: PlacePin) -> UIImage? {
        guard let c = pin.cover else { return nil }
        return GridThumbCache.cached(id: c.hash, maxPt: Self.pinSide)
    }

    // MARK: Asking

    /// The tiles the screen shows, with one around them.
    private var wanted: [PlaceTileKey] {
        guard let view else { return [] }
        return PlaceTiles.tiles(covering: view.rect, zoom: zoom, margin: 1)
    }

    /// The next ask, unless one is on its way or waits for its retry: the
    /// extent of a search not known yet (with the world's tile at zoom 0),
    /// else the screen's tiles the cache lacks, else - once the camera has
    /// rested a little more - the next zoom's.
    private func pump() {
        guard onMap, !asking, !retry.waiting else { return }
        let f = filter
        if cache.extent(for: f) == nil {
            ask(zoom: 0, tiles: [PlaceTileKey(zoom: 0, x: 0, y: 0)], filter: f)
            return
        }
        guard let view else { return }
        let z = zoom
        let missing = cache.missing(wanted, for: f)
        if !missing.isEmpty {
            ask(zoom: z, tiles: Array(missing.prefix(PlaceTiles.batchSize)), filter: f)
            return
        }
        guard z < PlaceTiles.maxZoom else { return }
        let next = cache.missing(PlaceTiles.tiles(covering: view.rect, zoom: z + 1, margin: 0), for: f)
        guard !next.isEmpty else { return }
        prefetchTask?.cancel()
        prefetchTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(self?.prefetchAfter ?? 0))
            guard let self, !Task.isCancelled, self.onMap, !self.asking, self.filter == f, self.zoom == z else { return }
            self.ask(zoom: z + 1, tiles: Array(next.prefix(PlaceTiles.batchSize)), filter: f)
        }
    }

    private func ask(zoom: Int, tiles: [PlaceTileKey], filter f: PlacesFilter) {
        asking = true
        Task { [weak self] in
            guard let self else { return }
            let resp = try? await self.request(.reqGetPhotoPlaces(f.request(zoom: zoom, tiles: tiles)))
            self.asking = false
            guard let resp, case .respPhotoPlaces(let answer) = resp.payload else {
                if resp?.errorCode == "unknown_payload" || resp?.errorCode == "places_off" {
                    // A device before Places (signed in to another one
                    // meanwhile), or Places turned off on another app: no map.
                    PlacesSupport.shared.set(false)
                    return
                }
                self.problem = true
                self.retry.failed { [weak self] in self?.pump() }
                return
            }
            self.retry.reset()
            self.problem = false
            self.lastAnswer = Date()
            self.cache.put(answer, asked: tiles, for: f)
            if f == self.filter {
                let e = PlacesExtent(answer)
                if e != self.extent { self.extent = e }
                self.redraw()
            }
            if answer.reading { self.askAgainWhileReading() }
            self.pump()
        }
    }

    // MARK: Drawing

    /// The pins for the screen: each tile's kept places at the zoom shown,
    /// or, for a tile still on its way, the pins drawn there before; then
    /// what is too close together drawn as one. Their thumbnails are asked
    /// for: those on the screen first.
    private func redraw() {
        guard let view else {
            if !pins.isEmpty { pins = [] }
            return
        }
        let z = zoom
        let tiles = wanted
        let before = Dictionary(grouping: pins) { PlaceTiles.tile(latitude: $0.latitude, longitude: $0.longitude, zoom: z) }
        var shown: [PlacePin] = []
        for t in tiles {
            if let kept = cache.places(in: t, for: filter) {
                shown.append(contentsOf: kept.map(PlacePin.init))
            } else {
                shown.append(contentsOf: before[t] ?? [])
            }
        }
        let merged = PinMerge.merge(shown, zoom: exactZoom)
        if merged != pins { pins = merged }
        askThumbnails(in: view.rect)
    }

    private func askThumbnails(in rect: MKMapRect) {
        var now = Set<String>()
        var ahead: [GridThumbLoader.Want] = []
        for p in pins {
            guard let c = p.cover else { continue }
            let w = GridThumbLoader.Want(id: c.hash, path: c.path, hash: c.hash)
            guard GridThumbCache.cached(id: c.hash, maxPt: Self.pinSide) == nil else { continue }
            if rect.contains(p.mapPoint) {
                now.insert(c.path)
                thumbs.need(w)
            } else {
                ahead.append(w)
            }
        }
        for gone in onScreen.subtracting(now) { thumbs.disappeared(gone) }
        onScreen = now
        if !ahead.isEmpty { thumbs.readAhead(ahead) }
    }
}

// MARK: - The map

/// The map of Images' photos (a narrow window's Images, switched to the
/// map; a wide one's Places page).
struct PlacesMapView: View {
    @ObservedObject var vm: PhotoGalleryVM
    @ObservedObject var places: PlacesModel
    /// A group too close to split was opened in Images: a wide window goes
    /// there (a narrow one's Images shows its grid again by itself).
    var onShowInImages: () -> Void

    @State private var position: MapCameraPosition = .rect(.world)
    @State private var width: Double = 0
    /// The map is on the search's photos or where it was left: the extent
    /// arriving later doesn't move it.
    @State private var framedFor: PlacesFilter?
    /// Opens a single photo (as Files opens one).
    @StateObject private var viewer = PhotoGalleryVM()
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    init(vm: PhotoGalleryVM, onShowInImages: @escaping () -> Void = {}) {
        self.vm = vm
        self.places = vm.places
        self.onShowInImages = onShowInImages
    }

    var body: some View {
        Map(position: $position, interactionModes: [.pan, .zoom]) {
            ForEach(places.pins) { pin in
                Annotation(pin.label, coordinate: pin.coordinate, anchor: .center) {
                    Button { tap(pin) } label: {
                        PlacePinView(pin: pin, image: places.image(for: pin), version: places.thumbsVersion)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(pin.label)
                }
                .annotationTitles(.hidden)
            }
        }
        // Calm, so the photos read: no businesses, muted colours.
        .mapStyle(.standard(elevation: .flat, emphasis: .muted, pointsOfInterest: .excludingAll))
        .onMapCameraChange(frequency: .onEnd) { ctx in
            places.cameraChanged(rect: ctx.rect, width: width, framed: framedFor == places.filter)
        }
        .onGeometryChange(for: Double.self) { Double($0.size.width) } action: { width = $0 }
        .overlay(alignment: .top) { notes.padding(.top, 10) }
        .onAppear {
            places.appeared(filter: vm.placesFilter)
            frame(animated: false)
        }
        .onDisappear { places.disappeared() }
        .onChange(of: vm.placesFilter) { _, f in
            places.setFilter(f)
            frame(animated: true)
        }
        .onChange(of: places.extent) { _, _ in frame(animated: framedFor != nil) }
        .fullScreenCover(isPresented: Binding(
            get: { viewer.openIndex != nil },
            set: { if !$0 { viewer.closeModal() } }
        )) {
            ImageModal(
                vm: viewer,
                save: {
                    let path = viewer.openPath
                    let fallback = viewerThumb()
                    viewer.saveOriginalToPhotos {
                        guard let path else { return fallback }
                        return await viewer.fullImage(of: path) ?? fallback
                    }
                },
                share: {
                    let fallback = viewerThumb()
                    Task { viewer.shareCurrentPhoto(await viewer.fullImageForOpen() ?? fallback) }
                },
                delete: { viewer.deleteCurrentPhoto(everyCopy: $0) }
            )
        }
    }

    /// The map on the search's photos: where it was left for this search,
    /// else framed on them once the device said where they are (the world
    /// when none has a position).
    private func frame(animated: Bool) {
        let f = places.filter
        guard framedFor != f else { return }
        let target: MKMapRect
        if let saved = places.savedRect, places.savedFor == f {
            target = saved
        } else if let e = places.extent {
            target = e.area.map { PlaceTiles.opening($0, middle: places.worldMiddle(), minWidth: PlaceTiles.townWidth) } ?? .world
        } else {
            return
        }
        framedFor = f
        if animated && !reduceMotion {
            withAnimation(.easeInOut(duration: 0.45)) { position = .rect(target) }
        } else {
            position = .rect(target)
        }
    }

    @ViewBuilder
    private var notes: some View {
        if places.problem {
            HStack(spacing: 10) {
                Text(S.appPhotosPlacesFailed).font(.subheadline)
                Button(LoadProblem.tryAgain) { places.retryNow() }
                    .font(.subheadline.weight(.semibold))
            }
            .padding(.horizontal, 14).padding(.vertical, 8)
            .background(.regularMaterial, in: Capsule())
        } else if let e = places.extent, e.reading {
            Text(S.appPhotosPlacesReading)
                .font(.subheadline)
                .padding(.horizontal, 14).padding(.vertical, 8)
                .background(.regularMaterial, in: Capsule())
        } else if let e = places.extent, e.total == 0 {
            Text(places.filter.isEmpty ? S.appPhotosPlacesNone : S.appPhotosPlacesNoneFiltered)
                .font(.subheadline)
                .padding(.horizontal, 14).padding(.vertical, 8)
                .background(.regularMaterial, in: Capsule())
        } else if places.extent == nil {
            ProgressView()
                .padding(10)
                .background(.regularMaterial, in: Circle())
                .accessibilityLabel(S.commonLoading)
        }
    }

    private func tap(_ pin: PlacePin) {
        switch PlaceTap.of(pin, zoom: places.exactZoom, view: places.view?.rect ?? .world) {
        case .open(let cover):
            open(cover, image: places.image(for: pin))
        case .zoom(let rect):
            if reduceMotion {
                position = .rect(rect)
            } else {
                withAnimation(.easeInOut(duration: 0.45)) { position = .rect(rect) }
            }
        case .showInImages(let area):
            vm.showArea(area)
            onShowInImages()
        }
    }

    /// One photo in the viewer, its pin's thumbnail until the full size
    /// comes. A delete there asks for the map's places again.
    private func open(_ f: Msg_File, image: UIImage?) {
        viewer.onDeleted = { [places] _ in places.libraryChanged() }
        viewer.showFiles([PhotoGalleryVM.Item(
            id: PhotoPaging.itemID(f), path: f.path, mime: f.mime, size: Int(f.fileSize),
            thumbData: nil, localURL: nil, isLocalOnly: false, thumbImage: image,
            created: f.hasCreated ? f.created.date : nil, hash: f.hash
        )], startAt: 0)
    }

    private func viewerThumb() -> UIImage? {
        guard let i = viewer.openIndex, viewer.items.indices.contains(i) else { return nil }
        return viewer.items[i].thumbImage
    }
}

/// A pin: the newest photo's thumbnail, rounded, in a white frame with a
/// soft shadow, and the count at its corner when it holds more than one. A
/// neutral square until the thumbnail comes; it fades in as it is drawn
/// (at once with Reduce Motion).
struct PlacePinView: View {
    let pin: PlacePin
    let image: UIImage?
    /// The model's thumbsVersion: drawn again when thumbnails arrive.
    let version: Int

    @State private var shown = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    static let side = PlacesModel.pinSide
    private static let corner: CGFloat = 11

    var body: some View {
        ZStack(alignment: .topTrailing) {
            thumb
                .frame(width: Self.side, height: Self.side)
                .clipShape(RoundedRectangle(cornerRadius: Self.corner, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Self.corner, style: .continuous).strokeBorder(.white, lineWidth: 2.5))
                .shadow(color: .black.opacity(0.28), radius: 5, y: 2)
            if pin.count > 1 {
                Text(verbatim: pin.count.formatted(.number.locale(L10n.shared.current.locale)))
                    .font(.caption2.weight(.bold))
                    .monospacedDigit()
                    .foregroundStyle(MenuStyle.emberInk)
                    .padding(.horizontal, 6)
                    .frame(minWidth: 20, minHeight: 20)
                    .background(MenuStyle.ember, in: Capsule())
                    .overlay(Capsule().strokeBorder(.white, lineWidth: 1.5))
                    .fixedSize()
                    .offset(x: 9, y: -9)
            }
        }
        .opacity(shown ? 1 : 0)
        .scaleEffect(shown || reduceMotion ? 1 : 0.85)
        .onAppear {
            if reduceMotion {
                shown = true
            } else {
                withAnimation(.easeOut(duration: 0.25)) { shown = true }
            }
        }
        .accessibilityElement(children: .ignore)
    }

    @ViewBuilder
    private var thumb: some View {
        if let image {
            Image(uiImage: image)
                .resizable()
                .scaledToFill()
                .transition(.opacity)
        } else {
            Rectangle().fill(Color(.systemGray5))
        }
    }
}

// MARK: - Back to the map

/// "Back to the map" at the top of Images' grid while it shows the photos
/// of a group opened from the map (PhotoGalleryVM.activeArea, the "Map
/// area" chip, which stays): an arrow and the words in ember, outlined as
/// the top bar's own action, on a 44 pt high target, read by VoiceOver as
/// a button by its words. It does what the chip's name does: the map where
/// it was - a narrow window's map in the grid's place, a wide one's Places
/// page (MainView turns `showMap` into it). Android's AreaBackButton.
struct AreaBackButton: View {
    let action: () -> Void

    /// Whether it shows: a group of the map is open and the map isn't on
    /// the screen in the grid's place (a narrow window switched to it).
    /// The chip's own rule.
    static func shown(area: Msg_PlaceArea?, mapShown: Bool) -> Bool {
        area != nil && !mapShown
    }

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                // "Backward": it points the other way in a right-to-left
                // language.
                Image(systemName: "arrow.backward")
                    .font(.subheadline.weight(.semibold))
                Text(S.appPhotosPlacesBackToMap)
                    .font(.subheadline.weight(.semibold))
                    .multilineTextAlignment(.leading)
            }
            .foregroundStyle(MenuStyle.ember)
            .padding(.horizontal, 16)
            .padding(.vertical, 6)
            .frame(minHeight: 44)
            .overlay(Capsule().stroke(MenuStyle.ember, lineWidth: 1))
            .contentShape(Capsule())
        }
        .buttonStyle(AreaBackButtonStyle())
        // The words alone, not the arrow's name.
        .accessibilityLabel(S.appPhotosPlacesBackToMap)
    }
}

/// Pressed: the ember faintly behind it, as the top bar's action.
private struct AreaBackButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .background(MenuStyle.ember.opacity(configuration.isPressed ? 0.12 : 0), in: Capsule())
    }
}
