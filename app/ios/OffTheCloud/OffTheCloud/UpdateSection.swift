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

    private let ws = OTCConnection.shared
    private var pollTask: Task<Void, Never>?

    var hasUpdate: Bool { !pending.isEmpty }
    var running: Bool { state == "running" }

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
            }
            .task { await vm.load() }
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
