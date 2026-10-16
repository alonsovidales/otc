// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  DemoDevice.swift
//  OffTheCloud
//
//  The public demo device (docs/demo-device.md): a device anyone can sign
//  in to with the password it publishes, to look around Off The Cloud
//  before having one of their own. It says what it is before sign-in
//  (PubKey.demo and demo_password) and after (Status.demo), and refuses
//  every change with error_code "demo" in its own words.
//
//  OTCConnection.isDemo is the one place the app knows it is signed in to
//  one. MainView hands it to every screen as \.demoDevice: they leave out
//  what would change something there, the banner over the tabs says what
//  this is, and the photo sync never runs against it (PhotoSync.swift).
//  The connection screen's "Try the demo" signs in to it as a typed name
//  and password would (OnboardingView).
//

import SwiftUI

enum DemoDevice {
    /// Its name on the bridge: the address is built as a typed name's is
    /// (wss://demo.off-the.cloud/ws).
    static let name = "demo"
    static var endpoint: String { SecretsStore.bridgeEndpoint(forName: name) }

    /// The banner's "Get your own device".
    static let website = URL(string: "https://off-the.cloud")!

    /// Why Try the demo didn't sign in, worded when shown.
    enum Failure: Error {
        /// Nothing at the demo's address answered as a device: why, as
        /// the connection card says it.
        case unreachable(ConnectionProblem)
        /// A device answered that isn't a demo one (it publishes no
        /// password).
        case notDemo

        /// The reason under "Can't connect", when there is one to give.
        var detail: String? {
            if case .unreachable(let problem) = self { return problem.text }
            return nil
        }
    }

    /// GetPubKey's answer at the demo's address: the password a demo device
    /// publishes, or why there is none. An Ack is the bridge's - the device
    /// isn't connected to it, say - with its words, as at a sign-in.
    static func password(in resp: Msg_RespEnvelope) -> Result<String, Failure> {
        switch resp.payload {
        case .respPubKey(let key):
            guard key.demo, !key.demoPassword.isEmpty else { return .failure(.notDemo) }
            return .success(key.demoPassword)
        case .respAck(let ack) where !ack.errorMsg.isEmpty:
            return .failure(.unreachable(.said(ack.errorMsg)))
        default:
            return .failure(.unreachable(.noPublicKey))
        }
    }

    /// Kept in the app's defaults (Log Out wipes them), so a launch knows
    /// before it connects: the photo sync and the notification prompt at
    /// launch never ask anything for the demo device.
    static let storedKey = "demoDevice"

    static func stored(_ defaults: UserDefaults = .standard) -> Bool {
        defaults.bool(forKey: storedKey)
    }

    static func store(_ demo: Bool, _ defaults: UserDefaults = .standard) {
        if demo {
            defaults.set(true, forKey: storedKey)
        } else {
            defaults.removeObject(forKey: storedKey)
        }
    }
}

private struct DemoDeviceKey: EnvironmentKey {
    static let defaultValue = false
}

extension EnvironmentValues {
    /// Signed in to the demo device (OTCConnection.isDemo), set by
    /// MainView over every tab: what would change something on it is left
    /// out.
    var demoDevice: Bool {
        get { self[DemoDeviceKey.self] }
        set { self[DemoDeviceKey.self] = newValue }
    }
}

/// The banner over every tab while signed in to the demo device, built as
/// the critical update's (CriticalUpdateBanner): what this is, and a link
/// to the website for a device of one's own.
struct DemoBanner: View {
    /// Its colour, carried up behind the status bar in the narrow layout.
    static let color = Color.indigo

    var body: some View {
        HStack(alignment: .center, spacing: 10) {
            Image(systemName: "sparkles")
                .font(.title3)
                .accessibilityHidden(true)
            Text(S.appShellDemoBanner)
                .font(.footnote)
                .frame(maxWidth: .infinity, alignment: .leading)
                .fixedSize(horizontal: false, vertical: true)
            Link(S.appShellDemoGetOwn, destination: DemoDevice.website)
                .buttonStyle(.bordered)
                .tint(.white)
                .controlSize(.small)
        }
        .foregroundStyle(.white)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(Self.color)
    }
}
