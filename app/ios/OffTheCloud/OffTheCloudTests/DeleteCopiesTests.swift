// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Issue #201: deleting a photo held at several paths (DeleteCopies.swift)
/// - which question Images asks, what each answer sends, and what a refusal
/// does. The same cases as Android's and the web's.
@MainActor
struct CopiesAskTests {
    @Test func oneHeldAtSeveralPathsAsksAboutItsCopies() {
        #expect(CopiesAsk.of([3]) == .one(count: 3))
        #expect(CopiesAsk.of([2]) == .one(count: 2))
        // One copy, or a device that doesn't say (before #201): as before.
        #expect(CopiesAsk.of([1]) == .plain)
        #expect(CopiesAsk.of([nil]) == .plain)
        #expect(CopiesAsk.of([]) == .plain)
    }

    @Test func severalSayHowManyHaveCopies() {
        #expect(CopiesAsk.of([nil, 2, 1, 4]) == .several(count: 2))
        #expect(CopiesAsk.of([1, 5]) == .several(count: 1))
        #expect(CopiesAsk.of([1, 1, nil]) == .plain)
        #expect(CopiesAsk.of([nil, nil]) == .plain)
    }

    @Test func theQuestionsWords() {
        #expect(CopiesAsk.plain.text == nil && CopiesAsk.plain.onlyShown == nil && CopiesAsk.plain.everyCopy == nil)
        let one = CopiesAsk.one(count: 4)
        #expect(one.text == S.appPhotosCopiesSingleText(count: 4))
        #expect(one.onlyShown == S.appPhotosCopiesOnlyThis)
        #expect(one.everyCopy == S.appPhotosCopiesAll(count: 4))
        let several = CopiesAsk.several(count: 2)
        #expect(several.text == S.appPhotosCopiesSeveralText(count: 2))
        #expect(several.onlyShown == S.appPhotosCopiesOnlyThese)
        #expect(several.everyCopy == S.appPhotosCopiesEvery)
    }

    @Test func theRequestCarriesTheHashAndEveryCopyOnlyWhenItHasCopies() {
        let all = DeleteCopies.request(path: "/a.jpg", hash: "h1", copies: 3, everyCopy: true)
        #expect(all.path == "/a.jpg" && all.hash == "h1" && all.hasHash)
        #expect(all.hasEveryCopy && all.everyCopy)
        // "Only this one".
        let one = DeleteCopies.request(path: "/a.jpg", hash: "h1", copies: 3, everyCopy: false)
        #expect(!one.hasEveryCopy)
        #expect(one.hash == "h1")
        // Every copy asked, but this one has none, or the device didn't
        // count them (it would delete only this path): never sent.
        #expect(!DeleteCopies.request(path: "/b.jpg", hash: "h2", copies: 1, everyCopy: true).hasEveryCopy)
        #expect(!DeleteCopies.request(path: "/b.jpg", hash: "h2", copies: nil, everyCopy: true).hasEveryCopy)
        // No hash known: none sent.
        #expect(!DeleteCopies.request(path: "/c.jpg", hash: "", copies: nil, everyCopy: false).hasHash)
    }

    private func refusal(_ code: String, _ message: String) -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        r.error = true
        r.errorCode = code
        r.errorMessage = message
        return r
    }

    @Test func outcomes() {
        var ok = Msg_RespEnvelope()
        var a = Msg_Ack()
        a.ok = true
        ok.payload = .respAck(a)
        #expect(DeleteCopies.outcome(ok) == .deleted)
        #expect(DeleteCopies.outcome(refusal("changed", "it changed")) == .refused("it changed", .changed))
        #expect(DeleteCopies.outcome(refusal("copy_upload_only", "a copy is in /Backups")) == .refused("a copy is in /Backups", .copyUploadOnly))
        #expect(DeleteCopies.outcome(refusal("upload_only", "this folder is upload only")) == .refused("this folder is upload only", .other))
        #expect(DeleteCopies.outcome(refusal("", "no such file")) == .refused("no such file", .other))
    }

    /// Issue #201: what a delete of several says about those still there.
    @Test func oneRefusalInTheDevicesWordsSeveralSummedUpPerKind() {
        let changed = "this file has changed since it was shown, so nothing was deleted: refresh and try again"
        let copy = "a copy is in the upload-only folder /Backups: no copy was deleted"
        let locked = "this folder is upload only: nothing in it can be deleted"
        // None.
        #expect(DeleteCopies.keptText([]) == nil)
        // One, whatever the kind: the device's words as it said them.
        #expect(DeleteCopies.keptText([.refused(changed, .changed)]) == S.appPhotosNoticeDeleteFailed(detail: changed))
        #expect(DeleteCopies.keptText([.refused(copy, .copyUploadOnly)]) == S.appPhotosNoticeDeleteFailed(detail: copy))
        #expect(DeleteCopies.keptText([.failed(WSClient.TimedOut())]) == S.appPhotosNoticeDeleteFailed(detail: WSClient.TimedOut().localizedDescription))
        // Two that changed: counted, not the last one's words.
        let two = DeleteCopies.keptText([.refused(changed, .changed), .refused(changed, .changed)])
        #expect(two == S.appPhotosNoticeDeleteChanged(count: 2))
        #expect(two?.contains("2") == true)
        #expect(DeleteCopies.keptText([.refused(copy, .copyUploadOnly), .refused(copy, .copyUploadOnly), .refused(copy, .copyUploadOnly)])
                == S.appPhotosNoticeDeleteCopyUploadOnly(count: 3))
        // Mixed: each kind counted, any other reason once in its own words.
        let mixed = DeleteCopies.keptText([
            .refused(changed, .changed), .refused(locked, .other), .refused(copy, .copyUploadOnly),
            .refused(locked, .other), .refused(changed, .changed),
        ])
        #expect(mixed == [S.appPhotosNoticeDeleteChanged(count: 2), S.appPhotosNoticeDeleteCopyUploadOnly(count: 1),
                          S.appPhotosNoticeDeleteFailed(detail: locked)].joined(separator: "\n"))
        // Two other reasons: each in its words.
        #expect(DeleteCopies.keptText([.refused("no such file", .other), .refused(locked, .other)])
                == [S.appPhotosNoticeDeleteFailed(detail: "no such file"), S.appPhotosNoticeDeleteFailed(detail: locked)].joined(separator: "\n"))
    }

    /// The new keys say the web's words (web.images.delete.*), in every
    /// language.
    @Test func theSummedUpWordsAreTheWebs() {
        #expect(S.appPhotosNoticeDeleteChanged(count: 1) != S.appPhotosNoticeDeleteChanged(count: 2))
        #expect(S.appPhotosNoticeDeleteChanged(count: 2).contains("2"))
        #expect(S.appPhotosNoticeDeleteCopyUploadOnly(count: 4).contains("4"))
    }

    @Test func aSignInForgetsWhatTheDeviceCouldNotDo() {
        let s = DeviceSupport()
        s.newerThan = false
        s.folderUsage = false
        s.signedIn()
        #expect(s.newerThan && s.folderUsage)
    }

    @Test func theOriginalKeepsItsNameAndGetsAnExtension() {
        #expect(PhotoGalleryVM.originalName(path: "/Phone/IMG_1.HEIC", mime: "image/heic") == "IMG_1.HEIC")
        #expect(PhotoGalleryVM.originalName(path: "/x/clip", mime: "video/quicktime") == "clip.mov")
        #expect(PhotoGalleryVM.originalName(path: "/x/pic", mime: "image/jpeg") == "pic.jpg")
        #expect(PhotoGalleryVM.originalName(path: "/x/blob", mime: "application/octet-stream") == "blob")
    }
}

/// The device, for Images' pages and deletes in the copies tests.
@MainActor
final class CopiesDevice {
    var deletes: [Msg_DelFile] = []
    var searches: [Msg_SearchPhotos] = []
    /// How a delete is answered (by its path); ok when not set.
    var refuse: [String: (code: String, message: String)] = [:]
    /// How often the months' counts were asked for.
    var bucketAsks = 0
    /// The photos: (copies; nil = not counted, as before #201).
    var copies: [Int?] = [3, nil, 1, 2]

    func file(_ i: Int) -> Msg_File {
        var f = Msg_File()
        f.path = "/photos/\(i).jpg"
        f.hash = "h\(i)"
        f.mime = "image/jpeg"
        f.created = Google_Protobuf_Timestamp(date: Date(timeIntervalSince1970: 1_780_000_000 - Double(i) * 3600))
        if let c = copies[i] { f.copies = Int32(c) }
        return f
    }

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
                var lof = Msg_ListOfFiles()
                lof.files = self.copies.indices.map(self.file)
                r.payload = .respListOfFiles(lof)
            case .reqPhotoDateBuckets:
                self.bucketAsks += 1
                r.payload = .respPhotoDateBuckets(Msg_RespPhotoDateBuckets())
            case .reqDelFile(let d):
                self.deletes.append(d)
                if let no = self.refuse[d.path] {
                    r.error = true
                    r.errorCode = no.code
                    r.errorMessage = no.message
                } else {
                    var a = Msg_Ack()
                    a.ok = true
                    r.payload = .respAck(a)
                }
            default:
                throw CancellationError()
            }
            return r
        }
        vm.libraryRequest = { _, _ in throw CancellationError() }
        vm.peopleRequest = { _, _ in throw CancellationError() }
    }
}

@MainActor
private func waitUntil(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct DeleteCopiesGalleryTests {
    private func model() async -> (PhotoGalleryVM, CopiesDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = CopiesDevice()
        device.attach(vm)
        await vm.resetAndLoadFirstPage()
        return (vm, device)
    }

    @Test func theTilesKeepTheirCopies() async {
        let (vm, _) = await model()
        #expect(vm.items.map(\.copies) == [3, nil, 1, 2])
    }

    @Test func theViewerDeletesEveryCopyOrOnlyThisOne() async {
        let (vm, device) = await model()
        vm.open(index: 0)
        #expect(vm.openCopies == 3)
        vm.deleteCurrentPhoto(everyCopy: true)
        await waitUntil { device.deletes.count == 1 }
        #expect(device.deletes[0].path == "/photos/0.jpg")
        #expect(device.deletes[0].everyCopy && device.deletes[0].hash == "h0")
        await waitUntil { vm.items.count == 3 }
        #expect(vm.items.first?.path == "/photos/1.jpg")
        // The next one, which the device didn't count: asked as before, and
        // never every copy even if asked.
        #expect(vm.openCopies == nil)
        vm.deleteCurrentPhoto(everyCopy: true)
        await waitUntil { device.deletes.count == 2 }
        #expect(!device.deletes[1].hasEveryCopy)
        #expect(device.deletes[1].hash == "h1")
        vm.closeModal()
    }

    @Test func aRefusalKeepsTheTileAndSaysTheDevicesWords() async {
        let (vm, device) = await model()
        device.refuse["/photos/3.jpg"] = ("copy_upload_only", "a copy is in the upload-only folder /Backups: no copy was deleted")
        vm.open(index: 3)
        let searches = device.searches.count
        vm.deleteCurrentPhoto(everyCopy: true)
        await waitUntil { vm.showAlert }
        #expect(vm.items.count == 4)
        #expect(vm.notice?.text == S.appPhotosNoticeDeleteFailed(detail: "a copy is in the upload-only folder /Backups: no copy was deleted"))
        // Nothing to read again.
        try? await Task.sleep(nanoseconds: 200_000_000)
        #expect(device.searches.count == searches)
        vm.closeModal()
    }

    @Test func changedKeepsTheTileAndReadsTheGridAgain() async {
        let (vm, device) = await model()
        device.refuse["/photos/0.jpg"] = ("changed", "this file has changed since it was shown")
        vm.open(index: 0)
        let searches = device.searches.count
        vm.deleteCurrentPhoto(everyCopy: false)
        await waitUntil { device.searches.count > searches }
        #expect(vm.notice?.text == S.appPhotosNoticeDeleteFailed(detail: "this file has changed since it was shown"))
        #expect(device.searches.count == searches + 1)
        await waitUntil { vm.items.count == 4 }
        #expect(vm.items.count == 4)
    }

    @Test func theSelectionAsksAboutTheCopiesOfThoseThatHaveThem() async {
        let (vm, device) = await model()
        vm.selected = ["/photos/0.jpg", "/photos/1.jpg", "/photos/3.jpg"]
        vm.askDeleteSelected()
        #expect(vm.confirmDeleteSelected)
        #expect(vm.deleteAsk == .several(count: 2))
        vm.deleteSelected(everyCopy: true)
        await waitUntil { device.deletes.count == 3 }
        let byPath = Dictionary(uniqueKeysWithValues: device.deletes.map { ($0.path, $0) })
        #expect(byPath["/photos/0.jpg"]?.everyCopy == true)
        #expect(byPath["/photos/3.jpg"]?.everyCopy == true)
        // Not counted by the device: a plain delete.
        #expect(byPath["/photos/1.jpg"]?.hasEveryCopy == false)
        #expect(device.deletes.allSatisfy { $0.hash == "h" + $0.path.dropFirst(8).prefix(1) })
        await waitUntil { vm.items.count == 1 }
        #expect(vm.items.map(\.path) == ["/photos/2.jpg"])
    }

    @Test func severalChangedAreSummedUpStayPickedAndTheGridIsReadAgain() async {
        let (vm, device) = await model()
        device.refuse["/photos/0.jpg"] = ("changed", "this file has changed since it was shown")
        device.refuse["/photos/2.jpg"] = ("changed", "this file has changed since it was shown")
        vm.selected = ["/photos/0.jpg", "/photos/1.jpg", "/photos/2.jpg"]
        let searches = device.searches.count
        let buckets = device.bucketAsks
        vm.deleteSelected()
        await waitUntil { vm.showAlert && device.searches.count > searches }
        #expect(device.deletes.count == 3)
        // Summed up, not the last one's words.
        #expect(vm.notice?.text == S.appPhotosNoticeDeleteChanged(count: 2))
        // Read again; what was refused is still picked, the one deleted not.
        #expect(device.searches.count == searches + 1)
        #expect(vm.selected == ["/photos/0.jpg", "/photos/2.jpg"])
        await waitUntil { !vm.loading && !vm.items.isEmpty }
        #expect(vm.selected == ["/photos/0.jpg", "/photos/2.jpg"])
        // One was deleted: the months' counts are asked for again.
        await waitUntil { device.bucketAsks > buckets }
        #expect(device.bucketAsks > buckets)
    }

    @Test func oneChangedAmongSeveralSaysTheDevicesWords() async {
        let (vm, device) = await model()
        device.refuse["/photos/3.jpg"] = ("changed", "this file has changed since it was shown")
        vm.selected = ["/photos/1.jpg", "/photos/3.jpg"]
        vm.deleteSelected()
        await waitUntil { vm.showAlert }
        #expect(vm.notice?.text == S.appPhotosNoticeDeleteFailed(detail: "this file has changed since it was shown"))
        await waitUntil { vm.selected == ["/photos/3.jpg"] }
        #expect(vm.selected == ["/photos/3.jpg"])
    }

    @Test func theFilesViewerClosesOnChangedAndItsListingIsReadAgain() async {
        let viewer = PhotoGalleryVM()
        var refused = Msg_RespEnvelope()
        refused.error = true
        refused.errorCode = "changed"
        refused.errorMessage = "this file has changed since it was shown"
        var sent: [Msg_DelFile] = []
        viewer.photoRequest = { payload, _ in
            guard case .reqDelFile(let d) = payload else { throw CancellationError() }
            sent.append(d)
            return refused
        }
        var relisted: [String] = []
        viewer.onDeleted = { relisted.append($0) }
        viewer.showFiles([PhotoGalleryVM.Item(id: "/Docs/a.jpg#ha", path: "/Docs/a.jpg", mime: "image/jpeg", size: 1,
                                              thumbData: nil, localURL: nil, isLocalOnly: false, hash: "ha")], startAt: 0)
        #expect(viewer.openIndex == 0)
        viewer.deleteCurrentPhoto()
        await waitUntil { viewer.showAlert }
        #expect(sent.first?.hash == "ha")
        #expect(viewer.notice?.text == S.appPhotosNoticeDeleteFailed(detail: "this file has changed since it was shown"))
        #expect(viewer.openIndex == nil)
        #expect(relisted == ["/Docs/a.jpg"])
        // The item stays: nothing was deleted.
        #expect(viewer.items.count == 1)
    }

    @Test func oneSelectedAsksAsTheViewerDoesAndNoneWithCopiesAsBefore() async {
        let (vm, _) = await model()
        vm.selected = ["/photos/3.jpg"]
        vm.askDeleteSelected()
        #expect(vm.deleteAsk == .one(count: 2))
        vm.selected = ["/photos/1.jpg", "/photos/2.jpg"]
        vm.askDeleteSelected()
        #expect(vm.deleteAsk == .plain)
    }
}
}
