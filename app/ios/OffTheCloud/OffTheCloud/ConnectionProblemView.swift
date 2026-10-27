// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  ConnectionProblemView.swift
//  OffTheCloud
//
//  Shown over the tabs when the app can't connect or sign in, whatever
//  the reason. Every tab's content comes from the device, so behind this
//  there is nothing but empty screens - which is exactly what used to be
//  shown, with no error and no way to correct the address or password
//  that caused it. Here the reason is stated and the connection settings
//  are right underneath, since a wrong one of those is the usual cause.
//

import SwiftUI

struct ConnectionProblemView: View {
    @EnvironmentObject var secrets: SecretsStore
    /// Leaves this device for the connection screen - how a phone moves on
    /// to set up a new device, or another one, when this one can't be
    /// reached or signed in to (a device being re-imaged, say). Given what
    /// the form holds, for the connection screen to fill in.
    var onLeave: ((_ endpoint: String, _ password: String) -> Void)? = nil

    var body: some View {
        ConnectionProblemCard(secrets: secrets, onLeave: onLeave)
    }
}

/// The card itself. The fields edit drafts, seeded from what is saved, until
/// Save & Retry (IOS-03, as Android's): every reconnect - and the pollers
/// keep retrying while this shows - dials what is saved, so a half-typed
/// device name would have handed the password to whichever device has that
/// name, and a field emptied sent the app back to Welcome.
private struct ConnectionProblemCard: View {
    @ObservedObject var secrets: SecretsStore
    @ObservedObject private var connection = OTCConnection.shared
    var onLeave: ((_ endpoint: String, _ password: String) -> Void)?
    @State private var endpoint: String
    @State private var password: String
    @State private var retrying = false
    @State private var confirmLeave = false
    @FocusState private var passwordFocused: Bool

    init(secrets: SecretsStore, onLeave: ((_ endpoint: String, _ password: String) -> Void)?) {
        self.secrets = secrets
        self.onLeave = onLeave
        _endpoint = State(initialValue: secrets.endpoint)
        _password = State(initialValue: secrets.password)
    }

    var body: some View {
        ScrollView {
            VStack(spacing: 18) {
                Image(systemName: "wifi.exclamationmark")
                    .resizable().scaledToFit()
                    .frame(width: 48, height: 48)
                    .foregroundStyle(Color.orange)

                Text(S.appShellCantConnect)
                    .font(.headline)
                    .multilineTextAlignment(.center)

                if let error = connection.lastError, !error.isEmpty {
                    Text(error)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }

                VStack(alignment: .leading, spacing: 10) {
                    Text(S.appShellConnectionHeading).font(.caption).foregroundStyle(.secondary)
                    ConnectionEndpointFields(endpoint: $endpoint, onSubmit: { passwordFocused = true })
                        .textFieldStyle(.roundedBorder)
                    PasswordField(title: S.commonPassword, text: $password, onSubmit: saveAndRetry, focus: $passwordFocused)
                        .textFieldStyle(.roundedBorder)
                }
                .padding(14)
                .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 10))

                Button(action: saveAndRetry) {
                    HStack(spacing: 8) {
                        if retrying { ProgressView().controlSize(.small) }
                        Text(retrying ? S.commonConnecting : S.appShellSaveRetry)
                    }
                    .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .disabled(retrying || !canSave)

                if onLeave != nil {
                    Button(S.appShellLeaveButton) { confirmLeave = true }
                        .font(.subheadline)
                }

                // The language, while this covers the app and Settings with
                // it: shown at once, and sent at the next sign-in.
                LanguageMenu()
                    .font(.subheadline)
            }
            .padding(24)
            .frame(maxWidth: 420)
            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
            .padding(24)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(.background.opacity(0.92))
        .alert(S.appShellLeaveTitle, isPresented: $confirmLeave) {
            Button(S.appShellLeave, role: .destructive) { onLeave?(trimmedEndpoint, password) }
            Button(S.commonCancel, role: .cancel) {}
        } message: {
            Text(S.appShellLeaveMessageStart)
        }
    }

    private var trimmedEndpoint: String { endpoint.trimmingCharacters(in: .whitespacesAndNewlines) }

    private var canSave: Bool { !trimmedEndpoint.isEmpty && !password.isEmpty }

    /// Save & Retry, and Go on the password's keyboard.
    private func saveAndRetry() {
        guard canSave, !retrying else { return }
        retrying = true
        secrets.saveCredentials(endpoint: trimmedEndpoint, password: password)
        OTCConnection.shared.invalidate()
        Task {
            // The failure state clears itself on success; on
            // another failure the new reason replaces this one.
            _ = try? await OTCConnection.shared.ensureConnected()
            retrying = false
        }
    }
}
