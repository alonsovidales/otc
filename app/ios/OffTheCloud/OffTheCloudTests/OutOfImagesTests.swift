// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Keeping a folder out of Images from Files (issue #192): what a listing
/// carries, what the switch sends and shows, and Images asking again.
/// Serialized: the switch tells Images through the notification center,
/// which every test here listens to.
// Nested in ImagesNotificationTests (RetryTests.swift): its Files test and
// imagesAsksAgainForWhatItHasShown post otcImagesChanged, which every
// Images model alive hears - never while ImagesRetryTests counts requests.
extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct OutOfImagesTests {
    private func file(_ path: String, dir: Bool = false, out: Bool = false) -> Msg_File {
        var f = Msg_File()
        f.path = path
        f.mime = dir ? "inode/directory" : "image/jpeg"
        f.outOfImages = out
        return f
    }

    private func listing(_ files: [Msg_File], supported: Bool, folderOut: Bool) -> Msg_RespEnvelope {
        var lof = Msg_ListOfFiles()
        lof.files = files
        lof.outOfImagesSupported = supported
        lof.folderOutOfImages = folderOut
        var r = Msg_RespEnvelope()
        r.payload = .respListOfFiles(lof)
        return r
    }

    nonisolated private static func ack() -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        var a = Msg_Ack()
        a.ok = true
        r.payload = .respAck(a)
        return r
    }

    nonisolated private static func refusal(_ code: String, _ message: String) -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        r.error = true
        r.errorCode = code
        r.errorMessage = message
        return r
    }

    final class Sent {
        var list: [Msg_ReqEnvelope.OneOf_Payload] = []
        var switches: [Msg_SetOutOfImages] {
            list.compactMap { if case .reqSetOutOfImages(let s) = $0 { return s } else { return nil } }
        }
        var listings: Int {
            list.filter { if case .reqListFiles = $0 { return true } else { return false } }.count
        }
    }

    /// An explorer at `path` whose listing is `files`; the switch answers
    /// `answer` (a throw: no answer).
    private func explorer(
        at path: String, _ files: [Msg_File], supported: Bool = true, folderOut: Bool = false,
        answer: @escaping () throws -> Msg_RespEnvelope = { OutOfImagesTests.ack() }
    ) -> (FilesExplorerViewModel, Sent) {
        let vm = FilesExplorerViewModel(initialPath: path)
        let sent = Sent()
        let list = listing(files, supported: supported, folderOut: folderOut)
        vm.request = { payload in
            sent.list.append(payload)
            if case .reqListFiles = payload { return list }
            return try answer()
        }
        return (vm, sent)
    }

    /// Counts the notifications that tell Images, while `body` runs.
    private func imagesToldDuring(_ body: () async -> Void) async -> Int {
        final class Count: @unchecked Sendable { var n = 0 }
        let count = Count()
        let token = NotificationCenter.default.addObserver(forName: .otcImagesChanged, object: nil, queue: nil) { _ in
            count.n += 1
        }
        await body()
        NotificationCenter.default.removeObserver(token)
        return count.n
    }

    @Test func rowsCarryTheMark() {
        #expect(FileRow(file: file("/Private/Trip", dir: true, out: true)).outOfImages)
        #expect(FileRow(file: file("/Private/a.jpg", out: true)).outOfImages)
        #expect(!FileRow(file: file("/Phone", dir: true)).outOfImages)
    }

    @Test func theListingSaysWhetherTheDeviceCanAndTheFolderIs() async {
        let (vm, _) = explorer(at: "/Private/", [file("/Private/Trip", dir: true, out: true), file("/Private/a.jpg", out: true)], folderOut: true)
        await vm.load()
        #expect(vm.outOfImagesSupported)
        #expect(vm.folderOutOfImages)
        // ".." first, then the folder kept out and the file in it.
        #expect(vm.rows.map(\.outOfImages) == [false, true, true])
        let ask = vm.outOfImagesAsk(vm.rows[1])
        #expect(ask == OutOfImagesAsk(path: "/Private/Trip", name: "Trip", outOfImages: true))
        // The folder browsed as its row would name it: no trailing slash.
        #expect(vm.currentFolder == OutOfImagesAsk(path: "/Private", name: "Private", outOfImages: true))
        #expect(vm.outOfImagesBanner)
    }

    @Test func aDeviceTooOldSaysNothingAboutIt() async {
        // Before release 108 the listing has neither field: no switch, no banner.
        let (vm, _) = explorer(at: "/", [file("/Photos", dir: true)], supported: false)
        await vm.load()
        #expect(!vm.outOfImagesSupported)
        #expect(!vm.folderOutOfImages)
        #expect(!vm.outOfImagesBanner)
    }

    @Test func aFailedListingDropsTheBanner() async {
        let vm = FilesExplorerViewModel(initialPath: "/Private/")
        var first = true
        vm.request = { [self] _ in
            if first {
                first = false
                return listing([], supported: true, folderOut: true)
            }
            return OutOfImagesTests.refusal("", "no such folder")
        }
        await vm.load()
        #expect(vm.folderOutOfImages)
        await vm.load()
        #expect(!vm.folderOutOfImages)
        #expect(vm.outOfImagesSupported)
        #expect(vm.error == "no such folder")
    }

    @Test func keepingOutSendsTheFolderTellsImagesAndListsAgain() async {
        let (vm, sent) = explorer(at: "/", [file("/Private", dir: true)])
        await vm.load()
        let told = await imagesToldDuring {
            await vm.setOutOfImages(vm.outOfImagesAsk(vm.rows[0]).path, keepOut: true)
        }
        #expect(told == 1)
        #expect(sent.switches.map(\.path) == ["/Private"])
        #expect(sent.switches.map(\.outOfImages) == [true])
        #expect(sent.listings == 2)
        #expect(vm.toast == nil)
    }

    @Test func showingTheFolderBrowsedSendsItsPath() async {
        let (vm, sent) = explorer(at: "/Private/Trip/", [], folderOut: true)
        await vm.load()
        let ask = vm.currentFolder
        let told = await imagesToldDuring {
            await vm.setOutOfImages(ask.path, keepOut: !ask.outOfImages)
        }
        #expect(told == 1)
        #expect(sent.switches.map(\.path) == ["/Private/Trip"])
        #expect(sent.switches.map(\.outOfImages) == [false])
    }

    @Test func aRefusalShowsTheDevicesWordsAndTellsNobody() async {
        let words = "Trip is inside /Private, which is kept out of Images - show /Private in Images to show Trip"
        let (vm, sent) = explorer(at: "/Private/", [file("/Private/Trip", dir: true, out: true)], folderOut: true) {
            OutOfImagesTests.refusal("out_of_images_by_parent", words)
        }
        var said: [String] = []
        vm.announce = { said.append($0) }
        await vm.load()
        let told = await imagesToldDuring {
            await vm.setOutOfImages("/Private/Trip", keepOut: false)
        }
        #expect(told == 0)
        #expect(vm.toast == words)
        // VoiceOver hears it too: the toast alone looks like nothing happened.
        #expect(said == [words])
        // Listed again either way: the folder shows as the device has it.
        #expect(sent.listings == 2)
    }

    @Test func anUnansweredSwitchSaysSo() async {
        let (vm, _) = explorer(at: "/", [file("/Private", dir: true)]) { throw CancellationError() }
        var said: [String] = []
        vm.announce = { said.append($0) }
        let told = await imagesToldDuring {
            await vm.setOutOfImages("/Private", keepOut: true)
        }
        #expect(told == 0)
        #expect(vm.toast == "Could not update the folder")
        #expect(said == ["Could not update the folder"])
    }

    @Test func aDoneSwitchSaysNothing() async {
        let (vm, _) = explorer(at: "/", [file("/Private", dir: true)])
        var said: [String] = []
        vm.announce = { said.append($0) }
        await vm.setOutOfImages("/Private", keepOut: true)
        #expect(said.isEmpty)
    }

    /// Holds a request's answer until the test gives it.
    @MainActor final class Gate {
        private var waiting: CheckedContinuation<Msg_RespEnvelope, Error>?
        var held: Bool { waiting != nil }
        func wait() async throws -> Msg_RespEnvelope {
            try await withCheckedThrowingContinuation { waiting = $0 }
        }
        func answer(_ r: Msg_RespEnvelope) {
            waiting?.resume(returning: r)
            waiting = nil
        }
    }

    @Test func oneSwitchAtATimeAndItsFolderShowsIt() async {
        // In the kept-out /x/sub/, its banner's "Show in Images" sends the
        // folder browsed - with its trailing slash in `path`.
        let gate = Gate()
        let (vm, sent) = explorer(at: "/x/sub/", [file("/x/sub/deeper", dir: true, out: true)], folderOut: true) {
            throw CancellationError()
        }
        let list = vm.request
        vm.request = { payload in
            if case .reqSetOutOfImages = payload {
                sent.list.append(payload)
                return try await gate.wait()
            }
            return try await list(payload)
        }
        await vm.load()
        #expect(vm.outOfImagesIdle)
        let first = Task { await vm.setOutOfImages(vm.currentFolder.path, keepOut: false) }
        await until { gate.held }
        // Under way: the banner's folder and the same folder's row (from
        // the folder above) both show it, others don't, and nothing else
        // may start (the view disables every switch on outOfImagesIdle).
        #expect(!vm.outOfImagesIdle)
        #expect(vm.outOfImagesBusy == "/x/sub")
        #expect(vm.isOutOfImagesBusy("/x/sub/"))
        #expect(vm.isOutOfImagesBusy("/x/sub"))
        #expect(!vm.isOutOfImagesBusy("/x/sub/deeper"))
        #expect(!vm.isOutOfImagesBusy("/x"))
        // A tap racing the disabled controls sends nothing.
        await vm.setOutOfImages("/x/sub/deeper", keepOut: false)
        #expect(sent.switches.map(\.path) == ["/x/sub"])
        gate.answer(OutOfImagesTests.ack())
        await first.value
        #expect(vm.outOfImagesIdle)
        #expect(!vm.isOutOfImagesBusy("/x/sub"))
        // Once answered, the next one goes.
        let second = Task { await vm.setOutOfImages("/x/sub/deeper", keepOut: false) }
        await until { gate.held }
        #expect(vm.isOutOfImagesBusy("/x/sub/deeper"))
        gate.answer(OutOfImagesTests.ack())
        await second.value
        #expect(sent.switches.map(\.path) == ["/x/sub", "/x/sub/deeper"])
    }

    @Test func foldersCompareWithoutTheirTrailingSlash() {
        #expect(FilesExplorerViewModel.folderKey("/") == "/")
        #expect(FilesExplorerViewModel.folderKey("/x/sub/") == "/x/sub")
        #expect(FilesExplorerViewModel.folderKey("/x/sub") == "/x/sub")
        #expect(FilesExplorerViewModel.folderKey("/x//") == "/x")
    }

    @Test func theBannerWaitsForTheNextFoldersListing() async {
        // Kept out: /x/sub/ and /x/sub2/. /sub/ is not, and its listing is
        // held to look at the screen in between.
        let gate = Gate()
        let vm = FilesExplorerViewModel(initialPath: "/x/sub/")
        vm.request = { [self] payload in
            guard case .reqListFiles(let req) = payload else { throw CancellationError() }
            switch req.path {
            case "/sub/": return try await gate.wait()
            default: return listing([], supported: true, folderOut: req.path.hasPrefix("/x/sub"))
            }
        }
        await vm.load()
        #expect(vm.outOfImagesBanner)
        #expect(vm.listedPath == "/x/sub/")

        // Typed a new path: the old folder's banner goes at once - its
        // button would act on /sub - while the old rows stay until the
        // new listing comes.
        vm.navigate(to: "/sub")
        #expect(vm.path == "/sub/")
        #expect(!vm.outOfImagesBanner)
        await until { gate.held }
        #expect(!vm.outOfImagesBanner)
        gate.answer(listing([], supported: true, folderOut: false))
        await until { vm.listedPath == "/sub/" }
        #expect(!vm.outOfImagesBanner)

        // Another kept-out folder: its banner once it is listed.
        await vm.show("/x/sub2")
        #expect(vm.listedPath == "/x/sub2/")
        #expect(vm.outOfImagesBanner)
        #expect(vm.currentFolder.path == "/x/sub2")

        // Going up to the root: no banner over it before its listing
        // says so (the device refuses to switch "/").
        vm.navigate(to: "/")
        #expect(!vm.outOfImagesBanner)
        await until { vm.listedPath == "/" }
        #expect(!vm.outOfImagesBanner)
    }

    @Test func wordsAreTheSpecs() {
        #expect(OutOfImagesText.keep == "Keep Out of Images")
        #expect(OutOfImagesText.show == "Show in Images")
        #expect(OutOfImagesText.state == "Kept out of Images")
        #expect(OutOfImagesText.explain == "Photos and videos here aren't tagged, searched for faces or shown in Images. Files still shows them.")
        #expect(OutOfImagesText.promptTitle(name: "Trip", outOfImages: false) == "Keep \u{201C}Trip\u{201D} out of Images?")
        #expect(OutOfImagesText.promptTitle(name: "Trip", outOfImages: true) == "Show \u{201C}Trip\u{201D} in Images?")
        #expect(OutOfImagesText.keepMessage.hasSuffix("the tags and faces already found in them are deleted. Files still shows them."))
        #expect(OutOfImagesText.showMessage == "Its photos and videos go back to Images, and are tagged - and searched for faces, if face recognition is on - in the background.")
        #expect(OutOfImagesText.banner == "Kept out of Images - photos and videos here aren't tagged, searched for faces or shown in Images.")
    }

    /// Waits (a second at most) for `done`.
    private func until(_ done: () -> Bool) async {
        for _ in 0..<100 where !done() {
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    @Test func imagesAsksAgainForWhatItHasShown() async {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil)
        let sent = Sent()
        vm.libraryRequest = { payload, _ in
            sent.list.append(payload)
            var r = Msg_RespEnvelope()
            switch payload {
            case .reqGetTags:
                var t = Msg_TagsList()
                t.tags = ["beach"]
                r.payload = .respTagsList(t)
            case .reqListImageGroups:
                r.payload = .respImageGroups(Msg_ImageGroups())
            default:
                throw CancellationError()
            }
            return r
        }
        vm.peopleRequest = { payload, _ in
            sent.list.append(payload)
            throw CancellationError()
        }
        #expect(await vm.loadGroups())
        vm.loadLibraryOnce()
        await until { vm.tags == ["beach"] }
        sent.list.removeAll()

        NotificationCenter.default.post(name: .otcImagesChanged, object: nil)
        func asked(_ p: Msg_ReqEnvelope.OneOf_Payload) -> Bool {
            switch p {
            case .reqGetTags, .reqListImageGroups: return true
            default: return false
            }
        }
        await until { sent.list.filter(asked).count >= 2 }
        #expect(sent.list.contains { if case .reqGetTags = $0 { return true } else { return false } })
        #expect(sent.list.contains { if case .reqListImageGroups = $0 { return true } else { return false } })
        // The photos were never shown: no search goes out for them.
        #expect(!sent.list.contains { if case .reqSearchPhotos = $0 { return true } else { return false } })
    }

    @Test func theFilesViewerAsksNothing() async {
        let vm = PhotoGalleryVM()
        let sent = Sent()
        vm.libraryRequest = { payload, _ in
            sent.list.append(payload)
            throw CancellationError()
        }
        vm.peopleRequest = vm.libraryRequest
        vm.imagesChanged()
        try? await Task.sleep(nanoseconds: 50_000_000)
        #expect(sent.list.isEmpty)
    }
}
}

/// Where the search's panel goes (SearchPanelPlace), as Android's
/// dropdownPlace: under the field, or risen over it above the keyboard.
struct SearchPanelPlaceTests {
    @Test func underTheFieldWithRoom() {
        // A phone upright: the keyboard leaves plenty under the field.
        let p = SearchPanelPlace(under: 64, bottom: 500, ceiling: 4, keyboardUp: true, cap: 640)
        #expect(!p.rises)
        #expect(p.maxHeight == CGFloat(500 - 8 - 64))
        #expect(p.top(height: 120) == 64)
        // A cap below the room is kept.
        #expect(SearchPanelPlace(under: 64, bottom: 900, ceiling: 4, keyboardUp: false, cap: 300).maxHeight == 300)
    }

    @Test func risesOverTheFieldAboveTheKeyboard() {
        // A phone turned sideways: 150 left above the keyboard, 58 of it the top bar.
        let p = SearchPanelPlace(under: 58, bottom: 150, ceiling: 4, keyboardUp: true, cap: 281)
        #expect(p.rises)
        #expect(p.maxHeight == CGFloat(150 - 8 - 4))
        // Only as far as its rows need: two short rows stay under the field...
        #expect(p.top(height: 60) == 58)
        // ...more go up, foot just above the keyboard...
        #expect(p.top(height: 120) == CGFloat(150 - 8 - 120))
        // ...as high as the ceiling.
        #expect(p.top(height: 138) == 4)
    }

    @Test func backUnderTheFieldWhenTheKeyboardGoes() {
        let p = SearchPanelPlace(under: 58, bottom: 150, ceiling: 4, keyboardUp: false, cap: 281)
        #expect(!p.rises)
        // The old floor stands without the keyboard: never shorter than 88.
        #expect(p.maxHeight == 88)
        #expect(p.top(height: 88) == 58)
    }

    @Test func theNarrowPanelFillsUnlessItRises() {
        let full = SearchPanelPlace(under: 54, bottom: 400, ceiling: 4, keyboardUp: true, cap: nil)
        #expect(!full.rises)
        #expect(full.maxHeight == CGFloat(400 - 54))
        #expect(full.top(height: 346) == 54)
        let short = SearchPanelPlace(under: 54, bottom: 180, ceiling: 4, keyboardUp: true, cap: nil)
        #expect(short.rises)
        #expect(short.maxHeight == CGFloat(180 - 8 - 4))
        #expect(short.top(height: 100) == 54)
        #expect(short.top(height: 168) == 4)
    }
}
