// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import CoreGraphics
import UIKit
import Testing
@testable import OffTheCloud

private func calendar(_ zone: String) -> Calendar {
    var c = Calendar(identifier: .gregorian)
    c.timeZone = TimeZone(identifier: zone)!
    return c
}

private func utc(_ s: String) -> Date {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime]
    return f.date(from: s)!
}

/// Images by month (PhotoMonths.swift), as the web's PhotoGallery.tsx
/// works out its month sections.
struct PhotoMonthsTests {
    @Test func monthKeysAreInTheViewersTimeZone() {
        // 23:30 UTC on the last day of March is already April in Madrid,
        // and still March in New York - the web's monthOf, in the
        // browser's zone.
        let d = utc("2024-03-31T23:30:00Z")
        #expect(PhotoMonths.key(for: d, calendar: calendar("UTC")) == "2024-03")
        #expect(PhotoMonths.key(for: d, calendar: calendar("Europe/Madrid")) == "2024-04")
        #expect(PhotoMonths.key(for: d, calendar: calendar("America/New_York")) == "2024-03")
        #expect(PhotoMonths.key(for: utc("0999-01-15T12:00:00Z"), calendar: calendar("UTC")) == "0999-01")
        // No date, no month.
        #expect(PhotoMonths.key(for: nil) == nil)
    }

    @Test func monthsAreGregorianWhateverCalendarThePhoneIsSetTo() {
        // 5 October 2026 is "2026-10" on the web and in the device's date
        // buckets: a phone set to another calendar (Thailand's default is
        // the Buddhist one) must say so too, or no title matches a bucket
        // and the scrubber's jump searches the wrong millennium.
        let d = utc("2026-10-05T12:00:00Z")
        for id: Calendar.Identifier in [.gregorian, .buddhist, .japanese, .hebrew, .islamicUmmAlQura, .persian, .chinese] {
            var c = Calendar(identifier: id)
            c.timeZone = TimeZone(identifier: "Europe/Madrid")!
            #expect(PhotoMonths.key(for: d, calendar: c) == "2026-10", "\(id)")
            // Its time zone still counts: 23:30 UTC on 31 March is April in Madrid.
            #expect(PhotoMonths.key(for: utc("2024-03-31T23:30:00Z"), calendar: c) == "2024-04", "\(id)")
        }
        #expect(PhotoMonths.calendar().identifier == .gregorian)
        #expect(PhotoMonths.calendar(in: TimeZone(identifier: "Asia/Bangkok")!).timeZone.identifier == "Asia/Bangkok")
    }

    @Test func aJumpStartsAtTheMonthsLastInstant() {
        // The web's new Date(y, m, 0, 23, 59, 59, 999), in the viewer's zone.
        let madrid = calendar("Europe/Madrid")
        #expect(PhotoMonths.lastInstant(of: "2024-01", calendar: madrid) == utc("2024-01-31T23:00:00Z").addingTimeInterval(-0.001))
        // Summer time, and a leap year's February.
        #expect(PhotoMonths.lastInstant(of: "2024-06", calendar: madrid) == utc("2024-06-30T22:00:00Z").addingTimeInterval(-0.001))
        #expect(PhotoMonths.lastInstant(of: "2024-02", calendar: calendar("UTC")) == utc("2024-03-01T00:00:00Z").addingTimeInterval(-0.001))
        #expect(PhotoMonths.lastInstant(of: "2026-12", calendar: calendar("UTC")) == utc("2027-01-01T00:00:00Z").addingTimeInterval(-0.001))
        // A phone on the Japanese or Buddhist calendar: the same instant,
        // not year 2024 of its era (4044, or 1481).
        for id: Calendar.Identifier in [.japanese, .buddhist, .hebrew] {
            var c = Calendar(identifier: id)
            c.timeZone = TimeZone(identifier: "Europe/Madrid")!
            #expect(PhotoMonths.lastInstant(of: "2024-01", calendar: c) == PhotoMonths.lastInstant(of: "2024-01", calendar: madrid), "\(id)")
        }
        // Its photos are of that month, the next instant's of the next.
        let end = PhotoMonths.lastInstant(of: "2024-01", calendar: madrid)!
        #expect(PhotoMonths.key(for: end, calendar: madrid) == "2024-01")
        #expect(PhotoMonths.key(for: end.addingTimeInterval(0.001), calendar: madrid) == "2024-02")
        #expect(PhotoMonths.lastInstant(of: "2024-13") == nil)
        #expect(PhotoMonths.lastInstant(of: "") == nil)
    }

    @Test func tilesReadAsTheWebsDo() {
        let madrid = TimeZone(identifier: "Europe/Madrid")!
        #expect(PhotoMonths.tileLabel(video: false, date: utc("2026-10-05T12:00:00Z"), timeZone: madrid) == "Photo, 5 October 2026")
        #expect(PhotoMonths.tileLabel(video: true, date: utc("2024-03-31T23:30:00Z"), timeZone: madrid) == "Video, 1 April 2024")
        #expect(PhotoMonths.tileLabel(video: true, date: utc("2024-03-31T23:30:00Z"), timeZone: TimeZone(identifier: "UTC")!) == "Video, 31 March 2024")
        #expect(PhotoMonths.tileLabel(video: false, date: nil) == "Photo")
        #expect(PhotoMonths.tileLabel(video: true, date: nil) == "Video")
    }

    @Test func peopleAreNamedAsTheWebsHeader() {
        #expect(PhotoMonths.peopleTitle(["Ana"]) == "Ana")
        #expect(PhotoMonths.peopleTitle(["Ana", "Luis"]) == "Ana & Luis")
        #expect(PhotoMonths.peopleTitle(["Ana", "Luis", "Rosa"]) == "Ana, Luis & 1 other")
        #expect(PhotoMonths.peopleTitle(["Ana", "Luis", "Rosa", "Unnamed"]) == "Ana, Luis & 2 others")
    }

    @Test func titlesAreTheWebs() {
        #expect(PhotoMonths.title("2024-03") == "March 2024")
        #expect(PhotoMonths.title("2026-09") == "September 2026")
        #expect(PhotoMonths.short("2026-09") == "Sep 2026")
        #expect(PhotoMonths.short("2024-12") == "Dec 2024")
        // Anything else is shown as it came.
        #expect(PhotoMonths.title("0000-00") == "0000-00")
    }

    @Test func undatedPhotosStayWithTheMonthBefore() {
        let runs = PhotoMonths.runs(["2024-05", "2024-05", nil, "2024-05", "2024-04", nil, nil, "2024-01"])
        #expect(runs == [
            .init(month: "2024-05", start: 0, end: 4),
            .init(month: "2024-04", start: 4, end: 7),
            .init(month: "2024-01", start: 7, end: 8),
        ])
        // At the very top they have no month before them: a run of their own.
        let top = PhotoMonths.runs([nil, nil, "2024-05", nil])
        #expect(top == [.init(month: nil, start: 0, end: 2), .init(month: "2024-05", start: 2, end: 4)])
        #expect(PhotoMonths.runs([]).isEmpty)
        // A month seen again further down (a restarted search) is a new run:
        // the photos are never moved.
        #expect(PhotoMonths.runs(["2024-05", "2024-04", "2024-05"]).count == 3)
    }

    @Test func spanAndCountWording() {
        #expect(PhotoMonths.span(["2024-03"]) == "Mar 2024")
        #expect(PhotoMonths.span(["2024-10", "2024-07", "2024-03"]) == "Mar – Oct 2024")
        #expect(PhotoMonths.span(["2026-10", "2024-03"]) == "Mar 2024 – Oct 2026")
        #expect(PhotoMonths.span(["2026-10", "2024-03"], years: true) == "2024 – 2026")
        #expect(PhotoMonths.span(["2024-10", "2024-03"], years: true) == "Mar – Oct 2024")
        #expect(PhotoMonths.span([]) == "")
        #expect(PhotoMonths.count(1, "item") == "1 item")
        #expect(PhotoMonths.count(0, "item") == "0 items")
        #expect(PhotoMonths.count(1234, "item") == "1,234 items")
    }

    @Test func scrubberTargetsTheMonthUnderIt() {
        // 10 + 30 + 60 photos: a month takes as much track as it has photos.
        let counts = [10, 30, 60]
        #expect(PhotoMonths.bucketIndex(atFraction: 0, counts: counts) == 0)
        #expect(PhotoMonths.bucketIndex(atFraction: 0.099, counts: counts) == 0)
        #expect(PhotoMonths.bucketIndex(atFraction: 0.1, counts: counts) == 1)
        #expect(PhotoMonths.bucketIndex(atFraction: 0.39, counts: counts) == 1)
        #expect(PhotoMonths.bucketIndex(atFraction: 0.4, counts: counts) == 2)
        // The very bottom, and past it, is the oldest.
        #expect(PhotoMonths.bucketIndex(atFraction: 1, counts: counts) == 2)
        #expect(PhotoMonths.bucketIndex(atFraction: 1.5, counts: counts) == 2)
        #expect(PhotoMonths.bucketIndex(atFraction: -1, counts: counts) == 0)
        #expect(PhotoMonths.bucketIndex(atFraction: 0.5, counts: []) == nil)
        #expect(PhotoMonths.bucketIndex(atFraction: 0.5, counts: [0, 0]) == nil)
    }
}

/// The grid's sizes and rows (PhotoGridMetrics, PhotoGridLayout), as
/// PhotoGallery.css lays out its months.
struct PhotoGridLayoutTests {
    private let phone = PhotoGridMetrics(width: 402, windowWidth: 402)

    private func ids(_ n: Int) -> [String] { (0..<n).map { "p\($0)" } }

    /// The months repeated: `spec` is [(month, photos)].
    private func months(_ spec: [(String?, Int)]) -> [String?] {
        spec.flatMap { Array(repeating: $0.0, count: $0.1) }
    }

    private func kinds(_ l: PhotoGridLayout) -> [String] {
        l.rows.map { row in
            switch row.kind {
            case .title(let m): return "title \(m ?? "-")"
            case .tiles(let r): return "tiles \(r.lowerBound)..<\(r.upperBound)"
            case .line(let ps): return "line " + ps.map { "\($0.month ?? "-"):\($0.tiles.count)/\($0.room)" }.joined(separator: " ")
            }
        }
    }

    @Test func metricsAreTheWebs() {
        // A phone: three across, 8 pt padding, 2 pt between tiles.
        #expect(phone.phone && phone.cols == 3)
        #expect(phone.pad == 8 && phone.gap == 2 && phone.secGap == 2)
        #expect(abs(phone.tile - (402 - 16 - 4) / 3) < 0.001)
        #expect(abs(phone.sectionWidth(3) - phone.content) < 0.001)
        #expect(phone.sectionWidth(9) == phone.content)
        // A phone on its side (the wide layout): as many 120 pt tiles as fit.
        let side = PhotoGridMetrics(width: 794, windowWidth: 874)
        #expect(!side.phone)
        #expect(side.cols == Int((794 - 32 + 4) / (120 + 4)))
        #expect(side.gap == 4 && side.secGap == 12 && side.pad == 16)
        #expect(abs(CGFloat(side.cols) * side.tile + CGFloat(side.cols - 1) * side.gap - side.content) < 0.001)
        // From 1024: 150 pt tiles, more room between months.
        let big = PhotoGridMetrics(width: 1100, windowWidth: 1366)
        #expect(big.cols == Int((1100 - 32 + 4) / (150 + 4)))
        #expect(big.secGap == 16)
    }

    @Test func thumbnailsAreDecodedForTheBiggestTile() {
        // Every iPhone upright (3x): its tiles are never shown bigger than
        // they were decoded for.
        for w: CGFloat in [320, 375, 390, 393, 402, 414, 420, 430, 440] {
            let m = PhotoGridMetrics(width: w, windowWidth: w)
            #expect(m.tile <= PhotoGridMetrics.decodeSide, "\(w)")
        }
        // On its side, the menu beside the grid or not.
        for (grid, window): (CGFloat, CGFloat) in [(874, 874), (794, 874), (956, 956), (876, 956), (667, 667), (600, 600)] {
            let m = PhotoGridMetrics(width: grid, windowWidth: window)
            #expect(m.tile <= PhotoGridMetrics.decodeSide + 1, "\(grid) in \(window)")
        }
        // An iPad (2x): 150 pt tiles and wider, up to 1.5 times the size.
        for w: CGFloat in [1024, 1180, 1366, 744, 820] {
            let m = PhotoGridMetrics(width: w - 80, windowWidth: w)
            #expect(m.tile * 2 <= PhotoGridMetrics.decodeSide * 3, "\(w)")
        }
    }

    @Test func theBiggestPhonesTilesGetThumbnailsAsBigAsThey() {
        // The device's thumbnails are 1000 px on their long side: decoded for
        // a 440 pt phone's 140 pt tile, a 4:3 one keeps 3 pixels per point
        // across its short side, which the tile shows whole.
        let tile = PhotoGridMetrics(width: 440, windowWidth: 440).tile
        let jpeg = UIGraphicsImageRenderer(size: CGSize(width: 1000, height: 750), format: {
            let f = UIGraphicsImageRendererFormat()
            f.scale = 1
            return f
        }()).jpegData(withCompressionQuality: 0.8) { ctx in
            UIColor.orange.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 1000, height: 750))
        }
        let img = GridThumbCache.decode(data: jpeg, localURL: nil, maxPt: PhotoGridMetrics.decodeSide)
        let px = img.map { min($0.size.width, $0.size.height) * $0.scale } ?? 0
        #expect(px >= tile * 3, "\(px) px for a \(tile) pt tile")
    }

    @Test func monthsShortOfARowShareALine() {
        // 1 + 2 photos fill a phone's row of three; a third month moves on.
        let m = months([("2026-08", 1), ("2026-07", 2), ("2026-06", 1), ("2026-05", 5), ("2026-04", 2)])
        let l = PhotoGridLayout.build(months: m, ids: ids(m.count), dated: true, lastRoom: nil, metrics: phone)
        #expect(kinds(l) == [
            "line 2026-08:1/1 2026-07:2/2",
            "line 2026-06:1/1",
            "title 2026-05",
            "tiles 4..<7",
            "tiles 7..<9",
            "line 2026-04:2/2",
        ])
        // The room above each: the grid's top, a line's gap, the tiles' gap.
        #expect(l.rows.map(\.top) == [phone.top, phone.lineGap, phone.lineGap, phone.gap, phone.gap, phone.lineGap])
    }

    @Test func widerGapsBetweenMonthsKeepThemApart() {
        // Five across: two 2-photo months fit side by side 12 pt apart, a
        // 2- and a 3-photo month don't.
        let m5 = PhotoGridMetrics(width: 16 * 2 + 5 * 124 - 4, windowWidth: 700)
        #expect(m5.cols == 5)
        let m = months([("2026-08", 2), ("2026-07", 2), ("2026-06", 2), ("2026-05", 3)])
        let l = PhotoGridLayout.build(months: m, ids: ids(m.count), dated: true, lastRoom: nil, metrics: m5)
        #expect(kinds(l) == ["line 2026-08:2/2 2026-07:2/2", "line 2026-06:2/2", "line 2026-05:3/3"])
    }

    @Test func noTitlesWhenOrderedByScore() {
        let m = months([("2026-08", 1), ("2026-07", 4), (nil, 2)])
        let l = PhotoGridLayout.build(months: m, ids: ids(m.count), dated: false, lastRoom: nil, metrics: phone)
        #expect(kinds(l) == ["tiles 0..<3", "tiles 3..<6", "tiles 6..<7"])
        #expect(l.rows.first?.top == phone.top)
    }

    @Test func undatedPhotosAtTheTopHaveAnEmptyTitle() {
        let m = months([(nil, 1), ("2026-07", 1), (nil, 1)])
        let l = PhotoGridLayout.build(months: m, ids: ids(m.count), dated: true, lastRoom: nil, metrics: phone)
        #expect(kinds(l) == ["line -:1/1 2026-07:2/2"])
    }

    @Test func theLastMonthTakesTheRoomItsBucketCounts() {
        // Two of the last month's 40 photos are in: it is laid out as a big
        // month already, so the next page fills it in instead of moving it.
        let m = months([("2026-08", 1), ("2026-07", 2)])
        let open = PhotoGridLayout.build(months: m, ids: ids(m.count), dated: true, lastRoom: 40, metrics: phone)
        #expect(kinds(open) == ["line 2026-08:1/1", "title 2026-07", "tiles 1..<3"])
        // Every page in: as it is.
        let done = PhotoGridLayout.build(months: m, ids: ids(m.count), dated: true, lastRoom: nil, metrics: phone)
        #expect(kinds(done) == ["line 2026-08:1/1 2026-07:2/2"])
        // Room for one more beside: kept for it.
        let one = PhotoGridLayout.build(months: months([("2026-08", 1), ("2026-07", 1)]), ids: ids(2), dated: true, lastRoom: 2, metrics: phone)
        #expect(kinds(one) == ["line 2026-08:1/1 2026-07:1/2"])
    }

    @Test func rowsKeepTheirIdsAsPagesArrive() {
        let first = months([("2026-08", 4), ("2026-07", 2)])
        let a = PhotoGridLayout.build(months: first, ids: ids(first.count), dated: true, lastRoom: 9, metrics: phone)
        let more = first + months([("2026-07", 7)])
        let b = PhotoGridLayout.build(months: more, ids: ids(more.count), dated: true, lastRoom: 9, metrics: phone)
        // Every row there was is still there, under the same id.
        #expect(Set(a.rows.map(\.id)).isSubset(of: Set(b.rows.map(\.id))))
        #expect(PhotoGridLayout.firstItem(ofRowID: b.rows[5].id) == 7)
    }

    @Test func scrollingBackToAPhotoFindsItsRow() {
        let m = months([("2026-08", 1), ("2026-07", 2), ("2026-05", 5)])
        let l = PhotoGridLayout.build(months: m, ids: ids(m.count), dated: true, lastRoom: nil, metrics: phone)
        // line(0..<3) | title(3) | tiles 3..<6 | tiles 6..<8
        #expect(l.rowIndex(forItem: 0) == 0)
        #expect(l.rowIndex(forItem: 2) == 0)
        // A month's first photo: its title.
        #expect(l.rowIndex(forItem: 3) == 1)
        #expect(l.rowIndex(forItem: 5) == 2)
        #expect(l.rowIndex(forItem: 7) == 3)
        #expect(l.rowIndex(forItem: 99) == 3)
        #expect(PhotoGridLayout.build(months: [], ids: [], dated: true, lastRoom: nil, metrics: phone).rowIndex(forItem: 0) == nil)
    }

    @Test func scrubberMonthShowsItsTitleOverGreyTiles() {
        let big = PhotoGridLayout.placeholder(.month("2019-06"), count: 7, metrics: phone)
        #expect(kinds(big) == ["title 2019-06", "tiles 0..<3", "tiles 3..<6", "tiles 6..<7"])
        let small = PhotoGridLayout.placeholder(.month("2019-06"), count: 2, metrics: phone)
        #expect(kinds(small) == ["line 2019-06:2/2"])
        let skeleton = PhotoGridLayout.placeholder(.skeleton(titled: true), count: PhotoGridLayout.skeletonTiles, metrics: phone)
        #expect(kinds(skeleton).first == "title -")
        #expect(skeleton.rows.count == 1 + 8)
        let untitled = PhotoGridLayout.placeholder(.skeleton(titled: false), count: 6, metrics: phone)
        #expect(kinds(untitled) == ["tiles 0..<3", "tiles 3..<6"])
    }
}

/// What PhotoGalleryVM hands the grid: titles only in date order, the date
/// buckets of the current filter only, the scrubber's month.
@MainActor
struct PhotoGalleryMonthsTests {
    private func item(_ i: Int, _ month: String?) -> PhotoGalleryVM.Item {
        PhotoGalleryVM.Item(id: "i\(i)", path: "/p/\(i).jpg", mime: "image/jpeg", size: 1, thumbData: nil,
                            localURL: nil, isLocalOnly: false, month: month)
    }

    private let phone = PhotoGridMetrics(width: 402, windowWidth: 402)

    @Test func tagsDropTheTitles() {
        let vm = PhotoGalleryVM()
        vm.items = [item(0, "2026-08"), item(1, "2026-07")]
        vm.endReached = true
        #expect(vm.gridLayout(phone).rows.count == 1)
        if case .line(let ps) = vm.gridLayout(phone).rows[0].kind {
            #expect(ps.map(\.month) == ["2026-08", "2026-07"])
        } else {
            Issue.record("not a line")
        }
        // A tag search is ordered by score: no titles.
        vm.chips = ["beach"]
        #expect(vm.gridLayout(phone).rows.map(\.kind) == [.tiles(0..<2)])
    }

    @Test func onlyThisFiltersBucketsSizeTheLastMonth() {
        let vm = PhotoGalleryVM()
        vm.items = [item(0, "2026-08"), item(1, "2026-07")]
        vm.endReached = false
        vm.setDateBuckets([("2026-08", 1), ("2026-07", 30)], key: vm.currentBucketKey)
        #expect(vm.showScrubber)
        #expect(vm.gridLayout(phone).rows.map(\.kind) == [
            .line([.init(month: "2026-08", tiles: 0..<1, room: 1)]),
            .title("2026-07"),
            .tiles(1..<2),
        ])
        // Another person picked: those counts aren't this filter's.
        vm.selectedPeople = ["ana"]
        #expect(!vm.showScrubber)
        #expect(vm.freshBuckets.isEmpty)
        #expect(vm.gridLayout(phone).rows.count == 1)
    }

    private func person(_ id: String, _ name: String) -> Msg_Person {
        var p = Msg_Person()
        p.id = id
        p.name = name
        return p
    }

    @Test func aPersonFilterShowsItsPeopleAndTheirCount() {
        let all = [person("a", "Ana"), person("b", "  "), person("c", "Leo")]
        typealias F = PhotoGalleryVM.PersonFilter
        // Nobody picked, or with tags or in a collection: no header, as the web's.
        #expect(F.of(selected: [], tags: [], group: nil, loaded: true, people: all, total: 3) == nil)
        #expect(F.of(selected: ["a"], tags: ["beach"], group: nil, loaded: true, people: all, total: 3) == nil)
        #expect(F.of(selected: ["a"], tags: [], group: "g", loaded: true, people: all, total: 3) == nil)
        // The list of people on its way: blanks.
        #expect(F.of(selected: ["a"], tags: [], group: nil, loaded: false, people: [], total: nil) == F(loaded: false, people: [], total: nil))
        // In the order picked; a person the list doesn't have is left out.
        let f = F.of(selected: ["c", "x", "a"], tags: [], group: nil, loaded: true, people: all, total: 1234)
        #expect(f?.people.map(\.id) == ["c", "a"])
        #expect(f?.total == 1234)
        // Nobody the list has: nothing to show.
        #expect(F.of(selected: ["x"], tags: [], group: nil, loaded: true, people: all, total: 3) == nil)

        // The model: the count only once the buckets are this filter's.
        let vm = PhotoGalleryVM()
        #expect(vm.personFilter == nil)
        vm.setDateBuckets([("2026-08", 10), ("2025-01", 30)], key: vm.currentBucketKey)
        vm.selectedPeople = ["a"]
        // (The list of people isn't asked for here: blanks.)
        #expect(vm.personFilter == F(loaded: false, people: [], total: nil))
        #expect(!vm.bucketsFresh)
        vm.setDateBuckets([("2026-08", 10), ("2025-01", 30)], key: vm.currentBucketKey)
        #expect(vm.bucketsFresh && vm.totalPhotos == 40)
        vm.chips = ["beach"]
        #expect(vm.personFilter == nil)
    }

    @Test func theScrubbersMonthIsWhatTheGreyTilesStandUnder() {
        let vm = PhotoGalleryVM()
        vm.items = [item(0, "2026-08")]
        vm.endReached = true
        vm.setDateBuckets([("2026-08", 10), ("2025-01", 30), ("2019-06", 900)], key: vm.currentBucketKey)
        vm.previewScrub(0.5)
        #expect(vm.scrubTarget?.month == "2019-06")
        #expect(vm.placeholderMonth == "2019-06")
        // Capped: enough to read as a lot.
        #expect(vm.placeholderCount == PhotoGalleryVM.maxPlaceholders)
        let l = vm.gridLayout(phone)
        #expect(l.placeholder == .month("2019-06"))
        #expect(l.rows.first?.kind == .title("2019-06"))
        vm.previewScrub(0.005)
        #expect(vm.placeholderMonth == "2026-08")
        #expect(vm.placeholderCount == 10)
        #expect(vm.gridLayout(phone).rows.map(\.kind) == [.title("2026-08"), .tiles(0..<3), .tiles(3..<6), .tiles(6..<9), .tiles(9..<10)])
    }
}
