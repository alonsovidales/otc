// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  ConnectionEndpointFields.swift
//  OffTheCloud
//
//  Issue #121: how a device's address is entered, shared by the first-run
//  screen and Settings so the two can't drift.
//
//  A device on the public bridge is reachable at wss://<name>.off-the.cloud
//  /ws, and nothing about that is the owner's to decide but the name - so
//  the name is all the form asks for. A device reached any other way (own
//  bridge, Tailscale Funnel, the LAN) needs its address typed, behind a
//  toggle, and an existing custom address opens the form in that mode so
//  it never silently rewrites what someone set up on purpose.
//

import SwiftUI

struct ConnectionEndpointFields: View {
    /// The endpoint as stored - always the full address, whichever way it
    /// was entered.
    @Binding var endpoint: String

    @State private var deviceName = ""
    @State private var customAddress = false
    @State private var loaded = false

    var body: some View {
        Group {
            if customAddress {
                TextField("Address (wss://host/ws)", text: $endpoint)
                    .autocapitalization(.none)
                    .disableAutocorrection(true)
                    .textContentType(.URL)
                    .keyboardType(.URL)
            } else {
                HStack(spacing: 0) {
                    TextField("Device name", text: $deviceName)
                        .autocapitalization(.none)
                        .disableAutocorrection(true)
                        .keyboardType(.asciiCapable)
                        .onChange(of: deviceName) { _, name in
                            endpoint = name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                                ? "" : SecretsStore.bridgeEndpoint(forName: name)
                        }
                    Text(".\(SecretsStore.bridgeDomain)")
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .fixedSize()
                }
            }
            Toggle("Custom address", isOn: $customAddress)
                .onChange(of: customAddress) { _, custom in
                    // Switching to the name keeps whatever name the
                    // address had; switching to custom keeps the address.
                    if !custom {
                        deviceName = SecretsStore.bridgeName(fromEndpoint: endpoint) ?? ""
                        endpoint = deviceName.isEmpty ? "" : SecretsStore.bridgeEndpoint(forName: deviceName)
                    }
                }
            if customAddress {
                Text("For a device not on the Off The Cloud bridge - your own bridge, Tailscale Funnel, or the local network (ws://192.168.…:8080/ws).")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .onAppear {
            guard !loaded else { return }
            loaded = true
            if let name = SecretsStore.bridgeName(fromEndpoint: endpoint) {
                deviceName = name
            } else if !endpoint.isEmpty {
                customAddress = true
            }
        }
    }
}
