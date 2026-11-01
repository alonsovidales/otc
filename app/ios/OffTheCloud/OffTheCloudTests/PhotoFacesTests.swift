// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
import UIKit
@testable import OffTheCloud

/// A photo's faces in the viewer's Info (PhotoFaces.swift): what the
/// device's answer comes to, what is asked for and when, a face's ×
/// (RemoveFromPerson) and a face tapped. Android's does the same.
private func photoFace(_ person: String, _ name: String, thumbnail: Data = Data()) -> Msg_PhotoFace {
    var f = Msg_PhotoFace()
    f.personID = person
    f.name = name
    f.thumbnail = thumbnail
    f.w = 40
    f.h = 40
    return f
}

private func facesAnswer(_ faces: [Msg_PhotoFace], on: Bool = true, hash: String = "h0") -> Msg_RespEnvelope {
    var pf = Msg_PhotoFaces()
    pf.faces = faces
    pf.faceRecognition = on
    pf.hash = hash
    var r = Msg_RespEnvelope()
    r.payload = .respPhotoFaces(pf)
    return r
}

private func refusal(_ code: String) -> Msg_RespEnvelope {
    var r = Msg_RespEnvelope()
    r.error = true
    r.errorCode = code
    r.errorMessage = "no"
    return r
}

/// A small JPEG, as the device sends a face's crop.
private func crop() -> Data {
    UIGraphicsImageRenderer(size: CGSize(width: 8, height: 8)).jpegData(withCompressionQuality: 0.8) { ctx in
        UIColor.orange.setFill()
        ctx.fill(CGRect(x: 0, y: 0, width: 8, height: 8))
    }
}

struct PhotoFacesStateTests {
    @Test func theFacesLeftToRightWithTheirNames() throws {
        let state = PhotoFacesState.of(facesAnswer([photoFace("p1", "  Ana  Lu ", thumbnail: crop()), photoFace("p2", "")]), path: "/p/0.jpg")
        guard case .faces(let rows, let path, let hash) = state else {
            Issue.record("no faces")
            return
        }
        #expect(path == "/p/0.jpg")
        #expect(hash == "h0")
        #expect(rows.map(\.id) == [0, 1])
        #expect(rows.map(\.person) == ["p1", "p2"])
        #expect(rows.map(\.name) == ["Ana Lu", ""])
        #expect(rows.map(\.shownName) == ["Ana Lu", "Unnamed"])
        // A blank circle for a face sent without its crop.
        #expect(rows[0].image != nil)
        #expect(rows[1].image == nil)
        // Not this person's question, for one photo.
        #expect(rows[0].removeTitle == "Remove this photo from \u{2068}Ana Lu\u{2069}?")
        #expect(rows[1].removeTitle == "Remove this photo from this person?")
        #expect(state.rows.count == 2)
        #expect(state.path == "/p/0.jpg")
    }

    @Test func nothingShowsWithoutFaces() {
        // Face recognition off.
        #expect(PhotoFacesState.of(facesAnswer([photoFace("p1", "Ana")], on: false), path: "/a.jpg").rows.isEmpty)
        // No face found.
        #expect(PhotoFacesState.of(facesAnswer([]), path: "/a.jpg").path == nil)
        // A device from before GetPhotoFaces, or any other refusal.
        #expect(PhotoFacesState.of(refusal("unknown_payload"), path: "/a.jpg").path == nil)
        #expect(PhotoFacesState.of(refusal(""), path: "/a.jpg").path == nil)
        // Something else answered, or no hash to take a face off with.
        #expect(PhotoFacesState.of(Msg_RespEnvelope(), path: "/a.jpg").path == nil)
        #expect(PhotoFacesState.of(facesAnswer([photoFace("p1", "Ana")], hash: ""), path: "/a.jpg").path == nil)
        // A face matched to no one isn't listed.
        #expect(PhotoFacesState.of(facesAnswer([photoFace("", "")]), path: "/a.jpg").path == nil)
        #expect(PhotoFacesState.of(facesAnswer([photoFace("", ""), photoFace("p1", "Ana")]), path: "/a.jpg").rows.map(\.id) == [0])
    }

    @Test func askedForPhotosTheDeviceHas() {
        func item(_ mime: String, local: Bool = false) -> PhotoGalleryVM.Item {
            PhotoGalleryVM.Item(id: "x", path: "/a", mime: mime, size: 1, thumbData: nil,
                                localURL: local ? URL(fileURLWithPath: "/tmp/a.jpg") : nil, isLocalOnly: local)
        }
        #expect(PhotoFacesState.wanted(item("image/jpeg")))
        #expect(PhotoFacesState.wanted(item("image/heic")))
        #expect(!PhotoFacesState.wanted(item("video/mp4")))
        #expect(!PhotoFacesState.wanted(item("image/jpeg", local: true)))
    }

    @Test func aPersonTakenOffLosesEveryFaceOfTheirs() {
        let state = PhotoFacesState.of(facesAnswer([photoFace("p1", "Ana"), photoFace("p2", "Leo"), photoFace("p1", "Ana")]), path: "/a.jpg")
        let left = state.without("p1")
        #expect(left.rows.map(\.person) == ["p2"])
        #expect(left.path == "/a.jpg")
        #expect(left.without("p2").path == nil)
        #expect(PhotoFacesState.hidden.without("p1").path == nil)
    }
}

@MainActor
private func waitFor(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

/// The device for Info's faces, over PersonDevice (Ana's photos): each
/// photo's faces by path, GetPhotoFaces asked, RemoveFromPerson taking
/// the person's faces off that content.
@MainActor
private final class FacesDevice {
    let person = PersonDevice()
    /// Each photo's faces and its content hash, by path.
    var faces: [String: (hash: String, faces: [Msg_PhotoFace])] = [
        "/p/0.jpg": ("h0", [photoFace("p1", "Ana", thumbnail: crop()), photoFace("p2", "Leo")]),
        "/p/1.jpg": ("h1", [photoFace("p1", "Ana")]),
        "/p/2.jpg": ("h2", [photoFace("p3", ""), photoFace("p1", "Ana")]),
    ]
    var asked: [String] = []
    var removes: [Msg_RemoveFromPerson] = []
    /// How GetPhotoFaces is answered instead, when set.
    var facesRefusal: String?
    var faceRecognition = true
    /// How long GetPhotoFaces takes, by path.
    var slow: [String: Double] = [:]
    /// RemoveFromPerson refused, when set.
    var removeRefusal: String?

    func attach(_ vm: PhotoGalleryVM) {
        person.attach(vm)
        let base = vm.peopleRequest
        vm.peopleRequest = { [weak self] payload, kind in
            guard let self else { throw CancellationError() }
            switch payload {
            case .reqGetPhotoFaces(let g):
                self.asked.append(g.path)
                if let s = self.slow[g.path] { try? await Task.sleep(nanoseconds: UInt64(s * 1_000_000_000)) }
                if let code = self.facesRefusal { return refusal(code) }
                guard let p = self.faces[g.path] else { return facesAnswer([], on: self.faceRecognition) }
                return facesAnswer(p.faces, on: self.faceRecognition, hash: p.hash)
            case .reqRemoveFromPerson(let rm):
                self.removes.append(rm)
                if let code = self.removeRefusal { return refusal(code) }
                for (path, p) in self.faces where rm.hashes.contains(p.hash) {
                    self.faces[path] = (p.hash, p.faces.filter { $0.personID != rm.personID })
                }
                // Ana's: off her photos too (PersonDevice keeps her listing).
                if rm.personID == "p1" { return try await base(payload, kind) }
                var a = Msg_Ack()
                a.ok = true
                var r = Msg_RespEnvelope()
                r.payload = .respAck(a)
                return r
            default:
                return try await base(payload, kind)
            }
        }
    }
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct PhotoFacesTests {
    private func person(_ id: String, _ name: String) -> Msg_Person {
        var p = Msg_Person()
        p.id = id
        p.name = name
        return p
    }

    /// Images on Ana's photos (a face opened from People), or on everyone.
    private func model(filter: Bool = true) async -> (PhotoGalleryVM, FacesDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = FacesDevice()
        device.attach(vm)
        vm.allPeople = [person("p1", "Ana"), person("p2", "Leo"), person("p3", "")]
        vm.selectedPeople = filter ? ["p1"] : []
        await vm.resetAndLoadFirstPage()
        return (vm, device)
    }

    /// Info opened on the photo at `index`, its faces in.
    private func openInfo(_ vm: PhotoGalleryVM, at index: Int) async {
        vm.open(index: index)
        vm.openInfo()
        await waitFor { !vm.infoFaces.rows.isEmpty }
    }

    @Test func infoAsksForTheOpenPhotosFaces() async {
        let (vm, device) = await model()
        #expect(vm.items.map(\.path) == ["/p/0.jpg", "/p/1.jpg", "/p/2.jpg"])
        // Nothing asked before Info is opened.
        vm.open(index: 2)
        #expect(device.asked.isEmpty)
        vm.openInfo()
        #expect(vm.infoOpen)
        await waitFor { !vm.infoFaces.rows.isEmpty }
        #expect(device.asked == ["/p/2.jpg"])
        #expect(vm.infoFaces.rows.map(\.shownName) == ["Unnamed", "Ana"])
        #expect(vm.infoFaces.path == "/p/2.jpg")
        // Another photo: Info closes with its faces.
        vm.open(index: 1)
        #expect(!vm.infoOpen)
        #expect(vm.infoFaces.path == nil)
        await openInfo(vm, at: 1)
        #expect(device.asked == ["/p/2.jpg", "/p/1.jpg"])
        #expect(vm.infoFaces.rows.map(\.person) == ["p1"])
    }

    @Test func noneWhenTheDeviceHasNone() async {
        let (vm, device) = await model()
        // Face recognition off.
        device.faceRecognition = false
        vm.open(index: 0)
        vm.openInfo()
        await waitFor { device.asked.count == 1 }
        await waitFor(0.2) { false }
        #expect(vm.infoFaces.path == nil)
        // A device from before GetPhotoFaces: quietly nothing.
        device.faceRecognition = true
        device.facesRefusal = "unknown_payload"
        vm.open(index: 1)
        vm.openInfo()
        await waitFor { device.asked.count == 2 }
        await waitFor(0.2) { false }
        #expect(vm.infoFaces.path == nil)
        #expect(!vm.showAlert)
        // A photo with no face.
        device.facesRefusal = nil
        device.faces["/p/1.jpg"] = ("h1", [])
        vm.open(index: 1)
        vm.openInfo()
        await waitFor { device.asked.count == 3 }
        await waitFor(0.2) { false }
        #expect(vm.infoFaces.path == nil)
    }

    @Test func notAskedForAVideo() async {
        let vm = PhotoGalleryVM()
        let device = FacesDevice()
        device.attach(vm)
        vm.showFiles([PhotoGalleryVM.Item(id: "v", path: "/v.mp4", mime: "video/mp4", size: 1, thumbData: nil,
                                          localURL: nil, isLocalOnly: false, hash: "hv")], startAt: 0)
        vm.openInfo()
        await waitFor(0.3) { false }
        #expect(device.asked.isEmpty)
        #expect(vm.infoFaces.path == nil)
    }

    /// An answer that comes once the viewer is on another photo is dropped.
    @Test func aLateAnswerForAnotherPhotoIsDropped() async {
        let (vm, device) = await model()
        device.slow["/p/0.jpg"] = 0.3
        vm.open(index: 0)
        vm.openInfo()
        await waitFor { device.asked.count == 1 }
        vm.open(index: 1)
        await waitFor(0.6) { false }
        #expect(vm.infoFaces.path == nil)
    }

    /// Someone else's face, among Ana's photos: the photo stays, the face
    /// leaves the list, the list is asked for again, and it is said over
    /// Info.
    @Test func someoneElsesFaceLeavesTheListAndThePhotoStays() async throws {
        let (vm, device) = await model()
        await openInfo(vm, at: 0)
        let leo = try #require(vm.infoFaces.rows.first { $0.person == "p2" })
        let asks = device.asked.count
        await vm.removeFace(leo)
        #expect(device.removes.count == 1)
        #expect(device.removes.first?.personID == "p2")
        #expect(device.removes.first?.hashes == ["h0"])
        #expect(device.removes.first?.toPersonID == "")
        #expect(vm.items.count == 3)
        #expect(vm.openPath == "/p/0.jpg")
        #expect(vm.infoOpen)
        #expect(vm.infoFaces.rows.map(\.person) == ["p1"])
        #expect(vm.faceRemoving == nil)
        #expect(vm.notice?.text == S.appPhotosPersonRemoved(count: 1, name: "Leo"))
        #expect(vm.alertShownOverInfo)
        #expect(!vm.alertShown(overViewer: true) && !vm.alertShown(overViewer: false))
        await waitFor { device.asked.count > asks }
        #expect(device.asked.last == "/p/0.jpg")
        #expect(vm.infoFaces.rows.map(\.person) == ["p1"])
        // Info closed: what it said shows over the viewer.
        vm.infoOpen = false
        #expect(vm.alertShown(overViewer: true) && !vm.alertShownOverInfo)
    }

    /// Ana's own face among her photos: the photo leaves her grid as after
    /// Not this person, the viewer goes on to the next one, Info closes,
    /// and it is said over the viewer.
    @Test func thePersonsOwnFaceTakesThePhotoOffTheirGrid() async throws {
        let (vm, device) = await model()
        await openInfo(vm, at: 0)
        let ana = try #require(vm.infoFaces.rows.first { $0.person == "p1" })
        await vm.removeFace(ana)
        #expect(device.removes.first?.personID == "p1")
        #expect(device.removes.first?.hashes == ["h0"])
        #expect(vm.items.map(\.path) == ["/p/1.jpg", "/p/2.jpg"])
        #expect(vm.openPath == "/p/1.jpg")
        #expect(!vm.infoOpen)
        #expect(vm.infoFaces.path == nil)
        #expect(vm.notice?.text == S.appPhotosPersonRemoved(count: 1, name: "Ana"))
        #expect(vm.alertShown(overViewer: true) && !vm.alertShownOverInfo)
    }

    /// Outside a person's photos nothing leaves the grid, whoever's face.
    @Test func overTheWholeLibraryThePhotoStays() async throws {
        let (vm, device) = await model(filter: false)
        await openInfo(vm, at: 0)
        let ana = try #require(vm.infoFaces.rows.first { $0.person == "p1" })
        await vm.removeFace(ana)
        #expect(device.removes.first?.personID == "p1")
        #expect(vm.items.count == 3)
        #expect(vm.infoOpen)
        #expect(vm.infoFaces.rows.map(\.person) == ["p2"])
    }

    @Test func anUnnamedPersonIsThisPerson() async throws {
        let (vm, device) = await model()
        await openInfo(vm, at: 2)
        let unnamed = try #require(vm.infoFaces.rows.first { $0.person == "p3" })
        await vm.removeFace(unnamed)
        #expect(device.removes.first?.personID == "p3")
        #expect(device.removes.first?.hashes == ["h2"])
        #expect(vm.items.count == 3)
        #expect(vm.notice?.text == S.appPhotosPersonRemovedUnnamed(count: 1))
    }

    /// Refused or unanswered: said over Info, the face and the photo stay.
    @Test func aRefusalIsSaidOverInfo() async throws {
        let (vm, device) = await model()
        await openInfo(vm, at: 0)
        device.removeRefusal = "demo"
        let ana = try #require(vm.infoFaces.rows.first { $0.person == "p1" })
        await vm.removeFace(ana)
        #expect(device.removes.count == 1)
        #expect(vm.items.count == 3)
        #expect(vm.infoOpen)
        #expect(vm.infoFaces.rows.count == 2)
        #expect(vm.faceRemoving == nil)
        #expect(vm.notice?.text == S.appPhotosPersonRemoveFailed)
        #expect(vm.alertShownOverInfo)
    }

    @Test func oneAtATime() async throws {
        let (vm, device) = await model()
        await openInfo(vm, at: 0)
        let leo = try #require(vm.infoFaces.rows.first { $0.person == "p2" })
        vm.faceRemoving = 0
        await vm.removeFace(leo)
        #expect(device.removes.isEmpty)
        vm.faceRemoving = nil
    }

    /// A face tapped: the viewer closes and Images shows that person's
    /// photos, as a face opened from People.
    @Test func aFaceTappedOpensThatPersonsPhotos() async {
        let (vm, device) = await model()
        await openInfo(vm, at: 0)
        vm.showFacePerson("p2")
        #expect(vm.openIndex == nil)
        #expect(!vm.infoOpen)
        #expect(vm.selectedPeople == ["p2"])
        await waitFor { device.person.searches.last?.personIds == ["p2"] }
        #expect(device.person.searches.last?.personIds == ["p2"])
    }

    /// From a viewer of a list (Files', the map's, a memory's) the person
    /// is asked of Images by way of PersonNav.
    @Test func fromAViewerOfAListImagesIsAsked() async {
        let vm = PhotoGalleryVM()
        let device = FacesDevice()
        device.attach(vm)
        vm.showFiles([PhotoGalleryVM.Item(id: "a", path: "/p/0.jpg", mime: "image/jpeg", size: 1, thumbData: nil,
                                          localURL: nil, isLocalOnly: false, hash: "h0")], startAt: 0)
        vm.openInfo()
        await waitFor { !vm.infoFaces.rows.isEmpty }
        #expect(vm.infoFaces.rows.map(\.person) == ["p1", "p2"])
        let sent = PersonNav.shared.sent
        vm.showFacePerson("p2")
        #expect(vm.openIndex == nil)
        #expect(vm.selectedPeople.isEmpty)
        #expect(PersonNav.shared.sent == sent + 1)
        #expect(PersonNav.shared.pending == "p2")
        #expect(PersonNav.shared.take() == "p2")
        #expect(PersonNav.shared.take() == nil)
    }

    /// In a viewer of a list, a face's × never takes the photo out of it.
    @Test func inAViewerOfAListThePhotoStays() async throws {
        let vm = PhotoGalleryVM()
        let device = FacesDevice()
        device.attach(vm)
        vm.showFiles([PhotoGalleryVM.Item(id: "a", path: "/p/0.jpg", mime: "image/jpeg", size: 1, thumbData: nil,
                                          localURL: nil, isLocalOnly: false, hash: "h0")], startAt: 0)
        vm.openInfo()
        await waitFor { !vm.infoFaces.rows.isEmpty }
        let ana = try #require(vm.infoFaces.rows.first { $0.person == "p1" })
        await vm.removeFace(ana)
        #expect(device.removes.first?.personID == "p1")
        #expect(vm.items.count == 1)
        #expect(vm.openPath == "/p/0.jpg")
        #expect(vm.infoFaces.rows.map(\.person) == ["p2"])
        #expect(vm.notice?.text == S.appPhotosPersonRemoved(count: 1, name: "Ana"))
    }
}
}
