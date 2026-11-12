// SPDX-License-Identifier: AGPL-3.0-or-later

import Combine
import Foundation
import Testing
@testable import OffTheCloud

/// Text in photos (TextInPhotos.swift, device release 180): the search's
/// free-text row offered while it is on - with Smart search off or its
/// model not there yet too -, the words sent then, a refusal taking it off,
/// and what Info shows of a photo's text. And Pets (the same release):
/// People shows while it is on too (FaceRecognition). Android's
/// TextInPhotosTest has the same rules.

/// Settings with Smart search's switch, its model (nil: a device before
/// 152; false: on its way) and Text in photos' switch (nil: a device
/// before 180).
@MainActor
private func settings(smart: Bool? = nil, ready: Bool? = nil, text: Bool? = nil) -> Msg_Settings {
    var s = Msg_Settings()
    if let smart { s.smartSearchEnabled = smart }
    if let ready {
        var m = Msg_SmartSearchModel()
        m.ready = ready
        m.downloading = !ready
        s.smartSearchModel = m
    }
    if let text { s.textInPhotosEnabled = text }
    return s
}

@MainActor
private func waitFor(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

@MainActor
struct TextInPhotosSupportTests {
    @Test func theRowIsOfferedWhileEitherAnswersTheWords() {
        #expect(SmartSearchSupport.searchable(on: false, model: .ready, text: true))
        #expect(SmartSearchSupport.searchable(on: nil, model: .ready, text: true))
        #expect(SmartSearchSupport.searchable(on: true, model: .missing, text: true))
        #expect(SmartSearchSupport.searchable(on: true, model: .downloading(percent: 5, problem: nil), text: true))
        #expect(SmartSearchSupport.searchable(on: true, model: .ready, text: false))
        #expect(!SmartSearchSupport.searchable(on: false, model: .ready, text: false))
        #expect(!SmartSearchSupport.searchable(on: true, model: .missing, text: nil))
        #expect(!SmartSearchSupport.searchable(on: nil, model: .ready, text: nil))

        let s = SmartSearchSupport()
        s.deviceSaid(settings: settings(smart: false, text: true))
        #expect(s.textInPhotos == true && !s.isOn && s.searchable)
        // Smart search's model on its way: the text answers meanwhile.
        s.deviceSaid(settings: settings(smart: true, ready: false, text: true))
        #expect(s.searchable)
        s.deviceSaid(settings: settings(smart: true, ready: false, text: false))
        #expect(s.textInPhotos == false && !s.searchable)
        // A device before it: nothing, and the search as before.
        s.deviceSaid(settings: settings(smart: true, ready: true))
        #expect(s.textInPhotos == nil && s.searchable)
        s.deviceSaid(settings: settings(smart: false))
        #expect(s.textInPhotos == nil && !s.searchable)
    }

    @Test func theSwitchARefusalAndLogOut() {
        let s = SmartSearchSupport()
        // Settings' switch acknowledged.
        s.setTextInPhotos(true)
        #expect(s.searchable)
        // The device refused a search's words: off there too.
        s.textRefused()
        #expect(s.textInPhotos == false && !s.searchable)
        // A device before it stays as it was.
        s.setTextInPhotos(nil)
        s.textRefused()
        #expect(s.textInPhotos == nil)
        // Log Out: the next device says for itself.
        s.setTextInPhotos(true)
        s.reset()
        #expect(s.textInPhotos == nil && !s.searchable)
    }

    @Test func whetherTheRowIsOfferedAsTheSwitchesChange() {
        let s = SmartSearchSupport()
        var seen: [Bool] = []
        let watching = s.searchableChanges.sink { seen.append($0) }
        s.deviceSaid(settings: settings(smart: false, text: true))
        s.deviceSaid(settings: settings(smart: true, ready: true, text: true))
        s.setTextInPhotos(false)
        s.set(false)
        watching.cancel()
        // Each change once: offered with the text alone, still with Smart
        // search on, gone when neither is.
        #expect(seen == [false, true, false])
    }
}

@MainActor
struct PeopleWithPetsTests {
    @Test func peopleShowsWithFaceRecognitionOrPets() {
        #expect(FaceRecognition.peopleShown(faces: true, pets: nil) == true)
        #expect(FaceRecognition.peopleShown(faces: false, pets: true) == true)
        #expect(FaceRecognition.peopleShown(faces: nil, pets: true) == true)
        #expect(FaceRecognition.peopleShown(faces: true, pets: false) == true)
        #expect(FaceRecognition.peopleShown(faces: false, pets: false) == false)
        // A device without Pets: as before.
        #expect(FaceRecognition.peopleShown(faces: false, pets: nil) == false)
        #expect(FaceRecognition.peopleShown(faces: nil, pets: nil) == nil)
        #expect(FaceRecognition.peopleShown(faces: nil, pets: false) == nil)
    }
}

@MainActor
struct PhotoTextTests {
    @Test func infoShowsTheTextAsItCame() {
        #expect(PhotoText.shown(nil) == nil)
        var info = Msg_FileExifInfo()
        // None read (or not yet, or off), or a device before it.
        #expect(PhotoText.shown(info) == nil)
        info.text = "  \n "
        #expect(PhotoText.shown(info) == nil)
        info.text = "GTS-04-R\nNO PARKING\n  Mon-Fri 8-20"
        #expect(PhotoText.shown(info) == "GTS-04-R\nNO PARKING\n  Mon-Fri 8-20")
        #expect(PhotoText.copiedFor == .milliseconds(2500))
    }

    @Test func theWordsInEnglish() {
        let was = L10n.shared.code
        L10n.shared.set("en")
        defer { L10n.shared.set(was) }
        #expect(S.appPhotosViewerTextTitle == "Text in this photo")
        #expect(S.appPhotosViewerTextCopy == "Copy")
        #expect(S.appPhotosViewerTextCopied == "Copied")
        #expect(S.appSettingsTextPhotosTitle == "Text in photos")
        #expect(S.appSettingsTextPhotosOffTitle == "Turn off Text in photos?")
        #expect(S.appSettingsTextPhotosOffConfirm == "Turn Off")
        #expect(S.appSettingsPetsToggle == "Pets")
        #expect(S.appSettingsPetsFooter.hasPrefix("Your device finds your dogs and cats"))
    }
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct TextInPhotosGalleryTests {
    private func gallery(_ device: SmartSearchDevice) async -> PhotoGalleryVM {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        device.attach(vm)
        await vm.resetAndLoadFirstPage()
        return vm
    }

    @Test func withSmartSearchOffTheWordsGoToTheDevice() async {
        let support = SmartSearchSupport.shared
        support.deviceSaid(settings: settings(smart: false, ready: false, text: true))
        defer { support.reset() }
        let device = SmartSearchDevice()
        let vm = await gallery(device)
        vm.searchText(" GTS-04-R ")
        await waitFor { device.searches.last?.text == "GTS-04-R" && vm.items.first?.hash == "s0" }
        #expect(device.searches.last?.text == "GTS-04-R")
        #expect(vm.smartText == "GTS-04-R")
        #expect(vm.ranked)
    }

    @Test func offOnADeviceBeforeItNothingIsSent() async {
        let support = SmartSearchSupport.shared
        support.deviceSaid(settings: settings(smart: false))
        defer { support.reset() }
        let device = SmartSearchDevice()
        let vm = await gallery(device)
        let before = device.searches.count
        vm.searchText("GTS-04-R")
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(vm.smartText == nil)
        #expect(device.searches.count == before)
    }

    @Test func aRefusalTakesTheWordsAndTextInPhotosAway() async {
        let support = SmartSearchSupport.shared
        support.deviceSaid(settings: settings(smart: false, text: true))
        defer { support.reset() }
        let device = SmartSearchDevice()
        // Turned off on another app: the device answers as before Text in photos.
        device.refuse = ("smart_search_off", "Smart search is off")
        let vm = await gallery(device)
        vm.searchText("GTS-04-R")
        await waitFor { vm.smartText == nil && device.searches.count >= 3 && !vm.loading }
        #expect(vm.smartText == nil)
        #expect(vm.notice?.text == "Smart search is off")
        #expect(support.textInPhotos == false)
        #expect(!support.searchable)
        #expect(device.searches.last?.text == "")
    }

    @Test func peopleComesWithPetsAndGoesWithBoth() {
        let people = FaceRecognition.shared
        defer { people.reset() }
        var s = Msg_Settings()
        s.faceRecognitionEnabled = false
        s.petsEnabled = true
        people.deviceSaid(settings: s)
        #expect(people.isOn)
        // Pets' switch turned off, face recognition's on: People as they say.
        people.setPets(false)
        #expect(people.enabled == false && !people.isOn)
        people.set(true)
        #expect(people.isOn)
        // A device before Pets: face recognition alone, as before.
        people.deviceSaid(settings: Msg_Settings())
        #expect(people.enabled == false)
        people.reset()
        #expect(people.enabled == nil)
    }

    @Test func thePanelsRowWithTextInPhotosOn() async {
        let support = SmartSearchSupport.shared
        defer { support.reset() }
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let search = TopSearchModel()
        search.query = "GTS-04-R"
        let actions = TopSearchActions(vm: vm, search: search, faces: false, filesNav: FilesNav.shared, focus: { _ in })
        // Smart search off and its model not there: no row without Text in photos...
        support.deviceSaid(settings: settings(smart: false, ready: false, text: false))
        #expect(actions.suggestions().smart == nil)
        // ...and with it the row, first, and what the Search key takes.
        support.deviceSaid(settings: settings(smart: false, ready: false, text: true))
        let s = actions.suggestions()
        #expect(s.smart?.id == "smart")
        #expect(s.best == "smart")
    }
}
}
