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

    func sendFriendRequest() async {
        let domain = targetDomain.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !domain.isEmpty else { return }
        sendingRequest = true
        defer { sendingRequest = false }
        var req = Msg_FriendshipRequest()
        req.domain = domain
        do {
            let resp = try await ws.request { $0.payload = .reqFriendshipRequest(req) }
            if case .respAck(let ack) = resp.payload, ack.ok {
                showToast("Friend request sent ✅")
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

    var body: some View {
        NavigationStack {
            List {
                Section("Add a friend") {
                    HStack {
                        TextField("friend-domain.example", text: $vm.targetDomain)
                            .autocapitalization(.none)
                            .keyboardType(.URL)
                        Button("Send") { Task { await vm.sendFriendRequest() } }
                            .disabled(vm.sendingRequest || vm.targetDomain.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                }

                Section {
                    if vm.friendships.isEmpty {
                        Text("No friendships yet.").foregroundColor(.secondary)
                    } else {
                        ForEach(vm.friendships, id: \.originProfile.domain) { f in
                            FriendRow(
                                f: f,
                                onChange: { status in Task { await vm.changeStatus(f, to: status) } },
                                onDelete: { Task { await vm.deleteFriendship(f) } }
                            )
                        }
                    }
                } header: {
                    HStack {
                        Text("Friend requests")
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
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
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
    @State private var confirmingDelete = false

    // Issue #25: a pending request can be removed by either side - the
    // sender withdraws it, the receiver declines it.
    private var canDelete: Bool { f.status == .pending }
    private var deleteLabel: String { f.sent ? "Cancel request" : "Delete request" }

    var body: some View {
        HStack(spacing: 12) {
            avatarView(data: f.originProfile.hasImage ? f.originProfile.image : nil, size: 44)
            VStack(alignment: .leading) {
                Text(f.originProfile.name.isEmpty ? "(no name)" : f.originProfile.name).font(.headline)
                Text(f.originProfile.domain.isEmpty ? "(no domain)" : f.originProfile.domain)
                    .font(.caption).foregroundColor(.secondary)
                Text(statusLabel + (f.sent ? " (sent)" : ""))
                    .font(.caption2).foregroundColor(.secondary)
            }
            Spacer()
            if !actionOptions.isEmpty || canDelete {
                Menu {
                    ForEach(actionOptions, id: \.0) { opt in
                        Button(opt.0) { onChange(opt.1) }
                    }
                    if canDelete {
                        Button(deleteLabel, role: .destructive) { confirmingDelete = true }
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
            }
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
    }

    private var statusLabel: String {
        switch f.status {
        case .accepted: return "Accepted"
        case .blocked: return "Blocked"
        default: return "Pending"
        }
    }

    private var actionOptions: [(String, Msg_FriendShipStatus)] {
        // The sender does not decide the status; the receiver does.
        if f.sent { return [] }
        switch f.status {
        case .pending: return [("Accept", .accepted), ("Block", .blocked)]
        case .accepted: return [("Set Pending", .pending), ("Block", .blocked)]
        case .blocked: return [("Accept", .accepted), ("Set Pending", .pending)]
        default: return []
        }
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
