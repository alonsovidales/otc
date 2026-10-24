// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Free up space: what may be deleted, and when a run is due. Android's
/// FreeUpSpaceTest is the same.
struct FreeUpSpaceTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private var cutoff: Date { now.addingTimeInterval(-7 * 24 * 3600) }
    private func photo(daysAgo: Double) -> FreeUpSpace.Traits {
        FreeUpSpace.Traits(created: now.addingTimeInterval(-daysAgo * 24 * 3600))
    }

    @Test func onlyWhatIsOldEnough() {
        #expect(FreeUpSpace.eligible(photo(daysAgo: 8), cutoff: cutoff, includeVideos: true, livePhotos: false))
        #expect(!FreeUpSpace.eligible(photo(daysAgo: 6), cutoff: cutoff, includeVideos: true, livePhotos: false))
        #expect(!FreeUpSpace.eligible(photo(daysAgo: 7), cutoff: cutoff, includeVideos: true, livePhotos: false))
        // No date: its age can't be told.
        #expect(!FreeUpSpace.eligible(FreeUpSpace.Traits(created: nil), cutoff: cutoff, includeVideos: true, livePhotos: true))
    }

    /// The sync sends the original of an edited asset and one picture of a
    /// burst: neither is ever deleted.
    @Test func editsAndBurstsStay() {
        var edited = photo(daysAgo: 30)
        edited.edited = true
        var burst = photo(daysAgo: 30)
        burst.burst = true
        #expect(!FreeUpSpace.eligible(edited, cutoff: cutoff, includeVideos: true, livePhotos: true))
        #expect(!FreeUpSpace.eligible(burst, cutoff: cutoff, includeVideos: true, livePhotos: true))
    }

    @Test func livePhotosOnlyWithTheirSwitch() {
        var live = photo(daysAgo: 30)
        live.live = true
        #expect(!FreeUpSpace.eligible(live, cutoff: cutoff, includeVideos: true, livePhotos: false))
        #expect(FreeUpSpace.eligible(live, cutoff: cutoff, includeVideos: true, livePhotos: true))
    }

    /// Include videos off: the sync doesn't send them, so none is deleted.
    @Test func videosOnlyWhileSynced() {
        var video = photo(daysAgo: 30)
        video.isVideo = true
        #expect(!FreeUpSpace.eligible(video, cutoff: cutoff, includeVideos: false, livePhotos: true))
        #expect(FreeUpSpace.eligible(video, cutoff: cutoff, includeVideos: true, livePhotos: false))
    }

    /// An album turned off is left alone, even what was backed up before.
    @Test func skippedAlbumsStay() {
        var skipped = photo(daysAgo: 30)
        skipped.skipped = true
        #expect(!FreeUpSpace.eligible(skipped, cutoff: cutoff, includeVideos: true, livePhotos: true))
        #expect(FreeUpSpace.eligible(photo(daysAgo: 30), cutoff: cutoff, includeVideos: true, livePhotos: true))
    }

    /// With limited access the albums may not be listed, so their photos
    /// can't be told apart: with any album turned off, nothing is deleted.
    @Test func limitedAccessWithAlbumsOffDeletesNothing() {
        #expect(FreeUpSpace.albumsKnown(fullAccess: true, skippedAlbums: []))
        #expect(FreeUpSpace.albumsKnown(fullAccess: true, skippedAlbums: ["a"]))
        #expect(FreeUpSpace.albumsKnown(fullAccess: false, skippedAlbums: []))
        #expect(!FreeUpSpace.albumsKnown(fullAccess: false, skippedAlbums: ["a"]))
    }

    @Test func onceADay() {
        #expect(FreeUpSpace.isDue(lastRun: nil, now: now))
        #expect(!FreeUpSpace.isDue(lastRun: now.addingTimeInterval(-3600), now: now))
        #expect(FreeUpSpace.isDue(lastRun: now.addingTimeInterval(-24 * 3600), now: now))
        // Set while the clock was ahead.
        #expect(FreeUpSpace.isDue(lastRun: now.addingTimeInterval(3600), now: now))
    }

    @Test func daysFallBackToSeven() {
        #expect(FreeUpSpace.days(nil) == 7)
        #expect(FreeUpSpace.days(30) == 30)
        #expect(FreeUpSpace.days(5) == 7)
        #expect(FreeUpSpace.days("30") == 7)
        #expect(FreeUpSpace.dayChoices.contains(FreeUpSpace.defaultDays))
    }

    @Test func englishText() {
        #expect(S.appSettingsFreeUpTitle == "Free Up Space")
        #expect(S.appSettingsFreeUpToggle == "Delete from this phone")
        #expect(S.appSettingsFreeUpDays(count: 1) == "1 day")
        #expect(S.appSettingsFreeUpDays(count: 7) == "7 days")
    }
}
