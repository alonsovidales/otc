// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  ProfilePhotoCropView.swift
//  OffTheCloud
//
//  Issue #178: circle-crop step for the Settings profile photo. Mirrors
//  the setup wizard's crop (scripts/setup_wizard.py cropPaint/cropSave):
//  the photo aspect-fills a square, drag pans, pinch or the slider zooms
//  1x-4x, pan is clamped so the square never shows an empty edge, and
//  "Use photo" renders exactly the square to a 320x320 JPEG, stepping the
//  quality down until it fits the profile size budget.
//

import SwiftUI
import UIKit

/// A picked photo waiting to be cropped. Identifiable so it can drive
/// fullScreenCover(item:).
struct ProfilePhotoCropItem: Identifiable {
    let id = UUID()
    let image: UIImage

    /// Decodes the picked data, bakes in its EXIF orientation and downscales
    /// anything with a longest side over 2048 px so the crop view doesn't
    /// hold a full-resolution camera photo in memory.
    init?(data: Data) {
        guard let src = UIImage(data: data) else { return nil }
        let px = CGSize(width: src.size.width * src.scale, height: src.size.height * src.scale)
        guard px.width > 0, px.height > 0 else { return nil }
        let k = min(1, 2048 / max(px.width, px.height))
        let size = CGSize(width: (px.width * k).rounded(), height: (px.height * k).rounded())
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        format.opaque = true
        // draw(in:) applies imageOrientation, so the result is always .up.
        image = UIGraphicsImageRenderer(size: size, format: format).image { _ in
            src.draw(in: CGRect(origin: .zero, size: size))
        }
    }
}

struct ProfilePhotoCropView: View {
    let image: UIImage
    let onCancel: () -> Void
    let onUse: (Data) -> Void

    /// Same numbers as the wizard: 320px output, JPEG quality ladder.
    private static let outputSide: CGFloat = 320
    private static let qualities: [CGFloat] = [0.85, 0.7, 0.55, 0.4]
    private static let maxBytes = 45 * 1024

    @State private var zoom: CGFloat = 1
    /// Pan offset of the image centre from the square's centre, in points
    /// of the on-screen square.
    @State private var offset: CGSize = .zero
    @State private var dragStart: CGSize?
    @State private var zoomStart: CGFloat?
    @State private var side: CGFloat = 1

    var body: some View {
        NavigationStack {
            VStack(spacing: 24) {
                GeometryReader { geo in
                    let s = min(geo.size.width, geo.size.height)
                    cropArea(side: s)
                        .frame(width: s, height: s)
                        .position(x: geo.size.width / 2, y: geo.size.height / 2)
                        .onAppear { side = s }
                        .onChange(of: s) { _, new in
                            side = new
                            clamp()
                        }
                }
                .aspectRatio(1, contentMode: .fit)
                .padding(.horizontal)

                HStack {
                    Image(systemName: "minus.magnifyingglass").foregroundColor(.secondary)
                    Slider(value: Binding(get: { zoom }, set: { zoom = $0; clamp() }), in: 1...4)
                    Image(systemName: "plus.magnifyingglass").foregroundColor(.secondary)
                }
                .padding(.horizontal)

                Text("Drag the photo to centre your face.")
                    .font(.footnote)
                    .foregroundColor(.secondary)
                Spacer()
            }
            .padding(.top)
            .navigationTitle("Crop photo")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", action: onCancel)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Use photo") {
                        if let data = renderJPEG() { onUse(data) } else { onCancel() }
                    }
                }
            }
        }
    }

    private func cropArea(side s: CGFloat) -> some View {
        let scale = displayScale(side: s)
        return ZStack {
            Color(white: 0.12)
            Image(uiImage: image)
                .resizable()
                .frame(width: image.size.width * scale, height: image.size.height * scale)
                .offset(offset)
            // Darken everything outside the circle, like the wizard's
            // evenodd-filled rgba(0,0,0,.55) mask.
            Rectangle()
                .fill(Color.black.opacity(0.55))
                .mask {
                    Rectangle()
                        .overlay(Circle().padding(2).blendMode(.destinationOut))
                        .compositingGroup()
                }
            Circle()
                .stroke(Color.white.opacity(0.6), lineWidth: 1)
                .padding(2)
        }
        .clipped()
        .contentShape(Rectangle())
        .gesture(dragGesture.simultaneously(with: magnifyGesture))
    }

    private var dragGesture: some Gesture {
        DragGesture()
            .onChanged { v in
                let start = dragStart ?? offset
                if dragStart == nil { dragStart = start }
                offset = CGSize(width: start.width + v.translation.width,
                                height: start.height + v.translation.height)
                clamp()
            }
            .onEnded { _ in dragStart = nil }
    }

    private var magnifyGesture: some Gesture {
        MagnifyGesture()
            .onChanged { v in
                let start = zoomStart ?? zoom
                if zoomStart == nil { zoomStart = start }
                zoom = min(4, max(1, start * v.magnification))
                clamp()
            }
            .onEnded { _ in zoomStart = nil }
    }

    /// Aspect-fill scale at zoom 1, times the current zoom (wizard's cropScale).
    private func displayScale(side s: CGFloat) -> CGFloat {
        let w = max(image.size.width, 1), h = max(image.size.height, 1)
        return max(s / w, s / h) * zoom
    }

    /// Keeps the image covering the whole square (wizard's cropClamp).
    private func clamp() {
        let scale = displayScale(side: side)
        let mx = max(0, (image.size.width * scale - side) / 2)
        let my = max(0, (image.size.height * scale - side) / 2)
        offset = CGSize(width: min(mx, max(-mx, offset.width)),
                        height: min(my, max(-my, offset.height)))
    }

    /// Renders exactly what's inside the square at 320x320 (wizard's
    /// cropSave), lowering JPEG quality until it's within maxBytes; the
    /// lowest quality is used regardless if nothing fits.
    private func renderJPEG() -> Data? {
        let out = Self.outputSide
        let k = out / side
        let scale = displayScale(side: side) * k
        let w = image.size.width * scale, h = image.size.height * scale
        let rect = CGRect(x: (out - w) / 2 + offset.width * k,
                          y: (out - h) / 2 + offset.height * k,
                          width: w, height: h)
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        format.opaque = true
        let rendered = UIGraphicsImageRenderer(size: CGSize(width: out, height: out), format: format).image { ctx in
            UIColor(white: 0.12, alpha: 1).setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: out, height: out))
            image.draw(in: rect)
        }
        var data: Data?
        for q in Self.qualities {
            data = rendered.jpegData(compressionQuality: q)
            if let d = data, d.count <= Self.maxBytes { return d }
        }
        return data
    }
}
