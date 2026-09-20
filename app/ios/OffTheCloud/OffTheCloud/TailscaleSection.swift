// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  TailscaleSection.swift
//  OffTheCloud
//
//  Issue #80: reaching this device over Tailscale Funnel instead of the
//  bridge.
//
//  In Settings rather than the setup wizard, deliberately: this is a
//  decision someone makes once the device is already working, and the one
//  place it could have gone in the wizard was after the WiFi step - which
//  drops the very connection the page is on.
//
//  Primary-only, like the update and users sections. Funnel publishes the
//  whole machine, which is not an additional user's to turn on, and is
//  also why it can only serve one user: one machine, one public name.
//
//  Mirrors web/src/components/TailscalePanel.tsx.

import SwiftUI
import UIKit

@MainActor
final class TailscaleViewModel: ObservableObject {
    @Published var isPrimary = false
    @Published var installed = false
    @Published var loggedIn = false
    @Published var funnelOn = false
    @Published var publicURL = ""
    @Published var loginURL = ""
    @Published var authKey = ""
    @Published var busy = false
    @Published var note: String?
    @Published var error: String?

    private let ws = OTCConnection.shared

    func load() async {
        do {
            let resp = try await ws.request { e in
                e.payload = .reqGetInstanceRole(Msg_ReqGetInstanceRole())
            }
            if case .respInstanceRole(let role) = resp.payload { isPrimary = role.isPrimary }
        } catch {
            isPrimary = false
        }
        guard isPrimary else { return }
        await refresh()
    }

    func refresh() async {
        do {
            let resp = try await ws.request { e in
                e.payload = .reqGetTailscaleStatus(Msg_ReqGetTailscaleStatus())
            }
            apply(resp)
        } catch {
            self.error = error.localizedDescription
        }
    }

    func configure(enable: Bool) async {
        busy = true
        note = nil
        error = nil
        defer { busy = false }
        do {
            let resp = try await ws.request { [self] e in
                var m = Msg_ReqSetupTailscale()
                m.authKey = authKey.trimmingCharacters(in: .whitespacesAndNewlines)
                m.enable = enable
                e.payload = .reqSetupTailscale(m)
            }
            guard apply(resp) else {
                error = resp.error ? resp.errorMessage : "Could not change the setting."
                return
            }
            if !enable {
                note = "Funnel is off. This device is reachable through the bridge again."
            } else if !installed {
                error = "Tailscale isn't installed on this device, so Funnel can't be turned on here."
            } else if !loginURL.isEmpty {
                note = "Authorise this device in the page that just opened, then press Enable again."
            } else if funnelOn {
                note = "Done — this device is reachable at \(publicURL)"
            } else {
                note = "Tailscale is connected, but Funnel didn't come up. Check that Funnel is enabled for your tailnet."
            }
        } catch {
            self.error = error.localizedDescription
        }
    }

    @discardableResult
    private func apply(_ resp: Msg_RespEnvelope) -> Bool {
        guard case .respTailscaleStatus(let s) = resp.payload else { return false }
        installed = s.installed
        loggedIn = s.loggedIn
        funnelOn = s.funnelOn
        publicURL = s.publicURL
        loginURL = s.loginURL
        if !s.error.isEmpty { error = s.error }

        return true
    }
}

struct TailscaleSection: View {
    @StateObject private var vm = TailscaleViewModel()
    @Environment(\.openURL) private var openURL

    var body: some View {
        if vm.isPrimary {
            Section(header: Text("Tailscale Funnel")) {
                if vm.funnelOn {
                    Text("Served over Tailscale Funnel at \(vm.publicURL)")
                        .font(.caption).foregroundStyle(.secondary)
                }

                if vm.installed {
                    Text("Tailscale Funnel is an alternative to the Off The Cloud bridge, it publishes this device on your own ts.net address but with the next limitations:")
                        .font(.caption).foregroundStyle(.secondary)
                    limitation("The social side won't work.", "Friends find each other through the bridge, so sharing and friends' timelines are unavailable.")
                    limitation("One user only.", "Extra users need a web address each, and Funnel gives this machine a single one.")
                    limitation("Bandwidth is capped.", "Tailscale limits what can pass through Funnel and doesn't publish the limit.")
                    limitation("You'll need a Tailscale account,", "with Funnel enabled for your tailnet.")
                    Text("It suits a device you want purely as your own private NAS — your files and photos, reachable from anywhere, nothing shared with anyone.")
                        .font(.caption).foregroundStyle(.secondary)

                    if !vm.funnelOn {
                        TextField("Auth key (optional)", text: $vm.authKey)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                    }

                    Button(vm.busy ? "Working…" : (vm.funnelOn ? "Turn Funnel off" : "Enable Tailscale Funnel")) {
                        Task {
                            await vm.configure(enable: !vm.funnelOn)
                            // Take them straight to the authorisation page
                            // rather than leaving a link to notice.
                            if let url = URL(string: vm.loginURL), !vm.loginURL.isEmpty {
                                openURL(url)
                            }
                        }
                    }
                    .disabled(vm.busy)

                    if !vm.funnelOn {
                        Text("With a key from your Tailscale admin console this finishes on its own. Without one, you'll be taken to Tailscale to authorise this device.")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                } else {
                    Text("Tailscale isn't installed on this device, so Funnel isn't available here.")
                        .font(.caption).foregroundStyle(.secondary)
                }

                if let note = vm.note {
                    Text(note).font(.caption).foregroundStyle(.secondary)
                }
                if let error = vm.error {
                    Text(error).font(.caption).foregroundStyle(.red)
                }
            }
            .task { await vm.load() }
            // Authorising happens in Safari, so coming back to the app is
            // the moment the device has something new to say.
            .onReceive(NotificationCenter.default.publisher(
                for: UIApplication.willEnterForegroundNotification
            )) { _ in
                Task { await vm.refresh() }
            }
        } else {
            Color.clear.frame(height: 0).task { await vm.load() }
        }
    }

    @ViewBuilder
    private func limitation(_ lead: String, _ rest: String) -> some View {
        HStack(alignment: .top, spacing: 4) {
            Text("•")
            Text("\(Text(lead).bold()) \(rest)")
        }
        .font(.caption)
        .foregroundStyle(.secondary)
    }
}
