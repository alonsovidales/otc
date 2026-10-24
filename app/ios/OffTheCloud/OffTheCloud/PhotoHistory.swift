// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Photos

// The photo sync fetches what was taken after its watermark (lastSyncDate),
// so a photo added to the library after the first sync but taken earlier -
// received with AirDrop, saved from Files, Safari or Messages, imported from
// a camera, an iCloud library still arriving on a new phone - was never
// sent. PhotoKit's persistent change history says what was added since a
// token the sync keeps (PhotoSync.State.changeToken): those the watermark
// is past go on the retry list, which is already kept on disk, exempt from
// the watermark and resolved photo by photo. Android needs none of this:
// its watermark is DATE_ADDED, when the phone first saw the file.

/// What the history says since the stored token.
enum LibraryHistory: Equatable {
    /// No token stored yet (the first run of this version, after Log Out,
    /// a new install): nothing to read; the run goes on as it always did.
    case noToken
    /// The local identifiers of the assets added since the token.
    case inserted(Set<String>)
    /// The token can't be read from: too old for the history kept, or from
    /// another library (a backup restored on another phone), or unreadable.
    case unavailable
}

/// The library's change history: PhotoKit in the app, a fake in tests.
protocol PhotoLibraryHistory {
    /// The library's current token, archived; nil when it can't be had.
    var currentToken: Data? { get }
    func history(since token: Data?) -> LibraryHistory
    /// The creation date of each of these assets still in the library
    /// (nil when it has none); one deleted since is absent.
    func creationDates(of ids: Set<String>) -> [String: Date?]
}

struct PhotoKitHistory: PhotoLibraryHistory {
    var currentToken: Data? {
        try? NSKeyedArchiver.archivedData(withRootObject: PHPhotoLibrary.shared().currentChangeToken, requiringSecureCoding: true)
    }

    func history(since token: Data?) -> LibraryHistory {
        guard let token else { return .noToken }
        guard let since = Self.unarchive(token) else { return .unavailable }
        do {
            var inserted = Set<String>()
            for change in try PHPhotoLibrary.shared().fetchPersistentChanges(since: since) {
                inserted.formUnion(try change.changeDetails(for: .asset).insertedLocalIdentifiers)
            }
            return .inserted(inserted)
        } catch {
            // persistentChangeTokenExpired, persistentChangeDetailsUnavailable,
            // or any other: a token restored from another phone's backup
            // belongs to another library.
            print("Photo change history unavailable:", error)
            return .unavailable
        }
    }

    static func unarchive(_ data: Data) -> PHPersistentChangeToken? {
        try? NSKeyedUnarchiver.unarchivedObject(ofClass: PHPersistentChangeToken.self, from: data)
    }

    /// Default options, as the date fetch: hidden assets are left out.
    func creationDates(of ids: Set<String>) -> [String: Date?] {
        guard !ids.isEmpty else { return [:] }
        var out: [String: Date?] = [:]
        PHAsset.fetchAssets(withLocalIdentifiers: Array(ids), options: PHFetchOptions()).enumerateObjects { a, _, _ in
            out[a.localIdentifier] = a.creationDate
        }
        return out
    }
}

/// What a run does with the history.
struct HistoryPlan: Equatable {
    /// Added with a date the watermark is past: onto the retry list.
    var markPending: [String] = []
    /// The history couldn't be read: queue the catch-up pass.
    var catchUp = false
}

extension PhotoSync {
    /// Pure: what to do with the history. `dates`: the creation date of
    /// each inserted id still in the library (one deleted since is absent);
    /// `watermark`: the run's own. Photos newer than the watermark come
    /// with the date fetch, and with no watermark that fetch is the whole
    /// library: neither needs the retry list (nor a catch-up). Skipped
    /// photos aren't backed up by any path. With the history unavailable,
    /// the catch-up pass goes over everything added since the floor - only
    /// when the floor is known; otherwise the run is what it was before the
    /// history.
    static func plan(_ history: LibraryHistory, dates: [String: Date?], watermark: Date?, skipped: Set<String>, floorKnown: Bool) -> HistoryPlan {
        switch history {
        case .noToken:
            return HistoryPlan()
        case .unavailable:
            return HistoryPlan(catchUp: floorKnown && watermark != nil)
        case .inserted(let ids):
            guard let watermark else { return HistoryPlan() }
            let late = ids.filter { id in
                guard let entry = dates[id] else { return false }
                guard let created = entry else { return true }
                return created <= watermark
            }
            return HistoryPlan(markPending: late.filter { !skipped.contains($0) }.sorted())
        }
    }

    /// A run's history step: reads the history since `token` and plans it.
    /// The creation dates are looked up only for what was inserted, and
    /// the skipped photos (`skipped`, PhotoKit reads) only when something
    /// old was.
    static func readHistory(_ library: PhotoLibraryHistory, token: Data?, watermark: Date?, skipped: () -> Set<String>, floorKnown: Bool) -> HistoryPlan {
        let history = library.history(since: token)
        guard case .inserted(let ids) = history else {
            return plan(history, dates: [:], watermark: watermark, skipped: [], floorKnown: floorKnown)
        }
        guard watermark != nil, !ids.isEmpty else { return HistoryPlan() }
        let dates = library.creationDates(of: ids)
        let unskipped = plan(history, dates: dates, watermark: watermark, skipped: [], floorKnown: floorKnown)
        guard !unskipped.markPending.isEmpty else { return unskipped }
        return plan(history, dates: dates, watermark: watermark, skipped: skipped(), floorKnown: floorKnown)
    }
}
