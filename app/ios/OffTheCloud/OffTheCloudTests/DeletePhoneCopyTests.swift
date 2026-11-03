// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Photos
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Also delete from this phone? (DeletePhoneCopy.swift): which deleted
/// photos are asked about, which of the phone's assets, and what the
/// question says. Cases D1-D9 are Android's DeletePhoneCopyTest's.
@MainActor
struct DeletePhoneCopyTests {
    private func d(_ hash: String, copies: Int? = nil, every: Bool = false) -> DeletedOnDevice {
        DeletedOnDevice(hash: hash, copies: copies, everyCopy: every)
    }

    private func asset(_ id: String, _ hash: String, video: Bool = false) -> PhoneAsset {
        PhoneAsset(id: id, hash: hash, isVideo: video)
    }

    /// D1: only what the device no longer holds - every copy deleted, or
    /// the only one; a copy that stays is never asked about.
    @Test func onlyWhatTheDeviceNoLongerHolds() {
        // The only copy, or a device that didn't count them.
        #expect(DeletePhoneCopy.gone([d("a", copies: 1)]) == ["a"])
        #expect(DeletePhoneCopy.gone([d("a")]) == ["a"])
        // Delete only this copy: the others stay on the device.
        #expect(DeletePhoneCopy.gone([d("a", copies: 3)]).isEmpty)
        #expect(DeletePhoneCopy.gone([d("a", copies: 2)]).isEmpty)
        // Every copy.
        #expect(DeletePhoneCopy.gone([d("a", copies: 3, every: true)]) == ["a"])
        #expect(DeletePhoneCopy.gone([d("a", copies: 1, every: true)]) == ["a"])
        // No hash: nothing to match.
        #expect(DeletePhoneCopy.gone([d("", copies: 1)]).isEmpty)
        #expect(DeletePhoneCopy.gone([d("a", copies: 2), d("b"), d("c", copies: 2, every: true), d("", copies: nil)]) == ["b", "c"])
        #expect(DeletePhoneCopy.gone([]).isEmpty)
    }

    /// D2: the phone's assets are those recorded under a gone hash that
    /// are still in the library - two assets of one hash both.
    @Test func thePhonesAssetsRecordedUnderAGoneHash() {
        let recorded = ["A/L0/001": "a", "B/L0/001": "a", "C/L0/001": "b", "D/L0/001": "c"]
        let inLibrary = ["A/L0/001": false, "B/L0/001": true, "C/L0/001": false]
        let got = DeletePhoneCopy.onPhone(gone: ["a", "c"], recorded: recorded, inLibrary: inLibrary, saved: [])
        // C's content wasn't deleted; D is gone from the library.
        #expect(got == [asset("A/L0/001", "a"), asset("B/L0/001", "a", video: true)])
        #expect(DeletePhoneCopy.onPhone(gone: [], recorded: recorded, inLibrary: inLibrary, saved: []).isEmpty)
        #expect(DeletePhoneCopy.onPhone(gone: ["c"], recorded: recorded, inLibrary: inLibrary, saved: []).isEmpty)
    }

    /// D3: never what the app saved to the library itself (the viewer's
    /// Save, the wallpaper's album).
    @Test func neverWhatTheAppSavedThere() {
        let recorded = ["A/L0/001": "a", "S/L0/001": "a"]
        let inLibrary = ["A/L0/001": false, "S/L0/001": false]
        let got = DeletePhoneCopy.onPhone(gone: ["a"], recorded: recorded, inLibrary: inLibrary, saved: ["S/L0/001"])
        #expect(got == [asset("A/L0/001", "a")])
        #expect(DeletePhoneCopy.onPhone(gone: ["a"], recorded: ["S/L0/001": "a"], inLibrary: inLibrary, saved: ["S/L0/001"]).isEmpty)
    }

    /// D4: what the device says it still holds is left out; nothing left,
    /// no question.
    @Test func whatTheDeviceStillHoldsIsLeftOut() {
        let deleted = [d("a"), d("b")]
        let onPhone = [asset("A", "a"), asset("B", "b")]
        let ask = DeletePhoneCopy.ask(deleted: deleted, onPhone: onPhone, held: ["a"])
        #expect(ask?.ids == ["B"])
        #expect(ask?.found == 1)
        #expect(ask?.deleted == 2)
        #expect(DeletePhoneCopy.ask(deleted: deleted, onPhone: onPhone, held: ["a", "b"]) == nil)
        #expect(DeletePhoneCopy.ask(deleted: deleted, onPhone: [], held: []) == nil)
        // A deleted item only this copy of which went is never counted,
        // even when the phone has its content.
        #expect(DeletePhoneCopy.ask(deleted: [d("a", copies: 2)], onPhone: [asset("A", "a")], held: []) == nil)
    }

    /// D5: one photo deleted and on the phone.
    @Test func onePhoto() throws {
        let ask = try #require(DeletePhoneCopy.ask(deleted: [d("a", copies: 1)], onPhone: [asset("A", "a")], held: []))
        #expect(ask == PhoneDeleteAsk(assets: [asset("A", "a")], found: 1, deleted: 1))
        #expect(ask.title == S.appPhotosDeletePhoneTitle)
        #expect(ask.also == S.appPhotosDeletePhonePhoto)
        #expect(ask.text == S.iosPhotosDeletePhoneText(also: S.appPhotosDeletePhonePhoto))
        #expect(ask.text.contains(S.appPhotosDeletePhonePhoto))
    }

    /// D6: one video; two assets of it on the phone are one item found.
    @Test func oneVideoOnThePhoneTwice() throws {
        let ask = try #require(DeletePhoneCopy.ask(deleted: [d("v", copies: 3, every: true)],
                                                   onPhone: [asset("A", "v", video: true), asset("B", "v", video: true)], held: []))
        #expect(ask.ids == ["A", "B"])
        #expect(ask.found == 1)
        #expect(ask.title == S.appPhotosDeletePhoneTitle)
        #expect(ask.also == S.appPhotosDeletePhoneVideo)
    }

    /// D7: several deleted, one of them on the phone.
    @Test func severalDeletedOneOnThePhone() throws {
        let deleted = [d("a"), d("b"), d("c", copies: 2), d("e")]
        let ask = try #require(DeletePhoneCopy.ask(deleted: deleted, onPhone: [asset("B", "b")], held: []))
        #expect(ask.found == 1)
        #expect(ask.deleted == 4)
        #expect(ask.title == S.appPhotosDeletePhoneTitle)
        #expect(ask.also == S.appPhotosDeletePhoneSome(count: 1))
        #expect(ask.also.contains("1"))
    }

    /// D8: several deleted, several on the phone.
    @Test func severalOnThePhone() throws {
        let deleted = [d("a"), d("b", copies: 1), d("c", copies: 2, every: true)]
        let onPhone = [asset("A", "a"), asset("C", "c", video: true)]
        let ask = try #require(DeletePhoneCopy.ask(deleted: deleted, onPhone: onPhone, held: []))
        #expect(ask.found == 2)
        #expect(ask.title == S.appPhotosDeletePhoneTitleSeveral)
        #expect(ask.also == S.appPhotosDeletePhoneSome(count: 2))
        #expect(ask.also.contains("2"))
        #expect(S.appPhotosDeletePhoneSome(count: 1) != S.appPhotosDeletePhoneSome(count: 2))
        #expect(ask.text == S.iosPhotosDeletePhoneText(also: S.appPhotosDeletePhoneSome(count: 2)))
    }

    /// D9: asked only with full access, and the person declining iOS's own
    /// question is no failure.
    @Test func fullAccessOnlyAndADeclineSaysNothing() {
        #expect(DeletePhoneCopy.asks(access: .authorized))
        #expect(!DeletePhoneCopy.asks(access: .limited))
        #expect(!DeletePhoneCopy.asks(access: .denied))
        #expect(!DeletePhoneCopy.asks(access: .notDetermined))
        #expect(!DeletePhoneCopy.asks(access: .restricted))
        #expect(DeletePhoneCopy.declined(NSError(domain: PHPhotosErrorDomain, code: PHPhotosError.Code.userCancelled.rawValue)))
        #expect(!DeletePhoneCopy.declined(NSError(domain: PHPhotosErrorDomain, code: PHPhotosError.Code.accessUserDenied.rawValue)))
        #expect(!DeletePhoneCopy.declined(NSError(domain: NSCocoaErrorDomain, code: PHPhotosError.Code.userCancelled.rawValue)))
    }

    /// Files' rows: only photos and videos (or a type not said).
    @Test func filesRowsThatMayBeInTheLibrary() {
        #expect(DeletePhoneCopy.isMedia(mime: "image/heic"))
        #expect(DeletePhoneCopy.isMedia(mime: "video/quicktime"))
        #expect(DeletePhoneCopy.isMedia(mime: ""))
        #expect(!DeletePhoneCopy.isMedia(mime: "application/pdf"))
        #expect(!DeletePhoneCopy.isMedia(mime: "text/plain"))
    }

    /// The records the other way round: every asset recorded under the
    /// hashes asked for, across a relaunch.
    @Test func theRecordsFoundByHash() {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("dpc-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: dir) }
        let cache = AssetSyncCache(directory: dir)
        cache.record(localIdentifier: "A/L0/001", hash: "aa")
        cache.record(localIdentifier: "B/L0/001", hash: "bb")
        cache.record(localIdentifier: "C/L0/001", hash: "aa")
        cache.record(localIdentifier: "B/L0/001", hash: "cc")
        #expect(cache.identifiers(withHashes: ["aa"]) == ["A/L0/001": "aa", "C/L0/001": "aa"])
        #expect(cache.identifiers(withHashes: ["bb"]).isEmpty)
        #expect(cache.identifiers(withHashes: ["cc", "zz"]) == ["B/L0/001": "cc"])
        #expect(cache.identifiers(withHashes: []).isEmpty)
        #expect(AssetSyncCache(directory: dir).identifiers(withHashes: ["aa", "cc"]).count == 3)
    }
}

@MainActor
private func waitFor(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

extension ImagesNotificationTests {
/// What Images and Files hand on after a delete: only what the device
/// deleted, as it was shown (its hash, copies and every_copy).
@MainActor
@Suite(.serialized)
struct DeletePhoneCopyGalleryTests {
    private func model() async -> (PhotoGalleryVM, CopiesDevice, Told) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = CopiesDevice()
        device.attach(vm)
        let told = Told()
        vm.deletedOnDevice = { told.batches.append($0) }
        await vm.resetAndLoadFirstPage()
        return (vm, device, told)
    }

    final class Told {
        var batches: [[DeletedOnDevice]] = []
    }

    @Test func theViewerHandsOnWhatTheDeviceDeleted() async {
        let (vm, device, told) = await model()
        vm.open(index: 0)
        vm.deleteCurrentPhoto(everyCopy: true)
        await waitFor { told.batches.count == 1 }
        #expect(told.batches == [[DeletedOnDevice(hash: "h0", copies: 3, everyCopy: true)]])
        #expect(DeletePhoneCopy.gone(told.batches[0]) == ["h0"])
        // Only this copy of one with copies: handed on, never asked about.
        await waitFor { vm.items.count == 3 }
        vm.open(index: 2)
        vm.deleteCurrentPhoto()
        await waitFor { told.batches.count == 2 }
        #expect(told.batches[1] == [DeletedOnDevice(hash: "h3", copies: 2, everyCopy: false)])
        #expect(DeletePhoneCopy.gone(told.batches[1]).isEmpty)
        #expect(device.deletes.count == 2)
        vm.closeModal()
    }

    @Test func aRefusedDeleteHandsOnNothing() async {
        let (vm, device, told) = await model()
        device.refuse["/photos/2.jpg"] = ("copy_upload_only", "a copy is in the upload-only folder /Backups")
        vm.open(index: 2)
        vm.deleteCurrentPhoto()
        await waitFor { vm.showAlert }
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(told.batches.isEmpty)
        vm.closeModal()
    }

    @Test func theSelectionHandsOnOnlyWhatWasDeleted() async {
        let (vm, device, told) = await model()
        device.refuse["/photos/1.jpg"] = ("changed", "this file has changed since it was shown")
        vm.selected = ["/photos/0.jpg", "/photos/1.jpg", "/photos/2.jpg"]
        vm.deleteSelected()
        await waitFor { told.batches.count == 1 }
        let byHash = Dictionary(uniqueKeysWithValues: told.batches[0].map { ($0.hash, $0) })
        #expect(byHash.count == 2)
        #expect(byHash["h0"] == DeletedOnDevice(hash: "h0", copies: 3, everyCopy: false))
        #expect(byHash["h2"] == DeletedOnDevice(hash: "h2", copies: 1, everyCopy: false))
        // h0 keeps its other copies on the device: only h2 is asked about.
        #expect(DeletePhoneCopy.gone(told.batches[0]) == ["h2"])
    }

    @Test func filesHandsOnItsPhotosAndVideos() async {
        let vm = FilesExplorerViewModel(initialPath: "/Docs/", defaults: UserDefaults(suiteName: "dpc-\(UUID().uuidString)")!)
        vm.thumbDisk = testThumbCache()
        var told: [[DeletedOnDevice]] = []
        vm.deletedOnDevice = { told.append($0) }
        func file(_ name: String, _ mime: String, _ hash: String) -> Msg_File {
            var f = Msg_File()
            f.path = "/Docs/" + name
            f.mime = mime
            f.hash = hash
            return f
        }
        var folder = Msg_File()
        folder.path = "/Docs/Sub"
        folder.mime = "inode/directory"
        let files = [file("a.jpg", "image/jpeg", "ha"), file("b.pdf", "application/pdf", "hb"),
                     file("c.mov", "video/quicktime", "hc"), file("d.jpg", "image/jpeg", "hd"), folder]
        vm.request = { payload in
            var r = Msg_RespEnvelope()
            switch payload {
            case .reqListFiles:
                var lof = Msg_ListOfFiles()
                lof.files = files
                r.payload = .respListOfFiles(lof)
            case .reqDelFile(let del):
                if del.path == "/Docs/d.jpg" {
                    r.error = true
                    r.errorCode = "changed"
                    r.errorMessage = "this file has changed since it was shown"
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
        await vm.load()
        vm.selected = ["/Docs/a.jpg", "/Docs/b.pdf", "/Docs/c.mov", "/Docs/d.jpg", "/Docs/Sub"]
        await vm.deleteSelected()
        #expect(told.count == 1)
        #expect(Set(told.first?.map(\.hash) ?? []) == ["ha", "hc"])
        #expect(told.first?.allSatisfy { $0.copies == nil && !$0.everyCopy } == true)
    }
}
}
