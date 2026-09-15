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
    // Issue #70: "send the app to background, take a photo and open it
    // again, there is no upload, I have to kill and restart it" -
    // OTCApp.init() only ever runs once per cold launch, so returning
    // from the background (without a full kill+relaunch) never re-ran the
    // sync at all. scenePhase is what actually observes that transition.
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        Group {
            if secrets.isConfigured {
                MainView()
                    .environmentObject(secrets)
                    .environmentObject(upload)
                    .onAppear {
                        SyncScheduler.scheduleNext() // schedule background sync
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
                Task { try? await PhotoSync.shared.runForeground() }
            case .background:
                // Re-arm the next opportunistic background task on *every*
                // backgrounding, not just once via MainView's .onAppear
                // above (which only fires the first time it's inserted
                // into the view tree, not on every background/foreground
                // cycle) - otherwise only the very first background window
                // after a cold launch ever had a pending task at all.
                SyncScheduler.scheduleNext()
            default:
                break
            }
        }
    }
}
