// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// The change history's fix for photos added to the library with a date
/// the watermark is already past (PhotoHistory.swift), against a fake
/// library: no photo access is needed or asked for.
struct PhotoSyncHistoryTests {
    private let watermark = Date(timeIntervalSince1970: 1_800_000_000)
    private var before: Date { watermark.addingTimeInterval(-3600) }
    private var after: Date { watermark.addingTimeInterval(3600) }

    /// A library whose history and dates the test sets.
    final class FakeLibrary: PhotoLibraryHistory {
        var token: Data? = Data("now".utf8)
        var answer: LibraryHistory = .inserted([])
        var dates: [String: Date?] = [:]
        private(set) var datesAsked = 0

        var currentToken: Data? { token }

        func history(since token: Data?) -> LibraryHistory {
            token == nil ? .noToken : answer
        }

        func creationDates(of ids: Set<String>) -> [String: Date?] {
            datesAsked += 1
            return dates.filter { ids.contains($0.key) }
        }
    }

    @Test func noTokenChangesNothing() {
        #expect(PhotoSync.plan(.noToken, dates: [:], watermark: watermark, skipped: [], floorKnown: true) == HistoryPlan())
    }

    @Test func onlyWhatTheWatermarkIsPastGoesOnTheRetryList() {
        let dates: [String: Date?] = ["airdrop": before, "camera": after, "nodate": nil, "same": watermark, "screenshot": before]
        let plan = PhotoSync.plan(.inserted(["airdrop", "camera", "nodate", "same", "screenshot", "deleted"]), dates: dates,
                                  watermark: watermark, skipped: ["screenshot"], floorKnown: true)
        // Newer: the date fetch has it. Deleted since: absent. Skipped: never sent.
        #expect(plan == HistoryPlan(markPending: ["airdrop", "nodate", "same"]))
    }

    /// No watermark: the date fetch is the whole library already.
    @Test func noWatermarkNoRetries() {
        let plan = PhotoSync.plan(.inserted(["a"]), dates: ["a": before], watermark: nil, skipped: [], floorKnown: true)
        #expect(plan == HistoryPlan())
    }

    @Test func unavailableHistoryCatchesUpOnlyFromAKnownFloor() {
        #expect(PhotoSync.plan(.unavailable, dates: [:], watermark: watermark, skipped: [], floorKnown: true) == HistoryPlan(catchUp: true))
        #expect(PhotoSync.plan(.unavailable, dates: [:], watermark: watermark, skipped: [], floorKnown: false) == HistoryPlan())
        // No watermark: the date fetch is the whole library already.
        #expect(PhotoSync.plan(.unavailable, dates: [:], watermark: nil, skipped: [], floorKnown: true) == HistoryPlan())
    }

    // MARK: A run's history step, with the fake library

    @Test func missingTokenReadsNothingMore() {
        let library = FakeLibrary()
        var skippedRead = false
        let plan = PhotoSync.readHistory(library, token: nil, watermark: watermark, skipped: { skippedRead = true; return [] }, floorKnown: true)
        #expect(plan == HistoryPlan())
        #expect(library.datesAsked == 0)
        #expect(!skippedRead)
    }

    @Test func expiredTokenQueuesTheCatchUp() {
        let library = FakeLibrary()
        library.answer = .unavailable
        #expect(PhotoSync.readHistory(library, token: Data("old".utf8), watermark: watermark, skipped: { [] }, floorKnown: true) == HistoryPlan(catchUp: true))
        // The floor unknown (chosen before it was kept): as before.
        #expect(PhotoSync.readHistory(library, token: Data("old".utf8), watermark: watermark, skipped: { [] }, floorKnown: false) == HistoryPlan())
        #expect(library.datesAsked == 0)
    }

    @Test func lateInsertsAreFoundAndSkippedOnesLeftOut() {
        let library = FakeLibrary()
        library.answer = .inserted(["old", "new", "inSkippedAlbum"])
        library.dates = ["old": before, "new": after, "inSkippedAlbum": before]
        var skippedReads = 0
        let plan = PhotoSync.readHistory(library, token: Data("t".utf8), watermark: watermark,
                                         skipped: { skippedReads += 1; return ["inSkippedAlbum"] }, floorKnown: true)
        #expect(plan == HistoryPlan(markPending: ["old"]))
        #expect(library.datesAsked == 1)
        #expect(skippedReads == 1)
    }

    /// The albums turned off are read only when something old came in.
    @Test func skippedAreReadOnlyWhenNeeded() {
        let library = FakeLibrary()
        library.answer = .inserted(["new"])
        library.dates = ["new": after]
        var skippedRead = false
        let plan = PhotoSync.readHistory(library, token: Data("t".utf8), watermark: watermark,
                                         skipped: { skippedRead = true; return [] }, floorKnown: true)
        #expect(plan == HistoryPlan())
        #expect(!skippedRead)
        // Nothing inserted: not even the dates.
        library.answer = .inserted([])
        _ = PhotoSync.readHistory(library, token: Data("t".utf8), watermark: watermark, skipped: { [] }, floorKnown: true)
        #expect(library.datesAsked == 1)
    }

    /// Run after run, as syncOnce goes: the first run of this version has no
    /// token and only stores one; the next reads from it.
    @Test func theTokenCarriesFromRunToRun() throws {
        let suite = "history-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let state = PhotoSync.State(defaults: defaults, directory: FileManager.default.temporaryDirectory)
        let library = FakeLibrary()
        library.answer = .inserted(["late"])
        library.dates = ["late": before]

        library.token = Data("first".utf8)
        let first = PhotoSync.readHistory(library, token: state.changeToken, watermark: watermark, skipped: { [] }, floorKnown: true)
        state.changeToken = library.currentToken
        #expect(first == HistoryPlan())

        library.token = Data("second".utf8)
        let second = PhotoSync.readHistory(library, token: state.changeToken, watermark: watermark, skipped: { [] }, floorKnown: true)
        state.changeToken = library.currentToken
        #expect(second == HistoryPlan(markPending: ["late"]))
        #expect(state.changeToken == Data("second".utf8))
    }

    // MARK: PhotoKit's side, without photo access

    @Test func photoKitHistoryWithoutAToken() {
        #expect(PhotoKitHistory().history(since: nil) == .noToken)
    }

    /// A token that can't be read (cut short, another format) is an
    /// unavailable history, never a crash or an empty one.
    @Test func junkTokenIsUnavailable() {
        #expect(PhotoKitHistory.unarchive(Data("junk".utf8)) == nil)
        #expect(PhotoKitHistory().history(since: Data("junk".utf8)) == .unavailable)
        #expect(PhotoKitHistory().history(since: Data()) == .unavailable)
    }
}
