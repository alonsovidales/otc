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

    /// A record keeps what it says about the folder here (its marker's
    /// token, the disks mounted in it), in otc-sync's keys; a record without
    /// them reads as before.
    @Test func recordsKeepTheFolderTheyWereMadeWith() throws {
        let device = SyncPaths.RecordDevice(domain: "cala.off-the.cloud", pin: "ab")
        let meta = SyncPaths.FolderMeta(root: "0123456789abcdef0123456789abcdef", mounts: ["nas"], maybeMounts: ["usb"])
        let data = try #require(SyncPaths.encodeRecord(files: ["a": "h"], device: device, meta: meta))
        let json = try #require(String(data: data, encoding: .utf8))
        #expect(json.contains(#""maybe_mounts":["usb"]"#) && json.contains(#""root":"0123456789abcdef0123456789abcdef""#))
        let read = try #require(SyncPaths.parseRecord(data))
        #expect(read.meta == meta && read.files == ["a": "h"] && read.device == device)
        // Nothing to say: the keys are left out, as otc-sync's omitempty.
        let bare = try #require(SyncPaths.encodeRecord(files: [:], device: device))
        let bareJSON = try #require(String(data: bare, encoding: .utf8))
        #expect(!bareJSON.contains("root") && !bareJSON.contains("mounts"))
        #expect(try #require(SyncPaths.parseRecord(bare)).meta == SyncPaths.FolderMeta())
        // otc-sync's file.
        let go = try #require(SyncPaths.parseRecord(Data(#"{"v":2,"device":{"domain":"d"},"root":"t","mounts":["m"],"maybe_mounts":["x"],"files":{"f":"h"}}"#.utf8)))
        #expect(go.meta == SyncPaths.FolderMeta(root: "t", mounts: ["m"], maybeMounts: ["x"]))
        // An older version's bare map knows nothing of the folder.
        #expect(try #require(SyncPaths.parseRecord(Data(#"{"f": "h"}"#.utf8))).meta == SyncPaths.FolderMeta())
    }

    /// A record goes by the pin this sign-in confirmed: one that names its
    /// device by a pin, with none confirmed and the same address, is
    /// uncertain - and keeps its pin. As otc-sync's TestRecordUncertain.
    @Test func aRecordPinnedToItsDeviceWaitsForThePin() {
        let pinned = SyncPaths.RecordDevice(domain: "cala.off-the.cloud", pin: "aa")
        let files = ["a.jpg": "h"]
        let unconfirmed = SyncPaths.recordFor(stored: files, made: pinned, device: .init(domain: "cala.off-the.cloud", pin: nil))
        #expect(unconfirmed.files == files && unconfirmed.uncertain && unconfirmed.save == pinned && !unconfirmed.otherDevice)
        // Nothing in it: nothing to hold.
        #expect(!SyncPaths.recordFor(stored: [:], made: pinned, device: .init(domain: "cala.off-the.cloud", pin: nil)).uncertain)
        let same = SyncPaths.recordFor(stored: files, made: pinned, device: .init(domain: "cala.off-the.cloud", pin: "aa"))
        #expect(same.files == files && !same.uncertain && same.save.pin == "aa")
        // Set up again from scratch under the same name: another pin.
        let other = SyncPaths.recordFor(stored: files, made: pinned, device: .init(domain: "cala.off-the.cloud", pin: "bb"))
        #expect(other.files.isEmpty && other.otherDevice && other.save.pin == "bb")
        // Another address with no pin: another device, as before.
        let elsewhere = SyncPaths.recordFor(stored: files, made: pinned, device: .init(domain: "pit.off-the.cloud", pin: nil))
        #expect(elsewhere.files.isEmpty && elsewhere.otherDevice && !elsewhere.uncertain)
        // An older version's record: the configured device's.
        let old = SyncPaths.recordFor(stored: files, made: nil, device: .init(domain: "cala.off-the.cloud", pin: nil))
        #expect(old.files == files && !old.uncertain && !old.otherDevice)
    }

    // MARK: Which folder a record was made with

    @Test func theMarkerHoldsItsToken() throws {
        let token = SyncPaths.newMarkerToken()
        #expect(token.count == 32 && token.allSatisfy(\.isHexDigit) && token != SyncPaths.newMarkerToken())
        let data = try #require(SyncPaths.markerData(token: token))
        #expect(SyncPaths.markerToken(data) == token)
        #expect(SyncPaths.markerToken(Data("not json".utf8)) == "")
        #expect(SyncPaths.markerToken(Data(#"{"token": "  t \n"}"#.utf8)) == "")
        #expect(SyncPaths.markerToken(Data(#"{"note": "n", "token": "  t \n"}"#.utf8)) == "t")
        let id = UUID()
        #expect(SyncPaths.markerName(id) == ".otc-sync-" + id.uuidString)
        // Dot-named: never synced, either way.
        #expect(SyncPaths.isExcludedFromSync(SyncPaths.markerName(id)))
        let dir = try scratchDirectory()
        defer { try? FileManager.default.removeItem(at: dir) }
        #expect(SyncPaths.readMarker(in: dir, id: id) == "")
        try data.write(to: dir.appendingPathComponent(SyncPaths.markerName(id)))
        #expect(SyncPaths.readMarker(in: dir, id: id) == token)
        #expect(SyncPaths.readMarker(in: dir, id: UUID()) == "")
    }

    /// As otc-sync's TestJudgeRoot.
    @Test func aFolderIsTheSyncedOneByItsMarkerOrMostOfItsFiles() {
        let record = ["a": "h1", "b": "h2", "c": "h3", "d": "h4", "k": SyncPaths.keptOnDevice("h5")]
        let none: (String) -> Bool = { _ in false }
        // The marker the record keeps: nothing else looked at.
        #expect(SyncPaths.judgeRoot(recorded: "t", marker: "t", record: record, here: [:], unknown: none) == .init(ok: true, token: "t"))
        // No marker: half the files unchanged is enough, and the marker is
        // written again with the record's token.
        #expect(SyncPaths.judgeRoot(recorded: "t", marker: "", record: record, here: ["a": "h1", "b": "h2"], unknown: none)
                == .init(ok: true, token: "t", mark: true))
        // Fewer: not the synced folder. A file changed doesn't count.
        #expect(!SyncPaths.judgeRoot(recorded: "t", marker: "", record: record, here: ["a": "h1", "b": "other"], unknown: none).ok)
        #expect(!SyncPaths.judgeRoot(recorded: "t", marker: "x", record: record, here: [:], unknown: none).ok)
        // The empty folder of a disk that isn't mounted, a fresh folder.
        #expect(!SyncPaths.judgeRoot(recorded: "", marker: "", record: record, here: [:], unknown: none).ok)
        // What is unknown here counts neither way.
        let unknown: (String) -> Bool = { $0 == "c" || $0 == "d" }
        #expect(SyncPaths.judgeRoot(recorded: "", marker: "m", record: record, here: ["a": "h1"], unknown: unknown)
                == .init(ok: true, token: "m", mark: true))
        // A record with nothing to lose: a new token, written.
        let fresh = SyncPaths.judgeRoot(recorded: "", marker: "", record: [:], here: [:], unknown: none)
        #expect(fresh.ok && fresh.mark && fresh.token.count == 32)
        let kept = SyncPaths.judgeRoot(recorded: "", marker: "", record: ["k": SyncPaths.keptOnDevice("h")], here: [:], unknown: none)
        #expect(kept.ok && kept.mark)
        // How far the hashing goes.
        let evidence = SyncPaths.rootEvidence(record: record, present: { $0 != "b" }, unknown: { $0 == "d" })
        #expect(evidence.listed == 3 && evidence.present == ["a", "c"])
        #expect(SyncPaths.rootSettled(listed: 4, unchanged: 2, left: 2))
        #expect(!SyncPaths.rootSettled(listed: 4, unchanged: 1, left: 1))
        #expect(SyncPaths.rootSettled(listed: 4, unchanged: 0, left: 1))
    }

    // MARK: What a two-way pass sees here

    @Test func theScanSkipsLinksAndHiddenFilesWithoutLosingThem() throws {
        let dir = try scratchDirectory()
        defer { try? FileManager.default.removeItem(at: dir) }
        let fm = FileManager.default
        func file(_ rel: String) throws {
            let url = dir.appendingPathComponent(rel)
            try fm.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            try Data("x".utf8).write(to: url)
        }
        try file("a.jpg")
        try file("year/b.jpg")
        try file(".DS_Store")
        try file(".git/config")
        try file("c.jpg.1a2b3c4d.otc-part")
        try file("Thumbs.db")
        try file("hidden.jpg")
        try file("secret/d.jpg")
        try fm.createSymbolicLink(atPath: dir.appendingPathComponent("link.jpg").path, withDestinationPath: "a.jpg")
        try fm.createSymbolicLink(atPath: dir.appendingPathComponent("linked").path, withDestinationPath: "year")
        #expect(mkfifo(dir.appendingPathComponent("pipe").path, 0o600) == 0)
        // Hidden in the Finder (chflags hidden).
        #expect(chflags(dir.appendingPathComponent("hidden.jpg").path, UInt32(UF_HIDDEN)) == 0)
        #expect(chflags(dir.appendingPathComponent("secret").path, UInt32(UF_HIDDEN)) == 0)
        let scan = SyncPaths.scanFolder(at: dir)
        let root = dir.standardizedFileURL.path
        let files = Set(scan.files.compactMap { SyncPaths.relativePath($0.standardizedFileURL.path, root: root) })
        #expect(files == ["a.jpg", "year/b.jpg"])
        #expect(Set(scan.skipped) == ["link.jpg", "linked", "pipe", "hidden.jpg", "secret"])
        #expect(scan.mounts.isEmpty && scan.failed.isEmpty && scan.complete)
    }

    /// The enumerator's skipDescendants skips the folder it returned last:
    /// called for a hidden file or a dotfile listed right after a folder, it
    /// left that folder's files out of the scan - deleted here, as far as a
    /// pass could tell.
    @Test func aHiddenFileNextToAFolderLeavesTheFolderScanned() throws {
        let dir = try scratchDirectory()
        defer { try? FileManager.default.removeItem(at: dir) }
        let fm = FileManager.default
        var expected = Set<String>()
        for i in 0..<40 {
            try fm.createDirectory(at: dir.appendingPathComponent("d\(i)"), withIntermediateDirectories: true)
            try Data("x".utf8).write(to: dir.appendingPathComponent("d\(i)/f.jpg"))
            expected.insert("d\(i)/f.jpg")
            try Data("x".utf8).write(to: dir.appendingPathComponent(".dot\(i)"))
            let hidden = dir.appendingPathComponent("h\(i).jpg")
            try Data("x".utf8).write(to: hidden)
            #expect(chflags(hidden.path, UInt32(UF_HIDDEN)) == 0)
        }
        let scan = SyncPaths.scanFolder(at: dir)
        let root = dir.standardizedFileURL.path
        #expect(Set(scan.files.compactMap { SyncPaths.relativePath($0.standardizedFileURL.path, root: root) }) == expected)
        #expect(scan.skipped.count == 40)
    }

    @Test func aFolderThatCantBeReadIsUnknownNotEmpty() throws {
        let dir = try scratchDirectory()
        let locked = dir.appendingPathComponent("locked")
        try FileManager.default.createDirectory(at: locked, withIntermediateDirectories: true)
        try Data("x".utf8).write(to: locked.appendingPathComponent("a.jpg"))
        #expect(chmod(locked.path, 0) == 0)
        defer {
            chmod(locked.path, 0o755)
            try? FileManager.default.removeItem(at: dir)
        }
        let scan = SyncPaths.scanFolder(at: dir)
        #expect(scan.files.isEmpty && scan.rootReadable && !scan.complete)
        #expect(scan.failed.map { $0.lastPathComponent } == ["locked"])
        #expect(!SyncPaths.scanFolder(at: dir.appendingPathComponent("gone")).rootReadable)
    }

    @Test func pathSetsCoverWhatIsInside() {
        let set = SyncPaths.PathSet(["nas", "a/b"])
        #expect(set.covers("nas") && set.covers("nas/x/y.jpg") && set.covers("a/b/c"))
        #expect(!set.covers("nas2/x") && !set.covers("a") && !set.covers("a/bc"))
        #expect(!SyncPaths.PathSet().covers("x"))
        // The two normalizations are one path.
        #expect(SyncPaths.PathSet(["Caf\u{E9}"]).covers("Cafe\u{301}/x.jpg"))
        #expect(SyncPaths.parentKey("a/b/c") == "a/b" && SyncPaths.parentKey("c") == nil)
        #expect(SyncPaths.ancestors("a/b/c") == ["a/b", "a"])
        #expect(SyncPaths.relativePath("/r/a/b", root: "/r") == "a/b" && SyncPaths.relativePath("/rx/a", root: "/r") == nil)
        #expect(SyncPaths.relativePath("/r", root: "/r") == "" && SyncPaths.relativePath("/x", root: "/") == "x")
        #expect(SyncPaths.relativePath("/r/\u{301}x", root: "/r") == "\u{301}x")
    }

    // MARK: A disk mounted inside a two-way folder

    @Test func aMountThatIsAPlainFolderAgainIsHeld() throws {
        let dir = try scratchDirectory()
        defer { try? FileManager.default.removeItem(at: dir) }
        try FileManager.default.createDirectory(at: dir.appendingPathComponent("nas"), withIntermediateDirectories: true)
        let root = dir.standardizedFileURL.path
        let (gone, keep) = SyncPaths.goneMounts(root: root, recorded: ["nas", "removed", "../x"], now: ["usb"])
        #expect(gone == ["nas"] && keep == ["nas", "usb"])
        let (none, still) = SyncPaths.goneMounts(root: root, recorded: ["nas"], now: ["nas"])
        #expect(none.isEmpty && still == ["nas"])
    }

    /// As otc-sync's TestMaybeMounts.
    @Test func aRecordFromBeforeMountsHoldsAnUntouchedEmptyFolder() throws {
        let dir = try scratchDirectory()
        defer { try? FileManager.default.removeItem(at: dir) }
        let fm = FileManager.default
        for d in ["usb", "photos", "emptied"] { try fm.createDirectory(at: dir.appendingPathComponent(d), withIntermediateDirectories: true) }
        try Data("x".utf8).write(to: dir.appendingPathComponent("photos/a.jpg"))
        let root = dir.standardizedFileURL.path
        let record = ["usb/1.jpg": "h", "usb/sub/2.jpg": "h", "photos/a.jpg": "h", "photos/old.jpg": "h", "emptied/3.jpg": "h",
                      "gone/4.jpg": "h", "kept/5.jpg": SyncPaths.keptOnDevice("h")]
        let here: Set<String> = ["photos/a.jpg"]
        let none: (String) -> Bool = { _ in false }
        // The record was written after the folders were last changed...
        let later = Date().addingTimeInterval(60)
        #expect(SyncPaths.emptiedFolders(root: root, record: record, here: here, known: none, written: later) == ["emptied", "usb"])
        #expect(SyncPaths.maybeMounts(root: root, meta: .init(), record: record, here: here, known: none, written: later) == ["emptied", "usb"])
        // ... not before: a folder emptied by hand since is newer.
        let before = Date().addingTimeInterval(-60)
        #expect(SyncPaths.emptiedFolders(root: root, record: record, here: here, known: none, written: before).isEmpty)
        // Not read from a file (this run wrote it): nothing.
        #expect(SyncPaths.emptiedFolders(root: root, record: record, here: here, known: none, written: nil).isEmpty)
        // What is known otherwise (unreadable, a link) is not a reason.
        #expect(SyncPaths.emptiedFolders(root: root, record: record, here: here, known: { $0.hasPrefix("usb/") }, written: later) == ["emptied"])
        // Once the record has its root, only those it keeps, while they stay
        // empty and the record lists files under them.
        let meta = SyncPaths.FolderMeta(root: "t", mounts: [], maybeMounts: ["usb", "photos", "gone"])
        #expect(SyncPaths.maybeMounts(root: root, meta: meta, record: record, here: here, known: none, written: later) == ["usb"])
        #expect(SyncPaths.maybeMounts(root: root, meta: meta, record: ["x.jpg": "h"], here: here, known: none, written: later).isEmpty)
        #expect(SyncPaths.maybeMounts(root: root, meta: meta, record: record, here: here.union(["usb/new.jpg"]), known: none, written: later).isEmpty)
    }

    // MARK: A folder renamed only in case on the device

    /// As otc-sync's TestFolderRenamedInCaseOnTheDevice, on this Mac's
    /// disk, which ignores case unless it was formatted otherwise.
    @Test func aFolderRenamedInCaseOnTheDeviceIsRenamedHere() throws {
        let dir = try scratchDirectory()
        defer { try? FileManager.default.removeItem(at: dir) }
        let fm = FileManager.default
        try fm.createDirectory(at: dir.appendingPathComponent("photos/trip"), withIntermediateDirectories: true)
        try Data("x".utf8).write(to: dir.appendingPathComponent("photos/trip/a.jpg"))
        let root = dir.standardizedFileURL.path
        guard fm.fileExists(atPath: root + "/PHOTOS") else { return } // a disk that minds case renames nothing
        let local = ["photos/trip/a.jpg": "photos/trip/a.jpg"]
        // Synced under this folder's spelling, renamed on the device: one
        // level a call, the shallowest first.
        let renames = SyncPaths.matchFolderCase(root: root, local: local, remote: ["Photos/Trip/a.jpg"], record: ["photos/trip/a.jpg": "h"])
        #expect(renames == [.init(from: "photos", to: "Photos", fromDir: root + "/photos", toDir: root + "/Photos")])
        #expect(try fm.contentsOfDirectory(atPath: root) == ["Photos"])
        let record = SyncPaths.renamedRecord(["photos/trip/a.jpg": "h", "other.jpg": "o"], renames)
        #expect(record == ["Photos/trip/a.jpg": "h", "other.jpg": "o"] && record.keys.contains { SyncPaths.sameBytes($0, "Photos/trip/a.jpg") })
        #expect(SyncPaths.renamedKey("photos/x", renames) == "Photos/x" && SyncPaths.renamedKey("photosx/y", renames) == "photosx/y")
        let next = SyncPaths.matchFolderCase(root: root, local: ["Photos/trip/a.jpg": "Photos/trip/a.jpg"], remote: ["Photos/Trip/a.jpg"], record: record)
        #expect(next.map(\.to) == ["Photos/Trip"])
        // Renamed here (Finder): the record has the device's spelling only.
        let mine = SyncPaths.matchFolderCase(root: root, local: ["Photos/Trip/a.jpg": "Photos/Trip/a.jpg"], remote: ["PHOTOS/Trip/a.jpg"],
                                             record: ["PHOTOS/Trip/a.jpg": "h"])
        #expect(mine.isEmpty)
        // Both spellings on the device: left alone.
        let twins = SyncPaths.matchFolderCase(root: root, local: ["Photos/Trip/a.jpg": "Photos/Trip/a.jpg"], remote: ["PHOTOS/a", "photos/b"], record: [:])
        #expect(twins.isEmpty)
    }

    @Test func aDeviceDeleteLooksForThatVeryName() throws {
        let dir = try scratchDirectory()
        defer { try? FileManager.default.removeItem(at: dir) }
        try FileManager.default.createDirectory(at: dir.appendingPathComponent("Trip"), withIntermediateDirectories: true)
        try Data("x".utf8).write(to: dir.appendingPathComponent("Trip/Caf\u{E9}.jpg"))
        let root = dir.standardizedFileURL.path
        #expect(SyncPaths.presentAsNamed(root: root, rel: "Trip/Caf\u{E9}.jpg"))
        #expect(SyncPaths.presentAsNamed(root: root, rel: "Trip/Cafe\u{301}.jpg"))
        // Another case: a rename made here, which goes up.
        #expect(!SyncPaths.presentAsNamed(root: root, rel: "trip/Caf\u{E9}.jpg"))
        #expect(!SyncPaths.presentAsNamed(root: root, rel: "Trip/caf\u{E9}.jpg"))
        #expect(!SyncPaths.presentAsNamed(root: root, rel: "Trip/gone.jpg"))
        // A link counts: something is there.
        try FileManager.default.createSymbolicLink(atPath: root + "/link.jpg", withDestinationPath: "nowhere")
        #expect(SyncPaths.presentAsNamed(root: root, rel: "link.jpg"))
    }

    // MARK: A conflict copy is made once

    @Test func conflictCopiesAreKnownByTheirName() {
        #expect(SyncPaths.isDeviceConflictCopy("report (conflict 2026-11-04 10.16).txt", of: "report.txt"))
        #expect(SyncPaths.isDeviceConflictCopy("report (conflict 2026-11-04 10.16 2).txt", of: "report.txt"))
        #expect(SyncPaths.isDeviceConflictCopy("a.b (conflict 2026-11-04 10.16).gz", of: "a.b.gz"))
        #expect(SyncPaths.isDeviceConflictCopy("notes (conflict 2026-11-04 10.16)", of: "notes"))
        // This Mac's own version, kept the other way, isn't one.
        #expect(!SyncPaths.isDeviceConflictCopy("report (conflict from Mac 2026-11-04 10.16).txt", of: "report.txt"))
        #expect(!SyncPaths.isDeviceConflictCopy("report (conflict 2026-11-04 10.16).doc", of: "report.txt"))
        #expect(!SyncPaths.isDeviceConflictCopy("rep (conflict 2026-11-04 10.16).txt", of: "report.txt"))
        #expect(!SyncPaths.isDeviceConflictCopy("re.port (conflict 2026-11-04 10.16).txt", of: "re+port.txt"))
        #expect(!SyncPaths.isDeviceConflictCopy("report (conflict 2026-11-04 10.16).txt.otc-part", of: "report.txt"))
    }

    // MARK: Where a deleted file goes when there is no Trash

    @Test func aFolderOfItsOwnTakesWhatTheTrashCant() throws {
        let dir = try scratchDirectory()
        defer { try? FileManager.default.removeItem(at: dir) }
        let fm = FileManager.default
        let root = dir.standardizedFileURL.path
        try fm.createDirectory(atPath: root + "/sub", withIntermediateDirectories: true)
        try Data("1".utf8).write(to: URL(fileURLWithPath: root + "/sub/a.txt"))
        let now = Date()
        let day = SyncPaths.trashDay(now)
        #expect(try SyncPaths.folderTrash(root + "/sub/a.txt", root: root, now: now) == root + "/.otc-trash/\(day)/sub/a.txt")
        try Data("2".utf8).write(to: URL(fileURLWithPath: root + "/sub/a.txt"))
        #expect(try SyncPaths.folderTrash(root + "/sub/a.txt", root: root, now: now) == root + "/.otc-trash/\(day)/sub/a 2.txt")
        #expect(!fm.fileExists(atPath: root + "/sub/a.txt"))
        // Never synced: dot-named.
        #expect(SyncPaths.isExcludedFromSync(".otc-trash/\(day)/sub/a.txt"))
        #expect(SyncPaths.trashTop(root + "/sub/x", root: root) == root)
        // Days older than 30 go after a pass; the rest stays.
        let old = SyncPaths.trashDay(now.addingTimeInterval(-40 * 86400))
        try fm.createDirectory(atPath: root + "/.otc-trash/\(old)/x", withIntermediateDirectories: true)
        try fm.createDirectory(atPath: root + "/.otc-trash/notes", withIntermediateDirectories: true)
        SyncPaths.pruneFolderTrashes(root: root, mounts: [], now: now)
        #expect(Set(try fm.contentsOfDirectory(atPath: root + "/.otc-trash")) == [day, "notes"])
        // Once nothing is left, the folder goes too.
        SyncPaths.pruneFolderTrash(root, now: now.addingTimeInterval(40 * 86400))
        try fm.removeItem(atPath: root + "/.otc-trash/notes")
        SyncPaths.pruneFolderTrash(root, now: now.addingTimeInterval(40 * 86400))
        #expect(!fm.fileExists(atPath: root + "/.otc-trash"))
    }

    // MARK: A download needs room; what keeps failing waits

    @Test func aDownloadThatCantFitIsRefused() throws {
        let gb: Int64 = 1 << 30
        #expect(SyncPaths.roomProblem(size: 5 * gb, free: 100 * gb, maxFile: SyncPaths.fatMaxFile) == .tooLarge)
        #expect(SyncPaths.roomProblem(size: SyncPaths.fatMaxFile, free: 100 * gb, maxFile: SyncPaths.fatMaxFile) == nil)
        let full = SyncPaths.roomProblem(size: 20 * gb, free: 10 * gb, maxFile: .max)
        let needed: Int64 = 20 * 1024 + 256
        #expect(full == .noSpace(freeMB: 10 * 1024, neededMB: needed))
        // The margin is left free.
        #expect(SyncPaths.roomProblem(size: gb, free: gb + SyncPaths.spaceReserve - 1, maxFile: .max) != nil)
        #expect(SyncPaths.roomProblem(size: gb, free: gb + SyncPaths.spaceReserve, maxFile: .max) == nil)
        // A volume that doesn't say how big it is refuses nothing.
        #expect(SyncPaths.roomProblem(size: 20 * gb, free: nil, maxFile: .max) == nil)
        let dir = try scratchDirectory()
        defer { try? FileManager.default.removeItem(at: dir) }
        let limits = try #require(SyncPaths.volumeLimits(dir))
        #expect(limits.maxFile == .max && (limits.free ?? 0) > 0)
        #expect(SyncPaths.failDelay(after: nil) == 120 && SyncPaths.failDelay(after: 120) == 240 && SyncPaths.failDelay(after: 3000) == 3600)
    }

    @Test func aPassSaysTheMostPressingFirst() {
        var failures = SyncPaths.PassFailures()
        #expect(SyncPaths.passNote(held: 0, unconfirmed: 0, failures: failures, unreadable: 0, restored: 0, notSyncedHere: 0) == nil)
        #expect(SyncPaths.passNote(held: 0, unconfirmed: 0, failures: failures, unreadable: 0, restored: 0, notSyncedHere: 2) == .notSyncedHere(2))
        #expect(SyncPaths.passNote(held: 0, unconfirmed: 0, failures: failures, unreadable: 0, restored: 30, notSyncedHere: 2) == .restored(30))
        #expect(SyncPaths.passNote(held: 0, unconfirmed: 0, failures: failures, unreadable: 3, restored: 30, notSyncedHere: 2) == .unreadable(3))
        failures.add(.other)
        failures.add(.tooLarge)
        #expect(SyncPaths.passNote(held: 0, unconfirmed: 0, failures: failures, unreadable: 3, restored: 0, notSyncedHere: 0) == .tooLarge(1))
        failures.add(.noSpace)
        #expect(SyncPaths.passNote(held: 0, unconfirmed: 0, failures: failures, unreadable: 3, restored: 0, notSyncedHere: 0) == .noSpace(1))
        #expect(SyncPaths.passNote(held: 0, unconfirmed: 0, failures: SyncPaths.PassFailures(other: 2), unreadable: 0, restored: 0, notSyncedHere: 0) == .filesFailed(2))
        #expect(SyncPaths.passNote(held: 0, unconfirmed: 4, failures: failures, unreadable: 0, restored: 0, notSyncedHere: 0) == .deviceUnconfirmed(4))
        #expect(SyncPaths.passNote(held: 25, unconfirmed: 4, failures: failures, unreadable: 0, restored: 0, notSyncedHere: 0) == .deletesHeld(25))
    }

    // MARK: Mass deletes here are held on the device

    /// As otc-sync's TestMassDeleteHereIsHeld.
    @Test func manyFilesGoneFromHereAtOnceStayOnTheDevice() {
        let deletes = (0..<30).map { (relative: "f\($0)", hash: "h\($0)") }
        #expect(SyncPaths.heldDeviceDeletes(deletes, deviceFiles: 100, here: []).count == 30)
        // Not a quarter of the folder, or not more than 20: they go.
        #expect(SyncPaths.heldDeviceDeletes(deletes, deviceFiles: 121, here: []).isEmpty)
        #expect(SyncPaths.heldDeviceDeletes(Array(deletes.prefix(20)), deviceFiles: 20, here: []).isEmpty)
        // Moved or renamed here: the content is here, the delete goes - and
        // with the moves left out, too few to hold.
        let moved = Set((0..<15).map { "h\($0)" })
        #expect(SyncPaths.heldDeviceDeletes(deletes, deviceFiles: 100, here: moved).isEmpty)
        let some = Set((0..<5).map { "h\($0)" })
        #expect(SyncPaths.heldDeviceDeletes(deletes, deviceFiles: 90, here: some) == Set((5..<30).map { "f\($0)" }))
        #expect(SyncPaths.overMassDelete(21, of: 83) && !SyncPaths.overMassDelete(21, of: 84) && !SyncPaths.overMassDelete(20, of: 21))
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

/// A folder of its own in the temporary directory, removed by the caller.
private func scratchDirectory() throws -> URL {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent("SyncPathsTests-" + UUID().uuidString, isDirectory: true)
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    return dir.resolvingSymlinksInPath()
}
