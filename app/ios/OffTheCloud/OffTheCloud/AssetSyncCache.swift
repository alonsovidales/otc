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

import Foundation

final class AssetSyncCache {
    static let shared = AssetSyncCache()

    private let url: URL
    private var cache: [String: String] = [:]
    // Serializes both the in-memory dict and the file writes below -
    // PhotoSync's sync loop calls record(_:hash:) from several concurrent
    // tasks at once (cMaxConcurrentUploads).
    private let queue = DispatchQueue(label: "otc.asset-sync-cache")
    // Rewriting the whole (potentially tens-of-thousands-of-entries) JSON
    // file after every single asset would make a large first sync do
    // O(n^2) disk I/O for no real benefit — batched instead, at most once
    // per this many new records.
    private static let flushBatchSize = 25
    private var dirtyCount = 0

    private init() {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        url = dir.appendingPathComponent("asset_sync_cache.json")
        if let data = try? Data(contentsOf: url),
           let decoded = try? JSONDecoder().decode([String: String].self, from: data) {
            cache = decoded
        }
    }

    func hash(for localIdentifier: String) -> String? {
        queue.sync { cache[localIdentifier] }
    }

    func record(localIdentifier: String, hash: String) {
        queue.sync {
            guard cache[localIdentifier] != hash else { return }
            cache[localIdentifier] = hash
            dirtyCount += 1
            if dirtyCount >= Self.flushBatchSize {
                flushLocked()
            }
        }
    }

    /// Call once at the end of a sync run so the last, sub-batch-sized
    /// group of records isn't left unwritten until the next run happens
    /// to cross the batch threshold.
    func flush() {
        queue.sync { flushLocked() }
    }

    /// Must only be called while already on `queue`.
    private func flushLocked() {
        guard dirtyCount > 0 else { return }
        if let data = try? JSONEncoder().encode(cache) {
            try? data.write(to: url, options: .atomic)
        }
        dirtyCount = 0
    }
}
