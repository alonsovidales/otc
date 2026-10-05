// SPDX-License-Identifier: AGPL-3.0-or-later

import Testing
@testable import OffTheCloud

struct SyncPathsTests {
    let prefix = "/mac/Studio/Users/me/Photos/"

    @Test func listingPathsInsideTheFolder() {
        #expect(SyncPaths.safeRelative(prefix + "a.jpg", under: prefix) == "a.jpg")
        #expect(SyncPaths.safeRelative(prefix + "2026/trip/a.jpg", under: prefix) == "2026/trip/a.jpg")
        #expect(SyncPaths.safeRelative(prefix + ".hidden", under: prefix) == ".hidden")
        #expect(SyncPaths.safeRelative(prefix + "a/..b/c", under: prefix) == "a/..b/c")
    }

    @Test func listingPathsLeadingOutsideAreRefused() {
        #expect(SyncPaths.safeRelative(prefix + "../../Code/build.sh", under: prefix) == nil)
        #expect(SyncPaths.safeRelative(prefix + "a/../../b", under: prefix) == nil)
        #expect(SyncPaths.safeRelative(prefix + "./a", under: prefix) == nil)
        #expect(SyncPaths.safeRelative(prefix + "a//b", under: prefix) == nil)
        #expect(SyncPaths.safeRelative(prefix + "a/", under: prefix) == nil)
        #expect(SyncPaths.safeRelative(prefix + "/etc/x", under: prefix) == nil)
        #expect(SyncPaths.safeRelative(prefix + "a\0b", under: prefix) == nil)
        #expect(SyncPaths.safeRelative(prefix, under: prefix) == nil)
        // Not under the prefix at all: never taken as a relative path.
        #expect(SyncPaths.safeRelative("/mac/Studio/Users/me/Other/a.jpg", under: prefix) == nil)
        #expect(SyncPaths.safeRelative("a.jpg", under: prefix) == nil)
    }

    @Test func storedBaselineKeys() {
        #expect(SyncPaths.isSafeRelative("a/b.txt"))
        #expect(!SyncPaths.isSafeRelative("../x"))
        #expect(!SyncPaths.isSafeRelative("a/.."))
        #expect(!SyncPaths.isSafeRelative(""))
    }

    @Test func twoWayLeavesOutWhatTheLocalScanSkips() {
        #expect(SyncPaths.isExcludedFromSync(".env"))
        #expect(SyncPaths.isExcludedFromSync("project/.git/config"))
        #expect(SyncPaths.isExcludedFromSync("a/b.jpg.otc-part"))
        #expect(!SyncPaths.isExcludedFromSync("a/b.jpg"))
        #expect(!SyncPaths.isExcludedFromSync("notes.v2/x.txt"))
    }

    @Test func backupWatcherSkipsWhatReconcileSkips() {
        let root = "/Users/me/Documents"
        #expect(SyncPaths.isSkippedBackupPath(root + "/.DS_Store", root: root))
        #expect(SyncPaths.isSkippedBackupPath(root + "/code/.git/index", root: root))
        #expect(SyncPaths.isSkippedBackupPath(root + "/a.pdf.otc-part", root: root))
        #expect(!SyncPaths.isSkippedBackupPath(root + "/report.pdf", root: root))
        // A backup inside a dot directory is still backed up.
        #expect(!SyncPaths.isSkippedBackupPath("/Users/me/.dotfiles/zshrc", root: "/Users/me/.dotfiles"))
        // Spelled differently by FSEvents: the file name decides.
        #expect(!SyncPaths.isSkippedBackupPath("/private/var/x/report.pdf", root: "/var/x"))
        #expect(SyncPaths.isSkippedBackupPath("/private/var/x/.DS_Store", root: "/var/x"))
    }
}
