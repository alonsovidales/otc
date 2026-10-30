// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  Memories.swift
//  OffTheCloud
//
//  On this day (Memories, device release 150): the photos taken around
//  today in earlier years, the best of each year chosen by the device
//  (GetMemories). The web and Android follow the same rules:
//    - Shown only while the user has it on (Settings.memories_enabled, on
//      by default; absent = a device before it, which shows nothing), and
//      only over Images' whole library: no search, person, collection or
//      map area.
//    - A strip at the top of the grid, titled by its period ("On this day",
//      "This week", "This month") with Day · Week · Month beside it. The
//      three periods are asked together; the strip opens on the first that
//      has memories (day, week, month), and a period picked stays for the
//      session (`MemoriesModel.period`). A period with none is dimmed.
//      Nothing in any period - or the device still measuring with nothing
//      to show - and the strip isn't there at all.
//    - A card per memory (an earlier year): its cover, "3 years ago", the
//      place. A tap opens it full screen as a slideshow (`MemoryViewer`):
//      4 s a photo, a tap on the left third goes back and elsewhere on, a
//      press held pauses, Pause/Resume, segments on top, a swipe down
//      closes; with VoiceOver it starts paused and its sides are buttons.
//      See all shows its photos in a grid that opens the viewer; the …
//      menu hides the photo (the viewer stays), the memory, the photo's day
//      or a named person (HideMemory; the last three close it), with Undo
//      for 6 s after (HideMemory show), or the device's words when it
//      refuses. A memory gone from the answers (hidden on another app)
//      closes its viewer.
//    - Asked with the phone's own date, time zone and UTC offset, again at
//      midnight, every 2 minutes while an answer says `measuring`, after a
//      hide and Settings' change; a failure again 1 s doubling to 10 s, at
//      once on a wake. Settings has the switch (SetMemoriesEnabled) and
//      "Show hidden memories again".
//

import SwiftUI
import Combine

// MARK: - Dates and words

/// The days memories are asked for and named by, in the Gregorian
/// calendar of the phone's time zone (as Images' months).
enum MemoryDates {
    /// "2026-10-30": the phone's today.
    static func day(of date: Date, zone: TimeZone = .autoupdatingCurrent) -> String {
        let c = PhotoMonths.calendar(in: zone).dateComponents([.year, .month, .day], from: date)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
    }

    /// The year, month and day "YYYY-MM-DD" names; nil for anything else.
    static func parse(_ s: String) -> (year: Int, month: Int, day: Int)? {
        let p = s.split(separator: "-")
        guard p.count == 3, let y = Int(p[0]), let m = Int(p[1]), let d = Int(p[2]),
              (1...12).contains(m), (1...31).contains(d) else { return nil }
        return (y, m, d)
    }

    /// GetMemories for `period` on the phone's `now`.
    static func request(_ period: Msg_MemoryPeriod, now: Date, zone: TimeZone) -> Msg_GetMemories {
        var r = Msg_GetMemories()
        r.date = day(of: now, zone: zone)
        r.period = period
        r.timeZone = zone.identifier
        r.utcOffsetMinutes = Int32(zone.secondsFromGMT(for: now) / 60)
        return r
    }
}

enum MemoryText {
    /// "1 year ago", "3 years ago".
    static func yearsAgo(_ n: Int32) -> String {
        S.appPhotosMemoriesYearsAgo(count: Int(max(1, n)))
    }

    /// The strip's title for a period.
    static func title(_ p: Msg_MemoryPeriod) -> String {
        switch p {
        case .memoryWeek: return S.appPhotosMemoriesTitleWeek
        case .memoryMonth: return S.appPhotosMemoriesTitleMonth
        default: return S.appPhotosMemoriesTitleDay
        }
    }

    /// Its short choice: Day, Week, Month.
    static func choice(_ p: Msg_MemoryPeriod) -> String {
        switch p {
        case .memoryWeek: return S.appPhotosMemoriesPeriodWeek
        case .memoryMonth: return S.appPhotosMemoriesPeriodMonth
        default: return S.appPhotosMemoriesPeriodDay
        }
    }

    /// The days a memory covers, by its key's period (Android's
    /// memoryDays): "30 Oct 2023", "27 Oct – 2 Nov 2023" (the end's year,
    /// across a new year too), "October 2023"; "" when they can't be read.
    static func days(_ m: Msg_Memory) -> String {
        guard let a = MemoryDates.parse(m.startDate) else { return "" }
        let b = MemoryDates.parse(m.endDate) ?? a
        if m.key.hasPrefix("month:") {
            return PhotoMonths.title(String(format: "%04d-%02d", a.year, a.month))
        }
        if m.key.hasPrefix("week:"), a != b,
           let from = PhotoMonths.dayShort(month: a.month, day: a.day),
           let to = PhotoMonths.dayShort(month: b.month, day: b.day) {
            return S.appPhotosMemoriesWeekInYear(from: from, to: to, year: String(b.year))
        }
        return PhotoMonths.dayTitle(year: a.year, month: a.month, day: a.day) ?? ""
    }

    /// An opened memory's first line: "3 years ago · 30 Oct 2023 ·
    /// Asturias" (no place, no last part).
    static func header(_ m: Msg_Memory) -> String {
        var parts = [yearsAgo(m.yearsAgo), days(m)].filter { !$0.isEmpty }
        if !m.place.isEmpty { parts.append(m.place) }
        return parts.joined(separator: " · ")
    }

    /// A card as VoiceOver reads it: "3 years ago, Asturias".
    static func cardLabel(_ m: Msg_Memory) -> String {
        m.place.isEmpty ? yearsAgo(m.yearsAgo) : [yearsAgo(m.yearsAgo), m.place].joined(separator: ", ")
    }
}

// MARK: - Which period shows

enum MemoryChoice {
    static let periods: [Msg_MemoryPeriod] = [.memoryDay, .memoryWeek, .memoryMonth]

    /// The period the strip shows: the one picked this session while it
    /// has memories, else the first that has (day, week, month); nil when
    /// none has.
    static func shown(picked: Msg_MemoryPeriod?, answers: [Msg_MemoryPeriod: Msg_Memories]) -> Msg_MemoryPeriod? {
        let has = { (p: Msg_MemoryPeriod) in !(answers[p]?.memories.isEmpty ?? true) }
        if let picked, has(picked) { return picked }
        return periods.first(where: has)
    }
}

// MARK: - The model

/// Images' memories, one for the session: whether the device has them and
/// they are on, the three periods' answers for today, the period picked,
/// hiding and its undo. Settings' switch and Log Out go through it too.
@MainActor
final class MemoriesModel: ObservableObject {
    typealias Request = @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope

    static let shared = MemoriesModel()

    /// Settings.memories_enabled: nil while not known, or on a device
    /// before On this day (nothing shown).
    @Published private(set) var enabled: Bool?
    /// Today's answers, by period.
    @Published private(set) var answers: [Msg_MemoryPeriod: Msg_Memories] = [:]
    /// The period picked this session (nil: the first that has memories).
    @Published var period: Msg_MemoryPeriod?
    /// What just happened, at the foot of the screen: hidden (with Undo)
    /// or a failure in the device's words.
    @Published private(set) var toast: MemoryToast?
    /// Bumped when cards got their thumbnails: they draw them.
    @Published private(set) var thumbsVersion = 0

    /// Sends a request. Tests answer in the device's place.
    var request: Request = { payload in
        try await OTCConnection.shared.ask("memories", base: OTCConnection.listTimeout) { $0.payload = payload }
    }
    var now: () -> Date = { Date() }
    var zone: () -> TimeZone = { .autoupdatingCurrent }
    /// How long a toast stays (tests shorten it).
    var toastFor: TimeInterval = 6
    /// How often the memories are asked again while the device measures.
    var measuringEvery: TimeInterval = 120
    /// The wait before asking again after a failure (tests shorten it).
    var retryDelay: (Int) -> TimeInterval = { PageRetry.delay(afterFailures: $0) }

    /// The cards' thumbnails, by content hash.
    let thumbs: GridThumbLoader
    static let cardSide: CGFloat = 160

    /// The date the answers are for, and when they came.
    private(set) var askedDay: String?
    private var askedAt = Date.distantPast
    private var loading: Task<Void, Never>?
    /// The next ask while the device measures.
    private var measuringTask: Task<Void, Never>?
    private var failed = false
    /// What was hidden last, for Undo.
    private var lastHidden: (kind: Msg_MemoryHideKind, value: String)?
    private var toastTask: Task<Void, Never>?
    private var watches: [AnyCancellable] = []

    init(thumbCache: ThumbDiskCache = .shared) {
        thumbs = GridThumbLoader(maxPt: Self.cardSide, cache: thumbCache, request: GridThumbLoader.deviceRequest)
        thumbs.onImages = { [weak self] in self?.thumbsVersion &+= 1 }
        // The date changed (midnight, a new time zone) or the app came back
        // to the front on another day: today's memories.
        watches.append(NotificationCenter.default.publisher(for: .NSCalendarDayChanged)
            .merge(with: NotificationCenter.default.publisher(for: UIApplication.willEnterForegroundNotification))
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.refreshIfStale() })
        // Signed in again, a network came up, back in front: a failed ask
        // goes again now.
        watches.append(OTCConnection.shared.$wakes
            .dropFirst()
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                guard let self, self.failed else { return }
                self.load()
            })
        watches.append(NotificationCenter.default.publisher(for: .otcThumbnailCacheCleared)
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                self?.thumbs.reset()
                self?.thumbsVersion &+= 1
            })
    }

    /// The period the strip shows, and its memories.
    var shownPeriod: Msg_MemoryPeriod? { MemoryChoice.shown(picked: period, answers: answers) }
    var shown: [Msg_Memory] { shownPeriod.flatMap { answers[$0]?.memories } ?? [] }
    /// Whether the strip is there at all.
    var isShown: Bool { enabled == true && shownPeriod != nil }
    /// Whether a period has memories (the others are dimmed).
    func has(_ p: Msg_MemoryPeriod) -> Bool { !(answers[p]?.memories.isEmpty ?? true) }
    /// How many things the user has hidden, for Settings.
    var hiddenCount: Int { Int(answers.values.map(\.hiddenCount).max() ?? 0) }

    // MARK: The device's word

    /// The device's Settings: whether it has On this day and it is on.
    func deviceSaid(settings s: Msg_Settings) {
        set(enabled: s.hasMemoriesEnabled ? s.memoriesEnabled : nil)
    }

    /// Settings' switch, or what the device said.
    func set(enabled on: Bool?) {
        guard enabled != on else { return }
        enabled = on
        if on == true {
            load()
        } else {
            forget()
        }
    }

    /// Images shows: today's memories, unless already here (or asked again
    /// a while after the device said it was still measuring).
    func appeared() {
        refreshIfStale()
    }

    private func refreshIfStale() {
        guard enabled == true else { return }
        let today = MemoryDates.day(of: now(), zone: zone())
        if askedDay != today || answers.count < MemoryChoice.periods.count || failed {
            load()
        }
    }

    /// Whether an answer says the device is still measuring.
    var measuring: Bool { answers.values.contains(where: \.measuring) }

    /// Asks the three periods for today, one after the other (the device
    /// keeps each answer for the day). A failure keeps what was there and
    /// asks again 1 s later, doubling to 10 s (at once on a wake); while the
    /// device measures, again every 2 minutes.
    func load() {
        guard enabled == true else { return }
        loading?.cancel()
        measuringTask?.cancel()
        loading = Task { [weak self] in
            var failures = 0
            while let self, !Task.isCancelled, self.enabled == true {
                if await self.ask() {
                    self.failed = false
                    self.whileMeasuring()
                    return
                }
                self.failed = true
                failures += 1
                try? await Task.sleep(for: .seconds(self.retryDelay(failures)))
            }
        }
    }

    private func whileMeasuring() {
        measuringTask?.cancel()
        guard measuring else { return }
        let wait = measuringEvery
        measuringTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(wait))
            guard !Task.isCancelled else { return }
            self?.load()
        }
    }

    /// Waits for the load under way (tests).
    func loaded() async {
        await loading?.value
    }

    /// The three periods asked, awaited: false when one didn't come (asked
    /// again later); true too when the device said it is off or older.
    @discardableResult
    func ask() async -> Bool {
        let day = MemoryDates.day(of: now(), zone: zone())
        var got: [Msg_MemoryPeriod: Msg_Memories] = [:]
        for p in MemoryChoice.periods {
            let resp = try? await request(.reqGetMemories(MemoryDates.request(p, now: now(), zone: zone())))
            guard !Task.isCancelled, let resp else { return false }
            if resp.error {
                if ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) {
                    // A device before On this day: nothing to show until it
                    // says otherwise (the next connection asks Settings).
                    enabled = nil
                    forget()
                    return true
                }
                if resp.errorCode == Self.memoriesOff {
                    // Turned off on another app.
                    enabled = false
                    forget()
                    return true
                }
                return false
            }
            guard case .respMemories(let m) = resp.payload else { return false }
            got[p] = m
        }
        answers = got
        askedDay = day
        askedAt = now()
        return true
    }

    static let memoriesOff = "memories_off"

    // MARK: Hiding

    /// Hides something from the memories; true when the device took it.
    /// The toast says so (with Undo) or gives the device's words.
    @discardableResult
    func hide(_ kind: Msg_MemoryHideKind, _ value: String) async -> Bool {
        var h = Msg_HideMemory()
        h.kind = kind
        h.value = value
        guard await send(h) else { return false }
        lastHidden = (kind, value)
        show(.hidden)
        // A photo or a memory leaves the strip at once; then everything is
        // asked again.
        answers = MemoryHiding.without(kind, value, in: answers)
        load()
        await loaded()
        return true
    }

    /// Undo: what was hidden last shows again.
    func undo() async {
        guard let last = lastHidden else { return }
        var h = Msg_HideMemory()
        h.kind = last.kind
        h.value = last.value
        h.show = true
        guard await send(h) else { return }
        lastHidden = nil
        dismissToast()
        load()
        await loaded()
    }

    /// Settings' "Show hidden memories again": nil when it worked, else
    /// the device's words (or nil words: no answer).
    func showAllHidden() async -> MemoryToast? {
        var h = Msg_HideMemory()
        h.showAll = true
        do {
            let resp = try await request(.reqHideMemory(h))
            if let refused = Self.refusal(resp) { return refused }
        } catch {
            return .failed(nil)
        }
        lastHidden = nil
        load()
        await loaded()
        return nil
    }

    private func send(_ h: Msg_HideMemory) async -> Bool {
        do {
            let resp = try await request(.reqHideMemory(h))
            if let refused = Self.refusal(resp) {
                show(refused)
                return false
            }
            return true
        } catch {
            show(.failed(nil))
            return false
        }
    }

    /// What a HideMemory answer says went wrong: nil when it worked.
    static func refusal(_ resp: Msg_RespEnvelope) -> MemoryToast? {
        if resp.error { return .failed(resp.errorMessage.isEmpty ? nil : resp.errorMessage) }
        if case .respAck(let ack) = resp.payload, !ack.ok {
            return .failed(ack.errorMsg.isEmpty ? nil : ack.errorMsg)
        }
        return nil
    }

    // MARK: Toast

    private func show(_ t: MemoryToast) {
        toast = t
        // A polite live region, as Android's.
        Announce.polite(t.text)
        toastTask?.cancel()
        let wait = toastFor
        toastTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(wait))
            guard !Task.isCancelled else { return }
            self?.toast = nil
        }
    }

    func dismissToast() {
        toastTask?.cancel()
        toast = nil
    }

    // MARK: Thumbnails

    /// A card's cover, when decoded.
    func image(for f: Msg_File) -> UIImage? {
        GridThumbCache.cached(id: f.hash, maxPt: Self.cardSide)
    }

    /// A card came on screen without its cover: from the cache, else the
    /// device.
    func needCover(_ f: Msg_File) {
        guard !f.hash.isEmpty, image(for: f) == nil else { return }
        thumbs.need(GridThumbLoader.Want(id: f.hash, path: f.path, hash: f.hash))
    }

    // MARK: Leaving

    private func forget() {
        loading?.cancel()
        loading = nil
        measuringTask?.cancel()
        measuringTask = nil
        failed = false
        answers = [:]
        askedDay = nil
        lastHidden = nil
        dismissToast()
    }

    /// Log Out: the next device may say otherwise, and another library's
    /// memories mustn't show.
    func reset() {
        forget()
        enabled = nil
        period = nil
        thumbs.reset()
    }
}

/// What a hide takes off the answers at once, before they come again
/// (Android's withoutHidden): a photo from every memory (a memory left with
/// none goes), or a memory by its key.
enum MemoryHiding {
    static func without(_ kind: Msg_MemoryHideKind, _ value: String,
                        in answers: [Msg_MemoryPeriod: Msg_Memories]) -> [Msg_MemoryPeriod: Msg_Memories] {
        answers.mapValues { a in
            var a = a
            switch kind {
            case .memoryHideMemory:
                a.memories.removeAll { $0.key == value }
            case .memoryHidePhoto:
                a.memories = a.memories.compactMap { m in
                    var m = m
                    m.files.removeAll { $0.hash == value }
                    guard let first = m.files.first else { return nil }
                    if m.cover.hash == value { m.cover = first }
                    return m
                }
            default:
                break
            }
            return a
        }
    }
}

/// The memories' message at the foot of the screen.
enum MemoryToast: Equatable {
    /// Something was hidden: with Undo.
    case hidden
    /// The device refused, in its words (nil: no answer, or no words).
    case failed(String?)

    var text: String {
        switch self {
        case .hidden: return S.appPhotosMemoriesHidden
        case .failed(let words): return words ?? S.appPhotosMemoriesHideFailed
        }
    }
}

// MARK: - The strip

/// The strip at the top of Images: its title, Day · Week · Month and a card
/// per memory, scrolling sideways.
struct MemoriesStrip: View {
    @ObservedObject var model: MemoriesModel
    /// Images' named people, for "Don't show <name>".
    let people: [Msg_Person]
    @State private var open: OpenMemory?

    var body: some View {
        if let period = model.shownPeriod {
            VStack(alignment: .leading, spacing: 10) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(MemoryText.title(period))
                        .font(.headline)
                        .accessibilityAddTraits(.isHeader)
                        .lineLimit(1)
                        .minimumScaleFactor(0.8)
                    Spacer(minLength: 4)
                    MemoryPeriodPicker(model: model, shown: period)
                }
                ScrollView(.horizontal, showsIndicators: false) {
                    LazyHStack(spacing: 10) {
                        ForEach(model.shown, id: \.key) { m in
                            Button {
                                open = OpenMemory(memory: m, period: period)
                            } label: {
                                MemoryCard(memory: m, image: m.hasCover ? model.image(for: m.cover) : nil, version: model.thumbsVersion)
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel(MemoryText.cardLabel(m))
                            .onAppear { if m.hasCover { model.needCover(m.cover) } }
                        }
                    }
                }
                .scrollClipDisabled()
            }
            .padding(.top, 12)
            .padding(.bottom, 6)
            .fullScreenCover(item: $open) { o in
                MemoryViewer(model: model, memory: o.memory, period: o.period, people: people)
            }
        }
    }
}

/// A memory being looked at.
struct OpenMemory: Identifiable {
    let memory: Msg_Memory
    let period: Msg_MemoryPeriod
    var id: String { memory.key }
}

/// Day · Week · Month: small capsules; a period with no memories is dimmed
/// and can't be picked.
struct MemoryPeriodPicker: View {
    @ObservedObject var model: MemoriesModel
    let shown: Msg_MemoryPeriod

    var body: some View {
        HStack(spacing: 4) {
            ForEach(MemoryChoice.periods, id: \.rawValue) { p in
                let on = p == shown
                Button {
                    model.period = p
                } label: {
                    Text(MemoryText.choice(p))
                        .font(.caption.weight(.semibold))
                        .lineLimit(1)
                        .padding(.horizontal, 10)
                        .padding(.vertical, 5)
                        .foregroundStyle(on ? MenuStyle.gold : Color.secondary)
                        .background(on ? MenuStyle.activeFill : Color.clear, in: Capsule())
                        .contentShape(Capsule())
                }
                .buttonStyle(.plain)
                .disabled(!model.has(p))
                .opacity(model.has(p) ? 1 : 0.35)
                .accessibilityAddTraits(on ? .isSelected : [])
            }
        }
    }
}

/// A memory's card: its cover, how many years ago and where.
struct MemoryCard: View {
    let memory: Msg_Memory
    let image: UIImage?
    /// The model's thumbsVersion: drawn again when covers arrive.
    let version: Int

    static let width: CGFloat = 120
    static let height: CGFloat = 160

    var body: some View {
        ZStack(alignment: .bottomLeading) {
            Group {
                if let image {
                    Image(uiImage: image).resizable().scaledToFill()
                } else {
                    Rectangle().fill(Color(.systemGray5))
                }
            }
            .frame(width: Self.width, height: Self.height)
            .clipped()
            LinearGradient(colors: [.clear, .black.opacity(0.65)], startPoint: .center, endPoint: .bottom)
            VStack(alignment: .leading, spacing: 1) {
                Text(MemoryText.yearsAgo(memory.yearsAgo))
                    .font(.subheadline.weight(.semibold))
                if !memory.place.isEmpty {
                    Text(memory.place).font(.caption)
                }
            }
            .foregroundStyle(.white)
            .lineLimit(1)
            .minimumScaleFactor(0.75)
            .padding(8)
        }
        .frame(width: Self.width, height: Self.height)
        .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
        .contentShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
    }
}

// MARK: - The slideshow

/// One memory's photos at the size the screen shows them: the big
/// thumbnails (GetThumbnails without small_thumbnails, a 1000 px side), the
/// card's or grid's small one until they come.
@MainActor
final class MemoryPhotos: ObservableObject {
    typealias Request = MemoriesModel.Request

    @Published private(set) var files: [Msg_File]
    @Published private(set) var images: [String: UIImage] = [:]
    var request: Request = { payload in
        try await OTCConnection.shared.ask("tiles", base: OTCConnection.pageTimeout) { $0.payload = payload }
    }
    private var asking: Task<Void, Never>?

    init(files: [Msg_File]) {
        self.files = files
    }

    /// The photo at `i`, at the best size here.
    func image(at i: Int) -> UIImage? {
        guard files.indices.contains(i) else { return nil }
        let f = files[i]
        return images[f.path]
            ?? GridThumbCache.cached(id: f.hash, maxPt: PhotoGridMetrics.decodeSide)
            ?? GridThumbCache.cached(id: f.hash, maxPt: MemoriesModel.cardSide)
    }

    /// A photo hidden: out of the slideshow.
    func remove(hash: String) {
        files.removeAll { $0.hash == hash }
    }

    /// Asks the device for every big thumbnail not here yet (one request
    /// holds them all: 12 at most), the rest again where the answer
    /// stopped short.
    func load() {
        guard asking == nil else { return }
        asking = Task { [weak self] in
            var tries = 0
            while let self, !Task.isCancelled, tries < 3 {
                let wanted = self.files.map(\.path).filter { self.images[$0] == nil }
                guard !wanted.isEmpty else { break }
                tries += 1
                var g = Msg_GetThumbnails()
                g.paths = wanted
                guard let resp = try? await self.request(.reqGetThumbnails(g)),
                      case .respListOfFiles(let lof) = resp.payload else { continue }
                for f in lof.files where !f.content.isEmpty {
                    let data = f.content
                    let decoded = await Task.detached(priority: .userInitiated) { () -> UIImage? in
                        UIImage(data: data)?.preparingForDisplay()
                    }.value
                    if let decoded { self.images[f.path] = decoded }
                }
                if lof.askAgainFrom == 0 { break }
            }
            self?.asking = nil
        }
    }

    func stop() {
        asking?.cancel()
        asking = nil
    }
}

/// A memory full screen, as a slideshow.
struct MemoryViewer: View {
    @ObservedObject var model: MemoriesModel
    let memory: Msg_Memory
    let period: Msg_MemoryPeriod
    let people: [Msg_Person]

    @StateObject private var photos: MemoryPhotos
    @State private var index = 0
    /// How far into the photo on screen, 0 to 1.
    @State private var progress: Double = 0
    @State private var held = false
    /// Paused with the Pause button; a VoiceOver user starts paused.
    @State private var userPaused = UIAccessibility.isVoiceOverRunning
    @State private var menuOpen = false
    @State private var seeAll = false
    @State private var dragDown: CGFloat = 0
    @Environment(\.dismiss) private var dismiss
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.accessibilityVoiceOverEnabled) private var voiceOver

    /// Seconds a photo shows.
    static let perPhoto: Double = 4
    private static let tick: Double = 0.05
    private let clock = Timer.publish(every: MemoryViewer.tick, on: .main, in: .common).autoconnect()

    init(model: MemoriesModel, memory: Msg_Memory, period: Msg_MemoryPeriod, people: [Msg_Person]) {
        self.model = model
        self.memory = memory
        self.period = period
        self.people = people
        _photos = StateObject(wrappedValue: MemoryPhotos(files: memory.files.isEmpty && memory.hasCover ? [memory.cover] : memory.files))
    }

    private var count: Int { photos.files.count }
    /// Paused: a press held, Pause, the menu or See all open.
    private var paused: Bool { held || userPaused || menuOpen || seeAll }

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            photo
                .offset(y: dragDown)
            VStack(spacing: 0) {
                top
                Spacer()
                bottom
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .offset(y: dragDown)
        }
        .overlay(alignment: .bottom) { MemoryToastView(model: model).padding(.bottom, 70) }
        .statusBarHidden()
        .onAppear { photos.load() }
        .onDisappear { photos.stop() }
        .onReceive(clock) { _ in advance() }
        .onChange(of: count) { _, n in
            if n == 0 { dismiss() } else if index >= n { index = n - 1 }
        }
        // Hidden meanwhile (a day or a person here, anything on another
        // app): gone from the answers, it closes.
        .onChange(of: model.answers) { _, answers in
            guard !answers.isEmpty else { return }
            let here = answers.values.contains { $0.memories.contains { $0.key == memory.key } }
            if !here { dismiss() }
        }
        .sheet(isPresented: $seeAll) {
            MemoryGrid(model: model, title: MemoryText.header(memory), files: photos.files)
        }
    }

    // MARK: Parts

    private var photo: some View {
        GeometryReader { g in
            ZStack {
                if let img = photos.image(at: index) {
                    Image(uiImage: img)
                        .resizable()
                        .scaledToFit()
                        .frame(width: g.size.width, height: g.size.height)
                        .id(index)
                        .transition(reduceMotion ? .identity : .opacity)
                } else {
                    ProgressView().tint(.white)
                        .accessibilityLabel(S.commonLoading)
                }
            }
            .frame(width: g.size.width, height: g.size.height)
            .contentShape(Rectangle())
            .onTapGesture(coordinateSpace: .local) { p in
                if p.x < g.size.width / 3 { step(-1) } else { step(1) }
            }
            .onLongPressGesture(minimumDuration: 0.2, maximumDistance: 30) {} onPressingChanged: { held = $0 }
            .gesture(DragGesture(minimumDistance: 20)
                .onChanged { v in dragDown = max(0, v.translation.height) }
                .onEnded { v in
                    if v.translation.height > 120 { dismiss() } else {
                        withAnimation(reduceMotion ? nil : .easeOut(duration: 0.2)) { dragDown = 0 }
                    }
                })
            .accessibilityHidden(true)
            // For VoiceOver the sides are buttons: back on the left third,
            // on elsewhere (Android's).
            .overlay {
                HStack(spacing: 0) {
                    Button { step(-1) } label: { Color.clear.contentShape(Rectangle()) }
                        .frame(width: g.size.width / 3)
                        .accessibilityLabel(S.appPhotosMemoriesPreviousPhoto)
                    Button { step(1) } label: { Color.clear.contentShape(Rectangle()) }
                        .accessibilityLabel([position, S.appPhotosMemoriesNextPhoto].joined(separator: ". "))
                }
                .buttonStyle(.plain)
                .allowsHitTesting(voiceOver)
                .accessibilityHidden(!voiceOver)
            }
        }
        .ignoresSafeArea()
    }

    /// "Photo 2 of 6".
    private var position: String {
        S.appPhotosMemoriesPhotoPosition(index: min(index + 1, max(count, 1)), count: max(count, 1))
    }

    private var top: some View {
        VStack(alignment: .leading, spacing: 10) {
            // One segment a photo: full before the one on screen, filling on it.
            HStack(spacing: 3) {
                ForEach(0..<max(count, 1), id: \.self) { i in
                    GeometryReader { g in
                        Capsule().fill(.white.opacity(0.35))
                            .overlay(alignment: .leading) {
                                Capsule().fill(.white)
                                    .frame(width: g.size.width * (i < index ? 1 : i == index ? progress : 0))
                            }
                    }
                    .frame(height: 3)
                }
            }
            .accessibilityHidden(true)
            HStack(alignment: .top, spacing: 10) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(MemoryText.header(memory))
                        .font(.subheadline.weight(.semibold))
                        .accessibilityAddTraits(.isHeader)
                    if !memory.people.isEmpty {
                        Text(PhotoMonths.peopleTitle(memory.people)).font(.footnote)
                    }
                }
                .foregroundStyle(.white)
                .shadow(color: .black.opacity(0.5), radius: 3)
                Spacer(minLength: 0)
                Button { userPaused.toggle() } label: {
                    Image(systemName: userPaused ? "play.fill" : "pause.fill")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(.white)
                        .frame(width: 44, height: 44)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel(userPaused ? S.commonResume : S.commonPause)
                menu
                Button { dismiss() } label: {
                    Image(systemName: "xmark")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(.white)
                        .frame(width: 44, height: 44)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel(S.commonClose)
            }
        }
    }

    private var menu: some View {
        Menu {
            Button(S.appPhotosMemoriesHidePhoto) { hidePhoto() }
            Button(S.appPhotosMemoriesHideMemory) { hide(.memoryHideMemory, memory.key, closes: true) }
            if let day = photoDay {
                Button(S.appPhotosMemoriesHideDay) { hide(.memoryHideDay, day, closes: true) }
            }
            ForEach(namedPeople, id: \.id) { p in
                Button(S.appPhotosMemoriesHidePerson(name: p.name)) { hide(.memoryHidePerson, p.id, closes: true) }
            }
        } label: {
            Image(systemName: "ellipsis")
                .font(.body.weight(.semibold))
                .foregroundStyle(.white)
                .frame(width: 44, height: 44)
                .contentShape(Rectangle())
        }
        .accessibilityLabel(S.appPhotosMemoriesOptions)
        // The slideshow waits while the menu is open (a pick or the next
        // tap goes on), so a hide is for the photo it was opened over.
        .simultaneousGesture(TapGesture().onEnded { menuOpen = true })
    }

    private var bottom: some View {
        HStack {
            Spacer()
            Button {
                userPaused = true
                seeAll = true
            } label: {
                Text(S.appPhotosMemoriesSeeAll)
                    .font(.subheadline.weight(.semibold))
                    .padding(.horizontal, 16)
                    .padding(.vertical, 9)
            }
            .buttonStyle(.plain)
            .foregroundStyle(MenuStyle.emberInk)
            .background(MenuStyle.ember, in: Capsule())
        }
    }

    // MARK: Stepping

    private func advance() {
        guard !paused, count > 0 else { return }
        progress += Self.tick / Self.perPhoto
        if progress >= 1 {
            if index + 1 < count {
                go(to: index + 1)
            } else {
                // The last photo stays.
                progress = 1
            }
        }
    }

    private func step(_ by: Int) {
        let to = index + by
        guard (0..<count).contains(to) else {
            if by > 0 { progress = 1 } else { progress = 0 }
            return
        }
        go(to: to)
    }

    private func go(to i: Int) {
        if reduceMotion {
            index = i
        } else {
            withAnimation(.easeInOut(duration: 0.25)) { index = i }
        }
        progress = 0
        menuOpen = false
    }

    // MARK: Hiding

    /// The day of the photo on screen, in the phone's time zone.
    private var photoDay: String? {
        guard photos.files.indices.contains(index) else { return nil }
        let f = photos.files[index]
        guard f.hasCreated else { return nil }
        return MemoryDates.day(of: f.created.date, zone: model.zone())
    }

    /// The memory's named people Images knows by id.
    private var namedPeople: [Msg_Person] {
        MemoryPeopleMatch.of(names: memory.people, people: people)
    }

    private func hidePhoto() {
        guard photos.files.indices.contains(index) else { return }
        let hash = photos.files[index].hash
        menuOpen = false
        Task {
            if await model.hide(.memoryHidePhoto, hash) {
                photos.remove(hash: hash)
                progress = 0
            }
        }
    }

    private func hide(_ kind: Msg_MemoryHideKind, _ value: String, closes: Bool) {
        menuOpen = false
        Task {
            if await model.hide(kind, value), closes { dismiss() }
        }
    }
}

/// The people of a memory (names, as the device sends them) that Images'
/// list of people has by id: those "Don't show" can name.
enum MemoryPeopleMatch {
    static func of(names: [String], people: [Msg_Person]) -> [Msg_Person] {
        var out: [Msg_Person] = []
        for n in names where !n.isEmpty {
            // A name two people share is left out: which one is meant can't
            // be told.
            let same = people.filter { $0.name == n }
            if same.count == 1, !out.contains(where: { $0.id == same[0].id }) { out.append(same[0]) }
        }
        return out
    }
}

/// See all: the memory's photos in a grid; a tap opens the viewer.
struct MemoryGrid: View {
    @ObservedObject var model: MemoriesModel
    let title: String
    let files: [Msg_File]
    @StateObject private var viewer = PhotoGalleryVM()
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 100), spacing: 2)], spacing: 2) {
                    ForEach(Array(files.enumerated()), id: \.element.path) { i, f in
                        Button { open(i) } label: {
                            Color(.systemGray5)
                                .aspectRatio(1, contentMode: .fit)
                                .overlay {
                                    if let img = model.image(for: f) ?? GridThumbCache.cached(id: f.hash, maxPt: PhotoGridMetrics.decodeSide) {
                                        Image(uiImage: img).resizable().scaledToFill()
                                    }
                                }
                                .clipped()
                                .id("\(f.path)#\(model.thumbsVersion)")
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel(PhotoMonths.tileLabel(video: f.mime.hasPrefix("video/"), date: f.hasCreated ? f.created.date : nil))
                        .onAppear { model.needCover(f) }
                    }
                }
            }
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(S.commonClose) { dismiss() }
                }
            }
        }
        .fullScreenCover(isPresented: Binding(
            get: { viewer.openIndex != nil },
            set: { if !$0 { viewer.closeModal() } }
        )) {
            ImageModal(
                vm: viewer,
                save: {
                    let path = viewer.openPath
                    let fallback = viewerThumb()
                    viewer.saveOriginalToPhotos {
                        guard let path else { return fallback }
                        return await viewer.fullImage(of: path) ?? fallback
                    }
                },
                share: {
                    let fallback = viewerThumb()
                    Task { viewer.shareCurrentPhoto(await viewer.fullImageForOpen() ?? fallback) }
                },
                delete: { viewer.deleteCurrentPhoto(everyCopy: $0) }
            )
        }
    }

    private func open(_ i: Int) {
        // A delete there asks for the memories again.
        viewer.onDeleted = { [model] _ in model.load() }
        viewer.showFiles(files.map { f in
            PhotoGalleryVM.Item(
                id: PhotoPaging.itemID(f), path: f.path, mime: f.mime, size: Int(f.fileSize),
                thumbData: nil, localURL: nil, isLocalOnly: false,
                thumbImage: model.image(for: f) ?? GridThumbCache.cached(id: f.hash, maxPt: PhotoGridMetrics.decodeSide),
                created: f.hasCreated ? f.created.date : nil, hash: f.hash
            )
        }, startAt: i)
    }

    private func viewerThumb() -> UIImage? {
        guard let i = viewer.openIndex, viewer.items.indices.contains(i) else { return nil }
        return viewer.items[i].thumbImage
    }
}

/// "Hidden from your memories" with Undo, or the device's words, at the
/// foot of the screen.
struct MemoryToastView: View {
    @ObservedObject var model: MemoriesModel

    var body: some View {
        if let t = model.toast {
            HStack(spacing: 12) {
                Text(t.text)
                    .font(.subheadline)
                    .foregroundStyle(.primary)
                if t == .hidden {
                    Button(S.appPhotosMemoriesUndo) { Task { await model.undo() } }
                        .font(.subheadline.weight(.semibold))
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
            .background(.regularMaterial, in: Capsule())
            .accessibilityElement(children: .combine)
            .transition(.opacity)
        }
    }
}
