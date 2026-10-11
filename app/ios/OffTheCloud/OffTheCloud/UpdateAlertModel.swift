// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  UpdateAlertModel.swift
//  OffTheCloud
//
//  Issue #183: a critical device update has to be seen from anywhere in
//  the app, not just by someone who happens to open Settings. The
//  device's status carries `update_alert` while a major or critical
//  release is not installed; this asks for it at launch and every five
//  minutes while the app is active (the same hand-rolled Task loop as
//  NotificationsModel - no Timer), and MainView draws the banner. Only
//  "critical" gets one; a major update is left to Settings.
//

import SwiftUI

@MainActor
final class UpdateAlertModel: ObservableObject {
    static let shared = UpdateAlertModel()

    /// The device's alert while it is critical, nil otherwise.
    @Published var critical: Msg_UpdateAlert?

    private let ws = OTCConnection.shared
    private var pollTask: Task<Void, Never>?

    func startPolling() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                await self.fetch()
                try? await Task.sleep(nanoseconds: 300_000_000_000)
            }
        }
    }

    func stopPolling() {
        pollTask?.cancel()
        pollTask = nil
    }

    /// Log Out: the next device's alerts are its own.
    func reset() {
        stopPolling()
        critical = nil
    }

    private func fetch() async {
        // A failed request keeps whatever was known: a dropped connection
        // says nothing about whether the update got installed.
        guard let resp = try? await ws.request({ $0.payload = .reqGetStatus(Msg_GetStatus()) }),
              case .respStatus(let s) = resp.payload else { return }
        critical = s.hasUpdateAlert && s.updateAlert.level == "critical" ? s.updateAlert : nil
        // The language chosen in another app reaches this one through the
        // same poll - also right after coming back to the front.
        LanguageSettings.shared.deviceSaid(status: s)
    }
}

/// Issue #183: the banner over every tab while a critical update waits.
struct CriticalUpdateBanner: View {
    let alert: Msg_UpdateAlert
    let onUpdate: () -> Void

    var body: some View {
        HStack(alignment: .center, spacing: 10) {
            Image(systemName: "exclamationmark.octagon.fill")
                .font(.title3)
            Text(message)
                .font(.footnote)
                .frame(maxWidth: .infinity, alignment: .leading)
                .fixedSize(horizontal: false, vertical: true)
            Button("Update", action: onUpdate)
                .buttonStyle(.bordered)
                .tint(.white)
                .controlSize(.small)
        }
        .foregroundStyle(.white)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(Color.red)
    }

    private var message: String {
        let summary = alert.summary.isEmpty ? "" : " \(alert.summary)"
        return "Critical update \(alert.version) available.\(summary) Install it as soon as possible."
    }
}
