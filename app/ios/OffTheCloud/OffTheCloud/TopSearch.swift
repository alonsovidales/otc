// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  TopSearch.swift
//  OffTheCloud
//
//  The search at the top of Images, as the web's top bar search
//  (web/src/components/TopSearch.tsx). With nothing typed there is no
//  panel. While something is typed a panel over the photos offers, in this
//  order: the things (tags) that match - five, those starting with the text
//  first, then those with a word starting with it, then any containing it,
//  without case or accents; the named people that match - the five with the
//  most photos, only while face recognition is on; and the files and
//  folders whose path matches (SearchFiles, asked as the typing pauses), up
//  to eight, with a row to search the documents for the word: every file
//  and folder found (up to 50), listed in Files. That row comes first, and
//  is what the Search key takes, when no tag or person matches; otherwise
//  it follows the files. A device older than SearchFiles has neither.
//
//  Picking a tag or a person narrows Images; picking a folder shows it in
//  Files, and a file opens there as a tap on it in its folder would. The
//  Search key never opens a file or folder by itself.
//
//  The same model and panel serve Files' own field (FilesSearch.swift,
//  the narrow layout only) with the .files scope: no things, people or
//  chips - only the row to search the documents, then the files and
//  folders whose path matches.
//

import SwiftUI

// MARK: - Text matching (the web's filesNav.ts)

/// Text compared without case or accents, so "jose" finds "José". `from`
/// maps each folded scalar back to the Character of the original it came
/// from, to bold the match. Each character goes to upper case and back, as
/// the device's searchFold does: "ς" and "Σ" both become "σ".
struct FoldedText: Equatable {
    let scalars: [Unicode.Scalar]
    let from: [Int]

    init(_ s: String) {
        var out: [Unicode.Scalar] = []
        var from: [Int] = []
        for (i, ch) in s.enumerated() {
            let bare = String(ch).decomposedStringWithCanonicalMapping.unicodeScalars.filter { u in
                switch u.properties.generalCategory {
                case .nonspacingMark, .spacingMark, .enclosingMark: return false
                default: return true
                }
            }
            var plain = String.UnicodeScalarView()
            plain.append(contentsOf: bare)
            for u in String(plain).uppercased().lowercased().unicodeScalars {
                out.append(u)
                from.append(i)
            }
        }
        scalars = out
        self.from = from
    }

    var isEmpty: Bool { scalars.isEmpty }

    /// The folded text itself, to compare two labels.
    var text: String { String(String.UnicodeScalarView(scalars)) }

    func contains(_ q: FoldedText) -> Bool { firstIndex(of: q.scalars, from: 0) != nil }

    fileprivate func firstIndex(of q: [Unicode.Scalar], from start: Int) -> Int? {
        guard !q.isEmpty, q.count <= scalars.count else { return nil }
        var j = start
        while j + q.count <= scalars.count {
            if scalars[j] == q[0] {
                var k = 1
                while k < q.count, scalars[j + k] == q[k] { k += 1 }
                if k == q.count { return j }
            }
            j += 1
        }
        return nil
    }
}

enum SearchMatch {
    /// Where the folded query best matches a label: rank 0 at its start, 1
    /// at the start of a word in it, 2 inside a word; with the Characters of
    /// the label it covers. nil when it isn't there.
    static func match(_ label: FoldedText, _ q: FoldedText) -> (rank: Int, span: Range<Int>)? {
        var rank = -1
        var at = -1
        var j = label.firstIndex(of: q.scalars, from: 0)
        while let found = j {
            let r = found == 0 ? 0 : (isWordScalar(label.scalars[found - 1]) ? 2 : 1)
            if rank < 0 || r < rank {
                rank = r
                at = found
            }
            if r < 2 { break }
            j = label.firstIndex(of: q.scalars, from: found + 1)
        }
        guard rank >= 0 else { return nil }
        return (rank, label.from[at]..<(label.from[at + q.scalars.count - 1] + 1))
    }

    private static func isWordScalar(_ u: Unicode.Scalar) -> Bool {
        switch u.properties.generalCategory {
        case .uppercaseLetter, .lowercaseLetter, .titlecaseLetter, .modifierLetter, .otherLetter,
             .decimalNumber, .letterNumber, .otherNumber:
            return true
        default:
            return false
        }
    }

    /// The label with the part that matched in bold.
    static func marked(_ text: String, _ span: Range<Int>?) -> AttributedString {
        guard let span else { return AttributedString(text) }
        let chars = Array(text)
        let lo = min(max(span.lowerBound, 0), chars.count)
        let hi = min(max(span.upperBound, lo), chars.count)
        var out = AttributedString(String(chars[..<lo]))
        var mid = AttributedString(String(chars[lo..<hi]))
        mid.inlinePresentationIntent = .stronglyEmphasized
        out += mid
        out += AttributedString(String(chars[hi...]))
        return out
    }
}

/// Paths as Files writes them.
enum FilePaths {
    /// The folder a path is in: "/a/b/".
    static func parentFolder(_ path: String) -> String {
        let clean = path.count > 1 && path.hasSuffix("/") ? String(path.dropLast()) : path
        guard let at = clean.lastIndex(of: "/"), at != clean.startIndex else { return "/" }
        return String(clean[...at])
    }

    /// A folder's own path: "/a/b/".
    static func asFolder(_ path: String) -> String { path.hasSuffix("/") ? path : path + "/" }

    /// A found file's name, and the folder it is in as shown with it ("/a/b").
    static func parts(_ path: String) -> (name: String, dir: String) {
        let clean = path.count > 1 && path.hasSuffix("/") ? String(path.dropLast()) : path
        let name = clean.split(separator: "/", omittingEmptySubsequences: false).last.map(String.init) ?? clean
        let dir = parentFolder(clean)
        return (name.isEmpty ? clean : name, dir.count > 1 ? String(dir.dropLast()) : dir)
    }
}

/// What a found file is, for its icon and how it opens.
enum FoundKind {
    case folder, photo, video, doc

    init(_ f: Msg_File) {
        let mime = f.mime
        let lower = f.path.lowercased()
        if mime == "inode/directory" { self = .folder }
        else if mime.hasPrefix("video/") { self = .video }
        else if mime.hasPrefix("image/") || lower.hasSuffix(".heic") || lower.hasSuffix(".heif") { self = .photo }
        else { self = .doc }
    }

    var icon: NavIcon {
        switch self {
        case .folder: return .files
        case .photo: return .images
        case .video: return .video
        case .doc: return .doc
        }
    }

    var word: String {
        switch self {
        case .folder: return "folder"
        case .photo: return "photo"
        case .video: return "video"
        case .doc: return "file"
        }
    }
}

// MARK: - Files, sent somewhere from the search

/// Files sent somewhere from outside it - the search: a folder to show and,
/// maybe, a file in it to open as a tap there would; or a text to search
/// every file and folder for, shown as a list of results over the folder.
/// MainView switches to Files when one comes, and FilesExplorerView takes it
/// (whether it was built already or is built for it). The web's filesNav.ts.
@MainActor
final class FilesNav: ObservableObject {
    static let shared = FilesNav()

    struct Request: Equatable {
        enum Kind: Equatable {
            /// A folder ("/a/b/") and a file in it to open.
            case folder(String, file: Msg_File?)
            /// Every file and folder whose path holds the text.
            case search(String)
        }
        let id = UUID()
        let kind: Kind
    }

    /// The request not yet taken, if any.
    @Published private(set) var pending: Request?

    /// One more for every request: what MainView watches to show Files.
    /// Not `pending` itself - a Files already built takes the request at
    /// once, and can do so before MainView ever saw it set.
    @Published private(set) var sent = 0

    /// The device answered SearchFiles "unknown_payload" (it is older than
    /// this app): the search offers neither files nor the row to search the
    /// documents again until Log Out - whichever asked first says so.
    @Published private(set) var noFileSearch = false

    private init() {}

    func showInFiles(_ dir: String, file: Msg_File? = nil) {
        pending = Request(kind: .folder(dir, file: file))
        sent += 1
    }

    func searchInFiles(_ text: String) {
        pending = Request(kind: .search(text.trimmingCharacters(in: .whitespacesAndNewlines)))
        sent += 1
    }

    /// Files has it: it isn't done again when Files shows next time.
    func take(_ r: Request) {
        if pending?.id == r.id { pending = nil }
    }

    /// Files picked in the wide layout's menu: the folder, not the
    /// results of a search (the web's leaveFilesSearch).
    @Published private(set) var leftSearch = 0

    func leaveSearch() {
        pending = nil
        leftSearch += 1
    }

    func deviceCantSearchFiles() {
        if !noFileSearch { noFileSearch = true }
    }

    /// Log Out.
    func reset() {
        pending = nil
        noFileSearch = false
    }
}

// MARK: - The search

/// One row of the panel, in the order shown.
enum SearchOption: Identifiable {
    case tag(String, span: Range<Int>?)
    case person(Msg_Person, label: String, span: Range<Int>?)
    /// The typed text itself, searched for in every file's path.
    case docs(String)
    case file(Msg_File, kind: FoundKind, name: String, dir: String, nameSpan: Range<Int>?, dirSpan: Range<Int>?)

    var id: String {
        switch self {
        case .tag(let t, _): return "t:\(t)"
        case .person(let p, _, _): return "p:\(p.id)"
        case .docs: return "docs"
        case .file(let f, _, _, _, _, _): return "f:\(f.path)"
        }
    }

    /// What Files does with the row when picked: a folder opens, a file
    /// opens in its folder as a tap there would, the documents row lists
    /// every file and folder found. nil for a tag or a person.
    var inFiles: FilesNav.Request.Kind? {
        switch self {
        case .tag, .person:
            return nil
        case .docs(let text):
            return .search(text)
        case .file(let f, let kind, _, _, _, _):
            return kind == .folder
                ? .folder(FilePaths.asFolder(f.path), file: nil)
                : .folder(FilePaths.parentFolder(f.path), file: f)
        }
    }
}

/// What the panel shows for the text typed.
struct SearchSuggestions {
    var tags: [SearchOption] = []
    var people: [SearchOption] = []
    var files: [SearchOption] = []
    var docs: SearchOption?
    /// What the Search key takes: an exact match, or else the first tag,
    /// person or the documents row - never a file or folder.
    var best: String?

    /// The row to search the documents: first when no tag or person matches.
    var docsFirst: Bool { tags.isEmpty && people.isEmpty }
    var isEmpty: Bool { tags.isEmpty && people.isEmpty && files.isEmpty && docs == nil }

    var bestOption: SearchOption? {
        guard let best else { return nil }
        return (tags + people + files + [docs].compactMap { $0 }).first { $0.id == best }
    }

    /// Every row in the panel's order: what the arrow keys step through.
    var ordered: [SearchOption] {
        let d = [docs].compactMap { $0 }
        return docsFirst ? d + files : tags + people + files + d
    }

    /// The highlighted row, what the Search key takes: where the arrow keys
    /// moved to (TopSearchModel.moved), or else the best suggestion. nil
    /// when the arrows went up past the first row.
    func active(_ moved: String?) -> String? {
        guard let moved else { return best }
        if moved.isEmpty { return nil }
        return ordered.contains { $0.id == moved } ? moved : best
    }

    /// What the Search key takes for `typed`: the highlighted row, or else
    /// the documents searched for the words as typed - nil on a device
    /// that can't search its files (no documents row then), where the
    /// words stay to be changed. As TopSearchActions.submit chooses.
    func searchKey(_ moved: String?, typed: String) -> SearchOption? {
        if let id = active(moved), let o = ordered.first(where: { $0.id == id }) { return o }
        return docs == nil ? nil : .docs(typed)
    }
}

@MainActor
final class TopSearchModel: ObservableObject {
    static let matchTags = 5
    static let matchPeople = 5
    static let matchFiles = 8
    /// The files are asked for once the typing pauses this long.
    static let filesDelay: Duration = .milliseconds(150)

    /// What the search offers.
    enum Scope {
        /// Things, people, files and folders: Images' field and the top bar.
        case everything
        /// Files and folders alone: Files' own field (FilesSearch.swift).
        case files
    }
    let scope: Scope

    init(scope: Scope = .everything) {
        self.scope = scope
    }

    /// Sends the file searches. Tests answer them in the device's place.
    var request: @MainActor (Msg_ReqEnvelope.OneOf_Payload) async throws -> Msg_RespEnvelope = { payload in
        try await OTCConnection.shared.request { $0.payload = payload }
    }

    @Published var query = "" {
        didSet { if query != oldValue { queryChanged() } }
    }

    /// Where a hardware keyboard's arrow keys moved to in the panel: a
    /// row's id, "" for above the first row (the Search key then takes the
    /// words as typed), nil for the best suggestion. Forgotten as the text
    /// changes or the panel closes, as on the web.
    @Published var moved: String?

    /// The panel shows while this is set and something is typed: from the
    /// field taking the focus until a pick, Cancel or the Search key. A
    /// swipe through the suggestions puts the keyboard away and keeps it.
    /// Here rather than in a view: the search is Images' own on a narrow
    /// window and the top bar's on a wide one (AppMenu.swift), and turning
    /// the phone keeps it as it was.
    @Published var open = false {
        didSet { if !open, moved != nil { moved = nil } }
    }

    /// The files the device found for the last search it answered, with
    /// the text it was asked for. None once the field is emptied.
    private struct Found {
        let typed: String
        let q: FoldedText
        let files: [Msg_File]
    }
    @Published private var found: Found?

    /// The last file search sent: an answer to an earlier one is dropped.
    private var seq = 0
    private var debounce: Task<Void, Never>?
    /// The text the files were last asked for, or are about to be: a key
    /// that leaves it as it was (a space) asks nothing again and keeps the
    /// answer on its way, as the web keys its request on the trimmed text.
    private var lastTyped = ""

    var typed: String { query.trimmingCharacters(in: .whitespacesAndNewlines) }

    private func queryChanged() {
        if moved != nil { moved = nil }
        let typed = self.typed
        guard typed != lastTyped else { return }
        lastTyped = typed
        debounce?.cancel()
        guard !typed.isEmpty else {
            // Emptied: forget the last answer, and one still on its way.
            seq += 1
            found = nil
            return
        }
        guard !FilesNav.shared.noFileSearch else { return }
        debounce = Task { [weak self] in
            try? await Task.sleep(for: Self.filesDelay)
            guard !Task.isCancelled else { return }
            self?.askForFiles(typed)
        }
    }

    /// Not cancelled by the next key: an answer for the text before still
    /// stands in while the typing goes on from it (see suggestions).
    private func askForFiles(_ typed: String) {
        seq += 1
        let mine = seq
        let send = request
        Task { [weak self] in
            var req = Msg_SearchFiles()
            req.query = typed
            req.limit = Int32(Self.matchFiles)
            let resp = try? await send(.reqSearchFiles(req))
            guard let self else { return }
            if let resp, resp.error, resp.errorCode == "unknown_payload" { FilesNav.shared.deviceCantSearchFiles() }
            guard mine == self.seq else { return }
            var files: [Msg_File] = []
            if case .respListOfFiles(let lof)? = resp?.payload { files = lof.files }
            self.found = Found(typed: typed, q: FoldedText(typed), files: files)
        }
    }

    // The tags and people, folded once per list rather than per key.
    private var tagSource: [String] = []
    private var tagIndex: [(label: String, f: FoldedText)] = []
    private var peopleSource: [Msg_Person] = []
    private var peopleIndex: [(person: Msg_Person, label: String, f: FoldedText)] = []

    private static func ranked<T>(_ items: [T], _ f: (T) -> FoldedText, _ q: FoldedText) -> [(item: T, rank: Int, span: Range<Int>, order: Int)] {
        var hits: [(item: T, rank: Int, span: Range<Int>, order: Int)] = []
        for (i, item) in items.enumerated() {
            if let m = SearchMatch.match(f(item), q) { hits.append((item, m.rank, m.span, i)) }
        }
        // Stable: in their own order within a rank.
        return hits.sorted { $0.rank != $1.rank ? $0.rank < $1.rank : $0.order < $1.order }
    }

    /// The up and down arrows: through every section in turn. Up past the
    /// first row leaves none highlighted, so the Search key takes the words
    /// as typed (the web's step).
    func step(_ dir: Int, in s: SearchSuggestions) {
        let rows = s.ordered
        guard !rows.isEmpty else { return }
        guard let id = s.active(moved), let i = rows.firstIndex(where: { $0.id == id }) else {
            moved = rows[dir > 0 ? 0 : rows.count - 1].id
            return
        }
        let next = i + dir
        if next < 0 {
            moved = ""
        } else if next < rows.count {
            moved = rows[next].id
        }
    }

    /// Files' own field: the files and folders, and the row to search the
    /// documents (first, and what the Search key takes).
    func fileSuggestions(noFileSearch: Bool) -> SearchSuggestions {
        suggestions(tags: [], inSearch: [], people: [], faces: false, noFileSearch: noFileSearch)
    }

    func suggestions(tags: [String], inSearch: [String], people: [Msg_Person], faces: Bool, noFileSearch: Bool) -> SearchSuggestions {
        let typed = self.typed
        let q = FoldedText(typed)
        var out = SearchSuggestions()
        guard !q.isEmpty else { return out }
        // Files alone: no things or people, whatever is handed in.
        let tags = scope == .files ? [] : tags
        let faces = scope == .files ? false : faces

        if tags != tagSource {
            tagSource = tags
            tagIndex = tags.map { ($0, FoldedText($0)) }
        }
        // Tags already searched for are chips, not suggestions.
        let chips = Set(inSearch.map { FoldedText($0).text })
        let free = tagIndex.filter { !chips.contains($0.f.text) }
        var exact: String?
        for h in Self.ranked(free, { $0.f }, q).prefix(Self.matchTags) {
            let o = SearchOption.tag(h.item.label, span: h.span)
            out.tags.append(o)
            if exact == nil, h.item.f.scalars == q.scalars { exact = o.id }
        }

        // The people with the most photos among those whose name matches.
        if faces {
            if people != peopleSource {
                peopleSource = people
                peopleIndex = people.compactMap { p in
                    let name = p.name.trimmingCharacters(in: .whitespacesAndNewlines)
                    return name.isEmpty ? nil : (p, name, FoldedText(name))
                }
            }
            let hits = Self.ranked(peopleIndex, { $0.f }, q).sorted { a, b in
                if a.item.person.faceCount != b.item.person.faceCount { return a.item.person.faceCount > b.item.person.faceCount }
                if a.rank != b.rank { return a.rank < b.rank }
                return a.order < b.order
            }
            for h in hits.prefix(Self.matchPeople) {
                let o = SearchOption.person(h.item.person, label: h.item.label, span: h.span)
                out.people.append(o)
                if exact == nil, h.item.f.scalars == q.scalars { exact = o.id }
            }
        }

        // The device's answer for the text as it is now. Until that comes,
        // the answer for the text before stands in only while the typing
        // goes on from it, kept to what still matches - narrowing rather
        // than emptying on every key. Never an answer for other text.
        if !noFileSearch, let found {
            let current = found.typed == typed
            if current || q.contains(found.q) {
                for file in found.files {
                    if !current && !FoldedText(file.path).contains(q) { continue }
                    let (name, dir) = FilePaths.parts(file.path)
                    let nameSpan = SearchMatch.match(FoldedText(name), q)?.span
                    let dirSpan = nameSpan == nil ? SearchMatch.match(FoldedText(dir), q)?.span : nil
                    out.files.append(.file(file, kind: FoundKind(file), name: name, dir: dir, nameSpan: nameSpan, dirSpan: dirSpan))
                    if out.files.count == Self.matchFiles { break }
                }
            }
        }
        if !noFileSearch { out.docs = .docs(typed) }
        out.best = exact ?? out.tags.first?.id ?? out.people.first?.id ?? out.docs?.id
        return out
    }
}

// MARK: - What the field and the panel do

/// What the search does when something is picked, the Search key is
/// pressed or it is left - the same from Images' own field (a narrow
/// window) and from the top bar's (a wide one, AppMenu.swift). The views
/// hold the keyboard focus; `focus` moves it.
@MainActor
struct TopSearchActions {
    let vm: PhotoGalleryVM
    let search: TopSearchModel
    /// Face recognition is on: people are offered.
    let faces: Bool
    let filesNav: FilesNav
    /// Puts the keyboard on the field (true) or away (false).
    let focus: (Bool) -> Void
    /// A tag or a person was picked: Images is to show (it already does
    /// where the field is Images' own).
    var showPhotos: () -> Void = {}

    func suggestions() -> SearchSuggestions {
        search.suggestions(
            tags: vm.tags,
            inSearch: vm.chips,
            people: vm.allPeople,
            faces: faces,
            noFileSearch: filesNav.noFileSearch
        )
    }

    /// The Search key: the row the arrow keys moved to, or else the best
    /// suggestion - an exact match, or the first tag or person - or else
    /// the documents searched for the words as typed. Never a file or a
    /// folder unless the arrows went there. A device that can't search its
    /// files leaves the panel saying nothing matches.
    func submit() {
        guard !search.typed.isEmpty else {
            finish()
            // Nothing typed: Images narrowed to the chips, as the web's
            // Enter (from the top bar on another section; Images' own field
            // is there already).
            if !vm.chips.isEmpty || !vm.selectedPeople.isEmpty || vm.activeGroup != nil { showPhotos() }
            return
        }
        let s = suggestions()
        if let id = s.active(search.moved), let o = s.ordered.first(where: { $0.id == id }) {
            pick(o)
        } else if !filesNav.noFileSearch {
            pick(.docs(search.typed))
        } else {
            search.open = true
            // Return took the keyboard away: the words stay, to change them.
            DispatchQueue.main.async { focus(true) }
        }
    }

    func pick(_ o: SearchOption) {
        search.query = ""
        finish()
        switch o {
        case .tag(let tag, _):
            vm.addChip(tag)
            showPhotos()
        case .person(let p, _, _):
            vm.togglePerson(p.id)
            showPhotos()
        case .docs(let text):
            // Every file and folder found, listed in Files.
            filesNav.searchInFiles(text)
        case .file(let f, let kind, _, _, _, _):
            // A folder opens; a file opens in its folder, as a tap there.
            if kind == .folder {
                filesNav.showInFiles(FilePaths.asFolder(f.path))
            } else {
                filesNav.showInFiles(FilePaths.parentFolder(f.path), file: f)
            }
        }
    }

    func finish() {
        search.open = false
        focus(false)
        vm.refreshLibraryIfStale()
    }

    func cancel() {
        search.query = ""
        finish()
    }

    /// A hardware keyboard's up (-1) or down (1) arrow: through the
    /// suggestions; with the panel hidden (Escape), it shows again.
    func arrow(_ dir: Int) -> KeyPress.Result {
        guard !search.typed.isEmpty else { return .ignored }
        if search.open {
            search.step(dir, in: suggestions())
        } else {
            search.open = true
        }
        return .handled
    }

    /// A hardware keyboard's Escape, as the web's: the panel goes and the
    /// field keeps the keyboard; with no panel showing, the field lets go.
    func escape() -> KeyPress.Result {
        if search.open && !search.typed.isEmpty {
            search.open = false
            vm.refreshLibraryIfStale()
        } else {
            finish()
        }
        return .handled
    }

    /// Everything typed and picked goes - not an open collection.
    func clear() {
        search.query = ""
        vm.clearSearch()
    }
}

// MARK: - The panel

/// The suggestions for what is typed, in sections, over the photos.
struct TopSearchPanel: View {
    let suggestions: SearchSuggestions
    let typed: String
    /// A tag typed that is a chip already.
    let alreadyIn: Bool
    let selectedPeople: [String]
    let face: (Msg_Person) -> UIImage?
    let onPick: (SearchOption) -> Void
    /// Where the arrow keys moved to (TopSearchModel.moved).
    var moved: String? = nil
    /// As tall as the rows, up to the height it is offered (the top bar's
    /// dropdown, or a panel risen over the keyboard - SearchPanelLayout).
    /// false: the whole space it is given, over the photos.
    var fitsRows = false
    var background = Color(.systemBackground)

    @State private var rowsHeight: CGFloat = 0

    /// The highlighted row: what the Search key takes.
    private var active: String? { suggestions.active(moved) }
    private static let topID = "top"

    var body: some View {
        ScrollViewReader { proxy in
            rows
                // The row the arrow keys moved to stays in sight; the first
                // shows with its section's heading.
                .onChange(of: moved) { _, m in
                    guard let m, !m.isEmpty else { return }
                    if m == suggestions.ordered.first?.id {
                        proxy.scrollTo(Self.topID, anchor: .top)
                    } else {
                        proxy.scrollTo(m)
                    }
                }
                // A new text, or a new first row (the files came after the
                // tags, say), starts at the top: the list would otherwise
                // keep its place, and the best matches go out of sight
                // above it.
                .onChange(of: FirstRow(typed: typed, id: suggestions.ordered.first?.id)) { _, _ in
                    proxy.scrollTo(Self.topID, anchor: .top)
                }
        }
    }

    private struct FirstRow: Equatable {
        let typed: String
        let id: String?
    }

    private var rows: some View {
        ScrollView {
            // Not lazy: twenty rows at most, and a panel sized to its rows
            // has to measure them all.
            VStack(alignment: .leading, spacing: 0) {
                Color.clear.frame(height: 0).id(Self.topID)
                if !suggestions.tags.isEmpty { section("Things", suggestions.tags) }
                if !suggestions.people.isEmpty { section("People", suggestions.people) }
                if suggestions.docsFirst, let docs = suggestions.docs { row(docs).padding(.top, 6) }
                if !suggestions.files.isEmpty { section("Files", suggestions.files) }
                if !suggestions.docsFirst, let docs = suggestions.docs { row(docs).padding(.top, 6) }
                if suggestions.isEmpty {
                    Text(alreadyIn ? "\u{201C}\(typed)\u{201D} is already in the search." : "Nothing matches \u{201C}\(typed)\u{201D}.")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 20)
                        .padding(.vertical, 14)
                }
            }
            .padding(.bottom, 12)
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { rowsHeight = $0 }
        }
        .frame(maxHeight: fitsRows ? rowsHeight : nil)
        // A swipe through the suggestions puts the keyboard away, and the
        // search stays open.
        .scrollDismissesKeyboard(.immediately)
        .background(background)
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Suggestions")
    }

    private func section(_ title: String, _ options: [SearchOption]) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(title)
                .font(.caption.weight(.semibold))
                .textCase(.uppercase)
                .tracking(0.5)
                .foregroundStyle(.secondary)
                .padding(.horizontal, 16)
                .padding(.top, 12)
                .padding(.bottom, 4)
                .accessibilityAddTraits(.isHeader)
            ForEach(options) { row($0) }
        }
    }

    @ViewBuilder
    private func row(_ o: SearchOption) -> some View {
        Button { onPick(o) } label: {
            HStack(spacing: 12) {
                switch o {
                case .tag(let t, let span):
                    NavIconView(.tag, size: 20)
                        .foregroundStyle(.secondary)
                        .frame(width: 32, height: 32)
                    Text(SearchMatch.marked(t, span)).lineLimit(1)
                    Spacer(minLength: 0)
                case .person(let p, let label, let span):
                    Group {
                        if let img = face(p) {
                            Image(uiImage: img).resizable().scaledToFill()
                        } else {
                            Color(.tertiarySystemFill).overlay(NavIconView(.face, size: 18).foregroundStyle(.secondary))
                        }
                    }
                    .frame(width: 32, height: 32)
                    .clipShape(Circle())
                    .overlay(Circle().stroke(Color.accentColor, lineWidth: selectedPeople.contains(p.id) ? 2 : 0))
                    VStack(alignment: .leading, spacing: 1) {
                        Text(SearchMatch.marked(label, span)).lineLimit(1)
                        Text(Self.photos(Int(p.faceCount))).font(.footnote).foregroundStyle(.secondary).lineLimit(1)
                    }
                    Spacer(minLength: 0)
                    if selectedPeople.contains(p.id) {
                        Image(systemName: "checkmark").font(.body.weight(.semibold)).foregroundStyle(Color.accentColor)
                    }
                case .docs(let text):
                    NavIconView(.docSearch, size: 20)
                        .foregroundStyle(.secondary)
                        .frame(width: 32, height: 32)
                    Text(docsLabel(text)).lineLimit(1)
                    Spacer(minLength: 0)
                case .file(_, let kind, let name, let dir, let nameSpan, let dirSpan):
                    NavIconView(kind.icon, size: 20)
                        .foregroundStyle(kind == .folder ? Color.accentColor : Color.primary)
                        .frame(width: 32, height: 32)
                        .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 8))
                    VStack(alignment: .leading, spacing: 1) {
                        Text(SearchMatch.marked(name, nameSpan)).lineLimit(1).truncationMode(.middle)
                        // Long folders lose their start, not the end nearest the file.
                        Text(SearchMatch.marked(dir, dirSpan))
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.head)
                    }
                    Spacer(minLength: 0)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 6)
            .frame(minHeight: 48)
            .background(o.id == active ? Color.primary.opacity(0.06) : Color.clear)
            .contentShape(Rectangle())
        }
        .buttonStyle(SearchRowStyle())
        .accessibilityLabel(accessibilityLabel(o))
        .accessibilityHint(o.id == active ? "What the Search key picks" : "")
        .id(o.id)
    }

    private func docsLabel(_ text: String) -> AttributedString {
        var out = AttributedString("Search documents for \u{201C}")
        var t = AttributedString(text)
        t.inlinePresentationIntent = .stronglyEmphasized
        out += t
        out += AttributedString("\u{201D}")
        return out
    }

    private func accessibilityLabel(_ o: SearchOption) -> String {
        switch o {
        case .tag(let t, _): return t
        case .person(let p, let label, _):
            return "\(label), \(Self.photos(Int(p.faceCount)))\(selectedPeople.contains(p.id) ? ", in the search" : "")"
        case .docs(let text): return "Search documents for \(text)"
        case .file(_, let kind, let name, let dir, _, _): return "\(name), \(kind.word) in \(dir)"
        }
    }

    static func photos(_ n: Int) -> String { "\(n.formatted()) \(n == 1 ? "photo" : "photos")" }
}

// MARK: - Where the panel goes

/// Where the search's panel goes, and how tall it may be (Android's
/// dropdownPlace): from `under`, under its field, as tall as its rows up
/// to `cap` and the room left down to `bottom` (`shortest` at least), or
/// with no cap the whole room (the narrow panel over the photos).
///
/// With the keyboard up and less than `roomy` under the field - a phone
/// turned sideways, where the keyboard takes most of the window - it would
/// end under the keyboard, its matches out of sight and reach while
/// typing. There it rises over the field instead, as high as `ceiling`
/// (just under the status bar), with its foot just above the keyboard
/// (`bottom` is the keyboard's top then): only as far as its rows need,
/// and back under the field when the keyboard goes. The keyboard's own
/// strip still shows the word being typed.
struct SearchPanelPlace: Equatable {
    static let roomy: CGFloat = 200
    /// Between the panel's foot and the keyboard (or the window's foot).
    static let gap: CGFloat = 8

    var under: CGFloat
    var bottom: CGFloat
    var ceiling: CGFloat
    var keyboardUp: Bool
    var cap: CGFloat? = 640
    var shortest: CGFloat = 88

    /// The room under the field.
    var below: CGFloat { cap == nil ? bottom - under : bottom - Self.gap - under }
    var rises: Bool { keyboardUp && below < Self.roomy }

    /// The tallest the panel may be.
    var maxHeight: CGFloat {
        if rises { return max(0, min(cap ?? .infinity, bottom - Self.gap - ceiling)) }
        guard let cap else { return max(0, below) }
        return max(shortest, min(cap, below))
    }

    /// The panel's top, once it is `height` tall.
    func top(height: CGFloat) -> CGFloat {
        guard rises else { return under }
        return max(ceiling, min(under, bottom - Self.gap - height))
    }
}

/// Lays the search's panel out where SearchPanelPlace puts it, in the space
/// it is given (whose height is the place's bottom): offered the place's
/// tallest, `width` wide (nil: the whole width) from `x`.
struct SearchPanelLayout: Layout {
    let place: SearchPanelPlace
    var x: CGFloat = 0
    var width: CGFloat?

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        proposal.replacingUnspecifiedDimensions()
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var place = place
        place.bottom = bounds.height
        let w = width ?? bounds.width
        for view in subviews {
            let h = min(view.sizeThatFits(ProposedViewSize(width: w, height: place.maxHeight)).height, place.maxHeight)
            view.place(
                at: CGPoint(x: bounds.minX + x, y: bounds.minY + place.top(height: h)),
                anchor: .topLeading,
                proposal: ProposedViewSize(width: w, height: h)
            )
        }
    }
}

extension View {
    /// Keeps `up` saying whether the keyboard shows (a hardware keyboard's
    /// bar counts), as UIKit tells it - before the window is laid out for
    /// it, so the search's panel is placed for the keyboard as it comes.
    func keyboardShown(_ up: Binding<Bool>) -> some View {
        onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in
            up.wrappedValue = true
        }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillHideNotification)) { _ in
            up.wrappedValue = false
        }
    }
}

/// A row darkens while pressed, like a list row.
private struct SearchRowStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .foregroundStyle(.primary)
            .background(configuration.isPressed ? Color.primary.opacity(0.1) : Color.clear)
    }
}
