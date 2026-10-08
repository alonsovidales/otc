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
        // A combining mark after "/" makes it one Character with the
        // slash, but the file system still splits there and sees "..".
        #expect(SyncPaths.safeRelative(prefix + "../\u{301}etc/x", under: prefix) == nil)
        #expect(SyncPaths.safeRelative(prefix + "a/..\u{301}/b", under: prefix) == "a/..\u{301}/b")
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
        #expect(!SyncPaths.isSafeRelative("."))
        #expect(!SyncPaths.isSafeRelative("a/."))
        #expect(SyncPaths.isSafeRelative("..."))
        #expect(SyncPaths.isSafeRelative("a/.b/c"))
    }

    @Test func twoWayLeavesOutWhatTheLocalScanSkips() {
        #expect(SyncPaths.isExcludedFromSync(".env"))
        #expect(SyncPaths.isExcludedFromSync("project/.git/config"))
        #expect(SyncPaths.isExcludedFromSync("a/b.jpg.otc-part"))
        #expect(!SyncPaths.isExcludedFromSync("a/b.jpg"))
        #expect(!SyncPaths.isExcludedFromSync("notes.v2/x.txt"))
    }

    /// Issue #192, as otc-sync's TestOutOfImagesState.
    @Test func outOfImagesState() {
        let folders = ["/mac/Studio/Users/me/Scans/", "/Photos/"]
        func state(_ path: String, _ list: [String]?, pending: Bool? = nil, supported: Bool? = true) -> OutOfImagesState {
            SyncPaths.outOfImagesState(devicePath: path, folders: list, pending: pending, supported: supported)
        }
        #expect(state("/Photos/", nil, supported: nil) == .unknown)
        #expect(state("/Photos/", folders) == .keptOut)
        #expect(state("/Photos/", folders, supported: nil) == .keptOut)
        #expect(state("/Photos/Trip/", folders) == .keptOutBy("/Photos"))
        #expect(state("/Photos/Trip/", folders + ["/Photos/Trip/"]) == .keptOutBy("/Photos"))
        #expect(state("/a/b/c/", ["/a/", "/a/b/"]) == .keptOutBy("/a/b"))
        #expect(state("/Photos2/", folders) == .shown)
        #expect(state("/photos/", folders) == .shown)
        #expect(state("/Documents/", []) == .shown)
        // Byte for byte, as the device: a decomposed é is another folder.
        #expect(state("/Cafe\u{301}/", ["/Café/"]) == .shown)
        #expect(state("/Café/", ["/Café/"]) == .keptOut)
        #expect(state("/Documents/", folders, pending: true) == .keeping)
        #expect(state("/Photos/", folders, pending: false) == .showing)
        #expect(state("/Documents/", nil, pending: true, supported: nil) == .keeping)
        #expect(state("/Documents/", nil, pending: true, supported: false) == .unsupported)
        #expect(state("/Documents/", nil, supported: false) == .unknown)
        #expect(OutOfImagesState.keeping.isKeptOut && OutOfImagesState.keptOutBy("/a").isKeptOut)
        #expect(!OutOfImagesState.showing.isKeptOut && !OutOfImagesState.unsupported.isKeptOut)
    }

    /// Issue #132, as otc-sync's TestUploadOnlyRequestState.
    @Test func uploadOnlyState() {
        #expect(SyncPaths.uploadOnlyState(pending: nil, supported: nil) == .none)
        #expect(SyncPaths.uploadOnlyState(pending: nil, supported: false) == .none)
        #expect(SyncPaths.uploadOnlyState(pending: true, supported: nil) == .making)
        #expect(SyncPaths.uploadOnlyState(pending: true, supported: true) == .making)
        #expect(SyncPaths.uploadOnlyState(pending: false, supported: true) == .lifting)
        #expect(SyncPaths.uploadOnlyState(pending: true, supported: false) == .unsupported)
    }

    /// A file deleted here that the device kept (an upload-only folder):
    /// its record entry stands for nothing here and the device's hash
    /// there. The same marker as otc-sync's, so a record reads the same.
    @Test func keptOnDeviceRecord() {
        let hash = String(repeating: "ab", count: 32)
        #expect(SyncPaths.keptOnDevice(hash) == "kept-on-device:" + hash)
        let kept = SyncPaths.recordBaselines(SyncPaths.keptOnDevice(hash))
        #expect(kept.local == nil && kept.remote == hash)
        let plain = SyncPaths.recordBaselines(hash)
        #expect(plain.local == hash && plain.remote == hash)
        let none = SyncPaths.recordBaselines(nil)
        #expect(none.local == nil && none.remote == nil)
    }

    /// Upload only lifted later (the listing no longer says upload_only):
    /// a file kept on the device has no baseline any more, so it comes back
    /// here instead of the old delete going. As otc-sync's TestBaselinesFor.
    @Test func keptOnDeviceAfterUploadOnlyIsLifted() {
        let hash = String(repeating: "cd", count: 32)
        let kept = SyncPaths.keptOnDevice(hash)
        let still = SyncPaths.baselines(kept, listedUploadOnly: true)
        #expect(still.local == nil && still.remote == hash)
        let lifted = SyncPaths.baselines(kept, listedUploadOnly: false)
        #expect(lifted.local == nil && lifted.remote == nil)
        let gone = SyncPaths.baselines(kept, listedUploadOnly: nil)
        #expect(gone.local == nil && gone.remote == hash)
        let plain = SyncPaths.baselines(hash, listedUploadOnly: false)
        #expect(plain.local == hash && plain.remote == hash)
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
