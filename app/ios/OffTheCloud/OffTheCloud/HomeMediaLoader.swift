// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  HomeMediaLoader.swift
//  OffTheCloud
//
//  Issue #190: streaming a video from the device on the home network.
//  AVPlayer makes its own trust decisions, and the device's certificate
//  there is self-signed, so a /media/<token> URL on the home network gets
//  a scheme of our own (otc-home://<address>:<port>/media/<token>).
//  AVFoundation can't fetch that, so it asks this loader for each byte
//  range it needs; the loader fetches the range over the pinned session
//  and hands the bytes back as they arrive. Through the bridge the URL
//  stays plain https and AVPlayer fetches it itself, as before.

import AVFoundation
import Foundation
import UniformTypeIdentifiers

final class HomeMediaLoader: NSObject, AVAssetResourceLoaderDelegate, @unchecked Sendable {
    static let scheme = "otc-home"

    /// The pin each home-network base ("https://<address>:<port>") was
    /// handed out with, so a URL only ever reaches the device it was
    /// minted for.
    private static let pinsLock = NSLock()
    private static var pins: [String: Data] = [:]

    /// The URL for a device media path ("/media/<token>") on the home
    /// network.
    static func url(path: String, link: HomeLink) -> URL? {
        guard path.hasPrefix("/") else { return nil }
        let base = "https://" + LocalEndpoint.authority(link.address, port: link.endpoint.port)
        pinsLock.withLock { pins[base] = link.endpoint.pin }
        return URL(string: scheme + base.dropFirst("https".count) + path)
    }

    /// The https URL behind one of ours, nil for any other.
    static func httpsURL(_ url: URL) -> URL? {
        guard url.scheme == scheme else { return nil }
        return URL(string: "https" + url.absoluteString.dropFirst(scheme.count))
    }

    private static func pin(for https: URL) -> Data? {
        let s = https.absoluteString
        guard let pathStart = s.range(of: "/", range: s.index(s.startIndex, offsetBy: "https://".count)..<s.endIndex) else { return nil }
        return pinsLock.withLock { pins[String(s[..<pathStart.lowerBound])] }
    }

    /// The asset every player of a device media URL is built from: one of
    /// ours is loaded here, any other URL is a plain AVURLAsset, as before.
    static func asset(for url: URL) -> AVURLAsset {
        guard let https = httpsURL(url), let pin = pin(for: https) else { return AVURLAsset(url: url) }
        return HomeAsset(url: url, loader: HomeMediaLoader(session: PinnedSession.forPin(pin)))
    }

    private let pinned: PinnedSession
    /// The fetch behind each loading request. Only touched on
    /// pinned.queue: AVFoundation calls this delegate there, and the
    /// session's callbacks run there too.
    private var running: [AVAssetResourceLoadingRequest: URLSessionDataTask] = [:]

    private init(session: PinnedSession) {
        pinned = session
    }

    deinit {
        for task in running.values { task.cancel() }
    }

    func resourceLoader(_ resourceLoader: AVAssetResourceLoader,
                        shouldWaitForLoadingOfRequestedResource loadingRequest: AVAssetResourceLoadingRequest) -> Bool {
        guard let url = loadingRequest.request.url, let https = Self.httpsURL(url) else { return false }
        var req = URLRequest(url: https)
        let (offset, header) = Self.range(of: loadingRequest.dataRequest)
        req.setValue(header, forHTTPHeaderField: "Range")
        let task = pinned.session.dataTask(with: req)
        let fetch = RangeFetch(request: loadingRequest, offset: offset)
        pinned.track(task, PinnedSession.Handlers(
            response: { fetch.received($0) },
            data: { fetch.received($0) },
            completed: { [weak self] error in
                self?.running.removeValue(forKey: loadingRequest)
                fetch.completed(error)
            }
        ))
        running[loadingRequest] = task
        task.resume()
        return true
    }

    func resourceLoader(_ resourceLoader: AVAssetResourceLoader, didCancel loadingRequest: AVAssetResourceLoadingRequest) {
        running.removeValue(forKey: loadingRequest)?.cancel()
    }

    /// The first byte asked for and the Range header asking for it. A
    /// request for the content information alone gets the first two
    /// bytes, as AVFoundation itself asks for.
    static func range(of data: AVAssetResourceLoadingDataRequest?) -> (offset: Int64, header: String) {
        guard let data else { return (0, "bytes=0-1") }
        let start = data.requestedOffset
        if data.requestsAllDataToEndOfResource || data.requestedLength <= 0 {
            return (start, "bytes=\(start)-")
        }
        return (start, "bytes=\(start)-\(start + Int64(data.requestedLength) - 1)")
    }

    /// The whole length from a 206's Content-Range ("bytes 0-1/12345"),
    /// or a 200's own length.
    static func totalLength(of resp: HTTPURLResponse) -> Int64? {
        if resp.statusCode == 206 {
            guard let range = resp.value(forHTTPHeaderField: "Content-Range"),
                  let slash = range.lastIndex(of: "/") else { return nil }
            return Int64(range[range.index(after: slash)...])
        }
        return resp.expectedContentLength >= 0 ? resp.expectedContentLength : nil
    }
}

/// Keeps the loader alive as long as the asset: AVAssetResourceLoader
/// holds its delegate weakly.
private final class HomeAsset: AVURLAsset, @unchecked Sendable {
    private let loader: HomeMediaLoader

    init(url: URL, loader: HomeMediaLoader) {
        self.loader = loader
        super.init(url: url, options: nil)
        resourceLoader.setDelegate(loader, queue: loader.queue)
    }
}

extension HomeMediaLoader {
    fileprivate var queue: DispatchQueue { pinned.queue }
}

/// One loading request's fetch, answered as the bytes arrive rather than
/// once the range is complete: AVPlayer often asks for everything to the
/// end of the file and cancels once it has enough.
private final class RangeFetch: @unchecked Sendable {
    private let request: AVAssetResourceLoadingRequest
    /// Bytes still to drop before the ones asked for: a server that
    /// ignores Range answers 200 with the whole file.
    private var skip: Int64 = 0
    private let offset: Int64
    private var failed = false

    init(request: AVAssetResourceLoadingRequest, offset: Int64) {
        self.request = request
        self.offset = offset
    }

    func received(_ response: URLResponse) -> URLSession.ResponseDisposition {
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 || http.statusCode == 206 else {
            failed = true
            let code = (response as? HTTPURLResponse)?.statusCode ?? 0
            if !request.isCancelled, !request.isFinished {
                request.finishLoading(with: NSError(domain: "HomeMediaLoader", code: code,
                                                    userInfo: [NSLocalizedDescriptionKey: "The device answered \(code)"]))
            }
            return .cancel
        }
        if http.statusCode == 200 { skip = offset }
        if let info = request.contentInformationRequest {
            info.contentType = http.mimeType.flatMap { UTType(mimeType: $0)?.identifier }
            if let total = HomeMediaLoader.totalLength(of: http) { info.contentLength = total }
            info.isByteRangeAccessSupported = http.statusCode == 206
                || http.value(forHTTPHeaderField: "Accept-Ranges")?.lowercased() == "bytes"
        }
        return .allow
    }

    func received(_ data: Data) {
        guard !request.isCancelled, !request.isFinished else { return }
        var data = data
        if skip > 0 {
            let drop = Int(min(skip, Int64(data.count)))
            data = data.dropFirst(drop)
            skip -= Int64(drop)
            if data.isEmpty { return }
        }
        request.dataRequest?.respond(with: data)
    }

    func completed(_ error: Error?) {
        guard !failed, !request.isCancelled, !request.isFinished else { return }
        if let error {
            request.finishLoading(with: error)
        } else {
            request.finishLoading()
        }
    }
}
