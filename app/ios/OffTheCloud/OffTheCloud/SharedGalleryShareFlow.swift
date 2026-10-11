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

/// Bytes as the app shows them: a file's size (Files, its versions) and
/// what a gallery or a link holds, all 64-bit.
func formatByteCount(_ n: Int64) -> String {
    let bytes = Double(n)
    if bytes >= Double(1 << 30) { return decimalText("%.1f GB", bytes / Double(1 << 30)) } // i18n-ignore: a size, kept as written today (sizes keep their formatter)
    if bytes >= Double(1 << 20) { return decimalText("%.1f MB", bytes / Double(1 << 20)) } // i18n-ignore: a size, kept as written today (sizes keep their formatter)
    if bytes >= Double(1 << 10) { return decimalText("%.1f KB", bytes / Double(1 << 10)) } // i18n-ignore: a size, kept as written today (sizes keep their formatter)
    return "\(n) B"
}

/// A number the app writes by hand ("%.1f"), with the decimal mark of the
/// app's language, as Android's String.format writes it: "177,7 KB" in
/// Spanish or German. The language's own tag, not the region's, so English
/// stays "177.7 KB" byte for byte on every phone.
func decimalText(_ format: String, _ value: Double) -> String {
    String(format: format, locale: L10n.shared.current.locale, value)
}

/// The expiry choices; ttlHours as CreateSharedGallery takes them.
enum SharedGalleryExpiry: Int32, CaseIterable, Identifiable {
    case oneDay = 24
    case sevenDays = 168
    case thirtyDays = 720
    var id: Int32 { rawValue }
    /// "1 day", "7 days", "30 days", in the language shown when read.
    var label: String { S.appFilesGalleryDays(count: Int(rawValue / 24)) }
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
        case failed(Failure)
    }

    /// Why the share stopped, worded when it is shown: the device's own
    /// words (it wrote them in the app's language), the phone's, or ours.
    enum Failure {
        case words(String)
        case error(Error)
        case prepare
        case create
        case job
        case noLink

        var text: String {
            switch self {
            case .words(let words): return words
            case .error(let error): return error.localizedDescription
            case .prepare: return S.appFilesGalleryPrepareFailed
            case .create: return S.appFilesGalleryCreateFailed
            case .job: return S.iosFilesGalleryJobFailed
            case .noLink: return S.appFilesGalleryNoLink
            }
        }

        /// The device's words when it gave some, else ours.
        static func device(_ words: String, or ours: Failure) -> Failure {
            words.isEmpty ? ours : .words(words)
        }
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

    /// The description a share starts with, in the language shown then:
    /// from there on it is the owner's text, which they may change.
    static func defaultDescription(_ now: Date = Date()) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = .current
        f.dateFormat = "dd-MM-yyyy HH:mm"
        return S.appFilesGalleryDefaultDescription(date: f.string(from: now))
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
                stage = .failed(.device(resp.errorMessage, or: .prepare))
            }
        } catch {
            stage = .failed(.error(error))
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
                    stage = .failed(.device(resp.errorMessage, or: .create))
                    return
                }
                handle(job)
                if !job.finished { poll(job.jobID) }
            } catch {
                stage = .failed(.error(error))
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
                        self.stage = .failed(.device(resp.errorMessage, or: .job))
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
            stage = .failed(.words(job.error))
        } else if job.link.isEmpty {
            stage = .failed(.noLink)
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
                .navigationTitle(S.appFilesGalleryTitle)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        switch model.stage {
                        case .done:
                            Button(S.commonDone) { model.close() }
                        case .creating, .copying:
                            EmptyView()
                        default:
                            Button(S.commonCancel) { model.close() }
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
                Text(S.iosFilesGalleryCounting).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)

        case .confirm(let p):
            Form {
                Section(footer: Text(S.appFilesGalleryPrivacy)) {
                    Text(S.appFilesGalleryContents(count: Int(p.files), size: formatByteCount(p.bytes)))
                        .fontWeight(.semibold)
                    if p.skipped > 0 {
                        Text(S.iosFilesGallerySkipped(count: Int(p.skipped)))
                            .foregroundStyle(.secondary)
                    }
                }
                Section(header: Text(S.appFilesGalleryDescription)) {
                    TextField(S.appFilesGalleryDescription, text: $model.description)
                }
                Section(footer: Text(model.lowRes
                    ? (p.videos > 0 ? S.appFilesGalleryLowResNoteVideos(count: Int(p.videos)) : S.appFilesGalleryLowResNote)
                    : S.appFilesGalleryFullResNote)) {
                    Toggle(S.appFilesGalleryLowRes, isOn: $model.lowRes)
                }
                Section(header: Text(S.iosFilesGalleryExpires)) {
                    Picker(S.iosFilesGalleryExpires, selection: $model.expiry) {
                        ForEach(SharedGalleryExpiry.allCases) { e in
                            Text(e.label).tag(e)
                        }
                    }
                    .pickerStyle(.segmented)
                }
                Section {
                    Button(S.commonShare) { model.share() }
                        .disabled(p.files == 0 || (model.lowRes && p.files == p.videos))
                    if p.files == 0 {
                        Text(S.iosFilesGalleryNothing)
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }
            }

        case .creating:
            VStack(spacing: 12) {
                ProgressView()
                Text(S.iosFilesGalleryStarting).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)

        case .copying(let job):
            VStack(alignment: .leading, spacing: 12) {
                Text(S.appFilesGalleryCopying(done: Int(job.done), total: Int(job.total)))
                ProgressView(value: job.bytesTotal > 0 ? min(1, Double(job.bytesDone) / Double(job.bytesTotal)) : 0)
                    .progressViewStyle(.linear)
                Text(S.appFilesGalleryBytes(done: formatByteCount(job.bytesDone), total: formatByteCount(job.bytesTotal)))
                    .font(.caption).foregroundStyle(.secondary)
                Text(S.iosFilesGalleryKeepOpen)
                    .font(.caption).foregroundStyle(.secondary)
            }
            .padding()
            .frame(maxWidth: .infinity, maxHeight: .infinity)

        case .done(let link):
            Form {
                Section(footer: Text(S.iosFilesGalleryLinkOnce)) {
                    Text(link)
                        .font(.callout.monospaced())
                        .textSelection(.enabled)
                }
                Section {
                    Button {
                        model.showActivity = true
                    } label: {
                        Label(S.appFilesGalleryShareLink, systemImage: "square.and.arrow.up")
                    }
                    Button {
                        model.copyLink(link)
                    } label: {
                        Label(model.copied ? S.iosFilesGalleryCopied : S.appFilesGalleryCopyLink, systemImage: model.copied ? "checkmark" : "doc.on.doc")
                    }
                }
            }

        case .failed(let failure):
            VStack(spacing: 12) {
                Image(systemName: "exclamationmark.triangle").font(.largeTitle).foregroundStyle(.red)
                Text(failure.text).multilineTextAlignment(.center)
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
