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

    /// Left out of every pass, both ways and on both sides - the same list
    /// as otc-sync's (engine/skip.go), written down in CLAUDE.md ("Sync
    /// invariants"): what the local scan never sees (a component starting
    /// with ".", as .skipsHiddenFiles, which also leaves out what Finder
    /// hides), downloads in progress, and the scratch files of Office and of
    /// Windows' Explorer (skippedName). A dotfile listed on the device used
    /// to come down, look deleted here on the next pass, and be deleted
    /// there.
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

    static let recordVersion = 2

    private struct RecordFile: Codable {
        let v: Int
        let device: RecordDevice
        let files: [String: String]
    }

    /// A record file: this version's ({"v": 2, "device": …, "files": …}) or
    /// an older one's bare map (device nil, taken as the configured
    /// device's, as it always was). Keys in NFC (key); nil when it can't be
    /// read - no record, so nothing is deleted. Same file as otc-sync's.
    static func parseRecord(_ data: Data) -> (files: [String: String], device: RecordDevice?)? {
        if let rec = try? JSONDecoder().decode(RecordFile.self, from: data), rec.v == recordVersion {
            return (normalized(rec.files), rec.device)
        }
        guard let map = try? JSONDecoder().decode([String: String].self, from: data) else { return nil }
        return (normalized(map), nil)
    }

    static func encodeRecord(files: [String: String], device: RecordDevice) -> Data? {
        try? JSONEncoder().encode(RecordFile(v: recordVersion, device: device, files: files))
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
