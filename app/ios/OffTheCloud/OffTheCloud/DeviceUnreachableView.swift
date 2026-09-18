// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  DeviceUnreachableView.swift
//  OffTheCloud
//
//  Issue #56: a domain that's registered on the bridge but whose device has
//  no live connection used to surface as nothing at all here - the app just
//  sat there empty, retrying in the background, with OTCConnection.lastError
//  set but displayed nowhere. This is the counterpart to the web app's own
//  DeviceUnreachable.tsx and to the bridge's unavailable.html (issue #97),
//  deliberately sharing their wording and their unplugged-cloud icon so the
//  same situation looks the same wherever someone meets it.
//

import SwiftUI

struct DeviceUnreachableView: View {
    /// The bridge's own wording (Ack.error_msg), shown as-is.
    let message: String
    /// Ack.code - "account_disabled" gets its own icon and title, since
    /// that one is permanent until the owner acts, not a "try again later".
    let code: String

    private var disabled: Bool { code == "account_disabled" }

    var body: some View {
        VStack(spacing: 16) {
            icon
                .frame(width: 56, height: 56)
                .foregroundStyle(Color.orange)

            Text(disabled ? "This account has been disabled" : "This device isn't available right now")
                .font(.headline)
                .multilineTextAlignment(.center)

            if !message.isEmpty {
                Text(message)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }

            if !disabled {
                // No retry button on purpose: OTCConnection already
                // reconnects on its own backoff (see handleDisconnect), and
                // this view disappears by itself the moment that succeeds -
                // a button would just be a second, slower path to what is
                // already happening.
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("Still trying…").font(.footnote).foregroundStyle(.secondary)
                }
                .padding(.top, 4)
            }
        }
        .padding(28)
        .frame(maxWidth: 360)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(.background.opacity(0.92))
    }

    @ViewBuilder
    private var icon: some View {
        if disabled {
            Image(systemName: "nosign")
                .resizable()
                .scaledToFit()
        } else {
            // The unplugged cloud the other two surfaces use.
            Image(systemName: "icloud.slash")
                .resizable()
                .scaledToFit()
        }
    }
}
