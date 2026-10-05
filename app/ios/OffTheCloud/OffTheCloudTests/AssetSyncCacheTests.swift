// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

struct AssetSyncCacheTests {
    private func tempDir() -> URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("asc-\(UUID().uuidString)")
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    @Test func recordsSurviveARelaunchWithoutFlush() {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let a = AssetSyncCache(directory: dir)
        a.record(localIdentifier: "A/L0/001", hash: "aa")
        a.record(localIdentifier: "B/L0/001", hash: "bb")
        a.record(localIdentifier: "A/L0/001", hash: "cc")

        let b = AssetSyncCache(directory: dir)
        #expect(b.hash(for: "A/L0/001") == "cc")
        #expect(b.hash(for: "B/L0/001") == "bb")
    }

    @Test func readsAnOlderReleasesSnapshot() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let json = try JSONEncoder().encode(["A/L0/001": "aa"])
        try json.write(to: dir.appendingPathComponent("asset_sync_cache.json"))

        let c = AssetSyncCache(directory: dir)
        #expect(c.hash(for: "A/L0/001") == "aa")
        c.record(localIdentifier: "B/L0/001", hash: "bb")
        #expect(AssetSyncCache(directory: dir).hash(for: "A/L0/001") == "aa")
        #expect(AssetSyncCache(directory: dir).hash(for: "B/L0/001") == "bb")
    }

    @Test func aTornLastLineIsIgnored() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let log = dir.appendingPathComponent("asset_sync_cache.log")
        try Data("A/L0/001\taa\nB/L0/001\tbb".utf8).write(to: log)

        let c = AssetSyncCache(directory: dir)
        #expect(c.hash(for: "A/L0/001") == "aa")
        #expect(c.hash(for: "B/L0/001") == nil)
        // An append after the torn line merges with it: skipped, never a
        // wrong hash.
        c.record(localIdentifier: "C/L0/001", hash: "cc")
        let d = AssetSyncCache(directory: dir)
        #expect(d.hash(for: "B/L0/001") == nil)
        #expect(d.hash(for: "A/L0/001") == "aa")
    }

    @Test func flushCompactsALongJournal() {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let c = AssetSyncCache(directory: dir)
        for i in 0..<1200 { c.record(localIdentifier: "id\(i)", hash: "h\(i)") }
        c.flush()
        let log = dir.appendingPathComponent("asset_sync_cache.log")
        #expect(!FileManager.default.fileExists(atPath: log.path))
        let d = AssetSyncCache(directory: dir)
        #expect(d.hash(for: "id0") == "h0")
        #expect(d.hash(for: "id1199") == "h1199")
    }

    @Test func clearForgetsEverything() {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let c = AssetSyncCache(directory: dir)
        c.record(localIdentifier: "A/L0/001", hash: "aa")
        c.markPending(["B/L0/001"], iCloud: false)
        c.clear()
        #expect(c.hash(for: "A/L0/001") == nil)
        #expect(c.pending(includeICloud: true).isEmpty)
        let d = AssetSyncCache(directory: dir)
        #expect(d.hash(for: "A/L0/001") == nil)
        #expect(d.pending(includeICloud: true).isEmpty)
    }

    @Test func retryListSurvivesARelaunch() {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let c = AssetSyncCache(directory: dir)
        c.markPending(["A", "B"], iCloud: false)
        c.markPending(["C"], iCloud: true)
        c.resolve(["B"])

        let d = AssetSyncCache(directory: dir)
        #expect(Set(d.pending(includeICloud: false)) == ["A"])
        #expect(Set(d.pending(includeICloud: true)) == ["A", "C"])
    }

    @Test func anAssetMovesBetweenTheRetryLists() {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let c = AssetSyncCache(directory: dir)
        c.markPending(["A"], iCloud: false)
        c.markPending(["A"], iCloud: true)
        #expect(c.pending(includeICloud: false).isEmpty)
        c.markPending(["A"], iCloud: false)
        #expect(AssetSyncCache(directory: dir).pending(includeICloud: false) == ["A"])
    }

    @Test func clearPendingKeepsTheHashes() {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let c = AssetSyncCache(directory: dir)
        c.record(localIdentifier: "A", hash: "aa")
        c.markPending(["B"], iCloud: true)
        c.clearPending()
        let d = AssetSyncCache(directory: dir)
        #expect(d.pending(includeICloud: true).isEmpty)
        #expect(d.hash(for: "A") == "aa")
    }

    @Test func aLongRetryLogIsCompacted() {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let c = AssetSyncCache(directory: dir)
        for i in 0..<300 {
            c.markPending(["id\(i)"], iCloud: false)
            c.resolve(["id\(i)"])
        }
        c.markPending(["kept"], iCloud: true)
        c.flush()
        let log = dir.appendingPathComponent("asset_sync_pending.log")
        let text = (try? String(contentsOf: log, encoding: .utf8)) ?? ""
        #expect(text == "i\tkept\n")
        #expect(AssetSyncCache(directory: dir).pending(includeICloud: true) == ["kept"])
    }
}
