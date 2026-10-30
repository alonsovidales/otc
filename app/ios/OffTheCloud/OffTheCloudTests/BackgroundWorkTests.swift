// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Settings › Background work (BackgroundWork.swift): which rows show and
/// what each says. Android's BackgroundWorkTest has the same rules.

private func task(_ kind: String, _ text: String, done: Int64, total: Int64) -> Msg_BackgroundTask {
    var t = Msg_BackgroundTask()
    t.kind = kind
    t.text = text
    t.done = done
    t.total = total
    return t
}

private func status(_ tasks: [Msg_BackgroundTask]) -> Msg_Status {
    var s = Msg_Status()
    s.backgroundTasks = tasks
    return s
}

@MainActor
struct BackgroundWorkTests {
    @Test func nothingFromAnOlderDeviceOrWhileNothingIsGoingOn() {
        #expect(BackgroundWork.items(nil).isEmpty)
        #expect(BackgroundWork.items(Msg_Status()).isEmpty)
        #expect(BackgroundWork.items(status([])).isEmpty)
    }

    @Test func aRowPerTaskInTheDevicesOrderAndWords() {
        let items = BackgroundWork.items(status([
            task("smart_search", "Getting to know your photos", done: 4210, total: 12800),
            task("thumbnails", " Making thumbnails ", done: 0, total: 0),
        ]))
        #expect(items.map(\.id) == ["smart_search", "thumbnails"])
        #expect(items[0].text == "Getting to know your photos")
        #expect(items[1].text == "Making thumbnails")
        #expect(items[0].progress == 4210.0 / 12800.0)
    }

    @Test func theCountInTheAppsNumbersAndASpinnerWhileTheTotalIsUnknown() {
        let was = L10n.shared.code
        defer { L10n.shared.set(was) }
        L10n.shared.set("en")
        let items = BackgroundWork.items(status([
            task("smart_search", "Getting to know your photos", done: 4210, total: 12800),
            task("dates", "Reading photo dates", done: 12, total: 0),
        ]))
        #expect(items[0].count == "4,210 of 12,800")
        // Not known yet: no count, no bar (a spinner).
        #expect(items[1].count == nil)
        #expect(items[1].progress == nil)
        L10n.shared.set("de")
        #expect(items[0].count?.contains("4.210") == true)
        #expect(items[0].count?.contains("12.800") == true)
    }

    @Test func doneIsKeptWithinTheTotal() {
        let items = BackgroundWork.items(status([
            task("analysis", "Analysing new uploads", done: 130, total: 120),
            task("places", "Finding places", done: -4, total: 50),
        ]))
        #expect(items[0].done == 120 && items[0].progress == 1)
        #expect(items[1].done == 0 && items[1].progress == 0)
    }

    @Test func aKindIsListedOnceAndOneWithoutAKindByItsPlace() {
        let items = BackgroundWork.items(status([
            task("analysis", "Analysing new uploads", done: 1, total: 2),
            task("", "Something new", done: 0, total: 0),
            task("analysis", "Analysing new uploads", done: 3, total: 4),
        ]))
        #expect(items.map(\.id) == ["analysis", "#1"])
        #expect(items[0].done == 1)
    }
}
