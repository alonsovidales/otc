// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation

/// The photos and videos this app added to the library itself - the
/// viewer's Save (issue #201) - by local identifier: copies of what the
/// device already holds, with the photo's own old date. The photo sync
/// doesn't send them back as this phone's photos (the library's change
/// history would find them, and the device then held a second copy under
/// /ios/<id>/), and Free Up Space doesn't delete them as backed up long ago.
/// Android goes by the folders its Save writes to (Pictures/Off The Cloud,
/// Movies/Off The Cloud). Kept in a suite of its own, outside the domain
/// Log Out removes: they stay copies whichever device is signed in to next.
struct SavedToPhone: @unchecked Sendable {
    static let suiteName = "cloud.off-the.OffTheCloud.saved"
    static let shared = SavedToPhone(defaults: UserDefaults(suiteName: suiteName) ?? .standard)

    private static let key = "assetIDs"
    private static let lock = NSLock()

    let defaults: UserDefaults

    /// Noted as the asset is made (in the library's change block), before
    /// the change it brings can start a sync.
    func remember(_ localIdentifier: String) {
        guard !localIdentifier.isEmpty else { return }
        Self.lock.withLock {
            var ids = defaults.stringArray(forKey: Self.key) ?? []
            guard !ids.contains(localIdentifier) else { return }
            ids.append(localIdentifier)
            defaults.set(ids, forKey: Self.key)
        }
    }

    func ids() -> Set<String> {
        Self.lock.withLock { Set(defaults.stringArray(forKey: Self.key) ?? []) }
    }
}
