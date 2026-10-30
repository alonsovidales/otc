// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Smart search (SmartSearch.swift): when it is offered, the search's row,
/// the request it sends, the reading note and the device's refusals.
/// Android's SmartSearchTest has the same cases.

@MainActor
private func settings(smart: Bool?) -> Msg_Settings {
    var s = Msg_Settings()
    if let smart { s.smartSearchEnabled = smart }
    return s
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
        #expect(SmartSearchSupport.refusal(resp("smart_search_off", "Smart search is off"))! == ("Smart search is off", true))
        #expect(SmartSearchSupport.refusal(resp("smart_search_unavailable", ""))! == (nil, false))
        #expect(SmartSearchSupport.refusal(resp("", "internal error")) == nil)
        #expect(SmartSearchSupport.refusal(nil) == nil)
        #expect(SmartSearchSupport.text("  kids in the snow \n") == "kids in the snow")
        #expect(SmartSearchSupport.text("   ") == nil)
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
}
}
