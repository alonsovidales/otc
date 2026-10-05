// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  AssetSyncCache.swift
//  OffTheCloud
//
//  Issue #58 follow-up: remembers, per PHAsset.localIdentifier, the content
//  hash it last successfully synced under. Without this, a later sync run
//  (most importantly after a reinstall — SecretsStore.deviceId is a fresh
//  UUID then, so every remote path this device would use is brand new and
//  the server's own path-based `knownPaths` check in PhotoSync can never
//  match) has no way to know an asset's content already exists on the
//  device without downloading the full original from iCloud and hashing
//  it — the exact ~20+ second-per-photo cost issue #58's server-side dedup
//  was meant to let a client avoid, just moved to the client's iCloud
//  fetch instead of the network upload. A hit here skips both: the cached
//  hash goes straight to LinkFile.
//
//  Also keeps PhotoSync's retry list: assets a run attempted and couldn't
//  finish, tried again by later runs although the date watermark has moved
//  past them.

import Foundation

final class AssetSyncCache {
    static let shared = AssetSyncCache(
        directory: FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0])

    /// The whole map, the format every release has read.
    private let url: URL
    /// Records since that snapshot, one "<localIdentifier>\t<hash>" line
    /// each, appended as they happen. Rewriting the whole JSON every 25
    /// records (as this used to) still made a first sync O(n^2): ~23 GB
    /// of encoding and flash writes for 100k photos, under the lock the
    /// concurrent uploads wait on. Folded back into the snapshot by
    /// flush() once it gets long.
    private let journal: AppendLog
    /// The retry list: "r\t<id>" (retry), "i\t<id>" (retry once "Sync from
    /// iCloud" is on) and "-\t<id>" (resolved) lines.
    private let pendingLog: AppendLog
    private var cache: [String: String] = [:]
    private var retry = Set<String>()
    private var retryICloud = Set<String>()
    // Serializes both the in-memory state and the file writes below -
    // PhotoSync's sync loop calls record(_:hash:) from several concurrent
    // tasks at once (cMaxConcurrentUploads).
    private let queue = DispatchQueue(label: "otc.asset-sync-cache")

    /// `directory` is Application Support; tests pass their own.
    init(directory dir: URL) {
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        url = dir.appendingPathComponent("asset_sync_cache.json")
        journal = AppendLog(url: dir.appendingPathComponent("asset_sync_cache.log"))
        pendingLog = AppendLog(url: dir.appendingPathComponent("asset_sync_pending.log"))
        if let data = try? Data(contentsOf: url),
           let decoded = try? JSONDecoder().decode([String: String].self, from: data) {
            cache = decoded
        }
        for (key, value) in journal.read() {
            cache[key] = value
        }
        for (op, id) in pendingLog.read() {
            switch op {
            case "r": retry.insert(id); retryICloud.remove(id)
            case "i": retryICloud.insert(id); retry.remove(id)
            case "-": retry.remove(id); retryICloud.remove(id)
            default: break
            }
        }
        compactIfLong()
    }

    func hash(for localIdentifier: String) -> String? {
        queue.sync { cache[localIdentifier] }
    }

    func record(localIdentifier: String, hash: String) {
        queue.sync {
            guard cache[localIdentifier] != hash else { return }
            cache[localIdentifier] = hash
            journal.append(localIdentifier, hash)
        }
    }

    /// Every record is already on disk; this folds a long journal back into
    /// the snapshot so the next launch replays little. Called at the end
    /// of a sync run.
    func flush() {
        queue.sync { compactIfLong() }
    }

    /// Log Out: another device knows none of these hashes.
    func clear() {
        queue.sync {
            cache = [:]
            retry = []
            retryICloud = []
            journal.remove()
            pendingLog.remove()
            try? FileManager.default.removeItem(at: url)
        }
    }

    // MARK: Retry list

    /// The assets to try again. Those that failed while "Sync from iCloud"
    /// was off only once it is on: retrying them with it still off would
    /// fail the same way.
    func pending(includeICloud: Bool) -> [String] {
        queue.sync { Array(includeICloud ? retry.union(retryICloud) : retry) }
    }

    func markPending(_ ids: [String], iCloud: Bool) {
        guard !ids.isEmpty else { return }
        queue.sync {
            for id in ids {
                // Both sides evaluated: the id moves between the lists.
                let added = iCloud ? retryICloud.insert(id).inserted : retry.insert(id).inserted
                let moved = (iCloud ? retry.remove(id) : retryICloud.remove(id)) != nil
                if added || moved { pendingLog.append(iCloud ? "i" : "r", id) }
            }
        }
    }

    /// Done, given up on (it can never work) or gone from the library.
    func resolve(_ ids: [String]) {
        guard !ids.isEmpty else { return }
        queue.sync {
            for id in ids where retry.remove(id) != nil || retryICloud.remove(id) != nil {
                pendingLog.append("-", id)
            }
        }
    }

    /// Sync From Now: skip everything already in the library.
    func clearPending() {
        queue.sync {
            retry = []
            retryICloud = []
            pendingLog.remove()
        }
    }

    /// Must only be called while already on `queue` (or from init).
    private func compactIfLong() {
        if journal.lines > max(1000, cache.count / 2),
           let data = try? JSONEncoder().encode(cache),
           (try? data.write(to: url, options: .atomic)) != nil {
            // A crash before this line is harmless: replaying the journal
            // over the new snapshot gives the same map.
            journal.remove()
        }
        if pendingLog.lines > max(200, 2 * (retry.count + retryICloud.count)) {
            pendingLog.replace(with: retry.sorted().map { ("r", $0) } + retryICloud.sorted().map { ("i", $0) })
        }
    }
}

/// A file of "<a>\t<b>" lines that only grows by appends, so each write is
/// O(1); the owner rewrites it whole now and then. Not thread-safe: the
/// owner serializes access. localIdentifiers and hex hashes never contain
/// a tab or a newline.
final class AppendLog {
    let url: URL
    private var handle: FileHandle?
    /// Lines in the file, to decide when to compact it.
    private(set) var lines = 0

    init(url: URL) {
        self.url = url
    }

    /// The well-formed lines. A kill mid-append leaves a last line without
    /// its newline: it is dropped, and anything appended after it merges
    /// into one line with too many fields, which is skipped as well.
    func read() -> [(String, String)] {
        guard let data = try? Data(contentsOf: url) else { return [] }
        var parts = String(decoding: data, as: UTF8.self).split(separator: "\n", omittingEmptySubsequences: false)
        parts.removeLast() // "" when the file ends with a newline, else the torn line
        lines = parts.count
        return parts.compactMap { line in
            let f = line.split(separator: "\t", omittingEmptySubsequences: false)
            guard f.count == 2, !f[0].isEmpty, !f[1].isEmpty else { return nil }
            return (String(f[0]), String(f[1]))
        }
    }

    func append(_ a: String, _ b: String) {
        do {
            if handle == nil {
                if !FileManager.default.fileExists(atPath: url.path) {
                    FileManager.default.createFile(atPath: url.path, contents: nil)
                }
                let h = try FileHandle(forWritingTo: url)
                try h.seekToEnd()
                handle = h
            }
            try handle?.write(contentsOf: Data("\(a)\t\(b)\n".utf8))
            lines += 1
        } catch {
            try? handle?.close()
            handle = nil
        }
    }

    func replace(with entries: [(String, String)]) {
        try? handle?.close()
        handle = nil
        let text = entries.map { "\($0.0)\t\($0.1)\n" }.joined()
        if (try? Data(text.utf8).write(to: url, options: .atomic)) != nil {
            lines = entries.count
        }
    }

    func remove() {
        try? handle?.close()
        handle = nil
        try? FileManager.default.removeItem(at: url)
        lines = 0
    }
}
