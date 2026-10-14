// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

/// Issue #197: a click on a folder's name shows it in Finder; a folder that
/// is gone is only reported. Against a fake Finder and temporary folders:
/// nothing real is opened. Runs in a scratch SwiftPM package holding only
/// FolderOpener.swift (module OffTheCloud), like SyncPathsTests - the app
/// target's tests launch the real app with its saved settings.
@Suite
struct FolderOpenerTests {
    final class FakeFinder: FolderOpening {
        var opened: [URL] = []
        var revealed: [URL] = []
        var packages: Set<String> = []
        var openSucceeds = true

        func open(_ url: URL) -> Bool {
            opened.append(url)
            return openSucceeds
        }

        func reveal(_ url: URL) { revealed.append(url) }

        func isPackage(_ path: String) -> Bool { packages.contains(path) }
    }

    /// A new empty folder, removed when the test's scope ends.
    private func scratch() throws -> URL {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("FolderOpenerTests-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    @Test func anExistingFolderOpensInFinder() throws {
        let dir = try scratch()
        defer { try? FileManager.default.removeItem(at: dir) }
        let finder = FakeFinder()
        #expect(FolderOpener.open(dir, using: finder) == .opened)
        #expect(finder.opened == [dir])
        #expect(finder.revealed.isEmpty)
    }

    @Test func aMissingFolderIsOnlyReported() throws {
        let dir = try scratch()
        try FileManager.default.removeItem(at: dir)
        let finder = FakeFinder()
        #expect(FolderOpener.open(dir, using: finder) == .missing)
        #expect(finder.opened.isEmpty)
        #expect(finder.revealed.isEmpty)
    }

    /// A file where the folder was would be opened by its app: missing.
    @Test func aFileInItsPlaceIsMissing() throws {
        let dir = try scratch()
        defer { try? FileManager.default.removeItem(at: dir) }
        let file = dir.appendingPathComponent("Photos")
        try Data("x".utf8).write(to: file)
        let finder = FakeFinder()
        #expect(FolderOpener.open(file, using: finder) == .missing)
        #expect(finder.opened.isEmpty)
    }

    @Test func notAFileURLIsMissing() {
        let finder = FakeFinder()
        #expect(FolderOpener.open(URL(string: "https://cala.off-the.cloud/")!, using: finder) == .missing)
        #expect(finder.opened.isEmpty)
    }

    /// A package picked as a folder (a Photos library) is shown selected,
    /// never opened: that would launch the app that owns it.
    @Test func aPackageIsRevealedNotOpened() throws {
        let dir = try scratch()
        defer { try? FileManager.default.removeItem(at: dir) }
        let finder = FakeFinder()
        finder.packages = [dir.path]
        #expect(FolderOpener.open(dir, using: finder) == .opened)
        #expect(finder.revealed == [dir])
        #expect(finder.opened.isEmpty)
    }

    /// The real check: an app picked as a folder is a package, a plain
    /// folder isn't (nothing is opened).
    @Test func workspaceTellsPackagesApart() throws {
        let dir = try scratch()
        defer { try? FileManager.default.removeItem(at: dir) }
        let app = dir.appendingPathComponent("Fake.app", isDirectory: true)
        try FileManager.default.createDirectory(at: app, withIntermediateDirectories: true)
        #expect(WorkspaceOpening().isPackage(app.path))
        #expect(!WorkspaceOpening().isPackage(dir.path))
    }

    @Test func finderRefusingIsAFailure() throws {
        let dir = try scratch()
        defer { try? FileManager.default.removeItem(at: dir) }
        let finder = FakeFinder()
        finder.openSucceeds = false
        #expect(FolderOpener.open(dir, using: finder) == .failed)
    }
}
