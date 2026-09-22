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
            }
            .navigationTitle("Welcome")
        }
        .onAppear {
            endpoint = secrets.endpoint
            password = secrets.password
        }
    }
}
