// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation

/// Issue #192: how a synced folder stands with Images on the device
/// (SyncPaths.outOfImagesState; otc-sync's engine.ImagesState).
enum OutOfImagesState: Equatable {
    /// Not known yet (the device hasn't listed its folders kept out, or
    /// can't): the row shows nothing.
    case unknown
    case shown
    /// Kept out by its own flag.
    case keptOut
    /// A folder above it is kept out, which covers it (its device path,
    /// without the slash).
    case keptOutBy(String)
    /// A request not acknowledged yet: keep it out, or show it again.
    case keeping
    case showing
    /// A request the device can't take: it needs an update, and the
    /// request goes by itself once it has one.
    case unsupported

    /// Shown as kept out (or about to be).
    var isKeptOut: Bool {
        switch self {
        case .keptOut, .keptOutBy, .keeping: return true
        default: return false
        }
    }
}

/// Issue #132: how a folder's request to be made upload only stands
/// (SyncPaths.uploadOnlyState; otc-sync's engine.UploadOnlyState): nothing
/// on its way (the device has it, or nothing was asked), a request not
/// acknowledged yet, or one the device can't take.
enum UploadOnlyRequestState: Equatable {
    case none
    /// A request to make it upload only, on its way.
    case making
    /// A request to lift it (no control asks for it; kept for stored data).
    case lifting
    /// The device needs an update; sent by itself once it has one.
    case unsupported
}

/// Which paths a sync pass may act on. Free of SyncModel (and of anything
/// else) so the rules can be tested on their own.
///
/// The checks read UTF-8 bytes, as the file system does: Character-wise,
/// a "/" followed by a combining mark is one grapheme and not a separator,
/// so "../\u{301}x" passed for one harmless name. They are also cheap (no
/// array per path): a two-way pass runs them on every listed file.
enum SyncPaths {
    /// A device path as a path inside the folder listed at `prefix`, or nil
    /// when it isn't one. The device stores paths as clients send them, so
    /// a hostile bridge or a buggy client can list "<prefix>../../x",
    /// which joined onto the local root would be written outside it.
    static func safeRelative(_ path: String, under prefix: String) -> String? {
        guard path.hasPrefix(prefix) else { return nil }
        let relative = String(path.dropFirst(prefix.count))
        return isSafeRelative(relative) ? relative : nil
    }

    /// Not empty, not absolute, no NUL, and no empty, "." or ".." component.
    static func isSafeRelative(_ relative: String) -> Bool {
        var length = 0, dots = 0   // the current component's bytes, and how many are "."
        for byte in relative.utf8 {
            switch byte {
            case 0:
                return false
            case UInt8(ascii: "/"):
                if length == 0 || (dots == length && dots <= 2) { return false }
                length = 0
                dots = 0
            default:
                length += 1
                if byte == UInt8(ascii: ".") { dots += 1 }
            }
        }
        return !(length == 0 || (dots == length && dots <= 2))
    }

    /// Left out of a two-way pass on both sides: what the local scan never
    /// sees (a component starting with ".", as .skipsHiddenFiles) and
    /// downloads in progress. A dotfile listed on the device used to come
    /// down, look deleted here on the next pass, and be deleted there.
    /// As otc-sync's notSynced.
    static func isExcludedFromSync(_ relative: String) -> Bool {
        relative.hasSuffix(".otc-part") || hasHiddenComponent(relative)
    }

    /// A backup's watcher skips what its reconcile never sends: a hidden
    /// path below the folder (.DS_Store, .git/...) or a partial download.
    /// Judged below `root` only, as .skipsHiddenFiles: a backup of
    /// ~/.dotfiles is still watched.
    static func isSkippedBackupPath(_ path: String, root: String) -> Bool {
        if path.hasSuffix(".otc-part") { return true }
        if path.hasPrefix(root + "/") {
            return hasHiddenComponent(String(path.dropFirst(root.count + 1)))
        }
        // FSEvents spelled the path differently (/private/var vs /var):
        // judge the file's own name only.
        return hasHiddenComponent((path as NSString).lastPathComponent)
    }

    /// After a two-way pass, the folders its local deletions left empty go
    /// too. The device has no empty folders - a folder there is only the
    /// files under it - so a folder deleted there used to leave its whole
    /// tree here, empty but "synced". From each folder a deleted file was
    /// in, up to (not including) `root`: removed while it holds nothing but
    /// Finder's .DS_Store, which is never synced. rmdir only removes an
    /// empty folder, so a file that arrives meanwhile keeps it. As
    /// otc-sync's removeEmptiedDirs. Returns how many went.
    @discardableResult
    static func removeEmptiedFolders(_ folders: Set<String>, root: String) -> Int {
        var removed = 0
        // Deepest first, so a parent is judged after its children went.
        for start in folders.sorted(by: { $0.count > $1.count }) {
            var dir = start
            while dir.hasPrefix(root + "/") {
                guard let names = try? FileManager.default.contentsOfDirectory(atPath: dir),
                      names.allSatisfy({ $0 == ".DS_Store" }) else { break }
                for name in names {
                    try? FileManager.default.removeItem(atPath: (dir as NSString).appendingPathComponent(name))
                }
                guard rmdir(dir) == 0 else { break }
                removed += 1
                dir = (dir as NSString).deletingLastPathComponent
            }
        }
        return removed
    }

    /// Issue #192: how a synced folder stands with Images on the device.
    /// `devicePath` is its device path with the trailing slash, `folders`
    /// what ListOutOfImages answered (each with its slash; nil while
    /// unknown), `pending` the request not yet acknowledged, `supported`
    /// whether the device can (nil while unknown). Paths compare byte for
    /// byte, as the device does (issue #172): Swift's == and hasPrefix
    /// would take "é" and "e\u{301}" for the same, and /kim/ never covers
    /// /kimono/ thanks to the slash. As otc-sync's engine.OutOfImagesState.
    static func outOfImagesState(devicePath: String, folders: [String]?, pending: Bool?, supported: Bool?) -> OutOfImagesState {
        switch (pending, supported) {
        case (.some, false?): return .unsupported
        case (nil, false?): return .unknown
        case (true?, _): return .keeping
        case (false?, _): return .showing
        default: break
        }
        guard let folders else { return .unknown }
        let path = Array(devicePath.utf8)
        var own = false
        var parent: [UInt8] = []
        for folder in folders {
            let f = Array(folder.utf8)
            if f == path {
                own = true
            } else if path.starts(with: f), f.count > parent.count {
                parent = f
            }
        }
        if !parent.isEmpty {
            // Above its own flag: showing it is refused until the folder
            // above is shown.
            var p = String(decoding: parent, as: UTF8.self)
            if p.hasSuffix("/") { p.removeLast() }
            return .keptOutBy(p)
        }
        return own ? .keptOut : .shown
    }

    /// A folder's UploadOnlyRequestState from its pending request and
    /// whether the device can (nil while unknown). As otc-sync's
    /// engine.UploadOnlyRequestState.
    static func uploadOnlyState(pending: Bool?, supported: Bool?) -> UploadOnlyRequestState {
        switch (pending, supported) {
        case (nil, _): return .none
        case (_, false?): return .unsupported
        case (true?, _): return .making
        case (false?, _): return .lifting
        }
    }

    /// Marks a two-way sync record entry for a file deleted on this Mac
    /// that the device kept: its folder there is upload only (issue #132),
    /// so the delete was refused ("upload_only"). The entry holds the
    /// device's hash after the prefix and stands for two baselines - absent
    /// here, that hash on the device (recordBaselines) - so while neither
    /// side changes the file, nothing happens: it isn't downloaded again
    /// and the delete isn't sent again. A new version on the device comes
    /// down, a file put back here goes up, both at once are a conflict like
    /// any other, gone from the device too the entry goes, and once the
    /// folder is no longer upload only the file comes back here
    /// (baselines). An older version reading the record sees a hash that
    /// matches neither side and downloads the file, which is what it did
    /// before. Same marker as otc-sync's keptOnDevice.
    static let keptOnDevicePrefix = "kept-on-device:"

    static func keptOnDevice(_ hash: String) -> String { keptOnDevicePrefix + hash }

    /// What a sync record entry says each side had after the last pass: the
    /// same hash, or for a file kept on the device, nothing here and its
    /// hash there.
    static func recordBaselines(_ entry: String?) -> (local: String?, remote: String?) {
        guard let entry else { return (nil, nil) }
        if entry.hasPrefix(keptOnDevicePrefix) {
            return (nil, String(entry.dropFirst(keptOnDevicePrefix.count)))
        }
        return (entry, entry)
    }

    /// recordBaselines for a path the device lists with `listedUploadOnly`
    /// (its File.upload_only; nil when the device doesn't list the path). A
    /// file kept on the device whose folder is no longer upload only there -
    /// lifted later from the web or a phone - has no baseline any more: it
    /// comes back here, which deletes nothing on the device, and both sides
    /// match again. Sending the old delete instead would delete what the
    /// lock protected. As otc-sync's baselinesFor.
    static func baselines(_ entry: String?, listedUploadOnly: Bool?) -> (local: String?, remote: String?) {
        if let entry, entry.hasPrefix(keptOnDevicePrefix), listedUploadOnly == false {
            return (nil, nil)
        }
        return recordBaselines(entry)
    }

    private static func hasHiddenComponent(_ relative: String) -> Bool {
        var atStart = true
        for byte in relative.utf8 {
            if atStart && byte == UInt8(ascii: ".") { return true }
            atStart = byte == UInt8(ascii: "/")
        }
        return false
    }
}
