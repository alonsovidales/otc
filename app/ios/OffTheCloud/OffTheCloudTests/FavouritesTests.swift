// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import Testing
@testable import OffTheCloud

/// Waits (`seconds` at most) for `done`.
@MainActor
private func waitUntil(_ seconds: Double = 3, _ done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

private func photo(_ path: String, _ hash: String = "") -> FavouritePhoto {
    FavouritePhoto(path: path, hash: hash)
}

private func item(_ path: String, local: Bool = false) -> PhotoGalleryVM.Item {
    PhotoGalleryVM.Item(id: path, path: path, mime: "image/jpeg", size: 1, thumbData: nil, localURL: nil,
                        isLocalOnly: local, hash: "h\(path)")
}

/// The device, for the heart's requests: which paths hold a favourite,
/// what it was asked, and how it answers.
@MainActor
final class FavouritesDevice {
    var favourites: Set<String> = []
    var gets: [[String]] = []
    var sets: [Msg_SetFavourite] = []
    /// How GetFavourites / SetFavourite are refused: nil is answered.
    var getRefusal: (code: String, message: String)?
    var setRefusal: (code: String, message: String)?
    /// SetFavourite answered with this Ack instead.
    var setAck: Msg_Ack?
    /// No answer to SetFavourite at all.
    var setThrows = false
    /// GetFavourites answers, read when asked, held until `releaseGets`.
    var holdGets = false
    private var heldGets: [CheckedContinuation<Void, Never>] = []
    /// SetFavourite answers held, one at a time, until `releaseSet`.
    var holdSets = false
    private var heldSets: [CheckedContinuation<Void, Never>] = []

    var setsHeld: Int { heldSets.count }

    func releaseGets() {
        let held = heldGets
        heldGets = []
        held.forEach { $0.resume() }
    }

    func releaseSet() {
        guard !heldSets.isEmpty else { return }
        heldSets.removeFirst().resume()
    }

    private func refusal(_ r: (code: String, message: String)) -> Msg_RespEnvelope {
        var e = Msg_RespEnvelope()
        e.error = true
        e.errorCode = r.code
        e.errorMessage = r.message
        return e
    }

    func attach(_ store: FavouritesModel) {
        store.request = { [weak self] payload in
            guard let self else { throw CancellationError() }
            return try await self.answer(payload)
        }
    }

    func answer(_ payload: Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope {
        switch payload {
        case .reqGetFavourites(let g):
            gets.append(g.paths)
            if let r = getRefusal { return refusal(r) }
            // Read now, answered once let go: the device read it before
            // whatever the app does meanwhile.
            var f = Msg_Favourites()
            f.paths = g.paths.filter { favourites.contains($0) }
            if holdGets { await withCheckedContinuation { heldGets.append($0) } }
            var e = Msg_RespEnvelope()
            e.payload = .respFavourites(f)
            return e
        case .reqSetFavourite(let s):
            sets.append(s)
            if holdSets { await withCheckedContinuation { heldSets.append($0) } }
            if setThrows { throw URLError(.timedOut) }
            if let r = setRefusal { return refusal(r) }
            var e = Msg_RespEnvelope()
            if let a = setAck {
                e.payload = .respAck(a)
                return e
            }
            for p in s.paths {
                if s.favourite { favourites.insert(p) } else { favourites.remove(p) }
            }
            var a = Msg_Ack()
            a.ok = true
            e.payload = .respAck(a)
            return e
        default:
            throw CancellationError()
        }
    }
}

/// What the viewer asks about as it shows a photo (FavouriteAsk).
struct FavouriteAskTests {
    @Test func theOpenPhotoThenTheNextAndThePrevious() {
        let items = (0..<5).map { item("/p/\($0).jpg") }
        #expect(FavouriteAsk.around(2, in: items).map(\.path) == ["/p/2.jpg", "/p/3.jpg", "/p/1.jpg"])
        #expect(FavouriteAsk.around(2, in: items).map(\.hash) == ["h/p/2.jpg", "h/p/3.jpg", "h/p/1.jpg"])
        // At either end, what there is.
        #expect(FavouriteAsk.around(0, in: items).map(\.path) == ["/p/0.jpg", "/p/1.jpg"])
        #expect(FavouriteAsk.around(4, in: items).map(\.path) == ["/p/4.jpg", "/p/3.jpg"])
        #expect(FavouriteAsk.around(0, in: [item("/a.jpg")]).map(\.path) == ["/a.jpg"])
        #expect(FavouriteAsk.around(7, in: items).isEmpty)
        #expect(FavouriteAsk.around(0, in: []).isEmpty)
    }

    @Test func neverAPhotoOnlyOnThisPhoneAndEachPathOnce() {
        let items = [item("/a.jpg"), item("/local.jpg", local: true), item("/a.jpg"), item("/b.jpg")]
        #expect(!FavouriteAsk.eligible(items[1]))
        #expect(FavouriteAsk.eligible(items[0]))
        #expect(FavouriteAsk.around(1, in: items).map(\.path) == ["/a.jpg"])
        #expect(FavouriteAsk.around(2, in: items).map(\.path) == ["/a.jpg", "/b.jpg"])
        #expect(FavouriteAsk.around(1, in: [item("/local.jpg", local: true)]).isEmpty)
        #expect(!FavouriteAsk.eligible(item("")))
    }
}

/// The hearts' store (FavouritesModel): what the device says, a tap, a
/// refusal, a device before Favourites. The Android app's rules are the
/// same.
@MainActor
struct FavouritesModelTests {
    private func store() -> (FavouritesModel, FavouritesDevice) {
        let s = FavouritesModel()
        let d = FavouritesDevice()
        d.attach(s)
        return (s, d)
    }

    @Test func askedPhotosGetTheDevicesAnswerInOneRequest() async {
        let (s, d) = store()
        d.favourites = ["/a.jpg"]
        #expect(await s.ask([photo("/a.jpg", "ha"), photo("/b.jpg", "hb")]))
        #expect(d.gets == [["/a.jpg", "/b.jpg"]])
        #expect(s.favourite("/a.jpg") == true)
        #expect(s.favourite("/b.jpg") == false)
        // Never asked about: not known, no heart.
        #expect(s.favourite("/c.jpg") == nil)
        // Nothing to ask about: nothing sent.
        #expect(!(await s.ask([])))
        #expect(d.gets.count == 1)
    }

    @Test func aTapShowsAtOnceAndTheDeviceTakesIt() async {
        let (s, d) = store()
        await s.ask([photo("/a.jpg", "ha")])
        d.holdSets = true
        let me = ObjectIdentifier(d)
        let tap = s.toggle(photo("/a.jpg", "ha"), by: me)
        await waitUntil { d.setsHeld == 1 }
        // Full before the device has answered.
        #expect(s.favourite("/a.jpg") == true)
        #expect(d.sets.map(\.paths) == [["/a.jpg"]])
        #expect(d.sets.map(\.favourite) == [true])
        #expect(s.change.count == 0)
        d.releaseSet()
        #expect(await tap.value == .done)
        #expect(s.favourite("/a.jpg") == true)
        #expect(d.favourites == ["/a.jpg"])
        #expect(s.change == FavouriteChange(count: 1, by: me))
        // And out again.
        d.holdSets = false
        #expect(await s.toggle(photo("/a.jpg", "ha")).value == .done)
        #expect(s.favourite("/a.jpg") == false)
        #expect(d.favourites.isEmpty)
        #expect(s.change == FavouriteChange(count: 2, by: nil))
    }

    @Test func aRefusalPutsTheHeartBackWithTheDevicesWords() async {
        let (s, d) = store()
        await s.ask([photo("/a.jpg")])
        d.setRefusal = ("demo", "This is a demo: nothing can be changed.")
        #expect(await s.toggle(photo("/a.jpg")).value == .failed("This is a demo: nothing can be changed."))
        #expect(s.favourite("/a.jpg") == false)
        #expect(s.change.count == 0)
        // Without words, ours (nil here).
        d.setRefusal = ("", "  ")
        #expect(await s.toggle(photo("/a.jpg")).value == .failed(nil))
        #expect(s.favourite("/a.jpg") == false)
        // No answer at all.
        d.setRefusal = nil
        d.setThrows = true
        #expect(await s.toggle(photo("/a.jpg")).value == .failed(nil))
        #expect(s.favourite("/a.jpg") == false)
    }

    @Test func whatEachAnswerSays() {
        #expect(FavouritesModel.verdict(nil) == .refused(nil))
        var ok = Msg_RespEnvelope()
        var a = Msg_Ack()
        a.ok = true
        ok.payload = .respAck(a)
        #expect(FavouritesModel.verdict(ok) == .ok)
        // The bridge answering for a device it can't reach: not the device's words.
        var bridge = Msg_RespEnvelope()
        var b = Msg_Ack()
        b.code = "device_unreachable"
        b.errorMsg = "device unreachable"
        bridge.payload = .respAck(b)
        #expect(FavouritesModel.verdict(bridge) == .refused(nil))
        var no = Msg_RespEnvelope()
        var n = Msg_Ack()
        n.errorMsg = "No room left"
        no.payload = .respAck(n)
        #expect(FavouritesModel.verdict(no) == .refused("No room left"))
        var older = Msg_RespEnvelope()
        older.error = true
        older.errorCode = "unknown_payload"
        older.errorMessage = "this device's software is older than the app"
        #expect(FavouritesModel.verdict(older) == .unknownPayload)
        // Builds before v9 said it in English alone.
        older.errorCode = ""
        older.errorMessage = "unknown payload"
        #expect(FavouritesModel.verdict(older) == .unknownPayload)
        var refused = Msg_RespEnvelope()
        refused.error = true
        refused.errorCode = "demo"
        refused.errorMessage = "Not on the demo"
        #expect(FavouritesModel.verdict(refused) == .refused("Not on the demo"))
        // Something else than an Ack.
        var other = Msg_RespEnvelope()
        other.payload = .respFavourites(Msg_Favourites())
        #expect(FavouritesModel.verdict(other) == .refused(nil))
    }

    @Test func anOlderDeviceHasNoHeartUntilTheNextSignIn() async {
        let (s, d) = store()
        s.connected(endpoint: "wss://a.off-the.cloud/ws")
        d.getRefusal = ("unknown_payload", "this device's software is older than the app")
        #expect(!(await s.ask([photo("/a.jpg")])))
        #expect(s.unsupported)
        #expect(s.favourite("/a.jpg") == nil)
        // Not asked again on this connection.
        #expect(!(await s.ask([photo("/b.jpg")])))
        #expect(d.gets.count == 1)
        #expect(await s.toggle(photo("/a.jpg")).value == .done)
        #expect(d.sets.isEmpty)
        // Signed in again (it may have been updated): asked again.
        d.getRefusal = nil
        s.connected(endpoint: "wss://a.off-the.cloud/ws")
        #expect(!s.unsupported)
        #expect(await s.ask([photo("/a.jpg")]))
        #expect(s.favourite("/a.jpg") == false)
    }

    @Test func anOlderDeviceRefusingTheTapToo() async {
        let (s, d) = store()
        await s.ask([photo("/a.jpg")])
        d.setRefusal = ("unknown_payload", "")
        #expect(await s.toggle(photo("/a.jpg")).value == .unsupported)
        #expect(s.unsupported)
        #expect(s.favourite("/a.jpg") == nil)
    }

    @Test func anotherDevicesAnswersGo() async {
        let (s, d) = store()
        s.connected(endpoint: "wss://a.off-the.cloud/ws")
        d.favourites = ["/a.jpg"]
        await s.ask([photo("/a.jpg")])
        // The same device again (a reconnect): kept.
        s.connected(endpoint: "wss://a.off-the.cloud/ws")
        #expect(s.favourite("/a.jpg") == true)
        s.connected(endpoint: "wss://b.off-the.cloud/ws")
        #expect(s.favourite("/a.jpg") == nil)
        await s.ask([photo("/a.jpg")])
        s.reset()
        #expect(s.favourite("/a.jpg") == nil)
        #expect(!s.unsupported)
    }

    @Test func anAnswerFromBeforeLogOutLandsNowhere() async {
        let (s, d) = store()
        d.favourites = ["/a.jpg"]
        d.holdGets = true
        let ask = Task { await s.ask([photo("/a.jpg")]) }
        await waitUntil { d.gets.count == 1 }
        s.reset()
        d.releaseGets()
        #expect(!(await ask.value))
        #expect(s.favourite("/a.jpg") == nil)
    }

    /// The device read the photo before the tap (requests run at once on
    /// it): that answer, coming after, doesn't empty the heart again.
    @Test func anAnswerReadBeforeATapDoesntUndoIt() async {
        let (s, d) = store()
        await s.ask([photo("/a.jpg")])
        d.holdGets = true
        let ask = Task { await s.ask([photo("/a.jpg"), photo("/b.jpg")]) }
        await waitUntil { d.gets.count == 2 }
        #expect(await s.toggle(photo("/a.jpg")).value == .done)
        #expect(s.favourite("/a.jpg") == true)
        d.releaseGets()
        #expect(await ask.value)
        #expect(s.favourite("/a.jpg") == true)
        // The rest of that answer is used.
        #expect(s.favourite("/b.jpg") == false)
        // Asked after it: the device's word again.
        d.holdGets = false
        d.favourites = []
        await s.ask([photo("/a.jpg")])
        #expect(s.favourite("/a.jpg") == false)
    }

    /// One asked while the tap was on its way may have been read before it
    /// too.
    @Test func anAnswerAskedWhileATapIsOnItsWayToo() async {
        let (s, d) = store()
        await s.ask([photo("/a.jpg")])
        d.holdSets = true
        let tap = s.toggle(photo("/a.jpg"))
        await waitUntil { d.setsHeld == 1 }
        // Read before the device took the tap: says no.
        let ask = Task { await s.ask([photo("/a.jpg")]) }
        await waitUntil { d.gets.count == 2 }
        #expect(await ask.value)
        #expect(s.favourite("/a.jpg") == true)
        d.releaseSet()
        #expect(await tap.value == .done)
        #expect(s.favourite("/a.jpg") == true)
    }

    @Test func tapsWhileOneIsOnItsWayAreSentOnceItIsAnswered() async {
        let (s, d) = store()
        await s.ask([photo("/a.jpg")])
        d.holdSets = true
        let first = s.toggle(photo("/a.jpg"))
        await waitUntil { d.setsHeld == 1 }
        // Emptied again meanwhile: shown at once, sent once the first is answered.
        #expect(await s.toggle(photo("/a.jpg")).value == .done)
        #expect(s.favourite("/a.jpg") == false)
        #expect(d.sets.count == 1)
        d.releaseSet()
        await waitUntil { d.setsHeld == 1 }
        #expect(d.sets.map(\.favourite) == [true, false])
        d.releaseSet()
        #expect(await first.value == .done)
        #expect(s.favourite("/a.jpg") == false)
        #expect(d.favourites.isEmpty)
        #expect(s.change.count == 2)
    }

    @Test func tappedBackBeforeTheAnswerSendsNothingMore() async {
        let (s, d) = store()
        await s.ask([photo("/a.jpg")])
        d.holdSets = true
        let first = s.toggle(photo("/a.jpg"))
        await waitUntil { d.setsHeld == 1 }
        s.toggle(photo("/a.jpg"))
        s.toggle(photo("/a.jpg"))
        #expect(s.favourite("/a.jpg") == true)
        d.releaseSet()
        #expect(await first.value == .done)
        #expect(d.sets.map(\.favourite) == [true])
        #expect(s.favourite("/a.jpg") == true)
    }

    @Test func aRefusalAfterSeveralTapsGoesBackToWhatTheDeviceHolds() async {
        let (s, d) = store()
        await s.ask([photo("/a.jpg")])
        d.holdSets = true
        let first = s.toggle(photo("/a.jpg"))
        await waitUntil { d.setsHeld == 1 }
        s.toggle(photo("/a.jpg"))
        d.releaseSet()
        await waitUntil { d.setsHeld == 1 }
        // The first was taken (in), the second refused: in it stays.
        d.setRefusal = ("", "Busy")
        d.releaseSet()
        #expect(await first.value == .failed("Busy"))
        #expect(s.favourite("/a.jpg") == true)
    }

    /// A member is a photo: every copy known here follows.
    @Test func everyCopyFollows() async {
        let (s, d) = store()
        await s.ask([photo("/p/a.jpg", "h"), photo("/backup/a.jpg", "h"), photo("/p/b.jpg", "hb")])
        #expect(await s.toggle(photo("/p/a.jpg", "h")).value == .done)
        #expect(d.sets.map(\.paths) == [["/p/a.jpg"]])
        #expect(s.favourite("/p/a.jpg") == true)
        #expect(s.favourite("/backup/a.jpg") == true)
        #expect(s.favourite("/p/b.jpg") == false)
        // Refused: back for both.
        d.setRefusal = ("", "No")
        #expect(await s.toggle(photo("/backup/a.jpg", "h")).value == .failed("No"))
        #expect(s.favourite("/p/a.jpg") == true)
        #expect(s.favourite("/backup/a.jpg") == true)
    }

    @Test func aPhotoNotAnsweredForHasNothingToTap() async {
        let (s, d) = store()
        #expect(await s.toggle(photo("/a.jpg")).value == .done)
        #expect(d.sets.isEmpty)
        #expect(s.favourite("/a.jpg") == nil)
    }

    /// Add to collection and Remove from collection on Favourites.
    @Test func photosPutInOrTakenOutAnotherWay() async {
        let (s, d) = store()
        await s.ask([photo("/a.jpg", "ha"), photo("/copy/a.jpg", "ha")])
        s.took([photo("/a.jpg", "ha"), photo("/b.jpg", "hb")], on: true)
        #expect(s.favourite("/a.jpg") == true)
        #expect(s.favourite("/copy/a.jpg") == true)
        #expect(s.favourite("/b.jpg") == true)
        s.took([photo("/b.jpg", "hb")], on: false)
        #expect(s.favourite("/b.jpg") == false)
        #expect(d.sets.isEmpty)
    }
}

/// The heart in Images' viewer over an open Favourites: an emptied heart
/// keeps the photo in the viewer, and the grid drops it as the viewer
/// closes.
extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct FavouritesViewerTests {
    static let favouritesGroup: Msg_ImageGroup = {
        var g = Msg_ImageGroup()
        g.id = "favourites"
        g.name = "Favourites"
        g.fileCount = 3
        g.favourites = true
        return g
    }()

    private func model(group: Msg_ImageGroup = Self.favouritesGroup) async -> (PhotoGalleryVM, CollectionDevice, FavouritesDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = CollectionDevice()
        device.attach(vm)
        vm.bigThumbDelay = { _ in 0.05 }
        let hearts = FavouritesDevice()
        let store = FavouritesModel()
        hearts.attach(store)
        vm.favourites = store
        vm.activeGroup = group
        await vm.resetAndLoadFirstPage()
        return (vm, device, hearts)
    }

    @Test func theOpenPhotoAndItsNeighboursAreAskedAbout() async {
        let (vm, _, hearts) = await model()
        hearts.favourites = ["/p/0.jpg", "/p/1.jpg", "/p/2.jpg"]
        #expect(vm.items.map(\.path) == ["/p/0.jpg", "/p/1.jpg", "/p/2.jpg"])
        vm.open(index: 1)
        #expect(vm.heartShown)
        await waitUntil { vm.openFavourite != nil }
        #expect(hearts.gets == [["/p/1.jpg", "/p/2.jpg", "/p/0.jpg"]])
        #expect(vm.openFavourite == true)
        // The swipe finds its answer.
        vm.next()
        #expect(vm.openFavourite == true)
        vm.closeModal()
        #expect(vm.items.count == 3)
    }

    @Test func anEmptiedHeartKeepsThePhotoUntilTheViewerCloses() async {
        let (vm, device, hearts) = await model()
        hearts.favourites = ["/p/0.jpg", "/p/1.jpg", "/p/2.jpg"]
        vm.open(index: 0)
        await waitUntil { vm.openFavourite != nil }
        let lists = device.groupLists
        vm.toggleFavourite()
        // At once.
        #expect(vm.openFavourite == false)
        await waitUntil { hearts.favourites.count == 2 }
        #expect(hearts.sets.map(\.favourite) == [false])
        // Still in the viewer, and can be filled again.
        #expect(vm.items.count == 3)
        #expect(vm.openPath == "/p/0.jpg")
        // The chip's count follows.
        await waitUntil { device.groupLists > lists }
        #expect(device.groupLists > lists)
        vm.closeModal()
        // Gone from the grid, every copy of it.
        #expect(vm.items.map(\.path) == ["/p/1.jpg", "/p/2.jpg"])
    }

    @Test func filledAgainItStays() async {
        let (vm, _, hearts) = await model()
        hearts.favourites = ["/p/0.jpg", "/p/1.jpg", "/p/2.jpg"]
        vm.open(index: 0)
        await waitUntil { vm.openFavourite != nil }
        vm.toggleFavourite()
        await waitUntil { hearts.favourites.count == 2 }
        vm.toggleFavourite()
        #expect(vm.openFavourite == true)
        await waitUntil { hearts.favourites.count == 3 }
        vm.closeModal()
        #expect(vm.items.count == 3)
    }

    @Test func outsideFavouritesTheGridKeepsIt() async {
        let (vm, _, hearts) = await model(group: CollectionDevice.group)
        hearts.favourites = ["/p/0.jpg"]
        vm.open(index: 0)
        await waitUntil { vm.openFavourite != nil }
        vm.toggleFavourite()
        await waitUntil { hearts.favourites.isEmpty }
        vm.closeModal()
        #expect(vm.items.count == 3)
    }

    @Test func aRefusalIsSaidOverTheViewerAndThePhotoStays() async {
        let (vm, _, hearts) = await model()
        hearts.favourites = ["/p/0.jpg", "/p/1.jpg", "/p/2.jpg"]
        vm.open(index: 0)
        await waitUntil { vm.openFavourite != nil }
        #expect(vm.openFavourite == true)
        hearts.setRefusal = ("demo", "Not on the demo")
        vm.toggleFavourite()
        await waitUntil { vm.showAlert }
        // Full again.
        #expect(vm.openFavourite == true)
        #expect(vm.notice?.text == "Not on the demo")
        #expect(vm.alertShown(overViewer: true))
        // Without the device's words, ours.
        vm.showAlert = false
        hearts.setRefusal = ("", "")
        vm.toggleFavourite()
        await waitUntil { vm.showAlert }
        #expect(vm.notice?.text == S.appPhotosViewerFavouriteFailed)
        #expect(vm.openFavourite == true)
        // Still in Favourites: stays in the grid.
        vm.closeModal()
        #expect(vm.items.count == 3)
    }

    /// The device says a photo of the grid isn't in Favourites (taken out
    /// on another app meanwhile): it goes as the viewer closes too.
    @Test func aPhotoTheDeviceSaysLeftGoesToo() async {
        let (vm, _, hearts) = await model()
        hearts.favourites = ["/p/0.jpg", "/p/2.jpg"]
        vm.open(index: 0)
        await waitUntil { vm.favourites.favourite("/p/1.jpg") != nil }
        vm.closeModal()
        #expect(vm.items.map(\.path) == ["/p/0.jpg", "/p/2.jpg"])
    }

    @Test func anOlderDeviceShowsNoHeart() async {
        let (vm, _, hearts) = await model()
        hearts.getRefusal = ("unknown_payload", "")
        vm.open(index: 0)
        await waitUntil { vm.favourites.unsupported }
        #expect(!vm.heartShown)
        #expect(vm.openFavourite == nil)
        vm.next()
        #expect(hearts.gets.count == 1)
        vm.closeModal()
    }

    /// Files' viewer (or the map's) took a photo out: an open Favourites
    /// asks for its photos again.
    @Test func aHeartFromAnotherViewerReloadsAnOpenFavourites() async {
        let (vm, device, hearts) = await model()
        let other = PhotoGalleryVM()
        other.favourites = vm.favourites
        other.showFiles([item("/p/0.jpg")], startAt: 0)
        await waitUntil { other.openFavourite != nil }
        let starts = vm.searchStarts
        let lists = device.groupLists
        other.toggleFavourite()
        await waitUntil { vm.searchStarts > starts && device.groupLists > lists }
        #expect(hearts.favourites == ["/p/0.jpg"])
        #expect(vm.searchStarts > starts)
        #expect(device.groupLists > lists)
        other.closeModal()
    }
}
}

/// Favourites in Collections: always first, never renamed or deleted.
@MainActor
struct FavouritesCollectionTests {
    @Test func favouritesKeepsItsNameAndIsAlwaysThere() async {
        let vm = PhotoGalleryVM()
        var g = Msg_ImageGroup()
        g.id = "favourites"
        g.name = "Favourites"
        g.favourites = true
        vm.activeGroup = g
        vm.renameGroupName = "Other"
        // Nothing is sent (the request would go to the shared connection).
        await vm.renameActiveGroup()
        await vm.deleteActiveGroup()
        #expect(vm.activeGroup?.name == "Favourites")
        #expect(!vm.showAlert)
    }

    /// Compared with the key, not the English: another suite may be
    /// switching the app's language meanwhile.
    @Test func aRefusalIsWordedByTheDeviceOrOurs() {
        #expect(GalleryNotice.favouriteFailed(nil).text == S.appPhotosViewerFavouriteFailed)
        #expect(GalleryNotice.favouriteFailed("Not on the demo").text == "Not on the demo")
    }
}
