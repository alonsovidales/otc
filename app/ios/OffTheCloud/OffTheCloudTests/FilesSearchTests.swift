// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI
import Testing
@testable import OffTheCloud

/// Files' own search (FilesSearch.swift): files and folders only, in the
/// narrow layout, on a device that can search its files. Serialized: one
/// test marks the device as unable to search, which FilesNav keeps for all.
@MainActor
@Suite(.serialized)
struct FilesSearchTests {
    private func person(_ id: String, _ name: String) -> Msg_Person {
        var p = Msg_Person()
        p.id = id
        p.name = name
        p.faceCount = 5
        return p
    }

    private let library: [Msg_File] = [
        Msg_File.with { $0.path = "/Documents/Invoices"; $0.mime = "inode/directory" },
        Msg_File.with { $0.path = "/Documents/Invoices/2026-03.pdf"; $0.mime = "application/pdf" },
        Msg_File.with { $0.path = "/Photos/invoice-scan.jpg"; $0.mime = "image/jpeg" },
        Msg_File.with { $0.path = "/Photos/Trip/beach.mov"; $0.mime = "video/quicktime" },
    ]

    /// A files-only search whose device finds, by path, what `files` holds
    /// - or answers `unknown_payload`, as one older than SearchFiles does.
    private func model(_ files: [Msg_File], old: Bool = false, asked: ((Msg_SearchFiles) -> Void)? = nil) -> TopSearchModel {
        let m = TopSearchModel(scope: .files)
        m.request = { payload in
            guard case .reqSearchFiles(let req) = payload else { throw CancellationError() }
            asked?(req)
            var resp = Msg_RespEnvelope()
            if old {
                resp.error = true
                resp.errorCode = "unknown_payload"
                return resp
            }
            var lof = Msg_ListOfFiles()
            lof.files = files.filter { FoldedText($0.path).contains(FoldedText(req.query)) }
            resp.payload = .respListOfFiles(lof)
            return resp
        }
        return m
    }

    private func actions(_ m: TopSearchModel, noFileSearch: Bool = false, went: @escaping (FilesNav.Request.Kind) -> Void = { _ in }, focus: @escaping (Bool) -> Void = { _ in }) -> FilesSearchActions {
        FilesSearchActions(search: m, noFileSearch: noFileSearch, focus: focus, go: went)
    }

    /// Waits for the device's answer (asked once the typing pauses).
    private func until(_ done: () -> Bool) async {
        for _ in 0..<150 where !done() {
            try? await Task.sleep(for: .milliseconds(20))
        }
    }

    @Test func noThingsOrPeopleOnlyTheDocumentsRow() {
        let m = model([])
        #expect(m.scope == .files)
        m.query = "an"
        // Whatever is handed in, no tag or person is offered.
        let s = m.suggestions(tags: ["ant", "plant"], inSearch: [], people: [person("1", "Ana")], faces: true, noFileSearch: false)
        #expect(s.tags.isEmpty && s.people.isEmpty)
        #expect(s.docsFirst)
        #expect(s.ordered.map(\.id) == ["docs"])
        #expect(s.best == "docs")
        #expect(m.fileSuggestions(noFileSearch: false).ordered.map(\.id) == ["docs"])
        // Images' field and the top bar still offer them.
        let all = TopSearchModel()
        all.request = { _ in Msg_RespEnvelope() }
        #expect(all.scope == .everything)
        all.query = "an"
        let everything = all.suggestions(tags: ["ant"], inSearch: [], people: [person("1", "Ana")], faces: true, noFileSearch: false)
        #expect(everything.tags.map(\.id) == ["t:ant"])
        #expect(everything.people.map(\.id) == ["p:1"])
    }

    @Test func filesAndFoldersByPathAfterTheDocumentsRow() async throws {
        var sent: [Msg_SearchFiles] = []
        let m = model(library) { sent.append($0) }
        m.query = "invo"
        await until { !m.fileSuggestions(noFileSearch: false).files.isEmpty }
        let s = m.fileSuggestions(noFileSearch: false)
        #expect(s.ordered.map(\.id) == ["docs", "f:/Documents/Invoices", "f:/Documents/Invoices/2026-03.pdf", "f:/Photos/invoice-scan.jpg"])
        // Asked once, as the typing paused, for as many as the panel shows.
        #expect(sent.map(\.query) == ["invo"])
        #expect(sent.first?.limit == Int32(TopSearchModel.matchFiles))
        // Each with its icon's kind, the name bold where it matches, else
        // the folder it is in.
        guard case .file(_, let kind, let name, let dir, let nameSpan, let dirSpan) = s.files[0] else {
            Issue.record("not a file row")
            return
        }
        #expect(kind == .folder && name == "Invoices" && dir == "/Documents")
        #expect(nameSpan == 0..<4 && dirSpan == nil)
        guard case .file(_, let pdfKind, _, let pdfDir, let pdfName, let pdfDirSpan) = s.files[1] else {
            Issue.record("not a file row")
            return
        }
        #expect(pdfKind == .doc && pdfDir == "/Documents/Invoices")
        #expect(pdfName == nil && pdfDirSpan == 11..<15)
        if case .file(_, let photo, _, _, _, _) = s.files[2] { #expect(photo == .photo) }
    }

    @Test func theSearchKeyListsEveryFileFound() async {
        let m = model(library)
        m.query = "invo "
        await until { !m.fileSuggestions(noFileSearch: false).files.isEmpty }
        m.open = true
        var went: [FilesNav.Request.Kind] = []
        var focused: Bool?
        actions(m, went: { went.append($0) }, focus: { focused = $0 }).submit()
        // Never a file or folder by itself: the results, "Files matching".
        #expect(went == [.search("invo")])
        #expect(m.query.isEmpty && !m.open)
        #expect(focused == false)
    }

    @Test func theArrowsPickWhatTheSearchKeyTakes() async {
        let m = model(library)
        m.query = "invo"
        await until { m.fileSuggestions(noFileSearch: false).files.count == 3 }
        m.open = true
        var went: [FilesNav.Request.Kind] = []
        let a = actions(m, went: { went.append($0) })
        // Down from the documents row, highlighted at first, to the first
        // folder found.
        #expect(a.arrow(1) == .handled)
        #expect(m.moved == "f:/Documents/Invoices")
        a.submit()
        #expect(went == [.folder("/Documents/Invoices/", file: nil)])

        // Up past the first row: the words as typed.
        m.query = "invo"
        await until { m.fileSuggestions(noFileSearch: false).files.count == 3 }
        m.open = true
        #expect(a.arrow(-1) == .handled)
        #expect(m.moved == "")
        a.submit()
        #expect(went.last == .search("invo"))
    }

    @Test func aPickOpensAsATapInFilesWould() {
        let m = model([])
        var went: [FilesNav.Request.Kind] = []
        let a = actions(m, went: { went.append($0) })
        let photo = library[2]
        let pdf = library[1]
        let folder = library[0]
        for f in [folder, photo, pdf] {
            m.query = "x"
            m.open = true
            let (name, dir) = FilePaths.parts(f.path)
            a.pick(.file(f, kind: FoundKind(f), name: name, dir: dir, nameSpan: nil, dirSpan: nil))
            #expect(m.query.isEmpty && !m.open)
        }
        m.query = "inv"
        a.pick(.docs("inv"))
        #expect(went == [
            .folder("/Documents/Invoices/", file: nil),
            // In the viewer over its folder, or opened as Files opens it.
            .folder("/Photos/", file: photo),
            .folder("/Documents/Invoices/", file: pdf),
            .search("inv"),
        ])
        // A tag or a person is no row of Files' search.
        m.query = "x"
        a.pick(.tag("dog", span: nil))
        #expect(went.count == 4)
        #expect(m.query == "x")
    }

    @Test func anOlderDeviceHidesTheField() async {
        FilesNav.shared.reset()
        defer { FilesNav.shared.reset() }
        #expect(FilesSearchActions.shown(wide: false, noFileSearch: FilesNav.shared.noFileSearch))
        let m = model(library, old: true)
        m.query = "invo"
        await until { FilesNav.shared.noFileSearch }
        // The device said so: the field goes, here and wherever Files is.
        #expect(FilesNav.shared.noFileSearch)
        #expect(!FilesSearchActions.shown(wide: false, noFileSearch: true))
        // Nor would it offer anything, or take the Search key anywhere.
        let s = m.fileSuggestions(noFileSearch: true)
        #expect(s.isEmpty && s.docs == nil)
        var went: [FilesNav.Request.Kind] = []
        m.open = true
        actions(m, noFileSearch: true, went: { went.append($0) }).submit()
        #expect(went.isEmpty)
        #expect(m.open && m.query == "invo")
    }

    @Test func onlyTheNarrowLayoutHasTheField() {
        #expect(FilesSearchActions.shown(wide: false, noFileSearch: false))
        // The top bar searches the files there.
        #expect(!FilesSearchActions.shown(wide: true, noFileSearch: false))
        #expect(!FilesSearchActions.shown(wide: true, noFileSearch: true))
    }

    @Test func turningThePhoneKeepsTheWords() {
        let m = model([])
        m.query = "trip"
        m.open = true
        var focused: Bool?
        actions(m, focus: { focused = $0 }).layoutChanged()
        #expect(m.query == "trip")
        #expect(!m.open)
        #expect(focused == false)
    }

    /// Images' field and the top bar's search, its device stubbed out.
    private func topSearch() -> TopSearchModel {
        let m = TopSearchModel()
        m.request = { _ in Msg_RespEnvelope() }
        return m
    }

    @Test func turnedOnFilesTheWordsCrossToTheTopBarAndBack() {
        let top = topSearch()
        let files = model([])
        top.query = "cats"
        files.query = "img_73"
        var handOff = FilesSearchHandOff()
        // Turned wide on Files: the top bar shows what Files' field held.
        handOff.layoutChanged(wide: true, onFiles: true, top: top, files: files)
        #expect(top.query == "img_73")
        #expect(handOff.imagesWords == "cats")
        // Changed there, then turned back: Files' field has the top bar's
        // words, and Images' field its own again.
        top.query = "macb"
        handOff.layoutChanged(wide: false, onFiles: true, top: top, files: files)
        #expect(files.query == "macb")
        #expect(top.query == "cats")
        #expect(handOff.imagesWords == nil)

        // Nothing typed in Files: the top bar is empty on Files too, and
        // Images' words come back with the field.
        files.query = ""
        handOff.layoutChanged(wide: true, onFiles: true, top: top, files: files)
        #expect(top.query.isEmpty)
        handOff.layoutChanged(wide: false, onFiles: true, top: top, files: files)
        #expect(files.query.isEmpty && top.query == "cats")
    }

    @Test func turnedElsewhereTheTopBarsWordsStayImages() {
        let top = topSearch()
        let files = model([])
        top.query = "cats"
        files.query = "img_73"
        var handOff = FilesSearchHandOff()
        // On another section the top bar is Images' field, as always.
        handOff.layoutChanged(wide: true, onFiles: false, top: top, files: files)
        #expect(top.query == "cats" && files.query == "img_73")
        #expect(handOff.imagesWords == nil)
        top.query = "dogs"
        handOff.layoutChanged(wide: false, onFiles: false, top: top, files: files)
        #expect(top.query == "dogs" && files.query == "img_73")

        // Turned wide on Files, then back on Images: the words on screen
        // stay where they are, and nothing set aside comes back later.
        handOff.layoutChanged(wide: true, onFiles: true, top: top, files: files)
        #expect(top.query == "img_73")
        handOff.layoutChanged(wide: false, onFiles: false, top: top, files: files)
        #expect(top.query == "img_73" && files.query == "img_73")
        #expect(handOff.imagesWords == nil)

        // Turned wide elsewhere, then Files picked and turned back there:
        // Files' field has the top bar's words, which Images keeps too.
        top.query = "macb"
        handOff.layoutChanged(wide: true, onFiles: false, top: top, files: files)
        handOff.layoutChanged(wide: false, onFiles: true, top: top, files: files)
        #expect(files.query == "macb" && top.query == "macb")
    }

    @Test func theSuggestionsHideWhatTheyCover() {
        // Up with something typed: the folder or the results under them are
        // out of VoiceOver's reach as well as out of sight.
        #expect(FilesSearchActions.covers(wide: false, noFileSearch: false, open: true, typed: "do"))
        // Closed, empty, wide or on an older device there is no panel.
        #expect(!FilesSearchActions.covers(wide: false, noFileSearch: false, open: false, typed: "do"))
        #expect(!FilesSearchActions.covers(wide: false, noFileSearch: false, open: true, typed: ""))
        #expect(!FilesSearchActions.covers(wide: true, noFileSearch: false, open: true, typed: "do"))
        #expect(!FilesSearchActions.covers(wide: false, noFileSearch: true, open: true, typed: "do"))
    }

    @Test func cancelClearAndEscape() {
        let m = model([])
        var focused: Bool?
        let a = actions(m, focus: { focused = $0 })
        m.query = "inv"
        m.open = true
        a.cancel()
        #expect(m.query.isEmpty && !m.open && focused == false)

        // The x: the words go, the field keeps the keyboard.
        focused = nil
        m.query = "inv"
        m.open = true
        a.clear()
        #expect(m.query.isEmpty && focused == nil)

        // Escape: the panel first, then the field.
        m.query = "inv"
        m.open = true
        #expect(a.escape() == .handled)
        #expect(!m.open && m.query == "inv" && focused == nil)
        #expect(a.arrow(1) == .handled)
        #expect(m.open)
        m.open = false
        #expect(a.escape() == .handled)
        #expect(focused == false)

        // Nothing typed: the Search key only lets go.
        var went = 0
        m.query = "  "
        m.open = true
        actions(m, went: { _ in went += 1 }, focus: { focused = $0 }).submit()
        #expect(went == 0 && !m.open)
    }
}
