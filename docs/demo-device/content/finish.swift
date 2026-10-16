// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Turns generated stills into believable camera JPEGs for the demo device's library.
//   finish jobs.tsv
// Each line of jobs.tsv (tab separated): in  out  longSide  "YYYY:MM:DD HH:MM:SS"  "+02:00"  [lat  lon]
//  - scales the image so its long side is longSide px (Lanczos; a light luminance sharpen
//    when it was enlarged), sRGB, JPEG quality 0.9;
//  - writes EXIF DateTimeOriginal / DateTimeDigitized (+ OffsetTime*), TIFF DateTime,
//    Orientation 1, the pixel dimensions and, when lat/lon are given, GPS latitude/longitude
//    (with the UTC GPS date/time stamps a phone writes). No Make/Model, no camera settings.
// Build: swiftc -O finish.swift -o finish
import CoreImage
import Foundation
import ImageIO
import UniformTypeIdentifiers

let ctx = CIContext(options: [.workingColorSpace: CGColorSpace(name: CGColorSpace.sRGB)!])

func finish(_ f: [String]) throws {
    let (inPath, outPath) = (f[0], f[1])
    let long = Double(f[2])!
    let (dt, off) = (f[3], f[4])
    guard let src = CGImageSourceCreateWithURL(URL(fileURLWithPath: inPath) as CFURL, nil),
          let cg = CGImageSourceCreateImageAtIndex(src, 0, nil) else { throw NSError(domain: "read " + inPath, code: 1) }
    var img = CIImage(cgImage: cg)
    let scale = long / Double(max(cg.width, cg.height))
    if abs(scale - 1) > 0.001 {
        let l = CIFilter(name: "CILanczosScaleTransform")!
        l.setValue(img, forKey: kCIInputImageKey)
        l.setValue(scale, forKey: kCIInputScaleKey)
        l.setValue(1.0, forKey: kCIInputAspectRatioKey)
        img = l.outputImage!
        if scale > 1 {
            let s = CIFilter(name: "CISharpenLuminance")!
            s.setValue(img, forKey: kCIInputImageKey)
            s.setValue(0.35, forKey: kCIInputSharpnessKey)
            s.setValue(1.6, forKey: kCIInputRadiusKey)
            img = s.outputImage!
        }
    }
    let w = Int((Double(cg.width) * scale).rounded()), h = Int((Double(cg.height) * scale).rounded())
    let rect = CGRect(x: 0, y: 0, width: w, height: h)
    guard let out = ctx.createCGImage(img.cropped(to: rect), from: rect, format: .RGBA8,
                                      colorSpace: CGColorSpace(name: CGColorSpace.sRGB)!) else { throw NSError(domain: "render", code: 2) }
    var exif: [CFString: Any] = [
        kCGImagePropertyExifDateTimeOriginal: dt,
        kCGImagePropertyExifDateTimeDigitized: dt,
        kCGImagePropertyExifOffsetTimeOriginal: off,
        kCGImagePropertyExifOffsetTimeDigitized: off,
        kCGImagePropertyExifOffsetTime: off,
        kCGImagePropertyExifPixelXDimension: w,
        kCGImagePropertyExifPixelYDimension: h,
        kCGImagePropertyExifColorSpace: 1,
    ]
    exif[kCGImagePropertyExifSubsecTimeOriginal] = String(format: "%03d", abs(dt.hashValue) % 1000)
    var props: [CFString: Any] = [
        kCGImageDestinationLossyCompressionQuality: 0.9,
        kCGImagePropertyOrientation: 1,
        kCGImagePropertyExifDictionary: exif,
        kCGImagePropertyTIFFDictionary: [kCGImagePropertyTIFFDateTime: dt, kCGImagePropertyTIFFOrientation: 1],
    ]
    if f.count >= 7, let lat = Double(f[5]), let lon = Double(f[6]) {
        // The GPS stamps are UTC: the local time less its offset.
        let fmt = DateFormatter()
        fmt.locale = Locale(identifier: "en_US_POSIX")
        fmt.dateFormat = "yyyy:MM:dd HH:mm:ssxxx"
        let when = fmt.date(from: dt + off)!
        let u = DateFormatter()
        u.locale = Locale(identifier: "en_US_POSIX")
        u.timeZone = TimeZone(identifier: "UTC")
        u.dateFormat = "yyyy:MM:dd"
        let ds = u.string(from: when)
        u.dateFormat = "HH:mm:ss"
        props[kCGImagePropertyGPSDictionary] = [
            kCGImagePropertyGPSLatitude: abs(lat), kCGImagePropertyGPSLatitudeRef: lat >= 0 ? "N" : "S",
            kCGImagePropertyGPSLongitude: abs(lon), kCGImagePropertyGPSLongitudeRef: lon >= 0 ? "E" : "W",
            kCGImagePropertyGPSDateStamp: ds, kCGImagePropertyGPSTimeStamp: u.string(from: when),
        ] as [CFString: Any]
    }
    try? FileManager.default.createDirectory(at: URL(fileURLWithPath: outPath).deletingLastPathComponent(), withIntermediateDirectories: true)
    guard let dst = CGImageDestinationCreateWithURL(URL(fileURLWithPath: outPath) as CFURL, UTType.jpeg.identifier as CFString, 1, nil)
    else { throw NSError(domain: "write " + outPath, code: 3) }
    CGImageDestinationAddImage(dst, out, props as CFDictionary)
    if !CGImageDestinationFinalize(dst) { throw NSError(domain: "finalize " + outPath, code: 4) }
}

let jobs = try String(contentsOfFile: CommandLine.arguments[1], encoding: .utf8)
var failed = 0
for line in jobs.split(separator: "\n") where !line.isEmpty {
    let f = line.split(separator: "\t", omittingEmptySubsequences: false).map(String.init)
    do { try finish(f); print("ok", f[1]) } catch { print("FAIL", f[1], error); failed += 1 }
}
exit(failed == 0 ? 0 : 1)
