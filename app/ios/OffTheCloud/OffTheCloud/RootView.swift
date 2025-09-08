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
    }
}
