// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  LongSync.swift
//  OffTheCloud
//
//  Issue #205: a big photo sync the owner asked for goes on after the app
//  is left. iOS gives a run started in front about half a minute once the
//  app leaves the screen (BackgroundTime) and BGProcessingTasks when it
//  pleases; from iOS 26 Sync All and the first sync (the sync question's
//  Start Sync) ask for a BGContinuedProcessingTask instead, which runs on
//  with the system's own progress for it - "Backing up N photos and
//  videos", a bar, and the system's way to stop it - until the run is over.
//  Below iOS 26 nothing changes. Android's LongSync.kt runs a big sync the
//  same way as a foreground job, with the same threshold and texts.
//

import Foundation
import BackgroundTasks
import Photos
import UIKit

enum LongSync {
    /// ~300 MB, Android's LongSync.THRESHOLD_BYTES: below it a run is over
    /// in minutes, inside the app's time in front.
    nonisolated static let thresholdBytes: Int64 = 300 << 20

    /// Pure: whether a run goes as the continued task. Only one the owner
    /// asked for (Sync All, the first sync): the system wants these to come
    /// from a person's action, and it has to be asked for while the app is
    /// in front; with something to send, and more than the threshold of it
    /// - or, before iOS 27, where PhotoKit can't say how big an asset is
    /// without reading it (`queuedBytes` nil), any.
    nonisolated static func continued(available: Bool, asked: Bool, inFront: Bool, assets: Int, queuedBytes: Int64?) -> Bool {
        guard available, asked, inFront, assets > 0 else { return false }
        guard let queuedBytes else { return true }
        return queuedBytes > thresholdBytes
    }

    /// What `assets` hold, the resource the sync sends of each, counted only
    /// as far as past `limit`; nil before iOS 27 (PHAssetResource.dataSize).
    static func queuedBytes(_ assets: [PHAsset], over limit: Int64 = thresholdBytes) -> Int64? {
        guard #available(iOS 27, *) else { return nil }
        var total: Int64 = 0
        for asset in assets {
            let resources = PHAssetResource.assetResources(for: asset)
            let res = resources.first(where: { $0.type == .photo || $0.type == .fullSizePhoto || $0.type == .video }) ?? resources.first
            total += Int64(res?.dataSize ?? 0)
            if total > limit { break }
        }
        return total
    }

    /// The run's progress, for the system's.
    nonisolated static let progress = BackupProgress()

    /// Whether a continued task holds the run: the app's own background time
    /// running out then doesn't stop it (PhotoSync.runForeground).
    nonisolated static var holdsRun: Bool {
        if #available(iOS 26.0, *) { return LongSyncTask.shared.active }
        return false
    }

    /// The run is over, whatever came of it: a continued task asked for it
    /// completes.
    nonisolated static func runEnded(success: Bool) {
        if #available(iOS 26.0, *) { LongSyncTask.shared.end(success: success) }
    }
}

/// Where a run is, by asset: those dealt with and the share of those on
/// their way the device holds (the uploads' acknowledged bytes), so the bar
/// moves through a single long video too. Thread-safe: three assets go at
/// once. As Android's BackupProgress.
final class BackupProgress: @unchecked Sendable {
    struct Snapshot: Equatable {
        let done: Int
        let total: Int
        let units: Int64
    }

    /// The bar's units an asset counts for.
    nonisolated static let perAsset = 100

    private let lock = NSLock()
    private var total = 0
    private var done = 0
    private var sending: [String: Double] = [:]

    /// A pass begins with `total` assets to go through.
    func begin(total: Int) {
        lock.withLock {
            self.total = total
            done = 0
            sending = [:]
        }
    }

    /// `sent` of `size` bytes of asset `id` are on the device.
    func sending(_ id: String, sent: Int64, size: Int64) {
        guard size > 0 else { return }
        lock.withLock { sending[id] = Double(sent) / Double(size) }
    }

    /// Asset `id` was dealt with, whatever came of it.
    func done(_ id: String) {
        lock.withLock {
            sending[id] = nil
            done += 1
        }
    }

    func snapshot() -> Snapshot {
        lock.withLock { Snapshot(done: min(done, total), total: total, units: Self.units(done: done, total: total, sending: Array(sending.values))) }
    }

    /// Pure: the bar's position for `done` of `total` assets and the shares
    /// of those on their way, never past the end.
    nonisolated static func units(done: Int, total: Int, sending: [Double]) -> Int64 {
        let partial = sending.reduce(Int64(0)) { $0 + Int64(min(max($1, 0), 1) * Double(perAsset)) }
        return min(max(Int64(done) * Int64(perAsset) + partial, 0), Int64(total) * Int64(perAsset))
    }
}

/// The continued processing task itself: asked for by the run that found
/// it big (`begin`, from the main actor while the app is in front), handed
/// the run's progress once a second while it runs, completed when the run
/// ends (`end`) - or, when the owner stops it from the system's progress or
/// the system needs it gone (`expirationHandler`), the run stops at its
/// next step, as at a BG task's end: the watermark before what it didn't
/// finish, the device keeping the uploads cut short for the next run.
@available(iOS 26.0, *)
final class LongSyncTask: @unchecked Sendable {
    static let shared = LongSyncTask()

    /// Info.plist permits `cloud.off-the.OffTheCloud.backup.*`; registered
    /// once, when first asked for (a continued task may be registered after
    /// launch).
    static let identifier = "cloud.off-the.OffTheCloud.backup.sync"

    private enum Phase {
        case idle
        /// Asked for; the system hasn't handed it over yet.
        case asked
        /// Asked for, and the run ended before the system handed it over.
        case endedEarly(success: Bool)
        case running(BGContinuedProcessingTask, updates: Task<Void, Never>)
    }

    private let lock = NSLock()
    private var registered: Bool?
    private var phase = Phase.idle

    /// A task asked for and not over yet.
    var active: Bool {
        lock.withLock {
            if case .idle = phase { return false }
            return true
        }
    }

    /// Asks for the task, to start now or not at all (`.fail`): the run goes
    /// on as before when the system says no.
    @MainActor
    func begin(total: Int) {
        guard register() else { return }
        let free: Bool = lock.withLock {
            guard case .idle = phase else { return false }
            phase = .asked
            return true
        }
        guard free else { return }
        let request = BGContinuedProcessingTaskRequest(identifier: Self.identifier,
                                                       title: S.appShellBackupTitle(count: total),
                                                       subtitle: S.appShellBackupProgress(done: 0, total: total))
        request.strategy = .fail
        do {
            try BGTaskScheduler.shared.submit(request)
            print("LongSyncTask: asked to go on in the background")
        } catch {
            print("LongSyncTask: the system said no:", error)
            lock.withLock {
                if case .asked = phase { phase = .idle }
            }
        }
    }

    /// The run is over: the task completes (now, or as soon as it comes).
    func end(success: Bool) {
        let running: BGContinuedProcessingTask? = lock.withLock {
            switch phase {
            case .asked:
                phase = .endedEarly(success: success)
                return nil
            case .running(let task, _):
                return task
            case .idle, .endedEarly:
                return nil
            }
        }
        if let running { complete(running, success: success) }
    }

    private func register() -> Bool {
        if let registered = lock.withLock({ registered }) { return registered }
        let ok = BGTaskScheduler.shared.register(forTaskWithIdentifier: Self.identifier, using: nil) { [weak self] task in
            guard let self, let continued = task as? BGContinuedProcessingTask else {
                task.setTaskCompleted(success: false)
                return
            }
            self.handle(continued)
        }
        lock.withLock { registered = ok }
        if !ok { print("LongSyncTask: not permitted (Info.plist)") }
        return ok
    }

    private func handle(_ task: BGContinuedProcessingTask) {
        // The owner stopped it from the system's progress, or the system
        // needs it gone: the run stops at its next step, and the task is
        // reported at once.
        task.expirationHandler = { [weak self, weak task] in
            PhotoSync.shared.stopRun()
            if let task { self?.complete(task, success: false) }
        }
        // The bar follows the run; one whose progress stands still too long
        // may be ended by the system, so it is set every second.
        let updates = Task.detached { [weak task] in
            var shown: (done: Int, total: Int)?
            while !Task.isCancelled, let task {
                let s = LongSync.progress.snapshot()
                task.progress.totalUnitCount = Int64(max(s.total, 1) * BackupProgress.perAsset)
                task.progress.completedUnitCount = s.units
                if s.total > 0, shown.map({ $0.done != s.done || $0.total != s.total }) ?? true {
                    task.updateTitle(S.appShellBackupTitle(count: s.total), subtitle: S.appShellBackupProgress(done: s.done, total: s.total))
                    shown = (s.done, s.total)
                }
                try? await Task.sleep(for: .seconds(1))
            }
        }
        let early: Bool? = lock.withLock {
            switch phase {
            case .endedEarly(let success):
                phase = .running(task, updates: updates)
                return success
            case .asked:
                phase = .running(task, updates: updates)
                return nil
            case .idle, .running:
                // Not asked for by this run (it is over): nothing to do.
                phase = .running(task, updates: updates)
                return true
            }
        }
        if let early { complete(task, success: early) }
    }

    /// setTaskCompleted, once per task, and the next run may ask again.
    private func complete(_ task: BGContinuedProcessingTask, success: Bool) {
        let updates: Task<Void, Never>? = lock.withLock {
            guard case .running(let current, let updates) = phase, current === task else { return nil }
            phase = .idle
            return updates
        }
        guard let updates else { return }
        updates.cancel()
        if success { task.progress.completedUnitCount = task.progress.totalUnitCount }
        task.setTaskCompleted(success: success)
        print("LongSyncTask: done (\(success))")
    }
}
