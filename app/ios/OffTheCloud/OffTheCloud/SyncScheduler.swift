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

    static func handle(task: BGProcessingTask) {
        print("Handle")
        scheduleNext() // plan the next one, regardless of how this one goes

        let work = Task.detached {
            do {
                print("Run forground sync...")
                try await PhotoSync.shared.runForeground()
                task.setTaskCompleted(success: true)
            } catch {
                task.setTaskCompleted(success: false)
            }
        }
        // Issue #70: this used to be an empty closure - iOS calling it
        // means the task's time budget is up, and doing nothing here left
        // the sync running right past its allotted window instead of
        // winding down. Cancelling `work` is checked between chunks inside
        // runForeground (Task.isCancelled), so whatever chunk is already
        // uploading finishes cleanly rather than being killed mid-request.
        task.expirationHandler = {
            work.cancel()
        }
    }
}
