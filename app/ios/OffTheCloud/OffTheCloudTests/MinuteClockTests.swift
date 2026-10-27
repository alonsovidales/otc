// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// The feed's and Alerts' "3m ago" move on while they show: reckoned from
/// the minute the TimelineView draws. Android's MinuteClockTest is the same.
struct MinuteClockTests {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    @Test func theSameTimeReadsOlderAsTheClockMovesOn() {
        #expect(formatPostDate(t0, now: t0.addingTimeInterval(30)) == "just now")
        #expect(formatPostDate(t0, now: t0.addingTimeInterval(60)) == "1m ago")
        #expect(formatPostDate(t0, now: t0.addingTimeInterval(59 * 60)) == "59m ago")
        #expect(formatPostDate(t0, now: t0.addingTimeInterval(3600)) == "1h ago")
        #expect(formatPostDate(t0, now: t0.addingTimeInterval(3 * 86400)) == "3d ago")
    }

    @MainActor
    @Test func alertsTimesMoveOnToo() {
        let a = NotificationsListView.relativeTime(t0, now: t0.addingTimeInterval(60))
        let b = NotificationsListView.relativeTime(t0, now: t0.addingTimeInterval(2 * 3600))
        #expect(a != b)
        #expect(b == NotificationsListView.relativeTime(t0, now: t0.addingTimeInterval(2 * 3600)))
    }
}
