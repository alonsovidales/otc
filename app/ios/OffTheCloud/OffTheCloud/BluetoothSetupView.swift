// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  BluetoothSetupView.swift
//  OffTheCloud
//
//  Issue #137: set a brand-new device up from the app, over Bluetooth LE,
//  without leaving the phone's WiFi for the device's hotspot. The device
//  (scripts/setup_ble.py) offers its setup wizard - the same page the
//  hotspot serves on port 80 - over one GATT service; this screen shows
//  that page in a web view whose every request (the page, /api/state,
//  /api/wifi, ...) goes over Bluetooth instead of HTTP. Nothing of the
//  wizard is reimplemented here, so both ways of setting up stay the
//  same thing. Once the wizard reports the install online, "Use this
//  device" hands its address back to the onboarding form; the first sign
//  in sets the owner password, as it does from a browser.
//
//  Wire format (see setup_ble.py): chunk = stream id, flags (bit 0 =
//  last), payload. Request = JSON {m, p, b}; answer = raw DEFLATE of JSON
//  {s, t, b}. The Android port is BluetoothSetupView.kt.
//

import AuthenticationServices
import Compression
import CoreBluetooth
import CryptoKit
import SwiftUI
import WebKit

enum BLESetupUUID {
    static let service = CBUUID(string: "0F7C5E70-0B1E-4B8A-9C2D-5E7A1C0D0001")
    static let request = CBUUID(string: "0F7C5E70-0B1E-4B8A-9C2D-5E7A1C0D0002")
    static let response = CBUUID(string: "0F7C5E70-0B1E-4B8A-9C2D-5E7A1C0D0003")
    static let info = CBUUID(string: "0F7C5E70-0B1E-4B8A-9C2D-5E7A1C0D0004")
}

struct BLESetupAnswer {
    let status: Int
    let contentType: String
    let body: Data
}

/// The Bluetooth side: finds a device advertising the setup service,
/// connects, and turns `request(...)` calls into chunked writes with the
/// answer reassembled from notifications. Everything runs on the main
/// queue (CoreBluetooth is created with it), so no locking.
final class BLESetupTransport: NSObject, ObservableObject, CBCentralManagerDelegate, CBPeripheralDelegate {
    /// Sent with every request: a device being set up answers only the
    /// first phone that talked to it - this key, not the phone's Bluetooth
    /// address, which changes every few minutes. Kept for the life of the
    /// app install, so reopening the app mid-setup still works.
    static let setupKey: String = {
        let k = "otcSetupKey"
        if let v = UserDefaults.standard.string(forKey: k), !v.isEmpty { return v }
        var bytes = [UInt8](repeating: 0, count: 24)
        _ = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)
        let v = bytes.map { String(format: "%02x", $0) }.joined()
        UserDefaults.standard.set(v, forKey: k)
        return v
    }()

    enum Phase: Equatable {
        case starting, off, unauthorized, scanning, choosing, connecting, ready(String), lost
    }

    @Published var phase: Phase = .starting
    /// Set from the wizard's /api/state once the install is online: the
    /// device's domain, or "" for a device set up without the bridge.
    @Published var readyDomain: String?
    /// The device came from a recovered array: it keeps its password.
    @Published var readyRecovery = false
    /// The owner password chosen in the wizard, handed over by its page
    /// (memory only): the app signs in with it at the end.
    @Published var chosenPassword = ""
    /// The device's domain as soon as the wizard has one (after the name
    /// step; "" without the bridge), nil before: with chosenPassword it is
    /// saved as the pending setup, so a phone locked through the install -
    /// whose Bluetooth link never comes back, the device drops it once
    /// installed - still finds the device at the next launch.
    @Published var setupDomain: String?
    /// Whether the wizard has been reached once: from then on a dropped
    /// link keeps the page on screen (with a banner) while it reconnects,
    /// instead of throwing the install's progress away.
    @Published var everReady = false

    /// A device in range announcing the setup service: its short ID (the
    /// "OTC 04B6" it advertises; an older image's announces none) and how
    /// strong its signal is, so the owner can tell several apart.
    struct Found: Identifiable {
        let id: UUID
        var shortID: String
        var rssi: Int
        let peripheral: CBPeripheral
    }
    /// Every device found, when there is more than one to choose from.
    @Published var found: [Found] = []
    /// The device picked (or the only one found): a dropped link
    /// reconnects to it and never to another one in range.
    private var chosen: UUID?
    private var decideScheduled = false

    private var central: CBCentralManager!
    private var peripheral: CBPeripheral?
    private var requestChrc: CBCharacteristic?
    private var responseChrc: CBCharacteristic?
    private var partial: [UInt8: Data] = [:]
    private var waiters: [UInt8: CheckedContinuation<Data, Error>] = [:]
    private var nextStream: UInt8 = 0

    override init() {
        super.init()
        central = CBCentralManager(delegate: self, queue: nil)
    }

    /// Leaving the setup: a device not yet installing is given back, so
    /// another phone can set it up (it answers only the first one).
    @MainActor
    func releaseAndStop() async {
        if isReady { _ = try? await request(method: "POST", path: "/__otc/release", body: nil) }
        stop()
    }

    func stop() {
        central.stopScan()
        if let p = peripheral { central.cancelPeripheralConnection(p) }
        failAll(BLESetupError.closed)
    }

    var isReady: Bool {
        if case .ready = phase { return true }
        return false
    }

    // MARK: requests

    @MainActor
    func request(method: String, path: String, body: Data?) async throws -> BLESetupAnswer {
        guard let peripheral, let requestChrc, isReady else {
            print("[ble] \(method) \(path): not connected (phase \(phase))")
            throw BLESetupError.notConnected
        }
        nextStream &+= 1
        let stream = nextStream
        var message: [String: Any] = ["m": method, "p": path, "k": Self.setupKey]
        if let body, !body.isEmpty { message["b"] = String(decoding: body, as: UTF8.self) }
        // The biggest notification this phone takes whole: MTU-3, and
        // never more than the 512 bytes an attribute value can hold.
        message["c"] = min(512, peripheral.maximumWriteValueLength(for: .withoutResponse))
        let payload = try JSONSerialization.data(withJSONObject: message)
        // CoreBluetooth queues writes-with-response in order, so all the
        // chunks can go out at once; stream ids keep answers apart.
        let size = max(18, peripheral.maximumWriteValueLength(for: .withResponse) - 2)
        print("[ble] → \(method) \(path) stream \(stream): \(payload.count) bytes in chunks of \(size)")
        var offset = 0
        repeat {
            let end = min(offset + size, payload.count)
            let last = end >= payload.count
            var chunk = Data([stream, last ? 1 : 0])
            chunk.append(payload[offset..<end])
            peripheral.writeValue(chunk, for: requestChrc, type: .withResponse)
            offset = end
        } while offset < payload.count

        // The answer arrives chunk by chunk in didUpdateValueFor, which
        // resumes the waiter; a timer resumes it with an error instead if
        // the device never answers.
        let timeout = Task { [weak self] in
            try? await Task.sleep(for: .seconds(90))
            await MainActor.run { self?.waiters.removeValue(forKey: stream)?.resume(throwing: BLESetupError.timeout) }
        }
        let raw: Data = try await withCheckedThrowingContinuation { cont in
            waiters[stream] = cont
        }
        timeout.cancel()
        guard let inflated = try? (raw as NSData).decompressed(using: .zlib) as Data else {
            print("[ble] ← stream \(stream): \(raw.count) bytes that don't inflate: \(raw.prefix(16).map { String(format: "%02x", $0) }.joined())")
            throw BLESetupError.badAnswer
        }
        guard let obj = try? JSONSerialization.jsonObject(with: inflated) as? [String: Any] else {
            print("[ble] ← stream \(stream): \(inflated.count) inflated bytes that aren't JSON: \(String(decoding: inflated.prefix(80), as: UTF8.self))")
            throw BLESetupError.badAnswer
        }
        print("[ble] ← \(method) \(path) stream \(stream): \(raw.count) bytes, status \(obj["s"] ?? "?")")
        let bodyText = obj["b"] as? String ?? ""
        let answer = BLESetupAnswer(status: obj["s"] as? Int ?? 502, contentType: obj["t"] as? String ?? "application/octet-stream", body: Data(bodyText.utf8))
        if path.hasPrefix("/api/state"), answer.status == 200 { noteState(bodyText) }
        return answer
    }

    /// The wizard's own state tells when the device is ready for the app.
    private func noteState(_ json: String) {
        guard let st = try? JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any] else { return }
        if !(st["name"] as? String ?? "").isEmpty {
            let d = st["domain"] as? String ?? ""
            if setupDomain != d { setupDomain = d }
        }
        guard let install = st["install"] as? [String: Any], install["phase"] as? String == "online" else { return }
        let domain = (install["domain"] as? String ?? "").isEmpty ? (st["domain"] as? String ?? "") : install["domain"] as? String ?? ""
        if readyDomain != domain { readyDomain = domain }
        let recovery = install["recovery"] as? Bool ?? false
        if readyRecovery != recovery { readyRecovery = recovery }
    }

    private func failAll(_ error: Error) {
        let pending = waiters
        waiters = [:]
        partial = [:]
        pending.values.forEach { $0.resume(throwing: error) }
    }

    // MARK: CBCentralManagerDelegate

    func centralManagerDidUpdateState(_ central: CBCentralManager) {
        switch central.state {
        case .poweredOn:
            if chosen == nil { phase = .scanning }
            central.scanForPeripherals(withServices: [BLESetupUUID.service], options: [CBCentralManagerScanOptionAllowDuplicatesKey: true])
        case .unauthorized: phase = .unauthorized
        case .poweredOff: phase = .off
        default: phase = .starting
        }
    }

    func centralManager(_ central: CBCentralManager, didDiscover peripheral: CBPeripheral, advertisementData: [String: Any], rssi RSSI: NSNumber) {
        guard self.peripheral == nil else { return }
        if let chosen {
            // Reconnecting: only ever to the device picked.
            if peripheral.identifier == chosen { connect(peripheral) }
            return
        }
        let name = advertisementData[CBAdvertisementDataLocalNameKey] as? String ?? peripheral.name ?? ""
        let shortID = name.hasPrefix("OTC ") ? String(name.dropFirst(4)) : ""
        if let i = found.firstIndex(where: { $0.id == peripheral.identifier }) {
            found[i].rssi = RSSI.intValue
            if !shortID.isEmpty { found[i].shortID = shortID }
        } else {
            found.append(Found(id: peripheral.identifier, shortID: shortID, rssi: RSSI.intValue, peripheral: peripheral))
        }
        // Listen a moment for others: one device is connected to right
        // away, several are listed for the owner to pick.
        if !decideScheduled {
            decideScheduled = true
            DispatchQueue.main.asyncAfter(deadline: .now() + 2.5) { [weak self] in
                guard let self, self.chosen == nil else { return }
                if self.found.count == 1 { self.choose(self.found[0].id) } else { self.phase = .choosing }
            }
        }
    }

    /// Sets up the device with this id (from `found`).
    func choose(_ id: UUID) {
        guard let dev = found.first(where: { $0.id == id }) else { return }
        chosen = id
        connect(dev.peripheral)
    }

    private func connect(_ p: CBPeripheral) {
        peripheral = p
        p.delegate = self
        phase = .connecting
        central.stopScan()
        central.connect(p)
    }

    func centralManager(_ central: CBCentralManager, didConnect peripheral: CBPeripheral) {
        peripheral.discoverServices([BLESetupUUID.service])
    }

    func centralManager(_ central: CBCentralManager, didFailToConnect peripheral: CBPeripheral, error: Error?) {
        dropAndRescan()
    }

    func centralManager(_ central: CBCentralManager, didDisconnectPeripheral peripheral: CBPeripheral, error: Error?) {
        dropAndRescan()
    }

    private func dropAndRescan() {
        peripheral = nil
        requestChrc = nil
        responseChrc = nil
        failAll(BLESetupError.notConnected)
        phase = .lost
        if central.state == .poweredOn {
            if chosen == nil { phase = .scanning }
            central.scanForPeripherals(withServices: [BLESetupUUID.service], options: nil)
        }
    }

    // MARK: CBPeripheralDelegate

    func peripheral(_ peripheral: CBPeripheral, didDiscoverServices error: Error?) {
        guard let service = peripheral.services?.first(where: { $0.uuid == BLESetupUUID.service }) else { return }
        peripheral.discoverCharacteristics([BLESetupUUID.request, BLESetupUUID.response, BLESetupUUID.info], for: service)
    }

    func peripheral(_ peripheral: CBPeripheral, didDiscoverCharacteristicsFor service: CBService, error: Error?) {
        for c in service.characteristics ?? [] {
            switch c.uuid {
            case BLESetupUUID.request: requestChrc = c
            case BLESetupUUID.response:
                responseChrc = c
                peripheral.setNotifyValue(true, for: c)
            case BLESetupUUID.info: peripheral.readValue(for: c)
            default: break
            }
        }
    }

    func peripheral(_ peripheral: CBPeripheral, didUpdateNotificationStateFor characteristic: CBCharacteristic, error: Error?) {
        print("[ble] notify state for \(characteristic.uuid): \(characteristic.isNotifying) \(error.map { "error \($0)" } ?? "")")
        if characteristic.uuid == BLESetupUUID.response, characteristic.isNotifying, requestChrc != nil, !isReady {
            phase = .ready(peripheral.name ?? "Off The Cloud")
            everReady = true
        }
    }

    // The device's service can go away while the Bluetooth link itself
    // stays up (its daemon restarting): requests would then wait for an
    // answer that never comes. Either sign means the link is useless -
    // drop it, which reconnects (and shows the banner) instead.
    func peripheral(_ peripheral: CBPeripheral, didWriteValueFor characteristic: CBCharacteristic, error: Error?) {
        guard let error else { return }
        print("[ble] write failed: \(error) - reconnecting")
        central.cancelPeripheralConnection(peripheral)
    }

    func peripheral(_ peripheral: CBPeripheral, didModifyServices invalidatedServices: [CBService]) {
        print("[ble] services changed: \(invalidatedServices.map { $0.uuid })")
        if invalidatedServices.contains(where: { $0.uuid == BLESetupUUID.service }) {
            central.cancelPeripheralConnection(peripheral)
        }
    }

    func peripheral(_ peripheral: CBPeripheral, didUpdateValueFor characteristic: CBCharacteristic, error: Error?) {
        guard let value = characteristic.value else { return }
        if characteristic.uuid == BLESetupUUID.info {
            if let obj = try? JSONSerialization.jsonObject(with: value) as? [String: Any], let name = obj["name"] as? String, isReady {
                phase = .ready(name)
            }
            return
        }
        guard characteristic.uuid == BLESetupUUID.response, value.count >= 2 else {
            print("[ble] value for \(characteristic.uuid): \(value.count) bytes \(error.map { "error \($0)" } ?? "")")
            return
        }
        let stream = value[value.startIndex]
        let last = value[value.startIndex + 1] & 1 == 1
        partial[stream, default: Data()].append(value.dropFirst(2))
        guard last, let message = partial.removeValue(forKey: stream) else { return }
        waiters.removeValue(forKey: stream)?.resume(returning: message)
    }
}

enum BLESetupError: LocalizedError {
    case notConnected, timeout, badAnswer, closed

    var errorDescription: String? {
        switch self {
        case .notConnected: return "Not connected to the device"
        case .timeout: return "The device did not answer in time"
        case .badAnswer: return "The device sent an answer this app could not read"
        case .closed: return "Setup was closed"
        }
    }
}

/// Serves the wizard's page to the web view under otc-setup://device/,
/// answering each request from the Bluetooth transport. The page's own
/// fetch() calls go through `BLESetupScriptBridge` instead (a script
/// injected into the page swaps fetch for a call into the app): a scheme
/// handler sees no POST bodies reliably, and Android's WebView none at
/// all, so both apps route the API calls the same way.
final class BLESetupSchemeHandler: NSObject, WKURLSchemeHandler {
    static let scheme = "otc-setup"
    /// What the page runs before its own script: fetch() → the app.
    static let fetchOverride = """
    <script>window.otcApp=1;window.otcSetupPassword=function(p){window.webkit.messageHandlers.otcPassword.postMessage(String(p))};window.otcSetupSignIn=function(p){return window.webkit.messageHandlers.otcSignIn.postMessage({p:String(p)})};window.fetch=function(u,o){o=o||{};const m=(o.method||'GET').toUpperCase();const b=o.body?String(o.body):'';return window.webkit.messageHandlers.otcSetup.postMessage({m:m,p:String(u),b:b}).then(function(r){return new Response(r.b,{status:r.s,headers:{'Content-Type':r.t}})})};</script>
    """
    private let transport: BLESetupTransport
    private var live: Set<ObjectIdentifier> = []

    init(transport: BLESetupTransport) {
        self.transport = transport
    }

    func webView(_ webView: WKWebView, start task: WKURLSchemeTask) {
        let id = ObjectIdentifier(task)
        live.insert(id)
        let request = task.request
        guard let url = request.url else { return }
        var path = url.path.isEmpty ? "/" : url.path
        if let q = url.query, !q.isEmpty { path += "?" + q }
        print("[ble] page request \(request.httpMethod ?? "GET") \(path)")
        Task { @MainActor in
            do {
                let a = try await transport.request(method: request.httpMethod ?? "GET", path: path, body: request.httpBody)
                guard live.contains(id) else { print("[ble] page request \(path) answered after the web view gave up"); return }
                var body = a.body
                if a.contentType.hasPrefix("text/html"), let html = String(data: body, encoding: .utf8), let r = html.range(of: "<head>") {
                    body = Data(html.replacingCharacters(in: r, with: "<head>" + Self.fetchOverride).utf8)
                }
                let headers = ["Content-Type": a.contentType, "Cache-Control": "no-store", "Content-Length": String(body.count)]
                let response = HTTPURLResponse(url: url, statusCode: a.status, httpVersion: "HTTP/1.1", headerFields: headers)!
                task.didReceive(response)
                task.didReceive(body)
                task.didFinish()
            } catch {
                print("[ble] page request \(path) failed: \(error)")
                guard live.contains(id) else { return }
                task.didFailWithError(error)
            }
            live.remove(id)
        }
    }

    func webView(_ webView: WKWebView, stop task: WKURLSchemeTask) {
        live.remove(ObjectIdentifier(task))
    }
}

/// The page's fetch() calls, answered over Bluetooth.
final class BLESetupScriptBridge: NSObject, WKScriptMessageHandlerWithReply {
    private let transport: BLESetupTransport

    init(transport: BLESetupTransport) {
        self.transport = transport
    }

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage, replyHandler: @escaping (Any?, String?) -> Void) {
        guard isFromSetupPage(message) else { return replyHandler(nil, "not allowed") }
        guard let req = message.body as? [String: Any] else { return replyHandler(nil, "bad request") }
        let method = req["m"] as? String ?? "GET"
        var path = req["p"] as? String ?? "/"
        if path.contains("://"), let u = URL(string: path) { path = u.path + (u.query.map { "?" + $0 } ?? "") }
        let body = (req["b"] as? String).map { Data($0.utf8) }
        Task { @MainActor in
            do {
                let a = try await transport.request(method: method, path: path, body: body)
                replyHandler(["s": a.status, "t": a.contentType, "b": String(decoding: a.body, as: UTF8.self)], nil)
            } catch {
                replyHandler(nil, error.localizedDescription)
            }
        }
    }
}

/// The setup page's native calls (Bluetooth requests, sign-in, the
/// password) answer only the setup page itself, in its main frame - not a
/// page it navigated to, nor a frame inside it.
func isFromSetupPage(_ message: WKScriptMessage) -> Bool {
    message.frameInfo.isMainFrame && message.frameInfo.securityOrigin.protocol == BLESetupSchemeHandler.scheme
}

/// Keeps the setup web view on the setup page: anything else a link points
/// at opens in the phone's browser, never inside this view (where the
/// native calls above live).
final class BLESetupNavigationGuard: NSObject, WKNavigationDelegate {
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let url = action.request.url else { return decisionHandler(.cancel) }
        if url.scheme == BLESetupSchemeHandler.scheme || url.absoluteString == "about:blank" {
            return decisionHandler(.allow)
        }
        if action.targetFrame?.isMainFrame ?? true, url.scheme == "https" || url.scheme == "http" {
            DispatchQueue.main.async { UIApplication.shared.open(url) }
        }
        decisionHandler(.cancel)
    }
}

struct BLESetupWebView: UIViewRepresentable {
    let transport: BLESetupTransport

    func makeCoordinator() -> BLESetupNavigationGuard { BLESetupNavigationGuard() }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.setURLSchemeHandler(BLESetupSchemeHandler(transport: transport), forURLScheme: BLESetupSchemeHandler.scheme)
        config.userContentController.addScriptMessageHandler(BLESetupScriptBridge(transport: transport), contentWorld: .page, name: "otcSetup")
        config.userContentController.addScriptMessageHandler(BLESetupSignIn(), contentWorld: .page, name: "otcSignIn")
        config.userContentController.add(BLESetupPasswordHandler(transport: transport), contentWorld: .page, name: "otcPassword")
        let view = WKWebView(frame: .zero, configuration: config)
        view.navigationDelegate = context.coordinator
        view.isOpaque = false
        view.backgroundColor = UIColor(red: 0.118, green: 0.122, blue: 0.133, alpha: 1) // the wizard's own background
        view.load(URLRequest(url: URL(string: "\(BLESetupSchemeHandler.scheme)://device/")!))
        return view
    }

    func updateUIView(_ uiView: WKWebView, context: Context) {}
}

struct BluetoothSetupView: View {
    /// Called with the endpoint the app should use for the new device.
    /// The new device's address and its password: the app signs in with
    /// them right away (the first sign-in sets the owner password).
    let onUseDevice: (String, String) -> Void
    @State private var password = ""
    @Environment(\.dismiss) private var dismiss
    @StateObject private var transport = BLESetupTransport()

    var body: some View {
        VStack(spacing: 0) {
            if transport.everReady {
                if !transport.isReady {
                    // The page stays; its 3-second state poll picks up
                    // where it left off once the link is back.
                    HStack(spacing: 8) {
                        ProgressView()
                        Text("Connection to the device lost - reconnecting. Keep the phone next to it.")
                            .font(.footnote)
                    }
                    .frame(maxWidth: .infinity)
                    .padding(10)
                    .background(Color.orange.opacity(0.25))
                }
                BLESetupWebView(transport: transport)
                    .ignoresSafeArea(edges: .bottom)
            } else {
                waiting
            }
            if let domain = transport.readyDomain {
                let endpoint = Self.endpoint(forDomain: domain)
                // The password chosen in the wizard, when the page handed it
                // over; otherwise (a recovered device, or the app restarted
                // mid-setup) the owner types the device's password.
                let known = transport.chosenPassword
                let valid = !known.isEmpty || !password.isEmpty
                VStack(alignment: .leading, spacing: 8) {
                    Text(domain.isEmpty ? "The device is ready on your home network." : "The device is ready as \(domain).")
                        .font(.footnote).foregroundStyle(.secondary)
                    // Issue #137: the password is chosen here, in the app,
                    // which then signs in with it straight away - the first
                    // sign-in to a new device sets it. A recovered device
                    // keeps the one it had.
                    if known.isEmpty {
                        Text("Enter the device's password").font(.headline)
                        SecureField("Password", text: $password)
                            .textContentType(.password)
                            .textFieldStyle(.roundedBorder)
                    } else {
                        Text("Signing in with the password you chose in the setup.")
                            .font(.footnote).foregroundStyle(.secondary)
                    }
                    Button {
                        onUseDevice(endpoint, known.isEmpty ? password : known)
                        dismiss()
                    } label: {
                        Text("Open my device").frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(!valid)
                }
                .padding()
                .background(.bar)
            }
        }
        .navigationTitle("Set up a new device")
        .navigationBarTitleDisplayMode(.inline)
        .onChange(of: transport.chosenPassword) { _, _ in savePending() }
        .onChange(of: transport.setupDomain) { _, _ in savePending() }
        // The install takes about 20 minutes: a locked phone suspends the
        // app and drops the Bluetooth link, so keep the screen on here.
        .onAppear { UIApplication.shared.isIdleTimerDisabled = true }
        .onDisappear {
            UIApplication.shared.isIdleTimerDisabled = false
            Task { await transport.releaseAndStop() }
        }
    }

    private var waiting: some View {
        VStack(spacing: 14) {
            Spacer()
            switch transport.phase {
            case .starting, .scanning, .lost:
                ProgressView()
                Text("Looking for a device to set up…").font(.headline)
                Text("Power the device on with its disks connected. Until it is set up it announces itself over Bluetooth; keep the phone next to it.")
                    .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
            case .choosing:
                Text("Which device?").font(.headline)
                Text("Several devices are waiting to be set up. Each one's ID is on its setup page, where \"Blink its light\" shows which box it is. The strongest signal is usually the closest.")
                    .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
                VStack(spacing: 8) {
                    ForEach(transport.found.sorted { $0.rssi > $1.rssi }) { dev in
                        Button { transport.choose(dev.id) } label: {
                            HStack {
                                Image(systemName: "externaldrive.connected.to.line.below")
                                Text(dev.shortID.isEmpty ? "Device (older image)" : "Device \(dev.shortID)").font(.body.monospacedDigit())
                                Spacer()
                                Image(systemName: "cellularbars", variableValue: min(1, max(0, Double(dev.rssi + 95) / 45)))
                            }
                            .padding(12)
                            .frame(maxWidth: .infinity)
                            .background(RoundedRectangle(cornerRadius: 10).fill(Color.secondary.opacity(0.15)))
                        }
                        .buttonStyle(.plain)
                    }
                }
            case .connecting:
                ProgressView()
                Text("Connecting…").font(.headline)
            case .off:
                Image(systemName: "antenna.radiowaves.left.and.right.slash").font(.largeTitle)
                Text("Bluetooth is off").font(.headline)
                Text("Turn Bluetooth on in Control Centre to find the device.").font(.footnote).foregroundStyle(.secondary)
            case .unauthorized:
                Image(systemName: "lock").font(.largeTitle)
                Text("Bluetooth access is needed").font(.headline)
                Text("Allow Bluetooth for Off The Cloud in Settings to set a device up this way.").font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
            case .ready:
                EmptyView()
            }
            Spacer()
        }
        .padding(24)
    }

    /// A bridge domain becomes the usual wss endpoint; a device set up
    /// without the bridge answers on the home network as otc.local.
    /// The new device and its password, kept until the app is signed in
    /// to it (Onboarding fills its form from them).
    private func savePending() {
        guard !transport.chosenPassword.isEmpty, let domain = transport.setupDomain else { return }
        SecretsStore.savePendingSetup(endpoint: Self.endpoint(forDomain: domain), password: transport.chosenPassword)
    }

    static func endpoint(forDomain domain: String) -> String {
        if domain.isEmpty { return "ws://otc.local:8080/ws" }
        if domain.hasSuffix("." + SecretsStore.bridgeDomain) {
            return SecretsStore.bridgeEndpoint(forName: String(domain.dropLast(SecretsStore.bridgeDomain.count + 1)))
        }
        return "wss://" + domain + "/ws"
    }
}


/// "Continue with Apple/Google" in the setup wizard (issue #137): the phone
/// has internet while it sets a device up over Bluetooth, so it runs the
/// bridge's sign-in in the system sign-in sheet and hands the wizard a
/// setup token - no code copied from another device. PKCE (see the
/// bridge's appsignin.go): only a challenge goes out; the redirect back to
/// otcsetup://done carries a one-time code, redeemed here with the secret
/// verifier. Android: BLESetupSignIn in BluetoothSetupView.kt.
final class BLESetupSignIn: NSObject, WKScriptMessageHandlerWithReply, ASWebAuthenticationPresentationContextProviding {
    private var session: ASWebAuthenticationSession?

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage, replyHandler: @escaping (Any?, String?) -> Void) {
        guard isFromSetupPage(message) else { return replyHandler(nil, "not allowed") }
        guard let body = message.body as? [String: Any], let provider = body["p"] as? String,
              provider == "apple" || provider == "google" else {
            return replyHandler(nil, "Unknown sign-in provider")
        }
        let verifier = Self.randomVerifier()
        let challenge = Data(SHA256.hash(data: Data(verifier.utf8))).base64URLEncoded
        var start = URLComponents(string: "https://\(SecretsStore.bridgeDomain)/account/auth/\(provider)/start")!
        start.queryItems = [URLQueryItem(name: "return", value: "otcsetup://done"), URLQueryItem(name: "challenge", value: challenge)]
        let session = ASWebAuthenticationSession(url: start.url!, callbackURLScheme: "otcsetup") { [weak self] callback, error in
            self?.session = nil
            if let error = error as? ASWebAuthenticationSessionError, error.code == .canceledLogin {
                return replyHandler(nil, "Sign-in cancelled")
            }
            guard let callback, let code = URLComponents(url: callback, resolvingAgainstBaseURL: false)?
                    .queryItems?.first(where: { $0.name == "code" })?.value else {
                return replyHandler(nil, error?.localizedDescription ?? "The sign-in did not finish")
            }
            Task {
                do {
                    replyHandler(try await Self.exchange(code: code, verifier: verifier), nil)
                } catch {
                    replyHandler(nil, error.localizedDescription)
                }
            }
        }
        session.presentationContextProvider = self
        session.prefersEphemeralWebBrowserSession = false // remember the Apple ID / Google account
        self.session = session
        session.start()
    }

    func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        UIApplication.shared.connectedScenes.compactMap { ($0 as? UIWindowScene)?.keyWindow }.first ?? ASPresentationAnchor()
    }

    private static func randomVerifier() -> String {
        var bytes = [UInt8](repeating: 0, count: 32)
        _ = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)
        return Data(bytes).base64URLEncoded
    }

    private static func exchange(code: String, verifier: String) async throws -> String {
        var req = URLRequest(url: URL(string: "https://\(SecretsStore.bridgeDomain)/api/account/app-exchange")!)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["code": code, "verifier": verifier])
        let (data, resp) = try await URLSession.shared.data(for: req)
        let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
        guard (resp as? HTTPURLResponse)?.statusCode == 200, let token = obj?["setup_token"] as? String else {
            throw NSError(domain: "signin", code: 1, userInfo: [NSLocalizedDescriptionKey: obj?["error"] as? String ?? "Could not finish the sign-in"])
        }
        return token
    }
}

private extension Data {
    var base64URLEncoded: String {
        base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
    }
}


/// The wizard page's window.otcSetupPassword: the owner password it just
/// sealed for the device, kept in memory for the sign-in at the end.
final class BLESetupPasswordHandler: NSObject, WKScriptMessageHandler {
    private weak var transport: BLESetupTransport?

    init(transport: BLESetupTransport) {
        self.transport = transport
    }

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        guard isFromSetupPage(message) else { return }
        guard let pw = message.body as? String, pw.count >= 8 else { return }
        Task { @MainActor in self.transport?.chosenPassword = pw }
    }
}
