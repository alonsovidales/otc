//
//  FriendshipsView.swift
//  OffTheCloud
//
//  Native port of the web app's Profile tab (web/src/components/FriendshipsManager.tsx):
//  edit your own profile, send a friend request by domain, and manage
//  incoming/outgoing friendships.

import SwiftUI
import PhotosUI

@MainActor
final class FriendshipsViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    @Published var name = ""
    @Published var bio = ""
    @Published var imageData: Data?
    @Published var savingProfile = false

    @Published var targetDomain = ""
    @Published var sendingRequest = false

    @Published var friendships: [Msg_Friendship] = []
    @Published var loadingFriendships = false

    @Published var toast: String?

    func loadAll() async {
        async let profile: Void = loadProfile()
        async let friends: Void = reloadFriendships()
        _ = await (profile, friends)
    }

    func loadProfile() async {
        do {
            let resp = try await ws.request { $0.payload = .reqGetProfile(Msg_GetProfile()) }
            if case .respProfile(let p) = resp.payload {
                name = p.name
                bio = p.text
                imageData = p.hasImage ? p.image : nil
            }
        } catch {
            // keep whatever was loaded before; the toolbar will still allow retry
        }
    }

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

    func saveProfile() async {
        savingProfile = true
        defer { savingProfile = false }
        var p = Msg_Profile()
        p.name = name
        p.text = bio
        if let imageData { p.image = imageData }
        do {
            let resp = try await ws.request { $0.payload = .reqSetProfile(p) }
            if case .respAck(let ack) = resp.payload {
                showToast(ack.ok ? "Profile updated ✅" : (ack.errorMsg.isEmpty ? "Profile update failed" : ack.errorMsg))
            } else {
                showToast("Unexpected response while saving profile")
            }
        } catch {
            showToast("Error saving profile")
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
    @State private var photoItem: PhotosPickerItem?

    var body: some View {
        NavigationView {
            List {
                Section("Your Profile") {
                    // Issue #23: photo + "Change photo" as a centered block
                    // of their own, with name/description stacked below at
                    // full section width instead of squeezed into a narrow
                    // column beside a small avatar.
                    VStack(spacing: 8) {
                        avatarView(data: vm.imageData, size: 96)
                        PhotosPicker("Change photo", selection: $photoItem, matching: .images)
                            .font(.footnote)
                    }
                    .frame(maxWidth: .infinity)

                    TextField("Name", text: $vm.name)
                        .textFieldStyle(.roundedBorder)
                    TextEditor(text: $vm.bio)
                        .frame(height: 90)
                        .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.secondary.opacity(0.25)))

                    Button {
                        Task { await vm.saveProfile() }
                    } label: {
                        if vm.savingProfile { ProgressView() } else { Text("Save Profile") }
                    }
                    .disabled(vm.savingProfile)
                }

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
                            FriendRow(f: f, onChange: { status in Task { await vm.changeStatus(f, to: status) } })
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
            // No nav title (issue #19): the tab bar already labels this
            // screen "Profile". Still .inline (not the default .large) so
            // there's no big empty title bar left behind.
            .navigationBarTitleDisplayMode(.inline)
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
        .task { await vm.loadAll() }
        .onChange(of: photoItem) { _, newItem in
            Task {
                if let data = try? await newItem?.loadTransferable(type: Data.self) {
                    vm.imageData = data
                }
            }
        }
    }
}

private struct FriendRow: View {
    let f: Msg_Friendship
    let onChange: (Msg_FriendShipStatus) -> Void

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
            if !f.sent {
                Menu {
                    ForEach(actionOptions, id: \.0) { opt in
                        Button(opt.0) { onChange(opt.1) }
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
            }
        }
        .padding(.vertical, 4)
    }

    private var statusLabel: String {
        switch f.status {
        case .accepted: return "Accepted"
        case .blocked: return "Blocked"
        default: return "Pending"
        }
    }

    private var actionOptions: [(String, Msg_FriendShipStatus)] {
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
