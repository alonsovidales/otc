// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import SwiftProtobuf
import SwiftUI
import Testing
@testable import OffTheCloud

/// Issue #194: the photos newer than a jump, asked for by a second cursor
/// as the grid's top comes near (NewerPhotos.swift, PhotoGalleryVM), and
/// put above the grid without moving what is on the screen. The same rules
/// as Android's and the web's.
@MainActor
struct NewerPageTests {
    private func file(_ path: String, _ hash: String) -> Msg_File {
        var f = Msg_File()
        f.path = path
        f.hash = hash
        f.mime = "image/jpeg"
        return f
    }

    @Test func anOlderDevicesPageIsDropped() {
        let page = PhotoPaging.newer(files: [file("/a.jpg", "a")], oldestFirst: false, shownIDs: [], shownHashes: [])
        #expect(page == .oldDevice)
    }

    @Test func theFilesComeNewestFirstLessThoseShown() {
        // Oldest first from the device: c (just above the cutoff), b, a.
        let files = [file("/c.jpg", "c"), file("/b.jpg", "b"), file("/a.jpg", "a"), file("/dup.jpg", "x"), file("/n.jpg", "")]
        let shownID = PhotoPaging.itemID(file("/b.jpg", "b"))
        let page = PhotoPaging.newer(files: files, oldestFirst: true, shownIDs: [shownID], shownHashes: ["x", ""])
        guard case .files(let got) = page else {
            Issue.record("not files")
            return
        }
        // b is shown already (by id), dup's content is (by hash: its oldest
        // copy moved); one without a hash isn't matched by content.
        #expect(got.map(\.path) == ["/n.jpg", "/a.jpg", "/c.jpg"])
        #expect(PhotoPaging.newer(files: [], oldestFirst: true, shownIDs: [], shownHashes: []) == .files([]))
    }

    @Test func eitherSearchsPageLeavesOutWhatIsShownByIdOrContent() {
        let files = [file("/a.jpg", "a"), file("/b.jpg", "b"), file("/moved.jpg", "x"), file("/n.jpg", ""), file("/a2.jpg", "a")]
        let shownID = PhotoPaging.itemID(file("/b.jpg", "b"))
        // b by id, moved by content (shown at another path), a2 twice from
        // the page; one without a hash isn't matched by content.
        #expect(PhotoPaging.notShown(files, shownIDs: [shownID], shownHashes: ["x"]) == [0, 3])
        #expect(PhotoPaging.notShown([], shownIDs: [], shownHashes: []) == [])
    }

    @Test func theEntriesShownByTheOtherSearchStillCountInThisOnesHave() {
        let files = [file("/a.jpg", "a"), file("/b.jpg", "b"), file("/c.jpg", "c")]
        // b came before in this search (a page served again): not counted
        // again; of a and c one was added, the other shows already.
        let own: Set<String> = [PhotoPaging.itemID(files[1])]
        #expect(PhotoPaging.hidden(files, own: own, added: 1) == 1)
        #expect(PhotoPaging.hidden(files, own: own, added: 2) == 0)
        #expect(PhotoPaging.hidden(files, own: [], added: 3) == 0)
        // A page served again, whole: nothing.
        #expect(PhotoPaging.hidden(files, own: Set(files.map(PhotoPaging.itemID)), added: 0) == 0)
    }

    /// Issue #194: the upward search starts on the first scroll up after a
    /// jump, however it is scrolled; not on the jump's own scroll down to
    /// the grid, nor on layout jitter.
    @Test func scrollingUpIsAnyOffsetGoingUp() {
        #expect(PhotoPaging.scrolledUp(from: 400, to: 380))
        #expect(PhotoPaging.scrolledUp(from: 400, to: 0))
        #expect(PhotoPaging.scrolledUp(from: 0, to: -40))
        #expect(!PhotoPaging.scrolledUp(from: 380, to: 400))
        #expect(!PhotoPaging.scrolledUp(from: 400, to: 400))
        #expect(!PhotoPaging.scrolledUp(from: 400, to: 399.6))
    }

    @Test func theTopIsReachedOnTheLastPageOrAnEmptyOne() {
        #expect(PhotoPaging.newerEnded(token: "", files: 3))
        #expect(PhotoPaging.newerEnded(token: "t", files: 0))
        #expect(!PhotoPaging.newerEnded(token: "t", files: 3))
    }
}

/// The grid's rows as photos are put above it (PhotoGridLayout).
struct NewerLayoutTests {
    private let phone = PhotoGridMetrics(width: 402, windowWidth: 402)

    private func months(_ spec: [(String?, Int)]) -> [String?] {
        spec.flatMap { Array(repeating: $0.0, count: $0.1) }
    }

    private func ids(_ n: Int) -> [String] { (0..<n).map { "p\($0)" } }

    @Test func rowsBelowKeepTheirNamesAndContentWhenPhotosArePutAbove() {
        // The jump's month (7) and an older one (2); then a page above: a
        // newer month of 4 and 2 short months.
        let below = months([("2026-03", 7), ("2026-02", 2)])
        let a = PhotoGridLayout.build(months: below, ids: ids(below.count), dated: true, lastRoom: nil, metrics: phone)
        let above = months([("2026-06", 4), ("2026-05", 1), ("2026-04", 1)])
        let all = above + below
        let b = PhotoGridLayout.build(months: all, ids: ids(all.count), dated: true, lastRoom: nil,
                                      breaks: ["2026-03"], metrics: phone)
        let shift = above.count
        for row in a.rows {
            guard let same = b.rows.first(where: { $0.id == row.id }) else {
                Issue.record("row \(row.id) is gone")
                continue
            }
            #expect(same.first == row.first + shift)
            switch (row.kind, same.kind) {
            case (.title(let m1), .title(let m2)): #expect(m1 == m2)
            case (.tiles(let r1), .tiles(let r2)): #expect(r2 == (r1.lowerBound + shift)..<(r1.upperBound + shift))
            case (.line(let p1), .line(let p2)): #expect(p1.map(\.month) == p2.map(\.month))
            default: Issue.record("row \(row.id) changed kind")
            }
        }
    }

    @Test func aShortJumpedMonthDoesNotJoinTheMonthsPutAbove() {
        // March alone shares a line with February; April, put above, would
        // have joined it - the break keeps March's line as it was.
        let below = months([("2026-03", 1), ("2026-02", 1)])
        let all = months([("2026-04", 1)]) + below
        let b = PhotoGridLayout.build(months: all, ids: ids(all.count), dated: true, lastRoom: nil,
                                      breaks: ["2026-03"], metrics: phone)
        #expect(b.rows.map(\.id) == ["l2026-04", "l2026-03"])
        let plain = PhotoGridLayout.build(months: all, ids: ids(all.count), dated: true, lastRoom: nil, metrics: phone)
        #expect(plain.rows.map(\.id) == ["l2026-04"])
    }

    @Test func theTopMonthKeepsGreyTilesForItsNewerPhotos() {
        // Two of May's 8 photos are in, and more are on their way: six grey
        // tiles before them (indices -6..<0), filled in place as they come.
        let m = months([("2026-05", 2), ("2026-04", 3)])
        let l = PhotoGridLayout.build(months: m, ids: ids(m.count), dated: true, lastRoom: nil, firstRoom: 8, metrics: phone)
        #expect(l.firstSlot == -6)
        #expect(l.rows.map(\.id) == ["h2026-05", "t2026-05@0", "t2026-05@3", "t2026-05@6", "h2026-04", "t2026-04@0"])
        #expect(l.rows[1].kind == .tiles(-6 ..< -3))
        #expect(l.rows[3].kind == .tiles(0..<2))
        // Three more came: the same rows, the photos where the grey was.
        let more = months([("2026-05", 5), ("2026-04", 3)])
        let l2 = PhotoGridLayout.build(months: more, ids: ids(more.count), dated: true, lastRoom: nil, firstRoom: 8, metrics: phone)
        #expect(l2.rows.map(\.id) == l.rows.map(\.id))
        #expect(l2.firstSlot == -3)
        #expect(l2.rows[3].kind == .tiles(3..<5))
        // The scroll memo's photo of the grey rows is the first one.
        #expect(l.topItem(ofRows: ["t2026-05@0"]) == 0)
        #expect(l.topItem(ofRows: ["t2026-04@0"]) == 2)
        #expect(l.topItem(ofRows: ["nope"]) == nil)
    }
}

/// Where the row at the top of the screen goes back to once photos are put
/// above it (GridAnchor): scrollTo lines its anchor up within the whole
/// scroll view, and the grid's first row moves up by the room it gains.
@MainActor
struct GridAnchorTests {
    @Test func theRowGoesBackWhereItWas() {
        // A row 129 tall, 78 above the top of a 675-tall scroll view: the
        // point 78/546 up its height lines up with the same point of the view.
        let p = GridAnchor.unitPoint(y: -78, rowHeight: 129, height: 675)
        #expect(abs(p.y - (-78.0 / 546.0)) < 1e-9)
        #expect(abs(p.y * (675 - 129) - (-78)) < 1e-6)
        #expect(GridAnchor.unitPoint(y: 0, rowHeight: 40, height: 675) == UnitPoint(x: 0, y: 0))
        // A row as tall as the view: its top.
        #expect(GridAnchor.unitPoint(y: -5, rowHeight: 675, height: 675) == .top)
    }

    @Test func theTopRowOnTheScreenIsTaken() {
        let a = GridAnchor()
        a.viewport = GridAnchor.Viewport(height: 675, top: 0, bottom: 83)
        // Above the screen, the top one (partly), one under it, one only
        // under the tab bar's inset.
        a.record("above", CGRect(x: 0, y: -300, width: 380, height: 129))
        a.record("top", CGRect(x: 0, y: -60, width: 380, height: 129))
        a.record("next", CGRect(x: 0, y: 71, width: 380, height: 129))
        a.record("under", CGRect(x: 0, y: 600, width: 380, height: 129))
        a.take(first: nil, grows: 6)
        let kept = a.taken()
        #expect(kept?.id == "top")
        #expect(abs((kept?.unitPoint.y ?? 0) - (-60.0 / (675 - 129))) < 1e-9)
        // Once.
        #expect(a.taken() == nil)
        // Gone from the screen: the next one.
        a.gone("top")
        a.take(first: nil, grows: 6)
        #expect(a.taken()?.id == "next")
    }

    @Test func theFirstRowGoesUpByTheRoomItGains() {
        let a = GridAnchor()
        a.viewport = GridAnchor.Viewport(height: 675, top: 0, bottom: 83)
        a.record("h2024-11", CGRect(x: 0, y: 0, width: 386, height: 32))
        a.take(first: "h2024-11", grows: 6)
        let kept = a.taken()
        // 6 higher, 6 taller: its title stays where it was.
        #expect(abs((kept?.unitPoint.y ?? 0) - (-6.0 / (675 - 38))) < 1e-9)
    }
}

/// The device for the newer-photos tests: a library of `count` photos one
/// day apart (photo 0 the newest, 28 May 2026; 28 to 57 in April), each
/// its own content, split by the cutoff as the device does, in pages of
/// `page` photos. A token goes on from `have`, as the device's does for a
/// client that counts right.
@MainActor
final class NewerDevice {
    var searches: [Msg_SearchPhotos] = []
    var deletes: [Msg_DelFile] = []
    var library: [Msg_File] = []
    /// Upward pages answered as a device before #194 (newest first,
    /// oldest_first false).
    var old = false
    /// Upward answers that fail before one works.
    var upFailures = 0
    var page = 20
    /// The jump's cutoff, which the downward token remembers.
    private var downCutoff: Date?
    /// The upward pages held until release().
    var hold = false
    /// How a delete is answered (by its path): refused with this code and
    /// message, nothing deleted; ok when not set.
    var refuse: [String: (code: String, message: String)] = [:]
    /// How often the months' counts were asked for.
    var bucketAsks = 0
    private var held: [CheckedContinuation<Void, Never>] = []

    init(count: Int = 70) {
        let base = Date(timeIntervalSince1970: 1_780_000_000)
        library = (0..<count).map { i in
            var f = Msg_File()
            f.path = "/photos/\(i).jpg"
            f.hash = "h\(i)"
            f.mime = "image/jpeg"
            f.copies = 1
            f.created = Google_Protobuf_Timestamp(date: base - Double(i) * 86_400)
            return f
        }
    }

    func release() {
        let h = held
        held = []
        h.forEach { $0.resume() }
    }

    private func answer(_ files: [Msg_File], token: String, oldestFirst: Bool) -> Msg_RespEnvelope {
        var lof = Msg_ListOfFiles()
        lof.files = files
        lof.token = token
        lof.oldestFirst = oldestFirst
        var r = Msg_RespEnvelope()
        r.payload = .respListOfFiles(lof)
        return r
    }

    func search(_ sp: Msg_SearchPhotos) async throws -> Msg_RespEnvelope {
        searches.append(sp)
        if sp.hasNewerThan && !old {
            if hold { await withCheckedContinuation { held.append($0) } }
            if upFailures > 0 {
                upFailures -= 1
                throw WSClient.TimedOut()
            }
            let cut = sp.newerThan.date
            let up = Array(library.filter { $0.created.date > cut }.reversed())
            let from = sp.token.isEmpty ? 0 : Int(sp.have)
            let slice = Array(up.dropFirst(from).prefix(page))
            return answer(slice, token: from + slice.count < up.count ? "up" : "", oldestFirst: true)
        }
        if sp.token.isEmpty { downCutoff = sp.hasBefore ? sp.before.date : nil }
        let down = downCutoff.map { cut in library.filter { $0.created.date <= cut } } ?? library
        let from = sp.token.isEmpty ? 0 : Int(sp.have)
        let slice = Array(down.dropFirst(from).prefix(page))
        return answer(slice, token: from + slice.count < down.count ? "down" : "", oldestFirst: false)
    }

    func attach(_ vm: PhotoGalleryVM, support: DeviceSupport) {
        vm.support = support
        vm.pageRetry.delay = { _ in 0.05 }
        vm.upRetry.delay = { _ in 0.05 }
        vm.bucketRetry.delay = { _ in 0.05 }
        vm.problemOf = { LoadProblem.of(resp: $0, error: $1, offline: false, statusCode: nil) }
        vm.photoRequest = { [weak self] payload, _ in
            guard let self else { throw CancellationError() }
            switch payload {
            case .reqSearchPhotos(let sp):
                return try await self.search(sp)
            case .reqDelFile(let d):
                self.deletes.append(d)
                var r = Msg_RespEnvelope()
                if let no = self.refuse[d.path] {
                    r.error = true
                    r.errorCode = no.code
                    r.errorMessage = no.message
                    return r
                }
                self.library.removeAll { $0.path == d.path }
                var a = Msg_Ack()
                a.ok = true
                r.payload = .respAck(a)
                return r
            case .reqPhotoDateBuckets:
                self.bucketAsks += 1
                var r = Msg_RespEnvelope()
                r.payload = .respPhotoDateBuckets(Msg_RespPhotoDateBuckets())
                return r
            default:
                throw CancellationError()
            }
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
struct NewerPhotosGalleryTests {
    /// A grid jumped to the month of photo `at` (its whole month and
    /// older), on `device`, then scrolled up (`armed`) as the view tells it.
    private func jumped(_ device: NewerDevice, at: Int = 35, support: DeviceSupport? = nil, armed: Bool = true) async -> PhotoGalleryVM {
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        device.attach(vm, support: support ?? DeviceSupport())
        await jump(vm, device, at: at, armed: armed)
        return vm
    }

    /// Jumps to the month of photo `at`, waits for its first page, and
    /// (`armed`) scrolls up.
    private func jump(_ vm: PhotoGalleryVM, _ device: NewerDevice, at: Int, armed: Bool = true) async {
        let gen = vm.searchGeneration
        vm.jumpToDate(PhotoMonths.key(for: device.library[at].created.date)!)
        await waitUntil { vm.searchGeneration != gen && !vm.items.isEmpty && !vm.loading && vm.placeholderCount == nil }
        if armed { vm.armNewer() }
    }

    private func ups(_ device: NewerDevice) -> [Msg_SearchPhotos] { device.searches.filter(\.hasNewerThan) }

    /// The tile at `i` came on screen.
    private func shown(_ vm: PhotoGalleryVM, _ i: Int) async {
        await vm.loadMoreIfNeeded(index: i, id: vm.items[i].id)
    }

    @Test func nothingIsAskedAtTheJumpOnlyOnceScrolledUpNearTheTop() async {
        let device = NewerDevice()
        let vm = await jumped(device, armed: false)
        #expect(ups(device).isEmpty)
        #expect(vm.jumpBefore != nil)
        #expect(!vm.upEnd)
        // The jump lands at the top: its first tiles show, nothing is asked.
        await shown(vm, 0)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(ups(device).isEmpty)
        #expect(!vm.upArmed)
        // Scrolled up: a tile further down still asks nothing upward.
        vm.armNewer()
        await shown(vm, 12)
        #expect(ups(device).isEmpty)
        await shown(vm, 0)
        await waitUntil { vm.items.contains(where: \.up) }
        let up = ups(device)
        #expect(up.count == 1)
        // The jump's own filters and instant, the first page's size, no
        // token, nothing held, never `before`.
        #expect(up[0].newerThan.date == vm.jumpBefore)
        #expect(!up[0].hasBefore)
        #expect(up[0].token.isEmpty && up[0].have == 0)
        #expect(up[0].limit == cFirstPhotoPageLimit)
        #expect(up[0].includeVideos && up[0].omitThumbnails && up[0].smallThumbnails)
    }

    @Test func newerPhotosGoAboveNewestFirstAndEachSearchCountsItsOwn() async {
        let device = NewerDevice()
        let vm = await jumped(device)
        let firstBefore = vm.items[0].path
        let downCount = vm.items.count
        await shown(vm, 0)
        await waitUntil { vm.items.count == downCount + device.page }
        // Newest first above what was there.
        let upPaths = vm.items.prefix(device.page).map(\.path)
        let numbers = upPaths.map { Int($0.dropFirst(8).dropLast(4))! }
        #expect(numbers == numbers.sorted())
        #expect(vm.items[device.page].path == firstBefore)
        #expect(vm.items.prefix(device.page).allSatisfy { $0.up })
        // The next upward page: its token and what it brought.
        await shown(vm, 0)
        await waitUntil { ups(device).count == 2 }
        let second = ups(device)[1]
        #expect(second.token == "up")
        #expect(second.have == Int32(device.page))
        #expect(second.newerThan.date == vm.jumpBefore)
        // Further down, the downward search's own token and count.
        await waitUntil { !vm.upLoading }
        let last = vm.items.count - 1
        await shown(vm, last)
        await waitUntil { device.searches.filter { !$0.hasNewerThan }.count == 2 }
        let down = device.searches.filter { !$0.hasNewerThan }[1]
        #expect(down.token == "down")
        #expect(down.have == Int32(downCount))
        #expect(!down.hasNewerThan && !down.hasBefore)
    }

    @Test func aDeleteLowersTheCountOfTheSearchThatBroughtIt() async {
        let device = NewerDevice()
        let vm = await jumped(device)
        await shown(vm, 0)
        await waitUntil { vm.items.contains(where: \.up) }
        // One of each.
        vm.selected = [vm.items[0].path, vm.items[device.page].path]
        vm.deleteSelected()
        await waitUntil { device.deletes.count == 2 }
        await waitUntil { vm.items.count == 2 * device.page - 2 }
        await shown(vm, 0)
        await waitUntil { ups(device).count == 2 }
        #expect(ups(device)[1].have == Int32(device.page - 1))
        await waitUntil { !vm.upLoading }
        await shown(vm, vm.items.count - 1)
        await waitUntil { device.searches.filter { !$0.hasNewerThan }.count == 2 }
        #expect(device.searches.filter { !$0.hasNewerThan }[1].have == Int32(device.page - 1))
    }

    @Test func theTopIsReachedAndNothingMoreIsAsked() async {
        let device = NewerDevice()
        device.page = 30
        // April: the 28 photos of May above it, in one page.
        let vm = await jumped(device, at: 35)
        await shown(vm, 0)
        await waitUntil { vm.upEnd }
        #expect(vm.items.first?.path == "/photos/0.jpg")
        let asked = ups(device).count
        await shown(vm, 0)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(ups(device).count == asked)
    }

    @Test func anOlderDeviceIsAskedOnceThenNotUntilTheNextSignIn() async {
        let device = NewerDevice()
        device.old = true
        let support = DeviceSupport()
        let vm = await jumped(device, support: support)
        let before = vm.items.map(\.path)
        await shown(vm, 0)
        await waitUntil { vm.upEnd }
        // Its newest photos, dropped.
        #expect(vm.items.map(\.path) == before)
        #expect(!support.newerThan)
        // Another jump: nothing upward.
        await jump(vm, device, at: 60)
        #expect(vm.upEnd)
        let asked = ups(device).count
        await shown(vm, 0)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(ups(device).count == asked)
    }

    @Test func aFailedPageIsAskedAgainWithTheSameTokenAndCount() async {
        let device = NewerDevice()
        device.upFailures = 1
        let vm = await jumped(device)
        vm.upRetry.delay = { _ in 0.2 }
        await shown(vm, 0)
        await waitUntil { vm.upProblem != nil }
        #expect(vm.upProblem == .slow)
        await waitUntil { vm.items.contains(where: \.up) }
        let up = ups(device)
        #expect(up.count == 2)
        #expect(up[0].token == up[1].token && up[0].have == up[1].have)
        #expect(vm.upProblem == nil)
    }

    @Test func aPlainSearchHasNoUpwardCursor() async {
        let device = NewerDevice()
        let vm = PhotoGalleryVM(deviceID: "test", localPhotosFolder: nil, thumbCache: testThumbCache())
        device.attach(vm, support: DeviceSupport())
        await vm.resetAndLoadFirstPage()
        #expect(vm.upEnd)
        vm.armNewer()
        #expect(!vm.upArmed)
        await shown(vm, 0)
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(ups(device).isEmpty)
    }

    @Test func theOpenPhotoStaysOpenAsPhotosArePutAbove() async {
        let device = NewerDevice()
        let vm = await jumped(device)
        vm.open(index: 2)
        let open = vm.openPath
        device.hold = true
        await shown(vm, 0)
        await waitUntil { ups(device).count == 1 }
        device.release()
        await waitUntil { vm.items.contains(where: \.up) }
        #expect(vm.openPath == open)
        #expect(vm.openIndex == 2 + device.page)
        vm.closeModal()
    }

    @Test func aPhotoShownAboveIsLeftOutBelowAndStillCounted() async {
        let device = NewerDevice()
        // One content at two paths: photo 20 (above the April jump, in the
        // first upward page) and photo 50 (in April's second page) - its
        // oldest copy moved between the two searches.
        device.library[50].hash = device.library[20].hash
        let vm = await jumped(device)
        await shown(vm, 0)
        await waitUntil { vm.items.contains(where: \.up) && !vm.upLoading }
        #expect(vm.items.contains { $0.path == "/photos/20.jpg" })
        let downs = { device.searches.filter { !$0.hasNewerThan } }
        await shown(vm, vm.items.count - 1)
        await waitUntil { downs().count == 2 && !vm.loading }
        // Shown once, above.
        #expect(!vm.items.contains { $0.path == "/photos/50.jpg" })
        #expect(vm.items.filter { $0.hash == device.library[20].hash }.count == 1)
        // The next page down: `have` counts what the device served, the
        // one left out too - no page served again, nothing skipped.
        await shown(vm, vm.items.count - 1)
        await waitUntil { downs().count == 3 && !vm.loading }
        #expect(downs()[2].have == Int32(2 * device.page))
        #expect(vm.items.contains { $0.path == "/photos/69.jpg" })
        #expect(Set(vm.items.map(\.id)).count == vm.items.count)
    }

    @Test func aPhotoShownBelowIsLeftOutAboveAndStillCounted() async {
        let device = NewerDevice()
        // Photo 10 (the first upward page) holds photo 30's content (shown
        // by the jump's first page).
        device.library[10].hash = device.library[30].hash
        let vm = await jumped(device)
        await shown(vm, 0)
        await waitUntil { vm.items.contains(where: \.up) && !vm.upLoading }
        #expect(!vm.items.contains { $0.path == "/photos/10.jpg" })
        await shown(vm, 0)
        await waitUntil { ups(device).count == 2 && !vm.upLoading }
        #expect(ups(device)[1].have == Int32(device.page))
        await waitUntil { vm.upEnd }
        #expect(vm.items.first?.path == "/photos/0.jpg")
    }

    @Test func twoChangedAfterAJumpAreSummedUpStayPickedAndTheJumpIsReadAgain() async {
        let device = NewerDevice()
        let vm = await jumped(device)
        let cutoff = vm.jumpBefore
        #expect(cutoff != nil)
        let a = vm.items[1].path, b = vm.items[4].path, c = vm.items[6].path
        device.refuse[a] = ("changed", "this file has changed since it was shown")
        device.refuse[b] = ("changed", "this file has changed since it was shown")
        vm.selected = [a, b, c]
        let searches = device.searches.count
        let buckets = device.bucketAsks
        vm.deleteSelected()
        await waitUntil { vm.showAlert && device.searches.count > searches }
        #expect(vm.notice?.text == S.appPhotosNoticeDeleteChanged(count: 2))
        // Read again at the jump: its cutoff, no token, nothing held.
        let again = device.searches[searches]
        #expect(again.hasBefore && again.before.date == cutoff)
        #expect(again.token.isEmpty && again.have == 0 && !again.hasNewerThan)
        await waitUntil { !vm.loading && !vm.items.isEmpty && vm.placeholderCount == nil }
        #expect(vm.jumpBefore == cutoff)
        #expect(!vm.upEnd)
        // The refused photos stay picked, on the grid again.
        #expect(vm.selected == [a, b])
        #expect(vm.items.contains { $0.path == a } && vm.items.contains { $0.path == b })
        #expect(!vm.items.contains { $0.path == c })
        // One was deleted: the months' counts follow.
        await waitUntil { device.bucketAsks > buckets }
        #expect(device.bucketAsks > buckets)
        // Deleted again once seen: with the content shown now.
        device.refuse = [:]
        vm.deleteSelected()
        await waitUntil { device.deletes.count == 5 }
        #expect(device.deletes.suffix(2).allSatisfy { $0.hash.hasPrefix("h") })
    }

    @Test func aPickLeftBehindByAJumpIsDeletedWithItsContent() async {
        let device = NewerDevice()
        let vm = await jumped(device, at: 35)
        let picked = vm.items[2]
        vm.selected = [picked.path]
        await jump(vm, device, at: 65)
        #expect(!vm.items.contains { $0.path == picked.path })
        vm.askDeleteSelected()
        #expect(vm.deleteAsk == .plain)
        vm.deleteSelected()
        await waitUntil { device.deletes.count == 1 }
        #expect(device.deletes[0].path == picked.path)
        #expect(device.deletes[0].hash == picked.hash)
        await waitUntil { vm.selected.isEmpty }
        #expect(vm.selected.isEmpty)
    }

    @Test func aNewJumpStartsBothCursorsAgain() async {
        let device = NewerDevice()
        let vm = await jumped(device)
        await shown(vm, 0)
        await waitUntil { vm.items.contains(where: \.up) }
        await jump(vm, device, at: 60)
        #expect(!vm.items.contains { $0.up })
        await shown(vm, 0)
        await waitUntil { ups(device).count == 2 }
        #expect(ups(device)[1].token.isEmpty && ups(device)[1].have == 0)
        #expect(ups(device)[1].newerThan.date == vm.jumpBefore)
    }
}
}
