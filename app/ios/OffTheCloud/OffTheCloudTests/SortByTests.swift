// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
@testable import OffTheCloud

private func person(_ id: String, _ name: String, _ faces: Int32 = 1) -> Msg_Person {
    var p = Msg_Person()
    p.id = id
    p.name = name
    p.faceCount = faces
    return p
}

private func collection(_ id: String, _ name: String) -> Msg_ImageGroup {
    var g = Msg_ImageGroup()
    g.id = id
    g.name = name
    return g
}

private let english = Locale(identifier: "en")

/// People's and Collections' Sort by (SortBy.swift), Android's
/// PeopleSort / CollectionsSort cases.
struct SortByTests {
    @Test func mostPhotosIsThePagesOrderAndTheDefault() {
        let all = [person("a", "", 9), person("b", "Zoe", 5), person("c", "", 4), person("d", "Ana", 3)]
        #expect(PeopleSort.default == .photos)
        #expect(PeopleSort.photos.ordered(all, locale: english).map(\.id) == PeopleOrder.shown(all).map(\.id))
        #expect(PeopleSort.photos.ordered(all, locale: english).map(\.id) == ["b", "d", "a", "c"])
    }

    @Test func peopleByNameAToZTheUnnamedLastInTheDevicesOrder() {
        // Case and accents ignored, numbers by value; names that compare
        // the same keep the device's order, as do the unnamed, last.
        let all = [
            person("a", "Ana 10"), person("b", "ana 2"), person("c", "Álvaro"), person("d", "zoe"),
            person("e", ""), person("f", "  Leo "), person("g", "   "), person("h", "ána 2"),
        ]
        #expect(PeopleSort.name.ordered(all, locale: english).map(\.id) == ["c", "b", "h", "a", "f", "d", "e", "g"])
    }

    @Test func peopleByNameKeepsEveryone() {
        let all = (0..<50).map { person("p\($0)", $0 % 3 == 0 ? "" : "Name \($0)") }
        let sorted = PeopleSort.name.ordered(all, locale: english)
        #expect(Set(sorted.map(\.id)) == Set(all.map(\.id)))
        #expect(sorted.count == all.count)
        // Name 1, Name 2 ... Name 49 (not Name 1, Name 10, Name 11 ...).
        #expect(sorted.prefix(3).map(\.name) == ["Name 1", "Name 2", "Name 4"])
    }

    @Test func newestFirstIsTheDevicesOrderAndTheDefault() {
        let all = [collection("1", "Zoo"), collection("2", ""), collection("3", "Beach")]
        #expect(CollectionsSort.default == .newest)
        #expect(CollectionsSort.newest.ordered(all, locale: english).map(\.id) == ["1", "2", "3"])
    }

    @Test func collectionsByNameAToZTheUntitledLast() {
        let all = [
            collection("1", "summer 2024"), collection("2", ""), collection("3", " Beach"),
            collection("4", "Summer 2023"), collection("5", "  "), collection("6", "Ébano"),
            collection("7", "beach"), collection("8", "Summer 10"),
        ]
        #expect(CollectionsSort.name.ordered(all, locale: english).map(\.id) == ["3", "7", "6", "8", "4", "1", "2", "5"])
    }

    @Test func storedChoices() {
        #expect(PeopleSort.storageKey == "people.sort")
        #expect(CollectionsSort.storageKey == "collections.sort")
        #expect(PeopleSort(stored: nil) == .photos)
        #expect(PeopleSort(stored: "name") == .name)
        #expect(PeopleSort(stored: "photos") == .photos)
        #expect(PeopleSort(stored: "Name") == .photos)
        #expect(PeopleSort(stored: "newest") == .photos)
        #expect(CollectionsSort(stored: nil) == .newest)
        #expect(CollectionsSort(stored: "name") == .name)
        #expect(CollectionsSort(stored: "newest") == .newest)
        #expect(CollectionsSort(stored: "photos") == .newest)
        for s in PeopleSort.allCases { #expect(PeopleSort(stored: s.rawValue) == s) }
        for s in CollectionsSort.allCases { #expect(CollectionsSort(stored: s.rawValue) == s) }
    }

    /// Each choice by its own key (compared with the key, not the English:
    /// another suite may be switching the app's language meanwhile).
    @Test func choicesAreWordedByTheirKeys() {
        #expect(PeopleSort.photos.title == S.appPhotosPeopleSortPhotos)
        #expect(PeopleSort.name.title == S.appPhotosPeopleSortName)
        #expect(CollectionsSort.newest.title == S.appPhotosCollectionsSortNewest)
        #expect(CollectionsSort.name.title == S.appPhotosCollectionsSortName)
    }
}

/// Places' "Back to the map" (AreaBackButton, Places.swift): shown while a
/// group of the map is open in the grid, as the Map area chip.
struct AreaBackButtonTests {
    @Test func showsWhileAGroupOfTheMapIsOpenInTheGrid() {
        var area = Msg_PlaceArea()
        area.south = 43.3
        area.west = -5.9
        area.north = 43.4
        area.east = -5.8
        #expect(AreaBackButton.shown(area: area, mapShown: false))
        // A narrow window showing the map in the grid's place: no grid.
        #expect(!AreaBackButton.shown(area: area, mapShown: true))
        // Every place's photos (no group open): nothing to go back to.
        #expect(!AreaBackButton.shown(area: nil, mapShown: false))
        #expect(!AreaBackButton.shown(area: nil, mapShown: true))
    }
}
