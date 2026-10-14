// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FilesSort.swift
//  OffTheCloud
//
//  Issue #196: the order of Files' list, grid and search results, one
//  choice for the app (not per folder, nor on the device). The same rules
//  as Android's FilesSort.kt and the web's filesSort.ts, tested with the
//  same cases (FilesSortTests.swift):
//  - by Name, Size, Date created or Date modified; each starts in its
//    natural direction (A to Z, largest first, newest first) and picking
//    the same one again reverses it;
//  - ".." first, then the folders, then the files, in every order;
//  - folders carry no size or dates in a listing, so by Size or a date
//    they stay in name order (A to Z) above the files;
//  - names in the app language's collation, ignoring case and accents,
//    with the numbers in them in counting order (file2 before file10);
//  - a file without the date sorted by goes last, either way;
//  - ties by name (A to Z), then by path code unit by code unit (UTF-16,
//    as the web and Android compare), so the order never depends on the
//    order the device sent.

import Foundation

/// What Files can be sorted by: the columns of the web's list.
enum FilesSortKey: String, CaseIterable, Identifiable, Sendable {
    case name, size, created, modified

    var id: String { rawValue }

    /// The direction it starts in: A to Z, largest first, newest first.
    var naturalAscending: Bool { self == .name }
}

/// The order chosen: a key and its direction.
struct FilesSortOrder: Equatable, Sendable {
    var key: FilesSortKey
    var ascending: Bool

    static let `default` = FilesSortOrder(key: .name, ascending: true)

    /// Where it is kept (UserDefaults, as files.viewMode; Android's
    /// otc_settings "files_sort").
    static let storageKey = "files.sort"

    init(key: FilesSortKey, ascending: Bool) {
        self.key = key
        self.ascending = ascending
    }

    /// A stored order ("size:desc"), or the default for anything else.
    init(stored: String?) {
        let parts = (stored ?? "").split(separator: ":", omittingEmptySubsequences: false)
        guard parts.count == 2, let key = FilesSortKey(rawValue: String(parts[0])),
              parts[1] == "asc" || parts[1] == "desc" else {
            self = .default
            return
        }
        self.init(key: key, ascending: parts[1] == "asc")
    }

    /// As it is kept: "size:desc".
    var stored: String { "\(key.rawValue):\(ascending ? "asc" : "desc")" }

    /// The order after picking `key`: the same key the other way, another
    /// one in its natural direction.
    func picking(_ key: FilesSortKey) -> FilesSortOrder {
        key == self.key ? FilesSortOrder(key: key, ascending: !ascending) : FilesSortOrder(key: key, ascending: key.naturalAscending)
    }
}

/// What a row is sorted by: its name as shown, its path, and its size and
/// dates (nil when the device sent none).
struct FilesSortFields: Sendable {
    let name: String
    let path: String
    let isDir: Bool
    let size: Int64
    let created: Date?
    let modified: Date?
}

enum FilesSort {
    /// Above this many rows the order is worked out off the main thread: a
    /// phone's whole library can sit in one folder, and comparing tens of
    /// thousands of names in the app's language takes a while.
    static let offMainAbove = 5000

    /// Names as the app's language orders them, case and accents ignored,
    /// numbers by value: ICU's numeric collation underneath, as the web's
    /// Intl.Collator and Android's NameCollator (FilesSortTests' tricky
    /// names hold all three to one order).
    static func compareNames(_ a: String, _ b: String, locale: Locale) -> ComparisonResult {
        a.compare(b, options: [.caseInsensitive, .diacriticInsensitive, .numeric], range: nil, locale: locale)
    }

    /// Code unit by code unit, as the web and Android compare strings.
    static func compareBinary(_ a: String, _ b: String) -> ComparisonResult {
        if a == b { return .orderedSame }
        return a.utf16.lexicographicallyPrecedes(b.utf16) ? .orderedAscending : .orderedDescending
    }

    /// ".." first, then folders, then files.
    private static func group(_ f: FilesSortFields) -> Int {
        f.path == ".." ? 0 : f.isDir ? 1 : 2
    }

    private static func reversed(_ r: ComparisonResult) -> ComparisonResult {
        switch r {
        case .orderedAscending: return .orderedDescending
        case .orderedDescending: return .orderedAscending
        case .orderedSame: return .orderedSame
        }
    }

    private static func compareValues<T: Comparable>(_ a: T, _ b: T) -> ComparisonResult {
        a < b ? .orderedAscending : a > b ? .orderedDescending : .orderedSame
    }

    /// Dates the order's way, a missing one last either way.
    private static func compareDates(_ a: Date?, _ b: Date?, ascending: Bool) -> ComparisonResult {
        switch (a, b) {
        case (nil, nil): return .orderedSame
        case (nil, _): return .orderedDescending
        case (_, nil): return .orderedAscending
        case let (a?, b?):
            let r = compareValues(a, b)
            return ascending ? r : reversed(r)
        }
    }

    /// Where `a` goes against `b` in `order`.
    static func compare(_ a: FilesSortFields, _ b: FilesSortFields, by order: FilesSortOrder, locale: Locale) -> ComparisonResult {
        let ga = group(a), gb = group(b)
        if ga != gb { return compareValues(ga, gb) }
        let byName = { () -> ComparisonResult in
            let n = compareNames(a.name, b.name, locale: locale)
            return n != .orderedSame ? n : compareBinary(a.path, b.path)
        }
        if order.key == .name {
            let r = byName()
            return order.ascending ? r : reversed(r)
        }
        // Folders have no size or dates of their own: name order, A to Z.
        if !a.isDir {
            let r: ComparisonResult
            switch order.key {
            case .size:
                let s = compareValues(a.size, b.size)
                r = order.ascending ? s : reversed(s)
            case .created: r = compareDates(a.created, b.created, ascending: order.ascending)
            case .modified: r = compareDates(a.modified, b.modified, ascending: order.ascending)
            case .name: r = .orderedSame
            }
            if r != .orderedSame { return r }
        }
        return byName()
    }

    /// A copy of `items` in `order`; `fields` says what each one is sorted
    /// by (worked out once per item).
    static func sorted<T>(_ items: [T], fields: (T) -> FilesSortFields, by order: FilesSortOrder, locale: Locale) -> [T] {
        var keyed = items.map { (item: $0, fields: fields($0)) }
        keyed.sort { compare($0.fields, $1.fields, by: order, locale: locale) == .orderedAscending }
        return keyed.map(\.item)
    }

    static func sorted(_ rows: [FileRow], by order: FilesSortOrder, locale: Locale) -> [FileRow] {
        sorted(rows, fields: \.sortFields, by: order, locale: locale)
    }

    /// Files as the search finds them (SearchFiles), in `order`.
    static func sorted(_ files: [Msg_File], by order: FilesSortOrder, locale: Locale) -> [Msg_File] {
        sorted(files, fields: { FileRow(file: $0).sortFields }, by: order, locale: locale)
    }
}

extension FileRow {
    var sortFields: FilesSortFields {
        FilesSortFields(name: name, path: path, isDir: isDir, size: size, created: created, modified: modified)
    }
}

/// What the order is called on screen (the View options menu) and to
/// VoiceOver: the same words as Android's FilesSortText, from the catalog
/// (app.files.*) in the language shown when read.
enum FilesSortText {
    static func name(_ key: FilesSortKey) -> String {
        switch key {
        case .name: return S.appFilesSortName
        case .size: return S.appFilesSortSize
        case .created: return S.appFilesSortCreated
        case .modified: return S.appFilesSortModified
        }
    }

    /// Which way, under the key in the menu: "A to Z", "Largest first"...
    static func direction(_ order: FilesSortOrder) -> String {
        switch (order.key, order.ascending) {
        case (.name, true): return S.appFilesSortAZ
        case (.name, false): return S.appFilesSortZA
        case (.size, false): return S.appFilesSortLargest
        case (.size, true): return S.appFilesSortSmallest
        case (.created, false), (.modified, false): return S.appFilesSortNewest
        case (.created, true), (.modified, true): return S.appFilesSortOldest
        }
    }

    /// Said once the order changes: "Sorted by Size, Largest first".
    static func sorted(_ order: FilesSortOrder) -> String {
        S.appFilesSorted(key: name(order.key), order: direction(order))
    }
}
