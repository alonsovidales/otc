// SPDX-License-Identifier: AGPL-3.0-or-later

import CoreGraphics
import Foundation
import Testing
@testable import OffTheCloud

/// Files' list columns on a wide window (FilesColumns.swift): the minimums,
/// the columns hidden as the list narrows, a resize, a reset and the widths
/// kept on the phone. The same cases and numbers as the web's
/// files-columns-test.mjs (its checkbox column, 26, is the lead here, and
/// its icons after the name, 75, the trail): change them together.
struct FilesColumnsTests {
    /// What a page in English measured there: the cells' padding,
    /// "0000000000…", "1023.0 MB" / "1.5 KB" and a date with and without
    /// its time.
    private let samples = FilesColumnSamples(
        lead: 26, trail: 75,
        pad: [.name: 24, .size: 24, .created: 24, .modified: 24],
        name: 100, size: 70, sizeShort: 40, date: 150, dateShort: 80
    )
    private var m: FilesColumnMetrics { FilesColumns.metrics(samples) }

    private func total(_ l: FilesColumnLayout, _ m: FilesColumnMetrics) -> CGFloat {
        m.lead + l.shown.reduce(0) { $0 + l[$1] }
    }

    // MARK: Minimums and defaults

    @Test func nameHasRoomForTenCharactersItsIconsAndItsPadding() {
        #expect(m.nameText == 100)
        #expect(m.nameMin == CGFloat(24 + 100 + 75 + 2))
        // The date columns show only while Name keeps more than its minimum.
        #expect(m.nameRoom > m.nameMin)
    }

    @Test func theOthersFitALongValueByDefaultAndAShortOneAtTheirMinimum() {
        #expect(m.def == [.size: 98, .created: 178, .modified: 178])
        #expect(m.min == [.size: 66, .created: 108, .modified: 108])
        for k in [FilesSortKey.size, .created, .modified] {
            #expect(m.min[k]! < m.def[k]!)
        }
    }

    @Test func measuredWidthsRoundUpNeverDown() {
        var s = samples
        s.name = 100.2
        s.date = 150.01
        let m = FilesColumns.metrics(s)
        #expect(m.nameText == 101)
        #expect(m.def[.created] == 179)
    }

    // MARK: The columns a width shows

    @Test func wideAllFourNameTakingWhatTheOthersLeave() {
        let l = FilesColumns.fit(room: 1000, given: [:], m)
        #expect(l.shown == [.name, .size, .created, .modified])
        #expect([l[.size], l[.created], l[.modified]] == [98, 178, 178])
        #expect(l[.name] == CGFloat(1000 - 26 - 98 - 178 - 178))
        #expect(total(l, m) == 1000)
        #expect(l.fixed == [.size, .created, .modified])
        #expect(l.last == .modified)
    }

    @Test func narrowerCreatedGoesFirstThenModifiedSizeAlwaysStays() {
        let all = m.lead + m.nameRoom + m.def[.size]! + m.def[.created]! + m.def[.modified]!
        #expect(FilesColumns.shown(room: all, m) == [.name, .size, .created, .modified])
        #expect(FilesColumns.shown(room: all - 1, m) == [.name, .size, .modified])
        let two = m.lead + m.nameRoom + m.def[.size]! + m.def[.modified]!
        #expect(FilesColumns.shown(room: two, m) == [.name, .size, .modified])
        #expect(FilesColumns.shown(room: two - 1, m) == [.name, .size])
        #expect(FilesColumns.shown(room: 100, m) == [.name, .size])
        let l = FilesColumns.fit(room: two - 1, given: [:], m)
        #expect(l.fixed == [.size])
        #expect(l[.size] == m.def[.size])
    }

    @Test func whichColumnsShowDoesntHangOnTheWidthsGiven() {
        let wide = FilesColumns.fit(room: 900, given: [.created: 600, .modified: 600], m)
        #expect(wide.shown == FilesColumns.fit(room: 900, given: [:], m).shown)
    }

    // MARK: Name's minimum is kept

    @Test func columnsGivenMoreThanFitsGiveWayTheLastFirstDownToTheirMinimums() {
        let room: CGFloat = 800
        let l = FilesColumns.fit(room: room, given: [.size: 150, .created: 400, .modified: 300], m)
        #expect(l.shown == [.name, .size, .created, .modified])
        #expect(l[.name] == m.nameMin)
        #expect(total(l, m) == room)
        // Modified gave first, then Created; Size kept what it was given.
        #expect(l[.modified] == m.min[.modified])
        #expect(l[.size] == 150)
        #expect(l[.created] < 400 && l[.created] > m.min[.created]!)
    }

    @Test func aListTooNarrowEvenForTheMinimumsNameGetsWhatIsLeftNeverLessThanNothing() {
        let l = FilesColumns.fit(room: 150, given: [:], m)
        #expect(l.shown == [.name, .size])
        #expect(l[.size] == m.min[.size])
        #expect(l[.name] == 150 - 26 - m.min[.size]!)
        #expect(FilesColumns.fit(room: 20, given: [:], m)[.name] == 0)
    }

    /// The phones only: too narrow for Name and Size at their minimums, the
    /// list keeps its narrow rows.
    @Test func theColumnsNeedRoomForNameAndSizeAtTheirMinimums() {
        let least = m.lead + m.nameMin + m.min[.size]!
        #expect(FilesColumns.fits(room: least, m))
        #expect(!FilesColumns.fits(room: least - 1, m))
    }

    @Test func aWidthGivenBelowAColumnsMinimumIsTakenAsTheMinimum() {
        #expect(FilesColumns.fit(room: 1000, given: [.size: 10], m)[.size] == m.min[.size])
    }

    // MARK: A resize

    @Test func nameWiderTakesFromSizeThenCreatedThenModifiedDownToTheirMinimums() {
        let l = FilesColumns.fit(room: 1000, given: [:], m)
        let a = FilesColumns.resize(l, m, column: .name, by: 20)
        #expect(a[.name] == l[.name] + 20)
        #expect(a[.size] == l[.size] - 20)
        #expect(a[.created] == l[.created])
        let far = FilesColumns.resize(l, m, column: .name, by: 5000)
        #expect([far[.size], far[.created], far[.modified]] == [m.min[.size]!, m.min[.created]!, m.min[.modified]!])
        // The list stays as wide as it was: nothing scrolls sideways.
        #expect(total(far, m) == 1000)
    }

    @Test func narrowerGivesTheRoomToTheNextColumnAndStopsAtItsOwnMinimum() {
        let l = FilesColumns.fit(room: 1000, given: [:], m)
        let a = FilesColumns.resize(l, m, column: .name, by: -30)
        #expect(a[.name] == l[.name] - 30)
        #expect(a[.size] == l[.size] + 30)
        let far = FilesColumns.resize(l, m, column: .name, by: -5000)
        #expect(far[.name] == m.nameMin)
        #expect(total(far, m) == 1000)
        let created = FilesColumns.resize(l, m, column: .created, by: -5000)
        #expect(created[.created] == m.min[.created])
        #expect(created[.modified] == l[.modified] + (l[.created] - m.min[.created]!))
        // A column before it doesn't move.
        #expect(created[.size] == l[.size])
    }

    @Test func aResizeGoesFromWhereTheDragBeganSoGoingBackUndoesIt() {
        let l = FilesColumns.fit(room: 1000, given: [:], m)
        #expect(FilesColumns.resize(l, m, column: .size, by: 0) == l)
        let there = FilesColumns.resize(l, m, column: .size, by: 5000)
        // Under half a point is nothing.
        #expect(FilesColumns.resize(l, m, column: .size, by: 0.4).width == l.width)
        #expect(there[.size] > l[.size])
    }

    @Test func theLastColumnShownHasNoEdgeNorDoesAHiddenOne() {
        let l = FilesColumns.fit(room: 1000, given: [:], m)
        #expect(FilesColumns.resize(l, m, column: .modified, by: 40) == l)
        let narrow = FilesColumns.fit(room: 500, given: [:], m)
        #expect(narrow.shown == [.name, .size])
        #expect(FilesColumns.resize(narrow, m, column: .created, by: 40) == narrow)
        #expect(FilesColumns.resize(narrow, m, column: .size, by: 40) == narrow)
        // With Created hidden, Size's edge gives to and takes from Modified.
        let mid = FilesColumns.fit(room: m.lead + m.nameRoom + m.def[.size]! + m.def[.modified]!, given: [:], m)
        let s = FilesColumns.resize(mid, m, column: .size, by: 10)
        #expect(s[.modified] == mid[.modified] - 10)
    }

    @Test func theStep() {
        #expect(FilesColumns.step == 16)
    }

    @Test func whatAHandleSaysTheColumnsWidthItsMinimumAndItsMost() {
        let l = FilesColumns.fit(room: 1000, given: [:], m)
        let v = FilesColumns.edgeValues(l, m, column: .size)
        #expect(v.now == l[.size])
        #expect(v.min == m.min[.size])
        #expect(v.max == l[.size] + (l[.created] - m.min[.created]!) + (l[.modified] - m.min[.modified]!))
        #expect(FilesColumns.edgeValues(l, m, column: .name).min == m.nameMin)
        #expect(FilesColumns.resize(l, m, column: .size, by: 10_000)[.size] == v.max)
    }

    @Test func halvesRoundAsTheWebDoes() {
        #expect(FilesColumns.round(0.4) == 0)
        #expect(FilesColumns.round(0.5) == 1)
        #expect(FilesColumns.round(-0.5) == 0)
        #expect(FilesColumns.round(-0.6) == -1)
        #expect(FilesColumns.round(120.6) == 121)
    }

    // MARK: The widths kept

    @Test func onlyTheColumnsAResizeChangedAreKept() {
        let l = FilesColumns.fit(room: 1000, given: [.modified: 200], m)
        let a = FilesColumns.resize(l, m, column: .name, by: 20)
        #expect(FilesColumns.widthsAfter([.modified: 200], before: l, after: a) == [.modified: 200, .size: l[.size] - 20])
    }

    @Test func aDoubleTapPutsThatColumnBackNamesPutsEveryColumnBack() {
        #expect(FilesColumns.reset([.size: 150, .created: 120], column: .size) == [.created: 120])
        #expect(FilesColumns.reset([.size: 150, .created: 120], column: .name) == [:])
        #expect(FilesColumns.reset([:], column: .modified) == [:])
        let back = FilesColumns.fit(room: 1000, given: FilesColumns.reset([.size: 150], column: .size), m)
        #expect(back == FilesColumns.fit(room: 1000, given: [:], m))
    }

    @Test func storedAsJSONUnderFilesColumnsAndReadBack() throws {
        let suite = "files-columns-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        #expect(FilesColumns.load(defaults) == [:])
        FilesColumns.save([.size: 150, .modified: 210], defaults)
        #expect(defaults.string(forKey: "files.columns") == #"{"modified":210,"size":150}"#)
        #expect(FilesColumns.load(defaults) == [.size: 150, .modified: 210])
        // Nothing given: nothing kept.
        FilesColumns.save([:], defaults)
        #expect(defaults.object(forKey: "files.columns") == nil)
    }

    @Test func aStoredValueThatIsntASaneWidthIsLeftOut() {
        #expect(FilesColumns.parse(nil) == [:])
        #expect(FilesColumns.parse("not json") == [:])
        #expect(FilesColumns.parse("[120, 130]") == [:])
        #expect(FilesColumns.parse("null") == [:])
        #expect(FilesColumns.parse(#"{"size": -5, "created": "120", "modified": 1e9, "name": 300}"#) == [:])
        #expect(FilesColumns.parse(#"{"size": true}"#) == [:])
        #expect(FilesColumns.parse(#"{"size": 120.6, "created": 0, "modified": 180}"#) == [.size: 121, .modified: 180])
    }
}

/// The widths given on the phone as a drag, VoiceOver's actions and a
/// double tap change them (FilesColumnsModel).
@MainActor
struct FilesColumnsModelTests {
    private let samples = FilesColumnSamples(
        lead: 26, trail: 75,
        pad: [.name: 24, .size: 24, .created: 24, .modified: 24],
        name: 100, size: 70, sizeShort: 40, date: 150, dateShort: 80
    )

    private func store() -> (UserDefaults, String) {
        let suite = "files-columns-model-\(UUID().uuidString)"
        return (UserDefaults(suiteName: suite)!, suite)
    }

    @Test func aDragGoesFromWhereItBeganAndIsKeptWhenTheFingerLifts() {
        let (defaults, suite) = store()
        defer { defaults.removePersistentDomain(forName: suite) }
        let model = FilesColumnsModel(defaults: defaults)
        let m = FilesColumns.metrics(samples)
        let l = model.layout(room: 1000, m)
        model.drag(.size, by: 10, from: l, m)
        #expect(model.dragging == .size)
        // Later moves go from where the drag began, not from the last one.
        model.drag(.size, by: 30, from: model.layout(room: 1000, m), m)
        #expect(model.layout(room: 1000, m)[.size] == l[.size] + 30)
        #expect(model.layout(room: 1000, m)[.created] == l[.created] - 30)
        // Nothing kept until the finger lifts.
        #expect(FilesColumns.load(defaults) == [:])
        model.endDrag()
        #expect(model.dragging == nil)
        #expect(model.live == nil)
        #expect(model.given == [.size: l[.size] + 30, .created: l[.created] - 30])
        #expect(FilesColumns.load(defaults) == model.given)
        // The next Files screen finds them.
        #expect(FilesColumnsModel(defaults: defaults).given == model.given)
        #expect(FilesColumnsModel(defaults: defaults).layout(room: 1000, m)[.size] == l[.size] + 30)
    }

    @Test func voiceOversStepsAndADoubleTap() {
        let (defaults, suite) = store()
        defer { defaults.removePersistentDomain(forName: suite) }
        let model = FilesColumnsModel(defaults: defaults)
        let m = FilesColumns.metrics(samples)
        let l = model.layout(room: 1000, m)
        model.nudge(.name, by: FilesColumns.step, in: l, m)
        #expect(model.layout(room: 1000, m)[.size] == l[.size] - 16)
        model.nudge(.size, by: -FilesColumns.step, in: model.layout(room: 1000, m), m)
        #expect(model.layout(room: 1000, m)[.size] == l[.size] - 32)
        // The last column has no edge: nothing changes, nothing is kept.
        let before = model.given
        model.nudge(.modified, by: FilesColumns.step, in: model.layout(room: 1000, m), m)
        #expect(model.given == before)
        // A double tap on Name's puts every column back.
        model.reset(.name)
        #expect(model.given == [:])
        #expect(defaults.object(forKey: FilesColumns.storageKey) == nil)
        #expect(model.layout(room: 1000, m) == l)
    }
}

/// What a wide row's cells say (FilesColumnsLook.cell).
struct FilesColumnCellTests {
    private let en = Locale(identifier: "en_US")

    private func row(_ path: String, dir: Bool = false, size: Int64 = 0, created: Date? = nil, modified: Date? = nil) -> FileRow {
        var f = Msg_File()
        f.path = path
        f.mime = dir ? "inode/directory" : "text/plain"
        f.size64 = size
        if let created { f.created = .init(date: created) }
        if let modified { f.modified = .init(date: modified) }
        return FileRow(file: f)
    }

    @Test func aSizeADateOrADashWhereThereIsNone() {
        let d = Calendar.current.date(from: DateComponents(year: 2026, month: 12, day: 28, hour: 22, minute: 58, second: 58))!
        let file = row("/d/a.txt", size: 1536, created: d)
        #expect(FilesColumnsLook.cell(file, .size, locale: en) == formatByteCount(1536))
        let created = FilesColumnsLook.cell(file, .created, locale: en)
        // The date and the time to the second, in numbers, as the web's.
        #expect(created.contains("12/28/2026") && created.contains("10:58:58"))
        #expect(FilesColumnsLook.cell(file, .modified, locale: en) == "—")
        // A folder has no size; ".." nothing at all.
        #expect(FilesColumnsLook.cell(row("/d/sub", dir: true), .size, locale: en) == "—")
        for k in [FilesSortKey.size, .created, .modified] {
            #expect(FilesColumnsLook.cell(row("..", dir: true), k, locale: en) == "—")
        }
    }

    @Test func theDefaultsFollowTheTextSize() {
        let small = FilesColumns.metrics(FilesColumnsLook.samples(.medium, locale: en))
        let large = FilesColumns.metrics(FilesColumnsLook.samples(.accessibility2, locale: en))
        #expect(large.nameMin > small.nameMin)
        #expect(large.def[.created]! > small.def[.created]!)
        // Before Name: the circle and the icon with their gaps.
        #expect(small.lead == CGFloat(28 + 8 + 20 + 8))
    }
}
