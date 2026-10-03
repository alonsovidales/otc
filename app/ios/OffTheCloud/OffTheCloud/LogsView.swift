// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  LogsView.swift
//  OffTheCloud
//
//  The device's own logs (the service's and the updater's), followed live,
//  with a way to share them or send them to us for help.
//
//  Primary-only, like the update, users and Tailscale sections: the logs
//  are the whole machine's, not one user's.
//
//  The live view is a loop of GetLogs: the first request takes the last
//  64 KB at once, every one after it asks from where the last ended with
//  wait_seconds set, so the device holds it until new lines are written.
//  Same as the web's LogsPanel and Android's LogsView.

import SwiftUI

/// A piece of the shown log. The text is kept in pieces of about 16 KB,
/// each its own Text in a lazy stack: one Text holding a megabyte of log
/// takes seconds to lay out on every new line.
struct LogChunk: Identifiable {
    let id: Int
    let text: String
}

@MainActor
final class LogsViewModel: ObservableObject {
    @Published var source = "app"
    /// Finished pieces, each ending at a line break.
    @Published private(set) var chunks: [LogChunk] = []
    /// What came after the last finished piece.
    @Published private(set) var tail = ""
    @Published private(set) var error: String?
    @Published private(set) var paused = false

    @Published var sending = false
    @Published var sendResult: String?
    @Published var sendError: String?

    private let ws = OTCConnection.shared
    private var loop: Task<Void, Never>?
    /// Bumped whenever a loop is started or stopped, so the answer to a
    /// request a stale loop still had in flight is dropped.
    private var generation = 0
    /// Where the next request reads from; -1 = the end of the log.
    private var offset: Int64 = -1
    private var nextChunkId = 0
    private var shownBytes = 0

    private static let maxShownBytes = 1_000_000
    private static let chunkBytes = 16_384

    var isEmpty: Bool { chunks.isEmpty && tail.isEmpty }

    /// Everything shown, for Share.
    var fullText: String { chunks.map(\.text).joined() + tail }

    /// Starts following the log from its last part, dropping what is shown.
    func restart() {
        stop()
        chunks = []
        tail = ""
        shownBytes = 0
        offset = -1
        error = nil
        paused = false
        start()
    }

    func togglePause() {
        if paused {
            paused = false
            start()
        } else {
            paused = true
            stop()
        }
    }

    /// Called when the screen appears: continues where it left off, unless
    /// the user paused it.
    func resume() {
        guard !paused, loop == nil else { return }
        start()
    }

    func stop() {
        generation += 1
        loop?.cancel()
        loop = nil
    }

    private func start() {
        generation += 1
        let gen = generation
        loop = Task { [weak self] in await self?.run(gen) }
    }

    private func run(_ gen: Int) async {
        let source = self.source
        while !Task.isCancelled && gen == generation {
            let from = offset
            let first = from < 0
            do {
                let resp = try await ws.request { e in
                    var m = Msg_GetLogs()
                    m.source = source
                    m.offset = from
                    m.maxBytes = first ? 65536 : 262144
                    m.waitSeconds = first ? 0 : 25
                    e.payload = .reqGetLogs(m)
                }
                guard !Task.isCancelled, gen == generation else { return }
                if resp.error {
                    throw NSError(domain: "logs", code: -1, userInfo: [
                        NSLocalizedDescriptionKey: resp.errorMessage.isEmpty ? "Could not read the logs." : resp.errorMessage,
                    ])
                }
                guard case .respLogs(let logs) = resp.payload else {
                    throw NSError(domain: "logs", code: -1, userInfo: [
                        NSLocalizedDescriptionKey: "Could not read the logs.",
                    ])
                }
                error = nil
                // Smaller than where we asked from: the log was rotated and
                // the text starts again from its beginning.
                if !first && logs.size < from {
                    append((tail.isEmpty || tail.hasSuffix("\n") ? "" : "\n") + "— log rotated —\n")
                }
                append(logs.text)
                offset = logs.nextOffset
            } catch {
                guard !Task.isCancelled, gen == generation else { return }
                self.error = error.localizedDescription
                try? await Task.sleep(for: .seconds(5))
            }
        }
    }

    private func append(_ text: String) {
        guard !text.isEmpty else { return }
        tail += text
        shownBytes += text.utf8.count
        var newChunks: [LogChunk] = []
        // Seal the tail into a piece once it is big enough, at its last
        // line break (a single huge line just keeps growing the tail).
        while tail.utf8.count >= Self.chunkBytes,
              let nl = tail.lastIndex(of: "\n") {
            let cut = tail.index(after: nl)
            newChunks.append(LogChunk(id: nextChunkId, text: String(tail[..<cut])))
            nextChunkId += 1
            tail = String(tail[cut...])
        }
        var all = chunks + newChunks
        // Keep about a megabyte: drop the oldest pieces.
        while shownBytes > Self.maxShownBytes, !all.isEmpty {
            shownBytes -= all.removeFirst().text.utf8.count
        }
        if shownBytes > Self.maxShownBytes {
            // Only the tail is left and it is still too big: keep its end,
            // from a line break when there is one.
            let bytes = tail.utf8
            let start = bytes.index(bytes.startIndex, offsetBy: bytes.count - Self.maxShownBytes)
            let from = bytes[start...].firstIndex(of: UInt8(ascii: "\n")).map { bytes.index(after: $0) } ?? start
            tail = String(decoding: bytes[from...], as: UTF8.self)
            shownBytes = tail.utf8.count
        }
        if !newChunks.isEmpty || all.count != chunks.count { chunks = all }
    }

    func send(note: String) async -> Bool {
        sending = true
        sendResult = nil
        sendError = nil
        defer { sending = false }
        do {
            let resp = try await ws.request { e in
                var m = Msg_SendLogs()
                m.note = note.trimmingCharacters(in: .whitespacesAndNewlines)
                e.payload = .reqSendLogs(m)
            }
            if case .respAck(let ack) = resp.payload, ack.ok, !resp.error {
                sendResult = "Sent - we'll answer at your account's email."
                return true
            }
            if !resp.errorMessage.isEmpty {
                sendError = resp.errorMessage
            } else if case .respAck(let ack) = resp.payload, !ack.errorMsg.isEmpty {
                sendError = ack.errorMsg
            } else {
                sendError = "Could not send the logs."
            }
        } catch {
            sendError = error.localizedDescription
        }
        return false
    }
}

struct LogsView: View {
    @StateObject private var vm = LogsViewModel()
    @State private var atBottom = true
    @State private var shareText: String?
    @State private var showSend = false

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 10) {
                Label("Logs can include file and folder names, search words, Wi-Fi network names and your device's addresses. Check them before you share them.", systemImage: "exclamationmark.triangle")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Picker("Log", selection: $vm.source) {
                    Text("Device").tag("app")
                    Text("Updates").tag("update")
                }
                .pickerStyle(.segmented)
                if let error = vm.error {
                    Text(error).font(.caption).foregroundStyle(.red)
                }
                if let result = vm.sendResult {
                    Text(result).font(.caption).foregroundStyle(.secondary)
                }
            }
            .padding()

            Divider()

            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        ForEach(vm.chunks) { chunk in
                            Text(chunk.text.hasSuffix("\n") ? String(chunk.text.dropLast()) : chunk.text)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        if !vm.tail.isEmpty {
                            Text(vm.tail.hasSuffix("\n") ? String(vm.tail.dropLast()) : vm.tail)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        if vm.isEmpty {
                            Text(vm.error == nil ? "Loading…" : "")
                                .foregroundStyle(.secondary)
                        }
                        Color.clear.frame(height: 1).id("bottom")
                    }
                    .font(.system(.caption, design: .monospaced))
                    .textSelection(.enabled)
                    .padding(.horizontal)
                    .padding(.vertical, 8)
                }
                .onScrollGeometryChange(for: Bool.self) { geo in
                    geo.contentOffset.y + geo.containerSize.height >= geo.contentSize.height - 40
                } action: { _, isAtBottom in
                    atBottom = isAtBottom
                }
                .onChange(of: vm.tail) { _, _ in
                    if atBottom { proxy.scrollTo("bottom", anchor: .bottom) }
                }
                .onChange(of: vm.chunks.last?.id) { _, _ in
                    if atBottom { proxy.scrollTo("bottom", anchor: .bottom) }
                }
            }
        }
        .navigationTitle("Logs")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItemGroup(placement: .topBarTrailing) {
                Button {
                    vm.togglePause()
                } label: {
                    Label(vm.paused ? "Resume" : "Pause", systemImage: vm.paused ? "play.fill" : "pause.fill")
                }
                Button {
                    shareText = vm.fullText
                } label: {
                    Label("Share", systemImage: "square.and.arrow.up")
                }
                .disabled(vm.isEmpty)
                Button {
                    showSend = true
                } label: {
                    Label("Send to us", systemImage: "paperplane")
                }
            }
        }
        .onChange(of: vm.source) { _, _ in
            atBottom = true
            vm.restart()
        }
        .onAppear { vm.resume() }
        .onDisappear { vm.stop() }
        .sheet(isPresented: Binding(
            get: { shareText != nil },
            set: { if !$0 { shareText = nil } }
        )) {
            if let text = shareText { ActivityView(items: [text]) }
        }
        .sheet(isPresented: $showSend) {
            SendLogsSheet(vm: vm)
        }
    }
}

private struct SendLogsSheet: View {
    @ObservedObject var vm: LogsViewModel
    @Environment(\.dismiss) private var dismiss
    @State private var note = ""

    var body: some View {
        NavigationStack {
            Form {
                Section(
                    header: Text("Note (optional)"),
                    footer: Text("Sends the last part of your device's logs, your note and your account's email to info@off-the.cloud, so we can help. Logs can include file and folder names, search words, Wi-Fi network names and your device's addresses.")
                ) {
                    TextEditor(text: $note)
                        .frame(minHeight: 120)
                }
                if let error = vm.sendError {
                    Section {
                        Text(error).font(.caption).foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle("Send to us")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(vm.sending)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(vm.sending ? "Sending…" : "Send") {
                        Task {
                            if await vm.send(note: note) { dismiss() }
                        }
                    }
                    .disabled(vm.sending)
                }
            }
        }
        .onAppear { vm.sendError = nil }
        .interactiveDismissDisabled(vm.sending)
    }
}

@MainActor
final class LogsSectionViewModel: ObservableObject {
    @Published var isPrimary = false

    func load() async {
        do {
            let resp = try await OTCConnection.shared.request { e in
                e.payload = .reqGetInstanceRole(Msg_ReqGetInstanceRole())
            }
            if case .respInstanceRole(let role) = resp.payload { isPrimary = role.isPrimary }
        } catch {
            isPrimary = false
        }
    }
}

/// The Settings row that opens LogsView - renders nothing on a non-primary
/// instance, the same rule as UpdateSection.
struct LogsSection: View {
    @StateObject private var vm = LogsSectionViewModel()

    var body: some View {
        if vm.isPrimary {
            Section(header: Text("Troubleshooting")) {
                NavigationLink("Logs") { LogsView() }
            }
            .task { await vm.load() }
        } else {
            // Nothing to show, but the role still has to be asked for.
            Color.clear.frame(height: 0).task { await vm.load() }
        }
    }
}
