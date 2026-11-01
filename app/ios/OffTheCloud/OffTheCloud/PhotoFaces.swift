// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  PhotoFaces.swift
//  OffTheCloud
//
//  A photo's faces, in the viewer's Info (owner, 2026-11-01, from a user:
//  "see what face it has, especially on new images ... and be able to
//  remove faces from there"). Info asks GetPhotoFaces for the open photo
//  as it opens and lists them under People, left to right: each face's
//  round crop and its person's name (Unnamed while they have none). A tap
//  on one closes the viewer and opens that person's photos in Images, as a
//  face opened from People. Its × asks first, as Not this person does,
//  then takes that face off its person (RemoveFromPerson with the photo's
//  hash - both faces when one person has two in it) and lists them again;
//  among that same person's photos the photo leaves the grid instead, as
//  after Not this person (PhotoGalleryVM.removeFace). Nothing shows for a
//  video, a photo only on this phone, while face recognition is off, for a
//  photo with no face, or from a device older than GetPhotoFaces
//  (unknown_payload). Android's viewer does the same.
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

    /// The name as Info shows it.
    var shownName: String { name.isEmpty ? S.appShellSearchUnnamed : name }

    /// Not this person's question for it: "Remove this photo from Ana?".
    var removeTitle: String {
        name.isEmpty ? S.appPhotosPersonRemoveTitleUnnamed(count: 1) : S.appPhotosPersonRemoveTitle(name: name, count: 1)
    }
}

/// What Info's People shows for the open photo.
enum PhotoFacesState {
    /// Nothing: not asked for (a video, a photo only on this phone), no
    /// answer yet, face recognition off, no face found, or a device older
    /// than GetPhotoFaces.
    case hidden
    /// The faces of the photo at `path`, whose content is `hash` (what
    /// RemoveFromPerson names).
    case faces([PhotoFaceRow], path: String, hash: String)

    var rows: [PhotoFaceRow] {
        if case .faces(let rows, _, _) = self { return rows }
        return []
    }

    /// The photo they are the faces of; nil when hidden.
    var path: String? {
        if case .faces(_, let path, _) = self { return path }
        return nil
    }

    /// Whether Info asks for `item`'s faces: a photo the device has.
    static func wanted(_ item: PhotoGalleryVM.Item) -> Bool {
        !item.isLocalOnly && item.localURL == nil && !item.mime.hasPrefix("video/")
    }

    /// What the device's answer for the photo at `path` comes to. An error
    /// (unknown_payload from a device before it among them) shows nothing,
    /// quietly.
    static func of(_ resp: Msg_RespEnvelope, path: String) -> PhotoFacesState {
        guard !resp.error, case .respPhotoFaces(let pf) = resp.payload, pf.faceRecognition, !pf.hash.isEmpty else {
            return .hidden
        }
        let rows = pf.faces.filter { !$0.personID.isEmpty }.enumerated().map { i, f in
            PhotoFaceRow(id: i, person: f.personID, name: PeopleOrder.tidy(f.name),
                         image: f.thumbnail.isEmpty ? nil : UIImage(data: f.thumbnail))
        }
        return rows.isEmpty ? .hidden : .faces(rows, path: path, hash: pf.hash)
    }

    /// The same once `person` was taken off the photo: every face of theirs
    /// leaves (RemoveFromPerson takes them all), until the device's list
    /// comes again.
    func without(_ person: String) -> PhotoFacesState {
        guard case .faces(let rows, let path, let hash) = self else { return self }
        let left = rows.filter { $0.person != person }
        return left.isEmpty ? .hidden : .faces(left, path: path, hash: hash)
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

/// Info's People: a row per face - its crop and its person's name, a tap
/// opening their photos - and a × that takes the face off its person (the
/// caller asks first).
struct PhotoFacesSection: View {
    let rows: [PhotoFaceRow]
    /// The row whose × is waiting on the device.
    let removing: Int?
    let open: (PhotoFaceRow) -> Void
    let remove: (PhotoFaceRow) -> Void

    /// The crop, as large as the name's text makes it (36 pt at the
    /// default size), within reason at the largest sizes.
    @ScaledMetric(relativeTo: .body) private var side: CGFloat = 36

    var body: some View {
        Section {
            ForEach(rows) { row($0) }
        } header: {
            Text(S.appShellSectionPeople)
        }
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
                        // before anything happens.
                        Image(systemName: "xmark")
                            .font(.body.weight(.semibold))
                            .foregroundStyle(Color.secondary)
                            .frame(minWidth: 44, minHeight: 44)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.borderless)
                    .disabled(removing != nil)
                    .accessibilityLabel(S.appShellNotThisPerson)
                }
            }
            .frame(minWidth: 44, minHeight: 44)
        }
    }
}
