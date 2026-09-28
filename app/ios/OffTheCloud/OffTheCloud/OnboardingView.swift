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

    var body: some View {
        NavigationStack {
            Form {
                Section(header: Text("Connect to Off The Cloud")) {
                    // Issue #121: the device's name is all the bridge
                    // needs; a custom address is behind the toggle.
                    ConnectionEndpointFields(endpoint: $endpoint)
                    SecureField("Password", text: $password)
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
            }
            .navigationTitle("Welcome")
        }
        .onAppear {
            endpoint = secrets.endpoint
            password = secrets.password
        }
    }
}
