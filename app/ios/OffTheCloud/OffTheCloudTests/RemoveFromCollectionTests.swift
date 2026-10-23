// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Remove from collection: what Images sends, what leaves the grid, and a
/// device too old for it (DeviceNeedsUpdate). Android's and the web's do
/// the same.
struct RemoveFromCollectionTextTests {
    @Test func everyTextReadsAsWritten() {
        #expect(S.appShellRemoveFromCollection == "Remove from collection")
        // The name is isolated, as every name a person wrote.
        #expect(S.appPhotosNoticeRemoved(name: "Trip") == "Removed from \"\u{2068}Trip\u{2069}\".")
        #expect(S.appPhotosNoticeRemoveFailed == "Could not remove from the collection.")
        #expect(S.appPhotosCollectionsRemoveNeedsUpdate == "Your device needs an update to remove photos from a collection.")
        #expect(S.appShellDeviceUpdateStarted == "Your device is updating. It restarts by itself in a few minutes; then try again.")
        #expect(S.appShellDeviceUpdateAskOwner == "Ask whoever set up the device to update it in Settings › Device version.")
    }
}

/// The device, for a collection's grid and the requests around it.
@MainActor
final class CollectionDevice {
    /// The collection's photos: (path, hash) - /backup/0.jpg is another
    /// copy of /p/0.jpg, which the grid shows once.
    var files: [(String, String)] = [("/p/0.jpg", "h0"), ("/p/1.jpg", "h1"), ("/backup/0.jpg", "h0"), ("/p/2.jpg", "h2")]
    var removes: [Msg_RemoveFromImageGroup] = []
    var applies = 0
    var groupLists = 0
    var bucketAsks = 0
    /// How RemoveFromImageGroup is answered: nil is ok.
    var removeRefusal: (code: String, message: String)?
    /// GetInstanceRole: nil leaves it unanswered.
    var primary: Bool? = true
    /// ApplyUpdate: nil is ok.
    var applyRefusal: String?

    static let group: Msg_ImageGroup = {
        var g = Msg_ImageGroup()
        g.id = "g1"
        g.name = "Trip"
        g.fileCount = 4
        return g
    }()

    private func ack() -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        var a = Msg_Ack()
        a.ok = true
        r.payload = .respAck(a)
        return r
    }

    private func refusal(_ code: String, _ message: String) -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        r.error = true
        r.errorCode = code
        r.errorMessage = message
        return r
    }

    func attach(_ vm: PhotoGalleryVM) {
        vm.pageRetry.delay = { _ in 0.05 }
        vm.bucketRetry.delay = { _ in 0.05 }
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
        vm.libraryRequest = { [weak self] payload, _ in
            guard let self else { throw CancellationError() }
            switch payload {
            case .reqRemoveFromImageGroup(let rm):
                self.removes.append(rm)
                if let no = self.removeRefusal { return self.refusal(no.code, no.message) }
                return self.ack()
            case .reqListImageGroups:
                self.groupLists += 1
                var gs = Msg_ImageGroups()
                gs.groups = [Self.group]
                var r = Msg_RespEnvelope()
                r.payload = .respImageGroups(gs)
                return r
            case .reqGetInstanceRole:
                guard let primary = self.primary else { throw CancellationError() }
                var role = Msg_RespInstanceRole()
                role.isPrimary = primary
                var r = Msg_RespEnvelope()
                r.payload = .respInstanceRole(role)
                return r
            case .reqApplyUpdate:
                self.applies += 1
                if let no = self.applyRefusal { return self.refusal("", no) }
                return self.ack()
            default:
                throw CancellationError()
            }
        }
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
struct RemoveFromCollectionTests {
    private func model() async -> (PhotoGalleryVM, CollectionDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = CollectionDevice()
        device.attach(vm)
        vm.activeGroup = CollectionDevice.group
        await vm.resetAndLoadFirstPage()
        return (vm, device)
    }

    @Test func thePickedPhotosLeaveTheCollectionAndEveryTileOfThem() async {
        let (vm, device) = await model()
        #expect(vm.items.map(\.path) == ["/p/0.jpg", "/p/1.jpg", "/p/2.jpg"])
        vm.selected = ["/p/0.jpg", "/p/1.jpg"]
        let lists = device.groupLists
        let buckets = device.bucketAsks
        await vm.removeSelectionFromGroup()
        #expect(device.removes.count == 1)
        #expect(device.removes[0].groupID == "g1")
        #expect(Set(device.removes[0].paths) == ["/p/0.jpg", "/p/1.jpg"])
        #expect(vm.items.map(\.path) == ["/p/2.jpg"])
        #expect(vm.selected.isEmpty)
        #expect(vm.preparing == nil)
        #expect(vm.showAlert && vm.notice?.text == S.appPhotosNoticeRemoved(name: "Trip"))
        #expect(vm.needsUpdate == nil)
        // The collection's count and the months follow.
        await waitUntil { device.groupLists > lists && device.bucketAsks > buckets }
        #expect(device.groupLists > lists)
        #expect(device.bucketAsks > buckets)
    }

    @Test func outsideACollectionNothingIsSent() async {
        let (vm, device) = await model()
        vm.activeGroup = nil
        vm.selected = ["/p/0.jpg"]
        await vm.removeSelectionFromGroup()
        #expect(device.removes.isEmpty)
        #expect(vm.items.count == 3)
    }

    @Test func aRefusalKeepsTheTilesAndThePicks() async {
        let (vm, device) = await model()
        device.removeRefusal = ("", "database is down")
        vm.selected = ["/p/1.jpg"]
        await vm.removeSelectionFromGroup()
        #expect(vm.items.count == 3)
        #expect(vm.selected == ["/p/1.jpg"])
        #expect(vm.notice?.text == S.appPhotosNoticeRemoveFailed)
        #expect(vm.needsUpdate == nil)
    }

    /// A device from before it: no tile leaves, nothing is said as failed,
    /// and the owner is offered the update.
    @Test func anOlderDeviceNeedsAnUpdateAndTheOwnerCanStartIt() async throws {
        let (vm, device) = await model()
        device.removeRefusal = ("unknown_payload", "this device's software is older than the app")
        vm.selected = ["/p/1.jpg"]
        await vm.removeSelectionFromGroup()
        #expect(vm.items.count == 3)
        #expect(vm.selected == ["/p/1.jpg"])
        #expect(!vm.showAlert)
        let ask = try #require(vm.needsUpdate)
        #expect(ask.title == S.appPhotosCollectionsRemoveNeedsUpdate)
        #expect(ask.canUpdate)
        #expect(await ask.startUpdate() == S.appShellDeviceUpdateStarted)
        #expect(device.applies == 1)
        // The device's own words for an update that didn't start, or ours
        // without them.
        device.applyRefusal = "an update is already running"
        #expect(await ask.startUpdate() == "an update is already running")
        device.applyRefusal = ""
        #expect(await ask.startUpdate() == S.appSettingsUpdateStartFailed)
    }

    /// Builds before v9 said it in English alone.
    @Test func anOlderDeviceWithoutTheCodeToo() async {
        let (vm, device) = await model()
        device.removeRefusal = ("", "unknown payload")
        vm.selected = ["/p/1.jpg"]
        await vm.removeSelectionFromGroup()
        #expect(vm.needsUpdate != nil)
    }

    @Test func aFamilyMemberIsToldWhomToAsk() async {
        let (vm, device) = await model()
        device.removeRefusal = ("unknown_payload", "")
        device.primary = false
        vm.selected = ["/p/1.jpg"]
        await vm.removeSelectionFromGroup()
        #expect(vm.needsUpdate?.canUpdate == false)
        // No answer to the role: not offered either.
        vm.needsUpdate = nil
        device.primary = nil
        await vm.removeSelectionFromGroup()
        #expect(vm.needsUpdate?.canUpdate == false)
        #expect(device.applies == 0)
    }
}
}
