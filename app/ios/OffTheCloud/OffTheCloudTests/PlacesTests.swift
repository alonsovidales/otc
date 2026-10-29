// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import MapKit
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Places, the map in Images (Places.swift): the tiles asked for, the
/// answers kept, the pins drawn and what a tap does. The web's and
/// Android's follow the same rules.
struct PlaceTilesTests {
    /// The device's formula (PlaceTile in messages.proto).
    private func slippy(_ lat: Double, _ lon: Double, _ z: Int) -> (Int, Int) {
        let n = Double(1 << z)
        let la = min(85.0511287798, max(-85.0511287798, lat)) * .pi / 180
        let x = min(n - 1, floor((lon + 180) / 360 * n))
        let y = min(n - 1, max(0, floor((1 - log(tan(la) + 1 / cos(la)) / .pi) / 2 * n)))
        return (Int(x), Int(y))
    }

    @Test func aPointsTileIsTheDevicesAtEveryZoom() {
        let points: [(Double, Double)] = [(40.4168, -3.7038), (-33.8688, 151.2093), (64.1466, -21.9426),
                                          (0, 0), (-0.0001, -0.0001), (51.5007, -0.1246), (35.6762, 139.6503)]
        for (lat, lon) in points {
            for z in 0...20 {
                let t = PlaceTiles.tile(latitude: lat, longitude: lon, zoom: z)
                let (x, y) = slippy(lat, lon, z)
                #expect(t.zoom == z)
                #expect(t.x == x && t.y == y, "\(lat),\(lon) at \(z): \(t) vs \(x),\(y)")
            }
        }
    }

    @Test func theEdgesOfTheMapAreItsLastTiles() {
        // Longitude 180 is the last column, as the device counts it; the
        // poles are the first and last rows.
        #expect(PlaceTiles.tile(latitude: 0, longitude: 180, zoom: 3).x == 7)
        #expect(PlaceTiles.tile(latitude: 0, longitude: -180, zoom: 3).x == 0)
        #expect(PlaceTiles.tile(latitude: 90, longitude: 0, zoom: 4).y == 0)
        #expect(PlaceTiles.tile(latitude: -90, longitude: 0, zoom: 4).y == 15)
        #expect(PlaceTiles.tile(latitude: 89, longitude: 10, zoom: 0) == PlaceTileKey(zoom: 0, x: 0, y: 0))
    }

    @Test func theZoomComesFromTheSpan() {
        // The whole world on a 256-point map is zoom 0; twice as wide, 1.
        #expect(PlaceTiles.zoom(viewWidth: 256, lonSpan: 360) == 0)
        #expect(PlaceTiles.zoom(viewWidth: 512, lonSpan: 360) == 1)
        // A phone across the world: still 0 (rounded down).
        #expect(PlaceTiles.zoom(viewWidth: 390, lonSpan: 360) == 0)
        #expect(abs(PlaceTiles.exactZoom(viewWidth: 390, lonSpan: 360) - log2(390.0 / 256)) < 1e-9)
        // A street, and closer than the device's last zoom: 20.
        #expect(PlaceTiles.zoom(viewWidth: 390, lonSpan: 0.0005) == 20)
        #expect(PlaceTiles.zoom(viewWidth: 390, lonSpan: 0.0000001) == 20)
        // A town across a phone.
        #expect(PlaceTiles.zoom(viewWidth: 390, lonSpan: 0.27) == 10)
        // Nothing to measure yet.
        #expect(PlaceTiles.zoom(viewWidth: 0, lonSpan: 10) == 0)
        #expect(PlaceTiles.zoom(viewWidth: 390, lonSpan: 0) == 0)
        // The same from a map rect.
        let half = MKMapRect(x: 0, y: 0, width: MKMapSize.world.width / 2, height: MKMapSize.world.height / 2)
        #expect(PlaceTiles.lonSpan(of: half) == 180)
    }

    @Test func theScreensTilesWithOneAroundThemNearestFirst() {
        let n = 4.0
        let side = MKMapSize.world.width / n
        // Exactly tile (1, 1) at zoom 2.
        let r = MKMapRect(x: side, y: side, width: side, height: side)
        let tiles = PlaceTiles.tiles(covering: r, zoom: 2, margin: 1)
        #expect(tiles.count == 9)
        #expect(tiles.first == PlaceTileKey(zoom: 2, x: 1, y: 1))
        #expect(Set(tiles.map(\.x)) == [0, 1, 2])
        #expect(Set(tiles.map(\.y)) == [0, 1, 2])
        #expect(PlaceTiles.tiles(covering: r, zoom: 2, margin: 0) == [PlaceTileKey(zoom: 2, x: 1, y: 1)])
    }

    @Test func theWholeWorldIsEachTileOnce() {
        let world = MKMapRect.world
        #expect(PlaceTiles.tiles(covering: world, zoom: 0, margin: 1) == [PlaceTileKey(zoom: 0, x: 0, y: 0)])
        let z2 = PlaceTiles.tiles(covering: world, zoom: 2, margin: 1)
        #expect(z2.count == 16)
        #expect(Set(z2).count == 16)
    }

    @Test func acrossTheAntimeridianTheTilesWrap() {
        let n = 8
        let side = MKMapSize.world.width / Double(n)
        // From the last column on past the world's east edge.
        let r = MKMapRect(x: side * 7.5, y: side * 3, width: side, height: side * 0.5)
        let xs = Set(PlaceTiles.tiles(covering: r, zoom: 3, margin: 0).map(\.x))
        #expect(xs == [7, 0])
        // Rows off the map are left out.
        let top = MKMapRect(x: side * 2, y: 0, width: side, height: side * 0.5)
        #expect(PlaceTiles.tiles(covering: top, zoom: 3, margin: 1).allSatisfy { $0.y >= 0 && $0.y < n })
    }

    @Test func atMost64ARequest() {
        let tiles = (0..<130).map { PlaceTileKey(zoom: 8, x: $0, y: 0) }
        let b = PlaceTiles.batches(tiles)
        #expect(b.map(\.count) == [64, 64, 2])
        #expect(b.flatMap { $0 } == tiles)
        #expect(PlaceTiles.batches([]).isEmpty)
    }

    @Test func anAreaAcrossTheAntimeridianIsOneRect() {
        var a = Msg_PlaceArea()
        a.south = -18; a.north = -16; a.west = 177; a.east = -179
        let r = PlaceTiles.rect(of: a)
        // 4 degrees wide, east of the world's edge.
        #expect(abs(PlaceTiles.lonSpan(of: r) - 4) < 1e-6)
        #expect(r.maxX > MKMapSize.world.width)
        // A single spot is framed about a town across.
        var spot = Msg_PlaceArea()
        spot.south = 40.4; spot.north = 40.4; spot.west = -3.7; spot.east = -3.7
        let f = PlaceTiles.framing(spot, minWidth: PlaceTiles.townWidth)
        #expect(abs(f.size.width - PlaceTiles.townWidth) < 1)
        #expect(f.contains(MKMapPoint(CLLocationCoordinate2D(latitude: 40.4, longitude: -3.7))))
    }

    /// Photos all over the world open on the world, its middle where most
    /// of them are - not on the smallest area holding them, which runs
    /// across the Pacific.
    @Test func photosAllOverTheWorldOpenOnTheWorldWhereMostAre() {
        // Tokyo eastwards to Nairobi, across the antimeridian: 258°.
        let wide = area(-34.6, 139.7, 64.1, 36.8)
        let world = [place(40.4, -3.7, count: 200, cover: nil, zoom: 0), place(35.7, 139.7, count: 15, cover: nil, zoom: 0)]
        let middle = PlaceTiles.middle(of: world)
        #expect(middle == -3.7)
        let r = PlaceTiles.opening(wide, middle: middle, minWidth: PlaceTiles.townWidth)
        #expect(abs(r.size.width - MKMapSize.world.width) < 1)
        let centre = MKMapPoint(x: r.midX.truncatingRemainder(dividingBy: MKMapSize.world.width), y: r.midY).coordinate
        #expect(abs(centre.longitude - -3.7) < 1e-6)
        #expect(abs(centre.latitude - 25) < 1e-6)
        #expect(r.minX >= 0 && r.minX < MKMapSize.world.width)
        // Before zoom 0's answer: 10° E.
        let early = PlaceTiles.opening(wide, middle: nil, minWidth: PlaceTiles.townWidth)
        #expect(abs(MKMapPoint(x: early.midX, y: early.midY).coordinate.longitude - 10) < 1e-6)
        // A continent is framed as before.
        let europe = area(36, -9.5, 64.2, 37.6)
        #expect(MKMapRectEqualToRect(PlaceTiles.opening(europe, middle: middle, minWidth: PlaceTiles.townWidth), PlaceTiles.framing(europe, minWidth: PlaceTiles.townWidth)))
        #expect(PlaceTiles.middle(of: []) == nil)
    }
}

// MARK: - Pins and taps

private func file(_ hash: String, _ day: Double, video: Bool = false) -> Msg_File {
    var f = Msg_File()
    f.hash = hash
    f.path = "/p/\(hash).jpg"
    f.mime = video ? "video/mp4" : "image/jpeg"
    f.created = Google_Protobuf_Timestamp(date: Date(timeIntervalSince1970: 1_780_000_000 + day * 86400))
    return f
}

private func area(_ s: Double, _ w: Double, _ n: Double, _ e: Double) -> Msg_PlaceArea {
    var a = Msg_PlaceArea()
    a.south = s; a.west = w; a.north = n; a.east = e
    return a
}

private func place(_ lat: Double, _ lon: Double, count: Int, cover: Msg_File?, zoom: Int = 10) -> Msg_PhotoPlace {
    var p = Msg_PhotoPlace()
    p.latitude = lat
    p.longitude = lon
    p.count = Int32(count)
    p.area = area(lat - 0.001, lon - 0.001, lat + 0.001, lon + 0.001)
    let t = PlaceTiles.tile(latitude: lat, longitude: lon, zoom: zoom)
    p.zoom = Int32(zoom)
    p.x = Int32(t.x)
    p.y = Int32(t.y)
    if let cover { p.cover = cover }
    return p
}

struct PlacePinTests {
    @Test func placesTooCloseOnTheScreenAreOnePin() {
        let a = PlacePin(place(40.4168, -3.7038, count: 3, cover: file("a", 1)))
        let b = PlacePin(place(40.4170, -3.7036, count: 5, cover: file("b", 9)))
        let far = PlacePin(place(41.3874, 2.1686, count: 2, cover: file("c", 3)))
        let pins = PinMerge.merge([a, b, far], zoom: 10)
        #expect(pins.count == 2)
        let merged = try! #require(pins.first { $0.count == 8 })
        // The newest photo's thumbnail, and its id: the pin keeps its place
        // across zooms while its cover stays the newest.
        #expect(merged.cover?.hash == "b")
        #expect(merged.id == b.id)
        // At one of them, never between: the bigger one, nearer their
        // weighted mean (the mean can be where no photo was taken).
        #expect(merged.latitude == b.latitude && merged.longitude == b.longitude)
        // The area holding both.
        #expect(merged.area.south == a.area.south && merged.area.north == b.area.north)
        #expect(merged.area.west == a.area.west && merged.area.east == b.area.east)
        #expect(pins.contains(far))
    }

    @Test func noTwoPinsAreLeftCloserThanAPin() {
        // A line of places a bit more than half a pin apart: the biggest
        // takes in its neighbours, moves, and comes near the next ones.
        let z = 12.0
        let step = PinMerge.gap * 0.6 * MKMapSize.world.width / (256 * pow(2, z))
        let start = MKMapPoint(CLLocationCoordinate2D(latitude: 40.4, longitude: -3.7))
        let line = (0..<12).map { i -> PlacePin in
            let c = MKMapPoint(x: start.x + Double(i) * step, y: start.y).coordinate
            return PlacePin(place(c.latitude, c.longitude, count: i == 5 ? 9 : 1 + i % 3, cover: file("h\(i)", Double(i))))
        }
        let pins = PinMerge.merge(line, zoom: z)
        #expect(pins.reduce(0) { $0 + $1.count } == line.reduce(0) { $0 + $1.count })
        let perPoint = 256 * pow(2, z) / MKMapSize.world.width
        for (i, a) in pins.enumerated() {
            for b in pins[(i + 1)...] {
                let d = hypot(a.mapPoint.x - b.mapPoint.x, a.mapPoint.y - b.mapPoint.y) * perPoint
                #expect(d >= PinMerge.gap, "\(d)")
            }
        }
    }

    @Test func zoomedInTheySplit() {
        let a = PlacePin(place(40.4168, -3.7038, count: 3, cover: file("a", 1)))
        let b = PlacePin(place(40.4268, -3.7038, count: 5, cover: file("b", 9)))
        #expect(PinMerge.merge([a, b], zoom: 8).count == 1)
        #expect(PinMerge.merge([a, b], zoom: 15).count == 2)
    }

    @Test func whatVoiceOverReads() {
        let one = PlacePin(place(1, 1, count: 1, cover: file("a", 0)))
        #expect(one.label == PhotoMonths.tileLabel(video: false, date: Date(timeIntervalSince1970: 1_780_000_000)))
        let video = PlacePin(place(1, 1, count: 1, cover: file("v", 0, video: true)))
        #expect(video.label.hasPrefix("Video"))
        #expect(PlacePin(place(1, 1, count: 12, cover: file("b", 0))).label == "12 items here")
        #expect(S.appPhotosPlacesNone == "No photos with a location yet")
        #expect(S.appPhotosPlacesAreaChip == "Map area")
        #expect(S.appShellSectionPlaces == "Places")
    }

    @Test func aTapOpensOneZoomsIntoManyAndShowsTheClosestInImages() {
        let view = MKMapRect(x: 0, y: 0, width: MKMapSize.world.width / 64, height: MKMapSize.world.height / 64)
        // One photo: the viewer.
        let one = PlacePin(place(40.4, -3.7, count: 1, cover: file("a", 0)))
        guard case .open(let f) = PlaceTap.of(one, zoom: 6, view: view) else { Issue.record("not opened"); return }
        #expect(f.hash == "a")
        // A group: closer, at least twice.
        let many = PlacePin(place(40.4, -3.7, count: 9, cover: file("b", 0)))
        guard case .zoom(let r) = PlaceTap.of(many, zoom: 6, view: view) else { Issue.record("not zoomed"); return }
        #expect(r.size.width <= view.size.width / 2)
        #expect(r.contains(many.mapPoint))
        // Zoomed in far already, or photos a few metres apart: Images.
        guard case .showInImages(let a) = PlaceTap.of(many, zoom: 16.2, view: view) else { Issue.record("not shown"); return }
        #expect(a == many.area)
        let same = PlacePin(id: "x", latitude: 40.4, longitude: -3.7, count: 4, cover: file("c", 0),
                            area: area(40.4, -3.7, 40.40001, -3.70001))
        guard case .showInImages = PlaceTap.of(same, zoom: 8, view: view) else { Issue.record("not shown"); return }
    }
}

// MARK: - The answers kept

struct PlacesCacheTests {
    private func answer(_ places: [Msg_PhotoPlace], zoom: Int, total: Int = 7) -> Msg_PhotoPlaces {
        var a = Msg_PhotoPlaces()
        a.places = places
        a.zoom = Int32(zoom)
        a.locatedTotal = Int32(total)
        a.locatedArea = area(40, -4, 42, 3)
        return a
    }

    @Test func everyTileAskedForIsKeptTheEmptyOnesToo() {
        var c = PlacesCache()
        let f = PlacesFilter()
        let p1 = place(40.4168, -3.7038, count: 3, cover: file("a", 1))
        let p2 = place(41.3874, 2.1686, count: 2, cover: file("b", 1))
        let t1 = PlaceTileKey(zoom: 10, x: Int(p1.x), y: Int(p1.y))
        let t2 = PlaceTileKey(zoom: 10, x: Int(p2.x), y: Int(p2.y))
        let empty = PlaceTileKey(zoom: 10, x: 3, y: 3)
        let unasked = PlaceTileKey(zoom: 10, x: 4, y: 4)
        #expect(c.missing([t1, t2, empty], for: f) == [t1, t2, empty])
        c.put(answer([p1, p2], zoom: 10), asked: [t1, t2, empty], for: f)
        #expect(c.places(in: t1, for: f)?.count == 1)
        #expect(c.places(in: t2, for: f)?.first?.cover.hash == "b")
        #expect(c.places(in: empty, for: f)?.isEmpty == true)
        #expect(c.missing([t1, unasked, empty, t2], for: f) == [unasked])
        #expect(c.extent(for: f)?.total == 7)
        // Another search's are its own.
        let person = PlacesFilter(people: ["p1"])
        #expect(c.places(in: t1, for: person) == nil)
        #expect(c.extent(for: person) == nil)
    }

    @Test func keptFiveMinutesOrUntilTheLibraryChanges() {
        var c = PlacesCache()
        let f = PlacesFilter(tags: ["beach"])
        let t = PlaceTileKey(zoom: 0, x: 0, y: 0)
        let then = Date()
        c.put(answer([], zoom: 0, total: 0), asked: [t], for: f, now: then)
        #expect(c.places(in: t, for: f, now: then + 299) != nil)
        #expect(c.places(in: t, for: f, now: then + 301) == nil)
        c.clearTiles()
        #expect(c.places(in: t, for: f, now: then) == nil)
        // The extent stays, so the map opens framed as before.
        #expect(c.extent(for: f) == PlacesExtent(answer([], zoom: 0, total: 0)))
        #expect(c.extent(for: f)?.area == nil)
        c.clear()
        #expect(c.extent(for: f) == nil)
    }

    @Test func theRequestCarriesTheSearch() {
        let f = PlacesFilter(tags: ["dog"], people: ["p1", "p2"], group: "g1")
        let r = f.request(zoom: 7, tiles: [PlaceTileKey(zoom: 7, x: 3, y: 4)])
        #expect(r.zoom == 7)
        #expect(r.tiles.map { [$0.x, $0.y] } == [[3, 4]])
        #expect(r.tags == ["dog"] && r.personIds == ["p1", "p2"] && r.groupID == "g1")
        // Videos as the grid shows them.
        #expect(r.includeVideos)
        #expect(!f.isEmpty && PlacesFilter().isEmpty)
    }

    /// Places shows only while the device's Settings say it is on; a
    /// device before Places (no places_enabled) has none.
    @Test func whatTheQuestionsAnswerSays() {
        func settings(_ on: Bool?) -> Msg_RespEnvelope {
            var s = Msg_Settings()
            if let on { s.placesEnabled = on }
            var r = Msg_RespEnvelope()
            r.payload = .respSettings(s)
            return r
        }
        #expect(PlacesSupport.verdict(settings(true)) == true)
        #expect(PlacesSupport.verdict(settings(false)) == false)
        #expect(PlacesSupport.verdict(settings(nil)) == false)
        var other = Msg_RespEnvelope()
        other.error = true
        other.errorMessage = "busy"
        #expect(PlacesSupport.verdict(other) == nil)
        #expect(PlacesSupport.verdict(nil) == nil)
    }

    /// While the device reads positions, its answers are kept 15 s, not 5
    /// minutes, and the extent says so.
    @Test func readingAnswersAgeSooner() {
        var c = PlacesCache()
        let f = PlacesFilter()
        let t = PlaceTileKey(zoom: 0, x: 0, y: 0)
        let then = Date()
        var a = answer([], zoom: 0, total: 0)
        a.reading = true
        c.put(a, asked: [t], for: f, now: then)
        #expect(c.extent(for: f)?.reading == true)
        #expect(c.places(in: t, for: f, now: then + PlacesCache.readingTTL - 1) != nil)
        #expect(c.places(in: t, for: f, now: then + PlacesCache.readingTTL + 1) == nil)
        a.reading = false
        c.put(a, asked: [t], for: f, now: then)
        #expect(c.extent(for: f)?.reading == false)
        #expect(c.places(in: t, for: f, now: then + 60) != nil)
    }
}

// MARK: - The model, and Images' area

@MainActor
private func waitUntil(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

/// The device, for the map: answers GetPhotoPlaces from a few places at
/// zoom 10, and counts what it is asked.
@MainActor
final class PlacesDevice {
    var asks: [Msg_GetPhotoPlaces] = []
    var fail = false
    /// Places turned off (on another app): every ask is told so.
    var off = false
    let places: [(Double, Double, Int)] = [(40.4168, -3.7038, 3), (40.4200, -3.7000, 2), (41.3874, 2.1686, 4)]

    func answer(_ r: Msg_GetPhotoPlaces) -> Msg_RespEnvelope {
        asks.append(r)
        var resp = Msg_RespEnvelope()
        if fail {
            resp.error = true
            resp.errorMessage = "busy"
            return resp
        }
        if off {
            resp.error = true
            resp.errorCode = "places_off"
            resp.errorMessage = "Places is off"
            return resp
        }
        var a = Msg_PhotoPlaces()
        a.zoom = r.zoom
        a.locatedTotal = 9
        a.locatedArea = area(40.4, -3.71, 41.39, 2.17)
        let z = Int(r.zoom)
        let asked = Set(r.tiles.map { PlaceTileKey(zoom: z, x: Int($0.x), y: Int($0.y)) })
        for (i, p) in places.enumerated() {
            let t = PlaceTiles.tile(latitude: p.0, longitude: p.1, zoom: z)
            guard asked.contains(t) else { continue }
            var pl = Msg_PhotoPlace()
            pl.latitude = p.0
            pl.longitude = p.1
            pl.count = Int32(p.2)
            pl.area = area(p.0, p.1, p.0, p.1)
            pl.zoom = Int32(z); pl.x = Int32(t.x); pl.y = Int32(t.y)
            pl.cover = file("h\(i)", Double(i))
            a.places.append(pl)
        }
        resp.payload = .respPhotoPlaces(a)
        return resp
    }

    func attach(_ m: PlacesModel) {
        m.settle = 0.01
        m.prefetchAfter = 0.05
        m.retry.delay = { _ in 0.05 }
        m.thumbs.request = { _ in throw CancellationError() }
        m.request = { [weak self] payload in
            guard let self, case .reqGetPhotoPlaces(let r) = payload else { throw CancellationError() }
            return self.answer(r)
        }
    }
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct PlacesModelTests {
    /// Madrid and around, on a phone 390 points wide, at about zoom 10.
    private var madrid: MKMapRect {
        let c = MKMapPoint(CLLocationCoordinate2D(latitude: 40.418, longitude: -3.702))
        let w = MKMapSize.world.width / Double(1 << 10) * (390.0 / 256)
        return MKMapRect(x: c.x - w / 2, y: c.y - w, width: w, height: 2 * w)
    }

    @Test func theExtentFirstThenTheScreensTilesThenTheNextZooms() async {
        let m = PlacesModel(thumbCache: testThumbCache())
        let device = PlacesDevice()
        device.attach(m)
        m.appeared(filter: PlacesFilter())
        await waitUntil { m.extent != nil }
        #expect(device.asks.first?.zoom == 0)
        #expect(device.asks.first?.tiles.count == 1)
        #expect(m.extent?.total == 9)
        m.cameraChanged(rect: madrid, width: 390)
        #expect(m.zoom == 10)
        await waitUntil { m.pins.count == 1 }
        // Madrid's two places are one pin at this zoom; Barcelona is off
        // the screen.
        #expect(m.pins.first?.count == 5)
        let z10 = device.asks.filter { $0.zoom == 10 }
        #expect(z10.count == 1)
        #expect(z10[0].tiles.count <= PlaceTiles.batchSize)
        #expect(z10[0].includeVideos)
        // Once it rests, the next zoom's, for a zoom in.
        await waitUntil { device.asks.contains { $0.zoom == 11 } }
        #expect(device.asks.contains { $0.zoom == 11 })
        // Back to where it was: nothing asked again.
        let count = device.asks.count
        m.cameraChanged(rect: madrid, width: 390)
        try? await Task.sleep(for: .milliseconds(150))
        #expect(device.asks.count == count)
        #expect(m.pins.first?.count == 5)
    }

    /// Places turned off on another app: the map is told "places_off" and
    /// goes, with no banner to try again.
    @Test func aMapToldPlacesOffGoes() async {
        let m = PlacesModel(thumbCache: testThumbCache())
        let device = PlacesDevice()
        device.attach(m)
        device.off = true
        PlacesSupport.shared.set(true)
        m.appeared(filter: PlacesFilter())
        await waitUntil { PlacesSupport.shared.supported == false }
        #expect(PlacesSupport.shared.supported == false)
        #expect(!m.problem)
        PlacesSupport.shared.reset()
    }

    @Test func offTheScreenNothingIsAsked() async {
        let m = PlacesModel(thumbCache: testThumbCache())
        let device = PlacesDevice()
        device.attach(m)
        // A delete in the grid before the map was ever shown.
        m.libraryChanged()
        m.setFilter(PlacesFilter(tags: ["dog"]))
        try? await Task.sleep(for: .milliseconds(100))
        #expect(device.asks.isEmpty)
        m.appeared(filter: PlacesFilter())
        await waitUntil { m.extent != nil }
        m.cameraChanged(rect: madrid, width: 390)
        await waitUntil { !m.pins.isEmpty }
        m.disappeared()
        let n = device.asks.count
        m.libraryChanged()
        try? await Task.sleep(for: .milliseconds(150))
        #expect(device.asks.count == n)
    }

    @Test func aFailureSaysSoAndIsAskedAgain() async {
        let m = PlacesModel(thumbCache: testThumbCache())
        let device = PlacesDevice()
        device.attach(m)
        device.fail = true
        m.appeared(filter: PlacesFilter())
        await waitUntil { m.problem }
        #expect(m.problem)
        device.fail = false
        await waitUntil { m.extent != nil }
        #expect(!m.problem)
        #expect(device.asks.count >= 2)
    }

    @Test func anotherSearchDrawsOnlyItsOwnPins() async {
        let m = PlacesModel(thumbCache: testThumbCache())
        let device = PlacesDevice()
        device.attach(m)
        m.appeared(filter: PlacesFilter())
        await waitUntil { m.extent != nil }
        m.cameraChanged(rect: madrid, width: 390)
        await waitUntil { !m.pins.isEmpty }
        m.setFilter(PlacesFilter(people: ["p1"]))
        #expect(m.pins.isEmpty)
        #expect(m.extent == nil)
        await waitUntil { !m.pins.isEmpty }
        #expect(device.asks.last?.personIds == ["p1"])
    }

    /// Images narrowed to a place's area: every page and the month counts
    /// carry it; × takes it away.
    @Test func theMapsAreaGoesWithImagesRequests() async {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        var searches: [Msg_SearchPhotos] = []
        var buckets: [Msg_ReqPhotoDateBuckets] = []
        vm.photoRequest = { payload, _ in
            var r = Msg_RespEnvelope()
            switch payload {
            case .reqSearchPhotos(let s):
                searches.append(s)
                r.payload = .respListOfFiles(Msg_ListOfFiles())
            case .reqPhotoDateBuckets(let b):
                buckets.append(b)
                r.payload = .respPhotoDateBuckets(Msg_RespPhotoDateBuckets())
            default:
                throw CancellationError()
            }
            return r
        }
        vm.libraryRequest = { _, _ in throw CancellationError() }
        vm.peopleRequest = { _, _ in throw CancellationError() }
        vm.showMap = true
        let a = area(40.41, -3.71, 40.42, -3.70)
        vm.showArea(a)
        #expect(!vm.showMap)
        await waitUntil { !searches.isEmpty && !buckets.isEmpty }
        #expect(searches.last?.area == a)
        #expect(buckets.last?.area == a)
        #expect(vm.emptyKind == .noMatch)
        // The map shows the search without the area.
        #expect(vm.placesFilter == PlacesFilter())
        let n = searches.count
        vm.leaveArea()
        await waitUntil { searches.count > n }
        #expect(searches.last?.hasArea == false)
        // Images from the menu: no area either.
        vm.showArea(a)
        await waitUntil { searches.last?.hasArea == true }
        vm.showAll()
        #expect(vm.activeArea == nil)
    }
}
}
