// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation

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

    private static func hasHiddenComponent(_ relative: String) -> Bool {
        var atStart = true
        for byte in relative.utf8 {
            if atStart && byte == UInt8(ascii: ".") { return true }
            atStart = byte == UInt8(ascii: "/")
        }
        return false
    }
}
