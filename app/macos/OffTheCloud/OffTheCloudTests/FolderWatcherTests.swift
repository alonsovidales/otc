// SPDX-License-Identifier: AGPL-3.0-or-later

import CoreServices
import Testing
@testable import OffTheCloud

/// FSEvents says when it lost track of changes - events dropped in the
/// kernel or in the app's queue, or a folder to be scanned again - and a
/// backup then looks at the whole folder at once rather than at the next
/// 10-minute pass. As otc-sync's watcher on an overflow. Runs in a scratch
/// SwiftPM package with FolderWatcher.swift, as SyncPathsTests.
struct FolderWatcherTests {
    @Test func droppedEventsMeanTheWholeFolder() {
        for flag in [kFSEventStreamEventFlagMustScanSubDirs, kFSEventStreamEventFlagUserDropped, kFSEventStreamEventFlagKernelDropped] {
            #expect(FolderWatcher.eventsLost(FSEventStreamEventFlags(flag | kFSEventStreamEventFlagItemIsFile)))
        }
        #expect(!FolderWatcher.eventsLost(FSEventStreamEventFlags(kFSEventStreamEventFlagItemModified | kFSEventStreamEventFlagItemIsFile)))
        #expect(!FolderWatcher.eventsLost(FSEventStreamEventFlags(kFSEventStreamEventFlagItemIsDir | kFSEventStreamEventFlagItemCreated)))
        #expect(!FolderWatcher.eventsLost(0))
    }
}
