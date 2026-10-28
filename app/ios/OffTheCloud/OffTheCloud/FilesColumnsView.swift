// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FilesColumnsView.swift
//  OffTheCloud
//
//  Files' list columns on a wide window, on the screen (the rules are
//  FilesColumns.swift): the headings over the list - each sorts by its
//  column, the one in use with its arrow - with a handle at the trailing
//  edge of each but the last, and the cells of a row. The headings sit
//  over the List rather than in it, so they stay put while it scrolls;
//  both are laid out on the same insets and the same widths, so every
//  column lines up with its heading.
//

import SwiftUI
import UIKit

/// The sizes the wide list is drawn with, and what the columns' defaults
/// are measured from (in the app's language and text size).
enum FilesColumnsLook {
    /// Each side of the headings and the rows.
    static let inset: CGFloat = 16
    /// The selection circle and the row's icon with the gaps after them
    /// (the narrow row's sizes): Name starts after them.
    static let circle: CGFloat = 28
    static let icon: CGFloat = 20
    static let gap: CGFloat = 8
    static var lead: CGFloat { circle + gap + icon + gap }
    /// Each side of a Size, Created or Modified cell.
    static let cellPad: CGFloat = 8
    /// A folder's marks after its name: "kept out of Images" and the lock.
    static let mark: CGFloat = 28

    /// The insets of a wide list's rows (the headings take the same).
    static let rowInsets = EdgeInsets(top: 6, leading: inset, bottom: 6, trailing: inset)

    /// A date as the columns show it: the date and the time to the second,
    /// in numbers (the web's dateTimeFields), in the app's formatting
    /// locale.
    static func date(_ d: Date, locale: Locale = L10n.shared.locale) -> String {
        d.formatted(Date.FormatStyle(date: .numeric, time: .standard, locale: locale))
    }

    /// The date alone: what a narrow date column keeps.
    static func dateOnly(_ d: Date, locale: Locale = L10n.shared.locale) -> String {
        d.formatted(Date.FormatStyle(date: .numeric, time: .omitted, locale: locale))
    }

    /// What a row's cell says for `key`: a size, or a date; "—" where the
    /// device sent none (a folder's size, ".."'s), as on the web.
    static func cell(_ row: FileRow, _ key: FilesSortKey, locale: Locale = L10n.shared.locale) -> String {
        switch key {
        case .name: return row.name
        case .size: return row.isDir ? "—" : formatByteCount(row.size)
        case .created: return row.created.map { date($0, locale: locale) } ?? "—"
        case .modified: return row.modified.map { date($0, locale: locale) } ?? "—"
        }
    }

    /// The fonts the rows are drawn in, at a text size.
    static func font(_ style: UIFont.TextStyle, _ size: DynamicTypeSize) -> UIFont {
        UIFont.preferredFont(forTextStyle: style, compatibleWith: UITraitCollection(preferredContentSizeCategory: UIContentSizeCategory(size)))
    }

    private static func width(_ text: String, _ font: UIFont) -> CGFloat {
        (text as NSString).size(withAttributes: [.font: font]).width
    }

    /// What the columns' minimums and defaults come from, measured in the
    /// rows' fonts at `size` (the web measures the same texts on its page).
    static func samples(_ size: DynamicTypeSize, locale: Locale = L10n.shared.locale) -> FilesColumnSamples {
        let body = font(.body, size)
        // The cells' digits are all as wide (FilesColumnCells' monospacedDigit).
        let cells = UIFont.monospacedDigitSystemFont(ofSize: font(.subheadline, size).pointSize, weight: .regular)
        let caption = font(.caption1, size)
        // A date late in the day, late in the year: the widest.
        let d = DateComponents(calendar: Calendar(identifier: .gregorian), year: 2026, month: 12, day: 28, hour: 22, minute: 58, second: 58).date ?? Date()
        // After a name, at most: a file's versions count (its clock, the
        // count and the capsule's padding) and a lock, or a folder's two
        // marks - each after a gap.
        let versions = (caption.pointSize * 1.3 + 4 + width("99", caption) + 16).rounded(.up)
        let trail = max(gap + mark + gap + mark, gap + versions + gap + mark)
        return FilesColumnSamples(
            lead: lead,
            trail: trail,
            pad: [.name: gap, .size: 2 * cellPad, .created: 2 * cellPad, .modified: 2 * cellPad],
            name: width("0000000000…", body),
            size: width(formatByteCount(1023 * 1024 * 1024), cells),
            sizeShort: width(formatByteCount(1536), cells),
            date: width(self.date(d, locale: locale), cells),
            dateShort: width(dateOnly(d, locale: locale), cells)
        )
    }
}

/// The widths given on this phone, a drag under way, and the metrics for
/// the text size and language in use. One per Files screen.
@MainActor
final class FilesColumnsModel: ObservableObject {
    @Published private(set) var given: FilesColumnWidths
    /// The layout while a handle is dragged, from where the drag began.
    @Published private(set) var live: FilesColumnLayout?
    /// The column being dragged.
    @Published private(set) var dragging: FilesSortKey?
    private var start: FilesColumnLayout?
    private let defaults: UserDefaults
    private var measured: (key: String, metrics: FilesColumnMetrics)?

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        given = FilesColumns.load(defaults)
    }

    /// The minimums and defaults at this text size, in this language
    /// (measured again only when either changes).
    func metrics(_ size: DynamicTypeSize, locale: Locale = L10n.shared.locale) -> FilesColumnMetrics {
        let key = "\(size)|\(locale.identifier)"
        if let m = measured, m.key == key { return m.metrics }
        let m = FilesColumns.metrics(FilesColumnsLook.samples(size, locale: locale))
        measured = (key, m)
        return m
    }

    /// The columns for a list `room` wide: the drag's while one is under
    /// way, else fitted to the widths given.
    func layout(room: CGFloat, _ m: FilesColumnMetrics) -> FilesColumnLayout {
        live ?? FilesColumns.fit(room: room, given: given, m)
    }

    /// A handle dragged `dx` points from where the drag began (`from`: the
    /// layout then).
    func drag(_ col: FilesSortKey, by dx: CGFloat, from now: FilesColumnLayout, _ m: FilesColumnMetrics) {
        if start == nil || dragging != col {
            start = now
            dragging = col
        }
        guard let start else { return }
        let next = FilesColumns.resize(start, m, column: col, by: dx)
        if next != live { live = next }
    }

    /// The finger lifted: the widths the drag changed are kept.
    func endDrag() {
        if let start, let live { keep(FilesColumns.widthsAfter(given, before: start, after: live)) }
        start = nil
        live = nil
        dragging = nil
    }

    /// VoiceOver's Wider or Narrower: `col`'s edge moved by `dx` at once.
    func nudge(_ col: FilesSortKey, by dx: CGFloat, in l: FilesColumnLayout, _ m: FilesColumnMetrics) {
        let next = FilesColumns.resize(l, m, column: col, by: dx)
        guard next != l else { return }
        keep(FilesColumns.widthsAfter(given, before: l, after: next))
    }

    /// A double tap on a handle (or VoiceOver's Reset width): that column
    /// back to its default - Name's, every column.
    func reset(_ col: FilesSortKey) {
        start = nil
        live = nil
        dragging = nil
        keep(FilesColumns.reset(given, column: col))
    }

    private func keep(_ next: FilesColumnWidths) {
        given = next
        FilesColumns.save(next, defaults)
    }
}

/// The headings over a wide list: Name, Size, Created and Modified as far
/// as they show. A tap sorts by that column (the same again: the other
/// way); the handle at a heading's trailing edge resizes it.
struct FilesColumnsHeading: View {
    @ObservedObject var model: FilesColumnsModel
    let layout: FilesColumnLayout
    let metrics: FilesColumnMetrics
    let sort: FilesSortOrder
    let onSort: (FilesSortKey) -> Void

    var body: some View {
        HStack(spacing: 0) {
            Color.clear.frame(width: metrics.lead, height: 1)
            // Each over the next: a handle reaches over the next heading's
            // edge, and takes the touches there.
            heading(.name)
                .frame(maxWidth: .infinity, alignment: .leading)
                .zIndex(4)
            ForEach(Array(layout.fixed.enumerated()), id: \.element) { i, k in
                heading(k)
                    .frame(width: layout[k], alignment: .leading)
                    .zIndex(Double(3 - i))
            }
        }
        .padding(.horizontal, FilesColumnsLook.inset)
        .frame(minHeight: 36)
        .overlay(alignment: .bottom) { Divider() }
    }

    static func label(_ k: FilesSortKey) -> String {
        switch k {
        case .name: return S.appFilesColumnName
        case .size: return S.appFilesColumnSize
        case .created: return S.appFilesColumnCreated
        case .modified: return S.appFilesColumnModified
        }
    }

    private func heading(_ k: FilesSortKey) -> some View {
        let on = sort.key == k
        return Button {
            onSort(k)
        } label: {
            HStack(spacing: 3) {
                Text(Self.label(k))
                    .lineLimit(1)
                if on {
                    Image(systemName: sort.ascending ? "chevron.up" : "chevron.down")
                        .font(.caption2.weight(.bold))
                        .accessibilityHidden(true)
                }
            }
            .font(.subheadline.weight(on ? .semibold : .regular))
            .foregroundStyle(on ? Color.primary : Color.secondary)
            .padding(.horizontal, k == .name ? 0 : FilesColumnsLook.cellPad)
            .frame(maxWidth: .infinity, minHeight: 36, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityValue(on ? FilesSortText.direction(sort) : "")
        .accessibilityAddTraits(on ? [.isSelected] : [])
        .overlay(alignment: .trailing) {
            if k != layout.last {
                handle(k)
            }
        }
    }

    /// The edge's handle: a line, the accent's while dragged; a finger gets
    /// the gap either side of it.
    private func handle(_ k: FilesSortKey) -> some View {
        let active = model.dragging == k
        let values = FilesColumns.edgeValues(layout, metrics, column: k)
        return Rectangle()
            .fill(active ? Color.accentColor : Color(.separator))
            .frame(width: active ? 2 : 1)
            .padding(.vertical, active ? 4 : 10)
            .frame(width: Self.grip)
            .frame(maxHeight: .infinity)
            .contentShape(Rectangle())
            .offset(x: Self.grip / 2)
            .gesture(
                DragGesture(minimumDistance: 2, coordinateSpace: .global)
                    .onChanged { v in model.drag(k, by: v.translation.width, from: layout, metrics) }
                    .onEnded { _ in model.endDrag() }
            )
            .simultaneousGesture(TapGesture(count: 2).onEnded { model.reset(k) })
            .accessibilityElement()
            .accessibilityLabel(S.appFilesColumnResize(column: Self.label(k)))
            .accessibilityValue(Int(values.now).formatted(.number.locale(L10n.shared.locale)))
            .accessibilityAdjustableAction { direction in
                switch direction {
                case .increment: model.nudge(k, by: FilesColumns.step, in: layout, metrics)
                case .decrement: model.nudge(k, by: -FilesColumns.step, in: layout, metrics)
                @unknown default: break
                }
            }
            .accessibilityAction(named: S.appFilesColumnWider) { model.nudge(k, by: FilesColumns.step, in: layout, metrics) }
            .accessibilityAction(named: S.appFilesColumnNarrower) { model.nudge(k, by: -FilesColumns.step, in: layout, metrics) }
            .accessibilityAction(named: S.appFilesColumnReset) { model.reset(k) }
    }

    /// The handle's width: the finger's room over the edge.
    static let grip: CGFloat = 24
}

/// A row's Size, Created and Modified, as far as they show.
struct FilesColumnCells: View {
    let row: FileRow
    let layout: FilesColumnLayout

    var body: some View {
        ForEach(layout.fixed, id: \.self) { k in
            Text(FilesColumnsLook.cell(row, k))
                .font(.subheadline)
                .monospacedDigit()
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .padding(.horizontal, FilesColumnsLook.cellPad)
                .frame(width: layout[k], alignment: .leading)
        }
    }
}
