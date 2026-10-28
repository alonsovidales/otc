// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  PersonMove.swift
//  OffTheCloud
//
//  "Not this person" and "Someone else…" (RemoveFromPerson): what Images'
//  selection bar and the viewer's menu ask before taking photos off the
//  person in the filter (PhotoGalleryVM.removeFromPerson). Not this person
//  asks whether to remove them; Someone else… lists the other people the
//  device found - their faces, names and counts, with a field to find a
//  name in a long list - and files the photos under the one picked.
//

import SwiftUI

/// Whom Someone else… offers, apart from the views to be tested.
enum PersonMove {
    /// Above this many people the list gets a field to find a name.
    static let filterAbove = 8

    static func wantsFilter(_ count: Int) -> Bool { count > filterAbove }

    /// Everyone but `person`, in the People page's order (named first, then
    /// the unnamed, each in the device's order - the most photos first);
    /// with `filter`, those whose name holds it, case and accents aside (an
    /// unnamed person goes by app.shell.search_unnamed).
    static func choices(_ all: [Msg_Person], excluding person: String, filter: String = "") -> [Msg_Person] {
        let others = PeopleOrder.shown(all).filter { $0.id != person }
        let q = FoldedText(filter.trimmingCharacters(in: .whitespacesAndNewlines))
        guard !q.isEmpty else { return others }
        return others.filter { FoldedText(shownName($0)).contains(q) }
    }

    /// A person's name as the list shows it.
    static func shownName(_ p: Msg_Person) -> String {
        let n = PeopleOrder.tidy(p.name)
        return n.isEmpty ? S.appShellSearchUnnamed : n
    }

    /// What VoiceOver reads for a face: the name (or that it has none) and
    /// how many photos show them.
    static func faceLabel(_ p: Msg_Person) -> String {
        let n = PeopleOrder.tidy(p.name)
        return n.isEmpty
            ? S.appPhotosPeopleFaceLabelUnnamed(count: Int(p.faceCount))
            : S.appPhotosPeopleFaceLabel(name: n, count: Int(p.faceCount))
    }

    /// The viewer's menu of the two, by the person it is about.
    static func menuLabel(name: String) -> String {
        name.isEmpty ? S.appPhotosPeopleMoreOptionsUnnamed : S.appPhotosPeopleMoreOptions(name: name)
    }
}

/// Not this person's question and Someone else…'s list, for the asks made
/// `from` the selection bar (PhotoGalleryView) or the viewer (ImageModal):
/// each presents its own, as nothing presented from under the viewer
/// shows over it.
struct PersonActionsPresenter: ViewModifier {
    @ObservedObject var vm: PhotoGalleryVM
    let from: PhotoGalleryVM.PersonPhotos.From

    private var removeAsk: PhotoGalleryVM.PersonPhotos? {
        vm.personRemoveAsk?.from == from ? vm.personRemoveAsk : nil
    }

    func body(content: Content) -> some View {
        content
            .alert(
                removeAsk?.removeTitle ?? "",
                isPresented: Binding(
                    get: { removeAsk != nil },
                    set: { if !$0, removeAsk != nil { vm.personRemoveAsk = nil } }
                ),
                presenting: removeAsk
            ) { ask in
                Button(S.commonRemove, role: .destructive) { Task { await vm.removeFromPerson(ask) } }
                Button(S.commonCancel, role: .cancel) {}
            } message: { _ in
                Text(S.appPhotosPersonRemoveText)
            }
            .sheet(item: Binding(
                get: { vm.personMoveAsk?.from == from ? vm.personMoveAsk : nil },
                set: { if $0 == nil, vm.personMoveAsk?.from == from { vm.personMoveAsk = nil } }
            )) { ask in
                PersonMoveSheet(vm: vm, ask: ask)
            }
            .modifier(ViewerSays(vm: vm, on: from == .viewer))
    }
}

/// Over the viewer, what its menu came to (PhotoGalleryVM.viewerNotice and
/// viewerNeedsUpdate): Images' alerts, presented from under the viewer,
/// would take it away.
private struct ViewerSays: ViewModifier {
    @ObservedObject var vm: PhotoGalleryVM
    let on: Bool

    func body(content: Content) -> some View {
        if on {
            content
                .alert(vm.viewerNotice?.text ?? "", isPresented: Binding(
                    get: { vm.viewerNotice != nil },
                    set: { if !$0 { vm.viewerNotice = nil } }
                )) {
                    Button(S.commonOk, role: .cancel) {}
                }
                .deviceNeedsUpdate($vm.viewerNeedsUpdate)
        } else {
            content
        }
    }
}

/// Someone else…: the other people the device found, to file the photos
/// under the one picked.
struct PersonMoveSheet: View {
    @ObservedObject var vm: PhotoGalleryVM
    let ask: PhotoGalleryVM.PersonPhotos
    @State private var filter = ""

    private var others: [Msg_Person] { PersonMove.choices(vm.allPeople, excluding: ask.person) }

    var body: some View {
        let others = others
        NavigationStack {
            Group {
                if others.isEmpty {
                    if vm.peopleLoaded {
                        Text(S.appPhotosPersonMoveNone)
                            .foregroundStyle(.secondary)
                            .multilineTextAlignment(.center)
                            .padding()
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                    } else {
                        // The list on its way (the first time it is asked).
                        ProgressView()
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                    }
                } else {
                    List(PersonMove.choices(vm.allPeople, excluding: ask.person, filter: filter), id: \.id) { p in
                        row(p)
                    }
                    .listStyle(.plain)
                }
            }
            .navigationTitle(S.appPhotosPersonMoveTitle(count: ask.count))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(S.commonCancel) { vm.personMoveAsk = nil }
                }
            }
            .modifier(NameFilter(on: PersonMove.wantsFilter(others.count), text: $filter))
        }
        .presentationDetents([.medium, .large])
        // Opaque at the medium height too, as the merge's sheet: the grid
        // under a glass sheet makes grey text hard to read.
        .presentationBackground(Color(.systemBackground))
    }

    private func row(_ p: Msg_Person) -> some View {
        let named = !PeopleOrder.tidy(p.name).isEmpty
        return Button {
            Task { await vm.removeFromPerson(ask, to: p) }
        } label: {
            HStack(spacing: 12) {
                Group {
                    if let img = vm.face(for: p) {
                        Image(uiImage: img).resizable().scaledToFill()
                    } else {
                        Color(.tertiarySystemFill)
                            .overlay(NavIconView(.face, size: 22).foregroundStyle(.secondary))
                    }
                }
                .frame(width: 44, height: 44)
                .clipShape(Circle())
                VStack(alignment: .leading, spacing: 2) {
                    Text(PersonMove.shownName(p))
                        .font(.body.weight(named ? .semibold : .regular))
                        .italic(!named)
                        .foregroundStyle(named ? Color.primary : Color.secondary)
                        .lineLimit(1)
                    Text(PeopleOrder.photos(p.faceCount))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .monospacedDigit()
                }
                Spacer(minLength: 0)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(PersonMove.faceLabel(p))
        .accessibilityAddTraits(.isButton)
    }
}

/// A field to find a name in, over a long list of people.
private struct NameFilter: ViewModifier {
    let on: Bool
    @Binding var text: String

    func body(content: Content) -> some View {
        if on {
            content.searchable(text: $text, placement: .navigationBarDrawer(displayMode: .always), prompt: Text(S.commonSearch))
        } else {
            content
        }
    }
}
