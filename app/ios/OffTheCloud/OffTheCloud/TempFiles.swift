// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  TempFiles.swift
//  OffTheCloud
//
//  The temporary copies the app writes, each kind in a folder of its own
//  under tmp/ and, in it, a folder for this launch. A copy is removed by
//  whoever made it once it is done with - but an app that iOS suspends in
//  the middle (its background time over) and then ends never gets there:
//  the photo sync's copies of multi-GB videos stayed in tmp until iOS
//  purged it or the owner logged out, and enough of them made the next
//  copies find the phone full (ExportRoom). A launch empties what earlier
//  launches left (`sweep`), never what this one is writing.
//

import Foundation

enum TempFiles {
    enum Kind: String, CaseIterable {
        /// The copies of a photo or video the photo sync, the composer and
        /// Free Up Space read (PhotoSync.exportAsset).
        case export
        /// The viewer's: a video played from a whole download, the picture
        /// Share hands over.
        case viewer
        /// Files' Quick Look copies of a file or one of its versions.
        case preview
    }

    /// This launch's folder name in each kind's.
    static let launch = UUID().uuidString
    /// When this launch began: only copies older than this are taken by the
    /// sweep of tmp's top level.
    static let launchedAt = Date()

    /// The kind's folder for this launch, made if missing.
    static func folder(_ kind: Kind, root: URL = FileManager.default.temporaryDirectory) -> URL {
        let dir = root.appendingPathComponent(kind.rawValue, isDirectory: true)
            .appendingPathComponent(launch, isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    /// A new file of `kind`, named by a UUID with `ext` (AVFoundation picks
    /// how to read a file by its extension).
    static func file(_ kind: Kind, ext: String, root: URL = FileManager.default.temporaryDirectory) -> URL {
        let url = folder(kind, root: root).appendingPathComponent(UUID().uuidString)
        return ext.isEmpty ? url : url.appendingPathExtension(ext)
    }

    /// A new file of `kind` under `name` (Quick Look shows a file's name),
    /// in a folder of its own, so two of one name never meet.
    static func named(_ kind: Kind, _ name: String, root: URL = FileManager.default.temporaryDirectory) -> URL {
        let dir = folder(kind, root: root).appendingPathComponent(UUID().uuidString, isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appendingPathComponent(name.isEmpty ? "file" : name)
    }

    /// Removes a copy made here, and the folder of its own that `named`
    /// made for it. Anything outside the kinds' folders is left alone.
    static func remove(_ url: URL?, root: URL = FileManager.default.temporaryDirectory) {
        guard let url else { return }
        let fm = FileManager.default
        try? fm.removeItem(at: url)
        let parent = url.deletingLastPathComponent()
        let base = root.standardizedFileURL.path
        let path = parent.standardizedFileURL.path
        guard path.hasPrefix(base) else { return }
        // tmp/<kind>/<launch>/<uuid>/<name>: the <uuid> folder goes too.
        let parts = path.dropFirst(base.count).split(separator: "/")
        if parts.count == 3, Kind(rawValue: String(parts[0])) != nil {
            try? fm.removeItem(at: parent)
        }
    }

    /// At launch, off the main thread: every kind's folders but this
    /// launch's go, and the copies earlier versions left at tmp's top level
    /// from before this launch - named by a UUID, with or without an
    /// extension, or the composer's trims and the wallpaper's downloads.
    /// Nothing else there is touched.
    static func sweep(root: URL = FileManager.default.temporaryDirectory, except launch: String = launch,
                      before cutoff: Date = launchedAt) {
        let fm = FileManager.default
        for kind in Kind.allCases {
            let dir = root.appendingPathComponent(kind.rawValue, isDirectory: true)
            for item in (try? fm.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? [] where item.lastPathComponent != launch {
                try? fm.removeItem(at: item)
            }
        }
        let keys: [URLResourceKey] = [.contentModificationDateKey]
        for item in (try? fm.contentsOfDirectory(at: root, includingPropertiesForKeys: keys)) ?? [] {
            guard leftByAnOlderVersion(item.lastPathComponent),
                  let modified = (try? item.resourceValues(forKeys: Set(keys)))?.contentModificationDate,
                  modified < cutoff else { continue }
            try? fm.removeItem(at: item)
        }
    }

    /// Pure: a name at tmp's top level that earlier versions gave their
    /// copies: "<UUID>" or "<UUID>.<ext>", "otc-trim-…", "wallpaper-…".
    static func leftByAnOlderVersion(_ name: String) -> Bool {
        if name.hasPrefix("otc-trim-") || name.hasPrefix("wallpaper-") { return true }
        let stem = name.split(separator: ".", maxSplits: 1).first.map(String.init) ?? name
        return UUID(uuidString: stem) != nil
    }
}
