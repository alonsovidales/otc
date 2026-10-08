// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation

/// How long a screen's request of one kind (a page of photos, the feed, a
/// folder's listing, the tags...) waits for its answer, by `key`: the kind
/// and the route it goes over (OTCConnection.ask), since the bridge and the
/// home network answer at their own pace. Android's Patience.
///
/// Its usual time (`base`), twice as long after each timeout in a row, up
/// to 5 minutes (timeoutAfter): a device that is slow but working (Pit's
/// pages took 65-70 s even at home) is never cut off for good, while a
/// request nothing will answer still fails and is asked again. An answer
/// brings it back down to the shortest time that would have held it - to
/// the usual time when it came within that - so one slow moment doesn't
/// leave every later request waiting minutes on a device that answers in
/// seconds again. (It used to come back only for an answer under half the
/// usual time, which Pit's pages never were: every page kept 4-5 minutes.)
final class Patience: @unchecked Sendable {
    nonisolated static let maxTimeout: TimeInterval = 300
    private static let maxSteps = 8

    private let lock = NSLock()
    /// Timeouts in a row, by key; absent: the usual time.
    private var steps: [String: Int] = [:]

    /// The time a request whose kind timed out `timedOut` times in a row
    /// gets: twice `base` each time, up to 5 minutes (a longer base keeps
    /// its own).
    nonisolated static func timeoutAfter(base: TimeInterval, timedOut: Int) -> TimeInterval {
        min(base * pow(2, Double(min(max(timedOut, 0), maxSteps))), max(base, maxTimeout))
    }

    /// The time a request of `key` gets now.
    func timeout(for key: String, base: TimeInterval) -> TimeInterval {
        Self.timeoutAfter(base: base, timedOut: lock.withLock { steps[key] ?? 0 })
    }

    /// A request of `key` got no answer in the time it had.
    func timedOut(_ key: String) {
        lock.withLock { steps[key] = min((steps[key] ?? 0) + 1, Self.maxSteps) }
    }

    /// A request of `key` was answered `took` seconds after it left the
    /// phone: back down to the shortest time that would have held it.
    func answered(_ key: String, base: TimeInterval, took: TimeInterval) {
        lock.withLock {
            guard let now = steps[key] else { return }
            var need = 0
            while need < now && Self.timeoutAfter(base: base, timedOut: need) < took { need += 1 }
            steps[key] = need == 0 ? nil : need
        }
    }
}
