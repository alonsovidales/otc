// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation

// Issue #194: after a jump to a date (a search started with `before` = the
// month's last instant), scrolling up near the top asks for the photos
// newer than that same instant (SearchPhotos.newer_than), which the device
// serves oldest first - a second cursor beside the jump's own, with its
// own token and `have`. The pages are put above the photos already there,
// newest first, and the photos on the screen stay where they are.

extension PhotoPaging {
    /// What an upward page brings.
    enum NewerPage: Equatable {
        /// A device before #194, which ignored newer_than and answered a
        /// page of the newest photos (oldest_first false): dropped, and
        /// that device is asked no more until the next sign-in.
        case oldDevice
        /// The photos to put above the grid, newest first, less those it
        /// already shows (by id, or by content: a photo's oldest copy can
        /// change between searches, and it can then turn up in the other
        /// half). Empty with nothing new.
        case files([Msg_File])
    }

    /// An upward page (`files` as the device sent them, `oldestFirst` its
    /// ListOfFiles.oldest_first) against what the grid shows.
    static func newer(files: [Msg_File], oldestFirst: Bool, shownIDs: Set<String>, shownHashes: Set<String>) -> NewerPage {
        guard oldestFirst else { return .oldDevice }
        let newestFirst = Array(files.reversed())
        return .files(notShown(newestFirst, shownIDs: shownIDs, shownHashes: shownHashes).map { newestFirst[$0] })
    }

    /// The entries of a page (either search's) that the grid doesn't show
    /// yet, by their index, in the page's order: not one it shows (by id),
    /// nor - by content, where the hash is known - a photo it shows as
    /// another entry (between a jump's two searches a photo's oldest copy
    /// can change, and it can then come from either: the web's notShown,
    /// Android's withoutShown), nor one twice from the page.
    static func notShown(_ files: [Msg_File], shownIDs: Set<String>, shownHashes: Set<String>) -> [Int] {
        var ids = shownIDs
        var hashes = shownHashes
        var out: [Int] = []
        for (i, f) in files.enumerated() {
            let id = itemID(f)
            guard !ids.contains(id), f.hash.isEmpty || !hashes.contains(f.hash) else { continue }
            ids.insert(id)
            if !f.hash.isEmpty { hashes.insert(f.hash) }
            out.append(i)
        }
        return out
    }

    /// How many entries of a page its search brought for the first time
    /// (not among `own`, the photos it brought before: a page served
    /// again) that weren't put on the grid (`added` were) because the grid
    /// shows them already, from the other search. The device counts them
    /// as served, so its `have` does too (as the web's and Android's do):
    /// sent short, a page of nothing but those would be served again.
    static func hidden(_ files: [Msg_File], own: Set<String>, added: Int) -> Int {
        max(0, files.reduce(0) { own.contains(itemID($1)) ? $0 : $0 + 1 } - added)
    }

    /// A page entry's id in the grid (PhotoGalleryVM.Item.id).
    static func itemID(_ f: Msg_File) -> String { "\(f.path)#\(f.hash)#\(f.fileSize)" }

    /// Whether the grid's scroll offset going from `old` to `new` is the
    /// user scrolling up - however they do it (a swipe, a flick, the
    /// status bar's tap to the top, VoiceOver's three-finger scroll): from
    /// the first one after a jump, the photos above it are asked for as
    /// the top comes near (as on the web and Android). Half a point of
    /// layout jitter is not a scroll.
    static func scrolledUp(from old: CGFloat, to new: CGFloat) -> Bool { new < old - 0.5 }

    /// Whether an upward page ends the upward search: its token is "" (the
    /// last page), or it is empty.
    static func newerEnded(token: String, files: Int) -> Bool { token.isEmpty || files == 0 }
}
