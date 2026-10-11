// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  UsersManagementView.swift
//  OffTheCloud
//
//  Issue #82: multiple OTC "users" on one device, each a fully separate
//  process/port/database/storage directory (see supervisor/supervisor.go
//  on the backend). This section only ever renders on the PRIMARY
//  instance - every other user's own Settings screen never even asks for
//  the role, so it never shows this section at all (see isPrimary below).
//

import SwiftUI

@MainActor
final class UsersManagementViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    @Published var isPrimary = false
    @Published var users: [Msg_User] = []
    @Published var loading = false
    @Published var metrics: [String: Msg_RespUserMetrics] = [:]

    @Published var newUsername = ""
    @Published var newPort = ""
    @Published var creating = false

    // Set while a row's inline delete-confirmation is expanded - mirrors
    // the web panel's modal, just inline instead of a sheet, since
    // SwiftUI's native .confirmationDialog has no text-entry of its own.
    @Published var deleteTargetUuid: String?
    @Published var deleteConfirmText = ""
    @Published var deleting = false

    @Published var busyActiveUuid: String?
    @Published var toast: SettingsText?

    func checkRole() async {
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            req.payload = .reqGetInstanceRole(.init())
            e = req
        }) else { return }
        if case .respInstanceRole(let r) = resp.payload {
            isPrimary = r.isPrimary
            if isPrimary { await loadUsers() }
        }
    }

    func loadUsers() async {
        loading = true
        defer { loading = false }
        guard let resp = try? await ws.request({ e in
            var req = ReqEnvelope()
            req.payload = .reqListUsers(.init())
            e = req
        }) else { return }
        if case .respUsers(let r) = resp.payload {
            users = r.users
        }
    }

    func fetchMetrics(_ uuid: String) async {
        var req = Msg_ReqGetUserMetrics()
        req.uuid = uuid
        guard let resp = try? await ws.request({ e in
            var env = ReqEnvelope()
            env.payload = .reqGetUserMetrics(req)
            e = env
        }) else { return }
        if case .respUserMetrics(let m) = resp.payload {
            metrics[uuid] = m
        }
    }

    func createUser() async {
        creating = true
        defer { creating = false }
        var req = Msg_ReqCreateUser()
        req.username = newUsername.trimmingCharacters(in: .whitespaces)
        req.port = Int32(newPort) ?? 0
        do {
            let resp = try await ws.request { e in
                var env = ReqEnvelope()
                env.payload = .reqCreateUser(req)
                e = env
            }
            if case .respUsers(let r) = resp.payload {
                users = r.users
                newUsername = ""
                newPort = ""
                let name = req.username
                toast = .say { S.appSettingsUsersCreated(name: name) }
            } else {
                toast = .asIs(resp.errorMessage, or: { S.appSettingsUsersCreateFailed })
            }
        } catch {
            toast = .error(error)
        }
    }

    func confirmDelete(_ u: Msg_User) async {
        deleting = true
        defer { deleting = false }
        var req = Msg_ReqDeleteUser()
        req.uuid = u.uuid
        req.confirmUsername = deleteConfirmText
        do {
            let resp = try await ws.request { e in
                var env = ReqEnvelope()
                env.payload = .reqDeleteUser(req)
                e = env
            }
            if case .respAck(let ack) = resp.payload, ack.ok {
                let name = u.username
                toast = .say { S.appSettingsUsersDeleted(name: name) }
                deleteTargetUuid = nil
                deleteConfirmText = ""
                await loadUsers()
            } else {
                toast = .asIs(resp.errorMessage, or: { S.appSettingsUsersDeleteFailed })
            }
        } catch {
            toast = .error(error)
        }
    }

    // Issue #90: reversible enable/disable, distinct from confirmDelete
    // above - no type-to-confirm needed, since nothing is destroyed.
    func toggleActive(_ u: Msg_User) async {
        busyActiveUuid = u.uuid
        defer { busyActiveUuid = nil }
        var req = Msg_ReqSetUserActive()
        req.uuid = u.uuid
        req.active = !u.active
        do {
            let resp = try await ws.request { e in
                var env = ReqEnvelope()
                env.payload = .reqSetUserActive(req)
                e = env
            }
            if case .respAck(let ack) = resp.payload, ack.ok {
                await loadUsers()
            } else {
                toast = .asIs(resp.errorMessage, or: { S.appSettingsUsersUpdateFailed })
            }
        } catch {
            toast = .error(error)
        }
    }
}

/// Embedded directly inside SettingsView's own Form (a Section-returning
/// subview, same as this file's usernameValid-style helpers elsewhere in
/// this app) - renders nothing at all once the role check comes back
/// non-primary.
struct UsersManagementSection: View {
    @StateObject private var vm = UsersManagementViewModel()

    private var usernameValid: Bool {
        let u = vm.newUsername.trimmingCharacters(in: .whitespaces)
        return !u.isEmpty && u.range(of: "^[a-z0-9-]+$", options: .regularExpression) != nil
    }

    var body: some View {
        if vm.isPrimary {
            Section(header: Text(S.appSettingsUsersTitle)) {
                Text(S.appSettingsUsersIntro)
                    .font(.caption)
                    .foregroundStyle(.secondary)

                ForEach(vm.users, id: \.uuid) { u in
                    userRow(u)
                }

                HStack {
                    TextField(S.appSettingsUsersUsername, text: $vm.newUsername)
                        .autocapitalization(.none)
                        .disableAutocorrection(true)
                    TextField(S.appSettingsUsersPort, text: $vm.newPort)
                        .keyboardType(.numberPad)
                        .frame(width: 70)
                    Button(vm.creating ? "…" : S.commonAdd) { Task { await vm.createUser() } } // i18n-ignore: an ellipsis while the user is being added
                        .disabled(vm.creating || !usernameValid)
                }
            }
            .alert(vm.toast?.text ?? "", isPresented: Binding(
                get: { vm.toast != nil },
                set: { if !$0 { vm.toast = nil } }
            )) {
                Button(S.commonOk, role: .cancel) {}
            }
        } else {
            // The role check has to run while nothing is shown yet: a
            // .task on the section above never ran, since the section
            // only exists once the check has passed (it was never shown).
            Color.clear.frame(height: 0).task { await vm.checkRole() }
        }
    }

    @ViewBuilder
    private func userRow(_ u: Msg_User) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                        Text(u.username).bold()
                        if !u.active {
                            Text(S.appSettingsUsersInactive).font(.caption2).bold().foregroundStyle(.secondary)
                        }
                    }
                    Text(S.appSettingsUsersPortDomain(port: "\(u.port)", domain: u.subdomain))
                        .font(.caption2).foregroundStyle(.secondary)
                }
                Spacer()
                if let m = vm.metrics[u.uuid] {
                    Text(S.appSettingsUsersUsage(size: "\(Int(m.storageMb))", percent: decimalText("%.1f", m.storagePct), count: Int(m.activeConnections)))
                        .font(.caption2).foregroundStyle(.secondary)
                } else {
                    Button(S.appSettingsUsersLoadUsage) { Task { await vm.fetchMetrics(u.uuid) } }
                        .font(.caption)
                }
            }
            HStack {
                Button(u.active ? S.appSettingsUsersDisable : S.appSettingsUsersEnable) { Task { await vm.toggleActive(u) } }
                    .font(.caption)
                    .disabled(vm.busyActiveUuid == u.uuid)
                Spacer()
                Button(S.commonDelete, role: .destructive) {
                    vm.deleteTargetUuid = (vm.deleteTargetUuid == u.uuid) ? nil : u.uuid
                    vm.deleteConfirmText = ""
                }
                .font(.caption)
            }
            // Issue #82: type-to-confirm, inline rather than a sheet -
            // SwiftUI's native .confirmationDialog has no text field of
            // its own, and this deletes a whole database + storage
            // directory with no way back.
            if vm.deleteTargetUuid == u.uuid {
                VStack(alignment: .leading, spacing: 6) {
                    Text(S.appSettingsUsersConfirmHint(name: u.username))
                        .font(.caption2).foregroundStyle(.secondary)
                    TextField(u.username, text: $vm.deleteConfirmText)
                        .autocapitalization(.none)
                        .disableAutocorrection(true)
                        .textFieldStyle(.roundedBorder)
                    Button(vm.deleting ? S.commonDeleting : S.appSettingsUsersConfirmDelete, role: .destructive) {
                        Task { await vm.confirmDelete(u) }
                    }
                    .disabled(vm.deleting || vm.deleteConfirmText != u.username)
                }
                .padding(.top, 4)
            }
        }
        .padding(.vertical, 2)
    }
}
