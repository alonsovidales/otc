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
    /// The device has said whether there is a section and, for its owner,
    /// what Funnel is doing: until then Settings shows its header and a
    /// spinner.
    @Published var answered = false
    @Published var installed = false
    @Published var loggedIn = false
    @Published var funnelOn = false
    @Published var publicURL = ""
    @Published var loginURL = ""
    @Published var authKey = ""
    @Published var busy = false
    @Published var note: SettingsText?
    @Published var error: SettingsText?

    private let ws = OTCConnection.shared

    func load() async {
        do {
            let resp = try await ws.request { e in
                e.payload = .reqGetInstanceRole(Msg_ReqGetInstanceRole())
            }
            if case .respInstanceRole(let role) = resp.payload { isPrimary = role.isPrimary }
        } catch {
            // Not known now: what is shown stays as it is.
            return
        }
        guard isPrimary else { answered = true; return }
        await refresh()
        answered = true
    }

    func refresh() async {
        do {
            let resp = try await ws.request { e in
                e.payload = .reqGetTailscaleStatus(Msg_ReqGetTailscaleStatus())
            }
            apply(resp)
        } catch {
            self.error = .error(error)
        }
    }

    func configure(enable: Bool) async {
        busy = true
        note = nil
        error = nil
        defer { busy = false }
        do {
            let key = enable ? authKey.trimmingCharacters(in: .whitespacesAndNewlines) : ""
            // The key sealed for this connection, as a password is
            // (PwCrypto, GetPubKey): the bridge relays this request, and the
            // key signs a machine in to the owner's tailnet. Never in the
            // clear: a device before release 143 ignores the sealed field and
            // takes this as no key at all (keyIgnored below).
            var sealed = Data()
            if !key.isEmpty {
                let pk = try await ws.request { $0.payload = .reqGetPubKey(Msg_GetPubKey()) }
                guard case .respPubKey(let pubKey) = pk.payload else {
                    error = .asIs(pk.errorMessage, or: { S.appShellNoPublicKey })
                    return
                }
                sealed = try PwCrypto.encryptPassword(key, pubKeyDER: pubKey.publicKey)
            }
            let resp = try await ws.request { e in
                var m = Msg_ReqSetupTailscale()
                m.authKeySealed = sealed
                m.enable = enable
                e.payload = .reqSetupTailscale(m)
            }
            guard apply(resp) else {
                error = resp.error ? .asIs(resp.errorMessage) : .say { S.appSettingsTailscaleChangeFailed }
                return
            }
            if !enable {
                note = .say { S.appSettingsTailscaleOff }
            } else if !installed {
                error = .say { S.appSettingsTailscaleNotInstalledOn }
            } else if !loginURL.isEmpty {
                // With a key the device signs in by itself; asking for the
                // sign-in page anyway means it read no key: one too old for
                // the sealed field.
                note = key.isEmpty ? .say { S.appSettingsTailscaleAuthorise } : .say { S.appSettingsTailscaleKeyIgnored }
            } else if funnelOn {
                let url = publicURL
                note = .say { S.appSettingsTailscaleDone(url: url) }
            } else {
                note = .say { S.appSettingsTailscaleNoFunnel }
            }
        } catch {
            self.error = .error(error)
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
        if !s.error.isEmpty { error = .asIs(s.error) }

        return true
    }
}

struct TailscaleSection: View {
    // Owned and asked for by SettingsView (loadDeviceSections).
    @ObservedObject var vm: TailscaleViewModel
    @Environment(\.openURL) private var openURL

    var body: some View {
        if !vm.answered {
            LoadingSection(title: Self.title)
        } else if vm.isPrimary {
            Section(header: Text(verbatim: Self.title)) {
                if vm.funnelOn {
                    Text(S.appSettingsTailscaleServed(url: vm.publicURL))
                        .font(.caption).foregroundStyle(.secondary)
                }

                if vm.installed {
                    Text(S.appSettingsTailscaleIntro)
                        .font(.caption).foregroundStyle(.secondary)
                    limitation(S.appSettingsTailscaleLimitSocial(b: Self.lead))
                    limitation(S.appSettingsTailscaleLimitOneUser(b: Self.lead))
                    limitation(S.appSettingsTailscaleLimitBandwidth(b: Self.lead))
                    limitation(S.appSettingsTailscaleLimitAccount(b: Self.lead))
                    Text(S.appSettingsTailscaleSuits)
                        .font(.caption).foregroundStyle(.secondary)

                    if !vm.funnelOn {
                        TextField(S.appSettingsTailscaleAuthKey, text: $vm.authKey)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                    }

                    Button(vm.busy ? S.appSettingsTailscaleWorking : (vm.funnelOn ? S.appSettingsTailscaleTurnOff : S.appSettingsTailscaleEnable)) {
                        Task {
                            await vm.configure(enable: !vm.funnelOn)
                            // Take them straight to the authorisation page
                            // rather than leaving a link to notice.
                            if let url = URL(string: vm.loginURL), !vm.loginURL.isEmpty {
                                openURL(url)
                            }
                        }
                    }
                    .buttonStyle(.bordered)
                    .disabled(vm.busy)

                    if !vm.funnelOn {
                        Text(S.appSettingsTailscaleKeyHint)
                            .font(.caption).foregroundStyle(.secondary)
                    }
                } else {
                    Text(S.appSettingsTailscaleNotInstalled)
                        .font(.caption).foregroundStyle(.secondary)
                }

                if let note = vm.note {
                    Text(note.text).font(.caption).foregroundStyle(.secondary)
                }
                if let error = vm.error {
                    Text(error.text).font(.caption).foregroundStyle(.red)
                }
            }
            // Authorising happens in Safari, so coming back to the app is
            // the moment the device has something new to say: SettingsView
            // asks again at each wake, the app back in front among them.
        }
    }

    /// A product's name, the same in every language.
    private static let title = "Tailscale Funnel"

    /// A limitation's lead (its <b> span), in bold.
    private static var lead: AttributeContainer {
        var c = AttributeContainer()
        c.inlinePresentationIntent = .stronglyEmphasized
        return c
    }

    @ViewBuilder
    private func limitation(_ text: AttributedString) -> some View {
        HStack(alignment: .top, spacing: 4) {
            Text(verbatim: "•")
            Text(text)
        }
        .font(.caption)
        .foregroundStyle(.secondary)
    }
}
