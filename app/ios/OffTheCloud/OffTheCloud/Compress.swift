// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  Compress.swift
//  OffTheCloud
//
//  Compress (2026-11-08): the photos and videos picked in Images or in
//  Settings › Largest files made smaller on the device - a video re-encoded
//  at 1080p or 720p, or as HEVC at its own size; a large photo shrunk to
//  about 12 megapixels, a big JPEG saved again at a lower quality, a PNG
//  saved as JPEG - and the originals' bytes deleted to free the space
//  (GetCompressOptions, CompressFiles and GetCompressJob in messages.proto:
//  their comments are the contract). The web and the Android app follow
//  the same rules, tested alike: the case ids C (the dialog), J (the job),
//  L (the list) and R (the viewer) are web/scripts/compress-test.mjs's,
//  CompressTests.swift's and CompressTest.kt's.
//    - The dialog asks the device what it can do for each file and what it
//      would weigh (at most 500 files), and shows the choices: a radio for
//      the videos (1080p, 720p, HEVC or as they are), a switch per photo
//      preset, each with how many files it takes and their sizes now and
//      after; the files it can't make smaller, each with the device's
//      reason; what the choices would free, that the originals go for good,
//      and Compress.
//    - Compress starts a job the device works through in the background
//      (Settings' Background work shows it too). It is asked about every
//      2 s while the dialog shows it, and every 10 s while only the screen
//      behind follows it (the dialog closed).
//    - Each file compressed takes the old one's place in Images and the
//      list: a new hash, and a new name for a PNG. The list is read again
//      once the job is over.
//    - A compressed file (File.original_removed) has no Revert to Original
//      in the viewer, and its Info says it was compressed.
//

import SwiftUI

/// What the dialog offers and sends, and what the device's answers mean:
/// pure, so the three apps' cases can be tested alike.
enum CompressRules {
    /// The most GetCompressOptions takes at once: the first 500 picked are
    /// asked about (the device refuses more, "too_many", in its words).
    static let maxItems = 500

    /// The videos' presets, in the radio's order.
    static let videoPresets = ["video_1080p", "video_720p", "video_hevc"]
    /// The photos' presets, strongest first: the order a photo takes them in.
    static let photoPresets = ["photo_12mp", "jpeg_resave", "png_to_jpeg"]
    /// The radio's "Leave as they are": no video is compressed.
    static let noVideo = "video_none"

    static func isVideo(_ preset: String) -> Bool { videoPresets.contains(preset) }

    /// What GetCompressOptions is asked about: the files picked, as shown
    /// (path and content), once each, the first 500 in the order given.
    static func items(_ picks: [(path: String, hash: String)]) -> [Msg_CompressItem] {
        var seen = Set<String>()
        return picks.filter { seen.insert($0.path).inserted }.prefix(maxItems).map { p in
            var i = Msg_CompressItem()
            i.path = p.path
            i.hash = p.hash
            return i
        }
    }

    /// An item's estimates by preset, for the presets this app knows: the
    /// device may offer new ones, which an older app leaves out.
    static func offered(_ o: Msg_CompressOption) -> [String: Int64] {
        var out: [String: Int64] = [:]
        for c in o.choices where (isVideo(c.preset) || photoPresets.contains(c.preset)) && out[c.preset] == nil {
            out[c.preset] = c.estimatedSize
        }
        return out
    }

    private static func offersVideo(_ ps: [String: Int64]) -> Bool {
        videoPresets.contains { ps[$0] != nil }
    }

    /// Every preset this app knows that any item offers.
    private static func anyOffered(_ options: [Msg_CompressOption]) -> Set<String> {
        options.reduce(into: Set<String>()) { $0.formUnion(offered($1).keys) }
    }

    /// C1: the videos' radio - the video presets any item offers, 1080p,
    /// 720p, HEVC, then "Leave as they are"; none at all when no item
    /// offers one.
    static func videoChoices(_ options: [Msg_CompressOption]) -> [String] {
        let any = anyOffered(options)
        let shown = videoPresets.filter { any.contains($0) }
        return shown.isEmpty ? [] : shown + [noVideo]
    }

    /// C2: the radio's first choice - 1080p if any item offers it, else
    /// HEVC if any does, else 720p; nil with no video choice at all (the
    /// dialog's is then "Leave as they are").
    static func defaultVideo(_ options: [Msg_CompressOption]) -> String? {
        let shown = videoChoices(options)
        guard !shown.isEmpty else { return nil }
        return ["video_1080p", "video_hevc"].first { shown.contains($0) } ?? "video_720p"
    }

    /// C3: a switch for each photo preset any item offers, strongest first
    /// (each on as the dialog opens).
    static func photoSwitches(_ options: [Msg_CompressOption]) -> [String] {
        let any = anyOffered(options)
        return photoPresets.filter { any.contains($0) }
    }

    /// C4: what one item gets with `video` on the radio and the photo
    /// switches `photos` on, nil for nothing: a video the preset chosen if
    /// it offers it, else HEVC if it offers that ("Leave as they are":
    /// nothing); a photo the strongest of its presets whose switch is on.
    static func preset(for o: Msg_CompressOption, video: String, photos: Set<String>) -> String? {
        let ps = offered(o)
        if offersVideo(ps) {
            guard video != noVideo else { return nil }
            if ps[video] != nil { return video }
            return ps["video_hevc"] != nil ? "video_hevc" : nil
        }
        return photoPresets.first { photos.contains($0) && ps[$0] != nil }
    }

    /// One file to compress, with what it weighs now and its estimate after
    /// (for the sums).
    struct Planned: Equatable {
        let path: String
        let hash: String
        let preset: String
        let size: Int64
        let estimate: Int64
    }

    /// C5: the tasks the choices make, in the order the items came, with the
    /// path and content the device answered for.
    static func tasks(_ options: [Msg_CompressOption], video: String, photos: Set<String>) -> [Planned] {
        options.compactMap { o in
            guard let p = preset(for: o, video: video, photos: photos) else { return nil }
            return Planned(path: o.path, hash: o.hash, preset: p, size: o.size, estimate: offered(o)[p] ?? o.size)
        }
    }

    /// How many files a choice compresses, what they weigh now and, estimated,
    /// after.
    struct Sum: Equatable {
        var count = 0
        var now: Int64 = 0
        var after: Int64 = 0
    }

    private static func sum(_ tasks: [Planned]) -> Sum {
        tasks.reduce(into: Sum()) { s, t in
            s.count += 1
            s.now += t.size
            s.after += t.estimate
        }
    }

    /// C6: a video choice's line - everything it would do if chosen: the
    /// videos that get it, and those that fall back to HEVC with it.
    static func videoRow(_ options: [Msg_CompressOption], _ choice: String) -> Sum {
        let videos = options.filter { offersVideo(offered($0)) }
        return sum(tasks(videos, video: choice, photos: []))
    }

    /// C7: a photo switch's line - the photos that would get its preset
    /// were it on, the other switches as they are.
    static func photoRow(_ options: [Msg_CompressOption], _ preset: String, photos: Set<String>) -> Sum {
        sum(tasks(options, video: noVideo, photos: photos.union([preset])).filter { $0.preset == preset })
    }

    /// C8: what the choices would free: each task's size less its estimate,
    /// never below nothing.
    static func saved(_ tasks: [Planned]) -> Int64 {
        tasks.reduce(0) { $0 + max(0, $1.size - $1.estimate) }
    }

    /// C9: the note that videos take a while, with a video among the tasks.
    static func slow(_ tasks: [Planned]) -> Bool {
        tasks.contains { isVideo($0.preset) }
    }

    /// C10: the files nothing can be done for (no preset this app knows),
    /// in the order they came: each one's name and the device's reason.
    static func notOffered(_ options: [Msg_CompressOption]) -> [Msg_CompressOption] {
        options.filter { offered($0).isEmpty }
    }

    /// C10: none of the files can be made smaller (none asked about either).
    static func nothing(_ options: [Msg_CompressOption]) -> Bool {
        notOffered(options).count == options.count
    }

    // MARK: The requests and their answers

    /// C13: GetCompressOptions, for the files picked.
    static func optionsRequest(_ items: [Msg_CompressItem]) -> Msg_GetCompressOptions {
        var g = Msg_GetCompressOptions()
        g.items = items
        return g
    }

    /// C13: CompressFiles, with the tasks.
    static func compressRequest(_ tasks: [Planned]) -> Msg_CompressFiles {
        var c = Msg_CompressFiles()
        c.tasks = tasks.map { t in
            var m = Msg_CompressTask()
            m.path = t.path
            m.hash = t.hash
            m.preset = t.preset
            return m
        }
        return c
    }

    /// C13: GetCompressJob, for the job CompressFiles answered with ("":
    /// whichever the device has).
    static func jobRequest(_ id: String) -> Msg_GetCompressJob {
        var g = Msg_GetCompressJob()
        g.id = id
        return g
    }

    /// The device's own words for a refusal ("" when it gave none: the
    /// bridge's answer for a device it can't reach carries none).
    static func said(_ resp: Msg_RespEnvelope) -> String {
        if !resp.errorMessage.isEmpty { return resp.errorMessage }
        if case .respAck(let ack) = resp.payload { return ack.errorMsg }
        return ""
    }

    /// C11: GetCompressOptions' answer.
    enum OptionsAnswer: Equatable {
        case options([Msg_CompressOption])
        /// A device before Compress (unknown_payload): it needs an update.
        case old
        /// The device's words (more than 500 files among them).
        case refused(String)
        /// No answer, or nothing usable: Try again.
        case failed
    }

    static func optionsAnswer(_ resp: Msg_RespEnvelope?) -> OptionsAnswer {
        guard let resp else { return .failed }
        if !resp.error, case .respCompressOptions(let o) = resp.payload { return .options(o.items) }
        if ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) { return .old }
        let words = said(resp)
        return words.isEmpty ? .failed : .refused(words)
    }

    /// C12: CompressFiles' answer.
    enum StartAnswer: Equatable {
        /// The job, at once, whatever is still to do.
        case job(Msg_CompressJob)
        case old
        /// The device's words (the demo device's among them).
        case refused(String)
        case failed
    }

    static func startAnswer(_ resp: Msg_RespEnvelope?) -> StartAnswer {
        guard let resp else { return .failed }
        if !resp.error, case .respCompressJob(let j) = resp.payload { return .job(j) }
        if ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) { return .old }
        let words = said(resp)
        return words.isEmpty ? .failed : .refused(words)
    }

    /// J1: GetCompressJob's answer for the job `id`.
    enum PollAnswer: Equatable {
        case job(Msg_CompressJob)
        /// not_found (the device restarted since), or another job's: this
        /// one is over as it was last heard of.
        case gone
        /// No answer: asked again at the next look.
        case failed
    }

    static func pollAnswer(_ resp: Msg_RespEnvelope?, id: String) -> PollAnswer {
        guard let resp else { return .failed }
        if !resp.error, case .respCompressJob(let j) = resp.payload {
            return !id.isEmpty && !j.id.isEmpty && j.id != id ? .gone : .job(j)
        }
        return resp.errorCode == "not_found" ? .gone : .failed
    }

    // MARK: The job

    /// J2: how long until the job is asked about again - 2 s while the
    /// dialog shows it, 10 s while only the screen behind follows it; never
    /// once it is over, or nothing looks.
    static func pollDelay(shown: Bool, watched: Bool, over: Bool) -> Duration? {
        if over { return nil }
        if shown { return .seconds(2) }
        return watched ? .seconds(10) : nil
    }

    /// J3: how far the job is, for its bar: the tasks over, and a running
    /// video's share of the next one. The total is the device's (tasks
    /// sent meanwhile join it).
    static func progress(_ job: Msg_CompressJob) -> (done: Int, total: Int, fraction: Double) {
        let total = max(Int(job.total), Int(job.done), 0)
        let running = job.results.first { $0.state == "running" }.map { Double(min(max($0.percent, 0), 100)) / 100 } ?? 0
        let fraction = total > 0 ? min(1, (Double(job.done) + running) / Double(total)) : 0
        return (min(Int(job.done), total), total, fraction)
    }

    /// A file the job left as it was, and why in the device's words ("":
    /// none came).
    struct LeftAsItWas: Equatable {
        let path: String
        let message: String
        var name: String { CompressRules.name(path) }
    }

    /// J4: what the job came to - how many files it compressed, what it
    /// freed, and the files it couldn't (failed, or skipped as no smaller:
    /// both left as they were). Gone (the device restarted mid-job), the
    /// tasks not over weren't compressed either.
    static func outcome(_ job: Msg_CompressJob, gone: Bool = false) -> (compressed: Int, saved: Int64, failed: [LeftAsItWas]) {
        var compressed = 0
        var failed: [LeftAsItWas] = []
        for r in job.results {
            if r.state == "done" {
                compressed += 1
            } else if r.state == "failed" || r.state == "skipped" || gone {
                failed.append(LeftAsItWas(path: r.path, message: r.message))
            }
        }
        return (compressed, max(0, job.saved), failed)
    }

    /// J4: the job is over: the device says so, or it is gone.
    static func over(_ job: Msg_CompressJob, gone: Bool) -> Bool { gone || job.finished }

    /// J5: the results done, each with the file now at its path, not put in
    /// place yet (`seen`, by resultKey): a screen puts each in place once.
    static func newlyDone(_ job: Msg_CompressJob, seen: Set<String>) -> [Msg_CompressResult] {
        job.results.filter { $0.state == "done" && $0.hasFile && !seen.contains(resultKey($0)) }
    }

    /// A done result's key: its path, and the content it was compressed into.
    static func resultKey(_ r: Msg_CompressResult) -> String {
        r.path + "\n" + (r.hasFile ? r.file.hash : "")
    }

    /// J6: the file a screen shows after a compress, from the one it showed:
    /// the device's (a new hash, maybe a new name - a PNG becomes .jpg - its
    /// size, original_hash and original_removed) over it, as after an edit
    /// replacing every copy - its dates, copies and stars kept where the
    /// answer doesn't say.
    static func compressedFile(shown: Msg_File, _ a: Msg_File) -> Msg_File {
        var f = a
        if f.path.isEmpty { f.path = shown.path }
        if !f.hasCreated && shown.hasCreated { f.created = shown.created }
        if !f.hasModified && shown.hasModified { f.modified = shown.modified }
        f.clearContent()
        if !(f.hasCopies && f.copies > 0) {
            if shown.hasCopies { f.copies = shown.copies } else { f.clearCopies() }
        }
        if !f.hasRating && shown.hasRating { f.rating = shown.rating }
        return f
    }

    // MARK: The viewer

    /// R2: the viewer's Info says the file was compressed and its original
    /// deleted (R1, no Revert to Original for it, is PhotoEditRules'
    /// offersRevert).
    static func compressedNote(_ item: PhotoGalleryVM.Item) -> Bool { item.originalRemoved }

    // MARK: Words

    /// A choice's name.
    static func title(_ preset: String) -> String {
        switch preset {
        case "video_1080p": return S.appFilesCompressVideo1080p
        case "video_720p": return S.appFilesCompressVideo720p
        case "video_hevc": return S.appFilesCompressVideoHevc
        case "photo_12mp": return S.appFilesCompressPhoto12mp
        case "jpeg_resave": return S.appFilesCompressJpegResave
        case "png_to_jpeg": return S.appFilesCompressPngToJpeg
        default: return S.appFilesCompressVideoNone
        }
    }

    /// "3 files · 2.1 GB → about 640 MB".
    static func rowText(_ s: Sum) -> String {
        S.appFilesCompressRow(count: s.count, now: formatByteCount(s.now), after: formatByteCount(s.after))
    }

    /// A file's name, as the dialog's lists show it.
    static func name(_ path: String) -> String {
        (path as NSString).lastPathComponent
    }
}

/// The Compress dialog's state - the device's options, the choices made,
/// then the job - and the job followed once the dialog has closed, while
/// the screen that started it holds the model.
@MainActor
final class CompressModel: ObservableObject, Identifiable {
    enum Phase: Equatable {
        /// GetCompressOptions is on its way.
        case estimating
        /// It didn't come: the device's words, else ours, with Try again.
        case optionsFailed(String?)
        /// A device before Compress (unknown_payload).
        case needsUpdate
        /// The options came: the choices.
        case choosing
        /// Compress was taken: the job, as last heard of.
        case job(Msg_CompressJob)
    }

    typealias Request = @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope

    /// Through OTCConnection: the options as a page is waited for (the
    /// device reads up to 500 files' headers), the job as a small list,
    /// Compress itself (answered at once) with a list's time.
    static let viaDevice: Request = { payload in
        let c = OTCConnection.shared
        switch payload {
        case .reqGetCompressOptions:
            return try await c.ask("compress", base: OTCConnection.pageTimeout) { $0.payload = payload }
        case .reqGetCompressJob:
            return try await c.ask("compress_job", base: OTCConnection.listTimeout) { $0.payload = payload }
        default:
            return try await c.request(timeout: OTCConnection.listTimeout) { $0.payload = payload }
        }
    }

    let id = UUID()
    /// The files asked about (at most 500).
    let items: [Msg_CompressItem]
    @Published private(set) var phase: Phase = .estimating
    @Published private(set) var options: [Msg_CompressOption] = []
    /// The radio's choice (CompressRules.noVideo: leave them).
    @Published var video = CompressRules.noVideo
    /// The photo switches that are on.
    @Published var photos: Set<String> = []
    /// CompressFiles is on its way: the button waits.
    @Published private(set) var starting = false
    /// Compress was refused (the demo device, among others): the device's
    /// words, shown over the dialog, the choices kept.
    @Published var refusal: String?
    /// The tasks sent had a video: the job's view says videos take a while.
    @Published private(set) var sentVideo = false
    /// The device no longer has the job (J1's gone): over as last heard of.
    @Published private(set) var gone = false
    /// The dialog shows: the job is asked about every 2 s, else every 10 s
    /// while `watched` (J2).
    var shown = false
    /// The screen behind follows the job (it holds this model): back on
    /// screen, it is followed again.
    var watched = true {
        didSet { if watched && !oldValue && jobID != nil && !over { startFollow() } }
    }

    /// Sends the dialog's requests. Tests answer in the device's place.
    var request: Request = CompressModel.viaDevice
    /// J2's waits (tests shorten them).
    var pollDelay: (_ shown: Bool, _ watched: Bool, _ over: Bool) -> Duration? = CompressRules.pollDelay
    /// The device took the tasks: the screen's selection goes, and it holds
    /// this model to follow the job.
    var onStarted: () -> Void = {}
    /// Files compressed since the last call (J5): each takes the old one's
    /// place on the screen behind.
    var onResults: ([Msg_CompressResult]) -> Void = { _ in }
    /// The job is over (J4): how many files it compressed.
    var onFinished: (Int) -> Void = { _ in }

    /// The results put in place already (J5's seen).
    private var seen = Set<String>()
    private var follow: Task<Void, Never>?

    init(items: [Msg_CompressItem]) {
        self.items = items
    }

    deinit {
        follow?.cancel()
    }

    /// What Compress would send now.
    var tasks: [CompressRules.Planned] { CompressRules.tasks(options, video: video, photos: photos) }

    /// The job's id, once there is one.
    var jobID: String? {
        if case .job(let j) = phase { return j.id }
        return nil
    }

    /// The job is over (J4).
    var over: Bool {
        if case .job(let j) = phase { return CompressRules.over(j, gone: gone) }
        return false
    }

    /// Asks the device for its options; the choices start as C2 and C3 say.
    /// Nothing picked that the device holds: nothing to ask, nothing to do.
    func load() async {
        phase = .estimating
        guard !items.isEmpty else {
            options = []
            phase = .choosing
            return
        }
        let resp = try? await request(.reqGetCompressOptions(CompressRules.optionsRequest(items)))
        switch CompressRules.optionsAnswer(resp) {
        case .options(let o):
            options = o
            video = CompressRules.defaultVideo(o) ?? CompressRules.noVideo
            photos = Set(CompressRules.photoSwitches(o))
            phase = .choosing
        case .old:
            phase = .needsUpdate
        case .refused(let words):
            phase = .optionsFailed(words)
        case .failed:
            phase = .optionsFailed(nil)
        }
    }

    /// Compress: the tasks go to the device, and the job shows. Refused,
    /// the device's words are said and the choices stay. No answer: the
    /// device is asked whether the job started even so (the request may
    /// have reached it); if it didn't, that nothing was compressed.
    func start() async {
        let tasks = tasks
        guard phase == .choosing, !starting, !tasks.isEmpty else { return }
        starting = true
        defer { starting = false }
        let resp = try? await request(.reqCompressFiles(CompressRules.compressRequest(tasks)))
        switch CompressRules.startAnswer(resp) {
        case .job(let job):
            started(job, tasks)
        case .old:
            phase = .needsUpdate
        case .refused(let words):
            refusal = words
        case .failed:
            // Taken all the same if the device's job holds them.
            let again = try? await request(.reqGetCompressJob(CompressRules.jobRequest("")))
            if case .job(let job) = CompressRules.pollAnswer(again, id: ""),
               job.results.contains(where: { r in tasks.contains { $0.path == r.path } }) {
                started(job, tasks)
            } else {
                refusal = S.appFilesCompressFailed(count: tasks.count)
            }
        }
    }

    private func started(_ job: Msg_CompressJob, _ tasks: [CompressRules.Planned]) {
        sentVideo = CompressRules.slow(tasks)
        onStarted()
        take(job)
        startFollow()
    }

    /// The job asked about until it is over (J2): every 2 s while the
    /// dialog shows it, every 10 s while only the screen behind holds this
    /// model - it lets go once the job is over, or when it goes itself, and
    /// the asking ends with it.
    private func startFollow() {
        follow?.cancel()
        // Held only while it asks: a model no one holds any more ends it.
        follow = Task { [weak self] in
            while !Task.isCancelled {
                guard let delay = self?.nextDelay() else { return }
                try? await Task.sleep(for: delay)
                guard !Task.isCancelled, let model = self else { return }
                await model.ask()
            }
        }
    }

    private func nextDelay() -> Duration? {
        jobID == nil ? nil : pollDelay(shown, watched, over)
    }

    private func ask() async {
        guard let id = jobID, !over else { return }
        let resp = try? await request(.reqGetCompressJob(CompressRules.jobRequest(id)))
        switch CompressRules.pollAnswer(resp, id: id) {
        case .job(let job):
            take(job)
        case .gone:
            gone = true
            if case .job(let j) = phase { ended(j) }
        case .failed:
            break
        }
    }

    /// The job as the device last said it: what was compressed since goes
    /// to the screen behind, and its end is said.
    private func take(_ job: Msg_CompressJob) {
        let wasOver = over
        phase = .job(job)
        let new = CompressRules.newlyDone(job, seen: seen)
        if !new.isEmpty {
            seen.formUnion(new.map(CompressRules.resultKey))
            onResults(new)
        }
        if over && !wasOver { ended(job) }
    }

    private func ended(_ job: Msg_CompressJob) {
        let o = CompressRules.outcome(job, gone: gone)
        onFinished(o.compressed)
        Announce.polite(Self.endText(o))
    }

    /// What the job's end says, a sentence each: compressed, and left as
    /// they were.
    static func endText(_ o: (compressed: Int, saved: Int64, failed: [CompressRules.LeftAsItWas])) -> String {
        var parts: [String] = []
        if o.compressed > 0 { parts.append(S.appFilesCompressDone(count: o.compressed, size: formatByteCount(o.saved))) }
        if !o.failed.isEmpty { parts.append(S.appFilesCompressFailed(count: o.failed.count)) }
        return parts.joined(separator: "\n")
    }
}

// MARK: - The dialog

/// The Compress dialog, as a sheet over Images or the Largest files list:
/// the options, the choices and Compress, then the job's progress and how
/// it ended.
struct CompressSheet: View {
    @ObservedObject var model: CompressModel
    @Environment(\.dismiss) private var dismiss
    @State private var showNotOffered = false
    @State private var showNotDone = false

    var body: some View {
        NavigationStack {
            Form { content }
                .navigationTitle(S.appFilesCompressTitle(count: model.items.count))
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        // The job goes on without the dialog.
                        Button(model.jobID == nil ? S.commonCancel : S.commonClose) { dismiss() }
                    }
                }
        }
        .task { if model.phase == .estimating { await model.load() } }
        .onAppear { model.shown = true }
        .onDisappear { model.shown = false }
        .alert(model.refusal ?? "", isPresented: Binding(get: { model.refusal != nil }, set: { if !$0 { model.refusal = nil } })) {
            Button(S.commonOk, role: .cancel) {}
        }
    }

    @ViewBuilder
    private var content: some View {
        switch model.phase {
        case .estimating:
            Section {
                HStack(spacing: 10) {
                    ProgressView()
                    Text(S.appFilesCompressEstimating).foregroundStyle(.secondary)
                }
                .accessibilityElement(children: .combine)
            }
        case .optionsFailed(let words):
            Section {
                Text(words ?? S.appFilesCompressOptionsFailed)
                Button(LoadProblem.tryAgain) { Task { await model.load() } }
            }
            .onAppear { Announce.polite(words ?? S.appFilesCompressOptionsFailed) }
        case .needsUpdate:
            Section {
                Text(S.appFilesCompressNeedsUpdate)
            }
        case .choosing:
            choices
        case .job(let job):
            jobView(job)
        }
    }

    // MARK: The choices

    @ViewBuilder
    private var choices: some View {
        let options = model.options
        let videos = CompressRules.videoChoices(options)
        let switches = CompressRules.photoSwitches(options)
        let left = CompressRules.notOffered(options)
        if !videos.isEmpty {
            Section {
                Picker(S.appFilesCompressVideos, selection: $model.video) {
                    ForEach(videos, id: \.self) { p in
                        Group {
                            if p == CompressRules.noVideo {
                                Text(S.appFilesCompressVideoNone)
                            } else {
                                choiceLabel(p, CompressRules.videoRow(options, p))
                            }
                        }
                        .tag(p)
                    }
                }
                .pickerStyle(.inline)
                .labelsHidden()
                .disabled(model.starting)
            } header: {
                Text(S.appFilesCompressVideos)
            }
        }
        if !switches.isEmpty {
            Section {
                ForEach(switches, id: \.self) { p in
                    Toggle(isOn: Binding(
                        get: { model.photos.contains(p) },
                        set: { on in if on { model.photos.insert(p) } else { model.photos.remove(p) } }
                    )) {
                        choiceLabel(p, CompressRules.photoRow(options, p, photos: model.photos))
                    }
                    .disabled(model.starting)
                }
            }
        }
        if CompressRules.nothing(options) {
            // Nothing can be done at all: that, and each file's reason.
            Section {
                reasons(left) { Text(S.appFilesCompressNothing) }
            }
        } else {
            if !left.isEmpty {
                Section {
                    reasons(left) { Text(S.appFilesCompressNotOffered(count: left.count)) }
                }
            }
            foot
        }
    }

    /// A choice: its name, and how many files it takes and their sizes now
    /// and after under it.
    private func choiceLabel(_ preset: String, _ sum: CompressRules.Sum) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(CompressRules.title(preset))
            Text(CompressRules.rowText(sum))
                .font(.footnote)
                .monospacedDigit()
                .foregroundStyle(.secondary)
        }
        .accessibilityElement(children: .combine)
    }

    /// The files the device can't make smaller under `label`, shown when
    /// it is opened: each one's name and the device's reason.
    private func reasons(_ left: [Msg_CompressOption], label: () -> Text) -> some View {
        DisclosureGroup(isExpanded: $showNotOffered) {
            ForEach(Array(left.enumerated()), id: \.offset) { _, o in
                nameAndWhy(CompressRules.name(o.path), o.reason)
            }
        } label: {
            label()
        }
    }

    private func nameAndWhy(_ name: String, _ why: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(name).lineLimit(1).truncationMode(.middle)
            if !why.isEmpty {
                Text(why)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .accessibilityElement(children: .combine)
    }

    /// What the choices would free, that the originals go for good, that
    /// videos take a while (when a video is among them), and Compress.
    private var foot: some View {
        let tasks = model.tasks
        return Section {
            Text(S.appFilesCompressTotal(size: formatByteCount(CompressRules.saved(tasks))))
                .font(.headline)
            Text(S.appFilesCompressWarning)
                .font(.footnote)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if CompressRules.slow(tasks) {
                Text(S.appFilesCompressSlow)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Button(role: .destructive) {
                Task { await model.start() }
            } label: {
                HStack {
                    Spacer()
                    if model.starting {
                        ProgressView()
                    } else {
                        Text(S.appFilesCompressConfirm).fontWeight(.semibold)
                    }
                    Spacer()
                }
            }
            .disabled(tasks.isEmpty || model.starting)
            .accessibilityLabel(S.appFilesCompressConfirm)
        }
    }

    // MARK: The job

    @ViewBuilder
    private func jobView(_ job: Msg_CompressJob) -> some View {
        if !model.over {
            let p = CompressRules.progress(job)
            Section {
                VStack(alignment: .leading, spacing: 8) {
                    Text(S.appFilesCompressProgress(done: p.done, total: p.total))
                        .monospacedDigit()
                    ProgressView(value: p.fraction)
                        .progressViewStyle(.linear)
                }
                .padding(.vertical, 4)
                .accessibilityElement(children: .combine)
                .accessibilityAddTraits(.updatesFrequently)
                if model.sentVideo {
                    Text(S.appFilesCompressSlow)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        } else {
            let o = CompressRules.outcome(job, gone: model.gone)
            Section {
                if o.compressed > 0 {
                    Text(S.appFilesCompressDone(count: o.compressed, size: formatByteCount(o.saved)))
                }
                if !o.failed.isEmpty {
                    DisclosureGroup(isExpanded: $showNotDone) {
                        ForEach(Array(o.failed.enumerated()), id: \.offset) { _, f in
                            nameAndWhy(f.name, f.message)
                        }
                    } label: {
                        Text(S.appFilesCompressFailed(count: o.failed.count))
                    }
                }
            }
        }
    }
}

// MARK: - Images

extension PhotoGalleryVM {
    /// The picked photos and videos Compress asks about, in the order the
    /// grid shows them (then those a reload took off it): the device's own,
    /// with the content they are shown with - never one only on this phone.
    func compressPicks() -> [(path: String, hash: String)] {
        let picked = pickedItems()
        var out: [(path: String, hash: String)] = []
        var seen = Set<String>()
        func add(_ it: Item) {
            guard !it.isLocalOnly, !it.path.isEmpty, !it.hash.isEmpty, seen.insert(it.path).inserted else { return }
            out.append((it.path, it.hash))
        }
        for it in items where picked[it.path] != nil { add(it) }
        for path in picked.keys.sorted() { if let it = picked[path] { add(it) } }
        return out
    }

    /// The selection bar's Compress: the dialog for the picked photos and
    /// videos. The device taking them clears the selection, and Images
    /// follows the job (compressFollow) until it is over, each file
    /// compressed taking its old one's place in the grid.
    func compressModel() -> CompressModel {
        let m = CompressModel(items: CompressRules.items(compressPicks()))
        m.onStarted = { [weak self, weak m] in
            self?.clearSelection()
            self?.compressFollow = m
        }
        m.onResults = { [weak self] in self?.compressed($0) }
        m.onFinished = { [weak self, weak m] _ in
            if let self, self.compressFollow === m { self.compressFollow = nil }
        }
        return m
    }

    /// J6: files compressed (`results`, each done with its File): each takes
    /// the place of the item at its path, as an edit replacing every copy
    /// does - a new hash (a new full size and tile), a new name for a PNG
    /// that became a JPEG, original_removed set - keeping its place, date,
    /// copies and stars. The original's tile leaves the cache on this
    /// phone: its content is gone from the device.
    func compressed(_ results: [Msg_CompressResult]) {
        var changed = false
        for r in results where r.state == "done" && r.hasFile {
            let f = r.file
            guard let at = items.firstIndex(where: { $0.path == r.path && !$0.isLocalOnly }) else { continue }
            let old = items[at]
            let new = Item(
                id: PhotoPaging.itemID(f),
                path: f.path.isEmpty ? old.path : f.path,
                mime: f.mime.isEmpty ? old.mime : f.mime,
                size: Int(f.fileSize),
                thumbData: nil,
                localURL: nil,
                isLocalOnly: false,
                month: old.month,
                created: old.created,
                hash: f.hash.isEmpty ? old.hash : f.hash,
                copies: f.hasCopies && f.copies > 0 ? Int(f.copies) : old.copies,
                up: old.up,
                rating: old.rating,
                originalHash: f.originalHash,
                originalRemoved: f.originalRemoved
            )
            forgetFullSize(of: old.path)
            if new.path != old.path { forgetFullSize(of: new.path) }
            if !old.hash.isEmpty && old.hash != new.hash { thumbs.cache.discard(old.hash) }
            items[at] = new
            if selected.remove(old.path) != nil { selected.insert(new.path) }
            if openIndex == at { open(index: at) }
            changed = true
        }
        if changed && !fixedList { places.libraryChanged() }
    }
}
