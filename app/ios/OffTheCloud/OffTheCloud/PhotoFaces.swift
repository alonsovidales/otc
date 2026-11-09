// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  PhotoFaces.swift
//  OffTheCloud
//
//  A photo's faces, in the viewer's Info (owner, 2026-11-01, from a user:
//  "see what face it has, especially on new images ... and be able to
//  remove faces from there"). Info asks GetPhotoFaces for the open photo
//  or video as it opens and lists them under People, left to right, then
//  the people added by hand: each one's round crop and their name (Unnamed
//  while they have none). A tap on one closes the viewer and opens that
//  person's photos in Images, as a face opened from People. A face's ×
//  asks first, as Not this person does, then takes that face off its
//  person (RemoveFromPerson with the photo's hash - both faces when one
//  person has two in it) and lists them again; among that same person's
//  photos the photo leaves the grid instead, as after Not this person
//  (PhotoGalleryVM.removeFace). Nothing shows for a photo only on this
//  phone, while face recognition is off, for a photo with no face, or from
//  a device older than GetPhotoFaces (unknown_payload).
//
//  People added by hand (owner, 2026-11-09: "add people to the photos and
//  videos by hand ... in the faces we will have an add button ... select
//  one of the faces that are not already in the photo"): where the device
//  takes AddToPerson (PhotoFaces.adds_people) People ends with Add a
//  person - so it shows with no face too, a video's included - opening
//  People's list without those in it already (PersonAddSheet); the one
//  picked is added and Info asks the device again. Their row is a face's,
//  their People face as the crop, and its × takes them off at once:
//  Not this person's question is about face recognition, which never put
//  them there, and adding them again is a tap away. A device before it
//  answers a video with no faces and never says adds_people, so nothing
//  changes there. Android's viewer does the same.
//

import SwiftUI

/// One face Info lists.
struct PhotoFaceRow: Identifiable {
    /// Its place in the photo, left to right.
    let id: Int
    /// The person it is matched to.
    let person: String
    /// Their name as saved; "" while they have none.
    let name: String
    /// Its crop; nil when the device sent none (or one that doesn't decode).
    let image: UIImage?
    /// A person added by hand (AddToPerson), not a face found: no box in
    /// the photo, their People face as the crop, and a × that asks nothing.
    let byHand: Bool

    /// The name as Info shows it.
    var shownName: String { name.isEmpty ? S.appShellSearchUnnamed : name }

    /// Not this person's question for it: "Remove this photo from Ana?".
    var removeTitle: String {
        name.isEmpty ? S.appPhotosPersonRemoveTitleUnnamed(count: 1) : S.appPhotosPersonRemoveTitle(name: name, count: 1)
    }

    /// What VoiceOver calls its ×: Not this person for a face, "Remove
    /// Ana" (as a person's chip in the search) for someone added by hand.
    var removeLabel: String {
        guard byHand else { return S.appShellNotThisPerson }
        return name.isEmpty ? S.appShellSearchRemovePerson : S.appShellSearchRemove(name: name)
    }
}

/// What Info's People shows for the open photo or video.
enum PhotoFacesState {
    /// Nothing: not asked for (a photo only on this phone), no answer yet,
    /// face recognition off, no one in it and no Add a person, or a device
    /// older than GetPhotoFaces.
    case hidden
    /// The people in the photo or video at `path`, whose content is `hash`
    /// (what RemoveFromPerson and AddToPerson name). `adds`: the device
    /// takes AddToPerson (PhotoFaces.adds_people), so People ends with Add
    /// a person - and shows with no one in it yet.
    case faces([PhotoFaceRow], path: String, hash: String, adds: Bool)

    var rows: [PhotoFaceRow] {
        if case .faces(let rows, _, _, _) = self { return rows }
        return []
    }

    /// The photo or video they are in; nil when hidden.
    var path: String? {
        if case .faces(_, let path, _, _) = self { return path }
        return nil
    }

    /// Its content hash; nil when hidden.
    var hash: String? {
        if case .faces(_, _, let hash, _) = self { return hash }
        return nil
    }

    /// Whether Info has People: someone in it, or Add a person.
    var shown: Bool { path != nil }

    /// Whether People ends with Add a person.
    var adds: Bool {
        if case .faces(_, _, _, let adds) = self { return adds }
        return false
    }

    /// Who is in it already: what Add a person doesn't offer.
    var people: Set<String> { Set(rows.map(\.person)) }

    /// Whether Info asks for `item`'s people: a photo or video the device
    /// has. A device before AddToPerson answers a video with no faces, and
    /// nothing shows, as before.
    static func wanted(_ item: PhotoGalleryVM.Item) -> Bool {
        !item.isLocalOnly && item.localURL == nil
    }

    /// What the device's answer for the photo or video at `path` comes to.
    /// An error (unknown_payload from a device before it among them) shows
    /// nothing, quietly. With no one in it People shows only for Add a
    /// person: while face recognition is on, on a device that takes it.
    static func of(_ resp: Msg_RespEnvelope, path: String) -> PhotoFacesState {
        guard !resp.error, case .respPhotoFaces(let pf) = resp.payload, pf.faceRecognition, !pf.hash.isEmpty else {
            return .hidden
        }
        let rows = pf.faces.filter { !$0.personID.isEmpty }.enumerated().map { i, f in
            PhotoFaceRow(id: i, person: f.personID, name: PeopleOrder.tidy(f.name),
                         image: f.thumbnail.isEmpty ? nil : UIImage(data: f.thumbnail), byHand: f.byHand)
        }
        return rows.isEmpty && !pf.addsPeople ? .hidden : .faces(rows, path: path, hash: pf.hash, adds: pf.addsPeople)
    }

    /// The same once `person` was taken off the photo: every face of theirs
    /// leaves (RemoveFromPerson takes them all), until the device's list
    /// comes again.
    func without(_ person: String) -> PhotoFacesState {
        guard case .faces(let rows, let path, let hash, let adds) = self else { return self }
        let left = rows.filter { $0.person != person }
        return left.isEmpty && !adds ? .hidden : .faces(left, path: path, hash: hash, adds: adds)
    }

    // MARK: Add a person

    /// What Add a person's list shows.
    enum AddList: Equatable {
        /// People's list on its way, the first time it is asked for.
        case loading
        /// No one found yet (app.photos.viewer.face_add_none).
        case noOne
        /// Everyone found is in it already (face_add_all).
        case everyoneIn
        /// No name holds what was typed (face_add_no_match).
        case noMatch
        case people([Msg_Person])
    }

    /// Whom Add a person offers: People's list in its order (Someone
    /// else…'s, PersonMove), without those in it already; with `filter`,
    /// those whose name holds it.
    func addChoices(_ all: [Msg_Person], filter: String = "") -> [Msg_Person] {
        PersonMove.choices(all, excluding: people, filter: filter)
    }

    /// Add a person's list, from People's (`loaded` once it came) and what
    /// is typed in its field - there only past PersonMove.filterAbove
    /// people to offer, so what was typed counts only then.
    func addList(_ all: [Msg_Person], loaded: Bool, filter: String) -> AddList {
        if all.isEmpty { return loaded ? .noOne : .loading }
        let offered = addChoices(all)
        if offered.isEmpty { return .everyoneIn }
        guard PersonMove.wantsFilter(offered.count) else { return .people(offered) }
        let found = addChoices(all, filter: filter)
        return found.isEmpty ? .noMatch : .people(found)
    }

    /// Add a person's title: "Who is in this photo?", or video.
    static func addTitle(video: Bool) -> String {
        video ? S.appPhotosViewerFaceAddTitleVideo : S.appPhotosViewerFaceAddTitlePhoto
    }

    /// What the list says of AddToPerson's answer: nil once the person is
    /// in it; else the device's words (in the app's language already), or
    /// face_add_failed without any - no answer, the bridge's own "device
    /// unreachable", or unknown_payload (offered only where the device
    /// said it takes it, so not its owner's words).
    static func addFailure(_ resp: Msg_RespEnvelope?) -> String? {
        let failed = S.appPhotosViewerFaceAddFailed
        guard let resp else { return failed }
        if resp.error {
            if ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) { return failed }
            return words(resp.errorMessage) ?? failed
        }
        guard case .respAck(let a) = resp.payload else { return failed }
        if a.ok { return nil }
        if a.code == LoadProblem.deviceUnreachable { return failed }
        return words(a.errorMsg) ?? failed
    }

    private static func words(_ s: String) -> String? {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }
}

// MARK: - Images sent to a person

/// A person's photos asked for from a viewer that isn't Images' own - Files',
/// the map's, a memory's - by a face tapped in its Info: MainView shows them
/// in Images, and a memory open over Images closes. Images' own viewer
/// opens them itself (PhotoGalleryVM.showFacePerson).
@MainActor
final class PersonNav: ObservableObject {
    static let shared = PersonNav()

    /// The person not yet shown, if any.
    private(set) var pending: String?

    /// One more for every person asked for: what MainView watches.
    @Published private(set) var sent = 0

    private init() {}

    func show(_ person: String) {
        pending = person
        sent += 1
    }

    /// The person asked for, once: nil when it was taken already.
    func take() -> String? {
        defer { pending = nil }
        return pending
    }
}

// MARK: - The section

/// Info's People: a row per face and per person added by hand - the crop
/// and the name, a tap opening their photos - each with a × that takes
/// them off (the caller asks first for a face); Add a person last, where
/// the device takes it.
struct PhotoFacesSection: View {
    let rows: [PhotoFaceRow]
    /// Whether it ends with Add a person.
    let adds: Bool
    /// The row whose × is waiting on the device.
    let removing: Int?
    let open: (PhotoFaceRow) -> Void
    let remove: (PhotoFaceRow) -> Void
    let add: () -> Void

    /// The crop, as large as the name's text makes it (36 pt at the
    /// default size), within reason at the largest sizes.
    @ScaledMetric(relativeTo: .body) private var side: CGFloat = 36

    var body: some View {
        Section {
            ForEach(rows) { row($0) }
            if adds { addRow }
        } header: {
            Text(S.appShellSectionPeople)
        }
    }

    /// Add a person: its icon where the crops are, its words in line with
    /// the names.
    private var addRow: some View {
        Button(action: add) {
            HStack(spacing: 12) {
                Image(systemName: "person.badge.plus")
                    .font(.body.weight(.medium))
                    .frame(width: min(side, 72), height: min(side, 72))
                Text(S.appPhotosViewerFaceAdd)
                    .multilineTextAlignment(.leading)
                Spacer(minLength: 0)
            }
            .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        .disabled(removing != nil)
        .accessibilityLabel(S.appPhotosViewerFaceAdd)
    }

    private func row(_ f: PhotoFaceRow) -> some View {
        let named = !f.name.isEmpty
        return HStack(spacing: 4) {
            // Both buttons borderless: in a List a row's plain buttons
            // would all fire on any tap in it.
            Button { open(f) } label: {
                HStack(spacing: 12) {
                    Group {
                        if let img = f.image {
                            Image(uiImage: img).resizable().scaledToFill()
                        } else {
                            Color(.tertiarySystemFill)
                        }
                    }
                    .frame(width: min(side, 72), height: min(side, 72))
                    .clipShape(Circle())
                    Text(f.shownName)
                        .italic(!named)
                        .foregroundStyle(named ? Color.primary : Color.secondary)
                        .multilineTextAlignment(.leading)
                    Spacer(minLength: 0)
                }
                .frame(minHeight: 44)
                .contentShape(Rectangle())
            }
            .buttonStyle(.borderless)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(f.shownName)
            .accessibilityAddTraits(.isButton)
            Group {
                if removing == f.id {
                    ProgressView()
                } else {
                    Button { remove(f) } label: {
                        // Grey, not the tint: a quiet action, asked about
                        // before anything happens (or, for someone added
                        // by hand, put back with a tap on Add).
                        Image(systemName: "xmark")
                            .font(.body.weight(.semibold))
                            .foregroundStyle(Color.secondary)
                            .frame(minWidth: 44, minHeight: 44)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.borderless)
                    .disabled(removing != nil)
                    .accessibilityLabel(f.removeLabel)
                }
            }
            .frame(minWidth: 44, minHeight: 44)
        }
    }
}

// MARK: - Add a person

/// Add a person: People's list without those in the photo or video
/// already, as Someone else… lists them (PersonPickRow) with a field to
/// find a name in a long list; the one picked is added by hand
/// (PhotoGalleryVM.addPerson), a spinner on their row meanwhile. A refusal
/// stays here, above the list.
struct PersonAddSheet: View {
    @ObservedObject var vm: PhotoGalleryVM
    let video: Bool
    @State private var filter = ""

    var body: some View {
        let state = vm.infoFaces
        let offered = state.addChoices(vm.allPeople).count
        NavigationStack {
            Group {
                switch state.addList(vm.allPeople, loaded: vm.peopleLoaded, filter: filter) {
                case .loading:
                    ProgressView()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                case .noOne:
                    note(S.appPhotosViewerFaceAddNone)
                case .everyoneIn:
                    note(S.appPhotosViewerFaceAddAll)
                case .noMatch:
                    note(S.appPhotosViewerFaceAddNoMatch)
                case .people(let people):
                    List(people, id: \.id) { p in
                        PersonPickRow(person: p, face: vm.face(for: p), sending: vm.faceAddSending == p.id) {
                            Task { await vm.addPerson(p) }
                        }
                        .disabled(vm.faceAddSending != nil)
                    }
                    .listStyle(.plain)
                }
            }
            .safeAreaInset(edge: .top, spacing: 0) {
                if let error = vm.faceAddError {
                    Text(error)
                        .font(.subheadline)
                        .foregroundStyle(.red)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 20)
                        .padding(.vertical, 8)
                        .background(Color(.systemBackground))
                }
            }
            .navigationTitle(PhotoFacesState.addTitle(video: video))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(S.commonCancel) { vm.closeFaceAdd() }
                }
            }
            .modifier(NameFilter(on: PersonMove.wantsFilter(offered), text: $filter, prompt: S.appPhotosViewerFaceAddFind))
        }
        .presentationDetents([.medium, .large])
        // Opaque at the medium height too, as Someone else…'s: Info under
        // a glass sheet makes grey text hard to read.
        .presentationBackground(Color(.systemBackground))
        .onChange(of: vm.faceAddError) { _, e in
            if let e { Announce.polite(e) }
        }
    }

    private func note(_ text: String) -> some View {
        Text(text)
            .foregroundStyle(.secondary)
            .multilineTextAlignment(.center)
            .padding()
            .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
