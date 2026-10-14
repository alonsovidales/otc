// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation

// Issue #201: a photo stored at several paths (a copy in another folder,
// or downloaded to a phone or computer and synced back) shows once in
// Images, and every SearchPhotos entry says at how many paths
// (File.copies). Deleting such a photo asks, in the owner's words, whether
// to delete only the copy shown or all of them. The same rules as on
// Android and the web.

/// What Images' delete asks: the question as before, or about the copies.
enum CopiesAsk: Equatable {
    /// No copies to ask about (none has more than one, or the device
    /// doesn't say - before #201): the question as before.
    case plain
    /// One photo or video, held at `count` paths (2 or more): delete only
    /// this copy, or all of them.
    case one(count: Int)
    /// Several, `count` of which (1 or more) have other copies: only these
    /// copies, or every copy.
    case several(count: Int)

    /// The question for the items about to be deleted, by their copies
    /// (File.copies; nil where the device didn't say). An older device
    /// never gets offered "all": its copies are always nil.
    static func of(_ copies: [Int?]) -> CopiesAsk {
        let held = copies.compactMap { $0 }.filter { $0 > 1 }
        if copies.count == 1, let n = held.first { return .one(count: n) }
        if copies.count > 1, !held.isEmpty { return .several(count: held.count) }
        return .plain
    }

    /// The question's text under its title; nil for the plain question.
    var text: String? {
        switch self {
        case .plain: return nil
        case .one(let n): return S.appPhotosCopiesSingleText(count: n)
        case .several(let n): return S.appPhotosCopiesSeveralText(count: n)
        }
    }

    /// The button that deletes only the copies shown, and the one that
    /// deletes every copy; nil for the plain question.
    var onlyShown: String? {
        switch self {
        case .plain: return nil
        case .one: return S.appPhotosCopiesOnlyThis
        case .several: return S.appPhotosCopiesOnlyThese
        }
    }

    var everyCopy: String? {
        switch self {
        case .plain: return nil
        case .one(let n): return S.appPhotosCopiesAll(count: n)
        case .several: return S.appPhotosCopiesEvery
        }
    }
}

enum DeleteCopies {
    /// The DelFile for one item: its path, the content it was shown with
    /// (`hash`, when known: the device refuses with "changed" if the path
    /// holds something else now), and every_copy only when asked for
    /// (`everyCopy`) and the item has other copies - never for one whose
    /// copies the device didn't count, which an older device would take as
    /// "only this one" while the question said "all".
    static func request(path: String, hash: String, copies: Int?, everyCopy: Bool) -> Msg_DelFile {
        var d = Msg_DelFile()
        d.path = path
        if !hash.isEmpty { d.hash = hash }
        if everyCopy, let n = copies, n > 1 { d.everyCopy = true }
        return d
    }

    /// Why the device deleted nothing (its error_code).
    enum Reason: Equatable {
        /// "changed": what was shown is out of date (the path holds other
        /// content now): the grid or the listing is read again.
        case changed
        /// "copy_upload_only": every copy was asked for, and one is in an
        /// upload-only folder.
        case copyUploadOnly
        /// Anything else (an upload-only folder, a failure on the device).
        case other
    }

    /// How the device answered a delete.
    enum Outcome: Equatable {
        case deleted
        /// Nothing was deleted: the device's own words (already in the
        /// app's language), and why.
        case refused(String, Reason)
    }

    static func outcome(_ resp: Msg_RespEnvelope) -> Outcome {
        guard resp.error else { return .deleted }
        let reason: Reason
        switch resp.errorCode {
        case "changed": reason = .changed
        case "copy_upload_only": reason = .copyUploadOnly
        default: reason = .other
        }
        return .refused(resp.errorMessage, reason)
    }

    /// One item of a delete that is still there: refused by the device
    /// (its words, and why), or no answer came (the phone's error).
    enum Kept {
        case refused(String, Reason)
        case failed(Error)

        /// Its own alert: the device's words as it said them (or the
        /// phone's), in "Delete failed: …".
        var alone: String {
            switch self {
            case .refused(let words, _): return S.appPhotosNoticeDeleteFailed(detail: words)
            case .failed(let error): return S.appPhotosNoticeDeleteFailed(detail: error.localizedDescription)
            }
        }
    }

    /// Issue #201: the alert once a delete of several is done, for the
    /// items still there (nil: none). One: its own words, as the device
    /// said them. Two or more: summed up per kind, as the web does - how
    /// many changed since they were shown, how many have a copy in an
    /// upload-only folder - and any other reason in its own words, once
    /// each. Worded when the alert shows (GalleryNotice).
    static func keptText(_ kept: [Kept]) -> String? {
        guard let first = kept.first else { return nil }
        guard kept.count > 1 else { return first.alone }
        var changed = 0
        var copies = 0
        var others: [String] = []
        for k in kept {
            switch k {
            case .refused(_, .changed): changed += 1
            case .refused(_, .copyUploadOnly): copies += 1
            default:
                let text = k.alone
                if !others.contains(text) { others.append(text) }
            }
        }
        var lines: [String] = []
        if changed > 0 { lines.append(S.appPhotosNoticeDeleteChanged(count: changed)) }
        if copies > 0 { lines.append(S.appPhotosNoticeDeleteCopyUploadOnly(count: copies)) }
        return (lines + others).joined(separator: "\n")
    }
}

/// What the signed-in device is known not to do, found out from its
/// answers and kept until the next sign-in (OTCConnection.connected): it
/// may have been updated meanwhile.
@MainActor
final class DeviceSupport {
    static let shared = DeviceSupport()

    /// Issue #194: it answered a newer_than page newest first (a device
    /// before #194): Images asks it for no photos above a jump.
    var newerThan = true
    /// Issue #195: it answered GetFolderUsage with unknown_payload (before
    /// #195): Files' Storage view says it needs an update without asking.
    var folderUsage = true

    /// Signed in (again): asked again, once.
    func signedIn() {
        newerThan = true
        folderUsage = true
    }
}
