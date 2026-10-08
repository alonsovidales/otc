// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  RootView.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//


import SwiftUI

struct RootView: View {
    @StateObject private var secrets = SecretsStore.loadOrCreate()
    @StateObject private var upload = UploadModel.shared
    @StateObject private var notifications = NotificationsModel.shared
    // Issue #79: shared with MainView, which needs to know whether Social
    // is empty to decide the app's default launch tab.
    @StateObject private var social = SocialFeedViewModel.shared
    // Issue #183: the device's critical-update alert, banner over every tab.
    @StateObject private var updateAlert = UpdateAlertModel.shared
    // Issue #70: "send the app to background, take a photo and open it
    // again, there is no upload, I have to kill and restart it" -
    // OTCApp.init() only ever runs once per cold launch, so returning
    // from the background (without a full kill+relaunch) never re-ran the
    // sync at all. scenePhase is what actually observes that transition.
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        Group {
            if secrets.isConfigured {
                MainView(deviceID: secrets.deviceId)
                    .environmentObject(secrets)
                    .environmentObject(upload)
                    .environmentObject(notifications)
                    .environmentObject(social)
                    .environmentObject(updateAlert)
                    .onAppear {
                        // The thumbnails kept on the phone for the device
                        // signed in to, from launch - before (or without)
                        // a connection, for Settings' Thumbnail cache.
                        // Signing in binds it again (OTCConnection).
                        ThumbDiskCache.shared.use(endpoint: secrets.endpointURLString)
                        // The first sync right after setup or sign-in: the
                        // app is already active then, so the scenePhase
                        // change below never comes and photos waited for a
                        // relaunch. A no-op if a sync is already running
                        // (the cold-launch one, say). As on Android.
                        Task { try? await PhotoSync.shared.runForeground() }
                        SyncScheduler.scheduleNext() // schedule background sync
                        notifications.startPolling()
                        updateAlert.startPolling()
                        // Issue #133: a no-op on first launch (init already
                        // started it), the restart after Log Out + sign in.
                        social.startAutoLoad()
                    }
            } else {
                OnboardingView()
                    .environmentObject(secrets)
            }
        }
        .onChange(of: scenePhase) { _, newPhase in
            guard secrets.isConfigured else { return }
            switch newPhase {
            case .active:
                // Covers both "reopened from the background" and "reopened
                // after being suspended" - PhotoSync's own isSyncing guard
                // makes this a no-op if a photo-library-change-triggered
                // sync (or the cold-launch one) is already in flight.
                // Issue #190: the route first - back home, the sync goes
                // over the home network rather than holding the bridge
                // route for as long as it runs.
                Task {
                    await OTCConnection.shared.reconsiderRoute()
                    try? await PhotoSync.shared.runForeground()
                }
                // Issue #183: a no-op while already polling; after a
                // background stint it asks again straight away.
                updateAlert.startPolling()
            case .background:
                // Re-arm the next opportunistic background task on *every*
                // backgrounding, not just once via MainView's .onAppear
                // above (which only fires the first time it's inserted
                // into the view tree, not on every background/foreground
                // cycle) - otherwise only the very first background window
                // after a cold launch ever had a pending task at all.
                SyncScheduler.scheduleNext()
                // Issue #183: only polled while the app is active.
                updateAlert.stopPolling()
            default:
                break
            }
        }
    }
}
