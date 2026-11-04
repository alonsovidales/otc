// SPDX-License-Identifier: AGPL-3.0-or-later

import AVFoundation
import Foundation
import Testing
@testable import OffTheCloud

/// The copies a launch leaves behind.
struct TempFilesTests {
    private func scratch() throws -> URL {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("TempFilesTests-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        return root
    }

    private func write(_ url: URL, modified: Date? = nil) throws {
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data("x".utf8).write(to: url)
        if let modified { try FileManager.default.setAttributes([.modificationDate: modified], ofItemAtPath: url.path) }
    }

    @Test func aLaunchEmptiesWhatEarlierLaunchesLeftAndNothingOfItsOwn() throws {
        let root = try scratch()
        defer { try? FileManager.default.removeItem(at: root) }
        let fm = FileManager.default
        let start = Date()
        let old = start.addingTimeInterval(-3600)
        // An earlier launch's copies, each kind.
        let earlier = root.appendingPathComponent("export/EARLIER/\(UUID().uuidString).mov")
        let earlierPreview = root.appendingPathComponent("preview/EARLIER/\(UUID().uuidString)/Report.pdf")
        try write(earlier)
        try write(earlierPreview)
        // This launch's.
        let mine = TempFiles.file(.export, ext: "mov", root: root)
        try write(mine)
        // What older versions left at the top: a copy named by a UUID, a
        // trim, a wallpaper download - and things that aren't theirs.
        let legacy = root.appendingPathComponent("\(UUID().uuidString).mov")
        let legacyBare = root.appendingPathComponent(UUID().uuidString)
        let trim = root.appendingPathComponent("otc-trim-\(UUID().uuidString).mp4")
        let wallpaper = root.appendingPathComponent("wallpaper-\(UUID().uuidString)")
        let other = root.appendingPathComponent("notes.txt")
        let recent = root.appendingPathComponent("\(UUID().uuidString).jpg")
        for u in [legacy, legacyBare, trim, wallpaper, other] { try write(u, modified: old) }
        try write(recent)

        TempFiles.sweep(root: root, except: TempFiles.launch, before: start)

        #expect(!fm.fileExists(atPath: earlier.path))
        #expect(!fm.fileExists(atPath: earlierPreview.path))
        #expect(fm.fileExists(atPath: mine.path), "this launch's copies stay")
        for u in [legacy, legacyBare, trim, wallpaper] { #expect(!fm.fileExists(atPath: u.path), "\(u.lastPathComponent)") }
        #expect(fm.fileExists(atPath: other.path), "not a copy of ours")
        #expect(fm.fileExists(atPath: recent.path), "made by this launch")
    }

    @Test func aNamedCopyGoesWithItsOwnFolder() throws {
        let root = try scratch()
        defer { try? FileManager.default.removeItem(at: root) }
        let a = TempFiles.named(.preview, "Report.pdf", root: root)
        let b = TempFiles.named(.preview, "Report.pdf", root: root)
        #expect(a != b, "two of one name never meet")
        #expect(a.lastPathComponent == "Report.pdf")
        try write(a)
        try write(b)
        TempFiles.remove(a, root: root)
        #expect(!FileManager.default.fileExists(atPath: a.deletingLastPathComponent().path))
        #expect(FileManager.default.fileExists(atPath: b.path))
        // A plain copy leaves the launch's folder.
        let c = TempFiles.file(.viewer, ext: "jpg", root: root)
        try write(c)
        TempFiles.remove(c, root: root)
        #expect(!FileManager.default.fileExists(atPath: c.path))
        #expect(FileManager.default.fileExists(atPath: c.deletingLastPathComponent().path))
        // Anything else is left alone.
        let outside = root.appendingPathComponent("elsewhere/x.txt")
        try write(outside)
        TempFiles.remove(nil, root: root)
        #expect(FileManager.default.fileExists(atPath: outside.path))
    }

    @Test func theNamesOlderVersionsGaveTheirCopies() {
        #expect(TempFiles.leftByAnOlderVersion("3F2504E0-4F89-11D3-9A0C-0305E82C3301"))
        #expect(TempFiles.leftByAnOlderVersion("3F2504E0-4F89-11D3-9A0C-0305E82C3301.mov"))
        #expect(TempFiles.leftByAnOlderVersion("otc-trim-anything.mp4"))
        #expect(TempFiles.leftByAnOlderVersion("wallpaper-anything"))
        #expect(!TempFiles.leftByAnOlderVersion("export"))
        #expect(!TempFiles.leftByAnOlderVersion("Report.pdf"))
        #expect(!TempFiles.leftByAnOlderVersion("3F2504E0-4F89"))
    }
}

/// The viewer's and Files' decrypted copies go as they close.
@MainActor
struct ViewerCopiesTests {
    private func copy(_ kind: TempFiles.Kind, ext: String = "mov") throws -> URL {
        let url = TempFiles.file(kind, ext: ext)
        try Data("x".utf8).write(to: url)
        return url
    }

    @Test func aDownloadedVideoGoesWithItsPlayer() throws {
        let vm = PhotoGalleryVM()
        let first = try copy(.viewer)
        vm.playDownloaded(AVPlayer(), file: first)
        #expect(FileManager.default.fileExists(atPath: first.path))
        // Another video's player: the first one's file goes.
        let second = try copy(.viewer)
        vm.playDownloaded(AVPlayer(), file: second)
        #expect(!FileManager.default.fileExists(atPath: first.path))
        #expect(FileManager.default.fileExists(atPath: second.path))
        // The viewer closes: so does the second's.
        vm.closeModal()
        #expect(!FileManager.default.fileExists(atPath: second.path))
    }

    @Test func theSharedPictureGoesOnceTheShareSheetCloses() throws {
        let vm = PhotoGalleryVM()
        let jpeg = try copy(.viewer, ext: "jpg")
        vm.shareURL = jpeg
        #expect(FileManager.default.fileExists(atPath: jpeg.path))
        vm.shareURL = nil
        #expect(!FileManager.default.fileExists(atPath: jpeg.path))
    }

    @Test func aQuickLookCopyGoesAsThePreviewCloses() throws {
        let vm = FilesExplorerViewModel(initialPath: "/d/", defaults: UserDefaults(suiteName: "vc-\(UUID().uuidString)")!)
        let doc = TempFiles.named(.preview, "Report.pdf")
        try Data("x".utf8).write(to: doc)
        vm.previewURL = doc
        let next = TempFiles.named(.preview, "Report.pdf")
        try Data("y".utf8).write(to: next)
        vm.previewURL = next
        #expect(!FileManager.default.fileExists(atPath: doc.deletingLastPathComponent().path))
        vm.previewURL = nil
        #expect(!FileManager.default.fileExists(atPath: next.path))
    }
}
