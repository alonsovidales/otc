// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  AppMenu.swift
//  OffTheCloud
//
//  The wide layout: a window 600 points wide or more - a phone on its
//  side, an unfolded phone, an iPad, a wide enough split screen - is laid
//  out as the web app lays out every window (web/src/App.tsx,
//  components/Sidebar.tsx). A top bar with the menu button, the logo and
//  the search, and a menu down the left in place of the bottom bar:
//
//    - 1024 points and wider: the whole menu (256 wide: pill rows under
//      their headings, storage at the foot), or a rail of icons if the
//      menu button folded it - remembered, as the web's otc_menu_open;
//    - 600 to 1023: always the rail (80 wide: each icon in a pill with
//      its label under it, a hairline between the groups, storage as a
//      ring), and the menu button lays the whole menu over the page.
//
//  The sections are the web's: Images, People (only while face
//  recognition is on), Collections, Files | Sharing: Social, Friends |
//  Device: Alerts, Settings. People, Collections and Friends are pages of
//  their own here; on a narrow window they are sheets over Images and
//  Social, and MainView moves one between the two when the window
//  changes. Narrower than 600 the app keeps its bottom bar, unchanged.
//
//  Colours: the web's, in the phone's light or dark - its gold marks the
//  open section (darker in light mode, to be read on white), its ember
//  the unread count.
//

import SwiftUI
import UIKit

// MARK: - Sections and widths

/// A section of the app as the wide layout's menu lists it. Most are a
/// tab of the bottom bar; People, Collections and Friends are pages that
/// a narrow window opens as sheets from Images and Social.
enum AppSection: String, CaseIterable, Hashable {
    case images, people, collections, files, social, friends, alerts, settings

    var label: String {
        switch self {
        case .images: return "Images"
        case .people: return "People"
        case .collections: return "Collections"
        case .files: return "Files"
        case .social: return "Social"
        case .friends: return "Friends"
        case .alerts: return "Alerts"
        case .settings: return "Settings"
        }
    }

    var icon: NavIcon {
        switch self {
        case .images: return .images
        case .people: return .people
        case .collections: return .collections
        case .files: return .files
        case .social: return .social
        case .friends: return .friends
        case .alerts: return .alerts
        case .settings: return .settings
        }
    }

    /// The bottom bar's tab (MainView's tags) that shows this section, or
    /// that opens it as a sheet on a narrow window.
    var tab: Int {
        switch self {
        case .alerts: return 0
        case .social, .friends: return 1
        case .files: return 3
        case .images, .people, .collections: return 4
        case .settings: return 5
        }
    }

    /// A page of its own only in the wide layout: a sheet otherwise.
    var isPage: Bool { self == .people || self == .collections || self == .friends }

    /// The section a bottom bar tab shows.
    static func forTab(_ tab: Int) -> AppSection {
        switch tab {
        case 0: return .alerts
        case 1: return .social
        case 3: return .files
        case 5: return .settings
        default: return .images
        }
    }

    /// The menu's groups, as the web's Sidebar: what is stored, what is
    /// shared, then the device. People only while face recognition is on.
    static func menu(faces: Bool) -> [MenuGroup] {
        [
            MenuGroup(heading: nil, items: faces ? [.images, .people, .collections, .files] : [.images, .collections, .files]),
            MenuGroup(heading: "Sharing", items: [.social, .friends]),
            MenuGroup(heading: "Device", items: [.alerts, .settings]),
        ]
    }
}

struct MenuGroup: Hashable {
    let heading: String?
    let items: [AppSection]
}

/// The web's widths (index.css): where the layout changes, and what the
/// menu and the top bar take.
enum WideLayout {
    /// Narrower than this: the bottom bar.
    static let minWidth: CGFloat = 600
    /// This and wider: the whole menu, or the rail by choice.
    static let fullWidth: CGFloat = 1024
    static let menuWidth: CGFloat = 256
    static let railWidth: CGFloat = 80
    static let topBarHeight: CGFloat = 64
    /// Shorter than this the rail tightens, as the web's max-height query.
    static let shortHeight: CGFloat = 680

    enum Menu: Equatable {
        /// The bottom bar's layout.
        case none
        case rail
        case full
    }

    /// The menu beside the page for a window this wide; `open` is the menu
    /// button's remembered choice, which only a full-width window has.
    static func menu(width: CGFloat, open: Bool) -> Menu {
        if width < minWidth { return .none }
        if width >= fullWidth { return open ? .full : .rail }
        return .rail
    }

    /// The UserDefaults key of the menu button's choice (on unless "0", as
    /// the web's otc_menu_open).
    static let openKey = "menuOpen"
}

private struct WideLayoutKey: EnvironmentKey {
    static let defaultValue = false
}

extension EnvironmentValues {
    /// The window is laid out wide (AppMenu.swift): the menu has People,
    /// Collections and Friends, and the top bar the search and the logo,
    /// so the screens leave out their own.
    var wideLayout: Bool {
        get { self[WideLayoutKey.self] }
        set { self[WideLayoutKey.self] = newValue }
    }
}

/// A page's name for VoiceOver alone, where the wide layout's menu names
/// the page on screen (the web keeps such a heading for screen readers).
struct HiddenPageHeading: View {
    let title: String

    var body: some View {
        Color.clear
            .frame(width: 1, height: 1)
            .accessibilityElement()
            .accessibilityLabel(title)
            .accessibilityAddTraits(.isHeader)
    }
}

/// Where the top bar's search field is, for its panel to hang from
/// (MainView): resolved in the panel's own coordinates, so the two line up
/// whatever lies between them.
struct SearchFieldAnchorKey: PreferenceKey {
    static let defaultValue: Anchor<CGRect>? = nil
    static func reduce(value: inout Anchor<CGRect>?, nextValue: () -> Anchor<CGRect>?) {
        value = value ?? nextValue()
    }
}

// MARK: - Colours

/// The web's tokens (index.css), in the phone's light or dark.
enum MenuStyle {
    private static func dynamic(light: UIColor, dark: UIColor) -> Color {
        Color(UIColor { $0.userInterfaceStyle == .dark ? dark : light })
    }

    private static func rgb(_ hex: UInt32, _ alpha: CGFloat = 1) -> UIColor {
        UIColor(
            red: CGFloat((hex >> 16) & 0xFF) / 255,
            green: CGFloat((hex >> 8) & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255,
            alpha: alpha
        )
    }

    /// The open section's label and icon: --gold; a darker gold on white.
    static let gold = dynamic(light: rgb(0x8F6200), dark: rgb(0xFFC857))
    /// The whole menu's open row.
    static let activeFill = dynamic(light: rgb(0xFFC857, 0.32), dark: rgb(0xFFC857, 0.14))
    /// The rail's open pill.
    static let railActiveFill = dynamic(light: rgb(0xFFC857, 0.36), dark: rgb(0xFFC857, 0.16))
    /// The storage gauge's ring and meter.
    static let meter = dynamic(light: rgb(0xE0A100), dark: rgb(0xFFC857))
    /// --ember and the text on it: the unread count, a storage running out.
    static let ember = Color(UIColor(red: 1, green: 0.42, blue: 0.29, alpha: 1))
    static let emberInk = Color(rgb(0x26100A))
    /// --danger: storage nearly full, a degraded mirror, the attention dot.
    static let danger = dynamic(light: .systemRed, dark: rgb(0xFF8A75))
    /// --line: hairlines.
    static let line = Color(.separator)
    /// A pressed row or pill.
    static let press = Color.primary.opacity(0.08)
    /// The bar and the menu: the page's own background, as on the web.
    static let bar = Color(.systemBackground)
    /// The menu laid over the page (--ink-2): a step off the page in the dark.
    static let drawer = dynamic(light: .systemBackground, dark: .secondarySystemBackground)
    /// The search field (--ink-3).
    static let field = Color(.tertiarySystemFill)
}

// MARK: - Storage

/// The device's storage as the menu's foot shows it - the web's
/// Sidebar.summarize, from the same status poll Settings makes.
struct StorageSummary: Equatable {
    enum Level: Equatable { case ok, warn, crit }

    struct Row: Equatable, Hashable {
        let label: String
        let value: String
    }

    var known = false
    var usedPct: Double = 0
    var level: Level = .ok
    var degraded = false
    var attention = false
    var usedText = "Reading…"
    var mirrorText = ""
    var details: [Row] = []
    var errors: [String] = []

    init(status: Msg_Status?, error: String?) {
        guard let s = status else {
            usedText = error == nil ? "Reading…" : "Storage unavailable right now"
            return
        }
        known = true
        usedPct = Self.pct(Double(s.raidUsage), Double(s.raidSize))
        level = usedPct >= 90 ? .crit : usedPct >= 70 ? .warn : .ok
        degraded = s.raidState == .raidDegraded
        attention = degraded || !s.errors.isEmpty
        usedText = "\(sizeText(Double(s.raidUsage))) of \(sizeText(Double(s.raidSize))) used"
        mirrorText = Self.mirrorLabel(s)
        details = [
            Row(label: "Disks", value: s.disks == 0 ? "—" : "\(s.disks)"),
            Row(label: "Free", value: sizeText(Double(max(0, Int64(s.raidSize) - Int64(s.raidUsage))))),
            Row(label: "System card", value: "\(sizeText(Double(s.diskUsage))) of \(sizeText(Double(s.diskSize)))"),
            Row(label: "CPU", value: "\(Self.trim(Self.round(Double(s.cpuUsagePrc))))%"),
            Row(label: "Memory", value: "\(sizeText(Double(s.memUsage))) of \(sizeText(Double(s.memSize)))"),
        ]
        errors = s.errors.map { $0.message.isEmpty ? "\($0.statusErrorCode)" : $0.message }
    }

    /// The rail's figure under its ring.
    var gaugeText: String { known ? "\(Int(usedPct.rounded()))%" : "–" }

    /// Between 0 and 100, to two decimals (the web's round).
    static func round(_ x: Double) -> Double { max(0, min(100, (x * 100).rounded() / 100)) }

    static func pct(_ used: Double, _ total: Double) -> Double {
        guard used > 0, total > 0 else { return 0 }
        return round(used / total * 100)
    }

    private static func trim(_ x: Double) -> String {
        x == x.rounded() ? String(Int(x)) : String(x)
    }

    /// The mirror's health, worded as the web's raidStateLabel.
    static func mirrorLabel(_ s: Msg_Status) -> String {
        switch s.raidState {
        case .raidNone: return "No mirror"
        case .raidInSync: return "Mirror in sync"
        case .raidSyncing: return "Mirror syncing (\(trim(round(Double(s.raidSyncPercent))))%)"
        case .raidDegraded: return "Mirror degraded"
        default: return "Mirror state unknown"
        }
    }

    var color: Color {
        switch level {
        case .ok: return MenuStyle.meter
        case .warn: return MenuStyle.ember
        case .crit: return MenuStyle.danger
        }
    }
}

// MARK: - The menu

/// The menu beside the page (whole or as a rail), or laid over it.
struct SideMenu: View {
    /// .rail or .full.
    let layout: WideLayout.Menu
    let groups: [MenuGroup]
    /// The section shown (Collections while a collection is open in Images).
    let current: AppSection?
    let unread: Int
    let storage: StorageSummary
    /// A window shorter than the web's 680: the rail tightens.
    let short: Bool
    /// The whole menu's storage details are open.
    @Binding var details: Bool
    /// The rail's gauge asked for the details: the whole menu that shows
    /// next scrolls to them.
    @Binding var revealStorage: Bool
    let onPick: (AppSection) -> Void
    /// The rail's gauge: the whole menu, at the storage.
    var onStorage: () -> Void = {}

    @Environment(\.openURL) private var openURL
    @State private var viewport: CGFloat = 0
    /// Where each section's row is in the visible part of the menu, kept
    /// without redrawing the menu as it scrolls.
    @State private var rows = RowFrames()

    private final class RowFrames {
        var frames: [AppSection: CGRect] = [:]
    }

    private static let space = "sideMenuViewport"

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView(.vertical) {
                VStack(spacing: 0) {
                    if layout == .rail {
                        rail
                    } else {
                        full
                    }
                }
                // As tall as the menu at least, so the storage sits at its
                // foot (the web's .sb-fill) unless there is no room.
                .frame(minHeight: viewport, alignment: .top)
            }
            .coordinateSpace(.named(Self.space))
            .scrollIndicators(layout == .rail ? .hidden : .automatic)
            .scrollBounceBehavior(.basedOnSize)
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { viewport = $0 }
            .onAppear { if !reveal(proxy) { showCurrent(proxy, appearing: true) } }
            .onChange(of: revealStorage) { _, _ in reveal(proxy) }
            .onChange(of: current) { _, _ in showCurrent(proxy, appearing: false) }
        }
        .frame(width: layout == .rail ? WideLayout.railWidth : WideLayout.menuWidth)
    }

    /// After the rail's gauge: the whole menu at its storage. False when
    /// that wasn't asked for.
    @discardableResult
    private func reveal(_ proxy: ScrollViewProxy) -> Bool {
        guard layout == .full, revealStorage else { return false }
        revealStorage = false
        DispatchQueue.main.async {
            withAnimation(.easeOut(duration: 0.2)) { proxy.scrollTo("storage", anchor: .bottom) }
        }
        return true
    }

    /// The open section in view, as the web's drawer opens with it: a menu
    /// that shows starts at the top and scrolls only as far as the section
    /// needs (Alerts and Settings are below the fold of a phone on its
    /// side); one already showing scrolls only when another section opens
    /// out of sight (from the search, a notification or the drawer).
    private func showCurrent(_ proxy: ScrollViewProxy, appearing: Bool) {
        guard let current else { return }
        DispatchQueue.main.async {
            if appearing {
                // At the top: a row in sight stays put (the scroll can't
                // go above the top), one below comes up to the foot.
                proxy.scrollTo(current, anchor: .bottom)
                return
            }
            guard let f = rows.frames[current] else { return }
            let below = f.maxY > viewport + 0.5
            let above = f.minY < -0.5
            guard below || above else { return }
            withAnimation(.easeOut(duration: 0.2)) {
                proxy.scrollTo(current, anchor: below ? .bottom : .top)
            }
        }
    }

    /// A row's place in the visible part of the menu.
    private func tracked(_ s: AppSection, _ row: some View) -> some View {
        row
            .id(s)
            .onGeometryChange(for: CGRect.self) { $0.frame(in: .named(Self.space)) } action: { rows.frames[s] = $0 }
    }

    // MARK: The whole menu

    private var full: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(groups, id: \.self) { g in
                VStack(alignment: .leading, spacing: 2) {
                    if let h = g.heading {
                        Text(h.uppercased())
                            .font(.system(size: 12, weight: .semibold))
                            .tracking(0.5)
                            .foregroundStyle(.secondary)
                            .padding(.leading, 16)
                            .padding(.top, 16)
                            .padding(.bottom, 4)
                            .accessibilityAddTraits(.isHeader)
                    }
                    ForEach(g.items, id: \.self) { tracked($0, fullRow($0)) }
                }
            }
            Spacer(minLength: 16)
            storagePanel
                .id("storage")
            legal
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }

    private func fullRow(_ s: AppSection) -> some View {
        let active = s == current
        let count = s == .alerts ? unread : 0
        return Button { onPick(s) } label: {
            HStack(spacing: 16) {
                NavIconView(s.icon, size: 22)
                    .foregroundStyle(active ? MenuStyle.gold : Color.secondary)
                Text(s.label)
                    .font(.system(size: 15, weight: active ? .semibold : .regular))
                    .foregroundStyle(active ? MenuStyle.gold : Color.primary)
                    .lineLimit(1)
                Spacer(minLength: 0)
                if count > 0 { UnreadBadge(count: count, small: false) }
            }
            .padding(.horizontal, 16)
            .frame(maxWidth: .infinity, minHeight: 44)
            .background(active ? MenuStyle.activeFill : Color.clear, in: Capsule())
            .contentShape(Capsule())
        }
        .buttonStyle(PillRowStyle())
        .accessibilityLabel(count > 0 ? "\(s.label), \(count) unread" : s.label)
        .accessibilityAddTraits(active ? .isSelected : [])
    }

    private var storagePanel: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                withAnimation(.easeInOut(duration: 0.2)) { details.toggle() }
            } label: {
                HStack(spacing: 16) {
                    NavIconView(.storage, size: 22).foregroundStyle(.secondary)
                    Text("Storage").font(.system(size: 15)).foregroundStyle(.primary)
                    if storage.attention {
                        Circle().fill(MenuStyle.danger).frame(width: 8, height: 8)
                            .accessibilityLabel("Needs attention")
                    }
                    Spacer(minLength: 0)
                    NavIconView(.chevron, size: 18)
                        .foregroundStyle(.secondary)
                        .rotationEffect(.degrees(details ? 180 : 0))
                }
                .padding(.horizontal, 16)
                .frame(maxWidth: .infinity, minHeight: 44)
                .contentShape(Capsule())
            }
            .buttonStyle(PillRowStyle())
            .accessibilityValue(details ? "Details shown" : "Details hidden")

            // How full, as a bar.
            GeometryReader { g in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color(.tertiarySystemFill))
                    Capsule().fill(storage.color)
                        .frame(width: g.size.width * storage.usedPct / 100)
                }
            }
            .frame(height: 4)
            .padding(.horizontal, 16)
            .padding(.top, 4)
            .padding(.bottom, 8)
            .accessibilityElement()
            .accessibilityLabel("Storage used")
            .accessibilityValue("\(Int(storage.usedPct.rounded())) percent")

            Text(storage.usedText)
                .storageLine()
            // Its line is kept while the status loads, so the block
            // doesn't grow when it comes.
            Text(storage.mirrorText.isEmpty ? " " : storage.mirrorText)
                .storageLine(bad: storage.degraded)

            if details && storage.known {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(storage.details, id: \.self) { r in
                        HStack {
                            Text(r.label)
                            Spacer(minLength: 8)
                            Text(r.value)
                        }
                    }
                    ForEach(storage.errors, id: \.self) { e in
                        Text(e).foregroundStyle(MenuStyle.danger)
                    }
                }
                .font(.system(size: 12))
                .monospacedDigit()
                .foregroundStyle(.secondary)
                .padding(.horizontal, 16)
                .padding(.top, 8)
                .transition(.opacity)
            }
        }
        .padding(.top, 8)
        .overlay(alignment: .top) { MenuStyle.line.frame(height: 1 / UIScreen.main.scale) }
    }

    private var legal: some View {
        HStack(spacing: 2) {
            legalLink("Privacy", "https://off-the.cloud/privacy")
            Text("·").accessibilityHidden(true)
            legalLink("Terms", "https://off-the.cloud/terms")
        }
        .font(.system(size: 12))
        .foregroundStyle(.secondary)
        .padding(.horizontal, 12)
        .padding(.top, 8)
    }

    private func legalLink(_ title: String, _ url: String) -> some View {
        Button(title) { if let u = URL(string: url) { openURL(u) } }
            .buttonStyle(.plain)
            .padding(.horizontal, 4)
            .frame(minHeight: 40)
            .contentShape(Rectangle())
    }

    // MARK: The rail

    private var rail: some View {
        VStack(spacing: 0) {
            ForEach(Array(groups.enumerated()), id: \.element) { i, g in
                if i > 0 {
                    MenuStyle.line
                        .frame(width: 40, height: 1)
                        .padding(.vertical, short ? 4 : 8)
                        .accessibilityHidden(true)
                }
                VStack(spacing: short ? 2 : 4) {
                    ForEach(g.items, id: \.self) { tracked($0, railItem($0)) }
                }
            }
            Spacer(minLength: 8)
            gauge
        }
        .padding(.vertical, short ? 4 : 8)
    }

    private func railItem(_ s: AppSection) -> some View {
        let active = s == current
        let count = s == .alerts ? unread : 0
        return Button { onPick(s) } label: {
            RailItemLabel(icon: s.icon, label: s.label, active: active, count: count, short: short)
        }
        .buttonStyle(RailItemStyle())
        .accessibilityLabel(count > 0 ? "\(s.label), \(count) unread" : s.label)
        .accessibilityAddTraits(active ? .isSelected : [])
    }

    private var gauge: some View {
        Button(action: onStorage) {
            RailGaugeLabel(storage: storage, short: short)
        }
        .buttonStyle(RailItemStyle())
        .accessibilityLabel("Storage\(storage.known ? ", \(storage.gaugeText) used" : "")\(storage.attention ? ", needs attention" : "")")
        .accessibilityHint("Shows the details")
    }
}

private extension Text {
    func storageLine(bad: Bool = false) -> some View {
        self.font(.system(size: 13))
            .monospacedDigit()
            .foregroundStyle(bad ? MenuStyle.danger : Color.secondary)
            .lineLimit(1)
            .padding(.horizontal, 16)
            .frame(minHeight: 20, alignment: .leading)
    }
}

/// The unread count: an ember pill, smaller on the rail's icon.
struct UnreadBadge: View {
    let count: Int
    let small: Bool

    var body: some View {
        Text(count > 99 ? "99+" : "\(count)")
            .font(.system(size: small ? 10 : 11, weight: .bold))
            .monospacedDigit()
            .foregroundStyle(MenuStyle.emberInk)
            .padding(.horizontal, small ? 4 : 6)
            .frame(minWidth: small ? 16 : 20, minHeight: small ? 16 : 20)
            .background(MenuStyle.ember, in: Capsule())
            .accessibilityHidden(true)
    }
}

/// A whole-menu row: a faint layer while pressed.
private struct PillRowStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .background(configuration.isPressed ? MenuStyle.press : Color.clear, in: Capsule())
    }
}

private struct RailPressedKey: EnvironmentKey {
    static let defaultValue = false
}

private extension EnvironmentValues {
    var railPressed: Bool {
        get { self[RailPressedKey.self] }
        set { self[RailPressedKey.self] = newValue }
    }
}

/// A rail item: the press shows on its pill, not the whole column.
private struct RailItemStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label.environment(\.railPressed, configuration.isPressed)
    }
}

/// The rail's item: the icon in a 56 x 32 pill, the label under it. The
/// open section's pill is gold, growing from the middle as it is picked.
private struct RailItemLabel: View {
    let icon: NavIcon
    let label: String
    let active: Bool
    let count: Int
    let short: Bool
    @Environment(\.railPressed) private var pressed

    var body: some View {
        VStack(spacing: short ? 2 : 4) {
            ZStack {
                Capsule().fill(Color.primary.opacity(0.12)).opacity(pressed ? 1 : 0)
                Capsule().fill(MenuStyle.railActiveFill)
                    .scaleEffect(x: active ? 1 : 0.4, y: 1)
                    .opacity(active ? 1 : 0)
                NavIconView(icon, size: 24)
                    .foregroundStyle(active ? MenuStyle.gold : Color.secondary)
            }
            .frame(width: 56, height: 32)
            .overlay(alignment: .topLeading) {
                if count > 0 {
                    UnreadBadge(count: count, small: true)
                        // Ringed in the menu's colour, off the icon.
                        .padding(2)
                        .background(MenuStyle.bar, in: Capsule())
                        .offset(x: 30, y: -2)
                        .fixedSize()
                }
            }
            .animation(.spring(response: 0.25, dampingFraction: 1), value: active)
            Text(label)
                .font(.system(size: 11, weight: active ? .bold : .medium))
                .foregroundStyle(active ? MenuStyle.gold : Color.secondary)
                .lineLimit(1)
                .minimumScaleFactor(0.8)
        }
        .padding(.horizontal, 2)
        .frame(maxWidth: .infinity, minHeight: short ? 52 : 56)
        .contentShape(Rectangle())
    }
}

/// The rail's storage: a ring filling up as the disks do, the percentage
/// under it. A tap opens the whole menu at the details.
private struct RailGaugeLabel: View {
    let storage: StorageSummary
    let short: Bool
    @Environment(\.railPressed) private var pressed

    var body: some View {
        VStack(spacing: short ? 2 : 4) {
            ZStack {
                Capsule().fill(Color.primary.opacity(0.12)).opacity(pressed ? 1 : 0)
                // The web's 36-unit ring (r 15.5, stroke 3) drawn at 32.
                let d: CGFloat = 31 * 32 / 36
                let w: CGFloat = 3 * 32 / 36
                Circle().stroke(MenuStyle.line, lineWidth: w).frame(width: d, height: d)
                if storage.usedPct > 0 {
                    Circle()
                        .trim(from: 0, to: storage.usedPct / 100)
                        .stroke(storage.color, style: StrokeStyle(lineWidth: w, lineCap: .round))
                        .rotationEffect(.degrees(-90))
                        .frame(width: d, height: d)
                }
                NavIconView(.storage, size: 14).foregroundStyle(.secondary)
            }
            .frame(width: 56, height: 32)
            .overlay(alignment: .topLeading) {
                if storage.attention {
                    Circle().fill(MenuStyle.danger)
                        .frame(width: 8, height: 8)
                        .padding(2)
                        .background(MenuStyle.bar, in: Circle())
                        .offset(x: 33, y: -1)
                }
            }
            Text(storage.gaugeText)
                .font(.system(size: 11, weight: .medium))
                .monospacedDigit()
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, minHeight: short ? 52 : 56)
        .contentShape(Rectangle())
    }
}

// MARK: - The top bar

/// The web's top bar: the menu button (centred over the rail's icons), the
/// logo, the search, and the page's own action (a new post, on Social).
struct WideTopBar<Search: View>: View {
    /// The whole menu shows (beside the page, or laid over it).
    let menuShown: Bool
    /// A full-width window: the search starts past the whole menu.
    let fullWidth: Bool
    /// The page's action, as an ember outlined "+ label" - only its "+"
    /// on a window under 700 points, where the search needs the room.
    var action: TopBarAction?
    var actionIconOnly = false
    let onMenu: () -> Void
    @ViewBuilder let search: () -> Search

    var body: some View {
        HStack(spacing: 8) {
            Button(action: onMenu) {
                NavIconView(.menu, size: 24)
                    .frame(width: 44, height: 44)
                    .contentShape(Circle())
            }
            .buttonStyle(CircleButtonStyle())
            .accessibilityLabel(menuShown ? "Hide menu" : "Show menu")

            Image("OTCLogo")
                .resizable()
                .scaledToFit()
                .frame(height: 38)
                .padding(.leading, 4)
                .frame(width: fullWidth ? WideLayout.menuWidth - 60 : nil, alignment: .leading)
                .accessibilityLabel("Off The Cloud")
                .accessibilityAddTraits(.isHeader)

            search()
                .frame(maxWidth: 720)
            Spacer(minLength: 0)
            if let action {
                Button(action: action.run) {
                    HStack(spacing: 6) {
                        Image(systemName: "plus").font(.system(size: 16, weight: .semibold))
                        if !actionIconOnly {
                            Text(action.label).font(.system(size: 15, weight: .semibold))
                        }
                    }
                    .foregroundStyle(MenuStyle.ember)
                    .padding(.horizontal, actionIconOnly ? 12 : 16)
                    .frame(minWidth: 40, minHeight: actionIconOnly ? 40 : 38)
                    .overlay(Capsule().stroke(MenuStyle.ember, lineWidth: 1))
                    .contentShape(Capsule())
                }
                .buttonStyle(ActionButtonStyle())
                .accessibilityLabel(action.label)
            }
        }
        .padding(.leading, 18)
        .padding(.trailing, 16)
        // An iPad window showing its controls (iPadOS 26's windowed apps)
        // has them in this corner: the bar's content moves clear of them.
        .modifier(AvoidsWindowControls())
        .frame(height: WideLayout.topBarHeight)
        .foregroundStyle(.primary)
        .background(MenuStyle.bar.ignoresSafeArea(edges: [.top, .horizontal]))
        .overlay(alignment: .bottom) {
            MenuStyle.line.frame(height: 1 / UIScreen.main.scale).ignoresSafeArea(edges: .horizontal)
        }
    }
}

/// Clear of the window's controls: iPadOS 26 puts them in the top leading
/// corner of a window that shows them (a resized one), over the start of a
/// bar along the top - the menu button, Images' search, Files' path. A
/// full-screen window, or an older system, has none, and nothing moves.
struct AvoidsWindowControls: ViewModifier {
    func body(content: Content) -> some View {
        if #available(iOS 26.0, *) {
            content.containerCornerOffset(.horizontal, sizeToFit: true)
        } else {
            content
        }
    }
}

/// A scrolling screen with no bar above it (Settings): what it scrolls
/// starts below an iPad window's controls instead of under them.
struct ScrollClearsWindowControls: ViewModifier {
    @State private var top: CGFloat = 0

    func body(content: Content) -> some View {
        if #available(iOS 26.0, *) {
            content
                .onGeometryChange(for: CGFloat.self) { $0.containerCornerInsets.topLeading.height } action: { top = $0 }
                .safeAreaPadding(.top, top)
        } else {
            content
        }
    }
}

/// The top bar's page action.
struct TopBarAction {
    let label: String
    let run: () -> Void
}

private struct ActionButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .background(MenuStyle.ember.opacity(configuration.isPressed ? 0.12 : 0), in: Capsule())
    }
}

private struct CircleButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .background(configuration.isPressed ? MenuStyle.press : Color.clear, in: Circle())
    }
}

/// The search in the top bar: the web's field, with the open collection,
/// the search's people and its tags as chips inside it. Its panel is
/// MainView's, under it.
struct TopBarSearchField: View {
    @ObservedObject var vm: PhotoGalleryVM
    @ObservedObject var search: TopSearchModel
    var focused: FocusState<Bool>.Binding
    let actions: TopSearchActions

    @State private var width: CGFloat = 0
    @State private var chipsWidth: CGFloat = 0

    /// Tags or people searched for: what the clear button takes away.
    private var searched: Bool { !vm.chips.isEmpty || !vm.selectedPeople.isEmpty }
    /// Chips in the field - an open collection is one too, as on the web,
    /// so that every section shows that Images is narrowed to it.
    private var hasChips: Bool { searched || vm.activeGroup != nil }

    var body: some View {
        HStack(spacing: 0) {
            NavIconView(.search, size: 20)
                .foregroundStyle(focused.wrappedValue ? Color.primary : Color.secondary)
                .frame(width: 36, height: 36)
            if hasChips {
                // The chips scroll sideways inside the field rather than
                // grow it; typing, the text gets room of its own.
                ScrollView(.horizontal, showsIndicators: false) {
                    chips
                        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { chipsWidth = $0 }
                }
                .frame(width: min(chipsWidth, max(0, width - (focused.wrappedValue || !search.query.isEmpty ? 150 : 90))))
                .padding(.leading, 2)
            }
            TextField(hasChips ? "Search" : "Search photos and files", text: $search.query)
                .focused(focused)
                .submitLabel(.search)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .onSubmit { actions.submit() }
                // A hardware keyboard: the arrows go through the suggestions
                // and Escape closes them, as on the web.
                .onKeyPress(.upArrow) { actions.arrow(-1) }
                .onKeyPress(.downArrow) { actions.arrow(1) }
                .onKeyPress(.escape) { actions.escape() }
                .font(.system(size: 16))
                .padding(.horizontal, 8)
                .frame(maxHeight: .infinity)
                .accessibilityLabel("Search photos and files")
            // Everything typed and searched for goes - not an open
            // collection, which its chip's x closes (the web's clearable).
            if !search.query.isEmpty || searched {
                Button {
                    actions.clear()
                } label: {
                    Image(systemName: "xmark")
                        .font(.system(size: 15, weight: .medium))
                        .foregroundStyle(.secondary)
                        .frame(width: 36, height: 36)
                        .contentShape(Circle())
                }
                .buttonStyle(CircleButtonStyle())
                .accessibilityLabel("Clear search")
            }
        }
        .padding(.leading, 4)
        .padding(.trailing, 3)
        .frame(height: 44)
        .background(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .fill(focused.wrappedValue ? Color(.secondarySystemBackground) : MenuStyle.field)
        )
        .overlay(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .stroke(Color.primary.opacity(focused.wrappedValue ? 0.24 : 0), lineWidth: 1)
        )
        .contentShape(RoundedRectangle(cornerRadius: 12))
        .onTapGesture { focused.wrappedValue = true }
        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { width = $0 }
    }

    private var chips: some View {
        HStack(spacing: 6) {
            // The open collection first, as the web's field has it.
            if let g = vm.activeGroup {
                chip(
                    label: g.name,
                    dim: false,
                    removeLabel: "Close the collection \(g.name)",
                    leading: AnyView(
                        NavIconView(.collections, size: 18)
                            .foregroundStyle(MenuStyle.gold)
                    ),
                    leadingPad: 8
                ) { vm.leaveGroup() }
            }
            ForEach(vm.selectedPeople, id: \.self) { pid in
                let person = vm.allPeople.first { $0.id == pid }
                let name = person.map { $0.name.trimmingCharacters(in: .whitespacesAndNewlines) } ?? ""
                chip(
                    label: person == nil ? "Person" : (name.isEmpty ? "Unnamed" : name),
                    dim: name.isEmpty,
                    removeLabel: name.isEmpty ? "Remove this person" : "Remove \(name)",
                    leading: AnyView(
                        Group {
                            if let person, let img = vm.face(for: person) {
                                Image(uiImage: img).resizable().scaledToFill()
                            } else {
                                Color(.tertiarySystemFill).overlay(NavIconView(.face, size: 16).foregroundStyle(.secondary))
                            }
                        }
                        .frame(width: 24, height: 24)
                        .clipShape(Circle())
                    )
                ) { vm.togglePerson(pid) }
            }
            ForEach(vm.chips, id: \.self) { tag in
                chip(label: tag, dim: false, removeLabel: "Remove \(tag)", leading: nil) { vm.removeChip(tag) }
            }
        }
    }

    private func chip(label: String, dim: Bool, removeLabel: String, leading: AnyView?, leadingPad: CGFloat = 3, remove: @escaping () -> Void) -> some View {
        HStack(spacing: 6) {
            if let leading { leading }
            Text(label)
                .font(.system(size: 14))
                .foregroundStyle(dim ? Color.secondary : Color.primary)
                .lineLimit(1)
                .frame(maxWidth: 160, alignment: .leading)
                .fixedSize(horizontal: true, vertical: false)
            Button(action: remove) {
                Image(systemName: "xmark")
                    .font(.system(size: 11, weight: .semibold))
                    .foregroundStyle(.secondary)
                    .frame(width: 24, height: 24)
                    .contentShape(Circle())
            }
            .buttonStyle(CircleButtonStyle())
            .accessibilityLabel(removeLabel)
        }
        .padding(.leading, leading == nil ? 12 : leadingPad)
        .padding(.trailing, 2)
        .frame(height: 32)
        .background(MenuStyle.activeFill, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 8, style: .continuous).stroke(MenuStyle.gold.opacity(0.3), lineWidth: 1))
    }
}

// MARK: - Collections

/// The Collections page, as the web's CollectionsView: every collection as
/// a card with its cover, its name and how many items, newest first as the
/// device lists them. A card opens Images on that collection, which is
/// where one is renamed, shared or deleted. On a narrow window Images'
/// Collections button lists them in a sheet instead.
struct CollectionsPage: View {
    @ObservedObject var vm: PhotoGalleryVM
    /// A collection was opened (vm.openGroup already called), or Images
    /// asked for: show Images.
    let onOpenPhotos: () -> Void

    @State private var asking = false
    @State private var failed = false
    @State private var slow = false
    @State private var shareSource: Msg_SharedGallerySource?

    private static let cols = [GridItem(.adaptive(minimum: 180), spacing: 16, alignment: .top)]

    var body: some View {
        Group {
            if vm.groupsLoaded && vm.groups.isEmpty {
                stateView(
                    art: AnyView(NavIconView(.collections, size: 72, stroke: 1.2).foregroundStyle(.secondary)),
                    title: "No collections yet",
                    text: Text("Select photos in Images, then choose **Add to collection**."),
                    button: "Go to Images"
                ) {
                    vm.showAll()
                    onOpenPhotos()
                }
            } else if vm.groupsLoaded {
                ScrollView {
                    LazyVGrid(columns: Self.cols, spacing: 24) {
                        ForEach(vm.groups, id: \.id) { g in card(g) }
                    }
                    .padding(24)
                    .padding(.bottom, 40)
                }
                .refreshable { await ask() }
            } else if failed || slow {
                stateView(
                    art: AnyView(Image(systemName: "exclamationmark.circle").font(.system(size: 40, weight: .light)).foregroundStyle(.secondary)),
                    title: failed ? "Couldn't load your collections" : "Still waiting for your device",
                    text: Text(failed ? "Check that your device is online, then try again." : "Your collections are taking longer than usual to load."),
                    button: "Try again"
                ) {
                    Task { await ask() }
                }
            } else {
                ScrollView {
                    LazyVGrid(columns: Self.cols, spacing: 24) {
                        ForEach(0..<8, id: \.self) { i in skeleton(i) }
                    }
                    .padding(24)
                }
                .accessibilityLabel("Loading collections")
            }
        }
        .navigationTitle("Collections")
        .navigationBarTitleDisplayMode(.inline)
        // Only the wide layout has this page, and its menu names it, as
        // the web's does: no title bar.
        .toolbar(.hidden, for: .navigationBar)
        .overlay(alignment: .topLeading) { HiddenPageHeading(title: "Collections") }
        // Asked again on every visit (covers and counts change as photos
        // are added elsewhere); what is here already shows meanwhile.
        .task { await ask() }
        .task(id: vm.groupsLoaded) {
            guard !vm.groupsLoaded else { return }
            try? await Task.sleep(for: .seconds(10))
            if !Task.isCancelled { slow = true }
        }
        .sharedGalleryShareFlow(source: $shareSource)
    }

    private func ask() async {
        guard !asking else { return }
        asking = true
        failed = false
        let ok = await vm.loadGroups()
        asking = false
        failed = !ok && !vm.groupsLoaded
    }

    private static func itemsLabel(_ n: Int32) -> String { "\(Int(n).formatted()) \(n == 1 ? "item" : "items")" }
    private static func name(_ g: Msg_ImageGroup) -> String {
        let n = g.name.trimmingCharacters(in: .whitespacesAndNewlines)
        return n.isEmpty ? "Untitled collection" : n
    }

    private func card(_ g: Msg_ImageGroup) -> some View {
        Button {
            vm.openGroup(g)
            onOpenPhotos()
        } label: {
            VStack(alignment: .leading, spacing: 0) {
                CollectionCover(data: g.coverThumbnail)
                Text(Self.name(g))
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundStyle(.primary)
                    .lineLimit(1)
                    .padding(.top, 8)
                Text(Self.itemsLabel(g.fileCount))
                    .font(.system(size: 13))
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(CardPressStyle())
        .accessibilityLabel("\(Self.name(g)), \(Self.itemsLabel(g.fileCount))")
        // Issue #180: long-press a collection to share it.
        .contextMenu {
            Button {
                shareSource = .group(g.id)
            } label: {
                Label("Share as Gallery", systemImage: "photo.on.rectangle.angled")
            }
        }
    }

    private func skeleton(_ i: Int) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .fill(Color(.tertiarySystemFill))
                .aspectRatio(1, contentMode: .fit)
            Capsule().fill(Color(.tertiarySystemFill))
                .frame(width: [0.64, 0.48, 0.72][i % 3] * 160, height: 12)
            Capsule().fill(Color(.tertiarySystemFill))
                .frame(width: 58, height: 10)
        }
        .phaseAnimator([0.55, 1]) { v, p in v.opacity(p) } animation: { _ in .easeInOut(duration: 0.7) }
        .accessibilityHidden(true)
    }

    private func stateView(art: AnyView, title: String, text: Text, button: String, action: @escaping () -> Void) -> some View {
        ScrollView {
            VStack(spacing: 0) {
                art
                    .frame(width: 128, height: 128)
                    .background(
                        Circle().fill(RadialGradient(
                            colors: [MenuStyle.gold.opacity(0.12), Color.primary.opacity(0.03)],
                            center: UnitPoint(x: 0.5, y: 0.35), startRadius: 0, endRadius: 64
                        ))
                    )
                    .padding(.bottom, 24)
                Text(title)
                    .font(.title3.weight(.semibold))
                    .multilineTextAlignment(.center)
                text
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.top, 8)
                Button(button, action: action)
                    .buttonStyle(.borderedProminent)
                    .padding(.top, 20)
            }
            .frame(maxWidth: 400)
            .padding(.horizontal, 24)
            .padding(.top, 48)
            .frame(maxWidth: .infinity)
        }
    }
}

/// A collection's cover, decoded once, square from the first frame so
/// nothing moves when it comes.
private struct CollectionCover: View {
    let data: Data
    @State private var image: UIImage?

    var body: some View {
        Color.clear
            .aspectRatio(1, contentMode: .fit)
            .overlay {
                if let image {
                    Image(uiImage: image).resizable().scaledToFill()
                } else {
                    LinearGradient(
                        colors: [Color(.tertiarySystemFill), Color(.quaternarySystemFill)],
                        startPoint: .topLeading, endPoint: .bottomTrailing
                    )
                    .overlay(NavIconView(.collections, size: 36).foregroundStyle(.secondary))
                }
            }
            .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
            .task(id: data) {
                guard !data.isEmpty else { image = nil; return }
                image = UIImage(data: data)
            }
    }
}

private struct CardPressStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .scaleEffect(configuration.isPressed ? 0.98 : 1)
            .animation(.easeOut(duration: 0.15), value: configuration.isPressed)
    }
}
