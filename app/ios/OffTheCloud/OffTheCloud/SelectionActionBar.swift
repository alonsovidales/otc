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
}

struct SelectionActionBar: View {
    let count: Int
    /// Swaps that action's icon for a spinner and holds the bar until it
    /// finishes, so a slow link can't be tapped twice.
    var busy: SelectionActionTask?
    let onShare: () -> Void
    let onDownload: () -> Void
    let onDelete: () -> Void
    /// Issue #115: "Group" - create a group from the selection or add it
    /// to one. Only the Images tab has it, so it is optional and absent
    /// from the bar when not given.
    var onGroup: (() -> Void)? = nil

    var body: some View {
        VStack(spacing: 6) {
            // Its own pill rather than bare text: this sits over the grid,
            // and unbacked caption text on top of photographs is
            // unreadable - which is exactly how it looked.
            Text("\(count) selected")
                .font(.caption)
                .foregroundStyle(.secondary)
                .padding(.horizontal, 12)
                .padding(.vertical, 5)
                .modifier(CapsuleBarBackground())

            HStack(spacing: 0) {
                item("Share", symbol: "square.and.arrow.up", busy: busy == .share, action: onShare)
                item("Download", symbol: "arrow.down.circle", busy: busy == .download, action: onDownload)
                if let onGroup {
                    item("Group", symbol: "book", action: onGroup)
                }
                item("Delete", symbol: "trash", tint: .red, action: onDelete)
            }
            .padding(.vertical, 10)
            .padding(.horizontal, 8)
            .modifier(CapsuleBarBackground())
        }
        .padding(.horizontal, 24)
        .disabled(busy != nil)
        .animation(.easeInOut(duration: 0.15), value: busy)
    }

    private func item(
        _ title: String,
        symbol: String,
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
                    } else {
                        Image(systemName: symbol).font(.system(size: 20))
                    }
                }
                .frame(height: 22)
                Text(title).font(.caption2)
            }
            // Equal shares of the bar, and the whole share is tappable -
            // not just the glyph and its label.
            .frame(maxWidth: .infinity)
            .contentShape(Rectangle())
        }
        .tint(tint)
    }
}

/// The tab bar's own look where the OS provides it, and a close-enough
/// capsule everywhere else - the deployment target is older than Liquid
/// Glass, so this can't simply assume it.
private struct CapsuleBarBackground: ViewModifier {
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
