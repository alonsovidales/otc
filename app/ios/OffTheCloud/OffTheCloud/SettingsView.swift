//
//  SettingsView.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//


import SwiftUI
import Photos

struct SettingsView: View {
    @Environment(\.dismiss) var dismiss
    @EnvironmentObject var secrets: SecretsStore
    @EnvironmentObject var upload: UploadModel

    var body: some View {
        NavigationView {
            Form {
                Section(header: Text("Connection")) {
                    TextField("Endpoint (wss://…/ws)", text: $secrets.endpoint)
                        .autocapitalization(.none)
                    SecureField("Password", text: $secrets.password)
                    Text("Device ID: \(secrets.deviceId)")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }

                Section(header: Text("Sync Options")) {
                    Toggle("Wi-Fi only", isOn: $secrets.wifiOnly)
                    Toggle("Include videos", isOn: $secrets.includeVideos)
                    Toggle("Sync from iCloud", isOn: $secrets.downloadFromiCloud)
                    Button("Authorize Photos Access") {
                        Task { _ = await PHPhotoLibrary.requestAuthorization(for: .readWrite) }
                    }
                }

                Section {
                    Button("Sync Now") {
                        Task {
                            secrets.persist()
                            try? await PhotoSync.shared.runForeground()
                        }
                    }
                    Button("Sync All") {
                        Task {
                            secrets.persist()
                            UserDefaults.standard.set(Date(), forKey: "lastSyncDate")
                            try? await PhotoSync.shared.runForeground()
                        }
                    }
                }
            }
            .navigationTitle("Settings")
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button("Done") {
                        secrets.persist()
                        dismiss()
                    }
                }
            }
        }
    }
}
