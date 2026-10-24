// SPDX-License-Identifier: AGPL-3.0-or-later

import Photos
import PhotosUI
import SwiftUI
import UIKit

/// Settings > Sync Options > Albums: the user's albums and the smart albums
/// offered (PhotoSources), each with what it holds and a switch, all on
/// unless turned off. Turning one off only stops backing it up; turning it
/// back on sends what it already holds (PhotoSync.setAlbum). Android's
/// FoldersView lists the phone's folders the same way.
struct AlbumsView: View {
    @EnvironmentObject var secrets: SecretsStore
    /// nil until the first load.
    @State private var albums: [AlbumInfo]?
    @State private var access = PHPhotoLibrary.authorizationStatus(for: .readWrite)
    /// Bumped on coming back to the app: photos taken, albums changed or
    /// access given meanwhile.
    @State private var resumed = 0

    private struct Load: Equatable {
        let includeVideos: Bool
        let readable: Bool
        let resumed: Int
    }

    private var readable: Bool { access == .authorized || access == .limited }

    var body: some View {
        Form {
            if access == .limited {
                Section {
                    // What was picked may bring albums or change counts.
                    LimitedAccessNotice { resumed += 1 }
                }
            }
            if !readable {
                Section {
                    Text(S.appSettingsFoldersNoAccess)
                    AuthorizePhotosButton { access = PHPhotoLibrary.authorizationStatus(for: .readWrite) }
                }
            } else if let albums {
                if albums.isEmpty {
                    // With limited access PhotoKit may show no album at
                    // all: the warning above says why, and "every photo is
                    // backed up" would contradict it.
                    if access != .limited {
                        Section {
                            Text(S.iosSettingsAlbumsEmpty)
                        }
                    }
                } else {
                    Section(footer: Text(S.iosSettingsAlbumsFooter)) {
                        ForEach(albums) { album in
                            Toggle(isOn: backedUp(album.id)) {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(album.title)
                                    Text(PhotoSources.countLine(photos: album.photos, videos: album.videos, includeVideos: secrets.includeVideos))
                                        .font(.footnote)
                                        .foregroundStyle(.secondary)
                                }
                            }
                        }
                    }
                }
            } else {
                Section {
                    ProgressView()
                        .frame(maxWidth: .infinity)
                }
            }
        }
        .navigationTitle(S.iosSettingsAlbums)
        .navigationBarTitleDisplayMode(.inline)
        .task(id: Load(includeVideos: secrets.includeVideos, readable: readable, resumed: resumed)) {
            access = PHPhotoLibrary.authorizationStatus(for: .readWrite)
            guard readable else { return }
            let includeVideos = secrets.includeVideos
            let list = await Task.detached(priority: .userInitiated) {
                PhotoSources.albums(includeVideos: includeVideos)
            }.value
            // Replaced by a newer load meanwhile: that one's list wins.
            guard !Task.isCancelled else { return }
            albums = list
        }
        .onReceive(NotificationCenter.default.publisher(for: UIApplication.didBecomeActiveNotification)) { _ in
            resumed += 1
        }
    }

    /// On unless the album is in the skip list.
    private func backedUp(_ id: String) -> Binding<Bool> {
        Binding(
            get: { !secrets.skippedAlbums.contains(id) },
            set: { on in PhotoSync.shared.setAlbum(id, skipped: !on, secrets: secrets) }
        )
    }
}

/// With limited access - the phone lets the app see only the photos picked
/// in iOS's prompt - only those are backed up: says so, with iOS's own
/// picker to pick more and the app's page in iOS Settings to allow every
/// photo. Each its own row, in Settings > Sync Options and at the top of
/// Albums. `picked` runs once iOS's picker is closed, on the main thread:
/// the app doesn't leave the foreground for it, so nothing else says the
/// photos it sees changed.
struct LimitedAccessNotice: View {
    var picked: () -> Void = {}

    var body: some View {
        Label(S.appSettingsLimitedWarning, systemImage: "exclamationmark.triangle")
            .font(.footnote)
            .foregroundStyle(.orange)
        Button(S.appSettingsLimitedSelectMore) {
            guard let top = UIApplication.shared.topMost else { return }
            // The run finds what was picked: it compares what it can see
            // with what it saw last time.
            PHPhotoLibrary.shared().presentLimitedLibraryPicker(from: top) { _ in
                DispatchQueue.main.async { picked() }
                Task { try? await PhotoSync.shared.runForeground() }
            }
        }
        .buttonStyle(.bordered)
        Button(S.appSettingsLimitedAllowAll) {
            if let url = URL(string: UIApplication.openSettingsURLString) {
                UIApplication.shared.open(url)
            }
        }
        .buttonStyle(.bordered)
    }
}

/// Authorize Photos Access, while the app can't read the library: the first
/// time it asks, after a refusal it opens the app's page in iOS Settings,
/// where access is given. `changed` runs once iOS's question is answered.
struct AuthorizePhotosButton: View {
    var changed: () -> Void = {}

    var body: some View {
        Button(S.appSettingsSyncAuthorize) {
            Task {
                if PHPhotoLibrary.authorizationStatus(for: .readWrite) == .notDetermined {
                    _ = await PHPhotoLibrary.requestAuthorization(for: .readWrite)
                    changed()
                } else if let url = URL(string: UIApplication.openSettingsURLString) {
                    await UIApplication.shared.open(url)
                }
            }
        }
        .buttonStyle(.bordered)
    }
}

extension UIApplication {
    /// The view controller on top of the key window, to present UIKit's
    /// own screens from (the limited-library picker).
    var topMost: UIViewController? {
        guard let s = connectedScenes.first as? UIWindowScene,
              let w = s.windows.first(where: { $0.isKeyWindow }),
              var top = w.rootViewController else { return nil }
        while let p = top.presentedViewController { top = p }
        return top
    }
}
