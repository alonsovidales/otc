// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Issue #195: Files' Storage view (FolderUsage.swift, FilesExplorerViewModel)
/// - its rows, how it says a share, which answers it takes, and that only
/// it asks the device. The same cases as Android's and the web's.
@MainActor
struct FolderUsageModelTests {
    private func entry(_ path: String, dir: Bool, files: Int64, versions: Int64 = 0, count: Int32 = 1) -> Msg_FolderUsageEntry {
        var e = Msg_FolderUsageEntry()
        e.path = path
        e.isDir = dir
        e.filesSize = files
        e.versionsSize = versions
        e.fileCount = count
        return e
    }

    private func usage(_ path: String, _ entries: [Msg_FolderUsageEntry]) -> Msg_FolderUsage {
        var u = Msg_FolderUsage()
        u.path = path
        u.entries = entries
        u.filesSize = entries.reduce(0) { $0 + $1.filesSize }
        u.versionsSize = entries.reduce(0) { $0 + $1.versionsSize }
        u.fileCount = entries.reduce(0) { $0 + $1.fileCount }
        return u
    }

    private func row(_ path: String, dir: Bool = false, uploadOnly: Bool = false) -> FileRow {
        var f = Msg_File()
        f.path = path
        f.mime = dir ? "inode/directory" : "audio/flac"
        f.uploadOnly = uploadOnly
        return FileRow(file: f)
    }

    @Test func onlyTheAnswerForTheFolderShownIsTaken() {
        #expect(FolderUsageModel.answers("/Docs/", folder: "/Docs/"))
        #expect(FolderUsageModel.answers("/Docs/", folder: "/Docs"))
        #expect(FolderUsageModel.answers("/", folder: "/"))
        #expect(!FolderUsageModel.answers("/Docs/", folder: "/Music/"))
        #expect(!FolderUsageModel.answers("/Docs/Work/", folder: "/Docs/"))
    }

    @Test func onlyTheLatestAsksAnswerIsTaken() {
        #expect(FolderUsageModel.takes("/Docs/", folder: "/Docs", ask: 3, latest: 3))
        // An older ask's, for the same folder: dropped.
        #expect(!FolderUsageModel.takes("/Docs/", folder: "/Docs/", ask: 2, latest: 3))
        // The latest's, for another folder: dropped.
        #expect(!FolderUsageModel.takes("/Music/", folder: "/Docs/", ask: 3, latest: 3))
    }

    @Test func anEmptyAnswerShowsTheSummaryTheEmptyTextAndTheNote() {
        #expect(FolderUsageModel.parts(usage("/Empty/", [])) == [.summary, .empty, .note])
        #expect(FolderUsageModel.parts(usage("/Docs/", [entry("/Docs/a.txt", dir: false, files: 3)])) == [.summary, .rows, .note])
    }

    @Test func aFolderOfNothingShowsOnlyTheSizesButVoiceOverHearsZero() {
        let en = Locale(identifier: "en")
        // No share shows when the folder takes no space.
        #expect(FolderUsageModel.rowText(size: "Zero KB", share: nil, locale: en) == "Zero KB")
        // Otherwise the size and the share.
        #expect(FolderUsageModel.rowText(size: "5 KB", share: 0.5, locale: en) == S.appFilesUsageRow(size: "5 KB", share: "50%"))
        #expect(FolderUsageModel.rowText(size: "0 KB", share: 0, locale: en) == S.appFilesUsageRow(size: "0 KB", share: "0%"))
        // VoiceOver's label still says 0%.
        let label = S.appFilesUsageRowLabel(name: "zero.txt", size: "Zero KB", share: FolderUsageModel.shareText(nil, locale: en))
        #expect(label.contains("0%"))
    }

    @Test func rowsKeepTheDevicesOrderAndJoinTheListingByPathAndKind() {
        // A folder and a file of one name (from two computers), a folder of
        // older versions only (the listing leaves it out), in the device's
        // order - which is never sorted again.
        let u = usage("/Music/", [
            entry("/Music/Live", dir: true, files: 52, count: 1),
            entry("/Music/Live", dir: false, files: 52),
            entry("/Music/Old", dir: true, files: 0, versions: 6, count: 0),
            entry("/Music/a.flac", dir: false, files: 0),
        ])
        let listing = [row(".."), row("/Music/Live", dir: true, uploadOnly: true), row("/Music/Live"), row("/Music/a.flac")]
        let rows = FolderUsageModel.rows(u, listing: listing, folder: "/Music/")
        #expect(rows.map(\.path) == ["/Music/Live", "/Music/Live", "/Music/Old", "/Music/a.flac"])
        #expect(rows.map(\.isDir) == [true, false, true, false])
        #expect(Set(rows.map(\.id)).count == 4)
        #expect(rows[0].listed?.isDir == true && rows[0].listed?.uploadOnly == true)
        #expect(rows[1].listed?.isDir == false && rows[1].listed?.uploadOnly == false)
        #expect(rows[2].listed == nil)
        #expect(rows.map(\.name) == ["Live", "Live", "Old", "a.flac"])
        // Sizes with their older versions; shares of the whole (110).
        #expect(rows.map(\.size) == [52, 52, 6, 0])
        #expect(FolderUsageModel.total(u) == 110)
        #expect(abs((rows[0].share ?? 0) - 52.0 / 110.0) < 1e-9)
        #expect(rows[3].share == 0)
    }

    @Test func aFolderOfNothingHasNoBars() {
        let u = usage("/Empty/", [entry("/Empty/zero.txt", dir: false, files: 0)])
        let rows = FolderUsageModel.rows(u, listing: [], folder: "/Empty/")
        #expect(rows.first?.share == nil)
        #expect(rows.first?.listed == nil)
    }

    @Test func sharesAreWholePercentagesInTheAppsLanguage() {
        let en = Locale(identifier: "en")
        #expect(FolderUsageModel.shareText(0.45, locale: en) == "45%")
        #expect(FolderUsageModel.shareText(1, locale: en) == "100%")
        #expect(FolderUsageModel.shareText(0.01, locale: en) == "1%")
        // Some space, but under one per cent.
        #expect(FolderUsageModel.shareText(0.004, locale: en) == S.appFilesUsageShareSmall)
        // None, or a folder of nothing.
        #expect(FolderUsageModel.shareText(0, locale: en) == "0%")
        #expect(FolderUsageModel.shareText(nil, locale: en) == "0%")
        // German writes a space before the sign.
        #expect(FolderUsageModel.shareText(0.45, locale: Locale(identifier: "de")).hasPrefix("45"))
        #expect(FolderUsageModel.shareText(0.45, locale: Locale(identifier: "de")) != "45%")
    }
}

/// The Files explorer asking for the sizes (FilesExplorerViewModel).
@MainActor
@Suite(.serialized)
struct FolderUsageExplorerTests {
    final class Sent {
        var list: [Msg_ReqEnvelope.OneOf_Payload] = []
        var usages: [String] {
            list.compactMap { if case .reqGetFolderUsage(let g) = $0 { return g.path } else { return nil } }
        }
        var deletes: [Msg_DelFile] {
            list.compactMap { if case .reqDelFile(let d) = $0 { return d } else { return nil } }
        }
    }

    nonisolated private static func listing(_ files: [Msg_File]) -> Msg_RespEnvelope {
        var lof = Msg_ListOfFiles()
        lof.files = files
        var r = Msg_RespEnvelope()
        r.payload = .respListOfFiles(lof)
        return r
    }

    nonisolated private static func usage(_ path: String) -> Msg_RespEnvelope {
        var u = Msg_FolderUsage()
        u.path = path
        var e = Msg_FolderUsageEntry()
        e.path = path + "a.txt"
        e.filesSize = 10
        e.fileCount = 1
        u.entries = [e]
        u.filesSize = 10
        u.fileCount = 1
        var r = Msg_RespEnvelope()
        r.payload = .respFolderUsage(u)
        return r
    }

    nonisolated private static func refusal(_ code: String, _ message: String) -> Msg_RespEnvelope {
        var r = Msg_RespEnvelope()
        r.error = true
        r.errorCode = code
        r.errorMessage = message
        return r
    }

    /// An explorer at `path` whose sizes are answered by `usage` (by the
    /// folder asked; a throw: no answer).
    private func explorer(at path: String, support: DeviceSupport? = nil,
                          usage: @escaping (String) throws -> Msg_RespEnvelope = { FolderUsageExplorerTests.usage($0) },
                          delete: @escaping (Msg_DelFile) -> Msg_RespEnvelope = { _ in
                              var r = Msg_RespEnvelope()
                              var a = Msg_Ack()
                              a.ok = true
                              r.payload = .respAck(a)
                              return r
                          }) -> (FilesExplorerViewModel, Sent) {
        let vm = FilesExplorerViewModel(initialPath: path, defaults: UserDefaults(suiteName: "usage-\(UUID().uuidString)")!)
        vm.thumbDisk = testThumbCache()
        vm.support = support ?? DeviceSupport()
        let sent = Sent()
        vm.request = { payload in
            sent.list.append(payload)
            switch payload {
            case .reqListFiles(let l):
                var f = Msg_File()
                f.path = l.path + "a.txt"
                f.mime = "text/plain"
                f.hash = "abc"
                var d = Msg_File()
                d.path = l.path + "Sub"
                d.mime = "inode/directory"
                return Self.listing([f, d])
            case .reqGetFolderUsage(let g):
                return try usage(g.path)
            case .reqDelFile(let d):
                return delete(d)
            default:
                throw CancellationError()
            }
        }
        return (vm, sent)
    }

    @Test func listAndGridNeverAsk() async {
        let (vm, sent) = explorer(at: "/Docs/")
        await vm.load()
        vm.navigate(to: "/Docs/Sub/")
        try? await Task.sleep(nanoseconds: 100_000_000)
        #expect(sent.usages.isEmpty)
        #expect(vm.usage == nil)
    }

    @Test func storageAsksWhenItOpensAndForEachFolder() async {
        let (vm, sent) = explorer(at: "/Docs/")
        await vm.load()
        #expect(vm.showStorage(true))
        await vm.loadUsage()
        #expect(sent.usages == ["/Docs/"])
        guard case .loaded(let u) = vm.usage else {
            Issue.record("no sizes")
            return
        }
        #expect(u.path == "/Docs/")
        // A refresh (or a delete) there: the listing and the sizes again.
        await vm.load()
        #expect(sent.usages == ["/Docs/", "/Docs/"])
        // Another folder, staying in Storage.
        await vm.show("/Docs/Sub/")
        #expect(sent.usages.last == "/Docs/Sub/")
        // Back to List: nothing more is asked.
        vm.showStorage(false)
        #expect(vm.usage == nil)
        await vm.load()
        #expect(sent.usages.count == 3)
    }

    @Test func anAnswerForAnotherFolderIsDropped() async {
        let (vm, _) = explorer(at: "/Docs/", usage: { _ in FolderUsageExplorerTests.usage("/Elsewhere/") })
        vm.showStorage(true)
        await vm.load()
        #expect(vm.usage == .loading)
    }

    @Test func anOlderDeviceSaysItNeedsAnUpdateAndIsNotAskedAgain() async {
        let support = DeviceSupport()
        let (vm, sent) = explorer(at: "/", support: support, usage: { _ in
            FolderUsageExplorerTests.refusal("unknown_payload", "unknown payload")
        })
        vm.showStorage(true)
        await vm.load()
        #expect(vm.usage == .needsUpdate)
        #expect(!support.folderUsage)
        await vm.show("/Docs/")
        #expect(vm.usage == .needsUpdate)
        #expect(sent.usages == ["/"])
    }

    @Test func noAnswerOffersTryAgainAndTheDevicesWordsAreShown() async {
        var fail = true
        let (vm, _) = explorer(at: "/", usage: { path in
            if fail { throw WSClient.TimedOut() }
            return FolderUsageExplorerTests.usage(path)
        })
        vm.showStorage(true)
        await vm.load()
        #expect(vm.usage == .failed(nil))
        fail = false
        await vm.loadUsage()
        if case .loaded = vm.usage {} else { Issue.record("not loaded after Try again") }

        let (other, _) = explorer(at: "/", usage: { _ in FolderUsageExplorerTests.refusal("", "internal error") })
        other.showStorage(true)
        await other.load()
        #expect(other.usage == .failed("internal error"))
    }

    @Test func anOlderAnswerForTheSameFolderLandingLastIsDropped() async {
        let vm = FilesExplorerViewModel(initialPath: "/Docs/", defaults: UserDefaults(suiteName: "usage-\(UUID().uuidString)")!)
        vm.thumbDisk = testThumbCache()
        vm.support = DeviceSupport()
        var held: CheckedContinuation<Void, Never>?
        var asks = 0
        vm.request = { payload in
            switch payload {
            case .reqGetFolderUsage(let g):
                asks += 1
                let mine = asks
                // The first answer is slow: it lands after the second.
                if mine == 1 { await withCheckedContinuation { held = $0 } }
                var r = FolderUsageExplorerTests.usage(g.path)
                if case .respFolderUsage(var u) = r.payload {
                    u.filesSize = Int64(mine * 100)
                    r.payload = .respFolderUsage(u)
                }
                return r
            default:
                throw CancellationError()
            }
        }
        vm.showStorage(true)
        let first = Task { await vm.loadUsage() }
        let end = Date() + 3
        while held == nil && Date() < end { try? await Task.sleep(nanoseconds: 10_000_000) }
        // A refresh meanwhile: answered at once.
        await vm.loadUsage()
        guard case .loaded(let now) = vm.usage else {
            Issue.record("no sizes")
            return
        }
        #expect(now.filesSize == 200)
        held?.resume()
        await first.value
        guard case .loaded(let after) = vm.usage else {
            Issue.record("no sizes")
            return
        }
        #expect(after.filesSize == 200)
    }

    @Test func storageHasNoSelection() async {
        let (vm, _) = explorer(at: "/Docs/")
        await vm.load()
        vm.toggleSelected("/Docs/a.txt")
        vm.showStorage(true)
        #expect(vm.selected.isEmpty)
    }

    @Test func deletesCarryTheListedContentAndSayWhenItChanged() async {
        let (vm, sent) = explorer(at: "/Docs/", delete: { d in
            d.path == "/Docs/a.txt" ? FolderUsageExplorerTests.refusal("changed", "this file has changed since it was shown")
                                     : { var r = Msg_RespEnvelope(); var a = Msg_Ack(); a.ok = true; r.payload = .respAck(a); return r }()
        })
        var toasts: [String] = []
        vm.announce = { toasts.append($0) }
        await vm.load()
        vm.selected = ["/Docs/a.txt", "/Docs/Sub"]
        let listings = sent.list.filter { if case .reqListFiles = $0 { return true } else { return false } }.count
        await vm.deleteSelected()
        let byPath = Dictionary(uniqueKeysWithValues: sent.deletes.map { ($0.path, $0) })
        #expect(byPath["/Docs/a.txt"]?.hash == "abc")
        // A folder's has none.
        #expect(byPath["/Docs/Sub"]?.hasHash == false)
        #expect(sent.deletes.allSatisfy { !$0.hasEveryCopy })
        #expect(vm.toast == "this file has changed since it was shown")
        #expect(toasts == ["this file has changed since it was shown"])
        // Listed again.
        #expect(sent.list.filter { if case .reqListFiles = $0 { return true } else { return false } }.count == listings + 1)
    }
}
