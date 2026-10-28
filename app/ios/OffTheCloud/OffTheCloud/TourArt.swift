// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  TourArt.swift
//  OffTheCloud
//
//  The first-run tour's drawings (Tour.swift shows one in each step's
//  card): a small moving mock of the controls the step teaches - the
//  search typed into, the dates dragged, photos selected and the bar
//  coming up, Files switching views, a post liked, one composed and
//  published, a friend added, a user added - so the tour teaches with
//  nothing in the library yet. SwiftUI shapes and animation, drawn on a
//  320 x 176 stage scaled to the card: every label is the real control's
//  own key, so it reads in the app's language; the icons are the real
//  controls' own; colours come from the system's, light or dark; "photos"
//  are gradient tiles and faces a plain outline, never a real person.
//  Each scene is a few states a timer steps through (TourScene); with
//  Reduce Motion one still state shows instead. Hidden from VoiceOver: the
//  step's text says it all. Android's TourArt.kt draws the same scenes.
//

import SwiftUI

/// A step's drawing.
struct TourArt: View {
    let step: TourStep
    let faces: Bool
    let wide: Bool
    /// A short window: drawn smaller.
    let compact: Bool

    static let size = CGSize(width: 320, height: 176)
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        let p = ArtPalette(dark: colorScheme == .dark)
        Color.clear
            .aspectRatio(Self.size.width / Self.size.height, contentMode: .fit)
            .frame(maxWidth: compact ? 220 : 380)
            .overlay {
                GeometryReader { geo in
                    scene(p)
                        .frame(width: Self.size.width, height: Self.size.height)
                        .scaleEffect(geo.size.width / Self.size.width, anchor: .topLeading)
                }
            }
            .background(p.stage)
            .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).stroke(p.line, lineWidth: 0.5))
            .frame(maxWidth: .infinity)
            .environment(\.artPalette, p)
            .accessibilityHidden(true)
    }

    @ViewBuilder
    private func scene(_ p: ArtPalette) -> some View {
        switch step {
        case .welcome: WelcomeArt(faces: faces, wide: wide)
        case .search: SearchArt(chipAbove: !wide)
        case .dates: DatesArt()
        case .select: SelectArt()
        case .viewer: ViewerArt()
        case .people: PeopleArt()
        case .collections: CollectionsArt()
        case .files: FilesArt()
        case .upload: UploadArt()
        case .social: SocialArt()
        case .post: PostArt(wide: wide)
        case .friends: FriendsArt()
        case .backup: BackupArt()
        case .users: UsersArt()
        case .alerts: AlertsArt(wide: wide)
        }
    }
}

// MARK: - The kit

/// The drawings' colours, from the system's own.
struct ArtPalette {
    let dark: Bool

    var stage: Color { dark ? Color(.systemBackground) : Color(.secondarySystemBackground) }
    var surface: Color { dark ? Color(.secondarySystemBackground) : Color(.systemBackground) }
    var field: Color { Color(.tertiarySystemFill) }
    var line: Color { Color(.separator) }
    var text: Color { .primary }
    var dim: Color { .secondary }
    var bar: Color { Color.secondary.opacity(0.32) }
    var accent: Color { .accentColor }
    var ember: Color { MenuStyle.ember }
    var gold: Color { MenuStyle.gold }

    private static let bases: [(Double, Double, Double)] = [
        (1.0, 0.78, 0.34), (1.0, 0.42, 0.29), (0.33, 0.70, 0.48), (0.56, 0.62, 0.69), (0.89, 0.36, 0.36),
    ]

    private func mix(_ i: Int, _ t: Double) -> Color {
        let (r, g, b) = Self.bases[i]
        let k: Double = dark ? 0.16 : 0.92
        return Color(red: r * (1 - t) + k * t, green: g * (1 - t) + k * t, blue: b * (1 - t) + k * t)
    }

    /// A "photo"'s two colours, by hue (0-5).
    func tile(_ hue: Int) -> (Color, Color) {
        switch ((hue % 6) + 6) % 6 {
        case 0: return (mix(0, 0.2), mix(1, 0.25))
        case 1: return (mix(2, 0.3), mix(3, 0.4))
        case 2: return (mix(1, 0.2), mix(0, 0.5))
        case 3: return (mix(3, 0.2), mix(2, 0.55))
        case 4: return (mix(0, 0.35), mix(2, 0.45))
        default: return (mix(4, 0.3), mix(3, 0.5))
        }
    }
}

private struct ArtPaletteKey: EnvironmentKey {
    static let defaultValue = ArtPalette(dark: false)
}

extension EnvironmentValues {
    var artPalette: ArtPalette {
        get { self[ArtPaletteKey.self] }
        set { self[ArtPaletteKey.self] = newValue }
    }
}

/// Steps through a scene's states, `times` (ms) each, over and over; the
/// still state with Reduce Motion on.
private struct TourScene<Content: View>: View {
    let times: [Int]
    let still: Int
    @ViewBuilder let content: (Int) -> Content
    @State private var at = 0
    @Environment(\.accessibilityReduceMotion) private var reduce

    var body: some View {
        ZStack(alignment: .topLeading) {
            content(reduce ? still : at)
        }
        .frame(width: TourArt.size.width, height: TourArt.size.height, alignment: .topLeading)
        .task(id: reduce) {
            guard !reduce else { return }
            at = 0
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(times[at]))
                if Task.isCancelled { return }
                withAnimation(.easeInOut(duration: 0.35)) { at = (at + 1) % times.count }
            }
        }
    }
}

/// A "photo": a gradient with a sun and a hill.
private struct Tile: View {
    let hue: Int
    var radius: CGFloat = 5
    @Environment(\.artPalette) private var p

    var body: some View {
        let (a, b) = p.tile(hue)
        GeometryReader { g in
            ZStack(alignment: .topLeading) {
                LinearGradient(colors: [a, b], startPoint: .topLeading, endPoint: .bottomTrailing)
                Circle().fill(Color.white.opacity(0.5))
                    .frame(width: g.size.width * 0.22, height: g.size.width * 0.22)
                    .offset(x: g.size.width * 0.66, y: g.size.height * 0.16)
                Ellipse().fill(Color.black.opacity(p.dark ? 0.3 : 0.16))
                    .frame(width: g.size.width * 1.3, height: g.size.height * 0.75)
                    .offset(x: -g.size.width * 0.15, y: g.size.height * 0.7)
            }
        }
        .clipShape(RoundedRectangle(cornerRadius: radius, style: .continuous))
    }
}

/// A face as People draws one: a plain outline in a tinted circle.
private struct Face: View {
    let hue: Int
    @Environment(\.artPalette) private var p

    var body: some View {
        let (a, b) = p.tile(hue)
        GeometryReader { g in
            let s = g.size.width
            ZStack {
                Circle().fill(LinearGradient(colors: [a.opacity(0.7), b.opacity(0.7)], startPoint: .top, endPoint: .bottom))
                Path { path in
                    path.addEllipse(in: CGRect(x: s * 0.36, y: s * 0.22, width: s * 0.28, height: s * 0.28))
                    path.move(to: CGPoint(x: s * 0.2, y: s * 0.82))
                    path.addQuadCurve(to: CGPoint(x: s * 0.8, y: s * 0.82), control: CGPoint(x: s * 0.5, y: s * 0.42))
                }
                .stroke(Color.white.opacity(0.85), style: StrokeStyle(lineWidth: max(1, s * 0.05), lineCap: .round))
            }
        }
        .clipShape(Circle())
    }
}

/// Text not worth reading (a file's name, a date).
private struct Bar: View {
    let width: CGFloat
    var height: CGFloat = 5
    @Environment(\.artPalette) private var p

    var body: some View {
        Capsule().fill(p.bar).frame(width: width, height: height)
    }
}

/// A label, one line, in the drawing's own small sizes.
private struct Words: View {
    let text: String
    var size: CGFloat = 10
    var weight: Font.Weight = .regular
    var color: Color = .primary

    init(_ text: String, size: CGFloat = 10, weight: Font.Weight = .regular, color: Color = .primary) {
        self.text = text
        self.size = size
        self.weight = weight
        self.color = color
    }

    var body: some View {
        Text(text)
            .font(.system(size: size, weight: weight))
            .foregroundStyle(color)
            .lineLimit(1)
            .truncationMode(.tail)
    }
}

/// Rich text (a bold name), in the drawing's sizes.
private struct RichWords: View {
    let text: AttributedString
    var size: CGFloat = 9

    var body: some View {
        Text(text).font(.system(size: size)).lineLimit(1).truncationMode(.tail)
    }
}

private var boldTag: AttributeContainer {
    var b = AttributeContainer()
    b.inlinePresentationIntent = .stronglyEmphasized
    return b
}

/// An SF Symbol, as the real control draws it.
private struct Symbol: View {
    let name: String
    var size: CGFloat = 12
    var color: Color = .primary

    init(_ name: String, size: CGFloat = 12, color: Color = .primary) {
        self.name = name
        self.size = size
        self.color = color
    }

    var body: some View {
        Image(systemName: name).font(.system(size: size)).foregroundStyle(color)
    }
}

/// A finger doing what the step says: pressed while `down`.
private struct Finger: View {
    var down = false
    var hidden = false

    var body: some View {
        ZStack {
            Circle().fill(Color.primary.opacity(down ? 0.32 : 0.2))
            Circle().stroke(Color.primary.opacity(0.55), lineWidth: 1.5)
            if down {
                Circle().stroke(MenuStyle.ember.opacity(0.7), lineWidth: 2).scaleEffect(1.5)
            }
        }
        .frame(width: 20, height: 20)
        .scaleEffect(down ? 0.86 : 1)
        .opacity(hidden ? 0 : 1)
        .allowsHitTesting(false)
    }
}

/// Text typed in, a character at a time, from when `on` turns true.
private struct Typed: View {
    let text: String
    let on: Bool
    var size: CGFloat = 10
    var caret = true
    @State private var count = 0
    @Environment(\.accessibilityReduceMotion) private var reduce

    var body: some View {
        HStack(spacing: 1) {
            Words(String(text.prefix(count)), size: size)
            if caret { Rectangle().fill(Color.accentColor).frame(width: 1.2, height: size + 2) }
        }
        .task(id: on) {
            guard on else { count = 0; return }
            if reduce { count = text.count; return }
            count = 0
            for i in 1...max(1, text.count) {
                try? await Task.sleep(for: .milliseconds(70))
                if Task.isCancelled { return }
                count = i
            }
        }
    }
}

/// A switch, on or off.
private struct Switch: View {
    let on: Bool

    var body: some View {
        ZStack(alignment: on ? .trailing : .leading) {
            Capsule().fill(on ? Color.green : Color.secondary.opacity(0.3))
            Circle().fill(Color.white).padding(1.5).shadow(radius: 0.5)
        }
        .frame(width: 26, height: 16)
    }
}

/// A selection's tick, as a selected photo shows it.
private struct Tick: View {
    let on: Bool

    var body: some View {
        ZStack {
            Circle().fill(on ? Color.accentColor : Color.black.opacity(0.15))
            Circle().stroke(Color.white, lineWidth: 1.2)
            if on { Image(systemName: "checkmark").font(.system(size: 7, weight: .bold)).foregroundStyle(.white) }
        }
        .frame(width: 13, height: 13)
    }
}

/// A floating capsule bar of icon-over-label items (SelectionActionBar).
private struct ArtActionBar: View {
    struct Item {
        let symbol: String
        var nav: NavIcon?
        let label: String
        var tint: Color = .accentColor
        var enabled = true
    }

    let items: [Item]
    var lit: Int = -1
    @Environment(\.artPalette) private var p

    var body: some View {
        HStack(spacing: 0) {
            ForEach(Array(items.enumerated()), id: \.offset) { i, it in
                VStack(spacing: 2) {
                    Group {
                        if let nav = it.nav {
                            NavIconView(nav, size: 13)
                        } else {
                            Image(systemName: it.symbol).font(.system(size: 12))
                        }
                    }
                    .frame(height: 14)
                    Words(it.label, size: 7, color: it.tint)
                        .minimumScaleFactor(0.7)
                }
                .foregroundStyle(it.tint)
                .opacity(it.enabled ? 1 : 0.35)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 4)
                .background(i == lit ? Color.accentColor.opacity(0.18) : Color.clear, in: Capsule())
            }
        }
        .padding(.horizontal, 6)
        .padding(.vertical, 3)
        .background(p.surface, in: Capsule())
        .overlay(Capsule().stroke(p.line, lineWidth: 0.5))
        .shadow(color: .black.opacity(0.12), radius: 4, y: 1)
    }
}

/// "3 selected", with its ×.
private struct SelectedPill: View {
    let count: Int
    @Environment(\.artPalette) private var p

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: "xmark").font(.system(size: 7, weight: .semibold)).foregroundStyle(.secondary)
            Words(S.appShellSelected(count: count), size: 8, color: .secondary)
        }
        .padding(.horizontal, 8).padding(.vertical, 3)
        .background(p.surface, in: Capsule())
        .overlay(Capsule().stroke(p.line, lineWidth: 0.5))
    }
}

/// A toast at the foot.
private struct Toast: View {
    let text: String
    let on: Bool

    var body: some View {
        Words(text, size: 9, color: .white)
            .padding(.horizontal, 10).padding(.vertical, 5)
            .background(Color.black.opacity(0.78), in: Capsule())
            .opacity(on ? 1 : 0)
            .scaleEffect(on ? 1 : 0.9)
    }
}

/// A made-up device name from a made-up first name: what Friends takes
/// (letters, digits and dashes).
private func deviceName(_ name: String) -> String {
    let folded = name.folding(options: [.diacriticInsensitive, .caseInsensitive], locale: nil).lowercased()
    let kept = folded.unicodeScalars.filter { CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyz0123456789-").contains($0) }
    let s = String(String.UnicodeScalarView(kept))
    return s.isEmpty ? "sam" : s // i18n-ignore: a fallback device name
}

/// The month `back` months before this one, as PhotoMonths keys it.
private func monthKey(_ back: Int) -> String {
    let cal = Calendar(identifier: .gregorian)
    let d = cal.date(byAdding: .month, value: -back, to: Date()) ?? Date()
    let c = cal.dateComponents([.year, .month], from: d)
    return String(format: "%04d-%02d", c.year ?? 2026, c.month ?? 1) // i18n-ignore: a month's key
}

extension View {
    /// Placed by its top left corner on the stage.
    fileprivate func at(_ x: CGFloat, _ y: CGFloat) -> some View {
        offset(x: x, y: y)
    }

    /// Placed by its centre on the stage.
    fileprivate func centred(_ x: CGFloat, _ y: CGFloat) -> some View {
        position(x: x, y: y)
    }
}

// MARK: - welcome: where the sections are

/// The bottom bar's tabs (MainView's order and symbols), or the menu's items.
private struct WelcomeArt: View {
    let faces: Bool
    let wide: Bool

    private enum Kind { case alerts, post, rows, tiles, settings, faces, cards, friends }

    private struct Item {
        let label: String
        let symbol: String?
        let nav: NavIcon?
        let kind: Kind
    }

    private var tabs: [Item] {
        [
            Item(label: S.appShellSectionAlerts, symbol: "bell.fill", nav: nil, kind: .alerts),
            Item(label: S.appShellSectionSocial, symbol: "bubble.left.and.bubble.right", nav: nil, kind: .post),
            Item(label: S.appShellSectionFiles, symbol: "folder", nav: nil, kind: .rows),
            Item(label: S.appShellSectionImages, symbol: "photo.on.rectangle", nav: nil, kind: .tiles),
            Item(label: S.appShellSectionSettings, symbol: "gearshape", nav: nil, kind: .settings),
        ]
    }

    private var menu: [Item] {
        var items = [Item(label: S.appShellSectionImages, symbol: nil, nav: .images, kind: .tiles)]
        if faces { items.append(Item(label: S.appShellSectionPeople, symbol: nil, nav: .people, kind: .faces)) }
        items += [
            Item(label: S.appShellSectionCollections, symbol: nil, nav: .collections, kind: .cards),
            Item(label: S.appShellSectionFiles, symbol: nil, nav: .files, kind: .rows),
            Item(label: S.appShellSectionSocial, symbol: nil, nav: .social, kind: .post),
            Item(label: S.appShellSectionFriends, symbol: nil, nav: .friends, kind: .friends),
            Item(label: S.appShellSectionAlerts, symbol: nil, nav: .alerts, kind: .alerts),
            Item(label: S.appShellSectionSettings, symbol: nil, nav: .settings, kind: .settings),
        ]
        return items
    }

    var body: some View {
        let items = wide ? menu : tabs
        TourScene(times: Array(repeating: 1000, count: items.count), still: wide ? 0 : 3) { at in
            if wide {
                wideBody(items, at: at)
            } else {
                narrowBody(items, at: at)
            }
        }
    }

    @ViewBuilder
    private func narrowBody(_ items: [Item], at: Int) -> some View {
        let w: CGFloat = 320 / CGFloat(items.count)
        Sketch(kind: items[at].kind)
            .frame(width: 300, height: 116)
            .at(10, 8)
            .id(at)
            .transition(.opacity)
        Rectangle().fill(Color(.separator)).frame(width: 320, height: 0.5).at(0, 132)
        ForEach(Array(items.enumerated()), id: \.offset) { i, it in
            VStack(spacing: 2) {
                Symbol(it.symbol ?? "", size: 15, color: i == at ? .accentColor : .secondary)
                Words(it.label, size: 8, color: i == at ? .accentColor : .secondary)
            }
            .frame(width: w)
            .centred(w * (CGFloat(i) + 0.5), 154)
        }
        Finger(down: true).centred(w * (CGFloat(at) + 0.5) + 6, 152)
    }

    @ViewBuilder
    private func wideBody(_ items: [Item], at: Int) -> some View {
        let row = 168 / CGFloat(items.count)
        Rectangle().fill(Color(.separator)).frame(width: 0.5, height: 176).at(112, 0)
        ForEach(Array(items.enumerated()), id: \.offset) { i, it in
            HStack(spacing: 6) {
                if let nav = it.nav { NavIconView(nav, size: 11) }
                Words(it.label, size: 9, weight: i == at ? .semibold : .regular, color: i == at ? MenuStyle.gold : .primary)
                Spacer(minLength: 0)
            }
            .foregroundStyle(i == at ? MenuStyle.gold : .secondary)
            .padding(.horizontal, 8)
            .frame(width: 104, height: row - 2)
            .background(i == at ? MenuStyle.activeFill : Color.clear, in: Capsule())
            .at(4, 4 + CGFloat(i) * row)
        }
        Sketch(kind: items[at].kind)
            .frame(width: 192, height: 150)
            .at(122, 13)
            .id(at)
            .transition(.opacity)
        Finger(down: true).centred(40, 4 + (CGFloat(at) + 0.5) * row + 4)
    }

    /// What a section looks like, at a glance.
    private struct Sketch: View {
        let kind: Kind

        var body: some View {
            switch kind {
            case .tiles:
                LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 4), count: 3), spacing: 4) {
                    ForEach(0..<6, id: \.self) { Tile(hue: $0).frame(height: 44) }
                }
            case .faces:
                HStack(spacing: 10) {
                    ForEach(0..<4, id: \.self) { i in
                        VStack(spacing: 4) { Face(hue: i + 1).frame(width: 36, height: 36); Bar(width: 30) }
                    }
                }
            case .cards:
                HStack(spacing: 10) {
                    ForEach([1, 3], id: \.self) { h in
                        VStack(alignment: .leading, spacing: 4) { Tile(hue: h).frame(height: 70); Bar(width: 50); Bar(width: 30, height: 4) }
                    }
                }
            case .rows, .settings, .alerts, .friends:
                VStack(alignment: .leading, spacing: 9) {
                    ForEach(0..<4, id: \.self) { i in
                        HStack(spacing: 8) {
                            switch kind {
                            case .rows: Image(systemName: i < 2 ? "folder.fill" : "doc").font(.system(size: 11)).foregroundStyle(i < 2 ? MenuStyle.ember : .secondary)
                            case .alerts: Circle().fill(MenuStyle.ember).frame(width: 7, height: 7)
                            case .friends: Circle().fill(Color.secondary.opacity(0.4)).frame(width: 14, height: 14)
                            default: EmptyView()
                            }
                            Bar(width: [110, 80, 96, 64][i])
                            Spacer(minLength: 0)
                            if kind == .settings { Switch(on: i % 2 == 0) }
                        }
                    }
                }
            case .post:
                VStack(alignment: .leading, spacing: 5) {
                    HStack(spacing: 6) { Circle().fill(Color.secondary.opacity(0.4)).frame(width: 14, height: 14); Bar(width: 60) }
                    Tile(hue: 2).frame(height: 64)
                    Image(systemName: "heart").font(.system(size: 11))
                }
            }
        }
    }
}

// MARK: - search: typed, suggested, picked as a chip

private struct SearchArt: View {
    /// iOS's narrow Images: chips in a row above the field.
    let chipAbove: Bool
    @Environment(\.artPalette) private var p

    var body: some View {
        // rest, typing, suggestions, on the thing, pressed, the chip
        TourScene(times: [700, 1000, 1100, 700, 250, 2200], still: 3) { at in
            let word = S.appTourArtThing
            let typed = String(word.prefix(max(2, (word.count + 1) / 2)))
            let chip = at == 5
            let panel = at >= 2 && at <= 4
            let fieldY: CGFloat = chip && chipAbove ? 30 : 8
            let kept: Set<Int> = [0, 2, 3]
            if chip && chipAbove { chipView(word).at(10, 6) }
            HStack(spacing: 6) {
                NavIconView(.search, size: 13).foregroundStyle(.secondary)
                if chip && !chipAbove { chipView(word) }
                if at == 0 || (chip) {
                    if !(chip && !chipAbove) || at == 0 {
                        Words(S.appShellSearchField, size: 10, color: .secondary)
                    }
                } else {
                    Typed(text: typed, on: at >= 1)
                }
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 10)
            .frame(width: 300, height: 26)
            .background(p.field, in: RoundedRectangle(cornerRadius: 9, style: .continuous))
            .at(10, fieldY)
            ForEach(0..<6, id: \.self) { i in
                Tile(hue: i)
                    .frame(width: 96, height: 56)
                    .opacity(chip && !kept.contains(i) ? 0.12 : 1)
                    .at(10 + CGFloat(i % 3) * 102, 66 + CGFloat(i / 3) * 56 + (i / 3 == 1 ? 2 : 0) - 8)
            }
            VStack(alignment: .leading, spacing: 5) {
                Words(S.appPhotosSearchThings.uppercased(), size: 7, weight: .semibold, color: .secondary)
                HStack(spacing: 6) {
                    NavIconView(.tag, size: 11)
                    RichWords(text: highlighted(word, typed), size: 10)
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, 6).padding(.vertical, 4)
                .background(at >= 3 ? Color.accentColor.opacity(0.16) : Color.clear, in: RoundedRectangle(cornerRadius: 6))
                Words(S.appPhotosSearchFiles.uppercased(), size: 7, weight: .semibold, color: .secondary)
                HStack(spacing: 6) {
                    Image(systemName: "photo").font(.system(size: 10))
                    RichWords(text: highlighted(word + ".jpg", typed), size: 10) // i18n-ignore: a file's name
                }
                .padding(.horizontal, 6)
                HStack(spacing: 6) {
                    NavIconView(.docSearch, size: 11)
                    RichWords(text: S.appPhotosSearchDocuments(text: typed, b: boldTag), size: 10)
                }
                .padding(.horizontal, 6)
            }
            .padding(10)
            .frame(width: 300, alignment: .leading)
            .background(p.surface, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            .shadow(color: .black.opacity(0.18), radius: 8, y: 3)
            .opacity(panel ? 1 : 0)
            .at(10, 40)
            Finger(down: at == 4, hidden: at < 2)
                .centred(at >= 3 && at <= 4 ? 70 : 250, at >= 3 && at <= 4 ? 70 : 150)
        }
    }

    private func chipView(_ word: String) -> some View {
        HStack(spacing: 4) {
            Words(word, size: 9)
            Image(systemName: "xmark").font(.system(size: 7, weight: .semibold)).foregroundStyle(.secondary)
        }
        .padding(.horizontal, 7).padding(.vertical, 3)
        .background(Color.accentColor.opacity(0.18), in: Capsule())
    }

    private func highlighted(_ text: String, _ typed: String) -> AttributedString {
        var s = AttributedString(text)
        if let r = s.range(of: typed) { s[r].inlinePresentationIntent = .stronglyEmphasized }
        return s
    }
}

// MARK: - dates: the right edge dragged, the grid jumping

private struct DatesArt: View {
    private static let months = (0..<12).map { monthKey($0 * 3) }
    private static let block: CGFloat = 62

    var body: some View {
        // rest, to the edge, pressed, dragged, dragged further, let go, the jump
        TourScene(times: [800, 500, 350, 600, 700, 250, 1900], still: 4) { at in
            let drag: CGFloat = [0.04, 0.04, 0.04, 0.34, 0.7, 0.7, 0.7][at]
            let under = min(Self.months.count - 1, Int((drag * CGFloat(Self.months.count - 1)).rounded()))
            let jumped = at >= 6 ? under : 0
            let y = 8 + drag * 160
            let dragging = at >= 2 && at <= 4
            VStack(alignment: .leading, spacing: 0) {
                ForEach(Array(Self.months.enumerated()), id: \.offset) { k, key in
                    VStack(alignment: .leading, spacing: 4) {
                        Words(PhotoMonths.title(key), size: 10, weight: .semibold)
                        HStack(spacing: 4) {
                            ForEach(0..<4, id: \.self) { i in Tile(hue: k + i).frame(width: 66, height: 38) }
                        }
                    }
                    .frame(height: Self.block, alignment: .top)
                }
            }
            .offset(y: 8 - CGFloat(jumped) * Self.block)
            .at(10, 0)
            .animation(at == 0 ? nil : .easeInOut(duration: 0.45), value: jumped)
            Capsule().fill(Color.secondary.opacity(0.35)).frame(width: 3, height: 160).at(306, 8)
            if dragging {
                ForEach(yearTicks(), id: \.0) { year, frac in
                    Words(year, size: 8, color: .secondary).frame(width: 40, alignment: .trailing).at(258, 2 + frac * 160)
                }
                Words(PhotoMonths.scrubLabel(Self.months[under]), size: 9, weight: .bold)
                    .padding(.horizontal, 6).padding(.vertical, 3)
                    .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 5))
                    .frame(width: 120, alignment: .trailing)
                    .at(170, y - 10)
                Circle().fill(Color.yellow).frame(width: 9, height: 9).centred(307.5, y)
            }
            Finger(down: dragging).centred(at == 0 ? 200 : 300, at == 0 ? 120 : y + 6)
        }
    }

    /// Each year once, where it starts on the strip.
    private func yearTicks() -> [(String, CGFloat)] {
        var seen = Set<String>()
        var out: [(String, CGFloat)] = []
        for (k, key) in Self.months.enumerated() {
            let year = String(key.prefix(4))
            if seen.insert(year).inserted { out.append((year, CGFloat(k) / CGFloat(Self.months.count - 1))) }
        }
        return out
    }
}

// MARK: - select: a long press, a drag across, the bar

private struct SelectArt: View {
    private static let cols: [CGFloat] = [10, 86, 162, 238]

    var body: some View {
        // rest, on the first, held, selected, across two, three, four, then
        // each of the bar's buttons
        TourScene(times: [600, 500, 600, 300, 300, 300, 600, 900, 900, 900, 900, 500], still: 8) { at in
            let picked = at >= 6 ? 4 : at >= 5 ? 3 : at >= 4 ? 2 : at >= 3 ? 1 : 0
            let lit = at >= 7 && at <= 10 ? [0, 2, 3, 4][at - 7] : -1
            ForEach(0..<8, id: \.self) { i in
                let on = i < picked
                ZStack(alignment: .topLeading) {
                    Tile(hue: i)
                        .scaleEffect(on ? 0.9 : 1)
                    if on {
                        RoundedRectangle(cornerRadius: 5).stroke(Color.accentColor, lineWidth: 3)
                        Tick(on: true).padding(4)
                    }
                }
                .frame(width: 72, height: 50)
                .at(Self.cols[i % 4], i < 4 ? 8 : 62)
            }
            if picked > 0 {
                SelectedPill(count: picked).centred(160, 124)
                ArtActionBar(items: [
                    .init(symbol: "square.and.arrow.up", label: S.commonShare),
                    .init(symbol: "photo.on.rectangle.angled", label: S.appShellShareAsGallery),
                    .init(symbol: "arrow.down.circle", label: S.commonDownload),
                    .init(symbol: "", nav: .addToCollection, label: S.appShellAddToCollection),
                    .init(symbol: "trash", label: S.commonDelete, tint: .red),
                ], lit: lit)
                .frame(width: 300)
                .at(10, 136)
                .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            let finger: CGPoint = {
                switch at {
                case 0: return CGPoint(x: 200, y: 150)
                case 1, 2, 3: return CGPoint(x: 46, y: 33)
                case 4: return CGPoint(x: 122, y: 33)
                case 5: return CGPoint(x: 198, y: 33)
                case 6: return CGPoint(x: 274, y: 33)
                case 7...10: return CGPoint(x: 10 + 30 + CGFloat(lit) * 60, y: 152)
                default: return CGPoint(x: 200, y: 110)
                }
            }()
            Finger(down: (at >= 2 && at <= 6) || lit >= 0).centred(finger.x, finger.y)
        }
    }
}

// MARK: - viewer: opened, swiped, Info

private struct ViewerArt: View {
    @Environment(\.artPalette) private var p

    var body: some View {
        // the grid, tapped, open, swiped, the next one, to Info, pressed, Info
        TourScene(times: [700, 300, 600, 600, 500, 500, 250, 2100], still: 7) { at in
            let open = at >= 2
            let second = at >= 4
            ForEach(0..<6, id: \.self) { i in
                Tile(hue: i + 1).frame(width: 96, height: 76).at(10 + CGFloat(i % 3) * 102, 8 + CGFloat(i / 3) * 82)
            }
            ZStack(alignment: .topLeading) {
                Color.black
                HStack(spacing: 20) {
                    Tile(hue: 2).frame(width: 200, height: 112)
                    Tile(hue: 3).frame(width: 200, height: 112)
                }
                .offset(x: 60 - (second ? 220 : 0) - (at == 3 ? 40 : 0))
                .at(0, 30)
                Image(systemName: "info.circle").font(.system(size: 15)).foregroundStyle(.white)
                    .padding(3)
                    .background(at == 6 ? Color.white.opacity(0.3) : Color.clear, in: Circle())
                    .centred(18, 16)
                Image(systemName: "xmark.circle.fill").font(.system(size: 16)).foregroundStyle(.white).centred(302, 16)
                HStack(spacing: 46) {
                    Image(systemName: "square.and.arrow.up")
                    Image(systemName: "arrow.down.circle")
                    Image(systemName: "trash")
                }
                .font(.system(size: 13)).foregroundStyle(.white)
                .centred(160, 160)
                VStack(alignment: .leading, spacing: 6) {
                    Capsule().fill(Color.secondary.opacity(0.4)).frame(width: 30, height: 3).frame(maxWidth: .infinity)
                    Words(S.appPhotosViewerInfoTitle, size: 11, weight: .bold)
                    HStack(alignment: .top, spacing: 10) {
                        VStack(alignment: .leading, spacing: 5) {
                            infoRow(S.appPhotosViewerInfoTaken) {
                                Text(Calendar.current.date(byAdding: .month, value: -2, to: Date()) ?? Date(), format: .dateTime.day().month().year())
                                    .font(.system(size: 8)).lineLimit(1)
                            }
                            infoRow(S.appPhotosViewerInfoCamera) { Bar(width: 50, height: 4) }
                            infoRow(S.appPhotosViewerInfoLocation) { Bar(width: 40, height: 4) }
                        }
                        ZStack {
                            RoundedRectangle(cornerRadius: 6).fill(Color.green.opacity(0.25))
                            Path { pa in pa.move(to: CGPoint(x: 0, y: 30)); pa.addLine(to: CGPoint(x: 90, y: 12)) }
                                .stroke(Color.secondary.opacity(0.5), lineWidth: 2)
                            Image(systemName: "mappin.circle.fill").font(.system(size: 13)).foregroundStyle(.red)
                        }
                        .frame(width: 90, height: 56)
                    }
                }
                .padding(10)
                .frame(width: 320, height: 104, alignment: .topLeading)
                .background(p.surface, in: UnevenRoundedRectangle(topLeadingRadius: 12, topTrailingRadius: 12))
                .offset(y: at >= 7 ? 72 : 180)
            }
            // The strip of photos is wider than the stage: kept to its corner.
            .frame(width: 320, height: 176, alignment: .topLeading)
            .clipped()
            .opacity(open ? 1 : 0)
            .scaleEffect(open ? 1 : 0.4, anchor: UnitPoint(x: 0.5, y: 0.3))
            let finger: CGPoint = {
                switch at {
                case 0, 1: return CGPoint(x: 160, y: 46)
                case 2: return CGPoint(x: 230, y: 90)
                case 3: return CGPoint(x: 110, y: 90)
                case 4: return CGPoint(x: 160, y: 120)
                case 5, 6: return CGPoint(x: 22, y: 20)
                default: return CGPoint(x: 250, y: 60)
                }
            }()
            Finger(down: at == 1 || at == 3 || at == 6, hidden: at == 7).centred(finger.x, finger.y)
        }
    }

    private func infoRow<V: View>(_ label: String, @ViewBuilder value: () -> V) -> some View {
        HStack(spacing: 6) {
            Words(label, size: 8, color: .secondary).frame(width: 64, alignment: .leading)
            value()
        }
    }
}

// MARK: - people: a name given, two faces merged

private struct PeopleArt: View {
    @Environment(\.artPalette) private var p
    private static let xs: [CGFloat] = [44, 120, 196, 272]

    var body: some View {
        // rest, Add a name, typing, named, a long press, another, the bar
        TourScene(times: [700, 500, 1300, 600, 450, 450, 1900], still: 6) { at in
            let picked = at >= 5 ? 2 : at >= 4 ? 1 : 0
            let friend = S.appTourArtFriend
            if picked > 0 {
                HStack(spacing: 6) {
                    Image(systemName: "xmark").font(.system(size: 9, weight: .semibold))
                    Words(S.appShellSelected(count: picked), size: 10, weight: .semibold)
                }
                .centred(160, 12)
            } else {
                Words(S.appShellSectionPeople, size: 11, weight: .bold).centred(160, 12)
            }
            ForEach(0..<4, id: \.self) { i in
                let on = i >= 2 && i - 2 < picked
                VStack(spacing: 3) {
                    ZStack(alignment: .topLeading) {
                        Face(hue: i + 1).frame(width: 50, height: 50)
                        if on {
                            Circle().stroke(Color.accentColor, lineWidth: 2.5).frame(width: 50, height: 50)
                            Tick(on: true)
                        }
                    }
                    label(i, at: at, friend: friend)
                        .frame(width: 72, height: 14)
                    Words(S.appPhotosPeoplePhotoCount(count: [12, 8, 5, 3][i]), size: 7, color: .secondary)
                }
                .frame(width: 74)
                .centred(Self.xs[i], 66)
            }
            if picked > 0 {
                ArtActionBar(items: [
                    .init(symbol: "", nav: .merge, label: S.appPhotosPeopleMerge, enabled: picked >= 2),
                    .init(symbol: "trash", label: S.commonDelete, tint: .red),
                ], lit: at == 6 ? 0 : -1)
                .frame(width: 150)
                .centred(160, 150)
                .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            let finger: CGPoint = {
                switch at {
                case 1, 2: return CGPoint(x: 120, y: 104)
                case 4: return CGPoint(x: 196, y: 50)
                case 5: return CGPoint(x: 272, y: 50)
                case 6: return CGPoint(x: 122, y: 150)
                default: return CGPoint(x: 240, y: 150)
                }
            }()
            Finger(down: at == 1 || at == 4 || at == 5 || at == 6).centred(finger.x, finger.y)
        }
    }

    @ViewBuilder
    private func label(_ i: Int, at: Int, friend: String) -> some View {
        if i == 0 {
            Words(S.appTourArtPerson, size: 9, weight: .semibold)
        } else if i == 1 && at == 2 {
            Typed(text: friend, on: true, size: 9)
                .padding(.horizontal, 4)
                .background(p.field, in: RoundedRectangle(cornerRadius: 4))
        } else if i == 1 && at >= 3 {
            Words(friend, size: 9, weight: .semibold)
        } else {
            Words(S.appPhotosPeopleAddName, size: 9, color: .accentColor).italic()
        }
    }
}

// MARK: - collections: added to a new one, found, opened

private struct CollectionsArt: View {
    @Environment(\.artPalette) private var p

    var body: some View {
        // selected, Add to collection, its choices, the name, the list,
        // the card tapped, the collection open
        TourScene(times: [700, 700, 900, 1300, 1300, 450, 1900], still: 4) { at in
            let name = S.appTourArtCollection
            let page = at >= 6 ? 2 : at >= 4 ? 1 : 0
            Group {
                ForEach(0..<6, id: \.self) { i in
                    ZStack(alignment: .topLeading) {
                        Tile(hue: i + 2).scaleEffect(i < 3 ? 0.9 : 1)
                        if i < 3 { Tick(on: true).padding(4) }
                    }
                    .frame(width: 96, height: 44)
                    .at(10 + CGFloat(i % 3) * 102, 6 + CGFloat(i / 3) * 50)
                }
                SelectedPill(count: 3).centred(160, 116)
                ArtActionBar(items: [
                    .init(symbol: "square.and.arrow.up", label: S.commonShare),
                    .init(symbol: "arrow.down.circle", label: S.commonDownload),
                    .init(symbol: "", nav: .addToCollection, label: S.appShellAddToCollection),
                    .init(symbol: "trash", label: S.commonDelete, tint: .red),
                ], lit: at >= 1 ? 2 : -1)
                .frame(width: 300)
                .at(10, 130)
                // The choices, as an action sheet.
                VStack(spacing: 0) {
                    Words(S.appPhotosCollectionsAddTitle(count: 3), size: 8, color: .secondary).padding(.vertical, 5)
                    Divider()
                    Words(S.appPhotosCollectionsNewItem, size: 10, color: .accentColor).padding(.vertical, 6)
                        .frame(maxWidth: .infinity)
                        .background(at == 2 ? Color.accentColor.opacity(0.12) : Color.clear)
                    Divider()
                    Bar(width: 60).padding(.vertical, 8)
                }
                .frame(width: 220)
                .background(p.surface, in: RoundedRectangle(cornerRadius: 10))
                .shadow(color: .black.opacity(0.2), radius: 8, y: 2)
                .opacity(at == 2 ? 1 : 0)
                .centred(160, at == 2 ? 120 : 150)
                // Its name, as the alert asks it.
                VStack(spacing: 6) {
                    Words(S.appPhotosCollectionsNewTitle, size: 10, weight: .semibold)
                    HStack { Typed(text: name, on: at == 3, size: 9); Spacer(minLength: 0) }
                        .padding(.horizontal, 6).frame(width: 150, height: 18)
                        .background(p.field, in: RoundedRectangle(cornerRadius: 4))
                    Words(S.commonCreate, size: 10, weight: .semibold, color: .accentColor)
                }
                .padding(10)
                .background(p.surface, in: RoundedRectangle(cornerRadius: 12))
                .shadow(color: .black.opacity(0.2), radius: 8, y: 2)
                .opacity(at == 3 ? 1 : 0)
                .centred(160, 80)
            }
            .opacity(page == 0 ? 1 : 0)
            // The collections.
            VStack(alignment: .leading, spacing: 8) {
                Words(S.appShellSectionCollections, size: 11, weight: .bold)
                HStack(spacing: 10) {
                    card(hue: 2, name: name, lit: at == 5)
                    card(hue: 5, name: nil, lit: false)
                    card(hue: 1, name: nil, lit: false)
                }
            }
            .at(10, 8)
            .opacity(page == 1 ? 1 : 0)
            // One open.
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 8) {
                    VStack(alignment: .leading, spacing: 1) {
                        Words(name, size: 11, weight: .bold)
                        Words(S.appPhotosCollectionsPictures(count: 3), size: 8, color: .secondary)
                    }
                    Spacer(minLength: 0)
                    Image(systemName: "square.and.arrow.up").font(.system(size: 11)).foregroundStyle(Color.accentColor)
                    Image(systemName: "trash").font(.system(size: 11)).foregroundStyle(.red)
                    Image(systemName: "xmark").font(.system(size: 9, weight: .semibold)).foregroundStyle(.secondary)
                }
                .padding(.horizontal, 10).padding(.vertical, 5)
                .frame(width: 300)
                .background(p.field, in: Capsule())
                HStack(spacing: 6) {
                    ForEach(0..<3, id: \.self) { Tile(hue: $0 + 2).frame(width: 96, height: 70) }
                }
            }
            .at(10, 8)
            .opacity(page == 2 ? 1 : 0)
            let finger: CGPoint = {
                switch at {
                case 1: return CGPoint(x: 198, y: 150)
                case 2: return CGPoint(x: 160, y: 122)
                case 3: return CGPoint(x: 230, y: 120)
                case 5: return CGPoint(x: 52, y: 70)
                default: return CGPoint(x: 260, y: 160)
                }
            }()
            Finger(down: at == 1 || at == 2 || at == 5).centred(finger.x, finger.y)
        }
    }

    private func card(hue: Int, name: String?, lit: Bool) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Tile(hue: hue).frame(width: 92, height: 80)
                .overlay(RoundedRectangle(cornerRadius: 5).stroke(lit ? Color.accentColor : Color.clear, lineWidth: 2))
            if let name {
                Words(name, size: 9, weight: .semibold)
                Words(S.appPhotosCollectionsPictures(count: 3), size: 7, color: .secondary)
            } else {
                Bar(width: 56).padding(.vertical, 2)
                Bar(width: 34, height: 4)
            }
        }
    }
}

// MARK: - files: List, Grid, Storage, and an order

private struct FilesArt: View {
    @Environment(\.artPalette) private var p

    private enum Mode { case list, grid, storage }

    private struct Row {
        let folder: Bool
        let symbol: String
        let w: CGFloat
        let bytes: Int64
        let share: CGFloat
    }

    private static let rows: [Row] = [
        Row(folder: true, symbol: "folder.fill", w: 80, bytes: 2_400_000_000, share: 0.62),
        Row(folder: false, symbol: "photo", w: 96, bytes: 4_300_000, share: 0.04),
        Row(folder: false, symbol: "doc", w: 64, bytes: 128_000, share: 0.01),
        Row(folder: false, symbol: "video", w: 88, bytes: 890_000_000, share: 0.33),
    ]

    var body: some View {
        // list, the menu (Grid), grid, the menu (Storage), storage, the
        // menu (List), list, the menu (Size), by size
        TourScene(times: [800, 900, 1100, 900, 1300, 700, 500, 900, 1900], still: 1) { at in
            let mode: Mode = [.list, .list, .grid, .grid, .storage, .storage, .list, .list, .list][at]
            let menu = at == 1 || at == 3 || at == 5 || at == 7
            let hl: Int = [-1, 1, -1, 2, -1, 0, -1, 4, -1][at]
            let bySize = at >= 8
            HStack(spacing: 8) {
                Words("/", size: 10) // i18n-ignore: the root folder's path
                    .padding(.horizontal, 8)
                    .frame(width: 262, height: 22, alignment: .leading)
                    .overlay(RoundedRectangle(cornerRadius: 5).stroke(p.line, lineWidth: 0.8))
                Image(systemName: mode == .grid ? "square.grid.2x2" : mode == .storage ? "chart.bar.xaxis" : "list.bullet")
                    .font(.system(size: 13)).foregroundStyle(Color.accentColor)
                    .padding(4)
                    .background(menu ? Color.accentColor.opacity(0.18) : Color.clear, in: Circle())
            }
            .at(10, 6)
            Group {
                switch mode {
                case .list:
                    let order = bySize ? [0, 3, 1, 2] : [0, 1, 2, 3]
                    ForEach(0..<4, id: \.self) { i in
                        let r = Self.rows[i]
                        HStack(spacing: 8) {
                            Circle().stroke(Color.secondary.opacity(0.6), lineWidth: 1).frame(width: 12, height: 12)
                            Image(systemName: r.symbol).font(.system(size: 11)).foregroundStyle(r.folder ? MenuStyle.ember : .secondary).frame(width: 14)
                            Bar(width: r.w)
                            Spacer(minLength: 0)
                            if r.folder { Image(systemName: "lock.open").font(.system(size: 10)).foregroundStyle(.secondary) }
                            else { Words(ByteCountFormatter.string(fromByteCount: r.bytes, countStyle: .file), size: 8, color: .secondary) }
                        }
                        .frame(width: 296)
                        .at(12, 40 + CGFloat(order.firstIndex(of: i) ?? i) * 32)
                    }
                case .grid:
                    ForEach(0..<4, id: \.self) { i in
                        let r = Self.rows[i]
                        VStack(spacing: 4) {
                            if i == 1 || i == 3 {
                                Tile(hue: i + 1).frame(width: 66, height: 56)
                            } else {
                                Image(systemName: r.symbol).font(.system(size: 20)).foregroundStyle(r.folder ? MenuStyle.ember : .secondary)
                                    .frame(width: 66, height: 56)
                                    .background(p.field, in: RoundedRectangle(cornerRadius: 6))
                            }
                            Bar(width: 46)
                        }
                        .at(12 + CGFloat(i) * 75, 44)
                    }
                case .storage:
                    Words(S.appFilesUsageTotal(size: ByteCountFormatter.string(fromByteCount: 3_294_428_000, countStyle: .file), count: 4),
                          size: 8, color: .secondary)
                        .at(12, 38)
                    ForEach(Array([0, 3, 1, 2].enumerated()), id: \.offset) { k, i in
                        let r = Self.rows[i]
                        HStack(spacing: 8) {
                            Image(systemName: r.symbol).font(.system(size: 11)).foregroundStyle(r.folder ? MenuStyle.ember : .secondary).frame(width: 14)
                            VStack(alignment: .leading, spacing: 3) {
                                Bar(width: r.w * 0.8, height: 4)
                                ZStack(alignment: .leading) {
                                    Capsule().fill(Color.secondary.opacity(0.18)).frame(width: 150, height: 4)
                                    Capsule().fill(Color.accentColor).frame(width: max(4, 150 * r.share), height: 4)
                                }
                            }
                            Spacer(minLength: 0)
                            Words(S.appFilesUsageRow(size: ByteCountFormatter.string(fromByteCount: r.bytes, countStyle: .file),
                                                     share: "\(Int((r.share * 100).rounded()))%"), size: 8, color: .secondary) // i18n-ignore: a share as a percentage
                        }
                        .frame(width: 296)
                        .at(12, 56 + CGFloat(k) * 29)
                    }
                }
            }
            .opacity(menu ? 0.45 : 1)
            // The View options menu.
            VStack(alignment: .leading, spacing: 0) {
                menuItem(S.appFilesViewList, symbol: "list.bullet", checked: mode == .list, lit: hl == 0)
                menuItem(S.appFilesViewGrid, symbol: "square.grid.2x2", checked: mode == .grid, lit: hl == 1)
                menuItem(S.appFilesViewStorage, symbol: "chart.bar.xaxis", checked: mode == .storage, lit: hl == 2)
                if mode != .storage {
                    Divider().padding(.vertical, 2)
                    Words(S.appFilesSortBy, size: 7, weight: .semibold, color: .secondary).padding(.horizontal, 8).padding(.vertical, 2)
                    menuItem(S.appFilesSortName, symbol: nil, checked: !bySize, lit: false, detail: !bySize ? S.appFilesSortAZ : nil)
                    menuItem(S.appFilesSortSize, symbol: nil, checked: bySize, lit: hl == 4, detail: bySize ? S.appFilesSortLargest : nil)
                }
            }
            .padding(.vertical, 4)
            .frame(width: 150)
            .background(p.surface, in: RoundedRectangle(cornerRadius: 10))
            .shadow(color: .black.opacity(0.22), radius: 8, y: 2)
            .opacity(menu ? 1 : 0)
            .scaleEffect(menu ? 1 : 0.8, anchor: .topTrailing)
            .at(158, 30)
            let finger: CGPoint = {
                switch hl {
                case 0: return CGPoint(x: 210, y: 44)
                case 1: return CGPoint(x: 210, y: 62)
                case 2: return CGPoint(x: 210, y: 80)
                case 4: return CGPoint(x: 210, y: 140)
                default: return CGPoint(x: 290, y: 18)
                }
            }()
            Finger(down: menu).centred(finger.x, finger.y)
        }
    }

    private func menuItem(_ label: String, symbol: String?, checked: Bool, lit: Bool, detail: String? = nil) -> some View {
        HStack(spacing: 6) {
            Image(systemName: "checkmark").font(.system(size: 8, weight: .semibold)).opacity(checked ? 1 : 0)
            VStack(alignment: .leading, spacing: 0) {
                Words(label, size: 9)
                if let detail { Words(detail, size: 7, color: .secondary) }
            }
            Spacer(minLength: 0)
            if let symbol { Image(systemName: symbol).font(.system(size: 9)) }
        }
        .padding(.horizontal, 8).padding(.vertical, 3)
        .background(lit ? Color.accentColor.opacity(0.16) : Color.clear)
    }
}

// MARK: - upload: added, ticked, shared, kept upload only

private struct UploadArt: View {
    @Environment(\.artPalette) private var p

    var body: some View {
        // rest, Upload, uploading, done, a tick, another, Share, its sheet,
        // the lock, locked, the versions
        TourScene(times: [600, 300, 1100, 500, 450, 450, 300, 1300, 500, 900, 1600], still: 5) { at in
            let added = at >= 2
            let ticks = at >= 5 ? 2 : at >= 4 ? 1 : 0
            let locked = at >= 9
            let rows: [(key: String, symbol: String, folder: Bool, w: CGFloat)] =
                (added ? [("new", "doc.text", false, 86)] : []) + [("f", "folder.fill", true, 70), ("p", "photo", false, 92), ("d", "doc", false, 60)]
            ForEach(Array(rows.enumerated()), id: \.element.key) { i, r in
                let checked = (i == 1 && ticks >= 1) || (i == 2 && ticks >= 2)
                HStack(spacing: 8) {
                    ZStack {
                        Circle().stroke(Color.secondary.opacity(0.6), lineWidth: 1)
                        if checked { Circle().fill(Color.accentColor); Image(systemName: "checkmark").font(.system(size: 6, weight: .bold)).foregroundStyle(.white) }
                    }
                    .frame(width: 12, height: 12)
                    Image(systemName: r.symbol).font(.system(size: 11)).foregroundStyle(r.folder ? MenuStyle.ember : .secondary).frame(width: 14)
                    Bar(width: r.w)
                    if r.key == "d" && at >= 10 {
                        HStack(spacing: 2) {
                            Image(systemName: "clock.arrow.circlepath").font(.system(size: 8))
                            Words("2", size: 8) // i18n-ignore: a count of versions
                        }
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 4).padding(.vertical, 1)
                        .background(p.field, in: Capsule())
                    }
                    Spacer(minLength: 0)
                    if r.key == "new" && at == 2 {
                        ZStack(alignment: .leading) {
                            Capsule().fill(Color.secondary.opacity(0.2)).frame(width: 60, height: 4)
                            Capsule().fill(Color.accentColor).frame(width: 40, height: 4)
                        }
                    }
                    if r.folder {
                        Image(systemName: locked ? "lock.fill" : "lock.open").font(.system(size: 11))
                            .foregroundStyle(locked ? Color.accentColor : .secondary)
                            .padding(3)
                            .background(at == 8 ? Color.accentColor.opacity(0.2) : Color.clear, in: Circle())
                    }
                }
                .frame(width: 296)
                .at(12, 8 + CGFloat(i) * 26)
                .transition(.move(edge: .top).combined(with: .opacity))
            }
            if at == 2 {
                Words(S.appFilesUploading(done: 1, total: 1), size: 8, color: .secondary).centred(160, 116)
            }
            if ticks > 0 && at <= 7 { SelectedPill(count: ticks).centred(160, 118) }
            ArtActionBar(items: [
                .init(symbol: "square.and.arrow.down.on.square", label: S.commonUpload),
                .init(symbol: "square.and.arrow.up", label: S.commonShare, enabled: ticks > 0 && at <= 7),
                .init(symbol: "arrow.down.circle", label: S.commonDownload, enabled: ticks > 0 && at <= 7),
                .init(symbol: "trash", label: S.commonDelete, tint: .red, enabled: ticks > 0 && at <= 7),
            ], lit: at == 1 ? 0 : at == 6 ? 1 : -1)
            .frame(width: 280)
            .at(20, 134)
            // The system's share sheet, with the link.
            VStack(spacing: 8) {
                HStack(spacing: 6) {
                    Image(systemName: "link").font(.system(size: 10)).foregroundStyle(Color.accentColor)
                    Bar(width: 120)
                }
                HStack(spacing: 14) {
                    ForEach(0..<5, id: \.self) { i in
                        RoundedRectangle(cornerRadius: 7).fill(p.tile(i).0).frame(width: 26, height: 26)
                    }
                }
            }
            .padding(12)
            .frame(width: 320)
            .background(p.surface, in: UnevenRoundedRectangle(topLeadingRadius: 14, topTrailingRadius: 14))
            .shadow(color: .black.opacity(0.18), radius: 8)
            .offset(y: at == 7 ? 100 : 190)
            if at == 9 {
                Words(S.appFilesUploadOnly, size: 8, color: .white)
                    .padding(.horizontal, 6).padding(.vertical, 2)
                    .background(Color.black.opacity(0.75), in: Capsule())
                    .at(220, 56)
            }
            let finger: CGPoint = {
                switch at {
                case 1: return CGPoint(x: 55, y: 152)
                case 4: return CGPoint(x: 18, y: 40)
                case 5: return CGPoint(x: 18, y: 66)
                case 6: return CGPoint(x: 125, y: 152)
                case 8: return CGPoint(x: 302, y: 40)
                default: return CGPoint(x: 250, y: 100)
                }
            }()
            Finger(down: at == 1 || at == 4 || at == 5 || at == 6 || at == 8, hidden: at == 7).centred(finger.x, finger.y)
        }
    }
}

// MARK: - social: liked, commented

private struct SocialArt: View {
    @Environment(\.artPalette) private var p

    var body: some View {
        // rest, the heart, liked, to the field, typing, Post, the comment
        TourScene(times: [800, 300, 700, 500, 1200, 300, 2000], still: 6) { at in
            let liked = at >= 1
            let comment = S.appTourArtComment
            HStack(spacing: 6) {
                Circle().fill(p.tile(4).0).frame(width: 18, height: 18)
                VStack(alignment: .leading, spacing: 1) {
                    Words(S.appTourArtFriend, size: 10, weight: .semibold)
                    Bar(width: 50, height: 4)
                }
            }
            .at(10, 4)
            HStack(spacing: 4) {
                Tile(hue: 3, radius: 0).frame(width: 300, height: 66)
            }
            .at(10, 28)
            Image(systemName: liked ? "heart.fill" : "heart")
                .font(.system(size: 14)).foregroundStyle(liked ? Color.red : Color.primary)
                .scaleEffect(at == 1 ? 1.3 : 1)
                .at(12, 98)
            Words(S.appSocialPostLikes(count: liked ? 4 : 3), size: 9, weight: .bold).at(12, 116)
            if at >= 6 {
                RichWords(text: S.appSocialPostComment(name: S.appTourArtPerson, comment: comment, b: boldTag), size: 9)
                    .at(12, 130)
                    .transition(.opacity)
            }
            HStack(spacing: 8) {
                HStack {
                    if at >= 4 && at <= 5 {
                        Typed(text: comment, on: true, size: 9)
                    } else {
                        Words(S.appSocialPostCommentPlaceholder, size: 9, color: .secondary)
                    }
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, 6)
                .frame(width: 250, height: 20)
                .overlay(RoundedRectangle(cornerRadius: 5).stroke(p.line, lineWidth: 0.8))
                Words(S.appSocialPostSendComment, size: 9, color: at >= 4 && at <= 5 ? .accentColor : .secondary)
            }
            .at(10, 148)
            let finger: CGPoint = {
                switch at {
                case 1, 2: return CGPoint(x: 19, y: 105)
                case 3, 4: return CGPoint(x: 120, y: 158)
                case 5: return CGPoint(x: 282, y: 158)
                default: return CGPoint(x: 220, y: 110)
                }
            }()
            Finger(down: at == 1 || at == 5).centred(finger.x, finger.y)
        }
    }
}

// MARK: - post: composed and published

private struct PostArt: View {
    let wide: Bool
    @Environment(\.artPalette) private var p

    var body: some View {
        // the feed, +, the composer, a photo, another, the caption,
        // Publish, the new post
        TourScene(times: [700, 300, 700, 450, 450, 1300, 400, 2100], still: 5) { at in
            let composing = at >= 2 && at <= 6
            let picks = at >= 4 ? 2 : at >= 3 ? 1 : 0
            let caption = S.appTourArtCaption
            // The bar: Friends and + (narrow), or the top bar's + New post.
            HStack(spacing: 14) {
                RoundedRectangle(cornerRadius: 4).fill(MenuStyle.ember.opacity(0.8)).frame(width: 18, height: 14)
                Bar(width: 46)
                Spacer(minLength: 0)
                if wide {
                    HStack(spacing: 3) {
                        Image(systemName: "plus").font(.system(size: 9, weight: .semibold))
                        Words(S.appShellNewPost, size: 9, weight: .semibold, color: MenuStyle.ember)
                    }
                    .foregroundStyle(MenuStyle.ember)
                    .padding(.horizontal, 8).padding(.vertical, 3)
                    .overlay(Capsule().stroke(MenuStyle.ember, lineWidth: 1))
                    .background(at == 1 ? MenuStyle.ember.opacity(0.2) : Color.clear, in: Capsule())
                } else {
                    Image(systemName: "person.2").font(.system(size: 12))
                    Image(systemName: "plus.circle").font(.system(size: 15)).foregroundStyle(MenuStyle.ember)
                        .background(at == 1 ? MenuStyle.ember.opacity(0.25) : Color.clear, in: Circle())
                }
            }
            .frame(width: 300)
            .at(10, 6)
            VStack(alignment: .leading, spacing: 8) {
                if at >= 7 {
                    VStack(alignment: .leading, spacing: 3) {
                        HStack(spacing: 5) {
                            Circle().fill(p.tile(0).0).frame(width: 14, height: 14)
                            Words(S.appTourArtPerson, size: 9, weight: .semibold)
                        }
                        Words(caption, size: 9)
                        HStack(spacing: 4) { Tile(hue: 1).frame(width: 90, height: 50); Tile(hue: 3).frame(width: 90, height: 50) }
                    }
                    .transition(.move(edge: .top).combined(with: .opacity))
                }
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 5) { Circle().fill(p.tile(3).0).frame(width: 14, height: 14); Bar(width: 40) }
                    Tile(hue: 4).frame(width: 300, height: 60)
                }
            }
            .at(10, 30)
            // The composer, as a sheet.
            VStack(spacing: 6) {
                HStack {
                    Words(S.commonCancel, size: 9, color: .accentColor)
                    Spacer()
                    Words(S.appSocialComposeTitle, size: 10, weight: .semibold)
                    Spacer()
                    Words(S.commonCancel, size: 9).hidden()
                }
                HStack(spacing: 0) {
                    Words(S.appSocialComposeSourcePhone, size: 8, weight: .semibold)
                        .frame(maxWidth: .infinity).padding(.vertical, 2)
                        .background(p.surface, in: RoundedRectangle(cornerRadius: 5))
                    Words(S.appSocialComposeSourceSynced, size: 8).frame(maxWidth: .infinity)
                }
                .padding(2)
                .background(p.field, in: RoundedRectangle(cornerRadius: 6))
                .frame(width: 180)
                HStack(spacing: 3) {
                    ForEach(0..<5, id: \.self) { i in
                        let order = i == 0 && picks >= 1 ? 1 : i == 2 && picks >= 2 ? 2 : 0
                        ZStack(alignment: .topTrailing) {
                            Tile(hue: i + 1, radius: 2)
                            if order > 0 {
                                Words("\(order)", size: 7, weight: .bold, color: .white) // i18n-ignore: a pick's place in the post
                                    .frame(width: 12, height: 12)
                                    .background(Color.accentColor, in: Circle())
                                    .padding(2)
                            }
                        }
                        .frame(width: 56, height: 48)
                    }
                }
                HStack(spacing: 6) {
                    Image(systemName: "camera").font(.system(size: 10)).foregroundStyle(.secondary)
                    HStack {
                        if at >= 5 {
                            Typed(text: caption, on: at >= 5, size: 8, caret: at == 5)
                        } else {
                            Words(S.appSocialComposeCaptionPlaceholder, size: 8, color: .secondary)
                        }
                        Spacer(minLength: 0)
                    }
                    .padding(.horizontal, 5)
                    .frame(height: 18)
                    .background(p.field, in: RoundedRectangle(cornerRadius: 5))
                    Words(picks > 0 ? S.appSocialComposePublishCount(number: picks) : S.appSocialComposePublish, size: 8, weight: .semibold, color: .white)
                        .padding(.horizontal, 7).padding(.vertical, 3)
                        .background(Color.accentColor.opacity(at >= 5 ? 1 : 0.4), in: Capsule())
                        .scaleEffect(at == 6 ? 0.92 : 1)
                }
            }
            .padding(10)
            .frame(width: 320, height: 160, alignment: .top)
            .background(p.surface, in: UnevenRoundedRectangle(topLeadingRadius: 14, topTrailingRadius: 14))
            .shadow(color: .black.opacity(0.18), radius: 8)
            .offset(y: composing ? 16 : 190)
            let finger: CGPoint = {
                switch at {
                case 1: return wide ? CGPoint(x: 270, y: 14) : CGPoint(x: 302, y: 14)
                case 3: return CGPoint(x: 42, y: 92)
                case 4: return CGPoint(x: 160, y: 92)
                case 5: return CGPoint(x: 120, y: 150)
                case 6: return CGPoint(x: 284, y: 150)
                default: return CGPoint(x: 230, y: 120)
                }
            }()
            Finger(down: at == 1 || at == 3 || at == 4 || at == 6).centred(finger.x, finger.y)
        }
    }
}

// MARK: - friends: the address, a request sent, one accepted

private struct FriendsArt: View {
    @Environment(\.artPalette) private var p

    var body: some View {
        // rest, Copy, copied, the name typed, Send, sent, Accept, accepted
        TourScene(times: [700, 300, 900, 1200, 300, 1100, 400, 1800], still: 3) { at in
            let me = deviceName(S.appTourArtPerson)
            let friend = S.appTourArtFriend
            let suffix = ".\(SecretsStore.bridgeDomain)"
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 8) {
                    Words(S.appSocialFriendsYourAddress(address: me + suffix), size: 9)
                    Spacer(minLength: 0)
                    Image(systemName: "doc.on.doc").font(.system(size: 10)).foregroundStyle(Color.accentColor)
                        .padding(3).background(at == 1 ? Color.accentColor.opacity(0.2) : Color.clear, in: Circle())
                    Image(systemName: "square.and.arrow.up").font(.system(size: 10)).foregroundStyle(Color.accentColor)
                }
                .padding(8)
                .background(p.surface, in: RoundedRectangle(cornerRadius: 8))
                Words(S.appSocialFriendsWaitingAnswer.uppercased(), size: 7, weight: .semibold, color: .secondary)
                HStack(spacing: 6) {
                    Circle().fill(p.tile(2).0).frame(width: 16, height: 16)
                    VStack(alignment: .leading, spacing: 1) {
                        Words(friend, size: 9, weight: .semibold)
                        Words(deviceName(friend) + suffix, size: 7, color: .secondary)
                    }
                    Spacer(minLength: 0)
                    if at >= 7 {
                        Words(S.appSocialFriendsStatusAccepted, size: 8, weight: .semibold, color: .green)
                    } else {
                        Words(S.commonDecline, size: 8, color: .secondary)
                            .padding(.horizontal, 6).padding(.vertical, 3)
                            .background(p.field, in: Capsule())
                        Words(S.commonAccept, size: 8, weight: .semibold, color: .white)
                            .padding(.horizontal, 6).padding(.vertical, 3)
                            .background(Color.accentColor, in: Capsule())
                            .scaleEffect(at == 6 ? 0.9 : 1)
                    }
                }
                .padding(8)
                .background(p.surface, in: RoundedRectangle(cornerRadius: 8))
                Words(S.appSocialFriendsAddHeading.uppercased(), size: 7, weight: .semibold, color: .secondary)
                HStack(spacing: 4) {
                    Spacer(minLength: 0)
                    if at >= 3 && at <= 4 {
                        Typed(text: deviceName(friend), on: true, size: 9)
                    } else {
                        Words(S.appSocialFriendsNamePlaceholder, size: 9, color: .secondary)
                    }
                    Words(suffix, size: 9, color: .secondary)
                    Words(S.commonSend, size: 9, weight: .semibold, color: at >= 3 && at <= 4 ? .accentColor : .secondary)
                        .padding(.leading, 6)
                }
                .padding(8)
                .background(p.surface, in: RoundedRectangle(cornerRadius: 8))
            }
            .frame(width: 300)
            .at(10, 6)
            Toast(text: S.appSocialFriendsAddressCopied, on: at == 2).centred(160, 160)
            Toast(text: S.appSocialFriendsRequestSent, on: at == 5).centred(160, 160)
            let finger: CGPoint = {
                switch at {
                case 1: return CGPoint(x: 268, y: 20)
                case 3: return CGPoint(x: 200, y: 148)
                case 4: return CGPoint(x: 290, y: 148)
                case 6: return CGPoint(x: 288, y: 76)
                default: return CGPoint(x: 120, y: 150)
                }
            }()
            Finger(down: at == 1 || at == 4 || at == 6, hidden: at == 2 || at == 5).centred(finger.x, finger.y)
        }
    }
}

// MARK: - backup: the phone's photos going to the device, Wi-Fi only

private struct BackupArt: View {
    @Environment(\.artPalette) private var p

    var body: some View {
        // rest, one photo sent, another, to Wi-Fi only, on, sent over Wi-Fi
        TourScene(times: [700, 900, 900, 500, 900, 1700], still: 4) { at in
            let wifiOnly = at >= 4
            // The phone, with its photos.
            ZStack {
                RoundedRectangle(cornerRadius: 9).stroke(Color.primary.opacity(0.6), lineWidth: 1.5)
                LazyVGrid(columns: [GridItem(.fixed(16), spacing: 3), GridItem(.fixed(16), spacing: 3)], spacing: 3) {
                    ForEach(0..<6, id: \.self) { Tile(hue: $0, radius: 2).frame(width: 16, height: 16) }
                }
            }
            .frame(width: 50, height: 84)
            .at(24, 8)
            // The device at home.
            VStack(spacing: 3) {
                ZStack {
                    RoundedRectangle(cornerRadius: 8).fill(p.surface)
                    RoundedRectangle(cornerRadius: 8).stroke(Color.primary.opacity(0.5), lineWidth: 1.2)
                    VStack(spacing: 5) {
                        ForEach(0..<3, id: \.self) { _ in Capsule().fill(Color.secondary.opacity(0.4)).frame(width: 34, height: 3) }
                    }
                    Circle().fill(Color.green).frame(width: 5, height: 5).offset(x: 20, y: 16)
                }
                .frame(width: 60, height: 50)
            }
            .at(236, 24)
            Path { pa in pa.move(to: CGPoint(x: 82, y: 50)); pa.addLine(to: CGPoint(x: 228, y: 50)) }
                .stroke(Color.secondary.opacity(0.5), style: StrokeStyle(lineWidth: 1.2, dash: [4, 4]))
            Image(systemName: "wifi").font(.system(size: 13))
                .foregroundStyle(wifiOnly ? Color.accentColor : .secondary)
                .centred(155, 34)
            // A photo on its way.
            Tile(hue: at % 3, radius: 2).frame(width: 16, height: 16)
                .centred(at == 1 || at == 2 || at == 5 ? 228 : 82, 50)
                .opacity(at == 1 || at == 2 || at == 5 ? 1 : 0)
                .id(at)
            VStack(alignment: .leading, spacing: 4) {
                Words(S.appSettingsSyncTitle.uppercased(), size: 7, weight: .semibold, color: .secondary)
                VStack(spacing: 0) {
                    HStack { Words(S.appSettingsSyncWifiOnly, size: 9); Spacer(); Switch(on: wifiOnly) }
                        .padding(.horizontal, 8).frame(height: 26)
                        .background(at == 3 ? Color.accentColor.opacity(0.12) : Color.clear)
                    Divider()
                    HStack { Words(S.appSettingsSyncVideos, size: 9); Spacer(); Switch(on: true) }
                        .padding(.horizontal, 8).frame(height: 26)
                }
                .background(p.surface, in: RoundedRectangle(cornerRadius: 8))
            }
            .frame(width: 300)
            .at(10, 104)
            Finger(down: at == 3).centred(at == 3 || at == 4 ? 296 : 200, at == 3 || at == 4 ? 129 : 90)
        }
    }
}

// MARK: - users: one added

private struct UsersArt: View {
    @Environment(\.artPalette) private var p

    var body: some View {
        // rest, the name typed, Add, the first password, typed, Create, added
        TourScene(times: [700, 1100, 300, 600, 900, 300, 2000], still: 6) { at in
            let me = deviceName(S.appTourArtPerson)
            let friend = deviceName(S.appTourArtFriend)
            let suffix = ".\(SecretsStore.bridgeDomain)"
            VStack(alignment: .leading, spacing: 5) {
                Words(S.appSettingsUsersTitle.uppercased(), size: 7, weight: .semibold, color: .secondary)
                VStack(alignment: .leading, spacing: 6) {
                    userRow(me, port: 8081, domain: me + suffix)
                    if at >= 6 {
                        Divider()
                        userRow(friend, port: 8082, domain: friend + suffix)
                            .transition(.move(edge: .top).combined(with: .opacity))
                    }
                    Divider()
                    HStack(spacing: 6) {
                        HStack {
                            if at >= 1 && at <= 5 {
                                Typed(text: friend, on: true, size: 9, caret: at == 1)
                            } else {
                                Words(S.appSettingsUsersUsername, size: 9, color: .secondary)
                            }
                            Spacer(minLength: 0)
                        }
                        Words(S.appSettingsUsersPort, size: 9, color: .secondary).frame(width: 40, alignment: .leading)
                        Words(S.commonAdd, size: 9, weight: .semibold, color: .accentColor)
                            .padding(.horizontal, 8).padding(.vertical, 3)
                            .background(Color.accentColor.opacity(at == 2 ? 0.3 : 0.14), in: Capsule())
                    }
                    Divider()
                    HStack { Words(S.appSettingsUsersRequestBridge, size: 9); Spacer(); Switch(on: true) }
                }
                .padding(8)
                .background(p.surface, in: RoundedRectangle(cornerRadius: 8))
            }
            .frame(width: 300)
            .at(10, 6)
            // The first password, asked as the user is added.
            VStack(spacing: 6) {
                Words(S.appSettingsUsersPasswordTitle(name: friend), size: 10, weight: .semibold)
                ForEach(0..<2, id: \.self) { i in
                    HStack {
                        Words(at >= 4 ? "••••••••" : (i == 0 ? S.appSettingsUsersPassword : S.appSettingsUsersPasswordConfirm), // i18n-ignore: a password's dots
                              size: 9, color: at >= 4 ? .primary : .secondary)
                        Spacer(minLength: 0)
                    }
                    .padding(.horizontal, 6).frame(width: 170, height: 18)
                    .background(p.field, in: RoundedRectangle(cornerRadius: 4))
                }
                Words(S.appSettingsUsersCreate, size: 9, weight: .semibold, color: .white)
                    .padding(.horizontal, 10).padding(.vertical, 3)
                    .background(Color.accentColor, in: Capsule())
                    .scaleEffect(at == 5 ? 0.9 : 1)
            }
            .padding(12)
            .background(p.surface, in: RoundedRectangle(cornerRadius: 12))
            .shadow(color: .black.opacity(0.25), radius: 10, y: 3)
            .opacity(at >= 3 && at <= 5 ? 1 : 0)
            .scaleEffect(at >= 3 && at <= 5 ? 1 : 0.9)
            .centred(160, 96)
            let finger: CGPoint = {
                switch at {
                case 1: return CGPoint(x: 80, y: 66)
                case 2: return CGPoint(x: 284, y: 66)
                case 5: return CGPoint(x: 160, y: 140)
                default: return CGPoint(x: 230, y: 160)
                }
            }()
            Finger(down: at == 2 || at == 5, hidden: at == 3 || at == 4).centred(finger.x, finger.y)
        }
    }

    private func userRow(_ name: String, port: Int, domain: String) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Words(name, size: 9, weight: .bold)
            Words(S.appSettingsUsersPortDomain(port: String(port), domain: domain), size: 7, color: .secondary)
        }
    }
}

// MARK: - alerts and settings

private struct AlertsArt: View {
    let wide: Bool
    @Environment(\.artPalette) private var p

    var body: some View {
        // rest, Alerts, its list, Settings, Settings' Show the tour
        TourScene(times: [700, 400, 1800, 600, 2000], still: 2) { at in
            let page = at >= 4 ? 2 : at >= 1 ? 1 : 0
            let friend = S.appTourArtFriend
            let left: CGFloat = wide ? 104 : 0
            // The alerts.
            VStack(alignment: .leading, spacing: 6) {
                Words(S.appSocialAlertsTitle, size: 11, weight: .bold)
                alertRow(S.appSocialAlertsLikedPost(name: friend, b: boldTag)) { Tile(hue: 2, radius: 3).frame(width: 22, height: 22) }
                alertRow(S.appSocialAlertsCommented(name: friend, b: boldTag)) { Tile(hue: 4, radius: 3).frame(width: 22, height: 22) }
                alertRow(S.appSocialAlertsFriendRequest(name: friend, b: boldTag)) {
                    Words(S.commonAccept, size: 8, weight: .semibold, color: .white)
                        .padding(.horizontal, 6).padding(.vertical, 3)
                        .background(Color.accentColor, in: Capsule())
                }
            }
            .frame(width: 300 - left)
            .at(10 + left, 6)
            .opacity(page == 1 ? 1 : 0)
            // Settings.
            VStack(alignment: .leading, spacing: 5) {
                Words(S.appSettingsProfileTitle.uppercased(), size: 7, weight: .semibold, color: .secondary)
                HStack(spacing: 6) { Circle().fill(p.tile(0).0).frame(width: 18, height: 18); Bar(width: 70) }
                    .padding(6).frame(maxWidth: .infinity, alignment: .leading).background(p.surface, in: RoundedRectangle(cornerRadius: 7))
                HStack { Words(S.appSettingsLanguage, size: 9); Spacer(); Bar(width: 40) }
                    .padding(6).background(p.surface, in: RoundedRectangle(cornerRadius: 7))
                Words(S.appTourSettingsTitle.uppercased(), size: 7, weight: .semibold, color: .secondary)
                HStack { Words(S.appTourShow, size: 9, color: .accentColor); Spacer() }
                    .padding(6)
                    .background(at >= 4 ? Color.accentColor.opacity(0.16) : p.surface, in: RoundedRectangle(cornerRadius: 7))
            }
            .frame(width: 300 - left)
            .at(10 + left, 4)
            .opacity(page == 2 ? 1 : 0)
            if wide {
                Rectangle().fill(p.line).frame(width: 0.5, height: 176).at(100, 0)
                ForEach(0..<2, id: \.self) { i in
                    let on = (i == 0 && page == 1) || (i == 1 && page == 2)
                    HStack(spacing: 6) {
                        NavIconView(i == 0 ? .alerts : .settings, size: 11)
                        Words(i == 0 ? S.appShellSectionAlerts : S.appShellSectionSettings, size: 9, weight: on ? .semibold : .regular,
                              color: on ? MenuStyle.gold : .primary)
                        Spacer(minLength: 0)
                        if i == 0 && page == 0 { badge }
                    }
                    .foregroundStyle(on ? MenuStyle.gold : .secondary)
                    .padding(.horizontal, 8)
                    .frame(width: 92, height: 24)
                    .background(on ? MenuStyle.activeFill : Color.clear, in: Capsule())
                    .at(4, 50 + CGFloat(i) * 30)
                }
                Finger(down: at == 1 || at == 3).centred(40, at >= 3 ? 92 : 62)
            } else {
                Rectangle().fill(p.line).frame(width: 320, height: 0.5).at(0, 132)
                let tabs: [(String, String)] = [
                    ("bell.fill", S.appShellSectionAlerts), ("bubble.left.and.bubble.right", S.appShellSectionSocial),
                    ("folder", S.appShellSectionFiles), ("photo.on.rectangle", S.appShellSectionImages), ("gearshape", S.appShellSectionSettings),
                ]
                ForEach(0..<5, id: \.self) { i in
                    let on = (i == 0 && page == 1) || (i == 4 && page == 2)
                    VStack(spacing: 2) {
                        ZStack(alignment: .topTrailing) {
                            Symbol(tabs[i].0, size: 15, color: on ? .accentColor : .secondary)
                            if i == 0 && page == 0 { badge.offset(x: 9, y: -5) }
                        }
                        Words(tabs[i].1, size: 8, color: on ? .accentColor : .secondary)
                    }
                    .frame(width: 64)
                    .centred(64 * (CGFloat(i) + 0.5), 154)
                }
                Finger(down: at == 1 || at == 3).centred(at >= 3 ? 294 : 38, 152)
            }
        }
    }

    private var badge: some View {
        Words("3", size: 7, weight: .bold, color: .white) // i18n-ignore: a count of new alerts
            .frame(width: 13, height: 13)
            .background(Color.red, in: Circle())
    }

    private func alertRow<T: View>(_ text: AttributedString, @ViewBuilder trailing: () -> T) -> some View {
        HStack(spacing: 6) {
            Circle().fill(p.tile(2).0).frame(width: 18, height: 18)
            RichWords(text: text, size: 8)
            Spacer(minLength: 0)
            trailing()
        }
        .padding(6)
        .background(p.surface, in: RoundedRectangle(cornerRadius: 7))
    }
}
