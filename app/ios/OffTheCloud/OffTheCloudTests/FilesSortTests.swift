// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Issue #196: Files' order (FilesSort.swift). The same cases as the web's
/// files-sort-test.mjs and Android's FilesSortTest.kt: change all three
/// together.
@MainActor
struct FilesSortTests {
    private let en = Locale(identifier: "en")

    private func entry(_ path: String, dir: Bool = false, size: Int64 = 0, created: Double? = nil, modified: Double? = nil) -> FileRow {
        var f = Msg_File()
        f.path = path
        f.mime = dir ? "inode/directory" : path.hasSuffix(".jpg") ? "image/jpeg" : "text/plain"
        f.size64 = size
        f.size = Int32(truncatingIfNeeded: size)
        if let created { f.created = .init(date: Date(timeIntervalSince1970: created)) }
        if let modified { f.modified = .init(date: Date(timeIntervalSince1970: modified)) }
        return FileRow(file: f)
    }

    /// A folder's listing: "..", two folders, and files whose sizes and
    /// dates tie in places, one without a creation date, one without a
    /// modification date, and two names that differ only by an accent.
    private var listing: [FileRow] {
        [
            entry("/d/IMG_10.jpg", size: 300, created: 3000, modified: 1000),
            entry("/d/é.txt", size: 50, modified: 500),
            entry("/d/Zeta", dir: true),
            entry("/d/apple.txt", size: 200, created: 2000, modified: 2000),
            entry("..", dir: true),
            entry("/d/IMG_2.jpg", size: 100, created: 4000, modified: 3000),
            entry("/d/Banana.txt", size: 200, created: 2000),
            entry("/d/alpha", dir: true),
            entry("/d/e.txt", size: 50, modified: 500),
        ]
    }

    private func names(_ key: FilesSortKey, ascending: Bool, _ rows: [FileRow]? = nil) -> [String] {
        FilesSort.sorted(rows ?? listing, by: FilesSortOrder(key: key, ascending: ascending), locale: en).map(\.name)
    }

    @Test func nameAToZCountsNumbersAndIgnoresCaseAndAccentsWithFoldersFirst() {
        #expect(names(.name, ascending: true) == ["..", "alpha", "Zeta", "apple.txt", "Banana.txt", "e.txt", "é.txt", "IMG_2.jpg", "IMG_10.jpg"])
    }

    @Test func nameZToA() {
        #expect(names(.name, ascending: false) == ["..", "Zeta", "alpha", "IMG_10.jpg", "IMG_2.jpg", "é.txt", "e.txt", "Banana.txt", "apple.txt"])
    }

    @Test func sizeKeepsFoldersByNameAboveTheFilesAndBreaksTiesByNameThenPath() {
        #expect(names(.size, ascending: false) == ["..", "alpha", "Zeta", "IMG_10.jpg", "apple.txt", "Banana.txt", "IMG_2.jpg", "e.txt", "é.txt"])
        #expect(names(.size, ascending: true) == ["..", "alpha", "Zeta", "e.txt", "é.txt", "IMG_2.jpg", "apple.txt", "Banana.txt", "IMG_10.jpg"])
    }

    @Test func createdPutsAMissingDateLastEitherWay() {
        #expect(names(.created, ascending: false) == ["..", "alpha", "Zeta", "IMG_2.jpg", "IMG_10.jpg", "apple.txt", "Banana.txt", "e.txt", "é.txt"])
        #expect(names(.created, ascending: true) == ["..", "alpha", "Zeta", "apple.txt", "Banana.txt", "IMG_10.jpg", "IMG_2.jpg", "e.txt", "é.txt"])
    }

    @Test func modifiedPutsAMissingDateLastEitherWay() {
        #expect(names(.modified, ascending: false) == ["..", "alpha", "Zeta", "IMG_2.jpg", "apple.txt", "IMG_10.jpg", "e.txt", "é.txt", "Banana.txt"])
        #expect(names(.modified, ascending: true) == ["..", "alpha", "Zeta", "e.txt", "é.txt", "IMG_10.jpg", "apple.txt", "IMG_2.jpg", "Banana.txt"])
    }

    @Test func theOrderTheDeviceSentDoesntMatter() {
        for key in FilesSortKey.allCases {
            for ascending in [true, false] {
                #expect(names(key, ascending: ascending, listing.reversed()) == names(key, ascending: ascending), "\(key) \(ascending)")
            }
        }
    }

    @Test func searchResultsGoByTheirNames() {
        var docs = Msg_File()
        docs.path = "/c/Docs"
        docs.mime = "inode/directory"
        let files = ["/b/file10", "/a/file2", "/a/file2.bak"].map { p -> Msg_File in
            var f = Msg_File()
            f.path = p
            f.mime = "text/plain"
            return f
        }
        let found = FilesSort.sorted([files[0], files[1], docs, files[2]], by: .default, locale: en)
        #expect(found.map(\.path) == ["/c/Docs", "/a/file2", "/a/file2.bak", "/b/file10"])
    }

    /// Names the three apps once put in different orders: a name ending
    /// where another goes on with a number, a space before the number,
    /// numbers inside names, case and accents (ties, which the paths
    /// decide), and digits that aren't ASCII - a fullwidth 5 is the number
    /// 5, a superscript 2 and a circled 5 aren't numbers at all (ICU's
    /// numeric collation, which all three use).
    private var tricky: [FileRow] {
        ["photo10.jpg", "IMG_10.jpg", "Photo 2.jpg", "é.txt", "photo.jpg", "10.txt", "img_2.jpg", "５.txt",
         "photo1.jpg", "E.txt", "IMG_2.jpg", "5.txt", "e.txt", "⑤.txt", "².txt", "2.txt"].map { entry("/d/\($0)") }
    }
    private let trickyAToZ = ["2.txt", "5.txt", "５.txt", "10.txt", "².txt", "⑤.txt", "E.txt", "e.txt", "é.txt",
                              "IMG_2.jpg", "img_2.jpg", "IMG_10.jpg", "Photo 2.jpg", "photo.jpg", "photo1.jpg", "photo10.jpg"]

    @Test func trickyNamesGoInTheSameOrderAsOnTheWebAndAndroid() {
        #expect(names(.name, ascending: true, tricky) == trickyAToZ)
        #expect(names(.name, ascending: false, tricky) == Array(trickyAToZ.reversed()))
    }

    @Test func trickyNamesGoInOneOrderWhicheverOrderTheyComeIn() {
        for from in [tricky, Array(tricky.reversed())] {
            for k in from.indices {
                #expect(names(.name, ascending: true, Array(from[k...] + from[..<k])) == trickyAToZ, "from \(k)")
            }
        }
    }

    @Test func namesFollowTheAppsLanguage() {
        let rows = [entry("/d/ä"), entry("/d/z"), entry("/d/a")]
        #expect(FilesSort.sorted(rows, by: .default, locale: Locale(identifier: "sv")).map(\.name) == ["a", "z", "ä"])
    }

    @Test func eachKeyStartsItsNaturalWayAndTheSameKeyAgainReverses() {
        #expect(FilesSortOrder.default == FilesSortOrder(key: .name, ascending: true))
        var o = FilesSortOrder.default.picking(.name)
        #expect(o == FilesSortOrder(key: .name, ascending: false))
        o = o.picking(.size)
        #expect(o == FilesSortOrder(key: .size, ascending: false))
        o = o.picking(.size)
        #expect(o == FilesSortOrder(key: .size, ascending: true))
        #expect(o.picking(.created) == FilesSortOrder(key: .created, ascending: false))
        #expect(o.picking(.modified) == FilesSortOrder(key: .modified, ascending: false))
    }

    @Test func theStoredOrderIsReadBackAndAnythingElseIsTheDefault() {
        for key in FilesSortKey.allCases {
            for ascending in [true, false] {
                let o = FilesSortOrder(key: key, ascending: ascending)
                #expect(FilesSortOrder(stored: o.stored) == o)
            }
        }
        #expect(FilesSortOrder(key: .size, ascending: false).stored == "size:desc")
        for bad in [nil, "", "garbage", "size", "name:up", "kind:asc", "SIZE:DESC", " size:desc", "size:desc:x"] as [String?] {
            #expect(FilesSortOrder(stored: bad) == .default, "\(bad ?? "nil")")
        }
    }

    @Test func theWordsSayTheOrder() {
        #expect(FilesSortText.name(.created) == "Date created")
        #expect(FilesSortText.direction(FilesSortOrder(key: .size, ascending: false)) == "Largest first")
        #expect(FilesSortText.direction(FilesSortOrder(key: .modified, ascending: true)) == "Oldest first")
        #expect(FilesSortText.sorted(.default) == "Sorted by Name, A to Z")
    }

    // MARK: The explorer

    /// A scratch place for the choice, as UserDefaults keeps it.
    private func scratchDefaults() -> UserDefaults {
        let name = "FilesSortTests-\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        d.removePersistentDomain(forName: name)
        return d
    }

    /// An explorer at /d/ whose listing is `files`.
    private func explorer(_ files: [Msg_File], defaults: UserDefaults) -> FilesExplorerViewModel {
        let vm = FilesExplorerViewModel(initialPath: "/d/", defaults: defaults)
        vm.thumbDisk = testThumbCache()
        vm.collationLocale = { Locale(identifier: "en") }
        vm.request = { payload in
            guard case .reqListFiles = payload else { throw CancellationError() }
            var lof = Msg_ListOfFiles()
            lof.files = files
            var r = Msg_RespEnvelope()
            r.payload = .respListOfFiles(lof)
            return r
        }
        return vm
    }

    @Test func theExplorerShowsTheListingInTheOrderKeptAndKeepsANewOne() async {
        let defaults = scratchDefaults()
        let vm = explorer(listing.filter { $0.path != ".." }.map(\.raw), defaults: defaults)
        #expect(vm.sort == .default)
        await vm.load()
        #expect(vm.rows.map(\.name) == names(.name, ascending: true))
        await vm.pickSort(.size)
        #expect(vm.rows.map(\.name) == names(.size, ascending: false))
        #expect(defaults.string(forKey: FilesSortOrder.storageKey) == "size:desc")
        await vm.pickSort(.size)
        #expect(vm.rows.map(\.name) == names(.size, ascending: true))
        // The viewer pages through the photos and videos in that order.
        #expect(vm.viewerItems().map(\.path) == ["/d/IMG_2.jpg", "/d/IMG_10.jpg"])
        // Another launch: the order kept.
        let again = explorer(listing.filter { $0.path != ".." }.map(\.raw), defaults: defaults)
        #expect(again.sort == FilesSortOrder(key: .size, ascending: true))
        await again.load()
        #expect(again.rows.map(\.name) == names(.size, ascending: true))
    }

    @Test func aStoredOrderThatCantBeReadIsTheDefault() {
        let defaults = scratchDefaults()
        defaults.set("by-colour", forKey: FilesSortOrder.storageKey)
        #expect(explorer([], defaults: defaults).sort == .default)
    }

    @Test func theSearchsResultsFollowANewOrder() async {
        let vm = explorer([], defaults: scratchDefaults())
        let files = listing.filter { $0.path != ".." }.map(\.raw)
        vm.results = .init(text: "x", state: .done, files: files)
        await vm.pickSort(.modified)
        #expect(vm.results?.files.map { FileRow(file: $0).name } == names(.modified, ascending: false, listing.filter { $0.path != ".." }))
    }

    @Test func aBigFolderIsPutInOrderOffTheMainThread() async {
        let files = (0..<(FilesSort.offMainAbove + 500)).reversed().map { i -> Msg_File in
            var f = Msg_File()
            f.path = "/d/IMG_\(i).jpg"
            f.mime = "image/jpeg"
            return f
        }
        let vm = explorer(files, defaults: scratchDefaults())
        await vm.load()
        #expect(!vm.sorting)
        #expect(vm.rows.count == files.count + 1)
        #expect(vm.rows.prefix(4).map(\.name) == ["..", "IMG_0.jpg", "IMG_1.jpg", "IMG_2.jpg"])
        #expect(vm.rows.last?.name == "IMG_\(FilesSort.offMainAbove + 499).jpg")
        await vm.pickSort(.name)
        #expect(vm.rows.prefix(2).map(\.name) == ["..", "IMG_\(FilesSort.offMainAbove + 499).jpg"])
    }
}
