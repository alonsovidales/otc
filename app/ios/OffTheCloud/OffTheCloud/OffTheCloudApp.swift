import SwiftUI
import BackgroundTasks

@main
struct OTCApp: App {
    init() {
        print("Registering task")
        BGTaskScheduler.shared.register(forTaskWithIdentifier: "com.yourco.otc.sync", using: nil) { task in
            guard let task = task as? BGProcessingTask else { return }
            SyncScheduler.handle(task: task)
        }
        try? await PhotoSync.shared.runForeground()
    }

    var body: some Scene {
        WindowGroup {
            RootView()
        }
    }
}
