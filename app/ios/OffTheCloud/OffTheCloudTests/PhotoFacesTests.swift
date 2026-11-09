// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
import UIKit
@testable import OffTheCloud

/// A photo's faces in the viewer's Info (PhotoFaces.swift): what the
/// device's answer comes to, what is asked for and when, a face's ×
/// (RemoveFromPerson), a face tapped, and Add a person (AddToPerson) with
/// the rows it adds. Android's does the same.
private func photoFace(_ person: String, _ name: String, thumbnail: Data = Data(), byHand: Bool = false) -> Msg_PhotoFace {
    var f = Msg_PhotoFace()
    f.personID = person
    f.name = name
    f.thumbnail = thumbnail
    // Someone added by hand has no box in the photo.
    f.w = byHand ? 0 : 40
    f.h = byHand ? 0 : 40
    f.byHand = byHand
    return f
}

private func facesAnswer(_ faces: [Msg_PhotoFace], on: Bool = true, hash: String = "h0", adds: Bool = false) -> Msg_RespEnvelope {
    var pf = Msg_PhotoFaces()
    pf.faces = faces
    pf.faceRecognition = on
    pf.hash = hash
    pf.addsPeople = adds
    var r = Msg_RespEnvelope()
    r.payload = .respPhotoFaces(pf)
    return r
}

private func someone(_ id: String, _ name: String, _ faces: Int32 = 1) -> Msg_Person {
    var p = Msg_Person()
    p.id = id
    p.name = name
    p.faceCount = faces
    return p
}

private func ack(ok: Bool = true, code: String = "", words: String = "") -> Msg_RespEnvelope {
    var a = Msg_Ack()
    a.ok = ok
    a.code = code
    a.errorMsg = words
    var r = Msg_RespEnvelope()
    r.payload = .respAck(a)
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
        guard case .faces(let rows, let path, let hash, let adds) = state else {
            Issue.record("no faces")
            return
        }
        #expect(path == "/p/0.jpg")
        #expect(hash == "h0")
        // A device from before AddToPerson: no Add a person.
        #expect(!adds)
        #expect(rows.allSatisfy { !$0.byHand })
        #expect(rows.map(\.removeLabel) == ["Not this person", "Not this person"])
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

    /// Videos too, for the people added to them by hand: a device from
    /// before AddToPerson answers one with no faces.
    @Test func askedForPhotosAndVideosTheDeviceHas() {
        func item(_ mime: String, local: Bool = false) -> PhotoGalleryVM.Item {
            PhotoGalleryVM.Item(id: "x", path: "/a", mime: mime, size: 1, thumbData: nil,
                                localURL: local ? URL(fileURLWithPath: "/tmp/a.jpg") : nil, isLocalOnly: local)
        }
        #expect(PhotoFacesState.wanted(item("image/jpeg")))
        #expect(PhotoFacesState.wanted(item("image/heic")))
        #expect(PhotoFacesState.wanted(item("video/mp4")))
        #expect(PhotoFacesState.wanted(item("video/quicktime")))
        #expect(!PhotoFacesState.wanted(item("image/jpeg", local: true)))
        #expect(!PhotoFacesState.wanted(item("video/mp4", local: true)))
    }

    @Test func aPersonTakenOffLosesEveryFaceOfTheirs() {
        let state = PhotoFacesState.of(facesAnswer([photoFace("p1", "Ana"), photoFace("p2", "Leo"), photoFace("p1", "Ana")]), path: "/a.jpg")
        let left = state.without("p1")
        #expect(left.rows.map(\.person) == ["p2"])
        #expect(left.path == "/a.jpg")
        #expect(left.without("p2").path == nil)
        #expect(PhotoFacesState.hidden.without("p1").path == nil)
        // With Add a person People stays, empty but for it.
        let adding = PhotoFacesState.of(facesAnswer([photoFace("p2", "Leo")], adds: true), path: "/a.jpg").without("p2")
        #expect(adding.shown && adding.adds && adding.rows.isEmpty)
        #expect(adding.hash == "h0")
    }

    /// The faces left to right, then the people added by hand in the order
    /// they were added: their rows are a face's, with their People face as
    /// the crop (none when People has none) and their own ×.
    @Test func thePeopleAddedByHandComeAfterTheFaces() {
        let state = PhotoFacesState.of(facesAnswer([
            photoFace("p1", "Ana"), photoFace("p2", " Leo ", thumbnail: crop(), byHand: true), photoFace("p3", "", byHand: true),
        ], adds: true), path: "/p/0.jpg")
        #expect(state.rows.map(\.id) == [0, 1, 2])
        #expect(state.rows.map(\.person) == ["p1", "p2", "p3"])
        #expect(state.rows.map(\.byHand) == [false, true, true])
        #expect(state.rows.map(\.shownName) == ["Ana", "Leo", "Unnamed"])
        #expect(state.rows[1].image != nil)
        #expect(state.rows[2].image == nil)
        // "Remove Ana", as a person's chip in the search says it.
        #expect(state.rows.map(\.removeLabel) == ["Not this person", "Remove \u{2068}Leo\u{2069}", "Remove this person"])
        #expect(state.people == ["p1", "p2", "p3"])
        #expect(state.adds)
        #expect(state.without("p2").rows.map(\.person) == ["p1", "p3"])
    }

    /// People shows with no one in it only for Add a person: on a device
    /// that takes it, with face recognition on and the content's hash.
    @Test func addAPersonWhereTheDeviceTakesIt() {
        let empty = PhotoFacesState.of(facesAnswer([], hash: "hv", adds: true), path: "/v.mp4")
        #expect(empty.shown)
        #expect(empty.adds)
        #expect(empty.rows.isEmpty)
        #expect(empty.path == "/v.mp4")
        #expect(empty.hash == "hv")
        #expect(empty.people.isEmpty)
        // A device before it: nothing, as before; with faces, no Add.
        #expect(!PhotoFacesState.of(facesAnswer([]), path: "/v.mp4").shown)
        let old = PhotoFacesState.of(facesAnswer([photoFace("p1", "Ana")]), path: "/a.jpg")
        #expect(old.shown && !old.adds)
        // Face recognition off, or no hash to add anyone with.
        #expect(!PhotoFacesState.of(facesAnswer([], on: false, adds: true), path: "/v.mp4").shown)
        #expect(!PhotoFacesState.of(facesAnswer([photoFace("p1", "Ana")], on: false, adds: true), path: "/a.jpg").shown)
        #expect(!PhotoFacesState.of(facesAnswer([], hash: "", adds: true), path: "/v.mp4").shown)
        #expect(!PhotoFacesState.hidden.adds && !PhotoFacesState.hidden.shown && PhotoFacesState.hidden.hash == nil)
    }

    /// Whom Add a person offers: People's list in its order (named first,
    /// the most photos first) without those in it already.
    @Test func addAPersonOffersEveryoneNotInItAlready() {
        let all = [someone("u1", "", 30), someone("p1", "Ana", 20), someone("p2", "Leo", 9), someone("p4", "Bea", 2)]
        let state = PhotoFacesState.of(facesAnswer([photoFace("p1", "Ana"), photoFace("p2", "Leo", byHand: true)], adds: true), path: "/a.jpg")
        #expect(state.addChoices(all).map(\.id) == ["p4", "u1"])
        #expect(state.addList(all, loaded: true, filter: "") == .people([all[3], all[0]]))
        // Nobody in it yet: everyone.
        let none = PhotoFacesState.of(facesAnswer([], adds: true), path: "/v.mp4")
        #expect(none.addChoices(all).map(\.id) == ["p1", "p2", "p4", "u1"])
        // Everyone in it already.
        let full = PhotoFacesState.of(facesAnswer(all.map { photoFace($0.id, $0.name) }, adds: true), path: "/a.jpg")
        #expect(full.addList(all, loaded: true, filter: "") == .everyoneIn)
        // No one found yet; the list still on its way the first time
        // (the one asked for before stands in meanwhile).
        #expect(none.addList([], loaded: true, filter: "") == .noOne)
        #expect(none.addList([], loaded: false, filter: "") == .loading)
        #expect(none.addList(all, loaded: false, filter: "") == .people(PeopleOrder.shown(all)))
    }

    /// Past 8 people to offer the list has a field: names holding what is
    /// typed, case and accents aside; nothing typed counts below that.
    @Test func addAPersonFindsANameInALongList() {
        let names = ["Ána", "Leo", "Bea", "Carla", "Dani", "Eva", "Fede", "Gala", "Hugo"]
        let all = names.enumerated().map { someone("p\($0.offset)", $0.element, Int32(20 - $0.offset)) } + [someone("u", "", 1)]
        let none = PhotoFacesState.of(facesAnswer([], adds: true), path: "/v.mp4")
        #expect(none.addList(all, loaded: true, filter: "ANA") == .people([all[0]]))
        #expect(none.addList(all, loaded: true, filter: " le ") == .people([all[1]]))
        #expect(none.addList(all, loaded: true, filter: "unnamed") == .people([all[9]]))
        #expect(none.addList(all, loaded: true, filter: "zz") == .noMatch)
        #expect(none.addList(all, loaded: true, filter: "").count == 10)
        // Two in it: 8 left, no field, so what was typed before is let be.
        let two = PhotoFacesState.of(facesAnswer([photoFace("p0", "Ána"), photoFace("p1", "Leo")], adds: true), path: "/a.jpg")
        #expect(two.addList(all, loaded: true, filter: "zz").count == 8)
        // Everyone's too, with the Set Someone else… shares.
        #expect(PersonMove.choices(all, excluding: Set(["p0", "u"])).map(\.id) == (1...8).map { "p\($0)" })
    }

    @Test func addAPersonsAnswer() {
        #expect(PhotoFacesState.addFailure(ack()) == nil)
        // The device's words, in the app's language already.
        var refused = Msg_RespEnvelope()
        refused.error = true
        refused.errorCode = "face_recognition_off"
        refused.errorMessage = " Face recognition is off. "
        #expect(PhotoFacesState.addFailure(refused) == "Face recognition is off.")
        #expect(PhotoFacesState.addFailure(ack(ok: false, words: "That person is gone.")) == "That person is gone.")
        // Without any: face_add_failed. No answer, a device too old for it
        // (whose words are English, not the owner's), the bridge's own
        // "device unreachable", or no words at all.
        let failed = "Couldn't add this person. Try again."
        #expect(PhotoFacesState.addFailure(nil) == failed)
        #expect(PhotoFacesState.addFailure(refusal("unknown_payload")) == failed)
        var old = Msg_RespEnvelope()
        old.error = true
        old.errorMessage = "unknown payload"
        #expect(PhotoFacesState.addFailure(old) == failed)
        var blank = refusal("demo")
        blank.errorMessage = "  "
        #expect(PhotoFacesState.addFailure(blank) == failed)
        #expect(PhotoFacesState.addFailure(ack(ok: false, code: LoadProblem.deviceUnreachable, words: "Device unreachable")) == failed)
        #expect(PhotoFacesState.addFailure(ack(ok: false)) == failed)
        #expect(PhotoFacesState.addFailure(Msg_RespEnvelope()) == failed)
    }

    @Test func addAPersonsTexts() {
        #expect(S.appPhotosViewerFaceAdd == "Add a person")
        #expect(PhotoFacesState.addTitle(video: false) == "Who is in this photo?")
        #expect(PhotoFacesState.addTitle(video: true) == "Who is in this video?")
        #expect(S.appPhotosViewerFaceAddFind == "Find by name")
        #expect(S.appPhotosViewerFaceAddNone == "No one has been found yet.")
        #expect(S.appPhotosViewerFaceAddAll == "Everyone found so far is in it already.")
        #expect(S.appPhotosViewerFaceAddNoMatch == "No one has that name.")
    }
}

private extension PhotoFacesState.AddList {
    /// How many people it lists.
    var count: Int {
        if case .people(let p) = self { return p.count }
        return 0
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
/// the person's faces off that content, AddToPerson adding them to it.
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
    /// It takes AddToPerson (PhotoFaces.adds_people).
    var addsPeople = false
    var adds: [Msg_AddToPerson] = []
    /// How AddToPerson is answered instead, when set; nil adds them.
    var addAnswer: Msg_RespEnvelope??
    /// How long AddToPerson takes.
    var addSlow: Double = 0

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
                guard let p = self.faces[g.path] else { return facesAnswer([], on: self.faceRecognition, adds: self.addsPeople) }
                return facesAnswer(p.faces, on: self.faceRecognition, hash: p.hash, adds: self.addsPeople)
            case .reqAddToPerson(let a):
                self.adds.append(a)
                if self.addSlow > 0 { try? await Task.sleep(nanoseconds: UInt64(self.addSlow * 1_000_000_000)) }
                if let answer = self.addAnswer {
                    guard let answer else { throw CancellationError() }
                    return answer
                }
                // After the faces, by hand, with their People face.
                for (path, p) in self.faces where a.hashes.contains(p.hash) {
                    self.faces[path] = (p.hash, p.faces + [photoFace(a.personID, a.personID == "p2" ? "Leo" : "", byHand: true)])
                }
                return ack()
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

    /// Images on Ana's photos (a face opened from People), or on everyone;
    /// `adds`: a device that takes AddToPerson.
    private func model(filter: Bool = true, adds: Bool = false) async -> (PhotoGalleryVM, FacesDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = FacesDevice()
        device.addsPeople = adds
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
        await waitFor { vm.infoFaces.shown }
    }

    /// Files' viewer on one video.
    private func video(adds: Bool) -> (PhotoGalleryVM, FacesDevice) {
        let vm = PhotoGalleryVM()
        let device = FacesDevice()
        device.addsPeople = adds
        device.faces["/v.mp4"] = ("hv", [])
        device.attach(vm)
        vm.showFiles([PhotoGalleryVM.Item(id: "v", path: "/v.mp4", mime: "video/mp4", size: 1, thumbData: nil,
                                          localURL: nil, isLocalOnly: false, hash: "hv")], startAt: 0)
        return (vm, device)
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

    /// A video is asked for too: a device from before AddToPerson answers
    /// it with no faces, and nothing shows, as before.
    @Test func aVideoIsAskedForAndNothingShowsFromAnOldDevice() async {
        let (vm, device) = video(adds: false)
        vm.openInfo()
        await waitFor { device.asked == ["/v.mp4"] }
        await waitFor(0.2) { false }
        #expect(device.asked == ["/v.mp4"])
        #expect(!vm.infoFaces.shown)
    }

    /// From a device that takes AddToPerson, People shows on a video with
    /// no one in it yet: Add a person alone.
    @Test func aVideoGetsAddAPerson() async {
        let (vm, device) = video(adds: true)
        vm.openInfo()
        await waitFor { vm.infoFaces.shown }
        #expect(device.asked == ["/v.mp4"])
        #expect(vm.infoFaces.adds)
        #expect(vm.infoFaces.rows.isEmpty)
        #expect(vm.infoFaces.hash == "hv")
        // Face recognition off: nothing.
        device.faceRecognition = false
        vm.open(index: 0)
        vm.openInfo()
        await waitFor { device.asked.count == 2 }
        await waitFor(0.2) { false }
        #expect(!vm.infoFaces.shown)
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

    // MARK: Add a person

    /// Add a person opens its list, asking People again for the faces
    /// found since; only where the device takes it.
    @Test func addAPersonOpensTheListOfPeople() async {
        let (vm, device) = await model(adds: true)
        await openInfo(vm, at: 1)
        #expect(vm.infoFaces.adds)
        let lists = device.person.peopleLists
        vm.openFaceAdd()
        #expect(vm.faceAddOpen)
        await waitFor { device.person.peopleLists > lists }
        #expect(device.person.peopleLists > lists)
        // Ana is in it already: Leo and the unnamed one are offered.
        #expect(vm.infoFaces.addChoices(vm.allPeople).map(\.id) == ["p2", "p3"])
        vm.closeFaceAdd()
        #expect(!vm.faceAddOpen)
        // Not from a device before it.
        let (old, oldDevice) = await model(adds: false)
        await openInfo(old, at: 1)
        #expect(oldDevice.asked == ["/p/1.jpg"])
        #expect(old.infoFaces.shown && !old.infoFaces.adds)
        old.openFaceAdd()
        #expect(!old.faceAddOpen)
    }

    /// The one picked is sent, the list goes, Info asks the device again -
    /// its list has them, by hand - and People's counts are asked again.
    @Test func thePersonPickedIsAddedAndListed() async {
        let (vm, device) = await model(adds: true)
        await openInfo(vm, at: 1)
        vm.openFaceAdd()
        let asks = device.asked.count
        let lists = device.person.peopleLists
        await vm.addPerson(person("p2", "Leo"))
        #expect(device.adds.count == 1)
        #expect(device.adds.first?.personID == "p2")
        #expect(device.adds.first?.hashes == ["h1"])
        #expect(!vm.faceAddOpen)
        #expect(vm.faceAddSending == nil)
        #expect(vm.faceAddError == nil)
        #expect(device.asked.count == asks + 1)
        #expect(device.asked.last == "/p/1.jpg")
        #expect(vm.infoFaces.rows.map(\.person) == ["p1", "p2"])
        #expect(vm.infoFaces.rows.map(\.byHand) == [false, true])
        #expect(vm.infoFaces.rows.last?.removeLabel == "Remove \u{2068}Leo\u{2069}")
        await waitFor { device.person.peopleLists > lists }
        #expect(device.person.peopleLists > lists)
        // The photo stays, nothing is said, Info stays open.
        #expect(vm.items.count == 3)
        #expect(vm.infoOpen)
        #expect(!vm.showAlert)
        // Someone in it already isn't sent again.
        vm.openFaceAdd()
        await vm.addPerson(person("p2", "Leo"))
        #expect(device.adds.count == 1)
    }

    /// A video, in a viewer of a list: added the same way.
    @Test func someoneIsAddedToAVideo() async {
        let (vm, device) = video(adds: true)
        vm.allPeople = [person("p1", "Ana"), person("p2", "Leo")]
        vm.openInfo()
        await waitFor { vm.infoFaces.shown }
        vm.openFaceAdd()
        await vm.addPerson(person("p2", "Leo"))
        #expect(device.adds.first?.hashes == ["hv"])
        #expect(vm.infoFaces.rows.map(\.person) == ["p2"])
        #expect(vm.infoFaces.rows.first?.byHand == true)
        #expect(vm.items.count == 1)
    }

    /// Refused: the device's words in the list, else face_add_failed; the
    /// list stays open and Info as it was.
    @Test func aRefusalStaysInTheList() async {
        let (vm, device) = await model(adds: true)
        await openInfo(vm, at: 1)
        vm.openFaceAdd()
        var refused = Msg_RespEnvelope()
        refused.error = true
        refused.errorCode = "no_such_person"
        refused.errorMessage = "That person is no longer there."
        device.addAnswer = .some(refused)
        let asks = device.asked.count
        await vm.addPerson(person("p2", "Leo"))
        #expect(vm.faceAddError == "That person is no longer there.")
        #expect(vm.faceAddOpen)
        #expect(vm.faceAddSending == nil)
        #expect(device.asked.count == asks)
        #expect(vm.infoFaces.rows.map(\.person) == ["p1"])
        #expect(!vm.showAlert)
        // No answer: the app's own words.
        device.addAnswer = .some(nil)
        await vm.addPerson(person("p2", "Leo"))
        #expect(vm.faceAddError == S.appPhotosViewerFaceAddFailed)
        #expect(vm.faceAddOpen)
        // Tried again and added: the words go with the list.
        device.addAnswer = nil
        await vm.addPerson(person("p2", "Leo"))
        #expect(vm.faceAddError == nil)
        #expect(!vm.faceAddOpen)
        #expect(vm.infoFaces.rows.map(\.person) == ["p1", "p2"])
    }

    /// One at a time: a second pick while the first is on its way sends
    /// nothing; a spinner shows on the one picked meanwhile.
    @Test func onePickAtATime() async {
        let (vm, device) = await model(adds: true)
        await openInfo(vm, at: 1)
        vm.openFaceAdd()
        device.addSlow = 0.3
        let adding = Task { await vm.addPerson(person("p2", "Leo")) }
        await waitFor { device.adds.count == 1 }
        #expect(vm.faceAddSending == "p2")
        await vm.addPerson(person("p3", ""))
        #expect(device.adds.count == 1)
        await adding.value
        #expect(vm.faceAddSending == nil)
        #expect(vm.infoFaces.rows.map(\.person) == ["p1", "p2"])
    }

    /// The viewer moved to another photo while the device answered: the
    /// list went with Info, and the answer lists nothing on the new photo.
    @Test func anAnswerAfterTheViewerMovedOnIsNotListedThere() async {
        let (vm, device) = await model(adds: true)
        await openInfo(vm, at: 1)
        vm.openFaceAdd()
        device.addSlow = 0.3
        let adding = Task { await vm.addPerson(person("p2", "Leo")) }
        await waitFor { device.adds.count == 1 }
        vm.open(index: 0)
        #expect(!vm.infoOpen)
        #expect(!vm.faceAddOpen)
        #expect(vm.faceAddSending == nil)
        vm.openInfo()
        await waitFor { vm.infoFaces.shown }
        let asks = device.asked
        await adding.value
        #expect(device.asked == asks)
        #expect(vm.infoFaces.path == "/p/0.jpg")
        #expect(vm.infoFaces.rows.map(\.person) == ["p1", "p2"])
        #expect(vm.infoFaces.rows.allSatisfy { !$0.byHand })
        #expect(!vm.faceAddOpen)
        #expect(vm.faceAddError == nil)
    }

    /// Someone added by hand to one of Ana's photos is taken off it as a
    /// face is (the view sends it without Not this person's question):
    /// RemoveFromPerson with the photo's hash; Ana's own leaves her grid.
    @Test func someoneAddedByHandIsTakenOffAsAFaceIs() async throws {
        let (vm, device) = await model(adds: true)
        device.faces["/p/1.jpg"] = ("h1", [photoFace("p1", "Ana", byHand: true)])
        await openInfo(vm, at: 1)
        let ana = try #require(vm.infoFaces.rows.first)
        #expect(ana.byHand)
        await vm.removeFace(ana)
        #expect(device.removes.first?.personID == "p1")
        #expect(device.removes.first?.hashes == ["h1"])
        #expect(device.removes.first?.toPersonID == "")
        #expect(vm.items.map(\.path) == ["/p/0.jpg", "/p/2.jpg"])
        #expect(!vm.infoOpen)
        #expect(vm.notice?.text == S.appPhotosPersonRemoved(count: 1, name: "Ana"))
    }

    /// Elsewhere the row leaves Info, which keeps Add a person.
    @Test func theLastOneAddedByHandLeavesAddAPerson() async throws {
        let (vm, device) = video(adds: true)
        device.faces["/v.mp4"] = ("hv", [photoFace("p2", "Leo", byHand: true)])
        vm.openInfo()
        await waitFor { !vm.infoFaces.rows.isEmpty }
        let leo = try #require(vm.infoFaces.rows.first)
        await vm.removeFace(leo)
        #expect(device.removes.first?.personID == "p2")
        #expect(device.removes.first?.hashes == ["hv"])
        #expect(vm.items.count == 1)
        #expect(vm.infoFaces.shown)
        #expect(vm.infoFaces.adds)
        #expect(vm.infoFaces.rows.isEmpty)
        // A video's note says video.
        #expect(vm.notice?.text == "Removed this video from \u{2068}Leo\u{2069}")
        #expect(vm.alertShownOverInfo)
    }

    /// Someone unnamed taken off a video: the note says this person, and
    /// video.
    @Test func anUnnamedPersonTakenOffAVideo() async throws {
        let (vm, device) = video(adds: true)
        device.faces["/v.mp4"] = ("hv", [photoFace("p3", "", byHand: true)])
        vm.openInfo()
        await waitFor { !vm.infoFaces.rows.isEmpty }
        let someone = try #require(vm.infoFaces.rows.first)
        await vm.removeFace(someone)
        #expect(device.removes.first?.personID == "p3")
        #expect(vm.notice?.text == "Removed this video from this person")
        // Photos keep their own note.
        #expect(GalleryNotice.personRemoved(1, "Ana").text == "Removed 1 photo from \u{2068}Ana\u{2069}")
        #expect(GalleryNotice.personRemovedVideo("Ana").text == S.appPhotosPersonRemovedVideo(name: "Ana"))
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
