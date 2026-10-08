// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import CoreGraphics

// Images by month, as the web's PhotoGallery.tsx lays it out: in date
// order the photos stand under month titles ("March 2024"), and months
// short of a row sit side by side, each as wide as its own tiles, as in
// Google Photos. A tag search is ordered by how well photos match, not by
// date, so it has no titles (the web's isDateOrdered). An open collection
// and a person keep date order, and their months.
//
// Everything here is plain values, worked out once per change to the
// photos or to the width (PhotoGalleryVM.gridLayout) - never while a frame
// is drawn - and laid out by a LazyVStack of rows: a big month is its
// title over rows of tiles, months sharing a line are one row.

enum PhotoMonths {
    private static let monthNames = ["January", "February", "March", "April", "May", "June", "July",
                                     "August", "September", "October", "November", "December"]

    /// The calendar months are counted in: the Gregorian one, in the
    /// viewer's time zone. Never the phone's own calendar - a phone set to
    /// the Buddhist, Japanese or Hebrew one would call October 2026
    /// "2569-10", "0008-10" or "5787-01" - since the device's date buckets
    /// and the web's getFullYear/getMonth are Gregorian.
    static func calendar(in zone: TimeZone = .autoupdatingCurrent) -> Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = zone
        return c
    }

    /// `calendar` as the Gregorian one, keeping its time zone.
    private static func gregorian(_ calendar: Calendar) -> Calendar {
        calendar.identifier == .gregorian ? calendar : Self.calendar(in: calendar.timeZone)
    }

    /// "2024-03" for a photo's date, in the viewer's time zone (`calendar`
    /// carries it; months are Gregorian whatever calendar it is) - the
    /// web's monthOf. nil for a photo without a date.
    static func key(for date: Date?, calendar: Calendar = calendar()) -> String? {
        guard let date else { return nil }
        let c = gregorian(calendar).dateComponents([.year, .month], from: date)
        guard let y = c.year, let m = c.month else { return nil }
        return String(format: "%04d-%02d", y, m)
    }

    /// The last instant of the month `key` names, in the viewer's time zone
    /// (Gregorian, as `key`): where the scrubber's jump starts its search -
    /// the web's `new Date(y, m, 0, 23, 59, 59, 999)`. nil for a key that
    /// isn't a month.
    static func lastInstant(of key: String, calendar: Calendar = calendar()) -> Date? {
        let p = key.split(separator: "-")
        guard p.count == 2, let y = Int(p[0]), let m = Int(p[1]), (1...12).contains(m) else { return nil }
        let cal = gregorian(calendar)
        guard let first = cal.date(from: DateComponents(year: y, month: m, day: 1)),
              let next = cal.date(byAdding: .month, value: 1, to: first) else { return nil }
        return next.addingTimeInterval(-0.001)
    }

    private static func parts(_ key: String) -> (year: Int, name: String)? {
        let p = key.split(separator: "-")
        guard p.count == 2, let y = Int(p[0]), let m = Int(p[1]), (1...12).contains(m) else { return nil }
        return (y, monthNames[m - 1])
    }

    /// "March 2024"
    static func title(_ key: String) -> String {
        guard let p = parts(key) else { return key }
        return "\(p.name) \(p.year)"
    }

    /// "Mar 2024", where "March 2024" doesn't fit (a month one tile wide).
    static func short(_ key: String) -> String {
        guard let p = parts(key) else { return key }
        return "\(p.name.prefix(3)) \(p.year)"
    }

    /// The months the photos span, from the date buckets (newest first):
    /// "Mar 2024", "Mar – Oct 2024" or "Mar 2024 – Oct 2026" - "2024 – 2026"
    /// with `years`, which fits a narrow header (the web's spanLabel).
    static func span(_ months: [String], years: Bool = false) -> String {
        guard let newest = months.first.flatMap(parts), let oldest = months.last.flatMap(parts) else { return "" }
        let short = { (p: (year: Int, name: String)) in String(p.name.prefix(3)) }
        if oldest.year == newest.year && oldest.name == newest.name { return "\(short(newest)) \(newest.year)" }
        if oldest.year == newest.year { return "\(short(oldest)) – \(short(newest)) \(newest.year)" }
        if years { return "\(oldest.year) – \(newest.year)" }
        return "\(short(oldest)) \(oldest.year) – \(short(newest)) \(newest.year)"
    }

    private static let number: NumberFormatter = {
        let f = NumberFormatter()
        f.numberStyle = .decimal
        f.locale = Locale(identifier: "en")
        return f
    }()

    /// "1 item", "1,234 items" - the web's count().
    static func count(_ n: Int, _ one: String, _ many: String? = nil) -> String {
        let num = number.string(from: NSNumber(value: n)) ?? "\(n)"
        return "\(num) \(n == 1 ? one : (many ?? one + "s"))"
    }

    /// "Photo, 5 October 2026", "Video" without a date: a tile as VoiceOver
    /// reads it, the web's tile label (en-GB, Gregorian, the viewer's time
    /// zone).
    static func tileLabel(video: Bool, date: Date?, timeZone: TimeZone = .autoupdatingCurrent) -> String {
        let kind = video ? "Video" : "Photo"
        guard let date else { return kind }
        let style = Date.FormatStyle(locale: Locale(identifier: "en_GB"), calendar: calendar(in: timeZone), timeZone: timeZone)
            .day().month(.wide).year()
        return "\(kind), \(date.formatted(style))"
    }

    /// "Ana", "Ana & Luis", "Ana, Luis & 2 others": the people a person
    /// filter shows, as the web's PersonHeader names them.
    static func peopleTitle(_ names: [String]) -> String {
        names.count <= 2
            ? names.joined(separator: " & ")
            : "\(names.prefix(2).joined(separator: ", ")) & \(count(names.count - 2, "other"))"
    }

    /// A run of photos in a row of the list that share a month. A photo
    /// without a date stays with the month before it; at the very top,
    /// such photos make a run of their own with no month.
    struct Run: Equatable {
        var month: String?
        var start: Int
        var end: Int
        var count: Int { end - start }
    }

    /// The months of the photos in their order, as runs: what the web's
    /// gridChildren makes its sections from. The photos are never moved.
    static func runs(_ months: [String?]) -> [Run] {
        var runs: [Run] = []
        for (i, m) in months.enumerated() {
            if let last = runs.indices.last, m == nil || m == runs[last].month {
                runs[last].end = i + 1
            } else {
                runs.append(Run(month: m, start: i, end: i + 1))
            }
        }
        return runs
    }

    /// The bucket a scrubber at `fraction` of its track (0 the newest, 1
    /// the oldest) is on: a month takes as much of the track as it has
    /// photos. nil with no photos.
    static func bucketIndex(atFraction fraction: Double, counts: [Int]) -> Int? {
        let total = counts.reduce(0) { $0 + max(0, $1) }
        guard total > 0 else { return nil }
        let at = Int(min(1, max(0, fraction)) * Double(total))
        var end = 0
        for (i, c) in counts.enumerated() {
            end += max(0, c)
            if at < end { return i }
        }
        return counts.count - 1
    }
}

/// Sizes of the grid, as PhotoGallery.css has them: a phone (a window under
/// 600 wide) three across with 2 pt between tiles and between months side
/// by side; a wider window as many 120 pt tiles as fit (150 pt from 1024),
/// 4 pt apart, and more room between months.
struct PhotoGridMetrics: Equatable {
    /// The grid's own width, its side padding included.
    var width: CGFloat
    /// A phone's sizes: a window under 600 wide (the web's max-width: 599px).
    var phone: Bool
    var cols: Int
    var tile: CGFloat
    var gap: CGFloat
    /// Between months side by side.
    var secGap: CGFloat
    /// Above a line of months (its titles).
    var lineGap: CGFloat
    /// Above the first line.
    var top: CGFloat
    var pad: CGFloat
    /// A title's own margins: inset from the tiles' edge, and under it.
    var titleInset: CGFloat
    var titleBottom: CGFloat

    /// Room above a title's text, inside its row.
    static let titleTop: CGFloat = 4
    /// A phone's columns, whatever its width (the web's --pg-cols-fixed).
    static let phoneCols = 3
    /// The size the tiles' thumbnails are decoded for (GridThumbCache, at
    /// 3x): the biggest phone tile - 140 pt three across a 440 pt Pro Max,
    /// under 145 five across one on its side - and an iPad's (2x) up to
    /// 216 pt, so none is shown enlarged. One size for every width:
    /// turning the phone decodes nothing again.
    static let decodeSide: CGFloat = 144

    init(width: CGFloat, windowWidth: CGFloat) {
        self.width = width
        phone = windowWidth < WideLayout.minWidth
        pad = phone ? 8 : 16
        gap = phone ? 2 : 4
        secGap = phone ? 2 : (windowWidth >= WideLayout.fullWidth ? 16 : 12)
        lineGap = phone ? 14 : 20
        top = phone ? 8 : 12
        titleInset = phone ? 4 : 0
        titleBottom = phone ? 2 : 4
        let content = max(1, width - 2 * pad)
        let tileMin: CGFloat = windowWidth >= WideLayout.fullWidth ? 150 : 120
        cols = phone ? Self.phoneCols : max(1, Int(((content + gap) / (tileMin + gap)).rounded(.down)))
        tile = max(1, (content - CGFloat(cols - 1) * gap) / CGFloat(cols))
    }

    /// The grid's width inside its padding.
    var content: CGFloat { max(1, width - 2 * pad) }

    /// How wide a month of `tiles` is: its tiles, up to a whole row.
    func sectionWidth(_ tiles: Int) -> CGFloat {
        min(content, CGFloat(tiles) * (tile + gap) - gap)
    }
}

/// The grid as rows for a LazyVStack.
struct PhotoGridLayout: Equatable {
    /// A month in a line it shares with others: its title over one row of
    /// tiles, `room` tiles wide (more than it has when more are on their
    /// way, so the next page fills it in rather than moving it).
    struct Piece: Equatable {
        var month: String?
        var tiles: Range<Int>
        var room: Int
    }

    struct Row: Identifiable, Equatable {
        enum Kind: Equatable {
            /// A month's title over the whole width (a month of a row or more).
            case title(String?)
            /// A row of tiles: the photos at these indices (or grey tiles).
            case tiles(Range<Int>)
            /// Months side by side, each with its title.
            case line([Piece])
        }
        let id: String
        let kind: Kind
        /// The first photo it shows, or for a title the first of its month:
        /// where the scroll position is kept when the width changes.
        let first: Int
        /// The room above it.
        let top: CGFloat
    }

    /// Grey tiles (the first page on its way, or the scrubber's month) rather
    /// than photos.
    enum Placeholder: Equatable {
        case none
        /// The first page on its way: a grey title (when in date order)
        /// over grey tiles.
        case skeleton(titled: Bool)
        /// The scrubber's month, with its title.
        case month(String)
    }

    var metrics: PhotoGridMetrics
    var rows: [Row] = []
    var placeholder: Placeholder = .none

    /// Grey tiles shown while the first page is on its way (the web's
    /// cSkeletonTiles).
    static let skeletonTiles = 24

    /// The photos' months (one per photo, nil without a date) laid out:
    /// `dated` false (a tag search) has no titles, one block of rows.
    /// `lastRoom` is how many photos the last month will have once every
    /// page is in (its date bucket), while more are on their way.
    /// `ids` names the rows after their first photo, so a page that adds
    /// photos keeps the rows that were there.
    static func build(months: [String?], ids: [String], dated: Bool, lastRoom: Int?, metrics m: PhotoGridMetrics) -> PhotoGridLayout {
        var out = PhotoGridLayout(metrics: m)
        let n = min(months.count, ids.count)
        guard n > 0 else { return out }
        guard dated else {
            out.addTileRows(0..<n, startsLine: true, idOf: { ids[$0] })
            return out
        }
        let runs = PhotoMonths.runs(Array(months.prefix(n)))
        out.addMonths(runs.enumerated().map { i, r in
            let room = i == runs.count - 1 && r.month != nil ? max(r.count, lastRoom ?? 0) : r.count
            return Piece(month: r.month, tiles: r.start..<r.end, room: room)
        }, idOf: { ids[$0] })
        return out
    }

    /// Grey tiles: `count` under `month`'s title (the scrubber), or the
    /// first page's skeleton.
    static func placeholder(_ kind: Placeholder, count: Int, metrics m: PhotoGridMetrics) -> PhotoGridLayout {
        var out = PhotoGridLayout(metrics: m, placeholder: kind)
        let n = max(0, count)
        let idOf = { (i: Int) in "ph\(i)" }
        switch kind {
        case .none:
            break
        case .skeleton(let titled):
            if titled {
                out.addMonths([Piece(month: nil, tiles: 0..<n, room: n)], idOf: idOf)
            } else {
                out.addTileRows(0..<n, startsLine: true, idOf: idOf)
            }
        case .month(let month):
            out.addMonths([Piece(month: month, tiles: 0..<n, room: n)], idOf: idOf)
        }
        return out
    }

    /// Months as the web's flex-wrap lays its sections: one of a row or
    /// more takes the whole width (its title, then rows of tiles), shorter
    /// ones share a line while they fit beside each other.
    private mutating func addMonths(_ pieces: [Piece], idOf: (Int) -> String) {
        var line: [Piece] = []
        var used: CGFloat = 0
        func flush() {
            guard let first = line.first else { return }
            let start = first.tiles.lowerBound
            rows.append(Row(id: "l\(start):\(idOf(start))", kind: .line(line), first: start, top: lineTop))
            line = []
            used = 0
        }
        for p in pieces {
            let w = metrics.sectionWidth(p.room)
            if p.room >= metrics.cols {
                flush()
                let start = p.tiles.lowerBound
                rows.append(Row(id: "h\(start):\(idOf(start))", kind: .title(p.month), first: start, top: lineTop))
                addTileRows(p.tiles, startsLine: false, idOf: idOf)
                continue
            }
            if !line.isEmpty && used + metrics.secGap + w > metrics.content + 0.5 { flush() }
            used += (line.isEmpty ? 0 : metrics.secGap) + w
            line.append(p)
        }
        flush()
    }

    /// The room above the next line: the grid's top, or the gap between lines.
    private var lineTop: CGFloat { rows.isEmpty ? metrics.top : metrics.lineGap }

    private mutating func addTileRows(_ range: Range<Int>, startsLine: Bool, idOf: (Int) -> String) {
        var i = range.lowerBound
        var firstRow = true
        while i < range.upperBound {
            let end = min(range.upperBound, i + metrics.cols)
            let top = firstRow && startsLine ? lineTop : metrics.gap
            rows.append(Row(id: "t\(i):\(idOf(i))", kind: .tiles(i..<end), first: i, top: top))
            firstRow = false
            i = end
        }
    }

    /// The row the photo at `item` is in (a month's title when the photo
    /// is the first of it): where to scroll back to after the width
    /// changed. nil with no rows.
    func rowIndex(forItem item: Int) -> Int? {
        guard !rows.isEmpty else { return nil }
        var lo = 0
        var hi = rows.count - 1
        // The last row starting at or before `item`.
        while lo < hi {
            let mid = (lo + hi + 1) / 2
            if rows[mid].first <= item { lo = mid } else { hi = mid - 1 }
        }
        // A month's title before its first row of tiles, for its first photo.
        while lo > 0 && rows[lo].first == item && rows[lo - 1].first == item { lo -= 1 }
        return lo
    }

    /// The first photo a row of this layout shows, from its id.
    static func firstItem(ofRowID id: String) -> Int? {
        guard let colon = id.firstIndex(of: ":") else { return nil }
        return Int(id[id.index(after: id.startIndex)..<colon])
    }
}
