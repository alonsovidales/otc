// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Puts a screen capture on the blank white screen of a generated photo (docs/promo).
//   composite base.jpg content.png out.png seedX seedY [--aspect A] [--toph H] [--blur R]
//             [--expand 5] [--reach 4] [--lift 0.05] [--soft 0] [--th 190] [--chroma 35]
//  1. flood-fills the white region around the seed (the screen inside its bezel);
//  2. fits a straight line to each of its four edges (total least squares, trimmed, away
//     from the rounded corners) and intersects them for sub-pixel corners;
//  3. estimates the screen's aspect ratio from the perspective (Zhang & He), or takes --aspect;
//  4. warps the content onto that quad grown by --expand px, supersampled 2x (the content
//     Lanczos-scaled first), blurred by --blur px for a screen out of focus;
//  5. composites: each pixel of the screen and of its soft edge (--reach px past the white)
//     is split into its lit-screen and bezel shares by brightness, and only the screen's
//     share becomes page - no light seam, and the photo's own antialiasing at the edge;
//  6. never covers anything colourful standing in front of the screen (a hue other than the
//     screen white's own tint); its edge is un-mixed into screen, bezel and object shares,
//     so it keeps a clean outline over the page.
// A screen cut off by the top of the frame gets its top edge from --toph (its height as a
// fraction of its bottom width). Build: swiftc -O composite.swift -o composite
import AppKit
import CoreImage

var args = Array(CommandLine.arguments.dropFirst())
func opt(_ k: String) -> Double? { if let i = args.firstIndex(of: k), i + 1 < args.count { let v = Double(args[i+1]); args.removeSubrange(i...i+1); return v }; return nil }
let th = opt("--th") ?? 190, aspectOverride = opt("--aspect"), blurR = opt("--blur") ?? 0, expand = opt("--expand") ?? 5, topH = opt("--toph")
let reach = Int(opt("--reach") ?? 4), lift = opt("--lift") ?? 0.05, soft = opt("--soft") ?? 0
let chromaT = opt("--chroma") ?? 35
let (basePath, contentPath, outPath) = (args[0], args[1], args[2])
let seed = (Int(args[3])!, Int(args[4])!)

let srgb = CGColorSpace(name: CGColorSpace.sRGB)!
func loadCG(_ p: String) -> CGImage { let i = NSImage(contentsOf: URL(fileURLWithPath: p))!; var r = CGRect(origin: .zero, size: i.size); return i.cgImage(forProposedRect: &r, context: nil, hints: nil)! }
func buffer(_ cg: CGImage) -> (CGContext, UnsafeMutablePointer<UInt8>) {
    let c = CGContext(data: nil, width: cg.width, height: cg.height, bitsPerComponent: 8, bytesPerRow: cg.width * 4, space: srgb, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
    c.draw(cg, in: CGRect(x: 0, y: 0, width: cg.width, height: cg.height))
    return (c, c.data!.assumingMemoryBound(to: UInt8.self))
}
let baseCG = loadCG(basePath)
let W = baseCG.width, H = baseCG.height
let (baseCtx, bp) = buffer(baseCG)   // row 0 = top
let orig = [UInt8](UnsafeBufferPointer(start: bp, count: W * H * 4))   // the photo as it came, for every measurement
func lum(_ x: Int, _ y: Int) -> Double { let i = (y * W + x) * 4; return 0.299 * Double(orig[i]) + 0.587 * Double(orig[i+1]) + 0.114 * Double(orig[i+2]) }
func chroma(_ x: Int, _ y: Int) -> Double { let i = (y * W + x) * 4; let r = Double(orig[i]), g = Double(orig[i+1]), b = Double(orig[i+2]); return max(r, g, b) - min(r, g, b) }

// 1. Flood fill.
var mask = [UInt8](repeating: 0, count: W * H)
var stack = [seed]; mask[seed.1 * W + seed.0] = 1
var pts: [(Int, Int)] = []
while let (x, y) = stack.popLast() {
    pts.append((x, y))
    for (dx, dy) in [(1,0),(-1,0),(0,1),(0,-1)] {
        let nx = x + dx, ny = y + dy
        if nx < 0 || ny < 0 || nx >= W || ny >= H { continue }
        let k = ny * W + nx
        if mask[k] == 0 && lum(nx, ny) >= th { mask[k] = 1; stack.append((nx, ny)) }
    }
}
// Fill holes (anything enclosed) by flood-filling the outside instead.
do {
    var outside = [UInt8](repeating: 0, count: W * H); var st: [(Int, Int)] = []
    for x in 0..<W { for y in [0, H-1] where mask[y*W+x] == 0 && outside[y*W+x] == 0 { outside[y*W+x] = 1; st.append((x,y)) } }
    for y in 0..<H { for x in [0, W-1] where mask[y*W+x] == 0 && outside[y*W+x] == 0 { outside[y*W+x] = 1; st.append((x,y)) } }
    while let (x, y) = st.popLast() { for (dx, dy) in [(1,0),(-1,0),(0,1),(0,-1)] { let nx = x+dx, ny = y+dy; if nx < 0 || ny < 0 || nx >= W || ny >= H { continue }; let k = ny*W+nx; if mask[k] == 0 && outside[k] == 0 { outside[k] = 1; st.append((nx,ny)) } } }
    for k in 0..<(W*H) where outside[k] == 0 { mask[k] = 1 }
}
FileHandle.standardError.write("region: \(pts.count) px\n".data(using: .utf8)!)

// 2. Boundary points, rough corners, edge classification, line fits.
var bnd: [(Double, Double)] = []
for y in 0..<H { for x in 0..<W where mask[y*W+x] == 1 {
    if x == 0 || y == 0 || x == W-1 || y == H-1 { continue }   // the image frame is not an edge
    if mask[y*W+x-1] == 0 || mask[y*W+x+1] == 0 || mask[(y-1)*W+x] == 0 || mask[(y+1)*W+x] == 0 { bnd.append((Double(x), Double(y))) }
} }
var touchesTop = false
for x in 0..<W where mask[x] == 1 { touchesTop = true; break }
func ext(_ f: ((Double, Double)) -> Double, _ maxi: Bool) -> (Double, Double) { maxi ? bnd.max { f($0) < f($1) }! : bnd.min { f($0) < f($1) }! }
var rTL = ext({ $0.0 + $0.1 }, false), rTR = ext({ $0.0 - $0.1 }, true), rBR = ext({ $0.0 + $0.1 }, true), rBL = ext({ $0.0 - $0.1 }, false)
if touchesTop {   // the top corners are where the region meets the frame
    var minX = W, maxX = 0
    for x in 0..<W where mask[x] == 1 { minX = min(minX, x); maxX = max(maxX, x) }
    rTL = (Double(minX), 0); rTR = (Double(maxX), 0)
}
struct Line { var a, b, c: Double }   // a x + b y + c = 0, (a,b) unit
func fit(_ p: [(Double, Double)]) -> Line {
    let n = Double(p.count); let mx = p.map { $0.0 }.reduce(0, +) / n, my = p.map { $0.1 }.reduce(0, +) / n
    var sxx = 0.0, sxy = 0.0, syy = 0.0
    for (x, y) in p { sxx += (x-mx)*(x-mx); sxy += (x-mx)*(y-my); syy += (y-my)*(y-my) }
    let ang = 0.5 * atan2(2*sxy, sxx - syy)          // direction of the line
    let a = -sin(ang), b = cos(ang)
    return Line(a: a, b: b, c: -(a*mx + b*my))
}
func dist(_ l: Line, _ p: (Double, Double)) -> Double { l.a*p.0 + l.b*p.1 + l.c }
func lineThrough(_ p: (Double, Double), _ q: (Double, Double)) -> Line { let dx = q.0-p.0, dy = q.1-p.1, n = hypot(dx, dy); let a = -dy/n, b = dx/n; return Line(a: a, b: b, c: -(a*p.0 + b*p.1)) }
let rough = [lineThrough(rTL, rTR), lineThrough(rTR, rBR), lineThrough(rBR, rBL), lineThrough(rBL, rTL)]
let rc = [rTL, rTR, rBR, rBL]
var groups: [[(Double, Double)]] = [[], [], [], []]
for p in bnd {
    let d = rough.map { abs(dist($0, p)) }
    let e = d.firstIndex(of: d.min()!)!
    // Away from the corners (their rounding would bend the fit): 6% of the edge each end.
    let p0 = rc[e], p1 = rc[(e+1)%4]; let len = hypot(p1.0-p0.0, p1.1-p0.1)
    let t = ((p.0-p0.0)*(p1.0-p0.0) + (p.1-p0.1)*(p1.1-p0.1)) / (len*len)
    if t > 0.06 && t < 0.94 && d[e] < 25 { groups[e].append(p) }
}
var lines: [Line] = []
for e in 0..<4 {
    if e == 0 && touchesTop { lines.append(rough[0]); continue }
    var g = groups[e]; var l = fit(g)
    for _ in 0..<3 { let keep = g.filter { abs(dist(l, $0)) < 1.5 }; if keep.count > 20 { g = keep; l = fit(g) } }
    lines.append(l)
}
func meet(_ l: Line, _ m: Line) -> (Double, Double) { let d = l.a*m.b - m.a*l.b; return ((l.b*m.c - m.b*l.c)/d, (m.a*l.c - l.a*m.c)/d) }
var BR = meet(lines[1], lines[2]), BL = meet(lines[2], lines[3])
var TL = meet(lines[3], lines[0]), TR = meet(lines[0], lines[1])
if touchesTop, let h = topH {
    // Top edge from the bottom width: walk up each side line by h x bottom width.
    let wB = hypot(BR.0-BL.0, BR.1-BL.1)
    func up(_ from: (Double, Double), _ l: Line) -> (Double, Double) { var dx = l.b, dy = -l.a; if dy > 0 { dx = -dx; dy = -dy }; return (from.0 + dx*h*wB, from.1 + dy*h*wB) }
    TL = up(BL, lines[3]); TR = up(BR, lines[1])
}
func f(_ p: (Double, Double)) -> String { String(format: "(%.1f,%.1f)", p.0, p.1) }
FileHandle.standardError.write("corners TL\(f(TL)) TR\(f(TR)) BR\(f(BR)) BL\(f(BL)); edge points \(groups.map { $0.count })\n".data(using: .utf8)!)

// 3. Aspect ratio from the perspective (principal point at the image centre).
func estAspect() -> Double {
    let u0 = Double(W)/2, v0 = Double(H)/2
    func v(_ p: (Double, Double)) -> [Double] { [p.0-u0, p.1-v0, 1] }
    func cross(_ a: [Double], _ b: [Double]) -> [Double] { [a[1]*b[2]-a[2]*b[1], a[2]*b[0]-a[0]*b[2], a[0]*b[1]-a[1]*b[0]] }
    func dot(_ a: [Double], _ b: [Double]) -> Double { a[0]*b[0]+a[1]*b[1]+a[2]*b[2] }
    let m1 = v(TL), m2 = v(TR), m3 = v(BL), m4 = v(BR)
    let k2 = dot(cross(m1, m4), m3) / dot(cross(m2, m4), m3)
    let k3 = dot(cross(m1, m4), m2) / dot(cross(m3, m4), m2)
    let n2 = [k2*m2[0]-m1[0], k2*m2[1]-m1[1], k2*m2[2]-m1[2]]
    let n3 = [k3*m3[0]-m1[0], k3*m3[1]-m1[1], k3*m3[2]-m1[2]]
    let f2 = -(n2[0]*n3[0] + n2[1]*n3[1]) / (n2[2]*n3[2])
    if !(f2 > 0) || abs(n2[2]*n3[2]) < 1e-9 { return sqrt((n2[0]*n2[0]+n2[1]*n2[1]) / (n3[0]*n3[0]+n3[1]*n3[1])) }
    return sqrt((n2[0]*n2[0]/f2 + n2[1]*n2[1]/f2 + n2[2]*n2[2]) / (n3[0]*n3[0]/f2 + n3[1]*n3[1]/f2 + n3[2]*n3[2]))
}
let est = estAspect()
let aspect = aspectOverride ?? est
FileHandle.standardError.write(String(format: "aspect: estimated %.3f, using %.3f\n", est, aspect).data(using: .utf8)!)

// 4. Content cropped to the aspect (keep the top-left: browser chrome and sidebar).
var content = CIImage(contentsOf: URL(fileURLWithPath: contentPath))!
let ce = content.extent
var cw = ce.width, ch = ce.height
if cw / ch > aspect { cw = ch * aspect } else { ch = cw / aspect }
content = content.cropped(to: CGRect(x: ce.minX, y: ce.maxY - ch, width: cw, height: ch))
content = content.transformed(by: CGAffineTransform(translationX: -content.extent.minX, y: -content.extent.minY))
// Grow the quad by `expand` px: offset each line away from the centre and intersect again.
let cx = (TL.0+TR.0+BR.0+BL.0)/4, cy = (TL.1+TR.1+BR.1+BL.1)/4
func offset(_ l: Line) -> Line { var m = l; if dist(l, (cx, cy)) > 0 { m.a = -m.a; m.b = -m.b; m.c = -m.c }; m.c -= expand; return m }
let qTop = lineThrough(TL, TR), qRight = lineThrough(TR, BR), qBottom = lineThrough(BR, BL), qLeft = lineThrough(BL, TL)
let oT = offset(qTop), oR = offset(qRight), oB = offset(qBottom), oL = offset(qLeft)
let eTL = meet(oL, oT), eTR = meet(oT, oR), eBR = meet(oR, oB), eBL = meet(oB, oL)
func cip(_ p: (Double, Double), _ k: Double) -> CIVector { CIVector(x: p.0 * k, y: (Double(H) - p.1) * k) }
// Supersampled: warp onto a 2x canvas, then Lanczos down to the photo's size. The content is
// first Lanczos-scaled to about the size it lands at on that canvas, so the warp's bilinear
// sampling never minifies much (which would alias the page's text).
let ss = 2.0
let landW = ss * max(hypot(eTR.0-eTL.0, eTR.1-eTL.1), hypot(eBR.0-eBL.0, eBR.1-eBL.1))
let pre = min(1, landW / Double(content.extent.width))
if pre < 1 { content = content.applyingFilter("CILanczosScaleTransform", parameters: [kCIInputScaleKey: pre, kCIInputAspectRatioKey: 1]) }
var warped = content.applyingFilter("CIPerspectiveTransform", parameters: [
    "inputTopLeft": cip(eTL, ss), "inputTopRight": cip(eTR, ss), "inputBottomRight": cip(eBR, ss), "inputBottomLeft": cip(eBL, ss)])
let rad = max(blurR, soft)
if rad > 0 { warped = warped.clampedToExtent().applyingFilter("CIGaussianBlur", parameters: ["inputRadius": rad * ss]).cropped(to: warped.extent) }
warped = warped.applyingFilter("CILanczosScaleTransform", parameters: [kCIInputScaleKey: 1 / ss, kCIInputAspectRatioKey: 1])
FileHandle.standardError.write(String(format: "content pre-scale %.3f, lands %.0f px wide (2x canvas)\n", pre, landW).data(using: .utf8)!)
let wctx = CGContext(data: nil, width: W, height: H, bitsPerComponent: 8, bytesPerRow: W * 4, space: srgb, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
let ci = CIContext(options: [.workingColorSpace: srgb, .outputColorSpace: srgb])
wctx.draw(ci.createCGImage(warped, from: CGRect(x: 0, y: 0, width: W, height: H), format: .RGBA8, colorSpace: srgb)!, in: CGRect(x: 0, y: 0, width: W, height: H))
let wp = wctx.data!.assumingMemoryBound(to: UInt8.self)
if let dbg = ProcessInfo.processInfo.environment["WARP_DEBUG"] {
    try! NSBitmapImageRep(cgImage: wctx.makeImage()!).representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: dbg))
}

// 5. Where to work: the quad grown by a margin; what stands in front of the screen.
let pad = 8 + max(reach, 12)
let minX = Int(min(eTL.0, eBL.0)) - pad, maxX = Int(max(eTR.0, eBR.0)) + pad, minY = Int(min(eTL.1, eTR.1)) - pad, maxY = Int(max(eBL.1, eBR.1)) + pad
// Something standing in front of the screen: colourful, in a hue other than the screen's
// own tint (a generated "white" screen is often a little cyan, and so are its soft edge
// and the bezel's glow - those are screen, not object).
func hue(_ r: Double, _ g: Double, _ b: Double) -> Double { atan2((r + g - 2*b) / 2.449, (r - g) / 1.414) }
var sr = 0.0, sg = 0.0, sb = 0.0, sn = 0.0
for (x, y) in pts where x % 3 == 0 && y % 3 == 0 { let i = (y*W+x)*4; sr += Double(orig[i]); sg += Double(orig[i+1]); sb += Double(orig[i+2]); sn += 1 }
sr /= sn; sg /= sn; sb /= sn
let tintHue: Double? = max(sr, sg, sb) - min(sr, sg, sb) >= 6 ? hue(sr, sg, sb) : nil
FileHandle.standardError.write(String(format: "screen white %.0f,%.0f,%.0f%@\n", sr, sg, sb, tintHue == nil ? " (neutral)" : String(format: ", tint hue %.0f deg", tintHue! * 180 / .pi)).data(using: .utf8)!)
func isObject(_ x: Int, _ y: Int) -> Bool {
    let i = (y*W+x)*4; let r = Double(orig[i]), g = Double(orig[i+1]), b = Double(orig[i+2])
    let c = max(r, g, b) - min(r, g, b)
    if c < 30 { return false }
    if c >= 80 { return true }
    guard let h0 = tintHue else { return c >= chromaT }
    var d = abs(hue(r, g, b) - h0); if d > .pi { d = 2 * .pi - d }
    return d > 35 * .pi / 180
}
// Pixels within 6 px of an object (outside the white region): its edge against the screen
// can be a few px of white-and-object blend that the threshold counted as screen.
var objNear = [UInt8](repeating: 0, count: W * H)
var objects = 0
for y in max(6, minY)..<min(H-6, maxY) { for x in max(6, minX)..<min(W-6, maxX) where mask[y*W+x] == 0 && isObject(x, y) {
    objects += 1
    for dy in -6...6 { for dx in -6...6 { objNear[(y+dy)*W+(x+dx)] = 1 } }
} }
FileHandle.standardError.write("object px near the screen: \(objects)\n".data(using: .utf8)!)
// 6. Composite. A pixel of the screen or of its soft edge is part lit screen, part bezel:
// c = a Wh + (1-a) Bz, with `a` read off its brightness between the bezel's (Lb) and the
// screen white's next to it (Lw). Swapping the white for the page keeps the bezel's share:
// c' = c + a (page - Wh). No light seam is left, and the edge stays antialiased exactly
// as the photo had it (an out-of-focus screen's soft edge included). Well inside the
// screen the page goes in as it is.
let bx0 = max(0, minX), bx1 = min(W, maxX), by0 = max(0, minY), by1 = min(H, maxY)
func bfs(seed: (Int) -> Bool, into: (Int) -> Bool, limit: Int) -> [UInt8] {
    var d = [UInt8](repeating: 255, count: W * H); var q: [Int] = []
    for y in by0..<by1 { for x in bx0..<bx1 where seed(y*W+x) { d[y*W+x] = 0; q.append(y*W+x) } }
    var h = 0
    while h < q.count { let k = q[h]; h += 1; let dk = Int(d[k]); if dk >= limit { continue }
        let x = k % W, y = k / W
        for (dx, dy) in [(1,0),(-1,0),(0,1),(0,-1)] { let nx = x+dx, ny = y+dy
            if nx < bx0 || ny < by0 || nx >= bx1 || ny >= by1 { continue }
            let nk = ny*W+nx; if d[nk] == 255 && into(nk) { d[nk] = UInt8(dk+1); q.append(nk) } } }
    return d
}
let outDist = bfs(seed: { mask[$0] == 1 }, into: { mask[$0] == 0 }, limit: max(reach, 12) + 1)   // px outside the region
let inDist = bfs(seed: { mask[$0] == 0 }, into: { mask[$0] == 1 }, limit: 4)                     // px inside it
var ring: [Double] = [], ringR: [Double] = [], ringG: [Double] = [], ringB: [Double] = []
for y in by0..<by1 { for x in bx0..<bx1 where (x + y) % 3 == 0 { let k = y*W+x; if outDist[k] >= 6 && outDist[k] <= 12 && objNear[k] == 0 {
    ring.append(lum(x, y)); ringR.append(Double(orig[k*4])); ringG.append(Double(orig[k*4+1])); ringB.append(Double(orig[k*4+2])) } } }
ring.sort(); ringR.sort(); ringG.sort(); ringB.sort()
let Lb = ring.isEmpty ? 30.0 : ring[ring.count / 2]
let bezel = ring.isEmpty ? (30.0, 30.0, 30.0) : (ringR[ring.count / 2], ringG[ring.count / 2], ringB[ring.count / 2])
// Light falloff of the original screen, normalised to its bright end.
var lightVals: [Double] = []
for (x, y) in pts where x % 4 == 0 && y % 4 == 0 { lightVals.append(lum(x, y)) }
lightVals.sort(); let ref = lightVals[Int(Double(lightVals.count) * 0.95)]
FileHandle.standardError.write(String(format: "bezel lum %.0f, screen white lum %.0f\n", Lb, ref).data(using: .utf8)!)
// The screen's white next to a pixel: the brightest region samples within 12 px (the
// soft edge's own darker pixels left out) - colour and luminance.
func localWhite(_ x: Int, _ y: Int) -> (Double, Double, Double, Double)? {
    var top = -1.0
    for dy in stride(from: -12, through: 12, by: 4) { for dx in stride(from: -12, through: 12, by: 4) {
        let nx = min(max(x+dx, 0), W-1), ny = min(max(y+dy, 0), H-1); if mask[ny*W+nx] == 1 { top = max(top, lum(nx, ny)) } } }
    if top < 0 { return nil }
    var r = 0.0, g = 0.0, b = 0.0, l = 0.0, n = 0.0
    for dy in stride(from: -12, through: 12, by: 4) { for dx in stride(from: -12, through: 12, by: 4) {
        let nx = min(max(x+dx, 0), W-1), ny = min(max(y+dy, 0), H-1)
        if mask[ny*W+nx] == 1 { let L = lum(nx, ny); if L >= top - 12 { let i = (ny*W+nx)*4; r += Double(orig[i]); g += Double(orig[i+1]); b += Double(orig[i+2]); l += L; n += 1 } } } }
    return (r/n, g/n, b/n, l/n)
}
// The page as shown at a pixel: the screen's light falloff (k), blacks lifted a little.
func page(_ x: Int, _ y: Int, _ k: Double) -> (Double, Double, Double)? {
    let i = (y * W + x) * 4; let aw = Double(wp[i+3]) / 255; if aw <= 0.5 { return nil }
    func v(_ c: Int) -> Double { (Double(wp[i+c]) / aw * (1 - lift) + 205 * lift) * k }
    return (v(0), v(1), v(2))
}
func page(_ x: Int, _ y: Int) -> (Double, Double, Double)? {
    guard let w = localWhite(x, y) else { return nil }
    return page(x, y, min(1, max(0.82, w.3 / ref)))
}
for y in by0..<by1 { for x in bx0..<bx1 {
    let k0 = y*W+x
    if objNear[k0] == 1 { continue }   // an object's edge: un-mixed below
    let inReg = mask[k0] == 1
    if !inReg && Int(outDist[k0]) > reach { continue }
    guard let w = localWhite(x, y) else { continue }
    guard let pv = page(x, y, min(1, max(0.82, w.3 / ref))) else { continue }
    let i = k0 * 4
    if inReg && inDist[k0] > 3 {
        bp[i] = UInt8(max(0, min(255, pv.0.rounded()))); bp[i+1] = UInt8(max(0, min(255, pv.1.rounded()))); bp[i+2] = UInt8(max(0, min(255, pv.2.rounded())))
        continue
    }
    let a = w.3 - Lb < 40 ? (inReg ? 1 : 0) : max(0, min(1, (lum(x, y) - Lb) / (w.3 - Lb)))
    if a <= 0 { continue }
    let out = [Double(orig[i]) + a * (pv.0 - w.0), Double(orig[i+1]) + a * (pv.1 - w.1), Double(orig[i+2]) + a * (pv.2 - w.2)]
    for c in 0..<3 { bp[i+c] = UInt8(max(0, min(255, out[c].rounded()))) }
} }
// An object in front of the screen: its edge pixels are part object, part white screen
// (and part bezel, where all three meet). Each is un-mixed into those shares, Wh being the
// screen's white beside it and P the object's own colour a few px in, and the page goes
// where the white was: c' = c + aW (page - Wh).
var matted = 0
for y in max(8, minY)..<min(H-8, maxY) { for x in max(8, minX)..<min(W-8, maxX) {
    let k0 = y*W+x
    let inRegion = mask[k0] == 1
    // Near an object, and on the screen or its soft edge.
    if objNear[k0] == 0 { continue }
    if !inRegion && Int(outDist[k0]) > reach { continue }   // the same extent as the soft edge above
    // Wh: the screen's white beside it (its brightest pixels around).
    guard let w = localWhite(x, y) else { continue }
    let wr = w.0, wg = w.1, wb = w.2
    // P: the object's own colour just behind this pixel - the mean of the object pixels
    // within 8 px that lie at least 3 px further from the screen than this one.
    var myDist: Int = 0
    if !inRegion { myDist = Int(outDist[k0]) }
    var pr = 0.0, pg = 0.0, pb = 0.0, pn = 0.0
    for dy in -8...8 { for dx in -8...8 { let k = (y+dy)*W+(x+dx), j = k*4
        let od: Int = Int(outDist[k])
        if mask[k] != 0 || od < myDist + 3 { continue }
        if isObject(x+dx, y+dy) { pr += Double(orig[j]); pg += Double(orig[j+1]); pb += Double(orig[j+2]); pn += 1 } } }
    if pn == 0 { continue }
    pr /= pn; pg /= pn; pb /= pn
    guard let pv = page(x, y, min(1, max(0.82, w.3 / ref))) else { continue }
    let j = k0 * 4
    let cr = Double(orig[j]), cg = Double(orig[j+1]), cb = Double(orig[j+2])
    // Three colours meet here - screen white, bezel, object: c - Bz = aW (Wh - Bz) + aP (P - Bz),
    // solved in least squares; only the white's share becomes page.
    let (br, bg, bb) = bezel
    let ux = wr - br, uy = wg - bg, uz = wb - bb, vx = pr - br, vy = pg - bg, vz = pb - bb
    let tx = cr - br, ty = cg - bg, tz = cb - bb
    let uu = ux*ux + uy*uy + uz*uz, vv = vx*vx + vy*vy + vz*vz, uv = ux*vx + uy*vy + uz*vz
    let tu = tx*ux + ty*uy + tz*uz, tv = tx*vx + ty*vy + tz*vz
    let det = uu*vv - uv*uv
    if det < 1 { continue }
    var aW = (tu*vv - tv*uv) / det, aP = (tv*uu - tu*uv) / det
    aW = max(0, aW); aP = max(0, aP); if aW + aP > 1 { let sum = aW + aP; aW /= sum; aP /= sum }
    let out = [cr + aW*(pv.0 - wr), cg + aW*(pv.1 - wg), cb + aW*(pv.2 - wb)]
    for c in 0..<3 { bp[j+c] = UInt8(max(0, min(255, out[c].rounded()))) }
    matted += 1
} }
FileHandle.standardError.write("un-mixed \(matted) edge px\n".data(using: .utf8)!)
let outCG = baseCtx.makeImage()!
let rep = NSBitmapImageRep(cgImage: outCG)
let data = outPath.hasSuffix(".jpg") ? rep.representation(using: .jpeg, properties: [.compressionFactor: 0.93])! : rep.representation(using: .png, properties: [:])!
try! data.write(to: URL(fileURLWithPath: outPath))
