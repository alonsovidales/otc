// SPDX-License-Identifier: AGPL-3.0-or-later

import AVFoundation
import Foundation
import Testing
@testable import OffTheCloud

/// Issue #190: videos from the device on the home network, through
/// HomeMediaLoader over the pinned session (servers and certificates from
/// HomeNetworkTests).
@Suite(.serialized)
struct HomeMediaLoaderTests {
    /// One second of 16x16 H.264 (ffmpeg, faststart).
    static let tinyMP4 = Data(base64Encoded: """
AAAAIGZ0eXBpc29tAAACAGlzb21pc28yYXZjMW1wNDEAAAN0bW9vdgAAAGxtdmhkAAAAAAAAAAAAAAAAAAAD6AAAA+gAAQAAAQAA
AAAAAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAABAAAAAAAAAAAAAAAAAABAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAgAA
Ap90cmFrAAAAXHRraGQAAAADAAAAAAAAAAAAAAABAAAAAAAAA+gAAAAAAAAAAAAAAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAABAAAA
AAAAAAAAAAAAAABAAAAAABAAAAAQAAAAAAAkZWR0cwAAABxlbHN0AAAAAAAAAAEAAAPoAAAQAAABAAAAAAIXbWRpYQAAACBtZGhk
AAAAAAAAAAAAAAAAAAAoAAAAKABVxAAAAAAALWhkbHIAAAAAAAAAAHZpZGUAAAAAAAAAAAAAAABWaWRlb0hhbmRsZXIAAAABwm1p
bmYAAAAUdm1oZAAAAAEAAAAAAAAAAAAAACRkaW5mAAAAHGRyZWYAAAAAAAAAAQAAAAx1cmwgAAAAAQAAAYJzdGJsAAAAvnN0c2QA
AAAAAAAAAQAAAK5hdmMxAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAAAABAAEABIAAAASAAAAAAAAAABFExhdmM2My4xLjEwMiBsaWJ4
MjY0AAAAAAAAAAAAAAAAGP//AAAANGF2Y0MBZAAK/+EAF2dkAAqs2V7ARAAAAwAEAAADACg8SJZYAQAGaOvjyyLA/fj4AAAAABBw
YXNwAAAAAQAAAAEAAAAUYnRydAAAAAAAABfQAAAAAAAAABhzdHRzAAAAAAAAAAEAAAAFAAAIAAAAABRzdHNzAAAAAAAAAAEAAAAB
AAAAOGN0dHMAAAAAAAAABQAAAAEAABAAAAAAAQAAKAAAAAABAAAQAAAAAAEAAAAAAAAAAQAACAAAAAAcc3RzYwAAAAAAAAABAAAA
AQAAAAUAAAABAAAAKHN0c3oAAAAAAAAAAAAAAAUAAALKAAAADAAAAAwAAAAMAAAADAAAABRzdGNvAAAAAAAAAAEAAAOkAAAAYXVk
dGEAAABZbWV0YQAAAAAAAAAhaGRscgAAAAAAAAAAbWRpcmFwcGwAAAAAAAAAAAAAAAAsaWxzdAAAACSpdG9vAAAAHGRhdGEAAAAB
AAAAAExhdmY2My4xLjEwMgAAAAhmcmVlAAADAm1kYXQAAAKtBgX//6ncRem95tlIt5Ys2CDZI+7veDI2NCAtIGNvcmUgMTY1IHIz
MjIyIGIzNTYwNWEgLSBILjI2NC9NUEVHLTQgQVZDIGNvZGVjIC0gQ29weWxlZnQgMjAwMy0yMDI1IC0gaHR0cDovL3d3dy52aWRl
b2xhbi5vcmcveDI2NC5odG1sIC0gb3B0aW9uczogY2FiYWM9MSByZWY9MyBkZWJsb2NrPTE6MDowIGFuYWx5c2U9MHgzOjB4MTEz
IG1lPWhleCBzdWJtZT03IHBzeT0xIHBzeV9yZD0xLjAwOjAuMDAgbWl4ZWRfcmVmPTEgbWVfcmFuZ2U9MTYgY2hyb21hX21lPTEg
dHJlbGxpcz0xIDh4OGRjdD0xIGNxbT0wIGRlYWR6b25lPTIxLDExIGZhc3RfcHNraXA9MSBjaHJvbWFfcXBfb2Zmc2V0PS0yIHRo
cmVhZHM9MSBsb29rYWhlYWRfdGhyZWFkcz0xIHNsaWNlZF90aHJlYWRzPTAgbnI9MCBkZWNpbWF0ZT0xIGludGVybGFjZWQ9MCBi
bHVyYXlfY29tcGF0PTAgY29uc3RyYWluZWRfaW50cmE9MCBiZnJhbWVzPTMgYl9weXJhbWlkPTIgYl9hZGFwdD0xIGJfYmlhcz0w
IGRpcmVjdD0xIHdlaWdodGI9MSBvcGVuX2dvcD0wIHdlaWdodHA9MiBrZXlpbnQ9MjUwIGtleWludF9taW49NSBzY2VuZWN1dD00
MCBpbnRyYV9yZWZyZXNoPTAgcmNfbG9va2FoZWFkPTQwIHJjPWNyZiBtYnRyZWU9MSBjcmY9MjMuMCBxY29tcD0wLjYwIHFwbWlu
PTAgcXBtYXg9NjkgcXBzdGVwPTQgaXBfcmF0aW89MS40MCBhcT0xOjEuMDAAgAAAABVliIQAEv/+6Mn8yysv49Q3s0Yps00AAAAI
QZokbEP//uAAAAAIQZ5CeIIfq4EAAAAIAZ5hdEP/s4AAAAAIAZ5jakP/s4E=
""", options: .ignoreUnknownCharacters)!

    @Test func homeMediaURLsGoToTheSameAddressOverHTTPS() throws {
        let link = HomeLink(endpoint: LocalEndpoint(endpoint: "e", addresses: ["fd00::5"], port: 8443, pin: HomeNetworkTests.goodPin), address: "fd00::5")
        let url = try #require(HomeMediaLoader.url(path: "/media/tok", link: link))
        #expect(url.absoluteString == "otc-home://[fd00::5]:8443/media/tok")
        #expect(HomeMediaLoader.httpsURL(url)?.absoluteString == "https://[fd00::5]:8443/media/tok")
        #expect(HomeMediaLoader.httpsURL(URL(string: "https://cala.off-the.cloud/media/tok")!) == nil)
        #expect(HomeMediaLoader.url(path: "media/tok", link: link) == nil)
    }

    @Test func aBridgeURLStaysAPlainAsset() {
        let asset = MediaStream.asset(for: URL(string: "https://cala.off-the.cloud/media/tok")!)
        #expect(asset.url.scheme == "https")
        #expect(asset.resourceLoader.delegate == nil)
    }

    @Test func theLengthComesFromContentRange() throws {
        let url = URL(string: "https://10.0.0.2:8443/media/tok")!
        let partial = try #require(HTTPURLResponse(url: url, statusCode: 206, httpVersion: "HTTP/1.1", headerFields: ["Content-Range": "bytes 0-1/1694"]))
        #expect(HomeMediaLoader.totalLength(of: partial) == 1694)
        let whole = try #require(HTTPURLResponse(url: url, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Length": "1694"]))
        #expect(HomeMediaLoader.totalLength(of: whole) == 1694)
        #expect(HomeMediaLoader.range(of: nil).header == "bytes=0-1")
    }

    @Test func aHomeVideoPlaysThroughThePinnedLoader() async throws {
        let server = try await TestServer.start(HomeNetworkTests.goodP12, mode: .http(Self.tinyMP4, mime: "video/mp4"))
        defer { server.stop() }
        let link = HomeLink(endpoint: LocalEndpoint(endpoint: "e", addresses: ["127.0.0.1"], port: server.port, pin: HomeNetworkTests.goodPin), address: "127.0.0.1")
        let url = try #require(HomeMediaLoader.url(path: "/media/tok", link: link))
        let asset = MediaStream.asset(for: url)
        let (playable, duration) = try await asset.load(.isPlayable, .duration)
        #expect(playable)
        #expect(duration.seconds > 0.5)
        let requests = server.requests
        #expect(!requests.isEmpty)
        #expect(requests.allSatisfy { $0.path == "/media/tok" && $0.range?.hasPrefix("bytes=") == true })
    }

    @Test func aHomeVideoFromAnImpostorGetsNothing() async throws {
        let server = try await TestServer.start(HomeNetworkTests.badP12, mode: .raw)
        defer { server.stop() }
        // The address the device's pin was handed out for, now answered
        // by someone else.
        let link = HomeLink(endpoint: LocalEndpoint(endpoint: "e", addresses: ["127.0.0.1"], port: server.port, pin: HomeNetworkTests.goodPin), address: "127.0.0.1")
        let url = try #require(HomeMediaLoader.url(path: "/media/tok", link: link))
        let asset = MediaStream.asset(for: url)
        let playable = (try? await asset.load(.isPlayable)) ?? false
        #expect(!playable)
        try await Task.sleep(for: .milliseconds(300))
        #expect(server.receivedBytes == 0)
    }
}
