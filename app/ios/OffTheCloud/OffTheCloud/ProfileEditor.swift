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

/// The profile as the section's fields hold it.
struct ProfileFields: Equatable {
    var name = ""
    var bio = ""
    var image: Data?

    /// The device's profile (`device`) read over the fields (`current`): a
    /// field still showing what was last read or saved (`shown`; nothing
    /// yet: every field) takes the device's, and one changed here and not
    /// saved keeps its change. Settings' Form runs the section's .task again
    /// whenever the section comes back on screen (a scroll), and reading
    /// every field again put the device's old photo back over one picked
    /// and not yet saved. As Android's ProfileFields.merge.
    static func merge(current: ProfileFields, shown: ProfileFields?, device: ProfileFields) -> ProfileFields {
        guard let shown else { return device }
        return ProfileFields(name: current.name == shown.name ? device.name : current.name,
                             bio: current.bio == shown.bio ? device.bio : current.bio,
                             image: current.image == shown.image ? device.image : current.image)
    }
}

@MainActor
final class ProfileEditorViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    @Published var name = ""
    @Published var bio = ""
    @Published var imageData: Data?
    @Published var saving = false
    @Published var toast: SettingsText?
    /// What the fields last showed of the device's profile: read from it,
    /// or saved to it (ProfileFields.merge).
    private var shown: ProfileFields?
    /// Bumped by each save that worked: a read asked before it answers with
    /// what the device held before, and is dropped.
    private var saves = 0

    private var fields: ProfileFields { ProfileFields(name: name, bio: bio, image: imageData) }

    func load() async {
        let asked = saves
        do {
            let resp = try await ws.request { $0.payload = .reqGetProfile(Msg_GetProfile()) }
            if case .respProfile(let p) = resp.payload, asked == saves {
                let device = ProfileFields(name: p.name, bio: p.text, image: p.hasImage ? p.image : nil)
                let merged = ProfileFields.merge(current: fields, shown: shown, device: device)
                name = merged.name
                bio = merged.bio
                imageData = merged.image
                shown = device
            }
        } catch {
            // keep whatever was loaded before; the Save button still allows retry
        }
    }

    func save() async {
        saving = true
        defer { saving = false }
        let sent = fields
        var p = Msg_Profile()
        p.name = sent.name
        p.text = sent.bio
        if let image = sent.image { p.image = image }
        do {
            let resp = try await ws.request { $0.payload = .reqSetProfile(p) }
            if case .respAck(let ack) = resp.payload {
                if ack.ok {
                    shown = sent
                    saves += 1
                }
                showToast(ack.ok ? .say { S.appSettingsProfileSaved } : .asIs(ack.errorMsg, or: { S.appSettingsProfileSaveFailed }))
            } else {
                showToast(.say { S.iosSettingsProfileUnexpected })
            }
        } catch {
            showToast(.say { S.appSettingsProfileSaveError })
        }
    }

    /// The crop's "Use photo": the photo is saved at once, with the name and
    /// text as they are. It used to wait for Save, and leaving Settings lost
    /// it (2026-11-05: "When I change the profile photo in the iPhone it
    /// doesn't save it"). Only once the device's profile has been read, so
    /// an unread name and text are never sent as empty; else it waits in the
    /// field for Save.
    func usePhoto(_ data: Data) async {
        if shown == nil { await load() }
        imageData = data
        guard shown != nil else { return }
        await save()
    }

    private func showToast(_ m: SettingsText) {
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
    /// Issue #178: a fresh pick goes through ProfilePhotoCropView first;
    /// vm.imageData only changes once the user taps "Use photo".
    @State private var cropItem: ProfilePhotoCropItem?
    /// Issue #178: "Change photo" offers the phone's library or the
    /// photos already on the OTC device (DevicePhotoPickerView).
    @State private var showPhonePicker = false
    @State private var showDevicePicker = false
    /// A device pick waits here until its sheet has finished dismissing,
    /// then moves to cropItem - presenting the crop while the sheet is
    /// still going away would be dropped.
    @State private var pendingDeviceCrop: ProfilePhotoCropItem?

    var body: some View {
        Section(header: Text(S.appSettingsProfileTitle)) {
            // Issue #23: photo + "Change photo" as a centered block of
            // their own, with name/description stacked below at full
            // section width instead of squeezed into a narrow column
            // beside a small avatar.
            VStack(spacing: 8) {
                avatarView(data: vm.imageData, size: 96)
                Menu(S.appSettingsProfileChangePhoto) {
                    Button {
                        showPhonePicker = true
                    } label: {
                        Label(S.iosSettingsProfileFromIphone, systemImage: "iphone")
                    }
                    Button {
                        showDevicePicker = true
                    } label: {
                        Label(S.appSettingsProfileFromDevice, systemImage: "externaldrive")
                    }
                }
                .font(.footnote)
            }
            .frame(maxWidth: .infinity)

            TextField(S.appSettingsProfileName, text: $vm.name)
                .textFieldStyle(.roundedBorder)
            TextEditor(text: $vm.bio)
                .frame(height: 90)
                .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.secondary.opacity(0.25)))

            Button {
                Task { await vm.save() }
            } label: {
                if vm.saving { ProgressView() } else { Text(S.appSettingsProfileSave) }
            }
            .disabled(vm.saving)

            if let toast = vm.toast {
                Text(toast.text).font(.caption).foregroundColor(.secondary)
            }
        }
        .task { await vm.load() }
        .photosPicker(isPresented: $showPhonePicker, selection: $photoItem, matching: .images)
        .sheet(isPresented: $showDevicePicker, onDismiss: {
            if let item = pendingDeviceCrop {
                pendingDeviceCrop = nil
                cropItem = item
            }
        }) {
            DevicePhotoPickerView { pendingDeviceCrop = $0 }
        }
        .onChange(of: photoItem) { _, newItem in
            guard let newItem else { return }
            Task {
                if let data = try? await newItem.loadTransferable(type: Data.self),
                   let item = await ProfilePhotoCropItem.make(from: data) {
                    cropItem = item
                }
                // Clear the selection so picking the same photo again
                // still fires onChange.
                photoItem = nil
            }
        }
        .fullScreenCover(item: $cropItem) { item in
            ProfilePhotoCropView(image: item.image,
                                 onCancel: { cropItem = nil },
                                 onUse: { data in
                                     cropItem = nil
                                     Task { await vm.usePhoto(data) }
                                 })
        }
    }
}
