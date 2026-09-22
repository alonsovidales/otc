// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  RaidMenuIcon.swift
//  OffTheCloud
//
//  Issue #69: the menu bar icon says how the device's RAID is doing. Two
//  drives in a rack, each coloured by state - green/green when fine,
//  red/orange with one drive down, red/red when the array is gone.
//
//  Drawn rather than an SF Symbol: "server.rack" can't take one colour per
//  drive, and the whole point is that the two lines differ. The menu bar
//  gives an icon about 18pt, so this is deliberately simple - a rounded
//  rack outline and two thick bars - anything finer smears at that size.
//

import SwiftUI

struct RaidMenuIcon: View {
    let health: RaidHealth

    var body: some View {
        let colors = health.driveColors
        Canvas { ctx, size in
            let inset: CGFloat = 1
            let rack = CGRect(x: inset, y: inset, width: size.width - 2 * inset, height: size.height - 2 * inset)
            ctx.stroke(Path(roundedRect: rack, cornerRadius: 3), with: .color(.primary), lineWidth: 1.5)

            let barH = (rack.height - 3 * 3) / 2   // 3pt gaps: top, middle, bottom
            let barX = rack.minX + 3
            let barW = rack.width - 6
            let top = CGRect(x: barX, y: rack.minY + 3, width: barW, height: barH)
            let bottom = CGRect(x: barX, y: top.maxY + 3, width: barW, height: barH)
            ctx.fill(Path(roundedRect: top, cornerRadius: 1.5), with: .color(colors.top))
            ctx.fill(Path(roundedRect: bottom, cornerRadius: 1.5), with: .color(colors.bottom))
        }
        .frame(width: 18, height: 18)
        .accessibilityLabel(health.summary)
    }
}
