// SPDX-License-Identifier: AGPL-3.0-or-later

import CoreGraphics
import SwiftUI
import Testing
@testable import OffTheCloud

/// The Images search (TopSearch.swift): matching as the web's filesNav.ts
/// does it, the panel's sections and what the Search key takes.
@MainActor
struct TopSearchTests {
    private func person(_ id: String, _ name: String, _ faces: Int32) -> Msg_Person {
        var p = Msg_Person()
        p.id = id
        p.name = name
        p.faceCount = faces
        return p
    }

    private func ids(_ options: [SearchOption]) -> [String] { options.map(\.id) }

    @Test func foldsCaseAndAccents() {
        #expect(FoldedText("José").text == "jose")
        #expect(FoldedText("ÁRBOL").text == "arbol")
        // Upper case and back, as the device folds: both sigmas are one.
        #expect(FoldedText("ΟΔΟΣ").text == FoldedText("Οδός").text)
        #expect(FoldedText("Straße").contains(FoldedText("STRASSE")))
    }

    @Test func ranksStartThenWordThenInside() throws {
        let label = FoldedText("José Luis")
        let start = try #require(SearchMatch.match(label, FoldedText("jose")))
        #expect(start.rank == 0)
        #expect(start.span == 0..<4)
        let word = try #require(SearchMatch.match(label, FoldedText("LUI")))
        #expect(word.rank == 1)
        #expect(word.span == 5..<8)
        let inside = try #require(SearchMatch.match(label, FoldedText("ose")))
        #expect(inside.rank == 2)
        #expect(SearchMatch.match(label, FoldedText("maria")) == nil)
        // A later word start beats an earlier match inside a word.
        let later = try #require(SearchMatch.match(FoldedText("bobcat cat"), FoldedText("cat")))
        #expect(later.rank == 1)
        #expect(later.span == 7..<10)
    }

    @Test func thingsAreFiveRankedAndNotAlreadyChips() {
        let m = TopSearchModel()
        m.query = "cat"
        let tags = ["bobcat", "dog", "black cat", "catalog", "cat", "Cats", "cathedral"]
        let s = m.suggestions(tags: tags, inSearch: [], people: [], faces: false, noFileSearch: false)
        #expect(ids(s.tags) == ["t:catalog", "t:cat", "t:Cats", "t:cathedral", "t:black cat"])
        // The exact match is what the Search key takes.
        #expect(s.best == "t:cat")
        #expect(!s.docsFirst)

        let chipped = m.suggestions(tags: tags, inSearch: ["CAT"], people: [], faces: false, noFileSearch: false)
        #expect(!ids(chipped.tags).contains("t:cat"))
        #expect(chipped.best == "t:catalog")
    }

    @Test func peopleOnlyWhileFaceRecognitionIsOn() {
        let m = TopSearchModel()
        m.query = "an"
        let people = [
            person("1", "Ana", 3), person("2", "Juan", 40), person("3", "", 99),
            person("4", "Andrés", 12), person("5", "Anabel", 1), person("6", "Dan", 7), person("7", "Iván", 2),
        ]
        let off = m.suggestions(tags: [], inSearch: [], people: people, faces: false, noFileSearch: false)
        #expect(off.people.isEmpty)

        let on = m.suggestions(tags: [], inSearch: [], people: people, faces: true, noFileSearch: false)
        // Named only, the five with the most photos.
        #expect(ids(on.people) == ["p:2", "p:4", "p:6", "p:1", "p:7"])
        #expect(on.best == "p:2")
    }

    @Test func documentsRowFirstWhenNothingElseMatches() {
        let m = TopSearchModel()
        m.query = "  invoice "
        let s = m.suggestions(tags: ["dog"], inSearch: [], people: [], faces: true, noFileSearch: false)
        #expect(s.tags.isEmpty && s.people.isEmpty)
        #expect(s.docsFirst)
        #expect(s.docs?.id == "docs")
        if case .docs(let text)? = s.docs { #expect(text == "invoice") } else { Issue.record("no documents row") }
        #expect(s.best == "docs")
    }

    @Test func anOlderDeviceHasNoDocumentsRow() {
        let m = TopSearchModel()
        m.query = "invoice"
        let s = m.suggestions(tags: [], inSearch: [], people: [], faces: true, noFileSearch: true)
        #expect(s.docs == nil)
        #expect(s.best == nil)
        #expect(s.isEmpty)
    }

    @Test func nothingTypedNothingShown() {
        let m = TopSearchModel()
        m.query = "   "
        let s = m.suggestions(tags: ["a"], inSearch: [], people: [person("1", "A", 1)], faces: true, noFileSearch: false)
        #expect(s.isEmpty)
    }

    @Test func pathsAsFilesWritesThem() {
        #expect(FilePaths.parentFolder("/a/b/c.pdf") == "/a/b/")
        #expect(FilePaths.parentFolder("/a/b/") == "/a/")
        #expect(FilePaths.parentFolder("/c.pdf") == "/")
        #expect(FilePaths.asFolder("/a/b") == "/a/b/")
        #expect(FilePaths.asFolder("/a/b/") == "/a/b/")
        #expect(FilePaths.parts("/a/b/c.pdf") == ("c.pdf", "/a/b"))
        #expect(FilePaths.parts("/a/b/") == ("b", "/a"))
        #expect(FilePaths.parts("/c.pdf") == ("c.pdf", "/"))
    }

    @Test func boldsTheMatch() {
        let a = SearchMatch.marked("José Luis", 5..<8)
        let bold = a.runs.filter { $0.inlinePresentationIntent == .stronglyEmphasized }
        #expect(bold.count == 1)
        #expect(bold.first.map { String(a[$0.range].characters) } == "Lui")
    }
}

/// The web's icons drawn from its SVG data (NavIcons.swift).
struct NavIconTests {
    @Test func everyIconStaysInItsBox() {
        for icon in NavIcon.allCases {
            let box = icon.path.cgPath.boundingBoxOfPath
            #expect(!box.isEmpty, "\(icon)")
            #expect(box.minX >= 1.9 && box.minY >= 1.9 && box.maxX <= 22.1 && box.maxY <= 22.1, "\(icon): \(box)")
        }
    }

    @Test func arcsTurnTheWaySVGSays() {
        // Files' top left corner, "M3.5 7.5a2 2 0 0 1 2-2": a rounded
        // corner, its centre at (5.5, 7.5) - the inside of the folder.
        let files = NavIcon.files.path
        #expect(files.contains(CGPoint(x: 4.2, y: 6.2)))
        #expect(!files.contains(CGPoint(x: 3.7, y: 5.7)))
        let box = files.cgPath.boundingBoxOfPath
        #expect(abs(box.minX - 3.5) < 0.01 && abs(box.minY - 5.5) < 0.01)
        #expect(abs(box.maxX - 20.5) < 0.01 && abs(box.maxY - 19.5) < 0.01)
    }

    @Test func theGearIsDrawnAt85Percent() {
        // Its teeth reach 1 and 23 (large arcs), scaled by 0.85 about the
        // web's translate(1.8 1.8).
        let box = NavIcon.settings.path.cgPath.boundingBoxOfPath
        #expect(abs(box.minX - 2.65) < 0.05 && abs(box.maxX - 21.35) < 0.05, "\(box)")
        #expect(abs(box.minY - 2.65) < 0.05 && abs(box.maxY - 21.35) < 0.05, "\(box)")
    }

    @Test func compactNumbersParse() {
        // "c.8-2.2 2.5-3.3 4.5-3.3s3.7 1.1 4.5 3.3": People's shoulders end
        // where they began, level.
        var p = Path()
        SVGPath.append("M7.5 17.5c.8-2.2 2.5-3.3 4.5-3.3s3.7 1.1 4.5 3.3", to: &p)
        let box = p.cgPath.boundingBoxOfPath
        #expect(abs(box.minX - 7.5) < 0.01 && abs(box.maxX - 16.5) < 0.01)
        #expect(abs(box.maxY - 17.5) < 0.01)
        #expect(box.minY > 14 && box.minY < 14.3)
    }
}
