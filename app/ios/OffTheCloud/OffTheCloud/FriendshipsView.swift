// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FriendshipsView.swift
//  OffTheCloud
//
//  Native port of the web app's Friends screen (web/src/components/
//  FriendshipsManager.tsx): send a friend request by domain and manage
//  incoming/outgoing friendships. Issue #84: "Your Profile" editing used to
//  live here too (this was the only place it was reachable at all) - moved
//  to the top of Settings instead (see ProfileEditor.swift), so this is
//  just friend management now, presented from a button in SocialFeedView's
//  own toolbar rather than a top-level tab of its own.

import SwiftUI

@MainActor
final class FriendshipsViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    @Published var targetDomain = ""
    @Published var sendingRequest = false

    @Published var friendships: [Msg_Friendship] = []
    @Published var loadingFriendships = false

    @Published var toast: String?

    func reloadFriendships() async {
        loadingFriendships = true
        defer { loadingFriendships = false }
        do {
            let resp = try await ws.request { $0.payload = .reqFriendshipsList(Msg_FriendshipsList()) }
            if case .respFriendships(let f) = resp.payload {
                friendships = f.friendships
            }
        } catch {
            showToast("Could not load friendships")
        }
    }

    /// Issue #142: friends are always <name>.off-the.cloud (the device
    /// refuses anything else), so the box takes only the name. Accepts a
    /// bare name, the full domain or a link to it; nil when it isn't a
    /// device name. Same rule as the web's friendDomain.ts and Android's.
    static func friendDomain(_ input: String) -> String? {
        var s = input.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if let r = s.range(of: "://") { s = String(s[r.upperBound...]) }
        s = String(s.prefix { $0 != "/" && $0 != "?" && $0 != "#" })
        let tld = "." + SecretsStore.bridgeDomain
        if s.hasSuffix(tld) { s = String(s.dropLast(tld.count)) }
        guard s.range(of: "^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$", options: .regularExpression) != nil else { return nil }
        return s + tld
    }

    func sendFriendRequest() async {
        guard !targetDomain.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        guard let domain = Self.friendDomain(targetDomain) else {
            showToast("Enter the device's name, like pit - letters, digits and dashes.")
            return
        }
        sendingRequest = true
        defer { sendingRequest = false }
        var req = Msg_FriendshipRequest()
        req.domain = domain
        do {
            let resp = try await ws.request { $0.payload = .reqFriendshipRequest(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                // Issue #140: "accepted" - they already had this device as a
                // friend and re-linked it, nothing left to accept.
                showToast(ack.code == "accepted" ? "You're friends again ✅" : "Friend request sent ✅")
                targetDomain = ""
                await reloadFriendships()
            } else if case .respAck(let ack) = resp.payload {
                showToast(ack.errorMsg.isEmpty ? "Request failed" : ack.errorMsg)
            } else {
                showToast("Unexpected response")
            }
        } catch {
            showToast("Error sending request")
        }
    }

    func changeStatus(_ f: Msg_Friendship, to status: Msg_FriendShipStatus) async {
        var req = Msg_ChangeFriendStatus()
        req.domain = f.originProfile.domain
        req.status = status
        do {
            let resp = try await ws.request { $0.payload = .reqChangeFriendStatus(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                await reloadFriendships()
                showToast("Status updated ✅")
            } else if case .respAck(let ack) = resp.payload {
                showToast(ack.errorMsg.isEmpty ? "Update failed" : ack.errorMsg)
            } else {
                showToast("Unexpected response")
            }
        } catch {
            showToast("Error updating status")
        }
    }

    // Issue #25: removes the request from this device and, best effort,
    // from the other one - the device does the asking.
    func deleteFriendship(_ f: Msg_Friendship) async {
        var req = Msg_DeleteFriendship()
        req.domain = f.originProfile.domain
        do {
            let resp = try await ws.request { $0.payload = .reqDeleteFriendship(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                await reloadFriendships()
                showToast(f.sent ? "Request cancelled" : "Request deleted")
            } else if resp.error {
                showToast(resp.errorMessage)
            } else {
                showToast("Delete failed")
            }
        } catch {
            showToast("Error deleting request")
        }
    }

    // Issue #174: removes an accepted or blocked friend. deleteTheirData
    // drops everything that friend left on this device (their posts and
    // photos, their comments and likes anywhere); askThemToDeleteMine asks
    // their device to drop what this one shared, and until it confirms the
    // friendship stays, marked leaving. Both false is "Remove Now" for a
    // leaving friend: stop waiting and just drop the friendship.
    func removeFriend(_ f: Msg_Friendship, deleteTheirData: Bool, askThemToDeleteMine: Bool) async {
        var req = Msg_DeleteFriendship()
        req.domain = f.originProfile.domain
        req.deleteTheirData = deleteTheirData
        req.askThemToDeleteMine = askThemToDeleteMine
        do {
            let resp = try await ws.request { $0.payload = .reqDeleteFriendship(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                await reloadFriendships()
                // Still listed as leaving: their device was offline and will
                // be asked the next time it connects.
                let leaving = friendships.contains { $0.originProfile.domain == f.originProfile.domain && $0.leaving }
                showToast(leaving ? "Waiting for their device" : "Friend removed")
            } else if case .respAck(let ack) = resp.payload {
                showToast(ack.errorMsg.isEmpty ? "Remove failed" : ack.errorMsg)
            } else if resp.error {
                showToast(resp.errorMessage)
            } else {
                showToast("Remove failed")
            }
        } catch {
            showToast("Error removing friend")
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

struct FriendshipsView: View {
    @StateObject private var vm = FriendshipsViewModel()
    // Issue #84: this is a sheet presented from Social now, not a tab of
    // its own - it needs its own way to close, same as NewPostPickerView.
    @Environment(\.dismiss) private var dismiss
    /// false: a page of the wide layout's menu (AppMenu.swift), which has
    /// nothing to close.
    private let showsDone: Bool

    init(showsDone: Bool = true) {
        self.showsDone = showsDone
    }

    var body: some View {
        NavigationStack {
            List {
                // Requests waiting for an answer come first: a tapped
                // friend-request alert lands here to answer it.
                // Issue #174: a leaving friendship is never waiting for an
                // answer, whatever its status.
                let waiting = vm.friendships.filter { $0.status == .pending && !$0.sent && !$0.leaving }
                if !waiting.isEmpty {
                    Section("Waiting for your answer") {
                        ForEach(waiting, id: \.originProfile.domain) { f in
                            HStack(spacing: 12) {
                                avatarView(data: f.originProfile.hasImage ? f.originProfile.image : nil, size: 44)
                                VStack(alignment: .leading) {
                                    Text(f.originProfile.name.isEmpty ? "(no name)" : f.originProfile.name).font(.headline)
                                    Text(f.originProfile.domain).font(.caption).foregroundColor(.secondary)
                                }
                                Spacer()
                                Button("Decline") { Task { await vm.deleteFriendship(f) } }
                                    .buttonStyle(.bordered).controlSize(.small)
                                Button("Accept") { Task { await vm.changeStatus(f, to: .accepted) } }
                                    .buttonStyle(.borderedProminent).controlSize(.small)
                            }
                            .padding(.vertical, 4)
                        }
                    }
                }

                Section("Add a friend") {
                    HStack {
                        // Issue #142: the name only; the suffix is fixed.
                        TextField("name", text: $vm.targetDomain)
                            .autocapitalization(.none)
                            .disableAutocorrection(true)
                            .keyboardType(.URL)
                            .multilineTextAlignment(.trailing)
                        Text(".\(SecretsStore.bridgeDomain)")
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .fixedSize()
                        Button("Send") { Task { await vm.sendFriendRequest() } }
                            .disabled(vm.sendingRequest || vm.targetDomain.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                }

                Section {
                    if vm.friendships.isEmpty {
                        Text("No friendships yet.").foregroundColor(.secondary)
                    } else {
                        ForEach(vm.friendships.filter { !($0.status == .pending && !$0.sent && !$0.leaving) }, id: \.originProfile.domain) { f in
                            FriendRow(
                                f: f,
                                onChange: { status in Task { await vm.changeStatus(f, to: status) } },
                                onDelete: { Task { await vm.deleteFriendship(f) } },
                                onRemove: { theirs, mine in
                                    Task { await vm.removeFriend(f, deleteTheirData: theirs, askThemToDeleteMine: mine) }
                                }
                            )
                        }
                    }
                } header: {
                    HStack {
                        Text("Friends")
                        Spacer()
                        if vm.loadingFriendships {
                            ProgressView()
                        } else {
                            Button("Refresh") { Task { await vm.reloadFriendships() } }
                                .font(.caption)
                        }
                    }
                }
            }
            .listStyle(.insetGrouped)
            // Issue #84: now a sheet (see SocialFeedView's own toolbar
            // button), so it needs a real title of its own - the tab bar
            // used to supply "Profile" for free.
            .navigationTitle("Friends")
            .navigationBarTitleDisplayMode(.inline)
            // The wide layout's page: its menu names it, as the web's
            // does, so no title bar.
            .toolbar(showsDone ? .automatic : .hidden, for: .navigationBar)
            .overlay(alignment: .topLeading) {
                if !showsDone { HiddenPageHeading(title: "Friends") }
            }
            .toolbar {
                if showsDone {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Done") { dismiss() }
                    }
                }
            }
            .overlay(alignment: .top) {
                if let toast = vm.toast {
                    Text(toast)
                        .padding(.horizontal, 12).padding(.vertical, 8)
                        .background(.ultraThinMaterial, in: Capsule())
                        .padding(.top, 8)
                        .transition(.move(edge: .top).combined(with: .opacity))
                }
            }
        }
        .task { await vm.reloadFriendships() }
    }
}

private struct FriendRow: View {
    let f: Msg_Friendship
    let onChange: (Msg_FriendShipStatus) -> Void
    let onDelete: () -> Void
    /// Issue #174: (deleteTheirData, askThemToDeleteMine).
    let onRemove: (Bool, Bool) -> Void
    @State private var confirmingDelete = false
    @State private var removing = false
    @State private var confirmingRemoveNow = false

    // Issue #25: a pending request can be removed by either side - the
    // sender withdraws it, the receiver declines it.
    private var canDelete: Bool { f.status == .pending && !f.leaving }
    private var deleteLabel: String { f.sent ? "Cancel request" : "Delete request" }
    // Issue #174: an accepted or blocked friend can be removed, with a
    // choice of what gets deleted on each side.
    private var canRemove: Bool { !f.leaving && (f.status == .accepted || f.status == .blocked) }
    private var name: String {
        f.originProfile.name.isEmpty ? f.originProfile.domain : f.originProfile.name
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 12) {
                avatarView(data: f.originProfile.hasImage ? f.originProfile.image : nil, size: 44)
                VStack(alignment: .leading) {
                    Text(f.originProfile.name.isEmpty ? "(no name)" : f.originProfile.name).font(.headline)
                    Text(f.originProfile.domain.isEmpty ? "(no domain)" : f.originProfile.domain)
                        .font(.caption).foregroundColor(.secondary)
                    Text(statusLabel + (f.sent && !f.leaving ? " (sent)" : ""))
                        .font(.caption2).foregroundColor(.secondary)
                    if f.leaving {
                        Text("Waiting for their device to delete what you shared.")
                            .font(.caption2).foregroundColor(.secondary)
                    }
                }
                Spacer()
            }
            // Issue #179: every action is a button on the row, as on the web.
            HStack(spacing: 8) {
                if f.leaving {
                    // Issue #174: nothing else to do with a leaving friend
                    // but stop waiting for their device.
                    Button("Remove Now", role: .destructive) { confirmingRemoveNow = true }
                        .buttonStyle(.bordered)
                } else {
                    ForEach(actionOptions, id: \.0) { opt in
                        if opt.1 == .accepted {
                            Button(opt.0) { onChange(opt.1) }.buttonStyle(.borderedProminent)
                        } else {
                            Button(opt.0) { onChange(opt.1) }.buttonStyle(.bordered)
                        }
                    }
                    if canDelete {
                        Button(f.sent ? "Cancel Request" : "Decline", role: .destructive) { confirmingDelete = true }
                            .buttonStyle(.bordered)
                    }
                    if canRemove {
                        Button("Remove…", role: .destructive) { removing = true }
                            .buttonStyle(.bordered)
                    }
                }
            }
            .controlSize(.small)
            .padding(.leading, 56)
        }
        .padding(.vertical, 4)
        .confirmationDialog(
            f.sent ? "Cancel this friend request?" : "Delete this friend request?",
            isPresented: $confirmingDelete,
            titleVisibility: .visible
        ) {
            Button(deleteLabel, role: .destructive) { onDelete() }
        } message: {
            Text(f.sent
                 ? "The request will be withdrawn on both devices."
                 : "The request will be removed here and on the sender's device.")
        }
        .confirmationDialog("Remove \(name) now?", isPresented: $confirmingRemoveNow, titleVisibility: .visible) {
            Button("Remove Now", role: .destructive) { onRemove(false, false) }
        } message: {
            Text("Stops waiting for their device. Whatever it hasn't deleted yet stays there.")
        }
        .sheet(isPresented: $removing) {
            RemoveFriendSheet(name: name) { theirs, mine in onRemove(theirs, mine) }
        }
    }

    private var statusLabel: String {
        if f.leaving { return "Leaving" }
        switch f.status {
        case .accepted: return "Accepted"
        case .blocked: return "Blocked"
        default: return "Pending"
        }
    }

    private var actionOptions: [(String, Msg_FriendShipStatus)] {
        // The sender does not decide the status; the receiver does.
        if f.sent || f.leaving { return [] }
        switch f.status {
        case .pending: return [("Accept", .accepted), ("Block", .blocked)]
        case .accepted: return [("Set Pending", .pending), ("Block", .blocked)]
        case .blocked: return [("Accept", .accepted), ("Set Pending", .pending)]
        default: return []
        }
    }
}

/// Issue #174: what removing a friend deletes, on each side - both off by
/// default, so a plain removal keeps everything where it is.
private struct RemoveFriendSheet: View {
    let name: String
    let onConfirm: (Bool, Bool) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var deleteTheirData = false
    @State private var askThemToDeleteMine = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Toggle("Delete everything from \(name) on this iPhone", isOn: $deleteTheirData)
                } footer: {
                    Text("Their posts and photos, and their comments and likes on any post.")
                }
                Section {
                    Toggle("Ask \(name)'s device to delete what I shared", isOn: $askThemToDeleteMine)
                } footer: {
                    Text("Your posts, comments and likes on their device. If it's offline, it happens the next time it connects; until then they show as leaving.")
                }
                Section {
                    Button("Remove Friend", role: .destructive) {
                        onConfirm(deleteTheirData, askThemToDeleteMine)
                        dismiss()
                    }
                    .frame(maxWidth: .infinity)
                }
            }
            .navigationTitle("Remove \(name)?")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}

/// Shared avatar rendering — used by the profile editor and the friends list.
@ViewBuilder
func avatarView(data: Data?, size: CGFloat) -> some View {
    Group {
        if let data, let ui = UIImage(data: data) {
            Image(uiImage: ui).resizable().scaledToFill()
        } else {
            Image(systemName: "person.crop.circle.fill")
                .resizable()
                .foregroundColor(.secondary)
        }
    }
    .frame(width: size, height: size)
    .clipShape(Circle())
}
