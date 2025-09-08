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
        let req = BGProcessingTaskRequest(identifier: "com.yourco.otc.sync")
        req.earliestBeginDate = Date(timeIntervalSinceNow: 15 * 60) // 15 minutes
        req.requiresNetworkConnectivity = true
        req.requiresExternalPower = false
        try? BGTaskScheduler.shared.submit(req)
    }

    static func handle(task: BGProcessingTask) {
        print("Handle")
        scheduleNext() // plan the next one
        task.expirationHandler = {
            // Called if you run out of time
        }
        Task.detached {
            do {
                print("Run forground sync...")
                try await PhotoSync.shared.runForeground()
                task.setTaskCompleted(success: true)
            } catch {
                task.setTaskCompleted(success: false)
            }
        }
    }
}
