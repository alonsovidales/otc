// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SettingsView.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//
//  Extended to also cover the web app's device Settings tab
//  (web/src/components/SettingsForm.tsx: password change, bridge secret) and a
//  native port of the status readout (web/src/components/StatusWidget.tsx),
//  alongside the original app-level connection/sync settings.

import SwiftUI
import Photos

@MainActor
final class DeviceSettingsViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    // Issue #40: shared secret this device registers with the bridge relay.
    @Published var currentBridgeSecret = ""
    @Published var newBridgeSecret = ""
    @Published var savingSecret = false

    @Published var oldKey = ""
    @Published var newKey = ""
    @Published var confirmKey = ""
    @Published var savingKey = false

    @Published var toast: String?

    // Issue #52: off by default - see db.sql's settings.face_recognition_
    // enabled doc comment for why turning this on never retroactively
    // processes anything already in the library.
    @Published var faceRecognitionEnabled = false
    @Published var savingFaceRecognition = false

    // Issue #73: full-library reprocess - re-runs tagging/face detection
    // against every already-uploaded photo/video (e.g. after a detection
    // fix), stripping existing tags/people first so they're recalculated
    // clean. Runs entirely server-side; this just starts it and polls for
    // progress the same way status.swift's StatusViewModel polls GetStatus.
    @Published var reprocessStatus = ""
    @Published var reprocessTotal: Int32 = 0
    @Published var reprocessProcessed: Int32 = 0
    @Published var startingReprocess = false
    // "resume": continue a stopped run from where it left off. "restart":
    // wipe everything and start fresh - always available for a first run,
    // and also offered *instead of* resume when a run was stopped, so the
    // owner isn't stuck only ever continuing (issue #73 follow-up).
    enum ReprocessConfirmAction { case resume, restart }
    @Published var reprocessConfirmAction: ReprocessConfirmAction? = nil
    private var reprocessPollTask: Task<Void, Never>?

    func loadReprocessStatus() async {
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            req.payload = .reqGetReprocessStatus(.init())
            e = req
        }) else { return }
        if case .respReprocessStatus(let s) = resp.payload {
            reprocessStatus = s.status
            reprocessTotal = s.total
            reprocessProcessed = s.processed
        }
    }

    // Self-rescheduling: loads once, and if that leaves status "running",
    // schedules another load in 1.5s - same idle-when-nothing's-happening
    // shape as OTCConnection's own reconnect backoff, just on a fixed
    // interval since this only ever needs to run while a job is active.
    func pollReprocessStatusWhileRunning() {
        reprocessPollTask?.cancel()
        reprocessPollTask = Task {
            await loadReprocessStatus()
            while !Task.isCancelled && reprocessStatus == "running" {
                try? await Task.sleep(nanoseconds: 1_500_000_000)
                guard !Task.isCancelled else { return }
                await loadReprocessStatus()
            }
        }
    }

    func stopPollingReprocessStatus() {
        reprocessPollTask?.cancel()
        reprocessPollTask = nil
    }

    // Rounded, clamped to [0, 100] - total can be 0 right when a run has
    // just started and the very first status poll hasn't landed yet.
    var reprocessPercent: Int {
        guard reprocessTotal > 0 else { return 0 }
        let pct = Int((Double(reprocessProcessed) / Double(reprocessTotal) * 100).rounded())
        return max(0, min(100, pct))
    }

    func startReprocess(forceRestart: Bool) async {
        reprocessConfirmAction = nil
        startingReprocess = true
        defer { startingReprocess = false }
        do {
            var req = Msg_StartReprocess()
            req.forceRestart = forceRestart
            let resp = try await ws.request { e in
                var envelope = ReqEnvelope()
                envelope.payload = .reqStartReprocess(req)
                e = envelope
            }
            if case .respAck(let ack) = resp.payload, ack.ok {
                pollReprocessStatusWhileRunning()
            } else if case .respAck(let ack) = resp.payload {
                showToast(ack.errorMsg.isEmpty ? "Could not start reprocessing" : ack.errorMsg)
            }
        } catch {
            showToast("Error starting reprocessing")
        }
    }

    // Cancelling doesn't wait for the worker to actually wind down (it
    // notices between files - see files_manager.CancelReprocess) - just
    // requests it and refreshes status, same as starting.
    @Published var cancellingReprocess = false
    func stopReprocess() async {
        cancellingReprocess = true
        defer { cancellingReprocess = false }
        do {
            let resp = try await ws.request { e in
                var req = ReqEnvelope()
                req.payload = .reqStopReprocess(.init())
                e = req
            }
            if case .respAck(let ack) = resp.payload, ack.ok {
                // Deliberately does NOT stop polling here, and does not
                // force one extra status read either: this Ack just means
                // the cancellation was *requested* - the worker only
                // notices between files (see files_manager.
                // CancelReprocess), which can take several seconds on a
                // slow file. Reading status right now would very likely
                // still see "running" and, if that got treated as fresh
                // truth while the poll loop was torn down, the screen
                // would freeze on "running" forever with nothing left to
                // ever notice the real "stopped" that follows a few
                // seconds later - exactly the bug this comment replaced.
                // The existing pollReprocessStatusWhileRunning loop
                // (already active, since this button only shows while
                // status is "running") keeps ticking every 1.5s and exits
                // itself the moment it actually observes "stopped".
            } else if case .respAck(let ack) = resp.payload {
                showToast(ack.errorMsg.isEmpty ? "Could not stop reprocessing" : ack.errorMsg)
            }
        } catch {
            showToast("Error stopping reprocessing")
        }
    }

    func loadSettings() async {
        do {
            let resp = try await ws.request { $0.payload = .reqGetSettings(Msg_GetSettings()) }
            if case .respSettings(let s) = resp.payload {
                currentBridgeSecret = s.bridgeSecret
                faceRecognitionEnabled = s.faceRecognitionEnabled
            }
        } catch { /* leave blank - the rest of the screen still works */ }
    }

    func toggleFaceRecognition(_ enabled: Bool) async {
        savingFaceRecognition = true
        defer { savingFaceRecognition = false }
        var req = Msg_SetFaceRecognitionEnabled()
        req.enabled = enabled
        do {
            let resp = try await ws.request { $0.payload = .reqSetFaceRecognitionEnabled(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                faceRecognitionEnabled = enabled
            } else {
                // Revert the optimistic toggle - see the Toggle binding below.
                faceRecognitionEnabled = !enabled
                if case .respAck(let ack) = resp.payload {
                    showToast(ack.errorMsg.isEmpty ? "Update failed" : ack.errorMsg)
                }
            }
        } catch {
            faceRecognitionEnabled = !enabled
            showToast("Error updating this setting")
        }
    }


    // Issue #40: update the bridge shared secret, independent of the
    // domain (they used to be updated together server-side, which silently
    // broke a device's bridge pairing on every plain domain rename).
    func saveBridgeSecret() async {
        let trimmed = newBridgeSecret.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        savingSecret = true
        defer { savingSecret = false }
        var req = Msg_SetBridgeSecret()
        req.secret = trimmed
        do {
            let resp = try await ws.request { $0.payload = .reqSetBridgeSecret(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                currentBridgeSecret = trimmed
                newBridgeSecret = ""
                showToast("Bridge secret updated ✅")
            } else if case .respAck(let ack) = resp.payload {
                showToast(ack.errorMsg.isEmpty ? "Update failed" : ack.errorMsg)
            } else {
                showToast("Unexpected response")
            }
        } catch {
            showToast("Error updating bridge secret")
        }
    }

    /// Encrypts old/new password with this connection's public key (same
    /// RSA-OAEP flow used at sign-in, see PwCrypto.swift / issue #2) and
    /// updates the locally stored password on success so future
    /// reconnects authenticate with the new one.
    func changePassword(secrets: SecretsStore) async {
        guard !oldKey.isEmpty, !newKey.isEmpty else { showToast("Fill in all fields"); return }
        guard newKey == confirmKey else { showToast("Passwords don't match"); return }

        savingKey = true
        defer { savingKey = false }
        do {
            let pubKeyResp = try await ws.request { $0.payload = .reqGetPubKey(Msg_GetPubKey()) }
            guard case .respPubKey(let pubKey) = pubKeyResp.payload else {
                showToast("Could not fetch the connection's public key")
                return
            }
            let encOld = try PwCrypto.encryptPassword(oldKey, pubKeyDER: pubKey.publicKey)
            let encNew = try PwCrypto.encryptPassword(newKey, pubKeyDER: pubKey.publicKey)

            var req = Msg_ChangeKey()
            req.oldKey = encOld
            req.newKey = encNew
            let resp = try await ws.request { $0.payload = .reqChangeKey(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                secrets.password = newKey
                secrets.persist()
                oldKey = ""; newKey = ""; confirmKey = ""
                showToast("Password changed ✅")
            } else if case .respAck(let ack) = resp.payload {
                showToast(ack.errorMsg.isEmpty ? "Change failed" : ack.errorMsg)
            } else {
                showToast("Unexpected response")
            }
        } catch {
            showToast("Error changing password: \(error.localizedDescription)")
        }
    }

    private func showToast(_ m: String) {
        toast = m
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_500_000_000)
            if self?.toast == m { self?.toast = nil }
        }
    }
}

@MainActor
final class StatusViewModel: ObservableObject {
    private let ws = OTCConnection.shared
    @Published var status: Msg_Status?
    @Published var errorText: String?
    private var pollTask: Task<Void, Never>?

    func start() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                await self.fetch()
                try? await Task.sleep(nanoseconds: 5_000_000_000)
            }
        }
    }

    func stop() {
        pollTask?.cancel()
        pollTask = nil
    }

    private func fetch() async {
        do {
            let resp = try await ws.request { $0.payload = .reqGetStatus(Msg_GetStatus()) }
            if resp.error {
                errorText = resp.errorMessage
                status = nil
            } else if case .respStatus(let s) = resp.payload {
                status = s
                errorText = nil
            }
        } catch {
            errorText = error.localizedDescription
        }
    }
}

struct SettingsView: View {
    @EnvironmentObject var secrets: SecretsStore
    @EnvironmentObject var upload: UploadModel
    @StateObject private var device = DeviceSettingsViewModel()
    @StateObject private var status = StatusViewModel()
    @State private var confirmLogout = false

    var body: some View {
        NavigationStack {
            Form {
                // Issue #84: first thing in Settings, not bundled in with
                // friend management on its own tab (which used to be the
                // only place this was reachable at all).
                ProfileEditorSection()

                UsersManagementSection()

                // Issue #40: set/view the shared secret this device pairs
                // with the bridge relay with, e.g. to match what's
                // configured on the bridge's own admin panel.
                Section(
                    header: Text("Bridge Shared Secret"),
                    footer: Text("This must match what the bridge has on record for this device — it can't learn a new one from here. To rotate it: on the bridge's admin panel, delete and re-add this device's domain (it'll hand you a new secret), then paste that value above.")
                ) {
                    LabeledContent("Current") {
                        Text(device.currentBridgeSecret)
                            .font(.system(.footnote, design: .monospaced))
                            .foregroundColor(.secondary)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                    TextField("Secret from the bridge", text: $device.newBridgeSecret)
                        .autocapitalization(.none)
                        .disableAutocorrection(true)
                        .font(.system(.body, design: .monospaced))
                    Button(device.savingSecret ? "Saving…" : "Save Secret") {
                        Task { await device.saveBridgeSecret() }
                    }
                    .disabled(device.savingSecret || device.newBridgeSecret.trimmingCharacters(in: .whitespaces).isEmpty)
                }

                Section(
                    header: Text("People"),
                    footer: Text("Detect faces in newly uploaded photos so you can search by person. Off by default. Turning this on only affects photos uploaded from now on — it never scans photos you already have, even after you enable it.")
                ) {
                    Toggle(
                        "Face Recognition",
                        isOn: Binding(
                            get: { device.faceRecognitionEnabled },
                            set: { newValue in Task { await device.toggleFaceRecognition(newValue) } }
                        )
                    )
                    .disabled(device.savingFaceRecognition)
                }

                Section(
                    header: Text("Reprocess Media"),
                    footer: Text("Re-run tagging and face detection on every photo and video already in your library — useful after a detection fix or model update. This clears existing tags and recognized people first and rebuilds them from scratch.")
                ) {
                    if device.reprocessStatus == "running" {
                        VStack(alignment: .leading, spacing: 6) {
                            ProgressView(value: device.reprocessTotal > 0 ? Double(device.reprocessProcessed) / Double(device.reprocessTotal) : 0)
                                .progressViewStyle(.linear)
                            HStack {
                                Text("Reprocessing… \(device.reprocessPercent)% (\(device.reprocessProcessed) / \(device.reprocessTotal))")
                                    .font(.caption)
                                    .foregroundColor(.secondary)
                                Spacer()
                                Button(device.cancellingReprocess ? "Stopping…" : "Cancel") {
                                    Task { await device.stopReprocess() }
                                }
                                .font(.caption)
                                .foregroundColor(.red)
                                .disabled(device.cancellingReprocess)
                            }
                        }
                    } else if device.reprocessStatus == "stopped" {
                        Text("Stopped at \(device.reprocessPercent)% (\(device.reprocessProcessed) / \(device.reprocessTotal)).")
                            .font(.caption).foregroundColor(.secondary)
                        HStack {
                            Button(device.startingReprocess ? "Starting…" : "Resume Reprocessing") {
                                device.reprocessConfirmAction = .resume
                            }
                            .disabled(device.startingReprocess)
                            Spacer()
                            Button("Start Over") {
                                device.reprocessConfirmAction = .restart
                            }
                            .disabled(device.startingReprocess)
                        }
                    } else {
                        Button(device.startingReprocess ? "Starting…" : "Reprocess All Media") {
                            device.reprocessConfirmAction = .restart
                        }
                        .disabled(device.startingReprocess)
                        if device.reprocessStatus == "completed" {
                            Text("Last run completed — \(device.reprocessProcessed) file(s) processed.")
                                .font(.caption).foregroundColor(.secondary)
                        } else if device.reprocessStatus == "failed" {
                            Text("Last run failed after \(device.reprocessProcessed) file(s) — try again.")
                                .font(.caption).foregroundColor(.red)
                        }
                    }
                }

                Section(header: Text("Change Password")) {
                    SecureField("Current password", text: $device.oldKey)
                    SecureField("New password", text: $device.newKey)
                    SecureField("Confirm new password", text: $device.confirmKey)
                    Button(device.savingKey ? "Changing…" : "Change Password") {
                        Task { await device.changePassword(secrets: secrets) }
                    }
                    .disabled(device.savingKey || device.oldKey.isEmpty || device.newKey.isEmpty)
                }

                // Issue #80: Tailscale Funnel as an alternative to the
                // bridge. Primary-only too - see TailscaleSection.
                TailscaleSection()

                Section(header: Text("Status")) {
                    StatusSectionContent(vm: status)
                }

                // Issue #122: the only place upload progress is shown. There
                // used to be a hairline over every tab as well (issue #14);
                // a background sync isn't worth announcing everywhere.
                if upload.totalPending > 0 || upload.isUploading {
                    Section(header: Text("Uploads")) {
                        UploadDetail(upload: upload)
                    }
                }

                Section(header: Text("Connection")) {
                    // Issue #121: name for the bridge, or a custom address.
                    ConnectionEndpointFields(endpoint: $secrets.endpoint)
                    SecureField("Password", text: $secrets.password)
                    Text("Device ID: \(secrets.deviceId)")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    Button("Save Connection") {
                        secrets.persist()
                        OTCConnection.shared.invalidate()
                    }
                    Button("Log Out", role: .destructive) {
                        confirmLogout = true
                    }
                }

                Section(header: Text("Sync Options")) {
                    Toggle("Wi-Fi only", isOn: $secrets.wifiOnly)
                    Toggle("Include videos", isOn: $secrets.includeVideos)
                    Toggle("Sync from iCloud", isOn: $secrets.downloadFromiCloud)
                    Button("Authorize Photos Access") {
                        Task { _ = await PHPhotoLibrary.requestAuthorization(for: .readWrite) }
                    }
                }

                Section {
                    Button("Sync Now") {
                        Task {
                            secrets.persist()
                            try? await PhotoSync.shared.runForeground()
                        }
                    }
                    Button("Sync All") {
                        Task {
                            secrets.persist()
                            UserDefaults.standard.set(Date(), forKey: "lastSyncDate")
                            try? await PhotoSync.shared.runForeground()
                        }
                    }
                }

                // Issue #94: in-place updates. Renders nothing on a
                // non-primary instance - see UpdateSection.
                UpdateSection()
            }
            // No nav title (issue #19): the tab bar already labels this
            // screen "Settings". Still .inline so there's no big empty
            // title bar left behind.
            .navigationBarTitleDisplayMode(.inline)
            .overlay(alignment: .top) {
                if let toast = device.toast {
                    Text(toast)
                        .padding(.horizontal, 12).padding(.vertical, 8)
                        .background(.ultraThinMaterial, in: Capsule())
                        .padding(.top, 8)
                }
            }
        }
        .task {
            await device.loadSettings()
            status.start()
            // Covers both "a run is already in progress" (this screen was
            // reopened while it's going) and "was interrupted" (status is
            // still 'running' from a server restart - the poll loop just
            // sees it hasn't advanced until the user starts it again,
            // which is fine, that's Reprocess's own resume path to drive).
            await device.loadReprocessStatus()
            if device.reprocessStatus == "running" {
                device.pollReprocessStatusWhileRunning()
            }
        }
        .onDisappear {
            status.stop()
            device.stopPollingReprocessStatus()
        }
        .alert("Log out of this device?", isPresented: $confirmLogout) {
            Button("Log Out", role: .destructive) { logOut() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The connection, the sync history and everything cached from the device are removed from this phone. Nothing on the device itself is deleted.")
        }
        .confirmationDialog(
            device.reprocessConfirmAction == .resume
                ? "Resume reprocessing where it left off? It can take a while."
                : "Reprocess every photo and video in your library? This deletes all existing tags and recognized people and rebuilds them from scratch. It can take a while and can't be undone.",
            isPresented: Binding(
                get: { device.reprocessConfirmAction != nil },
                set: { if !$0 { device.reprocessConfirmAction = nil } }
            ),
            titleVisibility: .visible
        ) {
            Button(device.reprocessConfirmAction == .resume ? "Resume" : "Reprocess", role: .destructive) {
                Task { await device.startReprocess(forceRestart: device.reprocessConfirmAction == .restart) }
            }
            Button("Cancel", role: .cancel) {}
        }
    }
}

extension SettingsView {
    /// Log Out (the same as SettingsView.kt's logOut): stop everything that
    /// talks to the device, forget what it told us, wipe what this phone
    /// stores. RootView shows onboarding the moment the secrets clear, and
    /// MainView going away takes every per-tab view model with it.
    fileprivate func logOut() {
        status.stop()
        device.stopPollingReprocessStatus()
        NotificationsModel.shared.reset()
        UploadModel.shared.reset()
        SocialFeedViewModel.shared.reset()
        OTCConnection.shared.reset()
        MediaStream.reset()
        SyncScheduler.cancel()
        AssetSyncCache.shared.clear()
        secrets.logOut()
    }
}

// Issue #65: a plain, always-visible read of the RAID's own health -
// "in sync"/"syncing"/"degraded" - rather than something the user has to
// infer from whether an error banner happens to be showing.
private func raidStateLabel(_ s: Msg_Status) -> String {
    switch s.raidState {
    case .raidNone: return "No RAID"
    case .raidInSync: return "In sync"
    case .raidSyncing: return "Syncing (\(Int(s.raidSyncPercent))%)"
    case .raidDegraded: return "Degraded"
    default: return "Unknown"
    }
}

private struct StatusSectionContent: View {
    @ObservedObject var vm: StatusViewModel

    var body: some View {
        if let s = vm.status {
            let usedPct = s.raidSize > 0 ? Double(s.raidUsage) / Double(s.raidSize) * 100 : 0
            // Same threshold as the web bar (see StatusWidget.tsx): a
            // single fill color that itself says "getting full" - green
            // under 70%, amber 70-90%, red past that - rather than a fixed
            // tint that doesn't track how full the RAID actually is.
            let usedTint: Color = usedPct >= 90 ? .red : usedPct >= 70 ? .yellow : .green
            VStack(alignment: .leading, spacing: 6) {
                RaidUsageBar(percent: usedPct, tint: usedTint)
                // The % now lives inside the bar itself - just the MB
                // figures here, not a repeated "(Z%)" suffix.
                Text("RAID used: \(s.raidUsage) MB / \(s.raidSize) MB")
                    .font(.caption)
                Text("Disk: \(s.diskUsage) MB / \(s.diskSize) MB")
                    .font(.caption)
                Text("CPU: \(String(format: "%.1f", s.cpuUsagePrc))% · Mem: \(s.memUsage) MB / \(s.memSize) MB")
                    .font(.caption)
                Text("RAID: \(s.raidLevel.isEmpty ? raidStateLabel(s) : "\(s.raidLevel) — \(raidStateLabel(s))") · Disks: \(s.disks)")
                    .font(.caption)
                ForEach(s.errors, id: \.message) { e in
                    Text("⚠️ \(e.message)").font(.caption).foregroundColor(.red)
                }
            }
        } else if let err = vm.errorText {
            Text(err).font(.caption).foregroundColor(.red)
        } else {
            ProgressView()
        }
    }
}

// A bigger, custom bar rather than the plain system ProgressView - matches
// the web version (StatusWidget.tsx/.css): tall enough to carry the % as a
// dark chip inside the bar's own right edge, so it reads whether that spot
// happens to sit over bare track or over the (light) fill color.
private struct RaidUsageBar: View {
    let percent: Double
    let tint: Color

    var body: some View {
        let clamped = min(max(percent, 0), 100)
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 7)
                    .fill(Color(.systemGray5))
                RoundedRectangle(cornerRadius: 7)
                    .fill(tint)
                    .frame(width: geo.size.width * clamped / 100)
                HStack {
                    Spacer()
                    Text("\(Int(clamped))%")
                        .font(.system(size: 9, weight: .medium))
                        .monospacedDigit()
                        .foregroundColor(.white)
                        .padding(.horizontal, 4)
                        .padding(.vertical, 0.5)
                        .background(Color.black.opacity(0.35), in: RoundedRectangle(cornerRadius: 4))
                        .padding(.trailing, 3)
                }
            }
        }
        // Matches the bar height to the % chip's own height rather than
        // padding it out - see the web version (StatusWidget.css) for the
        // same 14px sizing.
        .frame(height: 14)
    }
}
