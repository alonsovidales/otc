// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
import UIKit
@testable import OffTheCloud

/// A person's face chosen by the owner (SetPersonCover): when Use this face
/// is offered, the request built, what the device's answers come to, and the
/// face at the top of their photos. Android's are the same.
struct PersonCoverTextTests {
    @Test func everyTextReadsAsWritten() {
        #expect(S.appPhotosPersonCoverUse == "Use this face")
        #expect(S.appPhotosPersonCoverChange == "Change face")
        #expect(S.appPhotosPersonCoverUpload == "Upload a photo…")
        #expect(S.appPhotosPersonCoverAuto == "Choose automatically")
        #expect(S.appPhotosPersonCoverHint == "Or select one of their photos and choose Use this face.")
        #expect(S.appPhotosPersonCoverDone == "Face updated")
        #expect(S.appPhotosPersonCoverFailed == "Couldn't change the face. Try again.")
        #expect(S.appPhotosPersonCropTitle == "Choose the face")
        #expect(S.appPhotosPersonCropHint == "Drag the photo to centre the face.")
        #expect(S.appPhotosPersonCoverNeedsUpdate == "Your device needs an update to change a person's face.")
        // The notes, as the gallery says them.
        #expect(GalleryNotice.coverDone.text == "Face updated")
        #expect(GalleryNotice.coverFailed(nil).text == "Couldn't change the face. Try again.")
        #expect(GalleryNotice.coverFailed("No face of this person in that photo").text == "No face of this person in that photo")
    }
}

private func item(_ path: String, hash: String, mime: String = "image/jpeg") -> PhotoGalleryVM.Item {
    PhotoGalleryVM.Item(id: path, path: path, mime: mime, size: 1, thumbData: nil, localURL: nil,
                        isLocalOnly: hash.isEmpty, hash: hash)
}

/// A real JPEG of `side` points (the crop's output is one).
private func jpeg(side: CGFloat = 32) -> Data {
    let format = UIGraphicsImageRendererFormat()
    format.scale = 1
    let img = UIGraphicsImageRenderer(size: CGSize(width: side, height: side), format: format).image { ctx in
        UIColor.orange.setFill()
        ctx.fill(CGRect(x: 0, y: 0, width: side, height: side))
    }
    return img.jpegData(compressionQuality: 0.8)!
}

/// The rules apart from Images (PersonCover).
struct PersonCoverRuleTests {
    @Test func useThisFaceTakesOnePhotoTheDeviceHolds() {
        #expect(PersonCover.usable(mime: "image/jpeg", hash: "h1"))
        #expect(PersonCover.usable(mime: "image/heic", hash: "h1"))
        // A video, or a photo only on this phone, has no face to give.
        #expect(!PersonCover.usable(mime: "video/mp4", hash: "h1"))
        #expect(!PersonCover.usable(mime: "image/jpeg", hash: ""))
        #expect(PersonCover.photo([item("/a.jpg", hash: "ha")])?.hash == "ha")
        #expect(PersonCover.photo([]) == nil)
        #expect(PersonCover.photo([item("/a.jpg", hash: "ha"), item("/b.jpg", hash: "hb")]) == nil)
        #expect(PersonCover.photo([item("/v.mov", hash: "hv", mime: "video/quicktime")]) == nil)
        #expect(PersonCover.photo([item("/l.jpg", hash: "")]) == nil)
    }

    @Test func theRequestCarriesTheChoice() {
        let photo = PersonCover.request(person: "p1", .photo("h1"))
        #expect(photo.personID == "p1")
        #expect(photo.cover == .hash("h1"))
        let data = jpeg()
        let image = PersonCover.request(person: "p1", .image(data))
        #expect(image.cover == .image(data))
        // Choose automatically: neither.
        let auto = PersonCover.request(person: "p1", .automatic)
        #expect(auto.personID == "p1")
        #expect(auto.cover == nil)
        // On the wire: envelope 155.
        var env = Msg_ReqEnvelope()
        env.payload = .reqSetPersonCover(photo)
        let back = try? Msg_ReqEnvelope(serializedBytes: env.serializedData())
        #expect(back?.reqSetPersonCover.hash == "h1")
    }

    @Test func onlyASmallJPEGGoes() {
        let small = jpeg()
        #expect(PersonCover.fits(small))
        #expect(!PersonCover.fits(Data()))
        let png = UIGraphicsImageRenderer(size: CGSize(width: 8, height: 8)).image { _ in }.pngData()!
        #expect(!PersonCover.fits(png))
        var big = Data([0xFF, 0xD8, 0xFF])
        big.append(Data(count: PersonCover.maxImageBytes - 3))
        #expect(PersonCover.fits(big))
        big.append(0)
        #expect(!PersonCover.fits(big))
        #expect(PersonCover.maxImageBytes == 262_144)
    }

    @Test func theDevicesAnswers() {
        var ok = Msg_RespEnvelope()
        var ack = Msg_Ack()
        ack.ok = true
        ok.payload = .respAck(ack)
        #expect(PersonCover.outcome(ok) == .done)
        #expect(PersonCover.outcome(nil) == .refused(nil))
        var old = Msg_RespEnvelope()
        old.error = true
        old.errorCode = "unknown_payload"
        old.errorMessage = "this device's software is older than the app"
        #expect(PersonCover.outcome(old) == .needsUpdate)
        var noFace = Msg_RespEnvelope()
        noFace.error = true
        noFace.errorMessage = " No face of this person in that photo "
        #expect(PersonCover.outcome(noFace) == .refused("No face of this person in that photo"))
        // The demo device: its notice.
        var demo = Msg_RespEnvelope()
        demo.error = true
        demo.errorCode = "demo"
        demo.errorMessage = "This is a demo"
        #expect(PersonCover.outcome(demo) == .refused("This is a demo"))
        var silent = Msg_RespEnvelope()
        silent.error = true
        #expect(PersonCover.outcome(silent) == .refused(nil))
        var refusedAck = Msg_RespEnvelope()
        var no = Msg_Ack()
        no.errorMsg = "No such person"
        refusedAck.payload = .respAck(no)
        #expect(PersonCover.outcome(refusedAck) == .refused("No such person"))
        // The bridge's own "unreachable" isn't the device's words.
        no.code = LoadProblem.deviceUnreachable
        refusedAck.payload = .respAck(no)
        #expect(PersonCover.outcome(refusedAck) == .refused(nil))
    }
}

/// The device, for one person's photos and their face.
@MainActor
final class CoverDevice {
    /// The person's photos: (path, hash, mime) - a video among them.
    var files: [(String, String, String)] = [
        ("/p/0.jpg", "h0", "image/jpeg"), ("/p/1.mov", "h1", "video/quicktime"), ("/p/2.jpg", "h2", "image/jpeg"),
    ]
    var covers: [Msg_SetPersonCover] = []
    var applies = 0
    var peopleLists = 0
    /// How SetPersonCover is answered: nil is ok.
    var refusal: (code: String, message: String)?
    /// GetInstanceRole: nil leaves it unanswered.
    var primary: Bool? = true

    private func ack() -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        var a = Msg_Ack()
        a.ok = true
        r.payload = .respAck(a)
        return r
    }

    func attach(_ vm: PhotoGalleryVM) {
        vm.sheetCloses = 0
        vm.pageRetry.delay = { _ in 0.05 }
        vm.bucketRetry.delay = { _ in 0.05 }
        vm.peopleRetry.delay = { _ in 0.05 }
        vm.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: false, statusCode: nil) }
        vm.photoRequest = { [weak self] payload, _ in
            guard let self else { throw CancellationError() }
            var r = Msg_RespEnvelope()
            switch payload {
            case .reqSearchPhotos:
                var lof = Msg_ListOfFiles()
                lof.files = self.files.enumerated().map { i, pf in
                    var f = Msg_File()
                    f.path = pf.0
                    f.hash = pf.1
                    f.mime = pf.2
                    f.created = Google_Protobuf_Timestamp(date: Date(timeIntervalSince1970: 1_780_000_000 - Double(i) * 3600))
                    return f
                }
                r.payload = .respListOfFiles(lof)
            case .reqPhotoDateBuckets:
                r.payload = .respPhotoDateBuckets(Msg_RespPhotoDateBuckets())
            default:
                throw CancellationError()
            }
            return r
        }
        vm.libraryRequest = { _, _ in throw CancellationError() }
        vm.peopleRequest = { [weak self] payload, _ in
            guard let self else { throw CancellationError() }
            switch payload {
            case .reqSetPersonCover(let c):
                self.covers.append(c)
                if let no = self.refusal {
                    var r = Msg_RespEnvelope()
                    r.error = true
                    r.errorCode = no.code
                    r.errorMessage = no.message
                    return r
                }
                return self.ack()
            case .reqListPeople:
                self.peopleLists += 1
                throw CancellationError()
            case .reqGetInstanceRole:
                guard let primary = self.primary else { throw CancellationError() }
                var role = Msg_RespInstanceRole()
                role.isPrimary = primary
                var r = Msg_RespEnvelope()
                r.payload = .respInstanceRole(role)
                return r
            case .reqApplyUpdate:
                self.applies += 1
                return self.ack()
            default:
                throw CancellationError()
            }
        }
    }
}

@MainActor
private func waitUntil(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

private func person(_ id: String, _ name: String, chosen: Bool = false) -> Msg_Person {
    var p = Msg_Person()
    p.id = id
    p.name = name
    p.faceCount = 3
    p.coverChosen = chosen
    return p
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct PersonCoverTests {
    /// Images on Ana's photos (a face opened from People).
    private func model() async -> (PhotoGalleryVM, CoverDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = CoverDevice()
        device.attach(vm)
        vm.allPeople = [person("p1", "Ana"), person("p2", "Leo", chosen: true)]
        vm.selectedPeople = ["p1"]
        await vm.resetAndLoadFirstPage()
        return (vm, device)
    }

    @Test func offeredForOnePhotoAmongOnePersonsPhotos() async {
        let (vm, _) = await model()
        #expect(vm.items.map(\.path) == ["/p/0.jpg", "/p/1.mov", "/p/2.jpg"])
        #expect(vm.coverPhoto(from: .selection) == nil)
        vm.selected = ["/p/2.jpg"]
        #expect(vm.coverPhoto(from: .selection)?.hash == "h2")
        // Two photos, or a video, not.
        vm.selected = ["/p/0.jpg", "/p/2.jpg"]
        #expect(vm.coverPhoto(from: .selection) == nil)
        vm.selected = ["/p/1.mov"]
        #expect(vm.coverPhoto(from: .selection) == nil)
        // Tags may join, as for Not this person.
        vm.selected = ["/p/0.jpg"]
        vm.chips = ["beach"]
        #expect(vm.coverPhoto(from: .selection)?.hash == "h0")
        // Not with two people in the filter, nor with none.
        vm.selectedPeople = ["p1", "p2"]
        #expect(vm.coverPhoto(from: .selection) == nil)
        vm.selectedPeople = []
        #expect(vm.coverPhoto(from: .selection) == nil)
        // Nor in Files' viewer.
        let files = PhotoGalleryVM()
        files.selectedPeople = ["p1"]
        #expect(files.coverPhoto(from: .viewer) == nil)
    }

    @Test func theViewerOffersItForTheOpenPhotoOnly() async {
        let (vm, _) = await model()
        #expect(vm.coverPhoto(from: .viewer) == nil)
        vm.open(index: 0)
        #expect(vm.coverPhoto(from: .viewer)?.hash == "h0")
        vm.open(index: 1)
        #expect(vm.coverPhoto(from: .viewer) == nil)
        vm.closeModal()
    }

    @Test func useThisFaceSendsThePhotoAndSaysSo() async throws {
        let (vm, device) = await model()
        vm.selected = ["/p/2.jpg"]
        let lists = device.peopleLists
        await vm.useFace(from: .selection)
        let sent = try #require(device.covers.first)
        #expect(device.covers.count == 1)
        #expect(sent.personID == "p1")
        #expect(sent.cover == .hash("h2"))
        // Done: the pick cleared, the photos stay, the note, the people
        // listed again for the new face.
        #expect(vm.selected.isEmpty)
        #expect(vm.items.count == 3)
        #expect(vm.preparing == nil)
        #expect(vm.alertShown(overViewer: false) && vm.notice?.text == S.appPhotosPersonCoverDone)
        await waitUntil { device.peopleLists > lists }
        #expect(device.peopleLists > lists)
        #expect(vm.needsUpdate == nil)
    }

    @Test func fromTheViewerItStaysOnThePhoto() async throws {
        let (vm, device) = await model()
        vm.open(index: 2)
        await vm.useFace(from: .viewer)
        #expect(device.covers.first?.cover == .hash("h2"))
        #expect(vm.openPath == "/p/2.jpg")
        #expect(vm.alertShown(overViewer: true) && !vm.alertShown(overViewer: false))
        #expect(vm.notice?.text == S.appPhotosPersonCoverDone)
        // A video open: nothing sent.
        vm.showAlert = false
        vm.open(index: 1)
        await vm.useFace(from: .viewer)
        #expect(device.covers.count == 1)
        vm.closeModal()
    }

    @Test func anUploadedPictureIsTheirsAtOnce() async throws {
        let (vm, device) = await model()
        let data = jpeg()
        #expect(vm.coverPerson?.id == "p1")
        #expect(vm.coverPerson?.coverChosen == false)
        await vm.setPersonCover("p1", .image(data))
        #expect(device.covers.first?.cover == .image(data))
        #expect(vm.allPeople.first?.coverThumbnail == data)
        #expect(vm.allPeople.first?.coverChosen == true)
        #expect(vm.face(for: try #require(vm.allPeople.first)) != nil)
        // Choose automatically is offered now.
        #expect(vm.coverPerson?.coverChosen == true)
        #expect(vm.notice?.text == S.appPhotosPersonCoverDone)
    }

    @Test func aPictureTooBigIsNotSent() async {
        let (vm, device) = await model()
        var big = Data([0xFF, 0xD8, 0xFF])
        big.append(Data(count: PersonCover.maxImageBytes))
        await vm.setPersonCover("p1", .image(big))
        #expect(device.covers.isEmpty)
        #expect(vm.notice?.text == S.appPhotosPersonCoverFailed)
        #expect(vm.allPeople.first?.coverChosen == false)
    }

    @Test func chooseAutomaticallySendsNoCover() async throws {
        let (vm, device) = await model()
        await vm.setPersonCover("p2", .automatic)
        let sent = try #require(device.covers.first)
        #expect(sent.personID == "p2")
        #expect(sent.cover == nil)
        #expect(vm.notice?.text == S.appPhotosPersonCoverDone)
    }

    @Test func aRefusalSaysTheDevicesWordsAndKeepsThePick() async {
        let (vm, device) = await model()
        device.refusal = ("", "No face of this person in that photo")
        vm.selected = ["/p/0.jpg"]
        await vm.useFace(from: .selection)
        #expect(vm.selected == ["/p/0.jpg"])
        #expect(vm.notice?.text == "No face of this person in that photo")
        #expect(vm.needsUpdate == nil)
        // No words: ours.
        device.refusal = ("", "")
        vm.showAlert = false
        await vm.useFace(from: .selection)
        #expect(vm.notice?.text == S.appPhotosPersonCoverFailed)
        #expect(vm.alertShown(overViewer: false))
    }

    /// A device from before it: nothing said as failed, and the owner is
    /// offered the update; a family member is told whom to ask.
    @Test func anOlderDeviceNeedsAnUpdate() async throws {
        let (vm, device) = await model()
        device.refusal = ("unknown_payload", "this device's software is older than the app")
        vm.selected = ["/p/2.jpg"]
        await vm.useFace(from: .selection)
        #expect(!vm.showAlert)
        #expect(vm.selected == ["/p/2.jpg"])
        let ask = try #require(vm.needsUpdate)
        #expect(ask.title == S.appPhotosPersonCoverNeedsUpdate)
        #expect(ask.canUpdate)
        #expect(await ask.startUpdate() == S.appShellDeviceUpdateStarted)
        #expect(device.applies == 1)
        vm.needsUpdate = nil
        device.primary = false
        await vm.setPersonCover("p1", .automatic)
        #expect(vm.needsUpdate?.canUpdate == false)
        #expect(vm.needsUpdate?.title == S.appPhotosPersonCoverNeedsUpdate)
    }

    /// The face at the top of the photos changes one person's face only.
    @Test func theHeadersFaceIsAMenuForOnePerson() async {
        let (vm, _) = await model()
        #expect(vm.coverPerson?.id == "p1")
        vm.selectedPeople = ["p2"]
        #expect(vm.coverPerson?.id == "p2")
        #expect(vm.coverPerson?.coverChosen == true)
        // Several people, tags (no header then) or none: no menu.
        vm.selectedPeople = ["p1", "p2"]
        #expect(vm.coverPerson == nil)
        vm.selectedPeople = ["p1"]
        vm.chips = ["beach"]
        #expect(vm.coverPerson == nil)
        vm.chips = []
        vm.selectedPeople = []
        #expect(vm.coverPerson == nil)
    }

    @Test func oneAtATime() async {
        let (vm, device) = await model()
        vm.selected = ["/p/0.jpg"]
        vm.preparing = .notThisPerson
        await vm.useFace(from: .selection)
        await vm.setPersonCover("p1", .automatic)
        #expect(device.covers.isEmpty)
        vm.preparing = nil
    }
}
}
