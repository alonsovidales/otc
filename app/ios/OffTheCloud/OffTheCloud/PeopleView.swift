// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  PeopleView.swift
//  OffTheCloud
//
//  The People page, as the web's PeopleView.tsx: the faces the device
//  found, named people first, each a circle with the name and the photo
//  count under it that opens Images on that person. Images' People button
//  (next to Collections) opens it, only while face recognition is on
//  (FaceRecognition.swift).
//
//  "Add a name" under an unnamed face, or a face's "more" menu, names a
//  person in place. A long press picks a face, and once one is picked a
//  tap picks or unpicks; the bar that then shows merges them into one
//  (face matching split one person in several) or deletes them. The list
//  and the requests are PhotoGalleryVM's, so the search offers the same
//  people and a person merged or deleted stops narrowing Images.
//

import SwiftUI

// MARK: - Order and wording

/// The order the page shows people in and the merge's choices, as the web
/// works them out - apart from the views, to be tested.
enum PeopleOrder {
    /// A name as it is saved: spaces run together, none at either end.
    static func tidy(_ s: String) -> String {
        s.split(whereSeparator: \.isWhitespace).joined(separator: " ")
    }

    /// For telling names apart: Ana and ana are one.
    static func fold(_ s: String) -> String { tidy(s).lowercased() }

    /// Named people first, then the unnamed, each in the device's order
    /// (dao.ListPeople: the most photos first).
    static func shown(_ all: [Msg_Person]) -> [Msg_Person] {
        all.filter { !tidy($0.name).isEmpty } + all.filter { tidy($0.name).isEmpty }
    }

    /// The different names among some people, each once.
    static func names(_ list: [Msg_Person]) -> [String] {
        var seen = Set<String>()
        var out: [String] = []
        for p in list {
            let n = tidy(p.name)
            if !n.isEmpty, seen.insert(fold(n)).inserted { out.append(n) }
        }
        return out
    }

    /// The face a merge keeps unless another is picked, when the choice is
    /// plain: the one named person (the one of them with the most photos,
    /// when several share that name), or, when nobody has a name, the face
    /// with the most photos. People with different names need a pick: only
    /// one name can stay.
    static func firstKeep(_ list: [Msg_Person]) -> String? {
        let named = list.filter { !tidy($0.name).isEmpty }
        if names(named).count > 1 { return nil }
        let from = named.isEmpty ? list : named
        guard let first = from.first else { return nil }
        return from.dropFirst().reduce(first) { $1.faceCount > $0.faceCount ? $1 : $0 }.id
    }

    /// "Ana", "Ana and Leo", "Ana, Leo and Rosa".
    static func andList(_ xs: [String]) -> String {
        guard xs.count > 1 else { return xs.first ?? "" }
        return xs.dropLast().joined(separator: ", ") + " and " + xs[xs.count - 1]
    }

    static func counted(_ n: Int, _ one: String, _ many: String) -> String {
        "\(n.formatted()) \(n == 1 ? one : many)"
    }

    static func photos(_ n: Int32) -> String { counted(Int(n), "photo", "photos") }
}

// MARK: - What outlives the page

/// What the People page is in the middle of, kept by PhotoGalleryVM rather
/// than the view: turning the phone swaps the sheet for the wide layout's
/// page (and back), and the faces picked, a merge or a delete being asked
/// about, a delete under way, names being saved and the last note carry
/// over to the copy that shows next.
@MainActor
final class PeoplePageState: ObservableObject {
    struct MergeAsk: Identifiable {
        let id = UUID()
        let people: [Msg_Person]
    }

    struct DeleteProgress {
        var done = 0
        let total: Int
    }

    struct Toast: Equatable {
        let id = UUID()
        let text: String
        let error: Bool
    }

    /// A merge sent to the device and not answered yet: the face kept and
    /// the name it was asked for.
    struct MergeSent: Equatable {
        let keep: String
        let name: String
    }

    // Picked, in the order they were picked; shown in the page's own.
    @Published var sel: [String] = []
    // Started from a face's "Merge with others…": the bar says what to do.
    @Published var mergeHint = false
    @Published var mergeAsk: MergeAsk?
    /// The merge on its way. The sheet shows it busy - also the copy asked
    /// again after the phone turned, so the merge can't be sent twice.
    @Published private(set) var merging: MergeSent?
    /// What went wrong with the last merge, for the sheet to say.
    @Published var mergeError: String?
    @Published var deleteAsk: [String]?
    @Published var deleteProgress: DeleteProgress?
    @Published var toast: Toast?
    // Names on their way to the device, shown dimmed until it answers.
    @Published var saving: [String: String] = [:]

    /// The face at the top of the grid, where the copy that shows next
    /// after a hand-off starts scrolled to. Not published: the grid keeps
    /// it as it scrolls, without redrawing the page.
    var topPerson: String?

    /// The page is moving: the copy that shows next carries on.
    private var handoff = false
    /// A merge or delete question open as the page moved, asked again by
    /// the copy that shows next.
    private var openMerge: MergeAsk?
    private var openDelete: [String]?

    /// MainView.layoutChanged: the sheet becomes the page, or back. A
    /// question open now is closed while the copy that asked it is still
    /// there to close it (one closed as its copy goes can stay on screen,
    /// out of reach), to be asked again by the next copy (askAgain).
    func handOff() {
        // Turned twice before the next copy showed: the first one's.
        openMerge = mergeAsk ?? (handoff ? openMerge : nil)
        openDelete = deleteAsk ?? (handoff ? openDelete : nil)
        handoff = true
        mergeAsk = nil
        deleteAsk = nil
    }

    /// A copy of the page shows; true when it carries on from a hand-off.
    /// Any other visit starts at the top with nothing picked or asked; a
    /// delete under way still finishes and says how it went, and names
    /// being saved stay dimmed until saved.
    @discardableResult
    func arrive() -> Bool {
        if handoff {
            handoff = false
            return true
        }
        openMerge = nil
        openDelete = nil
        topPerson = nil
        sel = []
        mergeHint = false
        mergeAsk = nil
        mergeError = nil
        deleteAsk = nil
        if deleteProgress == nil { toast = nil }
        return false
    }

    /// After a hand-off: the question that was open, asked again - a merge
    /// still on its way shows busy. A question about someone who has gone
    /// meanwhile (merged or deleted elsewhere, or by the merge that was on
    /// its way) is not asked again, as the web's confirmMoot.
    func askAgain(present: Set<String>) {
        defer {
            openMerge = nil
            openDelete = nil
        }
        guard !handoff, mergeAsk == nil, deleteAsk == nil else { return }
        if let m = openMerge {
            if m.people.allSatisfy({ present.contains($0.id) }) { mergeAsk = m }
        } else if let d = openDelete {
            let left = d.filter { present.contains($0) }
            if !left.isEmpty { deleteAsk = left }
        }
    }

    /// The Merge button: the merge goes out. false while one is on its way.
    func startMerge(keep: String, name: String) -> Bool {
        guard merging == nil else { return false }
        merging = MergeSent(keep: keep, name: name)
        mergeError = nil
        return true
    }

    /// The device answered the merge: nil once merged - the question closes
    /// (in whichever copy asks it, or is about to after a turn) and nothing
    /// stays picked; else what went wrong, for the sheet to say.
    func mergeAnswered(_ error: String?) {
        merging = nil
        if let error {
            mergeError = error
            return
        }
        mergeAsk = nil
        openMerge = nil
        mergeError = nil
        sel = []
    }
}

// MARK: - The page

struct PeopleView: View {
    @ObservedObject var vm: PhotoGalleryVM
    /// A face was opened (vm.showPerson already called): show Images.
    let onOpenPhotos: () -> Void
    /// Where the page is a sheet: its Done button.
    var onClose: (() -> Void)? = nil

    /// The selection, the questions and the notes (vm.peoplePage).
    @ObservedObject private var state: PeoplePageState

    /// The wide layout's page: the menu names it, so no title shows.
    @Environment(\.wideLayout) private var wide

    init(vm: PhotoGalleryVM, onOpenPhotos: @escaping () -> Void, onClose: (() -> Void)? = nil) {
        self.vm = vm
        self.onOpenPhotos = onOpenPhotos
        self.onClose = onClose
        _state = ObservedObject(wrappedValue: vm.peoplePage)
    }

    // The list is asked for again on every visit (new faces are found as
    // photos arrive); what is already here shows meanwhile.
    @State private var asking = false
    @State private var askedOnce = false
    @State private var slow = false

    @State private var picks = 0
    /// The face at the top of the grid (state.topPerson).
    @State private var scrolledTo: String?

    @State private var editingID: String?
    @State private var editText = ""
    // The whole name, selected as the field opens: typing replaces it.
    @State private var editSelection: TextSelection?
    // Which face's name field has the keyboard. By person, so that a field
    // that goes away ends only its own edit, never the one started after it.
    @FocusState private var nameFocus: String?

    private var sel: [String] {
        get { state.sel }
        nonmutating set { state.sel = newValue }
    }
    private var mergeHint: Bool {
        get { state.mergeHint }
        nonmutating set { state.mergeHint = newValue }
    }
    private var mergeAsk: PeoplePageState.MergeAsk? {
        get { state.mergeAsk }
        nonmutating set { state.mergeAsk = newValue }
    }
    private var deleteAsk: [String]? {
        get { state.deleteAsk }
        nonmutating set { state.deleteAsk = newValue }
    }
    private var deleteProgress: PeoplePageState.DeleteProgress? {
        get { state.deleteProgress }
        nonmutating set { state.deleteProgress = newValue }
    }
    private var toast: PeoplePageState.Toast? {
        get { state.toast }
        nonmutating set { state.toast = newValue }
    }
    private var saving: [String: String] {
        get { state.saving }
        nonmutating set { state.saving = newValue }
    }

    private static let cols = [GridItem(.adaptive(minimum: 108, maximum: 168), spacing: 8, alignment: .top)]
    private static let deleteText = "Their photos stay; only the faces matched to them are forgotten. This can't be undone, and they'll come back as new people in photos added later."

    private var people: [Msg_Person] { PeopleOrder.shown(vm.allPeople) }
    private var selPeople: [Msg_Person] {
        let picked = Set(sel)
        return people.filter { picked.contains($0.id) }
    }
    private var selecting: Bool { !sel.isEmpty }
    private var canMerge: Bool { selPeople.count > 1 }
    private var busy: Bool { deleteProgress != nil }

    var body: some View {
        content
            .navigationTitle(selecting ? "\(sel.count.formatted()) selected" : "People")
            .navigationBarTitleDisplayMode(.inline)
            // The wide layout's menu names the page, as the web's does: no
            // title bar there, but for the count and the X while picking.
            .toolbar(wide && !selecting ? .hidden : .automatic, for: .navigationBar)
            .overlay(alignment: .topLeading) {
                if wide && !selecting { HiddenPageHeading(title: "People") }
            }
            .toolbar {
                if selecting {
                    ToolbarItem(placement: .topBarLeading) {
                        Button {
                            clearSel()
                        } label: {
                            Image(systemName: "xmark")
                        }
                        .accessibilityLabel("Clear selection")
                        .disabled(busy)
                    }
                } else if let onClose {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button("Done", action: onClose)
                    }
                }
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                VStack(spacing: 8) {
                    if let toast {
                        Text(toast.text)
                            .font(.subheadline)
                            .multilineTextAlignment(.center)
                            .foregroundStyle(toast.error ? Color.red : Color.primary)
                            .padding(.horizontal, 16)
                            .padding(.vertical, 10)
                            .modifier(CapsuleBarBackground())
                            .padding(.horizontal, 24)
                            .transition(.opacity.combined(with: .move(edge: .bottom)))
                    }
                    if let p = deleteProgress {
                        HStack(spacing: 10) {
                            ProgressView()
                            Text(p.total > 1 ? "Deleting \(min(p.done + 1, p.total)) of \(p.total)…" : "Deleting…")
                                .font(.subheadline)
                                .monospacedDigit()
                        }
                        .padding(.horizontal, 16)
                        .padding(.vertical, 10)
                        .modifier(CapsuleBarBackground())
                    }
                    if selecting { selectionBar }
                }
                .padding(.bottom, 8)
                .animation(.easeInOut(duration: 0.2), value: toast)
                .animation(.easeInOut(duration: 0.15), value: selecting)
            }
            .onAppear {
                guard state.arrive() else { return }
                // Turning the phone: the faces that were in view stay in view.
                scrolledTo = state.topPerson
                // Turning the phone: a question the other copy had open is
                // asked again once that copy's sheet has gone (asked at
                // once, it comes while that sheet is still closing and
                // doesn't show).
                let state = state, vm = vm
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.6) {
                    state.askAgain(present: Set(vm.allPeople.map(\.id)))
                }
            }
            .task { await ask() }
            .task(id: toast) {
                guard let t = toast else { return }
                try? await Task.sleep(for: .seconds(t.error ? 6 : 3.2))
                if !Task.isCancelled, toast == t { toast = nil }
            }
            // The first listing taking a while: offered again.
            .task(id: vm.peopleLoaded) {
                guard !vm.peopleLoaded else { return }
                try? await Task.sleep(for: .seconds(10))
                if !Task.isCancelled { slow = true }
            }
            // Merged or deleted elsewhere, or a new listing without them:
            // no longer picked.
            .onChange(of: vm.allPeople.map(\.id)) { _, ids in
                let here = Set(ids)
                let kept = sel.filter { here.contains($0) }
                if kept.count != sel.count { sel = kept }
                // A merge asked about someone gone meanwhile: no question
                // left (the web's confirmMoot) - not while it is on its way.
                if let m = mergeAsk, state.merging == nil, !m.people.allSatisfy({ here.contains($0.id) }) {
                    mergeAsk = nil
                }
            }
            .onChange(of: selecting) { _, on in if !on { mergeHint = false } }
            .onChange(of: nameFocus) { left, now in
                if let now, now == editingID {
                    // The name, selected once the field has the keyboard
                    // (set any sooner, the field puts its cursor at the end).
                    DispatchQueue.main.async {
                        let t = editText
                        if editingID == now, !t.isEmpty { editSelection = TextSelection(range: t.startIndex..<t.endIndex) }
                    }
                }
                // Leaving a field (a scroll, a tap elsewhere) saves it - its
                // own edit only: "Add a name" under another face has saved
                // this one already and opened that one's.
                if let left, left != now, editingID == left {
                    endRename(editText, fromBlur: true)
                }
            }
            .onDisappear {
                if editingID != nil { endRename(editText, fromBlur: true) }
            }
            .sensoryFeedback(.selection, trigger: picks)
            .interactiveDismissDisabled(busy)
            .sheet(item: $state.mergeAsk) { ask in
                MergeSheet(
                    people: ask.people,
                    state: state,
                    face: vm.face(for:),
                    onCancel: {
                        mergeAsk = nil
                        state.mergeError = nil
                    },
                    onMerge: merge
                )
            }
            .alert(
                deleteTitle,
                isPresented: Binding(get: { deleteAsk != nil }, set: { if !$0 { deleteAsk = nil } }),
                presenting: deleteAsk
            ) { ids in
                Button("Delete", role: .destructive) {
                    Task { await remove(ids) }
                }
                Button("Cancel", role: .cancel) {}
            } message: { _ in
                Text(Self.deleteText)
            }
    }

    // MARK: The states

    @ViewBuilder
    private var content: some View {
        if vm.peopleLoaded && people.isEmpty {
            stateView(
                art: AnyView(NavIconView(.people, size: 72, stroke: 1.2)),
                title: "No faces found yet",
                text: "Face recognition runs on the device after photos are uploaded. You can switch it on in Settings."
            )
        } else if vm.peopleLoaded {
            grid
        } else if (askedOnce && !asking) || slow {
            let failed = askedOnce && !asking
            stateView(
                art: AnyView(Image(systemName: "exclamationmark.circle").font(.system(size: 40, weight: .light))),
                title: failed ? "Couldn't load people" : "Still waiting for your device",
                text: failed ? "Check that your device is online, then try again." : "People are taking longer than usual to load.",
                retry: true
            )
        } else {
            skeleton
        }
    }

    private func stateView(art: AnyView, title: String, text: String, retry: Bool = false) -> some View {
        VStack(spacing: 12) {
            art.foregroundStyle(.secondary)
            Text(title).font(.title3.weight(.semibold))
            Text(text)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            if retry {
                Button("Try again") {
                    slow = false
                    Task { await ask() }
                }
                .buttonStyle(.borderedProminent)
                .disabled(asking)
                .padding(.top, 4)
            }
        }
        .padding(32)
        .frame(maxWidth: 420)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var skeleton: some View {
        ScrollView {
            LazyVGrid(columns: Self.cols, spacing: 24) {
                ForEach(0..<12, id: \.self) { i in
                    VStack(spacing: 0) {
                        Circle().frame(width: PersonTile.side, height: PersonTile.side)
                        Capsule().frame(width: [64, 52, 76][i % 3], height: 12).frame(height: 32).padding(.top, 8)
                        Capsule().frame(width: 44, height: 8)
                    }
                    .foregroundStyle(Color(.tertiarySystemFill))
                }
            }
            .padding(16)
            .modifier(Pulse())
        }
        .scrollDisabled(true)
        .accessibilityLabel("Loading people")
    }

    private var grid: some View {
        ScrollView {
            LazyVGrid(columns: Self.cols, spacing: 24) {
                ForEach(people, id: \.id) { p in
                    let pending = saving[p.id]
                    PersonTile(
                        person: p,
                        face: vm.face(for: p),
                        name: PeopleOrder.tidy(pending ?? p.name),
                        pending: pending != nil,
                        editing: editingID == p.id,
                        editText: $editText,
                        editSelection: $editSelection,
                        nameFocus: $nameFocus,
                        selecting: selecting,
                        selected: sel.contains(p.id),
                        canMerge: people.count > 1,
                        onTap: { tap(p) },
                        onLongPress: { pick(p.id) },
                        onRename: { startRename(p) },
                        onMergeWithOthers: { mergeWithOthers(p) },
                        onDelete: { deleteAsk = [p.id] },
                        onSubmitName: { endRename(editText, fromBlur: false) },
                        onCancelName: { endRename(nil, fromBlur: false) }
                    )
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 20)
            .scrollTargetLayout()
        }
        .scrollPosition(id: $scrolledTo, anchor: .top)
        .onChange(of: scrolledTo) { _, id in state.topPerson = id }
        .scrollDismissesKeyboard(.immediately)
        .disabled(busy)
        .refreshable { await ask() }
    }

    // MARK: The selection bar

    private var selectionBar: some View {
        VStack(spacing: 6) {
            if mergeHint && !canMerge {
                Text("Select the faces of the same person")
                    .font(.caption)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 5)
                    .modifier(CapsuleBarBackground())
            }
            HStack(spacing: 0) {
                barItem("Merge", tint: .accentColor, dimmed: !canMerge) {
                    NavIconView(.merge, size: 24)
                } action: {
                    guard canMerge else {
                        say("Select 2 or more faces to merge them into one.")
                        return
                    }
                    state.mergeError = nil
                    mergeAsk = PeoplePageState.MergeAsk(people: selPeople)
                }
                .accessibilityHint(canMerge ? "" : "Select 2 or more to merge")
                barItem("Delete", tint: .red, dimmed: false) {
                    Image(systemName: "trash").font(.system(size: 20))
                } action: {
                    deleteAsk = selPeople.map(\.id)
                }
            }
            .padding(.vertical, 10)
            .padding(.horizontal, 8)
            .frame(maxWidth: 260)
            .modifier(CapsuleBarBackground())
        }
        .padding(.horizontal, 24)
        .disabled(busy)
        .transition(.move(edge: .bottom).combined(with: .opacity))
    }

    private func barItem<Icon: View>(
        _ title: String, tint: Color, dimmed: Bool,
        @ViewBuilder icon: () -> Icon, action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            VStack(spacing: 3) {
                icon().frame(height: 22)
                Text(title).font(.caption2)
            }
            .frame(maxWidth: .infinity)
            .contentShape(Rectangle())
            // There, dimmed, and says why when pressed (as on the web).
            .opacity(dimmed ? 0.45 : 1)
        }
        .tint(tint)
        .foregroundStyle(tint)
    }

    // MARK: Picking, opening

    private func tap(_ p: Msg_Person) {
        // A tap while a name is being typed only ends the typing.
        if editingID != nil {
            nameFocus = nil
            return
        }
        if selecting {
            toggle(p.id)
        } else {
            vm.showPerson(p.id)
            onOpenPhotos()
        }
    }

    private func pick(_ id: String) {
        if editingID != nil { nameFocus = nil }
        picks += 1
        if !sel.contains(id) { sel.append(id) }
    }

    private func toggle(_ id: String) {
        picks += 1
        if let i = sel.firstIndex(of: id) { sel.remove(at: i) } else { sel.append(id) }
    }

    private func clearSel() {
        sel = []
    }

    /// "Merge with others…": a selection with this person in it, and the
    /// bar says what to pick next.
    private func mergeWithOthers(_ p: Msg_Person) {
        sel = [p.id]
        mergeHint = true
    }

    private func say(_ text: String, error: Bool = false) {
        toast = PeoplePageState.Toast(text: text, error: error)
        AccessibilityNotification.Announcement(text).post()
    }

    // MARK: The list

    private func ask() async {
        guard !asking else { return }
        asking = true
        await vm.loadPeople()
        asking = false
        askedOnce = true
    }

    // MARK: Renaming

    private func startRename(_ p: Msg_Person) {
        if let other = editingID {
            // Its own field already open: the keyboard back on it.
            if other == p.id {
                nameFocus = p.id
                return
            }
            // Another face's name being typed is saved first, as leaving
            // its field would (the web saves it as the field loses focus).
            endRename(editText, fromBlur: true)
        }
        editingID = p.id
        editText = PeopleOrder.tidy(saving[p.id] ?? p.name)
        editSelection = nil
        // Once the field is on screen.
        DispatchQueue.main.async {
            if editingID == p.id { nameFocus = p.id }
        }
    }

    /// typed is nil to cancel. Leaving a field emptied by accident (a
    /// scroll, a tap elsewhere) keeps the name; Done on an empty field
    /// takes it off.
    private func endRename(_ typed: String?, fromBlur: Bool) {
        guard let id = editingID else { return }
        editingID = nil
        if nameFocus != nil { nameFocus = nil }
        guard let typed, let p = vm.allPeople.first(where: { $0.id == id }) else { return }
        let name = PeopleOrder.tidy(typed)
        if name == PeopleOrder.tidy(p.name) || (fromBlur && name.isEmpty) { return }
        saving[id] = name
        let state = state
        Task {
            let res = await vm.renamePerson(id, to: name)
            state.saving[id] = nil
            if res != .ok { say("The name couldn't be saved. Try again.", error: true) }
        }
    }

    // MARK: Merging and deleting

    /// The merge sheet's Merge. The answer goes to the page's state, not to
    /// this copy: turning the phone meanwhile swaps the sheet for another
    /// copy's, which shows the merge busy and then how it went.
    private func merge(keep: Msg_Person, name: String) {
        guard let ask = mergeAsk, state.startMerge(keep: keep.id, name: name) else { return }
        let sources = ask.people.map(\.id).filter { $0 != keep.id }
        let state = state, vm = vm
        Task {
            switch await vm.mergePeople(keep: keep, sources: sources, name: name) {
            case .merged:
                state.mergeAnswered(nil)
                let n = sources.count + 1
                let text = name.isEmpty ? "Merged \(n) faces" : "Merged \(n) into \(name)"
                state.toast = PeoplePageState.Toast(text: text, error: false)
                AccessibilityNotification.Announcement(text).post()
            case .nameNotSaved:
                state.mergeAnswered("The name couldn't be saved, so nothing was merged. Try again.")
            case .notMerged:
                state.mergeAnswered("They couldn't be merged. Try again.")
            case .unanswered:
                state.mergeAnswered("Your device didn't answer. Try again.")
            }
        }
    }

    private var deleteTitle: String {
        guard let ids = deleteAsk else { return "" }
        if ids.count == 1 {
            let name = PeopleOrder.tidy(vm.allPeople.first { $0.id == ids[0] }?.name ?? "")
            return name.isEmpty ? "Delete this person?" : "Delete \(name)?"
        }
        return "Delete \(ids.count.formatted()) people?"
    }

    /// What couldn't be deleted stays picked, to try again.
    private func remove(_ ids: [String]) async {
        guard !ids.isEmpty, deleteProgress == nil else { return }
        deleteProgress = PeoplePageState.DeleteProgress(total: ids.count)
        let (deleted, unanswered) = await vm.deletePeople(ids) { deleteProgress?.done += 1 }
        deleteProgress = nil
        if deleted.isEmpty {
            say(unanswered == ids.count
                ? "Your device didn't answer. Try again."
                : ids.count == 1 ? "This person couldn't be deleted. Try again." : "They couldn't be deleted. Try again.",
                error: true)
            return
        }
        sel.removeAll { deleted.contains($0) }
        let failed = ids.count - deleted.count
        if failed == 0 {
            say(ids.count == 1 ? "Person deleted" : "Deleted \(PeopleOrder.counted(ids.count, "person", "people"))")
        } else {
            // "1 of 2 people couldn't be deleted and is still selected."
            say("\(failed.formatted()) of \(PeopleOrder.counted(ids.count, "person", "people")) couldn't be deleted and \(failed == 1 ? "is" : "are") still selected. Try again.", error: true)
        }
    }
}

// MARK: - One person

private struct PersonTile: View {
    static let side: CGFloat = 96

    let person: Msg_Person
    let face: UIImage?
    /// What shows as the name: one being saved, else the person's own.
    let name: String
    let pending: Bool
    let editing: Bool
    @Binding var editText: String
    @Binding var editSelection: TextSelection?
    var nameFocus: FocusState<String?>.Binding
    let selecting: Bool
    let selected: Bool
    let canMerge: Bool
    let onTap: () -> Void
    let onLongPress: () -> Void
    let onRename: () -> Void
    let onMergeWithOthers: () -> Void
    let onDelete: () -> Void
    let onSubmitName: () -> Void
    let onCancelName: () -> Void

    private var faceLabel: String {
        "\(name.isEmpty ? "Unnamed person" : name), \(PeopleOrder.photos(person.faceCount))"
    }

    var body: some View {
        VStack(spacing: 0) {
            faceCircle
                .overlay(alignment: .topLeading) {
                    if selecting { check.offset(x: -4, y: -4) }
                }
                .overlay(alignment: .topTrailing) {
                    if !selecting { more.offset(x: 6, y: -6) }
                }
            label
                .frame(height: 32)
                .frame(maxWidth: .infinity)
                .padding(.top, 8)
            Text(PeopleOrder.photos(person.faceCount))
                .font(.caption)
                .foregroundStyle(.secondary)
                .monospacedDigit()
                .lineLimit(1)
        }
        .animation(.easeOut(duration: 0.15), value: selected)
    }

    private var faceCircle: some View {
        Group {
            if let face {
                Image(uiImage: face).resizable().scaledToFill()
            } else {
                Color(.tertiarySystemFill)
                    .overlay(NavIconView(.face, size: 44, stroke: 1.1).foregroundStyle(.secondary))
            }
        }
        .frame(width: Self.side, height: Self.side)
        .clipShape(Circle())
        // Picked: a ring around a face that steps back a little.
        .scaleEffect(selected ? 0.88 : 1)
        .overlay {
            if selected {
                Circle().strokeBorder(Color.accentColor, lineWidth: 3)
            }
        }
        .contentShape(Circle())
        .onTapGesture(perform: onTap)
        .onLongPressGesture(minimumDuration: 0.45, perform: onLongPress)
        .accessibilityElement()
        .accessibilityLabel(faceLabel)
        .accessibilityAddTraits(selecting && selected ? [.isButton, .isSelected] : .isButton)
        .accessibilityAction(named: selecting ? (selected ? "Deselect" : "Select") : "Select", selecting ? onTap : onLongPress)
    }

    private var check: some View {
        ZStack {
            Circle()
                .fill(selected ? Color.accentColor : Color.black.opacity(0.35))
            Circle()
                .strokeBorder(selected ? Color.accentColor : Color.white.opacity(0.92), lineWidth: 2)
            if selected {
                Image(systemName: "checkmark")
                    .font(.system(size: 12, weight: .bold))
                    .foregroundStyle(.white)
            }
        }
        .frame(width: 26, height: 26)
        .shadow(color: .black.opacity(0.35), radius: 2, y: 1)
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }

    private var more: some View {
        Menu {
            Button(action: onRename) {
                Label(name.isEmpty ? "Add a name" : "Rename", systemImage: "pencil")
            }
            if canMerge {
                Button(action: onMergeWithOthers) {
                    Label { Text("Merge with others…") } icon: { NavIcon.merge.image(size: 22) }
                }
            }
            Button(role: .destructive, action: onDelete) {
                Label("Delete", systemImage: "trash")
            }
        } label: {
            Image(systemName: "ellipsis")
                .font(.system(size: 14, weight: .semibold))
                .foregroundStyle(.primary)
                .frame(width: 30, height: 30)
                .background(Circle().fill(Color(.secondarySystemBackground)))
                .overlay(Circle().strokeBorder(Color(.separator), lineWidth: 0.5))
                .shadow(color: .black.opacity(0.2), radius: 4, y: 1)
                .frame(width: 44, height: 44)
                .contentShape(Circle())
        }
        .tint(.primary)
        .accessibilityLabel("More options for \(name.isEmpty ? "this person" : name)")
    }

    @ViewBuilder
    private var label: some View {
        if editing {
            TextField("Name", text: $editText, selection: $editSelection)
                .focused(nameFocus, equals: person.id)
                .font(.subheadline)
                .multilineTextAlignment(.center)
                .textInputAutocapitalization(.words)
                .autocorrectionDisabled()
                .submitLabel(.done)
                .onSubmit(onSubmitName)
                .onKeyPress(.escape) {
                    onCancelName()
                    return .handled
                }
                .onChange(of: editText) { _, t in
                    // people.name is a varchar(150); the web stops at 100.
                    if t.count > 100 { editText = String(t.prefix(100)) }
                }
                .padding(.horizontal, 8)
                .frame(height: 32)
                .frame(maxWidth: 168)
                .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 8))
                .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(Color.accentColor, lineWidth: 1))
                .accessibilityLabel(name.isEmpty ? "Name this person" : "New name")
        } else if !name.isEmpty {
            Text(name)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(selected ? Color.accentColor : Color.primary)
                .lineLimit(1)
                .truncationMode(.tail)
                .opacity(pending ? 0.55 : 1)
        } else if selecting {
            Text("Unnamed")
                .font(.subheadline)
                .italic()
                .foregroundStyle(.secondary)
        } else {
            Button(action: onRename) {
                Text("Add a name")
                    .font(.subheadline)
                    .italic()
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 8)
                    .frame(height: 32)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
        }
    }
}

/// Placeholder faces, pulsing while the first listing is on its way.
private struct Pulse: ViewModifier {
    @State private var dim = false

    func body(content: Content) -> some View {
        content
            .opacity(dim ? 0.45 : 1)
            .animation(.easeInOut(duration: 0.7).repeatForever(autoreverses: true), value: dim)
            .onAppear { dim = true }
    }
}

// MARK: - Merging

/// Which face to keep: it keeps its name and picture and gets the photos
/// of the others. The web's MergeDialog: preselected when the choice is
/// plain, and a name field when the face kept has no name to give.
private struct MergeSheet: View {
    /// Two or more, in the page's order.
    let people: [Msg_Person]
    /// The merge on its way and what went wrong, kept by the page's state
    /// so that a copy asked again after the phone turned shows them too.
    @ObservedObject var state: PeoplePageState
    let face: (Msg_Person) -> UIImage?
    let onCancel: () -> Void
    /// Sends the merge; the answer comes back through `state`.
    let onMerge: (Msg_Person, String) -> Void

    @State private var keepID: String?
    @State private var typed = ""
    // Half height to begin with, also when asked again after the phone
    // turned (presented then, it used to open at full height).
    @State private var detent: PresentationDetent = .medium

    init(people: [Msg_Person], state: PeoplePageState, face: @escaping (Msg_Person) -> UIImage?,
         onCancel: @escaping () -> Void, onMerge: @escaping (Msg_Person, String) -> Void) {
        self.people = people
        _state = ObservedObject(wrappedValue: state)
        self.face = face
        self.onCancel = onCancel
        self.onMerge = onMerge
        // Asked again while the merge is on its way: the face and the name
        // it was sent with.
        let sent = state.merging
        _keepID = State(initialValue: sent?.keep ?? PeopleOrder.firstKeep(people))
        _typed = State(initialValue: sent?.name ?? "")
    }

    private var busy: Bool { state.merging != nil }
    private var error: String? { state.mergeError }

    private var keep: Msg_Person? { people.first { $0.id == keepID } }
    private var names: [String] { PeopleOrder.names(people) }
    /// The name the merged person gets: the kept face's own, or the only
    /// name among them; failing both, what is typed.
    private var carried: String {
        guard let keep else { return "" }
        let own = PeopleOrder.tidy(keep.name)
        return own.isEmpty ? (names.count == 1 ? names[0] : "") : own
    }
    private var askName: Bool { keep != nil && carried.isEmpty }
    private var name: String { carried.isEmpty ? PeopleOrder.tidy(typed) : carried }
    private var dropped: [String] {
        guard keep != nil else { return [] }
        return names.filter { PeopleOrder.fold($0) != PeopleOrder.fold(name) }
    }

    private var note: Text? {
        var t: Text?
        if keep == nil {
            t = Text("They have different names, and only one can stay. Pick the face to keep.")
        } else if !name.isEmpty && !askName {
            t = Text("The merged person will be called ") + Text(name).bold() + Text(".")
        }
        if !dropped.isEmpty {
            let d = Text("\(dropped.count == 1 ? "The name" : "The names") \(PeopleOrder.andList(dropped)) will be dropped.")
            t = t.map { $0 + Text(" ") + d } ?? d
        }
        return t
    }

    var body: some View {
        NavigationStack {
            ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 16) {
                        Text("Merge \(people.count.formatted()) people into one?")
                            .font(.title3.weight(.semibold))
                        Text("Pick the face to keep: it keeps its name and picture, and gets the photos of the others. This can't be undone.")
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                        LazyVGrid(columns: [GridItem(.adaptive(minimum: 92), spacing: 8, alignment: .top)], spacing: 16) {
                            ForEach(people, id: \.id) { p in option(p) }
                        }
                        .accessibilityElement(children: .contain)
                        .accessibilityLabel("Face to keep")
                        if let note {
                            note.font(.subheadline)
                        }
                        if askName {
                            VStack(alignment: .leading, spacing: 6) {
                                (Text("Name ") + Text("(optional)").foregroundStyle(.secondary))
                                    .font(.subheadline.weight(.medium))
                                TextField("Add a name", text: $typed)
                                    .textInputAutocapitalization(.words)
                                    .autocorrectionDisabled()
                                    .submitLabel(.done)
                                    .onSubmit(go)
                                    .disabled(busy)
                                    .padding(.horizontal, 12)
                                    .frame(height: 44)
                                    .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 10))
                                    .onChange(of: typed) { _, t in
                                        if t.count > 100 { typed = String(t.prefix(100)) }
                                    }
                            }
                            .id("name")
                        }
                        if let error {
                            Text(error)
                                .font(.subheadline)
                                .foregroundStyle(.red)
                                .id("error")
                        }
                    }
                    .padding(20)
                }
                .onChange(of: error) { _, e in
                    if e != nil { withAnimation { proxy.scrollTo("error", anchor: .bottom) } }
                }
            }
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", action: onCancel).disabled(busy)
                }
                ToolbarItem(placement: .confirmationAction) {
                    if busy {
                        ProgressView()
                    } else {
                        Button("Merge", action: go)
                            .fontWeight(.semibold)
                            .disabled(keep == nil)
                    }
                }
            }
        }
        .presentationDetents([.medium, .large], selection: $detent)
        // Opaque at the medium height too: the faces under a glass sheet
        // made its grey text hard to read.
        .presentationBackground(Color(.systemBackground))
        .interactiveDismissDisabled(busy)
    }

    private func option(_ p: Msg_Person) -> some View {
        let chosen = p.id == keepID
        let label = PeopleOrder.tidy(p.name)
        return Button {
            if !busy { keepID = p.id }
        } label: {
            VStack(spacing: 4) {
                ZStack(alignment: .bottom) {
                    Group {
                        if let img = face(p) {
                            Image(uiImage: img).resizable().scaledToFill()
                        } else {
                            Color(.tertiarySystemFill)
                                .overlay(NavIconView(.face, size: 28).foregroundStyle(.secondary))
                        }
                    }
                    .frame(width: 64, height: 64)
                    .clipShape(Circle())
                    .overlay {
                        Circle().strokeBorder(chosen ? Color.accentColor : .clear, lineWidth: 3)
                    }
                    .padding(.bottom, 6)
                    if chosen {
                        HStack(spacing: 2) {
                            Image(systemName: "checkmark").font(.system(size: 9, weight: .bold))
                            Text("Keep").font(.caption2.weight(.semibold))
                        }
                        .foregroundStyle(.white)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(Color.accentColor, in: Capsule())
                    }
                }
                Text(label.isEmpty ? "Unnamed" : label)
                    .font(.subheadline.weight(label.isEmpty ? .regular : .semibold))
                    .italic(label.isEmpty)
                    .foregroundStyle(label.isEmpty ? Color.secondary : Color.primary)
                    .lineLimit(1)
                Text(PeopleOrder.photos(p.faceCount))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 6)
            .background(chosen ? Color.accentColor.opacity(0.12) : .clear, in: RoundedRectangle(cornerRadius: 12))
            .contentShape(RoundedRectangle(cornerRadius: 12))
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(label.isEmpty ? "Unnamed" : label), \(PeopleOrder.photos(p.faceCount))")
        .accessibilityAddTraits(chosen ? [.isSelected] : [])
    }

    /// While the device merges, the face and the name it was asked for
    /// stay as they are.
    private func go() {
        guard !busy, let keep else { return }
        onMerge(keep, name)
    }
}
