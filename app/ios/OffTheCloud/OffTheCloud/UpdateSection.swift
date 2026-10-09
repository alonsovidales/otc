// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  UpdateSection.swift
//  OffTheCloud
//
//  Issue #94: updating the device from Settings, without anyone
//  rebuilding or reinstalling anything.
//
//  Shown only on the primary instance, the same rule the Users section
//  follows: an additional user (issue #82) is a separate process sharing
//  one machine, and the binary and schema it runs on are not theirs to
//  replace. The device enforces that too - this only decides what to draw.
//
//  Mirrors web/src/components/UpdatePanel.tsx.

import SwiftUI

@MainActor
final class UpdateViewModel: ObservableObject {
    @Published var isPrimary = false
    @Published var currentVersion: Int32 = 0
    @Published var latestVersion: Int32 = 0
    // Issue #183: "major.minor" names; empty from a device on old releases.
    @Published var currentLabel = ""
    @Published var latestLabel = ""
    @Published var pending: [Msg_UpdateRelease] = []
    @Published var state = ""
    @Published var message = ""
    @Published var checkError = ""
    @Published var lastUpdated = ""
    @Published var checking = false
    @Published var starting = false
    @Published var error: String?
    // Settings > Restart device: "" (nothing asked), "asking", "restarting",
    // "back" or "slow".
    @Published var restartPhase = ""
    @Published var restartError: String?

    private let ws = OTCConnection.shared
    private var pollTask: Task<Void, Never>?
    // Bounded by waitUntilBack's five minutes, like pollTask by the update.
    private var restartTask: Task<Void, Never>?

    var hasUpdate: Bool { !pending.isEmpty }
    var running: Bool { state == "running" }
    var restarting: Bool { restartPhase == "asking" || restartPhase == "restarting" }

    /// Issue #183: "1.1 (build 42)", or "build 42" without a label.
    var installedText: String {
        guard currentVersion > 0 else { return "—" }
        return currentLabel.isEmpty ? "build \(currentVersion)" : "\(currentLabel) (build \(currentVersion))"
    }

    /// The newest release's name, and its kind when it is pending.
    var latestText: String { latestLabel.isEmpty ? "build \(latestVersion)" : latestLabel }
    var latestKind: String { pending.first { $0.version == latestVersion }?.kind ?? "" }

    func load() async {
        do {
            let resp = try await ws.request { e in
                e.payload = .reqGetInstanceRole(Msg_ReqGetInstanceRole())
            }
            if case .respInstanceRole(let role) = resp.payload {
                isPrimary = role.isPrimary
            }
        } catch {
            isPrimary = false
        }
        guard isPrimary else { return }
        await check()
    }

    func check() async {
        checking = true
        defer { checking = false }
        do {
            let resp = try await ws.request { e in
                e.payload = .reqCheckUpdate(Msg_ReqCheckUpdate())
            }
            guard case .respUpdateInfo(let info) = resp.payload else {
                if resp.error { error = resp.errorMessage }
                return
            }
            currentVersion = info.currentVersion
            latestVersion = info.latestVersion
            currentLabel = info.currentLabel
            latestLabel = info.latestLabel
            pending = info.pending
            state = info.state
            message = info.message
            checkError = info.checkError
            lastUpdated = info.lastUpdated
            error = nil
            startPollingIfRunning()
        } catch {
            // A dropped socket mid-update is expected rather than a
            // failure worth reporting: the device is restarting into the
            // version it just installed.
            startPollingIfRunning()
        }
    }

    func apply() async {
        starting = true
        defer { starting = false }
        do {
            let resp = try await ws.request { e in
                e.payload = .reqApplyUpdate(Msg_ReqApplyUpdate())
            }
            if case .respAck(let ack) = resp.payload, ack.ok {
                state = "running"
                message = "Starting"
                startPollingIfRunning()
            } else {
                error = resp.error ? resp.errorMessage : "Could not start the update."
            }
        } catch {
            self.error = error.localizedDescription
        }
    }

    /// Restarts the whole device (owner of the primary only; the device
    /// checks too), then waits for it to answer again. An answer only
    /// counts once a request has failed since, or 90 s have passed: until
    /// the restart really begins the device still answers.
    func restart() async {
        restartPhase = "asking"
        restartError = nil
        do {
            let resp = try await ws.request { e in
                e.payload = .reqRestartDevice(Msg_RestartDevice())
            }
            guard case .respRestartingDevice = resp.payload else {
                if resp.error && resp.errorCode == "unknown_payload" {
                    restartError = "Your device needs an update before it can be restarted from here."
                } else if resp.error && !resp.errorMessage.isEmpty {
                    restartError = resp.errorMessage
                } else {
                    restartError = "Could not restart the device."
                }
                restartPhase = ""
                return
            }
        } catch {
            restartError = error.localizedDescription
            restartPhase = ""
            return
        }
        restartPhase = "restarting"
        restartTask?.cancel()
        restartTask = Task { [weak self] in
            await self?.waitUntilBack(asked: Date())
        }
    }

    private func waitUntilBack(asked: Date) async {
        var failed = false
        try? await Task.sleep(nanoseconds: 20_000_000_000)
        while !Task.isCancelled && Date().timeIntervalSince(asked) < 300 {
            var answered = false
            do {
                let resp = try await ws.request { e in
                    e.payload = .reqGetInstanceRole(Msg_ReqGetInstanceRole())
                }
                // Through the bridge a device that is away still gets an
                // answer - the bridge's error - which isn't the device.
                if case .respInstanceRole = resp.payload {
                    answered = true
                } else {
                    failed = true
                }
            } catch {
                failed = true
            }
            if answered && (failed || Date().timeIntervalSince(asked) >= 90) {
                restartPhase = "back"
                await check()
                return
            }
            try? await Task.sleep(nanoseconds: 5_000_000_000)
        }
        if !Task.isCancelled { restartPhase = "slow" }
    }

    /// The device rebuilds and restarts itself, so the connection drops
    /// partway through - polling is how this picks the story back up once
    /// it reconnects.
    private func startPollingIfRunning() {
        guard running else {
            pollTask?.cancel()
            pollTask = nil
            return
        }
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 5_000_000_000)
                guard await self.running else { break }
                await self.check()
            }
            await MainActor.run { self?.pollTask = nil }
        }
    }
}

struct UpdateSection: View {
    @StateObject private var vm = UpdateViewModel()
    @State private var confirmRestart = false

    var body: some View {
        if vm.isPrimary {
            Section(header: Text("Device version")) {
                HStack {
                    Text(vm.installedText)
                        .monospacedDigit()
                    if vm.hasUpdate {
                        Text("→ \(vm.latestText)").monospacedDigit().bold()
                        ReleaseKindBadge(kind: vm.latestKind)
                    }
                    Spacer()
                    if !vm.hasUpdate && !vm.running && vm.checkError.isEmpty {
                        Text("Up to date").foregroundStyle(.secondary).font(.caption)
                    }
                }

                ForEach(Array(vm.pending.enumerated()), id: \.offset) { _, release in
                    HStack(alignment: .top, spacing: 6) {
                        Text(release.label.isEmpty ? "build \(release.version)" : release.label)
                            .monospacedDigit().bold()
                        ReleaseKindBadge(kind: release.kind)
                        Text(release.summary).foregroundStyle(.secondary)
                    }
                    .font(.caption)
                }

                if vm.running {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text(vm.message.isEmpty ? "Updating…" : vm.message)
                            .foregroundStyle(.secondary)
                    }
                }

                if vm.state == "failed" {
                    // With the date: a failure sits in the status file
                    // until another run replaces it, so one from weeks
                    // ago would otherwise read as something that just
                    // happened.
                    Text(vm.lastUpdated.isEmpty
                         ? "Update failed: \(vm.message)"
                         : "Update failed on \(vm.lastUpdated): \(vm.message)")
                        .font(.caption).foregroundStyle(.red)
                }

                if !vm.checkError.isEmpty {
                    Text("Couldn't reach the update server just now, so this may be out of date.")
                        .font(.caption).foregroundStyle(.secondary)
                }

                Button(vm.checking ? "Checking…" : "Check again") {
                    Task { await vm.check() }
                }
                .disabled(vm.checking || vm.running)

                if vm.hasUpdate {
                    Button(vm.starting ? "Starting…" : "Update to \(vm.latestText)") {
                        Task { await vm.apply() }
                    }
                    .disabled(vm.starting || vm.running)
                }

                if vm.hasUpdate || vm.running {
                    Text("The device rebuilds itself and restarts, which takes a few minutes and drops this connection on the way. Your photos and settings are left alone.")
                        .font(.caption).foregroundStyle(.secondary)
                }

                if let error = vm.error {
                    Text(error).font(.caption).foregroundStyle(.red)
                }

                // Settings > Restart device: the whole machine, owner of
                // the primary only.
                Button(vm.restarting ? "Restarting…" : "Restart Device", role: .destructive) {
                    confirmRestart = true
                }
                .disabled(vm.restarting || vm.running)

                switch vm.restartPhase {
                case "restarting":
                    HStack(spacing: 8) {
                        ProgressView()
                        Text("Restarting… the device will be back in about a minute.")
                            .foregroundStyle(.secondary)
                    }
                case "back":
                    Text("The device is back.").font(.caption).foregroundStyle(.secondary)
                case "slow":
                    Text("The device hasn't come back yet. It can take a little longer; if it doesn't come back, unplug it and plug it back in.")
                        .font(.caption).foregroundStyle(.orange)
                default:
                    EmptyView()
                }

                if let error = vm.restartError {
                    Text(error).font(.caption).foregroundStyle(.red)
                }
            }
            .task { await vm.load() }
            .alert("Restart the device?", isPresented: $confirmRestart) {
                Button("Cancel", role: .cancel) {}
                Button("Restart", role: .destructive) {
                    Task { await vm.restart() }
                }
            } message: {
                Text("It will be unreachable for about a minute.")
            }
        } else {
            // Nothing to show, but the role still has to be asked for.
            Color.clear.frame(height: 0).task { await vm.load() }
        }
    }
}

/// Issue #183: "Major" (orange) or "Critical" (red) next to a release;
/// nothing for a minor one or a release from before kinds existed.
private struct ReleaseKindBadge: View {
    let kind: String

    var body: some View {
        if let (text, color) = style {
            Text(text)
                .font(.caption2.bold())
                .foregroundStyle(.white)
                .padding(.horizontal, 5)
                .padding(.vertical, 1)
                .background(color, in: Capsule())
        }
    }

    private var style: (String, Color)? {
        switch kind {
        case "major": return ("Major", .orange)
        case "critical": return ("Critical", .red)
        default: return nil
        }
    }
}
