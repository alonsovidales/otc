// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation

/// Which paths a sync pass may act on. Free of SyncModel (and of anything
/// else) so the rules can be tested on their own.
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

    /// Not empty, not absolute, and no empty, "." or ".." component.
    static func isSafeRelative(_ relative: String) -> Bool {
        guard !relative.isEmpty, !relative.hasPrefix("/"), !relative.contains("\0") else { return false }
        return !relative.split(separator: "/", omittingEmptySubsequences: false)
            .contains { $0.isEmpty || $0 == "." || $0 == ".." }
    }

    /// Left out of a two-way pass on both sides: what the local scan never
    /// sees (a component starting with ".", as .skipsHiddenFiles) and
    /// downloads in progress. A dotfile listed on the device used to come
    /// down, look deleted here on the next pass, and be deleted there.
    /// As otc-sync's notSynced.
    static func isExcludedFromSync(_ relative: String) -> Bool {
        if relative.hasSuffix(".otc-part") { return true }
        return relative.split(separator: "/").contains { $0.hasPrefix(".") }
    }
}
