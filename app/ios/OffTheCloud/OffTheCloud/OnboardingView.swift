// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  OnboardingView.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//


import SwiftUI

struct OnboardingView: View {
    @EnvironmentObject var secrets: SecretsStore
    @State private var endpoint = ""
    @State private var password = ""
    /// Set when the form was filled from a device set up from this phone.
    @State private var pendingNote = false
    @State private var lastNote = false

    var body: some View {
        NavigationStack {
            Form {
                Section(header: Text("Connect to Off The Cloud")) {
                    // Issue #121: the device's name is all the bridge
                    // needs; a custom address is behind the toggle.
                    ConnectionEndpointFields(endpoint: $endpoint)
                    SecureField("Password", text: $password)
                    if lastNote {
                        Text("Filled in from the device you left. Tap Save & Continue to connect to it again, or change it to use another one.")
                            .font(.footnote).foregroundStyle(.secondary)
                    }
                    if pendingNote {
                        Text("Filled in from the device you set up with this phone. Once its install has finished (about 20 minutes), tap Save & Continue.")
                            .font(.footnote).foregroundStyle(.secondary)
                    }
                }
                Section {
                    Button("Save & Continue") {
                        secrets.endpoint = endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
                        secrets.password = password
                        secrets.persist()
                    }
                    .disabled(endpoint.isEmpty || password.isEmpty)
                }
                // Issue #137: a brand-new device is set up from here over
                // Bluetooth; when it is done its address lands in the form
                // above and the first sign in sets the owner password.
                Section(footer: Text("For a device fresh out of the box: it runs the setup over Bluetooth, and this app becomes its app. Log out first if you are switching from another device.")) {
                    NavigationLink {
                        BluetoothSetupView { newEndpoint, newPassword in
                            // Straight in: the root view switches to the
                            // app as soon as both are saved.
                            endpoint = newEndpoint
                            password = newPassword
                            secrets.endpoint = newEndpoint
                            secrets.password = newPassword
                            secrets.persist()
                        }
                    } label: {
                        Label("Set up a new device", systemImage: "antenna.radiowaves.left.and.right")
                    }
                }
                // Issue #175: what the bridge keeps, before signing in.
                Section {
                    Link("Privacy", destination: URL(string: "https://off-the.cloud/privacy")!)
                }
            }
            .navigationTitle("Welcome")
        }
        .onAppear {
            endpoint = secrets.endpoint
            password = secrets.password
            if endpoint.isEmpty, password.isEmpty, let p = SecretsStore.pendingSetup() {
                endpoint = p.endpoint
                password = p.password
                pendingNote = true
            } else if endpoint.isEmpty, password.isEmpty, let l = SecretsStore.lastDevice() {
                endpoint = l.endpoint
                password = l.password
                lastNote = true
            }
        }
    }
}
