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
    @Published var error: String?
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
                error = resp.errorMessage.isEmpty ? "Could not load the shared links" : resp.errorMessage
            }
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
    }

    func delete(_ link: Msg_SharedLinkInfo) async {
        var req = Msg_DeleteSharedLink()
        req.uuid = link.uuid
        do {
            let resp = try await ws.request { $0.payload = .reqDeleteSharedLink(req) }
            if resp.error {
                error = resp.errorMessage.isEmpty ? "Could not delete the link" : resp.errorMessage
            }
        } catch {
            self.error = error.localizedDescription
        }
        await load()
    }
}

struct SharedLinksView: View {
    @StateObject private var vm = SharedLinksViewModel()

    var body: some View {
        List {
            if let error = vm.error {
                Text(error).font(.caption).foregroundColor(.red)
            }
            if vm.loaded && vm.links.isEmpty && vm.error == nil {
                Text("No shared links").foregroundStyle(.secondary)
            }
            ForEach(vm.links, id: \.uuid) { link in
                SharedLinkRow(link: link) { vm.confirmDelete = link }
            }
        }
        .overlay {
            if vm.loading && !vm.loaded { ProgressView() }
        }
        .navigationTitle("Shared Links")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await vm.load() }
        .task { await vm.load() }
        .confirmationDialog(
            "Delete this link?",
            isPresented: Binding(get: { vm.confirmDelete != nil }, set: { if !$0 { vm.confirmDelete = nil } }),
            titleVisibility: .visible,
            presenting: vm.confirmDelete
        ) { link in
            Button("Delete", role: .destructive) { Task { await vm.delete(link) } }
            Button("Cancel", role: .cancel) {}
        } message: { _ in
            Text("Delete this link? The shared copies are deleted and the link stops working.")
        }
    }
}

private struct SharedLinkRow: View {
    let link: Msg_SharedLinkInfo
    let onDelete: () -> Void

    private var title: String {
        if !link.description_p.isEmpty { return link.description_p }
        return link.kind == "archive" ? "Shared files (zip)" : "Shared gallery"
    }

    private var opened: String {
        guard link.opens > 0 else { return "Never opened" }
        var s = "Opened \(link.opens) \(link.opens == 1 ? "time" : "times")"
        if link.hasLastOpened {
            let rel = RelativeDateTimeFormatter()
            rel.unitsStyle = .full
            s += ", last \(rel.localizedString(for: link.lastOpened.date, relativeTo: Date()))"
        }
        return s
    }

    var body: some View {
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 3) {
                Text(title).fontWeight(.semibold).lineLimit(2)
                if link.hasCreated {
                    Text("Created \(link.created.date.formatted(date: .abbreviated, time: .shortened))")
                        .font(.caption).foregroundStyle(.secondary)
                }
                if link.hasExpires {
                    Text("Expires \(link.expires.date.formatted(date: .abbreviated, time: .shortened))")
                        .font(.caption).foregroundStyle(.secondary)
                }
                Text(opened).font(.caption).foregroundStyle(.secondary)
                Text("\(formatByteCount(link.bytes)) · \(link.files) \(link.files == 1 ? "file" : "files")")
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
            .accessibilityLabel("Delete link")
        }
        .padding(.vertical, 2)
    }
}
