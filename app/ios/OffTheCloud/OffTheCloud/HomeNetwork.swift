// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  HomeNetwork.swift
//  OffTheCloud
//
//  Issue #190: at home every byte to the device used to go out to the
//  bridge and back in over the same home internet link. A device now also
//  listens on the home network over TLS with a self-signed certificate of
//  its own, and tells a signed-in owner where (GetLocalEndpoint: its
//  private addresses, the port, the SHA-256 of that certificate's DER).
//  This file keeps that answer, opens connections that trust only that
//  certificate, and races them; OTCConnection decides when, signs in over
//  the winner exactly as through the bridge, and falls back to the bridge.

import Foundation
import CryptoKit
import Network
import Security

enum HomeNetwork {
    /// How long the addresses get, all together, before the bridge.
    static let raceBudget: TimeInterval = 2.5

    /// What a network change or a return to the foreground does.
    enum Check: Equatable {
        case nothing
        /// On the home network: reconnect unless it still answers.
        case pingHome
        /// Through the endpoint: move home if the device answers there.
        case tryHome
    }

    /// Never while something is transferring - switching cuts the socket
    /// it is on - and never with mobile data alone, where no home address
    /// can answer.
    static func check(signedIn: Bool, onHome: Bool, busy: Bool, hasStored: Bool, onlyCellular: Bool) -> Check {
        guard signedIn, !busy else { return .nothing }
        if onHome { return .pingHome }
        return hasStored && !onlyCellular ? .tryHome : .nothing
    }
}

/// What GetLocalEndpoint answered, kept with the connection it was learned
/// on (Keychain `local_endpoint`).
struct LocalEndpoint: Codable, Equatable, Sendable {
    /// The normalized endpoint (SecretsStore.endpointURLString) of the
    /// sign-in that learned it: any other endpoint ignores it.
    let endpoint: String
    let addresses: [String]
    let port: Int
    /// SHA-256 of the device certificate's DER.
    let pin: Data

    init(endpoint: String, addresses: [String], port: Int, pin: Data) {
        self.endpoint = endpoint
        self.addresses = addresses
        self.port = port
        self.pin = pin
    }

    /// nil when the answer has nothing usable. Only what the device lists,
    /// home-network IP literals (RFC 1918, IPv6 ULA): a name would be
    /// looked up, anything else pasted into a URL could point it
    /// somewhere else, and a public address is no home network. A handful
    /// at most, each being a connection attempt at once.
    init?(_ m: Msg_LocalEndpoint, endpoint: String) {
        let addresses = Array(m.addresses.filter(Self.isHomeAddress).prefix(8))
        guard !addresses.isEmpty, (1...65535).contains(Int(m.port)), m.certSha256.count == 32 else { return nil }
        self.init(endpoint: endpoint, addresses: addresses, port: Int(m.port), pin: m.certSha256)
    }

    static func isHomeAddress(_ a: String) -> Bool {
        guard !a.isEmpty, a.allSatisfy({ $0.isHexDigit || $0 == "." || $0 == ":" }) else { return false }
        if let v4 = IPv4Address(a) {
            let b = [UInt8](v4.rawValue)
            return b[0] == 10 || (b[0] == 172 && b[1] & 0xf0 == 16) || (b[0] == 192 && b[1] == 168)
        }
        if let v6 = IPv6Address(a) {
            return v6.rawValue[v6.rawValue.startIndex] & 0xfe == 0xfc
        }
        return false
    }

    /// "<address>:<port>" for a URL, an IPv6 address in brackets.
    static func authority(_ address: String, port: Int) -> String {
        address.contains(":") ? "[\(address)]:\(port)" : "\(address):\(port)"
    }

    func socketURL(for address: String) -> URL? {
        URL(string: "wss://\(Self.authority(address, port: port))/ws")
    }

    // MARK: Storage

    private static let keychainKey = "local_endpoint"

    /// The endpoint stored for `endpoint`, if any. Keychain: read it off
    /// the main thread.
    static func stored(for endpoint: String) -> LocalEndpoint? {
        guard let json = Keychain.loadString(key: keychainKey),
              let ep = try? JSONDecoder().decode(LocalEndpoint.self, from: Data(json.utf8)),
              ep.endpoint == endpoint else { return nil }
        return ep
    }

    func save() {
        guard let json = try? JSONEncoder().encode(self) else { return }
        Keychain.saveString(key: Self.keychainKey, value: String(decoding: json, as: UTF8.self))
    }

    static func clear() {
        Keychain.delete(key: keychainKey)
    }

    /// Clears what is stored unless it was learned on `endpoint`: a saved
    /// connection to another device or address must not inherit it.
    static func clear(unlessFor endpoint: String) {
        guard let json = Keychain.loadString(key: keychainKey) else { return }
        let ep = try? JSONDecoder().decode(LocalEndpoint.self, from: Data(json.utf8))
        if ep?.endpoint != endpoint { clear() }
    }

    // MARK: GetLocalEndpoint's answer

    enum Update: Equatable {
        case store(LocalEndpoint)
        case clear
        /// Nothing the device said: keep what is stored.
        case keep
    }

    /// What to do with the device's answer to GetLocalEndpoint. A device
    /// from before this release answers unknown_payload, and one that
    /// can't be reached at home local_unavailable: both are forgotten.
    /// Anything else that isn't the endpoint (the bridge's own answers for
    /// a device it can't reach) keeps it. A network error never gets here
    /// and keeps it too.
    static func update(for resp: Msg_RespEnvelope, endpoint: String) -> Update {
        if !resp.error, case .respLocalEndpoint(let m) = resp.payload {
            return LocalEndpoint(m, endpoint: endpoint).map { .store($0) } ?? .clear
        }
        if resp.error, resp.errorCode == "unknown_payload" || resp.errorCode == "local_unavailable" {
            return .clear
        }
        return .keep
    }
}

/// The home-network address a connection went to, and what it was
/// learned from.
struct HomeLink: Equatable, Sendable {
    let endpoint: LocalEndpoint
    let address: String
}

enum CertificatePin {
    static func sha256(of cert: SecCertificate) -> Data {
        Data(SHA256.hash(data: SecCertificateCopyData(cert) as Data))
    }

    /// The only trust decision on the home network: the leaf certificate's
    /// DER must hash to `pin`. Its name, issuer and dates don't matter -
    /// the device's certificate is self-signed for an address no CA could
    /// vouch for, and the pin came over the signed-in session.
    static func leafMatches(_ trust: SecTrust, pin: Data) -> Bool {
        guard pin.count == 32,
              let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let leaf = chain.first else { return false }
        return sha256(of: leaf) == pin
    }
}

/// A URLSession that trusts exactly one certificate, by its pin. A
/// connection to anything else is cancelled during the TLS handshake,
/// before a byte of the request - let alone the password - is sent. One per
/// pin, never invalidated (a loader may still hold it), shared by the
/// WebSocket race and HomeMediaLoader.
final class PinnedSession: NSObject, @unchecked Sendable {
    let pin: Data
    /// Every callback of this session runs here, and HomeMediaLoader's
    /// resource-loader calls too, so they never run concurrently.
    let queue: DispatchQueue
    private(set) var session: URLSession!

    /// What a task wants to hear about, by task identifier.
    struct Handlers {
        var opened: (() -> Void)? = nil
        var response: ((URLResponse) -> URLSession.ResponseDisposition)? = nil
        var data: ((Data) -> Void)? = nil
        var completed: (Error?) -> Void = { _ in }
    }
    private let lock = NSLock()
    private var handlers: [Int: Handlers] = [:]

    private static let sessionsLock = NSLock()
    private static var sessions: [Data: PinnedSession] = [:]

    static func forPin(_ pin: Data) -> PinnedSession {
        sessionsLock.withLock {
            if let s = sessions[pin] { return s }
            let s = PinnedSession(pin: pin)
            sessions[pin] = s
            return s
        }
    }

    private init(pin: Data) {
        self.pin = pin
        queue = DispatchQueue(label: "cloud.off-the.OffTheCloud.home-network")
        super.init()
        let ops = OperationQueue()
        ops.underlyingQueue = queue
        ops.maxConcurrentOperationCount = 1
        let cfg = URLSessionConfiguration.ephemeral
        // Fail at once without a network: the race has a budget to keep.
        cfg.waitsForConnectivity = false
        cfg.urlCache = nil
        cfg.requestCachePolicy = .reloadIgnoringLocalCacheData
        session = URLSession(configuration: cfg, delegate: self, delegateQueue: ops)
    }

    /// Before `task.resume()`.
    func track(_ task: URLSessionTask, _ h: Handlers) {
        lock.withLock { handlers[task.taskIdentifier] = h }
    }

    private func handlers(for task: URLSessionTask, remove: Bool = false) -> Handlers? {
        lock.withLock { remove ? handlers.removeValue(forKey: task.taskIdentifier) : handlers[task.taskIdentifier] }
    }

    /// Opens wss://<address>:<port>/ws on every address at once. The first
    /// whose WebSocket handshake completes - so whose certificate matched
    /// the pin - wins and the others are cancelled; nil when none did
    /// within `budget`.
    func race(_ ep: LocalEndpoint, budget: TimeInterval = HomeNetwork.raceBudget) async -> (link: HomeLink, task: URLSessionWebSocketTask)? {
        let candidates: [(String, URLSessionWebSocketTask)] = ep.addresses.compactMap { a in
            guard let url = ep.socketURL(for: a) else { return nil }
            let t = session.webSocketTask(with: url)
            t.maximumMessageSize = WSClient.maxMessageSize
            return (a, t)
        }
        guard !candidates.isEmpty else { return nil }
        let race = SocketRace(tasks: candidates.map(\.1))
        return await withTaskCancellationHandler {
            await withCheckedContinuation { cont in
                race.start(cont)
                for (address, t) in candidates {
                    track(t, Handlers(
                        opened: { race.won((HomeLink(endpoint: ep, address: address), t)) },
                        completed: { _ in race.lost() }
                    ))
                }
                for (_, t) in candidates { t.resume() }
                queue.asyncAfter(deadline: .now() + budget) { race.end() }
            }
        } onCancel: {
            race.end()
        }
    }
}

extension PinnedSession: URLSessionWebSocketDelegate, URLSessionDataDelegate {
    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        decide(challenge, completionHandler)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        decide(challenge, completionHandler)
    }

    /// Nothing but the server-trust challenge is answered, and that only
    /// for the pinned certificate.
    private func decide(_ challenge: URLAuthenticationChallenge,
                        _ completionHandler: (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust,
              CertificatePin.leafMatches(trust, pin: pin) else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        completionHandler(.useCredential, URLCredential(trust: trust))
    }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        // From here on the socket is WSClient's; its receive loop sees the
        // end of it.
        handlers(for: webSocketTask, remove: true)?.opened?()
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
                    completionHandler: @escaping @Sendable (URLSession.ResponseDisposition) -> Void) {
        completionHandler(handlers(for: dataTask)?.response?(response) ?? .allow)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        handlers(for: dataTask)?.data?(data)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        handlers(for: task, remove: true)?.completed(error)
    }
}

/// One race's bookkeeping: the first winner resumes the caller, every
/// other socket is cancelled.
private final class SocketRace: @unchecked Sendable {
    typealias Winner = (link: HomeLink, task: URLSessionWebSocketTask)
    private let lock = NSLock()
    private let tasks: [URLSessionWebSocketTask]
    private var cont: CheckedContinuation<Winner?, Never>?
    private var failed = 0
    private var done = false

    init(tasks: [URLSessionWebSocketTask]) { self.tasks = tasks }

    func start(_ c: CheckedContinuation<Winner?, Never>) {
        // Cancelled before it started.
        let ended = lock.withLock { () -> Bool in
            if !done { cont = c }
            return done
        }
        if ended { c.resume(returning: nil) }
    }

    func won(_ w: Winner) {
        let c: CheckedContinuation<Winner?, Never>? = lock.withLock {
            guard !done else { return nil }
            done = true
            defer { cont = nil }
            return cont
        }
        guard let c else {
            // Too late: the budget ran out, or another address won.
            w.task.cancel()
            return
        }
        for t in tasks where t !== w.task { t.cancel() }
        c.resume(returning: w)
    }

    func lost() {
        let c: CheckedContinuation<Winner?, Never>? = lock.withLock {
            failed += 1
            guard !done, failed == tasks.count else { return nil }
            done = true
            defer { cont = nil }
            return cont
        }
        c?.resume(returning: nil)
    }

    /// The budget ran out, or the caller was cancelled.
    func end() {
        let (wasDone, c): (Bool, CheckedContinuation<Winner?, Never>?) = lock.withLock {
            let wasDone = done
            done = true
            defer { cont = nil }
            return (wasDone, cont)
        }
        guard !wasDone else { return }
        for t in tasks { t.cancel() }
        c?.resume(returning: nil)
    }
}

/// Issue #190: the network the phone is on. OTCConnection reconsiders its
/// route whenever it changes.
final class NetworkWatch: @unchecked Sendable {
    static let shared = NetworkWatch()

    private let monitor = NWPathMonitor()
    private let lock = NSLock()
    private var path: NWPath?
    private var started = false

    /// `onChange` runs on every change after the first path (the network
    /// at start, which is no change).
    func start(onChange: @escaping @Sendable () -> Void) {
        let first: Bool = lock.withLock {
            defer { started = true }
            return !started
        }
        guard first else { return }
        monitor.pathUpdateHandler = { [weak self] p in
            guard let self else { return }
            let hadOne = self.lock.withLock { () -> Bool in
                defer { self.path = p }
                return self.path != nil
            }
            if hadOne { onChange() }
        }
        monitor.start(queue: DispatchQueue(label: "cloud.off-the.OffTheCloud.network-watch"))
    }

    /// True when the phone's only way out is cellular: the home network
    /// can't be there, and each try would wait out the whole budget. Any
    /// Wi-Fi, Ethernet or VPN (which may route home) is worth a try, as is
    /// a path not known yet.
    var onlyCellular: Bool {
        guard let p = lock.withLock({ path }), p.status == .satisfied else { return false }
        let types = p.availableInterfaces.map(\.type)
        return !types.isEmpty && types.allSatisfy { $0 == .cellular }
    }
}

/// Issue #190: uploads, downloads and photo syncs in progress. Switching
/// route cuts the socket they are on, so a network change only switches
/// while there are none; otherwise the next natural reconnect does.
final class TransferActivity: @unchecked Sendable {
    static let shared = TransferActivity()
    private let lock = NSLock()
    private var count = 0

    var busy: Bool { lock.withLock { count > 0 } }
    func begin() { lock.withLock { count += 1 } }
    func end() { lock.withLock { count -= 1 } }
}
