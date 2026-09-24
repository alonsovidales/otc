// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  MediaStream.swift
//  OffTheCloud
//
//  Issue #110: getting a URL AVPlayer can stream from, instead of
//  downloading a whole video into a temp file before it can start.
//
//  The win is larger here than on the web: today a video is pulled down
//  in one piece over the socket and written to disk before the first
//  frame appears, so a long clip is a long wait and a big file on a
//  phone. AVPlayer given a URL does ordinary HTTP range requests - it
//  starts on the first chunk, fetches only what's played, and writes
//  nothing to disk.
//
//  The device decides whether streaming is worth it: below its own size
//  threshold it answers with no URL, because a small clip already arrives
//  in a single round trip and streaming would add one (this call) before
//  any bytes moved. A nil result is therefore a normal answer meaning
//  "fetch it the old way", not a failure.

import Foundation

enum MediaStream {
    /// Asks the device for a streamable URL for a library file.
    static func url(forPath path: String) async -> URL? {
        await url { req in
            var m = Msg_ReqGetMediaURL()
            m.path = path
            req.payload = .reqGetMediaURL(m)
        }
    }

    /// Asks the device for a streamable URL for a publication's media,
    /// addressed by hash the way every other feed fetch is.
    static func url(forPublication pubUuid: String, hash: String) async -> URL? {
        await url { req in
            var m = Msg_ReqGetMediaURL()
            m.pubUuid = pubUuid
            m.hash = hash
            req.payload = .reqGetMediaURL(m)
        }
    }

    private static func url(_ build: @escaping (inout Msg_ReqEnvelope) -> Void) async -> URL? {
        do {
            let resp = try await OTCConnection.shared.request { e in build(&e) }
            guard case .respMediaURL(let m) = resp.payload, !m.url.isEmpty else { return nil }
            return await absolute(m.url)
        } catch {
            // Every caller has the whole-file fetch to fall back on, and
            // falling back quietly beats failing to show a video.
            return nil
        }
    }

    /// The device answers with a path ("/media/<token>"), not a full URL -
    /// it has no idea whether this app reached it directly on the LAN or
    /// through the bridge. Resolving it against the endpoint this app is
    /// already connected to is what makes it work in both.
    ///
    /// Read off the main actor and then remembered. loadOrCreate() does
    /// three synchronous Keychain reads, and a Task started inside a
    /// SwiftUI view inherits the main actor - so this ran on the main
    /// thread, and the keychain is slow the first time a process wakes it
    /// up. That put a stall in front of the *first* video played and none
    /// in front of any later one.
    private static var cachedBase: URLComponents?

    /// Log Out: the next device has another address.
    static func reset() { cachedBase = nil }

    static func absolute(_ path: String) async -> URL? {
        var components: URLComponents
        if let cachedBase {
            components = cachedBase
        } else {
            let endpoint = await Task.detached(priority: .userInitiated) {
                SecretsStore.loadOrCreate().endpointURLString // normalized: scheme + /ws filled in
            }.value
            guard var resolved = URLComponents(string: endpoint) else { return nil }
            resolved.scheme = resolved.scheme == "ws" ? "http" : "https"
            resolved.query = nil
            resolved.fragment = nil
            cachedBase = resolved
            components = resolved
        }
        components.path = path

        return components.url
    }
}
