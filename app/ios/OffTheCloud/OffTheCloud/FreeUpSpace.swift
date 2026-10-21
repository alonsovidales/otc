// SPDX-License-Identifier: AGPL-3.0-or-later

import CryptoKit
import Foundation
import Photos
import SwiftUI
import UIKit

/// Free up space: deletes from this phone's library the photos and videos
/// taken more than the chosen number of days ago that the device holds an
/// exact copy of. Off unless turned on (Settings > Free Up Space, and the
/// sheet after every sign-in, SyncChoiceView). As FreeUpSpace.kt on
/// Android.
///
/// What makes a deletion safe, in order:
/// - Each asset goes by the SHA-256 of the resource the sync sends (its
///   original, which PhotoKit never changes: an edit is stored next to it),
///   read here right before the deletion - only when the original is on the
///   phone, never downloaded from iCloud for this (one that is only in
///   iCloud takes little room here, and stays). AssetSyncCache's recorded
///   hash only saves reading what the device doesn't hold.
/// - The device confirms every hash on its own disk right before the
///   deletion (HasHashes: a row with it and its blob whole); an older
///   device answers HasFile, one hash at a time. Nothing it doesn't confirm
///   is deleted.
/// - Never deleted: edited assets (the sync sends the original, so the edit
///   is only here), bursts (the sync sends one picture of the set), Live
///   Photos unless their switch is on (the sync sends only the still),
///   videos while Include videos is off, hidden assets and anything outside
///   the user's own library - as the sync, which skips those too.
/// - iOS asks before deleting, every time, and keeps what goes in Recently
///   Deleted for 30 days.
///
/// It runs at the end of a completed sync while the app is active, at most
/// once a day: a run that found nothing, or whose deletion was declined,
/// counts; one cut short (the app left, the device gone) doesn't. Turning it
/// on, or changing its days, makes it due at once.
final class FreeUpSpace: @unchecked Sendable {
    static let shared = FreeUpSpace()

    /// UserDefaults keys: the first three are Settings' (SecretsStore).
    static let enabledKey = "freeUpSpace"
    static let daysKey = "freeUpDays"
    static let livePhotosKey = "freeUpLivePhotos"
    private static let lastRunKey = "freeUpLastRun"

    /// What Settings offers; Android's are the same.
    static let dayChoices = [1, 3, 7, 14, 30, 90, 365]
    static let defaultDays = 7
    /// Hashes per HasHashes; the device looks at up to 1000.
    static let batchSize = 500

    private let lock = NSLock()
    private var running = false

    /// The stored choice, or the default for a missing or unknown one.
    static func days(_ stored: Any?) -> Int {
        if let d = stored as? Int, dayChoices.contains(d) { return d }
        return defaultDays
    }

    /// Settings turned it on or changed what it deletes: the next sync's
    /// end runs it.
    static func makeDue() {
        UserDefaults.standard.removeObject(forKey: lastRunKey)
    }

    /// Once a day. A last run ahead of the clock (set while it was wrong)
    /// doesn't hold it off.
    static func isDue(lastRun: Date?, now: Date) -> Bool {
        guard let lastRun else { return true }
        return lastRun > now || now.timeIntervalSince(lastRun) >= 24 * 3600
    }

    /// What decides whether an asset may go, read from PhotoKit (traits(of:)).
    struct Traits: Equatable {
        var created: Date?
        var isVideo = false
        var edited = false
        var burst = false
        var live = false
    }

    static func traits(of asset: PHAsset) -> Traits {
        Traits(
            created: asset.creationDate,
            isVideo: asset.mediaType == .video,
            edited: asset.hasAdjustments,
            burst: asset.representsBurst || asset.burstIdentifier != nil,
            live: asset.mediaSubtypes.contains(.photoLive)
        )
    }

    static func eligible(_ t: Traits, cutoff: Date, includeVideos: Bool, livePhotos: Bool) -> Bool {
        guard let created = t.created, created < cutoff else { return false }
        if t.isVideo && !includeVideos { return false }
        if t.edited || t.burst { return false }
        if t.live && !livePhotos { return false }
        return true
    }

    /// PhotoSync.runForeground's end: runs when on, due and in front.
    func afterSync() async {
        let defaults = UserDefaults.standard
        guard defaults.bool(forKey: Self.enabledKey),
              Self.isDue(lastRun: defaults.object(forKey: Self.lastRunKey) as? Date, now: Date()),
              await Self.appIsActive(),
              begin()
        else { return }
        defer { end() }
        do {
            let deleted = try await run()
            print("[free-up] done, \(deleted) deleted")
            defaults.set(Date(), forKey: Self.lastRunKey)
        } catch is NotNow {
            print("[free-up] put off to the next sync")
        } catch {
            print("[free-up] stopped:", error)
        }
    }

    /// Cut short before anything was asked of iOS: tried again after the
    /// next sync, not tomorrow.
    private struct NotNow: Error {}

    private func begin() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if running { return false }
        running = true
        return true
    }

    private func end() {
        lock.lock()
        defer { lock.unlock() }
        running = false
    }

    @MainActor private static func appIsActive() -> Bool {
        UIApplication.shared.applicationState == .active
    }

    /// The run itself: how many assets it deleted (0 when iOS's question
    /// was declined).
    private func run() async throws -> Int {
        let access = PHPhotoLibrary.authorizationStatus(for: .readWrite)
        guard access == .authorized || access == .limited else { return 0 }
        let secrets = SecretsStore.loadOrCreate()
        guard secrets.isConfigured else { return 0 }
        let endpoint = secrets.endpointURLString
        let defaults = UserDefaults.standard
        let livePhotos = defaults.bool(forKey: Self.livePhotosKey)
        let includeVideos = secrets.includeVideos
        let cutoff = Date().addingTimeInterval(-Double(Self.days(defaults.object(forKey: Self.daysKey))) * 24 * 3600)

        let assets = candidates(cutoff: cutoff, includeVideos: includeVideos, livePhotos: livePhotos)
        print("[free-up] \(assets.count) assets old enough")
        guard !assets.isEmpty else { return 0 }

        let ws = await MainActor.run { OTCConnection.shared }
        try await ws.ensureConnected()
        let hashes = try await verifiedHashes(of: assets, ws: ws)
        try Task.checkCancellation()
        let held = try await Self.held(Set(hashes.values), ws: ws)
        let ids = hashes.filter { held.contains($0.value) }.map(\.key)
        print("[free-up] the device holds \(ids.count) of \(hashes.count) read here")
        guard !ids.isEmpty else { return 0 }

        // Fetched again right before the deletion: still there, still not
        // edited, still old enough.
        var toDelete: [PHAsset] = []
        PHAsset.fetchAssets(withLocalIdentifiers: ids, options: nil).enumerateObjects { asset, _, _ in
            if Self.eligible(Self.traits(of: asset), cutoff: cutoff, includeVideos: includeVideos, livePhotos: livePhotos) {
                toDelete.append(asset)
            }
        }
        guard !toDelete.isEmpty else { return 0 }
        // iOS's question needs the app in front, and a Log Out (or another
        // device) since the check means these hashes were someone else's.
        guard await Self.appIsActive(), SecretsStore.loadOrCreate().endpointURLString == endpoint else { throw NotNow() }
        do {
            try await PHPhotoLibrary.shared().performChanges {
                PHAssetChangeRequest.deleteAssets(toDelete as NSArray)
            }
        } catch {
            // Declined (PHPhotosError.userCancelled) or refused: asked again
            // tomorrow, not after every sync.
            print("[free-up] deletion not done:", error)
            return 0
        }
        return toDelete.count
    }

    private func candidates(cutoff: Date, includeVideos: Bool, livePhotos: Bool) -> [PHAsset] {
        let opts = PHFetchOptions()
        opts.includeAssetSourceTypes = [.typeUserLibrary]
        var types = [NSPredicate(format: "mediaType == %d", PHAssetMediaType.image.rawValue)]
        if includeVideos {
            types.append(NSPredicate(format: "mediaType == %d", PHAssetMediaType.video.rawValue))
        }
        opts.predicate = NSCompoundPredicate(andPredicateWithSubpredicates: [
            NSPredicate(format: "creationDate < %@", cutoff as NSDate),
            NSCompoundPredicate(orPredicateWithSubpredicates: types),
        ])
        var out: [PHAsset] = []
        PHAsset.fetchAssets(with: opts).enumerateObjects { asset, _, _ in
            if Self.eligible(Self.traits(of: asset), cutoff: cutoff, includeVideos: includeVideos, livePhotos: livePhotos) {
                out.append(asset)
            }
        }
        return out
    }

    /// The hash of each asset's original as read here now, by local
    /// identifier - only for assets whose original is on the phone (none is
    /// downloaded from iCloud for this; those stay). A hash AssetSyncCache
    /// recorded first asks the device, so what it doesn't hold isn't read;
    /// one with none is read and recorded, as the sync would. Reading the
    /// original is what proves the hash is this asset's: a recorded one may
    /// have come from the device's cloud-id map rather than these bytes.
    private func verifiedHashes(of assets: [PHAsset], ws: OTCConnection) async throws -> [String: String] {
        var recorded: [String: String] = [:]
        for asset in assets {
            if let h = AssetSyncCache.shared.hash(for: asset.localIdentifier) { recorded[asset.localIdentifier] = h }
        }
        let heldRecorded = try await Self.held(Set(recorded.values), ws: ws)
        var out: [String: String] = [:]
        for asset in assets {
            let id = asset.localIdentifier
            // Recorded and not on the device: nothing to read for.
            if let r = recorded[id], !heldRecorded.contains(r) { continue }
            try Task.checkCancellation()
            guard let data = try? PhotoSync.shared.readData(for: asset, allowNetwork: false).data else { continue }
            let h = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
            if recorded[id] != h {
                if let r = recorded[id] { print("[free-up] \(id): recorded \(r), read \(h)") }
                AssetSyncCache.shared.record(localIdentifier: id, hash: h)
            }
            out[id] = h
        }
        return out
    }

    /// The hashes, of these, that the device holds whole, asked on its disk
    /// now. An older device, without HasHashes, answers HasFile for each.
    static func held(_ hashes: Set<String>, ws: OTCConnection) async throws -> Set<String> {
        let all = Array(hashes)
        var out = Set<String>()
        for start in stride(from: 0, to: all.count, by: batchSize) {
            let batch = Array(all[start..<min(start + batchSize, all.count)])
            let resp = try await ws.request { env in
                var q = Msg_HasHashes()
                q.hashes = batch
                env.payload = .reqHasHashes(q)
            }
            if case .respHashesFound(let f) = resp.payload {
                // Only what was asked counts.
                out.formUnion(Set(batch).intersection(f.hashes))
            } else if resp.error && ErrorCodes.isUnknownPayload(code: resp.errorCode, message: resp.errorMessage) {
                for h in all[start...] {
                    try Task.checkCancellation()
                    let one = try await ws.request { env in
                        var q = Msg_HasFile()
                        q.hash = h
                        env.payload = .reqHasFile(q)
                    }
                    if case .respFileExists(let e) = one.payload, e.exists { out.insert(h) }
                }
                return out
            } else {
                throw NSError(domain: "FreeUpSpace", code: 1, userInfo: [NSLocalizedDescriptionKey: resp.errorMessage]) // i18n-ignore: logged by free up space, never shown
            }
        }
        return out
    }
}

/// The Free Up Space section, in Settings and on the sheet after a sign-in
/// (SyncChoiceView). The days and Live Photos stay visible while it is off,
/// so the footer's "older than that" always has its time above it.
struct FreeUpSpaceSection: View {
    @EnvironmentObject var secrets: SecretsStore

    var body: some View {
        Section(header: Text(S.appSettingsFreeUpTitle), footer: Text(S.iosSettingsFreeUpFooter)) {
            Toggle(S.appSettingsFreeUpToggle, isOn: $secrets.freeUpSpace)
            Picker(S.appSettingsFreeUpAfter, selection: $secrets.freeUpDays) {
                ForEach(FreeUpSpace.dayChoices, id: \.self) { days in
                    Text(S.appSettingsFreeUpDays(count: days)).tag(days)
                }
            }
            .disabled(!secrets.freeUpSpace)
            Toggle(isOn: $secrets.freeUpLivePhotos) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(S.iosSettingsFreeUpLivePhotos)
                    Text(S.iosSettingsFreeUpLivePhotosDetail)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
            .disabled(!secrets.freeUpSpace)
        }
    }
}
