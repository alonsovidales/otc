// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SharedGalleryShareFlow.swift
//  OffTheCloud
//
//  Issue #180: "Share as Gallery" - the owner's side of a shared gallery.
//  An image group (Images) or a folder (Files) is previewed on the device
//  (PreviewSharedGallery: how many photos and videos, how big, how many
//  other files are left out), confirmed with a description and an expiry,
//  then copied and re-encrypted under a key of the link's own
//  (CreateSharedGallery, polled with GetSharedGalleryJob). The link that
//  comes back is shown once: the device never stores its secret, so this
//  sheet is the only chance to send or copy it. Visitors open it in a
//  browser (the web app's /shared page). The Android counterpart is
//  SharedGalleryShareFlow.kt.

import SwiftUI
import UIKit

/// Bytes the way the rest of the app shows them (FilesExplorerView's
/// formatBytes), for the 64-bit sizes a gallery or a link can reach.
func formatByteCount(_ n: Int64) -> String {
    let bytes = Double(n)
    if bytes >= Double(1 << 30) { return String(format: "%.1f GB", bytes / Double(1 << 30)) }
    if bytes >= Double(1 << 20) { return String(format: "%.1f MB", bytes / Double(1 << 20)) }
    if bytes >= Double(1 << 10) { return String(format: "%.1f KB", bytes / Double(1 << 10)) }
    return "\(n) B"
}

/// The expiry choices; ttlHours as CreateSharedGallery takes them.
enum SharedGalleryExpiry: Int32, CaseIterable, Identifiable {
    case oneDay = 24
    case sevenDays = 168
    case thirtyDays = 720
    var id: Int32 { rawValue }
    var label: String {
        switch self {
        case .oneDay: return "1 day"
        case .sevenDays: return "7 days"
        case .thirtyDays: return "30 days"
        }
    }
}

@MainActor
final class SharedGalleryShareModel: ObservableObject {
    enum Stage {
        case idle
        case previewing
        case confirm(Msg_SharedGalleryPreview)
        case creating
        case copying(Msg_SharedGalleryJob)
        case done(String)
        case failed(String)
    }

    private let ws = OTCConnection.shared

    @Published var stage: Stage = .idle
    @Published var description = ""
    @Published var expiry: SharedGalleryExpiry = .sevenDays
    /// Only small copies of the photos (their thumbnails), no videos.
    @Published var lowRes = false
    @Published var showActivity = false
    @Published var copied = false

    private var source = Msg_SharedGallerySource()
    private var pollTask: Task<Void, Never>?

    var isPresented: Bool {
        if case .idle = stage { return false }
        return true
    }

    /// True while the device is copying: closing the sheet then would lose
    /// the link, which exists nowhere else.
    var busy: Bool {
        switch stage {
        case .creating, .copying: return true
        default: return false
        }
    }

    static func defaultDescription(_ now: Date = Date()) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = .current
        f.dateFormat = "dd-MM-yyyy HH:mm"
        return "Shared Media \(f.string(from: now))"
    }

    func start(_ source: Msg_SharedGallerySource) {
        pollTask?.cancel()
        self.source = source
        description = Self.defaultDescription()
        expiry = .sevenDays
        copied = false
        showActivity = false
        stage = .previewing
        Task { await loadPreview() }
    }

    private func loadPreview() async {
        var req = Msg_PreviewSharedGallery()
        req.source = source
        do {
            let resp = try await ws.request { $0.payload = .reqPreviewSharedGallery(req) }
            if case .respSharedGalleryPreview(let p) = resp.payload {
                stage = .confirm(p)
            } else {
                stage = .failed(resp.errorMessage.isEmpty ? "Could not prepare the gallery" : resp.errorMessage)
            }
        } catch {
            stage = .failed(error.localizedDescription)
        }
    }

    func share() {
        stage = .creating
        var req = Msg_CreateSharedGallery()
        req.source = source
        req.description_p = description.trimmingCharacters(in: .whitespacesAndNewlines)
        req.ttlHours = expiry.rawValue
        req.lowRes = lowRes
        Task {
            do {
                let resp = try await ws.request { $0.payload = .reqCreateSharedGallery(req) }
                guard case .respSharedGalleryJob(let job) = resp.payload else {
                    stage = .failed(resp.errorMessage.isEmpty ? "Could not create the gallery" : resp.errorMessage)
                    return
                }
                handle(job)
                if !job.finished { poll(job.jobID) }
            } catch {
                stage = .failed(error.localizedDescription)
            }
        }
    }

    private func poll(_ jobID: String) {
        pollTask?.cancel()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 700_000_000)
                guard let self, !Task.isCancelled else { return }
                var req = Msg_GetSharedGalleryJob()
                req.jobID = jobID
                // A dropped request (the socket reconnecting) just waits
                // for the next tick; the job carries on on the device.
                guard let resp = try? await self.ws.request({ $0.payload = .reqGetSharedGalleryJob(req) }) else { continue }
                guard case .respSharedGalleryJob(let job) = resp.payload else {
                    if resp.error {
                        self.stage = .failed(resp.errorMessage.isEmpty ? "The gallery could not be created" : resp.errorMessage)
                        return
                    }
                    continue
                }
                self.handle(job)
                if job.finished { return }
            }
        }
    }

    private func handle(_ job: Msg_SharedGalleryJob) {
        guard job.finished else {
            stage = .copying(job)
            return
        }
        if !job.error.isEmpty {
            stage = .failed(job.error)
        } else if job.link.isEmpty {
            stage = .failed("The device did not return a link")
        } else {
            stage = .done(job.link)
            showActivity = true
        }
    }

    func copyLink(_ link: String) {
        UIPasteboard.general.string = link
        copied = true
    }

    func close() {
        pollTask?.cancel()
        pollTask = nil
        showActivity = false
        stage = .idle
    }
}

/// The sheet itself: preview -> confirm -> progress -> the link.
struct SharedGalleryShareSheet: View {
    @ObservedObject var model: SharedGalleryShareModel

    var body: some View {
        NavigationStack {
            content
                .navigationTitle("Share as Gallery")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        switch model.stage {
                        case .done:
                            Button("Done") { model.close() }
                        case .creating, .copying:
                            EmptyView()
                        default:
                            Button("Cancel") { model.close() }
                        }
                    }
                }
        }
        .interactiveDismissDisabled(model.busy)
        .sheet(isPresented: $model.showActivity) {
            if case .done(let link) = model.stage {
                ActivityView(items: [URL(string: link).map { $0 as Any } ?? link])
            }
        }
    }

    @ViewBuilder
    private var content: some View {
        switch model.stage {
        case .idle, .previewing:
            VStack(spacing: 12) {
                ProgressView()
                Text("Counting photos and videos…").foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)

        case .confirm(let p):
            Form {
                Section(footer: Text("They are copied and re-encrypted with a key of their own. Only people with the link can see them, until it expires.")) {
                    Text("\(p.files) photos and videos, \(formatByteCount(p.bytes))")
                        .fontWeight(.semibold)
                    if p.skipped > 0 {
                        Text("\(p.skipped) other files are not included")
                            .foregroundStyle(.secondary)
                    }
                }
                Section(header: Text("Description")) {
                    TextField("Description", text: $model.description)
                }
                Section(footer: Text(model.lowRes
                    ? "Only small copies of the photos are shared (about 1000 pixels wide); the originals never leave your device." + (p.videos > 0 ? " \(p.videos) video\(p.videos == 1 ? " is" : "s are") left out." : "")
                    : "The full-size originals are shared.")) {
                    Toggle("Low resolution only", isOn: $model.lowRes)
                }
                Section(header: Text("Expires after")) {
                    Picker("Expires after", selection: $model.expiry) {
                        ForEach(SharedGalleryExpiry.allCases) { e in
                            Text(e.label).tag(e)
                        }
                    }
                    .pickerStyle(.segmented)
                }
                Section {
                    Button("Share") { model.share() }
                        .disabled(p.files == 0 || (model.lowRes && p.files == p.videos))
                    if p.files == 0 {
                        Text("There are no photos or videos to share here.")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }
            }

        case .creating:
            VStack(spacing: 12) {
                ProgressView()
                Text("Starting…").foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)

        case .copying(let job):
            VStack(alignment: .leading, spacing: 12) {
                Text("Copying \(job.done) of \(job.total)")
                ProgressView(value: job.bytesTotal > 0 ? min(1, Double(job.bytesDone) / Double(job.bytesTotal)) : 0)
                    .progressViewStyle(.linear)
                Text("\(formatByteCount(job.bytesDone)) of \(formatByteCount(job.bytesTotal))")
                    .font(.caption).foregroundStyle(.secondary)
                Text("Keep this screen open: the link is shown here once the copy is done.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            .padding()
            .frame(maxWidth: .infinity, maxHeight: .infinity)

        case .done(let link):
            Form {
                Section(footer: Text("This link is shown only now - the device never stores it. Send or copy it before closing.")) {
                    Text(link)
                        .font(.callout.monospaced())
                        .textSelection(.enabled)
                }
                Section {
                    Button {
                        model.showActivity = true
                    } label: {
                        Label("Share Link", systemImage: "square.and.arrow.up")
                    }
                    Button {
                        model.copyLink(link)
                    } label: {
                        Label(model.copied ? "Copied" : "Copy Link", systemImage: model.copied ? "checkmark" : "doc.on.doc")
                    }
                }
            }

        case .failed(let message):
            VStack(spacing: 12) {
                Image(systemName: "exclamationmark.triangle").font(.largeTitle).foregroundStyle(.red)
                Text(message).multilineTextAlignment(.center)
            }
            .padding()
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
}

/// Attaches the flow to a view: set `source` to start it (it is reset to
/// nil straight away, so the same source can be shared again).
private struct SharedGalleryShareFlowModifier: ViewModifier {
    @Binding var source: Msg_SharedGallerySource?
    @StateObject private var model = SharedGalleryShareModel()

    func body(content: Content) -> some View {
        content
            .onChange(of: source) { _, newValue in
                guard let s = newValue else { return }
                source = nil
                model.start(s)
            }
            .sheet(isPresented: Binding(
                get: { model.isPresented },
                set: { if !$0 && !model.busy { model.close() } }
            )) {
                SharedGalleryShareSheet(model: model)
            }
    }
}

extension View {
    func sharedGalleryShareFlow(source: Binding<Msg_SharedGallerySource?>) -> some View {
        modifier(SharedGalleryShareFlowModifier(source: source))
    }
}

extension Msg_SharedGallerySource {
    static func group(_ id: String) -> Msg_SharedGallerySource {
        var s = Msg_SharedGallerySource()
        s.groupID = id
        return s
    }
    static func paths(_ paths: [String]) -> Msg_SharedGallerySource {
        var s = Msg_SharedGallerySource()
        s.paths = paths
        return s
    }
    static func directory(_ path: String) -> Msg_SharedGallerySource {
        var s = Msg_SharedGallerySource()
        s.directory = path
        return s
    }
}
