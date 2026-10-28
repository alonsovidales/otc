// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FilesColumns.swift
//  OffTheCloud
//
//  The widths of Files' list columns on a wide window: the rules, apart
//  from the views (FilesColumnsView.swift puts them on the screen). The
//  web's filesColumns.ts, ported with its numbers and tested with its cases
//  (FilesColumnsTests.swift <-> web/scripts/files-columns-test.mjs). The
//  owner's request, 2026-10-28, on an open Galaxy Fold: the web app's Files
//  columns in the phone apps when the screen is wide.
//
//  - Wide is the app's wide layout (WideLayout.minWidth, 600 pt: the menu
//    beside the page instead of the bottom bar - an iPad, an iPhone on its
//    side with room for it), with room for Name and Size at their
//    minimums (`fits`). A narrow window keeps its list as it was: the
//    name with its size under it, and the View options menu.
//  - Name takes what the other columns leave, and never less than room for
//    10 characters of the name with its row's icons (`nameMin`).
//  - Size, Created and Modified have a width each: the one given on this
//    phone (UserDefaults, storageKey), else a default that fits a long size
//    or date in the app's language and text size.
//  - As the list narrows, Created goes, then Modified, once Name would keep
//    less than `nameRoom` with them at their defaults; Size always stays.
//    What shows is fitted to the room: columns wider than it allows give
//    way, the last first, down to their minimums. The list never scrolls
//    sideways.
//  - The handle at a heading's trailing edge resizes that column: wider
//    takes the room from the columns after it, nearest first, down to their
//    minimums; narrower gives it to the next column. A double tap puts the
//    column back (Name: every column), and VoiceOver's actions (or swiping
//    up and down on it) move it by `step`.
//

import CoreGraphics
import Foundation

/// The widths given on this phone, in points; a column left out has its
/// default. Name never has one (its width is what the others leave).
typealias FilesColumnWidths = [FilesSortKey: CGFloat]

/// What the columns need, measured in the app's fonts (FilesColumns.metrics).
struct FilesColumnMetrics: Equatable {
    /// Before Name: the selection circle and the row's icon (the web's
    /// checkbox column).
    var lead: CGFloat
    /// 10 characters of a name ("0000000000…").
    var nameText: CGFloat
    /// Name's minimum: those 10 characters, the row's icons after the name
    /// and the cell's padding.
    var nameMin: CGFloat
    /// What Name keeps before Created or Modified shows.
    var nameRoom: CGFloat
    var min: FilesColumnWidths
    var def: FilesColumnWidths
}

/// Text widths measured on the screen (FilesColumnsView.samples), in points.
struct FilesColumnSamples: Equatable {
    /// Before Name (FilesColumnMetrics.lead).
    var lead: CGFloat
    /// After the name, at most: the row's marks and buttons (a folder's
    /// "kept out of Images" mark and lock, a file's versions count and
    /// lock), with the gaps before them.
    var trail: CGFloat
    /// Leading plus trailing padding of each column's cells.
    var pad: [FilesSortKey: CGFloat]
    /// "0000000000…" in a name's font.
    var name: CGFloat
    /// A long size ("1023.0 MB") and a short one ("1.5 KB").
    var size: CGFloat
    var sizeShort: CGFloat
    /// A long date and time, and the date alone.
    var date: CGFloat
    var dateShort: CGFloat
}

/// The columns shown, Name first, and the width of each (Name's is what
/// the others leave).
struct FilesColumnLayout: Equatable {
    var shown: [FilesSortKey]
    var width: [FilesSortKey: CGFloat]

    /// A column's width; 0 for one not shown.
    subscript(_ k: FilesSortKey) -> CGFloat { width[k] ?? 0 }

    /// The columns with a width of their own: those shown after Name.
    var fixed: [FilesSortKey] { shown.filter { $0 != .name } }

    /// The last column shown, which has no edge to move.
    var last: FilesSortKey? { shown.last }
}

enum FilesColumns {
    /// Every column, in their order (Name first).
    static let all: [FilesSortKey] = [.name, .size, .created, .modified]
    /// Hidden in this order as the list narrows.
    static let hideOrder: [FilesSortKey] = [.created, .modified]

    /// Where the widths given are kept (UserDefaults, as files.viewMode;
    /// the web's localStorage otc_files_columns).
    static let storageKey = "files.columns"
    /// What VoiceOver's Wider and Narrower (the web's arrow keys) move a
    /// column's edge by.
    static let step: CGFloat = 16
    /// Never kept wider than this (a stored value is checked against it).
    static let maxStored: CGFloat = 4000

    private static func up(_ n: CGFloat) -> CGFloat { n.rounded(.up) }
    /// As the web's Math.round: halves go up (-0.5 to 0, 0.5 to 1).
    static func round(_ n: CGFloat) -> CGFloat { (n + 0.5).rounded(.down) }
    private static func sum(_ ns: [CGFloat]) -> CGFloat { ns.reduce(0, +) }

    /// The minimums and defaults from what the screen measured.
    static func metrics(_ s: FilesColumnSamples) -> FilesColumnMetrics {
        let nameMin = up((s.pad[.name] ?? 0) + s.name + s.trail + 2)
        return FilesColumnMetrics(
            lead: s.lead,
            nameText: up(s.name),
            nameMin: nameMin,
            nameRoom: nameMin + up(s.name),
            min: [
                .size: up((s.pad[.size] ?? 0) + s.sizeShort + 2),
                .created: up((s.pad[.created] ?? 0) + s.dateShort + 4),
                .modified: up((s.pad[.modified] ?? 0) + s.dateShort + 4),
            ],
            def: [
                .size: up((s.pad[.size] ?? 0) + s.size + 4),
                .created: up((s.pad[.created] ?? 0) + s.date + 4),
                .modified: up((s.pad[.modified] ?? 0) + s.date + 4),
            ]
        )
    }

    /// Whether a list `room` wide has room for the columns at all: Name at
    /// its minimum and Size at its. The phones keep the narrow rows (the
    /// name with its size under it) where it hasn't - the largest text
    /// sizes in a narrow wide window - rather than cut the name shorter
    /// than 10 characters.
    static func fits(room: CGFloat, _ m: FilesColumnMetrics) -> Bool {
        m.lead + m.nameMin + (m.min[.size] ?? 0) <= room
    }

    /// The columns a list `room` wide shows: Created goes first, then
    /// Modified. Which ones show never hangs on the widths given (a drag
    /// never hides one).
    static func shown(room: CGFloat, _ m: FilesColumnMetrics) -> [FilesSortKey] {
        var shown = all
        for c in hideOrder {
            let fixed = shown.filter { $0 != .name }.map { m.def[$0] ?? 0 }
            if m.lead + m.nameRoom + sum(fixed) <= room { break }
            shown.removeAll { $0 == c }
        }
        return shown
    }

    /// The layout for a list `room` wide with the widths given on this phone.
    static func fit(room: CGFloat, given: FilesColumnWidths, _ m: FilesColumnMetrics) -> FilesColumnLayout {
        let shown = shown(room: room, m)
        var width: [FilesSortKey: CGFloat] = [:]
        let fixed = shown.filter { $0 != .name }
        for k in fixed {
            width[k] = max(m.min[k] ?? 0, round(given[k] ?? m.def[k] ?? 0))
        }
        // Name keeps its minimum: the widest give way, from the last column.
        var over = m.lead + m.nameMin + sum(fixed.map { width[$0] ?? 0 }) - room
        for k in fixed.reversed() where over > 0 {
            let give = min(over, (width[k] ?? 0) - (m.min[k] ?? 0))
            width[k] = (width[k] ?? 0) - give
            over -= give
        }
        width[.name] = max(0, room - m.lead - sum(fixed.map { width[$0] ?? 0 }))
        return FilesColumnLayout(shown: shown, width: width)
    }

    /// A column's minimum: Name's room for 10 characters and its icons.
    static func minimum(_ k: FilesSortKey, _ m: FilesColumnMetrics) -> CGFloat {
        k == .name ? m.nameMin : (m.min[k] ?? 0)
    }

    /// `col`'s trailing edge moved by `dx` points from where it was in `l`:
    /// wider takes the room from the columns after it, nearest first, down
    /// to their minimums; narrower, down to its own minimum, gives it to
    /// the next one. The last column shown has no edge to move, nor does a
    /// hidden one: `l` comes back as it was.
    static func resize(_ l: FilesColumnLayout, _ m: FilesColumnMetrics, column col: FilesSortKey, by dx: CGFloat) -> FilesColumnLayout {
        guard let i = l.shown.firstIndex(of: col), i < l.shown.count - 1 else { return l }
        let step = round(dx)
        guard step != 0 else { return l }
        var w = l.shown.map { l[$0] }
        let mins = l.shown.map { minimum($0, m) }
        if step > 0 {
            var need = step
            var j = i + 1
            while j < w.count && need > 0 {
                let take = min(need, max(0, w[j] - mins[j]))
                w[j] -= take
                w[i] += take
                need -= take
                j += 1
            }
        } else {
            let take = min(-step, max(0, w[i] - mins[i]))
            w[i] -= take
            w[i + 1] += take
        }
        var width = l.width
        for (j, k) in l.shown.enumerated() { width[k] = w[j] }
        return FilesColumnLayout(shown: l.shown, width: width)
    }

    /// The widths to keep after a resize from `before` to `after`: those it
    /// changed, on top of the ones given.
    static func widthsAfter(_ given: FilesColumnWidths, before: FilesColumnLayout, after: FilesColumnLayout) -> FilesColumnWidths {
        var out = given
        for k in after.fixed where after[k] != before[k] { out[k] = after[k] }
        return out
    }

    /// A column put back as it was: Name puts every column back, as Name's
    /// own width is what they leave.
    static func reset(_ given: FilesColumnWidths, column col: FilesSortKey) -> FilesColumnWidths {
        if col == .name { return [:] }
        var out = given
        out[col] = nil
        return out
    }

    /// The handle's values for VoiceOver: `col`'s width, its minimum and
    /// its most.
    static func edgeValues(_ l: FilesColumnLayout, _ m: FilesColumnMetrics, column col: FilesSortKey) -> (now: CGFloat, min: CGFloat, max: CGFloat) {
        let now = round(l[col])
        var most = now
        if let i = l.shown.firstIndex(of: col) {
            for k in l.shown.dropFirst(i + 1) { most += max(0, l[k] - minimum(k, m)) }
        }
        return (now, Swift.min(now, minimum(col, m)), round(most))
    }

    /// Stored widths as read back: anything else than a sane number per
    /// column is left out.
    static func parse(_ raw: String?) -> FilesColumnWidths {
        guard let raw, let data = raw.data(using: .utf8),
              let v = try? JSONSerialization.jsonObject(with: data, options: [.fragmentsAllowed]),
              let dict = v as? [String: Any] else { return [:] }
        var out: FilesColumnWidths = [:]
        for k in all where k != .name {
            // A JSON true would read as 1: numbers only.
            guard let n = dict[k.rawValue] as? NSNumber, CFGetTypeID(n) != CFBooleanGetTypeID() else { continue }
            let d = CGFloat(n.doubleValue)
            if d.isFinite && d > 0 && d <= maxStored { out[k] = round(d) }
        }
        return out
    }

    /// As kept: {"size":120,"modified":180}; nil for none.
    static func stored(_ w: FilesColumnWidths) -> String? {
        guard !w.isEmpty else { return nil }
        let dict = Dictionary(uniqueKeysWithValues: w.map { ($0.key.rawValue, Int(round($0.value))) })
        guard let data = try? JSONSerialization.data(withJSONObject: dict, options: [.sortedKeys]) else { return nil }
        return String(data: data, encoding: .utf8)
    }

    static func load(_ defaults: UserDefaults = .standard) -> FilesColumnWidths {
        parse(defaults.string(forKey: storageKey))
    }

    static func save(_ w: FilesColumnWidths, _ defaults: UserDefaults = .standard) {
        if let s = stored(w) {
            defaults.set(s, forKey: storageKey)
        } else {
            defaults.removeObject(forKey: storageKey)
        }
    }
}
