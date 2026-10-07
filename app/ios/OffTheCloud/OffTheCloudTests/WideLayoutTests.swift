// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// The wide layout (AppMenu.swift): where it starts, the menu's sections,
/// and the storage at its foot, as the web's App.tsx and Sidebar.tsx.
struct WideLayoutTests {
    @Test func widthsAreTheWebs() {
        #expect(WideLayout.menu(width: 402, open: true) == .none)
        #expect(WideLayout.menu(width: 599.5, open: true) == .none)
        #expect(WideLayout.menu(width: 600, open: true) == .rail)
        // A medium window always has the rail, whatever was chosen.
        #expect(WideLayout.menu(width: 874, open: true) == .rail)
        #expect(WideLayout.menu(width: 1023, open: false) == .rail)
        #expect(WideLayout.menu(width: 1024, open: true) == .full)
        #expect(WideLayout.menu(width: 1024, open: false) == .rail)
        #expect(WideLayout.menu(width: 1376, open: true) == .full)
    }

    @Test func sectionsGroupedAsTheWebsMenu() {
        let on = AppSection.menu(faces: true)
        #expect(on.map(\.heading) == [nil, "Sharing", "Device"])
        #expect(on.map(\.items) == [[.images, .people, .collections, .files], [.social, .friends], [.alerts, .settings]])
        // People only while face recognition is on.
        let off = AppSection.menu(faces: false)
        #expect(off[0].items == [.images, .collections, .files])
        #expect(!off.flatMap(\.items).contains(.people))
    }

    @Test func eachSectionHasItsTab() {
        for s in AppSection.allCases where !s.isPage {
            #expect(AppSection.forTab(s.tab) == s)
        }
        // The pages open over the tab whose sheet they are on a narrow window.
        #expect(AppSection.people.tab == AppSection.images.tab)
        #expect(AppSection.collections.tab == AppSection.images.tab)
        #expect(AppSection.friends.tab == AppSection.social.tab)
        #expect(AppSection.allCases.filter(\.isPage) == [.people, .collections, .friends])
        #expect(AppSection.allCases.map(\.label) == ["Images", "People", "Collections", "Files", "Social", "Friends", "Alerts", "Settings"])
    }
}

struct StorageSummaryTests {
    private func status(used: Int32, size: Int32, state: Msg_RaidState = .raidInSync) -> Msg_Status {
        var s = Msg_Status()
        s.raidUsage = used
        s.raidSize = size
        s.raidState = state
        s.disks = 2
        s.diskUsage = 6300
        s.diskSize = 123_000
        s.cpuUsagePrc = 0.25
        s.memUsage = 1800
        s.memSize = 8300
        return s
    }

    @Test func readingThenUnavailable() {
        let reading = StorageSummary(status: nil, error: nil)
        #expect(!reading.known)
        #expect(reading.usedText == "Reading…")
        #expect(reading.gaugeText == "–")
        #expect(reading.mirrorText.isEmpty)
        #expect(StorageSummary(status: nil, error: "gone").usedText == "Storage unavailable right now")
    }

    @Test func usedAsTheWebWordsIt() {
        let s = StorageSummary(status: status(used: 2800, size: 474_000), error: nil)
        #expect(s.known)
        #expect(s.usedPct == 0.59)
        #expect(s.gaugeText == "1%")
        #expect(s.level == .ok)
        #expect(s.usedText == "2.8 GB of 474 GB used")
        #expect(s.mirrorText == "Mirror in sync")
        #expect(!s.attention)
        #expect(s.details == [
            .init(label: "Disks", value: "2"),
            .init(label: "Free", value: "471 GB"),
            .init(label: "System card", value: "6.3 GB of 123 GB"),
            .init(label: "CPU", value: "0.25%"),
            .init(label: "Memory", value: "1.8 GB of 8.3 GB"),
        ])
    }

    @Test func fullerTurnsEmberThenRed() {
        #expect(StorageSummary(status: status(used: 69, size: 100), error: nil).level == .ok)
        #expect(StorageSummary(status: status(used: 70, size: 100), error: nil).level == .warn)
        #expect(StorageSummary(status: status(used: 90, size: 100), error: nil).level == .crit)
        // Nothing to divide by: empty, not a crash.
        #expect(StorageSummary(status: status(used: 5, size: 0), error: nil).usedPct == 0)
    }

    @Test func aDegradedMirrorOrAnErrorNeedsAttention() {
        let degraded = StorageSummary(status: status(used: 1, size: 100, state: .raidDegraded), error: nil)
        #expect(degraded.degraded && degraded.attention)
        #expect(degraded.mirrorText == "Mirror degraded")

        var withError = status(used: 1, size: 100)
        var e = Msg_StatusErrors()
        e.message = "Disk sdb is failing"
        withError.errors = [e]
        let s = StorageSummary(status: withError, error: nil)
        #expect(s.attention && !s.degraded)
        #expect(s.errors == ["Disk sdb is failing"])

        var syncing = status(used: 1, size: 100, state: .raidSyncing)
        syncing.raidSyncPercent = 42.5
        #expect(StorageSummary(status: syncing, error: nil).mirrorText == "Mirror syncing (42.5%)")
        #expect(StorageSummary(status: status(used: 1, size: 100, state: .raidNone), error: nil).mirrorText == "No mirror")
    }
}

/// What the search does from either field (TopSearch.swift).
@MainActor
struct TopSearchActionsTests {
    private func actions(_ vm: PhotoGalleryVM, _ search: TopSearchModel, shown: @escaping () -> Void = {}, focus: @escaping (Bool) -> Void = { _ in }) -> TopSearchActions {
        TopSearchActions(vm: vm, search: search, faces: true, filesNav: FilesNav.shared, focus: focus, showPhotos: shown)
    }

    @Test func pickingATagNarrowsImagesAndShowsIt() {
        let vm = PhotoGalleryVM()
        let search = TopSearchModel()
        search.query = "do"
        search.open = true
        var shown = 0
        var focused: Bool?
        actions(vm, search, shown: { shown += 1 }, focus: { focused = $0 }).pick(.tag("dog", span: nil))
        #expect(vm.chips == ["dog"])
        #expect(shown == 1)
        #expect(search.query.isEmpty)
        #expect(!search.open)
        #expect(focused == false)
    }

    @Test func pickingAPersonAddsThem() {
        let vm = PhotoGalleryVM()
        var p = Msg_Person()
        p.id = "p1"
        p.name = "Ana"
        var shown = 0
        actions(vm, TopSearchModel(), shown: { shown += 1 }).pick(.person(p, label: "Ana", span: nil))
        #expect(vm.selectedPeople == ["p1"])
        #expect(shown == 1)
    }

    @Test func cancelAndClear() {
        let vm = PhotoGalleryVM()
        vm.chips = ["dog"]
        let search = TopSearchModel()
        search.query = "ca"
        search.open = true
        let a = actions(vm, search)
        a.cancel()
        #expect(search.query.isEmpty && !search.open)
        #expect(vm.chips == ["dog"])
        search.query = "x"
        a.clear()
        #expect(search.query.isEmpty)
        #expect(vm.chips.isEmpty)
    }

    @Test func imagesFromTheMenuIsTheWholeLibrary() {
        let vm = PhotoGalleryVM()
        vm.chips = ["dog"]
        vm.selectedPeople = ["x"]
        var g = Msg_ImageGroup()
        g.id = "g"
        vm.activeGroup = g
        vm.showAll()
        #expect(vm.chips.isEmpty && vm.selectedPeople.isEmpty && vm.activeGroup == nil)
    }
}
