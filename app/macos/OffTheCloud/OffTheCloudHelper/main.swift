// SPDX-License-Identifier: AGPL-3.0-or-later

// Off The Cloud's helper: opens the app again after the Mac App Store
// updates it.
//
// An App Store update quits the app, puts the new version in place, and
// opens it again only if it had a window on screen. A menu bar app never
// has one ("Skipping relaunch — app was not visible before termination"),
// so after every update the app stayed closed and nothing synced until its
// owner noticed the icon was gone (2.4 and 2.5, 2026-10-30 and 2026-11-05).
// The update leaves a helper inside the bundle running (Apple DTS,
// developer forums thread 763268).
//
// This helper is a launchd agent (Contents/Library/LaunchAgents/
// cloud.off-the.OffTheCloud.Helper.plist), registered by LoginItem.swift
// only once the owner chose Start at Login (App Store guideline
// 2.4.5(iii)), and it is what starts the app at login. Whenever it finds
// the app gone - after an update, or a crash - it opens it again, unless
// the owner quit it with the popover's Quit, which tells it so; then the
// app stays closed until the owner opens it, or logs in again. launchd
// keeps the helper itself running (KeepAlive). After it opens the app it
// exits, so the helper launchd starts next is the one the update put in
// place, not this one running from the old bundle's files.

import AppKit
import os

private let log = Logger(subsystem: "cloud.off-the.OffTheCloud.Helper", category: "helper")

/// The app: this helper sits in its Contents/Library/LoginItems. After an
/// update the same path holds the new version.
private let appURL = Bundle.main.bundleURL
    .deletingLastPathComponent() // LoginItems
    .deletingLastPathComponent() // Library
    .deletingLastPathComponent() // Contents
    .deletingLastPathComponent() // OffTheCloud.app

/// The app's bundle id: this helper's, without ".Helper". The app posts its
/// signals under it (HelperSignal in LoginItem.swift).
private let appID = (Bundle.main.bundleIdentifier ?? "")
    .split(separator: ".").dropLast().joined(separator: ".")

final class Watchdog: NSObject {
    private let defaults = UserDefaults.standard
    /// The owner quit the app with Quit: it isn't opened until they open it
    /// themselves, or log in again.
    private let quitKey = "quitByOwner"
    /// When the app was opened, for the last hour.
    private let opensKey = "opens"
    /// How often the helper looks for the app. Gone at two looks in a row
    /// before it is opened: at the first, the owner's Quit may not have
    /// arrived yet, or the update may still be putting the new version in
    /// place.
    private let every: TimeInterval = 10
    /// An app that keeps stopping right after it opens is opened at most
    /// this many times an hour.
    private let maxOpensPerHour = 5

    private var gone = 0
    private var failures = 0
    private var nextTry = Date.distantPast
    private var timer: Timer?
    private var term: DispatchSourceSignal?

    func start() {
        let center = DistributedNotificationCenter.default()
        center.addObserver(self, selector: #selector(quitByOwner), name: .init(appID + ".quitByOwner"),
                           object: nil, suspensionBehavior: .deliverImmediately)
        center.addObserver(self, selector: #selector(launched), name: .init(appID + ".launched"),
                           object: nil, suspensionBehavior: .deliverImmediately)

        // launchd stops its agents with SIGTERM when the owner logs out (and
        // when the helper is unregistered): a Quit is forgotten then, so the
        // next login starts the app, as Start at Login says.
        signal(SIGTERM, SIG_IGN)
        let term = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .main)
        term.setEventHandler { [weak self] in
            if let self {
                defaults.removeObject(forKey: quitKey)
                defaults.synchronize() // written before the exit
            }
            exit(0)
        }
        term.resume()
        self.term = term

        let timer = Timer(timeInterval: every, repeats: true) { [weak self] _ in self?.look() }
        timer.tolerance = every / 2
        RunLoop.main.add(timer, forMode: .common)
        self.timer = timer
        look()
    }

    @objc private func quitByOwner() {
        log.info("the owner quit the app")
        defaults.set(true, forKey: quitKey)
    }

    @objc private func launched() {
        defaults.removeObject(forKey: quitKey)
        gone = 0
    }

    private func look() {
        guard NSRunningApplication.runningApplications(withBundleIdentifier: appID).isEmpty,
              !defaults.bool(forKey: quitKey) else {
            gone = 0
            return
        }
        gone += 1
        guard gone >= 2, Date() >= nextTry else { return }
        open()
    }

    private func open() {
        // Moved to the Bin, say: nothing to open.
        guard FileManager.default.fileExists(atPath: appURL.path) else { return }
        let now = Date().timeIntervalSince1970
        var opens = (defaults.array(forKey: opensKey) as? [Double] ?? []).filter { now - $0 < 3600 }
        guard opens.count < maxOpensPerHour else { return }

        let config = NSWorkspace.OpenConfiguration()
        config.activates = false
        config.addsToRecentItems = false
        NSWorkspace.shared.openApplication(at: appURL, configuration: config) { _, error in
            DispatchQueue.main.async { [self] in
                if let error {
                    // Tried again later: 20 s, 40 s, ... up to 10 minutes.
                    failures += 1
                    nextTry = Date().addingTimeInterval(min(600, every * pow(2, Double(failures))))
                    log.error("opening the app failed: \(error.localizedDescription, privacy: .public)")
                    return
                }
                opens.append(now)
                defaults.set(opens, forKey: opensKey)
                defaults.synchronize() // written before the exit
                log.info("opened the app")
                // launchd starts the helper again, from the app's bundle as
                // it is now (an update's new version).
                exit(0)
            }
        }
    }
}

let watchdog = Watchdog()
watchdog.start()
RunLoop.main.run()
