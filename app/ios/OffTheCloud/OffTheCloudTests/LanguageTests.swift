// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  LanguageTests.swift
//  OffTheCloudTests
//
//  The stored choice of language (docs/i18n.md): resolution, the device's
//  answers, the 30 s window, the copy kept through Log Out, and the lang
//  every request carries. Android's LanguageSettingsTest.kt is the same.
//

import Foundation
import Network
import Testing
@testable import OffTheCloud

/// The seven languages, as a build that ships them all would list them,
/// plus the pseudo-locale of draft builds.
private let seven: [L10nLanguage] = [
    L10nLanguage(code: "en", tag: "en", apple: "en", name: "English", pseudo: false),
    L10nLanguage(code: "es", tag: "es", apple: "es", name: "Español", pseudo: false),
    L10nLanguage(code: "fr", tag: "fr", apple: "fr", name: "Français", pseudo: false),
    L10nLanguage(code: "de", tag: "de", apple: "de", name: "Deutsch", pseudo: false),
    L10nLanguage(code: "it", tag: "it", apple: "it", name: "Italiano", pseudo: false),
    L10nLanguage(code: "pt", tag: "pt-PT", apple: "pt-PT", name: "Português", pseudo: false),
    L10nLanguage(code: "nl", tag: "nl", apple: "nl", name: "Nederlands", pseudo: false),
    L10nLanguage(code: "qps", tag: "en-XA", apple: "en-XA", name: "Pseudo", pseudo: true),
]

private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

struct LanguageResolutionTests {
    @Test func theSystemLanguageMatchesByBaseLanguage() {
        #expect(LanguageChoice.effective("", system: ["es-MX"], among: seven) == "es")
        #expect(LanguageChoice.effective("", system: ["pt-BR"], among: seven) == "pt")
        #expect(LanguageChoice.effective("", system: ["nl-BE"], among: seven) == "nl")
        // The whole preference list counts.
        #expect(LanguageChoice.effective("", system: ["ca-ES", "es-ES"], among: seven) == "es")
        #expect(LanguageChoice.effective("", system: ["fr-CA", "de-DE"], among: seven) == "fr")
    }

    @Test func noMatchIsEnglishAndThePseudoLocaleNeverMatches() {
        #expect(LanguageChoice.effective("", system: ["ja-JP"], among: seven) == "en")
        #expect(LanguageChoice.effective("", system: ["en-US"], among: seven) == "en")
        #expect(LanguageChoice.effective("", system: ["en-XA"], among: seven) == "en")
        #expect(LanguageChoice.effective("", system: [], among: seven) == "en")
    }

    @Test func aChosenLanguageWinsOverTheSystem() {
        #expect(LanguageChoice.effective("de", system: ["es-ES"], among: seven) == "de")
        // Chosen in a draft build: the pseudo-locale is a language like any other.
        #expect(LanguageChoice.effective("qps", system: ["es-ES"], among: seven) == "qps")
    }

    @Test func aCodeThisBuildDoesntHaveShowsEnglish() {
        let englishOnly = [seven[0]]
        #expect(LanguageChoice.effective("es", system: ["es-ES"], among: englishOnly) == "en")
        #expect(LanguageChoice.effective("", system: ["es-ES"], among: englishOnly) == "en")
        #expect(LanguageChoice.effective("xx", system: ["es-ES"], among: seven) == "en")
    }

    @Test func formattersKeepTheSystemsLocaleWhenItSpeaksTheLanguage() {
        let enNL = Locale(identifier: "en_NL")
        #expect(L10n.formattingLocale(for: "en", system: enNL).identifier == "en_NL")
        // A Spanish phone that chose English: English's own formats.
        #expect(L10n.formattingLocale(for: "en", system: Locale(identifier: "es_ES")).identifier == "en")
        // Same base language: the region's formats stay (plural rules don't
        // come from here: L10n formats texts with the canonical tag).
        #expect(L10n.formattingLocale(for: "pt-PT", system: Locale(identifier: "pt_BR")).identifier == "pt_BR")
        #expect(L10n.formattingLocale(for: "de", system: Locale(identifier: "fr_CH")).identifier == "de")
    }

    @Test func anEnglishOnlyBuildKeepsTheSystemsFormats() {
        // What keeps dates and numbers as they were while English is the
        // only language: the system's locale speaks the app's language
        // (en_ES on a Spanish phone), so the root's locale is the system's.
        guard L10nLanguage.all.count == 1 else { return }
        #expect(Locale.current.language.languageCode?.identifier == "en")
        #expect(L10n.formattingLocale(for: "en").identifier == Locale.current.identifier)
    }
}

struct LanguageChoiceTests {
    @Test func aChangeIsPendingUntilTheDeviceTakesIt() {
        var c = LanguageChoice(language: "", seen: "")
        c.choose("es", at: t0)
        #expect(c.language == "es" && c.pending)
        c.taken("es")
        #expect(!c.pending)
        #expect(c.seen == "es")
    }

    @Test func whilePendingAnotherValueIsIgnoredForThirtySeconds() {
        var c = LanguageChoice(language: "en", seen: "en")
        c.choose("es", at: t0)
        // An answer given before ours reached the device.
        let took1 = c.deviceSaid("en", at: t0 + 5)
        #expect(!took1)
        #expect(c.language == "es" && c.pending)
        let took2 = c.deviceSaid("en", at: t0 + 29.9)
        #expect(!took2)
        #expect(c.language == "es")
        // After 30 s the device's value wins.
        let took3 = c.deviceSaid("en", at: t0 + 30)
        #expect(took3)
        #expect(c.language == "en" && !c.pending && c.seen == "en")
    }

    @Test func seeingItsOwnValueEndsTheWait() {
        var c = LanguageChoice(language: "en", seen: "en")
        c.choose("es", at: t0)
        let took4 = c.deviceSaid("es", at: t0 + 1)
        #expect(!took4)
        #expect(!c.pending && c.pendingAt == nil && c.seen == "es")
        // A change made in another app right after is taken at once.
        let took5 = c.deviceSaid("fr", at: t0 + 2)
        #expect(took5)
        #expect(c.language == "fr")
    }

    @Test func aStaleAnswerAfterTheAckIsStillIgnoredInTheWindow() {
        var c = LanguageChoice(language: "en", seen: "en")
        c.choose("es", at: t0)
        c.taken("es")
        let took6 = c.deviceSaid("en", at: t0 + 3)
        #expect(!took6)
        #expect(c.language == "es")
    }

    @Test func sendingAgainRestartsTheWindow() {
        var c = LanguageChoice(language: "en", seen: "en")
        c.choose("es", at: t0)
        // Offline for minutes; sent again at the next connection.
        c.sending(at: t0 + 600)
        let took7 = c.deviceSaid("en", at: t0 + 610)
        #expect(!took7)
        #expect(c.language == "es" && c.pending)
    }

    @Test func aValueFromAnotherAppIsAdoptedWhenNothingIsPending() {
        var c = LanguageChoice(language: "", seen: "")
        let took8 = c.deviceSaid("de", at: t0)
        #expect(took8)
        #expect(c.language == "de" && c.seen == "de")
        let took9 = c.deviceSaid("de", at: t0 + 1)
        #expect(!took9)
    }

    @Test func aDeviceThatPredatesTheFieldChangesNothing() {
        var c = LanguageChoice(language: "es", seen: nil)
        let took10 = c.deviceSaid(nil, at: t0)
        #expect(!took10)
        #expect(c == LanguageChoice(language: "es", seen: nil))
    }

    @Test func changedAdoptsTheDevicesValue() {
        var c = LanguageChoice(language: "en", seen: "en")
        c.choose("es", at: t0)
        c.refused("es", deviceHolds: "fr")
        #expect(c.language == "fr" && !c.pending && c.seen == "fr")
    }

    @Test func changedKeepsANewerChoiceForTheNextSend() {
        var c = LanguageChoice(language: "en", seen: "en")
        c.choose("es", at: t0)
        c.choose("de", at: t0 + 1)
        c.refused("es", deviceHolds: "fr")
        #expect(c.language == "de" && c.pending && c.seen == "fr")
    }

    @Test func logOutKeepsTheCopyAndForgetsTheDevice() {
        var c = LanguageChoice(language: "en", seen: "en")
        c.choose("it", at: t0)
        c.loggedOut()
        #expect(c == LanguageChoice(language: "it", pending: false, pendingAt: nil, seen: nil))
    }

    @Test func answersAreReadCodesFirst() {
        func ack(_ ok: Bool, _ code: String = "") -> Msg_RespEnvelope {
            var a = Msg_Ack()
            a.ok = ok
            a.code = code
            var r = Msg_RespEnvelope()
            r.payload = .respAck(a)
            return r
        }
        #expect(LanguageAnswer.of(ack(true)) == .taken)
        #expect(LanguageAnswer.of(ack(false, "changed")) == .changed)
        #expect(LanguageAnswer.of(ack(false, "forbidden")) == .failed)
        var old = Msg_RespEnvelope()
        old.error = true
        old.errorCode = "unknown_payload"
        old.errorMessage = "Este dispositivo no entiende esa petición"
        #expect(LanguageAnswer.of(old) == .tooOld)
        var older = Msg_RespEnvelope()
        older.error = true
        older.errorMessage = "unknown payload"
        #expect(LanguageAnswer.of(older) == .tooOld)
        var other = Msg_RespEnvelope()
        other.error = true
        other.errorMessage = "Not authenticated"
        #expect(LanguageAnswer.of(other) == .failed)
    }
}

/// A device stand-in for LanguageSettings: answers SetLanguage from a
/// queue, and records what it was sent.
@MainActor
private final class FakeDevice {
    var answers: [Result<Msg_RespEnvelope, Error>] = []
    var sent: [(language: String, expected: String?)] = []
    var settingsLanguage: String? = nil
    var languageChanges = 0

    var device: LanguageSettings.Device {
        LanguageSettings.Device(
            setLanguage: { [unowned self] language, expected in
                try await MainActor.run {
                    self.sent.append((language, expected))
                    guard !self.answers.isEmpty else { throw URLError(.notConnectedToInternet) }
                    return try self.answers.removeFirst().get()
                }
            },
            settings: { [unowned self] in
                await MainActor.run {
                    var s = Msg_Settings()
                    if let l = self.settingsLanguage { s.language = l }
                    return s
                }
            },
            languageChanged: { [unowned self] in self.languageChanges += 1 }
        )
    }

    static func ack(_ ok: Bool, _ code: String = "") -> Result<Msg_RespEnvelope, Error> {
        var a = Msg_Ack()
        a.ok = ok
        a.code = code
        var r = Msg_RespEnvelope()
        r.payload = .respAck(a)
        return .success(r)
    }

    static var unknownPayload: Result<Msg_RespEnvelope, Error> {
        var r = Msg_RespEnvelope()
        r.error = true
        r.errorCode = "unknown_payload"
        r.errorMessage = "This device does not understand that request"
        return .success(r)
    }
}

/// A throwaway defaults suite, removed when the test ends.
private final class TempDefaults {
    let name = "cloud.off-the.OffTheCloud.test.\(UUID().uuidString)"
    lazy var defaults = UserDefaults(suiteName: name)!
    deinit { UserDefaults().removePersistentDomain(forName: name) }
}

/// Lets the settings' own task (the send loop) run.
@MainActor
private func settle() async {
    for _ in 0..<20 { await Task.yield() }
    try? await Task.sleep(for: .milliseconds(50))
    for _ in 0..<20 { await Task.yield() }
}

@MainActor
@Suite(.serialized)
struct LanguageSettingsTests {
    private final class Shown { var codes: [String] = [] }

    private func make(_ suite: TempDefaults, _ fake: FakeDevice, system: [String] = ["en-US"],
                      clock: @escaping () -> Date = { t0 }, shown: Shown = Shown()) -> LanguageSettings {
        LanguageSettings(defaults: suite.defaults, device: fake.device, languages: seven,
                         system: { system }, show: { shown.codes.append($0) }, now: clock)
    }

    @Test func aChangeShowsAtOnceAndIsSentExpectingWhatWasSeen() async {
        let suite = TempDefaults(), fake = FakeDevice(), shown = Shown()
        let s = make(suite, fake, shown: shown)
        s.start()
        s.deviceSaid("")
        #expect(shown.codes == ["en"])
        fake.answers = [FakeDevice.ack(true)]
        s.choose("es")
        #expect(shown.codes == ["en", "es"])
        await settle()
        #expect(fake.sent.count == 1)
        #expect(fake.sent.first?.language == "es")
        #expect(fake.sent.first?.expected == "")
        #expect(!s.choice.pending)
        #expect(s.deviceKeepsIt == true)
        // The push token goes again in the new language.
        #expect(fake.languageChanges == 1)
    }

    @Test func neverSeeingTheDeviceSendsNoExpected() async {
        let suite = TempDefaults(), fake = FakeDevice()
        let s = make(suite, fake)
        fake.answers = [FakeDevice.ack(true)]
        s.choose("de")
        await settle()
        #expect(fake.sent.count == 1)
        #expect(fake.sent.first?.expected == nil)
    }

    @Test func changedAdoptsTheDevicesCurrentValue() async {
        let suite = TempDefaults(), fake = FakeDevice(), shown = Shown()
        let s = make(suite, fake, shown: shown)
        s.start()
        s.deviceSaid("en")
        fake.answers = [FakeDevice.ack(false, "changed")]
        fake.settingsLanguage = "fr"
        s.choose("es")
        await settle()
        #expect(s.choice.language == "fr")
        #expect(!s.choice.pending)
        #expect(s.choice.seen == "fr")
        #expect(shown.codes.last == "fr")
    }

    @Test func unknownPayloadKeepsItPendingUntilTheNextConnection() async {
        let suite = TempDefaults(), fake = FakeDevice()
        var now = t0
        let s = make(suite, fake, clock: { now })
        fake.answers = [FakeDevice.unknownPayload]
        s.choose("es")
        await settle()
        #expect(s.choice.language == "es")
        #expect(s.choice.pending)
        #expect(s.deviceKeepsIt == false)
        // Another change on the same connection isn't sent; it stays pending.
        s.choose("de")
        await settle()
        #expect(fake.sent.count == 1)
        #expect(s.choice.pending)
        // Long after, the device was updated: the next connection sends it,
        // and the device's default from its first Status doesn't undo it.
        now = t0 + 3600
        fake.answers = [FakeDevice.ack(true)]
        s.connected()
        s.deviceSaid("")
        #expect(s.choice.language == "de")
        await settle()
        #expect(fake.sent.count == 2)
        #expect(fake.sent.last?.language == "de")
        #expect(fake.sent.last?.expected == nil)
        #expect(!s.choice.pending)
        #expect(s.choice.language == "de")
    }

    @Test func aFailureStaysPendingAndGoesAtTheNextConnection() async {
        let suite = TempDefaults(), fake = FakeDevice()
        let s = make(suite, fake)
        s.deviceSaid("en")
        // No answers queued: the request fails as if offline.
        s.choose("nl")
        await settle()
        #expect(s.choice.pending)
        #expect(fake.sent.count == 1)
        // Still pending after a relaunch.
        let again = make(suite, fake)
        #expect(again.choice.pending)
        #expect(again.choice.language == "nl")
        fake.answers = [FakeDevice.ack(true)]
        again.connected()
        await settle()
        #expect(fake.sent.count == 2)
        #expect(fake.sent.last?.language == "nl")
        #expect(fake.sent.last?.expected == "en")
        #expect(!again.choice.pending)
    }

    @Test func theDevicesValueWaitsThirtySecondsWhilePending() async {
        let suite = TempDefaults(), fake = FakeDevice(), shown = Shown()
        var now = t0
        let s = make(suite, fake, clock: { now }, shown: shown)
        s.start()
        s.deviceSaid("en")
        s.choose("es")      // fails: stays pending
        await settle()
        now = t0 + 10
        s.deviceSaid("en")
        #expect(s.choice.language == "es")
        now = t0 + 31
        s.deviceSaid("en")
        #expect(s.choice.language == "en")
        #expect(shown.codes.last == "en")
    }

    @Test func theCopySurvivesLogOutAndARelaunch() async {
        let suite = TempDefaults(), fake = FakeDevice()
        let s = make(suite, fake)
        s.deviceSaid("en")
        fake.answers = [FakeDevice.ack(true)]
        s.choose("pt")
        await settle()
        s.loggedOut()
        #expect(s.choice.language == "pt")
        #expect(s.choice.seen == nil)
        #expect(s.deviceKeepsIt == nil)
        // Log Out removes the app's own defaults domain: the copy has its own.
        #expect(LanguageSettings.suiteName != Bundle.main.bundleIdentifier)
        let app = TempDefaults()
        app.defaults.set("x", forKey: "wifiOnly")
        UserDefaults().removePersistentDomain(forName: app.name)
        let relaunched = make(suite, fake)
        #expect(relaunched.choice.language == "pt")
        #expect(!relaunched.choice.pending)
        #expect(relaunched.effective == "pt")
    }

    @Test func automaticFollowsTheSystemAndNamesWhatItGives() {
        let suite = TempDefaults(), fake = FakeDevice()
        let s = make(suite, fake, system: ["ca-ES", "es-ES"])
        #expect(s.effective == "es")
        #expect(s.selected == "")
        #expect(s.name(of: s.systemLanguage) == "Español")
        #expect(s.pickerShown)
        s.deviceSaid("xx")
        // A language this build doesn't have: English, marked as such.
        #expect(s.effective == "en")
        #expect(s.selected == "en")
    }
}

/// Text compared with English: the app's language pinned to it first.
@MainActor
@Suite(.serialized)
struct LanguageTextTests {
    @Test func theScreensTextsInEnglish() {
        L10n.shared.set("en")
        #expect(S.appSettingsLanguage == "Language")
        #expect(S.appSettingsLanguageAutomatic == "Automatic")
        #expect(S.appSettingsLanguageAutomaticNamed(language: "Español") == "Automatic (Español)")
    }

    @Test func theAppsLanguageTableIsOnlyEnglishInAShippingBuild() {
        // The picker stays hidden while English is the only language.
        #expect(L10nLanguage.all.first?.code == "en")
        #expect(LanguageSettings.shared.pickerShown == (L10nLanguage.all.count > 1))
    }
}

// MARK: lang on every request

/// A WebSocket server on loopback that answers every request with an Ack
/// and keeps the envelopes it got.
private final class LangStandIn: @unchecked Sendable {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "test-lang-server")
    private let lock = NSLock()
    private var _received: [Msg_ReqEnvelope] = []
    var received: [Msg_ReqEnvelope] { lock.withLock { _received } }

    init() throws {
        let ws = NWProtocolWebSocket.Options()
        ws.autoReplyPing = true
        let params = NWParameters.tcp
        params.defaultProtocolStack.applicationProtocols.insert(ws, at: 0)
        params.requiredLocalEndpoint = NWEndpoint.hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: params)
        listener.newConnectionHandler = { [weak self] conn in
            guard let self else { return }
            conn.start(queue: self.queue)
            self.receive(on: conn)
        }
    }

    func start() async throws -> URL {
        let port: UInt16 = try await withCheckedThrowingContinuation { cont in
            let once = LangOnce()
            listener.stateUpdateHandler = { [listener] state in
                switch state {
                case .ready: if once.first() { cont.resume(returning: listener.port?.rawValue ?? 0) }
                case .failed(let e): if once.first() { cont.resume(throwing: e) }
                default: break
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
                self.lock.withLock { self._received.append(env) }
                var resp = Msg_RespEnvelope()
                resp.id = env.id
                var ack = Msg_Ack()
                ack.ok = true
                resp.payload = .respAck(ack)
                if let out = try? resp.serializedData() {
                    let m = NWProtocolWebSocket.Metadata(opcode: .binary)
                    let ctx = NWConnection.ContentContext(identifier: "answer", metadata: [m])
                    conn.send(content: out, contentContext: ctx, isComplete: true, completion: .contentProcessed { _ in })
                }
            }
            if error == nil { self.receive(on: conn) }
        }
    }
}

private final class LangOnce: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func first() -> Bool { lock.withLock { defer { done = true }; return !done } }
}

private func isGetTags(_ e: Msg_ReqEnvelope?) -> Bool {
    if case .reqGetTags? = e?.payload { return true }
    return false
}

@Suite(.serialized)
struct RequestLangTests {
    @Test func aBuildClosureThatReplacesTheEnvelopeStillSendsLang() async throws {
        await MainActor.run { L10n.shared.set("en") }
        let server = try LangStandIn()
        let url = try await server.start()
        defer { server.stop() }
        let ws = WSClient()
        try await ws.connect(url: url)

        // Most call sites build a fresh envelope and replace the one they
        // were given - id and lang included.
        let resp = try await ws.request(timeout: 5) { e in
            var req = Msg_ReqEnvelope()
            req.lang = "zz"
            req.payload = .reqGetTags(Msg_GetTags())
            e = req
        }
        // And the ones that set the payload in place.
        _ = try await ws.request(timeout: 5) { $0.payload = .reqGetStatus(Msg_GetStatus()) }
        await ws.close()

        let got = server.received
        #expect(got.count == 2)
        #expect(got.allSatisfy { $0.lang == "en" })
        #expect(got.first?.id == resp.id)
        #expect(isGetTags(got.first))
    }
}
