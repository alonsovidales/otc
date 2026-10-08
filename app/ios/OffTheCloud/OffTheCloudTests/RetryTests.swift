// SPDX-License-Identifier: AGPL-3.0-or-later

import CryptoKit
import Foundation
import Network
import SwiftProtobuf
import Testing
import UIKit
@testable import OffTheCloud

/// Waits (`seconds` at most) for `done`.
@MainActor
private func until(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

// MARK: - The backoff (PageRetry), as the web's usePageRetry

@MainActor
struct PageRetryTests {
    @Test func waitsOneSecondDoublingToTen() {
        #expect((1...7).map { PageRetry.delay(afterFailures: $0) } == [1, 2, 4, 8, 10, 10, 10])
        #expect(PageRetry.delay(afterFailures: 0) == 0)
        #expect(PageRetry.delay(afterFailures: 1000) == 10)
    }

    @Test func asksAgainOnceTheWaitIsOver() async {
        let r = PageRetry()
        var waits: [Int] = []
        r.delay = { n in waits.append(n); return 0.02 }
        var runs = 0
        r.failed { runs += 1 }
        #expect(r.waiting)
        await until { runs == 1 }
        #expect(runs == 1)
        #expect(!r.waiting)
        // A second failure in a row waits the second delay.
        r.failed { runs += 1 }
        await until { runs == 2 }
        #expect(waits == [1, 2])
        #expect(r.failures == 2)
    }

    @Test func aLoadThatWorkedOrANewSearchCancelsTheWait() async {
        let r = PageRetry()
        r.delay = { _ in 0.05 }
        var runs = 0
        r.failed { runs += 1 }
        r.reset()
        try? await Task.sleep(nanoseconds: 150_000_000)
        #expect(runs == 0)
        #expect(r.failures == 0)
        #expect(!r.waiting)
    }

    @Test func wakeAsksAtOnceAndStartsTheWaitOver() async {
        let r = PageRetry()
        r.delay = { _ in 100 }
        var runs = 0
        r.failed { runs += 1 }
        r.failed { runs += 1 }
        #expect(r.failures == 2)
        #expect(r.wake())
        #expect(runs == 1)
        #expect(r.failures == 0)
        #expect(!r.waiting)
        // Nothing waiting (one on its way, or nothing failed): nothing to do.
        #expect(!r.wake())
        #expect(runs == 1)
    }

    @Test func keepsAskingUntilItComesOneLoopAtATime() async {
        let r = PageRetry()
        var waits: [Int] = []
        r.delay = { n in waits.append(n); return 0.02 }
        var asks = 0
        let load: @MainActor () async -> Bool = { asks += 1; return asks >= 3 }
        r.keepAsking(load)
        // A second ask while the first loop goes is left to it.
        r.keepAsking(load)
        #expect(r.looping)
        await until { !r.looping }
        #expect(asks == 3)
        #expect(waits == [1, 2, 3])
        #expect(r.failures == 0)
        #expect(!r.waiting)
    }

    @Test func keepAskingStopsWhenNoLongerWantedOrReset() async {
        let r = PageRetry()
        r.delay = { _ in 0.02 }
        var asks = 0
        var wanted = true
        r.keepAsking(wanted: { wanted }) { asks += 1; return false }
        await until { asks == 2 }
        wanted = false
        await until { !r.looping }
        #expect(!r.looping)
        let after = asks
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(asks == after)

        // reset() ends a loop too (a new filter's load, say).
        var other = 0
        r.keepAsking { other += 1; return false }
        r.reset()
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(other == 0)
        #expect(!r.looping)
    }

    @Test func wakeCutsAListsWaitShort() async {
        let r = PageRetry()
        r.delay = { _ in 100 }
        var asks = 0
        r.keepAsking { asks += 1; return true }
        #expect(r.waiting)
        r.wake()
        await until { !r.looping }
        #expect(asks == 1)
    }
}

// MARK: - Why a load failed, in Android's words

struct LoadProblemTests {
    private func ack(_ code: String) -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        var a = Msg_Ack()
        a.code = code
        a.errorMsg = "The device is not reachable right now."
        r.payload = .respAck(a)
        return r
    }

    @Test func whyAsAndroidTellsIt() {
        // No network at all: whatever failed, that is why.
        #expect(LoadProblem.of(resp: nil, error: WSClient.TimedOut(), offline: true, statusCode: nil) == .offline)
        #expect(LoadProblem.of(resp: ack("device_unreachable"), error: nil, offline: true, statusCode: nil) == .offline)
        // The bridge answering for a device it can't reach.
        #expect(LoadProblem.of(resp: ack("device_unreachable"), error: nil, offline: false, statusCode: nil) == .unreachable)
        // ...or saying so at sign-in (OTCConnection threw for it).
        #expect(LoadProblem.of(resp: nil, error: URLError(.cancelled), offline: false, statusCode: "device_unreachable") == .unreachable)
        // No answer in time.
        #expect(LoadProblem.of(resp: nil, error: WSClient.TimedOut(), offline: false, statusCode: nil) == .slow)
        // Anything else: a dropped connection, an error answer.
        #expect(LoadProblem.of(resp: nil, error: WSClient.Unresponsive(), offline: false, statusCode: nil) == .failed)
        #expect(LoadProblem.of(resp: ack("account_disabled"), error: nil, offline: false, statusCode: nil) == .failed)
        var refused = Msg_RespEnvelope()
        refused.error = true
        refused.errorMessage = "internal error"
        #expect(LoadProblem.of(resp: refused, error: nil, offline: false, statusCode: nil) == .failed)
        // A sign-in that failed otherwise is no verdict on the device.
        #expect(LoadProblem.of(resp: nil, error: URLError(.cancelled), offline: false, statusCode: "account_disabled") == .failed)
    }

    @Test func androidsWording() {
        #expect(LoadProblem.photosTitle == "Couldn't load your photos")
        #expect(LoadProblem.offline.photosText == "This phone is offline. Your photos will appear once it's back online.")
        #expect(LoadProblem.unreachable.photosText == "Your device isn't reachable right now. Your photos will appear as soon as it answers.")
        #expect(LoadProblem.slow.photosText == "Your device took too long to answer. Your photos will appear as soon as it does.")
        #expect(LoadProblem.failed.photosText == "Your photos will appear as soon as your device answers.")
        #expect(LoadProblem.slow.text("The posts") == "Your device took too long to answer. The posts will appear as soon as it does.")
        #expect(LoadProblem.offline.text("The files") == "This phone is offline. The files will appear once it's back online.")
        #expect(LoadProblem.morePhotos == "Couldn't load more photos.")
        #expect(LoadProblem.morePosts == "Couldn't load more posts.")
        #expect(LoadProblem.photosSlow == "Still waiting for your device…")
        #expect(OTCConnection.offlineMessage == "This phone is offline. Connect to Wi-Fi or mobile data.")
        // Android's theWordingIOSCopies.
        #expect(LoadProblem.tryAgain == "Try again")
        #expect(LoadProblem.trying == "Trying…")
        #expect(LoadProblem.photosLoading == "Loading photos")
        #expect(LoadProblem.videoUnplayable == "Couldn't play this video.")
        #expect(LoadProblem.failed.text("The files") == "The files will appear as soon as your device answers.")
        // Every text differs: the reason is what tells them apart.
        #expect(Set([LoadProblem.offline, .unreachable, .slow, .failed].map(\.photosText)).count == 4)
    }

    @Test func backOnlineOfflineGivesWayToTheNeutralLine() {
        #expect(LoadProblem.offline.backOnline(true) == .failed)
        #expect(LoadProblem.offline.backOnline(false) == .offline)
        // Any other reason still says why.
        for p in [LoadProblem.unreachable, .slow, .failed] { #expect(p.backOnline(true) == p) }
    }
}

// MARK: - Request timeouts and a socket that stops answering

struct RequestTimeoutTests {
    @Test func whatAScreenShowsWaitsAsAndroidsDoes() {
        #expect(OTCConnection.pageTimeout == 120)
        #expect(OTCConnection.listTimeout == 60)
        #expect(PhotoGalleryVM.AskKind.page.base == 120)
        #expect(PhotoGalleryVM.AskKind.people.base == 120)
        #expect(PhotoGalleryVM.AskKind.groups.base == 120)
        #expect(PhotoGalleryVM.AskKind.tags.base == 60)
        #expect(PhotoGalleryVM.AskKind.buckets.base == 60)
        #expect(PhotoGalleryVM.AskKind.thumbnail.base == 60)
        // Android's kinds, which Patience keys by.
        #expect([PhotoGalleryVM.AskKind.page, .tags, .buckets, .people, .groups, .thumbnail].map(\.key)
                == ["page", "tags", "buckets", "people", "groups", "thumbnail"])
        // Connecting and everything else (Android's CONNECT_TIMEOUT_MS,
        // DEFAULT_TIMEOUT_MS, SIGN_IN_TIMEOUT_MS).
        #expect(WSClient.connectTimeout == 20)
        #expect(WSClient.defaultTimeout == 30 * 60)
    }

    @MainActor
    @Test func signingInHasItsLimit() {
        #expect(OTCConnection.shared.signInTimeout == 120)
    }

    @Test func androidsPongWait() {
        // OkHttp's ping: a pong may take up to 30 s.
        #expect(SocketLiveness.Timing().pongWait == 30)
    }
}

// MARK: - Patience: how long a screen's request waits (Android's PatienceTest)

struct PatienceTests {
    private let page = OTCConnection.pageTimeout

    @Test func moreTimeAfterEachTimeoutUpToFiveMinutes() {
        #expect((0...3).map { Patience.timeoutAfter(base: page, timedOut: $0) } == [120, 240, 300, 300])
        #expect(Patience.timeoutAfter(base: page, timedOut: 50) == 300)
        #expect((0...3).map { Patience.timeoutAfter(base: OTCConnection.listTimeout, timedOut: $0) } == [60, 120, 240, 300])
        #expect(Patience.timeoutAfter(base: 60, timedOut: -1) == 60)
        // A longer base than the cap keeps its own.
        #expect(Patience.timeoutAfter(base: WSClient.defaultTimeout, timedOut: 3) == WSClient.defaultTimeout)
    }

    @Test func anAnswerWithinTheUsualTimeBringsItBack() {
        let p = Patience()
        p.timedOut("page/bridge")
        p.timedOut("page/bridge")
        #expect(p.timeout(for: "page/bridge", base: page) == 300)
        // Pit's pages: 60-90 s. One answer is enough to be back at 2
        // minutes, not "under half of it" as before (60 s), which a slow
        // device never got to, leaving every page 4-5 minutes for good.
        p.answered("page/bridge", base: page, took: 85)
        #expect(p.timeout(for: "page/bridge", base: page) == 120)
    }

    @Test func aSlowerAnswerStepsDownOnlyAsFarAsItWouldHaveFit() {
        let p = Patience()
        for _ in 0..<3 { p.timedOut("page/home") }
        // 150 s needed the doubled time: 4 minutes, not 5, not 2.
        p.answered("page/home", base: page, took: 150)
        #expect(p.timeout(for: "page/home", base: page) == 240)
        // Never up on an answer.
        p.answered("page/home", base: page, took: 290)
        #expect(p.timeout(for: "page/home", base: page) == 240)
        p.answered("page/home", base: page, took: 3)
        #expect(p.timeout(for: "page/home", base: page) == 120)
    }

    @Test func eachKindAndRouteKeepsItsOwn() {
        let p = Patience()
        p.timedOut("page/bridge")
        #expect(p.timeout(for: "page/bridge", base: page) == 240)
        #expect(p.timeout(for: "page/home", base: page) == 120)
        #expect(p.timeout(for: "feed/bridge", base: page) == 120)
    }
}

// MARK: - The socket's check: never while an answer may be on its way

struct SocketLivenessTests {
    private let t0 = Date(timeIntervalSince1970: 1_000_000)
    private func at(_ s: TimeInterval) -> Date { t0 + s }
    private var timing: SocketLiveness.Timing { .init(pongWait: 15) }

    @Test func aSocketThatHasntOpenedIsLeftToConnect() {
        // Its upgrade has its own limit (WSClient.connectTimeout).
        var l = SocketLiveness(timing: timing)
        l.check()
        #expect(l.tick(at: at(600), waiting: 0) == .none)
    }

    @Test func checkedOnDemandWithNothingWaiting() {
        // The app back in front, a network change, a request that timed out.
        var l = SocketLiveness(timing: timing, heard: at(0))
        l.check()
        #expect(l.checking)
        #expect(l.tick(at: at(500), waiting: 0) == .ping)
        #expect(l.tick(at: at(514), waiting: 0) == .none)
        #expect(l.tick(at: at(515), waiting: 0) == .dead)

        var alive = SocketLiveness(timing: timing, heard: at(0))
        alive.check()
        #expect(alive.tick(at: at(500), waiting: 0) == .ping)
        alive.heard(at: at(500.2))
        #expect(alive.tick(at: at(520), waiting: 0) == .none)
        #expect(!alive.checking)
    }

    @Test func neverWhileAnAnswerMayBeOnItsWay() {
        // A 4 MiB answer coming in at 32 KB/s: two minutes in which no
        // message completes and the bridge drops every pong. Nothing is
        // decided, however long, while it is waited for.
        var l = SocketLiveness(timing: timing, heard: at(0))
        #expect(l.tick(at: at(15), waiting: 1) == .none)
        #expect(l.tick(at: at(45), waiting: 1) == .none)
        #expect(l.tick(at: at(3600), waiting: 1) == .none)
        #expect(!l.checking)
        // A check asked for meanwhile (the network changed) waits for
        // nothing to be waiting.
        l.check()
        #expect(l.tick(at: at(3601), waiting: 1) == .none)
        #expect(l.checking)
        #expect(l.tick(at: at(3602), waiting: 0) == .ping)
        // A request sent after the ping doesn't hold the check up: its
        // answer comes after the pong.
        #expect(l.tick(at: at(3617), waiting: 1) == .dead)
    }

    @Test func anythingHeardAfterThePingKeepsIt() {
        // A late answer to the request that timed out counts as much as
        // the pong.
        var l = SocketLiveness(timing: timing, heard: at(0))
        l.check()
        #expect(l.tick(at: at(10), waiting: 0) == .ping)
        l.heard(at: at(20))
        #expect(l.tick(at: at(40), waiting: 0) == .none)
        #expect(!l.checking)
        // What was heard before the ping doesn't.
        l.check()
        #expect(l.tick(at: at(50), waiting: 0) == .ping)
        l.heard(at: at(49))
        #expect(l.tick(at: at(65), waiting: 0) == .dead)
    }
}

/// A WebSocket server on this machine's loopback, answering as a device
/// would - or late, or not at all.
private final class DeviceStandIn: @unchecked Sendable {
    /// What to do with a request: answer after `delay` seconds, or never (nil).
    typealias Plan = @Sendable (Msg_ReqEnvelope) -> TimeInterval?

    private let listener: NWListener
    private let queue = DispatchQueue(label: "test-ws-server")
    private let plan: Plan
    private let lock = NSLock()
    private var _received: [Int32] = []
    var received: [Int32] { lock.withLock { _received } }

    init(answersPings: Bool, plan: @escaping Plan) throws {
        let ws = NWProtocolWebSocket.Options()
        ws.autoReplyPing = answersPings
        let params = NWParameters.tcp
        params.defaultProtocolStack.applicationProtocols.insert(ws, at: 0)
        params.requiredLocalEndpoint = NWEndpoint.hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: params)
        self.plan = plan
        listener.newConnectionHandler = { [weak self] conn in
            guard let self else { return }
            conn.start(queue: self.queue)
            self.receive(on: conn)
        }
    }

    func start() async throws -> URL {
        let port: UInt16 = try await withCheckedThrowingContinuation { cont in
            let once = OnceFlag()
            listener.stateUpdateHandler = { [listener] state in
                switch state {
                case .ready:
                    if once.first() { cont.resume(returning: listener.port?.rawValue ?? 0) }
                case .failed(let e):
                    if once.first() { cont.resume(throwing: e) }
                default:
                    break
                }
            }
            listener.start(queue: queue)
        }
        return URL(string: "ws://127.0.0.1:\(port)/ws")!
    }

    func stop() { listener.cancel() }

    private func receive(on conn: NWConnection) {
        conn.receiveMessage { [weak self] data, context, _, error in
            guard let self else { return }
            let meta = context?.protocolMetadata(definition: NWProtocolWebSocket.definition) as? NWProtocolWebSocket.Metadata
            if let data, meta?.opcode == .binary, let env = try? Msg_ReqEnvelope(serializedBytes: data) {
                self.lock.withLock { self._received.append(env.id) }
                if let delay = self.plan(env) {
                    self.queue.asyncAfter(deadline: .now() + delay) { self.answer(env, on: conn) }
                }
            }
            if error == nil { self.receive(on: conn) }
        }
    }

    /// Answers with the request's own id, and the id again as a tag, so a
    /// test can tell which request an answer was for.
    private func answer(_ req: Msg_ReqEnvelope, on conn: NWConnection) {
        var resp = Msg_RespEnvelope()
        resp.id = req.id
        var tags = Msg_TagsList()
        tags.tags = ["\(req.id)"]
        resp.payload = .respTagsList(tags)
        guard let data = try? resp.serializedData() else { return }
        let meta = NWProtocolWebSocket.Metadata(opcode: .binary)
        let ctx = NWConnection.ContentContext(identifier: "answer", metadata: [meta])
        conn.send(content: data, contentContext: ctx, isComplete: true, completion: .contentProcessed { _ in })
    }
}

/// A device on plain BSD sockets, for what Network.framework's own server
/// can't do: read nothing for a while. It reads ahead into memory whatever
/// its receives ask for; here the kernel's buffers fill instead, and the
/// client's sends stop completing - what a slow uplink does. Answers every
/// request at once (its id again as a tag), after `readPause` without
/// reading anything at all.
private final class RawDevice: @unchecked Sendable {
    private let listener: Int32
    private let lock = NSLock()
    private var conn: Int32 = -1
    private let readPause: TimeInterval
    let url: URL

    init(readPause: TimeInterval) throws {
        self.readPause = readPause
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        listener = fd
        var one: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, socklen_t(MemoryLayout<Int32>.size))
        // Small buffers on the accepted socket: little held for the client.
        var small: Int32 = 64 << 10
        setsockopt(fd, SOL_SOCKET, SO_RCVBUF, &small, socklen_t(MemoryLayout<Int32>.size))
        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        addr.sin_port = 0
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        let bound = withUnsafeMutablePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { p -> Bool in
                bind(fd, p, len) == 0 && listen(fd, 1) == 0 && getsockname(fd, p, &len) == 0
            }
        }
        guard bound else { throw URLError(.cannotConnectToHost) }
        url = URL(string: "ws://127.0.0.1:\(UInt16(bigEndian: addr.sin_port))/ws")!
        Thread { [self] in serve() }.start()
    }

    func stop() {
        shutdown(listener, SHUT_RDWR)
        close(listener)
        lock.withLock {
            if conn >= 0 { shutdown(conn, SHUT_RDWR) }
        }
    }

    private func serve() {
        let c = accept(listener, nil, nil)
        guard c >= 0 else { return }
        // A client gone mid-answer: an error, not a SIGPIPE taking the
        // test process down.
        var one: Int32 = 1
        setsockopt(c, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
        lock.withLock { conn = c }
        defer { close(c) }
        // The upgrade.
        var head = Data()
        while !head.contains(Data("\r\n\r\n".utf8)) {
            guard let b = read(c, 1) else { return }
            head.append(b)
        }
        let text = String(decoding: head, as: UTF8.self)
        guard let keyLine = text.split(separator: "\r\n").first(where: { $0.lowercased().hasPrefix("sec-websocket-key:") }) else { return }
        let key = keyLine.dropFirst("sec-websocket-key:".count).trimmingCharacters(in: .whitespaces)
        let accepted = Data(Insecure.SHA1.hash(data: Data((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").utf8))).base64EncodedString()
        write(c, Data("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: \(accepted)\r\n\r\n".utf8))
        // A busy link: nothing read at all for a while.
        Thread.sleep(forTimeInterval: readPause)
        var message = Data()
        while true {
            guard let h = read(c, 2) else { return }
            let fin = h[0] & 0x80 != 0, op = h[0] & 0x0f
            var n = Int(h[1] & 0x7f)
            if n == 126 { guard let e = read(c, 2) else { return }; n = Int(e[0]) << 8 | Int(e[1]) }
            if n == 127 { guard let e = read(c, 8) else { return }; n = e.reduce(0) { $0 << 8 | Int($1) } }
            guard h[1] & 0x80 != 0, let mask = read(c, 4), var payload = read(c, n) else { return }
            unmask(&payload, mask)
            switch op {
            case 0x9:
                frame(c, op: 0xA, payload)
            case 0x8:
                return
            case 0x0, 0x2:
                message.append(payload)
                guard fin else { continue }
                if let env = try? Msg_ReqEnvelope(serializedBytes: message) {
                    var resp = Msg_RespEnvelope()
                    resp.id = env.id
                    var tags = Msg_TagsList()
                    tags.tags = ["\(env.id)"]
                    resp.payload = .respTagsList(tags)
                    if let d = try? resp.serializedData() { frame(c, op: 0x2, d) }
                }
                message = Data()
            default:
                continue
            }
        }
    }

    /// Eight bytes at a time: 16 MiB a byte at a time is seconds in a
    /// debug build, which would be the stand-in's slowness, not the link's.
    private func unmask(_ payload: inout Data, _ mask: Data) {
        let m4 = Array(mask)
        var m8: UInt64 = 0
        withUnsafeMutableBytes(of: &m8) { b in for i in 0..<8 { b[i] = m4[i % 4] } }
        payload.withUnsafeMutableBytes { raw in
            let n = raw.count
            let words = n / 8
            if words > 0 {
                let w = raw.baseAddress!.assumingMemoryBound(to: UInt64.self)
                for i in 0..<words { w[i] ^= m8 }
            }
            for i in (words * 8)..<n { raw[i] ^= m4[i % 4] }
        }
    }

    private func read(_ c: Int32, _ n: Int) -> Data? {
        var out = Data(count: n)
        var got = 0
        while got < n {
            let r = out.withUnsafeMutableBytes { recv(c, $0.baseAddress! + got, n - got, 0) }
            if r <= 0 { return nil }
            got += r
        }
        return out
    }

    private func write(_ c: Int32, _ d: Data) {
        var sent = 0
        while sent < d.count {
            let r = d.withUnsafeBytes { send(c, $0.baseAddress! + sent, d.count - sent, 0) }
            if r <= 0 { return }
            sent += r
        }
    }

    private func frame(_ c: Int32, op: UInt8, _ payload: Data) {
        var f = Data([0x80 | op])
        if payload.count < 126 {
            f.append(UInt8(payload.count))
        } else if payload.count < 65536 {
            f.append(126)
            f.append(contentsOf: [UInt8(payload.count >> 8), UInt8(payload.count & 0xff)])
        } else {
            f.append(127)
            f.append(contentsOf: (0..<8).reversed().map { UInt8((payload.count >> ($0 * 8)) & 0xff) })
        }
        f.append(payload)
        write(c, f)
    }
}

/// Takes TCP connections and never says a word: a WebSocket upgrade that
/// is never answered.
private final class SilentTCP: @unchecked Sendable {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "test-silent-tcp")
    private var conns: [NWConnection] = []

    init() throws {
        let params = NWParameters.tcp
        params.requiredLocalEndpoint = NWEndpoint.hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: params)
        listener.newConnectionHandler = { [weak self] conn in
            guard let self else { return }
            self.conns.append(conn)
            conn.start(queue: self.queue)
        }
    }

    func start() async throws -> URL {
        let port: UInt16 = try await withCheckedThrowingContinuation { cont in
            let once = OnceFlag()
            listener.stateUpdateHandler = { [listener] state in
                switch state {
                case .ready:
                    if once.first() { cont.resume(returning: listener.port?.rawValue ?? 0) }
                case .failed(let e):
                    if once.first() { cont.resume(throwing: e) }
                default:
                    break
                }
            }
            listener.start(queue: queue)
        }
        return URL(string: "ws://127.0.0.1:\(port)/ws")!
    }

    func stop() {
        listener.cancel()
        queue.async { self.conns.forEach { $0.cancel() } }
    }
}

private final class OnceFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func first() -> Bool { lock.withLock { defer { done = true }; return !done } }
}

private final class Counter: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    func add() { lock.withLock { n += 1 } }
    var value: Int { lock.withLock { n } }
}

/// The client against DeviceStandIn: short waits, so a test takes a second or two.
private func client() -> WSClient {
    WSClient(timing: .init(liveness: .init(pongWait: 0.3), tick: 0.05))
}

private func tagOf(_ r: Msg_RespEnvelope) -> String? {
    if case .respTagsList(let t) = r.payload { return t.tags.first }
    return nil
}

@Suite(.serialized)
struct WSClientTimeoutTests {
    @Test func aLateAnswerIsDroppedAndTheNextRequestGetsItsOwn() async throws {
        // The tags are answered after 1 s; everything else at once.
        let server = try DeviceStandIn(answersPings: true) { req in
            if case .reqGetTags = req.payload { return 1.0 }
            return 0
        }
        let url = try await server.start()
        defer { server.stop() }
        let ws = client()
        let drops = Counter()
        await ws.setOnDisconnect { drops.add() }
        try await ws.connect(url: url)

        let started = Date()
        await #expect(throws: WSClient.TimedOut.self) {
            _ = try await ws.request(timeout: 0.4) { $0.payload = .reqGetTags(.init()) }
        }
        #expect(Date().timeIntervalSince(started) < 0.9)
        // A timeout leaves the socket alone: no reconnect, no sign-in.
        let up = await ws.connected
        #expect(up)
        #expect(drops.value == 0)

        let b = try await ws.request(timeout: 5) { $0.payload = .reqGetStatus(.init()) }
        #expect(tagOf(b) == "\(b.id)")
        // The late answer to the tags lands now: no one waits for it, and
        // the next request still gets its own.
        try await Task.sleep(nanoseconds: 800_000_000)
        let c = try await ws.request { $0.payload = .reqGetStatus(.init()) }
        #expect(tagOf(c) == "\(c.id)")
        #expect(c.id != b.id)
        #expect(drops.value == 0)
        await ws.close()
    }

    @Test func withoutATimeoutARequestWaitsForItsAnswer() async throws {
        // Uploads, downloads and the rest keep waiting as before: 1.5 s
        // here, on a socket that answers its pings meanwhile.
        let server = try DeviceStandIn(answersPings: true) { req in
            if case .reqReadFile = req.payload { return 1.5 }
            return 0
        }
        let url = try await server.start()
        defer { server.stop() }
        let ws = client()
        try await ws.connect(url: url)
        _ = try await ws.request { $0.payload = .reqGetStatus(.init()) }
        let r = try await ws.request { $0.payload = .reqReadFile(.init()) }
        #expect(tagOf(r) == "\(r.id)")
        await ws.close()
    }

    @Test func aSocketThatStopsAnsweringIsFoundOnceARequestTimesOut() async throws {
        // The first request is answered (the socket is heard from), then
        // nothing at all: no answers, no pongs - a link that died.
        let first = OnceFlag()
        let server = try DeviceStandIn(answersPings: false) { _ in first.first() ? 0 : nil }
        let url = try await server.start()
        defer { server.stop() }
        let ws = client()
        let drops = Counter()
        await ws.setOnDisconnect { drops.add() }
        try await ws.connect(url: url)
        _ = try await ws.request { $0.payload = .reqGetStatus(.init()) }

        // Nothing decides while it is waited for: its own time does.
        let started = Date()
        await #expect(throws: WSClient.TimedOut.self) {
            _ = try await ws.request(timeout: 0.5) { $0.payload = .reqSearchPhotos(.init()) }
        }
        #expect(Date().timeIntervalSince(started) >= 0.5)
        #expect(drops.value == 0)
        // Then the socket is checked at once (nothing else waits): the
        // screen's retry, sent meanwhile, fails with it rather than wait
        // its own time on a socket that is gone.
        let retry = Task { try await ws.request(timeout: 10) { $0.payload = .reqSearchPhotos(.init()) } }
        await #expect(throws: WSClient.Unresponsive.self) { _ = try await retry.value }
        #expect(Date().timeIntervalSince(started) < 2)
        #expect(drops.value == 1)
        let stillConnected = await ws.connected
        #expect(!stillConnected)
    }

    @Test func aSlowAnswerIsNeverCutOffByAMissingPong() async throws {
        // H2: pongs never come back (the bridge drops them while it writes
        // a big answer's frames), and the answer takes five pong waits.
        // The socket stays, and the request gets its answer.
        let server = try DeviceStandIn(answersPings: false) { req in
            if case .reqReadFile = req.payload { return 1.5 }
            return 0
        }
        let url = try await server.start()
        defer { server.stop() }
        let ws = client()
        let drops = Counter()
        await ws.setOnDisconnect { drops.add() }
        try await ws.connect(url: url)
        let answer = try await ws.request(timeout: 10) { $0.payload = .reqReadFile(.init()) }
        #expect(tagOf(answer) == "\(answer.id)")
        #expect(drops.value == 0)
        let up = await ws.connected
        #expect(up)
        await ws.close()
    }

    @Test func anAnswerSaysHowLongItWasWaitedFor() async throws {
        let server = try DeviceStandIn(answersPings: true) { req in
            if case .reqGetTags = req.payload { return 0.4 }
            return 0
        }
        let url = try await server.start()
        defer { server.stop() }
        let ws = client()
        try await ws.connect(url: url)
        let a = try await ws.exchange(timeout: 5) { $0.payload = .reqGetTags(.init()) }
        #expect(a.waited >= 0.35)
        #expect(a.waited < 2)
        await ws.close()
    }

    @Test func aStalledUpgradeFailsTheConnectInTime() async throws {
        // M1: the connection is taken and the upgrade never answered.
        let server = try SilentTCP()
        let url = try await server.start()
        defer { server.stop() }
        let ws = client()
        let drops = Counter()
        await ws.setOnDisconnect { drops.add() }
        let started = Date()
        await #expect(throws: WSClient.ConnectTimeout.self) {
            try await ws.connect(url: url, within: 0.5)
        }
        #expect(Date().timeIntervalSince(started) < 2)
        // A failed connect like any other: its owner retries.
        #expect(drops.value == 1)
        let up = await ws.connected
        #expect(!up)
    }

    @Test func theClockStartsOnceTheRequestHasLeftThePhone() async throws {
        // H3: a 16 MiB chunk goes first and the device reads nothing for
        // 1.5 s: the small request behind it can't have left before then,
        // and its 0.6 s only count from there (Android's test of the same).
        let server = try RawDevice(readPause: 1.5)
        defer { server.stop() }
        let ws = client()
        try await ws.connect(url: server.url)
        let started = Date()
        let chunk = Task {
            var c = Msg_UploadChunk()
            c.data = Data(repeating: 7, count: 16 << 20)
            return try await ws.request { $0.payload = .reqUploadChunk(c) }
        }
        try await Task.sleep(nanoseconds: 50_000_000)
        let small = try await ws.exchange(timeout: 0.6) { $0.payload = .reqGetStatus(.init()) }
        // It waited behind the chunk, yet didn't time out; the wait for the
        // answer itself was short.
        #expect(Date().timeIntervalSince(started) >= 1.4)
        #expect(small.waited < 0.6)
        _ = try await chunk.value
        await ws.close()
    }

    @Test func aSlowDeviceOnALiveSocketKeepsItsRequest() async throws {
        // Answers 1.5 s late - five times the quiet time - but the socket
        // answers its pings meanwhile.
        let server = try DeviceStandIn(answersPings: true) { req in
            if case .reqSearchPhotos = req.payload { return 1.5 }
            return 0
        }
        let url = try await server.start()
        defer { server.stop() }
        let ws = client()
        let drops = Counter()
        await ws.setOnDisconnect { drops.add() }
        try await ws.connect(url: url)
        _ = try await ws.request { $0.payload = .reqGetStatus(.init()) }

        let r = try await ws.request(timeout: 10) { $0.payload = .reqSearchPhotos(.init()) }
        #expect(tagOf(r) == "\(r.id)")
        #expect(drops.value == 0)
        await ws.close()
    }

    @Test func checkedOnDemandWithNothingWaiting() async throws {
        let first = OnceFlag()
        let server = try DeviceStandIn(answersPings: false) { _ in first.first() ? 0 : nil }
        let url = try await server.start()
        defer { server.stop() }
        let ws = client()
        let drops = Counter()
        await ws.setOnDisconnect { drops.add() }
        try await ws.connect(url: url)
        _ = try await ws.request { $0.payload = .reqGetStatus(.init()) }
        // The app back in front: the dead socket is found without a request.
        await ws.verify()
        for _ in 0..<200 where drops.value == 0 { try await Task.sleep(nanoseconds: 10_000_000) }
        #expect(drops.value == 1)

        // A live one stays.
        let alive = try DeviceStandIn(answersPings: true) { _ in 0 }
        let aliveURL = try await alive.start()
        defer { alive.stop() }
        let ws2 = client()
        let drops2 = Counter()
        await ws2.setOnDisconnect { drops2.add() }
        try await ws2.connect(url: aliveURL)
        _ = try await ws2.request { $0.payload = .reqGetStatus(.init()) }
        await ws2.verify()
        try await Task.sleep(nanoseconds: 1_000_000_000)
        #expect(drops2.value == 0)
        let aliveConnected = await ws2.connected
        #expect(aliveConnected)
        await ws2.close()
    }
}

// A wake reaches every Images model alive: never beside ImagesRetryTests.
extension ImagesNotificationTests {
@MainActor
struct WakeTests {
    @Test func aWakeCutsTheWaitShort() async {
        let c = OTCConnection.shared
        let started = Date()
        Task { @MainActor in
            try? await Task.sleep(nanoseconds: 100_000_000)
            c.wake()
        }
        await c.sleepOrWake(10)
        #expect(Date().timeIntervalSince(started) < 2)
        // Without one, the whole wait.
        let quiet = Date()
        await c.sleepOrWake(0.3)
        #expect(Date().timeIntervalSince(quiet) >= 0.29)
    }
}
}

// MARK: - Images: the pages, their retries and what the grid shows

/// Suites whose Images models must not run beside each other: Files
/// posts otcImagesChanged, which every Images model alive answers with a
/// new search (OutOfImagesTests is nested here too), and a wake
/// (OTCConnection.wake) reaches every model alive.
@Suite(.serialized)
enum ImagesNotificationTests {}

/// The device, for Images' requests: answers each SearchPhotos with the
/// next of `pages` (an error to throw, or an answer), and the rest as told.
@MainActor
private final class FakeDevice {
    enum Page {
        case files(Int, from: Int, token: String)
        case fail(Error)
        case answer(Msg_RespEnvelope)
        /// Held until `release` is called.
        case held(Int, from: Int, token: String)
    }
    var pages: [Page] = []
    var searches: [Msg_SearchPhotos] = []
    /// The kind each request was asked as (nil: the socket's own time),
    /// in order, by what it asked.
    var kinds: [(String, PhotoGalleryVM.AskKind?)] = []
    var buckets: [Result<Msg_RespEnvelope, Error>] = []
    var bucketAsks = 0
    var deletes: [String] = []
    var library: [Msg_ReqEnvelope.OneOf_Payload] = []
    var groupFails = 0
    var groupCount: Int32 = 9
    private var held: [CheckedContinuation<Void, Never>] = []

    func release() {
        let h = held
        held = []
        h.forEach { $0.resume() }
    }

    static func files(_ n: Int, from: Int, token: String) -> Msg_RespEnvelope {
        var lof = Msg_ListOfFiles()
        lof.files = (from..<(from + n)).map { i in
            var f = Msg_File()
            f.path = "/photos/\(i).jpg"
            f.hash = "h\(i)"
            f.mime = "image/jpeg"
            f.created = Google_Protobuf_Timestamp(date: Date(timeIntervalSince1970: 1_780_000_000 - Double(i) * 3600))
            return f
        }
        lof.token = token
        var r = Msg_RespEnvelope()
        r.payload = .respListOfFiles(lof)
        return r
    }

    static let unreachable: Msg_RespEnvelope = {
        var r = Msg_RespEnvelope()
        var a = Msg_Ack()
        a.code = "device_unreachable"
        r.payload = .respAck(a)
        return r
    }()

    func attach(_ vm: PhotoGalleryVM) {
        vm.pageRetry.delay = { _ in 0.05 }
        vm.bucketRetry.delay = { _ in 0.05 }
        vm.tagsRetry.delay = { _ in 0.05 }
        vm.peopleRetry.delay = { _ in 0.05 }
        vm.groupsRetry.delay = { _ in 0.05 }
        // As a phone that is online, signed in to a device that answers.
        vm.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: false, statusCode: nil) }
        // Weak: a model can outlive its test (a retry still waiting).
        vm.photoRequest = { [weak self] payload, kind in
            guard let self else { throw CancellationError() }
            switch payload {
            case .reqSearchPhotos(let sp):
                self.searches.append(sp)
                self.kinds.append(("page", kind))
                let next = self.pages.isEmpty ? Page.files(0, from: 0, token: "") : self.pages.removeFirst()
                switch next {
                case .files(let n, let from, let token): return Self.files(n, from: from, token: token)
                case .fail(let e): throw e
                case .answer(let r): return r
                case .held(let n, let from, let token):
                    await withCheckedContinuation { self.held.append($0) }
                    return Self.files(n, from: from, token: token)
                }
            case .reqPhotoDateBuckets:
                self.bucketAsks += 1
                self.kinds.append(("buckets", kind))
                if !self.buckets.isEmpty { return try self.buckets.removeFirst().get() }
                var b = Msg_RespPhotoDateBuckets()
                var m = Msg_PhotoDateBucket()
                m.month = "2026-05"
                m.count = 40
                b.buckets = [m]
                var r = Msg_RespEnvelope()
                r.payload = .respPhotoDateBuckets(b)
                return r
            case .reqDelFile(let d):
                self.deletes.append(d.path)
                self.kinds.append(("delete", kind))
                var r = Msg_RespEnvelope()
                var a = Msg_Ack()
                a.ok = true
                r.payload = .respAck(a)
                return r
            default:
                throw CancellationError()
            }
        }
        vm.libraryRequest = { [weak self] payload, kind in
            guard let self else { throw CancellationError() }
            self.library.append(payload)
            var r = Msg_RespEnvelope()
            switch payload {
            case .reqGetTags:
                self.kinds.append(("tags", kind))
                var t = Msg_TagsList()
                t.tags = ["beach"]
                r.payload = .respTagsList(t)
            case .reqListImageGroups:
                self.kinds.append(("groups", kind))
                if self.groupFails > 0 {
                    self.groupFails -= 1
                    return Self.unreachable
                }
                var g = Msg_ImageGroup()
                g.id = "g1"
                g.name = "Trip"
                g.fileCount = self.groupCount
                var gs = Msg_ImageGroups()
                gs.groups = [g]
                r.payload = .respImageGroups(gs)
            default:
                throw CancellationError()
            }
            return r
        }
        vm.peopleRequest = { _, _ in throw CancellationError() }
    }

    func kinds(of what: String) -> [PhotoGalleryVM.AskKind?] {
        kinds.filter { $0.0 == what }.map(\.1)
    }
}

private struct Boom: Error {}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct ImagesRetryTests {
    private func model() -> (PhotoGalleryVM, FakeDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = FakeDevice()
        device.attach(vm)
        return (vm, device)
    }

    @Test func greyTilesFromTheVeryStart() {
        let (vm, _) = model()
        // Before anything was asked: grey tiles, never a blank page.
        #expect(vm.gridBody == .skeleton)
        #expect(vm.pageProblem == nil)
    }

    @Test func aFirstPageThatFailsSaysWhyAtOnceAndIsAskedAgain() async {
        let (vm, device) = model()
        device.pages = [.answer(FakeDevice.unreachable), .files(12, from: 0, token: "t1")]
        vm.pageRetry.delay = { _ in 0.3 }
        vm.onAppearInitial()
        await until { vm.pageProblem != nil }
        // After the first failure the grid says so (Android's PROBLEM):
        // the bridge's word for a device it can't reach.
        #expect(vm.pageProblem == .unreachable)
        #expect(vm.gridBody == .problem)
        #expect(vm.pageRetry.waiting)
        await until { vm.items.count == 12 }
        #expect(vm.items.count == 12)
        #expect(vm.pageProblem == nil)
        #expect(vm.gridBody == .photos)
        // Both asked for a first page: no token, the small first page.
        #expect(device.searches.count == 2)
        #expect(device.searches.allSatisfy { $0.token.isEmpty && $0.limit == cFirstPhotoPageLimit })
    }

    @Test func theProblemStaysUpThroughTheRetriesAndTryAgainAsksAtOnce() async {
        let (vm, device) = model()
        device.pages = [.fail(Boom()), .fail(WSClient.TimedOut()), .held(3, from: 0, token: "")]
        vm.pageRetry.delay = { n in n == 1 ? 0.05 : 100 }
        vm.onAppearInitial()
        await until { vm.pageProblem == .slow }
        // Asked again once by itself (1 s, shortened here): still the
        // problem, not grey tiles in between, and now why: no answer in time.
        #expect(vm.gridBody == .problem)
        #expect(device.searches.count == 2)
        #expect(!vm.retrying)
        // Try again: at once, "Trying…" while it is on its way.
        vm.retryPage()
        await until { device.searches.count == 3 }
        #expect(vm.retrying)
        #expect(vm.gridBody == .problem)
        device.release()
        await until { vm.items.count == 3 }
        #expect(vm.gridBody == .photos)
        #expect(!vm.retrying)
        #expect(!vm.pageRetry.waiting)
    }

    @Test func aWakeAsksAgainAtOnce() async {
        let (vm, device) = model()
        device.pages = [.fail(URLError(.notConnectedToInternet)), .files(5, from: 0, token: "")]
        vm.pageRetry.delay = { _ in 100 }
        vm.onAppearInitial()
        await until { vm.pageProblem != nil }
        #expect(vm.pageRetry.waiting)
        // Signed in again, a network came up, back in the foreground.
        OTCConnection.shared.wake()
        await until { vm.items.count == 5 }
        #expect(vm.items.count == 5)
        #expect(vm.pageProblem == nil)
    }

    @Test func aNewSearchDropsTheOldOnesRetryAndLateAnswer() async {
        let (vm, device) = model()
        // The first search's page fails; its retry would come in 0.3 s.
        device.pages = [.fail(Boom())]
        vm.pageRetry.delay = { _ in 0.3 }
        vm.onAppearInitial()
        await until { vm.pageProblem != nil }
        // A tag: a new search, whose page is held, then a third search.
        device.pages = [.held(4, from: 100, token: ""), .files(2, from: 200, token: "")]
        vm.addChip("beach")
        await until { device.searches.count == 2 }
        #expect(vm.pageProblem == nil)
        #expect(vm.gridBody == .skeleton)
        vm.removeChip("beach")
        await until { vm.items.count == 2 }
        // The held answer of the search in between lands now: dropped.
        device.release()
        try? await Task.sleep(nanoseconds: 500_000_000)
        #expect(vm.items.map(\.path) == ["/photos/200.jpg", "/photos/201.jpg"])
        // And the first search's retry never went out.
        #expect(device.searches.count == 3)
        #expect(device.searches.map(\.tags) == [[], ["beach"], []])
    }

    @Test func aLaterPageThatFailsSaysSoAtTheEndAndIsAskedAgainByItsToken() async {
        let (vm, device) = model()
        device.pages = [.files(12, from: 0, token: "t1"), .fail(WSClient.TimedOut()), .files(5, from: 12, token: "")]
        vm.pageRetry.delay = { _ in 0.3 }
        vm.onAppearInitial()
        await until { vm.items.count == 12 }
        await vm.loadMoreIfNeeded(index: 11, id: vm.items[11].id)
        #expect(vm.pageProblem == .slow)
        // A later page's problem doesn't take the grid's place.
        #expect(vm.gridBody == .photos)
        // Tiles coming on screen meanwhile don't hurry the retry.
        await vm.loadMoreIfNeeded(index: 11, id: vm.items[11].id)
        #expect(device.searches.count == 2)
        await until { vm.items.count == 17 }
        #expect(vm.pageProblem == nil)
        #expect(vm.endReached)
        #expect(device.searches[1].token == "t1")
        #expect(device.searches[2].token == "t1")
    }

    @Test func pageAfterPageThatAddsNothingStopsAndSaysSo() async {
        let (vm, device) = model()
        var waits: [Int] = []
        vm.pageRetry.noProgressDelay = { n in waits.append(n); return 100 }
        // The same 12 photos, page after page: a device starting the search over.
        device.pages = [.files(12, from: 0, token: "t1")] + Array(repeating: .files(12, from: 0, token: "t1"), count: 12)
        vm.onAppearInitial()
        await until { vm.items.count == 12 }
        await vm.loadMoreIfNeeded(index: 11, id: vm.items[11].id)
        #expect(device.searches.count == 13)
        #expect(vm.pageProblem == .failed)
        // Said at the end of the grid with its Try again, and asked for
        // again in a while (Android: 10 s doubling to a minute), not on
        // and on.
        #expect(vm.pageRetry.waiting)
        #expect(waits == [1])
        #expect(vm.gridBody == .photos)
        #expect((1...4).map { PageRetry().noProgressDelay($0) } == [10, 20, 40, 60])
    }

    @Test func stillWaitingAfterAWhile() async {
        let (vm, device) = model()
        vm.slowFirstPageAfter = 0.1
        device.pages = [.held(4, from: 0, token: "")]
        vm.onAppearInitial()
        await until { vm.slowFirstPage }
        #expect(vm.slowFirstPage)
        #expect(vm.gridBody == .skeleton)
        device.release()
        await until { vm.items.count == 4 }
        #expect(!vm.slowFirstPage)

        // One that fails says why instead: no pill over a problem.
        let (vm2, device2) = model()
        vm2.slowFirstPageAfter = 0.1
        vm2.pageRetry.delay = { _ in 100 }
        device2.pages = [.fail(Boom())]
        vm2.onAppearInitial()
        await until { vm2.pageProblem != nil }
        try? await Task.sleep(nanoseconds: 300_000_000)
        #expect(!vm2.slowFirstPage)
    }

    @Test func eachRequestIsAskedAsItsKind() async {
        // Its time - the kind's, more after a timeout, back down on an
        // answer, per route - is OTCConnection.ask's (Patience).
        let (vm, device) = model()
        device.pages = [.fail(WSClient.TimedOut()), .files(12, from: 0, token: "t1"), .files(5, from: 12, token: "")]
        vm.onAppearInitial()
        await until { vm.items.count == 12 }
        await vm.loadMoreIfNeeded(index: 11, id: vm.items[11].id)
        await until { vm.items.count == 17 }
        #expect(device.kinds(of: "page") == [.page, .page, .page])
        #expect(device.kinds(of: "tags") == [.tags])
        #expect(device.kinds(of: "buckets").first == .buckets)
        // What the user does (a delete) gets the socket's own time.
        vm.toggleSelect(vm.items[0].path)
        vm.deleteSelected()
        await until { !device.deletes.isEmpty }
        #expect(device.kinds(of: "delete") == [nil])
    }

    @Test func theDateBucketsAreAskedAgainUntilTheyCome() async {
        let (vm, device) = model()
        device.buckets = [.failure(Boom()), .success(FakeDevice.unreachable)]
        vm.onAppearInitial()
        await until { vm.showScrubber }
        #expect(vm.showScrubber)
        #expect(device.bucketAsks == 3)
        #expect(vm.totalPhotos == 40)
    }

    @Test func aDeviceWithoutDateBucketsIsNotAskedAgain() async {
        let (vm, device) = model()
        var unknown = Msg_RespEnvelope()
        unknown.error = true
        unknown.errorCode = "unknown_payload"
        device.buckets = [.success(unknown)]
        vm.onAppearInitial()
        await until { vm.bucketsFresh }
        try? await Task.sleep(nanoseconds: 300_000_000)
        #expect(device.bucketAsks == 1)
        #expect(!vm.showScrubber)
    }

    @Test func theTagsAreAskedAgainUntilTheyCome() async {
        let (vm, device) = model()
        var tagsAsked = 0
        let answer = vm.libraryRequest
        vm.libraryRequest = { payload, timeout in
            if case .reqGetTags = payload {
                tagsAsked += 1
                if tagsAsked == 1 { throw Boom() }
            }
            return try await answer(payload, timeout)
        }
        vm.loadLibraryOnce()
        await until { vm.tags == ["beach"] }
        #expect(vm.tags == ["beach"])
        #expect(tagsAsked == 2)
        _ = device
    }

    @Test func theCollectionsFillInWhenTheBackgroundRetryGetsThem() async {
        let (vm, device) = model()
        device.groupFails = 2
        #expect(await vm.loadGroups() == false)
        #expect(!vm.groupsLoaded)
        // Nobody asks again: the background retry gets them.
        await until { vm.groupsLoaded }
        #expect(vm.groupsLoaded)
        #expect(vm.groups.map(\.id) == ["g1"])
        #expect(device.kinds(of: "groups") == [.groups, .groups, .groups])
    }

    @Test func aJumpsFirstPageIsAskedAgainAtItsMonth() async {
        let (vm, device) = model()
        device.pages = [.files(12, from: 0, token: "t1"), .fail(Boom()), .files(3, from: 50, token: "j1")]
        vm.onAppearInitial()
        await until { vm.items.count == 12 }
        vm.placeholderMonth = "2024-03"
        vm.placeholderCount = 30
        vm.jumpToDate("2024-03")
        // Failed: the grey tiles go for the grid's problem.
        await until { vm.pageProblem != nil && vm.placeholderCount == nil }
        #expect(vm.placeholderCount == nil)
        #expect(vm.gridBody == .problem)
        await until { vm.items.count == 3 }
        #expect(vm.items.first?.path == "/photos/50.jpg")
        let cutoff = PhotoMonths.lastInstant(of: "2024-03")!
        #expect(device.searches.count == 3)
        #expect(abs(device.searches[1].before.date.timeIntervalSince(cutoff)) < 0.001)
        // Asked again: a first page again, at the same month.
        #expect(abs(device.searches[2].before.date.timeIntervalSince(cutoff)) < 0.001)
        #expect(device.searches[2].token.isEmpty)
        #expect(vm.placeholderCount == nil)
    }

    @Test func aJumpWhoseTokenTheDeviceLostIsAskedAgainWithItsCutoff() async {
        let (vm, device) = model()
        let cutoff = PhotoMonths.lastInstant(of: "2024-03")!
        // The jump's first page, then a page from a search the device
        // started again (a new token): from the newest photos.
        var jumped = FakeDevice.files(12, from: 50, token: "j1")
        var restarted = FakeDevice.files(12, from: 0, token: "other")
        if case .respListOfFiles(var lof) = jumped.payload {
            for i in lof.files.indices { lof.files[i].created = Google_Protobuf_Timestamp(date: cutoff - Double(i + 1) * 86400) }
            jumped.payload = .respListOfFiles(lof)
        }
        if case .respListOfFiles(var lof) = restarted.payload {
            lof.files[0].created = Google_Protobuf_Timestamp(date: Date())
            restarted.payload = .respListOfFiles(lof)
        }
        device.pages = [.files(1, from: 900, token: ""), .answer(jumped), .answer(restarted), .files(4, from: 62, token: "")]
        vm.onAppearInitial()
        await until { vm.items.count == 1 }
        vm.jumpToDate("2024-03")
        await until { vm.items.count == 12 }
        await vm.loadMoreIfNeeded(index: 11, id: vm.items[11].id)
        await until { vm.items.count == 16 }
        #expect(device.searches.count == 4)
        #expect(device.searches[2].token == "j1")
        #expect(!device.searches[2].hasBefore)
        #expect(device.searches[3].token == "j1")
        #expect(abs(device.searches[3].before.date.timeIntervalSince(cutoff)) < 0.001)
        #expect(device.searches[3].have == 12)
        #expect(vm.items.last?.path == "/photos/65.jpg")
    }

    @Test func theNoDateBucketJumpsBeforeEveryDate() async {
        let (vm, device) = model()
        vm.onAppearInitial()
        await until { device.searches.count == 1 && !vm.loading }
        vm.jumpToDate("0000-00")
        await until { device.searches.count == 2 }
        let cal = PhotoMonths.calendar()
        let expected = cal.date(from: DateComponents(year: 1900, month: 1, day: 1))!.addingTimeInterval(-0.001)
        #expect(abs(device.searches[1].before.date.timeIntervalSince(expected)) < 0.002)
        #expect(PhotoMonths.scrubLabel("0000-00") == "No date")
        #expect(PhotoMonths.scrubLabel("2024-03") == "Mar 2024")
        // Not a month at all: no jump, and no grey tiles left standing.
        vm.placeholderMonth = "junk"
        vm.placeholderCount = 12
        vm.jumpToDate("junk")
        #expect(vm.placeholderCount == nil)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(device.searches.count == 2)
    }

    @Test func aJumpKeepsTheSelection() async {
        let (vm, device) = model()
        device.pages = [.files(12, from: 0, token: "t1"), .files(3, from: 50, token: "")]
        vm.onAppearInitial()
        await until { vm.items.count == 12 }
        vm.toggleSelect(vm.items[2].path)
        vm.jumpToDate("2024-03")
        await until { vm.items.count == 3 }
        #expect(vm.selected == ["/photos/2.jpg"])
    }

    @Test func deletingInAnOpenCollectionAsksForItsCountAgain() async {
        let (vm, device) = model()
        var g = Msg_ImageGroup()
        g.id = "g1"
        g.name = "Trip"
        g.fileCount = 10
        device.pages = [.files(10, from: 0, token: "")]
        vm.openGroup(g)
        await until { vm.items.count == 10 }
        let bucketsBefore = device.bucketAsks
        device.library.removeAll()
        // The first answer after the delete is lost: asked again by itself.
        device.groupFails = 1
        vm.toggleSelect(vm.items[0].path)
        vm.deleteSelected()
        await until { vm.activeGroup?.fileCount == 9 }
        #expect(device.deletes == ["/photos/0.jpg"])
        #expect(vm.activeGroup?.fileCount == 9)
        #expect(device.library.filter { if case .reqListImageGroups = $0 { return true } else { return false } }.count == 2)
        #expect(device.bucketAsks > bucketsBefore)
    }

    @Test func deletingEverythingShownAsksForTheNextPage() async {
        let (vm, device) = model()
        device.pages = [.files(2, from: 0, token: "t1"), .files(3, from: 2, token: "")]
        vm.onAppearInitial()
        await until { vm.items.count == 2 }
        vm.toggleSelect(vm.items[0].path)
        vm.toggleSelect(vm.items[1].path)
        vm.deleteSelected()
        await until { vm.items.count == 3 }
        #expect(vm.items.first?.path == "/photos/2.jpg")
        #expect(device.searches.last?.token == "t1")
    }

    @Test func theEmptyStates() async {
        let (vm, device) = model()
        device.pages = [.files(0, from: 0, token: "")]
        vm.onAppearInitial()
        await until { vm.gridBody == .empty }
        #expect(vm.gridBody == .empty)
        #expect(vm.emptyKind == .noPhotos)

        device.pages = [.files(0, from: 0, token: "")]
        vm.addChip("beach")
        await until { vm.gridBody == .empty }
        #expect(vm.emptyKind == .noMatch)

        var g = Msg_ImageGroup()
        g.id = "g1"
        g.name = "Trip"
        device.pages = [.files(0, from: 0, token: "")]
        vm.openGroup(g)
        await until { vm.gridBody == .empty }
        #expect(vm.emptyKind == .collection)
        // A search inside it that finds nothing is "No photos match".
        device.pages = [.files(0, from: 0, token: "")]
        vm.addChip("dog")
        await until { vm.gridBody == .empty && !vm.chips.isEmpty }
        #expect(vm.emptyKind == .noMatch)
    }

    @Test func theFilesViewerShowsItsPhotosOnly() {
        let vm = PhotoGalleryVM()
        #expect(vm.gridBody == .photos)
    }

    @Test func lostCutoff() {
        let cutoff = Date(timeIntervalSince1970: 1_700_000_000)
        let older = cutoff - 86400
        // A held token comes back as it was sent.
        #expect(!PhotoPaging.lostCutoff(files: [(older, "/a")], token: "t", sent: "t", cutoff: cutoff, shown: []))
        #expect(PhotoPaging.lostCutoff(files: [(older, "/a")], token: "new", sent: "t", cutoff: cutoff, shown: []))
        // The last page: the photos tell.
        #expect(!PhotoPaging.lostCutoff(files: [(older, "/a"), (nil, "/b")], token: "", sent: "t", cutoff: cutoff, shown: ["/z"]))
        #expect(PhotoPaging.lostCutoff(files: [(cutoff + 1, "/a")], token: "", sent: "t", cutoff: cutoff, shown: []))
        #expect(PhotoPaging.lostCutoff(files: [(older, "/z")], token: "", sent: "t", cutoff: cutoff, shown: ["/z"]))
    }
}

}

// MARK: - Files: a listing that didn't come

extension ImagesNotificationTests {
@MainActor
struct FilesListingRetryTests {
    @Test func aListingThatDidntComeSaysWhyAndIsAskedAgain() async {
        let vm = FilesExplorerViewModel(initialPath: "/Photos/")
        vm.thumbDisk = testThumbCache()
        vm.listRetry.delay = { _ in 0.3 }
        var asks = 0
        vm.request = { payload in
            guard case .reqListFiles = payload else { throw CancellationError() }
            asks += 1
            if asks == 1 { throw WSClient.TimedOut() }
            var lof = Msg_ListOfFiles()
            var f = Msg_File()
            f.path = "/Photos/a.jpg"
            lof.files = [f]
            var r = Msg_RespEnvelope()
            r.payload = .respListOfFiles(lof)
            return r
        }
        await vm.load()
        #expect(vm.error == "Your device took too long to answer. The files will appear as soon as it does.")
        await until { vm.rows.count == 2 }
        #expect(vm.rows.map(\.path) == ["..", "/Photos/a.jpg"])
        #expect(vm.error == nil)
        #expect(asks == 2)
    }

    @Test func theBridgesWordIsAskedAgainTheDevicesOwnRefusalIsNot() async {
        let vm = FilesExplorerViewModel(initialPath: "/Gone/")
        vm.thumbDisk = testThumbCache()
        vm.listRetry.delay = { _ in 0.05 }
        var asks = 0
        vm.request = { payload in
            guard case .reqListFiles = payload else { throw CancellationError() }
            asks += 1
            var r = Msg_RespEnvelope()
            if asks == 1 {
                var a = Msg_Ack()
                a.code = "device_unreachable"
                r.payload = .respAck(a)
            } else {
                r.error = true
                r.errorMessage = "no such folder"
            }
            return r
        }
        await vm.load()
        #expect(vm.error == "Your device isn't reachable right now. The files will appear as soon as it answers.")
        await until { asks == 2 }
        try? await Task.sleep(nanoseconds: 300_000_000)
        #expect(asks == 2)
        #expect(vm.error == "no such folder")
    }
}
}

// MARK: - Images: back online, small tiles, where the grid is

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct ImagesReviewFixTests {
    private func model() -> (PhotoGalleryVM, FakeDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = FakeDevice()
        device.attach(vm)
        return (vm, device)
    }

    @Test func backOnlineTheNeutralLineWhileTheRetryRuns() async {
        // (5): offline, then Wi-Fi back: the wake asks again, and while that
        // page is on its way the grid no longer says the phone is offline.
        let (vm, device) = model()
        var online = false
        vm.online = { online }
        vm.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: !online, statusCode: nil) }
        vm.pageRetry.delay = { _ in 100 }
        device.pages = [.fail(URLError(.notConnectedToInternet)), .held(5, from: 0, token: "")]
        vm.onAppearInitial()
        await until { vm.pageProblem != nil }
        #expect(vm.pageProblem == .offline)
        online = true
        OTCConnection.shared.wake()
        await until { device.searches.count == 2 }
        #expect(vm.retrying)
        #expect(vm.pageProblem == .failed)
        #expect(vm.pageProblem?.photosText == "Your photos will appear as soon as your device answers.")
        device.release()
        await until { !vm.items.isEmpty }
        #expect(vm.pageProblem == nil)
    }

    @Test func everySearchAsksForSmallTilesAndSaysWhereTheGridIs() async {
        // Part B and H1: every page asks for small thumbnails, and every
        // page that carries a token says how many photos the grid holds -
        // the retry of a lost later page with the same token and `have`.
        let (vm, device) = model()
        vm.pageRetry.delay = { _ in 0.05 }
        device.pages = [.files(12, from: 0, token: "t1"), .fail(WSClient.TimedOut()), .files(30, from: 12, token: "t2"), .files(3, from: 42, token: "")]
        vm.onAppearInitial()
        await until { vm.items.count == 12 }
        await vm.loadMoreIfNeeded(index: 11, id: vm.items[11].id)
        await until { vm.items.count == 42 }
        await vm.loadMoreIfNeeded(index: 41, id: vm.items[41].id)
        await until { vm.items.count == 45 }
        #expect(device.searches.allSatisfy { $0.smallThumbnails })
        #expect(device.searches.map(\.token) == ["", "t1", "t1", "t2"])
        #expect(device.searches.map(\.have) == [0, 12, 12, 42])
        // A jump's first page too.
        vm.jumpToDate("2024-03")
        await until { device.searches.count == 5 }
        #expect(device.searches[4].smallThumbnails)
        #expect(device.searches[4].hasBefore)
    }

    @Test func theCollectionsAskForSmallCovers() async {
        let (vm, device) = model()
        var created: Msg_CreateImageGroup?
        let answer = vm.libraryRequest
        vm.libraryRequest = { payload, kind in
            if case .reqCreateImageGroup(let c) = payload {
                created = c
                var r = Msg_RespEnvelope()
                var g = Msg_RespImageGroup()
                g.group.id = "g2"
                g.group.name = c.name
                r.payload = .respImageGroup(g)
                return r
            }
            return try await answer(payload, kind)
        }
        #expect(await vm.loadGroups())
        let asked = device.library.compactMap { p -> Msg_ListImageGroups? in
            if case .reqListImageGroups(let l) = p { return l }
            return nil
        }
        #expect(asked.count == 1)
        #expect(asked.allSatisfy { $0.smallThumbnails })
        vm.selected = ["/photos/1.jpg"]
        vm.newGroupName = "Beach"
        await vm.createGroupFromSelection()
        #expect(created?.smallThumbnails == true)
        #expect(created?.paths == ["/photos/1.jpg"])
        #expect(vm.groups.first?.id == "g2")
    }
}
}

// MARK: - The viewer: the big thumbnail only where a thumbnail stays

private func jpeg(_ side: CGFloat, _ color: UIColor = .orange) -> Data {
    let fmt = UIGraphicsImageRendererFormat()
    fmt.scale = 1
    return UIGraphicsImageRenderer(size: CGSize(width: side, height: side), format: fmt).image { ctx in
        color.setFill()
        ctx.fill(CGRect(x: 0, y: 0, width: side, height: side))
    }.jpegData(compressionQuality: 0.8)!
}

@MainActor
private func viewerItem(_ i: Int, video: Bool = false) -> PhotoGalleryVM.Item {
    PhotoGalleryVM.Item(id: "/photos/\(i).jpg#h\(i)#10", path: "/photos/\(i).jpg", mime: video ? "video/mp4" : "image/jpeg",
                        size: 10, thumbData: jpeg(40), localURL: nil, isLocalOnly: false)
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct ViewerBigThumbnailTests {
    /// The device for the viewer: GetFile as told, GetThumbnails as told.
    @MainActor
    final class Viewer {
        var getFile: (String) throws -> Msg_RespEnvelope = { _ in throw URLError(.timedOut) }
        var thumbs: [Result<Msg_RespEnvelope, Error>] = []
        var asked: [(Msg_GetThumbnails, PhotoGalleryVM.AskKind?)] = []

        func attach(_ vm: PhotoGalleryVM) {
            vm.bigThumbDelay = { _ in 0.05 }
            vm.photoRequest = { [weak self] payload, kind in
                guard let self else { throw CancellationError() }
                switch payload {
                case .reqGetFile(let g):
                    return try self.getFile(g.path)
                case .reqGetThumbnails(let g):
                    self.asked.append((g, kind))
                    if self.thumbs.isEmpty { throw URLError(.timedOut) }
                    return try self.thumbs.removeFirst().get()
                default:
                    throw CancellationError()
                }
            }
        }

        static func big(_ path: String, _ data: Data) -> Msg_RespEnvelope {
            var f = Msg_File()
            f.path = path
            f.content = data
            var lof = Msg_ListOfFiles()
            lof.files = [f]
            var r = Msg_RespEnvelope()
            r.payload = .respListOfFiles(lof)
            return r
        }
    }

    @Test func theFullSizeArrivesAndNoBigThumbnailIsAskedFor() async {
        let vm = PhotoGalleryVM()
        let device = Viewer()
        device.attach(vm)
        device.getFile = { path in
            var f = Msg_File()
            f.path = path
            f.content = jpeg(300)
            var r = Msg_RespEnvelope()
            r.payload = .respFile(f)
            return r
        }
        vm.showFiles([viewerItem(0)], startAt: 0)
        await until { vm.hiResImages["/photos/0.jpg"] != nil }
        try? await Task.sleep(nanoseconds: 200_000_000)
        #expect(device.asked.isEmpty)
        #expect(vm.bigThumbs.isEmpty)
        vm.closeModal()
    }

    @Test func aThumbnailThatStaysGetsTheBigOneAskedAgainUntilItComes() async {
        let vm = PhotoGalleryVM()
        let device = Viewer()
        device.attach(vm)
        // The full size fails; the big thumbnail twice, then comes.
        device.thumbs = [.failure(URLError(.timedOut)), .failure(WSClient.TimedOut()), .success(Viewer.big("/photos/0.jpg", jpeg(800)))]
        let small = viewerItem(0)
        vm.showFiles([small, viewerItem(1)], startAt: 0)
        await until { vm.bigThumbs["/photos/0.jpg"] != nil }
        let big = vm.bigThumbs["/photos/0.jpg"]
        #expect(big?.size.width == 800)
        #expect(device.asked.count == 3)
        // Without the flag (the big one), as its own kind, for that path only.
        #expect(device.asked.allSatisfy { !$0.0.smallThumbnails && $0.0.paths == ["/photos/0.jpg"] && $0.1 == .thumbnail })
        // Never in the tiles' place: the item keeps the grid's small bytes.
        #expect(vm.items[0].thumbData == small.thumbData)
        #expect(vm.hiResFailed.contains("/photos/0.jpg"))
        // The neighbour's failed prefetch asked for nothing: it isn't open.
        #expect(!device.asked.contains { $0.0.paths == ["/photos/1.jpg"] })
        vm.closeModal()
        #expect(vm.bigThumbs.isEmpty)
    }

    @Test func itStopsWhenTheDeviceHasNoneOrCantSay() async {
        // The path left out of the answer.
        let vm = PhotoGalleryVM()
        let device = Viewer()
        device.attach(vm)
        var none = Msg_RespEnvelope()
        none.payload = .respListOfFiles(Msg_ListOfFiles())
        device.thumbs = [.success(none)]
        vm.showFiles([viewerItem(0)], startAt: 0)
        await until { device.asked.count == 1 }
        try? await Task.sleep(nanoseconds: 300_000_000)
        #expect(device.asked.count == 1)
        #expect(vm.bigThumbs.isEmpty)
        vm.closeModal()

        // A device before GetThumbnails.
        let vm2 = PhotoGalleryVM()
        let device2 = Viewer()
        device2.attach(vm2)
        var unknown = Msg_RespEnvelope()
        unknown.error = true
        unknown.errorCode = "unknown_payload"
        device2.thumbs = [.success(unknown)]
        vm2.showFiles([viewerItem(0)], startAt: 0)
        await until { device2.asked.count == 1 }
        try? await Task.sleep(nanoseconds: 300_000_000)
        #expect(device2.asked.count == 1)
        vm2.closeModal()
    }

    @Test func itIsAskedOnlyWhileTheViewerStaysOnTheItem() async {
        let vm = PhotoGalleryVM()
        let device = Viewer()
        device.attach(vm)
        // Never answered: asked again and again while it is open...
        vm.showFiles([viewerItem(0), viewerItem(1)], startAt: 0)
        await until { device.asked.count >= 3 }
        // ...not after the viewer moved on to the next one, whose own
        // thumbnail stays and is asked for instead.
        vm.next()
        await until { device.asked.last?.0.paths == ["/photos/1.jpg"] }
        let onFirst = device.asked.filter { $0.0.paths == ["/photos/0.jpg"] }.count
        try? await Task.sleep(nanoseconds: 300_000_000)
        #expect(device.asked.filter { $0.0.paths == ["/photos/0.jpg"] }.count == onFirst)
        vm.closeModal()
        let afterClose = device.asked.count
        try? await Task.sleep(nanoseconds: 300_000_000)
        #expect(device.asked.count == afterClose)
    }

    @Test func aVideoThatCantPlayKeepsItsPosterWithTryAgain() async {
        let vm = PhotoGalleryVM()
        let device = Viewer()
        device.attach(vm)
        device.thumbs = [.success(Viewer.big("/photos/0.jpg", jpeg(640)))]
        vm.showFiles([viewerItem(0, video: true)], startAt: 0)
        vm.videoFailed("/photos/0.jpg")
        #expect(vm.unplayable.contains("/photos/0.jpg"))
        #expect(vm.videoPlayer == nil)
        await until { vm.bigThumbs["/photos/0.jpg"] != nil }
        #expect(vm.bigThumbs["/photos/0.jpg"]?.size.width == 640)
        // Try again: fetched and played again.
        vm.retryVideo()
        #expect(!vm.unplayable.contains("/photos/0.jpg"))
        #expect(vm.openIndex == 0)
        // Another item's failure says nothing about this one.
        vm.videoFailed("/photos/9.jpg")
        #expect(!vm.unplayable.contains("/photos/9.jpg"))
        vm.closeModal()
        #expect(vm.unplayable.isEmpty)
    }
}
}

// MARK: - Social: the feed's loads

struct FeedLoadStateTests {
    private let failedMore = FeedLoadState(loading: false, hasLoadedOnce: true, moreFailed: true)

    @Test func aRefreshThatWorkedTakesCouldntLoadMoreAway() {
        let after = failedMore.afterLoad(nil)
        #expect(!after.moreFailed)
        #expect(after.problem == nil)
        #expect(after.hasLoadedOnce)
    }

    @Test func aRefreshThatFailedLeavesTheEndAsItWas() {
        let after = failedMore.afterLoad(.unreachable)
        #expect(after.moreFailed)
        #expect(after.problem == .unreachable)
        // The first load failing says why, and isn't "loaded".
        let first = FeedLoadState().afterLoad(.offline)
        #expect(!first.hasLoadedOnce)
        #expect(first.problem == .offline)
    }

    @Test func theNextPageIsClaimedOnce() {
        // A wake's retryMore and the last post's task both asking: the
        // first claims it (and takes the failure line away), the second
        // finds it taken.
        let claimed = failedMore.claimMore()
        #expect(claimed != nil)
        #expect(claimed?.loadingMore == true)
        #expect(claimed?.moreFailed == false)
        #expect(claimed?.claimMore() == nil)
        // Nor while the feed itself is loading.
        #expect(FeedLoadState(loading: true).claimMore() == nil)
    }
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct SocialFeedRetryTests {
    private func post(_ i: Int) -> Msg_SocialPublication {
        var p = Msg_SocialPublication()
        p.uuid = "p\(i)"
        return p
    }

    private func page(_ range: Range<Int>) -> Msg_RespEnvelope {
        var sp = Msg_SocialPublications()
        sp.publications = range.map(post)
        var r = Msg_RespEnvelope()
        r.payload = .respSocialPublications(sp)
        return r
    }

    @Test func aRefreshThatWorkedEndsCouldntLoadMore() async {
        // (9): "Couldn't load more posts." used to survive it.
        let vm = SocialFeedViewModel(autoLoad: false)
        vm.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: false, statusCode: nil) }
        var answers: [Result<Msg_RespEnvelope, Error>] = [.success(page(0..<4)), .failure(WSClient.TimedOut()), .success(page(0..<4))]
        var asked: [Msg_GetSocialPublications] = []
        vm.feedRequest = { req in
            asked.append(req)
            return try answers.removeFirst().get()
        }
        #expect(await vm.loadFeed())
        await vm.loadMoreIfNeeded(current: vm.posts.last)
        #expect(vm.moreFailed)
        #expect(await vm.loadFeed())
        #expect(!vm.moreFailed)
        #expect(vm.problem == nil)
        #expect(asked.map(\.total) == [4, 4, 4])
        #expect(asked[1].excludeUuids == ["p0", "p1", "p2", "p3"])
    }

    @Test func twoAsksForTheNextPageSendOne() async {
        let vm = SocialFeedViewModel(autoLoad: false)
        var release: CheckedContinuation<Void, Never>?
        var asks = 0
        vm.feedRequest = { req in
            asks += 1
            if req.excludeUuids.isEmpty { return self.page(0..<4) }
            await withCheckedContinuation { release = $0 }
            return self.page(4..<8)
        }
        #expect(await vm.loadFeed())
        let last = vm.posts.last
        async let a: Void = vm.loadMoreIfNeeded(current: last)
        async let b: Void = vm.loadMoreIfNeeded(current: last)
        await until { release != nil }
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(asks == 2)
        release?.resume()
        _ = await (a, b)
        #expect(vm.posts.count == 8)
    }

    @Test func backOnlineTheNeutralLine() async {
        let vm = SocialFeedViewModel(autoLoad: false)
        var online = false
        vm.online = { online }
        vm.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: !online, statusCode: nil) }
        var release: CheckedContinuation<Void, Never>?
        var asks = 0
        vm.feedRequest = { _ in
            asks += 1
            if asks == 1 { throw URLError(.notConnectedToInternet) }
            await withCheckedContinuation { release = $0 }
            return self.page(0..<2)
        }
        #expect(await vm.loadFeed() == false)
        #expect(vm.problem == .offline)
        online = true
        vm.woke()
        #expect(vm.problem == .failed)
        let load = Task { await vm.loadFeed() }
        await until { release != nil }
        #expect(vm.problem == .failed)
        release?.resume()
        #expect(await load.value)
        #expect(vm.problem == nil)
    }
}

// MARK: - Files: the line stays through the retries; small tiles

@MainActor
@Suite(.serialized)
struct FilesReviewFixTests {
    @Test func theLineStaysWhileTheSameFolderIsAskedAgain() async {
        // (5): it used to be cleared at every try (a flicker, announced
        // anew), and to keep saying the phone is offline once it wasn't.
        let vm = FilesExplorerViewModel(initialPath: "/Photos/")
        vm.thumbDisk = testThumbCache()
        vm.listRetry.delay = { _ in 100 }
        var online = false
        vm.online = { online }
        vm.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: !online, statusCode: nil) }
        var release: CheckedContinuation<Void, Never>?
        var asks = 0
        vm.request = { payload in
            guard case .reqListFiles = payload else { throw CancellationError() }
            asks += 1
            if asks == 1 { throw URLError(.notConnectedToInternet) }
            await withCheckedContinuation { release = $0 }
            release = nil
            if asks == 2 { throw URLError(.notConnectedToInternet) }
            var lof = Msg_ListOfFiles()
            var f = Msg_File()
            f.path = "/Photos/a.jpg"
            lof.files = [f]
            var r = Msg_RespEnvelope()
            r.payload = .respListOfFiles(lof)
            return r
        }
        let offlineLine = "This phone is offline. The files will appear once it's back online."
        await vm.load()
        #expect(vm.error == offlineLine)
        // Asked again (its retry, a wake) while still offline: the same
        // line all along, not none in between.
        let again = Task { await vm.load() }
        await until { release != nil }
        #expect(vm.error == offlineLine)
        release?.resume()
        await again.value
        #expect(vm.error == offlineLine)
        // Back online: the neutral line while it is asked again, then the
        // listing.
        online = true
        let third = Task { await vm.load() }
        await until { release != nil }
        #expect(vm.error == "The files will appear as soon as your device answers.")
        release?.resume()
        await third.value
        #expect(vm.error == nil)
        #expect(vm.rows.map(\.path) == ["..", "/Photos/a.jpg"])
        // Back online: the neutral line while it is asked again.
        #expect(FilesExplorerViewModel.errorWhileAsking(.offline, failedPath: "/Photos/", "/Photos/", online: true)
                == "The files will appear as soon as your device answers.")
        #expect(FilesExplorerViewModel.errorWhileAsking(.slow, failedPath: "/Photos/", "/Photos/", online: true)
                == "Your device took too long to answer. The files will appear as soon as it does.")
        // Another folder says nothing of this one's problem.
        #expect(FilesExplorerViewModel.errorWhileAsking(.offline, failedPath: "/Photos/", "/Other/", online: false) == nil)
        #expect(FilesExplorerViewModel.errorWhileAsking(nil, failedPath: nil, "/Photos/", online: true) == nil)
    }

    @Test func theGridAndTheSearchAskForSmallTiles() async {
        let vm = FilesExplorerViewModel(initialPath: "/Photos/")
        vm.thumbDisk = testThumbCache()
        var asked: [Msg_GetThumbnails] = []
        vm.request = { payload in
            var r = Msg_RespEnvelope()
            switch payload {
            case .reqListFiles:
                var lof = Msg_ListOfFiles()
                lof.files = (0..<3).map { i in
                    var f = Msg_File()
                    f.path = "\(i).jpg"
                    f.mime = "image/jpeg"
                    f.hash = "h\(i)"
                    return f
                }
                r.payload = .respListOfFiles(lof)
            case .reqGetThumbnails(let g):
                asked.append(g)
                r.payload = .respListOfFiles(Msg_ListOfFiles())
            default:
                throw CancellationError()
            }
            return r
        }
        await vm.load()
        for row in vm.rows where vm.isMedia(row) { vm.wantThumbnail(for: row) }
        await until { !asked.isEmpty }
        #expect(asked.first?.paths == ["/Photos/0.jpg", "/Photos/1.jpg", "/Photos/2.jpg"])
        #expect(asked.allSatisfy { $0.smallThumbnails })
    }
}
}

// MARK: - M1: a device that takes the socket and never answers

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct StalledSignInTests {
    @Test func aRequestsTimeCoversConnectingAndSigningInHasItsOwn() async throws {
        // Never on a simulator signed in to a device: this test configures
        // the app's own connection (a fresh test simulator has none).
        guard SecretsStore.loadOrCreate().endpoint.isEmpty else { return }
        // Takes the WebSocket, answers its pings, and never says a word.
        let device = try DeviceStandIn(answersPings: true) { _ in nil }
        let url = try await device.start()
        defer { device.stop() }
        let c = OTCConnection.shared
        let s = SecretsStore.loadOrCreate()
        s.endpoint = url.absoluteString
        s.password = "test-dummy"
        s.persist()
        c.reset()
        c.signInTimeout = 1.5
        defer {
            let s = SecretsStore.loadOrCreate()
            s.endpoint = ""
            s.password = ""
            s.persist()
            c.signInTimeout = 120
            c.reset()
        }

        // A request given 0.5 s: its time covers connecting, and it is
        // told so rather than waiting behind the sign-in for good.
        let started = Date()
        await #expect(throws: WSClient.TimedOut.self) {
            _ = try await c.request(timeout: 0.5) { $0.payload = .reqGetTags(.init()) }
        }
        #expect(Date().timeIntervalSince(started) < 1.4)
        // A screen's ask too - and a time that ran out while connecting
        // says nothing about the device's pace: no more time for the next.
        await #expect(throws: WSClient.TimedOut.self) {
            _ = try await c.ask("tags", base: 0.5) { $0.payload = .reqGetTags(.init()) }
        }
        #expect(c.patience.timeout(for: "tags/bridge", base: 0.5) == 0.5)
        // GetPubKey reached the device and was never answered: the sign-in
        // gives up within its own time, the connection says why, and the
        // next ask starts a new one rather than waiting on this.
        await until(5) { c.connectionFailed }
        #expect(c.connectionFailed)
        #expect(c.lastError == "The device didn't answer in time.")
        #expect(!device.received.isEmpty)
        #expect(Date().timeIntervalSince(started) < 4)
    }
}
}
