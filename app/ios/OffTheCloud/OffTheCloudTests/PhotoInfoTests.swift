// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// The viewer's Info shows the details of the photo open: an answer for the
/// photo before (Info opened on A, the viewer moved on to B, Info opened
/// again) used to show A's camera, date and map under B, and its end
/// cleared B's spinner early. Android's PhotoGalleryInfoTest is the same.
@MainActor
private final class InfoDevice {
    let person = PersonDevice()
    /// How long GetFileInfo takes, by path.
    var slow: [String: Double] = [:]
    /// Paths GetFileInfo gets no answer for.
    var failing: Set<String> = []

    func attach(_ vm: PhotoGalleryVM) {
        person.attach(vm)
        let base = vm.photoRequest
        vm.photoRequest = { [weak self] payload, kind in
            guard let self else { throw CancellationError() }
            guard case .reqGetFileInfo(let gi) = payload else { return try await base(payload, kind) }
            if let s = self.slow[gi.path] { try? await Task.sleep(nanoseconds: UInt64(s * 1_000_000_000)) }
            if self.failing.contains(gi.path) { throw URLError(.timedOut) }
            var info = Msg_FileExifInfo()
            info.cameraMake = "camera of \(gi.path)"
            var r = Msg_RespEnvelope()
            r.payload = .respFileInfo(info)
            return r
        }
    }
}

@MainActor
private func settle(_ seconds: Double = 3, until done: () -> Bool) async {
    let end = Date() + seconds
    while !done() && Date() < end {
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

extension ImagesNotificationTests {
@MainActor
@Suite(.serialized)
struct PhotoInfoTests {
    private func model() async -> (PhotoGalleryVM, InfoDevice) {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        let device = InfoDevice()
        device.attach(vm)
        var ana = Msg_Person()
        ana.id = "p1"
        ana.name = "Ana"
        vm.allPeople = [ana]
        vm.selectedPeople = ["p1"]
        await vm.resetAndLoadFirstPage()
        return (vm, device)
    }

    @Test func aLateAnswerForThePhotoBeforeIsDropped() async {
        let (vm, device) = await model()
        #expect(vm.items.map(\.path) == ["/p/0.jpg", "/p/1.jpg", "/p/2.jpg"])
        device.slow = ["/p/0.jpg": 0.4, "/p/1.jpg": 0.8]
        vm.open(index: 0)
        vm.openInfo()
        vm.open(index: 1)
        vm.openInfo()
        // A's answer lands while B's Info is open: neither its details nor its end show.
        await settle(0.6) { false }
        #expect(vm.infoData == nil)
        #expect(vm.infoLoading)
        await settle { !vm.infoLoading }
        #expect(vm.infoData?.cameraMake == "camera of /p/1.jpg")
    }

    @Test func aFailedAnswerLeavesNothingOfThePhotoBefore() async {
        let (vm, device) = await model()
        vm.open(index: 0)
        vm.openInfo()
        await settle { !vm.infoLoading }
        #expect(vm.infoData?.cameraMake == "camera of /p/0.jpg")
        device.failing = ["/p/1.jpg"]
        vm.open(index: 1)
        vm.openInfo()
        await settle { !vm.infoLoading }
        #expect(vm.infoData == nil)
        // Info closed before its answer: nothing lands after, and no spinner stays.
        device.failing = []
        device.slow = ["/p/1.jpg": 0.3]
        vm.openInfo()
        vm.closeInfo()
        #expect(!vm.infoLoading)
        await settle(0.5) { false }
        #expect(vm.infoData == nil)
    }
}
}
