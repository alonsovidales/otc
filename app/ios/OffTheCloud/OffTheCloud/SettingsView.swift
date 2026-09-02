//
//  SettingsView.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//
//  Extended to also cover the web app's device Settings tab
//  (web/src/components/SettingsForm.tsx: domain + password change) and a
//  native port of the status readout (web/src/components/StatusWidget.tsx),
//  alongside the original app-level connection/sync settings.

import SwiftUI
import Photos

@MainActor
final class DeviceSettingsViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    @Published var domain = ""
    @Published var savingDomain = false

    @Published var oldKey = ""
    @Published var newKey = ""
    @Published var confirmKey = ""
    @Published var savingKey = false

    @Published var toast: String?

    func loadSettings() async {
        do {
            let resp = try await ws.request { $0.payload = .reqGetSettings(Msg_GetSettings()) }
            if case .respSettings(let s) = resp.payload { domain = s.domain }
        } catch { /* leave blank; user can still type a new domain */ }
    }

    func saveDomain() async {
        let trimmed = domain.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        savingDomain = true
        defer { savingDomain = false }
        var req = Msg_SetSettings()
        req.domain = trimmed
        do {
            let resp = try await ws.request { $0.payload = .reqSetSettings(req) }
            switch resp.payload {
            case .respAck(let ack):
                showToast(ack.ok ? "Domain updated ✅" : (ack.errorMsg.isEmpty ? "Update failed" : ack.errorMsg))
            case .respSettings(let s):
                domain = s.domain
                showToast("Domain updated ✅")
            default:
                showToast("Unexpected response")
            }
        } catch {
            showToast("Error updating domain")
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

    var body: some View {
        NavigationView {
            Form {
                Section(header: Text("Device")) {
                    HStack {
                        TextField("Domain", text: $device.domain)
                            .autocapitalization(.none)
                            .keyboardType(.URL)
                        Button(device.savingDomain ? "Saving…" : "Save") {
                            Task { await device.saveDomain() }
                        }
                        .disabled(device.savingDomain || device.domain.trimmingCharacters(in: .whitespaces).isEmpty)
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

                Section(header: Text("Status")) {
                    StatusSectionContent(vm: status)
                }

                Section(header: Text("Connection")) {
                    TextField("Endpoint (wss://…/ws)", text: $secrets.endpoint)
                        .autocapitalization(.none)
                    SecureField("Password", text: $secrets.password)
                    Text("Device ID: \(secrets.deviceId)")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    Button("Save Connection") {
                        secrets.persist()
                        OTCConnection.shared.invalidate()
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
            }
            .navigationTitle("Settings")
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
        }
        .onDisappear { status.stop() }
    }
}

private struct StatusSectionContent: View {
    @ObservedObject var vm: StatusViewModel

    var body: some View {
        if let s = vm.status {
            let usedPct = s.raidSize > 0 ? Double(s.raidUsage) / Double(s.raidSize) * 100 : 0
            VStack(alignment: .leading, spacing: 6) {
                ProgressView(value: min(max(usedPct, 0), 100), total: 100)
                Text("RAID used: \(s.raidUsage) MB / \(s.raidSize) MB (\(Int(usedPct))%)")
                    .font(.caption)
                Text("Disk: \(s.diskUsage) MB / \(s.diskSize) MB")
                    .font(.caption)
                Text("CPU: \(String(format: "%.1f", s.cpuUsagePrc))% · Mem: \(s.memUsage) MB / \(s.memSize) MB")
                    .font(.caption)
                Text("Local IP: \(s.localIp) · Disks: \(s.disks)")
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
