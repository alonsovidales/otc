// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SelectionActionBar.swift
//  OffTheCloud
//
//  What Images and Files both show once something is selected.
//
//  Shared on purpose: the two screens had drifted into different bars with
//  different actions - Images had five text buttons that wrapped onto two
//  lines, Files had three unlabelled icons - sitting directly under a tab
//  bar that looks like neither. One component means the same three actions,
//  in the same order, reading the same way on both screens.
//
//  Shaped to match the tab bar below it: a floating capsule with the same
//  material, and icon-over-label items spaced evenly across it.
//

import SwiftUI

/// Which action is waiting on the device, if any. Both Share and Download
/// have to ask the device for a link first - and for anything not uploaded
/// yet, upload it - so neither is instant and both need to say so.
enum SelectionActionTask {
    case share
    case download
    /// Issue #192: keeping a folder out of Images, or showing it again.
    case outOfImages
}

struct SelectionActionBar: View {
    let count: Int
    /// Swaps that action's icon for a spinner and holds the bar until it
    /// finishes, so a slow link can't be tapped twice.
    var busy: SelectionActionTask?
    let onShare: () -> Void
    let onDownload: () -> Void
    let onDelete: () -> Void
    /// Issue #115: "Add to collection" - make a collection (an image group)
    /// from the selection or add it to one. Only the Images tab has it, so
    /// it is optional and absent from the bar when not given.
    var onGroup: (() -> Void)? = nil
    /// Issue #180: "Share as gallery" - the selection as a gallery: Images'
    /// and Files' when the selection is one folder (issue #200, as on
    /// Android); absent otherwise.
    var onGallery: (() -> Void)? = nil
    /// Files only: upload from this phone into the folder being browsed.
    /// It acts on the folder, not the selection, so the Files bar is shown
    /// all the time and this is the one item that works with nothing
    /// selected - the others grey out until something is.
    var onUpload: (() -> Void)? = nil
    /// Issue #200: Files, one folder picked, on a device that can - keep it
    /// out of Images, or show it there again (`keptOutOfImages`: it is kept
    /// out now). Once the long-press menu's; Android's bar has it too.
    var onOutOfImages: (() -> Void)? = nil
    var keptOutOfImages = false

    /// The labels' sizes where a word doesn't fit at the usual one (six
    /// items, Files with one folder picked: "In „Bilder“ ausblenden",
    /// "Buiten Afbeeldingen houden"), following the reader's text size as
    /// caption2 does. 8 is Android's smallest too (BAR_LABEL_MIN_SP).
    @ScaledMetric(relativeTo: .caption2) private var tightLabel: CGFloat = 9
    @ScaledMetric(relativeTo: .caption2) private var tightestLabel: CGFloat = 8
    /// caption2's own size (11), as the reader's text size makes it: the
    /// space between a label's words is a quarter of the size.
    @ScaledMetric(relativeTo: .caption2) private var captionLabel: CGFloat = 11

    /// How many items the bar has.
    private var itemCount: Int {
        3 + [onUpload != nil, onGallery != nil, onOutOfImages != nil, onGroup != nil].filter { $0 }.count
    }

    /// Six items (Files with one folder picked) take nearly the whole
    /// width, as Android's bar does then (BarTightInset).
    private var crowded: Bool { itemCount >= 6 }

    var body: some View {
        VStack(spacing: 6) {
            // Its own pill rather than bare text: this sits over the grid,
            // and unbacked caption text on top of photographs is
            // unreadable - which is exactly how it looked. Only once
            // there is a count to show.
            if count > 0 {
                Text(S.appShellSelected(count: count))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 5)
                    .modifier(CapsuleBarBackground())
            }

            EqualShares {
                if let onUpload {
                    item(S.commonUpload, symbol: "square.and.arrow.down.on.square", action: onUpload)
                }
                item(S.commonShare, symbol: "square.and.arrow.up", busy: busy == .share, action: onShare)
                    .disabled(count == 0)
                if let onGallery {
                    item(S.appShellShareAsGallery, symbol: "photo.on.rectangle.angled", action: onGallery)
                        .disabled(count == 0)
                }
                if let onOutOfImages {
                    item(keptOutOfImages ? S.appShellShowInImages : S.appShellKeepOutOfImages,
                         symbol: keptOutOfImages ? "eye" : "eye.slash",
                         busy: busy == .outOfImages, action: onOutOfImages)
                        .disabled(count == 0)
                }
                item(S.commonDownload, symbol: "arrow.down.circle", busy: busy == .download, action: onDownload)
                    .disabled(count == 0)
                if let onGroup {
                    // The web's own drawing: a collection with a plus.
                    item(S.appShellAddToCollection, symbol: "", icon: .addToCollection, action: onGroup)
                        .disabled(count == 0)
                }
                item(S.commonDelete, symbol: "trash", tint: .red, action: onDelete)
                    .disabled(count == 0)
            }
            .padding(.vertical, 10)
            .padding(.horizontal, crowded ? 4 : 8)
            .modifier(CapsuleBarBackground())
        }
        .padding(.horizontal, crowded ? 8 : 24)
        .disabled(busy != nil)
        .animation(.easeInOut(duration: 0.15), value: busy)
        .animation(.easeInOut(duration: 0.15), value: count > 0)
    }

    /// A label in `font` (`size` points), its words whole, centred on as
    /// few lines as its share of the bar takes (BarLabelLines).
    private func wholeWords(_ title: String, _ font: Font, size: CGFloat) -> some View {
        BarLabelLines(space: size / 4) {
            ForEach(Array(title.split(separator: " ").enumerated()), id: \.offset) { _, word in
                Text(word).font(font).lineLimit(1).fixedSize()
            }
        }
    }

    private func item(
        _ title: String,
        symbol: String,
        icon: NavIcon? = nil,
        tint: Color = .accentColor,
        busy: Bool = false,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            VStack(spacing: 3) {
                // Sized to the glyph it stands in for, so swapping to the
                // spinner doesn't shuffle the other two items sideways.
                ZStack {
                    if busy {
                        ProgressView()
                    } else if let icon {
                        NavIconView(icon, size: 24)
                    } else {
                        Image(systemName: symbol).font(.system(size: 20))
                    }
                }
                .frame(height: 22)
                // "Share as gallery" and "Add to collection" wrap onto two
                // lines in a full bar. Whole words only: a label with more
                // words takes a third line rather than an ellipsis (Zur
                // Sammlung hinzufügen, Ajouter à une collection on an iPhone
                // SE), and one word wider than its share of the bar makes
                // the label smaller, words still whole (the six items of
                // Files with a folder picked, in German or Dutch). The lines
                // are made here, at spaces only: Text also refuses to break
                // after a German closing quote, and then broke the next
                // word instead ("In „Bilder“ au-sblenden"). A word wider
                // even at the smallest size gets smaller still on one line
                // rather than broken (Herunter-laden, Download-en).
                ViewThatFits(in: .horizontal) {
                    wholeWords(title, .caption2, size: captionLabel)
                    wholeWords(title, .system(size: tightLabel), size: tightLabel)
                    wholeWords(title, .system(size: tightestLabel), size: tightestLabel)
                    Text(title).font(.caption2).lineLimit(1).minimumScaleFactor(0.6)
                }
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(title)
            }
            // Equal shares of the bar, and the whole share is tappable -
            // not just the glyph and its label.
            .frame(maxWidth: .infinity)
            .contentShape(Rectangle())
        }
        .tint(tint)
    }
}

/// A label's words (one Text each), centred on as few lines as fit the
/// width offered (BarLabelLines.arrange); ideally as narrow as its widest
/// word, so ViewThatFits takes the largest size at which no word is wider
/// than the item.
struct BarLabelLines: Layout {
    /// Between two words on a line.
    var space: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let sizes = subviews.map { $0.sizeThatFits(.unspecified) }
        let widest = sizes.map(\.width).max() ?? 0
        let width = proposal.width ?? widest
        let lines = Self.arrange(widths: sizes.map(\.width), space: space, width: width)
        let height = lines.reduce(0) { $0 + ($1.map { sizes[$0].height }.max() ?? 0) }
        return CGSize(width: proposal.width ?? widest, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let sizes = subviews.map { $0.sizeThatFits(.unspecified) }
        var y = bounds.minY
        for line in Self.arrange(widths: sizes.map(\.width), space: space, width: bounds.width) {
            let w = Self.span(line[...], sizes.map(\.width), space: space)
            var x = bounds.midX - w / 2
            for i in line {
                subviews[i].place(at: CGPoint(x: x, y: y), anchor: .topLeading, proposal: .unspecified)
                x += sizes[i].width + space
            }
            y += line.map { sizes[$0].height }.max() ?? 0
        }
    }

    /// How wide the words `line` are side by side.
    private static func span(_ line: ArraySlice<Int>, _ widths: [CGFloat], space: CGFloat) -> CGFloat {
        line.reduce(0) { $0 + widths[$1] } + space * CGFloat(max(0, line.count - 1))
    }

    /// The words (their widths, in order) on lines no wider than `width`:
    /// as few lines as that takes; among the ways to make that many, one
    /// whose last lines are no shorter than the first ones ("Share" over
    /// "as gallery", as Text has it), then the narrowest. A word wider than
    /// `width` has a line of its own.
    static func arrange(widths: [CGFloat], space: CGFloat, width: CGFloat) -> [[Int]] {
        let n = widths.count
        guard n > 1 else { return n == 1 ? [[0]] : [] }
        let all = Array(0..<n)
        let fits = { (line: ArraySlice<Int>) in span(line, widths, space: space) <= width + 0.5 }
        for k in 1...n {
            var best: (lines: [[Int]], rising: Bool, widest: CGFloat)?
            // Every way to cut the words into k lines: k - 1 cuts among n - 1 gaps.
            func cut(from start: Int, left: Int, made: [[Int]]) {
                if left == 1 {
                    let lines = made + [Array(all[start...])]
                    guard lines.allSatisfy({ fits($0[...]) }) else { return }
                    let ws = lines.map { span($0[...], widths, space: space) }
                    let rising = zip(ws, ws.dropFirst()).allSatisfy { $0 <= $1 + 0.5 }
                    let widest = ws.max() ?? 0
                    if best == nil || (rising && !best!.rising) || (rising == best!.rising && widest < best!.widest) {
                        best = (lines, rising, widest)
                    }
                    return
                }
                guard start + left <= n else { return }
                for end in (start + 1)...(n - left + 1) {
                    cut(from: end, left: left - 1, made: made + [Array(all[start..<end])])
                }
            }
            cut(from: 0, left: k, made: [])
            if let best { return best.lines }
        }
        return all.map { [$0] }
    }
}

/// The bar's items side by side in equal shares of its width, which
/// `.frame(maxWidth: .infinity)` in an HStack only approximates: an item
/// whose label measured wider took more, squeezing the next one's label
/// into breaking a word. Each is offered exactly its share, so its label
/// picks the size it fits at (ViewThatFits) for that width; centred
/// vertically, as the HStack had them.
private struct EqualShares: Layout {
    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard !subviews.isEmpty else { return .zero }
        let n = CGFloat(subviews.count)
        let width = proposal.width ?? (subviews.map { $0.sizeThatFits(.unspecified).width }.max() ?? 0) * n
        let share = ProposedViewSize(width: width / n, height: nil)
        let height = subviews.map { $0.sizeThatFits(share).height }.max() ?? 0
        return CGSize(width: width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        guard !subviews.isEmpty else { return }
        let share = bounds.width / CGFloat(subviews.count)
        for (i, view) in subviews.enumerated() {
            view.place(at: CGPoint(x: bounds.minX + share * (CGFloat(i) + 0.5), y: bounds.midY),
                       anchor: .center, proposal: ProposedViewSize(width: share, height: nil))
        }
    }
}

/// The tab bar's own look where the OS provides it, and a close-enough
/// capsule everywhere else - the deployment target is older than Liquid
/// Glass, so this can't simply assume it. People's selection bar
/// (PeopleView.swift) wears it too.
struct CapsuleBarBackground: ViewModifier {
    @ViewBuilder
    func body(content: Content) -> some View {
        if #available(iOS 26.0, *) {
            content.glassEffect(.regular, in: Capsule())
        } else {
            content
                .background(Capsule().fill(.ultraThinMaterial))
                .shadow(color: .black.opacity(0.15), radius: 8, y: 2)
        }
    }
}
