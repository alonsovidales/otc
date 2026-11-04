// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Photos
import Testing
@testable import OffTheCloud

/// What a copy that failed with "Sync from iCloud" off waits for, and what a
/// BG task's end stops.
struct ICloudWaitTests {
    @Test func onlyWhatPhotoKitSaysWaitsForSyncFromICloud() {
        // The photo is in iCloud, or PhotoKit can't read it without the
        // network: tried again once the switch is on.
        #expect(PhotoSync.waitsForICloud(NSError(domain: PHPhotosErrorDomain, code: PHPhotosError.Code.networkAccessRequired.rawValue)))
        #expect(PhotoSync.waitsForICloud(NSError(domain: "CloudPhotoLibraryErrorDomain", code: 1005)))
        // A run stopped mid-copy (the app's background time over, a BG
        // task's end): the next run tries it again.
        #expect(!PhotoSync.waitsForICloud(CancellationError()))
        #expect(!PhotoSync.waitsForICloud(NSError(domain: PHPhotosErrorDomain, code: PHPhotosError.Code.userCancelled.rawValue)))
        // The copy's own file failing, or the disk.
        #expect(!PhotoSync.waitsForICloud(NSError(domain: "PhotoExport", code: -11)))
        #expect(!PhotoSync.waitsForICloud(NSError(domain: "PhotoExport", code: -12)))
        #expect(!PhotoSync.waitsForICloud(NSError(domain: NSPOSIXErrorDomain, code: Int(EIO))))
        #expect(!PhotoSync.waitsForICloud(NSError(domain: NSCocoaErrorDomain, code: NSFileWriteUnknownError)))
    }

    @Test func aBGTasksEndStopsOnlyARunNoContinuedTaskHolds() {
        #expect(SyncScheduler.stopsRunAtExpiry(continuedTaskHolds: false))
        #expect(!SyncScheduler.stopsRunAtExpiry(continuedTaskHolds: true))
    }
}
