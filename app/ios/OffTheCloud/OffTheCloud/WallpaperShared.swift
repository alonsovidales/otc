// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  WallpaperShared.swift
//  OffTheCloud
//
//  What the app and its home screen widget (OffTheCloudWidget) share
//  through their App Group: the copies of one collection's photos stored
//  for the widget (Wallpaper.swift stores them, the widget shows them), the
//  list of collections that have them, the order the widget goes through
//  them, and the language chosen in the app. Compiled into both targets.
//  The copies live in the App Group's own container on this iPhone and
//  nowhere else - never in Photos, never uploaded.
//

import Foundation

enum WallpaperGroup {
    /// The App Group of the app and the widget (both entitlements).
    static let id = "group.cloud.off-the.OffTheCloud"
    /// The widget's kind: what the app reloads (WidgetCenter).
    static let widgetKind = "cloud.off-the.OffTheCloud.collection"

    static var container: URL? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: id)
    }

    /// Shared defaults: the app's language (languageKey) and the widget's
    /// shuffle (WallpaperShuffle).
    static var defaults: UserDefaults? { UserDefaults(suiteName: id) }

    /// The code of the language chosen in the app ("pt"): the widget shows
    /// its texts in it.
    static let languageKey = "language"

    /// The widget's copies, one folder per collection, and its catalog.
    static func widgetRoot(in container: URL) -> URL {
        container.appendingPathComponent("Widget", isDirectory: true)
    }
}

/// The collections whose photos are stored for the widget, with their
/// copies: what the widget offers in its setting and shows. One at a time
/// (WallpaperModel), kept as a list so the widget's setting is a plain
/// choice. Read and written as JSON next to the copies.
struct WidgetCatalog: Codable, Equatable {
    struct Collection: Codable, Equatable, Identifiable {
        /// The collection's id on the device.
        var id: String
        var name: String
        /// Its folder under the widget's root.
        var folder: String
        /// The copies' file names in that folder, by the photo's content hash.
        var files: [String: String]
    }

    var collections: [Collection] = []

    static let fileName = "collections.json"

    static func url(root: URL) -> URL { root.appendingPathComponent(fileName) }

    /// The catalog under `root`; empty when there is none, or it can't be
    /// read.
    static func load(root: URL) -> WidgetCatalog {
        guard let data = try? Data(contentsOf: url(root: root)),
              let c = try? JSONDecoder().decode(WidgetCatalog.self, from: data) else { return WidgetCatalog() }
        return c
    }

    func save(root: URL) throws {
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        let data = try JSONEncoder().encode(self)
        try data.write(to: Self.url(root: root), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    func collection(_ id: String) -> Collection? { collections.first { $0.id == id } }

    /// A collection's copies as files, in a stable order (by name).
    static func files(of c: Collection, root: URL) -> [URL] {
        let dir = root.appendingPathComponent(c.folder, isDirectory: true)
        return c.files.values.sorted().map { dir.appendingPathComponent($0) }
    }

    /// A folder name for a collection id: its characters that are safe in a
    /// file name, and never empty.
    static func folderName(for id: String) -> String {
        let safe = id.unicodeScalars.filter { CharacterSet.alphanumerics.contains($0) || $0 == "-" || $0 == "_" }
        let name = String(String.UnicodeScalarView(safe))
        return name.isEmpty ? "collection" : name
    }
}

/// The order the widget goes through a collection's photos: at random, and
/// every photo once before any comes again (the photos already shown in
/// the current round are kept between timelines).
enum WallpaperShuffle {
    /// The next `count` photos from `all`, none of them in `shown` until
    /// every one has been; and what has been shown after them (a new round
    /// starts once all have). A photo no longer in `all` is forgotten.
    static func next<R: RandomNumberGenerator>(_ count: Int, from all: [String], shown: [String],
                                                using rng: inout R) -> (picks: [String], shown: [String]) {
        guard !all.isEmpty, count > 0 else { return ([], []) }
        let present = Set(all)
        var round = shown.filter { present.contains($0) }
        var picks: [String] = []
        var last = shown.last
        while picks.count < count {
            let seen = Set(round)
            var left = all.filter { !seen.contains($0) }
            if left.isEmpty {
                // Every photo has been shown: a new round, which doesn't
                // start with the photo just shown when there is a choice
                // (it comes later in the round).
                round = []
                left = all.count == 1 ? all : all.filter { $0 != last }
            }
            left.shuffle(using: &rng)
            for p in left where picks.count < count {
                picks.append(p)
                round.append(p)
                last = p
            }
        }
        return (picks, round)
    }

    /// Where a collection's round is kept (WallpaperGroup.defaults).
    static func key(_ collection: String) -> String { "widget.shown." + collection }
}

/// The names of the collections whose copies were stored for the widget,
/// kept after Stop (a name, no photo): a widget still set to one whose
/// copies are gone says so (ios.widget_not_stored) rather than asking for
/// a collection again. Forgotten at Log Out.
enum WidgetNames {
    static let key = "widget.names"

    static func all(_ defaults: UserDefaults?) -> [String: String] {
        defaults?.dictionary(forKey: key) as? [String: String] ?? [:]
    }

    static func remember(id: String, name: String, in defaults: UserDefaults?) {
        var names = all(defaults)
        names[id] = name
        defaults?.set(names, forKey: key)
    }

    static func forget(in defaults: UserDefaults?) {
        defaults?.removeObject(forKey: key)
    }
}
