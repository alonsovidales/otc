// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  Tour.swift
//  OffTheCloud
//
//  The first-run tour (the owner's request of 2026-10-27: how the app
//  works, how to publish on Social and the rest, and it can be skipped). A
//  full-screen pager that RootView lays over MainView (TourOverlay): an
//  overlay rather than a sheet, so it never meets SyncChoice's sheet or a
//  tab's own, which open above it.
//
//  It opens by itself once: when the phone is signed in, MainView is up
//  (not the connection screen, the Bluetooth setup or a card saying the
//  device can't be reached), the sync question is answered and this phone
//  hasn't seen this version of it - an install that was signed in before
//  the update that brought it included. "Seen" is marked the moment it
//  opens by itself, so Skip, Done or leaving the app midway all leave it
//  seen; defaults that can't be opened mean it doesn't open by itself.
//  Settings' Getting started shows it again from its first page, the mark
//  untouched.
//
//  The mark is this phone's alone - no protocol change, nothing told to the
//  device (the demo is one shared account) - in defaults of their own,
//  which Log Out's removal of the app's domain doesn't reach: a Log Out and
//  a new sign-in don't show it again. Raise TourRules.version only to show
//  a rewritten tour again.
//
//  Pages: Welcome, Images, this phone's photo backup, People (only while
//  face recognition is on, as in the menu), Collections, Files, Social,
//  publishing a post, Friends, Alerts and Settings, the end. Each label a
//  page names comes from that label's own key, so the pages follow it in
//  every language. Where People, Collections and Friends open follows
//  MainView's own layout rule, live as the phone turns (WideLayout.menu).
//  The same pages, keys and rules as Tour.kt on Android (TourTests.swift <->
//  TourTest.kt, the same cases).
//

import SwiftUI
import UIKit

/// The tour's pages, in their fixed order.
enum TourStep: String, CaseIterable {
    case welcome, images, backup, people, collections, files, social, post, friends, alerts, done
}

/// The tour's rules, apart from any view (TourTests).
enum TourRules {
    /// The tour this build shows: a phone that saw an older one sees it
    /// again. Raise it only for a rewritten tour.
    static let version = 1

    /// The pages shown: People only while face recognition is on (nil,
    /// not known yet, leaves it out as the menu does).
    static func steps(faces: Bool?) -> [TourStep] {
        TourStep.allCases.filter { $0 != .people || faces == true }
    }

    /// Whether the tour opens by itself now. `seenVersion` nil: the mark
    /// can't be read, so it doesn't.
    static func shouldShow(seenVersion: Int?, syncChoicePending: Bool, signedIn: Bool, mainVisible: Bool) -> Bool {
        guard let seen = seenVersion else { return false }
        return seen < version && signedIn && mainVisible && !syncChoicePending
    }

    /// The page after `step` among `steps`, by the fixed order (nil after
    /// the last). `step` itself may be gone from them.
    static func after(_ step: TourStep, in steps: [TourStep]) -> TourStep? {
        let at = order(step)
        return steps.first { order($0) > at }
    }

    /// The page before `step` among `steps` (nil before the first).
    static func before(_ step: TourStep, in steps: [TourStep]) -> TourStep? {
        let at = order(step)
        return steps.last { order($0) < at }
    }

    /// Where `step` is among `steps`: itself, or - gone, as People when
    /// face recognition is turned off - the next page in the fixed order.
    static func place(_ step: TourStep, in steps: [TourStep]) -> TourStep {
        if steps.contains(step) { return step }
        return after(step, in: steps) ?? before(step, in: steps) ?? .welcome
    }

    private static func order(_ step: TourStep) -> Int {
        TourStep.allCases.firstIndex(of: step) ?? 0
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

/// Whether the tour is up and on which page. One for the app (`shared`);
/// tests make their own.
@MainActor
final class TourModel: ObservableObject {
    static let shared = TourModel()

    /// Up: TourOverlay draws it over MainView.
    @Published private(set) var shown = false
    /// The page on screen, by its id: People can come or go meanwhile
    /// (TourRules.place says where it is then).
    @Published var step: TourStep = .welcome

    private let seen: TourSeen

    init(seen: TourSeen = .live) {
        self.seen = seen
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

    /// Settings' Show the tour: from the first page, the mark untouched.
    func replay() {
        open()
    }

    /// Next: the page after, or Done on the last.
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

    /// Skip, Done, Escape.
    func close() {
        shown = false
    }

    /// Log Out (AppLogOut): closed, back at the first page. The mark stays.
    func reset() {
        shown = false
        step = .welcome
    }

    private func open() {
        step = .welcome
        shown = true
    }
}

// MARK: - Over MainView

/// RootView's layer for the tour: drawn over MainView, which VoiceOver
/// leaves alone meanwhile, and asked to open by itself whenever what it
/// waits for changes.
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
                TourView(model: tour)
                    .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
                    .zIndex(1)
            }
        }
        .animation(.easeInOut(duration: reduceMotion ? 0.2 : 0.3), value: shown)
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

// MARK: - The pager

/// The tour itself: where it is and Skip at the top, a page per step in
/// the middle (swiped, or Back and Next), the dots and the buttons below.
struct TourView: View {
    @ObservedObject var model: TourModel
    @ObservedObject private var faces = FaceRecognition.shared
    @AppStorage(WideLayout.openKey) private var menuOpen = true
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    /// The window's width, safe areas included, as MainView measures it.
    @State private var width: CGFloat

    init(model: TourModel) {
        self.model = model
        let window = UIApplication.shared.connectedScenes
            .compactMap { ($0 as? UIWindowScene)?.keyWindow }
            .first
        _width = State(initialValue: window?.bounds.width ?? 0)
    }

    var body: some View {
        let steps = TourRules.steps(faces: faces.enabled)
        let current = TourRules.place(model.step, in: steps)
        let index = steps.firstIndex(of: current) ?? 0
        // MainView's rule: the menu beside the page from 600 points.
        let wide = WideLayout.menu(width: width, open: menuOpen) != .none
        VStack(spacing: 0) {
            header(index: index, total: steps.count)
            TabView(selection: Binding(get: { current }, set: { model.step = $0 })) {
                ForEach(steps, id: \.self) { step in
                    TourPage(step: step, wide: wide)
                        .tag(step)
                }
            }
            .tabViewStyle(.page(indexDisplayMode: .never))
            footer(steps: steps, current: current, index: index)
        }
        .background(Color(.systemBackground).ignoresSafeArea())
        .onGeometryChange(for: CGFloat.self) { proxy in
            proxy.size.width + proxy.safeAreaInsets.leading + proxy.safeAreaInsets.trailing
        } action: { width = $0 }
        .accessibilityElement(children: .contain)
        .accessibilityAddTraits(.isModal)
        // VoiceOver's two-finger scrub: Skip.
        .accessibilityAction(.escape) { model.close() }
        .onAppear { UIAccessibility.post(notification: .screenChanged, argument: nil) }
        .onChange(of: current) { _, _ in UIAccessibility.post(notification: .screenChanged, argument: nil) }
    }

    private func header(index: Int, total: Int) -> some View {
        HStack(alignment: .center, spacing: 12) {
            Text(S.appTourProgress(step: index + 1, total: total))
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
            Button(S.appTourSkip) { model.close() }
                // An iPad keyboard's Escape.
                .keyboardShortcut(.cancelAction)
                .frame(minWidth: 44, minHeight: 44)
        }
        .frame(maxWidth: 520)
        .padding(.horizontal, 20)
        .frame(maxWidth: .infinity)
    }

    private func footer(steps: [TourStep], current: TourStep, index: Int) -> some View {
        let first = index == 0
        let last = index == steps.count - 1
        return TourFooterLayout {
            // Kept in its place from page 1, so the dots don't move.
            Button { turn { model.back(in: steps) } } label: {
                Text(S.commonBack)
                    .frame(minWidth: 44, minHeight: 44)
            }
            .opacity(first ? 0 : 1)
            .disabled(first)
            .accessibilityHidden(first)

            TourDots(steps: steps, current: current)

            Button { turn { model.next(in: steps) } } label: {
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
        }
        .frame(maxWidth: 520)
        .padding(.horizontal, 20)
        .padding(.top, 8)
        .padding(.bottom, 12)
        .frame(maxWidth: .infinity)
    }

    /// Another page: slid to, or there at once with Reduce Motion on.
    private func turn(_ change: () -> Void) {
        if reduceMotion {
            change()
        } else {
            withAnimation(.easeInOut(duration: 0.3)) { change() }
        }
    }
}

/// One dot per page, the current one in ember; out of VoiceOver's way (the
/// header says where the tour is).
private struct TourDots: View {
    let steps: [TourStep]
    let current: TourStep

    var body: some View {
        HStack(spacing: 7) {
            ForEach(steps, id: \.self) { step in
                Circle()
                    .fill(step == current ? MenuStyle.ember : Color.secondary.opacity(0.35))
                    .frame(width: 7, height: 7)
            }
        }
        .accessibilityHidden(true)
    }
}

/// Back, the dots and Next on one line while they fit with the dots in the
/// middle; else the dots on a line of their own above the buttons, and at
/// the largest text sizes Next above Back. Each is laid out once.
private struct TourFooterLayout: Layout {
    private let gap: CGFloat = 12

    private enum Shape { case row, dotsAbove, stacked }

    private func shape(_ sizes: [CGSize], width: CGFloat) -> Shape {
        let back = sizes[0].width, dots = sizes[1].width, next = sizes[2].width
        if 2 * max(back, next) + dots + 2 * gap <= width { return .row }
        if back + next + gap <= width { return .dotsAbove }
        return .stacked
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let sizes = subviews.map { $0.sizeThatFits(.unspecified) }
        guard sizes.count == 3 else { return .zero }
        let width = proposal.width ?? (2 * max(sizes[0].width, sizes[2].width) + sizes[1].width + 2 * gap)
        switch shape(sizes, width: width) {
        case .row:
            return CGSize(width: width, height: sizes.map(\.height).max() ?? 0)
        case .dotsAbove:
            return CGSize(width: width, height: sizes[1].height + gap + max(sizes[0].height, sizes[2].height))
        case .stacked:
            return CGSize(width: width, height: sizes[1].height + sizes[2].height + sizes[0].height + 2 * gap)
        }
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let sizes = subviews.map { $0.sizeThatFits(.unspecified) }
        guard sizes.count == 3 else { return }
        let fit = ProposedViewSize(width: bounds.width, height: nil)
        switch shape(sizes, width: bounds.width) {
        case .row:
            subviews[0].place(at: CGPoint(x: bounds.minX, y: bounds.midY), anchor: .leading, proposal: .unspecified)
            subviews[1].place(at: CGPoint(x: bounds.midX, y: bounds.midY), anchor: .center, proposal: .unspecified)
            subviews[2].place(at: CGPoint(x: bounds.maxX, y: bounds.midY), anchor: .trailing, proposal: .unspecified)
        case .dotsAbove:
            subviews[1].place(at: CGPoint(x: bounds.midX, y: bounds.minY), anchor: .top, proposal: .unspecified)
            let y = bounds.minY + sizes[1].height + gap + max(sizes[0].height, sizes[2].height) / 2
            subviews[0].place(at: CGPoint(x: bounds.minX, y: y), anchor: .leading, proposal: .unspecified)
            subviews[2].place(at: CGPoint(x: bounds.maxX, y: y), anchor: .trailing, proposal: .unspecified)
        case .stacked:
            var y = bounds.minY
            subviews[1].place(at: CGPoint(x: bounds.midX, y: y), anchor: .top, proposal: .unspecified)
            y += sizes[1].height + gap
            subviews[2].place(at: CGPoint(x: bounds.midX, y: y), anchor: .top, proposal: fit)
            y += sizes[2].height + gap
            subviews[0].place(at: CGPoint(x: bounds.midX, y: y), anchor: .top, proposal: fit)
        }
    }
}

// MARK: - The pages

/// One page: its picture, its title and its text, centred, scrolling when
/// the text is large.
private struct TourPage: View {
    let step: TourStep
    /// MainView's wide layout: People, Collections and Friends are in the menu.
    let wide: Bool

    /// Shorter than this (a phone on its side), the picture and the gaps
    /// shrink so the text fits.
    private static let compactHeight: CGFloat = 440

    /// Text out of sight above or below (large text): a hairline marks that
    /// edge, so a line cut at it doesn't read as lying under the buttons.
    private struct Hidden: Equatable {
        var above = false
        var below = false
    }

    @State private var hidden = Hidden()

    var body: some View {
        GeometryReader { proxy in
            let compact = proxy.size.height < Self.compactHeight
            ScrollView {
                VStack(spacing: compact ? 8 : 16) {
                    TourArt(step: step, compact: compact)
                        .padding(.bottom, compact ? 4 : 8)
                    Text(title)
                        .font(.title2.weight(.bold))
                        .multilineTextAlignment(.center)
                        .accessibilityAddTraits(.isHeader)
                    content
                }
                .frame(maxWidth: 520)
                .padding(.horizontal, 24)
                .padding(.vertical, compact ? 8 : 16)
                .frame(maxWidth: .infinity, minHeight: proxy.size.height)
            }
            .scrollBounceBehavior(.basedOnSize)
            .onScrollGeometryChange(for: Hidden.self) { g in
                Hidden(above: g.visibleRect.minY > 1, below: g.visibleRect.maxY < g.contentSize.height - 1)
            } action: { _, now in hidden = now }
            .overlay(alignment: .top) { if hidden.above { Divider() } }
            .overlay(alignment: .bottom) { if hidden.below { Divider() } }
        }
    }

    private static var bold: AttributeContainer {
        var b = AttributeContainer()
        b.inlinePresentationIntent = .stronglyEmphasized
        return b
    }

    private var title: String {
        switch step {
        case .welcome: return S.appTourWelcomeTitle
        case .images: return S.appShellSectionImages
        case .backup: return S.appTourBackupTitle
        case .people: return S.appShellSectionPeople
        case .collections: return S.appShellSectionCollections
        case .files: return S.appShellSectionFiles
        case .social: return S.appShellSectionSocial
        case .post: return S.appTourPostTitle
        case .friends: return S.appShellSectionFriends
        case .alerts: return S.appTourAlertsTitle
        case .done: return S.appTourDoneTitle
        }
    }

    @ViewBuilder
    private var content: some View {
        let b = Self.bold
        switch step {
        case .welcome:
            paragraph(AttributedString(S.appTourWelcomeText))
        case .images:
            paragraph(AttributedString(S.appTourImagesText))
        case .backup:
            paragraph(S.appTourBackupText(settings: S.appShellSectionSettings, sync: S.appSettingsSyncTitle, b: b))
        case .people:
            paragraph(S.appTourPeopleText(name: S.appPhotosPeopleAddName, b: b))
            whereLine(wide ? S.appTourPeopleWhereWide : S.appTourPeopleWhere)
        case .collections:
            paragraph(S.appTourCollectionsText(add: S.appShellAddToCollection, b: b))
            whereLine(wide ? S.appTourCollectionsWhereWide : S.appTourCollectionsWhere)
        case .files:
            paragraph(S.appTourFilesText(upload: S.commonUpload, b: b))
        case .social:
            paragraph(AttributedString(S.appTourSocialText))
        case .post:
            VStack(alignment: .leading, spacing: 14) {
                numbered(1, S.appTourPostStep1(b: b))
                numbered(2, S.appTourPostStep2(phone: S.appSocialComposeSourcePhone, synced: S.appSocialComposeSourceSynced, b: b))
                numbered(3, S.appTourPostStep3(publish: S.appSocialComposePublish, b: b))
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        case .friends:
            paragraph(S.appTourFriendsAdd(suffix: SecretsStore.bridgeDomain, heading: S.appSocialFriendsAddHeading, send: S.commonSend, b: b))
            paragraph(S.appTourFriendsAccept(waiting: S.appSocialFriendsWaitingAnswer, accept: S.commonAccept, b: b))
            whereLine(wide ? S.appTourFriendsWhereWide : S.appTourFriendsWhere)
        case .alerts:
            paragraph(S.appTourAlertsText(alerts: S.appShellSectionAlerts, settings: S.appShellSectionSettings, b: b))
        case .done:
            paragraph(S.appTourDoneText(show: S.appTourShow, settings: S.appShellSectionSettings, b: b))
        }
    }

    private func paragraph(_ text: AttributedString) -> some View {
        Text(text)
            .font(.body)
            .multilineTextAlignment(.center)
            .fixedSize(horizontal: false, vertical: true)
    }

    /// Where the section opens, in the layout on screen.
    private func whereLine(_ text: String) -> some View {
        Text(text)
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .multilineTextAlignment(.center)
            .fixedSize(horizontal: false, vertical: true)
    }

    /// A step of the post page: its number in an ember circle, then the step.
    private func numbered(_ number: Int, _ text: AttributedString) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Text(verbatim: String(number))
                .font(.subheadline.weight(.bold))
                .foregroundStyle(MenuStyle.ember)
                .frame(width: 28, height: 28)
                .background(MenuStyle.ember.opacity(0.15), in: Circle())
            Text(text)
                .font(.body)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.top, 3)
        }
        .accessibilityElement(children: .combine)
    }
}

/// A page's picture: the logo on the first, else the section's own icon in
/// an ember circle (the post page the composer's plus, Alerts and Settings
/// both icons), two thirds of its size on a `compact` page. Decoration only.
private struct TourArt: View {
    let step: TourStep
    let compact: Bool

    private var circle: CGFloat { compact ? 64 : 96 }
    private var iconSize: CGFloat { compact ? 32 : 48 }

    var body: some View {
        Group {
            switch step {
            case .welcome:
                Image("OTCLogo")
                    .resizable()
                    .scaledToFit()
                    .frame(height: compact ? 56 : 84)
            case .post:
                badge(width: circle) {
                    Image(systemName: "plus.circle")
                        .font(.system(size: iconSize - 2))
                }
            case .alerts:
                badge(width: circle + iconSize + 8) {
                    HStack(spacing: iconSize / 3) {
                        NavIconView(.alerts, size: iconSize)
                        NavIconView(.settings, size: iconSize)
                    }
                }
            default:
                badge(width: circle) {
                    NavIconView(icon, size: iconSize)
                }
            }
        }
        .accessibilityHidden(true)
    }

    private var icon: NavIcon {
        switch step {
        case .images: return .images
        case .backup: return .storage
        case .people: return .people
        case .collections: return .collections
        case .files: return .files
        case .social: return .social
        case .friends: return .friends
        default: return .settings
        }
    }

    private func badge<Content: View>(width: CGFloat, @ViewBuilder content: () -> Content) -> some View {
        content()
            .foregroundStyle(MenuStyle.ember)
            .frame(width: width, height: circle)
            .background(MenuStyle.ember.opacity(0.15), in: Capsule())
    }
}
