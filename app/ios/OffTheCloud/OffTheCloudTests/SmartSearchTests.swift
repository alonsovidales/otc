// SPDX-License-Identifier: AGPL-3.0-or-later

import Combine
import Foundation
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Smart search (SmartSearch.swift): when it is offered, the search's row,
/// the request it sends, the reading note, the device's refusals and its
/// model's download (release 152). Android's SmartSearchTest has the same
/// rules.

@MainActor
private func settings(smart: Bool?) -> Msg_Settings {
    var s = Msg_Settings()
    if let smart { s.smartSearchEnabled = smart }
    return s
}

/// Settings with the switch and the model (nil: a device before 152).
@MainActor
private func settings(smart: Bool, ready: Bool? = nil, downloading: Bool = false, percent: Int32 = 0,
                      problem: String = "") -> Msg_Settings {
    var s = Msg_Settings()
    s.smartSearchEnabled = smart
    if let ready {
        var m = Msg_SmartSearchModel()
        m.ready = ready
        m.downloading = downloading
        m.percent = percent
        m.problem = problem
        s.smartSearchModel = m
    }
    return s
}

/// English, for the words a test reads, put back afterwards.
@MainActor
private func inEnglish<T>(_ body: () -> T) -> T {
    let was = L10n.shared.code
    L10n.shared.set("en")
    defer { L10n.shared.set(was) }
    return body()
}

@MainActor
struct SmartSearchSupportTests {
    @Test func onlyADeviceThatSaysItIsOnOffersIt() {
        let s = SmartSearchSupport()
        #expect(!s.isOn)
        s.deviceSaid(settings: settings(smart: nil))
        #expect(s.enabled == nil && !s.isOn) // a device before it
        s.deviceSaid(settings: settings(smart: false))
        #expect(s.enabled == false && !s.isOn)
        s.deviceSaid(settings: settings(smart: true))
        #expect(s.isOn)
        s.reset()
        #expect(s.enabled == nil)
    }

    @Test func theDevicesRefusals() {
        func resp(_ code: String, _ message: String) -> Msg_RespEnvelope {
            var r = Msg_RespEnvelope()
            r.error = true
            r.errorCode = code
            r.errorMessage = message
            return r
        }
        #expect(SmartSearchSupport.refusal(resp("smart_search_off", "Smart search is off"))! == ("Smart search is off", .off))
        #expect(SmartSearchSupport.refusal(resp("smart_search_unavailable", ""))! == (nil, .unavailable))
        #expect(SmartSearchSupport.refusal(resp("smart_search_downloading", "Still downloading"))! == ("Still downloading", .downloading))
        #expect(SmartSearchSupport.refusal(resp("", "internal error")) == nil)
        #expect(SmartSearchSupport.refusal(nil) == nil)
        #expect(SmartSearchSupport.text("  kids in the snow \n") == "kids in the snow")
        #expect(SmartSearchSupport.text("   ") == nil)
    }
}

/// The model on demand (release 152): where it is, what Settings' section
/// says, when Settings asks again and when the search offers its row.
@MainActor
struct SmartSearchModelTests {
    @Test func whereTheModelIsFromTheDevicesSettings() {
        // A device before 152 says nothing: it had the model.
        #expect(SmartSearchModelState(settings(smart: true)) == .ready)
        #expect(SmartSearchModelState(settings(smart: true, ready: true)) == .ready)
        // Ready wins over a stale downloading flag.
        #expect(SmartSearchModelState(settings(smart: true, ready: true, downloading: true, percent: 40)) == .ready)
        #expect(SmartSearchModelState(settings(smart: false, ready: false)) == .missing)
        #expect(SmartSearchModelState(settings(smart: true, ready: false, downloading: true, percent: 40))
                == .downloading(percent: 40, problem: nil))
        // Kept to 0-100; a problem of only spaces is none.
        #expect(SmartSearchModelState(settings(smart: true, ready: false, downloading: true, percent: 140, problem: "  "))
                == .downloading(percent: 100, problem: nil))
        #expect(SmartSearchModelState(settings(smart: true, ready: false, downloading: true, percent: -3,
                                               problem: " Can't reach the download server. "))
                == .downloading(percent: 0, problem: "Can't reach the download server."))
    }

    @Test func whatTheSectionSays() {
        inEnglish {
            for on in [false, true] {
                #expect(SmartSearchModelState.ready.words(on: on) == nil)
            }
            #expect(SmartSearchModelState.ready.progress == nil)
            // Missing: the note only while the switch is off. On (by default
            // since 152) the device is about to fetch it: nothing yet.
            #expect(SmartSearchModelState.missing.words(on: false)
                    == "Turning it on the first time downloads about 400 MB to your device.")
            #expect(SmartSearchModelState.missing.words(on: true) == nil)
            #expect(SmartSearchModelState.missing.progress == nil)
            let going = SmartSearchModelState.downloading(percent: 40, problem: nil)
            for on in [false, true] {
                #expect(going.words(on: on) == "Downloading what it needs… 40%")
            }
            #expect(going.progress == 0.4)
            // The device's words in its place, the bar where it got to.
            let waiting = SmartSearchModelState.downloading(percent: 40, problem: "Can't reach the download server.")
            #expect(waiting.words(on: true) == "Can't reach the download server.")
            #expect(waiting.progress == 0.4)
        }
    }

    @Test func voiceOverIsToldOnlyOfTheStartAProblemAndTheEnd() {
        typealias M = SmartSearchModelState
        func say(_ before: M, _ now: M) -> SmartSearchAnnouncement? { M.announcement(before: before, now: now) }
        func going(_ p: Int, _ problem: String? = nil) -> M { .downloading(percent: p, problem: problem) }
        // Started (turned on where it was missing).
        #expect(say(.missing, going(0)) == .progress(0))
        inEnglish { #expect(say(.missing, going(0))?.text == "Downloading what it needs… 0%") }
        // Never at every percent.
        #expect(say(going(0), going(3)) == nil)
        #expect(say(going(3), going(80)) == nil)
        // A problem, once; another problem; going again after one.
        #expect(say(going(10), going(10, "No internet")) == .problem("No internet"))
        #expect(say(going(10), going(10, "No internet"))?.text == "No internet")
        #expect(say(going(10, "No internet"), going(10, "No internet")) == nil)
        #expect(say(going(10, "No internet"), going(10, "Disk full")) == .problem("Disk full"))
        #expect(say(going(10, "No internet"), going(11)) == .progress(11))
        // Done: as 100%.
        #expect(say(going(97), .ready) == .progress(100))
        inEnglish { #expect(say(going(97), .ready)?.text == "Downloading what it needs… 100%") }
        // Turned off (the download stops), or there all along: nothing.
        #expect(say(going(40), .missing) == nil)
        #expect(say(.ready, .ready) == nil)
        #expect(say(.missing, .ready) == nil)
    }

    @Test func settingsAsksAgainOnlyWhileOnAndDownloading() {
        let going = SmartSearchModelState.downloading(percent: 10, problem: nil)
        #expect(SmartSearchSupport.polls(on: true, model: going))
        #expect(SmartSearchSupport.polls(on: true, model: .downloading(percent: 10, problem: "waiting")))
        #expect(!SmartSearchSupport.polls(on: false, model: going))
        #expect(!SmartSearchSupport.polls(on: nil, model: going))
        #expect(!SmartSearchSupport.polls(on: true, model: .ready))
        #expect(!SmartSearchSupport.polls(on: true, model: .missing))
        #expect(SmartSearchSupport.pollEvery == .seconds(3))
    }

    @Test func theRowNeedsTheModel() {
        let s = SmartSearchSupport()
        // A device before 152: on is enough, as before.
        s.deviceSaid(settings: settings(smart: true))
        #expect(s.isOn && s.searchable)
        s.deviceSaid(settings: settings(smart: false, ready: false))
        #expect(s.model == .missing && !s.searchable)
        s.deviceSaid(settings: settings(smart: true, ready: false, downloading: true, percent: 5))
        #expect(s.isOn && !s.searchable)
        s.deviceSaid(settings: settings(smart: true, ready: true))
        #expect(s.searchable)
        s.deviceSaid(settings: settings(smart: false, ready: true))
        #expect(!s.searchable)
    }

    @Test func turnedOnTheDownloadShowsAtOnceAndLogOutForgetsIt() {
        let s = SmartSearchSupport()
        s.deviceSaid(settings: settings(smart: false, ready: false))
        s.set(true)
        s.modelRequested()
        #expect(s.model == .downloading(percent: 0, problem: nil))
        // How far it is stays as the device said.
        s.deviceSaid(settings: settings(smart: true, ready: false, downloading: true, percent: 30))
        s.modelRequested()
        #expect(s.model == .downloading(percent: 30, problem: nil))
        s.reset()
        #expect(s.enabled == nil && s.model == .ready)
    }

    @Test func theSearchFieldAsksForTheModelAtMostEveryFiveSeconds() async {
        let s = SmartSearchSupport()
        var asked = 0
        var ready = false
        s.request = { payload in
            guard case .reqGetSettings = payload else { throw CancellationError() }
            asked += 1
            var r = Msg_RespEnvelope()
            r.payload = .respSettings(ready ? settings(smart: true, ready: true)
                                            : settings(smart: true, ready: false, downloading: true, percent: 80))
            return r
        }
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        // Off, or a device before it: nothing asked.
        #expect(s.searchFocused(now: t0) == nil)
        s.deviceSaid(settings: settings(smart: false, ready: false))
        #expect(s.searchFocused(now: t0) == nil)
        // On and ready: nothing to learn.
        s.deviceSaid(settings: settings(smart: true, ready: true))
        #expect(s.searchFocused(now: t0) == nil)
        // On, the model on its way: asked once...
        s.deviceSaid(settings: settings(smart: true, ready: false, downloading: true, percent: 40))
        await s.searchFocused(now: t0)?.value
        #expect(asked == 1 && s.model == .downloading(percent: 80, problem: nil))
        // ...not again within 5 s...
        #expect(s.searchFocused(now: t0 + 4.9) == nil)
        // ...and again after them: there now, the row is offered.
        ready = true
        await s.searchFocused(now: t0 + 5)?.value
        #expect(asked == 2 && s.searchable)
        #expect(s.searchFocused(now: t0 + 20) == nil)
        // Log Out forgets when it last asked.
        s.reset()
        s.deviceSaid(settings: settings(smart: true, ready: false))
        await s.searchFocused(now: t0 + 6)?.value
        #expect(asked == 3)
    }

    @Test func whetherTheRowIsOfferedAsItChanges() {
        let s = SmartSearchSupport()
        var seen: [Bool] = []
        let watching = s.searchableChanges.sink { seen.append($0) }
        s.deviceSaid(settings: settings(smart: true, ready: false, downloading: true, percent: 1))
        s.deviceSaid(settings: settings(smart: true, ready: false, downloading: true, percent: 2))
        s.deviceSaid(settings: settings(smart: true, ready: true))
        s.set(false)
        watching.cancel()
        // Each change once, not each step of the download.
        #expect(seen == [false, true, false])
        #expect(!SmartSearchSupport.searchable(on: nil, model: .ready))
        #expect(SmartSearchSupport.searchable(on: true, model: .ready))
    }

    @Test func askingAgainTakesTheSwitchAndTheModel() async {
        let s = SmartSearchSupport()
        var asked = 0
        s.request = { payload in
            guard case .reqGetSettings = payload else { throw CancellationError() }
            asked += 1
            var r = Msg_RespEnvelope()
            r.payload = .respSettings(settings(smart: true, ready: false, downloading: true, percent: 64))
            return r
        }
        let got = await s.refresh()
        #expect(asked == 1)
        #expect(got?.smartSearchModel.percent == 64)
        #expect(s.isOn && s.model == .downloading(percent: 64, problem: nil))
        // No answer: as it was.
        s.request = { _ in throw CancellationError() }
        #expect(await s.refresh() == nil)
        #expect(s.model == .downloading(percent: 64, problem: nil))
    }
}

@MainActor
struct SmartSearchSuggestionsTests {
    private func model(_ typed: String, scope: TopSearchModel.Scope = .everything) -> TopSearchModel {
        let m = TopSearchModel(scope: scope)
        m.query = typed
        return m
    }

    @Test func itsRowComesFirstAndTakesTheSearchKey() {
        let s = model("dog on the beach").suggestions(tags: ["dog", "beach"], inSearch: [], people: [], faces: false,
                                                      noFileSearch: false, smart: true)
        #expect(s.smart?.id == "smart")
        #expect(s.ordered.first?.id == "smart")
        #expect(s.ordered.last?.id == "docs") // the documents row stays, after it
        #expect(s.best == "smart")
        if case .smart(let t)? = s.smart { #expect(t == "dog on the beach") } else { Issue.record("no smart row") }
    }

    @Test func wordsThatAreExactlyATagStayATag() {
        // "beach" is the tag, the row after the things; "beac" is a text search.
        let s = model("beach").suggestions(tags: ["beach", "beaches"], inSearch: [], people: [], faces: false,
                                           noFileSearch: false, smart: true)
        #expect(s.best == "t:beach")
        #expect(s.ordered.map(\.id) == ["t:beach", "t:beaches", "smart", "docs"])
        let partial = model("beac").suggestions(tags: ["beach", "beaches"], inSearch: [], people: [], faces: false,
                                                noFileSearch: false, smart: true)
        #expect(partial.best == "smart")
        #expect(partial.ordered.map(\.id) == ["smart", "t:beach", "t:beaches", "docs"])
        // The arrows still win.
        #expect(partial.active("t:beaches") == "t:beaches")
    }

    @Test func aPersonsExactNameStaysThePerson() {
        var ana = Msg_Person()
        ana.id = "1"
        ana.name = "Ana"
        ana.faceCount = 3
        let s = model("ana").suggestions(tags: [], inSearch: [], people: [ana], faces: true, noFileSearch: false, smart: true)
        #expect(s.best == "p:1")
        #expect(s.ordered.map(\.id) == ["p:1", "smart", "docs"])
    }

    @Test func offOrInFilesTheSearchIsAsBefore() {
        let off = model("dog on the beach").suggestions(tags: ["dog"], inSearch: [], people: [], faces: false, noFileSearch: false)
        #expect(off.smart == nil)
        #expect(!off.ordered.contains { $0.id == "smart" })
        #expect(off.best == "docs")
        let files = model("plan", scope: .files).suggestions(tags: [], inSearch: [], people: [], faces: false,
                                                            noFileSearch: false, smart: true)
        #expect(files.smart == nil)
    }
}

/// The device, for Images: every SearchPhotos recorded; a text search
/// answered with `reading`, or refused with `refuse` (code, words).
@MainActor
final class SmartSearchDevice {
    var searches: [Msg_SearchPhotos] = []
    var reading = false
    var refuse: (String, String)?

    func attach(_ vm: PhotoGalleryVM) {
        vm.pageRetry.delay = { _ in 0.05 }
        vm.bucketRetry.delay = { _ in 0.05 }
        vm.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: false, statusCode: nil) }
        vm.photoRequest = { [weak self] payload, _ in
            guard let self else { throw CancellationError() }
            var r = Msg_RespEnvelope()
            switch payload {
            case .reqSearchPhotos(let sp):
                self.searches.append(sp)
                if !sp.text.isEmpty, let (code, words) = self.refuse {
                    r.error = true
                    r.errorCode = code
                    r.errorMessage = words
                    return r
                }
                var lof = Msg_ListOfFiles()
                lof.files = (0..<3).map { i in
                    var f = Msg_File()
                    f.hash = "\(sp.text.isEmpty ? "d" : "s")\(i)"
                    f.path = "/Photos/\(f.hash).jpg"
                    f.mime = "image/jpeg"
                    f.created = Google_Protobuf_Timestamp(date: Date(timeIntervalSince1970: 1_700_000_000 - Double(i) * 86_400))
                    return f
                }
                lof.smartSearchReading = !sp.text.isEmpty && self.reading
                r.payload = .respListOfFiles(lof)
                return r
            case .reqPhotoDateBuckets:
                r.payload = .respPhotoDateBuckets(Msg_RespPhotoDateBuckets())
                return r
            default:
                throw CancellationError()
            }
        }
    }
}

@MainActor
private func until(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct SmartSearchGalleryTests {
    private func gallery(_ device: SmartSearchDevice) async -> PhotoGalleryVM {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        device.attach(vm)
        await vm.resetAndLoadFirstPage()
        return vm
    }

    @Test func theWordsGoWithTheChipsRankedAndNeverByDate() async {
        SmartSearchSupport.shared.set(true)
        defer { SmartSearchSupport.shared.reset() }
        let device = SmartSearchDevice()
        device.reading = true
        let vm = await gallery(device)
        vm.addChip("beach")
        await until { device.searches.last?.tags == ["beach"] && !vm.loading }
        vm.searchText("  kids in the snow ")
        await until { device.searches.last?.text == "kids in the snow" && vm.items.first?.hash == "s0" }
        let sp = device.searches.last!
        #expect(sp.text == "kids in the snow")
        #expect(sp.tags == ["beach"])
        #expect(!sp.hasBefore && !sp.hasNewerThan)
        #expect(vm.smartText == "kids in the snow")
        #expect(vm.ranked)
        #expect(vm.smartReading)
        #expect(vm.emptyKind == .noMatch)

        // The chip's ×: back to the tags alone.
        vm.clearSmartText()
        await until { device.searches.last?.text == "" && vm.items.first?.hash == "d0" }
        #expect(vm.smartText == nil)
        #expect(!vm.smartReading)
        #expect(device.searches.last?.tags == ["beach"])
    }

    @Test func offOrOnADeviceBeforeItNothingIsSent() async {
        SmartSearchSupport.shared.set(nil)
        let device = SmartSearchDevice()
        let vm = await gallery(device)
        let before = device.searches.count
        vm.searchText("kids in the snow")
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(vm.smartText == nil)
        #expect(device.searches.count == before)
        #expect(device.searches.allSatisfy { $0.text.isEmpty })
    }

    @Test func turnedOffElsewhereTheWordsGoWithTheDevicesOwn() async {
        SmartSearchSupport.shared.set(true)
        defer { SmartSearchSupport.shared.reset() }
        let device = SmartSearchDevice()
        device.refuse = ("smart_search_off", "Smart search is off")
        let vm = await gallery(device)
        vm.searchText("kids in the snow")
        await until { vm.smartText == nil && device.searches.count >= 3 && !vm.loading }
        #expect(vm.smartText == nil)
        #expect(vm.notice?.text == "Smart search is off")
        #expect(!SmartSearchSupport.shared.isOn)
        // The search ran again without the words.
        #expect(device.searches.last?.text == "")
    }

    @Test func noModelWithoutWordsSaysItInOurs() async {
        SmartSearchSupport.shared.set(true)
        defer { SmartSearchSupport.shared.reset() }
        let device = SmartSearchDevice()
        device.refuse = ("smart_search_unavailable", "")
        let vm = await gallery(device)
        vm.searchText("kids in the snow")
        await until { vm.smartText == nil && device.searches.count >= 3 && !vm.loading }
        let was = L10n.shared.code
        L10n.shared.set("en")
        let words = vm.notice?.text
        L10n.shared.set(was)
        #expect(words == "Smart search isn't available right now.")
        // Unavailable is not off: the switch stays as the device said.
        #expect(SmartSearchSupport.shared.isOn)
    }

    @Test func stillDownloadingTheWordsGoTheSwitchStaysAndTheModelIsAskedFor() async {
        let support = SmartSearchSupport.shared
        let ask = support.request
        support.set(true)
        var asked = 0
        support.request = { payload in
            guard case .reqGetSettings = payload else { throw CancellationError() }
            asked += 1
            var r = Msg_RespEnvelope()
            r.payload = .respSettings(settings(smart: true, ready: false, downloading: true, percent: 12))
            return r
        }
        defer {
            support.request = ask
            support.reset()
        }
        let device = SmartSearchDevice()
        device.refuse = ("smart_search_downloading", "Smart search is still getting ready.")
        let vm = await gallery(device)
        vm.searchText("kids in the snow")
        await until { vm.smartText == nil && device.searches.count >= 3 && !vm.loading && asked == 1 }
        #expect(vm.smartText == nil)
        #expect(vm.notice?.text == "Smart search is still getting ready.")
        #expect(device.searches.last?.text == "")
        // On still, but no row (nor words taken) until the model is there.
        #expect(support.isOn)
        #expect(asked == 1)
        #expect(support.model == .downloading(percent: 12, problem: nil))
        #expect(!support.searchable)
        let before = device.searches.count
        vm.searchText("kids in the snow")
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(vm.smartText == nil && device.searches.count == before)
    }

    @Test func thePanelsRowWaitsForTheModel() async {
        let support = SmartSearchSupport.shared
        defer { support.reset() }
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let search = TopSearchModel()
        search.query = "kids in the snow"
        let actions = TopSearchActions(vm: vm, search: search, faces: false, filesNav: FilesNav.shared, focus: { _ in })
        // Turned on, the model on its way: the search is as when off.
        support.deviceSaid(settings: settings(smart: true, ready: false, downloading: true, percent: 50))
        var s = actions.suggestions()
        #expect(s.smart == nil)
        #expect(!s.ordered.contains { $0.id == "smart" })
        #expect(s.best != "smart")
        // There: first, and what the Search key takes.
        support.deviceSaid(settings: settings(smart: true, ready: true))
        s = actions.suggestions()
        #expect(s.smart?.id == "smart")
        #expect(s.best == "smart")
    }
}
}
