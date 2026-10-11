// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SetupWizardView.swift
//  OffTheCloud
//
//  Issue #184: set up a new device from the Mac - download the Raspberry Pi
//  image, get it onto an SD card, then continue the setup over Bluetooth
//  (BluetoothSetupView, the phone apps' setup) or on a phone. otc-sync's
//  tray wizard is the Windows/Linux counterpart; it writes the card itself.
//  This app is sandboxed (App Store) and can't write raw disks, so the card
//  is written by Raspberry Pi Imager with the image this app downloaded and
//  verified.
//
//  Trust: the image's SHA-256 file is signed with the project's release key
//  (the one devices pin for updates, scripts/release-signing.pub; `make
//  image-publish` signs it), and the download is refused unless both the
//  signature and the hash match. What the image installs is then verified
//  on the device itself (scripts/verified-install.sh).
//

import AppKit
import Combine
import CryptoKit
import SwiftUI

enum SetupImage {
    static let base = "https://github.com/alonsovidales/otc/releases/download/image/"
    static let name = "off-the-cloud-rpi-lite-arm64.img.xz"
    /// The raw Ed25519 release key: the last 32 bytes of the DER in
    /// scripts/release-signing.pub (MCowBQYDK2VwAyEA...).
    static let releaseKey = Data(base64Encoded: "MCowBQYDK2VwAyEAtVgLIKBzcqMNM2nUnK9xfgpqWrLTuZsk8ylhyI0BK9g=")!.suffix(32)

    /// ~/Downloads itself, not the sandbox container's: Raspberry Pi
    /// Imager is pointed at the file, and the person sees it in Finder.
    static var downloadsFolder: URL {
        if let pw = getpwuid(getuid()), let home = pw.pointee.pw_dir {
            return URL(fileURLWithPath: String(cString: home)).appendingPathComponent("Downloads", isDirectory: true)
        }
        return FileManager.default.urls(for: .downloadsDirectory, in: .userDomainMask)[0]
    }

    static var file: URL { downloadsFolder.appendingPathComponent(name) }
}

enum SetupImageError: LocalizedError, Equatable {
    case badSignature, badHash, http(Int)

    var errorDescription: String? {
        switch self {
        case .badSignature: return S.macosSetupBadSignature
        case .badHash: return S.macosSetupBadHash
        // The status code as digits, as before ("HTTP 404").
        case .http(let code): return S.macosSetupHttpFailed(code: String(code))
        }
    }
}

/// Why the download stopped, kept as data so the step shows it in the
/// language in effect: one of ours (worded when shown), or the system's
/// own words (a URLError's, already in the system's language).
enum SetupDownloadFailure: Equatable {
    case image(SetupImageError)
    case system(String)

    init(_ error: Error) {
        if let e = error as? SetupImageError {
            self = .image(e)
        } else {
            self = .system(error.localizedDescription)
        }
    }

    var text: String {
        switch self {
        case .image(let e): return e.errorDescription ?? ""
        case .system(let message): return message
        }
    }
}

/// Downloads the image to ~/Downloads and checks it against its signed
/// SHA-256. A file already there that matches is used as it is.
@MainActor
final class SetupImageModel: ObservableObject {
    enum State: Equatable {
        case idle
        case checkingRelease
        case downloading(done: Int64, total: Int64)
        case verifying(Double)
        case ready
        case failed(SetupDownloadFailure)
    }

    @Published var state: State = .idle
    private var task: Task<Void, Never>?
    private var downloader: ImageDownloader?

    func start() {
        guard task == nil else { return }
        state = .checkingRelease
        task = Task { [weak self] in
            do {
                try await self?.run()
            } catch is CancellationError {
                self?.state = .idle
            } catch {
                self?.state = .failed(SetupDownloadFailure(error))
            }
            self?.task = nil
            self?.downloader = nil
        }
    }

    func cancel() {
        downloader?.cancel()
        task?.cancel()
    }

    /// Deletes the download (once the card is written).
    func deleteDownload() {
        try? FileManager.default.removeItem(at: SetupImage.file)
        try? FileManager.default.removeItem(at: SetupImage.file.appendingPathExtension("part"))
    }

    private func run() async throws {
        let expected = try await Self.signedHash()
        let file = SetupImage.file
        // Already downloaded (an earlier, interrupted run): check it.
        if FileManager.default.fileExists(atPath: file.path) {
            if try await hash(of: file) == expected {
                state = .ready
                return
            }
            try? FileManager.default.removeItem(at: file)
        }
        let part = file.appendingPathExtension("part")
        try? FileManager.default.removeItem(at: part)
        state = .downloading(done: 0, total: 0)
        let d = ImageDownloader()
        downloader = d
        try await d.download(from: URL(string: SetupImage.base + SetupImage.name)!, to: part) { [weak self] done, total in
            Task { @MainActor in
                if case .downloading = self?.state { self?.state = .downloading(done: done, total: total) }
            }
        }
        try Task.checkCancellation()
        guard try await hash(of: part) == expected else {
            try? FileManager.default.removeItem(at: part)
            throw SetupImageError.badHash
        }
        try? FileManager.default.removeItem(at: file)
        try FileManager.default.moveItem(at: part, to: file)
        state = .ready
    }

    /// The image's SHA-256 (hex), from its .sha256 file - trusted only
    /// with a valid release-key signature over that file's exact bytes.
    private static func signedHash() async throws -> String {
        let shaURL = URL(string: SetupImage.base + SetupImage.name + ".sha256")!
        let (shaFile, r1) = try await URLSession.shared.data(from: shaURL)
        let (sigFile, r2) = try await URLSession.shared.data(from: shaURL.appendingPathExtension("sig"))
        for r in [r1, r2] {
            if let h = r as? HTTPURLResponse, h.statusCode != 200 { throw SetupImageError.http(h.statusCode) }
        }
        let sigText = String(decoding: sigFile, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        guard let sig = Data(base64Encoded: sigText),
              let key = try? Curve25519.Signing.PublicKey(rawRepresentation: SetupImage.releaseKey),
              key.isValidSignature(sig, for: shaFile) else {
            throw SetupImageError.badSignature
        }
        let hex = String(decoding: shaFile, as: UTF8.self).split(whereSeparator: { $0 == " " || $0 == "\n" || $0 == "\t" }).first.map(String.init) ?? ""
        guard hex.count == 64 else { throw SetupImageError.badSignature }
        return hex.lowercased()
    }

    /// SHA-256 of a file, read in pieces off the main thread.
    private func hash(of url: URL) async throws -> String {
        state = .verifying(0)
        let size = (try? FileManager.default.attributesOfItem(atPath: url.path)[.size] as? Int64) ?? 0
        return try await Task.detached(priority: .userInitiated) { [weak self] in
            let h = try FileHandle(forReadingFrom: url)
            defer { try? h.close() }
            var sha = SHA256()
            var read: Int64 = 0
            var lastReport = 0.0
            while let chunk = try h.read(upToCount: 8 << 20), !chunk.isEmpty {
                try Task.checkCancellation()
                sha.update(data: chunk)
                read += Int64(chunk.count)
                let p = size > 0 ? Double(read) / Double(size) : 0
                if p - lastReport > 0.01 {
                    lastReport = p
                    await MainActor.run { self?.state = .verifying(p) }
                }
            }
            return sha.finalize().map { String(format: "%02x", $0) }.joined()
        }.value
    }
}

/// One download to a file with progress: URLSession's download task, moved
/// to `destination` as soon as it finishes.
final class ImageDownloader: NSObject, URLSessionDownloadDelegate {
    private var session: URLSession?
    private var continuation: CheckedContinuation<Void, Error>?
    private var destination: URL?
    private var progress: ((Int64, Int64) -> Void)?

    func download(from url: URL, to destination: URL, progress: @escaping (Int64, Int64) -> Void) async throws {
        self.destination = destination
        self.progress = progress
        let session = URLSession(configuration: .default, delegate: self, delegateQueue: nil)
        self.session = session
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
            continuation = c
            session.downloadTask(with: url).resume()
        }
        session.finishTasksAndInvalidate()
    }

    func cancel() {
        session?.invalidateAndCancel()
    }

    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didWriteData bytesWritten: Int64, totalBytesWritten: Int64, totalBytesExpectedToWrite: Int64) {
        progress?(totalBytesWritten, max(0, totalBytesExpectedToWrite))
    }

    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didFinishDownloadingTo location: URL) {
        guard let destination else { return }
        if let h = downloadTask.response as? HTTPURLResponse, h.statusCode != 200 {
            finish(SetupImageError.http(h.statusCode))
            return
        }
        do {
            try? FileManager.default.removeItem(at: destination)
            try FileManager.default.moveItem(at: location, to: destination)
            finish(nil)
        } catch {
            finish(error)
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        if let error {
            finish((error as? URLError)?.code == .cancelled ? CancellationError() : error)
        }
    }

    private func finish(_ error: Error?) {
        guard let c = continuation else { return }
        continuation = nil
        if let error { c.resume(throwing: error) } else { c.resume() }
    }
}

/// Raspberry Pi Imager, if this Mac has it.
enum PiImager {
    static let bundleIDs = ["com.raspberrypi.rpi-imager", "org.raspberrypi.imagingutility"]
    static let website = URL(string: "https://www.raspberrypi.com/software/")!

    static var appURL: URL? {
        for id in bundleIDs {
            if let u = NSWorkspace.shared.urlForApplication(withBundleIdentifier: id) { return u }
        }
        let fallback = URL(fileURLWithPath: "/Applications/Raspberry Pi Imager.app")
        return FileManager.default.fileExists(atPath: fallback.path) ? fallback : nil
    }

    /// Opens Imager, passing it the image (Imager preselects an image given
    /// on its command line - when macOS passes the argument on, which it may
    /// not for a sandboxed caller), and shows the image in Finder for "Use
    /// custom".
    static func open(image: URL) {
        guard let app = appURL else { return }
        let config = NSWorkspace.OpenConfiguration()
        config.arguments = [image.path]
        config.activates = true
        NSWorkspace.shared.openApplication(at: app, configuration: config) { _, _ in }
    }
}

/// Where the wizard is, kept outside its window: closing the window, or
/// the window going away while the person is in Imager, resumes at the
/// same step (and a download keeps going). Cancel and Done start over.
@MainActor
final class SetupWizardSession: ObservableObject {
    static let shared = SetupWizardSession()

    @Published var step: SetupWizardView.Step = .intro
    @Published var deleted = false
    @Published var usedDomain = ""
    /// A language was picked in this wizard (its LanguageMenu): it goes to
    /// the device set up, once this Mac syncs with it.
    var languageChosen = false
    let image = SetupImageModel()

    /// The wizard's window while it is open.
    weak var window: NSWindow?

    /// Clicking the menu bar icon while the wizard is open brings its
    /// window back in front of the other apps' windows (without taking
    /// focus from the popover, which would close it).
    func bringToFront() {
        guard let window, window.isVisible else { return }
        window.orderFrontRegardless()
    }

    func reset() {
        image.cancel()
        step = .intro
        deleted = false
        usedDomain = ""
        languageChosen = false
    }
}

struct SetupWizardView: View {
    enum Step {
        case intro, download, write, continueSetup, bluetooth, phone, done
    }

    @ObservedObject private var session = SetupWizardSession.shared
    @ObservedObject private var image = SetupWizardSession.shared.image
    @ObservedObject private var login = LoginItemSettings.shared
    @State private var imagerURL: URL? = PiImager.appURL
    @Environment(\.dismiss) private var dismissWindow

    private var step: Step {
        get { session.step }
        nonmutating set { session.step = newValue }
    }
    private var deleted: Bool {
        get { session.deleted }
        nonmutating set { session.deleted = newValue }
    }
    private var usedDomain: String {
        get { session.usedDomain }
        nonmutating set { session.usedDomain = newValue }
    }

    /// Cancel, Close and Done: the wizard is over, the next one starts
    /// from the beginning.
    private func dismiss() {
        session.reset()
        dismissWindow()
    }

    var body: some View {
        Group {
            switch step {
            case .bluetooth:
                VStack(spacing: 0) {
                    BluetoothSetupView { domain, password in
                        guard Self.switchDevice(to: domain) else { return }
                        // The language picked in this wizard is the new
                        // device's too: sent at its first sign-in.
                        if session.languageChosen { LanguageSettings.shared.carryToNextDevice() }
                        SettingsStore.shared.apply(domain: domain, password: password)
                        usedDomain = domain
                        step = .done
                    }
                    HStack {
                        Button(S.commonBack) { step = .continueSetup }
                        Spacer()
                        // Under the setup page, which follows it
                        // (BLESetupTransport.appLanguageChanged).
                        languageMenu
                    }
                    .padding(10)
                }
            default:
                page
                    .frame(width: 520)
                    .padding(28)
                    // In the margin above the title, at the right: every
                    // step keeps its layout.
                    .overlay(alignment: .topTrailing) {
                        languageMenu
                            .padding(.top, 6)
                            .padding(.trailing, 12)
                    }
            }
        }
        .navigationTitle(S.deskFoldersSetupTitle)
        // A menu bar app has no Dock icon, so its window is hard to find
        // again after switching to Imager: while the wizard is open the app
        // is a regular one, in the Dock and in ⌘-Tab.
        .background(WindowFinder { session.window = $0 })
        .onAppear {
            NSApp.setActivationPolicy(.regular)
            NSApp.activate()
            imagerURL = PiImager.appURL
        }
        .onDisappear {
            NSApp.setActivationPolicy(.accessory)
        }
        // Back from installing Imager: no need to press Check Again.
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            imagerURL = PiImager.appURL
        }
    }

    @ViewBuilder
    private var page: some View {
        VStack(alignment: .leading, spacing: 16) {
            switch step {
            case .intro: intro
            case .download: download
            case .write: write
            case .continueSetup: continueSetup
            case .phone: phone
            case .done: done
            case .bluetooth: EmptyView()
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// Syncing with the new device while another one is set up means
    /// disconnecting from that one first - same as Settings' Disconnect:
    /// its folders are removed, so they never sync with the new device.
    @MainActor
    static func switchDevice(to domain: String) -> Bool {
        let settings = SettingsStore.shared
        guard settings.ready, settings.domain != domain else { return true }
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = S.macosSetupSwitchTitle
        alert.informativeText = S.macosSetupSwitchMessage(device: settings.domain)
        alert.addButton(withTitle: S.macosSetupSwitchButton)
        alert.addButton(withTitle: S.commonCancel)
        guard alert.runModal() == .alertFirstButtonReturn else { return false }
        SyncModel.shared.disconnect()
        return true
    }

    // MARK: steps

    private var intro: some View {
        Group {
            header(S.macosSetupIntroTitle, S.macosSetupIntroSubtitle)
            VStack(alignment: .leading, spacing: 10) {
                bullet("1", S.macosSetupIntroStep1)
                bullet("2", S.macosSetupIntroStep2)
                bullet("3", S.macosSetupIntroStep3)
            }
            Text(S.macosSetupIntroNote)
                .font(.footnote).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button(S.commonCancel) { dismiss() }
                Button(S.macosSetupDownloadButton) {
                    step = .download
                    image.start()
                }
                .buttonStyle(.borderedProminent)
                .keyboardShortcut(.defaultAction)
            }
        }
    }

    private var download: some View {
        Group {
            header(S.macosSetupDownloadTitle, S.macosSetupDownloadSubtitle)
            switch image.state {
            case .idle, .checkingRelease:
                ProgressView().progressViewStyle(.linear)
                Text(S.macosSetupCheckingRelease).font(.footnote).foregroundStyle(.secondary)
            case .downloading(let done, let total):
                if total > 0 {
                    ProgressView(value: Double(done), total: Double(total))
                    Text(S.macosSetupDownloadProgress(done: Self.mb(done), total: Self.mb(total))).font(.footnote.monospacedDigit()).foregroundStyle(.secondary)
                } else {
                    ProgressView().progressViewStyle(.linear)
                    Text(Self.mb(done)).font(.footnote.monospacedDigit()).foregroundStyle(.secondary)
                }
            case .verifying(let p):
                ProgressView(value: p)
                Text(S.macosSetupVerifying).font(.footnote).foregroundStyle(.secondary)
            case .ready:
                Label(S.macosSetupDownloaded(file: SetupImage.name), systemImage: "checkmark.circle.fill")
                    .foregroundStyle(.green)
            case .failed(let failure):
                Label(failure.text, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
            }
            HStack {
                Spacer()
                switch image.state {
                case .ready:
                    Button(S.commonNext) { step = .write }
                        .buttonStyle(.borderedProminent)
                        .keyboardShortcut(.defaultAction)
                case .failed:
                    Button(S.commonCancel) { dismiss() }
                    Button(S.deskFoldersTryAgain) { image.start() }
                        .buttonStyle(.borderedProminent)
                default:
                    Button(S.commonCancel) {
                        image.cancel()
                        image.deleteDownload()
                        step = .intro
                    }
                }
            }
        }
    }

    private var write: some View {
        Group {
            header(S.macosSetupWriteTitle, S.macosSetupWriteSubtitle)
            if imagerURL != nil {
                VStack(alignment: .leading, spacing: 10) {
                    bullet("1", S.macosSetupWriteStep1(button: S.macosSetupOpenImager))
                    bullet("2", S.macosSetupWriteStep2)
                    bullet("3", S.macosSetupWriteStep3(file: SetupImage.name))
                    bullet("4", S.macosSetupWriteStep4)
                    bullet("5", S.macosSetupWriteStep5)
                }
                HStack {
                    Button(S.macosSetupShowInFinder) { NSWorkspace.shared.activateFileViewerSelecting([SetupImage.file]) }
                    Spacer()
                    Button(S.macosSetupOpenImager) {
                        PiImager.open(image: SetupImage.file)
                        NSWorkspace.shared.activateFileViewerSelecting([SetupImage.file])
                    }
                }
            } else {
                Text(S.macosSetupImagerMissing)
                HStack {
                    Button(S.macosSetupGetImager) { NSWorkspace.shared.open(PiImager.website) }
                    Button(S.macosSetupCheckAgain) { imagerURL = PiImager.appURL }
                    Spacer()
                }
            }
            Divider()
            HStack {
                Text(S.macosSetupComeBack)
                    .font(.footnote).foregroundStyle(.secondary)
                Spacer()
                Button(S.macosSetupCardWritten) {
                    image.deleteDownload()
                    deleted = true
                    step = .continueSetup
                }
                .buttonStyle(.borderedProminent)
            }
        }
    }

    private var continueSetup: some View {
        Group {
            header(S.macosSetupStartTitle, S.macosSetupStartSubtitle)
            if deleted {
                Label(S.macosSetupImageDeleted, systemImage: "trash")
                    .font(.footnote).foregroundStyle(.secondary)
            }
            Text(S.macosSetupFinishPrompt)
            HStack(alignment: .top, spacing: 12) {
                choice(icon: "laptopcomputer", title: S.macosSetupOnThisMac, text: S.macosSetupOnThisMacDetail) { step = .bluetooth }
                choice(icon: "iphone", title: S.macosSetupOnMyPhone, text: S.macosSetupOnMyPhoneDetail) { step = .phone }
            }
            HStack {
                Spacer()
                Button(S.commonClose) { dismiss() }
            }
        }
    }

    private var phone: some View {
        Group {
            header(S.macosSetupPhoneTitle, S.macosSetupPhoneSubtitle)
            VStack(alignment: .leading, spacing: 10) {
                bullet("•", S.macosSetupPhoneStep)
            }
            Text(S.macosSetupPhoneThen)
                .font(.footnote).foregroundStyle(.secondary)
            HStack {
                Button(S.commonBack) { step = .continueSetup }
                Spacer()
                Button(S.commonDone) { dismiss() }
                    .buttonStyle(.borderedProminent)
            }
        }
    }

    private var done: some View {
        Group {
            header(S.macosSetupDoneTitle, usedDomain.isEmpty ? "" : S.macosSetupDoneSubtitle(device: usedDomain))
            Text(S.macosSetupDoneText)
            // This Mac was just pointed at the device: the natural moment
            // for the one-time start-at-login offer - the same one the
            // popover shows, so answering either hides both. Not prominent:
            // "Done" stays the page's one accent button (and Return).
            if !usedDomain.isEmpty && login.choice == nil {
                LoginItemOffer(prominent: false) { login.choose($0) }
            }
            HStack {
                Spacer()
                Button(S.commonDone) { dismiss() }
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
            }
        }
    }

    // MARK: pieces

    /// The language for every app, chosen here by someone setting up a
    /// device: no Settings to go to yet, maybe no device to sign in to.
    private var languageMenu: some View {
        LanguageMenu { _ in session.languageChosen = true }
    }

    private func header(_ title: String, _ subtitle: String) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title).font(.title2.bold())
            if !subtitle.isEmpty {
                Text(subtitle).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private func bullet(_ mark: String, _ text: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(mark).font(.callout.bold().monospacedDigit()).frame(width: 16)
            Text(text).fixedSize(horizontal: false, vertical: true)
        }
    }

    private func choice(icon: String, title: String, text: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 6) {
                Image(systemName: icon).font(.title2)
                Text(title).font(.headline)
                Text(text).font(.footnote).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            .padding(14)
            .frame(maxWidth: .infinity, minHeight: 110, alignment: .topLeading)
            .background(RoundedRectangle(cornerRadius: 10).fill(Color.secondary.opacity(0.12)))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    /// A size for the download's progress. ByteCountFormatter speaks the
    /// system's language (it has no locale) and keeps today's words ("Zero
    /// KB", "999 bytes"), so it formats while the app speaks that language;
    /// in another one the size is formatted for the app's language instead
    /// (L10n.locale).
    private static func mb(_ bytes: Int64) -> String {
        let locale = L10n.shared.locale
        if locale.language.languageCode == Locale.autoupdatingCurrent.language.languageCode {
            return ByteCountFormatter.string(fromByteCount: bytes, countStyle: .file)
        }
        return bytes.formatted(.byteCount(style: .file).locale(locale))
    }
}

/// Hands over the NSWindow a SwiftUI view is in.
private struct WindowFinder: NSViewRepresentable {
    var found: (NSWindow?) -> Void

    func makeNSView(context: Context) -> NSView {
        let v = NSView()
        DispatchQueue.main.async { found(v.window) }
        return v
    }

    func updateNSView(_ v: NSView, context: Context) {
        DispatchQueue.main.async { found(v.window) }
    }
}
