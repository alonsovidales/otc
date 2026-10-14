// SPDX-License-Identifier: AGPL-3.0-or-later

import AppKit

/// Issue #197: a click on a folder's icon or name in the panel shows that
/// folder on this Mac in Finder - a backup's own folder, a two-way folder's
/// local one, from this Mac or from the device alike. otc-sync's tray does
/// the same from "Open" in each folder's submenu (tray/openfolder.go,
/// browser.OpenFolder).
///
/// No entitlement of its own: the URL is the folder's, whose
/// security-scoped bookmark SyncModel started when the folder was added or
/// restored at launch and stops only when it is removed.
protocol FolderOpening {
    /// Shows the folder in Finder; false when it couldn't.
    func open(_ url: URL) -> Bool
    /// Shows the item selected in its parent folder in Finder.
    func reveal(_ url: URL)
    /// A directory Finder shows as one item (an app, a Photos library).
    func isPackage(_ path: String) -> Bool
}

/// The real Finder, through NSWorkspace.
struct WorkspaceOpening: FolderOpening {
    func open(_ url: URL) -> Bool { NSWorkspace.shared.open(url) }
    func reveal(_ url: URL) { NSWorkspace.shared.activateFileViewerSelecting([url]) }
    func isPackage(_ path: String) -> Bool { NSWorkspace.shared.isFilePackage(atPath: path) }
}

enum FolderOpenResult: Equatable {
    /// Finder shows it.
    case opened
    /// Not a folder at that path any more (gone, or a file in its place):
    /// nothing was launched, and nothing about the folder is changed.
    case missing
    /// It is there, but Finder couldn't be asked to show it.
    case failed
}

enum FolderOpener {
    /// Shows the folder at `url` in Finder. A missing folder is only
    /// reported - the row says so (desk.main.folder_missing); its bookmark
    /// isn't re-resolved to follow a rename, which would open a folder the
    /// sync isn't using. A package picked as a folder (a .photoslibrary) is
    /// shown selected in its parent rather than opened, which would launch
    /// the app that owns it.
    static func open(_ url: URL, using opener: FolderOpening = WorkspaceOpening(),
                     fileManager: FileManager = .default) -> FolderOpenResult {
        var isDirectory: ObjCBool = false
        guard url.isFileURL, fileManager.fileExists(atPath: url.path, isDirectory: &isDirectory),
              isDirectory.boolValue else {
            return .missing
        }
        if opener.isPackage(url.path) {
            opener.reveal(url)
            return .opened
        }
        return opener.open(url) ? .opened : .failed
    }
}
