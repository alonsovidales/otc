// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Photos
import Testing
@testable import OffTheCloud

/// Settings > Sync Options > Albums and what the sync does with it: the
/// list, the skip list, the one-off passes, limited access. No PhotoKit:
/// albums come as AlbumInfo and photos as local identifiers. Android's
/// PhotoFoldersTest and PhotoSyncPlanTest are the same rules.
struct PhotoSourcesTests {
    private func album(_ id: String, _ title: String, photos: Int = 0, videos: Int = 0, smart: Bool = false) -> AlbumInfo {
        AlbumInfo(id: id, title: title, photos: photos, videos: videos, isSmart: smart)
    }

    // MARK: The list

    @Test func usersAlbumsByTitleThenTheSmartOnes() {
        let user = [
            album("u3", "trips", photos: 3),
            album("u1", "Album 10", photos: 1),
            album("u2", "Album 9", photos: 2),
            album("u4", "Album 9", photos: 1),
        ]
        let smart = [album("s1", "Screenshots", photos: 40, smart: true), album("s2", "Selfies", photos: 7, smart: true)]
        let list = PhotoSources.build(user: user, smart: smart, includeVideos: true)
        // Numbers in titles as Photos sorts them, case aside; the same
        // title by id; the smart ones after, in the order given.
        #expect(list.map(\.id) == ["u2", "u4", "u1", "u3", "s1", "s2"])
    }

    @Test func albumsWithNothingToBackUpArentListed() {
        let user = [album("empty", "Empty"), album("clips", "Clips", videos: 4), album("mixed", "Mixed", photos: 1, videos: 1)]
        let recordings = album("rec", "Screen Recordings", videos: 2, smart: true)
        let withVideos = PhotoSources.build(user: user, smart: [recordings], includeVideos: true)
        #expect(withVideos.map(\.id) == ["clips", "mixed", "rec"])
        // Include videos off: albums of videos only (Screen Recordings among
        // them) hold nothing the sync sends.
        let photosOnly = PhotoSources.build(user: user, smart: [recordings], includeVideos: false)
        #expect(photosOnly.map(\.id) == ["mixed"])
    }

    /// The decision, in one place: Screenshots, Selfies and Screen
    /// Recordings, in that order - nothing that would leave out ordinary
    /// camera photos.
    @Test func theSmartAlbumsOffered() {
        #expect(PhotoSources.offeredSmartAlbums == [.smartAlbumScreenshots, .smartAlbumSelfPortraits, .smartAlbumScreenRecordings])
    }

    @Test func countLines() {
        #expect(PhotoSources.countLine(photos: 1, videos: 0, includeVideos: true) == "1 photo")
        #expect(PhotoSources.countLine(photos: 310, videos: 0, includeVideos: true) == "310 photos")
        #expect(PhotoSources.countLine(photos: 0, videos: 1, includeVideos: true) == "1 video")
        #expect(PhotoSources.countLine(photos: 12, videos: 56, includeVideos: true) == "12 photos · 56 videos")
        // Videos aren't counted while they aren't backed up.
        #expect(PhotoSources.countLine(photos: 12, videos: 56, includeVideos: false) == "12 photos")
    }

    @Test func rowValueCountsOnlyAlbumsListedNow() {
        #expect(PhotoSources.rowValue(existing: ["a", "b"], skipped: []) == "All")
        // A skipped album that is gone (or empty) isn't counted.
        #expect(PhotoSources.rowValue(existing: ["a", "b"], skipped: ["gone"]) == "All")
        #expect(PhotoSources.rowValue(existing: ["a", "b", "c"], skipped: ["a", "gone"]) == "1 off")
        #expect(PhotoSources.rowValue(existing: ["a", "b", "c"], skipped: ["a", "c", "gone"]) == "2 off")
    }

    // MARK: The skip list

    @Test func turningAnAlbumOffAndOn() {
        // Off: skipped, and a pass still waiting for it dropped.
        var next = PhotoSources.toggled("a", skipped: true, skips: ["b"], backfill: ["a", "*"])
        #expect(next.skips == ["b", "a"])
        #expect(next.backfill == ["*"])
        // Off twice: listed once.
        next = PhotoSources.toggled("a", skipped: true, skips: next.skips, backfill: next.backfill)
        #expect(next.skips == ["b", "a"])
        // On: no longer skipped, and a pass over what it holds queued once.
        next = PhotoSources.toggled("a", skipped: false, skips: next.skips, backfill: next.backfill)
        #expect(next.skips == ["b"])
        #expect(next.backfill == ["*", "a"])
        next = PhotoSources.toggled("a", skipped: false, skips: next.skips, backfill: next.backfill)
        #expect(next.backfill == ["*", "a"])
    }

    /// SecretsStore keeps it in UserDefaults; Log Out wipes the domain.
    /// The value in place before is put back.
    @Test func theSkipListIsKept() {
        // Never on a simulator signed in to a device: saving the store
        // rewrites the app's own connection (a fresh test simulator has none).
        guard SecretsStore.loadOrCreate().endpoint.isEmpty else { return }
        let key = SecretsStore.skippedAlbumsKey
        let d = UserDefaults.standard
        let before = d.object(forKey: key)
        defer {
            if let before { d.set(before, forKey: key) } else { d.removeObject(forKey: key) }
        }
        let store = SecretsStore.loadOrCreate()
        let kept = store.skippedAlbums
        store.skippedAlbums = kept + ["TEST-ALBUM/L0/040"]
        #expect(d.stringArray(forKey: key) == kept + ["TEST-ALBUM/L0/040"])
        #expect(SecretsStore.loadOrCreate().skippedAlbums == kept + ["TEST-ALBUM/L0/040"])
    }

    @Test func photoSyncStateIsKept() throws {
        let suite = "photosync-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("photosync-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: dir) }
        let state = PhotoSync.State(defaults: defaults, directory: dir)

        // Nothing stored: today's behaviour.
        #expect(state.backfill.isEmpty)
        #expect(state.floor == nil)
        #expect(state.changeToken == nil)
        #expect(!state.accessWasLimited)
        #expect(state.limitedSeen() == nil)

        state.queueBackfill("album")
        state.queueBackfill(PhotoSources.catchUp)
        state.queueBackfill("album")
        #expect(state.backfill == ["album", "*"])
        // Read back by another instance, as the next launch would.
        #expect(PhotoSync.State(defaults: defaults, directory: dir).backfill == ["album", "*"])
        state.backfill = []
        #expect(defaults.object(forKey: PhotoSync.State.backfillKey) == nil)

        state.floor = .distantPast
        #expect(state.floor == .distantPast)
        state.changeToken = Data([1, 2, 3])
        #expect(state.changeToken == Data([1, 2, 3]))
        state.changeToken = nil
        #expect(state.changeToken == nil)

        state.accessWasLimited = true
        #expect(state.accessWasLimited)
        state.accessWasLimited = false
        #expect(defaults.object(forKey: PhotoSync.State.wasLimitedKey) == nil)

        state.setLimitedSeen(["b", "a"])
        #expect(state.limitedSeen() == ["a", "b"])
        state.removeLimitedSeen()
        #expect(state.limitedSeen() == nil)
    }

    // MARK: One-off passes

    @Test func oneOffPasses() {
        let floor = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(PhotoSources.oneOffs([], floor: floor).isEmpty)
        #expect(PhotoSources.oneOffs(["a", "*", "b"], floor: floor) == [.album("a"), .since(floor), .album("b")])
        // Everything: the whole library.
        #expect(PhotoSources.oneOffs(["*"], floor: .distantPast) == [.since(nil)])
        // No floor known: no catch-up (the album passes still go).
        #expect(PhotoSources.oneOffs(["a", "*"], floor: nil) == [.album("a")])
    }

    @Test func aPassIsDoneOnlyWhenTheRunFinished() {
        // Queued while the run went on: stays.
        #expect(PhotoSources.afterPass(stored: ["a", "*", "c"], taken: ["a", "*"], skippedInRun: [], finished: true) == ["c"])
        #expect(PhotoSources.afterPass(stored: ["a", "*", "c"], taken: ["a", "*"], skippedInRun: [], finished: false) == ["a", "*", "c"])
        #expect(PhotoSources.afterPass(stored: [], taken: ["a"], skippedInRun: [], finished: true) == [])
    }

    /// An album turned back on while a run went on: the run took its pass
    /// but still had it off, so it sent nothing of it. The pass stays for
    /// the next run; the others it took are done.
    @Test func aPassTakenWhileItsAlbumWasStillOffStays() {
        #expect(PhotoSources.afterPass(stored: ["a", "*", "b"], taken: ["a", "*", "b"], skippedInRun: ["a", "z"], finished: true) == ["a"])
        // Turned off again since (no longer queued): nothing comes back.
        #expect(PhotoSources.afterPass(stored: ["*"], taken: ["a", "*"], skippedInRun: ["a"], finished: true) == [])
    }

    /// New photos left out as skipped: the watermark goes to the newest new
    /// one, never past the run's start nor back.
    @Test func theWatermarkPastSkippedPhotos() {
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        let earlier = start.addingTimeInterval(-3600)
        let later = start.addingTimeInterval(3600)
        // Nothing dated: it stays.
        #expect(PhotoSources.watermarkPastSkipped(newest: nil, runStart: start, stored: earlier) == nil)
        #expect(PhotoSources.watermarkPastSkipped(newest: earlier, runStart: start, stored: nil) == earlier)
        #expect(PhotoSources.watermarkPastSkipped(newest: earlier, runStart: start, stored: earlier.addingTimeInterval(-1)) == earlier)
        // Already as far, or further (the chunks moved it): it stays.
        #expect(PhotoSources.watermarkPastSkipped(newest: earlier, runStart: start, stored: earlier) == nil)
        #expect(PhotoSources.watermarkPastSkipped(newest: earlier, runStart: start, stored: start) == nil)
        // A future date (a wrong camera clock): no further than the run's start.
        #expect(PhotoSources.watermarkPastSkipped(newest: later, runStart: start, stored: earlier) == start)
    }

    @Test func theRunsList() {
        // Retried first, then one-off passes, then the new ones; each once.
        let plain = PhotoSources.runList(retried: ["r1", "n2"], oneOff: ["o1", "r1", "n1"], fresh: ["n1", "n2"], skipped: [])
        #expect(plain.ids == ["r1", "o1", "n1", "n2"])
        #expect(plain.exempt == ["r1", "o1"])
        #expect(!plain.droppedFresh)

        // Skipped wins over any path: retried, one-off and new.
        let skipping = PhotoSources.runList(retried: ["r1", "r2"], oneOff: ["o1", "o2"], fresh: ["n1", "n2"], skipped: ["r2", "o1", "n2"])
        #expect(skipping.ids == ["r1", "o2", "n1"])
        #expect(skipping.exempt == ["r1", "o2"])
        #expect(skipping.droppedFresh)
    }

    /// Nothing turned off and nothing queued: the run is what it always
    /// was - the retry list, then the date fetch.
    @Test func withNothingOffTheRunIsAsBefore() {
        let list = PhotoSources.runList(retried: ["r1"], oneOff: [], fresh: ["n1", "n2"], skipped: [])
        #expect(list.ids == ["r1", "n1", "n2"])
        #expect(list.exempt == ["r1"])
    }

    // MARK: Limited access

    @Test func photosPickedSinceTheLastRun() {
        // First time: only recorded.
        #expect(PhotoSources.newlyVisible(seen: nil, visible: ["a", "b"]).isEmpty)
        #expect(PhotoSources.newlyVisible(seen: ["a"], visible: ["a", "b", "c"]) == ["b", "c"])
        // Unpicked ones don't matter.
        #expect(PhotoSources.newlyVisible(seen: ["a", "b"], visible: ["b"]).isEmpty)
    }

    @Test func fullAccessAfterLimitedAccess() {
        typealias Step = PhotoSync.AccessStep
        for floorKnown in [true, false] {
            for wasLimited in [true, false] {
                // Still limited, or taken away (neither limited nor full).
                #expect(PhotoSync.accessStep(limited: true, full: false, wasLimited: wasLimited, floorKnown: floorKnown) == Step.none)
                #expect(PhotoSync.accessStep(limited: false, full: false, wasLimited: wasLimited, floorKnown: floorKnown) == Step.none)
            }
            // Full all along.
            #expect(PhotoSync.accessStep(limited: false, full: true, wasLimited: false, floorKnown: floorKnown) == Step.none)
        }
        #expect(PhotoSync.accessStep(limited: false, full: true, wasLimited: true, floorKnown: true) == .catchUp)
        #expect(PhotoSync.accessStep(limited: false, full: true, wasLimited: true, floorKnown: false) == .askAgain)
    }
}
