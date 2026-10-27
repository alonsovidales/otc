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
    @FocusState private var passwordFocused: Bool

    var body: some View {
        NavigationStack {
            Form {
                Section(header: Text(S.appShellConnectTitle)) {
                    // Issue #121: the device's name is all the bridge
                    // needs; a custom address is behind the toggle.
                    ConnectionEndpointFields(endpoint: $endpoint, onSubmit: { passwordFocused = true })
                    PasswordField(title: S.commonPassword, text: $password, onSubmit: saveAndContinue, focus: $passwordFocused)
                    if lastNote {
                        Text(S.appShellOnboardingLastDevice)
                            .font(.footnote).foregroundStyle(.secondary)
                    }
                    if pendingNote {
                        Text(S.appShellOnboardingPending)
                            .font(.footnote).foregroundStyle(.secondary)
                    }
                }
                Section {
                    Button(S.appShellSaveContinue, action: saveAndContinue)
                        .disabled(endpoint.isEmpty || password.isEmpty)
                }
                // Issue #137: a brand-new device is set up from here over
                // Bluetooth; when it is done its address lands in the form
                // above and the first sign in sets the owner password.
                Section(footer: Text(S.appShellOnboardingSetupFooter)) {
                    NavigationLink {
                        BluetoothSetupView { newEndpoint, newPassword in
                            // Straight in: the root view switches to the
                            // app as soon as both are saved.
                            endpoint = newEndpoint
                            password = newPassword
                            SyncChoice.ask()
                            secrets.saveCredentials(endpoint: newEndpoint, password: newPassword)
                            OTCConnection.shared.invalidate()
                        }
                    } label: {
                        Label(S.appShellSetupNewDevice, systemImage: "antenna.radiowaves.left.and.right")
                    }
                }
                // Issue #175: what the bridge keeps, before signing in.
                Section {
                    Link(S.appShellPrivacy, destination: URL(string: "https://off-the.cloud/privacy")!)
                }
            }
            .navigationTitle(S.appShellWelcome)
            // The language, before any device: Settings, where it lives,
            // needs one. Kept for the device signed in to next.
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) { LanguageMenu() }
            }
        }
        .onAppear(perform: fill)
    }

    /// Save & Continue, and Go on the password's keyboard.
    private func saveAndContinue() {
        let address = endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !address.isEmpty, !password.isEmpty else { return }
        // Asked first, so no sync starts before it (SyncChoice).
        SyncChoice.ask()
        secrets.saveCredentials(endpoint: address, password: password)
        // New credentials (IOS-03): a socket still signed in to the device
        // logged out of - a request that went on during Log Out - or to the
        // last ones must not carry over, as on Android.
        OTCConnection.shared.invalidate()
    }

    /// The form, from what is saved, else a device set up from this phone
    /// or the one it left last.
    private func fill() {
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
