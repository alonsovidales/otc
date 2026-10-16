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
    /// Try the demo (DemoDevice.swift): asking the demo device for its
    /// password, and why it didn't sign in.
    @State private var tryingDemo = false
    @State private var demoFailure: DemoDevice.Failure?

    var body: some View {
        NavigationStack {
            Form {
                Section(header: Text(S.appShellConnectTitle)) {
                    // Issue #121: the device's name is all the bridge
                    // needs; a custom address is behind the toggle.
                    ConnectionEndpointFields(endpoint: $endpoint)
                    SecureField(S.commonPassword, text: $password)
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
                    Button(S.appShellSaveContinue) {
                        secrets.endpoint = endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
                        secrets.password = password
                        secrets.persist()
                    }
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
                            secrets.endpoint = newEndpoint
                            secrets.password = newPassword
                            secrets.persist()
                        }
                    } label: {
                        Label(S.appShellSetupNewDevice, systemImage: "antenna.radiowaves.left.and.right")
                    }
                }
                // The public demo device, for someone with no device yet:
                // after the ways to connect one's own.
                Section {
                    Text(S.appShellDemoTryNote)
                        .font(.footnote).foregroundStyle(.secondary)
                    Button(action: tryDemo) {
                        HStack(spacing: 8) {
                            if tryingDemo { ProgressView().controlSize(.small) }
                            Text(tryingDemo ? S.commonConnecting : S.appShellDemoTry)
                        }
                    }
                    .disabled(tryingDemo)
                    if let failure = demoFailure {
                        let detail = failure.detail
                        VStack(alignment: .leading, spacing: 2) {
                            Text(S.appShellCantConnect)
                                .foregroundStyle(.red)
                            if let detail, !detail.isEmpty {
                                Text(detail).foregroundStyle(.secondary)
                            }
                        }
                        .font(.footnote)
                        .accessibilityElement(children: .combine)
                        .onAppear { Announce.polite([S.appShellCantConnect, detail ?? ""].joined(separator: " ")) }
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

    /// Try the demo: the demo device's address as its name builds it, the
    /// password it publishes, then the usual sign-in - saved as a typed
    /// device is, so the app comes back to it at its next launch. Never the
    /// setup of a new device. Known as the demo before the first sign-in,
    /// so the photo sync the tabs start asks nothing of Photos meanwhile.
    private func tryDemo() {
        tryingDemo = true
        demoFailure = nil
        Task {
            defer { tryingDemo = false }
            do {
                let published = try await OTCConnection.shared.demoPassword()
                OTCConnection.shared.deviceSaid(demo: true)
                secrets.endpoint = DemoDevice.endpoint
                secrets.password = published
                secrets.persist()
            } catch let failure as DemoDevice.Failure {
                demoFailure = failure
            } catch {
                demoFailure = .unreachable(.failure(error))
            }
        }
    }
}
