// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FileTypeIcon.swift
//  OffTheCloud
//
//  The Files grid's tile for anything without a thumbnail: a generic
//  document with a folded corner and a coloured band naming its type. Not
//  brand logos on purpose - the same label/colour mapping is used by the web
//  and Android grids, so a file looks the same on every client.

import SwiftUI

private func hexColor(_ hex: UInt32) -> Color {
    Color(red: Double((hex >> 16) & 0xFF) / 255,
          green: Double((hex >> 8) & 0xFF) / 255,
          blue: Double(hex & 0xFF) / 255)
}

/// The label and band colour for a file, by its lowercased extension.
func fileTypeBadge(for name: String) -> (label: String, color: Color) {
    let dot = name.lastIndex(of: ".")
    let ext = dot.map { String(name[name.index(after: $0)...]).lowercased() } ?? ""
    let upper = String(ext.uppercased().prefix(4))
    switch ext {
    case "pdf": return ("PDF", hexColor(0xE5252A))
    case "doc", "docx", "odt", "rtf", "pages": return ("DOC", hexColor(0x2B579A))
    case "xls", "xlsx", "ods", "csv", "numbers": return ("XLS", hexColor(0x217346))
    case "ppt", "pptx", "odp", "key": return ("PPT", hexColor(0xD24726))
    case "txt", "md", "log": return ("TXT", hexColor(0x6B7280))
    case "zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "xz": return ("ZIP", hexColor(0xB7791F))
    case "mp3", "wav", "flac", "m4a", "aac", "ogg", "opus": return (upper, hexColor(0x7C3AED))
    case "json", "js", "ts", "go", "py", "swift", "kt", "java", "c", "cpp", "h",
         "html", "css", "xml", "yml", "yaml", "sh":
        return ("</>", hexColor(0x0F766E))
    case "apk": return ("APK", hexColor(0x3DDC84))
    case "jpg", "jpeg", "png", "heic", "gif", "webp", "tiff", "bmp", "dng", "raw", "cr2", "nef", "arw":
        return (upper, hexColor(0x0EA5E9))
    case "mp4", "mov", "m4v", "mkv", "avi", "webm", "3gp": return (upper, hexColor(0xDB2777))
    default: return (ext.isEmpty ? "FILE" : upper, hexColor(0x64748B))
    }
}

/// A portrait sheet (aspect ~0.78) with its top-right corner folded over and
/// the type label in white on a band across its lower part. It fills the
/// height it is given, so the caller sizes it with a frame.
struct FileTypeIcon: View {
    let name: String
    @Environment(\.colorScheme) private var scheme

    private var sheetFill: Color { scheme == .dark ? hexColor(0x48484A) : hexColor(0xE4E7EB) }
    private var foldFill: Color { scheme == .dark ? hexColor(0x2C2C2E) : hexColor(0xC4C9D0) }

    var body: some View {
        let badge = fileTypeBadge(for: name)
        GeometryReader { geo in
            let h = geo.size.height
            let w = min(geo.size.width, h * 0.78)
            let fold = w * 0.26
            ZStack {
                VStack(spacing: 0) {
                    Spacer(minLength: 0)
                    Rectangle()
                        .fill(badge.color)
                        .frame(height: h * 0.3)
                        .overlay(
                            Text(badge.label)
                                .font(.system(size: h * 0.17, weight: .bold, design: .rounded))
                                .foregroundColor(.white)
                                .lineLimit(1)
                                .minimumScaleFactor(0.3)
                                .padding(.horizontal, w * 0.08)
                        )
                    Color.clear.frame(height: h * 0.15)
                }
                .background(sheetFill)
                // Clipping the whole stack is what keeps the band inside
                // the sheet's outline.
                .clipShape(DocumentShape(fold: fold, radius: w * 0.08))
                .overlay(DocumentShape(fold: fold, radius: w * 0.08).stroke(Color(.separator), lineWidth: 0.5))
                // The folded-over corner, a shade darker than the sheet.
                FoldShape(fold: fold).fill(foldFill)
            }
            .frame(width: w, height: h)
            .position(x: geo.size.width / 2, y: h / 2)
        }
        .aspectRatio(0.78, contentMode: .fit)
        .accessibilityLabel("\(badge.label) file")
    }
}

/// The sheet: a rounded rectangle with a diagonal cut where the top-right
/// corner folds.
private struct DocumentShape: Shape {
    let fold: CGFloat
    let radius: CGFloat

    func path(in r: CGRect) -> Path {
        var p = Path()
        p.move(to: CGPoint(x: r.minX + radius, y: r.minY))
        p.addLine(to: CGPoint(x: r.maxX - fold, y: r.minY))
        p.addLine(to: CGPoint(x: r.maxX, y: r.minY + fold))
        p.addLine(to: CGPoint(x: r.maxX, y: r.maxY - radius))
        p.addQuadCurve(to: CGPoint(x: r.maxX - radius, y: r.maxY), control: CGPoint(x: r.maxX, y: r.maxY))
        p.addLine(to: CGPoint(x: r.minX + radius, y: r.maxY))
        p.addQuadCurve(to: CGPoint(x: r.minX, y: r.maxY - radius), control: CGPoint(x: r.minX, y: r.maxY))
        p.addLine(to: CGPoint(x: r.minX, y: r.minY + radius))
        p.addQuadCurve(to: CGPoint(x: r.minX + radius, y: r.minY), control: CGPoint(x: r.minX, y: r.minY))
        p.closeSubpath()
        return p
    }
}

/// The triangle of the fold itself, filling the cut DocumentShape leaves.
private struct FoldShape: Shape {
    let fold: CGFloat

    func path(in r: CGRect) -> Path {
        var p = Path()
        p.move(to: CGPoint(x: r.maxX - fold, y: r.minY))
        p.addLine(to: CGPoint(x: r.maxX - fold, y: r.minY + fold))
        p.addLine(to: CGPoint(x: r.maxX, y: r.minY + fold))
        p.closeSubpath()
        return p
    }
}
