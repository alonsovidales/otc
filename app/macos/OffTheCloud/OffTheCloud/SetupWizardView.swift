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

enum SetupImageError: LocalizedError {
    case badSignature, badHash, http(Int)

    var errorDescription: String? {
        switch self {
        case .badSignature: return "The image's checksum is not signed with the Off The Cloud release key. Nothing was kept."
        case .badHash: return "The downloaded image does not match its signed checksum, so it was deleted. Try again."
        case .http(let code): return "The download failed (HTTP \(code)). Try again in a moment."
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
        case failed(String)
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
                self?.state = .failed(error.localizedDescription)
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

struct SetupWizardView: View {
    enum Step {
        case intro, download, write, continueSetup, bluetooth, phone, done
    }

    @StateObject private var image = SetupImageModel()
    @State private var step: Step = .intro
    @State private var imagerURL: URL? = PiImager.appURL
    @State private var deleted = false
    @State private var usedDomain = ""
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        Group {
            switch step {
            case .bluetooth:
                VStack(spacing: 0) {
                    BluetoothSetupView { domain, password in
                        SettingsStore.shared.apply(domain: domain, password: password)
                        usedDomain = domain
                        step = .done
                    }
                    HStack {
                        Button("Back") { step = .continueSetup }
                        Spacer()
                    }
                    .padding(10)
                }
            default:
                page
                    .frame(width: 520)
                    .padding(28)
            }
        }
        .navigationTitle("Set Up a New Device")
        .onDisappear {
            image.cancel()
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

    // MARK: steps

    private var intro: some View {
        Group {
            header("Set up a new device", "A Raspberry Pi 5 (8 GB) with a microSD card and two USB disks becomes your Off The Cloud device.")
            VStack(alignment: .leading, spacing: 10) {
                bullet("1", "Download the Off The Cloud image (about 550 MB). It is checked against the project's signed checksum.")
                bullet("2", "Write it to the microSD card with Raspberry Pi Imager.")
                bullet("3", "Put the card in the Pi, connect the disks, power it on and finish the setup here over Bluetooth, or on your phone.")
            }
            Text("The download is deleted once the card is written.")
                .font(.footnote).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                Button("Download the Image") {
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
            header("Downloading the image", "Into your Downloads folder, then checked against its signed checksum.")
            switch image.state {
            case .idle, .checkingRelease:
                ProgressView().progressViewStyle(.linear)
                Text("Checking the latest image…").font(.footnote).foregroundStyle(.secondary)
            case .downloading(let done, let total):
                if total > 0 {
                    ProgressView(value: Double(done), total: Double(total))
                    Text("\(Self.mb(done)) of \(Self.mb(total))").font(.footnote.monospacedDigit()).foregroundStyle(.secondary)
                } else {
                    ProgressView().progressViewStyle(.linear)
                    Text(Self.mb(done)).font(.footnote.monospacedDigit()).foregroundStyle(.secondary)
                }
            case .verifying(let p):
                ProgressView(value: p)
                Text("Checking the download…").font(.footnote).foregroundStyle(.secondary)
            case .ready:
                Label("Downloaded and verified: \(SetupImage.name)", systemImage: "checkmark.seal.fill")
                    .foregroundStyle(.green)
            case .failed(let message):
                Label(message, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
            }
            HStack {
                Spacer()
                switch image.state {
                case .ready:
                    Button("Next") { step = .write }
                        .buttonStyle(.borderedProminent)
                        .keyboardShortcut(.defaultAction)
                case .failed:
                    Button("Cancel") { dismiss() }
                    Button("Try Again") { image.start() }
                        .buttonStyle(.borderedProminent)
                default:
                    Button("Cancel") {
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
            header("Write the card", "macOS only lets apps outside the App Store write SD cards, so Raspberry Pi Imager does this step.")
            if imagerURL != nil {
                VStack(alignment: .leading, spacing: 10) {
                    bullet("1", "Insert the microSD card and click Open Raspberry Pi Imager below.")
                    bullet("2", "Choose Device: Raspberry Pi 5.")
                    bullet("3", "Choose OS: scroll down to Use custom and pick \(SetupImage.name) in your Downloads folder (it is selected in Finder) - unless Imager already shows it.")
                    bullet("4", "Choose Storage: your SD card. Everything on it is erased.")
                    bullet("5", "Click Next and answer No to \"apply OS customisation settings\": the device is set up from this Mac or your phone.")
                }
                HStack {
                    Button("Show in Finder") { NSWorkspace.shared.activateFileViewerSelecting([SetupImage.file]) }
                    Spacer()
                    Button("Open Raspberry Pi Imager") {
                        PiImager.open(image: SetupImage.file)
                        NSWorkspace.shared.activateFileViewerSelecting([SetupImage.file])
                    }
                }
            } else {
                Text("Raspberry Pi Imager isn't installed. It's free, from the Raspberry Pi Foundation.")
                HStack {
                    Button("Get Raspberry Pi Imager") { NSWorkspace.shared.open(PiImager.website) }
                    Button("Check Again") { imagerURL = PiImager.appURL }
                    Spacer()
                }
            }
            Divider()
            HStack {
                Text("When Imager says the card is written, come back here.")
                    .font(.footnote).foregroundStyle(.secondary)
                Spacer()
                Button("I've Written the Card") {
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
            header("Start the device", "Put the card in the Pi, connect the two USB disks, and power it on. It takes a minute or two to start.")
            if deleted {
                Label("The downloaded image was deleted from your Downloads folder.", systemImage: "trash")
                    .font(.footnote).foregroundStyle(.secondary)
            }
            Text("Then finish the setup - WiFi, the device's name, its disks and your password:")
            HStack(alignment: .top, spacing: 12) {
                choice(icon: "laptopcomputer", title: "On this Mac", text: "Over Bluetooth, with this Mac near the device.") { step = .bluetooth }
                choice(icon: "iphone", title: "On my phone", text: "In the Off The Cloud app for iPhone or Android.") { step = .phone }
            }
            HStack {
                Spacer()
                Button("Close") { dismiss() }
            }
        }
    }

    private var phone: some View {
        Group {
            header("Continue on your phone", "Keep the phone near the device.")
            VStack(alignment: .leading, spacing: 10) {
                bullet("•", "In the Off The Cloud app for iPhone or Android, tap Set up a new device. The app finds the device over Bluetooth and shows its setup.")
                bullet("•", "Or, from any phone or computer, join the WiFi network \"Off The Cloud\": the setup page opens by itself (if it doesn't, open http://10.42.0.1).")
            }
            Text("Once it is installed, add the device to this Mac from the menu bar: Settings, then its name and password.")
                .font(.footnote).foregroundStyle(.secondary)
            HStack {
                Button("Back") { step = .continueSetup }
                Spacer()
                Button("Done") { dismiss() }
                    .buttonStyle(.borderedProminent)
            }
        }
    }

    private var done: some View {
        Group {
            header("Your device is ready", usedDomain.isEmpty ? "" : "This Mac now syncs with \(usedDomain).")
            Text("Add folders to back up or keep in sync from the Off The Cloud menu bar icon. The device's own page, in a browser, has your photos, files and settings.")
            HStack {
                Spacer()
                Button("Done") { dismiss() }
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
            }
        }
    }

    // MARK: pieces

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

    private static func mb(_ bytes: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: bytes, countStyle: .file)
    }
}
