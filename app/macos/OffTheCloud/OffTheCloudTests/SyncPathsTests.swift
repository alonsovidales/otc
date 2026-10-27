// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
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

    // MARK: The scratch files neither client syncs

    @Test func scratchFilesAreNotSynced() {
        #expect(SyncPaths.isExcludedFromSync("~$report.docx"))
        #expect(SyncPaths.isExcludedFromSync("sub/~$book.xlsx"))
        #expect(SyncPaths.isExcludedFromSync("Thumbs.db"))
        #expect(SyncPaths.isExcludedFromSync("x/DESKTOP.INI"))
        #expect(SyncPaths.isExcludedFromSync("ehthumbs.db"))
        #expect(!SyncPaths.isExcludedFromSync("report~$.docx"))
        #expect(!SyncPaths.isExcludedFromSync("thumbs.db.txt"))
        #expect(SyncPaths.isSkippedBackupPath("/Users/me/Docs/~$cv.docx", root: "/Users/me/Docs"))
    }

    // MARK: One name, two normalizations

    @Test func keysAreNFCAndBytesAreCompared() {
        let nfd = "Re\u{301}sume\u{301}.pdf", nfc = "R\u{E9}sum\u{E9}.pdf"
        #expect(SyncPaths.sameBytes(SyncPaths.key(nfd), nfc))
        #expect(nfd == nfc)  // what Swift's String says
        #expect(!SyncPaths.sameBytes(nfd, nfc))  // what the device says
        #expect(SyncPaths.prefers(nfc, over: nfd, key: nfc))
        #expect(!SyncPaths.prefers(nfd, over: nfc, key: nfc))
        // A combining mark never makes a "/" or a "." out of nothing.
        #expect(SyncPaths.isSafeRelative(SyncPaths.key("a/..\u{301}/b")))
    }

    // MARK: A record names its device

    @Test func recordsMatchTheirDevice() {
        let a = SyncPaths.RecordDevice(domain: "cala.off-the.cloud", pin: "aa")
        #expect(SyncPaths.recordMatches(a, .init(domain: "ws://192.168.1.2:8080/ws", pin: "aa")))
        #expect(!SyncPaths.recordMatches(a, .init(domain: "cala.off-the.cloud", pin: "bb")))
        #expect(SyncPaths.recordMatches(a, .init(domain: "cala.off-the.cloud", pin: nil)))
        #expect(!SyncPaths.recordMatches(a, .init(domain: "pit.off-the.cloud", pin: nil)))
        #expect(SyncPaths.sameDevice("cala.off-the.cloud", "Cala.off-the.cloud"))
        #expect(SyncPaths.sameDevice("cala.off-the.cloud", "wss://cala.off-the.cloud/ws"))
        #expect(SyncPaths.sameDevice("ws://192.168.1.2:8080/ws", "WS://192.168.1.2:8080/ws/"))
        #expect(!SyncPaths.sameDevice("cala.off-the.cloud", "pit.off-the.cloud"))
    }

    @Test func recordFiles() throws {
        let device = SyncPaths.RecordDevice(domain: "d", pin: nil)
        let data = try #require(SyncPaths.encodeRecord(files: ["a": "h"], device: device))
        let read = try #require(SyncPaths.parseRecord(data))
        #expect(read.files == ["a": "h"] && read.device == device)
        // An older version's bare map: no device, as it always was.
        let old = try #require(SyncPaths.parseRecord(Data(#"{"v": "h1", "device": "h2"}"#.utf8)))
        #expect(old.device == nil && old.files["v"] == "h1" && old.files["device"] == "h2")
        #expect(SyncPaths.parseRecord(Data("not json".utf8)) == nil)
        // The same file otc-sync writes.
        let go = try #require(SyncPaths.parseRecord(Data(#"{"v":2,"device":{"domain":"cala.off-the.cloud","pin":"ab"},"files":{"x.txt":"h"}}"#.utf8)))
        #expect(go.device == SyncPaths.RecordDevice(domain: "cala.off-the.cloud", pin: "ab") && go.files == ["x.txt": "h"])
        // A Swift dictionary can't even hold both spellings (a literal with
        // both traps): an NFD key comes out in NFC bytes.
        let keys = SyncPaths.normalized(["Re\u{301}sume\u{301}.pdf": "h"])
        #expect(keys.count == 1 && SyncPaths.sameBytes(keys.keys.first!, "R\u{E9}sum\u{E9}.pdf"))
        // A file with both, from another program, reads as one entry.
        let twins = try #require(SyncPaths.parseRecord(Data(#"{"R\u00e9sum\u00e9.pdf": "nfc", "Re\u0301sume\u0301.pdf": "nfd"}"#.utf8)))
        #expect(twins.files.count == 1)
    }

    // MARK: A move on the device is no mass deletion

    @Test func movedHashesAreTheNewPaths() {
        let moved = SyncPaths.movedHashes(listed: ["Archive/a": "h1", "b": "h2", "lost": ""], record: ["Projects/a": "h1", "b": "h2"])
        #expect(moved == ["h1"])
    }

    // MARK: A folder is synced once

    @Test func foldersOverlap() {
        let synced = [(path: "/Users/me/Photos", name: "Photos"), (path: "/Volumes/R\u{E9}sum\u{E9}s/", name: "R\u{E9}sum\u{E9}s")]
        #expect(SyncPaths.overlap("/Users/me/Photos/", with: synced, ignoringCase: true) == .same("Photos"))
        #expect(SyncPaths.overlap("/Users/me/photos/2024", with: synced, ignoringCase: true) == .inside("Photos"))
        #expect(SyncPaths.overlap("/Users/me/photos/2024", with: synced, ignoringCase: false) == nil)
        #expect(SyncPaths.overlap("/Users/me", with: synced, ignoringCase: true) == .holds("Photos"))
        #expect(SyncPaths.overlap("/Users/me/Photos2", with: synced, ignoringCase: true) == nil)
        #expect(SyncPaths.overlap("/Volumes/Re\u{301}sume\u{301}s/cv", with: synced, ignoringCase: true) == .inside("R\u{E9}sum\u{E9}s"))
        #expect(SyncPaths.overlap("/", with: synced, ignoringCase: true) == .holds("Photos"))
    }

    // MARK: ws:// is for the home network

    @Test func unencryptedAddresses() {
        for local in ["ws://192.168.1.10:8080/ws", "ws://10.0.0.2/ws", "ws://172.20.0.5/ws", "ws://127.0.0.1:8080/ws",
                      "ws://[::1]:8080/ws", "ws://[fd12::5]/ws", "ws://169.254.3.4/ws", "ws://nas.local:8080/ws",
                      "ws://otc/ws", "ws://otc.lan/ws", "ws://otc.home.arpa/ws", "ws://localhost:8080/ws",
                      "wss://otc.example.com/ws", "cala.off-the.cloud"] {
            #expect(!SyncPaths.isUnencryptedRemote(local), "\(local)")
        }
        for remote in ["ws://203.0.113.7:8080/ws", "ws://otc.example.com/ws", "WS://[2001:db8::1]/ws"] {
            #expect(SyncPaths.isUnencryptedRemote(remote), "\(remote)")
        }
    }
}
