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
        guard let relative = relativeUnder(path, prefix: prefix) else { return nil }
        return isSafeRelative(relative) ? relative : nil
    }

    /// A device path below the folder listed at `prefix`, safe or not; nil
    /// when it isn't under the prefix.
    static func relativeUnder(_ path: String, prefix: String) -> String? {
        guard path.hasPrefix(prefix) else { return nil }
        return String(path.dropFirst(prefix.count))
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

    /// Left out of every pass, both ways and on both sides - the same list
    /// as otc-sync's (engine/skip.go), written down in CLAUDE.md ("Sync
    /// invariants"): what the local scan never sees (a component starting
    /// with "."; what Finder hides is left out too, but as skipped -
    /// scanFolder - not by its name), downloads in progress, and the
    /// scratch files of Office and of Windows' Explorer (skippedName). A
    /// dotfile listed on the device used to come down, look deleted here on
    /// the next pass, and be deleted there.
    static func isExcludedFromSync(_ relative: String) -> Bool {
        relative.hasSuffix(".otc-part") || hasHiddenComponent(relative)
            || skippedName((relative as NSString).lastPathComponent)
    }

    /// Office's owner files ("~$report.docx": an open document's, made and
    /// deleted as Word and Excel please) and Explorer's desktop.ini,
    /// Thumbs.db, ehthumbs.db and ehthumbs_vista.db, in any case.
    static let skippedNames: Set<String> = ["desktop.ini", "thumbs.db", "ehthumbs.db", "ehthumbs_vista.db"]

    static func skippedName(_ name: String) -> Bool {
        name.hasPrefix("~$") || skippedNames.contains(name.lowercased())
    }

    /// A backup's watcher skips what its reconcile never sends: a hidden
    /// path below the folder (.DS_Store, .git/...), a partial download, a
    /// scratch file. Judged below `root` only, as .skipsHiddenFiles: a
    /// backup of ~/.dotfiles is still watched.
    static func isSkippedBackupPath(_ path: String, root: String) -> Bool {
        if path.hasSuffix(".otc-part") || skippedName((path as NSString).lastPathComponent) { return true }
        if path.hasPrefix(root + "/") {
            return hasHiddenComponent(String(path.dropFirst(root.count + 1)))
        }
        // FSEvents spelled the path differently (/private/var vs /var):
        // judge the file's own name only.
        return hasHiddenComponent((path as NSString).lastPathComponent)
    }

    // MARK: Normalization

    /// A relative path as passes and records key it: its NFC form (otc-sync's
    /// pathKey). The device keeps paths byte for byte, as clients sent them -
    /// this Mac's file system hands out NFD, a phone sends NFC - so one name
    /// could be two entries there; Swift's == takes them for one String,
    /// which made Dictionary(uniqueKeysWithValues:) trap at every launch.
    /// New files go up under their key; a file the device has is always
    /// addressed by the device's own bytes, never renamed to NFC.
    static func key(_ relative: String) -> String { relative.precomposedStringWithCanonicalMapping }

    /// The same bytes - what the device compares - not merely the same
    /// String: "é" and "e\u{301}" are equal Strings.
    static func sameBytes(_ a: String, _ b: String) -> Bool { a.utf8.elementsEqual(b.utf8) }

    /// Of two device entries whose keys are equal, the one to sync: the one
    /// already spelled as its key. The other stays on the device as it is.
    static func prefers(_ candidate: String, over current: String, key: String) -> Bool {
        sameBytes(candidate, key) && !sameBytes(current, key)
    }

    // MARK: Sync records

    /// The device a two-way sync record was made with (otc-sync's
    /// recordDevice): the configured address (deviceDomainKey) and, when the
    /// device gave one, the hex SHA-256 of its home-network certificate -
    /// made once and kept on its storage, so it names that device whatever
    /// address reaches it, and a device set up again from scratch gets a new
    /// one.
    struct RecordDevice: Codable, Equatable {
        var domain: String
        var pin: String?
    }

    /// A record made with `record` is one of `device`: by the pin when both
    /// have one, else by the address. One of another device is no record:
    /// that pass only copies what one side lacks, and deletes nothing.
    static func recordMatches(_ record: RecordDevice, _ device: RecordDevice) -> Bool {
        if let a = record.pin, !a.isEmpty, let b = device.pin, !b.isEmpty { return a == b }
        return record.domain == device.domain
    }

    /// A configured address as records compare it: a bridge name in any
    /// spelling ("Cala.off-the.cloud", "wss://cala.off-the.cloud/ws") is
    /// its bare host; anything else the trimmed address in lower case. As
    /// otc-sync's deviceDomainKey (and SettingsStore.bridgeName).
    static func deviceDomainKey(_ domain: String) -> String {
        var d = domain.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let bridge = "off-the.cloud"
        var bare = d
        if bare.hasPrefix("wss://") {
            bare.removeFirst("wss://".count)
            if bare.hasSuffix("/ws") { bare.removeLast("/ws".count) }
            bare = bare.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        }
        if !bare.contains("://"), !bare.contains("/"), !bare.contains(":"), bare.hasSuffix("." + bridge) {
            let name = String(bare.dropLast(bridge.count + 1))
            if !name.isEmpty, !name.contains(".") { return name + "." + bridge }
        }
        while d.hasSuffix("/") { d.removeLast() }
        return d
    }

    /// The same device as far as the two addresses say.
    static func sameDevice(_ a: String, _ b: String) -> Bool { deviceDomainKey(a) == deviceDomainKey(b) }

    /// A record made with `made` (nil: an older version's, which doesn't
    /// say) for the device a pass talks to: the files to go by, whether the
    /// device is uncertain, and the device to save the record with. One of
    /// another device is none - that pass copies what one side lacks and
    /// deletes nothing (`otherDevice`). One that names its device by a pin
    /// while this sign-in has confirmed none (WSClient.sessionPin), at the
    /// same address, is uncertain: the device, or one set up again since
    /// under its name, empty. That pass uses the record but takes nothing
    /// it can't take back, and the record keeps its pin. As otc-sync's
    /// recordForDeviceLocked.
    static func recordFor(stored: [String: String], made: RecordDevice?, device: RecordDevice)
        -> (files: [String: String], uncertain: Bool, save: RecordDevice, otherDevice: Bool) {
        guard let made else { return (stored, false, device, false) }
        if let pin = made.pin, !pin.isEmpty, device.pin?.isEmpty ?? true, made.domain == device.domain {
            return (stored, !stored.isEmpty, made, false)
        }
        if !recordMatches(made, device), !stored.isEmpty {
            return ([:], false, device, true)
        }
        return (stored, false, device, false)
    }

    static let recordVersion = 2

    /// What a record says about the folder here, whatever the device: the
    /// token of its marker file ("" before it had one; markerName), the
    /// folders below it that were another file system (relative, as the
    /// disk spells them; scanFolder's mounts), and those held as maybe such
    /// a file system that isn't mounted (keys; maybeMounts). As otc-sync's
    /// folderMeta.
    struct FolderMeta: Equatable, Sendable {
        var root = ""
        var mounts: [String] = []
        var maybeMounts: [String] = []
    }

    private struct RecordFile: Codable {
        let v: Int
        let device: RecordDevice
        var root: String?
        var mounts: [String]?
        var maybeMounts: [String]?
        let files: [String: String]

        enum CodingKeys: String, CodingKey {
            case v, device, root, mounts, files
            case maybeMounts = "maybe_mounts"
        }
    }

    /// A record file: this version's ({"v": 2, "device": …, "root": …,
    /// "mounts": […], "maybe_mounts": […], "files": …}) or an older one's
    /// bare map (device nil, taken as the configured device's, as it always
    /// was). Keys in NFC (key); nil when it can't be read - no record, so
    /// nothing is deleted. Same file as otc-sync's; a version that knows
    /// neither "root" nor the mounts ignores them.
    static func parseRecord(_ data: Data) -> (files: [String: String], device: RecordDevice?, meta: FolderMeta)? {
        if let rec = try? JSONDecoder().decode(RecordFile.self, from: data), rec.v == recordVersion {
            let meta = FolderMeta(root: rec.root ?? "", mounts: rec.mounts ?? [], maybeMounts: rec.maybeMounts ?? [])
            return (normalized(rec.files), rec.device, meta)
        }
        guard let map = try? JSONDecoder().decode([String: String].self, from: data) else { return nil }
        return (normalized(map), nil, FolderMeta())
    }

    static func encodeRecord(files: [String: String], device: RecordDevice, meta: FolderMeta = FolderMeta()) -> Data? {
        try? JSONEncoder().encode(RecordFile(v: recordVersion, device: device, root: meta.root.isEmpty ? nil : meta.root,
                                             mounts: meta.mounts.isEmpty ? nil : meta.mounts,
                                             maybeMounts: meta.maybeMounts.isEmpty ? nil : meta.maybeMounts, files: files))
    }

    /// `files` keyed by key(); of two spellings, the NFC one wins.
    static func normalized(_ files: [String: String]) -> [String: String] {
        var out: [String: String] = [:]
        out.reserveCapacity(files.count)
        for (k, v) in files {
            let nk = key(k)
            if out[nk] != nil, !sameBytes(k, nk) { continue }
            out[nk] = v
        }
        return out
    }

    // MARK: The mass-deletion guard

    /// The hashes of the device's files at paths the record doesn't have:
    /// new there, or moved or renamed there. A local delete whose content is
    /// among them is a move, not a loss, and doesn't count for the guard.
    static func movedHashes(listed: [String: String], record: [String: String]) -> Set<String> {
        var moved = Set<String>()
        for (relative, hash) in listed where record[relative] == nil && !hash.isEmpty {
            moved.insert(hash)
        }
        return moved
    }

    /// Both guards: more than this many files in one pass...
    static let massDeleteMin = 20

    /// ... and more than a quarter of `total`.
    static func overMassDelete(_ n: Int, of total: Int) -> Bool { n > massDeleteMin && n * 4 > max(total, 1) }

    /// The guard the other way. A delete on the device is for good - its
    /// versions and, with the last copy, its content - and files gone from
    /// here in numbers are what a folder emptied by accident looks like: a
    /// disk mounted inside it that isn't, a program that emptied it, a
    /// folder that isn't the synced one. Of the device deletes a pass plans
    /// (each with the device's hash), those whose content isn't here under
    /// another name (`here`: a file moved or renamed here) are held - all of
    /// them - when there are more than massDeleteMin and more than a quarter
    /// of the files the device lists; else none. The held files stay on the
    /// device with their baselines here. As otc-sync's holdMassDeviceDeletes.
    static func heldDeviceDeletes(_ deletes: [(relative: String, hash: String)], deviceFiles: Int, here: Set<String>) -> Set<String> {
        guard overMassDelete(deletes.count, of: deviceFiles) else { return [] }
        var held = Set<String>()
        for d in deletes where !here.contains(d.hash) { held.insert(d.relative) }
        return overMassDelete(held.count, of: deviceFiles) ? held : []
    }

    // MARK: Which folder a record was made with
    //
    // A two-way folder's record is only true of the folder it was made
    // with. "Missing" used to mean only that nothing was at the folder's
    // path, so a folder that was there but wasn't that one - the empty
    // ~/Documents macOS leaves when iCloud's Desktop & Documents is turned
    // off, the empty folder of a disk or share that isn't mounted, another
    // disk with the same name, a folder made afresh where the synced one was
    // - read as one whose every file had been deleted here, and each was
    // deleted on the device, for good.
    //
    // So every two-way folder holds a small hidden file of its own,
    // ".otc-sync-<folder id>", with a token its record keeps too
    // (FolderMeta.root). A pass goes ahead when the file holds the record's
    // token. Without it - a record from before these files, the file deleted
    // - the folder must still hold at least half of the files the record
    // lists, unchanged, and the file is written again; a record that lists
    // none has nothing to lose. Otherwise the folder waits as a missing one
    // does ("Not the folder that was synced"), untouched on both sides, and
    // carries on once its own folder is back. Being inside the folder, the
    // file moves and is copied with it. The file is dot-named, so it is
    // never synced. As otc-sync's engine/rootid.go.

    static let markerPrefix = ".otc-sync-"

    /// Folder `id`'s marker file name.
    static func markerName(_ id: UUID) -> String { markerPrefix + id.uuidString }

    /// What the file says to whoever opens it.
    static let markerNote = "Off The Cloud keeps this file here to know that this is the folder it syncs with your device. It is never uploaded." // i18n-ignore: written into a file, never shown by the app

    private struct MarkerFile: Codable {
        let note: String
        let token: String
    }

    static func markerData(token: String) -> Data? {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        return (try? encoder.encode(MarkerFile(note: markerNote, token: token))).map { $0 + Data("\n".utf8) }
    }

    /// The token a marker file holds; "" when it doesn't hold one.
    static func markerToken(_ data: Data) -> String {
        guard let m = try? JSONDecoder().decode(MarkerFile.self, from: data) else { return "" }
        return m.token.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// The token in folder `id`'s marker file in `root`; "" when there is
    /// none or it can't be read.
    static func readMarker(in root: URL, id: UUID) -> String {
        guard let fh = try? FileHandle(forReadingFrom: root.appendingPathComponent(markerName(id))) else { return "" }
        defer { try? fh.close() }
        guard let data = try? fh.read(upToCount: 4096) else { return "" }
        return markerToken(data)
    }

    static func newMarkerToken() -> String {
        var rng = SystemRandomNumberGenerator()
        return (0..<16).map { _ in String(format: "%02x", UInt8.random(in: 0...255, using: &rng)) }.joined()
    }

    struct RootVerdict: Equatable {
        /// The folder is the one the record was made with, or the record
        /// has nothing to lose.
        var ok: Bool
        /// The token the record keeps from now on; `mark`: the marker file
        /// is to be written with it.
        var token: String = ""
        var mark = false
    }

    /// Whether the folder is the one its record was made with: `recorded`
    /// is the token the record keeps, `marker` the one in the folder's
    /// marker file, `record` the record's files, `here` the hashes of the
    /// recorded files found here (key -> hash). What is unknown here (under
    /// a directory that can't be read, a disk that isn't mounted, a link)
    /// counts neither way. As otc-sync's judgeRoot.
    static func judgeRoot(recorded: String, marker: String, record: [String: String], here: [String: String],
                          unknown: (String) -> Bool) -> RootVerdict {
        if !recorded.isEmpty, marker == recorded { return RootVerdict(ok: true, token: recorded) }
        var listed = 0, unchanged = 0
        for (rel, entry) in record where !entry.hasPrefix(keptOnDevicePrefix) && !unknown(rel) {
            listed += 1
            if let h = here[rel], h == entry { unchanged += 1 }
        }
        if listed > 0, unchanged * 2 < listed { return RootVerdict(ok: false) }
        var token = recorded
        if token.isEmpty { token = marker }
        if token.isEmpty { token = newMarkerToken() }
        return RootVerdict(ok: true, token: token, mark: true)
    }

    /// What judgeRoot weighs: how many files the record lists here (plain
    /// entries, not unknown here), and which of them are here (`present`),
    /// sorted - the ones to hash, as far as the answer needs.
    static func rootEvidence(record: [String: String], present: (String) -> Bool, unknown: (String) -> Bool) -> (listed: Int, present: [String]) {
        var listed = 0
        var here: [String] = []
        for (rel, entry) in record where !entry.hasPrefix(keptOnDevicePrefix) && !unknown(rel) {
            listed += 1
            if present(rel) { here.append(rel) }
        }
        return (listed, here.sorted())
    }

    /// The evidence is settled: at least half of `listed` unchanged, or too
    /// few left to get there.
    static func rootSettled(listed: Int, unchanged: Int, left: Int) -> Bool {
        unchanged * 2 >= listed || (unchanged + left) * 2 < listed
    }

    // MARK: What a two-way pass sees here

    /// What scanFolder found below a folder.
    struct FolderScan: Sendable {
        /// The regular files to sync.
        var files: [URL] = []
        /// What could not be read (a directory without permission): what is
        /// under it is not "gone", it is unknown.
        var failed: [URL] = []
        /// false when the folder itself could not be listed - an empty list
        /// then says nothing about what it holds.
        var rootReadable = true
        /// What is here but isn't synced, and isn't left out by its name on
        /// both sides: a symbolic link (to a file or a folder; never
        /// followed), anything that is neither a regular file nor a folder,
        /// and what Finder hides (UF_HIDDEN, chflags hidden; its folder's
        /// files with it) - relative, as the disk spells them. A file synced
        /// before that is one of these now, or is under one, is still here:
        /// its copy on the device must not be deleted for it.
        var skipped: [String] = []
        /// The folders below the root that are another file system than
        /// their parent - a disk or a share mounted there, walked as the
        /// rest -, relative, as the disk spells them.
        var mounts: [String] = []
        /// Every file of the folder is in `files`.
        var complete: Bool { rootReadable && failed.isEmpty }
    }

    /// Walks a folder for its regular files, leaving out what is never
    /// synced by its name (a "." component, partial downloads, scratch
    /// files: isExcludedFromSync), and notes what it skipped for other
    /// reasons and the file systems mounted below it (FolderScan). It used
    /// to leave out what Finder hides by its enumerator's .skipsHiddenFiles,
    /// without a word: a synced file hidden since read as one deleted here,
    /// and was deleted on the device. As otc-sync's scanFolder. Self-contained
    /// and nonisolated: the walk of a big tree runs off the main actor.
    ///
    /// The enumerator stops at a disk or share mounted below the folder it
    /// walks - it lists the mount point as an empty folder - so each one
    /// found is walked on its own, as otc-sync's WalkDir goes into it: its
    /// files used to be neither sent nor seen here, a device file downloaded
    /// onto it was taken for deleted here at every pass after, and the disk
    /// itself, at the first pass of an older record, for a folder emptied
    /// while the disk was away.
    static func scanFolder(at root: URL) -> FolderScan {
        var scan = FolderScan()
        // The enumerator skipped what it couldn't read without a word, and
        // a two-way pass took those files for deleted here.
        final class Failures: @unchecked Sendable { var urls: [URL] = [] }
        let failures = Failures()
        let rootPath = root.standardizedFileURL.path
        func relative(_ url: URL) -> String { relativePath(url.standardizedFileURL.path, root: rootPath) ?? "" }
        // Each folder's file system, to find those mounted below the root.
        var devs: [String: dev_t] = [:]
        var rootStat = stat()
        if stat(rootPath, &rootStat) == 0 { devs[rootPath] = rootStat.st_dev }
        let keys: [URLResourceKey] = [.isRegularFileKey, .isDirectoryKey, .isSymbolicLinkKey, .isHiddenKey]
        // One file system's part of the tree, from `dir` down; false when
        // `dir` couldn't be listed at all.
        func walk(_ dir: URL) -> Bool {
            guard let e = FileManager.default.enumerator(at: dir, includingPropertiesForKeys: keys, options: [],
                                                         errorHandler: { url, _ in failures.urls.append(url); return true }) else {
                return false
            }
            for case let url as URL in e {
                let name = url.lastPathComponent
                let v = try? url.resourceValues(forKeys: Set(keys))
                // A folder's own: skipDescendants skips the folder obtained
                // last, which after a file may be another one.
                let isFolder = v?.isDirectory == true && v?.isSymbolicLink != true
                // By its first byte, as isExcludedFromSync: a combining mark
                // after the dot makes it another Character.
                if name.utf8.first == UInt8(ascii: ".") {
                    if isFolder { e.skipDescendants() }
                    continue
                }
                guard let v else { continue }
                // Neither followed nor synced, nor taken for deleted.
                if v.isSymbolicLink == true {
                    scan.skipped.append(relative(url))
                    continue
                }
                if v.isHidden == true {
                    scan.skipped.append(relative(url))
                    if isFolder { e.skipDescendants() }
                    continue
                }
                if v.isDirectory == true {
                    let full = url.standardizedFileURL.path
                    var st = stat()
                    if let parent = devs[url.deletingLastPathComponent().standardizedFileURL.path], lstat(full, &st) == 0 {
                        devs[full] = st.st_dev
                        if st.st_dev != parent {
                            scan.mounts.append(relative(url))
                            // Walked here, once: should this enumerator go
                            // into it after all, nothing is listed twice.
                            e.skipDescendants()
                            if !walk(url) { failures.urls.append(url) }
                        }
                    }
                    continue
                }
                // A download in progress (download() writes to a ".otc-part"
                // file first) is not a file of the folder: it used to be found
                // and uploaded, half-written. Nor are Office's and Explorer's
                // scratch files.
                if name.hasSuffix(".otc-part") || skippedName(name) { continue }
                if v.isRegularFile == true {
                    scan.files.append(url)
                } else {
                    scan.skipped.append(relative(url))
                }
            }
            return true
        }
        guard walk(root) else {
            scan.rootReadable = false
            return scan
        }
        if failures.urls.contains(where: { $0.standardizedFileURL.path == rootPath }) { scan.rootReadable = false }
        scan.failed = failures.urls
        return scan
    }

    /// A set of relative paths (keys, or as the disk spells them: Swift
    /// compares the two normalizations as one).
    struct PathSet: Sendable {
        private(set) var paths: Set<String>

        init(_ paths: [String] = []) { self.paths = Set(paths) }

        mutating func insert(_ path: String) { paths.insert(path) }

        var isEmpty: Bool { paths.isEmpty }

        /// `rel` is one of the set's paths, or inside one.
        func covers(_ rel: String) -> Bool {
            guard !paths.isEmpty else { return false }
            var p = rel
            while true {
                if paths.contains(p) { return true }
                guard let parent = SyncPaths.parentKey(p) else { return false }
                p = parent
            }
        }
    }

    /// `path` below `root` (both as the disk spells them), without the
    /// slash; nil when it isn't below it. By UTF-8 bytes: a combining mark
    /// after a "/" makes one Character of the two.
    static func relativePath(_ path: String, root: String) -> String? {
        let r = root.utf8
        guard path.utf8.starts(with: r) else { return nil }
        var rest = path.utf8.dropFirst(r.count)
        if rest.first == UInt8(ascii: "/") { rest = rest.dropFirst() } else if !(r.last == UInt8(ascii: "/")) && !rest.isEmpty { return nil }
        return String(decoding: rest, as: UTF8.self)
    }

    /// The folder holding `key` ("a/b" for "a/b/c.txt"); nil at the top.
    /// By its UTF-8 bytes, as the checks above.
    static func parentKey(_ key: String) -> String? {
        let u = key.utf8
        guard let i = u.lastIndex(of: UInt8(ascii: "/")) else { return nil }
        return String(decoding: u[..<i], as: UTF8.self)
    }

    /// Every folder above `key`, the deepest first.
    static func ancestors(_ key: String) -> [String] {
        var out: [String] = []
        var p = key
        while let parent = parentKey(p) {
            out.append(parent)
            p = parent
        }
        return out
    }

    /// Something is at `path`, a folder of its own (not a link to one).
    static func isPlainDirectory(_ path: String) -> Bool {
        var st = stat()
        return lstat(path, &st) == 0 && (st.st_mode & S_IFMT) == S_IFDIR
    }

    // MARK: A disk mounted inside a two-way folder
    //
    // A disk or a share mounted inside a two-way folder (a NAS at
    // ~/Sync/nas) is, while it isn't mounted, an empty folder: the files it
    // holds looked deleted here, and were deleted on the device. The folder's
    // own identity can't tell, being in the folder above. So the scan notes
    // the folders below the root that are another file system than their
    // parent (FolderScan.mounts), the record keeps them (FolderMeta.mounts),
    // and one that is a plain folder again holds everything under it, as a
    // folder that could not be read: no action either way, the baselines
    // kept. One that is gone altogether was removed on purpose and is
    // forgotten. A record from before the record kept its mounts knows
    // nothing of a disk mounted inside: at its first pass, a folder under
    // which the record lists files and the scan finds none - still a plain
    // folder, and not changed since the record was written, as a mount
    // point nothing touched while its disk was away; a folder emptied by
    // hand since is newer - is held the same way (maybeMounts), and the
    // record keeps it ("maybe_mounts") while it stays so. As otc-sync's
    // engine/mounts.go (otc-sync on Linux also holds the mount points
    // /etc/fstab declares, which the Mac has no counterpart of).

    /// The mounts the record lists (`recorded`) against those this pass
    /// found (`now`), relative to `root` as the disk spells them: `gone` are
    /// those whose folder is there with nothing mounted on it - what is under
    /// them is held -, `keep` what the record keeps: now and gone, sorted.
    static func goneMounts(root: String, recorded: [String], now: [String]) -> (gone: [String], keep: [String]) {
        let have = Set(now)
        var gone: [String] = []
        for m in recorded where !have.contains(m) && isSafeRelative(m) {
            if isPlainDirectory((root as NSString).appendingPathComponent(m)) { gone.append(m) }
        }
        return (gone, Array(Set(now + gone)).sorted())
    }

    /// The folders (keys) to hold as maybe a disk or share mounted in
    /// `root` that isn't now: of the candidates - those the record keeps
    /// (meta.maybeMounts), or at the first pass of a record from before
    /// mounts were kept, emptiedFolders - those still a plain folder here
    /// with nothing found under them (`here`, the keys of the files here)
    /// while `record` lists files under them. Sorted.
    static func maybeMounts(root: String, meta: FolderMeta, record: [String: String], here: Set<String>,
                            known: (String) -> Bool, written: Date?) -> [String] {
        var candidates = meta.maybeMounts
        if meta.root.isEmpty && meta.mounts.isEmpty {
            candidates = emptiedFolders(root: root, record: record, here: here, known: known, written: written)
        }
        guard !candidates.isEmpty else { return [] }
        var hereUnder = Set<String>(), recordUnder = Set<String>()
        for key in here { hereUnder.formUnion(ancestors(key)) }
        for (key, entry) in record where !entry.hasPrefix(keptOnDevicePrefix) { recordUnder.formUnion(ancestors(key)) }
        var out = Set<String>()
        for m in candidates where isSafeRelative(m) && !hereUnder.contains(m) && recordUnder.contains(m) {
            if isPlainDirectory((root as NSString).appendingPathComponent(m)) { out.insert(m) }
        }
        return out.sorted()
    }

    /// The folders below `root`, each the shallowest of its branch, under
    /// which the record lists files (plain entries, not `known` otherwise -
    /// unreadable, skipped) and the scan found none (`here`), that are a
    /// plain folder here not changed since `written`, when the record file
    /// was last written (nil: not read from a file).
    static func emptiedFolders(root: String, record: [String: String], here: Set<String>,
                               known: (String) -> Bool, written: Date?) -> [String] {
        guard let written else { return [] }
        var localDirs = Set<String>()
        for key in here { localDirs.formUnion(ancestors(key)) }
        var checked = Set<String>(), found = Set<String>()
        for (key, entry) in record where !entry.hasPrefix(keptOnDevicePrefix) && !known(key) && !here.contains(key) {
            // From the shallowest folder down to the first with no file
            // here under it.
            for dir in ancestors(key).reversed() {
                if localDirs.contains(dir) { continue }
                if checked.insert(dir).inserted {
                    var st = stat()
                    let path = (root as NSString).appendingPathComponent(dir)
                    if lstat(path, &st) == 0, (st.st_mode & S_IFMT) == S_IFDIR {
                        let mtime = Date(timeIntervalSince1970: TimeInterval(st.st_mtimespec.tv_sec) + TimeInterval(st.st_mtimespec.tv_nsec) / 1e9)
                        if mtime < written { found.insert(dir) }
                    }
                }
                break
            }
        }
        return found.sorted()
    }

    // MARK: A folder renamed only in case on the device
    //
    // A folder renamed only in case on the device ("photos" to "Photos",
    // from the web or a phone) used to be undone there. On a disk that
    // ignores case (APFS as macOS formats it) the pass trashed photos/a.jpg
    // here and downloaded Photos/a.jpg - into the folder still called
    // "photos", the one name the disk has for both. The next pass saw
    // photos/a.jpg as new here (uploaded) and Photos/a.jpg as deleted here
    // (deleted on the device): the rename reverted, the device's rows made
    // again without their versions. So before a pass plans anything, a
    // folder here whose name differs only in case from the device's - the
    // device has it in that case only, and the disk takes the device's
    // spelling for this very folder - is renamed to the device's case, and
    // the folder looked at again. Its files keep their record entries and
    // cached hashes under the new name. Unless the rename was made here
    // (Finder): the record then has the folder's files under the device's
    // spelling and none under this one; that folder stays, and the pass
    // sends the rename up as any. As otc-sync's engine/casefix.go.

    /// A key as a disk that ignores case compares it.
    static func caseFold(_ key: String) -> String { key.folding(options: [.caseInsensitive], locale: nil) }

    /// One folder renamed: its key and the key it has now, and its full path
    /// before and after.
    struct CaseRename: Equatable {
        let from: String
        let to: String
        let fromDir: String
        let toDir: String
    }

    /// Renames the folders here (in `root`, holding files: `local`, key ->
    /// the file's path below root as the disk spells it) whose names are
    /// the device's (`remote`, its keys) but for case, the shallowest first
    /// - one level a call; the caller looks at the folder again after any.
    /// `record` is the sync record: a folder it lists under the device's
    /// spelling only was renamed here, and stays. Returns what it renamed.
    static func matchFolderCase(root: String, local: [String: String], remote: Set<String>, record: [String: String]) -> [CaseRename] {
        var deviceDirs = Set<String>()
        for key in remote { deviceDirs.formUnion(ancestors(key)) }
        var byFold: [String: [String]] = [:]
        for d in deviceDirs { byFold[caseFold(d), default: []].append(d) }
        // The folders here, as the disk spells them, by key.
        var localDirs: [String: String] = [:]
        for (key, diskPath) in local {
            for (k, disk) in zip(ancestors(key), ancestors(diskPath)) { localDirs[k] = disk }
        }
        var recordDirs = Set<String>()
        for key in record.keys { recordDirs.formUnion(ancestors(key)) }
        var wrong: [String] = []
        var depth = -1
        for k in localDirs.keys where !deviceDirs.contains(k) {
            // The device's folder, in one case only, and this folder's
            // parent already the device's spelling (shallowest first).
            guard let same = byFold[caseFold(k)], same.count == 1, parentKey(same[0]) == parentKey(k) else { continue }
            // Synced under the device's spelling, never under this one: the
            // rename is this Mac's, and goes up.
            if recordDirs.contains(same[0]) && !recordDirs.contains(k) { continue }
            let n = k.utf8.filter { $0 == UInt8(ascii: "/") }.count
            if depth < 0 || n < depth {
                depth = n
                wrong = [k]
            } else if n == depth {
                wrong.append(k)
            }
        }
        var done: [CaseRename] = []
        for k in wrong.sorted() {
            guard let to = byFold[caseFold(k)]?.first, let disk = localDirs[k] else { continue }
            let from = (root as NSString).appendingPathComponent(disk)
            let dest = ((from as NSString).deletingLastPathComponent as NSString).appendingPathComponent((to as NSString).lastPathComponent)
            // Only where the disk takes the device's spelling for this very
            // folder: it ignores case, and there is no other folder by it.
            var a = stat(), b = stat()
            guard lstat(from, &a) == 0, lstat(dest, &b) == 0, (a.st_mode & S_IFMT) == S_IFDIR,
                  a.st_dev == b.st_dev, a.st_ino == b.st_ino else { continue }
            guard rename(from, dest) == 0 else { continue }
            done.append(CaseRename(from: k, to: to, fromDir: from, toDir: dest))
        }
        return done
    }

    /// `key` after the folder renames: under a renamed folder, its new name.
    static func renamedKey(_ key: String, _ renames: [CaseRename]) -> String {
        for r in renames where key.utf8.starts(with: (r.from + "/").utf8) {
            return r.to + String(decoding: key.utf8.dropFirst(r.from.utf8.count), as: UTF8.self)
        }
        return key
    }

    /// The record with the renamed folders' entries under their new names.
    static func renamedRecord(_ record: [String: String], _ renames: [CaseRename]) -> [String: String] {
        var out = record
        for (k, v) in record {
            let nk = renamedKey(k, renames)
            guard !sameBytes(nk, k) else { continue }
            out.removeValue(forKey: k)
            if out[nk] == nil { out[nk] = v }
        }
        return out
    }

    /// Something is at `rel` (a key) below `root` under exactly that name -
    /// each component as its folder lists it, compared in NFC but minding
    /// case - and not only under a name the disk takes for it. On a disk
    /// that ignores case, "a.jpg" finds "A.jpg": a file renamed only in case
    /// here, which must not hold up the delete of its old name on the
    /// device (the device tells the two apart, and kept both for good).
    /// Folders are listed only when something answers to the name. As
    /// otc-sync's presentAsNamed.
    static func presentAsNamed(root: String, rel: String) -> Bool {
        var st = stat()
        guard lstat((root as NSString).appendingPathComponent(rel), &st) == 0 else { return false }
        var dir = root
        for part in rel.utf8.split(separator: UInt8(ascii: "/"), omittingEmptySubsequences: false).map({ String(decoding: $0, as: UTF8.self) }) {
            // Swift's == is NFC-blind and minds case: what is wanted here.
            guard let names = try? FileManager.default.contentsOfDirectory(atPath: dir),
                  let name = names.first(where: { $0 == part }) else { return false }
            dir = (dir as NSString).appendingPathComponent(name)
        }
        return true
    }

    // MARK: A conflict copy is made once

    /// `candidate` is a name conflictURL gives the device's version of a
    /// file named `name`: "report (conflict 2026-11-04 10.16).txt", or with
    /// a number after the time. A pass whose upload of the local version
    /// failed kept one; the next reuses it rather than download another. As
    /// otc-sync's conflictCopyRe.
    static func isDeviceConflictCopy(_ candidate: String, of name: String) -> Bool {
        let url = URL(fileURLWithPath: name)
        var ext = url.pathExtension
        var stem = url.deletingPathExtension().lastPathComponent
        if stem.isEmpty { stem = name; ext = "" }
        let suffix = ext.isEmpty ? "" : "." + ext
        let pattern = "^" + NSRegularExpression.escapedPattern(for: stem)
            + #" \(conflict [0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}\.[0-9]{2}( [0-9]+)?\)"#
            + NSRegularExpression.escapedPattern(for: suffix) + "$"
        guard let re = try? NSRegularExpression(pattern: pattern) else { return false }
        return re.firstMatch(in: candidate, range: NSRange(candidate.startIndex..., in: candidate)) != nil
    }

    // MARK: Where a deleted file goes when there is no Trash
    //
    // A file a two-way pass deletes here goes to the Trash. A volume without
    // one - an SMB or NFS share - refused it, and the delete was planned
    // again at every pass while the file stayed here, gone everywhere else.
    // There it goes to a .otc-trash folder in the synced folder (dot-named,
    // so never synced), in a folder per day, with its path below the synced
    // folder - below the top folder of a disk mounted inside it for a file
    // on that disk, as a rename can't leave its file system; days older
    // than folderTrashDays are emptied after the passes. Never deleted
    // outright: a file that can't be moved anywhere stays, and its delete is
    // tried again at the next pass. The same folder as otc-sync's
    // engine/trash.go, so both apps leave the same thing on a shared disk.

    static let folderTrashName = ".otc-trash"
    static let folderTrashDays = 30

    /// A day's folder name in a .otc-trash, local time.
    static func trashDay(_ date: Date) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = .current
        f.dateFormat = "yyyy-MM-dd"
        return f.string(from: date)
    }

    /// The folder whose .otc-trash takes `path`: `root`, or the top folder
    /// below root of the file system `path` is on when that isn't root's.
    static func trashTop(_ path: String, root: String) -> String {
        var st = stat()
        guard lstat(path, &st) == 0 else { return root }
        let dev = st.st_dev
        var top = (path as NSString).deletingLastPathComponent
        while top != root {
            let parent = (top as NSString).deletingLastPathComponent
            var pst = stat()
            guard parent != top, stat(parent, &pst) == 0, pst.st_dev == dev else { break }
            top = parent
        }
        return relativePath(top, root: root) != nil ? top : root
    }

    /// Moves `path`, a file in the two-way folder `root`, into the folder's
    /// trash: <top>/.otc-trash/<day>/<its path below top> (trashTop), a
    /// taken name numbered. Returns where it went.
    @discardableResult
    static func folderTrash(_ path: String, root: String, now: Date = Date()) throws -> String {
        let top = trashTop(path, root: root)
        guard let rel = relativePath(path, root: top), !rel.isEmpty else { throw POSIXError(.EINVAL) }
        let trash = (top as NSString).appendingPathComponent(folderTrashName)
        let dest = ((trash as NSString).appendingPathComponent(trashDay(now)) as NSString).appendingPathComponent(rel)
        try FileManager.default.createDirectory(atPath: (dest as NSString).deletingLastPathComponent,
                                                withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        guard let free = freeName(dest) else { throw POSIXError(.EEXIST) }
        guard rename(path, free) == 0 else { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
        return free
    }

    /// `path`, or `path` with a number before its extension ("a 2.txt")
    /// when something is there already.
    static func freeName(_ path: String) -> String? {
        var st = stat()
        if lstat(path, &st) != 0 { return path }
        let url = URL(fileURLWithPath: path)
        let dir = url.deletingLastPathComponent()
        var ext = url.pathExtension
        var stem = url.deletingPathExtension().lastPathComponent
        if stem.isEmpty { stem = url.lastPathComponent; ext = "" }
        for i in 2..<10000 {
            let candidate = dir.appendingPathComponent(ext.isEmpty ? "\(stem) \(i)" : "\(stem) \(i).\(ext)").path
            if lstat(candidate, &st) != 0 { return candidate }
        }
        return nil
    }

    /// Empties the days in `root`'s .otc-trash older than folderTrashDays,
    /// and the folder itself once it is empty.
    static func pruneFolderTrash(_ root: String, now: Date = Date()) {
        let trash = (root as NSString).appendingPathComponent(folderTrashName)
        guard let names = try? FileManager.default.contentsOfDirectory(atPath: trash) else { return }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = .current
        f.dateFormat = "yyyy-MM-dd"
        f.isLenient = false
        let cutoff = Calendar.current.date(byAdding: .day, value: -folderTrashDays, to: now) ?? now
        var left = 0
        for name in names {
            let path = (trash as NSString).appendingPathComponent(name)
            guard let day = f.date(from: name), f.string(from: day) == name, isPlainDirectory(path), day < cutoff else {
                left += 1
                continue
            }
            if (try? FileManager.default.removeItem(atPath: path)) == nil { left += 1 }
        }
        if left == 0 { rmdir(trash) }
    }

    /// Every .otc-trash of a two-way folder: its own, and that of each disk
    /// mounted in it (`mounts`, as the record keeps them), where
    /// folderTrash puts the files that were on it.
    static func pruneFolderTrashes(root: String, mounts: [String], now: Date = Date()) {
        pruneFolderTrash(root, now: now)
        for m in mounts where isSafeRelative(m) {
            pruneFolderTrash((root as NSString).appendingPathComponent(m), now: now)
        }
    }

    // MARK: A download needs room
    //
    // A two-way file whose transfer failed for a reason that doesn't go away
    // in a minute - no room for it here, too large for this disk, the device
    // refusing it - was tried again in full at every pass: a 20 GB download
    // filled the disk to nothing every minute, then gave the space back; a
    // 5 GB one on a FAT32 stick wrote 4 GB, failed and started again; and the
    // folder said "Synced" all along. Now a download is refused before it
    // writes anything when it can't fit, a file that failed waits (2 minutes,
    // doubling up to an hour, as a busy file), and the pass ends saying how
    // many files it couldn't sync and why. As otc-sync's engine/fail.go.

    /// Left free on the disk by a download: never filled to the last byte,
    /// where the other programs' saves fail.
    static let spaceReserve: Int64 = 256 << 20
    /// The largest file FAT (MS-DOS) holds: 4 GiB less a byte.
    static let fatMaxFile: Int64 = (1 << 32) - 1

    enum RoomProblem: Error, Equatable, CustomStringConvertible {
        case noSpace(freeMB: Int64, neededMB: Int64)
        case tooLarge

        var description: String { // i18n-ignore: a file's error, only logged
            switch self {
            case let .noSpace(free, needed): return "not enough free space on this disk (\(free) MB free, \(needed) MB needed)" // i18n-ignore: only logged
            case .tooLarge: return "too large for this disk's file system (FAT32 holds files up to 4 GB)" // i18n-ignore: only logged
            }
        }
    }

    /// Why a file of `size` bytes can't be written where `free` bytes are
    /// free (nil: unknown, no limit) and files are at most `maxFile`; nil
    /// when it can.
    static func roomProblem(size: Int64, free: Int64?, maxFile: Int64) -> RoomProblem? {
        guard size >= 0 else { return nil }
        if size > maxFile { return .tooLarge }
        if let free, size + spaceReserve > free { return .noSpace(freeMB: free >> 20, neededMB: (size + spaceReserve) >> 20) }
        return nil
    }

    /// The free space of `dir`'s volume (nil when it reports no size, as
    /// some network and FUSE volumes do: an unknown size is no reason to
    /// refuse a download) and the largest file it holds (FAT: fatMaxFile);
    /// nil when the system can't say. The free space is the most any of
    /// the system's measures gives - what the system frees for an
    /// important file counts - so a download is refused only when it
    /// certainly can't fit.
    static func volumeLimits(_ dir: URL) -> (free: Int64?, maxFile: Int64)? {
        var fs = statfs()
        let stated = statfs(dir.path, &fs) == 0
        let values = try? dir.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey, .volumeAvailableCapacityKey, .volumeTotalCapacityKey])
        guard stated || values != nil else { return nil }
        var maxFile = Int64.max
        if stated {
            let type = withUnsafeBytes(of: fs.f_fstypename) { String(decoding: $0.prefix { $0 != 0 }, as: UTF8.self) }
            if type == "msdos" { maxFile = fatMaxFile }
        }
        if (stated && fs.f_blocks == 0) || values?.volumeTotalCapacity == 0 { return (nil, maxFile) }
        var free: Int64?
        if stated { free = Int64(fs.f_bavail) * Int64(fs.f_bsize) }
        if let important = values?.volumeAvailableCapacityForImportantUsage { free = max(free ?? 0, important) }
        if let available = values?.volumeAvailableCapacity { free = max(free ?? 0, Int64(available)) }
        return (free, maxFile)
    }

    /// Why a file couldn't be synced, as the folder's note says it.
    enum FailKind: Sendable { case other, noSpace, tooLarge }

    /// How long a file that failed waits before it is tried again: twice
    /// as long as the time before, from 2 minutes up to an hour (the
    /// busy-file numbers).
    static func failDelay(after previous: TimeInterval?) -> TimeInterval { min(max((previous ?? 0) * 2, 120), 3600) }

    /// The files a pass couldn't sync, by why.
    struct PassFailures: Equatable {
        var other = 0, noSpace = 0, tooLarge = 0

        mutating func add(_ kind: FailKind) {
            switch kind {
            case .noSpace: noSpace += 1
            case .tooLarge: tooLarge += 1
            case .other: other += 1
            }
        }

        var total: Int { other + noSpace + tooLarge }
    }

    /// What a two-way pass ends saying, when it doesn't end synced.
    enum PassNote: Equatable {
        /// So many deletes here held on the device (heldDeviceDeletes).
        case deletesHeld(Int)
        /// So many changes wait for the device's pin (recordFor).
        case deviceUnconfirmed(Int)
        case noSpace(Int)
        case tooLarge(Int)
        case filesFailed(Int)
        /// So many files that could not be read here.
        case unreadable(Int)
        /// The guard put back so many files the device lost.
        case restored(Int)
        /// So many files the device has that are links or hidden here.
        case notSyncedHere(Int)
    }

    /// The pass's note, the most pressing first: deletes held, then changes
    /// waiting for the device, then the files it couldn't sync (no room
    /// first), what couldn't be read, what the guard restored, and what is
    /// left on the device for being a link or hidden here. As otc-sync's end
    /// of reconcileRemoteFolder.
    static func passNote(held: Int, unconfirmed: Int, failures: PassFailures, unreadable: Int, restored: Int, notSyncedHere: Int) -> PassNote? {
        if held > 0 { return .deletesHeld(held) }
        if unconfirmed > 0 { return .deviceUnconfirmed(unconfirmed) }
        if failures.total > 0 {
            if failures.noSpace > 0 { return .noSpace(failures.noSpace) }
            if failures.tooLarge > 0 { return .tooLarge(failures.tooLarge) }
            return .filesFailed(failures.total)
        }
        if unreadable > 0 { return .unreadable(unreadable) }
        if restored > 0 { return .restored(restored) }
        if notSyncedHere > 0 { return .notSyncedHere(notSyncedHere) }
        return nil
    }

    // MARK: Folders synced once

    /// Why a folder can't be added next to the ones synced: it is one of
    /// them, inside one, or holds one (the other's name).
    enum Overlap: Equatable {
        case same(String)
        case inside(String)
        case holds(String)
    }

    /// `path` against `others` (path and name each), both local paths or
    /// both device paths; case-insensitive when `ignoringCase` (this Mac's
    /// disks, and otc-sync's on Windows). Paths compare in NFC, without a
    /// trailing slash. As otc-sync's config.Overlap.
    static func overlap(_ path: String, with others: [(path: String, name: String)], ignoringCase: Bool) -> Overlap? {
        func norm(_ p: String) -> String {
            var s = key(p)
            if ignoringCase { s = s.lowercased() }
            while s.count > 1, s.hasSuffix("/") { s.removeLast() }
            return s
        }
        func under(_ p: String, _ dir: String) -> Bool {
            dir == "/" ? p != "/" : p.utf8.starts(with: (dir + "/").utf8)
        }
        let mine = norm(path)
        for other in others {
            let theirs = norm(other.path)
            if mine.utf8.elementsEqual(theirs.utf8) { return .same(other.name) }
            if under(mine, theirs) { return .inside(other.name) }
            if under(theirs, mine) { return .holds(other.name) }
        }
        return nil
    }

    // MARK: Unencrypted addresses

    /// `domain` is a ws:// address (not encrypted) for a device that isn't on
    /// this network: the Settings form warns before using it. Not for
    /// loopback, private (10/8, 172.16/12, 192.168/16, fc00::/7) or
    /// link-local addresses, a .local, .lan, .home.arpa or .internal name,
    /// or a single-label one ("nas"). As otc-sync's config.Unencrypted.
    static func isUnencryptedRemote(_ domain: String) -> Bool {
        let d = domain.trimmingCharacters(in: .whitespacesAndNewlines)
        guard d.lowercased().hasPrefix("ws://"), let host = URLComponents(string: d)?.host?.lowercased(), !host.isEmpty else {
            return false
        }
        var h = host
        if h.hasPrefix("[") && h.hasSuffix("]") { h = String(h.dropFirst().dropLast()) }
        if h == "localhost" || h.hasSuffix(".localhost") { return false }
        if let b = ipv4(h) {
            let local = b[0] == 127 || b[0] == 10 || (b[0] == 172 && b[1] & 0xf0 == 16) || (b[0] == 192 && b[1] == 168)
                || (b[0] == 169 && b[1] == 254)
            return !local
        }
        if h.contains(":") {
            // IPv6: loopback, unique local (fc00::/7), link-local (fe80::/10).
            if h == "::1" { return false }
            let first = h.split(separator: ":", omittingEmptySubsequences: false).first.map(String.init) ?? ""
            if let v = UInt16(first, radix: 16), v & 0xfe00 == 0xfc00 || v & 0xffc0 == 0xfe80 { return false }
            return true
        }
        if !h.contains(".") { return false }
        for suffix in [".local", ".lan", ".home.arpa", ".internal"] where h.hasSuffix(suffix) { return false }
        return true
    }

    private static func ipv4(_ s: String) -> [UInt8]? {
        let parts = s.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 4 else { return nil }
        var out: [UInt8] = []
        for p in parts {
            guard !p.isEmpty, p.count <= 3, p.allSatisfy(\.isASCII), let v = UInt8(p) else { return nil }
            out.append(v)
        }
        return out
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
