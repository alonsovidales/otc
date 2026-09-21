// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI
import AVFoundation
import BackgroundTasks
import UIKit
import UserNotifications

@main
struct OTCApp: App {
    // Issue #43: real push delivery needs UIKit's app-launch callbacks
    // (didRegisterForRemoteNotificationsWithDeviceToken), which SwiftUI's
    // App protocol has no equivalent for — this adaptor is the standard way
    // to get a UIApplicationDelegate inside an otherwise-SwiftUI app.
    @UIApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    init() {
        BGTaskScheduler.shared.register(forTaskWithIdentifier: "com.yourco.otc.sync", using: nil) { task in
            guard let task = task as? BGProcessingTask else { return }
            SyncScheduler.handle(task: task)
        }
        Task.detached {
            try await PhotoSync.shared.runForeground()
        }

        // Make the Swift runtime instantiate the generated protobuf
        // conformances here, off the main thread, rather than wherever the
        // first message happens to be built or parsed - which at launch is
        // on the main thread. Parsing nothing is enough: the cost is the
        // metadata, not the bytes. userInitiated, not utility, because
        // something on the main thread is very likely about to want it.
        Task.detached(priority: .userInitiated) {
            _ = try? Msg_SocialPublications(serializedData: Data())
        }

        // The first AVPlayer a process creates pays for all of
        // AVFoundation waking up, and creating it in response to a tap
        // means paying that on the main thread while someone is watching
        // - reported as the whole app freezing on the first video played
        // and behaving perfectly on every one after it. Building a
        // throwaway player here does the same warm-up at launch, on a
        // background thread, where there is nothing waiting on it.
        // AVPlayer may be created off the main thread; only its views
        // may not.
        Task.detached(priority: .utility) {
            _ = AVPlayer()

            // Raising the audio session is a synchronous round trip to the
            // media server, and whoever calls play() first pays for it.
            // .ambient rather than the default .soloAmbient is also the
            // right category for a feed that starts muted - it leaves
            // whatever the owner was already listening to playing.
            // FeedAudio raises it to .playback if they unmute.
            let session = AVAudioSession.sharedInstance()
            try? session.setCategory(.ambient)
            try? session.setActive(true)

            // There used to be an AVPlayerViewController warm-up here as
            // well. It is gone because what it was amortising is gone: the
            // feed draws into a bare AVPlayerLayer now (see
            // CroppingVideoPlayer), which has no view controller, no
            // transport UI and no layout machinery to pay for up front.
        }
    }

    var body: some Scene {
        WindowGroup {
            RootView()
        }
    }
}

// Issue #43: registers this device for APNs and forwards the token to the
// OTC device the same way the browser forwards a PushSubscription (see
// web/src/net/webPush.ts) — sending the actual push still needs the device
// owner to configure an APNs Auth Key in the [apns] config section (see
// push/push.go); this registration path works and is harmless without one.
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) { granted, error in
            if let error {
                print("Notification authorization request failed:", error)
                return
            }
            guard granted else { return }
            DispatchQueue.main.async {
                application.registerForRemoteNotifications()
            }
        }
        return true
    }

    func application(
        _ application: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
    ) {
        let token = deviceToken.map { String(format: "%02x", $0) }.joined()
        // This can fire very early in app launch - possibly before
        // OTCConnection has finished its own first connect+auth (which
        // itself needs SecretsStore already populated) - so a single
        // attempt risks silently dropping the token if it loses that race.
        // Retry with backoff instead of giving up after one try; this also
        // self-heals a transient network hiccup on launch.
        Task {
            var delaySeconds: UInt64 = 1
            for attempt in 1...6 {
                do {
                    _ = try await OTCConnection.shared.request { req in
                        var reg = Msg_RegisterApnsToken()
                        reg.token = token
                        req.payload = .reqRegisterApnsToken(reg)
                    }
                    print("Registered APNs token with device")
                    return
                } catch {
                    print("Failed to register APNs token with device (attempt \(attempt)/6):", error)
                    if attempt == 6 { return }
                    try? await Task.sleep(nanoseconds: delaySeconds * 1_000_000_000)
                    delaySeconds = min(delaySeconds * 2, 30)
                }
            }
        }
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: Error
    ) {
        // Expected until Push Notifications capability + an APNs Auth Key
        // are set up (see the doc comment above) — not fatal either way.
        print("Failed to register for remote notifications:", error)
    }
}
