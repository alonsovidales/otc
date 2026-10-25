// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  WifiSection.swift
//  OffTheCloud
//
//  Settings › Wi-Fi (release 140): the device's owner changes the network
//  the device uses, or its password - the router's Wi-Fi changed name or
//  password, say. The device hands the change to a root runner, which joins
//  the new network, keeps it only once the device reaches the internet
//  through it, and otherwise goes back to the network it was on.
//
//  Primary-only and only where the device manages its own Wi-Fi
//  (GetWifiStatus' managed); devices before release 140 answer
//  unknown_payload, and the section stays hidden until the next sign-in
//  (DeviceSupport.wifi). The password is sealed with this connection's key
//  (PwCrypto) and lives only in the view model, never in the view's saved
//  state; it is cleared once sealed and when the section hides.
//
//  WifiFlow holds the rules the tests check (OffTheCloudTests/
//  WifiFlowTests.swift) - the same case table as Android's ui/settings/
//  WifiFlow.kt (WifiFlowTest.kt) and the web's wifiFlow.ts.
//
//  Mirrors web/src/components/settings/WifiPanel.tsx and Android's
//  WifiSection() / WifiViewModel.

import SwiftUI
import UIKit

/// The pure rules of Settings › Wi-Fi: what may be sent, and what an answer
/// means for the section. Nothing here talks to the device.
enum WifiFlow {
    /// Which kind of network the picker has chosen.
    enum Kind: Equatable {
        /// A listed network that needs a password.
        case secured
        /// A listed open network: no password field, nothing sent.
        case open
        /// "Other Network…": a typed name (a network that hides it) and an
        /// optional password - empty for an open one.
        case other
    }

    /// Why Connect can't send yet; each has the app's own words.
    enum Problem: Equatable {
        case nameNeeded, nameTooLong, passwordNeeded, passwordInvalid
    }

    /// What a GetWifiStatus answer on load means for the section.
    enum Load: Equatable {
        case show
        case hide
        /// A device before release 140: not asked again until the next
        /// sign-in (DeviceSupport.wifi).
        case hideUntilSignIn
    }

    /// How a change ended.
    enum Outcome: Equatable {
        /// The device's words (e.g. "Connected to Home.").
        case connected(String)
        /// The runner's code and the device's words for it.
        case failed(code: String, message: String)
        /// The device no longer reports this change (another one, none,
        /// or a state this build doesn't know).
        case unknown
    }

    /// What JoinWifi's own answer means.
    enum Sent: Equatable {
        /// Accepted: wait for this change id to end.
        case wait(String)
        case done(Outcome)
        /// Refused before anything changed: the device's words, nil when it
        /// gave none (the app's "failed" then). The picker stays open.
        case refused(String?)
        /// not_managed: the device can't change its Wi-Fi after all.
        case hide
        case hideUntilSignIn
    }

    /// What one look while waiting means.
    enum Poll: Equatable {
        case keepWaiting
        case done(Outcome)
    }

    /// idle, the request being sent, waiting for the device, or waiting
    /// past `slowAfter`.
    enum Phase: Equatable {
        case idle, sending, waiting, slow
    }

    /// A look at the device's status while waiting, every 3 s, each given
    /// up after 10 s; past 4 minutes the section says so and looks every
    /// 10 s until the device answers.
    static let pollEvery: TimeInterval = 3
    static let lookTimeout: TimeInterval = 10
    static let slowAfter: TimeInterval = 240
    static let slowPollEvery: TimeInterval = 10

    /// Failures worth another try at the same network - its password or
    /// its name - so the picker stays open with it still chosen
    /// (open_network: a password typed for a network that has none).
    static let pickerCodes: Set<String> = ["wrong_password", "auth_failed", "not_found", "password_required", "open_network"]

    // MARK: What may be sent

    /// 1-32 bytes of UTF-8, as typed: a name is never trimmed or changed.
    /// Control characters are left to the device (bad_name).
    static func checkName(_ ssid: String) -> Problem? {
        if ssid.isEmpty { return .nameNeeded }
        if ssid.utf8.count > 32 { return .nameTooLong }
        return nil
    }

    /// Empty only where the network may be open; otherwise 8-63 bytes of
    /// UTF-8 with no control characters, or exactly 64 hexadecimal digits.
    /// An open row's password is never looked at (nothing is sent).
    static func checkPassword(_ password: String, kind: Kind) -> Problem? {
        switch kind {
        case .open:
            return nil
        case .secured:
            if password.isEmpty { return .passwordNeeded }
        case .other:
            if password.isEmpty { return nil }
        }
        return validPassword(password) ? nil : .passwordInvalid
    }

    /// The name first, then the password.
    static func check(ssid: String, password: String, kind: Kind) -> Problem? {
        checkName(ssid) ?? checkPassword(password, kind: kind)
    }

    /// What gets sealed and sent: nothing for an open row.
    static func sentPassword(_ password: String, kind: Kind) -> String {
        kind == .open ? "" : password
    }

    /// JoinWifi.hidden: only a typed name may be missing from the scan.
    static func hidden(_ kind: Kind) -> Bool { kind == .other }

    /// Whether a listed network can be chosen: not one the device says it
    /// can't join (enterprise, WEP, OWE), nor one whose name it wouldn't
    /// take (control characters, over 32 bytes). Shown greyed out as "Not
    /// supported".
    static func joinable(_ n: Msg_WifiNetwork) -> Bool {
        !n.unsupported && checkName(n.ssid) == nil
            && !n.ssid.unicodeScalars.contains { $0.value < 0x20 || $0.value == 0x7f }
    }

    /// On its cable and no Wi-Fi network: the device stays reachable while
    /// it changes, and the confirmation says so.
    static func cableOnly(wired: Bool, ssid: String) -> Bool { wired && ssid.isEmpty }

    static func validPassword(_ password: String) -> Bool {
        let bytes = Array(password.utf8)
        if bytes.count == 64 && bytes.allSatisfy(isHexDigit) { return true }
        guard (8...63).contains(bytes.count) else { return false }
        return !password.unicodeScalars.contains { $0.value < 0x20 || $0.value == 0x7f }
    }

    /// ASCII only: Character.isHexDigit also takes the fullwidth forms.
    private static func isHexDigit(_ b: UInt8) -> Bool {
        (0x30...0x39).contains(b) || (0x41...0x46).contains(b) || (0x61...0x66).contains(b)
    }

    // MARK: What an answer means

    static func onLoad(_ resp: Msg_RespEnvelope) -> Load {
        if !resp.error, case .respWifiStatus(let s) = resp.payload { return s.managed ? .show : .hide }
        if resp.error && ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) {
            return .hideUntilSignIn
        }
        return .hide
    }

    /// A change still running when the section loads (the app was closed
    /// while waiting, say): its id, to wait for it again.
    static func resumeID(_ status: Msg_WifiStatus) -> String? {
        guard status.hasLastJoin, status.lastJoin.state == "joining", !status.lastJoin.id.isEmpty else { return nil }
        return status.lastJoin.id
    }

    static func afterJoin(_ resp: Msg_RespEnvelope) -> Sent {
        if !resp.error, case .respWifiStatus(let s) = resp.payload {
            guard s.hasLastJoin else { return .done(.unknown) }
            let join = s.lastJoin
            if join.state == "joining" { return join.id.isEmpty ? .done(.unknown) : .wait(join.id) }
            return .done(outcome(join))
        }
        if resp.error {
            if ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) { return .hideUntilSignIn }
            if resp.errorCode == "not_managed" { return .hide }
        }
        return .refused(words(resp))
    }

    /// Anything but the device's status - a timeout never gets here, a
    /// bridge answer while the device is away, an error - says nothing
    /// about the change: keep waiting. So does an answer from a device
    /// that says for a moment it doesn't manage its Wi-Fi.
    static func onPoll(_ resp: Msg_RespEnvelope, joinID: String) -> Poll {
        guard !resp.error, case .respWifiStatus(let s) = resp.payload, s.managed else { return .keepWaiting }
        guard s.hasLastJoin, s.lastJoin.id == joinID else { return .done(.unknown) }
        if s.lastJoin.state == "joining" { return .keepWaiting }
        return .done(outcome(s.lastJoin))
    }

    static func keepsPicker(_ code: String) -> Bool { pickerCodes.contains(code) }

    static func slow(_ elapsed: TimeInterval) -> Bool { elapsed >= slowAfter }

    static func pollInterval(slow: Bool) -> TimeInterval { slow ? slowPollEvery : pollEvery }

    /// An answer's words: its error message, else a bridge Ack's.
    static func words(_ resp: Msg_RespEnvelope) -> String? {
        if !resp.errorMessage.isEmpty { return resp.errorMessage }
        if case .respAck(let ack) = resp.payload, !ack.errorMsg.isEmpty { return ack.errorMsg }
        return nil
    }

    private static func outcome(_ join: Msg_WifiJoin) -> Outcome {
        switch join.state {
        case "connected": return .connected(join.message)
        case "failed": return .failed(code: join.code, message: join.message)
        default: return .unknown
        }
    }

    // MARK: Words

    /// A signal (0-100) as a whole percentage in the app's language.
    static func percent(_ signal: Int32, locale: Locale) -> String {
        let s = Double(min(max(signal, 0), 100)) / 100
        return s.formatted(.percent.precision(.fractionLength(0)).locale(locale))
    }

    static func text(_ problem: Problem) -> String {
        switch problem {
        case .nameNeeded: return S.appSettingsWifiNameNeeded
        case .nameTooLong: return S.appSettingsWifiNameTooLong
        case .passwordNeeded: return S.appSettingsWifiPasswordNeeded
        case .passwordInvalid: return S.appSettingsWifiPasswordInvalid
        }
    }
}

@MainActor
final class WifiViewModel: ObservableObject {
    /// A network chosen in the picker; ssid is "" for Other Network until
    /// typed (`otherName`).
    struct Choice: Equatable {
        let ssid: String
        let kind: WifiFlow.Kind
    }

    /// The confirmation on screen: the name to join, and which message.
    struct Confirm: Equatable {
        let ssid: String
        let kind: WifiFlow.Kind
        let cableOnly: Bool
    }

    /// The owner of the primary instance, on a device that manages its
    /// own Wi-Fi.
    @Published var shown = false
    // The device's network now.
    @Published var ssid = ""
    @Published var signal: Int32 = 0
    @Published var wired = false

    @Published var picking = false
    @Published var scanning = false
    @Published var networks: [Msg_WifiNetwork] = []
    @Published var chosen: Choice?
    @Published var otherName = ""
    /// Only ever here, and only until it is sealed.
    @Published var password = ""
    @Published var confirm: Confirm?

    @Published var phase: WifiFlow.Phase = .idle
    @Published var note: SettingsText?
    @Published var error: SettingsText?

    private let ws = OTCConnection.shared
    private let support = DeviceSupport.shared
    /// The change being waited for, and when the wait began.
    private var joinID = ""
    private var waitStarted = Date()
    private var waitTask: Task<Void, Never>?
    /// The ssid the wait is about, for its "Connecting to …" note.
    private var joiningSSID = ""

    var busy: Bool { phase != .idle }

    func load() async {
        guard support.wifi else { hide(); return }
        var primary = false
        do {
            let resp = try await ws.request(timeout: 30) { e in
                e.payload = .reqGetInstanceRole(Msg_ReqGetInstanceRole())
            }
            if case .respInstanceRole(let role) = resp.payload { primary = role.isPrimary }
        } catch {
            // Not known now: what is shown stays as it is.
            return
        }
        guard primary else { hide(); return }
        await refresh()
    }

    /// The device's status: shows or hides the section, and picks up a
    /// change still running (one asked before the app was closed).
    func refresh() async {
        guard support.wifi else { hide(); return }
        let resp: Msg_RespEnvelope
        do {
            resp = try await ws.request(timeout: 30) { e in
                e.payload = .reqGetWifiStatus(Msg_GetWifiStatus())
            }
        } catch {
            // A dropped connection says nothing about the section.
            return
        }
        if busy {
            // The wait reads the answers itself; this only keeps the
            // current network up to date.
            if case .respWifiStatus(let s) = resp.payload, s.managed { apply(s) }
            return
        }
        switch WifiFlow.onLoad(resp) {
        case .show:
            guard case .respWifiStatus(let s) = resp.payload else { return }
            apply(s)
            shown = true
            if let id = WifiFlow.resumeID(s) {
                error = nil
                startWaiting(id: id, ssid: s.lastJoin.ssid, since: Date())
            }
        case .hide:
            hide()
        case .hideUntilSignIn:
            support.wifi = false
            hide()
        }
    }

    func startPicking() {
        picking = true
        chosen = nil
        otherName = ""
        password = ""
        note = nil
        error = nil
        Task { await scan() }
    }

    func closePicker() {
        picking = false
        chosen = nil
        otherName = ""
        password = ""
        error = nil
    }

    func scan() async {
        scanning = true
        defer { scanning = false }
        do {
            // The device scans for up to 20 s.
            let resp = try await ws.request(timeout: 60) { e in
                e.payload = .reqListWifiNetworks(Msg_ListWifiNetworks())
            }
            if case .respWifiNetworks(let list) = resp.payload {
                networks = list.networks
            } else {
                error = .asIs(WifiFlow.words(resp) ?? "", or: { S.appSettingsWifiScanFailed })
            }
        } catch {
            self.error = .error(error)
        }
    }

    func choose(_ network: Msg_WifiNetwork) {
        guard WifiFlow.joinable(network), !busy else { return }
        let kind: WifiFlow.Kind = network.secured ? .secured : .open
        if chosen != Choice(ssid: network.ssid, kind: kind) { password = "" }
        chosen = Choice(ssid: network.ssid, kind: kind)
        error = nil
    }

    func chooseOther() {
        guard !busy else { return }
        if chosen?.kind != .other { password = "" }
        chosen = Choice(ssid: "", kind: .other)
        error = nil
    }

    /// Connect: checked here first with the device's own rules, then
    /// confirmed.
    func askToConnect() {
        guard let chosen, !busy else { return }
        let name = chosen.kind == .other ? otherName : chosen.ssid
        if let problem = WifiFlow.check(ssid: name, password: password, kind: chosen.kind) {
            error = .say { WifiFlow.text(problem) }
            return
        }
        error = nil
        confirm = Confirm(ssid: name, kind: chosen.kind, cableOnly: WifiFlow.cableOnly(wired: wired, ssid: ssid))
    }

    func join(_ c: Confirm) async {
        guard !busy else { return }
        phase = .sending
        note = nil
        error = nil
        let plain = WifiFlow.sentPassword(password, kind: c.kind)
        var sealed = Data()
        do {
            if !plain.isEmpty {
                let keyResp = try await ws.request(timeout: 30) { $0.payload = .reqGetPubKey(Msg_GetPubKey()) }
                guard case .respPubKey(let key) = keyResp.payload else {
                    error = .say { S.appSettingsPasswordNoKey }
                    phase = .idle
                    return
                }
                sealed = try PwCrypto.encryptPassword(plain, pubKeyDER: key.publicKey)
            }
        } catch {
            self.error = .error(error)
            phase = .idle
            return
        }
        password = ""

        let resp: Msg_RespEnvelope
        do {
            resp = try await ws.request(timeout: 60) { e in
                var m = Msg_JoinWifi()
                m.ssid = c.ssid
                m.password = sealed
                m.hidden = WifiFlow.hidden(c.kind)
                e.payload = .reqJoinWifi(m)
            }
        } catch {
            // The device may have taken it before the connection went:
            // the status says, and a change under way is waited for.
            self.error = .error(error)
            phase = .idle
            await refresh()
            return
        }

        if case .respWifiStatus(let s) = resp.payload, !resp.error, s.managed { apply(s) }
        switch WifiFlow.afterJoin(resp) {
        case .wait(let id):
            startWaiting(id: id, ssid: c.ssid, since: Date())
        case .done(let outcome):
            phase = .idle
            finish(outcome)
        case .refused(let words):
            phase = .idle
            error = .asIs(words ?? "", or: { S.appSettingsWifiFailed })
            // A change already under way (busy) is waited for.
            await refresh()
        case .hide:
            phase = .idle
            error = .asIs(WifiFlow.words(resp) ?? "", or: { S.appSettingsWifiFailed })
            await refresh()
        case .hideUntilSignIn:
            phase = .idle
            support.wifi = false
            hide()
        }
    }

    /// Waits for change `id` to end: a look every 3 s (10 s once slow),
    /// owned by the view model rather than the view - Settings is a tab
    /// kept alive, so the result is there when the owner comes back to it.
    /// It ends with the device's answer, or when the section hides; one
    /// started again (a resumed change) replaces it.
    private func startWaiting(id: String, ssid: String, since: Date) {
        joinID = id
        joiningSSID = ssid
        waitStarted = since
        phase = WifiFlow.slow(Date().timeIntervalSince(since)) ? .slow : .waiting
        showWaitNote()
        waitTask?.cancel()
        waitTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let interval = self?.nextLookIn() else { return }
                try? await Task.sleep(for: .seconds(interval))
                guard !Task.isCancelled, let self else { return }
                if await self.look(id: id) { return }
            }
        }
    }

    private func nextLookIn() -> TimeInterval {
        let slow = WifiFlow.slow(Date().timeIntervalSince(waitStarted))
        if slow && phase == .waiting {
            phase = .slow
            showWaitNote()
        }
        return WifiFlow.pollInterval(slow: slow)
    }

    private func showWaitNote() {
        let name = joiningSSID
        note = phase == .slow ? .say { S.appSettingsWifiSlow } : .say { S.appSettingsWifiJoining(ssid: name) }
    }

    /// One look at the device's status; true once the change has ended.
    private func look(id: String) async -> Bool {
        guard joinID == id else { return true }
        let resp: Msg_RespEnvelope
        do {
            resp = try await ws.request(timeout: WifiFlow.lookTimeout) { e in
                e.payload = .reqGetWifiStatus(Msg_GetWifiStatus())
            }
        } catch {
            return false
        }
        guard joinID == id else { return true }
        if !resp.error, case .respWifiStatus(let s) = resp.payload, s.managed { apply(s) }
        switch WifiFlow.onPoll(resp, joinID: id) {
        case .keepWaiting:
            return false
        case .done(let outcome):
            finish(outcome)
            return true
        }
    }

    private func finish(_ outcome: WifiFlow.Outcome) {
        joinID = ""
        phase = .idle
        switch outcome {
        case .connected(let words):
            note = words.isEmpty ? nil : .asIs(words)
            error = nil
            closePicker()
        case .failed(let code, let words):
            note = nil
            let shown: SettingsText = .asIs(words, or: { S.appSettingsWifiFailed })
            if !WifiFlow.keepsPicker(code) { closePicker() }
            error = shown
        case .unknown:
            note = .say { S.appSettingsWifiUnknownResult }
            error = nil
            closePicker()
        }
    }

    private func apply(_ s: Msg_WifiStatus) {
        ssid = s.ssid
        signal = s.signal
        wired = s.wired
    }

    private func hide() {
        waitTask?.cancel()
        waitTask = nil
        joinID = ""
        phase = .idle
        shown = false
        picking = false
        chosen = nil
        confirm = nil
        password = ""
        otherName = ""
        note = nil
        error = nil
    }
}

struct WifiSection: View {
    @StateObject private var vm = WifiViewModel()

    var body: some View {
        if vm.shown {
            Section(header: Text(S.appSettingsWifiTitle), footer: Text(S.appSettingsWifiIntro)) {
                current

                if vm.picking {
                    picker
                } else {
                    Button(S.appSettingsWifiChange) { vm.startPicking() }
                        .buttonStyle(.bordered)
                        .disabled(vm.busy)
                }

                if let note = vm.note {
                    HStack(spacing: 8) {
                        if vm.phase == .waiting || vm.phase == .sending { ProgressView() }
                        Text(note.text)
                            .font(.caption)
                            .foregroundStyle(vm.phase == .slow ? Color.orange : Color.secondary)
                    }
                } else if vm.phase == .sending {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text(S.commonConnecting).font(.caption).foregroundStyle(.secondary)
                    }
                }
                if let error = vm.error {
                    Text(error.text).font(.caption).foregroundStyle(.red)
                }
            }
            .task { await vm.load() }
            // The cable may have gone in or out meanwhile.
            .onReceive(NotificationCenter.default.publisher(
                for: UIApplication.willEnterForegroundNotification
            )) { _ in
                Task { await vm.refresh() }
            }
            .alert(
                vm.confirm.map { S.appSettingsWifiConfirmTitle(ssid: $0.ssid) } ?? "",
                isPresented: Binding(get: { vm.confirm != nil }, set: { if !$0 { vm.confirm = nil } }),
                presenting: vm.confirm
            ) { c in
                Button(S.commonCancel, role: .cancel) {}
                Button(S.commonConnect) { Task { await vm.join(c) } }
            } message: { c in
                Text(c.cableOnly ? S.appSettingsWifiConfirmCableMessage : S.appSettingsWifiConfirmMessage)
            }
        } else {
            // Nothing to show, but the role and the status still have to be
            // asked for.
            Color.clear.frame(height: 0).task { await vm.load() }
        }
    }

    /// The device's network now: its Wi-Fi (or none) and its cable.
    @ViewBuilder
    private var current: some View {
        if vm.ssid.isEmpty {
            Label(S.appSettingsWifiNoWifi, systemImage: "wifi.slash")
        } else {
            Label(S.appSettingsWifiOnWifi(ssid: vm.ssid, signal: WifiFlow.percent(vm.signal, locale: L10n.shared.locale)),
                  systemImage: "wifi")
        }
        if vm.wired {
            Label(S.appSettingsWifiOnCable, systemImage: "cable.connector")
        }
    }

    @ViewBuilder
    private var picker: some View {
        if vm.scanning {
            HStack(spacing: 8) {
                ProgressView()
                Text(S.appSettingsWifiScanning).foregroundStyle(.secondary)
            }
        } else if vm.networks.isEmpty {
            Text(S.appSettingsWifiNoNetworks).font(.caption).foregroundStyle(.secondary)
        }

        ForEach(vm.networks, id: \.ssid) { n in
            networkRow(n)
        }

        Button {
            vm.chooseOther()
        } label: {
            HStack(spacing: 8) {
                chosenMark(vm.chosen?.kind == .other)
                Text(S.appSettingsWifiOther).foregroundStyle(.primary)
                Spacer(minLength: 0)
            }
            .contentShape(Rectangle())
        }
        .disabled(vm.busy)
        .accessibilityAddTraits(vm.chosen?.kind == .other ? .isSelected : [])

        if let chosen = vm.chosen {
            if chosen.kind == .other {
                PlainTextField(placeholder: S.appSettingsWifiName, text: $vm.otherName)
                    .disabled(vm.busy)
            }
            if chosen.kind != .open {
                // No password AutoFill: this is the Wi-Fi's password, not
                // the device's, and must not be saved as it.
                SecureField(chosen.kind == .other ? S.appSettingsWifiPasswordOptional : S.commonPassword,
                            text: $vm.password)
                    .textContentType(nil)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .submitLabel(.join)
                    .onSubmit { vm.askToConnect() }
                    .disabled(vm.busy)
            }
            Button(S.commonConnect) { vm.askToConnect() }
                .buttonStyle(.borderedProminent)
                .disabled(vm.busy)
        }

        HStack {
            Button(S.appSettingsWifiScan) { Task { await vm.scan() } }
                .buttonStyle(.bordered)
                .disabled(vm.scanning || vm.busy)
            Spacer()
            Button(S.commonCancel) { vm.closePicker() }
                .buttonStyle(.bordered)
                .disabled(vm.busy)
        }
    }

    private func networkRow(_ n: Msg_WifiNetwork) -> some View {
        let picked = vm.chosen?.ssid == n.ssid && vm.chosen?.kind != .other
        let joinable = WifiFlow.joinable(n)
        return Button {
            vm.choose(n)
        } label: {
            HStack(spacing: 8) {
                chosenMark(picked)
                Text(verbatim: n.ssid)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .foregroundStyle(joinable ? Color.primary : Color.secondary)
                if n.current {
                    Text(S.appSettingsWifiCurrentBadge)
                        .font(.caption2.bold())
                        .foregroundStyle(.white)
                        .padding(.horizontal, 5)
                        .padding(.vertical, 1)
                        .background(Color.green, in: Capsule())
                }
                Spacer(minLength: 4)
                if !joinable {
                    Text(S.appSettingsWifiUnsupported).font(.caption).foregroundStyle(.secondary)
                }
                if n.secured {
                    Image(systemName: "lock.fill")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .accessibilityLabel(S.appSettingsWifiSecured)
                } else {
                    Text(S.appSettingsWifiOpenNetwork).font(.caption).foregroundStyle(.secondary)
                }
                Text(verbatim: WifiFlow.percent(n.signal, locale: L10n.shared.locale))
                    .font(.caption)
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
            }
            .contentShape(Rectangle())
        }
        .disabled(!joinable || vm.busy)
        .accessibilityAddTraits(picked ? .isSelected : [])
    }

    private func chosenMark(_ on: Bool) -> some View {
        Image(systemName: "checkmark")
            .font(.body.weight(.semibold))
            .foregroundStyle(.tint)
            .opacity(on ? 1 : 0)
            .accessibilityHidden(true)
    }
}

/// A one-line field that leaves what is typed alone: no capitalisation,
/// correction, spell check or smart punctuation (a Wi-Fi name with ' must
/// not become ’), and no AutoFill. SwiftUI's TextField can't turn smart
/// quotes off.
private struct PlainTextField: UIViewRepresentable {
    let placeholder: String
    @Binding var text: String

    func makeUIView(context: Context) -> UITextField {
        let f = UITextField()
        f.autocorrectionType = .no
        f.autocapitalizationType = .none
        f.spellCheckingType = .no
        f.smartQuotesType = .no
        f.smartDashesType = .no
        f.smartInsertDeleteType = .no
        f.textContentType = nil
        f.returnKeyType = .done
        f.clearButtonMode = .whileEditing
        f.font = .preferredFont(forTextStyle: .body)
        f.adjustsFontForContentSizeCategory = true
        f.setContentHuggingPriority(.defaultLow, for: .horizontal)
        f.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        f.delegate = context.coordinator
        f.addTarget(context.coordinator, action: #selector(Coordinator.changed(_:)), for: .editingChanged)
        return f
    }

    func updateUIView(_ f: UITextField, context: Context) {
        context.coordinator.text = $text
        if f.text != text { f.text = text }
        f.placeholder = placeholder
        f.accessibilityLabel = placeholder
        f.isEnabled = context.environment.isEnabled
    }

    func makeCoordinator() -> Coordinator { Coordinator(text: $text) }

    @MainActor
    final class Coordinator: NSObject, UITextFieldDelegate {
        var text: Binding<String>

        init(text: Binding<String>) { self.text = text }

        @objc func changed(_ f: UITextField) { text.wrappedValue = f.text ?? "" }

        func textFieldShouldReturn(_ f: UITextField) -> Bool {
            f.resignFirstResponder()
            return true
        }
    }
}
