// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Network
import Security
import Testing
@testable import OffTheCloud

/// The public demo device (DemoDevice.swift, docs/demo-device.md): what its
/// answers say, the flag the app keeps, and its refusals shown in its words.
struct DemoDeviceTests {
    private static func pubKey(demo: Bool, password: String) -> Msg_RespEnvelope {
        var k = Msg_PubKey()
        k.publicKey = Data([1, 2, 3])
        k.demo = demo
        k.demoPassword = password
        var r = Msg_RespEnvelope()
        r.payload = .respPubKey(k)
        return r
    }

    @Test func itsAddressIsTheNameDemoOnTheBridge() {
        #expect(DemoDevice.endpoint == "wss://demo.off-the.cloud/ws")
        #expect(SecretsStore.bridgeName(fromEndpoint: DemoDevice.endpoint) == "demo")
    }

    @Test func onlyADemoDeviceWithAPasswordSignsIn() throws {
        #expect(try DemoDevice.password(in: Self.pubKey(demo: true, password: "look-around")).get() == "look-around")
        // A demo device that publishes no password, or any other device.
        for resp in [Self.pubKey(demo: true, password: ""), Self.pubKey(demo: false, password: ""), Self.pubKey(demo: false, password: "x")] {
            guard case .failure(.notDemo) = DemoDevice.password(in: resp) else {
                Issue.record("\(resp)")
                continue
            }
        }
    }

    @Test func theBridgesAnswerSaysWhyInItsWords() {
        var ack = Msg_Ack()
        ack.ok = false
        ack.code = "device_unreachable"
        ack.errorMsg = "The device is not connected right now"
        var resp = Msg_RespEnvelope()
        resp.payload = .respAck(ack)
        guard case .failure(let f) = DemoDevice.password(in: resp) else {
            Issue.record("signed in")
            return
        }
        #expect(f.detail == "The device is not connected right now")
        // No words, or no answer the app knows: no key, as at a sign-in.
        ack.errorMsg = ""
        resp.payload = .respAck(ack)
        guard case .failure(.unreachable(.noPublicKey)) = DemoDevice.password(in: resp) else {
            Issue.record("\(resp)")
            return
        }
        guard case .failure(.unreachable(.noPublicKey)) = DemoDevice.password(in: Msg_RespEnvelope()) else {
            Issue.record("an empty answer")
            return
        }
        #expect(DemoDevice.Failure.notDemo.detail == nil)
    }

    @Test func theFlagIsKeptUntilForgotten() {
        let defaults = UserDefaults(suiteName: "demo-\(UUID().uuidString)")!
        #expect(!DemoDevice.stored(defaults))
        DemoDevice.store(true, defaults)
        #expect(DemoDevice.stored(defaults))
        DemoDevice.store(false, defaults)
        #expect(!DemoDevice.stored(defaults))
        #expect(defaults.object(forKey: DemoDevice.storedKey) == nil)
    }

    @Test func aDemoRefusalIsToldByItsCodeOnTheEnvelopeOrTheAck() {
        // As the device sends it: both (websocket/demo.go's demoRefusal).
        var both = Msg_RespEnvelope()
        both.error = true
        both.errorCode = "demo"
        both.errorMessage = "Esto es un dispositivo de demostración: los cambios están desactivados."
        var ack = Msg_Ack()
        ack.ok = false
        ack.code = "demo"
        ack.errorMsg = both.errorMessage
        both.payload = .respAck(ack)
        #expect(ErrorCodes.isDemo(both))
        #expect(ErrorCodes.demoRefusal(both) == both.errorMessage)
        // The Ack alone.
        var onlyAck = Msg_RespEnvelope()
        onlyAck.payload = .respAck(ack)
        #expect(ErrorCodes.isDemo(onlyAck))
        #expect(ErrorCodes.demoRefusal(onlyAck) == ack.errorMsg)
        // Another refusal, a message without the code, an Ack that is ok.
        var other = Msg_RespEnvelope()
        other.error = true
        other.errorCode = "upload_only"
        other.errorMessage = "demo"
        #expect(!ErrorCodes.isDemo(other))
        #expect(ErrorCodes.demoRefusal(other) == nil)
        var fine = Msg_Ack()
        fine.ok = true
        fine.code = "demo"
        var ok = Msg_RespEnvelope()
        ok.payload = .respAck(fine)
        #expect(!ErrorCodes.isDemo(ok))
    }
}

/// Refusals the demo device makes, shown in its words where a control
/// could still reach it.
@MainActor
struct DemoRefusalTests {
    private static let words = "This is a demo device: changes are turned off."

    private static func refusal() -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        r.error = true
        r.errorCode = "demo"
        r.errorMessage = words
        var a = Msg_Ack()
        a.ok = false
        a.code = "demo"
        a.errorMsg = words
        r.payload = .respAck(a)
        return r
    }

    @Test func filesDeleteSaysWhyOnceAndStops() async {
        let vm = FilesExplorerViewModel(initialPath: "/Docs/", defaults: UserDefaults(suiteName: "demo-\(UUID().uuidString)")!)
        vm.thumbDisk = testThumbCache()
        var deletes = 0
        vm.request = { payload in
            switch payload {
            case .reqDelFile:
                deletes += 1
                return Self.refusal()
            case .reqListFiles:
                var r = Msg_RespEnvelope()
                r.payload = .respListOfFiles(Msg_ListOfFiles())
                return r
            default:
                throw CancellationError()
            }
        }
        vm.announce = { _ in }
        vm.selected = ["/Docs/a.txt", "/Docs/b.txt"]
        await vm.deleteSelected()
        #expect(deletes == 1)
        #expect(vm.toast == Self.words)
    }

    @Test func aPersonsNameRefusedSaysWhy() async {
        let vm = PhotoGalleryVM()
        var p = Msg_Person()
        p.id = "a"
        vm.allPeople = [p]
        vm.peopleRequest = { _, _ in Self.refusal() }
        #expect(await vm.renamePerson("a", to: "Ana") == .refused(Self.words))
        #expect(vm.allPeople.first?.name == "")
        #expect(PeopleNote.refused(Self.words).text == Self.words)
        // Any other refusal keeps its own words ("Name not saved").
        vm.peopleRequest = { _, _ in
            var r = Msg_RespEnvelope()
            r.payload = .respAck(Msg_Ack())
            return r
        }
        #expect(await vm.renamePerson("a", to: "Ana") == .refused(""))
    }
}

/// A device on this Mac's loopback, for Try the demo and the sign-in:
/// answers each request with `answer`'s envelope (its id filled in), or
/// nothing when it gives none. `hangsUp`: it drops every connection as it
/// comes, before the WebSocket is up.
private final class DemoStandIn: @unchecked Sendable {
    typealias Answer = @Sendable (Msg_ReqEnvelope) -> Msg_RespEnvelope?

    private let listener: NWListener
    private let queue = DispatchQueue(label: "test-demo-device")
    private let answer: Answer
    private let lock = NSLock()
    private var _asked: [Msg_ReqEnvelope] = []
    var asked: [Msg_ReqEnvelope] { lock.withLock { _asked } }

    init(hangsUp: Bool = false, answer: @escaping Answer) throws {
        let ws = NWProtocolWebSocket.Options()
        ws.autoReplyPing = true
        let params = NWParameters.tcp
        params.defaultProtocolStack.applicationProtocols.insert(ws, at: 0)
        params.requiredLocalEndpoint = NWEndpoint.hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: params)
        self.answer = answer
        listener.newConnectionHandler = { [weak self] conn in
            guard let self else { return }
            if hangsUp {
                conn.cancel()
                return
            }
            conn.start(queue: self.queue)
            self.receive(on: conn)
        }
    }

    func start() async throws -> URL {
        let started = StartOnce()
        let port: UInt16 = try await withCheckedThrowingContinuation { cont in
            listener.stateUpdateHandler = { [listener] state in
                switch state {
                case .ready:
                    if started.first() { cont.resume(returning: listener.port?.rawValue ?? 0) }
                case .failed(let e):
                    if started.first() { cont.resume(throwing: e) }
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
                self.lock.withLock { self._asked.append(env) }
                if var resp = self.answer(env) {
                    resp.id = env.id
                    if let out = try? resp.serializedData() {
                        let ctx = NWConnection.ContentContext(identifier: "answer", metadata: [NWProtocolWebSocket.Metadata(opcode: .binary)])
                        conn.send(content: out, contentContext: ctx, isComplete: true, completion: .contentProcessed { _ in })
                    }
                }
            }
            if error == nil { self.receive(on: conn) }
        }
    }
}

private final class StartOnce: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func first() -> Bool { lock.withLock { defer { done = true }; return !done } }
}

/// An RSA key the stand-in hands out as the device does (SPKI DER, what
/// Go's MarshalPKIXPublicKey writes), and opens the sealed password with.
private struct StandInKey: @unchecked Sendable {
    let privateKey: SecKey
    let spki: Data

    init() throws {
        let attrs: [CFString: Any] = [kSecAttrKeyType: kSecAttrKeyTypeRSA, kSecAttrKeySizeInBits: 2048]
        var error: Unmanaged<CFError>?
        guard let key = SecKeyCreateRandomKey(attrs as CFDictionary, &error),
              let pub = SecKeyCopyPublicKey(key),
              let pkcs1 = SecKeyCopyExternalRepresentation(pub, &error) as Data? else {
            throw error!.takeRetainedValue() as Error
        }
        privateKey = key
        // SEQUENCE { AlgorithmIdentifier rsaEncryption NULL, BIT STRING { pkcs1 } }
        let algorithm = Data([0x30, 0x0d, 0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x01, 0x05, 0x00])
        let bits = Self.der(0x03, Data([0x00]) + pkcs1)
        spki = Self.der(0x30, algorithm + bits)
    }

    func open(_ sealed: Data) -> String? {
        guard let plain = SecKeyCreateDecryptedData(privateKey, .rsaEncryptionOAEPSHA256, sealed as CFData, nil) as Data? else { return nil }
        return String(data: plain, encoding: .utf8)
    }

    private static func der(_ tag: UInt8, _ body: Data) -> Data {
        var out = Data([tag])
        let n = body.count
        if n < 0x80 {
            out.append(UInt8(n))
        } else if n <= 0xff {
            out.append(contentsOf: [0x81, UInt8(n)])
        } else {
            out.append(contentsOf: [0x82, UInt8(n >> 8), UInt8(n & 0xff)])
        }
        return out + body
    }
}

/// Try the demo's question, over a socket of its own.
@MainActor
struct DemoProbeTests {
    @Test func aDemoDeviceAnswersItsPublishedPassword() async throws {
        let device = try DemoStandIn { req in
            guard case .reqGetPubKey = req.payload else { return nil }
            var k = Msg_PubKey()
            k.publicKey = Data([1])
            k.demo = true
            k.demoPassword = "look-around"
            var r = Msg_RespEnvelope()
            r.payload = .respPubKey(k)
            return r
        }
        let url = try await device.start()
        defer { device.stop() }
        let password = try await OTCConnection.shared.demoPassword(at: url)
        #expect(password == "look-around")
        // Only the key was asked for: no sign-in on the probe's socket.
        #expect(device.asked.count == 1)
    }

    @Test func anotherDeviceIsNotTheDemo() async throws {
        let device = try DemoStandIn { req in
            guard case .reqGetPubKey = req.payload else { return nil }
            var k = Msg_PubKey()
            k.publicKey = Data([1])
            var r = Msg_RespEnvelope()
            r.payload = .respPubKey(k)
            return r
        }
        let url = try await device.start()
        defer { device.stop() }
        do {
            _ = try await OTCConnection.shared.demoPassword(at: url)
            Issue.record("signed in to a device that isn't the demo")
        } catch DemoDevice.Failure.notDemo {
        } catch {
            Issue.record("\(error)")
        }
    }

    @Test func noDeviceThereCantConnect() async throws {
        let gone = try DemoStandIn(hangsUp: true) { _ in nil }
        let url = try await gone.start()
        defer { gone.stop() }
        do {
            _ = try await OTCConnection.shared.demoPassword(at: url)
            Issue.record("signed in to nothing")
        } catch DemoDevice.Failure.unreachable(let problem) {
            #expect(!problem.text.isEmpty)
        } catch {
            Issue.record("\(error)")
        }
    }
}

// The app's own connection: never beside the other suites that sign it in
// (StalledSignInTests) nor the Images models a sign-in wakes.
extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct DemoSignInTests {
    /// Try the demo's password signs in as a typed one does - sealed to the
    /// connection's key - and the connection then knows it is the demo,
    /// which the photo sync asks before doing anything; Status and Log Out
    /// say otherwise.
    @Test func theDemosSignInIsKeptOnTheConnection() async throws {
        // Never on a simulator signed in to a device: this test configures
        // the app's own connection (a fresh test simulator has none).
        guard SecretsStore.loadOrCreate().endpoint.isEmpty else { return }
        let key = try StandInKey()
        let opened = OpenedPasswords()
        let device = try DemoStandIn { req in
            var r = Msg_RespEnvelope()
            switch req.payload {
            case .reqGetPubKey:
                var k = Msg_PubKey()
                k.publicKey = key.spki
                k.demo = true
                k.demoPassword = "look-around"
                r.payload = .respPubKey(k)
            case .reqAuth(let auth):
                opened.add(key.open(auth.key))
                var a = Msg_Ack()
                a.ok = true
                r.payload = .respAck(a)
            case .reqGetLocalEndpoint:
                r.error = true
                r.errorCode = "local_unavailable"
            default:
                var a = Msg_Ack()
                a.ok = true
                r.payload = .respAck(a)
            }
            return r
        }
        let url = try await device.start()
        defer { device.stop() }
        let c = OTCConnection.shared
        c.reset()
        let password = try await c.demoPassword(at: url)
        let s = SecretsStore.loadOrCreate()
        s.endpoint = url.absoluteString
        s.password = password
        s.persist()
        defer {
            let s = SecretsStore.loadOrCreate()
            s.endpoint = ""
            s.password = ""
            s.persist()
            c.reset()
        }
        #expect(!c.isDemo)
        #expect(!(await PhotoSync.shared.refusesDevice()))

        try await c.ensureConnected()
        #expect(opened.list == ["look-around"])
        #expect(c.isDemo)
        #expect(DemoDevice.stored())
        #expect(await PhotoSync.shared.refusesDevice())

        // A Status that doesn't say so (the device out of demo mode).
        c.deviceSaid(status: Msg_Status())
        #expect(!c.isDemo)
        #expect(!DemoDevice.stored())
        var demoStatus = Msg_Status()
        demoStatus.demo = true
        c.deviceSaid(status: demoStatus)
        #expect(c.isDemo)

        // Log Out forgets it.
        c.reset()
        #expect(!c.isDemo)
        #expect(!DemoDevice.stored())
        #expect(!(await PhotoSync.shared.refusesDevice()))
    }
}
}

private final class OpenedPasswords: @unchecked Sendable {
    private let lock = NSLock()
    private var _list: [String] = []
    var list: [String] { lock.withLock { _list } }
    func add(_ p: String?) { lock.withLock { _list.append(p ?? "<not opened>") } }
}
