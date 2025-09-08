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
        NavigationView {
            Form {
                Section(header: Text("Connect to Off The Cloud")) {
                    TextField("WebSocket endpoint (wss://host/ws)", text: $endpoint)
                        .autocapitalization(.none)
                        .textContentType(.URL)
                        .keyboardType(.URL)
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
