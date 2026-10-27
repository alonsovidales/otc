// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SyncScheduler.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//


import Foundation
import BackgroundTasks
import Network

enum SyncScheduler {
    static func scheduleNext() {
        print("SyncScheduler running in background...")
        let req = BGProcessingTaskRequest(identifier: "cloud.off-the.OffTheCloud.sync")
        req.earliestBeginDate = Date(timeIntervalSinceNow: 15 * 60) // 15 minutes
        req.requiresNetworkConnectivity = true
        req.requiresExternalPower = false
        try? BGTaskScheduler.shared.submit(req)
    }

    /// Log Out: nothing to sync until someone signs in again.
    static func cancel() {
        BGTaskScheduler.shared.cancelAllTaskRequests()
    }

    static func handle(task: BGProcessingTask) {
        print("Handle")
        scheduleNext() // plan the next one, regardless of how this one goes

        let done = TaskDone(task)
        // IOS-02: the run in flight, if there is one, is waited for - it
        // then goes through once more for this task - rather than the task
        // reported done at once while that run had barely started.
        let work = Task.detached {
            do {
                print("Run background sync...")
                try await PhotoSync.shared.runInBackground()
                done.complete(success: true)
            } catch {
                done.complete(success: false)
            }
        }
        // Issue #70: iOS calling this means the task's time is up.
        // runInBackground's cancellation handler passes it on to the run:
        // it stops between chunks, uploads in flight stop at their next 4
        // MiB chunk, the watermark stays before what wasn't finished.
        // IOS-04: and the task is reported at once - not once the run has
        // wound down, which an iCloud download or a request waiting on the
        // socket can take a while to do, past the moment iOS gave.
        task.expirationHandler = {
            work.cancel()
            done.complete(success: false)
        }
    }
}

/// setTaskCompleted, once: by the run's end or the task's expiration,
/// whichever comes first.
private final class TaskDone: @unchecked Sendable {
    private let task: BGProcessingTask
    private let lock = NSLock()
    private var completed = false

    init(_ task: BGProcessingTask) { self.task = task }

    func complete(success: Bool) {
        let first: Bool = lock.withLock {
            defer { completed = true }
            return !completed
        }
        if first { task.setTaskCompleted(success: success) }
    }
}
