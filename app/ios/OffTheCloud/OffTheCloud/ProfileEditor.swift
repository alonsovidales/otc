// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  ProfileEditor.swift
//  OffTheCloud
//
//  Issue #84: this used to be FriendshipsViewModel/FriendshipsView's own
//  "Your Profile" section - the only place the app could actually edit its
//  own name/bio/photo, bundled in with friend management on the same
//  screen. Split out so it can sit at the top of Settings instead (same
//  reshuffle as the web app's ProfileCard moving into SettingsForm.tsx),
//  leaving FriendshipsView to just be about friends.
//

import SwiftUI
import PhotosUI

@MainActor
final class ProfileEditorViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    @Published var name = ""
    @Published var bio = ""
    @Published var imageData: Data?
    @Published var saving = false
    @Published var toast: String?

    func load() async {
        do {
            let resp = try await ws.request { $0.payload = .reqGetProfile(Msg_GetProfile()) }
            if case .respProfile(let p) = resp.payload {
                name = p.name
                bio = p.text
                imageData = p.hasImage ? p.image : nil
            }
        } catch {
            // keep whatever was loaded before; the Save button still allows retry
        }
    }

    func save() async {
        saving = true
        defer { saving = false }
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

    private func showToast(_ m: String) {
        toast = m
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_500_000_000)
            if self?.toast == m { self?.toast = nil }
        }
    }
}

/// Embedded directly inside SettingsView's own Form, same shape as
/// UsersManagementSection - a Section-returning subview, not a screen of
/// its own.
struct ProfileEditorSection: View {
    @StateObject private var vm = ProfileEditorViewModel()
    @State private var photoItem: PhotosPickerItem?

    var body: some View {
        Section(header: Text("Profile")) {
            // Issue #23: photo + "Change photo" as a centered block of
            // their own, with name/description stacked below at full
            // section width instead of squeezed into a narrow column
            // beside a small avatar.
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
                Task { await vm.save() }
            } label: {
                if vm.saving { ProgressView() } else { Text("Save Profile") }
            }
            .disabled(vm.saving)

            if let toast = vm.toast {
                Text(toast).font(.caption).foregroundColor(.secondary)
            }
        }
        .task { await vm.load() }
        .onChange(of: photoItem) { _, newItem in
            Task {
                if let data = try? await newItem?.loadTransferable(type: Data.self) {
                    vm.imageData = data
                }
            }
        }
    }
}
