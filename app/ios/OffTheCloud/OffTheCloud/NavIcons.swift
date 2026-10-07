// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  NavIcons.swift
//  OffTheCloud
//
//  The web app's own icons (web/src/components/NavIcons.tsx, and the ones
//  its top bar's search draws), drawn here from the very same SVG data:
//  24-point outlines stroked 1.6 wide with round ends, in the colour of
//  whatever holds them. One set of drawings for the web and the phones, so
//  a collection looks like a collection everywhere.
//
//  NavIconView draws one as a SwiftUI view; NavIcon.image gives a template
//  Image for places that only take an Image (tab items, Labels, UIKit).
//  The stroke scales with the size, so at 22 points it weighs about what
//  an SF Symbol does at the body size. Copy any change to the web's
//  drawings here (and to Android's NavIcons.kt) with the same numbers.
//

import SwiftUI
import UIKit

enum NavIcon: CaseIterable, Hashable {
    // The web's menu (NavIcons.tsx).
    case menu, images, people, collections, files, social, friends, alerts, settings, storage, chevron
    // Images' selection bar: a collection with a plus (PhotoGallery.tsx).
    case addToCollection
    // People's selection bar: two lines joining into one (PeopleView.tsx).
    case merge
    // The top bar's search (TopSearch.tsx). A folder is `files` and a
    // photo `images`, as there.
    case search, docSearch, tag, face, video, doc

    /// The drawing, as the web writes it.
    fileprivate var elements: [NavIconElement] {
        switch self {
        case .menu:
            return [.path("M4 7h16M4 12h16M4 17h16")]
        case .images:
            return [.rect(3.5, 4.5, 17, 15, 2.5), .circle(9, 10, 1.6), .path("m4 17 4.5-4.5 3.5 3.5 2.5-2.5L20 18")]
        case .people:
            // A face in a viewfinder: the faces found in the photos
            // (Friends is the two people).
            return [
                .path("M4 8V6a2 2 0 0 1 2-2h2M16 4h2a2 2 0 0 1 2 2v2M20 16v2a2 2 0 0 1-2 2h-2M8 20H6a2 2 0 0 1-2-2v-2"),
                .circle(12, 10.5, 2.8),
                .path("M7.5 17.5c.8-2.2 2.5-3.3 4.5-3.3s3.7 1.1 4.5 3.3"),
            ]
        case .collections:
            // A photo with another behind it: photos put together.
            return [
                .path("M7.5 4.5h10a3 3 0 0 1 3 3v10"),
                .rect(3.5, 7.5, 13, 13, 2),
                .circle(8, 11.5, 1.3),
                .path("m4 19 4-4 3 3 2-2 3.5 3.5"),
            ]
        case .files:
            return [.path("M3.5 7.5a2 2 0 0 1 2-2h4l2 2h7a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2v-10Z")]
        case .social:
            return [
                .path("M4 5.5h12a1.5 1.5 0 0 1 1.5 1.5v7A1.5 1.5 0 0 1 16 15.5H9l-4 3.5v-3.5H4A1.5 1.5 0 0 1 2.5 14V7A1.5 1.5 0 0 1 4 5.5Z"),
                .path("M20.5 9.5v7.5a1.5 1.5 0 0 1-1.5 1.5h-.5V21l-3.5-2.5"),
            ]
        case .friends:
            return [
                .circle(9, 8, 3), .path("M3.5 19c0-3 2.5-5 5.5-5s5.5 2 5.5 5"),
                .circle(17, 9, 2.5), .path("M15.5 14.2c2.4.3 4 2 4 4.8"),
            ]
        case .alerts:
            return [
                .path("M12 3a5 5 0 0 0-5 5v2.7c0 1.15-.45 2.25-1.26 3.06L4.5 15h15l-1.24-1.24A4.33 4.33 0 0 1 17 10.7V8a5 5 0 0 0-5-5Z"),
                .path("M9.5 18a2.5 2.5 0 0 0 5 0"),
            ]
        case .settings:
            // The gear's outline fills the whole box: drawn at 85% so it
            // weighs the same as the other icons (the web's transform).
            return [
                .circle(12, 12, 3),
                .path("M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1.08-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1.08 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z"),
            ]
        case .storage:
            return [.rect(3.5, 4.5, 17, 6, 1.5), .rect(3.5, 13.5, 17, 6, 1.5), .path("M7 7.5h.01M7 16.5h.01")]
        case .chevron:
            // Points down: a section that opens below its row.
            return [.path("m7 10 5 5 5-5")]
        case .addToCollection:
            return [.path("M7.5 4.5h10a3 3 0 0 1 3 3v10"), .rect(3.5, 7.5, 13, 13, 2), .path("M10 11v6M7 14h6")]
        case .merge:
            return [
                .path("M6 20v-3.5a4 4 0 0 1 1.17-2.83L12 9"),
                .path("M18 20v-3.5a4 4 0 0 0-1.17-2.83L12 9V4"),
                .path("m8.5 7.5 3.5-3.5 3.5 3.5"),
            ]
        case .search:
            return [.circle(10.5, 10.5, 6), .path("m15 15 5 5")]
        case .docSearch:
            // A page with a magnifier over its corner: search the documents.
            return [
                .path("M10.5 20.5h-4a2 2 0 0 1-2-2v-13a2 2 0 0 1 2-2h7l4 4v3"),
                .path("M13.5 3.5v4h4M8 11h4M8 14.5h2"),
                .circle(16, 16, 3.2),
                .path("m18.4 18.4 2.6 2.6"),
            ]
        case .tag:
            return [
                .path("M3.5 12V5.5a2 2 0 0 1 2-2H12a2 2 0 0 1 1.4.6l6.9 6.9a2 2 0 0 1 0 2.8l-6.6 6.6a2 2 0 0 1-2.8 0l-6.9-6.9A2 2 0 0 1 3.5 12Z"),
                .circle(8.5, 8.5, 1.4),
            ]
        case .face:
            return [.circle(12, 9.5, 3.5), .path("M5.5 19.5c1.1-3.2 3.6-4.8 6.5-4.8s5.4 1.6 6.5 4.8")]
        case .video:
            return [.rect(3.5, 5.5, 17, 13, 2.5), .path("m10.5 9.5 4 2.5-4 2.5Z")]
        case .doc:
            return [
                .path("M6.5 3.5h7l4 4v11a2 2 0 0 1-2 2h-9a2 2 0 0 1-2-2v-13a2 2 0 0 1 2-2Z"),
                .path("M13.5 3.5v4h4M8.5 12.5h7M8.5 16h5"),
            ]
        }
    }

    /// Anything drawn smaller inside the 24-point box (the web's <g transform>).
    fileprivate var transform: CGAffineTransform {
        switch self {
        case .settings: return CGAffineTransform(translationX: 1.8, y: 1.8).scaledBy(x: 0.85, y: 0.85)
        default: return .identity
        }
    }

    /// How much the web's <g transform> shrinks the stroke along with the
    /// drawing: SVG strokes in the element's own units, so the gear's 1.6
    /// is 1.36 in the box.
    fileprivate var strokeScale: CGFloat {
        let t = transform
        return sqrt(abs(t.a * t.d - t.b * t.c))
    }

    /// The width a stroke of `stroke` (in the 24-point box, as the web
    /// writes it) is drawn at for an icon `size` points wide.
    func lineWidth(_ stroke: CGFloat, size: CGFloat) -> CGFloat {
        stroke * strokeScale * size / 24
    }

    /// The stroke the web draws with, in the icon's 24-point box.
    static let webStroke: CGFloat = 1.6

    /// The outline in its 24 x 24 box, built once per icon.
    var path: Path {
        NavIconCache.shared.path(for: self)
    }

    /// A template image of the icon, for places that only take an Image (a
    /// tab item, a Label, UIKit): drawn once per size and tinted like an SF
    /// Symbol by whatever shows it.
    func image(size: CGFloat = 24, stroke: CGFloat = NavIcon.webStroke) -> Image {
        Image(uiImage: uiImage(size: size, stroke: stroke))
    }

    func uiImage(size: CGFloat = 24, stroke: CGFloat = NavIcon.webStroke) -> UIImage {
        NavIconCache.shared.image(for: self, size: size, stroke: stroke)
    }
}

/// One icon, drawn in the colour around it (foregroundStyle / tint).
struct NavIconView: View {
    let icon: NavIcon
    var size: CGFloat = 22
    /// In the 24-point box, as the web writes it: 1.6.
    var stroke: CGFloat = NavIcon.webStroke

    init(_ icon: NavIcon, size: CGFloat = 22, stroke: CGFloat = NavIcon.webStroke) {
        self.icon = icon
        self.size = size
        self.stroke = stroke
    }

    var body: some View {
        NavIconShape(icon: icon)
            .stroke(style: StrokeStyle(lineWidth: icon.lineWidth(stroke, size: size), lineCap: .round, lineJoin: .round))
            .frame(width: size, height: size)
            .accessibilityHidden(true)
    }
}

/// The outline scaled into whatever rect it is given (kept square, centred).
struct NavIconShape: Shape {
    let icon: NavIcon

    func path(in rect: CGRect) -> Path {
        let side = min(rect.width, rect.height)
        let scale = side / 24
        let t = CGAffineTransform(translationX: rect.midX - side / 2, y: rect.midY - side / 2)
            .scaledBy(x: scale, y: scale)
        return icon.path.applying(t)
    }
}

// MARK: - Drawing

fileprivate enum NavIconElement {
    case path(String)
    /// x, y, width, height, corner radius.
    case rect(CGFloat, CGFloat, CGFloat, CGFloat, CGFloat)
    /// Centre x, centre y, radius.
    case circle(CGFloat, CGFloat, CGFloat)
}

/// The parsed outlines and the template images drawn from them. Main
/// thread only, like the views that use it.
private final class NavIconCache: @unchecked Sendable {
    static let shared = NavIconCache()
    private var paths: [NavIcon: Path] = [:]
    private var images: [String: UIImage] = [:]
    private let lock = NSLock()

    func path(for icon: NavIcon) -> Path {
        lock.lock()
        defer { lock.unlock() }
        if let p = paths[icon] { return p }
        var p = Path()
        for e in icon.elements {
            switch e {
            case .path(let d): SVGPath.append(d, to: &p)
            case let .rect(x, y, w, h, r):
                p.addRoundedRect(in: CGRect(x: x, y: y, width: w, height: h),
                                 cornerSize: CGSize(width: r, height: r), style: .circular)
            case let .circle(cx, cy, r):
                p.addEllipse(in: CGRect(x: cx - r, y: cy - r, width: 2 * r, height: 2 * r))
            }
        }
        p = p.applying(icon.transform)
        paths[icon] = p
        return p
    }

    func image(for icon: NavIcon, size: CGFloat, stroke: CGFloat) -> UIImage {
        let key = "\(icon)|\(size)|\(stroke)"
        lock.lock()
        if let img = images[key] { lock.unlock(); return img }
        lock.unlock()
        let path = self.path(for: icon).applying(CGAffineTransform(scaleX: size / 24, y: size / 24)).cgPath
        let img = UIGraphicsImageRenderer(size: CGSize(width: size, height: size)).image { ctx in
            let cg = ctx.cgContext
            cg.addPath(path)
            cg.setLineWidth(icon.lineWidth(stroke, size: size))
            cg.setLineCap(.round)
            cg.setLineJoin(.round)
            cg.setStrokeColor(UIColor.black.cgColor)
            cg.strokePath()
        }.withRenderingMode(.alwaysTemplate)
        lock.lock()
        images[key] = img
        lock.unlock()
        return img
    }
}

/// Just enough of SVG's path syntax for these drawings, and the rest of it
/// besides: M L H V C S Q T A Z, absolute and relative, numbers written as
/// the web writes them ("-.45", "1.5.5", flags without separators).
enum SVGPath {
    static func append(_ d: String, to path: inout Path) {
        var s = Scanner(Array(d.utf8))
        var cmd: UInt8 = 0
        var cur = CGPoint.zero
        var start = CGPoint.zero
        // The last control point, for S and T's reflection.
        var lastCubic: CGPoint?
        var lastQuad: CGPoint?

        while true {
            if let c = s.command() {
                cmd = c
            } else if s.hasNumber(), cmd != 0, cmd != UInt8(ascii: "Z"), cmd != UInt8(ascii: "z") {
                // The same command again, its letter left out.
            } else {
                return
            }
            let rel = cmd >= UInt8(ascii: "a")
            func pt(_ x: CGFloat, _ y: CGFloat) -> CGPoint { rel ? CGPoint(x: cur.x + x, y: cur.y + y) : CGPoint(x: x, y: y) }
            var cubic: CGPoint?
            var quad: CGPoint?
            switch cmd {
            case UInt8(ascii: "M"), UInt8(ascii: "m"):
                guard let x = s.number(), let y = s.number() else { return }
                cur = pt(x, y)
                start = cur
                path.move(to: cur)
                // More pairs after a move are lines.
                cmd = rel ? UInt8(ascii: "l") : UInt8(ascii: "L")
            case UInt8(ascii: "L"), UInt8(ascii: "l"):
                guard let x = s.number(), let y = s.number() else { return }
                cur = pt(x, y)
                path.addLine(to: cur)
            case UInt8(ascii: "H"), UInt8(ascii: "h"):
                guard let x = s.number() else { return }
                cur = CGPoint(x: rel ? cur.x + x : x, y: cur.y)
                path.addLine(to: cur)
            case UInt8(ascii: "V"), UInt8(ascii: "v"):
                guard let y = s.number() else { return }
                cur = CGPoint(x: cur.x, y: rel ? cur.y + y : y)
                path.addLine(to: cur)
            case UInt8(ascii: "C"), UInt8(ascii: "c"):
                guard let x1 = s.number(), let y1 = s.number(), let x2 = s.number(), let y2 = s.number(),
                      let x = s.number(), let y = s.number() else { return }
                let c1 = pt(x1, y1), c2 = pt(x2, y2), end = pt(x, y)
                path.addCurve(to: end, control1: c1, control2: c2)
                cubic = c2
                cur = end
            case UInt8(ascii: "S"), UInt8(ascii: "s"):
                guard let x2 = s.number(), let y2 = s.number(), let x = s.number(), let y = s.number() else { return }
                let c1 = lastCubic.map { CGPoint(x: 2 * cur.x - $0.x, y: 2 * cur.y - $0.y) } ?? cur
                let c2 = pt(x2, y2), end = pt(x, y)
                path.addCurve(to: end, control1: c1, control2: c2)
                cubic = c2
                cur = end
            case UInt8(ascii: "Q"), UInt8(ascii: "q"):
                guard let x1 = s.number(), let y1 = s.number(), let x = s.number(), let y = s.number() else { return }
                let c = pt(x1, y1), end = pt(x, y)
                path.addQuadCurve(to: end, control: c)
                quad = c
                cur = end
            case UInt8(ascii: "T"), UInt8(ascii: "t"):
                guard let x = s.number(), let y = s.number() else { return }
                let c = lastQuad.map { CGPoint(x: 2 * cur.x - $0.x, y: 2 * cur.y - $0.y) } ?? cur
                let end = pt(x, y)
                path.addQuadCurve(to: end, control: c)
                quad = c
                cur = end
            case UInt8(ascii: "A"), UInt8(ascii: "a"):
                guard let rx = s.number(), let ry = s.number(), let rot = s.number(),
                      let large = s.flag(), let sweep = s.flag(),
                      let x = s.number(), let y = s.number() else { return }
                let end = pt(x, y)
                addArc(to: &path, from: cur, rx: rx, ry: ry, rotation: rot, largeArc: large, sweep: sweep, end: end)
                cur = end
            case UInt8(ascii: "Z"), UInt8(ascii: "z"):
                path.closeSubpath()
                cur = start
            default:
                return
            }
            lastCubic = cubic
            lastQuad = quad
        }
    }

    /// SVG's elliptical arc (endpoint form) as cubic curves, one per
    /// quarter turn at most - the conversion in the SVG spec's appendix.
    private static func addArc(to path: inout Path, from p0: CGPoint, rx rx0: CGFloat, ry ry0: CGFloat,
                               rotation: CGFloat, largeArc: Bool, sweep: Bool, end p: CGPoint) {
        if p0 == p { return }
        var rx = abs(rx0), ry = abs(ry0)
        if rx == 0 || ry == 0 {
            path.addLine(to: p)
            return
        }
        let phi = rotation * .pi / 180
        let cosP = cos(phi), sinP = sin(phi)
        let dx2 = (p0.x - p.x) / 2, dy2 = (p0.y - p.y) / 2
        let x1 = cosP * dx2 + sinP * dy2
        let y1 = -sinP * dx2 + cosP * dy2
        // Radii too small to reach: scaled up just enough.
        let lambda = (x1 * x1) / (rx * rx) + (y1 * y1) / (ry * ry)
        if lambda > 1 {
            rx *= lambda.squareRoot()
            ry *= lambda.squareRoot()
        }
        let num = rx * rx * ry * ry - rx * rx * y1 * y1 - ry * ry * x1 * x1
        let den = rx * rx * y1 * y1 + ry * ry * x1 * x1
        let coef = (den == 0 ? 0 : max(0, num / den).squareRoot()) * (largeArc == sweep ? -1 : 1)
        let cxp = coef * rx * y1 / ry
        let cyp = -coef * ry * x1 / rx
        let cx = cosP * cxp - sinP * cyp + (p0.x + p.x) / 2
        let cy = sinP * cxp + cosP * cyp + (p0.y + p.y) / 2

        func angle(_ ux: CGFloat, _ uy: CGFloat, _ vx: CGFloat, _ vy: CGFloat) -> CGFloat {
            atan2(ux * vy - uy * vx, ux * vx + uy * vy)
        }
        let ux = (x1 - cxp) / rx, uy = (y1 - cyp) / ry
        let vx = (-x1 - cxp) / rx, vy = (-y1 - cyp) / ry
        let theta1 = angle(1, 0, ux, uy)
        var delta = angle(ux, uy, vx, vy)
        if !sweep && delta > 0 { delta -= 2 * .pi }
        if sweep && delta < 0 { delta += 2 * .pi }

        let segments = max(1, Int((abs(delta) / (.pi / 2)).rounded(.up)))
        let step = delta / CGFloat(segments)
        let k = 4 / 3 * tan(step / 4)
        func map(_ x: CGFloat, _ y: CGFloat) -> CGPoint {
            CGPoint(x: cx + rx * x * cosP - ry * y * sinP, y: cy + rx * x * sinP + ry * y * cosP)
        }
        for i in 0..<segments {
            let a1 = theta1 + CGFloat(i) * step
            let a2 = a1 + step
            let c1 = map(cos(a1) - k * sin(a1), sin(a1) + k * cos(a1))
            let c2 = map(cos(a2) + k * sin(a2), sin(a2) - k * cos(a2))
            // The last one lands exactly on the end point, not a rounding off it.
            let e = i == segments - 1 ? p : map(cos(a2), sin(a2))
            path.addCurve(to: e, control1: c1, control2: c2)
        }
    }

    private struct Scanner {
        let s: [UInt8]
        var i = 0
        init(_ s: [UInt8]) { self.s = s }

        private static func isDigit(_ c: UInt8) -> Bool { c >= 48 && c <= 57 }

        mutating func skip() {
            while i < s.count, s[i] == 32 || s[i] == 44 || s[i] == 9 || s[i] == 10 || s[i] == 13 { i += 1 }
        }

        mutating func command() -> UInt8? {
            skip()
            guard i < s.count else { return nil }
            let c = s[i]
            // Letters, but not an exponent's "e" (only ever after a digit).
            guard (c >= 65 && c <= 90) || (c >= 97 && c <= 122), c != UInt8(ascii: "e"), c != UInt8(ascii: "E") else { return nil }
            i += 1
            return c
        }

        mutating func hasNumber() -> Bool {
            skip()
            guard i < s.count else { return false }
            let c = s[i]
            return Self.isDigit(c) || c == UInt8(ascii: "-") || c == UInt8(ascii: "+") || c == UInt8(ascii: ".")
        }

        mutating func number() -> CGFloat? {
            guard hasNumber() else { return nil }
            let from = i
            if s[i] == UInt8(ascii: "-") || s[i] == UInt8(ascii: "+") { i += 1 }
            var dot = false
            while i < s.count {
                let c = s[i]
                if Self.isDigit(c) {
                    i += 1
                } else if c == UInt8(ascii: "."), !dot {
                    dot = true
                    i += 1
                } else {
                    break
                }
            }
            if i < s.count, s[i] == UInt8(ascii: "e") || s[i] == UInt8(ascii: "E") {
                var j = i + 1
                if j < s.count, s[j] == UInt8(ascii: "-") || s[j] == UInt8(ascii: "+") { j += 1 }
                if j < s.count, Self.isDigit(s[j]) {
                    i = j
                    while i < s.count, Self.isDigit(s[i]) { i += 1 }
                }
            }
            guard let text = String(bytes: s[from..<i], encoding: .ascii), let v = Double(text) else { return nil }
            return CGFloat(v)
        }

        /// An arc's flag: one character, 0 or 1, separator optional.
        mutating func flag() -> Bool? {
            skip()
            guard i < s.count else { return nil }
            let c = s[i]
            guard c == UInt8(ascii: "0") || c == UInt8(ascii: "1") else { return nil }
            i += 1
            return c == UInt8(ascii: "1")
        }
    }
}
