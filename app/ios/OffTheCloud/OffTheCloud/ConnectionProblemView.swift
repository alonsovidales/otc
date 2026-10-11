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
    @ObservedObject private var connection = OTCConnection.shared
    @State private var retrying = false
    @State private var confirmLeave = false
    /// Leaves this device for the connection screen - how a phone moves on
    /// to set up a new device, or another one, when this one can't be
    /// reached or signed in to (a device being re-imaged, say).
    var onLeave: (() -> Void)? = nil

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
                    ConnectionEndpointFields(endpoint: $secrets.endpoint)
                        .textFieldStyle(.roundedBorder)
                    SecureField(S.commonPassword, text: $secrets.password)
                        .textFieldStyle(.roundedBorder)
                }
                .padding(14)
                .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 10))

                Button {
                    retrying = true
                    secrets.persist()
                    OTCConnection.shared.invalidate()
                    Task {
                        // The failure state clears itself on success; on
                        // another failure the new reason replaces this one.
                        _ = try? await OTCConnection.shared.ensureConnected()
                        retrying = false
                    }
                } label: {
                    HStack(spacing: 8) {
                        if retrying { ProgressView().controlSize(.small) }
                        Text(retrying ? S.commonConnecting : S.appShellSaveRetry)
                    }
                    .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .disabled(retrying || !secrets.isConfigured)

                if onLeave != nil {
                    Button(S.appShellLeaveButton) { confirmLeave = true }
                        .font(.subheadline)
                }
            }
            .padding(24)
            .frame(maxWidth: 420)
            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
            .padding(24)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(.background.opacity(0.92))
        .alert(S.appShellLeaveTitle, isPresented: $confirmLeave) {
            Button(S.appShellLeave, role: .destructive) { onLeave?() }
            Button(S.commonCancel, role: .cancel) {}
        } message: {
            Text(S.appShellLeaveMessageStart)
        }
    }
}
