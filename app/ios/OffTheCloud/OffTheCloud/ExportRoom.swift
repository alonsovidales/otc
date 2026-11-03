// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  ExportRoom.swift
//  OffTheCloud
//
//  Issue #210: every photo or video the sync sends is first copied out of
//  Photos into a temporary file (PhotoKit hands its bytes to a stream), and
//  the upload reads that file a chunk at a time. Until then the copy was
//  mapped into one Data and anything over 1 GB was given up on for good:
//  a few minutes of 4K video never reached the device, and nothing said so.
//  Nothing holds a whole file in memory now; what bounds a copy is the room
//  on the phone. This decides whether a copy may start and stops one that
//  would leave the phone with almost nothing free. An asset without room
//  stays on the retry list (never given up on), and Settings' Uploads says
//  how much room it needs.
//
//  Android sends from MediaStore's stream with no copy, so it has no
//  counterpart.

import Foundation

/// An asset copied out of Photos for the sync, the post composer or Free Up
/// Space. The caller removes `url` when done with it.
struct ExportedAsset: Sendable {
    let url: URL
    let filename: String
    let mime: String
    let size: Int64
}

/// Not enough room on the phone for an asset's copy. `needed` is the free
/// space it takes, its size (or as much of it as was copied before the
/// copy stopped) plus `ExportRoom.floorBytes`.
struct ExportNoRoom: LocalizedError, Equatable {
    let filename: String
    let size: Int64
    let needed: Int64

    /// Shown where a person waits for it (the post composer).
    var errorDescription: String? { S.iosUploadsPhoneFull }
}

/// Room on the phone for the copies under way. The copies of one run go at
/// most `cMaxConcurrentUploads` at a time, and each one whose size is known
/// (PHAssetResource.dataSize, iOS 27) holds that much until it is gone: one
/// that doesn't fit waits for the others, and fails only when it doesn't fit
/// alone - else two 3 GB videos on a phone with 4 GB free would stop each
/// other at every run. Before iOS 27 a size is known only after a copy that
/// stopped for room (`learn`), until the app quits.
final class ExportRoom: @unchecked Sendable {
    static let shared = ExportRoom(free: ExportRoom.freeBytes)

    /// What a copy always leaves free on the phone: iOS warns and starts
    /// failing other apps' writes below about this.
    nonisolated static let floorBytes: Int64 = 256 << 20

    /// How often a copy looks at the room left: every 64 MiB written.
    nonisolated static let checkEvery: Int64 = 64 << 20

    private let lock = NSLock()
    private let free: @Sendable () -> Int64?
    private var held: Int64 = 0
    /// Lower bounds of sizes learnt from copies that stopped for room.
    private var learnt: [String: Int64] = [:]

    init(free: @escaping @Sendable () -> Int64?) {
        self.free = free
    }

    /// The phone's free space for what the user asked for (the system frees
    /// purgeable caches for it), nil when it can't be read.
    nonisolated static func freeBytes() -> Int64? {
        // A new URL each time: resource values are cached on the instance.
        let url = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
        let values = try? url.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey])
        return values?.volumeAvailableCapacityForImportantUsage
    }

    /// Pure: whether a copy of `size` fits with `free` on the phone while
    /// copies holding `held` go on.
    nonisolated static func fits(size: Int64, free: Int64, held: Int64) -> Bool {
        size + held + floorBytes <= free
    }

    /// Pure: whether a copy that has written so far should stop, `free`
    /// being the room left now.
    nonisolated static func mustStop(free: Int64) -> Bool {
        free < floorBytes
    }

    /// The size the room is judged by: the one PhotoKit gives, else one
    /// learnt from a copy cut short. nil: unknown, the copy starts and its
    /// own checks stop it in time.
    func size(of id: String, given: Int64?) -> Int64? {
        if let given, given > 0 { return given }
        return lock.withLock { learnt[id] }
    }

    /// Takes room for a copy of `size` (nothing when unknown), waiting while
    /// other copies hold room it needs. Throws ExportNoRoom when it doesn't
    /// fit alone. `release` the answer once the copy is gone.
    func take(_ size: Int64?, filename: String) async throws -> Int64 {
        guard let size, size > 0 else { return 0 }
        while true {
            let verdict: Int64? = lock.withLock {
                // Free space that can't be read doesn't stop a copy: its own
                // checks can't either, so it goes on as it always did.
                guard let room = free() else { held += size; return size }
                if Self.fits(size: size, free: room, held: held) {
                    held += size
                    return size
                }
                return held == 0 ? -1 : nil
            }
            switch verdict {
            case .some(-1):
                throw ExportNoRoom(filename: filename, size: size, needed: size + Self.floorBytes)
            case .some(let taken):
                return taken
            case .none:
                try await Task.sleep(for: .seconds(1))
            }
        }
    }

    func release(_ taken: Int64) {
        guard taken > 0 else { return }
        lock.withLock { held = max(0, held - taken) }
    }

    /// A copy of `id` stopped for room after writing `written` bytes: it is
    /// at least that big, so the next run doesn't copy it again before the
    /// room is there.
    func learn(_ id: String, written: Int64) {
        lock.withLock {
            learnt[id] = max(learnt[id] ?? 0, written)
            // A handful at most, the app's whole life.
            if learnt.count > 256, let first = learnt.keys.first { learnt[first] = nil }
        }
    }

    /// Whether a copy under way stops now; asked every `checkEvery` bytes
    /// it writes.
    func copyMustStop() -> Bool {
        guard let room = free() else { return false }
        return Self.mustStop(free: room)
    }
}
