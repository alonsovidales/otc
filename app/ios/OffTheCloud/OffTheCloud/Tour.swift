// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  Tour.swift
//  OffTheCloud
//
//  The first-run tour, tour 2 (the owner's request of 2026-10-27: show how
//  things work, in each section, rather than say what each section is). A
//  card RootView lays over MainView (TourOverlay), one step at a time:
//  each step opens the section it talks about (MainView follows
//  TourModel.step through TourRules.section), dims the app around the
//  real control it teaches - a hole with an ember ring, where the control
//  is on screen (TourTargets, marked with .tourTarget where each control is
//  drawn) - and shows a small moving drawing of that control in use
//  (TourArt.swift), so it teaches an empty library as well. Ending it -
//  Skip, Done, Escape - goes back to the section it opened over. It never
//  opens Alerts, which marks every alert read: its last step points at
//  Alerts and shows Settings. Nothing it does sends a request that changes
//  anything (the sections it opens make their usual reads).
//
//  It opens by itself once: when the phone is signed in, MainView is up
//  (not the connection screen, the Bluetooth setup or a card saying the
//  device can't be reached), the sync question is answered and this phone
//  hasn't seen this version of it. "Seen" is marked the moment it opens by
//  itself, so Skip, Done or leaving the app midway all leave it seen;
//  defaults that can't be opened mean it doesn't open by itself.
//  Settings' Getting started shows it again from its first step, the mark
//  untouched. The mark is this phone's alone, in defaults of their own,
//  which Log Out's removal of the app's domain doesn't reach. Raise
//  TourRules.version only to show a rewritten tour again (2: a phone that
//  saw the first tour sees this one once).
//
//  Steps: welcome, search, dates, select, viewer, people (only while face
//  recognition is on), collections, files, upload, social, post, friends,
//  backup, users (only for the device's owner, who alone can add users),
//  alerts. Every label a step quotes comes from that label's own key. The
//  same steps, keys and rules as Tour.kt on Android (TourTests.swift <->
//  TourTest.kt, the same cases).
//

import SwiftUI
import UIKit

/// The tour's steps, in their fixed order.
enum TourStep: String, CaseIterable {
    case welcome, search, dates, select, viewer, people, collections, files, upload, social, post, friends, backup, users, alerts
}

/// The real controls the steps ring, marked where they are drawn
/// (.tourTarget); the bottom bar's are UIKit's (TabBarProbe).
enum TourTarget: String, CaseIterable, Hashable {
    /// Where the sections are: the bottom bar, or the menu on a wide window.
    case sections
    /// The search field: Images' own, or the wide top bar's.
    case search
    /// The date scrubber's strip along the photos' right edge.
    case scrubber
    /// The grid's first photo.
    case firstPhoto
    /// Beside Images' search on a narrow window.
    case peopleButton, collectionsButton
    /// The People and Collections pages' first face and card (wide).
    case firstFace, firstCollection
    /// Files' View options, its Upload and the first folder's lock.
    case viewOptions, upload, folderLock
    /// The first post's heart and comment field.
    case like, comment
    /// Social's + (narrow) or the top bar's + New post (wide).
    case newPost
    /// Social's two-people button (narrow).
    case friendsButton
    /// Friends' own address and the add-a-friend row.
    case address, addFriend
    /// Settings: Sync Options' Wi-Fi only, Users' add row and bridge
    /// switch, Getting started's Show the tour.
    case wifiOnly, addUser, bridgeAccess, showTour
    /// Alerts and Settings: their tabs, or the menu's items.
    case alertsSection, settingsSection
}

/// The tour's rules, apart from any view (TourTests).
enum TourRules {
    /// The tour this build shows: a phone that saw an older one sees it
    /// again. Raise it only for a rewritten tour.
    static let version = 2

    /// The steps shown: People only while face recognition is on, Users only
    /// for the device's owner (nil, not known yet, leaves them out, as the
    /// menu and Settings do).
    static func steps(faces: Bool?, owner: Bool?) -> [TourStep] {
        TourStep.allCases.filter {
            switch $0 {
            case .people: return faces == true
            case .users: return owner == true
            default: return true
            }
        }
    }

    /// Whether the tour opens by itself now. `seenVersion` nil: the mark
    /// can't be read, so it doesn't.
    static func shouldShow(seenVersion: Int?, syncChoicePending: Bool, signedIn: Bool, mainVisible: Bool) -> Bool {
        guard let seen = seenVersion else { return false }
        return seen < version && signedIn && mainVisible && !syncChoicePending
    }

    /// The step after `step` among `steps`, by the fixed order (nil after
    /// the last). `step` itself may be gone from them.
    static func after(_ step: TourStep, in steps: [TourStep]) -> TourStep? {
        let at = order(step)
        return steps.first { order($0) > at }
    }

    /// The step before `step` among `steps` (nil before the first).
    static func before(_ step: TourStep, in steps: [TourStep]) -> TourStep? {
        let at = order(step)
        return steps.last { order($0) < at }
    }

    /// Where `step` is among `steps`: itself, or - gone, as People when
    /// face recognition is turned off - the next step in the fixed order.
    static func place(_ step: TourStep, in steps: [TourStep]) -> TourStep {
        if steps.contains(step) { return step }
        return after(step, in: steps) ?? before(step, in: steps) ?? .welcome
    }

    private static func order(_ step: TourStep) -> Int {
        TourStep.allCases.firstIndex(of: step) ?? 0
    }

    /// The section a step shows (nil: the one the tour opened over). On a
    /// narrow window People and Collections stay Images, whose buttons
    /// beside the search the step rings, and Friends stays Social - their
    /// sheets would cover the card. Never Alerts: opening it marks every
    /// alert read.
    static func section(_ step: TourStep, wide: Bool) -> AppSection? {
        switch step {
        case .welcome: return nil
        case .search, .dates, .select, .viewer: return .images
        case .people: return wide ? .people : .images
        case .collections: return wide ? .collections : .images
        case .files, .upload: return .files
        case .social, .post: return .social
        case .friends: return wide ? .friends : .social
        case .backup, .users, .alerts: return .settings
        }
    }

    /// What a step rings, those of them on screen.
    static func targets(_ step: TourStep, wide: Bool) -> [TourTarget] {
        switch step {
        case .welcome: return [.sections]
        case .search: return [.search]
        case .dates: return [.scrubber]
        case .select, .viewer: return [.firstPhoto]
        case .people: return wide ? [.firstFace] : [.peopleButton]
        case .collections: return wide ? [.firstCollection] : [.collectionsButton]
        case .files: return [.viewOptions]
        case .upload: return [.upload, .folderLock]
        case .social: return [.like, .comment]
        case .post: return [.newPost]
        case .friends: return wide ? [.address, .addFriend] : [.friendsButton]
        case .backup: return [.wifiOnly]
        case .users: return [.addUser, .bridgeAccess]
        // Show the tour sits too near the top of Settings to scroll clear
        // of a narrow window's card, which the bottom bar puts at the top.
        case .alerts: return wide ? [.alertsSection, .settingsSection, .showTour] : [.alertsSection, .settingsSection]
        }
    }

    /// Where Settings scrolls for a step: the control, at this share of
    /// its height from the top - clear of the card, which goes at the foot
    /// of a narrow window. The last step brings Show the tour back in sight
    /// (rung only on a wide window), where the tour ends.
    static func settingsScroll(_ step: TourStep, wide: Bool) -> (target: TourTarget, at: CGFloat)? {
        switch step {
        case .backup: return (.wifiOnly, wide ? 0.4 : 0.15)
        case .users: return (.addUser, wide ? 0.4 : 0.15)
        case .alerts: return (.showTour, wide ? 0.4 : 0.5)
        default: return nil
        }
    }

    // MARK: Holes and the card

    /// The hole's margin around its control, in points.
    static let holePad: CGFloat = 6
    /// Kept this far inside the window, so the ring shows whole.
    static let holeInset: CGFloat = 2
    /// Between the card and the window's edges (its safe area).
    static let cardMargin: CGFloat = 12
    /// Between the card and a hole beside it.
    static let cardGap: CGFloat = 16
    /// The card beside the rings on a wide window.
    static let cardWidth: CGFloat = 380
    /// The card at the foot (or top) of a narrow window, at most.
    static let sheetMaxWidth: CGFloat = 560

    /// The holes for the controls in `rects` (window coordinates), one
    /// each, padded and kept inside `bounds`: those not on screen left
    /// out, those that would touch made one (the dimmed layer is drawn
    /// even-odd, where two over each other would dim again).
    static func holes(_ rects: [CGRect], in bounds: CGRect) -> [CGRect] {
        let inner = bounds.insetBy(dx: holeInset, dy: holeInset)
        guard inner.width > 0, inner.height > 0 else { return [] }
        var holes = rects
            .filter { $0.width > 0 && $0.height > 0 && $0.intersects(bounds) }
            .map { $0.insetBy(dx: -holePad, dy: -holePad).intersection(inner) }
            .filter { !$0.isNull && $0.width > 0 && $0.height > 0 }
        var merged = true
        while merged {
            merged = false
            outer: for i in holes.indices {
                for j in holes.indices where j > i {
                    let a = holes[i], b = holes[j]
                    if a.minX <= b.maxX && b.minX <= a.maxX && a.minY <= b.maxY && b.minY <= a.maxY {
                        holes[i] = a.union(b)
                        holes.remove(at: j)
                        merged = true
                        break outer
                    }
                }
            }
        }
        return holes
    }

    /// How much of the holes a card at `frame` covers.
    static func covered(_ frame: CGRect, holes: [CGRect]) -> CGFloat {
        holes.reduce(0) { sum, h in
            let i = frame.intersection(h)
            return sum + (i.isNull ? 0 : i.width * i.height)
        }
    }

    /// Where the card goes. `area` is the window's safe area, `card` the
    /// card's size (its width is ours: TourRules.cardWidth, or the sheet's).
    /// A narrow window (`sheet`) has it across the foot, else across the top
    /// or in the middle, whichever covers less of the holes. A wide one has
    /// it beside them -
    /// to the right, below, to the left or above, the roomiest first - and
    /// where no side has room, the corner that covers the least; in the
    /// middle when nothing is ringed.
    static func cardFrame(holes: [CGRect], card: CGSize, area: CGRect, sheet: Bool) -> CGRect {
        let m = cardMargin
        let height = min(card.height, max(0, area.height - 2 * m))
        if sheet {
            let width = min(area.width - 2 * m, sheetMaxWidth)
            let x = area.midX - width / 2
            let bottom = CGRect(x: x, y: area.maxY - m - height, width: width, height: height)
            let top = CGRect(x: x, y: area.minY + m, width: width, height: height)
            let middle = CGRect(x: x, y: area.midY - height / 2, width: width, height: height)
            return [bottom, top, middle].min { covered($0, holes: holes) < covered($1, holes: holes) } ?? bottom
        }
        let width = min(card.width, area.width - 2 * m)
        func clampX(_ x: CGFloat) -> CGFloat { max(area.minX + m, min(x, area.maxX - m - width)) }
        func clampY(_ y: CGFloat) -> CGFloat { max(area.minY + m, min(y, area.maxY - m - height)) }
        let middle = CGRect(x: clampX(area.midX - width / 2), y: clampY(area.midY - height / 2), width: width, height: height)
        guard let first = holes.first else { return middle }
        let u = holes.dropFirst().reduce(first) { $0.union($1) }
        func fits(_ r: CGRect) -> Bool {
            r.minX >= area.minX + m - 0.5 && r.minY >= area.minY + m - 0.5
                && r.maxX <= area.maxX - m + 0.5 && r.maxY <= area.maxY - m + 0.5
        }
        let sides: [(room: CGFloat, frame: CGRect)] = [
            ((area.maxX - u.maxX - cardGap - m) / width,
             CGRect(x: u.maxX + cardGap, y: clampY(u.midY - height / 2), width: width, height: height)),
            ((area.maxY - u.maxY - cardGap - m) / height,
             CGRect(x: clampX(u.midX - width / 2), y: u.maxY + cardGap, width: width, height: height)),
            ((u.minX - cardGap - m - area.minX) / width,
             CGRect(x: u.minX - cardGap - width, y: clampY(u.midY - height / 2), width: width, height: height)),
            ((u.minY - cardGap - m - area.minY) / height,
             CGRect(x: clampX(u.midX - width / 2), y: u.minY - cardGap - height, width: width, height: height)),
        ].sorted { $0.room > $1.room }
        for s in sides where fits(s.frame) && covered(s.frame, holes: holes) == 0 { return s.frame }
        let spots = [
            CGRect(x: clampX(area.maxX), y: clampY(area.maxY), width: width, height: height),
            CGRect(x: clampX(area.minX), y: clampY(area.maxY), width: width, height: height),
            CGRect(x: clampX(area.maxX), y: clampY(area.minY), width: width, height: height),
            CGRect(x: clampX(area.minX), y: clampY(area.minY), width: width, height: height),
            middle,
        ]
        return spots.min { covered($0, holes: holes) < covered($1, holes: holes) } ?? middle
    }
}

/// Where "seen" is kept: on this phone only.
struct TourSeen {
    /// The version seen (0 for none); nil when it can't be read.
    let read: () -> Int?
    /// Marks a version seen; false when it couldn't be kept.
    let write: (Int) -> Bool

    /// Defaults of their own, apart from the app's domain (which Log Out
    /// removes), as LanguageSettings keeps the language.
    static let suiteName = "cloud.off-the.OffTheCloud.tour"
    static let key = "seenVersion"

    init(read: @escaping () -> Int?, write: @escaping (Int) -> Bool) {
        self.read = read
        self.write = write
    }

    /// In `defaults`; nil (they couldn't be opened) reads and keeps nothing.
    init(defaults: UserDefaults?) {
        read = { defaults.map { $0.integer(forKey: TourSeen.key) } }
        write = { version in
            guard let defaults else { return false }
            defaults.set(version, forKey: TourSeen.key)
            return defaults.integer(forKey: TourSeen.key) == version
        }
    }

    static var live: TourSeen { TourSeen(defaults: UserDefaults(suiteName: suiteName)) }
}

/// Whether the tour is up and on which step. One for the app (`shared`);
/// tests make their own.
@MainActor
final class TourModel: ObservableObject {
    static let shared = TourModel()

    /// Up: TourOverlay draws it over MainView.
    @Published private(set) var shown = false
    /// The step on screen, by its id: People and Users can come or go
    /// meanwhile (TourRules.place says where it is then).
    @Published var step: TourStep = .welcome
    /// Whether this account is the device's owner (Settings' Users shows
    /// for it alone): asked as the tour opens; nil until known.
    @Published private(set) var owner: Bool?

    private let seen: TourSeen
    private let role: () async -> Bool?

    init(seen: TourSeen = .live, role: @escaping () async -> Bool? = { await UsersManagementViewModel.askIsPrimary() }) {
        self.seen = seen
        self.role = role
    }

    /// Opens the tour by itself when TourRules.shouldShow says so, marking
    /// it seen first: one that can't be marked doesn't open.
    func evaluate(syncChoicePending: Bool, signedIn: Bool, mainVisible: Bool) {
        guard !shown,
              TourRules.shouldShow(seenVersion: seen.read(), syncChoicePending: syncChoicePending,
                                   signedIn: signedIn, mainVisible: mainVisible),
              seen.write(TourRules.version)
        else { return }
        open()
    }

    /// Settings' Show the tour: from the first step, the mark untouched.
    func replay() {
        open()
    }

    /// Next: the step after, or Done on the last.
    func next(in steps: [TourStep]) {
        if let after = TourRules.after(TourRules.place(step, in: steps), in: steps) {
            step = after
        } else {
            close()
        }
    }

    func back(in steps: [TourStep]) {
        if let before = TourRules.before(TourRules.place(step, in: steps), in: steps) {
            step = before
        }
    }

    /// Skip, Done, Escape: MainView goes back to where it opened.
    func close() {
        shown = false
    }

    /// Log Out (AppLogOut): closed, back at the first step. The mark stays.
    func reset() {
        shown = false
        step = .welcome
        owner = nil
    }

    /// Who is signed in, for the Users step: asked once a tour opens.
    func learnOwner() async {
        let known = await role()
        if let known { owner = known }
    }

    private func open() {
        step = .welcome
        shown = true
        Task { await learnOwner() }
    }
}

// MARK: - The controls on screen

/// Where the controls the tour rings are, in the window's coordinates:
/// each mark (.tourTarget) keeps its own frame here while it is on screen.
/// Kept quietly while the tour is down; while it is up (`live`) every
/// change is published, once a run of the main loop.
@MainActor
final class TourTargets: ObservableObject {
    static let shared = TourTargets()

    /// One mark's last frame, kept without redrawing its view.
    final class Mark {
        let id = UUID()
        var rect: CGRect = .zero
    }

    @Published private(set) var version = 0
    private var frames: [TourTarget: [UUID: CGRect]] = [:]
    private var pending = false

    var live = false {
        didSet { if live && !oldValue { changed() } }
    }

    func set(_ target: TourTarget, _ id: UUID, _ rect: CGRect) {
        guard rect.width > 0, rect.height > 0 else { return }
        if frames[target]?[id] == rect { return }
        frames[target, default: [:]][id] = rect
        changed()
    }

    func remove(_ target: TourTarget, _ id: UUID) {
        guard frames[target]?.removeValue(forKey: id) != nil else { return }
        changed()
    }

    func rects(_ target: TourTarget) -> [CGRect] {
        Array(frames[target]?.values ?? [:].values)
    }

    private func changed() {
        guard live, !pending else { return }
        pending = true
        DispatchQueue.main.async {
            self.pending = false
            self.version += 1
        }
    }
}

private struct TourTargetMark: ViewModifier {
    let target: TourTarget
    @State private var mark = TourTargets.Mark()

    func body(content: Content) -> some View {
        content
            .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { rect in
                mark.rect = rect
                TourTargets.shared.set(target, mark.id, rect)
            }
            .onAppear { TourTargets.shared.set(target, mark.id, mark.rect) }
            .onDisappear { TourTargets.shared.remove(target, mark.id) }
    }
}

extension View {
    /// Marks the control a tour step rings (nil: none).
    @ViewBuilder
    func tourTarget(_ target: TourTarget?) -> some View {
        if let target {
            modifier(TourTargetMark(target: target))
        } else {
            self
        }
    }
}

/// The bottom bar is UIKit's (TabView): its frame and its five items'
/// (Alerts, Social, Files, Images, Settings, MainView's order), in the
/// window's coordinates, from the bar itself.
@MainActor
enum TabBarProbe {
    static func frames() -> (bar: CGRect, items: [CGRect])? {
        guard let window = UIApplication.shared.connectedScenes
            .compactMap({ ($0 as? UIWindowScene)?.keyWindow }).first,
              let bar = find(in: window),
              bar.window != nil, !bar.isHidden, bar.alpha > 0.01
        else { return nil }
        // The items' own views, left to right; a bar drawn otherwise is
        // shared out evenly.
        var buttons: [UIView] = []
        collect(in: bar, into: &buttons)
        var items = buttons
            .filter { !$0.isHidden && $0.alpha > 0.01 && $0.bounds.width > 8 }
            .map { $0.convert($0.bounds, to: nil) }
            .sorted { $0.minX < $1.minX }
        let frame = bar.convert(bar.bounds, to: nil)
        if items.count != 5 {
            let w = frame.width / 5
            items = (0..<5).map { CGRect(x: frame.minX + CGFloat($0) * w, y: frame.minY, width: w, height: min(frame.height, 56)) }
        }
        let shown = items.dropFirst().reduce(items[0]) { $0.union($1) }
        return (shown, items)
    }

    private static func find(in view: UIView) -> UITabBar? {
        if let bar = view as? UITabBar { return bar }
        for sub in view.subviews {
            if let bar = find(in: sub) { return bar }
        }
        return nil
    }

    private static func collect(in view: UIView, into out: inout [UIView]) {
        for sub in view.subviews {
            let name = String(describing: type(of: sub))
            if name.contains("TabBarButton") || name.contains("TabButton") {
                out.append(sub)
            } else {
                collect(in: sub, into: &out)
            }
        }
    }
}

// MARK: - Over MainView

/// RootView's layer for the tour: drawn over MainView, which VoiceOver
/// leaves alone meanwhile, and asked to open by itself whenever what it
/// waits for changes. Sheets and dialogs open above it.
struct TourOverlay: ViewModifier {
    /// The secrets are set: MainView is up, not the connection screen or
    /// the Bluetooth setup.
    let configured: Bool
    let syncChoicePending: Bool
    @ObservedObject private var tour = TourModel.shared
    @ObservedObject private var connection = OTCConnection.shared
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    init(configured: Bool, syncChoicePending: Bool) {
        self.configured = configured
        self.syncChoicePending = syncChoicePending
    }

    private var shown: Bool { tour.shown && configured }

    func body(content: Content) -> some View {
        ZStack {
            content
                .accessibilityHidden(shown)
            if shown {
                TourLayer(model: tour)
                    .transition(.opacity)
                    .zIndex(1)
            }
        }
        .animation(.easeInOut(duration: reduceMotion ? 0.15 : 0.25), value: shown)
        .onChange(of: shown, initial: true) { _, now in TourTargets.shared.live = now }
        .onChange(of: inputs, initial: true) { _, now in
            tour.evaluate(syncChoicePending: now.syncChoicePending, signedIn: now.signedIn, mainVisible: now.mainVisible)
        }
    }

    private struct Inputs: Equatable {
        let syncChoicePending: Bool
        let signedIn: Bool
        let mainVisible: Bool
    }

    /// MainView with no card over it: neither the bridge's verdict that the
    /// device can't be reached nor connecting that keeps failing (the card
    /// MainChrome shows a few seconds into it).
    private var inputs: Inputs {
        Inputs(
            syncChoicePending: syncChoicePending,
            signedIn: connection.authenticated,
            mainVisible: configured && connection.statusCode == nil && !connection.connectionFailed
        )
    }
}

/// The dimmed app with its holes and rings, and the card.
struct TourLayer: View {
    @ObservedObject var model: TourModel
    @ObservedObject private var targets = TourTargets.shared
    @ObservedObject private var faces = FaceRecognition.shared
    @AppStorage(WideLayout.openKey) private var menuOpen = true
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.colorScheme) private var colorScheme
    @State private var cardSize = CGSize(width: TourRules.cardWidth, height: 420)
    /// Bumped a moment after each step, for the bottom bar (UIKit's, which
    /// says nothing as it moves).
    @State private var settle = 0

    var body: some View {
        GeometryReader { geo in
            let window = geo.frame(in: .global)
            let bounds = CGRect(origin: .zero, size: geo.size)
            let wide = WideLayout.menu(width: geo.size.width, open: menuOpen) != .none
            let steps = TourRules.steps(faces: faces.enabled, owner: model.owner)
            let current = TourRules.place(model.step, in: steps)
            let holes = TourRules.holes(rects(current, wide: wide, window: window), in: bounds)
            // The window's own safe area: this layer ignores it, so its
            // proxy has none to say.
            let area = bounds.inset(by: Self.windowInsets)
            let sheet = !wide
            let frame = TourRules.cardFrame(holes: holes, card: cardSize, area: area, sheet: sheet)
            // As tall as its content, up to the window's height.
            let tallest = max(0, area.height - 2 * TourRules.cardMargin)
            ZStack(alignment: .topLeading) {
                TourDim(holes: holes, dark: colorScheme == .dark)
                    .animation(reduceMotion ? nil : .easeInOut(duration: 0.3), value: holes)
                TourCard(model: model, steps: steps, current: current, wide: wide, compact: geo.size.height < 560,
                         maxHeight: tallest)
                    .frame(width: frame.width)
                    // The card's own height, at the width it was given.
                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { height in
                        if abs(height - cardSize.height) > 0.5 { cardSize = CGSize(width: TourRules.cardWidth, height: height) }
                    }
                    .offset(x: frame.minX, y: frame.minY)
                    .animation(reduceMotion ? nil : .easeInOut(duration: 0.3), value: frame)
            }
            .frame(width: geo.size.width, height: geo.size.height, alignment: .topLeading)
        }
        .ignoresSafeArea()
        .task(id: model.step) {
            // The bottom bar and a section opening settle after the step.
            for _ in 0..<3 {
                try? await Task.sleep(for: .milliseconds(250))
                settle += 1
            }
        }
    }

    private static var windowInsets: UIEdgeInsets {
        UIApplication.shared.connectedScenes.compactMap { ($0 as? UIWindowScene)?.keyWindow }.first?.safeAreaInsets ?? .zero
    }

    /// The step's controls on screen, in this layer's coordinates.
    private func rects(_ step: TourStep, wide: Bool, window: CGRect) -> [CGRect] {
        _ = targets.version
        _ = settle
        #if DEBUG
        // The UI checks of an empty library: no control rung, the drawing alone.
        if ProcessInfo.processInfo.environment["OTC_TOUR_NO_TARGETS"] == "1" { return [] } // i18n-ignore: a debug switch's name
        #endif
        let bar = wide ? nil : TabBarProbe.frames()
        return TourRules.targets(step, wide: wide).flatMap { t -> [CGRect] in
            if let bar {
                switch t {
                case .sections: return [bar.bar]
                case .alertsSection: return [bar.items[0]]
                case .settingsSection: return [bar.items[4]]
                default: break
                }
            }
            return targets.rects(t)
        }
        .map { $0.offsetBy(dx: -window.minX, dy: -window.minY) }
    }
}

/// The app dimmed but for the holes, each ringed in ember. Every touch on
/// it stops there: the card is the only thing to use while the tour shows.
private struct TourDim: View {
    let holes: [CGRect]
    let dark: Bool

    var body: some View {
        ZStack(alignment: .topLeading) {
            DimShape(holes: holes)
                .fill(Color.black.opacity(dark ? 0.62 : 0.5), style: FillStyle(eoFill: true))
            ForEach(Array(holes.enumerated()), id: \.offset) { _, h in
                RoundedRectangle(cornerRadius: min(14, min(h.width, h.height) / 2), style: .continuous)
                    .stroke(MenuStyle.ember, lineWidth: 2)
                    .frame(width: h.width, height: h.height)
                    .offset(x: h.minX, y: h.minY)
            }
        }
        .contentShape(Rectangle())
        .onTapGesture {}
        .accessibilityHidden(true)
    }
}

private struct DimShape: Shape {
    var holes: [CGRect]

    func path(in rect: CGRect) -> Path {
        var p = Path(rect)
        for h in holes {
            p.addRoundedRect(in: h, cornerSize: CGSize(width: min(14, min(h.width, h.height) / 2),
                                                        height: min(14, min(h.width, h.height) / 2)), style: .continuous)
        }
        return p
    }
}

// MARK: - The card

/// One step: where the tour is and Skip, the drawing, the title and the
/// text (scrolling when the text is large), Back and Next.
private struct TourCard: View {
    @ObservedObject var model: TourModel
    let steps: [TourStep]
    let current: TourStep
    let wide: Bool
    /// A short window (a phone on its side): a smaller drawing.
    let compact: Bool
    let maxHeight: CGFloat
    @ObservedObject private var faces = FaceRecognition.shared
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let index = steps.firstIndex(of: current) ?? 0
        UpTo(maxHeight: maxHeight) {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .center, spacing: 12) {
                Text(S.appTourProgress(step: index + 1, total: steps.count))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
                Button(S.appTourSkip) { model.close() }
                    // An iPad keyboard's Escape.
                    .keyboardShortcut(.cancelAction)
                    .frame(minWidth: 44, minHeight: 44)
            }
            ViewThatFits(in: .vertical) {
                content
                ScrollView { content }
                    .scrollBounceBehavior(.basedOnSize)
            }
            footer(first: index == 0, last: index == steps.count - 1)
        }
        .padding(.horizontal, 18)
        .padding(.top, 6)
        .padding(.bottom, 14)
        }
        .background(
            RoundedRectangle(cornerRadius: 22, style: .continuous)
                .fill(MenuStyle.drawer)
                .shadow(color: .black.opacity(0.25), radius: 18, y: 6)
        )
        .overlay(RoundedRectangle(cornerRadius: 22, style: .continuous).stroke(MenuStyle.line, lineWidth: 0.5))
        .accessibilityElement(children: .contain)
        .accessibilityAddTraits(.isModal)
        // VoiceOver's two-finger scrub: Skip.
        .accessibilityAction(.escape) { model.close() }
        .onAppear { UIAccessibility.post(notification: .screenChanged, argument: nil) }
        .onChange(of: current) { _, _ in UIAccessibility.post(notification: .screenChanged, argument: nil) }
    }

    private var content: some View {
        VStack(alignment: .leading, spacing: 10) {
            TourArt(step: current, faces: faces.isOn, wide: wide, compact: compact)
                .id(current)
            Text(TourTexts.title(current))
                .font(.title3.weight(.bold))
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
            Text(TourTexts.text(current, wide: wide, faces: faces.isOn))
                .font(.callout)
                .foregroundStyle(.primary.opacity(0.85))
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func footer(first: Bool, last: Bool) -> some View {
        let back = Button { turn { model.back(in: steps) } } label: {
            Text(S.commonBack)
                .padding(.horizontal, 8)
                .frame(minWidth: 44, minHeight: 36)
        }
        .buttonStyle(.bordered)
        let next = Button { turn { model.next(in: steps) } } label: {
            Text(last ? S.commonDone : S.commonNext)
                .fontWeight(.semibold)
                // The web's --ember-ink on --ember: white on ember is
                // too faint to read.
                .foregroundStyle(MenuStyle.emberInk)
                .padding(.horizontal, 8)
                .frame(minWidth: 44, minHeight: 36)
        }
        .buttonStyle(.borderedProminent)
        .tint(MenuStyle.ember)
        // An iPad keyboard's Return.
        .keyboardShortcut(.defaultAction)
        return ViewThatFits(in: .horizontal) {
            HStack(spacing: 12) {
                Spacer(minLength: 0)
                if !first { back }
                next
            }
            VStack(spacing: 8) {
                next.frame(maxWidth: .infinity)
                if !first { back.frame(maxWidth: .infinity) }
            }
        }
    }

    /// Another step: the holes and the card move to it, or are there at
    /// once with Reduce Motion on.
    private func turn(_ change: () -> Void) {
        if reduceMotion {
            change()
        } else {
            withAnimation(.easeInOut(duration: 0.3)) { change() }
        }
    }
}

/// As tall as what it holds, up to `maxHeight` - where the card's text then
/// scrolls (its ViewThatFits) - without growing to the height it is offered,
/// as a `frame(maxHeight:)` would.
private struct UpTo: Layout {
    let maxHeight: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard let child = subviews.first else { return .zero }
        let ideal = child.sizeThatFits(ProposedViewSize(width: proposal.width, height: nil))
        let height = min(ideal.height, maxHeight)
        return CGSize(width: proposal.width ?? ideal.width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        subviews.first?.place(at: bounds.origin, proposal: ProposedViewSize(width: bounds.width, height: bounds.height))
    }
}

/// The steps' titles and texts, every quoted label from its own key.
@MainActor
enum TourTexts {
    private static var bold: AttributeContainer {
        var b = AttributeContainer()
        b.inlinePresentationIntent = .stronglyEmphasized
        return b
    }

    static func title(_ step: TourStep) -> String {
        switch step {
        case .welcome: return S.appTourWelcomeTitle
        case .search: return S.appTourSearchTitle
        case .dates: return S.appTourDatesTitle
        case .select: return S.appTourSelectTitle
        case .viewer: return S.appTourViewerTitle
        case .people: return S.appTourPeopleTitle
        case .collections: return S.appTourCollectionsTitle
        case .files: return S.appTourFilesTitle
        case .upload: return S.appTourUploadTitle
        case .social: return S.appTourSocialTitle
        case .post: return S.appTourPostTitle
        case .friends: return S.appTourFriendsTitle
        case .backup: return S.appTourBackupTitle
        case .users: return S.appTourUsersTitle
        case .alerts: return S.appTourAlertsTitle
        }
    }

    static func text(_ step: TourStep, wide: Bool, faces: Bool) -> AttributedString {
        let b = bold
        switch step {
        case .welcome:
            return AttributedString(wide ? S.appTourWelcomeTextWide : S.appTourWelcomeText)
        case .search:
            return AttributedString(faces ? S.appTourSearchTextFaces : S.appTourSearchText)
        case .dates:
            return AttributedString(S.appTourDatesText)
        case .select:
            return S.appTourSelectText(share: S.commonShare, download: S.commonDownload, add: S.appShellAddToCollection,
                                       delete: S.commonDelete, b: b)
        case .viewer:
            return S.appTourViewerText(info: S.appPhotosViewerMoreInfo, b: b)
        case .people:
            return wide
                ? S.appTourPeopleTextWide(name: S.appPhotosPeopleAddName, merge: S.appPhotosPeopleMerge, b: b)
                : S.appTourPeopleText(name: S.appPhotosPeopleAddName, merge: S.appPhotosPeopleMerge, b: b)
        case .collections:
            return wide
                ? S.appTourCollectionsTextWide(add: S.appShellAddToCollection, new: S.appPhotosCollectionsNewItem, b: b)
                : S.appTourCollectionsText(add: S.appShellAddToCollection, new: S.appPhotosCollectionsNewItem, b: b)
        case .files:
            return S.appTourFilesText(list: S.appFilesViewList, grid: S.appFilesViewGrid, storage: S.appFilesViewStorage,
                                      sort: S.appFilesSortBy, b: b)
        case .upload:
            return S.appTourUploadText(upload: S.commonUpload, share: S.commonShare, download: S.commonDownload, b: b)
        case .social:
            return AttributedString(S.appTourSocialText)
        case .post:
            return S.appTourPostText(phone: S.appSocialComposeSourcePhone, synced: S.appSocialComposeSourceSynced,
                                     publish: S.appSocialComposePublish, b: b)
        case .friends:
            return wide
                ? S.appTourFriendsTextWide(add: S.appSocialFriendsAddHeading, send: S.commonSend,
                                           waiting: S.appSocialFriendsWaitingAnswer, accept: S.commonAccept, b: b)
                : S.appTourFriendsText(add: S.appSocialFriendsAddHeading, send: S.commonSend,
                                       waiting: S.appSocialFriendsWaitingAnswer, accept: S.commonAccept, b: b)
        case .backup:
            return S.appTourBackupText(sync: S.appSettingsSyncTitle, wifi: S.appSettingsSyncWifiOnly, b: b)
        case .users:
            // As the web's: the switch's sentence after the step's own.
            var text = S.appTourUsersText(settings: S.appShellSectionSettings, users: S.appSettingsUsersTitle, add: S.commonAdd, b: b)
            text.append(AttributedString(" ")) // i18n-ignore: the space between two sentences
            text.append(S.appTourUsersBridge(bridge: S.appSettingsUsersRequestBridge, b: b))
            return text
        case .alerts:
            return S.appTourAlertsText(alerts: S.appShellSectionAlerts, settings: S.appShellSectionSettings, show: S.appTourShow, b: b)
        }
    }
}
