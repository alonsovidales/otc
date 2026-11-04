// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// What VoiceOver can do where only a drag could: the date scrubber, a
/// month a swipe; the trimmer's handles, a step a swipe.
@MainActor
struct VoiceOverControlTests {
    @Test func theScrubberGoesAMonthASwipeAndJumpsThere() {
        let vm = PhotoGalleryVM()
        vm.photoRequest = { _, _ in throw CancellationError() }
        // No months known: nothing to name or step through.
        #expect(vm.scrubberMonth == nil)
        vm.scrubStep(newer: false)
        #expect(vm.jumpMonth == nil)
        vm.setDateBuckets([("2026-08", 10), ("2025-01", 30), ("2019-06", 900)], key: vm.currentBucketKey)
        // At rest it names the newest month.
        #expect(vm.scrubberMonth == "2026-08")
        vm.scrubStep(newer: true)
        #expect(vm.jumpMonth == nil, "nothing newer: no jump")
        // Older, a month at a time, jumped to at once under grey tiles.
        vm.scrubStep(newer: false)
        #expect(vm.jumpMonth == "2025-01")
        #expect(vm.scrubberMonth == "2025-01")
        #expect(vm.placeholderMonth == "2025-01")
        #expect(vm.placeholderCount == 30)
        vm.scrubStep(newer: false)
        #expect(vm.scrubberMonth == "2019-06")
        #expect(vm.placeholderCount == PhotoGalleryVM.maxPlaceholders)
        vm.scrubStep(newer: false)
        #expect(vm.scrubberMonth == "2019-06", "the oldest stays")
        vm.scrubStep(newer: true)
        #expect(vm.scrubberMonth == "2025-01")
    }

    @Test func aMonthIsSaidWhole() {
        #expect(PhotoMonths.spoken("2024-03") == S.appPhotosMonthsTitle(month: S.appPhotosMonthsMarch, year: "2024"))
        #expect(PhotoMonths.spoken("0000-00") == S.appPhotosMonthsNoDate)
    }

    @Test func theTrimmersHandlesStepAHundredthOfTheClip() {
        #expect(DualRangeSlider.step(for: 60) == 0.6)
        #expect(DualRangeSlider.step(for: 600) == 6)
        // Never under a tenth of a second.
        #expect(DualRangeSlider.step(for: 3) == 0.1)
        #expect(DualRangeSlider.step(for: 0) == 0.1)
        #expect(abs(DualRangeSlider.stepped(10, true, duration: 60) - 10.6) < 1e-9)
        #expect(abs(DualRangeSlider.stepped(10, false, duration: 60) - 9.4) < 1e-9)
    }
}
