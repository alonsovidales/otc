// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  DeviceNeedsUpdate.swift
//  OffTheCloud
//
//  A feature the signed-in device is too old for: it answered the request
//  with unknown_payload (ErrorCodes.isUnknownPayload). The phones reach
//  devices that haven't installed the release the feature came with yet,
//  so this says so - titled with what needed the update - and offers the
//  way out: the update itself for the account that set the device up (as
//  Settings > Device version's button, UpdateViewModel.apply), whom to ask
//  for everyone else (a family member's instance can't update the device).
//  Present it with .deviceNeedsUpdate($ask) on a view outside any List or
//  Form section, so a re-layout can't dismiss it.
//

import SwiftUI

struct DeviceNeedsUpdate: Identifiable {
    typealias Send = @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope

    /// Through OTCConnection, with the socket's own time.
    static let viaDevice: Send = { payload in
        try await OTCConnection.shared.request { $0.payload = payload }
    }

    let id = UUID()
    /// "Your device needs an update to …".
    let title: String
    /// The account that set the device up (GetInstanceRole's is_primary):
    /// it gets Update, everyone else whom to ask.
    let canUpdate: Bool
    /// What starts the update (tests answer in the device's place).
    let send: Send

    /// Asks the device whether this account may update it; not when the
    /// answer doesn't come.
    @MainActor
    static func asking(title: String, via send: @escaping Send = viaDevice) async -> DeviceNeedsUpdate {
        var primary = false
        if let resp = try? await send(.reqGetInstanceRole(Msg_ReqGetInstanceRole())),
           case .respInstanceRole(let role) = resp.payload {
            primary = role.isPrimary
        }
        return DeviceNeedsUpdate(title: title, canUpdate: primary, send: send)
    }

    /// Starts the update: what to say then - that it is updating, or the
    /// device's own words for why it didn't start.
    @MainActor
    func startUpdate() async -> String {
        guard let resp = try? await send(.reqApplyUpdate(Msg_ReqApplyUpdate())) else {
            return S.appSettingsUpdateStartFailed
        }
        if case .respAck(let ack) = resp.payload, ack.ok { return S.appShellDeviceUpdateStarted }
        return resp.error && !resp.errorMessage.isEmpty ? resp.errorMessage : S.appSettingsUpdateStartFailed
    }
}

extension View {
    /// The "needs an update" alert while `ask` is set, then what starting
    /// the update came to.
    func deviceNeedsUpdate(_ ask: Binding<DeviceNeedsUpdate?>) -> some View {
        modifier(DeviceNeedsUpdateAlert(ask: ask))
    }
}

private struct DeviceNeedsUpdateAlert: ViewModifier {
    @Binding var ask: DeviceNeedsUpdate?
    /// Said once Update was tapped: updating, or why not.
    @State private var outcome: String?

    func body(content: Content) -> some View {
        content
            .alert(ask?.title ?? "", isPresented: Binding(
                get: { ask != nil },
                set: { if !$0 { ask = nil } }
            ), presenting: ask) { a in
                if a.canUpdate {
                    Button(S.commonUpdate) {
                        Task {
                            let said = await a.startUpdate()
                            // At home the device answers in milliseconds,
                            // while this alert is still going away - and an
                            // alert presented then can be dropped unseen.
                            try? await Task.sleep(nanoseconds: 700_000_000)
                            outcome = said
                        }
                    }
                    Button(S.commonCancel, role: .cancel) {}
                } else {
                    Button(S.commonOk, role: .cancel) {}
                }
            } message: { a in
                if !a.canUpdate { Text(S.appShellDeviceUpdateAskOwner) }
            }
            .alert(outcome ?? "", isPresented: Binding(
                get: { outcome != nil },
                set: { if !$0 { outcome = nil } }
            )) {
                Button(S.commonOk, role: .cancel) {}
            }
    }
}
