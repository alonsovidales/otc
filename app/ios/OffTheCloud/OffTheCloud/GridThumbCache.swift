// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  GridThumbCache.swift
//  OffTheCloud
//
//  Grid tiles (Images, the post composer, the device photo picker) used to
//  build a UIImage from the device's 1000 px thumbnail bytes inside their
//  view body. A tile with closure properties is re-evaluated on every
//  change to its view model, so each pass handed SwiftUI fresh, undecoded
//  images and the main thread decoded ~5 MB bitmaps for 120 pt tiles again
//  and again while a page loaded or the grid scrolled. Here each is decoded
//  once, just big enough for its tile, off the main thread when possible,
//  and kept in a bounded cache (like the feed's MediaSizeCache).
//
//  Only grid tiles: the small thumbnails every grid asks for
//  (small_thumbnails, release 111 - an older device's big ones when it
//  ignores the flag). A big thumbnail asked for on purpose (the viewer's,
//  where the thumbnail stays on screen: PhotoGalleryVM.bigThumbs) is never
//  kept here: the answers carry no mark of which size came, so the two
//  must never share an entry - the keys say "small" (Android's
//  ThumbStore.tileKey).
//

import UIKit
import ImageIO

enum GridThumbCache {
    private static let images: NSCache<NSString, UIImage> = {
        let cache = NSCache<NSString, UIImage>()
        cache.countLimit = 400
        // Images' tiles are decoded for 144 pt (PhotoTile.decodeSide), not
        // 120: half as much again per tile, and so half as much again room,
        // so as many stay decoded when scrolling back.
        cache.totalCostLimit = 72 << 20
        return cache
    }()

    /// Pixels per point the tiles are decoded for: the densest iPhone
    /// screen, so they are never soft.
    private static let pixelScale: CGFloat = 3

    private static func key(_ id: String, _ maxPt: CGFloat) -> NSString {
        "\(id)#small@\(Int(maxPt))" as NSString
    }

    private static func cost(of image: UIImage) -> Int {
        Int(image.size.width * image.scale * image.size.height * image.scale) * 4
    }

    /// The tile image for `id`: cached, or decoded now and cached - from
    /// the file at `localURL` if there is one and it reads, else `data`.
    static func image(id: String, data: Data?, localURL: URL? = nil, maxPt: CGFloat) -> UIImage? {
        let k = key(id, maxPt)
        if let cached = images.object(forKey: k) { return cached }
        guard let img = decode(data: data, localURL: localURL, maxPt: maxPt) else { return nil }
        images.setObject(img, forKey: k, cost: cost(of: img))
        return img
    }

    /// Decodes a page's tiles off the main thread before they are shown,
    /// so the first paint finds them ready.
    static func prewarm(_ entries: [(id: String, data: Data?)], maxPt: CGFloat) async {
        let missing = entries.filter { $0.data != nil && images.object(forKey: key($0.id, maxPt)) == nil }
        guard !missing.isEmpty else { return }
        await Task.detached(priority: .userInitiated) {
            DispatchQueue.concurrentPerform(iterations: missing.count) { i in
                if let img = decode(data: missing[i].data, localURL: nil, maxPt: maxPt) {
                    images.setObject(img, forKey: key(missing[i].id, maxPt), cost: cost(of: img))
                }
            }
        }.value
    }

    /// An image that is already the right size (the composer's phone
    /// thumbnails come from PhotoKit at 450 px).
    static func store(_ image: UIImage, id: String) {
        images.setObject(image, forKey: id as NSString, cost: cost(of: image))
    }

    static func stored(_ id: String) -> UIImage? {
        images.object(forKey: id as NSString)
    }

    /// Decodes without caching, for a caller that keeps its own (the
    /// Files grid).
    static func decode(data: Data?, localURL: URL?, maxPt: CGFloat) -> UIImage? {
        if let localURL, let src = CGImageSourceCreateWithURL(localURL as CFURL, nil),
           let img = decode(src, maxPt: maxPt) {
            return img
        }
        guard let data, let src = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
        return decode(src, maxPt: maxPt)
    }

    /// Short side at least maxPt * pixelScale (scaledToFill crops the long
    /// one), never above the image's own size, with its orientation applied.
    private static func decode(_ src: CGImageSource, maxPt: CGFloat) -> UIImage? {
        var opts: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
        ]
        if let props = CGImageSourceCopyPropertiesAtIndex(src, 0, nil) as? [CFString: Any],
           let w = (props[kCGImagePropertyPixelWidth] as? NSNumber)?.doubleValue,
           let h = (props[kCGImagePropertyPixelHeight] as? NSNumber)?.doubleValue,
           w > 0, h > 0 {
            let target = Double(maxPt * pixelScale)
            let long = max(w, h), short = min(w, h)
            opts[kCGImageSourceThumbnailMaxPixelSize] = Int(min(long, (target * long / short).rounded(.up)))
        }
        guard let cg = CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary) else { return nil }
        return UIImage(cgImage: cg, scale: 1, orientation: .up)
    }
}
