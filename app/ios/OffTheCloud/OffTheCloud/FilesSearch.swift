// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  FilesSearch.swift
//  OffTheCloud
//
//  Files' own search, at the top of Files in the narrow layout only (a
//  phone held upright, with the tab bar at the foot). It searches the
//  files and folders alone - TopSearchModel with the .files scope: no
//  things, no people, no chips - and shows the same panel as Images' field
//  (TopSearch.swift): the row to search the documents for the words, then
//  the files and folders whose path matches. Picking a folder opens it, a
//  file opens as a tap on it in its folder would, and the Search key lists
//  every file and folder found (up to 50, "Files matching x") - what the
//  Images search sends Files. A wide window has no such field, its top bar
//  searches the files already; nor does a device older than SearchFiles.
//
//  Turning the phone to the wide layout takes the field away (its panel
//  and the keyboard with it), and turning it back brings it again, its
//  panel closed. Its words are its own, not Images': held by the window
//  (MainView), they go to the top bar as the phone turns on Files and
//  come back from it, changed there or not (FilesSearchHandOff); held by
//  Files itself, they wait for the field to come back.
//

import SwiftUI

/// What Files' own field does when something is picked, the Search key is
/// pressed or it is left - TopSearchActions' counterpart without Images.
/// The view holds the keyboard focus; `focus` moves it.
@MainActor
struct FilesSearchActions {
    let search: TopSearchModel
    /// The device answered SearchFiles "unknown_payload" (FilesNav).
    let noFileSearch: Bool
    /// Puts the keyboard on the field (true) or away (false).
    let focus: (Bool) -> Void
    /// Files shows what was picked: a folder, a file in its folder, or
    /// every file and folder the words find.
    let go: (FilesNav.Request.Kind) -> Void

    /// The field is there: the narrow layout, on a device that can search
    /// its files.
    static func shown(wide: Bool, noFileSearch: Bool) -> Bool { !wide && !noFileSearch }

    /// The suggestions cover Files under the field: the field is there,
    /// open, with something typed. What they cover - the path, the folder
    /// or the results, the actions - is out of VoiceOver's reach then too.
    static func covers(wide: Bool, noFileSearch: Bool, open: Bool, typed: String) -> Bool {
        shown(wide: wide, noFileSearch: noFileSearch) && open && !typed.isEmpty
    }

    func suggestions() -> SearchSuggestions {
        search.fileSuggestions(noFileSearch: noFileSearch)
    }

    /// The Search key: the row the arrow keys moved to, or else every file
    /// and folder the words find, listed in Files. Nothing typed, it only
    /// lets go.
    func submit() {
        guard !search.typed.isEmpty else {
            finish()
            return
        }
        if let o = suggestions().searchKey(search.moved, typed: search.typed) {
            pick(o)
        } else {
            search.open = true
            // Return took the keyboard away: the words stay, to change them.
            DispatchQueue.main.async { focus(true) }
        }
    }

    func pick(_ o: SearchOption) {
        guard let kind = o.inFiles else { return }
        search.query = ""
        finish()
        go(kind)
    }

    func finish() {
        search.open = false
        focus(false)
    }

    func cancel() {
        search.query = ""
        finish()
    }

    /// The x in the field: the words go, the keyboard stays.
    func clear() {
        search.query = ""
    }

    /// The window crossed 600 points (turned, folded, resized): the field
    /// goes or comes back, its panel closed. Its words: FilesSearchHandOff.
    func layoutChanged() {
        search.open = false
        focus(false)
    }

    /// A hardware keyboard's up (-1) or down (1) arrow, as in Images.
    func arrow(_ dir: Int) -> KeyPress.Result {
        guard !search.typed.isEmpty else { return .ignored }
        if search.open {
            search.step(dir, in: suggestions())
        } else {
            search.open = true
        }
        return .handled
    }

    /// A hardware keyboard's Escape, as in Images: the panel goes and the
    /// field keeps the keyboard; with no panel showing, the field lets go.
    func escape() -> KeyPress.Result {
        if search.open && !search.typed.isEmpty {
            search.open = false
        } else {
            finish()
        }
        return .handled
    }
}

/// The words of Files' field and of the top bar as the window crosses 600
/// points while Files shows (MainView.layoutChanged), so that the field on
/// screen keeps them. Turned wide, the top bar takes Files' words and
/// Images' are set aside; turned back on Files, Files' field takes the top
/// bar's words, changed there or not, and Images' come back. Turned
/// anywhere else, the top bar's words are Images' as they always were.
struct FilesSearchHandOff {
    /// Images' words while the top bar holds Files'.
    private(set) var imagesWords: String?

    /// `top`: the window's search, Images' field and the top bar; `files`:
    /// Files' own.
    @MainActor
    mutating func layoutChanged(wide: Bool, onFiles: Bool, top: TopSearchModel, files: TopSearchModel) {
        let images = imagesWords
        imagesWords = nil
        guard onFiles else { return }
        if wide {
            imagesWords = top.query
            top.query = files.query
        } else {
            files.query = top.query
            if let images { top.query = images }
        }
    }
}

/// Files' search field: Images' narrow field (PhotoGallery.swift) in look -
/// the capsule with the magnifier, the x while something is typed, and
/// Cancel beside it while it is in use.
struct FilesSearchField: View {
    @ObservedObject var search: TopSearchModel
    var focused: FocusState<Bool>.Binding
    let actions: FilesSearchActions

    /// The field has the keyboard, or its panel shows: Cancel ends it.
    private var active: Bool {
        focused.wrappedValue || (search.open && !search.typed.isEmpty)
    }

    var body: some View {
        HStack(spacing: 8) {
            HStack(spacing: 6) {
                NavIconView(.search, size: 18)
                    .foregroundStyle(.secondary)
                TextField("Search files", text: $search.query)
                    .focused(focused)
                    .submitLabel(.search)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .onSubmit { actions.submit() }
                    // A hardware keyboard: the arrows go through the
                    // suggestions and Escape closes them.
                    .onKeyPress(.upArrow) { actions.arrow(-1) }
                    .onKeyPress(.downArrow) { actions.arrow(1) }
                    .onKeyPress(.escape) { actions.escape() }
                    .accessibilityLabel("Search files")
                if !search.query.isEmpty {
                    Button {
                        actions.clear()
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(.secondary)
                            .frame(width: 28, height: 28)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Clear search")
                }
            }
            .padding(.leading, 12)
            .padding(.trailing, 4)
            .frame(height: 38)
            .background(Color(.tertiarySystemFill), in: Capsule())
            .contentShape(Capsule())
            .onTapGesture { focused.wrappedValue = true }
            if active {
                Button("Cancel") { actions.cancel() }
            }
        }
    }
}

/// The suggestions of Files' field, under it and down to the keyboard (or
/// the foot of Files) - risen over the field, as a card only as tall as
/// its rows, when that space is too short (SearchPanelPlace). As Images'
/// narrow panel.
struct FilesSearchPanel: View {
    @ObservedObject var search: TopSearchModel
    let actions: FilesSearchActions
    /// Where the panel starts: under the field, in this space's coordinates.
    let under: CGFloat
    let keyboardUp: Bool

    var body: some View {
        GeometryReader { proxy in
            let place = SearchPanelPlace(under: under, bottom: proxy.size.height, ceiling: 4, keyboardUp: keyboardUp, cap: nil)
            SearchPanelLayout(place: place) {
                TopSearchPanel(
                    suggestions: actions.suggestions(),
                    typed: search.typed,
                    alreadyIn: false,
                    selectedPeople: [],
                    face: { _ in nil },
                    onPick: actions.pick,
                    moved: search.moved,
                    fitsRows: place.rises
                )
                .clipShape(UnevenRoundedRectangle(bottomLeadingRadius: place.rises ? 16 : 0, bottomTrailingRadius: place.rises ? 16 : 0, style: .continuous))
                .shadow(color: .black.opacity(place.rises ? 0.22 : 0), radius: 24, y: 10)
            }
        }
    }
}
