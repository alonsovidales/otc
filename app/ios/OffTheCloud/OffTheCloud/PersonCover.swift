// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  PersonCover.swift
//  OffTheCloud
//
//  A person's face, chosen by the owner (SetPersonCover, owner 2026-10-31):
//  among one person's photos, Use this face on one photo - the selection
//  bar with one photo picked, the viewer's person menu - or, from their
//  face at the top of the photos (PersonHeader), Upload a photo… (picked
//  as the profile photo is, then the same circle crop) and Choose
//  automatically once a face was chosen. The rules, apart from the views to
//  be tested; the request is PhotoGalleryVM.setPersonCover. Android does
//  the same.
//

import SwiftUI
import PhotosUI

enum PersonCover {
    /// What the person's face becomes.
    enum Choice: Equatable {
        /// The face of theirs found in this photo (its content hash).
        case photo(String)
        /// A picture of the owner's own: the crop's JPEG.
        case image(Data)
        /// The device's own choice again.
        case automatic
    }

    /// The most a picture may weigh: the device takes a JPEG of at most
    /// 256 KB (its cMaxPersonCoverBytes).
    static let maxImageBytes = 256 * 1024

    /// Use this face can take a photo, not a video, that the device holds
    /// (it has a content hash; one only on this phone has none).
    static func usable(mime: String, hash: String) -> Bool {
        !hash.isEmpty && !mime.hasPrefix("video/")
    }

    /// The photo Use this face takes among `picked`: exactly one, usable.
    static func photo(_ picked: [PhotoGalleryVM.Item]) -> PhotoGalleryVM.Item? {
        guard picked.count == 1, let it = picked.first, usable(mime: it.mime, hash: it.hash) else { return nil }
        return it
    }

    /// A picture the device takes: a JPEG (FF D8 FF) of at most
    /// maxImageBytes.
    static func fits(_ data: Data) -> Bool {
        data.count >= 3 && data.count <= maxImageBytes && data.prefix(3).elementsEqual([0xFF, 0xD8, 0xFF])
    }

    /// The request that makes `choice` `person`'s face.
    static func request(person: String, _ choice: Choice) -> Msg_SetPersonCover {
        var r = Msg_SetPersonCover()
        r.personID = person
        switch choice {
        case .photo(let hash): r.cover = .hash(hash)
        case .image(let data): r.cover = .image(data)
        case .automatic: r.cover = nil
        }
        return r
    }

    /// How the device answered.
    enum Outcome: Equatable {
        case done
        /// A device from before it (unknown_payload).
        case needsUpdate
        /// Refused, with the device's words (already in the app's
        /// language), if any; nil also when no answer came.
        case refused(String?)
    }

    static func outcome(_ resp: Msg_RespEnvelope?) -> Outcome {
        guard let resp else { return .refused(nil) }
        if resp.error {
            if ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) { return .needsUpdate }
            return .refused(words(resp.errorMessage))
        }
        guard case .respAck(let a) = resp.payload else { return .refused(nil) }
        if a.ok { return .done }
        // The bridge's own "device unreachable" isn't the device's answer.
        if a.code == LoadProblem.deviceUnreachable { return .refused(nil) }
        return .refused(words(a.errorMsg))
    }

    private static func words(_ s: String) -> String? {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }
}

/// Upload a photo… for a person's face (PhotoGalleryVM.coverUploadFor): the
/// phone's photo picker, as the profile photo's, then the same circle crop
/// with the face's own title and hint; its JPEG becomes their face.
struct PersonCoverUpload: ViewModifier {
    @ObservedObject var vm: PhotoGalleryVM
    @State private var picking = false
    @State private var pick: PhotosPickerItem?
    @State private var crop: ProfilePhotoCropItem?
    /// Whose face the picture becomes, from the menu's tap to the crop's
    /// Use photo.
    @State private var person: String?

    func body(content: Content) -> some View {
        content
            .onChange(of: vm.coverUploadFor) { _, id in
                guard let id else { return }
                vm.coverUploadFor = nil
                person = id
                picking = true
            }
            .photosPicker(isPresented: $picking, selection: $pick, matching: .images)
            .onChange(of: pick) { _, item in
                guard let item else { return }
                Task {
                    if let data = try? await item.loadTransferable(type: Data.self),
                       let made = await ProfilePhotoCropItem.make(from: data) {
                        crop = made
                    }
                    // Picking the same photo again still comes here.
                    pick = nil
                }
            }
            .fullScreenCover(item: $crop) { item in
                ProfilePhotoCropView(image: item.image,
                                     title: S.appPhotosPersonCropTitle,
                                     hint: S.appPhotosPersonCropHint,
                                     maxBytes: PersonCover.maxImageBytes,
                                     onCancel: { crop = nil },
                                     onUse: { data in
                                         crop = nil
                                         guard let person else { return }
                                         Task { await vm.setPersonCover(person, .image(data)) }
                                     })
            }
    }
}
