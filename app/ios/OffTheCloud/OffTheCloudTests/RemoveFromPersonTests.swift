// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Not this person and Someone else… (RemoveFromPerson): when Images offers
/// them, what it asks and sends, what leaves the grid and the viewer, and a
/// device too old for it (DeviceNeedsUpdate). Android's and the web's do
/// the same.
struct RemoveFromPersonTextTests {
    @Test func everyTextReadsAsWritten() {
        #expect(S.appShellNotThisPerson == "Not this person")
        #expect(S.appShellMoveToPerson == "Someone else…")
        // A name is isolated, as every name a person wrote.
        #expect(S.appPhotosPersonRemoveTitle(name: "Ana", count: 2) == "Remove 2 photos from \u{2068}Ana\u{2069}?")
        #expect(S.appPhotosPersonRemoveTitle(name: "Ana", count: 1) == "Remove this photo from \u{2068}Ana\u{2069}?")
        #expect(S.appPhotosPersonRemoveTitleUnnamed(count: 1) == "Remove this photo from this person?")
        #expect(S.appPhotosPersonRemoveText == "The photos stay on your device, and face recognition won't put them back.")
        #expect(S.appPhotosPersonMoveTitle(count: 3) == "Move 3 photos to…")
        #expect(S.appPhotosPersonMoveNone == "No one else has been found yet.")
        #expect(S.appPhotosPersonRemoved(count: 2, name: "Ana") == "Removed 2 photos from \u{2068}Ana\u{2069}")
        #expect(S.appPhotosPersonRemovedUnnamed(count: 1) == "Removed 1 photo from this person")
        #expect(S.appPhotosPersonMoved(count: 1, name: "Leo") == "Moved 1 photo to \u{2068}Leo\u{2069}")
        #expect(S.appPhotosPersonMovedUnnamed(count: 2) == "Moved 2 photos to an unnamed person")
        #expect(S.appPhotosPersonRemoveFailed == "Couldn't remove them. Try again.")
        #expect(S.appPhotosPersonMoveFailed == "Couldn't move them. Try again.")
        #expect(S.appPhotosPersonNeedsUpdate == "Your device needs an update to change who is in a photo.")
    }
}

private func person(_ id: String, _ name: String, _ faces: Int32) -> Msg_Person {
    var p = Msg_Person()
    p.id = id
    p.name = name
    p.faceCount = faces
    return p
}

/// Whom Someone else… offers (PersonMove).
struct PersonMoveTests {
    private let people = [
        person("u1", "", 30), person("ana", "Ána", 20), person("leo", "Leo", 9), person("u2", " ", 4), person("bea", "Bea", 2),
    ]

    @Test func everyoneElseNamedFirstInTheDevicesOrder() {
        #expect(PersonMove.choices(people, excluding: "leo").map(\.id) == ["ana", "bea", "u1", "u2"])
        #expect(PersonMove.choices(people, excluding: "nobody").count == 5)
        #expect(PersonMove.choices([person("ana", "Ana", 1)], excluding: "ana").isEmpty)
    }

    @Test func theNameFilterIgnoresCaseAndAccents() {
        #expect(PersonMove.choices(people, excluding: "leo", filter: "ANA").map(\.id) == ["ana"])
        #expect(PersonMove.choices(people, excluding: "leo", filter: " be ").map(\.id) == ["bea"])
        // The unnamed go by the word shown for them.
        #expect(PersonMove.choices(people, excluding: "leo", filter: "unnamed").map(\.id) == ["u1", "u2"])
        #expect(PersonMove.choices(people, excluding: "leo", filter: "zz").isEmpty)
    }

    @Test func aLongListGetsTheField() {
        #expect(!PersonMove.wantsFilter(8))
        #expect(PersonMove.wantsFilter(9))
    }

    @Test func whatEachFaceSays() {
        #expect(PersonMove.shownName(person("x", "  Ana  Lu ", 1)) == "Ana Lu")
        #expect(PersonMove.shownName(person("x", "", 1)) == "Unnamed")
        #expect(PersonMove.faceLabel(person("x", "Ana", 3)) == "\u{2068}Ana\u{2069}, 3 photos")
        #expect(PersonMove.faceLabel(person("x", "", 1)) == "Unnamed person, 1 photo")
        #expect(PersonMove.menuLabel(name: "Ana") == "More options for \u{2068}Ana\u{2069}")
        #expect(PersonMove.menuLabel(name: "") == "More options for this person")
    }
}

/// The device, for one person's photos and the requests around them.
@MainActor
final class PersonDevice {
    /// The person's photos: (path, hash) - /backup/0.jpg is another copy
    /// of /p/0.jpg, which the grid shows once.
    var files: [(String, String)] = [("/p/0.jpg", "h0"), ("/p/1.jpg", "h1"), ("/backup/0.jpg", "h0"), ("/p/2.jpg", "h2")]
    var removes: [Msg_RemoveFromPerson] = []
    var applies = 0
    var peopleLists = 0
    var bucketAsks = 0
    var searches: [Msg_SearchPhotos] = []
    /// How RemoveFromPerson is answered: nil is ok.
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

    private func refused(_ code: String, _ message: String) -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        r.error = true
        r.errorCode = code
        r.errorMessage = message
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
            case .reqSearchPhotos(let sp):
                self.searches.append(sp)
                var lof = Msg_ListOfFiles()
                lof.files = self.files.enumerated().map { i, pf in
                    var f = Msg_File()
                    f.path = pf.0
                    f.hash = pf.1
                    f.mime = "image/jpeg"
                    f.created = Google_Protobuf_Timestamp(date: Date(timeIntervalSince1970: 1_780_000_000 - Double(i) * 3600))
                    return f
                }
                r.payload = .respListOfFiles(lof)
            case .reqPhotoDateBuckets:
                self.bucketAsks += 1
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
            case .reqRemoveFromPerson(let rm):
                self.removes.append(rm)
                if let no = self.refusal { return self.refused(no.code, no.message) }
                // Off the person: no longer among their photos.
                self.files.removeAll { rm.hashes.contains($0.1) }
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

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct RemoveFromPersonTests {
    /// Images on Ana's photos (a face opened from People).
    private func model(name: String = "Ana") async -> (PhotoGalleryVM, PersonDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = PersonDevice()
        device.attach(vm)
        vm.allPeople = [person("p1", name, 3), person("p2", "Leo", 9), person("p3", "", 4)]
        vm.selectedPeople = ["p1"]
        await vm.resetAndLoadFirstPage()
        return (vm, device)
    }

    @Test func offeredWithExactlyOnePersonInTheFilter() async {
        let (vm, device) = await model()
        #expect(device.searches.last?.personIds == ["p1"])
        #expect(vm.items.map(\.path) == ["/p/0.jpg", "/p/1.jpg", "/p/2.jpg"])
        #expect(vm.personInFilter == "p1")
        #expect(vm.personInFilterName == "Ana")
        // Tags may be combined.
        vm.chips = ["beach"]
        #expect(vm.personInFilter == "p1")
        // Not with two people, nor with none.
        vm.selectedPeople = ["p1", "p2"]
        #expect(vm.personInFilter == nil)
        vm.selected = ["/p/0.jpg"]
        vm.askNotThisPerson(from: .selection)
        vm.askMoveToPerson(from: .selection)
        #expect(vm.personRemoveAsk == nil && vm.personMoveAsk == nil)
        vm.selectedPeople = []
        #expect(vm.personInFilter == nil)
        // Nor in Files' viewer.
        let files = PhotoGalleryVM()
        files.selectedPeople = ["p1"]
        #expect(files.personInFilter == nil)
    }

    @Test func notThisPersonAsksFirstThenTakesThePhotosOffAndEveryTileOfThem() async throws {
        let (vm, device) = await model()
        vm.selected = ["/p/0.jpg", "/p/1.jpg"]
        vm.askNotThisPerson(from: .selection)
        let ask = try #require(vm.personRemoveAsk)
        #expect(ask.from == .selection)
        #expect(ask.person == "p1")
        #expect(ask.count == 2)
        #expect(ask.removeTitle == S.appPhotosPersonRemoveTitle(name: "Ana", count: 2))
        // Nothing sent while it asks.
        #expect(device.removes.isEmpty)
        let lists = device.peopleLists
        let buckets = device.bucketAsks
        await vm.removeFromPerson(ask)
        #expect(device.removes.count == 1)
        #expect(device.removes.first?.personID == "p1")
        #expect(Set(device.removes.first?.hashes ?? []) == ["h0", "h1"])
        #expect(device.removes.first?.toPersonID == "")
        #expect(vm.items.map(\.path) == ["/p/2.jpg"])
        #expect(vm.selected.isEmpty)
        #expect(vm.preparing == nil)
        #expect(vm.personRemoveAsk == nil)
        #expect(vm.showAlert && vm.notice?.text == S.appPhotosPersonRemoved(count: 2, name: "Ana"))
        #expect(vm.needsUpdate == nil)
        // The months' and the person's counts, and the people's covers.
        await waitUntil { device.bucketAsks > buckets && device.peopleLists > lists }
        #expect(device.bucketAsks > buckets)
        #expect(device.peopleLists > lists)
    }

    @Test func aPersonWithoutANameIsThisPerson() async throws {
        let (vm, device) = await model(name: "  ")
        vm.selected = ["/p/1.jpg"]
        vm.askNotThisPerson(from: .selection)
        let ask = try #require(vm.personRemoveAsk)
        #expect(ask.removeTitle == S.appPhotosPersonRemoveTitleUnnamed(count: 1))
        await vm.removeFromPerson(ask)
        #expect(device.removes.count == 1)
        #expect(vm.notice?.text == S.appPhotosPersonRemovedUnnamed(count: 1))
    }

    @Test func someoneElseMovesThemToThePersonPicked() async throws {
        let (vm, device) = await model()
        vm.selected = ["/p/2.jpg"]
        let lists = device.peopleLists
        vm.askMoveToPerson(from: .selection)
        let ask = try #require(vm.personMoveAsk)
        #expect(ask.count == 1)
        // The list is asked for again as it opens, and never offers Ana.
        await waitUntil { device.peopleLists > lists }
        #expect(device.peopleLists > lists)
        #expect(PersonMove.choices(vm.allPeople, excluding: ask.person).map(\.id) == ["p2", "p3"])
        await vm.removeFromPerson(ask, to: vm.allPeople[1])
        #expect(device.removes.first?.personID == "p1")
        #expect(device.removes.first?.hashes == ["h2"])
        #expect(device.removes.first?.toPersonID == "p2")
        #expect(vm.personMoveAsk == nil)
        #expect(vm.items.map(\.path) == ["/p/0.jpg", "/p/1.jpg"])
        #expect(vm.notice?.text == S.appPhotosPersonMoved(count: 1, name: "Leo"))
    }

    @Test func movedToAnUnnamedPerson() async throws {
        let (vm, device) = await model()
        vm.selected = ["/p/0.jpg", "/p/2.jpg"]
        vm.askMoveToPerson(from: .selection)
        let ask = try #require(vm.personMoveAsk)
        await vm.removeFromPerson(ask, to: vm.allPeople[2])
        #expect(device.removes.first?.toPersonID == "p3")
        #expect(vm.notice?.text == S.appPhotosPersonMovedUnnamed(count: 2))
    }

    @Test func movingToTheSamePersonSendsNothing() async throws {
        let (vm, device) = await model()
        vm.selected = ["/p/0.jpg"]
        vm.askMoveToPerson(from: .selection)
        let ask = try #require(vm.personMoveAsk)
        await vm.removeFromPerson(ask, to: vm.allPeople[0])
        #expect(device.removes.isEmpty)
        #expect(vm.items.count == 3)
    }

    @Test func aRefusalKeepsTheTilesAndThePicks() async throws {
        let (vm, device) = await model()
        device.refusal = ("", "database is down")
        vm.selected = ["/p/1.jpg"]
        vm.askNotThisPerson(from: .selection)
        await vm.removeFromPerson(try #require(vm.personRemoveAsk))
        #expect(vm.items.count == 3)
        #expect(vm.selected == ["/p/1.jpg"])
        #expect(vm.notice?.text == S.appPhotosPersonRemoveFailed)
        vm.askMoveToPerson(from: .selection)
        await vm.removeFromPerson(try #require(vm.personMoveAsk), to: vm.allPeople[1])
        #expect(vm.items.count == 3)
        #expect(vm.notice?.text == S.appPhotosPersonMoveFailed)
        #expect(vm.needsUpdate == nil)
    }

    /// A device from before it: no tile leaves, nothing is said as failed,
    /// and the owner is offered the update.
    @Test func anOlderDeviceNeedsAnUpdate() async throws {
        let (vm, device) = await model()
        device.refusal = ("unknown_payload", "this device's software is older than the app")
        vm.selected = ["/p/1.jpg"]
        vm.askNotThisPerson(from: .selection)
        await vm.removeFromPerson(try #require(vm.personRemoveAsk))
        #expect(vm.items.count == 3)
        #expect(vm.selected == ["/p/1.jpg"])
        #expect(!vm.showAlert)
        let ask = try #require(vm.needsUpdate)
        #expect(ask.title == S.appPhotosPersonNeedsUpdate)
        #expect(ask.canUpdate)
        #expect(await ask.startUpdate() == S.appShellDeviceUpdateStarted)
        #expect(device.applies == 1)
        // A family member is told whom to ask.
        vm.needsUpdate = nil
        device.primary = false
        vm.askMoveToPerson(from: .selection)
        await vm.removeFromPerson(try #require(vm.personMoveAsk), to: vm.allPeople[1])
        #expect(vm.needsUpdate?.canUpdate == false)
        #expect(vm.needsUpdate?.title == S.appPhotosPersonNeedsUpdate)
    }

    /// The viewer's menu acts on the open photo, and moves on as after a
    /// delete: to the photo after it, the one before at the end, closed
    /// when none is left.
    @Test func fromTheViewerItGoesOnToTheNextPhotoOrCloses() async throws {
        let (vm, device) = await model()
        vm.open(index: 1)
        vm.askNotThisPerson(from: .viewer)
        let first = try #require(vm.personRemoveAsk)
        #expect(first.from == .viewer)
        #expect(first.paths == ["/p/1.jpg"])
        #expect(first.removeTitle == S.appPhotosPersonRemoveTitle(name: "Ana", count: 1))
        await vm.removeFromPerson(first)
        #expect(device.removes.last?.hashes == ["h1"])
        #expect(vm.items.map(\.path) == ["/p/0.jpg", "/p/2.jpg"])
        #expect(vm.openPath == "/p/2.jpg")
        // Said over the viewer, which goes on: Images' own alert would
        // take it away.
        #expect(vm.notice?.text == S.appPhotosPersonRemoved(count: 1, name: "Ana"))
        #expect(vm.alertShown(overViewer: true) && !vm.alertShown(overViewer: false))
        vm.showAlert = false
        // The last one: the one before it.
        vm.askMoveToPerson(from: .viewer)
        await vm.removeFromPerson(try #require(vm.personMoveAsk), to: vm.allPeople[1])
        #expect(device.removes.last?.toPersonID == "p2")
        #expect(vm.items.map(\.path) == ["/p/0.jpg"])
        #expect(vm.openPath == "/p/0.jpg")
        #expect(vm.notice?.text == S.appPhotosPersonMoved(count: 1, name: "Leo"))
        #expect(vm.alertShown(overViewer: true))
        vm.showAlert = false
        // None left: closed, and said by Images.
        vm.askNotThisPerson(from: .viewer)
        await vm.removeFromPerson(try #require(vm.personRemoveAsk))
        #expect(vm.items.isEmpty)
        #expect(vm.openIndex == nil)
        #expect(vm.alertShown(overViewer: false) && !vm.alertShown(overViewer: true))
        #expect(vm.notice?.text == S.appPhotosPersonRemoved(count: 1, name: "Ana"))
    }

    /// A refusal or an older device while the viewer is open: said over it,
    /// and the viewer stays on the photo.
    @Test func fromTheViewerARefusalIsSaidOverIt() async throws {
        let (vm, device) = await model()
        vm.open(index: 0)
        device.refusal = ("", "no")
        vm.askNotThisPerson(from: .viewer)
        await vm.removeFromPerson(try #require(vm.personRemoveAsk))
        #expect(vm.notice?.text == S.appPhotosPersonRemoveFailed)
        #expect(vm.alertShown(overViewer: true) && !vm.alertShown(overViewer: false))
        #expect(vm.openPath == "/p/0.jpg")
        #expect(vm.items.count == 3)
        device.refusal = ("unknown_payload", "")
        vm.askMoveToPerson(from: .viewer)
        await vm.removeFromPerson(try #require(vm.personMoveAsk), to: vm.allPeople[1])
        #expect(vm.needsUpdateShown(overViewer: true)?.title == S.appPhotosPersonNeedsUpdate)
        #expect(vm.needsUpdateShown(overViewer: false) == nil)
        #expect(vm.openPath == "/p/0.jpg")
    }

    /// Any alert raised while the viewer is open (Save's "Photos access
    /// denied", a refused delete) shows over it, never from the screen
    /// under it, which would close the viewer; once it is closed, Images
    /// shows it.
    @Test func alertsShowOverTheViewerWhileItIsOpen() async {
        let (vm, _) = await model()
        vm.notice = .photosDenied
        vm.showAlert = true
        #expect(vm.alertShown(overViewer: false) && !vm.alertShown(overViewer: true))
        vm.open(index: 0)
        #expect(vm.alertShown(overViewer: true) && !vm.alertShown(overViewer: false))
        vm.alertBinding(overViewer: true).wrappedValue = false
        #expect(!vm.showAlert)
        vm.closeModal()
        #expect(!vm.alertShown(overViewer: false))
    }

    /// What a pick in Someone else…'s list came to is said once the list
    /// has left the screen; Not this person's question is an alert, gone
    /// at once.
    @Test func aMoveIsSaidOnceTheListHasGone() async throws {
        let (vm, device) = await model()
        vm.sheetCloses = 0.3
        vm.selected = ["/p/0.jpg"]
        vm.askMoveToPerson(from: .selection)
        var began = Date()
        await vm.removeFromPerson(try #require(vm.personMoveAsk), to: vm.allPeople[1])
        #expect(Date().timeIntervalSince(began) >= 0.29)
        #expect(device.removes.count == 1)
        vm.showAlert = false
        vm.selected = ["/p/1.jpg"]
        vm.askNotThisPerson(from: .selection)
        began = Date()
        await vm.removeFromPerson(try #require(vm.personRemoveAsk))
        #expect(Date().timeIntervalSince(began) < 0.29)
        #expect(device.removes.count == 2)
    }

    @Test func oneAtATime() async throws {
        let (vm, device) = await model()
        vm.selected = ["/p/1.jpg"]
        vm.askNotThisPerson(from: .selection)
        let ask = try #require(vm.personRemoveAsk)
        vm.preparing = .notThisPerson
        await vm.removeFromPerson(ask)
        #expect(device.removes.isEmpty)
        vm.personRemoveAsk = nil
        vm.askNotThisPerson(from: .selection)
        #expect(vm.personRemoveAsk == nil)
        vm.preparing = nil
    }
}
}
