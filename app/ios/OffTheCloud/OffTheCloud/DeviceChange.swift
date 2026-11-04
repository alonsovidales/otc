// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  DeviceChange.swift
//  OffTheCloud
//
//  Settings › Connection's Save, or the "can't connect" card's Save & Retry,
//  with another address: another device, or the same one reached another
//  way (its bridge name, its home address, Tailscale)? Until this, the
//  photo sync went on from the last device's watermark - the new device
//  never got the photos taken before it, unless Sync All - a wallpaper on
//  Favourites (every device's has the same id) followed the new device's
//  without its notice, and the screens kept the last device's lists.
//
//  What shows is made afresh at once (`addressChanged`): the screens and
//  what their models keep, as Log Out lets go of them, the sign-in and this
//  phone's own settings staying. Whether it is another device is told by
//  the device's own identity, its home-network certificate (the pin
//  GetLocalEndpoint answers; the desktop apps' sync records go by it too):
//  until the new address's device has said (`pending`), no photo sync runs
//  and the wallpaper isn't kept in step. The same pin: nothing else
//  changes, and the sync goes on. Another pin, or none to compare (a device
//  that can't say, a phone that never learned the last one's): another
//  device - the question every sign-in to a device asks (SyncChoice) is
//  asked again, and the wallpaper stops (`apply`). Leave the Bridge is the
//  same device by definition, and no change (SecretsStore.saveCredentials'
//  sameDevice). Android doesn't do this yet: it stops the wallpaper at every
//  address saved, and its photo sync goes on from the last watermark.
//

import Foundation

enum DeviceChange {
    /// UserDefaults (removed by Log Out with the app's domain): the pin of
    /// the device signed in to before the address changed - empty when it
    /// had none - while the new address's device hasn't said who it is.
    static let pendingKey = "deviceChangePin"

    /// The new address's device hasn't said who it is yet.
    static var pending: Bool { pending(UserDefaults.standard) }

    static func pending(_ defaults: UserDefaults) -> Bool {
        defaults.data(forKey: pendingKey) != nil
    }

    enum Verdict: Equatable {
        case sameDevice, anotherDevice
    }

    /// Pure: the same device only when both pins are known and equal.
    static func verdict(before: Data, now: Data?) -> Verdict {
        before.count == 32 && now == before ? .sameDevice : .anotherDevice
    }

    /// The saved address changed: from the device whose pin is `before`
    /// (nil: none known). A check already waiting keeps its own: an address
    /// that never answered, then the last one again, is no change.
    static func begin(before: Data?, defaults: UserDefaults = .standard) {
        guard !pending(defaults) else { return }
        defaults.set(before ?? Data(), forKey: pendingKey)
    }

    /// What the device at the new address said about itself: `pin` its
    /// certificate's, nil when it can't say (a device before the home
    /// network, one with no home route). The verdict taken from the check
    /// waiting, which ends; nil when none was.
    @discardableResult
    static func take(pin: Data?, defaults: UserDefaults = .standard) -> Verdict? {
        guard let before = defaults.data(forKey: pendingKey) else { return nil }
        defaults.removeObject(forKey: pendingKey)
        return verdict(before: before, now: pin)
    }

    /// OTCConnection, once the device at the new address has said (or a
    /// device that can't say has been asked often enough).
    @MainActor
    static func resolve(pin: Data?) {
        switch take(pin: pin) {
        case .sameDevice?:
            print("[device] the same device at the new address")
            // The sync held for the check goes now.
            Task { try? await PhotoSync.shared.runForeground() }
        case .anotherDevice?:
            print("[device] another device at the new address")
            apply()
        case nil:
            break
        }
    }

    /// Another device: what this phone did for the last one stops - a run
    /// still going, the wallpaper made of its photos (the widget's copies
    /// go, the album too if iOS is told yes) - and what to send to this
    /// one is asked, as at every sign-in to a device (SyncChoice: all, or
    /// only what comes from now on).
    @MainActor
    static func apply() {
        PhotoSync.shared.cancel()
        SyncChoice.ask()
        Task { await WallpaperModel.shared.deviceChanged() }
    }

    /// What saving another address does at once (addressChanged); tests,
    /// whose other suites read those models, put their own.
    @MainActor static var onAddressChange: () -> Void = { addressChanged() }

    /// Save with another address, at once: the run going stops (it read the
    /// last device's watermark), the wallpaper's work under way stops
    /// writing (its next requests go to the new address), and what the
    /// screens' models hold for the last device goes, as at Log Out - asked
    /// again from the new one as MainView appears again (RootView makes it
    /// afresh: deviceSession).
    @MainActor
    static func addressChanged() {
        PhotoSync.shared.cancel()
        WallpaperModel.shared.addressChanged()
        NotificationsModel.shared.reset()
        UpdateAlertModel.shared.reset()
        UploadModel.shared.reset()
        SocialFeedViewModel.shared.reset()
        FaceRecognition.shared.reset()
        PlacesSupport.shared.reset()
        MemoriesModel.shared.reset()
        SmartSearchSupport.shared.reset()
        FavouritesModel.shared.reset()
        FilesNav.shared.reset()
    }
}
