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
        c.clear()
        #expect(c.hash(for: "A/L0/001") == nil)
        #expect(AssetSyncCache(directory: dir).hash(for: "A/L0/001") == nil)
    }
}
