// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  SortBy.swift
//  OffTheCloud
//
//  The order of the People and Collections pages (the owner's request,
//  2026-10-30; Android's PeopleSort / CollectionsSort, the same rules,
//  tested with the same cases in SortByTests.swift): a "Sort by" menu in
//  each page's header - the narrow sheet's toolbar, the wide page's own
//  row - with two choices each:
//  - People: Most photos (the order the page always had: named people
//    first, then the unnamed, each in the device's order, most photos
//    first - the default) or Name;
//  - Collections: Newest first (the device's order - the default) or Name.
//  By Name: A to Z in the app language's collation, case and accents
//  ignored, numbers by value (FilesSort.compareNames, as Files' names);
//  those without a name come last, in the device's order, and so do names
//  that compare the same. Kept in UserDefaults like files.viewMode
//  (people.sort, collections.sort), so Log Out forgets them with the
//  rest of the app's defaults, as it does Files' view.

import SwiftUI

/// By name, A to Z; the unnamed last. Ties and the unnamed keep the order
/// they came in (the device's), whatever the sort does with equal keys.
enum NameOrder {
    static func sorted<T>(_ items: [T], locale: Locale, name: (T) -> String) -> [T] {
        let keyed = items.enumerated().map { (index: $0.offset, item: $0.element, name: name($0.element)) }
        let named = keyed.filter { !$0.name.isEmpty }.sorted { a, b in
            switch FilesSort.compareNames(a.name, b.name, locale: locale) {
            case .orderedAscending: return true
            case .orderedDescending: return false
            case .orderedSame: return a.index < b.index
            }
        }
        return named.map(\.item) + keyed.filter { $0.name.isEmpty }.map(\.item)
    }
}

/// People's order.
enum PeopleSort: String, CaseIterable, Identifiable, Sendable {
    /// The device's order, named people first (PeopleOrder.shown).
    case photos
    case name

    var id: String { rawValue }

    static let `default` = PeopleSort.photos
    /// Where it is kept (UserDefaults, as files.viewMode).
    static let storageKey = "people.sort"

    /// A stored choice, or the default for anything else.
    init(stored: String?) {
        self = stored.flatMap(PeopleSort.init(rawValue:)) ?? .default
    }

    var title: String {
        switch self {
        case .photos: return S.appPhotosPeopleSortPhotos
        case .name: return S.appPhotosPeopleSortName
        }
    }

    /// The people as the page shows them.
    func ordered(_ all: [Msg_Person], locale: Locale) -> [Msg_Person] {
        switch self {
        case .photos: return PeopleOrder.shown(all)
        case .name: return NameOrder.sorted(all, locale: locale) { PeopleOrder.tidy($0.name) }
        }
    }
}

/// Collections' order.
enum CollectionsSort: String, CaseIterable, Identifiable, Sendable {
    /// The device's order: the newest first.
    case newest
    case name

    var id: String { rawValue }

    static let `default` = CollectionsSort.newest
    /// Where it is kept (UserDefaults, as files.viewMode).
    static let storageKey = "collections.sort"

    /// A stored choice, or the default for anything else.
    init(stored: String?) {
        self = stored.flatMap(CollectionsSort.init(rawValue:)) ?? .default
    }

    var title: String {
        switch self {
        case .newest: return S.appPhotosCollectionsSortNewest
        case .name: return S.appPhotosCollectionsSortName
        }
    }

    /// The collections as the page and the sheet show them. A name is
    /// compared as shown, without the spaces at its ends; one without a
    /// name (shown as Untitled) comes last.
    func ordered(_ all: [Msg_ImageGroup], locale: Locale) -> [Msg_ImageGroup] {
        switch self {
        case .newest: return all
        case .name: return NameOrder.sorted(all, locale: locale) { $0.name.trimmingCharacters(in: .whitespacesAndNewlines) }
        }
    }
}

/// "Sort by": a menu of the choices, the one in use ticked. In a toolbar
/// (`compact`) an icon; in a page's own header the icon and the choice's
/// name, on a 44 pt high target. VoiceOver reads "Sort by" and the choice.
struct SortByMenu<Choice: Hashable & Identifiable>: View {
    let choices: [Choice]
    @Binding var selection: Choice
    let title: (Choice) -> String
    var compact = false

    var body: some View {
        Menu {
            // Toggles under a titled section, as Files' Sort by: an inline
            // Picker there drops the section's title.
            Section(S.appPhotosSortBy) {
                ForEach(choices) { c in
                    Toggle(title(c), isOn: Binding(get: { selection == c }, set: { if $0 { selection = c } }))
                }
            }
        } label: {
            if compact {
                Image(systemName: "arrow.up.arrow.down")
            } else {
                HStack(spacing: 6) {
                    Image(systemName: "arrow.up.arrow.down")
                        .font(.footnote.weight(.semibold))
                    Text(title(selection))
                        .lineLimit(1)
                    Image(systemName: "chevron.down")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.secondary)
                }
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.primary)
                .padding(.horizontal, 12)
                .frame(minHeight: 44)
                .contentShape(Rectangle())
            }
        }
        .accessibilityLabel(S.appPhotosSortBy)
        .accessibilityValue(title(selection))
    }
}

/// A page's own header on the wide layout, where no title bar shows: the
/// Sort by menu at its end.
struct SortByHeader<Choice: Hashable & Identifiable>: View {
    let choices: [Choice]
    @Binding var selection: Choice
    let title: (Choice) -> String

    var body: some View {
        HStack {
            Spacer(minLength: 0)
            SortByMenu(choices: choices, selection: $selection, title: title)
        }
    }
}
