// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Photos

// Settings > Sync Options > Albums: the albums whose photos the sync leaves
// out. The library has no folders, so where Android lists the phone's
// folders (PhotoFolders.kt) this lists the Photos app's albums. What is
// stored is the list of albums turned off (SecretsStore.skippedAlbums),
// never a list to include: everything else is backed up, albums that
// appear later too, and with nothing turned off the sync is what it always
// was. A photo in any album turned off is left out, even when an album
// that is on holds it too.

/// An album the sync can leave out: one of the owner's (or an app's), or
/// one of the smart albums offered.
struct AlbumInfo: Equatable, Identifiable {
    /// Its PHAssetCollection.localIdentifier, what the skip list stores.
    let id: String
    /// As the Photos app names it, in the phone's language.
    let title: String
    let photos: Int
    /// 0 while Include videos is off: they aren't counted then.
    let videos: Int
    let isSmart: Bool
}

enum PhotoSources {
    /// The smart albums offered, in this order: Screenshots (what people
    /// most often don't want backed up), Selfies (the front camera's) and
    /// Screen Recordings (Screenshots' video counterpart; it holds only
    /// videos, so it is listed only while Include videos is on).
    ///
    /// Not offered: Recents (everything), Favorites (what people most want
    /// kept), Videos (Include videos does that), Hidden and Recently Deleted
    /// (the sync never sees them), Shared Albums and My Photo Stream (not
    /// this iPhone's own library, which is all the sync reads), the camera's
    /// formats - Live Photos, Portrait, Panoramas, Bursts, RAW, Cinematic,
    /// Slo-mo, Time-lapse, Long Exposure, Animated, Spatial: turning one off
    /// would silently leave out ordinary camera photos - and Recently Added
    /// and Unable to Upload, which change by themselves.
    static let offeredSmartAlbums: [PHAssetCollectionSubtype] = [.smartAlbumScreenshots, .smartAlbumSelfPortraits, .smartAlbumScreenRecordings]

    /// The one-off pass that catches up with everything added since the
    /// point the owner chose to sync from (PhotoSync.State.floor), queued
    /// beside album ids.
    static let catchUp = "*"

    /// Every album offered, with what it holds (build). Reads PhotoKit:
    /// call it off the main thread.
    static func albums(includeVideos: Bool) -> [AlbumInfo] {
        var user: [AlbumInfo] = []
        // The albums the owner and other apps made, those inside folders
        // too: this fetch isn't limited to the top level.
        PHAssetCollection.fetchAssetCollections(with: .album, subtype: .albumRegular, options: nil).enumerateObjects { c, _, _ in
            user.append(info(of: c, isSmart: false, includeVideos: includeVideos))
        }
        var smart: [AlbumInfo] = []
        for subtype in offeredSmartAlbums {
            PHAssetCollection.fetchAssetCollections(with: .smartAlbum, subtype: subtype, options: nil).enumerateObjects { c, _, _ in
                smart.append(info(of: c, isSmart: true, includeVideos: includeVideos))
            }
        }
        return build(user: user, smart: smart, includeVideos: includeVideos)
    }

    private static func info(of c: PHAssetCollection, isSmart: Bool, includeVideos: Bool) -> AlbumInfo {
        AlbumInfo(id: c.localIdentifier, title: c.localizedTitle ?? "",
                  photos: count(of: .image, in: c),
                  videos: includeVideos ? count(of: .video, in: c) : 0,
                  isSmart: isSmart)
    }

    /// `.count`, never estimatedAssetCount: smart albums answer NSNotFound.
    /// Default options, as the sync's own fetch: hidden assets don't count.
    private static func count(of type: PHAssetMediaType, in c: PHAssetCollection) -> Int {
        let opts = PHFetchOptions()
        opts.predicate = NSPredicate(format: "mediaType == %d", type.rawValue)
        return PHAsset.fetchAssets(in: c, options: opts).count
    }

    /// The list as shown: the user's albums by title, then the smart ones
    /// in the order given (offeredSmartAlbums'). An album with nothing to
    /// back up (no photos, and no videos or Include videos off) isn't
    /// listed.
    static func build(user: [AlbumInfo], smart: [AlbumInfo], includeVideos: Bool) -> [AlbumInfo] {
        func holdsSomething(_ a: AlbumInfo) -> Bool { a.photos + (includeVideos ? a.videos : 0) > 0 }
        let mine = user.filter(holdsSomething).sorted {
            let order = $0.title.localizedStandardCompare($1.title)
            return order == .orderedSame ? $0.id < $1.id : order == .orderedAscending
        }
        return mine + smart.filter(holdsSomething)
    }

    /// The assets in these albums, by local identifier: what the sync and
    /// Free Up Space leave alone. Reads PhotoKit.
    static func skippedAssetIDs(_ albumIDs: [String]) -> Set<String> {
        guard !albumIDs.isEmpty else { return [] }
        var out = Set<String>()
        PHAssetCollection.fetchAssetCollections(withLocalIdentifiers: albumIDs, options: nil).enumerateObjects { c, _, _ in
            PHAsset.fetchAssets(in: c, options: nil).enumerateObjects { a, _, _ in out.insert(a.localIdentifier) }
        }
        return out
    }

    /// The line under an album's name: its photos, its videos while
    /// Include videos is on, or both.
    static func countLine(photos: Int, videos: Int, includeVideos: Bool) -> String {
        let counted = includeVideos ? videos : 0
        if photos > 0 && counted > 0 {
            return S.appSettingsFoldersPhotosVideos(photos: S.appSettingsFoldersPhotos(count: photos), videos: S.appSettingsFoldersVideos(count: counted))
        }
        if counted > 0 { return S.appSettingsFoldersVideos(count: counted) }
        return S.appSettingsFoldersPhotos(count: photos)
    }

    /// The Albums row's value: All, or how many of the albums listed now
    /// (`existing`) are off. A skipped album that is gone or empty isn't
    /// counted, and stays on the list: one emptied and filled again stays
    /// off.
    static func rowValue(existing: [String], skipped: [String]) -> String {
        let off = Set(skipped).intersection(existing).count
        return off == 0 ? S.iosSettingsAlbumsAll : S.iosSettingsAlbumsOff(count: off)
    }

    /// With limited access, the photos the owner picked since the last run
    /// (`seen`, what was visible then). Nothing the first time: there is
    /// nothing to compare with yet, and the date fetch goes on as it did.
    static func newlyVisible(seen: Set<String>?, visible: Set<String>) -> Set<String> {
        guard let seen else { return [] }
        return visible.subtracting(seen)
    }

    /// The one-off passes left once a run went through those it took
    /// (`taken`): all of them when it finished, none when it was cut short.
    /// Those queued while it ran stay, and so does the pass of an album the
    /// run's own skip list (`skippedInRun`, read when it started) still had
    /// off - turned back on while it ran: that pass sent nothing.
    static func afterPass(stored: [String], taken: [String], skippedInRun: [String], finished: Bool) -> [String] {
        guard finished else { return stored }
        let skipped = Set(skippedInRun)
        let done = Set(taken.filter { !skipped.contains($0) })
        return stored.filter { !done.contains($0) }
    }

    /// An album turned off or back on: the skip list, and the one-off passes
    /// after it. Off drops any pass still waiting for the album; on queues
    /// one over everything it holds (older than the watermark too). Nothing
    /// is ever deleted for either.
    static func toggled(_ id: String, skipped: Bool, skips: [String], backfill: [String]) -> (skips: [String], backfill: [String]) {
        if skipped {
            return (skips.contains(id) ? skips : skips + [id], backfill.filter { $0 != id })
        }
        return (skips.filter { $0 != id }, backfill.contains(id) ? backfill : backfill + [id])
    }

    /// What one queued pass reads.
    enum OneOff: Equatable {
        /// Everything in the album, whatever its date.
        case album(String)
        /// Everything added after the date; nil: the whole library.
        case since(Date?)
    }

    /// The passes to make of those queued. The catch-up needs the floor:
    /// without it, it is dropped (asked again only through SyncChoice).
    /// Date.distantPast, "everything", is the whole library.
    static func oneOffs(_ taken: [String], floor: Date?) -> [OneOff] {
        taken.compactMap { entry in
            guard entry == catchUp else { return .album(entry) }
            guard let floor else { return nil }
            return .since(floor == .distantPast ? nil : floor)
        }
    }

    /// A run's list of local identifiers, in the order it goes: the date
    /// fetch's new ones, then the retry list's, then the one-off passes' -
    /// each once, none of them in a skipped album. New first, as on Android:
    /// one large video the device keeps refusing, or that never finishes
    /// in a background run's time, no longer holds up every newer photo.
    /// `exempt`: those that never move the watermark (all but the new
    /// ones). `droppedFresh`: whether new ones were left out as skipped.
    static func runList(retried: [String], oneOff: [String], fresh: [String], skipped: Set<String>) -> (ids: [String], exempt: Set<String>, droppedFresh: Bool) {
        var seen = Set<String>()
        var ids: [String] = []
        var exempt = Set<String>()
        var droppedFresh = false
        for id in fresh where seen.insert(id).inserted {
            if skipped.contains(id) { droppedFresh = true; continue }
            ids.append(id)
        }
        for id in retried + oneOff where seen.insert(id).inserted && !skipped.contains(id) {
            ids.append(id)
            exempt.insert(id)
        }
        return (ids, exempt, droppedFresh)
    }

    /// Where the watermark goes once a run that left out new photos as
    /// skipped went through: to the newest new one (`newest`), never past
    /// the run's start, or nil to leave it where it is - already that far,
    /// or no new one dated.
    static func watermarkPastSkipped(newest: Date?, runStart: Date, stored: Date?) -> Date? {
        guard let newest else { return nil }
        let mark = min(newest, runStart)
        if let stored, mark <= stored { return nil }
        return mark
    }
}
