// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftUI

/// Backoff for a load that failed - a page of photos, the date buckets, the
/// lists the search and the headers use, a folder's listing - as the web's
/// usePageRetry and Android's retryWaitMs: asked again 1 s after the first
/// failure, then 2, 4, 8 and every 10 s, for as long as it is still wanted
/// (the caller's own check, in `due`). Kept short: a screen should fill
/// soon after the device is back. A load that works resets it, as does one
/// the user starts (a new search, "Try again"); wake() asks again at once
/// at the moments it is worth it (OTCConnection.wakes: signed in again, a
/// network came up, the app back in front).
@MainActor
final class PageRetry {
    nonisolated static let firstDelay: TimeInterval = 1
    nonisolated static let maxDelay: TimeInterval = 10

    /// The wait after `failures` failures in a row: 1, 2, 4, 8, 10, 10...
    /// (from `first`, doubling to `max`).
    nonisolated static func delay(afterFailures failures: Int, first: TimeInterval = firstDelay,
                                  max: TimeInterval = maxDelay) -> TimeInterval {
        guard failures > 0 else { return 0 }
        return Swift.min(first * pow(2, Double(Swift.min(failures, 30) - 1)), max)
    }

    /// Failures in a row since the last load that worked.
    private(set) var failures = 0
    private var timer: Task<Void, Never>?
    private var due: (@MainActor () -> Void)?
    /// The waits, by failures in a row (tests shorten them).
    var delay: (Int) -> TimeInterval = { PageRetry.delay(afterFailures: $0) }
    /// The waits after loads that came back again and again with nothing
    /// new (a device starting a search over each time): 10 s doubling to a
    /// minute, as Android's NO_PROGRESS - each round asks up to 12 pages.
    var noProgressDelay: (Int) -> TimeInterval = { PageRetry.delay(afterFailures: $0, first: 10, max: 60) }

    /// A retry is waiting for its time.
    var waiting: Bool { timer != nil }

    /// The load failed: `due` runs once the wait is over, unless reset()
    /// or another failure came first, or wake() runs it sooner.
    /// `noProgress`: it came, with nothing new (noProgressDelay).
    func failed(noProgress: Bool = false, then due: @escaping @MainActor () -> Void) {
        failures += 1
        timer?.cancel()
        self.due = due
        let wait = noProgress ? noProgressDelay(failures) : delay(failures)
        timer = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .seconds(wait))
            guard !Task.isCancelled, let self else { return }
            self.fire()
        }
    }

    /// The load worked, or the user started a new one: no retry waits, a
    /// keepAsking loop ends, and the next failure waits the first delay
    /// again.
    func reset() {
        timer?.cancel()
        timer = nil
        due = nil
        failures = 0
        looping = false
        loop &+= 1
    }

    /// Now is a good time (OTCConnection.wakes): a retry that waits goes
    /// now (the wait starts over from the first delay should it fail
    /// again). Nothing happens while none waits - one already on its way
    /// included.
    @discardableResult
    func wake() -> Bool {
        guard timer != nil else { return false }
        failures = 0
        fire()
        return true
    }

    private func fire() {
        timer?.cancel()
        timer = nil
        let d = due
        due = nil
        d?()
    }

    // MARK: A list asked again until it comes (Android's keepAsking)

    /// A keepAsking loop is going: waiting, or asking.
    private(set) var looping = false
    /// Which loop: one that reset() ended stops at its next step.
    private var loop = 0

    /// `load` just failed (the caller's own ask): it is asked again after
    /// the wait, and again, until it comes (true) or is no longer `wanted`
    /// (checked after each wait) - one loop at a time, so a loop already
    /// going is left to it. reset() ends it; wake() cuts its wait short.
    func keepAsking(wanted: @escaping @MainActor () -> Bool = { true }, _ load: @escaping @MainActor () async -> Bool) {
        guard !looping else { return }
        looping = true
        loop &+= 1
        askAgain(loop, wanted, load)
    }

    private func askAgain(_ n: Int, _ wanted: @escaping @MainActor () -> Bool, _ load: @escaping @MainActor () async -> Bool) {
        failed { [weak self] in
            guard let self, n == self.loop else { return }
            guard wanted() else {
                self.reset()
                return
            }
            Task { @MainActor [weak self] in
                let ok = await load()
                guard let self, n == self.loop else { return }
                if ok {
                    self.reset()
                } else {
                    self.askAgain(n, wanted, load)
                }
            }
        }
    }
}

/// Why a screen's load failed, as far as the phone can tell (Android's
/// LoadProblem) - what the screen says while it keeps asking: the phone
/// has no network, the bridge says the device isn't connected to it,
/// nothing answered in time, or anything else (an error answer, a dropped
/// connection).
enum LoadProblem: Equatable {
    case offline
    case unreachable
    case slow
    case failed

    /// The bridge's Ack.code for a device it can't reach (deviceStatus.ts
    /// on the web).
    static let deviceUnreachable = "device_unreachable"

    /// `resp`: the answer that came instead of the one asked for (nil: none
    /// came), `error`: what was thrown instead of an answer. `offline`: the
    /// phone has no network (NetworkWatch); `statusCode`: the bridge's last
    /// word on the device at sign-in (OTCConnection.statusCode).
    static func of(resp: Msg_RespEnvelope?, error: Error?, offline: Bool, statusCode: String?) -> LoadProblem {
        if offline { return .offline }
        if let resp, case .respAck(let ack) = resp.payload, ack.code == deviceUnreachable { return .unreachable }
        if error is WSClient.TimedOut { return .slow }
        // Signing in got the bridge's verdict: OTCConnection threw for it.
        if error != nil && statusCode == deviceUnreachable { return .unreachable }
        return .failed
    }

    /// As the phone sees things now.
    @MainActor
    static func now(resp: Msg_RespEnvelope?, error: Error?) -> LoadProblem {
        of(resp: resp, error: error, offline: NetworkWatch.shared.offline, statusCode: OTCConnection.shared.statusCode)
    }

    /// Why `what` ("Your photos", "The posts") isn't here yet, and that it
    /// comes by itself once it can.
    func text(_ what: String) -> String {
        switch self {
        case .offline: return "This phone is offline. \(what) will appear once it's back online."
        case .unreachable: return "Your device isn't reachable right now. \(what) will appear as soon as it answers."
        case .slow: return "Your device took too long to answer. \(what) will appear as soon as it does."
        case .failed: return "\(what) will appear as soon as your device answers."
        }
    }

    /// The phone is back online while a load that failed offline is asked
    /// for again: "This phone is offline" no longer says why - the neutral
    /// line (failed's) until the answer comes or the next failure says
    /// why. Android's backOnline.
    func backOnline(_ online: Bool) -> LoadProblem {
        self == .offline && online ? .failed : self
    }

    /// Images' first page failed: the title, then why (photosText), then
    /// Try again.
    static let photosTitle = "Couldn't load your photos"
    /// Under photosTitle: why, in plain words, and that they come by
    /// themselves.
    var photosText: String { text("Your photos") }
    /// A failed load's button, and while it is asked again.
    static let tryAgain = "Try again"
    static let trying = "Trying…"
    /// A later page failed: the row at the end of the grid, before its Try
    /// again (the web's wording).
    static let morePhotos = "Couldn't load more photos."
    /// The first page has been on its way a while (People's and
    /// Collections' wording).
    static let photosSlow = "Still waiting for your device…"
    /// The grey tiles, to VoiceOver (the web's aria-busy grid).
    static let photosLoading = "Loading photos"
    /// The feed's next page failed: the end of the feed, before its Try again.
    static let morePosts = "Couldn't load more posts."
    /// The viewer: a video that couldn't be fetched or played, over its
    /// poster, before its Try again.
    static let videoUnplayable = "Couldn't play this video."
}

/// What a screen says to VoiceOver when its problem line appears or
/// changes (TalkBack's polite live region, the web's role="status"):
/// announced, not just shown, since nothing on screen took the focus.
enum Announce {
    @MainActor
    static func polite(_ text: String) {
        guard UIAccessibility.isVoiceOverRunning, !text.isEmpty else { return }
        var s = AttributedString(text)
        s.accessibilitySpeechAnnouncementPriority = .low
        AccessibilityNotification.Announcement(s).post()
    }
}

enum PhotoPaging {
    /// Whether a page that went on with a jump's search (by its token) came
    /// from a search the device started again: one that no longer holds the
    /// token (unused for five minutes, or a restart) searches again without
    /// the jump's cutoff, from the newest photo. A held token comes back as
    /// it was sent; the last page of either has none, and then the photos
    /// tell - newer than the cutoff, or already on the screen. The web's
    /// and Android's lostCutoff.
    static func lostCutoff(files: [(created: Date?, path: String)], token: String, sent: String, cutoff: Date, shown: Set<String>) -> Bool {
        if !token.isEmpty { return token != sent }
        if files.contains(where: { ($0.created.map { $0 > cutoff }) ?? false }) { return true }
        return files.contains { shown.contains($0.path) }
    }
}
