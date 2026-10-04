// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  BridgeAccountSection.swift
//  OffTheCloud
//
//  Issue #182: the bridge and the Off The Cloud account, from Settings.
//  "Leave the Bridge" gives the device's name back and the device restarts
//  local-only (http://otc.local:8080), as if set up without an account;
//  the app then talks to it at that address. "Delete My Account" does the
//  same, then opens the account page, where the account itself is deleted
//  (it needs the account's own sign-in, which the app doesn't hold).
//
//  Primary-only, like the Tailscale section. Mirrors the web app's
//  BridgePanel and Android's BridgeAccountSection.

import SwiftUI

/// The address a local-only device answers at when it didn't say its own:
/// .local names don't resolve on every network.
let localDeviceEndpoint = "ws://otc.local:8080/ws"

/// The endpoint for a device's home-network address ("192.168.1.20:8080").
func localEndpoint(_ address: String) -> String {
    address.isEmpty ? localDeviceEndpoint : "ws://\(address)/ws"
}

@MainActor
final class BridgeAccountViewModel: ObservableObject {
    @Published var visible = false
    @Published var enabled = false
    @Published var domain = ""
    @Published var bridge = "off-the.cloud"
    @Published var pending = false
    @Published var leftReason = ""
    /// The device's address at home, e.g. "192.168.1.20:8080".
    @Published var localAddress = ""
    @Published var busy = false
    @Published var note: String?
    @Published var error: String?

    private let ws = OTCConnection.shared

    func load() async {
        do {
            let resp = try await ws.request { $0.payload = .reqGetBridgeAccess(Msg_ReqGetBridgeAccess()) }
            // An additional user's instance answers with an error: not theirs.
            guard case .respBridgeAccess(let a) = resp.payload else { visible = false; return }
            apply(a)
            visible = true
        } catch {
            visible = false
        }
    }

    private func apply(_ a: Msg_RespBridgeAccess) {
        enabled = a.enabled
        domain = a.domain
        bridge = a.bridge.isEmpty ? "off-the.cloud" : a.bridge
        pending = a.pending
        leftReason = a.leftReason
        if !a.localAddress.isEmpty { localAddress = a.localAddress }
        if !a.error.isEmpty { error = a.error }
    }

    /// Takes the device off the bridge and points the app at its local
    /// address. True when the device accepted.
    func leave(secrets: SecretsStore) async -> Bool {
        busy = true
        note = nil
        error = nil
        defer { busy = false }
        do {
            let resp = try await ws.request { $0.payload = .reqDisableBridge(Msg_ReqDisableBridge()) }
            guard case .respBridgeAccess(let a) = resp.payload else {
                error = resp.errorMessage.isEmpty ? "Could not leave the bridge." : resp.errorMessage
                return false
            }
            apply(a)
            secrets.endpoint = localEndpoint(localAddress)
            secrets.persist()
            OTCConnection.shared.invalidate()
            note = "The device is restarting off the bridge. From now on the app reaches it on your home network, at \(localAddress.isEmpty ? "otc.local" : localAddress)."
            return true
        } catch {
            self.error = error.localizedDescription
            return false
        }
    }
}

struct BridgeAccountSection: View {
    @EnvironmentObject var secrets: SecretsStore
    @StateObject private var vm = BridgeAccountViewModel()
    @Environment(\.openURL) private var openURL
    @State private var confirmLeave = false
    @State private var confirmDelete = false

    var body: some View {
        Group {
            if vm.visible {
                Section(
                    header: Text("Bridge and Account"),
                    footer: Text(footer)
                ) {
                    if vm.enabled {
                        Text("Reachable from anywhere at \(vm.domain)")
                            .font(.caption).foregroundStyle(.secondary)
                        if !vm.localAddress.isEmpty {
                            Text("At home: \(vm.localAddress)").font(.caption).foregroundStyle(.secondary)
                        }
                        Button("Leave the Bridge", role: .destructive) { confirmLeave = true }
                            .disabled(vm.busy)
                        Button("Delete My Account…", role: .destructive) { confirmDelete = true }
                            .disabled(vm.busy)
                    } else {
                        if vm.leftReason == "released" {
                            Text("This device left the bridge: \(vm.bridge) no longer knew its name (its account was deleted, or the name released). Everything on it is still there.")
                                .font(.caption).foregroundStyle(.orange)
                        }
                        Text("Only reachable on your home network. Join the bridge from Settings in the device's web app.")
                            .font(.caption).foregroundStyle(.secondary)
                        Button("Your Account Page") { open("account") }
                    }
                    Button("Privacy") { open("privacy") }
                    if let n = vm.note { Text(n).font(.caption).foregroundStyle(.secondary) }
                    if let e = vm.error { Text(e).font(.caption).foregroundStyle(.red) }
                }
                .confirmationDialog("Leave the bridge?", isPresented: $confirmLeave, titleVisibility: .visible) {
                    Button("Leave the Bridge", role: .destructive) { Task { _ = await vm.leave(secrets: secrets) } }
                } message: {
                    Text("\(vm.domain) is given back and the device restarts. It keeps everything and works at home, but is no longer reachable from outside or by your friends. You can join again later.")
                }
                .confirmationDialog("Delete your account?", isPresented: $confirmDelete, titleVisibility: .visible) {
                    Button("Leave and Delete the Account", role: .destructive) {
                        Task {
                            if await vm.leave(secrets: secrets) { open("account?delete=1") }
                        }
                    }
                } message: {
                    Text("This device leaves the bridge first and goes on working at home. Then your account page opens: sign in there and delete the account, which releases any other devices' names too.")
                }
            } else {
                // Something to hang the load on while hidden: a .task on an
                // empty Group never runs, so the section never appeared.
                Color.clear.frame(height: 0)
            }
        }
        .task { await vm.load() }
    }

    private var footer: String {
        vm.enabled
            ? "Leaving gives the name back; the device keeps working at home, at http://\(vm.localAddress.isEmpty ? "otc.local:8080" : vm.localAddress)."
            : ""
    }

    private func open(_ path: String) {
        if let url = URL(string: "https://\(vm.bridge)/\(path)") { openURL(url) }
    }
}
