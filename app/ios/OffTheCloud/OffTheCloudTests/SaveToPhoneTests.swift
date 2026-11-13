// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Photos
import Testing
@testable import OffTheCloud

/// Save to Photos (SaveToPhone.swift): which of the picked photos the
/// selection bar's Download saves, in what order, and the note once it is
/// done. The same rules as Android's SaveToPhoneTest.
@MainActor
struct SaveToPhoneTests {
    private func item(_ name: String, _ hash: String = "") -> PhotoGalleryVM.Item {
        PhotoGalleryVM.Item(id: name, path: "/photos/\(name).jpg", mime: "image/jpeg", size: 1, thumbData: nil,
                            localURL: nil, isLocalOnly: false, hash: hash)
    }

    private func paths(_ plan: SaveToPhone.Plan) -> [String] {
        plan.save.map(\.path)
    }

    /// A saved photo is known by its content; without a hash, by its path.
    @Test func whatASavedPhotoIsKnownBy() {
        #expect(SaveToPhone.key(hash: "h1", path: "/photos/a.jpg") == "h1")
        #expect(SaveToPhone.key(hash: "", path: "/photos/a.jpg") == "/photos/a.jpg")
        // An edit: the same path, new content - not the one saved.
        #expect(SaveToPhone.key(hash: "h2", path: "/photos/a.jpg") != SaveToPhone.key(hash: "h1", path: "/photos/a.jpg"))
    }

    /// Without a hash nothing can be matched: always saved, even when its
    /// path was saved from here or "" is on the phone.
    @Test func noHashIsAlwaysSaved() {
        let plan = SaveToPhone.plan([item("a"), item("b")], onPhone: [""],
                                    saved: [SaveToPhone.key(hash: "", path: "/photos/a.jpg")])
        #expect(paths(plan) == ["/photos/a.jpg", "/photos/b.jpg"])
        #expect(plan.already == 0)
    }

    /// What this phone's library holds is left out, and counted.
    @Test func whatThePhoneHoldsIsLeftOut() {
        let plan = SaveToPhone.plan([item("a", "h1"), item("b", "h2"), item("c", "h3")], onPhone: ["h2", "h9"], saved: [])
        #expect(paths(plan) == ["/photos/a.jpg", "/photos/c.jpg"])
        #expect(plan.already == 1)
        let all = SaveToPhone.plan([item("a", "h1"), item("b", "h2")], onPhone: ["h1", "h2"], saved: [])
        #expect(all.save.isEmpty)
        #expect(all.already == 2)
    }

    /// What was saved from here already (the viewer's Save, an earlier
    /// Save to Photos) is left out, and counted.
    @Test func whatWasSavedFromHereIsLeftOut() {
        let saved: Set<String> = [SaveToPhone.key(hash: "h1", path: "/photos/a.jpg")]
        let plan = SaveToPhone.plan([item("a", "h1"), item("b", "h2")], onPhone: [], saved: saved)
        #expect(paths(plan) == ["/photos/b.jpg"])
        #expect(plan.already == 1)
        // The same content at another path is the same photo.
        let elsewhere = SaveToPhone.plan([item("copy", "h1")], onPhone: [], saved: saved)
        #expect(elsewhere.save.isEmpty)
        #expect(elsewhere.already == 1)
        // Both: counted once.
        let both = SaveToPhone.plan([item("a", "h1")], onPhone: ["h1"], saved: saved)
        #expect(both.already == 1)
    }

    /// The same content picked twice (copies at other paths) is saved
    /// once, and counted once when the phone holds it.
    @Test func theSameContentTwiceIsSavedOnce() {
        let plan = SaveToPhone.plan([item("a", "h1"), item("b", "h1"), item("c", "h2"), item("d", "h1")], onPhone: [], saved: [])
        #expect(paths(plan) == ["/photos/a.jpg", "/photos/c.jpg"])
        #expect(plan.already == 0)
        let there = SaveToPhone.plan([item("a", "h1"), item("b", "h1")], onPhone: ["h1"], saved: [])
        #expect(there.save.isEmpty)
        #expect(there.already == 1)
    }

    /// Saved in the order picked (the grid's).
    @Test func theOrderIsKept() {
        let picked = [item("e", "h5"), item("x"), item("b", "h2"), item("d", "h4"), item("y"), item("a", "h1")]
        let plan = SaveToPhone.plan(picked, onPhone: ["h4"], saved: [])
        #expect(paths(plan) == ["/photos/e.jpg", "/photos/x.jpg", "/photos/b.jpg", "/photos/y.jpg", "/photos/a.jpg"])
        #expect(plan.already == 1)
        #expect(SaveToPhone.plan([], onPhone: [], saved: []) == SaveToPhone.Plan(save: [], already: 0))
    }

    /// The note: how many were saved, then how many were there already;
    /// those alone when none were saved.
    @Test func theNotesThreeForms() {
        #expect(SaveToPhone.note(saved: 3, already: 0) == S.iosPhotosSavedToPhotos(count: 3))
        #expect(SaveToPhone.note(saved: 2, already: 1)
                == S.iosPhotosSavedToPhotos(count: 2) + " " + S.appPhotosDownloadAlready(count: 1))
        #expect(SaveToPhone.note(saved: 0, already: 4) == S.appPhotosDownloadAlready(count: 4))
        #expect(SaveToPhone.note(saved: 0, already: 0) == nil)
        #expect(S.iosPhotosSavedToPhotos(count: 3).contains("3"))
        #expect(S.appPhotosDownloadAlready(count: 4).contains("4"))
        #expect(S.appPhotosDownloadAlready(count: 1) != S.appPhotosDownloadAlready(count: 2))
    }

    /// Some not saved: how many of how many, and the first one's reason -
    /// a format Photos refuses by the file's name, not Photos' error number.
    @Test func someNotSavedSaysHowManyAndWhy() {
        let why = FileDownload.Refused(message: "no such file")
        #expect(GalleryNotice.someNotSaved(2, 5, why, "a.jpg").text
                == S.appPhotosDownloadFailed(count: 2, total: 5, detail: "no such file"))
        #expect(GalleryNotice.someNotSaved(2, 5, why, "a.jpg").text.contains("no such file"))
        let refused = NSError(domain: PHPhotosErrorDomain, code: PHPhotosError.Code.invalidResource.rawValue)
        #expect(SaveToPhone.refusedFormat(refused))
        #expect(!SaveToPhone.refusedFormat(why))
        #expect(GalleryNotice.someNotSaved(1, 3, refused, "clip.mkv").text
                == S.appPhotosDownloadFailed(count: 1, total: 3, detail: S.appPhotosDownloadUnsupported(name: "clip.mkv")))
    }

    /// Cancel stops the download under way before its next piece, and its
    /// part file goes.
    @Test func aCancelledDownloadStops() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("stp-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let dest = dir.appendingPathComponent("a.jpg")
        let download = Task { try await FileDownload.download(path: "/photos/a.jpg", mime: "image/jpeg", to: dest, original: true) }
        download.cancel()
        await #expect(throws: CancellationError.self) { try await download.value }
        #expect(try FileManager.default.contentsOfDirectory(atPath: dir.path).isEmpty)
    }
}

extension ImagesNotificationTests {
/// The selection's Save to Photos takes the picked photos in the grid's
/// order.
@MainActor
@Suite(.serialized)
struct SaveToPhoneGalleryTests {
    @Test func thePickedInTheGridsOrder() async {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = CopiesDevice()
        device.attach(vm)
        await vm.resetAndLoadFirstPage()
        vm.selected = ["/photos/3.jpg", "/photos/0.jpg", "/photos/2.jpg"]
        #expect(vm.pickedInOrder().map(\.path) == ["/photos/0.jpg", "/photos/2.jpg", "/photos/3.jpg"])
        #expect(vm.pickedInOrder().map(\.hash) == ["h0", "h2", "h3"])
        // Nothing saved from here yet: the viewer's Save is its arrow.
        vm.open(index: 0)
        #expect(!vm.openSaved)
        vm.closeModal()
    }
}
}
