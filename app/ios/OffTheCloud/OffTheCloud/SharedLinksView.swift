// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SharedLinksView.swift
//  OffTheCloud
//
//  Issue #180: Settings -> Shared Links. Every share link the device keeps
//  (galleries, and the zip archives of Share in Files/Images) with when it
//  was made, when it expires, how often it was opened, what it takes on
//  disk - and a delete that removes the shared copies and stops the link
//  working. The links themselves can't be shown again: the device never
//  stores their secret. The Android counterpart is SharedLinksView.kt.

import SwiftUI

@MainActor
final class SharedLinksViewModel: ObservableObject {
    private let ws = OTCConnection.shared

    @Published var links: [Msg_SharedLinkInfo] = []
    @Published var loading = false
    @Published var loaded = false
    @Published var error: SettingsText?
    @Published var confirmDelete: Msg_SharedLinkInfo?

    func load() async {
        loading = true
        defer { loading = false }
        do {
            let resp = try await ws.request { $0.payload = .reqListSharedLinks(Msg_ListSharedLinks()) }
            if case .respSharedLinks(let l) = resp.payload {
                links = l.links
                error = nil
            } else {
                error = .asIs(resp.errorMessage, or: { S.appSettingsLinksLoadFailed })
            }
        } catch {
            self.error = .error(error)
        }
        loaded = true
    }

    func delete(_ link: Msg_SharedLinkInfo) async {
        var req = Msg_DeleteSharedLink()
        req.uuid = link.uuid
        do {
            let resp = try await ws.request { $0.payload = .reqDeleteSharedLink(req) }
            if resp.error {
                error = .asIs(resp.errorMessage, or: { S.appSettingsLinksDeleteFailed })
            }
        } catch {
            self.error = .error(error)
        }
        await load()
    }
}

struct SharedLinksView: View {
    @StateObject private var vm = SharedLinksViewModel()

    var body: some View {
        List {
            if let error = vm.error {
                Text(error.text).font(.caption).foregroundColor(.red)
            }
            if vm.loaded && vm.links.isEmpty && vm.error == nil {
                Text(S.appSettingsLinksEmpty).foregroundStyle(.secondary)
            }
            ForEach(vm.links, id: \.uuid) { link in
                SharedLinkRow(link: link) { vm.confirmDelete = link }
            }
        }
        .overlay {
            if vm.loading && !vm.loaded { ProgressView() }
        }
        .navigationTitle(S.appSettingsLinksTitle)
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await vm.load() }
        .task { await vm.load() }
        .confirmationDialog(
            S.appSettingsLinksDeleteTitle,
            isPresented: Binding(get: { vm.confirmDelete != nil }, set: { if !$0 { vm.confirmDelete = nil } }),
            titleVisibility: .visible,
            presenting: vm.confirmDelete
        ) { link in
            Button(S.commonDelete, role: .destructive) { Task { await vm.delete(link) } }
            Button(S.commonCancel, role: .cancel) {}
        } message: { _ in
            Text(S.iosSettingsLinksDeleteMessage)
        }
    }
}

private struct SharedLinkRow: View {
    let link: Msg_SharedLinkInfo
    let onDelete: () -> Void

    private var title: String {
        if !link.description_p.isEmpty { return link.description_p }
        return link.kind == "archive" ? S.appSettingsLinksUntitledZip : S.appSettingsLinksUntitledGallery
    }

    private var opened: String {
        guard link.opens > 0 else { return S.appSettingsLinksNeverOpened }
        guard link.hasLastOpened else { return S.appSettingsLinksOpened(count: Int(link.opens)) }
        let rel = RelativeDateTimeFormatter()
        rel.unitsStyle = .full
        rel.locale = L10n.shared.locale
        let last = rel.localizedString(for: link.lastOpened.date, relativeTo: Date())
        return S.appSettingsLinksOpenedLast(count: Int(link.opens), last: last)
    }

    /// A date and time as the row writes them ("Oct 11, 2026 at 10:30"), in the app's locale.
    private func when(_ date: Date) -> String {
        date.formatted(Date.FormatStyle(date: .abbreviated, time: .shortened).locale(L10n.shared.locale))
    }

    var body: some View {
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 3) {
                Text(title).fontWeight(.semibold).lineLimit(2)
                if link.hasCreated {
                    Text(S.appSettingsLinksCreated(date: when(link.created.date)))
                        .font(.caption).foregroundStyle(.secondary)
                }
                if link.hasExpires {
                    Text(S.appSettingsLinksExpires(date: when(link.expires.date)))
                        .font(.caption).foregroundStyle(.secondary)
                }
                Text(opened).font(.caption).foregroundStyle(.secondary)
                Text(S.appSettingsLinksSizeFiles(size: formatByteCount(link.bytes), count: Int(link.files)))
                    .font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            Button(role: .destructive, action: onDelete) {
                Image(systemName: "trash")
                    .frame(width: 28, height: 28)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.borderless)
            .foregroundColor(.red)
            .accessibilityLabel(S.appSettingsLinksDeleteLink)
        }
        .padding(.vertical, 2)
    }
}
