// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Issue #205: when a sync asks iOS 26 to go on in the background, and what
/// the system's progress for it shows. Android's LongSyncTest has the same
/// threshold and bar.
struct LongSyncTests {
    private let mb: Int64 = 1 << 20

    @Test func onlyWhatTheOwnerAskedForInFrontAndBig() {
        #expect(LongSync.thresholdBytes == 300 * mb)
        #expect(LongSync.continued(available: true, asked: true, inFront: true, assets: 40, queuedBytes: 300 * mb + 1))
        #expect(LongSync.continued(available: true, asked: true, inFront: true, assets: 1, queuedBytes: 3 << 30))
        // At the threshold or under it: the plain run.
        #expect(!LongSync.continued(available: true, asked: true, inFront: true, assets: 40, queuedBytes: 300 * mb))
        // Before iOS 27 the sizes can't be known: Sync All and the first
        // sync go as the task whenever there is something to send.
        #expect(LongSync.continued(available: true, asked: true, inFront: true, assets: 3, queuedBytes: nil))
        #expect(!LongSync.continued(available: true, asked: true, inFront: true, assets: 0, queuedBytes: nil))
        // A run nobody asked for (a new photo, the app back in front), one
        // with the app away, or below iOS 26: as before.
        #expect(!LongSync.continued(available: true, asked: false, inFront: true, assets: 40, queuedBytes: 3 << 30))
        #expect(!LongSync.continued(available: true, asked: true, inFront: false, assets: 40, queuedBytes: 3 << 30))
        #expect(!LongSync.continued(available: false, asked: true, inFront: true, assets: 40, queuedBytes: 3 << 30))
    }

    @Test func unitsCountAssetsAndWhatIsOnItsWay() {
        #expect(BackupProgress.units(done: 0, total: 4, sending: []) == 0)
        #expect(BackupProgress.units(done: 2, total: 4, sending: [0.5]) == 250)
        // Three at once, one of them a long video half way.
        #expect(BackupProgress.units(done: 1, total: 10, sending: [0.5, 0.2, 0]) == 170)
        // Never outside the bar, whatever comes in.
        #expect(BackupProgress.units(done: 4, total: 4, sending: [1]) == 400)
        #expect(BackupProgress.units(done: 1, total: 4, sending: [-1]) == 100)
        #expect(BackupProgress.units(done: 0, total: 0, sending: [0.3]) == 0)
    }

    @Test func progressFollowsTheAssets() {
        let p = BackupProgress()
        p.begin(total: 3)
        #expect(p.snapshot() == .init(done: 0, total: 3, units: 0))
        p.sending("a", sent: 2 << 20, size: 8 << 20)
        #expect(p.snapshot() == .init(done: 0, total: 3, units: 25))
        p.sending("a", sent: 8 << 20, size: 8 << 20)
        p.done("a")
        p.done("b")
        #expect(p.snapshot() == .init(done: 2, total: 3, units: 200))
        // A size it doesn't know moves nothing.
        p.sending("c", sent: 10, size: 0)
        #expect(p.snapshot().units == 200)
        // A new pass starts from nothing.
        p.begin(total: 5)
        #expect(p.snapshot() == .init(done: 0, total: 5, units: 0))
    }
}
