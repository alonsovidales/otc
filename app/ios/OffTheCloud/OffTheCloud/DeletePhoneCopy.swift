// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Photos
import UIKit

// A photo or video deleted from the device that is also in this phone's own
// library - the photo sync sent it from here, or Free Up Space read it here
// (both record its hash in AssetSyncCache): once the device has deleted it,
// the app asks whether to delete it from the phone too. Asked only about
// content the device no longer holds (every copy deleted, or the only one),
// only with full Photos access, and never about what the app saved to the
// library itself (SavedToPhone). Delete from Phone is iOS's own deletion,
// which asks again and keeps them in Recently Deleted for 30 days. The same
// rules as DeletePhoneCopy.kt on Android.

/// One photo or video the device accepted to delete, as it was shown.
struct DeletedOnDevice: Equatable, Sendable {
    /// Its content's hash; "" when the app never had it (nothing to match).
    var hash: String
    /// At how many paths the device held it (File.copies), nil when the
    /// device didn't say (an older device, Files' rows).
    var copies: Int?
    /// Every copy was asked for (DelFile.every_copy).
    var everyCopy: Bool
}

/// A photo or video in this phone's library recorded under a deleted hash.
struct PhoneAsset: Equatable, Sendable {
    /// Its local identifier (PHAsset.localIdentifier).
    var id: String
    var hash: String
    var isVideo: Bool
}

/// The question asked after a delete: the phone's assets it is about, and
/// how many of the deleted items they are.
struct PhoneDeleteAsk: Equatable, Sendable {
    var assets: [PhoneAsset]
    /// How many of the deleted photos and videos are on this phone (1 or more).
    var found: Int
    /// How many the device deleted in that go.
    var deleted: Int

    var ids: [String] { assets.map(\.id) }

    var title: String {
        found == 1 ? S.appPhotosDeletePhoneTitle : S.appPhotosDeletePhoneTitleSeveral
    }

    /// The first sentence: the one item deleted, or how many of several.
    var also: String {
        if deleted == 1 {
            return assets.allSatisfy(\.isVideo) ? S.appPhotosDeletePhoneVideo : S.appPhotosDeletePhonePhoto
        }
        return S.appPhotosDeletePhoneSome(count: found)
    }

    var text: String { S.iosPhotosDeletePhoneText(also: also) }
}

/// The pure rules, tested as Android's DeletePhoneCopyTest.
enum DeletePhoneCopy {
    /// The hashes of what the device no longer holds after these deletes:
    /// every copy deleted, or one that had no other copy - nothing more than
    /// one copy of which stays (Delete only this copy), and nothing without
    /// a hash. A device that doesn't count copies (nil) is taken at its
    /// word; the device is asked about each match before the question
    /// (FreeUpSpace.held), so a copy it still holds is never asked about.
    static func gone(_ deleted: [DeletedOnDevice]) -> Set<String> {
        Set(deleted.filter { !$0.hash.isEmpty && ($0.everyCopy || ($0.copies ?? 1) <= 1) }.map(\.hash))
    }

    /// The phone's assets to ask about: recorded under a gone hash
    /// (`recorded`: hash by local identifier), still in the library
    /// (`inLibrary`: whether each is a video, for those found), and not
    /// saved there by this app (`saved`). In identifier order.
    static func onPhone(gone: Set<String>, recorded: [String: String], inLibrary: [String: Bool],
                        saved: Set<String>) -> [PhoneAsset] {
        recorded.compactMap { id, hash -> PhoneAsset? in
            guard gone.contains(hash), !saved.contains(id), let video = inLibrary[id] else { return nil }
            return PhoneAsset(id: id, hash: hash, isVideo: video)
        }
        .sorted { $0.id < $1.id }
    }

    /// The question, once the device has said which of those hashes it
    /// still holds (`held`: never asked about); nil when nothing is left.
    static func ask(deleted: [DeletedOnDevice], onPhone: [PhoneAsset], held: Set<String>) -> PhoneDeleteAsk? {
        let assets = onPhone.filter { !held.contains($0.hash) }
        let hashes = Set(assets.map(\.hash))
        let gone = gone(deleted)
        let found = deleted.filter { gone.contains($0.hash) && hashes.contains($0.hash) }.count
        guard found > 0 else { return nil }
        return PhoneDeleteAsk(assets: assets, found: found, deleted: deleted.count)
    }

    /// Asked only with full access to the library: with limited access the
    /// app sees only part of it, and without any it can delete nothing.
    static func asks(access: PHAuthorizationStatus) -> Bool {
        access == .authorized
    }

    /// The person declined iOS's own question: nothing more to say.
    static func declined(_ error: Error) -> Bool {
        let e = error as NSError
        return e.domain == PHPhotosErrorDomain && e.code == PHPhotosError.Code.userCancelled.rawValue
    }

    /// Files' rows: a photo or a video (or a type the listing didn't say),
    /// the only files the library can hold.
    static func isMedia(mime: String) -> Bool {
        mime.isEmpty || mime.hasPrefix("image/") || mime.hasPrefix("video/")
    }
}

/// Looks for the deleted photos in this phone's library and asks, one
/// delete at a time. The question shows in a window of its own, over
/// whatever is on screen: the viewer it came from may close under it (its
/// last photo deleted), and a SwiftUI alert presented from under a
/// full-screen viewer takes the viewer away.
@MainActor
final class DeletePhoneCopyModel {
    static let shared = DeletePhoneCopyModel()

    private var waiting: [(deleted: [DeletedOnDevice], endpoint: String)] = []
    private var busy = false
    private var window: UIWindow?
    private weak var keyBefore: UIWindow?

    /// A delete the device accepted.
    func deleted(_ items: [DeletedOnDevice]) {
        guard !DeletePhoneCopy.gone(items).isEmpty,
              DeletePhoneCopy.asks(access: PHPhotoLibrary.authorizationStatus(for: .readWrite))
        else { return }
        waiting.append((items, SecretsStore.loadOrCreate().endpointURLString))
        next()
    }

    private func next() {
        guard !busy, !waiting.isEmpty else { return }
        busy = true
        let (items, endpoint) = waiting.removeFirst()
        Task {
            let ask = await Self.lookUp(items)
            // Not after a Log Out or another device: those hashes were its.
            guard let ask, SecretsStore.loadOrCreate().endpointURLString == endpoint,
                  UIApplication.shared.applicationState == .active,
                  present(ask)
            else { return finish() }
        }
    }

    private func finish() {
        busy = false
        next()
    }

    /// The question for these deletes, or nil: off the main thread (the
    /// records and the library are read here).
    nonisolated private static func lookUp(_ deleted: [DeletedOnDevice]) async -> PhoneDeleteAsk? {
        let gone = DeletePhoneCopy.gone(deleted)
        guard !gone.isEmpty, DeletePhoneCopy.asks(access: PHPhotoLibrary.authorizationStatus(for: .readWrite)) else { return nil }
        let recorded = AssetSyncCache.shared.identifiers(withHashes: gone)
        guard !recorded.isEmpty else { return nil }
        let saved = SavedToPhone.shared.ids()
        let ids = recorded.keys.filter { !saved.contains($0) }
        guard !ids.isEmpty else { return nil }
        var inLibrary: [String: Bool] = [:]
        PHAsset.fetchAssets(withLocalIdentifiers: Array(ids), options: nil).enumerateObjects { asset, _, _ in
            inLibrary[asset.localIdentifier] = asset.mediaType == .video
        }
        let onPhone = DeletePhoneCopy.onPhone(gone: gone, recorded: recorded, inLibrary: inLibrary, saved: saved)
        guard !onPhone.isEmpty else { return nil }
        // The device's own say, as Free Up Space asks it: a copy it still
        // holds (another path, a device that doesn't count copies) is never
        // asked about. No answer, no question.
        let ws = await MainActor.run { OTCConnection.shared }
        guard let held = try? await FreeUpSpace.held(Set(onPhone.map(\.hash)), ws: ws) else { return nil }
        return DeletePhoneCopy.ask(deleted: deleted, onPhone: onPhone, held: held)
    }

    private func present(_ ask: PhoneDeleteAsk) -> Bool {
        let alert = UIAlertController(title: ask.title, message: ask.text, preferredStyle: .alert)
        alert.addAction(UIAlertAction(title: S.appPhotosDeletePhoneKeep, style: .cancel) { [weak self] _ in
            self?.close()
            self?.finish()
        })
        alert.addAction(UIAlertAction(title: S.appPhotosDeletePhoneDelete, style: .destructive) { [weak self] _ in
            self?.close()
            Task { await self?.deleteFromPhone(ask) }
        })
        return show(alert)
    }

    /// iOS's own deletion: it asks again, and Recently Deleted keeps them
    /// 30 days. Fetched again first: only those still there, and still not
    /// the app's own.
    private func deleteFromPhone(_ ask: PhoneDeleteAsk) async {
        let failed = await Self.delete(ids: ask.ids)
        if failed {
            let alert = UIAlertController(title: S.appPhotosDeletePhoneFailed, message: nil, preferredStyle: .alert)
            alert.addAction(UIAlertAction(title: S.commonOk, style: .cancel) { [weak self] _ in
                self?.close()
                self?.finish()
            })
            if show(alert) { return }
        }
        finish()
    }

    /// Whether the deletion failed (not when it was declined, or nothing
    /// was left to delete).
    nonisolated private static func delete(ids: [String]) async -> Bool {
        guard DeletePhoneCopy.asks(access: PHPhotoLibrary.authorizationStatus(for: .readWrite)) else { return false }
        let saved = SavedToPhone.shared.ids()
        var assets: [PHAsset] = []
        PHAsset.fetchAssets(withLocalIdentifiers: ids, options: nil).enumerateObjects { asset, _, _ in
            if !saved.contains(asset.localIdentifier) { assets.append(asset) }
        }
        guard !assets.isEmpty else { return false }
        let toDelete = assets
        do {
            try await PHPhotoLibrary.shared().performChanges {
                PHAssetChangeRequest.deleteAssets(toDelete as NSArray)
            }
        } catch {
            if DeletePhoneCopy.declined(error) { return false }
            print("[delete-phone] deletion not done:", error)
            return true
        }
        // Gone from the library: off the sync's retry list, as the sync
        // does for an asset it no longer finds.
        AssetSyncCache.shared.resolve(toDelete.map(\.localIdentifier))
        return false
    }

    // MARK: The window

    private func show(_ alert: UIAlertController) -> Bool {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        guard let scene = scenes.first(where: { $0.activationState == .foregroundActive }) ?? scenes.first else { return false }
        let key = scene.windows.first(where: \.isKeyWindow)
        let w = UIWindow(windowScene: scene)
        w.windowLevel = .alert
        w.backgroundColor = .clear
        w.tintColor = key?.tintColor
        w.overrideUserInterfaceStyle = key?.overrideUserInterfaceStyle ?? .unspecified
        let root = UIViewController()
        root.view.backgroundColor = .clear
        w.rootViewController = root
        keyBefore = key
        window = w
        w.makeKeyAndVisible()
        root.present(alert, animated: true)
        return true
    }

    private func close() {
        window?.isHidden = true
        window = nil
        keyBefore?.makeKey()
        keyBefore = nil
    }
}
