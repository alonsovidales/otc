// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

private func person(_ id: String, _ name: String, _ faces: Int32) -> Msg_Person {
    var p = Msg_Person()
    p.id = id
    p.name = name
    p.faceCount = faces
    return p
}

/// The People page's order and the merge's choices (PeopleView.swift), as
/// the web's PeopleView.tsx works them out.
struct PeopleOrderTests {
    @Test func namedFirstEachInTheDevicesOrder() {
        let all = [person("a", "", 9), person("b", "Ana", 5), person("c", "  ", 4), person("d", "Leo", 7), person("e", "", 1)]
        #expect(PeopleOrder.shown(all).map(\.id) == ["b", "d", "a", "c", "e"])
    }

    @Test func tidiesNames() {
        #expect(PeopleOrder.tidy("  Ana   María \n") == "Ana María")
        #expect(PeopleOrder.tidy("   ") == "")
        #expect(PeopleOrder.names([person("a", "Ana", 1), person("b", "ana ", 2), person("c", "", 3), person("d", "Leo", 1)]) == ["Ana", "Leo"])
    }

    @Test func keepsTheOnlyNamedFace() {
        let list = [person("a", "", 30), person("b", "Ana", 2), person("c", "", 5)]
        #expect(PeopleOrder.firstKeep(list) == "b")
    }

    @Test func keepsTheMostPhotosWhenNobodyIsNamed() {
        let list = [person("a", "", 3), person("b", "", 8), person("c", "", 8)]
        // The first of a tie, as the web's reduce.
        #expect(PeopleOrder.firstKeep(list) == "b")
    }

    @Test func oneNameTwiceKeepsTheOneWithMostPhotos() {
        let list = [person("a", "Ana", 3), person("b", "ana", 9), person("c", "", 20)]
        #expect(PeopleOrder.firstKeep(list) == "b")
    }

    @Test func differentNamesNeedAPick() {
        let list = [person("a", "Ana", 3), person("b", "Leo", 9)]
        #expect(PeopleOrder.firstKeep(list) == nil)
    }

    @Test func wording() {
        #expect(PeopleOrder.andList(["Ana"]) == "Ana")
        #expect(PeopleOrder.andList(["Ana", "Leo"]) == "Ana and Leo")
        #expect(PeopleOrder.andList(["Ana", "Leo", "Rosa"]) == "Ana, Leo and Rosa")
        #expect(PeopleOrder.photos(1) == "1 photo")
        #expect(PeopleOrder.photos(27) == "27 photos")
    }
}

/// People's requests (PhotoGalleryVM), answered here in the device's place.
@MainActor
struct PeopleRequestTests {
    /// A viewer-only model: no search of its own goes out (fixedList).
    private func model(_ people: [Msg_Person], answer: @escaping (Msg_ReqEnvelope.OneOf_Payload) throws -> Bool) -> (PhotoGalleryVM, Sent) {
        let vm = PhotoGalleryVM()
        vm.allPeople = people
        let sent = Sent()
        vm.peopleRequest = { payload in
            sent.list.append(payload)
            if case .reqListPeople = payload { throw CancellationError() }
            var r = Msg_RespEnvelope()
            var a = Msg_Ack()
            a.ok = try answer(payload)
            r.payload = .respAck(a)
            return r
        }
        return (vm, sent)
    }

    final class Sent {
        var list: [Msg_ReqEnvelope.OneOf_Payload] = []
        /// Everything but the list asked for again afterwards.
        var changes: [Msg_ReqEnvelope.OneOf_Payload] {
            list.filter { if case .reqListPeople = $0 { return false } else { return true } }
        }
    }

    @Test func mergeNamesTheKeptFaceFirstThenMergesOnce() async {
        let (vm, sent) = model([person("a", "", 9), person("b", "", 3), person("c", "", 1)]) { _ in true }
        vm.selectedPeople = ["c"]
        let res = await vm.mergePeople(keep: vm.allPeople[0], sources: ["b", "c"], name: "Ana")
        #expect(res == .merged)
        #expect(sent.changes.count == 2)
        guard sent.changes.count == 2 else { return }
        if case .reqRenamePerson(let r) = sent.changes[0] {
            #expect(r.id == "a" && r.name == "Ana")
        } else {
            Issue.record("first: \(sent.changes[0])")
        }
        if case .reqMergePeople(let m) = sent.changes[1] {
            #expect(m.targetID == "a" && m.sourceIds == ["b", "c"])
        } else {
            Issue.record("second: \(sent.changes[1])")
        }
        #expect(vm.allPeople.map(\.id) == ["a"])
        #expect(vm.allPeople.first?.name == "Ana")
        // Merged away: no longer narrowing Images.
        #expect(vm.selectedPeople.isEmpty)
    }

    @Test func aKeptNameIsNotSentAgain() async {
        let (vm, sent) = model([person("a", "Ana", 9), person("b", "", 3)]) { _ in true }
        let res = await vm.mergePeople(keep: vm.allPeople[0], sources: ["b"], name: "Ana")
        #expect(res == .merged)
        #expect(sent.changes.count == 1)
        if case .reqMergePeople = sent.changes.first {} else { Issue.record("\(sent.changes)") }
    }

    @Test func aRefusedNameMergesNothing() async {
        let (vm, sent) = model([person("a", "", 9), person("b", "", 3)]) { p in
            if case .reqRenamePerson = p { return false }
            return true
        }
        let res = await vm.mergePeople(keep: vm.allPeople[0], sources: ["b"], name: "Ana")
        #expect(res == .nameNotSaved)
        #expect(sent.changes.count == 1)
        #expect(vm.allPeople.count == 2)
    }

    @Test func aRefusedMergeKeepsEveryone() async {
        let (vm, _) = model([person("a", "Ana", 9), person("b", "", 3)]) { p in
            if case .reqMergePeople = p { return false }
            return true
        }
        #expect(await vm.mergePeople(keep: vm.allPeople[0], sources: ["b"], name: "Ana") == .notMerged)
        #expect(vm.allPeople.count == 2)
    }

    @Test func anUnansweredMergeSaysSo() async {
        let (vm, _) = model([person("a", "Ana", 9), person("b", "", 3)]) { _ in throw CancellationError() }
        #expect(await vm.mergePeople(keep: vm.allPeople[0], sources: ["b"], name: "Ana") == .unanswered)
    }

    @Test func deletesOneByOneAndReportsWhatFailed() async {
        let people = ["a", "b", "c", "d", "e"].map { person($0, "", 1) }
        let (vm, sent) = model(people) { p in
            guard case .reqDeletePerson(let d) = p else { return true }
            if d.id == "b" { return false }
            if d.id == "d" { throw CancellationError() }
            return true
        }
        vm.selectedPeople = ["a", "b"]
        var heard = 0
        let (deleted, unanswered) = await vm.deletePeople(["a", "b", "c", "d"]) { heard += 1 }
        #expect(deleted == ["a", "c"])
        #expect(unanswered == 1)
        #expect(heard == 4)
        let ids = sent.changes.compactMap { p -> String? in
            if case .reqDeletePerson(let d) = p { return d.id }
            return nil
        }
        #expect(Set(ids) == ["a", "b", "c", "d"])
        #expect(ids.count == 4)
        #expect(vm.allPeople.map(\.id) == ["b", "d", "e"])
        #expect(vm.selectedPeople == ["b"])
    }

    @Test func renameUpdatesTheList() async {
        let (vm, _) = model([person("a", "", 9)]) { _ in true }
        #expect(await vm.renamePerson("a", to: "Ana") == .ok)
        #expect(vm.allPeople.first?.name == "Ana")
        #expect(await vm.renamePerson("a", to: "") == .ok)
        #expect(vm.allPeople.first?.name == "")
    }

    @Test func showPersonIsANewSearch() {
        let vm = PhotoGalleryVM()
        vm.chips = ["dog"]
        vm.selectedPeople = ["x", "y"]
        vm.showPerson("a")
        #expect(vm.chips.isEmpty)
        #expect(vm.selectedPeople == ["a"])
        #expect(vm.activeGroup == nil)
    }
}

/// What the People page carries from the sheet to the wide layout's page
/// and back (PeoplePageState).
@MainActor
struct PeoplePageStateTests {
    @Test func turningThePhoneKeepsThePicksAndThePlace() {
        let state = PeoplePageState()
        #expect(!state.arrive())
        state.sel = ["a", "b"]
        state.topPerson = "k"
        state.handOff()
        #expect(state.arrive())
        #expect(state.sel == ["a", "b"])
        #expect(state.topPerson == "k")
    }

    @Test func anotherVisitStartsAtTheTopWithNothingPicked() {
        let state = PeoplePageState()
        state.sel = ["a"]
        state.topPerson = "k"
        #expect(!state.arrive())
        #expect(state.sel.isEmpty)
        #expect(state.topPerson == nil)
    }
}
