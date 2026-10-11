// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  WSClient.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//

import Foundation

/// Whether a socket that went quiet is still there - the check made when
/// it is most likely to have died without a word: the app back in front, a
/// network change, a request whose answer didn't come in its time.
///
/// A connection can die without a word: the phone moves between networks,
/// a mobile link drops, and no reset ever arrives. Then the socket is
/// pinged; one that stays silent for `pongWait` more (OkHttp's 30 s) is
/// taken for dead: what waits on it fails, and the owner connects again.
///
/// Only with no request waiting for its answer. A missing pong says nothing
/// while an answer may be on its way: the device and the bridge send a big
/// answer as one message, in 64 and 256 KiB frames, and the bridge (gorilla,
/// its default ping handler) drops a pong it can't write within a second,
/// which it can't while a frame is being written to a link that is slower
/// than ~2 Mbit/s. URLSession tells nothing either while a message is on
/// its way: a socket counts as heard only once a whole message has come,
/// and countOfBytesReceived stays 0 for a WebSocket task. Pinging while a
/// 4 MiB answer came in at 32 KB/s took the socket for dead after 45 s, and
/// every retry the same: ReadFile pieces, thumbnails and slow pages never
/// came. So while anything waits, the requests' own times decide (a
/// screen's 2 minutes or more, everything else WSClient.defaultTimeout);
/// once one runs out and nothing else waits, the socket is checked at once.
///
/// A check whose ping went out with nothing waiting is decisive: the
/// answers of requests sent after it come after its pong, so a pong that
/// doesn't come means a socket that is gone (or a late answer to a request
/// that already timed out still coming in, which counts as heard).
///
/// Plain values, so the decisions can be tested without a socket.
struct SocketLiveness {
    struct Timing: Sendable {
        /// How long a check's ping may go unanswered (OkHttp's 30 s).
        var pongWait: TimeInterval = 30
    }

    enum Action: Equatable { case none, ping, dead }

    let timing: Timing
    /// When the socket was last heard from: opened, a message, a pong; nil
    /// until it has opened (connecting has its own limit, WSClient.connect).
    private(set) var heard: Date?
    /// A check's ping went out then (with nothing waiting) and nothing has
    /// been heard since.
    private(set) var pingSentAt: Date?
    /// A check asked for: made at the first tick with nothing waiting.
    private var checkAsked = false

    init(timing: Timing, heard: Date? = nil) {
        self.timing = timing
        self.heard = heard
    }

    /// Whether a tick still has something to decide.
    var checking: Bool { pingSentAt != nil || checkAsked }

    mutating func heard(at now: Date) {
        if heard == nil || heard! < now { heard = now }
        if let p = pingSentAt, now >= p { pingSentAt = nil }
    }

    /// Check the socket as soon as nothing waits on it: the app came back
    /// to the front, the network changed, a request's time ran out.
    mutating func check() {
        checkAsked = true
    }

    /// `waiting`: requests waiting for their answers now.
    mutating func tick(at now: Date, waiting: Int) -> Action {
        guard heard != nil else { return .none }
        if let p = pingSentAt {
            return now >= p + timing.pongWait ? .dead : .none
        }
        if checkAsked && waiting == 0 {
            checkAsked = false
            pingSentAt = now
            return .ping
        }
        return .none
    }
}

/// An `actor` (not a plain class) because URLSession delivers `receive`/`send`
/// completions on its own background queue, not on whatever thread called
/// into this client. With a plain class those callbacks mutated `waiters`/
/// `nextId` concurrently with calls made from OTCConnection, a real data
/// race that showed up in practice as a heap-corruption crash on a real
/// device once traffic got busy (large file listings). The actor serializes
/// every access to this state, callbacks included.
actor WSClient {
    /// Thrown to a request whose answer didn't come in the time it was
    /// given (Android's WSClient.RequestTimeout). The socket may be fine:
    /// it is checked once nothing else waits on it (SocketLiveness), and
    /// one that is gone fails what waits on it as Unresponsive. Its answer,
    /// should it still come, is dropped - nothing waits for its id any
    /// more, and ids are never used again on a socket.
    struct TimedOut: LocalizedError {
        /// false: the time ran out before the request was sent - still
        /// connecting (OTCConnection) - which says nothing about how fast
        /// the device answers (Patience).
        var answerTimedOut = true

        var errorDescription: String? {
            answerTimedOut ? "The device didn't answer in time." : "Still connecting to the device."
        }
    }

    /// Thrown to every request waiting on a socket that stopped answering
    /// (SocketLiveness), which is closed: the owner connects again.
    struct Unresponsive: LocalizedError {
        var errorDescription: String? { "The connection to the device stopped answering." }
    }

    /// The WebSocket upgrade wasn't answered within connectTimeout
    /// (Android's WSClient.ConnectTimeout).
    struct ConnectTimeout: LocalizedError {
        var errorDescription: String? { "The device didn't answer the connection in time." }
    }

    /// A request's answer, and how long it was waited for once the request
    /// had left the phone (Patience steps back down by it).
    struct Answer: Sendable {
        let resp: Msg_RespEnvelope
        let waited: TimeInterval
    }

    struct Timing: Sendable {
        var liveness = SocketLiveness.Timing()
        /// How often deadlines and the socket are looked at while anything waits.
        var tick: TimeInterval = 1
    }

    /// How long a connect may take, the WebSocket upgrade included
    /// (Android's CONNECT_TIMEOUT_MS). The session waits for connectivity
    /// and has no limit of its own on the upgrade's answer, so a bridge node
    /// or device that took the connection and never answered left every
    /// request waiting behind the sign-in for good.
    static let connectTimeout: TimeInterval = 20

    /// The time a request gets when it is given none (Android's
    /// DEFAULT_TIMEOUT_MS, as the macOS client and otc-sync): long enough
    /// for a 4 MiB chunk on a slow link and ApplyUpdate, but a request on a
    /// socket that is gone no longer waits for good. What a screen shows
    /// asks with less (OTCConnection.ask).
    static let defaultTimeout: TimeInterval = 30 * 60

    private struct Waiter {
        let resume: (Result<Answer, Error>) -> Void
        /// Its time, counted from `sentAt`.
        let limit: TimeInterval
        /// When the socket took it (the send's completion): its clock
        /// starts then. nil while it is still queued behind what was sent
        /// before it (a photo sync's chunks): the device can't answer what
        /// it hasn't got.
        var sentAt: Date?
    }

    private var task: URLSessionWebSocketTask?
    private let session: URLSession
    private let opens = OpenWatch()
    private let timing: Timing
    private(set) var connected = false
    /// The socket came from adopt(): the home network's, never reused to
    /// reach the endpoint.
    private var adopted = false
    private var nextId: Int32 = 1
    private var waiters = [Int32: Waiter]()
    private var live: SocketLiveness
    /// Looks at the deadlines and the socket while anything waits.
    private var watch: Task<Void, Never>?
    /// A connect waiting for its socket to open.
    private var opening: (task: URLSessionWebSocketTask, resume: (Result<Void, Error>) -> Void)?

    /// Fired once, from the receive loop, when the socket breaks (read error
    /// or clean close), when it stopped answering, or when it didn't open
    /// in time. The owner (OTCConnection) uses this to mark itself
    /// unauthenticated and schedule a reconnect; WSClient itself does not
    /// retry on its own.
    var onDisconnect: (() -> Void)?

    init(timing: Timing = Timing()) {
        let cfg = URLSessionConfiguration.default
        cfg.waitsForConnectivity = true
        session = URLSession(configuration: cfg, delegate: opens, delegateQueue: nil)
        self.timing = timing
        live = SocketLiveness(timing: timing.liveness)
    }

    func setOnDisconnect(_ cb: @escaping () -> Void) {
        onDisconnect = cb
    }

    /// The default is 1 MiB, which a file listing for a few thousand
    /// photos blows straight past — every receive then fails with
    /// "Message too long" and the connection never gets anywhere. Match
    /// the macOS client's generous cap.
    static let maxMessageSize = 1000 * 1024 * 1024

    /// Opens the socket and waits for its upgrade to be answered: within
    /// `limit`, else ConnectTimeout - a failed connect like any other (the
    /// socket goes, onDisconnect fires). From then on it counts as heard
    /// from, so a check (verify) can tell it is gone.
    func connect(url: URL, within limit: TimeInterval = WSClient.connectTimeout) async throws {
        if let task, task.state == .running, !adopted, connected { return }
        if task != nil { close() }
        let t = session.webSocketTask(with: url)
        t.maximumMessageSize = Self.maxMessageSize
        task = t
        adopted = false
        connected = false
        live = SocketLiveness(timing: timing.liveness)
        opens.watch(t) { [weak self] in
            Task { await self?.opened(t) }
        }
        let limitTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(limit))
            guard !Task.isCancelled else { return }
            await self?.openTimedOut(t)
        }
        defer { limitTask.cancel() }
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            opening = (t, { cont.resume(with: $0) })
            t.resume()
            listen(on: t)
        }
    }

    private func opened(_ t: URLSessionWebSocketTask) {
        guard t === task, let o = opening, o.task === t else { return }
        opening = nil
        connected = true
        live = SocketLiveness(timing: timing.liveness, heard: Date())
        o.resume(.success(()))
    }

    private func openTimedOut(_ t: URLSessionWebSocketTask) {
        guard t === task, let o = opening, o.task === t else { return }
        print("[ws] the connection wasn't answered in \(Self.connectTimeout) s")
        failAndClose(ConnectTimeout())
    }

    /// Issue #190: close(), only while `t` is still the socket in use.
    func close(ifCurrent t: URLSessionWebSocketTask) {
        if task === t { close() }
    }

    /// Issue #190: waits up to `seconds` for no request to be waiting for
    /// its answer; whether none is. A route switch replaces the socket,
    /// and a request it cuts off is sent again on the new one, after the
    /// device may have acted on it already. A socket that breaks fails its
    /// requests, so it is idle too.
    func waitIdle(within seconds: TimeInterval) async -> Bool {
        let deadline = Date() + seconds
        while !waiters.isEmpty {
            guard Date() < deadline else { return false }
            do { try await Task.sleep(for: .milliseconds(50)) } catch { return false }
        }
        return true
    }

    /// Issue #190: takes over a socket whose handshake has already
    /// completed (the home network's, from PinnedSession.race) in place of
    /// whatever this held. What is still waiting on a live old socket gets
    /// up to `drain` seconds to be answered there first.
    func adopt(_ t: URLSessionWebSocketTask, drain: TimeInterval = 0) async {
        if drain > 0 { _ = await waitIdle(within: drain) }
        close()
        task = t
        adopted = true
        connected = true
        // Its handshake was answered: it has been heard from.
        live = SocketLiveness(timing: timing.liveness, heard: Date())
        listen(on: t)
    }

    /// Issue #190: whether the socket still answers, after a network
    /// change that may have taken the home network away under it.
    func ping(within seconds: TimeInterval) async -> Bool {
        guard connected, let t = task else { return false }
        let once = PingOnce()
        let ok = await withCheckedContinuation { cont in
            once.start(cont)
            t.sendPing { error in once.finish(error == nil) }
            DispatchQueue.global().asyncAfter(deadline: .now() + seconds) { once.finish(false) }
        }
        if ok, t === task { live.heard(at: Date()) }
        return ok
    }

    /// Checks that the socket still answers, even with nothing waiting on
    /// it: the app came back to the front, or the network changed - when a
    /// socket most often dies without a word. One that doesn't answer is
    /// closed and reported (onDisconnect), so the next request goes out on
    /// a new one instead of waiting on it. Made once nothing waits on it
    /// (SocketLiveness).
    func verify() {
        guard connected, task != nil else { return }
        live.check()
        ensureWatch()
    }

    /// Fails what is still waiting here itself: the cancelled socket's
    /// late receive failure is ignored now (see handleReceive), and it is
    /// what used to fail them. No onDisconnect - the caller already deals
    /// with the state.
    func close() {
        task?.cancel()
        task = nil
        connected = false
        live = SocketLiveness(timing: timing.liveness)
        if let o = opening {
            opening = nil
            o.resume(.failure(URLError(.cancelled)))
        }
        let pending = waiters
        waiters.removeAll()
        for (_, w) in pending { w.resume(.failure(URLError(.cancelled))) }
    }

    /// Tied to one socket: a callback from a socket that has since been
    /// closed or replaced must not act on the current one.
    private func listen(on t: URLSessionWebSocketTask) {
        t.receive { [weak self] result in
            guard let self else { return }
            Task { await self.handleReceive(result, from: t) }
        }
    }

    private func handleReceive(_ result: Result<URLSessionWebSocketTask.Message, Error>, from t: URLSessionWebSocketTask) {
        // A cancelled socket's receive completes later with an error; on
        // the current connection that tore down the new socket and failed
        // its handshake.
        guard t === task else { return }
        switch result {
        case .failure(let err):
            print("WS receive error:", err)
            failAndClose(err)
        case .success(let message):
            deliver(message, from: t)
            // keep listening only while the socket is still healthy
            listen(on: t)
        }
    }

    private func deliver(_ message: URLSessionWebSocketTask.Message, from t: URLSessionWebSocketTask) {
        guard t === task else { return }
        let now = Date()
        live.heard(at: now)
        switch message {
        case .data(let data):
            do {
                let env = try Msg_RespEnvelope(serializedData: data)
                // An answer nobody waits for any more - its request
                // timed out, or the socket it went on was replaced -
                // is dropped here: ids are never reused.
                if let w = waiters.removeValue(forKey: env.id) {
                    w.resume(.success(Answer(resp: env, waited: w.sentAt.map { now.timeIntervalSince($0) } ?? 0)))
                }
                // If you expect server push, also post a Notification here.
            } catch {
                print("Decode error:", error)
            }
        default: break
        }
    }

    /// Marks the connection dead, fails every outstanding request (and a
    /// connect waiting for the socket to open), and notifies the owner so
    /// it can reconnect. Does NOT call listen() again — a fresh connect()
    /// starts a new receive loop.
    private func failAndClose(_ error: Error) {
        connected = false
        if let o = opening {
            opening = nil
            o.resume(.failure(error))
        }
        let pending = waiters
        waiters.removeAll()
        for (_, w) in pending { w.resume(.failure(error)) }
        task?.cancel(with: .abnormalClosure, reason: nil)
        // Its late receive failure is ignored from here on (handleReceive):
        // a socket the watchdog gave up on would otherwise report twice.
        task = nil
        live = SocketLiveness(timing: timing.liveness)
        onDisconnect?()
    }

    /// Sends a request and waits for its answer: `timeout` seconds at most
    /// (nil: defaultTimeout), counted from when the socket took it, then
    /// TimedOut.
    func request(timeout: TimeInterval? = nil, build: (inout Msg_ReqEnvelope) -> Void) async throws -> Msg_RespEnvelope {
        try await exchange(timeout: timeout, build: build).resp
    }

    /// request(), with how long the answer took. The clock starts once the
    /// request has left the phone - the send's completion, when URLSession
    /// has handed its bytes to the socket - as Android's does once OkHttp
    /// has written it: on the one socket it may queue behind a photo sync's
    /// chunks (three of 4 MiB over a 64 KB/s uplink: three minutes), and
    /// timed from the call, a page "took too long" and was asked again
    /// before it ever reached the device.
    func exchange(timeout: TimeInterval? = nil, build: (inout Msg_ReqEnvelope) -> Void) async throws -> Answer {
        guard connected, let t = task else {
            throw NSError(domain: "ws", code: -1, userInfo: [NSLocalizedDescriptionKey: "Not connected"])
        }
        var env = Msg_ReqEnvelope()
        let id = nextId; nextId &+= 1
        env.id = id
        build(&env)
        // Re-assert the id after build() rather than trusting it survived:
        // this is issue #77's actual "stuck loading" bug. Most call sites'
        // closures do `var req = ReqEnvelope(); ...; e = req`, replacing
        // the whole envelope - id included - with a fresh one whose id
        // defaults to 0, instead of mutating the passed-in `e` in place.
        // Every request built that way was silently going out on the wire
        // as id=0, so any two of them in flight at once collided on the
        // same waiters[0] slot: the second registration overwrote the
        // first's, and when the first request's actual response
        // eventually arrived, waiters[0] pointed at someone else's
        // callback, leaving the first one's continuation waiting forever.
        // Confirmed live via the Swift runtime's own "leaked its
        // continuation" diagnostic and by literally logging "request
        // id=0" for nearly every request. Restoring the real id here
        // fixes every call site at once, rather than relying on dozens of
        // them to each preserve it correctly on their own.
        env.id = id
        // Localization (docs/i18n.md): the language the app shows, on every
        // request - set here alone, after build() for the same reason as the
        // id. The device writes its replies in it.
        env.lang = L10n.shared.code

        //print("Sending request: \(env)")

        let data = try env.serializedData()
        let limit = timeout ?? Self.defaultTimeout
        return try await withCheckedThrowingContinuation { cont in
            self.waiters[id] = Waiter(resume: { result in cont.resume(with: result) }, limit: limit, sentAt: nil)
            self.ensureWatch()
            t.send(.data(data)) { error in
                Task { await self.sent(id, on: t, error: error) }
            }
        }
    }

    private func sent(_ id: Int32, on t: URLSessionWebSocketTask, error: Error?) {
        guard t === task else { return }
        if let error {
            failWaiter(id: id, error: error)
            return
        }
        // Its clock starts now (exchange).
        waiters[id]?.sentAt = Date()
    }

    private func failWaiter(id: Int32, error: Error) {
        if let w = waiters.removeValue(forKey: id) {
            w.resume(.failure(error))
        }
    }

    // MARK: Deadlines and liveness

    private func ensureWatch() {
        guard watch == nil else { return }
        let tick = timing.tick
        watch = Task { [weak self] in
            while true {
                try? await Task.sleep(for: .seconds(tick))
                guard let self, await self.watchTick() else { return }
            }
        }
    }

    /// One look at the deadlines and the socket; false once nothing is
    /// left to watch (the loop ends, and the next request starts another).
    private func watchTick() -> Bool {
        let now = Date()
        var expired = false
        for (id, w) in waiters where w.sentAt.map({ now >= $0 + w.limit }) ?? false {
            waiters.removeValue(forKey: id)
            expired = true
            print("[ws] request \(id) got no answer in time")
            w.resume(.failure(TimedOut()))
        }
        // No answer in its time: the socket may be gone. Checked once
        // nothing else waits on it - in this same tick when nothing does,
        // so the check's ping goes out before the screen asks again.
        if expired { live.check() }
        switch live.tick(at: now, waiting: waiters.count) {
        case .none:
            break
        case .ping:
            sendLivenessPing()
        case .dead:
            print("[ws] the connection stopped answering; closing it")
            failAndClose(Unresponsive())
        }
        guard task != nil, !waiters.isEmpty || live.checking else {
            watch = nil
            return false
        }
        return true
    }

    private func sendLivenessPing() {
        guard let t = task else { return }
        t.sendPing { [weak self] error in
            guard error == nil, let self else { return }
            Task { await self.pong(from: t) }
        }
    }

    private func pong(from t: URLSessionWebSocketTask) {
        guard t === task else { return }
        live.heard(at: Date())
    }
}

/// The session's delegate, for one thing: when a socket's upgrade was
/// answered (WSClient.connect waits for it).
private final class OpenWatch: NSObject, URLSessionWebSocketDelegate, @unchecked Sendable {
    private let lock = NSLock()
    private var waiting: [Int: () -> Void] = [:]

    /// Before `task.resume()`.
    func watch(_ task: URLSessionTask, opened: @escaping () -> Void) {
        lock.withLock { waiting[task.taskIdentifier] = opened }
    }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        let opened = lock.withLock { waiting.removeValue(forKey: webSocketTask.taskIdentifier) }
        opened?()
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        // Never opened (refused, cancelled): its receive loop reports it.
        lock.withLock { _ = waiting.removeValue(forKey: task.taskIdentifier) }
    }
}

/// Resumes a ping's wait once: with the pong, or false at the deadline.
private final class PingOnce: @unchecked Sendable {
    private let lock = NSLock()
    private var cont: CheckedContinuation<Bool, Never>?

    func start(_ c: CheckedContinuation<Bool, Never>) { lock.withLock { cont = c } }

    func finish(_ ok: Bool) {
        let c: CheckedContinuation<Bool, Never>? = lock.withLock {
            defer { cont = nil }
            return cont
        }
        c?.resume(returning: ok)
    }
}
