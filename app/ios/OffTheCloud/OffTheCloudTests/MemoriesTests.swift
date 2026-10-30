// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// On this day (Memories.swift): the period shown, the words, the dates
/// asked and the model's answers, hiding and undo. Android's MemoriesTest
/// has the same cases.

private func memory(_ key: String, years: Int32, start: String, end: String? = nil, place: String = "",
                    people: [String] = [], hashes: [String] = ["h1"]) -> Msg_Memory {
    var m = Msg_Memory()
    m.key = key
    m.yearsAgo = years
    m.startDate = start
    m.endDate = end ?? start
    m.place = place
    m.people = people
    m.files = hashes.map { h in
        var f = Msg_File()
        f.hash = h
        f.path = "/Photos/\(h).jpg"
        f.mime = "image/jpeg"
        return f
    }
    m.cover = m.files[0]
    return m
}

private func answer(_ ms: [Msg_Memory], measuring: Bool = false, hidden: Int32 = 0) -> Msg_Memories {
    var a = Msg_Memories()
    a.memories = ms
    a.measuring = measuring
    a.hiddenCount = hidden
    return a
}

@MainActor
private func english<T>(_ body: () -> T) -> T {
    let was = L10n.shared.code
    L10n.shared.set("en")
    defer { L10n.shared.set(was) }
    return body()
}

struct MemoryChoiceTests {
    @Test func theFirstPeriodWithMemoriesShows() {
        let some = answer([memory("week:2023-10-27", years: 3, start: "2023-10-27")])
        #expect(MemoryChoice.shown(picked: nil, answers: [.memoryDay: answer([]), .memoryWeek: some, .memoryMonth: some]) == .memoryWeek)
        #expect(MemoryChoice.shown(picked: nil, answers: [.memoryDay: some, .memoryWeek: some]) == .memoryDay)
        #expect(MemoryChoice.shown(picked: nil, answers: [.memoryMonth: some]) == .memoryMonth)
    }

    @Test func aPickedPeriodStaysWhileItHasMemories() {
        let some = answer([memory("day:2023-10-30", years: 3, start: "2023-10-30")])
        #expect(MemoryChoice.shown(picked: .memoryMonth, answers: [.memoryDay: some, .memoryMonth: some]) == .memoryMonth)
        // Emptied (everything hidden): the first that has some.
        #expect(MemoryChoice.shown(picked: .memoryMonth, answers: [.memoryDay: some, .memoryMonth: answer([])]) == .memoryDay)
    }

    @Test func noneAnywhereShowsNothing() {
        #expect(MemoryChoice.shown(picked: nil, answers: [:]) == nil)
        #expect(MemoryChoice.shown(picked: .memoryDay, answers: [.memoryDay: answer([]), .memoryWeek: answer([])]) == nil)
    }
}

@MainActor
struct MemoryTextTests {
    @Test func yearsAgoCountsInTheLanguage() {
        let words = english { [MemoryText.yearsAgo(1), MemoryText.yearsAgo(3), MemoryText.yearsAgo(0)] }
        #expect(words == ["1 year ago", "3 years ago", "1 year ago"])
    }

    @Test func theDaysAMemoryCovers() {
        // As Android's memoryDays: by the key's period, the end's year.
        let words = english {
            [MemoryText.days(memory("day:2023-10-30", years: 3, start: "2023-10-30")),
             MemoryText.days(memory("week:2023-10-27", years: 3, start: "2023-10-27", end: "2023-11-02")),
             MemoryText.days(memory("week:2023-12-29", years: 3, start: "2023-12-29", end: "2024-01-04")),
             MemoryText.days(memory("month:2023-10", years: 3, start: "2023-10-01", end: "2023-10-31")),
             MemoryText.days(memory("day:x", years: 3, start: "soon"))]
        }
        #expect(words == ["30 Oct 2023", "27 Oct – 2 Nov 2023", "29 Dec – 4 Jan 2024", "October 2023", ""])
    }

    @Test func theHeaderLeavesOutAMissingPlace() {
        let words = english {
            [MemoryText.header(memory("day:2023-10-30", years: 3, start: "2023-10-30", place: "Asturias")),
             MemoryText.header(memory("day:2025-10-30", years: 1, start: "2025-10-30")),
             MemoryText.cardLabel(memory("day:2023-10-30", years: 3, start: "2023-10-30", place: "Asturias"))]
        }
        #expect(words == ["3 years ago · 30 Oct 2023 · Asturias", "1 year ago · 30 Oct 2025", "3 years ago, Asturias"])
    }

    @Test func spanishSaysItsOwnWay() {
        let was = L10n.shared.code
        L10n.shared.set("es")
        let words = [MemoryText.yearsAgo(1), MemoryText.yearsAgo(2), MemoryText.title(.memoryDay)]
        L10n.shared.set(was)
        #expect(words == ["Hace 1 año", "Hace 2 años", "Un día como hoy"])
    }
}

struct MemoryDatesTests {
    @Test func todayInThePhonesTimeZone() throws {
        // 2026-10-30 23:30 UTC is already the 31st in Madrid (UTC+1).
        let instant = Date(timeIntervalSince1970: 1_793_403_000)
        let madrid = try #require(TimeZone(identifier: "Europe/Madrid"))
        let utc = try #require(TimeZone(identifier: "UTC"))
        #expect(MemoryDates.day(of: instant, zone: utc) == "2026-10-30")
        #expect(MemoryDates.day(of: instant, zone: madrid) == "2026-10-31")
        let r = MemoryDates.request(.memoryWeek, now: instant, zone: madrid)
        #expect(r.date == "2026-10-31")
        #expect(r.period == .memoryWeek)
        #expect(r.timeZone == "Europe/Madrid")
        #expect(r.utcOffsetMinutes == 60)
    }

    @Test func onlyAWholeDayParses() {
        #expect(MemoryDates.parse("2023-10-30")! == (2023, 10, 30))
        #expect(MemoryDates.parse("2023-13-01") == nil)
        #expect(MemoryDates.parse("2023-10") == nil)
        #expect(MemoryDates.parse("") == nil)
    }
}

struct MemoryHidingTests {
    @Test func aHiddenPhotoOrMemoryLeavesTheAnswersAtOnce() {
        let two = memory("day:2023-10-30", years: 3, start: "2023-10-30", hashes: ["a", "b"])
        let one = memory("day:2021-10-30", years: 5, start: "2021-10-30", hashes: ["a"])
        let answers: [Msg_MemoryPeriod: Msg_Memories] = [.memoryDay: answer([two, one])]
        // A photo: out of every memory, the cover moving on; a memory left
        // with none goes.
        let noA = MemoryHiding.without(.memoryHidePhoto, "a", in: answers)[.memoryDay]!.memories
        #expect(noA.map(\.key) == ["day:2023-10-30"])
        #expect(noA[0].files.map(\.hash) == ["b"])
        #expect(noA[0].cover.hash == "b")
        // A memory, by its key.
        let noTwo = MemoryHiding.without(.memoryHideMemory, "day:2023-10-30", in: answers)[.memoryDay]!.memories
        #expect(noTwo.map(\.key) == ["day:2021-10-30"])
        // A day or a person: the device knows which photos; asked again.
        #expect(MemoryHiding.without(.memoryHideDay, "2023-10-30", in: answers) == answers)
    }
}

struct MemoryPeopleMatchTests {
    private func person(_ id: String, _ name: String) -> Msg_Person {
        var p = Msg_Person()
        p.id = id
        p.name = name
        return p
    }

    @Test func namesFindTheirPeopleOnce() {
        let people = [person("1", "Ana"), person("2", "Luis"), person("3", "Leo"), person("4", "Leo"), person("5", "")]
        let found = MemoryPeopleMatch.of(names: ["Ana", "Leo", "Ana", "Marta", ""], people: people)
        // Two people called Leo: which one is meant can't be told.
        #expect(found.map(\.id) == ["1"])
    }
}

// MARK: - The model

/// The device, for the memories: answers each period from `byPeriod`, and
/// records what it is asked.
@MainActor
final class MemoriesDevice {
    var asked: [Msg_GetMemories] = []
    var hides: [Msg_HideMemory] = []
    var byPeriod: [Msg_MemoryPeriod: Msg_Memories] = [:]
    /// An error every GetMemories gets: (code, message).
    var error: (String, String)?
    /// HideMemory refused in these words (a demo device).
    var refuseHide: String?
    /// GetMemories answers nothing this many times first.
    var silentFor = 0

    func attach(_ m: MemoriesModel) {
        m.thumbs.request = { _ in throw CancellationError() }
        m.toastFor = 30
        m.retryDelay = { _ in 0.01 }
        m.now = { Date(timeIntervalSince1970: 1_793_361_600) } // 2026-10-30 12:00 UTC
        m.zone = { TimeZone(identifier: "UTC")! }
        m.request = { [weak self] payload in
            guard let self else { throw CancellationError() }
            var resp = Msg_RespEnvelope()
            switch payload {
            case .reqGetMemories(let g):
                self.asked.append(g)
                if self.silentFor > 0 {
                    self.silentFor -= 1
                    throw CancellationError()
                }
                if let (code, message) = self.error {
                    resp.error = true
                    resp.errorCode = code
                    resp.errorMessage = message
                } else {
                    resp.payload = .respMemories(self.byPeriod[g.period] ?? Msg_Memories())
                }
            case .reqHideMemory(let h):
                self.hides.append(h)
                if let words = self.refuseHide {
                    resp.error = true
                    resp.errorCode = "demo"
                    resp.errorMessage = words
                } else {
                    var ack = Msg_Ack()
                    ack.ok = true
                    resp.payload = .respAck(ack)
                }
            default:
                throw CancellationError()
            }
            return resp
        }
    }
}

@MainActor
private func settings(memories: Bool?) -> Msg_Settings {
    var s = Msg_Settings()
    if let memories { s.memoriesEnabled = memories }
    return s
}

@MainActor
@Suite(.serialized)
struct MemoriesModelTests {
    @Test func theThreePeriodsAreAskedForTodayAndTheFirstWithMemoriesShows() async {
        let m = MemoriesModel(thumbCache: testThumbCache())
        let device = MemoriesDevice()
        device.attach(m)
        device.byPeriod[.memoryWeek] = answer([memory("week:2023-10-27", years: 3, start: "2023-10-27", end: "2023-11-02")])
        m.deviceSaid(settings: settings(memories: true))
        await m.loaded()
        #expect(device.asked.map(\.period) == [.memoryDay, .memoryWeek, .memoryMonth])
        // Foundation names UTC "GMT".
        let utc = TimeZone(identifier: "UTC")!.identifier
        #expect(device.asked.allSatisfy { $0.date == "2026-10-30" && $0.timeZone == utc && $0.utcOffsetMinutes == 0 })
        #expect(m.isShown)
        #expect(m.shownPeriod == .memoryWeek)
        #expect(m.has(.memoryWeek) && !m.has(.memoryDay) && !m.has(.memoryMonth))
        // Picked: it stays.
        device.byPeriod[.memoryDay] = answer([memory("day:2023-10-30", years: 3, start: "2023-10-30")])
        await m.ask()
        #expect(m.shownPeriod == .memoryDay)
        m.period = .memoryWeek
        #expect(m.shownPeriod == .memoryWeek)
    }

    @Test func nothingIsAskedWhileOffOrOnADeviceBeforeIt() async {
        let m = MemoriesModel(thumbCache: testThumbCache())
        let device = MemoriesDevice()
        device.attach(m)
        m.deviceSaid(settings: settings(memories: nil))
        m.appeared()
        m.deviceSaid(settings: settings(memories: false))
        m.appeared()
        try? await Task.sleep(nanoseconds: 50_000_000)
        #expect(device.asked.isEmpty)
        #expect(!m.isShown)
    }

    @Test func anOlderDeviceOrTheSwitchTurnedOffElsewhereHidesTheStrip() async {
        let m = MemoriesModel(thumbCache: testThumbCache())
        let device = MemoriesDevice()
        device.attach(m)
        device.byPeriod[.memoryDay] = answer([memory("day:2023-10-30", years: 3, start: "2023-10-30")])
        m.deviceSaid(settings: settings(memories: true))
        await m.loaded()
        #expect(m.isShown)

        device.error = ("memories_off", "On this day is off")
        await m.ask()
        #expect(m.enabled == false)
        #expect(!m.isShown)

        m.set(enabled: true)
        device.error = ("unknown_payload", "unknown payload")
        await m.ask()
        #expect(m.enabled == nil)
        #expect(!m.isShown)
    }

    @Test func aFailureIsAskedAgain() async {
        let m = MemoriesModel(thumbCache: testThumbCache())
        let device = MemoriesDevice()
        device.attach(m)
        device.silentFor = 2
        device.byPeriod[.memoryDay] = answer([memory("day:2023-10-30", years: 3, start: "2023-10-30")])
        m.deviceSaid(settings: settings(memories: true))
        await m.loaded()
        #expect(m.isShown)
        #expect(device.asked.count == 5) // two days unanswered, then the three
    }

    @Test func whileMeasuringItIsAskedAgain() async {
        let m = MemoriesModel(thumbCache: testThumbCache())
        let device = MemoriesDevice()
        device.attach(m)
        m.measuringEvery = 0.05
        device.byPeriod[.memoryDay] = answer([], measuring: true)
        m.deviceSaid(settings: settings(memories: true))
        await m.loaded()
        #expect(!m.isShown)
        device.byPeriod[.memoryDay] = answer([memory("day:2023-10-30", years: 3, start: "2023-10-30")])
        let end = Date() + 3
        while !m.isShown && Date() < end { try? await Task.sleep(nanoseconds: 10_000_000) }
        #expect(m.isShown)
        #expect(device.asked.count >= 6)
    }

    @Test func measuringWithNothingToShowShowsNothing() async {
        let m = MemoriesModel(thumbCache: testThumbCache())
        let device = MemoriesDevice()
        device.attach(m)
        for p in MemoryChoice.periods { device.byPeriod[p] = answer([], measuring: true) }
        m.deviceSaid(settings: settings(memories: true))
        await m.loaded()
        #expect(!m.isShown)
    }

    @Test func hidingSaysSoAndUndoShowsItAgain() async {
        let m = MemoriesModel(thumbCache: testThumbCache())
        let device = MemoriesDevice()
        device.attach(m)
        device.byPeriod[.memoryDay] = answer([memory("day:2023-10-30", years: 3, start: "2023-10-30")], hidden: 0)
        m.deviceSaid(settings: settings(memories: true))
        await m.loaded()

        device.byPeriod[.memoryDay] = answer([], hidden: 1)
        let took = await m.hide(.memoryHideMemory, "day:2023-10-30")
        #expect(took)
        #expect(device.hides.last?.kind == .memoryHideMemory)
        #expect(device.hides.last?.value == "day:2023-10-30")
        #expect(device.hides.last?.show == false)
        #expect(m.toast == .hidden)
        #expect(!m.isShown)
        #expect(m.hiddenCount == 1)

        device.byPeriod[.memoryDay] = answer([memory("day:2023-10-30", years: 3, start: "2023-10-30")], hidden: 0)
        await m.undo()
        #expect(device.hides.last?.show == true)
        #expect(device.hides.last?.value == "day:2023-10-30")
        #expect(m.toast == nil)
        #expect(m.isShown)
        #expect(m.hiddenCount == 0)
    }

    @Test func aRefusalGivesTheDevicesWords() async {
        let m = MemoriesModel(thumbCache: testThumbCache())
        let device = MemoriesDevice()
        device.attach(m)
        m.deviceSaid(settings: settings(memories: true))
        await m.loaded()
        device.refuseHide = "This is a demo"
        let took = await m.hide(.memoryHidePhoto, "h1")
        #expect(!took)
        #expect(m.toast == .failed("This is a demo"))
        // Nothing to undo.
        let before = device.hides.count
        await m.undo()
        #expect(device.hides.count == before)
        // Settings' Show hidden memories again, refused the same way.
        let refused = await m.showAllHidden()
        #expect(refused == .failed("This is a demo"))
        #expect(device.hides.last?.showAll == true)
    }

    @Test func logOutForgetsEverything() async {
        let m = MemoriesModel(thumbCache: testThumbCache())
        let device = MemoriesDevice()
        device.attach(m)
        device.byPeriod[.memoryDay] = answer([memory("day:2023-10-30", years: 3, start: "2023-10-30")])
        m.deviceSaid(settings: settings(memories: true))
        await m.loaded()
        m.period = .memoryDay
        m.reset()
        #expect(m.enabled == nil)
        #expect(m.period == nil)
        #expect(m.answers.isEmpty)
        #expect(!m.isShown)
    }
}
