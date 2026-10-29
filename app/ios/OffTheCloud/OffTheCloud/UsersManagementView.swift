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
    /// The device has said whether this account is its owner (and, for the
    /// owner, the accounts have been asked for once): until then Settings
    /// shows the section's header and a spinner.
    @Published var answered = false
    @Published var users: [Msg_User] = []
    @Published var loading = false
    @Published var metrics: [String: Msg_RespUserMetrics] = [:]

    @Published var newUsername = ""
    @Published var newPort = ""
    // Issue #103: an address on the bridge for the new user, on by default as in the web app.
    @Published var requestBridgeAccess = true
    @Published var creating = false

    // The sheet Add opens: the new account's first password, set on the
    // device as the account is created, so nobody can sign in first and
    // claim it. Its person changes it later in Change Password.
    @Published var passwordFor: String?
    @Published var newPassword = ""
    @Published var confirmPassword = ""
    @Published var passwordError: SettingsText?
    // The "created" message, shown once the sheet has gone: an alert
    // asked for while it is still closing never comes up.
    var createdToast: SettingsText?

    // The same 8 the device asks for (session.MinPasswordLen).
    var passwordTooShort: Bool { !newPassword.isEmpty && newPassword.count < 8 }
    var passwordMismatch: Bool { !confirmPassword.isEmpty && newPassword != confirmPassword }
    var canCreate: Bool { newPassword.count >= 8 && newPassword == confirmPassword }

    // Set while a row's inline delete-confirmation is expanded - mirrors
    // the web panel's modal, just inline instead of a sheet, since
    // SwiftUI's native .confirmationDialog has no text-entry of its own.
    @Published var deleteTargetUuid: String?
    @Published var deleteConfirmText = ""
    @Published var deleting = false

    @Published var busyActiveUuid: String?
    @Published var toast: SettingsText?

    func checkRole() async {
        guard let primary = await Self.askIsPrimary() else { return }
        // The list first: the section comes with its accounts in it.
        if primary { await loadUsers() }
        isPrimary = primary
        answered = true
    }

    /// Whether the account signed in is the device's owner (the primary
    /// instance), which alone has this section; nil when it couldn't be
    /// asked. The tour asks it too, for its step about users.
    static func askIsPrimary() async -> Bool? {
        guard let resp = try? await OTCConnection.shared.request({ e in
            var req = ReqEnvelope()
            req.payload = .reqGetInstanceRole(.init())
            e = req
        }) else { return nil }
        if case .respInstanceRole(let r) = resp.payload { return r.isPrimary }
        return nil
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

    func askPassword() {
        newPassword = ""
        confirmPassword = ""
        passwordError = nil
        passwordFor = newUsername.trimmingCharacters(in: .whitespaces)
    }

    func cancelPassword() {
        passwordFor = nil
        newPassword = ""
        confirmPassword = ""
    }

    func createUser() async {
        guard let name = passwordFor, canCreate, !creating else { return }
        creating = true
        defer { creating = false }
        passwordError = nil
        do {
            // Same RSA-OAEP flow as sign-in (issue #2).
            let pubKeyResp = try await ws.request { $0.payload = .reqGetPubKey(Msg_GetPubKey()) }
            guard case .respPubKey(let pubKey) = pubKeyResp.payload else {
                passwordError = .say { S.appSettingsPasswordNoKey }
                return
            }
            var req = Msg_ReqCreateUser()
            req.username = name
            req.port = Int32(newPort) ?? 0
            req.requestBridgeAccess = requestBridgeAccess
            req.password = try PwCrypto.encryptPassword(newPassword, pubKeyDER: pubKey.publicKey)
            let resp = try await ws.request { e in
                var env = ReqEnvelope()
                env.payload = .reqCreateUser(req)
                e = env
            }
            if case .respUsers(let r) = resp.payload {
                users = r.users
                newUsername = ""
                newPort = ""
                cancelPassword()
                // Unset: a device older than the password, which created
                // the account without one (its first sign-in sets it).
                createdToast = r.hasPasswordSet
                    ? .say { S.appSettingsUsersCreated(name: name) }
                    : .say { S.appSettingsUsersCreatedNoPassword(name: name) }
            } else {
                passwordError = .asIs(resp.errorMessage, or: { S.appSettingsUsersCreateFailed })
            }
        } catch {
            passwordError = .error(error)
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
    // Owned by SettingsView, which also presents the first-password sheet
    // and the toast (usersManagementSheets): presented from this section,
    // a row of the Settings list, the sheet came and went at once when the
    // list laid out again as the keyboard went down.
    @ObservedObject var vm: UsersManagementViewModel

    private var usernameValid: Bool {
        let u = vm.newUsername.trimmingCharacters(in: .whitespaces)
        return !u.isEmpty && u.range(of: "^[a-z0-9-]+$", options: .regularExpression) != nil
    }

    /// Where the new user will be reachable, then what turning it off means.
    private var bridgeHint: String {
        let name = vm.newUsername.trimmingCharacters(in: .whitespaces)
        let domain = vm.users.first { $0.bridgeAccess && !$0.subdomain.isEmpty }
            .map { $0.subdomain.split(separator: ".").dropFirst().joined(separator: ".") } ?? ""
        let reach = !name.isEmpty && !domain.isEmpty
            ? S.appSettingsUsersReachableAt(address: "\(name).\(domain)")
            : S.appSettingsUsersReachableOwn
        return reach + " " + S.appSettingsUsersLocalHint
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
                    Button(S.commonAdd) {
                        // The name field's keyboard goes down: the sheet
                        // has fields of its own.
                        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
                        vm.askPassword()
                    }
                    .buttonStyle(.bordered)
                        .disabled(vm.creating || !usernameValid)
                }
                .tourTarget(.addUser)
                .id(TourTarget.addUser)

                // Issue #103, as the web app: the address the new user
                // gets on the bridge.
                VStack(alignment: .leading, spacing: 4) {
                    Toggle(S.appSettingsUsersRequestBridge, isOn: $vm.requestBridgeAccess)
                    Text(bridgeHint)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .tourTarget(.bridgeAccess)
            }
        } else if !vm.answered {
            // Asked for by SettingsView (loadDeviceSections).
            LoadingSection(title: S.appSettingsUsersTitle)
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
                    .buttonStyle(.bordered)
                        .font(.caption)
                }
            }
            HStack {
                Button(u.active ? S.appSettingsUsersDisable : S.appSettingsUsersEnable) { Task { await vm.toggleActive(u) } }
                .buttonStyle(.bordered)
                    .font(.caption)
                    .disabled(vm.busyActiveUuid == u.uuid)
                Spacer()
                Button(S.commonDelete, role: .destructive) {
                    vm.deleteTargetUuid = (vm.deleteTargetUuid == u.uuid) ? nil : u.uuid
                    vm.deleteConfirmText = ""
                }
                .buttonStyle(.bordered)
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
                    .buttonStyle(.bordered)
                    .disabled(vm.deleting || vm.deleteConfirmText != u.username)
                }
                .padding(.top, 4)
            }
        }
        .padding(.vertical, 2)
    }
}

extension View {
    /// The Users section's first-password sheet and its toast, presented
    /// from outside the Settings list (see UsersManagementSection).
    func usersManagementSheets(_ vm: UsersManagementViewModel) -> some View {
        self
            .sheet(isPresented: Binding(
                get: { vm.passwordFor != nil },
                set: { if !$0 { vm.cancelPassword() } }
            ), onDismiss: {
                vm.toast = vm.createdToast
                vm.createdToast = nil
            }) {
                FirstPasswordSheet(vm: vm)
            }
            .alert(vm.toast?.text ?? "", isPresented: Binding(
                get: { vm.toast != nil },
                set: { if !$0 { vm.toast = nil } }
            )) {
                Button(S.commonOk, role: .cancel) {}
            }
    }
}

/// The new account's first password, asked for when Add is tapped. Its
/// errors show here: an alert can't come up over the sheet.
private struct FirstPasswordSheet: View {
    @ObservedObject var vm: UsersManagementViewModel

    var body: some View {
        let name = vm.passwordFor ?? ""
        NavigationStack {
            Form {
                Section {
                    SecureField(S.appSettingsUsersPassword, text: $vm.newPassword)
                    SecureField(S.appSettingsUsersPasswordConfirm, text: $vm.confirmPassword)
                } footer: {
                    VStack(alignment: .leading, spacing: 6) {
                        if vm.passwordTooShort {
                            Text(S.appSettingsUsersPasswordTooShort).foregroundStyle(.red)
                        }
                        if vm.passwordMismatch {
                            Text(S.appSettingsUsersPasswordMismatch).foregroundStyle(.red)
                        }
                        if let e = vm.passwordError {
                            Text(e.text).foregroundStyle(.red)
                        }
                        Text(S.appSettingsUsersPasswordHint(name: name))
                    }
                }
            }
            .navigationTitle(S.appSettingsUsersPasswordTitle(name: name))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(S.commonCancel) { vm.cancelPassword() }
                        .disabled(vm.creating)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(vm.creating ? "…" : S.appSettingsUsersCreate) { Task { await vm.createUser() } } // i18n-ignore: an ellipsis while the user is being added
                        .disabled(vm.creating || !vm.canCreate)
                }
            }
        }
        .presentationDetents([.medium, .large])
        .interactiveDismissDisabled(vm.creating)
    }
}
